package client

import (
	"context"
	"net/url"
	"strings"
)

// rbac.go — Neutron RBAC policies (network sharing), the dbaas leg's tenant-side prerequisite:
// an Octavia LB created by the OCCM running as the OPS-owned dbaas keystone project can only
// plant its VIP on a customer network the dbaas project can see, so the create path shares the
// network via `access_as_shared` and the orphan sweep revokes it. Raw REST like neutron_list.go
// (the rbac-policies collection has no project_id list quirk to worry about here: policies are
// listed/deleted with the same tenant-scoped admin client that created them).

// CreateNetworkRBAC shares a network with targetProjectID (`access_as_shared`). Idempotent: a
// 409 (already shared with that project) is success.
func (c *Client) CreateNetworkRBAC(ctx context.Context, networkID, targetProjectID string) error {
	base, err := c.EndpointURL("network")
	if err != nil {
		return err
	}
	body := map[string]any{"rbac_policy": map[string]any{
		"object_type":   "network",
		"object_id":     networkID,
		"action":        "access_as_shared",
		"target_tenant": targetProjectID,
	}}
	err = c.Do(ctx, "POST", strings.TrimRight(base, "/")+"/v2.0/rbac-policies", body, nil, 201)
	if IsConflict(err) {
		return nil
	}
	return err
}

// ListNetworkRBACs lists the network's `access_as_shared` RBAC policies as raw maps
// (id / target_tenant / object_id).
func (c *Client) ListNetworkRBACs(ctx context.Context, networkID string) ([]map[string]any, error) {
	base, err := c.EndpointURL("network")
	if err != nil {
		return nil, err
	}
	u := strings.TrimRight(base, "/") + "/v2.0/rbac-policies?object_type=network&action=access_as_shared&object_id=" + url.QueryEscape(networkID)
	var resp map[string][]map[string]any
	if err := c.Do(ctx, "GET", u, nil, &resp); err != nil {
		return nil, err
	}
	return resp["rbac_policies"], nil
}

// DeleteNetworkRBAC removes one RBAC policy. Neutron 409s while a port of the target project
// still lives on the network (the winding-down Octavia amphora) — callers treat that as
// retry-later, never as force-delete-the-port.
func (c *Client) DeleteNetworkRBAC(ctx context.Context, rbacID string) error {
	base, err := c.EndpointURL("network")
	if err != nil {
		return err
	}
	return c.Do(ctx, "DELETE", strings.TrimRight(base, "/")+"/v2.0/rbac-policies/"+url.PathEscape(rbacID), nil, nil, 204)
}
