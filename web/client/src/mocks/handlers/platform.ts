// Projects, organizations, invites, menu/features, search, user bootstrap.
import { on } from "../router"
import { db } from "../db"
import { features, ORG_ID, organizations, uiMenu } from "../fixtures/platform"
import { myInvites } from "../fixtures/people"

on("GET /project", () => ({ data: db.projects }))
on("POST /project", ({ opts }) => {
  const body = opts.body as { name?: string; organizationId?: string }
  const project = {
    id: db.nextId("prj"),
    name: body.name ?? "new-project",
    status: "ACTIVE",
    organizationId: body.organizationId ?? ORG_ID,
    billingProfileId: "bp-menlo-1",
    memberships: [],
    services: [{ serviceId: "svc-openstack-1" }],
  }
  db.projects.push(project)
  return { data: project }
})
on("POST /project/:pid/init", () => ({ data: {} }))
on("POST /project/:pid/rename", ({ params, opts }) => {
  const p = db.projects.find((x) => x.id === params.pid)
  if (p) p.name = (opts.body as { name?: string })?.name ?? p.name
  return { data: p }
})
on("DELETE /project/:pid", ({ params }) => {
  const p = db.projects.find((x) => x.id === params.pid)
  if (p) p.status = "SCHEDULED_FOR_DELETION"
  return { data: {} }
})
on("DELETE /project/:pid/cancel", ({ params }) => {
  const p = db.projects.find((x) => x.id === params.pid)
  if (p) p.status = "ACTIVE"
  return { data: {} }
})
on("DELETE /project/:pid/now", ({ params }) => {
  db.projects = db.projects.filter((x) => x.id !== params.pid)
  return { data: {} }
})
on("PUT /project/:pid/organization", () => ({ data: {} }))
on("POST /project/:pid/billing/:bpId", () => ({ data: {} }))
on("GET /project/:pid/service", () => ({
  data: [
    { id: "svc-openstack-1", name: "Menlo Cloud", type: "CLOUD", status: "ACTIVE" },
    { id: "svc-ceph-1", name: "Menlo S3", type: "CLOUD", status: "ACTIVE" },
  ],
}))

on("GET /organizations", () => ({ data: organizations }))
on("GET /organizations/self-service", () => ({ data: { canCreateOrganization: true } }))
on("POST /organizations", ({ opts }) => ({
  data: { id: db.nextId("org"), name: (opts.body as { name?: string })?.name ?? "new-org" },
}))
on("PUT /organizations/:orgId", () => ({ data: {} }))
on("DELETE /organizations/:orgId", () => ({ data: {} }))

on("GET /project-invites/mine", () => ({ data: myInvites }))
on("GET /project-invites/:token", () => ({ data: {} }))
on("POST /project-invites/accept/:token", () => ({ data: {} }))
on("POST /project-invites/decline/:token", () => ({ data: {} }))
on("POST /project-invites/invite", () => ({ data: {} }))

on("GET /init/:pid", () => ({ data: uiMenu }))
on("GET /features", () => ({ data: features }))
on("POST /user", () => ({ data: {} }))
on("POST /user/custom-info/:key", () => ({ data: {} }))
on("DELETE /user/custom-info/:key", () => ({ data: {} }))

on("GET /search/:pid", () => {
  const servers = db.cloud
    .filter((r) => r.type === "SERVER")
    .map((r) => ({
      type: "SERVER",
      data: {
        id: r.id,
        name: r.name,
        status: r.status,
        ipv4: r.data?.server?.addresses?.["net-private"]?.[0]?.addr,
        flavor: r.data?.server?.flavor?.original_name,
        region: "RegionOne",
      },
    }))
  const projects = db.projects.map((p) => ({ type: "PROJECT", data: { id: p.id, name: p.name, status: p.status } }))
  return { data: [...servers, ...projects] }
})
