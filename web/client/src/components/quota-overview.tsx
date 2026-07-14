import type { LucideIcon } from "lucide-react"
import { CircuitBoard, Cpu, HardDrive, MemoryStick, Server, TriangleAlert } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import {
  Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle,
} from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import type { CloudScope } from "@/lib/api"
import type { ProjectQuotaUsage, QuotaMetric } from "@/lib/types"
import { cn } from "@/lib/utils"

type QuotaOverviewProps = {
  data?: ProjectQuotaUsage
  scope?: CloudScope
  scopeOptions?: Array<{ key: string; label: string; scope: CloudScope }>
  selectedScopeKey?: string
  onScopeChange?: (key: string) => void
  isLoading?: boolean
  error?: unknown
}

type MetricTileProps = {
  label: string
  metric?: QuotaMetric
  icon: LucideIcon
  format?: (value: number) => string
  details?: string[]
}

const countFormat = new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 })

function formatCount(value: number): string {
  return countFormat.format(value)
}

function formatRam(value: number): string {
  if (Math.abs(value) < 1024 && value !== 0) return `${formatCount(value)} MiB`
  return `${formatCount(value / 1024)} GiB`
}

function formatGiB(value: number): string {
  return `${formatCount(value)} GiB`
}

function metricValues(metric?: QuotaMetric) {
  if (
    !metric ||
    !Number.isFinite(metric.used) ||
    !Number.isFinite(metric.reserved) ||
    !Number.isFinite(metric.limit)
  ) {
    return undefined
  }
  const used = Math.max(0, metric.used)
  const reserved = Math.max(0, metric.reserved)
  return { used, reserved, consumed: used + reserved, limit: metric.limit }
}

function compactMetric(label: string, metric?: QuotaMetric, format = formatCount): string | undefined {
  const values = metricValues(metric)
  if (!values) return undefined
  const limit = values.limit < 0 ? "Unlimited" : format(values.limit)
  return `${label} ${format(values.consumed)} / ${limit}`
}

function MetricTile({ label, metric, icon: Icon, format = formatCount, details = [] }: MetricTileProps) {
  const values = metricValues(metric)
  if (!values) {
    return (
      <div className="min-w-0 rounded-xl border bg-muted/20 p-4">
        <div className="flex items-center gap-2 text-sm font-medium">
          <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
          <span className="break-words">{label}</span>
        </div>
        <p className="mt-3 text-sm text-muted-foreground">Unavailable</p>
        {details.map((detail) => (
          <p key={detail} className="mt-1 break-words text-xs text-muted-foreground">{detail}</p>
        ))}
      </div>
    )
  }

  const { used, reserved, consumed, limit } = values
  const unlimited = limit < 0
  const ratio = unlimited ? 0 : limit > 0 ? consumed / limit : 1
  const percent = Math.min(Math.max(ratio * 100, 0), 100)
  const over = !unlimited && consumed > limit
  const exhausted = !unlimited && consumed >= limit
  const nearing = !unlimited && ratio >= 0.8
  const remaining = unlimited ? undefined : Math.max(limit - consumed, 0)
  const valueText = unlimited
    ? `${format(consumed)} used, unlimited`
    : `${format(consumed)} used of ${format(limit)}`

  return (
    <div className="min-w-0 rounded-xl border bg-muted/20 p-4">
      <div className="flex items-center gap-2 text-sm font-medium">
        <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="break-words">{label}</span>
      </div>

      <div
        className={cn(
          "mt-3 font-mono text-lg font-semibold tabular-nums",
          exhausted ? "text-destructive" : nearing ? "text-warning-text" : undefined,
        )}
      >
        {format(consumed)}
        <span className="ml-1 text-sm font-normal text-muted-foreground">
          / {unlimited ? "Unlimited" : format(limit)}
        </span>
      </div>

      {unlimited ? (
        <div className="mt-3 h-1.5 rounded-full bg-muted" aria-label={`${label}: ${valueText}`} />
      ) : limit <= 0 ? (
        <div
          className="mt-3 h-1.5 rounded-full bg-destructive"
          role="status"
          aria-label={`${label}: ${valueText}`}
        />
      ) : (
        <div
          className="mt-3 h-1.5 overflow-hidden rounded-full bg-muted"
          role="meter"
          aria-label={`${label} quota`}
          aria-valuemin={0}
          aria-valuemax={limit}
          aria-valuenow={Math.min(consumed, limit)}
          aria-valuetext={valueText}
        >
          <div
            className={cn(
              "h-full rounded-full transition-[width]",
              exhausted ? "bg-destructive" : nearing ? "bg-warning" : "bg-primary",
            )}
            style={{ width: `${percent}%` }}
          />
        </div>
      )}

      <p className="mt-2 break-words text-xs text-muted-foreground">
        {over
          ? `${format(consumed - limit)} over quota`
          : unlimited
            ? `${format(consumed)} consumed`
            : `${format(remaining ?? 0)} remaining`}
        {reserved > 0 ? ` | ${format(used)} used + ${format(reserved)} reserved` : ""}
      </p>
      {details.map((detail) => (
        <p key={detail} className="mt-1 break-words text-xs text-muted-foreground">{detail}</p>
      ))}
    </div>
  )
}

