package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/menlocloud/stratos/internal/cloud/client"
	"github.com/menlocloud/stratos/internal/platform/externalservice"
	"github.com/menlocloud/stratos/pkg/httpx"
)

// cloudadmin.go serves the LIVE-cloud reads of the external-service admin surface +
// getPublicNetworks (cloud-admin). Each builds a per-externalService
// CloudClient (decrypted creds → client.Config → h.cloudNew) and queries the live region(s)
// READ-ONLY. Cloud errors degrade to an empty result so
// the cloud-provider config page always renders (per-region failures are cached/swallowed too).
//
// The matching PUT mutations (quota/features/volume-types/share-protocols/availability-zones/…) are
// in externalservicemut.go. VHI placement-quotas stays an empty stub in handler.go.

const cloudServiceCollection = "externalService"

// routeCloudAdmin registers the cloud-admin live reads.
func (h *Handler) routeCloudAdmin(r chi.Router) {
	r.Post("/service/openstack/auth", h.keystoneAuth)
	r.Get("/service/os-images", h.osImagesAll)
	r.Get("/service/regions", h.serviceRegionsList)
	r.Get("/service/{id}/os-images", h.osImagesByService)
	r.Get("/service/{id}/os-flavors", h.osFlavorsByService)
	// Kamaji chart-pin surface: list every managed cluster's pin; re-pin one or all onto the
	// provider's current chartVersion (the operator-side "platform update").
	r.Get("/service/{id}/k8s-clusters", h.kamajiClusterPins)
	r.Post("/service/{id}/k8s-clusters/bump-chart", h.kamajiClusterBumpAll)
	r.Post("/service/{id}/k8s-clusters/{clusterId}/bump-chart", h.kamajiClusterBump)
	r.Get("/service/{id}/gpu-info", h.gpuInfo)
	r.Get("/service/{id}/unpriced-flavors", h.unpricedFlavors)
	r.Get("/service/{id}/volume/types", h.volumeTypes)
	r.Get("/service/{id}/share/protocols", h.shareProtocols)
	r.Get("/service/{id}/availability-zones", h.availabilityZones)
	r.Get("/cloud-resource/public-networks/{id}", h.publicNetworks)
	// listFlavors — live flavors across all CLOUD services.
	r.Get("/flavor-categories/flavors", h.flavorsAll)
}

// flavorsAll handles listFlavors (GET /admin/flavor-categories/flavors):
// the live Nova flavors across every non-disabled CLOUD service + region, flattened.
// ADMIN_FLAVOR_CATEGORY_MANAGE. Cloud errors skip that service/region (best-effort, cached).
func (h *Handler) flavorsAll(w http.ResponseWriter, r *http.Request) {
	if !h.require(w, r, "admin:flavor_category:manage") {
		return
	}
	out := []client.Flavor{}
	if h.esSvc != nil {
		services, _ := h.esSvc.ListByType(r.Context(), externalservice.TypeCloud)
		for i := range services {
			es := &services[i]
			if es.IsDisabled() {
				continue
			}
			for _, region := range h.serviceRegions(es) {
				cc, err := h.cloudClient(r.Context(), es, region)
				if err != nil || cc == nil {
					continue
				}
				if fs, err := cc.ListFlavors(r.Context()); err == nil {
					out = append(out, fs...)
				}
			}
		}
	}
	httpx.List(w, out)
}

// ── helpers ────────────────────────────────────────────────────────────────

// cloudClient builds a READ-ONLY CloudClient for one externalService + region. Returns (nil,nil)
// when the cloud factory is unwired (tests) so callers degrade to an empty result.
func (h *Handler) cloudClient(ctx context.Context, es *externalservice.ExternalService, region string) (*client.Client, error) {
	if h.cloudNew == nil {
		return nil, nil
	}
	return h.cloudNew(ctx, es.ClientConfig(region))
}

// serviceRegions returns the service's configured regions, falling back to the platform default
// region (so a single-region dev service with no config.regions still resolves one scope).
func (h *Handler) serviceRegions(es *externalservice.ExternalService) []string {
	rs := es.RegionNames()
	if len(rs) == 0 {
		if h.region != "" {
			return []string{h.region}
		}
		return []string{""}
	}
	return rs
}

