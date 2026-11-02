import { useMemo, useState } from 'react'
import { ArrowDown, ArrowUp, ArrowUpDown } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { cn } from "@/lib/utils"
import { sortRows } from "@/lib/statistics"

// StatTile is one headline number: the derived figure large, the raw counter
// that backs it underneath.
export function StatTile({ icon: Icon, label, value, detail, hint, className }) {
  return (
    <Card className={cn("overflow-hidden", className)} title={hint}>
      <CardContent className="flex flex-col gap-1 p-4">
        <div className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
          {Icon && <Icon className="h-3.5 w-3.5 shrink-0" />}
          <span className="truncate">{label}</span>
        </div>
        <div className="text-2xl font-semibold tabular-nums leading-tight text-foreground">{value}</div>
        {detail && <div className="truncate text-xs text-muted-foreground">{detail}</div>}
      </CardContent>
    </Card>
  )
}

export function StatGrid({ children, className }) {
  return <div className={cn("grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6", className)}>{children}</div>
}

// SortableTable renders rows by a column spec:
//   { key, label, hint?, align?: 'left' | 'right', render?(row), defaultDir?: 'asc' | 'desc', sortable? }
// Clicking a header sorts by it; clicking again flips the direction.
export function SortableTable({ columns, rows, initialSort, emptyText, actions, rowKey = (r) => r.name }) {
  const [sort, setSort] = useState(initialSort)
  const sorted = useMemo(() => (sort ? sortRows(rows, sort.key, sort.dir) : rows), [rows, sort])

  const toggle = (col) => {
    if (col.sortable === false) return
    setSort((prev) =>
      prev?.key === col.key
        ? { key: col.key, dir: prev.dir === 'asc' ? 'desc' : 'asc' }
        : { key: col.key, dir: col.defaultDir || 'desc' },
    )
  }

  return (
    <div className="overflow-x-auto rounded-md border border-border/60">
      <table className="w-full text-sm">
        <thead className="bg-muted/40 text-muted-foreground">
          <tr>
            {columns.map((col) => {
              const active = sort?.key === col.key
              const Icon = active ? (sort.dir === 'asc' ? ArrowUp : ArrowDown) : ArrowUpDown
              const right = col.align !== 'left'
              return (
                <th
                  key={col.key}
                  scope="col"
                  title={col.hint}
                  aria-sort={active ? (sort.dir === 'asc' ? 'ascending' : 'descending') : undefined}
                  className={cn("whitespace-nowrap px-3 py-2 font-medium", right ? "text-right" : "text-left")}
                >
                  {col.sortable === false ? col.label : (
                    <button
                      type="button"
                      onClick={() => toggle(col)}
                      className={cn(
                        "inline-flex items-center gap-1 rounded-sm hover:text-foreground focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-ring",
                        right && "flex-row-reverse",
                        active && "text-foreground",
                      )}
                    >
                      {col.label}
                      <Icon className={cn("h-3 w-3 shrink-0", !active && "opacity-40")} />
                    </button>
                  )}
                </th>
              )
            })}
            {actions && <th className="px-3 py-2"><span className="sr-only">Actions</span></th>}
          </tr>
        </thead>
        <tbody>
          {sorted.map((row) => (
            <tr key={rowKey(row)} className="border-t border-border/50">
              {columns.map((col) => (
                <td
                  key={col.key}
                  className={cn("px-3 py-2 tabular-nums", col.align === 'left' ? "text-left" : "text-right whitespace-nowrap")}
                >
                  {col.render ? col.render(row) : row[col.key]}
                </td>
              ))}
              {actions && <td className="px-3 py-2 text-right">{actions(row)}</td>}
            </tr>
          ))}
          {sorted.length === 0 && (
            <tr>
              <td colSpan={columns.length + (actions ? 1 : 0)} className="px-3 py-6 text-center text-muted-foreground">
                {emptyText}
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  )
}

// Muted placeholder for a cell whose rate has no samples behind it yet.
export function NoSamples() {
  return <span className="text-muted-foreground">No samples</span>
}

// MeterCell shows a percentage with a thin bar under it, for the columns
// where the share matters more than the exact figure.
export function MeterCell({ percent, label, tone = 'primary', title }) {
  const width = Math.max(0, Math.min(100, percent || 0))
  return (
    <div className="ml-auto flex w-24 flex-col items-end gap-1" title={title}>
      <span>{label}</span>
      <div className="h-1 w-full overflow-hidden rounded-full bg-muted">
        <div
          className={cn("h-full rounded-full", tone === 'warn' ? "bg-amber-500" : tone === 'bad' ? "bg-destructive" : "bg-primary")}
          style={{ width: `${width}%` }}
        />
      </div>
    </div>
  )
}

const SHARE_COLORS = [1, 2, 3, 4, 5].map((n) => `hsl(var(--chart-${n}))`)

// ShareBar splits 100% between items as one stacked bar with a legend, e.g.
// how traffic divides across providers.
export function ShareBar({ items, format }) {
  const visible = items.filter((i) => i.percent > 0)
  if (visible.length === 0) return null
  return (
    <div className="space-y-3">
      <div className="flex h-3 w-full overflow-hidden rounded-full bg-muted">
        {visible.map((item, i) => (
          <div
            key={item.name}
            title={`${item.name}: ${item.percent.toFixed(1)}%`}
            style={{ width: `${item.percent}%`, backgroundColor: SHARE_COLORS[i % SHARE_COLORS.length] }}
          />
        ))}
      </div>
      <ul className="grid gap-x-6 gap-y-1.5 text-sm sm:grid-cols-2">
        {visible.map((item, i) => (
          <li key={item.name} className="flex min-w-0 items-center gap-2">
            <span className="h-2.5 w-2.5 shrink-0 rounded-sm" style={{ backgroundColor: SHARE_COLORS[i % SHARE_COLORS.length] }} />
            <span className="min-w-0 flex-1 truncate">{item.name}</span>
            <span className="tabular-nums text-muted-foreground">{format ? format(item) : null}</span>
            <span className="w-14 text-right font-medium tabular-nums">{item.percent.toFixed(1)}%</span>
          </li>
        ))}
      </ul>
    </div>
  )
}
