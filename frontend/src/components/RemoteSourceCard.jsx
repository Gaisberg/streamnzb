import React, { useState } from "react"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { Link2, Loader2, RefreshCw, Unlink } from "lucide-react"
import { checkForUpdate } from "@/lib/remoteProfiles"
import { checkFormatForUpdate } from "@/lib/formatProfiles"
import { checkDefineLibraryForUpdate } from "@/lib/defineLibraries"
import { sourceHost } from "@/lib/shareCodes"

function formatWhen(value) {
  if (!value) return "never"
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? "never" : date.toLocaleString()
}

// DiffLines renders one side of a rule change as the editor's one-line text
// form, colored the way a diff reads.
function DiffLines({ label, lines, tone }) {
  if (!lines.length) return null
  return (
    <div className="space-y-1">
      <p className="text-xs font-medium text-muted-foreground">{label}</p>
      <div className="overflow-x-auto rounded-md border border-border bg-muted/30 p-2 font-mono text-[11px] leading-relaxed">
        {lines.map((line, i) => (
          <div key={i} className={tone}>{line}</div>
        ))}
      </div>
    </div>
  )
}

// The filter diff is rule-shaped: updated, added, removed, and the rules the
// merge kept because they are the user's own.
function FilterDiff({ pending }) {
  const diff = pending.diff
  return (
    <>
      {diff?.preset && (
        <p className="text-xs">
          Preset: <span className="font-mono">{diff.preset.from}</span> → <span className="font-mono">{diff.preset.to}</span>
        </p>
      )}
      {diff?.changed.length > 0 && (
        <div className="space-y-1">
          <p className="text-xs font-medium text-muted-foreground">Updated</p>
          <div className="overflow-x-auto rounded-md border border-border bg-muted/30 p-2 font-mono text-[11px] leading-relaxed">
            {diff.changed.map((change, i) => (
              <div key={i}>
                <div className="text-destructive">- {change.before}</div>
                <div className="text-emerald-600 dark:text-emerald-500">+ {change.after}</div>
              </div>
            ))}
          </div>
        </div>
      )}
      <DiffLines label="Added" lines={diff?.added || []} tone="text-emerald-600 dark:text-emerald-500" />
      <DiffLines label="Removed" lines={diff?.removed || []} tone="text-destructive" />
      <DiffLines
        label="Your own rules, kept"
        lines={(pending?.keptLocal || []).map((rule) => rule.name)}
        tone="text-muted-foreground"
      />
    </>
  )
}

// The format diff shows each template whole. They are short enough to read,
// and a line diff of Go template syntax would obscure more than it shows.
function FormatDiff({ pending }) {
  return (pending.diff?.changes || []).map((change, i) => (
    <div key={i} className="space-y-1">
      <p className="text-xs font-medium text-muted-foreground">{change.label}</p>
      <div className="space-y-1 rounded-md border border-border bg-muted/30 p-2 font-mono text-[11px] leading-relaxed">
        <div className="whitespace-pre-wrap break-all text-destructive">- {change.before}</div>
        <div className="whitespace-pre-wrap break-all text-emerald-600 dark:text-emerald-500">+ {change.after}</div>
      </div>
    </div>
  ))
}

// What differs between the profile kinds that can be linked: how to check for
// an update, how to render its diff, and how to describe the contract.
const FLAVORS = {
  filter: {
    check: checkForUpdate,
    noun: "profile",
    blurb: "Refresh fetches the current share code and shows what would change before anything is applied. Rules the maintainer owns are updated in place; rules you added under your own names are kept.",
    dialogNote: "Nothing changes until you apply. Your own rules — names the maintainer never used — stay yours.",
    Diff: FilterDiff,
  },
  format: {
    check: checkFormatForUpdate,
    noun: "profile",
    blurb: "Refresh fetches the current share code and shows what would change before anything is applied. Applying replaces the templates with the maintainer's current version.",
    dialogNote: "Nothing changes until you apply. The profile keeps its name; the templates become the maintainer's.",
    Diff: FormatDiff,
  },
  // A library diff is rule-shaped like the filter one, but the contract is
  // wholesale: the rules are the maintainer's, and a lasting override belongs
  // in the profile, whose own rule shadows the library's.
  library: {
    check: checkDefineLibraryForUpdate,
    noun: "library",
    blurb: "Refresh fetches the current file and shows what would change before anything is applied. Applying replaces the defines with the maintainer's current version; to override one for good, write a profile rule under the same name — it shadows the library's.",
    dialogNote: "Nothing changes until you apply. The library keeps its name; the defines become the maintainer's.",
    Diff: FilterDiff,
  },
}

