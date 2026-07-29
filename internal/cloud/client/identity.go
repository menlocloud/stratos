package client

// identity.go = the Keystone v3 admin identity ops used by project bootstrap (project create +
// role/user provisioning). Requires the client to be built from an
// ADMIN-scoped Config (the externalService admin creds). Resource provisioning then uses an
// admin-scoped-to-tenant client (ClientConfigForProject).

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/applicationcredentials"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/projects"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/roles"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/users"
)

// CreateProjectOpts mirrors the Keystone project-create fields used by bootstrap.
type CreateProjectOpts struct {
	Name        string
	DomainID    string
	Description string
	Enabled     bool
	Tags        []string
}

// CreateProject creates a Keystone project (tenant) and returns its id
// (domainId from config.customer.domainId, tags provisioner:stratos + stratos_project_id:<id>).
func (c *Client) CreateProject(ctx context.Context, o CreateProjectOpts) (string, error) {
	ic, err := openstack.NewIdentityV3(c.provider, c.endpointOpts())
	if err != nil {
		return "", err
	}
	enabled := o.Enabled
	p, err := projects.Create(ctx, ic, projects.CreateOpts{
		Name:        o.Name,
		DomainID:    o.DomainID,
		Description: o.Description,
		Enabled:     &enabled,
		Tags:        o.Tags,
	}).Extract()
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

// DeleteProject deletes a Keystone project (tenant) by id — the project-teardown final step, after
// its cloud resources are removed. Errors (incl. an already-gone tenant) surface to the best-effort
// teardown caller, which logs and continues.
func (c *Client) DeleteProject(ctx context.Context, id string) error {
	ic, err := openstack.NewIdentityV3(c.provider, c.endpointOpts())
	if err != nil {
		return err
	}
	return projects.Delete(ctx, ic, id).ExtractErr()
}

// FindProjectByTag returns the id of the first Keystone project carrying the given tag, or "" when
// none — the bootstrap idempotency guard (re-enable must not create a duplicate tenant).
func (c *Client) FindProjectByTag(ctx context.Context, tag string) (string, error) {
	ic, err := openstack.NewIdentityV3(c.provider, c.endpointOpts())
	if err != nil {
		return "", err
	}
	pages, err := projects.List(ic, projects.ListOpts{Tags: tag}).AllPages(ctx)
	if err != nil {
		return "", err
	}
	ps, err := projects.ExtractProjects(pages)
	if err != nil {
		return "", err
	}
	if len(ps) > 0 {
		return ps[0].ID, nil
	}
	return "", nil
}

// KeystoneProject is the keystone project shape returned to the admin unassociated-os-projects
// read (subset of the identity v3 project serialization — id/name/domainId/description/
// enabled are what the admin UI consumes).
type KeystoneProject struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DomainID    string `json:"domainId,omitempty"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
}

// ListAllProjects lists EVERY keystone project (admin-scope read).
func (c *Client) ListAllProjects(ctx context.Context) ([]KeystoneProject, error) {
	ic, err := openstack.NewIdentityV3(c.provider, c.endpointOpts())
	if err != nil {
		return nil, err
	}
	pages, err := projects.List(ic, projects.ListOpts{}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	ps, err := projects.ExtractProjects(pages)
	if err != nil {
		return nil, err
	}
	out := make([]KeystoneProject, 0, len(ps))
	for _, p := range ps {
		out = append(out, KeystoneProject{
			ID: p.ID, Name: p.Name, DomainID: p.DomainID,
			Description: p.Description, Enabled: p.Enabled,
		})
	}
	return out, nil
}

// FindUserID returns the id of the first Keystone user with the given name (admin-scope read).
func (c *Client) FindUserID(ctx context.Context, name string) (string, error) {
	ic, err := openstack.NewIdentityV3(c.provider, c.endpointOpts())
	if err != nil {
		return "", err
	}
	pages, err := users.List(ic, users.ListOpts{Name: name}).AllPages(ctx)
	if err != nil {
		return "", err
	}
	us, err := users.ExtractUsers(pages)
	if err != nil {
		return "", err
	}
	if len(us) > 0 {
		return us[0].ID, nil
	}
	return "", nil
}

// FindRoleID returns the id of the Keystone role with the given name.
func (c *Client) FindRoleID(ctx context.Context, name string) (string, error) {
	ic, err := openstack.NewIdentityV3(c.provider, c.endpointOpts())
	if err != nil {
		return "", err
	}
	pages, err := roles.List(ic, roles.ListOpts{Name: name}).AllPages(ctx)
	if err != nil {
		return "", err
	}
	rs, err := roles.ExtractRoles(pages)
	if err != nil {
		return "", err
	}
	if len(rs) > 0 {
		return rs[0].ID, nil
	}
	return "", nil
}

// GrantProjectUserRole grants a role to a user on a project (admin identity op) — idempotent on the
// keystone side. Assigns one role on the project.
func (c *Client) GrantProjectUserRole(ctx context.Context, projectID, userID, roleID string) error {
	ic, err := openstack.NewIdentityV3(c.provider, c.endpointOpts())
	if err != nil {
		return err
	}
	return roles.Assign(ctx, ic, roleID, roles.AssignOpts{UserID: userID, ProjectID: projectID}).ExtractErr()
}

// currentUserID extracts the authenticated user's id off the session token — the owner every
// application credential minted through this client belongs to. Only password-auth sessions
// carry it (keystone refuses appcred-minting from an appcred-authenticated token anyway).
func (c *Client) currentUserID() (string, error) {
	res, ok := c.provider.GetAuthResult().(tokens.CreateResult)
	if !ok {
		return "", fmt.Errorf("cloud: auth session carries no keystone token (appcred-authenticated?)")
	}
	u, err := res.ExtractUser()
	if err != nil {
		return "", err
	}
	return u.ID, nil
}

// CreateAppCredential mints a keystone application credential owned by the authenticated user
// and scoped to the session token's project — call it on a client built with
// ClientConfigForProject so the credential is locked to the CUSTOMER's tenant (kamaji plan D4:
// the per-cluster credential CAPO/OCCM hold is bounded to that one project). Returns the owning
// user id (needed for revocation) plus the credential id/secret — the secret is shown by
// keystone exactly once, here.
func (c *Client) CreateAppCredential(ctx context.Context, name, description string) (userID, id, secret string, err error) {
	userID, err = c.currentUserID()
	if err != nil {
		return "", "", "", err
	}
	ic, err := openstack.NewIdentityV3(c.provider, c.endpointOpts())
	if err != nil {
		return "", "", "", err
	}
	ac, err := applicationcredentials.Create(ctx, ic, userID, applicationcredentials.CreateOpts{
		Name:        name,
		Description: description,
	}).Extract()
	if err != nil {
		return "", "", "", fmt.Errorf("cloud: create application credential: %w", err)
	}
	return userID, ac.ID, ac.Secret, nil
}

// DeleteAppCredential revokes an application credential (absent = success — revocation is
// idempotent for the finalize-orphans retry loop).
func (c *Client) DeleteAppCredential(ctx context.Context, userID, id string) error {
	ic, err := openstack.NewIdentityV3(c.provider, c.endpointOpts())
	if err != nil {
		return err
	}
	err = applicationcredentials.Delete(ctx, ic, userID, id).ExtractErr()
	if IsNotFound(err) {
		return nil
	}
	return err
}
