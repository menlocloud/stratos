package providers

import (
	"testing"
	"time"

	"github.com/menlocloud/stratos/internal/cloud"
	"github.com/menlocloud/stratos/internal/cloud/client"
)

func TestVolumesToResources_CarriesRealCreatedAt(t *testing.T) {
	created := time.Date(2025, 11, 25, 6, 22, 22, 0, time.UTC)
	got := volumesToResources([]client.Volume{
		{ID: "v-1", Name: "pvc-x", Size: 8, Status: "in-use", CreatedAt: created},
		{ID: "v-2", Name: "no-created"}, // zero CreatedAt → no Info (falls back to doc/sync time)
	}, "RegionOne", "proj-1")

	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	// Real created_at flows to Info.CreatedAt → billing accrues from true age, not first-sync time.
	if got[0].Info == nil || got[0].Info.CreatedAt == nil || !got[0].Info.CreatedAt.Equal(created) {
		t.Errorf("v-1 Info.CreatedAt = %v, want %v", got[0].Info, created)
	}
	if got[0].ExternalID != "v-1" || got[0].Type != cloud.TypeVolume {
		t.Errorf("v-1 bad shape: %#v", got[0])
	}
	// Zero created_at must not stamp a bogus Info (heal path leaves it to the doc createdAt fallback).
	if got[1].Info != nil {
		t.Errorf("v-2 Info should be nil for zero CreatedAt, got %#v", got[1].Info)
	}
}

func TestVolumesToResourcesPreservesAttachments(t *testing.T) {
	got := volumesToResources([]client.Volume{{
		ID: "root-1", Bootable: "true",
		Attachments: []client.VolumeAttachment{{
			AttachmentID: "attach-1", ServerID: "server-1", VolumeID: "root-1", Device: "/dev/vda",
		}},
	}}, "RegionOne", "project-1")
	if len(got) != 1 {
		t.Fatalf("want one volume, got %d", len(got))
	}
	attachments := asAnySlice(got[0].Data["attachments"])
	if len(attachments) != 1 || attServerID(attachments[0]) != "server-1" {
		t.Fatalf("attachments = %#v", attachments)
	}
	if !volumeIsBootable(got[0].Data) {
		t.Fatal("bootable root volume was not preserved")
	}
}
