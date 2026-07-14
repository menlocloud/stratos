# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Live project quota on the client console** — new `GET /project/{id}/quota-usage` combines Nova quota detail (microversion 2.50), Cinder `os-quota-sets?usage=true` (including per-volume-type rows) and the Stratos-managed GPU quota; partial provider failures degrade to a `warnings` list instead of an error. The dashboard gains a **Quota & usage** card with per-region scope switching, and the server / volume create flows pre-check the selected flavor / size against the live snapshot (re-validated right before submit). Provider quota races on create **and** resize/extend now surface as client-correctable 409s instead of internal errors.
- Backend-free **mock mode** for the customer console (`npm run dev:mock`): a mock OIDC session and a path-matching mock API covering ~90 endpoints with per-domain fixtures, so frontend work and the Playwright e2e suites (screens, a11y, quota) run without a backend. Production builds never include the module. (#28)
- Data tables in both consoles gained **pagination** and a **responsive card layout** on narrow viewports. (#48)

### Changed

- **Menlo design-system restyle across both consoles** — the customer and operator consoles were re-skinned to the Menlo brand (tokens, components, page layouts). (#28) A follow-up design pass quieted sortable table headers, separated stat-card trend from hint, and re-sequenced chart hues so warm colors never sit adjacent in donuts and stacks. (#32)
- Client dashboard **Top resources by cost** is now a real data table (typed columns, links to server detail), and the admin platform-configuration page validates branding logo / favicon URLs with a live image preview instead of accepting any string. (#49)

### Fixed

- Confirming a server resize twice returned `500 internal server error`: the first click finalizes the resize (nova → `ACTIVE`), but the confirm panel stayed visible while the cached status lagged, so a second click sent `confirmResize` to an already-confirmed server and nova's `409` surfaced as a raw 500. The client now treats a `409` on confirm/revert-resize as idempotent success, and the UI hides the confirm/revert buttons as soon as one is clicked. (#29)
- OpenStack notification ingestion (`POST /api/v1/notifications/{provider}/{region}`) rejected every real ceilometer notification with `400 Invalid request body`: oslo.messaging emits a space-separated, timezone-less timestamp (`2026-07-11 10:08:51.622578`) that the `timestamp` field, a plain `*time.Time`, could not decode. The field now parses oslo's format as well as RFC3339 and no longer fails the message on an unrecognized/absent timestamp (falls back to receipt time). Real-time cloud-cache sync via notifications works again; the periodic sync had been the only thing keeping the cache fresh. (#27)

## [0.3.27] - 2026-07-10

### Added

- **Ceph S3 (RGW) object-storage provider** — a second object-store backend alongside OpenStack Swift, driven purely by the RGW S3 + Admin Ops APIs (no Keystone). Per-project RGW users with encrypted credentials, S3 access-key management, bucket grants and per-bucket settings, optional static-website endpoint, per-project storage quota. Admin **Add provider** dialog gains an OpenStack | Ceph S3 switch, and ceph providers get a tailored detail page (RGW card, trimmed tabs). (#19, #20)
- Onboarding existing projects onto a Ceph S3 provider: the admin attach-external-service leg now routes ceph providers through the RGW bootstrap (`BootstrapCephOnto`) instead of the Keystone tenant bootstrap. (#20)
- **Attach provider** action on the project detail Cloud-services card — lists providers the project is not on yet and provisions the binding (shows the RGW user id for ceph-s3 bindings). Previously the backend leg existed but no UI called it. (#22)
- Per-provider usage-metrics source for traffic billing (`config.metrics.source`: `gnocchi` default, `prometheus`, or `none`): a Prometheus-compatible endpoint (Prometheus, Mimir `X-Scope-OrgID`, VictoriaMetrics, Thanos) can now feed the hourly `instance_traffic` ingestion. Three metric schemas (`libvirt-exporter`, `ceilometer-pushgateway`, `ceilometer-exporter`), basic/bearer/custom-header auth, custom CA / insecure TLS. New admin endpoints `PUT /api/v1/admin/service/{id}/metrics-config` and `POST .../metrics-test` (live probe: liveness, schema series count, month-start retention) + admin MCP tools `set_metrics_config` / `test_metrics_config`. Billing math is gnocchi-parity (per-series max−min over the month window, decimal64 — no PromQL `increase()` extrapolation). (#24)

### Changed

- The create-bucket store picker now defaults to **S3 (Ceph)** ahead of Swift for projects that have a Ceph S3 provider, so buckets are less often created on the wrong backend by mistake. Swift stays available, second. (#25)

### Fixed

- Client cloud-resource audit events (`CLOUD_RESOURCE_CREATE`/`DELETE`/`ACTION`) now record the affected resource's own kind, id, and name — so a bucket (or server, volume, …) create is findable in the audit log by its name. Previously every cloud event was stamped with the project's identity only, so it was logged but appeared as an anonymous project row. (#25)
- Admin billing-profile **Projects** tab was always empty for greenfield projects: the query matched only a project's own `billingProfileId`, but billing resolves the EFFECTIVE profile (own id, else the owning organization's) and greenfield projects leave the own id blank. The org-fallback leg is now pushed into the query, with an integration test. (#20)

## [0.3.26] - 2026-07-09

The first public release of **Stratos** — a multi-tenant self-service and billing
platform for OpenStack clouds. Operators turn a set of OpenStack regions into a
metered, self-service cloud with accounts, projects, usage-based billing, and
payments; customers get a console to run compute, storage, and networking and to
manage their spend.

### Consoles & API

- **Customer console** (`web/client`) — self-service compute (including GPU
  flavors), block storage, networks / subnets / routers, floating IPs, load
  balancers, object storage, and shares, plus billing, invoices, savings plans,
  team members, and account settings.
- **Operator console** (`web/admin`) — cloud providers, price plans and rate
  rules, currencies and taxes, invoicing, promotions, savings plans, flavor and
  image catalogs, custom menu items, login branding, message templates, users,
  organizations, billing profiles, and an audit log.
- **Go API** serving three surfaces from one process:
  - `/api/v1` — the customer API,
  - `/admin-api/v1` — the AWS SigV4-signed operator API,
  - `/mcp` — a Model Context Protocol server exposing an admin toolset to AI agents.
- **PostgreSQL storage** — all application state lives in one PostgreSQL database
  as JSONB documents; RabbitMQ carries the charge fan-out. Both can be bundled by
  the chart or brought externally.

### Billing & payments

- **Usage-based billing** — cached OpenStack resources are rated each hour against
  configurable price plans (per resource type, priced attribute, and time unit),
  accrued onto an open monthly bill, and settled from account credits.
- **GPU pricing, capacity & quota** — per-model GPU rate rules, live cluster GPU
  capacity read from the Placement API, and per-project GPU quotas enforced at the
  Stratos gate.
- **Currencies, taxes, invoicing & suspension** — a platform base currency, tax
  rules, PDF invoices and statements, and automatic balance- or due-date-based
  account suspension.
- **Payments** — card and deposit payments and refunds via Stripe, bank transfers,
  account credits, sign-up bonuses, and provisioning promotional credits.
- **Savings plans & promotions** — commitment discounts and promotion codes.

### OpenStack integration

- **Provisioning & sync** — projects are bootstrapped as Keystone tenants; servers,
  volumes, networks, images, and other resources are synced into a cache that feeds
  billing.
- **Metering & notifications** — Gnocchi usage metrics and OpenStack event
  notifications, with per-service enable/disable across one or more regions.
- **Networking controls** — a per-project external-network allow-list, plus an
  optional auto-pick of the external network for floating IPs and router gateways.

### Identity & access

- **Sign-in** via Keycloak or any OpenID Connect provider (authorization-code +
  PKCE).
- **Organizations, projects & RBAC** — organization- and project-scoped roles, user
  invitations, and organization-provisioning quotas. Organization owners and admins
  inherit access to every project in their organization.

### Deployment & documentation

- **Helm chart** (`deploy/chart`) bundling PostgreSQL, RabbitMQ, and Keycloak (each
  toggle-able), with per-component Ingress or Gateway API, external datastores, and
  CloudNativePG as options. Container images and the chart are published to
  `ghcr.io/menlocloud` on each `v*` tag.
- **In-app documentation** at `/docs` in both consoles — tenant guides, operator
  guides, and an Admin API reference.

### Security

- Third-party integration secrets (SMTP, Stripe) and cloud credentials are
  encrypted at rest.
- The operator API and MCP require a purpose-scoped credential; the unauthenticated
  key-minting debug trigger is disabled unless explicitly enabled.
- The admin PDF-template preview is rendered in a sandboxed frame.
- Tenant and project authorization is enforced on cloud and billing operations,
  including the external-network allow-list and application-credential scoping.

[Unreleased]: https://github.com/menlocloud/stratos/compare/v0.3.27...HEAD
[0.3.27]: https://github.com/menlocloud/stratos/compare/v0.3.26...v0.3.27
[0.3.26]: https://github.com/menlocloud/stratos/releases/tag/v0.3.26
