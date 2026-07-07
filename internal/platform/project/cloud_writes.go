package project

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/menlocloud/stratos/internal/cloud"
	"github.com/menlocloud/stratos/internal/cloud/client"
	"github.com/menlocloud/stratos/internal/cloud/providers"
	"github.com/menlocloud/stratos/internal/platform/rbac"
	"github.com/menlocloud/stratos/pkg/httpx"
)

// cloud_writes.go = the client write surface (client cloud-resource create/delete),
// project-membership + project:cloud_resource:manage gated. The dispatch + OpenStack write +
// cache live in providers.WriteService; this layer resolves the project's service + region and
// builds the per-request WriteService over the live CloudClient.

// resolveServiceID picks the externalService for a cloud write: the FE's `x-service-id` header (or
// `?serviceId=`) when the project is attached to it, else the project's first attached service.
// "" = the project has no cloud service (→ 400, the no-service guard).
func (h *Handler) resolveServiceID(r *http.Request, p *Project) string {
	if hd := r.Header.Get("x-service-id"); hd != "" && p.HasService(hd) {
		return hd
	}
	if q := r.URL.Query().Get("serviceId"); q != "" && p.HasService(q) {
		return q
	}
	if ids := p.ServiceIDs(); len(ids) > 0 {
		return ids[0]
	}
	return ""
}

// tenantWriteService builds a WriteService over a cloud client scoped to the project's provisioned
// OpenStack tenant (externalProjectId) using the external service's admin creds — so resources are
// created INSIDE the customer's tenant, not the admin project. region is the provisioned region.
func (h *Handler) tenantWriteService(ctx context.Context, w http.ResponseWriter, p *Project, svcID string) (*providers.WriteService, string, bool) {
	extProjID := p.ExternalProjectID(svcID)
	if extProjID == "" {
		h.fail(w, httpx.BadRequest("Project is not provisioned on the cloud service"))
		return nil, "", false
	}
	es, err := h.esSvc.Get(ctx, svcID)
	if err != nil || es == nil {
		httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "cloud service not available")
		return nil, "", false
	}
	region := p.ServiceRegion(svcID)
	if region == "" {
		region = h.cloudRegion
	}
	cc, err := client.New(ctx, es.ClientConfigForProject(region, extProjID))
	if err != nil {
		httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "cloud client not ready")
		return nil, "", false
	}
	return providers.NewWriteService(cc, h.cloud), region, true
}

// cloudResourceList handles POST /api/v1/project/{id}/resource?type=
// → the project's cached cloud resources, filtered by type. Project-membership-gated (read).
func (h *Handler) cloudResourceList(w http.ResponseWriter, r *http.Request) {
	u, ok := h.principal(w, r)
	if !ok {
		return
	}
	proj, err := h.svc.GetProject(r.Context(), u.Sub, chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	typ := r.URL.Query().Get("type")
	// Live-listed types (read straight from the project's tenant, like the synced cache — the
	// os-notification real-time sync is deferred). IMAGE feeds the wizard + the per-server Snapshots tab;
	// PORT/FLOATING_IP/SECURITY_GROUP feed the server-detail Networking + Security-Groups tabs (the
	// cache never holds a server's auto-created port/secgroup, so these must be live).
	switch typ {
	case cloud.TypeImage:
		h.listImages(w, r, proj)
		return
	case cloud.TypePort:
		h.listPorts(w, r, proj)
		return
	case cloud.TypeFloatingIP:
		h.listFloatingIPs(w, r, proj)
		return
	case cloud.TypeSecurityGroup:
		h.listSecurityGroups(w, r, proj)
		return
	}
	resources, err := h.cloud.FindAllByProjectID(r.Context(), proj.ID)
	if err != nil {
		h.fail(w, err)
		return
	}
	out := make([]cloud.CloudResource, 0, len(resources))
	for _, cr := range resources {
		if typ != "" && cr.Type != typ {
			continue
		}
		out = append(out, cr)
	}
	// SERVER rows bind data.server.flavor.{vcpus,ram} → without specs the list shows
	// "undefined CPU, NaN GB RAM". Best-effort enrich each cached server's flavor live.
	if typ == cloud.TypeServer && len(out) > 0 {
		if cc, ok := h.tryTenantClient(r.Context(), proj, h.resolveServiceID(r, proj)); ok {
			for i := range out {
				if srv, isMap := out[i].Data["server"].(map[string]any); isMap {
					enrichServerFlavor(r.Context(), cc, srv)
				}
			}
		}
	}
	httpx.List(w, out)
}

// listPorts live-lists the project's Neutron ports as PORT CloudResources.
// When `?deviceId=` is set the list is scoped to that server's interfaces (the server-detail
// Networking tab). Empty list if the cloud is unreachable.
func (h *Handler) listPorts(w http.ResponseWriter, r *http.Request, proj *Project) {
	svcID := h.resolveServiceID(r, proj)
	region := h.regionFor(proj, svcID)
	out := []cloud.CloudResource{}
	if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
		if ports, err := cc.ListPortsFull(r.Context(), r.URL.Query().Get("deviceId")); err == nil {
			for _, p := range ports {
				id, _ := p["id"].(string)
				out = append(out, cloud.CloudResource{
					ID: id, Type: cloud.TypePort, ExternalID: id, Region: region,
					ServiceID: svcID, ProjectID: proj.ID, Data: map[string]any{"port": p},
					CreatedAt: liveCreatedAt(p),
				})
			}
		}
	}
	httpx.List(w, out)
}

// listFloatingIPs live-lists the project's Neutron floating IPs as FLOATING_IP CloudResources.
// `?deviceId=<server>` scopes to the FIPs assigned to that server's ports (the Networking tab's
// assigned list); empty deviceId returns all FIPs (the "Assign Floating IP" picker).
func (h *Handler) listFloatingIPs(w http.ResponseWriter, r *http.Request, proj *Project) {
	svcID := h.resolveServiceID(r, proj)
	region := h.regionFor(proj, svcID)
	out := []cloud.CloudResource{}
	if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
		deviceID := r.URL.Query().Get("deviceId")
		serverPorts := map[string]bool{}
		if deviceID != "" {
			if ports, err := cc.ListPortsFull(r.Context(), deviceID); err == nil {
				for _, p := range ports {
					if id, _ := p["id"].(string); id != "" {
						serverPorts[id] = true
					}
				}
			}
		}
		if fips, err := cc.ListFloatingIPsFull(r.Context()); err == nil {
			for _, f := range fips {
				if deviceID != "" {
					pid, _ := f["port_id"].(string)
					if pid == "" || !serverPorts[pid] {
						continue
					}
				}
				id, _ := f["id"].(string)
				out = append(out, cloud.CloudResource{
					ID: id, Type: cloud.TypeFloatingIP, ExternalID: id, Region: region,
					ServiceID: svcID, ProjectID: proj.ID, Data: map[string]any{"floatingIp": f},
					CreatedAt: liveCreatedAt(f),
				})
			}
		}
	}
	httpx.List(w, out)
}

// listSecurityGroups live-lists the project's Neutron security groups as SECURITY_GROUP
// CloudResources (the Networking/Security-Groups selection + management source).
func (h *Handler) listSecurityGroups(w http.ResponseWriter, r *http.Request, proj *Project) {
	svcID := h.resolveServiceID(r, proj)
	region := h.regionFor(proj, svcID)
	out := []cloud.CloudResource{}
	if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
		if sgs, err := cc.ListSecurityGroups(r.Context()); err == nil {
			for _, sg := range sgs {
				id, _ := sg["id"].(string)
				out = append(out, cloud.CloudResource{
					ID: id, Type: cloud.TypeSecurityGroup, ExternalID: id, Region: region,
					ServiceID: svcID, ProjectID: proj.ID, Data: map[string]any{"securityGroup": sg},
					CreatedAt: liveCreatedAt(sg),
				})
			}
		}
	}
	httpx.List(w, out)
}

