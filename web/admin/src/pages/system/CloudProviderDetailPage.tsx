import { useState } from "react"
import { useNavigate, useParams } from "react-router-dom"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Check, Copy, PlugZap, RefreshCw, Trash2 } from "lucide-react"
import { toast } from "sonner"
import { PageHeader } from "@/components/layout/PageHeader"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { Checkbox } from "@/components/ui/checkbox"
import { Textarea } from "@/components/ui/textarea"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { apiFetch, ApiError } from "@/lib/api"
import { config } from "@/lib/config"
import { useAdminGet } from "@/lib/hooks"
import type { CloudProvider } from "./CloudProvidersPage"

// ── helpers ─────────────────────────────────────────────────────────────────
type Obj = Record<string, any>
const asObj = (v: unknown): Obj => (v && typeof v === "object" && !Array.isArray(v) ? (v as Obj) : {})
const asArr = (v: unknown): any[] => (Array.isArray(v) ? v : [])
const str = (v: unknown) => (typeof v === "string" ? v : "")
const errMsg = (e: unknown) => (e instanceof Error ? e.message : String(e))

// The 8 named feature components (old-admin Features tab).
const FEATURE_COMPONENTS = [
  "volume-backup",
  "volume-snapshot",
  "image-download",
  "instance-metrics",
  "volume-backed-reinstallation",
  "driver-handles-share-servers",
  "port-security-disable-public",
  "port-security-disable-private",
]
const CONSOLE_TYPES = ["NOVNC", "SPICE_HTML5", "SPICE", "WEBMKS", "SERIAL"]
// Common OpenStack quota keys the Manage-Quota tab exposes (unknown stored keys are preserved on save).
const QUOTA_KEYS = [
  "instances",
  "cores",
  "ram",
  "volumes",
  "gigabytes",
  "snapshots",
  "networks",
  "subnets",
  "ports",
  "routers",
  "floatingips",
  "security_groups",
  "security_group_rules",
  "key_pairs",
]

// useEsSave — the shared PUT/DELETE mutation for every provider tab. On success it invalidates the
// provider doc (so all tabs re-read the merged config) plus any per-tab live query for this provider.
function useEsSave(id: string, onDone?: () => void) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (v: { path: string; body?: unknown; method?: string }) =>
      apiFetch(v.path, { method: v.method ?? "PUT", body: v.body }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin-get", `/admin/service/${id}`] })
      qc.invalidateQueries({ queryKey: ["es-live", id] })
      toast.success("Saved")
      onDone?.()
    },
    onError: (e) => toast.error(errMsg(e)),
  })
}

function Note({ children }: { children: React.ReactNode }) {
  return <div className="rounded-lg border bg-muted/40 p-4 text-sm text-muted-foreground">{children}</div>
}
function Row({ label, value }: { label: string; value?: string }) {
  return (
    <div className="flex items-center justify-between gap-4 border-b py-2 text-sm last:border-b-0">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-mono text-xs">{value || "—"}</span>
    </div>
  )
}

// CopyField — a read-only value + a copy button. The clipboard write can reject (insecure
// context / denied permission), so a rejected copy toasts an error instead of silently doing nothing.
function CopyField({ label, value }: { label?: string; value: string }) {
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      toast.error("Copy failed — select and copy manually")
    }
  }
  return (
    <div className="grid gap-1">
      {label ? <Label className="text-xs text-muted-foreground">{label}</Label> : null}
      <div className="flex items-center gap-2">
        <Input readOnly value={value} className="font-mono text-xs" onFocus={(e) => e.currentTarget.select()} />
        <Button type="button" variant="outline" size="icon" onClick={copy} aria-label="Copy">
          {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
        </Button>
      </div>
    </div>
  )
}

// NotifierUriSection — the read-only OpenStack "Notifier URI" per region, ready to paste into the
// cloud's ceilometer event publisher. URL shape = {apiUrl}/notifications/{serviceId}/{region}
// (config.apiUrl already ends in /api/v1). No configured regions → show the {region} template.
function NotifierUriSection({ id, provider }: TabProps) {
  const regions = Object.keys(asObj(provider.config?.regions)).sort()
  const shown = regions.length ? regions : ["{region}"]
  const [secret, setSecret] = useState("")
  const save = useEsSave(id, () => setSecret(""))
  return (
    <div className="mt-6 space-y-3 border-t pt-4">
      <div>
        <div className="text-sm font-medium">OpenStack Notifier URI</div>
        <p className="text-xs text-muted-foreground">
          Point the cloud's ceilometer event publisher at these URLs to push resource lifecycle events for live
          dashboard updates. Optional — the periodic sync reconciles the cache without it. When a notification secret
          is set below, the webhook is rejected unless the caller sends it as the{" "}
          <span className="font-mono">X-Stratos-Notification-Secret</span> header.
        </p>
      </div>
      {shown.map((region) => (
        <CopyField
          key={region}
          label={regions.length ? region : undefined}
          value={`${config.apiUrl}/notifications/${id}/${region}`}
        />
      ))}
      <div className="grid gap-1 pt-1">
        <Label className="text-xs text-muted-foreground">Notification secret (leave blank to keep current)</Label>
        <div className="flex items-center gap-2">
          <Input
            type="password"
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
            autoComplete="off"
            placeholder="Shared secret ceilometer must present"
            className="font-mono text-xs"
          />
          <Button
            type="button"
            variant="outline"
            disabled={!secret.trim() || save.isPending}
            onClick={() => save.mutate({ path: `/admin/service/${id}/update`, body: { secret: { notificationSecret: secret.trim() } } })}
          >
            {save.isPending ? "Saving…" : "Save secret"}
          </Button>
        </div>
      </div>
    </div>
  )
}

