// Managed Kubernetes (kamaji provider) — list/create/manage clusters. The cloud scope for
// cluster CRUD is a KAMAJI location (the create wizard offers a picker when the project has
// more than one); the flavor picker for node groups reads the project's OPENSTACK service
// (worker VMs run in the customer's own tenant) and mirrors CreateServerPage's flavor gating:
// quota + region GPU capacity + no ephemeral/swap flavors + the admin's kubernetesFlavorIds
// allowlist. Actions map to Go cloud_kamaji.go: create/delete + GET_KUBECONFIG / UPGRADE /
// SET_NODE_GROUPS / SET_OIDC.
import { useCallback, useMemo, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import type { ColumnDef } from "@tanstack/react-table"
import { toast } from "sonner"
import { Boxes, Download, MoreHorizontal, Plus, RefreshCw, Settings2, Trash2 } from "lucide-react"
import { PageHeader } from "@/components/layout/PageHeader"
import { DataTable, sortableHeader } from "@/components/data-table"
import { EmptyState } from "@/components/empty-state"
import { StatusBadge } from "@/components/status-badge"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import {
  Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle,
} from "@/components/ui/sheet"
import { Switch } from "@/components/ui/switch"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { apiFetch, type CloudScope } from "@/lib/api"
import { timeAgo } from "@/lib/format"
import {
  useLocations, useProject, useProjectGpuCapacity, useProjectId, useProjectQuota, useProjectServices,
} from "@/lib/hooks"
import { gpuCapacityViolations, serverQuotaViolations } from "@/lib/quota"
import type { CloudResource, Location } from "@/lib/types"

type Cluster = Record<string, any>
type NodeGroupRow = {
  name: string
  flavorId: string
  count: string
  autoscale: boolean
  min: string
  max: string
  labels: string // "k=v,k2=v2"
  taints: string // "key=val:NoSchedule,…"
}
// LIST_FLAVORS rows come back in the same shape CreateServerPage consumes: the OpenStack
// flavor document nested under `data`, with the nova id mirrored as `externalId`.
type Flavor = {
  externalId?: string
  data?: {
    id?: string
    name?: string
    vcpus?: number
    ram?: number
    disk?: number
    ephemeral?: number
    swap?: number
    extra_specs?: Record<string, unknown>
  }
}
// What the node-group flavor Select renders — precomputed at page level so the create wizard
// and the manage sheet share identical filtering/gating.
type FlavorOption = {
  id: string
  name: string
  spec?: string // "4 vCPU · 8 GB RAM"
  blocked?: boolean // exceeds project quota or region GPU capacity — must not be picked
}

type OidcDraft = {
  issuerUrl: string
  clientId: string
  usernameClaim: string
  usernamePrefix: string
  groupsClaim: string
  groupsPrefix: string
}
const emptyOidc: OidcDraft = {
  issuerUrl: "", clientId: "", usernameClaim: "", usernamePrefix: "", groupsClaim: "", groupsPrefix: "",
}

// Trimmed, empty-free OIDC payload — `{}` (absent issuerUrl) means "disable OIDC" server-side.
function oidcToBody(o: OidcDraft): Record<string, string> {
  return Object.fromEntries(
    Object.entries(o)
      .map(([k, v]) => [k, v.trim()])
      .filter(([, v]) => v !== ""),
  )
}

const emptyGroup: NodeGroupRow = { name: "workers", flavorId: "", count: "3", autoscale: false, min: "1", max: "5", labels: "", taints: "" }

function cluster(r: CloudResource): Cluster {
  return (r.data?.cluster as Cluster) ?? {}
}

// Stable key for a location picker — the API array order is not stable, so never key by index.
const locKeyOf = (l: Location) => `${l.serviceId ?? ""}::${l.region ?? ""}`

// Newest-first semver sort for the curated version list.
function sortVersions(vs: string[]): string[] {
  const parts = (v: string) => v.split(".").map((n) => Number(n) || 0)
  return [...vs].sort((a, b) => {
    const pa = parts(a), pb = parts(b)
    for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
      if ((pb[i] ?? 0) !== (pa[i] ?? 0)) return (pb[i] ?? 0) - (pa[i] ?? 0)
    }
    return 0
  })
}

function parseVersion(v: string): [number, number, number] {
  const p = v.trim().replace(/^v/i, "").split(".").map((n) => Number(n) || 0)
  return [p[0] ?? 0, p[1] ?? 0, p[2] ?? 0]
}

// Mirrors the server's UPGRADE validation: same major AND (same minor with a higher patch,
// OR exactly the next minor). Downgrades and jumps of two or more minors are rejected.
function isUpgradeTarget(current: string, target: string): boolean {
  if (!current.trim() || !target.trim() || current === target) return false
  const [cMaj, cMin, cPat] = parseVersion(current)
  const [tMaj, tMin, tPat] = parseVersion(target)
  if (tMaj !== cMaj) return false
  if (tMin === cMin) return tPat > cPat
  return tMin === cMin + 1
}

// Total desired worker capacity across node groups — fixed groups contribute `count`,
// autoscale groups their min–max range. Rendered as "n" when min === max, else "min–max".
function desiredNodes(c: Cluster): { min: number; max: number } {
  let min = 0
  let max = 0
  for (const g of (c.node_groups as Cluster[]) ?? []) {
    if (g.autoscale === true) {
      min += Number(g.min) || 0
      max += Number(g.max) || 0
    } else {
      const n = Number(g.count ?? g.replicas) || 0
      min += n
      max += n
    }
  }
  return { min, max }
}

