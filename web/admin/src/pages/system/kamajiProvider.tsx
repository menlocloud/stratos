// kamajiProvider.tsx — the shape and the form of a `kamaji` Managed Kubernetes provider, shared by
// the Add-provider dialog and the provider detail page so the two can never drift.
//
// config.provider === "kamaji": customer clusters are ArgoCD Applications of the pinned
// `openstack-kamaji-cluster` chart, applied to the provider's Kamaji MANAGEMENT cluster; the secret
// is a kubeconfig for a stratos-scoped service account there. Worker VMs, and everything they cost,
// land in the customer's own OpenStack project — so a project needs BOTH this service and an
// OpenStack one.

import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"

export type KamajiFormState = {
  name: string
  region: string
  kubeconfig: string
  chartRepo: string
  chartVersion: string
  argoNamespace: string
  argoProject: string
  dataStoreName: string
  floatingNetworkId: string
  externalNetworkId: string
  dnsZone: string
  rootVolumeGiB: string
  supportKeypairName: string
  supportKeypairPublicKey: string
  allowedCidrs: string // comma-separated
  versions: string // one "1.35.4=<glance-image-id>" per line
  flavors: string // optional allowlist, one Nova flavor id per line (empty = all tenant flavors)
}

export const emptyKamajiForm: KamajiFormState = {
  name: "",
  region: "az1",
  kubeconfig: "",
  chartRepo: "ghcr.io/menlocloud/stratos-charts",
  chartVersion: "",
  argoNamespace: "argocd",
  argoProject: "stratos-k8s",
  dataStoreName: "default",
  floatingNetworkId: "",
  externalNetworkId: "",
  dnsZone: "",
  rootVolumeGiB: "120",
  supportKeypairName: "",
  supportKeypairPublicKey: "",
  allowedCidrs: "",
  versions: "",
  flavors: "",
}

// splitLines is the shared reader for the one-per-line textarea fields.
const splitLines = (s: string) => s.split("\n").map((v) => v.trim()).filter(Boolean)

// parseVersions turns the "version=imageId" lines into the config.cluster.versions map;
// null = at least one non-empty line is malformed.
export function parseVersions(raw: string): Record<string, string> | null {
  const out: Record<string, string> = {}
  for (const line of raw.split("\n")) {
    const t = line.trim()
    if (!t) continue
    const eq = t.indexOf("=")
    if (eq <= 0 || eq === t.length - 1) return null
    out[t.slice(0, eq).trim()] = t.slice(eq + 1).trim()
  }
  return out
}

const splitCsv = (s: string) => s.split(",").map((v) => v.trim()).filter(Boolean)

export const kamajiFormValid = (f: KamajiFormState, requireKubeconfig = true) => {
  const versions = parseVersions(f.versions)
  const required = [f.name, f.region, f.chartRepo, f.chartVersion]
  if (requireKubeconfig) required.push(f.kubeconfig)
  return (
    required.every((v) => v.trim() !== "") &&
    Number(f.rootVolumeGiB) >= 1 &&
    versions !== null &&
    Object.keys(versions).length > 0
  )
}

// kamajiConfigBlocks are the two config sub-objects stratos reads. The admin update route replaces
// top-level config keys wholesale, so each block must always be sent complete.
export function kamajiConfigBlocks(f: KamajiFormState) {
  return {
    argocd: {
      namespace: f.argoNamespace.trim() || "argocd",
      project: f.argoProject.trim() || "default",
      chartRepo: f.chartRepo.trim(),
      chartName: "openstack-kamaji-cluster",
      chartVersion: f.chartVersion.trim(),
    },
    cluster: {
      ...(f.dataStoreName.trim() ? { dataStoreName: f.dataStoreName.trim() } : {}),
      ...(f.floatingNetworkId.trim() ? { floatingNetworkId: f.floatingNetworkId.trim() } : {}),
      ...(f.externalNetworkId.trim() ? { externalNetworkId: f.externalNetworkId.trim() } : {}),
      ...(f.dnsZone.trim() ? { dnsZone: f.dnsZone.trim() } : {}),
      ...(Number(f.rootVolumeGiB) > 0 ? { rootVolumeGiB: Number(f.rootVolumeGiB) } : {}),
      ...(f.supportKeypairName.trim() ? { supportKeypairName: f.supportKeypairName.trim() } : {}),
      ...(f.supportKeypairPublicKey.trim() ? { supportKeypairPublicKey: f.supportKeypairPublicKey.trim() } : {}),
      ...(splitCsv(f.allowedCidrs).length ? { allowedCidrs: splitCsv(f.allowedCidrs) } : {}),
      // Omitted entirely when empty — an absent key means "offer all tenant flavors".
      ...(splitLines(f.flavors).length ? { flavors: splitLines(f.flavors) } : {}),
      versions: parseVersions(f.versions) ?? {},
    },
  }
}

