package metrics

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/menlocloud/stratos/internal/cloud/client"
)

// TestGnocchiPrometheusParity is the READ-ONLY live parity proof: the same instance's
// month-to-date traffic read through BOTH fetchers (gnocchi via OS_* creds, prometheus via
// STRATOS_PROM_LIVE_*) on a cloud where ceilometer→gnocchi and prometheus-libvirt-exporter
// both run. Skipped unless both env sets are present plus STRATOS_PARITY_INSTANCE (a nova
// uuid VISIBLE to the OS_* project — gnocchi scopes resources by project).
//
// The two sources are sampled moments apart and gnocchi's hourly resample can lag up to an
// hour of very recent traffic, so the assertion is deliberately loose: same interface set,
// and per-direction totals within 10% or 16 MB, whichever is larger. The exact numbers are
// logged — this test's output IS the parity report.
func TestGnocchiPrometheusParity(t *testing.T) {
	instance := os.Getenv("STRATOS_PARITY_INSTANCE")
	if os.Getenv("OS_AUTH_URL") == "" || os.Getenv("STRATOS_PROM_LIVE_URL") == "" || instance == "" {
		t.Skip("needs OS_AUTH_URL + STRATOS_PROM_LIVE_URL + STRATOS_PARITY_INSTANCE — skipping live parity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cc, err := client.New(ctx, client.Config{
		AuthURL: os.Getenv("OS_AUTH_URL"), Region: os.Getenv("OS_REGION_NAME"),
		Username: os.Getenv("OS_USERNAME"), Password: os.Getenv("OS_PASSWORD"),
		UserDomainName: os.Getenv("OS_USER_DOMAIN_NAME"),
		ProjectName:    os.Getenv("OS_PROJECT_NAME"), ProjectDomainName: os.Getenv("OS_PROJECT_DOMAIN_NAME"),
	})
	if err != nil {
		t.Fatalf("keystone auth: %v", err)
	}
	gn, err := New(cc)
	if err != nil {
		t.Fatalf("gnocchi endpoint: %v", err)
	}
	promCfg := PrometheusConfig{URL: os.Getenv("STRATOS_PROM_LIVE_URL")}
	if tenant := os.Getenv("STRATOS_PROM_LIVE_TENANT"); tenant != "" {
		promCfg.Headers = map[string]string{"X-Scope-OrgID": tenant}
	}
	pr, err := NewPrometheus(promCfg)
	if err != nil {
		t.Fatalf("prometheus client: %v", err)
	}

	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	type side struct {
		name    string
		ifaces  []Resource
		in, out map[string]decimal.Decimal // tap name → MB
	}
	measure := func(name string, f MeasureFetcher) side {
		ifaces, err := f.SearchInstanceInterfaces(ctx, instance)
		if err != nil {
			t.Fatalf("%s: discover interfaces: %v", name, err)
		}
		s := side{name: name, ifaces: ifaces, in: map[string]decimal.Decimal{}, out: map[string]decimal.Decimal{}}
		for _, iface := range ifaces {
			in, err := f.MeasuresMBForCurrentMonth(ctx, iface.Metrics["network.incoming.bytes"], 0, monthStart)
			if err != nil {
				t.Fatalf("%s: incoming %s: %v", name, iface.Name, err)
			}
			out, err := f.MeasuresMBForCurrentMonth(ctx, iface.Metrics["network.outgoing.bytes"], 0, monthStart)
			if err != nil {
				t.Fatalf("%s: outgoing %s: %v", name, iface.Name, err)
			}
			s.in[iface.Name], s.out[iface.Name] = in, out
			t.Logf("%s: %s incoming=%s MB outgoing=%s MB", name, iface.Name, in, out)
		}
		return s
	}

	g := measure("gnocchi", gn)
	p := measure("prometheus", pr)

	if len(g.ifaces) != len(p.ifaces) {
		t.Fatalf("interface sets differ: gnocchi=%d prometheus=%d", len(g.ifaces), len(p.ifaces))
	}
	tolerance := func(a, b decimal.Decimal) bool {
		diff := a.Sub(b).Abs()
		limit := decimal.NewFromInt(16) // MB floor: hourly-resample lag on a quiet VM
		if pct := a.Add(b).Div(decimal.NewFromInt(2)).Mul(decimal.NewFromFloat(0.10)); pct.GreaterThan(limit) {
			limit = pct
		}
		return diff.LessThanOrEqual(limit)
	}
	for tap, gin := range g.in {
		pin, ok := p.in[tap]
		if !ok {
			t.Fatalf("prometheus missing interface %s", tap)
		}
		if !tolerance(gin, pin) {
			t.Fatalf("INCOMING mismatch on %s: gnocchi=%s prometheus=%s (direction bug or source drift)", tap, gin, pin)
		}
		if gout, pout := g.out[tap], p.out[tap]; !tolerance(gout, pout) {
			t.Fatalf("OUTGOING mismatch on %s: gnocchi=%s prometheus=%s", tap, gout, pout)
		}
	}
	t.Logf("PARITY OK: %d interfaces, per-direction totals within tolerance", len(g.ifaces))
}
