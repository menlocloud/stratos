import { useMemo, useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import type { ColumnDef } from "@tanstack/react-table"
import { Cloud, Plus } from "lucide-react"
import { useNavigate } from "react-router-dom"
import { toast } from "sonner"
import { PageHeader } from "@/components/layout/PageHeader"
import { DataTable, sortableHeader } from "@/components/data-table"
import { EmptyState } from "@/components/empty-state"
import { StatusBadge } from "@/components/status-badge"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
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
import { Textarea } from "@/components/ui/textarea"
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

// Region chips: OpenStack providers key config.regions; ceph-s3 stores a single config.region.
function providerRegions(p: CloudProvider): string[] {
  const keyed = Object.keys(p.config?.regions ?? {})
  if (keyed.length) return keyed
  return p.config?.region ? [p.config.region] : []
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

// ── kamaji (managed kubernetes) create form ──────────────────────────────────
// config.provider === "kamaji": clusters are ArgoCD Applications of the pinned
// `openstack-kamaji-cluster` chart on the provider's Kamaji MANAGEMENT cluster; the secret is a
// kubeconfig for a stratos-scoped service account there (tasks/managed-k8s-plan.md D3/D7).
type KamajiFormState = {
  name: string
  region: string
  kubeconfig: string
  chartRepo: string
  chartVersion: string
  argoNamespace: string
  argoProject: string
  dataStoreName: string
  floatingNetworkId: string
  externalNetworkId: string
  dnsZone: string
  versions: string // one "1.35.4=<glance-image-id>" per line
  flavors: string // optional allowlist, one Nova flavor id per line (empty = all tenant flavors)
}

const emptyKamajiForm: KamajiFormState = {
  name: "",
  region: "az1",
  kubeconfig: "",
  chartRepo: "ghcr.io/menlocloud/charts",
  chartVersion: "",
  argoNamespace: "argocd",
  argoProject: "stratos-k8s",
  dataStoreName: "default",
  floatingNetworkId: "",
  externalNetworkId: "",
  dnsZone: "",
  versions: "",
  flavors: "",
}

// parseVersions turns the "version=imageId" lines into the config.cluster.versions map;
// null = at least one non-empty line is malformed.
function parseVersions(raw: string): Record<string, string> | null {
  const out: Record<string, string> = {}
  for (const line of raw.split("\n")) {
    const t = line.trim()
    if (!t) continue
    const eq = t.indexOf("=")
    if (eq <= 0 || eq === t.length - 1) return null
    out[t.slice(0, eq).trim()] = t.slice(eq + 1).trim()
  }
  return out
}

const kamajiFormValid = (f: KamajiFormState) => {
  const versions = parseVersions(f.versions)
  return (
    [f.name, f.region, f.kubeconfig, f.chartRepo, f.chartVersion].every((v) => v.trim() !== "") &&
    versions !== null &&
    Object.keys(versions).length > 0
  )
}

function kamajiFormToBody(f: KamajiFormState) {
  const region = f.region.trim()
  // Optional Nova-flavor allowlist → config.cluster.flavors: string[] (externalservice KamajiConfig).
  // Omitted entirely when empty — an absent key means "offer all tenant flavors".
  const flavors = f.flavors
    .split("\n")
    .map((s) => s.trim())
    .filter(Boolean)
  return {
    name: f.name.trim(),
    type: "CLOUD",
    status: "PUBLIC",
    config: {
      provider: "kamaji",
      regions: { [region]: { name: region, country: "", displayName: region } },
      // kubernetes is the ONLY service a kamaji provider serves — enabled directly, no discovery.
      services: { kubernetes: { [region]: true } },
      argocd: {
        namespace: f.argoNamespace.trim() || "argocd",
        // Keep in sync with the backend (externalservice KamajiConfig), which also defaults the
        // AppProject guardrail to "stratos-k8s" when unset.
        project: f.argoProject.trim() || "stratos-k8s",
        chartRepo: f.chartRepo.trim(),
        chartName: "openstack-kamaji-cluster",
        chartVersion: f.chartVersion.trim(),
      },
      cluster: {
        ...(f.dataStoreName.trim() ? { dataStoreName: f.dataStoreName.trim() } : {}),
        ...(f.floatingNetworkId.trim() ? { floatingNetworkId: f.floatingNetworkId.trim() } : {}),
        ...(f.externalNetworkId.trim() ? { externalNetworkId: f.externalNetworkId.trim() } : {}),
        ...(f.dnsZone.trim() ? { dnsZone: f.dnsZone.trim() } : {}),
        versions: parseVersions(f.versions) ?? {},
        ...(flavors.length ? { flavors } : {}),
      },
    },
    secret: { kubeconfig: f.kubeconfig.trim() },
  }
}

function KamajiProviderForm({ form, setForm }: { form: KamajiFormState; setForm: (f: KamajiFormState) => void }) {
  return (
    <div className="grid gap-4">
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="km-name">Display name</Label>
          <Input id="km-name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Managed Kubernetes AZ1" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="km-region">Region</Label>
          <Input id="km-region" value={form.region} onChange={(e) => setForm({ ...form, region: e.target.value })} placeholder="az1" />
        </div>
      </div>
      <div className="grid gap-2">
        <Label htmlFor="km-kubeconfig">Management-cluster kubeconfig</Label>
        <Textarea
          id="km-kubeconfig"
          className="min-h-28 font-mono text-xs"
          value={form.kubeconfig}
          onChange={(e) => setForm({ ...form, kubeconfig: e.target.value })}
          placeholder="apiVersion: v1&#10;kind: Config&#10;…"
          autoComplete="off"
        />
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="km-repo">Chart OCI repo</Label>
          <Input id="km-repo" className="font-mono" value={form.chartRepo} onChange={(e) => setForm({ ...form, chartRepo: e.target.value })} placeholder="ghcr.io/menlocloud/charts" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="km-chartver">Chart version (pinned)</Label>
          <Input id="km-chartver" className="font-mono" value={form.chartVersion} onChange={(e) => setForm({ ...form, chartVersion: e.target.value })} placeholder="0.2.3" />
        </div>
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="km-argons">ArgoCD namespace</Label>
          <Input id="km-argons" value={form.argoNamespace} onChange={(e) => setForm({ ...form, argoNamespace: e.target.value })} placeholder="argocd" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="km-argoproj">ArgoCD AppProject</Label>
          <Input id="km-argoproj" value={form.argoProject} onChange={(e) => setForm({ ...form, argoProject: e.target.value })} placeholder="stratos-k8s" />
          <p className="text-xs text-muted-foreground">
            The ArgoCD guardrail — cluster Applications are confined to this AppProject (defaults to stratos-k8s).
          </p>
        </div>
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="km-fnet">Floating network ID (API LB)</Label>
          <Input id="km-fnet" className="font-mono" value={form.floatingNetworkId} onChange={(e) => setForm({ ...form, floatingNetworkId: e.target.value })} autoComplete="off" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="km-extnet">External network ID (workers)</Label>
          <Input id="km-extnet" className="font-mono" value={form.externalNetworkId} onChange={(e) => setForm({ ...form, externalNetworkId: e.target.value })} autoComplete="off" />
        </div>
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="km-datastore">Kamaji DataStore</Label>
          <Input id="km-datastore" value={form.dataStoreName} onChange={(e) => setForm({ ...form, dataStoreName: e.target.value })} placeholder="default" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="km-dns">DNS zone (optional)</Label>
          <Input id="km-dns" value={form.dnsZone} onChange={(e) => setForm({ ...form, dnsZone: e.target.value })} placeholder="k8s.example.com" />
        </div>
      </div>
      <div className="grid gap-2">
        <Label htmlFor="km-versions">Kubernetes versions (one “version=image-id” per line)</Label>
        <Textarea
          id="km-versions"
          className="min-h-20 font-mono text-xs"
          value={form.versions}
          onChange={(e) => setForm({ ...form, versions: e.target.value })}
          placeholder="1.35.4=db37655f-…"
        />
      </div>
      <div className="grid gap-2">
        <Label htmlFor="km-flavors">Flavor allowlist (optional, one Nova flavor id per line)</Label>
        <Textarea
          id="km-flavors"
          className="min-h-20 font-mono text-xs"
          value={form.flavors}
          onChange={(e) => setForm({ ...form, flavors: e.target.value })}
          placeholder="c1a4r8&#10;g1a8r16"
        />
        <p className="text-xs text-muted-foreground">
          Only these Nova flavors are offered for worker pools; empty = all tenant flavors offered.
        </p>
      </div>
      <p className="text-xs text-muted-foreground">
        The kubeconfig belongs to a stratos service account on the Kamaji management cluster (ArgoCD +
        AppProject installed there). Only versions listed here are offered to customers.
      </p>
    </div>
  )
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
        <Input id="cs-s3" className="font-mono" value={form.s3Endpoint} onChange={(e) => setForm({ ...form, s3Endpoint: e.target.value })} placeholder="https://s3.example.com" />
      </div>
      <div className="grid gap-2">
        <Label htmlFor="cs-admin">Admin Ops URL</Label>
        <Input id="cs-admin" className="font-mono" value={form.adminApiUrl} onChange={(e) => setForm({ ...form, adminApiUrl: e.target.value })} placeholder="https://s3.example.com/admin" />
      </div>
      <div className="grid gap-2">
        <Label htmlFor="cs-web">Website endpoint (optional)</Label>
        <Input id="cs-web" className="font-mono" value={form.s3WebsiteEndpoint} onChange={(e) => setForm({ ...form, s3WebsiteEndpoint: e.target.value })} placeholder="https://s3-website.example.com" />
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="cs-region">Region (RGW zonegroup)</Label>
          <Input id="cs-region" value={form.region} onChange={(e) => setForm({ ...form, region: e.target.value })} placeholder="us-east-1" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="cs-prefix">RGW uid prefix (optional)</Label>
          <Input id="cs-prefix" value={form.uidPrefix} onChange={(e) => setForm({ ...form, uidPrefix: e.target.value })} placeholder="prod_" />
        </div>
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="cs-key">Admin access key</Label>
          <Input id="cs-key" className="font-mono" value={form.adminAccessKey} onChange={(e) => setForm({ ...form, adminAccessKey: e.target.value })} autoComplete="off" />
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
          className="font-mono"
          value={form.identityUrl}
          onChange={(e) => setForm({ ...form, identityUrl: e.target.value })}
          placeholder="https://keystone.example.com:5000/v3"
        />
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="cp-user">Admin username</Label>
          <Input id="cp-user" value={form.adminUsername} onChange={(e) => setForm({ ...form, adminUsername: e.target.value })} autoComplete="off" />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="cp-project">Admin project</Label>
          <Input id="cp-project" value={form.adminProjectName} onChange={(e) => setForm({ ...form, adminProjectName: e.target.value })} placeholder="admin" />
        </div>
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
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
          <Label htmlFor="cp-shared" className="text-sm font-medium">Shared provider</Label>
          <div className="text-xs text-muted-foreground">One OpenStack tenant shared by all projects (no per-project tenant).</div>
        </div>
        <Switch id="cp-shared" checked={form.shared} onCheckedChange={(on) => setForm({ ...form, shared: on })} />
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
  const [kind, setKind] = useState<"openstack" | "ceph-s3" | "kamaji">("openstack")
  const [form, setForm] = useState<FormState>(emptyForm)
  const [cephForm, setCephForm] = useState<CephFormState>(emptyCephForm)
  const [kamajiForm, setKamajiForm] = useState<KamajiFormState>(emptyKamajiForm)

  const create = useMutation({
    // POST /admin/service (externalServiceCreate). The operator finishes the Services/Features tabs on
    // the detail page (it has its own "Test connection") — so we go straight there on success.
    mutationFn: () =>
      apiFetch<CloudProvider>(LIST_PATH, {
        method: "POST",
        body: kind === "ceph-s3" ? cephFormToBody(cephForm) : kind === "kamaji" ? kamajiFormToBody(kamajiForm) : formToBody(form),
      }),
    onSuccess: (created) => {
      toast.success("Cloud provider created")
      setCreateOpen(false)
      setForm(emptyForm)
      setCephForm(emptyCephForm)
      setKamajiForm(emptyKamajiForm)
      void qc.invalidateQueries({ queryKey: ["admin-list", LIST_PATH] })
      if (created?.id) navigate(`/system/cloud-providers/${created.id}`)
    },
    // Go's create runs a live Keystone auto-fill + provisioning; on this deployment it is a seam (501).
    // Surface the API message so the operator sees why it did not persist.
    onError: (e: Error) => toast.error(e.message),
  })

  const columns = useMemo<ColumnDef<CloudProvider, any>[]>(
    () => [
      {
        id: "id",
        accessorFn: (p) => p.id,
        header: sortableHeader("ID"),
        cell: ({ getValue }) => <span className="font-mono text-xs">{getValue()}</span>,
      },
      {
        id: "name",
        accessorFn: (p) => p.name ?? "",
        header: sortableHeader("Name"),
        cell: ({ getValue }) => <span className="font-medium">{getValue() || "—"}</span>,
      },
      {
        id: "type",
        accessorFn: (p) => p.type ?? "",
        header: sortableHeader("Type"),
        cell: ({ getValue }) => <span className="text-sm text-muted-foreground">{getValue() || "—"}</span>,
      },
      {
        id: "endpoint",
        accessorFn: (p) => p.config?.identityUrl ?? p.config?.s3Endpoint ?? "",
        header: "Identity URL",
        cell: ({ getValue }) => <span className="font-mono text-xs">{getValue() || "—"}</span>,
      },
      {
        id: "regions",
        accessorFn: (p) => providerRegions(p).join(", "),
        header: "Regions",
        enableSorting: false,
        cell: ({ row }) => (
          <div className="flex flex-wrap gap-1">
            {providerRegions(row.original).map((r) => (
              <Badge key={r} variant="outline">{r}</Badge>
            ))}
          </div>
        ),
      },
      {
        id: "status",
        accessorFn: (p) => p.status ?? "",
        header: sortableHeader("Status"),
        cell: ({ row }) => <StatusBadge status={row.original.status} />,
      },
    ],
    // useState setters are stable; helpers are module-scope.
    [],
  )

  const addBtn = (
    <Button size="sm" onClick={() => setCreateOpen(true)}>
      <Plus className="size-4" /> Add provider
    </Button>
  )

  return (
    <>
      <PageHeader
        title="Cloud providers"
        eyebrow="System"
        description="External services connected to the platform."
        actions={addBtn}
      />
      {!isLoading && !error && items.length === 0 ? (
        <EmptyState
          icon={Cloud}
          title="No cloud providers"
          hint="Connect an OpenStack cloud or a Ceph S3 object store so projects can provision resources."
          action={addBtn}
        />
      ) : (
        <DataTable
          columns={columns}
          data={items}
          isLoading={isLoading}
          error={error as Error | null}
          searchPlaceholder="Search providers…"
          getRowId={(p) => p.id}
          onRowClick={(p) => navigate(`/system/cloud-providers/${p.id}`)}
        />
      )}

      <Dialog
        open={createOpen}
        onOpenChange={(o) => {
          setCreateOpen(o)
          // Clear ALL forms on every close (Cancel, Esc, overlay) — the ceph form holds admin keys
          // and the kamaji form a kubeconfig; neither must sit in state and re-appear on next open.
          if (!o) {
            setForm(emptyForm)
            setCephForm(emptyCephForm)
            setKamajiForm(emptyKamajiForm)
            setKind("openstack")
          }
        }}
      >
        <DialogContent className="max-h-[85vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>Add cloud provider</DialogTitle>
            <DialogDescription>
              {kind === "ceph-s3"
                ? "Connect a Ceph RGW object store over its S3 and Admin Ops endpoints. No Keystone involved — projects get a dedicated RGW user."
                : kind === "kamaji"
                  ? "Connect a Kamaji management cluster for Managed Kubernetes. Clusters are delivered as ArgoCD Applications of the pinned chart; worker nodes run in each customer's OpenStack tenant."
                  : 'Connect an OpenStack cloud with its Keystone admin credentials. You will enable per-region services and run "Test connection" on the provider page after it is created.'}
            </DialogDescription>
          </DialogHeader>
          <div className="grid grid-cols-3 gap-2">
            <Button variant={kind === "openstack" ? "default" : "outline"} onClick={() => setKind("openstack")}>
              OpenStack
            </Button>
            <Button variant={kind === "ceph-s3" ? "default" : "outline"} onClick={() => setKind("ceph-s3")}>
              Ceph S3
            </Button>
            <Button variant={kind === "kamaji" ? "default" : "outline"} onClick={() => setKind("kamaji")}>
              Kubernetes
            </Button>
          </div>
          {kind === "ceph-s3" ? (
            <CephProviderForm form={cephForm} setForm={setCephForm} />
          ) : kind === "kamaji" ? (
            <KamajiProviderForm form={kamajiForm} setForm={setKamajiForm} />
          ) : (
            <ProviderForm form={form} setForm={setForm} />
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>
              Cancel
            </Button>
            <Button
              onClick={() => create.mutate()}
              disabled={
                (kind === "ceph-s3" ? !cephFormValid(cephForm) : kind === "kamaji" ? !kamajiFormValid(kamajiForm) : !formValid(form)) ||
                create.isPending
              }
            >
              {create.isPending ? "Creating…" : "Create provider"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
