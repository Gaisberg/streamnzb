import React, { useState } from "react"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Checkbox } from "@/components/ui/checkbox"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { Link2, Loader2, RefreshCw, Unlink } from "lucide-react"
import { applySelectedChanges, changeKeys, checkForUpdate } from "@/lib/remoteProfiles"
import { applySelectedFormatChanges, checkFormatForUpdate, formatChangeKeys } from "@/lib/formatProfiles"
import { checkDefineLibraryForUpdate } from "@/lib/defineLibraries"
import { sourceHost } from "@/lib/shareCodes"

function formatWhen(value) {
  if (!value) return "never"
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? "never" : date.toLocaleString()
}

// ChangeRow puts one change behind a checkbox: what is ticked is what Apply
// writes. The row is a label wrapping the box, so the whole line toggles.
function ChangeRow({ entryKey, selected, onToggle, children }) {
  return (
    <label className="flex cursor-pointer items-start gap-2 rounded px-1 py-0.5 hover:bg-muted/40">
      <Checkbox
        checked={selected.has(entryKey)}
        onCheckedChange={() => onToggle(entryKey)}
        className="mt-[3px] shrink-0"
      />
      <span className="min-w-0 flex-1">{children}</span>
    </label>
  )
}

// DiffLines renders one side of a rule change as the editor's one-line text
// form, colored the way a diff reads. With a selection it is a list of
// decisions, one checkbox each; without one it is informational — the rules
// the merge kept because they are the user's own decide nothing.
function DiffLines({ label, entries, tone, selection }) {
  if (!entries.length) return null
  return (
    <div className="space-y-1">
      <p className="text-xs font-medium text-muted-foreground">{label}</p>
      <div className="overflow-x-auto rounded-md border border-border bg-muted/30 p-2 font-mono text-[11px] leading-relaxed">
        {entries.map((entry, i) => (
          selection
            ? (
              <ChangeRow key={entry.key} entryKey={entry.key} {...selection}>
                <div className={tone}>{entry.line}</div>
              </ChangeRow>
            )
            : <div key={i} className={tone}>{entry.line}</div>
        ))}
      </div>
    </div>
  )
}

// ScoringChange is the attribute-scoring move as one decision: the map is
// replaced whole, so it gets one checkbox, shown as the config's own field
// names on either side. An empty side is the preset's scoring, which is what a
// profile has when it carries none.
function ScoringChange({ change, selection }) {
  const lines = (text) => (text ? text.split("\n") : ["(the preset's own scoring)"])
  return (
    <div className="space-y-1">
      <p className="text-xs font-medium text-muted-foreground">Attribute scoring</p>
      <div className="overflow-x-auto rounded-md border border-border bg-muted/30 p-2 font-mono text-[11px] leading-relaxed">
        <ChangeRow entryKey={change.key} {...selection}>
          {lines(change.before).map((line, i) => <div key={`b${i}`} className="text-destructive">- {line}</div>)}
          {lines(change.after).map((line, i) => <div key={`a${i}`} className="text-emerald-600 dark:text-emerald-500">+ {line}</div>)}
        </ChangeRow>
      </div>
    </div>
  )
}

// The filter diff is rule-shaped: updated, added, removed, and the rules the
// merge kept because they are the user's own — plus the preset and scoring
// moves, which are each one decision.
function FilterDiff({ pending, selection }) {
  const diff = pending.diff
  return (
    <>
      {diff?.preset && (
        <div className="rounded-md border border-border bg-muted/30 p-2 text-xs">
          <ChangeRow entryKey={diff.preset.key} {...selection}>
            Preset: <span className="font-mono">{diff.preset.from}</span> → <span className="font-mono">{diff.preset.to}</span>
          </ChangeRow>
        </div>
      )}
      {diff?.scoring && <ScoringChange change={diff.scoring} selection={selection} />}
      {diff?.changed.length > 0 && (
        <div className="space-y-1">
          <p className="text-xs font-medium text-muted-foreground">Updated</p>
          <div className="overflow-x-auto rounded-md border border-border bg-muted/30 p-2 font-mono text-[11px] leading-relaxed">
            {diff.changed.map((change) => (
              <ChangeRow key={change.key} entryKey={change.key} {...selection}>
                <div className="text-destructive">- {change.before}</div>
                <div className="text-emerald-600 dark:text-emerald-500">+ {change.after}</div>
              </ChangeRow>
            ))}
          </div>
        </div>
      )}
      <DiffLines label="Added" entries={diff?.added || []} tone="text-emerald-600 dark:text-emerald-500" selection={selection} />
      <DiffLines label="Removed" entries={diff?.removed || []} tone="text-destructive" selection={selection} />
      <DiffLines
        label="Your own rules, kept"
        entries={(pending?.keptLocal || []).map((rule) => ({ line: rule.name }))}
        tone="text-muted-foreground"
      />
    </>
  )
}

