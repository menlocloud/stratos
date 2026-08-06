// Package kamaji drives Managed Kubernetes clusters on a Kamaji management cluster: one
// ArgoCD Application per customer cluster (chart `openstack-kamaji-cluster` from our OCI
// registry, pinned targetRevision + full generated values), applied through the management
// cluster's own API (the Application CRD — no ArgoCD API/auth involved). ArgoCD renders,
// syncs and health-checks; stratos reads status back off the Application + the Kamaji
// TenantControlPlane + CAPI MachineDeployments.
package kamaji

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
)

// maxNodeGroupSecurityGroups caps how many extra groups one pool may name. Neutron's own limit is
// far higher; this is a sanity bound so a malformed client cannot stuff a values document that
// every machine in the pool then carries.
const maxNodeGroupSecurityGroups = 10

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

// ControlPlaneName is the KamajiControlPlane the chart renders for a cluster — and therefore the
// TenantControlPlane, since the Kamaji CAPI provider names the TCP after it. The chart builds it as
// `<release>-kamaji-cp` (templates/control-plane/kamaji-control-plane.yaml), and Kamaji in turn
// derives `<tcp>-admin-kubeconfig` from that. Getting this wrong is invisible until the first
// kubeconfig fetch, so it lives here next to the other name derivations.
func ControlPlaneName(clusterID string) string { return clusterID + "-kamaji-cp" }

// AdminKubeconfigSecretName is the Kamaji-issued admin kubeconfig secret for a cluster.
func AdminKubeconfigSecretName(clusterID string) string {
	return ControlPlaneName(clusterID) + "-admin-kubeconfig"
}

// ServerGroupName is the nova server group backing one node group. Derived, never stored: the
// delete path finds a cluster's groups by listing and matching this prefix, so cleanup survives a
// lost Application or a half-written cache row (CloudSecretName precedent).
func ServerGroupName(clusterID, nodeGroup string) string { return clusterID + "-" + nodeGroup }

// ServerGroupPrefix matches every server group belonging to a cluster.
func ServerGroupPrefix(clusterID string) string { return clusterID + "-" }

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

