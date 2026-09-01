import React, { useRef, useState } from "react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { Hint, NumberField, ScoreInput, SettingBlock, SettingGroup } from "@/components/ui/setting"
import { ConfidenceChip } from "@/components/ConfidenceChip"
import { Check, ChevronDown, Copy, Plus, SkipForward, Trash2, TriangleAlert } from "lucide-react"
import {
  DEFAULT_LIMIT_COUNT, RULE_ACTIONS, RULE_ATTRIBUTES, RULE_GROUP_BY_PRESETS, RULE_PRESETS, RULE_SCOPES,
  formatScore, inlineRuleRefs, renameRuleRefs, ruleAction, ruleGroupBy, ruleKey, rulesFromText, rulesToText,
} from "@/lib/profiles"
import { cn, selectClass } from "@/lib/utils"

// tierOf reports which optional attribute tier a condition depends on. It
// mirrors the AST walk the server does, minus string literals, so the editor
// can warn about a rule that will skip most releases while it is being typed
// rather than after it is saved.
// stripSetCalls removes the body of every result-set call — count(...),
// exists(...), any(...), none(...). What those read decides whether the set
// can answer them, not whether this release will be judged, so their contents
// must not trip the per-release tier warning.
function stripSetCalls(s) {
  const re = /\b(?:count|exists|any|none)\s*\(/g
  let out = ""
  let last = 0
  let m
  while ((m = re.exec(s))) {
    let depth = 1
    let i = re.lastIndex
    while (i < s.length && depth > 0) {
      if (s[i] === "(") depth++
      else if (s[i] === ")") depth--
      i++
    }
    out += s.slice(last, m.index)
    last = i
    re.lastIndex = i
  }
  return out + s.slice(last)
}

function tierOf(when = "", rules = []) {
  // References are inlined before literals are dropped: the name inside
  // matched("…") is a literal, and stripping it first would leave nothing to
  // resolve.
  const stripped = stripSetCalls(
    inlineRuleRefs(when, rules).replace(/"(?:[^"\\]|\\.)*"/g, "").replace(/'(?:[^'\\]|\\.)*'/g, ""),
  )
  if (/\bprobed\./.test(stripped)) return "measured"
  if (/\bseadex\./.test(stripped)) return "seadex"
  if (/\bavail\./.test(stripped)) return "community"
  if (/\b(sizeGB|ageDays|grabs|passworded|indexer|releaseName|querySource|library)\b/.test(stripped)) {
    return "reported"
  }
  return "inferred"
}

// The chip shows how far a value can be trusted; seadex is community-curated
// data with its own skip behavior, so it keys its own note but wears the
// community chip.
const CHIP_TIER = { seadex: "community" }

const SKIP_NOTE = {
  measured: "Only judges releases that have been probed — library items. Everything else passes untouched.",
  community: "Only judges releases AvailNZB has an opinion about. Everything else passes untouched.",
  seadex: "Only judges anime requested through Kitsu, and only when the title could be looked up on SeaDex. Everything else passes untouched.",
  // Not a fail-open caveat like the two above: this one runs on every real
  // result. It is the preview that cannot answer it, and saying so is the
  // difference between "my rule is broken" and "my rule is untestable here".
  reported: "Reads size, age or grabs, which come from the NZB. Runs on every real result, but the preview below cannot judge it from a release name alone.",
}

// The grouping menu offers the handful worth a menu; anything else is written
// as an expression, which is what grouping is underneath. CUSTOM_GROUP_BY is
// the menu entry that reveals the field rather than a value a rule can carry:
// it never reaches a rule, and no grouping can collide with it, because a
// grouping is an expression and this is not one.
const CUSTOM_GROUP_BY = "__custom__"

function isCustomGroupBy(groupBy) {
  return !!groupBy && !RULE_GROUP_BY_PRESETS.some((preset) => preset.key === groupBy)
}

