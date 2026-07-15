package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
)

func TestCreateServerUsesCuratedVolumeBlockDevices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/servers" {
			t.Fatalf("request = %s %s, want POST /servers", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-OpenStack-Nova-API-Version"); got != serverBlockDeviceMicroversion {
			t.Fatalf("nova microversion = %q, want %q", got, serverBlockDeviceMicroversion)
		}
		var envelope map[string]map[string]any
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		body := envelope["server"]
		if imageRef, present := body["imageRef"]; present && imageRef != "" {
			t.Fatalf("volume-backed request must not carry a direct imageRef: %#v", imageRef)
		}
		devices, ok := body["block_device_mapping_v2"].([]any)
		if !ok || len(devices) != 3 {
			t.Fatalf("block devices = %#v, want root + 2 data volumes", body["block_device_mapping_v2"])
		}
		root := devices[0].(map[string]any)
		if root["source_type"] != "image" || root["destination_type"] != "volume" || root["uuid"] != "image-1" || root["boot_index"] != float64(0) || root["volume_size"] != float64(80) || root["volume_type"] != "ceph-ssd1" || root["delete_on_termination"] != true {
			t.Fatalf("root block device = %#v", root)
		}
		for index, raw := range devices[1:] {
			data := raw.(map[string]any)
			if data["source_type"] != "blank" || data["destination_type"] != "volume" || data["boot_index"] != float64(-1) || data["delete_on_termination"] != false || data["volume_type"] != "ceph-ssd1" {
				t.Fatalf("data block device %d = %#v", index+1, data)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"server":{"id":"server-1","name":"vm"}}`)
	}))
	defer server.Close()

	provider := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return server.URL + "/", nil },
		HTTPClient:      *server.Client(),
	}
	provider.UseTokenLock()
	provider.SetToken("test-token")

	created, err := (&Client{provider: provider, region: "RegionOne"}).CreateServer(context.Background(), CreateServerOpts{
		Name: "vm", FlavorID: "flavor-1", ImageID: "image-1", NetworkIDs: []string{"network-1"},
		BootVolume: &CreateServerVolumeOpts{
			Size: 80, VolumeType: "ceph-ssd1", DeleteOnTermination: true, Tag: "root",
		},
		DataVolumes: []CreateServerVolumeOpts{
			{Size: 100, VolumeType: "ceph-ssd1", Tag: "data-1"},
			{Size: 200, VolumeType: "ceph-ssd1", Tag: "data-2"},
		},
	})
	if err != nil {
		t.Fatalf("CreateServer() error = %v", err)
	}
	if created["id"] != "server-1" {
		t.Fatalf("created server = %#v", created)
	}
}

func TestGetServerEnrichesFlavorExtraSpecs(t *testing.T) {
	// The default microversion returns flavor:{id,links} with no extra_specs; GetServer
	// must resolve them by id so notification-refetched GPU servers still rate/report GPUs.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/servers/srv-1":
			fmt.Fprint(w, `{"server":{"id":"srv-1","name":"gpu","status":"ACTIVE","flavor":{"id":"flv-1","links":[]},"image":{"id":"img-1"}}}`)
		case r.URL.Path == "/flavors/flv-1":
			if got := r.Header.Get("X-OpenStack-Nova-API-Version"); got != flavorMicroversion {
				t.Fatalf("flavor GET microversion = %q, want %q", got, flavorMicroversion)
			}
			fmt.Fprint(w, `{"flavor":{"id":"flv-1","name":"g1.a100","ram":8192,"vcpus":4,"disk":40,"extra_specs":{"pci_passthrough:alias":"nvidia-a100-80gb:1"}}}`)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	provider := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return server.URL + "/", nil },
		HTTPClient:      *server.Client(),
	}
	provider.UseTokenLock()
	provider.SetToken("test-token")

	srv, err := (&Client{provider: provider, region: "RegionOne"}).GetServer(context.Background(), "srv-1")
	if err != nil {
		t.Fatalf("GetServer() error = %v", err)
	}
	flavor, ok := srv["flavor"].(map[string]any)
	if !ok {
		t.Fatalf("flavor missing/wrong shape: %#v", srv["flavor"])
	}
	// GetFlavor returns extra_specs as map[string]string; the cache consumers
	// (cloud.GPUFromFlavor) tolerate both that and the map[string]any round-trip shape.
	alias := ""
	switch specs := flavor["extra_specs"].(type) {
	case map[string]string:
		alias = specs["pci_passthrough:alias"]
	case map[string]any:
		alias, _ = specs["pci_passthrough:alias"].(string)
	}
	if alias != "nvidia-a100-80gb:1" {
		t.Fatalf("flavor extra_specs not enriched: %#v", flavor["extra_specs"])
	}
}

func TestServerToMapReAddsImageKey(t *testing.T) {
	// gophercloud tags Server.Image `json:"-"`, so a plain toMap drops the key
	// and every re-fetched server would read as image-backed.
	var volumeBacked, imageBacked servers.Server
	if err := json.Unmarshal([]byte(`{"id":"server-1","image":""}`), &volumeBacked); err != nil {
		t.Fatalf("unmarshal volume-backed: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"id":"server-2","image":{"id":"image-1"}}`), &imageBacked); err != nil {
		t.Fatalf("unmarshal image-backed: %v", err)
	}
	if got := serverToMap(&volumeBacked)["image"]; got != "" {
		t.Fatalf("volume-backed image = %#v, want \"\"", got)
	}
	image, ok := serverToMap(&imageBacked)["image"].(map[string]any)
	if !ok || image["id"] != "image-1" {
		t.Fatalf("image-backed image = %#v, want map with id image-1", image)
	}
}

func TestRebuildVolumeBackedServerUsesMicroversion293(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/servers/server-1/action" {
			t.Fatalf("request = %s %s, want POST /servers/server-1/action", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-OpenStack-Nova-API-Version"); got != "2.93" {
			t.Fatalf("nova microversion = %q, want 2.93", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"server":{"id":"server-1","name":"vm"}}`)
	}))
	defer server.Close()

	provider := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return server.URL + "/", nil },
		HTTPClient:      *server.Client(),
	}
	provider.UseTokenLock()
	provider.SetToken("test-token")

	if _, err := (&Client{provider: provider, region: "RegionOne"}).RebuildServer(
		context.Background(), "server-1", "image-2", "", "", true,
	); err != nil {
		t.Fatalf("RebuildServer() error = %v", err)
	}
}

func TestRescueVolumeBackedServerUsesMicroversion287(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/servers/server-1/action" {
			t.Fatalf("request = %s %s, want POST /servers/server-1/action", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-OpenStack-Nova-API-Version"); got != "2.87" {
			t.Fatalf("nova microversion = %q, want 2.87", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"adminPass":"temporary-password"}`)
	}))
	defer server.Close()

	provider := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return server.URL + "/", nil },
		HTTPClient:      *server.Client(),
	}
	provider.UseTokenLock()
	provider.SetToken("test-token")

	password, err := (&Client{provider: provider, region: "RegionOne"}).RescueServer(
		context.Background(), "server-1", "", true,
	)
	if err != nil {
		t.Fatalf("RescueServer() error = %v", err)
	}
	if password != "temporary-password" {
		t.Fatalf("rescue password = %q", password)
	}
}
