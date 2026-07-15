package project

import (
	"reflect"
	"testing"

	"github.com/menlocloud/stratos/internal/platform/externalservice"
)

func TestGPUQuotaLimits(t *testing.T) {
	got := gpuQuotaLimits(map[string]any{
		"gpu": map[string]any{
			"nvidia-a6000": float64(4),
			"h100":         int64(2),
			"*":            8,
			"fractional":   1.5,
			"negative":     -1,
			"invalid":      "3",
		},
	})
	want := map[string]int{"nvidia-a6000": 4, "h100": 2, "*": 8}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gpuQuotaLimits() = %#v, want %#v", got, want)
	}
}

func TestQuotaReaderClientConfigKeepsApplicationCredentialScope(t *testing.T) {
	es := &externalservice.ExternalService{
		Config: map[string]any{
			"identityUrl": "https://keystone.example/v3",
			"auth": map[string]any{
				"adminAuthType":           "application_credential",
				"applicationCredentialId": "app-cred-id",
			},
		},
		Secret: map[string]any{"applicationCredentialSecret": "secret"},
	}
	cfg := quotaReaderClientConfig(es, "RegionTwo", "tenant-project")
	if cfg.AppCredID != "app-cred-id" || cfg.AppCredSecret != "secret" {
		t.Fatalf("application credential was not preserved: %+v", cfg)
	}
	if cfg.ProjectID != "" || cfg.ProjectName != "" {
		t.Fatalf("application credential must not be falsely re-scoped: %+v", cfg)
	}
	if cfg.Region != "RegionTwo" {
		t.Fatalf("application credential region = %q, want RegionTwo", cfg.Region)
	}
}

func TestQuotaReaderClientConfigScopesPasswordCredentialToTenant(t *testing.T) {
	es := &externalservice.ExternalService{
		Config: map[string]any{
			"identityUrl": "https://keystone.example/v3",
			"auth": map[string]any{
				"adminAuthType":   "password",
				"adminUsername":   "admin",
				"adminProjectId":  "admin-project",
				"adminDomainName": "Default",
			},
		},
		Secret: map[string]any{"adminPassword": "secret"},
	}
	cfg := quotaReaderClientConfig(es, "RegionOne", "tenant-project")
	if cfg.ProjectID != "tenant-project" || cfg.ProjectName != "" {
		t.Fatalf("password credential target = %+v, want tenant project scope", cfg)
	}
}

func TestGPUQuotaLimitsAlwaysReturnsObject(t *testing.T) {
	got := gpuQuotaLimits(nil)
	if got == nil || len(got) != 0 {
		t.Fatalf("gpuQuotaLimits(nil) = %#v, want non-nil empty map", got)
	}
}

func TestGPUQuotaLimitsCanonicalizesLegacyAliases(t *testing.T) {
	got := gpuQuotaLimits(map[string]any{"gpu": map[string]any{
		"NVIDIA_A6000": float64(4),
		"nvidia-a6000": float64(2),
	}})
	want := map[string]int{"nvidia-a6000": 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gpuQuotaLimits() = %#v, want canonical key to win: %#v", got, want)
	}
}

func TestQuotaRegionForRequestValidatesConfiguredRegion(t *testing.T) {
	es := &externalservice.ExternalService{Config: map[string]any{
		"regions": map[string]any{
			"RegionOne": map[string]any{},
			"RegionTwo": map[string]any{},
		},
	}}

	if got, ok := quotaRegionForRequest(es, "RegionOne", "RegionTwo"); !ok || got != "RegionTwo" {
		t.Fatalf("configured request = (%q, %v), want (RegionTwo, true)", got, ok)
	}
	if got, ok := quotaRegionForRequest(es, "RegionOne", "Unknown"); ok || got != "" {
		t.Fatalf("unknown request = (%q, %v), want (empty, false)", got, ok)
	}
	if got, ok := quotaRegionForRequest(es, "RegionOne", ""); !ok || got != "RegionOne" {
		t.Fatalf("default request = (%q, %v), want (RegionOne, true)", got, ok)
	}
}

// Legacy service docs have no config.regions: the provisioned/default region still
// works, an arbitrary caller-supplied region does not, and no region at all resolves
// to not-ok (the handler degrades to a warnings-partial 200, not a 400).
func TestQuotaRegionForRequestLegacyServiceWithoutRegions(t *testing.T) {
	es := &externalservice.ExternalService{Config: map[string]any{}}

	if got, ok := quotaRegionForRequest(es, "RegionOne", ""); !ok || got != "RegionOne" {
		t.Fatalf("legacy default = (%q, %v), want (RegionOne, true)", got, ok)
	}
	if got, ok := quotaRegionForRequest(es, "RegionOne", "RegionOne"); !ok || got != "RegionOne" {
		t.Fatalf("legacy matching request = (%q, %v), want (RegionOne, true)", got, ok)
	}
	if _, ok := quotaRegionForRequest(es, "RegionOne", "Other"); ok {
		t.Fatal("legacy arbitrary request must not be ok")
	}
	if _, ok := quotaRegionForRequest(es, "", ""); ok {
		t.Fatal("no fallback and no request must not be ok")
	}
}
