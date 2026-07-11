package client

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/snapshots"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/keypairs"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servergroups"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/volumeattach"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/floatingips"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/routers"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/mtu"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/portsecurity"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/groups"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/rules"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/subnets"
)

// write.go = the networking WRITE surface on the CloudClient facade (network / subnet / router
// create/delete). Each create
// returns the created object as a free-form map[string]any — the same shape CloudResource.data
// stores and the notification fetcher returns — so the SDK type never leaks past the facade.
//
// CONSTRAINT (user): automated create paths build INTERNAL networks only (router:external is
// never set → false). Creating an EXTERNAL network is intentionally NOT offered here; the
// operator pre-creates external nets (e.g. "guest") on Horizon. Internal nets attach OUT to a
// pre-existing external net via a router gateway (CreateRouter ExternalGatewayNetworkID).

func toMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}

func (c *Client) net() (*gophercloud.ServiceClient, error) {
	return openstack.NewNetworkV2(c.provider, c.endpointOpts())
}

// CreateNetworkOpts mirrors the internal-network fields of CreateNetworkRequest. No
// external/shared/router:external knob — internal VPC only (admin-state-up defaults true).
type CreateNetworkOpts struct {
	Name                  string
	AdminStateUp          *bool    // nil → true
	AvailabilityZoneHints []string // neutron availability_zone_hints
	MTU                   int      // 0 → don't set (OpenStack picks the provider default)
}

// CreateNetwork creates an INTERNAL Neutron network. Returns the
// created network object as a free-form map (data.network).
func (c *Client) CreateNetwork(ctx context.Context, o CreateNetworkOpts) (map[string]any, error) {
	nc, err := c.net()
	if err != nil {
		return nil, err
	}
	asu := true
	if o.AdminStateUp != nil {
		asu = *o.AdminStateUp
	}
	base := networks.CreateOpts{Name: o.Name, AdminStateUp: &asu, AvailabilityZoneHints: o.AvailabilityZoneHints}
	// MTU rides via the mtu extension; 0 leaves it unset so neutron uses the provider default.
	var builder networks.CreateOptsBuilder = base
	if o.MTU > 0 {
		builder = mtu.CreateOptsExt{CreateOptsBuilder: base, MTU: o.MTU}
	}
	n, err := networks.Create(ctx, nc, builder).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(n), nil
}

// GetNetwork re-reads one network as a free-form map.
func (c *Client) GetNetwork(ctx context.Context, id string) (map[string]any, error) {
	nc, err := c.net()
	if err != nil {
		return nil, err
	}
	n, err := networks.Get(ctx, nc, id).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(n), nil
}

// DeleteNetwork removes a network.
func (c *Client) DeleteNetwork(ctx context.Context, id string) error {
	nc, err := c.net()
	if err != nil {
		return err
	}
	return networks.Delete(ctx, nc, id).ExtractErr()
}

// AllocationPool / HostRoute mirror the subnet sub-objects.
type AllocationPool struct{ Start, End string }
type HostRoute struct{ DestinationCIDR, NextHop string }

// CreateSubnetOpts mirrors CreateSubnetRequest. Gateway semantics: Gateway=false →
// gateway disabled; Gateway=true + CustomGatewayIP → GatewayIP; else auto (nil).
type CreateSubnetOpts struct {
	NetworkID       string
	Name            string
	CIDR            string
	IPVersion       int // 4 or 6; 0 → 4
	EnableDHCP      *bool
	Gateway         bool
	CustomGatewayIP bool
	GatewayIP       string
	DNSNameservers  []string
	AllocationPools []AllocationPool
	HostRoutes      []HostRoute
}

