package project

import (
	"errors"
	"strings"
	"testing"

	"github.com/menlocloud/stratos/internal/cloud"
	"github.com/menlocloud/stratos/internal/cloud/kamaji"
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
	// A non-bool addon value must be a 400, not a silent drop — {"metricsServer": "false"}
	// ignored would install metrics-server (the chart default), the opposite of the request.
	if _, err := kamajiSpecFromData("p1", map[string]any{
		"name": "x", "version": "1.35.4",
		"nodeGroups": []any{map[string]any{"name": "w", "flavorId": "f", "count": 1}},
		"addons":     map[string]any{"metricsServer": "false"},
	}); err == nil || !strings.Contains(err.Error(), "metricsServer") {
		t.Errorf("non-bool addon: err = %v", err)
	}
	if _, err := kamajiSpecFromData("p1", map[string]any{"name": "x", "version": "1"}); err == nil {
		t.Error("missing nodeGroups: want error")
	}
}

// TestRotateNodeGroupImages pins the UPGRADE image rotation: every pool follows ITS OWN variant
// onto the target version, a variant without a target image refuses the upgrade, and a missing
// DEFAULT image keeps the current one instead of stamping "".
func TestRotateNodeGroupImages(t *testing.T) {
	d := kamaji.ClusterDefaults{
		Versions:      map[string]string{"1.35.4": "img-plain-1354"},
		ImageVariants: map[string]map[string]string{"nvidia": {"1.35.4": "img-nv-1354"}},
	}
	values := map[string]any{"nodeGroups": []any{
		map[string]any{"name": "cpu", "machineImageId": "img-plain-1342"},
		map[string]any{"name": "gpu", "machineImageId": "img-nv-1342", "imageVariant": "nvidia"},
	}}
	if err := rotateNodeGroupImages(values, d, "1.35.4"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	groups := values["nodeGroups"].([]any)
	if img := groups[0].(map[string]any)["machineImageId"]; img != "img-plain-1354" {
		t.Errorf("cpu image = %v", img)
	}
	if img := groups[1].(map[string]any)["machineImageId"]; img != "img-nv-1354" {
		t.Errorf("gpu image = %v, want the nvidia build — NOT the plain image", img)
	}

	// The variant has no image for the target → the whole upgrade is refused up front.
	d2 := kamaji.ClusterDefaults{Versions: map[string]string{"1.36.3": "img-plain-1363"}, ImageVariants: d.ImageVariants}
	if err := rotateNodeGroupImages(values, d2, "1.36.3"); err == nil || !strings.Contains(err.Error(), "nvidia") {
		t.Errorf("missing variant image: err = %v", err)
	}

	// No default image for the target (matrix gap) → default pools keep their current image.
	before := groups[0].(map[string]any)["machineImageId"]
	noDefault := map[string]any{"nodeGroups": []any{groups[0]}}
	if err := rotateNodeGroupImages(noDefault, kamaji.ClusterDefaults{}, "1.37.0"); err != nil {
		t.Fatalf("matrix gap: %v", err)
	}
	if img := groups[0].(map[string]any)["machineImageId"]; img != before {
		t.Errorf("matrix gap must keep the current image, got %v", img)
	}
}

// TestApplyNodeGroupImages pins the SET_NODE_GROUPS fallback ladder: keep-previous fires ONLY
// for an unchanged variant (matrix narrowing must not brick resizes), while a CHANGED variant —
// including changing to the default — must resolve or be refused, so the stored variant can
// never disagree with the image the pool actually boots.
func TestApplyNodeGroupImages(t *testing.T) {
	prevImage := map[string]string{"workers": "img-old", "gpu": "img-nv-old"}
	prevVariant := map[string]string{"workers": "", "gpu": "nvidia"}

	group := func(name, variant, img string) map[string]any {
		g := map[string]any{"name": name, "machineImageId": img}
		if variant != "" {
			g["imageVariant"] = variant
		}
		return g
	}

	// Unchanged variant, unresolvable (version dropped from the matrix) → keeps the previous image.
	unchanged := []any{group("gpu", "nvidia", "")}
	if err := applyNodeGroupImages(unchanged, prevImage, prevVariant, "1.34.2"); err != nil {
		t.Fatalf("unchanged variant: %v", err)
	}
	if img := unchanged[0].(map[string]any)["machineImageId"]; img != "img-nv-old" {
		t.Errorf("unchanged variant image = %v, want img-nv-old", img)
	}

	// Variant CHANGED (default → nvidia) but unresolvable → refused, never the silent fallback.
	changed := []any{group("workers", "nvidia", "")}
	if err := applyNodeGroupImages(changed, prevImage, prevVariant, "1.34.2"); err == nil || !strings.Contains(err.Error(), "nvidia") {
		t.Errorf("changed variant: err = %v", err)
	}

	// Variant cleared (nvidia → default) with no default image → refused, not a silent no-op.
	cleared := []any{group("gpu", "", "")}
	if err := applyNodeGroupImages(cleared, prevImage, prevVariant, "1.34.2"); err == nil || !strings.Contains(err.Error(), "no image") {
		t.Errorf("cleared variant: err = %v", err)
	}

	// New group with an unoffered variant → the variant-specific refusal.
	fresh := []any{group("newpool", "nvidia", "")}
	if err := applyNodeGroupImages(fresh, prevImage, prevVariant, "1.34.2"); err == nil || !strings.Contains(err.Error(), "variant") {
		t.Errorf("new group variant: err = %v", err)
	}

	// Resolved groups pass through untouched.
	ok := []any{group("workers", "", "img-new")}
	if err := applyNodeGroupImages(ok, prevImage, prevVariant, "1.35.4"); err != nil {
		t.Fatalf("resolved: %v", err)
	}
	if img := ok[0].(map[string]any)["machineImageId"]; img != "img-new" {
		t.Errorf("resolved image = %v", img)
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

// A node group's root volume must fit the image its machines boot — Cinder refuses a smaller one,
// and the refusal is otherwise invisible to the customer (a MachineDeployment stuck at zero ready).
func TestValidateNodeGroupDisks(t *testing.T) {
	defaults := kamaji.ClusterDefaults{
		RootVolumeGiB: 120,
		Versions:      map[string]string{"1.35.4": "img-default"},
		ImageVariants: map[string]map[string]string{"nvidia": {"1.35.4": "img-gpu"}},
	}
	calls := 0
	images := map[string]map[string]any{
		// Declared min_disk wins over the (smaller) image body.
		"img-default": {"min_disk": 60, "size": 3 * 1073741824},
		// No min_disk at all: the image's own virtual size is still a floor.
		"img-gpu": {"min_disk": 0, "virtual_size": 40 * 1073741824},
		"img-old": {"min_disk": 20},
	}
	get := func(id string) (map[string]any, error) {
		calls++
		im, ok := images[id]
		if !ok {
			return nil, errNoImage
		}
		return im, nil
	}
	check := func(groups []kamaji.NodeGroup, current map[string]string) error {
		return validateNodeGroupDisks(defaults, "1.35.4", groups, current, get)
	}

	if err := check([]kamaji.NodeGroup{{Name: "w", RootVolumeGiB: 60}}, nil); err != nil {
		t.Errorf("exactly at min_disk: %v", err)
	}
	err := check([]kamaji.NodeGroup{{Name: "w", RootVolumeGiB: 40}}, nil)
	if err == nil || !strings.Contains(err.Error(), "at least 60 GiB") {
		t.Errorf("undersized default image = %v", err)
	}
	// A variant pool is weighed against ITS image, and virtual_size counts even with min_disk 0.
	err = check([]kamaji.NodeGroup{{Name: "gpu", ImageVariant: "nvidia", RootVolumeGiB: 30}}, nil)
	if err == nil || !strings.Contains(err.Error(), "at least 40 GiB") {
		t.Errorf("undersized variant image = %v", err)
	}
	// Size omitted => the provider default is what gets checked, not zero.
	if err := check([]kamaji.NodeGroup{{Name: "w"}}, nil); err != nil {
		t.Errorf("provider default 120: %v", err)
	}
	// Edit path: a group the version matrix no longer covers (an admin dropped an old release)
	// resolves through the image it already runs — the same fallback applyNodeGroupImages uses.
	err = validateNodeGroupDisks(defaults, "1.34.0",
		[]kamaji.NodeGroup{{Name: "legacy", RootVolumeGiB: 10}}, map[string]string{"legacy": "img-old"}, get)
	if err == nil || !strings.Contains(err.Error(), "at least 20 GiB") {
		t.Errorf("undersized against current image = %v", err)
	}
	// An unreadable image leaves its group unchecked rather than blocking the request.
	if err := check([]kamaji.NodeGroup{{Name: "w", ImageID: "img-gone", RootVolumeGiB: 1}}, nil); err != nil {
		t.Errorf("unreadable image should not block: %v", err)
	}
	// One glance call per DISTINCT image, however many pools share it.
	calls = 0
	if err := check([]kamaji.NodeGroup{
		{Name: "a", RootVolumeGiB: 120}, {Name: "b", RootVolumeGiB: 120}, {Name: "c", RootVolumeGiB: 120},
	}, nil); err != nil {
		t.Fatalf("shared image: %v", err)
	}
	if calls != 1 {
		t.Errorf("glance calls = %d, want 1", calls)
	}
}

var errNoImage = errors.New("image not found")

func TestKamajiCachedImages(t *testing.T) {
	version, images := kamajiCachedImages(&cloud.CloudResource{Data: map[string]any{
		"cluster": map[string]any{
			"version": "1.35.4",
			"node_groups": []any{
				map[string]any{"name": "w", "image_id": "img-a"},
				map[string]any{"name": "gpu", "image_id": "img-b"},
				"junk",
			},
		},
	}})
	if version != "1.35.4" || images["w"] != "img-a" || images["gpu"] != "img-b" || len(images) != 2 {
		t.Errorf("version=%q images=%v", version, images)
	}
	// A cluster with no cached payload at all must not panic — it just has no fallback to offer.
	if v, m := kamajiCachedImages(&cloud.CloudResource{}); v != "" || len(m) != 0 {
		t.Errorf("empty resource = %q/%v", v, m)
	}
}
