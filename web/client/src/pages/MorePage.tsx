import { useParams } from "react-router-dom"
import { PageHeader } from "@/components/layout/PageHeader"
import { useAuth } from "@/lib/auth"
import { useBillingSummary, useProjectId, useUIMenu } from "@/lib/hooks"

// Renders an admin-configured Custom Menu item ("More" section). IFRAME
// renderMode embeds the (variable-substituted) URL full-page; other modes get
// a link. Substituted tokens match the old client exactly: {{project.id}},
// {{billingProfile.id}}, {{billingProfile.email}}, {{user.email}}.
export function substituteUrlVariables(
  url: string,
  vars: { projectId: string; billingProfileId?: string; billingProfileEmail?: string; userEmail?: string },
): string {
  return url
    .replaceAll("{{project.id}}", vars.projectId)
    .replaceAll("{{billingProfile.id}}", vars.billingProfileId ?? "")
    .replaceAll("{{billingProfile.email}}", encodeURIComponent(vars.billingProfileEmail ?? ""))
    .replaceAll("{{user.email}}", encodeURIComponent(vars.userEmail ?? ""))
}

export default function MorePage() {
  const pid = useProjectId()
  const { slug = "" } = useParams()
  const auth = useAuth()
  const { data: init } = useUIMenu(pid)
  const { data: summary } = useBillingSummary(pid)

  const item = init?.menu?.items?.[slug] as
    | { displayName?: string; url?: string; renderMode?: string }
    | undefined

  if (!item?.url) {
    return (
      <>
        <PageHeader title="Not available" description="This menu item is not configured." />
      </>
    )
  }

  const url = substituteUrlVariables(item.url, {
    projectId: pid,
    billingProfileId: summary?.id as string | undefined,
    billingProfileEmail: (summary?.email as string | undefined) ?? undefined,
    userEmail: auth.user?.profile.email,
  })

  // The URL is operator-configured, but only ever render web URLs — never javascript: etc.
  const lower = url.toLowerCase()
  if (!lower.startsWith("http://") && !lower.startsWith("https://")) {
    return (
      <>
        <PageHeader title="Not available" description="This menu item is not configured." />
      </>
    )
  }

  if ((item.renderMode ?? "IFRAME").toUpperCase() !== "IFRAME") {
    return (
      <>
        <PageHeader title={item.displayName ?? slug} />
        <a className="text-primary underline" href={url} target="_blank" rel="noreferrer">
          Open {item.displayName ?? "link"} in a new tab
        </a>
      </>
    )
  }

  return (
    <div className="-mx-6 -my-6 h-[calc(100vh-3.5rem)]">
      <iframe title={item.displayName ?? slug} src={url} className="size-full border-0" />
    </div>
  )
}