// CreateSubnet creates a subnet on a network.
func (c *Client) CreateSubnet(ctx context.Context, o CreateSubnetOpts) (map[string]any, error) {
	nc, err := c.net()
	if err != nil {
		return nil, err
	}
	ver := gophercloud.IPv4
	if o.IPVersion == 6 {
		ver = gophercloud.IPv6
	}
	opts := subnets.CreateOpts{
		NetworkID:      o.NetworkID,
		Name:           o.Name,
		CIDR:           o.CIDR,
		IPVersion:      ver,
		EnableDHCP:     o.EnableDHCP,
		DNSNameservers: o.DNSNameservers,
	}
	// Gateway: disabled → empty pointer; custom → the ip; auto → leave nil.
	switch {
	case !o.Gateway:
		empty := ""
		opts.GatewayIP = &empty
	case o.CustomGatewayIP && o.GatewayIP != "":
		opts.GatewayIP = &o.GatewayIP
	}
	for _, p := range o.AllocationPools {
		opts.AllocationPools = append(opts.AllocationPools, subnets.AllocationPool{Start: p.Start, End: p.End})
	}
	for _, h := range o.HostRoutes {
		opts.HostRoutes = append(opts.HostRoutes, subnets.HostRoute{DestinationCIDR: h.DestinationCIDR, NextHop: h.NextHop})
	}
	s, err := subnets.Create(ctx, nc, opts).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(s), nil
}

// UpdateSubnetOpts mirrors the mutable subnet fields. Nil pointers = field omitted; GatewayIP set to
// a pointer-to-"" disables the gateway, a pointer-to-ip sets it.
type UpdateSubnetOpts struct {
	Name           *string
	GatewayIP      *string
	EnableDHCP     *bool
	DNSNameservers *[]string
	HostRoutes     *[]HostRoute
}

// UpdateSubnet updates a subnet (name/gateway/dhcp/dns/host-routes). Returns the updated subnet map.
func (c *Client) UpdateSubnet(ctx context.Context, id string, o UpdateSubnetOpts) (map[string]any, error) {
	nc, err := c.net()
	if err != nil {
		return nil, err
	}
	opts := subnets.UpdateOpts{Name: o.Name, GatewayIP: o.GatewayIP, EnableDHCP: o.EnableDHCP, DNSNameservers: o.DNSNameservers}
	if o.HostRoutes != nil {
		hr := make([]subnets.HostRoute, 0, len(*o.HostRoutes))
		for _, h := range *o.HostRoutes {
			hr = append(hr, subnets.HostRoute{DestinationCIDR: h.DestinationCIDR, NextHop: h.NextHop})
		}
		opts.HostRoutes = &hr
	}
	s, err := subnets.Update(ctx, nc, id, opts).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(s), nil
}

// DeleteSubnet removes a subnet.
func (c *Client) DeleteSubnet(ctx context.Context, id string) error {
	nc, err := c.net()
	if err != nil {
		return err
	}
	return subnets.Delete(ctx, nc, id).ExtractErr()
}

// CreateRouterOpts mirrors CreateRouterRequest. ExternalGatewayNetworkID, when set,
// attaches the router OUT to a pre-existing external network (e.g. "guest") — the only way an
// internal VPC reaches outside. We never CREATE that external network here.
type CreateRouterOpts struct {
	Name                     string
	AdminStateUp             *bool // nil → true
	ExternalGatewayNetworkID string
}

// CreateRouter creates a Neutron router, optionally with an external gateway.
func (c *Client) CreateRouter(ctx context.Context, o CreateRouterOpts) (map[string]any, error) {
	nc, err := c.net()
	if err != nil {
		return nil, err
	}
	asu := true
	if o.AdminStateUp != nil {
		asu = *o.AdminStateUp
	}
	opts := routers.CreateOpts{Name: o.Name, AdminStateUp: &asu}
	if o.ExternalGatewayNetworkID != "" {
		opts.GatewayInfo = &routers.GatewayInfo{NetworkID: o.ExternalGatewayNetworkID}
	}
	r, err := routers.Create(ctx, nc, opts).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(r), nil
}

// AddRouterInterface attaches a subnet to a router.
func (c *Client) AddRouterInterface(ctx context.Context, routerID, subnetID string) error {
	nc, err := c.net()
	if err != nil {
		return err
	}
	_, err = routers.AddInterface(ctx, nc, routerID, routers.AddInterfaceOpts{SubnetID: subnetID}).Extract()
	return err
}

// RemoveRouterInterface detaches a subnet from a router.
func (c *Client) RemoveRouterInterface(ctx context.Context, routerID, subnetID string) error {
	nc, err := c.net()
	if err != nil {
		return err
	}
	_, err = routers.RemoveInterface(ctx, nc, routerID, routers.RemoveInterfaceOpts{SubnetID: subnetID}).Extract()
	return err
}