// owns reports whether obj is an Application THIS provider created.
//
// `managedBy` alone is the fleet-wide "stratos made this" marker, and it is NOT an ownership
// test: the Managed-Database and Managed-Kubernetes providers can be pointed at the same ArgoCD
// and the same namespace (they are, in production), and both stamp that exact label on every
// Application they create. A guard built on it therefore lets one product's delete and patch
// funnels reach the OTHER product's workloads — an Application delete cascades through the
// argocd resources finalizer, so that is a live cluster or database destroyed, not a bad read.
//
// The per-service label has been stamped by BuildApplication since day one and survives every
// mutation (Patch*App re-applies metadata.labels verbatim), so this needs no backfill.
//
// NOT for namespace objects: the project namespace is shared between the two providers and its
// stratos.io/service label is last-writer-wins, so `owns` there is a coin flip. Namespaces keep
// the `managedBy` check.
func (s *Service) owns(obj map[string]any) bool {
	return managedBy(obj) && digStr(obj, "metadata", "labels", LabelService) == s.serviceID
}

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
	ChartRepo     string // OCI helm repo, e.g. "ghcr.io/menlocloud/stratos-charts" (no oci:// prefix in Argo repoURL)
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
	Versions          map[string]string // curated k8s version → DEFAULT Glance image id (the ONLY versions offered)
	// ImageVariants are curated alternative node images per version, keyed variant → version →
	// image id (e.g. "nvidia" → {"1.35.4": "<id>"} for the GPU build of the same release). A node
	// group picks a variant BY NAME; upgrades re-resolve the group onto the target version's image
	// of the SAME variant, so a GPU pool never silently rolls back onto the plain image.
	ImageVariants map[string]map[string]string
	Flavors       []string // optional node-flavor allowlist (empty = every tenant flavor)
	// RootVolumeGiB is the default worker root-disk size. Node images are larger than most flavors'
	// ephemeral disk, so booting from an explicitly sized Cinder volume is the norm, not an option —
	// without it nova rejects the machine with FlavorDiskSmallerThanImage.
	RootVolumeGiB int
	// SupportKeypairName is a nova keypair injected into EVERY node so support can SSH in when a
	// customer asks for hands-on troubleshooting. Empty = no key injected. Nova keypairs belong to
	// the authenticating user, so one keypair covers the whole provider.
	SupportKeypairName string
	// SupportKeypairPublicKey, when set, lets stratos create that keypair if it is missing.
	// Without it the operator owns creating the key and stratos only injects the name.
	SupportKeypairPublicKey string
	// AllowedCIDRs is the provider-wide default API-server ingress allowlist; a cluster's own
	// AllowedCIDRs overrides it. Empty on both = open.
	AllowedCIDRs []string
	// StorageVolumeType is the Cinder volume type behind every cluster's default StorageClass
	// (csi-cinder). Empty = the cloud's default volume type.
	StorageVolumeType string
	// RegistryMirrors is the containerd pull-through map every node of every cluster gets:
	// upstream registry host → mirror endpoints, rendered by the chart into
	// /etc/containerd/certs.d/<host>/hosts.toml. It therefore covers EVERY image the node
	// pulls — CNI, CSI, add-ons and the customer's own workloads — not just chart images, and
	// containerd falls back to the upstream registry when a mirror misses.
	//
	// This is the lever against public-registry rate limits: unmirrored clusters bootstrap at
	// the mercy of docker.io/quay.io throttling, which is what turns a create into an hour of
	// ImagePullBackOff. Empty = no mirrors at all (chart 0.8.0 dropped the inherited Azimuth
	// default rather than route an installer's pulls through a third party), so a provider that
	// wants a cache has to name one — and a registry left off the map is pulled from directly.
	//
	// Mirror endpoints are REGISTRY API ROOTS, not repo paths: a Harbor proxy-cache project is
	// https://<harbor>/v2/<project>. Provider-level, applied at create — an existing cluster
	// keeps the mirrors stored on its Application (full-values contract, plan §9).
	RegistryMirrors map[string][]string
	// NodeSelector/Tolerations place everything this chart runs ON THE MANAGEMENT CLUSTER: the
	// hosted control-plane pods, the per-cluster OpenStack CCM, the Cinder CSI controller and the
	// cluster-autoscaler. Worker VMs are unaffected — they are Nova servers, not pods.
	//
	// The point is a dedicated management node pool: label + taint it, set both here, and the
	// pool scales on tenant-cluster demand alone. Both matter — a selector alone still lets other
	// workloads onto the pool, a toleration alone does not keep control planes on it. Empty =
	// schedule anywhere, which is the chart's default.
	//
	// Provider-level and applied at create, like every other value: an existing cluster keeps the
	// placement stored on its Application until something rewrites its values (plan §9).
	NodeSelector map[string]string
	// Tolerations are opaque Kubernetes toleration objects, passed to the chart unchanged.
	Tolerations []map[string]any
}

// ClusterAddons is the curated add-on menu a customer can toggle per cluster (name →
// default-enabled). Keys are the addons subchart's own top-level blocks, passed through as
// `addons.<key>.enabled`; everything else in the addons stack — the CNI, the CSI/credential
// push, the mirror-registry pins — stays operator territory and is never client-writable.
var ClusterAddons = map[string]bool{
	"certManager":         false, // cert-manager (Let's Encrypt & friends)
	"ingress":             false, // NGINX ingress controller
	"metricsServer":       true,  // kubectl top / HPA metrics
	"monitoring":          false, // kube-prometheus-stack + Loki (heavy)
	"nodeProblemDetector": true,  // node faults (kernel hangs, bad disks) as Events; chart default is on
	"nvidiaGPUOperator":   false, // driver/toolkit for NVIDIA GPU pools
	"reloader":            false, // roll Deployments when watched ConfigMaps/Secrets change
}

// ClusterFQDN is the cluster's public API hostname (<clusterID>.<dnsZone>) — the name
// external-dns publishes for the API-server LoadBalancer and the apiserver cert carries as a
// SAN (BuildValues). "" when the provider has no DNS zone.
func (d ClusterDefaults) ClusterFQDN(clusterID string) string {
	if d.DNSZone == "" {
		return ""
	}
	return clusterID + "." + d.DNSZone
}

// ImageFor resolves the node image for (version, variant): the variant's own matrix entry, or
// the default version matrix when no variant is asked for. "" = no image offered for that
// combination — the caller decides whether that is an error (create) or a keep-current (edit).
func (d ClusterDefaults) ImageFor(version, variant string) string {
	if variant != "" {
		return d.ImageVariants[variant][version]
	}
	return d.Versions[version]
}