// RemoteSourceCard shows what a linked profile is subscribed to, and owns the
// whole Refresh flow: fetch, compare, and the diff dialog that gates applying.
// Changes go through onChange — the same draft the editor writes — so an
// applied update rides the page's normal auto-save, validation included.
export function RemoteSourceCard({ profile, onChange, flavor = "filter" }) {
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState(null)
  const [pending, setPending] = useState(null)
  const [confirmUnlink, setConfirmUnlink] = useState(false)

  const { check, noun, blurb, dialogNote, Diff } = FLAVORS[flavor]
  const source = profile.source || {}
  const host = sourceHost(source.url)
  const now = () => new Date().toISOString()

  const refresh = async () => {
    setBusy(true)
    setNote(null)
    try {
      const result = await check(profile)
      if (result.status === "current") {
        onChange({ ...profile, source: { ...source, checked_at: now() } })
        setNote({ type: "ok", msg: "Up to date." })
      } else {
        setPending(result)
      }
    } catch (err) {
      setNote({ type: "error", msg: err?.message || "The check failed." })
    } finally {
      setBusy(false)
    }
  }

  const applyUpdate = () => {
    const stamp = now()
    onChange({
      ...pending.merged,
      source: { ...source, code: pending.code, checked_at: stamp, applied_at: stamp },
    })
    setPending(null)
    setNote({ type: "ok", msg: "Update applied." })
  }

  const unlink = () => {
    const next = { ...profile }
    delete next.source
    onChange(next)
    setConfirmUnlink(false)
  }

  const renamed = pending && pending.remoteName &&
    pending.remoteName.trim().toLowerCase() !== (profile.name || "").trim().toLowerCase()

  return (
    <Card className="border border-border bg-card">
      <CardContent className="space-y-2 py-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="min-w-0">
            <p className="flex items-center gap-1.5 text-sm font-medium">
              <Link2 className="h-3.5 w-3.5 shrink-0" /> Linked to {host || "a remote source"}
            </p>
            <p className="truncate text-xs text-muted-foreground" title={source.url}>{source.url}</p>
            <p className="text-[11px] text-muted-foreground/70">
              Checked {formatWhen(source.checked_at)} · applied {formatWhen(source.applied_at)}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Button variant="outline" size="sm" disabled={busy} onClick={refresh}>
              {busy
                ? <Loader2 className="mr-2 h-3.5 w-3.5 animate-spin" />
                : <RefreshCw className="mr-2 h-3.5 w-3.5" />}
              Refresh
            </Button>
            <Button variant="ghost" size="sm" disabled={busy} onClick={() => setConfirmUnlink(true)}>
              <Unlink className="mr-2 h-3.5 w-3.5" /> Unlink
            </Button>
          </div>
        </div>
        <p className="text-xs text-muted-foreground">{blurb}</p>
        {note && (
          <p className={note.type === "error" ? "text-xs text-destructive" : "text-xs text-emerald-600 dark:text-emerald-500"}>
            {note.msg}
          </p>
        )}
      </CardContent>

      <Dialog open={pending !== null} onOpenChange={(open) => { if (!open) setPending(null) }}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>Update from {host}</DialogTitle>
            <DialogDescription>{dialogNote}</DialogDescription>
          </DialogHeader>

          <div className="max-h-[55vh] space-y-3 overflow-y-auto pr-1">
            {renamed && (
              <p className="text-xs text-muted-foreground">
                The maintainer calls this {noun} “{pending.remoteName}”. Your {noun} keeps its name, “{profile.name}”.
              </p>
            )}
            {pending && <Diff pending={pending} />}
          </div>

          <DialogFooter className="flex-row items-center justify-end gap-2 sm:space-x-0">
            <Button type="button" variant="outline" size="sm" onClick={() => setPending(null)}>Cancel</Button>
            <Button type="button" size="sm" onClick={applyUpdate}>Apply update</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={confirmUnlink}
        onOpenChange={setConfirmUnlink}
        title={`Unlink ${noun}`}
        description={`Unlink “${profile.name}” from ${host}? The ${noun} keeps its current state and becomes fully yours; Refresh goes away until it is imported from a URL again.`}
        confirmLabel="Unlink"
        onConfirm={unlink}
      />
    </Card>
  )
}
