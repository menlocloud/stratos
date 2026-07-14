package providers

import (
	"context"
	"reflect"
	"testing"
)

type testFlavorReader struct {
	flavor map[string]any
}

func (r testFlavorReader) GetFlavor(context.Context, string) (map[string]any, error) {
	return r.flavor, nil
}

func TestEnrichServerFlavorForCacheIncludesGPUExtraSpecs(t *testing.T) {
	server := map[string]any{"flavor": map[string]any{"id": "gpu-flavor"}}
	wantExtraSpecs := map[string]any{"pci_passthrough:alias": "a6000:2"}
	enrichServerFlavorForCache(context.Background(), testFlavorReader{flavor: map[string]any{
		"id": "gpu-flavor", "ram": 32768, "vcpus": 8, "extra_specs": wantExtraSpecs,
	}}, server)

	flavor := server["flavor"].(map[string]any)
	if !reflect.DeepEqual(flavor["extra_specs"], wantExtraSpecs) {
		t.Fatalf("extra_specs = %#v, want %#v", flavor["extra_specs"], wantExtraSpecs)
	}
}

func TestEnrichServerFlavorForCacheIsOptional(t *testing.T) {
	server := map[string]any{"flavor": map[string]any{"id": "cpu-flavor"}}
	enrichServerFlavorForCache(context.Background(), struct{}{}, server)
	if _, exists := server["flavor"].(map[string]any)["extra_specs"]; exists {
		t.Fatal("writer without GetFlavor should leave flavor unchanged")
	}
}
