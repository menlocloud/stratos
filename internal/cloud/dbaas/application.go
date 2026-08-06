package dbaas

// BuildApplication renders the ArgoCD Application CR for one database: pinned chart revision,
// FULL values inline (helm.valuesObject), destination = the project namespace on the DB cluster
// itself. The resources-finalizer makes an Application delete cascade to everything the chart
// rendered (engine CR, LB Service, NetworkPolicy) — our delete path relies on it; the LB
// Service delete in turn makes the OCCM tear down the Octavia LB on the tenant subnet.
//
// The BACKGROUND variant of the finalizer, deliberately. ArgoCD's default cascade deletes with
// foreground propagation, which makes the API server stamp a `foregroundDeletion` finalizer on
// each resource and hold it until its dependents are gone. An operator that keeps reconciling a
// resource it can still see then recreates those dependents faster than the GC removes them, and
// the delete LIVELOCKS: valkey-operator does exactly this — a deleted ValkeyCluster sat with
// `foregroundDeletion` while its headless Service was recreated every second, forever, and each
// pass minted a fresh PVC and a fresh Cinder volume until the project hit its volume quota.
//
// Background propagation deletes the parent immediately and lets the GC collect children by
// ownerReference, so the operator has no object left to reconcile. Ordering within the cascade is
// not something we rely on: the LB Service still carries its own OCCM finalizer, so Octavia is
// torn down properly either way.
func BuildApplication(cfg Config, spec DatabaseSpec, serviceID, chartVersion string, values map[string]any) map[string]any {
	if chartVersion == "" {
		chartVersion = cfg.ChartVersion
	}
	// The dedicated AppProject is the guardrail (sourceRepos/destinations constrained —
	// deploy/dbaas-cluster/appproject.yaml). Defaulting to ArgoCD's unrestricted "default"
	// project would silently drop it, so the fallback is the guardrail project's name.
	project := cfg.ArgoProject
	if project == "" {
		project = "stratos-dbaas"
	}
	return map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]any{
			"name":      spec.ID,
			"namespace": cfg.ArgoNamespace,
			"labels": map[string]any{
				LabelProject:   spec.ProjectID,
				LabelService:   serviceID,
				LabelManagedBy: ManagedByValue,
			},
			"annotations": map[string]any{
				AnnotationDisplayName: spec.DisplayName,
			},
			"finalizers": []any{"resources-finalizer.argocd.argoproj.io/background"},
		},
		"spec": map[string]any{
			"project": project,
			"source": map[string]any{
				"repoURL":        cfg.ChartRepo,
				"chart":          cfg.ChartName,
				"targetRevision": chartVersion,
				"helm":           map[string]any{"valuesObject": values},
			},
			"destination": map[string]any{
				"server":    "https://kubernetes.default.svc",
				"namespace": NamespaceFor(spec.ProjectID),
			},
			"syncPolicy": map[string]any{
				"automated": map[string]any{"prune": true, "selfHeal": true},
			},
		},
	}
}
