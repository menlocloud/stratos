package project

// bootstrap.go holds the cloud-bootstrap slice: when
// a project is enabled it is provisioned onto the platform CLOUD external service — a Keystone
// project (tenant) is created (tagged provisioner:stratos / stratos_project_id:<id>) and recorded as a
// ProjectExternalService on the project. Keystone user/role + quota provisioning (for the customer's
// DIRECT OpenStack access) are deferred — app-mediated resource creates use the admin client
// re-scoped to the tenant (ClientConfigForProject), which needs only the tenant project.

import (
	"context"
	"fmt"

	"github.com/menlocloud/stratos/internal/cloud/cephcred"
	"github.com/menlocloud/stratos/internal/cloud/client"
	"github.com/menlocloud/stratos/internal/platform/externalservice"
)

// EnableAndBootstrap exposes the bootstrap for cross-package orchestration (billing
// activation activates projects; the bootstrap is service-public).
func (h *Handler) EnableAndBootstrap(ctx context.Context, p *Project) error {
	return h.enableAndBootstrap(ctx, p)
}

// enableAndBootstrap provisions the project onto the first non-disabled OpenStack CLOUD service and
// flips it to ENABLED. Idempotent: a project that already has an attached service is left as-is, and
// an existing tenant (matched by the stratos_project_id tag) is reused rather than recreated.
func (h *Handler) enableAndBootstrap(ctx context.Context, p *Project) error {
	if h.esSvc == nil {
		return nil
	}
	// Already provisioned (the project carries its attached service + external tenant) → this is a
	// re-entry; the keystone tenant + admin-role grant were done at first bootstrap. Skip ALL the live
	// keystone calls (FindProjectByTag/role-grant) — they are idempotent setup, not per-entry work, and
	// re-running them on every project-open is both wasteful and a failure surface (an admin-scope
	// config change must not 500 an already-working project).
	if len(p.ServiceIDs()) > 0 {
		return nil
	}
	services, err := h.esSvc.ListByType(ctx, externalservice.TypeCloud)
	if err != nil {
		return err
	}
	var osES *externalservice.ExternalService
	var cephES []*externalservice.ExternalService
	for i := range services {
		if services[i].IsDisabled() {
			continue
		}
		switch {
		case services[i].Provider() == "openstack" && osES == nil:
			osES = &services[i]
		case services[i].IsCephS3():
			cephES = append(cephES, &services[i])
		}
	}
	if osES == nil && len(cephES) == 0 {
		return nil // no cloud provider configured → nothing to provision
	}
	p.Status = StatusEnabled
	// OpenStack tenant bootstrap (Keystone) — the primary compute/network anchor.
	if osES != nil {
		if err := h.BootstrapOnto(ctx, p, osES, ""); err != nil {
			return err
		}
	}
	// Ceph-s3 object-store bootstrap (no Keystone) — provision the project's RGW tenant-user per service.
	for _, es := range cephES {
		if err := h.BootstrapCephOnto(ctx, p, es); err != nil {
			return err
		}
	}
	return nil
}

