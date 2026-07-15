package client

import (
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
)

// Times in data.image must be RFC3339 STRINGS: a time.Time round-trips through the datastore as
// primitive.DateTime and the sync keyed compare then "differs" on every pass (dev216 churn).
func TestImageToMapTimesAreStrings(t *testing.T) {
	ts := time.Date(2026, 7, 2, 4, 3, 59, 0, time.UTC)
	m := imageToMap(&images.Image{ID: "img-1", CreatedAt: ts, UpdatedAt: ts})
	for _, k := range []string{"created_at", "createdAt", "updated_at", "updatedAt"} {
		s, ok := m[k].(string)
		if !ok || s != "2026-07-02T04:03:59Z" {
			t.Errorf("%s: want RFC3339 string, got %T %v", k, m[k], m[k])
		}
	}
}

func TestImageToMapIncludesVirtualSize(t *testing.T) {
	m := imageToMap(&images.Image{ID: "img-1", SizeBytes: 2 << 30, VirtualSize: 20 << 30})
	if m["virtual_size"] != int64(20<<30) || m["virtualSize"] != int64(20<<30) {
		t.Fatalf("virtual size aliases = %#v / %#v", m["virtual_size"], m["virtualSize"])
	}
}