// RemoveRouterInterfaceByPort detaches a router interface BY PORT id
// (passes portId with subnetId=null).
func (c *Client) RemoveRouterInterfaceByPort(ctx context.Context, routerID, portID string) error {
	nc, err := c.net()
	if err != nil {
		return err
	}
	_, err = routers.RemoveInterface(ctx, nc, routerID, routers.RemoveInterfaceOpts{PortID: portID}).Extract()
	return err
}

// SetRouterGateway sets (networkID non-empty) or clears (networkID == "") a router's external gateway
// (router update setting / clearing the external gateway).
func (c *Client) SetRouterGateway(ctx context.Context, routerID, networkID string) (map[string]any, error) {
	nc, err := c.net()
	if err != nil {
		return nil, err
	}
	gw := &routers.GatewayInfo{}
	if networkID != "" {
		gw.NetworkID = networkID
	}
	r, err := routers.Update(ctx, nc, routerID, routers.UpdateOpts{GatewayInfo: gw}).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(r), nil
}

// DeleteRouter removes a router (its gateway/interfaces must be cleared first by the caller).
func (c *Client) DeleteRouter(ctx context.Context, id string) error {
	nc, err := c.net()
	if err != nil {
		return err
	}
	return routers.Delete(ctx, nc, id).ExtractErr()
}

func (c *Client) compute() (*gophercloud.ServiceClient, error) {
	return openstack.NewComputeV2(c.provider, c.endpointOpts())
}

func (c *Client) blockStorage() (*gophercloud.ServiceClient, error) {
	return openstack.NewBlockStorageV3(c.provider, c.endpointOpts())
}

// CreateServerOpts mirrors the CreateInstanceRequest essentials. NetworkIDs attach the VM to those networks.
type CreateServerOpts struct {
	Name             string
	FlavorID         string
	ImageID          string
	NetworkIDs       []string
	FixedIPs         map[string]string // networkID → requested fixed IP on that network (optional)
	KeyName          string
	SecurityGroups   []string
	UserData         []byte
	AvailabilityZone string
	AdminPass        string // sets the instance's admin/root password (nova adminPass); "" = nova-generated
}

// CreateServer boots a Nova server. Returns the created server object
// as a free-form map (data.server).
func (c *Client) CreateServer(ctx context.Context, o CreateServerOpts) (map[string]any, error) {
	cc, err := c.compute()
	if err != nil {
		return nil, err
	}
	nets := make([]servers.Network, 0, len(o.NetworkIDs))
	for _, id := range o.NetworkIDs {
		nets = append(nets, servers.Network{UUID: id, FixedIP: o.FixedIPs[id]})
	}
	opts := servers.CreateOpts{
		Name:             o.Name,
		FlavorRef:        o.FlavorID,
		ImageRef:         o.ImageID,
		Networks:         nets,
		SecurityGroups:   o.SecurityGroups,
		UserData:         o.UserData,
		AvailabilityZone: o.AvailabilityZone,
		AdminPass:        o.AdminPass,
	}
	// KeyName rides via the keypairs ext (the base servers.CreateOpts carries it only through a
	// CreateOptsBuilder wrapper); keyless boots pass the base opts unchanged.
	var builder servers.CreateOptsBuilder = opts
	if o.KeyName != "" {
		builder = keypairs.CreateOptsExt{CreateOptsBuilder: opts, KeyName: o.KeyName}
	}
	s, err := servers.Create(ctx, cc, builder, nil).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(s), nil
}

// GetServer re-reads one server as a free-form map.
func (c *Client) GetServer(ctx context.Context, id string) (map[string]any, error) {
	cc, err := c.compute()
	if err != nil {
		return nil, err
	}
	s, err := servers.Get(ctx, cc, id).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(s), nil
}

// DeleteServer deletes a Nova server.
func (c *Client) DeleteServer(ctx context.Context, id string) error {
	cc, err := c.compute()
	if err != nil {
		return err
	}
	return servers.Delete(ctx, cc, id).ExtractErr()
}

// CreateVolumeOpts mirrors CreateVolumeRequest.
type CreateVolumeOpts struct {
	Name             string
	Size             int // GB
	VolumeType       string
	ImageID          string
	SnapshotID       string
	AvailabilityZone string
}

