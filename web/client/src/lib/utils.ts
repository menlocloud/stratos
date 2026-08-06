import { clsx, type ClassValue } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

// Dotted-numeric version compare — "10.11" < "11.4", "9.6" < "10". Missing segments count as 0,
// and a leading "v" is ignored. Shared so the "an update is available" offers on the Kubernetes
// and Databases pages agree on what NEWER means: a plain `!==` there treats a resource that sits
// AHEAD of the provider's pin as behind it, and advertises the downgrade as an update.
export function versionGt(a: string, b: string): boolean {
  const parts = (v: string) => v.trim().replace(/^v/i, "").split(".").map((n) => Number(n) || 0)
  const as = parts(a)
  const bs = parts(b)
  for (let i = 0; i < Math.max(as.length, bs.length); i++) {
    const x = as[i] ?? 0
    const y = bs[i] ?? 0
    if (x !== y) return x > y
  }
  return false
}
