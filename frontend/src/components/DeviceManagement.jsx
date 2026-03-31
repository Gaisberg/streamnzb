import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { apiFetch } from "@/api"
import { ArrowUpDown, Check, ChevronDown, ChevronUp, Clipboard, Copy, Globe, GripVertical, Loader2, Plus, RefreshCw, Search, Server, Settings, Trash2 } from "lucide-react"

function mapDevicesByUsername(devices) {
  return (Array.isArray(devices) ? devices : []).reduce((acc, device) => {
    if (!device?.username) return acc
    acc[device.username] = device
    return acc
  }, {})
}

function devicesFromMap(devicesByName) {
  return Object.values(devicesByName || {}).filter(Boolean)
}

function uniquePreserveOrder(values) {
  const seen = new Set()
  const next = []
  ;(Array.isArray(values) ? values : []).forEach((value) => {
    if (!value || seen.has(value)) return
    seen.add(value)
    next.push(value)
  })
  return next
}

function normalizeDeviceDraft(draft) {
  return {
    filter_sorting_mode: draft?.filter_sorting_mode === 'aiostreams' ? 'aiostreams' : 'none',
    indexer_mode: draft?.indexer_mode === 'failover' ? 'failover' : 'combine',
    username: (draft?.username || '').trim(),
    use_availnzb: draft?.use_availnzb !== false,
    combine_results: draft?.combine_results !== false,
    enable_failover: draft?.enable_failover !== false,
    results_mode: draft?.results_mode === 'display_all' ? 'display_all' : 'combined_stream',
    providers: uniquePreserveOrder(draft?.providers),
    indexers: uniquePreserveOrder(draft?.indexers),
    movie_search_queries: uniquePreserveOrder(draft?.movie_search_queries),
    series_search_queries: uniquePreserveOrder(draft?.series_search_queries),
  }
}

function buildDeviceDraft(device) {
  return normalizeDeviceDraft({
    filter_sorting_mode: device?.filter_sorting_mode,
    indexer_mode: device?.indexer_mode,
    username: device?.username || '',
    use_availnzb: device?.use_availnzb,
    combine_results: device?.combine_results,
    enable_failover: device?.enable_failover,
    results_mode: device?.results_mode,
    providers: device?.provider_selections || device?.providers || [],
    indexers: device?.indexer_selections || device?.indexers || Object.keys(device?.indexer_overrides || {}),
    movie_search_queries: device?.movie_search_queries || [],
    series_search_queries: device?.series_search_queries || [],
  })
}

function buildIndexerOverrides(selectedIndexerNames, existingOverrides = {}) {
  return selectedIndexerNames.reduce((acc, name) => {
    acc[name] = existingOverrides?.[name] || {}
    return acc
  }, {})
}

function generalCompactValues(device) {
  return [device?.filter_sorting_mode === 'aiostreams' ? 'AIOStreams' : 'Custom']
}

function generalDetailValues(device) {
  return [
    `AvailNZB ${device?.use_availnzb !== false ? 'On' : 'Off'}`,
    `Failover ${device?.enable_failover !== false ? 'On' : 'Off'}`,
    `Indexers ${(device?.indexer_mode || 'combine') === 'failover' ? 'Failover' : 'Combine'}`,
    `Search ${device?.combine_results !== false ? 'Combine' : 'First hit'}`,
    `Results ${device?.results_mode === 'display_all' ? 'All' : 'Combine'}`,
  ]
}

function filterSortingSummaryValues(device) {
  return [device?.filter_sorting_mode === 'aiostreams' ? 'AIOStreams' : 'None']
}

function filterSortingLabel(value) {
  return value === 'aiostreams' ? 'AIOStreams' : 'None'
}

function searchRequestsLabel(combineResults) {
  return combineResults !== false ? 'Combine all' : 'Stop after first hit'
}

function indexerModeLabel(value) {
  return value === 'failover' ? 'Failover' : 'Combine'
}

function resultsModeLabel(value) {
  return value === 'display_all' ? 'Display all' : 'Combined stream'
}

function applyFilterSortingMode(current, nextMode) {
  const normalizedMode = nextMode === 'aiostreams' ? 'aiostreams' : 'none'
  const nextDraft = {
    ...current,
    filter_sorting_mode: normalizedMode,
  }
  if (normalizedMode === 'aiostreams') {
    nextDraft.indexer_mode = 'combine'
    nextDraft.use_availnzb = true
    nextDraft.combine_results = true
    nextDraft.enable_failover = true
    nextDraft.results_mode = 'display_all'
  }
  return normalizeDeviceDraft(nextDraft)
}

function copyToClipboard(text) {
  if (navigator.clipboard && window.isSecureContext) {
    return navigator.clipboard.writeText(text)
  }
  const textarea = document.createElement('textarea')
  textarea.value = text
  textarea.style.position = 'fixed'
  textarea.style.opacity = '0'
  document.body.appendChild(textarea)
  textarea.select()
  try { document.execCommand('copy') } catch (_) {}
  document.body.removeChild(textarea)
  return Promise.resolve()
}

function SummaryRow({ label, values, icon: Icon }) {
  return (
    <div className="space-y-1">
      <div className="flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
        {Icon && <Icon className="h-3.5 w-3.5" />}
        <span>{label}</span>
      </div>
      {values.length === 0 ? (
        <div className="text-sm text-muted-foreground">None</div>
      ) : (
        <div className="flex flex-col items-start gap-2">
          {values.map((value) => (
            <span key={value} className="inline-flex w-fit items-center justify-center rounded-full border border-border px-2 py-1 text-xs text-muted-foreground">{value}</span>
          ))}
        </div>
      )}
    </div>
  )
}