export function kamajiFormToBody(f: KamajiFormState) {
  const region = f.region.trim()
  return {
    name: f.name.trim(),
    type: "CLOUD",
    status: "PUBLIC",
    config: {
      provider: "kamaji",
      regions: { [region]: { name: region, country: "", displayName: region } },
      // kubernetes is the ONLY service a kamaji provider serves — enabled directly, no discovery.
      services: { kubernetes: { [region]: true } },
      ...kamajiConfigBlocks(f),
    },
    secret: { kubeconfig: f.kubeconfig.trim() },
  }
}

// kamajiFormFromService fills the form from a stored provider so the detail page can edit it. The
// kubeconfig is never returned by the API (secrets are stripped) — it stays blank and is only sent
// when the operator types a new one.
export function kamajiFormFromService(svc: {
  name?: string
  config?: Record<string, any>
}): KamajiFormState {
  const cfg = svc.config ?? {}
  const argo = (cfg.argocd as Record<string, any>) ?? {}
  const cluster = (cfg.cluster as Record<string, any>) ?? {}
  const regions = Object.keys((cfg.regions as Record<string, unknown>) ?? {})
  const versions = (cluster.versions as Record<string, string>) ?? {}
  return {
    ...emptyKamajiForm,
    name: svc.name ?? "",
    region: String(cfg.region ?? regions[0] ?? ""),
    kubeconfig: "",
    chartRepo: String(argo.chartRepo ?? emptyKamajiForm.chartRepo),
    chartVersion: String(argo.chartVersion ?? ""),
    argoNamespace: String(argo.namespace ?? "argocd"),
    argoProject: String(argo.project ?? "default"),
    dataStoreName: String(cluster.dataStoreName ?? ""),
    floatingNetworkId: String(cluster.floatingNetworkId ?? ""),
    externalNetworkId: String(cluster.externalNetworkId ?? ""),
    dnsZone: String(cluster.dnsZone ?? ""),
    rootVolumeGiB: String(cluster.rootVolumeGiB ?? emptyKamajiForm.rootVolumeGiB),
    supportKeypairName: String(cluster.supportKeypairName ?? ""),
    supportKeypairPublicKey: String(cluster.supportKeypairPublicKey ?? ""),
    allowedCidrs: ((cluster.allowedCidrs as string[]) ?? []).join(","),
    versions: Object.entries(versions).map(([v, img]) => `${v}=${img}`).join("\n"),
    flavors: ((cluster.flavors as string[]) ?? []).join("\n"),
  }
}