// VariantsForVersion lists the variant names that carry an image for the version, sorted — the
// client DTO's per-version picker feed.
func (d ClusterDefaults) VariantsForVersion(version string) []string {
	names := make([]string, 0, len(d.ImageVariants))
	for name, m := range d.ImageVariants {
		if m[version] != "" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
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
	// Addons are the customer's picks off the curated ClusterAddons menu (unknown names are
	// rejected). nil/empty = the chart's own defaults, which match the menu's defaults.
	Addons     map[string]bool
	NodeGroups []NodeGroup
	// NetworkID/SubnetID attach the cluster to a network the customer already owns (EKS-style
	// "pick your VPC subnet") instead of the per-cluster network CAPO otherwise creates. Both or
	// neither: CAPO accepts a lone filter, but a network with the wrong subnet picked — or a
	// subnet resolved off a different network — is exactly the class of half-configured cluster
	// we refuse to build. Customer-supplied, but the create path MUST verify both belong to the
	// project's tenant before building values (kamajiValidateClusterNetwork): CAPO runs with a
	// project-scoped credential, yet shared/external networks are visible cross-tenant, and a
	// crafted id would otherwise let a customer hang their nodes onto infrastructure networks.
	NetworkID string
	SubnetID  string
	// DNSServers are the resolvers every node in the cluster is pointed at (a systemd-resolved
	// drop-in written at bootstrap). Empty = the nodes inherit whatever DNS the customer's subnet
	// hands out over DHCP, which is what every cluster did before this existed — the platform has
	// never had a DNS default of its own.
	//
	// CoreDNS in the workload cluster forwards to the node resolver, so this is also the cluster's
	// fallback DNS.
	//
	// CLUSTER-WIDE, and changing it replaces EVERY worker: the value lands in every pool's
	// KubeadmConfigTemplate, whose checksum names the template. That is inherent to a setting
	// applied at bootstrap, not a limitation of the plumbing.
	DNSServers []string
	// ExternalNetworkID overrides ClusterDefaults.ExternalNetworkID for THIS cluster. Stratos-owned,
	// derived at create time (the external network the chosen subnet's router egresses through, via
	// the admin allowlist) — never accepted from the client. CAPO treats spec.externalNetwork as
	// immutable after create (pkg/webhooks/openstackcluster_webhook.go ValidateUpdate), so this is
	// pinned into the Application values once and never rewritten.
	ExternalNetworkID string
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
	Name     string `json:"name"`
	FlavorID string `json:"flavorId"`
	ImageID  string `json:"imageId,omitempty"` // explicit override; else resolved from the version matrix
	// ImageVariant picks a curated image variant (Defaults.ImageVariants) for this pool — e.g.
	// "nvidia" for GPU nodes. Sticky across upgrades: the UPGRADE action re-resolves the group
	// onto the target version's image of the same variant. Empty = the default image.
	ImageVariant string `json:"imageVariant,omitempty"`
	// AvailabilityZone pins the pool's machines to one Nova AZ (the chart's failureDomain).
	// Empty = the cloud scheduler places them (single-AZ clouds today).
	AvailabilityZone string `json:"availabilityZone,omitempty"`
	// PublicIP gives every machine in the pool a floating IP from the cluster's external
	// network (an OpenStackFloatingIPPool per group; released with the machine). The FIPs land
	// in the customer's tenant, so they sync and bill like any other floating IP.
	PublicIP      bool              `json:"publicIp,omitempty"`
	Count         int               `json:"count"`
	Autoscale     bool              `json:"autoscale"`
	Min           int               `json:"min,omitempty"`
	Max           int               `json:"max,omitempty"`
	RootVolumeGiB int               `json:"rootVolumeGiB,omitempty"` // 0 = ClusterDefaults.RootVolumeGiB
	Labels        map[string]string `json:"labels,omitempty"`
	Taints        []string          `json:"taints,omitempty"` // "key=value:Effect" / "key:Effect"
	// SecurityGroupIDs are EXTRA OpenStack security groups attached to this pool's machines.
	// Customer-supplied, so — like NetworkID/SubnetID and unlike ServerGroupID — every id MUST be
	// proven to belong to the project's own tenant before it reaches the values
	// (kamajiValidateSecurityGroups); security groups are visible cross-tenant to an
	// admin-scoped token, so an unchecked id would let a customer pin their nodes behind
	// somebody else's rules.
	//
	// ADDITIVE: CAPO appends the cluster's MANAGED group to whatever is listed here
	// (v0.14.4 ConstructPorts), so a pool that names none still gets the platform's group and a
	// pool that names some cannot drop it. Empty = the managed group alone, today's behaviour.
	//
	// Per POOL, not per cluster: the ids ride in the machine-template spec, which is what the
	// template checksum covers — changing them replaces THIS pool's machines and leaves the
	// others running.
	SecurityGroupIDs []string `json:"securityGroupIds,omitempty"`
	// ServerGroupID is the nova server group the pool's machines join (soft anti-affinity, one per
	// node group). Stratos-owned: minted at create/update, never accepted from the client — hence
	// json:"-", so a crafted request cannot pin machines into somebody else's server group.
	ServerGroupID string `json:"-"`
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
	// Bring-your-own VPC, always. Leaving both empty used to mean "let CAPO build a dedicated
	// per-cluster network", which reads like a convenience and is not one: those nodes land on a
	// network the customer does not own, cannot route to from the rest of their estate, and
	// cannot see in their own network list. It also skipped the only ownership check on this
	// path — BYO ids are PROVEN to belong to the project's tenant before use
	// (cloud_kamaji_openstack.go), and a dedicated network has nothing to prove, so it bypassed
	// it entirely. Requiring the pick makes that proof unconditional.
	case s.NetworkID == "" || s.SubnetID == "":
		return fmt.Errorf("cluster: networkId and subnetId are required (the cluster's nodes land in your VPC)")
	case len(s.NodeGroups) == 0:
		return fmt.Errorf("cluster: at least one node group is required")
	}
	if len(d.Versions) > 0 {
		if _, ok := d.Versions[s.Version]; !ok {
			return fmt.Errorf("cluster: version %q is not offered by this provider", s.Version)
		}
	}
	for name := range s.Addons {
		if _, ok := ClusterAddons[name]; !ok {
			return fmt.Errorf("cluster: unknown add-on %q", name)
		}
	}
	if err := ValidateDNSServers(s.DNSServers); err != nil {
		return err
	}
	return s.ValidateNodeGroups(d)
}

