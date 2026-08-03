# Managed Databases (DBaaS) — operator guide

Stratos provisions customer databases as **ArgoCD Applications on a dedicated, ops-built DB
Kubernetes cluster** — the managed-Kubernetes (kamaji) delivery pattern pointed at a different
cluster. One database = one Application rendering the `database-cluster` chart
(`deploy/charts/database-cluster`), which emits the engine operator CR, a tenant-facing Octavia
LoadBalancer Service and a NetworkPolicy. Stratos stays stateless about desired spec: the
Application's `helm.valuesObject` is the state store, status is read back off Application
health + the LB Service.

```mermaid
flowchart LR
    stratos["Stratos"]
    subgraph cloud["OpenStack cloud"]
        subgraph dbc["DB cluster (ops-built, OpenStack VMs)<br/>ArgoCD + AppProject stratos-dbaas<br/>CNPG · Percona PS · mariadb-op · valkey-op<br/>OCCM + cinder-csi"]
            app["Application std-&lt;id&gt;<br/>(chart database-cluster)"]
            pods["DB pods + PVCs<br/>ns st-&lt;projectId&gt;"]
            lb["Service std-&lt;id&gt;-lb<br/>(LoadBalancer)"]
        end
        octavia["Octavia amphora<br/>(dbaas keystone project)"]
        vip["VIP on the CUSTOMER's<br/>VPC subnet"]
    end
    client["Customer app<br/>(tenant VM)"]
    stratos -- "Application CRs<br/>(kubeconfig, SSA)" --> app
    stratos -- "neutron RBAC<br/>access_as_shared" --> vip
    app --> pods
    app --> lb
    lb -- "OCCM" --> octavia
    octavia --> vip
    client -- "5432/3306/6379/27017" --> vip
```

Engines (all five ship in the chart; valkey is beta-gated):

| Engine | Operator | HA semantics | Client port |
|---|---|---|---|
| `postgresql` | CloudNativePG ≥ 1.29.1 | `instances` 1/2/3, operator failover | 5432 |
| `mysql` | Percona Operator for MySQL (PS) | group replication, size 1 or 3 | 3306 |
| `mariadb` | mariadb-operator 26.x | replication + chart-owned HAProxy | 3306 |
| `valkey` | valkey-operator (pre-GA, **beta**) | 1 shard, `instances`-1 replicas | 6379 |
| `ferretdb` | FerretDB v2 over a CNPG backend | CNPG instances + stateless frontends | 27017 |
| `opensearch` | opensearch-k8s-operator 3.0.2 | N same-size nodes (all roles), 1 or 3 | 9200 (HTTPS) |
| `kafka` | Strimzi 1.1.0 (KRaft, Kafka 4.2.0–4.3.0) | dual-role KRaft pool, 3 brokers | 9094 (SASL/SCRAM) |

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

Optional, only for the DNS/TLS features (§ Provider registration): **external-dns** watching
Services (publishes `<id>.<zone>` A records off the chart-stamped hostname annotations) and
**cert-manager** with a ClusterIssuer that solves **DNS-01** for the zone (the VIPs are private
tenant-subnet IPs — an HTTP-01 challenge can never reach them).

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
  Replica choices default to `[1, 3]` (single node or the HA trio) and are capped at 3
  platform-wide; the admin form restricts them per engine with an `@` suffix
  (`kafka=4.3.0@3`). More than 3 is a deliberate future release, not a config knob.
- `config.database.dnsZone` (optional) — platform DNS: every database gets `<id>.<zone>`
  (`-dash` for Dashboards, `-b<N>` per kafka broker — kafka brokers then advertise those names)
  as an A record to its private VIP, and connection info returns the name instead of the IP.
  The cached endpoint and `GET_CONNECTION_INFO` both spell it through one helper
  (`Config.PublicHost`: customer domain > platform name > VIP), so the list and the connection
  panel can never disagree; the raw VIP stays available as `endpoint_ip` (what a BYO domain's
  A record must target). The name only appears once the VIP exists — the billing eligibility
  gate is unchanged.
