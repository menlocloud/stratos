package kamaji

import (
	"context"
	"fmt"

	"github.com/menlocloud/stratos/internal/cloud/client"
	"github.com/menlocloud/stratos/internal/cloud/kamajik8s"
)

// K8sAPI is the management-cluster surface Service consumes — implemented by *kamajik8s.Client,
// fake-able in tests.
type K8sAPI interface {
	EnsureNamespace(ctx context.Context, name string, labels map[string]string) error
	ApplySecret(ctx context.Context, ns, name string, stringData map[string]string, labels map[string]string) error
	GetSecretData(ctx context.Context, ns, name string) (map[string][]byte, error)
	DeleteSecret(ctx context.Context, ns, name string) error
	ApplyApplication(ctx context.Context, app map[string]any) error
	GetApplication(ctx context.Context, ns, name string) (map[string]any, error)
	ListApplications(ctx context.Context, ns, labelSelector string) ([]map[string]any, error)
	DeleteApplication(ctx context.Context, ns, name string) error
	GetTenantControlPlane(ctx context.Context, ns, name string) (map[string]any, error)
	ListTenantControlPlanes(ctx context.Context, ns, labelSelector string) ([]map[string]any, error)
	ListMachineDeployments(ctx context.Context, ns, labelSelector string) ([]map[string]any, error)
}

// Service drives one kamaji provider (one management cluster). Built per external service via
// New (live) or NewWithAPI (tests).
type Service struct {
	api       K8sAPI
	cfg       Config
	serviceID string
}

// New builds a live Service from the provider config.
func New(cfg Config, serviceID string) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	kc, err := kamajik8s.New(cfg.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("kamaji: management kubeconfig: %w", err)
	}
	return &Service{api: kc, cfg: cfg, serviceID: serviceID}, nil
}

// NewWithAPI builds a Service over a fake API (tests).
func NewWithAPI(api K8sAPI, cfg Config, serviceID string) *Service {
	return &Service{api: api, cfg: cfg, serviceID: serviceID}
}

// Config exposes the provider config (read-only use: version catalog, chart pin).
func (s *Service) Config() Config { return s.cfg }

// EnsureProjectNamespace creates/labels the project's namespace on the management cluster —
// the kamaji bootstrap leg (BootstrapKamajiOnto).
func (s *Service) EnsureProjectNamespace(ctx context.Context, projectID string) error {
	return s.api.EnsureNamespace(ctx, NamespaceFor(projectID), map[string]string{
		LabelProject:   projectID,
		LabelService:   s.serviceID,
		LabelManagedBy: ManagedByValue,
	})
}

// CreateCluster provisions one cluster: clouds.yaml secret (mgmt-side only) + Application CR.
// osCfg is the tenant-scoped OpenStack config (ClientConfigForProject) the worker VMs / OCCM /
// cinder-csi run under — the CUSTOMER's own project (plan D4). Returns the initial cached data
// payload (status PENDING until the first sync reads real health).
func (s *Service) CreateCluster(ctx context.Context, spec ClusterSpec, osCfg client.Config) (map[string]any, error) {
	if err := spec.Validate(s.cfg.Defaults); err != nil {
		return nil, err
	}
	ns := NamespaceFor(spec.ProjectID)
	if err := s.EnsureProjectNamespace(ctx, spec.ProjectID); err != nil {
		return nil, fmt.Errorf("kamaji: ensure namespace: %w", err)
	}
	cloudsYAML, err := CloudsYAML(osCfg)
	if err != nil {
		return nil, err
	}
	if err := s.api.ApplySecret(ctx, ns, CloudSecretName(spec.ID),
		map[string]string{"clouds.yaml": cloudsYAML},
		map[string]string{LabelProject: spec.ProjectID, LabelService: s.serviceID, LabelManagedBy: ManagedByValue}); err != nil {
		return nil, fmt.Errorf("kamaji: apply cloud credentials secret: %w", err)
	}
	values := BuildValues(s.cfg, spec)
	app := BuildApplication(s.cfg, spec, s.serviceID, s.cfg.ChartVersion, values)
	if err := s.api.ApplyApplication(ctx, app); err != nil {
		return nil, fmt.Errorf("kamaji: apply application: %w", err)
	}
	return clusterData(app, nil, nil), nil
}

