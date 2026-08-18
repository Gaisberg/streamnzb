import React, { useMemo, useState } from "react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Slider } from "@/components/ui/slider"
import { Switch } from "@/components/ui/switch"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { CircleHelp, RotateCcw, Search, Sparkles, X } from "lucide-react"
import {
  ATTRIBUTE_GROUPS, LANGUAGE_CODES, LANGUAGE_GROUPS, LANGUAGE_OPTIONS, LIMIT_FIELDS,
  LIMIT_KINDS, PATTERN_PRESETS, RESOLUTIONS, effectivePolicy, formatScore,
} from "@/lib/profiles"
import { cn } from "@/lib/utils"

const SCORE_MIN = -10000
const SCORE_MAX = 25000

function Hint({ children }) {
  return (
    <TooltipProvider delayDuration={200}>
      <Tooltip>
        <TooltipTrigger asChild>
          <button type="button" className="text-muted-foreground/70 transition-colors hover:text-foreground">
            <CircleHelp className="h-3.5 w-3.5" />
          </button>
        </TooltipTrigger>
        <TooltipContent className="max-w-xs text-xs leading-relaxed">{children}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}

// NumberField keeps what the user typed while it is not yet a number, so
// clearing the box does not commit 0 and quietly change the rule. The value is
// committed only once it parses, clamped when a range is given.
function NumberField({ value, onCommit, min, max, step, className, ...props }) {
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
      className={className}
      {...props}
    />
  )
}

function FieldRow({ label, hint, children }) {
  return (
    <div className="flex items-center justify-between gap-4 rounded-lg border border-border/60 bg-card/40 px-3.5 py-2.5">
      <div className="flex min-w-0 items-center gap-1.5">
        <Label className="text-sm font-normal">{label}</Label>
        {hint && <Hint>{hint}</Hint>}
      </div>
      {children}
    </div>
  )
}

