package providers

import (
	"context"

	"github.com/menlocloud/stratos/internal/cloud"
	"github.com/menlocloud/stratos/internal/cloud/client"
)

// VolumeProvider lists Cinder volumes → CloudResource.
// `data.volume` holds the payload the volume BillingResource provider reads.
type VolumeProvider struct {
	cc        *client.Client
	region    string
	projectID string
}

func NewVolumeProvider(cc *client.Client, region, projectID string) *VolumeProvider {
	return &VolumeProvider{cc: cc, region: region, projectID: projectID}
}

func (p *VolumeProvider) Type() string      { return cloud.TypeVolume }
func (p *VolumeProvider) ProjectID() string { return p.projectID }

func (p *VolumeProvider) List(ctx context.Context) ([]cloud.CloudResource, error) {
	vols, err := p.cc.ListVolumes(ctx)
	if err != nil {
		return nil, err
	}
	return volumesToResources(vols, p.region, p.projectID), nil
}

// volumesToResources maps Cinder volumes → CloudResources. Info.CreatedAt carries cinder's real
// created_at so billing accrues from the volume's true age (clamped to the cycle start), not the
// first-sync time — see the billingresource createdAt resolution.
func volumesToResources(vols []client.Volume, region, projectID string) []cloud.CloudResource {
	out := make([]cloud.CloudResource, 0, len(vols))
	for _, v := range vols {
		attachments := make([]any, 0, len(v.Attachments))
		for _, attachment := range v.Attachments {
			attachments = append(attachments, map[string]any{
				"attachmentId": attachment.AttachmentID,
				"device":       attachment.Device,
				"serverId":     attachment.ServerID,
				"volumeId":     attachment.VolumeID,
			})
		}
		cr := cloud.CloudResource{
			Type:       cloud.TypeVolume,
			ExternalID: v.ID,
			Region:     region,
			ProjectID:  projectID,
			Data: map[string]any{"attachments": attachments, "volume": map[string]any{
				"id": v.ID, "name": v.Name, "size": v.Size, "status": v.Status,
				"volume_type": v.VolumeType, "availability_zone": v.AvailabilityZone,
				"bootable": v.Bootable, "attachments": attachments,
			}},
		}
		if !v.CreatedAt.IsZero() {
			created := v.CreatedAt
			cr.Info = &cloud.Info{CreatedAt: &created}
		}
		out = append(out, cr)
	}
	return out
}
