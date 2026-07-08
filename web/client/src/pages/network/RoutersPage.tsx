import { useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"
import { Plus, RefreshCw, Route, Settings2, Trash2 } from "lucide-react"
import { PageHeader } from "@/components/layout/PageHeader"
import { EmptyState } from "@/components/empty-state"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { Separator } from "@/components/ui/separator"
import {
  Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle,
} from "@/components/ui/sheet"
import { Skeleton } from "@/components/ui/skeleton"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { apiFetch } from "@/lib/api"
import { timeAgo } from "@/lib/format"
import { useCloudList, useCloudScope, useProject, useProjectId } from "@/lib/hooks"
import type { CloudResource } from "@/lib/types"
import { networkName } from "./NetworksPage"

function routerName(r: CloudResource): string {
  return (r.data?.routerName as string) || (r.data?.router?.name as string) || r.name || r.id
}
function routerStatus(r: CloudResource): string | undefined {
  return (r.data?.router?.status as string) ?? r.status
}

const NONE = "__none__"

export default function RoutersPage() {
  const pid = useProjectId()
  const scope = useCloudScope(pid)
  const qc = useQueryClient()
  // publicNetworksVisible=false → hide the gateway picker; the server auto-picks the external network.
  const netsVisible = useProject(pid).project?.publicNetworksVisible === true
  const { data, isLoading, refetch, isFetching, error } = useCloudList(pid, "ROUTER")
  const { data: networks } = useCloudList(pid, "NETWORK")
  const { data: ports } = useCloudList(pid, "PORT")
  const [createOpen, setCreateOpen] = useState(false)
  const [toDelete, setToDelete] = useState<CloudResource | null>(null)

  const [name, setName] = useState("")
  const [extNet, setExtNet] = useState(NONE)

  // Manage sheet — keep the id (not the row object) so the sheet re-renders fresh data after
  // a mutation invalidates the list.
  const [manageId, setManageId] = useState<string | null>(null)
  const manage = (data ?? []).find((x) => x.id === manageId) ?? null
  const [addSubnet, setAddSubnet] = useState(NONE)
  const [gwNet, setGwNet] = useState(NONE)
  const [confirmClearGw, setConfirmClearGw] = useState(false)

  // External gateway candidates — networks flagged router:external in the cache.
  const externalNets = (networks ?? []).filter((n) => n.data?.network?.["router:external"] === true)

  // Subnet candidates for ADD_INTERFACE — subnets have no cache docs of their own; their ids live
  // on the parent NETWORK doc (data.network.subnets). We send the raw subnet UUID (its neutron
  // external id); the backend passes it straight to neutron, which enforces the tenant scope.
  const subnetOptions = (networks ?? []).flatMap((n) => {
    const subs = (n.data?.network?.subnets as string[] | undefined) ?? []
    return subs.map((s) => ({ id: s, label: `${networkName(n)} — ${s.slice(0, 8)}…` }))
  })

  // The router's interfaces = the project's ports whose device_id is this router.
  const routerPorts = manage
    ? (ports ?? []).filter((p) => (p.data?.port?.device_id as string) === manage.externalId)
    : []

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ["cloud", pid, "ROUTER"] })
    void qc.invalidateQueries({ queryKey: ["cloud", pid, "PORT"] })
  }

  // One mutation for all router actions (POST /cloud/{id}/action). Contracts (providers/write.go):
  // ADD_INTERFACE data{interfaceId: subnet UUID} · DELETE_INTERFACE data{interfaceId: port UUID} ·
  // ADD_EXTERNAL_GATEWAY data{networkId: network UUID, not resolved — must be the externalId} ·
  // DELETE_EXTERNAL_GATEWAY (no data).
  const act = useMutation({
    mutationFn: (v: { router: CloudResource; action: string; data?: Record<string, unknown>; ok: string }) =>
      apiFetch(`/project/${pid}/cloud/${v.router.id}/action`, {
        method: "POST",
        cloud: scope,
        body: { action: v.action, ...(v.data ? { data: v.data } : {}) },
      }),
    onSuccess: (_d, v) => {
      toast.success(v.ok)
      setAddSubnet(NONE)
      setGwNet(NONE)
      setConfirmClearGw(false)
      invalidate()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const create = useMutation({
    mutationFn: () => {
      const data: Record<string, unknown> = { name }
      if (extNet !== NONE) data.externalNetworkId = extNet
      return apiFetch(`/project/${pid}/cloud`, {
        method: "POST",
        cloud: scope,
        body: { type: "ROUTER", data },
      })
    },
    onSuccess: () => {
      toast.success(`Router "${name}" created`)
      setCreateOpen(false)
      setName("")
      setExtNet(NONE)
      invalidate()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const del = useMutation({
    mutationFn: (r: CloudResource) =>
      apiFetch(`/project/${pid}/cloud/${r.id}`, { method: "DELETE", cloud: scope }),
    onSuccess: () => {
      toast.success("Router deletion requested")
      setToDelete(null)
      setTimeout(invalidate, 1500)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <>
      <PageHeader
        title="Routers"
        description="Routers connecting your networks."
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => void refetch()} disabled={isFetching}>
              <RefreshCw className={isFetching ? "size-4 animate-spin" : "size-4"} />
            </Button>
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus className="size-4" /> Create router
            </Button>
          </>
        }
      />

      {isLoading ? (
        <Skeleton className="h-64" />
      ) : error ? (
        <div className="rounded-lg border bg-muted/40 p-6 text-sm text-muted-foreground">{(error as Error).message}</div>
      ) : !data?.length ? (
        <EmptyState
          icon={Route}
          title="No routers yet"
          hint="Create a router to route traffic between networks or out to the internet."
          action={
            <Button onClick={() => setCreateOpen(true)}>
              <Plus className="size-4" /> Create router
            </Button>
          }
        />
      ) : (
        <Card className="overflow-hidden py-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>External gateway</TableHead>
                <TableHead>Created</TableHead>
                <TableHead className="w-20" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.map((r) => {
                const gw = r.data?.router?.external_gateway_info as { network_id?: string } | undefined
                return (
                  <TableRow key={r.id}>
                    <TableCell className="font-medium">{routerName(r)}</TableCell>
                    <TableCell>
                      <StatusBadge status={routerStatus(r)} />
                    </TableCell>
                    <TableCell className="text-sm text-muted-foreground">{gw?.network_id ? "yes" : "—"}</TableCell>
                    <TableCell className="text-sm text-muted-foreground">
                      {timeAgo(r.info?.createdAt ?? r.createdAt)}
                    </TableCell>
                    <TableCell>
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="icon"
                          onClick={() => setManageId(r.id)}
                          aria-label="Manage router"
                          title="Manage interfaces and gateway"
                        >
                          <Settings2 className="size-4 text-muted-foreground" />
                        </Button>
                        <Button variant="ghost" size="icon" onClick={() => setToDelete(r)} aria-label="Delete router">
                          <Trash2 className="size-4 text-muted-foreground" />
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </Card>
      )}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Create router</DialogTitle>
            <DialogDescription>Optionally attach an external gateway network.</DialogDescription>
          </DialogHeader>
          <div className="grid gap-4 py-2">
            <div className="grid gap-2">
              <Label htmlFor="router-name">Name</Label>
              <Input id="router-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="my-router" />
            </div>
            {netsVisible ? (
              <div className="grid gap-2">
                <Label>External network (optional)</Label>
                <Select value={extNet} onValueChange={setExtNet}>
                  <SelectTrigger>
                    <SelectValue placeholder="None" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={NONE}>None</SelectItem>
                    {externalNets.map((n) => {
                      const ext = n.externalId ?? (n.data?.network?.id as string) ?? n.id
                      return (
                        <SelectItem key={n.id} value={ext}>
                          {networkName(n)}
                        </SelectItem>
                      )
                    })}
                  </SelectContent>
                </Select>
              </div>
            ) : (
              <p className="text-sm text-muted-foreground">
                An external gateway will be assigned automatically.
              </p>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>
              Cancel
            </Button>
            <Button onClick={() => create.mutate()} disabled={!name || create.isPending}>
              {create.isPending ? "Creating…" : "Create router"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Sheet open={!!manage} onOpenChange={(o) => !o && setManageId(null)}>
        <SheetContent className="w-full overflow-y-auto sm:max-w-md">
          <SheetHeader>
            <SheetTitle>{manage ? routerName(manage) : "Router"}</SheetTitle>
            <SheetDescription>Manage this router's interfaces and external gateway.</SheetDescription>
          </SheetHeader>
          {manage && (
            <div className="grid gap-6 px-4 pb-6">
              <section className="grid gap-3">
                <h3 className="text-sm font-medium">Interfaces</h3>
                {routerPorts.length === 0 ? (
                  <p className="text-sm text-muted-foreground">No interfaces attached.</p>
                ) : (
                  <div className="grid gap-2">
                    {routerPorts.map((p) => {
                      const ips = ((p.data?.port?.fixed_ips as Array<{ ip_address?: string }>) ?? [])
                        .map((f) => f.ip_address)
                        .filter(Boolean)
                        .join(", ")
                      const portExt = (p.data?.port?.id as string) ?? p.externalId ?? p.id
                      return (
                        <div key={p.id} className="flex items-center justify-between rounded-md border px-3 py-2">
                          <div className="min-w-0">
                            <div className="truncate font-mono text-xs">{portExt}</div>
                            <div className="text-xs text-muted-foreground">{ips || "no IP"}</div>
                          </div>
                          <Button
                            variant="ghost"
                            size="sm"
                            disabled={act.isPending}
                            onClick={() =>
                              act.mutate({
                                router: manage,
                                action: "DELETE_INTERFACE",
                                data: { interfaceId: portExt },
                                ok: "Interface removed",
                              })
                            }
                          >
                            Remove
                          </Button>
                        </div>
                      )
                    })}
                  </div>
                )}
                <div className="flex items-end gap-2">
                  <div className="grid flex-1 gap-2">
                    <Label>Add interface (subnet)</Label>
                    <Select value={addSubnet} onValueChange={setAddSubnet}>
                      <SelectTrigger>
                        <SelectValue placeholder="Select a subnet" />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value={NONE}>Select a subnet</SelectItem>
                        {subnetOptions.map((s) => (
                          <SelectItem key={s.id} value={s.id}>
                            {s.label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                  <Button
                    disabled={addSubnet === NONE || act.isPending}
                    onClick={() =>
                      act.mutate({
                        router: manage,
                        action: "ADD_INTERFACE",
                        data: { interfaceId: addSubnet },
                        ok: "Interface added",
                      })
                    }
                  >
                    Add
                  </Button>
                </div>
              </section>

              <Separator />

              <section className="grid gap-3">
                <h3 className="text-sm font-medium">External gateway</h3>
                {(() => {
                  const gwNetId = (
                    manage.data?.router?.external_gateway_info as { network_id?: string } | undefined
                  )?.network_id
                  if (gwNetId) {
                    const net = (networks ?? []).find((n) => n.externalId === gwNetId)
                    return (
                      <div className="flex items-center justify-between rounded-md border px-3 py-2">
                        <div className="min-w-0">
                          <div className="truncate text-sm">{net ? networkName(net) : "External network"}</div>
                          <div className="truncate font-mono text-xs text-muted-foreground">{gwNetId}</div>
                        </div>
                        <Button variant="destructive" size="sm" onClick={() => setConfirmClearGw(true)}>
                          Clear
                        </Button>
                      </div>
                    )
                  }
                  return (
                    <>
                      <p className="text-sm text-muted-foreground">No external gateway set.</p>
                      <div className="flex items-end gap-2">
                        <div className="grid flex-1 gap-2">
                          <Label>External network</Label>
                          {externalNets.length ? (
                            <Select value={gwNet} onValueChange={setGwNet}>
                              <SelectTrigger>
                                <SelectValue placeholder="Select a network" />
                              </SelectTrigger>
                              <SelectContent>
                                <SelectItem value={NONE}>Select a network</SelectItem>
                                {externalNets.map((n) => {
                                  const ext = n.externalId ?? (n.data?.network?.id as string) ?? n.id
                                  return (
                                    <SelectItem key={n.id} value={ext}>
                                      {networkName(n)}
                                    </SelectItem>
                                  )
                                })}
                              </SelectContent>
                            </Select>
                          ) : (
                            <Input
                              className="font-mono"
                              placeholder="External network id"
                              value={gwNet === NONE ? "" : gwNet}
                              onChange={(e) => setGwNet(e.target.value || NONE)}
                            />
                          )}
                        </div>
                        <Button
                          disabled={gwNet === NONE || act.isPending}
                          onClick={() =>
                            act.mutate({
                              router: manage,
                              action: "ADD_EXTERNAL_GATEWAY",
                              data: { networkId: gwNet },
                              ok: "External gateway set",
                            })
                          }
                        >
                          Set
                        </Button>
                      </div>
                    </>
                  )
                })()}
              </section>
            </div>
          )}
        </SheetContent>
      </Sheet>

      <Dialog open={confirmClearGw} onOpenChange={setConfirmClearGw}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Clear external gateway</DialogTitle>
            <DialogDescription>
              Remove the external gateway from "{manage ? routerName(manage) : ""}"? Instances routed through it
              lose internet access.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmClearGw(false)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              disabled={act.isPending}
              onClick={() =>
                manage &&
                act.mutate({ router: manage, action: "DELETE_EXTERNAL_GATEWAY", ok: "External gateway cleared" })
              }
            >
              {act.isPending ? "Clearing…" : "Clear gateway"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={!!toDelete} onOpenChange={(o) => !o && setToDelete(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete router</DialogTitle>
            <DialogDescription>
              Delete router "{toDelete ? routerName(toDelete) : ""}"? This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setToDelete(null)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={() => toDelete && del.mutate(toDelete)} disabled={del.isPending}>
              {del.isPending ? "Deleting…" : "Delete router"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
