# Configuration

How the Stratos API (`stratos-api`) is configured, for contributors and operators.
The source of truth is `internal/config/config.go` (plus a few subsystems that read
their own env, noted below). Deployment wiring lives in `deploy/chart`.

## Sources and precedence

Configuration is a **config file overlaid by environment variables**, and
**env always wins over the file**.

- **Config file:** `application.yml`, loaded from `/opt/stratos/api/application.yml`
  by default. Override the path with `STRATOS_CONFIG_FILE`. If the file is absent,
  the service runs on env + defaults alone.
- **Environment variables:** injected by the deployment (secrets, connection
  strings, toggles). Any env key listed below takes priority over its file key.

The Helm chart mounts a rendered `application.yml` (from `values.yaml`) at the
default path and injects the secret/connection env vars into the pod
(`deploy/chart/templates/api/configmap.yaml` and `deploy/chart/templates/api/deployment.yaml`).

Required config is validated at startup (`Config.Validate`): the service refuses
to start without `STRATOS_DB_URL` and a RabbitMQ host.

## Configuration reference

Legend: **env** = environment variable, **file** = `application.yml` key, **helm**
= `values.yaml` path.

### Server

| Concern | env | file | helm | Default | Meaning |
|---------|-----|------|------|---------|---------|
| API port | — | `server.port` | fixed `8080` | `8080` | Public API listener. |
| Management port | — | `management.server.port` | fixed `8081` | `8081` | Health probes + operator debug triggers. |
| Log level | — | `logging.level.root` | `api.logLevel` | `INFO` | Root log level. |

### PostgreSQL (required)

| Concern | env | file | helm | Default | Meaning |
|---------|-----|------|------|---------|---------|
| Connection DSN | `STRATOS_DB_URL` | `db.url` | derived from `postgresql.*` or `externalPostgresql.*` | — (required) | PostgreSQL connection string (`postgres://user:pass@host:port/stratos?sslmode=…`). |

PostgreSQL is the sole primary datastore: every document kind is a
`(id text primary key, doc jsonb not null)` table (see
[data-model.md](data-model.md)). The chart builds the DSN from the bundled
`postgresql` subchart or from `externalPostgresql.*` and injects it as env;
the credentials come from a secret (or `externalPostgresql.existingSecret`).
Documents are (de)serialized with the standard-library `encoding/json` (no ORM
or external codec) and stored verbatim in each table's `doc` jsonb column.

### RabbitMQ (host required)

| Concern | env | file | helm | Default | Meaning |
|---------|-----|------|------|---------|---------|
| Host | `STRATOS_RABBITMQ_HOST` | `rabbitmq.host` | derived from `rabbitmq.*` / `externalRabbitmq.host` | — (required) | Broker host. |
| Port | `STRATOS_RABBITMQ_PORT` | `rabbitmq.port` | derived | `5672` | Broker port. |
| Username | `STRATOS_RABBITMQ_USERNAME` | `rabbitmq.username` | secret | — | Broker user. |
| Password | `STRATOS_RABBITMQ_PASSWORD` | `rabbitmq.password` | secret | — | Broker password. |

### Encryption (data-at-rest key)

| Concern | env | file | helm | Default | Meaning |
|---------|-----|------|------|---------|---------|
| Default key | `STRATOS_ENCRYPTION_DEFAULT_KEY` | `stratos.encryption.default.key` (alias `default-key`) | `encryption.bcrypt.password` / `encryption.bcrypt.existingPasswordSecret` | — | Key used to encrypt sensitive fields at rest. |

The chart's `encryption.provider` is `bcrypt`; it renders a secret and injects the
key as env.

### Self URLs

Used to build absolute links (mail, redirects, MCP resource metadata).

| Concern | env | file | helm | Meaning |
|---------|-----|------|------|---------|
| Base URL | `STRATOS_SELF_BASE_URL` | `stratos.self.base-url` | `global.baseUrl` | Overall public base URL. |
| API base URL | `STRATOS_SELF_API_BASE_URL` | `stratos.self.api-base-url` | `api.baseUrl` (else derived from ingress) | Public API URL (also the MCP resource base). |
| UI base URL | `STRATOS_SELF_UI_BASE_URL` | `stratos.self.ui-base-url` | `ui.baseUrl` (else derived) | Customer console URL. |
| Admin base URL | `STRATOS_SELF_ADMIN_BASE_URL` | `stratos.self.admin-base-url` | `admin.baseUrl` (else derived) | Admin console URL. |

### OIDC realms

Three realm configs, each an issuer URI + client id. See `docs/auth.md` for the
identity model. Empty issuer = realm disabled (tokens for it are rejected).

| Realm | env (issuer / client) | file | helm |
|-------|-----------------------|------|------|
| Customer (`clients`) | `AUTH_MAIN_OAUTH2_ISSUER_URI` / `AUTH_MAIN_OAUTH2_CLIENT_ID` | `auth.main.oauth2.issuer-uri` / `.client-id` | issuer: `externalOpenid.issuer` (or bundled Keycloak `clients` realm); client: `ui.oauth2.clientId` (`stratos-ui`) |
| Operator (`master`) | `AUTH_ADMIN_OAUTH2_ISSUER_URI` / `AUTH_ADMIN_OAUTH2_CLIENT_ID` | `auth.admin.oauth2.issuer-uri` / `.client-id` | issuer: `externalOpenid.adminIssuer` (or bundled `master` realm); client: `admin.oauth2.clientId` (`stratos-admin`) |
| Admin API | `AUTH_ADMIN_API_OAUTH2_ISSUER_URI` / `AUTH_ADMIN_API_OAUTH2_CLIENT_ID` | `auth.admin-api.oauth2.issuer-uri` / `.client-id` | issuer: `adminApi.oauth2.issuerUri`; client: `adminApi.oauth2.clientId` (`stratos-admin-api`) |