- `config.database.certIssuer` (optional, with `dnsZone`) — cert-manager ClusterIssuer name;
  OpenSearch API + Dashboards then serve a real certificate (`<id>-tls`) for the platform name
  instead of the operator's self-signed pair. Other engines are raw TCP — an A record is all
  they need.
- `config.database.backup` (optional) — the object store every database on this location backs
  up to: `{endpoint, bucket, prefix?, region?, pathStyle}` plus `secret.backupAccessKey` /
  `secret.backupSecretKey`. Customers never see or choose it; they only turn backups on. Empty
  bucket/endpoint = the whole backup surface stays hidden client-side rather than offering a
  toggle that would write nowhere.
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

```mermaid
sequenceDiagram
    participant C as Customer
    participant S as Stratos
    participant N as Neutron (customer tenant)
    participant A as ArgoCD (DB cluster)
    participant OP as Engine operator
    participant O as Octavia (via OCCM)
    C->>S: Create database (engine, size, VPC network/subnet)
    S->>S: Prove network/subnet belong to the tenant
    S->>A: st-project namespace + default-deny NP + net-share marker Secret
    S->>N: RBAC access_as_shared → dbaas keystone project
    S->>A: (mariadb/valkey) auth Secret + Application CR (pinned chart, full values)
    A->>OP: Render + sync chart (engine CR, LB Service, NetworkPolicy)
    OP-->>A: Pods ready (Lua health)
    O-->>A: VIP programmed on the tenant subnet (Service health)
    S-->>C: Status READY + endpoint (sync reads Application + LB Service)
    C->>S: GET_CONNECTION_INFO
    S->>A: Read operator secret + LB ingress on demand (never stored)
    C->>S: Delete database
    S->>A: Delete Application (resources-finalizer cascades, LB torn down)
    S->>N: Sweep revokes the RBAC share once the LB port is gone
```

