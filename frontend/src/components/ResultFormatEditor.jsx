import React, { useEffect, useState } from 'react'
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { ChevronDown } from "lucide-react"
import { apiFetch } from '@/api'
import { cn } from "@/lib/utils"

// Starting points for custom result templates, mirroring the built-in format.
const EXAMPLE_NAME_TEMPLATE = `{{if .Avail}}⚡ {{end}}{{.Service}}
{{.Stream}}{{if .Resolution}} {{.Resolution}}{{end}}`

const EXAMPLE_DESCRIPTION_TEMPLATE = `{{.ReleaseTitle}}
{{if .Quality}}📡 {{.Quality}} {{end}}{{if .Codec}}🎞️ {{upper .Codec}} {{end}}{{if .Size}}💾 {{size .Size}}{{end}}
{{if .HDR}}📺 {{join .HDR "|"}} {{end}}{{if .Audio}}🎧 {{join .Audio ", "}}{{end}}
{{if .Caps}}✅ {{.Caps}}
{{end}}🔍 {{.Indexer}} • 🎯 {{score .Score}}{{if .Age}} • 🕰️ {{.Age}}{{end}}`

// Field reference shown under the template editors. Groups mirror FormatContext
// in the backend formatter, including the parts a fresh indexer hit cannot
// answer (Probed, Avail) — those are labelled rather than hidden, because a
// field that renders empty half the time is worth knowing about before you use
// it. Entries in a `raw` group are printed verbatim; every other entry is
// wrapped in {{ }}.
const FORMAT_FIELDS = [
  { group: 'Request', fields: ['.Service', '.Stream', '.Content', '.Index', '.Count', '.TopScore'] },
  { group: 'Release', fields: ['.ReleaseTitle', '.Size', '.Indexer', '.Variants', '.VariantIndexers', '.Grabs', '.Age', '.Duration', '.Score', '.Avail', '.Library', '.Caps'] },
  {
    group: 'Rules',
    raw: true,
    fields: [
      '{{range .MatchedRules}}[{{.Name}}] {{end}}',
      '{{range .MatchedRules}}{{.Name}} {{score .Score}} · {{end}}',
    ],
  },
  { group: 'Parsed', fields: ['.ParsedTitle', '.Year', '.Date', '.Resolution', '.Quality', '.Codec', '.BitDepth', '.Bitrate', '.Container', '.Extension', '.Group', '.Edition', '.Network', '.Site', '.Country', '.Region', '.Audio', '.Channels', '.HDR', '.Languages'] },
  { group: 'Episode', fields: ['.Season', '.Episode', '.Seasons', '.Episodes', '.EpisodeCode', '.Volumes'] },
  { group: 'Flags', fields: ['.Proper', '.Repack', '.Remastered', '.Upscaled', '.ThreeD', '.Scene', '.Retail', '.Hardcoded', '.Dubbed', '.Subbed', '.Commentary', '.Complete', '.Documentary', '.Unrated', '.Uncensored', '.PPV'] },
  { group: 'Kind', fields: ['.Kind', '.IsAnime', '.OriginalLanguage'] },
  { group: 'Probed', fields: ['.Verified', '.Probed.VideoCodec', '.Probed.AudioCodec', '.Probed.Width', '.Probed.Height', '.Probed.Profile', '.Probed.BitDepth', '.Probed.HDR', '.Probed.DolbyVision', '.Probed.HasHDRFallback', '.Probed.DynamicRange', '.Probed.TracksProbed', '.Probed.AudioLanguages', '.Probed.SubtitleLanguages', '.Probed.AudioStreams', '.Probed.SubtitleStreams'] },
  { group: 'Avail', fields: ['.Availability.Status', '.Availability.Known', '.Availability.OnMyBackbone', '.Availability.CheckedDaysAgo', '.Availability.Compression'] },
  { group: 'SeaDex', fields: ['.Seadex.Checked', '.Seadex.Known', '.Seadex.Best', '.Seadex.Alternative', '.Seadex.DualAudio'] },
  { group: 'Helpers', fields: ['size .Size', 'score .Score', 'join .HDR "|"', 'upper .Codec', 'lower', 'trim', 'replace .Resolution "1080p" "HD"', 'default "?" .Group', 'title .ParsedTitle', 'truncate 24 .ParsedTitle', 'remove "DD" .Audio', 'translate "0123456789" "₀₁₂₃₄₅₆₇₈₉" .Score', 'smallcaps .Network', '.ParsedTitle | title | truncate 24'] },
  { group: 'Lists', fields: ['sortAsc .Audio', 'sortDesc .Channels', 'first .Audio', 'last .Audio', 'length .HDR', 'join (sortAsc .Audio) " · "'] },
  { group: 'Math', fields: ['add 100 .Score', 'sub 100 .Score', 'mul 2 .Season', 'div 1000 .Score', 'mod 10 .Score', 'min 50 .Score', 'max 0 .Score', 'stars 5 .TopScore .Score', 'repeat "▰" 3', '.Score | div 1000 | repeat "▰"'] },
  {
    group: 'Checks',
    raw: true,
    fields: [
      '{{if .HDR}}…{{end}}',
      '{{if not .Avail}}…{{end}}',
      '{{if .Avail}}…{{else}}…{{end}}',
      '{{if and .Avail .Library}}…{{end}}',
      '{{if or .Proper .Repack}}…{{end}}',
      '{{if exists .HDR}}…{{end}}',
      '{{if eq .Resolution "2160p"}}…{{end}}',
      '{{if ne .Group ""}}…{{end}}',
      '{{if gt (length .Audio) 1}}…{{end}}',
      '{{if ge .Grabs 100}}…{{end}}',
      '{{if lt .Year 2000}}…{{end}}',
      '{{if le .Index 3}}…{{end}}',
      '{{if contains "DV" .HDR}}…{{end}}',
      '{{if hasPrefix "2160" .Resolution}}…{{end}}',
      '{{if hasSuffix "p" .Resolution}}…{{end}}',
    ],
  },
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

// AIOStreamsImport converts pasted AIOStreams formatter templates into Go
// templates via the backend and fills the editors with the result. Anything
// that has no StreamNZB equivalent stays in the output verbatim and is listed
// as a warning for hand-editing.
function AIOStreamsImport({ onNameChange, onDescriptionChange }) {
  const [open, setOpen] = useState(false)
  const [aioName, setAioName] = useState('')
  const [aioDescription, setAioDescription] = useState('')
  const [warnings, setWarnings] = useState([])
  const [error, setError] = useState('')
  const [converting, setConverting] = useState(false)

  const convert = () => {
    setConverting(true)
    setError('')
    apiFetch('/api/format/convert', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name_template: aioName || '', description_template: aioDescription || '' }),
    })
      .then((res) => {
        if (aioName.trim()) onNameChange(res.name_template || '')
        if (aioDescription.trim()) onDescriptionChange(res.description_template || '')
        setWarnings([...(res.name_warnings || []), ...(res.description_warnings || [])])
      })
      .catch(() => setError('Conversion failed — is the backend up to date?'))
      .finally(() => setConverting(false))
  }

  return (
    <div className="rounded-md border border-border/60 p-3">
      <button type="button" className="w-full text-left text-xs font-medium uppercase tracking-wider text-muted-foreground"
        onClick={() => setOpen(!open)}>
        {open ? '▾' : '▸'} Import from AIOStreams
      </button>
      {open && (
        <div className="mt-2 space-y-2">
          <p className="text-xs text-muted-foreground">
            Paste your AIOStreams custom formatter below and convert it into the Go templates above.
            The conversion is best-effort: fields without a StreamNZB equivalent are dropped (conditionals fall back to their false branch)
            and listed below, so nothing unconvertible leaks into your results.
          </p>
          <textarea
            value={aioName}
            onChange={(event) => setAioName(event.target.value)}
            rows={2}
            spellCheck={false}
            placeholder={'AIOStreams name template, e.g. {stream.resolution::exists["{stream.resolution}"||"N/A"]}'}
            className={templateBoxClass}
          />
          <textarea
            value={aioDescription}
            onChange={(event) => setAioDescription(event.target.value)}
            rows={4}
            spellCheck={false}
            placeholder={"AIOStreams description template, e.g. {stream.audioTags::lsort::join(' · ')}"}
            className={templateBoxClass}
          />
          <Button type="button" variant="secondary" size="sm" className="h-7 px-3 text-xs"
            disabled={converting || (!aioName.trim() && !aioDescription.trim())} onClick={convert}>
            {converting ? 'Converting…' : 'Convert'}
          </Button>
          {error && <p className="text-xs text-destructive">{error}</p>}
          {warnings.length > 0 && (
            <ul className="list-disc space-y-0.5 pl-4 text-[11px] text-muted-foreground">
              {warnings.map((warning) => <li key={warning}>{warning}</li>)}
            </ul>
          )}
        </div>
      )}
    </div>
  )
}

