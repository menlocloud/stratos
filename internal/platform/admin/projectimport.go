package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/menlocloud/stratos/internal/pgdoc"
	"github.com/menlocloud/stratos/pkg/httpx"
)

// projectimport.go implements the OpenStack project-import surface (/api/v1/admin/project-import).
// NEITHER endpoint is already registered in
// handler.go, so both are added here.
//
// Call graph:
//
//	GET  /{externalServiceId}              projectImportFetch  — ADMIN_PROJECT_READ
//	POST /bulk-import/{externalServiceId}  projectImportBulk   — ADMIN_PROJECT_IMPORT
//
// Both first resolve the ExternalService (datastore read + decrypt). A missing service yields
// HTTP 500 "Service not found: %s" (the same missing-record→500 precedent as platformConfigByID).
//
// ⚠ CLOUD, not wired: projectImportFetch makes LIVE Keystone calls (list projects, users, and role
// assignments) to enumerate the OpenStack projects/users/roles and diff them against the linked
// stratos projects. There is no datastore-state of its own to persist (it only READS stratos projects
// to compute linkage), so after the ExternalService lookup it returns 501 — it never calls the live
// cloud.
//
// projectImportBulk is IN SCOPE (a pure datastore write — the import path itself makes NO live cloud
// call): for each request whose stratosProjectId is blank it inserts a new ENABLED Project doc whose
// single service is { serviceId: <externalServiceId>, config:{ openstackProjectId: <os project id> }}.
// Per-item exceptions are caught and logged+continued, then it always returns "Successful operation" —
// so this endpoint never fails after the ExternalService lookup. Audit (an IMPORT admin event) is deferred.

const projectImportReadPerm = "admin:project:read"
const projectImportImportPerm = "admin:project:import"

const projectImportExternalServiceCollection = "externalService"
const projectImportProjectCollection = "project"

// routeProjectImport registers the project-import routes. The /bulk-import/{…} path is
// a distinct prefix so its param does not collide with the bare /{externalServiceId} GET; both reuse
// the param name `externalServiceId` at their own (different) tree positions, so there is no chi
// conflict with handler.go's existing /service/{id} etc. (different first segment, `project-import`).
func (h *Handler) routeProjectImport(r chi.Router) {
	r.Get("/project-import/{externalServiceId}", h.projectImportFetch)
	r.Post("/project-import/bulk-import/{externalServiceId}", h.projectImportBulk)
}

// projectImportServiceNotFound builds the exact "Service not found: %s" message, mapped to HTTP 500,
// code 500.
func projectImportServiceNotFound(id string) *httpx.HTTPError {
	return httpx.NewError(http.StatusInternalServerError, http.StatusInternalServerError,
		fmt.Sprintf("Service not found: %s", id))
}

// projectImportFetch resolves the ExternalService (→ 500 if absent),
// then list the OpenStack projects LIVE (admin GET /v3/projects) and diff each against the stratos
// projects already linked to this service (services[].serviceId == esID &&
// services[].config.openstackProjectId == keystone project id). Each entry carries the keystone
// {id,name} plus stratosProjectId ("" when unlinked → importable). Gated ADMIN_PROJECT_READ.
func (h *Handler) projectImportFetch(w http.ResponseWriter, r *http.Request) {
	if !h.require(w, r, projectImportReadPerm) {
		return
	}
	esID := chi.URLParam(r, "externalServiceId")
	raw, err := h.repo.FindByIDRaw(r.Context(), projectImportExternalServiceCollection, esID)
	if httpx.WriteError(w, err) {
		return
	}
	if raw == nil {
		// Missing external service → 500.
		httpx.WriteError(w, projectImportServiceNotFound(esID))
		return
	}
	// No cloud client on this deployment → empty importable list (parity with the other live reads).
	if h.esSvc == nil || h.cloudNew == nil {
		httpx.List(w, []any{})
		return
	}
	es, err := h.esSvc.Get(r.Context(), esID)
	if err != nil || es == nil {
		httpx.WriteError(w, projectImportServiceNotFound(esID))
		return
	}
	cc, err := h.cloudNew(r.Context(), es.ClientConfig(h.region))
	if err != nil {
		httpx.WriteError(w, httpx.BadRequest("OpenStack authentication failed: "+err.Error()))
		return
	}
	// linkage: openstack project id → stratos project id, over projects that reference this service.
	linked := map[string]string{}
	linkedDocs, _ := h.repo.ListRawFiltered(r.Context(), projectImportProjectCollection,
		pgdoc.M{"services": pgdoc.M{"$contains": pgdoc.M{"serviceId": esID}}})
	for _, p := range linkedDocs {
		pid := projectImportExternalServiceID(p, "")
		for _, sv := range asAnyArray(p["services"]) {
			svm, ok := sv.(pgdoc.M)
			if !ok {
				if mm, ok2 := sv.(map[string]any); ok2 {
					svm = pgdoc.M(mm)
				} else {
					continue
				}
			}
			if str(svm["serviceId"]) != esID {
				continue
			}
			cfg, _ := svm["config"].(pgdoc.M)
			if cfg == nil {
				if mm, ok := svm["config"].(map[string]any); ok {
					cfg = pgdoc.M(mm)
				}
			}
			if opid := str(cfg["openstackProjectId"]); opid != "" && pid != "" {
				linked[opid] = pid
			}
		}
	}
	out := []map[string]any{}
	for _, kp := range keystoneArray(r.Context(), cc, es.IdentityURL(), "/projects", "projects") {
		m, ok := kp.(map[string]any)
		if !ok {
			continue
		}
		id := str(m["id"])
		out = append(out, map[string]any{
			"project":          map[string]any{"id": id, "name": str(m["name"])},
			"stratosProjectId": linked[id],
			"users":            []any{},
		})
	}
	httpx.List(w, out)
}

