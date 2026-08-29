import React from "react"
import { cn } from "@/lib/utils"

// UsageChips is the one look for "who uses this": the streams referencing a
// profile, indexer, provider or search query, as small green chips. Renders
// nothing when nothing does — callers decide whether absence deserves a
// "Not in use" line of their own.
export function UsageChips({ labels = [], className }) {
  if (!labels.length) return null
  return (
    <div className={cn("flex flex-wrap items-center gap-1", className)}>
      {labels.map((label) => (
        <span
          key={label}
          className="max-w-full truncate rounded-full bg-emerald-500/10 px-1.5 py-px text-[10px] leading-4 text-emerald-600 dark:text-emerald-500"
        >
          {label}
        </span>
      ))}
    </div>
  )
}
