package kamaji

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/menlocloud/stratos/internal/cloud/client"
	"github.com/menlocloud/stratos/internal/cloud/kamajik8s"
)

// K8sAPI is the management-cluster surface Service consumes — implemented by *kamajik8s.Client,
// fake-able in tests.
type K8sAPI interface {
	EnsureNamespace(ctx context.Context, name string, labels map[string]string) error
	GetNamespace(ctx context.Context, name string) (map[string]any, error)
	DeleteNamespace(ctx context.Context, name string) error
	ApplySecret(ctx context.Context, ns, name string, stringData map[string]string, labels, annotations map[string]string) error
	GetSecretData(ctx context.Context, ns, name string) (map[string][]byte, error)
	ListSecrets(ctx context.Context, ns, labelSelector string) ([]map[string]any, error)
	ListSecretsAllNamespaces(ctx context.Context, labelSelector string) ([]map[string]any, error)
	DeleteSecret(ctx context.Context, ns, name string) error
	ApplyApplication(ctx context.Context, app map[string]any) error
	GetApplication(ctx context.Context, ns, name string) (map[string]any, error)
	ListApplications(ctx context.Context, ns, labelSelector string) ([]map[string]any, error)
	DeleteApplication(ctx context.Context, ns, name string) error
	GetTenantControlPlane(ctx context.Context, ns, name string) (map[string]any, error)
	ListTenantControlPlanes(ctx context.Context, ns, labelSelector string) ([]map[string]any, error)
	ListMachineDeployments(ctx context.Context, ns, labelSelector string) ([]map[string]any, error)
}

// defaultFinalizeGrace: a cloud-config secret younger than this is never treated as an orphan.
// This closes the create-race: CreateCluster applies the secret BEFORE the Application, so a
// concurrent finalize pass could otherwise see a fresh secret with no Application — the exact
// signature of a finished delete cascade — and destroy a cluster mid-create.
const defaultFinalizeGrace = 30 * time.Minute

// Service drives one kamaji provider (one management cluster). Built per external service via
// New (live) or NewWithAPI (tests).
type Service struct {
	api           K8sAPI
	cfg           Config
	serviceID     string
	finalizeGrace time.Duration
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
	return &Service{api: kc, cfg: cfg, serviceID: serviceID, finalizeGrace: defaultFinalizeGrace}, nil
}

// NewWithAPI builds a Service over a fake API (tests).
func NewWithAPI(api K8sAPI, cfg Config, serviceID string) *Service {
	return &Service{api: api, cfg: cfg, serviceID: serviceID, finalizeGrace: defaultFinalizeGrace}
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
	// The appcred annotations are the durable revocation record (plan D4) — the secret is the
	// only k8s-side object that outlives the Application, so the record rides on it.
	var ann map[string]string
	if spec.AppCredID != "" {
		ann = map[string]string{
			AnnotationAppCredID:      spec.AppCredID,
			AnnotationAppCredUser:    spec.AppCredUserID,
			AnnotationAppCredService: spec.AppCredServiceID,
		}
	}
	if err := s.api.ApplySecret(ctx, ns, CloudSecretName(spec.ID),
		map[string]string{"clouds.yaml": cloudsYAML},
		map[string]string{LabelProject: spec.ProjectID, LabelService: s.serviceID, LabelManagedBy: ManagedByValue},
		ann); err != nil {
		return nil, fmt.Errorf("kamaji: apply cloud credentials secret: %w", err)
	}
	values := BuildValues(s.cfg, spec)
	app := BuildApplication(s.cfg, spec, s.serviceID, s.cfg.ChartVersion, values)
	if err := s.api.ApplyApplication(ctx, app); err != nil {
		return nil, fmt.Errorf("kamaji: apply application: %w", err)
	}
	return clusterData(app, nil, nil), nil
}

// DeleteCluster removes the cluster: Application delete only (the resources-finalizer cascades
// the rendered chart). The clouds.yaml secret deliberately STAYS — CAPO/OCCM authenticate with
// it to delete the worker VMs and the API load balancer during the cascade, so removing it here
// would strand those cloud resources with stuck finalizers. FinalizeOrphans (sync-driven) reaps
// the secret, revokes the appcred and GCs the namespace once the cascade has finished.
// Idempotent (absent objects are success). Ownership-guarded: an Application without the
// managed-by marker is NOT ours (a pre-stratos cluster or foreign app that happens to share the
// name) — refuse rather than cascade-delete it.
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
	return nil
}

// RevokeCredResolver revokes one keystone application credential recorded on a cluster secret.
// osServiceID is the OpenStack externalService the credential was minted on (stamped at create;
// "" on legacy records). Contract is FAIL-CLOSED: any error keeps the secret — the only
// revocation record — for a later pass. This package deliberately never builds OpenStack
// clients; the caller resolves the service id to an admin client.
type RevokeCredResolver func(ctx context.Context, osServiceID, userID, credID string) error

// FinalizeOrphans completes asynchronous cluster deletions for ONE project (teardown + tests);
// the periodic reaper is FinalizeAllOrphans. Returns how many secrets still await the cascade.
func (s *Service) FinalizeOrphans(ctx context.Context, projectID string, revoke RevokeCredResolver) (int, error) {
	ns := NamespaceFor(projectID)
	sel := LabelProject + "=" + projectID + "," + LabelManagedBy + "=" + ManagedByValue
	secrets, err := s.api.ListSecrets(ctx, ns, sel)
	if err != nil {
		return 0, err
	}
	return s.finalizeNamespace(ctx, ns, projectID, secrets, revoke)
}

