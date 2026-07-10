# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Per-provider usage-metrics source for traffic billing (`config.metrics.source`: `gnocchi` default, `prometheus`, or `none`): a Prometheus-compatible endpoint (Prometheus, Mimir `X-Scope-OrgID`, VictoriaMetrics, Thanos) can now feed the hourly `instance_traffic` ingestion. Three metric schemas (`libvirt-exporter`, `ceilometer-pushgateway`, `ceilometer-exporter`), basic/bearer/custom-header auth, custom CA / insecure TLS. New admin endpoints `PUT /api/v1/admin/service/{id}/metrics-config` and `POST .../metrics-test` (live probe: liveness, schema series count, month-start retention) + admin MCP tools `set_metrics_config` / `test_metrics_config`. Billing math is gnocchi-parity (per-series max−min over the month window, decimal64 — no PromQL `increase()` extrapolation).

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

[Unreleased]: https://github.com/menlocloud/stratos/compare/v0.3.26...HEAD
[0.3.26]: https://github.com/menlocloud/stratos/releases/tag/v0.3.26
