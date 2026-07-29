package project

// cloud_kamaji_openstack.go — the OpenStack-side half of a Managed Kubernetes cluster.
//
// The cluster's controllers (CAPO on the management cluster, the management-side CCM) act inside
// the CUSTOMER's own keystone project. Three things have to exist there before the chart can build
// a node, and be cleaned up when the cluster goes away:
//
//	appcred      — a project-scoped, individually revocable credential; it replaces handing the
//	               provider's admin username/password to the cluster's controllers.
//	server group — one per node group, soft anti-affinity, so a pool's machines spread over hosts.
//	keypair      — one support key, injected into every node for break-glass SSH.
//
// Nothing here is stored in the cloudResource cache: the sync overwrites `Data` wholesale, so
// anything kept there would be gone by the time delete needed it. Every object is named after the
// cluster id and rediscovered by name — the same derive-don't-store rule as the clouds.yaml secret
// (kamaji.CloudSecretName).

import (
	"context"
	"fmt"
	"strings"

	"github.com/menlocloud/stratos/internal/cloud/client"
	"github.com/menlocloud/stratos/internal/cloud/kamaji"
)

// kamajiProvisionCloud creates the nova-side prerequisites for a cluster: the break-glass support
// keypair and one server group per node group, whose ids it stamps onto spec.NodeGroups so the
// chart values carry them.
//
// The cluster's CREDENTIAL is not minted here — kamajiCreate mints the per-cluster application
// credential and records it as annotations on the clouds.yaml secret, which is what lets the
// orphan sweep revoke it long after the request is over.
//
// On failure it removes whatever it already created, so a failed create leaves no orphans.
func (h *Handler) kamajiProvisionCloud(ctx context.Context, p *Project, d kamaji.ClusterDefaults, spec *kamaji.ClusterSpec) error {
	osCfg, _, err := h.kamajiWorkerCloudConfig(ctx, p)
	if err != nil {
		return err
	}
	cc, err := client.New(ctx, osCfg)
	if err != nil {
		return fmt.Errorf("Managed Kubernetes could not reach your cloud project: %w", err)
	}
	if err := kamajiEnsureSupportKeypair(ctx, cc, d); err != nil {
		return err
	}
	if err := kamajiEnsureServerGroups(ctx, cc, spec); err != nil {
		kamajiDeleteServerGroups(ctx, cc, spec.ID)
		return err
	}
	return nil
}

// kamajiResolveClusterNetwork settles the cluster's network placement before anything is built.
//
// Dedicated mode (no NetworkID): nothing to do — CAPO creates a per-cluster network, and the
// provider-level default external network applies.
//
// BYO mode (NetworkID+SubnetID set): the ids are customer input, so first PROVE they belong to the
// project's own tenant. ListNetworksFull/ListSubnetsFull are project_id-filtered AND the create
// would otherwise happily wire nodes onto a shared/external network that happens to be visible
// cross-tenant. Then derive the external network the same way EKS-style clouds hide this knob:
// the network a router already egresses through is the only one whose floating IPs are routable
// to the chosen subnet — for CAPO it is presence-only here (it creates no router in BYO mode,
// verified against a live cluster: status.router stays empty), but the management-side CCM uses
// it as the `[LoadBalancer] floating-network-id` pool for every tenant LoadBalancer Service.
//
// Derive order: gateway of a router with an interface on the CHOSEN subnet → any project router's
// gateway → provider default (BuildValues fallback). The admin public-network allowlist gates
// every candidate. All best-effort by design: a cluster on a subnet with no router yet still
// builds (nodes just have no egress until the customer attaches one), so an empty derive is not
// an error unless the provider default is empty too — CAPO then auto-discovers, and with several
// external networks visible that is a hard reconcile error, so fail HERE with a message the
// customer can act on instead.
func (h *Handler) kamajiResolveClusterNetwork(ctx context.Context, p *Project, osSvcID string, d kamaji.ClusterDefaults, spec *kamaji.ClusterSpec) error {
	if spec.NetworkID == "" {
		return nil
	}
	cc, ok := h.tryTenantClient(ctx, p, osSvcID)
	if !ok {
		return fmt.Errorf("Managed Kubernetes could not reach your cloud project to verify the network")
	}
	nets, err := cc.ListNetworksFull(ctx)
	if err != nil {
		return fmt.Errorf("Managed Kubernetes could not list your networks: %w", err)
	}
	if !hasNeutronID(nets, spec.NetworkID) {
		return fmt.Errorf("network %s is not one of this project's networks", spec.NetworkID)
	}
	subnets, err := cc.ListSubnetsFull(ctx)
	if err != nil {
		return fmt.Errorf("Managed Kubernetes could not list your subnets: %w", err)
	}
	subnetOK := false
	for _, s := range subnets {
		if strAny(s["id"]) == spec.SubnetID && strAny(s["network_id"]) == spec.NetworkID {
			subnetOK = true
			break
		}
	}
	if !subnetOK {
		return fmt.Errorf("subnet %s does not belong to network %s in this project", spec.SubnetID, spec.NetworkID)
	}

	if ext := kamajiSubnetEgressNetwork(ctx, cc, spec.SubnetID); ext != "" && publicNetworkAllowed(p, ext) {
		spec.ExternalNetworkID = ext
	} else if ext := h.autoPickExternalNetwork(ctx, p, osSvcID, true); ext != "" {
		spec.ExternalNetworkID = ext
	}
	if spec.ExternalNetworkID == "" && d.ExternalNetworkID == "" {
		return fmt.Errorf("no external network could be determined for subnet %s — attach the network to a router with an external gateway, or ask your operator to set a provider default", spec.SubnetID)
	}
	return nil
}

