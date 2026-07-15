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
