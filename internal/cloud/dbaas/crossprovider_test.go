package dbaas

import (
	"context"
	"strings"
	"testing"
)

// TestForeignServiceApplicationIsUnreachable pins the cross-provider isolation invariant.
//
// The Managed-Kubernetes provider can be pointed at the SAME ArgoCD and the SAME namespace as
// this one — in production it is — and it stamps the identical `managed-by=stratos` and
// `stratos.io/project` labels on its cluster Applications. Selecting on those alone made a
// kamaji cluster appear in this provider's cache typed as a DATABASE, and the Application delete
// cascades through the argocd resources finalizer: a live Kubernetes cluster destroyed by a
// customer deleting what the UI told them was a database.
//
// So every read scopes to `stratos.io/service`, and every write and delete funnel proves
// ownership rather than trusting the fleet-wide marker.
func TestForeignServiceApplicationIsUnreachable(t *testing.T) {
	const (
		ours    = "svc-db"
		foreign = "svc-k8s"
		project = "p1"
		alien   = "stc-deadbeef" // a kamaji CLUSTER Application, note the stc- prefix
	)
	ctx := context.Background()
	cfg := testConfig()
	ns := NamespaceFor(project)

	api := newFakeAPI()
	api.apps[key(cfg.ArgoNamespace, alien)] = map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]any{
			"name":      alien,
			"namespace": cfg.ArgoNamespace,
			"labels": map[string]any{
				LabelManagedBy: ManagedByValue,
				LabelProject:   project,
				LabelService:   foreign,
			},
		},
		"spec": map[string]any{
			"source": map[string]any{
				"chart": "openstack-kamaji-cluster", "targetRevision": "0.7.1",
			},
		},
	}
	svc := NewWithAPI(api, cfg, ours)

	// READS — the alien must not enter the cache or the admin pin table. A row in either is
	// what mints the phantom the delete path then acts on.
	rows, err := svc.SyncProvider(cfg.Region, project).List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ExternalID == alien {
			t.Fatalf("sync surfaced another service's Application as a database: %+v", r)
		}
	}
	pins, err := svc.ListDatabasePins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pins {
		if p.ID == alien {
			t.Fatal("admin pin table listed another service's Application — 'Bump all' would re-pin it")
		}
	}

	// WRITES — reached by id from a URL path, never through a selector, so the guard has to live
	// in the funnel itself.
	if err := svc.SetChartVersion(ctx, alien, "0.4.5"); err == nil {
		t.Fatal("SetChartVersion re-pinned another service's Application")
	} else if !strings.Contains(err.Error(), "not managed") {
		t.Errorf("unexpected error: %v", err)
	}
	if err := svc.DeleteDatabase(ctx, project, alien); err == nil {
		t.Fatal("DeleteDatabase destroyed another service's Application")
	}
	if _, ok := api.apps[key(cfg.ArgoNamespace, alien)]; !ok {
		t.Fatal("the alien Application was deleted")
	}

	// NAMESPACE GC — kamaji no longer lands here (dbaas owns the stdb- prefix), but a SECOND
	// dbaas provider on the same cluster still shares a project's namespace, and each keeps its
	// neutron-share revocation marker in it AFTER its Application is gone. A foreign remnant is
	// a reason to stop, whoever owns it.
	api.namespaces[ns] = map[string]any{
		"metadata": map[string]any{
			"name":   ns,
			"labels": map[string]any{LabelManagedBy: ManagedByValue},
		},
	}
	api.secrets[key(ns, "std-0ther1234-net-share")] = map[string]any{
		"metadata": map[string]any{
			"name": "std-0ther1234-net-share",
			"labels": map[string]any{
				LabelManagedBy: ManagedByValue,
				LabelService:   foreign,
			},
		},
	}
	if err := svc.gcNamespace(ctx, ns); err != nil {
		t.Fatal(err)
	}
	if _, ok := api.namespaces[ns]; !ok {
		t.Fatal("gcNamespace deleted a namespace still holding another service's revocation record")
	}

	// With the foreign remnant gone it may proceed — the probe must not wedge the GC shut.
	delete(api.secrets, key(ns, "std-0ther1234-net-share"))
	if err := svc.gcNamespace(ctx, ns); err != nil {
		t.Fatal(err)
	}
	if _, ok := api.namespaces[ns]; ok {
		t.Fatal("gcNamespace refused to collect a namespace with no foreign remnants")
	}
}