// CreateVolume creates a Cinder volume. Returns the created volume
// object as a free-form map (data.volume).
func (c *Client) CreateVolume(ctx context.Context, o CreateVolumeOpts) (map[string]any, error) {
	bs, err := c.blockStorage()
	if err != nil {
		return nil, err
	}
	v, err := volumes.Create(ctx, bs, volumes.CreateOpts{
		Name:             o.Name,
		Size:             o.Size,
		VolumeType:       o.VolumeType,
		ImageID:          o.ImageID,
		SnapshotID:       o.SnapshotID,
		AvailabilityZone: o.AvailabilityZone,
	}, nil).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(v), nil
}

// GetVolume re-reads one volume as a free-form map.
func (c *Client) GetVolume(ctx context.Context, id string) (map[string]any, error) {
	bs, err := c.blockStorage()
	if err != nil {
		return nil, err
	}
	v, err := volumes.Get(ctx, bs, id).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(v), nil
}

// DeleteVolume deletes a Cinder volume.
func (c *Client) DeleteVolume(ctx context.Context, id string) error {
	bs, err := c.blockStorage()
	if err != nil {
		return err
	}
	return volumes.Delete(ctx, bs, id, volumes.DeleteOpts{}).ExtractErr()
}

// AttachVolume attaches a volume to a server (nova volumeAttach).
// Returns the attachment object (carrying its id + device) as a free-form map.
func (c *Client) AttachVolume(ctx context.Context, serverID, volumeID string) (map[string]any, error) {
	cc, err := c.compute()
	if err != nil {
		return nil, err
	}
	a, err := volumeattach.Create(ctx, cc, serverID, volumeattach.CreateOpts{VolumeID: volumeID}).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(a), nil
}

// DetachVolume detaches a volume from a server by attachment id.
// For Nova the attachment id equals the volume id.
func (c *Client) DetachVolume(ctx context.Context, serverID, attachmentID string) error {
	cc, err := c.compute()
	if err != nil {
		return err
	}
	return volumeattach.Delete(ctx, cc, serverID, attachmentID).ExtractErr()
}

// RebootServer soft/hard-reboots a server (action REBOOT_SOFT/REBOOT_HARD).
func (c *Client) RebootServer(ctx context.Context, id string, hard bool) error {
	cc, err := c.compute()
	if err != nil {
		return err
	}
	how := servers.SoftReboot
	if hard {
		how = servers.HardReboot
	}
	return servers.Reboot(ctx, cc, id, servers.RebootOpts{Type: how}).ExtractErr()
}

// StartServer / StopServer (action START/STOP).
func (c *Client) StartServer(ctx context.Context, id string) error {
	cc, err := c.compute()
	if err != nil {
		return err
	}
	return servers.Start(ctx, cc, id).ExtractErr()
}

func (c *Client) StopServer(ctx context.Context, id string) error {
	cc, err := c.compute()
	if err != nil {
		return err
	}
	return servers.Stop(ctx, cc, id).ExtractErr()
}

// PauseServer / UnpauseServer are the billing suspend/resume verbs (suspend = nova Action.PAUSE,
// resume = Action.UNPAUSE — a suspended customer's VMs freeze but keep state).
func (c *Client) PauseServer(ctx context.Context, id string) error {
	cc, err := c.compute()
	if err != nil {
		return err
	}
	return servers.Pause(ctx, cc, id).ExtractErr()
}

func (c *Client) UnpauseServer(ctx context.Context, id string) error {
	cc, err := c.compute()
	if err != nil {
		return err
	}
	return servers.Unpause(ctx, cc, id).ExtractErr()
}

// ResizeServer changes a server's flavor (action RESIZE → nova resize; the server enters
// VERIFY_RESIZE until confirmed/reverted).
func (c *Client) ResizeServer(ctx context.Context, id, flavorID string) error {
	cc, err := c.compute()
	if err != nil {
		return err
	}
	return servers.Resize(ctx, cc, id, servers.ResizeOpts{FlavorRef: flavorID}).ExtractErr()
}

// ConfirmResize finalizes a pending resize (action CONFIRMRESIZE). A 409 means the server is no
// longer in VERIFY_RESIZE — it was already confirmed (a double-click, or nova auto-confirmed after
// its resize_confirm_window). The resize is already finalized, which is exactly what confirm wants,
// so this is treated as idempotent success rather than surfacing a raw 500 for a no-op.
func (c *Client) ConfirmResize(ctx context.Context, id string) error {
	cc, err := c.compute()
	if err != nil {
		return err
	}
	if err := servers.ConfirmResize(ctx, cc, id).ExtractErr(); err != nil && !gophercloud.ResponseCodeIs(err, http.StatusConflict) {
		return err
	}
	return nil
}

