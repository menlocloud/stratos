package project

import (
	"strings"
	"testing"
)

func TestKamajiSpecFromData(t *testing.T) {
	spec, err := kamajiSpecFromData("p1", map[string]any{
		"name":         "my cluster ✨",
		"version":      "1.35.4",
		"ha":           true,
		"oidc":         map[string]any{"issuerUrl": "https://idp", "clientId": "kube", "empty": ""},
		"allowedCidrs": []any{"10.0.0.0/8", ""},
		"nodeGroups": []any{
			map[string]any{"name": "w", "flavorId": "m5.large", "count": 3,
				"labels": map[string]any{"tier": "app"}, "taints": []any{"a=b:NoSchedule"}},
			map[string]any{"name": "b", "flavorId": "m5.xl", "autoscale": true, "min": 1, "max": 4},
		},
	})
	if err != nil {
		t.Fatalf("kamajiSpecFromData: %v", err)
	}
	// Display name is free-form; the k8s identifier is always the generated stc- id (plan §9).
	if spec.DisplayName != "my cluster ✨" || !strings.HasPrefix(spec.ID, "stc-") || len(spec.ID) != 12 {
		t.Errorf("id/display = %q/%q", spec.ID, spec.DisplayName)
	}
	if !spec.HA || spec.Version != "1.35.4" || spec.ProjectID != "p1" {
		t.Errorf("spec = %+v", spec)
	}
	if spec.OIDC["issuerUrl"] != "https://idp" {
		t.Errorf("oidc = %v", spec.OIDC)
	}
	if _, has := spec.OIDC["empty"]; has {
		t.Error("empty oidc values must be dropped")
	}
	if len(spec.AllowedCIDRs) != 1 {
		t.Errorf("cidrs = %v", spec.AllowedCIDRs)
	}
	if len(spec.NodeGroups) != 2 || spec.NodeGroups[0].Labels["tier"] != "app" || spec.NodeGroups[1].Max != 4 {
		t.Errorf("nodeGroups = %+v", spec.NodeGroups)
	}

	// Missing pieces fail fast.
	if _, err := kamajiSpecFromData("p1", map[string]any{"version": "1.35.4", "nodeGroups": []any{}}); err == nil {
		t.Error("missing name: want error")
	}
	if _, err := kamajiSpecFromData("p1", map[string]any{"name": "x", "version": "1"}); err == nil {
		t.Error("missing nodeGroups: want error")
	}
}

func TestNewClusterIDUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 64 {
		id := newClusterID()
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
	}
}
