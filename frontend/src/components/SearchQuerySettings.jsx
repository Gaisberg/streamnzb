import React, { useEffect, useMemo, useRef, useState } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { EntityDialog } from "@/components/EntityDialog"
import { useEntityDialog } from "@/hooks/useEntityDialog"
import { UsageChips } from "@/components/UsageChips"
import { apiFetch } from "@/api"
import { normalizeSearchTitleLanguage, normalizeSearchTitleLanguages } from "@/lib/config"
import {
  ADDRESS_ID,
  ADDRESS_OPTIONS,
  ADDRESS_TITLE,
  ORDER_ADAPTIVE_SEASON,
  ORDER_OPTIONS,
  STOP_ALL,
  STOP_OPTIONS,
  TARGET_ABSOLUTE,
  TARGET_OPTIONS,
  attemptKey,
  attemptLabel,
  attemptsInRunOrder,
  defaultAttempt,
  isSeriesKind,
  normalizeAddress,
  normalizeAttempt,
  normalizeAttempts,
  normalizeOrder,
  normalizeStop,
  normalizeTarget,
  planPresets,
  presetPlan,
} from "@/lib/searchPlan"
import { SortableList, SortableRow } from "@/components/SortableList"
import { moveItem } from "@/lib/lists"
import { assignedStreams } from "@/lib/usage"
import { mapStreamsByUsername } from "@/lib/streams"
import { ArrowRight, CircleHelp, Copy, Plus, Settings, Sparkles, Trash2, X } from "lucide-react"

const CACHE_CLEARED_SUFFIX = ' Search cache cleared.'

const TITLE_LANGUAGE_OPTIONS = [
  { value: '', label: 'Original' },
  { value: 'en-US', label: 'English' },
  { value: 'de-DE', label: 'German' },
  { value: 'fr-FR', label: 'French' },
  { value: 'es-ES', label: 'Spanish' },
  { value: 'it-IT', label: 'Italian' },
  { value: 'nl-NL', label: 'Dutch' },
  { value: 'pl-PL', label: 'Polish' },
  { value: 'pt-BR', label: 'Portuguese (Brazil)' },
  { value: 'pt-PT', label: 'Portuguese (Portugal)' },
  { value: 'sv-SE', label: 'Swedish' },
  { value: 'no-NO', label: 'Norwegian' },
  { value: 'da-DK', label: 'Danish' },
  { value: 'fi-FI', label: 'Finnish' },
  { value: 'cs-CZ', label: 'Czech' },
  { value: 'sk-SK', label: 'Slovak' },
  { value: 'hu-HU', label: 'Hungarian' },
  { value: 'ro-RO', label: 'Romanian' },
  { value: 'tr-TR', label: 'Turkish' },
  { value: 'ru-RU', label: 'Russian' },
  { value: 'uk-UA', label: 'Ukrainian' },
  { value: 'ja-JP', label: 'Japanese' },
  { value: 'ko-KR', label: 'Korean' },
  { value: 'zh-CN', label: 'Chinese (Simplified)' },
  { value: 'zh-TW', label: 'Chinese (Traditional)' },
]

const ATTEMPTS_HINT_ITEMS = [
  {
    label: 'What it is',
    text: 'The questions this request asks indexers, in order. Each row is one query.',
  },
  {
    label: 'Address',
    text: 'ID asks by database id (IMDb/TVDB/TMDB/Kitsu — whichever the indexer supports) and trusts the title it answers with. Title sends a text query and checks the answer.',
  },
  {
    label: 'Target',
    text: 'Episode asks for one episode, Season for the whole season, Series for the title alone, Absolute for the anime absolute episode number. Absolute rows are skipped for anything that is not anime.',
  },
  {
    label: 'Order',
    text: 'Put the narrowest question first. With "Stop at first hit" the later rows are only paid for when the earlier ones matched nothing.',
  },
]

const STOP_HINT_ITEMS = [
  {
    label: 'Stop at first hit',
    text: 'Walk the rows in order and stop at the first one that matched anything. A request that finds what it wanted costs one indexer round trip.',
  },
  {
    label: 'Run every attempt',
    text: 'Ask every row every time and merge the results. Broader, and always pays for every query.',
  },
]

const ORDER_HINT_ITEMS = [
  {
    label: 'As listed',
    text: 'Run the rows exactly as written.',
  },
  {
    label: 'Season first once it has aired',
    text: 'A finished season is where the season pack lives, so the Season rows move to the front once every episode of the requested season has aired. Air dates come from TVMaze, TVDB or TMDB; when none of them can say, the order is left alone.',
  },
]

const ACCEPT_TITLES_HINT_ITEMS = [
  {
    label: 'What it is',
    text: 'The metadata titles a release name may match to prove it is the right content. Separate from what goes out: a Title row queries under one language, and this is what comes back.',
  },
  {
    label: 'ID rows',
    text: 'An ID row names an id, so a title mismatch is counted and kept rather than dropped — the indexer resolved the title itself.',
  },
  {
    label: 'Empty',
    text: 'With no titles listed, an attempt falls back to its own query language.',
  },
]

const ACCEPT_YEAR_HINT_ITEMS = [
  {
    label: 'On',
    text: 'A release year has to be within a year of the metadata year, and Title rows with "+ year" put it in the query too.',
  },
  {
    label: 'TV',
    text: 'Usually off: scene TV releases are named Title.S01E01.1080p... and carry no year at all.',
  },
]

