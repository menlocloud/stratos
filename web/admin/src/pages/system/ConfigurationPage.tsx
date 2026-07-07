import { useEffect, useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"
import { PageHeader } from "@/components/layout/PageHeader"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { apiFetch } from "@/lib/api"
import { useAdminGet } from "@/lib/hooks"

type PlatformConfig = {
  id: string | Record<string, unknown>
  name?: string
  language?: string
  branding?: { name?: string; color?: string; logo?: string; faviconUrl?: string } & Record<string, unknown>
  dateConfiguration?: { dateFormat?: string }
  projectProvisioningQuota?: { enabled: boolean; limit: number }
  organizationProvisioningQuota?: { enabled: boolean; limit: number }
  regions?: Array<{ serviceId: string; region: string; order: number }>
  [k: string]: unknown
}

type BillingConfig = {
  id?: string
  baseCurrency?: string
  promotionCodesEnabled?: boolean
  defaultConfiguration?: boolean
}

const PLATFORM_PATH = "/admin/platform-configuration/current"
const BILLING_PATH = "/admin/billing/configuration/current"

type PlatformForm = {
  name: string
  brandingName: string
  brandingColor: string
  dateFormat: string
  quotaEnabled: boolean
  quotaLimit: string
  orgQuotaEnabled: boolean
  orgQuotaLimit: string
}

export default function ConfigurationPage() {
  const qc = useQueryClient()
  const platformQ = useAdminGet<PlatformConfig>(PLATFORM_PATH)
  const billingQ = useAdminGet<BillingConfig>(BILLING_PATH)
  const cfg = platformQ.data

  const [form, setForm] = useState<PlatformForm | null>(null)
  useEffect(() => {
    if (cfg && !form) {
      setForm({
        name: cfg.name ?? "",
        brandingName: cfg.branding?.name ?? "",
        brandingColor: cfg.branding?.color ?? "",
        dateFormat: cfg.dateConfiguration?.dateFormat ?? "",
        quotaEnabled: cfg.projectProvisioningQuota?.enabled === true,
        quotaLimit: String(cfg.projectProvisioningQuota?.limit ?? 0),
        orgQuotaEnabled: cfg.organizationProvisioningQuota?.enabled === true,
        orgQuotaLimit: String(cfg.organizationProvisioningQuota?.limit ?? 0),
      })
    }
  }, [cfg, form])

  const save = useMutation({
    mutationFn: () => {
      if (!cfg || !form) throw new Error("Configuration not loaded")
      // PUT /{id} REPLACES the stored document — send back every field the read returned, merged
      // with the edits, or unedited fields would be lost.
      const id = typeof cfg.id === "string" ? cfg.id : String(cfg.id)
      const body: PlatformConfig = {
        ...cfg,
        name: form.name,
        branding: { ...(cfg.branding ?? {}), name: form.brandingName, color: form.brandingColor },
        dateConfiguration: { ...(cfg.dateConfiguration ?? {}), dateFormat: form.dateFormat },
        projectProvisioningQuota: { enabled: form.quotaEnabled, limit: Number(form.quotaLimit) || 0 },
        organizationProvisioningQuota: { enabled: form.orgQuotaEnabled, limit: Number(form.orgQuotaLimit) || 0 },
      }
      return apiFetch(`/admin/platform-configuration/${id}`, { method: "PUT", body })
    },
    onSuccess: () => {
      setForm(null) // re-derive from the fresh read
      qc.invalidateQueries({ queryKey: ["admin-get", PLATFORM_PATH] })
      toast.success("Platform configuration saved")
    },
    onError: (e) => toast.error((e as Error).message),
  })

  return (
    <>
      <PageHeader title="Configuration" description="Platform branding, formats, quota and billing basics." />

      <div className="space-y-6">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Platform</CardTitle>
          </CardHeader>
          <CardContent>
            {platformQ.isLoading ? (
              <Skeleton className="h-48" />
            ) : platformQ.error ? (
              <div className="rounded-lg border bg-muted/40 p-4 text-sm text-muted-foreground">
                {(platformQ.error as Error).message}
              </div>
            ) : !cfg || !form ? (
              <p className="text-sm text-muted-foreground">No platform configuration found.</p>
            ) : (
              <div className="space-y-4">
                <div className="grid gap-4 md:grid-cols-2">
                  <div className="space-y-1.5">
                    <Label htmlFor="pc-name">Configuration name</Label>
                    <Input id="pc-name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="pc-brand">Brand name</Label>
                    <Input id="pc-brand" value={form.brandingName} onChange={(e) => setForm({ ...form, brandingName: e.target.value })} />
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="pc-color">Brand color</Label>
                    <Input id="pc-color" placeholder="#4f46e5" value={form.brandingColor} onChange={(e) => setForm({ ...form, brandingColor: e.target.value })} />
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="pc-date">Date format</Label>
                    <Input id="pc-date" placeholder="DD/MM/YYYY" value={form.dateFormat} onChange={(e) => setForm({ ...form, dateFormat: e.target.value })} />
                  </div>
                </div>

                <div className="grid gap-4 md:grid-cols-2">
                  <div className="space-y-1.5">
                    <Label>Logo</Label>
                    <p className="truncate font-mono text-xs text-muted-foreground">{cfg.branding?.logo || "—"}</p>
                  </div>
                  <div className="space-y-1.5">
                    <Label>Favicon</Label>
                    <p className="truncate font-mono text-xs text-muted-foreground">{cfg.branding?.faviconUrl || "—"}</p>
                  </div>
                </div>

                <div className="space-y-3">
                  <div className="flex items-center gap-6 rounded-lg border p-4">
                    <div className="flex items-center gap-2">
                      <Switch checked={form.quotaEnabled} onCheckedChange={(v) => setForm({ ...form, quotaEnabled: v })} />
                      <span className="text-sm">Project provisioning quota</span>
                    </div>
                    <div className="flex items-center gap-2">
                      <Label htmlFor="pc-limit" className="text-sm text-muted-foreground">Limit</Label>
                      <Input
                        id="pc-limit"
                        type="number"
                        min={0}
                        className="w-24"
                        value={form.quotaLimit}
                        disabled={!form.quotaEnabled}
                        onChange={(e) => setForm({ ...form, quotaLimit: e.target.value })}
                      />
                    </div>
                    <p className="text-xs text-muted-foreground">Max projects per organization. 0 = only operators create projects.</p>
                  </div>
                  <div className="flex items-center gap-6 rounded-lg border p-4">
                    <div className="flex items-center gap-2">
                      <Switch checked={form.orgQuotaEnabled} onCheckedChange={(v) => setForm({ ...form, orgQuotaEnabled: v })} />
                      <span className="text-sm">Organization provisioning quota</span>
                    </div>
                    <div className="flex items-center gap-2">
                      <Label htmlFor="pc-org-limit" className="text-sm text-muted-foreground">Limit</Label>
                      <Input
                        id="pc-org-limit"
                        type="number"
                        min={0}
                        className="w-24"
                        value={form.orgQuotaLimit}
                        disabled={!form.orgQuotaEnabled}
                        onChange={(e) => setForm({ ...form, orgQuotaLimit: e.target.value })}
                      />
                    </div>
                    <p className="text-xs text-muted-foreground">Max organizations a user can own. 0 = only operators create organizations and assign members.</p>
                  </div>
                </div>

                <div>
                  <Label className="mb-2 block">Regions</Label>
                  {(cfg.regions ?? []).length === 0 ? (
                    <p className="text-sm text-muted-foreground">No regions configured.</p>
                  ) : (
                    <Card className="overflow-hidden py-0">
                      <Table>
                        <TableHeader>
                          <TableRow>
                            <TableHead>Service</TableHead>
                            <TableHead>Region</TableHead>
                            <TableHead>Order</TableHead>
                          </TableRow>
                        </TableHeader>
                        <TableBody>
                          {(cfg.regions ?? []).map((r, i) => (
                            <TableRow key={i}>
                              <TableCell className="font-mono text-xs">{r.serviceId}</TableCell>
                              <TableCell>{r.region}</TableCell>
                              <TableCell className="tabular-nums">{r.order}</TableCell>
                            </TableRow>
                          ))}
                        </TableBody>
                      </Table>
                    </Card>
                  )}
                </div>

                <Button onClick={() => save.mutate()} disabled={save.isPending}>
                  {save.isPending ? "Saving…" : "Save platform configuration"}
                </Button>
              </div>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-base">Billing</CardTitle>
          </CardHeader>
          <CardContent>
            {billingQ.isLoading ? (
              <Skeleton className="h-24" />
            ) : billingQ.error ? (
              <div className="rounded-lg border bg-muted/40 p-4 text-sm text-muted-foreground">
                {(billingQ.error as Error).message}
              </div>
            ) : !billingQ.data ? (
              <p className="text-sm text-muted-foreground">No billing configuration found.</p>
            ) : (
              <div className="space-y-3">
                <div className="flex items-center justify-between border-b py-2 text-sm">
                  <span className="text-muted-foreground">Base currency</span>
                  <span className="font-mono">{billingQ.data.baseCurrency ?? "—"}</span>
                </div>
                <div className="flex items-center justify-between py-2 text-sm">
                  <span className="text-muted-foreground">Promotion codes</span>
                  <Badge variant={billingQ.data.promotionCodesEnabled ? "default" : "outline"}>
                    {billingQ.data.promotionCodesEnabled ? "Enabled" : "Disabled"}
                  </Badge>
                </div>
                {/* ponytail: read-only by design — the read DTO only exposes 4 fields while
                    PUT /admin/billing/configuration/{id} overwrites ALL 13 mutable fields (an
                    omitted field is nulled), so saving from this thin read would wipe stored
                    settings/suspension config. Enable editing once a full-shape read exists. */}
                <p className="text-xs text-muted-foreground">
                  Editing is disabled: the API's billing-configuration read is partial and its update replaces
                  unspecified fields.
                </p>
              </div>
            )}
          </CardContent>
        </Card>
      </div>
    </>
  )
}
