import React, { useState } from "react"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { SettingBlock, SettingGroup, SettingRow } from "@/components/ui/setting"
import { SampleRelease } from "@/components/SampleRelease"
import { ChevronDown, FlaskConical, Loader2, SkipForward } from "lucide-react"
import { ATTRIBUTE_LABELS, RULE_SCOPES, formatScore } from "@/lib/profiles"
import { cn, selectClass } from "@/lib/utils"

// clauseLabel turns a scoring clause into something readable:
// "attribute:remux" -> "Remux", "rule:IMAX" -> "Rule IMAX".
function clauseLabel(source = "") {
  if (source.startsWith("attribute:")) {
    const key = source.slice("attribute:".length)
    return ATTRIBUTE_LABELS[key] || key
  }
  if (source.startsWith("rule:")) return `Rule ${source.slice("rule:".length)}`
  if (source.startsWith("pattern:")) return `Pattern ${source.slice("pattern:".length)}`
  if (source === "preferred_pattern") return "Preferred pattern"
  if (source === "preferred_language") return "Preferred language"
  return source
}

// reasonLabel turns a rejection reason into plain language.
function reasonLabel(reason = "") {
  if (reason.startsWith("rule: ")) return `Rule ${reason.slice(6)}`
  if (reason.startsWith("score ")) return reason
  if (reason.startsWith("attribute:")) return `${ATTRIBUTE_LABELS[reason.slice(10)] || reason.slice(10)} not allowed`
  if (reason.startsWith("resolution:")) return `${reason.slice(11)} not allowed`
  if (reason.startsWith("require:")) return `Missing ${reason.slice(8)}`
  if (reason.startsWith("exclude:")) return `Matched ${reason.slice(8)}`
  if (reason.startsWith("language:")) return `Language ${reason.slice(9)}`
  if (reason.startsWith("trash")) return "Low-quality rip"
  if (reason === "min_rank") return "Below the minimum score"
  if (reason === "title_mismatch") return "Title did not match"
  if (reason === "adult") return "Adult content"
  if (reason === "parse_error") return "Could not read the release name"
  return reason
}