// RevertResize rolls back a pending resize (action REVERTRESIZE). A 409 (server no longer in
// VERIFY_RESIZE — already reverted/settled) is idempotent success, same rationale as ConfirmResize.
func (c *Client) RevertResize(ctx context.Context, id string) error {
	cc, err := c.compute()
	if err != nil {
		return err
	}
	if err := servers.RevertResize(ctx, cc, id).ExtractErr(); err != nil && !gophercloud.ResponseCodeIs(err, http.StatusConflict) {
		return err
	}
	return nil
}

// RenameServer updates a server's display name (action RENAME → nova update).
func (c *Client) RenameServer(ctx context.Context, id, name string) (map[string]any, error) {
	cc, err := c.compute()
	if err != nil {
		return nil, err
	}
	s, err := servers.Update(ctx, cc, id, servers.UpdateOpts{Name: name}).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(s), nil
}

// CreateServerImage snapshots a running server into a Glance image (nova createImage).
// Returns the new image id.
func (c *Client) CreateServerImage(ctx context.Context, id, name string) (string, error) {
	cc, err := c.compute()
	if err != nil {
		return "", err
	}
	res := servers.CreateImage(ctx, cc, id, servers.CreateImageOpts{Name: name})
	return res.ExtractImageID()
}

// GetConsoleOutput returns the tail of a server's console log (SHOW_CONSOLE_OUTPUT →
// nova os-getConsoleOutput). length = number of lines (0 = all).
func (c *Client) GetConsoleOutput(ctx context.Context, id string, length int) (string, error) {
	cc, err := c.compute()
	if err != nil {
		return "", err
	}
	return servers.ShowConsoleOutput(ctx, cc, id, servers.ShowConsoleOutputOpts{Length: length}).Extract()
}

// SetServerMetadata REPLACES a server's metadata with the given map
// (nova PUT /servers/{id}/metadata, the client metadata tab's full-map save).
func (c *Client) SetServerMetadata(ctx context.Context, id string, meta map[string]string) (map[string]string, error) {
	cc, err := c.compute()
	if err != nil {
		return nil, err
	}
	return servers.ResetMetadata(ctx, cc, id, servers.MetadataOpts(meta)).Extract()
}

// CreatePortOpts mirrors CreatePortRequest essentials.
// AddressPair is a port allowed-address-pair (an extra IP/MAC the port may source traffic as — e.g.
// a keepalived/HAProxy VIP, or the CIDR a routing VM forwards for). MACAddress is optional.
type AddressPair struct {
	IPAddress  string
	MACAddress string
}

// toPortPairs maps the local AddressPair slice to gophercloud's (dropping blank IPs).
func toPortPairs(in []AddressPair) []ports.AddressPair {
	out := make([]ports.AddressPair, 0, len(in))
	for _, p := range in {
		if p.IPAddress == "" {
			continue
		}
		out = append(out, ports.AddressPair{IPAddress: p.IPAddress, MACAddress: p.MACAddress})
	}
	return out
}

type CreatePortOpts struct {
	NetworkID           string
	Name                string
	MACAddress          string // request macAddress (optional)
	FixedIP             string // request fixedIp — paired with SubnetID
	SubnetID            string // request subnetId → fixedIp(fixedIp, subnetId)
	PortSecurityEnabled *bool  // nil → omit; false/true → portsecurity ext
	AllowedAddressPairs []AddressPair
}

// CreatePort creates a Neutron port: networkId/name/adminState plus the
// optional macAddress, a fixed-ip-on-subnet (subnetId [+ fixedIp]), and a port-security toggle (via
// the portsecurity ext, mirroring the UPDATE path). The disable-policy guard
// (assertPortSecurityDisableAllowed — an OpenStack-feature-flag check on the service) is NOT implemented
// here; neutron still enforces its own ACL, and the feature-flag config isn't wired into the write
// path yet (deferred, see the §6 logic-gap note).
func (c *Client) CreatePort(ctx context.Context, o CreatePortOpts) (map[string]any, error) {
	nc, err := c.net()
	if err != nil {
		return nil, err
	}
	asu := true
	base := ports.CreateOpts{NetworkID: o.NetworkID, Name: o.Name, AdminStateUp: &asu, MACAddress: o.MACAddress}
	if o.SubnetID != "" {
		base.FixedIPs = []ports.IP{{SubnetID: o.SubnetID, IPAddress: o.FixedIP}}
	}
	if len(o.AllowedAddressPairs) > 0 {
		base.AllowedAddressPairs = toPortPairs(o.AllowedAddressPairs)
	}
	var opts ports.CreateOptsBuilder = base
	if o.PortSecurityEnabled != nil {
		opts = portsecurity.PortCreateOptsExt{CreateOptsBuilder: base, PortSecurityEnabled: o.PortSecurityEnabled}
	}
	p, err := ports.Create(ctx, nc, opts).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(p), nil
}

