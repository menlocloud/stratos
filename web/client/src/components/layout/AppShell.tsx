import { NavLink, Outlet, useNavigate, useParams } from "react-router-dom"
import {
  BarChart3, BookOpen, Boxes, Camera, ChevronsUpDown, CreditCard, Database, FileClock, Globe, HardDrive,
  Image, KeyRound, Layers, LayoutDashboard, Lock, LogOut, Moon, Network, Receipt,
  ExternalLink, Gift, PiggyBank, Route as RouteIcon, Search as SearchIcon, Server, Settings, Share2, Shield, Sun, UserCircle, Users, Wallet, Waypoints, Zap,
} from "lucide-react"
import { useEffect, useState } from "react"
import { useAuth } from "@/lib/auth"
import { useFeatures, useProjects, useProjectInit, useUIMenu } from "@/lib/hooks"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel,
  DropdownMenuSeparator, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { cn } from "@/lib/utils"
import SearchModal from "@/components/search-modal"

type Item = {
  to: string
  label: string
  icon: React.ComponentType<{ className?: string }>
  // gating: hidden unless the /init menu carries the service enabled, and/or
  // the /features flag is present. No gate = always visible.
  service?: string
  feature?: string
}
type Group = { label: string; items: Item[] }

function navGroups(pid: string): Group[] {
  const p = `/p/${pid}`
  return [
    {
      label: "Overview",
      items: [{ to: `${p}/dashboard`, label: "Dashboard", icon: LayoutDashboard }],
    },
    {
      label: "Compute",
      items: [
        { to: `${p}/servers`, label: "Servers", icon: Server, service: "compute" },
        { to: `${p}/server-groups`, label: "Server groups", icon: Boxes, service: "compute" },
        { to: `${p}/keypairs`, label: "Key pairs", icon: KeyRound, service: "compute" },
        { to: `${p}/images`, label: "Images", icon: Image, service: "image" },
      ],
    },
    {
      label: "Storage",
      items: [
        { to: `${p}/volumes`, label: "Volumes", icon: HardDrive, service: "volumev3" },
        { to: `${p}/snapshots`, label: "Snapshots", icon: Camera, service: "volumev3" },
        { to: `${p}/object-storage`, label: "Object storage", icon: Database, service: "object-store" },
        { to: `${p}/s3-keys`, label: "S3 access keys", icon: KeyRound, service: "object-store" },
        { to: `${p}/shares`, label: "File shares", icon: Share2, service: "sharev2" },
      ],
    },
    {
      label: "Network",
      items: [
        { to: `${p}/networks`, label: "Networks", icon: Network, service: "network" },
        { to: `${p}/routers`, label: "Routers", icon: RouteIcon, service: "network" },
        { to: `${p}/ports`, label: "Ports", icon: Waypoints, service: "network" },
        { to: `${p}/floating-ips`, label: "Floating IPs", icon: Globe, service: "network" },
        { to: `${p}/security-groups`, label: "Security groups", icon: Shield, service: "network" },
        { to: `${p}/load-balancers`, label: "Load balancers", icon: Zap, service: "load-balancer" },
        { to: `${p}/dns`, label: "DNS zones", icon: Globe, service: "dns" },
      ],
    },
    {
      label: "Platform",
      items: [
        { to: `${p}/stacks`, label: "Stacks", icon: Layers, service: "orchestration" },
        { to: `${p}/secrets`, label: "Secrets", icon: Lock, service: "key-manager" },
      ],
    },
    {
      label: "Billing",
      items: [
        { to: `${p}/billing/funds`, label: "Funds", icon: Wallet, feature: "billing" },
        { to: `${p}/billing/cards`, label: "Cards", icon: CreditCard, feature: "billing" },
        { to: `${p}/billing/history`, label: "Billing history", icon: Receipt, feature: "billing" },
        { to: `${p}/billing/savings`, label: "Savings plans", icon: PiggyBank, feature: "billing" },
        { to: `${p}/billing/credits`, label: "Promotional credits", icon: Gift, feature: "billing" },
      ],
    },
    {
      label: "Organization",
      items: [
        { to: `${p}/org/billing`, label: "Billing", icon: BarChart3 },
        { to: `${p}/org/members`, label: "Members", icon: Users },
        { to: `${p}/org/projects`, label: "Projects", icon: Boxes },
        { to: `${p}/org/audit`, label: "Audit log", icon: FileClock },
        { to: `${p}/org/roles`, label: "Roles", icon: Shield },
        { to: `${p}/org/settings`, label: "Settings", icon: Settings },
      ],
    },
  ]
}

// Applies the admin-driven gates: /init menu.items (service keys, admin's cloud
// provider Services toggles) + /features flags. While either query is loading,
// nothing is filtered out (avoids a nav flash); once loaded, a service absent
// or disabled hides its items, an absent feature hides feature-gated items.
function useGatedNavGroups(pid: string): Group[] {
  const { data: init } = useUIMenu(pid)
  const { data: features } = useFeatures()
  const items = init?.menu?.items
  const featureSet = features ? new Set(features) : undefined
  // Admin Custom Menu items (newMenuItem:true) render as a trailing "More" group.
  const more: Group[] = []
  if (items) {
    const customs = Object.entries(items)
      .filter(([, v]) => (v as Record<string, unknown>)?.newMenuItem === true && (v as Record<string, unknown>)?.enabled === true)
      .sort((a, b) => Number((a[1] as Record<string, unknown>).order ?? 0) - Number((b[1] as Record<string, unknown>).order ?? 0))
    if (customs.length) {
      more.push({
        label: "More",
        items: customs.map(([slug, v]) => ({
          to: `/p/${pid}/more/${slug}`,
          label: ((v as Record<string, unknown>).displayName as string) ?? slug,
          icon: ExternalLink,
        })),
      })
    }
  }
  return navGroups(pid)
    .map((g) => ({
      ...g,
      items: g.items.filter((it) => {
        if (it.service && items && !(items[it.service]?.enabled === true)) return false
        if (it.feature && featureSet && !featureSet.has(it.feature)) return false
        return true
      }),
    }))
    .filter((g) => g.items.length > 0)
    .concat(more)
}