// DeleteCluster removes the cluster: Application delete (finalizer cascades the rendered chart)
// + the clouds.yaml secret. Idempotent (absent objects are success). Ownership-guarded: an
// Application without the managed-by marker is NOT ours (a pre-stratos cluster or foreign app
// that happens to share the name) — refuse rather than cascade-delete it.
func (s *Service) DeleteCluster(ctx context.Context, projectID, clusterID string) error {
	app, err := s.api.GetApplication(ctx, s.cfg.ArgoNamespace, clusterID)
	if err != nil {
		return err
	}
	if app != nil {
		if !managedBy(app) {
			return fmt.Errorf("kamaji: cluster %s is not managed by stratos — refusing to delete", clusterID)
		}
		if err := s.api.DeleteApplication(ctx, s.cfg.ArgoNamespace, clusterID); err != nil {
			return fmt.Errorf("kamaji: delete application: %w", err)
		}
	}
	if err := s.api.DeleteSecret(ctx, NamespaceFor(projectID), CloudSecretName(clusterID)); err != nil {
		return fmt.Errorf("kamaji: delete cloud credentials secret: %w", err)
	}
	return nil
}

// AdminKubeconfig fetches the cluster's admin kubeconfig from the Kamaji-generated secret —
// read-on-demand, streamed to the caller, NEVER stored in stratos (plan D5).
func (s *Service) AdminKubeconfig(ctx context.Context, projectID, clusterID string) ([]byte, error) {
	ns := NamespaceFor(projectID)
	tcp, err := s.findTCP(ctx, ns, clusterID)
	if err != nil {
		return nil, err
	}
	if tcp == nil {
		return nil, fmt.Errorf("kamaji: cluster %s: control plane not found (still provisioning?)", clusterID)
	}
	name, _ := dig(tcp, "metadata", "name").(string)
	// Kamaji admin kubeconfig secret convention: <tcp>-admin-kubeconfig
	// (https://kamaji.clastix.io/concepts/tenant-control-plane/).
	data, err := s.api.GetSecretData(ctx, ns, name+"-admin-kubeconfig")
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("kamaji: cluster %s: admin kubeconfig not ready", clusterID)
	}
	// The secret holds the kubeconfig under "admin.conf" (kamaji convention); fall back to the
	// single value if the key differs across kamaji versions.
	if b, ok := data["admin.conf"]; ok {
		return b, nil
	}
	for _, b := range data {
		return b, nil
	}
	return nil, fmt.Errorf("kamaji: cluster %s: admin kubeconfig secret is empty", clusterID)
}

// PatchClusterValues mutates the Application's helm values in place (UPGRADE, SET_NODE_GROUPS…):
// read → mutate → re-apply with the SAME chart pin. One reconcile path for every change (plan §9).
func (s *Service) PatchClusterValues(ctx context.Context, clusterID string, mutate func(values map[string]any) error) error {
	app, err := s.api.GetApplication(ctx, s.cfg.ArgoNamespace, clusterID)
	if err != nil {
		return err
	}
	if app == nil {
		return fmt.Errorf("kamaji: cluster %s not found", clusterID)
	}
	if !managedBy(app) {
		return fmt.Errorf("kamaji: cluster %s is not managed by stratos — refusing to modify", clusterID)
	}
	values, _ := dig(app, "spec", "source", "helm", "valuesObject").(map[string]any)
	if values == nil {
		return fmt.Errorf("kamaji: cluster %s: application carries no values", clusterID)
	}
	if err := mutate(values); err != nil {
		return err
	}
	// Re-apply only the fields stratos owns (SSA merges; metadata.name/namespace route the patch).
	patch := map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]any{
			"name":      clusterID,
			"namespace": s.cfg.ArgoNamespace,
		},
		"spec": map[string]any{
			"source": map[string]any{
				"repoURL":        dig(app, "spec", "source", "repoURL"),
				"chart":          dig(app, "spec", "source", "chart"),
				"targetRevision": dig(app, "spec", "source", "targetRevision"),
				"helm":           map[string]any{"valuesObject": values},
			},
		},
	}
	return s.api.ApplyApplication(ctx, patch)
}

// findTCP resolves the cluster's TenantControlPlane: direct name first (chart names the TCP
// after the release in the wrappers), else the helm release-instance label. Nil when absent.
func (s *Service) findTCP(ctx context.Context, ns, clusterID string) (map[string]any, error) {
	tcp, err := s.api.GetTenantControlPlane(ctx, ns, clusterID)
	if err != nil || tcp != nil {
		return tcp, err
	}
	list, err := s.api.ListTenantControlPlanes(ctx, ns, "app.kubernetes.io/instance="+clusterID)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return list[0], nil
}

// dig walks nested map[string]any keys; nil when any hop is absent.
func dig(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func digStr(m map[string]any, keys ...string) string {
	s, _ := dig(m, keys...).(string)
	return s
}