- **Create** — client `POST /project/{id}/cloud` `{type: DATABASE_CLUSTER, data: {name,
  engine, version, replicas, cpu, memoryGiB, storageGiB, networkId, subnetId, allowedCidrs,
  beta?, dashboards?, sso?}}` (`dashboards` — opensearch only — deploys OpenSearch Dashboards
  with its own `<id>-dash-lb` endpoint; `sso` takes the same block as `SET_SSO` and implies
  `dashboards`, so a customer never has to create the database and immediately reconfigure it.
  Both paths share one validator, so they cannot drift on the "connectUrl + clientId, or
  nothing" rule). Order: namespace (+ its `stratos-default-deny` NetworkPolicy) → net-share marker →
  neutron RBAC share → (mariadb/valkey) stratos-owned `<id>-auth` secret → Application —
  marker BEFORE share, so a crash mid-create always leaves a record the sweep converges on.
  Status PENDING → PROGRESSING → READY as ArgoCD converges and the VIP is programmed
  (minutes; the charge is deferred until the endpoint exists, then back-billed).
- **Actions** — `GET_CONNECTION_INFO` (secret + LB read on demand, never stored),
  `RESIZE {cpu,memoryGiB}`, `RESIZE_STORAGE {storageGiB}` (grow-only), `SCALE_REPLICAS`,
  `UPGRADE {version}` (catalog-gated, upward-only vs live values; the operator rolls the pods
  onto the new engine image, the endpoint never changes — off only for valkey [pre-GA
  operator]; ferretdb is supported because the chart derives BOTH halves of its matched
  frontend/backend image pair from engineVersion), `RESTART`, `RESET_PASSWORD`
  (returned once), `SET_ALLOWED_CIDRS`, `SET_SSO` (opensearch only: Dashboards OIDC against a
  **public** IdP client — no client secret is ever stored; API-side securityconfig wiring is a
  live-drill item), `SET_CUSTOM_DOMAIN` (opensearch only: BYO domain + certificate —
  `{domain, certPem, keyPem, caPem?}`, validated key-matches-cert/covers-domain/not-expired,
  written straight to the cluster secret `<id>-custom-tls` and mounted on the http layer +
  Dashboards; **no ACME** — the customer certifies their own name and points DNS at the
  endpoint themselves (CNAME to the platform name or an A record to the VIP); empty domain
  removes it), `MANAGE_ACCESS` and `RESET_USER_PASSWORD` (see below). Engine-gated via `dbaas.Capabilities` — verify each mechanism live before
  widening the map.
- **Delete** — Application delete only; the ArgoCD resources-finalizer cascades the chart, the
  LB Service delete tears the Octavia LB (and its tenant-subnet port) down. The periodic sweep
  (`syncjob.sweepDbaasOrphans`) then revokes the network share (skipped while a live sibling
  database rides the same network), reaps the marker/auth secrets and GCs the emptied
  namespace. A neutron 409 while the amphora winds down is the normal first-pass outcome —
  fail-closed retry, never force-delete ports.
- **Project teardown** — dbaas rows are swept before the tenant sweep, and keystone tenant
  deletion is DEFERRED while any database remnant is still finalizing (the LB port / network
  share would wedge it). Re-run teardown after the sweep reports clean.

## Pricing (DigitalOcean parity)

The seed price plan carries **one `database_cluster` rule per engine** (filter `engine eq …`),
rated on the pre-multiplied totals and derived from DigitalOcean's managed-databases price
list (monthly/730h; derivation table in `deploy/seed/price-plan-seed.json` `_readme`):

| Engine | vCPU/h | GB-RAM/h | GB-disk/h | DO anchor |
|---|---|---|---|---|
| postgresql, mysql, mariadb, ferretdb | $0.002740 | $0.015068 | $0.000295 | $15.15/mo (1c/1GB/10GB), exact at small tiers |
| valkey | $0.002945 | $0.014658 | $0.000295 | $15/mo (1c/1GB/10GB) |
| opensearch | $0.002740 | $0.006164 | $0.000295 | exact on all three DO tiers |
| kafka | — | $0.028767 | $0.000295 | DO prices the 3-broker cluster; RAM-rated |

Example: a PostgreSQL 2 vCPU / 4 GB / 60 GB single node rates to $60.90/mo (DO: $60.90);
Kafka with three 2c/2GB brokers at 40 GB each rates to about $149/mo (DO: $148.80).
Re-derive when DO reprices.

## HA and write-node failover (why the endpoint never moves)

The customer-facing address is the Octavia VIP — a fixed IP on the tenant subnet. Failover
never changes it; the "flip to the new write node" happens INSIDE the cluster, behind the LB:

| Engine | Write-path routing behind the VIP |
|---|---|
| postgresql | LB Service selects `cnpg.io/instanceRole: primary`; CNPG re-labels pods on failover/switchover, so the Service endpoints flip to the new primary (verify on 1.29; fallback = CNPG `managed.services.additional` selectorType `rw`) |
| mysql | LB targets the Percona operator's HAProxy pods; HAProxy follows the group-replication primary election |
| mariadb | LB targets the chart-owned HAProxy, whose backend is the operator's `<id>-primary` Service — the operator re-points its endpoints on switchover/failover |
| ferretdb | LB targets the stateless frontends, which talk to the CNPG `-pg-rw` Service (operator-managed) |
| valkey | beta — routing unverified until the operator CRD is pinned |
| opensearch | LB targets all nodes (every node coordinates); cluster re-elects internally |
| kafka | Strimzi-created LBs: ONE bootstrap VIP + one VIP per broker (advertised addresses) — N+1 Octavia LBs per cluster, mind the amphora quota; partition leadership moves internally |

Clients see a few seconds of connection resets during a failover, reconnect to the SAME
host:port, and land on the new primary. No floating-IP dance, no DNS TTL.

## Vertical autoscale (opt-in, surcharge-priced)

`SET_AUTOSCALE {enabled, maxCpu, maxMemoryGiB, maxStorageGiB}` — available on every engine.
Design: **the VPA is the brain, stratos is the hand**. The chart renders a
VerticalPodAutoscaler in `updateMode: Off` (recommendation only — a VPA that evicts database
pods on its own schedule is a data-safety hazard); each sync cycle stratos reads the
recommendation, clamps it into the customer's ceilings and applies it through the SAME values
patch RESIZE uses, so the operator performs its own safe rolling change. Disk rides the same
tick: when any of the database's PVCs passes 80% full (kubelet volume-stats via the provider's
Prometheus metrics config), the volume grows 20% (grow-only), up to `maxStorageGiB`.

