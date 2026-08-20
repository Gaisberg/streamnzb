import React, { useRef, useState } from "react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { NumberField, ScoreInput, SettingBlock, SettingGroup } from "@/components/ui/setting"
import { ConfidenceChip } from "@/components/ConfidenceChip"
import { Check, ChevronDown, Plus, SkipForward, Trash2, TriangleAlert } from "lucide-react"
import { RULE_ACTIONS, RULE_ATTRIBUTES, RULE_PRESETS, RULE_SCOPES, formatScore } from "@/lib/profiles"
import { cn, selectClass } from "@/lib/utils"

// tierOf reports which optional attribute tier a condition depends on. It
// mirrors the AST walk the server does, minus string literals, so the editor
// can warn about a rule that will skip most releases while it is being typed
// rather than after it is saved.
function tierOf(when = "") {
  const stripped = when.replace(/"(?:[^"\\]|\\.)*"/g, "").replace(/'(?:[^'\\]|\\.)*'/g, "")
  if (/\bprobed\./.test(stripped)) return "measured"
  if (/\bavail\./.test(stripped)) return "community"
  if (/\b(sizeGB|ageDays|grabs|passworded|indexer|releaseName|querySource|library)\b/.test(stripped)) {
    return "reported"
  }
  return "inferred"
}

const SKIP_NOTE = {
  measured: "Only judges releases that have been probed — library items. Everything else passes untouched.",
  community: "Only judges releases AvailNZB has an opinion about. Everything else passes untouched.",
}

// AttributeReference is the rules counterpart to the formatter's field list:
// everything a condition can read, grouped by how far it can be trusted.
// Clicking a name inserts it into the condition you were last editing, which
// is the part the formatter's read-only list makes you do by hand.
function AttributeReference({ onInsert }) {
  const [open, setOpen] = useState(false)

  return (
    <div className="rounded-lg border border-border/60 bg-card/40">
      <button
        type="button"
        onClick={() => setOpen(!open)}
        className="flex w-full items-center gap-2 px-3 py-2.5 text-left"
      >
        <ChevronDown className={cn("h-3.5 w-3.5 text-muted-foreground transition-transform", !open && "-rotate-90")} />
        <span className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          What a rule can read
        </span>
        <span className="text-[11px] text-muted-foreground/70">
          {open ? "" : "click a name to insert it"}
        </span>
      </button>

      {open && (
        <div className="space-y-4 border-t border-border/50 px-3 py-3">
          {RULE_ATTRIBUTES.map((group) => (
            <div key={group.title} className="space-y-1.5">
              <div className="flex items-center gap-2">
                <span className="text-[11px] font-medium text-foreground">{group.title}</span>
                <ConfidenceChip tier={group.tier} />
              </div>
              {group.note && <p className="max-w-prose text-[11px] text-muted-foreground">{group.note}</p>}
              <div className="flex flex-wrap gap-1">
                {group.items.map((item) => (
                  <button
                    key={item.name}
                    type="button"
                    title={item.example ? `${item.type} — ${item.example}` : item.type}
                    onClick={() => onInsert(item.name)}
                    className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
                  >
                    {item.name}
                  </button>
                ))}
              </div>
            </div>
          ))}

          <div className="space-y-1.5 border-t border-border/50 pt-3">
            <span className="text-[11px] font-medium text-foreground">Operators</span>
            <div className="flex flex-wrap gap-1">
              {["and", "or", "not", "in", "==", "!=", ">", ">=", "<", "<=", "( )", "matches", "contains", "startsWith", "endsWith"].map((op) => (
                <button
                  key={op}
                  type="button"
                  onClick={() => onInsert(op === "( )" ? "()" : op)}
                  className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
                >
                  {op}
                </button>
              ))}
            </div>
            <p className="max-w-prose pt-1 text-[11px] text-muted-foreground">
              A condition has to answer yes or no. <code className="font-mono">matches</code> takes a Go regular
              expression — <code className="font-mono">releaseName matches &quot;(?i)\bIMAX\b&quot;</code>. There is no
              lookahead, and rules are why you do not need one:{" "}
              <code className="font-mono">\bDV\b(?!.*HDR10)</code> is{" "}
              <code className="font-mono">dolbyVision and not hdrFallback</code>.
            </p>
          </div>
        </div>
      )}
    </div>
  )
}

