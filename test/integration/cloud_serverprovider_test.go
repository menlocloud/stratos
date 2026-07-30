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
	"github.com/menlocloud/stratos/pkg/billingapi"
)

// TestServerProviderBillingResources verifies the SERVER → BillingResources mapping: the
// "instance" resource carries flavor attributes, and "instance_traffic" carries the month's
// GnocchiMetrics usage (the gnocchi→rating content link).
func TestServerProviderBillingResources(t *testing.T) {
	ctx := context.Background()
	db := freshPG(t)
	gnocchiRepo := metrics.NewRepo(db)

	now := time.Now().UTC()
	y, mo, _ := now.Date()
	cycleStart := time.Date(y, mo, 1, 0, 0, 0, 0, time.UTC)
	cycleEnd := cycleStart.AddDate(0, 1, 0)

	// seed the month's usage for the server.
	if _, err := gnocchiRepo.Save(ctx, &metrics.GnocchiMetrics{
		ResourceID: "srv-X", ResourceType: cloud.TypeServer,
		BillingCycle: &metrics.BillBillingCycle{StartDate: &cycleStart, EndDate: &cycleEnd},
		Details: &metrics.GnocchiMetricsDetails{
			IncomingPublicTrafficMb: decimal.RequireFromString("10"),
			OutgoingPublicTrafficMb: decimal.RequireFromString("5"),
			TotalPublicTrafficMb:    decimal.RequireFromString("15"),
			TotalTrafficMb:          decimal.RequireFromString("42"),
		},
	}); err != nil {
		t.Fatalf("seed gnocchi: %v", err)
	}

	server := &cloud.CloudResource{
		ID: "srv-X", ProjectID: "proj-X", ExternalID: "nova-x", Type: cloud.TypeServer,
		Data: map[string]any{
			"flavorName": "m5.large",
			"server": map[string]any{
				"name":   "vm-1",
				"flavor": map[string]any{"ram": int64(2048), "vcpus": int64(2), "disk": int64(20)},
			},
		},
	}

	brs, err := billingresource.NewServerProvider(gnocchiRepo).GetBillingInformation(ctx, billingapi.BillingContext{}, server)
	if err != nil {
		t.Fatalf("getBillingInformation: %v", err)
	}
	byType := map[string]*billingapi.BillingResource{}
	for _, br := range brs {
		byType[br.ResourceType] = br
	}

	inst := byType["instance"]
	if inst == nil {
		t.Fatal("missing instance BillingResource")
	}
	if d, _ := inst.Values["ram_gb"].(decimal.Decimal); !d.Equal(decimal.NewFromInt(2)) {
		t.Errorf("ram_gb = %v, want 2", inst.Values["ram_gb"])
	}
	if inst.Values["vcpus"] != int64(2) {
		t.Errorf("vcpus = %v, want 2", inst.Values["vcpus"])
	}

	tr := byType["instance_traffic"]
	if tr == nil {
		t.Fatal("missing instance_traffic BillingResource")
	}
	if tr.ResourceID != "instance_traffic-srv-X" {
		t.Errorf("traffic resourceId = %q", tr.ResourceID)
	}
	if d, _ := tr.Values["total_traffic_mb"].(decimal.Decimal); !d.Equal(decimal.RequireFromString("42")) {
		t.Errorf("total_traffic_mb = %v, want 42", tr.Values["total_traffic_mb"])
	}
	if tr.Values["display_name"] != "vm-1" {
		t.Errorf("display_name = %v, want vm-1", tr.Values["display_name"])
	}
}