Scale-UP only by design — automatic shrink flaps and is where databases lose caches; scaling
down stays a deliberate customer RESIZE. Billing: the scaled-to size bills through the normal
declared-size totals (the tick raises the declared values), plus a flat surcharge
(`autoscale_enabled`, $0.0137/h ≈ $10/mo per database) while enabled. Prerequisites on the DB
cluster: the VPA recommender, and Prometheus metrics config on the provider for the disk leg —
see `deploy/dbaas-cluster/README.md`.

## Chart pin / platform update

Databases keep their chart pin when the provider's `chartVersion` moves — nothing is upgraded
behind a customer's back. Two ways to move one:

- **Customer, opt-in** — the database page shows a "Platform update available" banner whenever
  the pin differs from the provider's, driving the `APPLY_PLATFORM_UPDATE` action. It re-pins
  onto the provider's current version; the engine version, the data and the endpoint are
  untouched. Deliberately NOT capability-gated: a re-pin is engine-agnostic, and a database
  whose values are unreadable is exactly the one an update should still be able to repair.
- **Operator override** — `GET /api/v1/admin/service/{id}/db-clusters` lists pins;
  `POST .../db-clusters/bump-chart` (all) or `POST .../db-clusters/{clusterId}/bump-chart`
  (one). The provider page renders these as the "Managed databases — platform (chart)
  versions" card, shared with managed Kubernetes.

Engine-version upgrades are a different action (`UPGRADE`) and are catalog-gated and
upward-only. Available for every engine except **valkey**, whose operator is pre-GA.

## Customer-managed databases and users (MANAGE_ACCESS)

`MANAGE_ACCESS` takes the WHOLE desired state — `{databases: [{name, owner?}], users: [{name,
databases?: [...], roles?: [...]}]}` — so an entry dropped from the list is removed on the
database. Newly declared users come back with a generated password in the response **once**;
`RESET_USER_PASSWORD {username}` issues a new one. Nothing is stored stratos-side: each
password lives only in Secret `<id>-u-<user>` on the DB cluster, which the engine CR
references, so values stay safe to read.

| Engine | Mechanism | Notes |
|---|---|---|
| `postgresql` | CNPG `Database` CR + `spec.managed.roles` | Postgres has no per-database grant CR, so "user may use database" becomes membership of that database's OWNER role. A database declared without an owner is owned by the first user that lists it — leaving it blank would hand it to the engine's `app` role and grant the customer nothing. |
| `mariadb` | `Database` + `User` + `Grant` CRs | One Grant per (user, database), `ALL PRIVILEGES` on that database only, no `GRANT OPTION`. |
| `opensearch` | `OpensearchUser` + `OpensearchUserRoleBinding` | Roles are BUILT-IN names from a server-side allowlist. **The CR name is the login name** — the CRD has no username field and one namespace holds every database in the project — so the customer signs in as `<id>-u-<name>`; the sync echoes that back as the user's `login`. |
| `mysql` | — | Not offered. The Percona ps-operator ships no user/database CRDs, so there is nothing values-shaped to reconcile. |

**Identifier validation is a security control, not a nicety.** mariadb-operator interpolates
these names straight into SQL with no escaping (upstream issue k8s.mariadb.com#1722, closed as
not planned), so `dbaas.ValidIdent` (`^[a-z][a-z0-9_]{0,30}$`) plus a reserved-name list is the
escaping layer. `TestValidateAccess` pins it. Never relax that regex without replacing the
mechanism.

**OpenSearch also gets two surfaces of its own**, both declarative and both driven from the
same page:

