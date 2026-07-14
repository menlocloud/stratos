package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	computequotas "github.com/gophercloud/gophercloud/v2/openstack/compute/v2/quotasets"
)

func TestComputeQuotaUsageRequestsDetailMicroversion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/os-quota-sets/tenant-1/detail" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("X-OpenStack-Nova-API-Version"); got != computeQuotaDetailMicroversion {
			t.Fatalf("nova microversion = %q, want %q", got, computeQuotaDetailMicroversion)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"quota_set":{"id":"tenant-1","instances":{"in_use":2,"reserved":1,"limit":10},"cores":{"in_use":4,"reserved":0,"limit":20},"ram":{"in_use":8192,"reserved":0,"limit":32768}}}`)
	}))
	defer server.Close()

	provider := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return server.URL + "/", nil },
		HTTPClient:      *server.Client(),
	}
	provider.UseTokenLock()
	provider.SetToken("test-token")

	got, err := (&Client{provider: provider, region: "RegionOne"}).ComputeQuotaUsage(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("ComputeQuotaUsage() error = %v", err)
	}
	if got.Instances != (QuotaMetric{Used: 2, Reserved: 1, Limit: 10}) {
		t.Fatalf("instances = %+v", got.Instances)
	}
}

func TestStorageQuotaUsagePreservesVolumeTypeDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/os-quota-sets/tenant-1" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("usage"); got != "true" {
			t.Fatalf("usage query = %q, want true", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"quota_set":{"id":"tenant-1","volumes":{"in_use":3,"reserved":1,"limit":20},"gigabytes":{"in_use":120,"reserved":10,"limit":500},"snapshots":{"in_use":2,"reserved":0,"limit":10},"per_volume_gigabytes":{"in_use":0,"reserved":0,"limit":200},"volumes_high-iops":{"in_use":2,"reserved":1,"limit":3},"gigabytes_high-iops":{"in_use":90,"reserved":0,"limit":100}}}`)
	}))
	defer server.Close()

	provider := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return server.URL + "/", nil },
		HTTPClient:      *server.Client(),
	}
	provider.UseTokenLock()
	provider.SetToken("test-token")

	got, err := (&Client{provider: provider, region: "RegionOne"}).StorageQuotaUsage(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("StorageQuotaUsage() error = %v", err)
	}
	if typed := got.VolumeTypes["high-iops"]; typed.Gigabytes == nil || typed.Gigabytes.Limit != 100 {
		t.Fatalf("high-iops quota = %+v", typed)
	}
}

func TestComputeQuotaUsageFromDetail(t *testing.T) {
	got := computeQuotaUsageFromDetail(computequotas.QuotaDetailSet{
		Instances: computequotas.QuotaDetail{InUse: 2, Reserved: 1, Limit: 10},
		Cores:     computequotas.QuotaDetail{InUse: 8, Reserved: 4, Limit: 32},
		RAM:       computequotas.QuotaDetail{InUse: 16384, Reserved: 4096, Limit: 65536},
	})

	if got.Instances != (QuotaMetric{Used: 2, Reserved: 1, Limit: 10}) {
		t.Fatalf("instances = %+v", got.Instances)
	}
	if got.Cores != (QuotaMetric{Used: 8, Reserved: 4, Limit: 32}) {
		t.Fatalf("cores = %+v", got.Cores)
	}
	if got.RAMMB != (QuotaMetric{Used: 16384, Reserved: 4096, Limit: 65536}) {
		t.Fatalf("ram = %+v", got.RAMMB)
	}
}

