// Package client is the OpenStack CloudClient facade. It hides whether a call
// goes through gophercloud or a direct-REST client (gnocchi/Ostor land later) so the
// higher cloud layers (CloudResource providers, usage ingestion) never see the SDK.
//
// Current scope: Keystone v3 authentication (password or application-credential,
// project-scoped, with reauth) + READ-ONLY service reads (flavors/images/networks).
// Write providers + the per-region ExternalService-backed config land in later phases.
package client

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/imagedata"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/external"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
)

// Config carries the connection parameters for one OpenStack scope. Sourced from env/
// config initially; in later phases a per-region ExternalService (datastore) supplies these.
// Prefer an application credential (AppCredID/Secret) for the service account; the
// username/password path is the dev fallback.
type Config struct {
	AuthURL           string // Keystone v3 endpoint, e.g. https://host:5000/v3
	Region            string // e.g. RegionOne
	Username          string
	Password          string
	UserDomainName    string // e.g. Default
	ProjectName       string
	ProjectID         string // scope by project id (ExternalService config.auth.adminProjectId); wins over ProjectName
	ProjectDomainName string
	AppCredID         string
	AppCredSecret     string
}

func (c Config) authOptions() gophercloud.AuthOptions {
	if c.AppCredID != "" {
		return gophercloud.AuthOptions{
			IdentityEndpoint:            c.AuthURL,
			ApplicationCredentialID:     c.AppCredID,
			ApplicationCredentialSecret: c.AppCredSecret,
			AllowReauth:                 true,
		}
	}
	opts := gophercloud.AuthOptions{
		IdentityEndpoint: c.AuthURL,
		Username:         c.Username,
		Password:         c.Password,
		DomainName:       c.UserDomainName,
		AllowReauth:      true, // wires gophercloud's ReauthFunc for token re-authentication
	}
	// No project id/name → UNSCOPED token (Scope nil) — keystone accepts it and it lets the
	// connection-test / dropdown-population calls run before a project is selected. With a project,
	// scope by id (wins) else by name + project domain.
	if c.ProjectID == "" && c.ProjectName == "" {
		return opts
	}
	scope := &gophercloud.AuthScope{DomainName: c.ProjectDomainName} // the domain that CONTAINS the project
	if c.ProjectID != "" {
		scope.ProjectID = c.ProjectID
		scope.DomainName = ""
	} else {
		scope.ProjectName = c.ProjectName
	}
	opts.Scope = scope
	return opts
}

// Client is the authenticated facade over one OpenStack scope.
type Client struct {
	provider  *gophercloud.ProviderClient
	region    string
	projectID string // the scoped tenant — used to project-filter neutron list calls (the
	// service account is cloud-admin, so unfiltered neutron lists return ALL tenants' resources).
}

// New authenticates against Keystone v3 and returns the facade. The ProviderClient
// transparently re-authenticates on token expiry (AllowReauth).
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.AuthURL == "" {
		return nil, fmt.Errorf("cloud: AuthURL required")
	}
	provider, err := openstack.AuthenticatedClient(ctx, cfg.authOptions())
	if err != nil {
		return nil, fmt.Errorf("cloud: authenticate: %w", err)
	}
	return &Client{provider: provider, region: cfg.Region, projectID: cfg.ProjectID}, nil
}

func (c *Client) endpointOpts() gophercloud.EndpointOpts {
	return gophercloud.EndpointOpts{Region: c.region}
}

// EndpointURL resolves the public endpoint for an OpenStack service type from the token
// catalog (e.g. "metric" for Gnocchi, which gophercloud has no typed client for).
func (c *Client) EndpointURL(serviceType string) (string, error) {
	return c.provider.EndpointLocator(gophercloud.EndpointOpts{
		Type: serviceType, Region: c.region, Availability: gophercloud.AvailabilityPublic,
	})
}