- **Custom roles** ride `MANAGE_ACCESS` alongside users (`roles: [{name, indexPatterns,
  actions}]`). `actions` are built-in OpenSearch ACTION GROUPS from a server-side allowlist —
  raw action strings are refused, because `indices:admin/*` is a management grant wearing an
  index-permission costume, and cluster-level permissions are not offered at all. Like users,
  the CR name is the role name, so a custom role is `<id>-r-<name>` and the role binding
  resolves it through that prefix.
- **Index retention** is `SET_INDEX_POLICIES` (`{policies: [{name, indexPatterns,
  deleteAfterDays, rolloverGiB?, rolloverDays?}]}`). Four numbers rather than the full ISM state
  machine, which the chart expands into a real `hot -> delete` policy bound to the patterns.
  Note the kind casing: `OpenSearchISMPolicy`, while its siblings are `OpensearchUser` /
  `OpensearchRole` — upstream is inconsistent and `kubectl api-resources` is the authority.

The access CRs carry `argocd.argoproj.io/sync-wave: "1"`: they reference the engine CR, whose
webhooks reject a User/Database/Grant naming a cluster that does not exist yet.

## Backups (BACKUP capability)

`SET_BACKUP {enabled, schedule, retentionDays}` sets the posture; `CREATE_BACKUP` runs one now;
`LIST_BACKUPS` reads the history live off the DB cluster (someone reading it is usually deciding
what to restore, so a stale answer is worse than a slow one). The store is provider config —
nothing in the request names a bucket.

Credentials never travel through values: stratos writes one Secret per database
(`<id>-backup-s3`) carrying every key alias the operators want — `ACCESS_KEY_ID` /
`ACCESS_SECRET_KEY` for CNPG and `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` for Percona and
mariadb — and the CRs reference it. Enabling writes the Secret BEFORE the values flip;
disabling removes it AFTER, so no operator ever reconciles against a Secret that is not there.

| Engine | Mechanism | PITR |
|---|---|---|
| `postgresql`, `ferretdb` | barman-cloud **plugin**: an `ObjectStore` CR plus `spec.plugins[].isWALArchiver` on the Cluster, with a `ScheduledBackup` and on-demand `Backup` CRs | Yes — WAL archiving is what `isWALArchiver` turns on |
| `mariadb` | `PhysicalBackup` with `spec.schedule.{cron,immediate,onDemand}` | Only on the **replication** topology; 26.6.0 does not do it standalone or on Galera |
| `mysql` | Percona in-CR `spec.backup.storages` + `spec.backup.schedule[]`, on-demand as its own `PerconaServerMySQLBackup` | Yes, via a binlog-server |

**Why the plugin and not `spec.backup.barmanObjectStore`:** the in-core object store is
deprecated as of CNPG 1.26 and **removed in 1.31**. Building on it would buy one minor version
and then force a rewrite of every Cluster spec, ScheduledBackup and stored restore recipe.
PREREQUISITE: the `plugin-barman-cloud` chart must be installed on the DB cluster (namespace
`cnpg-system`) — without it the `barmancloud.cnpg.io` CRD does not exist and the Application
fails to sync.

Four format traps, each of which fails silently or not at all rather than loudly, all pinned by
tests or by a live `apply --dry-run=server`:

- **Cron arity.** CNPG's `ScheduledBackup` takes SIX fields (leading seconds); Percona and
  mariadb take five. Stratos stores ONE canonical five-field cron and `_backup.tpl` converts —
  a five-field string in a six-field parser runs at the wrong hour instead of erroring.
- **`endpointUrl`, not `endpointURL`** on the Percona CR. The docs use the capitalised spelling;
  a CRD PRUNES unknown fields, so the typo does not error — the endpoint simply vanishes and
  xbcloud uploads to real AWS S3 with these credentials.
- **mariadb's S3 endpoint is `host:port` with NO scheme**, and TLS comes from
  `storage.s3.tls.enabled`. Passing a URL builds `https://https://…`.
- **mariadb's `maxRetention` is a Go duration**, which has no day unit — `30d` is rejected with
  `unknown unit "d"`. Days are converted to hours.

Retention is days everywhere except Percona, which counts BACKUPS; at one run per day the two
agree, and a sub-daily schedule keeps proportionally less wall-clock history.

