// Package kamaji drives Managed Kubernetes clusters on a Kamaji management cluster: one
// ArgoCD Application per customer cluster (chart `openstack-kamaji-cluster` from our OCI
// registry, pinned targetRevision + full generated values), applied through the management
// cluster's own API (the Application CRD — no ArgoCD API/auth involved). ArgoCD renders,
// syncs and health-checks; stratos reads status back off the Application + the Kamaji
// TenantControlPlane + CAPI MachineDeployments. See tasks/managed-k8s-plan.md (D3/D7/§9).
package kamaji

import (
	"fmt"
	"slices"
)

// Namespace / naming derivations — the ONE place these are derived (ceph RGWUIDFor precedent).
// Customer-typed names are display-only; every k8s-side identifier is the generated cluster id
// (`stc-<8 hex>`), so duplicate display names, unicode and the RFC1123/63-char limit are
// non-issues, and the API endpoint/DNS never changes on rename.

// NamespaceFor is the management-cluster namespace holding a project's clusters.
func NamespaceFor(projectID string) string { return "st-" + projectID }

// CloudSecretName is the per-cluster clouds.yaml secret (management-cluster side ONLY — the
// customer cluster never sees it; see plan §4/D7).
func CloudSecretName(clusterID string) string { return clusterID + cloudSecretSuffix }

// cloudSecretSuffix lets FinalizeOrphans map a secret name back to its cluster id.
const cloudSecretSuffix = "-cloud-config"

// Application labels/annotations stamped by stratos and read back by the sync.
//
// LabelManagedBy/ManagedByValue is the OWNERSHIP marker (decision 2026-07-12): clusters that
// pre-date stratos on the management cluster (the infra-ops wrappers) must stay invisible and
// untouchable — the sync lists ONLY objects carrying this label, and delete/patch REFUSE any
// object without it. Pre-existing clusters get migrated by hand later (stamp the labels).
const (
	LabelProject          = "stratos.io/project"
	LabelService          = "stratos.io/service"
	LabelManagedBy        = "app.kubernetes.io/managed-by"
	ManagedByValue        = "stratos"
	AnnotationDisplayName = "stratos.io/display-name"
	// Appcred annotations on the per-cluster clouds.yaml secret: the ONLY durable record of the
	// keystone application credential minted for the cluster (plan D4). FinalizeOrphans reads them
	// to revoke the credential once the ArgoCD delete cascade has finished with it.
	AnnotationAppCredID   = "stratos.io/appcred-id"
	AnnotationAppCredUser = "stratos.io/appcred-user"
	// AnnotationAppCredService records WHICH OpenStack externalService minted the credential, so
	// the service-level sweep can revoke it even after the project doc (and its service
	// bindings) is gone.
	AnnotationAppCredService = "stratos.io/appcred-service"
)

// managedBy reports whether obj carries the stratos ownership marker.
func managedBy(obj map[string]any) bool {
	labels, _ := dig(obj, "metadata", "labels").(map[string]any)
	v, _ := labels[LabelManagedBy].(string)
	return v == ManagedByValue
}

// Config is the provider-level connection + chart contract, assembled from the kamaji
// externalService document by externalservice.KamajiConfig.
type Config struct {
	Kubeconfig    string // management-cluster kubeconfig (provider secret)
	Region        string // the stratos region stamped on cached resources
	ArgoNamespace string // namespace holding Application CRs (default "argocd")
	ArgoProject   string // AppProject constraining sources/destinations (default "default")
	ChartRepo     string // OCI helm repo, e.g. "ghcr.io/menlocloud/charts" (no oci:// prefix in Argo repoURL)
	ChartName     string // default "openstack-kamaji-cluster"
	ChartVersion  string // pinned default for NEW clusters; existing clusters keep their own pin
	Defaults      ClusterDefaults
}

// ClusterDefaults are the per-provider chart value defaults every cluster inherits.
type ClusterDefaults struct {
	DataStoreName     string            // Kamaji DataStore (default "default")
	FloatingNetworkID string            // Octavia floating network for the API LB
	ExternalNetworkID string            // CAPO external network (clusterNetworking)
	DNSZone           string            // optional: API FQDN = <clusterID>.<DNSZone> (certSAN + external-dns)
	Versions          map[string]string // curated k8s version → Glance image id (the ONLY versions offered)
	Flavors           []string          // optional node-flavor allowlist (empty = every tenant flavor)
}

