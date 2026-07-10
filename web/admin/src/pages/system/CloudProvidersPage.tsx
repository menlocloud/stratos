import { useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Cloud, Plus } from "lucide-react"
import { useNavigate } from "react-router-dom"
import { toast } from "sonner"
import { PageHeader } from "@/components/layout/PageHeader"
import { EmptyState } from "@/components/empty-state"
import { StatusBadge } from "@/components/status-badge"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Skeleton } from "@/components/ui/skeleton"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { apiFetch } from "@/lib/api"
import { useAdminList } from "@/lib/hooks"

const LIST_PATH = "/admin/service"

export type CloudProvider = {
  id: string
  name?: string
  type?: string
  status?: string
  config?: {
    identityUrl?: string
    provider?: string
    regions?: Record<string, unknown>
    services?: Record<string, Record<string, boolean>>
    auth?: Record<string, unknown>
    // ceph-s3 provider fields (config.provider === "ceph-s3")
    s3Endpoint?: string
    adminApiUrl?: string
    s3WebsiteEndpoint?: string
    region?: string
    uidPrefix?: string
    defaultQuotaGiB?: number
  }
}

// ── create form ──────────────────────────────────────────────────────────────
type FormState = {
  name: string
  identityUrl: string
  adminUsername: string
  adminProjectName: string
  adminDomainName: string
  adminPassword: string
  region: string
  shared: boolean
}

const emptyForm: FormState = {
  name: "",
  identityUrl: "",
  adminUsername: "",
  adminProjectName: "",
  adminDomainName: "Default",
  adminPassword: "",
  region: "RegionOne",
  shared: false,
}

const formValid = (f: FormState) =>
  [f.name, f.identityUrl, f.adminUsername, f.adminProjectName, f.adminDomainName, f.adminPassword, f.region]
    .every((v) => v.trim() !== "")

// formToBody builds the ExternalService document the Go create handler (POST /admin/service) reads —
// the same shape as deploy/seed/external-service-dev.json. Services stay empty here; the operator
// enables per-region services + finishes Features/Quota on the detail page after "Test connection".
// A non-blank region displayName is required for the client create-form Location dropdown → use the
// region name. The secret carries the OpenStack admin password (stripped from every read response).
function formToBody(f: FormState) {
  const region = f.region.trim()
  return {
    name: f.name.trim(),
    type: "CLOUD",
    status: "PUBLIC",
    config: {
      identityUrl: f.identityUrl.trim(),
      provider: "openstack",
      shared: f.shared,
      auth: {
        adminAuthType: "password",
        adminUsername: f.adminUsername.trim(),
        adminProjectName: f.adminProjectName.trim(),
        adminDomainName: f.adminDomainName.trim(),
      },
      regions: { [region]: { name: region, country: "", displayName: region } },
      services: {},
      features: {},
      provisioning: { projectRoles: [] },
    },
    secret: { adminPassword: f.adminPassword },
  }
}

// ── ceph-s3 create form ──────────────────────────────────────────────────────
type CephFormState = {
  name: string
  s3Endpoint: string
  adminApiUrl: string
  s3WebsiteEndpoint: string
  region: string
  uidPrefix: string
  defaultQuotaGiB: string
  adminAccessKey: string
  adminSecretKey: string
}

const emptyCephForm: CephFormState = {
  name: "",
  s3Endpoint: "",
  adminApiUrl: "",
  s3WebsiteEndpoint: "",
  region: "us-east-1",
  uidPrefix: "",
  defaultQuotaGiB: "",
  adminAccessKey: "",
  adminSecretKey: "",
}

// defaultQuotaGiB is an integer server-side (config.defaultQuotaGiB) — empty means "no quota".
const cephQuotaValid = (v: string) => v.trim() === "" || (Number.isInteger(Number(v)) && Number(v) > 0)