// PatternInput edits one list of regular expressions or language codes.
function PatternInput({ label, hint, values = [], onChange, placeholder, suggestions = [], mono = false }) {
  const [draft, setDraft] = useState("")

  const add = (value) => {
    const next = (value ?? draft).trim()
    if (!next || values.includes(next)) { setDraft(""); return }
    onChange([...values, next])
    setDraft("")
  }

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-1.5">
        <Label className="text-sm">{label}</Label>
        {hint && <Hint>{hint}</Hint>}
      </div>
      <div className="flex gap-2">
        <Input
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); add() } }}
          placeholder={placeholder}
          className={cn("h-9", mono && "font-mono text-xs")}
        />
        <Button type="button" variant="secondary" size="sm" className="h-9 px-4" onClick={() => add()}>
          Add
        </Button>
      </div>
      {values.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {values.map((value) => (
            <Badge key={value} variant="secondary" className={cn("gap-1 pr-1 font-normal", mono && "font-mono text-[11px]")}>
              {value}
              <button
                type="button"
                onClick={() => onChange(values.filter((v) => v !== value))}
                className="rounded-sm opacity-60 transition-opacity hover:opacity-100"
                aria-label={`Remove ${value}`}
              >
                <X className="h-3 w-3" />
              </button>
            </Badge>
          ))}
        </div>
      )}
      {suggestions.length > 0 && (
        <div className="flex flex-wrap gap-1 pt-0.5">
          {suggestions.filter((s) => !values.includes(s)).slice(0, 12).map((s) => (
            <button
              key={s}
              type="button"
              onClick={() => add(s)}
              className="rounded-md border border-dashed border-border px-1.5 py-0.5 text-[11px] text-muted-foreground transition-colors hover:border-foreground/40 hover:text-foreground"
            >
              {s}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

function LanguageSearchInput({ label, hint, values = [], onChange, placeholder }) {
  const [query, setQuery] = useState("")
  const [open, setOpen] = useState(false)

  const filteredOptions = useMemo(() => {
    const q = query.trim().toLowerCase()
    return LANGUAGE_OPTIONS.filter(
      (opt) =>
        !values.includes(opt.code) &&
        (opt.name.toLowerCase().includes(q) || opt.code.toLowerCase().includes(q))
    )
  }, [query, values])

  const add = (code) => {
    const target = (code || query).trim().toLowerCase()
    if (!target || values.includes(target)) {
      setQuery("")
      setOpen(false)
      return
    }
    onChange([...values, target])
    setQuery("")
    setOpen(false)
  }

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-1.5">
        <Label className="text-sm font-medium">{label}</Label>
        {hint && <Hint>{hint}</Hint>}
      </div>

      <div className="relative">
        <div className="flex gap-2">
          <div className="relative flex-1">
            <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => {
                setQuery(e.target.value)
                setOpen(true)
              }}
              onFocus={() => setOpen(true)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault()
                  add()
                }
              }}
              placeholder={placeholder || "Search language by name or code… (e.g. English, Finnish, ja)"}
              className="h-9 pl-8"
            />
          </div>
          <Button type="button" variant="secondary" size="sm" className="h-9 px-4" onClick={() => add()}>
            Add
          </Button>
        </div>

        {open && filteredOptions.length > 0 && query.trim() !== "" && (
          <div className="absolute z-50 mt-1 max-h-56 w-full overflow-y-auto rounded-md border border-border bg-popover p-1 shadow-md">
            {filteredOptions.slice(0, 15).map((opt) => (
              <button
                key={opt.code}
                type="button"
                className="flex w-full items-center justify-between rounded-sm px-2.5 py-1.5 text-left text-xs transition-colors hover:bg-accent hover:text-accent-foreground"
                onClick={() => add(opt.code)}
              >
                <span>{opt.name}</span>
                <Badge variant="outline" className="font-mono text-[10px]">
                  {opt.code}
                </Badge>
              </button>
            ))}
          </div>
        )}
      </div>

      {values.length > 0 && (
        <div className="flex flex-wrap gap-1.5 pt-1">
          {values.map((code) => {
            const match = LANGUAGE_OPTIONS.find((o) => o.code === code)
            const labelText = match ? `${match.name} (${code})` : code
            return (
              <Badge key={code} variant="secondary" className="gap-1 pr-1 font-normal">
                {labelText}
                <button
                  type="button"
                  onClick={() => onChange(values.filter((v) => v !== code))}
                  className="rounded-sm opacity-60 transition-opacity hover:opacity-100"
                  aria-label={`Remove ${code}`}
                >
                  <X className="h-3 w-3" />
                </button>
              </Badge>
            )
          })}
        </div>
      )}
    </div>
  )
}

// WeightedPatterns edits patterns that carry their own score. Unlike Prefer,
// each one is weighted individually and they stack, so several preferences can
// be ranked against each other.
function WeightedPatterns({ values = [], onChange }) {
  const [pattern, setPattern] = useState("")
  const [score, setScore] = useState(1000)

  const add = (entry) => {
    const next = entry || { pattern: pattern.trim(), rank: Number(score) || 0 }
    if (!next.pattern || values.some((v) => v.pattern === next.pattern)) return
    onChange([...values, next])
    setPattern("")
  }

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-1.5">
        <Label className="text-sm">Weighted preferences</Label>
        <Hint>
          Each pattern adds its own score when it matches the release name, and they stack. Use a negative
          score to push something down without rejecting it. Releases carrying extra audio tracks — dual
          audio, multi audio — also count as Dubbed under Scoring, so check that score too if you are
          trying to favour them.
        </Hint>
      </div>

      <div className="flex gap-2">
        <Input
          value={pattern}
          onChange={(e) => setPattern(e.target.value)}
          onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); add() } }}
          placeholder="Dual.?Audio"
          className="h-9 flex-1 font-mono text-xs"
        />
        <Input
          type="number"
          value={score}
          onChange={(e) => setScore(e.target.value)}
          className="h-9 w-28 font-mono text-xs"
          aria-label="Score"
        />
        <Button type="button" variant="secondary" size="sm" className="h-9 px-4" onClick={() => add()}>
          Add
        </Button>
      </div>

      {values.length > 0 && (
        <div className="space-y-1.5">
          {values.map((entry, index) => (
            <div key={entry.pattern} className="flex items-center gap-3 rounded-lg border border-border/60 bg-card/40 px-3 py-2">
              <code className="min-w-0 flex-1 truncate text-xs text-muted-foreground">{entry.pattern}</code>
              <NumberField
                value={entry.rank}
                onCommit={(rank) => {
                  const next = [...values]
                  next[index] = { ...entry, rank }
                  onChange(next)
                }}
                className="h-8 w-24 font-mono text-xs"
              />
              <button
                type="button"
                onClick={() => onChange(values.filter((_, i) => i !== index))}
                className="text-muted-foreground/70 transition-colors hover:text-foreground"
                aria-label={`Remove ${entry.pattern}`}
              >
                <X className="h-3.5 w-3.5" />
              </button>
            </div>
          ))}
        </div>
      )}

      <div className="flex flex-wrap gap-1 pt-0.5">
        {PATTERN_PRESETS.filter((preset) => !values.some((v) => v.pattern === preset.pattern)).map((preset) => (
          <button
            key={preset.label}
            type="button"
            onClick={() => add({ pattern: preset.pattern, rank: preset.rank })}
            className="rounded-md border border-dashed border-border px-1.5 py-0.5 text-[11px] text-muted-foreground transition-colors hover:border-foreground/40 hover:text-foreground"
          >
            {preset.label} {formatScore(preset.rank)}
          </button>
        ))}
      </div>
    </div>
  )
}

