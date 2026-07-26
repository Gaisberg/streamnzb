import React, { useEffect, useMemo, useState } from "react"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { Copy, Plus, SlidersHorizontal, Trash2, TriangleAlert } from "lucide-react"
import { ProfileEditor } from "@/components/ProfileEditor"
import { ExplainBench } from "@/components/ExplainBench"
import { CONTENT_KINDS, defaultProfile } from "@/lib/profiles"
import { cn } from "@/lib/utils"

// summarize gives each profile card a one-line read of what it does.
function summarize(profile) {
  const ranking = profile.ranking || {}
  const bits = []
  const enabled = Object.entries(ranking.resolutions || {})
    .filter(([key, on]) => on && key !== "unknown")
    .map(([key]) => (key === "2160p" ? "4K" : key))
  if (enabled.length) bits.push(enabled.join(" / "))
  if (ranking.options?.remove_trash !== false) bits.push("no low-quality rips")
  const blocked = Object.values(ranking.attributes || {}).filter((p) => p && p.fetch === false).length
  if (blocked) bits.push(`${blocked} blocked`)
  if (ranking.languages?.required?.length) bits.push(ranking.languages.required.join(", "))
  return bits.join(" \u00b7 ") || "No restrictions"
}

// profileUsage maps a profile name to where it is actually applied. A profile
// that appears nowhere here never runs, whatever its settings say.
function profileUsage(streams = {}) {
  const usage = {}
  const note = (name, label) => {
    const key = (name || "").trim().toLowerCase()
    if (!key) return
    if (!usage[key]) usage[key] = []
    if (!usage[key].includes(label)) usage[key].push(label)
  }

  Object.entries(streams).forEach(([streamName, stream]) => {
    // AIOStreams mode returns everything unfiltered, so nothing is applied.
    if (stream?.filter_sorting_mode === "aiostreams") return
    note(stream?.filter_profile_name, `${streamName} · all content`)
    Object.entries(stream?.filter_profile_by_type || {}).forEach(([kind, name]) => {
      const label = CONTENT_KINDS.find((k) => k.key === kind)?.label || kind
      note(name, `${streamName} · ${label.toLowerCase()}`)
    })
  })
  return usage
}

function uniqueName(profiles, base) {
  const taken = new Set(profiles.map((p) => p.name.trim().toLowerCase()))
  if (!taken.has(base.trim().toLowerCase())) return base
  let n = 2
  while (taken.has(`${base} ${n}`.toLowerCase())) n += 1
  return `${base} ${n}`
}

