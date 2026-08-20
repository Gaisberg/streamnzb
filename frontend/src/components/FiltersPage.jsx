import React, { useMemo, useState } from "react"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Copy, Download, Import, Share2, SlidersHorizontal, TriangleAlert } from "lucide-react"
import { Label } from "@/components/ui/label"
import { ProfileManager } from "@/components/ProfileManager"
import { ProfileEditor } from "@/components/ProfileEditor"
import {
  CONTENT_KINDS, DEFAULT_PRESET, PRESETS, decodeProfileShareCode, defaultProfile,
  encodeProfileShareCode, profileFromJSON, profileToJSON, withoutLegacyFields,
} from "@/lib/profiles"

// summarize gives each profile card a one-line read of what it does. A profile
// is a preset plus rules, so that is the whole summary.
function summarize(profile) {
  const preset = PRESETS.find((p) => p.key === (profile.preset || DEFAULT_PRESET))
  const bits = [preset ? preset.label : profile.preset]
  const rules = (profile.rules || []).filter((r) => r && r.enabled !== false)
  if (rules.length) {
    const rejects = rules.filter((r) => r.action === "reject").length
    bits.push(`${rules.length} rule${rules.length === 1 ? "" : "s"}${rejects ? ` · ${rejects} reject` : ""}`)
  }
  return bits.join(" · ")
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

// A profile is a preset plus rules; saving only trims the name and makes sure
// a preset is recorded, so a profile written before presets existed does not
// save itself back without one.
function normalizeOnSave(profile) {
  return {
    ...profile,
    name: (profile.name || "").trim(),
    preset: profile.preset || DEFAULT_PRESET,
  }
}

// One box style for every code/JSON textarea in this page.
const shareBoxClass = "w-full resize-y rounded-md border border-input bg-background p-2.5 font-mono text-[11px] leading-relaxed focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"

// downloadJSON hands the profile over as a file, which is the form it needs to
// be in to live in a repository.
function downloadJSON(name, json) {
  const slug = (name || "profile").trim().toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "") || "profile"
  const url = URL.createObjectURL(new Blob([json], { type: "application/json" }))
  const link = document.createElement("a")
  link.href = url
  link.download = `${slug}.streamnzb.json`
  document.body.appendChild(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(url)
}

export function FiltersPage({ config, onSave, isSaving, saveStatus }) {
  const profiles = useMemo(() => config?.filter_profiles || [], [config])
  const usage = useMemo(() => profileUsage(config?.streams || {}), [config])
  const anyInUse = Object.keys(usage).length > 0
  const [exportCode, setExportCode] = useState(null)
  const [exportJSON, setExportJSON] = useState("")
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
      setExportJSON(profileToJSON(draft))
      const code = await encodeProfileShareCode(draft)
      setExportCode(code)
    } catch {
      setExportCode("")
    }
  }

  const copyText = async (text) => {
    if (!text) return
    try {
      await navigator.clipboard.writeText(text)
      setExportCopied(true)
    } catch {
      // Clipboard needs a secure context; the textarea is still selectable.
    }
  }

  // Import takes either format from one box. A share code is base64 and a
  // profile file is JSON, so they are trivially distinguishable and asking the
  // user which one they pasted would be asking them something the computer
  // already knows.
  const importProfile = async () => {
    try {
      const text = importCode.trim()
      const profile = text.startsWith("{")
        ? profileFromJSON(text)
        : await decodeProfileShareCode(text)

      const taken = new Set(profiles.map((p) => p.name.trim().toLowerCase()))
      let name = (profile.name || "").trim() || "Imported Profile"
      if (taken.has(name.toLowerCase())) {
        let n = 2
        while (taken.has(`${name} ${n}`.toLowerCase())) n += 1
        name = `${name} ${n}`
      }
      profile.name = name
      onSave([...profiles, profile])
      setImportOpen(false)
      setImportCode("")
      setImportError("")
    } catch (err) {
      setImportError(err?.message || "Could not read that profile.")
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
        renderEditor={(draft, setDraft) => <ProfileEditor profile={draft} onChange={setDraft} />}
      />

      <Dialog open={exportCode !== null} onOpenChange={(open) => { if (!open) setExportCode(null) }}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>Export “{exportName}”</DialogTitle>
            <DialogDescription>
              Two formats for two jobs. A share code pastes into a chat; the JSON is what you commit to a
              repository, review in a pull request, or edit by hand.
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4">
            <div className="space-y-1.5">
              <div className="flex items-center justify-between gap-2">
                <Label className="text-sm">Share code</Label>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-7 px-2 text-xs"
                  disabled={!exportCode}
                  onClick={() => copyText(exportCode)}
                >
                  <Copy className="mr-1.5 h-3 w-3" /> Copy
                </Button>
              </div>
              <textarea
                readOnly
                value={exportCode || "This browser cannot generate share codes."}
                onFocus={(e) => e.target.select()}
                rows={3}
                className={shareBoxClass}
              />
            </div>

            <div className="space-y-1.5">
              <div className="flex items-center justify-between gap-2">
                <Label className="text-sm">JSON</Label>
                <div className="flex items-center gap-1">
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="h-7 px-2 text-xs"
                    onClick={() => copyText(exportJSON)}
                  >
                    <Copy className="mr-1.5 h-3 w-3" /> Copy
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="h-7 px-2 text-xs"
                    onClick={() => downloadJSON(exportName, exportJSON)}
                  >
                    <Download className="mr-1.5 h-3 w-3" /> Download
                  </Button>
                </div>
              </div>
              <textarea
                readOnly
                value={exportJSON}
                onFocus={(e) => e.target.select()}
                rows={12}
                className={shareBoxClass}
              />
            </div>
          </div>

          <DialogFooter className="flex-row items-center justify-between gap-2 sm:justify-between sm:space-x-0">
            <span className="text-xs text-muted-foreground">{exportCopied ? "Copied to clipboard." : ""}</span>
            <Button type="button" variant="outline" size="sm" onClick={() => setExportCode(null)}>Done</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={importOpen} onOpenChange={setImportOpen}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>Import profile</DialogTitle>
            <DialogDescription>
              Paste a share code or an exported JSON profile — either works. It is added as a new profile and
              never overwrites an existing one.
            </DialogDescription>
          </DialogHeader>
          <textarea
            value={importCode}
            onChange={(e) => { setImportCode(e.target.value); setImportError("") }}
            rows={8}
            placeholder={'SNZBP1:…  or  { "streamnzb_profile": 1, … }'}
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
