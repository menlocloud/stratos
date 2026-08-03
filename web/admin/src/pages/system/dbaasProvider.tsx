// dbaasProvider.tsx — the shape and the form of a `dbaas` Managed Database provider, shared by
// the Add-provider dialog and the provider detail page so the two can never drift.
//
// config.provider === "dbaas": customer databases are ArgoCD Applications of the pinned
// `database-cluster` chart, applied to an ops-pre-built Kubernetes cluster that runs the DB
// operator suite; the secret is a kubeconfig for a stratos-scoped service account there. The
// database endpoint is an Octavia internal LB in the customer's tenant network, so the provider
// also records the OpenStack service it is coupled to (network sharing + LB member subnet).

import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { useAdminList } from "@/lib/hooks"

export type DbaasFormState = {
  name: string
  region: string
  kubeconfig: string
  chartRepo: string
  chartVersion: string
  argoNamespace: string
  argoProject: string
  // The OpenStack provider whose clouds host the tenant networks (neutron RBAC + Octavia).
  osServiceId: string
  // The dbaas keystone project (the DB cluster's own OpenStack tenant).
  osProjectId: string
  // The dbaas-side subnet LB members live on (Octavia member-subnet-id annotation).
  memberSubnetId: string
  // Platform DNS zone: every database gets <id>.<zone> via external-dns. Optional.
  dnsZone: string
  // cert-manager ClusterIssuer (DNS-01 for dnsZone) for real opensearch certs. Optional.
  certIssuer: string
  storageClasses: string // comma-separated
  engines: string // one "postgresql=16,17,18" line per engine
  valkeyEnabled: boolean // valkey is pre-GA — its line is rejected unless explicitly enabled
  // The stored config.database object as loaded from the service. The engines textarea only
  // round-trips versions, and the update route replaces top-level config keys wholesale — so
  // everything the form does not own (limits, per-engine default/replicas/beta, future keys)
  // must be carried here and merged back on save, or a plain Save would silently wipe it.
  storedDatabase: Record<string, any>
}

export const emptyDbaasForm: DbaasFormState = {
  name: "",
  region: "az1",
  kubeconfig: "",
  chartRepo: "ghcr.io/menlocloud/stratos-charts",
  chartVersion: "",
  argoNamespace: "argocd",
  argoProject: "stratos-dbaas",
  osServiceId: "",
  osProjectId: "",
  memberSubnetId: "",
  dnsZone: "",
  certIssuer: "",
  storageClasses: "",
  engines: "",
  valkeyEnabled: false,
  storedDatabase: {},
}

export const DBAAS_ENGINES = ["postgresql", "mysql", "mariadb", "valkey", "ferretdb", "opensearch", "kafka"] as const

const splitCsv = (s: string) => s.split(",").map((v) => v.trim()).filter(Boolean)

// parseEngines turns the engines textarea into the config.database.engines map. Line grammar:
// `postgresql=16,17,18` with an optional replica-choice suffix `@1,3` (allowed counts 1..3 —
// the platform cap; omitted = the server default of 1 or 3). null = malformed (bad line shape,
// unknown engine, duplicate line, empty version list, replicas outside 1..3, or a valkey line
// while the beta switch is off).
export function parseEngines(raw: string, valkeyEnabled: boolean): Record<string, { versions: string[]; replicas?: number[] }> | null {
  const out: Record<string, { versions: string[]; replicas?: number[] }> = {}
  for (const line of raw.split("\n")) {
    const t = line.trim()
    if (!t) continue
    const eq = t.indexOf("=")
    if (eq <= 0 || eq === t.length - 1) return null
    const engine = t.slice(0, eq).trim()
    if (!(DBAAS_ENGINES as readonly string[]).includes(engine)) return null
    if (engine === "valkey" && !valkeyEnabled) return null
    if (out[engine]) return null
    const rest = t.slice(eq + 1)
    const at = rest.indexOf("@")
    const versions = splitCsv(at >= 0 ? rest.slice(0, at) : rest)
    if (versions.length === 0) return null
    let replicas: number[] | undefined
    if (at >= 0) {
      const parts = splitCsv(rest.slice(at + 1))
      replicas = parts.map(Number)
      if (replicas.length === 0 || replicas.some((n) => !Number.isInteger(n) || n < 1 || n > 3)) return null
      if (new Set(replicas).size !== replicas.length) return null
    }
    out[engine] = replicas ? { versions, replicas } : { versions }
  }
  return out
}

