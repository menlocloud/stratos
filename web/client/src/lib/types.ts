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

// Live OpenStack quota values. `reserved` is capacity promised to in-flight
// operations, so consumers must include it when calculating current usage.
export type QuotaMetric = {
  used: number
  reserved: number
  // OpenStack uses a negative limit for an unlimited resource.
  limit: number
}

export type ProjectQuotaUsage = {
  serviceId: string
  region: string
  compute?: {
    instances?: QuotaMetric
    cores?: QuotaMetric
    ramMb?: QuotaMetric
  }
  storage?: {
    volumes?: QuotaMetric
    gigabytes?: QuotaMetric
    snapshots?: QuotaMetric
    perVolumeGigabytes?: QuotaMetric
    volumeTypes?: Record<string, {
      volumes?: QuotaMetric
      gigabytes?: QuotaMetric
      snapshots?: QuotaMetric
    }>
  }
  gpu: {
    limits: Record<string, number>
    usage: Record<string, number>
    // False means the cache could not provide a trustworthy usage snapshot.
    // Limits remain useful for display, but the UI must not treat missing usage as zero.
    usageAvailable: boolean
  }
  // A partial provider response still returns the metrics that were available.
  warnings: string[]
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
