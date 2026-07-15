package providers

import "testing"

func TestAllocationPoolsParse(t *testing.T) {
	in := []any{
		map[string]any{"start": "10.0.0.10", "end": "10.0.0.20"},
		map[string]any{"start": "10.0.0.30", "end": "10.0.0.40"},
		"not-a-map", // skipped
	}
	got := allocationPools(in)
	if len(got) != 2 || got[0].Start != "10.0.0.10" || got[0].End != "10.0.0.20" || got[1].Start != "10.0.0.30" {
		t.Fatalf("allocationPools = %#v", got)
	}
	if allocationPools(nil) == nil || len(allocationPools(nil)) != 0 {
		t.Errorf("nil → empty slice")
	}
}

func TestHostRoutesParse(t *testing.T) {
	in := []any{map[string]any{"destination": "0.0.0.0/0", "nexthop": "10.0.0.1"}}
	got := hostRoutes(in)
	if len(got) != 1 || got[0].DestinationCIDR != "0.0.0.0/0" || got[0].NextHop != "10.0.0.1" {
		t.Fatalf("hostRoutes = %#v", got)
	}
}

func TestIfaceFixedIPs(t *testing.T) {
	in := []any{
		map[string]any{"uuid": "net-1", "fixedIp": "10.0.0.10"},
		map[string]any{"uuid": "net-2"},                  // no fixedIp → omitted
		map[string]any{"uuid": "", "fixedIp": "1.2.3.4"}, // no uuid → omitted
	}
	got := ifaceFixedIPs(in)
	if len(got) != 1 || got["net-1"] != "10.0.0.10" {
		t.Fatalf("ifaceFixedIPs = %#v", got)
	}
	if ifaceFixedIPs([]any{map[string]any{"uuid": "n"}}) != nil {
		t.Errorf("no fixed IPs → nil (not empty map)")
	}
}

func TestAddressPairs(t *testing.T) {
	in := []any{
		map[string]any{"ipAddress": "10.0.0.100"},
		map[string]any{"ipAddress": "10.0.0.0/24", "macAddress": "fa:16:3e:aa:bb:cc"},
		map[string]any{"macAddress": "no-ip"}, // no ipAddress → skipped
	}
	got := addressPairs(in)
	if len(got) != 2 || got[0].IPAddress != "10.0.0.100" || got[1].IPAddress != "10.0.0.0/24" || got[1].MACAddress != "fa:16:3e:aa:bb:cc" {
		t.Fatalf("addressPairs = %#v", got)
	}
}

func TestMboolPtr(t *testing.T) {
	if mboolPtr(map[string]any{}, "x") != nil {
		t.Errorf("absent → nil")
	}
	if p := mboolPtr(map[string]any{"x": false}, "x"); p == nil || *p != false {
		t.Errorf("present false → &false, got %v", p)
	}
	if p := mboolPtr(map[string]any{"x": true}, "x"); p == nil || *p != true {
		t.Errorf("present true → &true, got %v", p)
	}
}

func TestCreateServerVolumesParseLifecyclePolicy(t *testing.T) {
	root, err := createServerVolume(map[string]any{"sizeGiB": float64(80), "type": "ceph-ssd1"}, true, "root")
	if err != nil {
		t.Fatalf("createServerVolume() error = %v", err)
	}
	if root.Size != 80 || root.VolumeType != "ceph-ssd1" || !root.DeleteOnTermination || root.Tag != "root" {
		t.Fatalf("root volume = %#v", root)
	}
	data, err := createServerDataVolumes([]any{
		map[string]any{"sizeGiB": float64(100), "type": "ceph-ssd1"},
		map[string]any{"sizeGiB": float64(200), "type": "archive"},
	})
	if err != nil {
		t.Fatalf("createServerDataVolumes() error = %v", err)
	}
	if len(data) != 2 || data[0].DeleteOnTermination || data[0].Tag != "data-1" || data[1].Tag != "data-2" {
		t.Fatalf("data volumes = %#v", data)
	}
}

func TestCreateServerVolumesRejectInvalidInput(t *testing.T) {
	for _, input := range []map[string]any{
		{"sizeGiB": float64(0), "type": "ssd"},
		{"sizeGiB": 10.5, "type": "ssd"},
		{"sizeGiB": float64(10), "type": ""},
	} {
		if _, err := createServerVolume(input, true, "root"); err == nil {
			t.Fatalf("createServerVolume(%#v) unexpectedly succeeded", input)
		}
	}
}

func TestVolumeAttachmentHelpersSupportCinderShape(t *testing.T) {
	data := map[string]any{"volume": map[string]any{
		"bootable":    "true",
		"attachments": []any{map[string]any{"server_id": "server-1", "attachment_id": "attach-1"}},
	}}
	if !volumeIsBootable(data) {
		t.Fatal("string bootable flag must be recognized")
	}
	attachments := volumeAttachments(data)
	if len(attachments) != 1 || attServerID(attachments[0]) != "server-1" {
		t.Fatalf("volumeAttachments() = %#v", attachments)
	}
	updated := withoutAttachment(data, "server-1")
	if nested := volumeAttachments(updated); len(nested) != 0 {
		t.Fatalf("attachment was not removed: %#v", nested)
	}
}

func TestReplaceServerDataPreservesVolumeBackedMetadata(t *testing.T) {
	existing := map[string]any{
		"volumeBacked": true,
		"rootVolume":   map[string]any{"sizeGiB": 40, "type": "ssd"},
		"dataVolumes":  []any{map[string]any{"sizeGiB": 100, "type": "ssd"}},
		"flavorName":   "m1.medium",
		"server":       map[string]any{"name": "old"},
	}
	updated := replaceServerData(existing, map[string]any{"name": "renamed"})
	if updated["volumeBacked"] != true || updated["rootVolume"] == nil || updated["dataVolumes"] == nil {
		t.Fatalf("volume-backed metadata was lost: %#v", updated)
	}
	if updated["flavorName"] != "m1.medium" {
		t.Fatalf("flavorName was lost: %#v", updated)
	}
	if updated["server"].(map[string]any)["name"] != "renamed" {
		t.Fatalf("server was not replaced: %#v", updated)
	}
}
