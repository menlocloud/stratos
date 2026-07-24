package notification

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/menlocloud/stratos/internal/cloud"
)

// withRoute injects the chi {externalServiceId}/{region} path params the handler reads.
func withRoute(r *http.Request, serviceID, region string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("externalServiceId", serviceID)
	rctx.URLParams.Add("region", region)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// TestReceiveSharedSecret pins the PER-PROVIDER shared-secret gate: the secret is resolved from the
// {externalServiceId} in the path (externalService.NotificationSecret). A request without the
// matching header — or for a provider that has no secret configured, or before a resolver is wired —
// is rejected 401 BEFORE any processing; the correct header for a configured provider passes.
func TestReceiveSharedSecret(t *testing.T) {
	h := &Handler{} // svc nil → a passing request just 200s (no cache mutation)
	h.SetSecretResolver(func(_ context.Context, serviceID string) string {
		if serviceID == "cloud-1" {
			return "s3cr3t"
		}
		return "" // any other provider has no secret configured
	})
	call := func(serviceID, hdr string) int {
		req := withRoute(httptest.NewRequest("POST", "/x/RegionOne", strings.NewReader(`{}`)), serviceID, "RegionOne")
		if hdr != "" {
			req.Header.Set(notificationSecretHeader, hdr)
		}
		rec := httptest.NewRecorder()
		h.receive(rec, req)
		return rec.Code
	}
	if got := call("cloud-1", ""); got != 401 {
		t.Errorf("missing secret: want 401, got %d", got)
	}
	if got := call("cloud-1", "wrong"); got != 401 {
		t.Errorf("wrong secret: want 401, got %d", got)
	}
	if got := call("cloud-1", "s3cr3t"); got != 200 {
		t.Errorf("correct secret: want 200, got %d", got)
	}
	// A provider with NO secret configured (resolver returns "") → closed even with a header.
	if got := call("unconfigured", "s3cr3t"); got != 401 {
		t.Errorf("provider without a secret: want 401 (fail-closed), got %d", got)
	}
	// No resolver wired at all → also closed.
	bare := &Handler{}
	rec := httptest.NewRecorder()
	bare.receive(rec, withRoute(httptest.NewRequest("POST", "/x/RegionOne", strings.NewReader(`{}`)), "cloud-1", "RegionOne"))
	if rec.Code != 401 {
		t.Errorf("no resolver wired: want 401 (fail-closed), got %d", rec.Code)
	}
}

// TestSameProject pins the cross-tenant delete guard: a notification resolving to project A must
// not archive a cached resource owned by project B.
func TestSameProject(t *testing.T) {
	if !sameProject("projA", "projA") {
		t.Error("same project must match")
	}
	if sameProject("projB", "projA") {
		t.Error("cross-project delete must be rejected")
	}
	if sameProject("projA", "") {
		t.Error("blank resolved project must fail closed")
	}
}

func TestTypeForEvent(t *testing.T) {
	bm := func(it string) bool { return it == "baremetal.large" }
	cases := []struct {
		event    string
		instType string
		want     string
		ok       bool
	}{
		// Host-level telemetry must NOT map to SERVER: it has no resource id, and nova-compute
		// emits it every monitoring cycle on every compute node.
		{"compute.metrics.update", "", "", false},
		{"compute.instance.create.end", "", cloud.TypeServer, true},
		{"compute.instance.create.end", "m1.small", cloud.TypeServer, true},
		{"compute.instance.create.end", "baremetal.large", cloud.TypeBaremetalServer, true},
		{"compute.instance.delete.start", "", cloud.TypeServer, true},
		{"volume.create.end", "", cloud.TypeVolume, true},
		{"network.create.end", "", cloud.TypeNetwork, true},
		{"subnet.delete.end", "", cloud.TypeSubnet, true},
		{"port.create.end", "", cloud.TypePort, true},
		{"router.update.end", "", cloud.TypeRouter, true},
		{"floatingip.create.end", "", cloud.TypeFloatingIP, true},
		{"image.update", "", cloud.TypeImage, true},
		{"dns.zone.create", "", cloud.TypeDNSZone, true},
		{"magnum.cluster.create", "", cloud.TypeKubernetesCluster, true},
		{"security_group.create.end", "", cloud.TypeSecurityGroup, true},
		{"orchestration.stack.create.end", "", cloud.TypeStack, true},
		{"share.create.end", "", cloud.TypeShare, true},
		{"identity.user.created", "", "", false}, // unmapped → skip
		{"", "", "", false},
	}
	for _, c := range cases {
		msg := OsloMessage{EventType: c.event}
		if c.instType != "" {
			msg.Payload = map[string]any{"instance_type": c.instType}
		}
		got, ok := TypeForEvent(msg, bm)
		if got != c.want || ok != c.ok {
			t.Errorf("TypeForEvent(%q,%q) = (%q,%v), want (%q,%v)", c.event, c.instType, got, ok, c.want, c.ok)
		}
	}
}

func TestMinimalInfo(t *testing.T) {
	// flat <x>_id + tenant_id
	got := minimalInfo(cloud.TypePort, map[string]any{"port_id": "p1", "tenant_id": "t1"})
	if got.externalResourceID != "p1" || got.externalProjectID != "t1" {
		t.Errorf("flat port: got %+v", got)
	}
	// nested <x>.id + <x>.tenant_id
	got = minimalInfo(cloud.TypeNetwork, map[string]any{
		"network": map[string]any{"id": "n1", "tenant_id": "t2"},
	})
	if got.externalResourceID != "n1" || got.externalProjectID != "t2" {
		t.Errorf("nested network: got %+v", got)
	}
	// project_id fallback for tenant
	got = minimalInfo(cloud.TypeVolume, map[string]any{"volume_id": "v1", "project_id": "t3"})
	if got.externalResourceID != "v1" || got.externalProjectID != "t3" {
		t.Errorf("project_id fallback: got %+v", got)
	}
	// flat id wins over nested
	got = minimalInfo(cloud.TypeFloatingIP, map[string]any{
		"floatingip_id": "f1",
		"floatingip":    map[string]any{"id": "f2", "tenant_id": "t4"},
	})
	if got.externalResourceID != "f1" || got.externalProjectID != "t4" {
		t.Errorf("flat-wins: got %+v", got)
	}
	// missing id → blank (Handle then skips)
	got = minimalInfo(cloud.TypeServer, map[string]any{"tenant_id": "t5"})
	if got.externalResourceID != "" || got.externalProjectID != "t5" {
		t.Errorf("missing id: got %+v", got)
	}
}

func TestParseOsloBody_UnwrapsEnvelope(t *testing.T) {
	// oslo.messaging AMQP envelope: the real notification is a JSON string under oslo.message.
	wrapped := []byte(`{"oslo.version":"2.0","oslo.message":"{\"event_type\":\"compute.instance.update\",\"payload\":{\"instance_id\":\"srv-1\",\"tenant_id\":\"proj-1\"}}"}`)
	msg, err := ParseOsloBody(wrapped)
	if err != nil {
		t.Fatalf("wrapped: %v", err)
	}
	if msg.EventType != "compute.instance.update" {
		t.Fatalf("event_type = %q, want compute.instance.update", msg.EventType)
	}
	if msg.Payload["instance_id"] != "srv-1" || msg.Payload["tenant_id"] != "proj-1" {
		t.Errorf("payload not unwrapped: %#v", msg.Payload)
	}

	// A bare (already-unwrapped) notification still parses directly.
	bare := []byte(`{"event_type":"volume.delete.end","payload":{"volume_id":"v-1"}}`)
	m2, err := ParseOsloBody(bare)
	if err != nil || m2.EventType != "volume.delete.end" {
		t.Fatalf("bare: %v msg=%#v", err, m2)
	}
}
