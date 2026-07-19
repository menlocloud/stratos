# Managed Kubernetes (kamaji provider) — operator runbook

How to stand up, operate and debug the `kamaji` managed-Kubernetes provider. This is the
operational companion to the design doc — **`tasks/managed-k8s-plan.md` stays the design
source**; section references (§) below point into it. Release posture is **internal-first**
(plan §3.5): we run this ourselves, hardened by these procedures, before customer exposure.

> ## Chart contract: VALUES VERIFIED (2026-07-19) — template semantics still need the drill
>
> The values contract in `internal/cloud/kamaji/values.go` was reconciled against the chart's
> default-values snapshot `values.upstream.yaml` (chart **0.2.3**) vendored in the infra-ops
> wrappers (`kubernetes/clusters/kamaji-cluster-az1/charts/{dev,sysadmin,stag,prod}-cluster`):
> `cloudCredentialsSecretName`, inline `oidc.*` (no helm `lookup` on our path — ArgoCD-safe),
> `kamajiControlPlane.*`, `clusterNetworking.externalNetworkId` (internal network auto-created
> per cluster by default), `nodeGroups[].machine*` spellings, taint **objects**, the
> autoscaler-tag-matches-cluster-minor constraint, and the KAMAJI-FIX addon tolerations.
> **Still unverified until the live drill** (chart source is not anonymously pullable from
> ghcr — 401; vendor it or grant a read:packages token): MachineDeployment naming, autoscale
> annotation mechanics, upgrade rotation semantics, Lua health checks against live CRDs.

## 1. Management-cluster prerequisites

One-time, per management cluster (details + apply order: [`deploy/mgmt-cluster/README.md`](../deploy/mgmt-cluster/README.md)):

1. Kamaji stack live (Kamaji operator, cluster-api-operator + KamajiControlPlane provider,
   CAPO, CAAPH).
2. **ArgoCD installed** (plan D3 — ArgoCD is the delivery plane; stratos only writes
   `Application` CRs, never talks to the ArgoCD API).
3. `deploy/mgmt-cluster/` applied: `rbac.yaml` (stratos ServiceAccount, least-privilege
   RBAC, token), `appproject.yaml` (AppProject `stratos-k8s` — source/destination
   guardrail), `argocd-health.yaml` (custom Lua health for TenantControlPlane / Cluster /
   MachineDeployment — **validate the status fields during the live drill**).
4. **Chart CI pinning**: CI publishes `openstack-kamaji-cluster` + mirrored images to the
   OCI registry at pinned versions. Never `latest`, never `0.0.0+latest` (plan §9) — the
   provider config and every existing cluster pin an exact version, and the registry must
   retain every pinned version.

## 2. Provider setup (admin → Cloud providers → Add provider → Kubernetes)

Seed-file twin of the form: `deploy/seed/external-service-dev.json` (`svc-kamaji-dev`).
A kamaji provider is OpenStack-*adjacent*: control planes live on the management cluster,
worker VMs land in the customer's keystone tenant of the project's **openstack** service
(plan D4) — so a project must be attached to both providers.

| Form field | Config key | Notes |
|---|---|---|
| Management kubeconfig | `secret.kubeconfig` | The stratos SA kubeconfig assembled per the recipe in `deploy/mgmt-cluster/rbac.yaml`. Plain token/client-cert only — no exec plugins. Encrypted at rest. |
| Regions | `config.regions` | The stratos region(s) stamped on cached clusters. Use the same region name as the paired openstack service so flavors/images line up. |
| Services | `config.services` | `{"kubernetes": {"<region>": true}}` — drives the client-portal nav gating. |
| ArgoCD namespace / project | `config.argocd.namespace` / `.project` | Must match `deploy/mgmt-cluster` (`argocd` / `stratos-k8s`). |
| Chart repo / name / version | `config.argocd.chartRepo` / `.chartName` / `.chartVersion` | OCI repo **without** `oci://` prefix. Version is the pin for NEW clusters; existing clusters keep their own pin until a fleet wave moves them. |
| DataStore | `config.cluster.dataStoreName` | Kamaji DataStore name (default `default` = kamaji-etcd). |
| Floating network id | `config.cluster.floatingNetworkId` | Octavia floating network for the API-server LB. |
| External network id | `config.cluster.externalNetworkId` | CAPO external network for worker networking. |
| DNS zone | `config.cluster.dnsZone` | Optional. API FQDN = `<clusterId>.<zone>` (certSAN + external-dns). Stable across display renames — cluster ids (`stc-<8hex>`) never change (plan §9). |
| Version → image matrix | `config.cluster.versions` | Curated map `k8s version → Glance image id` — the ONLY versions offered to customers. Keep tenant versions within Kamaji compat (mgmt ≥1.33 hosts v1.30–v1.35 today). New CVE node image = new Glance id here, then rotate node groups (§4 below). |
| Flavors allowlist | `config.cluster.flavors` | Flavor ids offered in the cluster-create wizard. **Empty array = no restriction** (full region flavor catalog). Use it to keep GPU/baremetal flavors out of node groups. |

