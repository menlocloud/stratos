# OpenStack Notifications

How to feed OpenStack resource-lifecycle events into Stratos for near-real-time
dashboard updates.

This document is for **operators** wiring a cloud into Stratos. For how the
cache is modelled and rated, see [cloud-integration.md](cloud-integration.md).

---

## What this is, and why it's optional

Stratos keeps a PostgreSQL cache of every customer's cloud resources and bills
against that cache (never straight off live OpenStack). Two paths keep the cache
honest:

- **Sync** — a periodic reconcile that lists live OpenStack and makes the cache
  match. Authoritative, but only as fresh as its interval.
- **Notifications** — this feature. A webhook fed by oslo.messaging events that
  applies single-resource changes within seconds of them happening.

Notifications are **optional**: with them off, the periodic sync still keeps the
cache correct — just not instantly. Turn them on when you want a customer's
dashboard to reflect a create/delete/resize almost immediately instead of
waiting for the next sync.

---

## Architecture

```text
OpenStack services  ──emit──▶  RabbitMQ  ──consumed by──▶  notifier bridge  ──HTTP POST──▶  Stratos webhook
(nova, neutron, …)             (oslo topic)                 (you run this)                   /api/v1/notifications/{id}/{region}
```

**Stratos ships only the receiver** — the webhook endpoint. It does **not** dial
into your RabbitMQ. So yes: you add a **separate component** (a small "notifier"
sidecar/deployment) that connects to *your* RabbitMQ, consumes the OpenStack
notification stream, and POSTs each event to the Stratos webhook. That bridge is
the manifest you have to add.

Why a bridge instead of Stratos consuming RabbitMQ directly: the message broker
belongs to the cloud, not to Stratos. Keeping Stratos a pure HTTP receiver means
it needs no AMQP credentials, no network path into the cloud's control plane, and
one provider's broker outage can't stall another's ingestion.

---

## Step 1 — Make OpenStack emit notifications

Configure oslo.messaging notifications on the OpenStack services you care about
(nova, neutron, cinder, glance, designate, heat, magnum, manila). In each
service's config:

```ini
[oslo_messaging_notifications]
transport_url = rabbit://openstack:<password>@<rabbitmq-host>:5672//
driver = messagingv2
topics = notifications
```

**Kolla Ansible:** enabling ceilometer turns notifications on across the stack —
in `globals.yml`:

```yaml
enable_ceilometer: "yes"
```

After this, OpenStack publishes lifecycle events (e.g. `compute.instance.create.end`,
`volume.delete.end`) to the `notifications` topic on RabbitMQ.

---

## Step 2 — Get the webhook URL and set the secret

In the admin console: **Settings → Cloud providers → [provider]**, the
**OpenStack Notifier URI** section shows one URL per configured region:

```text
https://cloud.<your-domain>/api/v1/notifications/<serviceId>/<region>
```

- `<serviceId>` is the cloud provider's external-service id (filled in for you).
- `<region>` is each configured region (e.g. `RegionOne`).

Set a **Notification secret** in the same section. The webhook is
**fail-closed**: until a secret is set it rejects every request, and once set it
accepts a request only if the caller sends that secret in the
`X-Stratos-Notification-Secret` header. The secret is stored encrypted and never
returned on reads.

> The endpoint is unauthenticated in the bearer-token sense (the notifier can't
> mint an OAuth token), so this shared secret is the **only** thing standing
> between the internet and forged cache mutations. Treat it like a password and
> send it over TLS only.

---

## Step 3 — Run the notifier bridge

Stratos ships the bridge as `stratos-notifier` ([cmd/notifier](../cmd/notifier/main.go)):
a small AMQP consumer that subscribes to the OpenStack notification exchanges
(`nova`, `neutron`, `cinder`, `glance`, `heat`, `magnum`, `manila`, `designate`)
on its own durable queue and re-posts each raw oslo.messaging body to the region's
Notifier URI with the `X-Stratos-Notification-Secret` header.

CI publishes the image to ghcr.io (`.github/workflows/docker.yml`): a `v*` tag
publishes `ghcr.io/menlocloud/stratos-notifier:<tag>`, a push to `main` publishes
`:dev-<short-sha>`. Pin the manifest's `image:` to a released tag — no manual
build needed. (To build locally anyway: `docker build -f deploy/notifier.Dockerfile
-t ghcr.io/menlocloud/stratos-notifier:dev .`)

