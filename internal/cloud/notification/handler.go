package notification

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/menlocloud/stratos/pkg/httpx"
)

// notificationSecretHeader carries the shared secret ceilometer must present. The endpoint is
// permitAll (ceilometer cannot send a bearer), so this shared secret is the only thing standing
// between the internet and forged cache mutations.
const notificationSecretHeader = "X-Stratos-Notification-Secret"

// Handler serves the os-notification ingestion endpoint:
// POST /api/v1/notifications/{externalServiceId}/{region}. OpenStack/ceilometer HTTP-POSTs an
// oslo.messaging notification here (the "Notifier URI" shown in the admin Connection tab). With no
// message broker wired we
// process IN-PROCESS (the chargefanout precedent) and ALWAYS return 200 — the notifier is
// fire-and-forget, a processing error must not make OpenStack retry-storm (we
// swallow exceptions, only logging them). The path is permitAll (auth.go whitelist) — ceilometer
// cannot send a bearer.
type Handler struct {
	svc       *Service
	log       *slog.Logger
	notify    func(serviceID, region, eventType string)          // optional SSE fan-out (best-effort)
	secretFor func(ctx context.Context, serviceID string) string // per-provider shared secret ("" = not configured)
}

func NewHandler(svc *Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// SetSecretResolver wires the per-provider shared-secret lookup — given the {externalServiceId} path
// segment it returns that cloud provider's configured secret (externalService secret.notificationSecret),
// or "" when none is set. Until it is wired every request is rejected (fail-closed).
func (h *Handler) SetSecretResolver(fn func(ctx context.Context, serviceID string) string) {
	h.secretFor = fn
}

// authorized reports whether the request carries the shared secret configured for THIS cloud provider
// (its externalService secret.notificationSecret). Fails CLOSED: a provider with no secret configured
// — or before the resolver is wired — rejects every request, so an anonymous caller cannot forge
// cloud-cache mutations against a provider that never opted notification ingestion in.
func (h *Handler) authorized(ctx context.Context, serviceID string, r *http.Request) bool {
	want := ""
	if h.secretFor != nil {
		want = h.secretFor(ctx, serviceID)
	}
	if want == "" {
		return false
	}
	got := r.Header.Get(notificationSecretHeader)
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// FetcherFunc adapts a closure to ResourceFetcher (so main can wire the cloud-client fetch inline).
type FetcherFunc func(ctx context.Context, externalProjectID, resourceType, externalID string) (map[string]any, bool, error)

func (f FetcherFunc) Get(ctx context.Context, externalProjectID, resourceType, externalID string) (map[string]any, bool, error) {
	return f(ctx, externalProjectID, resourceType, externalID)
}

// ResolverFunc adapts a closure to ProjectResolver.
type ResolverFunc func(ctx context.Context, externalProjectID string) (string, bool)

func (f ResolverFunc) ByExternalID(ctx context.Context, externalProjectID string) (string, bool) {
	return f(ctx, externalProjectID)
}

// SetNotifier wires an optional SSE push fired after a notification is applied to the cache.
func (h *Handler) SetNotifier(fn func(serviceID, region, eventType string)) { h.notify = fn }

func (h *Handler) Routes(r chi.Router) {
	r.Post("/notifications/{externalServiceId}/{region}", h.receive)
}

func (h *Handler) receive(w http.ResponseWriter, r *http.Request) {
	serviceID := chi.URLParam(r, "externalServiceId")
	region := chi.URLParam(r, "region")
	if !h.authorized(r.Context(), serviceID, r) {
		// Reject a forged notification (bad/missing shared secret) BEFORE any cache mutation.
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var msg OsloMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		// A malformed body is the one case we 400 (request-body decode failure).
		httpx.WriteError(w, httpx.BadRequest("Invalid request body"))
		return
	}
	// Apply the change in the BACKGROUND and return 200 immediately. Handle re-fetches the resource
	// live from OpenStack, which can take longer than the notifier's HTTP timeout; blocking on it
	// makes the notifier time out and retry-storm the same message. The context is detached (the
	// request's is canceled once we return) with its own deadline. The periodic sync is the safety
	// net if the async apply fails.
	if h.svc != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := h.svc.Handle(ctx, serviceID, region, msg); err != nil {
				if h.log != nil {
					h.log.Error("os-notification process", "serviceId", serviceID, "region", region,
						"eventType", msg.EventType, "err", err)
				}
			} else if h.notify != nil {
				h.notify(serviceID, region, msg.EventType)
			}
		}()
	}
	httpx.Empty(w) // always 200, fast
}
