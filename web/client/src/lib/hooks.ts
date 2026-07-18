import { useQuery } from "@tanstack/react-query"
import { useParams } from "react-router-dom"
import { apiFetch, apiFetchEnvelope, type CloudScope } from "./api"
import type {
  BillingSummary, CloudResource, CostInfo, GpuCapacityUsage, Location, Project, ProjectQuotaUsage,
  ProjectService,
} from "./types"

export function useProjects() {
  return useQuery({
    queryKey: ["projects"],
    queryFn: () => apiFetch<Project[]>("/project"),
  })
}

export function useProject(pid?: string) {
  const projects = useProjects()
  return { ...projects, project: projects.data?.find((p) => p.id === pid) }
}

// Current project id from the /p/:pid/* route.
export function useProjectId(): string {
  const { pid } = useParams()
  return pid ?? ""
}

// Locations (one per attached cloud service × region) — the cloud-call scope
// (x-service-id / x-region-id headers) comes from here.
export function useLocations(pid: string) {
  return useQuery({
    queryKey: ["locations", pid],
    queryFn: () => apiFetch<Location[]>(`/project/${pid}/resource-types`),
    enabled: !!pid,
  })
}

export function useCloudScope(pid: string): CloudScope | undefined {
  const { data } = useLocations(pid)
  // Generic compute/network/storage pages need an OpenStack scope. A project can
  // also attach Ceph S3 (buckets only) or Kamaji (managed Kubernetes control planes),
  // and either may appear first — skip both, mirroring the backend's resolveServiceID.
  const loc =
    data?.find((candidate) => candidate.provider !== "ceph-s3" && candidate.provider !== "kamaji") ?? data?.[0]
  if (!loc?.serviceId || !loc?.region) return undefined
  return { serviceId: loc.serviceId, region: loc.region }
}

// Live quota usage for the selected OpenStack service/region. The response can
// be partial (with warnings), so callers should render each metric defensively.
export function useProjectQuota(pid: string, scope: CloudScope | undefined) {
  return useQuery({
    queryKey: ["project-quota", pid, scope?.serviceId, scope?.region],
    queryFn: () =>
      apiFetch<ProjectQuotaUsage>(`/project/${pid}/quota-usage`, { cloud: scope }),
    enabled: !!pid && !!scope,
    staleTime: 30_000,
  })
}

// Region GPU capacity (available/total per model) — only fetched when the project has it enabled.
export function useProjectGpuCapacity(pid: string, scope: CloudScope | undefined, enabled: boolean) {
  return useQuery({
    queryKey: ["project-gpu-capacity", pid, scope?.serviceId, scope?.region],
    queryFn: () =>
      apiFetch<GpuCapacityUsage>(`/project/${pid}/gpu-capacity`, { cloud: scope }),
    enabled: enabled && !!pid && !!scope,
    staleTime: 30_000,
  })
}

// Cached cloud-resource list by type (POST /project/{pid}/resource?type=X).
export function useCloudList(pid: string, type: string, extraQuery = "") {
  const scope = useCloudScope(pid)
  return useQuery({
    queryKey: ["cloud", pid, type, extraQuery],
    queryFn: () =>
      apiFetch<CloudResource[]>(`/project/${pid}/resource?type=${type}${extraQuery}`, {
        method: "POST",
        cloud: scope,
      }),
    enabled: !!pid && !!scope,
  })
}

// External (router:external) networks the project may allocate public IPs from —
// already filtered by the project's allow-list server-side.
export type PublicNetwork = { id: string; name: string }
export function usePublicNetworks(pid: string, scope: CloudScope | undefined) {
  return useQuery({
    queryKey: ["public-networks", pid, scope?.serviceId, scope?.region],
    queryFn: () => apiFetch<PublicNetwork[]>(`/project/${pid}/public-networks`, { cloud: scope }),
    enabled: !!pid && !!scope,
  })
}

