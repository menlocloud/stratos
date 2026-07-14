// Managed Kubernetes (kamaji provider) — list/create/manage clusters. The cloud scope for
// cluster CRUD is the KAMAJI service's location; the flavor picker for node groups reads the
// project's OPENSTACK service (worker VMs run in the customer's own tenant). Actions map to
// Go cloud_kamaji.go: create/delete + GET_KUBECONFIG / UPGRADE / SET_NODE_GROUPS.
import { useMemo, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import type { ColumnDef } from "@tanstack/react-table"
import { toast } from "sonner"
import { Boxes, Download, MoreHorizontal, Plus, RefreshCw, Settings2, Trash2 } from "lucide-react"
import { PageHeader } from "@/components/layout/PageHeader"
import { DataTable, sortableHeader } from "@/components/data-table"
import { EmptyState } from "@/components/empty-state"
import { StatusBadge } from "@/components/status-badge"
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
import { useLocations, useProjectId, useProjectServices } from "@/lib/hooks"
import type { CloudResource } from "@/lib/types"

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
type Flavor = { externalId?: string; id?: string; name?: string; vcpus?: number; ram?: number }

const emptyGroup: NodeGroupRow = { name: "workers", flavorId: "", count: "3", autoscale: false, min: "1", max: "5", labels: "", taints: "" }

function cluster(r: CloudResource): Cluster {
  return (r.data?.cluster as Cluster) ?? {}
}

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

function groupValid(g: NodeGroupRow): boolean {
  if (!g.name.trim() || !g.flavorId) return false
  if (g.autoscale) return Number(g.min) >= 1 && Number(g.max) >= Number(g.min)
  return Number(g.count) >= 1
}

// dataGroupsToRows prefills the edit dialog from the cached cluster payload (snake_case sync shape).
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
    labels: "",
    taints: "",
  }))
}

