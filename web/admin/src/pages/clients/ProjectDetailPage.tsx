import { useEffect, useState } from "react"
import { Link, useNavigate, useParams } from "react-router-dom"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Building2, CreditCard, Pause, Play, Plus, RefreshCw, Server, Trash2, Users } from "lucide-react"
import { toast } from "sonner"
import { PageHeader } from "@/components/layout/PageHeader"
import { EmptyState } from "@/components/empty-state"
import { StatusBadge } from "@/components/status-badge"
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from "@/components/ui/breadcrumb"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
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
import { useAdminGet, useAdminList, useTabParam } from "@/lib/hooks"

// GET /admin/project/{id} (handler.go rawByID "project") — the project doc shaped (_id→id).
type ProjectDoc = {
  id?: string
  name?: string
  status?: string
  organizationId?: string
  billingProfileId?: string
  memberships?: Array<{ sub?: string; role?: string }>
  // ceph-s3 bindings carry provider/region/rgwUid instead of an (OpenStack) externalProjectId.
  services?: Array<{ serviceId?: string; externalProjectId?: string; provider?: string; region?: string; rgwUid?: string }>
  // absent/null = all external networks allowed; array = allow-list of Neutron network ids.
  publicNetworkIds?: string[] | null
  // false/absent = the client can't choose an external network (server auto-picks); true = client picks.
  publicNetworksVisible?: boolean
  // Admin-managed per-project quota; gpu = {model alias (or "*") → device limit}.
  quota?: { gpu?: Record<string, number> }
  createdAt?: string
}

