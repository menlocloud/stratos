package kamaji

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/menlocloud/stratos/internal/cloud/client"
)

func testCfg() Config {
	return Config{
		Kubeconfig:    "kc",
		Region:        "az1",
		ArgoNamespace: "argocd",
		ArgoProject:   "stratos-k8s",
		ChartRepo:     "ghcr.io/menlocloud/charts",
		ChartName:     "openstack-kamaji-cluster",
		ChartVersion:  "0.2.3",
		Defaults: ClusterDefaults{
			DataStoreName:     "default",
			FloatingNetworkID: "fnet-1",
			ExternalNetworkID: "ext-1",
			DNSZone:           "k8s.example.com",
			Versions:          map[string]string{"1.35.4": "img-1354", "1.34.2": "img-1342"},
		},
	}
}

func testSpec() ClusterSpec {
	return ClusterSpec{
		ID: "stc-abcd1234", DisplayName: "prod cluster", ProjectID: "p1", Version: "1.35.4", HA: true,
		OIDC:         map[string]string{"issuerUrl": "https://idp.example.com", "clientId": "kube"},
		AllowedCIDRs: []string{"10.0.0.0/8", "1.2.3.4/32"},
		NodeGroups: []NodeGroup{
			{Name: "workers", FlavorID: "m5.large", Count: 3, Labels: map[string]string{"tier": "app"}, Taints: []string{"gpu=true:NoSchedule"}},
			{Name: "burst", FlavorID: "m5.xlarge", Autoscale: true, Min: 1, Max: 5},
		},
	}
}

func TestSpecValidate(t *testing.T) {
	d := testCfg().Defaults
	if err := testSpec().Validate(d); err != nil {
		t.Fatalf("valid spec: %v", err)
	}
	bad := testSpec()
	bad.Version = "1.99.0"
	if err := bad.Validate(d); err == nil || !strings.Contains(err.Error(), "not offered") {
		t.Errorf("uncurated version: %v", err)
	}
	bad = testSpec()
	bad.NodeGroups = nil
	if err := bad.Validate(d); err == nil {
		t.Error("no node groups: want error")
	}
	bad = testSpec()
	bad.NodeGroups[1].Max = 0
	if err := bad.Validate(d); err == nil {
		t.Error("autoscale max < min: want error")
	}
	// Flavor allowlist: when configured, only listed flavors pass.
	d.Flavors = []string{"m5.large"}
	if err := testSpec().Validate(d); err == nil || !strings.Contains(err.Error(), "not offered") {
		t.Errorf("allowlist: m5.xlarge must be refused: %v", err)
	}
	d.Flavors = []string{"m5.large", "m5.xlarge"}
	if err := testSpec().Validate(d); err != nil {
		t.Errorf("allowlist covering both flavors: %v", err)
	}
}

