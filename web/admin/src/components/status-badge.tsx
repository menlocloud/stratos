import { statusKind } from "@/lib/format"
import { cn } from "@/lib/utils"

export function StatusBadge({ status, className }: { status?: string; className?: string }) {
  const kind = statusKind(status)
  const label = (status ?? "unknown").toLowerCase().replace(/_/g, " ")
  return (
    <span className={cn("inline-flex items-center gap-1.5 text-sm", className)}>
      <span className={cn("status-dot", `status-dot-${kind}`)} />
      {/* Sentence case (Menlo convention) — neutral label carries the state;
          the dot is the redundant color signal. */}
      <span className="first-letter:uppercase">{label}</span>
    </span>
  )
}
