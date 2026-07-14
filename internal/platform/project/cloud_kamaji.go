package project

// cloud_kamaji.go — the client write surface for kamaji Managed-Kubernetes clusters. A kamaji
// provider has no Keystone tenant, so KUBERNETES_CLUSTER writes bypass the OpenStack
// WriteService entirely (ceph precedent) and go through kamaji.Service: Application CR on the
// management cluster in, status via the sync. Worker VMs land in the customer's own keystone
// tenant of the project's OPENSTACK service (plan D4) — the create leg resolves that binding to
// render the mgmt-side clouds.yaml secret.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/menlocloud/stratos/internal/cloud"
	"github.com/menlocloud/stratos/internal/cloud/client"
	"github.com/menlocloud/stratos/internal/cloud/kamaji"
	"github.com/menlocloud/stratos/internal/cloud/providers"
	"github.com/menlocloud/stratos/internal/platform/externalservice"
	"github.com/menlocloud/stratos/internal/platform/user"
	"github.com/menlocloud/stratos/pkg/httpx"
)

// newClusterID mints the platform identifier every k8s-side object is named by (Application,
// helm release, CAPI Cluster, TCP, DNS). Customer names are display-only (plan §9) — this id is
// what keeps duplicate names, unicode input and the RFC1123/63-char limit out of the system.
func newClusterID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "stc-" + hex.EncodeToString(b)
}

// kamajiCreate handles the KUBERNETES_CLUSTER leg of cloudCreate for a kamaji provider.
func (h *Handler) kamajiCreate(w http.ResponseWriter, r *http.Request, u *user.User, proj *Project, es *externalservice.ExternalService, req providers.CreateRequest) {
	if req.Type != cloud.TypeKubernetesCluster {
		h.fail(w, httpx.BadRequest("A Managed Kubernetes service only provisions KUBERNETES_CLUSTER resources"))
		return
	}
	if h.kamajiFor == nil {
		httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "managed kubernetes is not configured")
		return
	}
	ks, err := h.kamajiFor(es)
	if err != nil {
		httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "managed kubernetes is not available")
		return
	}
	spec, err := kamajiSpecFromData(proj.ID, req.Data)
	if err != nil {
		h.fail(w, httpx.BadRequest(err.Error()))
		return
	}
	osCfg, err := h.kamajiWorkerCloudConfig(r.Context(), proj)
	if err != nil {
		h.fail(w, httpx.BadRequest(err.Error()))
		return
	}
	data, err := ks.CreateCluster(r.Context(), spec, osCfg)
	if err != nil {
		h.fail(w, err)
		return
	}
	now := time.Now().UTC()
	region := proj.ServiceRegion(es.ID)
	if region == "" {
		region = es.KamajiRegion()
	}
	cr := &cloud.CloudResource{
		ServiceID: es.ID, Region: region, ProjectID: proj.ID,
		Type: cloud.TypeKubernetesCluster, ExternalID: spec.ID,
		Data: data, CreatedAt: &now, UpdatedAt: &now,
	}
	if _, err := h.cloud.Insert(r.Context(), cr); err != nil {
		h.fail(w, err)
		return
	}
	h.cloudResourceAudit(u, proj, "CLOUD_RESOURCE_CREATE", "", cr)
	httpx.OK(w, *cr)
}

// kamajiDelete handles the KUBERNETES_CLUSTER leg of cloudDelete: Application delete (ArgoCD's
// resources-finalizer cascades the rendered chart) + cache archive.
func (h *Handler) kamajiDelete(ctx context.Context, es *externalservice.ExternalService, proj *Project, cr *cloud.CloudResource) error {
	if h.kamajiFor == nil {
		return fmt.Errorf("managed kubernetes is not configured")
	}
	ks, err := h.kamajiFor(es)
	if err != nil {
		return err
	}
	if err := ks.DeleteCluster(ctx, proj.ID, cr.ExternalID); err != nil {
		return err
	}
	return h.cloud.DeleteAndArchive(ctx, cr, time.Now().UTC())
}