// liveCreatedAt maps the live object's own created_at (neutron/glance RFC3339) onto the DTO —
// the live-list must carry the cloud's creation time or the UI Created-At column renders
// "Invalid date".
func liveCreatedAt(obj map[string]any) *time.Time {
	s, _ := obj["created_at"].(string)
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05"} { // neutron omits the Z on some builds
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

// resourceOwnedBy is the project-scoped ownership gate (§5/§7): a cloudResource is usable by a
// request only when it exists AND belongs to the acting project. A nil row (a raw/uncached id) or a
// row owned by another project is rejected, so a read/mutate/delete never runs against an id with
// no project binding.
func resourceOwnedBy(cr *cloud.CloudResource, projectID string) bool {
	return cr != nil && cr.ProjectID == projectID
}

// ownedResource loads a cloudResource cache doc by id and confirms it belongs to proj. ok=false for
// a miss, a non-cache raw id, or another project's resource — the caller 404s instead of acting on
// a raw externalId.
func (h *Handler) ownedResource(ctx context.Context, resourceID string, proj *Project) (*cloud.CloudResource, bool) {
	cr, _ := h.cloud.FindByID(ctx, resourceID)
	if !resourceOwnedBy(cr, proj.ID) {
		return nil, false
	}
	return cr, true
}

// imageVisibleTo is the §27 glance-image filter, applied in EVERY listImages branch: an image is
// visible only when it is owned by the project's tenant (ownerID) AND — when scoped to a server's
// snapshots (assoc) — stamped with that instance_uuid. The owner check must never be skipped in the
// assoc branch, or a caller could read another tenant's snapshots by naming their instance uuid.
func imageVisibleTo(im map[string]any, ownerID, assoc string) bool {
	if ownerID != "" {
		if o, _ := im["owner"].(string); o != ownerID {
			return false
		}
	}
	if assoc != "" {
		iu, _ := im["instance_uuid"].(string)
		if iu != assoc {
			return false
		}
	}
	return true
}

// lbChildIDs is the set of child ids that belong to one Octavia LB — used to reject a body child id
// (§30) that points at another LB's listener/pool/member/monitor.
type lbChildIDs struct {
	listeners map[string]bool
	pools     map[string]bool
	members   map[string]bool // member ids across this LB's pools
	monitors  map[string]bool // health-monitor ids referenced by this LB's pools
}

// lbChildSets parses live listener + pool listings (each pool carrying its members + healthmonitor_id)
// into the child-id sets. Pure so the §30 guard is unit-testable without a live Octavia.
func lbChildSets(listeners, pools []map[string]any) lbChildIDs {
	ids := lbChildIDs{
		listeners: map[string]bool{}, pools: map[string]bool{},
		members: map[string]bool{}, monitors: map[string]bool{},
	}
	for _, l := range listeners {
		if id, _ := l["id"].(string); id != "" {
			ids.listeners[id] = true
		}
	}
	for _, p := range pools {
		if id, _ := p["id"].(string); id != "" {
			ids.pools[id] = true
		}
		if hm, _ := p["healthmonitor_id"].(string); hm != "" {
			ids.monitors[hm] = true
		}
		if ms, _ := p["members"].([]map[string]any); ms != nil {
			for _, m := range ms {
				if id, _ := m["id"].(string); id != "" {
					ids.members[id] = true
				}
			}
		}
	}
	return ids
}

// lbChildrenOf lists an LB's live children (externalID-scoped) and builds the id sets. Fail-closed:
// a listing error yields an empty set for that kind, so the §30 guard rejects rather than trusts.
func lbChildrenOf(ctx context.Context, cc *client.Client, lbExternalID string) lbChildIDs {
	var listeners, pools []map[string]any
	if ls, err := cc.GetListeners(ctx, lbExternalID); err == nil {
		listeners = ls
	}
	if ps, err := cc.GetPools(ctx, lbExternalID); err == nil {
		pools = ps
	}
	return lbChildSets(listeners, pools)
}

// regionFor resolves the project's region for a service, falling back to the deploy default.
func (h *Handler) regionFor(p *Project, svcID string) string {
	if region := p.ServiceRegion(svcID); region != "" {
		return region
	}
	return h.cloudRegion
}

// listImages live-lists the project's glance images as IMAGE CloudResources:
// {externalId, type:IMAGE, region, serviceId, projectId, data:{image:{...}}}. The FE shows these
// as the OS options and sends the picked externalId as the create's imageId. Empty list (not an
// error) if the cloud is unreachable.
func (h *Handler) listImages(w http.ResponseWriter, r *http.Request, proj *Project) {
	svcID := h.resolveServiceID(r, proj)
	region := h.regionFor(proj, svcID)
	// `?dataAssociatedTo=<serverId>` = the server-detail Snapshots tab: only this server's snapshots
	// (glance images stamped instance_uuid=<serverId>). Otherwise this is the "My Images" tab → the
	// project's OWN images/snapshots (owner==tenant), NOT the whole glance catalog (the wizard's OS
	// list comes from the curated imageGroups + the PUBLIC_IMAGES action, a different path). The svc
	// account is cloud-admin so an unfiltered glance list leaks every tenant's private images.
	assoc := r.URL.Query().Get("dataAssociatedTo")
	ownerID := proj.ExternalProjectID(svcID)
	out := []cloud.CloudResource{}
	if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
		if imgs, err := cc.ListImagesFull(r.Context()); err == nil {
			for _, im := range imgs {
				if !imageVisibleTo(im, ownerID, assoc) {
					continue
				}
				id, _ := im["id"].(string)
				out = append(out, cloud.CloudResource{
					ID: id, Type: cloud.TypeImage, ExternalID: id, Region: region,
					ServiceID: svcID, ProjectID: proj.ID,
					Data:      map[string]any{"image": im},
					CreatedAt: liveCreatedAt(im),
				})
			}
		}
	}
	httpx.List(w, out)
}

// cloudGet handles GET /api/v1/project/{id}/cloud/{resourceId} → the
// cached CloudResource (by its doc id), best-effort live-refreshed from the cloud so the FE detail
// view shows current status/addresses. Includes a sync step (the cache
// otherwise holds only the minimal create-time snapshot; real-time os-notification refresh is deferred).
// Project-membership-gated (read).
func (h *Handler) cloudGet(w http.ResponseWriter, r *http.Request) {
	u, ok := h.principal(w, r)
	if !ok {
		return
	}
	proj, err := h.svc.GetProject(r.Context(), u.Sub, chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	resourceID := chi.URLParam(r, "resourceId")
	cr, err := h.cloud.FindByID(r.Context(), resourceID)
	if err != nil {
		h.fail(w, err)
		return
	}
	if cr != nil && cr.ProjectID != proj.ID {
		cr = nil
	}
	if cr == nil {
		// Not in the cache → live-resolve by externalId (ports/floating-ips/security-groups the
		// cache never held; the os-notification sync that would cache them is deferred).
		svcID := h.resolveServiceID(r, proj)
		if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
			if live := liveResolveByExternalID(r.Context(), cc, resourceID, svcID, h.regionFor(proj, svcID), proj.ID); live != nil {
				httpx.OK(w, *live)
				return
			}
		}
		h.fail(w, httpx.NotFound("Resource not found"))
		return
	}
	// Best-effort live refresh — never fail the read if the cloud is unreachable.
	if cc, ok := h.tryTenantClient(r.Context(), proj, cr.ServiceID); ok {
		if refreshResource(r.Context(), cc, cr) {
			if saved, err := h.cloud.Insert(r.Context(), cr); err == nil {
				cr = saved
			}
		}
	}
	// Attach the monthly price (the FE server-detail Pricing tab reads model.vps.pricePlan). Set
	// AFTER the refresh/persist so the computed preview isn't written to the cache.
	h.attachPricePlan(r.Context(), proj, cr)
	httpx.OK(w, *cr)
}

// tryTenantClient builds a tenant-scoped cloud client, returning ok=false (no error written) on any
// failure — for best-effort live reads where the cached resource is the fallback.
func (h *Handler) tryTenantClient(ctx context.Context, p *Project, svcID string) (*client.Client, bool) {
	extProjID := p.ExternalProjectID(svcID)
	if extProjID == "" {
		return nil, false
	}
	es, err := h.esSvc.Get(ctx, svcID)
	if err != nil || es == nil {
		return nil, false
	}
	region := p.ServiceRegion(svcID)
	if region == "" {
		region = h.cloudRegion
	}
	cc, err := client.New(ctx, es.ClientConfigForProject(region, extProjID))
	if err != nil {
		return nil, false
	}
	return cc, true
}

// refreshResource re-fetches the live cloud object for the supported types and merges it into the
// cached resource's Data, returning true when it updated cr (so the caller persists). Best-effort:
// a fetch error leaves the cache untouched (returns false).
func refreshResource(ctx context.Context, cc *client.Client, cr *cloud.CloudResource) bool {
	switch cr.Type {
	case cloud.TypeServer, cloud.TypeBaremetalServer:
		if srv, err := cc.GetServer(ctx, cr.ExternalID); err == nil && srv != nil {
			enrichServerFlavor(ctx, cc, srv)
			// DataServer carries instanceMetadata = server.getMetadata() (CloudResourceVPSProvider);
			// the FE metadata panel reads server.data.instanceMetadata, not data.server.metadata.
			cr.Data = map[string]any{"server": srv, "instanceMetadata": serverMetadataMap(srv)}
			return true
		}
	case cloud.TypeNetwork:
		if net, err := cc.GetNetwork(ctx, cr.ExternalID); err == nil && net != nil {
			name, _ := cr.Data["networkName"].(string)
			cr.Data = map[string]any{"network": net, "networkName": name}
			return true
		}
	case cloud.TypeVolume:
		if v, err := cc.GetVolume(ctx, cr.ExternalID); err == nil && v != nil {
			att := cr.Data["attachments"]
			cr.Data = map[string]any{"volume": v, "attachments": att}
			return true
		}
	case cloud.TypeBarbicanSecret:
		if sec, err := cc.GetSecret(ctx, cr.ExternalID); err == nil && sec != nil {
			cr.Data = map[string]any{"secret": sec}
			return true
		}
	case cloud.TypeBucket:
		if b, err := cc.GetBucket(ctx, cr.ExternalID); err == nil && b != nil {
			cr.Data = b
			return true
		}
	case cloud.TypeLoadBalancer:
		if lb, err := cc.GetLoadBalancer(ctx, cr.ExternalID); err == nil && lb != nil {
			cr.Data = map[string]any{"loadBalancer": lb}
			return true
		}
	case cloud.TypeStack:
		// Heat get is name+id keyed; the name is in the cached data.stack.
		st, _ := cr.Data["stack"].(map[string]any)
		name, _ := st["stack_name"].(string)
		if name == "" {
			name, _ = st["name"].(string)
		}
		if name != "" {
			if full, err := cc.GetStack(ctx, name, cr.ExternalID); err == nil && full != nil {
				cr.Data = map[string]any{"stack": full}
				return true
			}
		}
	case cloud.TypeShare:
		if sh, err := cc.GetShare(ctx, cr.ExternalID); err == nil && sh != nil {
			cr.Data = map[string]any{"share": sh}
			return true
		}
	}
	return false
}

