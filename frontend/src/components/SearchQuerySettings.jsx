import React, { useEffect, useMemo, useState } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { apiFetch } from "@/api"
import { Copy, Plus, Settings, Trash2 } from "lucide-react"

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

function normalizeName(value) {
  return (value || '').trim().toLowerCase()
}

function truncateCompactValue(value, maxLength = 28) {
  const text = String(value || '').trim()
  if (text.length <= maxLength) return text
  return `${text.slice(0, maxLength - 3)}...`
}

function assignedStreamsForQuery(streamsByName, kind, queryName) {
  const field = kind === 'movie' ? 'movie_search_queries' : 'series_search_queries'
  const target = normalizeName(queryName)
  if (!target || !streamsByName) return []
  return Object.values(streamsByName)
    .filter(Boolean)
    .filter((stream) => Array.isArray(stream[field]) && stream[field].some((name) => normalizeName(name) === target))
    .map((stream) => stream.username)
}

function mapStreamsByUsername(streams) {
  return (Array.isArray(streams) ? streams : []).reduce((acc, stream) => {
    if (!stream?.username) return acc
    acc[stream.username] = stream
    return acc
  }, {})
}

function emptyDraft(kind) {
  return {
    name: kind === 'movie' ? 'MovieQuery01' : 'TVQuery01',
    search_mode: 'id',
    movie_categories: kind === 'movie' ? '2000' : undefined,
    tv_categories: kind === 'series' ? '5000' : undefined,
    extra_search_terms: '',
    search_result_limit: 1000,
    search_title_language: '',
    include_year_in_text_search: true,
    use_season_episode_params: kind === 'series' ? true : undefined,
  }
}

function normalizeDraft(kind, draft) {
  const base = emptyDraft(kind)
  const value = draft || {}
  return {
    ...base,
    ...value,
    name: (value.name || '').trim(),
    search_mode: value.search_mode || 'id',
    extra_search_terms: value.extra_search_terms || '',
    search_result_limit: value.search_result_limit ?? 1000,
    movie_categories: kind === 'movie' ? (value.movie_categories ?? '2000') : undefined,
    tv_categories: kind === 'series' ? (value.tv_categories ?? '5000') : undefined,
    search_title_language: value.search_title_language || '',
    include_year_in_text_search: value.include_year_in_text_search ?? true,
    use_season_episode_params: kind === 'series' ? (value.use_season_episode_params ?? true) : undefined,
  }
}

function comparableQuerySignature(kind, draft) {
  const value = normalizeDraft(kind, draft)
  return JSON.stringify({
    search_mode: value.search_mode || 'id',
    movie_categories: kind === 'movie' ? String(value.movie_categories ?? '').trim() : '',
    tv_categories: kind === 'series' ? String(value.tv_categories ?? '').trim() : '',
    extra_search_terms: String(value.extra_search_terms || '').trim(),
    search_result_limit: Number(value.search_result_limit || 0),
    search_title_language: value.search_mode === 'text' ? String(value.search_title_language || '').trim() : '',
    include_year_in_text_search: value.search_mode === 'text' ? value.include_year_in_text_search !== false : false,
    use_season_episode_params: kind === 'series' ? value.use_season_episode_params !== false : undefined,
  })
}

function findDuplicateQueryName(kind, draft, queries) {
  const signature = comparableQuerySignature(kind, draft)
  const match = (Array.isArray(queries) ? queries : []).find((query) => comparableQuerySignature(kind, query) === signature)
  return match?.name || ''
}

function summarizeQuery(query, kind) {
  const parts = []
  if (query.search_mode) parts.push(`Mode: ${query.search_mode.toUpperCase()}`)
  if (kind === 'movie' && query.movie_categories) parts.push(`Movie: ${query.movie_categories}`)
  if (kind === 'series' && query.tv_categories) parts.push(`TV: ${query.tv_categories}`)
  if (query.extra_search_terms) parts.push(`Extra: ${truncateCompactValue(query.extra_search_terms)}`)
  if (query.search_result_limit) parts.push(`Limit: ${query.search_result_limit}`)
  if (query.search_mode === 'text') {
    if (query.search_title_language) parts.push(`Lang: ${query.search_title_language}`)
    parts.push(`Year: ${query.include_year_in_text_search === false ? 'Off' : 'On'}`)
  }
  if (kind === 'series') parts.push(`S/E: ${query.use_season_episode_params === false ? 'Query' : 'Param'}`)
  return parts
}


