// Core DTO shapes from the Stratos API (optional fields may
// be absent — keep everything optional unless proven always-present).

export type Project = {
  id: string
  name: string
  status?: string
  organizationId?: string
  billingProfileId?: string
  memberships?: Array<{ sub?: string; roles?: string[] }>
  services?: Array<{ serviceId?: string; config?: Record<string, unknown> }>
  // false/absent = the client gets no external-network picker (the server auto-picks the pool for
  // floating IPs / router gateways); true = the client chooses.
  publicNetworksVisible?: boolean
}

export type Organization = {
  id: string
  name?: string
  billingProfileId?: string
  members?: Array<{ sub?: string; roles?: string[]; email?: string }>
}

export type BillingSummary = {
  id?: string
  status?: string
  currency?: string
  balance?: number
  accountCredit?: number
  promotionalCredit?: number
  currentMonthUsage?: number
  defaultCardId?: string
  hasBillingDetails?: boolean
  fullName?: string
  [k: string]: unknown
}

export type CloudResource = {
  id: string
  type: string
  name?: string
  status?: string
  externalId?: string
  projectId?: string
  createdAt?: string
  info?: { createdAt?: string; updatedAt?: string }
  data?: Record<string, any>
  [k: string]: unknown
}

export type ProjectService = {
  id: string
  name?: string
  type?: string
  status?: string
  [k: string]: unknown
}

export type Location = {
  serviceId?: string
  region?: string
  displayName?: string
  resourceTypes?: string[]
  /** "openstack" (Swift object store) | "ceph-s3" — the two object stores run side by side. */
  provider?: string
  serviceName?: string
  [k: string]: unknown
}

export type CostInfo = {
  currentMonthCosts?: number
  forecastedMonthEndCosts?: number
  currentMonthCostsByType?: Record<string, number>
  dueAmount?: number
  balance?: number
  accountCredit?: number
  promotionalCredits?: number
  topResourcePrices?: Array<{
    // Backend emits currentCost; price is a legacy alias some callers still read.
    currentCost?: number
    price?: number
    resource?: { id?: string; type?: string; name?: string; createdAt?: string; data?: Record<string, any> }
  }>
  projects?: Record<string, CostInfo>
  [k: string]: unknown
}

export type Bill = {
  id: string
  status?: string
  netAmount?: number
  grossAmount?: number
  unpaidGrossAmount?: number
  invoiceCurrency?: string
  createdAt?: string
  dueAt?: string
  items?: Array<Record<string, any>>
  [k: string]: unknown
}

export type Transaction = {
  id: string
  status?: string
  amount?: number
  grossAmount?: number
  currency?: string
  createdAt?: string
  externalInvoiceId?: string
  [k: string]: unknown
}

export type CreditCard = {
  id: string
  panMasked?: string
  tokenExpirationDate?: string
  [k: string]: unknown
}
