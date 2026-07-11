import { useMemo, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import type { ColumnDef } from "@tanstack/react-table"
import { toast } from "sonner"
import { FileCode2, Layers, MoreHorizontal, Pause, Play, Plus, RefreshCw, Trash2 } from "lucide-react"
import { PageHeader } from "@/components/layout/PageHeader"
import { DataTable, sortableHeader } from "@/components/data-table"
import { EmptyState } from "@/components/empty-state"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle,
} from "@/components/ui/sheet"
import { Skeleton } from "@/components/ui/skeleton"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"
import { apiFetch, type CloudScope } from "@/lib/api"
import { fmtDateTime, timeAgo } from "@/lib/format"
import { useCloudList, useCloudScope, useProjectId } from "@/lib/hooks"
import type { CloudResource } from "@/lib/types"

function stackName(r: CloudResource): string {
  return (r.data?.stack?.stack_name as string) ?? (r.data?.stack?.name as string) ?? r.name ?? r.id
}
function stackStatus(r: CloudResource): string | undefined {
  return (r.data?.stack?.stack_status as string) ?? r.status
}

// Heat event rows (LIST_STACK_EVENTS returns them verbatim; field names are defensive —
// gophercloud's Event time field may surface as event_time or time depending on tags).
type StackEvent = {
  id?: string
  resource_name?: string
  resource_status?: string
  resource_status_reason?: string
  event_time?: string
  time?: string
}

// Heat resource rows (LIST_RESOURCES → gophercloud stackresources.Resource JSON).
type StackResource = {
  logical_resource_id?: string
  physical_resource_id?: string
  resource_name?: string
  resource_type?: string
  resource_status?: string
  resource_status_reason?: string
}

const TEMPLATE_PLACEHOLDER = `heat_template_version: 2021-04-16
description: Example stack
resources:
  random:
    type: OS::Heat::RandomString
`