// AttributeReference is the rules counterpart to the formatter's field list:
// everything a condition can read, grouped by how far it can be trusted.
// Clicking a name inserts it into the condition you were last editing, which
// is the part the formatter's read-only list makes you do by hand.
function AttributeReference({ onInsert, rules = [], libraryRules = [] }) {
  const [open, setOpen] = useState(false)
  // Referable rules are the named ones, deduplicated: a name two rules share
  // has no answer to which was meant, and the compiler refuses it, so it is
  // not offered as something to click.
  const referable = rules
    .map((rule) => String(rule.name || "").trim())
    .filter((name, i, all) => name && all.findIndex((other) => ruleKey(other) === ruleKey(name)) === i)
  // Library defines a profile rule shadows are listed under the profile's own
  // rules already — one name, one chip.
  const referableLibrary = libraryRules
    .map((rule) => String(rule.name || "").trim())
    .filter((name, i, all) => name && all.findIndex((other) => ruleKey(other) === ruleKey(name)) === i)
    .filter((name) => !referable.some((own) => ruleKey(own) === ruleKey(name)))

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
                {group.tier && <ConfidenceChip tier={group.tier} />}
              </div>
              {group.note && <p className="max-w-prose text-[11px] text-muted-foreground">{group.note}</p>}
              <div className="flex flex-wrap gap-1">
                {group.items.map((item) => (
                  <button
                    key={item.name}
                    type="button"
                    title={item.example ? `${item.type} — ${item.example}` : item.type}
                    onClick={() => onInsert(item.insert || item.name)}
                    className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
                  >
                    {item.name}
                  </button>
                ))}
              </div>
            </div>
          ))}

          {referableLibrary.length > 0 && (
            <div className="space-y-1.5 border-t border-border/50 pt-3">
              <span className="text-[11px] font-medium text-foreground">Library defines</span>
              <p className="max-w-prose text-[11px] text-muted-foreground">
                Defines from the shared libraries below, referenced the same way —{" "}
                <code className="font-mono">matched(&quot;Name&quot;)</code>. A rule of your own under the
                same name shadows the library&apos;s version.
              </p>
              <div className="flex flex-wrap gap-1">
                {referableLibrary.map((name) => (
                  <button
                    key={name}
                    type="button"
                    title={`matched(${JSON.stringify(name)})`}
                    onClick={() => onInsert(`matched(${JSON.stringify(name)})`)}
                    className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
                  >
                    {name}
                  </button>
                ))}
              </div>
            </div>
          )}

          {referable.length > 0 && (
            <div className="space-y-1.5 border-t border-border/50 pt-3">
              <span className="text-[11px] font-medium text-foreground">Your other rules</span>
              <p className="max-w-prose text-[11px] text-muted-foreground">
                <code className="font-mono">matched(&quot;Name&quot;)</code> holds when that rule&apos;s own
                condition holds, so a list of trusted groups is written once and referred to from everywhere
                else. Renaming a rule rewrites the references to it. A rule whose action is{" "}
                <span className="font-medium">Define</span> is only this: a named condition to reference,
                doing nothing on its own.
              </p>
              <div className="flex flex-wrap gap-1">
                {referable.map((name) => (
                  <button
                    key={name}
                    type="button"
                    title={`matched(${JSON.stringify(name)})`}
                    onClick={() => onInsert(`matched(${JSON.stringify(name)})`)}
                    className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
                  >
                    {name}
                  </button>
                ))}
              </div>
            </div>
          )}

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
        {action === "limit" ? "not judged here" : "cannot be judged here"}
      </span>
    )
  }

  // A cap is about how many releases fall under it, not how many it paid out
  // on: "4 match, 3 kept" is the thing you need to see to know it is working.
  // A grouped cap keeps its count per bucket, so the buckets are counted one
  // at a time — summing them first would report a cap of three as keeping
  // three when it is keeping nine.
  if (action === "limit") {
    const under = stat?.limited || 0
    if (under === 0) {
      return <span className="text-[11px] text-muted-foreground">nothing falls under this</span>
    }
    const buckets = Object.values(stat?.limitedGroups || {})
    const perBucket = buckets.length ? buckets : [under]
    const kept = perBucket.reduce((total, inBucket) => total + Math.min(inBucket, count || 0), 0)
    return (
      <span className={cn(
        "flex items-center gap-1 text-[11px]",
        under > kept ? "text-destructive" : "text-muted-foreground",
      )}>
        <Check className="h-3 w-3" />
        {under} match, {kept} kept
        {perBucket.length > 1 && ` across ${perBucket.length} groups`}
      </span>
    )
  }

  const matched = (stat?.matched || 0) + (stat?.rejected || 0)
  if (matched === 0) {
    return <span className="text-[11px] text-muted-foreground">no sample matched</span>
  }
  const removes = action === "reject" || action === "prune"
  return (
    <span className={cn(
      "flex items-center gap-1 text-[11px]",
      removes ? "text-destructive" : "text-emerald-600 dark:text-emerald-500",
    )}>
      <Check className="h-3 w-3" />
      {removes ? "removes" : "pays out on"} {matched} of {sampleCount}
    </span>
  )
}