export const dbaasFormValid = (f: DbaasFormState, requireKubeconfig = true) => {
  const engines = parseEngines(f.engines, f.valkeyEnabled)
  const required = [f.name, f.region, f.chartRepo, f.chartVersion, f.osServiceId, f.osProjectId, f.memberSubnetId]
  if (requireKubeconfig) required.push(f.kubeconfig)
  return required.every((v) => v.trim() !== "") && engines !== null && Object.keys(engines).length > 0
}

// The database.* keys the form edits directly — everything else stored under config.database
// (limits, future keys) passes through dbaasConfigBlocks untouched.
const FORM_OWNED_DATABASE_KEYS = ["osServiceId", "osProjectId", "memberSubnetId", "dnsZone", "certIssuer", "storageClasses", "engines"]

// dbaasConfigBlocks are the two config sub-objects stratos reads. The admin update route
// replaces top-level config keys wholesale, so each block must always be sent complete —
// deep-merged over the stored config.database, because the textarea grammar only carries
// versions: per engine the parsed versions replace the stored ones, `default` is kept if
// still offered (else the first parsed version), and `replicas`/`beta`/any other stored
// per-engine keys survive. Engines removed from the textarea are removed.
export function dbaasConfigBlocks(f: DbaasFormState) {
  const stored = f.storedDatabase ?? {}
  const storedEngines = (stored.engines as Record<string, Record<string, any>>) ?? {}
  const engines: Record<string, any> = {}
  for (const [name, { versions, replicas }] of Object.entries(parseEngines(f.engines, f.valkeyEnabled) ?? {})) {
    const prev = storedEngines[name] ?? {}
    engines[name] = {
      ...prev,
      versions,
      // An explicit @suffix replaces the stored replica choices; without one they survive.
      ...(replicas ? { replicas } : {}),
      default: prev.default && versions.includes(prev.default) ? prev.default : versions[0],
      // valkey only parses with the beta switch on; the server refuses a beta engine without
      // the customer acknowledgement, so its offer must always carry the beta flag.
      ...(name === "valkey" ? { beta: true } : {}),
    }
  }
  const passthrough = { ...stored }
  for (const k of FORM_OWNED_DATABASE_KEYS) delete passthrough[k]
  return {
    argocd: {
      namespace: f.argoNamespace.trim() || "argocd",
      project: f.argoProject.trim() || "stratos-dbaas",
      chartRepo: f.chartRepo.trim(),
      chartName: "database-cluster",
      chartVersion: f.chartVersion.trim(),
    },
    database: {
      ...passthrough,
      osServiceId: f.osServiceId.trim(),
      osProjectId: f.osProjectId.trim(),
      memberSubnetId: f.memberSubnetId.trim(),
      dnsZone: f.dnsZone.trim(),
      certIssuer: f.certIssuer.trim(),
      storageClasses: splitCsv(f.storageClasses),
      engines,
    },
  }
}

export function dbaasFormToBody(f: DbaasFormState) {
  const region = f.region.trim()
  return {
    name: f.name.trim(),
    type: "CLOUD",
    status: "PUBLIC",
    config: {
      provider: "dbaas",
      region,
      regions: { [region]: { name: region, country: "", displayName: region } },
      // database is the ONLY service a dbaas provider serves — enabled directly, no discovery.
      services: { database: { [region]: true } },
      ...dbaasConfigBlocks(f),
    },
    secret: { kubeconfig: f.kubeconfig.trim() },
  }
}

