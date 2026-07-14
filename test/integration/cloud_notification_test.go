//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/menlocloud/stratos/internal/cloud"
	"github.com/menlocloud/stratos/internal/cloud/notification"
)

// notifFetcher / notifResolver are the test doubles for the os-notification handler — they stand
// in for the admin-scoped OpenStack GET and the project lookup so Handle is exercised end to
// end against real Postgres without a live cloud (our-side verification; the live ceilometer
// HTTP-publisher path is verified separately).
type notifFetcher struct {
	obj   map[string]any
	found bool
}

func (f notifFetcher) Get(_ context.Context, _, _, _ string) (map[string]any, bool, error) {
	return f.obj, f.found, nil
}

type notifResolver map[string]string

func (r notifResolver) ByExternalID(_ context.Context, ext string) (string, bool) {
	id, ok := r[ext]
	return id, ok
}

// TestNotificationHandle exercises notification.Service.Handle: CREATE_UPDATE upsert,
// delete-event archive, fetch-nil archive,
// and the skip paths (unmapped event / unresolvable project), against real Postgres.
func TestNotificationHandle(t *testing.T) {
	ctx := context.Background()
	repo := cloud.NewRepo(freshPG(t))
	resolver := notifResolver{"os-proj-1": "internal-proj-1"}
	ts := time.Now().UTC().Truncate(time.Millisecond)

	// 1. CREATE_UPDATE: a network.create event whose live object the fetcher returns → upsert.
	svc := notification.NewService(repo, notifFetcher{obj: map[string]any{"id": "net-1", "name": "vpc-a"}, found: true}, resolver, nil)
	msg := notification.OsloMessage{
		EventType: "network.create.end",
		Timestamp: notification.OsloTimeAt(ts),
		Payload:   map[string]any{"network_id": "net-1", "tenant_id": "os-proj-1"},
	}
	if err := svc.Handle(ctx, "svc1", "RegionOne", msg); err != nil {
		t.Fatalf("create-update: %v", err)
	}
	got, err := repo.FindByServiceIDAndExternalID(ctx, "svc1", "net-1")
	if err != nil || got == nil {
		t.Fatalf("create-update: not cached: %v / %v", err, got)
	}
	if got.Type != cloud.TypeNetwork || got.ProjectID != "internal-proj-1" || got.Region != "RegionOne" {
		t.Fatalf("create-update: wrong fields: %+v", got)
	}
	if got.Data["name"] != "vpc-a" {
		t.Fatalf("create-update: data not stored: %v", got.Data)
	}

	// 2. DELETE event: the same resource is removed from the cache (and archived).
	delMsg := notification.OsloMessage{
		EventType: "network.delete.end",
		Timestamp: notification.OsloTimeAt(ts),
		Payload:   map[string]any{"network_id": "net-1", "tenant_id": "os-proj-1"},
	}
	if err := svc.Handle(ctx, "svc1", "RegionOne", delMsg); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got, _ := repo.FindByServiceIDAndExternalID(ctx, "svc1", "net-1"); got != nil {
		t.Fatalf("delete: still cached: %+v", got)
	}

	// 3. fetch-nil ⇒ DELETE: a create-class event whose object no longer exists in OpenStack.
	// Seed a cached doc, then a non-delete event whose fetcher reports found=false → removed.
	seed := &cloud.CloudResource{ServiceID: "svc1", ExternalID: "port-9", Type: cloud.TypePort, ProjectID: "internal-proj-1", Region: "RegionOne", CreatedAt: &ts, UpdatedAt: &ts}
	if _, err := repo.Insert(ctx, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	gone := notification.NewService(repo, notifFetcher{found: false}, resolver, nil)
	if err := gone.Handle(ctx, "svc1", "RegionOne", notification.OsloMessage{
		EventType: "port.update.end",
		Payload:   map[string]any{"port_id": "port-9", "tenant_id": "os-proj-1"},
	}); err != nil {
		t.Fatalf("fetch-nil: %v", err)
	}
	if got, _ := repo.FindByServiceIDAndExternalID(ctx, "svc1", "port-9"); got != nil {
		t.Fatalf("fetch-nil: still cached: %+v", got)
	}

	// 4. SKIP: unmapped event_type → no-op (no error, nothing written).
	if err := svc.Handle(ctx, "svc1", "RegionOne", notification.OsloMessage{
		EventType: "identity.user.created",
		Payload:   map[string]any{"resource_info": "u1", "tenant_id": "os-proj-1"},
	}); err != nil {
		t.Fatalf("skip-unmapped: %v", err)
	}

	// 5. SKIP: unresolvable project (unknown tenant, no cached resource) → no-op.
	if err := svc.Handle(ctx, "svc1", "RegionOne", notification.OsloMessage{
		EventType: "volume.create.end",
		Payload:   map[string]any{"volume_id": "vol-x", "tenant_id": "unknown-tenant"},
	}); err != nil {
		t.Fatalf("skip-noproject: %v", err)
	}
	if got, _ := repo.FindByServiceIDAndExternalID(ctx, "svc1", "vol-x"); got != nil {
		t.Fatalf("skip-noproject: should not cache: %+v", got)
	}
}