function useTheme() {
  const [dark, setDark] = useState(() => localStorage.getItem("stratos.theme") === "dark")
  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark)
    localStorage.setItem("stratos.theme", dark ? "dark" : "light")
  }, [dark])
  return { dark, toggle: () => setDark((d) => !d) }
}

export function AppShell() {
  const { pid = "" } = useParams()
  const auth = useAuth()
  const navigate = useNavigate()
  const { data: projects } = useProjects()
  const current = projects?.find((p) => p.id === pid)
  const { dark, toggle } = useTheme()
  const gatedGroups = useGatedNavGroups(pid)
  const [searchOpen, setSearchOpen] = useState(false)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault()
        setSearchOpen((o) => !o)
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [])
  useProjectInit(pid)

  return (
    <div className="flex min-h-screen">
      {/* rail */}
      <aside className="fixed inset-y-0 left-0 z-30 flex w-60 flex-col bg-sidebar text-sidebar-foreground">
        <div className="flex h-14 items-center gap-2 px-5">
          <span className="font-display text-lg font-semibold tracking-wide text-white">
            Stratos<span className="text-sidebar-primary">.</span>
          </span>
          <span className="ml-1 rounded border border-sidebar-border px-1.5 py-0.5 text-[10px] uppercase tracking-widest text-sidebar-foreground/70">
            console
          </span>
        </div>

        {/* project switcher */}
        <div className="px-3 pb-2">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button className="flex w-full items-center justify-between rounded-md border border-sidebar-border bg-sidebar-accent/50 px-3 py-2 text-left text-sm hover:bg-sidebar-accent">
                <span className="truncate">{current?.name ?? "Select project"}</span>
                <ChevronsUpDown className="size-4 opacity-60" />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent className="w-56" align="start">
              <DropdownMenuLabel>Projects</DropdownMenuLabel>
              {(projects ?? []).map((p) => (
                <DropdownMenuItem key={p.id} onClick={() => navigate(`/p/${p.id}/dashboard`)}>
                  {p.name}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>

        <nav className="flex-1 space-y-4 overflow-y-auto px-3 pb-6 pt-2">
          {gatedGroups.map((g) => (
            <div key={g.label}>
              <div className="px-2 pb-1 text-[11px] font-medium uppercase tracking-widest text-sidebar-foreground/50">
                {g.label}
              </div>
              <div className="space-y-0.5">
                {g.items.map((it) => (
                  <NavLink
                    key={it.to}
                    to={it.to}
                    className={({ isActive }) =>
                      cn(
                        "group flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm transition-colors",
                        isActive
                          ? "bg-sidebar-accent text-sidebar-accent-foreground shadow-[inset_2px_0_0_var(--sidebar-primary)]"
                          : "hover:bg-sidebar-accent/60 hover:text-sidebar-accent-foreground",
                      )
                    }
                  >
                    <it.icon className="size-4 opacity-70 group-hover:opacity-100" />
                    {it.label}
                  </NavLink>
                ))}
              </div>
            </div>
          ))}
        </nav>
      </aside>

      {/* content */}
      <div className="ml-60 flex min-h-screen flex-1 flex-col">
        <header className="sticky top-0 z-20 flex h-14 items-center justify-end gap-2 border-b bg-background/80 px-6 backdrop-blur">
          <Button
            variant="outline"
            size="sm"
            className="mr-auto w-56 justify-between text-muted-foreground"
            onClick={() => setSearchOpen(true)}
          >
            <span className="inline-flex items-center gap-2"><SearchIcon className="size-4" /> Search…</span>
            <kbd className="rounded border px-1.5 font-mono text-[10px]">⌘K</kbd>
          </Button>
          <Button variant="ghost" size="sm" asChild>
            <NavLink to="/docs">
              <BookOpen className="size-4" /> Docs
            </NavLink>
          </Button>
          <Button variant="ghost" size="icon" onClick={toggle} aria-label="Toggle theme">
            {dark ? <Sun className="size-4" /> : <Moon className="size-4" />}
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="outline" size="sm" className="gap-2">
                <span className="size-2 rounded-full bg-ok" />
                {auth.user?.profile.email ?? "Account"}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuLabel className="font-mono text-xs">{auth.user?.profile.sub}</DropdownMenuLabel>
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={() => navigate(`/p/${pid}/account`)}>
                <UserCircle className="mr-2 size-4" /> Account settings
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => void auth.signoutRedirect()}>
                <LogOut className="mr-2 size-4" /> Sign out
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </header>
        <SearchModal pid={pid} open={searchOpen} onOpenChange={setSearchOpen} />
        <main className="flex-1 px-6 py-6">
          <div className="mx-auto w-full max-w-6xl">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  )
}