function ResultRow({ result, expanded, onToggle }) {
  const clauses = result.contributions || []
  const peak = Math.max(1, ...clauses.map((c) => Math.abs(c.rank)))

  return (
    <div className={cn(
      "overflow-hidden rounded-lg border transition-colors",
      result.fetch ? "border-border/60 bg-card/40" : "border-destructive/25 bg-destructive/[0.03]",
    )}>
      <button type="button" onClick={onToggle} className="flex w-full items-start gap-3 px-3 py-2.5 text-left">
        <ChevronDown className={cn(
          "mt-0.5 h-4 w-4 shrink-0 text-muted-foreground transition-transform",
          !expanded && "-rotate-90",
        )} />
        <div className="min-w-0 flex-1 space-y-1.5">
          <div className="truncate font-mono text-xs text-foreground">{result.title}</div>
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge variant={result.fetch ? "default" : "destructive"} className="h-5 text-[10px] font-normal">
              {result.fetch ? "Offered" : "Rejected"}
            </Badge>
            <span className={cn(
              "font-mono text-xs tabular-nums",
              result.rank > 0 ? "text-emerald-600 dark:text-emerald-500" : result.rank < 0 ? "text-destructive" : "text-muted-foreground",
            )}>
              {formatScore(result.rank)}
            </span>
            {result.resolution && result.resolution !== "unknown" && (
              <Badge variant="outline" className="h-5 text-[10px] font-normal">{result.resolution}</Badge>
            )}
            {(result.matched || []).map((m) => (
              <Badge key={m.name} variant="outline" className="h-5 border-primary/40 text-[10px] font-normal text-primary">
                {m.name} {formatScore(m.score)}
              </Badge>
            ))}
            {typeof result.title_ratio === "number" && result.title_ratio > 0 && (
              <Badge variant="outline" className="h-5 text-[10px] font-normal">
                {Math.round(result.title_ratio * 100)}% title match
              </Badge>
            )}
          </div>
          {!result.fetch && result.rejections?.length > 0 && (
            <div className="flex flex-wrap gap-1">
              {result.rejections.map((reason, i) => (
                <span key={i} className="rounded bg-destructive/10 px-1.5 py-0.5 text-[10px] text-destructive">
                  {reasonLabel(reason)}
                </span>
              ))}
            </div>
          )}
        </div>
      </button>

      {expanded && (
        <div className="border-t border-border/60 px-3 py-2.5">
          {clauses.length > 0 ? (
            <div className="space-y-1.5">
              {clauses.map((clause, i) => (
                <div key={i} className="flex items-center gap-3">
                  <span className="w-40 shrink-0 truncate text-xs text-muted-foreground">
                    {clauseLabel(clause.source)}
                  </span>
                  <div className="relative h-1.5 flex-1 overflow-hidden rounded-full bg-muted">
                    <div
                      className={cn(
                        "absolute inset-y-0 rounded-full",
                        clause.rank >= 0 ? "left-1/2 bg-emerald-500/70" : "right-1/2 bg-destructive/70",
                      )}
                      style={{ width: `${(Math.abs(clause.rank) / peak) * 50}%` }}
                    />
                    <div className="absolute inset-y-0 left-1/2 w-px bg-border" />
                  </div>
                  <span className={cn(
                    "w-16 shrink-0 text-right font-mono text-xs tabular-nums",
                    clause.rank > 0 ? "text-emerald-600 dark:text-emerald-500" : clause.rank < 0 ? "text-destructive" : "text-muted-foreground",
                  )}>
                    {formatScore(clause.rank)}
                  </span>
                </div>
              ))}
              <div className="flex items-center justify-between border-t border-border/60 pt-2 text-xs font-medium">
                <span>Total</span>
                <span className="font-mono tabular-nums">{formatScore(result.rank)}</span>
              </div>
            </div>
          ) : (
            <p className="text-xs text-muted-foreground">Nothing in this profile scored this release.</p>
          )}

          {result.skipped_rules?.length > 0 && (
            <div className="mt-3 space-y-1 border-t border-border/60 pt-2">
              <p className="flex items-center gap-1.5 text-[11px] font-medium text-muted-foreground">
                <SkipForward className="h-3 w-3" /> Not judged here
              </p>
              {result.skipped_rules.map((note, i) => (
                <p key={i} className="text-[11px] text-muted-foreground">{note}</p>
              ))}
              <p className="max-w-prose pt-0.5 text-[11px] text-muted-foreground/80">
                A release name carries no file measurements and no availability record, so these rules are skipped —
                exactly as they would be for any release that has never been probed or reported.
              </p>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

// groupAggregates puts one heading on each result-set question. Most are
// counted once for the whole set and are their own group; one comparing
// against current.* is counted once per release, and repeating the condition
// above every count would bury what actually differs — which release it was
// asked on behalf of.
function groupAggregates(aggregates) {
  const groups = []
  const bySource = new Map()
  for (const agg of aggregates || []) {
    let group = bySource.get(agg.source)
    if (!group) {
      group = { source: agg.source, reports: [] }
      bySource.set(agg.source, group)
      groups.push(group)
    }
    group.reports.push(agg)
  }
  return groups
}

// ProfilePreview shows what the profile being edited would do to a set of
// release names. It is fed by the editor rather than fetching for itself, so
// the per-rule counts on the Rules tab and the breakdown here always come from
// the same evaluation.
export function ProfilePreview({
  preview,
  sampleInput,
  onSampleInputChange,
  kind,
  onKindChange,
  targetTitle,
  onTargetTitleChange,
  sample,
  onSampleChange,
}) {
  const [expanded, setExpanded] = useState({})
  const [sampleOpen, setSampleOpen] = useState(false)
  const { results, aggregates, loading, error } = preview

  // Offered first, then by score, matching what the addon returns.
  const ordered = results
    ? results.map((r, i) => ({ ...r, idx: i })).sort((a, b) => {
        if (a.fetch !== b.fetch) return a.fetch ? -1 : 1
        return (b.rank || 0) - (a.rank || 0)
      })
    : []
  const offered = ordered.filter((r) => r.fetch).length

  return (
    <Card className="border border-border bg-card">
      <CardHeader className="pb-3">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div>
            <CardTitle className="flex items-center gap-2 text-base font-semibold">
              <FlaskConical className="h-4 w-4" /> Preview
            </CardTitle>
            <CardDescription>
              What this profile would do to these releases. Updates as you edit, unsaved changes included.
              Rules about size, grabs, the file itself or availability need something to judge — supply it below.
            </CardDescription>
          </div>
          <div className="flex h-6 items-center text-xs text-muted-foreground">
            {loading && <span className="flex items-center gap-1.5"><Loader2 className="h-3.5 w-3.5 animate-spin" /> Evaluating…</span>}
          </div>
        </div>
      </CardHeader>

      <CardContent className="space-y-4">
        <SettingGroup>
          <SettingBlock
            label="Release names"
            description="One per line. These are also what the per-rule counts on the Rules tab are measured against."
          >
            <textarea
              value={sampleInput}
              onChange={(e) => onSampleInputChange(e.target.value)}
              rows={5}
              spellCheck={false}
              className="w-full resize-y rounded-md border border-input bg-background p-2.5 font-mono text-xs leading-relaxed focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
              aria-label="Release names"
            />
          </SettingBlock>

          <SettingRow
            label="Judge as"
            htmlFor="preview-kind"
            description="Picks which scoped rules and per-kind NZB limits apply."
          >
            <select
              id="preview-kind"
              className={cn(selectClass, "w-44")}
              value={kind}
              onChange={(e) => onKindChange(e.target.value)}
            >
              {RULE_SCOPES.map((s) => <option key={s.key} value={s.key}>{s.label}</option>)}
            </select>
          </SettingRow>

          <SettingRow
            label="Match against a title"
            htmlFor="preview-target"
            description="Optional. Scores how closely each name matches the title you are pretending to have searched for, and rejects the ones that fall short."
          >
            <Input
              id="preview-target"
              value={targetTitle}
              onChange={(e) => onTargetTitleChange(e.target.value)}
              placeholder="The Matrix"
              className="h-9 w-44"
            />
          </SettingRow>

        </SettingGroup>

        <SampleRelease
          value={sample}
          onChange={onSampleChange}
          open={sampleOpen}
          onOpenChange={setSampleOpen}
        />

        {error && (
          <div className="rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
            {error}
          </div>
        )}

        {ordered.length > 0 && aggregates?.length > 0 && (
          <div className="space-y-2 rounded-lg border border-border/60 bg-card/40 px-3 py-2.5">
            <p className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
              Result-set conditions
            </p>
            {groupAggregates(aggregates).map((group, i) => (
              <div key={i} className="space-y-0.5">
                <code className="block font-mono text-[11px] text-foreground">{group.source}</code>
                {group.reports.map((agg, j) => (
                  <div key={j} className={cn("space-y-0.5", agg.release && "pl-3")}>
                    <div className="flex flex-wrap items-baseline gap-x-2">
                      {agg.release && (
                        <span className="truncate font-mono text-[10px] text-muted-foreground">
                          for {agg.release}
                        </span>
                      )}
                      <span className={cn(
                        "text-[11px]",
                        !agg.known || agg.count === 0 ? "text-muted-foreground" : "text-emerald-600 dark:text-emerald-500",
                      )}>
                        {!agg.known
                          ? "cannot be judged from these releases"
                          : agg.count === 0
                            ? "matches nothing in this set"
                            : `matches ${agg.count === 1 ? "1 release" : `${agg.count} releases`}`}
                      </span>
                    </div>
                    {(agg.matched || []).map((title, k) => (
                      <p key={k} className="truncate pl-3 font-mono text-[10px] text-muted-foreground/80">{title}</p>
                    ))}
                  </div>
                ))}
              </div>
            ))}
            <p className="max-w-prose pt-0.5 text-[11px] text-muted-foreground/80">
              Counted over the whole set before any rule fires — these are the values count(), exists() and
              none() read. A question comparing against current.* is counted once per release, so it is
              listed from each release's own point of view.
            </p>
          </div>
        )}

        {ordered.length > 0 && (
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <Label className="text-xs font-normal text-muted-foreground">
                {offered} of {ordered.length} would be offered
              </Label>
            </div>
            {ordered.map((result) => (
              <ResultRow
                key={result.idx}
                result={result}
                expanded={Boolean(expanded[result.idx])}
                onToggle={() => setExpanded((prev) => ({ ...prev, [result.idx]: !prev[result.idx] }))}
              />
            ))}
          </div>
        )}

        {!error && ordered.length === 0 && !loading && (
          <p className="text-sm text-muted-foreground">Add a release name above to see what this profile does.</p>
        )}
      </CardContent>
    </Card>
  )
}