// GET /admin/service/{id}/gpu-info (cloudadmin.go gpuInfo) — per-region GPU capacity.
type GpuRegionCapacity = { region: string; gpus: Array<{ name: string; total: number; inUse: number }> }

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
      <p className="text-eyebrow mb-1">{label}</p>
      <p className={mono ? "font-mono text-xs" : "text-sm"}>{value || "—"}</p>
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
  const [tab, setTab] = useTabParam("overview")
  const { data: project, isLoading, error } = useAdminGet<ProjectDoc>(projectPath, !!id)
  // GET /admin/project/{id}/resources/counts (projectmut.go projectResourceCounts) — {TYPE: n, TOTAL: n}.
  const counts = useAdminGet<Record<string, number>>(`${projectPath}/resources/counts`, !!id)
  const members = useAdminList<MemberUser>(`${projectPath}/members`, !!id)
  const resources = useAdminList<CloudResource>(`/admin/cloud-resource/project/${id}`, !!id)

  const [statusConfirm, setStatusConfirm] = useState<"ENABLED" | "DISABLED" | null>(null)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [bpOpen, setBpOpen] = useState(false)
  const [bpChoice, setBpChoice] = useState("")
  const [orgOpen, setOrgOpen] = useState(false)
  const [orgChoice, setOrgChoice] = useState("")
  const [addMemberOpen, setAddMemberOpen] = useState(false)
  const [memberChoice, setMemberChoice] = useState("")
  const [memberRole, setMemberRole] = useState("MEMBER")
  const [memberToRemove, setMemberToRemove] = useState<MemberUser | null>(null)
  const [attachOpen, setAttachOpen] = useState(false)
  const [attachChoice, setAttachChoice] = useState("")

  const bps = useAdminList<BillingProfile>("/admin/billing-profile", bpOpen)
  const orgsList = useAdminList<{ id: string; name?: string }>("/admin/organizations", orgOpen)
  const users = useAdminList<AdminUser>("/admin/user", addMemberOpen)
  const providers = useAdminList<{ id: string; name?: string; status?: string; config?: { provider?: string } }>(
    "/admin/service",
    attachOpen,
  )

  const externalServiceId = project?.services?.find((s) => s.serviceId)?.serviceId ?? ""
  const publicNets = useAdminList<PublicNetwork>(
    `/admin/cloud-resource/public-networks/${externalServiceId}`,
    !!externalServiceId,
  )
  const [allPublicNets, setAllPublicNets] = useState(true)
  const [publicNetChoice, setPublicNetChoice] = useState<string[]>([])
  const [publicNetsVisible, setPublicNetsVisible] = useState(false)
  useEffect(() => {
    setAllPublicNets(!project?.publicNetworkIds)
    setPublicNetChoice(project?.publicNetworkIds ?? [])
    setPublicNetsVisible(project?.publicNetworksVisible === true)
  }, [project?.publicNetworkIds, project?.publicNetworksVisible])

  const enabled = (project?.status ?? "").toUpperCase() === "ENABLED"

  // GPU quota tab state: rows derive from project.quota.gpu; the model picker offers the
  // provider's live GPU models (placement gpu-info) plus the "*" wildcard.
  const gpuInfo = useAdminList<GpuRegionCapacity>(`/admin/service/${externalServiceId}/gpu-info`, !!externalServiceId)
  const [quotaRows, setQuotaRows] = useState<Array<{ model: string; limit: string }>>([])
  const [quotaModel, setQuotaModel] = useState("")
  const [quotaLimit, setQuotaLimit] = useState("")
  useEffect(() => {
    setQuotaRows(Object.entries(project?.quota?.gpu ?? {}).map(([model, limit]) => ({ model, limit: String(limit) })))
  }, [project?.quota])
  const gpuModelOptions = [
    "*",
    ...new Set((gpuInfo.data?.data ?? []).flatMap((r) => r.gpus.map((g) => g.name))),
  ].filter((m) => !quotaRows.some((row) => row.model === m))

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

  // GET /admin/project/{id}/external-service/{esid} (projectmut.go projectAddExternalService):
  // bootstraps the project onto that provider — a Keystone tenant for openstack, an RGW user +
  // stored S3 credential for ceph-s3. Idempotent, so a re-attach is safe.
  const attachService = useMutation({
    mutationFn: (esID: string) => apiFetch(`${projectPath}/external-service/${esID}`),
    onSuccess: () => {
      toast.success("Cloud provider attached")
      setAttachOpen(false)
      setAttachChoice("")
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

  // PUT /admin/project/{id} — assign/reassign the project's organization. Overwrites
  // name/billingProfileId/organizationId together, so resend the current name + bp.
  // Lets an operator adopt an imported (org-less) project into an organization.
  const changeOrg = useMutation({
    mutationFn: () =>
      apiFetch(projectPath, {
        method: "PUT",
        body: {
          name: project?.name ?? "",
          organizationId: orgChoice,
          billingProfileId: project?.billingProfileId ?? "",
        },
      }),
    onSuccess: () => {
      toast.success("Organization updated")
      setOrgOpen(false)
      setOrgChoice("")
      invalidateProject()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  // PUT /admin/project/{id}/quota (projectquota.go) — stores {gpu:{model→limit}}; enforcement is
  // the server create/resize gate. An empty object clears the quota (unlimited).
  const saveQuota = useMutation({
    mutationFn: (rows: Array<{ model: string; limit: string }>) => {
      const gpu = Object.fromEntries(
        rows.filter((r) => r.model && r.limit.trim() !== "").map((r) => [r.model, Number(r.limit)]),
      )
      return apiFetch(`${projectPath}/quota`, {
        method: "PUT",
        body: Object.keys(gpu).length ? { gpu } : {},
      })
    },
    onSuccess: () => {
      toast.success("Project quota saved")
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
        body: {
          publicNetworkIds: allPublicNets ? null : publicNetChoice,
          publicNetworksVisible: publicNetsVisible,
        },
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
        eyebrow="Clients"
        description="Client project detail."
        breadcrumb={
          <Breadcrumb>
            <BreadcrumbList>
              <BreadcrumbItem>
                <BreadcrumbLink asChild>
                  <Link to="/clients/projects">Projects</Link>
                </BreadcrumbLink>
              </BreadcrumbItem>
              <BreadcrumbSeparator />
              <BreadcrumbItem>
                <BreadcrumbPage>{project?.name ?? id}</BreadcrumbPage>
              </BreadcrumbItem>
            </BreadcrumbList>
          </Breadcrumb>
        }
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => syncProject.mutate()} disabled={syncProject.isPending}>
              <RefreshCw className={syncProject.isPending ? "size-4 animate-spin" : "size-4"} /> Sync
            </Button>
            <Button
              variant="outline"
              size="sm"
              disabled={!project}
              onClick={() => setStatusConfirm(enabled ? "DISABLED" : "ENABLED")}
            >
              {enabled ? <Pause className="size-4" /> : <Play className="size-4" />}
              {enabled ? "Disable" : "Enable"}
            </Button>
            <Button variant="destructive" size="sm" disabled={!project} onClick={() => setDeleteOpen(true)}>
              <Trash2 className="size-4" /> Delete project
            </Button>
          </>
        }
      />

      {isLoading ? (
        <Skeleton className="h-64" />
      ) : error ? (
        <ErrorPanel error={error} />
      ) : (
        <Tabs value={tab} onValueChange={setTab}>
          <TabsList>
            <TabsTrigger value="overview">Overview</TabsTrigger>
            <TabsTrigger value="members">
              Members{members.data?.data ? ` (${members.data.data.length})` : ""}
            </TabsTrigger>
            <TabsTrigger value="resources">
              Cloud resources{counts.data?.TOTAL != null ? ` (${counts.data.TOTAL})` : ""}
            </TabsTrigger>
            <TabsTrigger value="quota">Quota</TabsTrigger>
          </TabsList>

          <TabsContent value="overview" className="mt-4 space-y-6">
            <Card>
              <CardHeader>
                <CardTitle className="text-base">Project</CardTitle>
              </CardHeader>
              <CardContent className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                <Field label="Name" value={project?.name} />
                <div>
                  <p className="text-eyebrow mb-1">Status</p>
                  <StatusBadge status={project?.status} />
                </div>
                <Field label="ID" value={project?.id} mono />
                <div>
                  <p className="text-eyebrow mb-1">Organization</p>
                  <div className="flex items-center gap-2 text-sm">
                    {project?.organizationId ? (
                      <Link
                        to={`/clients/organizations/${project.organizationId}`}
                        className="font-mono text-xs underline-offset-2 hover:underline"
                      >
                        {project.organizationId}
                      </Link>
                    ) : (
                      <span className="text-muted-foreground">None (imported / unassigned)</span>
                    )}
                    <Button variant="outline" size="sm" onClick={() => setOrgOpen(true)}>
                      <Building2 className="size-4" /> {project?.organizationId ? "Change" : "Assign"}
                    </Button>
                  </div>
                </div>
                <div>
                  <p className="text-eyebrow mb-1">Billing profile</p>
                  <div className="flex items-center gap-2 text-sm">
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
                      <CreditCard className="size-4" /> Change
                    </Button>
                  </div>
                </div>
                <Field label="Created" value={fmtDateTime(project?.createdAt)} />
              </CardContent>
            </Card>

            <Card>
              <CardHeader className="flex flex-row items-center justify-between">
                <CardTitle className="text-base">Cloud services</CardTitle>
                <Button variant="outline" size="sm" onClick={() => setAttachOpen(true)}>
                  <Plus className="size-4" /> Attach provider
                </Button>
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
                        {s.rgwUid ? (
                          <span>
                            <span className="text-xs text-muted-foreground">RGW user: </span>
                            <span className="font-mono text-xs">{s.rgwUid}</span>
                          </span>
                        ) : (
                          <span>
                            <span className="text-xs text-muted-foreground">External project: </span>
                            <span className="font-mono text-xs">{s.externalProjectId ?? "—"}</span>
                          </span>
                        )}
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
                      <Switch
                        id="public-nets-visible"
                        checked={publicNetsVisible}
                        onCheckedChange={setPublicNetsVisible}
                      />
                      <label htmlFor="public-nets-visible" className="text-sm">
                        Let users pick the external network
                        <span className="ml-1 text-xs text-muted-foreground">
                          (off = the server auto-assigns it)
                        </span>
                      </label>
                    </div>
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
                <Plus className="size-4" /> Add member
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
                              size="icon-sm"
                              aria-label={`Remove ${m.email ?? m.sub ?? "member"}`}
                              disabled={role === "OWNER"}
                              onClick={() => setMemberToRemove(m)}
                            >
                              <Trash2 className="size-4 text-muted-foreground" />
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
                      <TableHead>Region</TableHead>
                      <TableHead>Status</TableHead>
                      <TableHead>External ID</TableHead>
                      <TableHead>Created</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {(resources.data?.data ?? []).map((cr) => (
                      <TableRow
                        key={cr.id ?? cr.externalId}
                        className={cr.id ? "cursor-pointer" : undefined}
                        onClick={() => cr.id && navigate(`/clients/cloud-resources/${cr.id}`)}
                      >
                        <TableCell className="font-mono text-xs">{cr.type ?? "—"}</TableCell>
                        <TableCell>
                          {cr.id ? (
                            <Link
                              to={`/clients/cloud-resources/${cr.id}`}
                              className="inline-block py-1 font-medium hover:underline"
                              onClick={(e) => e.stopPropagation()}
                            >
                              {dataField(cr, "name") ?? cr.externalId ?? "—"}
                            </Link>
                          ) : (
                            <span className="font-medium">{dataField(cr, "name") ?? "—"}</span>
                          )}
                        </TableCell>
                        <TableCell className="font-mono text-xs text-muted-foreground">{cr.region ?? "—"}</TableCell>
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

          <TabsContent value="quota" className="mt-4">
            <Card>
              <CardHeader>
                <CardTitle className="text-base">GPU quota</CardTitle>
              </CardHeader>
              <CardContent className="space-y-4">
                <p className="text-sm text-muted-foreground">
                  Per-model GPU device limits, enforced when servers are created or resized through
                  Stratos. No entry = unlimited; "*" applies to any model without its own row.
                  Horizon-direct usage on imported projects bypasses this gate.
                </p>
                {quotaRows.length === 0 ? (
                  <p className="text-sm text-muted-foreground">No GPU limits configured.</p>
                ) : (
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>GPU model</TableHead>
                        <TableHead className="w-32">Limit</TableHead>
                        <TableHead />
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {quotaRows.map((row, i) => (
                        <TableRow key={row.model}>
                          <TableCell className="font-mono text-xs">{row.model}</TableCell>
                          <TableCell>
                            <Input
                              type="number"
                              min={0}
                              className="w-24"
                              aria-label={`Limit for ${row.model}`}
                              value={row.limit}
                              onChange={(e) =>
                                setQuotaRows((rows) => rows.map((r, idx) => (idx === i ? { ...r, limit: e.target.value } : r)))
                              }
                            />
                          </TableCell>
                          <TableCell className="text-right">
                            <Button
                              variant="ghost"
                              size="sm"
                              aria-label={`Remove limit for ${row.model}`}
                              onClick={() => setQuotaRows((rows) => rows.filter((_, idx) => idx !== i))}
                            >
                              Remove
                            </Button>
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                )}
                <div className="flex flex-wrap items-end gap-3">
                  <div className="space-y-1.5">
                    <p className="text-eyebrow">GPU model</p>
                    <Select value={quotaModel} onValueChange={setQuotaModel}>
                      <SelectTrigger className="w-56" aria-label="GPU model">
                        <SelectValue placeholder="Select model" />
                      </SelectTrigger>
                      <SelectContent>
                        {gpuModelOptions.map((m) => (
                          <SelectItem key={m} value={m}>
                            {m === "*" ? "* (any model)" : m}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                  <div className="space-y-1.5">
                    <p className="text-eyebrow">Limit</p>
                    <Input
                      type="number"
                      min={0}
                      className="w-24"
                      aria-label="Limit"
                      value={quotaLimit}
                      onChange={(e) => setQuotaLimit(e.target.value)}
                    />
                  </div>
                  <Button
                    variant="outline"
                    disabled={!quotaModel || quotaLimit.trim() === ""}
                    onClick={() => {
                      setQuotaRows((rows) => [...rows, { model: quotaModel, limit: quotaLimit }])
                      setQuotaModel("")
                      setQuotaLimit("")
                    }}
                  >
                    Add limit
                  </Button>
                  <Button onClick={() => saveQuota.mutate(quotaRows)} disabled={saveQuota.isPending}>
                    {saveQuota.isPending ? "Saving…" : "Save quota"}
                  </Button>
                </div>
              </CardContent>
            </Card>
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
              <SelectTrigger className="w-full" aria-label="Billing profile">
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

      <Dialog
        open={attachOpen}
        onOpenChange={(o) => {
          setAttachOpen(o)
          if (!o) setAttachChoice("")
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Attach cloud provider</DialogTitle>
            <DialogDescription>
              Provisions this project on the provider: a Keystone tenant for OpenStack, a dedicated RGW user for
              Ceph S3. Projects created after a provider was added pick it up on their own — this is for the ones
              that already existed.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <Select value={attachChoice} onValueChange={setAttachChoice}>
              <SelectTrigger className="w-full" aria-label="Cloud provider">
                <SelectValue placeholder={providers.isLoading ? "Loading providers…" : "Pick a provider"} />
              </SelectTrigger>
              <SelectContent>
                {(providers.data?.data ?? [])
                  .filter((p) => p.id && !(project?.services ?? []).some((s) => s.serviceId === p.id))
                  .map((p) => (
                    <SelectItem key={p.id} value={p.id}>
                      {`${p.name ?? p.id} · ${p.config?.provider ?? "openstack"}`}
                    </SelectItem>
                  ))}
              </SelectContent>
            </Select>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setAttachOpen(false)}>
              Cancel
            </Button>
            <Button disabled={!attachChoice || attachService.isPending} onClick={() => attachService.mutate(attachChoice)}>
              {attachService.isPending ? "Attaching…" : "Attach provider"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={orgOpen} onOpenChange={setOrgOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Change organization</DialogTitle>
            <DialogDescription>
              Assigns this project to an organization. Useful for imported projects that arrived without one.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <Select value={orgChoice} onValueChange={setOrgChoice}>
              <SelectTrigger className="w-full" aria-label="Organization">
                <SelectValue placeholder={orgsList.isLoading ? "Loading organizations…" : "Pick an organization"} />
              </SelectTrigger>
              <SelectContent>
                {(orgsList.data?.data ?? []).map((o) =>
                  o.id ? (
                    <SelectItem key={o.id} value={o.id}>
                      {o.name ? `${o.name} · ${o.id}` : o.id}
                    </SelectItem>
                  ) : null,
                )}
              </SelectContent>
            </Select>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setOrgOpen(false)}>
              Cancel
            </Button>
            <Button disabled={!orgChoice || changeOrg.isPending} onClick={() => changeOrg.mutate()}>
              {changeOrg.isPending ? "Saving…" : "Assign organization"}
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
              <SelectTrigger className="w-full" aria-label="User">
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
              <SelectTrigger className="w-full" aria-label="Role">
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
