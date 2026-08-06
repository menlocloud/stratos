// scheduling.tsx — the pod-placement half of a managed-service provider form (node selector +
// tolerations), shared by the Kamaji and DBaaS provider forms so the grammar, the validation and
// the wording can never drift between them.
//
// What it buys the operator: a dedicated node pool for the managed service. Label + taint the
// pool, set both fields, and the pool then scales on that service's demand alone — nothing else
// lands on it, and the service's pods do not drift onto general nodes. Optional everywhere: with
// both blank the chart keeps its own "schedule anywhere" default.

import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"

// A k8s label key: optional DNS-subdomain prefix + "/" + name. Deliberately loose on the
// character set (the API server is the real validator) but strict about the shape, so a pasted
// "key: value" or a stray space fails here rather than silently never matching a node.
const LABEL_KEY = /^([a-z0-9]([-a-z0-9.]*[a-z0-9])?\/)?[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$/
const LABEL_VALUE = /^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$/
const EFFECTS = ["NoSchedule", "PreferNoSchedule", "NoExecute"]

// parseNodeSelector reads the "key=value" per line textarea. null = malformed, which must block
// the save: a selector with a typo is not a weaker constraint, it is a pod that never schedules.
export function parseNodeSelector(raw: string): Record<string, string> | null {
  // Null-prototype: a label named like an Object.prototype property must land in the map instead
  // of resolving to the inherited value and vanishing from the config.
  const out: Record<string, string> = Object.create(null)
  for (const line of raw.split("\n")) {
    const t = line.trim()
    if (!t) continue
    const eq = t.indexOf("=")
    if (eq <= 0 || eq === t.length - 1) return null
    const key = t.slice(0, eq).trim()
    const value = t.slice(eq + 1).trim()
    if (!LABEL_KEY.test(key) || !LABEL_VALUE.test(value)) return null
    out[key] = value
  }
  return out
}

// parseTolerations reads the taint-style textarea and returns Kubernetes toleration objects.
// Grammar, matching the string form kubectl taint uses:
//
//	key=value:Effect  -> Equal  (tolerates that exact taint)
//	key:Effect        -> Exists (any value of that key, that effect)
//	key               -> Exists (any value, any effect)
//
// null = malformed. An unparsed line would mean a pod that cannot tolerate the pool's taint, so
// this blocks the save rather than dropping the line.
export function parseTolerations(raw: string): Record<string, string>[] | null {
  const out: Record<string, string>[] = []
  for (const line of raw.split("\n")) {
    const t = line.trim()
    if (!t) continue
    const colon = t.lastIndexOf(":")
    const head = colon < 0 ? t : t.slice(0, colon)
    const effect = colon < 0 ? "" : t.slice(colon + 1).trim()
    if (colon >= 0 && !EFFECTS.includes(effect)) return null
    const eq = head.indexOf("=")
    const key = (eq < 0 ? head : head.slice(0, eq)).trim()
    const value = eq < 0 ? "" : head.slice(eq + 1).trim()
    if (!key || !LABEL_KEY.test(key)) return null
    if (eq >= 0 && !LABEL_VALUE.test(value)) return null
    out.push({
      key,
      ...(eq >= 0 ? { operator: "Equal", value } : { operator: "Exists" }),
      ...(effect ? { effect } : {}),
    })
  }
  return out
}

// schedulingBlock renders the two fields into the config shape both providers read
// (`scheduling: {nodeSelector, tolerations}`). Always complete — an emptied textarea has to send
// an empty value, or clearing the field would leave the stored placement in place forever.
export function schedulingBlock(nodeSelector: string, tolerations: string) {
  return {
    scheduling: {
      nodeSelector: parseNodeSelector(nodeSelector) ?? {},
      tolerations: parseTolerations(tolerations) ?? [],
    },
  }
}

// schedulingValid gates the save on both textareas parsing.
export const schedulingValid = (nodeSelector: string, tolerations: string) =>
  parseNodeSelector(nodeSelector) !== null && parseTolerations(tolerations) !== null

// schedulingFromConfig turns a stored `scheduling` block back into the two textareas. Tolerations
// round-trip through the string grammar; anything richer than key/operator/value/effect (a
// tolerationSeconds, say) is shown as its closest string form and would be lost on save — which
// is why the field documents itself as the simple form.
export function schedulingFromConfig(block: Record<string, any> | undefined) {
  const selector = (block?.nodeSelector as Record<string, string>) ?? {}
  const tolerations = (block?.tolerations as Record<string, string>[]) ?? []
  return {
    nodeSelector: Object.entries(selector)
      .map(([k, v]) => `${k}=${v}`)
      .join("\n"),
    tolerations: tolerations
      .map((t) => {
        const head = t.operator === "Equal" && t.value ? `${t.key}=${t.value}` : t.key
        return t.effect ? `${head}:${t.effect}` : head
      })
      .filter(Boolean)
      .join("\n"),
  }
}

// SchedulingFields is the form half itself. `what` names the pods being placed so each provider
// can say what actually moves (control planes vs database pods).
export function SchedulingFields({
  nodeSelector,
  tolerations,
  onChange,
  what,
  idPrefix,
}: {
  nodeSelector: string
  tolerations: string
  onChange: (next: { nodeSelector: string; tolerations: string }) => void
  what: string
  idPrefix: string
}) {
  const selectorBad = parseNodeSelector(nodeSelector) === null
  const tolerationsBad = parseTolerations(tolerations) === null
  return (
    <div className="grid gap-4 rounded-lg border p-3">
      <div className="grid gap-1">
        <Label>Pod placement (optional)</Label>
        <p className="text-xs text-muted-foreground">
          Where {what} run. Set both to keep them on a dedicated node pool: the selector puts them
          there, the toleration lets them past the pool's taint. Leave blank to schedule anywhere.
        </p>
      </div>
      <div className="grid gap-2">
        <Label htmlFor={`${idPrefix}-nodeselector`}>Node selector (one “label=value” per line)</Label>
        <Textarea
          id={`${idPrefix}-nodeselector`}
          className="min-h-16 font-mono text-xs"
          value={nodeSelector}
          onChange={(e) => onChange({ nodeSelector: e.target.value, tolerations })}
          placeholder={"node-role=managed-service\ntopology.kubernetes.io/zone=az1"}
        />
        {selectorBad ? (
          <p className="text-xs text-destructive">
            Each line must be <code>label=value</code> with a valid Kubernetes label on both sides.
          </p>
        ) : null}
      </div>
      <div className="grid gap-2">
        <Label htmlFor={`${idPrefix}-tolerations`}>Tolerations (one taint per line)</Label>
        <Textarea
          id={`${idPrefix}-tolerations`}
          className="min-h-16 font-mono text-xs"
          value={tolerations}
          onChange={(e) => onChange({ nodeSelector, tolerations: e.target.value })}
          placeholder={"dedicated=managed-service:NoSchedule\nnode-role:NoSchedule"}
        />
        {tolerationsBad ? (
          <p className="text-xs text-destructive">
            Each line must be <code>key=value:Effect</code>, <code>key:Effect</code> or{" "}
            <code>key</code>, where Effect is NoSchedule, PreferNoSchedule or NoExecute.
          </p>
        ) : (
          <p className="text-xs text-muted-foreground">
            Same spelling as <code>kubectl taint</code>: <code>key=value:Effect</code> tolerates that
            exact taint, <code>key:Effect</code> any value of the key, a bare <code>key</code> any
            value and any effect.
          </p>
        )}
      </div>
    </div>
  )
}