function QueryDraftFields({ kind, draft, setDraft, editing = false, fieldErrors = {} }) {
  const update = (key, value) => {
    setDraft((current) => {
      if (key === 'search_mode' && value === 'id') {
        return {
          ...current,
          search_mode: value,
          search_title_language: '',
          include_year_in_text_search: false,
        }
      }
      return { ...current, [key]: value }
    })
  }
  const fieldClass = (key) => fieldErrors[key] ? "border-destructive focus-visible:ring-destructive" : ""
  const categoryField = kind === 'movie' ? 'movie_categories' : 'tv_categories'
  const rowClass = "space-y-3"
  const topRowClass = "flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4"
  const labelClass = "min-w-0 xl:flex-1"
  const controlWideClass = "w-full xl:max-w-sm"
  const controlNarrowClass = "w-full xl:max-w-[8rem]"

  return (
    <div className="space-y-4">
      <div className="rounded-md border border-border/60 p-3">
        <div className={rowClass}>
          <div className={topRowClass}>
            <div className={labelClass}>
              <Label className="text-sm font-medium">Search Request Name</Label>
            </div>
            <div className={controlWideClass}>
              <Input className={`h-9 ${fieldClass('name')}`} value={draft.name || ''} onChange={(event) => update('name', event.target.value)} placeholder={kind === 'movie' ? 'MovieQuery01' : 'TVQuery01'} disabled={editing} />
            </div>
          </div>
        </div>
      </div>

      <div className="rounded-md border border-border/60">
        <div className="p-3">
          <div className={rowClass}>
            <div className={topRowClass}>
              <div className={labelClass}>
                <Label className="text-sm font-medium">Search Mode</Label>
              </div>
              <div className={controlNarrowClass}>
                <select
                  className={`flex h-9 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 ${fieldClass('search_mode')}`}
                  value={draft.search_mode || 'id'}
                  onChange={(event) => update('search_mode', event.target.value)}
                >
                  <option value="id">ID Search</option>
                  <option value="text">Text Search</option>
                </select>
              </div>
            </div>
          </div>
        </div>
        <div className="relative p-3">
          <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
          <div className={rowClass}>
            <div className={topRowClass}>
              <div className={labelClass}>
                <Label className="text-sm font-medium">Category</Label>
              </div>
              <div className={controlNarrowClass}>
                <Input className={`h-9 ${fieldClass(categoryField)}`} value={kind === 'movie' ? (draft.movie_categories ?? '') : (draft.tv_categories ?? '')} onChange={(event) => update(categoryField, event.target.value)} placeholder={kind === 'movie' ? '2000' : '5000'} />
              </div>
            </div>
          </div>
        </div>
        <div className="relative p-3">
          <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
          <div className={rowClass}>
            <div className={topRowClass}>
              <div className={labelClass}>
                <Label className="text-sm font-medium">Limit</Label>
              </div>
              <div className={controlNarrowClass}>
                <Input
                  type="number"
                  min={0}
                  max={5000}
                  placeholder="1000"
                  className={`h-9 ${fieldClass('search_result_limit')}`}
                  value={draft.search_result_limit ?? ''}
                  onChange={(event) => update('search_result_limit', event.target.value === '' ? '' : Number(event.target.value))}
                />
              </div>
            </div>
          </div>
        </div>
        {kind === 'series' && (
          <div className="relative p-3">
            <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
            <div className={rowClass}>
              <div className={topRowClass}>
                <div className={labelClass}>
                  <Label className="text-sm font-medium">Season/Episode</Label>
                </div>
                <div className={controlNarrowClass}>
                  <select
                    className={`flex h-9 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 ${fieldClass('use_season_episode_params')}`}
                    value={draft.use_season_episode_params === false ? 'off' : 'on'}
                    onChange={(event) => update('use_season_episode_params', event.target.value === 'on')}
                  >
                    <option value="off">Query</option>
                    <option value="on">Param</option>
                  </select>
                </div>
              </div>
            </div>
          </div>
        )}
      </div>

      {draft.search_mode === 'text' && (
        <div className="rounded-md border border-border/60">
          <div className="p-3">
            <div className={rowClass}>
              <div className={topRowClass}>
                <div className={labelClass}>
                  <Label className="text-sm font-medium">Year</Label>
                </div>
                <div className={controlNarrowClass}>
                  <select
                    className={`flex h-9 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 ${fieldClass('include_year_in_text_search')}`}
                    value={draft.include_year_in_text_search === false ? 'off' : 'on'}
                    onChange={(event) => update('include_year_in_text_search', event.target.value === 'on')}
                  >
                    <option value="on">Use in Query</option>
                    <option value="off">Ignore</option>
                  </select>
                </div>
              </div>
            </div>
          </div>
          <div className="relative p-3">
            <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
            <div className={rowClass}>
              <div className={topRowClass}>
                <div className={labelClass}>
                  <Label className="text-sm font-medium">Title Language</Label>
                </div>
                <div className={controlNarrowClass}>
                  <select
                    className={`flex h-9 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 ${fieldClass('search_title_language')}`}
                    value={draft.search_title_language || ''}
                    onChange={(event) => update('search_title_language', event.target.value)}
                  >
                    {TITLE_LANGUAGE_OPTIONS.map((option) => (
                      <option key={option.value || 'original'} value={option.value}>
                        {option.label}
                      </option>
                    ))}
                  </select>
                </div>
              </div>
            </div>
          </div>
        </div>
      )}

      <div className="rounded-md border border-border/60 p-3">
        <div className={rowClass}>
          <div className={topRowClass}>
            <div className={labelClass}>
              <Label className="text-sm font-medium">Extra Terms</Label>
            </div>
            <div className="w-full xl:max-w-xl">
              <Input className={`h-9 ${fieldClass('extra_search_terms')}`} placeholder={'"The Walking Dead" !cam (1080p|720p)'} value={draft.extra_search_terms || ''} onChange={(event) => update('extra_search_terms', event.target.value)} />
            </div>
          </div>
          <p className="text-sm text-muted-foreground">Optional terms for text and ID searches. Use quotes for exact phrases, `!term` to exclude words, `*` as wildcard, `|` or `OR` for alternatives, and parentheses to group like `(1080p|720p)`.</p>
        </div>
      </div>
    </div>
  )
}