// serverMetadataMap surfaces the nova server metadata as the FE-expected data.instanceMetadata
// (CloudResourceVPSProvider sets DataServer.instanceMetadata = server.getMetadata()). Never nil so
// the metadata panel reads an object, not undefined.
func serverMetadataMap(srv map[string]any) map[string]any {
	if m, ok := srv["metadata"].(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// liveResolveByExternalID builds a CloudResource for an uncached resource by probing the live
// cloud for each type until one resolves (the FE resource-detail pages pass the externalId of a
// live port/floating-ip/security-group/etc. that the cache never held). Returns nil if none match.
func liveResolveByExternalID(ctx context.Context, cc *client.Client, extID, svcID, region, projID string) *cloud.CloudResource {
	probes := []struct {
		typ, key string
		fetch    func() (map[string]any, error)
	}{
		{cloud.TypeServer, "server", func() (map[string]any, error) { return cc.GetServer(ctx, extID) }},
		{cloud.TypeNetwork, "network", func() (map[string]any, error) { return cc.GetNetwork(ctx, extID) }},
		{cloud.TypePort, "port", func() (map[string]any, error) { return cc.GetPort(ctx, extID) }},
		{cloud.TypeFloatingIP, "floatingIp", func() (map[string]any, error) { return cc.GetFloatingIP(ctx, extID) }},
		{cloud.TypeSecurityGroup, "securityGroup", func() (map[string]any, error) { return cc.GetSecurityGroup(ctx, extID) }},
		{cloud.TypeRouter, "router", func() (map[string]any, error) { return cc.GetRouter(ctx, extID) }},
		{cloud.TypeVolume, "volume", func() (map[string]any, error) { return cc.GetVolume(ctx, extID) }},
	}
	for _, p := range probes {
		obj, err := p.fetch()
		if err != nil || obj == nil {
			continue
		}
		if p.typ == cloud.TypeServer {
			enrichServerFlavor(ctx, cc, obj)
		}
		return &cloud.CloudResource{
			ID: extID, Type: p.typ, ExternalID: extID, Region: region,
			ServiceID: svcID, ProjectID: projID, Data: map[string]any{p.key: obj},
		}
	}
	return nil
}

// enrichServerFlavor resolves a raw nova server's `flavor:{id,links}` into full specs
// ({ram,vcpus,disk,name}) in place — the newer nova microversion embeds only id/links, so the
// client server-detail/list otherwise binds vcpus/ram as undefined/NaN. Best-effort + idempotent
// (skips when already resolved or the flavor lookup fails).
func enrichServerFlavor(ctx context.Context, cc *client.Client, srv map[string]any) {
	fl, ok := srv["flavor"].(map[string]any)
	if !ok || fl == nil {
		return
	}
	if _, has := fl["vcpus"]; has {
		return
	}
	id, _ := fl["id"].(string)
	if id == "" {
		return
	}
	if specs, err := cc.GetFlavor(ctx, id); err == nil {
		for k, v := range specs {
			fl[k] = v
		}
		srv["flavor"] = fl
	}
}

// cloudCreate handles POST /api/v1/project/{id}/cloud with
// {type, data} → WriteService.Create → the persisted CloudResource.
func (h *Handler) cloudCreate(w http.ResponseWriter, r *http.Request) {
	u, ok := h.principal(w, r)
	if !ok {
		return
	}
	proj, ok := h.project(w, r, u, rbac.ProjectCloudResourceManage)
	if !ok {
		return
	}
	svcID := h.resolveServiceID(r, proj)
	if svcID == "" {
		h.fail(w, httpx.BadRequest("Project has no cloud service"))
		return
	}
	ws, region, ok := h.tenantWriteService(r.Context(), w, proj, svcID)
	if !ok {
		return
	}
	var req providers.CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.fail(w, httpx.BadRequest("invalid request body"))
		return
	}
	// Public-network allow-list enforcement (project.publicNetworkIds, admin-managed): any
	// external-network target — a FIP pool, a router gateway, or the server auto-FIP leg — must
	// be enabled for the project (nil allow-list = all allowed; see publicnetworks.go).
	assignFIP, _ := req.Data["assignFloatingIp"].(bool)
	fipNetID := strAny(req.Data["floatingNetworkId"])
	switch req.Type {
	case cloud.TypeFloatingIP:
		fnet := fipNetID
		if fnet == "" {
			fnet = strAny(req.Data["networkId"])
		}
		if fnet != "" && !publicNetworkAllowed(proj, fnet) {
			h.fail(w, httpx.BadRequest("External network is not enabled for this project"))
			return
		}
	case cloud.TypeRouter:
		if ext := strAny(req.Data["externalNetworkId"]); ext != "" && !publicNetworkAllowed(proj, ext) {
			h.fail(w, httpx.BadRequest("External network is not enabled for this project"))
			return
		}
	case cloud.TypeServer, cloud.TypeBaremetalServer:
		if assignFIP {
			if fipNetID == "" {
				h.fail(w, httpx.BadRequest("floatingNetworkId is required when assignFloatingIp is set"))
				return
			}
			if !publicNetworkAllowed(proj, fipNetID) {
				h.fail(w, httpx.BadRequest("External network is not enabled for this project"))
				return
			}
		}
	}
	uid := u.ID
	if uid == "" {
		uid = u.Sub
	}
	cr, err := ws.Create(r.Context(), svcID, region, proj.ID, uid, req)
	if err != nil {
		h.fail(w, err)
		return
	}
	if assignFIP && (req.Type == cloud.TypeServer || req.Type == cloud.TypeBaremetalServer) {
		h.autoAssignFloatingIP(ws, proj, svcID, region, uid, cr.ExternalID, strAny(req.Data["name"]), fipNetID)
	}
	h.projectAudit(u, proj, "CLOUD_RESOURCE_CREATE")
	httpx.OK(w, *cr)
}

// autoAssignFloatingIP asynchronously allocates a floating IP on floatingNetworkID and associates
// it with the new server's first port, through the normal FLOATING_IP create pipeline (so the FIP
// is persisted/cached like a manual create — providers/write.go reads data.networkId +
// data.externalPortId). Fire-and-forget: nova wires the port up after boot, so poll for it;
// failures are logged and never surfaced (the server create already succeeded). The captured ws +
// tenant client hold no request-scoped state (client.New only uses its ctx for the auth call), so
// reuse is safe; the polling client is rebuilt inside the goroutine off the background ctx.
func (h *Handler) autoAssignFloatingIP(ws *providers.WriteService, proj *Project, svcID, region, uid, serverExtID, serverName, floatingNetworkID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		cc, ok := h.tryTenantClient(ctx, proj, svcID)
		if !ok {
			slog.Error("auto-assign floating ip: cloud client not ready", "server", serverExtID)
			return
		}
		var portID string
		for portID == "" {
			if ports, err := cc.ListPortsFull(ctx, serverExtID); err == nil && len(ports) > 0 {
				portID, _ = ports[0]["id"].(string)
			}
			if portID != "" {
				break
			}
			select {
			case <-ctx.Done():
				slog.Error("auto-assign floating ip: no port appeared for server", "server", serverExtID)
				return
			case <-time.After(5 * time.Second):
			}
		}
		if _, err := ws.Create(ctx, svcID, region, proj.ID, uid, providers.CreateRequest{
			Type: cloud.TypeFloatingIP,
			Data: map[string]any{
				"networkId":      floatingNetworkID,
				"externalPortId": portID,
				"description":    "Auto-assigned for server " + serverName,
			},
		}); err != nil {
			slog.Error("auto-assign floating ip failed", "server", serverExtID, "network", floatingNetworkID, "err", err)
		}
	}()
}

// cloudDelete handles DELETE /api/v1/project/{id}/cloud/{rid}.
// {rid} is the resource's externalId (the create response carries it).
func (h *Handler) cloudDelete(w http.ResponseWriter, r *http.Request) {
	u, ok := h.principal(w, r)
	if !ok {
		return
	}
	proj, ok := h.project(w, r, u, rbac.ProjectCloudResourceManage)
	if !ok {
		return
	}
	svcID := h.resolveServiceID(r, proj)
	if svcID == "" {
		h.fail(w, httpx.BadRequest("Project has no cloud service"))
		return
	}
	ws, _, ok := h.tenantWriteService(r.Context(), w, proj, svcID)
	if !ok {
		return
	}
	// The FE passes the cloudResource CACHE id. §7: require an OWNED cache row and delete by ITS
	// externalId — never fall back to the raw param, or a caller could delete another project's
	// resource (WriteService.Delete resolves by {serviceId, externalId} with no project filter).
	resourceID := chi.URLParam(r, "resourceId")
	cr, ok := h.ownedResource(r.Context(), resourceID, proj)
	if !ok {
		h.fail(w, httpx.NotFound("Resource not found"))
		return
	}
	if err := ws.Delete(r.Context(), svcID, cr.ExternalID); err != nil {
		h.fail(w, err)
		return
	}
	h.projectAudit(u, proj, "CLOUD_RESOURCE_DELETE")
	httpx.Accepted(w)
}

