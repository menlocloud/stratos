# deploy/charts — the Managed Kubernetes cluster chart

`openstack-kamaji-cluster` is the chart stratos installs, once per customer cluster, on a Kamaji
management cluster: a CAPI `Cluster` + `OpenStackCluster`, a `KamajiControlPlane` (the control plane
runs as pods on the management cluster, not as VMs), worker `MachineDeployment`s in the customer's
own keystone project, a management-side OpenStack cloud-controller-manager, and the CAAPH addons.
`cluster-addons-menlo` is its vendored subchart.

Stratos never templates it itself. `internal/cloud/kamaji/values.go` builds a values document and
stratos writes it into an ArgoCD `Application` (`spec.source.helm.valuesObject`) pointing at the
published chart; ArgoCD renders and syncs. So this chart IS stratos' Managed Kubernetes product
surface, which is why it lives here rather than in a separate chart repo — a change to what a
cluster looks like is one commit, one review, one release.

## Relationship to `menlocloud/charts`

A copy of both charts also lives in `menlocloud/charts` and is published to
`oci://ghcr.io/menlocloud/charts`. That copy backs the four hand-written cluster wrappers in
`infra-ops` (dev / stag / prod / sysadmin) and is deliberately left alone.

The two copies are independent forks with the same template and helper names, so a fix can be
diffed and carried across with `diff -r`. They are published to **different OCI paths**, which is
what keeps them from overwriting each other:

| copy | published to | consumed by |
|---|---|---|
| `menlocloud/charts` | `oci://ghcr.io/menlocloud/charts` | infra-ops cluster wrappers |
| this one | `oci://ghcr.io/menlocloud/stratos-charts` | stratos, per customer cluster |

## How the stratos copy differs

- **The OpenStack CCM runs management-side** (`templates/control-plane/ccm-openstack.yaml`), reading
  the tenant API through the Kamaji-issued admin kubeconfig. `addons.openstack.enabled` is
  correspondingly **off** by default — that flag also gates the CAAPH `cloud-config` push, which
  copies the cluster's clouds.yaml into the workload cluster where any cluster-admin of the tenant
  could read it. A customer cluster therefore holds no OpenStack credential at all.
  Consequence, accepted for now: no Cinder CSI, so no dynamic block storage.
- **Addon defaults are the ones a Kamaji cluster on our clouds needs** — cilium with native routing,
  images from `registry.menlo.ai`, `etcd-defrag` off (Kamaji owns etcd), GPU/RDMA operators off,
  and `tolerations: [{operator: Exists}]` on everything that has to come up before the CCM has
  initialised the nodes. In the other copy these live copy-pasted in each infra-ops wrapper.

## Publishing

`.github/workflows/helm.yml` packages and pushes on a `v*` tag, using the **chart's own
`Chart.yaml` version** (not the git tag — this chart's lifecycle is not the API's), and skips when
that version is already in the registry. So: bump `version:` in `Chart.yaml` in the same PR as the
change, and it publishes with the next release tag.

Stratos pins the version per cluster in the provider config (`config.argocd.chartVersion`) and
never resolves `latest` — an existing cluster keeps its pin until something explicitly moves it.

## Working on it

```sh
cd deploy/charts/openstack-kamaji-cluster
helm dependency build
helm template stc-test . -n st-demo -f /tmp/values.yaml
```

For a values document shaped exactly like the one stratos generates, dump one from
`kamaji.BuildValues` — `TestBuildValues` in `internal/cloud/kamaji/kamaji_test.go` pins every key
this chart reads, and is the guard against the two drifting apart.