// RuleStat is the live feedback beside a condition: how the rule behaved
// against the release names in the preview below. A rule that matches nothing
// looks identical to a rule that is wrong until something says so.
function RuleStat({ stat, sampleCount, action, count }) {
  if (!sampleCount) return null
  const skipped = stat?.skipped || 0

  if (skipped === sampleCount) {
    return (
      <span className="flex items-center gap-1 text-[11px] text-muted-foreground">
        <SkipForward className="h-3 w-3" />
        not judged on any sample
      </span>
    )
  }

  // A cap is about how many releases fall under it, not how many it paid out
  // on: "4 match, 3 kept" is the thing you need to see to know it is working.
  if (action === "limit") {
    const under = stat?.limited || 0
    if (under === 0) {
      return <span className="text-[11px] text-muted-foreground">nothing falls under this</span>
    }
    const kept = Math.min(under, count || 0)
    return (
      <span className={cn(
        "flex items-center gap-1 text-[11px]",
        under > kept ? "text-destructive" : "text-muted-foreground",
      )}>
        <Check className="h-3 w-3" />
        {under} match, {kept} kept
      </span>
    )
  }

  const matched = (stat?.matched || 0) + (stat?.rejected || 0)
  if (matched === 0) {
    return <span className="text-[11px] text-muted-foreground">no sample matched</span>
  }
  return (
    <span className={cn(
      "flex items-center gap-1 text-[11px]",
      action === "reject" ? "text-destructive" : "text-emerald-600 dark:text-emerald-500",
    )}>
      <Check className="h-3 w-3" />
      {action === "reject" ? "removes" : "pays out on"} {matched} of {sampleCount}
    </span>
  )
}

function RuleCard({ rule, stat, sampleCount, onChange, onRemove, registerInput }) {
  // Three actions, not two. Reading this as a boolean is what made a limit
  // preset arrive as a score rule worth nothing.
  const action = rule.action === "reject" || rule.action === "limit" ? rule.action : "score"
  const enabled = rule.enabled !== false
  const tier = tierOf(rule.when)
  const skipNote = SKIP_NOTE[tier]

  const patch = (next) => onChange({ ...rule, ...next })

  return (
    <div className={cn("rounded-lg border border-border/60 bg-card/40", !enabled && "opacity-55")}>
      <div className="flex flex-wrap items-center gap-2 border-b border-border/50 px-3 py-2">
        <Switch
          checked={enabled}
          onCheckedChange={(v) => patch({ enabled: v })}
          aria-label={`Enable ${rule.name || "rule"}`}
        />
        <Input
          value={rule.name || ""}
          onChange={(e) => patch({ name: e.target.value })}
          placeholder="Name this rule"
          className="h-8 w-44 text-xs"
          aria-label="Rule name"
        />
        <ConfidenceChip tier={tier} />
        <div className="flex-1" />
        <select
          className={cn(selectClass, "h-8 w-auto py-1 text-xs")}
          value={rule.scope || "all"}
          onChange={(e) => patch({ scope: e.target.value === "all" ? undefined : e.target.value })}
          aria-label="Applies to"
        >
          {RULE_SCOPES.map((s) => <option key={s.key} value={s.key}>{s.label}</option>)}
        </select>
        <select
          className={cn(selectClass, "h-8 w-auto py-1 text-xs")}
          value={action}
          onChange={(e) => {
            const next = e.target.value
            patch({
              action: next === "score" ? undefined : next,
              count: next === "limit" ? (rule.count || 3) : undefined,
            })
          }}
          aria-label="Action"
        >
          {RULE_ACTIONS.map((a) => <option key={a.key} value={a.key}>{a.label}</option>)}
        </select>
        {action === "score" && (
          <ScoreInput
            value={rule.points ?? 0}
            onCommit={(points) => patch({ points })}
            className="h-8 w-24"
            aria-label="Points"
          />
        )}
        {action === "reject" && (
          <Badge variant="outline" className="h-8 px-2 text-[11px] font-normal text-destructive">
            removes it
          </Badge>
        )}
        {action === "limit" && (
          <span className="flex items-center gap-1.5 whitespace-nowrap text-[11px] text-muted-foreground">
            keep best
            <NumberField
              value={rule.count ?? 3}
              onCommit={(count) => patch({ count: Math.max(1, count) })}
              min={1}
              step={1}
              className="h-8 w-16"
              aria-label="How many to keep"
            />
          </span>
        )}
        <button
          type="button"
          onClick={onRemove}
          className="text-muted-foreground/70 transition-colors hover:text-destructive"
          aria-label={`Delete ${rule.name || "rule"}`}
        >
          <Trash2 className="h-3.5 w-3.5" />
        </button>
      </div>

      <div className="space-y-1.5 px-3 py-2.5">
        <textarea
          ref={registerInput}
          value={rule.when || ""}
          onChange={(e) => patch({ when: e.target.value })}
          rows={2}
          spellCheck={false}
          placeholder={'dolbyVision and not hdrFallback'}
          className="w-full resize-y rounded-md border border-input bg-background p-2.5 font-mono text-xs leading-relaxed focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
          aria-label="Condition"
        />
        <div className="flex flex-wrap items-center justify-between gap-2">
          <p className="text-[11px] text-muted-foreground">
            {action === "limit"
              ? `Of the releases matching this, the best ${rule.count ?? 3} are offered and the rest are dropped.`
              : skipNote}
          </p>
          <RuleStat stat={stat} sampleCount={sampleCount} action={action} count={rule.count} />
        </div>
      </div>
    </div>
  )
}

