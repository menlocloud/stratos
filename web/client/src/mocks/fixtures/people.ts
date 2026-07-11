// Org members, roles, audit trails, and the account profile.
import { mockProfile } from "../enabled"

export const orgMembers = [
  { sub: mockProfile.sub, firstName: "Dev", lastName: "User", email: mockProfile.email, role: "OWNER" },
  { sub: "user-ashley", firstName: "Ashley", lastName: "Tran", email: "ashley@menlo.ai", role: "ADMIN" },
  { sub: "user-james", firstName: "James", lastName: "Dam", email: "james@menlo.ai", role: "MEMBER" },
]

export const projectMembers = [
  { userId: "u-1", sub: mockProfile.sub, firstName: "Dev", lastName: "User", email: mockProfile.email, role: "OWNER" },
  { userId: "u-2", sub: "user-ashley", firstName: "Ashley", lastName: "Tran", email: "ashley@menlo.ai", role: "MEMBER" },
]

export const orgRoles = [
  { id: "role-owner", name: "OWNER", description: "Full control of the organization", permissions: ["*"], expandedPermissions: ["*"], builtIn: true },
  { id: "role-admin", name: "ADMIN", description: "Manage projects and members", permissions: ["org:manage", "project:manage"], expandedPermissions: ["org:manage", "project:manage"], builtIn: true },
  { id: "role-member", name: "MEMBER", description: "Use assigned projects", permissions: ["project:use"], expandedPermissions: ["project:use"], builtIn: true },
  { id: "role-billing", name: "billing-auditor", description: "Read-only billing access", permissions: ["billing:read"], expandedPermissions: ["billing:read"], builtIn: false },
]

export const permissionMeta = [
  { key: "org:manage", description: "Manage organization settings and members", resourceType: "ORGANIZATION" },
  { key: "project:manage", description: "Create, modify and delete projects", resourceType: "PROJECT" },
  { key: "project:use", description: "Use project cloud resources", resourceType: "PROJECT" },
  { key: "billing:read", description: "View invoices and cost data", resourceType: "BILLING" },
]

export const orgAuditEvents = [
  { id: "evt-1", timestamp: "2026-07-10T09:15:00Z", action: "server.start", resourceType: "SERVER", resourceId: "res-server-003", resourceDisplayName: "worker-01", outcome: "SUCCESS", actor: { id: mockProfile.sub, displayName: "Dev User", type: "USER" } },
  { id: "evt-2", timestamp: "2026-07-09T16:40:00Z", action: "member.invite", resourceType: "PROJECT", resourceId: "prj-aurora", resourceDisplayName: "aurora-production", outcome: "SUCCESS", actor: { id: "user-ashley", displayName: "Ashley Tran", type: "USER" } },
  { id: "evt-3", timestamp: "2026-07-08T11:05:00Z", action: "volume.create", resourceType: "VOLUME", resourceId: "res-volume-002", resourceDisplayName: "scratch-vol", outcome: "SUCCESS", actor: { id: mockProfile.sub, displayName: "Dev User", type: "USER" } },
  { id: "evt-4", timestamp: "2026-07-07T22:10:00Z", action: "server.delete", resourceType: "SERVER", resourceId: "res-server-099", resourceDisplayName: "old-worker", outcome: "FAILED", actor: { id: "user-james", displayName: "James Dam", type: "USER" } },
]

export const accountAuditEvents = orgAuditEvents.map((e) => ({
  ...e,
  actor: { displayName: e.actor.displayName, ipAddress: "203.0.113.77" },
}))

export const accountDetails = {
  id: "acct-1",
  sub: mockProfile.sub,
  createdAt: "2026-01-15T08:00:00Z",
  firstName: "Dev",
  lastName: "User",
  email: mockProfile.email,
  language: "en",
  customInfo: {},
}

export const myInvites: Array<{ token: string; projectId: string; projectName?: string; expiresAt?: string }> = []