func TestStorageQuotaUsageFromRawIncludesVolumeTypes(t *testing.T) {
	raw := map[string]json.RawMessage{}
	for key, value := range map[string]string{
		"volumes":              `{"in_use":3,"reserved":1,"limit":20}`,
		"gigabytes":            `{"in_use":120,"reserved":10,"limit":500}`,
		"snapshots":            `{"in_use":2,"reserved":0,"limit":10}`,
		"per_volume_gigabytes": `{"in_use":0,"reserved":0,"limit":200}`,
		"volumes_high-iops":    `{"in_use":2,"reserved":1,"limit":3}`,
		"gigabytes_high-iops":  `{"in_use":90,"reserved":0,"limit":100}`,
	} {
		raw[key] = json.RawMessage(value)
	}
	got, err := storageQuotaUsageFromRaw(raw)
	if err != nil {
		t.Fatalf("storageQuotaUsageFromRaw() error = %v", err)
	}

	if got.Volumes != (QuotaMetric{Used: 3, Reserved: 1, Limit: 20}) {
		t.Fatalf("volumes = %+v", got.Volumes)
	}
	if got.Gigabytes != (QuotaMetric{Used: 120, Reserved: 10, Limit: 500}) {
		t.Fatalf("gigabytes = %+v", got.Gigabytes)
	}
	if got.Snapshots != (QuotaMetric{Used: 2, Reserved: 0, Limit: 10}) {
		t.Fatalf("snapshots = %+v", got.Snapshots)
	}
	if got.PerVolumeGigabytes == nil || *got.PerVolumeGigabytes != (QuotaMetric{Used: 0, Reserved: 0, Limit: 200}) {
		t.Fatalf("per-volume gigabytes = %+v", got.PerVolumeGigabytes)
	}
	typed := got.VolumeTypes["high-iops"]
	if typed.Volumes == nil || *typed.Volumes != (QuotaMetric{Used: 2, Reserved: 1, Limit: 3}) {
		t.Fatalf("high-iops volumes = %+v", typed.Volumes)
	}
	if typed.Gigabytes == nil || *typed.Gigabytes != (QuotaMetric{Used: 90, Reserved: 0, Limit: 100}) {
		t.Fatalf("high-iops gigabytes = %+v", typed.Gigabytes)
	}
}

func TestQuotaUsageRequiresOpenStackTargetProject(t *testing.T) {
	// The authenticated client does not need to carry a project scope: this is
	// how an admin application credential reads a tenant's quota. The explicit
	// target is nevertheless mandatory.
	if _, err := (&Client{}).ComputeQuotaUsage(context.Background(), ""); err == nil {
		t.Fatal("ComputeQuotaUsage without a target project should fail")
	}
	if _, err := (&Client{ceph: &cephBackend{}}).StorageQuotaUsage(context.Background(), "tenant-1"); !errors.Is(err, ErrNotOpenStack) {
		t.Fatalf("StorageQuotaUsage on Ceph: want ErrNotOpenStack, got %v", err)
	}
}

// The pgdoc-sourced externalProjectId is spliced into the quota-set URL path, so
// it must be shape-validated at the shared choke point (request-forgery barrier).
func TestQuotaUsageRejectsMalformedTargetProject(t *testing.T) {
	for _, target := range []string{"../admin-tenant", "tenant?usage=false", "tenant#frag", "tenant/detail", "tenant%2f"} {
		if _, err := (&Client{}).ComputeQuotaUsage(context.Background(), target); err == nil {
			t.Fatalf("ComputeQuotaUsage(%q) should fail validation", target)
		}
		if _, err := (&Client{}).StorageQuotaUsage(context.Background(), target); err == nil {
			t.Fatalf("StorageQuotaUsage(%q) should fail validation", target)
		}
	}
	if err := (&Client{}).requireOpenStackQuotaTarget("b1f4c6a8e2d34f7a9c0b1d2e3f4a5b6c"); err != nil {
		t.Fatalf("hex keystone id rejected: %v", err)
	}
	if err := (&Client{}).requireOpenStackQuotaTarget("3fa85f64-5717-4562-b3fc-2c963f66afa6"); err != nil {
		t.Fatalf("uuid keystone id rejected: %v", err)
	}
}
