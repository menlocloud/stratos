import { NavLink, Outlet } from "react-router-dom"
import {
  Banknote, Blocks, BookOpen, Building2, CheckSquare, Cloud, CreditCard, FileClock, FileText,
  FolderKanban, Gauge, KeyRound, LayoutDashboard, LogOut, Mail, Menu as MenuIcon,
  Moon, Percent, PiggyBank, Receipt, Server, Settings2, Shield, Sun, Tag, Users, Wallet,
} from "lucide-react"
import { useEffect, useState } from "react"
import { useAdminMe, useAuth } from "@/lib/auth"
import { useAdminGet } from "@/lib/hooks"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel,
  DropdownMenuSeparator, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { cn } from "@/lib/utils"

// Mirrors the OLD admin's structure: Dashboard · Client Area (users → cloud
// resources, in the old sidebar order) · System (the old Settings areas).
// Every item carries the old app's requiredPermission and is hidden unless
// /admin/me grants it; Validations additionally needs the billing
// configuration's autoActivationFlow to enable billing-profile validation.
type Item = {
  to: string
  label: string
  icon: React.ComponentType<{ className?: string }>
  permission?: string
  special?: "validations"
}
type Group = { label: string; items: Item[] }

const groups: Group[] = [
  {
    label: "Overview",
    items: [{ to: "/dashboard", label: "Dashboard", icon: LayoutDashboard }],
  },
  {
    label: "Client area",
    items: [
      { to: "/clients/users", label: "Users", icon: Users, permission: "admin:user:read" },
      { to: "/clients/projects", label: "Projects", icon: FolderKanban, permission: "admin:project:read" },
      { to: "/clients/organizations", label: "Organizations", icon: Building2, permission: "admin:organization:read" },
      { to: "/clients/billing-profiles", label: "Billing profiles", icon: Wallet, permission: "admin:billing_profile:read" },
      { to: "/clients/validations", label: "Validations", icon: CheckSquare, permission: "admin:billing_profile:read", special: "validations" },
      { to: "/clients/bills", label: "Customer bills", icon: Receipt, permission: "admin:bill:read" },
      { to: "/clients/cloud-resources", label: "Cloud resources", icon: Server, permission: "admin:cloud_resource:read" },
      { to: "/clients/transactions", label: "Transactions", icon: CreditCard, permission: "admin:transaction:read" },
      { to: "/clients/bank-transfers", label: "Bank transfers", icon: Banknote, permission: "admin:transaction:read" },
    ],
  },
  {
    label: "System",
    items: [
      { to: "/system/configuration", label: "Platform", icon: FileText, permission: "admin:platform_config:read" },
      { to: "/system/price-plans", label: "Price plans", icon: Tag, permission: "admin:price_plan:read" },
      { to: "/system/billing-configuration", label: "Billing configuration", icon: Gauge, permission: "admin:billing_config:read" },
      { to: "/system/menu", label: "Custom menu", icon: MenuIcon, permission: "admin:menu:manage" },
      { to: "/system/taxes", label: "Taxes", icon: Percent, permission: "admin:tax:read" },
      { to: "/system/templates", label: "Message templates", icon: Mail, permission: "admin:message_template:read" },
      { to: "/system/catalog", label: "Instances", icon: Blocks, permission: "admin:flavor_category:manage" },
      { to: "/system/cloud-providers", label: "Cloud providers", icon: Cloud, permission: "admin:service:read" },
      { to: "/system/savings-plans", label: "Savings plans", icon: PiggyBank, permission: "admin:savings_plan:read" },
      { to: "/system/promotions", label: "Promotion codes", icon: Gauge, permission: "admin:promotional_credit:manage" },
      { to: "/system/integrations", label: "Integrations", icon: Settings2, permission: "admin:integration:read" },
      { to: "/system/roles", label: "Admin permissions", icon: Shield, permission: "admin:permission:read" },
      { to: "/system/hmac-keys", label: "API keys", icon: KeyRound, permission: "admin:hmac_key:manage" },
      { to: "/audit", label: "Audit log", icon: FileClock, permission: "admin:audit:read" },
    ],
  },
]

function useTheme() {
  const [dark, setDark] = useState(() => localStorage.getItem("stratos.theme") === "dark")
  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark)
    localStorage.setItem("stratos.theme", dark ? "dark" : "light")
  }, [dark])
  return { dark, toggle: () => setDark((d) => !d) }
}

// Permission match supports the wildcard grants /admin/me can carry
// (admin:* or admin:<area>:*), mirroring the API's ExpandPatterns.
function hasPermission(granted: string[] | undefined, want?: string): boolean {
  if (!want) return true
  if (!granted) return true // still loading — don't flash-hide
  if (granted.includes(want) || granted.includes("admin:*")) return true
  const area = want.split(":").slice(0, 2).join(":")
  return granted.includes(`${area}:*`)
}

export function AdminShell() {
  const auth = useAuth()
  const { data: me } = useAdminMe()
  const { dark, toggle } = useTheme()
  // Validations visibility follows the old app: hidden unless the billing
  // configuration enables billing-profile validation in its activation flow.
  const { data: billingCfg } = useAdminGet<Record<string, any>>("/admin/billing/configuration/current")
  const validationMode = billingCfg?.autoActivationFlow?.billingProfileValidation as string | undefined
  const validationsVisible = validationMode === "REQUIRED" || validationMode === "ALTERNATIVE"

  const visibleGroups = groups
    .map((g) => ({
      ...g,
      items: g.items.filter((it) => {
        if (!hasPermission(me?.permissions, it.permission)) return false
        if (it.special === "validations" && !validationsVisible) return false
        return true
      }),
    }))
    .filter((g) => g.items.length > 0)

  return (
    <div className="flex min-h-screen">
      <aside className="fixed inset-y-0 left-0 z-30 flex w-60 flex-col bg-sidebar text-sidebar-foreground">
        <div className="flex h-14 items-center gap-2 px-5">
          <span className="font-display text-lg font-semibold tracking-wide text-white">
            Stratos<span className="text-sidebar-primary">.</span>
          </span>
          <span className="ml-1 rounded border border-sidebar-border px-1.5 py-0.5 text-[10px] uppercase tracking-widest text-sidebar-foreground/70">
            admin
          </span>
        </div>

        <nav className="flex-1 space-y-4 overflow-y-auto px-3 pb-6 pt-2">
          {visibleGroups.map((g) => (
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

      <div className="ml-60 flex min-h-screen flex-1 flex-col">
        <header className="sticky top-0 z-20 flex h-14 items-center justify-end gap-2 border-b bg-background/80 px-6 backdrop-blur">
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
                <Shield className="size-3.5 text-warn" />
                {me?.role ?? "Admin"}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuLabel className="font-mono text-xs">
                {me?.email ?? auth.user?.profile.preferred_username}
              </DropdownMenuLabel>
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={() => void auth.signoutRedirect()}>
                <LogOut className="mr-2 size-4" /> Sign out
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </header>
        <main className="flex-1 px-6 py-6">
          <div className="mx-auto w-full max-w-6xl">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  )
}