// FieldReference is the read-only counterpart to the rules editor's attribute
// list: everything a template can read, collapsed by default so the dialog
// opens on the editors rather than on a wall of field names.
function FieldReference() {
  const [open, setOpen] = useState(false)

  return (
    <div className="rounded-lg border border-border/60 bg-card/40">
      <button
        type="button"
        onClick={() => setOpen(!open)}
        className="flex w-full items-center gap-2 px-3 py-2.5 text-left"
      >
        <ChevronDown className={cn("h-3.5 w-3.5 text-muted-foreground transition-transform", !open && "-rotate-90")} />
        <span className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          Available fields
        </span>
        <span className="text-[11px] text-muted-foreground/70">
          {open ? "" : "everything a template can read"}
        </span>
      </button>

      {open && (
        <div className="border-t border-border/50 px-3 py-3">
          <div className="space-y-1.5">
            {FORMAT_FIELDS.map((group) => (
              <div key={group.group} className="flex flex-wrap items-baseline gap-1.5">
                <span className="w-16 shrink-0 text-[11px] text-muted-foreground">{group.group}</span>
                {group.fields.map((f) => (
                  <code key={f} className="rounded bg-muted px-1 py-0.5 font-mono text-[10px]">
                    {group.raw ? f : `{{${f}}}`}
                  </code>
                ))}
              </div>
            ))}
          </div>
          <p className="mt-2 max-w-prose text-[11px] text-muted-foreground">
            Lines that render empty are removed, so a false conditional on its own line never leaves a blank line.
            Lists render comma-separated until you join them yourself. Rules are the profile rules that paid out on
            this release, in configuration order — one that rejected it never reaches a template. Caps, Verified and
            Probed are ffprobe readings, filled for library releases only; Availability stays blank until AvailNZB
            has an opinion.
          </p>
        </div>
      )}
    </div>
  )
}

// ResultFormatEditor edits one stream's result name/description templates with
// a field reference and a live preview rendered by the backend.
export function ResultFormatEditor({ nameTemplate, descriptionTemplate, onNameChange, onDescriptionChange, onErrorsChange }) {
  return (
    <div className="space-y-4">
      <p className="text-xs text-muted-foreground">
        Customize how results render in Stremio using Go template syntax over each release's parsed data.
        Leave a template empty to keep the built-in format. AIOStreams responses keep their fixed format.
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

      <FieldReference />

      <AIOStreamsImport onNameChange={onNameChange} onDescriptionChange={onDescriptionChange} />

      <FormatPreview nameTemplate={nameTemplate} descriptionTemplate={descriptionTemplate} onErrorsChange={onErrorsChange} />
    </div>
  )
}
