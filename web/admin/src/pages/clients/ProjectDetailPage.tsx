import { useEffect, useState } from "react"
import { Link, useNavigate, useParams } from "react-router-dom"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { ArrowLeft, CreditCard, Pause, Play, Plus, RefreshCw, Server, Trash2, Users } from "lucide-react"
import { toast } from "sonner"
import { PageHeader } from "@/components/layout/PageHeader"
import { EmptyState } from "@/components/empty-state"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ApiError, apiFetch } from "@/lib/api"
import { fmtDate, fmtDateTime } from "@/lib/format"
import { useAdminGet, useAdminList } from "@/lib/hooks"

// GET /admin/project/{id} (handler.go rawByID "project") — the project doc shaped (_id→id).
type ProjectDoc = {
  id?: string
  name?: string
  status?: string
  organizationId?: string
  billingProfileId?: string
  memberships?: Array<{ sub?: string; role?: string }>
  services?: Array<{ serviceId?: string; externalProjectId?: string }>
  // absent/null = all external networks allowed; array = allow-list of Neutron network ids.
  publicNetworkIds?: string[] | null
  createdAt?: string
}

// GET /admin/cloud-resource/public-networks/{externalServiceId} (cloudadmin.go publicNetworks) —
// the provider's router:external networks.
type PublicNetwork = {
  id?: string
  name?: string
}

// GET /admin/project/{id}/members (projectmut.go projectMembers) — shaped user docs.
type MemberUser = {
  id?: string
  sub?: string
  email?: string
  firstName?: string
  lastName?: string
}

// GET /admin/cloud-resource/project/{id} → cloud.CloudResource.
type CloudResource = {
  id?: string
  type?: string
  externalId?: string
  region?: string
  data?: Record<string, unknown>
  createdAt?: string
}

// GET /admin/billing-profile (clientarea_reads.go billingProfileAdminList) — for the change dialog.
type BillingProfile = {
  id?: string
  name?: string
  email?: string
  status?: string
  currency?: string
}

// GET /admin/user (handler.go listRaw "users") — for the add-member picker.
type AdminUser = {
  id?: string
  sub?: string
  email?: string
  firstName?: string
  lastName?: string
}

function dataField(cr: CloudResource, key: "name" | "status"): string | undefined {
  for (const v of Object.values(cr.data ?? {})) {
    if (v && typeof v === "object") {
      const s = (v as Record<string, unknown>)[key]
      if (typeof s === "string" && s) return s
    }
  }
  return undefined
}

function Field({ label, value, mono }: { label: string; value?: React.ReactNode; mono?: boolean }) {
  return (
    <div>
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className={`mt-0.5 text-sm ${mono ? "font-mono" : ""}`}>{value || "—"}</p>
    </div>
  )
}

function ErrorPanel({ error }: { error: unknown }) {
  return (
    <div className="rounded-lg border bg-muted/40 p-6 text-sm text-muted-foreground">
      {error instanceof Error ? error.message : "Something went wrong"}
    </div>
  )
}