function SelectionSection({ title, values, selected, onToggle, onMove, error }) {
  const [dragIndex, setDragIndex] = useState(null)
  const [dragOverIndex, setDragOverIndex] = useState(null)
  const selectedValues = useMemo(
    () => uniquePreserveOrder(selected).filter((value) => values.includes(value)),
    [selected, values]
  )
  const availableValues = useMemo(
    () => values.filter((value) => !selectedValues.includes(value)),
    [values, selectedValues]
  )

  const handleDrop = (targetIndex) => {
    if (dragIndex === null || dragIndex === targetIndex) {
      setDragIndex(null)
      setDragOverIndex(null)
      return
    }
    onMove?.(dragIndex, targetIndex)
    setDragIndex(null)
    setDragOverIndex(null)
  }

  const handleDragStart = (event, index) => {
    const row = event.currentTarget.closest('[data-drag-row="true"]')
    if (row) {
      const dragPreview = row.cloneNode(true)
      dragPreview.style.position = 'fixed'
      dragPreview.style.top = '-9999px'
      dragPreview.style.left = '-9999px'
      dragPreview.style.width = `${row.getBoundingClientRect().width}px`
      dragPreview.style.pointerEvents = 'none'
      dragPreview.style.opacity = '0.95'
      dragPreview.style.transform = 'scale(1)'
      dragPreview.style.boxShadow = '0 12px 28px rgba(0, 0, 0, 0.18)'
      dragPreview.style.borderRadius = '10px'
      dragPreview.style.background = 'var(--background)'
      document.body.appendChild(dragPreview)
      event.dataTransfer.setDragImage(dragPreview, 24, 24)
      window.setTimeout(() => {
        if (dragPreview.parentNode) {
          dragPreview.parentNode.removeChild(dragPreview)
        }
      }, 0)
    }
    event.dataTransfer.effectAllowed = 'move'
    setDragIndex(index)
  }

  return (
    <div className={`space-y-3 rounded-md border p-3 ${error ? 'border-destructive/60 bg-destructive/5' : 'border-border/60'}`}>
      <div className="flex items-center justify-between gap-3">
        <Label className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{title}</Label>
        <DropdownMenu>
          <Tooltip>
            <TooltipTrigger asChild>
              <DropdownMenuTrigger asChild>
                <Button type="button" variant="destructive" size="icon" className="h-8 w-8" disabled={availableValues.length === 0}>
                  <Plus className="h-4 w-4" />
                </Button>
              </DropdownMenuTrigger>
            </TooltipTrigger>
            <TooltipContent>{availableValues.length === 0 ? 'No more entries to add' : `Add ${title.toLowerCase()}`}</TooltipContent>
          </Tooltip>
          <DropdownMenuContent align="end" className="max-h-80 w-60 overflow-y-auto">
            {availableValues.length === 0 ? (
              <DropdownMenuItem disabled>No more entries available</DropdownMenuItem>
            ) : (
              availableValues.map((value) => (
                <DropdownMenuItem key={value} onClick={() => onToggle(value, true)}>
                  {value}
                </DropdownMenuItem>
              ))
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      <div className="space-y-2">
        {selectedValues.length === 0 ? (
          <div className={`rounded-md border border-dashed px-3 py-3 text-sm ${error ? 'border-destructive/60 text-destructive' : 'border-border/70 text-muted-foreground'}`}>
            No entries added yet.
          </div>
        ) : (
          selectedValues.map((value, index) => (
            <div
              key={value}
              data-drag-row="true"
              className={`flex items-center gap-3 rounded-md border px-3 py-2 transition ${
                dragIndex === index
                  ? 'border-primary/40 bg-muted/50 opacity-60'
                  : 'border-border/60'
              }`}
            >
              <div
                className={`flex h-9 w-9 shrink-0 cursor-grab items-center justify-center rounded-md border border-dashed transition ${
                  dragOverIndex === index
                    ? 'border-destructive/60 bg-destructive/10 text-destructive'
                    : 'border-border/80 bg-muted/30 text-muted-foreground hover:bg-muted/50'
                }`}
                draggable
                onDragStart={(event) => handleDragStart(event, index)}
                onDragOver={(event) => {
                  event.preventDefault()
                  if (dragOverIndex !== index) setDragOverIndex(index)
                }}
                onDragLeave={() => {
                  if (dragOverIndex === index) setDragOverIndex(null)
                }}
                onDrop={() => handleDrop(index)}
                onDragEnd={() => {
                  setDragIndex(null)
                  setDragOverIndex(null)
                }}
              >
                <GripVertical className="h-4 w-4" />
              </div>
              <div className="min-w-0 flex-1 text-sm font-medium">{value}</div>
              <Button type="button" variant="ghost" size="sm" className="h-8 px-2 text-muted-foreground" onClick={() => onToggle(value, false)}>
                Remove
              </Button>
            </div>
          ))
        )}
      </div>
      {error && <div className="text-sm text-destructive">{error}</div>}
    </div>
  )
}

const STREAM_DIALOG_TABS = [
  { id: 'general', label: 'General' },
  { id: 'providers', label: 'Providers' },
  { id: 'indexers', label: 'Indexers' },
  { id: 'movie', label: 'Movie' },
  { id: 'tv', label: 'TV' },
]

function defaultStreamName(index) {
  return `Stream${String(index + 1).padStart(2, '0')}`
}

function nextStreamName(devices) {
  const existing = new Set((Array.isArray(devices) ? devices : []).map((device) => (device?.username || '').toLowerCase()))
  for (let index = 0; index < 999; index += 1) {
    const candidate = defaultStreamName(index)
    if (!existing.has(candidate.toLowerCase())) {
      return candidate
    }
  }
  return `Stream${Date.now()}`
}

function StreamDialog({ open, onOpenChange, initialDevice, mode = 'edit', providerNames, indexerNames, movieQueryNames, seriesQueryNames, onSave, saving, nextIndex = 0 }) {
  const isEditing = mode === 'edit'
  const [draft, setDraft] = useState(() => buildDeviceDraft(initialDevice))
  const [saveError, setSaveError] = useState('')
  const [fieldErrors, setFieldErrors] = useState({})
  const [activeTab, setActiveTab] = useState('general')
  const [showDiscardConfirm, setShowDiscardConfirm] = useState(false)
  const [wasOpen, setWasOpen] = useState(open)
  const dialogIdentity = `${mode}:${initialDevice?.username || ''}:${nextIndex}`
  const [lastDialogIdentity, setLastDialogIdentity] = useState(dialogIdentity)

  useEffect(() => {
    if (open && (!wasOpen || dialogIdentity !== lastDialogIdentity)) {
      const nextDraft = buildDeviceDraft(initialDevice)
      if (!isEditing && !nextDraft.username) {
        nextDraft.username = defaultStreamName(nextIndex)
      }
      setDraft(nextDraft)
      setSaveError('')
      setFieldErrors({})
      setActiveTab('general')
      setLastDialogIdentity(dialogIdentity)
    }
    setWasOpen(open)
  }, [open, initialDevice, nextIndex, isEditing, wasOpen, dialogIdentity, lastDialogIdentity])

  const normalizedInitial = JSON.stringify(buildDeviceDraft(isEditing ? initialDevice : null))
  const normalizedCurrent = JSON.stringify(normalizeDeviceDraft(draft))
  const isDirty = normalizedInitial !== normalizedCurrent
  const aiostreamsMode = draft.filter_sorting_mode === 'aiostreams'

  const requestClose = () => {
    if (isDirty) {
      setShowDiscardConfirm(true)
      return
    }
    onOpenChange(false)
  }

  const toggleListValue = (field, value, checked) => {
    setDraft((current) => {
      const currentValues = uniquePreserveOrder(current[field])
      const nextValues = checked
        ? uniquePreserveOrder([...currentValues, value])
        : currentValues.filter((entry) => entry !== value)
      return { ...current, [field]: nextValues }
    })
  }

  const moveListValue = (field, fromIndex, toIndex) => {
    setDraft((current) => {
      const nextValues = [...uniquePreserveOrder(current[field])]
      const [moved] = nextValues.splice(fromIndex, 1)
      if (moved === undefined) return current
      nextValues.splice(toIndex, 0, moved)
      return { ...current, [field]: nextValues }
    })
  }

  const handleSave = () => {
    const next = normalizeDeviceDraft(draft)
    const nextFieldErrors = {}
    if (!next.username) {
      nextFieldErrors.username = 'Stream name is required'
    }
    if (next.providers.length === 0) {
      nextFieldErrors.providers = 'Add at least one provider.'
    }
    if (next.indexers.length === 0) {
      nextFieldErrors.indexers = 'Add at least one indexer.'
    }
    if (next.movie_search_queries.length === 0) {
      nextFieldErrors.movie_search_queries = 'Add at least one movie search request.'
    }
    if (next.series_search_queries.length === 0) {
      nextFieldErrors.series_search_queries = 'Add at least one TV search request.'
    }
    if (Object.keys(nextFieldErrors).length > 0) {
      setFieldErrors(nextFieldErrors)
      setSaveError(
        nextFieldErrors.username ||
          nextFieldErrors.providers ||
          nextFieldErrors.indexers ||
          nextFieldErrors.movie_search_queries ||
          nextFieldErrors.series_search_queries ||
          'Please review the highlighted fields.'
      )
      return
    }
    setFieldErrors({})
    setSaveError('')
    onSave(next)
  }

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => {
      if (nextOpen) {
        onOpenChange(true)
        return
      }
      requestClose()
    }}>
      <DialogContent className="flex h-[85vh] max-h-[85vh] max-w-3xl flex-col overflow-visible">
        <DialogHeader>
          <DialogTitle>{isEditing ? 'Change Stream' : 'Add Stream'}</DialogTitle>
          <DialogDescription>Create a stream or manage its provider, indexer, and search request assignments.</DialogDescription>
        </DialogHeader>

        <div className="flex-1 space-y-4 overflow-y-auto px-1">
          <div className="space-y-2">
            <Label>Stream Name</Label>
            <Input
              className={fieldErrors.username ? "border-destructive focus-visible:ring-destructive" : ""}
              value={draft.username || ''}
              onChange={(event) => setDraft((current) => ({ ...current, username: event.target.value }))}
              disabled={isEditing}
              placeholder="Stream01"
            />
          </div>

          <div className="flex items-center gap-1 overflow-x-auto border-b border-border">
            {STREAM_DIALOG_TABS.map((tab) => (
              <button
                key={tab.id}
                type="button"
                onClick={() => setActiveTab(tab.id)}
                className={`relative whitespace-nowrap px-3 py-2 text-sm font-medium transition-colors ${
                  activeTab === tab.id
                    ? fieldErrors[tab.id === 'movie' ? 'movie_search_queries' : tab.id === 'tv' ? 'series_search_queries' : tab.id]
                      ? 'text-destructive after:absolute after:bottom-0 after:inset-x-0 after:h-0.5 after:bg-destructive'
                      : 'text-foreground after:absolute after:bottom-0 after:inset-x-0 after:h-0.5 after:bg-primary'
                    : fieldErrors[tab.id === 'movie' ? 'movie_search_queries' : tab.id === 'tv' ? 'series_search_queries' : tab.id]
                      ? 'text-destructive hover:text-destructive'
                      : 'text-muted-foreground hover:text-foreground'
                }`}
              >
                {tab.label}
              </button>
            ))}
          </div>

          {activeTab === 'general' && (
            <div className="space-y-6">
              <div className="rounded-md border border-border/60 p-3">
                <div className="flex items-center justify-between gap-4">
                  <div className="text-sm font-medium">Filter/Sorting</div>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button type="button" variant="outline" className="h-9 w-40 justify-between">
                        <span>{filterSortingLabel(draft.filter_sorting_mode)}</span>
                        <ChevronDown className="h-4 w-4 text-muted-foreground" />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end" className="w-40">
                      <DropdownMenuItem onClick={() => setDraft((current) => applyFilterSortingMode(current, 'none'))}>
                        None
                      </DropdownMenuItem>
                      <DropdownMenuItem onClick={() => setDraft((current) => applyFilterSortingMode(current, 'aiostreams'))}>
                        AIOStreams
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </div>
                <p className="mt-3 text-sm text-muted-foreground">
                  Apply a predefined stream profile. AIOStreams locks the settings below to the values needed for that client flow.
                </p>
              </div>

              <div className={`relative rounded-md border border-border/60 p-3 ${aiostreamsMode ? 'bg-muted/20' : ''}`}>
                <div className="flex items-center justify-between gap-4">
                  <div className="text-sm font-medium">AvailNZB</div>
                  <Switch
                    checked={draft.use_availnzb}
                    onCheckedChange={(checked) => setDraft((current) => ({ ...current, use_availnzb: checked === true }))}
                    disabled={aiostreamsMode}
                  />
                </div>
                <p className="mt-3 text-sm text-muted-foreground">
                  Use AvailNZB for this stream when AvailNZB is enabled in Network settings.
                </p>
                {aiostreamsMode && <div className="pointer-events-none absolute inset-0 rounded-md bg-background/45" />}
              </div>

              <div className={`relative rounded-md border border-border/60 p-3 ${aiostreamsMode ? 'bg-muted/20' : ''}`}>
                <div className="flex items-center justify-between gap-4">
                  <div className="text-sm font-medium">Failover</div>
                  <Switch
                    checked={draft.enable_failover}
                    onCheckedChange={(checked) => setDraft((current) => ({ ...current, enable_failover: checked === true }))}
                    disabled={aiostreamsMode}
                  />
                </div>
                <p className="mt-3 text-sm text-muted-foreground">
                  If enabled, StreamNZB automatically tries the next release in order when the current NZB fails during playback.
                </p>
                {aiostreamsMode && <div className="pointer-events-none absolute inset-0 rounded-md bg-background/45" />}
              </div>

              <div className={`relative rounded-md border border-border/60 ${aiostreamsMode ? 'bg-muted/20' : ''}`}>
                <div className="p-3">
                  <div className="flex items-center justify-between gap-4">
                    <div className="text-sm font-medium">Indexers</div>
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <Button type="button" variant="outline" className="h-9 w-40 justify-between" disabled={aiostreamsMode}>
                          <span>{indexerModeLabel(draft.indexer_mode)}</span>
                          <ChevronDown className="h-4 w-4 text-muted-foreground" />
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end" className="w-40">
                        <DropdownMenuItem onClick={() => setDraft((current) => ({ ...current, indexer_mode: 'combine' }))}>
                          Combine
                        </DropdownMenuItem>
                        <DropdownMenuItem onClick={() => setDraft((current) => ({ ...current, indexer_mode: 'failover' }))}>
                          Failover
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>
                  <p className="mt-3 text-sm text-muted-foreground">
                    Search all selected indexers together or use them in stream order as fallback chain.
                  </p>
                </div>

                <div className="relative p-3">
                  <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                  <div className="flex items-center justify-between gap-4">
                    <div className="text-sm font-medium">Search requests</div>
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <Button type="button" variant="outline" className="h-9 w-40 justify-between" disabled={aiostreamsMode}>
                          <span>{searchRequestsLabel(draft.combine_results)}</span>
                          <ChevronDown className="h-4 w-4 text-muted-foreground" />
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end" className="w-48">
                        <DropdownMenuItem onClick={() => setDraft((current) => ({ ...current, combine_results: true }))}>
                          Combine all
                        </DropdownMenuItem>
                        <DropdownMenuItem onClick={() => setDraft((current) => ({ ...current, combine_results: false }))}>
                          Stop after first hit
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>
                  <p className="mt-3 text-sm text-muted-foreground">
                    If enabled, results from all search requests are combined. If disabled, requests run in order and stop after the first one that returns results.
                  </p>
                </div>

                <div className="relative p-3">
                  <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                  <div className="flex items-center justify-between gap-4">
                    <div className="text-sm font-medium">Results</div>
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <Button type="button" variant="outline" className="h-9 w-40 justify-between" disabled={aiostreamsMode}>
                          <span>{resultsModeLabel(draft.results_mode)}</span>
                          <ChevronDown className="h-4 w-4 text-muted-foreground" />
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end" className="w-40">
                        <DropdownMenuItem onClick={() => setDraft((current) => ({ ...current, results_mode: 'combined_stream' }))}>
                          Combined stream
                        </DropdownMenuItem>
                        <DropdownMenuItem onClick={() => setDraft((current) => ({ ...current, results_mode: 'display_all' }))}>
                          Display all
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>
                  <p className="mt-3 text-sm text-muted-foreground">
                    Choose whether StreamNZB returns one combined stream or shows every matching result as a separate stream entry.
                  </p>
                </div>

                {aiostreamsMode && <div className="pointer-events-none absolute inset-0 rounded-md bg-background/45" />}
              </div>
            </div>
          )}

          {activeTab === 'providers' && (
            <SelectionSection
              title="Providers"
              values={providerNames}
              selected={draft.providers || []}
              onToggle={(value, checked) => toggleListValue('providers', value, checked)}
              onMove={(fromIndex, toIndex) => moveListValue('providers', fromIndex, toIndex)}
              error={fieldErrors.providers}
            />
          )}

          {activeTab === 'indexers' && (
            <SelectionSection
              title="Indexers"
              values={indexerNames}
              selected={draft.indexers || []}
              onToggle={(value, checked) => toggleListValue('indexers', value, checked)}
              onMove={(fromIndex, toIndex) => moveListValue('indexers', fromIndex, toIndex)}
              error={fieldErrors.indexers}
            />
          )}

          {activeTab === 'movie' && (
            <SelectionSection
              title="Movie Search Requests"
              values={movieQueryNames}
              selected={draft.movie_search_queries || []}
              onToggle={(value, checked) => toggleListValue('movie_search_queries', value, checked)}
              onMove={(fromIndex, toIndex) => moveListValue('movie_search_queries', fromIndex, toIndex)}
              error={fieldErrors.movie_search_queries}
            />
          )}

          {activeTab === 'tv' && (
            <SelectionSection
              title="TV Search Requests"
              values={seriesQueryNames}
              selected={draft.series_search_queries || []}
              onToggle={(value, checked) => toggleListValue('series_search_queries', value, checked)}
              onMove={(fromIndex, toIndex) => moveListValue('series_search_queries', fromIndex, toIndex)}
              error={fieldErrors.series_search_queries}
            />
          )}

        </div>

        <DialogFooter className="flex items-center justify-between gap-3">
          <div className="min-h-9 flex-1">
            {saveError && (
              <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
                {saveError}
              </div>
            )}
          </div>
          <div className="flex flex-col-reverse sm:flex-row sm:justify-end sm:space-x-2">
            <Button type="button" variant="ghost" onClick={requestClose}>Cancel</Button>
            <Button type="button" variant="destructive" onClick={handleSave} disabled={saving}>
              {saving && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
              Save
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
      <ConfirmDialog
        open={showDiscardConfirm}
        onOpenChange={setShowDiscardConfirm}
        title="Discard changes?"
        description="Your unsaved stream changes will be lost."
        confirmLabel="Discard"
        onConfirm={() => {
          setShowDiscardConfirm(false)
          onOpenChange(false)
        }}
      />
    </Dialog>
  )
}

function StreamManagement({ globalConfig, movieSearchQueries = [], seriesSearchQueries = [], initialDevicesByName = {}, onDevicesChange, onStatus }) {
  const initialDevices = useMemo(() => devicesFromMap(initialDevicesByName), [initialDevicesByName])
  const initialDevicesSignature = useMemo(() => JSON.stringify(initialDevices), [initialDevices])
  const initialFetchStartedRef = useRef(false)
  const lastAppliedInitialSignatureRef = useRef(initialDevicesSignature)
  const [devices, setDevices] = useState(() => initialDevices)
  const [loading, setLoading] = useState(false)
  const [actionLoading, setActionLoading] = useState(null)
  const [dialogSaving, setDialogSaving] = useState(false)
  const [error, setError] = useState('')
  const [showAddDialog, setShowAddDialog] = useState(false)
  const [addDialogDraft, setAddDialogDraft] = useState(null)
  const [editingDevice, setEditingDevice] = useState(null)
  const [copiedToken, setCopiedToken] = useState('')
  const [visibleFooterStatus, setVisibleFooterStatus] = useState(null)
  const [footerStatusVisible, setFooterStatusVisible] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState('')
  const [expandedDevices, setExpandedDevices] = useState({})

  const indexerNames = useMemo(
    () => (globalConfig?.indexers || []).map((indexer) => indexer.name).filter(Boolean),
    [globalConfig]
  )
  const providerNames = useMemo(
    () => (globalConfig?.providers || []).map((provider) => provider.name).filter(Boolean),
    [globalConfig]
  )
  const movieQueryNames = useMemo(() => movieSearchQueries.map((query) => query.name).filter(Boolean), [movieSearchQueries])
  const seriesQueryNames = useMemo(() => seriesSearchQueries.map((query) => query.name).filter(Boolean), [seriesSearchQueries])

  useEffect(() => {
    if (initialDevices.length === 0) return
    if (lastAppliedInitialSignatureRef.current === initialDevicesSignature) return
    lastAppliedInitialSignatureRef.current = initialDevicesSignature
    setDevices(initialDevices)
    setLoading(false)
  }, [initialDevices, initialDevicesSignature])

  const showStatus = useCallback((status) => {
    if (status?.type === 'error') {
      setError(status.message || '')
    } else if (status?.type === 'success') {
      setError('')
    } else {
      setError('')
    }
    onStatus?.(status)
  }, [onStatus])

  useEffect(() => {
    if (!visibleFooterStatus?.message) return
    setFooterStatusVisible(true)
    if (visibleFooterStatus.type === 'error') return undefined
    const hideTimer = window.setTimeout(() => setFooterStatusVisible(false), 2200)
    const clearTimer = window.setTimeout(() => setVisibleFooterStatus(null), 2500)
    return () => {
      window.clearTimeout(hideTimer)
      window.clearTimeout(clearTimer)
    }
  }, [visibleFooterStatus])

  const showFooterStatus = useCallback((status) => {
    if (!status?.message) {
      setFooterStatusVisible(false)
      setVisibleFooterStatus(null)
      return
    }
    setVisibleFooterStatus(status)
  }, [])

  const fetchDevices = useCallback(async (showLoader = true) => {
    if (showLoader) setLoading(true)
    try {
      const nextDevices = await apiFetch('/api/streams')
      setDevices(Array.isArray(nextDevices) ? nextDevices : [])
      onDevicesChange?.(mapDevicesByUsername(nextDevices))
      setError('')
    } catch (err) {
      const status = { type: 'error', message: err.message || 'Failed to load streams' }
      showStatus(status)
      showFooterStatus(status)
    } finally {
      if (showLoader) setLoading(false)
    }
  }, [onDevicesChange, showStatus])

  useEffect(() => {
    if (initialFetchStartedRef.current) return
    initialFetchStartedRef.current = true
    fetchDevices(false)
  }, [fetchDevices])

  const getManifestUrl = (token) => {
    const baseUrl = globalConfig?.addon_base_url
      ? globalConfig.addon_base_url.replace(/\/$/, '')
      : window.location.origin
    return `${baseUrl}/${token}/manifest.json`
  }

  const copyManifestUrl = (token) => {
    copyToClipboard(getManifestUrl(token)).then(() => {
      setCopiedToken(token)
      setTimeout(() => setCopiedToken(''), 2000)
    })
  }

  const saveDeviceAssignments = async (username, draft, existingDevice) => {
    const payload = {
      [username]: {
        filter_sorting_mode: draft.filter_sorting_mode,
        indexer_mode: draft.indexer_mode,
        use_availnzb: draft.use_availnzb,
        combine_results: draft.combine_results,
        enable_failover: draft.enable_failover,
        results_mode: draft.results_mode,
        provider_selections: draft.providers || [],
        indexer_selections: draft.indexers || [],
        indexer_overrides: buildIndexerOverrides(draft.indexers, existingDevice?.indexer_overrides),
        movie_search_queries: draft.movie_search_queries || [],
        series_search_queries: draft.series_search_queries || [],
      },
    }
    await apiFetch('/api/streams/configs', {
      method: 'PUT',
      body: JSON.stringify(payload),
    })
  }

  const handleCreateDevice = async (draft) => {
    setDialogSaving(true)
    showStatus(null)
    try {
      await apiFetch('/api/streams', {
        method: 'POST',
        body: JSON.stringify({ username: draft.username }),
      })
      await saveDeviceAssignments(draft.username, draft, null)
      const status = { type: 'success', message: `Stream "${draft.username}" created successfully` }
      showStatus(status)
      showFooterStatus(status)
      setAddDialogDraft(null)
      setShowAddDialog(false)
      await fetchDevices(false)
    } catch (err) {
      const status = { type: 'error', message: err.message || 'Failed to create stream' }
      showStatus(status)
      showFooterStatus(status)
    } finally {
      setDialogSaving(false)
    }
  }

  const handleCloneDevice = (device) => {
    if (!device?.username) return
    const nextName = nextStreamName(devices)
    const draft = normalizeDeviceDraft({
      filter_sorting_mode: device.filter_sorting_mode,
      indexer_mode: device.indexer_mode,
      username: nextName,
      use_availnzb: device.use_availnzb,
      combine_results: device.combine_results,
      enable_failover: device.enable_failover,
      results_mode: device.results_mode,
      providers: device.provider_selections || [],
      indexers: device.indexer_selections || Object.keys(device.indexer_overrides || {}),
      movie_search_queries: device.movie_search_queries || [],
      series_search_queries: device.series_search_queries || [],
    })
    setAddDialogDraft(draft)
    setShowAddDialog(true)
  }

  const handleSaveDevice = async (draft) => {
    if (!editingDevice) return
    setDialogSaving(true)
    showStatus(null)
    try {
      await saveDeviceAssignments(editingDevice.username, draft, editingDevice)
      const status = { type: 'success', message: `Stream "${editingDevice.username}" saved successfully` }
      showStatus(status)
      showFooterStatus(status)
      setEditingDevice(null)
      await fetchDevices(false)
    } catch (err) {
      const status = { type: 'error', message: err.message || 'Failed to save stream' }
      showStatus(status)
      showFooterStatus(status)
    } finally {
      setDialogSaving(false)
    }
  }

  const handleDeleteDevice = async (username) => {
    setActionLoading(`delete-${username}`)
    showStatus(null)
    try {
      await apiFetch(`/api/streams/${encodeURIComponent(username)}`, { method: 'DELETE' })
      const status = { type: 'success', message: `Stream "${username}" deleted successfully` }
      showStatus(status)
      showFooterStatus(status)
      await fetchDevices(false)
    } catch (err) {
      const status = { type: 'error', message: err.message || 'Failed to delete stream' }
      showStatus(status)
      showFooterStatus(status)
    } finally {
      setActionLoading(null)
    }
  }

  const handleRegenerateToken = async (username) => {
    setActionLoading(`regenerate-${username}`)
    showStatus(null)
    try {
      const payload = await apiFetch(`/api/streams/${encodeURIComponent(username)}/regenerate-token`, { method: 'POST' })
      setDevices((prev) => {
        const next = prev.map((device) => device.username === username ? { ...device, token: payload.token } : device)
        onDevicesChange?.(mapDevicesByUsername(next))
        return next
      })
      const status = { type: 'success', message: `Token regenerated for "${username}"` }
      showStatus(status)
      showFooterStatus(status)
    } catch (err) {
      const status = { type: 'error', message: err.message || 'Failed to regenerate token' }
      showStatus(status)
      showFooterStatus(status)
    } finally {
      setActionLoading(null)
    }
  }

  const toggleExpandedDevice = (username) => {
    setExpandedDevices((current) => ({
      ...current,
      [username]: !current[username],
    }))
  }

  return (
    <TooltipProvider delayDuration={100}>
      <Card>
        <CardHeader>
          <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
            <div className="space-y-0.5">
              <CardTitle>Streams</CardTitle>
              <CardDescription>Configure stream-specific manifests and their provider, indexer and search order.</CardDescription>
            </div>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  type="button"
                  variant="destructive"
                  size="icon"
                  className="h-9 w-9"
                  onClick={() => {
                    setAddDialogDraft(null)
                    setShowAddDialog(true)
                  }}
                >
                  <Plus className="h-4 w-4 shrink-0" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Add Stream</TooltipContent>
            </Tooltip>
          </div>
        </CardHeader>
        <CardContent className="space-y-4">
          {loading && devices.length > 0 ? (
            <div className="flex items-center justify-center p-8"><Loader2 className="h-6 w-6 animate-spin" /></div>
          ) : devices.length === 0 ? (
            <div className="p-8 text-center text-muted-foreground">No streams found. Create your first stream to get started.</div>
          ) : (
            <div className="space-y-4">
              {devices.map((device) => (
                <Card key={device.username}>
                  <CardContent className="pt-6">
                    <div className="space-y-4">
                      <div className="space-y-3">
                        <div className="flex items-center justify-between gap-3">
                          <div className="min-w-0 flex-1">
                            <h3 className="font-semibold">{device.username}</h3>
                          </div>
                          <div className="flex flex-wrap items-center gap-2 sm:ml-4 sm:shrink-0">
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Button type="button" variant="outline" size="icon" onClick={() => setEditingDevice(device)} className="h-9 w-9">
                                <Settings className="h-4 w-4" />
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent>Edit stream</TooltipContent>
                          </Tooltip>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Button
                                type="button"
                                variant="outline"
                                size="icon"
                                onClick={() => handleCloneDevice(device)}
                                disabled={actionLoading !== null || loading}
                                className="h-9 w-9"
                              >
                                  {actionLoading === `copy-${device.username}` ? <Loader2 className="h-4 w-4 animate-spin" /> : <Copy className="h-4 w-4" />}
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent>Copy stream</TooltipContent>
                          </Tooltip>
                          <Tooltip>
                            <TooltipTrigger asChild>
                                <Button type="button" variant="destructive" size="icon" onClick={() => setDeleteTarget(device.username)} disabled={actionLoading !== null || loading} className="h-9 w-9">
                                  {actionLoading === `delete-${device.username}` ? <Loader2 className="h-4 w-4 animate-spin" /> : <Trash2 className="h-4 w-4" />}
                                </Button>
                              </TooltipTrigger>
                              <TooltipContent>Delete stream</TooltipContent>
                            </Tooltip>
                          </div>
                        </div>
                      </div>

                      <div className="min-w-0 flex-1 rounded-md border border-border/70 bg-muted/20 px-3 py-2">
                        <div className="space-y-1.5">
                          <Label className="block text-xs text-muted-foreground">Manifest</Label>
                          <div className="flex items-start gap-2">
                            <code className="block min-w-0 flex-1 break-all rounded bg-muted px-2.5 py-1.5 text-[11px] leading-5">{getManifestUrl(device.token)}</code>
                            <div className="flex items-center gap-2">
                              <Tooltip>
                                <TooltipTrigger asChild>
                                  <Button type="button" variant="ghost" size="icon" onClick={() => copyManifestUrl(device.token)} className="h-8 w-8 shrink-0 bg-muted hover:bg-muted">
                                    {copiedToken === device.token ? <Check className="h-3.5 w-3.5" /> : <Clipboard className="h-3.5 w-3.5" />}
                                  </Button>
                                </TooltipTrigger>
                                <TooltipContent>{copiedToken === device.token ? 'Copied' : 'Copy manifest URL'}</TooltipContent>
                              </Tooltip>
                              <Tooltip>
                                <TooltipTrigger asChild>
                                  <Button type="button" variant="outline" size="icon" onClick={() => handleRegenerateToken(device.username)} disabled={actionLoading !== null || loading} className="h-8 w-8 shrink-0">
                                    {actionLoading === `regenerate-${device.username}` ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <RefreshCw className="h-3.5 w-3.5" />}
                                  </Button>
                                </TooltipTrigger>
                                <TooltipContent>Regenerate token</TooltipContent>
                              </Tooltip>
                            </div>
                          </div>
                        </div>
                      </div>

                      <div className="relative rounded-md border border-border/70 bg-muted/10 px-3 py-3 pb-6">
                        {expandedDevices[device.username] ? (
                          <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-6">
                            <SummaryRow label="General" icon={Settings} values={generalDetailValues(device)} />
                            <SummaryRow label="Providers" icon={Globe} values={device.provider_selections || []} />
                            <SummaryRow label="Indexers" icon={Server} values={device.indexer_selections || Object.keys(device.indexer_overrides || {})} />
                            <SummaryRow label="Movie" icon={Search} values={device.movie_search_queries || []} />
                            <SummaryRow label="TV" icon={Search} values={device.series_search_queries || []} />
                            <SummaryRow label="Filter/Sorting" icon={ArrowUpDown} values={filterSortingSummaryValues(device)} />
                          </div>
                        ) : (
                          <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-6">
                            <div className="space-y-1">
                              <div className="flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
                                <Settings className="h-3.5 w-3.5" />
                                <span>General</span>
                              </div>
                              <div className="flex flex-wrap items-center gap-2">
                                {generalCompactValues(device).map((value) => (
                                  <div key={value} className="inline-flex items-center justify-center rounded-full border border-border px-2 py-1 text-xs text-muted-foreground">
                                    {value}
                                  </div>
                                ))}
                              </div>
                            </div>
                            <div className="space-y-1">
                              <div className="flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
                                <Globe className="h-3.5 w-3.5" />
                                <span>Providers</span>
                              </div>
                              <div className="inline-flex items-center justify-center rounded-full border border-border px-2 py-1 text-xs text-muted-foreground">{(device.provider_selections || []).length}</div>
                            </div>
                            <div className="space-y-1">
                              <div className="flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
                                <Server className="h-3.5 w-3.5" />
                                <span>Indexers</span>
                              </div>
                              <div className="inline-flex items-center justify-center rounded-full border border-border px-2 py-1 text-xs text-muted-foreground">{(device.indexer_selections || Object.keys(device.indexer_overrides || {})).length}</div>
                            </div>
                            <div className="space-y-1">
                              <div className="flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
                                <Search className="h-3.5 w-3.5" />
                                <span>Movie</span>
                              </div>
                              <div className="inline-flex items-center justify-center rounded-full border border-border px-2 py-1 text-xs text-muted-foreground">{(device.movie_search_queries || []).length}</div>
                            </div>
                            <div className="space-y-1">
                              <div className="flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
                                <Search className="h-3.5 w-3.5" />
                                <span>TV</span>
                              </div>
                              <div className="inline-flex items-center justify-center rounded-full border border-border px-2 py-1 text-xs text-muted-foreground">{(device.series_search_queries || []).length}</div>
                            </div>
                            <div className="space-y-1">
                              <div className="flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
                                <ArrowUpDown className="h-3.5 w-3.5" />
                                <span>Filter/Sorting</span>
                              </div>
                              <div className="flex flex-wrap items-center gap-2">
                                {filterSortingSummaryValues(device).map((value) => (
                                  <div key={value} className="inline-flex items-center justify-center rounded-full border border-border px-2 py-1 text-xs text-muted-foreground">
                                    {value}
                                  </div>
                                ))}
                              </div>
                            </div>
                          </div>
                        )}

                        <div className="absolute inset-x-0 -bottom-4 flex justify-center">
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Button
                                type="button"
                                variant="outline"
                                onClick={() => toggleExpandedDevice(device.username)}
                                className="h-7 w-9 rounded-md border-dashed border-border/80 bg-muted text-muted-foreground shadow-sm hover:bg-muted/90"
                              >
                                {expandedDevices[device.username] ? <ChevronUp className="h-4 w-4" /> : <ChevronDown className="h-4 w-4" />}
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent>{expandedDevices[device.username] ? 'Hide details' : 'Show details'}</TooltipContent>
                          </Tooltip>
                        </div>
                      </div>
                    </div>
                  </CardContent>
                </Card>
              ))}
            </div>
          )}

          <StreamDialog
            open={showAddDialog}
            onOpenChange={(nextOpen) => {
              setShowAddDialog(nextOpen)
              if (!nextOpen) setAddDialogDraft(null)
            }}
            initialDevice={addDialogDraft}
            mode="add"
            providerNames={providerNames}
            indexerNames={indexerNames}
            movieQueryNames={movieQueryNames}
            seriesQueryNames={seriesQueryNames}
            onSave={handleCreateDevice}
            saving={dialogSaving}
            nextIndex={devices.length}
          />

          <StreamDialog
            open={Boolean(editingDevice)}
            onOpenChange={(nextOpen) => {
              if (!nextOpen) setEditingDevice(null)
            }}
            initialDevice={editingDevice}
            mode="edit"
            providerNames={providerNames}
            indexerNames={indexerNames}
            movieQueryNames={movieQueryNames}
            seriesQueryNames={seriesQueryNames}
            onSave={handleSaveDevice}
            saving={dialogSaving}
          />
        </CardContent>
      </Card>
      {visibleFooterStatus?.message && (
        <div
          className={`fixed bottom-4 left-4 right-4 z-40 rounded-lg border px-4 py-3 text-sm shadow-lg transition-all duration-200 ease-out md:left-[calc(var(--sidebar-width)+1rem)] ${
            footerStatusVisible ? "translate-y-0 opacity-100" : "translate-y-2 opacity-0"
          } ${
            visibleFooterStatus.type === 'error'
              ? 'border-destructive/30 bg-background text-destructive'
              : visibleFooterStatus.type === 'success'
                ? 'border-emerald-500/30 bg-background text-emerald-700 dark:text-emerald-400'
                : 'border-border bg-background text-foreground'
          }`}
        >
          {visibleFooterStatus.message}
        </div>
      )}
      <ConfirmDialog
        open={Boolean(deleteTarget)}
        onOpenChange={(nextOpen) => {
          if (!nextOpen) setDeleteTarget('')
        }}
        title="Delete stream?"
        description={deleteTarget ? `Are you sure you want to delete stream "${deleteTarget}"?` : ''}
        confirmLabel="Delete"
        onConfirm={() => {
          const username = deleteTarget
          setDeleteTarget('')
          if (username) {
            void handleDeleteDevice(username)
          }
        }}
      />
    </TooltipProvider>
  )
}

export default React.memo(StreamManagement)
