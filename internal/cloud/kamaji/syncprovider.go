package kamaji

import (
	"context"
	"strings"

	"github.com/menlocloud/stratos/internal/cloud"
)

// ClusterSyncProvider lists a project's kamaji clusters → KUBERNETES_CLUSTER cloud resources.
// It satisfies providers.Provider + ProjectScoped implicitly (no import — keeps the dependency
// arrow pointing kamaji→cloud only). One Application = one cached resource; the TCP + the CAPI
// MachineDeployments enrich it with live endpoint/health/replica counts.
type ClusterSyncProvider struct {
	svc       *Service
	region    string
	projectID string
}

// SyncProvider builds the read-sync provider for one project (syncjob leg).
func (s *Service) SyncProvider(region, projectID string) *ClusterSyncProvider {
	return &ClusterSyncProvider{svc: s, region: region, projectID: projectID}
}

func (p *ClusterSyncProvider) Type() string      { return cloud.TypeKubernetesCluster }
func (p *ClusterSyncProvider) ProjectID() string { return p.projectID }

func (p *ClusterSyncProvider) List(ctx context.Context) ([]cloud.CloudResource, error) {
	s := p.svc
	// Ownership marker in the selector: pre-stratos clusters on the same management cluster
	// (unlabelled) never enter the cache, so they never surface in the UI or billing.
	apps, err := s.api.ListApplications(ctx, s.cfg.ArgoNamespace,
		LabelProject+"="+p.projectID+","+LabelManagedBy+"="+ManagedByValue+
			","+LabelService+"="+s.serviceID)
	if err != nil {
		return nil, err
	}
	ns := NamespaceFor(p.projectID)
	out := make([]cloud.CloudResource, 0, len(apps))
	for _, app := range apps {
		id := digStr(app, "metadata", "name")
		if id == "" {
			continue
		}
		// A terminating Application (delete issued, resources-finalizer still cascading) must
		// not re-enter the cache — the row was archived at delete time, and resurrecting it
		// would flash the cluster back into the UI and billing until the cascade finishes.
		if dig(app, "metadata", "deletionTimestamp") != nil {
			continue
		}
		// Best-effort enrichment: a half-provisioned cluster still syncs (status stays PROGRESSING).
		tcp, err := s.findTCP(ctx, ns, id)
		if err != nil {
			return nil, err
		}
		mds, err := s.api.ListMachineDeployments(ctx, ns, "cluster.x-k8s.io/cluster-name="+id)
		if err != nil {
			return nil, err
		}
		// The CAPO infrastructure object, for the "what did the platform create for me" panel.
		// BEST EFFORT on purpose: this enrichment must never be able to fail a project's whole
		// cluster sync — a management cluster without the CRD, or without RBAC for it, still has
		// clusters worth listing. An error means "not known", which renders as an absent panel.
		osc, oscErr := s.api.GetOpenStackCluster(ctx, ns, id)
		if oscErr != nil {
			osc = nil
		}
		out = append(out, cloud.CloudResource{
			Type:       cloud.TypeKubernetesCluster,
			ExternalID: id,
			Region:     p.region,
			ProjectID:  p.projectID,
			Data:       clusterData(app, tcp, mds, osc),
		})
	}
	return out, nil
}

