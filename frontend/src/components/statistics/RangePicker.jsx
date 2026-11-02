import { useMemo, useState } from 'react'
import { CalendarDays, Check, ChevronDown } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { cn } from "@/lib/utils"
import { RANGE_PRESETS, defaultDateRange, formatDateInput } from "@/lib/statistics"

// RangePicker picks the observation window for the whole page: quick presets
// as a segmented control on wide screens, and a popover with every preset plus
// a custom from/to range.
export function RangePicker({ preset, loading, onSelectPreset, onApplyCustom }) {
  const [open, setOpen] = useState(false)
  const [custom, setCustom] = useState(defaultDateRange)

  const validation = useMemo(() => {
    if (!custom.from || !custom.to) return 'Select both From and To dates.'
    const todayStr = formatDateInput(new Date())
    if (custom.from > custom.to) return 'From date must be on or before To date.'
    if (custom.from > todayStr || custom.to > todayStr) return 'Future dates are not allowed.'
    return ''
  }, [custom.from, custom.to])

  const selectPreset = (id) => {
    onSelectPreset(id)
    setOpen(false)
  }

  const label = preset === 'custom' ? 'Custom Range' : RANGE_PRESETS.find((p) => p.id === preset)?.label

  return (
    <div className="flex items-center gap-2">
      <ToggleGroup
        type="single"
        value={preset}
        onValueChange={(val) => val && selectPreset(val)}
        variant="outline"
        size="sm"
        className="hidden lg:flex flex-wrap gap-1"
      >
        {RANGE_PRESETS.map((p) => (
          <ToggleGroupItem key={p.id} value={p.id} className="h-8 px-2.5 text-xs">{p.short}</ToggleGroupItem>
        ))}
      </ToggleGroup>

      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <Button variant="outline" size="sm" className="h-8 gap-2 px-3 font-normal">
            <CalendarDays className="h-4 w-4 text-primary shrink-0" />
            <span className="text-xs font-medium sm:text-sm">{label}</span>
            <ChevronDown className="ml-1 h-3.5 w-3.5 shrink-0 opacity-50" />
          </Button>
        </PopoverTrigger>
        <PopoverContent className="w-80 p-3" align="end">
          <div className="space-y-3">
            <div className="px-1 text-xs font-semibold uppercase tracking-wider text-muted-foreground">Quick Presets</div>
            <div className="grid grid-cols-1 gap-1">
              {RANGE_PRESETS.map((p) => {
                const isSelected = preset === p.id
                return (
                  <button
                    key={p.id}
                    type="button"
                    onClick={() => selectPreset(p.id)}
                    className={cn(
                      "flex w-full items-center justify-between rounded-md px-2.5 py-1.5 text-left text-xs transition-colors",
                      isSelected ? "bg-primary/10 font-medium text-primary" : "text-foreground hover:bg-muted",
                    )}
                  >
                    <div>
                      <div className="font-medium">{p.label}</div>
                      <div className="text-[10px] text-muted-foreground">{p.description}</div>
                    </div>
                    {isSelected && <Check className="ml-2 h-3.5 w-3.5 shrink-0 text-primary" />}
                  </button>
                )
              })}
            </div>

            <div className="space-y-2 border-t pt-3">
              <div className="px-1 text-xs font-semibold uppercase tracking-wider text-muted-foreground">Custom Range</div>
              <div className="grid grid-cols-2 gap-2">
                <div>
                  <label className="mb-1 block text-[11px] font-medium text-muted-foreground">From</label>
                  <Input
                    type="date"
                    max={formatDateInput(new Date())}
                    value={custom.from}
                    onChange={(e) => setCustom((prev) => ({ ...prev, from: e.target.value }))}
                    className="h-8 px-2 text-xs"
                  />
                </div>
                <div>
                  <div className="mb-1 flex items-center justify-between">
                    <label className="text-[11px] font-medium text-muted-foreground">To</label>
                    <button
                      type="button"
                      onClick={() => setCustom((prev) => ({ ...prev, to: formatDateInput(new Date()) }))}
                      className="text-[10px] font-medium text-primary hover:underline"
                    >
                      Today
                    </button>
                  </div>
                  <Input
                    type="date"
                    max={formatDateInput(new Date())}
                    value={custom.to}
                    onChange={(e) => setCustom((prev) => ({ ...prev, to: e.target.value }))}
                    className="h-8 px-2 text-xs"
                  />
                </div>
              </div>

              {validation && <div className="px-1 text-[11px] text-destructive">{validation}</div>}

              <Button
                type="button"
                size="sm"
                className="mt-1 h-8 w-full text-xs"
                disabled={loading || Boolean(validation)}
                onClick={() => {
                  onApplyCustom(custom)
                  setOpen(false)
                }}
              >
                Apply Custom Range
              </Button>
            </div>
          </div>
        </PopoverContent>
      </Popover>
    </div>
  )
}
