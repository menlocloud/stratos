# Offering Managed Databases

Stratos can sell managed databases — PostgreSQL, MySQL, MariaDB, Valkey, FerretDB, OpenSearch and Kafka — that run on a Kubernetes cluster you operate and are reachable only from the customer's own network.

The shape mirrors [Managed Kubernetes](/docs/platform-admin/cloud/managed-kubernetes): Stratos writes an ArgoCD `Application` and the operators on your cluster do the work.

| | What it is | Who builds it |
|---|---|---|
| **DBaaS cluster** | One long-lived cluster running every database operator | **You**, once |
| Customer databases | One namespace per project, one `Application` per database | Stratos, on demand |

## Building the DBaaS cluster

A Kubernetes cluster whose nodes are OpenStack VMs, with the standard cloud plumbing already working:

- **OpenStack cloud-controller-manager (OCCM)** — this is what turns each database's Service into an internal Octavia load balancer. The whole connectivity model depends on it.
- **cinder-csi with a default StorageClass** — every database volume is a PVC.
- **Working `kubectl logs` and `exec`**, i.e. the API server is authorized against the kubelets. CloudNativePG takes backups by exec'ing into the instance pod, so a cluster missing that binding takes **no backups at all**. `deploy/dbaas-cluster/kubelet-api-admin.yaml` fixes it.
- **ArgoCD** in the `argocd` namespace.
- **A containerd registry mirror on the nodes**, covering `docker.io`, `registry-1.docker.io`, `ghcr.io`, `quay.io` and `registry.k8s.io`. Every database image comes from a public registry, and the operators resolve some image names themselves where no chart value can reach them — so this has to be node-level, not chart-level. Note containerd does **not** treat `registry-1.docker.io` as `docker.io`; list both.

### Operators, and why the namespace matters

Install each operator at a pinned version, in the namespace named here:

| Operator | Namespace | Version | Engine |
|---|---|---|---|
| CloudNativePG | `cnpg-system` | ≥ 1.29.1 | PostgreSQL, and the FerretDB backend |
| Percona Operator for MySQL | `ps-operator` | current stable, **installed cluster-wide** (`watchAllNamespaces=true`) | MySQL |
| mariadb-operator | `mariadb-operator` | 26.x | MariaDB |
| valkey-operator | `valkey-operator-system` | only if the Valkey beta is opened | Valkey |
| opensearch-k8s-operator | `opensearch-operator-system` | 3.0.2 | OpenSearch |
| Strimzi | `strimzi-system` | 1.1.0, **watch-all mode** (`watchAnyNamespace: true`) | Kafka |
| prometheus-operator (+ stack) | `monitoring` | optional; **required** for storage autoscaling | all |
| Vertical Pod Autoscaler (recommender only) | `kube-system` | only if autoscaling is offered | all |

**The namespace column is not a suggestion.** Each database gets a NetworkPolicy that allowlists operator-to-pod traffic by exactly these namespace names. Install an operator somewhere else and its probes and management traffic are silently denied — the database comes up and then behaves inexplicably.

Two of these have a failure mode worth knowing before you hit it:

- **Percona MySQL scoped to its own namespace** (the default install) never sees a `PerconaServerMySQL` in a customer's `stdb-*` namespace. The CR is created, nothing reconciles it, and no pods ever appear. Verify with `kubectl -n ps-operator get deploy -o yaml | grep -A1 WATCH_NAMESPACE`.
- **Strimzi CRDs are not upgraded by `helm upgrade`.** Apply the new CRD bundle with `kubectl apply` on every operator upgrade.

Record the installed operator versions next to the chart version you release. CRD drift between an operator and the chart is the main compatibility risk here.

### One-time manifests

From `deploy/dbaas-cluster/`:

```sh
kubectl apply -f kubelet-api-admin.yaml   # skip only if `kubectl logs` already works
kubectl apply -f rbac.yaml                # Stratos identity + least-privilege RBAC
kubectl apply -f appproject.yaml          # ArgoCD guardrail
kubectl apply -f repo-credential.yaml     # chart pull credential

kubectl -n argocd patch configmap argocd-cm --type merge --patch-file argocd-health.yaml
kubectl -n argocd rollout restart statefulset \
  -l app.kubernetes.io/name=argocd-application-controller
```

As with the management cluster, the health-check patch is what stops Stratos reporting **Ready** for a database that has no pods.

## The OpenStack side

- **A dedicated `dbaas` Keystone project.** Database pods live there; their endpoints are shared into customer networks by Neutron RBAC. Its id goes in `config.database.osProjectId`.
- **The member subnet** the DBaaS cluster's nodes sit on, so Octavia can pool them: `config.database.memberSubnetId`.
- **Octavia quota** — one load balancer per database, so size it against how many databases you expect, not how many customers.
- **Cinder volume quota** — one volume per database instance, plus one per replica. Watch this one: the default StorageClass reclaim policy decides whether a deleted database frees its volume or leaves it behind forever. With `Retain`, every database ever deleted keeps consuming quota until someone removes the volume by hand, and new provisioning fails with `VolumeLimitExceeded` once you hit the limit.

## Registering the provider

**System → Cloud providers → Add provider**, then the Managed Databases type:

| Field | What it is |
|---|---|
| `secret.kubeconfig` | DBaaS-cluster kubeconfig from the service account. **Required.** |
| `config.argocd.chartRepo` | OCI repo holding the `database-cluster` chart. **Required.** |
| `config.argocd.chartVersion` | Pinned chart version. **Required — never `latest`.** |
| `config.database.memberSubnetId` | Subnet the DBaaS nodes sit on, for Octavia pool members. **Required.** |
| `config.database.osProjectId` | The dbaas Keystone project — the Neutron RBAC target. **Required.** |
| `config.metrics` | Prometheus URL; needed for storage autoscaling. |

Enable the engines you actually have operators for. An engine offered without its operator produces a database that provisions and then never becomes ready.

## Verifying it works

Create one database of each engine you plan to offer, from a real customer project, and for each one:

1. Confirm it reaches **Ready** with pods actually running — not just an `Application` reporting Synced.
2. Connect from a server on the same network. The endpoint is internal by design; if you can reach it from outside, something is wrong.
3. Take a backup, then **restore it into a new database** and check the data is there. A restore that produces an empty database is the failure you least want to discover during an incident.
4. Delete it, and confirm the namespace is empty afterwards — no leftover PVCs, no orphaned `Application`.

Step 4 is the one people skip. A delete path that leaves resources behind quietly eats the quotas above until provisioning stops working for everyone.

## Upgrading the platform

Same posture as Managed Kubernetes: publishing a new chart version and re-pinning the provider leaves existing databases alone. Customers apply the update themselves, or you bump one or all of them from the provider's database list.

New chart versions are only used by databases created *after* the pin changes, so a version you delete from the registry breaks every database still pinned to it.
