# Sandbox — implementation plan

A proposal to add **code sandboxes** to Stratos as a sellable SKU: fast-booting,
isolated Linux VMs that customers create through an API or SDK, run untrusted
code in, and are billed for by the second. The reference product is
[E2B](https://e2b.dev).

> **Status: PROPOSAL.** Nothing here is built. This document exists to be
> reviewed and argued with before any code is written. Effort figures are
> estimates, not commitments. Decisions marked **OPEN** still need an answer.

**Contents**

- [1. Why this shape](#1-why-this-shape)
- [2. Decisions already taken](#2-decisions-already-taken)
- [3. What Stratos already provides](#3-what-stratos-already-provides)
- [4. Architecture](#4-architecture)
- [5. Data model](#5-data-model)
- [6. API surface](#6-api-surface)
- [7. Billing](#7-billing)
- [8. Phases](#8-phases)
- [9. Risks](#9-risks)
- [10. Open questions](#10-open-questions)
- [11. Prior art](#11-prior-art)

---

## 1. Why this shape

A sandbox product is two halves:

1. **A fast-boot execution runtime** — create an isolated VM in under a second,
   run code in it, snapshot it, throw it away.
2. **Everything around the runtime** — identity, organizations, projects, RBAC,
   API keys, quota, metered rating, invoicing, a console, an agent-facing API.

Stratos already has the second half in production-grade Go. It has none of the
first. So the project is not "build an E2B" — it is **build the runtime plane and
let Stratos be the commerce plane.**

That framing is what keeps the scope finite. Every feature below either lives in
the new runtime, or reuses a Stratos subsystem that already exists.

---

## 2. Decisions already taken

| # | Decision | Rationale |
|---|---|---|
| 1 | **It is a product**, sold like a server SKU | Implies public SDK, CLI, docs, and a rate card — not an internal-only capability |
| 2 | **Firecracker microVMs**, not containers | See below |
| 3 | **CPU and RAM only**, no GPU | See below |
| 4 | **Stratos-native Go**, written and owned in-house | No forked third-party control plane |
| 5 | **Per-second precision, per-minute accrual** | See [§7](#7-billing) |
| 6 | **Stratos `pgdoc` store**, no separate database | One operational surface |
| 7 | **Wildcard-domain access** for per-port preview URLs | See [§9](#9-risks) for the domain-choice caveat |
| 8 | **No Kubernetes** | See below |

### On isolation (decision 2)

The product runs arbitrary untrusted code from paying strangers. That rules out
shared-kernel isolation.

| | Containers (runc) | gVisor | **Firecracker** |
|---|---|---|---|
| Kernel | shared with host | shared, syscalls intercepted in user space | **own guest kernel, KVM-backed** |
| Consequence of an escape | full host and every co-tenant | reduced, but host-kernel surface remains | requires a KVM or VMM break |
| Boot | ~50 ms | ~150 ms | ~125 ms cold, ~50 ms snapshot restore |
| Deployed at scale by | — | GKE Sandbox | **AWS Lambda, AWS Fargate** |

### On GPUs (decision 3)

Ruling GPUs out is what makes Firecracker viable: **Firecracker has no PCIe
passthrough.** A GPU requirement would have forced Cloud Hypervisor or Kata
Containers instead. CPU/RAM-only workloads are Firecracker's design center.

If GPU sandboxes are ever wanted, that is a second runtime, not a parameter on
this one. Decide it now or accept the rewrite later.

### On Kubernetes (decision 8)

Firecracker needs `/dev/kvm` on bare metal or a nested-virtualization-capable
host. A Kubernetes scheduler would duplicate and then fight the placement logic
the control plane has to own anyway, and it adds a large operational surface for
no benefit. The node agent ships as a standalone binary under systemd —
the same pattern as the existing [`cmd/notifier`](../cmd/notifier/main.go).

---

## 3. What Stratos already provides

Verified against the tree at the time of writing. This table is the argument for
why the non-runtime half is mostly done.

| Capability the product needs | Status | Where |
|---|---|---|
| Per-minute metered rating | ✅ `TimeUnitMinute` and a minutely charge cron both exist | `internal/platform/pricing/bill.go:29`, `internal/platform/scheduler/scheduler.go` (`MinutelyChargeSpec`) |
| Pluggable billable resource types | ✅ `Provider` interface (`Type()` + `GetBillingInformation`) with a type catalog — additive | `internal/cloud/billingresource/{billingresource,catalog}.go` |
| Free-form resource documents | ✅ 40 type constants, untyped `data map[string]any` | `internal/cloud/resource.go:14` |
| API keys for SDK auth | ✅ `Authorization: Bearer <pk>.<sk>` over `hmac_keys` | `pkg/auth/clientkey.go`, `docs/auth.md` |
| Short-lived token + WebSocket proxy | ✅ already built for noVNC and serial console — the exact pattern a PTY needs | `internal/platform/project/cloud_writes.go:2101` (`emitProxiedConsole`) |
| Per-project quota enforcement | ✅ GPU quota is the template to copy | `internal/platform/project/gpuquota.go` |
| Document store | ✅ `pgdoc.Store` — collections, indexes, optimistic update, `FindOneForUpdate` | `internal/pgdoc/store.go` |
| Object storage for artifacts | ✅ Ceph RGW: buckets, per-key access, policies, rotation | `internal/cloud/client/objectstore_ceph*.go` |
| Secret encryption at rest | ✅ `textcrypt.EncryptObject` walk | `pkg/textcrypt` |
| Agent-facing tool surface | ✅ MCP with realm-split client/admin toolsets | `internal/platform/mcp/tools_client.go` |
| Catalog / SKU concept | ✅ flavor categories, image groups | `internal/platform/catalog/` |
| Standalone node-agent precedent | ✅ second binary, env-configured, systemd-deployed | `cmd/notifier/main.go` |
| Live event streaming | ⚠️ works single-pod; **cross-pod fan-out is a known deferred TODO** (`LocalPublisher`, intended transport `sse_topic_streaming`) | `internal/platform/sse/sse.go` |
| External usage ingestion (SigV4) | ⚠️ scaffolded, not wired — admin can register a provider, but the `/test/**` probes return 501 and the charge job reads only the cloud cache | `pkg/billingapi/`, `internal/platform/billingclient/`, `internal/platform/admin/extresourceprovider.go`, `internal/platform/billingjob/billingjob.go:246` |
| Rate limiting | ❌ absent | — |

**Gaps that this project must fill:** sub-second boot, memory-snapshot
pause/resume, a template build pipeline, per-port preview URLs, SDKs and CLI,
and rate limiting.

---

## 4. Architecture

```
SDK / CLI ──── Bearer pk.sk ────┐
Console ─────── OIDC ───────────┤
MCP agent ──────────────────────┘
                                ▼
                    stratos-api
                      └─ internal/platform/sandbox        (NEW: control plane)
                           placement · lifecycle · quota · usage ledger
                                │
                                ├── gRPC ──> cmd/sandboxd  (NEW: node agent, one per host)
                                │              └─ Firecracker + jailer
                                │                 TAP/NAT · overlay disks
                                │                    └── vsock ──> guest-agent (NEW, in-VM)
                                │                                    exec · PTY · filesystem
                                │
                                └── Ceph RGW: rootfs templates, memory snapshots

*.sbx.<separate-domain> ──> sandbox-proxy (NEW) ──> node : tap-ip : port
```

### Components

| Component | Runs where | Responsibility |
|---|---|---|
| `internal/platform/sandbox` | inside `stratos-api` | Placement, lifecycle state machine, quota, usage ledger, HTTP handlers, MCP tools |
| `cmd/sandboxd` | each sandbox host | Firecracker VMM processes, jailer, rootfs overlays, snapshot to/from Ceph, TAP networking, capacity heartbeat |
| `guest-agent` | inside each microVM | exec, PTY, filesystem operations, port detection, health — over vsock |
| `sandbox-proxy` | ingress tier | Wildcard host routing to the owning node's TAP IP; also carries the PTY WebSocket |

### Lifecycle

```
CREATING ──> RUNNING ──> PAUSING ──> PAUSED ──> RESUMING ──> RUNNING
                │                       │
                └──────> KILLED <───────┘

  plus terminal: TIMED_OUT, FAILED
```

**Pause writes a Firecracker memory snapshot plus a disk diff to Ceph and stops
billing.** That is the commercial primitive, not merely a convenience — it is
what lets a customer keep a warm workspace without paying for idle compute.

### Reaching sub-200ms

Cold-booting a kernel per request will not get there. Two mechanisms combine:

1. **Snapshot restore** rather than cold boot (~50 ms vs ~125 ms).
2. **A warm pool** of pre-booted VMs per popular template.

Warm pools cost money while idle. That cost belongs in the rate card
([§7](#7-billing)) or the margin disappears at low utilization.

---

## 5. Data model

Five new `pgdoc` collections. Field lists are indicative, not final.

| Collection | Key fields |
|---|---|
| `sandbox` | `projectId`, `userId`, `templateId`, `state`, `vcpus`, `ramMb`, `diskGb`, `nodeId`, `vmIp`, `timeoutAt`, `envVars` (textcrypt), `metadata`, lifecycle timestamps |
| `sandboxTemplate` | `projectId` (null = public), `name`, `baseImage`, `buildStatus`, `rootfsRef`, `kernelRef`, `memSnapshotRef`, `sizeBytes` |
| `sandboxNode` | `region`, `addr`, `capacity{vcpus,ramMb,diskGb}`, `allocated{…}`, `state`, `lastHeartbeat` |
| `sandboxUsage` | `sandboxId`, `projectId`, `intervalStart`, `vcpuSeconds`, `ramGbSeconds`, `snapshotGbHours`, `consumed` |
| `sandboxEvent` | `sandboxId`, `type`, `at`, `detail` — lifecycle and audit trail |

`sandboxUsage` is the ledger the charge job drains; `consumed` makes the drain
idempotent under retry.

---

## 6. API surface

Mounted under the existing `/api/v1` project scope
(`internal/server/server.go:113`), so RBAC, org membership, and project
resolution all come for free.

```
POST   /api/v1/projects/{projectId}/sandboxes                  create
GET    /api/v1/projects/{projectId}/sandboxes                  list
GET    /api/v1/projects/{projectId}/sandboxes/{id}             get
DELETE /api/v1/projects/{projectId}/sandboxes/{id}             kill
POST   /api/v1/projects/{projectId}/sandboxes/{id}/pause
POST   /api/v1/projects/{projectId}/sandboxes/{id}/resume
POST   /api/v1/projects/{projectId}/sandboxes/{id}/timeout     extend TTL
POST   /api/v1/projects/{projectId}/sandboxes/{id}/exec        streamed
GET    /api/v1/projects/{projectId}/sandboxes/{id}/files       read / list
PUT    /api/v1/projects/{projectId}/sandboxes/{id}/files       write / upload
DELETE /api/v1/projects/{projectId}/sandboxes/{id}/files
GET    /api/v1/projects/{projectId}/sandboxes/{id}/pty         WebSocket
GET    /api/v1/projects/{projectId}/sandboxes/{id}/metrics
POST   /api/v1/projects/{projectId}/sandbox-templates          build
GET    /api/v1/projects/{projectId}/sandbox-templates
```

**Auth.** SDK and CLI use the existing API-key scheme
(`Authorization: Bearer <pk>.<sk>`, `pkg/auth/clientkey.go`); the console uses
the normal OIDC bearer path. No new auth mechanism.

**MCP tools.** `create_sandbox`, `run_code`, `read_file`, `write_file`,
`kill_sandbox`, registered in `internal/platform/mcp/tools_client.go` alongside
the existing client toolset.

**SKUs.** Sandbox sizes as catalog entries mirroring flavor categories:

| SKU | vCPU | RAM |
|---|---|---|
| `sbx.small` | 1 | 2 GB |
| `sbx.medium` | 2 | 4 GB |
| `sbx.large` | 4 | 8 GB |
| `sbx.xlarge` | 8 | 16 GB |

---

## 7. Billing

### Where the usage enters the charge path

**Do not write sandboxes into the `cloudResource` table.**
`billingresource.GetBillingResources` reads from
`cloud.Repo.FindByProjectAndService` — the OpenStack mirror. `syncjob` notes that
its "delete-of-vanished" dispatch is a later slice; a `SANDBOX` row survives
there today but becomes a silent deletion hazard the moment that slice lands.

Instead, add a **second resolution source** in `billingjob.billingResources()`
([`internal/platform/billingjob/billingjob.go:246`](../internal/platform/billingjob/billingjob.go))
alongside the cloud path. The same seam later serves any other non-OpenStack
metered product.

### Resource types

Added to `billingresource.Catalog()`:

```
resourceType: "sandbox"
  vcpu_seconds     number   isUsage=true    delta for the interval
  ram_gb_seconds   number   isUsage=true    delta for the interval
  template_id      string   (filter)
  region           string   (auto-stamped by stampResourceValues)

resourceType: "sandbox_storage"
  snapshot_gb      number                   level, billed hourly
```

### Granularity: per-second precision, per-minute accrual

The node agent keeps a second-accurate ledger. Every minute it emits the
**delta** since the last interval; the existing minutely charge cron
(`MinutelyChargeSpec = "30 * * * * *"`) rates it. No new cadence, no engine
change.

> ⚠️ **Emit deltas, never cumulative totals.** `is_usage` attributes are re-rated
> from their new value each interval — they are *not* accumulated by the engine.
> Reporting a running total double-bills every customer, compounding. Cover this
> with a golden test before the first sandbox is charged.

True per-second *charging* is not worth it: 60× the charge-job load and a bill
item per second, for no product difference. E2B itself meters per-second and
aggregates. Display `$/hr` in the console; accrue per minute.

### Rate card proposal

Anchors: the existing Stratos VM rate is $0.008/vCPU-hr + $0.004/GB-hr
([`docs/pricing-rate-card.md`](pricing-rate-card.md)). E2B's published rate is
$0.0504/vCPU-hr + $0.0162/GiB-hr.

| Component | Proposed | Note |
|---|---|---|
| vCPU | **$0.012 / vCPU-hr** | 1.5× the VM rate — covers orchestration and warm-pool idle |
| RAM | **$0.006 / GB-hr** | same multiple |
| Snapshot storage | **$0.10 / GB-month** | reuses the existing volume rate |
| Egress | existing tiers | 1 TiB free, then $0.01/GB |

A 2 vCPU / 4 GB sandbox lands at **$0.048/hr** against E2B's ~$0.166/hr — roughly
a 3.5× undercut while still carrying a premium over a plain VM.

These are a starting point for the pricing conversation, not a recommendation to
launch at. Validate against real warm-pool utilization before publishing.

---

## 8. Phases

Each phase ends on something demonstrable. **Phase 3 is the first one that can be
invoiced.**

| # | Scope | Estimate |
|---|---|---|
| **0** | **Hardware spike + ADR.** Verify nested KVM on the region (`grep -c vmx /proc/cpuinfo` inside a Nova guest) or allocate Ironic nodes. Write a Go program that boots a Firecracker VM, snapshots it, restores it — measured on the real metal. Read `e2b-dev/infra` for solved problems worth lifting. | 2–3 wks |
| **1** | `sandboxd` + `guest-agent`. Cold boot, exec, filesystem, kill, the vsock protocol, TAP/NAT. CLI-driven; no HTTP API yet. | 6–8 wks |
| **2** | Control plane. Five collections, placement, state machine, `/api/v1/projects/{id}/sandboxes/**`, API-key auth, per-project quota (copy `gpuquota.go`). **First working product.** | 4–6 wks |
| **3** | Billing, console page, SKUs, rate card. **First sellable milestone.** | 4–5 wks |
| **4** | Snapshots, pause/resume, warm pool → sub-200 ms starts and pause-stops-billing. | 6–8 wks |
| **5** | Wildcard-domain proxy, per-port preview URLs, PTY WebSocket, log streaming. **Requires fixing the SSE cross-pod fan-out first.** | 4–6 wks |
| **6** | Template build pipeline: Dockerfile → ext4 rootfs → Ceph registry. | 6–8 wks |
| **7** | Python and TypeScript SDKs, CLI, MCP tools, product docs. | 4–6 wks |
| **8** | Hardening: egress policy, rate limiting, abuse detection, seccomp and jailer audit. | ongoing |

**Total: roughly 9–13 months solo, 6–8 months with two engineers.** That is the
honest cost of owning a VMM orchestrator. It is the right call if sandboxes
become a real SKU line, but it should be a staffed project rather than a side
quest.

**On reading E2B.** `e2b-dev/infra` is Apache-2.0, which is one-way compatible
into AGPL-3.0. Their vsock protocol, snapshot handling, and warm-pool logic are
solved problems worth studying rather than rediscovering. Budget reading time in
Phase 0. Any code actually lifted must carry attribution and be recorded in the
Phase 0 ADR.

---

## 9. Risks

Ordered by when they will hurt.

1. **Nested virtualization is the gating unknown.** No `/dev/kvm` inside guests
   means Ironic bare metal, which changes cost, capacity planning, and the
   deployment story. **Resolve in week one** — everything downstream depends on
   it.

2. **Put preview URLs on a separate apex domain**, not `*.sbx.<console-domain>`.
   The platform will be serving arbitrary customer-controlled content; cookie
   scoping, CORS, and phishing all argue for the split that
   `googleusercontent.com` exists to provide. Wildcard TLS via cert-manager
   DNS-01.

3. **Egress abuse is a certainty, not a risk.** Mining, spam, and port scanning
   arrive with the first public credentials. Egress rate limits, destination
   filtering, and CPU anomaly detection belong in **Phase 3**, not Phase 8.

4. **Stratos has no rate limiting at all.** Sandbox creation is expensive and
   trivially abusable, so this has to be built rather than configured.

5. **Never reuse a rootfs overlay across tenants**, and run every VM under the
   Firecracker `jailer` — chroot, cgroups, seccomp, network namespace.

6. **Warm pools burn money while idle.** Model it in the rate card or the margin
   evaporates at low utilization.

7. **The SSE cross-pod fan-out gap blocks Phase 5.** It is a known, deliberately
   deferred TODO with the `Publisher` seam already in place — but it must be
   closed before log streaming works at more than one API replica.

---

## 10. Open questions

**OPEN** — needs an answer before the phase named.

| # | Question | Blocks |
|---|---|---|
| 1 | Does the region support nested KVM, or must sandbox hosts be Ironic bare metal? | Phase 0 |
| 2 | How many sandbox hosts, at what size, for the initial capacity target? | Phase 0 |
| 3 | Which apex domain for preview URLs, and which DNS provider for DNS-01? | Phase 5 |
| 4 | Default and maximum sandbox TTL? E2B's Pro tier caps sessions at 24 h. | Phase 2 |
| 5 | Concurrency limits per project and per organization — and are they a quota or a plan entitlement? | Phase 2 |
| 6 | Is there a free tier? E2B gives 100 hours/month and 10 GiB storage. | Phase 3 |
| 7 | Do sandboxes get unrestricted internet egress, or an allowlist by default? | Phase 3 |
| 8 | Is GPU support genuinely out of scope permanently, or deferred? A deferred yes means Firecracker is the wrong runtime. | Phase 0 |
| 9 | Which SDK ships first, Python or TypeScript? | Phase 7 |

---

## 11. Prior art

Surveyed while writing this plan.

| Project | Isolation | Boot | License | Relevance |
|---|---|---|---|---|
| [E2B `infra`](https://github.com/e2b-dev/infra) | Firecracker | <200 ms | Apache-2.0 | The reference product. Readable and liftable. Its IaC assumes GCP/AWS plus a Cloudflare-managed domain. |
| [Daytona](https://github.com/daytonaio/daytona) | OCI containers, optional microVM | <90 ms | AGPL-3.0 | Same license as Stratos. Faster to adopt, weaker default isolation. |
| [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) | Kata or gVisor via RuntimeClass | ~1–3 s | Apache-2.0 | Sandbox CRDs, launched KubeCon Nov 2025. Rejected with decision 8. |
| [Firecracker](https://github.com/firecracker-microvm/firecracker) | — | 125 ms | Apache-2.0 | **The chosen VMM.** |
| [gVisor](https://github.com/google/gvisor) | application kernel | ~150 ms | Apache-2.0 | Considered and rejected — shared host kernel. |
| [OpenStack Zun](https://github.com/openstack/zun) | runc or Kata | seconds | Apache-2.0 | OpenStack-native containers with CRI and Placement support, but low upstream activity. |
| Nova VMs | full VM | 30–60 s | — | Far too slow for this UX. Right for a dev box, wrong for a sandbox. |
