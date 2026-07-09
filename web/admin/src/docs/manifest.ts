// Docs sidebar manifest. Each page maps to src/docs/content/<slug>.md.
// Slugs mirror the public URL: /docs/<slug>.
export type DocPage = { slug: string; title: string }
export type DocSection = { title: string; pages: DocPage[] }

export const docsTitle = "Stratos Admin Docs"

export const sections: DocSection[] = [
  {
    title: "Running the Platform",
    pages: [
      { slug: "platform-admin/overview", title: "Overview" },
      { slug: "platform-admin/account-activation", title: "Account Activation" },
    ],
  },
  {
    title: "Cloud Providers",
    pages: [
      { slug: "platform-admin/cloud/connect-a-cloud", title: "Connecting a Cloud" },
      { slug: "platform-admin/cloud/service-availability", title: "Service Availability" },
      { slug: "platform-admin/cloud/instance-metrics", title: "Instance Metrics" },
      { slug: "platform-admin/cloud/reseller-domains", title: "Reseller Domains" },
    ],
  },
  {
    title: "Billing Configuration",
    pages: [
      { slug: "platform-admin/billing/price-plans", title: "Price Plans" },
      { slug: "platform-admin/billing/resource-types", title: "Resource Types" },
      { slug: "platform-admin/billing/currency", title: "Platform Currency" },
      { slug: "platform-admin/billing/tax", title: "Tax Rates" },
      { slug: "platform-admin/billing/invoicing", title: "Invoicing" },
      { slug: "platform-admin/billing/suspension", title: "Automatic Suspension" },
      { slug: "platform-admin/billing/signup-credits", title: "Sign-up Credits" },
      { slug: "platform-admin/billing/savings-plans", title: "Savings Plans" },
    ],
  },
  {
    title: "Platform Settings",
    pages: [
      { slug: "platform-admin/settings/flavor-categories", title: "Flavor Categories" },
      { slug: "platform-admin/settings/custom-menu", title: "Custom Menu Items" },
      { slug: "platform-admin/settings/login-branding", title: "Login Branding" },
    ],
  },
  {
    title: "Admin API Reference",
    pages: [
      { slug: "reference/overview", title: "Overview" },
      { slug: "reference/authentication", title: "Authentication" },
      { slug: "reference/users", title: "Users" },
      { slug: "reference/organizations", title: "Organizations" },
      { slug: "reference/projects", title: "Projects" },
      { slug: "reference/billing-profiles", title: "Billing Profiles" },
      { slug: "reference/bills", title: "Bills" },
      { slug: "reference/account-credits", title: "Account Credits" },
      { slug: "reference/service-providers", title: "Service Providers" },
      { slug: "reference/mcp-server", title: "MCP Server" },
    ],
  },
]

export const defaultSlug = sections[0].pages[0].slug
