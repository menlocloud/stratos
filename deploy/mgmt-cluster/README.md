# Management-cluster manifests for the Stratos `kamaji` provider

Operator-applied, one-time setup on the **Kamaji management cluster** (the cluster running
the Kamaji operator + cluster-api-operator/CAPO — infra-ops `kamaji-cluster-az1`, or a dev
equivalent). Stratos does not apply these itself: they create the identity and guardrails
stratos then operates *within*.

Full operator runbook: [`docs/managed-k8s.md`](../../docs/managed-k8s.md). Design source:
`tasks/managed-k8s-plan.md`.

## Prerequisites

1. **Kamaji stack live** — Kamaji operator, cluster-api-operator with the KamajiControlPlane
   provider, CAPO, CAAPH. (Already true on `kamaji-cluster-az1`; a dev mgmt cluster needs the
   same four charts — plan §3.0.)
2. **ArgoCD installed** on this cluster (plan D3). Any standard install; the manifests here
   assume the `argocd` namespace.
3. **Chart CI publishing `openstack-kamaji-cluster`** (and its mirrored images) to
   `ghcr.io/menlocloud/charts` at **pinned versions** — never `latest`/`0.0.0+latest`. The
   version stratos deploys is pinned per provider (`config.argocd.chartVersion`) and per
   existing cluster; the registry must keep every version a live cluster is pinned to.

## Apply order

```sh
# 1. Stratos identity + least-privilege RBAC + SA token
kubectl apply -f rbac.yaml

# 2. AppProject guardrail (source/destination allowlist for stratos Applications)
kubectl apply -f appproject.yaml

# 3. Custom health checks — a MERGE PATCH onto the existing argocd-cm, NOT kubectl apply
kubectl -n argocd patch configmap argocd-cm --type merge --patch-file argocd-health.yaml

# 4. Restart the ArgoCD controllers so the new Lua health checks load
kubectl -n argocd rollout restart statefulset/argocd-application-controller
```

Then assemble the stratos kubeconfig from the SA token (recipe in the comment block at the
bottom of `rbac.yaml`) and paste it as the provider secret when registering the `kamaji`
external service (admin UI → Cloud providers → Add provider, or
`deploy/seed/external-service-dev.json`).

## What each file is

| File | Contents |
|---|---|
| `rbac.yaml` | `stratos-system` namespace, `stratos` ServiceAccount, ClusterRole/binding limited to exactly the verbs `internal/cloud/kamajik8s/client.go` uses, a namespaced Role for Application CRUD in `argocd`, the SA token Secret, and the kubeconfig recipe. Read its header for why the secrets grant is cluster-wide (RBAC cannot wildcard `st-*` namespaces). |
| `appproject.yaml` | AppProject `stratos-k8s`: sourceRepos = our OCI registry only, destinations = in-cluster `st-*` only, empty (conservative) clusterResourceWhitelist. |
| `argocd-health.yaml` | argocd-cm patch: Lua health for TenantControlPlane / Cluster / MachineDeployment. **Status fields must be validated during the live drill** (plan §3.0) — see the file header. |

## Verify

```sh
# RBAC is exactly scoped (yes / yes / no):
kubectl auth can-i list tenantcontrolplanes.kamaji.clastix.io -A --as=system:serviceaccount:stratos-system:stratos
kubectl auth can-i create applications.argoproj.io -n argocd --as=system:serviceaccount:stratos-system:stratos
kubectl auth can-i '*' '*' -A --as=system:serviceaccount:stratos-system:stratos

# AppProject present:
kubectl -n argocd get appproject stratos-k8s

# Health customizations loaded:
kubectl -n argocd get cm argocd-cm -o jsonpath='{.data}' | grep -o 'resource\.customizations\.health\.[^"]*'
```