function gpuMetrics(data?: ProjectQuotaUsage): Array<{ key: string; label: string; metric: QuotaMetric }> {
  const limits = data?.gpu?.limits ?? {}
  const usage = data?.gpu?.usage ?? {}
  const usageAvailable = data?.gpu?.usageAvailable !== false
  const metric = (model: string, limit: number): QuotaMetric => ({
    used: usageAvailable ? Number(usage[model] ?? 0) : Number.NaN,
    reserved: 0,
    limit: Number(limit),
  })
  const explicit = new Set(Object.keys(limits).filter((model) => model !== "*"))
  const rows = Object.entries(limits)
    .filter(([model]) => model !== "*")
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([model, limit]) => ({
      key: model,
      label: `GPU / ${model}`,
      metric: metric(model, limit),
    }))

  if (Object.prototype.hasOwnProperty.call(limits, "*")) {
    const fallbackLimit = Number(limits["*"])
    const fallbackModels = Object.keys(usage)
      .filter((model) => model !== "*" && !explicit.has(model))
      .sort((a, b) => a.localeCompare(b))

    if (fallbackModels.length > 0) {
      rows.push(...fallbackModels.map((model) => ({
        key: `fallback:${model}`,
        label: `GPU / ${model}`,
        metric: metric(model, fallbackLimit),
      })))
    } else {
      rows.push({
        key: "fallback:*",
        label: "GPU / Other models",
        metric: metric("*", fallbackLimit),
      })
    }
  }

  return rows
}

export function QuotaOverview({
  data,
  scope,
  scopeOptions = [],
  selectedScopeKey,
  onScopeChange,
  isLoading = false,
  error,
}: QuotaOverviewProps) {
  const region = data?.region || scope?.region
  const storageDetails = [
    compactMetric("Volumes", data?.storage?.volumes),
    compactMetric("Snapshots", data?.storage?.snapshots),
    data?.storage?.perVolumeGigabytes
      ? (() => {
          const values = metricValues(data.storage?.perVolumeGigabytes)
          if (!values) return undefined
          return `Max per volume ${values.limit < 0 ? "Unlimited" : formatGiB(values.limit)}`
        })()
      : undefined,
  ].filter((detail): detail is string => !!detail)
  const gpu = gpuMetrics(data)
  const warnings = data?.warnings ?? []

  return (
    <Card className="min-w-0">
      <CardHeader className="border-b">
        <CardTitle className="text-base">Quota &amp; usage</CardTitle>
        <CardDescription>Current consumption, including resources reserved by in-flight operations.</CardDescription>
        <CardAction>
          {scopeOptions.length > 1 && selectedScopeKey && onScopeChange ? (
            <Select value={selectedScopeKey} onValueChange={onScopeChange}>
              <SelectTrigger className="w-36 sm:w-64" aria-label="Quota location">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {scopeOptions.map((option) => (
                  <SelectItem key={option.key} value={option.key}>{option.label}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          ) : (
            <Badge
              variant="outline"
              className="max-w-36 truncate sm:max-w-64"
              title={region ? `Region: ${region}` : "No region"}
            >
              {region ? `Region: ${region}` : "No region"}
            </Badge>
          )}
        </CardAction>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4" aria-label="Loading quota usage">
            {[0, 1, 2, 3].map((i) => <Skeleton key={i} className="h-32" />)}
          </div>
        ) : !scope ? (
          <p className="rounded-lg border border-dashed p-5 text-sm text-muted-foreground">
            Quota usage is unavailable until a cloud location is attached to this project.
          </p>
        ) : error ? (
          <p className="rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive" role="alert">
            Could not load quota usage{error instanceof Error && error.message ? `: ${error.message}` : "."}
          </p>
        ) : !data ? (
          <p className="rounded-lg border border-dashed p-5 text-sm text-muted-foreground">
            Quota usage is currently unavailable for this region.
          </p>
        ) : (
          <>
            <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
              <MetricTile label="Instances" metric={data.compute?.instances} icon={Server} />
              <MetricTile label="vCPU" metric={data.compute?.cores} icon={Cpu} />
              <MetricTile label="RAM" metric={data.compute?.ramMb} icon={MemoryStick} format={formatRam} />
              <MetricTile
                label="Block storage"
                metric={data.storage?.gigabytes}
                icon={HardDrive}
                format={formatGiB}
                details={storageDetails}
              />
              {gpu.map((item) => (
                <MetricTile
                  key={item.key}
                  label={item.label}
                  metric={item.metric}
                  icon={CircuitBoard}
                  details={["Project-wide custom quota"]}
                />
              ))}
            </div>

            {warnings.length > 0 ? (
              <div className="mt-4 flex gap-3 rounded-lg border border-warning/40 bg-warning/10 p-3 text-sm text-warning-text" role="status">
                <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
                <div>
                  <p className="font-medium">Some quota data may be incomplete.</p>
                  <ul className="mt-1 list-disc space-y-1 pl-4">
                    {warnings.map((warning, index) => <li key={`${warning}:${index}`}>{warning}</li>)}
                  </ul>
                </div>
              </div>
            ) : null}
          </>
        )}
      </CardContent>
    </Card>
  )
}