// DeletePort removes a Neutron port.
func (c *Client) DeletePort(ctx context.Context, id string) error {
	nc, err := c.net()
	if err != nil {
		return err
	}
	return ports.Delete(ctx, nc, id).ExtractErr()
}

// UpdatePortOpts mirrors the DataPort UPDATE action fields. Nil pointers = field omitted.
type UpdatePortOpts struct {
	Name                *string
	SecurityGroups      *[]string
	PortSecurityEnabled *bool
	AllowedAddressPairs *[]AddressPair // nil → omit; empty slice → clear all pairs
}

// UpdatePort updates a neutron port. Sets
// name/security-groups/port-security-enabled; portSecurityEnabled toggling uses the portsecurity
// extension. Returns the updated port as a map.
func (c *Client) UpdatePort(ctx context.Context, portID string, o UpdatePortOpts) (map[string]any, error) {
	nc, err := c.net()
	if err != nil {
		return nil, err
	}
	base := ports.UpdateOpts{Name: o.Name, SecurityGroups: o.SecurityGroups}
	if o.AllowedAddressPairs != nil {
		pairs := toPortPairs(*o.AllowedAddressPairs)
		base.AllowedAddressPairs = &pairs
	}
	var opts ports.UpdateOptsBuilder = base
	if o.PortSecurityEnabled != nil {
		opts = portsecurity.PortUpdateOptsExt{UpdateOptsBuilder: base, PortSecurityEnabled: o.PortSecurityEnabled}
	}
	p, err := ports.Update(ctx, nc, portID, opts).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(p), nil
}

// CreateFloatingIPOpts mirrors CreateFloatingIPRequest. FloatingNetworkID = the external
// network to allocate FROM (e.g. "guest"); PortID optionally associates on create.
type CreateFloatingIPOpts struct {
	FloatingNetworkID string
	PortID            string
	Description       string
}

// CreateFloatingIP allocates a floating IP from an external network.
func (c *Client) CreateFloatingIP(ctx context.Context, o CreateFloatingIPOpts) (map[string]any, error) {
	nc, err := c.net()
	if err != nil {
		return nil, err
	}
	opts := floatingips.CreateOpts{FloatingNetworkID: o.FloatingNetworkID, Description: o.Description}
	if o.PortID != "" {
		opts.PortID = o.PortID
	}
	f, err := floatingips.Create(ctx, nc, opts).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(f), nil
}

// AssociateFloatingIP binds a floating IP to a port (action ASSIGN).
func (c *Client) AssociateFloatingIP(ctx context.Context, fipID, portID string) (map[string]any, error) {
	nc, err := c.net()
	if err != nil {
		return nil, err
	}
	f, err := floatingips.Update(ctx, nc, fipID, floatingips.UpdateOpts{PortID: &portID}).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(f), nil
}

// DisassociateFloatingIP clears a floating IP's port (action UNASSIGN).
func (c *Client) DisassociateFloatingIP(ctx context.Context, fipID string) (map[string]any, error) {
	nc, err := c.net()
	if err != nil {
		return nil, err
	}
	empty := ""
	f, err := floatingips.Update(ctx, nc, fipID, floatingips.UpdateOpts{PortID: &empty}).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(f), nil
}

// DeleteFloatingIP releases a floating IP.
func (c *Client) DeleteFloatingIP(ctx context.Context, id string) error {
	nc, err := c.net()
	if err != nil {
		return err
	}
	return floatingips.Delete(ctx, nc, id).ExtractErr()
}

// ── Security groups ──────────────────────────────────────────────────────────────────────────────

