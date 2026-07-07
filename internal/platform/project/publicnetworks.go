package project

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/menlocloud/stratos/internal/cloud/client"
	"github.com/menlocloud/stratos/pkg/httpx"
)

// publicnetworks.go = the client read surface for the project's allowed public
// (router:external) networks: GET /project/{id}/public-networks → the provider's external
// networks filtered through the project's publicNetworkIds allow-list (nil = all allowed,
// empty = none). The allow-list itself is admin-managed (PUT /admin/project/{id}/public-networks).

// publicNetworks handles GET /api/v1/project/{id}/public-networks. Membership-gated (read,
// like cloudResourceList); best-effort empty list when the cloud is unreachable.
func (h *Handler) publicNetworks(w http.ResponseWriter, r *http.Request) {
	u, ok := h.principal(w, r)
	if !ok {
		return
	}
	proj, err := h.svc.GetProject(r.Context(), u.Sub, chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	out := []client.Network{}
	if cc, ok := h.tryTenantClient(r.Context(), proj, h.resolveServiceID(r, proj)); ok {
		if nets, err := cc.ListExternalNetworks(r.Context()); err == nil {
			out = filterPublicNetworks(proj, nets)
		}
	}
	httpx.List(w, out)
}

// publicNetworkAllowed reports whether an external network id is enabled for the project:
// nil PublicNetworkIds = all allowed (the default), empty slice = none.
func publicNetworkAllowed(proj *Project, netID string) bool {
	if proj.PublicNetworkIds == nil {
		return true
	}
	for _, id := range proj.PublicNetworkIds {
		if id == netID {
			return true
		}
	}
	return false
}

// filterPublicNetworks keeps only the external networks the project's allow-list permits
// (never nil, so the response serializes as []).
func filterPublicNetworks(proj *Project, nets []client.Network) []client.Network {
	out := make([]client.Network, 0, len(nets))
	for _, n := range nets {
		if publicNetworkAllowed(proj, n.ID) {
			out = append(out, n)
		}
	}
	return out
}
