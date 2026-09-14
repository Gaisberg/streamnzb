import React, { useMemo } from "react"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { SortableList, SortableRow } from "@/components/SortableList"
import { uniquePreserveOrder } from "@/lib/lists"
import { Plus } from "lucide-react"

// SelectionSection is the shared "ordered subset of a fixed set" control: a
// titled box with an add-dropdown of what is not picked yet, a drag-reorder
// list where position means priority, and a per-row Remove.
//
// It started as a local component on the Streams page and is now also the
// metadata source priority list, so the differences between the two are props
// rather than a second copy: `renderLabel` for a display name that is not the
// raw value, and `minSelected` for a list that may never be emptied.
export function SelectionSection({
  title,
  values,
  selected,
  onToggle,
  onMove,
  error,
  helperText = '',
  membershipLocked = false,
  renderRowExtra = null,
  dimmedValues = [],
  // renderLabel maps a value to what the user reads; the default shows the
  // value itself, which is what the Streams page wants.
  renderLabel = (value) => value,
  // addLabel overrides the add button's tooltip when "Add <title>" does not
  // read well for the list in question.
  addLabel = '',
  // minSelected keeps a list from shrinking past what the backend accepts. The
  // remove control on the last rows goes disabled rather than disappearing, so
  // the reason stays visible.
  minSelected = 0,
  minSelectedReason = '',
}) {
  const selectedValues = useMemo(
    () => uniquePreserveOrder(selected).filter((value) => values.includes(value)),
    [selected, values]
  )
  const availableValues = useMemo(
    () => values.filter((value) => !selectedValues.includes(value)),
    [values, selectedValues]
  )
  const removeLocked = selectedValues.length <= minSelected
  const addText = addLabel || `Add ${String(title).toLowerCase()}`

  return (
    // A nested provider keeps the box self-contained: pages that host no other
    // tooltip do not have to wrap it, and the delay matches every other one.
    <TooltipProvider delayDuration={100}>
      <div className={`space-y-3 rounded-md border p-3 ${error ? 'border-destructive/60 bg-destructive/5' : 'border-border/60'}`}>
        <div className="flex items-center justify-between gap-3">
          <Label className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{title}</Label>
          {!membershipLocked && (
            <DropdownMenu>
              <Tooltip>
                <TooltipTrigger asChild>
                  <DropdownMenuTrigger asChild>
                    <Button
                      type="button"
                      variant="destructive"
                      size="icon"
                      className="h-8 w-8"
                      aria-label={addText}
                      disabled={availableValues.length === 0}
                    >
                      <Plus className="h-4 w-4" />
                    </Button>
                  </DropdownMenuTrigger>
                </TooltipTrigger>
                <TooltipContent>{availableValues.length === 0 ? 'No more entries to add' : addText}</TooltipContent>
              </Tooltip>
              <DropdownMenuContent align="end" className="max-h-80 w-60 overflow-y-auto">
                {availableValues.length === 0 ? (
                  <DropdownMenuItem disabled>No more entries available</DropdownMenuItem>
                ) : (
                  availableValues.map((value) => (
                    <DropdownMenuItem key={value} onClick={() => onToggle(value, true)}>
                      {renderLabel(value)}
                    </DropdownMenuItem>
                  ))
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>
        {helperText ? <p className="text-sm text-muted-foreground">{helperText}</p> : null}

        <div className="space-y-2">
          {selectedValues.length === 0 ? (
            <div className={`rounded-md border border-dashed px-3 py-3 text-sm ${error ? 'border-destructive/60 text-destructive' : 'border-border/70 text-muted-foreground'}`}>
              No entries added yet.
            </div>
          ) : (
            <SortableList ids={selectedValues} onMove={(from, to) => onMove?.(from, to)} disabled={!onMove}>
              {selectedValues.map((value) => (
                <SortableRow key={value} id={value} disabled={!onMove}>
                  <div className={`min-w-0 flex-1 text-sm font-medium ${dimmedValues.includes(value) ? 'text-muted-foreground line-through' : ''}`}>{renderLabel(value)}</div>
                  {renderRowExtra?.(value)}
                  {!membershipLocked && (
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      className="h-8 px-2 text-muted-foreground"
                      disabled={removeLocked}
                      title={removeLocked ? minSelectedReason : undefined}
                      onClick={() => onToggle(value, false)}
                    >
                      Remove
                    </Button>
                  )}
                </SortableRow>
              ))}
            </SortableList>
          )}
        </div>
        {error && <div className="text-sm text-destructive">{error}</div>}
      </div>
    </TooltipProvider>
  )
}