// Validate rejects a config that cannot possibly provision (fail at create, not mid-flight).
func (c Config) Validate() error {
	switch {
	case c.Kubeconfig == "":
		return fmt.Errorf("kamaji provider: secret.kubeconfig is required")
	case c.ChartRepo == "":
		return fmt.Errorf("kamaji provider: config.argocd.chartRepo is required")
	case c.ChartVersion == "":
		return fmt.Errorf("kamaji provider: config.argocd.chartVersion is required (never latest — plan §9)")
	}
	return nil
}

// ClusterSpec is the desired state of ONE customer cluster — the values-builder input. The
// full generated values live on the Application (spec.source.helm.valuesObject); this struct
// is what the client API accepts and what actions mutate.
type ClusterSpec struct {
	ID          string // generated stc-<8hex>; Application/release/Cluster/TCP name
	DisplayName string // customer-typed, display-only
	ProjectID   string
	Version     string // k8s version, must be a key of Defaults.Versions
	HA          bool   // control-plane replicas 3 (true) or 1
	// OIDC is the customer-supplied issuer config (chart oidc.* block): issuerUrl, clientId,
	// usernameClaim, usernamePrefix, groupsClaim, groupsPrefix, signingAlgs. Empty issuerUrl = disabled.
	OIDC map[string]string
	// AllowedCIDRs restricts API-server LB ingress (Octavia ACL — plan Phase 2a). Empty = open.
	AllowedCIDRs []string
	NodeGroups   []NodeGroup
	// AppCredID/AppCredUserID/AppCredServiceID record the per-cluster keystone application
	// credential minted at create (plan D4) — stamped as annotations on the clouds.yaml secret
	// so the orphan sweep can revoke it after the delete cascade, even when the project doc is
	// already gone. Empty when minting was skipped (fallback admin auth).
	AppCredID        string
	AppCredUserID    string
	AppCredServiceID string
}

// NodeGroup is one CAPI MachineDeployment-backed worker pool.
type NodeGroup struct {
	Name      string            `json:"name"`
	FlavorID  string            `json:"flavorId"`
	ImageID   string            `json:"imageId,omitempty"` // resolved from Defaults.Versions when empty
	Count     int               `json:"count"`
	Autoscale bool              `json:"autoscale"`
	Min       int               `json:"min,omitempty"`
	Max       int               `json:"max,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Taints    []string          `json:"taints,omitempty"` // "key=value:NoSchedule" kubeadm form
}

// Validate rejects an unbuildable spec.
func (s ClusterSpec) Validate(d ClusterDefaults) error {
	switch {
	case s.ID == "":
		return fmt.Errorf("cluster: id is required")
	case s.ProjectID == "":
		return fmt.Errorf("cluster: projectId is required")
	case s.Version == "":
		return fmt.Errorf("cluster: version is required")
	case len(s.NodeGroups) == 0:
		return fmt.Errorf("cluster: at least one node group is required")
	}
	if len(d.Versions) > 0 {
		if _, ok := d.Versions[s.Version]; !ok {
			return fmt.Errorf("cluster: version %q is not offered by this provider", s.Version)
		}
	}
	for _, ng := range s.NodeGroups {
		if ng.Name == "" || ng.FlavorID == "" {
			return fmt.Errorf("cluster: node group name and flavorId are required")
		}
		if len(d.Flavors) > 0 && !slices.Contains(d.Flavors, ng.FlavorID) {
			return fmt.Errorf("cluster: node group %q: flavor %q is not offered by this provider", ng.Name, ng.FlavorID)
		}
		if ng.Autoscale && (ng.Min < 1 || ng.Max < ng.Min) {
			return fmt.Errorf("cluster: node group %q: autoscale needs 1 <= min <= max", ng.Name)
		}
		if !ng.Autoscale && ng.Count < 1 {
			return fmt.Errorf("cluster: node group %q: count must be >= 1", ng.Name)
		}
	}
	return nil
}
