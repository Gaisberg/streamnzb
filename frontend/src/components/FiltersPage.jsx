import React, { useMemo, useState } from "react"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Copy, Import, Share2, SlidersHorizontal, TriangleAlert } from "lucide-react"
import { ProfileManager } from "@/components/ProfileManager"
import { ProfileEditor } from "@/components/ProfileEditor"
import { ExplainBench } from "@/components/ExplainBench"
import {
  CONTENT_KINDS, decodeProfileShareCode, defaultProfile, encodeProfileShareCode, withoutLegacyFields,
} from "@/lib/profiles"

// summarize gives each profile card a one-line read of what it does.
function summarize(profile) {
  const ranking = profile.ranking || {}
  const bits = []
  const enabled = Object.entries(ranking.resolutions || {})
    .filter(([key, on]) => on && key !== "unknown")
    .map(([key]) => (key === "2160p" ? "4K" : key))
  if (enabled.length) bits.push(enabled.join(" / "))
  if (ranking.options?.remove_trash !== false) bits.push("no garbage")
  const blocked = Object.values(ranking.attributes || {}).filter((p) => p && p.fetch === false).length
  if (blocked) bits.push(`${blocked} blocked`)
  if (ranking.languages?.required?.length) bits.push(ranking.languages.required.join(", "))
  return bits.join(" · ") || "No restrictions"
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

// describeDelete spells out the knock-on effect, since deleting a profile
// also clears it from any stream using it.
function describeDelete(profile, usage) {
  const name = profile?.name || ""
  const used = usage[name.trim().toLowerCase()]
  if (!used?.length) {
    return `Delete “${name}”? It is not in use, so nothing else changes.`
  }
  return `Delete “${name}”? It will be cleared from ${used.join(", ")}, which will fall back to returning everything unfiltered.`
}

// The saved profile mirrors its name into the compiled ranking profile, so
// jhin's diagnostics label rejections with the profile the user sees.
function normalizeOnSave(profile) {
  const name = (profile.name || "").trim()
  return { ...profile, name, ranking: { ...(profile.ranking || {}), name } }
}

export function FiltersPage({ config, onSave, isSaving, saveStatus }) {
  const profiles = useMemo(() => config?.filter_profiles || [], [config])
  const usage = useMemo(() => profileUsage(config?.streams || {}), [config])
  const anyInUse = Object.keys(usage).length > 0
  const [exportCode, setExportCode] = useState(null)
  const [exportName, setExportName] = useState("")
  const [exportCopied, setExportCopied] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [importCode, setImportCode] = useState("")
  const [importError, setImportError] = useState("")

  const exportProfile = async (draft) => {
    if (!draft) return
    try {
      setExportCopied(false)
      setExportName(draft.name || "")
      const code = await encodeProfileShareCode(draft)
      setExportCode(code)
      try {
        await navigator.clipboard.writeText(code)
        setExportCopied(true)
      } catch {
        // Clipboard needs a secure context; the dialog still shows the code to copy by hand.
      }
    } catch {
      setExportCode("")
    }
  }

  const importProfile = async () => {
    try {
      const profile = await decodeProfileShareCode(importCode)
      const taken = new Set(profiles.map((p) => p.name.trim().toLowerCase()))
      let name = profile.name.trim() || "Imported Profile"
      if (taken.has(name.toLowerCase())) {
        let n = 2
        while (taken.has(`${name} ${n}`.toLowerCase())) n += 1
        name = `${name} ${n}`
      }
      profile.name = name
      if (profile.ranking) profile.ranking.name = name
      onSave([...profiles, profile])
      setImportOpen(false)
      setImportCode("")
      setImportError("")
    } catch (err) {
      setImportError(err?.message || "Could not read the profile code.")
    }
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
        <Button
          variant="outline"
          size="sm"
          onClick={() => { setImportCode(""); setImportError(""); setImportOpen(true) }}
        >
          <Import className="mr-2 h-4 w-4" /> Import
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

      <ProfileManager
        profiles={profiles}
        onSave={onSave}
        usage={usage}
        summarize={summarize}
        newProfile={defaultProfile}
        describeDelete={describeDelete}
        entityLabel="filter profile"
        emptyText="No filter profiles yet."
        isSaving={isSaving}
        saveStatus={saveStatus}
        normalizeOnSave={normalizeOnSave}
        normalizeOnDuplicate={withoutLegacyFields}
        renderActions={(draft) => (
          <Button variant="ghost" size="sm" onClick={() => exportProfile(draft)}>
            <Share2 className="mr-2 h-3.5 w-3.5" /> Export
          </Button>
        )}
        renderEditor={(draft, setDraft) => (
          <>
            <ProfileEditor profile={draft} onChange={setDraft} />
            <ExplainBench profile={draft} />
          </>
        )}
      />

      <Dialog open={exportCode !== null} onOpenChange={(open) => { if (!open) setExportCode(null) }}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>Export profile</DialogTitle>
            <DialogDescription>
              {exportCode === ""
                ? "This browser cannot generate share codes."
                : `Share this code to hand “${exportName}” to another StreamNZB instance.`}
            </DialogDescription>
          </DialogHeader>
          {exportCode !== "" && (
            <textarea
              readOnly
              value={exportCode || ""}
              onFocus={(e) => e.target.select()}
              rows={5}
              className="w-full resize-none rounded-md border border-input bg-background p-2.5 font-mono text-[11px] leading-relaxed text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
            />
          )}
          <DialogFooter className="flex-row items-center justify-between gap-2 sm:justify-between sm:space-x-0">
            <span className="text-xs text-muted-foreground">{exportCopied ? "Copied to clipboard." : ""}</span>
            <Button
              type="button"
              size="sm"
              disabled={!exportCode}
              onClick={async () => {
                try {
                  await navigator.clipboard.writeText(exportCode)
                  setExportCode(null)
                } catch { /* clipboard unavailable: keep the dialog open for manual copy */ }
              }}
            >
              <Copy className="mr-2 h-3.5 w-3.5" /> Copy code
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={importOpen} onOpenChange={setImportOpen}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>Import profile</DialogTitle>
            <DialogDescription>Paste a profile share code. It is added as a new profile and never overwrites an existing one.</DialogDescription>
          </DialogHeader>
          <textarea
            value={importCode}
            onChange={(e) => { setImportCode(e.target.value); setImportError("") }}
            rows={5}
            placeholder="SNZBP1:..."
            className="w-full resize-none rounded-md border border-input bg-background p-2.5 font-mono text-[11px] leading-relaxed focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
          />
          {importError && <p className="text-xs text-destructive">{importError}</p>}
          <DialogFooter className="flex-row items-center justify-end gap-2 sm:space-x-0">
            <Button type="button" variant="outline" size="sm" onClick={() => setImportOpen(false)}>
              Cancel
            </Button>
            <Button type="button" size="sm" disabled={!importCode.trim() || isSaving} onClick={importProfile}>
              <Import className="mr-2 h-3.5 w-3.5" /> Import
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
