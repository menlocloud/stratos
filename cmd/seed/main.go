// Command seed populates a fresh PostgreSQL database with the default
// configuration required for the Stratos API to function. Idempotent:
// safe to re-run, overwrites seed data in-place.
//
// Usage:
//
//	go run ./cmd/seed
//	go run ./cmd/seed <DATABASE_URL>
//
// Reads STRATOS_DB_URL env var when no argument is given.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/menlocloud/stratos/internal/pgdoc"
)

func run() error {
	dsn := os.Getenv("STRATOS_DB_URL")
	if dsn == "" && len(os.Args) >= 2 {
		dsn = os.Args[1]
	}
	if dsn == "" {
		fmt.Println("Usage: go run ./cmd/seed [DATABASE_URL]")
		fmt.Println("Or set STRATOS_DB_URL environment variable.")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := pgdoc.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer db.Close(ctx)

	s := slog.Default()
	s.Info("connected to database")

	err = db.WithTx(ctx, func(tc context.Context) error {
		// Ensure all tables exist.
		for _, name := range []string{
			"platformConfiguration",
			"billingConfiguration",
			"pricePlan",
			"pricePlanRule",
			"imageCategory",
		} {
			if err := db.C(name).Ensure(tc); err != nil {
				return fmt.Errorf("ensure %s: %w", name, err)
			}
		}

		// Seed data.
		seeded, err := seed(tc, db)
		if err != nil {
			return err
		}
		s.Info("seeded rows", "count", seeded)
		return nil
	})
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}

	s.Info("seed complete")
	return nil
}

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func seed(ctx context.Context, db *pgdoc.DB) (int, error) {
	var n int

	seedOne := func(col, id string, doc pgdoc.M) error {
		if err := db.C(col).Upsert(ctx, id, doc); err != nil {
			return fmt.Errorf("seed %s/%s: %w", col, id, err)
		}
		n++
		return nil
	}

	// ---- platformConfiguration ------------------------------------------------
	if err := seedOne("platformConfiguration", "seed", pgdoc.M{
		"name":                 "Stratos",
		"language":             "en",
		"defaultConfiguration": true,
		"branding": pgdoc.M{
			"name":       "Stratos",
			"logo":       "https://stratos.example/logo.png",
			"color":      "#0066cc",
			"faviconUrl": "https://stratos.example/favicon.ico",
		},
		"dateConfiguration": pgdoc.M{
			"dateFormat": "DD/MM/YYYY",
		},
		"regions": []any{},
	}); err != nil {
		return 0, err
	}

	// ---- billingConfiguration -------------------------------------------------
	if err := seedOne("billingConfiguration", "seed", pgdoc.M{
		"defaultConfiguration":  true,
		"baseCurrency":          "USD",
		"promotionCodesEnabled": true,
		"name":                  "Stratos",
		"company": pgdoc.M{
			"businessName": "Stratos Pte. Ltd.",
		},
		"address": pgdoc.M{
			"country": "SG",
			"city":    "Singapore",
			"address": "143 Cecil Street, #06-02, GB Building, 069542",
		},
	}); err != nil {
		return 0, err
	}

	// ---- pricePlan ------------------------------------------------------------
	planID := "seed-plan-01"
	if err := seedOne("pricePlan", planID, pgdoc.M{
		"name":             "Public rate card",
		"enabled":          true,
		"accessMode":       "PUBLIC",
		"serviceProviders": []any{},
	}); err != nil {
		return 0, err
	}

	// ---- pricePlanRule (12 rules, names corrected from config-patch) ----------
	rules := []pgdoc.M{
		{
			"name":         "Compute: CPU + RAM components",
			"timeUnit":     "hour",
			"resourceType": "instance",
			"pricePlanId":  planID,
			"applyMethod":  "ADD_TO_TOTAL",
			"prices": []any{
				pgdoc.M{"attributeName": "vcpus", "tiers": []any{pgdoc.M{"value": "0.008"}}},
				pgdoc.M{"attributeName": "ram_gb", "tiers": []any{pgdoc.M{"value": "0.004"}}},
			},
			"filters":   []any{},
			"modifiers": []any{},
		},
		{
			"name":         "GPU: nvidia-4090 (RTX 4090)",
			"timeUnit":     "hour",
			"resourceType": "instance",
			"pricePlanId":  planID,
			"applyMethod":  "ADD_TO_TOTAL",
			"prices":       []any{pgdoc.M{"attributeName": "gpu_count", "tiers": []any{pgdoc.M{"value": "0.65"}}}},
			"filters":      []any{pgdoc.M{"attributeName": "gpu_model", "operator": "eq", "value": "nvidia-4090"}},
			"modifiers":    []any{},
		},
		{
			"name":         "GPU: nvidia-3090 (RTX 3090)",
			"timeUnit":     "hour",
			"resourceType": "instance",
			"pricePlanId":  planID,
			"applyMethod":  "ADD_TO_TOTAL",
			"prices":       []any{pgdoc.M{"attributeName": "gpu_count", "tiers": []any{pgdoc.M{"value": "0.43"}}}},
			"filters":      []any{pgdoc.M{"attributeName": "gpu_model", "operator": "eq", "value": "nvidia-3090"}},
			"modifiers":    []any{},
		},
		{
			"name":         "GPU: nvidia-3080ti (RTX 3080 Ti)",
			"timeUnit":     "hour",
			"resourceType": "instance",
			"pricePlanId":  planID,
			"applyMethod":  "ADD_TO_TOTAL",
			"prices":       []any{pgdoc.M{"attributeName": "gpu_count", "tiers": []any{pgdoc.M{"value": "0.29"}}}},
			"filters":      []any{pgdoc.M{"attributeName": "gpu_model", "operator": "eq", "value": "nvidia-3080ti"}},
			"modifiers":    []any{},
		},
		{
			"name":         "GPU: nvidia-a6000 (RTX A6000 Ampere 48GB)",
			"timeUnit":     "hour",
			"resourceType": "instance",
			"pricePlanId":  planID,
			"applyMethod":  "ADD_TO_TOTAL",
			"prices":       []any{pgdoc.M{"attributeName": "gpu_count", "tiers": []any{pgdoc.M{"value": "0.47"}}}},
			"filters":      []any{pgdoc.M{"attributeName": "gpu_model", "operator": "eq", "value": "nvidia-a6000"}},
			"modifiers":    []any{},
		},
		{
			"name":         "GPU: nvidia-pro-6000 (RTX PRO 6000 Blackwell 96GB)",
			"timeUnit":     "hour",
			"resourceType": "instance",
			"pricePlanId":  planID,
			"applyMethod":  "ADD_TO_TOTAL",
			"prices":       []any{pgdoc.M{"attributeName": "gpu_count", "tiers": []any{pgdoc.M{"value": "1.99"}}}},
			"filters":      []any{pgdoc.M{"attributeName": "gpu_model", "operator": "eq", "value": "nvidia-pro-6000"}},
			"modifiers":    []any{},
		},
		{
			"name":         "GPU: nvidia-pro-4500 (RTX PRO 4500 Blackwell 32GB)",
			"timeUnit":     "hour",
			"resourceType": "instance",
			"pricePlanId":  planID,
			"applyMethod":  "ADD_TO_TOTAL",
			"prices":       []any{pgdoc.M{"attributeName": "gpu_count", "tiers": []any{pgdoc.M{"value": "0.69"}}}},
			"filters":      []any{pgdoc.M{"attributeName": "gpu_model", "operator": "eq", "value": "nvidia-pro-4500"}},
			"modifiers":    []any{},
		},
		{
			"name":         "GPU: intel-a60",
			"timeUnit":     "hour",
			"resourceType": "instance",
			"pricePlanId":  planID,
			"applyMethod":  "ADD_TO_TOTAL",
			"prices":       []any{pgdoc.M{"attributeName": "gpu_count", "tiers": []any{pgdoc.M{"value": "0.15"}}}},
			"filters":      []any{pgdoc.M{"attributeName": "gpu_model", "operator": "eq", "value": "intel-a60"}},
			"modifiers":    []any{},
		},
		{
			"name":         "Public egress: 1 TiB free, then $0.01/GB",
			"timeUnit":     "hour",
			"resourceType": "instance_traffic",
			"pricePlanId":  planID,
			"applyMethod":  "OVERWRITE_TOTAL",
			"prices": []any{pgdoc.M{"attributeName": "outgoing_public_traffic_mb", "tiers": []any{
				pgdoc.M{"from": "0", "to": "1048576", "value": "0"},
				pgdoc.M{"from": "1048576", "value": "0.00001"},
			}}},
			"filters":   []any{},
			"modifiers": []any{},
		},
		{
			"name":         "Block storage",
			"timeUnit":     "hour",
			"resourceType": "volume",
			"pricePlanId":  planID,
			"applyMethod":  "ADD_TO_TOTAL",
			"prices":       []any{pgdoc.M{"attributeName": "size", "tiers": []any{pgdoc.M{"value": "0.000137"}}}},
			"filters":      []any{},
			"modifiers":    []any{},
		},
		{
			"name":         "Floating IP",
			"timeUnit":     "hour",
			"resourceType": "floating_ip",
			"pricePlanId":  planID,
			"applyMethod":  "ADD_TO_TOTAL",
			"prices":       []any{pgdoc.M{"attributeName": "existence", "tiers": []any{pgdoc.M{"value": "0.005"}}}},
			"filters":      []any{},
			"modifiers":    []any{},
		},
		{
			"name":         "Load balancer",
			"timeUnit":     "hour",
			"resourceType": "load_balancer",
			"pricePlanId":  planID,
			"applyMethod":  "ADD_TO_TOTAL",
			"prices":       []any{pgdoc.M{"attributeName": "existence", "tiers": []any{pgdoc.M{"value": "0.0165"}}}},
			"filters":      []any{},
			"modifiers":    []any{},
		},
	}

	for _, rule := range rules {
		id, ok := rule["id"].(string)
		if !ok {
			id = pgdoc.NewID()
			rule["id"] = id
		}
		if err := seedOne("pricePlanRule", id, rule); err != nil {
			return 0, err
		}
	}

	// ---- imageCategory (4 rows from config-patch) -----------------------------
	categories := []struct {
		id   string
		name string
		desc string
	}{
		{"seed-ic-01", "Ubuntu", "Ubuntu LTS server images"},
		{"seed-ic-02", "Debian", "Debian stable server images"},
		{"seed-ic-03", "Windows", "Windows Server images"},
		{"seed-ic-04", "GPU / ML", "GPU-ready images with CUDA / ML frameworks preinstalled"},
	}

	for _, ic := range categories {
		if err := seedOne("imageCategory", ic.id, pgdoc.M{
			"bareMetal":   false,
			"name":        ic.name,
			"description": ic.desc,
		}); err != nil {
			return 0, err
		}
	}

	return n, nil
}
