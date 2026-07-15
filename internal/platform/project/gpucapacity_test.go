package project

import (
	"reflect"
	"testing"

	"github.com/menlocloud/stratos/internal/cloud/client"
)

func TestGPUCapacityFromInfo(t *testing.T) {
	got := gpuCapacityFromInfo([]client.GPUCapacity{
		{Name: "NVIDIA_A100_80GB", Total: 16, InUse: 9}, // canonicalized + available derived
		{Name: "nvidia-l40s", Total: 24, InUse: 24},     // fully used → 0 available
		{Name: "vgpu", Total: 4, InUse: 6},              // over-allocated → clamp at 0, not negative
	})
	want := []gpuCapacityEntry{
		{Model: "nvidia-a100-80gb", Available: 7, Total: 16},
		{Model: "nvidia-l40s", Available: 0, Total: 24},
		{Model: "vgpu", Available: 0, Total: 4},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gpuCapacityFromInfo() = %#v, want %#v", got, want)
	}
	if empty := gpuCapacityFromInfo(nil); len(empty) != 0 {
		t.Fatalf("nil input must yield empty slice, got %#v", empty)
	}
}