function RuleCard({ rule, rules, stat, sampleCount, onChange, onRemove, onDuplicate, registerInput }) {
  const action = ruleAction(rule)
  const enabled = rule.enabled !== false
  const groupBy = ruleGroupBy(rule)
  // The grouping is judged alongside the condition, so a cap grouped by a
  // probed attribute is as measured-only as one that tests it.
  const tier = tierOf(`${rule.when || ""} ${groupBy}`, rules)
  const skipNote = SKIP_NOTE[tier]
  // The menu shows the field rather than a value, so whether it is open is
  // state: a grouping the user has cleared back to empty should keep the box
  // they were typing in.
  const [customGroup, setCustomGroup] = useState(() => isCustomGroupBy(groupBy))

  // A rule with no condition yet is one just added, and it opens: everything
  // else stays folded, because a list of twenty is easier to move around when
  // each rule is a line rather than a box.
  const [open, setOpen] = useState(() => !(rule.when || "").trim())

  const patch = (next) => onChange({ ...rule, ...next })

  return (
    <div className={cn("rounded-lg border border-border/60 bg-card/40", !enabled && "opacity-55")}>
      <div className={cn("flex flex-wrap items-center gap-2 px-3 py-2", open && "border-b border-border/50")}>
        <button
          type="button"
          onClick={() => setOpen(!open)}
          className="text-muted-foreground/70 transition-colors hover:text-foreground"
          aria-expanded={open}
          aria-label={`${open ? "Collapse" : "Expand"} ${rule.name || "rule"}`}
        >
          <ChevronDown className={cn("h-3.5 w-3.5 transition-transform", !open && "-rotate-90")} />
        </button>
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
        <ConfidenceChip tier={CHIP_TIER[tier] || tier} />
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
            // A grouping is only meaningful on a cap — the server refuses it
            // elsewhere — so it goes when the action does.
            patch({
              action: next === "score" ? undefined : next,
              count: next === "limit" ? (rule.count || DEFAULT_LIMIT_COUNT) : undefined,
              group_by: next === "limit" ? rule.group_by : undefined,
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
        {action === "prune" && (
          <Badge variant="outline" className="h-8 px-2 text-[11px] font-normal text-destructive">
            removes it after scoring
          </Badge>
        )}
        {action === "define" && (
          <Badge variant="outline" className="h-8 px-2 text-[11px] font-normal text-muted-foreground">
            reference only
          </Badge>
        )}
        {action === "limit" && (
          <span className="flex items-center gap-1.5 whitespace-nowrap text-[11px] text-muted-foreground">
            keep best
            <NumberField
              value={rule.count ?? DEFAULT_LIMIT_COUNT}
              onCommit={(count) => patch({ count: Math.max(1, count) })}
              min={1}
              step={1}
              className="h-8 w-16"
              aria-label="How many to keep"
            />
            per
            <select
              className={cn(selectClass, "h-8 w-auto py-1 text-xs")}
              value={customGroup ? CUSTOM_GROUP_BY : groupBy}
              onChange={(e) => {
                const next = e.target.value
                if (next === CUSTOM_GROUP_BY) {
                  // Nothing to patch yet: the rule keeps whatever grouping it
                  // had until something is typed into the field this opens.
                  setCustomGroup(true)
                  setOpen(true)
                  return
                }
                setCustomGroup(false)
                patch({ group_by: next || undefined })
              }}
              aria-label="Group the cap by"
            >
              {RULE_GROUP_BY_PRESETS.map((p) => <option key={p.key} value={p.key}>{p.label}</option>)}
              <option value={CUSTOM_GROUP_BY}>Custom expression…</option>
            </select>
          </span>
        )}
        <button
          type="button"
          onClick={onDuplicate}
          className="text-muted-foreground/70 transition-colors hover:text-foreground"
          aria-label={`Duplicate ${rule.name || "rule"}`}
          title="Duplicate"
        >
          <Copy className="h-3.5 w-3.5" />
        </button>
        <button
          type="button"
          onClick={onRemove}
          className="text-muted-foreground/70 transition-colors hover:text-destructive"
          aria-label={`Delete ${rule.name || "rule"}`}
          title="Delete"
        >
          <Trash2 className="h-3.5 w-3.5" />
        </button>
      </div>

      {!open && (
        <button
          type="button"
          onClick={() => setOpen(true)}
          className="block w-full truncate px-3 pb-2 text-left font-mono text-[11px] text-muted-foreground/80 hover:text-foreground"
        >
          {(rule.when || "").replace(/\s+/g, " ").trim() || "no condition yet"}
        </button>
      )}

      {open && (
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
          {action === "limit" && customGroup && (
            <label className="flex items-center gap-2 text-[11px] text-muted-foreground">
              <span className="whitespace-nowrap">Group by</span>
              <Input
                value={rule.group_by || ""}
                onChange={(e) => patch({ group_by: e.target.value || undefined })}
                placeholder={'resolution + " " + quality'}
                spellCheck={false}
                className="h-8 flex-1 font-mono text-xs"
                aria-label="Group the cap by"
              />
            </label>
          )}
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-[11px] text-muted-foreground">
              {action === "limit"
                ? groupBy
                  ? `For each value of ${groupBy}, the best ${rule.count ?? DEFAULT_LIMIT_COUNT} of the matching releases are offered and the rest are dropped.`
                  : `Of the releases matching this, the best ${rule.count ?? DEFAULT_LIMIT_COUNT} are offered and the rest are dropped.`
                : action === "define"
                  ? `Does nothing on its own — other rules use it with matched(${JSON.stringify(rule.name || "Name")}).`
                  : action === "prune"
                    ? "Runs after every point is in and the results are ranked — the only stage where finalScore, finalRank and current.* exist."
                    : skipNote}
            </p>
            {action !== "define" && (
              <RuleStat stat={stat} sampleCount={sampleCount} action={action} count={rule.count} />
            )}
          </div>
        </div>
      )}
    </div>
  )
}

// RulesEditor is the advanced surface: write a condition over everything known
// about a release, see straight away what it does to your sample releases.
// Built to the same anatomy as the result-format editor — an editing surface,
// a reference of what can be read, and a live preview — because it is the same
// job: hand-writing an expression against data you cannot see.
export function RulesEditor({ values = [], onChange, libraryRules = [], ruleStats = {}, sampleCount = 0, error = "" }) {
  const inputsRef = useRef({})
  // References resolve against the profile's own rules first, then the shared
  // library defines — the same shadowing order the compiler applies.
  const refRules = [...values, ...libraryRules]

  // Cards are one rule at a time; text is all of them at once. The text is
  // seeded from the rules on the way in and committed back on every parse that
  // succeeds — saving upstream is debounced, so there is nothing to press and
  // no way to leave valid text uncommitted. Text that does not parse is simply
  // not committed, and says which line stopped it.
  const [mode, setMode] = useState("cards")
  const [ruleText, setRuleText] = useState("")
  const [ruleTextError, setRuleTextError] = useState("")
  const textRef = useRef(null)

  const showText = () => {
    setRuleText(rulesToText(values))
    setRuleTextError("")
    setMode("text")
  }

  const editText = (next) => {
    setRuleText(next)
    try {
      onChange(rulesFromText(next))
      setRuleTextError("")
    } catch (err) {
      setRuleTextError(err.message)
    }
  }

  // In text mode there is one box and one caret, so an insert is the plain
  // splice — no rule to find first.
  const insertIntoText = (chunk) => {
    const el = textRef.current
    if (!el) return
    const start = el.selectionStart ?? ruleText.length
    const end = el.selectionEnd ?? start
    const needsSpace = start > 0 && !/\s$/.test(ruleText.slice(0, start))
    const piece = (needsSpace ? " " : "") + chunk
    editText(ruleText.slice(0, start) + piece + ruleText.slice(end))
    const caret = start + piece.length
    window.requestAnimationFrame(() => {
      el.focus()
      el.setSelectionRange(caret, caret)
    })
  }

  // insert drops an attribute or operator into whichever condition was last
  // focused, at the caret. With no condition focused it appends to the last
  // rule, which is almost always the one just added.
  const focusedRef = useRef(null)
  const insert = (text) => {
    if (mode === "text") return insertIntoText(text)
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

  // A preset is a rule either way: a card at the end of the list, or its line
  // at the end of the text.
  const add = (preset) => {
    if (mode === "text") {
      if (!preset) return
      editText([ruleText.replace(/\s+$/, ""), rulesToText([preset])].filter(Boolean).join("\n"))
      return
    }
    onChange([...values, { ...(preset || { name: "", when: "", points: 0 }) }])
    focusedRef.current = values.length
  }

  // duplicate drops the copy directly under its original rather than at the
  // end. Tiers are built in runs — T1, T2, T3 — and the same rule is often
  // copied between anime shows and anime films with only the scope changed, so
  // the copy belongs next to what it was copied from.
  const duplicate = (index) => {
    const source = values[index]
    if (!source) return
    const taken = new Set(values.map((r) => (r.name || "").toLowerCase()))
    const base = (source.name || "Rule").replace(/ copy( \d+)?$/i, "")
    let name = `${base} copy`
    for (let n = 2; taken.has(name.toLowerCase()); n += 1) name = `${base} copy ${n}`
    const next = [...values]
    next.splice(index + 1, 0, { ...source, name })
    onChange(next)
    focusedRef.current = index + 1
  }

  const availablePresets = RULE_PRESETS.filter((preset) => !values.some((v) => v.name === preset.name))

  return (
    <SettingGroup
      actions={
        <div className="flex items-center gap-1.5">
          {/* Both of these are worth reading once and then out of the way: the
              editor below is what the space is for. */}
          <Hint side="bottom">
            <p>
              A condition over everything known about a release, and what to do when it holds: move its score,
              remove it, or cap how many like it you are offered. This is where the things one pattern cannot
              say live — “Dolby Vision but only without an HDR fallback”, “over 30 GB unless it is 4K”, “at most
              three in 4K”.
            </p>
            <p className="mt-2">
              Rules reading <code className="font-mono">probed.*</code> or <code className="font-mono">avail.*</code>{" "}
              skip releases that carry nothing in that tier — a release that was never probed, or that nobody
              has reported. Without that, one rule like <code className="font-mono">probed.height &lt; 1080</code>{" "}
              would empty every result list of everything except library hits.
            </p>
          </Hint>
          {mode === "cards" && (
            <Button type="button" variant="secondary" size="sm" className="h-7 gap-1.5 px-2.5 text-xs" onClick={() => add()}>
              <Plus className="h-3.5 w-3.5" /> Add rule
            </Button>
          )}
          <div className="flex overflow-hidden rounded-md border border-border/60">
            {[["cards", "Cards"], ["text", "Text"]].map(([key, label]) => (
              <button
                key={key}
                type="button"
                onClick={() => (key === "text" ? showText() : setMode("cards"))}
                className={cn(
                  "px-2.5 py-1 text-[11px] transition-colors",
                  mode === key ? "bg-secondary text-foreground" : "text-muted-foreground hover:text-foreground",
                )}
              >
                {label}
              </button>
            ))}
          </div>
        </div>
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

      <SettingBlock
        description={mode === "text"
          ? "One rule per line: a name, what it does, and the same condition the cards take. Tags in brackets set the scope and switch a rule off."
          : undefined}
      >
        {mode === "text" ? (
          <div className="space-y-2">
            <textarea
              ref={textRef}
              value={ruleText}
              onChange={(e) => editText(e.target.value)}
              rows={Math.min(24, Math.max(8, ruleText.split("\n").length + 1))}
              spellCheck={false}
              placeholder={'Atmos: score -800 if "atmos" in traits\n3D: reject if threeD\n4K cap [movie]: keep 3 if resolution == "2160p"\nWeak tail: prune if finalScore < 0 and count(finalScore >= 0) >= 6'}
              className="w-full resize-y rounded-md border border-input bg-background p-2.5 font-mono text-xs leading-relaxed focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
              aria-label="All rules as text"
            />
            {ruleTextError ? (
              <div className="flex items-start gap-2 rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2">
                <TriangleAlert className="mt-0.5 h-3.5 w-3.5 shrink-0 text-destructive" />
                <p className="text-xs text-destructive">{ruleTextError} Nothing is saved until that line parses.</p>
              </div>
            ) : (
              <p className="text-[11px] text-muted-foreground">
                {values.length === 0
                  ? "No rules — this profile filters on its preset alone."
                  : `${values.length} rule${values.length === 1 ? "" : "s"}, saved as you type.`}
              </p>
            )}
          </div>
        ) : values.length > 0 ? (
          <div className="space-y-2">
            {values.map((rule, index) => (
              <RuleCard
                key={index}
                rule={rule}
                rules={refRules}
                stat={ruleStats[rule.name]}
                sampleCount={sampleCount}
                onChange={(next) => {
                  const list = values.map((r, i) => (i === index ? next : r))
                  // A rename carries its references with it, so the rules that
                  // named this one keep naming it while it is being retyped.
                  onChange(next.name === rule.name ? list : renameRuleRefs(list, rule.name, next.name))
                }}
                onRemove={() => onChange(values.filter((_, i) => i !== index))}
                onDuplicate={() => duplicate(index)}
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
            No rules yet. Start from one above, or add an empty one and write your own.
          </p>
        )}
      </SettingBlock>

      <SettingBlock>
        <AttributeReference onInsert={insert} rules={values} libraryRules={libraryRules} />
      </SettingBlock>

    </SettingGroup>
  )
}