// regionMeta reads config.regions[region] → (displayName, country); blanks when absent.
func regionMeta(es *externalservice.ExternalService, region string) (displayName, country string) {
	regions, ok := es.Config["regions"].(map[string]any)
	if !ok {
		return "", ""
	}
	m, ok := regions[region].(map[string]any)
	if !ok {
		return "", ""
	}
	return str(m["displayName"]), str(m["country"])
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// loadServiceOr writes the not-found (HTTP 500 "Service not found: %s") and returns false when
// the externalService is absent (get throws ServiceNotFoundException).
func (h *Handler) loadServiceOr(w http.ResponseWriter, r *http.Request, id string) (*externalservice.ExternalService, bool) {
	if h.esSvc == nil {
		httpx.WriteError(w, serviceNotFoundErr(id))
		return nil, false
	}
	es, err := h.esSvc.Get(r.Context(), id)
	if err != nil || es == nil {
		httpx.WriteError(w, serviceNotFoundErr(id))
		return nil, false
	}
	return es, true
}

// ── DTOs (the response shapes) ──────────────────────────────────

// OpenstackImagesByLocationDto is the images-by-location response.
type OpenstackImagesByLocationDto struct {
	ServiceID         string         `json:"serviceId"`
	ServiceName       string         `json:"serviceName"`
	Region            string         `json:"region"`
	RegionDisplayName string         `json:"regionDisplayName"`
	Images            []client.Image `json:"images"`
}

// VolumeTypes is the volume-types response.
type VolumeTypes struct {
	Region      string   `json:"region"`
	VolumeTypes []string `json:"volumeTypes"`
}

// ShareProtocol is one share-protocol entry.
type ShareProtocol struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Enabled     bool   `json:"enabled"`
}

