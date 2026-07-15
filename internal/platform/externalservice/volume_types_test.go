package externalservice

import (
	"reflect"
	"testing"
)

func TestEnabledVolumeTypesIsRegionScopedAndFailClosed(t *testing.T) {
	es := &ExternalService{Config: map[string]any{
		"features": map[string]any{
			"volumeTypes": map[string]any{
				"RegionOne": []any{
					map[string]any{"name": "multiattach", "enabled": false},
					map[string]any{"name": "ceph-ssd1", "displayName": " SSD ", "enabled": true},
					map[string]any{"name": "archive", "displayName": "", "enabled": true},
					map[string]any{"name": "ceph-ssd1", "displayName": "duplicate", "enabled": true},
				},
			},
		},
	}}

	want := []VolumeTypeConfig{
		{Name: "ceph-ssd1", DisplayName: "SSD"},
		{Name: "archive", DisplayName: "archive"},
	}
	if got := es.EnabledVolumeTypes("RegionOne"); !reflect.DeepEqual(got, want) {
		t.Fatalf("EnabledVolumeTypes() = %#v, want %#v", got, want)
	}
	if got := es.EnabledVolumeTypes("RegionTwo"); len(got) != 0 {
		t.Fatalf("missing region must fail closed, got %#v", got)
	}
	if got := (&ExternalService{}).EnabledVolumeTypes("RegionOne"); len(got) != 0 {
		t.Fatalf("missing catalog must fail closed, got %#v", got)
	}
}
