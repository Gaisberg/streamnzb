import React, { useMemo } from "react"
import { Card, CardContent } from "@/components/ui/card"
import { Type } from "lucide-react"
import { ProfileManager } from "@/components/ProfileManager"
import { ResultFormatEditor } from "@/components/ResultFormatEditor"
import { RemoteSourceCard } from "@/components/RemoteSourceCard"
import { useProfileSharing } from "@/components/useProfileSharing"
import {
  decodeFormatProfileShareCode, encodeFormatProfileShareCode, fetchRemoteFormatProfile,
} from "@/lib/formatProfiles"
import { nameKey, usageByName } from "@/lib/usage"

function summarize(profile) {
  const bits = []
  if ((profile.result_name_template || "").trim()) bits.push("custom name")
  if ((profile.result_description_template || "").trim()) bits.push("custom description")
  return bits.join(" · ") || "Built-in format"
}

function describeDelete(profile, usage) {
  const name = profile?.name || ""
  const used = usage[nameKey(name)]
  if (!used?.length) {
    return `Delete “${name}”? It is not in use, so nothing else changes.`
  }
  return `Delete “${name}”? It will be cleared from ${used.join(", ")}, which will fall back to the built-in result format.`
}

export function FormattingPage({ config, onPersist, isSaving, saveStatus }) {
  const profiles = useMemo(() => config?.format_profiles || [], [config])
  const usage = useMemo(() => usageByName(config?.streams, (stream) => [stream.format_profile_name]), [config])
  const onSave = (next) => onPersist({ format_profiles: next })
  const sharing = useProfileSharing({
    profiles,
    onSave,
    isSaving,
    codec: {
      encode: encodeFormatProfileShareCode,
      decode: decodeFormatProfileShareCode,
      fetchRemote: fetchRemoteFormatProfile,
      placeholder: "SNZBF1:…",
    },
  })

  return (
    <div className="space-y-6">
      <div>
        <h2 className="flex items-center gap-2 text-lg font-medium text-foreground">
          <Type className="h-4 w-4" /> Formatting
        </h2>
        <p className="text-sm text-muted-foreground">
          A format profile decides how a stream&apos;s results render in Stremio — name and description, in Go
          template syntax over each release&apos;s parsed data. Bind one to a stream from the Streams page; a
          stream without one uses the built-in format. AIOStreams responses keep their fixed format.
        </p>
      </div>

      <ProfileManager
        profiles={profiles}
        onSave={onSave}
        usage={usage}
        summarize={summarize}
        newProfile={(name) => ({ name })}
        describeDelete={describeDelete}
        entityLabel="format profile"
        emptyText="No format profiles yet. Every stream renders the built-in format."
        isSaving={isSaving}
        saveStatus={saveStatus}
        // A duplicate is a fork: it drops the remote link, so two profiles
        // never refresh from the same URL.
        normalizeOnDuplicate={(profile) => {
          const copy = { ...profile }
          delete copy.source
          return copy
        }}
        sharing={sharing}
        renderEditor={(draft, setDraft) => (
          <>
            {draft.source?.url && <RemoteSourceCard profile={draft} onChange={setDraft} flavor="format" />}
            <Card className="border border-border bg-card">
              <CardContent className="pt-6">
                <ResultFormatEditor
                  nameTemplate={draft.result_name_template}
                  descriptionTemplate={draft.result_description_template}
                  onNameChange={(value) => setDraft({ ...draft, result_name_template: value })}
                  onDescriptionChange={(value) => setDraft({ ...draft, result_description_template: value })}
                />
              </CardContent>
            </Card>
          </>
        )}
      />
    </div>
  )
}