const cephFormValid = (f: CephFormState) =>
  [f.name, f.s3Endpoint, f.adminApiUrl, f.region, f.adminAccessKey, f.adminSecretKey].every((v) => v.trim() !== "") &&
  cephQuotaValid(f.defaultQuotaGiB)

// cephFormToBody builds the ceph-s3 ExternalService document (see docs/cloud-integration.md
// "Provider config"): no Keystone at all — the S3 + Admin Ops endpoints drive everything, and the
// secret carries the RGW admin keys (caps users=*;buckets=*;usage=*). object-store is the ONLY
// service a ceph-s3 provider serves, so it is enabled here directly — no discover round-trip exists.
function cephFormToBody(f: CephFormState) {
  const region = f.region.trim()
  const trimURL = (s: string) => s.trim().replace(/\/+$/, "")
  return {
    name: f.name.trim(),
    type: "CLOUD",
    status: "PUBLIC",
    config: {
      provider: "ceph-s3",
      s3Endpoint: trimURL(f.s3Endpoint),
      adminApiUrl: trimURL(f.adminApiUrl),
      ...(f.s3WebsiteEndpoint.trim() ? { s3WebsiteEndpoint: trimURL(f.s3WebsiteEndpoint) } : {}),
      region,
      ...(f.uidPrefix.trim() ? { uidPrefix: f.uidPrefix.trim() } : {}),
      ...(f.defaultQuotaGiB.trim() ? { defaultQuotaGiB: Number(f.defaultQuotaGiB) } : {}),
      services: { "object-store": { [region]: true } },
    },
    // Trim BOTH keys — a pasted secret with a trailing newline would validate but break SigV4.
    secret: { adminAccessKey: f.adminAccessKey.trim(), adminSecretKey: f.adminSecretKey.trim() },
  }
}

function CephProviderForm({ form, setForm }: { form: CephFormState; setForm: (f: CephFormState) => void }) {
  return (
    <div className="grid gap-4">
      <div className="grid gap-2">
        <Label htmlFor="cs-name">Display name</Label>
        <Input id="cs-name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="ceph-s3" />
      </div>
      <div className="grid gap-2">
        <Label htmlFor="cs-s3">S3 endpoint</Label>
        <Input id="cs-s3" value={form.s3Endpoint} onChange={(e) => setForm({ ...form, s3Endpoint: e.target.value })} placeholder="https://s3.example.com" />
      </div>
      <div className="grid gap-2">
        <Label htmlFor="cs-admin">Admin Ops URL</Label>
        <Input id="cs-admin" value={form.adminApiUrl} onChange={(e) => setForm({ ...form, adminApiUrl: e.target.value })} placeholder="https://s3.example.com/admin" />
      </div>
      <div className="grid gap-2">
        <Label htmlFor="cs-web">Website endpoint (optional)</Label>
        <Input id="cs-web" value={form.s3WebsiteEndpoint} onChange={(e) => setForm({ ...form, s3WebsiteEndpoint: e.target.value })} placeholder="https://s3-website.example.com" />
      </div>
      <div className="grid grid-cols-2 gap-4">
        <div className="grid gap-2">
          <Label htmlFor="cs-region">Region (RGW zonegroup)</Label>
          <Input id="cs-region" value={form.region} onChange={(e) => setForm({ ...form, region: e.target.value })} placeholder="us-east-1" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="cs-prefix">RGW uid prefix (optional)</Label>
          <Input id="cs-prefix" value={form.uidPrefix} onChange={(e) => setForm({ ...form, uidPrefix: e.target.value })} placeholder="prod_" />
        </div>
      </div>
      <div className="grid grid-cols-2 gap-4">
        <div className="grid gap-2">
          <Label htmlFor="cs-key">Admin access key</Label>
          <Input id="cs-key" value={form.adminAccessKey} onChange={(e) => setForm({ ...form, adminAccessKey: e.target.value })} autoComplete="off" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="cs-secret">Admin secret key</Label>
          <Input
            id="cs-secret"
            type="password"
            value={form.adminSecretKey}
            onChange={(e) => setForm({ ...form, adminSecretKey: e.target.value })}
            autoComplete="new-password"
          />
        </div>
      </div>
      <div className="grid gap-2">
        <Label htmlFor="cs-quota">Default per-project quota (GiB, optional)</Label>
        <Input id="cs-quota" value={form.defaultQuotaGiB} onChange={(e) => setForm({ ...form, defaultQuotaGiB: e.target.value })} placeholder="100" />
      </div>
      <p className="text-xs text-muted-foreground">
        The admin keys belong to an RGW admin user with caps <code>users=*;buckets=*;usage=*</code>.
      </p>
    </div>
  )
}

