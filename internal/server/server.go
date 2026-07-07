// Package server builds the public API router (:8080) and the management
// router (:8081). Resource-Server auth enforcement is live: protected
// /api/v1/** returns 401 for unauthenticated requests.
package server

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/menlocloud/stratos/internal/cloud/notification"
	"github.com/menlocloud/stratos/internal/health"
	"github.com/menlocloud/stratos/internal/platform/account"
	"github.com/menlocloud/stratos/internal/platform/admin"
	"github.com/menlocloud/stratos/internal/platform/adminapi"
	"github.com/menlocloud/stratos/internal/platform/affiliate"
	"github.com/menlocloud/stratos/internal/platform/billing"
	"github.com/menlocloud/stratos/internal/platform/catalog"
	"github.com/menlocloud/stratos/internal/platform/feature"
	"github.com/menlocloud/stratos/internal/platform/job"
	"github.com/menlocloud/stratos/internal/platform/mcp"
	"github.com/menlocloud/stratos/internal/platform/order"
	"github.com/menlocloud/stratos/internal/platform/org"
	"github.com/menlocloud/stratos/internal/platform/platformconfig"
	"github.com/menlocloud/stratos/internal/platform/project"
	"github.com/menlocloud/stratos/internal/platform/projectinvite"
	"github.com/menlocloud/stratos/internal/platform/promotion"
	"github.com/menlocloud/stratos/internal/platform/sse"
	"github.com/menlocloud/stratos/pkg/auth"
	"github.com/menlocloud/stratos/pkg/httpx"
)

// requestLogger emits one structured access-log line per request (method, path, status, latency,
// response bytes, request id) so the app's own traffic is visible in the logs — otherwise only
// startup and explicit domain events are logged and successful requests leave no trace.
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			defer func() {
				log.Info("request",
					"method", r.Method,
					"path", r.URL.Path,
					"status", ww.Status(),
					"bytes", ww.BytesWritten(),
					"ms", time.Since(start).Milliseconds(),
					"reqid", middleware.GetReqID(r.Context()),
				)
			}()
			next.ServeHTTP(ww, r)
		})
	}
}

// corsMiddleware allows the browser FE origins (the UI + admin SPAs, which are served from
// different subdomains than the api) to call the api cross-origin. The SPAs run on separate
// subdomains from the api, so cross-origin support is required. Credentialed (Bearer) → echo
// the specific Origin, never "*".
// Preflight OPTIONS is answered here (204) BEFORE auth enforcement so it never 401/405s.
func corsMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range allowedOrigins {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			allowed[o] = true
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && allowed[origin] {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Add("Vary", "Origin")
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS")
				if req := r.Header.Get("Access-Control-Request-Headers"); req != "" {
					h.Set("Access-Control-Allow-Headers", req)
				} else {
					h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, X-Requested-With")
				}
				h.Set("Access-Control-Max-Age", "3600")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// AppRouter is the public API on :8080.
func AppRouter(log *slog.Logger, corsOrigins []string, authn *auth.Authenticator, acct *account.Handler, orgH *org.Handler, roleH *org.RoleHandler, orgAuditH *org.AuditHandler, billingH *org.BillingHandler, billingCfgH *billing.ConfigHandler, projectH *project.Handler, pcfgH *platformconfig.Handler, featureH *feature.Handler, promotionH *promotion.Handler, affiliateH *affiliate.Handler, catalogH *catalog.Handler, orderH *order.Handler, inviteH *projectinvite.Handler, adminH *admin.Handler, adminAPIH *adminapi.Handler, sseH *sse.Handler, notiH *notification.Handler, jobH *job.Handler, mcpH *mcp.Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(requestLogger(log)) // access log: method/path/status/latency per request (JSON, one line each)
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware(corsOrigins)) // CORS + preflight, BEFORE auth (preflight must not 401)
	r.Use(authn.Enforce)               // 401 for unauthenticated protected paths

	// 404/405 carry the response envelope (for public unmatched paths).
	r.NotFound(httpx.NotFoundHandler())
	r.MethodNotAllowed(httpx.MethodNotAllowedHandler())

	// Ingress prefixes routed to stratos-api. /.well-known + /oauth2/* belong to
	// Keycloak, not served here.
	r.Route("/api/v1", func(r chi.Router) {
		acct.Routes(r)        // account/user slice
		orgH.Routes(r)        // organization slice
		roleH.Routes(r)       // custom org roles slice
		orgAuditH.Routes(r)   // org audit log
		billingH.Routes(r)    // billing-profile read (BillingSummary)
		billingCfgH.Routes(r) // billing-configuration read (public config)
		projectH.Routes(r)    // project slice
		pcfgH.Routes(r)       // platform configuration (public read, whitelisted)
		featureH.Routes(r)    // feature flags
		promotionH.Routes(r)  // promotion (deposit config)
		affiliateH.Routes(r)  // affiliate (cfy check + project config/log)
		catalogH.Routes(r)    // cloud catalog config (flavor-categories + image groups)
		orderH.Routes(r)      // order by-id (404 under seed; happy path when orders exist)
		inviteH.Routes(r)     // project-invite by-token (empty {} under seed)
		adminH.Routes(r)      // /admin/** surface (admin-permission gated)
		sseH.Routes(r)        // SSE real-time stream (/events/{projectId}); source connected later
		notiH.Routes(r)       // os-notification ingestion (the "Notifier URI"; permitAll whitelist)
		jobH.Routes(r)        // operator job triggers (/admin/job/*; whitelisted)
	})
	// Public Admin API: SigV4 (hmac_keys) or the admin-api OIDC realm.
	r.Route("/admin-api/v1", adminAPIH.Routes)

	// OpenAPI stub (public) so the /openapi.json ingress path resolves.
	r.Get("/openapi.json", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"openapi":"3.0.1","info":{"title":"stratos-api (go)","version":"1.0"},"paths":{}}`))
	})

	// MCP endpoint (/mcp) + RFC 9728 resource metadata. The mcp handler does its
	// own dual-scheme gate (both paths are public-listed for Enforce); tool calls
	// dispatch back through this router, so wire it as the dispatch root.
	if mcpH != nil {
		mcpH.Routes(r)
		mcpH.SetRoot(r)
	}

	return r
}

// MgmtRouter serves Actuator-compatible health on :8081 + a build/debug probe.
func MgmtRouter(h *health.Handler, cloudDebug http.HandlerFunc, jobsDebug map[string]http.HandlerFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /actuator/health", h.Liveness)
	// Read-only cloud connectivity probe (mgmt port; nil when cloud disabled).
	if cloudDebug != nil {
		mux.HandleFunc("GET /debug/cloud", cloudDebug)
	}
	// Job triggers (mgmt port, NOT the /api/v1 surface) — present only when the scheduler is
	// enabled, so a dormant deploy exposes nothing. Used to run sync/metrics/charge on demand
	// for the golden run instead of waiting on cron.
	for name, fn := range jobsDebug {
		mux.HandleFunc("POST /debug/"+name, fn)
	}
	mux.HandleFunc("GET /actuator/health/readiness", h.Readiness)
	mux.HandleFunc("GET /actuator/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"app":"stratos-api","impl":"go"}`))
	})
	// Build/debug probe (mgmt port, not the protected /api/v1 surface).
	mux.HandleFunc("GET /__build", func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, map[string]any{
			"service": "stratos-api (go)",
		})
	})
	return mux
}