## 3. How provisioning works (what you'll see on the mgmt cluster)

Create path (plan D3/D4/D7):

1. Stratos ensures namespace `st-<projectId>` (labeled `app.kubernetes.io/managed-by: stratos`).
2. Stratos **mints a per-cluster keystone application credential** in the customer's own
   tenant, renders it into a `clouds.yaml` Secret `<clusterId>-cloud-config` in that
   namespace. Mgmt-side only — the customer cluster never carries any OpenStack credential
   (plan D7); blast radius of a leak is that customer's own project.
3. Stratos applies one ArgoCD `Application` named `<clusterId>` (`stc-<8hex>`) in the
   `argocd` namespace: source = pinned chart from our OCI registry, full generated values
   inline, destination = `st-<projectId>`, project = `stratos-k8s`, finalizer
   `resources-finalizer.argocd.argoproj.io`.
4. ArgoCD renders + syncs the chart → TenantControlPlane (CP pods), CAPI Cluster +
   MachineDeployments (worker VMs in the customer tenant), addons. Stratos reads status
   back off the Application health + TCP + MachineDeployments — the custom health checks
   from `argocd-health.yaml` are what make that health signal real.
5. Kubeconfig download is fetch-on-demand from the Kamaji `<tcp>-admin-kubeconfig` secret —
   never stored in stratos (plan D5).

Every change (k8s upgrade, node-group edit, OIDC, chart bump) is the same operation: mutate
the stored desired spec → stratos re-applies the Application (plan §9, one reconcile path).

**Ownership marker:** stratos only lists/patches/deletes objects labeled
`app.kubernetes.io/managed-by: stratos`. Pre-existing (infra-ops wrapper) clusters on the
same mgmt cluster are invisible and untouchable until deliberately migrated by stamping
labels.

### Deletion and orphan finalization (sync-driven GC)

Delete path: stratos deletes the Application → the resources-finalizer makes ArgoCD cascade
the delete through everything the chart rendered (TCP, CAPI machines → nova VMs, LB). The
clouds.yaml **secret delete, application-credential revoke, and (when the project's last
cluster is gone) the `st-*` namespace delete are sync-driven finalization** — they happen on
a later sync pass, *after* the ArgoCD cascade completes, not synchronously in the delete
request. Consequences:

- A cluster can show as gone in the UI while its mgmt-cluster leftovers are still being
  finalized. This is normal for minutes, not hours.
- The finalizer is a **service-level sweep** (`syncjob.sweepKamajiOrphans`, once per sync
  cycle): it scans the management cluster itself, so it also reaps leftovers of projects whose
  stratos doc is already gone (scheduled deletion, teardown) — the appcred is revoked against
  the OpenStack service recorded on the secret (`stratos.io/appcred-service` annotation).
  A secret younger than the 30-minute finalize grace window is never touched (create-race
  guard), so freshly-deleted clusters finalize on a later pass.
- **Project teardown defers the keystone tenant delete** while any cluster cascade is still
  running (the cascade deletes worker VMs/LB with tenant-scoped credentials — deleting the
  tenant first would wedge the CAPI finalizers). Teardown then returns an explicit
  "re-run after the sweep finishes" error. Operator flow: wait for the sweep to report clean
  (or check below), then re-run teardown to delete the tenant.

  ```sh
  # namespaces stratos owns, with what's left inside them
  kubectl get ns -l app.kubernetes.io/managed-by=stratos
  kubectl get applications.argoproj.io -n argocd -l app.kubernetes.io/managed-by=stratos
  kubectl get tenantcontrolplanes,machinedeployments -n st-<projectId>
  # manual mop-up is only needed when the sweep reports it CANNOT revoke an appcred
  # (minting service deleted / legacy secret without the service annotation):
  openstack application credential list --user <svc-user>   # revoke strays named stratos-stc-*
  kubectl delete secret -n st-<projectId> <stc-id>-cloud-config
  ```

## 4. Fleet upgrades (plan §9 — read it before running one)

