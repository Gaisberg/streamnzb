import React, { useMemo, useState } from "react"
import { Badge } from "@/components/ui/badge"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Check } from "lucide-react"
import { RulesEditor } from "@/components/RulesEditor"
import { ProfilePreview } from "@/components/ProfilePreview"
import { PresetArt } from "@/components/PresetArt"
import { EMPTY_SAMPLE } from "@/lib/sample"
import { DEFAULT_PRESET, PRESETS } from "@/lib/profiles"
import { SAMPLE_TITLES, useProfilePreview } from "@/hooks/useProfilePreview"
import { cn } from "@/lib/utils"

// PresetCard is one of the three choices that make up a profile's baseline.
//
// The cards are large and illustrated on purpose: this is the only decision
// most people will ever make here, so it should be answerable at a glance and
// pleasant to land on, rather than a row of radio buttons for a question about
// pixel counts.
function PresetCard({ preset, active, onSelect }) {
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-pressed={active}
      className={cn(
        "group relative flex flex-col overflow-hidden rounded-xl border text-left transition-all",
        active
          ? "border-primary/60 bg-primary/5 shadow-sm"
          : "border-border hover:border-muted-foreground/40 hover:bg-muted/30",
      )}
    >
      <span
        className={cn(
          "absolute right-3 top-3 flex h-5 w-5 items-center justify-center rounded-full border transition-colors",
          active
            ? "border-primary bg-primary text-primary-foreground"
            : "border-border bg-background text-transparent group-hover:border-muted-foreground/50",
        )}
      >
        <Check className="h-3 w-3" />
      </span>

      <div
        className={cn(
          "flex items-end justify-center px-5 pb-1 pt-6 transition-colors",
          active ? "text-primary" : "text-muted-foreground/70 group-hover:text-muted-foreground",
        )}
      >
        <PresetArt preset={preset.key} />
      </div>

      <div className="flex flex-col gap-1 px-5 pb-5 pt-3">
        <span className="text-2xl font-semibold leading-none tracking-tight">{preset.label}</span>
        <span className="font-mono text-[11px] text-muted-foreground">{preset.tiers}</span>
        <span className="pt-1 text-xs leading-relaxed text-muted-foreground">{preset.description}</span>
      </div>
    </button>
  )
}

function PresetPicker({ value, onChange }) {
  const selected = value || DEFAULT_PRESET
  return (
    <div className="grid gap-3 sm:grid-cols-3">
      {PRESETS.map((preset) => (
        <PresetCard
          key={preset.key}
          preset={preset}
          active={preset.key === selected}
          onSelect={() => onChange(preset.key)}
        />
      ))}
    </div>
  )
}

export function ProfileEditor({ profile, onChange, libraryRules = [] }) {
  const [sampleInput, setSampleInput] = useState(SAMPLE_TITLES.join("\n"))
  const [previewKind, setPreviewKind] = useState("movie")
  const [targetTitle, setTargetTitle] = useState("")
  const [sample, setSample] = useState(EMPTY_SAMPLE)

  // One preview request serves the rules tab's per-rule counts and the panel's
  // full breakdown, so the two can never disagree on screen. It lives here
  // rather than in either consumer because both need it and neither owns it.
  const sampleTitles = useMemo(
    () => sampleInput.split("\n").map((t) => t.trim()).filter(Boolean),
    [sampleInput],
  )
  const preview = useProfilePreview(profile, {
    titles: sampleTitles,
    kind: previewKind,
    targetTitle,
    sample,
  })

  const ruleCount = (profile.rules || []).filter((r) => r && r.enabled !== false).length

  return (
    <div className="space-y-5">
      <Tabs defaultValue="quality" className="w-full">
        <TabsList className="w-full justify-start overflow-x-auto">
          <TabsTrigger value="quality">Quality</TabsTrigger>
          <TabsTrigger value="rules">
            Rules
            {ruleCount > 0 && (
              <Badge variant="secondary" className="ml-1 h-4 px-1 text-[10px] tabular-nums">{ruleCount}</Badge>
            )}
          </TabsTrigger>
        </TabsList>

        <TabsContent value="quality" className="space-y-3">
          <p className="max-w-prose text-xs text-muted-foreground">
            Pick the largest screen you watch on. This sets the resolution ceiling and every other ranking
            default: garbage and adult releases are refused, and everything else is scored so a poor release
            sorts last rather than disappearing. Anything you want beyond that is a rule.
          </p>
          <p className="max-w-prose text-xs text-muted-foreground">
            The ceiling decides what you are offered, not the order you are offered it in. Order is the score,
            and only the score: a resolution is worth 20000 points a tier, so 4K leads on its own, and a rule
            worth more than that puts its releases first whatever resolution they are.
          </p>
          <PresetPicker
            value={profile.preset}
            onChange={(preset) => onChange({ ...profile, preset })}
          />
        </TabsContent>

        {/* The preview lives with the rules rather than beside both tabs: it
            is what the per-rule counts are measured against, and on the Quality
            tab there is nothing to preview that the three cards do not already
            say. Its state is held above the tabs, so switching away and back
            keeps the release names you pasted. */}
        <TabsContent value="rules" className="space-y-5">
          <RulesEditor
            values={profile.rules || []}
            onChange={(rules) => onChange({ ...profile, rules })}
            libraryRules={libraryRules}
            ruleStats={preview.ruleStats}
            sampleCount={preview.sampleCount}
            error={preview.error}
          />
          <ProfilePreview
            preview={preview}
            sampleInput={sampleInput}
            onSampleInputChange={setSampleInput}
            kind={previewKind}
            onKindChange={setPreviewKind}
            targetTitle={targetTitle}
            onTargetTitleChange={setTargetTitle}
            sample={sample}
            onSampleChange={setSample}
          />
        </TabsContent>
      </Tabs>
    </div>
  )
}
