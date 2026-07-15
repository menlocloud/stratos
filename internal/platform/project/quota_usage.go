package project

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/menlocloud/stratos/internal/cloud"
	"github.com/menlocloud/stratos/internal/cloud/client"
	"github.com/menlocloud/stratos/internal/platform/externalservice"
	"github.com/menlocloud/stratos/pkg/httpx"
)

type gpuQuotaUsage struct {
	Limits         map[string]int `json:"limits"`
	Usage          map[string]int `json:"usage"`
	UsageAvailable bool           `json:"usageAvailable"`
}

type projectQuotaUsage struct {
	ServiceID string                    `json:"serviceId"`
	Region    string                    `json:"region"`
	Compute   *client.ComputeQuotaUsage `json:"compute,omitempty"`
	Storage   *client.StorageQuotaUsage `json:"storage,omitempty"`
	GPU       gpuQuotaUsage             `json:"gpu"`
	Warnings  []string                  `json:"warnings"`
}

const quotaServiceReadTimeout = 10 * time.Second

// quotaUsage returns the project's live OpenStack quota usage together with the
// Stratos-managed GPU quota. Compute and storage are independent optional legs:
// a missing service endpoint or a temporary cloud failure must not hide the
// remaining quota data or turn unavailable values into misleading zeroes.
func (h *Handler) quotaUsage(w http.ResponseWriter, r *http.Request) {
	_, proj, ok := h.resolveForMember(w, r)
	if !ok {
		return
	}

	limits := gpuQuotaLimits(proj.Quota)
	usage, gpuErr := h.gpuUsageDetail(r.Context(), proj.ID)
	response := projectQuotaUsage{
		GPU: gpuQuotaUsage{
			Limits:         limits,
			Usage:          usage,
			UsageAvailable: gpuErr == nil,
		},
		Warnings: []string{},
	}
	if gpuErr != nil {
		slog.Warn("project quota usage: GPU cache unavailable", "project", proj.ID, "err", gpuErr)
		response.Warnings = append(response.Warnings, "GPU quota usage is unavailable.")
	}

	serviceID := h.resolveServiceID(r, proj)
	response.ServiceID = serviceID
	if serviceID == "" {
		response.Warnings = append(response.Warnings,
			"Cloud quota usage is unavailable because the project has no cloud service.")
		httpx.OK(w, response)
		return
	}
	if h.esSvc == nil {
		response.Warnings = append(response.Warnings,
			"Cloud quota usage is unavailable because the cloud service registry is not ready.")
		httpx.OK(w, response)
		return
	}
	es, err := h.esSvc.Get(r.Context(), serviceID)
	if err != nil || es == nil {
		slog.Warn("project quota usage: cloud service unavailable", "project", proj.ID,
			"service", serviceID, "err", err)
		response.Warnings = append(response.Warnings,
			"Cloud quota usage is unavailable because the cloud service is not ready.")
		httpx.OK(w, response)
		return
	}
	requestedRegion := strings.TrimSpace(r.Header.Get("x-region-id"))
	region, regionOK := quotaRegionForRequest(es, h.regionFor(proj, serviceID), requestedRegion)
	if !regionOK {
		// An explicitly requested region that the service doesn't have is caller error.
		// No region resolvable at all (legacy service doc, no default) degrades like the
		// other unavailable legs — GPU limits don't need a region.
		if requestedRegion != "" {
			httpx.Err(w, http.StatusBadRequest, http.StatusBadRequest,
				fmt.Sprintf("Region %q is not configured for cloud service %q", requestedRegion, serviceID))
			return
		}
		response.Warnings = append(response.Warnings,
			"Cloud quota usage is unavailable because no region is configured for the cloud service.")
		httpx.OK(w, response)
		return
	}
	response.Region = region

	// Keystone auth gets the same bound as the quota reads: a black-holed identity
	// endpoint must degrade this endpoint into a warnings-partial 200, not hang the
	// handler for the OS TCP timeout. Reauth is per-request-context in gophercloud v2,
	// so cancelling after New does not break the returned client.
	authCtx, cancelAuth := context.WithTimeout(r.Context(), quotaServiceReadTimeout)
	cc, externalProjectID, err := h.quotaReaderClient(authCtx, proj, es, serviceID, region)
	cancelAuth()
	if err != nil {
		slog.Warn("project quota usage: cloud client unavailable", "project", proj.ID,
			"service", serviceID, "err", err)
		response.Warnings = append(response.Warnings,
			"Cloud quota usage is unavailable because the project cloud client is not ready.")
		httpx.OK(w, response)
		return
	}

	var (
		compute    *client.ComputeQuotaUsage
		storage    *client.StorageQuotaUsage
		computeErr error
		storageErr error
		wg         sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(r.Context(), quotaServiceReadTimeout)
		defer cancel()
		compute, computeErr = cc.ComputeQuotaUsage(ctx, externalProjectID)
	}()
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(r.Context(), quotaServiceReadTimeout)
		defer cancel()
		storage, storageErr = cc.StorageQuotaUsage(ctx, externalProjectID)
	}()
	wg.Wait()

	if computeErr != nil {
		slog.Warn("project quota usage: Nova quota unavailable", "project", proj.ID,
			"service", serviceID, "err", computeErr)
		response.Warnings = append(response.Warnings, "Compute quota usage is unavailable.")
	} else {
		response.Compute = compute
	}
	if storageErr != nil {
		slog.Warn("project quota usage: Cinder quota unavailable", "project", proj.ID,
			"service", serviceID, "err", storageErr)
		response.Warnings = append(response.Warnings, "Storage quota usage is unavailable.")
	} else {
		response.Storage = storage
	}

	httpx.OK(w, response)
}