export function FiltersPage({ config, onSave, isSaving, saveStatus }) {
  const profiles = useMemo(() => config?.filter_profiles || [], [config])
  const usage = useMemo(() => profileUsage(config?.streams || {}), [config])
  const anyInUse = Object.keys(usage).length > 0
  const [selected, setSelected] = useState(0)
  const [draft, setDraft] = useState(null)
  const [confirmDelete, setConfirmDelete] = useState(null)
  const [nameError, setNameError] = useState("")

  // Adopt the saved profile whenever the selection or the config changes, so
  // the editor never shows a stale draft after a save elsewhere.
  useEffect(() => {
    const current = profiles[selected]
    setDraft(current ? structuredClone(current) : null)
    setNameError("")
  }, [profiles, selected])

  const dirty = useMemo(() => {
    const current = profiles[selected]
    if (!current || !draft) return false
    return JSON.stringify(current) !== JSON.stringify(draft)
  }, [profiles, selected, draft])

  const commit = (next, nextIndex) => {
    onSave(next)
    if (typeof nextIndex === "number") setSelected(nextIndex)
  }

  const addProfile = () => {
    const name = uniqueName(profiles, "New Profile")
    commit([...profiles, defaultProfile(name)], profiles.length)
  }

  const duplicateProfile = (index) => {
    const source = profiles[index]
    const copy = structuredClone(source)
    copy.name = uniqueName(profiles, `${source.name} copy`)
    if (copy.ranking) copy.ranking.name = copy.name
    commit([...profiles, copy], profiles.length)
  }

  const deleteProfile = (index) => {
    const next = profiles.filter((_, i) => i !== index)
    commit(next, Math.max(0, Math.min(selected, next.length - 1)))
    setConfirmDelete(null)
  }

  const saveDraft = () => {
    const name = (draft.name || "").trim()
    if (!name) { setNameError("Name is required."); return }
    const clash = profiles.some((p, i) => i !== selected && p.name.trim().toLowerCase() === name.toLowerCase())
    if (clash) { setNameError("Another profile already uses this name."); return }

    const next = profiles.map((p, i) => (i === selected ? { ...draft, name, ranking: { ...(draft.ranking || {}), name } } : p))
    setNameError("")
    commit(next)
  }

  const toggleKind = (kind) => {
    const current = draft.applies_to || []
    setDraft({
      ...draft,
      applies_to: current.includes(kind) ? current.filter((k) => k !== kind) : [...current, kind],
    })
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="flex items-center gap-2 text-lg font-medium text-foreground">
            <SlidersHorizontal className="h-4 w-4" /> Filters
          </h2>
          <p className="text-sm text-muted-foreground">
            A profile decides which releases you are offered, how they are scored, and what order they arrive in.
            Assign one to a stream from the Streams page.
          </p>
        </div>
        <Button onClick={addProfile} size="sm">
          <Plus className="mr-2 h-4 w-4" /> New profile
        </Button>
      </div>

      {profiles.length > 0 && !anyInUse && (
        <div className="flex items-start gap-2 rounded-lg border border-amber-500/30 bg-amber-500/5 px-3.5 py-2.5">
          <TriangleAlert className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
          <p className="text-xs text-muted-foreground">
            None of these profiles are being applied. A profile only takes effect once a stream selects it, on the
            Streams page. Streams set to AIOStreams return every release unfiltered and ignore profiles.
          </p>
        </div>
      )}

      {profiles.length === 0 ? (
        <Card className="border border-dashed border-border bg-card">
          <CardContent className="py-10 text-center">
            <p className="text-sm text-muted-foreground">No filter profiles yet.</p>
            <Button onClick={addProfile} size="sm" className="mt-3">
              <Plus className="mr-2 h-4 w-4" /> Create one
            </Button>
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-6 lg:grid-cols-[280px_minmax(0,1fr)]">
          <div className="space-y-2">
            {profiles.map((profile, index) => (
              <button
                key={`${profile.name}-${index}`}
                type="button"
                onClick={() => setSelected(index)}
                className={cn(
                  "w-full rounded-md border px-3 py-2.5 text-left transition-colors",
                  index === selected
                    ? "border-primary/50 bg-primary/5"
                    : "border-border hover:border-muted-foreground/40"
                )}
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="truncate text-sm font-medium">{profile.name}</span>
                  {index === selected && dirty && (
                    <Badge variant="outline" className="shrink-0 text-[10px]">unsaved</Badge>
                  )}
                </div>
                <div className="mt-0.5 truncate text-xs text-muted-foreground">{summarize(profile)}</div>
                <div className="mt-1 truncate text-[11px]">
                  {usage[profile.name.trim().toLowerCase()]
                    ? <span className="text-emerald-600 dark:text-emerald-500">
                        In use · {usage[profile.name.trim().toLowerCase()].join(", ")}
                      </span>
                    : <span className="text-muted-foreground/70">Not in use</span>}
                </div>
                {profile.applies_to?.length > 0 && (
                  <div className="mt-1.5 flex flex-wrap gap-1">
                    {profile.applies_to.map((kind) => (
                      <Badge key={kind} variant="secondary" className="text-[10px]">
                        {CONTENT_KINDS.find((k) => k.key === kind)?.label || kind}
                      </Badge>
                    ))}
                  </div>
                )}
              </button>
            ))}
          </div>

          {draft && (
            <div className="min-w-0 space-y-4">
              <Card className="border border-border bg-card">
                <CardHeader className="pb-3">
                  <CardTitle className="text-base font-semibold">Profile</CardTitle>
                  <CardDescription>Give it a name and choose what it applies to.</CardDescription>
                </CardHeader>
                <CardContent className="space-y-4">
                  <div className="space-y-1.5">
                    <Label htmlFor="profile-name">Name</Label>
                    <Input
                      id="profile-name"
                      value={draft.name || ""}
                      onChange={(e) => { setDraft({ ...draft, name: e.target.value }); setNameError("") }}
                    />
                    {nameError && <p className="text-xs text-destructive">{nameError}</p>}
                  </div>

                  <div className="space-y-1.5">
                    <Label>Applies to</Label>
                    <p className="text-xs text-muted-foreground">
                      Leave these off to use this profile for anything.
                    </p>
                    <div className="flex flex-wrap gap-2 pt-1">
                      {CONTENT_KINDS.map((kind) => {
                        const on = (draft.applies_to || []).includes(kind.key)
                        return (
                          <button
                            key={kind.key}
                            type="button"
                            onClick={() => toggleKind(kind.key)}
                            className={cn(
                              "rounded-md border px-3 py-1.5 text-xs transition-colors",
                              on
                                ? "border-primary/40 bg-primary/10 text-foreground"
                                : "border-border text-muted-foreground hover:text-foreground"
                            )}
                          >
                            {kind.label}
                          </button>
                        )
                      })}
                    </div>
                  </div>

                  <div className="flex flex-wrap items-center gap-2 pt-1">
                    <Button onClick={saveDraft} disabled={!dirty || isSaving} size="sm">
                      {isSaving ? "Saving\u2026" : "Save changes"}
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => setDraft(structuredClone(profiles[selected]))}
                      disabled={!dirty}
                    >
                      Discard
                    </Button>
                    <div className="flex-1" />
                    <Button variant="ghost" size="sm" onClick={() => duplicateProfile(selected)}>
                      <Copy className="mr-2 h-3.5 w-3.5" /> Duplicate
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="text-destructive hover:text-destructive"
                      onClick={() => setConfirmDelete(selected)}
                    >
                      <Trash2 className="mr-2 h-3.5 w-3.5" /> Delete
                    </Button>
                  </div>
                  {saveStatus?.message && (
                    <p className={cn("text-xs", saveStatus.type === "error" ? "text-destructive" : "text-muted-foreground")}>
                      {saveStatus.message}
                    </p>
                  )}
                </CardContent>
              </Card>

              <ProfileEditor profile={draft} onChange={setDraft} />

              <ExplainBench profile={draft} />
            </div>
          )}
        </div>
      )}

      <ConfirmDialog
        open={confirmDelete !== null}
        onOpenChange={(open) => { if (!open) setConfirmDelete(null) }}
        title="Delete filter profile"
        description={
          confirmDelete !== null
            ? `Delete \u201c${profiles[confirmDelete]?.name}\u201d? Any stream using it will stop filtering.`
            : ""
        }
        confirmLabel="Delete"
        onConfirm={() => deleteProfile(confirmDelete)}
      />
    </div>
  )
}
