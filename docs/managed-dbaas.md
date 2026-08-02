# Managed Databases (DBaaS) — operator guide

Stratos provisions customer databases as **ArgoCD Applications on a dedicated, ops-built DB
Kubernetes cluster** — the managed-Kubernetes (kamaji) delivery pattern pointed at a different
cluster. One database = one Application rendering the `database-cluster` chart
(`deploy/charts/database-cluster`), which emits the engine operator CR, a tenant-facing Octavia
LoadBalancer Service and a NetworkPolicy. Stratos stays stateless about desired spec: the
Application's `helm.valuesObject` is the state store, status is read back off Application
health + the LB Service.

Engines (all five ship in the chart; valkey is beta-gated):

| Engine | Operator | HA semantics | Client port |
|---|---|---|---|
| `postgresql` | CloudNativePG ≥ 1.29.1 | `instances` 1/2/3, operator failover | 5432 |
| `mysql` | Percona Operator for MySQL (PS) | group replication, size 1 or 3 | 3306 |
| `mariadb` | mariadb-operator 26.x | replication + chart-owned HAProxy | 3306 |
| `valkey` | valkey-operator (pre-GA, **beta**) | primary + replicas | 6379 |
| `ferretdb` | FerretDB v2 over a CNPG backend | CNPG instances + stateless frontends | 27017 |

Unlike managed-k8s, the compute/storage under a database is **ops-owned** — nothing lands in
the customer tenant except the LB VIP. Billing therefore carries the full price on the
`database_cluster` resource type (pre-multiplied totals `vcpus_total`/`ram_gb_total`/
`storage_gb_total`). While the endpoint is absent the charge is **deferred, not waived**:
once the Octavia VIP appears the engine back-bills the current cycle's elapsed hours from
creation.

## Cluster prerequisites (ops, out-of-band)

Everything in `deploy/dbaas-cluster/` — apply order, RBAC + kubeconfig recipe, AppProject
`stratos-dbaas`, `argocd-cm` Lua health checks for the engine CRDs, the pinned operator install
list, and the Octavia/amphora quota math live there. Assumed pre-existing on the cluster:
openstack-cloud-controller-manager (Octavia LBs), cinder-csi + a default StorageClass, ArgoCD
(self-syncing) in `argocd`.

The Lua health checks are load-bearing: the LB Service's built-in health keeps an Application
`Progressing` until the Octavia VIP is programmed, so Stratos never reports READY without an
endpoint — and a missing engine-CRD check would leave Applications Healthy while the database
is degraded.

## Provider registration

Admin UI → System → Cloud providers → Add provider → **Database (DBaaS)**, or `POST
/api/v1/admin/service` with the doc shape documented in
`internal/platform/externalservice/dbaas.go` (`config.provider = "dbaas"`). Fields that matter:

- `secret.kubeconfig` — the DB cluster kubeconfig from `deploy/dbaas-cluster/rbac.yaml`'s
  recipe (token or client-cert only; encrypted at rest, stripped from admin reads).
- `config.argocd.*` — namespace/project/chartRepo/`chartVersion` (pinned, never latest).
- `config.database.osServiceId` / `osProjectId` — the OpenStack service the DB cluster's nodes
  live on and the **dbaas keystone project id** there: the neutron-RBAC `target_tenant` every
  customer network is shared with.
- `config.database.memberSubnetId` — the DB-cluster node subnet
  (`loadbalancer.openstack.org/member-subnet-id` on every LB).
- `config.database.engines` — the curated catalog (versions / default / replicas / `beta`).
- `config.services.database.<region> = true` — un-hides the client Databases surface.

Seed example: `deploy/seed/external-service-dev.json` (`svc-dbaas-dev`).

## Networking (why neutron RBAC)

The OCCM on the DB cluster authenticates as the ops-owned dbaas keystone project; for it to
plant an Octavia VIP on a customer subnet, the customer network must be visible to that
project. At database create Stratos verifies the customer owns the network/subnet, then creates
a neutron RBAC policy (`access_as_shared`, target = `osProjectId`) with the project's OpenStack
admin creds re-scoped to the tenant. The share is recorded as annotations on a per-database
marker secret (`<id>-net-share`) on the DB cluster — the only durable revocation record.
`loadBalancerSourceRanges` (Octavia ACLs) enforce the customer's allowed CIDRs; the chart's
NetworkPolicy deliberately does NOT mirror them (post-LB source IPs are amphora/node IPs).

