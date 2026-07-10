import { useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"
import { Copy, Eye, EyeOff, KeyRound, MoreHorizontal, Plus, RefreshCw, RotateCw } from "lucide-react"
import { PageHeader } from "@/components/layout/PageHeader"
import { EmptyState } from "@/components/empty-state"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { ApiError, apiFetch } from "@/lib/api"
import { timeAgo } from "@/lib/format"
import { useProjectId } from "@/lib/hooks"
import { useS3Credentials, useS3Keys, type S3Key } from "@/lib/objectstore"

async function copy(label: string, value: string) {
  // navigator.clipboard.writeText rejects on a non-secure context, denied permission, or an old browser.
  // Callers use void copy(...), so an uncaught rejection would silently give the user no feedback.
  try {
    await navigator.clipboard.writeText(value)
    toast.success(`${label} copied`)
  } catch {
    toast.error(`Couldn't copy ${label} — select and copy it manually`)
  }
}

/** A secret is hidden until explicitly revealed — it stays out of screenshots and shoulder-surfing by default. */
function SecretField({ label, value }: { label: string; value: string }) {
  const [shown, setShown] = useState(false)
  return (
    <div className="grid gap-1.5">
      <Label className="text-xs text-muted-foreground">{label}</Label>
      <div className="flex items-center gap-2">
        <Input readOnly value={shown ? value : "•".repeat(Math.min(value.length, 40))} className="font-mono text-xs" />
        <Button variant="outline" size="sm" onClick={() => setShown((s) => !s)} aria-label={shown ? "Hide" : "Reveal"}>
          {shown ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
        </Button>
        <Button variant="outline" size="sm" onClick={() => void copy(label, value)} aria-label={`Copy ${label}`}>
          <Copy className="size-4" />
        </Button>
      </div>
    </div>
  )
}

function PlainField({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid gap-1.5">
      <Label className="text-xs text-muted-foreground">{label}</Label>
      <div className="flex items-center gap-2">
        <Input readOnly value={value} className="font-mono text-xs" />
        <Button variant="outline" size="sm" onClick={() => void copy(label, value)} aria-label={`Copy ${label}`}>
          <Copy className="size-4" />
        </Button>
      </div>
    </div>
  )
}

export default function S3KeysPage() {
  const pid = useProjectId()
  const qc = useQueryClient()
  const creds = useS3Credentials(pid)
  const keys = useS3Keys(pid)

  const [createOpen, setCreateOpen] = useState(false)
  const [name, setName] = useState("")
  const [rotateTarget, setRotateTarget] = useState<S3Key | "project" | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<S3Key | null>(null)

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ["s3-keys", pid] })
    void qc.invalidateQueries({ queryKey: ["s3-credentials", pid] })
  }

  const create = useMutation({
    mutationFn: () => apiFetch<S3Key>(`/project/${pid}/s3-keys`, { method: "POST", body: { name: name.trim() } }),
    onSuccess: () => {
      toast.success("Access key created")
      setCreateOpen(false)
      setName("")
      invalidate()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const rotate = useMutation({
    mutationFn: (t: S3Key | "project") =>
      apiFetch<{ warning?: string }>(
        t === "project" ? `/project/${pid}/s3-credentials/rotate` : `/project/${pid}/s3-keys/${t.id}/rotate`,
        { method: "POST" },
      ),
    // The backend sets `warning` when the NEW key works but the old one could not be retired — in that case
    // the previous key may still be live, so don't claim it stopped working.
    onSuccess: (res) => {
      if (res?.warning) {
        toast.warning("New key issued, but the previous key could not be retired — revoke it manually.")
      } else {
        toast.success("Key rotated — the previous key no longer works")
      }
      setRotateTarget(null)
      invalidate()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const del = useMutation({
    mutationFn: (k: S3Key) => apiFetch(`/project/${pid}/s3-keys/${k.id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Access key deleted")
      setDeleteTarget(null)
      invalidate()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  // A 400 means the project has no ceph-s3 service — a normal state, show the empty page. Any OTHER error
  // (403 no permission, 5xx, network) is a real failure and must surface, not masquerade as "not available".
  if (creds.isError) {
    const status = creds.error instanceof ApiError ? creds.error.status : 0
    return (
      <>
        <PageHeader title="S3 access keys" description="Use these credentials with the AWS CLI or any S3 client." />
        {status === 400 ? (
          <EmptyState
            icon={KeyRound}
            title="S3 access keys are not available"
            hint="This project has no S3 (Ceph) object storage service. Swift buckets are managed through Stratos only."
          />
        ) : (
          <p className="rounded-md bg-muted p-4 text-sm text-muted-foreground">{(creds.error as Error).message}</p>
        )}
      </>
    )
  }

  return (
    <>
      <PageHeader
        title="S3 access keys"
        description="Use these credentials with the AWS CLI or any S3-compatible client."
        actions={
          <Button size="sm" onClick={() => setCreateOpen(true)}>
            <Plus className="size-4" /> Create access key
          </Button>
        }
      />

      {creds.isLoading ? (
        <Skeleton className="h-56" />
      ) : creds.data ? (
        <Card className="mb-6 p-4">
          <div className="mb-3 flex items-center justify-between">
            <div>
              <h2 className="text-sm font-medium">Project credentials</h2>
              <p className="text-xs text-muted-foreground">
                Full access to every bucket in this project. Treat the secret key like a password.
              </p>
            </div>
            <Button variant="outline" size="sm" onClick={() => setRotateTarget("project")}>
              <RotateCw className="size-4" /> Rotate
            </Button>
          </div>
          <div className="grid gap-3 md:grid-cols-2">
            <PlainField label="Endpoint" value={creds.data.s3Endpoint} />
            <PlainField label="Region" value={creds.data.region} />
            <PlainField label="Access key" value={creds.data.accessKey} />
            <SecretField label="Secret key" value={creds.data.secretKey} />
          </div>
          <pre className="mt-4 overflow-x-auto rounded-md bg-muted p-3 text-xs">
{`aws --endpoint-url ${creds.data.s3Endpoint} s3 ls`}
          </pre>
        </Card>
      ) : null}

      <div className="mb-2 flex items-center justify-between">
        <h2 className="text-sm font-medium text-muted-foreground">Additional keys</h2>
        <Button variant="outline" size="sm" onClick={() => void keys.refetch()} disabled={keys.isFetching}>
          <RefreshCw className={keys.isFetching ? "size-4 animate-spin" : "size-4"} />
        </Button>
      </div>

      {keys.isLoading ? (
        <Skeleton className="h-40" />
      ) : !keys.data?.length ? (
        <EmptyState
          icon={KeyRound}
          title="No additional keys"
          hint="Create a key to give an app or a teammate access to specific buckets, without sharing the project credentials."
          action={
            <Button onClick={() => setCreateOpen(true)}>
              <Plus className="size-4" /> Create access key
            </Button>
          }
        />
      ) : (
        <Card className="overflow-hidden py-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Access key</TableHead>
                <TableHead>Created</TableHead>
                <TableHead className="w-10" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {keys.data.map((k) => (
                <TableRow key={k.id}>
                  <TableCell className="font-medium">{k.name}</TableCell>
                  <TableCell className="font-mono text-xs">{k.accessKey}</TableCell>
                  <TableCell className="text-sm text-muted-foreground">{timeAgo(k.createdAt)}</TableCell>
                  <TableCell>
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <Button variant="ghost" size="sm">
                          <MoreHorizontal className="size-4" />
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem onClick={() => void copy("Access key", k.accessKey)}>
                          <Copy className="size-4" /> Copy access key
                        </DropdownMenuItem>
                        {k.secretKey ? (
                          <DropdownMenuItem onClick={() => void copy("Secret key", k.secretKey as string)}>
                            <Copy className="size-4" /> Copy secret key
                          </DropdownMenuItem>
                        ) : null}
                        <DropdownMenuItem onClick={() => setRotateTarget(k)}>
                          <RotateCw className="size-4" /> Rotate
                        </DropdownMenuItem>
                        <DropdownMenuItem className="text-destructive" onClick={() => setDeleteTarget(k)}>
                          Delete
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}

      <p className="mt-4 text-xs text-muted-foreground">
        An additional key can read or write only the buckets you grant it. Open a bucket’s settings to manage access.
      </p>

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Create access key</DialogTitle>
            <DialogDescription>
              The new key has no access until you grant it a bucket. Lowercase letters, digits and hyphens.
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-2">
            <Label htmlFor="key-name">Name</Label>
            <Input id="key-name" placeholder="backup-app" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>Cancel</Button>
            <Button onClick={() => create.mutate()} disabled={!name.trim() || create.isPending}>
              {create.isPending ? "Creating…" : "Create key"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={!!rotateTarget} onOpenChange={(o) => !o && setRotateTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Rotate access key</DialogTitle>
            <DialogDescription>
              A new access key and secret are issued and the current one is retired — normally it stops working{" "}
              <strong>immediately</strong>, and you will be warned if it could not be retired and must be revoked
              manually. Anything still using the old key will start failing. Bucket access is unaffected.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRotateTarget(null)}>Cancel</Button>
            <Button
              variant="destructive"
              onClick={() => rotateTarget && rotate.mutate(rotateTarget)}
              disabled={rotate.isPending}
            >
              {rotate.isPending ? "Rotating…" : "Rotate key"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={!!deleteTarget} onOpenChange={(o) => !o && setDeleteTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete access key</DialogTitle>
            <DialogDescription>
              This deletes {deleteTarget?.name} and revokes its access to every bucket in this project. Objects are
              not deleted.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteTarget(null)}>Cancel</Button>
            <Button variant="destructive" onClick={() => deleteTarget && del.mutate(deleteTarget)} disabled={del.isPending}>
              {del.isPending ? "Deleting…" : "Delete key"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