func TestBuildValues(t *testing.T) {
	cfg := testCfg()
	v := BuildValues(cfg, testSpec())

	if v["kubernetesVersion"] != "1.35.4" {
		t.Errorf("kubernetesVersion = %v", v["kubernetesVersion"])
	}
	cp := v["kamajiControlPlane"].(map[string]any)
	if cp["replicas"] != 3 {
		t.Errorf("HA replicas = %v", cp["replicas"])
	}
	net := cp["network"].(map[string]any)
	ann := net["serviceAnnotations"].(map[string]any)
	if ann["loadbalancer.openstack.org/allowed-cidrs"] != "10.0.0.0/8,1.2.3.4/32" {
		t.Errorf("allowed-cidrs = %v", ann["loadbalancer.openstack.org/allowed-cidrs"])
	}
	if ann["external-dns.alpha.kubernetes.io/hostname"] != "stc-abcd1234.k8s.example.com" {
		t.Errorf("hostname = %v", ann["external-dns.alpha.kubernetes.io/hostname"])
	}
	oidc := v["oidc"].(map[string]any)
	if oidc["issuerUrl"] != "https://idp.example.com" || oidc["clientId"] != "kube" {
		t.Errorf("oidc = %v", oidc)
	}
	groups := v["nodeGroups"].([]any)
	if len(groups) != 2 {
		t.Fatalf("nodeGroups = %d", len(groups))
	}
	g0 := groups[0].(map[string]any)
	if g0["imageId"] != "img-1354" { // resolved from the version matrix
		t.Errorf("imageId = %v", g0["imageId"])
	}
	if g0["count"] != 3 {
		t.Errorf("count = %v", g0["count"])
	}
	if _, has := g0["autoscale"]; has {
		t.Error("fixed group must not carry autoscale")
	}
	g1 := groups[1].(map[string]any)
	if g1["autoscale"] != true || g1["min"] != 1 || g1["max"] != 5 {
		t.Errorf("autoscale group = %v", g1)
	}
	if _, has := g1["count"]; has {
		t.Error("autoscale group must not carry count")
	}

	// No OIDC issuer → no oidc block at all (chart default = disabled).
	spec := testSpec()
	spec.OIDC = nil
	spec.HA = false
	v = BuildValues(cfg, spec)
	if _, has := v["oidc"]; has {
		t.Error("oidc block must be absent when no issuer")
	}
	if v["kamajiControlPlane"].(map[string]any)["replicas"] != 1 {
		t.Error("non-HA replicas != 1")
	}
}

func TestBuildApplication(t *testing.T) {
	cfg := testCfg()
	spec := testSpec()
	app := BuildApplication(cfg, spec, "svc-1", "", BuildValues(cfg, spec))

	meta := app["metadata"].(map[string]any)
	if meta["name"] != "stc-abcd1234" || meta["namespace"] != "argocd" {
		t.Errorf("metadata = %v", meta)
	}
	labels := meta["labels"].(map[string]any)
	if labels[LabelProject] != "p1" || labels[LabelService] != "svc-1" || labels[LabelManagedBy] != "stratos" {
		t.Errorf("labels = %v", labels)
	}
	fins := meta["finalizers"].([]any)
	if len(fins) != 1 || fins[0] != "resources-finalizer.argocd.argoproj.io" {
		t.Errorf("finalizers = %v", fins)
	}
	src := app["spec"].(map[string]any)["source"].(map[string]any)
	if src["targetRevision"] != "0.2.3" { // pinned default when none given
		t.Errorf("targetRevision = %v", src["targetRevision"])
	}
	dst := app["spec"].(map[string]any)["destination"].(map[string]any)
	if dst["namespace"] != "st-p1" {
		t.Errorf("destination ns = %v", dst["namespace"])
	}
}

