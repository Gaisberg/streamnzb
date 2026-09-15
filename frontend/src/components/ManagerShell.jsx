import React, { useRef } from "react"
import { Card, CardContent } from "@/components/ui/card"
import { UsageChips } from "@/components/UsageChips"
import { cn } from "@/lib/utils"

// ManagerShell is the master/detail layout the named-entity pages share: a
// selectable list on the left, the page's own editor on the right under a
// sticky header bar that keeps the title, save state and errors visible
// however deep the editor scrolls.
//
// It is presentation only. What a row means, how a draft is saved and what the
// header's actions do all belong to the caller, because those are exactly what
// the pages disagree about: profiles are an array in the config saved as a
// whole, while a stream is a login identity with its own REST lifecycle.
// ProfileManager composes this with useProfileDrafts; StreamManagement
// composes it with its own.
export function ManagerShell({
  items = [],
  selectedIndex,
  onSelect,
  listActions,
  emptyText = "Nothing here yet.",
  emptyActions,
  title,
  status,
  actions,
  error,
  children,
  footer,
}) {
  const detailRef = useRef(null)

  // Below lg the list stacks above the editor, so selecting a row changes
  // nothing on screen; bring the editor into view. Wide layouts show both
  // columns and must not jump.
  const revealEditor = () => {
    if (window.matchMedia?.("(min-width: 1024px)").matches) return
    window.requestAnimationFrame(() => detailRef.current?.scrollIntoView({ behavior: "smooth", block: "start" }))
  }

  if (items.length === 0) {
    return (
      <>
        <Card className="border border-dashed border-border bg-card">
          <CardContent className="py-10 text-center">
            <p className="text-sm text-muted-foreground">{emptyText}</p>
            <div className="mt-3 flex flex-wrap items-center justify-center gap-2">{emptyActions}</div>
          </CardContent>
        </Card>
        {footer}
      </>
    )
  }

  return (
    <div className="grid gap-6 lg:grid-cols-[280px_minmax(0,1fr)]">
      <div className="space-y-2">
        {listActions && <div className="flex gap-2">{listActions}</div>}
        {items.map((item, index) => (
          <button
            key={item.key ?? index}
            type="button"
            onClick={() => { onSelect(index); revealEditor() }}
            className={cn(
              "w-full rounded-md border px-3 py-2.5 text-left transition-colors",
              index === selectedIndex
                ? "border-primary/50 bg-primary/5"
                : "border-border hover:border-muted-foreground/40"
            )}
          >
            <div className="flex items-center gap-1.5">
              <span className="truncate text-sm font-medium">{item.name}</span>
              {item.badge}
            </div>
            {item.summary && (
              <div className="mt-0.5 truncate text-xs text-muted-foreground">{item.summary}</div>
            )}
            {item.usageLabels?.length ? (
              // Names, not keywords: "In use · Kids, default" used to blur who
              // exactly was bound.
              <UsageChips labels={item.usageLabels} className="mt-1.5" />
            ) : item.emptyUsageText ? (
              <div className="mt-1 truncate text-[11px] text-muted-foreground/70">{item.emptyUsageText}</div>
            ) : null}
          </button>
        ))}
      </div>

      {children && (
        <div ref={detailRef} className="min-w-0 scroll-mt-4 space-y-4">
          <div className="sticky top-0 z-10 rounded-lg border border-border bg-card/95 p-3 shadow-sm backdrop-blur-sm">
            <div className="flex flex-wrap items-center gap-2">
              {title}
              <div className="ml-auto flex flex-wrap items-center justify-end gap-1">
                <span className="mr-1 text-xs text-muted-foreground">{status}</span>
                {actions}
              </div>
            </div>
            {error && (
              // The full complaint, wrapping — a refused save leaves the draft
              // dirty forever, so an error nobody can read is an error nobody
              // can fix.
              <p className="mt-1.5 whitespace-pre-wrap text-xs text-destructive">{error}</p>
            )}
          </div>
          {children}
        </div>
      )}

      {footer}
    </div>
  )
}
