import { useMemo, useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import type { ColumnDef } from "@tanstack/react-table"
import { toast } from "sonner"
import { Cable, MoreHorizontal, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react"
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
import { Textarea } from "@/components/ui/textarea"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { apiFetch } from "@/lib/api"
import { useCloudCursorList, useCloudList, useCloudScope, useProjectId } from "@/lib/hooks"
import type { CloudResource } from "@/lib/types"
import { networkName } from "./NetworksPage"

function portName(r: CloudResource): string {
  return (r.data?.port?.name as string) || r.name || r.id
}
function portFixedIPs(r: CloudResource): string {
  const ips = (r.data?.port?.fixed_ips as Array<{ ip_address?: string }> | undefined) ?? []
  return ips.map((f) => f.ip_address).filter(Boolean).join(", ")
}

export default function PortsPage() {
  const pid = useProjectId()
  const scope = useCloudScope(pid)
  const qc = useQueryClient()
  const {
    rows: data, isLoading, refetch, isFetching, error, hasNextPage, fetchNextPage, isFetchingNextPage,
  } = useCloudCursorList(pid, "PORT")
  const { data: networks } = useCloudList(pid, "NETWORK")
  const [createOpen, setCreateOpen] = useState(false)
  const [toDelete, setToDelete] = useState<CloudResource | null>(null)

  const [name, setName] = useState("")
  const [networkId, setNetworkId] = useState("")
  const [fixedIp, setFixedIp] = useState("")
  // Allowed address pairs — extra IPs/CIDRs the port may source traffic as (keepalived/HAProxy VIP,
  // or the range a self-hosted router VM forwards). One per line.
  const [pairs, setPairs] = useState("")

  // Edit dialog (Go TypePort UPDATE reads exactly: name?, portSecurityEnabled?, securityGroups? —
  // adminStateUp is NOT read; securityGroups editing is omitted here, disabling port security
  // force-clears them server-side).
  const [editTarget, setEditTarget] = useState<CloudResource | null>(null)
  const [editName, setEditName] = useState("")
  const [editPortSec, setEditPortSec] = useState(true)
  const [editPairs, setEditPairs] = useState("")

  const invalidate = () => void qc.invalidateQueries({ queryKey: ["cloud", pid, "PORT"] })

  // parsePairs turns a newline/comma list of IPs/CIDRs into the allowedAddressPairs wire shape.
  const parsePairs = (s: string) =>
    s.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean).map((ip) => ({ ipAddress: ip }))

  const openEdit = (r: CloudResource) => {
    setEditName((r.data?.port?.name as string) ?? "")
    setEditPortSec((r.data?.port?.port_security_enabled as boolean) !== false)
    const aap = (r.data?.port?.allowed_address_pairs as Array<{ ip_address?: string }> | undefined) ?? []
    setEditPairs(aap.map((p) => p.ip_address).filter(Boolean).join("\n"))
    setEditTarget(r)
  }

  const update = useMutation({
    mutationFn: (r: CloudResource) =>
      apiFetch(`/project/${pid}/cloud/${r.id}/action`, {
        method: "POST",
        cloud: scope,
        body: {
          action: "UPDATE",
          data: { name: editName, portSecurityEnabled: editPortSec, allowedAddressPairs: parsePairs(editPairs) },
        },
      }),
    onSuccess: () => {
      toast.success("Port updated")
      setEditTarget(null)
      invalidate()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const create = useMutation({
    mutationFn: () => {
      const data: Record<string, unknown> = { name, networkId }
      if (fixedIp) data.fixedIp = fixedIp
      const aap = parsePairs(pairs)
      if (aap.length) data.allowedAddressPairs = aap
      return apiFetch(`/project/${pid}/cloud`, {
        method: "POST",
        cloud: scope,
        body: { type: "PORT", data },
      })
    },
    onSuccess: () => {
      toast.success(`Port "${name}" created`)
      setCreateOpen(false)
      setName("")
      setFixedIp("")
      setPairs("")
      invalidate()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const del = useMutation({
    mutationFn: (r: CloudResource) =>
      apiFetch(`/project/${pid}/cloud/${r.id}`, { method: "DELETE", cloud: scope }),
    onSuccess: () => {
      toast.success("Port deletion requested")
      setToDelete(null)
      setTimeout(invalidate, 1500)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const columns = useMemo<ColumnDef<CloudResource, any>[]>(
    () => [
      {
        id: "name",
        accessorFn: (r) => `${portName(r)} ${(r.data?.port?.id as string) ?? r.externalId ?? ""}`,
        header: sortableHeader("Name / ID"),
        cell: ({ row }) => {
          const r = row.original
          return (
            <div>
              <span className="font-medium">{portName(r)}</span>
              <div className="font-mono text-xs text-muted-foreground">
                {(r.data?.port?.id as string) ?? r.externalId}
              </div>
            </div>
          )
        },
      },
      {
        id: "status",
        accessorFn: (r) => (r.data?.port?.status as string) ?? r.status ?? "",
        header: sortableHeader("Status"),
        cell: ({ getValue }) => <StatusBadge status={getValue()} />,
      },
      {
        id: "mac",
        accessorFn: (r) => (r.data?.port?.mac_address as string) ?? "",
        header: "MAC address",
        cell: ({ getValue }) => <span className="font-mono text-sm">{getValue() || "—"}</span>,
      },
      {
        id: "ips",
        accessorFn: (r) => portFixedIPs(r),
        header: sortableHeader("Fixed IPs"),
        cell: ({ getValue }) => <span className="font-mono text-sm">{getValue() || "—"}</span>,
      },
      {
        id: "device",
        accessorFn: (r) => (r.data?.port?.device_owner as string) ?? "",
        header: sortableHeader("Device"),
        cell: ({ getValue }) => (
          <span className="text-sm text-muted-foreground">{getValue() || "—"}</span>
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
                  <Button variant="ghost" size="icon-sm" aria-label={`Actions for ${portName(r)}`}>
                    <MoreHorizontal className="size-4" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem onClick={() => openEdit(r)}>
                    <Pencil className="size-4" /> Edit
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
    // useState setters are stable; openEdit only touches setters.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  )

  return (
    <>
      <PageHeader
        title="Ports"
        eyebrow="Network"
        description="Network interfaces in this project."
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => void refetch()} disabled={isFetching} aria-label="Refresh">
              <RefreshCw className={isFetching ? "size-4 animate-spin" : "size-4"} />
            </Button>
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus className="size-4" /> Create port
            </Button>
          </>
        }
      />

      {!isLoading && !error && !data.length ? (
        <EmptyState
          icon={Cable}
          title="No ports"
          hint="Ports appear here when servers attach to networks, or create one directly."
          action={
            <Button onClick={() => setCreateOpen(true)}>
              <Plus className="size-4" /> Create port
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
          />
          <LoadMore
            hasNextPage={hasNextPage}
            isFetching={isFetchingNextPage}
            onClick={() => void fetchNextPage()}
            count={data.length}
            noun="port"
          />
        </>
      )}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Create port</DialogTitle>
            <DialogDescription>A network interface on one of your networks.</DialogDescription>
          </DialogHeader>
          <div className="grid gap-4 py-2">
            <div className="grid gap-2">
              <Label htmlFor="port-name">Name</Label>
              <Input id="port-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="my-port" />
            </div>
            <div className="grid gap-2">
              <Label>Network</Label>
              <Select value={networkId} onValueChange={setNetworkId}>
                <SelectTrigger>
                  <SelectValue placeholder="Select a network" />
                </SelectTrigger>
                <SelectContent>
                  {(networks ?? []).map((n) => {
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
            <div className="grid gap-2">
              <Label htmlFor="port-ip">Fixed IP (optional)</Label>
              <Input
                id="port-ip"
                className="font-mono"
                value={fixedIp}
                onChange={(e) => setFixedIp(e.target.value)}
                placeholder="10.0.0.10"
              />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="port-aap">Allowed address pairs (optional)</Label>
              <Textarea
                id="port-aap"
                className="font-mono text-xs"
                rows={3}
                value={pairs}
                onChange={(e) => setPairs(e.target.value)}
                placeholder={"10.0.0.100\n10.0.0.0/24"}
              />
              <p className="text-xs text-muted-foreground">
                Extra IPs/CIDRs this port may use — e.g. a keepalived/HAProxy VIP, or the range a router VM
                forwards. One per line.
              </p>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>
              Cancel
            </Button>
            <Button onClick={() => create.mutate()} disabled={!name || !networkId || create.isPending}>
              {create.isPending ? "Creating…" : "Create port"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={!!editTarget} onOpenChange={(o) => !o && setEditTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Edit port</DialogTitle>
            <DialogDescription>Update the port's name, security and allowed address pairs.</DialogDescription>
          </DialogHeader>
          <div className="grid gap-4 py-2">
            <div className="grid gap-2">
              <Label htmlFor="edit-port-name">Name</Label>
              <Input id="edit-port-name" value={editName} onChange={(e) => setEditName(e.target.value)} />
            </div>
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-1">
                <Label htmlFor="edit-port-sec">Port security</Label>
                <p className="text-xs text-muted-foreground">
                  Disabling port security removes all security groups from this port.
                </p>
              </div>
              <Switch id="edit-port-sec" checked={editPortSec} onCheckedChange={setEditPortSec} />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="edit-port-aap">Allowed address pairs</Label>
              <Textarea
                id="edit-port-aap"
                className="font-mono text-xs"
                rows={3}
                value={editPairs}
                onChange={(e) => setEditPairs(e.target.value)}
                placeholder={"10.0.0.100\n10.0.0.0/24"}
              />
              <p className="text-xs text-muted-foreground">
                Extra IPs/CIDRs this port may use (keepalived/HAProxy VIP, router-VM range). One per line; empty
                clears them.
              </p>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setEditTarget(null)}>
              Cancel
            </Button>
            <Button onClick={() => editTarget && update.mutate(editTarget)} disabled={update.isPending}>
              {update.isPending ? "Saving…" : "Save changes"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={!!toDelete} onOpenChange={(o) => !o && setToDelete(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete port</DialogTitle>
            <DialogDescription>
              Delete port "{toDelete ? portName(toDelete) : ""}"? This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setToDelete(null)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={() => toDelete && del.mutate(toDelete)} disabled={del.isPending}>
              {del.isPending ? "Deleting…" : "Delete port"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