// dbaasFormFromService fills the form from a stored provider so the detail page can edit it.
// The kubeconfig is never returned by the API (secrets are stripped) — it stays blank and is
// only sent when the operator pastes a new one.
export function dbaasFormFromService(svc: {
  name?: string
  config?: Record<string, any>
}): DbaasFormState {
  const cfg = svc.config ?? {}
  const argo = (cfg.argocd as Record<string, any>) ?? {}
  const database = (cfg.database as Record<string, any>) ?? {}
  const regions = Object.keys((cfg.regions as Record<string, unknown>) ?? {})
  const engines = (database.engines as Record<string, { versions?: string[] }>) ?? {}
  const engineLines = Object.entries(engines).map(([e, o]) => `${e}=${(o?.versions ?? []).join(",")}`)
  return {
    ...emptyDbaasForm,
    name: svc.name ?? "",
    region: String(cfg.region ?? regions[0] ?? ""),
    kubeconfig: "",
    chartRepo: String(argo.chartRepo ?? emptyDbaasForm.chartRepo),
    chartVersion: String(argo.chartVersion ?? ""),
    argoNamespace: String(argo.namespace ?? "argocd"),
    argoProject: String(argo.project ?? "stratos-dbaas"),
    osServiceId: String(database.osServiceId ?? ""),
    osProjectId: String(database.osProjectId ?? ""),
    memberSubnetId: String(database.memberSubnetId ?? ""),
    dnsZone: String(database.dnsZone ?? ""),
    certIssuer: String(database.certIssuer ?? ""),
    storageClasses: ((database.storageClasses as string[]) ?? []).join(","),
    engines: engineLines.join("\n"),
    valkeyEnabled: !!engines.valkey,
    storedDatabase: database,
  }
}

type PickerProvider = { id: string; name?: string; type?: string; config?: Record<string, any> }