// AttributeRow tunes what one release trait is worth and whether it is allowed.
function AttributeRow({ attrKey, label, ranking, onChange }) {
  const policy = effectivePolicy(ranking, attrKey)

  const update = (patch) => {
    const attributes = { ...(ranking.attributes || {}) }
    attributes[attrKey] = { fetch: policy.fetch, rank: policy.score, ...patch }
    onChange({ ...ranking, attributes })
  }

  const reset = () => {
    const attributes = { ...(ranking.attributes || {}) }
    delete attributes[attrKey]
    onChange({ ...ranking, attributes })
  }

  return (
    <div className={cn(
      "grid grid-cols-[1fr_auto] items-center gap-x-4 gap-y-2 rounded-lg border px-3.5 py-2.5 transition-colors sm:grid-cols-[minmax(0,1fr)_minmax(0,180px)_auto]",
      policy.fetch ? "border-border/60 bg-card/40" : "border-destructive/30 bg-destructive/[0.04]"
    )}>
      <div className="flex min-w-0 items-center gap-2">
        <span className={cn("truncate text-sm", !policy.fetch && "text-muted-foreground line-through")}>
          {label}
        </span>
        {policy.overridden ? (
          <button
            type="button"
            onClick={reset}
            title="Reset to the recommended value"
            className="text-muted-foreground/70 transition-colors hover:text-foreground"
          >
            <RotateCcw className="h-3 w-3" />
          </button>
        ) : (
          <span className="hidden text-[10px] text-muted-foreground/60 sm:inline">default</span>
        )}
      </div>

      <div className="col-span-2 flex items-center gap-3 sm:col-span-1">
        <Slider
          value={[Math.max(SCORE_MIN, Math.min(SCORE_MAX, policy.score))]}
          min={SCORE_MIN}
          max={SCORE_MAX}
          step={50}
          disabled={!policy.fetch}
          onValueChange={([v]) => update({ rank: v })}
          className={cn("flex-1", !policy.fetch && "opacity-40")}
        />
        <span className={cn(
          "w-16 shrink-0 text-right font-mono text-xs tabular-nums",
          policy.score > 0 ? "text-emerald-500" : policy.score < 0 ? "text-destructive" : "text-muted-foreground"
        )}>
          {formatScore(policy.score)}
        </span>
      </div>

      <div className="flex items-center justify-end gap-2">
        <Switch
          checked={policy.fetch}
          onCheckedChange={(v) => update({ fetch: v })}
          aria-label={`${policy.fetch ? "Allow" : "Block"} ${label}`}
        />
      </div>
    </div>
  )
}

function AttributeGroup({ group, ranking, onChange, query, modifiedOnly }) {
  const attrs = group.attrs.filter((attr) => {
    if (modifiedOnly && !ranking.attributes?.[attr.key]) return false
    if (!query) return true
    return attr.label.toLowerCase().includes(query) || attr.key.includes(query)
  })
  if (attrs.length === 0) return null

  const blocked = group.attrs.filter((a) => effectivePolicy(ranking, a.key).fetch === false).length

  return (
    <div className="space-y-2">
      <div className="flex items-baseline justify-between gap-2">
        <h4 className="text-xs font-medium uppercase tracking-wider text-muted-foreground">{group.label}</h4>
        {blocked > 0 && (
          <span className="text-[11px] text-muted-foreground">{blocked} blocked</span>
        )}
      </div>
      {group.description && !query && (
        <p className="text-xs text-muted-foreground">{group.description}</p>
      )}
      <div className="space-y-1.5">
        {attrs.map((attr) => (
          <AttributeRow
            key={attr.key}
            attrKey={attr.key}
            label={attr.label}
            ranking={ranking}
            onChange={onChange}
          />
        ))}
      </div>
    </div>
  )
}

