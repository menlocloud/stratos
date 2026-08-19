package dbaas

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/menlocloud/stratos/internal/cloud/kamajik8s"
)

// fakeAPI implements K8sAPI in memory, recording op order (create-ordering assertions) and
// modelling the two SSA semantics the real client has: Secret.type is IMMUTABLE (ApplySecret
// hardcodes Opaque — a re-apply onto an operator-minted basic-auth secret must 422) and the
// label set is REPLACED per apply (same-field-manager retraction). ListSecrets* honor the
// label selector — a typo'd sweep selector must fail tests, not silently no-op in prod.
type fakeAPI struct {
	namespaces  map[string]map[string]any
	secrets     map[string]map[string]any // key ns/name → full object
	secretData  map[string]map[string][]byte
	secretTypes map[string]string // key ns/name → Secret.type
	netpols     map[string]map[string]any
	apps        map[string]map[string]any            // key ns/name
	services    map[string]map[string]any            // key ns/name
	vpas        map[string]map[string]any            // key ns/name
	crs         map[string]map[string]map[string]any // plural → ns/name → object
	pods        map[string]map[string]any            // key ns/name
	ops         []string
	failApply   bool
	failDelete  map[string]bool // secret names whose DeleteSecret fails
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		namespaces:  map[string]map[string]any{},
		secrets:     map[string]map[string]any{},
		secretData:  map[string]map[string][]byte{},
		secretTypes: map[string]string{},
		netpols:     map[string]map[string]any{},
		apps:        map[string]map[string]any{},
		services:    map[string]map[string]any{},
		vpas:        map[string]map[string]any{},
		crs:         map[string]map[string]map[string]any{},
		pods:        map[string]map[string]any{},
		failDelete:  map[string]bool{},
	}
}

func key(ns, name string) string { return ns + "/" + name }

func (f *fakeAPI) EnsureNamespace(_ context.Context, name string, labels map[string]string) error {
	f.ops = append(f.ops, "ns:"+name)
	l := map[string]any{}
	for k, v := range labels {
		l[k] = v
	}
	f.namespaces[name] = map[string]any{"metadata": map[string]any{"name": name, "labels": l}}
	return nil
}
func (f *fakeAPI) GetNamespace(_ context.Context, name string) (map[string]any, error) {
	return f.namespaces[name], nil
}
func (f *fakeAPI) DeleteNamespace(_ context.Context, name string) error {
	f.ops = append(f.ops, "delns:"+name)
	delete(f.namespaces, name)
	return nil
}
func (f *fakeAPI) ApplySecret(_ context.Context, ns, name string, stringData map[string]string, labels, annotations map[string]string) error {
	f.ops = append(f.ops, "secret:"+name)
	k := key(ns, name)
	// The real ApplySecret SSA-applies type Opaque; the apiserver rejects a type change.
	if existing, ok := f.secretTypes[k]; ok && existing != "Opaque" {
		return &kamajik8s.APIError{Status: 422, Message: "Secret.type: field is immutable"}
	}
	obj := f.secrets[k]
	if obj == nil {
		meta := map[string]any{"name": name, "namespace": ns,
			"creationTimestamp": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)}
		obj = map[string]any{"metadata": meta}
		f.secrets[k] = obj
		f.secretData[k] = map[string][]byte{}
	}
	f.secretTypes[k] = "Opaque"
	meta := obj["metadata"].(map[string]any)
	// SSA same-manager semantics: the label/annotation SETS are replaced, absent = retracted.
	l := map[string]any{}
	for lk, lv := range labels {
		l[lk] = lv
	}
	meta["labels"] = l
	a := map[string]any{}
	for ak, av := range annotations {
		a[ak] = av
	}
	meta["annotations"] = a
	for dk, dv := range stringData {
		f.secretData[k][dk] = []byte(dv)
	}
	return nil
}
func (f *fakeAPI) PatchSecretData(_ context.Context, ns, name string, stringData map[string]string) error {
	f.ops = append(f.ops, "patchsecret:"+name)
	k := key(ns, name)
	if _, ok := f.secrets[k]; !ok {
		return &kamajik8s.APIError{Status: 404, Message: "not found"}
	}
	for dk, dv := range stringData {
		f.secretData[k][dk] = []byte(dv)
	}
	return nil
}
func (f *fakeAPI) ApplyNetworkPolicy(_ context.Context, np map[string]any) error {
	name := digStr(np, "metadata", "name")
	ns := digStr(np, "metadata", "namespace")
	f.ops = append(f.ops, "np:"+name)
	f.netpols[key(ns, name)] = np
	return nil
}
func (f *fakeAPI) GetSecretData(_ context.Context, ns, name string) (map[string][]byte, error) {
	d, ok := f.secretData[key(ns, name)]
	if !ok {
		return nil, nil
	}
	return d, nil
}
func (f *fakeAPI) listSecrets(ns, sel string) []map[string]any {
	var out []map[string]any
	for k, obj := range f.secrets {
		if (ns == "" || strings.HasPrefix(k, ns+"/")) && matchesSelector(obj, sel) {
			out = append(out, obj)
		}
	}
	return out
}
func (f *fakeAPI) ListSecrets(_ context.Context, ns, labelSelector string) ([]map[string]any, error) {
	return f.listSecrets(ns, labelSelector), nil
}
func (f *fakeAPI) ListSecretsAllNamespaces(_ context.Context, labelSelector string) ([]map[string]any, error) {
	return f.listSecrets("", labelSelector), nil
}
func (f *fakeAPI) DeleteSecret(_ context.Context, ns, name string) error {
	f.ops = append(f.ops, "delsecret:"+name)
	if f.failDelete[name] {
		return errors.New("transient apiserver error")
	}
	delete(f.secrets, key(ns, name))
	delete(f.secretData, key(ns, name))
	delete(f.secretTypes, key(ns, name))
	return nil
}
func (f *fakeAPI) ApplyApplication(_ context.Context, app map[string]any) error {
	if f.failApply {
		return errors.New("apply failed")
	}
	meta := app["metadata"].(map[string]any)
	name := meta["name"].(string)
	ns := meta["namespace"].(string)
	f.ops = append(f.ops, "app:"+name)
	if meta["creationTimestamp"] == nil {
		meta["creationTimestamp"] = time.Now().UTC().Format(time.RFC3339)
	}
	f.apps[key(ns, name)] = app
	return nil
}
func (f *fakeAPI) GetApplication(_ context.Context, ns, name string) (map[string]any, error) {
	return f.apps[key(ns, name)], nil
}
func (f *fakeAPI) ListApplications(_ context.Context, ns, labelSelector string) ([]map[string]any, error) {
	var out []map[string]any
	for k, app := range f.apps {
		if !strings.HasPrefix(k, ns+"/") {
			continue
		}
		if !matchesSelector(app, labelSelector) {
			continue
		}
		out = append(out, app)
	}
	return out, nil
}
func (f *fakeAPI) DeleteApplication(_ context.Context, ns, name string) error {
	f.ops = append(f.ops, "delapp:"+name)
	delete(f.apps, key(ns, name))
	return nil
}
func (f *fakeAPI) GetService(_ context.Context, ns, name string) (map[string]any, error) {
	return f.services[key(ns, name)], nil
}

func (f *fakeAPI) GetVPA(_ context.Context, ns, name string) (map[string]any, error) {
	return f.vpas[key(ns, name)], nil
}

// ListCRs serves the generic CR reads (backup lists). Keyed by plural so a test can seed
// backups without modelling every operator's CRD.
func (f *fakeAPI) ListCRs(_ context.Context, _, _, plural, ns, sel string) ([]map[string]any, error) {
	var out []map[string]any
	for k, obj := range f.crs[plural] {
		if strings.HasPrefix(k, ns+"/") && matchesSelector(obj, sel) {
			out = append(out, obj)
		}
	}
	return out, nil
}

// ListPods / PodLogs back the log reads. Pods are seeded per test; a name in failDelete
// doubles as "this pod's log read fails", which is how the partial-failure path is covered.
func (f *fakeAPI) ListPods(_ context.Context, ns, sel string) ([]map[string]any, error) {
	var out []map[string]any
	for k, obj := range f.pods {
		if strings.HasPrefix(k, ns+"/") && matchesSelector(obj, sel) {
			out = append(out, obj)
		}
	}
	return out, nil
}

func (f *fakeAPI) PodLogs(_ context.Context, ns, pod, container string, tail int) (string, error) {
	if f.failDelete[pod] {
		return "", errors.New("container is not running")
	}
	return fmt.Sprintf("log of %s/%s container=%s tail=%d", ns, pod, container, tail), nil
}

// matchesSelector honours the label selectors the Service actually uses (k=v comma lists).
func matchesSelector(obj map[string]any, sel string) bool {
	if sel == "" {
		return true
	}
	labels, _ := dig(obj, "metadata", "labels").(map[string]any)
	for _, part := range strings.Split(sel, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		if v, _ := labels[kv[0]].(string); v != kv[1] {
			return false
		}
	}
	return true
}

func testConfig() Config {
	return Config{
		Kubeconfig:     "kc",
		Region:         "RegionOne",
		ArgoNamespace:  "argocd",
		ArgoProject:    "stratos-dbaas",
		ChartRepo:      "ghcr.io/menlocloud/stratos-charts",
		ChartName:      "database-cluster",
		ChartVersion:   "0.1.0",
		OSServiceID:    "svc-os",
		OSProjectID:    "dbaas-tenant",
		MemberSubnetID: "member-subnet",
		StorageClasses: []string{"nvme"},
		Limits:         Limits{MaxCPU: 32, MaxMemoryGiB: 128, MaxStorageGiB: 2048},
		Engines: map[string]EngineOffer{
			EnginePostgreSQL: {Versions: []string{"16", "17"}, Default: "17", Replicas: []int{1, 2, 3}},
			EngineMySQL:      {Versions: []string{"8.4"}, Default: "8.4", Replicas: []int{1, 3}},
			EngineMariaDB:    {Versions: []string{"11.8"}, Default: "11.8", Replicas: []int{1, 3}},
			EngineValkey:     {Versions: []string{"9"}, Default: "9", Replicas: []int{1, 3}, Beta: true},
			EngineFerretDB:   {Versions: []string{"2"}, Default: "2", Replicas: []int{1, 2, 3}},
			EngineOpenSearch: {Versions: []string{"3.3.0"}, Default: "3.3.0", Replicas: []int{1, 3}},
			EngineKafka:      {Versions: []string{"4.2.0", "4.3.0"}, Default: "4.3.0", Replicas: []int{1, 3}},
		},
	}
}

func testSpec(engine, version string) DatabaseSpec {
	return DatabaseSpec{
		ID: "std-abc12345", DisplayName: "my db", ProjectID: "p1",
		Engine: engine, Version: version, Replicas: 1,
		CPU: 2, MemoryGiB: 4, StorageGiB: 20,
		NetworkID: "net-1", SubnetID: "sub-1",
		AllowedCIDRs: []string{"10.1.0.0/24"},
	}
}

func testShare() NetShare {
	return NetShare{NetworkID: "net-1", SubnetID: "sub-1", OSServiceID: "svc-os", OSProjectID: "dbaas-tenant", OSRegion: "RegionOne"}
}