// ValidateNodeGroups checks ONLY the node-group half of a spec, so an action that edits pools
// (SET_NODE_GROUPS) can reuse the rules without the create-time ones.
//
// It is split out because it was not: the edit path shape-checked its groups by building a
// throwaway ClusterSpec and calling Validate, which worked until Validate grew a cluster-level
// rule the throwaway could not satisfy. Requiring a BYO network did exactly that, and every
// node-group edit — resize, add or remove a pool, relabel, retaint — started failing with
// "networkId and subnetId are required" on clusters that already had both. Keeping the two sets
// of rules in separate methods is what stops the next cluster-level rule from doing it again.
func (s ClusterSpec) ValidateNodeGroups(d ClusterDefaults) error {
	if len(s.NodeGroups) == 0 {
		return fmt.Errorf("cluster: at least one node group is required")
	}
	seen := map[string]bool{}
	for _, ng := range s.NodeGroups {
		if ng.Name == "" || ng.FlavorID == "" {
			return fmt.Errorf("cluster: node group name and flavorId are required")
		}
		if len(d.Flavors) > 0 && !slices.Contains(d.Flavors, ng.FlavorID) {
			return fmt.Errorf("cluster: node group %q: flavor %q is not offered by this provider", ng.Name, ng.FlavorID)
		}
		// The chart derives MachineDeployment / template names from the group name and rejects
		// anything else outright (machine-deployment.yaml `fail`), so catch it here with a message
		// the customer can act on.
		if !nodeGroupName.MatchString(ng.Name) {
			return fmt.Errorf("cluster: node group %q: name must be 3+ lowercase letters, digits or dashes, starting with a letter", ng.Name)
		}
		if seen[ng.Name] {
			return fmt.Errorf("cluster: node group %q: duplicate name", ng.Name)
		}
		seen[ng.Name] = true
		// Variant must resolve for the cluster's version (skipped when d is empty — the
		// SET_NODE_GROUPS shape check — where the values patch resolves images with fallback).
		if ng.ImageVariant != "" && len(d.Versions) > 0 && d.ImageFor(s.Version, ng.ImageVariant) == "" {
			return fmt.Errorf("cluster: node group %q: image variant %q is not offered for version %s", ng.Name, ng.ImageVariant, s.Version)
		}
		if ng.Autoscale && (ng.Min < 1 || ng.Max < ng.Min) {
			return fmt.Errorf("cluster: node group %q: autoscale needs 1 <= min <= max", ng.Name)
		}
		if !ng.Autoscale && ng.Count < 1 {
			return fmt.Errorf("cluster: node group %q: count must be >= 1", ng.Name)
		}
		// Nodes always boot from a Cinder volume (the images are bigger than most flavors'
		// ephemeral disk), so a size has to come from somewhere.
		if ng.RootVolumeGiB == 0 && d.RootVolumeGiB == 0 {
			return fmt.Errorf("cluster: node group %q: rootVolumeGiB is required (no provider default configured)", ng.Name)
		}
		// Shape only — OWNERSHIP is proven against the live tenant by
		// kamajiValidateSecurityGroups, which is the check that actually matters. An empty or
		// duplicated id here renders `- id: ""` into the machine template, which CAPO's webhook
		// rejects: the pool then never provisions and nothing in the cluster's status says why.
		seenSG := map[string]bool{}
		for _, id := range ng.SecurityGroupIDs {
			if strings.TrimSpace(id) == "" {
				return fmt.Errorf("cluster: node group %q: securityGroupIds contains an empty id", ng.Name)
			}
			if seenSG[id] {
				return fmt.Errorf("cluster: node group %q: securityGroupIds contains %q twice", ng.Name, id)
			}
			seenSG[id] = true
		}
		if len(ng.SecurityGroupIDs) > maxNodeGroupSecurityGroups {
			return fmt.Errorf("cluster: node group %q: at most %d security groups per node group", ng.Name, maxNodeGroupSecurityGroups)
		}
		for _, t := range ng.Taints {
			obj := taintToObject(t)
			if obj == nil {
				return fmt.Errorf("cluster: node group %q: taint %q must be key[=value]:Effect", ng.Name, t)
			}
			if effect, _ := obj["effect"].(string); !ValidTaintEffects[effect] {
				return fmt.Errorf("cluster: node group %q: taint %q: effect must be NoSchedule, PreferNoSchedule or NoExecute", ng.Name, t)
			}
		}
	}
	return nil
}

