//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/menlocloud/stratos/internal/cloud"
	"github.com/menlocloud/stratos/internal/cloud/billingresource"
	"github.com/menlocloud/stratos/internal/cloud/metrics"
	"github.com/menlocloud/stratos/internal/platform/pricing"
	"github.com/menlocloud/stratos/pkg/billingapi"
)

// TestM6Pipeline is the capstone: usage (GnocchiMetrics) → BillingResource
// (ServerProvider) → dispatch (GetBillingResources) → rate+assemble (ChargeBillingResources)
// → a charged, persisted bill. Proves the assembled cloud→rating pipeline end-to-end
// against real Postgres (the only mocked edge is the cloud itself — gnocchi data is seeded).
func TestM6Pipeline(t *testing.T) {
	ctx := context.Background()
	db := freshPG(t)
	cloudRepo := cloud.NewRepo(db)
	gnocchiRepo := metrics.NewRepo(db)
	pricingRepo := pricing.NewRepo(db)
	eng := pricing.NewEngine(pricing.SystemClock())

	now := time.Now().UTC()
	y, mo, _ := now.Date()
	cycleStart := time.Date(y, mo, 1, 0, 0, 0, 0, time.UTC)
	cycleEnd := cycleStart.AddDate(0, 1, 0)

	// 1. a SERVER cloud resource for the (project, service). Its createdAt must PRECEDE the charge
	// timestamp (cycleStart+12h below): the pipeline stamps BillingResource.CreatedAt and the hour-rule
	// accrual = elapsed hours since createdAt — a createdAt AFTER the charge instant yields a
	// negative diff (a physical impossibility outside test fixtures).
	seededAt := cycleStart.Add(11 * time.Hour) // exactly 1 hour before the charge → 1 time unit
	srv := &cloud.CloudResource{
		ExternalID: "nova-1", ServiceID: "svc-p", ProjectID: "proj-p", Type: cloud.TypeServer,
		Data:      map[string]any{"flavorName": "m5.large", "server": map[string]any{"name": "vm", "flavor": map[string]any{"ram": int64(2048), "vcpus": int64(2), "disk": int64(20)}}},
		CreatedAt: &seededAt, UpdatedAt: &seededAt,
	}
	saved, err := cloudRepo.Insert(ctx, srv)
	if err != nil {
		t.Fatalf("seed server: %v", err)
	}

	// 2. its month usage: 100 MB total traffic.
	if _, err := gnocchiRepo.Save(ctx, &metrics.GnocchiMetrics{
		ResourceID: saved.ID, ResourceType: cloud.TypeServer,
		BillingCycle: &metrics.BillBillingCycle{StartDate: &cycleStart, EndDate: &cycleEnd},
		Details:      &metrics.GnocchiMetricsDetails{TotalTrafficMb: decimal.RequireFromString("100")},
	}); err != nil {
		t.Fatalf("seed gnocchi: %v", err)
	}

	// 3. dispatch → BillingResources (instance + instance_traffic).
	registry := map[string]billingresource.Provider{cloud.TypeServer: billingresource.NewServerProvider(gnocchiRepo)}
	brs, err := billingresource.GetBillingResources(ctx, cloudRepo, registry, "proj-p", "svc-p", billingapi.BillingContext{})
	if err != nil {
		t.Fatalf("getBillingResources: %v", err)
	}

	// 4. a price rule: charge total_traffic_mb (graduated from 0) at 0.01 / MB.
	rule := pricing.PricePlanRule{
		TimeUnit: pricing.TimeUnitHour, ResourceType: "instance_traffic",
		Prices: []pricing.PricePlanRulePrice{{
			AttributeName: "total_traffic_mb",
			Tiers:         []pricing.PriceTier{{From: dptr("0"), To: nil, Value: dptr("0.01")}},
		}},
	}
	rc := pricing.RatingContext{TimeUnit: pricing.TimeUnitHour, CycleTimestamp: cycleStart.Add(12 * time.Hour)}

	bill, err := pricing.ChargeBillingResources(ctx, pricingRepo, eng, rc, pricing.BillingContext{},
		// The charge seam: resolution hands over billingapi resources, rating maps them in.
		"bp-pipeline", []pricing.PricePlanRule{rule}, pricing.FromAPIResources(brs), cycleStart, cycleEnd, cycleStart.Add(time.Hour), "USD", nil)
	if err != nil {
		t.Fatalf("charge: %v", err)
	}

	// 5. the bill carries a positive charge for the traffic resource.
	if bill == nil || bill.ID == "" {
		t.Fatalf("expected a persisted bill, got %v", bill)
	}
	var trafficNet decimal.Decimal
	for _, it := range bill.Items {
		if it.ResourceID == "instance_traffic-"+saved.ID {
			trafficNet = it.NetAmount
		}
	}
	if !trafficNet.GreaterThan(decimal.Zero) {
		t.Fatalf("expected a positive traffic charge on the bill, items=%+v", bill.Items)
	}
	t.Logf("pipeline: 100 MB @ 0.01/MB → traffic net = %s", trafficNet)
}