// CreateSecurityGroupOpts mirrors CreateSecurityGroupRequest {name, description}.
type CreateSecurityGroupOpts struct {
	Name        string
	Description string
}

// CreateSecurityGroup creates a Neutron security group.
func (c *Client) CreateSecurityGroup(ctx context.Context, o CreateSecurityGroupOpts) (map[string]any, error) {
	nc, err := c.net()
	if err != nil {
		return nil, err
	}
	g, err := groups.Create(ctx, nc, groups.CreateOpts{Name: o.Name, Description: o.Description}).Extract()
	if err != nil {
		return nil, err
	}
	// Re-fetch via the raw neutron GET so the map carries lowercase neutron keys: gophercloud's
	// SecGroup struct leaves id/name/description UNTAGGED, so a json round-trip (toMap) would emit
	// "ID"/"Name"/"Description" and the FE (+ our mstr(...,"id")) would miss them.
	return c.GetSecurityGroup(ctx, g.ID)
}

// DeleteSecurityGroup removes a Neutron security group.
func (c *Client) DeleteSecurityGroup(ctx context.Context, id string) error {
	nc, err := c.net()
	if err != nil {
		return err
	}
	return groups.Delete(ctx, nc, id).ExtractErr()
}

// CreateSecGroupRuleOpts mirrors AddSecurityGroupRuleRequest {direction, etherType, portRangeMin,
// portRangeMax, remoteIpPrefix, remoteGroupId, protocol}.
type CreateSecGroupRuleOpts struct {
	SecGroupID     string
	Direction      string
	EtherType      string
	Protocol       string
	PortRangeMin   int
	PortRangeMax   int
	RemoteIPPrefix string
	RemoteGroupID  string
}

// CreateSecGroupRule adds a rule to a security group (action ADD_RULE).
func (c *Client) CreateSecGroupRule(ctx context.Context, o CreateSecGroupRuleOpts) (map[string]any, error) {
	nc, err := c.net()
	if err != nil {
		return nil, err
	}
	ether := o.EtherType
	if ether == "" {
		ether = "IPv4"
	}
	opts := rules.CreateOpts{
		SecGroupID:     o.SecGroupID,
		Direction:      rules.RuleDirection(strings.ToLower(o.Direction)),
		EtherType:      rules.RuleEtherType(ether),
		Protocol:       rules.RuleProtocol(strings.ToLower(o.Protocol)),
		PortRangeMin:   o.PortRangeMin,
		PortRangeMax:   o.PortRangeMax,
		RemoteIPPrefix: o.RemoteIPPrefix,
		RemoteGroupID:  o.RemoteGroupID,
	}
	r, err := rules.Create(ctx, nc, opts).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(r), nil
}

// DeleteSecGroupRule removes a security group rule (action DELETE_RULE).
func (c *Client) DeleteSecGroupRule(ctx context.Context, ruleID string) error {
	nc, err := c.net()
	if err != nil {
		return err
	}
	return rules.Delete(ctx, nc, ruleID).ExtractErr()
}

// ── Keypairs (identity-scoped) ───────────────────────────────────────────────────────────────────

// CreateKeypairOpts mirrors CreateKeyPairRequest {name, publicKey}. An empty PublicKey makes nova
// generate a new keypair and return the privateKey (transient, never persisted).
type CreateKeypairOpts struct {
	Name      string
	PublicKey string
}

// CreateKeypair creates/imports a nova keypair. The returned map carries
// privateKey when nova generated one.
func (c *Client) CreateKeypair(ctx context.Context, o CreateKeypairOpts) (map[string]any, error) {
	cc, err := c.compute()
	if err != nil {
		return nil, err
	}
	k, err := keypairs.Create(ctx, cc, keypairs.CreateOpts{Name: o.Name, PublicKey: o.PublicKey}).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(k), nil
}

// DeleteKeypair removes a nova keypair by name.
func (c *Client) DeleteKeypair(ctx context.Context, name string) error {
	cc, err := c.compute()
	if err != nil {
		return err
	}
	return keypairs.Delete(ctx, cc, name, keypairs.DeleteOpts{}).ExtractErr()
}

// ── Volume snapshots ─────────────────────────────────────────────────────────────────────────────

// CreateVolumeSnapshotOpts mirrors CreateVolumeSnapshotRequest {name, description, externalVolumeId,
// force}.
type CreateVolumeSnapshotOpts struct {
	VolumeID    string
	Name        string
	Description string
	Force       bool
}