// cloudBulkAction handles the collection-level action (NO resourceId):
// POST /api/v1/project/{id}/cloud/action {type, action, data} with x-service-id/x-region-id →
// CustomActionResponse {result}. Serves the create-server wizard's catalog actions live from the
// project's tenant: PUBLIC_IMAGES (glance images), PUBLIC_IMAGES_AS_CLOUD_RESOURCE, and
// LIST_AVAILABILITY_ZONES (nova AZs). Unknown actions return an empty result (non-fatal).
func (h *Handler) cloudBulkAction(w http.ResponseWriter, r *http.Request) {
	u, ok := h.principal(w, r)
	if !ok {
		return
	}
	proj, err := h.svc.GetProject(r.Context(), u.Sub, chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	var req struct {
		Type   string         `json:"type"`
		Action string         `json:"action"`
		Data   map[string]any `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.fail(w, httpx.BadRequest("invalid request body"))
		return
	}
	svcID := h.resolveServiceID(r, proj)
	var result any = []any{}
	if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
		switch req.Action {
		case "PUBLIC_IMAGES":
			if imgs, err := cc.ListImagesFull(r.Context()); err == nil {
				// PUBLIC_IMAGES filters out visibility==PRIVATE.
				out := make([]map[string]any, 0, len(imgs))
				for _, im := range imgs {
					if !imageIsPrivate(im) {
						out = append(out, im)
					}
				}
				result = out
			}
		case "PUBLIC_IMAGES_AS_CLOUD_RESOURCE":
			if imgs, err := cc.ListImagesFull(r.Context()); err == nil {
				region := proj.ServiceRegion(svcID)
				if region == "" {
					region = h.cloudRegion
				}
				crs := make([]cloud.CloudResource, 0, len(imgs))
				for _, im := range imgs {
					if imageIsPrivate(im) { // filter out visibility==PRIVATE
						continue
					}
					id, _ := im["id"].(string)
					cr := cloud.CloudResource{
						Type: cloud.TypeImage, ExternalID: id, Region: region,
						ServiceID: svcID, ProjectID: proj.ID, Data: map[string]any{"image": im},
					}
					// info.createdAt/updatedAt come from the glance
					// image; the FE created-at helper reads resource.info.createdAt → the CREATED column
					// (without it `new Date(undefined)` renders "Invalid date").
					if c := timeFromAny(im["created_at"]); c != nil {
						cr.Info = &cloud.Info{CreatedAt: c, UpdatedAt: timeFromAny(im["updated_at"])}
					}
					crs = append(crs, cr)
				}
				result = crs
			}
		case "LIST_AVAILABILITY_ZONES":
			if azs, err := cc.ListAvailabilityZones(r.Context()); err == nil {
				result = azs
			}
		case "LIST_FLAVORS":
			// the create-server Hardware table — live nova flavors (LIST_FLAVORS →
			// fetch flavors, mapped to a provider resource). The FE matches a curated
			// flavorCategory entry to a live flavor by `flavor.data.name === category.flavorName` and
			// creates with `selectedFlavor.externalId`, so each flavor must carry externalId + data.name.
			if fs, err := cc.ListFlavors(r.Context()); err == nil {
				region := proj.ServiceRegion(svcID)
				if region == "" {
					region = h.cloudRegion
				}
				out := make([]map[string]any, 0, len(fs))
				for _, f := range fs {
					out = append(out, map[string]any{
						"externalId": f.ID, "serviceId": svcID, "region": region, "type": cloud.TypeServer,
						"data": map[string]any{"id": f.ID, "name": f.Name, "vcpus": f.VCPUs, "ram": f.RAM, "disk": f.Disk},
					})
				}
				result = out
			}
		// Trilio collection-level actions (snapshot/workload, no resourceId) — live-blocked
		// (no backend on the current regions; resolve-endpoint errors → empty result, non-fatal).
		case "LIST_BACKUP_TARGET_TYPES":
			if v, err := cc.ListBackupTargetTypes(r.Context()); err == nil {
				result = v
			}
		case "LIST_MOUNTED":
			if v, err := cc.ListMountedSnapshots(r.Context(), strAny(req.Data["workloadId"])); err == nil {
				result = v
			}
		case "FILE_SEARCH":
			if v, err := cc.StartFileSearch(r.Context(), req.Data); err == nil {
				result = v
			}
		case "FILE_SEARCH_RESULTS":
			if v, err := cc.GetFileSearchResults(r.Context(), strAny(req.Data["searchId"])); err == nil {
				result = v
			}
		}
	}
	httpx.OK(w, map[string]any{"result": result})
}

// imageIsPrivate reports whether a glance image map (from ListImagesFull) has visibility==private —
// the PUBLIC_IMAGES actions filter these out (only public/shared/community are "public").
func imageIsPrivate(im map[string]any) bool {
	v, _ := im["visibility"].(string)
	return v == "private"
}

// timeFromAny coerces a glance timestamp (time.Time from gophercloud, or an RFC3339 string) to a
// *time.Time, returning nil for zero/absent/unparseable values.
func timeFromAny(v any) *time.Time {
	switch t := v.(type) {
	case time.Time:
		if t.IsZero() {
			return nil
		}
		u := t.UTC()
		return &u
	case *time.Time:
		if t == nil || t.IsZero() {
			return nil
		}
		u := t.UTC()
		return &u
	case string:
		if t == "" {
			return nil
		}
		if parsed, err := time.Parse(time.RFC3339, t); err == nil && !parsed.IsZero() {
			u := parsed.UTC()
			return &u
		}
	}
	return nil
}

// cloudAction handles POST /cloud/{resourceId}/action with
// {action, data}. The FE passes the cloudResource CACHE doc id (not the externalId), so the
// resource is resolved by FindByID (the externalId is the fallback). READ actions (LIST_EVENTS,
// LIST_SECURITY_GROUPS, GET_PORTS) return live data; mutating actions go through WriteService.
func (h *Handler) cloudAction(w http.ResponseWriter, r *http.Request) {
	u, ok := h.principal(w, r)
	if !ok {
		return
	}
	proj, ok := h.project(w, r, u, rbac.ProjectCloudResourceManage)
	if !ok {
		return
	}
	svcID := h.resolveServiceID(r, proj)
	if svcID == "" {
		h.fail(w, httpx.BadRequest("Project has no cloud service"))
		return
	}
	// §5: require an OWNED cache row before acting. A raw external id (not a cache doc) or a row
	// owned by another project must never become the action target — both READ and mutating actions
	// would otherwise run against an id with no project binding.
	resourceID := chi.URLParam(r, "resourceId")
	cr, ok := h.ownedResource(r.Context(), resourceID, proj)
	if !ok {
		h.fail(w, httpx.NotFound("Resource not found"))
		return
	}
	externalID := cr.ExternalID
	var req struct {
		Action string         `json:"action"`
		Type   string         `json:"type"`
		Data   map[string]any `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.fail(w, httpx.BadRequest("invalid request body"))
		return
	}

	// READ actions — return live data, no cache mutation.
	switch req.Action {
	case "LIST_EVENTS":
		events := []map[string]any{}
		if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
			if acts, err := cc.ListServerActions(r.Context(), externalID); err == nil {
				for _, a := range acts {
					events = append(events, map[string]any{
						"date":      firstStr(a, "start_time", "startTime"),
						"action":    prettyAction(strAny(a["action"])),
						"message":   a["message"],
						"requestId": firstStr(a, "request_id", "requestId"),
						"userId":    firstStr(a, "user_id", "userId"),
					})
				}
			}
		}
		httpx.OK(w, map[string]any{"result": events})
		return
	case "LIST_SECURITY_GROUPS":
		out := []cloud.CloudResource{}
		if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
			if sgs, err := cc.ListServerSecurityGroups(r.Context(), externalID); err == nil {
				region := h.regionFor(proj, svcID)
				for _, sg := range sgs {
					id, _ := sg["id"].(string)
					out = append(out, cloud.CloudResource{
						ID: id, Type: cloud.TypeSecurityGroup, ExternalID: id, Region: region,
						ServiceID: svcID, ProjectID: proj.ID, Data: map[string]any{"securityGroup": sg},
					})
				}
			}
		}
		httpx.OK(w, map[string]any{"result": out})
		return
	case "LIST_RULES":
		// SECURITY_GROUP detail: the group's rules (LIST_RULES). The neutron
		// group embeds security_group_rules; return them raw (id/direction/ethertype/protocol/ports).
		rules := []any{}
		if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
			if sg, err := cc.GetSecurityGroup(r.Context(), externalID); err == nil {
				if rs, ok := sg["security_group_rules"].([]any); ok {
					rules = rs
				}
			}
		}
		httpx.OK(w, map[string]any{"result": rules})
		return
	case "GET_MEMBERS":
		// SERVER_GROUP detail: the member servers (GET_MEMBERS). The nova group
		// embeds member externalIds; resolve each to its cached CloudResource (drop misses).
		out := []cloud.CloudResource{}
		if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
			if sg, err := cc.GetServerGroup(r.Context(), externalID); err == nil {
				if members, ok := sg["members"].([]any); ok {
					for _, m := range members {
						mid, _ := m.(string)
						if mid == "" {
							continue
						}
						if mc, _ := h.cloud.FindByServiceIDAndExternalID(r.Context(), svcID, mid); mc != nil {
							out = append(out, *mc)
						}
					}
				}
			}
		}
		httpx.OK(w, map[string]any{"result": out})
		return
	case "GET_RECORDSETS":
		// DNS zone detail: the zone's recordsets. Empty on error.
		rs := []map[string]any{}
		if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
			if list, err := cc.ListRecordsets(r.Context(), externalID); err == nil {
				rs = list
			}
		}
		httpx.OK(w, map[string]any{"result": rs})
		return
	case "LIST_NAMESERVERS":
		// Designate nameservers — not implemented (best-effort empty so the zone page doesn't 500).
		httpx.OK(w, map[string]any{"result": []any{}})
		return
	case "LIST_OBJECTS":
		// Object-storage explore: the bucket's objects/folders.
		// data.folderName scopes to a sub-path. Empty on error/empty bucket.
		objs := []map[string]any{}
		if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
			if list, err := cc.ListBucketObjects(r.Context(), externalID, strAny(req.Data["folderName"])); err == nil {
				objs = list
			}
		}
		httpx.OK(w, map[string]any{"result": objs})
		return
	case "IS_BUCKET_PUBLIC":
		// Object-storage: the bucket's read ACL → public?
		pub := false
		if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
			if v, err := cc.IsBucketPublic(r.Context(), externalID); err == nil {
				pub = v
			}
		}
		httpx.OK(w, map[string]any{"result": pub})
		return
	case "LIST_APIS":
		// Object-storage: the bucket's Swift + S3 access URLs
		// (swiftApis, s3Apis).
		swiftApis, s3Apis := []string{}, []string{}
		if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
			if sw, s3, err := cc.BucketAPIs(r.Context(), externalID); err == nil {
				swiftApis, s3Apis = sw, s3
			}
		}
		httpx.OK(w, map[string]any{"result": map[string]any{"swiftApis": swiftApis, "s3Apis": s3Apis}})
		return
	case "DOWNLOAD":
		// Object-storage: mint a short-lived download token (DOWNLOAD) + return its URL. The FE
		// opens result.url → GET /api/v1/download/{token} streams the object.
		if h.downloads == nil {
			h.fail(w, httpx.NewError(http.StatusServiceUnavailable, http.StatusServiceUnavailable, "download not configured"))
			return
		}
		tok, err := h.downloads.Create(r.Context(), &cloud.CloudDownload{
			Type: cloud.DownloadTypeSwiftObject, ServiceID: svcID, ProjectID: proj.ID, ExternalID: externalID,
			Metadata: map[string]string{"objectName": strAny(req.Data["objectName"])},
		})
		if err != nil {
			h.fail(w, err)
			return
		}
		url := strings.TrimRight(h.apiBaseURL, "/") + "/api/v1/download/" + tok.ID
		httpx.OK(w, map[string]any{"result": map[string]any{"url": url}})
		return
	case "CREATE_FOLDER":
		// Object-storage: create a pseudo-folder marker (createFolder). The
		// FE sends data.folderName. noContent → empty result.
		cc, ok := h.tryTenantClient(r.Context(), proj, svcID)
		if !ok {
			httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "cloud client not ready")
			return
		}
		if err := cc.CreateFolder(r.Context(), externalID, strAny(req.Data["folderName"])); err != nil {
			h.fail(w, err)
			return
		}
		httpx.OK(w, map[string]any{"result": map[string]any{}})
		return
	case "DELETE_OBJECT":
		// Object-storage: delete an object (or folder + contents) by name (DELETE_OBJECT,
		// startsWith). Then refresh the bucket's size/count cache.
		cc, ok := h.tryTenantClient(r.Context(), proj, svcID)
		if !ok {
			httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "cloud client not ready")
			return
		}
		if err := cc.DeleteBucketObject(r.Context(), externalID, strAny(req.Data["objectName"])); err != nil {
			h.fail(w, err)
			return
		}
		h.refreshBucketCache(r.Context(), cc, cr)
		httpx.OK(w, map[string]any{"result": map[string]any{}})
		return
	case "UPDATE_OBJECT":
		// Object-storage: update an object's metadata (UPDATE_OBJECT). Returns the refreshed cr.
		cc, ok := h.tryTenantClient(r.Context(), proj, svcID)
		if !ok {
			httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "cloud client not ready")
			return
		}
		if err := cc.UpdateBucketObject(r.Context(), externalID, strAny(req.Data["objectName"]), strMap(req.Data["metadata"])); err != nil {
			h.fail(w, err)
			return
		}
		h.refreshBucketCache(r.Context(), cc, cr)
		httpx.OK(w, map[string]any{"result": bucketResult(cr)})
		return
	case "UPDATE_BUCKET_METADATA":
		// Object-storage: update the container's metadata (UPDATE_BUCKET_METADATA). Returns cr.
		cc, ok := h.tryTenantClient(r.Context(), proj, svcID)
		if !ok {
			httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "cloud client not ready")
			return
		}
		if err := cc.UpdateBucketMetadata(r.Context(), externalID, strMap(req.Data["metadata"])); err != nil {
			h.fail(w, err)
			return
		}
		h.refreshBucketCache(r.Context(), cc, cr)
		httpx.OK(w, map[string]any{"result": bucketResult(cr)})
		return
	case "MAKE_BUCKET_PUBLIC":
		// Object-storage: grant public read (.r:*,.rlistings — makeBucketPublic). result=true.
		cc, ok := h.tryTenantClient(r.Context(), proj, svcID)
		if !ok {
			httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "cloud client not ready")
			return
		}
		if err := cc.SetBucketRead(r.Context(), externalID, ".r:*,.rlistings"); err != nil {
			h.fail(w, err)
			return
		}
		httpx.OK(w, map[string]any{"result": true})
		return
	case "MAKE_BUCKET_PRIVATE":
		// Object-storage: remove public read (makeBucketPrivate, ""). result=false.
		cc, ok := h.tryTenantClient(r.Context(), proj, svcID)
		if !ok {
			httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "cloud client not ready")
			return
		}
		if err := cc.SetBucketRead(r.Context(), externalID, ""); err != nil {
			h.fail(w, err)
			return
		}
		httpx.OK(w, map[string]any{"result": false})
		return
	case "CREATE_RECORDSET":
		// DNS zone action: add a recordset (createRecordSet; ttl forced to 7200).
		var result any = map[string]any{}
		if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
			rs, err := cc.CreateRecordset(r.Context(), externalID, client.CreateRecordsetOpts{
				Name: strAny(req.Data["name"]), Type: strAny(req.Data["type"]),
				TTL: intAny(req.Data["ttl"]), Records: strSlice(req.Data["records"]),
			})
			if err != nil {
				h.fail(w, err)
				return
			}
			result = rs
		}
		httpx.OK(w, map[string]any{"result": result})
		return
	case "DELETE_RECORDSET":
		if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
			if err := cc.DeleteRecordset(r.Context(), externalID, strAny(req.Data["recordsetId"])); err != nil {
				h.fail(w, err)
				return
			}
		}
		httpx.OK(w, map[string]any{"result": map[string]any{}})
		return
	case "UPDATE_RECORDSET":
		// DNS zone action: update a recordset's ttl/records (updateRecordset).
		var result any = map[string]any{}
		if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
			rs, err := cc.UpdateRecordset(r.Context(), externalID, strAny(req.Data["recordsetId"]), intAny(req.Data["ttl"]), strSlice(req.Data["records"]))
			if err != nil {
				h.fail(w, err)
				return
			}
			result = rs
		}
		httpx.OK(w, map[string]any{"result": result})
		return
	case "GET_PORTS":
		out := []cloud.CloudResource{}
		if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
			if ports, err := cc.ListPortsFull(r.Context(), externalID); err == nil {
				region := h.regionFor(proj, svcID)
				for _, p := range ports {
					id, _ := p["id"].(string)
					out = append(out, cloud.CloudResource{
						ID: id, Type: cloud.TypePort, ExternalID: id, Region: region,
						ServiceID: svcID, ProjectID: proj.ID, Data: map[string]any{"port": p},
					})
				}
			}
		}
		httpx.OK(w, map[string]any{"result": out})
		return
	case "GET_SERVERS":
		// NETWORK detail: the servers attached to this network (GET_SERVERS).
		// Resolve via the network's ports (device_owner=compute:*) → unique device_ids → live servers.
		out := []cloud.CloudResource{}
		if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
			region := h.regionFor(proj, svcID)
			if ports, err := cc.ListPortsFull(r.Context(), ""); err == nil {
				seen := map[string]bool{}
				for _, p := range ports {
					if nid, _ := p["network_id"].(string); nid != externalID {
						continue
					}
					owner, _ := p["device_owner"].(string)
					did, _ := p["device_id"].(string)
					if did == "" || seen[did] || !strings.HasPrefix(owner, "compute:") {
						continue
					}
					seen[did] = true
					if srv, err := cc.GetServer(r.Context(), did); err == nil && srv != nil {
						enrichServerFlavor(r.Context(), cc, srv)
						out = append(out, cloud.CloudResource{
							ID: did, Type: cloud.TypeServer, ExternalID: did, Region: region,
							ServiceID: svcID, ProjectID: proj.ID, Data: map[string]any{"server": srv},
						})
					}
				}
			}
		}
		httpx.OK(w, map[string]any{"result": out})
		return
	case "SHOW_CONSOLE_OUTPUT":
		var logs string
		if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
			length := 50
			if n, isF := req.Data["length"].(float64); isF {
				length = int(n)
			}
			if out, err := cc.GetConsoleOutput(r.Context(), externalID, length); err == nil {
				logs = out
			}
		}
		httpx.OK(w, map[string]any{"result": logs})
		return
	case "REMOTECONTROL":
		var console map[string]any
		if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
			if rc, err := cc.GetVNCConsole(r.Context(), externalID); err == nil {
				console = rc
			} else {
				h.fail(w, err)
				return
			}
		}
		httpx.OK(w, map[string]any{"result": console})
		return
	case "REBUILD":
		// Server REBUILD onto an image (rebuild). FE: data{imageId|imageRef|
		// image, name?, adminPass?}. Refresh the cached server after.
		cc, ok := h.tryTenantClient(r.Context(), proj, svcID)
		if !ok {
			httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "cloud client not ready")
			return
		}
		imageRef := firstStr(req.Data, "imageId", "imageRef", "image")
		srv, err := cc.RebuildServer(r.Context(), externalID, imageRef, strAny(req.Data["name"]), strAny(req.Data["adminPass"]))
		if err != nil {
			h.fail(w, err)
			return
		}
		if cr != nil {
			cr.Data = map[string]any{"server": srv, "instanceMetadata": serverMetadataMap(srv)}
			if saved, e := h.cloud.Insert(r.Context(), cr); e == nil {
				cr = saved
			}
		}
		httpx.OK(w, map[string]any{"result": map[string]any{"server": srv}})
		return
	case "SET_PASSWORD":
		// Server admin-password change (setPassword). FE: data{password}.
		cc, ok := h.tryTenantClient(r.Context(), proj, svcID)
		if !ok {
			httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "cloud client not ready")
			return
		}
		if err := cc.SetServerPassword(r.Context(), externalID, strAny(req.Data["password"])); err != nil {
			h.fail(w, err)
			return
		}
		httpx.OK(w, map[string]any{"result": map[string]any{}})
		return
	case "RESCUE":
		// Server RESCUE (rescue) → generated admin password as the result.
		cc, ok := h.tryTenantClient(r.Context(), proj, svcID)
		if !ok {
			httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "cloud client not ready")
			return
		}
		pw, err := cc.RescueServer(r.Context(), externalID, strAny(req.Data["rescueImageRef"]))
		if err != nil {
			h.fail(w, err)
			return
		}
		httpx.OK(w, map[string]any{"result": pw})
		return
	case "UNRESCUE":
		cc, ok := h.tryTenantClient(r.Context(), proj, svcID)
		if !ok {
			httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "cloud client not ready")
			return
		}
		if err := cc.UnrescueServer(r.Context(), externalID); err != nil {
			h.fail(w, err)
			return
		}
		httpx.OK(w, map[string]any{"result": map[string]any{}})
		return
	case "LIST_TYPES":
		// Volume types catalog (LIST_TYPES → cinder volume types).
		types := []map[string]any{}
		if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
			if list, err := cc.ListVolumeTypes(r.Context()); err == nil {
				types = list
			}
		}
		httpx.OK(w, map[string]any{"result": types})
		return
	case "LIST_AVAILABILITY_ZONES":
		// Volume AZs (LIST_AVAILABILITY_ZONES → cinder AZs). The nova-AZ variant is
		// the collection-level cloudBulkAction; here (resource-level) it serves the volume page.
		azs := []map[string]any{}
		if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
			if list, err := cc.ListVolumeAvailabilityZones(r.Context()); err == nil {
				azs = list
			}
		}
		httpx.OK(w, map[string]any{"result": azs})
		return
	case "GET_OBJECT_ACL", "UPDATE_OBJECT_ACL", "GENERATE_TEMP_URL":
		// Object-storage ACL / presigned-URL actions are S3-only (need the S3 client);
		// this region is Swift → not supported → 501 (throws UnsupportedOperation).
		httpx.Err(w, http.StatusNotImplemented, http.StatusNotImplemented, "object ACL / temp-url is S3-only, not supported on Swift")
		return
	case "GET_PASSWORD":
		// Nova os-server-password: return the ENCRYPTED admin password blob; the FE decrypts it
		// client-side with the user's keypair private key. Empty (graceful) when unset or cloud not
		// ready — "no password" is a normal state (Linux / no-keypair instances).
		pw := ""
		if cc, ok := h.tryTenantClient(r.Context(), proj, svcID); ok {
			if p, err := cc.GetServerPassword(r.Context(), externalID); err == nil {
				pw = p
			}
		}
		httpx.OK(w, map[string]any{"result": map[string]any{"password": pw}})
		return
	case "METRICS":
		// Instance metrics (gnocchi time-series) are not surfaced through this client path yet → 501.
		httpx.Err(w, http.StatusNotImplemented, http.StatusNotImplemented, "action not implemented")
		return
	}

	// New cloud-cluster actions (Octavia LB sub-resources / Heat stack / Manila share+network /
	// VPNaaS-IPSec / Magnum / Barbican-container / keystone-user). Type-gated (generic names like
	// UPDATE / GET_TEMPLATE collide across types), cc-based (full control of the result shape).
	if h.clusterAction(w, r, proj, svcID, cr, externalID, req.Action, req.Data) {
		return
	}

	// Mutating actions → WriteService (resolves by externalId, now correct). The FE's
	// cloudresource.action() reads response.data.result, so wrap the updated resource the same way.
	ws, _, ok := h.tenantWriteService(r.Context(), w, proj, svcID)
	if !ok {
		return
	}
	res, err := ws.Action(r.Context(), svcID, proj.ID, externalID, req.Action, req.Data)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.projectAudit(u, proj, "CLOUD_RESOURCE_ACTION")
	httpx.OK(w, map[string]any{"result": res})
}

