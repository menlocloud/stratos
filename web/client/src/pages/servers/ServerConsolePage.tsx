// VNC console. The browser NEVER talks to the OpenStack novncproxy: REMOTECONTROL mints a
// short-lived console token and returns Stratos' own wss:// endpoint, which reverse-proxies the
// VNC WebSocket server-side (see cloudConsoleProxy). That keeps nova's console service off the
// public internet — only Stratos needs to be reachable.
//
// noVNC is rendered here rather than iframing nova's vnc_lite.html, so there is exactly one thing
// to proxy (the socket) and no asset-path rewriting.
import { useCallback, useEffect, useRef, useState } from "react"
import { Link, useParams } from "react-router-dom"
import RFB from "@novnc/novnc"
import { ArrowLeft, Keyboard, Maximize, RefreshCw } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { apiFetch } from "@/lib/api"
import { useCloudResource, useCloudScope, useProjectId } from "@/lib/hooks"
import { cn } from "@/lib/utils"

type ConsoleState = "connecting" | "connected" | "disconnected" | "error"

export default function ServerConsolePage() {
  const pid = useProjectId()
  const { resourceId = "" } = useParams()
  const scope = useCloudScope(pid)
  const { data: server } = useCloudResource(pid, resourceId)

  const screenRef = useRef<HTMLDivElement>(null)
  const rfbRef = useRef<RFB | null>(null)
  const [state, setState] = useState<ConsoleState>("connecting")
  const [message, setMessage] = useState("")
  // Bumping this re-runs the connect effect (Reconnect mints a fresh token — the old one is
  // short-lived and single-session).
  const [attempt, setAttempt] = useState(0)

  const name = (server?.data?.server?.name as string) ?? server?.name ?? resourceId

  useEffect(() => {
    if (!pid || !resourceId || !scope) return
    let cancelled = false
    let rfb: RFB | null = null

    setState("connecting")
    setMessage("")

    void (async () => {
      try {
        // REMOTECONTROL returns OUR proxied wss:// endpoint, not nova's novncproxy URL.
        const res = await apiFetch<{ result?: { url?: string } }>(
          `/project/${pid}/cloud/${resourceId}/action`,
          { method: "POST", body: { action: "REMOTECONTROL" }, cloud: scope },
        )
        const url = res?.result?.url
        if (cancelled) return
        if (!url) {
          setState("error")
          setMessage("No console URL returned.")
          return
        }
        if (!screenRef.current) return

        rfb = new RFB(screenRef.current, url)
        rfb.scaleViewport = true
        rfb.background = "#000"
        rfb.addEventListener("connect", () => {
          if (!cancelled) setState("connected")
        })
        rfb.addEventListener("disconnect", (e: Event) => {
          if (cancelled) return
          const clean = (e as CustomEvent<{ clean?: boolean }>).detail?.clean
          setState(clean ? "disconnected" : "error")
          setMessage(clean ? "Console session ended." : "Console connection lost.")
        })
        rfb.addEventListener("securityfailure", (e: Event) => {
          if (cancelled) return
          setState("error")
          setMessage((e as CustomEvent<{ reason?: string }>).detail?.reason ?? "Security handshake failed.")
        })
        rfbRef.current = rfb
      } catch (err) {
        if (cancelled) return
        setState("error")
        setMessage((err as Error).message)
      }
    })()

    return () => {
      cancelled = true
      // Always tear the socket down on unmount/reconnect — a stray RFB keeps the proxied
      // WebSocket (and the nova console session behind it) open.
      try {
        rfb?.disconnect()
      } catch {
        /* already closed */
      }
      rfbRef.current = null
    }
  }, [pid, resourceId, scope, attempt])

  const fullscreen = useCallback(() => {
    void screenRef.current?.requestFullscreen?.()
  }, [])

  return (
    <div className="flex min-h-0 flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="outline" size="sm" asChild>
          <Link to={`/p/${pid}/servers/${resourceId}`}>
            <ArrowLeft className="size-4" /> Back
          </Link>
        </Button>
        <div className="min-w-0">
          <div className="text-eyebrow">Console</div>
          <div className="truncate font-medium">{name}</div>
        </div>

        <div className="ml-auto flex items-center gap-2">
          <StatusDot state={state} />
          <Button
            variant="outline"
            size="sm"
            onClick={() => rfbRef.current?.sendCtrlAltDel()}
            disabled={state !== "connected"}
          >
            <Keyboard className="size-4" /> Ctrl+Alt+Del
          </Button>
          <Button variant="outline" size="sm" onClick={fullscreen} disabled={state !== "connected"}>
            <Maximize className="size-4" /> Fullscreen
          </Button>
          <Button variant="outline" size="sm" onClick={() => setAttempt((n) => n + 1)}>
            <RefreshCw className={cn("size-4", state === "connecting" && "animate-spin")} /> Reconnect
          </Button>
        </div>
      </div>

      <div className="relative min-h-[60vh] flex-1 overflow-hidden rounded-xl border bg-black">
        {/* noVNC renders its canvas into this element. */}
        <div ref={screenRef} className="size-full" />

        {state === "connecting" ? (
          <div className="absolute inset-0 grid place-items-center bg-black/80 p-6">
            <Skeleton className="h-8 w-48" />
          </div>
        ) : null}

        {state === "error" || state === "disconnected" ? (
          <div className="absolute inset-0 grid place-items-center bg-black/80 p-6 text-center">
            <div className="space-y-3">
              <p className="text-sm text-muted-foreground">{message || "Console unavailable."}</p>
              <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>
                <RefreshCw className="size-4" /> Reconnect
              </Button>
            </div>
          </div>
        ) : null}
      </div>
    </div>
  )
}

function StatusDot({ state }: { state: ConsoleState }) {
  const label =
    state === "connected" ? "Connected" : state === "connecting" ? "Connecting…" : "Disconnected"
  return (
    <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
      <span
        aria-hidden
        className={cn(
          "size-2 rounded-full",
          state === "connected" && "bg-emerald-500",
          state === "connecting" && "bg-amber-500",
          (state === "error" || state === "disconnected") && "bg-destructive",
        )}
      />
      {label}
    </span>
  )
}