function parseLabels(s: string): Record<string, string> | undefined {
  const out: Record<string, string> = {}
  for (const term of s.split(",")) {
    const t = term.trim()
    if (!t) continue
    const eq = t.indexOf("=")
    if (eq <= 0) continue
    out[t.slice(0, eq)] = t.slice(eq + 1)
  }
  return Object.keys(out).length ? out : undefined
}

function groupsToData(groups: NodeGroupRow[]) {
  return groups.map((g) => ({
    name: g.name.trim(),
    flavorId: g.flavorId,
    ...(g.autoscale
      ? { autoscale: true, min: Number(g.min), max: Number(g.max) }
      : { count: Number(g.count) }),
    ...(parseLabels(g.labels) ? { labels: parseLabels(g.labels) } : {}),
    ...(g.taints.trim() ? { taints: g.taints.split(",").map((t) => t.trim()).filter(Boolean) } : {}),
  }))
}

// rowsToSyncGroups mirrors the sync payload's snake_case node_groups shape for the optimistic
// cache patch after SET_NODE_GROUPS (phase/ready counts are unknown until the next sync and
// are intentionally left out).
function rowsToSyncGroups(rows: NodeGroupRow[]): Cluster[] {
  return rows.map((g) => ({
    name: g.name.trim(),
    flavor_id: g.flavorId,
    ...(g.autoscale
      ? { autoscale: true, min: Number(g.min), max: Number(g.max) }
      : { count: Number(g.count) }),
    ...(parseLabels(g.labels) ? { labels: parseLabels(g.labels) } : {}),
    ...(g.taints.trim() ? { taints: g.taints.split(",").map((t) => t.trim()).filter(Boolean) } : {}),
  }))
}

// The SET_OIDC request body is camelCase; the sync payload's cluster.oidc is snake_case.
const oidcSnakeKeys: Record<string, string> = {
  issuerUrl: "issuer_url",
  clientId: "client_id",
  usernameClaim: "username_claim",
  usernamePrefix: "username_prefix",
  groupsClaim: "groups_claim",
  groupsPrefix: "groups_prefix",
}
function oidcToSyncShape(oidc: Record<string, string>): Record<string, string> {
  return Object.fromEntries(Object.entries(oidc).map(([k, v]) => [oidcSnakeKeys[k] ?? k, v]))
}

function groupValid(g: NodeGroupRow): boolean {
  if (!g.name.trim() || !g.flavorId) return false
  if (g.autoscale) return Number(g.min) >= 1 && Number(g.max) >= Number(g.min)
  return Number(g.count) >= 1
}

// dataGroupsToRows prefills the edit dialog from the cached cluster payload (snake_case sync
// shape) — including labels/taints, since SET_NODE_GROUPS is a full replace and an empty
// prefill would silently strip them on save.
function dataGroupsToRows(c: Cluster): NodeGroupRow[] {
  const groups = (c.node_groups as Cluster[]) ?? []
  if (!groups.length) return [{ ...emptyGroup }]
  return groups.map((g) => ({
    name: String(g.name ?? ""),
    flavorId: String(g.flavor_id ?? ""),
    count: String(g.count ?? 1),
    autoscale: g.autoscale === true,
    min: String(g.min ?? 1),
    max: String(g.max ?? 5),
    labels: Object.entries((g.labels as Record<string, string>) ?? {})
      .map(([k, v]) => `${k}=${v}`)
      .join(","),
    taints: ((g.taints as string[]) ?? []).map(String).join(","),
  }))
}

