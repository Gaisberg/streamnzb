import React, { useEffect, useState } from 'react'
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { apiFetch } from '@/api'

// Starting points for custom result templates, mirroring the built-in format.
const EXAMPLE_NAME_TEMPLATE = `{{if .Avail}}⚡ {{end}}{{.Service}}
{{.Stream}}{{if .Resolution}} {{.Resolution}}{{end}}`

const EXAMPLE_DESCRIPTION_TEMPLATE = `{{.ReleaseTitle}}
{{if .Quality}}📡 {{.Quality}} {{end}}{{if .Codec}}🎞️ {{upper .Codec}} {{end}}{{if .Size}}💾 {{size .Size}}{{end}}
{{if .HDR}}📺 {{join .HDR "|"}} {{end}}{{if .Audio}}🎧 {{join .Audio ", "}}{{end}}
{{if .Caps}}✅ {{.Caps}}
{{end}}🔍 {{.Indexer}} • 🎯 {{score .Score}}{{if .Age}} • 🕰️ {{.Age}}{{end}}`

// Field reference shown next to the template editors. Kept flat on purpose:
// these are exactly the FormatContext fields the backend exposes.
const FORMAT_FIELDS = [
  { group: 'Request', fields: ['.Service', '.Stream', '.Content', '.Index', '.Count'] },
  { group: 'Release', fields: ['.ReleaseTitle', '.Size', '.Indexer', '.Grabs', '.Age', '.Score', '.Avail', '.Library', '.Caps'] },
  { group: 'Parsed', fields: ['.ParsedTitle', '.Year', '.Date', '.Resolution', '.Quality', '.Codec', '.BitDepth', '.Bitrate', '.Container', '.Extension', '.Group', '.Edition', '.Network', '.Site', '.Country', '.Region', '.Audio', '.Channels', '.HDR', '.Languages'] },
  { group: 'Episode', fields: ['.Season', '.Episode', '.Seasons', '.Episodes', '.EpisodeCode', '.Volumes'] },
  { group: 'Flags', fields: ['.Proper', '.Repack', '.Remastered', '.Upscaled', '.ThreeD', '.Scene', '.Retail', '.Hardcoded', '.Dubbed', '.Subbed', '.Commentary', '.Complete', '.Documentary', '.Unrated', '.Uncensored', '.PPV'] },
  { group: 'Helpers', fields: ['size .Size', 'score .Score', 'join .HDR "|"', 'upper .Codec', 'lower', 'trim', 'default "?" .Group'] },
]

const templateBoxClass = "w-full resize-y rounded-md border border-input bg-background p-2.5 font-mono text-xs leading-relaxed focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"

