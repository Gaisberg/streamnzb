import React, { useMemo } from "react"
import { Button } from "@/components/ui/button"
import { Import, Share2, SlidersHorizontal, TriangleAlert } from "lucide-react"
import { ProfileManager } from "@/components/ProfileManager"
import { ProfileEditor } from "@/components/ProfileEditor"
import { RemoteSourceCard } from "@/components/RemoteSourceCard"
import { useProfileSharing } from "@/components/useProfileSharing"
import {
  CONTENT_KINDS, DEFAULT_PRESET, PRESETS, decodeProfileShareCode, defaultProfile,
  encodeProfileShareCode, withoutLegacyFields,
} from "@/lib/profiles"
import { fetchRemoteProfile } from "@/lib/remoteProfiles"

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

export function FiltersPage({ config, onSave, isSaving, saveStatus }) {
  const profiles = useMemo(() => config?.filter_profiles || [], [config])
  const usage = useMemo(() => profileUsage(config?.streams || {}), [config])
  const anyInUse = Object.keys(usage).length > 0
  const sharing = useProfileSharing({
    profiles,
    onSave,
    isSaving,
    codec: {
      encode: encodeProfileShareCode,
      decode: decodeProfileShareCode,
      fetchRemote: fetchRemoteProfile,
      placeholder: "SNZBP1:…",
    },
  })

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
        <Button variant="outline" size="sm" onClick={sharing.openImport}>
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
        // A duplicate is a fork: it drops the remote link along with the
        // legacy fields, so two profiles never refresh from the same URL.
        normalizeOnDuplicate={(profile) => {
          const copy = withoutLegacyFields(profile)
          delete copy.source
          return copy
        }}
        renderActions={(draft) => (
          <Button variant="ghost" size="sm" onClick={() => sharing.exportProfile(draft)}>
            <Share2 className="mr-2 h-3.5 w-3.5" /> Export
          </Button>
        )}
        renderEditor={(draft, setDraft) => (
          <>
            {draft.source?.url && <RemoteSourceCard profile={draft} onChange={setDraft} flavor="filter" />}
            <ProfileEditor profile={draft} onChange={setDraft} />
          </>
        )}
      />

      {sharing.dialogs}
    </div>
  )
}