func TestSpecValidate(t *testing.T) {
	cfg := testConfig()
	if err := testSpec(EnginePostgreSQL, "17").Validate(cfg); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	cases := map[string]DatabaseSpec{}
	s := testSpec(EnginePostgreSQL, "17")
	s.Engine = "oracle"
	cases["unknown engine"] = s
	s = testSpec(EnginePostgreSQL, "99")
	cases["version off catalog"] = s
	s = testSpec(EnginePostgreSQL, "17")
	s.Replicas = 5
	cases["replicas off catalog"] = s
	s = testSpec(EnginePostgreSQL, "17")
	s.DashboardsEnabled = true
	cases["dashboards on non-opensearch"] = s
	s = testSpec(EnginePostgreSQL, "17")
	s.CPU = 64
	cases["cpu over limit"] = s
	s = testSpec(EnginePostgreSQL, "17")
	s.StorageGiB = 0
	cases["zero storage"] = s
	s = testSpec(EnginePostgreSQL, "17")
	s.SubnetID = ""
	cases["missing subnet"] = s
	s = testSpec(EnginePostgreSQL, "17")
	s.StorageClass = "hdd"
	cases["storage class off allowlist"] = s
	s = testSpec(EnginePostgreSQL, "17")
	s.AllowedCIDRs = []string{"nope"}
	cases["bad cidr"] = s
	s = testSpec(EngineValkey, "9")
	cases["beta without ack"] = s
	for name, spec := range cases {
		if err := spec.Validate(cfg); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
	// Beta ack lets valkey through.
	vk := testSpec(EngineValkey, "9")
	vk.BetaAck = true
	if err := vk.Validate(cfg); err != nil {
		t.Errorf("beta ack rejected: %v", err)
	}
}

func TestNames(t *testing.T) {
	// The prefix is a boundary, not a name. It must differ from kamaji's `st-` — the two products
	// share a cluster — and it must not MATCH the glob `st-*`, which is the destination
	// constraint on kamaji's ArgoCD AppProject. Twinned with deploy/dbaas-cluster/appproject.yaml
	// (namespace: "stdb-*"); changing one without the other breaks every database sync.
	if NamespaceFor("p1") != "stdb-p1" {
		t.Fatal("NamespaceFor")
	}
	if LBServiceName("std-x") != "std-x-lb" {
		t.Fatal("LBServiceName")
	}
	if AuthSecretName("std-x") != "std-x-auth" {
		t.Fatal("AuthSecretName")
	}
	if NetShareSecretName("std-x") != "std-x-net-share" {
		t.Fatal("NetShareSecretName")
	}
	// '_' is a legal ident character but not a legal k8s-name character; the mapping is what
	// keeps a `keycloak_stag` user from 422-ing every derived Secret/CR (issue #168). '-' cannot
	// appear in an ident, so the mapping cannot collide two idents.
	if K8sName("keycloak_stag") != "keycloak-stag" || K8sName("alice") != "alice" {
		t.Fatal("K8sName")
	}
	if UserSecretName("std-x", "keycloak_stag") != "std-x-u-keycloak-stag" {
		t.Fatal("UserSecretName must be a legal k8s name")
	}
}

// TestPodSelectorFor pins the per-engine pod ownership label — the derivation twin of the chart's
// networkpolicy.yaml podSelector switch. app.kubernetes.io/instance matched NOTHING for the three
// operators that stamp their own cluster label, which surfaced as issue #170: a healthy valkey's
// Logs tab returned an internal error because the pod list came back empty.
func TestPodSelectorFor(t *testing.T) {
	cases := map[string]string{
		EnginePostgreSQL: "app.kubernetes.io/instance=std-1",
		EngineMySQL:      "app.kubernetes.io/instance=std-1",
		EngineMariaDB:    "app.kubernetes.io/instance=std-1",
		EngineFerretDB:   "app.kubernetes.io/instance=std-1",
		EngineValkey:     "valkey.io/cluster=std-1",
		EngineOpenSearch: "opensearch.org/opensearch-cluster=std-1",
		EngineKafka:      "strimzi.io/cluster=std-1",
	}
	for engine, want := range cases {
		if got := PodSelectorFor(engine, "std-1"); got != want {
			t.Errorf("%s: selector %q, want %q", engine, got, want)
		}
	}
}

// TestLogs drives the log read end to end against pods labelled the way each operator actually
// labels them. The valkey pod deliberately carries app.kubernetes.io/instance in the operator's
// PER-NODE form — the exact shape that made the old selector match nothing.
func TestLogs(t *testing.T) {
	api := newFakeAPI()
	s := NewWithAPI(api, testConfig(), "svc-dbaas")
	ns := NamespaceFor("p1")
	seed := func(name string, labels map[string]any) {
		api.pods[key(ns, name)] = map[string]any{
			"metadata": map[string]any{"name": name, "labels": labels},
		}
	}
	seed("std-v-0-0", map[string]any{
		"valkey.io/cluster":           "std-v",
		"app.kubernetes.io/instance": "std-v-0-0", // per-node, NOT the cluster id
	})
	seed("std-p-1", map[string]any{"app.kubernetes.io/instance": "std-p"})
	// A sibling database in the same namespace must never leak into the read.
	seed("std-x-1", map[string]any{"app.kubernetes.io/instance": "std-x"})

	logs, err := s.Logs(context.Background(), "p1", "std-v", EngineValkey, 100)
	if err != nil {
		t.Fatalf("valkey logs: %v", err)
	}
	if len(logs) != 1 || logs[0]["pod"] != "std-v-0-0" || logs[0]["container"] != "valkey" {
		t.Fatalf("valkey logs = %v", logs)
	}
	logs, err = s.Logs(context.Background(), "p1", "std-p", EnginePostgreSQL, 100)
	if err != nil || len(logs) != 1 || logs[0]["pod"] != "std-p-1" {
		t.Fatalf("postgres logs = %v (%v)", logs, err)
	}
}

// TestBuildValues pins EVERY chart-values key per engine — the chart-drift tripwire (the
// database-cluster chart's values.yaml is the other half of this contract).
func TestBuildValues(t *testing.T) {
	cfg := testConfig()
	for _, engine := range []string{EnginePostgreSQL, EngineMySQL, EngineMariaDB, EngineValkey, EngineFerretDB} {
		spec := testSpec(engine, cfg.Engines[engine].Default)
		spec.StorageClass = "nvme"
		v := BuildValues(cfg, spec)
		want := map[string]any{
			"engine":        engine,
			"engineVersion": cfg.Engines[engine].Default,
			"instances":     1,
		}
		for k, w := range want {
			if v[k] != w {
				t.Errorf("%s: values[%q] = %v, want %v", engine, k, v[k], w)
			}
		}
		res := v["resources"].(map[string]any)
		if res["cpu"] != 2 || res["memoryGi"] != 4 {
			t.Errorf("%s: resources = %v", engine, res)
		}
		st := v["storage"].(map[string]any)
		if st["sizeGi"] != 20 || st["storageClassName"] != "nvme" {
			t.Errorf("%s: storage = %v", engine, st)
		}
		net := v["network"].(map[string]any)
		if net["networkId"] != "net-1" || net["subnetId"] != "sub-1" || net["memberSubnetId"] != "member-subnet" {
			t.Errorf("%s: network = %v", engine, net)
		}
		if cidrs := net["allowedCidrs"].([]any); len(cidrs) != 1 || cidrs[0] != "10.1.0.0/24" {
			t.Errorf("%s: allowedCidrs = %v", engine, net["allowedCidrs"])
		}
		strat := v["stratos"].(map[string]any)
		if strat["projectId"] != "p1" || strat["resourceId"] != spec.ID || strat["displayName"] != "my db" {
			t.Errorf("%s: stratos = %v", engine, strat)
		}
		// The complete top-level key set — a new key must be added HERE and in the chart.
		for k := range v {
			switch k {
			case "engine", "engineVersion", "instances", "resources", "storage", "network", "stratos":
			default:
				t.Errorf("%s: unexpected top-level values key %q", engine, k)
			}
		}
		// No secret-looking material may ever enter values (Application CRs are argocd-readable).
		if strings.Contains(strings.ToLower(fmt.Sprint(v)), "password") {
			t.Errorf("%s: values leak a password-like key", engine)
		}
	}
	// Default storage class stays absent (chart falls back to the cluster default).
	v := BuildValues(cfg, testSpec(EnginePostgreSQL, "17"))
	if _, has := v["storage"].(map[string]any)["storageClassName"]; has {
		t.Error("empty storage class must be omitted")
	}
	// Dashboards: opensearch-only opt-in block; absent otherwise (SET_SSO patches it later,
	// an unconditional key would fight that patch).
	os := testSpec(EngineOpenSearch, "3.3.0")
	if _, has := BuildValues(cfg, os)["opensearch"]; has {
		t.Error("opensearch key must be absent when dashboards are off")
	}
	os.DashboardsEnabled = true
	dash := BuildValues(cfg, os)["opensearch"].(map[string]any)["dashboards"].(map[string]any)
	if dash["enabled"] != true {
		t.Errorf("dashboards block = %v, want enabled true", dash)
	}
	// Create-time SSO implies Dashboards (the chart reads sso only underneath them) — the same
	// rule SET_SSO applies later, pinned here so create and the action cannot drift.
	ssoSpec := testSpec(EngineOpenSearch, "3.3.0")
	ssoSpec.SSO = map[string]any{"connectUrl": "https://idp/.well-known", "clientId": "dash"}
	osv2 := BuildValues(cfg, ssoSpec)["opensearch"].(map[string]any)
	if got := osv2["dashboards"].(map[string]any)["enabled"]; got != true {
		t.Errorf("sso at create must enable dashboards, got %v", got)
	}
	if got := osv2["sso"].(map[string]any); got["enabled"] != true || got["clientId"] != "dash" {
		t.Errorf("sso block = %v", got)
	}
	// DNS/TLS: no zone (testConfig) = no dnsZone/certIssuer keys anywhere; zone+issuer = both
	// emitted (network.dnsZone for every engine, opensearch.certIssuer for opensearch).
	if _, has := BuildValues(cfg, testSpec(EnginePostgreSQL, "17"))["network"].(map[string]any)["dnsZone"]; has {
		t.Error("network.dnsZone must be absent without a provider zone")
	}
	dnsCfg := cfg
	dnsCfg.DNSZone, dnsCfg.CertIssuer = "db.example.com", "letsencrypt-dns"
	if z := BuildValues(dnsCfg, testSpec(EnginePostgreSQL, "17"))["network"].(map[string]any)["dnsZone"]; z != "db.example.com" {
		t.Errorf("network.dnsZone = %v, want db.example.com", z)
	}
	osv := BuildValues(dnsCfg, testSpec(EngineOpenSearch, "3.3.0"))["opensearch"].(map[string]any)
	if osv["certIssuer"] != "letsencrypt-dns" {
		t.Errorf("opensearch.certIssuer = %v, want letsencrypt-dns", osv["certIssuer"])
	}
	if _, has := BuildValues(dnsCfg, testSpec(EngineMySQL, "8.4"))["opensearch"]; has {
		t.Error("opensearch block must stay opensearch-only")
	}
	if h := dnsCfg.HostnameFor("std-abc"); h != "std-abc.db.example.com" {
		t.Errorf("HostnameFor = %q", h)
	}
	if h := cfg.HostnameFor("std-abc"); h != "" {
		t.Errorf("HostnameFor without zone = %q, want empty", h)
	}
	// PublicHost is the ONE spelling rule the cache and the connection panel share.
	for _, tc := range []struct{ name, custom, vip, want string }{
		{"no vip yet", "", "", ""},
		{"custom domain wins", "search.acme.com", "10.0.0.5", "search.acme.com"},
		{"platform name beats vip", "", "10.0.0.5", "std-abc.db.example.com"},
		{"custom beats platform even with a zone", "x.acme.com", "10.0.0.5", "x.acme.com"},
	} {
		if got := dnsCfg.PublicHost("std-abc", tc.custom, tc.vip); got != tc.want {
			t.Errorf("PublicHost(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
	// Without a zone the raw VIP is the endpoint — and "" stays "" so the billing gate holds.
	if got := cfg.PublicHost("std-abc", "", "10.0.0.5"); got != "10.0.0.5" {
		t.Errorf("PublicHost without zone = %q", got)
	}
	if got := cfg.PublicHost("std-abc", "search.acme.com", ""); got != "" {
		t.Errorf("PublicHost with no vip must stay empty, got %q", got)
	}
}

// TestReplicaChoices pins the unconfigured default (single node or the HA trio) and that an
// explicit offer wins.
func TestReplicaChoices(t *testing.T) {
	if got := (EngineOffer{}).ReplicaChoices(); !slices.Equal(got, []int{1, 3}) {
		t.Errorf("default choices = %v, want [1 3]", got)
	}
	if got := (EngineOffer{Replicas: []int{3}}).ReplicaChoices(); !slices.Equal(got, []int{3}) {
		t.Errorf("explicit choices = %v, want [3]", got)
	}
}

func TestBuildApplication(t *testing.T) {
	cfg := testConfig()
	spec := testSpec(EnginePostgreSQL, "17")
	app := BuildApplication(cfg, spec, "svc-dbaas", "", BuildValues(cfg, spec))
	if digStr(app, "metadata", "name") != spec.ID || digStr(app, "metadata", "namespace") != "argocd" {
		t.Fatal("metadata name/namespace")
	}
	fins, _ := dig(app, "metadata", "finalizers").([]any)
	// The BACKGROUND variant: ArgoCD's default foreground cascade livelocks against an operator
	// that recreates children while the parent is still visible (valkey-operator does).
	if len(fins) != 1 || fins[0] != "resources-finalizer.argocd.argoproj.io/background" {
		t.Fatal("background resources-finalizer missing — delete would not cascade, or would livelock")
	}
	if digStr(app, "spec", "project") != "stratos-dbaas" {
		t.Fatal("AppProject")
	}
	if digStr(app, "spec", "source", "targetRevision") != "0.1.0" {
		t.Fatal("chart pin fallback")
	}
	if digStr(app, "spec", "destination", "namespace") != NamespaceFor("p1") {
		t.Fatal("destination namespace")
	}
	labels, _ := dig(app, "metadata", "labels").(map[string]any)
	if labels[LabelProject] != "p1" || labels[LabelService] != "svc-dbaas" || labels[LabelManagedBy] != ManagedByValue {
		t.Fatal("labels")
	}
	// Empty ArgoProject falls back to the guardrail project, never ArgoCD's "default".
	cfg2 := cfg
	cfg2.ArgoProject = ""
	app2 := BuildApplication(cfg2, spec, "svc", "", nil)
	if digStr(app2, "spec", "project") != "stratos-dbaas" {
		t.Fatal("ArgoProject fallback")
	}
}

func TestServiceCreateDelete(t *testing.T) {
	api := newFakeAPI()
	s := NewWithAPI(api, testConfig(), "svc-dbaas")
	ctx := context.Background()

	// mariadb needs the stratos-owned auth secret; ordering = ns+default-deny → marker →
	// ensureShare → auth → Application (marker BEFORE the neutron share: a crash between them
	// converges via the sweep instead of leaking an unfindable share).
	spec := testSpec(EngineMariaDB, "11.8")
	shared := false
	data, err := s.CreateDatabase(ctx, spec, testShare(), func(context.Context) error {
		if got := strings.Join(api.ops, ","); !strings.Contains(got, "secret:"+NetShareSecretName(spec.ID)) {
			t.Fatalf("ensureShare ran before the marker was applied: %s", got)
		}
		shared = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !shared {
		t.Fatal("ensureShare not invoked")
	}
	wantOps := []string{"ns:" + NamespaceFor("p1"), "np:stratos-default-deny", "secret:" + NetShareSecretName(spec.ID), "secret:" + AuthSecretName(spec.ID), "app:" + spec.ID}
	if got := strings.Join(api.ops, ","); got != strings.Join(wantOps, ",") {
		t.Fatalf("create op order = %s, want %s", got, strings.Join(wantOps, ","))
	}
	if _, has := api.netpols[key(NamespaceFor("p1"), "stratos-default-deny")]; !has {
		t.Fatal("namespace default-deny must be stamped at ensure time")
	}
	marker := api.secrets[key(NamespaceFor("p1"), NetShareSecretName(spec.ID))]
	if digStr(marker, "metadata", "annotations", AnnotationNetworkID) != "net-1" ||
		digStr(marker, "metadata", "annotations", AnnotationOSProject) != "dbaas-tenant" ||
		digStr(marker, "metadata", "annotations", AnnotationOSRegion) != "RegionOne" {
		t.Fatal("net-share annotations are the revocation record — must be stamped (incl. region)")
	}
	// A failed ensureShare reaps the fresh marker and applies no Application.
	api3 := newFakeAPI()
	s3 := NewWithAPI(api3, testConfig(), "svc-dbaas")
	spec3 := testSpec(EngineMariaDB, "11.8")
	if _, err := s3.CreateDatabase(ctx, spec3, testShare(), func(context.Context) error {
		return errors.New("neutron down")
	}); err == nil {
		t.Fatal("ensureShare failure must fail the create")
	}
	if _, has := api3.secrets[key(NamespaceFor("p1"), NetShareSecretName(spec3.ID))]; has {
		t.Fatal("marker must be reaped when ensureShare fails")
	}
	if _, has := api3.apps[key("argocd", spec3.ID)]; has {
		t.Fatal("no Application may be applied when ensureShare fails")
	}
	db, _ := data["database"].(map[string]any)
	if db["status"] != "PENDING" || db["engine"] != EngineMariaDB {
		t.Fatalf("initial data = %v", db)
	}

	// postgres does NOT get an auth secret (CNPG mints its own).
	api2 := newFakeAPI()
	s2 := NewWithAPI(api2, testConfig(), "svc-dbaas")
	if _, err := s2.CreateDatabase(ctx, testSpec(EnginePostgreSQL, "17"), testShare(), nil); err != nil {
		t.Fatal(err)
	}
	if _, has := api2.secrets[key(NamespaceFor("p1"), AuthSecretName("std-abc12345"))]; has {
		t.Fatal("postgresql must not get a stratos auth secret")
	}

	// Delete removes only the Application; the marker stays (the sweep owns it).
	if err := s.DeleteDatabase(ctx, "p1", spec.ID); err != nil {
		t.Fatal(err)
	}
	if _, alive := api.apps[key("argocd", spec.ID)]; alive {
		t.Fatal("application not deleted")
	}
	if _, has := api.secrets[key(NamespaceFor("p1"), NetShareSecretName(spec.ID))]; !has {
		t.Fatal("net-share marker must survive the delete (revocation record)")
	}
}

func TestOwnershipGuards(t *testing.T) {
	api := newFakeAPI()
	s := NewWithAPI(api, testConfig(), "svc-dbaas")
	ctx := context.Background()
	// A foreign Application (no managed-by label) must refuse delete AND patch.
	api.apps[key("argocd", "std-foreign")] = map[string]any{
		"metadata": map[string]any{"name": "std-foreign", "namespace": "argocd"},
		"spec":     map[string]any{},
	}
	if err := s.DeleteDatabase(ctx, "p1", "std-foreign"); err == nil {
		t.Fatal("delete of unmanaged application must refuse")
	}
	if err := s.PatchDatabaseApp(ctx, "std-foreign", func(map[string]any) error { return nil }); err == nil {
		t.Fatal("patch of unmanaged application must refuse")
	}
}

func TestFinalizeOrphans(t *testing.T) {
	ctx := context.Background()
	mkMarker := func(api *fakeAPI, dbID string, age time.Duration) {
		k := key(NamespaceFor("p1"), NetShareSecretName(dbID))
		api.secrets[k] = map[string]any{"metadata": map[string]any{
			"name": NetShareSecretName(dbID), "namespace": NamespaceFor("p1"),
			"creationTimestamp": time.Now().UTC().Add(-age).Format(time.RFC3339),
			"labels":            map[string]any{LabelProject: "p1", LabelService: "svc-dbaas", LabelManagedBy: ManagedByValue},
			"annotations": map[string]any{
				AnnotationNetworkID: "net-1", AnnotationSubnetID: "sub-1",
				AnnotationOSService: "svc-os", AnnotationOSProject: "dbaas-tenant",
				AnnotationOSRegion: "RegionOne",
			},
		}}
		api.secretData[k] = map[string][]byte{"network-id": []byte("net-1")}
	}

	// Fresh marker (inside grace) → pending, untouched.
	api := newFakeAPI()
	s := NewWithAPI(api, testConfig(), "svc-dbaas")
	mkMarker(api, "std-new", time.Minute)
	pending, err := s.FinalizeOrphans(ctx, "p1", nil)
	if err != nil || pending != 1 {
		t.Fatalf("grace window: pending=%d err=%v", pending, err)
	}

	// Live Application → skipped, not pending.
	api = newFakeAPI()
	s = NewWithAPI(api, testConfig(), "svc-dbaas")
	mkMarker(api, "std-live", 2*time.Hour)
	api.apps[key("argocd", "std-live")] = map[string]any{"metadata": map[string]any{
		"name": "std-live", "namespace": "argocd",
		"labels": map[string]any{LabelProject: "p1", LabelManagedBy: ManagedByValue},
	}}
	if pending, err = s.FinalizeOrphans(ctx, "p1", nil); err != nil || pending != 0 {
		t.Fatalf("live app: pending=%d err=%v", pending, err)
	}
	if _, has := api.secrets[key(NamespaceFor("p1"), NetShareSecretName("std-live"))]; !has {
		t.Fatal("live database's marker must stay")
	}

	// LB Service still present → cascade in flight → pending.
	api = newFakeAPI()
	s = NewWithAPI(api, testConfig(), "svc-dbaas")
	mkMarker(api, "std-lb", 2*time.Hour)
	api.services[key(NamespaceFor("p1"), LBServiceName("std-lb"))] = map[string]any{"metadata": map[string]any{"name": LBServiceName("std-lb")}}
	if pending, err = s.FinalizeOrphans(ctx, "p1", nil); err != nil || pending != 1 {
		t.Fatalf("lb present: pending=%d err=%v", pending, err)
	}

	// Revoke fails → FAIL-CLOSED: marker stays, pending.
	api = newFakeAPI()
	s = NewWithAPI(api, testConfig(), "svc-dbaas")
	mkMarker(api, "std-x", 2*time.Hour)
	pending, err = s.FinalizeOrphans(ctx, "p1", func(_ context.Context, _, _, _, _ string) error {
		return errors.New("neutron 409")
	})
	if pending != 1 || err == nil {
		t.Fatalf("revoke failure must keep the marker: pending=%d err=%v", pending, err)
	}
	if _, has := api.secrets[key(NamespaceFor("p1"), NetShareSecretName("std-x"))]; !has {
		t.Fatal("marker deleted despite failed revoke")
	}

	// Sibling live database on the SAME network → revoke skipped, marker reaped, ns kept.
	api = newFakeAPI()
	s = NewWithAPI(api, testConfig(), "svc-dbaas")
	mkMarker(api, "std-x", 2*time.Hour)
	api.apps[key("argocd", "std-sibling")] = map[string]any{
		"metadata": map[string]any{"name": "std-sibling", "namespace": "argocd",
			"labels": map[string]any{LabelProject: "p1", LabelManagedBy: ManagedByValue}},
		"spec": map[string]any{"source": map[string]any{"helm": map[string]any{"valuesObject": map[string]any{
			"network": map[string]any{"networkId": "net-1"},
		}}}},
	}
	revoked := false
	pending, err = s.FinalizeOrphans(ctx, "p1", func(_ context.Context, _, _, _, _ string) error {
		revoked = true
		return nil
	})
	if err != nil || pending != 0 {
		t.Fatalf("sibling: pending=%d err=%v", pending, err)
	}
	if revoked {
		t.Fatal("revoke must be skipped while a sibling database rides the network")
	}
	if _, has := api.secrets[key(NamespaceFor("p1"), NetShareSecretName("std-x"))]; has {
		t.Fatal("finished-cascade marker must be reaped")
	}

	// Sibling MID-CREATE (fresh marker, Application not applied yet) on the same network →
	// revoke skipped, orphan marker still reaped (the sibling's own marker records the share).
	api = newFakeAPI()
	s = NewWithAPI(api, testConfig(), "svc-dbaas")
	mkMarker(api, "std-x", 2*time.Hour)
	mkMarker(api, "std-fresh", time.Minute)
	revoked = false
	pending, err = s.FinalizeOrphans(ctx, "p1", func(_ context.Context, _, _, _, _ string) error {
		revoked = true
		return nil
	})
	// std-fresh itself counts pending (grace window); the orphan std-x must reap WITHOUT revoke.
	if err != nil || pending != 1 {
		t.Fatalf("mid-create sibling: pending=%d err=%v", pending, err)
	}
	if revoked {
		t.Fatal("revoke must be skipped while a mid-create sibling's marker holds the network")
	}
	if _, has := api.secrets[key(NamespaceFor("p1"), NetShareSecretName("std-x"))]; has {
		t.Fatal("orphan marker must be reaped even when the revoke is skipped")
	}

	// Auth-secret delete failure → marker STAYS (it is the only retry driver), pending.
	api = newFakeAPI()
	s = NewWithAPI(api, testConfig(), "svc-dbaas")
	mkMarker(api, "std-x", 2*time.Hour)
	api.secrets[key(NamespaceFor("p1"), AuthSecretName("std-x"))] = map[string]any{"metadata": map[string]any{
		"name": AuthSecretName("std-x"), "namespace": NamespaceFor("p1")}}
	api.failDelete[AuthSecretName("std-x")] = true
	pending, err = s.FinalizeOrphans(ctx, "p1", func(_ context.Context, _, _, _, _ string) error { return nil })
	if pending != 1 || err == nil {
		t.Fatalf("auth delete failure: pending=%d err=%v", pending, err)
	}
	if _, has := api.secrets[key(NamespaceFor("p1"), NetShareSecretName("std-x"))]; !has {
		t.Fatal("marker must survive an auth-secret delete failure (retry driver)")
	}

	// Clean orphan → revoke called with the recorded ids, marker + auth reaped, ns GC'd.
	api = newFakeAPI()
	s = NewWithAPI(api, testConfig(), "svc-dbaas")
	api.namespaces[NamespaceFor("p1")] = map[string]any{"metadata": map[string]any{
		"name": NamespaceFor("p1"), "labels": map[string]any{LabelManagedBy: ManagedByValue}}}
	mkMarker(api, "std-x", 2*time.Hour)
	api.secrets[key(NamespaceFor("p1"), AuthSecretName("std-x"))] = map[string]any{"metadata": map[string]any{
		"name": AuthSecretName("std-x"), "namespace": NamespaceFor("p1")}}
	var gotSvc, gotProj, gotRegion, gotNet string
	pending, err = s.FinalizeAllOrphans(ctx, func(_ context.Context, osSvc, osProj, osRegion, net string) error {
		gotSvc, gotProj, gotRegion, gotNet = osSvc, osProj, osRegion, net
		return nil
	})
	if err != nil || pending != 0 {
		t.Fatalf("clean orphan: pending=%d err=%v", pending, err)
	}
	if gotSvc != "svc-os" || gotProj != "dbaas-tenant" || gotRegion != "RegionOne" || gotNet != "net-1" {
		t.Fatalf("revoke got (%s,%s,%s,%s)", gotSvc, gotProj, gotRegion, gotNet)
	}
	if _, has := api.secrets[key(NamespaceFor("p1"), AuthSecretName("std-x"))]; has {
		t.Fatal("auth secret must be reaped with the marker")
	}
	if _, has := api.namespaces[NamespaceFor("p1")]; has {
		t.Fatal("emptied managed namespace must be GC'd")
	}
}

func TestSyncProviderList(t *testing.T) {
	api := newFakeAPI()
	cfg := testConfig()
	s := NewWithAPI(api, cfg, "svc-dbaas")
	ctx := context.Background()
	spec := testSpec(EnginePostgreSQL, "17")
	if _, err := s.CreateDatabase(ctx, spec, testShare(), nil); err != nil {
		t.Fatal(err)
	}
	app := api.apps[key("argocd", spec.ID)]
	app["status"] = map[string]any{
		"health": map[string]any{"status": "Healthy"},
		"sync":   map[string]any{"status": "Synced"},
	}
	api.services[key(NamespaceFor("p1"), LBServiceName(spec.ID))] = map[string]any{
		"status": map[string]any{"loadBalancer": map[string]any{"ingress": []any{map[string]any{"ip": "10.1.0.5"}}}},
	}
	// A terminating sibling must NOT re-enter the cache.
	api.apps[key("argocd", "std-dying")] = map[string]any{
		"metadata": map[string]any{"name": "std-dying", "namespace": "argocd",
			"deletionTimestamp": "2026-01-01T00:00:00Z",
			"labels":            map[string]any{LabelProject: "p1", LabelManagedBy: ManagedByValue}},
		"spec": map[string]any{},
	}
	rows, err := s.SyncProvider("RegionOne", "p1").List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d (terminating app must be skipped)", len(rows))
	}
	db, _ := rows[0].Data["database"].(map[string]any)
	if db["status"] != "READY" || db["endpoint"] != "10.1.0.5" || db["port"] != 5432 {
		t.Fatalf("data = %v", db)
	}
	if db["engine"] != EnginePostgreSQL || db["version"] != "17" || db["replicas"] != 1 {
		t.Fatalf("spec read-back = %v", db)
	}
	if rows[0].Type != "DATABASE_CLUSTER" || rows[0].ExternalID != spec.ID || rows[0].Region != "RegionOne" {
		t.Fatalf("row = %+v", rows[0])
	}
	// Degraded maps through; missing health = PENDING.
	app["status"] = map[string]any{"health": map[string]any{"status": "Degraded"}}
	rows, _ = s.SyncProvider("RegionOne", "p1").List(ctx)
	if db, _ := rows[0].Data["database"].(map[string]any); db["status"] != "DEGRADED" {
		t.Fatal("Degraded map")
	}
	delete(app, "status")
	rows, _ = s.SyncProvider("RegionOne", "p1").List(ctx)
	if db, _ := rows[0].Data["database"].(map[string]any); db["status"] != "PENDING" {
		t.Fatal("no-health map")
	}
}

// TestConnectionSecretContract pins the per-engine secret tuple — the chart templates are the
// other half of this contract; a rename on either side must fail here.
func TestConnectionSecretContract(t *testing.T) {
	cases := []struct {
		engine, name, userKey, passKey, dbKey string
		port                                  int
		defUser, defDB                        string
	}{
		// The exposed postgres account is the SUPERUSER (chart sets enableSuperuserAccess); the
		// database name is fixed, not read off that secret.
		{EnginePostgreSQL, "std-1-superuser", "username", "password", "", 5432, "", "app"},
		{EngineFerretDB, "std-1-pg-app", "username", "password", "dbname", 27017, "", ""},
		{EngineMySQL, "std-1-secrets", "", "root", "", 3306, "root", ""},
		{EngineMariaDB, "std-1-auth", "", "password", "", 3306, "app", "app"},
		// valkey's exposed account is the `default` ACL user the chart wires to <id>-auth —
		// named, so the connection panel can show WHICH user authenticates.
		{EngineValkey, "std-1-auth", "", "password", "", 6379, "default", ""},
		{EngineOpenSearch, "std-1-admin-password", "username", "password", "", 9200, "", ""},
		{EngineKafka, "std-1-auth", "", "password", "", 9094, "std-1-app", ""},
	}
	for _, c := range cases {
		name, uk, pk, dk := ConnectionSecret(c.engine, "std-1")
		if name != c.name || uk != c.userKey || pk != c.passKey || dk != c.dbKey {
			t.Errorf("%s: got (%s,%s,%s,%s)", c.engine, name, uk, pk, dk)
		}
		if Port(c.engine) != c.port {
			t.Errorf("%s: port %d", c.engine, Port(c.engine))
		}
		if DefaultUser(c.engine, "std-1") != c.defUser || DefaultDB(c.engine) != c.defDB {
			t.Errorf("%s: defaults (%s,%s)", c.engine, DefaultUser(c.engine, "std-1"), DefaultDB(c.engine))
		}
	}
	// Databases predating the superuser switch keep working off the scoped app role.
	if n, uk, pk, dk := PriorConnectionSecret(EnginePostgreSQL, "std-1"); n != "std-1-app" || uk != "username" || pk != "password" || dk != "dbname" {
		t.Errorf("postgres fallback = (%s,%s,%s,%s)", n, uk, pk, dk)
	}
	for _, e := range []string{EngineMySQL, EngineMariaDB, EngineValkey, EngineOpenSearch, EngineKafka, EngineFerretDB} {
		if n, _, _, _ := PriorConnectionSecret(e, "std-1"); n != "" {
			t.Errorf("%s must have no fallback, got %s", e, n)
		}
	}
	if !NeedsAuthSecret(EngineMariaDB) || !NeedsAuthSecret(EngineValkey) || !NeedsAuthSecret(EngineKafka) ||
		NeedsAuthSecret(EnginePostgreSQL) || NeedsAuthSecret(EngineMySQL) || NeedsAuthSecret(EngineOpenSearch) {
		t.Fatal("NeedsAuthSecret set")
	}
	if LBServiceNameFor(EngineKafka, "std-1") != "std-1-kafka-external-bootstrap" ||
		LBServiceNameFor(EnginePostgreSQL, "std-1") != "std-1-lb" {
		t.Fatal("LBServiceNameFor")
	}
}

func TestConnectionInfo(t *testing.T) {
	api := newFakeAPI()
	s := NewWithAPI(api, testConfig(), "svc-dbaas")
	ctx := context.Background()
	spec := testSpec(EnginePostgreSQL, "17")
	if _, err := s.CreateDatabase(ctx, spec, testShare(), nil); err != nil {
		t.Fatal(err)
	}
	// Not ready: no secret yet.
	if _, err := s.ConnectionInfo(ctx, "p1", spec.ID); err == nil {
		t.Fatal("expected not-ready before the operator mints the secret")
	}
	// CNPG mints the app secret; LB gets its VIP.
	api.secretData[key(NamespaceFor("p1"), spec.ID+"-app")] = map[string][]byte{
		"username": []byte("app"), "password": []byte("s3cr3t"), "dbname": []byte("app"),
	}
	api.secrets[key(NamespaceFor("p1"), spec.ID+"-app")] = map[string]any{"metadata": map[string]any{"name": spec.ID + "-app"}}
	api.services[key(NamespaceFor("p1"), LBServiceName(spec.ID))] = map[string]any{
		"status": map[string]any{"loadBalancer": map[string]any{"ingress": []any{map[string]any{"ip": "10.1.0.9"}}}},
	}
	info, err := s.ConnectionInfo(ctx, "p1", spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info.Host != "10.1.0.9" || info.Port != 5432 || info.Username != "app" || info.Password != "s3cr3t" || info.DBName != "app" {
		t.Fatalf("info = %+v", info)
	}
	if info.URI != "postgresql://app:s3cr3t@10.1.0.9:5432/app" {
		t.Fatalf("uri = %s", info.URI)
	}
}

func TestResetPassword(t *testing.T) {
	api := newFakeAPI()
	s := NewWithAPI(api, testConfig(), "svc-dbaas")
	ctx := context.Background()
	spec := testSpec(EngineMariaDB, "11.8")
	if _, err := s.CreateDatabase(ctx, spec, testShare(), nil); err != nil {
		t.Fatal(err)
	}
	before := string(api.secretData[key(NamespaceFor("p1"), AuthSecretName(spec.ID))]["password"])
	pw, err := s.ResetPassword(ctx, "p1", spec.ID, EngineMariaDB)
	if err != nil {
		t.Fatal(err)
	}
	after := string(api.secretData[key(NamespaceFor("p1"), AuthSecretName(spec.ID))]["password"])
	if pw == "" || pw == before || after != pw {
		t.Fatalf("reset: before=%q pw=%q after=%q", before, pw, after)
	}
	// The rotation must be a DATA-ONLY patch: the stratos ownership labels applied at create
	// survive (an SSA re-apply under the same field manager would retract them).
	auth := api.secrets[key(NamespaceFor("p1"), AuthSecretName(spec.ID))]
	if digStr(auth, "metadata", "labels", LabelManagedBy) != ManagedByValue {
		t.Fatal("reset stripped the ownership labels off the stratos-owned auth secret")
	}
	if _, err := s.ResetPassword(ctx, "p1", spec.ID, "oracle"); err == nil {
		t.Fatal("unknown engine must refuse")
	}
}

// TestResetPasswordOperatorOwned pins the CNPG case that killed the SSA approach: the app
// secret is OPERATOR-minted with the immutable type kubernetes.io/basic-auth — the rotation
// must merge-patch only the password key (an ApplySecret would 422 on the type), and a
// not-yet-minted secret must read as "not ready", never be SSA-created under the operator.
func TestResetPasswordOperatorOwned(t *testing.T) {
	api := newFakeAPI()
	s := NewWithAPI(api, testConfig(), "svc-dbaas")
	ctx := context.Background()
	spec := testSpec(EnginePostgreSQL, "17")
	if _, err := s.CreateDatabase(ctx, spec, testShare(), nil); err != nil {
		t.Fatal(err)
	}
	// Not minted yet → not-ready, and NO secret may appear as a side effect.
	if _, err := s.ResetPassword(ctx, "p1", spec.ID, EnginePostgreSQL); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("pre-mint reset must be not-ready, got %v", err)
	}
	if _, has := api.secrets[key(NamespaceFor("p1"), spec.ID+"-app")]; has {
		t.Fatal("reset must never create the operator's secret")
	}
	// CNPG mints the basic-auth secret.
	k := key(NamespaceFor("p1"), spec.ID+"-app")
	api.secrets[k] = map[string]any{"metadata": map[string]any{
		"name": spec.ID + "-app", "namespace": NamespaceFor("p1"),
		"labels": map[string]any{"cnpg.io/cluster": spec.ID},
	}}
	api.secretTypes[k] = "kubernetes.io/basic-auth"
	api.secretData[k] = map[string][]byte{"username": []byte("app"), "password": []byte("old"), "dbname": []byte("app")}
	pw, err := s.ResetPassword(ctx, "p1", spec.ID, EnginePostgreSQL)
	if err != nil {
		t.Fatalf("rotation on a basic-auth secret must merge-patch, not SSA: %v", err)
	}
	if string(api.secretData[k]["password"]) != pw || string(api.secretData[k]["username"]) != "app" {
		t.Fatalf("only the password key may change: %v", api.secretData[k])
	}
	if api.secretTypes[k] != "kubernetes.io/basic-auth" {
		t.Fatal("secret type must be untouched")
	}
	if digStr(api.secrets[k], "metadata", "labels", "cnpg.io/cluster") != spec.ID {
		t.Fatal("operator labels must be untouched")
	}
}

func TestValidateUpgradePath(t *testing.T) {
	ok := [][2]string{{"16", "17"}, {"10.11", "11.4"}, {"8.4", "9.0"}, {"9.6", "10"}, {"2.5", "2.6"}}
	for _, c := range ok {
		if err := ValidateUpgradePath(c[0], c[1]); err != nil {
			t.Errorf("%s→%s must be allowed: %v", c[0], c[1], err)
		}
	}
	bad := [][2]string{{"17", "16"}, {"11.4", "10.11"}, {"17", "17"}, {"10", "9.6"}, {"x", "17"}, {"17", ""}}
	for _, c := range bad {
		if err := ValidateUpgradePath(c[0], c[1]); err == nil {
			t.Errorf("%s→%s must be refused", c[0], c[1])
		}
	}
}

func TestSetChartVersionAndPins(t *testing.T) {
	api := newFakeAPI()
	s := NewWithAPI(api, testConfig(), "svc-dbaas")
	ctx := context.Background()
	spec := testSpec(EnginePostgreSQL, "17")
	if _, err := s.CreateDatabase(ctx, spec, testShare(), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetChartVersion(ctx, spec.ID, "0.2.0"); err != nil {
		t.Fatal(err)
	}
	pins, err := s.ListDatabasePins(ctx)
	if err != nil || len(pins) != 1 {
		t.Fatalf("pins=%v err=%v", pins, err)
	}
	if pins[0].ChartVersion != "0.2.0" || pins[0].Engine != EnginePostgreSQL || pins[0].ProjectID != "p1" {
		t.Fatalf("pin = %+v", pins[0])
	}
	if err := s.SetChartVersion(ctx, spec.ID, ""); err == nil {
		t.Fatal("empty version must refuse")
	}
	// The patch re-applies FULL metadata (finalizer survives).
	app := api.apps[key("argocd", spec.ID)]
	fins, _ := dig(app, "metadata", "finalizers").([]any)
	if len(fins) != 1 {
		t.Fatal("patch dropped the resources-finalizer")
	}
}

// TestValidateAccess pins the identifier gate. This is a SECURITY test, not a UX one:
// mariadb-operator interpolates these names straight into SQL, so anything that gets past
// ValidateAccess reaches a SQL parser unescaped.
func TestValidateAccess(t *testing.T) {
	ok := []DBUser{{Name: "alice", Databases: []string{"orders"}}}
	okDBs := []DBDatabase{{Name: "orders", Owner: "alice"}}
	if err := ValidateAccess(EnginePostgreSQL, okDBs, ok, nil); err != nil {
		t.Fatalf("valid access rejected: %v", err)
	}
	bad := map[string]struct {
		dbs   []DBDatabase
		users []DBUser
	}{
		"sql injection in user":     {nil, []DBUser{{Name: "alice; DROP TABLE x"}}},
		"sql injection in database": {[]DBDatabase{{Name: "a`; DROP"}}, nil},
		"quote in name":             {nil, []DBUser{{Name: "a'b"}}},
		"backslash in name":         {nil, []DBUser{{Name: `a\b`}}},
		"upper case":                {nil, []DBUser{{Name: "Alice"}}},
		"leading digit":             {nil, []DBUser{{Name: "1alice"}}},
		"empty":                     {nil, []DBUser{{Name: ""}}},
		"too long":                  {nil, []DBUser{{Name: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}},
		"reserved user":             {nil, []DBUser{{Name: "postgres"}}},
		"reserved app user":         {nil, []DBUser{{Name: "app"}}},
		"reserved database":         {[]DBDatabase{{Name: "postgres"}}, nil},
		"duplicate user":            {nil, []DBUser{{Name: "alice"}, {Name: "alice"}}},
		"duplicate database":        {[]DBDatabase{{Name: "orders"}, {Name: "orders"}}, nil},
		"owner not a listed user":   {[]DBDatabase{{Name: "orders", Owner: "ghost"}}, nil},
		"grant on unknown database": {nil, []DBUser{{Name: "alice", Databases: []string{"ghost"}}}},
	}
	for name, tc := range bad {
		if err := ValidateAccess(EnginePostgreSQL, tc.dbs, tc.users, nil); err == nil {
			t.Errorf("%s: must be rejected", name)
		}
	}
	// OpenSearch roles are an allowlist — an unknown role is a silent privilege grant otherwise.
	if err := ValidateAccess(EngineOpenSearch, nil, []DBUser{{Name: "alice", Roles: []string{"readall"}}}, nil); err != nil {
		t.Errorf("built-in role rejected: %v", err)
	}
	if err := ValidateAccess(EngineOpenSearch, nil, []DBUser{{Name: "alice", Roles: []string{"superuser"}}}, nil); err == nil {
		t.Error("unknown opensearch role must be rejected")
	}
	// valkey is users-only: ACL entries on the ValkeyCluster, no logical databases to declare or
	// grant, and `default` is the engine's own account (the connection-panel credential).
	if err := ValidateAccess(EngineValkey, nil, []DBUser{{Name: "cache_writer"}}, nil); err != nil {
		t.Errorf("valkey user rejected: %v", err)
	}
	if err := ValidateAccess(EngineValkey, []DBDatabase{{Name: "orders"}}, nil, nil); err == nil {
		t.Error("valkey must reject logical databases")
	}
	if err := ValidateAccess(EngineValkey, nil, []DBUser{{Name: "alice", Databases: []string{"orders"}}}, nil); err == nil {
		t.Error("valkey must reject database grants")
	}
	if err := ValidateAccess(EngineValkey, nil, []DBUser{{Name: "default"}}, nil); err == nil {
		t.Error("the default valkey ACL user must be reserved")
	}
	// The '_'->'-' mapping is injective per ident but NOT across the '-'-joined grant pair:
	// a_b×c and a×b_c derive one object name (mariadb's Grant CR) — two CRs with the same
	// identity wedge the sync, so the list is refused up front. Same-pair duplicates too.
	collide := []DBDatabase{{Name: "c"}, {Name: "b_c"}}
	if err := ValidateAccess(EngineMariaDB, collide,
		[]DBUser{{Name: "a_b", Databases: []string{"c"}}, {Name: "a", Databases: []string{"b_c"}}}, nil); err == nil {
		t.Error("colliding derived grant names must be rejected")
	}
	if err := ValidateAccess(EngineMariaDB, []DBDatabase{{Name: "c"}},
		[]DBUser{{Name: "a", Databases: []string{"c", "c"}}}, nil); err == nil {
		t.Error("a duplicated grant must be rejected")
	}
	if err := ValidateAccess(EngineMariaDB, collide,
		[]DBUser{{Name: "a_b", Databases: []string{"c"}}, {Name: "a", Databases: []string{"c"}}}, nil); err != nil {
		t.Errorf("distinct derived grant names rejected: %v", err)
	}
}

// TestCapabilityGateKeys pins that every handler-gated action is declared by the engines that
// serve it — SET_INDEX_POLICIES was gated but declared by NO engine, which killed opensearch
// retention policies end-to-end (the handler 400'd before its dispatch could run).
func TestCapabilityGateKeys(t *testing.T) {
	if !Capabilities[EngineOpenSearch]["SET_INDEX_POLICIES"] {
		t.Error("opensearch must declare SET_INDEX_POLICIES")
	}
	if !Capabilities[EngineValkey]["MANAGE_ACCESS"] {
		t.Error("valkey must declare MANAGE_ACCESS")
	}
	for engine, caps := range Capabilities {
		if caps["SET_INDEX_POLICIES"] && engine != EngineOpenSearch {
			t.Errorf("%s: index policies are an opensearch surface", engine)
		}
	}
}

// TestSetAccess drives the whole reconcile: new users get a Secret and a one-time password,
// the values document carries names only, and a user dropped from the list loses its Secret.
func TestSetAccess(t *testing.T) {
	api := newFakeAPI()
	cfg := testConfig()
	svc := NewWithAPI(api, cfg, "svc-1")
	spec := testSpec(EnginePostgreSQL, "17")
	if _, err := svc.CreateDatabase(context.Background(), spec, NetShare{NetworkID: "net-1"}, nil); err != nil {
		t.Fatal(err)
	}
	ns := NamespaceFor(spec.ProjectID)

	created, err := svc.SetAccess(context.Background(), spec.ProjectID, spec.ID, EnginePostgreSQL,
		[]DBDatabase{{Name: "orders", Owner: "alice"}},
		[]DBUser{{Name: "alice", Databases: []string{"orders"}}, {Name: "bob", Databases: []string{"orders"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 || created["alice"] == "" || created["bob"] == "" {
		t.Fatalf("both new users must come back with a password once, got %d", len(created))
	}
	for _, u := range []string{"alice", "bob"} {
		data, _ := api.GetSecretData(context.Background(), ns, UserSecretName(spec.ID, u))
		if string(data["password"]) != created[u] {
			t.Errorf("%s: secret does not hold the returned password", u)
		}
	}
	app, _ := api.GetApplication(context.Background(), cfg.ArgoNamespace, spec.ID)
	values, _ := dig(app, "spec", "source", "helm", "valuesObject").(map[string]any)
	if len(values["users"].([]any)) != 2 || len(values["databases"].([]any)) != 1 {
		t.Fatalf("values do not carry the access lists: %v", values["users"])
	}
	// Passwords must never reach the Application — it is argocd-readable.
	if strings.Contains(fmt.Sprint(values), created["alice"]) {
		t.Fatal("a password leaked into values")
	}
	// bob reaches orders through its owner alice: postgres has no per-database grant.
	first, _ := values["users"].([]any)[0].(map[string]any)
	second, _ := values["users"].([]any)[1].(map[string]any)
	byName := map[string]map[string]any{first["name"].(string): first, second["name"].(string): second}
	if _, has := byName["alice"]["inRoles"]; has {
		t.Error("the owner must not be granted membership of itself")
	}
	if roles, _ := byName["bob"]["inRoles"].([]any); len(roles) != 1 || roles[0] != "alice" {
		t.Errorf("bob.inRoles = %v, want [alice]", byName["bob"]["inRoles"])
	}

	// A database declared with NO owner must still be usable: the first user that asked for it
	// becomes the owner, otherwise postgres hands it to the app role and the grant is a no-op.
	if _, err := svc.SetAccess(context.Background(), spec.ProjectID, spec.ID, EnginePostgreSQL,
		[]DBDatabase{{Name: "logs"}},
		[]DBUser{{Name: "alice", Databases: []string{"logs"}}, {Name: "bob", Databases: []string{"logs"}}}, nil); err != nil {
		t.Fatal(err)
	}
	app, _ = api.GetApplication(context.Background(), cfg.ArgoNamespace, spec.ID)
	values, _ = dig(app, "spec", "source", "helm", "valuesObject").(map[string]any)
	logs, _ := values["databases"].([]any)[0].(map[string]any)
	if logs["owner"] != "alice" {
		t.Errorf("owner-less database must default to its first user, got %v", logs["owner"])
	}

	// Re-declaring without bob removes bob's Secret and keeps alice's password stable.
	alicePassword := created["alice"]
	created2, err := svc.SetAccess(context.Background(), spec.ProjectID, spec.ID, EnginePostgreSQL,
		[]DBDatabase{{Name: "orders", Owner: "alice"}}, []DBUser{{Name: "alice", Databases: []string{"orders"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(created2) != 0 {
		t.Errorf("an existing user must not be re-issued a password, got %v", created2)
	}
	if data, _ := api.GetSecretData(context.Background(), ns, UserSecretName(spec.ID, "alice")); string(data["password"]) != alicePassword {
		t.Error("alice's password must survive an unrelated change")
	}
	if data, _ := api.GetSecretData(context.Background(), ns, UserSecretName(spec.ID, "bob")); data != nil {
		t.Error("a removed user must lose its secret")
	}

	// Rotation is scoped to the named user and returns the new password once.
	rotated, err := svc.ResetUserPassword(context.Background(), spec.ProjectID, spec.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if rotated == alicePassword {
		t.Error("rotation must change the password")
	}
	if _, err := svc.ResetUserPassword(context.Background(), spec.ProjectID, spec.ID, "bob"); err == nil {
		t.Error("rotating a user that does not exist must fail")
	}
	// A rejected list must not write anything: bob stays gone.
	if _, err := svc.SetAccess(context.Background(), spec.ProjectID, spec.ID, EnginePostgreSQL, nil,
		[]DBUser{{Name: "bob"}, {Name: "DROP TABLE"}}, nil); err == nil {
		t.Fatal("an invalid list must be rejected")
	}
	if data, _ := api.GetSecretData(context.Background(), ns, UserSecretName(spec.ID, "bob")); data != nil {
		t.Error("a rejected list must not create secrets")
	}

	// Underscore idents are legal SQL names; their SECRET must land under the k8s-safe mapped
	// name while the values keep the real identifier (issue #168's exact shape).
	created3, err := svc.SetAccess(context.Background(), spec.ProjectID, spec.ID, EnginePostgreSQL,
		[]DBDatabase{{Name: "keycloak_stag", Owner: "keycloak_stag"}},
		[]DBUser{{Name: "keycloak_stag", Databases: []string{"keycloak_stag"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if created3["keycloak_stag"] == "" {
		t.Fatal("underscore user must be created")
	}
	if data, _ := api.GetSecretData(context.Background(), ns, spec.ID+"-u-keycloak-stag"); string(data["password"]) != created3["keycloak_stag"] {
		t.Error("the user secret must live under the '_'->'-' mapped name")
	}
	app, _ = api.GetApplication(context.Background(), cfg.ArgoNamespace, spec.ID)
	values, _ = dig(app, "spec", "source", "helm", "valuesObject").(map[string]any)
	u0, _ := values["users"].([]any)[0].(map[string]any)
	if u0["name"] != "keycloak_stag" {
		t.Errorf("values must keep the real identifier, got %v", u0["name"])
	}
}

// TestSetAccessValkey pins the users-only surface: an ACL user gets its Secret and lands in
// values by name; a database list is refused before anything is written.
func TestSetAccessValkey(t *testing.T) {
	api := newFakeAPI()
	cfg := testConfig()
	svc := NewWithAPI(api, cfg, "svc-1")
	spec := testSpec(EngineValkey, cfg.Engines[EngineValkey].Default)
	spec.BetaAck = true
	if _, err := svc.CreateDatabase(context.Background(), spec, NetShare{NetworkID: "net-1"}, nil); err != nil {
		t.Fatal(err)
	}
	created, err := svc.SetAccess(context.Background(), spec.ProjectID, spec.ID, EngineValkey,
		nil, []DBUser{{Name: "cache_writer"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if created["cache_writer"] == "" {
		t.Fatal("new valkey user must come back with a password once")
	}
	ns := NamespaceFor(spec.ProjectID)
	if data, _ := api.GetSecretData(context.Background(), ns, UserSecretName(spec.ID, "cache_writer")); string(data["password"]) != created["cache_writer"] {
		t.Error("valkey user secret missing or wrong")
	}
	app, _ := api.GetApplication(context.Background(), cfg.ArgoNamespace, spec.ID)
	values, _ := dig(app, "spec", "source", "helm", "valuesObject").(map[string]any)
	users, _ := values["users"].([]any)
	if len(users) != 1 || users[0].(map[string]any)["name"] != "cache_writer" {
		t.Fatalf("values users = %v", values["users"])
	}
	if _, err := svc.SetAccess(context.Background(), spec.ProjectID, spec.ID, EngineValkey,
		[]DBDatabase{{Name: "orders"}}, nil, nil); err == nil {
		t.Fatal("valkey must reject logical databases")
	}
}

// TestBackupSpecValidate pins the cron arity gate. This matters more than it looks: CNPG's
// ScheduledBackup takes SIX fields and reads a five-field string as seconds-first, so it runs at
// the wrong time instead of failing. One canonical arity is stored and the chart converts.
func TestBackupSpecValidate(t *testing.T) {
	for _, ok := range []string{"", "0 2 * * *", "*/15 * * * *", "0 2 * * 0", "0 0 1 1 *"} {
		if err := (BackupSpec{Schedule: ok, RetentionDays: 30}).Validate(); err != nil {
			t.Errorf("schedule %q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"0 0 2 * * *", "0 2 * *", "@daily", "0 2 * * * *", "not a cron"} {
		if err := (BackupSpec{Schedule: bad}).Validate(); err == nil {
			t.Errorf("schedule %q must be rejected (arity/format)", bad)
		}
	}
	if err := (BackupSpec{RetentionDays: -1}).Validate(); err == nil {
		t.Error("negative retention must be rejected")
	}
	if err := (BackupSpec{RetentionDays: 4000}).Validate(); err == nil {
		t.Error("absurd retention must be rejected")
	}
}

// TestBackupDefaultsAtCreate pins the two things that make a new database actually recoverable:
// a base-backup SCHEDULE exists from the start (continuous archiving alone has nothing to replay
// onto), and it is the weekly one, because every run is a full copy.
func TestBackupDefaultsAtCreate(t *testing.T) {
	cfg := testConfig()
	cfg.Backup = BackupConfig{
		Endpoint: "https://s3.example", Bucket: "backups",
		AccessKey: "AKIA-TEST", SecretKey: "s3cr3t-key", PathStyle: true,
	}
	for engine, version := range map[string]string{
		EnginePostgreSQL: "17", EngineMySQL: "8.4", EngineMariaDB: "11.4", EngineFerretDB: "2",
	} {
		values := BuildValues(cfg, testSpec(engine, version))
		block, _ := values["backup"].(map[string]any)
		if block["enabled"] != true {
			t.Fatalf("%s: store not wired at create: %v", engine, block)
		}
		if block["schedule"] != DefaultBackupSchedule {
			t.Errorf("%s: schedule = %v, want the weekly default %q — without one there is no base "+
				"backup and the archived WAL/binlog is unrestorable", engine, block["schedule"], DefaultBackupSchedule)
		}
		if block["retentionDays"] != DefaultBackupRetentionDays {
			t.Errorf("%s: retentionDays = %v", engine, block["retentionDays"])
		}
		// 30 days of a WEEKLY schedule is four backups, not thirty.
		if block["keepBackups"] != 4 {
			t.Errorf("%s: keepBackups = %v, want 4", engine, block["keepBackups"])
		}
		// Only Percona can chain increments; the others must not carry the key at all.
		incr := block["incrementalSchedule"]
		if engine == EngineMySQL {
			if incr != "0 2 * * 1,2,3,4,5,6" {
				t.Errorf("mysql: incrementalSchedule = %v", incr)
			}
		} else if incr != nil {
			t.Errorf("%s: must not get an incremental schedule, got %v", engine, incr)
		}
	}
	// An engine without the BACKUP capability gets no block at all.
	if _, ok := BuildValues(cfg, testSpec(EngineKafka, "4.0"))["backup"]; ok {
		t.Error("kafka must not get a backup block")
	}
}

func TestIncrementalScheduleAndKeep(t *testing.T) {
	// Only a base pinned to ONE weekday leaves whole days free to fill.
	if got := IncrementalSchedule(EngineMySQL, "0 2 * * 0"); got != "0 2 * * 1,2,3,4,5,6" {
		t.Errorf("weekly base -> %q", got)
	}
	if got := IncrementalSchedule(EngineMySQL, "30 4 * * 3"); got != "30 4 * * 0,1,2,4,5,6" {
		t.Errorf("midweek base -> %q", got)
	}
	// No gap, no clean complement, or not Percona: no second entry. An increment colliding with
	// its base would chain onto the PREVIOUS week's full.
	for _, base := range []string{"", "0 2 * * *", "0 * * * *", "0 2 1 * 0", "0 2 * * 1,3", "0 2 * *"} {
		if got := IncrementalSchedule(EngineMySQL, base); got != "" {
			t.Errorf("base %q -> %q, want none", base, got)
		}
	}
	if got := IncrementalSchedule(EnginePostgreSQL, "0 2 * * 0"); got != "" {
		t.Errorf("postgres has no incremental base backup, got %q", got)
	}

	// keepBackups converts days->count for the weekly shape only; anything it cannot read
	// confidently over-keeps rather than pruning a backup someone still needs.
	for _, c := range []struct {
		schedule string
		days     int
		want     int
	}{
		{"0 2 * * 0", 30, 4}, {"0 2 * * 0", 7, 1}, {"0 2 * * 0", 3, 1},
		{"0 2 * * *", 30, 30}, {"0 * * * *", 30, 30}, {"", 30, 30},
	} {
		if got := keepBackups(c.schedule, c.days); got != c.want {
			t.Errorf("keepBackups(%q, %d) = %d, want %d", c.schedule, c.days, got, c.want)
		}
	}
}

// TestSetBackup covers the whole posture change: credentials land in a Secret (never values),
// the values carry the provider's store, and disabling takes the credential away again.
func TestSetBackup(t *testing.T) {
	api := newFakeAPI()
	cfg := testConfig()
	cfg.Backup = BackupConfig{
		Endpoint: "https://s3.example", Bucket: "backups", Prefix: "az1",
		AccessKey: "AKIA-TEST", SecretKey: "s3cr3t-key", PathStyle: true,
	}
	svc := NewWithAPI(api, cfg, "svc-1")
	spec := testSpec(EnginePostgreSQL, "17")
	if _, err := svc.CreateDatabase(context.Background(), spec, NetShare{NetworkID: "net-1"}, nil); err != nil {
		t.Fatal(err)
	}
	ns := NamespaceFor(spec.ProjectID)

	if err := svc.SetBackup(context.Background(), spec.ProjectID, spec.ID,
		BackupSpec{Enabled: true, Schedule: "0 2 * * *", RetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	data, _ := api.GetSecretData(context.Background(), ns, BackupSecretName(spec.ID))
	if string(data[BackupCredKeys.CNPGAccess]) != "AKIA-TEST" || string(data[BackupCredKeys.AWSSecret]) != "s3cr3t-key" {
		t.Fatalf("credential secret missing an alias: %v", data)
	}
	app, _ := api.GetApplication(context.Background(), cfg.ArgoNamespace, spec.ID)
	values, _ := dig(app, "spec", "source", "helm", "valuesObject").(map[string]any)
	block, _ := values["backup"].(map[string]any)
	if block["enabled"] != true || block["schedule"] != "0 2 * * *" || block["retentionDays"] != 30 {
		t.Fatalf("backup values = %v", block)
	}
	s3, _ := block["s3"].(map[string]any)
	if s3["bucket"] != "backups" || s3["credsSecret"] != BackupSecretName(spec.ID) {
		t.Fatalf("s3 block = %v", s3)
	}
	// The whole point: no key material may reach the Application.
	if strings.Contains(fmt.Sprint(values), "s3cr3t-key") || strings.Contains(fmt.Sprint(values), "AKIA-TEST") {
		t.Fatal("backup credentials leaked into values")
	}

	// On-demand stamps a value the chart turns into one extra run.
	if err := svc.TriggerBackup(context.Background(), spec.ID, "20260803-101500"); err != nil {
		t.Fatal(err)
	}
	app, _ = api.GetApplication(context.Background(), cfg.ArgoNamespace, spec.ID)
	values, _ = dig(app, "spec", "source", "helm", "valuesObject").(map[string]any)
	if got := dig(values, "backup", "runAt"); got != "20260803-101500" {
		t.Errorf("runAt = %v", got)
	}

	// Clearing the SCHEDULE must not unwire the object store: doing that would pull the barman
	// sidecar out of every instance pod and roll the whole cluster, and it would end continuous
	// archiving. On-demand backups and PITR keep working with no schedule.
	if err := svc.SetBackup(context.Background(), spec.ProjectID, spec.ID, BackupSpec{}); err != nil {
		t.Fatal(err)
	}
	if data, _ := api.GetSecretData(context.Background(), ns, BackupSecretName(spec.ID)); data == nil {
		t.Error("the credential secret must survive a schedule change")
	}
	app, _ = api.GetApplication(context.Background(), cfg.ArgoNamespace, spec.ID)
	values, _ = dig(app, "spec", "source", "helm", "valuesObject").(map[string]any)
	if got := dig(values, "backup", "enabled"); got != true {
		t.Errorf("backup.enabled = %v, want it to stay true", got)
	}
	if got := dig(values, "backup", "schedule"); got != nil {
		t.Errorf("no schedule must mean no schedule key, got %v", got)
	}
	// On-demand still works without a schedule — that is the whole point of the split.
	if err := svc.TriggerBackup(context.Background(), spec.ID, "20260804-120000"); err != nil {
		t.Errorf("on-demand backup must work without a schedule: %v", err)
	}
	// And a pending on-demand run survives a later schedule change.
	if err := svc.SetBackup(context.Background(), spec.ProjectID, spec.ID,
		BackupSpec{Schedule: "0 3 * * *", RetentionDays: 7}); err != nil {
		t.Fatal(err)
	}
	app, _ = api.GetApplication(context.Background(), cfg.ArgoNamespace, spec.ID)
	values, _ = dig(app, "spec", "source", "helm", "valuesObject").(map[string]any)
	if got := dig(values, "backup", "runAt"); got != "20260804-120000" {
		t.Errorf("a schedule change must not cancel a backup already started, runAt = %v", got)
	}

	// A provider with no object store has no backup surface at all.
	noStore := NewWithAPI(newFakeAPI(), testConfig(), "svc-2")
	if err := noStore.SetBackup(context.Background(), spec.ProjectID, spec.ID, BackupSpec{Enabled: true}); err == nil {
		t.Error("a location without an object store must refuse backup settings")
	}
	// Creating with a store wires backups from the start, so nothing has to be turned on later.
	atCreate := BuildValues(cfg, testSpec(EnginePostgreSQL, "17"))
	if got := dig(atCreate, "backup", "enabled"); got != true {
		t.Errorf("a new database must have its object store wired at create, got %v", got)
	}
	if got := dig(BuildValues(testConfig(), testSpec(EnginePostgreSQL, "17")), "backup"); got != nil {
		t.Errorf("without a provider store there must be no backup block, got %v", got)
	}
}

// TestListBackups pins the ownership filter: one namespace holds every database in a project,
// so a sibling's backups must never show up in this database's history.
func TestListBackups(t *testing.T) {
	api := newFakeAPI()
	cfg := testConfig()
	svc := NewWithAPI(api, cfg, "svc-1")
	ns := NamespaceFor("p1")
	mine := map[string]any{
		"metadata": map[string]any{
			"name": "std-mine-ondemand-1", "namespace": ns,
			"labels":            map[string]any{LabelManagedBy: ManagedByValue},
			"creationTimestamp": "2026-08-03T10:00:00Z",
		},
		"status": map[string]any{"phase": "completed"},
	}
	sibling := map[string]any{
		"metadata": map[string]any{
			"name": "std-other-ondemand-1", "namespace": ns,
			"labels": map[string]any{LabelManagedBy: ManagedByValue},
		},
		"status": map[string]any{"phase": "completed"},
	}
	// A SCHEDULED backup: created by the engine's operator from our ScheduledBackup, so it carries
	// the operator's labels and NOT ours. It is still this database's backup and must be listed —
	// filtering on the stratos label hid every unattended backup and left the schedule decorative.
	scheduled := map[string]any{
		"metadata": map[string]any{
			"name": "std-mine-schedule-20260805165026", "namespace": ns,
			"labels":            map[string]any{"cnpg.io/cluster": "std-mine", "cnpg.io/scheduled-backup": "std-mine-schedule"},
			"creationTimestamp": "2026-08-05T16:50:26Z",
		},
		"status": map[string]any{"phase": "completed"},
	}
	api.crs["backups"] = map[string]map[string]any{
		key(ns, "std-mine-ondemand-1"):              mine,
		key(ns, "std-other-ondemand-1"):             sibling,
		key(ns, "std-mine-schedule-20260805165026"): scheduled,
	}
	got, err := svc.ListBackups(context.Background(), "p1", "std-mine", EnginePostgreSQL)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, b := range got {
		names[b["name"].(string)] = b["phase"].(string)
	}
	if len(got) != 2 || names["std-mine-ondemand-1"] != "completed" || names["std-mine-schedule-20260805165026"] != "completed" {
		t.Fatalf("list = %v, want this database's on-demand AND scheduled backups only", got)
	}
	if _, err := svc.ListBackups(context.Background(), "p1", "std-mine", EngineValkey); err == nil {
		t.Error("an engine with no backup CR must report that, not an empty list")
	}
}

// TestRestoreFrom pins the recovery contract: values name the SOURCE's folder, the credential
// Secret exists before the Application (the bootstrap reads S3 immediately), and engines whose
// restore targets a running cluster are refused rather than half-wired.
func TestRestoreFrom(t *testing.T) {
	api := newFakeAPI()
	cfg := testConfig()
	cfg.Backup = BackupConfig{
		Endpoint: "https://s3.example", Bucket: "backups", Prefix: "az1",
		AccessKey: "AK", SecretKey: "SK", PathStyle: true,
	}
	svc := NewWithAPI(api, cfg, "svc-1")
	spec := testSpec(EnginePostgreSQL, "17")
	spec.RestoreFrom = &RestoreSource{SourceID: "std-source01", TargetTime: "2026-08-03T10:00:00Z"}
	if _, err := svc.CreateDatabase(context.Background(), spec, NetShare{NetworkID: "net-1"}, nil); err != nil {
		t.Fatal(err)
	}
	// Ordering is the whole point: the credential must exist by the time the Application does.
	secretAt, appAt := -1, -1
	for i, op := range api.ops {
		if op == "secret:"+BackupSecretName(spec.ID) {
			secretAt = i
		}
		if op == "app:"+spec.ID {
			appAt = i
		}
	}
	if secretAt < 0 || appAt < 0 || secretAt > appAt {
		t.Fatalf("restore credentials must be applied before the Application: %v", api.ops)
	}
	app, _ := api.GetApplication(context.Background(), cfg.ArgoNamespace, spec.ID)
	values, _ := dig(app, "spec", "source", "helm", "valuesObject").(map[string]any)
	restore, _ := values["restore"].(map[string]any)
	if restore["sourceId"] != "std-source01" || restore["targetTime"] != "2026-08-03T10:00:00Z" {
		t.Fatalf("restore values = %v", restore)
	}
	if restore["credsSecret"] != BackupSecretName(spec.ID) {
		t.Errorf("restore must read through this database's own credential secret, got %v", restore["credsSecret"])
	}
	if strings.Contains(fmt.Sprint(values), "SK") && strings.Contains(fmt.Sprint(values), "AK") {
		t.Error("restore credentials leaked into values")
	}

	// A normal create carries no restore block at all.
	plain := BuildValues(cfg, testSpec(EnginePostgreSQL, "17"))
	if _, has := plain["restore"]; has {
		t.Error("a non-restoring database must not carry a restore block")
	}

	// mysql backs up but cannot bootstrap from a backup — refuse rather than half-wire it.
	my := testSpec(EngineMySQL, "8.4")
	my.RestoreFrom = &RestoreSource{SourceID: "std-source01"}
	if err := my.Validate(cfg); err == nil {
		t.Error("mysql restore-into-new-database must be refused")
	}
	// Malformed sources are rejected before anything is written.
	bad := testSpec(EnginePostgreSQL, "17")
	bad.RestoreFrom = &RestoreSource{SourceID: "not-an-id"}
	if err := bad.Validate(cfg); err == nil {
		t.Error("a source id that is not a database id must be rejected")
	}
	bad.RestoreFrom = &RestoreSource{SourceID: "std-source01", TargetTime: "yesterday"}
	if err := bad.Validate(cfg); err == nil {
		t.Error("a non-RFC3339 target time must be rejected")
	}
}

// TestStampBackupRun pins the safety-backup helper: it rides an existing values patch, and it
// is a NO-OP when backups are off — a risky change must never fail because the customer has no
// object store configured.
func TestStampBackupRun(t *testing.T) {
	off := map[string]any{}
	if StampBackupRun(off, "20260804-090000") {
		t.Error("no backup block: must report that nothing was taken")
	}
	if _, has := off["backup"]; has {
		t.Error("no backup block: must not invent one")
	}
	disabled := map[string]any{"backup": map[string]any{"enabled": false}}
	if StampBackupRun(disabled, "20260804-090000") {
		t.Error("backups disabled: must report that nothing was taken")
	}
	if _, has := disabled["backup"].(map[string]any)["runAt"]; has {
		t.Error("backups disabled: must not stamp a run")
	}
	on := map[string]any{"backup": map[string]any{"enabled": true, "schedule": "0 2 * * *"}}
	if !StampBackupRun(on, "20260804-090000") {
		t.Fatal("backups on: must take one")
	}
	block := on["backup"].(map[string]any)
	if block["runAt"] != "20260804-090000" {
		t.Errorf("runAt = %v", block["runAt"])
	}
	if block["schedule"] != "0 2 * * *" {
		t.Error("the rest of the backup posture must survive")
	}
}

// Placement rides one `scheduling` block; the chart is what spreads it over each engine's pods.
func TestBuildValuesScheduling(t *testing.T) {
	cfg := testConfig()
	if v := BuildValues(cfg, testSpec(EnginePostgreSQL, "17")); v["scheduling"] != nil {
		t.Errorf("unconfigured placement must not emit the key: %v", v["scheduling"])
	}

	cfg.NodeSelector = map[string]string{"node-role": "database"}
	cfg.Tolerations = []map[string]any{
		{"key": "dedicated", "operator": "Equal", "value": "database", "effect": "NoSchedule"},
	}
	v := BuildValues(cfg, testSpec(EnginePostgreSQL, "17"))
	block, ok := v["scheduling"].(map[string]any)
	if !ok {
		t.Fatalf("scheduling = %#v", v["scheduling"])
	}
	if sel, _ := block["nodeSelector"].(map[string]any); sel["node-role"] != "database" {
		t.Errorf("nodeSelector = %#v", block["nodeSelector"])
	}
	tol, _ := block["tolerations"].([]any)
	if len(tol) != 1 {
		t.Fatalf("tolerations = %#v", block["tolerations"])
	}
	if first, _ := tol[0].(map[string]any); first["effect"] != "NoSchedule" {
		t.Errorf("toleration passed through changed: %#v", tol[0])
	}
}

// A plain create (no restore, no SetBackup) must still write the object-store credential
// Secret. BuildValues wires the ObjectStore into every database the provider can back up, and
// a CR pointing at a Secret that does not exist takes down CONTINUOUS ARCHIVING as well as
// backups — silently, with WAL then accumulating on the data volume.
func TestCreateDatabaseWritesBackupCredentials(t *testing.T) {
	api := newFakeAPI()
	cfg := testConfig()
	cfg.Backup = BackupConfig{
		Endpoint: "https://s3.example", Bucket: "backups", Prefix: "az1",
		AccessKey: "AKIA-TEST", SecretKey: "s3cr3t-key", PathStyle: true,
	}
	svc := NewWithAPI(api, cfg, "svc-1")
	spec := testSpec(EnginePostgreSQL, "17")
	ctx := context.Background()
	if _, err := svc.CreateDatabase(ctx, spec, testShare(), nil); err != nil {
		t.Fatal(err)
	}

	data, _ := api.GetSecretData(ctx, NamespaceFor(spec.ProjectID), BackupSecretName(spec.ID))
	if string(data[BackupCredKeys.CNPGAccess]) != "AKIA-TEST" || string(data[BackupCredKeys.AWSSecret]) != "s3cr3t-key" {
		t.Fatalf("backup credential secret not written at create: %v", data)
	}
	// Before the Application: the operator starts reconciling the moment the CR lands.
	ops := strings.Join(api.ops, ",")
	if si, ai := strings.Index(ops, "secret:"+BackupSecretName(spec.ID)), strings.Index(ops, "app:"+spec.ID); si < 0 || si > ai {
		t.Fatalf("credential must be written before the Application: %s", ops)
	}
	// Engines the platform cannot back up get no credential — nothing references it.
	api2 := newFakeAPI()
	svc2 := NewWithAPI(api2, cfg, "svc-1")
	valkey := testSpec(EngineValkey, "9")
	valkey.BetaAck = true
	if _, err := svc2.CreateDatabase(ctx, valkey, testShare(), nil); err != nil {
		t.Fatal(err)
	}
	if d, _ := api2.GetSecretData(ctx, NamespaceFor(valkey.ProjectID), BackupSecretName(valkey.ID)); d != nil {
		t.Fatalf("engine without BACKUP capability must not get credentials: %v", d)
	}
}

// A postgres database created before the superuser switch has no <id>-superuser Secret. It is
// serving traffic, so the connection panel must show the account it actually has instead of
// "credentials not ready", and RESET_PASSWORD must rotate that same account.
func TestConnectionInfoFallsBackToAppRole(t *testing.T) {
	api := newFakeAPI()
	cfg := testConfig()
	svc := NewWithAPI(api, cfg, "svc-1")
	spec := testSpec(EnginePostgreSQL, "17")
	ctx := context.Background()
	if _, err := svc.CreateDatabase(ctx, spec, testShare(), nil); err != nil {
		t.Fatal(err)
	}
	ns := NamespaceFor(spec.ProjectID)
	// Only the legacy secret exists — exactly the shape of a pre-switch database. Registered in
	// both maps because the fake mirrors the real API: a patch 404s when the object is absent.
	api.secrets[key(ns, spec.ID+"-app")] = map[string]any{
		"metadata": map[string]any{"name": spec.ID + "-app", "namespace": ns},
	}
	api.secretData[key(ns, spec.ID+"-app")] = map[string][]byte{
		"username": []byte("app"), "password": []byte("legacy-pw"), "dbname": []byte("app"),
	}
	api.services[key(ns, LBServiceName(spec.ID))] = map[string]any{
		"status": map[string]any{"loadBalancer": map[string]any{"ingress": []any{map[string]any{"ip": "10.1.0.9"}}}},
	}

	info, err := svc.ConnectionInfo(ctx, spec.ProjectID, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info.Username != "app" || info.Password != "legacy-pw" || info.DBName != "app" {
		t.Fatalf("fallback conn info = %+v", info)
	}

	pw, err := svc.ResetPassword(ctx, spec.ProjectID, spec.ID, EnginePostgreSQL)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(api.secretData[key(ns, spec.ID+"-app")]["password"]); got != pw {
		t.Fatalf("reset must rotate the legacy secret: %q vs %q", got, pw)
	}
}

// The cached row must carry the backup posture. It did not, and the cost was not a wrong label:
// the console builds its restore-source list from `backup.enabled`, so with the key absent no
// database was ever offered as a recovery source and restore-from-backup could not be reached
// from the UI at all.
func TestDatabaseDataCarriesBackupPosture(t *testing.T) {
	cfg := testConfig()
	cfg.Backup = BackupConfig{Endpoint: "https://s3.example", Bucket: "b", AccessKey: "a", SecretKey: "s"}
	spec := testSpec(EnginePostgreSQL, "17")
	values := BuildValues(cfg, spec)
	app := BuildApplication(cfg, spec, "svc-1", cfg.ChartVersion, values)

	d, _ := databaseData(cfg, app, nil)["database"].(map[string]any)
	block, ok := d["backup"].(map[string]any)
	if !ok {
		t.Fatalf("cached row has no backup block: %v", d["backup"])
	}
	if block["enabled"] != true {
		t.Errorf("backup.enabled = %v, want true (the store is wired at create)", block["enabled"])
	}
	if block["schedule"] != DefaultBackupSchedule {
		t.Errorf("backup.schedule = %v, want %q", block["schedule"], DefaultBackupSchedule)
	}
	if block["retentionDays"] != DefaultBackupRetentionDays {
		t.Errorf("backup.retentionDays = %v, want %d", block["retentionDays"], DefaultBackupRetentionDays)
	}

	// An engine the platform cannot back up carries no block rather than a false one.
	valkey := testSpec(EngineValkey, "9")
	vApp := BuildApplication(cfg, valkey, "svc-1", cfg.ChartVersion, BuildValues(cfg, valkey))
	vd, _ := databaseData(cfg, vApp, nil)["database"].(map[string]any)
	if _, has := vd["backup"]; has {
		t.Errorf("valkey must not carry a backup block: %v", vd["backup"])
	}
}

// A database whose desired state never reached the cluster must not be shown as ready. ArgoCD
// computes health over the resources it can see and has no health check for most operator CRs, so
// an engine CR the API server rejected — or one the operator marked failed — leaves the app
// Healthy while nothing runs. Live-caught: valkey (UsersACLError) and opensearch (admission
// webhook refusal) both reported `ready` with a published endpoint and zero pods.
func TestDatabaseStatusNotReadyWhenOutOfSync(t *testing.T) {
	app := func(health, sync string) map[string]any {
		return map[string]any{
			"metadata": map[string]any{"name": "std-1"},
			"spec": map[string]any{"source": map[string]any{"helm": map[string]any{
				"valuesObject": map[string]any{"engine": EngineValkey, "engineVersion": "7.2"},
			}}},
			"status": map[string]any{
				"health": map[string]any{"status": health},
				"sync":   map[string]any{"status": sync},
			},
		}
	}
	cfg := testConfig()
	for _, tc := range []struct {
		health, sync, want string
	}{
		{"Healthy", "Synced", "READY"},
		{"Healthy", "OutOfSync", "PROGRESSING"},
		{"Progressing", "OutOfSync", "PROGRESSING"},
		// A real failure keeps its own word — OutOfSync must not soften Degraded into
		// "still working on it".
		{"Degraded", "OutOfSync", "DEGRADED"},
	} {
		db, _ := databaseDataWithHost(cfg, app(tc.health, tc.sync), "10.1.0.5", "")["database"].(map[string]any)
		got := db["status"]
		if got != tc.want {
			t.Errorf("health=%s sync=%s → status=%v, want %s", tc.health, tc.sync, got, tc.want)
		}
	}
}
