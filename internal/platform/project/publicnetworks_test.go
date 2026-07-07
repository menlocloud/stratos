package project

import (
	"testing"

	"github.com/menlocloud/stratos/internal/cloud/client"
)

func TestPublicNetworkAllowList(t *testing.T) {
	nets := []client.Network{{ID: "a", Name: "ext-a"}, {ID: "b", Name: "ext-b"}}
	cases := []struct {
		name    string
		allow   []string
		wantIDs []string
		allowed map[string]bool
	}{
		{name: "nil = all allowed", allow: nil, wantIDs: []string{"a", "b"},
			allowed: map[string]bool{"a": true, "b": true, "x": true}},
		{name: "empty = none allowed", allow: []string{}, wantIDs: []string{},
			allowed: map[string]bool{"a": false, "b": false}},
		{name: "subset filters", allow: []string{"b"}, wantIDs: []string{"b"},
			allowed: map[string]bool{"a": false, "b": true}},
		{name: "unknown id excluded", allow: []string{"b", "ghost"}, wantIDs: []string{"b"},
			allowed: map[string]bool{"ghost": true, "a": false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proj := &Project{PublicNetworkIds: tc.allow}
			got := filterPublicNetworks(proj, nets)
			if len(got) != len(tc.wantIDs) {
				t.Fatalf("filter: got %d nets, want %d (%v)", len(got), len(tc.wantIDs), got)
			}
			for i, id := range tc.wantIDs {
				if got[i].ID != id {
					t.Fatalf("filter[%d]: got %s, want %s", i, got[i].ID, id)
				}
			}
			for id, want := range tc.allowed {
				if publicNetworkAllowed(proj, id) != want {
					t.Fatalf("allowed(%s): got %v, want %v", id, !want, want)
				}
			}
		})
	}
}
