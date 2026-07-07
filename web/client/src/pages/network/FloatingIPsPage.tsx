import { useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"
import { Globe, Link2, Link2Off, Plus, RefreshCw, Trash2 } from "lucide-react"
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
import { Skeleton } from "@/components/ui/skeleton"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { apiFetch } from "@/lib/api"
import { useCloudList, useCloudScope, useProjectId, usePublicNetworks } from "@/lib/hooks"
import type { CloudResource } from "@/lib/types"

function fipAddress(r: CloudResource): string {
  return (r.data?.floatingIp?.floating_ip_address as string) || r.name || r.id
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

  const invalidate = () => void qc.invalidateQueries({ queryKey: ["cloud", pid, "FLOATING_IP"] })

  const create = useMutation({
    mutationFn: () =>
      apiFetch(`/project/${pid}/cloud`, {
        method: "POST",
        cloud: scope,
        body: { type: "FLOATING_IP", data: { networkId: netManual || netSelect } },
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

  return (
    <>
      <PageHeader
        title="Floating IPs"
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

      {isLoading ? (
        <Skeleton className="h-64" />
      ) : error ? (
        <div className="rounded-lg border bg-muted/40 p-6 text-sm text-muted-foreground">{(error as Error).message}</div>
      ) : !data?.length ? (
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
        <Card className="overflow-hidden py-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Address</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Mapped fixed IP</TableHead>
                <TableHead>Port</TableHead>
                <TableHead className="w-40 text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.map((r) => {
                const f = (r.data?.floatingIp ?? {}) as Record<string, unknown>
                const assigned = !!f.port_id
                return (
                  <TableRow key={r.id}>
                    <TableCell className="font-mono font-medium">{fipAddress(r)}</TableCell>
                    <TableCell>
                      <StatusBadge status={(f.status as string) ?? r.status} />
                    </TableCell>
                    <TableCell className="font-mono text-sm">{(f.fixed_ip_address as string) || "—"}</TableCell>
                    <TableCell className="font-mono text-xs text-muted-foreground">
                      {(f.port_id as string) || "—"}
                    </TableCell>
                    <TableCell>
                      <div className="flex justify-end gap-1">
                        {assigned ? (
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => act.mutate({ r, action: "UNASSIGN" })}
                            disabled={act.isPending}
                          >
                            <Link2Off className="size-4" /> Unassign
                          </Button>
                        ) : (
                          <Button variant="ghost" size="sm" onClick={() => setToAssign(r)}>
                            <Link2 className="size-4" /> Assign
                          </Button>
                        )}
                        <Button variant="ghost" size="icon" onClick={() => setToDelete(r)} aria-label="Delete floating IP">
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
            <DialogTitle>Allocate floating IP</DialogTitle>
            <DialogDescription>Pick the external network pool to allocate from.</DialogDescription>
          </DialogHeader>
          <div className="grid gap-4 py-2">
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
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>
              Cancel
            </Button>
            <Button onClick={() => create.mutate()} disabled={(!netSelect && !netManual) || create.isPending}>
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
              Map {toAssign ? fipAddress(toAssign) : ""} onto a port (a server's network interface).
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
              Release {toDelete ? fipAddress(toDelete) : ""}? The address returns to the pool.
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