// The format diff shows each template whole. They are short enough to read,
// and a line diff of Go template syntax would obscure more than it shows.
function FormatDiff({ pending, selection }) {
  return (pending.diff?.changes || []).map((change) => (
    <div key={change.key} className="space-y-1">
      <p className="text-xs font-medium text-muted-foreground">{change.label}</p>
      <div className="rounded-md border border-border bg-muted/30 p-2 font-mono text-[11px] leading-relaxed">
        <ChangeRow entryKey={change.key} {...selection}>
          <div className="whitespace-pre-wrap break-all text-destructive">- {change.before}</div>
          <div className="whitespace-pre-wrap break-all text-emerald-600 dark:text-emerald-500">+ {change.after}</div>
        </ChangeRow>
      </div>
    </div>
  ))
}

// What differs between the profile kinds that can be linked: how to check for
// an update, how to render its diff, which decisions the diff offers and how
// to narrow the merge to the ticked ones, and how to describe the contract.
const FLAVORS = {
  filter: {
    check: checkForUpdate,
    keys: changeKeys,
    apply: applySelectedChanges,
    noun: "profile",
    blurb: "Refresh fetches the current share code and shows what would change before anything is applied. Rules the maintainer owns are updated in place; rules you added under your own names are kept.",
    dialogNote: "Nothing changes until you apply, and only the changes you tick are applied. Your own rules — names the maintainer never used — stay yours.",
    Diff: FilterDiff,
  },
  format: {
    check: checkFormatForUpdate,
    keys: formatChangeKeys,
    apply: applySelectedFormatChanges,
    noun: "profile",
    blurb: "Refresh fetches the current share code and shows what would change before anything is applied. Applying replaces the templates with the maintainer's current version.",
    dialogNote: "Nothing changes until you apply, and only the templates you tick are applied. The profile keeps its name.",
    Diff: FormatDiff,
  },
  // A library diff is rule-shaped like the filter one, but the contract is
  // wholesale: the rules are the maintainer's, and a lasting override belongs
  // in the profile, whose own rule shadows the library's.
  library: {
    check: checkDefineLibraryForUpdate,
    keys: changeKeys,
    apply: applySelectedChanges,
    noun: "library",
    blurb: "Refresh fetches the current file and shows what would change before anything is applied. Applying replaces the defines with the maintainer's current version; to override one for good, write a profile rule under the same name — it shadows the library's.",
    dialogNote: "Nothing changes until you apply, and only the changes you tick are applied. The library keeps its name; the defines become the maintainer's.",
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
  // The changes ticked in the diff dialog, by key. Everything arrives ticked:
  // taking the whole update is the common case, and skipping one is a
  // deliberate act.
  const [selected, setSelected] = useState(new Set())
  const [confirmUnlink, setConfirmUnlink] = useState(false)

  const { check, keys, apply, noun, blurb, dialogNote, Diff } = FLAVORS[flavor]
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
        setSelected(new Set(keys(result.diff)))
        setPending(result)
      }
    } catch (err) {
      setNote({ type: "error", msg: err?.message || "The check failed." })
    } finally {
      setBusy(false)
    }
  }

  const allKeys = pending ? keys(pending.diff) : []
  const allSelected = allKeys.length > 0 && selected.size === allKeys.length

  const toggle = (key) => setSelected((prev) => {
    const next = new Set(prev)
    if (!next.delete(key)) next.add(key)
    return next
  })
  const toggleAll = () => setSelected(allSelected ? new Set() : new Set(allKeys))

  // The snapshot stored is the upstream code in full even when only part of it
  // was applied: it records what upstream *is*, which is what lets the next
  // merge tell a rule the user added from one the maintainer deleted.
  const applyUpdate = () => {
    const stamp = now()
    onChange({
      ...apply(profile, pending.merged, pending.diff, selected),
      source: { ...source, code: pending.code, checked_at: stamp, applied_at: stamp },
    })
    const applied = selected.size
    const total = allKeys.length
    setPending(null)
    setNote({
      type: "ok",
      msg: applied === total
        ? "Update applied."
        : `Applied ${applied} of ${total} changes; the rest are offered again on the next refresh.`,
    })
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

          <div className="flex items-center justify-between gap-2 border-b border-border pb-2">
            <p className="text-xs text-muted-foreground">
              {selected.size} of {allKeys.length} change{allKeys.length === 1 ? "" : "s"} selected
            </p>
            <Button type="button" variant="ghost" size="sm" className="h-7 px-2 text-xs" onClick={toggleAll}>
              {allSelected ? "Select none" : "Select all"}
            </Button>
          </div>

          <div className="max-h-[55vh] space-y-3 overflow-y-auto pr-1">
            {renamed && (
              <p className="text-xs text-muted-foreground">
                The maintainer calls this {noun} “{pending.remoteName}”. Your {noun} keeps its name, “{profile.name}”.
              </p>
            )}
            {pending && <Diff pending={pending} selection={{ selected, onToggle: toggle }} />}
          </div>

          <DialogFooter className="flex-row items-center justify-end gap-2 sm:space-x-0">
            <Button type="button" variant="outline" size="sm" onClick={() => setPending(null)}>Cancel</Button>
            <Button type="button" size="sm" disabled={selected.size === 0} onClick={applyUpdate}>
              {allSelected ? "Apply update" : `Apply ${selected.size} change${selected.size === 1 ? "" : "s"}`}
            </Button>
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
