// Package notification is the OpenStack os-notification ingestion path.
// OpenStack/ceilometer HTTP-POSTs an oslo notification to
// /api/v1/notifications/{externalServiceId}/{region}; Stratos routes it by event_type to a
// CloudResourceType, (admin-scoped) re-fetches the live object, and upserts/deletes the
// `cloudResource` cache — keeping the cache eventually-consistent between sync passes.
//
// The fetch (admin-scoped, sudo-to-project) and the project lookup are seams (ResourceFetcher
// / ProjectResolver), so the routing + decision logic is unit-testable without a live cloud,
// mirroring the metrics MeasureFetcher pattern.
package notification

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/menlocloud/stratos/internal/cloud"
)

// OsloMessage is the oslo.messaging notification envelope. Field names are
// the wire snake_case keys oslo emits.
type OsloMessage struct {
	MessageID   string         `json:"message_id"`
	EventType   string         `json:"event_type"`
	PublisherID string         `json:"publisher_id"`
	Priority    string         `json:"priority"`
	Timestamp   osloTime       `json:"timestamp"`
	Payload     map[string]any `json:"payload"`
}

// osloTimeLayouts are the timestamp formats a notification may carry, most-common first.
// oslo_utils emits a SPACE-separated, timezone-less stamp ("2006-01-02 15:04:05.999999") —
// NOT RFC3339 — so a plain *time.Time field fails to decode and 400s every real ceilometer
// notification. Parsing is tried across these layouts; an unrecognized stamp yields the zero
// time rather than failing the whole message (the timestamp is non-essential — Handle falls
// back to now()).
var osloTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999",
	"2006-01-02 15:04:05.999999-07:00",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05.999999",
}

// osloTime is a time.Time that decodes oslo's space-separated stamps as well as RFC3339, and
// never errors on an unparseable value (leaves the zero time).
type osloTime struct{ time.Time }

func (t *osloTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	for _, layout := range osloTimeLayouts {
		if parsed, err := time.Parse(layout, s); err == nil {
			t.Time = parsed.UTC()
			return nil
		}
	}
	return nil // unparseable timestamp must not reject the notification
}

