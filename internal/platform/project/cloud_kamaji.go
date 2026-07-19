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
	"log/slog"
	"net/http"
	"slices"
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
	osCfg, osSvcID, err := h.kamajiWorkerCloudConfig(r.Context(), proj)
	if err != nil {
		h.fail(w, httpx.BadRequest(err.Error()))
		return
	}
	// Tier-1 GPU quota gate, same as the server create path (#63 parity): GPU worker nodes must
	// not slip past a limit that blocks the equivalent instances.
	if err := h.enforceGPUQuotaForNodeGroups(r.Context(), proj, osSvcID, spec.NodeGroups, nil); err != nil {
		h.fail(w, err)
		return
	}
	// Mint the per-cluster application credential (plan D4): scoped to the customer's tenant, so
	// the credential CAPO/OCCM hold on the management cluster is bounded to that one project.
	// Fail-open to the admin-scoped-to-tenant auth (CloudsYAML fallback) — a keystone hiccup must
	// not block cluster creation; the deferral is logged.
	var mintedBy *client.Client
	if cc, cerr := client.New(r.Context(), osCfg); cerr == nil {
		if userID, credID, secret, merr := cc.CreateAppCredential(r.Context(),
			"stratos-"+spec.ID, "stratos managed-k8s cluster "+spec.ID+" (project "+proj.ID+")"); merr == nil {
			osCfg.AppCredID, osCfg.AppCredSecret = credID, secret
			spec.AppCredID, spec.AppCredUserID, spec.AppCredServiceID = credID, userID, osSvcID
			mintedBy = cc
		} else {
			slog.Warn("kamaji: appcred mint failed — falling back to admin-scoped auth", "cluster", spec.ID, "err", merr)
		}
	} else {
		slog.Warn("kamaji: tenant client for appcred mint failed — falling back to admin-scoped auth", "cluster", spec.ID, "err", cerr)
	}
	data, err := ks.CreateCluster(r.Context(), spec, osCfg)
	if err != nil {
		// The secret carrying the revocation record was never applied — revoke the fresh appcred
		// now rather than leaking a live credential with no inventory pointing at it. The revoke
		// must survive the client hanging up (a canceled request is a likely reason the create
		// failed in the first place), hence WithoutCancel.
		if mintedBy != nil && spec.AppCredID != "" {
			rctx := context.WithoutCancel(r.Context())
			if rerr := mintedBy.DeleteAppCredential(rctx, spec.AppCredUserID, spec.AppCredID); rerr != nil {
				slog.Error("kamaji: revoke appcred after failed create", "cluster", spec.ID, "appcred", spec.AppCredID, "err", rerr)
			}
		}
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
//     rolls first (Kamaji blue/green), node groups rotate off the same values change; guarded to
//     one minor at a time, never backwards
//   - SET_NODE_GROUPS {nodeGroups} → replaces the nodeGroups value (add/remove/resize/labels/taints)
//   - SET_OIDC {oidc}         → replaces the customer OIDC block; empty issuerUrl disables
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
			current, _ := values["kubernetesVersion"].(string)
			// One minor at a time, never backwards (plan §3.4) — validated against the live
			// values, not the cache, so a stale sync can't let a bad jump through.
			if err := kamaji.ValidateUpgradePath(current, version); err != nil {
				return httpx.BadRequest(err.Error())
			}
			values["kubernetesVersion"] = version
			// Node images follow the curated version→image matrix: rotate every group to the new
			// version's image (a MachineDeployment template rotation = CAPI rolling replace).
			if img := d.Versions[version]; img != "" {
				if groups, ok := values["nodeGroups"].([]any); ok {
					for _, raw := range groups {
						if g, ok := raw.(map[string]any); ok {
							g["machineImageId"] = img
						}
					}
				}
			}
			// Keep the autoscaler on the new minor (chart constraint: tag minor == cluster minor).
			if maj, min, _, verr := kamaji.ParseVersion(version); verr == nil {
				values["autoscaler"] = map[string]any{
					"image": map[string]any{"tag": fmt.Sprintf("v%d.%d.0", maj, min)},
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
		if err := validateNodeGroupShapes(groups); err != nil {
			h.fail(w, httpx.BadRequest(err.Error()))
			return true
		}
		// GPU gate on the DELTA vs the cluster's current groups (their workers are already in
		// the usage count once synced as servers). Fail-open when the OpenStack binding is gone.
		if osSvcID := h.kamajiOpenStackServiceID(r.Context(), proj); osSvcID != "" {
			if err := h.enforceGPUQuotaForNodeGroups(r.Context(), proj, osSvcID, groups, nodeGroupsFromCache(cr)); err != nil {
				h.fail(w, err)
				return true
			}
		}
		err = ks.PatchClusterValues(r.Context(), cr.ExternalID, func(values map[string]any) error {
			version, _ := values["kubernetesVersion"].(string)
			prevFlavor, prevImage := prevGroupIndex(values)
			// Flavor allowlist applies to NEW or flavor-CHANGED groups only — an admin narrowing
			// the allowlist must not brick resizes of groups that already run a since-removed
			// flavor.
			if len(d.Flavors) > 0 {
				for _, ng := range groups {
					if prevFlavor[ng.Name] == ng.FlavorID {
						continue
					}
					if !slices.Contains(d.Flavors, ng.FlavorID) {
						return httpx.BadRequest(fmt.Sprintf("node group %q: flavor %q is not offered by this provider", ng.Name, ng.FlavorID))
					}
				}
			}
			rendered := kamaji.NodeGroupValues(d, version, groups)
			// The version→image matrix may no longer carry this cluster's (older) version — keep
			// each untouched group's existing image rather than rendering imageId:"" (which would
			// roll every MachineDeployment onto a broken template).
			for _, raw := range rendered {
				g, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if img, _ := g["machineImageId"].(string); img == "" {
					name, _ := g["name"].(string)
					if prev := prevImage[name]; prev != "" {
						g["machineImageId"] = prev
					} else {
						return httpx.BadRequest(fmt.Sprintf("node group %q: no image for version %s — set an imageId or update the provider's version matrix", name, version))
					}
				}
			}
			values["nodeGroups"] = rendered
			return nil
		})
		if err != nil {
			h.fail(w, err)
			return true
		}
		httpx.OK(w, map[string]any{"result": "UPDATING"})
		return true

	case "SET_OIDC":
		// Post-create OIDC (re)configuration — same chart block the create wizard sets; an empty
		// issuerUrl disables OIDC on the apiserver.
		oidc := map[string]string{}
		if m, ok := data["oidc"].(map[string]any); ok {
			for k, v := range m {
				if s := strAny(v); s != "" {
					oidc[k] = s
				}
			}
		}
		err := ks.PatchClusterValues(r.Context(), cr.ExternalID, func(values map[string]any) error {
			// signingAlgs is operator/advanced config the UI form doesn't carry — preserve the
			// existing value unless the request explicitly sets it.
			if _, sent := oidc["signingAlgs"]; !sent {
				if prev := digAnyStr(values, "oidc", "signingAlgs"); prev != "" {
					oidc["signingAlgs"] = prev
				}
			}
			if block := kamaji.OIDCValues(oidc); block != nil {
				values["oidc"] = block
			} else {
				delete(values, "oidc")
			}
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

// validateNodeGroupShapes re-runs the create-time node-group SHAPE rules for SET_NODE_GROUPS
// (name/flavor required, count/min/max sanity) — allowlist enforcement happens inside the
// values patch where the cluster's current groups are known.
func validateNodeGroupShapes(groups []kamaji.NodeGroup) error {
	spec := kamaji.ClusterSpec{ID: "x", ProjectID: "x", Version: "x", NodeGroups: groups}
	return spec.Validate(kamaji.ClusterDefaults{})
}

// prevGroupIndex indexes the live values' nodeGroups by name → flavor / imageId.
func prevGroupIndex(values map[string]any) (flavor, image map[string]string) {
	flavor, image = map[string]string{}, map[string]string{}
	raw, _ := values["nodeGroups"].([]any)
	for _, r := range raw {
		g, ok := r.(map[string]any)
		if !ok {
			continue
		}
		name, _ := g["name"].(string)
		if name == "" {
			continue
		}
		flavor[name], _ = g["machineFlavor"].(string)
		image[name], _ = g["machineImageId"].(string)
	}
	return flavor, image
}

// digAnyStr reads a nested string out of a free-form map (nil-safe).
func digAnyStr(m map[string]any, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	s, _ := cur.(string)
	return s
}

// nodeGroupsFromCache rebuilds the cluster's current node groups off the sync cache — the
// baseline the GPU-quota delta subtracts. It deliberately reports each group at its LIVE
// replica count when the sync has one (falling back to the declared count / autoscale min):
// gpuUsage counts actual worker VMs, so subtracting an autoscale group at max would credit
// headroom the project never held and let an edit slip past the limit.
func nodeGroupsFromCache(cr *cloud.CloudResource) []kamaji.NodeGroup {
	cl, _ := cr.Data["cluster"].(map[string]any)
	raw, _ := cl["node_groups"].([]any)
	out := make([]kamaji.NodeGroup, 0, len(raw))
	for _, r := range raw {
		g, ok := r.(map[string]any)
		if !ok {
			continue
		}
		ng := kamaji.NodeGroup{FlavorID: strAny(g["flavor_id"])}
		autoscale, _ := g["autoscale"].(bool)
		switch {
		case g["replicas"] != nil:
			ng.Count = intAny(g["replicas"])
		case autoscale:
			ng.Count = intAny(g["min"])
		default:
			ng.Count = intAny(g["count"])
		}
		out = append(out, ng)
	}
	return out
}

// kamajiOpenStackServiceID resolves the project's OPENSTACK binding id (worker-VM home) — the
// service GPU quota and flavors resolve against. Empty when the project has none.
func (h *Handler) kamajiOpenStackServiceID(ctx context.Context, p *Project) string {
	_, svcID, err := h.kamajiWorkerCloudConfig(ctx, p)
	if err != nil {
		return ""
	}
	return svcID
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
func (h *Handler) kamajiWorkerCloudConfig(ctx context.Context, p *Project) (client.Config, string, error) {
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
		return es.ClientConfigForProject(region, extProjID), svcID, nil
	}
	return client.Config{}, "", fmt.Errorf("Managed Kubernetes needs the project provisioned on an OpenStack service (worker nodes run in your cloud tenant)")
}

// kamajiCredRevoker builds the orphan-finalize revocation resolver: the appcred is revoked on
// the OpenStack service recorded at mint time (secret annotation), falling back to the
// project's current OpenStack binding for legacy records. FAIL-CLOSED — any resolution failure
// keeps the secret (the only revocation record) for a later sweep pass.
func (h *Handler) kamajiCredRevoker(ctx context.Context, p *Project) kamaji.RevokeCredResolver {
	return func(ctx context.Context, osServiceID, userID, credID string) error {
		svcID := osServiceID
		if svcID == "" {
			var err error
			if _, svcID, err = h.kamajiWorkerCloudConfig(ctx, p); err != nil {
				return fmt.Errorf("appcred %s: no minting service recorded and no OpenStack binding: %w", credID, err)
			}
		}
		es, err := h.esSvc.Get(ctx, svcID)
		if err != nil {
			return err
		}
		if es == nil {
			return fmt.Errorf("appcred %s: minting service %s no longer exists — revoke manually", credID, svcID)
		}
		cc, err := client.New(ctx, es.ClientConfig(p.ServiceRegion(svcID)))
		if err != nil {
			return err
		}
		return cc.DeleteAppCredential(ctx, userID, credID)
	}
}