// Do performs an authenticated REST call (token attached + reauth) for services without a
// gophercloud client — the direct-REST path (Gnocchi, Ostor, …). body/out are JSON; pass
// the acceptable status codes (default {200}). This keeps the facade the single place that
// knows about the SDK transport.
func (c *Client) Do(ctx context.Context, method, url string, body, out any, okCodes ...int) error {
	if len(okCodes) == 0 {
		okCodes = []int{200}
	}
	opts := &gophercloud.RequestOpts{OkCodes: okCodes}
	if body != nil {
		opts.JSONBody = body
	}
	if out != nil {
		opts.JSONResponse = out
	}
	_, err := c.provider.Request(ctx, method, url, opts)
	return err
}

// Flavor is the facade view of a Nova flavor (read-only fields the platform needs).
type Flavor struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	VCPUs      int               `json:"vcpus"`
	RAM        int               `json:"ram"`
	Disk       int               `json:"disk"`
	ExtraSpecs map[string]string `json:"extra_specs"`
}

// flavorMicroversion: nova ≥ 2.61 embeds extra_specs in flavor list/get responses —
// needed to see GPU passthrough aliases (pci_passthrough:alias) without per-flavor calls.
const flavorMicroversion = "2.61"

// ListFlavors returns the project's Nova flavors (read-only).
func (c *Client) ListFlavors(ctx context.Context) ([]Flavor, error) {
	cc, err := openstack.NewComputeV2(c.provider, c.endpointOpts())
	if err != nil {
		return nil, err
	}
	cc.Microversion = flavorMicroversion
	pages, err := flavors.ListDetail(cc, flavors.ListOpts{}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	fs, err := flavors.ExtractFlavors(pages)
	if err != nil {
		return nil, err
	}
	out := make([]Flavor, 0, len(fs))
	for _, f := range fs {
		es := f.ExtraSpecs
		if es == nil {
			es = map[string]string{}
		}
		out = append(out, Flavor{ID: f.ID, Name: f.Name, VCPUs: f.VCPUs, RAM: f.RAM, Disk: f.Disk, ExtraSpecs: es})
	}
	return out, nil
}

// GetFlavor resolves one Nova flavor to its specs as a free-form map (id/name/ram/vcpus/disk +
// originalName + extra_specs). Used to enrich a server's `flavor:{id,links}` (the newer nova
// microversion omits specs) so the client server-detail/list shows real vCPU/RAM/disk instead of
// NaN, and so the billing cron can rate GPUs (pci_passthrough:alias) from the cached doc.
// extra_specs is always present (possibly empty) — the enrich path keys idempotence on it.
func (c *Client) GetFlavor(ctx context.Context, id string) (map[string]any, error) {
	cc, err := openstack.NewComputeV2(c.provider, c.endpointOpts())
	if err != nil {
		return nil, err
	}
	cc.Microversion = flavorMicroversion
	f, err := flavors.Get(ctx, cc, id).Extract()
	if err != nil {
		return nil, err
	}
	es := f.ExtraSpecs
	if es == nil {
		es = map[string]string{}
	}
	return map[string]any{
		"id": f.ID, "name": f.Name, "originalName": f.Name, "original_name": f.Name,
		"ram": f.RAM, "vcpus": f.VCPUs, "disk": f.Disk, "extra_specs": es,
	}, nil
}

// Image is the facade view of a Glance image.
type Image struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// ListImages returns the project's Glance images (read-only).
func (c *Client) ListImages(ctx context.Context) ([]Image, error) {
	ic, err := openstack.NewImageV2(c.provider, c.endpointOpts())
	if err != nil {
		return nil, err
	}
	pages, err := images.List(ic, images.ListOpts{}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	is, err := images.ExtractImages(pages)
	if err != nil {
		return nil, err
	}
	out := make([]Image, 0, len(is))
	for _, im := range is {
		out = append(out, Image{ID: im.ID, Name: im.Name, Status: string(im.Status)})
	}
	return out, nil
}

// ListImagesFull returns glance images as the `data.image` maps the create-server wizard reads
// (id/name/status/size/min_disk/min_ram/visibility + os_distro/os_version from properties). Both
// snake_case and camelCase keys are emitted since the recovered FE's exact binding is uncertain.
func (c *Client) ListImagesFull(ctx context.Context) ([]map[string]any, error) {
	ic, err := openstack.NewImageV2(c.provider, c.endpointOpts())
	if err != nil {
		return nil, err
	}
	pages, err := images.List(ic, images.ListOpts{}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	is, err := images.ExtractImages(pages)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(is))
	for i := range is {
		out = append(out, imageToMap(&is[i]))
	}
	return out, nil
}

// ListImagesOwned returns only the glance images OWNED by the given tenant, as `data.image`
// maps (glance image list filtered by owner=externalProjectId).
// The owner filter is the sync leak-guard: an unfiltered glance list also returns public/
// shared/community images from other tenants, which would pollute the project's cache and
// get billed (the dev125/187 leak class).
func (c *Client) ListImagesOwned(ctx context.Context, owner string) ([]map[string]any, error) {
	ic, err := openstack.NewImageV2(c.provider, c.endpointOpts())
	if err != nil {
		return nil, err
	}
	pages, err := images.List(ic, images.ListOpts{Owner: owner}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	is, err := images.ExtractImages(pages)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(is))
	for i := range is {
		out = append(out, imageToMap(&is[i]))
	}
	return out, nil
}

// imageToMap shapes one glance image into the `data.image` map the FE/cache read.
// Times are RFC3339 STRINGS (glance's wire format), not time.Time: a time.Time value round-trips
// through the datastore as primitive.DateTime, which the keyed compare's scalar DeepEqual never matches
// → every sync pass "differs" → update churn (live-caught on kolla, dev216).
func imageToMap(im *images.Image) map[string]any {
	created := im.CreatedAt.UTC().Format(time.RFC3339)
	updated := im.UpdatedAt.UTC().Format(time.RFC3339)
	m := map[string]any{
		"id": im.ID, "name": im.Name, "status": string(im.Status),
		"owner": im.Owner, "created_at": created, "createdAt": created,
		"updated_at": updated, "updatedAt": updated,
		"size": im.SizeBytes, "min_disk": im.MinDiskGigabytes, "min_ram": im.MinRAMMegabytes,
		"minDisk": im.MinDiskGigabytes, "minRam": im.MinRAMMegabytes,
		"visibility": string(im.Visibility),
	}
	if im.Properties != nil {
		if v, ok := im.Properties["os_distro"]; ok {
			m["os_distro"], m["osDistro"] = v, v
		}
		if v, ok := im.Properties["os_version"]; ok {
			m["os_version"], m["osVersion"] = v, v
		}
		// Snapshot-identifying props (stamped when a server snapshot is taken): a server
		// snapshot is a glance image with image_type=snapshot + instance_uuid=<serverId>. The
		// client Snapshots tab filters `type=IMAGE&dataAssociatedTo=<serverId>` by instance_uuid.
		if v, ok := im.Properties["image_type"]; ok {
			m["image_type"], m["imageType"] = v, v
		}
		if v, ok := im.Properties["instance_uuid"]; ok {
			m["instance_uuid"], m["instanceUuid"] = v, v
		}
	}
	return m
}

// GetImage fetches one glance image as the `data.image` map (os-notification re-fetch). nil if absent.
func (c *Client) GetImage(ctx context.Context, id string) (map[string]any, error) {
	ic, err := openstack.NewImageV2(c.provider, c.endpointOpts())
	if err != nil {
		return nil, err
	}
	im, err := images.Get(ctx, ic, id).Extract()
	if err != nil {
		return nil, err
	}
	if im == nil {
		return nil, nil
	}
	return imageToMap(im), nil
}

// DeleteImage removes a Glance image.
func (c *Client) DeleteImage(ctx context.Context, id string) error {
	ic, err := openstack.NewImageV2(c.provider, c.endpointOpts())
	if err != nil {
		return err
	}
	return images.Delete(ctx, ic, id).ExtractErr()
}

// UploadImage streams file bytes into an existing Glance image (glance image-data PUT).
func (c *Client) UploadImage(ctx context.Context, imageID string, data io.Reader) error {
	ic, err := openstack.NewImageV2(c.provider, c.endpointOpts())
	if err != nil {
		return err
	}
	return imagedata.Upload(ctx, ic, imageID, data).ExtractErr()
}

// ListAvailabilityZones returns the compute (Nova) availability zones — the create-server wizard's
// location AZ select (the bulk action LIST_AVAILABILITY_ZONES). Each carries name + availability.
func (c *Client) ListAvailabilityZones(ctx context.Context) ([]map[string]any, error) {
	base, err := c.EndpointURL("compute")
	if err != nil {
		return nil, err
	}
	var resp struct {
		AvailabilityZoneInfo []struct {
			ZoneName  string `json:"zoneName"`
			ZoneState struct {
				Available bool `json:"available"`
			} `json:"zoneState"`
		} `json:"availabilityZoneInfo"`
	}
	if err := c.Do(ctx, "GET", strings.TrimRight(base, "/")+"/os-availability-zone", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(resp.AvailabilityZoneInfo))
	for _, az := range resp.AvailabilityZoneInfo {
		out = append(out, map[string]any{
			"name": az.ZoneName, "zoneName": az.ZoneName,
			"available": az.ZoneState.Available, "enabled": true,
		})
	}
	return out, nil
}

// Network is the facade view of a Neutron network.
type Network struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListNetworks returns the project's Neutron networks (read-only).
func (c *Client) ListNetworks(ctx context.Context) ([]Network, error) {
	nc, err := openstack.NewNetworkV2(c.provider, c.endpointOpts())
	if err != nil {
		return nil, err
	}
	pages, err := networks.List(nc, networks.ListOpts{}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	ns, err := networks.ExtractNetworks(pages)
	if err != nil {
		return nil, err
	}
	out := make([]Network, 0, len(ns))
	for _, n := range ns {
		out = append(out, Network{ID: n.ID, Name: n.Name})
	}
	return out, nil
}

// Server is the facade view of a Nova server, carrying the fields the rating path reads
// (flavor ram/vcpus/disk, flavorName, name/host/status/availabilityZone, image id). Flavor details are resolved
// from the flavor id (the list view embeds only the id) via a per-call cache.
type Server struct {
	ID               string
	Name             string
	Status           string
	Host             string
	AvailabilityZone string
	ImageID          string
	FlavorID         string
	FlavorName       string
	RAM              int // MB
	VCPUs            int
	Disk             int // GB
	// FlavorExtraSpecs feeds GPU rating (pci_passthrough:alias) — the sync cache MUST
	// carry it or GPU servers silently bill zero (rules filter on gpu_model).
	FlavorExtraSpecs map[string]string
	Metadata         map[string]string
	Created          time.Time
	Updated          time.Time
}

// ListServers returns the project's Nova servers (detail), resolving each server's flavor
// (cached by id) so ram/vcpus/disk/originalName AND extra_specs (GPU rating) are populated
// for the rating mapping. READ-ONLY.
func (c *Client) ListServers(ctx context.Context) ([]Server, error) {
	cc, err := openstack.NewComputeV2(c.provider, c.endpointOpts())
	if err != nil {
		return nil, err
	}
	cc.Microversion = flavorMicroversion
	pages, err := servers.List(cc, servers.ListOpts{}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	ss, err := servers.ExtractServers(pages)
	if err != nil {
		return nil, err
	}
	flavorCache := map[string]*flavors.Flavor{}
	out := make([]Server, 0, len(ss))
	for _, s := range ss {
		srv := Server{
			ID: s.ID, Name: s.Name, Status: s.Status, Host: s.HostID,
			AvailabilityZone: s.AvailabilityZone, Metadata: s.Metadata,
			Created: s.Created, Updated: s.Updated,
			ImageID:  mapID(s.Image),
			FlavorID: mapID(s.Flavor),
		}
		if srv.FlavorID != "" {
			f := flavorCache[srv.FlavorID]
			if f == nil {
				if got, gerr := flavors.Get(ctx, cc, srv.FlavorID).Extract(); gerr == nil {
					f = got
					flavorCache[srv.FlavorID] = f
				}
			}
			if f != nil {
				srv.FlavorName, srv.RAM, srv.VCPUs, srv.Disk = f.Name, f.RAM, f.VCPUs, f.Disk
				srv.FlavorExtraSpecs = f.ExtraSpecs
				if srv.FlavorExtraSpecs == nil {
					srv.FlavorExtraSpecs = map[string]string{}
				}
			}
		}
		out = append(out, srv)
	}
	return out, nil
}

// ListExternalNetworks returns the project's PUBLIC (router:external) Neutron networks
// — the set the metrics classifier treats as public traffic. READ-ONLY.
func (c *Client) ListExternalNetworks(ctx context.Context) ([]Network, error) {
	nc, err := openstack.NewNetworkV2(c.provider, c.endpointOpts())
	if err != nil {
		return nil, err
	}
	isExternal := true
	pages, err := networks.List(nc, external.ListOptsExt{
		ListOptsBuilder: networks.ListOpts{},
		External:        &isExternal,
	}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	ns, err := networks.ExtractNetworks(pages)
	if err != nil {
		return nil, err
	}
	out := make([]Network, 0, len(ns))
	for _, n := range ns {
		out = append(out, Network{ID: n.ID, Name: n.Name})
	}
	return out, nil
}

// Port is the facade view of a Neutron port (the fields the metrics classifier + a future
// port billing provider read: networkId, deviceId/owner, mac, status).
type Port struct {
	ID          string
	NetworkID   string
	Name        string
	DeviceID    string
	DeviceOwner string
	MACAddress  string
	Status      string
}

// ListPorts returns the project's Neutron ports (read-only).
func (c *Client) ListPorts(ctx context.Context) ([]Port, error) {
	nc, err := openstack.NewNetworkV2(c.provider, c.endpointOpts())
	if err != nil {
		return nil, err
	}
	// Filter by the scoped tenant — a neutron ADMIN token lists EVERY tenant's ports otherwise (the
	// sync would pull the whole region into this project). Empty projectID → unfiltered (admin probe).
	pages, err := ports.List(nc, ports.ListOpts{ProjectID: c.projectID}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	ps, err := ports.ExtractPorts(pages)
	if err != nil {
		return nil, err
	}
	out := make([]Port, 0, len(ps))
	for _, p := range ps {
		out = append(out, Port{
			ID: p.ID, NetworkID: p.NetworkID, Name: p.Name,
			DeviceID: p.DeviceID, DeviceOwner: p.DeviceOwner,
			MACAddress: p.MACAddress, Status: p.Status,
		})
	}
	return out, nil
}

// ListPortsFull returns Neutron ports as the free-form `data.port` maps the client server-detail
// Networking tab reads (id/name/network_id/device_id/device_owner/mac_address/status/fixed_ips).
// When deviceID is set the list is filtered to that server's ports (by device_id).
func (c *Client) ListPortsFull(ctx context.Context, deviceID string) ([]map[string]any, error) {
	nc, err := openstack.NewNetworkV2(c.provider, c.endpointOpts())
	if err != nil {
		return nil, err
	}
	pages, err := ports.List(nc, ports.ListOpts{DeviceID: deviceID, ProjectID: c.projectID}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	ps, err := ports.ExtractPorts(pages)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(ps))
	for _, p := range ps {
		fixed := make([]map[string]any, 0, len(p.FixedIPs))
		for _, ip := range p.FixedIPs {
			fixed = append(fixed, map[string]any{
				"subnet_id": ip.SubnetID, "subnetId": ip.SubnetID,
				"ip_address": ip.IPAddress, "ipAddress": ip.IPAddress,
			})
		}
		m := map[string]any{
			"id": p.ID, "name": p.Name,
			"network_id": p.NetworkID, "networkId": p.NetworkID,
			"device_id": p.DeviceID, "deviceId": p.DeviceID,
			"device_owner": p.DeviceOwner, "deviceOwner": p.DeviceOwner,
			"mac_address": p.MACAddress, "macAddress": p.MACAddress,
			"status": p.Status, "admin_state_up": p.AdminStateUp,
			"fixed_ips": fixed, "fixedIps": fixed,
			"security_groups": p.SecurityGroups, "securityGroups": p.SecurityGroups,
		}
		// created_at as an RFC3339 STRING (wire format; round-trip-stable — never time.Time
		// in a data map). Feeds the UI Created-At column via liveCreatedAt.
		if !p.CreatedAt.IsZero() {
			m["created_at"] = p.CreatedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, m)
	}
	return out, nil
}

// ListFloatingIPsFull returns the project's Neutron floating IPs as free-form `data.floatingIp`
// maps (id/floating_ip_address/status/port_id/router_id/fixed_ip_address/floating_network_id).
func (c *Client) ListFloatingIPsFull(ctx context.Context) ([]map[string]any, error) {
	base, err := c.EndpointURL("network")
	if err != nil {
		return nil, err
	}
	var resp struct {
		FloatingIPs []map[string]any `json:"floatingips"`
	}
	url := strings.TrimRight(base, "/") + "/v2.0/floatingips"
	if c.projectID != "" {
		url += "?project_id=" + c.projectID
	}
	if err := c.Do(ctx, "GET", url, nil, &resp); err != nil {
		return nil, err
	}
	for _, f := range resp.FloatingIPs {
		// camelCase aliases for the recovered FE binding.
		if v, ok := f["floating_ip_address"]; ok {
			f["floatingIpAddress"] = v
		}
		if v, ok := f["port_id"]; ok {
			f["portId"] = v
		}
		if v, ok := f["floating_network_id"]; ok {
			f["floatingNetworkId"] = v
		}
		if v, ok := f["fixed_ip_address"]; ok {
			f["fixedIpAddress"] = v
		}
	}
	return resp.FloatingIPs, nil
}

// ListSecurityGroups returns the project's Neutron security groups as free-form maps
// (id/name/description/security_group_rules) — the SECURITY_GROUP resource list + the
// create-server / detail Security-Groups selection source.
func (c *Client) ListSecurityGroups(ctx context.Context) ([]map[string]any, error) {
	base, err := c.EndpointURL("network")
	if err != nil {
		return nil, err
	}
	var resp struct {
		SecurityGroups []map[string]any `json:"security_groups"`
	}
	url := strings.TrimRight(base, "/") + "/v2.0/security-groups"
	if c.projectID != "" {
		url += "?project_id=" + c.projectID
	}
	if err := c.Do(ctx, "GET", url, nil, &resp); err != nil {
		return nil, err
	}
	for _, sg := range resp.SecurityGroups {
		if v, ok := sg["security_group_rules"]; ok {
			sg["securityGroupRules"] = v
			sg["rules"] = v
		}
	}
	return resp.SecurityGroups, nil
}

// ListServerSecurityGroups returns the security groups attached to one server (Nova
// /servers/{id}/os-security-groups), each carrying id/name/description/rules.
func (c *Client) ListServerSecurityGroups(ctx context.Context, serverID string) ([]map[string]any, error) {
	base, err := c.EndpointURL("compute")
	if err != nil {
		return nil, err
	}
	var resp struct {
		SecurityGroups []map[string]any `json:"security_groups"`
	}
	if err := c.Do(ctx, "GET", strings.TrimRight(base, "/")+"/servers/"+serverID+"/os-security-groups", nil, &resp); err != nil {
		return nil, err
	}
	for _, sg := range resp.SecurityGroups {
		if v, ok := sg["rules"]; ok {
			sg["securityGroupRules"] = v
			sg["security_group_rules"] = v
		}
	}
	return resp.SecurityGroups, nil
}

// neutronGetByID fetches one Neutron resource by id (the singular collection wraps it under
// `key`, e.g. ports/{id}→{port}, floatingips/{id}→{floatingip}). Returns the inner object.
func (c *Client) neutronGetByID(ctx context.Context, collection, key, id string) (map[string]any, error) {
	base, err := c.EndpointURL("network")
	if err != nil {
		return nil, err
	}
	var resp map[string]any
	if err := c.Do(ctx, "GET", strings.TrimRight(base, "/")+"/v2.0/"+collection+"/"+id, nil, &resp); err != nil {
		return nil, err
	}
	if inner, ok := resp[key].(map[string]any); ok {
		return inner, nil
	}
	return nil, fmt.Errorf("neutron %s/%s: no %s in response", collection, id, key)
}

// GetPort / GetFloatingIP / GetSecurityGroup / GetRouter re-read one Neutron resource by id
// (the client resource-detail pages resolve a live resource by its externalId).
func (c *Client) GetPort(ctx context.Context, id string) (map[string]any, error) {
	return c.neutronGetByID(ctx, "ports", "port", id)
}
func (c *Client) GetFloatingIP(ctx context.Context, id string) (map[string]any, error) {
	return c.neutronGetByID(ctx, "floatingips", "floatingip", id)
}
func (c *Client) GetSecurityGroup(ctx context.Context, id string) (map[string]any, error) {
	return c.neutronGetByID(ctx, "security-groups", "security_group", id)
}
func (c *Client) GetRouter(ctx context.Context, id string) (map[string]any, error) {
	return c.neutronGetByID(ctx, "routers", "router", id)
}

// GetVNCConsole opens a Nova remote VNC console for a server (nova remote-consoles)
// and returns {url, type} — the client "Console" button opens result.url.
func (c *Client) GetVNCConsole(ctx context.Context, id string) (map[string]any, error) {
	base, err := c.EndpointURL("compute")
	if err != nil {
		return nil, err
	}
	body := map[string]any{"remote_console": map[string]any{"protocol": "vnc", "type": "novnc"}}
	var resp struct {
		RemoteConsole map[string]any `json:"remote_console"`
	}
	// The remote-consoles API requires nova microversion >= 2.6 (default 2.1 → 404).
	opts := &gophercloud.RequestOpts{
		JSONBody: body, JSONResponse: &resp, OkCodes: []int{200},
		MoreHeaders: map[string]string{"X-OpenStack-Nova-API-Version": "2.6"},
	}
	if _, err := c.provider.Request(ctx, "POST", strings.TrimRight(base, "/")+"/servers/"+id+"/remote-consoles", opts); err != nil {
		return nil, err
	}
	return resp.RemoteConsole, nil
}

// GetServerPassword returns a nova server's ENCRYPTED admin password (nova os-server-password GET) —
// a base64 blob RSA-encrypted with the instance's keypair public key. It is decrypted client-side
// with the user's private key (never server-side; the platform holds no private keys). Empty string
// when no password has been set (a Linux/no-keypair instance).
func (c *Client) GetServerPassword(ctx context.Context, id string) (string, error) {
	base, err := c.EndpointURL("compute")
	if err != nil {
		return "", err
	}
	var resp struct {
		Password string `json:"password"`
	}
	opts := &gophercloud.RequestOpts{JSONResponse: &resp, OkCodes: []int{200}}
	if _, err := c.provider.Request(ctx, "GET", strings.TrimRight(base, "/")+"/servers/"+id+"/os-server-password", opts); err != nil {
		return "", err
	}
	return resp.Password, nil
}

// ListServerActions returns one server's Nova instance-action log (the Server Activity panel /
// LIST_EVENTS — os-instance-actions), newest first, each
// carrying action/message/start_time/request_id/user_id.
func (c *Client) ListServerActions(ctx context.Context, serverID string) ([]map[string]any, error) {
	base, err := c.EndpointURL("compute")
	if err != nil {
		return nil, err
	}
	var resp struct {
		InstanceActions []map[string]any `json:"instanceActions"`
	}
	if err := c.Do(ctx, "GET", strings.TrimRight(base, "/")+"/servers/"+serverID+"/os-instance-actions", nil, &resp); err != nil {
		return nil, err
	}
	return resp.InstanceActions, nil
}

// mapID extracts an "id" from a Nova embedded reference (Image/Flavor are JSON objects in
// the detail view, e.g. {"id":"…","links":[…]}). Returns "" when absent.
func mapID(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if id, ok := m["id"].(string); ok {
		return id
	}
	return ""
}
