package kamaji

import (
	"context"
	"strings"
	"testing"
)

// TestForeignServiceApplicationIsUnreachable is the mirror of the dbaas test of the same name,
// and it exists for the same reason: the Managed-Database provider can be pointed at the SAME
// ArgoCD and the SAME namespace as this one — in production it is — and it stamps the identical
// `managed-by=stratos` and `stratos.io/project` labels on its database Applications. Selecting
// on those alone made a database appear in this provider's cache typed as a KUBERNETES CLUSTER,
// and the Application delete cascades through the argocd resources finalizer: a live database
// and its volumes destroyed by a customer deleting what the UI told them was a cluster.
func TestForeignServiceApplicationIsUnreachable(t *testing.T) {
	const (
		ours    = "svc-k8s"
		foreign = "svc-db"
		project = "p1"
		alien   = "std-deadbeef" // a dbaas DATABASE Application, note the std- prefix
	)
	ctx := context.Background()
	cfg := testCfg()
	ns := NamespaceFor(project)

	api := newFakeAPI()
	api.apps[cfg.ArgoNamespace+"/"+alien] = map[string]any{
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
			"source": map[string]any{"chart": "database-cluster", "targetRevision": "0.4.5"},
		},
	}
	svc := NewWithAPI(api, cfg, ours)

	// READS — a row in either is what mints the phantom the delete path then acts on.
	rows, err := svc.SyncProvider(cfg.Region, project).List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ExternalID == alien {
			t.Fatalf("sync surfaced another service's Application as a cluster: %+v", r)
		}
	}
	pins, err := svc.ListClusterPins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pins {
		if p.ID == alien {
			t.Fatal("admin pin table listed another service's Application — 'Bump all' would re-pin it")
		}
	}

	// WRITES — reached by id from a URL path, never through a selector.
	if err := svc.SetChartVersion(ctx, alien, "0.7.1"); err == nil {
		t.Fatal("SetChartVersion re-pinned another service's Application")
	} else if !strings.Contains(err.Error(), "not managed") {
		t.Errorf("unexpected error: %v", err)
	}
	if err := svc.DeleteCluster(ctx, project, alien); err == nil {
		t.Fatal("DeleteCluster destroyed another service's Application")
	}
	if _, ok := api.apps[cfg.ArgoNamespace+"/"+alien]; !ok {
		t.Fatal("the alien Application was deleted")
	}

	// NAMESPACE GC — dbaas no longer lands here (it owns the stdb- prefix), but a SECOND kamaji
	// provider on the same cluster still shares a project's namespace, and each keeps its
	// keystone-appcred revocation record in it after its Application is gone. Collecting the
	// namespace loses the only record by which that credential can be revoked.
	api.namespaces[ns] = map[string]string{LabelManagedBy: ManagedByValue}
	api.secretObjs[ns+"/stc-0ther1234-cloud-config"] = map[string]any{
		"metadata": map[string]any{
			"name": "stc-0ther1234-cloud-config",
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
	delete(api.secretObjs, ns+"/stc-0ther1234-cloud-config")
	if err := svc.gcNamespace(ctx, ns); err != nil {
		t.Fatal(err)
	}
	if _, ok := api.namespaces[ns]; ok {
		t.Fatal("gcNamespace refused to collect a namespace with no foreign remnants")
	}
}