// kamajiSubnetEgressNetwork finds the external network a subnet already egresses through: the
// gateway of a project router holding an interface (a network:router_interface* port with a fixed
// IP) on that subnet. "" when no such router exists — best effort, callers fall back.
func kamajiSubnetEgressNetwork(ctx context.Context, cc *client.Client, subnetID string) string {
	routers, err := cc.ListRoutersFull(ctx)
	if err != nil {
		return ""
	}
	for _, r := range routers {
		gw, _ := r["external_gateway_info"].(map[string]any)
		gwNet := strAny(gw["network_id"])
		if gwNet == "" {
			continue
		}
		ports, err := cc.ListPortsFull(ctx, strAny(r["id"]))
		if err != nil {
			continue
		}
		for _, port := range ports {
			if !strings.HasPrefix(strAny(port["device_owner"]), "network:router_interface") {
				continue
			}
			fixed, _ := port["fixed_ips"].([]map[string]any)
			for _, ip := range fixed {
				if strAny(ip["subnet_id"]) == subnetID {
					return gwNet
				}
			}
		}
	}
	return ""
}

// hasNeutronID reports whether a raw Neutron object list contains the id.
func hasNeutronID(objs []map[string]any, id string) bool {
	for _, o := range objs {
		if strAny(o["id"]) == id {
			return true
		}
	}
	return false
}

// kamajiEnsureSupportKeypair makes sure the break-glass keypair exists. Nova keypairs belong to the
// authenticating USER, not the project, so this is a one-time object for the whole provider — but
// creating it is idempotent and cheap, and doing it here means a fresh provider works without a
// manual step. With only a name configured (no public key) the name is passed through to the chart
// as-is and the operator owns creating it.
func kamajiEnsureSupportKeypair(ctx context.Context, cc *client.Client, d kamaji.ClusterDefaults) error {
	if d.SupportKeypairName == "" || d.SupportKeypairPublicKey == "" {
		return nil
	}
	_, err := cc.CreateKeypair(ctx, client.CreateKeypairOpts{
		Name:      d.SupportKeypairName,
		PublicKey: d.SupportKeypairPublicKey,
	})
	if err != nil && !client.IsConflict(err) {
		return fmt.Errorf("Managed Kubernetes could not create the support SSH key: %w", err)
	}
	return nil
}