const ACCEPT_PACKS_HINT_ITEMS = [
  {
    label: 'On',
    text: 'A season or complete-series pack that contains the requested episode counts as a match.',
  },
  {
    label: 'Off',
    text: 'Only releases that name the episode are kept, even when a Season row found the pack.',
  },
]

const CATEGORIES_HINT_ITEMS = [
  {
    label: 'Default',
    text: 'Empty. Movies ask for 2000, TV for 5000, and anime for 5070 alongside 5000 — plus any bucket an indexer names "anime" in its own caps under another id.',
  },
  {
    label: 'Override',
    text: 'A comma-separated Newznab category list, sent to every indexer for this request instead of the defaults. Only needed when an indexer files things under ids of its own.',
  },
]

const SEARCH_LIMIT_HINT_ITEMS = [
  {
    label: 'Max',
    text: '0 uses Max.',
  },
  {
    label: 'Newznab',
    text: 'Reads the max from caps. If caps are unavailable, it falls back to 2000.',
  },
  {
    label: 'Explicit',
    text: 'Any explicit value is sent as-is.',
  },
]
const EXTRA_TERMS_HINT_ITEMS = [
  {
    label: 'Usage',
    text: 'Optional terms for text and ID searches.',
  },
  {
    label: 'Syntax',
    text: 'Use quotes for exact phrases, `!term` to exclude words, `*` as wildcard, `|` or `OR` for alternatives, and parentheses for groups like `(1080p|720p)`.',
  },
]

function normalizeName(value) {
  return (value || '').trim().toLowerCase()
}

function remainingTitleLanguageOptions(selectedLanguages) {
  const selected = new Set(normalizeSearchTitleLanguages(selectedLanguages).map((value) => value.toLowerCase()))
  return TITLE_LANGUAGE_OPTIONS.filter((option) => !selected.has(normalizeSearchTitleLanguage(option.value).toLowerCase()))
}

function titleLanguageLabel(value) {
  return TITLE_LANGUAGE_OPTIONS.find((option) => option.value === normalizeSearchTitleLanguage(value))?.label || 'Original'
}

function assignedStreamsForQuery(streamsByName, kind, queryName) {
  const field = kind === 'movie' ? 'movie_search_queries' : 'series_search_queries'
  return assignedStreams(streamsByName, field, queryName)
}

function emptyDraft(kind) {
  return { name: kind === 'movie' ? 'MovieQuery01' : 'TVQuery01', ...presetPlan(kind, 'balanced'), search_result_limit: 0, categories: '' }
}

function normalizeAccept(kind, accept) {
  const value = accept || {}
  const next = {
    titles: normalizeSearchTitleLanguages(value.titles),
    year: value.year === true,
  }
  if (isSeriesKind(kind)) {
    next.packs = value.packs !== false
  }
  return next
}

function normalizeDraft(kind, draft) {
  const value = draft || {}
  return {
    name: (value.name || '').trim(),
    attempts: normalizeAttempts(value.attempts, kind),
    stop: normalizeStop(value.stop),
    order: isSeriesKind(kind) ? normalizeOrder(value.order) : undefined,
    accept: normalizeAccept(kind, value.accept),
    search_result_limit: value.search_result_limit ?? 0,
    categories: String(value.categories ?? '').trim(),
  }
}

function persistableDraft(kind, draft) {
  return normalizeDraft(kind, draft)
}

function comparableQuerySignature(kind, draft) {
  const value = normalizeDraft(kind, draft)
  return JSON.stringify({
    attempts: value.attempts,
    stop: value.stop,
    order: value.order,
    accept: value.accept,
    search_result_limit: Number(value.search_result_limit || 0),
    categories: value.categories,
  })
}

function findDuplicateQueryName(kind, draft, queries) {
  const signature = comparableQuerySignature(kind, draft)
  const match = (Array.isArray(queries) ? queries : []).find((query) => comparableQuerySignature(kind, query) === signature)
  return match?.name || ''
}

function extractScopedQueryFieldErrors(fieldErrors, kind, index) {
  if (!fieldErrors || typeof fieldErrors !== 'object') return {}
  const prefix = `${kind === 'movie' ? 'movie_search_queries' : 'series_search_queries'}.${index}.`
  return Object.entries(fieldErrors).reduce((acc, [path, message]) => {
    if (!path.startsWith(prefix) || typeof message !== 'string' || message.trim() === '') {
      return acc
    }
    const field = path.slice(prefix.length)
    if (field) {
      acc[field] = message
    }
    return acc
  }, {})
}

function summarizeQuery(query, kind) {
  const value = normalizeDraft(kind, query)
  const primary = value.attempts.map((attempt) => attemptLabel(attempt, kind))
  const validation = []
  if (value.stop === STOP_ALL) {
    validation.push('Runs every attempt')
  } else {
    validation.push('Stops at first hit')
  }
  if (value.order === ORDER_ADAPTIVE_SEASON) validation.push('Season first once aired')
  validation.push(`Titles: ${value.accept.titles.length === 0 ? 'attempt language' : value.accept.titles.map(titleLanguageLabel).join(', ')}`)
  validation.push(`Year: ${value.accept.year ? 'Must match' : 'Ignored'}`)
  if (isSeriesKind(kind)) validation.push(`Packs: ${value.accept.packs ? 'Accepted' : 'Rejected'}`)

  const extra = []
  extra.push(`Limit: ${Number(value.search_result_limit || 0) === 0 ? 'Max' : value.search_result_limit}`)
  if (value.categories) extra.push(`Categories: ${value.categories}`)

  return { primary, validation, extra }
}

