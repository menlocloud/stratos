# DBaaS-cluster manifests for the Stratos `dbaas` provider

Operator-applied, one-time setup on the **DBaaS cluster** — the ops-pre-built Kubernetes
cluster (nodes on OpenStack VMs) that runs the full database operator suite and hosts every
customer database. Stratos does not apply these itself: they create the identity and
guardrails stratos then operates *within* (mirror of `deploy/mgmt-cluster/` for the kamaji
provider).

Full operator runbook: [`docs/managed-dbaas.md`](../../docs/managed-dbaas.md).

## Prerequisites

1. **A pre-built cluster on OpenStack VMs** with the standard cloud plumbing already in
   place — assumed pre-existing, NOT installed by anything here:
   - **OpenStack cloud-controller-manager (OCCM)** — it is what turns the chart's
     `<id>-lb` Services into Octavia internal LBs (the whole connectivity model).
   - **cinder-csi** with a default StorageClass — every database volume is a PVC.
   - **ArgoCD** on this cluster. Any standard install; the manifests here assume the
     `argocd` namespace.
2. **The database operator suite**, at PINNED versions — record the installed version next
   to the chart version you release, because CRD drift between operator and chart is the
   main compatibility risk (chart README, plan risk list):

   | operator | install namespace (LOAD-BEARING) | version guidance | engine |
   |---|---|---|---|
   | CloudNativePG | `cnpg-system` | **≥ 1.29.1** | postgresql + the ferretdb backend |
   | Percona Operator for MySQL (ps-operator) | `ps-operator` | current stable; verify served CRD version (`kubectl api-resources \| grep ps.percona.com`) | mysql |
   | mariadb-operator | `mariadb-operator` | **26.x** | mariadb |
   | valkey-operator | `valkey-operator-system` | **only if the valkey beta gate will open** — CRD unverified, see the chart's `templates/valkey/valkey.yaml` header | valkey |
   | opensearch-k8s-operator | `opensearch-operator-system` | **3.0.2** — 3.x serves both api groups (`opensearch.opster.io` deprecated → `opensearch.org`); the chart renders `opensearch.opster.io/v1`, re-check at the drill (chart's `templates/opensearch/opensearchcluster.yaml` header) | opensearch (+ Dashboards) |
   | Strimzi | `strimzi-system` | **1.1.0**, installed in **watch-all mode** (`watchAnyNamespace: true`) — REQUIRED, databases live in per-project `stdb-*` namespaces the operator cannot enumerate up front. 1.x serves ONLY `kafka.strimzi.io/v1`. ⚠ `helm upgrade` does NOT upgrade Strimzi's CRDs — `kubectl apply` the new CRD bundle separately on every operator upgrade | kafka |
   | prometheus-operator CRDs (+ stack) | `monitoring` | optional — required only when databases run with `monitoring.enabled`; **REQUIRED (full kube-prometheus-stack) for the disk-autoscale leg** — it grows volumes off kubelet volume-stats (`kubelet_volume_stats_*`), and the provider then needs `config.metrics {source: prometheus, prometheus: {url: …}}` (same admin Metrics tab as OpenStack providers) | all |
   | Vertical Pod Autoscaler (**autoscaler component only**) | `kube-system` (or upstream default) | install **only if the autoscale feature will be offered**. The **recommender is REQUIRED**; updater and admission-controller are NOT needed — every chart-rendered VPA is `updateMode: Off` (recommendation-only; stratos applies the scale-ups itself, a VPA must never evict DB pods) | all (autoscale feature) |

   The **namespace column is not a suggestion**: the chart's per-database NetworkPolicy
   (`templates/networkpolicy.yaml` rule 3) allowlists operator→pod traffic by exactly these
   namespace names — install an operator elsewhere and its probes/management traffic is
   silently denied. FerretDB needs no operator: the chart renders its frontend as a plain
   Deployment over a CNPG backend.
3. **Chart CI publishing `database-cluster`** to `ghcr.io/menlocloud/stratos-charts` at
   **pinned versions** — never `latest`. The chart source lives in this repo at
   `deploy/charts/database-cluster/` and is published by `.github/workflows/helm.yml` on a
   `database-cluster-v*` tag; see `deploy/charts/README.md`. The version stratos deploys is
   pinned per provider (`config.argocd.chartVersion`) and per existing database; the registry
   must keep every version a live database is pinned to.
4. **OpenStack side**: a dedicated dbaas keystone project (the one tenant networks get
   neutron-RBAC-shared with), the member subnet id for the provider config
   (`config.database.memberSubnetId`), and Octavia quota — see the quota math below.

## Apply order

```sh
# 1. Stratos identity + least-privilege RBAC + SA token
kubectl apply -f rbac.yaml

# 2. AppProject guardrail (source/destination allowlist for stratos Applications)
kubectl apply -f appproject.yaml

# 3. Registry credential, so ArgoCD can pull the chart. Skip ONLY if you made the package
#    public. Fill in the placeholders first — see the header of the file.
kubectl apply -f repo-credential.yaml

# 4. Custom health checks — a MERGE PATCH onto the existing argocd-cm, NOT kubectl apply
kubectl -n argocd patch configmap argocd-cm --type merge --patch-file argocd-health.yaml

# 5. Restart the ArgoCD controllers so the new Lua health checks load.
#    By label, not by name: the statefulset is named after the Helm release.
kubectl -n argocd rollout restart statefulset \
  -l app.kubernetes.io/name=argocd-application-controller
```

Then assemble the stratos kubeconfig from the SA token (recipe in the comment block at the
bottom of `rbac.yaml`) and paste it as the provider secret when registering the `dbaas`
external service (admin UI → Cloud providers → Add provider → Database, or
`deploy/seed/external-service-dev.json`).

## What each file is

| File | Contents |
|---|---|
| `rbac.yaml` | `stratos-system` namespace, `stratos` ServiceAccount, ClusterRole/binding limited to exactly the verbs the dbaas leg of `internal/cloud/kamajik8s/client.go` uses (namespaces CRUD, secrets CRUD, services read, engine-CRD status reads), a namespaced Role for Application CRUD in `argocd`, the SA token Secret, and the kubeconfig recipe. Read its header for why the secrets grant is cluster-wide (RBAC cannot wildcard `stdb-*` namespaces). |
| `appproject.yaml` | AppProject `stratos-dbaas`: sourceRepos = our OCI registry only, destinations = in-cluster `stdb-*` only, empty (conservative, record-on-demand) clusterResourceWhitelist. |
| `repo-credential.yaml` | ArgoCD repository Secret for the (private) chart registry, with `enableOCI: true`. Its `url` must match `config.argocd.chartRepo` and the AppProject sourceRepos exactly — ArgoCD matches credentials by URL prefix. |
| `argocd-health.yaml` | argocd-cm patch: Lua health for CNPG Cluster / PerconaServerMySQL / MariaDB / Strimzi Kafka / OpenSearchCluster, plus a commented-out valkey placeholder. **Status fields must be validated during the live drill** — see the file header. |

## Verify

```sh
# RBAC is exactly scoped (yes / yes / no):
kubectl auth can-i list clusters.postgresql.cnpg.io -A --as=system:serviceaccount:stratos-system:stratos
kubectl auth can-i create applications.argoproj.io -n argocd --as=system:serviceaccount:stratos-system:stratos
kubectl auth can-i '*' '*' -A --as=system:serviceaccount:stratos-system:stratos

# AppProject present:
kubectl -n argocd get appproject stratos-dbaas

# Health customizations loaded:
kubectl -n argocd get cm argocd-cm -o jsonpath='{.data}' | grep -o 'resource\.customizations\.health\.[^"]*'
```

## Amphora quota math

Every customer database is **one Octavia internal LB** — EXCEPT kafka, which is
**`instances + 1`**: Strimzi provisions one LB per broker (each broker must be individually
addressable) plus the external bootstrap LB (see the chart's
`templates/kafka/kafka.yaml` header). Count kafka databases at N+1 in everything below.
Every Octavia LB is **one
amphora VM** in the dbaas keystone project (two with an active-standby amphora flavor —
check `[controller_worker] loadbalancer_topology` in the Octavia config before doing the
math). Each LB additionally consumes:

- 1 VIP **port on the tenant subnet** (the customer's quota is untouched — the VIP port is
  owned by Octavia; the tenant network is neutron-RBAC-shared into the dbaas project by
  stratos at DB create),
- member ports on the **dbaas member subnet** (`config.database.memberSubnetId`) — size that
  subnet for `#DBs × amphorae`, not `#DBs`.

So for N databases the dbaas keystone project needs, minimum: `loadbalancer` quota ≥ N,
`instances`/`cores`/`ram` quota for N (or 2N active-standby) amphora VMs of the configured
amphora flavor, and `port` quota to match. Exhausted amphora quota surfaces as the LB Service
parked `<pending>` — the Application stays Progressing and the database never goes READY, with
nothing obviously wrong on the k8s side; check the Octavia quota first.

## Every CRD the chart can render

Audited by rendering the chart across all seven engines with every feature on and checking each
`apiVersion/Kind` against a live cluster. A missing CRD does not fail at render — ArgoCD reports
the resource as `Missing` / `OutOfSync` with "Resource not found in cluster", which is quiet
enough to lose.

| Component | Provides | Namespace | Needed for |
|---|---|---|---|
| `cloudnative-pg` | `postgresql.cnpg.io` Cluster, Backup, ScheduledBackup, Database | `cnpg-system` | postgresql, ferretdb |
| `plugin-barman-cloud` 0.7.1 | **`barmancloud.cnpg.io` ObjectStore** | `cnpg-system` | postgresql/ferretdb BACKUPS and restore |
| `ps-operator` | `ps.percona.com` PerconaServerMySQL, ...Backup, ...Restore | `ps-operator` | mysql |
| `mariadb-operator` (+ `-crds`) | `k8s.mariadb.com` MariaDB, Database, User, Grant, PhysicalBackup, PointInTimeRecovery | `mariadb-operator` | mariadb |
| `opensearch-operator` | `opensearch.org` OpenSearchCluster, OpensearchUser, OpensearchRole, OpensearchUserRoleBinding, OpenSearchISMPolicy | `opensearch-operator-system` | opensearch |
| `strimzi-kafka-operator` | `kafka.strimzi.io` Kafka, KafkaNodePool, KafkaUser | `strimzi-system` | kafka |
| `valkey-operator` 0.4.1 | `valkey.io` ValkeyCluster | `valkey-operator-system` | valkey (beta) |
| `cert-manager` | `cert-manager.io` Certificate | cluster-wide | opensearch with a platform TLS certificate |
| `vpa` (recommender only) | `autoscaling.k8s.io` VerticalPodAutoscaler | `vpa` | the opt-in vertical autoscale tick |

Everything except valkey-operator is in the internal helm mirror (`charts-mirror.yaml`) with an
umbrella chart and an ArgoCD Application in infra-ops under
`kubernetes/clusters/<cluster>/charts/` and `.../manifests/argo-apps/`.

**valkey**: use the OFFICIAL `valkey-io/valkey-operator` (CRD `valkey.io/ValkeyCluster`), which
publishes a helm chart at `https://valkey.io/valkey-helm` and is what the chart targets. The
third-party `hyperspike` operator (`hyperspike.io/Valkey`) is a different CRD entirely and ships
no chart at all — do not mix them up. Its chart keeps its CRDs in the helm `crds/` directory,
which ArgoCD picks up because it templates with `--include-crds`. **Until it is installed, any
valkey database fails to sync** — so either install it or drop `valkey` from the provider's
engine catalog rather than leaving a beta engine on offer that cannot provision.

**Why the barman PLUGIN and not in-core `spec.backup.barmanObjectStore`:** the in-core object
store is deprecated in CNPG 1.26 and **removed in 1.31**, so it would buy one minor version and
then force a rewrite of every Cluster spec, ScheduledBackup and stored restore recipe. mariadb
and mysql need nothing extra for backups; their operators do it themselves.
