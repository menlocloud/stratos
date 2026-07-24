import { useMemo, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import type { ColumnDef } from "@tanstack/react-table"
import { toast } from "sonner"
import { MoreHorizontal, Network, Plus, RefreshCw, Settings2, Trash2 } from "lucide-react"
import { PageHeader } from "@/components/layout/PageHeader"
import { DataTable, sortableHeader } from "@/components/data-table"
import { EmptyState } from "@/components/empty-state"
import { LoadMore } from "@/components/load-more"
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
import { Skeleton } from "@/components/ui/skeleton"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { apiFetch, type CloudScope } from "@/lib/api"
import { timeAgo } from "@/lib/format"
import { useCloudCursorList, useCloudList, useCloudScope, useProjectId } from "@/lib/hooks"
import type { CloudResource } from "@/lib/types"

function lbName(r: CloudResource): string {
  return (r.data?.loadBalancer?.name as string) ?? r.name ?? r.id
}
function lbVip(r: CloudResource): string {
  return (r.data?.loadBalancer?.vip_address as string) ?? ""
}
function networkName(r: CloudResource): string {
  return (r.data?.network?.name as string) ?? (r.data?.networkName as string) ?? r.name ?? r.id
}

// Octavia sub-resources come back verbatim (gophercloud JSON, snake_case).
type LbMap = Record<string, any>

const LISTENER_PROTOCOLS = ["HTTP", "HTTPS", "TCP", "UDP"]
const POOL_PROTOCOLS = ["HTTP", "HTTPS", "TCP", "UDP", "PROXY"]
const POOL_ALGORITHMS = ["ROUND_ROBIN", "LEAST_CONNECTIONS", "SOURCE_IP"]
const MONITOR_TYPES = ["HTTP", "HTTPS", "TCP", "PING", "UDP-CONNECT"]

export default function LoadBalancersPage() {
  const pid = useProjectId()
  const scope = useCloudScope(pid)
  const qc = useQueryClient()
  const {
    rows: data, isLoading, refetch, isFetching, error, hasNextPage, fetchNextPage, isFetchingNextPage,
  } = useCloudCursorList(pid, "LOAD_BALANCER")
  const networks = useCloudList(pid, "NETWORK")

  const [createOpen, setCreateOpen] = useState(false)
  const [name, setName] = useState("")
  const [networkId, setNetworkId] = useState("")
  const [toDelete, setToDelete] = useState<CloudResource | null>(null)
  const [manageFor, setManageFor] = useState<CloudResource | null>(null)

  const invalidate = () => void qc.invalidateQueries({ queryKey: ["cloud", pid, "LOAD_BALANCER"] })

  const create = useMutation({
    mutationFn: () =>
      apiFetch(`/project/${pid}/cloud`, {
        method: "POST",
        body: { type: "LOAD_BALANCER", data: { name, networkExternalId: networkId } },
        cloud: scope,
      }),
    onSuccess: () => {
      toast.success(`Load balancer "${name}" is being created`)
      setCreateOpen(false)
      setName("")
      setNetworkId("")
      invalidate()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const del = useMutation({
    mutationFn: (id: string) =>
      apiFetch(`/project/${pid}/cloud/${id}`, { method: "DELETE", cloud: scope }),
    onSuccess: () => {
      toast.success("Load balancer deletion requested")
      setToDelete(null)
      setTimeout(invalidate, 1500)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const columns = useMemo<ColumnDef<CloudResource, any>[]>(
    () => [
      {
        id: "name",
        accessorFn: (r) => lbName(r),
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
        id: "provisioning",
        accessorFn: (r) => (r.data?.loadBalancer?.provisioning_status as string) ?? r.status ?? "",
        header: sortableHeader("Provisioning"),
        cell: ({ getValue }) => <StatusBadge status={getValue()} />,
      },
      {
        id: "operating",
        accessorFn: (r) => (r.data?.loadBalancer?.operating_status as string) ?? "",
        header: sortableHeader("Operating"),
        cell: ({ getValue }) => <StatusBadge status={getValue() || undefined} />,
      },
      {
        id: "vip",
        accessorFn: (r) => lbVip(r),
        header: sortableHeader("VIP address"),
        cell: ({ getValue }) => <span className="font-mono text-sm">{getValue() || "—"}</span>,
      },
      {
        id: "created",
        accessorFn: (r) => r.info?.createdAt ?? r.createdAt ?? "",
        header: sortableHeader("Created"),
        cell: ({ getValue }) => (
          <span className="text-sm text-muted-foreground">{timeAgo(getValue())}</span>
        ),
      },
      {
        id: "actions",
        header: () => null,
        enableSorting: false,
        cell: ({ row }) => {
          const r = row.original
          return (
            <div className="text-right" onClick={(e) => e.stopPropagation()}>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="ghost" size="icon-sm" aria-label={`Actions for ${lbName(r)}`}>
                    <MoreHorizontal className="size-4" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem onClick={() => setManageFor(r)}>
                    <Settings2 className="size-4" /> Manage
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem variant="destructive" onClick={() => setToDelete(r)}>
                    <Trash2 className="size-4" /> Delete
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          )
        },
      },
    ],
    // useState setters are stable; helpers are module-scope.
    [],
  )

  return (
    <>
      <PageHeader
        title="Load balancers"
        eyebrow="Network"
        description="Distribute traffic across servers in this project."
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => void refetch()} disabled={isFetching} aria-label="Refresh">
              <RefreshCw className={isFetching ? "size-4 animate-spin" : "size-4"} />
            </Button>
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus className="size-4" /> Create load balancer
            </Button>
          </>
        }
      />

      {!isLoading && !data.length ? (
        <EmptyState
          icon={Network}
          title="No load balancers yet"
          hint="Create a load balancer on one of your networks to distribute traffic."
          action={
            <Button onClick={() => setCreateOpen(true)}>
              <Plus className="size-4" /> Create load balancer
            </Button>
          }
        />
      ) : (
        <>
          <DataTable
            columns={columns}
            data={data}
            isLoading={isLoading}
            error={error as Error | null}
            pagination={false}
            onRowClick={(r) => setManageFor(r)}
          />
          <LoadMore
            hasNextPage={hasNextPage}
            isFetching={isFetchingNextPage}
            onClick={() => void fetchNextPage()}
            count={data.length}
            noun="load balancer"
          />
        </>
      )}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="max-h-[85vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>Create load balancer</DialogTitle>
            <DialogDescription>The VIP is allocated on the network you pick.</DialogDescription>
          </DialogHeader>
          <div className="grid gap-4 py-2">
            <div className="grid gap-2">
              <Label htmlFor="lb-name">Name</Label>
              <Input id="lb-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="web-lb" />
            </div>
            <div className="grid gap-2">
              <Label>Network</Label>
              <Select value={networkId} onValueChange={setNetworkId}>
                <SelectTrigger>
                  <SelectValue placeholder={networks.isLoading ? "Loading networks…" : "Pick a network"} />
                </SelectTrigger>
                <SelectContent>
                  {(networks.data ?? []).map((n) => (
                    <SelectItem key={n.id} value={n.id}>
                      {networkName(n)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>
              Cancel
            </Button>
            <Button onClick={() => create.mutate()} disabled={!name.trim() || !networkId || create.isPending}>
              {create.isPending ? "Creating…" : "Create load balancer"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={!!toDelete} onOpenChange={(o) => !o && setToDelete(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete load balancer</DialogTitle>
            <DialogDescription>
              Delete "{toDelete ? lbName(toDelete) : ""}"? This cascades — its listeners, pools, members and
              health monitors are deleted with it. This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setToDelete(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => toDelete && del.mutate(toDelete.id)}
              disabled={del.isPending}
            >
              {del.isPending ? "Deleting…" : "Delete load balancer"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {manageFor && (
        <LbManageSheet pid={pid} scope={scope} lb={manageFor} onClose={() => setManageFor(null)} />
      )}
    </>
  )
}

// Per-LB manage view: Listeners / Pools (+members) / Monitors over the Octavia
// sub-resource actions (GET_LISTENERS/CREATE_LISTENER/…; Go cloud_writes.go clusterAction).
function LbManageSheet({
  pid, scope, lb, onClose,
}: {
  pid: string
  scope: CloudScope | undefined
  lb: CloudResource
  onClose: () => void
}) {
  const qc = useQueryClient()
  const lbId = lb.id

  const act = (action: string, data?: Record<string, any>) =>
    apiFetch<{ result?: any }>(`/project/${pid}/cloud/${lbId}/action`, {
      method: "POST",
      body: data ? { action, data } : { action },
      cloud: scope,
    })

  const listeners = useQuery({
    queryKey: ["lb-listeners", pid, lbId],
    queryFn: () => act("GET_LISTENERS"),
    enabled: !!scope,
  })
  const pools = useQuery({
    queryKey: ["lb-pools", pid, lbId],
    queryFn: () => act("GET_POOLS"),
    enabled: !!scope,
  })
  const monitors = useQuery({
    queryKey: ["lb-monitors", pid, lbId],
    queryFn: () => act("GET_MONITORS"),
    enabled: !!scope,
  })

  const [listenerOpen, setListenerOpen] = useState(false)
  const [listenerForm, setListenerForm] = useState({ name: "", protocol: "HTTP", port: "80" })
  const [poolOpen, setPoolOpen] = useState(false)
  const [poolForm, setPoolForm] = useState({ name: "", protocol: "HTTP", lbAlgorithm: "ROUND_ROBIN", listenerId: "" })
  const [memberFor, setMemberFor] = useState<LbMap | null>(null)
  const [memberForm, setMemberForm] = useState({ address: "", port: "80" })
  const [monitorOpen, setMonitorOpen] = useState(false)
  const [monitorForm, setMonitorForm] = useState({ poolId: "", type: "HTTP", delay: "5", timeout: "5", maxRetries: "3" })
  const [confirm, setConfirm] = useState<{
    title: string
    description: string
    /** Verb-specific destructive CTA ("Delete listener"), never a bare "Confirm". */
    confirmLabel: string
    action: string
    data: Record<string, any>
    keys: string[]
    success: string
  } | null>(null)

  const run = useMutation({
    mutationFn: (v: { action: string; data?: Record<string, any>; success: string; keys: string[] }) =>
      act(v.action, v.data),
    onSuccess: (_d, v) => {
      toast.success(v.success)
      v.keys.forEach((k) => void qc.invalidateQueries({ queryKey: [k, pid, lbId] }))
      setConfirm(null)
      setListenerOpen(false)
      setPoolOpen(false)
      setMemberFor(null)
      setMonitorOpen(false)
      setListenerForm({ name: "", protocol: "HTTP", port: "80" })
      setPoolForm({ name: "", protocol: "HTTP", lbAlgorithm: "ROUND_ROBIN", listenerId: "" })
      setMemberForm({ address: "", port: "80" })
      setMonitorForm({ poolId: "", type: "HTTP", delay: "5", timeout: "5", maxRetries: "3" })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const listenerList: LbMap[] = listeners.data?.result ?? []
  const poolList: LbMap[] = pools.data?.result ?? []
  const monitorList: LbMap[] = monitors.data?.result ?? []

  const listenerPort = Number(listenerForm.port)
  const listenerValid = !!listenerForm.name.trim() && listenerPort >= 1 && listenerPort <= 65535
  const memberPort = Number(memberForm.port)
  const memberValid = !!memberForm.address.trim() && memberPort >= 1 && memberPort <= 65535
  const monitorValid =
    !!monitorForm.poolId && Number(monitorForm.delay) > 0 && Number(monitorForm.timeout) > 0 && Number(monitorForm.maxRetries) > 0

  const section = (q: { isLoading: boolean; isError: boolean; error: unknown }, empty: string, count: number, table: React.ReactNode) =>
    q.isLoading ? (
      <Skeleton className="h-32" />
    ) : q.isError ? (
      <p className="rounded-lg border bg-card p-3 text-sm text-muted-foreground">{(q.error as Error).message}</p>
    ) : count === 0 ? (
      <p className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">{empty}</p>
    ) : (
      table
    )

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-2xl">
        <SheetHeader className="border-b">
          <div className="text-eyebrow">Load balancer</div>
          <SheetTitle className="font-display text-lg tracking-tight">{lbName(lb)}</SheetTitle>
          <SheetDescription>
            VIP <span className="font-mono">{lbVip(lb) || "—"}</span> — manage listeners, pools, members and
            health monitors.
          </SheetDescription>
        </SheetHeader>

        <div className="px-4 pb-6">
          <Tabs defaultValue="listeners">
            <TabsList>
              <TabsTrigger value="listeners">Listeners</TabsTrigger>
              <TabsTrigger value="pools">Pools</TabsTrigger>
              <TabsTrigger value="monitors">Monitors</TabsTrigger>
            </TabsList>

            <TabsContent value="listeners" className="mt-4 space-y-3">
              <div className="flex justify-end">
                <Button size="sm" onClick={() => setListenerOpen(true)}>
                  <Plus className="size-4" /> Add listener
                </Button>
              </div>
              {section(
                listeners,
                "No listeners configured.",
                listenerList.length,
                <div className="overflow-hidden rounded-xl border bg-card">
                  <Table>
                    <TableHeader>
                      <TableRow className="hover:bg-transparent">
                        <TableHead>Name</TableHead>
                        <TableHead>Protocol</TableHead>
                        <TableHead>Port</TableHead>
                        <TableHead className="w-10" />
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {listenerList.map((l, i) => (
                        <TableRow key={l.id ?? i}>
                          <TableCell className="font-medium">{l.name || l.id || "—"}</TableCell>
                          <TableCell className="text-sm">{l.protocol ?? "—"}</TableCell>
                          <TableCell className="font-mono text-sm">{l.protocol_port ?? "—"}</TableCell>
                          <TableCell className="text-right">
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              aria-label="Delete listener"
                              onClick={() =>
                                setConfirm({
                                  title: "Delete listener",
                                  description: `Delete listener "${l.name || l.id}"? Traffic on port ${l.protocol_port ?? "—"} stops being accepted. This cannot be undone.`,
                                  confirmLabel: "Delete listener",
                                  action: "DELETE_LISTENER",
                                  data: { id: l.id },
                                  keys: ["lb-listeners"],
                                  success: "Listener deleted",
                                })
                              }
                            >
                              <Trash2 className="size-4 text-muted-foreground" />
                            </Button>
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>,
              )}
            </TabsContent>

            <TabsContent value="pools" className="mt-4 space-y-3">
              <div className="flex justify-end">
                <Button size="sm" onClick={() => setPoolOpen(true)}>
                  <Plus className="size-4" /> Add pool
                </Button>
              </div>
              {section(
                pools,
                "No pools configured. A pool attaches to a listener and holds the backend members.",
                poolList.length,
                <div className="space-y-4">
                  {poolList.map((p, i) => (
                    <div key={p.id ?? i} className="overflow-hidden rounded-xl border bg-card">
                      <div className="flex items-center justify-between gap-2 border-b px-3 py-2">
                        <div>
                          <span className="text-sm font-medium">{p.name || p.id}</span>
                          <span className="ml-2 text-xs text-muted-foreground">
                            {p.protocol ?? "—"} · {(p.lb_algorithm as string | undefined)?.replaceAll("_", " ").toLowerCase() ?? "—"}
                          </span>
                        </div>
                        <div className="flex gap-1">
                          <Button
                            variant="outline"
                            size="sm"
                            onClick={() => {
                              setMemberFor(p)
                              setMemberForm({ address: "", port: "80" })
                            }}
                          >
                            <Plus className="size-4" /> Add member
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            aria-label="Delete pool"
                            onClick={() =>
                              setConfirm({
                                title: "Delete pool",
                                description: `Delete pool "${p.name || p.id}" and its members? Its listener stops routing traffic. This cannot be undone.`,
                                confirmLabel: "Delete pool",
                                action: "DELETE_POOL",
                                data: { id: p.id },
                                keys: ["lb-pools"],
                                success: "Pool deleted",
                              })
                            }
                          >
                            <Trash2 className="size-4 text-muted-foreground" />
                          </Button>
                        </div>
                      </div>
                      {!(p.members as LbMap[] | undefined)?.length ? (
                        <p className="px-3 py-3 text-sm text-muted-foreground">No members in this pool.</p>
                      ) : (
                        <Table>
                          <TableHeader>
                            <TableRow className="hover:bg-transparent">
                              <TableHead>Address</TableHead>
                              <TableHead>Port</TableHead>
                              <TableHead>Status</TableHead>
                              <TableHead className="w-10" />
                            </TableRow>
                          </TableHeader>
                          <TableBody>
                            {(p.members as LbMap[]).map((m, j) => (
                              <TableRow key={m.id ?? j}>
                                <TableCell className="font-mono text-sm">{m.address ?? "—"}</TableCell>
                                <TableCell className="font-mono text-sm">{m.protocol_port ?? "—"}</TableCell>
                                <TableCell>
                                  <StatusBadge status={(m.operating_status as string) ?? undefined} />
                                </TableCell>
                                <TableCell className="text-right">
                                  <Button
                                    variant="ghost"
                                    size="icon-sm"
                                    aria-label="Remove member"
                                    onClick={() =>
                                      setConfirm({
                                        title: "Remove member",
                                        description: `Remove member ${m.address}:${m.protocol_port} from "${p.name || p.id}"? It stops receiving traffic immediately.`,
                                        confirmLabel: "Remove member",
                                        action: "DELETE_MEMBER",
                                        data: { poolId: p.id, id: m.id },
                                        keys: ["lb-pools"],
                                        success: "Member removed",
                                      })
                                    }
                                  >
                                    <Trash2 className="size-4 text-muted-foreground" />
                                  </Button>
                                </TableCell>
                              </TableRow>
                            ))}
                          </TableBody>
                        </Table>
                      )}
                    </div>
                  ))}
                </div>,
              )}
            </TabsContent>

            <TabsContent value="monitors" className="mt-4 space-y-3">
              <div className="flex justify-end">
                <Button size="sm" onClick={() => setMonitorOpen(true)} disabled={!poolList.length}>
                  <Plus className="size-4" /> Add monitor
                </Button>
              </div>
              {!poolList.length && (
                <p className="text-xs text-muted-foreground">A health monitor attaches to a pool — create a pool first.</p>
              )}
              {section(
                monitors,
                "No health monitors configured.",
                monitorList.length,
                <div className="overflow-hidden rounded-xl border bg-card">
                  <Table>
                    <TableHeader>
                      <TableRow className="hover:bg-transparent">
                        <TableHead>Name</TableHead>
                        <TableHead>Type</TableHead>
                        <TableHead>Delay</TableHead>
                        <TableHead>Timeout</TableHead>
                        <TableHead>Retries</TableHead>
                        <TableHead className="w-10" />
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {monitorList.map((m, i) => (
                        <TableRow key={m.id ?? i}>
                          <TableCell className="font-medium">{m.name || m.id || "—"}</TableCell>
                          <TableCell className="text-sm">{m.type ?? "—"}</TableCell>
                          <TableCell className="font-mono text-sm">{m.delay != null ? `${m.delay}s` : "—"}</TableCell>
                          <TableCell className="font-mono text-sm">{m.timeout != null ? `${m.timeout}s` : "—"}</TableCell>
                          <TableCell className="font-mono text-sm">{m.max_retries ?? "—"}</TableCell>
                          <TableCell className="text-right">
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              aria-label="Delete monitor"
                              onClick={() =>
                                setConfirm({
                                  title: "Delete health monitor",
                                  description: `Delete monitor "${m.name || m.id}"? Unhealthy members are no longer ejected from the pool. This cannot be undone.`,
                                  confirmLabel: "Delete monitor",
                                  action: "DELETE_MONITOR",
                                  data: { id: m.id },
                                  keys: ["lb-monitors"],
                                  success: "Monitor deleted",
                                })
                              }
                            >
                              <Trash2 className="size-4 text-muted-foreground" />
                            </Button>
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>,
              )}
            </TabsContent>
          </Tabs>
        </div>

        {/* Add listener */}
        <Dialog open={listenerOpen} onOpenChange={setListenerOpen}>
          <DialogContent className="max-h-[85vh] overflow-y-auto">
            <DialogHeader>
              <DialogTitle>Add listener</DialogTitle>
              <DialogDescription>A listener accepts traffic on the VIP at the given port.</DialogDescription>
            </DialogHeader>
            <div className="grid gap-4 py-2">
              <div className="grid gap-2">
                <Label htmlFor="lst-name">Name</Label>
                <Input
                  id="lst-name"
                  value={listenerForm.name}
                  onChange={(e) => setListenerForm({ ...listenerForm, name: e.target.value })}
                  placeholder="http-listener"
                />
              </div>
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                <div className="grid gap-2">
                  <Label>Protocol</Label>
                  <Select
                    value={listenerForm.protocol}
                    onValueChange={(v) => setListenerForm({ ...listenerForm, protocol: v })}
                  >
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {LISTENER_PROTOCOLS.map((p) => (
                        <SelectItem key={p} value={p}>
                          {p}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="lst-port">Port</Label>
                  <Input
                    id="lst-port"
                    type="number"
                    min={1}
                    max={65535}
                    value={listenerForm.port}
                    onChange={(e) => setListenerForm({ ...listenerForm, port: e.target.value })}
                  />
                </div>
              </div>
            </div>
            <DialogFooter>
              <Button variant="outline" onClick={() => setListenerOpen(false)}>
                Cancel
              </Button>
              <Button
                onClick={() =>
                  run.mutate({
                    action: "CREATE_LISTENER",
                    data: {
                      name: listenerForm.name.trim(),
                      protocol: listenerForm.protocol,
                      listenerPort: listenerPort,
                    },
                    success: "Listener created",
                    keys: ["lb-listeners"],
                  })
                }
                disabled={!listenerValid || run.isPending}
              >
                {run.isPending ? "Creating…" : "Add listener"}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>

        {/* Add pool */}
        <Dialog open={poolOpen} onOpenChange={setPoolOpen}>
          <DialogContent className="max-h-[85vh] overflow-y-auto">
            <DialogHeader>
              <DialogTitle>Add pool</DialogTitle>
              <DialogDescription>A pool groups backend members behind a listener.</DialogDescription>
            </DialogHeader>
            <div className="grid gap-4 py-2">
              <div className="grid gap-2">
                <Label htmlFor="pool-name">Name</Label>
                <Input
                  id="pool-name"
                  value={poolForm.name}
                  onChange={(e) => setPoolForm({ ...poolForm, name: e.target.value })}
                  placeholder="web-pool"
                />
              </div>
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                <div className="grid gap-2">
                  <Label>Protocol</Label>
                  <Select value={poolForm.protocol} onValueChange={(v) => setPoolForm({ ...poolForm, protocol: v })}>
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {POOL_PROTOCOLS.map((p) => (
                        <SelectItem key={p} value={p}>
                          {p}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className="grid gap-2">
                  <Label>Algorithm</Label>
                  <Select
                    value={poolForm.lbAlgorithm}
                    onValueChange={(v) => setPoolForm({ ...poolForm, lbAlgorithm: v })}
                  >
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {POOL_ALGORITHMS.map((a) => (
                        <SelectItem key={a} value={a}>
                          {a.replaceAll("_", " ").toLowerCase()}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              </div>
              <div className="grid gap-2">
                <Label>Listener</Label>
                <Select value={poolForm.listenerId} onValueChange={(v) => setPoolForm({ ...poolForm, listenerId: v })}>
                  <SelectTrigger>
                    <SelectValue placeholder={listenerList.length ? "Pick a listener" : "No listeners — create one first"} />
                  </SelectTrigger>
                  <SelectContent>
                    {listenerList.map((l, i) => (
                      <SelectItem key={l.id ?? i} value={l.id}>
                        {l.name || l.id} ({l.protocol}:{l.protocol_port})
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </div>
            <DialogFooter>
              <Button variant="outline" onClick={() => setPoolOpen(false)}>
                Cancel
              </Button>
              <Button
                onClick={() =>
                  run.mutate({
                    action: "CREATE_POOL",
                    data: {
                      name: poolForm.name.trim(),
                      protocol: poolForm.protocol,
                      lbAlgorithm: poolForm.lbAlgorithm,
                      listenerId: poolForm.listenerId,
                    },
                    success: "Pool created",
                    keys: ["lb-pools"],
                  })
                }
                disabled={!poolForm.name.trim() || !poolForm.listenerId || run.isPending}
              >
                {run.isPending ? "Creating…" : "Add pool"}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>

        {/* Add member */}
        <Dialog open={!!memberFor} onOpenChange={(o) => !o && setMemberFor(null)}>
          <DialogContent className="max-h-[85vh] overflow-y-auto">
            <DialogHeader>
              <DialogTitle>Add member</DialogTitle>
              <DialogDescription>
                Add a backend to pool "{memberFor ? memberFor.name || memberFor.id : ""}" by IP address and port.
              </DialogDescription>
            </DialogHeader>
            <div className="grid grid-cols-1 gap-4 py-2 sm:grid-cols-2">
              <div className="grid gap-2">
                <Label htmlFor="mem-addr">IP address</Label>
                <Input
                  id="mem-addr"
                  value={memberForm.address}
                  onChange={(e) => setMemberForm({ ...memberForm, address: e.target.value })}
                  placeholder="10.0.0.12"
                  className="font-mono"
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="mem-port">Port</Label>
                <Input
                  id="mem-port"
                  type="number"
                  min={1}
                  max={65535}
                  value={memberForm.port}
                  onChange={(e) => setMemberForm({ ...memberForm, port: e.target.value })}
                />
              </div>
            </div>
            <DialogFooter>
              <Button variant="outline" onClick={() => setMemberFor(null)}>
                Cancel
              </Button>
              <Button
                onClick={() =>
                  memberFor &&
                  run.mutate({
                    action: "ADD_MEMBER",
                    data: { poolId: memberFor.id, address: memberForm.address.trim(), memberPort: memberPort },
                    success: "Member added",
                    keys: ["lb-pools"],
                  })
                }
                disabled={!memberValid || run.isPending}
              >
                {run.isPending ? "Adding…" : "Add member"}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>

        {/* Add monitor */}
        <Dialog open={monitorOpen} onOpenChange={setMonitorOpen}>
          <DialogContent className="max-h-[85vh] overflow-y-auto">
            <DialogHeader>
              <DialogTitle>Add health monitor</DialogTitle>
              <DialogDescription>Octavia probes the pool's members and ejects unhealthy ones.</DialogDescription>
            </DialogHeader>
            <div className="grid gap-4 py-2">
              <div className="grid gap-2">
                <Label>Pool</Label>
                <Select value={monitorForm.poolId} onValueChange={(v) => setMonitorForm({ ...monitorForm, poolId: v })}>
                  <SelectTrigger>
                    <SelectValue placeholder="Pick a pool" />
                  </SelectTrigger>
                  <SelectContent>
                    {poolList.map((p, i) => (
                      <SelectItem key={p.id ?? i} value={p.id}>
                        {p.name || p.id}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                <div className="grid gap-2">
                  <Label>Type</Label>
                  <Select value={monitorForm.type} onValueChange={(v) => setMonitorForm({ ...monitorForm, type: v })}>
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {MONITOR_TYPES.map((t) => (
                        <SelectItem key={t} value={t}>
                          {t}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="mon-delay">Delay (s)</Label>
                  <Input
                    id="mon-delay"
                    type="number"
                    min={1}
                    value={monitorForm.delay}
                    onChange={(e) => setMonitorForm({ ...monitorForm, delay: e.target.value })}
                  />
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="mon-timeout">Timeout (s)</Label>
                  <Input
                    id="mon-timeout"
                    type="number"
                    min={1}
                    value={monitorForm.timeout}
                    onChange={(e) => setMonitorForm({ ...monitorForm, timeout: e.target.value })}
                  />
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="mon-retries">Max retries</Label>
                  <Input
                    id="mon-retries"
                    type="number"
                    min={1}
                    max={10}
                    value={monitorForm.maxRetries}
                    onChange={(e) => setMonitorForm({ ...monitorForm, maxRetries: e.target.value })}
                  />
                </div>
              </div>
            </div>
            <DialogFooter>
              <Button variant="outline" onClick={() => setMonitorOpen(false)}>
                Cancel
              </Button>
              <Button
                onClick={() =>
                  run.mutate({
                    action: "ADD_MONITOR",
                    data: {
                      poolId: monitorForm.poolId,
                      type: monitorForm.type,
                      protocol: monitorForm.type,
                      delay: Number(monitorForm.delay),
                      timeout: Number(monitorForm.timeout),
                      maxRetries: Number(monitorForm.maxRetries),
                    },
                    success: "Health monitor created",
                    keys: ["lb-monitors"],
                  })
                }
                disabled={!monitorValid || run.isPending}
              >
                {run.isPending ? "Creating…" : "Add monitor"}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>

        {/* Delete confirms */}
        <Dialog open={!!confirm} onOpenChange={(o) => !o && setConfirm(null)}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>{confirm?.title}</DialogTitle>
              <DialogDescription>{confirm?.description}</DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button variant="outline" onClick={() => setConfirm(null)}>
                Cancel
              </Button>
              <Button
                variant="destructive"
                onClick={() =>
                  confirm &&
                  run.mutate({ action: confirm.action, data: confirm.data, success: confirm.success, keys: confirm.keys })
                }
                disabled={run.isPending}
              >
                {run.isPending ? "Working…" : (confirm?.confirmLabel ?? "Confirm")}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </SheetContent>
    </Sheet>
  )
}
