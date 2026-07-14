package billingresource

import "github.com/menlocloud/stratos/internal/platform/pricing"

// Catalog returns the BillingResourceType catalog the cloud provider bills — the same per-type
// attribute schemas the charge engine maps resources to. Backs the admin PricePlanRule form
// (GET /admin/price-plan[/{id}]/resource-types returns these types): the form picks a
// resourceType then filters/prices on its attributes. Order is stable for the UI.
func Catalog() []*pricing.BillingResourceType {
	return []*pricing.BillingResourceType{
		instanceType(),
		instanceTrafficType(),
		volumeType(),
		floatingIPType(),
		loadBalancerType(),
		kubernetesClusterType(),
	}
}
