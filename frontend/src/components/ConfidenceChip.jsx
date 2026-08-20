import React from "react"
import { CONFIDENCE } from "@/lib/profiles"
import { cn } from "@/lib/utils"

// Colours per tier. Deliberately not the accent: these encode how much a value
// can be trusted, which is a scale, and a scale wants its own hues.
const TIER_CLASS = {
  inferred: "border-muted-foreground/40 text-muted-foreground",
  reported: "border-amber-500/50 text-amber-600 dark:text-amber-400",
  community: "border-blue-500/50 text-blue-600 dark:text-blue-400",
  measured: "border-emerald-500/50 text-emerald-600 dark:text-emerald-400",
}

// ConfidenceChip labels how far a value can be trusted: read off a release
// name, claimed by an indexer, reported by strangers, or measured in the file.
// It is the axis the filter editor is grouped by, because it is what decides
// how to configure something — and, for rules, whether they can run at all.
export function ConfidenceChip({ tier, className }) {
  const meta = CONFIDENCE[tier]
  if (!meta) return null
  return (
    <span
      title={meta.hint}
      className={cn(
        "inline-flex shrink-0 items-center rounded border px-1.5 py-0 font-mono text-[10px] leading-4",
        TIER_CLASS[tier],
        className,
      )}
    >
      {meta.label}
    </span>
  )
}