export default function KubernetesPage() {
  const pid = useProjectId()
  const qc = useQueryClient()
  const locations = useLocations(pid)
  const services = useProjectServices(pid)
  const project = useProject(pid).project

  // Cluster CRUD scope = a kamaji location; flavors come from the OpenStack service.
  const kLocs = useMemo(
    () => (locations.data ?? []).filter((l) => l.provider === "kamaji" && l.serviceId && l.region),
    [locations.data],
  )
  const kLoc = kLocs[0]
  const osLoc = locations.data?.find((l) => l.provider !== "kamaji" && l.provider !== "ceph-s3")
  const kScope: CloudScope | undefined = kLoc?.serviceId && kLoc?.region ? { serviceId: kLoc.serviceId, region: kLoc.region } : undefined
  const osScope: CloudScope | undefined = osLoc?.serviceId && osLoc?.region ? { serviceId: osLoc.serviceId, region: osLoc.region } : undefined

  // The create wizard's chosen kamaji location (auto-selects the sole one; picker when several).
  const [createLocKey, setCreateLocKey] = useState("")
  const createLoc = kLocs.find((l) => locKeyOf(l) === createLocKey) ?? kLocs[0]
  const createScope: CloudScope | undefined =
    createLoc?.serviceId && createLoc?.region ? { serviceId: createLoc.serviceId, region: createLoc.region } : undefined

  // Curated versions live on the kamaji service DTO — per service, so per selected location.
  const versionsFor = useCallback(
    (serviceId?: string) =>
      sortVersions((services.data?.find((s) => s.id === serviceId)?.kubernetesVersions ?? []).filter(Boolean)),
    [services.data],
  )
  const createVersions = useMemo(() => versionsFor(createLoc?.serviceId), [versionsFor, createLoc?.serviceId])

  // A cached cluster row records the kamaji service it lives on (serviceId/region on the
  // resource DTO). With several kamaji locations attached, the ROW's own service — not
  // whichever location happens to be first — must resolve the curated versions, the flavor
  // allowlist and the x-service-id scope for actions; rows synced before those fields
  // existed fall back to the first location.
  const rowServiceId = useCallback(
    (r: CloudResource) => r.serviceId || kLoc?.serviceId,
    [kLoc?.serviceId],
  )
  const rowScope = useCallback(
    (r: CloudResource): CloudScope | undefined => {
      const serviceId = r.serviceId || kLoc?.serviceId
      const region = r.region || kLocs.find((l) => l.serviceId === serviceId)?.region
      return serviceId && region ? { serviceId, region } : undefined
    },
    [kLoc?.serviceId, kLocs],
  )

  const { data, isLoading, isError, error, refetch, isFetching } = useQuery({
    queryKey: ["cloud", pid, "KUBERNETES_CLUSTER"],
    queryFn: () =>
      apiFetch<CloudResource[]>(`/project/${pid}/resource?type=KUBERNETES_CLUSTER`, { method: "POST", cloud: kScope }),
    enabled: !!pid && !!kScope,
  })
  const flavors = useQuery({
    queryKey: ["bulk-action", pid, "LIST_FLAVORS", osScope?.serviceId],
    queryFn: () =>
      apiFetch<{ result?: Flavor[] }>(`/project/${pid}/cloud/action`, {
        method: "POST",
        body: { action: "LIST_FLAVORS" },
        cloud: osScope,
      }),
    enabled: !!pid && !!osScope,
    select: (d) => d?.result ?? [],
  })
  // Same gating inputs CreateServerPage uses: live compute/GPU quota + region GPU capacity
  // (capacity only fetched when the operator made it visible to the project).
  const projectQuota = useProjectQuota(pid, osScope)
  const gpuCapacity = useProjectGpuCapacity(pid, osScope, project?.gpuCapacityVisible === true)

  // Node-group flavor options, filtered and gated exactly like the create-server wizard:
  //  - no local ephemeral/swap disk (worker nodes boot from volume; backend rejects those),
  //  - only ids on the kamaji service's kubernetesFlavorIds allowlist when one is configured,
  //  - flavors that exceed the project quota or the region's GPU capacity cannot be picked.
  const flavorOptionsFor = useCallback(
    (kamajiServiceId?: string): FlavorOption[] => {
      const allow = new Set(
        (services.data?.find((s) => s.id === kamajiServiceId)?.kubernetesFlavorIds ?? []).filter(Boolean),
      )
      return (flavors.data ?? [])
        .filter((f) => !(f.data?.ephemeral ?? 0) && !(f.data?.swap ?? 0))
        .filter((f) => allow.size === 0 || allow.has(f.externalId ?? "") || allow.has(f.data?.id ?? ""))
        .map((f) => {
          const id = f.externalId ?? f.data?.id ?? ""
          const blocked =
            serverQuotaViolations(projectQuota.data, f).length > 0 ||
            gpuCapacityViolations(gpuCapacity.data, f).length > 0
          const spec = [
            f.data?.vcpus != null ? `${f.data.vcpus} vCPU` : "",
            f.data?.ram ? `${Math.round(f.data.ram / 1024)} GB RAM` : "",
          ]
            .filter(Boolean)
            .join(" · ")
          return { id, name: f.data?.name ?? id, spec, blocked }
        })
        .filter((o) => !!o.id)
    },
    [services.data, flavors.data, projectQuota.data, gpuCapacity.data],
  )
  const createFlavorOptions = useMemo(
    () => flavorOptionsFor(createLoc?.serviceId),
    [flavorOptionsFor, createLoc?.serviceId],
  )

  const invalidate = () => void qc.invalidateQueries({ queryKey: ["cloud", pid, "KUBERNETES_CLUSTER"] })

  const [createOpen, setCreateOpen] = useState(false)
  const [toDelete, setToDelete] = useState<CloudResource | null>(null)
  const [manageFor, setManageFor] = useState<CloudResource | null>(null)

  // Optimistic patch of a cluster row after a successful action. The mgmt-cluster sync only
  // refreshes the cached row minutes later, and the manage sheet re-reads this row — without
  // the patch a second SET_NODE_GROUPS/SET_OIDC full-replaces with the STALE payload and
  // silently reverts the first edit. The next sync overwrites with live truth, which is fine.
  const patchCluster = useCallback(
    (id: string, patch: Record<string, any>) => {
      const apply = (r: CloudResource): CloudResource =>
        r.id === id ? { ...r, data: { ...r.data, cluster: { ...(r.data?.cluster ?? {}), ...patch } } } : r
      qc.setQueryData<CloudResource[]>(["cloud", pid, "KUBERNETES_CLUSTER"], (rows) => rows?.map(apply))
      setManageFor((m) => (m && m.id === id ? apply(m) : m))
    },
    [qc, pid],
  )

  const del = useMutation({
    mutationFn: (r: CloudResource) =>
      apiFetch(`/project/${pid}/cloud/${r.id}`, { method: "DELETE", cloud: rowScope(r) }),
    onSuccess: () => {
      toast.success("Cluster deletion requested")
      setToDelete(null)
      setManageFor(null)
      setTimeout(invalidate, 1500)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const columns = useMemo<ColumnDef<CloudResource, any>[]>(
    () => [
      {
        id: "name",
        accessorFn: (r) => (cluster(r).name as string) || r.externalId || r.id,
        header: sortableHeader("Name"),
        cell: ({ row, getValue }) => (
          <button
            className="inline-block py-1 font-medium hover:underline"
            onClick={(e) => {
              e.stopPropagation()
              setManageFor(row.original)
            }}
          >
            {getValue()}
          </button>
        ),
      },
      {
        id: "version",
        accessorFn: (r) => (cluster(r).version as string) ?? "",
        header: sortableHeader("Version"),
        cell: ({ row, getValue }) => {
          const current = (getValue() as string) || ""
          const upgradable =
            !!current && versionsFor(rowServiceId(row.original)).some((v) => isUpgradeTarget(current, v))
          return (
            <span className="flex items-center gap-2">
              <span className="font-mono text-sm">{current || "—"}</span>
              {upgradable ? <Badge variant="outline">Upgrade available</Badge> : null}
            </span>
          )
        },
      },
      {
        id: "status",
        accessorFn: (r) => (cluster(r).status as string) ?? "",
        header: sortableHeader("Status"),
        cell: ({ getValue }) => <StatusBadge status={getValue() || undefined} />,
      },
      {
        id: "endpoint",
        accessorFn: (r) => (cluster(r).endpoint as string) ?? "",
        header: "API endpoint",
        cell: ({ getValue }) => <span className="font-mono text-xs">{getValue() || "—"}</span>,
      },
      {
        id: "nodes",
        accessorFn: (r) => desiredNodes(cluster(r)).min,
        header: sortableHeader("Nodes"),
        cell: ({ row }) => {
          const { min, max } = desiredNodes(cluster(row.original))
          return <span className="text-sm tabular-nums">{max > min ? `${min}–${max}` : min}</span>
        },
      },
      {
        id: "created",
        accessorFn: (r) => (cluster(r).created_at as string) ?? r.createdAt ?? "",
        header: sortableHeader("Created"),
        cell: ({ getValue }) => <span className="text-sm text-muted-foreground">{timeAgo(getValue())}</span>,
      },
      {
        id: "actions",
        header: () => null,
        enableSorting: false,
        cell: ({ row }) => (
          <div className="text-right" onClick={(e) => e.stopPropagation()}>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon-sm" aria-label="Cluster actions">
                  <MoreHorizontal className="size-4" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onClick={() => setManageFor(row.original)}>
                  <Settings2 className="size-4" /> Manage
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem variant="destructive" onClick={() => setToDelete(row.original)}>
                  <Trash2 className="size-4" /> Delete
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        ),
      },
    ],
    [versionsFor, rowServiceId],
  )

  return (
    <>
      <PageHeader
        title="Kubernetes"
        eyebrow="Platform"
        description="Managed Kubernetes clusters — the control plane is hosted for you; worker nodes run in this project."
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => void refetch()} disabled={isFetching} aria-label="Refresh">
              <RefreshCw className={isFetching ? "size-4 animate-spin" : "size-4"} />
            </Button>
            <Button size="sm" onClick={() => setCreateOpen(true)} disabled={!kScope}>
              <Plus className="size-4" /> Create cluster
            </Button>
          </>
        }
      />

      {!isLoading && !isError && !data?.length ? (
        <EmptyState
          icon={Boxes}
          title="No Kubernetes clusters yet"
          hint="Create a managed cluster — pick a version and node groups; the control plane is provisioned for you."
          action={
            <Button onClick={() => setCreateOpen(true)} disabled={!kScope}>
              <Plus className="size-4" /> Create cluster
            </Button>
          }
        />
      ) : (
        <DataTable
          columns={columns}
          data={data}
          isLoading={isLoading}
          error={isError ? (error as Error) : null}
          searchPlaceholder="Search clusters…"
          onRowClick={(r) => setManageFor(r)}
        />
      )}

      {createOpen && (
        <ClusterFormDialog
          title="Create Kubernetes cluster"
          submitLabel="Create cluster"
          versions={createVersions}
          flavors={createFlavorOptions}
          locations={kLocs}
          locKey={createLoc ? locKeyOf(createLoc) : ""}
          onLocKey={setCreateLocKey}
          onClose={() => setCreateOpen(false)}
          onSubmit={async (body) => {
            // Target the CHOSEN location explicitly — with several kamaji locations the first
            // one in the API array is not necessarily the one the user picked.
            if (!createScope) throw new Error("Select a location first")
            await apiFetch(`/project/${pid}/cloud`, {
              method: "POST",
              body: { type: "KUBERNETES_CLUSTER", data: body },
              cloud: createScope,
            })
            toast.success(`Cluster "${body.name}" is being created`)
            setCreateOpen(false)
            invalidate()
          }}
        />
      )}

      <Dialog open={!!toDelete} onOpenChange={(o) => !o && setToDelete(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete cluster</DialogTitle>
            <DialogDescription>
              Delete "{toDelete ? (cluster(toDelete).name as string) || toDelete.externalId : ""}"? The control
              plane and ALL worker nodes are deleted. Workloads and data on the cluster are lost. This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setToDelete(null)}>Cancel</Button>
            <Button variant="destructive" onClick={() => toDelete && del.mutate(toDelete)} disabled={del.isPending}>
              {del.isPending ? "Deleting…" : "Delete"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {manageFor && (
        <ClusterManageSheet
          pid={pid}
          scope={rowScope(manageFor)}
          resource={manageFor}
          versions={versionsFor(rowServiceId(manageFor))}
          flavors={flavorOptionsFor(rowServiceId(manageFor))}
          onClose={() => setManageFor(null)}
          onDeleted={() => setToDelete(manageFor)}
          onPatch={(patch) => patchCluster(manageFor.id, patch)}
        />
      )}
    </>
  )
}

// ── create/edit form ─────────────────────────────────────────────────────────
function ClusterFormDialog({
  title, submitLabel, versions, flavors, locations, locKey, onLocKey, onClose, onSubmit,
}: {
  title: string
  submitLabel: string
  versions: string[]
  flavors: FlavorOption[]
  locations: Location[]
  locKey: string
  onLocKey: (key: string) => void
  onClose: () => void
  onSubmit: (body: Record<string, any>) => Promise<void>
}) {
  const [name, setName] = useState("")
  // Derived, not effect-reset: switching location swaps the curated list, and a stale pick
  // that is no longer offered falls back to the newest offered version.
  const [versionSel, setVersionSel] = useState("")
  const version = versions.includes(versionSel) ? versionSel : versions[0] ?? ""
  const [ha, setHa] = useState(true)
  const [groups, setGroups] = useState<NodeGroupRow[]>([{ ...emptyGroup }])
  const [oidcOpen, setOidcOpen] = useState(false)
  const [oidc, setOidc] = useState<OidcDraft>({ ...emptyOidc })
  const [allowedCidrs, setAllowedCidrs] = useState("")
  const [pending, setPending] = useState(false)

  const valid = !!name.trim() && !!version && groups.length > 0 && groups.every(groupValid)

  const submit = async () => {
    setPending(true)
    try {
      await onSubmit({
        name: name.trim(),
        version,
        ha,
        nodeGroups: groupsToData(groups),
        ...(oidc.issuerUrl.trim() ? { oidc: oidcToBody(oidc) } : {}),
        ...(allowedCidrs.trim()
          ? { allowedCidrs: allowedCidrs.split(",").map((c) => c.trim()).filter(Boolean) }
          : {}),
      })
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>
            The control plane is hosted by the platform; worker nodes are servers in this project and are billed
            like regular instances.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-4 py-2">
          {locations.length > 1 && (
            <div className="grid gap-2">
              <Label>Location</Label>
              <Select
                value={locKey}
                onValueChange={(k) => {
                  if (k === locKey) return
                  onLocKey(k)
                  // Offered flavors can differ per location's allowlist — clear picks so a
                  // flavor from the previous location can't be submitted here.
                  setGroups(groups.map((g) => ({ ...g, flavorId: "" })))
                }}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {locations.map((l) => (
                    <SelectItem key={locKeyOf(l)} value={locKeyOf(l)}>
                      {l.displayName || l.region}
                      {l.serviceName ? ` — ${l.serviceName}` : ""}
                      {l.displayName && l.displayName !== l.region ? ` (${l.region})` : ""}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <p className="text-xs text-muted-foreground">
                Each location hosts its own clusters — offered versions and flavors may differ.
              </p>
            </div>
          )}
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div className="grid gap-2">
              <Label htmlFor="k8s-name">Name</Label>
              <Input id="k8s-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="prod-cluster" />
            </div>
            <div className="grid gap-2">
              <Label>Version</Label>
              <Select value={version} onValueChange={setVersionSel}>
                <SelectTrigger>
                  <SelectValue placeholder={versions.length ? "Pick a version" : "No versions offered"} />
                </SelectTrigger>
                <SelectContent>
                  {versions.map((v) => (
                    <SelectItem key={v} value={v}>{v}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="flex items-center justify-between rounded-lg border p-3">
            <div>
              <Label className="text-sm font-medium">High availability</Label>
              <div className="text-xs text-muted-foreground">3 control-plane replicas instead of 1.</div>
            </div>
            <Switch checked={ha} onCheckedChange={setHa} />
          </div>

          <NodeGroupsEditor groups={groups} setGroups={setGroups} flavors={flavors} />

          <div className="grid gap-2">
            <Label htmlFor="k8s-cidrs">Allowed API CIDRs (optional, comma-separated)</Label>
            <Input
              id="k8s-cidrs"
              className="font-mono"
              value={allowedCidrs}
              onChange={(e) => setAllowedCidrs(e.target.value)}
              placeholder="203.0.113.0/24, 198.51.100.7/32"
            />
          </div>

          <button type="button" className="text-left text-sm font-medium text-primary hover:underline" onClick={() => setOidcOpen(!oidcOpen)}>
            {oidcOpen ? "Hide" : "Configure"} OIDC authentication (optional)
          </button>
          {oidcOpen && (
            <div className="grid gap-4 rounded-lg border p-3">
              <OidcFields value={oidc} onChange={setOidc} idPrefix="oidc" />
              <p className="text-xs text-muted-foreground">
                Point the cluster's API server at your own identity provider. You manage RBAC bindings for OIDC users.
              </p>
            </div>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>Cancel</Button>
          <Button onClick={() => void submit()} disabled={!valid || pending}>
            {pending ? "Working…" : submitLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// Shared OIDC field set — used by the create wizard and the manage sheet's SET_OIDC dialog.
function OidcFields({
  value, onChange, idPrefix,
}: {
  value: OidcDraft
  onChange: (v: OidcDraft) => void
  idPrefix: string
}) {
  const set = (patch: Partial<OidcDraft>) => onChange({ ...value, ...patch })
  return (
    <>
      <div className="grid gap-2">
        <Label htmlFor={`${idPrefix}-issuer`}>Issuer URL</Label>
        <Input id={`${idPrefix}-issuer`} className="font-mono" value={value.issuerUrl} onChange={(e) => set({ issuerUrl: e.target.value })} placeholder="https://auth.example.com" />
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor={`${idPrefix}-client`}>Client ID</Label>
          <Input id={`${idPrefix}-client`} value={value.clientId} onChange={(e) => set({ clientId: e.target.value })} placeholder="kubernetes" />
        </div>
        <div />
        <div className="grid gap-2">
          <Label htmlFor={`${idPrefix}-user`}>Username claim</Label>
          <Input id={`${idPrefix}-user`} value={value.usernameClaim} onChange={(e) => set({ usernameClaim: e.target.value })} placeholder="email" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor={`${idPrefix}-user-prefix`}>Username prefix</Label>
          <Input id={`${idPrefix}-user-prefix`} value={value.usernamePrefix} onChange={(e) => set({ usernamePrefix: e.target.value })} placeholder="oidc:" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor={`${idPrefix}-groups`}>Groups claim</Label>
          <Input id={`${idPrefix}-groups`} value={value.groupsClaim} onChange={(e) => set({ groupsClaim: e.target.value })} placeholder="groups" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor={`${idPrefix}-groups-prefix`}>Groups prefix</Label>
          <Input id={`${idPrefix}-groups-prefix`} value={value.groupsPrefix} onChange={(e) => set({ groupsPrefix: e.target.value })} placeholder="oidc:" />
        </div>
      </div>
    </>
  )
}

function NodeGroupsEditor({
  groups, setGroups, flavors,
}: {
  groups: NodeGroupRow[]
  setGroups: (g: NodeGroupRow[]) => void
  flavors: FlavorOption[]
}) {
  const set = (i: number, patch: Partial<NodeGroupRow>) =>
    setGroups(groups.map((g, j) => (j === i ? { ...g, ...patch } : g)))
  return (
    <div className="grid gap-3">
      <div className="flex items-center justify-between">
        <Label>Node groups</Label>
        <Button variant="outline" size="sm" onClick={() => setGroups([...groups, { ...emptyGroup, name: `pool-${groups.length + 1}` }])}>
          <Plus className="size-4" /> Add group
        </Button>
      </div>
      {groups.map((g, i) => (
        <div key={i} className="grid gap-3 rounded-lg border p-3">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div className="grid gap-2">
              <Label htmlFor={`ng-name-${i}`}>Name</Label>
              <Input id={`ng-name-${i}`} value={g.name} onChange={(e) => set(i, { name: e.target.value })} placeholder="workers" />
            </div>
            <div className="grid gap-2">
              <Label>Flavor</Label>
              <Select value={g.flavorId} onValueChange={(v) => set(i, { flavorId: v })}>
                <SelectTrigger>
                  <SelectValue placeholder={flavors.length ? "Pick a flavor" : "No flavors available"} />
                </SelectTrigger>
                <SelectContent>
                  {/* An existing group's flavor that is no longer offered (allowlist change,
                      catalog drift) stays visible and submittable — SET_NODE_GROUPS is a full
                      replace and must not silently drop untouched groups. */}
                  {g.flavorId && !flavors.some((o) => o.id === g.flavorId) ? (
                    <SelectItem value={g.flavorId}>
                      <span className="font-mono text-xs">{g.flavorId}</span>
                    </SelectItem>
                  ) : null}
                  {flavors.map((o) => (
                    <SelectItem key={o.id} value={o.id} disabled={o.blocked}>
                      {o.name}
                      {o.spec ? <span className="text-xs text-muted-foreground"> {o.spec}</span> : null}
                      {o.blocked ? <span className="text-xs text-destructive"> exceeds project quota</span> : null}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="grid grid-cols-2 items-end gap-3 sm:grid-cols-4">
            <div className="flex items-center gap-2 pb-2">
              <Switch id={`ng-as-${i}`} checked={g.autoscale} onCheckedChange={(on) => set(i, { autoscale: on })} />
              <Label htmlFor={`ng-as-${i}`} className="text-sm">Autoscale</Label>
            </div>
            {g.autoscale ? (
              <>
                <div className="grid gap-2">
                  <Label htmlFor={`ng-min-${i}`}>Min</Label>
                  <Input id={`ng-min-${i}`} type="number" min={1} value={g.min} onChange={(e) => set(i, { min: e.target.value })} />
                </div>
                <div className="grid gap-2">
                  <Label htmlFor={`ng-max-${i}`}>Max</Label>
                  <Input id={`ng-max-${i}`} type="number" min={1} value={g.max} onChange={(e) => set(i, { max: e.target.value })} />
                </div>
              </>
            ) : (
              <div className="grid gap-2">
                <Label htmlFor={`ng-count-${i}`}>Nodes</Label>
                <Input id={`ng-count-${i}`} type="number" min={1} value={g.count} onChange={(e) => set(i, { count: e.target.value })} />
              </div>
            )}
            <div className="flex justify-end pb-1">
              {groups.length > 1 && (
                <Button variant="ghost" size="icon-sm" aria-label="Remove group" onClick={() => setGroups(groups.filter((_, j) => j !== i))}>
                  <Trash2 className="size-4 text-muted-foreground" />
                </Button>
              )}
            </div>
          </div>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div className="grid gap-2">
              <Label htmlFor={`ng-labels-${i}`}>Node labels (k=v, comma-separated)</Label>
              <Input id={`ng-labels-${i}`} className="font-mono text-xs" value={g.labels} onChange={(e) => set(i, { labels: e.target.value })} placeholder="tier=app,env=prod" />
            </div>
            <div className="grid gap-2">
              <Label htmlFor={`ng-taints-${i}`}>Taints (key=val:Effect, comma-separated)</Label>
              <Input id={`ng-taints-${i}`} className="font-mono text-xs" value={g.taints} onChange={(e) => set(i, { taints: e.target.value })} placeholder="gpu=true:NoSchedule" />
            </div>
          </div>
        </div>
      ))}
    </div>
  )
}

// ── manage sheet ─────────────────────────────────────────────────────────────
function ClusterManageSheet({
  pid, scope, resource, versions, flavors, onClose, onDeleted, onPatch,
}: {
  pid: string
  scope: CloudScope | undefined
  resource: CloudResource
  versions: string[]
  flavors: FlavorOption[]
  onClose: () => void
  onDeleted: () => void
  // Optimistically applies a partial data.cluster patch to the cached row (and this sheet's
  // resource prop) — MUST be called after every successful mutating action, or the next
  // full-replace action would be built from stale data.
  onPatch: (patch: Record<string, any>) => void
}) {
  const c = cluster(resource)
  const name = (c.name as string) || resource.externalId || resource.id
  const groups = (c.node_groups as Cluster[]) ?? []
  const [upgradeOpen, setUpgradeOpen] = useState(false)
  const [editOpen, setEditOpen] = useState(false)
  const [oidcEditOpen, setOidcEditOpen] = useState(false)
  const current = (c.version as string) ?? ""
  // Only versions the server would accept (same major; same minor higher patch, or minor+1) —
  // never offer downgrades or multi-minor jumps that would just 400.
  const upgradeTargets = versions.filter((v) => isUpgradeTarget(current, v))
  const [target, setTarget] = useState("")

  // Prefill the SET_OIDC form from the sync payload's oidc object (fields present only when
  // set); the legacy oidc_issuer string covers clusters synced before the object existed.
  const oidcData = (c.oidc as Record<string, unknown>) ?? {}
  const oidcInitial: OidcDraft = {
    issuerUrl: String(oidcData.issuer_url ?? c.oidc_issuer ?? ""),
    clientId: String(oidcData.client_id ?? ""),
    usernameClaim: String(oidcData.username_claim ?? ""),
    usernamePrefix: String(oidcData.username_prefix ?? ""),
    groupsClaim: String(oidcData.groups_claim ?? ""),
    groupsPrefix: String(oidcData.groups_prefix ?? ""),
  }

  const act = (action: string, data?: Record<string, any>) =>
    apiFetch<{ result?: any }>(`/project/${pid}/cloud/${resource.id}/action`, {
      method: "POST",
      body: data ? { action, data } : { action },
      cloud: scope,
    })

  const kubeconfig = useMutation({
    mutationFn: () => act("GET_KUBECONFIG"),
    onSuccess: (d) => {
      const kc = d?.result?.kubeconfig as string | undefined
      if (!kc) {
        toast.error("Kubeconfig is not ready yet")
        return
      }
      const blob = new Blob([kc], { type: "application/yaml" })
      const url = URL.createObjectURL(blob)
      const a = document.createElement("a")
      a.href = url
      a.download = `${name}-kubeconfig.yaml`
      a.click()
      URL.revokeObjectURL(url)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const upgrade = useMutation({
    mutationFn: () => act("UPGRADE", { version: target }),
    onSuccess: () => {
      toast.success(`Upgrade to ${target} started — control plane first, then node groups roll`)
      setUpgradeOpen(false)
      onPatch({ version: target })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-2xl">
        <SheetHeader className="border-b">
          <div className="text-eyebrow">Kubernetes cluster</div>
          <SheetTitle className="font-display text-lg tracking-tight">{name}</SheetTitle>
          <SheetDescription>
            <span className="font-mono">{(c.endpoint as string) || "endpoint pending…"}</span>
          </SheetDescription>
        </SheetHeader>

        <div className="space-y-6 px-4 pb-6">
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <div className="rounded-lg border bg-card p-3">
              <div className="text-xs text-muted-foreground">Status</div>
              <StatusBadge status={(c.status as string) || undefined} />
            </div>
            <div className="rounded-lg border bg-card p-3">
              <div className="text-xs text-muted-foreground">Version</div>
              <div className="flex flex-wrap items-center gap-1.5">
                <span className="font-mono text-sm">{current || "—"}</span>
                {upgradeTargets.length > 0 ? <Badge variant="outline">Upgrade available</Badge> : null}
              </div>
            </div>
            <div className="rounded-lg border bg-card p-3">
              <div className="text-xs text-muted-foreground">Control plane</div>
              <div className="text-sm">{c.cp_replicas ? `${c.cp_replicas} replicas` : "—"}</div>
            </div>
            <div className="rounded-lg border bg-card p-3">
              <div className="text-xs text-muted-foreground">Sync</div>
              <div className="text-sm">{(c.sync_status as string) || "—"}</div>
            </div>
          </div>

          <div className="flex flex-wrap gap-2">
            <Button size="sm" onClick={() => kubeconfig.mutate()} disabled={kubeconfig.isPending}>
              <Download className="size-4" /> {kubeconfig.isPending ? "Fetching…" : "Download kubeconfig"}
            </Button>
            <Button size="sm" variant="outline" onClick={() => setUpgradeOpen(true)}>
              Upgrade
            </Button>
            <Button size="sm" variant="outline" onClick={() => setEditOpen(true)}>
              Edit node groups
            </Button>
            <Button size="sm" variant="outline" onClick={() => setOidcEditOpen(true)}>
              Configure OIDC
            </Button>
            <Button size="sm" variant="destructive" onClick={onDeleted}>
              <Trash2 className="size-4" /> Delete
            </Button>
          </div>

          {oidcInitial.issuerUrl && (
            <p className="text-xs text-muted-foreground">
              OIDC issuer: <span className="font-mono">{oidcInitial.issuerUrl}</span>
            </p>
          )}

          <div className="overflow-hidden rounded-xl border bg-card">
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead>Node group</TableHead>
                  <TableHead>Flavor</TableHead>
                  <TableHead>Desired</TableHead>
                  <TableHead>Ready</TableHead>
                  <TableHead>Phase</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {groups.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={5} className="text-center text-sm text-muted-foreground">
                      No node groups reported yet.
                    </TableCell>
                  </TableRow>
                ) : (
                  groups.map((g, i) => (
                    <TableRow key={(g.name as string) ?? i}>
                      <TableCell className="font-medium">{(g.name as string) || "—"}</TableCell>
                      <TableCell className="font-mono text-xs">{(g.flavor_id as string) || "—"}</TableCell>
                      <TableCell className="font-mono text-sm">
                        {g.autoscale ? `${g.min}–${g.max} (auto)` : (g.count ?? g.replicas ?? "—")}
                      </TableCell>
                      <TableCell className="font-mono text-sm">{g.ready_replicas ?? "—"}</TableCell>
                      <TableCell className="text-sm">{(g.phase as string) || "—"}</TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          </div>
        </div>

        {/* Upgrade */}
        <Dialog open={upgradeOpen} onOpenChange={setUpgradeOpen}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Upgrade cluster</DialogTitle>
              <DialogDescription>
                The control plane upgrades first (zero-downtime rollout), then node groups rotate to the new
                version's image. Workloads move as nodes are replaced.
              </DialogDescription>
            </DialogHeader>
            {upgradeTargets.length === 0 ? (
              <p className="py-2 text-sm text-muted-foreground">
                This cluster is up to date — none of the offered versions is a valid upgrade
                from <span className="font-mono">{current || "the current version"}</span>. Upgrades go one minor
                version at a time (or to a newer patch of the same minor).
              </p>
            ) : (
              <div className="grid gap-2 py-2">
                <Label>Target version (current: {current || "—"})</Label>
                <Select value={target} onValueChange={setTarget}>
                  <SelectTrigger>
                    <SelectValue placeholder="Pick a version" />
                  </SelectTrigger>
                  <SelectContent>
                    {upgradeTargets.map((v) => (
                      <SelectItem key={v} value={v}>{v}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            )}
            <DialogFooter>
              <Button variant="outline" onClick={() => setUpgradeOpen(false)}>
                {upgradeTargets.length === 0 ? "Close" : "Cancel"}
              </Button>
              {upgradeTargets.length > 0 && (
                <Button onClick={() => upgrade.mutate()} disabled={!upgradeTargets.includes(target) || upgrade.isPending}>
                  {upgrade.isPending ? "Starting…" : "Upgrade"}
                </Button>
              )}
            </DialogFooter>
          </DialogContent>
        </Dialog>

        {/* Edit node groups: prefilled rows → SET_NODE_GROUPS */}
        {editOpen && (
          <EditNodeGroupsDialog
            initial={dataGroupsToRows(c)}
            flavors={flavors}
            onClose={() => setEditOpen(false)}
            onSubmit={async (rows) => {
              await act("SET_NODE_GROUPS", { nodeGroups: groupsToData(rows) })
              onPatch({ node_groups: rowsToSyncGroups(rows) })
              toast.success("Node groups update started")
              setEditOpen(false)
            }}
          />
        )}

        {/* OIDC: prefilled form → SET_OIDC (empty issuer = disable) */}
        {oidcEditOpen && (
          <OidcEditDialog
            initial={oidcInitial}
            onClose={() => setOidcEditOpen(false)}
            onSubmit={async (oidc) => {
              await act("SET_OIDC", { oidc })
              onPatch({ oidc: oidcToSyncShape(oidc), oidc_issuer: oidc.issuerUrl ?? "" })
              toast.success(oidc.issuerUrl ? "OIDC update started" : "OIDC is being disabled")
              setOidcEditOpen(false)
            }}
          />
        )}
      </SheetContent>
    </Sheet>
  )
}

function OidcEditDialog({
  initial, onClose, onSubmit,
}: {
  initial: OidcDraft
  onClose: () => void
  onSubmit: (oidc: Record<string, string>) => Promise<void>
}) {
  const [oidc, setOidc] = useState<OidcDraft>(initial)
  const [pending, setPending] = useState(false)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>OIDC authentication</DialogTitle>
          <DialogDescription>
            Point the cluster's API server at your own identity provider. You manage RBAC bindings for OIDC users.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-4 py-2">
          <OidcFields value={oidc} onChange={setOidc} idPrefix="oidc-edit" />
          <p className="text-xs text-muted-foreground">
            Leave the issuer URL empty to disable OIDC authentication on this cluster.
          </p>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>Cancel</Button>
          <Button
            onClick={() => {
              setPending(true)
              onSubmit(oidcToBody(oidc))
                .catch((e: Error) => toast.error(e.message))
                .finally(() => setPending(false))
            }}
            disabled={pending}
          >
            {pending ? "Applying…" : "Apply"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function EditNodeGroupsDialog({
  initial, flavors, onClose, onSubmit,
}: {
  initial: NodeGroupRow[]
  flavors: FlavorOption[]
  onClose: () => void
  onSubmit: (rows: NodeGroupRow[]) => Promise<void>
}) {
  const [groups, setGroups] = useState<NodeGroupRow[]>(initial)
  const [pending, setPending] = useState(false)
  const valid = groups.length > 0 && groups.every(groupValid)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Edit node groups</DialogTitle>
          <DialogDescription>
            Add, remove or resize node groups. Removing a group deletes its nodes; workloads reschedule onto the
            remaining groups.
          </DialogDescription>
        </DialogHeader>
        <NodeGroupsEditor groups={groups} setGroups={setGroups} flavors={flavors} />
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>Cancel</Button>
          <Button
            onClick={() => {
              setPending(true)
              onSubmit(groups)
                .catch((e: Error) => toast.error(e.message))
                .finally(() => setPending(false))
            }}
            disabled={!valid || pending}
          >
            {pending ? "Applying…" : "Apply changes"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
