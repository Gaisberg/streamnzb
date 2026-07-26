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
import {
  DndContext, KeyboardSensor, PointerSensor, closestCenter, useSensor, useSensors,
} from "@dnd-kit/core"
import {
  SortableContext, arrayMove, sortableKeyboardCoordinates, useSortable, verticalListSortingStrategy,
} from "@dnd-kit/sortable"
import { CSS } from "@dnd-kit/utilities"
import { CircleHelp, GripVertical, RotateCcw, Search, Sparkles, X } from "lucide-react"
import {
  ATTRIBUTE_GROUPS, LANGUAGE_CODES, LANGUAGE_GROUPS,
  RESOLUTIONS, SORT_KEYS, effectivePolicy, formatScore,
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

function SortableRow({ id, index, meta }) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id })

  return (
    <div
      ref={setNodeRef}
      style={{ transform: CSS.Transform.toString(transform), transition }}
      className={cn(
        "flex items-center gap-3 rounded-lg border border-border/60 bg-card/40 px-3 py-2.5",
        isDragging && "z-10 border-primary/40 shadow-lg"
      )}
    >
      <button
        type="button"
        className="cursor-grab touch-none text-muted-foreground/60 transition-colors hover:text-foreground active:cursor-grabbing"
        {...attributes}
        {...listeners}
        aria-label={`Reorder ${meta?.label || id}`}
      >
        <GripVertical className="h-4 w-4" />
      </button>
      <span className="w-5 text-xs tabular-nums text-muted-foreground">{index + 1}</span>
      <div className="min-w-0 flex-1">
        <div className="text-sm">{meta?.label || id}</div>
        {meta?.hint && <div className="text-[11px] text-muted-foreground">{meta.hint}</div>}
      </div>
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

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates })
  )

  const sortOrder = useMemo(() => {
    const stored = profile.sort_order?.length ? profile.sort_order : SORT_KEYS.map((s) => s.key)
    const known = stored.filter((k) => SORT_KEYS.some((s) => s.key === k))
    return [...known, ...SORT_KEYS.map((s) => s.key).filter((k) => !known.includes(k))]
  }, [profile.sort_order])

  const onDragEnd = ({ active, over }) => {
    if (!over || active.id === over.id) return
    const from = sortOrder.indexOf(active.id)
    const to = sortOrder.indexOf(over.id)
    onChange({ ...profile, sort_order: arrayMove(sortOrder, from, to) })
  }

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
        <TabsTrigger value="sorting">Sorting</TabsTrigger>
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
            label="Skip low-quality rips"
            hint="Rejects camcorder, telesync, telecine and screener releases, plus anything flagged as a bad rip."
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
            <Input
              type="number"
              value={options.min_rank ?? -10000}
              onChange={(e) => setOptions({ min_rank: Number(e.target.value) || 0 })}
              className="h-8 w-32 font-mono text-xs"
            />
          </FieldRow>
          <FieldRow
            label="Preference bonus"
            hint="Added once when a preferred pattern or language matches."
          >
            <Input
              type="number"
              value={options.preferred_bonus ?? 10000}
              onChange={(e) => setOptions({ preferred_bonus: Number(e.target.value) || 0 })}
              className="h-8 w-32 font-mono text-xs"
            />
          </FieldRow>
          <FieldRow
            label="Title match strictness"
            hint="How closely a release title must match what was requested, from 0 to 1. Higher is stricter."
          >
            <Input
              type="number"
              step="0.05"
              min="0"
              max="1"
              value={options.title_threshold ?? 0.85}
              onChange={(e) => setOptions({ title_threshold: Number(e.target.value) })}
              className="h-8 w-32 font-mono text-xs"
            />
          </FieldRow>
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
        <p className="text-xs text-muted-foreground">
          These are regular expressions matched against the full release name. Wrap a pattern in slashes to make it
          case-sensitive.
        </p>
      </TabsContent>

      <TabsContent value="languages" className="space-y-5">
        <PatternInput
          label="Required"
          hint="A release must include at least one of these languages. Groups are accepted too: anime, common, non_anime, all."
          values={ranking.languages?.required || []}
          onChange={(v) => setLanguages({ required: v })}
          placeholder="en"
          suggestions={LANGUAGE_CODES}
        />
        <PatternInput
          label="Excluded"
          hint="Releases including any of these are rejected."
          values={ranking.languages?.exclude || []}
          onChange={(v) => setLanguages({ exclude: v })}
          placeholder="ru"
          suggestions={[...LANGUAGE_GROUPS, ...LANGUAGE_CODES]}
        />
        <PatternInput
          label="Preferred"
          hint="Adds the preference bonus when matched."
          values={ranking.languages?.preferred || []}
          onChange={(v) => setLanguages({ preferred: v })}
          placeholder="en"
          suggestions={LANGUAGE_CODES}
        />
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

      <TabsContent value="sorting" className="space-y-3">
        <p className="text-xs text-muted-foreground">
          Drag to reorder. Results are sorted by the first entry, and each one below it breaks ties in the one above.
        </p>
        <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={onDragEnd}>
          <SortableContext items={sortOrder} strategy={verticalListSortingStrategy}>
            <div className="space-y-1.5">
              {sortOrder.map((key, index) => (
                <SortableRow
                  key={key}
                  id={key}
                  index={index}
                  meta={SORT_KEYS.find((s) => s.key === key)}
                />
              ))}
            </div>
          </SortableContext>
        </DndContext>
      </TabsContent>
    </Tabs>
  )
}