// LimitsGrid edits the per-kind NZB attribute bounds. The "All content" row is
// the default every kind inherits; a kind's own value overrides it field by
// field, shown as the placeholder while the kind leaves the field unset.
function LimitsGrid({ limits, onChange }) {
  const setLimit = (kindKey, field, value) => {
    const entry = { ...(limits[kindKey] || {}) }
    if (value > 0) entry[field] = value
    else delete entry[field]
    const next = { ...limits, [kindKey]: entry }
    if (Object.keys(entry).length === 0) delete next[kindKey]
    onChange(next)
  }

  return (
    <div className="overflow-x-auto rounded-lg border border-border/60 bg-card/40">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-border/60 text-xs text-muted-foreground">
            <th className="px-3.5 py-2 text-left font-normal">Content</th>
            {LIMIT_FIELDS.map((field) => (
              <th key={field.key} className="px-2 py-2 text-left font-normal">{field.label}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {LIMIT_KINDS.map((kind) => (
            <tr key={kind.key} className="border-b border-border/40 last:border-b-0">
              <td className="whitespace-nowrap px-3.5 py-1.5">{kind.label}</td>
              {LIMIT_FIELDS.map((field) => {
                const inherited = kind.key === "default" ? 0 : limits.default?.[field.key]
                return (
                  <td key={field.key} className="px-2 py-1.5">
                    <NumberField
                      value={limits[kind.key]?.[field.key] ?? ""}
                      onCommit={(value) => setLimit(kind.key, field.key, value)}
                      min={0}
                      step={field.step}
                      placeholder={inherited ? String(inherited) : "off"}
                      className="h-8 w-24 font-mono text-xs"
                    />
                  </td>
                )
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

export function ProfileEditor({ profile, onChange }) {
  const ranking = profile.ranking || {}
  const options = ranking.options || {}
  const [query, setQuery] = useState("")
  const [modifiedOnly, setModifiedOnly] = useState(false)

  const setRanking = (next) => onChange({ ...profile, ranking: next })
  const setOptions = (patch) => setRanking({ ...ranking, options: { ...options, ...patch } })
  const setLanguages = (patch) => setRanking({ ...ranking, languages: { ...(ranking.languages || {}), ...patch } })

  const enabledResolutions = RESOLUTIONS
    .filter((r) => ranking.resolutions?.[r.key] !== false)
    .map((r) => r.key)

  const normalizedQuery = query.trim().toLowerCase()
  const overrideCount = Object.keys(ranking.attributes || {}).length

  return (
    <Tabs defaultValue="quality" className="w-full">
      <TabsList className="w-full justify-start overflow-x-auto">
        <TabsTrigger value="quality">Quality</TabsTrigger>
        <TabsTrigger value="scoring">
          Scoring
          {overrideCount > 0 && (
            <Badge variant="secondary" className="ml-1 h-4 px-1 text-[10px] tabular-nums">{overrideCount}</Badge>
          )}
        </TabsTrigger>
        <TabsTrigger value="rules">Rules</TabsTrigger>
        <TabsTrigger value="languages">Languages</TabsTrigger>
      </TabsList>

      <TabsContent value="quality" className="space-y-5">
        <div className="space-y-2.5">
          <div className="flex items-center gap-1.5">
            <Label className="text-sm">Resolutions</Label>
            <Hint>Only releases in the selected tiers are offered. Unknown covers releases whose resolution could not be read from the title.</Hint>
          </div>
          <ToggleGroup
            type="multiple"
            value={enabledResolutions}
            onValueChange={(next) => {
              const resolutions = {}
              RESOLUTIONS.forEach((r) => { resolutions[r.key] = next.includes(r.key) })
              setRanking({ ...ranking, resolutions })
            }}
            className="flex-wrap justify-start gap-1.5"
          >
            {RESOLUTIONS.map((res) => (
              <ToggleGroupItem
                key={res.key}
                value={res.key}
                variant="outline"
                size="sm"
                className="h-8 px-3 text-xs data-[state=on]:border-primary/40 data-[state=on]:bg-primary/10"
              >
                {res.label}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
        </div>

        <div className="space-y-2">
          <Label className="text-sm">Content</Label>
          <FieldRow
            label="Remove garbage titles"
            hint="Rejects camcorder, telesync, telecine and screener rips, and the junk that carries no source of its own: leaked copies, pre-retail rips and deleted-scene reels."
          >
            <Switch checked={options.remove_trash !== false} onCheckedChange={(v) => setOptions({ remove_trash: v })} />
          </FieldRow>
          <FieldRow label="Skip adult content">
            <Switch checked={options.remove_adult !== false} onCheckedChange={(v) => setOptions({ remove_adult: v })} />
          </FieldRow>
        </div>

        <div className="space-y-2">
          <Label className="text-sm">Thresholds</Label>
          <FieldRow
            label="Minimum score"
            hint="Releases scoring below this are rejected outright, however else they qualify."
          >
            <NumberField
              value={options.min_rank ?? -10000}
              onCommit={(min_rank) => setOptions({ min_rank })}
              className="h-8 w-32 font-mono text-xs"
            />
          </FieldRow>
          <FieldRow
            label="Preference bonus"
            hint="Added once when a preferred pattern matches, and once more when a preferred language matches — a release matching both gets it twice. Same setting as the preferred language bonus on the Languages tab."
          >
            <NumberField
              value={options.preferred_bonus ?? 10000}
              onCommit={(preferred_bonus) => setOptions({ preferred_bonus })}
              className="h-8 w-32 font-mono text-xs"
            />
          </FieldRow>
          <FieldRow
            label="Title match strictness"
            hint="How closely a release title must match a requested title, from 0 to 1. Higher is stricter. Only applies where a target title is given — the Try it out bench. Live results are already title-checked during search, before the profile runs."
          >
            <NumberField
              value={options.title_threshold ?? 0.85}
              onCommit={(title_threshold) => setOptions({ title_threshold })}
              min={0}
              max={1}
              step={0.05}
              className="h-8 w-32 font-mono text-xs"
            />
          </FieldRow>
        </div>

        <div className="space-y-2">
          <div className="flex items-center gap-1.5">
            <Label className="text-sm">NZB limits</Label>
            <Hint>
              Bounds on the NZB itself rather than the parsed title. Empty fields are off; the All content
              row applies everywhere, and a kind&apos;s own value overrides it. Sizes are decimal GB, matching
              stream descriptions. Season packs are judged per episode when the episode count is parseable,
              and a release that does not report an attribute (age, grabs) is never rejected for it.
            </Hint>
          </div>
          <FieldRow
            label="Block password-protected releases"
            hint="Rejects releases the indexer flags as password protected. Indexers that never report the flag are unaffected."
          >
            <Switch
              checked={profile.block_passworded !== false}
              onCheckedChange={(v) => onChange({ ...profile, block_passworded: v })}
            />
          </FieldRow>
          <LimitsGrid limits={profile.limits || {}} onChange={(limits) => onChange({ ...profile, limits })} />
        </div>
      </TabsContent>

      <TabsContent value="scoring" className="space-y-4">
        <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
          <div className="relative flex-1">
            <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search traits…"
              className="h-9 pl-8"
            />
          </div>
          <div className="flex items-center gap-2 rounded-lg border border-border/60 px-3 py-1.5">
            <Label className="whitespace-nowrap text-xs font-normal text-muted-foreground">Changed only</Label>
            <Switch checked={modifiedOnly} onCheckedChange={setModifiedOnly} />
          </div>
        </div>

        <p className="flex items-start gap-1.5 text-xs text-muted-foreground">
          <Sparkles className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          Scores add up to decide the order results come back in. Turn a trait off to reject those releases entirely.
        </p>

        <div className="space-y-6">
          {(!normalizedQuery || "library hit score bonus".includes(normalizedQuery)) &&
            (!modifiedOnly || profile.library_score_bonus != null) && (
            <div className="space-y-2">
              <h4 className="text-xs font-medium uppercase tracking-wider text-muted-foreground">Library</h4>
              <p className="text-xs text-muted-foreground">
                Applied on top of every trait score below when the release comes from the local library.
              </p>
              <FieldRow
                label="Library hit score bonus"
                hint="Extra ranking score added to cached library releases so proven-playable results outrank fresh indexer hits. Default 500; set -1 to disable the bonus."
              >
                <NumberField
                  value={profile.library_score_bonus ?? 500}
                  onCommit={(library_score_bonus) => onChange({ ...profile, library_score_bonus })}
                  min={-1}
                  max={100000}
                  className="h-8 w-32 font-mono text-xs"
                />
              </FieldRow>
            </div>
          )}
          {ATTRIBUTE_GROUPS.map((group) => (
            <AttributeGroup
              key={group.id}
              group={group}
              ranking={ranking}
              onChange={setRanking}
              query={normalizedQuery}
              modifiedOnly={modifiedOnly}
            />
          ))}
        </div>
      </TabsContent>

      <TabsContent value="rules" className="space-y-5">
        <PatternInput
          label="Must match"
          hint="Every pattern here has to appear in the release name, otherwise it is rejected. For “either or”, put the options in one pattern: (IMAX|Extended)."
          values={ranking.require || []}
          onChange={(v) => setRanking({ ...ranking, require: v })}
          placeholder="IMAX"
          mono
        />
        <PatternInput
          label="Never match"
          hint="A release is rejected if any of these appear in its name."
          values={ranking.exclude || []}
          onChange={(v) => setRanking({ ...ranking, exclude: v })}
          placeholder="HDCAM"
          mono
        />
        <PatternInput
          label="Prefer"
          hint={`Adds the preference bonus (${formatScore(options.preferred_bonus ?? 0)}) when matched. Never rejects anything.`}
          values={ranking.preferred || []}
          onChange={(v) => setRanking({ ...ranking, preferred: v })}
          placeholder="IMAX"
          mono
        />
        <WeightedPatterns
          values={ranking.pattern_ranks || []}
          onChange={(v) => setRanking({ ...ranking, pattern_ranks: v })}
        />
        <p className="text-xs text-muted-foreground">
          These are regular expressions matched against the full release name. Wrap a pattern in slashes to make it
          case-sensitive.
        </p>
      </TabsContent>

      <TabsContent value="languages" className="space-y-5">
        <LanguageSearchInput
          label="Required"
          hint="A release must include at least one of these languages. Groups are accepted too: anime, common, non_anime, all."
          values={ranking.languages?.required || []}
          onChange={(v) => setLanguages({ required: v })}
          placeholder="Search language or group to require…"
        />
        <LanguageSearchInput
          label="Excluded"
          hint="Releases including any of these are rejected."
          values={ranking.languages?.exclude || []}
          onChange={(v) => setLanguages({ exclude: v })}
          placeholder="Search language or group to exclude…"
        />
        <LanguageSearchInput
          label="Preferred"
          hint="Adds the preference bonus when matched."
          values={ranking.languages?.preferred || []}
          onChange={(v) => setLanguages({ preferred: v })}
          placeholder="Search language or group to prefer…"
        />

        <div className="rounded-lg border border-border/60 bg-card/40 px-3.5 py-3 space-y-3">
          <div className="flex items-center justify-between gap-4">
            <div className="flex min-w-0 items-center gap-1.5">
              <Label className="text-sm font-normal">Preferred language score bonus</Label>
              <Hint>Score bonus added to releases matching any language in your preferred list. Same setting as the preference bonus on the Quality tab — preferred patterns add it separately.</Hint>
            </div>
            <span className={cn(
              "w-20 text-right font-mono text-xs tabular-nums font-semibold",
              (options.preferred_bonus ?? 10000) > 0 ? "text-emerald-500" : (options.preferred_bonus ?? 10000) < 0 ? "text-destructive" : "text-muted-foreground"
            )}>
              {formatScore(options.preferred_bonus ?? 10000)}
            </span>
          </div>
          <Slider
            value={[Math.max(SCORE_MIN, Math.min(SCORE_MAX, options.preferred_bonus ?? 10000))]}
            min={SCORE_MIN}
            max={SCORE_MAX}
            step={100}
            onValueChange={([v]) => setOptions({ preferred_bonus: v })}
            className="w-full"
          />
        </div>

        <div className="space-y-2">
          <FieldRow
            label="Always allow English"
            hint="English releases are kept even when they would be caught by the exclusion list."
          >
            <Switch checked={options.allow_english !== false} onCheckedChange={(v) => setOptions({ allow_english: v })} />
          </FieldRow>
          <FieldRow
            label="Reject unknown languages"
            hint="Drops releases whose language could not be determined from the name."
          >
            <Switch
              checked={options.remove_unknown_languages === true}
              onCheckedChange={(v) => setOptions({ remove_unknown_languages: v })}
            />
          </FieldRow>
        </div>
      </TabsContent>
    </Tabs>
  )
}
