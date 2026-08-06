# Managed Kubernetes

**Platform → Kubernetes** gives you a Kubernetes cluster without running the control plane yourself. The control plane is hosted and operated by the platform; the worker nodes are ordinary servers in *your* project, on *your* network, billed like any other instance.

That split is the thing to understand before you start:

| | Where it runs | Who operates it | What it costs |
|---|---|---|---|
| Control plane (API server, etcd, scheduler) | Platform infrastructure | The operator | Not billed as your instances |
| Worker nodes | Your project, your network | You choose the shape | Billed as servers + volumes |

Because workers are your servers, they appear under **Compute → Servers**, consume your project quota, and are reachable from the rest of your network. Deleting them from the Servers page is not how you resize a cluster — use the node-group controls described below, or Cluster API will simply build them again.

## Creating a cluster

**Create cluster** asks for everything up front. Nothing here is guesswork — every field has a safe default except the network.

![The Create Kubernetes cluster dialog](/docs-img/k8s-create.png)

**Name and version.** The name is display-only; the platform derives every underlying object from an internal identifier, so two clusters may share a name and renaming never breaks anything. The version list only offers releases the operator has published a node image for.

**High availability** runs three control-plane replicas instead of one. It changes nothing about your workers or your bill — the control plane is not billed as your instances — so leave it on for anything you would be unhappy to lose.

**Node groups.** A node group is a set of identical workers. Each group has its own flavor, count, disk size, labels and taints, so a cluster can mix, say, a small always-on CPU pool with a GPU pool. Add more with **Add group**.

- **Nodes** is a fixed count, or a min–max range when **Autoscale** is on. Autoscaling adds and removes nodes as pending pods require, within that range.
- **Public IP / node** attaches a floating IP to every node in the group. Only turn it on if something outside your network must reach the nodes directly; a cluster works fine without it.
- **Disk (GiB)** is the node root volume. Node images are larger than most flavors' built-in disk, so this is a real volume and is billed as one.
- **Node labels** and **taints** are applied at join time. They are part of the node's bootstrap configuration, so changing them replaces the group's nodes.
- **Security groups** are additive. The platform always attaches its own group to the cluster's nodes; anything you pick here is added on top. Leaving it empty is the normal choice.

**Add-ons.** Metrics Server and Node Problem Detector are on by default and worth keeping — the first powers `kubectl top` and horizontal pod autoscaling, the second surfaces node faults as Kubernetes Events. cert-manager, NGINX Ingress, the monitoring stack, the NVIDIA GPU Operator and Reloader are off by default; turn on what you need and the platform installs and maintains it. The monitoring stack is the heavy one — budget roughly 2 GB of RAM for it.

**Network.** Worker nodes are placed on the network and subnet you pick, so it must be one that reaches the internet through a router. This is the only field with no default: the platform will not guess which of your networks a cluster belongs on.

**Allowed API CIDRs** restricts who may reach the Kubernetes API. Leave it empty and the API is reachable from anywhere the endpoint routes; set it and everything else is refused.

**DNS servers** are written onto every node. Leave it empty and nodes use whatever DNS your subnet hands out over DHCP, which is what most clusters want. Note that changing it later replaces every worker in the cluster — the value is part of node bootstrap.

Creation takes several minutes: the platform provisions the control plane, waits for its endpoint to answer, then boots and joins the workers.

## Connecting

Open the cluster and use **Download kubeconfig**.

```sh
export KUBECONFIG=~/Downloads/my-cluster-kubeconfig.yaml
kubectl get nodes
```

The **API endpoint** shown is the cluster's DNS name, not a raw address — the certificate is issued for that name, so use it rather than the IP if you write the endpoint into your own tooling. The address is also shown for cases where DNS has not propagated yet.

**Rotate kubeconfig** issues a fresh administrator kubeconfig. It is not a revocation: kubeconfigs already downloaded keep working. Use it when you want a new credential, not when you want to lock someone out.

For day-to-day access by a team rather than by a shared file, **Configure OIDC authentication** points the cluster's API server at your identity provider, so `kubectl` authenticates people instead of a static credential.

## Changing a running cluster

Everything below is available from the cluster's detail page and is applied by the platform — you never edit Cluster API objects yourself.

**Edit node groups** changes counts, adds groups, or removes them. Growing a group adds nodes; shrinking it drains and removes them.

**Upgrade** moves the cluster to a newer Kubernetes version. The platform only offers versions it can actually reach from where you are — the same minor with a higher patch, or exactly one minor up. Multi-minor jumps and downgrades are refused, because kubeadm does not support them.

**Manage add-ons** turns the optional components on or off after the fact.

**Platform update** is separate from a Kubernetes upgrade and is offered when the operator has published a newer version of the managed platform components. It restarts those components and, if the node template changed, replaces worker nodes one at a time — a new node comes up and is drained into before the old one is removed. Your Kubernetes version, workloads and data are untouched. It is opt-in: the platform never rolls your cluster on its own.

Some changes replace nodes rather than modify them, because the value is baked into a node at bootstrap: node labels, taints, security groups, DNS servers, and most platform updates. The dialogs say so where it applies. Replacement is rolling, one node at a time, so a cluster with spare capacity keeps serving throughout.

## Storage and load balancers

The cluster comes wired to your cloud:

- **PersistentVolumeClaims** are provisioned as block volumes through the Cinder CSI driver, using the cluster's default storage class. They are billed as volumes.
- **Services of type LoadBalancer** are provisioned as real load balancers in your project.

Both appear under **Storage → Volumes** and **Network → Load balancers** alongside everything else you own, and both are billed normally. A PVC left behind by a deleted workload keeps costing money exactly like a forgotten volume — clean up claims you no longer need.

## Deleting

**Delete** removes the control plane and every worker node. Workloads and data on the cluster are lost, including the volumes backing your PVCs. There is no undo, and no separate "keep the disks" option — take what you need off the cluster first.

## Troubleshooting

**A node stays out of the cluster.** The instance is running but never appears in `kubectl get nodes`. Look at the node's serial console from **Compute → Servers → the node → Console output**: bootstrap problems are almost always visible there and nowhere else. The platform replaces a node that never reports in, but it waits 45 minutes first to avoid churning a slow boot.

**The cluster says degraded.** That is the aggregate of the control plane and every node group. A single node still joining is enough to show it, and it clears on its own. If it persists, check the node count on the detail page against what you asked for.

**`kubectl` cannot reach the API.** If you set **Allowed API CIDRs**, check that your current address is inside one of them. Otherwise confirm you are using the DNS name from the detail page rather than a cached address.
