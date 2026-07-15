package client

import (
	"context"
	"time"

	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/loadbalancers"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/floatingips"
)

// list_extra.go adds READ-ONLY list calls for the billable resource types beyond
// server/network/port: Cinder volumes, Neutron floating IPs, Octavia load balancers. Plain
// structs hide the SDK; the CloudResource sync providers consume these.

// Volume is the slice of a Cinder volume the cache/billing needs.
type VolumeAttachment struct {
	AttachmentID string
	Device       string
	ServerID     string
	VolumeID     string
}

type Volume struct {
	ID               string
	Name             string
	Size             int
	Status           string
	VolumeType       string
	AvailabilityZone string
	Bootable         string // cinder returns "true"/"false" as a string
	Attachments      []VolumeAttachment
	CreatedAt        time.Time // cinder's real created_at → billing accrual start (not the sync time)
}

// ListVolumes returns the project's Cinder volumes (read-only).
func (c *Client) ListVolumes(ctx context.Context) ([]Volume, error) {
	bc, err := openstack.NewBlockStorageV3(c.provider, c.endpointOpts())
	if err != nil {
		return nil, err
	}
	pages, err := volumes.List(bc, volumes.ListOpts{}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	vs, err := volumes.ExtractVolumes(pages)
	if err != nil {
		return nil, err
	}
	out := make([]Volume, 0, len(vs))
	for _, v := range vs {
		attachments := make([]VolumeAttachment, 0, len(v.Attachments))
		for _, attachment := range v.Attachments {
			attachments = append(attachments, VolumeAttachment{
				AttachmentID: attachment.AttachmentID,
				Device:       attachment.Device,
				ServerID:     attachment.ServerID,
				VolumeID:     attachment.VolumeID,
			})
		}
		out = append(out, Volume{
			ID: v.ID, Name: v.Name, Size: v.Size, Status: v.Status,
			VolumeType: v.VolumeType, AvailabilityZone: v.AvailabilityZone, Bootable: v.Bootable,
			Attachments: attachments, CreatedAt: v.CreatedAt,
		})
	}
	return out, nil
}

// FloatingIP is the slice of a Neutron floating IP the cache/billing needs.
type FloatingIP struct {
	ID                string
	Status            string
	FloatingIP        string
	FloatingNetworkID string
	PortID            string
}

// ListFloatingIPs returns the project's Neutron floating IPs (read-only).
func (c *Client) ListFloatingIPs(ctx context.Context) ([]FloatingIP, error) {
	nc, err := openstack.NewNetworkV2(c.provider, c.endpointOpts())
	if err != nil {
		return nil, err
	}
	// Tenant-filter — a neutron admin token lists every tenant's FIPs otherwise.
	pages, err := floatingips.List(nc, floatingips.ListOpts{ProjectID: c.projectID}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	fs, err := floatingips.ExtractFloatingIPs(pages)
	if err != nil {
		return nil, err
	}
	out := make([]FloatingIP, 0, len(fs))
	for _, f := range fs {
		out = append(out, FloatingIP{
			ID: f.ID, Status: f.Status, FloatingIP: f.FloatingIP,
			FloatingNetworkID: f.FloatingNetworkID, PortID: f.PortID,
		})
	}
	return out, nil
}

// LoadBalancer is the slice of an Octavia load balancer the cache/billing needs.
type LoadBalancer struct {
	ID                 string
	Name               string
	OperatingStatus    string
	ProvisioningStatus string
	FlavorID           string
	VipNetworkID       string
	VipAddress         string
}

// ListLoadBalancers returns the project's Octavia load balancers (read-only). Errors (e.g. no
// Octavia in the catalog) are returned for the caller's best-effort sync to log + skip.
func (c *Client) ListLoadBalancers(ctx context.Context) ([]LoadBalancer, error) {
	lc, err := openstack.NewLoadBalancerV2(c.provider, c.endpointOpts())
	if err != nil {
		return nil, err
	}
	// Tenant-filter — an octavia admin token lists every tenant's load balancers otherwise.
	pages, err := loadbalancers.List(lc, loadbalancers.ListOpts{ProjectID: c.projectID}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	lbs, err := loadbalancers.ExtractLoadBalancers(pages)
	if err != nil {
		return nil, err
	}
	out := make([]LoadBalancer, 0, len(lbs))
	for _, l := range lbs {
		out = append(out, LoadBalancer{
			ID: l.ID, Name: l.Name, OperatingStatus: l.OperatingStatus,
			ProvisioningStatus: l.ProvisioningStatus,
			FlavorID:           l.FlavorID, VipNetworkID: l.VipNetworkID, VipAddress: l.VipAddress,
		})
	}
	return out, nil
}