// clusterData maps Application (+ optional TCP/MachineDeployments) → the cached `data` payload.
// bson-round-trip-stable by construction: strings, bools and JSON numbers only, timestamps as
// the RFC3339 strings Kubernetes already serializes (never time.Time — the dev-era churn lesson).
func clusterData(app, tcp map[string]any, mds []map[string]any, osc map[string]any) map[string]any {
	values, _ := dig(app, "spec", "source", "helm", "valuesObject").(map[string]any)

	status := "PENDING"
	if h := digStr(app, "status", "health", "status"); h != "" {
		// Argo health: Healthy / Progressing / Degraded / Suspended / Missing / Unknown.
		status = map[string]string{
			"Healthy":     "READY",
			"Progressing": "PROGRESSING",
			"Degraded":    "DEGRADED",
			"Suspended":   "SUSPENDED",
			"Missing":     "PROGRESSING",
			"Unknown":     "UNKNOWN",
		}[h]
		if status == "" {
			status = "UNKNOWN"
		}
	}

	c := map[string]any{
		"id":            digStr(app, "metadata", "name"),
		"name":          digStr(app, "metadata", "annotations", AnnotationDisplayName),
		"version":       digStr(values, "kubernetesVersion"),
		"chart_version": digStr(app, "spec", "source", "targetRevision"),
		"status":        status,
		"sync_status":   digStr(app, "status", "sync", "status"),
		"created_at":    digStr(app, "metadata", "creationTimestamp"),
	}
	if replicas, ok := dig(values, "kamajiControlPlane", "replicas").(float64); ok {
		c["cp_replicas"] = replicas
	} else if replicas, ok := dig(values, "kamajiControlPlane", "replicas").(int); ok {
		c["cp_replicas"] = replicas
	}
	// Network placement, read back off the values so the UI can show whether the cluster runs on
	// a dedicated CAPO-created network or on one of the project's own (BYO). Absent keys mean
	// dedicated. All immutable after create (CAPO webhook), so no edit path feeds off these.
	if netID := digStr(values, "clusterNetworking", "internalNetwork", "networkFilter", "id"); netID != "" {
		c["network_id"] = netID
		c["subnet_id"] = digStr(values, "clusterNetworking", "internalNetwork", "subnetFilter", "id")
	}
	if ext := digStr(values, "clusterNetworking", "externalNetworkId"); ext != "" {
		c["external_network_id"] = ext
	}
	// Node resolvers. Absent = the cluster inherits the subnet's DHCP servers, which is what the
	// UI must say rather than showing an empty list as if a choice had been made.
	if dns, ok := dig(values, "clusterNetworking", "dnsServers").([]any); ok && len(dns) > 0 {
		c["dns_servers"] = dns
	}
	// Customer add-on toggles, read back for the UI (absent block = chart defaults). Filtered to
	// the curated menu: the stratos-owned `openstack` storage leg (and any operator hand-edit)
	// must not surface as a customer toggle — it would round-trip into SET_ADDONS and be
	// rejected as an unknown add-on.
	if adds, ok := dig(values, "addons").(map[string]any); ok {
		out := map[string]any{}
		for name, raw := range adds {
			if _, curated := ClusterAddons[name]; !curated {
				continue
			}
			if m, ok := raw.(map[string]any); ok {
				if enabled, ok := m["enabled"].(bool); ok {
					out[name] = enabled
				}
			}
		}
		if len(out) > 0 {
			c["addons"] = out
		}
	}
	if issuer := digStr(values, "oidc", "issuerUrl"); issuer != "" {
		c["oidc_issuer"] = issuer
		// Full block (snake_case like the rest of the payload) — the client OIDC edit form
		// prefills from it; keys mirror OIDCValues.
		oidc := map[string]any{"issuer_url": issuer}
		for src, dst := range map[string]string{
			"clientId": "client_id", "usernameClaim": "username_claim", "usernamePrefix": "username_prefix",
			"groupsClaim": "groups_claim", "groupsPrefix": "groups_prefix",
		} {
			if v := digStr(values, "oidc", src); v != "" {
				oidc[dst] = v
			}
		}
		c["oidc"] = oidc
	}

	// Control-plane endpoint from the TCP status (host:port the kubeconfig points at).
	if tcp != nil {
		if ep := digStr(tcp, "status", "controlPlaneEndpoint"); ep != "" {
			c["endpoint"] = ep
		}
		if v := digStr(tcp, "status", "kubernetesResources", "version", "status"); v != "" {
			c["cp_status"] = v
		}
	}

	// Desired node groups from values, live replica counts from the MachineDeployments.
	live := map[string]map[string]any{}
	for _, md := range mds {
		name := digStr(md, "metadata", "name")
		entry := map[string]any{}
		if r, ok := dig(md, "status", "replicas").(float64); ok {
			entry["replicas"] = r
		}
		if r, ok := dig(md, "status", "readyReplicas").(float64); ok {
			entry["ready_replicas"] = r
		}
		if phase := digStr(md, "status", "phase"); phase != "" {
			entry["phase"] = phase
		}
		live[name] = entry
	}
	groups := []any{}
	for _, g := range NodeGroupsFromValues(values) {
		name, _ := g["name"].(string)
		// MachineDeployment names are chart-derived (typically <cluster>-<group>); match by suffix.
		for mdName, entry := range live {
			if name != "" && (mdName == name || strings.HasSuffix(mdName, "-"+name)) {
				for k, v := range entry {
					g[k] = v
				}
			}
		}
		groups = append(groups, g)
	}
	c["node_groups"] = groups

	// ── what the platform created for this cluster ────────────────────────────
	// Read off CAPO's own status rather than derived from names: these are the objects the
	// delete cascade takes with it, and a customer deciding whether an OpenStack object is safe
	// to touch needs the authoritative list, not a guess.
	if osc != nil {
		managed := []any{}
		for _, sg := range []struct{ field, role string }{
			{"controlPlaneSecurityGroup", "control-plane"},
			{"workerSecurityGroup", "worker"},
			{"bastionSecurityGroup", "bastion"},
		} {
			g, _ := dig(osc, "status", sg.field).(map[string]any)
			if id := digStr(g, "id"); id != "" {
				managed = append(managed, map[string]any{"id": id, "name": digStr(g, "name"), "role": sg.role})
			}
		}
		if len(managed) > 0 {
			c["managed_security_groups"] = managed
		}
	}
	// The break-glass keypair injected into every node — platform-owned, and the customer should
	// know it is there.
	if kp := digStr(values, "machineSSHKeyName"); kp != "" {
		c["support_keypair"] = kp
	}
	return map[string]any{"cluster": c}
}
