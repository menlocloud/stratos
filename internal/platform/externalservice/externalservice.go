// Package externalservice backs the `externalService` table: each region's cloud
// connection plus the encrypted `secret` (decrypted on read). It is the deployed pod's source
// of OpenStack credentials — the production replacement for the dev OS_* env bootstrap in
// cmd/api.
//
// config/secret are polymorphic free-form sub-documents, modeled as map[string]any with typed
// accessors (OpenstackConfig/OpenstackSecret) over the known keys — exactly the read-only
// fields the CloudClient + rating loop need.
package externalservice

import (
	"strings"

	"github.com/menlocloud/stratos/internal/cloud/client"
)

// ExternalServiceType values. Only CLOUD matters to the cloud slice.
const (
	TypeCloud   = "CLOUD"
	TypeCPanel  = "CPANEL"
	TypePayment = "PAYMENT"
)

// ExternalServiceStatus values.
const (
	StatusPublic   = "PUBLIC"
	StatusPrivate  = "PRIVATE"
	StatusDisabled = "DISABLED"
)

// defaultGnocchiGranularity is the default gnocchi granularity (300s).
const defaultGnocchiGranularity = 300

// ExternalService is a document in the "externalService" collection. `config` and `secret`
// are free-form sub-documents; `secret` is stored encrypted and is decrypted in-place by the
// Service on read (Service.decrypt → textcrypt.DecryptObject).
type ExternalService struct {
	ID               string         `json:"id,omitempty"`
	Name             string         `json:"name,omitempty"`
	DefaultPricePlan string         `json:"defaultPricePlan,omitempty"`
	Type             string         `json:"type,omitempty"`
	Status           string         `json:"status,omitempty"`
	Config           map[string]any `json:"config,omitempty"`
	// Secret is stored (encrypted at rest) and decrypted into memory on read. It is NEVER put on the
	// wire: this struct is not serialized to any response — admin reads go through a secret-stripped
	// path and cloud ops use it only in-memory.
	Secret any `json:"secret,omitempty"`
}

// IsDisabled / IsNotDisabled are the status helpers.
func (e *ExternalService) IsDisabled() bool    { return e.Status == StatusDisabled }
func (e *ExternalService) IsNotDisabled() bool { return !e.IsDisabled() }

// secretMap returns the decrypted secret as a map (nil if absent/not a map).
func (e *ExternalService) secretMap() map[string]any {
	m, _ := e.Secret.(map[string]any)
	return m
}

// NotificationSecret is the per-provider shared secret ceilometer must present on the
// os-notification webhook (secret.notificationSecret; encrypted at rest, decrypted on read, and
// stripped from admin read responses like every other secret field). Empty means this provider has
// not opted notification ingestion in — the webhook stays CLOSED for it. Set it via the admin
// Cloud-providers connection save (`{"secret":{"notificationSecret":"…"}}`).
func (e *ExternalService) NotificationSecret() string {
	return str(e.secretMap()["notificationSecret"])
}

// IdentityURL is config.identityUrl, normalized to end with /v3 (the auth URL ALWAYS ends in
// the v3 path).
func (e *ExternalService) IdentityURL() string {
	return ensureV3(str(e.Config["identityUrl"]))
}

// Provider is config.provider; Shared is config.shared.
func (e *ExternalService) Provider() string { return str(e.Config["provider"]) }
func (e *ExternalService) Shared() bool     { return boolean(e.Config["shared"]) }

// GnocchiGranularity returns config.gnocchiGranularity, else 300.
func (e *ExternalService) GnocchiGranularity() int {
	if g, ok := intFrom(e.Config["gnocchiGranularity"]); ok && g > 0 {
		return g
	}
	return defaultGnocchiGranularity
}

// RegionNames returns the keys of config.regions (the regions this service serves).
func (e *ExternalService) RegionNames() []string {
	regions, ok := e.Config["regions"].(map[string]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(regions))
	for k := range regions {
		out = append(out, k)
	}
	return out
}

// ServiceEnabledInRegion reports whether config.services[slug][region] is toggled on —
// the same admin Services-tab map the client menu gates on. A provider with NO services
// map at all says yes to everything (legacy docs predate the map; sync everything rather
// than nothing).
func (e *ExternalService) ServiceEnabledInRegion(slug, region string) bool {
	svcs, ok := e.Config["services"].(map[string]any)
	if !ok || len(svcs) == 0 {
		return true
	}
	regions, _ := svcs[slug].(map[string]any)
	b, _ := regions[region].(bool)
	return b
}

func (e *ExternalService) auth() map[string]any {
	m, _ := e.Config["auth"].(map[string]any)
	return m
}

// IsAppCred reports whether this provider authenticates with an application credential
// (config.auth.adminAuthType == "application_credential") — the same test ClientConfig uses to
// set AppCredID. App-cred tokens are keystone-locked to one project and CANNOT be re-scoped, so
// the tenant-write choke points refuse to build a mis-scoped client for such a provider.
func (e *ExternalService) IsAppCred() bool {
	return strings.EqualFold(str(e.auth()["adminAuthType"]), "application_credential")
}

// ClientConfig maps the (decrypted) external service to a cloud client.Config for the given
// region. password vs application_credential is selected by config.auth.adminAuthType.
// Region falls back to the service's first configured region when empty.
func (e *ExternalService) ClientConfig(region string) client.Config {
	if region == "" {
		if rs := e.RegionNames(); len(rs) > 0 {
			region = rs[0]
		}
	}
	auth := e.auth()
	secret := e.secretMap()
	cfg := client.Config{
		AuthURL: e.IdentityURL(),
		Region:  region,
	}
	if strings.EqualFold(str(auth["adminAuthType"]), "application_credential") {
		cfg.AppCredID = str(auth["applicationCredentialId"])
		cfg.AppCredSecret = str(secret["applicationCredentialSecret"])
		return cfg
	}
	// password (default): scope to the admin project. By id when adminProjectId is set (it wins in
	// client.authOptions); otherwise by NAME (adminProjectName) + project domain — so a config that
	// only knows the project name (e.g. "admin") still scopes correctly.
	cfg.Username = str(auth["adminUsername"])
	cfg.Password = str(secret["adminPassword"])
	cfg.UserDomainName = str(auth["adminDomainName"])
	cfg.ProjectID = str(auth["adminProjectId"])
	cfg.ProjectName = str(auth["adminProjectName"])
	cfg.ProjectDomainName = str(auth["adminDomainName"])
	return cfg
}

// ClientConfigForProject is ClientConfig re-scoped to a tenant project (the customer's
// externalProjectId) using the same admin credentials — admin can scope to any project, so this
// lets the platform create resources INSIDE the customer's tenant. (Application-credential auth is
// pre-scoped and cannot be re-scoped; password auth — the OpenStack default here — re-scopes by id.)
func (e *ExternalService) ClientConfigForProject(region, externalProjectID string) client.Config {
	cfg := e.ClientConfig(region)
	if externalProjectID != "" {
		cfg.ProjectID = externalProjectID
		cfg.ProjectName = ""
	}
	return cfg
}

func ensureV3(url string) string {
	if url == "" {
		return url
	}
	trimmed := strings.TrimRight(url, "/")
	if strings.HasSuffix(trimmed, "/v3") {
		return trimmed
	}
	return trimmed + "/v3"
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func boolean(v any) bool {
	b, _ := v.(bool)
	return b
}

func intFrom(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}