## Lifecycle

- **Create** — client `POST /project/{id}/cloud` `{type: DATABASE_CLUSTER, data: {name,
  engine, version, replicas, cpu, memoryGiB, storageGiB, networkId, subnetId, allowedCidrs,
  beta?}}`. Order: namespace (+ its `stratos-default-deny` NetworkPolicy) → net-share marker →
  neutron RBAC share → (mariadb/valkey) stratos-owned `<id>-auth` secret → Application —
  marker BEFORE share, so a crash mid-create always leaves a record the sweep converges on.
  Status PENDING → PROGRESSING → READY as ArgoCD converges and the VIP is programmed
  (minutes; the charge is deferred until the endpoint exists, then back-billed).
- **Actions** — `GET_CONNECTION_INFO` (secret + LB read on demand, never stored),
  `RESIZE {cpu,memoryGiB}`, `RESIZE_STORAGE {storageGiB}` (grow-only), `SCALE_REPLICAS`,
  `RESTART`, `RESET_PASSWORD` (returned once), `SET_ALLOWED_CIDRS`. Engine-gated via
  `dbaas.Capabilities` — verify each mechanism live before widening the map.
- **Delete** — Application delete only; the ArgoCD resources-finalizer cascades the chart, the
  LB Service delete tears the Octavia LB (and its tenant-subnet port) down. The periodic sweep
  (`syncjob.sweepDbaasOrphans`) then revokes the network share (skipped while a live sibling
  database rides the same network), reaps the marker/auth secrets and GCs the emptied
  namespace. A neutron 409 while the amphora winds down is the normal first-pass outcome —
  fail-closed retry, never force-delete ports.
- **Project teardown** — dbaas rows are swept before the tenant sweep, and keystone tenant
  deletion is DEFERRED while any database remnant is still finalizing (the LB port / network
  share would wedge it). Re-run teardown after the sweep reports clean.

## Chart pin / platform update

Databases keep their chart pin when the provider's `chartVersion` moves. Operator override:
`GET /api/v1/admin/service/{id}/db-clusters` lists pins; `POST .../db-clusters/bump-chart`
(all) or `POST .../db-clusters/{clusterId}/bump-chart` (one) re-pins onto the provider's
current version.

## Connection secrets (contract, pinned by TestConnectionSecretContract)

| Engine | Secret | Exposed account |
|---|---|---|
| postgresql | `<id>-app` (CNPG-minted) | `username`/`password`/`dbname` keys |
| ferretdb | `<id>-pg-app` (CNPG backend `<id>-pg`) | same keys, Mongo wire |
| mysql | `<id>-secrets` (operator-minted) | `root` under key `root` (PS has no declarative app users) |
| mariadb | `<id>-auth` (**stratos-provisioned**) | user `app`, database `app` |
| valkey | `<id>-auth` (**stratos-provisioned**) | AUTH only |

The stratos-provisioned secrets live OUTSIDE the Application on purpose: a chart-generated
password would re-roll on every ArgoCD render, and values must never carry secrets (the
Application CR is readable by anyone with argocd read).

## Verify at the live drill (flags embedded in the chart/manifests)

CNPG `instanceRole: primary` LB selector + restart annotation; Percona served CRD version,
secret keys, haproxy labels; mariadb `podMetadata` passthrough + `-primary` Service naming;
the whole valkey CRD (beta gate stays closed until pinned); FerretDB healthz + CNPG
DocumentDB bootstrap; engineVersion→image maps; Lua status value sets; PodMonitor ports.

## Troubleshooting

- **Endpoint stuck pending** — `kubectl -n st-<project> get svc <id>-lb`; empty
  `EXTERNAL-IP` → OCCM events (amphora quota in the dbaas project, member-subnet ports,
  neutron RBAC missing).
- **Application Degraded on first create (ferretdb)** — frontend crash-loops until the CNPG
  backend accepts connections; the startup probe absorbs the normal window.
- **Sweep logs "revoke network share: 409"** — expected while the amphora winds down; the next
  cycle retries. Persistent 409 = a foreign port of the dbaas project on the tenant network.
- **`not managed by stratos — refusing`** — the object lacks the
  `app.kubernetes.io/managed-by: stratos` label; pre-existing/hand-made objects are invisible
  and untouchable by design.