### OpenStack cloud

Standard `OS_*` env (env-only for credentials). An empty `OS_AUTH_URL` **disables**
the cloud connection entirely.

| Concern | env | file | Meaning |
|---------|-----|------|---------|
| Auth URL | `OS_AUTH_URL` | `openstack.auth-url` | Keystone endpoint. Empty = cloud disabled. |
| Region | `OS_REGION_NAME` | `openstack.region` | Region name. |
| Username / Password | `OS_USERNAME` / `OS_PASSWORD` | — | Password auth. |
| User domain | `OS_USER_DOMAIN_NAME` | — | User domain. |
| Project name / domain | `OS_PROJECT_NAME` / `OS_PROJECT_DOMAIN_NAME` | — | Scoped project. |
| App credential | `OS_APPLICATION_CREDENTIAL_ID` / `OS_APPLICATION_CREDENTIAL_SECRET` | — | Application-credential auth (alternative to password). |

### Scheduled jobs (billing / metrics crons)

All **off by default** — a plain deploy stays dormant and never charges bills
unexpectedly.

| Concern | env | file | Default | Meaning |
|---------|-----|------|---------|---------|
| Scheduler | `STRATOS_JOBS_SCHEDULER_ENABLED` | `stratos.jobs.scheduler-enabled` | `false` | Auto-start the charge/metrics crons (charges bills on a timer). |
| Debug triggers | `STRATOS_JOBS_DEBUG_TRIGGERS` | `stratos.jobs.debug-triggers` | `false` | Expose on-demand `POST :8081/debug/run-*` triggers **without** starting the crons — for deterministic, manual runs. |
| Rabbit fanout | `STRATOS_JOBS_RABBIT_FANOUT` | `stratos.jobs.rabbit-fanout` | `false` | Route the charge cron through RabbitMQ (one message per active profile) instead of the in-process loop. |
| Default network MTU | `STRATOS_DEFAULT_NETWORK_MTU` | `api.network.defaultMtu` (Helm) | _unset_ | MTU stamped on client-created networks. Unset/0 leaves it to neutron's provider default (e.g. the geneve/vxlan value); set e.g. `1500` to force a fixed MTU. |

When the scheduler or debug triggers are enabled, the management port also exposes
operator triggers such as `POST :8081/debug/gen-hmac-key` (mint an Admin API key —
see `docs/auth.md`).

### Mail / SMTP

Mail is configured via `STRATOS_MAIL_*` **environment variables**
(`internal/platform/mail/mail.go`). If `STRATOS_MAIL_SMTP_HOST` is unset the
service uses a no-op mailer and gated email side-effects silently do nothing.

| Concern | env | Default | Meaning |
|---------|-----|---------|---------|
| SMTP host | `STRATOS_MAIL_SMTP_HOST` | — (unset ⇒ no-op mailer) | Relay host. |
| SMTP port | `STRATOS_MAIL_SMTP_PORT` | `587` | Relay port. |
| SMTP username | `STRATOS_MAIL_SMTP_USERNAME` | — | Relay auth user. |
| SMTP password | `STRATOS_MAIL_SMTP_PASSWORD` | — | Relay auth password. |
| From address | `STRATOS_MAIL_FROM` | — | Envelope-from / sender. |
| Business name | `STRATOS_MAIL_BUSINESS_NAME` | `Stratos` | Sender display / branding in templates. |

Set these in Helm via `api.extraEnvVars`. (The chart's `smtp.*` values render an
`smtp` block into `application.yml`, but the running service reads mail from the
`STRATOS_MAIL_*` env vars above — wire mail through `api.extraEnvVars`.)

### Feature set

The available feature set is a fixed list in code (`internal/platform/feature`:
`billing`, `search`, `mailchimp`) and
is not config-driven. There is no license mechanism — Stratos is open-source and
imposes no licensed limits. The meaningful **runtime toggles** are the
scheduled-jobs flags above and per-region service enablement managed at runtime
(not via this config file).

### Other chart values

`sentry.dsn` renders into `application.yml`, but the current API binary has no
Sentry integration and does not consume it.

## Bundled vs external dependencies

The chart can run everything in-cluster or point at your own managed services.
Toggle the bundled component off and fill in the matching `external*` block:

| Dependency | Bundle toggle | Bring-your-own |
|------------|---------------|----------------|
| PostgreSQL | `postgresql.enabled` (bitnami subchart) or `cnpg.enabled` (CloudNativePG operator) | `externalPostgresql.*` |
| RabbitMQ | `rabbitmq.enabled` | `externalRabbitmq.*` |
| Keycloak (OIDC) | `keycloakx.enabled` (codecentric/keycloakx) | `externalOpenid.issuer` / `externalOpenid.adminIssuer` (+ client ids) |

For the deployment side — ingress, TLS, Gateway API routes, secrets wiring, and the
external-IdP checklist — see `deploy/chart/README.md` and
`deploy/chart/values.yaml` (a minimal starting point is in
`values-dev.yaml`).