export default function StacksPage() {
  const pid = useProjectId()
  const scope = useCloudScope(pid)
  const qc = useQueryClient()
  const { data, isLoading, isError, error, refetch, isFetching } = useCloudList(pid, "STACK")

  const [createOpen, setCreateOpen] = useState(false)
  const [name, setName] = useState("")
  const [template, setTemplate] = useState("")
  const [toDelete, setToDelete] = useState<CloudResource | null>(null)
  const [detailsFor, setDetailsFor] = useState<CloudResource | null>(null)

  const invalidate = () => void qc.invalidateQueries({ queryKey: ["cloud", pid, "STACK"] })

  const create = useMutation({
    mutationFn: () =>
      apiFetch(`/project/${pid}/cloud`, {
        method: "POST",
        body: { type: "STACK", data: { name, template } },
        cloud: scope,
      }),
    onSuccess: () => {
      toast.success(`Stack "${name}" is being created`)
      setCreateOpen(false)
      setName("")
      setTemplate("")
      invalidate()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const act = useMutation({
    mutationFn: ({ id, action }: { id: string; action: string }) =>
      apiFetch(`/project/${pid}/cloud/${id}/action`, {
        method: "POST",
        body: { action },
        cloud: scope,
      }),
    onSuccess: (_d, { action }) => {
      toast.success(`${action === "SUSPEND_STACK" ? "Suspend" : "Resume"} requested`)
      setTimeout(invalidate, 1500)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const del = useMutation({
    mutationFn: (id: string) =>
      apiFetch(`/project/${pid}/cloud/${id}`, { method: "DELETE", cloud: scope }),
    onSuccess: () => {
      toast.success("Stack deletion requested")
      setToDelete(null)
      setTimeout(invalidate, 1500)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const columns = useMemo<ColumnDef<CloudResource, any>[]>(
    () => [
      {
        id: "name",
        accessorFn: (r) => stackName(r),
        header: sortableHeader("Name"),
        cell: ({ getValue }) => <span className="font-medium">{getValue()}</span>,
      },
      {
        id: "status",
        accessorFn: (r) => stackStatus(r) ?? "",
        header: sortableHeader("Status"),
        cell: ({ getValue }) => <StatusBadge status={getValue()} />,
      },
      {
        id: "created",
        accessorFn: (r) => (r.data?.stack?.creation_time as string) ?? r.info?.createdAt ?? r.createdAt ?? "",
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
            <div className="text-right" onClick={(e) => e.stopPropagation()}>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="ghost" size="icon-sm" aria-label={`Actions for ${stackName(r)}`}>
                    <MoreHorizontal className="size-4" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem onClick={() => setDetailsFor(r)}>
                    <FileCode2 className="size-4" /> Details
                  </DropdownMenuItem>
                  <DropdownMenuItem onClick={() => act.mutate({ id: r.id, action: "SUSPEND_STACK" })}>
                    <Pause className="size-4" /> Suspend
                  </DropdownMenuItem>
                  <DropdownMenuItem onClick={() => act.mutate({ id: r.id, action: "RESUME_STACK" })}>
                    <Play className="size-4" /> Resume
                  </DropdownMenuItem>
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
    // act.mutate and the dialog setters are stable references.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  )

  return (
    <>
      <PageHeader
        title="Stacks"
        eyebrow="Platform"
        description="Heat orchestration stacks deployed from HOT templates."
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => void refetch()} disabled={isFetching} aria-label="Refresh stacks">
              <RefreshCw className={isFetching ? "size-4 animate-spin" : "size-4"} />
            </Button>
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus className="size-4" /> Create stack
            </Button>
          </>
        }
      />

      {!isLoading && !isError && !data?.length ? (
        <EmptyState
          icon={Layers}
          title="No stacks yet"
          hint="Deploy infrastructure as code by creating a stack from a HOT template."
          action={
            <Button onClick={() => setCreateOpen(true)}>
              <Plus className="size-4" /> Create stack
            </Button>
          }
        />
      ) : (
        <DataTable
          columns={columns}
          data={data}
          isLoading={isLoading}
          error={(error as Error | null) ?? null}
          onRowClick={(r) => setDetailsFor(r)}
          getRowId={(r) => r.id}
        />
      )}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="flex max-h-[85vh] flex-col sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>Create stack</DialogTitle>
            <DialogDescription>Paste a Heat orchestration template (HOT, YAML).</DialogDescription>
          </DialogHeader>
          <div className="grid min-h-0 flex-1 gap-4 overflow-y-auto py-2 pr-1">
            <div className="grid gap-2">
              <Label htmlFor="stack-name">Name</Label>
              <Input id="stack-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="my-stack" />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="stack-template">Template</Label>
              <Textarea
                id="stack-template"
                value={template}
                onChange={(e) => setTemplate(e.target.value)}
                placeholder={TEMPLATE_PLACEHOLDER}
                rows={12}
                spellCheck={false}
                className="max-h-[45vh] min-h-48 resize-y font-mono text-xs leading-relaxed"
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>
              Cancel
            </Button>
            <Button onClick={() => create.mutate()} disabled={!name.trim() || !template.trim() || create.isPending}>
              {create.isPending ? "Creating…" : "Create stack"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={!!toDelete} onOpenChange={(o) => !o && setToDelete(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete stack</DialogTitle>
            <DialogDescription>
              Delete "{toDelete ? stackName(toDelete) : ""}"? All resources created by this stack are deleted
              with it. This cannot be undone.
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
              {del.isPending ? "Deleting…" : "Delete"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {detailsFor && (
        <StackDetailsSheet pid={pid} scope={scope} stack={detailsFor} onClose={() => setDetailsFor(null)} />
      )}
    </>
  )
}

// Per-stack details: Events (LIST_STACK_EVENTS) / Template (GET_TEMPLATE) / Resources
// (LIST_RESOURCES) — Go cloud_writes.go clusterAction TypeStack.
function StackDetailsSheet({
  pid, scope, stack, onClose,
}: {
  pid: string
  scope: CloudScope | undefined
  stack: CloudResource
  onClose: () => void
}) {
  const stackId = stack.id

  const act = <T,>(action: string) =>
    apiFetch<{ result?: T }>(`/project/${pid}/cloud/${stackId}/action`, {
      method: "POST",
      body: { action },
      cloud: scope,
    })

  const events = useQuery({
    queryKey: ["stack-events", pid, stackId],
    queryFn: () => act<StackEvent[]>("LIST_STACK_EVENTS"),
    enabled: !!scope,
  })
  const tpl = useQuery({
    queryKey: ["stack-template", pid, stackId],
    queryFn: () => act<{ template?: string }>("GET_TEMPLATE"),
    enabled: !!scope,
  })
  const resources = useQuery({
    queryKey: ["stack-resources", pid, stackId],
    queryFn: () => act<StackResource[]>("LIST_RESOURCES"),
    enabled: !!scope,
  })

  // GET_TEMPLATE returns the raw template body as one JSON string — pretty-print when parseable.
  const rawTemplate = tpl.data?.result?.template ?? ""
  let prettyTemplate = rawTemplate
  try {
    prettyTemplate = JSON.stringify(JSON.parse(rawTemplate), null, 2)
  } catch {
    // not JSON (YAML HOT) — show as-is
  }

  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="overflow-y-auto sm:max-w-2xl">
        <SheetHeader>
          <div className="text-eyebrow">Stack</div>
          <SheetTitle className="font-display">{stackName(stack)}</SheetTitle>
          <SheetDescription>Events, template and resources of this stack.</SheetDescription>
        </SheetHeader>

        <div className="px-4 pb-6">
          <Tabs defaultValue="events">
            <TabsList>
              <TabsTrigger value="events">Events</TabsTrigger>
              <TabsTrigger value="template">Template</TabsTrigger>
              <TabsTrigger value="resources">Resources</TabsTrigger>
            </TabsList>

            <TabsContent value="events" className="mt-4">
              {events.isLoading ? (
                <Skeleton className="h-32" />
              ) : events.isError ? (
                <p className="rounded-md bg-muted p-3 text-sm text-muted-foreground">
                  {(events.error as Error).message}
                </p>
              ) : !events.data?.result?.length ? (
                <p className="py-6 text-center text-sm text-muted-foreground">No events recorded.</p>
              ) : (
                <div className="overflow-hidden rounded-lg border">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>Resource</TableHead>
                        <TableHead>Status</TableHead>
                        <TableHead>Time</TableHead>
                        <TableHead>Reason</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {events.data.result.map((e, i) => (
                        <TableRow key={e.id ?? i}>
                          <TableCell className="font-medium">{e.resource_name ?? "—"}</TableCell>
                          <TableCell>
                            <StatusBadge status={e.resource_status} />
                          </TableCell>
                          <TableCell className="text-sm text-muted-foreground">
                            {fmtDateTime(e.event_time ?? e.time)}
                          </TableCell>
                          <TableCell className="max-w-xs truncate text-sm text-muted-foreground">
                            {e.resource_status_reason ?? "—"}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>
              )}
            </TabsContent>

            <TabsContent value="template" className="mt-4">
              {tpl.isLoading ? (
                <Skeleton className="h-32" />
              ) : tpl.isError ? (
                <p className="rounded-md bg-muted p-3 text-sm text-muted-foreground">
                  {(tpl.error as Error).message}
                </p>
              ) : !rawTemplate ? (
                <p className="py-6 text-center text-sm text-muted-foreground">No template returned.</p>
              ) : (
                <pre className="max-h-[60vh] overflow-auto rounded-lg border bg-muted/50 p-3 font-mono text-xs leading-relaxed">
                  {prettyTemplate}
                </pre>
              )}
            </TabsContent>

            <TabsContent value="resources" className="mt-4">
              {resources.isLoading ? (
                <Skeleton className="h-32" />
              ) : resources.isError ? (
                <p className="rounded-md bg-muted p-3 text-sm text-muted-foreground">
                  {(resources.error as Error).message}
                </p>
              ) : !resources.data?.result?.length ? (
                <p className="py-6 text-center text-sm text-muted-foreground">No resources in this stack.</p>
              ) : (
                <div className="overflow-hidden rounded-lg border">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>Name</TableHead>
                        <TableHead>Type</TableHead>
                        <TableHead>Status</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {resources.data.result.map((res, i) => (
                        <TableRow key={res.physical_resource_id ?? i}>
                          <TableCell className="font-medium">
                            {res.resource_name ?? res.logical_resource_id ?? "—"}
                          </TableCell>
                          <TableCell className="font-mono text-xs">{res.resource_type ?? "—"}</TableCell>
                          <TableCell>
                            <StatusBadge status={res.resource_status} />
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>
              )}
            </TabsContent>
          </Tabs>
        </div>
      </SheetContent>
    </Sheet>
  )
}