// FormatPreview renders the live sample output for the result templates being
// edited, debounced against the admin preview endpoint. Compile errors are
// reported up through onErrorsChange so the dialog can block saving.
function FormatPreview({ nameTemplate, descriptionTemplate, onErrorsChange }) {
  const [preview, setPreview] = useState(null)
  const [previewError, setPreviewError] = useState('')

  useEffect(() => {
    const handle = setTimeout(() => {
      apiFetch('/api/format/preview', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name_template: nameTemplate || '', description_template: descriptionTemplate || '' }),
      })
        .then((res) => {
          setPreview(res)
          setPreviewError('')
          onErrorsChange?.({ name: res?.name_error || '', description: res?.description_error || '' })
        })
        .catch(() => {
          setPreview(null)
          setPreviewError('Preview unavailable — is the backend up to date?')
          onErrorsChange?.(null)
        })
    }, 500)
    return () => clearTimeout(handle)
  }, [nameTemplate, descriptionTemplate, onErrorsChange])

  return (
    <div className="space-y-2">
      <div className="text-xs font-medium uppercase tracking-wider text-muted-foreground">Live preview</div>
      {previewError && <p className="text-xs text-destructive">{previewError}</p>}
      {preview?.name_error && <p className="text-xs text-destructive">Name template: {preview.name_error}</p>}
      {preview?.description_error && <p className="text-xs text-destructive">Description template: {preview.description_error}</p>}
      <div className="space-y-2">
        {(preview?.samples || []).map((sample) => (
          <div key={sample.label} className="rounded-md border border-border/60 bg-card/40 p-2.5">
            <div className="mb-1.5 text-[11px] text-muted-foreground">{sample.label}</div>
            <div className="flex gap-3">
              <div className="w-28 shrink-0 whitespace-pre-line rounded bg-background/80 p-2 text-[11px] font-medium leading-snug">
                {sample.name}
              </div>
              <div className="min-w-0 flex-1 whitespace-pre-line rounded bg-background/80 p-2 text-[11px] leading-snug text-muted-foreground">
                {sample.description}
              </div>
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}

// ResultFormatEditor edits one stream's result name/description templates with
// a field reference and a live preview rendered by the backend.
export function ResultFormatEditor({ nameTemplate, descriptionTemplate, onNameChange, onDescriptionChange, onErrorsChange }) {
  return (
    <div className="space-y-4">
      <p className="text-xs text-muted-foreground">
        Customize how this stream's results render in Stremio using Go template syntax over each release's
        parsed data. Leave a template empty to keep the built-in format. AIOStreams responses keep their fixed format.
      </p>

      <div className="space-y-1.5">
        <div className="flex items-center justify-between gap-2">
          <Label className="text-sm font-medium">Name template</Label>
          <Button type="button" variant="ghost" size="sm" className="h-7 px-2 text-xs"
            onClick={() => onNameChange(EXAMPLE_NAME_TEMPLATE)}>
            Insert example
          </Button>
        </div>
        <textarea
          value={nameTemplate || ''}
          onChange={(event) => onNameChange(event.target.value)}
          rows={3}
          spellCheck={false}
          placeholder={'{{if .Avail}}⚡ {{end}}{{.Service}}\n{{.Stream}}'}
          className={templateBoxClass}
        />
        <p className="text-xs text-muted-foreground">The short label on the left of each result.</p>
      </div>

      <div className="space-y-1.5">
        <div className="flex items-center justify-between gap-2">
          <Label className="text-sm font-medium">Description template</Label>
          <Button type="button" variant="ghost" size="sm" className="h-7 px-2 text-xs"
            onClick={() => onDescriptionChange(EXAMPLE_DESCRIPTION_TEMPLATE)}>
            Insert example
          </Button>
        </div>
        <textarea
          value={descriptionTemplate || ''}
          onChange={(event) => onDescriptionChange(event.target.value)}
          rows={6}
          spellCheck={false}
          placeholder={'{{.ReleaseTitle}}\n🔍 {{.Indexer}} • 🎯 {{score .Score}}'}
          className={templateBoxClass}
        />
        <p className="text-xs text-muted-foreground">The multi-line detail text under each result.</p>
      </div>

      <div className="rounded-md border border-border/60 p-3">
        <div className="mb-2 text-xs font-medium uppercase tracking-wider text-muted-foreground">Available fields</div>
        <div className="space-y-1.5">
          {FORMAT_FIELDS.map((group) => (
            <div key={group.group} className="flex flex-wrap items-baseline gap-1.5">
              <span className="w-16 shrink-0 text-[11px] text-muted-foreground">{group.group}</span>
              {group.fields.map((f) => (
                <code key={f} className="rounded bg-muted px-1 py-0.5 font-mono text-[10px]">{`{{${f}}}`}</code>
              ))}
            </div>
          ))}
        </div>
        <p className="mt-2 text-[11px] text-muted-foreground">
          Conditionals: {'{{if .HDR}}…{{end}}'} — lists render with {'{{join .HDR "|"}}'}. Caps is the ffprobe-verified summary, present on library releases only.
        </p>
      </div>

      <FormatPreview nameTemplate={nameTemplate} descriptionTemplate={descriptionTemplate} onErrorsChange={onErrorsChange} />
    </div>
  )
}
