//go:build integration

package integration

import (
	"context"
	"reflect"
	"testing"

	"github.com/menlocloud/stratos/internal/cloud"
	"github.com/menlocloud/stratos/internal/cloud/client"
	"github.com/menlocloud/stratos/internal/cloud/providers"
)

// sliceLen counts elements of a value that may be []any (JSON round-trip) or nil.
func sliceLen(v any) int {
	if v == nil {
		return 0
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Slice {
		return rv.Len()
	}
	return -1
}

// fakeWriter records calls and returns canned objects, standing in for the live CloudClient so
// the write dispatch + CloudResource cache write are tested without a cloud.
type fakeWriter struct {
	// Embed the interface so fakeWriter satisfies providers.Writer even as it grows (the interface
	// carries ~75 niche methods this test never calls). Only the methods overridden below have real
	// behavior; an un-overridden method would panic on the nil embedded interface if ever called
	// (none are in these tests).
	providers.Writer
	created []string // type:id log
	deleted []string
	acted   []string // action log
}

func (f *fakeWriter) CreateNetwork(_ context.Context, o client.CreateNetworkOpts) (map[string]any, error) {
	f.created = append(f.created, "net:"+o.Name)
	return map[string]any{"id": "net-1", "name": o.Name, "status": "ACTIVE"}, nil
}
func (f *fakeWriter) GetNetwork(_ context.Context, id string) (map[string]any, error) {
	return map[string]any{"id": id, "name": "vpc", "status": "ACTIVE", "subnets": []any{"sn-1"}}, nil
}
func (f *fakeWriter) DeleteNetwork(_ context.Context, id string) error {
	f.deleted = append(f.deleted, "net:"+id)
	return nil
}
func (f *fakeWriter) CreateSubnet(_ context.Context, o client.CreateSubnetOpts) (map[string]any, error) {
	f.created = append(f.created, "subnet:"+o.CIDR)
	return map[string]any{"id": "sn-1", "cidr": o.CIDR}, nil
}
func (f *fakeWriter) DeleteSubnet(_ context.Context, id string) error { return nil }
func (f *fakeWriter) CreateServer(_ context.Context, o client.CreateServerOpts) (map[string]any, error) {
	f.created = append(f.created, "server:"+o.Name)
	return map[string]any{"id": "srv-1", "name": o.Name, "flavor": map[string]any{"id": o.FlavorID}}, nil
}
func (f *fakeWriter) GetServer(_ context.Context, id string) (map[string]any, error) {
	// Create re-reads the server for the fuller object; echo an id + a BUILD status.
	return map[string]any{"id": id, "name": "srv-1", "status": "BUILD"}, nil
}
func (f *fakeWriter) SetServerPassword(_ context.Context, id, password string) error { return nil }
func (f *fakeWriter) UpdateSubnet(_ context.Context, id string, o client.UpdateSubnetOpts) (map[string]any, error) {
	return map[string]any{"id": id}, nil
}
func (f *fakeWriter) DeleteServer(_ context.Context, id string) error {
	f.deleted = append(f.deleted, "server:"+id)
	return nil
}
func (f *fakeWriter) CreateVolume(_ context.Context, o client.CreateVolumeOpts) (map[string]any, error) {
	f.created = append(f.created, "volume:"+o.Name)
	return map[string]any{"id": "vol-1", "name": o.Name, "size": o.Size}, nil
}
func (f *fakeWriter) DeleteVolume(_ context.Context, id string) error {
	f.deleted = append(f.deleted, "volume:"+id)
	return nil
}
func (f *fakeWriter) CreatePort(_ context.Context, o client.CreatePortOpts) (map[string]any, error) {
	f.created = append(f.created, "port:"+o.NetworkID)
	return map[string]any{"id": "port-1", "network_id": o.NetworkID}, nil
}
func (f *fakeWriter) DeletePort(_ context.Context, id string) error {
	f.deleted = append(f.deleted, "port:"+id)
	return nil
}
func (f *fakeWriter) CreateFloatingIP(_ context.Context, o client.CreateFloatingIPOpts) (map[string]any, error) {
	f.created = append(f.created, "fip:"+o.FloatingNetworkID)
	return map[string]any{"id": "fip-1", "floating_network_id": o.FloatingNetworkID}, nil
}
func (f *fakeWriter) DeleteFloatingIP(_ context.Context, id string) error {
	f.deleted = append(f.deleted, "fip:"+id)
	return nil
}
func (f *fakeWriter) AttachVolume(_ context.Context, serverID, volumeID string) (map[string]any, error) {
	f.acted = append(f.acted, "attach:"+serverID+":"+volumeID)
	return map[string]any{"id": "att-1", "device": "/dev/vdb"}, nil
}
func (f *fakeWriter) DetachVolume(_ context.Context, serverID, attachmentID string) error {
	f.acted = append(f.acted, "detach:"+serverID+":"+attachmentID)
	return nil
}
func (f *fakeWriter) RebootServer(_ context.Context, id string, hard bool) error {
	f.acted = append(f.acted, "reboot:"+id)
	return nil
}
func (f *fakeWriter) StartServer(_ context.Context, id string) error {
	f.acted = append(f.acted, "start:"+id)
	return nil
}
func (f *fakeWriter) StopServer(_ context.Context, id string) error {
	f.acted = append(f.acted, "stop:"+id)
	return nil
}
func (f *fakeWriter) AssociateFloatingIP(_ context.Context, fipID, portID string) (map[string]any, error) {
	f.acted = append(f.acted, "assign:"+fipID+":"+portID)
	return map[string]any{"id": fipID, "port_id": portID}, nil
}
func (f *fakeWriter) DisassociateFloatingIP(_ context.Context, fipID string) (map[string]any, error) {
	f.acted = append(f.acted, "unassign:"+fipID)
	return map[string]any{"id": fipID}, nil
}

// TestCloudWriteDispatch exercises providers.WriteService.Create/Delete (create/delete
// by resource type) against real Postgres with a fake cloud writer:
// network (+default subnet), server, volume → cache upsert; then delete → cache archive.
func TestCloudWriteDispatch(t *testing.T) {
	ctx := context.Background()
	repo := cloud.NewRepo(freshPG(t))
	fw := &fakeWriter{}
	svc := providers.NewWriteService(fw, repo)

	// NETWORK with default subnet → network + subnet created, cache holds data.network + name.
	cr, err := svc.Create(ctx, "svc1", "RegionOne", "proj1", "user1", providers.CreateRequest{
		Type: cloud.TypeNetwork,
		Data: map[string]any{"name": "vpc-a", "defaultSubnet": true, "cidr": "10.0.0.0/24", "gateway": true, "enableDhcp": true},
	})
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	if cr.ExternalID != "net-1" || cr.Type != cloud.TypeNetwork || cr.ProjectID != "proj1" {
		t.Fatalf("network CR wrong: %+v", cr)
	}
	if cr.Data["networkName"] != "vpc-a" || cr.Data["network"] == nil {
		t.Fatalf("network data wrong: %v", cr.Data)
	}
	if got, _ := repo.FindByServiceIDAndExternalID(ctx, "svc1", "net-1"); got == nil {
		t.Fatal("network not cached")
	}

	// SERVER → cache holds data.server. Servers are volume-backed: rootVolume is
	// mandatory at the WriteService layer (the HTTP handler defaults it).
	cr, err = svc.Create(ctx, "svc1", "RegionOne", "proj1", "user1", providers.CreateRequest{
		Type: cloud.TypeServer,
		Data: map[string]any{
			"name": "vm-a", "flavorId": "f1", "imageId": "i1", "networkIds": []any{"net-1"},
			"rootVolume": map[string]any{"sizeGiB": float64(20), "type": "ssd"},
		},
	})
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	if cr.ExternalID != "srv-1" || cr.Data["server"] == nil {
		t.Fatalf("server CR wrong: %+v", cr)
	}

	// VOLUME → cache holds data.volume + empty attachments.
	cr, err = svc.Create(ctx, "svc1", "RegionOne", "proj1", "user1", providers.CreateRequest{
		Type: cloud.TypeVolume,
		Data: map[string]any{"name": "vol-a", "size": float64(10)},
	})
	if err != nil {
		t.Fatalf("create volume: %v", err)
	}
	if cr.ExternalID != "vol-1" || cr.Data["volume"] == nil {
		t.Fatalf("volume CR wrong: %+v", cr)
	}

	// delete server → cloud delete + cache archive.
	if err := svc.Delete(ctx, "svc1", "srv-1"); err != nil {
		t.Fatalf("delete server: %v", err)
	}
	if got, _ := repo.FindByServiceIDAndExternalID(ctx, "svc1", "srv-1"); got != nil {
		t.Fatalf("server still cached after delete: %+v", got)
	}

	// verify the fake writer saw the expected calls.
	wantCreated := []string{"net:vpc-a", "subnet:10.0.0.0/24", "server:vm-a", "volume:vol-a"}
	if len(fw.created) != len(wantCreated) {
		t.Fatalf("created calls = %v, want %v", fw.created, wantCreated)
	}
	if len(fw.deleted) != 1 || fw.deleted[0] != "server:srv-1" {
		t.Fatalf("deleted calls = %v, want [server:srv-1]", fw.deleted)
	}
}

// TestCloudWriteActionsAndTypes covers the action endpoint (volume attach/detach, server
// reboot, floatingip assign/unassign) + the PORT/FLOATING_IP create+delete dispatch.
func TestCloudWriteActionsAndTypes(t *testing.T) {
	ctx := context.Background()
	repo := cloud.NewRepo(freshPG(t))
	fw := &fakeWriter{}
	svc := providers.NewWriteService(fw, repo)

	mk := func(typ string, data map[string]any) *cloud.CloudResource {
		cr, err := svc.Create(ctx, "svc1", "RegionOne", "proj1", "user1", providers.CreateRequest{Type: typ, Data: data})
		if err != nil {
			t.Fatalf("create %s: %v", typ, err)
		}
		return cr
	}

	// PORT + FLOATING_IP create → cache.
	port := mk(cloud.TypePort, map[string]any{"networkId": "net-1", "name": "p"})
	if port.ExternalID != "port-1" || port.Data["port"] == nil {
		t.Fatalf("port CR wrong: %+v", port)
	}
	fip := mk(cloud.TypeFloatingIP, map[string]any{"floatingNetworkId": "guest", "description": "x"})
	if fip.ExternalID != "fip-1" || fip.Data["floatingIp"] == nil {
		t.Fatalf("fip CR wrong: %+v", fip)
	}
	vol := mk(cloud.TypeVolume, map[string]any{"name": "v", "size": float64(1)})

	// volume ATTACH → attachment recorded in cache.
	cr, err := svc.Action(ctx, "svc1", "proj1", vol.ExternalID, "ATTACH", map[string]any{"serverId": "srv-9"})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if n := sliceLen(cr.Data["attachments"]); n != 1 {
		t.Fatalf("attach: attachments len=%d (%v)", n, cr.Data["attachments"])
	}
	// volume DETACH → attachment removed.
	cr, err = svc.Action(ctx, "svc1", "proj1", vol.ExternalID, "DETACH", map[string]any{"serverId": "srv-9"})
	if err != nil {
		t.Fatalf("detach: %v", err)
	}
	if n := sliceLen(cr.Data["attachments"]); n != 0 {
		t.Fatalf("detach: attachments len=%d (%v)", n, cr.Data["attachments"])
	}

	// floatingip ASSIGN/UNASSIGN. The FE sends the port's INTERNAL cloud-resource id as data.id
	// (FloatingIPsPage: value={p.id}); resolveExtID maps it to the external id ("port-1") for the
	// cloud call. Passing the external id literal here would resolve to "" (no internal row by that id).
	if _, err := svc.Action(ctx, "svc1", "proj1", fip.ExternalID, "ASSIGN", map[string]any{"id": port.ID}); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if _, err := svc.Action(ctx, "svc1", "proj1", fip.ExternalID, "UNASSIGN", nil); err != nil {
		t.Fatalf("unassign: %v", err)
	}

	// server REBOOT (no cache change, just the cloud call).
	srv := mk(cloud.TypeServer, map[string]any{
		"name": "s", "flavorId": "f1", "imageId": "i1",
		"rootVolume": map[string]any{"sizeGiB": float64(20), "type": "ssd"},
	})
	if _, err := svc.Action(ctx, "svc1", "proj1", srv.ExternalID, "REBOOT_SOFT", nil); err != nil {
		t.Fatalf("reboot: %v", err)
	}

	// delete PORT + FLOATING_IP → cloud delete dispatched.
	if err := svc.Delete(ctx, "svc1", "port-1"); err != nil {
		t.Fatalf("delete port: %v", err)
	}
	if err := svc.Delete(ctx, "svc1", "fip-1"); err != nil {
		t.Fatalf("delete fip: %v", err)
	}

	wantActed := []string{"attach:srv-9:vol-1", "detach:srv-9:vol-1", "assign:fip-1:port-1", "unassign:fip-1", "reboot:srv-1"}
	if len(fw.acted) != len(wantActed) {
		t.Fatalf("acted = %v, want %v", fw.acted, wantActed)
	}
	for i := range wantActed {
		if fw.acted[i] != wantActed[i] {
			t.Fatalf("acted[%d] = %q, want %q (all: %v)", i, fw.acted[i], wantActed[i], fw.acted)
		}
	}
	// unsupported action → error.
	if _, err := svc.Action(ctx, "svc1", "proj1", srv.ExternalID, "BOGUS", nil); err == nil {
		t.Fatal("expected error for unsupported action")
	}
}