export function DbaasProviderForm({
  form, setForm, mode = "create",
}: {
  form: DbaasFormState
  setForm: (f: DbaasFormState) => void
  // "edit" hides the fields that cannot change after creation and makes the kubeconfig optional.
  mode?: "create" | "edit"
}) {
  const edit = mode === "edit"
  const providers = useAdminList<PickerProvider>("/admin/service")
  const osProviders = (providers.data?.data ?? []).filter(
    (p) => !["ceph-s3", "kamaji", "dbaas"].includes(String(p.config?.provider ?? "")),
  )
  return (
    <div className="grid gap-4">
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="db-name">Display name</Label>
          <Input id="db-name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Managed Databases AZ1" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="db-region">Region</Label>
          <Input id="db-region" value={form.region} onChange={(e) => setForm({ ...form, region: e.target.value })} placeholder="az1" disabled={edit} />
        </div>
      </div>
      <div className="grid gap-2">
        <Label htmlFor="db-kubeconfig">
          DB-cluster kubeconfig{edit ? " (leave blank to keep the current one)" : ""}
        </Label>
        <Textarea
          id="db-kubeconfig"
          className="min-h-28 font-mono text-xs"
          value={form.kubeconfig}
          onChange={(e) => setForm({ ...form, kubeconfig: e.target.value })}
          placeholder="apiVersion: v1&#10;kind: Config&#10;…"
          autoComplete="off"
        />
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="db-repo">Chart OCI repo</Label>
          <Input id="db-repo" className="font-mono" value={form.chartRepo} onChange={(e) => setForm({ ...form, chartRepo: e.target.value })} placeholder="ghcr.io/menlocloud/stratos-charts" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="db-chartver">Chart version (pinned)</Label>
          <Input id="db-chartver" className="font-mono" value={form.chartVersion} onChange={(e) => setForm({ ...form, chartVersion: e.target.value })} placeholder="x.y.z" />
          <p className="text-xs text-muted-foreground">
            The database-cluster chart version NEW databases pin (existing ones keep theirs).
          </p>
        </div>
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="db-argons">ArgoCD namespace</Label>
          <Input id="db-argons" value={form.argoNamespace} onChange={(e) => setForm({ ...form, argoNamespace: e.target.value })} placeholder="argocd" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="db-argoproj">ArgoCD AppProject</Label>
          <Input id="db-argoproj" value={form.argoProject} onChange={(e) => setForm({ ...form, argoProject: e.target.value })} placeholder="stratos-dbaas" />
        </div>
      </div>
      <div className="grid gap-2">
        <Label>OpenStack provider</Label>
        <Select
          value={form.osServiceId || undefined}
          onValueChange={(v) => setForm({ ...form, osServiceId: v })}
        >
          <SelectTrigger>
            <SelectValue placeholder={providers.isLoading ? "Loading providers…" : "Pick the coupled OpenStack provider"} />
          </SelectTrigger>
          <SelectContent>
            {/* A stored id whose provider vanished stays selectable — edits must not silently drop it. */}
            {form.osServiceId && !osProviders.some((p) => p.id === form.osServiceId) ? (
              <SelectItem value={form.osServiceId}>
                <span className="font-mono text-xs">{form.osServiceId}</span>
              </SelectItem>
            ) : null}
            {osProviders.map((p) => (
              <SelectItem key={p.id} value={p.id}>{p.name || p.id}</SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p className="text-xs text-muted-foreground">
          The cloud the customers' tenant networks live in. At create time stratos shares the
          tenant network with the dbaas keystone project (neutron RBAC) so the internal LB can
          land its VIP there.
        </p>
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="db-osproj">DBaaS keystone project ID</Label>
          <Input id="db-osproj" className="font-mono" value={form.osProjectId} onChange={(e) => setForm({ ...form, osProjectId: e.target.value })} autoComplete="off" />
          <p className="text-xs text-muted-foreground">
            The OpenStack project the DB-cluster nodes (and their Octavia LBs) run in.
          </p>
        </div>
        <div className="grid gap-2">
          <Label htmlFor="db-membersubnet">LB member subnet ID</Label>
          <Input id="db-membersubnet" className="font-mono" value={form.memberSubnetId} onChange={(e) => setForm({ ...form, memberSubnetId: e.target.value })} autoComplete="off" />
          <p className="text-xs text-muted-foreground">
            The dbaas-side subnet the cluster nodes sit on — the LB's member-subnet-id.
          </p>
        </div>
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="db-dnszone">DNS zone (optional)</Label>
          <Input id="db-dnszone" className="font-mono" value={form.dnsZone} onChange={(e) => setForm({ ...form, dnsZone: e.target.value })} placeholder="db.sg1.menlo.ai" autoComplete="off" />
          <p className="text-xs text-muted-foreground">
            Every database gets <code>&lt;id&gt;.&lt;zone&gt;</code> as an A record to its private
            VIP — needs external-dns on the DB cluster watching Services.
          </p>
        </div>
        <div className="grid gap-2">
          <Label htmlFor="db-certissuer">Cert issuer (optional)</Label>
          <Input id="db-certissuer" className="font-mono" value={form.certIssuer} onChange={(e) => setForm({ ...form, certIssuer: e.target.value })} placeholder="letsencrypt-dns" autoComplete="off" />
          <p className="text-xs text-muted-foreground">
            cert-manager ClusterIssuer solving DNS-01 for the zone — OpenSearch API + Dashboards
            then get real certificates instead of self-signed.
          </p>
        </div>
      </div>
      <div className="grid gap-2">
        <Label htmlFor="db-storageclasses">StorageClasses (comma-separated, optional)</Label>
        <Input id="db-storageclasses" className="font-mono" value={form.storageClasses} onChange={(e) => setForm({ ...form, storageClasses: e.target.value })} placeholder="csi-cinder, csi-cinder-highiops" />
        <p className="text-xs text-muted-foreground">
          StorageClasses offered for database volumes; empty = the cluster default only.
        </p>
      </div>
      <div className="grid gap-2">
        <Label htmlFor="db-engines">Engines (one “engine=versions” line each)</Label>
        <Textarea
          id="db-engines"
          className="min-h-20 font-mono text-xs"
          value={form.engines}
          onChange={(e) => setForm({ ...form, engines: e.target.value })}
          placeholder={"postgresql=14,15,16,17,18\nmysql=8.0,8.4\nmariadb=10.11,11.4\nferretdb=2.7\nkafka=4.3.0"}
        />
        <p className="text-xs text-muted-foreground">
          One line per engine, versions comma-separated. Known engines:{" "}
          <code>{DBAAS_ENGINES.join(", ")}</code>. Only listed engines/versions are offered to
          customers; a <code>valkey</code> line needs the beta switch below. Optional{" "}
          <code>@1,3</code> suffix restricts the replica choices (allowed 1–3; default: 1 or 3),
          e.g. <code>kafka=4.3.0@3</code>.
        </p>
      </div>
      <div className="flex items-center justify-between rounded-lg border p-3">
        <div>
          <Label htmlFor="db-valkey" className="text-sm font-medium">Offer Valkey (beta)</Label>
          <div className="text-xs text-muted-foreground">
            The valkey operator is pre-GA — customers see a Beta badge and must acknowledge it.
          </div>
        </div>
        <Switch id="db-valkey" checked={form.valkeyEnabled} onCheckedChange={(on) => setForm({ ...form, valkeyEnabled: on })} />
      </div>
      <p className="text-xs text-muted-foreground">
        The kubeconfig belongs to a stratos service account on the pre-built DB cluster (operators,
        ArgoCD and the AppProject installed there by ops). Databases are delivered as ArgoCD
        Applications of the pinned chart.
      </p>
    </div>
  )
}