function ProviderForm({ form, setForm }: { form: FormState; setForm: (f: FormState) => void }) {
  return (
    <div className="grid gap-4">
      <div className="grid gap-2">
        <Label htmlFor="cp-name">Display name</Label>
        <Input id="cp-name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="dev region" />
      </div>
      <div className="grid gap-2">
        <Label htmlFor="cp-identity">Identity URL (Keystone v3)</Label>
        <Input
          id="cp-identity"
          value={form.identityUrl}
          onChange={(e) => setForm({ ...form, identityUrl: e.target.value })}
          placeholder="https://keystone.example.com:5000/v3"
        />
      </div>
      <div className="grid grid-cols-2 gap-4">
        <div className="grid gap-2">
          <Label htmlFor="cp-user">Admin username</Label>
          <Input id="cp-user" value={form.adminUsername} onChange={(e) => setForm({ ...form, adminUsername: e.target.value })} autoComplete="off" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="cp-project">Admin project</Label>
          <Input id="cp-project" value={form.adminProjectName} onChange={(e) => setForm({ ...form, adminProjectName: e.target.value })} placeholder="admin" />
        </div>
      </div>
      <div className="grid grid-cols-2 gap-4">
        <div className="grid gap-2">
          <Label htmlFor="cp-domain">Admin domain</Label>
          <Input id="cp-domain" value={form.adminDomainName} onChange={(e) => setForm({ ...form, adminDomainName: e.target.value })} placeholder="Default" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="cp-region">Region</Label>
          <Input id="cp-region" value={form.region} onChange={(e) => setForm({ ...form, region: e.target.value })} placeholder="RegionOne" />
        </div>
      </div>
      <div className="grid gap-2">
        <Label htmlFor="cp-pass">Admin password</Label>
        <Input
          id="cp-pass"
          type="password"
          value={form.adminPassword}
          onChange={(e) => setForm({ ...form, adminPassword: e.target.value })}
          autoComplete="new-password"
        />
      </div>
      <div className="flex items-center justify-between rounded-lg border p-3">
        <div>
          <div className="text-sm font-medium">Shared provider</div>
          <div className="text-xs text-muted-foreground">One OpenStack tenant shared by all projects (no per-project tenant).</div>
        </div>
        <Switch checked={form.shared} onCheckedChange={(on) => setForm({ ...form, shared: on })} />
      </div>
    </div>
  )
}

