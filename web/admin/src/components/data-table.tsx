// The one table wrapper for list pages. Headless TanStack Table rendered
// through the design-system Table primitives: sorting, optional global
// search, skeleton loading under a real header, empty/error states, and
// keyboard-accessible row navigation. Detail-page mini-tables (<~15 static
// rows) should keep using the bare <Table> primitives instead.
//
// Contract for callers: `columns` MUST be referentially stable (module scope
// or useMemo) and accessors for nested cloud data use accessorFn, not
// accessorKey. All useReactTable usage stays inside this leaf component.
import { useMemo, useState } from "react"
import {
  flexRender,
  getCoreRowModel,
  getFilteredRowModel,
  getPaginationRowModel,
  getSortedRowModel,
  useReactTable,
  type Column,
  type ColumnDef,
  type SortingState,
  type Table as TanStackTable,
} from "@tanstack/react-table"
import { ArrowDown, ArrowUp, ArrowUpDown, Search } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { fmtMoney } from "@/lib/format"
import { cn } from "@/lib/utils"

const EMPTY: never[] = []

export interface DataTableProps<TData> {
  /** Must be referentially stable (module const or useMemo). */
  columns: ColumnDef<TData, any>[]
  /** Pass query.data directly; undefined is normalized to a stable []. */
  data: TData[] | undefined
  /** First-load only — background refetches keep rendered rows. */
  isLoading?: boolean
  error?: Error | null
  /** Shown (spanning all columns) when there are no rows. */
  empty?: React.ReactNode
  onRowClick?: (row: TData) => void
  getRowId?: (row: TData) => string
  /** Custom toolbar content; function form receives the table instance. */
  toolbar?: React.ReactNode | ((table: TanStackTable<TData>) => React.ReactNode)
  /** Renders a built-in search input bound to the global filter. */
  searchPlaceholder?: string
  initialSorting?: SortingState
  /** Client pagination; omit for none (lists here are small). */
  pageSize?: number
  skeletonRows?: number
  className?: string
}

