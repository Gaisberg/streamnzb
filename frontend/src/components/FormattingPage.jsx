import React, { useMemo } from "react"
import { Card, CardContent } from "@/components/ui/card"
import { Type } from "lucide-react"
import { ProfileManager } from "@/components/ProfileManager"
import { ResultFormatEditor } from "@/components/ResultFormatEditor"

// profileUsage maps a format profile name to the streams bound to it.
function profileUsage(streams = {}) {
  const usage = {}
  Object.entries(streams).forEach(([streamName, stream]) => {
    const key = (stream?.format_profile_name || "").trim().toLowerCase()
    if (!key) return
    if (!usage[key]) usage[key] = []
    if (!usage[key].includes(streamName)) usage[key].push(streamName)
  })
  return usage
}

function summarize(profile) {
  const bits = []
  if ((profile.result_name_template || "").trim()) bits.push("custom name")
  if ((profile.result_description_template || "").trim()) bits.push("custom description")
  return bits.join(" · ") || "Built-in format"
}

function describeDelete(profile, usage) {
  const name = profile?.name || ""
  const used = usage[name.trim().toLowerCase()]
  if (!used?.length) {
    return `Delete “${name}”? It is not in use, so nothing else changes.`
  }
  return `Delete “${name}”? It will be cleared from ${used.join(", ")}, which will fall back to the built-in result format.`
}

export function FormattingPage({ config, onPersist, isSaving, saveStatus }) {
  const profiles = useMemo(() => config?.format_profiles || [], [config])
  const usage = useMemo(() => profileUsage(config?.streams || {}), [config])

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
        onSave={(next) => onPersist({ format_profiles: next })}
        usage={usage}
        summarize={summarize}
        newProfile={(name) => ({ name })}
        describeDelete={describeDelete}
        entityLabel="format profile"
        emptyText="No format profiles yet. Every stream renders the built-in format."
        isSaving={isSaving}
        saveStatus={saveStatus}
        renderEditor={(draft, setDraft) => (
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
        )}
      />
    </div>
  )
}
