import { useMemo, useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import type { ColumnDef } from "@tanstack/react-table"
import { toast } from "sonner"
import { Globe, Link2, Link2Off, MoreHorizontal, Plus, RefreshCw, Trash2 } from "lucide-react"
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
import { apiFetch } from "@/lib/api"
import { useCloudList, useCloudScope, useProject, useProjectId, usePublicNetworks } from "@/lib/hooks"
import type { CloudResource } from "@/lib/types"

function fipAddress(r: CloudResource): string {
  return (r.data?.floatingIp?.floating_ip_address as string) || r.name || r.id
}
function fipAssigned(r: CloudResource): boolean {
  return !!(r.data?.floatingIp?.port_id as string | undefined)
}

export default function FloatingIPsPage() {
  const pid = useProjectId()
  const scope = useCloudScope(pid)
  const qc = useQueryClient()
  const { data, isLoading, refetch, isFetching, error } = useCloudList(pid, "FLOATING_IP")
  const { data: ports } = useCloudList(pid, "PORT")
  const [createOpen, setCreateOpen] = useState(false)
  const [toDelete, setToDelete] = useState<CloudResource | null>(null)
  const [toAssign, setToAssign] = useState<CloudResource | null>(null)

  // create form: external networks from GET /project/{pid}/public-networks (already filtered by
  // the project's allow-list server-side), with a manual-ID fallback for anything not listed.
  const [netSelect, setNetSelect] = useState("")
  const [netManual, setNetManual] = useState("")
  const [assignPort, setAssignPort] = useState("")

  const externalNets = usePublicNetworks(pid, scope).data ?? []
  // publicNetworksVisible=false → hide the pool picker; the server auto-picks the external network.
  const netsVisible = useProject(pid).project?.publicNetworksVisible === true

  const invalidate = () => void qc.invalidateQueries({ queryKey: ["cloud", pid, "FLOATING_IP"] })

  const create = useMutation({
    mutationFn: () =>
      apiFetch(`/project/${pid}/cloud`, {
        method: "POST",
        cloud: scope,
        // Hidden picker → no networkId; the server auto-selects the pool.
        body: { type: "FLOATING_IP", data: netsVisible ? { networkId: netManual || netSelect } : {} },
      }),
    onSuccess: () => {
      toast.success("Floating IP allocated")
      setCreateOpen(false)
      setNetSelect("")
      setNetManual("")
      invalidate()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const act = useMutation({
    mutationFn: ({ r, action, data }: { r: CloudResource; action: string; data?: Record<string, unknown> }) =>
      apiFetch(`/project/${pid}/cloud/${r.id}/action`, {
        method: "POST",
        cloud: scope,
        body: { action, data },
      }),
    onSuccess: (_d, { action }) => {
      toast.success(action === "ASSIGN" ? "Floating IP assigned" : "Floating IP unassigned")
      setToAssign(null)
      setAssignPort("")
      invalidate()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const del = useMutation({
    mutationFn: (r: CloudResource) =>
      apiFetch(`/project/${pid}/cloud/${r.id}`, { method: "DELETE", cloud: scope }),
    onSuccess: () => {
      toast.success("Floating IP release requested")
      setToDelete(null)
      setTimeout(invalidate, 1500)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const columns = useMemo<ColumnDef<CloudResource, any>[]>(
    () => [
      {
        id: "address",
        accessorFn: (r) => fipAddress(r),
        header: sortableHeader("Address"),
        cell: ({ getValue }) => <span className="font-mono font-medium">{getValue()}</span>,
      },
      {
        id: "status",
        accessorFn: (r) => (r.data?.floatingIp?.status as string) ?? r.status ?? "",
        header: sortableHeader("Status"),
        cell: ({ getValue }) => <StatusBadge status={getValue()} />,
      },
      {
        id: "fixedIp",
        accessorFn: (r) => (r.data?.floatingIp?.fixed_ip_address as string) ?? "",
        header: sortableHeader("Mapped fixed IP"),
        cell: ({ getValue }) => <span className="font-mono text-sm">{getValue() || "—"}</span>,
      },
      {
        id: "port",
        accessorFn: (r) => (r.data?.floatingIp?.port_id as string) ?? "",
        header: "Port",
        cell: ({ getValue }) => (
          <span className="font-mono text-xs text-muted-foreground">{getValue() || "—"}</span>
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
                  <Button variant="ghost" size="icon-sm" aria-label={`Actions for ${fipAddress(r)}`}>
                    <MoreHorizontal className="size-4" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  {fipAssigned(r) ? (
                    <DropdownMenuItem onClick={() => act.mutate({ r, action: "UNASSIGN" })}>
                      <Link2Off className="size-4" /> Unassign
                    </DropdownMenuItem>
                  ) : (
                    <DropdownMenuItem onClick={() => setToAssign(r)}>
                      <Link2 className="size-4" /> Assign to port
                    </DropdownMenuItem>
                  )}
                  <DropdownMenuSeparator />
                  <DropdownMenuItem variant="destructive" onClick={() => setToDelete(r)}>
                    <Trash2 className="size-4" /> Release
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          )
        },
      },
    ],
    // act is a stable useMutation object; setters are stable.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  )

  return (
    <>
      <PageHeader
        title="Floating IPs"
        eyebrow="Network"
        description="Public IP addresses mapped onto your instances."
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => void refetch()} disabled={isFetching}>
              <RefreshCw className={isFetching ? "size-4 animate-spin" : "size-4"} />
            </Button>
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus className="size-4" /> Allocate floating IP
            </Button>
          </>
        }
      />

      {!isLoading && !error && !data?.length ? (
        <EmptyState
          icon={Globe}
          title="No floating IPs"
          hint="Allocate a public IP from an external network and assign it to a port."
          action={
            <Button onClick={() => setCreateOpen(true)}>
              <Plus className="size-4" /> Allocate floating IP
            </Button>
          }
        />
      ) : (
        <DataTable
          columns={columns}
          data={data}
          isLoading={isLoading}
          error={error ? (error as Error) : null}
          searchPlaceholder="Search floating IPs…"
        />
      )}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Allocate floating IP</DialogTitle>
            <DialogDescription>
              {netsVisible ? "Pick the external network pool to allocate from." : "Allocate a public IP address."}
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-4 py-2">
            {netsVisible ? (
              <>
                <div className="grid gap-2">
                  <Label>External network</Label>
                  <Select value={netSelect} onValueChange={setNetSelect}>
                    <SelectTrigger>
                      <SelectValue
                        placeholder={
                          externalNets.length
                            ? "Select an external network"
                            : "No public networks are enabled for this project"
                        }
                      />
                    </SelectTrigger>
                    <SelectContent>
                      {externalNets.map((n) => (
                        <SelectItem key={n.id} value={n.id}>
                          {n.name || n.id}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="fip-net">Or enter a network ID manually</Label>
                  <Input
                    id="fip-net"
                    className="font-mono"
                    value={netManual}
                    onChange={(e) => setNetManual(e.target.value)}
                    placeholder="network UUID"
                  />
                </div>
              </>
            ) : (
              <p className="text-sm text-muted-foreground">
                A public network will be chosen automatically for this address.
              </p>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>
              Cancel
            </Button>
            <Button
              onClick={() => create.mutate()}
              disabled={(netsVisible && !netSelect && !netManual) || create.isPending}
            >
              {create.isPending ? "Allocating…" : "Allocate floating IP"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={!!toAssign} onOpenChange={(o) => !o && setToAssign(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Assign floating IP</DialogTitle>
            <DialogDescription>
              Map <span className="font-mono">{toAssign ? fipAddress(toAssign) : ""}</span> onto a port (a
              server's network interface).
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-2 py-2">
            <Label>Port</Label>
            <Select value={assignPort} onValueChange={setAssignPort}>
              <SelectTrigger>
                <SelectValue placeholder="Select a port" />
              </SelectTrigger>
              <SelectContent>
                {(ports ?? []).map((p) => {
                  const ips = ((p.data?.port?.fixed_ips as Array<{ ip_address?: string }> | undefined) ?? [])
                    .map((f) => f.ip_address)
                    .filter(Boolean)
                    .join(", ")
                  return (
                    <SelectItem key={p.id} value={p.id}>
                      {(p.data?.port?.name as string) || p.id}
                      {ips ? ` — ${ips}` : ""}
                    </SelectItem>
                  )
                })}
              </SelectContent>
            </Select>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setToAssign(null)}>
              Cancel
            </Button>
            <Button
              onClick={() => toAssign && act.mutate({ r: toAssign, action: "ASSIGN", data: { id: assignPort } })}
              disabled={!assignPort || act.isPending}
            >
              {act.isPending ? "Assigning…" : "Assign"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={!!toDelete} onOpenChange={(o) => !o && setToDelete(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Release floating IP</DialogTitle>
            <DialogDescription>
              Release <span className="font-mono">{toDelete ? fipAddress(toDelete) : ""}</span>? The address
              returns to the pool.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setToDelete(null)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={() => toDelete && del.mutate(toDelete)} disabled={del.isPending}>
              {del.isPending ? "Releasing…" : "Release floating IP"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
