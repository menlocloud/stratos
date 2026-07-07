// Create-server wizard: Location → Availability zone → Image → Flavor → Network → Public IP → Access → Name.
// API contract verified against internal/cloud/providers/write.go (TypeServer branch):
// data reads name / imageId / flavorId / networkInterfaces:[{uuid}] / availabilityZoneName /
// keyName / securityGroupNames / assignFloatingIp / floatingNetworkId.
// (No user-data / boot-volume keys in the Go create — not offered.)
import { useState } from "react"
import { Link, useNavigate } from "react-router-dom"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"
import { Check, Server } from "lucide-react"
import { PageHeader } from "@/components/layout/PageHeader"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { StatusBadge } from "@/components/status-badge"
import { Switch } from "@/components/ui/switch"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { apiFetch, type CloudScope } from "@/lib/api"
import { useLocations, useProjectId, usePublicNetworks } from "@/lib/hooks"
import type { CloudResource, Location } from "@/lib/types"

type Az = { name?: string; available?: boolean }
type GlanceImage = Record<string, any>
type Flavor = {
  externalId?: string
  data?: { id?: string; name?: string; vcpus?: number; ram?: number; disk?: number }
}

const gb = (bytes?: number) => (bytes ? (bytes / 1073741824).toFixed(2) : "0.00")

// Collection-level cloud action (POST /project/{pid}/cloud/action → {result}).
function useBulkAction<T>(pid: string, scope: CloudScope | undefined, action: string) {
  return useQuery({
    queryKey: ["bulk-action", pid, action, scope?.serviceId, scope?.region],
    queryFn: () =>
      apiFetch<{ result?: T[] }>(`/project/${pid}/cloud/action`, {
        method: "POST",
        body: { action },
        cloud: scope,
      }),
    enabled: !!pid && !!scope,
    select: (d) => d?.result ?? [],
  })
}