// RulesEditor is the advanced surface: write a condition over everything known
// about a release, see straight away what it does to your sample releases.
// Built to the same anatomy as the result-format editor — an editing surface,
// a reference of what can be read, and a live preview — because it is the same
// job: hand-writing an expression against data you cannot see.
export function RulesEditor({ values = [], onChange, ruleStats = {}, sampleCount = 0, error = "" }) {
  const inputsRef = useRef({})

  // insert drops an attribute or operator into whichever condition was last
  // focused, at the caret. With no condition focused it appends to the last
  // rule, which is almost always the one just added.
  const focusedRef = useRef(null)
  const insert = (text) => {
    const index = focusedRef.current ?? values.length - 1
    const rule = values[index]
    if (!rule) return
    const el = inputsRef.current[index]
    const current = rule.when || ""
    let next
    let caret
    if (el && typeof el.selectionStart === "number") {
      const start = el.selectionStart
      const end = el.selectionEnd
      const needsSpace = start > 0 && !/\s$/.test(current.slice(0, start))
      const chunk = (needsSpace ? " " : "") + text
      next = current.slice(0, start) + chunk + current.slice(end)
      caret = start + chunk.length
    } else {
      next = current ? `${current} ${text}` : text
      caret = next.length
    }
    onChange(values.map((r, i) => (i === index ? { ...r, when: next } : r)))
    window.requestAnimationFrame(() => {
      const target = inputsRef.current[index]
      if (!target) return
      target.focus()
      target.setSelectionRange(caret, caret)
    })
  }

  const add = (preset) => {
    onChange([...values, { ...(preset || { name: "", when: "", points: 0 }) }])
    focusedRef.current = values.length
  }

  const availablePresets = RULE_PRESETS.filter((preset) => !values.some((v) => v.name === preset.name))

  return (
    <SettingGroup
      description="A condition over everything known about a release, and what to do when it holds: move its score, remove it, or cap how many like it you are offered. This is where the things one pattern cannot say live — “Dolby Vision but only without an HDR fallback”, “over 30 GB unless it is 4K”, “at most three in 4K”."
      actions={
        <Button type="button" variant="secondary" size="sm" className="h-7 gap-1.5 px-2.5 text-xs" onClick={() => add()}>
          <Plus className="h-3.5 w-3.5" /> Add rule
        </Button>
      }
    >
      {/* A condition that will not compile is reported by the same preview
          request that produces the match counts, and it names the rule. Showing
          it here rather than only under the preview means the error appears
          where the typing is happening. */}
      {error && (
        <SettingBlock>
          <div className="flex items-start gap-2 rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2">
            <TriangleAlert className="mt-0.5 h-3.5 w-3.5 shrink-0 text-destructive" />
            <p className="text-xs text-destructive">{error}</p>
          </div>
        </SettingBlock>
      )}

      <SettingBlock>
        {values.length > 0 ? (
          <div className="space-y-2">
            {values.map((rule, index) => (
              <RuleCard
                key={index}
                rule={rule}
                stat={ruleStats[rule.name]}
                sampleCount={sampleCount}
                onChange={(next) => onChange(values.map((r, i) => (i === index ? next : r)))}
                onRemove={() => onChange(values.filter((_, i) => i !== index))}
                registerInput={(el) => {
                  if (el) {
                    inputsRef.current[index] = el
                    el.onfocus = () => { focusedRef.current = index }
                  } else {
                    delete inputsRef.current[index]
                  }
                }}
              />
            ))}
          </div>
        ) : (
          <p className="rounded-lg border border-dashed border-border/60 px-3 py-6 text-center text-xs text-muted-foreground">
            No rules yet. Start from one below, or add an empty one and write your own.
          </p>
        )}
      </SettingBlock>

      {availablePresets.length > 0 && (
        <SettingBlock label="Start from" description="Adds the rule so you can rename and adjust it.">
          <div className="flex flex-wrap gap-1">
            {availablePresets.map((preset) => (
              <button
                key={preset.name}
                type="button"
                onClick={() => add(preset)}
                className="rounded-md border border-dashed border-border px-1.5 py-0.5 text-[11px] text-muted-foreground transition-colors hover:border-foreground/40 hover:text-foreground"
              >
                {preset.name}{" "}
                <span className={cn(preset.action === "reject" ? "text-destructive" : "")}>
                  {preset.action === "reject"
                    ? "reject"
                    : preset.action === "limit"
                      ? `keep ${preset.count}`
                      : formatScore(preset.points ?? 0)}
                </span>
              </button>
            ))}
          </div>
        </SettingBlock>
      )}

      <SettingBlock>
        <AttributeReference onInsert={insert} />
      </SettingBlock>

      <SettingBlock>
        <div className="flex items-start gap-2 rounded-lg border border-border/60 px-3 py-2.5">
          <Label className="sr-only">About fail-open</Label>
          <SkipForward className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <p className="max-w-prose text-[11px] text-muted-foreground">
            Rules reading <code className="font-mono">probed.*</code> or <code className="font-mono">avail.*</code>{" "}
            skip releases that carry nothing in that tier — a release that was never probed, or that nobody has
            reported. Without that, one rule like <code className="font-mono">probed.height &lt; 1080</code> would
            empty every result list of everything except library hits.
          </p>
        </div>
      </SettingBlock>
    </SettingGroup>
  )
}