// maxClusterDNSServers caps the resolver list. systemd-resolved itself accepts more; this is a
// sanity bound on a value that is written into a file on every node.
const maxClusterDNSServers = 4

// ValidateDNSServers checks the resolver list a customer supplied. Shared by create and by the
// edit action, so the two can never drift into different rules.
//
// Addresses only, never hostnames: the file is read by the resolver itself, so a name in there is
// a chicken-and-egg that presents as a node which boots and cannot resolve anything.
func ValidateDNSServers(servers []string) error {
	if len(servers) > maxClusterDNSServers {
		return fmt.Errorf("cluster: at most %d DNS servers", maxClusterDNSServers)
	}
	seen := map[string]bool{}
	for _, srv := range servers {
		srv = strings.TrimSpace(srv)
		if srv == "" {
			return fmt.Errorf("cluster: dnsServers contains an empty entry")
		}
		if net.ParseIP(srv) == nil {
			return fmt.Errorf("cluster: dnsServers: %q is not an IP address (a hostname cannot be used to find a resolver)", srv)
		}
		if seen[srv] {
			return fmt.Errorf("cluster: dnsServers contains %q twice", srv)
		}
		seen[srv] = true
	}
	return nil
}

// DNSServersSupported reports whether a chart pin renders clusterNetworking.dnsServers (0.10.0+).
// Older pins ignore the key, which would leave a customer believing their resolvers are in use.
func DNSServersSupported(chartVersion string) bool {
	maj, min, _, err := ParseVersion(chartVersion)
	if err != nil {
		return false
	}
	return maj > 0 || min >= 10
}

// SecurityGroupsSupported reports whether a chart pin renders nodeGroups[].securityGroupIds.
//
// The key arrived in 0.9.0. On an older pin the values still carry it and the chart still
// renders — it just ignores it, so a customer who picked groups would get a cluster with none of
// them and no error anywhere. That is a security expectation quietly not met, which is worse than
// a refusal, so the create path refuses instead.
//
// Unparseable or empty pin = NOT supported: a version we cannot read is not one we can promise
// anything about.
func SecurityGroupsSupported(chartVersion string) bool {
	maj, min, _, err := ParseVersion(chartVersion)
	if err != nil {
		return false
	}
	return maj > 0 || min >= 9
}

// nodeGroupName mirrors the chart's own guard (templates/node-group/machine-deployment.yaml).
var nodeGroupName = regexp.MustCompile(`^[a-z][a-z0-9\-]+[a-z0-9]$`)