// cloudMetadata handles PUT /project/{id}/cloud/{serverId}/metadata
// with the FULL metadata map (the client Metadata tab's full-map save) → nova ResetMetadata, then
// refresh the cached server. {resourceId} = the cloudResource cache id.
func (h *Handler) cloudMetadata(w http.ResponseWriter, r *http.Request) {
	u, ok := h.principal(w, r)
	if !ok {
		return
	}
	proj, ok := h.project(w, r, u, rbac.ProjectCloudResourceManage)
	if !ok {
		return
	}
	cr, _ := h.cloud.FindByID(r.Context(), chi.URLParam(r, "resourceId"))
	if cr == nil || cr.ProjectID != proj.ID {
		h.fail(w, httpx.NotFound("Resource not found"))
		return
	}
	var meta map[string]string
	if err := json.NewDecoder(r.Body).Decode(&meta); err != nil {
		h.fail(w, httpx.BadRequest("invalid request body"))
		return
	}
	cc, ok := h.tryTenantClient(r.Context(), proj, cr.ServiceID)
	if !ok {
		httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "cloud client not ready")
		return
	}
	if _, err := cc.SetServerMetadata(r.Context(), cr.ExternalID, meta); err != nil {
		h.fail(w, err)
		return
	}
	if refreshResource(r.Context(), cc, cr) {
		if saved, err := h.cloud.Insert(r.Context(), cr); err == nil {
			cr = saved
		}
	}
	h.projectAudit(u, proj, "CLOUD_RESOURCE_ACTION")
	httpx.OK(w, *cr)
}