export default function CreateServerPage() {
  const pid = useProjectId()
  const navigate = useNavigate()
  const qc = useQueryClient()

  const locations = useLocations(pid)
  const [locKey, setLocKey] = useState<string>()
  const locs = (locations.data ?? []).filter((l) => l.serviceId && l.region)
  const locOf = (l: Location) => `${l.serviceId}/${l.region}`
  const selectedLoc = locs.find((l) => locOf(l) === locKey) ?? locs[0]
  const scope: CloudScope | undefined =
    selectedLoc?.serviceId && selectedLoc?.region
      ? { serviceId: selectedLoc.serviceId, region: selectedLoc.region }
      : undefined

  const azs = useBulkAction<Az>(pid, scope, "LIST_AVAILABILITY_ZONES")
  const images = useBulkAction<GlanceImage>(pid, scope, "PUBLIC_IMAGES")
  const flavors = useBulkAction<Flavor>(pid, scope, "LIST_FLAVORS")
  const networks = useQuery({
    queryKey: ["cloud", pid, "NETWORK", scope?.serviceId, scope?.region],
    queryFn: () =>
      apiFetch<CloudResource[]>(`/project/${pid}/resource?type=NETWORK`, { method: "POST", cloud: scope }),
    enabled: !!pid && !!scope,
  })
  const keypairs = useQuery({
    queryKey: ["cloud", pid, "KEYPAIR", scope?.serviceId, scope?.region],
    queryFn: () =>
      apiFetch<CloudResource[]>(`/project/${pid}/resource?type=KEYPAIR`, { method: "POST", cloud: scope }),
    enabled: !!pid && !!scope,
  })
  const secgroups = useQuery({
    queryKey: ["cloud", pid, "SECURITY_GROUP", scope?.serviceId, scope?.region],
    queryFn: () =>
      apiFetch<CloudResource[]>(`/project/${pid}/resource?type=SECURITY_GROUP`, { method: "POST", cloud: scope }),
    enabled: !!pid && !!scope,
  })
  const pubNets = usePublicNetworks(pid, scope)

  const [azName, setAzName] = useState<string>()
  const [imageId, setImageId] = useState<string>()
  const [flavorId, setFlavorId] = useState<string>() // = flavor externalId
  const [netIds, setNetIds] = useState<string[]>([])
  const [assignFip, setAssignFip] = useState(true)
  const [fipNetId, setFipNetId] = useState<string>()
  const [keyName, setKeyName] = useState("") // "" = no key pair
  const [sgNames, setSgNames] = useState<string[]>([])
  const [name, setName] = useState("")

  const keypairName = (r: CloudResource) => (r.data?.keypair?.name as string) ?? r.name ?? ""
  const sgName = (r: CloudResource) => (r.data?.securityGroup?.name as string) ?? r.name ?? ""

  const az = azName ?? (azs.data?.[0]?.name as string | undefined)
  const fipNet = fipNetId ?? pubNets.data?.[0]?.id
  const wantFip = assignFip && !!pubNets.data?.length

  const create = useMutation({
    mutationFn: () =>
      apiFetch<CloudResource>(`/project/${pid}/cloud`, {
        method: "POST",
        cloud: scope,
        body: {
          type: "SERVER",
          data: {
            name: name.trim(),
            imageId,
            flavorId,
            ...(az ? { availabilityZoneName: az } : {}),
            networkInterfaces: netIds.map((uuid) => ({ uuid })),
            assignFloatingIp: wantFip,
            ...(wantFip && fipNet ? { floatingNetworkId: fipNet } : {}),
            ...(keyName ? { keyName } : {}),
            ...(sgNames.length ? { securityGroupNames: sgNames } : {}),
          },
        },
      }),
    onSuccess: () => {
      toast.success(`Server "${name.trim()}" is being created`)
      void qc.invalidateQueries({ queryKey: ["cloud", pid, "SERVER"] })
      navigate(`/p/${pid}/servers`)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const ready =
    !!scope && !!imageId && !!flavorId && netIds.length > 0 && !!name.trim() && (!wantFip || !!fipNet)

  if (locations.isLoading) {
    return (
      <>
        <PageHeader title="Create server" />
        <Skeleton className="h-72" />
      </>
    )
  }

  return (
    <>
      <PageHeader
        title="Create server"
        description="Pick a location, image, flavor and network, then name your server."
      />

      {locations.error ? (
        <p className="rounded-md bg-muted p-4 text-sm text-muted-foreground">
          {(locations.error as Error).message}
        </p>
      ) : !locs.length ? (
        <p className="rounded-md bg-muted p-4 text-sm text-muted-foreground">
          No locations are available in this project — attach a cloud service first.
        </p>
      ) : (
        <div className="grid gap-4">
          <Step n={1} title="Location">
            <div className="flex flex-wrap gap-2">
              {locs.map((l) => {
                const active = locOf(l) === (selectedLoc ? locOf(selectedLoc) : undefined)
                return (
                  <Button
                    key={locOf(l)}
                    type="button"
                    variant={active ? "default" : "outline"}
                    size="sm"
                    onClick={() => setLocKey(locOf(l))}
                  >
                    {active ? <Check className="size-4" /> : null}
                    {l.displayName || l.region} <span className="font-mono text-xs opacity-70">{l.region}</span>
                  </Button>
                )
              })}
            </div>
          </Step>

          <Step n={2} title="Availability zone">
            {azs.isLoading ? (
              <Skeleton className="h-9 w-48" />
            ) : !azs.data?.length ? (
              <p className="text-sm text-muted-foreground">No availability zones reported — the default zone will be used.</p>
            ) : (
              <div className="flex flex-wrap gap-2">
                {azs.data.map((z) => (
                  <Button
                    key={z.name}
                    type="button"
                    variant={az === z.name ? "default" : "outline"}
                    size="sm"
                    disabled={z.available === false}
                    onClick={() => setAzName(z.name)}
                  >
                    {az === z.name ? <Check className="size-4" /> : null}
                    {z.name}
                  </Button>
                ))}
              </div>
            )}
          </Step>

          <Step n={3} title="Image">
            {images.isLoading ? (
              <Skeleton className="h-40" />
            ) : !images.data?.length ? (
              <p className="text-sm text-muted-foreground">No public images available.</p>
            ) : (
              <div className="max-h-80 overflow-y-auto rounded-md border">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead className="w-8" />
                      <TableHead>Name</TableHead>
                      <TableHead>OS</TableHead>
                      <TableHead>Size</TableHead>
                      <TableHead>Status</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {images.data.map((im) => (
                      <TableRow
                        key={String(im.id)}
                        className="cursor-pointer"
                        data-state={imageId === im.id ? "selected" : undefined}
                        onClick={() => setImageId(String(im.id))}
                      >
                        <TableCell>{imageId === im.id ? <Check className="size-4 text-primary" /> : null}</TableCell>
                        <TableCell className="font-medium">{String(im.name ?? im.id)}</TableCell>
                        <TableCell className="text-sm text-muted-foreground">
                          {[im.os_distro, im.os_version].filter(Boolean).join(" ") || "—"}
                        </TableCell>
                        <TableCell className="text-sm text-muted-foreground">{gb(im.size as number)} GB</TableCell>
                        <TableCell>
                          <StatusBadge status={im.status as string} />
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            )}
          </Step>

          <Step n={4} title="Flavor">
            {flavors.isLoading ? (
              <Skeleton className="h-40" />
            ) : !flavors.data?.length ? (
              <p className="text-sm text-muted-foreground">No flavors available.</p>
            ) : (
              <div className="max-h-80 overflow-y-auto rounded-md border">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead className="w-8" />
                      <TableHead>Name</TableHead>
                      <TableHead>vCPUs</TableHead>
                      <TableHead>RAM</TableHead>
                      <TableHead>Disk</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {flavors.data.map((f) => (
                      <TableRow
                        key={f.externalId}
                        className="cursor-pointer"
                        data-state={flavorId === f.externalId ? "selected" : undefined}
                        onClick={() => setFlavorId(f.externalId)}
                      >
                        <TableCell>
                          {flavorId === f.externalId ? <Check className="size-4 text-primary" /> : null}
                        </TableCell>
                        <TableCell className="font-medium">{f.data?.name ?? f.externalId}</TableCell>
                        <TableCell>{f.data?.vcpus ?? "—"}</TableCell>
                        <TableCell>{f.data?.ram ? `${Math.round(f.data.ram / 1024)} GB` : "—"}</TableCell>
                        <TableCell>{f.data?.disk != null ? `${f.data.disk} GB` : "—"}</TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            )}
          </Step>

          <Step n={5} title="Network">
            {networks.isLoading ? (
              <Skeleton className="h-16" />
            ) : !networks.data?.length ? (
              <p className="text-sm text-muted-foreground">
                No networks in this project — create one under Networking first.
              </p>
            ) : (
              <div className="grid gap-2">
                {networks.data.map((n) => {
                  const ext = n.externalId ?? ""
                  const checked = netIds.includes(ext)
                  return (
                    <label key={n.id} className="flex cursor-pointer items-center gap-3 text-sm">
                      <Checkbox
                        checked={checked}
                        disabled={!ext}
                        onCheckedChange={(v) =>
                          setNetIds((ids) => (v === true ? [...ids, ext] : ids.filter((x) => x !== ext)))
                        }
                      />
                      <span className="font-medium">{(n.data?.network?.name as string) ?? n.name ?? n.id}</span>
                      <span className="font-mono text-xs text-muted-foreground">{ext}</span>
                    </label>
                  )
                })}
              </div>
            )}
          </Step>

          <Step n={6} title="Public IP">
            {pubNets.isLoading ? (
              <Skeleton className="h-9 w-48" />
            ) : (
              <div className="grid max-w-md gap-2">
                <div className="flex items-center gap-3">
                  <Switch
                    id="assign-fip"
                    checked={wantFip}
                    disabled={!pubNets.data?.length}
                    onCheckedChange={setAssignFip}
                  />
                  <Label htmlFor="assign-fip">Assign floating IP</Label>
                </div>
                {!pubNets.data?.length ? (
                  <p className="text-sm text-muted-foreground">
                    No public networks are enabled for this project.
                  </p>
                ) : wantFip ? (
                  <>
                    <Select value={fipNet} onValueChange={setFipNetId}>
                      <SelectTrigger className="w-full">
                        <SelectValue placeholder="Select a public network" />
                      </SelectTrigger>
                      <SelectContent>
                        {pubNets.data.map((n) => (
                          <SelectItem key={n.id} value={n.id}>
                            {n.name || n.id}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <p className="text-sm text-muted-foreground">
                      The floating IP is attached automatically shortly after the server becomes active.
                    </p>
                  </>
                ) : null}
              </div>
            )}
          </Step>

          <Step n={7} title="Access (optional)">
            <div className="grid gap-4">
              <div className="grid max-w-md gap-2">
                <Label>SSH key pair</Label>
                {keypairs.isLoading ? (
                  <Skeleton className="h-9" />
                ) : !keypairs.data?.length ? (
                  <p className="text-sm text-muted-foreground">
                    No key pairs in this project — create one under Compute → Key pairs first.
                  </p>
                ) : (
                  <Select
                    value={keyName || "__none__"}
                    onValueChange={(v) => setKeyName(v === "__none__" ? "" : v)}
                  >
                    <SelectTrigger className="w-full">
                      <SelectValue placeholder="No key pair" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="__none__">No key pair</SelectItem>
                      {keypairs.data
                        .filter((k) => !!keypairName(k))
                        .map((k) => (
                          <SelectItem key={k.id} value={keypairName(k)}>
                            {keypairName(k)}
                          </SelectItem>
                        ))}
                    </SelectContent>
                  </Select>
                )}
              </div>
              <div className="grid gap-2">
                <Label>Security groups</Label>
                {secgroups.isLoading ? (
                  <Skeleton className="h-9" />
                ) : !secgroups.data?.length ? (
                  <p className="text-sm text-muted-foreground">
                    No security groups in this project — the default group will apply.
                  </p>
                ) : (
                  <div className="grid gap-2">
                    {secgroups.data
                      .filter((sg) => !!sgName(sg))
                      .map((sg) => {
                        const n = sgName(sg)
                        const checked = sgNames.includes(n)
                        return (
                          <label key={sg.id} className="flex cursor-pointer items-center gap-3 text-sm">
                            <Checkbox
                              checked={checked}
                              onCheckedChange={(v) =>
                                setSgNames((ns) => (v === true ? [...ns, n] : ns.filter((x) => x !== n)))
                              }
                            />
                            <span className="font-medium">{n}</span>
                            <span className="text-xs text-muted-foreground">
                              {(sg.data?.securityGroup?.description as string) || ""}
                            </span>
                          </label>
                        )
                      })}
                  </div>
                )}
              </div>
            </div>
          </Step>

          <Step n={8} title="Name">
            <div className="grid max-w-md gap-2">
              <Label htmlFor="server-name">Server name</Label>
              <Input
                id="server-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="my-server"
              />
            </div>
          </Step>

          <div className="flex items-center gap-2">
            <Button onClick={() => create.mutate()} disabled={!ready || create.isPending}>
              <Server className="size-4" />
              {create.isPending ? "Creating…" : "Create server"}
            </Button>
            <Button variant="outline" asChild>
              <Link to={`/p/${pid}/servers`}>Cancel</Link>
            </Button>
          </div>
        </div>
      )}
    </>
  )
}

function Step({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <span className="flex size-6 items-center justify-center rounded-full bg-primary text-xs font-semibold text-primary-foreground">
            {n}
          </span>
          {title}
        </CardTitle>
      </CardHeader>
      <CardContent>{children}</CardContent>
    </Card>
  )
}
