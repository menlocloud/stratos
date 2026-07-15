import { CircuitBoard } from "lucide-react"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import type { GpuCapacityUsage } from "@/lib/types"

// GpuCapacityPanel shows the region's free GPUs per model, when the operator enabled it for the
// project (GET /project/{id}/gpu-capacity → {visible, capacity}). The caller already gates on
// project.gpuCapacityVisible; this also hides itself if the response says not visible.
export function GpuCapacityPanel({ data, isLoading }: { data?: GpuCapacityUsage; isLoading?: boolean }) {
  if (!isLoading && data && !data.visible) return null
  const capacity = data?.capacity ?? []
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <CircuitBoard className="size-4 text-muted-foreground" aria-hidden="true" /> GPU availability
        </CardTitle>
        <p className="text-sm text-muted-foreground">Free GPUs in this region right now, per model.</p>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <Skeleton className="h-24 w-full" />
        ) : capacity.length === 0 ? (
          <p className="text-sm text-muted-foreground">No GPU capacity is reported for this region.</p>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {capacity.map((gpu) => {
              const used = Math.max(0, gpu.total - gpu.available)
              const usedPct = gpu.total > 0 ? Math.min(100, Math.round((used / gpu.total) * 100)) : 0
              const soldOut = gpu.available <= 0
              return (
                <div key={gpu.model} className="min-w-0 rounded-xl border bg-muted/20 p-4">
                  <p className="break-words font-mono text-xs font-medium">{gpu.model}</p>
                  <p
                    className={
                      soldOut
                        ? "mt-2 font-mono text-lg font-semibold tabular-nums text-destructive"
                        : "mt-2 font-mono text-lg font-semibold tabular-nums"
                    }
                  >
                    {gpu.available}
                    <span className="ml-1 text-sm font-normal text-muted-foreground">/ {gpu.total} free</span>
                  </p>
                  <div
                    className="mt-3 h-1.5 overflow-hidden rounded-full bg-muted"
                    role="meter"
                    aria-label={`${gpu.model} GPUs in use`}
                    aria-valuemin={0}
                    aria-valuemax={gpu.total}
                    aria-valuenow={used}
                    aria-valuetext={`${gpu.available} of ${gpu.total} free`}
                  >
                    <div
                      className={soldOut ? "h-full rounded-full bg-destructive" : "h-full rounded-full bg-primary"}
                      style={{ width: `${usedPct}%` }}
                    />
                  </div>
                </div>
              )
            })}
          </div>
        )}
      </CardContent>
    </Card>
  )
}