// cloudObjectDownload resolves a download token
// (whitelisted — the token IS the auth, no bearer) → streams the cloud object's bytes. Currently the
// Swift-object type (object-store DOWNLOAD action).
func (h *Handler) cloudObjectDownload(w http.ResponseWriter, r *http.Request) {
	if h.downloads == nil {
		h.fail(w, httpx.NotFound("Download not found"))
		return
	}
	tok, err := h.downloads.ByID(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		h.fail(w, err)
		return
	}
	if tok == nil {
		h.fail(w, httpx.NotFound("Download not found"))
		return
	}
	proj, err := h.svc.GetProjectByID(r.Context(), tok.ProjectID)
	if err != nil || proj == nil {
		h.fail(w, httpx.NotFound("Download not found"))
		return
	}
	cc, ok := h.tryTenantClient(r.Context(), proj, tok.ServiceID)
	if !ok {
		httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "cloud client not ready")
		return
	}
	switch tok.Type {
	case cloud.DownloadTypeSwiftObject:
		objectName := tok.Metadata["objectName"]
		data, ct, err := cc.DownloadObject(r.Context(), tok.ExternalID, objectName)
		if err != nil {
			h.fail(w, err)
			return
		}
		fn := objectName
		if i := strings.LastIndex(fn, "/"); i >= 0 {
			fn = fn[i+1:]
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Disposition", "attachment; filename="+fn)
		_, _ = w.Write(data)
	default:
		h.fail(w, httpx.NewError(http.StatusNotImplemented, http.StatusNotImplemented, "download type not supported"))
	}
}

// cloudImageUpload handles POST /{projectId}/image/{imageId}/upload
// with the raw image bytes as the body → streams into the existing glance image.
func (h *Handler) cloudImageUpload(w http.ResponseWriter, r *http.Request) {
	u, ok := h.principal(w, r)
	if !ok {
		return
	}
	proj, ok := h.project(w, r, u, rbac.ProjectCloudResourceManage)
	if !ok {
		return
	}
	// Size guard: contentLength (GB, integer division) > 11 → 400. An unknown length (-1)
	// divides to 0 and passes.
	if r.ContentLength/(1<<30) > 11 {
		h.fail(w, httpx.BadRequest("Image size is too large. Max size is 10GB"))
		return
	}
	cc, ok := h.tryTenantClient(r.Context(), proj, h.resolveServiceID(r, proj))
	if !ok {
		httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "cloud client not ready")
		return
	}
	if err := cc.UploadImage(r.Context(), chi.URLParam(r, "imageId"), r.Body); err != nil {
		h.fail(w, err)
		return
	}
	h.projectAudit(u, proj, "CLOUD_RESOURCE_ACTION")
	w.WriteHeader(http.StatusNoContent)
}