**Restore is not wired yet.** Three of the four engines can only restore into a NEW instance
(CNPG always bootstraps a new Cluster; mariadb physical/PITR goes through `bootstrapFrom` on a
new MariaDB), so restore belongs on the create path as "new database from a backup" rather than
as an action on the existing one. The backups and the WAL/binlog archive are being taken today.

## Connection secrets (contract, pinned by TestConnectionSecretContract)

| Engine | Secret | Exposed account |
|---|---|---|
| postgresql | `<id>-app` (CNPG-minted) | `username`/`password`/`dbname` keys |
| ferretdb | `<id>-pg-app` (CNPG backend `<id>-pg`) | same keys, Mongo wire |
| mysql | `<id>-secrets` (operator-minted) | `root` under key `root` (PS has no declarative app users) |
| mariadb | `<id>-auth` (**stratos-provisioned**) | user `app`, database `app` |
| valkey | `<id>-auth` (**stratos-provisioned**) | AUTH only |
| opensearch | `<id>-admin-password` (operator-minted, default securityconfig) | `username`/`password` keys |
| kafka | `<id>-auth` (**stratos-provisioned**, KafkaUser BYO-password) | SASL user `<id>-app`, SCRAM-SHA-512 |

The stratos-provisioned secrets live OUTSIDE the Application on purpose: a chart-generated
password would re-roll on every ArgoCD render, and values must never carry secrets (the
Application CR is readable by anyone with argocd read).

## Verify at the live drill (flags embedded in the chart/manifests)

CNPG `instanceRole: primary` LB selector + restart annotation; Percona served CRD version,
secret keys, haproxy labels; mariadb `podMetadata` passthrough + `-primary` Service naming;
the whole valkey CRD (beta gate stays closed until pinned); FerretDB healthz;
engineVersion→image maps; Lua status value sets; PodMonitor ports.

**Closed at the first live drill (chart 0.4.1) — the FerretDB/CNPG bootstrap.** CNPG does not
use the image entrypoint, so nothing in `/docker-entrypoint-initdb.d/` runs and every setting
those scripts write must live in the Cluster spec instead:

- `postgresUID/postgresGID: 999` — the FerretDB image is built on the official `postgres`
  image (uid 999), not CNPG's own (uid 26). Missing it, `initdb` fails at bootstrap with
  `could not look up effective user ID 26: user does not exist` and the Job retries forever.
  **Both fields are immutable**, so a database created before 0.4.1 cannot be repaired — it
  must be deleted and recreated.
- `shared_preload_libraries: [pg_cron, pg_documentdb_core, pg_documentdb]` and
  `cron.database_name: app` — `documentdb.control` requires `pg_cron`, so the
  `CREATE EXTENSION documentdb CASCADE` creates it too, and pg_cron refuses any database
  other than `cron.database_name`; it must therefore name the CNPG **app** database (what the
  frontend connects to), not the image's `postgres` default.
- The `documentdb.*` feature GUCs from `10-preload.sh`. CNPG's webhook keeps custom extension
  parameters verbatim (verified with `kubectl apply --dry-run=server`).
- `GRANT documentdb_admin_role TO app` — ours, not the image's. The extension's API schemas are
  owned by `documentdb_admin_role` while the frontend connects as CNPG's unprivileged `app`
  owner, so without it every call fails with `permission denied for schema documentdb_api`.
  (FerretDB's own guide sidesteps this by running as the superuser; we do not expose one.)
- `pg_hba` loopback `trust`. DocumentDB opens an internal libpq connection back to itself with
  no password for its DDL, and CNPG's generated `pg_hba` ends in `scram-sha-256`, so
  `drop_collection` and friends fail with `fe_sendauth: no password supplied ... while
  executing command over libpq connection`. Loopback only, on a pod that holds exactly one
  customer's database and offers them no shell.

All five were proven on the live cluster: a clean bootstrap from this chart, then
create_collection → insert_one → read → drop_collection as the `app` role over TCP through the
`-rw` Service — the exact `FERRETDB_POSTGRESQL_URL` path.

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