export default function ProjectDetailPage() {
  const { id = "" } = useParams()
  const navigate = useNavigate()
  const qc = useQueryClient()

  const projectPath = `/admin/project/${id}`
  const { data: project, isLoading, error } = useAdminGet<ProjectDoc>(projectPath, !!id)
  // GET /admin/project/{id}/resources/counts (projectmut.go projectResourceCounts) — {TYPE: n, TOTAL: n}.
  const counts = useAdminGet<Record<string, number>>(`${projectPath}/resources/counts`, !!id)
  const members = useAdminList<MemberUser>(`${projectPath}/members`, !!id)
  const resources = useAdminList<CloudResource>(`/admin/cloud-resource/project/${id}`, !!id)

  const [statusConfirm, setStatusConfirm] = useState<"ENABLED" | "DISABLED" | null>(null)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [bpOpen, setBpOpen] = useState(false)
  const [bpChoice, setBpChoice] = useState("")
  const [addMemberOpen, setAddMemberOpen] = useState(false)
  const [memberChoice, setMemberChoice] = useState("")
  const [memberRole, setMemberRole] = useState("MEMBER")
  const [memberToRemove, setMemberToRemove] = useState<MemberUser | null>(null)

  const bps = useAdminList<BillingProfile>("/admin/billing-profile", bpOpen)
  const users = useAdminList<AdminUser>("/admin/user", addMemberOpen)

  const externalServiceId = project?.services?.find((s) => s.serviceId)?.serviceId ?? ""
  const publicNets = useAdminList<PublicNetwork>(
    `/admin/cloud-resource/public-networks/${externalServiceId}`,
    !!externalServiceId,
  )
  const [allPublicNets, setAllPublicNets] = useState(true)
  const [publicNetChoice, setPublicNetChoice] = useState<string[]>([])
  useEffect(() => {
    setAllPublicNets(!project?.publicNetworkIds)
    setPublicNetChoice(project?.publicNetworkIds ?? [])
  }, [project?.publicNetworkIds])

  const enabled = (project?.status ?? "").toUpperCase() === "ENABLED"

  const invalidateProject = () => {
    qc.invalidateQueries({ queryKey: ["admin-get", projectPath] })
    qc.invalidateQueries({ queryKey: ["admin-list", `${projectPath}/members`] })
    qc.invalidateQueries({ queryKey: ["admin-list", "/admin/project"] })
  }

  // POST /admin/project/{id}/{ENABLED|DISABLED} (projectmut.go projectUpdateStatus).
  const updateStatus = useMutation({
    mutationFn: (status: "ENABLED" | "DISABLED") => apiFetch(`${projectPath}/${status}`, { method: "POST" }),
    onSuccess: (_d, status) => {
      toast.success(status === "ENABLED" ? "Project enabled" : "Project disabled")
      setStatusConfirm(null)
      invalidateProject()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  // POST /admin/project/{id}/sync (projectmut.go projectSync).
  const syncProject = useMutation({
    mutationFn: () => apiFetch(`${projectPath}/sync`, { method: "POST" }),
    onSuccess: () => {
      toast.success("Project synced")
      qc.invalidateQueries({ queryKey: ["admin-list", `/admin/cloud-resource/project/${id}`] })
      qc.invalidateQueries({ queryKey: ["admin-get", `${projectPath}/resources/counts`] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  // DELETE /admin/project/{id} (projectmut.go projectScheduleDeletion — cloud pre-check is a 501
  // seam on this deployment).
  const deleteProject = useMutation({
    mutationFn: () => apiFetch(projectPath, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Project deletion scheduled")
      navigate("/clients/projects")
    },
    onError: (e: Error) => {
      setDeleteOpen(false)
      if (e instanceof ApiError && e.status === 501) {
        toast.info("Not available: project deletion is not supported on this deployment.")
      } else {
        toast.error(e.message)
      }
    },
  })

  // PUT /admin/project/{id} (projectmut.go projectUpdate) — overwrites name/billingProfileId/
  // organizationId together, so resend the current name + organizationId alongside the new bp.
  const changeBp = useMutation({
    mutationFn: () =>
      apiFetch(projectPath, {
        method: "PUT",
        body: {
          name: project?.name ?? "",
          organizationId: project?.organizationId ?? "",
          billingProfileId: bpChoice,
        },
      }),
    onSuccess: () => {
      toast.success("Billing profile updated")
      setBpOpen(false)
      setBpChoice("")
      invalidateProject()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  // POST /admin/projects/manage {userId, projectId, role} (projectmanager.go). The membership is
  // PERSISTED before the cloud-access grant, which 501s on this deployment — treat 501 as applied.
  const addMember = useMutation({
    mutationFn: () =>
      apiFetch("/admin/projects/manage", {
        method: "POST",
        body: { userId: memberChoice, projectId: id, role: memberRole },
      }),
    onSuccess: () => {
      toast.success("Member added")
      setAddMemberOpen(false)
      setMemberChoice("")
      invalidateProject()
    },
    onError: (e: Error) => {
      if (e instanceof ApiError && e.status === 501) {
        toast.success("Member added (cloud access grant not available on this deployment)")
        setAddMemberOpen(false)
        setMemberChoice("")
        invalidateProject()
      } else {
        toast.error(e.message)
      }
    },
  })

  // POST /admin/projects/manage/remove {projectId, sub} — same persisted-then-501 semantics.
  const removeMember = useMutation({
    mutationFn: (sub: string) =>
      apiFetch("/admin/projects/manage/remove", { method: "POST", body: { projectId: id, sub } }),
    onSuccess: () => {
      toast.success("Member removed")
      setMemberToRemove(null)
      invalidateProject()
    },
    onError: (e: Error) => {
      if (e instanceof ApiError && e.status === 501) {
        toast.success("Member removed (cloud access revoke not available on this deployment)")
        setMemberToRemove(null)
        invalidateProject()
      } else {
        toast.error(e.message)
      }
    },
  })

  // PUT /admin/project/{id}/public-networks — null resets to the default (all external networks),
  // an array restricts the project to that allow-list.
  const savePublicNets = useMutation({
    mutationFn: () =>
      apiFetch(`${projectPath}/public-networks`, {
        method: "PUT",
        body: { publicNetworkIds: allPublicNets ? null : publicNetChoice },
      }),
    onSuccess: () => {
      toast.success("Public networks updated")
      invalidateProject()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const memberRoleOf = (sub?: string) =>
    project?.memberships?.find((m) => m.sub === sub)?.role ?? "MEMBER"

  const countEntries = Object.entries(counts.data ?? {}).filter(([k, v]) => k !== "TOTAL" && (v ?? 0) > 0)

  return (
    <>
      <PageHeader
        title={project?.name ?? (isLoading ? "Loading…" : "Project")}
        description="Client project detail."
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => navigate("/clients/projects")}>
              <ArrowLeft />
              Back to projects
            </Button>
            <Button variant="outline" size="sm" onClick={() => syncProject.mutate()} disabled={syncProject.isPending}>
              <RefreshCw className={syncProject.isPending ? "animate-spin" : ""} />
              Sync
            </Button>
            <Button
              variant="outline"
              size="sm"
              disabled={!project}
              onClick={() => setStatusConfirm(enabled ? "DISABLED" : "ENABLED")}
            >
              {enabled ? <Pause /> : <Play />}
              {enabled ? "Disable" : "Enable"}
            </Button>
            <Button variant="destructive" size="sm" disabled={!project} onClick={() => setDeleteOpen(true)}>
              <Trash2 />
              Delete project
            </Button>
          </>
        }
      />

      {isLoading ? (
        <Skeleton className="h-64" />
      ) : error ? (
        <ErrorPanel error={error} />
      ) : (
        <Tabs defaultValue="overview">
          <TabsList>
            <TabsTrigger value="overview">Overview</TabsTrigger>
            <TabsTrigger value="members">Members</TabsTrigger>
            <TabsTrigger value="resources">Cloud resources</TabsTrigger>
          </TabsList>

          <TabsContent value="overview" className="mt-4 space-y-6">
            <Card>
              <CardHeader>
                <CardTitle className="text-base">Project</CardTitle>
              </CardHeader>
              <CardContent className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                <Field label="Name" value={project?.name} />
                <div>
                  <p className="text-xs text-muted-foreground">Status</p>
                  <div className="mt-1">
                    <StatusBadge status={project?.status} />
                  </div>
                </div>
                <Field label="ID" value={project?.id} mono />
                <div>
                  <p className="text-xs text-muted-foreground">Organization</p>
                  <p className="mt-0.5 text-sm">
                    {project?.organizationId ? (
                      <Link
                        to={`/clients/organizations/${project.organizationId}`}
                        className="font-mono text-xs underline-offset-2 hover:underline"
                      >
                        {project.organizationId}
                      </Link>
                    ) : (
                      "—"
                    )}
                  </p>
                </div>
                <div>
                  <p className="text-xs text-muted-foreground">Billing profile</p>
                  <div className="mt-0.5 flex items-center gap-2 text-sm">
                    {project?.billingProfileId ? (
                      <Link
                        to={`/clients/billing-profiles/${project.billingProfileId}`}
                        className="font-mono text-xs underline-offset-2 hover:underline"
                      >
                        {project.billingProfileId}
                      </Link>
                    ) : (
                      <span className="text-muted-foreground">Inherited from organization</span>
                    )}
                    <Button variant="outline" size="sm" onClick={() => setBpOpen(true)}>
                      <CreditCard />
                      Change
                    </Button>
                  </div>
                </div>
                <Field label="Created" value={fmtDateTime(project?.createdAt)} />
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle className="text-base">Cloud services</CardTitle>
              </CardHeader>
              <CardContent>
                {(project?.services ?? []).length === 0 ? (
                  <p className="text-sm text-muted-foreground">No external service attached.</p>
                ) : (
                  <div className="space-y-2">
                    {(project?.services ?? []).map((s, i) => (
                      <div key={i} className="flex flex-wrap items-center gap-x-6 gap-y-1 text-sm">
                        <span>
                          <span className="text-xs text-muted-foreground">Service: </span>
                          <span className="font-mono text-xs">{s.serviceId ?? "—"}</span>
                        </span>
                        <span>
                          <span className="text-xs text-muted-foreground">External project: </span>
                          <span className="font-mono text-xs">{s.externalProjectId ?? "—"}</span>
                        </span>
                      </div>
                    ))}
                  </div>
                )}
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle className="text-base">Public networks</CardTitle>
              </CardHeader>
              <CardContent>
                {!externalServiceId ? (
                  <p className="text-sm text-muted-foreground">No cloud service attached.</p>
                ) : (
                  <div className="space-y-4">
                    <div className="flex items-center gap-2">
                      <Switch id="all-public-networks" checked={allPublicNets} onCheckedChange={setAllPublicNets} />
                      <label htmlFor="all-public-networks" className="text-sm">
                        All public networks (default)
                      </label>
                    </div>
                    {!allPublicNets &&
                      (publicNets.isLoading ? (
                        <Skeleton className="h-10" />
                      ) : publicNets.error ? (
                        <p className="text-sm text-muted-foreground">{(publicNets.error as Error).message}</p>
                      ) : (publicNets.data?.data ?? []).length === 0 ? (
                        <p className="text-sm text-muted-foreground">No public networks available.</p>
                      ) : (
                        <div className="space-y-2">
                          {(publicNets.data?.data ?? []).map((n) => {
                            const nid = n.id
                            if (!nid) return null
                            return (
                              <label key={nid} className="flex items-center gap-2 text-sm">
                                <Checkbox
                                  checked={publicNetChoice.includes(nid)}
                                  onCheckedChange={(on) =>
                                    setPublicNetChoice((prev) =>
                                      on === true ? [...prev, nid] : prev.filter((x) => x !== nid),
                                    )
                                  }
                                />
                                <span>{n.name || nid}</span>
                                <span className="font-mono text-xs text-muted-foreground">{nid}</span>
                              </label>
                            )
                          })}
                        </div>
                      ))}
                    <Button size="sm" disabled={savePublicNets.isPending} onClick={() => savePublicNets.mutate()}>
                      {savePublicNets.isPending ? "Saving…" : "Save"}
                    </Button>
                  </div>
                )}
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle className="text-base">Resource counts</CardTitle>
              </CardHeader>
              <CardContent>
                {counts.isLoading ? (
                  <Skeleton className="h-10" />
                ) : counts.error ? (
                  <p className="text-sm text-muted-foreground">{(counts.error as Error).message}</p>
                ) : (
                  <div className="flex flex-wrap gap-3">
                    <div className="rounded-md border px-3 py-1.5 text-sm">
                      <span className="text-xs text-muted-foreground">TOTAL </span>
                      <span className="font-medium tabular-nums">{counts.data?.TOTAL ?? 0}</span>
                    </div>
                    {countEntries.map(([type, n]) => (
                      <div key={type} className="rounded-md border px-3 py-1.5 text-sm">
                        <span className="font-mono text-xs text-muted-foreground">{type} </span>
                        <span className="font-medium tabular-nums">{n}</span>
                      </div>
                    ))}
                  </div>
                )}
              </CardContent>
            </Card>
          </TabsContent>

          <TabsContent value="members" className="mt-4">
            <div className="mb-3 flex justify-end">
              <Button size="sm" onClick={() => setAddMemberOpen(true)}>
                <Plus />
                Add member
              </Button>
            </div>
            {members.isLoading ? (
              <Skeleton className="h-32" />
            ) : members.error ? (
              <ErrorPanel error={members.error} />
            ) : (members.data?.data ?? []).length === 0 ? (
              <EmptyState icon={Users} title="No members" hint="Add a user to this project." />
            ) : (
              <Card className="overflow-hidden py-0">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Member</TableHead>
                      <TableHead>Sub</TableHead>
                      <TableHead>Role</TableHead>
                      <TableHead className="w-10" />
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {(members.data?.data ?? []).map((m, i) => {
                      const role = memberRoleOf(m.sub)
                      return (
                        <TableRow key={m.sub ?? i}>
                          <TableCell>
                            <span className="font-medium">
                              {[m.firstName, m.lastName].filter(Boolean).join(" ") || m.email || "—"}
                            </span>
                            {m.email && (m.firstName || m.lastName) ? (
                              <span className="ml-2 text-xs text-muted-foreground">{m.email}</span>
                            ) : null}
                          </TableCell>
                          <TableCell className="font-mono text-xs text-muted-foreground">{m.sub ?? "—"}</TableCell>
                          <TableCell className="capitalize">{role.toLowerCase()}</TableCell>
                          <TableCell>
                            <Button
                              variant="ghost"
                              size="icon"
                              aria-label="Remove member"
                              disabled={role === "OWNER"}
                              onClick={() => setMemberToRemove(m)}
                            >
                              <Trash2 className="text-muted-foreground" />
                            </Button>
                          </TableCell>
                        </TableRow>
                      )
                    })}
                  </TableBody>
                </Table>
              </Card>
            )}
          </TabsContent>

          <TabsContent value="resources" className="mt-4">
            {resources.isLoading ? (
              <Skeleton className="h-32" />
            ) : resources.error ? (
              <ErrorPanel error={resources.error} />
            ) : (resources.data?.data ?? []).length === 0 ? (
              <EmptyState icon={Server} title="No cloud resources" hint="This project has no cached cloud resources." />
            ) : (
              <Card className="overflow-hidden py-0">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Type</TableHead>
                      <TableHead>Name</TableHead>
                      <TableHead>Status</TableHead>
                      <TableHead>External ID</TableHead>
                      <TableHead>Created</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {(resources.data?.data ?? []).map((cr) => (
                      <TableRow key={cr.id ?? cr.externalId}>
                        <TableCell className="font-mono text-xs">{cr.type ?? "—"}</TableCell>
                        <TableCell className="font-medium">{dataField(cr, "name") ?? "—"}</TableCell>
                        <TableCell>
                          <StatusBadge status={dataField(cr, "status")} />
                        </TableCell>
                        <TableCell className="font-mono text-xs text-muted-foreground">
                          {cr.externalId ?? "—"}
                        </TableCell>
                        <TableCell className="text-muted-foreground">{fmtDate(cr.createdAt)}</TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </Card>
            )}
          </TabsContent>
        </Tabs>
      )}

      {/* Status confirm */}
      <Dialog open={!!statusConfirm} onOpenChange={(o) => !o && setStatusConfirm(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{statusConfirm === "DISABLED" ? "Disable project" : "Enable project"}</DialogTitle>
            <DialogDescription>
              {statusConfirm === "DISABLED"
                ? "Pauses every server in the project before disabling it."
                : "Unpauses the project's servers and re-enables it."}{" "}
              Project: <span className="font-medium">{project?.name}</span>
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setStatusConfirm(null)}>
              Cancel
            </Button>
            <Button
              variant={statusConfirm === "DISABLED" ? "destructive" : "default"}
              disabled={updateStatus.isPending}
              onClick={() => statusConfirm && updateStatus.mutate(statusConfirm)}
            >
              {updateStatus.isPending ? "Working…" : statusConfirm === "DISABLED" ? "Disable" : "Enable"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete confirm */}
      <Dialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete project</DialogTitle>
            <DialogDescription>
              Schedules {project?.name ?? "this project"} for deletion, including its cloud resources. This cannot be
              undone once the deletion runs.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteOpen(false)}>
              Cancel
            </Button>
            <Button variant="destructive" disabled={deleteProject.isPending} onClick={() => deleteProject.mutate()}>
              {deleteProject.isPending ? "Deleting…" : "Delete project"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Change billing profile */}
      <Dialog open={bpOpen} onOpenChange={setBpOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Change billing profile</DialogTitle>
            <DialogDescription>
              Assigns a billing profile to this project. Charges accrue against the selected profile.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <Select value={bpChoice} onValueChange={setBpChoice}>
              <SelectTrigger className="w-full">
                <SelectValue placeholder={bps.isLoading ? "Loading billing profiles…" : "Pick a billing profile"} />
              </SelectTrigger>
              <SelectContent>
                {(bps.data?.data ?? []).map((bp) =>
                  bp.id ? (
                    <SelectItem key={bp.id} value={bp.id}>
                      {[bp.name || bp.email || bp.id, bp.currency].filter(Boolean).join(" · ")}
                    </SelectItem>
                  ) : null,
                )}
              </SelectContent>
            </Select>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setBpOpen(false)}>
              Cancel
            </Button>
            <Button disabled={!bpChoice || changeBp.isPending} onClick={() => changeBp.mutate()}>
              {changeBp.isPending ? "Saving…" : "Assign billing profile"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Add member */}
      <Dialog open={addMemberOpen} onOpenChange={setAddMemberOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Add member</DialogTitle>
            <DialogDescription>Adds an existing user to {project?.name ?? "this project"}.</DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <Select value={memberChoice} onValueChange={setMemberChoice}>
              <SelectTrigger className="w-full">
                <SelectValue placeholder={users.isLoading ? "Loading users…" : "Pick a user"} />
              </SelectTrigger>
              <SelectContent>
                {(users.data?.data ?? []).map((u) =>
                  u.id ? (
                    <SelectItem key={u.id} value={u.id}>
                      {u.email ?? [u.firstName, u.lastName].filter(Boolean).join(" ") ?? u.id}
                    </SelectItem>
                  ) : null,
                )}
              </SelectContent>
            </Select>
            <Select value={memberRole} onValueChange={setMemberRole}>
              <SelectTrigger className="w-full">
                <SelectValue placeholder="Role" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="MEMBER">Member</SelectItem>
                <SelectItem value="OWNER">Owner</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setAddMemberOpen(false)}>
              Cancel
            </Button>
            <Button disabled={!memberChoice || addMember.isPending} onClick={() => addMember.mutate()}>
              {addMember.isPending ? "Adding…" : "Add member"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Remove member confirm */}
      <Dialog open={!!memberToRemove} onOpenChange={(o) => !o && setMemberToRemove(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Remove member</DialogTitle>
            <DialogDescription>
              Removes {memberToRemove?.email ?? memberToRemove?.sub ?? "this user"} from{" "}
              {project?.name ?? "the project"}.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setMemberToRemove(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              disabled={removeMember.isPending}
              onClick={() => memberToRemove?.sub && removeMember.mutate(memberToRemove.sub)}
            >
              {removeMember.isPending ? "Removing…" : "Remove member"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