// cloudUploadBucketFile handles POST /project/{id}/cloud/{resourceId}/upload-bucket-file?objectName=<name>
// with the raw file bytes as the request body (NOT multipart — the handler reads the input stream).
// Streams the body straight into the Swift container; 204 No Content on success.
func (h *Handler) cloudUploadBucketFile(w http.ResponseWriter, r *http.Request) {
	u, ok := h.principal(w, r)
	if !ok {
		return
	}
	proj, ok := h.project(w, r, u, rbac.ProjectCloudResourceManage)
	if !ok {
		return
	}
	cr, _ := h.cloud.FindByID(r.Context(), chi.URLParam(r, "resourceId"))
	if cr == nil || cr.ProjectID != proj.ID {
		h.fail(w, httpx.NotFound("Resource not found"))
		return
	}
	objectName := r.URL.Query().Get("objectName")
	if r.ContentLength < 0 {
		h.fail(w, httpx.BadRequest("Content-Length header is required for bucket object uploads"))
		return
	}
	cc, ok := h.tryTenantClient(r.Context(), proj, cr.ServiceID)
	if !ok {
		httpx.Err(w, http.StatusServiceUnavailable, http.StatusServiceUnavailable, "cloud client not ready")
		return
	}
	if err := cc.UploadBucketObject(r.Context(), cr.ExternalID, objectName, r.Header.Get("Content-Type"), r.ContentLength, r.Body); err != nil {
		h.fail(w, err)
		return
	}
	h.refreshBucketCache(r.Context(), cc, cr)
	h.projectAudit(u, proj, "CLOUD_RESOURCE_ACTION")
	w.WriteHeader(http.StatusNoContent)
}

// extID resolves a cloudResource CACHE id to its OpenStack externalId (FindByID), falling back to the
// id itself when it is not a cache doc.
func (h *Handler) extID(ctx context.Context, id string) string {
	if id == "" {
		return id
	}
	if c, _ := h.cloud.FindByID(ctx, id); c != nil && c.ExternalID != "" {
		return c.ExternalID
	}
	return id
}

// stackNameOf pulls the Heat stack name from a cached STACK resource's data (stack_name | name).
func stackNameOf(cr *cloud.CloudResource) string {
	st, _ := cr.Data["stack"].(map[string]any)
	if n, _ := st["stack_name"].(string); n != "" {
		return n
	}
	n, _ := st["name"].(string)
	return n
}

// serverIPv4FromCache returns the first IPv4 of a cached SERVER (the LB ADD_MEMBER target —
// resolves the member's targetId → the server's IPv4 address). "" when not resolvable.
func serverIPv4FromCache(ctx context.Context, h *Handler, cacheID string) string {
	c, _ := h.cloud.FindByID(ctx, cacheID)
	if c == nil {
		return ""
	}
	srv, _ := c.Data["server"].(map[string]any)
	addrs, _ := srv["addresses"].(map[string]any)
	for _, v := range addrs {
		list, _ := v.([]any)
		for _, a := range list {
			m, _ := a.(map[string]any)
			if iv, ok := m["version"].(float64); ok && iv == 4 {
				if ip, _ := m["addr"].(string); ip != "" {
					return ip
				}
			}
		}
	}
	return ""
}