// asAnyArray normalizes a array field (pgdoc.A / []any / []pgdoc.M) to []any for iteration.
func asAnyArray(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	default:
		return nil
	}
}

// openStackImportUser is the OpenStack user shape (id/name/email/enabled/roles) on the bulk-import body.
type openStackImportUser struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Email   string   `json:"email"`
	Enabled bool     `json:"enabled"`
	Roles   []string `json:"roles"`
}

// openStackImportProject holds the KeystoneProject fields the import path reads (id + name).
type openStackImportProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// openStackProjectImportReq is a bulk-import request element. Only
// stratosProjectId (the "already linked" guard) and project.{id,name} are read on import;
// users is carried for shape fidelity but unused on import.
type openStackProjectImportReq struct {
	StratosProjectID string                  `json:"stratosProjectId"`
	Users            []openStackImportUser   `json:"users"`
	Project          *openStackImportProject `json:"project"`
}

// projectImportBulk resolves the ExternalService (→ 500 if
// absent), then for each request whose stratosProjectId is blank inserts a new ENABLED Project doc.
// Per-item failures are swallowed (logged + continue); the endpoint always returns
// "Successful operation" after the lookup. Gated ADMIN_PROJECT_IMPORT.
func (h *Handler) projectImportBulk(w http.ResponseWriter, r *http.Request) {
	if !h.require(w, r, projectImportImportPerm) {
		return
	}
	esID := chi.URLParam(r, "externalServiceId")

	es, err := h.repo.FindByIDRaw(r.Context(), projectImportExternalServiceCollection, esID)
	if httpx.WriteError(w, err) {
		return
	}
	if es == nil {
		// Missing external service → 500 (before any import).
		httpx.WriteError(w, projectImportServiceNotFound(esID))
		return
	}

	var reqs []openStackProjectImportReq
	if err := json.NewDecoder(r.Body).Decode(&reqs); err != nil {
		httpx.WriteError(w, httpx.BadRequest("Invalid request body"))
		return
	}

	// The external service's String id (_id→id). Use the looked-up
	// _id (a string id) so the stored ProjectExternalService.serviceId matches exactly.
	serviceID := projectImportExternalServiceID(es, esID)

	var imported []string
	for i := range reqs {
		req := reqs[i]
		// Skip requests already linked to an stratos project.
		if req.StratosProjectID != "" {
			continue
		}
		// Build + save a new project; per-item exceptions are caught and
		// logged+continued (so a malformed/empty project never fails the whole import).
		if req.Project == nil {
			continue
		}
		doc := projectImportNewProjectDoc(req.Project, serviceID)
		saved, err := h.repo.InsertDoc(r.Context(), projectImportProjectCollection, doc)
		if err != nil {
			// Log and continue — never propagate per-item failures.
			continue
		}
		if id, _ := saved["_id"].(string); id != "" {
			imported = append(imported, id)
		}
		// TODO(audit): write an admin audit event when a project is imported.
	}

	// Sync the freshly imported projects in the background so their existing
	// cloud resources show up right away — the import itself is a pure
	// datastore write and would otherwise leave the resource cache empty
	// until the next sync cron (which a dormant deploy never runs).
	if len(imported) > 0 && h.projectCloud != nil && h.projectCloud.Sync != nil {
		go func(ids []string) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			for _, pid := range ids {
				if err := h.projectCloud.Sync(ctx, pid, ""); err != nil {
					slog.Error("post-import project sync failed", "project", pid, "err", err)
				}
			}
		}(imported)
	}

	// Success response → "Successful operation".
	httpx.OK(w, "Successful operation")
}

// projectImportExternalServiceID extracts the external service's String id from the raw doc
// (_id → id). Falls back to the path id when the field is missing/odd.
func projectImportExternalServiceID(es pgdoc.M, fallback string) string {
	if es == nil {
		return fallback
	}
	if v, ok := es["_id"]; ok {
		switch t := v.(type) {
		case string:
			if t != "" {
				return t
			}
		case fmt.Stringer:
			if s := t.String(); s != "" {
				return s
			}
		}
	}
	return fallback
}

// projectImportNewProjectDoc builds the stored JSON for a newly imported Project:
// name = os project name, status = ENABLED, empty memberships/services/customInfo, and a single
// ProjectExternalService { serviceId, config:{ openstackProjectId } } appended to services.
// The builder sets memberships=[] / services=[…] / customInfo={} explicitly
// (non-null empties are kept), so they are always emitted.
func projectImportNewProjectDoc(p *openStackImportProject, serviceID string) pgdoc.M {
	projectExternalService := pgdoc.M{
		"serviceId": serviceID,
		"config":    pgdoc.M{"openstackProjectId": p.ID},
	}
	doc := pgdoc.M{
		"name":        p.Name,
		"status":      "ENABLED",
		"memberships": []any{},
		"services":    []any{projectExternalService},
		"customInfo":  pgdoc.M{},
	}
	return doc
}
