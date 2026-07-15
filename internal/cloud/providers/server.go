package providers

import (
	"context"

	"github.com/menlocloud/stratos/internal/cloud"
	"github.com/menlocloud/stratos/internal/cloud/client"
)

// ServerProvider lists Nova servers → CloudResource.
// `data.server` carries the fields the rating SERVER provider reads
// (cloud/billingresource.ServerProvider.instanceBR): flavor.{ram,vcpus,disk}, name, host,
// status, availabilityZone, image.id; plus top-level `flavorName`. Write/dispatch
// (create/delete, clusterInfo, instanceMetadata) land later — this is the read-sync that
// populates the cache the metrics job + charge loop walk.
type ServerProvider struct {
	cc        *client.Client
	region    string
	projectID string
}

func NewServerProvider(cc *client.Client, region, projectID string) *ServerProvider {
	return &ServerProvider{cc: cc, region: region, projectID: projectID}
}

func (p *ServerProvider) Type() string      { return cloud.TypeServer }
func (p *ServerProvider) ProjectID() string { return p.projectID }

// ShouldBeDeleted: a cached doc with no
// data.server is inconsistent → delete; a server in nova status DELETED → delete. (We also
// delete baremetal-flavor servers — they belong to the baremetal provider — but the flavor-
// category lookup isn't threaded into this read provider and the region runs no baremetal;
// add it with the BM provider if that ever lands.)
func (p *ServerProvider) ShouldBeDeleted(cr *cloud.CloudResource) bool {
	srv, _ := cr.Data["server"].(map[string]any)
	if srv == nil {
		return true
	}
	status, _ := srv["status"].(string)
	return status == "DELETED"
}

func (p *ServerProvider) List(ctx context.Context) ([]cloud.CloudResource, error) {
	servers, err := p.cc.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]cloud.CloudResource, 0, len(servers))
	for _, s := range servers {
		created, updated := s.Created, s.Updated
		out = append(out, cloud.CloudResource{
			Type:       cloud.TypeServer,
			ExternalID: s.ID,
			Region:     p.region,
			ProjectID:  p.projectID,
			Data: map[string]any{
				"flavorName":   s.FlavorName,
				"volumeBacked": s.ImageID == "",
				"server": map[string]any{
					"name":             s.Name,
					"host":             s.Host,
					"status":           s.Status,
					"availabilityZone": s.AvailabilityZone,
					// addresses feeds the list/detail IP column (the FE reads server.addresses); nil
					// when the server has none yet.
					"addresses": s.Addresses,
					"flavor": map[string]any{
						// id/original_name so the FE flavor helper (original_name ?? name) renders the
						// flavor without a live re-fetch.
						"id":            s.FlavorID,
						"name":          s.FlavorName,
						"original_name": s.FlavorName,
						"ram":           s.RAM,
						"vcpus":         s.VCPUs,
						"disk":          s.Disk,
						// extra_specs feeds GPU rating (gpu_model/gpu_count) — without it a
						// GPU server bills zero. nil preserves a failed lookup; an empty map
						// is a resolved CPU-only flavor.
						"extra_specs": s.FlavorExtraSpecs,
					},
					"image": map[string]any{"id": s.ImageID},
				},
			},
			Info: &cloud.Info{CreatedAt: &created, UpdatedAt: &updated},
		})
	}
	return out, nil
}
