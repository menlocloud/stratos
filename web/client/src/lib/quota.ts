import type { ProjectQuotaUsage, QuotaMetric } from "./types"

export type QuotaViolation = {
  resource: "instances" | "cores" | "ram" | "gpu" | "volumes" | "gigabytes" | "per-volume"
  message: string
}

export type FlavorQuotaRequest = {
  data?: {
    vcpus?: number
    ram?: number
    extra_specs?: Record<string, unknown>
  }
}

function finiteNonNegative(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : 0
}

export function quotaConsumed(metric: QuotaMetric): number {
  return finiteNonNegative(metric.used) + finiteNonNegative(metric.reserved)
}

export function quotaRemaining(metric?: QuotaMetric): number | null | undefined {
  if (!metric) return undefined
  if (metric.limit < 0) return null
  return Math.max(0, metric.limit - quotaConsumed(metric))
}

function exceeds(metric: QuotaMetric | undefined, requested: number): boolean {
  return !!metric && metric.limit >= 0 && quotaConsumed(metric) + requested > metric.limit
}

function normalizeGPUAlias(value: string): string {
  return value.trim().toLowerCase().replaceAll("_", "-")
}

// Matches Go's strconv.Atoi so the pre-check agrees with the server gate: plain
// decimal integers only — "1.0", "2e0", "0x2" are rejected, not coerced.
function parseGPUCount(value: string): number {
  const trimmed = value.trim()
  return /^\+?\d+$/.test(trimmed) ? Number(trimmed) : 0
}

// Keep this in lock-step with internal/cloud/gpu.go. Nova's first PCI alias names the
// quota bucket; every valid device count in the alias list contributes to that bucket.
export function gpuFromFlavor(specs?: Record<string, unknown>): { model: string; count: number } | undefined {
  if (!specs) return undefined
  const alias = specs["pci_passthrough:alias"]
  if (typeof alias === "string" && alias.trim()) {
    let model = ""
    let count = 0
    for (const entry of alias.split(",")) {
      const separator = entry.indexOf(":")
      if (separator < 0) continue
      const parsed = parseGPUCount(entry.slice(separator + 1))
      if (parsed <= 0) continue
      if (!model) model = normalizeGPUAlias(entry.slice(0, separator))
      count += parsed
    }
    if (model && count > 0) return { model, count }
  }

  const vgpu = specs["resources:VGPU"]
  if (typeof vgpu === "string") {
    const count = parseGPUCount(vgpu)
    if (count > 0) return { model: "vgpu", count }
  }
  return undefined
}

function metricViolation(
  metric: QuotaMetric | undefined,
  requested: number,
  resource: QuotaViolation["resource"],
  label: string,
  format: (value: number) => string = String,
): QuotaViolation | undefined {
  if (!exceeds(metric, requested) || !metric) return undefined
  const consumed = quotaConsumed(metric)
  return {
    resource,
    message: `${label}: ${format(consumed)} in use/reserved + ${format(requested)} requested exceeds the ${format(metric.limit)} limit.`,
  }
}

function formatMiB(value: number): string {
  return value !== 0 && value % 1024 === 0 ? `${value / 1024} GiB` : `${value} MiB`
}

function formatGiB(value: number): string {
  return `${value} GiB`
}

export function serverQuotaViolations(
  quota: ProjectQuotaUsage | undefined,
  flavor: FlavorQuotaRequest | undefined,
): QuotaViolation[] {
  if (!quota || !flavor) return []

  const violations = [
    metricViolation(quota.compute?.instances, 1, "instances", "Instances"),
    metricViolation(quota.compute?.cores, finiteNonNegative(flavor.data?.vcpus), "cores", "vCPU"),
    metricViolation(quota.compute?.ramMb, finiteNonNegative(flavor.data?.ram), "ram", "RAM", formatMiB),
  ].filter((item): item is QuotaViolation => !!item)

  const gpu = gpuFromFlavor(flavor.data?.extra_specs)
  if (gpu && quota.gpu.usageAvailable !== false) {
    const exact = quota.gpu.limits[gpu.model]
    const wildcard = quota.gpu.limits["*"]
    const limit = exact ?? wildcard
    const used = finiteNonNegative(quota.gpu.usage[gpu.model])
    if (limit !== undefined && used + gpu.count > limit) {
      violations.push({
        resource: "gpu",
        message: `GPU ${gpu.model}: ${used} in use + ${gpu.count} requested exceeds the ${limit} limit.`,
      })
    }
  }

  return violations
}

export function volumeCreateQuotaViolations(
  quota: ProjectQuotaUsage | undefined,
  requestedGiB: number,
  volumeType?: string,
): QuotaViolation[] {
  if (!quota?.storage || !Number.isFinite(requestedGiB) || requestedGiB <= 0) return []

  const violations = [
    metricViolation(quota.storage.volumes, 1, "volumes", "Volumes"),
    metricViolation(quota.storage.gigabytes, requestedGiB, "gigabytes", "Block storage", formatGiB),
  ].filter((item): item is QuotaViolation => !!item)

  const normalizedType = volumeType?.trim()
  const typedQuota = normalizedType ? quota.storage.volumeTypes?.[normalizedType] : undefined
  if (typedQuota && normalizedType) {
    const typeLabel = `Volume type ${normalizedType}`
    const typedViolations = [
      metricViolation(typedQuota.volumes, 1, "volumes", `${typeLabel} volumes`),
      metricViolation(typedQuota.gigabytes, requestedGiB, "gigabytes", `${typeLabel} storage`, formatGiB),
    ].filter((item): item is QuotaViolation => !!item)
    violations.push(...typedViolations)
  }

  const perVolume = quota.storage.perVolumeGigabytes
  if (perVolume && perVolume.limit >= 0 && requestedGiB > perVolume.limit) {
    violations.push({
      resource: "per-volume",
      message: `Volume size: ${requestedGiB} GiB requested exceeds the ${perVolume.limit} GiB per-volume limit.`,
    })
  }
  return violations
}
