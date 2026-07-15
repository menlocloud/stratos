package project

// gpucapacity.go — the client-facing GPU capacity read. Cluster GPU capacity (available/total
// per model) comes from Placement, an ADMIN-scoped read, so it is fetched with the service admin
// client (like the admin gpu-info surface) rather than the tenant client, and gated on the
// per-project GpuCapacityVisible flag (admin-managed). The result is cluster-global, so it is
// cached briefly per (service, region) and shared across every project/user.

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/menlocloud/stratos/internal/cloud"
	"github.com/menlocloud/stratos/internal/cloud/client"
	"github.com/menlocloud/stratos/internal/platform/externalservice"
	"github.com/menlocloud/stratos/pkg/httpx"
)

// gpuCapacityTTL bounds how often the client-facing read hits Placement (1 + 2N calls per fetch);
// capacity is cluster-global so one fetch per (service, region) serves every project in the window.
const gpuCapacityTTL = 30 * time.Second

type gpuCapacityEntry struct {
	Model     string `json:"model"`
	Available int    `json:"available"`
	Total     int    `json:"total"`
}

type gpuCapacityResponse struct {
	Visible  bool               `json:"visible"`
	Region   string             `json:"region,omitempty"`
	Capacity []gpuCapacityEntry `json:"capacity"`
	Warnings []string           `json:"warnings"`
}

type gpuCapacityCached struct {
	at   time.Time
	caps []gpuCapacityEntry
}

var gpuCapacityCache sync.Map // "serviceID|region" → gpuCapacityCached

// projectGPUCapacity handles GET /project/{id}/gpu-capacity: the region's cluster GPU capacity
// (available/total per model) when the operator enabled it for this project; otherwise a cheap
// {visible:false} with no cloud call. Member-gated. Cloud failures degrade to a warnings-partial
// 200 (never a 5xx) so the dashboard just hides the panel.
func (h *Handler) projectGPUCapacity(w http.ResponseWriter, r *http.Request) {
	_, proj, ok := h.resolveForMember(w, r)
	if !ok {
		return
	}
	resp := gpuCapacityResponse{Visible: proj.GpuCapacityVisible, Capacity: []gpuCapacityEntry{}, Warnings: []string{}}
	if !proj.GpuCapacityVisible {
		httpx.OK(w, resp)
		return
	}
	serviceID := h.resolveServiceID(r, proj)
	if serviceID == "" || h.esSvc == nil {
		resp.Warnings = append(resp.Warnings, "GPU capacity is unavailable for this project.")
		httpx.OK(w, resp)
		return
	}
	es, err := h.esSvc.Get(r.Context(), serviceID)
	if err != nil || es == nil {
		resp.Warnings = append(resp.Warnings, "GPU capacity is unavailable because the cloud service is not ready.")
		httpx.OK(w, resp)
		return
	}
	region, regionOK := quotaRegionForRequest(es, h.regionFor(proj, serviceID), strings.TrimSpace(r.Header.Get("x-region-id")))
	if !regionOK {
		resp.Warnings = append(resp.Warnings, "GPU capacity is unavailable because no region is configured for the cloud service.")
		httpx.OK(w, resp)
		return
	}
	resp.Region = region
	caps, err := gpuCapacityForService(r.Context(), es, serviceID, region)
	if err != nil {
		resp.Warnings = append(resp.Warnings, "GPU capacity is currently unavailable.")
		httpx.OK(w, resp)
		return
	}
	resp.Capacity = caps
	httpx.OK(w, resp)
}

// gpuCapacityForService returns the region's per-model GPU capacity, served from a short-lived
// cache. Placement is admin-scoped, so this uses the service admin client (es.ClientConfig), not
// the tenant client; the auth + reads are bounded so a black-holed cloud cannot hang the request.
func gpuCapacityForService(ctx context.Context, es *externalservice.ExternalService, serviceID, region string) ([]gpuCapacityEntry, error) {
	key := serviceID + "|" + region
	if v, ok := gpuCapacityCache.Load(key); ok {
		if entry := v.(gpuCapacityCached); time.Since(entry.at) < gpuCapacityTTL {
			return entry.caps, nil
		}
	}
	fetchCtx, cancel := context.WithTimeout(ctx, quotaServiceReadTimeout)
	defer cancel()
	cc, err := client.New(fetchCtx, es.ClientConfig(region))
	if err != nil {
		return nil, err
	}
	gpus, err := cc.GPUInfo(fetchCtx)
	if err != nil {
		return nil, err
	}
	caps := gpuCapacityFromInfo(gpus)
	gpuCapacityCache.Store(key, gpuCapacityCached{at: time.Now(), caps: caps})
	return caps, nil
}

// gpuCapacityFromInfo maps Placement per-model device counts to the client shape: canonical model
// alias, available = total − inUse (clamped at 0 so a transient over-allocation never shows negative).
func gpuCapacityFromInfo(gpus []client.GPUCapacity) []gpuCapacityEntry {
	caps := make([]gpuCapacityEntry, 0, len(gpus))
	for _, g := range gpus {
		available := g.Total - g.InUse
		if available < 0 {
			available = 0
		}
		caps = append(caps, gpuCapacityEntry{Model: cloud.NormalizeGPUAlias(g.Name), Available: available, Total: g.Total})
	}
	return caps
}
