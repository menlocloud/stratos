package client

import (
	"context"
	"strings"

	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/availabilityzones"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumetypes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
)

// server_actions.go = additional nova/cinder action seams (server run-actions + volume direct-actions).
// Each returns
// the free-form shape the action handler wraps in {result}.

// RebuildServer rebuilds a nova server onto an image. imageRef is
// the glance image id; name optional (keeps current when blank); adminPass optional.
func (c *Client) RebuildServer(ctx context.Context, id, imageRef, name, adminPass string, volumeBacked ...bool) (map[string]any, error) {
	cc, err := c.compute()
	if err != nil {
		return nil, err
	}
	// Nova added rebuild support for volume-backed servers in microversion 2.93.
	// Keep the legacy default for image-backed servers so older clouds retain
	// their existing behavior.
	if len(volumeBacked) > 0 && volumeBacked[0] {
		cc.Microversion = "2.93"
	}
	srv, err := servers.Rebuild(ctx, cc, id, servers.RebuildOpts{ImageRef: imageRef, Name: name, AdminPass: adminPass}).Extract()
	if err != nil {
		return nil, err
	}
	return serverToMap(srv), nil
}

// SetServerPassword changes a nova server's admin password (nova changeAdminPassword).
func (c *Client) SetServerPassword(ctx context.Context, id, password string) error {
	cc, err := c.compute()
	if err != nil {
		return err
	}
	return servers.ChangeAdminPassword(ctx, cc, id, password).ExtractErr()
}

// RescueServer puts a nova server into RESCUE mode, returning the
// generated admin password. rescueImageRef optional (default rescue image when blank).
func (c *Client) RescueServer(ctx context.Context, id, rescueImageRef string, volumeBacked ...bool) (string, error) {
	cc, err := c.compute()
	if err != nil {
		return "", err
	}
	// Nova added rescue support for volume-backed servers in microversion 2.87.
	if len(volumeBacked) > 0 && volumeBacked[0] {
		cc.Microversion = "2.87"
	}
	return servers.Rescue(ctx, cc, id, servers.RescueOpts{RescueImageRef: rescueImageRef}).Extract()
}

// UnrescueServer takes a nova server out of RESCUE mode.
func (c *Client) UnrescueServer(ctx context.Context, id string) error {
	cc, err := c.compute()
	if err != nil {
		return err
	}
	return servers.Unrescue(ctx, cc, id).ExtractErr()
}

// AddServerSecurityGroup attaches a security group (by name) to a nova server
// (nova addSecurityGroup action). gophercloud v2 dropped the
// compute secgroups extension → direct-REST.
func (c *Client) AddServerSecurityGroup(ctx context.Context, serverID, sgName string) error {
	base, err := c.EndpointURL("compute")
	if err != nil {
		return err
	}
	body := map[string]any{"addSecurityGroup": map[string]any{"name": sgName}}
	return c.Do(ctx, "POST", strings.TrimRight(base, "/")+"/servers/"+serverID+"/action", body, nil, 202)
}

// RemoveServerSecurityGroup detaches a security group (by name) from a nova server
// (nova removeSecurityGroup action).
func (c *Client) RemoveServerSecurityGroup(ctx context.Context, serverID, sgName string) error {
	base, err := c.EndpointURL("compute")
	if err != nil {
		return err
	}
	body := map[string]any{"removeSecurityGroup": map[string]any{"name": sgName}}
	return c.Do(ctx, "POST", strings.TrimRight(base, "/")+"/servers/"+serverID+"/action", body, nil, 202)
}

// AttachServerPort attaches a network interface to a nova server
// (nova os-interface create). Pass portID (preferred) or netID. Returns the interfaceAttachment.
func (c *Client) AttachServerPort(ctx context.Context, serverID, portID, netID string) (map[string]any, error) {
	base, err := c.EndpointURL("compute")
	if err != nil {
		return nil, err
	}
	att := map[string]any{}
	if portID != "" {
		att["port_id"] = portID
	}
	if netID != "" {
		att["net_id"] = netID
	}
	var resp struct {
		InterfaceAttachment map[string]any `json:"interfaceAttachment"`
	}
	if err := c.Do(ctx, "POST", strings.TrimRight(base, "/")+"/servers/"+serverID+"/os-interface", map[string]any{"interfaceAttachment": att}, &resp, 200); err != nil {
		return nil, err
	}
	return resp.InterfaceAttachment, nil
}

// DetachServerPort detaches a port from a nova server
// (nova os-interface delete; the path id is the PORT id).
func (c *Client) DetachServerPort(ctx context.Context, serverID, portID string) error {
	base, err := c.EndpointURL("compute")
	if err != nil {
		return err
	}
	return c.Do(ctx, "DELETE", strings.TrimRight(base, "/")+"/servers/"+serverID+"/os-interface/"+portID, nil, nil, 202)
}

// ListVolumeTypes lists the cinder volume types (LIST_TYPES → blockstorage types).
func (c *Client) ListVolumeTypes(ctx context.Context) ([]map[string]any, error) {
	bc, err := c.blockStorage()
	if err != nil {
		return nil, err
	}
	pages, err := volumetypes.List(bc, volumetypes.ListOpts{}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	ts, err := volumetypes.ExtractVolumeTypes(pages)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(ts))
	for i := range ts {
		out = append(out, map[string]any{
			"id": ts[i].ID, "name": ts[i].Name, "description": ts[i].Description,
			"isPublic": ts[i].IsPublic, "extraSpecs": ts[i].ExtraSpecs,
		})
	}
	return out, nil
}

// ListVolumeAvailabilityZones lists the cinder availability zones
// (LIST_AVAILABILITY_ZONES). Each carries name + available.
func (c *Client) ListVolumeAvailabilityZones(ctx context.Context) ([]map[string]any, error) {
	bc, err := c.blockStorage()
	if err != nil {
		return nil, err
	}
	pages, err := availabilityzones.List(bc).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	azs, err := availabilityzones.ExtractAvailabilityZones(pages)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(azs))
	for i := range azs {
		out = append(out, map[string]any{
			"name": azs[i].ZoneName, "available": azs[i].ZoneState.Available,
		})
	}
	return out, nil
}
