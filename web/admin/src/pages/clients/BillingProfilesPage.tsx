import { useMemo } from "react"
import { Link, useNavigate } from "react-router-dom"
import type { Column, ColumnDef } from "@tanstack/react-table"
import { RefreshCw, Wallet } from "lucide-react"
import { PageHeader } from "@/components/layout/PageHeader"
import { DataTable, sortableHeader } from "@/components/data-table"
import { EmptyState } from "@/components/empty-state"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { useAdminList } from "@/lib/hooks"
import { fmtMoney, timeAgo } from "@/lib/format"

// GET /admin/billing-profile — shaped profile doc + computed financials
// (balance/accountCredit/promotionalCredit/currentMonth/lastMonth/forecastedMonthEnd as JSON numbers).
type BpRow = Record<string, any>

export function profileName(p: BpRow): string {
  const full = [p.firstName, p.lastName].filter(Boolean).join(" ")
  return (p.fullName as string) || full || (p.companyName as string) || (p.email as string) || (p.id as string)
}

/** Right-aligned variant of sortableHeader for numeric (amount) columns. */
function sortableRightHeader<TData>(label: string) {
  const Inner = sortableHeader<TData>(label)
  return function SortableRightHeader({ column }: { column: Column<TData, unknown> }) {
    return (
      <div className="text-right">
        <Inner column={column} />
      </div>
    )
  }
}

export default function BillingProfilesPage() {
  const navigate = useNavigate()
  const { data, isLoading, isError, error, refetch, isFetching } = useAdminList<BpRow>("/admin/billing-profile")
  const rows = data?.data ?? []

  const columns = useMemo<ColumnDef<BpRow, any>[]>(
    () => [
      {
        id: "client",
        accessorFn: (p) => `${profileName(p)} ${p.email ?? ""}`,
        header: sortableHeader("Client"),
        cell: ({ row }) => (
          <div>
            <Link
              to={`/clients/billing-profiles/${row.original.id}`}
              className="inline-block py-1 font-medium hover:underline"
              onClick={(e) => e.stopPropagation()}
            >
              {profileName(row.original)}
            </Link>
            <p className="text-xs text-muted-foreground">{row.original.email ?? "—"}</p>
          </div>
        ),
      },
      {
        id: "status",
        accessorFn: (p) => p.status ?? "",
        header: sortableHeader("Status"),
        cell: ({ getValue }) => <StatusBadge status={getValue()} />,
      },
      {
        id: "currency",
        accessorFn: (p) => p.currency ?? "",
        header: "Currency",
        cell: ({ getValue }) => <span className="text-sm text-muted-foreground">{getValue() || "—"}</span>,
      },
      {
        id: "balance",
        accessorFn: (p) => Number(p.balance ?? 0),
        header: sortableRightHeader("Balance"),
        cell: ({ row }) => (
          <div className="text-right font-mono text-sm tabular-nums">
            {fmtMoney(row.original.balance, row.original.currency)}
          </div>
        ),
      },
      {
        id: "currentMonth",
        accessorFn: (p) => Number(p.currentMonth ?? 0),
        header: sortableRightHeader("This month"),
        cell: ({ row }) => (
          <div className="text-right font-mono text-sm tabular-nums">
            {fmtMoney(row.original.currentMonth, row.original.currency)}
          </div>
        ),
      },
      {
        id: "created",
        accessorFn: (p) => p.createdAt ?? "",
        header: sortableHeader("Created"),
        cell: ({ getValue }) => <span className="text-sm text-muted-foreground">{timeAgo(getValue())}</span>,
      },
    ],
    [],
  )

  return (
    <>
      <PageHeader
        title="Billing profiles"
        eyebrow="Clients"
        description="Customer billing profiles with live balances."
        actions={
          <Button
            variant="outline"
            size="sm"
            onClick={() => void refetch()}
            disabled={isFetching}
            aria-label="Refresh billing profiles"
          >
            <RefreshCw className={isFetching ? "size-4 animate-spin" : "size-4"} />
          </Button>
        }
      />

      {!isLoading && !isError && !rows.length ? (
        <EmptyState icon={Wallet} title="No billing profiles" hint="Profiles appear here once clients sign up." />
      ) : (
        <DataTable
          columns={columns}
          data={rows}
          isLoading={isLoading}
          error={isError ? (error as Error) : null}
          searchPlaceholder="Search billing profiles…"
          onRowClick={(p) => navigate(`/clients/billing-profiles/${p.id}`)}
          getRowId={(p) => p.id}
          pageSize={25}
        />
      )}
    </>
  )
}