// kamajiEnsureServerGroups creates one soft-anti-affinity group per node group and stamps its id on
// the spec. Soft, not hard: hard anti-affinity fails the build outright once a pool has more
// machines than the cloud has hosts, which would make autoscaling a trap.
func kamajiEnsureServerGroups(ctx context.Context, cc *client.Client, spec *kamaji.ClusterSpec) error {
	existing, err := kamajiClusterServerGroups(ctx, cc, spec.ID)
	if err != nil {
		return err
	}
	for i := range spec.NodeGroups {
		name := kamaji.ServerGroupName(spec.ID, spec.NodeGroups[i].Name)
		if id, ok := existing[name]; ok {
			spec.NodeGroups[i].ServerGroupID = id
			continue
		}
		g, err := cc.CreateServerGroup(ctx, client.CreateServerGroupOpts{Name: name, Policy: "soft-anti-affinity"})
		if err != nil {
			return fmt.Errorf("Managed Kubernetes could not create a server group for node group %q: %w", spec.NodeGroups[i].Name, err)
		}
		id, _ := g["id"].(string)
		if id == "" {
			return fmt.Errorf("Managed Kubernetes: server group for node group %q came back without an id", spec.NodeGroups[i].Name)
		}
		spec.NodeGroups[i].ServerGroupID = id
	}
	return nil
}

// kamajiSyncServerGroups reconciles the groups to a new node-group set (SET_NODE_GROUPS): create
// the missing ones, drop the ones whose node group is gone.
func (h *Handler) kamajiSyncServerGroups(ctx context.Context, p *Project, clusterID string, groups []kamaji.NodeGroup) error {
	osCfg, _, err := h.kamajiWorkerCloudConfig(ctx, p)
	if err != nil {
		return err
	}
	cc, err := client.New(ctx, osCfg)
	if err != nil {
		return fmt.Errorf("Managed Kubernetes could not reach your cloud project: %w", err)
	}
	spec := kamaji.ClusterSpec{ID: clusterID, NodeGroups: groups}
	if err := kamajiEnsureServerGroups(ctx, cc, &spec); err != nil {
		return err
	}
	copy(groups, spec.NodeGroups)

	wanted := map[string]bool{}
	for _, ng := range groups {
		wanted[kamaji.ServerGroupName(clusterID, ng.Name)] = true
	}
	existing, err := kamajiClusterServerGroups(ctx, cc, clusterID)
	if err != nil {
		return err
	}
	for name, id := range existing {
		if !wanted[name] {
			// Best effort: a group that still has members refuses to delete, and the next
			// reconcile picks it up once CAPI has torn the machines down.
			_ = cc.DeleteServerGroup(ctx, id)
		}
	}
	return nil
}

// kamajiClusterServerGroups returns the cluster's server groups as name → id.
func kamajiClusterServerGroups(ctx context.Context, cc *client.Client, clusterID string) (map[string]string, error) {
	gs, err := cc.ListServerGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("Managed Kubernetes could not list server groups: %w", err)
	}
	out := map[string]string{}
	prefix := kamaji.ServerGroupPrefix(clusterID)
	for _, g := range gs {
		name, _ := g["name"].(string)
		id, _ := g["id"].(string)
		if id != "" && strings.HasPrefix(name, prefix) {
			out[name] = id
		}
	}
	return out, nil
}

// kamajiReleaseServerGroups drops a cluster's server groups. Called after the Application delete —
// nova refuses to delete a group that still has members, so CAPI has to have released the machines
// first; anything left behind is picked up by the next attempt.
func (h *Handler) kamajiReleaseServerGroups(ctx context.Context, p *Project, clusterID string) error {
	osCfg, _, err := h.kamajiWorkerCloudConfig(ctx, p)
	if err != nil {
		return err
	}
	cc, err := client.New(ctx, osCfg)
	if err != nil {
		return fmt.Errorf("Managed Kubernetes could not reach your cloud project: %w", err)
	}
	kamajiDeleteServerGroups(ctx, cc, clusterID)
	return nil
}

// kamajiDeleteServerGroups removes every server group named after the cluster. Best effort by
// design — it is the rollback path of a half-finished create AND the cleanup path of a delete, and
// in both cases giving up on the first error would strand the rest.
func kamajiDeleteServerGroups(ctx context.Context, cc *client.Client, clusterID string) {
	groups, err := kamajiClusterServerGroups(ctx, cc, clusterID)
	if err != nil {
		return
	}
	for _, id := range groups {
		_ = cc.DeleteServerGroup(ctx, id)
	}
}
