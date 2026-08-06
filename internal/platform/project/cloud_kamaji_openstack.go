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
	"log/slog"
	"strings"

	"github.com/menlocloud/stratos/internal/cloud"
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

// kamajiCheckNodeGroupDisks refuses a node group whose root volume is too small for the image its
// machines boot. Every worker boots FROM A CINDER VOLUME — kamaji.NodeGroupValues always writes
// machineRootVolume, because the node images are bigger than most flavors' ephemeral disk — and
// Cinder will not write an image into a volume smaller than the image's min_disk or its own virtual
// size. Nothing checked this: an undersized number was accepted by the API, stamped into the
// Application values and only surfaced as a MachineDeployment that stayed at zero ready replicas,
// with the real reason (a Cinder 400 CAPO logs on the management cluster) invisible to the customer.
//
// Runs BEFORE anything is minted, on create and on every SET_NODE_GROUPS.
func (h *Handler) kamajiCheckNodeGroupDisks(ctx context.Context, p *Project, d kamaji.ClusterDefaults, version string, groups []kamaji.NodeGroup, current map[string]string) error {
	osCfg, _, err := h.kamajiWorkerCloudConfig(ctx, p)
	if err != nil {
		return err
	}
	cc, err := client.New(ctx, osCfg)
	if err != nil {
		return fmt.Errorf("Managed Kubernetes could not reach your cloud project: %w", err)
	}
	return validateNodeGroupDisks(d, version, groups, current, func(id string) (map[string]any, error) {
		return cc.GetImage(ctx, id)
	})
}

// validateNodeGroupDisks is the rule itself, with the cloud behind a getter so it is testable
// without one. getImage is called at most once per distinct image.
//
// Best effort on the LOOKUP, strict on the ANSWER: an image we cannot read leaves its groups
// unvalidated (status quo — the create still goes through) rather than turning a glance hiccup
// into a blocked cluster. An image we CAN read is enforced.
func validateNodeGroupDisks(
	d kamaji.ClusterDefaults,
	version string,
	groups []kamaji.NodeGroup,
	current map[string]string,
	getImage func(string) (map[string]any, error),
) error {
	minimums := map[string]int{}
	for _, ng := range groups {
		size := ng.RootVolumeGiB
		if size == 0 {
			size = d.RootVolumeGiB
		}
		imageID := kamajiNodeGroupImage(d, version, ng, current)
		if size == 0 || imageID == "" {
			// No size or no resolvable image — ClusterSpec.ValidateNodeGroups and
			// applyNodeGroupImages own those two refusals; nothing to weigh here.
			continue
		}
		minimum, known := minimums[imageID]
		if !known {
			image, err := getImage(imageID)
			if err != nil || image == nil {
				slog.Warn("kamaji: node image unreadable — root-disk size left unchecked",
					"image", imageID, "nodeGroup", ng.Name, "err", err)
				minimums[imageID] = 0
				continue
			}
			minimum = imageMinimumRootGiB(image)
			minimums[imageID] = minimum
		}
		if minimum > 0 && size < minimum {
			return fmt.Errorf(
				"node group %q: a root disk of %d GiB is too small for its node image — use at least %d GiB",
				ng.Name, size, minimum)
		}
	}
	return nil
}

// kamajiNodeGroupImage resolves the glance image a node group's machines boot, in the SAME order
// the values builder does: an explicit override, then the provider's curated version→variant image
// matrix, then — on an edit — whatever the group already runs (kamaji.NodeGroupValues +
// applyNodeGroupImages). "" when nothing resolves.
func kamajiNodeGroupImage(d kamaji.ClusterDefaults, version string, ng kamaji.NodeGroup, current map[string]string) string {
	if ng.ImageID != "" {
		return ng.ImageID
	}
	if img := d.ImageFor(version, ng.ImageVariant); img != "" {
		return img
	}
	return current[ng.Name]
}

// clusterField reads one field off the cluster's cached sync payload. "" when the cache has not
// caught up yet — callers must treat that as "unknown", never as a permissive default.
func clusterField(cr *cloud.CloudResource, key string) any {
	cl, _ := cr.Data["cluster"].(map[string]any)
	return cl[key]
}