export function KamajiProviderForm({
  form, setForm, mode = "create",
}: {
  form: KamajiFormState
  setForm: (f: KamajiFormState) => void
  // "edit" hides the fields that cannot change after creation and makes the kubeconfig optional.
  mode?: "create" | "edit"
}) {
  const edit = mode === "edit"
  return (
    <div className="grid gap-4">
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="km-name">Display name</Label>
          <Input id="km-name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Managed Kubernetes AZ1" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="km-region">Region</Label>
          <Input id="km-region" value={form.region} onChange={(e) => setForm({ ...form, region: e.target.value })} placeholder="az1" disabled={edit} />
        </div>
      </div>
      <div className="grid gap-2">
        <Label htmlFor="km-kubeconfig">
          Management-cluster kubeconfig{edit ? " (leave blank to keep the current one)" : ""}
        </Label>
        <Textarea
          id="km-kubeconfig"
          className="min-h-28 font-mono text-xs"
          value={form.kubeconfig}
          onChange={(e) => setForm({ ...form, kubeconfig: e.target.value })}
          placeholder="apiVersion: v1&#10;kind: Config&#10;…"
          autoComplete="off"
        />
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="km-repo">Chart OCI repo</Label>
          <Input id="km-repo" className="font-mono" value={form.chartRepo} onChange={(e) => setForm({ ...form, chartRepo: e.target.value })} placeholder="ghcr.io/menlocloud/stratos-charts" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="km-chartver">Chart version (pinned)</Label>
          <Input id="km-chartver" className="font-mono" value={form.chartVersion} onChange={(e) => setForm({ ...form, chartVersion: e.target.value })} placeholder="0.5.0" />
        </div>
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="km-argons">ArgoCD namespace</Label>
          <Input id="km-argons" value={form.argoNamespace} onChange={(e) => setForm({ ...form, argoNamespace: e.target.value })} placeholder="argocd" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="km-argoproj">ArgoCD AppProject</Label>
          <Input id="km-argoproj" value={form.argoProject} onChange={(e) => setForm({ ...form, argoProject: e.target.value })} placeholder="stratos-k8s" />
        </div>
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="km-fnet">API-server LB floating network</Label>
          <Input id="km-fnet" className="font-mono" value={form.floatingNetworkId} onChange={(e) => setForm({ ...form, floatingNetworkId: e.target.value })} autoComplete="off" />
          <p className="text-xs text-muted-foreground">
            On the <strong>management</strong> cluster's cloud. The floating network the Kamaji
            API-server LoadBalancer draws its public IP from — the address customers connect to.
          </p>
        </div>
        <div className="grid gap-2">
          <Label htmlFor="km-extnet">Default worker external network</Label>
          <Input id="km-extnet" className="font-mono" value={form.externalNetworkId} onChange={(e) => setForm({ ...form, externalNetworkId: e.target.value })} autoComplete="off" />
          <p className="text-xs text-muted-foreground">
            In the <strong>customer's</strong> project. The fallback egress + LoadBalancer-Service
            IP pool for worker clusters. Usually leave blank: a cluster on a chosen network derives
            this from that network's router; this only applies when nothing can be derived. Required
            if the customer cloud has more than one external network.
          </p>
        </div>
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="km-datastore">Kamaji DataStore</Label>
          <Input id="km-datastore" value={form.dataStoreName} onChange={(e) => setForm({ ...form, dataStoreName: e.target.value })} placeholder="default" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="km-dns">DNS zone (optional)</Label>
          <Input id="km-dns" value={form.dnsZone} onChange={(e) => setForm({ ...form, dnsZone: e.target.value })} placeholder="k8s.example.com" />
        </div>
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="km-rootdisk">Default node disk (GiB)</Label>
          <Input id="km-rootdisk" type="number" min={1} value={form.rootVolumeGiB} onChange={(e) => setForm({ ...form, rootVolumeGiB: e.target.value })} />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="km-cidrs">Default API allowlist (CIDRs, comma-separated)</Label>
          <Input id="km-cidrs" className="font-mono" value={form.allowedCidrs} onChange={(e) => setForm({ ...form, allowedCidrs: e.target.value })} placeholder="10.0.0.0/8,203.0.113.4/32" />
        </div>
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="km-keyname">Support SSH key name</Label>
          <Input id="km-keyname" className="font-mono" value={form.supportKeypairName} onChange={(e) => setForm({ ...form, supportKeypairName: e.target.value })} placeholder="stratos-support" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="km-keypub">Support SSH public key (optional)</Label>
          <Input id="km-keypub" className="font-mono" value={form.supportKeypairPublicKey} onChange={(e) => setForm({ ...form, supportKeypairPublicKey: e.target.value })} placeholder="ssh-ed25519 AAAA…" autoComplete="off" />
        </div>
      </div>
      <div className="grid gap-2">
        <Label htmlFor="km-flavors">Flavor allowlist (optional, one Nova flavor ID per line)</Label>
        {/* IDs, not names: the create wizard sends the flavor's id and the backend matches on it. */}
        <Textarea
          id="km-flavors"
          className="min-h-20 font-mono text-xs"
          value={form.flavors}
          onChange={(e) => setForm({ ...form, flavors: e.target.value })}
          placeholder="8a5a8eb8-3685-42d0-a40b-9fbb8f8a1f60&#10;37ee35b5-e997-47da-b42b-be8d96a16731"
        />
        <p className="text-xs text-muted-foreground">
          Nova flavor <em>IDs</em> (UUIDs), not names. Only these are offered for worker pools;
          empty = every flavor in the tenant. Flavors with ephemeral or swap disk are never offered
          either way — worker nodes boot from a volume.
        </p>
      </div>
      <div className="grid gap-2">
        <Label htmlFor="km-versions">Kubernetes versions (one “version=image-id” per line)</Label>
        <Textarea
          id="km-versions"
          className="min-h-20 font-mono text-xs"
          value={form.versions}
          onChange={(e) => setForm({ ...form, versions: e.target.value })}
          placeholder="1.35.4=db37655f-…"
        />
      </div>
      <p className="text-xs text-muted-foreground">
        The kubeconfig belongs to a stratos service account on the Kamaji management cluster (ArgoCD +
        AppProject installed there). Only versions listed here are offered to customers. The support
        SSH key is injected into every node for break-glass access — supply the public key to have
        stratos create it, or leave it blank and create the keypair yourself.
      </p>
    </div>
  )
}