// Location is one region location entry.
type Location struct {
	Name        string `json:"name"`
	ServiceID   string `json:"serviceId"`
	ServiceName string `json:"serviceName"`
	Region      string `json:"region"`
	Country     string `json:"country,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
}

// ── handlers ───────────────────────────────────────────────────────────────

// osImagesByService handles getOsImages(externalServiceId): per region, the public Glance images,
// grouped into OpenstackImagesByLocationDto. ADMIN_SERVICE_READ.
func (h *Handler) osImagesByService(w http.ResponseWriter, r *http.Request) {
	if !h.require(w, r, "admin:service:read") {
		return
	}
	es, ok := h.loadServiceOr(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	httpx.List(w, h.imagesByLocation(r.Context(), es))
}

// osFlavorsByService lists the live Nova flavors of ONE service (all its regions, deduped by
// id) — the kamaji-provider form's flavor/image pickers browse a linked OpenStack provider
// instead of the operator hand-copying UUIDs out of Horizon. ADMIN_SERVICE_READ.
func (h *Handler) osFlavorsByService(w http.ResponseWriter, r *http.Request) {
	if !h.require(w, r, "admin:service:read") {
		return
	}
	es, ok := h.loadServiceOr(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	out := []client.Flavor{}
	seen := map[string]bool{}
	for _, region := range h.serviceRegions(es) {
		cc, err := h.cloudClient(r.Context(), es, region)
		if err != nil || cc == nil {
			continue
		}
		fs, err := cc.ListFlavors(r.Context())
		if err != nil {
			continue
		}
		for _, f := range fs {
			if !seen[f.ID] {
				seen[f.ID] = true
				out = append(out, f)
			}
		}
	}
	httpx.List(w, out)
}

// osImagesAll handles getOsImages(): public images across all non-disabled CLOUD services. ADMIN_SERVICE_READ.
func (h *Handler) osImagesAll(w http.ResponseWriter, r *http.Request) {
	if !h.require(w, r, "admin:service:read") {
		return
	}
	out := []OpenstackImagesByLocationDto{}
	if h.esSvc != nil {
		services, _ := h.esSvc.ListByType(r.Context(), externalservice.TypeCloud)
		for i := range services {
			es := &services[i]
			if es.IsDisabled() {
				continue
			}
			out = append(out, h.imagesByLocation(r.Context(), es)...)
		}
	}
	httpx.List(w, out)
}

// imagesByLocation lists each region's public images for one service (cloud errors → that region is
// skipped, cached/best-effort behavior).
func (h *Handler) imagesByLocation(ctx context.Context, es *externalservice.ExternalService) []OpenstackImagesByLocationDto {
	out := []OpenstackImagesByLocationDto{}
	for _, region := range h.serviceRegions(es) {
		cc, err := h.cloudClient(ctx, es, region)
		if err != nil || cc == nil {
			continue
		}
		imgs, err := cc.ListImages(ctx)
		if err != nil {
			continue
		}
		display, _ := regionMeta(es, region)
		out = append(out, OpenstackImagesByLocationDto{
			ServiceID: es.ID, ServiceName: es.Name, Region: region, RegionDisplayName: display, Images: imgs,
		})
	}
	return out
}

// GPURegionCapacity is the gpu-info response entry: one region's per-model GPU device counts.
type GPURegionCapacity struct {
	Region string               `json:"region"`
	Gpus   []client.GPUCapacity `json:"gpus"`
}

// gpuInfo handles GET /admin/service/{id}/gpu-info: per region, the cluster-wide GPU device
// capacity per model from Placement (see client.GPUInfo). Cloud errors skip that region
// (best-effort, same posture as the other live reads here). ADMIN_SERVICE_READ.
func (h *Handler) gpuInfo(w http.ResponseWriter, r *http.Request) {
	if !h.require(w, r, "admin:service:read") {
		return
	}
	es, ok := h.loadServiceOr(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	out := []GPURegionCapacity{}
	for _, region := range h.serviceRegions(es) {
		cc, err := h.cloudClient(r.Context(), es, region)
		if err != nil || cc == nil {
			continue
		}
		gpus, err := cc.GPUInfo(r.Context())
		if err != nil {
			continue
		}
		out = append(out, GPURegionCapacity{Region: region, Gpus: gpus})
	}
	httpx.List(w, out)
}

// volumeTypes handles getVolumeTypes: per region, the Cinder volume-type names. ADMIN_SERVICE_READ.
func (h *Handler) volumeTypes(w http.ResponseWriter, r *http.Request) {
	if !h.require(w, r, "admin:service:read") {
		return
	}
	es, ok := h.loadServiceOr(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	out := []VolumeTypes{}
	for _, region := range h.serviceRegions(es) {
		cc, err := h.cloudClient(r.Context(), es, region)
		if err != nil || cc == nil {
			continue
		}
		base, ok := endpointAny(cc, cinderServiceTypes...)
		if !ok {
			continue
		}
		var resp struct {
			VolumeTypes []struct {
				Name string `json:"name"`
			} `json:"volume_types"`
		}
		if err := cc.Do(r.Context(), "GET", strings.TrimRight(base, "/")+"/types", nil, &resp); err != nil {
			continue
		}
		names := make([]string, 0, len(resp.VolumeTypes))
		for _, vt := range resp.VolumeTypes {
			names = append(names, vt.Name)
		}
		out = append(out, VolumeTypes{Region: region, VolumeTypes: names})
	}
	httpx.List(w, out)
}

// shareProtocols handles getShareProtocols: the default protocol set, each overridden by the saved
// config.features.shareProtocols entry of the same name. NO cloud call. ADMIN_SERVICE_READ.
func (h *Handler) shareProtocols(w http.ResponseWriter, r *http.Request) {
	if !h.require(w, r, "admin:service:read") {
		return
	}
	es, ok := h.loadServiceOr(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	saved := savedShareProtocols(es)
	out := make([]ShareProtocol, 0, len(defaultShareProtocols))
	for _, def := range defaultShareProtocols {
		if s, ok := saved[strings.ToUpper(def.Name)]; ok {
			out = append(out, s)
		} else {
			out = append(out, def)
		}
	}
	httpx.List(w, out)
}

// defaultShareProtocols is the default share-protocol set.
var defaultShareProtocols = []ShareProtocol{
	{"NFS", "NFS", false},
	{"CIFS", "CIFS (SMB)", false},
	{"GlusterFS", "GlusterFS", false},
	{"HDFS", "HDFS (Hadoop)", false},
	{"CephFS", "CephFS", false},
	{"MAPRFS", "MapR-FS", false},
}

// savedShareProtocols reads config.features.shareProtocols → {NAME(upper): ShareProtocol}.
func savedShareProtocols(es *externalservice.ExternalService) map[string]ShareProtocol {
	out := map[string]ShareProtocol{}
	features, ok := es.Config["features"].(map[string]any)
	if !ok {
		return out
	}
	arr, ok := features["shareProtocols"].([]any)
	if !ok {
		return out
	}
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		name := str(m["name"])
		if name == "" {
			continue
		}
		enabled, _ := m["enabled"].(bool)
		display := str(m["displayName"])
		out[strings.ToUpper(name)] = ShareProtocol{Name: name, DisplayName: display, Enabled: enabled}
	}
	return out
}

// availabilityZones handles getAvailabilityZones: {serviceType → {region → [zones]}} across
// compute / volumev3 / network (manila/sharev2 left empty — not in the dev region). Cloud errors
// skip that service/region. Returns single(map). ADMIN_SERVICE_READ.
func (h *Handler) availabilityZones(w http.ResponseWriter, r *http.Request) {
	if !h.require(w, r, "admin:service:read") {
		return
	}
	es, ok := h.loadServiceOr(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	zones := map[string]map[string][]string{
		"compute": {}, "volumev3": {}, "network": {}, "sharev2": {},
	}
	for _, region := range h.serviceRegions(es) {
		cc, err := h.cloudClient(r.Context(), es, region)
		if err != nil || cc == nil {
			continue
		}
		if base, err := cc.EndpointURL("compute"); err == nil {
			if z := osAvailabilityZones(r.Context(), cc, base); len(z) > 0 {
				zones["compute"][region] = z
			}
		}
		if base, ok := endpointAny(cc, cinderServiceTypes...); ok {
			if z := osAvailabilityZones(r.Context(), cc, base); len(z) > 0 {
				zones["volumev3"][region] = z
			}
		}
		if z := neutronAZ(r.Context(), cc); len(z) > 0 {
			zones["network"][region] = z
		}
	}
	httpx.OK(w, zones)
}

// cinderServiceTypes are the catalog names a Cinder v3 endpoint may register under (varies by
// deployment); EndpointURL is tried in order.
var cinderServiceTypes = []string{"volumev3", "block-storage", "volumev2", "volume"}

// endpointAny resolves the first service type whose public endpoint exists in the token catalog.
func endpointAny(cc *client.Client, serviceTypes ...string) (string, bool) {
	for _, t := range serviceTypes {
		if u, err := cc.EndpointURL(t); err == nil && u != "" {
			return u, true
		}
	}
	return "", false
}

// osAvailabilityZones queries the standard `/os-availability-zone` (Nova + Cinder share the shape).
func osAvailabilityZones(ctx context.Context, cc *client.Client, base string) []string {
	var resp struct {
		AvailabilityZoneInfo []struct {
			ZoneName string `json:"zoneName"`
		} `json:"availabilityZoneInfo"`
	}
	if err := cc.Do(ctx, "GET", strings.TrimRight(base, "/")+"/os-availability-zone", nil, &resp); err != nil {
		return nil
	}
	out := make([]string, 0, len(resp.AvailabilityZoneInfo))
	for _, az := range resp.AvailabilityZoneInfo {
		out = append(out, az.ZoneName)
	}
	return out
}

// neutronAZ queries Neutron `/v2.0/availability_zones`.
func neutronAZ(ctx context.Context, cc *client.Client) []string {
	base, err := cc.EndpointURL("network")
	if err != nil {
		return nil
	}
	url := strings.TrimRight(base, "/")
	if !strings.Contains(url, "/v2.0") {
		url += "/v2.0"
	}
	var resp struct {
		AvailabilityZones []struct {
			Name string `json:"name"`
		} `json:"availability_zones"`
	}
	if err := cc.Do(ctx, "GET", url+"/availability_zones", nil, &resp); err != nil {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, az := range resp.AvailabilityZones {
		if az.Name != "" && !seen[az.Name] {
			seen[az.Name] = true
			out = append(out, az.Name)
		}
	}
	return out
}

// serviceRegionsList handles listRegions: the configured regions across all services → []Location.
// NO cloud call (pure config). ADMIN_SERVICE_READ.
func (h *Handler) serviceRegionsList(w http.ResponseWriter, r *http.Request) {
	if !h.require(w, r, "admin:service:read") {
		return
	}
	out := []Location{}
	if h.esSvc != nil {
		services, _ := h.esSvc.List(r.Context())
		for i := range services {
			es := &services[i]
			for _, region := range es.RegionNames() {
				display, country := regionMeta(es, region)
				out = append(out, Location{
					Name: region, ServiceID: es.ID, ServiceName: es.Name,
					Region: region, Country: country, DisplayName: display,
				})
			}
		}
	}
	httpx.List(w, out)
}

// publicNetworks handles getPublicNetworks(externalServiceId): the
// external (router:external) Neutron networks of the service's default region. ADMIN_CLOUD_RESOURCE_READ.
func (h *Handler) publicNetworks(w http.ResponseWriter, r *http.Request) {
	if !h.require(w, r, "admin:cloud_resource:read") {
		return
	}
	es, ok := h.loadServiceOr(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	out := []client.Network{}
	regions := h.serviceRegions(es)
	if len(regions) > 0 {
		if cc, err := h.cloudClient(r.Context(), es, regions[0]); err == nil && cc != nil {
			if nets, err := cc.ListExternalNetworks(r.Context()); err == nil {
				out = nets
			}
		}
	}
	httpx.List(w, out)
}

// keystoneAuthReq is the keystone-auth request body.
type keystoneAuthReq struct {
	Endpoint                    string `json:"endpoint"`
	DomainName                  string `json:"domainName"`
	Shared                      bool   `json:"shared"`
	DomainOnlyAdmin             bool   `json:"domainOnlyAdmin"`
	ProjectID                   string `json:"projectId"`
	AuthType                    string `json:"authType"`
	ApplicationCredentialID     string `json:"applicationCredentialId"`
	ApplicationCredentialSecret string `json:"applicationCredentialSecret"`
	AdminID                     string `json:"adminId"`
	AdminUsername               string `json:"adminUsername"`
	AdminPassword               string `json:"adminPassword"`
}

// keystoneAuth handles getOpenStackAuth (OpenstackAdmin path):
// authenticate (creds from the body, or the stored service via ?externalServiceId), then return what
// the token can see so the cloud-provider setup page can populate its Project / Domain dropdowns:
//   - projects  = GET /v3/auth/projects     (the projects the user can scope to)
//   - domains   = GET /v3/domains           (admin list-all)
//   - services  = GET /v3/auth/catalog      (the token's service catalog)
//   - roles     = GET /v3/roles             (role names)
//   - selected* = the "admin" project (else first) + the request-domain (else first)
//
// ADMIN_SERVICE_READ. Auth failure → 400 (this is a connection test). Cloud factory unwired → empty.
func (h *Handler) keystoneAuth(w http.ResponseWriter, r *http.Request) {
	if !h.require(w, r, "admin:service:read") {
		return
	}
	var req keystoneAuthReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, httpx.BadRequest("Invalid request body"))
		return
	}
	cfg, base, ok := h.keystoneConfig(r.Context(), req, r.URL.Query().Get("externalServiceId"))
	if !ok || h.cloudNew == nil {
		httpx.OK(w, OpenstackAuthResponse{Services: []any{}, Projects: []any{}, Domains: []any{}, IdentityProviders: []any{}, Roles: []string{}})
		return
	}
	cc, err := h.cloudNew(r.Context(), cfg)
	if err != nil {
		httpx.WriteError(w, httpx.BadRequest("OpenStack authentication failed: "+err.Error()))
		return
	}
	projects := keystoneArray(r.Context(), cc, base, "/auth/projects", "projects")
	domains := keystoneArray(r.Context(), cc, base, "/domains", "domains")
	services := keystoneArray(r.Context(), cc, base, "/auth/catalog", "catalog")
	selPID, selPName := pickByName(projects, "admin")
	selDID, selDName := pickByName(domains, req.DomainName)
	httpx.OK(w, OpenstackAuthResponse{
		Services:            services,
		Projects:            projects,
		Domains:             domains,
		IdentityProviders:   []any{},
		Roles:               keystoneRoleNames(r.Context(), cc, base),
		SelectedProjectID:   selPID,
		SelectedProjectName: selPName,
		SelectedDomainID:    selDID,
		SelectedDomainName:  selDName,
	})
}

// keystoneConfig builds the client.Config + the identity v3 base URL from the request creds, or from
// the stored service when externalServiceId is given (the FE re-tests an existing provider without
// resending the password).
func (h *Handler) keystoneConfig(ctx context.Context, req keystoneAuthReq, externalServiceID string) (client.Config, string, bool) {
	if externalServiceID != "" && req.AdminPassword == "" && req.ApplicationCredentialSecret == "" && h.esSvc != nil {
		if es, err := h.esSvc.Get(ctx, externalServiceID); err == nil && es != nil {
			return es.ClientConfig(h.region), es.IdentityURL(), true
		}
	}
	if req.Endpoint == "" {
		return client.Config{}, "", false
	}
	base := ensureV3(req.Endpoint)
	cfg := client.Config{AuthURL: base, Region: h.region}
	if strings.EqualFold(req.AuthType, "application_credential") {
		cfg.AppCredID = req.ApplicationCredentialID
		cfg.AppCredSecret = req.ApplicationCredentialSecret
		return cfg, base, true
	}
	cfg.Username = req.AdminUsername
	cfg.Password = req.AdminPassword
	cfg.UserDomainName = req.DomainName
	cfg.ProjectID = req.ProjectID // empty → unscoped auth (lets the dropdowns load before a project is picked)
	cfg.ProjectDomainName = req.DomainName
	return cfg, base, true
}

// ensureV3 normalizes a keystone URL to end with /v3 (mirrors externalservice.ensureV3).
func ensureV3(url string) string {
	t := strings.TrimRight(url, "/")
	if t == "" || strings.HasSuffix(t, "/v3") {
		return t
	}
	return t + "/v3"
}

// keystoneArray GETs an identity sub-resource off the v3 base (e.g. /auth/projects) and returns its
// array under `key` as []any (raw maps passed through). Empty on any error.
func keystoneArray(ctx context.Context, cc *client.Client, base, path, key string) []any {
	var resp map[string]any
	if err := cc.Do(ctx, "GET", strings.TrimRight(base, "/")+path, nil, &resp); err != nil {
		return []any{}
	}
	if arr, ok := resp[key].([]any); ok {
		return arr
	}
	return []any{}
}

// keystoneRoleNames GETs /v3/roles → the role names. Empty on error.
func keystoneRoleNames(ctx context.Context, cc *client.Client, base string) []string {
	var resp struct {
		Roles []struct {
			Name string `json:"name"`
		} `json:"roles"`
	}
	if err := cc.Do(ctx, "GET", strings.TrimRight(base, "/")+"/roles", nil, &resp); err != nil {
		return []string{}
	}
	out := make([]string, 0, len(resp.Roles))
	for _, r := range resp.Roles {
		out = append(out, r.Name)
	}
	return out
}

// pickByName returns the (id, name) of the array element whose name == want, else the first element's
// (id, name), else ("",""). Elements are raw keystone maps. Backs the selected project/domain.
func pickByName(items []any, want string) (id, name string) {
	first := true
	var fid, fname string
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if first {
			fid, fname, first = str(m["id"]), str(m["name"]), false
		}
		if str(m["name"]) == want {
			return str(m["id"]), str(m["name"])
		}
	}
	return fid, fname
}
