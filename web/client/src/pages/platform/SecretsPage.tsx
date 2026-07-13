import { useMemo, useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import type { ColumnDef } from "@tanstack/react-table"
import { toast } from "sonner"
import { KeyRound, Plus, RefreshCw, Trash2 } from "lucide-react"
import { PageHeader } from "@/components/layout/PageHeader"
import { DataTable, sortableHeader } from "@/components/data-table"
import { EmptyState } from "@/components/empty-state"
import { StatusBadge } from "@/components/status-badge"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import { apiFetch } from "@/lib/api"
import { fmtDateTime, timeAgo } from "@/lib/format"
import { useCloudList, useCloudScope, useProjectId } from "@/lib/hooks"
import type { CloudResource } from "@/lib/types"

function secretName(r: CloudResource): string {
  return (r.data?.secret?.name as string) ?? r.name ?? r.id
}
function secretType(r: CloudResource): string {
  return (r.data?.secret?.secret_type as string) ?? "—"
}
function secretStatus(r: CloudResource): string | undefined {
  return (r.data?.secret?.status as string) ?? r.status
}
function secretExpiration(r: CloudResource): string | undefined {
  return r.data?.secret?.expiration as string | undefined
}

export default function SecretsPage() {
  const pid = useProjectId()
  const scope = useCloudScope(pid)
  const qc = useQueryClient()
  const { data, isLoading, error, refetch, isFetching } = useCloudList(pid, "BARBICAN_SECRET")

  const [createOpen, setCreateOpen] = useState(false)
  const [name, setName] = useState("")
  const [type, setType] = useState("opaque")
  const [payload, setPayload] = useState("")
  const [toDelete, setToDelete] = useState<CloudResource | null>(null)

  const invalidate = () => void qc.invalidateQueries({ queryKey: ["cloud", pid, "BARBICAN_SECRET"] })

  const create = useMutation({
    mutationFn: () =>
      apiFetch(`/project/${pid}/cloud`, {
        method: "POST",
        body: {
          type: "BARBICAN_SECRET",
          data: { name, secretType: type, payload, payloadContentType: "text/plain" },
        },
        cloud: scope,
      }),
    onSuccess: () => {
      toast.success(`Secret "${name}" created`)
      setCreateOpen(false)
      setName("")
      setPayload("")
      invalidate()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const del = useMutation({
    mutationFn: (id: string) =>
      apiFetch(`/project/${pid}/cloud/${id}`, { method: "DELETE", cloud: scope }),
    onSuccess: () => {
      toast.success("Secret deletion requested")
      setToDelete(null)
      setTimeout(invalidate, 1500)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const columns = useMemo<ColumnDef<CloudResource, any>[]>(
    () => [
      {
        id: "name",
        accessorFn: (r) => secretName(r),
        header: sortableHeader("Name"),
        cell: ({ getValue }) => (
          <span className="flex items-center gap-2 font-medium">
            <KeyRound className="size-3.5 text-muted-foreground" aria-hidden="true" />
            {getValue()}
          </span>
        ),
      },
      {
        id: "type",
        accessorFn: (r) => secretType(r),
        header: sortableHeader("Type"),
        cell: ({ getValue }) => <Badge variant="secondary">{getValue()}</Badge>,
      },
      {
        id: "status",
        accessorFn: (r) => secretStatus(r) ?? "",
        header: sortableHeader("Status"),
        cell: ({ getValue }) => <StatusBadge status={getValue()} />,
      },
      {
        id: "expiration",
        accessorFn: (r) => secretExpiration(r) ?? "",
        header: sortableHeader("Expiration"),
        cell: ({ getValue }) => (
          <span className="text-sm text-muted-foreground">
            {getValue() ? fmtDateTime(getValue()) : "Never"}
          </span>
        ),
      },
      {
        id: "created",
        accessorFn: (r) => r.info?.createdAt ?? r.createdAt ?? "",
        header: sortableHeader("Created"),
        cell: ({ getValue }) => <span className="text-sm text-muted-foreground">{timeAgo(getValue())}</span>,
      },
      {
        id: "actions",
        header: () => null,
        enableSorting: false,
        cell: ({ row }) => {
          const r = row.original
          return (
            <div className="text-right">
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={() => setToDelete(r)}
                aria-label={`Delete secret ${secretName(r)}`}
              >
                <Trash2 className="size-4" />
              </Button>
            </div>
          )
        },
      },
    ],
    [],
  )

  return (
    <>
      <PageHeader
        title="Secrets"
        eyebrow="Platform"
        description="Payloads stored in the key manager (Barbican)."
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => void refetch()} disabled={isFetching} aria-label="Refresh secrets">
              <RefreshCw className={isFetching ? "size-4 animate-spin" : "size-4"} />
            </Button>
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus className="size-4" /> Create secret
            </Button>
          </>
        }
      />

      {!isLoading && !error && !data?.length ? (
        <EmptyState
          icon={KeyRound}
          title="No secrets yet"
          hint="Store passphrases and other sensitive payloads in the key manager."
          action={
            <Button onClick={() => setCreateOpen(true)}>
              <Plus className="size-4" /> Create secret
            </Button>
          }
        />
      ) : (
        <DataTable
          columns={columns}
          data={data}
          isLoading={isLoading}
          error={(error as Error | null) ?? null}
          searchPlaceholder="Search secrets…"
          getRowId={(r) => r.id}
        />
      )}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Create secret</DialogTitle>
            <DialogDescription>The payload is stored encrypted in the key manager.</DialogDescription>
          </DialogHeader>
          <div className="grid gap-4 py-2">
            <div className="grid gap-2">
              <Label htmlFor="secret-name">Name</Label>
              <Input id="secret-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="db-password" />
            </div>
            <div className="grid gap-2">
              <Label>Secret type</Label>
              <Select value={type} onValueChange={setType}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="opaque">Opaque</SelectItem>
                  <SelectItem value="passphrase">Passphrase</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-2">
              <Label htmlFor="secret-payload">Payload</Label>
              <Textarea
                id="secret-payload"
                value={payload}
                onChange={(e) => setPayload(e.target.value)}
                placeholder="secret value"
                rows={5}
                spellCheck={false}
                autoComplete="off"
                className="max-h-60 resize-y font-mono text-xs leading-relaxed"
              />
              <p className="text-xs text-muted-foreground">
                Stored as <span className="font-mono">text/plain</span>; the payload is write-only after creation.
              </p>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>
              Cancel
            </Button>
            <Button onClick={() => create.mutate()} disabled={!name.trim() || !payload || create.isPending}>
              {create.isPending ? "Creating…" : "Create secret"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={!!toDelete} onOpenChange={(o) => !o && setToDelete(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete secret</DialogTitle>
            <DialogDescription>
              Delete "{toDelete ? secretName(toDelete) : ""}"? The stored payload is destroyed. This cannot be
              undone.
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
              {del.isPending ? "Deleting…" : "Delete secret"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