// CreateVolumeSnapshot snapshots a Cinder volume.
func (c *Client) CreateVolumeSnapshot(ctx context.Context, o CreateVolumeSnapshotOpts) (map[string]any, error) {
	bc, err := c.blockStorage()
	if err != nil {
		return nil, err
	}
	s, err := snapshots.Create(ctx, bc, snapshots.CreateOpts{
		VolumeID: o.VolumeID, Name: o.Name, Description: o.Description, Force: o.Force,
	}).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(s), nil
}

// DeleteVolumeSnapshot removes a Cinder snapshot.
func (c *Client) DeleteVolumeSnapshot(ctx context.Context, id string) error {
	bc, err := c.blockStorage()
	if err != nil {
		return err
	}
	return snapshots.Delete(ctx, bc, id).ExtractErr()
}

// ── Volume actions (EXTEND / RETYPE) ─────────────────────────────────────────────────────────────

// ExtendVolume grows a volume to newSize GB (action EXTEND → cinder os-extend).
func (c *Client) ExtendVolume(ctx context.Context, id string, newSize int) error {
	bc, err := c.blockStorage()
	if err != nil {
		return err
	}
	return volumes.ExtendSize(ctx, bc, id, volumes.ExtendSizeOpts{NewSize: newSize}).ExtractErr()
}

// RetypeVolume changes a volume's type (action RETYPE → cinder os-retype).
// migrationPolicy defaults to "never" when blank.
func (c *Client) RetypeVolume(ctx context.Context, id, newType, migrationPolicy string) error {
	bc, err := c.blockStorage()
	if err != nil {
		return err
	}
	mp := volumes.MigrationPolicy(migrationPolicy)
	if migrationPolicy == "" {
		mp = volumes.MigrationPolicyNever
	}
	return volumes.ChangeType(ctx, bc, id, volumes.ChangeTypeOpts{NewType: newType, MigrationPolicy: mp}).ExtractErr()
}

// ── Server groups ────────────────────────────────────────────────────────────────────────────────

// serverGroupMicroversion: nova ≥ 2.15 accepts soft-affinity/soft-anti-affinity policies —
// in the create schema AND the (2025.1+ schema-validated) list/get responses. At the default
// 2.1 a soft-* create 400s, and every sync LIST of a project holding a soft-policy group makes
// nova-api log a "Schema failed to validate" ERROR. Stay below 2.64 so the pre-2.64 Policies
// array body keeps working.
const serverGroupMicroversion = "2.15"

// computeServerGroups is the compute client pinned to the server-group microversion.
func (c *Client) computeServerGroups() (*gophercloud.ServiceClient, error) {
	cc, err := c.compute()
	if err != nil {
		return nil, err
	}
	cc.Microversion = serverGroupMicroversion
	return cc, nil
}

// CreateServerGroupOpts mirrors CreateServerGroupRequest {name, policy}.
type CreateServerGroupOpts struct {
	Name   string
	Policy string
}

// CreateServerGroup creates a nova server group (affinity/anti-affinity/soft-*). Uses the
// pre-2.64 Policies array form for broad microversion compatibility.
func (c *Client) CreateServerGroup(ctx context.Context, o CreateServerGroupOpts) (map[string]any, error) {
	cc, err := c.computeServerGroups()
	if err != nil {
		return nil, err
	}
	policy := o.Policy
	if policy == "" {
		policy = "affinity"
	}
	g, err := servergroups.Create(ctx, cc, servergroups.CreateOpts{Name: o.Name, Policies: []string{policy}}).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(g), nil
}

// GetServerGroup fetches a nova server group (backs GET_MEMBERS — the group embeds member ids).
func (c *Client) GetServerGroup(ctx context.Context, id string) (map[string]any, error) {
	cc, err := c.computeServerGroups()
	if err != nil {
		return nil, err
	}
	g, err := servergroups.Get(ctx, cc, id).Extract()
	if err != nil {
		return nil, err
	}
	return toMap(g), nil
}

// DeleteServerGroup removes a nova server group.
func (c *Client) DeleteServerGroup(ctx context.Context, id string) error {
	cc, err := c.computeServerGroups()
	if err != nil {
		return err
	}
	return servergroups.Delete(ctx, cc, id).ExtractErr()
}
