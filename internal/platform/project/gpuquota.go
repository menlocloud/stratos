package project

// gpuquota.go — the tier-1 GPU quota gate. Per-project limits live on the project doc as
// quota.gpu = {"<model>": n, "*": n} (admin-managed via PUT /admin/project/{id}/quota;
// model names use the shared alias vocabulary, e.g. "a100-80gb"). Enforcement happens HERE,
// at the Stratos create/resize gate — nothing is pushed to OpenStack (nova legacy quotas
// have no GPU class). Horizon-direct usage on imported projects bypasses this gate (tier 2,
// operator-managed).

import (
	"context"
	"fmt"
	"net/http"

	"github.com/menlocloud/stratos/internal/cloud"
	"github.com/menlocloud/stratos/internal/cloud/kamaji"
	"github.com/menlocloud/stratos/pkg/httpx"
)

// gpuLimitFor resolves the project limit for a GPU model: the exact model key, else the
// "*" wildcard, else unlimited (limited=false).
func gpuLimitFor(quota map[string]any, model string) (limit int, limited bool) {
	limits := gpuQuotaLimits(quota)
	if value, ok := limits[cloud.NormalizeGPUAlias(model)]; ok {
		return value, true
	}
	if value, ok := limits["*"]; ok {
		return value, true
	}
	return 0, false
}

// gpuUsageDetail sums the project's cached servers' GPU devices per model (DELETED excluded —
// everything else, including ERROR, still holds its devices until the server is gone). Keeping
// the repository error lets read surfaces warn instead of presenting a misleading zero; the
// create/resize gate intentionally retains its existing fail-open behavior through gpuUsage.
func (h *Handler) gpuUsageDetail(ctx context.Context, projectID string) (map[string]int, error) {
	if h.cloud == nil {
		return map[string]int{}, fmt.Errorf("GPU usage repository is unavailable")
	}
	return h.cloud.GPUUsageByProject(ctx, projectID)
}

func (h *Handler) gpuUsage(ctx context.Context, projectID string) map[string]int {
	usage, _ := h.gpuUsageDetail(ctx, projectID)
	return usage
}

// enforceGPUQuota rejects a server create/resize that would push the project's GPU usage
// over its quota (409). replacedFlavor is the flavor being resized away — its devices free
// up before the comparison. Fail-open by design: no quota config, a non-GPU target flavor,
// or an unresolvable flavor/cloud never blocks (the gate must not take down CPU workloads
// on a cloud hiccup — capacity truth stays with placement).
func (h *Handler) enforceGPUQuota(ctx context.Context, proj *Project, svcID, flavorID string, replacedFlavor map[string]any) error {
	if len(proj.Quota) == 0 || flavorID == "" {
		return nil
	}
	cc, ok := h.tryTenantClient(ctx, proj, svcID)
	if !ok {
		return nil
	}
	fl, err := cc.GetFlavor(ctx, flavorID)
	if err != nil {
		return nil
	}
	model, want := cloud.GPUFromFlavor(fl["extra_specs"])
	if want == 0 {
		return nil
	}
	limit, limited := gpuLimitFor(proj.Quota, model)
	if !limited {
		return nil
	}
	used := h.gpuUsage(ctx, proj.ID)[model]
	if replacedFlavor != nil {
		es, _ := replacedFlavor["extra_specs"].(map[string]any)
		if m, n := cloud.GPUFromFlavor(es); m == model {
			used -= n
			if used < 0 {
				used = 0
			}
		}
	}
	if used+want > limit {
		return httpx.NewError(http.StatusConflict, http.StatusConflict, fmt.Sprintf(
			"GPU quota exceeded for %s: %d in use + %d requested exceeds the project limit of %d",
			model, used, want, limit))
	}
	return nil
}

// enforceGPUQuotaForNodeGroups is the same tier-1 gate for kamaji node groups: without it, a
// user blocked from creating a GPU server could still request GPU worker VMs through a node
// group and have CAPO create them in the tenant, bypassing the quota entirely. Demand per GPU
// model = Σ nodes × flavor devices, where nodes = count, or MAX for an autoscale group (the
// autoscaler can reach max with no further gate). prevGroups (the cluster's current groups, on
// a node-group edit) subtract out — their workers are already counted in gpuUsage once the
// server sync picks them up. osSvcID is the project's OPENSTACK binding (where the worker VMs
// and flavors live), not the kamaji service. Fail-open like enforceGPUQuota.
func (h *Handler) enforceGPUQuotaForNodeGroups(ctx context.Context, proj *Project, osSvcID string, groups, prevGroups []kamaji.NodeGroup) error {
	if len(proj.Quota) == 0 || len(groups) == 0 || osSvcID == "" {
		return nil
	}
	cc, ok := h.tryTenantClient(ctx, proj, osSvcID)
	if !ok {
		return nil
	}
	flavorGPU := map[string]struct {
		model string
		n     int
	}{}
	gpuOf := func(flavorID string) (string, int) {
		if flavorID == "" {
			return "", 0
		}
		if g, ok := flavorGPU[flavorID]; ok {
			return g.model, g.n
		}
		model, n := "", 0
		if fl, err := cc.GetFlavor(ctx, flavorID); err == nil {
			model, n = cloud.GPUFromFlavor(fl["extra_specs"])
		}
		flavorGPU[flavorID] = struct {
			model string
			n     int
		}{model, n}
		return model, n
	}
	nodes := func(ng kamaji.NodeGroup) int {
		if ng.Autoscale {
			return ng.Max
		}
		return ng.Count
	}
	demand := map[string]int{}
	for _, ng := range groups {
		if model, n := gpuOf(ng.FlavorID); n > 0 {
			demand[model] += n * nodes(ng)
		}
	}
	for _, ng := range prevGroups {
		if model, n := gpuOf(ng.FlavorID); n > 0 {
			demand[model] -= n * nodes(ng)
		}
	}
	var usage map[string]int
	for model, want := range demand {
		if want <= 0 {
			continue
		}
		limit, limited := gpuLimitFor(proj.Quota, model)
		if !limited {
			continue
		}
		if usage == nil {
			usage = h.gpuUsage(ctx, proj.ID)
		}
		if usage[model]+want > limit {
			return httpx.NewError(http.StatusConflict, http.StatusConflict, fmt.Sprintf(
				"GPU quota exceeded for %s: %d in use + %d requested by node groups exceeds the project limit of %d",
				model, usage[model], want, limit))
		}
	}
	return nil
}