export function useCloudResource(pid: string, resourceId?: string) {
  const scope = useCloudScope(pid)
  return useQuery({
    queryKey: ["cloud-resource", pid, resourceId],
    queryFn: () => apiFetch<CloudResource>(`/project/${pid}/cloud/${resourceId}`, { cloud: scope }),
    enabled: !!pid && !!resourceId && !!scope,
  })
}

export function useCostInfo(pid: string) {
  return useQuery({
    queryKey: ["cost-info", pid],
    queryFn: () => apiFetch<CostInfo>(`/project/${pid}/cost-info`),
    enabled: !!pid,
  })
}

// Org-wide billing overview (profile aggregate + per-project breakdown) for the org billing
// dashboard. `bp` = the org's billing-profile id (from useBillingSummary().id).
export type OrgCostInfo = {
  billingProfileCostInfo?: CostInfo
  projects?: Record<string, CostInfo>
  currency?: string
}
export function useOrgCostInfo(bp?: string) {
  return useQuery({
    queryKey: ["org-cost-info", bp],
    queryFn: () => apiFetch<OrgCostInfo>(`/bill/${bp}/cost-info`),
    enabled: !!bp,
  })
}

export function useBillingSummary(pid: string) {
  return useQuery({
    queryKey: ["billing-summary", pid],
    queryFn: () => apiFetch<BillingSummary>(`/project/${pid}/billing`),
    enabled: !!pid,
  })
}

// Curated flavor categories (admin-configured "hardware" groups). The create-server flavor picker
// shows only flavors that belong to a category, grouped by it — uncategorized flavors are hidden.
export type FlavorCategory = {
  id: string
  name: string
  orderNumber?: number
  bareMetal?: boolean
  flavors?: Array<{ flavorName?: string }>
}
export function useFlavorCategories() {
  return useQuery({
    queryKey: ["flavor-categories"],
    queryFn: () => apiFetch<FlavorCategory[]>("/flavor-categories"),
  })
}

// Curated image groups + categories. Each enabled group offers named images under a category;
// the create-server image picker shows only offered images (matched to live glance images by name).
export type ImageGroup = {
  id: string
  name: string
  categoryId?: string
  enabled?: boolean
  orderNumber?: number
  images?: Array<{ name?: string; version?: string; orderNumber?: number }>
}
export type ImageGrouping = {
  imageGroups?: ImageGroup[]
  imageCategories?: Array<{ id: string; name: string }>
}
export function useImageGroups() {
  return useQuery({
    queryKey: ["image-groups"],
    queryFn: () => apiFetch<ImageGrouping>("/groups/images"),
  })
}

export function useProjectServices(pid: string) {
  return useQuery({
    queryKey: ["project-services", pid],
    queryFn: () => apiFetch<ProjectService[]>(`/project/${pid}/service`),
    enabled: !!pid,
  })
}

// UI menu gating — GET /init/{pid} returns menu.items keyed by OpenStack
// service name ({enabled}). The ADMIN toggles these per region on the cloud
// provider's Services tab; a disabled/absent service hides its client section.
export type UIMenu = { items?: Record<string, { enabled?: boolean; newMenuItem?: boolean }> }

export function useUIMenu(pid: string) {
  return useQuery({
    queryKey: ["ui-menu", pid],
    queryFn: () => apiFetch<{ id: string; menu?: UIMenu }>(`/init/${pid}`),
    enabled: !!pid,
    staleTime: 60_000,
  })
}

// Platform feature flags — GET /features (billing, search, ...).
export function useFeatures() {
  return useQuery({
    queryKey: ["features"],
    queryFn: () => apiFetch<string[]>("/features"),
    staleTime: 300_000,
  })
}

// Ensures the project is bootstrapped (keystone tenant) — fire-and-forget on entry.
export function useProjectInit(pid: string) {
  return useQuery({
    queryKey: ["project-init", pid],
    queryFn: async () => {
      await apiFetchEnvelope(`/project/${pid}/init`, { method: "POST" })
      return true
    },
    enabled: !!pid,
    staleTime: Infinity,
    retry: 1,
  })
}
