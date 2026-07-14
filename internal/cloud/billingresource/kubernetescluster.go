package billingresource

import (
	"context"

	"github.com/menlocloud/stratos/internal/cloud"
	"github.com/menlocloud/stratos/internal/platform/pricing"
)

// KubernetesClusterProvider maps a KUBERNETES_CLUSTER → a "kubernetes_cluster" BillingResource:
// the CONTROL-PLANE fee only. Worker VMs live in the customer's own keystone tenant and are
// billed by the existing instance/volume/LB providers (plan D4) — pricing them here would
// double-charge.
type KubernetesClusterProvider struct{}

func NewKubernetesClusterProvider() *KubernetesClusterProvider { return &KubernetesClusterProvider{} }

func (p *KubernetesClusterProvider) Type() string { return cloud.TypeKubernetesCluster }

func (p *KubernetesClusterProvider) GetBillingInformation(_ context.Context, _ pricing.BillingContext, cr *cloud.CloudResource) ([]*pricing.BillingResource, error) {
	values := map[string]any{}
	notEligible := true
	if c, ok := mapAt(cr.Data, "cluster"); ok {
		values["display_name"] = c["name"]
		values["version"] = c["version"]
		values["status"] = c["status"]
		values["cp_replicas"] = c["cp_replicas"]
		if groups, ok := c["node_groups"].([]any); ok {
			values["node_groups"] = len(groups)
		}
		// Charge only once the control plane is actually up (the endpoint exists): provisioning
		// time is free, and a cluster degraded AFTER coming up keeps billing (it still holds
		// control-plane capacity) — same stance as the instance provider's status handling.
		endpoint := str(c["endpoint"])
		values["endpoint"] = endpoint
		notEligible = endpoint == ""
	}
	return []*pricing.BillingResource{{
		ResourceID:            cr.ID,
		ProjectID:             cr.ProjectID,
		ResourceType:          "kubernetes_cluster",
		Values:                values,
		BillingResourceType:   kubernetesClusterType(),
		NotEligibleForBilling: notEligible,
	}}, nil
}

func kubernetesClusterType() *pricing.BillingResourceType {
	s := func(n string) pricing.ResourceAttribute { return pricing.ResourceAttribute{Name: n, Type: "string"} }
	n := func(nm string) pricing.ResourceAttribute { return pricing.ResourceAttribute{Name: nm, Type: "number"} }
	return &pricing.BillingResourceType{ResourceType: "kubernetes_cluster", Attributes: []pricing.ResourceAttribute{
		s("display_name"), s("version"), s("status"), s("endpoint"), n("cp_replicas"), n("node_groups"),
	}}
}