func TestCloudsYAML(t *testing.T) {
	out, err := CloudsYAML(client.Config{
		AuthURL: "https://keystone:5000/v3", Region: "az1",
		Username: "admin", Password: "pw", UserDomainName: "Default",
		ProjectID: "ext-proj-1", ProjectDomainName: "Default",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"auth_url: https://keystone:5000/v3", "project_id: ext-proj-1", "region_name: az1", "password: pw"} {
		if !strings.Contains(out, want) {
			t.Errorf("clouds.yaml missing %q:\n%s", want, out)
		}
	}
	out, err = CloudsYAML(client.Config{AuthURL: "https://k/v3", Region: "az1", AppCredID: "ac", AppCredSecret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "application_credential_id: ac") {
		t.Errorf("appcred clouds.yaml:\n%s", out)
	}
}

// fakeAPI records calls and serves canned objects.
type fakeAPI struct {
	namespaces map[string]map[string]string
	secrets    map[string]map[string]string // ns/name → stringData
	secretObjs map[string]map[string]any    // ns/name → full object (metadata for ListSecrets)
	apps       map[string]map[string]any    // ns/name → object
	tcps       map[string]map[string]any
	mds        []map[string]any
	deleted    []string
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		namespaces: map[string]map[string]string{},
		secrets:    map[string]map[string]string{},
		secretObjs: map[string]map[string]any{},
		apps:       map[string]map[string]any{},
		tcps:       map[string]map[string]any{},
	}
}

func (f *fakeAPI) EnsureNamespace(_ context.Context, name string, labels map[string]string) error {
	f.namespaces[name] = labels
	return nil
}
func (f *fakeAPI) GetNamespace(_ context.Context, name string) (map[string]any, error) {
	labels, ok := f.namespaces[name]
	if !ok {
		return nil, nil
	}
	l := map[string]any{}
	for k, v := range labels {
		l[k] = v
	}
	return map[string]any{"metadata": map[string]any{"name": name, "labels": l}}, nil
}
func (f *fakeAPI) DeleteNamespace(_ context.Context, name string) error {
	delete(f.namespaces, name)
	f.deleted = append(f.deleted, "namespace:"+name)
	return nil
}
func (f *fakeAPI) ApplySecret(_ context.Context, ns, name string, sd map[string]string, labels, annotations map[string]string) error {
	f.secrets[ns+"/"+name] = sd
	meta := map[string]any{"name": name, "namespace": ns}
	l := map[string]any{}
	for k, v := range labels {
		l[k] = v
	}
	meta["labels"] = l
	if len(annotations) > 0 {
		a := map[string]any{}
		for k, v := range annotations {
			a[k] = v
		}
		meta["annotations"] = a
	}
	f.secretObjs[ns+"/"+name] = map[string]any{"metadata": meta}
	return nil
}
func (f *fakeAPI) ListSecrets(_ context.Context, ns, labelSelector string) ([]map[string]any, error) {
	var out []map[string]any
	for k, v := range f.secretObjs {
		if strings.HasPrefix(k, ns+"/") && matchSelector(v, labelSelector) {
			out = append(out, v)
		}
	}
	return out, nil
}
func (f *fakeAPI) ListSecretsAllNamespaces(_ context.Context, labelSelector string) ([]map[string]any, error) {
	var out []map[string]any
	for _, v := range f.secretObjs {
		if matchSelector(v, labelSelector) {
			out = append(out, v)
		}
	}
	return out, nil
}
func (f *fakeAPI) GetSecretData(_ context.Context, ns, name string) (map[string][]byte, error) {
	sd, ok := f.secrets[ns+"/"+name]
	if !ok {
		return nil, nil
	}
	out := map[string][]byte{}
	for k, v := range sd {
		out[k] = []byte(v)
	}
	return out, nil
}
func (f *fakeAPI) DeleteSecret(_ context.Context, ns, name string) error {
	delete(f.secrets, ns+"/"+name)
	delete(f.secretObjs, ns+"/"+name)
	f.deleted = append(f.deleted, "secret:"+ns+"/"+name)
	return nil
}
func (f *fakeAPI) ApplyApplication(_ context.Context, app map[string]any) error {
	meta := app["metadata"].(map[string]any)
	key := meta["namespace"].(string) + "/" + meta["name"].(string)
	if prev, ok := f.apps[key]; ok {
		// SSA merge approximation: replace only spec.source + keep the rest.
		prevSpec := prev["spec"].(map[string]any)
		newSpec := app["spec"].(map[string]any)
		for k, v := range newSpec {
			prevSpec[k] = v
		}
		return nil
	}
	f.apps[key] = app
	return nil
}
func (f *fakeAPI) GetApplication(_ context.Context, ns, name string) (map[string]any, error) {
	return f.apps[ns+"/"+name], nil
}
// matchSelector applies a "k=v[,k2=v2…]" label selector against metadata.labels.
func matchSelector(obj map[string]any, selector string) bool {
	if selector == "" {
		return true
	}
	labels, _ := dig(obj, "metadata", "labels").(map[string]any)
	for _, term := range strings.Split(selector, ",") {
		k, v, _ := strings.Cut(term, "=")
		if labels[k] != v {
			return false
		}
	}
	return true
}

func (f *fakeAPI) ListApplications(_ context.Context, ns, labelSelector string) ([]map[string]any, error) {
	var out []map[string]any
	for k, v := range f.apps {
		if strings.HasPrefix(k, ns+"/") && matchSelector(v, labelSelector) {
			out = append(out, v)
		}
	}
	return out, nil
}
func (f *fakeAPI) DeleteApplication(_ context.Context, ns, name string) error {
	delete(f.apps, ns+"/"+name)
	f.deleted = append(f.deleted, "app:"+ns+"/"+name)
	return nil
}
func (f *fakeAPI) GetTenantControlPlane(_ context.Context, ns, name string) (map[string]any, error) {
	return f.tcps[ns+"/"+name], nil
}
func (f *fakeAPI) ListTenantControlPlanes(_ context.Context, ns, labelSelector string) ([]map[string]any, error) {
	var out []map[string]any
	for k, v := range f.tcps {
		if strings.HasPrefix(k, ns+"/") && matchSelector(v, labelSelector) {
			out = append(out, v)
		}
	}
	return out, nil
}
func (f *fakeAPI) ListMachineDeployments(_ context.Context, _, _ string) ([]map[string]any, error) {
	return f.mds, nil
}

func TestServiceCreateDelete(t *testing.T) {
	api := newFakeAPI()
	svc := NewWithAPI(api, testCfg(), "svc-1")
	ctx := context.Background()

	data, err := svc.CreateCluster(ctx, testSpec(), client.Config{AuthURL: "https://k/v3", Username: "u", Password: "p", ProjectID: "ext-1", Region: "az1"})
	if err != nil {
		t.Fatalf("CreateCluster: %v", err)
	}
	if _, ok := api.namespaces["st-p1"]; !ok {
		t.Error("namespace not ensured")
	}
	sd := api.secrets["st-p1/stc-abcd1234-cloud-config"]
	if sd == nil || !strings.Contains(sd["clouds.yaml"], "project_id: ext-1") {
		t.Errorf("clouds.yaml secret = %v", sd)
	}
	if api.apps["argocd/stc-abcd1234"] == nil {
		t.Error("application not applied")
	}
	c := data["cluster"].(map[string]any)
	if c["status"] != "PENDING" || c["id"] != "stc-abcd1234" || c["name"] != "prod cluster" {
		t.Errorf("initial data = %v", c)
	}

	if err := svc.DeleteCluster(ctx, "p1", "stc-abcd1234"); err != nil {
		t.Fatalf("DeleteCluster: %v", err)
	}
	if len(api.apps) != 0 {
		t.Error("delete left the application behind")
	}
	// The clouds.yaml secret must SURVIVE the cluster delete — CAPO/OCCM need it while the
	// ArgoCD cascade deletes the worker VMs / LB. FinalizeOrphans reaps it afterwards.
	if len(api.secrets) != 1 {
		t.Error("clouds.yaml secret must outlive the application delete")
	}
	// Idempotent: deleting again is fine.
	if err := svc.DeleteCluster(ctx, "p1", "stc-abcd1234"); err != nil {
		t.Fatalf("DeleteCluster twice: %v", err)
	}
}

func TestFinalizeOrphans(t *testing.T) {
	api := newFakeAPI()
	svc := NewWithAPI(api, testCfg(), "svc-1")
	ctx := context.Background()
	spec := testSpec()
	spec.AppCredID, spec.AppCredUserID, spec.AppCredServiceID = "cred-1", "user-1", "svc-os"
	if _, err := svc.CreateCluster(ctx, spec, client.Config{AuthURL: "https://k/v3", Username: "u", Password: "p", ProjectID: "ext-1", Region: "az1"}); err != nil {
		t.Fatal(err)
	}
	var revoked []string
	revoke := func(_ context.Context, osSvcID, userID, credID string) error {
		revoked = append(revoked, osSvcID+"/"+userID+"/"+credID)
		return nil
	}

	// Cluster still alive → nothing happens.
	if _, err := svc.FinalizeOrphans(ctx, "p1", revoke); err != nil {
		t.Fatalf("FinalizeOrphans (live): %v", err)
	}
	if len(revoked) != 0 || len(api.secrets) != 1 {
		t.Fatal("live cluster must not be finalized")
	}

	// Application deleted, but the TCP (cascade in flight) still exists → still nothing, and the
	// leftover is reported as pending (teardown defers tenant deletion on this signal).
	if err := svc.DeleteCluster(ctx, "p1", spec.ID); err != nil {
		t.Fatal(err)
	}
	api.tcps["st-p1/"+spec.ID] = map[string]any{"metadata": map[string]any{"name": spec.ID}}
	pending, err := svc.FinalizeOrphans(ctx, "p1", revoke)
	if err != nil {
		t.Fatalf("FinalizeOrphans (cascade): %v", err)
	}
	if pending != 1 || len(revoked) != 0 || len(api.secrets) != 1 {
		t.Fatalf("in-flight cascade must stay pending (pending=%d)", pending)
	}

	// Grace window: a fresh secret (mid-create signature) must never be treated as an orphan
	// even with no Application/TCP — pending, untouched.
	delete(api.tcps, "st-p1/"+spec.ID)
	meta := api.secretObjs["st-p1/"+CloudSecretName(spec.ID)]["metadata"].(map[string]any)
	meta["creationTimestamp"] = time.Now().UTC().Format(time.RFC3339)
	pending, err = svc.FinalizeOrphans(ctx, "p1", revoke)
	if err != nil {
		t.Fatalf("FinalizeOrphans (grace): %v", err)
	}
	if pending != 1 || len(revoked) != 0 || len(api.secrets) != 1 {
		t.Fatalf("fresh secret must ride out the grace window (pending=%d)", pending)
	}

	// Cascade done (TCP gone, secret old) → appcred revoked (annotations incl. minting service),
	// secret deleted, and the now-empty managed namespace GC'd.
	meta["creationTimestamp"] = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if _, err := svc.FinalizeOrphans(ctx, "p1", revoke); err != nil {
		t.Fatalf("FinalizeOrphans (done): %v", err)
	}
	if len(revoked) != 1 || revoked[0] != "svc-os/user-1/cred-1" {
		t.Errorf("revoked = %v", revoked)
	}
	if len(api.secrets) != 0 {
		t.Error("orphan secret not reaped")
	}
	if _, ok := api.namespaces["st-p1"]; ok {
		t.Error("empty managed namespace not GC'd")
	}

	// The service-level sweep sees the same world (labels carry service+project) — a second
	// cluster's leftovers finalize through FinalizeAllOrphans without any project doc.
	spec2 := testSpec()
	spec2.ID = "stc-second01"
	spec2.AppCredID, spec2.AppCredUserID, spec2.AppCredServiceID = "cred-2", "user-1", "svc-os"
	if _, err := svc.CreateCluster(ctx, spec2, client.Config{AuthURL: "https://k/v3", Username: "u", Password: "p", ProjectID: "ext-1", Region: "az1"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteCluster(ctx, "p1", spec2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.FinalizeAllOrphans(ctx, revoke); err != nil {
		t.Fatalf("FinalizeAllOrphans: %v", err)
	}
	if len(revoked) != 2 || revoked[1] != "svc-os/user-1/cred-2" {
		t.Errorf("sweep revoked = %v", revoked)
	}
	if len(api.secrets) != 0 {
		t.Error("sweep left the orphan secret")
	}

	// Fail-closed: a failing revoker keeps the secret (the only revocation record).
	spec3 := testSpec()
	spec3.ID = "stc-third002"
	spec3.AppCredID, spec3.AppCredUserID, spec3.AppCredServiceID = "cred-3", "user-1", "svc-os"
	if _, err := svc.CreateCluster(ctx, spec3, client.Config{AuthURL: "https://k/v3", Username: "u", Password: "p", ProjectID: "ext-1", Region: "az1"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteCluster(ctx, "p1", spec3.ID); err != nil {
		t.Fatal(err)
	}
	failing := func(_ context.Context, _, _, _ string) error { return context.DeadlineExceeded }
	pending, err = svc.FinalizeOrphans(ctx, "p1", failing)
	if err == nil || pending != 1 {
		t.Errorf("failing revoker: want pending=1 + error, got pending=%d err=%v", pending, err)
	}
	if len(api.secrets) != 1 {
		t.Error("failing revoker must keep the revocation record")
	}
}

func TestValidateUpgradePath(t *testing.T) {
	ok := [][2]string{{"1.34.2", "1.34.5"}, {"1.34.2", "1.35.0"}, {"v1.34.2", "1.35.4"}}
	for _, c := range ok {
		if err := ValidateUpgradePath(c[0], c[1]); err != nil {
			t.Errorf("%s → %s: %v", c[0], c[1], err)
		}
	}
	bad := [][2]string{
		{"1.35.4", "1.35.4"}, // same version
		{"1.35.4", "1.34.2"}, // minor downgrade
		{"1.35.4", "1.35.1"}, // patch downgrade
		{"1.33.0", "1.35.0"}, // two-minor jump
		{"1.35.4", "2.0.0"},  // major change
		{"junk", "1.35.4"},   // unparseable
	}
	for _, c := range bad {
		if err := ValidateUpgradePath(c[0], c[1]); err == nil {
			t.Errorf("%s → %s: want error", c[0], c[1])
		}
	}
}

func TestOwnershipGuards(t *testing.T) {
	api := newFakeAPI()
	svc := NewWithAPI(api, testCfg(), "svc-1")
	ctx := context.Background()
	// An Application NOT created by stratos (no managed-by label) — delete and patch must refuse.
	api.apps["argocd/legacy"] = map[string]any{
		"metadata": map[string]any{"name": "legacy", "namespace": "argocd"},
		"spec":     map[string]any{"source": map[string]any{"helm": map[string]any{"valuesObject": map[string]any{}}}},
	}
	if err := svc.DeleteCluster(ctx, "p1", "legacy"); err == nil || !strings.Contains(err.Error(), "not managed by stratos") {
		t.Errorf("DeleteCluster unmanaged: %v", err)
	}
	if _, ok := api.apps["argocd/legacy"]; !ok {
		t.Fatal("unmanaged application was deleted")
	}
	if err := svc.PatchClusterValues(ctx, "legacy", func(map[string]any) error { return nil }); err == nil || !strings.Contains(err.Error(), "not managed by stratos") {
		t.Errorf("PatchClusterValues unmanaged: %v", err)
	}
}

func TestAdminKubeconfig(t *testing.T) {
	api := newFakeAPI()
	svc := NewWithAPI(api, testCfg(), "svc-1")
	ctx := context.Background()

	// TCP named differently from the cluster id → resolved via the instance-label list fallback.
	api.tcps["st-p1/stc-x-openstack-kamaji-cluster"] = map[string]any{
		"metadata": map[string]any{
			"name":   "stc-x-openstack-kamaji-cluster",
			"labels": map[string]any{"app.kubernetes.io/instance": "stc-x"},
		},
	}
	api.secrets["st-p1/stc-x-openstack-kamaji-cluster-admin-kubeconfig"] = map[string]string{"admin.conf": "KUBECONFIG"}

	kc, err := svc.AdminKubeconfig(ctx, "p1", "stc-x")
	if err != nil {
		t.Fatalf("AdminKubeconfig: %v", err)
	}
	if string(kc) != "KUBECONFIG" {
		t.Errorf("kubeconfig = %q", kc)
	}
	// Never persisted anywhere by the service — nothing to assert beyond the fetch itself (plan D5).

	if _, err := svc.AdminKubeconfig(ctx, "p1", "stc-none"); err == nil {
		t.Error("absent cluster: want error")
	}
}

func TestPatchClusterValuesUpgrade(t *testing.T) {
	api := newFakeAPI()
	cfg := testCfg()
	svc := NewWithAPI(api, cfg, "svc-1")
	ctx := context.Background()
	spec := testSpec()
	if _, err := svc.CreateCluster(ctx, spec, client.Config{AuthURL: "https://k/v3", Username: "u", Password: "p", Region: "az1"}); err != nil {
		t.Fatal(err)
	}

	err := svc.PatchClusterValues(ctx, spec.ID, func(values map[string]any) error {
		values["kubernetesVersion"] = "1.34.2"
		return nil
	})
	if err != nil {
		t.Fatalf("PatchClusterValues: %v", err)
	}
	app := api.apps["argocd/"+spec.ID]
	src := app["spec"].(map[string]any)["source"].(map[string]any)
	if src["targetRevision"] != "0.2.3" {
		t.Errorf("chart pin changed: %v", src["targetRevision"])
	}
	values := src["helm"].(map[string]any)["valuesObject"].(map[string]any)
	if values["kubernetesVersion"] != "1.34.2" {
		t.Errorf("version = %v", values["kubernetesVersion"])
	}

	if err := svc.PatchClusterValues(ctx, "stc-none", func(map[string]any) error { return nil }); err == nil {
		t.Error("absent cluster: want error")
	}
}

func TestSyncProviderList(t *testing.T) {
	api := newFakeAPI()
	cfg := testCfg()
	svc := NewWithAPI(api, cfg, "svc-1")
	ctx := context.Background()
	spec := testSpec()
	if _, err := svc.CreateCluster(ctx, spec, client.Config{AuthURL: "https://k/v3", Username: "u", Password: "p", Region: "az1"}); err != nil {
		t.Fatal(err)
	}
	// Enrich the fake with live status: argo health + TCP endpoint + one MD.
	app := api.apps["argocd/"+spec.ID]
	app["status"] = map[string]any{
		"health": map[string]any{"status": "Healthy"},
		"sync":   map[string]any{"status": "Synced"},
	}
	app["metadata"].(map[string]any)["creationTimestamp"] = "2026-07-12T00:00:00Z"
	api.tcps["st-p1/"+spec.ID] = map[string]any{
		"metadata": map[string]any{"name": spec.ID},
		"status":   map[string]any{"controlPlaneEndpoint": "10.0.0.5:6443"},
	}
	api.mds = []map[string]any{{
		"metadata": map[string]any{"name": spec.ID + "-workers"},
		"status":   map[string]any{"replicas": float64(3), "readyReplicas": float64(2), "phase": "ScalingUp"},
	}}

	// A PRE-STRATOS cluster on the same management cluster (same project label but NO managed-by
	// marker — worst case) must never enter the cache (decision 2026-07-12).
	api.apps["argocd/legacy-cluster"] = map[string]any{
		"metadata": map[string]any{
			"name":   "legacy-cluster",
			"labels": map[string]any{LabelProject: "p1"},
		},
		"spec": map[string]any{"source": map[string]any{}},
	}

	list, err := svc.SyncProvider("az1", "p1").List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("resources = %d (unmanaged cluster must be invisible)", len(list))
	}
	cr := list[0]
	if cr.Type != "KUBERNETES_CLUSTER" || cr.ExternalID != spec.ID || cr.Region != "az1" || cr.ProjectID != "p1" {
		t.Errorf("resource identity = %+v", cr)
	}
	c := cr.Data["cluster"].(map[string]any)
	if c["status"] != "READY" || c["sync_status"] != "Synced" {
		t.Errorf("status = %v / %v", c["status"], c["sync_status"])
	}
	if c["endpoint"] != "10.0.0.5:6443" {
		t.Errorf("endpoint = %v", c["endpoint"])
	}
	if c["created_at"] != "2026-07-12T00:00:00Z" {
		t.Errorf("created_at = %v", c["created_at"])
	}
	groups := c["node_groups"].([]any)
	g0 := groups[0].(map[string]any)
	if g0["ready_replicas"] != float64(2) || g0["phase"] != "ScalingUp" {
		t.Errorf("live merge = %v", g0)
	}
}