export default function KubernetesPage() {
  const pid = useProjectId()
  const qc = useQueryClient()
  const locations = useLocations(pid)
  const services = useProjectServices(pid)

  // Cluster CRUD scope = the kamaji service; flavors come from the OpenStack service.
  const kLoc = locations.data?.find((l) => l.provider === "kamaji")
  const osLoc = locations.data?.find((l) => l.provider !== "kamaji" && l.provider !== "ceph-s3")
  const kScope: CloudScope | undefined = kLoc?.serviceId && kLoc?.region ? { serviceId: kLoc.serviceId, region: kLoc.region } : undefined
  const osScope: CloudScope | undefined = osLoc?.serviceId && osLoc?.region ? { serviceId: osLoc.serviceId, region: osLoc.region } : undefined

  const versions = useMemo(() => {
    const svc = services.data?.find((s) => s.id === kLoc?.serviceId)
    return sortVersions(((svc?.kubernetesVersions as string[]) ?? []).filter(Boolean))
  }, [services.data, kLoc?.serviceId])

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

  const invalidate = () => void qc.invalidateQueries({ queryKey: ["cloud", pid, "KUBERNETES_CLUSTER"] })

  const [createOpen, setCreateOpen] = useState(false)
  const [toDelete, setToDelete] = useState<CloudResource | null>(null)
  const [manageFor, setManageFor] = useState<CloudResource | null>(null)

  const del = useMutation({
    mutationFn: (id: string) => apiFetch(`/project/${pid}/cloud/${id}`, { method: "DELETE", cloud: kScope }),
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
        cell: ({ getValue }) => <span className="font-mono text-sm">{getValue() || "—"}</span>,
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
        accessorFn: (r) => ((cluster(r).node_groups as Cluster[]) ?? []).length,
        header: sortableHeader("Node groups"),
        cell: ({ getValue }) => <span className="text-sm">{getValue()}</span>,
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
    [],
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
          versions={versions}
          flavors={flavors.data ?? []}
          onClose={() => setCreateOpen(false)}
          onSubmit={async (body) => {
            await apiFetch(`/project/${pid}/cloud`, {
              method: "POST",
              body: { type: "KUBERNETES_CLUSTER", data: body },
              cloud: kScope,
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
            <Button variant="destructive" onClick={() => toDelete && del.mutate(toDelete.id)} disabled={del.isPending}>
              {del.isPending ? "Deleting…" : "Delete"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {manageFor && (
        <ClusterManageSheet
          pid={pid}
          scope={kScope}
          resource={manageFor}
          versions={versions}
          flavors={flavors.data ?? []}
          onClose={() => setManageFor(null)}
          onDeleted={() => setToDelete(manageFor)}
          onChanged={invalidate}
        />
      )}
    </>
  )
}

// ── create/edit form ─────────────────────────────────────────────────────────
function ClusterFormDialog({
  title, submitLabel, versions, flavors, onClose, onSubmit,
}: {
  title: string
  submitLabel: string
  versions: string[]
  flavors: Flavor[]
  onClose: () => void
  onSubmit: (body: Record<string, any>) => Promise<void>
}) {
  const [name, setName] = useState("")
  const [version, setVersion] = useState(versions[0] ?? "")
  const [ha, setHa] = useState(true)
  const [groups, setGroups] = useState<NodeGroupRow[]>([{ ...emptyGroup }])
  const [oidcOpen, setOidcOpen] = useState(false)
  const [oidc, setOidc] = useState({ issuerUrl: "", clientId: "", usernameClaim: "", groupsClaim: "" })
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
        ...(oidc.issuerUrl.trim()
          ? {
              oidc: Object.fromEntries(
                Object.entries(oidc)
                  .map(([k, v]) => [k, v.trim()])
                  .filter(([, v]) => v !== ""),
              ),
            }
          : {}),
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
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div className="grid gap-2">
              <Label htmlFor="k8s-name">Name</Label>
              <Input id="k8s-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="prod-cluster" />
            </div>
            <div className="grid gap-2">
              <Label>Version</Label>
              <Select value={version} onValueChange={setVersion}>
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
              <div className="grid gap-2">
                <Label htmlFor="oidc-issuer">Issuer URL</Label>
                <Input id="oidc-issuer" className="font-mono" value={oidc.issuerUrl} onChange={(e) => setOidc({ ...oidc, issuerUrl: e.target.value })} placeholder="https://auth.example.com" />
              </div>
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
                <div className="grid gap-2">
                  <Label htmlFor="oidc-client">Client ID</Label>
                  <Input id="oidc-client" value={oidc.clientId} onChange={(e) => setOidc({ ...oidc, clientId: e.target.value })} placeholder="kubernetes" />
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="oidc-user">Username claim</Label>
                  <Input id="oidc-user" value={oidc.usernameClaim} onChange={(e) => setOidc({ ...oidc, usernameClaim: e.target.value })} placeholder="email" />
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="oidc-groups">Groups claim</Label>
                  <Input id="oidc-groups" value={oidc.groupsClaim} onChange={(e) => setOidc({ ...oidc, groupsClaim: e.target.value })} placeholder="groups" />
                </div>
              </div>
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

function NodeGroupsEditor({
  groups, setGroups, flavors,
}: {
  groups: NodeGroupRow[]
  setGroups: (g: NodeGroupRow[]) => void
  flavors: Flavor[]
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
                  <SelectValue placeholder={flavors.length ? "Pick a flavor" : "Loading flavors…"} />
                </SelectTrigger>
                <SelectContent>
                  {flavors.map((f, j) => {
                    const id = f.externalId ?? f.id ?? String(j)
                    return (
                      <SelectItem key={id} value={id}>
                        {f.name ?? id}
                      </SelectItem>
                    )
                  })}
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
  pid, scope, resource, versions, flavors, onClose, onDeleted, onChanged,
}: {
  pid: string
  scope: CloudScope | undefined
  resource: CloudResource
  versions: string[]
  flavors: Flavor[]
  onClose: () => void
  onDeleted: () => void
  onChanged: () => void
}) {
  const c = cluster(resource)
  const name = (c.name as string) || resource.externalId || resource.id
  const groups = (c.node_groups as Cluster[]) ?? []
  const [upgradeOpen, setUpgradeOpen] = useState(false)
  const [editOpen, setEditOpen] = useState(false)
  const current = (c.version as string) ?? ""
  const upgradeTargets = versions.filter((v) => v !== current)
  const [target, setTarget] = useState("")

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
      onChanged()
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
              <div className="font-mono text-sm">{current || "—"}</div>
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
            <Button size="sm" variant="outline" onClick={() => setUpgradeOpen(true)} disabled={!upgradeTargets.length}>
              Upgrade
            </Button>
            <Button size="sm" variant="outline" onClick={() => setEditOpen(true)}>
              Edit node groups
            </Button>
            <Button size="sm" variant="destructive" onClick={onDeleted}>
              <Trash2 className="size-4" /> Delete
            </Button>
          </div>

          {(c.oidc_issuer as string) && (
            <p className="text-xs text-muted-foreground">
              OIDC issuer: <span className="font-mono">{c.oidc_issuer as string}</span>
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
            <DialogFooter>
              <Button variant="outline" onClick={() => setUpgradeOpen(false)}>Cancel</Button>
              <Button onClick={() => upgrade.mutate()} disabled={!target || upgrade.isPending}>
                {upgrade.isPending ? "Starting…" : "Upgrade"}
              </Button>
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
              toast.success("Node groups update started")
              setEditOpen(false)
              onChanged()
            }}
          />
        )}
      </SheetContent>
    </Sheet>
  )
}

function EditNodeGroupsDialog({
  initial, flavors, onClose, onSubmit,
}: {
  initial: NodeGroupRow[]
  flavors: Flavor[]
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