Three separate planes; never conflate them:

| Plane | What changes | How |
|---|---|---|
| Platform (mgmt cluster) | Kamaji operator, cluster-api-operator/CAPO, CAAPH, ArgoCD | infra-ops GitOps + Renovate; staging mgmt cluster first. Operator upgrades do NOT mutate tenant clusters. |
| Per-cluster chart | addon images, CP settings, k8s version | Stratos fleet rollout: bump provider default for NEW clusters immediately; EXISTING clusters move in **waves — canary cluster first, then health-gated batches** (gate = Application Healthy + TCP Ready + MachineDeployments ready). |
| Node images (Glance) | worker OS CVEs, kubelet | New image id in the provider `versions` matrix → node groups rotate via the same MachineDeployment-rotation path as k8s upgrades. |

Ordering guards (enforced by the backend, know them anyway): control plane before nodes;
kubelet never more than 3 minors behind the apiserver; Kamaji CP upgrades roll blue/green;
worker "upgrade" is always a MachineDeployment rotation (Kamaji ships no worker helper).

## 5. Troubleshooting

Start from the ArgoCD Application — it aggregates everything the chart rendered:

```sh
# health + sync state of every stratos cluster
kubectl get applications.argoproj.io -n argocd -l app.kubernetes.io/managed-by=stratos
# one cluster's full resource tree with per-resource health (or use the ArgoCD UI)
argocd app get <stc-id> --show-operation
kubectl get application <stc-id> -n argocd -o jsonpath='{.status.health}{"\n"}{.status.sync}'
```

| Symptom | Look at |
|---|---|
| Application `OutOfSync`/`SyncFailed` | `argocd app get <stc-id>` sync error. "not permitted in project stratos-k8s" → the chart needs a cluster-scoped resource; add exactly that group/kind to `deploy/mgmt-cluster/appproject.yaml` (expected during the unverified-chart phase). Chart pull errors → registry/pin problem (§1.4). |
| Application Healthy but cluster unusable | Health checks not loaded or wrong — verify `argocd-health.yaml` keys exist in argocd-cm and the application controller was restarted; then validate the Lua against the live CRD status (top-blocker caveat). |
| Cluster stuck `PROGRESSING`, no endpoint | `kubectl get tcp -n st-<projectId>` → TCP status. Kamaji CP pods pending → mgmt capacity/datastore; endpoint absent → Octavia LB creation (`kubectl describe svc` in the namespace; floating network id wrong?). |
| Workers not appearing | `kubectl get machinedeployments,machines -n st-<projectId>`; `kubectl describe` a stuck Machine → CAPO events. Usual causes: quota in the customer tenant, wrong Glance image id in the versions matrix, appcred invalid/revoked (check the `<stc-id>-cloud-config` secret exists and the appcred is alive in keystone). |
| MachineDeployment stuck mid-rotation | Old machines not draining / new not joining: `kubectl get machinehealthchecks -n st-<projectId>`; verify the new image id boots (nova console of the new VM in the customer tenant). |
| Delete hangs | Application stays with finalizer while the cascade runs — inspect what's left in the tree (`argocd app get`). Nova VMs refusing to delete block CAPI → fix in the customer tenant. Only remove the finalizer by hand if you accept orphaning the rendered objects, then GC per §3 above. |
| Orphaned namespaces/secrets after teardown | §3 orphan-finalization check. |

## 6. Dev environment (plan §3.0 — decision still open)

Stratos dev points at the kolla test region, which has **no** Kamaji mgmt cluster. Two
options until one is picked and documented:

- **(a) Small k3s/kubeadm mgmt cluster on the kolla VM** with the four mgmt charts
  (kamaji, cluster-api-operator, CAAPH, + ArgoCD), CAPO pointed at kolla. Self-contained;
  matches prod shape end-to-end. Then apply `deploy/mgmt-cluster/` to it and seed
  `svc-kamaji-dev` with its kubeconfig.
- **(b) A dev namespace on the prod `kamaji-cluster-az1`** pointed at kolla. Cheaper, but
  dev traffic shares the prod mgmt cluster and its ArgoCD — acceptable only with the
  AppProject guardrail applied and a separate `stratos-k8s-dev` AppProject.

Either way, the first end-to-end must be the **manual drill** (plan §3.0): one cluster via
hand-written values, then create → ACTIVE → kubeconfig → scale → upgrade one minor → delete
through stratos, plus the accrual drill (CP fee via `kubernetes_cluster`, node VMs via the
existing instance billing — `deploy/seed/price-plan-seed.json`).