// kamajiCachedImages reads a cluster's current k8s version and per-group boot image off the sync
// cache — the fallback an EDIT resolves an unchanged group's image through, mirroring
// applyNodeGroupImages.
func kamajiCachedImages(cr *cloud.CloudResource) (string, map[string]string) {
	cl, _ := cr.Data["cluster"].(map[string]any)
	images := map[string]string{}
	raw, _ := cl["node_groups"].([]any)
	for _, item := range raw {
		g, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if name := strAny(g["name"]); name != "" {
			images[name] = strAny(g["image_id"])
		}
	}
	return strAny(cl["version"]), images
}

// kamajiCheckSecurityGroups is the whole gate for customer-named security groups: the chart pin
// must be able to render them, and every id must belong to this project's tenant. Shared by
// create and by SET_NODE_GROUPS so the edit path can never be the looser one.
func (h *Handler) kamajiCheckSecurityGroups(ctx context.Context, p *Project, osSvcID, chartVersion string, groups []kamaji.NodeGroup) error {
	named := false
	for _, ng := range groups {
		if len(ng.SecurityGroupIDs) > 0 {
			named = true
			break
		}
	}
	if !named {
		return nil
	}
	if !kamaji.SecurityGroupsSupported(chartVersion) {
		return fmt.Errorf("this cluster's platform version (%s) cannot attach your own security groups — apply the platform update first, or create the node group without them", chartVersion)
	}
	return h.kamajiValidateSecurityGroups(ctx, p, osSvcID, groups)
}

// kamajiValidateSecurityGroups proves every customer-named security group belongs to the
// project's OWN tenant before it can reach the chart values.
//
// Same hazard, same answer as the BYO network ids in kamajiResolveClusterNetwork: neutron shows
// security groups belonging to other tenants to a token that can see them, so an id taken on
// trust would let a customer pin their nodes behind somebody else's rules — rules they cannot
// read, cannot change, and which someone else can widen at any time. ListSecurityGroups is
// project_id-filtered on the wire AND the result is re-checked here, because a filter that
// silently stops being applied is exactly how this class of bug comes back.
//
// FAIL CLOSED. An unreachable cloud refuses the request rather than letting the ids through
// unproven — the whole point is that nothing unverified reaches the values.
//
// Runs on create AND on every node-group edit: a validated-once-at-create check is a hole,
// because SET_NODE_GROUPS accepts a fresh list of ids every time.
func (h *Handler) kamajiValidateSecurityGroups(ctx context.Context, p *Project, osSvcID string, groups []kamaji.NodeGroup) error {
	wanted := map[string]bool{}
	for _, ng := range groups {
		for _, id := range ng.SecurityGroupIDs {
			wanted[id] = true
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	cc, ok := h.tryTenantClient(ctx, p, osSvcID)
	if !ok {
		return fmt.Errorf("Managed Kubernetes could not reach your cloud project to verify the security groups")
	}
	sgs, err := cc.ListSecurityGroups(ctx)
	if err != nil {
		return fmt.Errorf("Managed Kubernetes could not list your security groups: %w", err)
	}
	tenant := p.ExternalProjectID(osSvcID)
	owned := map[string]bool{}
	for _, sg := range sgs {
		id := strAny(sg["id"])
		if id == "" {
			continue
		}
		// Belt and braces over the wire-level project_id filter. Resolve the owner field FIRST,
		// then compare exactly once — an id whose owner neither key reports stays unowned, so a
		// response shape we did not expect refuses the group instead of waving it through.
		if tenant != "" {
			owner := strAny(sg["project_id"])
			if owner == "" {
				owner = strAny(sg["tenant_id"])
			}
			if owner != tenant {
				continue
			}
		}
		owned[id] = true
	}
	for _, ng := range groups {
		for _, id := range ng.SecurityGroupIDs {
			if !owned[id] {
				return fmt.Errorf("node group %q: security group %s is not one of this project's security groups", ng.Name, id)
			}
		}
	}
	return nil
}

// kamajiResolveClusterNetwork settles the cluster's network placement before anything is built.
//
// Every cluster is bring-your-own VPC (ClusterSpec.Validate enforces it), so this always runs:
// the ids are customer input, so first PROVE they belong to the
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
	// Unreachable via create — Validate rejects an empty network before this is called — but it
	// stays a hard error rather than an early return. This function is the ONLY place a
	// customer-supplied network id is proven to belong to their tenant; a future caller that
	// skips validation must fail here, not silently wire nodes onto whatever id it was handed.
	if spec.NetworkID == "" || spec.SubnetID == "" {
		return fmt.Errorf("Managed Kubernetes needs a network and subnet from your project")
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