type TabProps = { id: string; provider: CloudProvider }

// ── 1. Connection (identity + read-only auth + test) ─────────────────────────
type OpenstackAuthResponse = {
  services?: unknown[]
  projects?: unknown[]
  domains?: unknown[]
  roles?: string[]
  selectedProjectName?: string
}
function ConnectionTab({ id, provider }: TabProps) {
  const qc = useQueryClient()
  const auth = asObj(provider.config?.auth)
  const [result, setResult] = useState<OpenstackAuthResponse | null>(null)
  const test = useMutation({
    // ?externalServiceId + empty body → keystoneAuth uses the stored creds.
    mutationFn: () =>
      apiFetch<OpenstackAuthResponse>(`/admin/service/openstack/auth?externalServiceId=${encodeURIComponent(id)}`, {
        method: "POST",
        body: {},
      }),
    onSuccess: (r) => {
      setResult(r)
      toast.success(`Connection OK — ${r.services?.length ?? 0} services, ${r.projects?.length ?? 0} projects`)
    },
    onError: (e) => toast.error(errMsg(e)),
  })
  // Discover regions + services from the live keystone catalog and persist them onto the provider —
  // this is what makes a UI-created provider usable (client menu + Location dropdown, Services tab).
  const sync = useMutation({
    mutationFn: () => apiFetch(`/admin/service/${id}/discover`, { method: "POST" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin-get", `/admin/service/${id}`] })
      toast.success("Synced services & regions from the cloud")
    },
    onError: (e) => toast.error(errMsg(e)),
  })
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Keystone connection</CardTitle>
      </CardHeader>
      <CardContent>
        <Row label="Identity URL" value={provider.config?.identityUrl} />
        <Row label="Auth type" value={str(auth.adminAuthType)} />
        <Row label="Admin username" value={str(auth.adminUsername)} />
        <Row label="Admin project" value={str(auth.adminProjectName) || str(auth.adminProjectId)} />
        <Row label="Admin domain" value={str(auth.adminDomainName) || str(auth.adminDomainId)} />
        <div className="mt-4 flex items-center gap-3">
          <Button onClick={() => test.mutate()} disabled={test.isPending}>
            <PlugZap className="size-4" />
            {test.isPending ? "Testing…" : "Test connection"}
          </Button>
          <Button variant="outline" onClick={() => sync.mutate()} disabled={sync.isPending}>
            <RefreshCw className="size-4" />
            {sync.isPending ? "Syncing…" : "Sync services & regions"}
          </Button>
          {result ? (
            <span className="text-sm text-muted-foreground">
              {result.services?.length ?? 0} services · {result.projects?.length ?? 0} projects
              {result.selectedProjectName ? ` · scoped to ${result.selectedProjectName}` : ""}
            </span>
          ) : null}
        </div>
        <NotifierUriSection id={id} provider={provider} />
      </CardContent>
    </Card>
  )
}

