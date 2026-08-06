package project

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestDbaasSpecFromData(t *testing.T) {
	body := func() map[string]any {
		return map[string]any{
			"name": "orders", "engine": "postgresql", "version": "17",
			"replicas": float64(3), "cpu": float64(2), "memoryGiB": float64(8), "storageGiB": float64(100),
			"networkId": "net-1", "subnetId": "sub-1",
			"allowedCidrs": []any{"10.1.0.0/24"},
		}
	}
	spec, err := dbaasSpecFromData("p1", body())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(spec.ID, "std-") || len(spec.ID) != 12 {
		t.Fatalf("id = %q", spec.ID)
	}
	if spec.DisplayName != "orders" || spec.Engine != "postgresql" || spec.Version != "17" ||
		spec.Replicas != 3 || spec.CPU != 2 || spec.MemoryGiB != 8 || spec.StorageGiB != 100 ||
		spec.NetworkID != "net-1" || spec.SubnetID != "sub-1" || len(spec.AllowedCIDRs) != 1 || spec.BetaAck {
		t.Fatalf("spec = %+v", spec)
	}

	// Strict decode: missing/typed-wrong numbers are 400s, not zero values.
	for _, k := range []string{"replicas", "cpu", "memoryGiB", "storageGiB"} {
		d := body()
		delete(d, k)
		if _, err := dbaasSpecFromData("p1", d); err == nil {
			t.Errorf("missing %s must be rejected", k)
		}
		d = body()
		d[k] = "three"
		if _, err := dbaasSpecFromData("p1", d); err == nil {
			t.Errorf("string %s must be rejected", k)
		}
	}
	d := body()
	delete(d, "name")
	if _, err := dbaasSpecFromData("p1", d); err == nil {
		t.Error("missing name must be rejected")
	}
	d = body()
	d["allowedCidrs"] = []any{float64(1)}
	if _, err := dbaasSpecFromData("p1", d); err == nil {
		t.Error("non-string cidr must be rejected")
	}
	// Beta ack is strict: a non-bool is a 400, not a silent false.
	d = body()
	d["beta"] = "true"
	if _, err := dbaasSpecFromData("p1", d); err == nil {
		t.Error("non-bool beta must be rejected")
	}
	d = body()
	d["beta"] = true
	if spec, err := dbaasSpecFromData("p1", d); err != nil || !spec.BetaAck {
		t.Error("bool beta must decode")
	}
}

func TestDbaasCIDRs(t *testing.T) {
	out, err := dbaasCIDRs([]any{"10.0.0.0/8", ""})
	if err != nil || len(out) != 1 || out[0] != "10.0.0.0/8" {
		t.Fatalf("out=%v err=%v", out, err)
	}
	if _, err := dbaasCIDRs([]any{"10.0.0.1"}); err == nil {
		t.Fatal("bare IP must be rejected")
	}
	if _, err := dbaasCIDRs("10.0.0.0/8"); err == nil {
		t.Fatal("non-array must be rejected")
	}
}

// TestValidateCustomDomainTLS covers the BYO-cert gate: key must match, name must be covered,
// expiry enforced, domain shape checked. Certs are minted in-test — no fixtures to go stale.
func TestValidateCustomDomainTLS(t *testing.T) {
	mint := func(dnsName string, notAfter time.Time) (certPEM, keyPEM string) {
		t.Helper()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(1),
			Subject:      pkix.Name{CommonName: dnsName},
			DNSNames:     []string{dnsName},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     notAfter,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
			string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	}

	good, goodKey := mint("search.example.com", time.Now().Add(24*time.Hour))
	if err := validateCustomDomainTLS("search.example.com", good, goodKey); err != nil {
		t.Fatalf("valid bundle rejected: %v", err)
	}
	if err := validateCustomDomainTLS("other.example.com", good, goodKey); err == nil {
		t.Fatal("cert not covering the domain must be rejected")
	}
	expired, expiredKey := mint("search.example.com", time.Now().Add(-time.Minute))
	if err := validateCustomDomainTLS("search.example.com", expired, expiredKey); err == nil {
		t.Fatal("expired cert must be rejected")
	}
	_, otherKey := mint("search.example.com", time.Now().Add(24*time.Hour))
	if err := validateCustomDomainTLS("search.example.com", good, otherKey); err == nil {
		t.Fatal("mismatched key must be rejected")
	}
	for _, bad := range []string{"", "singlelabel", "UPPER.example.com", "-bad.example.com", "a b.example.com"} {
		if err := validateCustomDomainTLS(bad, good, goodKey); err == nil {
			t.Fatalf("domain %q must be rejected", bad)
		}
	}
	if err := validateCustomDomainTLS("search.example.com", "", ""); err == nil {
		t.Fatal("missing PEM material must be rejected")
	}
}