// quotaReaderClient builds a client for quota-set reads only. Password-backed
// providers are scoped to the tenant as usual. Application credentials cannot
// be re-scoped, so they retain their credential scope and pass the tenant's
// external project ID explicitly to Nova/Cinder's admin-capable quota APIs.
// This is intentionally separate from tryTenantClient: no cloud write path is
// broadened to use an application credential against another project.
func (h *Handler) quotaReaderClient(
	ctx context.Context,
	p *Project,
	es *externalservice.ExternalService,
	serviceID string,
	region string,
) (*client.Client, string, error) {
	if es == nil {
		return nil, "", fmt.Errorf("external service %q not found", serviceID)
	}
	if es.IsCephS3() {
		return nil, "", client.ErrNotOpenStack
	}
	externalProjectID := p.ExternalProjectID(serviceID)
	if externalProjectID == "" {
		return nil, "", fmt.Errorf("project has no external project ID for service %q", serviceID)
	}
	cc, err := client.New(ctx, quotaReaderClientConfig(es, region, externalProjectID))
	if err != nil {
		return nil, "", err
	}
	return cc, externalProjectID, nil
}

// quotaRegionForRequest honors the UI's selected region only when it belongs
// to the attached external service. Legacy services without config.regions can
// still use their provisioned/default region, but cannot accept an arbitrary
// caller-supplied region.
func quotaRegionForRequest(es *externalservice.ExternalService, fallback, requested string) (string, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return fallback, fallback != ""
	}
	regions := es.RegionNames()
	if len(regions) == 0 {
		return requested, requested == fallback
	}
	for _, configured := range regions {
		if configured == requested {
			return requested, true
		}
	}
	return "", false
}

func quotaReaderClientConfig(es *externalservice.ExternalService, region, externalProjectID string) client.Config {
	if es.IsAppCred() {
		return es.ClientConfig(region)
	}
	return es.ClientConfigForProject(region, externalProjectID)
}

func gpuQuotaLimits(quota map[string]any) map[string]int {
	out := map[string]int{}
	sources := map[string]string{}
	gpu, ok := quota["gpu"].(map[string]any)
	if !ok {
		return out
	}
	for model, raw := range gpu {
		trimmed := strings.TrimSpace(model)
		canonical := cloud.NormalizeGPUAlias(trimmed)
		if trimmed == "*" {
			canonical = "*"
		}
		if canonical == "" {
			continue
		}
		value, ok := toDecAny(raw)
		if !ok || value.IsNegative() || !value.Equal(value.Truncate(0)) {
			continue
		}
		previous, exists := sources[canonical]
		canonicalSource := trimmed == canonical
		previousCanonical := previous == canonical
		if exists && !canonicalSource && (previousCanonical || trimmed >= previous) {
			continue
		}
		out[canonical] = int(value.IntPart())
		sources[canonical] = trimmed
	}
	return out
}