function CompactRow({ items = [] }) {
  if (!Array.isArray(items) || items.length === 0) return null
  return (
    <div className="flex flex-wrap gap-2 text-xs text-muted-foreground">
      {items.map((part) => (
        <span key={part} className="rounded-full border border-border px-2 py-1">{part}</span>
      ))}
    </div>
  )
}

function LabelWithHelp({ label, items = [] }) {
  const [open, setOpen] = useState(false)
  const containerRef = useRef(null)

  useEffect(() => {
    if (!open) return undefined

    const handlePointerDown = (event) => {
      if (!containerRef.current?.contains(event.target)) {
        setOpen(false)
      }
    }

    const handleEscape = (event) => {
      if (event.key === 'Escape') {
        setOpen(false)
      }
    }

    document.addEventListener('mousedown', handlePointerDown)
    document.addEventListener('touchstart', handlePointerDown)
    document.addEventListener('keydown', handleEscape)

    return () => {
      document.removeEventListener('mousedown', handlePointerDown)
      document.removeEventListener('touchstart', handlePointerDown)
      document.removeEventListener('keydown', handleEscape)
    }
  }, [open])

  if (!Array.isArray(items) || items.length === 0) {
    return <Label className="text-sm font-medium">{label}</Label>
  }

  return (
    <div ref={containerRef} className="relative flex items-center gap-2">
      <Label className="text-sm font-medium">{label}</Label>
      <button
        type="button"
        className="inline-flex h-5 w-5 items-center justify-center rounded-full text-muted-foreground transition hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-label={`${label} help`}
        onClick={() => setOpen((current) => !current)}
      >
        <CircleHelp className="h-4 w-4" />
      </button>
      {open ? (
        <div className="absolute left-0 top-full z-30 mt-2 w-72 rounded-md border border-border bg-background p-3 text-xs leading-relaxed text-muted-foreground shadow-md">
          <div className="space-y-1.5">
            {items.map((item) => (
              <div key={item.label}>
                <span className="font-medium text-foreground/80">{item.label}:</span>{' '}
                <span>{item.text}</span>
              </div>
            ))}
          </div>
        </div>
      ) : null}
    </div>
  )
}