// ── 2. Services (per-region service toggle grid; Save sends the FULL map) ─────
type ServicesMap = Record<string, Record<string, boolean>>
function ServicesTab({ id, provider }: TabProps) {
  const stored: ServicesMap = (provider.config?.services as ServicesMap) ?? {}
  const [edits, setEdits] = useState<ServicesMap | null>(null)
  const services = edits ?? stored
  const save = useEsSave(id, () => setEdits(null))

  const regionSet = new Set<string>(Object.keys(provider.config?.regions ?? {}))
  for (const perRegion of Object.values(services)) for (const r of Object.keys(perRegion ?? {})) regionSet.add(r)
  const regions = [...regionSet].sort()

  const toggle = (svc: string, region: string, on: boolean) => {
    const next: ServicesMap = Object.fromEntries(Object.entries(services).map(([k, v]) => [k, { ...(v ?? {}) }]))
    next[svc] = { ...(next[svc] ?? {}), [region]: on }
    setEdits(next)
  }
  return (
    <div className="space-y-4">
      {Object.keys(services).length === 0 ? (
        <Note>No services configured on this provider.</Note>
      ) : (
        <Card className="overflow-hidden py-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Service</TableHead>
                {regions.map((r) => (
                  <TableHead key={r}>{r}</TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {Object.keys(services)
                .sort()
                .map((svc) => (
                  <TableRow key={svc}>
                    <TableCell className="font-mono text-xs">{svc}</TableCell>
                    {regions.map((r) => (
                      <TableCell key={r}>
                        <Switch checked={services[svc]?.[r] === true} onCheckedChange={(on) => toggle(svc, r, on)} />
                      </TableCell>
                    ))}
                  </TableRow>
                ))}
            </TableBody>
          </Table>
        </Card>
      )}
      <div className="flex items-center gap-3">
        <Button
          // PUT /service/{id} merges config.services wholesale → must send the FULL map.
          onClick={() => edits && save.mutate({ path: `/admin/service/${id}?provisioning=false`, body: { config: { services: edits } } })}
          disabled={!edits || save.isPending}
        >
          {save.isPending ? "Saving…" : "Save services"}
        </Button>
        {edits ? <span className="text-sm text-muted-foreground">Unsaved changes</span> : null}
      </div>
    </div>
  )
}

// ── 3. Configuration (name + status; /configuration only reads these) ─────────
function ConfigurationTab({ id, provider }: TabProps) {
  const [name, setName] = useState(provider.name ?? "")
  const [status, setStatus] = useState(provider.status ?? "PUBLIC")
  const save = useEsSave(id)
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">General configuration</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid max-w-md gap-2">
          <Label htmlFor="es-name">Display name</Label>
          <Input id="es-name" value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <div className="grid max-w-md gap-2">
          <Label>Status</Label>
          <Select value={status} onValueChange={setStatus}>
            <SelectTrigger className="w-56">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {["PUBLIC", "PRIVATE", "DISABLED"].map((s) => (
                <SelectItem key={s} value={s}>
                  {s}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <Button
          // /configuration sets name, status and config.openstackReseller — pass the current reseller so we never null it.
          onClick={() =>
            save.mutate({
              path: `/admin/service/${id}/configuration`,
              body: { name, status, config: { openstackReseller: asObj((provider.config as Obj)?.openstackReseller) } },
            })
          }
          disabled={save.isPending}
        >
          {save.isPending ? "Saving…" : "Save configuration"}
        </Button>
      </CardContent>
    </Card>
  )
}

// ── 4. Provisioning (config.provisioning.projectRoles) ───────────────────────
function ProvisioningTab({ id, provider }: TabProps) {
  const prov = asObj((provider.config as Obj)?.provisioning)
  const [roles, setRoles] = useState(asArr(prov.projectRoles).map(String).join("\n"))
  const save = useEsSave(id)
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Provisioning roles</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <Note>Roles granted to a project on provisioning. One role name per line.</Note>
        <Textarea
          rows={6}
          value={roles}
          onChange={(e) => setRoles(e.target.value)}
          placeholder="member&#10;_member_"
          className="font-mono text-sm"
        />
        <Button
          // PUT /service/{id}/update merges config; keep the rest of provisioning (e.g. quota) intact.
          onClick={() =>
            save.mutate({
              path: `/admin/service/${id}/update`,
              body: { config: { provisioning: { ...prov, projectRoles: roles.split("\n").map((r) => r.trim()).filter(Boolean) } } },
            })
          }
          disabled={save.isPending}
        >
          {save.isPending ? "Saving…" : "Save provisioning"}
        </Button>
      </CardContent>
    </Card>
  )
}

// ── 5. Features (8 component toggles + console type) ──────────────────────────
function FeaturesTab({ id, provider }: TabProps) {
  const features = asObj((provider.config as Obj)?.features)
  const compMap: Record<string, boolean> = {}
  for (const c of asArr(features.components)) if (asObj(c).name) compMap[str(asObj(c).name)] = asObj(c).enabled === true
  const [comps, setComps] = useState<Record<string, boolean>>(() => {
    const seed: Record<string, boolean> = {}
    for (const n of FEATURE_COMPONENTS) seed[n] = compMap[n] === true
    return seed
  })
  const storedConsole = asArr(features.enabledConsoleTypes).map(String)[0] || asArr((provider.config as Obj)?.enabledConsoleTypes).map(String)[0] || "NOVNC"
  const [consoleType, setConsoleType] = useState(storedConsole)
  const save = useEsSave(id)

  const submit = () => {
    // Merge over the stored features (keep volumeTypes/publicNetworks/shareProtocols the other tabs manage),
    // then overwrite the known component set (preserving any unknown stored components).
    const merged: Record<string, boolean> = { ...compMap }
    for (const n of FEATURE_COMPONENTS) merged[n] = comps[n]
    const components = Object.entries(merged).map(([name, enabled]) => ({ name, enabled }))
    save.mutate({ path: `/admin/service/${id}/features`, body: { ...features, components, enabledConsoleTypes: [consoleType] } })
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Feature components</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="divide-y">
          {FEATURE_COMPONENTS.map((n) => (
            <div key={n} className="flex items-center justify-between py-2">
              <span className="font-mono text-xs">{n}</span>
              <Switch checked={comps[n]} onCheckedChange={(on) => setComps((c) => ({ ...c, [n]: on }))} />
            </div>
          ))}
        </div>
        <div className="grid max-w-md gap-2">
          <Label>Console type</Label>
          <Select value={consoleType} onValueChange={setConsoleType}>
            <SelectTrigger className="w-56">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {CONSOLE_TYPES.map((c) => (
                <SelectItem key={c} value={c}>
                  {c}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <Button onClick={submit} disabled={save.isPending}>
          {save.isPending ? "Saving…" : "Save features"}
        </Button>
      </CardContent>
    </Card>
  )
}

// ── 6. Quota (config.provisioning.quota) ─────────────────────────────────────
function QuotaTab({ id, provider }: TabProps) {
  const quota = asObj(asObj((provider.config as Obj)?.provisioning).quota)
  const [vals, setVals] = useState<Record<string, string>>(() => {
    const seed: Record<string, string> = {}
    for (const k of QUOTA_KEYS) seed[k] = quota[k] != null ? String(quota[k]) : ""
    return seed
  })
  const save = useEsSave(id)
  const submit = () => {
    // /quota replaces config.provisioning.quota wholesale → merge over the stored quota (keeps placementQuotas etc).
    const next: Obj = { ...quota }
    for (const k of QUOTA_KEYS) {
      const v = vals[k].trim()
      if (v !== "") {
        const parsed = Number(v)
        // NaN is dropped by JSON serialization — the key would silently keep its old value.
        if (!Number.isFinite(parsed)) {
          toast.error(`Invalid quota value for "${k}"`)
          return
        }
        next[k] = parsed
      }
    }
    save.mutate({ path: `/admin/service/${id}/quota`, body: next })
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Default quota</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-3">
          {QUOTA_KEYS.map((k) => (
            <div key={k} className="grid gap-1">
              <Label className="text-xs" htmlFor={`q-${k}`}>
                {k}
              </Label>
              <Input
                id={`q-${k}`}
                type="number"
                value={vals[k]}
                onChange={(e) => setVals((s) => ({ ...s, [k]: e.target.value }))}
              />
            </div>
          ))}
        </div>
        <Button onClick={submit} disabled={save.isPending}>
          {save.isPending ? "Saving…" : "Save quota"}
        </Button>
      </CardContent>
    </Card>
  )
}

// ── 6b. GPU (placement capacity + unpriced-flavor guard) ─────────────────────
type GpuRegionCapacity = { region: string; gpus: Array<{ name: string; total: number; inUse: number }> }
type UnpricedFlavor = { region: string; id: string; name: string; gpuModel?: string; gpuCount?: number; reason?: string }

function GpuTab({ id }: { id: string }) {
  const capQ = useAdminGet<GpuRegionCapacity[]>(`/admin/service/${id}/gpu-info`)
  const unpricedQ = useAdminGet<UnpricedFlavor[]>(`/admin/service/${id}/unpriced-flavors`)
  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle className="text-base">GPU capacity</CardTitle>
        </CardHeader>
        <CardContent>
          {capQ.isLoading ? (
            <Skeleton className="h-24" />
          ) : (capQ.data ?? []).flatMap((r) => r.gpus).length === 0 ? (
            <p className="text-sm text-muted-foreground">
              No GPU devices found in placement (expects nova PCI-in-placement resource providers
              carrying the CUSTOM_PCI_GPU trait).
            </p>
          ) : (
            <div className="space-y-6">
              {(capQ.data ?? []).map((r) => (
                <div key={r.region} className="space-y-2">
                  <p className="text-sm font-medium">{r.region}</p>
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>GPU model</TableHead>
                        <TableHead className="text-right">In use</TableHead>
                        <TableHead className="text-right">Free</TableHead>
                        <TableHead className="text-right">Total</TableHead>
                        <TableHead className="w-48">Usage</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {r.gpus.map((g) => (
                        <TableRow key={g.name}>
                          <TableCell className="font-mono text-xs">{g.name}</TableCell>
                          <TableCell className="text-right tabular-nums">{g.inUse}</TableCell>
                          <TableCell className="text-right tabular-nums">{g.total - g.inUse}</TableCell>
                          <TableCell className="text-right tabular-nums">{g.total}</TableCell>
                          <TableCell>
                            <div className="h-2 w-full rounded bg-muted">
                              <div
                                className="h-2 rounded bg-primary"
                                style={{ width: `${g.total ? Math.round((g.inUse / g.total) * 100) : 0}%` }}
                              />
                            </div>
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Unpriced flavors</CardTitle>
        </CardHeader>
        <CardContent>
          {unpricedQ.isLoading ? (
            <Skeleton className="h-16" />
          ) : (unpricedQ.data ?? []).length === 0 ? (
            <p className="text-sm text-muted-foreground">Every live flavor matches at least one enabled public price rule.</p>
          ) : (
            <div className="space-y-3">
              <p className="text-sm text-destructive">
                These flavors have a pricing gap — "no rule" bills the whole server ZERO,
                "no gpu rule" bills the GPU devices ZERO (only CPU/RAM rules match).
              </p>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Flavor</TableHead>
                    <TableHead>Region</TableHead>
                    <TableHead>GPU</TableHead>
                    <TableHead>Gap</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {(unpricedQ.data ?? []).map((f) => (
                    <TableRow key={`${f.region}-${f.id}`}>
                      <TableCell>{f.name}</TableCell>
                      <TableCell>{f.region}</TableCell>
                      <TableCell className="font-mono text-xs">
                        {f.gpuModel ? `${f.gpuModel} × ${f.gpuCount}` : "—"}
                      </TableCell>
                      <TableCell className="text-xs text-destructive">{f.reason ?? "no rule"}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

// ── 7. VHI-ostor (config.vhiOstorConfig + write-only secret auth) ─────────────
function VhiOstorTab({ id, provider }: TabProps) {
  const cfg = asObj((provider.config as Obj)?.vhiOstorConfig)
  const [text, setText] = useState(JSON.stringify(cfg, null, 2))
  const [accessKey, setAccessKey] = useState("")
  const [secretKey, setSecretKey] = useState("")
  const save = useEsSave(id)
  const submit = () => {
    let parsed: unknown
    try {
      parsed = text.trim() ? JSON.parse(text) : {}
    } catch {
      toast.error("VHI-ostor config is not valid JSON")
      return
    }
    const body: Obj = { vhiOstorConfig: parsed }
    if (accessKey || secretKey) body.vhiOstorAuth = { accessKey, secretKey }
    save.mutate({ path: `/admin/service/${id}/vhi-ostor`, body })
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">VHI object storage (Ostor)</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid gap-2">
          <Label>Ostor config (JSON)</Label>
          <Textarea rows={8} value={text} onChange={(e) => setText(e.target.value)} className="font-mono text-xs" />
        </div>
        <div className="grid gap-2 sm:grid-cols-2">
          <div className="grid gap-1">
            <Label className="text-xs">Access key (leave blank to keep)</Label>
            <Input value={accessKey} onChange={(e) => setAccessKey(e.target.value)} autoComplete="off" />
          </div>
          <div className="grid gap-1">
            <Label className="text-xs">Secret key (leave blank to keep)</Label>
            <Input type="password" value={secretKey} onChange={(e) => setSecretKey(e.target.value)} autoComplete="off" />
          </div>
        </div>
        <Button onClick={submit} disabled={save.isPending}>
          {save.isPending ? "Saving…" : "Save Ostor settings"}
        </Button>
      </CardContent>
    </Card>
  )
}

// ── 8. Advanced (gnocchi granularity + delete provider) ──────────────────────
function AdvancedTab({ id, provider }: TabProps) {
  const navigate = useNavigate()
  const [gran, setGran] = useState(String((provider.config as Obj)?.gnocchiGranularity ?? 300))
  const save = useEsSave(id)
  const [confirm, setConfirm] = useState(false)
  const del = useMutation({
    mutationFn: () => apiFetch(`/admin/service/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Cloud provider deleted")
      navigate("/system/cloud-providers")
    },
    onError: (e) => toast.error(errMsg(e)),
  })
  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Advanced settings</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid max-w-xs gap-2">
            <Label htmlFor="gran">Gnocchi granularity (seconds)</Label>
            <Input id="gran" type="number" value={gran} onChange={(e) => setGran(e.target.value)} />
          </div>
          <Button
            onClick={() => {
              const parsed = Number(gran)
              if (!Number.isInteger(parsed) || parsed <= 0) {
                toast.error("Granularity must be a positive integer")
                return
              }
              save.mutate({ path: `/admin/service/${id}/gnocchi-granularity`, body: { granularity: parsed } })
            }}
            disabled={save.isPending}
          >
            {save.isPending ? "Saving…" : "Save granularity"}
          </Button>
        </CardContent>
      </Card>
      <Card className="border-destructive/40">
        <CardHeader>
          <CardTitle className="text-base text-destructive">Danger zone</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <p className="text-sm text-muted-foreground">
            Delete this cloud provider. Fails if any project, user or cloud resource still uses it.
          </p>
          {confirm ? (
            <div className="flex items-center gap-3">
              <Button variant="destructive" onClick={() => del.mutate()} disabled={del.isPending}>
                <Trash2 className="size-4" />
                {del.isPending ? "Deleting…" : "Confirm delete"}
              </Button>
              <Button variant="outline" onClick={() => setConfirm(false)}>
                Cancel
              </Button>
            </div>
          ) : (
            <Button variant="destructive" onClick={() => setConfirm(true)}>
              <Trash2 className="size-4" />
              Delete cloud provider
            </Button>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

// ── 9. Volume types (live GET + per-type enable/display-name per region) ──────
type VolumeTypesResp = { region: string; volumeTypes: string[] }[]
type VtState = Record<string, Record<string, { enabled: boolean; displayName: string }>>
function VolumeTypesTab({ id, provider }: TabProps) {
  const saved = asObj(asObj((provider.config as Obj)?.features).volumeTypes) // { region: [{name,displayName,enabled}] }
  const live = useQuery({
    queryKey: ["es-live", id, "volume-types"],
    queryFn: () => apiFetch<VolumeTypesResp>(`/admin/service/${id}/volume/types`),
  })
  const [state, setState] = useState<VtState>({})
  const save = useEsSave(id)

  const rows = live.data ?? []
  const cell = (region: string, name: string) => {
    const stored = asArr(saved[region]).find((t) => asObj(t).name === name)
    const cur = state[region]?.[name]
    return {
      enabled: cur ? cur.enabled : asObj(stored).enabled === true,
      displayName: cur ? cur.displayName : str(asObj(stored).displayName),
    }
  }
  const set = (region: string, name: string, patch: Partial<{ enabled: boolean; displayName: string }>) =>
    setState((s) => ({ ...s, [region]: { ...(s[region] ?? {}), [name]: { ...cell(region, name), ...patch } } }))

  const submit = () => {
    // ponytail: OpenStackVolumeType is sent as {name, displayName, enabled} keyed by region; adjust if the
    // client model carries more fields. Only live (available) types are written.
    const body: Record<string, any[]> = {}
    for (const { region, volumeTypes } of rows) body[region] = volumeTypes.map((name) => ({ name, ...cell(region, name) }))
    save.mutate({ path: `/admin/service/${id}/volume/types`, body })
  }

  if (live.isLoading) return <Skeleton className="h-40" />
  if (live.error) return <Note>{errMsg(live.error)}</Note>
  if (rows.length === 0) return <Note>No volume types reported by the cloud.</Note>
  return (
    <div className="space-y-4">
      {rows.map(({ region, volumeTypes }) => (
        <Card key={region} className="overflow-hidden py-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{region} — type</TableHead>
                <TableHead>Display name</TableHead>
                <TableHead className="w-24">Enabled</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {volumeTypes.map((name) => {
                const c = cell(region, name)
                return (
                  <TableRow key={name}>
                    <TableCell className="font-mono text-xs">{name}</TableCell>
                    <TableCell>
                      <Input value={c.displayName} onChange={(e) => set(region, name, { displayName: e.target.value })} />
                    </TableCell>
                    <TableCell>
                      <Switch checked={c.enabled} onCheckedChange={(on) => set(region, name, { enabled: on })} />
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </Card>
      ))}
      <Button onClick={submit} disabled={save.isPending}>
        {save.isPending ? "Saving…" : "Save volume types"}
      </Button>
    </div>
  )
}

// ── 10. Placement (VHI placement-quotas; GET is an empty seam → doc/empty) ────
function PlacementTab({ id, provider }: TabProps) {
  const stored = asObj(asObj((provider.config as Obj)?.provisioning).quota).placementQuotas
  const [text, setText] = useState(JSON.stringify(stored ?? [], null, 2))
  const save = useEsSave(id)
  const submit = () => {
    let parsed: unknown
    try {
      parsed = text.trim() ? JSON.parse(text) : []
    } catch {
      toast.error("Placement quotas is not valid JSON")
      return
    }
    save.mutate({ path: `/admin/service/${id}/vhi/placement-quotas`, body: parsed })
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">VHI placement quotas</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        {/* ponytail: the live GET is an empty stub (VHI-only) → edit the stored value as raw JSON. */}
        <Note>VHI-specific placement quotas. Edit the stored value as JSON.</Note>
        <Textarea rows={8} value={text} onChange={(e) => setText(e.target.value)} className="font-mono text-xs" />
        <Button onClick={submit} disabled={save.isPending}>
          {save.isPending ? "Saving…" : "Save placement quotas"}
        </Button>
      </CardContent>
    </Card>
  )
}

// ── 11. Reseller (config.openstackReseller) ──────────────────────────────────
function ResellerTab({ id, provider }: TabProps) {
  const r = asObj((provider.config as Obj)?.openstackReseller)
  const [enabled, setEnabled] = useState(r.enabled === true)
  const [org, setOrg] = useState(str(r.organizationId))
  const [domain, setDomain] = useState(str(r.domain))
  const save = useEsSave(id)
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Reseller</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex items-center justify-between border-b py-2">
          <span className="text-sm">Enabled</span>
          <Switch checked={enabled} onCheckedChange={setEnabled} />
        </div>
        <div className="grid max-w-md gap-2">
          <Label>Organization ID</Label>
          <Input value={org} onChange={(e) => setOrg(e.target.value)} />
        </div>
        <div className="grid max-w-md gap-2">
          <Label>Domain</Label>
          <Input value={domain} onChange={(e) => setDomain(e.target.value)} />
        </div>
        <Button
          onClick={() => save.mutate({ path: `/admin/service/${id}/reseller`, body: { ...r, enabled, organizationId: org, domain } })}
          disabled={save.isPending}
        >
          {save.isPending ? "Saving…" : "Save reseller"}
        </Button>
      </CardContent>
    </Card>
  )
}

// ── 12. Availability zones (live GET + per-zone enable/display-name) ──────────
type AzResp = Record<string, Record<string, string[]>> // serviceType → region → [zones]
function AvailabilityZonesTab({ id, provider }: TabProps) {
  // stored config.availabilityZones: accept either a list [{name,displayName,enabled}] or a name-keyed map.
  const savedRaw = (provider.config as Obj)?.availabilityZones
  const savedMap: Record<string, { displayName: string; enabled: boolean }> = {}
  for (const e of asArr(savedRaw)) if (asObj(e).name) savedMap[str(asObj(e).name)] = { displayName: str(asObj(e).displayName), enabled: asObj(e).enabled === true }
  if (!Array.isArray(savedRaw)) for (const [name, v] of Object.entries(asObj(savedRaw))) savedMap[name] = { displayName: str(asObj(v).displayName), enabled: asObj(v).enabled === true }

  const live = useQuery({
    queryKey: ["es-live", id, "availability-zones"],
    queryFn: () => apiFetch<AzResp>(`/admin/service/${id}/availability-zones`),
  })
  const [state, setState] = useState<Record<string, { enabled: boolean; displayName: string }>>({})
  const save = useEsSave(id)

  const names = new Set<string>(Object.keys(savedMap))
  for (const byRegion of Object.values(live.data ?? {})) for (const zones of Object.values(byRegion)) for (const z of zones) names.add(z)
  const zoneNames = [...names].sort()

  const cell = (name: string) => state[name] ?? savedMap[name] ?? { enabled: false, displayName: "" }
  const set = (name: string, patch: Partial<{ enabled: boolean; displayName: string }>) =>
    setState((s) => ({ ...s, [name]: { ...cell(name), ...patch } }))

  const submit = () => {
    // ponytail: sent as a list [{name, displayName, enabled}]; switch to a name-keyed map if the client reads one.
    const body = zoneNames.map((name) => ({ name, ...cell(name) }))
    save.mutate({ path: `/admin/service/${id}/availability-zones`, body })
  }

  if (live.isLoading) return <Skeleton className="h-40" />
  if (zoneNames.length === 0) return <Note>{live.error ? errMsg(live.error) : "No availability zones reported by the cloud."}</Note>
  return (
    <div className="space-y-4">
      <Card className="overflow-hidden py-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Zone</TableHead>
              <TableHead>Display name</TableHead>
              <TableHead className="w-24">Enabled</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {zoneNames.map((name) => {
              const c = cell(name)
              return (
                <TableRow key={name}>
                  <TableCell className="font-mono text-xs">{name}</TableCell>
                  <TableCell>
                    <Input value={c.displayName} onChange={(e) => set(name, { displayName: e.target.value })} />
                  </TableCell>
                  <TableCell>
                    <Switch checked={c.enabled} onCheckedChange={(on) => set(name, { enabled: on })} />
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      </Card>
      <Button onClick={submit} disabled={save.isPending}>
        {save.isPending ? "Saving…" : "Save availability zones"}
      </Button>
    </div>
  )
}

// ── 13. Project imports (live keystone project list + bulk-import) ────────────
type ImportProject = { project: { id: string; name: string }; stratosProjectId?: string; users?: unknown[] }
function ProjectImportsTab({ id }: { id: string }) {
  const qc = useQueryClient()
  const q = useQuery({
    queryKey: ["es-live", id, "project-import"],
    queryFn: () => apiFetch<ImportProject[]>(`/admin/project-import/${id}`),
    retry: false,
  })
  const [sel, setSel] = useState<Record<string, boolean>>({})
  const seam = q.error instanceof ApiError && q.error.code === 501
  const rows = q.data ?? []
  const importable = rows.filter((r) => !r.stratosProjectId)
  const chosen = importable.filter((r) => sel[r.project.id])

  const doImport = useMutation({
    mutationFn: () =>
      apiFetch(`/admin/project-import/bulk-import/${id}`, {
        method: "POST",
        // stratosProjectId blank → the server inserts a new linked project for each.
        body: chosen.map((r) => ({ stratosProjectId: "", project: r.project, users: [] })),
      }),
    onSuccess: () => {
      setSel({})
      qc.invalidateQueries({ queryKey: ["es-live", id, "project-import"] })
      toast.success(`Imported ${chosen.length} project(s)`)
    },
    onError: (e) => toast.error(errMsg(e)),
  })

  return (
    <Card className="overflow-hidden">
      <CardHeader>
        <CardTitle className="text-base">Project imports</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        {q.isLoading ? (
          <Skeleton className="h-24" />
        ) : seam ? (
          <Note>
            Listing importable OpenStack projects requires a live Keystone connection, which is not available on this
            deployment.
          </Note>
        ) : q.error ? (
          <Note>{errMsg(q.error)}</Note>
        ) : rows.length === 0 ? (
          <Note>No OpenStack projects were found on this provider.</Note>
        ) : (
          <>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-10">
                    <Checkbox
                      checked={chosen.length > 0 && chosen.length === importable.length}
                      onCheckedChange={(v) =>
                        setSel(v ? Object.fromEntries(importable.map((r) => [r.project.id, true])) : {})
                      }
                      aria-label="Select all importable"
                    />
                  </TableHead>
                  <TableHead>Project</TableHead>
                  <TableHead>OpenStack ID</TableHead>
                  <TableHead className="w-28">Status</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((r) => {
                  const linked = !!r.stratosProjectId
                  return (
                    <TableRow key={r.project.id}>
                      <TableCell>
                        <Checkbox
                          checked={!!sel[r.project.id]}
                          disabled={linked}
                          onCheckedChange={(v) => setSel((s) => ({ ...s, [r.project.id]: !!v }))}
                          aria-label={`Select ${r.project.name}`}
                        />
                      </TableCell>
                      <TableCell className="font-medium">{r.project.name}</TableCell>
                      <TableCell className="font-mono text-xs text-muted-foreground">{r.project.id}</TableCell>
                      <TableCell>
                        <StatusBadge status={linked ? "IMPORTED" : "IMPORTABLE"} />
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
            <Button onClick={() => doImport.mutate()} disabled={chosen.length === 0 || doImport.isPending}>
              {doImport.isPending ? "Importing…" : `Import ${chosen.length || ""} selected`}
            </Button>
          </>
        )}
      </CardContent>
    </Card>
  )
}

// ── 14. Share protocols (live GET defaults + enable toggle) ───────────────────
type ShareProtocol = { name: string; displayName: string; enabled: boolean }
function ShareProtocolsTab({ id }: { id: string }) {
  const live = useQuery({
    queryKey: ["es-live", id, "share-protocols"],
    queryFn: () => apiFetch<ShareProtocol[]>(`/admin/service/${id}/share/protocols`),
  })
  const [state, setState] = useState<Record<string, boolean>>({})
  const save = useEsSave(id)
  const rows = live.data ?? []
  const enabledOf = (p: ShareProtocol) => (p.name in state ? state[p.name] : p.enabled)
  const submit = () =>
    save.mutate({
      path: `/admin/service/${id}/share/protocols`,
      body: rows.map((p) => ({ name: p.name, displayName: p.displayName, enabled: enabledOf(p) })),
    })

  if (live.isLoading) return <Skeleton className="h-40" />
  if (live.error) return <Note>{errMsg(live.error)}</Note>
  if (rows.length === 0) return <Note>No share protocols available.</Note>
  return (
    <div className="space-y-4">
      <Card className="overflow-hidden py-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Protocol</TableHead>
              <TableHead className="w-24">Enabled</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((p) => (
              <TableRow key={p.name}>
                <TableCell>
                  <span className="font-medium">{p.displayName}</span>{" "}
                  <span className="font-mono text-xs text-muted-foreground">({p.name})</span>
                </TableCell>
                <TableCell>
                  <Switch checked={enabledOf(p)} onCheckedChange={(on) => setState((s) => ({ ...s, [p.name]: on }))} />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>
      <Button onClick={submit} disabled={save.isPending}>
        {save.isPending ? "Saving…" : "Save share protocols"}
      </Button>
    </div>
  )
}

// ── page ─────────────────────────────────────────────────────────────────────
const TAB_DEFS = [
  { v: "connection", label: "Connection" },
  { v: "services", label: "Services" },
  { v: "configuration", label: "Configuration" },
  { v: "provisioning", label: "Provisioning" },
  { v: "features", label: "Features" },
  { v: "quota", label: "Quota" },
  { v: "gpu", label: "GPU" },
  { v: "vhi-ostor", label: "VHI-ostor" },
  { v: "advanced", label: "Advanced" },
  { v: "volume-types", label: "Volume types" },
  { v: "placement", label: "Placement" },
  { v: "reseller", label: "Reseller" },
  { v: "availability-zones", label: "Availability zones" },
  { v: "project-imports", label: "Project imports" },
  { v: "share-protocols", label: "Share protocols" },
]

export default function CloudProviderDetailPage() {
  const { id = "" } = useParams()
  const { data, isLoading, error } = useAdminGet<CloudProvider>(`/admin/service/${id}`)

  if (isLoading) {
    return (
      <>
        <PageHeader title="Cloud provider" />
        <Skeleton className="h-64" />
      </>
    )
  }
  if (error || !data) {
    return (
      <>
        <PageHeader title="Cloud provider" />
        <Note>{(error as Error | undefined)?.message ?? "Failed to load cloud provider."}</Note>
      </>
    )
  }
  const p = data

  return (
    <>
      <PageHeader
        title={p.name ?? id}
        description={`${p.type ?? "CLOUD"} provider ${id}`}
        actions={<StatusBadge status={p.status} />}
      />
      <Tabs defaultValue="connection">
        <div className="overflow-x-auto">
          <TabsList>
            {TAB_DEFS.map((t) => (
              <TabsTrigger key={t.v} value={t.v}>
                {t.label}
              </TabsTrigger>
            ))}
          </TabsList>
        </div>

        <TabsContent value="connection" className="mt-4">
          <ConnectionTab id={id} provider={p} />
        </TabsContent>
        <TabsContent value="services" className="mt-4">
          <ServicesTab id={id} provider={p} />
        </TabsContent>
        <TabsContent value="configuration" className="mt-4">
          <ConfigurationTab id={id} provider={p} />
        </TabsContent>
        <TabsContent value="provisioning" className="mt-4">
          <ProvisioningTab id={id} provider={p} />
        </TabsContent>
        <TabsContent value="features" className="mt-4">
          <FeaturesTab id={id} provider={p} />
        </TabsContent>
        <TabsContent value="quota" className="mt-4">
          <QuotaTab id={id} provider={p} />
        </TabsContent>
        <TabsContent value="gpu" className="mt-4">
          <GpuTab id={id} />
        </TabsContent>
        <TabsContent value="vhi-ostor" className="mt-4">
          <VhiOstorTab id={id} provider={p} />
        </TabsContent>
        <TabsContent value="advanced" className="mt-4">
          <AdvancedTab id={id} provider={p} />
        </TabsContent>
        <TabsContent value="volume-types" className="mt-4">
          <VolumeTypesTab id={id} provider={p} />
        </TabsContent>
        <TabsContent value="placement" className="mt-4">
          <PlacementTab id={id} provider={p} />
        </TabsContent>
        <TabsContent value="reseller" className="mt-4">
          <ResellerTab id={id} provider={p} />
        </TabsContent>
        <TabsContent value="availability-zones" className="mt-4">
          <AvailabilityZonesTab id={id} provider={p} />
        </TabsContent>
        <TabsContent value="project-imports" className="mt-4">
          <ProjectImportsTab id={id} />
        </TabsContent>
        <TabsContent value="share-protocols" className="mt-4">
          <ShareProtocolsTab id={id} />
        </TabsContent>
      </Tabs>
    </>
  )
}
