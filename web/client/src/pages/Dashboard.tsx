import { useMemo } from "react"
import { Link } from "react-router-dom"
import { Cell, Pie, PieChart } from "recharts"
import { CalendarClock, Receipt, Wallet } from "lucide-react"
import { PageHeader } from "@/components/layout/PageHeader"
import { StatCard } from "@/components/stat-card"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { ChartContainer, ChartTooltip, ChartTooltipContent, type ChartConfig } from "@/components/ui/chart"
import { Skeleton } from "@/components/ui/skeleton"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useBillingSummary, useCostInfo, useProject, useProjectId } from "@/lib/hooks"
import { fmtMoney, timeAgo } from "@/lib/format"

// Stable slot assignment: sort categories so a service keeps its color across
// months/filters; >7 categories fold into "Other".
const MAX_SLICES = 7

function serviceLabel(key: string): string {
  const label = key.toLowerCase().replace(/_/g, " ")
  return label.charAt(0).toUpperCase() + label.slice(1)
}

export function DashboardPage() {
  const pid = useProjectId()
  const { project } = useProject(pid)
  const { data: cost, isLoading: costLoading } = useCostInfo(pid)
  const { data: summary } = useBillingSummary(pid)

  const currency = (summary?.currency as string) ?? "USD"
  const projectCost = cost?.projects?.[pid] ?? cost

  const byService = useMemo(() => {
    const entries = Object.entries(projectCost?.currentMonthCostsByType ?? {}).sort(([a], [b]) =>
      a.localeCompare(b),
    )
    const head = entries.slice(0, MAX_SLICES)
    const rest = entries.slice(MAX_SLICES)
    const rows = head.map(([key, value], i) => ({
      key,
      label: serviceLabel(key),
      value,
      color: `var(--chart-${i + 1})`,
    }))
    if (rest.length) {
      rows.push({
        key: "OTHER",
        label: "Other",
        value: rest.reduce((s, [, v]) => s + v, 0),
        color: "var(--chart-other)",
      })
    }
    return rows
  }, [projectCost?.currentMonthCostsByType])

  const donutConfig = useMemo(
    () =>
      Object.fromEntries(
        byService.map((r) => [r.key, { label: r.label, color: r.color }]),
      ) satisfies ChartConfig,
    [byService],
  )

  const monthTotal = byService.reduce((s, r) => s + r.value, 0)

  return (
    <>
      <PageHeader
        title={project?.name ?? "Dashboard"}
        eyebrow="Overview"
        description="Live usage, balance and this month's spend for this project."
      />

      {costLoading ? (
        <div className="grid gap-4 md:grid-cols-3">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-28" />
          ))}
        </div>
      ) : (
        <div className="grid gap-4 @3xl:grid-cols-3 @container md:grid-cols-3">
          <StatCard
            label="Balance"
            numericValue={summary?.balance ?? 0}
            format={{ style: "currency", currency }}
            hint={`Account credit ${fmtMoney(summary?.accountCredit, currency)} · Promo ${fmtMoney(summary?.promotionalCredit, currency)}`}
            icon={Wallet}
          />
          <StatCard
            label="This month"
            numericValue={projectCost?.currentMonthCosts ?? 0}
            format={{ style: "currency", currency }}
            hint={`Forecast ${fmtMoney(projectCost?.forecastedMonthEndCosts, currency)}`}
            icon={CalendarClock}
            trend={
              projectCost?.forecastedMonthEndCosts && projectCost?.lastMonthCosts
                ? {
                    direction:
                      Number(projectCost.forecastedMonthEndCosts) >= Number(projectCost.lastMonthCosts)
                        ? "up"
                        : "down",
                    label: "vs last month",
                    // Rising spend is a caution signal, not a win.
                    tone:
                      Number(projectCost.forecastedMonthEndCosts) >= Number(projectCost.lastMonthCosts)
                        ? ("negative" as const)
                        : ("positive" as const),
                  }
                : undefined
            }
          />
          <StatCard
            label="Due now"
            numericValue={projectCost?.dueAmount ?? 0}
            format={{ style: "currency", currency }}
            hint={summary?.status ? `Billing profile ${String(summary.status).toLowerCase()}` : undefined}
            icon={Receipt}
          />
        </div>
      )}

      <div className="mt-6 grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader className="border-b">
            <CardTitle className="text-base">Cost by service</CardTitle>
            <p className="text-sm text-muted-foreground">This month, rated so far.</p>
          </CardHeader>
          <CardContent>
            {byService.length > 0 ? (
              <div className="flex flex-col items-center gap-4 sm:flex-row sm:gap-6">
                <div className="relative">
                  <ChartContainer config={donutConfig} className="aspect-square h-44">
                    <PieChart>
                      <ChartTooltip
                        content={<ChartTooltipContent hideLabel formatter={(value, name) => (
                          <div className="flex w-full items-center justify-between gap-4">
                            <span className="text-muted-foreground">{donutConfig[name as string]?.label ?? name}</span>
                            <span className="font-mono font-medium tabular-nums">{fmtMoney(Number(value), currency)}</span>
                          </div>
                        )} />}
                      />
                      <Pie
                        data={byService}
                        dataKey="value"
                        nameKey="key"
                        innerRadius={55}
                        outerRadius={82}
                        paddingAngle={2}
                        strokeWidth={2}
                        isAnimationActive={false}
                      >
                        {byService.map((r) => (
                          <Cell key={r.key} fill={r.color} stroke="var(--card)" />
                        ))}
                      </Pie>
                    </PieChart>
                  </ChartContainer>
                  {/* Donut center total: HTML, wearing text tokens. */}
                  <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center">
                    <span className="text-xs text-muted-foreground">Total</span>
                    <span className="font-display text-lg font-semibold tabular-nums">
                      {fmtMoney(monthTotal, currency)}
                    </span>
                  </div>
                </div>
                <div className="flex w-full flex-1 flex-col gap-1.5">
                  {byService.map((r) => (
                    <div
                      key={r.key}
                      className="flex items-center justify-between rounded bg-muted/40 px-2 py-1.5 text-xs"
                    >
                      <div className="flex items-center gap-2">
                        <span className="size-2 rounded-full" style={{ backgroundColor: r.color }} />
                        <span className="text-muted-foreground">{r.label}</span>
                      </div>
                      <span className="font-medium tabular-nums">{fmtMoney(r.value, currency)}</span>
                    </div>
                  ))}
                </div>
              </div>
            ) : (
              <p className="py-6 text-center text-sm text-muted-foreground">No usage recorded yet this month.</p>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="border-b">
            <CardTitle className="text-base">Top cost generators</CardTitle>
            <p className="text-sm text-muted-foreground">Most expensive resources this month.</p>
          </CardHeader>
          <CardContent className="p-0">
            {projectCost?.topResourcePrices?.length ? (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Resource</TableHead>
                    <TableHead>Type</TableHead>
                    <TableHead>Created</TableHead>
                    <TableHead className="text-right">Cost</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {projectCost.topResourcePrices.map((r, i) => (
                    <TableRow key={i}>
                      <TableCell className="font-medium">
                        {r.resource?.type === "SERVER" && r.resource?.id ? (
                          <Link className="inline-block py-1 hover:underline" to={`/p/${pid}/servers/${r.resource.id}`}>
                            {r.resource?.name ?? r.resource?.id}
                          </Link>
                        ) : (
                          (r.resource?.name ?? r.resource?.id ?? "—")
                        )}
                      </TableCell>
                      <TableCell>
                        <Badge variant="secondary">{serviceLabel(r.resource?.type ?? "—")}</Badge>
                      </TableCell>
                      <TableCell className="text-muted-foreground">{timeAgo(r.resource?.createdAt)}</TableCell>
                      <TableCell className="text-right font-mono tabular-nums">{fmtMoney(r.currentCost ?? r.price, currency)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            ) : (
              <p className="py-6 text-center text-sm text-muted-foreground">Nothing billed yet.</p>
            )}
          </CardContent>
        </Card>
      </div>
    </>
  )
}
