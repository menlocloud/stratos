// Package config loads Stratos runtime configuration in a way that is
// wire-compatible with the deployment contract: a mounted application.yml
// overlaid with a small set of injected environment variables.
//
// The Helm chart (deploy/chart) mounts application.yml at
// /opt/stratos/api/application.yml and injects STRATOS_DB_URL,
// STRATOS_RABBITMQ_USERNAME/PASSWORD and STRATOS_ENCRYPTION_DEFAULT_KEY as env
// vars. We reproduce exactly that merge so the chart values/secrets work
// unchanged.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// DefaultConfigPath is where the chart mounts application.yml.
const DefaultConfigPath = "/opt/stratos/api/application.yml"

type OAuth2Realm struct {
	IssuerURI string
	ClientID  string
}

type Config struct {
	Server struct {
		Port int
	}
	Management struct {
		Port int
	}
	DB struct {
		URL string // PostgreSQL DSN (primary datastore)
	}
	Rabbit struct {
		Host     string
		Port     int
		Username string
		Password string
	}
	Encryption struct {
		DefaultKey string
	}
	Self struct {
		BaseURL      string
		APIBaseURL   string
		UIBaseURL    string
		AdminBaseURL string
	}
	Auth struct {
		Main     OAuth2Realm
		Admin    OAuth2Realm
		AdminAPI OAuth2Realm
	}
	// OpenStack is the cloud connection (dev bootstrap from env; the per-region
	// ExternalService doc supersedes this later). Empty AuthURL = cloud disabled.
	OpenStack struct {
		AuthURL       string
		Region        string
		Username      string
		Password      string
		UserDomain    string
		ProjectName   string
		ProjectDomain string
		AppCredID     string
		AppCredSecret string
	}
	// Jobs gates the in-process scheduled jobs (charge cron + cloud metrics ingestion).
	// OFF by default: a plain deploy stays dormant until the gated live rating run flips
	// STRATOS_JOBS_SCHEDULER_ENABLED=true (so deploys never charge bills unexpectedly).
	Jobs struct {
		SchedulerEnabled bool // auto-start the charge/metrics crons (charges bills on a timer)
		// DebugTriggers exposes the on-demand mgmt triggers (/debug/run-{sync,metrics,charge})
		// WITHOUT auto-starting the crons — so a deploy can be driven deterministically for the
		// golden run while staying dormant (no timed bill charging).
		DebugTriggers bool
		// RemoteCharge routes the charge cron to the standalone billing service instead of rating
		// in-process: this pod still resolves each profile's cloud resources (that needs the
		// OpenStack cache, which only lives here) and POSTs them to Billing.URL to be rated.
		// Requires Billing.URL. Default off, so the in-process path remains authoritative until
		// the remote path has been validated against it.
		RemoteCharge bool
	}
	// Billing points at the standalone billing service. Empty → not configured, and any
	// billing-service-backed path stays disabled.
	Billing struct {
		URL string
		// Timeout bounds one billing call. A charge carries every billable resource for a profile,
		// so it is deliberately generous compared to an interactive request.
		Timeout time.Duration
		// CallbackToken is the shared secret the billing service presents when calling back to
		// suspend/resume clouds, activate projects or send mail. Empty → those callbacks are not
		// mounted at all. They can pause a customer's whole fleet, so they are never exposed
		// unauthenticated, network policy notwithstanding.
		CallbackToken string
	}
	LogLevel string
}

