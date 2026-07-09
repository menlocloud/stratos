# Installing on Kubernetes

Stratos is delivered as one Helm chart that installs the three application workloads and, by default, every stateful dependency they rely on. A single `helm upgrade --install` yields a complete platform: API, customer portal, admin console, PostgreSQL, and RabbitMQ — plus, optionally, a bundled Keycloak for identity.

## Prerequisites

- A working Kubernetes cluster with a default StorageClass.
- For classic ingress: an ingress controller (such as ingress-nginx) and, for TLS, cert-manager with a ClusterIssuer. Gateway API exposure is supported as an alternative.
- A DNS name for the platform (e.g. `cloud.example.com`) pointed at your ingress.
- Helm 3.8+.

## The chart and its images

| | |
|---|---|
| Chart | `oci://ghcr.io/menlocloud/charts/stratos` |
| API image | `ghcr.io/menlocloud/stratos` (Go backend: REST + admin API + jobs) |
| Portal image | `ghcr.io/menlocloud/stratos-web` (customer SPA, served at `/`) |
| Admin image | `ghcr.io/menlocloud/stratos-admin` (admin SPA, served at `/stratos_admin`) |

Bundled dependencies, each swappable for an external instance:

| Dependency | Toggle | External counterpart |
|------------|--------|----------------------|
| PostgreSQL | `postgresql.enabled` | `externalPostgresql.*` |
| RabbitMQ | `rabbitmq.enabled` | `externalRabbitmq.*` |
| Keycloak | `keycloakx.enabled` (off by default) | any external IdP via `auth.*.issuer` (see [How Identity Works](/docs/concepts/identity)) |

The application state lives in one PostgreSQL database (`stratos`), one document-per-aggregate in a `jsonb` column. The bundled PostgreSQL subchart creates the `stratos` role and database on first boot.

## A minimal values file

```yaml
# values.yaml
api:
  # Data-at-rest encryption key — CHANGE THIS and keep it safe (see Backup).
  encryptionKey: "<a long random string>"
  # Public base URL the API embeds in mail links, redirects and MCP metadata.
  selfUrls:
    base: "https://cloud.example.com"
  # Mail gateway (optional here — you can also configure SMTP later under
  # Admin → Integrations → Mail, which is stored in the database and wins).
  mail:
    from: support@example.com
    smtp:
      host: smtp.example.com
      port: 587
      username: mailer@example.com
      existingSecret: my-smtp-secret        # holds the SMTP password / API key
      passwordKey: STRATOS_MAIL_SMTP_PASSWORD
  # Ingress is per-component; the three share one host, split by path.
  ingress:
    enabled: true
    className: "nginx"
    host: "cloud.example.com"
    path: /api
    annotations:
      cert-manager.io/cluster-issuer: letsencrypt
    tls:
      - hosts: ["cloud.example.com"]
        secretName: stratos-tls

ui:
  ingress:
    enabled: true
    className: "nginx"
    host: "cloud.example.com"
    path: /
    annotations:
      cert-manager.io/cluster-issuer: letsencrypt
    tls:
      - hosts: ["cloud.example.com"]
        secretName: stratos-tls

admin:
  ingress:
    enabled: true
    className: "nginx"
    host: "cloud.example.com"
    path: /stratos_admin
    annotations:
      cert-manager.io/cluster-issuer: letsencrypt
    tls:
      - hosts: ["cloud.example.com"]
        secretName: stratos-tls
```

With ingress on, the API answers under `/api/v1`, `/admin-api/v1`, `/openapi.json` and `/.well-known`; the portal takes `/`, the admin console `/stratos_admin`, and a bundled Keycloak (if enabled) is exposed on its own host via `keycloakx.ingress.*`.

## Running the install

```sh
helm upgrade --install stratos oci://ghcr.io/menlocloud/charts/stratos \
  --namespace stratos --create-namespace \
  -f values.yaml
```

Watch it come up:

```sh
kubectl -n stratos get pods
kubectl -n stratos get ingress
```

The API pod carries a `wait-for-db` init container, so it stays in `Init` until PostgreSQL is reachable — expect that on first boot.

To pin application versions explicitly (otherwise the chart's `appVersion` is used):

```sh
--set api.image.tag=<TAG> --set ui.image.tag=<TAG> --set admin.image.tag=<TAG>
```

## Exposing via Gateway API instead of Ingress

For a cluster on the Gateway API, turn classic ingress off and attach HTTPRoutes to your Gateway:

```yaml
api:
  selfUrls:
    base: "https://cloud.example.com"   # public URL, since it can't be derived from ingress
gateway:
  enabled: true
  parentRefs:
    - name: my-gateway
      namespace: gateways
      sectionName: https
  hostnames:
    api: api.cloud.example.com
    ui: cloud.example.com
    admin: admin.cloud.example.com
```

Leave the per-component `*.ingress.enabled` off (the default) and set `api.selfUrls.base` (and optionally `api.selfUrls.api`/`ui`/`admin`) so the API knows its public URLs.

## The encryption key — don't skip this

The API encrypts sensitive fields at rest with `STRATOS_ENCRYPTION_DEFAULT_KEY`. The chart holds it in the API Secret **`<release>-api`** under the key `encryption-key`, sourced from `api.encryptionKey` (or your own `api.existingSecret`). Set a strong value and store it somewhere safe the moment the first install finishes:

```sh
kubectl -n stratos get secret stratos-api \
  -o jsonpath='{.data.encryption-key}' | base64 -d
```

Lose this value and encrypted data cannot be recovered — see [Backup and Recovery](/docs/self-hosting/backup). To supply your own secret instead of the chart-managed one, set `api.existingSecret` to a Secret carrying the keys `db-url`, `rabbitmq-password`, and `encryption-key`.

## Pointing at external dependencies

For example, using an existing PostgreSQL rather than the bundled one:

```yaml
postgresql:
  enabled: false
externalPostgresql:
  host: pg.example.com
  port: 5432
  database: stratos
  username: stratos
  password: "…"
  sslMode: require
  # Or, instead of the fields above, a pre-created Secret whose key holds the
  # COMPLETE DSN (postgres://user:pass@host:port/db?sslmode=...):
  # existingSecret: my-pg-secret
  # secretKey: db-url
```

`externalRabbitmq` follows the same shape (`rabbitmq.enabled: false` plus the `externalRabbitmq` block). For identity, point `auth.*.issuer` at your IdP — see [How Identity Works](/docs/concepts/identity).

## Scaling for production

```yaml
api:
  replicas: 3
ui:
  replicas: 3
admin:
  replicas: 3
```

For the datastore, either run the bundled PostgreSQL in a replicated architecture (`postgresql.architecture: replication`) or, better for production, point `externalPostgresql.*` at a managed/HA PostgreSQL that has its own backup and failover.

Scale each workload manually with `api.replicas` / `ui.replicas` / `admin.replicas`; the chart ships no HorizontalPodAutoscaler.

## Once it's installed

1. Save the `<release>-api` encryption key offline (as above).
2. Open `https://cloud.example.com/stratos_admin` and sign in with an admin-realm account.

![Admin console after first sign-in](/docs-img/admin-console-first-login.png)

3. Add your OpenStack region(s) and credentials in the admin console, then set up [OpenStack Event Notifications](/docs/self-hosting/openstack-notifications).

## Upgrading

Re-run the same command with the new chart/app version:

```sh
helm -n stratos upgrade stratos oci://ghcr.io/menlocloud/charts/stratos -f values.yaml
```
