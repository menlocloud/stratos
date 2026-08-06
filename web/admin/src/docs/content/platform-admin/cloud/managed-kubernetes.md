# Offering Managed Kubernetes

Stratos can sell Kubernetes clusters whose control planes you host and whose worker nodes land in the customer's own OpenStack project. Stratos never installs Kubernetes itself: it writes an ArgoCD `Application` onto a **management cluster** you run, and Cluster API does the rest.

So there are two clusters in this picture, and keeping them straight is most of the work:

| | What it is | Who builds it |
|---|---|---|
| **Management cluster** | One long-lived cluster running Kamaji, Cluster API and ArgoCD | **You**, once, before offering the service |
| Tenant clusters | One per customer cluster — control plane as pods on the management cluster, workers as VMs in the customer's project | Stratos, on demand |

## Building the management cluster

This is a normal Kubernetes cluster you own. It needs four things installed, at pinned versions:

1. **Kamaji operator** — runs each tenant control plane as pods rather than as dedicated VMs. This is what makes a control plane cheap enough to give every customer one.
2. **cluster-api-operator** with the **KamajiControlPlane** provider.
3. **CAPO** (Cluster API Provider OpenStack) — creates the worker VMs, ports, security groups and load balancers in the customer's project.
4. **CAAPH** (Cluster API Add-on Provider Helm) — installs the optional add-ons customers pick.

Plus:

5. **ArgoCD**, in the `argocd` namespace. Stratos writes `Application` objects and nothing else; ArgoCD is what reconciles them.
6. **A container registry mirror** the tenant nodes can pull through. Every node image and add-on image otherwise comes straight from a public registry, and a cluster then provisions at the mercy of rate limits. Configure it as `config.cluster.registryMirrors` on the provider — Stratos writes it into each node's containerd config.

### One-time manifests

The repository ships the identity and guardrails Stratos needs in `deploy/mgmt-cluster/`. Apply them in this order:

```sh
kubectl apply -f rbac.yaml            # Stratos identity + least-privilege RBAC + SA token
kubectl apply -f appproject.yaml      # ArgoCD AppProject: source/destination allowlist
kubectl apply -f repo-credential.yaml # so ArgoCD can pull the chart (skip if the package is public)

# Custom health checks — a MERGE PATCH, not kubectl apply
kubectl -n argocd patch configmap argocd-cm --type merge --patch-file argocd-health.yaml
kubectl -n argocd rollout restart statefulset \
  -l app.kubernetes.io/name=argocd-application-controller
```

The health-check patch is load-bearing rather than cosmetic. Without it ArgoCD calls a cluster Healthy the moment the manifests apply, and the customer sees **Ready** for a cluster that has no nodes at all.

Then build a kubeconfig from the service-account token — the recipe is in the comment block at the bottom of `rbac.yaml`. That kubeconfig is what you paste into the provider.

### Publishing the cluster chart

Stratos deploys the `openstack-kamaji-cluster` chart from an OCI registry at a **pinned** version, never `latest`. Publish it from this repository by pushing an `openstack-kamaji-cluster-v*` tag; see `deploy/charts/README.md`.

Keep every version any live cluster is pinned to. A customer cluster stays on the version it was created with until someone applies a platform update, so deleting an old chart version breaks reconciliation for every cluster still on it.

## The OpenStack side

Managed Kubernetes rides on an existing OpenStack provider — the customer's worker VMs are created in the customer's own Keystone project through it. Two things on the cloud itself are worth checking before you offer the service:

- **`force_config_drive = True` in nova.** Worker user-data carries the kubeadm join. Without a config drive its only delivery path is the Neutron metadata service, and a node that cannot reach it boots healthy, keeps the hostname `localhost`, never runs kubeadm, and reports nothing anywhere except its serial console.
- **Node images per Kubernetes version.** Each version you offer needs an image, registered under `config.cluster.versions`. Optional variants (GPU driver builds, for example) go under `config.cluster.imageVariants`.

## Registering the provider

**System → Cloud providers → Add provider**, then choose the Managed Kubernetes type. The fields that matter:

| Field | What it is |
|---|---|
| `secret.kubeconfig` | The management-cluster kubeconfig built above. **Required.** |
| `config.argocd.chartRepo` | OCI repo holding the chart, e.g. `ghcr.io/menlocloud/stratos-charts`. **Required.** |
| `config.argocd.chartVersion` | Pinned chart version. **Required — never `latest`.** |
| `config.argocd.namespace` / `project` | Where Stratos writes Applications, and the AppProject guardrail applied above. |
| `config.cluster.openstackServiceId` | The OpenStack provider whose projects the worker VMs land in. |
| `config.cluster.versions` | Kubernetes version → node image id. |
| `config.cluster.dnsZone` | Zone the per-cluster API DNS name is published in, via external-dns. |
| `config.cluster.externalNetworkId` / `floatingNetworkId` | External network for the API load balancer and for per-node floating IPs. |
| `config.cluster.registryMirrors` | Pull-through mirrors written into every node's containerd config. |
| `config.cluster.rootVolumeGiB` | Default node root volume size. Node images are bigger than most flavors' built-in disk. |
| `config.cluster.storageVolumeType` | Cinder volume type behind the tenant cluster's default StorageClass. |
| `config.cluster.scheduling` | Node selector and tolerations for the hosted control-plane pods. |

Enable the `kubernetes` service for the regions you want it offered in, exactly as with any other service.

## Upgrading the platform

Publishing a newer chart version and re-pinning the provider does **not** touch existing clusters — by design. Customers see a *Platform update available* offer on their cluster and apply it when it suits them. You can also bump a single cluster, or all of them, from the provider's cluster list when you need to.

Read the chart's `Chart.yaml` before bumping the pin: each version's header says what it changes and, in particular, whether it replaces worker nodes. Anything that lands in a node's bootstrap configuration — DNS servers, security groups, node labels — rolls every worker in the cluster when it changes.

## Verifying it works

Create a cluster from a test project with **two** nodes, not one. A single-node cluster hides every cross-node networking problem there is, and passes while a real cluster would not. Then check:

```sh
kubectl get nodes          # both nodes Ready
kubectl exec <pod> -- true # API server can reach the kubelets
```

and, from a pod, reach a pod on the *other* node. If that fails while the same destination works from the node itself, the problem is in the OpenStack security groups, not in Kubernetes.