Then edit the two secret values in
[deploy/notifier/stratos-notifier.yaml](../deploy/notifier/stratos-notifier.yaml)
(`rabbitmq-password` and `target-secret`) and the RabbitMQ address / Notifier URI,
and apply it:

```sh
kubectl apply -f deploy/notifier/stratos-notifier.yaml
```

Configuration is all environment variables (see the manifest and the
[package doc](../cmd/notifier/main.go)): `RABBITMQ_URL` **or**
`RABBITMQ_ADDRESSES`+`RABBITMQ_USERNAME`+`RABBITMQ_PASSWORD` for the source,
`TARGET_URL`+`TARGET_SECRET` for the sink, and optional `RABBITMQ_EXCHANGES` /
`RABBITMQ_QUEUE` / `RABBITMQ_QUEUE_TYPE` / `RABBITMQ_TOPIC` / `RABBITMQ_PREFETCH` / `PORT`.

**Quorum-queue clusters.** The bridge declares its queue as `classic` by default. If your
RabbitMQ enforces quorum queues (e.g. kolla-ansible with `om_enable_rabbitmq_quorum_queues`, whose
deploy precheck fails with *"stratos-notifier is a non-quorum queue"*), set
`RABBITMQ_QUEUE_TYPE=quorum`. A queue's type is immutable, so if the queue already exists as classic,
delete it first (`rabbitmqctl delete_queue stratos-notifier`) — the bridge recreates it as quorum on
next start. Quorum queues need RabbitMQ 3.8+ and a clustered broker.

Run **one bridge per (cloud, region)** — the Notifier URI is region-scoped. The
bridge exposes `/healthz` on `PORT` (default 7476) for liveness/readiness, and
exits non-zero on a broker drop so the orchestrator restarts it.

---

## What Stratos does with each event

Stratos routes by the first dot-segment of `event_type`:

| event_type prefix | OpenStack service | cache resource |
|---|---|---|
| `compute` | nova | server (or bare-metal server) |
| `volume` | cinder | volume |
| `image` | glance | image |
| `network` / `subnet` / `port` / `router` / `floatingip` / `security_group` | neutron | the matching network resource |
| `dns` | designate | DNS zone |
| `orchestration` | heat | stack |
| `magnum` | magnum | cluster |
| `share` | manila | file share |

- A **delete** event (`*.delete.*`) removes the resource from the cache.
- Any other event re-fetches that one resource live from OpenStack and upserts it
  (so the cache reflects the post-change state, not just the event payload).
- An unmapped `event_type` is silently skipped.

Applied changes also push an SSE update to any open dashboard for that project, so
the UI refreshes without a reload.

---

## Delivery semantics & security

- **Always 200.** The webhook is fire-and-forget: even on a processing error it
  returns `200` and logs the failure, so a transient error can't make OpenStack
  (or the bridge) retry-storm. The periodic sync is the safety net that repairs
  anything a dropped event missed.
- **Fail-closed auth.** No secret configured, or a wrong/missing header →
  `401`, before any cache mutation. The comparison is constant-time.
- **Per-provider isolation.** Each provider has its own secret; a secret for one
  cloud can't post events against another.
- **Malformed body → 400.** The only case that isn't swallowed.

---

## Verify

1. Set the secret (Step 2), deploy the bridge (Step 3).
2. Create or delete a resource in OpenStack (e.g. boot an instance).
3. The customer's dashboard should reflect it within seconds — no manual refresh.
4. If nothing happens: check the bridge logs for AMQP connect + POST status,
   confirm the URL region matches, and confirm the `X-Stratos-Notification-Secret`
   header matches the saved secret (a mismatch is a silent `401` at Stratos).

**Common gotcha — the port.** In-cluster the API Service listens on its service
port (`8080` by default), not `80`. `http://stratos-api/…` (implicit `:80`) has
nothing to connect to and the POST times out with
`context deadline exceeded (Client.Timeout exceeded while awaiting headers)`.
Use `http://<api-service>:8080/…`. A fast `401` (not a timeout) instead means the
API is reachable but the secret is missing/wrong.
