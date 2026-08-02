# deploy/charts — every Helm chart this repo ships

Four charts live here:

- **`stratos`** — the application chart: the stratos API plus its bundled PostgreSQL,
  RabbitMQ and Keycloak. This is what `make deploy` installs and what CI publishes to
  `oci://ghcr.io/<owner>/charts` at the release tag (see its own README).
- **`openstack-kamaji-cluster`** (+ its vendored subchart **`cluster-addons-menlo`**) — the
  Managed Kubernetes cluster chart, described below.
- **`database-cluster`** — the Managed Database (DBaaS) chart: ONE chart, five engines
  (`values.engine` discriminator — postgresql / mysql / mariadb / valkey / ferretdb) rendering
  the engine's operator CR plus an Octavia internal LB Service into the tenant network and a
  NetworkPolicy, once per customer database, on the ops-built DBaaS cluster. Same delivery
  model as the kamaji chart: stratos writes an ArgoCD `Application` whose values come from
  `internal/cloud/dbaas/values.go` (`BuildValues` — the values contract's other half; see the
  chart's `Chart.yaml` header for the change discipline). Cluster-side setup lives in
  `deploy/dbaas-cluster/`. NOTE: bare defaults deliberately do not render (`engine` is
  required) — template it with one of `examples/values-<engine>.yaml`, which is exactly what
  CI does.

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

`.github/workflows/helm.yml` gives each chart its own tag-driven release, because their
lifecycles are independent (a customer cluster pins a chart version; the app releases on its
own cadence):

| chart | release tag | version source |
|---|---|---|
| `stratos` (app) | `vX.Y.Z` (shared with the image tags) | the tag |
| `openstack-kamaji-cluster` | `openstack-kamaji-cluster-vX.Y.Z` | `Chart.yaml` — the tag must match or the job fails |
| `cluster-addons-menlo` | `cluster-addons-menlo-vX.Y.Z` | `Chart.yaml` — same match rule |
| `database-cluster` | `database-cluster-vX.Y.Z` | `Chart.yaml` — same match rule |

Flow for the k8s charts: bump `version:` in `Chart.yaml` in the PR that changes the chart,
merge, then cut the matching tag (`git tag openstack-kamaji-cluster-v0.7.0 && git push origin
openstack-kamaji-cluster-v0.7.0`). Publishing is idempotent — an already-published version is
skipped. A `cluster-addons-menlo` change additionally needs the parent chart's `dependencies`
pin bumped (it is vendored via `file://`) and a parent release to actually reach clusters; the
standalone publish exists for direct consumers and parity with `menlocloud/charts`.

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

## Attribution & licenses

Both charts are **modified derivatives** of the Azimuth project's
[capi-helm-charts](https://github.com/azimuth-cloud/capi-helm-charts) (`openstack-cluster` and
`cluster-addons`), © StackHPC Ltd and the Azimuth contributors, used and redistributed under the
Apache License 2.0. Each chart directory carries its own `LICENSE` (Apache-2.0) and `NOTICE`
(what was changed, and by whom) — both files ship inside the packaged chart.

The stack they orchestrate is Apache-2.0 upstream software by its respective authors:
[Kamaji](https://github.com/clastix/kamaji) and its
[CAPI control-plane provider](https://github.com/clastix/cluster-api-control-plane-provider-kamaji)
(Clastix Labs), [Cluster API](https://github.com/kubernetes-sigs/cluster-api) and
[CAPO](https://github.com/kubernetes-sigs/cluster-api-provider-openstack) (The Kubernetes
Authors), and the
[OpenStack cloud-provider](https://github.com/kubernetes/cloud-provider-openstack). When touching
templates that came from upstream, keep their in-file comments and references intact — that
attribution is part of honouring the license.