// FinalizeAllOrphans is the service-level orphan sweep, run once per sync cycle: it scans EVERY
// managed cloud-config secret of this provider across all namespaces — so leftovers of projects
// whose stratos doc is already gone (scheduled deletion, teardown) are still reaped — revokes
// recorded appcreds, deletes finished-cascade secrets and GCs emptied project namespaces.
// Returns the number of secrets still awaiting their cascade.
func (s *Service) FinalizeAllOrphans(ctx context.Context, revoke RevokeCredResolver) (int, error) {
	sel := LabelService + "=" + s.serviceID + "," + LabelManagedBy + "=" + ManagedByValue
	secrets, err := s.api.ListSecretsAllNamespaces(ctx, sel)
	if err != nil {
		return 0, err
	}
	byNS := map[string][]map[string]any{}
	for _, sec := range secrets {
		if ns := digStr(sec, "metadata", "namespace"); ns != "" {
			byNS[ns] = append(byNS[ns], sec)
		}
	}
	pending := 0
	var errs []error
	for ns, secs := range byNS {
		projectID := digStr(secs[0], "metadata", "labels", LabelProject)
		p, err := s.finalizeNamespace(ctx, ns, projectID, secs, revoke)
		pending += p
		if err != nil {
			errs = append(errs, err)
		}
	}
	return pending, errors.Join(errs...)
}

// finalizeNamespace reaps finished-cascade cluster secrets in one namespace. A secret is an
// orphan only when ALL of: it is older than the finalize grace window (create-race guard — the
// secret is applied before the Application), a point-in-time GetApplication finds no
// Application (never a stale list), and the TCP + MachineDeployments are gone (the cascade
// authenticates with this secret; reaping early would strand cloud resources). The namespace is
// GC'd only on a pass that actually reaped something and verified nothing lives there anymore —
// an idle project's bootstrap namespace is never touched.
func (s *Service) finalizeNamespace(ctx context.Context, ns, projectID string, secrets []map[string]any, revoke RevokeCredResolver) (int, error) {
	var errs []error
	pending, reaped := 0, 0
	for _, sec := range secrets {
		name := digStr(sec, "metadata", "name")
		cid, isCloud := strings.CutSuffix(name, cloudSecretSuffix)
		if !isCloud {
			continue
		}
		if created, err := time.Parse(time.RFC3339, digStr(sec, "metadata", "creationTimestamp")); err == nil {
			if time.Since(created) < s.finalizeGrace {
				// Possibly mid-create — the Application may not be applied yet. Counts as
				// pending: teardown reads pending as "the cascade may still need the tenant".
				pending++
				continue
			}
		}
		app, err := s.api.GetApplication(ctx, s.cfg.ArgoNamespace, cid)
		if err != nil {
			return pending, err
		}
		if app != nil {
			if dig(app, "metadata", "deletionTimestamp") != nil {
				pending++ // delete cascade in flight
			}
			continue // cluster alive (or deleting) — its secret is in use
		}
		tcp, err := s.findTCP(ctx, ns, cid)
		if err != nil {
			return pending, err
		}
		mds, err := s.api.ListMachineDeployments(ctx, ns, "cluster.x-k8s.io/cluster-name="+cid)
		if err != nil {
			return pending, err
		}
		if tcp != nil || len(mds) > 0 {
			pending++
			continue
		}
		if credID := digStr(sec, "metadata", "annotations", AnnotationAppCredID); credID != "" {
			userID := digStr(sec, "metadata", "annotations", AnnotationAppCredUser)
			svcID := digStr(sec, "metadata", "annotations", AnnotationAppCredService)
			if revoke == nil {
				errs = append(errs, fmt.Errorf("cluster %s: appcred %s not revoked (no revoker)", cid, credID))
				pending++
				continue
			}
			if err := revoke(ctx, svcID, userID, credID); err != nil {
				// Fail closed: the secret's annotations are the only revocation record — keep it.
				errs = append(errs, fmt.Errorf("cluster %s: revoke appcred: %w", cid, err))
				pending++
				continue
			}
		}
		if err := s.api.DeleteSecret(ctx, ns, name); err != nil {
			errs = append(errs, fmt.Errorf("cluster %s: delete cloud secret: %w", cid, err))
			pending++
			continue
		}
		reaped++
	}
	if reaped > 0 && pending == 0 {
		// Fresh look (never the caller's snapshot): any Application for this project means the
		// namespace is still in use.
		apps, err := s.api.ListApplications(ctx, s.cfg.ArgoNamespace,
			LabelProject+"="+projectID+","+LabelManagedBy+"="+ManagedByValue)
		if err != nil {
			errs = append(errs, err)
		} else if len(apps) == 0 {
			if err := s.gcNamespace(ctx, ns); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return pending, errors.Join(errs...)
}

// gcNamespace deletes the project namespace once it demonstrably holds no cluster remnants —
// and only if stratos created it (ownership label on the namespace).
func (s *Service) gcNamespace(ctx context.Context, ns string) error {
	nsObj, err := s.api.GetNamespace(ctx, ns)
	if err != nil || nsObj == nil {
		return err
	}
	if !managedBy(nsObj) {
		return nil
	}
	tcps, err := s.api.ListTenantControlPlanes(ctx, ns, "")
	if err != nil {
		return err
	}
	mds, err := s.api.ListMachineDeployments(ctx, ns, "")
	if err != nil {
		return err
	}
	if len(tcps) > 0 || len(mds) > 0 {
		return nil
	}
	return s.api.DeleteNamespace(ctx, ns)
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
