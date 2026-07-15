package project

import (
	"testing"

	"github.com/menlocloud/stratos/internal/platform/externalservice"
)

func TestApplyVolumeTypeConfigIsStrictAndUsesDisplayName(t *testing.T) {
	live := []map[string]any{
		{"id": "1", "name": "multiattach"},
		{"id": "2", "name": "ceph-ssd1"},
		{"id": "3", "name": "__DEFAULT__"},
	}
	configured := []externalservice.VolumeTypeConfig{{Name: "ceph-ssd1", DisplayName: "SSD"}}
	got := applyVolumeTypeConfig(live, configured)
	if len(got) != 1 || got[0]["name"] != "ceph-ssd1" || got[0]["displayName"] != "SSD" || got[0]["id"] != "2" {
		t.Fatalf("applyVolumeTypeConfig() = %#v", got)
	}
}

func TestResolveVolumeType(t *testing.T) {
	one := []map[string]any{{"name": "ceph-ssd1"}}
	if got, err := resolveVolumeType(one, ""); err != nil || got != "ceph-ssd1" {
		t.Fatalf("sole type should auto-select: got=%q err=%v", got, err)
	}
	many := []map[string]any{{"name": "ssd"}, {"name": "hdd"}}
	if _, err := resolveVolumeType(many, ""); err == nil {
		t.Fatal("multiple types must require an explicit selection")
	}
	if _, err := resolveVolumeType(one, "multiattach"); err == nil {
		t.Fatal("disabled/unconfigured type must be rejected")
	}
	if _, err := resolveVolumeType(nil, ""); err == nil {
		t.Fatal("empty catalog must fail closed")
	}
}

func TestExactPositiveIntRejectsFractions(t *testing.T) {
	for _, value := range []any{float64(0), float64(-1), 1.5, "10", nil} {
		if _, ok := exactPositiveInt(value); ok {
			t.Fatalf("exactPositiveInt(%#v) unexpectedly accepted", value)
		}
	}
	if got, ok := exactPositiveInt(float64(10)); !ok || got != 10 {
		t.Fatalf("exactPositiveInt(10) = %d, %v", got, ok)
	}
}

func TestImageSizeGiBUsesVirtualSize(t *testing.T) {
	image := map[string]any{
		"size":         float64(2 * 1073741824),
		"virtual_size": int64(20 * 1073741824),
	}
	if got := imageSizeGiB(image); got != 20 {
		t.Fatalf("imageSizeGiB() = %d, want virtual size 20 GiB", got)
	}
}

func TestNormalizeServerStorageDefaultsRootAndDataType(t *testing.T) {
	data := map[string]any{
		"rootVolume":  map[string]any{},
		"dataVolumes": []any{map[string]any{"sizeGiB": float64(25)}},
	}
	curated := []map[string]any{{"name": "ceph-ssd1", "displayName": "SSD"}}
	if err := normalizeServerStorageRequest(
		data,
		map[string]any{"disk": float64(40), "ephemeral": float64(0)},
		map[string]any{"min_disk": float64(20), "size": float64(2 * 1073741824)},
		curated,
	); err != nil {
		t.Fatalf("normalizeServerStorageRequest() error = %v", err)
	}
	root := data["rootVolume"].(map[string]any)
	if root["sizeGiB"] != 40 || root["type"] != "ceph-ssd1" {
		t.Fatalf("root volume = %#v", root)
	}
	dataVolumes := data["dataVolumes"].([]any)
	dataVolume := dataVolumes[0].(map[string]any)
	if dataVolume["sizeGiB"] != 25 || dataVolume["type"] != "ceph-ssd1" {
		t.Fatalf("data volume = %#v", dataVolume)
	}
}

func TestNormalizeServerStorageRejectsUndersizedAndEphemeral(t *testing.T) {
	curated := []map[string]any{{"name": "ssd"}}
	undersized := map[string]any{"rootVolume": map[string]any{"sizeGiB": float64(19), "type": "ssd"}}
	if err := normalizeServerStorageRequest(
		undersized,
		map[string]any{"disk": float64(40)},
		map[string]any{"min_disk": float64(20)},
		curated,
	); err == nil {
		t.Fatal("root smaller than the image minimum must be rejected")
	}
	customSize := map[string]any{"rootVolume": map[string]any{"sizeGiB": float64(25), "type": "ssd"}}
	if err := normalizeServerStorageRequest(
		customSize,
		map[string]any{"disk": float64(40)},
		map[string]any{"min_disk": float64(20)},
		curated,
	); err != nil {
		t.Fatalf("root below the flavor suggestion but above the image minimum should be accepted: %v", err)
	}
	if err := normalizeServerStorageRequest(
		map[string]any{},
		map[string]any{"disk": float64(40), "ephemeral": float64(1)},
		map[string]any{},
		curated,
	); err == nil {
		t.Fatal("flavor with local ephemeral disk must be rejected")
	}
	if err := normalizeServerStorageRequest(
		map[string]any{},
		map[string]any{"disk": float64(40), "swap": float64(512)},
		map[string]any{},
		curated,
	); err == nil {
		t.Fatal("flavor with local swap disk must be rejected")
	}
}

func TestNormalizeServerStorageRejectsMalformedRootVolume(t *testing.T) {
	err := normalizeServerStorageRequest(
		map[string]any{"rootVolume": "not-an-object"},
		map[string]any{"disk": 20},
		map[string]any{},
		[]map[string]any{{"name": "ssd"}},
	)
	if err == nil {
		t.Fatal("non-object rootVolume must be rejected")
	}
}