// Text requests only: an ID request sends no title, so it has no title language
// to pick.
// TitleLanguagesField is the acceptance titles: the metadata titles a release
// name may match. It is a genuine multi-select, because acceptance genuinely
// takes several — the single language a query goes out under lives on the
// attempt that sends it.
function TitleLanguagesField({ languages, onChange, error }) {
  const selected = normalizeSearchTitleLanguages(languages)
  const available = remainingTitleLanguageOptions(selected)

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-3">
        <LabelWithHelp label="Accepted titles" items={ACCEPT_TITLES_HINT_ITEMS} />
        <DropdownMenu>
          <Tooltip>
            <TooltipTrigger asChild>
              <DropdownMenuTrigger asChild>
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  className={`h-8 w-8 ${error ? 'border-destructive text-destructive' : ''}`}
                  disabled={available.length === 0}
                >
                  <Plus className="h-4 w-4" />
                </Button>
              </DropdownMenuTrigger>
            </TooltipTrigger>
            <TooltipContent>
              {available.length === 0 ? 'Every language is already accepted' : 'Accept another title'}
            </TooltipContent>
          </Tooltip>
          <DropdownMenuContent align="end" className="max-h-80 w-60 overflow-y-auto">
            {available.map((option) => (
              <DropdownMenuItem key={option.value || 'original'} onClick={() => onChange([...selected, option.value])}>
                {option.label}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <div className={`min-h-12 rounded-md border px-3 py-2 ${error ? 'border-destructive/60 bg-destructive/5' : 'border-border/60'} flex items-center`}>
        <div className="flex w-full flex-wrap items-center gap-2">
          {selected.length === 0 ? (
            <span className="text-xs text-muted-foreground">Falls back to each attempt&apos;s own language</span>
          ) : (
            selected.map((language) => (
              <Badge key={language || 'original'} variant="secondary" className="flex items-center gap-1 rounded-full px-3 py-1">
                <span>{titleLanguageLabel(language)}</span>
                <button
                  type="button"
                  className="text-muted-foreground transition hover:text-foreground"
                  aria-label={`Remove ${titleLanguageLabel(language)}`}
                  onClick={() => onChange(selected.filter((value) => value !== language))}
                >
                  <X className="h-3 w-3" />
                </button>
              </Badge>
            ))
          )}
        </div>
      </div>
      {error ? <p className="text-xs text-destructive">{error}</p> : null}
    </div>
  )
}

// ToggleRow is the two-state switch the Accept section is made of: a label with
// help on the left, an on/off select on the right, matching every other control
// in this dialog.
function ToggleRow({ label, items, value, onChange, onLabel = 'On', offLabel = 'Off', className }) {
  return (
    <div className="flex flex-col gap-3 min-[360px]:flex-row min-[360px]:items-center min-[360px]:gap-4">
      <div className="min-w-0 min-[360px]:flex-1">
        <LabelWithHelp label={label} items={items} />
      </div>
      <div className={className}>
        <select
          className="flex h-9 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2"
          value={value ? 'on' : 'off'}
          onChange={(event) => onChange(event.target.value === 'on')}
        >
          <option value="on">{onLabel}</option>
          <option value="off">{offLabel}</option>
        </select>
      </div>
    </div>
  )
}

const attemptSelectClass = "h-8 rounded-md border border-input bg-background px-2 text-xs ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-1"

// AttemptRow is one question in the plan. Everything it needs is on the row —
// address, target, and for a title attempt the language it queries under and
// whether the year rides along — because that is exactly what gets dispatched.
function AttemptRow({ kind, index, attempt, onChange, onRemove, canRemove }) {
  const address = normalizeAddress(attempt.address)
  const target = normalizeTarget(attempt.target)
  const isTitle = address === ADDRESS_TITLE
  const isSeries = isSeriesKind(kind)
  const update = (patch) => onChange(normalizeAttempt({ ...attempt, ...patch }, kind))

  return (
    <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
      <span className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-muted text-xs font-semibold text-muted-foreground">
        {index + 1}
      </span>
      <select
        className={attemptSelectClass}
        value={address}
        onChange={(event) => update({ address: event.target.value })}
        aria-label={`Attempt ${index + 1} address`}
      >
        {ADDRESS_OPTIONS.map((option) => (
          <option key={option.value} value={option.value}>{option.label}</option>
        ))}
      </select>
      {isSeries ? (
        <select
          className={attemptSelectClass}
          value={target}
          onChange={(event) => update({ target: event.target.value })}
          aria-label={`Attempt ${index + 1} target`}
        >
          {TARGET_OPTIONS.map((option) => (
            <option key={option.value} value={option.value} disabled={option.value === TARGET_ABSOLUTE && !isTitle}>
              {option.label}
            </option>
          ))}
        </select>
      ) : null}
      {isTitle ? (
        <select
          className={attemptSelectClass}
          value={normalizeSearchTitleLanguage(attempt.title ?? '')}
          onChange={(event) => update({ title: event.target.value })}
          aria-label={`Attempt ${index + 1} title language`}
        >
          {TITLE_LANGUAGE_OPTIONS.map((option) => (
            <option key={option.value || 'original'} value={option.value}>{option.label}</option>
          ))}
        </select>
      ) : null}
      {isTitle ? (
        <Button
          type="button"
          variant={attempt.year ? 'secondary' : 'outline'}
          className="h-8 px-2 text-xs font-normal"
          onClick={() => update({ year: !attempt.year })}
        >
          {attempt.year ? '+ year' : 'no year'}
        </Button>
      ) : null}
      {isSeries && target === TARGET_ABSOLUTE ? (
        <Badge variant="outline" className="rounded-full px-2 py-0 text-[11px] font-normal text-muted-foreground">anime only</Badge>
      ) : null}
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="ml-auto h-8 w-8 text-muted-foreground hover:text-destructive"
            onClick={onRemove}
            disabled={!canRemove}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </TooltipTrigger>
        <TooltipContent>{canRemove ? 'Remove attempt' : 'A request needs at least one attempt'}</TooltipContent>
      </Tooltip>
    </div>
  )
}

// AttemptChain is the plan read back as one line — what the request will
// actually ask, in the order it will ask it. It is the answer to the question
// the old two-dropdown form could not answer: what does this run?
function AttemptChain({ kind, attempts, order, seasonCompleted = false }) {
  const chain = attemptsInRunOrder(attempts, order, kind, seasonCompleted)
  if (chain.length === 0) return null
  return (
    <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
      {chain.map((attempt, index) => (
        <span key={attemptKey(attempt, index)} className="flex items-center gap-1.5">
          {index > 0 ? <ArrowRight className="h-3 w-3 shrink-0 opacity-60" /> : null}
          <span className="rounded-full border border-border px-2 py-0.5">{attemptLabel(attempt, kind)}</span>
        </span>
      ))}
    </div>
  )
}

function QueryDraftFields({ kind, draft, setDraft, editing = false, fieldErrors = {} }) {
  const isSeries = isSeriesKind(kind)
  const attempts = normalizeAttempts(draft.attempts, kind)
  const accept = normalizeAccept(kind, draft.accept)
  const stop = normalizeStop(draft.stop)
  const order = normalizeOrder(draft.order)

  const update = (patch) => setDraft((current) => ({ ...current, ...patch }))
  const setAttempts = (next) => update({ attempts: next })
  const updateAccept = (patch) => update({ accept: { ...accept, ...patch } })

  const fieldClass = (key) => fieldErrors[key] ? "border-destructive focus-visible:ring-destructive" : ""
  const rowClass = "space-y-3"
  const inlineRowClass = "flex flex-col gap-3 min-[360px]:flex-row min-[360px]:items-center min-[360px]:gap-4"
  const inlineLabelClass = "min-w-0 min-[360px]:flex-1"
  const controlBaseClass = "w-full min-[360px]:ml-auto min-[360px]:shrink-0"
  const controlWideClass = `${controlBaseClass} min-[360px]:w-[14rem]`
  const controlMediumClass = `${controlBaseClass} min-[360px]:w-[13rem]`
  const controlNarrowClass = `${controlBaseClass} min-[360px]:w-[9rem]`
  const sectionCardClass = "rounded-lg border border-border/60 bg-background/80"
  const attemptsError = fieldErrors.attempts

  return (
    <div className="space-y-5">
      <div className={`${sectionCardClass} p-3`}>
        <div className={rowClass}>
          <div className={inlineRowClass}>
            <div className={inlineLabelClass}>
              <Label className="text-sm font-medium">Name</Label>
            </div>
            <div className={controlWideClass}>
              <Input className={`h-9 ${fieldClass('name')}`} value={draft.name || ''} onChange={(event) => update({ name: event.target.value })} placeholder={kind === 'movie' ? 'MovieQuery01' : 'TVQuery01'} disabled={editing} />
            </div>
          </div>
        </div>
      </div>

      <div className={sectionCardClass}>
        <div className="space-y-3 p-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <LabelWithHelp label="Attempts" items={ATTEMPTS_HINT_ITEMS} />
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button type="button" variant="outline" className="h-8 gap-1.5 px-2 text-xs font-normal">
                  <Sparkles className="h-3.5 w-3.5" />
                  Preset
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-72">
                {planPresets(kind).map((preset) => (
                  <DropdownMenuItem
                    key={preset.id}
                    className="flex flex-col items-start gap-0.5"
                    onClick={() => update(presetPlan(kind, preset.id))}
                  >
                    <span className="font-medium">{preset.label}</span>
                    <span className="text-xs text-muted-foreground">{preset.description}</span>
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          </div>

          <div className="space-y-2">
            <SortableList
              ids={attempts.map((attempt, index) => attemptKey(attempt, index))}
              onMove={(from, to) => setAttempts(moveItem(attempts, from, to))}
              disabled={attempts.length < 2}
            >
              {attempts.map((attempt, index) => (
                <SortableRow
                  key={attemptKey(attempt, index)}
                  id={attemptKey(attempt, index)}
                  disabled={attempts.length < 2}
                  className="mb-2 bg-background/60"
                >
                  <AttemptRow
                    kind={kind}
                    index={index}
                    attempt={attempt}
                    canRemove={attempts.length > 1}
                    onChange={(next) => setAttempts(attempts.map((current, at) => (at === index ? next : current)))}
                    onRemove={() => setAttempts(attempts.filter((_, at) => at !== index))}
                  />
                </SortableRow>
              ))}
            </SortableList>
            {attempts.length === 0 ? (
              <p className={`text-sm ${attemptsError ? 'text-destructive' : 'text-muted-foreground'}`}>
                No attempts yet — this request would ask nothing.
              </p>
            ) : null}
            <Button
              type="button"
              variant="outline"
              className="h-8 w-full gap-1.5 border-dashed text-xs font-normal"
              onClick={() => setAttempts([...attempts, defaultAttempt(kind)])}
            >
              <Plus className="h-3.5 w-3.5" />
              Add attempt
            </Button>
            {attemptsError ? <p className="text-xs text-destructive">{attemptsError}</p> : null}
          </div>
        </div>

        <div className="relative p-3">
          <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
          <div className={rowClass}>
            <div className={inlineRowClass}>
              <div className={inlineLabelClass}>
                <LabelWithHelp label="When to stop" items={STOP_HINT_ITEMS} />
              </div>
              <div className={controlMediumClass}>
                <select
                  className={`flex h-9 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 ${fieldClass('stop')}`}
                  value={stop}
                  onChange={(event) => update({ stop: event.target.value })}
                >
                  {STOP_OPTIONS.map((option) => (
                    <option key={option.value} value={option.value}>{option.label}</option>
                  ))}
                </select>
              </div>
            </div>
          </div>
        </div>

        {isSeries ? (
          <div className="relative p-3">
            <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
            <div className={rowClass}>
              <div className={inlineRowClass}>
                <div className={inlineLabelClass}>
                  <LabelWithHelp label="Ordering" items={ORDER_HINT_ITEMS} />
                </div>
                <div className={controlWideClass}>
                  <select
                    className={`flex h-9 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 ${fieldClass('order')}`}
                    value={order}
                    onChange={(event) => update({ order: event.target.value })}
                  >
                    {ORDER_OPTIONS.map((option) => (
                      <option key={option.value} value={option.value}>{option.label}</option>
                    ))}
                  </select>
                </div>
              </div>
            </div>
          </div>
        ) : null}

        {attempts.length > 0 ? (
          <div className="relative space-y-2 p-3">
            <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
            <p className="text-xs font-medium text-muted-foreground">
              {stop === STOP_ALL ? 'This request asks all of' : 'This request asks, until something answers'}
            </p>
            <AttemptChain kind={kind} attempts={attempts} order={order} />
            {isSeries && order === ORDER_ADAPTIVE_SEASON ? (
              <div className="space-y-1.5 pt-1">
                <p className="text-xs font-medium text-muted-foreground">Once the season has finished airing</p>
                <AttemptChain kind={kind} attempts={attempts} order={order} seasonCompleted />
              </div>
            ) : null}
          </div>
        ) : null}
      </div>

      <div className={sectionCardClass}>
        <div className="p-3">
          <TitleLanguagesField
            languages={accept.titles}
            onChange={(next) => updateAccept({ titles: normalizeSearchTitleLanguages(next) })}
            error={fieldErrors['accept.titles']}
          />
        </div>
        <div className="relative p-3">
          <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
          <ToggleRow
            label="Year must match"
            items={ACCEPT_YEAR_HINT_ITEMS}
            value={accept.year}
            onChange={(value) => updateAccept({ year: value })}
            onLabel="Must match"
            offLabel="Ignored"
            className={controlMediumClass}
          />
        </div>
        {isSeries ? (
          <div className="relative p-3">
            <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
            <ToggleRow
              label="Season packs"
              items={ACCEPT_PACKS_HINT_ITEMS}
              value={accept.packs}
              onChange={(value) => updateAccept({ packs: value })}
              onLabel="Accepted"
              offLabel="Rejected"
              className={controlMediumClass}
            />
          </div>
        ) : null}
      </div>

      <div className={sectionCardClass}>
        <div className="p-3">
          <div className={rowClass}>
            <div className={inlineRowClass}>
              <div className={inlineLabelClass}>
                <LabelWithHelp label="Limit" items={SEARCH_LIMIT_HINT_ITEMS} />
              </div>
              <div className={controlNarrowClass}>
                <Input
                  type="number"
                  min={0}
                  max={5000}
                  placeholder="Max"
                  className={`h-9 ${fieldClass('search_result_limit')}`}
                  value={Number(draft.search_result_limit || 0) === 0 ? '' : draft.search_result_limit}
                  onChange={(event) => update({ search_result_limit: event.target.value === '' ? 0 : Number(event.target.value) })}
                />
              </div>
            </div>
          </div>
        </div>
        <div className="relative p-3">
          <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
          <div className={rowClass}>
            <div className={inlineRowClass}>
              <div className={inlineLabelClass}>
                <LabelWithHelp label="Categories" items={CATEGORIES_HINT_ITEMS} />
              </div>
              <div className={controlMediumClass}>
                <Input
                  className={`h-9 ${fieldClass('categories')}`}
                  value={draft.categories ?? ''}
                  onChange={(event) => update({ categories: event.target.value })}
                  placeholder="From indexer caps"
                />
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

function dialogTitle(kind, editing) {
  if (editing) return kind === 'movie' ? 'Change Movie Request' : 'Change TV Request'
  return kind === 'movie' ? 'Add Movie Request' : 'Add TV Request'
}

function dialogDescription(kind) {
  return kind === 'movie'
    ? 'The questions this request asks indexers for a movie, in order.'
    : 'The questions this request asks indexers for an episode, in order.'
}

function defaultQueryName(kind, index) {
  return kind === 'movie' ? `MovieQuery${String(index + 1).padStart(2, '0')}` : `TVQuery${String(index + 1).padStart(2, '0')}`
}

function QueryDialog({ open, onOpenChange, kind, initialValue, existingNames = [], existingQueries = [], onSave, saveLabel, editing = false, nextIndex = 0, onClearStatus }) {
  const dialog = useEntityDialog({
    open,
    onOpenChange,
    initialValue,
    makeDraft: () => {
      const nextDraft = normalizeDraft(kind, initialValue)
      if (!editing && (!nextDraft.name || nextDraft.name === 'MovieQuery01' || nextDraft.name === 'TVQuery01')) {
        nextDraft.name = defaultQueryName(kind, nextIndex)
      }
      return nextDraft
    },
    normalize: (value) => normalizeDraft(kind, value),
    onClearStatus,
  })
  const { draft, setDraft, fieldErrors } = dialog

  const duplicateName = existingNames.some((name) => normalizeName(name) === normalizeName(draft.name))
  const duplicateQueryName = findDuplicateQueryName(kind, draft, existingQueries)
  const duplicateQuery = Boolean(duplicateQueryName)

  const handleSave = () => {
    const next = persistableDraft(kind, draft)
    const limit = Number(next.search_result_limit)
    return dialog.runSave({
      validate: () => {
        const nextFieldErrors = {}
        if (!next.name) nextFieldErrors.name = 'Name is required.'
        if (duplicateName) nextFieldErrors.name = 'Name already exists.'
        if (duplicateQuery) nextFieldErrors.name = `An identical search request already exists: "${duplicateQueryName}".`
        if (next.attempts.length === 0) {
          nextFieldErrors.attempts = 'Add at least one attempt.'
        }
        if (Number.isNaN(limit) || limit < 0) {
          nextFieldErrors.search_result_limit = 'Limit must be 0 or greater.'
        }
        return nextFieldErrors
      },
      commit: () => {
        next.search_result_limit = limit
        return onSave(next)
      },
      mapError: (error) => extractScopedQueryFieldErrors(error?.fieldErrors, kind, nextIndex),
    })
  }

  return (
    <EntityDialog
      dialog={dialog}
      open={open}
      onOpenChange={onOpenChange}
      title={dialogTitle(kind, editing)}
      description={dialogDescription(kind)}
      saveLabel={saveLabel}
      savingLabel="Saving..."
      onSave={handleSave}
      discardDescription="Your unsaved search request changes will be lost."
      bannerError={dialog.saveError}
    >
      <QueryDraftFields kind={kind} draft={draft} setDraft={setDraft} editing={editing} fieldErrors={fieldErrors} />
    </EntityDialog>
  )
}

function QuerySection({ title, description, kind, items, names, update, remove, watch, streamsByName, onPersist, onCreate, onStatus, onClearStatus }) {
  const [editingId, setEditingId] = useState(null)
  const [copyDraft, setCopyDraft] = useState(null)
  const [deleteTarget, setDeleteTarget] = useState(null)
  const [deleteBlockedName, setDeleteBlockedName] = useState('')
  const existingQueries = items.map((item) => normalizeDraft(kind, watch(item.prefix) || item.field))
  const buildPersistPayload = (nextQueries) => (
    kind === 'movie'
      ? { movie_search_queries: nextQueries.map((query) => persistableDraft(kind, query)) }
      : { series_search_queries: nextQueries.map((query) => persistableDraft(kind, query)) }
  )

  const handleDelete = async (queryName, index) => {
    let assignedStreams = []
    try {
      const liveStreams = await apiFetch('/api/streams')
      assignedStreams = assignedStreamsForQuery(mapStreamsByUsername(liveStreams), kind, queryName)
    } catch {
      assignedStreams = assignedStreamsForQuery(streamsByName, kind, queryName)
    }

    if (assignedStreams.length > 0) {
      setDeleteBlockedName(queryName || '')
      onStatus?.({
        type: 'error',
        message: `Query "${queryName}" cannot be deleted while assigned to stream(s): ${assignedStreams.join(', ')}`
      })
      return
    }

    setDeleteBlockedName('')
    const nextQueries = items
      .filter((_, currentIndex) => currentIndex !== index)
      .map((item) => normalizeDraft(kind, watch(item.prefix) || item.field))
    try {
      await onPersist?.(buildPersistPayload(nextQueries))
      remove(index)
      onStatus?.({
        type: 'success',
        message: `${kind === 'movie' ? 'Movie' : 'Show'} query "${queryName}" deleted successfully.${CACHE_CLEARED_SUFFIX}`
      })
    } catch (error) {
      onStatus?.({
        type: 'error',
        message: error?.message || `Failed to delete query "${queryName}".`,
      })
    }
  }

  return (
    <Card>
      <CardHeader>
        <div className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-3">
          <div className="min-w-0 space-y-0.5">
            <CardTitle>{title}</CardTitle>
            <CardDescription className="break-words">{description}</CardDescription>
          </div>
          <AddQueryButton
            kind={kind}
            title={kind === 'movie' ? 'Add Movie Query' : 'Add Show Query'}
            existingNames={names}
            existingQueries={existingQueries}
            onCreate={onCreate}
            onPersist={onPersist}
            onStatus={onStatus}
            onClearStatus={onClearStatus}
          />
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        {items.length === 0 ? (
          <p className="text-sm text-muted-foreground">No queries configured yet.</p>
        ) : (
          items.map(({ field, index, prefix }) => {
            const query = normalizeDraft(kind, watch(prefix) || field)
            const summary = summarizeQuery(query, kind)
            const editNames = names.filter((name, nameIndex) => nameIndex !== index)
            const editQueries = items
              .filter((item) => item.field.id !== field.id)
              .map((item) => normalizeDraft(kind, watch(item.prefix) || item.field))
            return (
              <Card className={deleteBlockedName && deleteBlockedName === query.name ? 'border-destructive/60 ring-1 ring-destructive/30' : ''} key={field.id}>
                <CardContent className="pt-6">
                  <div className="space-y-3">
                    <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                      <div className="flex items-center gap-2 self-end sm:order-2">
                        <Tooltip>
                          <TooltipTrigger asChild>
                            <Button type="button" variant="outline" size="icon" className="h-9 w-9" onClick={() => {
                              setDeleteBlockedName('')
                              onClearStatus?.()
                              setEditingId(field.id)
                            }}>
                              <Settings className="h-4 w-4" />
                            </Button>
                          </TooltipTrigger>
                          <TooltipContent>Edit query</TooltipContent>
                        </Tooltip>
                        <Tooltip>
                          <TooltipTrigger asChild>
                            <Button
                              type="button"
                              variant="outline"
                              size="icon"
                              className="h-9 w-9"
                              onClick={() => setCopyDraft({
                                ...query,
                                name: defaultQueryName(kind, names.length),
                              })}
                            >
                              <Copy className="h-4 w-4" />
                            </Button>
                          </TooltipTrigger>
                          <TooltipContent>Copy query</TooltipContent>
                        </Tooltip>
                        <Tooltip>
                          <TooltipTrigger asChild>
                            <Button type="button" variant="destructive" size="icon" className="h-9 w-9" onClick={() => setDeleteTarget({ name: query.name, index })}>
                              <Trash2 className="h-4 w-4" />
                            </Button>
                          </TooltipTrigger>
                          <TooltipContent>Delete query</TooltipContent>
                        </Tooltip>
                      </div>
                      <div className="min-w-0 font-semibold sm:order-1">{query.name || defaultQueryName(kind, index)}</div>
                    </div>
                    {!summary.primary.length && !summary.validation.length && !summary.extra.length ? (
                      <p className="text-sm text-muted-foreground">No values set.</p>
                    ) : (
                      <div className="space-y-3">
                        <CompactRow items={summary.primary} />
                        <CompactRow items={summary.validation} />
                        <CompactRow items={summary.extra} />
                      </div>
                    )}
                    <UsageChips labels={assignedStreamsForQuery(streamsByName, kind, query.name)} />
                  </div>
                </CardContent>
                <QueryDialog
                  open={editingId === field.id}
                  onOpenChange={(nextOpen) => {
                    if (!nextOpen) {
                      setDeleteBlockedName('')
                    }
                    setEditingId(nextOpen ? field.id : null)
                  }}
                  kind={kind}
                  initialValue={query}
                  existingNames={editNames}
                  existingQueries={editQueries}
                  saveLabel="Save"
                  editing
                  nextIndex={index}
                  onClearStatus={onClearStatus}
                  onSave={async (next) => {
                    const nextQueries = items.map((item, currentIndex) => (
                      currentIndex === index
                        ? normalizeDraft(kind, next)
                        : normalizeDraft(kind, watch(item.prefix) || item.field)
                    ))
                    await onPersist?.(buildPersistPayload(nextQueries))
                    update(index, next)
                    setDeleteBlockedName('')
                    onStatus?.({
                      type: 'success',
                      message: `${kind === 'movie' ? 'Movie' : 'Show'} query "${next.name}" saved successfully.${CACHE_CLEARED_SUFFIX}`
                    })
                  }}
                />
              </Card>
            )
          })
        )}
      </CardContent>
      <QueryDialog
        open={copyDraft !== null}
        onOpenChange={(nextOpen) => {
          if (!nextOpen) {
            setCopyDraft(null)
          }
        }}
        kind={kind}
        initialValue={copyDraft || emptyDraft(kind)}
        existingNames={names}
        existingQueries={existingQueries}
        saveLabel="Save"
        nextIndex={names.length}
        onClearStatus={onClearStatus}
        onSave={async (next) => {
          const nextQueries = [...existingQueries, normalizeDraft(kind, next)]
          await onPersist?.(buildPersistPayload(nextQueries))
          onCreate(next)
          setDeleteBlockedName('')
          onStatus?.({
            type: 'success',
            message: `${kind === 'movie' ? 'Movie' : 'Show'} query "${next.name}" created successfully.${CACHE_CLEARED_SUFFIX}`
          })
          setCopyDraft(null)
        }}
      />
      <ConfirmDialog
        open={Boolean(deleteTarget)}
        onOpenChange={(nextOpen) => {
          if (!nextOpen) setDeleteTarget(null)
        }}
        title="Delete search request?"
        description={deleteTarget ? `Are you sure you want to delete query "${deleteTarget.name}"?` : ''}
        confirmLabel="Delete"
        onConfirm={() => {
          const target = deleteTarget
          setDeleteTarget(null)
          if (target) {
            void handleDelete(target.name, target.index)
          }
        }}
      />
    </Card>
  )
}

function AddQueryButton({ kind, title, existingNames, existingQueries, onCreate, onPersist, onStatus, onClearStatus }) {
  const [open, setOpen] = useState(false)

  return (
    <>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button type="button" variant="destructive" size="icon" className="h-9 w-9 shrink-0" onClick={() => setOpen(true)}>
            <Plus className="h-4 w-4" />
          </Button>
        </TooltipTrigger>
        <TooltipContent>{title}</TooltipContent>
      </Tooltip>
      <QueryDialog
        open={open}
        onOpenChange={(nextOpen) => {
          setOpen(nextOpen)
        }}
        kind={kind}
        initialValue={emptyDraft(kind)}
        existingNames={existingNames}
        existingQueries={existingQueries}
        saveLabel="Save"
        nextIndex={existingNames.length}
        onClearStatus={onClearStatus}
        onSave={async (next) => {
          const nextQueries = [...existingQueries, normalizeDraft(kind, next)]
          await onPersist?.(
            kind === 'movie'
              ? { movie_search_queries: nextQueries }
              : { series_search_queries: nextQueries }
          )
          onCreate(next)
          onStatus?.({
            type: 'success',
            message: `${kind === 'movie' ? 'Movie' : 'Show'} query "${next.name}" created successfully.${CACHE_CLEARED_SUFFIX}`
          })
        }}
      />
    </>
  )
}

export const SearchQuerySettings = React.memo(function SearchQuerySettings({
  watch,
  movieFields,
  seriesFields,
  appendMovie,
  appendSeries,
  updateMovie,
  updateSeries,
  removeMovie,
  removeSeries,
  streamsByName = {},
  onPersist,
  onStatus,
  onClearStatus,
}) {
  const movieItems = useMemo(() => movieFields.map((field, index) => ({ field, index, prefix: `movie_search_queries.${index}` })), [movieFields])
  const seriesItems = useMemo(() => seriesFields.map((field, index) => ({ field, index, prefix: `series_search_queries.${index}` })), [seriesFields])
  const movieNames = useMemo(() => movieFields.map((field, index) => (watch(`movie_search_queries.${index}.name`) || field.name || '')).filter(Boolean), [movieFields, watch])
  const seriesNames = useMemo(() => seriesFields.map((field, index) => (watch(`series_search_queries.${index}.name`) || field.name || '')).filter(Boolean), [seriesFields, watch])

  useEffect(() => () => {
    onClearStatus?.()
  }, [onClearStatus])

  return (
    <TooltipProvider delayDuration={100}>
    <div className="space-y-4">
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <QuerySection
          title="Movie"
          description="Build your search requests for movies."
          kind="movie"
          items={movieItems}
          names={movieNames}
          update={updateMovie}
          remove={removeMovie}
          watch={watch}
          streamsByName={streamsByName}
          onPersist={onPersist}
          onCreate={(query) => appendMovie(query)}
          onStatus={onStatus}
          onClearStatus={onClearStatus}
        />
        <QuerySection
          title="TV"
          description="Build your search requests for TV."
          kind="series"
          items={seriesItems}
          names={seriesNames}
          update={updateSeries}
          remove={removeSeries}
          watch={watch}
          streamsByName={streamsByName}
          onPersist={onPersist}
          onCreate={(query) => appendSeries(query)}
          onStatus={onStatus}
          onClearStatus={onClearStatus}
        />
      </div>
    </div>
    </TooltipProvider>
  )
})
