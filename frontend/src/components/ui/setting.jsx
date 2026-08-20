import React, { useState } from "react"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { CircleHelp } from "lucide-react"
import { cn } from "@/lib/utils"

// The settings vocabulary, shared so every page reads the same way: a label on
// the left, its control on the right, and the explanation underneath rather
// than hidden behind an icon. The Settings pages built on react-hook-form
// arrive at the same shape through FormItem/FormDescription; these are the
// plain-state equivalents for editors that do not use a form.

// Hint is for genuinely secondary detail — a caveat, an edge case. Anything a
// user needs in order to answer the question in front of them belongs in the
// row's description, where it is visible without a hover.
export function Hint({ children, side = "top" }) {
  return (
    <TooltipProvider delayDuration={200}>
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            type="button"
            className="text-muted-foreground/70 transition-colors hover:text-foreground"
            aria-label="More detail"
          >
            <CircleHelp className="h-3.5 w-3.5" />
          </button>
        </TooltipTrigger>
        <TooltipContent side={side} className="max-w-xs text-xs leading-relaxed">{children}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}

// SettingGroup is a titled band of rows. Rows separate themselves with a
// hairline, so a group needs no internal spacing of its own.
export function SettingGroup({ title, badge, description, actions, children, className }) {
  return (
    <section className={cn("space-y-3", className)}>
      {(title || actions) && (
        <div className="flex flex-wrap items-center gap-2">
          {title && (
            <h4 className="text-xs font-medium uppercase tracking-wider text-muted-foreground">{title}</h4>
          )}
          {badge}
          <div className="flex-1" />
          {actions}
        </div>
      )}
      {description && <p className="max-w-prose text-xs text-muted-foreground">{description}</p>}
      <div className="overflow-hidden rounded-lg border border-border/60 bg-card/40">{children}</div>
    </section>
  )
}

// SettingRow is one setting: label left, control right, description below.
export function SettingRow({ label, badge, hint, description, htmlFor, children, className }) {
  return (
    <div className={cn("border-b border-border/50 p-3 last:border-b-0", className)}>
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between sm:gap-4">
        <div className="flex min-w-0 items-center gap-1.5 sm:flex-1">
          <Label htmlFor={htmlFor} className="text-sm font-normal">{label}</Label>
          {badge}
          {hint && <Hint>{hint}</Hint>}
        </div>
        <div className="shrink-0">{children}</div>
      </div>
      {description && <p className="mt-2 max-w-prose text-xs text-muted-foreground">{description}</p>}
    </div>
  )
}

// SettingBlock is one setting whose control needs the full width — a token
// list, a grid, a table. Same label and description treatment as SettingRow so
// the two read as one language.
export function SettingBlock({ label, badge, hint, description, children, className }) {
  return (
    <div className={cn("space-y-2 border-b border-border/50 p-3 last:border-b-0", className)}>
      {(label || description) && (
        <div className="space-y-1">
          {label && (
            <div className="flex items-center gap-1.5">
              <Label className="text-sm font-normal">{label}</Label>
              {badge}
              {hint && <Hint>{hint}</Hint>}
            </div>
          )}
          {description && <p className="max-w-prose text-xs text-muted-foreground">{description}</p>}
        </div>
      )}
      {children}
    </div>
  )
}

// NumberField keeps what the user typed while it is not yet a number, so
// clearing the box does not commit 0 and quietly change the setting. The value
// is committed only once it parses, clamped when a range is given.
export function NumberField({ value, onCommit, min, max, step, className, ...props }) {
  const [text, setText] = useState(String(value ?? ""))
  const [editing, setEditing] = useState(false)

  const shown = editing ? text : String(value ?? "")

  const change = (raw) => {
    setText(raw)
    const parsed = Number(raw)
    if (raw.trim() === "" || !Number.isFinite(parsed)) return
    let next = parsed
    if (typeof min === "number") next = Math.max(min, next)
    if (typeof max === "number") next = Math.min(max, next)
    onCommit(next)
  }

  return (
    <Input
      type="number"
      value={shown}
      min={min}
      max={max}
      step={step}
      onFocus={() => { setText(String(value ?? "")); setEditing(true) }}
      onBlur={() => setEditing(false)}
      onChange={(e) => change(e.target.value)}
      className={cn("h-9 w-32 font-mono text-xs tabular-nums", className)}
      {...props}
    />
  )
}

// ScoreInput is the one control for a point value. It colours by sign, because
// the difference between a preference and a demotion should be readable
// without hunting for a minus.
export function ScoreInput({ value, onCommit, className, ...props }) {
  const n = Number(value) || 0
  return (
    <NumberField
      value={value ?? 0}
      onCommit={onCommit}
      step={100}
      className={cn(
        n > 0 && "text-emerald-600 dark:text-emerald-500",
        n < 0 && "text-destructive",
        className,
      )}
      {...props}
    />
  )
}