// Load reads application.yml (if present) and overlays the env vars the chart
// injects. Env always wins over the file.
func Load() (*Config, error) {
	k := koanf.New(".")

	path := envOr("STRATOS_CONFIG_FILE", DefaultConfigPath)
	if _, err := os.Stat(path); err == nil {
		if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("load %s: %w", path, err)
		}
	}

	c := &Config{}

	// Ports (yaml: server.port / management.server.port).
	c.Server.Port = intOr(k.Int("server.port"), 8080)
	c.Management.Port = intOr(k.Int("management.server.port"), 8081)

	// PostgreSQL — the primary datastore; injected purely via env by the chart.
	c.DB.URL = envOr("STRATOS_DB_URL", k.String("db.url"))

	// RabbitMQ — host/port from yaml, creds from env.
	c.Rabbit.Host = envOr("STRATOS_RABBITMQ_HOST", k.String("rabbitmq.host"))
	c.Rabbit.Port = intOr(envInt("STRATOS_RABBITMQ_PORT"), intOr(k.Int("rabbitmq.port"), 5672))
	c.Rabbit.Username = envOr("STRATOS_RABBITMQ_USERNAME", k.String("rabbitmq.username"))
	c.Rabbit.Password = envOr("STRATOS_RABBITMQ_PASSWORD", k.String("rabbitmq.password"))

	// Data-at-rest key (env). The config binds stratos.encryption.default.key (config
	// prefix "stratos.encryption.default", field "key") — read that path too, then the
	// dashed alias, so the key resolves from the same chart value either way.
	c.Encryption.DefaultKey = envOr("STRATOS_ENCRYPTION_DEFAULT_KEY",
		firstNonEmpty(k.String("stratos.encryption.default.key"), k.String("stratos.encryption.default-key")))

	// Self URLs.
	c.Self.BaseURL = envOr("STRATOS_SELF_BASE_URL", k.String("stratos.self.base-url"))
	c.Self.APIBaseURL = envOr("STRATOS_SELF_API_BASE_URL", k.String("stratos.self.api-base-url"))
	c.Self.UIBaseURL = envOr("STRATOS_SELF_UI_BASE_URL", k.String("stratos.self.ui-base-url"))
	c.Self.AdminBaseURL = envOr("STRATOS_SELF_ADMIN_BASE_URL", k.String("stratos.self.admin-base-url"))

	// Auth realms (yaml; issuer-uri may be empty when a realm uses LOCAL_IDP).
	// Env overrides let the test point Go at the in-cluster Keycloak service
	// URL so the token's `iss` (Host-derived) matches what the pod resolves.
	c.Auth.Main = realmEnv(k, "auth.main.oauth2", "AUTH_MAIN_OAUTH2")
	c.Auth.Admin = realmEnv(k, "auth.admin.oauth2", "AUTH_ADMIN_OAUTH2")
	c.Auth.AdminAPI = realmEnv(k, "auth.admin-api.oauth2", "AUTH_ADMIN_API_OAUTH2")

	// OpenStack (cloud) — standard OS_* env (dev bootstrap).
	c.OpenStack.AuthURL = envOr("OS_AUTH_URL", k.String("openstack.auth-url"))
	c.OpenStack.Region = envOr("OS_REGION_NAME", k.String("openstack.region"))
	c.OpenStack.Username = envOr("OS_USERNAME", "")
	c.OpenStack.Password = envOr("OS_PASSWORD", "")
	c.OpenStack.UserDomain = envOr("OS_USER_DOMAIN_NAME", "")
	c.OpenStack.ProjectName = envOr("OS_PROJECT_NAME", "")
	c.OpenStack.ProjectDomain = envOr("OS_PROJECT_DOMAIN_NAME", "")
	c.OpenStack.AppCredID = envOr("OS_APPLICATION_CREDENTIAL_ID", "")
	c.OpenStack.AppCredSecret = envOr("OS_APPLICATION_CREDENTIAL_SECRET", "")

	// Scheduled jobs (charge cron + cloud metrics) — OFF unless explicitly enabled.
	c.Jobs.SchedulerEnabled = boolEnv("STRATOS_JOBS_SCHEDULER_ENABLED") || k.Bool("stratos.jobs.scheduler-enabled")
	c.Jobs.DebugTriggers = boolEnv("STRATOS_JOBS_DEBUG_TRIGGERS") || k.Bool("stratos.jobs.debug-triggers")
	c.Jobs.RemoteCharge = boolEnv("STRATOS_JOBS_REMOTE_CHARGE") || k.Bool("stratos.jobs.remote-charge")

	// Standalone billing service.
	c.Billing.URL = firstNonEmpty(os.Getenv("STRATOS_BILLING_URL"), k.String("stratos.billing.url"))
	c.Billing.Timeout = 60 * time.Second
	if s := os.Getenv("STRATOS_BILLING_TIMEOUT"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			c.Billing.Timeout = d
		}
	}
	c.Billing.CallbackToken = envOr("STRATOS_BILLING_CALLBACK_TOKEN", k.String("stratos.billing.callback-token"))

	c.LogLevel = strings.ToUpper(firstNonEmpty(k.String("logging.level.root"), "INFO"))

	return c, nil
}

// Validate fails fast on missing required config — follows the fail-closed
// bootstrap principle (a half-configured service must not serve traffic).
func (c *Config) Validate() error {
	var missing []string
	if c.DB.URL == "" {
		missing = append(missing, "STRATOS_DB_URL")
	}
	if c.Rabbit.Host == "" {
		missing = append(missing, "rabbitmq.host / STRATOS_RABBITMQ_HOST")
	}
	// Remote charging without a billing URL would silently stop charging bills altogether — the
	// cron would fire, find nowhere to send the work, and log. Fail closed instead.
	if c.Jobs.RemoteCharge && c.Billing.URL == "" {
		missing = append(missing, "STRATOS_BILLING_URL (required when STRATOS_JOBS_REMOTE_CHARGE is set)")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required config: %s", strings.Join(missing, ", "))
	}
	return nil
}

func realmEnv(k *koanf.Koanf, yamlPrefix, envPrefix string) OAuth2Realm {
	return OAuth2Realm{
		IssuerURI: envOr(envPrefix+"_ISSUER_URI", k.String(yamlPrefix+".issuer-uri")),
		ClientID:  envOr(envPrefix+"_CLIENT_ID", k.String(yamlPrefix+".client-id")),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string) int {
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	var n int
	_, _ = fmt.Sscanf(v, "%d", &n)
	return n
}

func boolEnv(key string) bool {
	return strings.EqualFold(os.Getenv(key), "true")
}

func intOr(v, fallback int) int {
	if v == 0 {
		return fallback
	}
	return v
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