// BootstrapCephOnto provisions the project onto a ceph-s3 object-store service (decision #2: no Keystone
// tenant — rgwTenant is the sole anchor). It ensures the project's RGW tenant-user + quota via Admin Ops,
// stores the generated S3 keys (encrypted) in the ceph credential store, and appends the ProjectExternal
// service binding {serviceId, provider:"ceph-s3", region, rgwTenant, rgwUid}. Idempotent: a project
// already attached to this service is saved as-is; ensureUser reuses an existing RGW user's keys.
func (h *Handler) BootstrapCephOnto(ctx context.Context, p *Project, es *externalservice.ExternalService) error {
	if p.HasService(es.ID) {
		return h.svc.Save(ctx, p)
	}
	if h.cephCreds == nil {
		return fmt.Errorf("bootstrap ceph: credential store not configured")
	}
	region := es.CephRegion()
	if region == "" {
		region = h.cloudRegion
	}
	uid := es.RGWUIDFor(p.ID) // uidPrefix + projectId — the one derivation
	// Admin-keyed client (no project keys yet) drives the Admin Ops user create + quota.
	adminCC, err := client.NewCephS3(ctx, es.CephConfig(region, "", "", uid))
	if err != nil {
		return fmt.Errorf("bootstrap ceph: build admin client: %w", err)
	}
	access, secret, err := adminCC.EnsureCephUser(ctx, "stratos-"+p.ID)
	if err != nil {
		return fmt.Errorf("bootstrap ceph: ensure rgw user: %w", err)
	}
	if err := h.cephCreds.Save(ctx, &cephcred.Credential{
		ProjectID: p.ID, ServiceID: es.ID, RGWUID: uid,
		AccessKey: access, SecretKey: secret,
	}); err != nil {
		return fmt.Errorf("bootstrap ceph: store credential: %w", err)
	}
	p.Services = append(p.Services, map[string]any{
		"serviceId": es.ID,
		"provider":  "ceph-s3",
		"region":    region,
		"rgwUid":    uid,
	})
	return h.svc.Save(ctx, p)
}

// BootstrapOnto provisions the project onto the GIVEN cloud service — the explicit-service leg of
// the project bootstrap: create-or-reuse the keystone tenant (or ADOPT adoptExternalProjectID when
// supplied), grant the admin service account onto it, append the ProjectExternalService entry and
// save. Does NOT flip status (the bootstrap never does; enableAndBootstrap sets ENABLED itself
// before calling). Idempotent: a project already attached to this service is saved as-is.
func (h *Handler) BootstrapOnto(ctx context.Context, p *Project, es *externalservice.ExternalService, adoptExternalProjectID string) error {
	if p.HasService(es.ID) {
		return h.svc.Save(ctx, p)
	}
	region := h.cloudRegion
	if names := es.RegionNames(); len(names) > 0 {
		region = names[0]
	}
	domainID := "default"
	if cust, ok := es.Config["customer"].(map[string]any); ok {
		if d, _ := cust["domainId"].(string); d != "" {
			domainID = d
		}
	}

	adminCC, err := client.New(ctx, es.ClientConfig(region))
	if err != nil {
		return fmt.Errorf("bootstrap: admin auth: %w", err)
	}
	extProjID := adoptExternalProjectID
	tag := fmt.Sprintf("stratos_project_id:%s", p.ID)
	if extProjID == "" {
		extProjID, err = adminCC.FindProjectByTag(ctx, tag)
		if err != nil {
			return fmt.Errorf("bootstrap: lookup tenant: %w", err)
		}
	}
	if extProjID == "" {
		extProjID, err = adminCC.CreateProject(ctx, client.CreateProjectOpts{
			Name:        p.Name,
			DomainID:    domainID,
			Description: fmt.Sprintf("Stratos project %s", p.ID),
			Enabled:     true,
			Tags:        []string{"provisioner:stratos", tag},
		})
		if err != nil {
			return fmt.Errorf("bootstrap: create tenant: %w", err)
		}
	}

	// Grant the admin service account the admin role ON the new tenant so it can scope to it and
	// create resources there (the app provisions resources as admin-scoped-to-tenant). Idempotent;
	// best-effort (keystone may already grant admin global access).
	if cfgAuth, ok := es.Config["auth"].(map[string]any); ok {
		adminUser, _ := cfgAuth["adminUsername"].(string)
		if adminUser != "" {
			if uid, err := adminCC.FindUserID(ctx, adminUser); err == nil && uid != "" {
				if rid, err := adminCC.FindRoleID(ctx, "admin"); err == nil && rid != "" {
					_ = adminCC.GrantProjectUserRole(ctx, extProjID, uid, rid)
				}
			}
		}
	}

	p.Services = append(p.Services, map[string]any{
		"serviceId":         es.ID,
		"externalProjectId": extProjID,
		"region":            region,
	})
	return h.svc.Save(ctx, p)
}