export default function CloudProvidersPage() {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { data, isLoading, error } = useAdminList<CloudProvider>(LIST_PATH)
  const items = data?.data ?? []

  const [createOpen, setCreateOpen] = useState(false)
  const [kind, setKind] = useState<"openstack" | "ceph-s3">("openstack")
  const [form, setForm] = useState<FormState>(emptyForm)
  const [cephForm, setCephForm] = useState<CephFormState>(emptyCephForm)

  const create = useMutation({
    // POST /admin/service (externalServiceCreate). The operator finishes the Services/Features tabs on
    // the detail page (it has its own "Test connection") — so we go straight there on success.
    mutationFn: () =>
      apiFetch<CloudProvider>(LIST_PATH, {
        method: "POST",
        body: kind === "ceph-s3" ? cephFormToBody(cephForm) : formToBody(form),
      }),
    onSuccess: (created) => {
      toast.success("Cloud provider created")
      setCreateOpen(false)
      setForm(emptyForm)
      setCephForm(emptyCephForm)
      void qc.invalidateQueries({ queryKey: ["admin-list", LIST_PATH] })
      if (created?.id) navigate(`/system/cloud-providers/${created.id}`)
    },
    // Go's create runs a live Keystone auto-fill + provisioning; on this deployment it is a seam (501).
    // Surface the API message so the operator sees why it did not persist.
    onError: (e: Error) => toast.error(e.message),
  })

  const addBtn = (
    <Button size="sm" onClick={() => setCreateOpen(true)}>
      <Plus className="size-4" /> Add provider
    </Button>
  )

  return (
    <>
      <PageHeader
        title="Cloud providers"
        description="External services connected to the platform."
        actions={addBtn}
      />
      {isLoading ? (
        <Skeleton className="h-64" />
      ) : error ? (
        <div className="rounded-lg border bg-muted/40 p-4 text-sm text-muted-foreground">{(error as Error).message}</div>
      ) : items.length === 0 ? (
        <EmptyState
          icon={Cloud}
          title="No cloud providers"
          hint="Connect an OpenStack cloud or a Ceph S3 object store so projects can provision resources."
          action={addBtn}
        />
      ) : (
        <Card className="overflow-hidden py-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>ID</TableHead>
                <TableHead>Name</TableHead>
                <TableHead>Type</TableHead>
                <TableHead>Identity URL</TableHead>
                <TableHead>Regions</TableHead>
                <TableHead>Status</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((p) => (
                <TableRow
                  key={p.id}
                  className="cursor-pointer"
                  onClick={() => navigate(`/system/cloud-providers/${p.id}`)}
                >
                  <TableCell className="font-mono text-xs">{p.id}</TableCell>
                  <TableCell className="font-medium">{p.name ?? "—"}</TableCell>
                  <TableCell>{p.type ?? "—"}</TableCell>
                  <TableCell className="font-mono text-xs">
                    {p.config?.identityUrl ?? p.config?.s3Endpoint ?? "—"}
                  </TableCell>
                  <TableCell>
                    <div className="flex flex-wrap gap-1">
                      {(Object.keys(p.config?.regions ?? {}).length
                        ? Object.keys(p.config?.regions ?? {})
                        : p.config?.region
                          ? [p.config.region]
                          : []
                      ).map((r) => (
                        <Badge key={r} variant="outline">{r}</Badge>
                      ))}
                    </div>
                  </TableCell>
                  <TableCell>
                    <StatusBadge status={p.status} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Add cloud provider</DialogTitle>
            <DialogDescription>
              {kind === "ceph-s3"
                ? "Connect a Ceph RGW object store over its S3 and Admin Ops endpoints. No Keystone involved — projects get a dedicated RGW user."
                : 'Connect an OpenStack cloud with its Keystone admin credentials. You will enable per-region services and run "Test connection" on the provider page after it is created.'}
            </DialogDescription>
          </DialogHeader>
          <div className="grid grid-cols-2 gap-2">
            <Button variant={kind === "openstack" ? "default" : "outline"} onClick={() => setKind("openstack")}>
              OpenStack
            </Button>
            <Button variant={kind === "ceph-s3" ? "default" : "outline"} onClick={() => setKind("ceph-s3")}>
              Ceph S3
            </Button>
          </div>
          {kind === "ceph-s3" ? (
            <CephProviderForm form={cephForm} setForm={setCephForm} />
          ) : (
            <ProviderForm form={form} setForm={setForm} />
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>
              Cancel
            </Button>
            <Button
              onClick={() => create.mutate()}
              disabled={(kind === "ceph-s3" ? !cephFormValid(cephForm) : !formValid(form)) || create.isPending}
            >
              {create.isPending ? "Creating…" : "Create provider"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
