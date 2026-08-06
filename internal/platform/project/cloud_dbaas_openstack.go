package project

// cloud_dbaas_openstack.go — the OpenStack-side half of a Managed Database.
//
// The database's pods run on OPS-owned nodes; the OCCM on the DB cluster authenticates as the
// OPS-owned dbaas keystone project. For it to plant an Octavia LB VIP on the CUSTOMER's subnet,
// the customer network must be visible to that project — a neutron RBAC policy
// (`access_as_shared`), created here with the project's OpenStack service admin creds re-scoped
// to the customer tenant (the network's OWNER creates the share, which default neutron policy
// allows). Nothing here is stored in the cloudResource cache: the share is recorded as
// annotations on the DB cluster's marker secret (dbaas.NetShareSecretName) and rediscovered by
// listing — the same derive-don't-store rule as kamaji's clouds.yaml secret.

import (
	"context"
	"fmt"
	"net"

	"github.com/menlocloud/stratos/internal/cloud/client"
	"github.com/menlocloud/stratos/internal/cloud/dbaas"
)

// dbaasResolveTenantNetwork proves the customer-supplied network/subnet ids belong to the
// project's own tenant (shared/external networks are visible cross-tenant, and a crafted id
// would otherwise let a customer point the dbaas plane at infrastructure networks). Returns the
// OpenStack service id the project's VPC lives on — pinned to the provider's cfg.OSServiceID
// when set, because a share on any OTHER cloud is a silent no-op the OCCM never sees.
func (h *Handler) dbaasResolveTenantNetwork(ctx context.Context, p *Project, cfg dbaas.Config, spec dbaas.DatabaseSpec) (string, error) {
	osCfg, osSvcID, err := h.dbaasTenantCloudConfig(ctx, p, cfg.OSServiceID)
	if err != nil {
		return "", err
	}
	cc, err := client.New(ctx, osCfg)
	if err != nil {
		return "", fmt.Errorf("Managed Databases could not reach your cloud project to verify the network")
	}
	nets, err := cc.ListNetworksFull(ctx)
	if err != nil {
		return "", fmt.Errorf("Managed Databases could not list your networks: %w", err)
	}
	if !hasNeutronID(nets, spec.NetworkID) {
		return "", fmt.Errorf("network %s is not one of this project's networks", spec.NetworkID)
	}
	subnets, err := cc.ListSubnetsFull(ctx)
	if err != nil {
		return "", fmt.Errorf("Managed Databases could not list your subnets: %w", err)
	}
	for _, s := range subnets {
		if strAny(s["id"]) == spec.SubnetID && strAny(s["network_id"]) == spec.NetworkID {
			return osSvcID, nil
		}
	}
	return "", fmt.Errorf("subnet %s does not belong to network %s in this project", spec.SubnetID, spec.NetworkID)
}

// dbaasEnsureNetShare shares the tenant network with the dbaas keystone project. Reports
// whether THIS call created the share (false = it already existed — a sibling database's, which
// a failed create must not unwind).
func (h *Handler) dbaasEnsureNetShare(ctx context.Context, p *Project, cfg dbaas.Config, networkID string) (bool, error) {
	cc, err := h.dbaasTenantClient(ctx, p, cfg.OSServiceID)
	if err != nil {
		return false, err
	}
	existing, err := cc.ListNetworkRBACs(ctx, networkID)
	if err != nil {
		return false, fmt.Errorf("Managed Databases could not inspect the network's sharing policies: %w", err)
	}
	for _, pol := range existing {
		if strAny(pol["target_tenant"]) == cfg.OSProjectID {
			return false, nil
		}
	}
	if err := cc.CreateNetworkRBAC(ctx, networkID, cfg.OSProjectID); err != nil {
		return false, fmt.Errorf("Managed Databases could not share network %s with the database platform: %w", networkID, err)
	}
	return true, nil
}