function dialogTitle(kind, editing) {
  if (editing) return kind === 'movie' ? 'Change Movie Query' : 'Change TV Query'
  return kind === 'movie' ? 'Add Movie Query' : 'Add TV Query'
}

function dialogDescription(kind) {
  return kind === 'movie'
    ? 'Build your search requests for movies.'
    : 'Build your search requests for TV.'
}

function defaultQueryName(kind, index) {
  return kind === 'movie' ? `MovieQuery${String(index + 1).padStart(2, '0')}` : `TVQuery${String(index + 1).padStart(2, '0')}`
}

function QueryDialog({ open, onOpenChange, kind, initialValue, existingNames = [], existingQueries = [], onSave, saveLabel, editing = false, nextIndex = 0, onClearStatus }) {
  const [draft, setDraft] = useState(() => normalizeDraft(kind, initialValue))
  const [wasOpen, setWasOpen] = useState(open)
  const [validationError, setValidationError] = useState('')
  const [fieldErrors, setFieldErrors] = useState({})
  const [showDiscardConfirm, setShowDiscardConfirm] = useState(false)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (open && !wasOpen) {
      const nextDraft = normalizeDraft(kind, initialValue)
      if (!editing && (!nextDraft.name || nextDraft.name === 'MovieQuery01' || nextDraft.name === 'TVQuery01')) {
        nextDraft.name = defaultQueryName(kind, nextIndex)
      }
      setDraft(nextDraft)
      setValidationError('')
      setFieldErrors({})
    }
    setWasOpen(open)
  }, [open, wasOpen, kind, initialValue, editing, nextIndex])

  const normalizedInitial = JSON.stringify(normalizeDraft(kind, initialValue))
  const normalizedCurrent = JSON.stringify(normalizeDraft(kind, draft))
  const isDirty = normalizedInitial !== normalizedCurrent
  const duplicateName = existingNames.some((name) => normalizeName(name) === normalizeName(draft.name))
  const duplicateQueryName = findDuplicateQueryName(kind, draft, existingQueries)
  const duplicateQuery = Boolean(duplicateQueryName)

  const requestClose = () => {
    if (saving) return
    if (isDirty) {
      setShowDiscardConfirm(true)
      return
    }
    onClearStatus?.()
    onOpenChange(false)
  }

  const handleSave = async () => {
    const next = normalizeDraft(kind, draft)
    const nextFieldErrors = {}
    if (!next.name) nextFieldErrors.name = 'Name is required.'
    if (duplicateName) nextFieldErrors.name = 'Name already exists.'
    if (duplicateQuery) nextFieldErrors.name = `An identical search request already exists: "${duplicateQueryName}".`
    const category = kind === 'movie' ? String(next.movie_categories ?? '').trim() : String(next.tv_categories ?? '').trim()
    const limit = Number(next.search_result_limit)
    if (!category || category === '0') {
      nextFieldErrors[kind === 'movie' ? 'movie_categories' : 'tv_categories'] = 'Category is required.'
    }
    if (!limit || limit < 1) {
      nextFieldErrors.search_result_limit = 'Limit is required and must be greater than 0.'
    }
    if (Object.keys(nextFieldErrors).length > 0) {
      setFieldErrors(nextFieldErrors)
      setValidationError(Object.values(nextFieldErrors)[0])
      return
    }
    setFieldErrors({})
    setValidationError('')
    if (next.search_mode !== 'text') {
      next.search_title_language = ''
      next.include_year_in_text_search = false
    }
    next.search_result_limit = limit
    setSaving(true)
    try {
      await onSave(next)
      onOpenChange(false)
    } catch (error) {
      setValidationError(error?.message || 'Save failed.')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => {
      if (nextOpen) {
        onOpenChange(true)
        return
      }
      requestClose()
    }}>
      <DialogContent className="flex max-h-[85vh] max-w-3xl flex-col overflow-hidden" onOpenAutoFocus={(event) => event.preventDefault()}>
        <DialogHeader>
          <DialogTitle>{dialogTitle(kind, editing)}</DialogTitle>
          <DialogDescription>{dialogDescription(kind)}</DialogDescription>
        </DialogHeader>
        <div className="min-h-0 flex-1 overflow-y-auto pr-1">
          <QueryDraftFields kind={kind} draft={draft} setDraft={setDraft} editing={editing} fieldErrors={fieldErrors} />
        </div>
        <DialogFooter className="flex items-center justify-between gap-3">
          <div className="min-h-9 flex-1">
            {validationError && (
              <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{validationError}</div>
            )}
          </div>
          <div className="flex flex-row items-center justify-end gap-2">
            <Button type="button" variant="outline" onClick={requestClose} disabled={saving}>Cancel</Button>
            <Button type="button" variant="destructive" onClick={() => void handleSave()} disabled={saving}>
              {saving ? 'Saving...' : saveLabel}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
      <ConfirmDialog
        open={showDiscardConfirm}
        onOpenChange={setShowDiscardConfirm}
        title="Discard changes?"
        description="Your unsaved search request changes will be lost."
        confirmLabel="Discard"
        onConfirm={() => {
          setShowDiscardConfirm(false)
          onClearStatus?.()
          onOpenChange(false)
        }}
      />
    </Dialog>
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
      ? { movie_search_queries: nextQueries }
      : { series_search_queries: nextQueries }
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
                    {summary.length === 0 ? (
                      <p className="text-sm text-muted-foreground">No values set.</p>
                    ) : (
                      <div className="flex flex-wrap gap-2 text-xs text-muted-foreground">
                        {summary.map((part) => (
                          <span key={part} className="rounded-full border border-border px-2 py-1">{part}</span>
                        ))}
                      </div>
                    )}
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

export function SearchQuerySettings({
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
}