// ParseOsloBody decodes an os-notification request body into an OsloMessage, unwrapping the
// oslo.messaging AMQP envelope that ceilometer/nova publish to RabbitMQ:
//
//	{"oslo.version": "2.0", "oslo.message": "<the notification JSON, as a string>"}
//
// The actual event_type/payload live inside that inner string, so a bridge that forwards the raw
// broker body must be unwrapped here or every event decodes to an empty (→ skipped) message. A body
// that is already the bare notification (no oslo.message) is parsed directly.
func ParseOsloBody(body []byte) (OsloMessage, error) {
	var env struct {
		OsloMessage string `json:"oslo.message"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.OsloMessage != "" {
		body = []byte(env.OsloMessage)
	}
	var msg OsloMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return OsloMessage{}, err
	}
	return msg, nil
}

// ResourceFetcher re-reads the live OpenStack object for a resource, admin-scoped + sudo to
// the owning project.
// found=false means the object is gone in OpenStack → the notification resolves to a DELETE.
type ResourceFetcher interface {
	Get(ctx context.Context, externalProjectID, resourceType, externalID string) (obj map[string]any, found bool, err error)
}

// ProjectResolver maps an OpenStack project id → the internal Stratos project id.
// ok=false when the project is unknown.
type ProjectResolver interface {
	ByExternalID(ctx context.Context, externalProjectID string) (projectID string, ok bool)
}

// BareMetalChecker reports whether a nova instance_type names a bare-metal flavor
// — decides SERVER vs BAREMETAL_SERVER. Under
// the greenfield seed the flavorCategory collection is empty, so this is false → SERVER.
type BareMetalChecker func(instanceType string) bool

// Service handles one notification end to end against the cache.
type Service struct {
	repo     *cloud.Repo
	fetch    ResourceFetcher
	projects ProjectResolver
	bareMeta BareMetalChecker
	log      *slog.Logger // optional; diagnoses why an event was skipped/applied (nil = quiet)
}

// SetLogger wires an optional logger so skipped/applied notifications are traceable (why a live
// dashboard update didn't land). Nil keeps it quiet.
func (s *Service) SetLogger(l *slog.Logger) { s.log = l }

func (s *Service) debug(msg string, args ...any) {
	if s.log != nil {
		s.log.Info("os-notification "+msg, args...)
	}
}

func NewService(repo *cloud.Repo, fetch ResourceFetcher, projects ProjectResolver, bareMeta BareMetalChecker) *Service {
	if bareMeta == nil {
		bareMeta = func(string) bool { return false }
	}
	return &Service{repo: repo, fetch: fetch, projects: projects, bareMeta: bareMeta}
}

// minimal is the externalProjectId + externalResourceId extracted from the payload,
// keyed per type by evMeta.
type minimal struct {
	externalResourceID string
	externalProjectID  string
}

// evMeta names, per CloudResourceType, the oslo payload keys: idKey = the flat "<x>_id" field;
// objKey = the nested "<x>" object holding {id, tenant_id}.
// Only the types Stratos acts on are listed.
type evMeta struct{ idKey, objKey string }

var metaByType = map[string]evMeta{
	cloud.TypeServer:            {"instance_id", "instance"},
	cloud.TypeBaremetalServer:   {"instance_id", "instance"},
	cloud.TypeVolume:            {"volume_id", "volume"},
	cloud.TypeNetwork:           {"network_id", "network"},
	cloud.TypeSubnet:            {"subnet_id", "subnet"},
	cloud.TypeRouter:            {"router_id", "router"},
	cloud.TypePort:              {"port_id", "port"},
	cloud.TypeFloatingIP:        {"floatingip_id", "floatingip"},
	cloud.TypeImage:             {"id", "image"}, // glance oslo puts the image id in payload.id (NOT resource_id)
	cloud.TypeKubernetesCluster: {"cluster_id", "cluster"},
	cloud.TypeStack:             {"stack_identity", "stack"},
	cloud.TypeShare:             {"share_id", "share"},
	cloud.TypeDNSZone:           {"id", "zone"}, // designate oslo puts the zone id in payload.id
}

// TypeForEvent maps the first dot-segment of event_type → CloudResourceType.
// ok=false for an unmapped prefix (skip). compute disambiguates
// SERVER vs BAREMETAL_SERVER via the payload instance_type + the bare-metal check.
func TypeForEvent(msg OsloMessage, bareMeta BareMetalChecker) (string, bool) {
	prefix, _, _ := strings.Cut(msg.EventType, ".")
	switch prefix {
	case "compute":
		if it, _ := msg.Payload["instance_type"].(string); it != "" && bareMeta != nil && bareMeta(it) {
			return cloud.TypeBaremetalServer, true
		}
		return cloud.TypeServer, true
	case "image":
		return cloud.TypeImage, true
	case "volume":
		return cloud.TypeVolume, true
	case "dns":
		return cloud.TypeDNSZone, true
	case "network":
		return cloud.TypeNetwork, true
	case "subnet":
		return cloud.TypeSubnet, true
	case "floatingip":
		return cloud.TypeFloatingIP, true
	case "router":
		return cloud.TypeRouter, true
	case "magnum":
		return cloud.TypeKubernetesCluster, true
	case "port":
		return cloud.TypePort, true
	case "security_group":
		return cloud.TypeSecurityGroup, true
	case "orchestration":
		return cloud.TypeStack, true
	case "share":
		return cloud.TypeShare, true
	default:
		return "", false
	}
}

// minimalInfo extracts the minimal ids from the payload: externalResourceId =
// payload["<x>_id"] else payload["<x>"]["id"]; externalProjectId = payload["tenant_id"] else
// payload["<x>"]["tenant_id"] else payload["project_id"].
func minimalInfo(typ string, payload map[string]any) minimal {
	m := metaByType[typ]
	id := strVal(payload, m.idKey)
	if id == "" && m.objKey != "" {
		id = nestedStr(payload, m.objKey, "id")
	}
	proj := strVal(payload, "tenant_id")
	if proj == "" && m.objKey != "" {
		proj = nestedStr(payload, m.objKey, "tenant_id")
	}
	if proj == "" {
		proj = strVal(payload, "project_id")
	}
	return minimal{externalResourceID: id, externalProjectID: proj}
}

// Handle processes one notification message.
func (s *Service) Handle(ctx context.Context, serviceID, region string, msg OsloMessage) error {
	typ, ok := TypeForEvent(msg, s.bareMeta)
	if !ok {
		s.debug("skip: unmapped event_type", "eventType", msg.EventType)
		return nil // unmapped event_type → skip
	}
	info := minimalInfo(typ, msg.Payload)
	if info.externalResourceID == "" {
		s.debug("skip: no resource id in payload", "eventType", msg.EventType, "type", typ)
		return nil // prepareMinimalInfo: blank resource id → skip
	}

	// Resolve the internal project: by external project id, else by a cached resource's project
	// (fallback). No project → skip.
	projectID := ""
	if info.externalProjectID != "" {
		if pid, ok := s.projects.ByExternalID(ctx, info.externalProjectID); ok {
			projectID = pid
		}
	}
	if projectID == "" {
		if cr, err := s.repo.FindByServiceIDAndExternalID(ctx, serviceID, info.externalResourceID); err != nil {
			return err
		} else if cr != nil {
			projectID = cr.ProjectID
		}
	}
	if projectID == "" {
		s.debug("skip: unresolvable project", "eventType", msg.EventType, "type", typ,
			"extProjectId", info.externalProjectID, "resourceId", info.externalResourceID)
		return nil // unresolvable project → skip
	}
	s.debug("applying", "eventType", msg.EventType, "type", typ, "resourceId", info.externalResourceID,
		"projectId", projectID, "delete", strings.Contains(msg.EventType, "delete"))

	// processOsNotification: a delete event, or a live object that no longer exists, → DELETE;
	// otherwise re-fetch the object and CREATE_UPDATE the cache with it.
	isDelete := strings.Contains(msg.EventType, "delete")
	var obj map[string]any
	if !isDelete {
		o, found, err := s.fetch.Get(ctx, info.externalProjectID, typ, info.externalResourceID)
		if err != nil {
			return err
		}
		if !found {
			isDelete = true
		} else {
			obj = o
		}
	}

	now := time.Now().UTC()
	if isDelete {
		cr, err := s.repo.FindByServiceIDAndExternalID(ctx, serviceID, info.externalResourceID)
		if err != nil || cr == nil {
			return err
		}
		// Scope the delete to the resolved project: a notification whose tenant resolves to one
		// project must not archive a cached resource owned by another project (a forged/mismatched
		// notification must not delete across the tenant boundary).
		if !sameProject(cr.ProjectID, projectID) {
			return nil
		}
		return s.repo.DeleteAndArchive(ctx, cr, now)
	}

	ts := now
	if !msg.Timestamp.IsZero() {
		ts = msg.Timestamp.UTC()
	}
	cr := &cloud.CloudResource{
		ExternalID: info.externalResourceID,
		Type:       typ,
		ProjectID:  projectID,
		ServiceID:  serviceID,
		Region:     region,
		Data:       obj,
		CreatedAt:  &ts,
		UpdatedAt:  &ts,
	}
	_, err := s.repo.Insert(ctx, cr) // upsert (create-or-update)
	return err
}

// sameProject reports whether the cached resource belongs to the notification's resolved project.
// A blank resolved project (should not reach here) is treated as a non-match to fail closed.
func sameProject(resourceProjectID, resolvedProjectID string) bool {
	return resolvedProjectID != "" && resourceProjectID == resolvedProjectID
}

func strVal(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func nestedStr(m map[string]any, objKey, field string) string {
	if m == nil {
		return ""
	}
	inner, ok := m[objKey].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := inner[field].(string)
	return s
}
