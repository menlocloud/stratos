// Platform-level seed data: projects, orgs, locations, menu, features.
import type { Location, Organization, Project } from "@/lib/types"
import { mockProfile } from "../enabled"

export const PID = "prj-aurora"
export const ORG_ID = "org-menlo"
export const BP_ID = "bp-menlo-1"

export const projects: Project[] = [
  {
    id: PID,
    name: "aurora-production",
    status: "ACTIVE",
    organizationId: ORG_ID,
    billingProfileId: BP_ID,
    memberships: [{ sub: mockProfile.sub, roles: ["OWNER"] }],
    services: [{ serviceId: "svc-openstack-1" }, { serviceId: "svc-ceph-1" }],
    publicNetworksVisible: true,
  },
  {
    id: "prj-staging",
    name: "aurora-staging",
    status: "ACTIVE",
    organizationId: ORG_ID,
    billingProfileId: BP_ID,
    memberships: [{ sub: mockProfile.sub, roles: ["OWNER"] }],
    services: [{ serviceId: "svc-openstack-1" }],
  },
  // Seeded mid-deletion so the projects list renders the scheduled-for-deletion
  // treatment (and its "Cancel deletion" action) without needing a mutation.
  {
    id: "prj-icarus",
    name: "icarus-sandbox",
    status: "SCHEDULED_FOR_DELETION",
    organizationId: ORG_ID,
    billingProfileId: BP_ID,
    memberships: [{ sub: mockProfile.sub, roles: ["OWNER"] }],
    services: [{ serviceId: "svc-openstack-1" }],
  },
]

export const organizations: Organization[] = [
  {
    id: ORG_ID,
    name: "Menlo Research",
    billingProfileId: BP_ID,
    members: [{ sub: mockProfile.sub, roles: ["OWNER"], email: mockProfile.email }],
  },
]

const ALL_TYPES = [
  "SERVER", "SERVER_GROUP", "KEYPAIR", "IMAGE", "VOLUME", "VOLUME_SNAPSHOT",
  "SHARE", "NETWORK", "SUBNET", "PORT", "ROUTER", "FLOATING_IP",
  "SECURITY_GROUP", "LOAD_BALANCER", "DNS_ZONE", "STACK", "BARBICAN_SECRET",
]

export const locations: Location[] = [
  {
    serviceId: "svc-openstack-1",
    region: "RegionOne",
    displayName: "Hanoi (RegionOne)",
    resourceTypes: [...ALL_TYPES, "BUCKET"],
    provider: "openstack",
    serviceName: "Menlo Cloud",
  },
  {
    serviceId: "svc-openstack-1",
    region: "RegionTwo",
    displayName: "Da Nang (RegionTwo)",
    resourceTypes: ALL_TYPES,
    provider: "openstack",
    serviceName: "Menlo Cloud",
  },
  {
    serviceId: "svc-ceph-1",
    region: "RegionOne",
    displayName: "Hanoi Object Storage",
    resourceTypes: ["BUCKET"],
    provider: "ceph-s3",
    serviceName: "Menlo S3",
  },
]

// Sidebar service gating — keys are OpenStack service names (AppShell.tsx).
export const uiMenu = {
  id: PID,
  menu: {
    items: {
      compute: { enabled: true },
      image: { enabled: true },
      volumev3: { enabled: true },
      "object-store": { enabled: true },
      sharev2: { enabled: true },
      network: { enabled: true },
      "load-balancer": { enabled: true },
      dns: { enabled: true },
      orchestration: { enabled: true },
      "key-manager": { enabled: true },
    },
  },
}

export const features = ["billing", "search"]

export const publicNetworks = [{ id: "net-public-ext", name: "public" }]