// clusterAction handles the per-type actions for the newer cloud clusters (Octavia LB sub-resources,
// Heat stack, Manila share + share-network + security-service, VPNaaS/IPSec policy updates, Magnum,
// Barbican container, keystone user). Returns true when it produced a response (handled); false to let
// the caller fall through to WriteService. cc-based so each action returns its faithful shape.
func (h *Handler) clusterAction(w http.ResponseWriter, r *http.Request, proj *Project, svcID string, cr *cloud.CloudResource, externalID, action string, data map[string]any) bool {
	if cr == nil {
		return false
	}
	cc, ok := h.tryTenantClient(r.Context(), proj, svcID)
	if !ok {
		return false
	}
	ctx := r.Context()
	res := func(v any) bool { httpx.OK(w, map[string]any{"result": v}); return true }
	failT := func(err error) bool { h.fail(w, err); return true }

	switch cr.Type {
	case cloud.TypeServer, cloud.TypeBaremetalServer:
		switch action {
		case "ADD_SECURITY_GROUP":
			if err := cc.AddServerSecurityGroup(ctx, externalID, firstStr(data, "name", "securityGroupName", "securityGroup")); err != nil {
				return failT(err)
			}
			return res(map[string]any{})
		case "REMOVE_SECURITY_GROUP":
			if err := cc.RemoveServerSecurityGroup(ctx, externalID, firstStr(data, "name", "securityGroupName", "securityGroup")); err != nil {
				return failT(err)
			}
			return res(map[string]any{})
		case "ATTACH_PORT":
			portID := h.extID(ctx, firstStr(data, "portId", "id"))
			out, err := cc.AttachServerPort(ctx, externalID, portID, h.extID(ctx, strAny(data["networkId"])))
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "DETACH_PORT":
			if err := cc.DetachServerPort(ctx, externalID, h.extID(ctx, firstStr(data, "portId", "id"))); err != nil {
				return failT(err)
			}
			return res(map[string]any{})
		}
	case cloud.TypeLoadBalancer:
		// §30: child ids in the action body (listener/pool/member/monitor) are only trustworthy if
		// they belong to THIS LB (externalID). The body could otherwise name a child of another
		// project's LB in the same tenant. children() lazily lists the LB's live children once and
		// reject() 404s a body id that isn't in that set (fail-closed: a listing error → empty set).
		var kids *lbChildIDs
		children := func() *lbChildIDs {
			if kids == nil {
				k := lbChildrenOf(ctx, cc, externalID)
				kids = &k
			}
			return kids
		}
		reject := func() bool { h.fail(w, httpx.NotFound("Load balancer child resource not found")); return true }
		switch action {
		case "GET_LISTENERS":
			out, err := cc.GetListeners(ctx, externalID)
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "CREATE_LISTENER":
			if pid := strAny(data["poolId"]); pid != "" && !children().pools[pid] {
				return reject()
			}
			out, err := cc.CreateListener(ctx, client.CreateListenerOpts{
				Name: strAny(data["name"]), Protocol: strAny(data["protocol"]), ListenerPort: intAny(data["listenerPort"]),
				LoadBalancerID: externalID, ConnectionLimit: intAny(data["connectionLimit"]), PoolID: strAny(data["poolId"]),
			})
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "DELETE_LISTENER":
			if !children().listeners[strAny(data["id"])] {
				return reject()
			}
			if err := cc.DeleteListener(ctx, strAny(data["id"])); err != nil {
				return failT(err)
			}
			return res(map[string]any{})
		case "GET_POOLS":
			out, err := cc.GetPools(ctx, externalID)
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "CREATE_POOL":
			if !children().listeners[strAny(data["listenerId"])] {
				return reject()
			}
			out, err := cc.CreatePool(ctx, client.CreatePoolOpts{
				Name: strAny(data["name"]), Protocol: strAny(data["protocol"]), ListenerID: strAny(data["listenerId"]), LbAlgorithm: strAny(data["lbAlgorithm"]),
			})
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "DELETE_POOL":
			if !children().pools[strAny(data["id"])] {
				return reject()
			}
			if err := cc.DeletePool(ctx, strAny(data["id"])); err != nil {
				return failT(err)
			}
			return res(map[string]any{})
		case "ADD_MEMBER":
			if !children().pools[strAny(data["poolId"])] {
				return reject()
			}
			addr := firstStr(data, "address", "memberAddress")
			if addr == "" {
				addr = serverIPv4FromCache(ctx, h, strAny(data["targetId"]))
			}
			out, err := cc.AddMember(ctx, client.AddMemberOpts{PoolID: strAny(data["poolId"]), Address: addr, Port: intAny(data["memberPort"])})
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "DELETE_MEMBER":
			if !children().pools[strAny(data["poolId"])] {
				return reject()
			}
			if err := cc.DeleteMember(ctx, strAny(data["poolId"]), strAny(data["id"])); err != nil {
				return failT(err)
			}
			return res(map[string]any{})
		case "GET_MONITORS":
			out, err := cc.GetMonitors(ctx)
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "ADD_MONITOR":
			if !children().pools[strAny(data["poolId"])] {
				return reject()
			}
			out, err := cc.AddMonitor(ctx, client.AddMonitorOpts{
				PoolID: strAny(data["poolId"]), Name: strAny(data["name"]), Protocol: strAny(data["protocol"]),
				Timeout: intAny(data["timeout"]), Delay: intAny(data["delay"]), MaxRetries: intAny(data["maxRetries"]),
				MaxRetriesDown: intAny(data["maxRetriesDown"]), URLPath: strAny(data["urlPath"]), HTTPMethod: strAny(data["httpMethod"]), ExpectedCodes: strAny(data["expectedCodes"]),
			})
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "DELETE_MONITOR":
			if !children().monitors[strAny(data["id"])] {
				return reject()
			}
			if err := cc.DeleteMonitor(ctx, strAny(data["id"])); err != nil {
				return failT(err)
			}
			return res(map[string]any{})
		}
	case cloud.TypeStack:
		name := stackNameOf(cr)
		switch action {
		case "LIST_STACK_EVENTS", "LIST_EVENTS":
			out, err := cc.ListStackEvents(ctx, name, externalID)
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "LIST_RESOURCES":
			out, err := cc.ListStackResources(ctx, name, externalID)
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "GET_TEMPLATE":
			out, err := cc.GetStackTemplate(ctx, name, externalID)
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "UPDATE_TEMPLATE":
			if err := cc.UpdateStackTemplate(ctx, name, externalID, client.UpdateStackOpts{Template: strAny(data["template"]), Environment: strAny(data["environment"])}); err != nil {
				return failT(err)
			}
			return res(map[string]any{})
		case "SUSPEND_STACK":
			return suspendLike(cc.SuspendStack(ctx, name, externalID), failT, res)
		case "RESUME_STACK":
			return suspendLike(cc.ResumeStack(ctx, name, externalID), failT, res)
		case "CHECK_STACK":
			return suspendLike(cc.CheckStack(ctx, name, externalID), failT, res)
		case "CANCEL_UPDATE_STACK":
			return suspendLike(cc.CancelUpdateStack(ctx, name, externalID), failT, res)
		case "CANCEL_NO_ROLLBACK":
			return suspendLike(cc.CancelStackWithoutRollback(ctx, name, externalID), failT, res)
		case "ABANDON_STACK":
			out, err := cc.AbandonStack(ctx, name, externalID)
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "LIST_TEMPLATE_VERSIONS":
			out, err := cc.ListTemplateVersions(ctx)
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "LIST_TEMPLATE_FUNCTIONS":
			out, err := cc.ListTemplateFunctions(ctx, strAny(data["templateVersion"]))
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "LIST_RESOURCE_TYPES":
			out, err := cc.ListResourceTypes(ctx)
			if err != nil {
				return failT(err)
			}
			return res(out)
		}
	case cloud.TypeShare:
		switch action {
		case "GRANT_ACCESS":
			out, err := cc.GrantShareAccess(ctx, externalID, client.GrantShareAccessOpts{AccessType: strAny(data["accessType"]), AccessTo: strAny(data["accessTo"]), AccessLevel: strAny(data["accessLevel"])})
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "REVOKE_ACCESS":
			if err := cc.RevokeShareAccess(ctx, externalID, strAny(data["ruleId"])); err != nil {
				return failT(err)
			}
			return res(map[string]any{})
		case "LIST_ACCESS":
			out, err := cc.ListShareAccess(ctx, externalID)
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "EXTEND_SHARE":
			if err := cc.ExtendShare(ctx, externalID, intAny(data["size"])); err != nil {
				return failT(err)
			}
			return res(map[string]any{})
		case "SHRINK_SHARE":
			if err := cc.ShrinkShare(ctx, externalID, intAny(data["size"])); err != nil {
				return failT(err)
			}
			return res(map[string]any{})
		}
	case cloud.TypeShareNetwork:
		switch action {
		case "ADD_SECURITY_SERVICE":
			out, err := cc.AddSecurityServiceToNetwork(ctx, externalID, h.extID(ctx, strAny(data["securityServiceId"])))
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "REMOVE_SECURITY_SERVICE":
			out, err := cc.RemoveSecurityServiceFromNetwork(ctx, externalID, h.extID(ctx, strAny(data["securityServiceId"])))
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "UPDATE":
			out, err := cc.UpdateShareNetwork(ctx, externalID, h.extID(ctx, strAny(data["networkId"])), h.extID(ctx, strAny(data["subnetId"])))
			if err != nil {
				return failT(err)
			}
			return res(out)
		}
	case cloud.TypeIKEPolicy:
		if action == "UPDATE" {
			out, err := cc.UpdateIKEPolicy(ctx, externalID, client.UpdateIKEPolicyOpts{
				Name: strAny(data["name"]), Description: strAny(data["description"]), AuthAlgorithm: strAny(data["authAlgorithm"]),
				EncryptionAlgorithm: strAny(data["encryptionAlgorithm"]), PFS: strAny(data["pfs"]),
				Phase1NegotiationMode: strAny(data["phase1NegotiationMode"]), IKEVersion: strAny(data["ikeVersion"]),
			})
			if err != nil {
				return failT(err)
			}
			return res(out)
		}
	case cloud.TypeIPSecPolicy:
		if action == "UPDATE" {
			out, err := cc.UpdateIPSecPolicy(ctx, externalID, client.UpdateIPSecPolicyOpts{
				Name: strAny(data["name"]), Description: strAny(data["description"]), EncryptionAlgorithm: strAny(data["encryptionAlgorithm"]),
				PFS: strAny(data["pfs"]), TransformProtocol: strAny(data["transformProtocol"]), EncapsulationMode: strAny(data["encapsulationMode"]), AuthAlgorithm: strAny(data["authAlgorithm"]),
			})
			if err != nil {
				return failT(err)
			}
			return res(out)
		}
	case cloud.TypeIPSecSiteConnection:
		if action == "UPDATE" {
			out, err := cc.UpdateIPSecSiteConnection(ctx, externalID, client.UpdateIPSecSiteConnectionOpts{
				Name: strAny(data["name"]), Description: strAny(data["description"]), PeerAddress: strAny(data["peerAddress"]),
				PeerID: strAny(data["peerId"]), PSK: strAny(data["psk"]), Initiator: strAny(data["initiator"]), MTU: intAny(data["mtu"]),
			})
			if err != nil {
				return failT(err)
			}
			return res(out)
		}
	case cloud.TypeKubernetesCluster:
		switch action {
		case "RESIZE_CLUSTER":
			out, err := cc.ResizeCluster(ctx, externalID, intAny(data["nodeCount"]))
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "UPGRADE_CLUSTER":
			out, err := cc.UpgradeCluster(ctx, externalID, strAny(data["clusterTemplateId"]))
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "KUBE_CONFIG":
			out, err := cc.GetClusterCertificate(ctx, externalID)
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "LIST_TEMPLATES":
			out, err := cc.ListClusterTemplates(ctx)
			if err != nil {
				return failT(err)
			}
			return res(out)
		case "GET_TEMPLATE":
			out, err := cc.GetClusterTemplate(ctx, strAny(data["templateId"]))
			if err != nil {
				return failT(err)
			}
			return res(out)
		}
	case cloud.TypeBarbicanContainer:
		switch action {
		case "ADD_SECRET":
			if err := cc.AddContainerSecret(ctx, externalID, strAny(data["name"]), strAny(data["secretRef"])); err != nil {
				return failT(err)
			}
			return res(map[string]any{})
		case "REMOVE_SECRET":
			if err := cc.RemoveContainerSecret(ctx, externalID, strAny(data["name"]), strAny(data["secretRef"])); err != nil {
				return failT(err)
			}
			return res(map[string]any{})
		}
	case cloud.TypeUser:
		if action == "GENERATE_PASSWORD" {
			pw, err := cc.GeneratePassword(ctx, externalID)
			if err != nil {
				return failT(err)
			}
			return res(pw)
		}
	case cloud.TypeTrilioSnapshot:
		switch action {
		case "CANCEL":
			if err := cc.CancelSnapshot(ctx, externalID); err != nil {
				return failT(err)
			}
			return res(map[string]any{})
		case "MOUNT":
			if err := cc.MountSnapshot(ctx, externalID, strAny(data["mountVmId"])); err != nil {
				return failT(err)
			}
			return res(map[string]any{})
		case "DISMOUNT":
			if err := cc.DismountSnapshot(ctx, externalID); err != nil {
				return failT(err)
			}
			return res(map[string]any{})
		}
	case cloud.TypeTrilioWorkload:
		if action == "MODIFY" {
			obj, err := cc.UpdateWorkload(ctx, externalID, data)
			if err != nil {
				return failT(err)
			}
			return res(obj)
		}
	}
	return false
}

// suspendLike wraps a void-returning stack action into the {result} envelope.
func suspendLike(err error, failT func(error) bool, res func(any) bool) bool {
	if err != nil {
		return failT(err)
	}
	return res(map[string]any{})
}

// strAny coerces a free-form value to a string ("" when not a string).
func strAny(v any) string { s, _ := v.(string); return s }

// strMap coerces a free-form JSON object into a map[string]string (stringifying scalar values),
// for the object/bucket metadata maps the FE sends in action data.
func strMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return map[string]string{}
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		out[k] = strAny(val)
	}
	return out
}

// refreshBucketCache re-fetches the bucket's live size/count and persists the updated cache doc
// (a resource sync after a mutating object action). Best-effort, no-op for a nil cr.
func (h *Handler) refreshBucketCache(ctx context.Context, cc *client.Client, cr *cloud.CloudResource) {
	if cr == nil {
		return
	}
	if refreshResource(ctx, cc, cr) {
		if saved, err := h.cloud.Insert(ctx, cr); err == nil {
			*cr = *saved
		}
	}
}

// bucketResult returns the cached resource for an action that yields the updated resource;
// empty object when the bucket isn't cached.
func bucketResult(cr *cloud.CloudResource) any {
	if cr == nil {
		return map[string]any{}
	}
	return *cr
}

// intAny coerces a free-form JSON number/string to an int (0 when not numeric).
func intAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

// strSlice coerces a free-form JSON array to a []string (nil when not an array of strings).
func strSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// firstStr returns the first present non-empty string value among keys k.
func firstStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// prettyAction humanizes a nova instance-action name (e.g. "reboot_hard" → "Reboot Hard").
func prettyAction(a string) string {
	if a == "" {
		return a
	}
	words := strings.FieldsFunc(a, func(r rune) bool { return r == '_' || r == '-' || r == ' ' })
	for i, w := range words {
		if w == "" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
	}
	return strings.Join(words, " ")
}