// dbaasRevokeNetShare removes the network share (failed-create rollback + the sweep's revoker
// body). osRegion pins WHICH region's neutron holds the share ("" falls back to the project's
// binding region — neutron is per-region, and asking the wrong one reports "already gone").
// Neutron 409s while a dbaas-project port (the winding-down amphora) still lives on the
// network — callers treat any error as retry-later.
func (h *Handler) dbaasRevokeNetShare(ctx context.Context, p *Project, osServiceID, osProjectID, osRegion, networkID string) error {
	es, err := h.esSvc.Get(ctx, osServiceID)
	if err != nil {
		return err
	}
	if es == nil {
		return fmt.Errorf("network share on %s: OpenStack service %s no longer exists — revoke manually", networkID, osServiceID)
	}
	extProjID := p.ExternalProjectID(osServiceID)
	if extProjID == "" {
		return fmt.Errorf("network share on %s: project has no tenant on service %s", networkID, osServiceID)
	}
	if osRegion == "" {
		osRegion = p.ServiceRegion(osServiceID)
	}
	cc, err := client.New(ctx, es.ClientConfigForProject(osRegion, extProjID))
	if err != nil {
		return err
	}
	policies, err := cc.ListNetworkRBACs(ctx, networkID)
	if err != nil {
		return err
	}
	for _, pol := range policies {
		if strAny(pol["target_tenant"]) != osProjectID {
			continue
		}
		if id := strAny(pol["id"]); id != "" {
			return cc.DeleteNetworkRBAC(ctx, id)
		}
	}
	return nil // already gone
}

// dbaasNetShareRevoker builds the orphan-finalize revocation resolver (kamajiCredRevoker
// precedent). FAIL-CLOSED — any resolution failure keeps the marker secret (the only revocation
// record) for a later sweep pass.
func (h *Handler) dbaasNetShareRevoker(p *Project) dbaas.RevokeNetShareResolver {
	return func(ctx context.Context, osServiceID, osProjectID, osRegion, networkID string) error {
		if osServiceID == "" {
			var err error
			if _, osServiceID, err = h.dbaasTenantCloudConfig(ctx, p, ""); err != nil {
				return fmt.Errorf("network share on %s: no recording service and no OpenStack binding: %w", networkID, err)
			}
		}
		return h.dbaasRevokeNetShare(ctx, p, osServiceID, osProjectID, osRegion, networkID)
	}
}

// dbaasTenantClient builds a client scoped to the customer's tenant on the project's OpenStack
// binding (pinned to wantSvcID when set).
func (h *Handler) dbaasTenantClient(ctx context.Context, p *Project, wantSvcID string) (*client.Client, error) {
	osCfg, _, err := h.dbaasTenantCloudConfig(ctx, p, wantSvcID)
	if err != nil {
		return nil, err
	}
	cc, err := client.New(ctx, osCfg)
	if err != nil {
		return nil, fmt.Errorf("Managed Databases could not reach your cloud project: %w", err)
	}
	return cc, nil
}

// dbaasTenantCloudConfig resolves the tenant-scoped OpenStack config for the project's VPC —
// the project's OPENSTACK binding, scoped to the customer's own keystone tenant
// (kamajiWorkerCloudConfig precedent, including the appcred fail-closed skip). wantSvcID != ""
// pins the resolution to that service (the dbaas provider's config.database.osServiceId — the
// cloud its OCCM actually lives on); a project without a usable binding THERE is refused rather
// than silently resolved onto some other cloud whose networks the dbaas plane cannot see.
func (h *Handler) dbaasTenantCloudConfig(ctx context.Context, p *Project, wantSvcID string) (client.Config, string, error) {
	for _, svcID := range p.ServiceIDs() {
		if wantSvcID != "" && svcID != wantSvcID {
			continue
		}
		extProjID := p.ExternalProjectID(svcID)
		if extProjID == "" {
			continue
		}
		es, err := h.esSvc.Get(ctx, svcID)
		if err != nil || es == nil || es.Provider() != "openstack" {
			continue
		}
		// App-cred admin auth is keystone-locked to ONE project — it cannot be scoped to the
		// customer tenant (same fail-closed rule as tenantWriteService).
		if es.IsAppCred() {
			continue
		}
		region := p.ServiceRegion(svcID)
		return es.ClientConfigForProject(region, extProjID), svcID, nil
	}
	if wantSvcID != "" {
		return client.Config{}, "", fmt.Errorf("Managed Databases need the project provisioned on the provider's OpenStack service (%s) — the database endpoint lands in your cloud network there", wantSvcID)
	}
	return client.Config{}, "", fmt.Errorf("Managed Databases need the project provisioned on an OpenStack service (the database endpoint lands in your cloud network)")
}

// dbaasValidateCIDRs re-runs the CIDR syntax rule for SET_ALLOWED_CIDRS.
func dbaasValidateCIDRs(cidrs []string) error {
	for _, c := range cidrs {
		if _, _, err := net.ParseCIDR(c); err != nil {
			return fmt.Errorf("allowed CIDR %q: %w", c, err)
		}
	}
	return nil
}