// kamajiAction serves the per-cluster actions; reports whether it handled the request
// (bucketSettingsAction precedent). Actions:
//   - GET_KUBECONFIG          → {result:{kubeconfig}} — fetched on demand, never stored (plan D5)
//   - UPGRADE {version}       → bumps kubernetesVersion + re-resolves node-group images; the CP
//     rolls first (Kamaji blue/green), node groups rotate off the same values change
//   - SET_NODE_GROUPS {nodeGroups} → replaces the nodeGroups value (add/remove/resize/labels/taints)
func (h *Handler) kamajiAction(w http.ResponseWriter, r *http.Request, proj *Project, es *externalservice.ExternalService, cr *cloud.CloudResource, action string, data map[string]any) bool {
	if !es.IsKamaji() || cr.Type != cloud.TypeKubernetesCluster {
		return false
	}
	if h.kamajiFor == nil {
		httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "managed kubernetes is not configured")
		return true
	}
	ks, err := h.kamajiFor(es)
	if err != nil {
		httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "managed kubernetes is not available")
		return true
	}
	switch action {
	case "GET_KUBECONFIG":
		kc, err := ks.AdminKubeconfig(r.Context(), proj.ID, cr.ExternalID)
		if err != nil {
			h.fail(w, err)
			return true
		}
		httpx.OK(w, map[string]any{"result": map[string]any{"kubeconfig": string(kc)}})
		return true

	case "UPGRADE":
		version, _ := data["version"].(string)
		d := ks.Config().Defaults
		if version == "" {
			h.fail(w, httpx.BadRequest("version is required"))
			return true
		}
		if len(d.Versions) > 0 {
			if _, ok := d.Versions[version]; !ok {
				h.fail(w, httpx.BadRequest("version is not offered by this provider"))
				return true
			}
		}
		err := ks.PatchClusterValues(r.Context(), cr.ExternalID, func(values map[string]any) error {
			values["kubernetesVersion"] = version
			// Node images follow the curated version→image matrix: rotate every group to the new
			// version's image (a MachineDeployment template rotation = CAPI rolling replace).
			if img := d.Versions[version]; img != "" {
				if groups, ok := values["nodeGroups"].([]any); ok {
					for _, raw := range groups {
						if g, ok := raw.(map[string]any); ok {
							g["imageId"] = img
						}
					}
				}
			}
			return nil
		})
		if err != nil {
			h.fail(w, err)
			return true
		}
		httpx.OK(w, map[string]any{"result": "UPGRADING"})
		return true

	case "SET_NODE_GROUPS":
		groups, err := kamajiNodeGroups(data["nodeGroups"])
		if err != nil || len(groups) == 0 {
			h.fail(w, httpx.BadRequest("nodeGroups: at least one valid node group is required"))
			return true
		}
		d := ks.Config().Defaults
		err = ks.PatchClusterValues(r.Context(), cr.ExternalID, func(values map[string]any) error {
			version, _ := values["kubernetesVersion"].(string)
			values["nodeGroups"] = kamaji.NodeGroupValues(d, version, groups)
			return nil
		})
		if err != nil {
			h.fail(w, err)
			return true
		}
		httpx.OK(w, map[string]any{"result": "UPDATING"})
		return true
	}
	return false
}

// kamajiSpecFromData maps the client create body → ClusterSpec. Body shape:
//
//	{name, version, ha, oidc:{issuerUrl,clientId,...}, allowedCidrs:[...],
//	 nodeGroups:[{name, flavorId, count|min/max+autoscale, labels, taints}]}
func kamajiSpecFromData(projectID string, d map[string]any) (kamaji.ClusterSpec, error) {
	spec := kamaji.ClusterSpec{
		ID:          newClusterID(),
		ProjectID:   projectID,
		DisplayName: strAny(d["name"]),
		Version:     strAny(d["version"]),
	}
	if spec.DisplayName == "" {
		return spec, fmt.Errorf("name is required")
	}
	if ha, ok := d["ha"].(bool); ok {
		spec.HA = ha
	}
	if oidc, ok := d["oidc"].(map[string]any); ok {
		spec.OIDC = map[string]string{}
		for k, v := range oidc {
			if s := strAny(v); s != "" {
				spec.OIDC[k] = s
			}
		}
	}
	if cidrs, ok := d["allowedCidrs"].([]any); ok {
		for _, c := range cidrs {
			if s := strAny(c); s != "" {
				spec.AllowedCIDRs = append(spec.AllowedCIDRs, s)
			}
		}
	}
	groups, err := kamajiNodeGroups(d["nodeGroups"])
	if err != nil {
		return spec, err
	}
	spec.NodeGroups = groups
	return spec, nil
}

// kamajiNodeGroups decodes a free-form nodeGroups array via a JSON round-trip into the typed
// slice (kamaji.NodeGroup carries the json tags).
func kamajiNodeGroups(v any) ([]kamaji.NodeGroup, error) {
	if v == nil {
		return nil, fmt.Errorf("nodeGroups is required")
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("nodeGroups: %w", err)
	}
	var groups []kamaji.NodeGroup
	if err := json.Unmarshal(raw, &groups); err != nil {
		return nil, fmt.Errorf("nodeGroups: %w", err)
	}
	return groups, nil
}

// kamajiWorkerCloudConfig resolves the tenant-scoped OpenStack config the cluster's worker VMs
// (CAPO) + in-cluster-adjacent OCCM/cinder-csi run under: the project's OPENSTACK binding,
// scoped to the customer's own keystone tenant (plan D4). The rendered clouds.yaml lives ONLY
// on the management cluster (plan D7).
func (h *Handler) kamajiWorkerCloudConfig(ctx context.Context, p *Project) (client.Config, error) {
	for _, svcID := range p.ServiceIDs() {
		extProjID := p.ExternalProjectID(svcID)
		if extProjID == "" {
			continue
		}
		es, err := h.esSvc.Get(ctx, svcID)
		if err != nil || es == nil || es.Provider() != "openstack" {
			continue
		}
		// App-cred admin auth is keystone-locked to ONE project — it cannot be scoped to the
		// customer tenant (same fail-closed rule as tenantWriteService).
		if es.IsAppCred() {
			continue
		}
		region := p.ServiceRegion(svcID)
		return es.ClientConfigForProject(region, extProjID), nil
	}
	return client.Config{}, fmt.Errorf("Managed Kubernetes needs the project provisioned on an OpenStack service (worker nodes run in your cloud tenant)")
}
