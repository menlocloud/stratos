package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
)

// nova ≥ mv2.47 embeds the flavor specs (no id) — the shape the sync must read so rating isn't zero.
func TestFillEmbeddedFlavor_Embedded(t *testing.T) {
	srv := &Server{}
	fillEmbeddedFlavor(srv, map[string]any{
		"original_name": "gpu.8xa6000",
		"ram":           float64(131072), // JSON numbers decode to float64
		"vcpus":         float64(32),
		"disk":          float64(400),
		"extra_specs":   map[string]any{"pci_passthrough:alias": "a6000:8"},
	})
	if srv.FlavorName != "gpu.8xa6000" || srv.RAM != 131072 || srv.VCPUs != 32 || srv.Disk != 400 {
		t.Fatalf("embedded specs not read: %+v", srv)
	}
	if srv.FlavorExtraSpecs["pci_passthrough:alias"] != "a6000:8" {
		t.Errorf("extra_specs not read: %v", srv.FlavorExtraSpecs)
	}
}

// A bare {id,links} link (older microversion) → no-op, so the caller falls back to by-id resolution.
func TestFillEmbeddedFlavor_BareLink(t *testing.T) {
	srv := &Server{}
	fillEmbeddedFlavor(srv, map[string]any{"id": "f-1", "links": []any{}})
	if srv.RAM != 0 || srv.VCPUs != 0 || srv.FlavorName != "" {
		t.Errorf("bare link should not populate specs: %+v", srv)
	}
	if srv.FlavorExtraSpecs != nil {
		t.Errorf("unresolved extra specs must remain nil, got: %#v", srv.FlavorExtraSpecs)
	}
}

func TestListServersPreservesUnresolvedEmbeddedExtraSpecs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/servers/detail" {
			t.Fatalf("path = %q, want /servers/detail", r.URL.Path)
		}
		if got := r.Header.Get("X-OpenStack-Nova-API-Version"); got != flavorMicroversion {
			t.Fatalf("nova microversion = %q, want %q", got, flavorMicroversion)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"servers":[{"id":"srv-1","name":"cpu-server","status":"ACTIVE","hostId":"host-1","OS-EXT-AZ:availability_zone":"nova","created":"2026-07-15T00:00:00Z","updated":"2026-07-15T00:00:00Z","metadata":{},"addresses":{},"image":{"id":"img-1"},"flavor":{"original_name":"m1.small","ram":2048,"vcpus":1,"disk":20}}]}`)
	}))
	defer server.Close()

	provider := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return server.URL + "/", nil },
		HTTPClient:      *server.Client(),
	}
	provider.UseTokenLock()
	provider.SetToken("test-token")

	servers, err := (&Client{provider: provider, region: "RegionOne"}).ListServers(context.Background())
	if err != nil {
		t.Fatalf("ListServers() error = %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("ListServers() returned %d servers, want 1", len(servers))
	}
	if servers[0].FlavorExtraSpecs != nil {
		t.Fatalf("unresolved embedded extra specs must remain nil, got %#v", servers[0].FlavorExtraSpecs)
	}
}