export function DataTable<TData>({
  columns,
  data,
  isLoading = false,
  error = null,
  empty,
  onRowClick,
  getRowId,
  toolbar,
  searchPlaceholder,
  initialSorting = [],
  pageSize,
  skeletonRows = 5,
  className,
}: DataTableProps<TData>) {
  const [sorting, setSorting] = useState<SortingState>(initialSorting)
  const [globalFilter, setGlobalFilter] = useState("")

  const rows = data ?? (EMPTY as TData[])
  const table = useReactTable({
    data: rows,
    columns,
    state: { sorting, globalFilter },
    onSortingChange: setSorting,
    onGlobalFilterChange: setGlobalFilter,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
    ...(pageSize
      ? {
          getPaginationRowModel: getPaginationRowModel(),
          initialState: { pagination: { pageSize } },
        }
      : {}),
    globalFilterFn: "includesString",
    // Stable row identity by default: never fall back to array indexes
    // (breaks React keys + row state on refetch/reorder).
    getRowId:
      getRowId ??
      ((row: TData, index: number) => {
        const r = row as { id?: unknown; externalId?: unknown }
        return String(r.id ?? r.externalId ?? index)
      }),
  })

  const visibleColumns = table.getVisibleLeafColumns().length || columns.length
  const modelRows = table.getRowModel().rows
  const hasToolbar = Boolean(toolbar) || Boolean(searchPlaceholder)

  const ariaSort = (col: Column<TData, unknown>): React.AriaAttributes["aria-sort"] => {
    if (!col.getCanSort()) return undefined
    const dir = col.getIsSorted()
    return dir === "asc" ? "ascending" : dir === "desc" ? "descending" : "none"
  }

  return (
    <div className={cn("flex flex-col gap-3", className)}>
      {hasToolbar && (
        <div className="flex flex-wrap items-center gap-2">
          {searchPlaceholder && (
            <div className="relative w-full max-w-xs">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={globalFilter}
                onChange={(e) => setGlobalFilter(e.target.value)}
                placeholder={searchPlaceholder}
                className="pl-8"
                aria-label={searchPlaceholder}
              />
            </div>
          )}
          {typeof toolbar === "function" ? toolbar(table) : toolbar}
        </div>
      )}

      {/* A table is never naked: card surface with hairline border. */}
      <div className="overflow-hidden rounded-xl border bg-card">
      <Table>
        <TableHeader>
          {table.getHeaderGroups().map((hg) => (
            <TableRow key={hg.id} className="hover:bg-transparent">
              {hg.headers.map((header) => (
                <TableHead key={header.id} aria-sort={ariaSort(header.column)}>
                  {header.isPlaceholder
                    ? null
                    : flexRender(header.column.columnDef.header, header.getContext())}
                </TableHead>
              ))}
            </TableRow>
          ))}
        </TableHeader>
        <TableBody>
          {isLoading ? (
            Array.from({ length: skeletonRows }).map((_, i) => (
              <TableRow key={`skeleton-${i}`} className="hover:bg-transparent">
                {Array.from({ length: visibleColumns }).map((_, j) => (
                  <TableCell key={j}>
                    <Skeleton className="h-4 w-full max-w-32" />
                  </TableCell>
                ))}
              </TableRow>
            ))
          ) : error ? (
            <TableRow className="hover:bg-transparent">
              <TableCell colSpan={visibleColumns} className="py-10 text-center text-destructive">
                {error.message || "Something went wrong loading this list."}
              </TableCell>
            </TableRow>
          ) : modelRows.length === 0 ? (
            <TableRow className="hover:bg-transparent">
              <TableCell colSpan={visibleColumns} className="py-10 text-center text-muted-foreground">
                {/* An active search always explains itself; the caller's empty
                    state is for a genuinely empty list. */}
                {globalFilter ? "No results match your search." : (empty ?? "Nothing here yet.")}
              </TableCell>
            </TableRow>
          ) : (
            // Row click is a pointer convenience only: making the <tr>
            // focusable/role=button nests interactive elements (axe serious).
            // The accessible path is the in-row name link or the kebab menu,
            // which every clickable-row page provides.
            modelRows.map((row) => (
              <TableRow
                key={row.id}
                data-state={row.getIsSelected() ? "selected" : undefined}
                className={cn(onRowClick && "cursor-pointer")}
                onClick={onRowClick ? () => onRowClick(row.original) : undefined}
              >
                {row.getVisibleCells().map((cell) => (
                  <TableCell key={cell.id}>
                    {flexRender(cell.column.columnDef.cell, cell.getContext())}
                  </TableCell>
                ))}
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>
      </div>

      {pageSize && table.getPageCount() > 1 && (
        <div className="flex items-center justify-between text-sm text-muted-foreground">
          <span className="tabular-nums">
            Page {table.getState().pagination.pageIndex + 1} of {table.getPageCount()}
          </span>
          <div className="flex gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => table.previousPage()}
              disabled={!table.getCanPreviousPage()}
            >
              Previous
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => table.nextPage()}
              disabled={!table.getCanNextPage()}
            >
              Next
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

/** Sortable column header: ghost button with a direction indicator. Use as
 * `header: sortableHeader("Name")` in a column def. */
export function sortableHeader<TData>(label: string) {
  return function SortableHeader({ column }: { column: Column<TData, unknown> }) {
    const dir = column.getIsSorted()
    const Icon = dir === "asc" ? ArrowUp : dir === "desc" ? ArrowDown : ArrowUpDown
    return (
      <Button
        variant="ghost"
        size="sm"
        className="-ml-2 h-8 gap-1.5 px-2 font-medium data-[state=open]:bg-accent"
        onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}
      >
        {label}
        <Icon className={cn("size-3.5", dir ? "text-foreground" : "text-muted-foreground/70")} />
      </Button>
    )
  }
}

/** Right-aligned sortable header — numeric/money columns. */
export function sortableRightHeader<TData>(label: string) {
  const Header = sortableHeader<TData>(label)
  return function SortableRightHeader(ctx: { column: Column<TData, unknown> }) {
    return (
      <div className="flex justify-end">
        <Header {...ctx} />
      </div>
    )
  }
}

/** Right-aligned mono money cell — the one way money renders in tables. */
export function MoneyCell({ value, currency }: { value: number | null | undefined; currency: string }) {
  return <div className="text-right font-mono tabular-nums">{fmtMoney(value ?? 0, currency)}</div>
}

/** Stable hook for column defs — thin alias so pages remember the rule. */
export function useColumns<TData>(factory: () => ColumnDef<TData, any>[], deps: React.DependencyList = []) {
  // eslint-disable-next-line react-hooks/exhaustive-deps
  return useMemo(factory, deps)
}
