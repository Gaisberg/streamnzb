import React, { useMemo } from "react"
import { Library, SlidersHorizontal, TriangleAlert } from "lucide-react"
import { ProfileManager } from "@/components/ProfileManager"
import { ProfileEditor } from "@/components/ProfileEditor"
import { DefineLibraryEditor } from "@/components/DefineLibraryEditor"
import { RemoteSourceCard } from "@/components/RemoteSourceCard"
import { useProfileSharing } from "@/components/useProfileSharing"
import {
  CONTENT_KINDS, DEFAULT_PRESET, PRESETS, decodeProfileShareCode, defaultProfile,
  encodeProfileShareCode, matchedRuleNames, ruleKey, withoutLegacyFields,
} from "@/lib/profiles"
import { fetchRemoteProfile } from "@/lib/remoteProfiles"
import { nameKey, usageByName } from "@/lib/usage"
import {
  defineLibraryFromPaste, encodeDefineLibraryShareCode, fetchRemoteDefineLibrary,
  summarizeDefineLibrary,
} from "@/lib/defineLibraries"

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
function profileUsage(streams) {
  return usageByName(streams, (stream, streamName) => {
    // AIOStreams mode returns everything unfiltered, so nothing is applied.
    if (stream.filter_sorting_mode === "aiostreams") return []
    return [
      { name: stream.filter_profile_name, label: `${streamName} · all content` },
      ...Object.entries(stream.filter_profile_by_type || {}).map(([kind, name]) => ({
        name,
        label: `${streamName} · ${(CONTENT_KINDS.find((k) => k.key === kind)?.label || kind).toLowerCase()}`,
      })),
    ]
  })
}

// describeDelete spells out the knock-on effect, since deleting a profile
// also clears it from any stream using it.
function describeDelete(profile, usage) {
  const name = profile?.name || ""
  const used = usage[nameKey(name)]
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

// libraryUsage maps a library name to the profiles that reference any of its
// defines through matched(). It is what the delete confirmation reads —
// deleting a library a profile leans on stops that profile from compiling —
// and what marks a library as in use in the list.
function libraryUsage(libraries, profiles) {
  const usage = {}
  libraries.forEach((library) => {
    const defines = new Set((library.rules || []).map((rule) => ruleKey(rule.name)))
    const users = profiles.filter((profile) =>
      (profile.rules || []).some((rule) =>
        matchedRuleNames(`${rule.when || ""} ${rule.group_by || ""}`)
          .some((name) => defines.has(ruleKey(name)))))
    if (users.length) usage[ruleKey(library.name)] = users.map((profile) => profile.name)
  })
  return usage
}

function describeLibraryDelete(library, usage) {
  const name = library?.name || ""
  const used = usage[ruleKey(name)]
  if (!used?.length) {
    return `Delete “${name}”? No profile references its defines, so nothing else changes.`
  }
  return `Delete “${name}”? Its defines are referenced by ${used.join(", ")} — those profiles will stop compiling until the references are removed, and their next save will be refused.`
}

export function FiltersPage({ config, onSave, onSaveLibraries, isSaving, saveStatus }) {
  const profiles = useMemo(() => config?.filter_profiles || [], [config])
  const libraries = useMemo(() => config?.define_libraries || [], [config])
  const usage = useMemo(() => profileUsage(config?.streams || {}), [config])
  const libUsage = useMemo(() => libraryUsage(libraries, profiles), [libraries, profiles])
  const libraryRules = useMemo(() => libraries.flatMap((library) => library.rules || []), [libraries])
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
  const librarySharing = useProfileSharing({
    profiles: libraries,
    onSave: onSaveLibraries,
    isSaving,
    noun: "define library",
    importNote: "Paste a share code or plain rule text — one define per line, # comments allowed — or give the https URL of a file serving either. It is added as a new library and never overwrites an existing one. A library imported from a URL stays linked to it: a Refresh button fetches updates, which apply only after you review them.",
    codec: {
      encode: encodeDefineLibraryShareCode,
      decode: defineLibraryFromPaste,
      fetchRemote: fetchRemoteDefineLibrary,
      placeholder: "SNZBD1:…  — or paste defines, one per line",
    },
  })

  return (
    <div className="space-y-6">
      <div>
        <h2 className="flex items-center gap-2 text-lg font-medium text-foreground">
          <SlidersHorizontal className="h-4 w-4" /> Filters
        </h2>
        <p className="text-sm text-muted-foreground">
          A profile decides which releases you are offered, how they are scored, and what order they arrive in.
          Assign one to a stream from the Streams page.
        </p>
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
        sharing={sharing}
        renderEditor={(draft, setDraft) => (
          <>
            {draft.source?.url && <RemoteSourceCard profile={draft} onChange={setDraft} flavor="filter" />}
            <ProfileEditor profile={draft} onChange={setDraft} libraryRules={libraryRules} />
          </>
        )}
      />

      <div className="border-t border-border pt-6">
        <h2 className="flex items-center gap-2 text-lg font-medium text-foreground">
          <Library className="h-4 w-4" /> Define libraries
        </h2>
        <p className="text-sm text-muted-foreground">
          Shared bundles of define rules — release-group tiers, community lists — kept once and referenced
          from every profile with <code className="font-mono text-xs">matched(&quot;Name&quot;)</code>. The
          data lives here; what a tier is worth stays each profile&apos;s own rule.
        </p>
      </div>

      <ProfileManager
        profiles={libraries}
        onSave={onSaveLibraries}
        usage={libUsage}
        summarize={summarizeDefineLibrary}
        newProfile={(name) => ({ name, rules: [] })}
        describeDelete={describeLibraryDelete}
        entityLabel="define library"
        addButtonLabel="New library"
        newProfileBaseName="New Library"
        emptyText="No define libraries yet. Import a community-maintained one from a URL, or create your own."
        isSaving={isSaving}
        saveStatus={saveStatus}
        // A duplicate is a fork: it drops the remote link, so two libraries
        // never refresh from the same URL.
        normalizeOnDuplicate={(library) => {
          const copy = { ...library }
          delete copy.source
          return copy
        }}
        sharing={librarySharing}
        renderEditor={(draft, setDraft) => (
          <>
            {draft.source?.url && <RemoteSourceCard profile={draft} onChange={setDraft} flavor="library" />}
            <DefineLibraryEditor library={draft} onChange={setDraft} />
          </>
        )}
      />
    </div>
  )
}
