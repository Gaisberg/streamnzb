import React, { useState } from "react"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { ChevronDown, FlaskConical, Loader2 } from "lucide-react"
import { apiFetch } from "@/api"
import { ATTRIBUTE_LABELS, formatScore } from "@/lib/profiles"
import { cn } from "@/lib/utils"

const SAMPLE_TITLES = [
  "The Matrix 1999 2160p UHD BluRay REMUX DV HDR HEVC TrueHD 7.1 Atmos-FraMeSToR",
  "The Matrix 1999 1080p BluRay DDP5.1 x264-GRP",
  "The Matrix 1999 720p HDTV XviD-TRASH",
  "The.Matrix.1999.1080p.WEBRip.HDCAM.x264",
]

// clauseLabel turns a scoring clause into something readable:
// "attribute:remux" -> "Remux", "pattern:IMAX" -> "Pattern IMAX".
function clauseLabel(source = "") {
  if (source.startsWith("attribute:")) {
    const key = source.slice("attribute:".length)
    return ATTRIBUTE_LABELS[key] || key
  }
  if (source.startsWith("pattern:")) return `Pattern ${source.slice("pattern:".length)}`
  if (source === "preferred_pattern") return "Preferred pattern"
  if (source === "preferred_language") return "Preferred language"
  return source
}

// reasonLabel turns a rejection reason into plain language.
function reasonLabel(reason = "") {
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
      result.fetch ? "border-border/60 bg-card/40" : "border-destructive/25 bg-destructive/[0.03]"
    )}>
      <button type="button" onClick={onToggle} className="flex w-full items-start gap-3 px-3.5 py-3 text-left">
        <ChevronDown className={cn(
          "mt-0.5 h-4 w-4 shrink-0 text-muted-foreground transition-transform",
          !expanded && "-rotate-90"
        )} />
        <div className="min-w-0 flex-1 space-y-1.5">
          <div className="truncate font-mono text-xs text-foreground">{result.title}</div>
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge variant={result.fetch ? "default" : "destructive"} className="h-5 text-[10px] font-normal">
              {result.fetch ? "Eligible" : "Rejected"}
            </Badge>
            <span className={cn(
              "font-mono text-xs tabular-nums",
              result.rank > 0 ? "text-emerald-500" : result.rank < 0 ? "text-destructive" : "text-muted-foreground"
            )}>
              {formatScore(result.rank)}
            </span>
            {result.resolution && result.resolution !== "unknown" && (
              <Badge variant="outline" className="h-5 text-[10px] font-normal">{result.resolution}</Badge>
            )}
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
        <div className="border-t border-border/60 px-3.5 py-3">
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
                        clause.rank >= 0 ? "left-1/2 bg-emerald-500/70" : "right-1/2 bg-destructive/70"
                      )}
                      style={{ width: `${(Math.abs(clause.rank) / peak) * 50}%` }}
                    />
                    <div className="absolute inset-y-0 left-1/2 w-px bg-border" />
                  </div>
                  <span className={cn(
                    "w-16 shrink-0 text-right font-mono text-xs tabular-nums",
                    clause.rank > 0 ? "text-emerald-500" : clause.rank < 0 ? "text-destructive" : "text-muted-foreground"
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
        </div>
      )}
    </div>
  )
}

// ExplainBench runs release names through the profile being edited and shows
// why each one scored what it did, including unsaved changes.
export function ExplainBench({ profile }) {
  const [input, setInput] = useState(SAMPLE_TITLES.join("\n"))
  const [targetTitle, setTargetTitle] = useState("")
  const [results, setResults] = useState(null)
  const [expanded, setExpanded] = useState({})
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState("")

  const run = async () => {
    const titles = input.split("\n").map((t) => t.trim()).filter(Boolean)
    if (titles.length === 0) {
      setError("Add at least one release name.")
      return
    }
    setLoading(true)
    setError("")
    try {
      const data = await apiFetch("/api/ranking/explain", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ titles, profile, target_title: targetTitle.trim() || undefined }),
      })
      setResults(data?.results || [])
      setExpanded({})
    } catch (err) {
      setError(err.message || "Could not evaluate those release names.")
      setResults(null)
    } finally {
      setLoading(false)
    }
  }

  // Eligible releases first, then by score, matching what the addon returns.
  const ordered = results
    ? results.map((r, i) => ({ ...r, idx: i })).sort((a, b) => {
        if (a.fetch !== b.fetch) return a.fetch ? -1 : 1
        return (b.rank || 0) - (a.rank || 0)
      })
    : []

  const eligible = ordered.filter((r) => r.fetch).length

  return (
    <Card className="border border-border bg-card">
      <CardHeader className="pb-3">
        <CardTitle className="flex items-center gap-2 text-base font-semibold">
          <FlaskConical className="h-4 w-4" /> Try it out
        </CardTitle>
        <CardDescription>
          Paste release names to see how this profile scores them, and why. Unsaved changes are included.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="space-y-2">
          <Label htmlFor="bench-titles">Release names, one per line</Label>
          <textarea
            id="bench-titles"
            value={input}
            onChange={(e) => setInput(e.target.value)}
            rows={5}
            spellCheck={false}
            className="w-full rounded-lg border border-input bg-background px-3 py-2 font-mono text-xs shadow-sm outline-none transition-colors focus-visible:border-ring focus-visible:ring-1 focus-visible:ring-ring"
          />
        </div>

        <div className="flex flex-col gap-3 sm:flex-row sm:items-start">
          <div className="flex-1 space-y-1.5">
            <Label htmlFor="bench-target">Match against a title (optional)</Label>
            <Input
              id="bench-target"
              value={targetTitle}
              onChange={(e) => setTargetTitle(e.target.value)}
              placeholder="The Matrix"
              className="h-9"
            />
            <p className="text-xs text-muted-foreground">
              Scores how closely each name matches, and rejects the ones that do not.
            </p>
          </div>
          <Button onClick={run} disabled={loading} className="h-9 sm:mt-6 sm:w-28">
            {loading && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
            {loading ? "Running" : "Run"}
          </Button>
        </div>

        {error && (
          <div className="rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
            {error}
          </div>
        )}

        {results && results.length === 0 && (
          <p className="text-sm text-muted-foreground">No results.</p>
        )}

        {ordered.length > 0 && (
          <div className="space-y-2">
            <p className="text-xs text-muted-foreground">
              {eligible} of {ordered.length} would be offered.
            </p>
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
      </CardContent>
    </Card>
  )
}
