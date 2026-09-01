import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { CONTENT_KINDS } from "@/lib/profiles"
import {
  MAX_ADDON_NAME_LENGTH,
  activeProviderNames,
  applyFilterSortingMode,
  buildIndexerOverrides,
  buildStreamDraft,
  buildStreamStateFromDraft,
  defaultAddonName,
  filterSortingLabel,
  filterSortingSummaryValues,
  formattingSummaryValues,
  generalCompactValues,
  generalDetailValues,
  getInitialStreamDraft,
  indexerModeLabel,
  mapStreamsByUsername,
  metadataSummaryValues,
  nextStreamName,
  normalizeStreamDraft,
  resultsModeLabel,
  searchRequestsLabel,
  VARIANT_ATTEMPTS_UNLIMITED,
  variantAttemptsLabel,
  preloadAttemptsLabel,
  streamsFromMap,
  tabHasError,
  uniquePreserveOrder,
} from '@/lib/streams'
import { isAvailNZBEnabled } from "@/lib/availnzb"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, focusDialogCloseButton } from "@/components/ui/dialog"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { SortableList, SortableRow } from "@/components/SortableList"
import { apiFetch } from "@/api"
import { copyToClipboard } from "@/lib/utils"
import { ArrowUpDown, Check, ChevronDown, ChevronUp, Clapperboard, Clipboard, Copy, Globe, Loader2, Plus, RefreshCw, Search, Server, Settings, Trash2, Type } from "lucide-react"

const CACHE_CLEARED_SUFFIX = ' Search cache cleared.'

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

function SelectionSection({ title, values, selected, onToggle, onMove, error, helperText = '', membershipLocked = false, renderRowExtra = null, dimmedValues = [] }) {
  const selectedValues = useMemo(
    () => uniquePreserveOrder(selected).filter((value) => values.includes(value)),
    [selected, values]
  )
  const availableValues = useMemo(
    () => values.filter((value) => !selectedValues.includes(value)),
    [values, selectedValues]
  )

  return (
    <div className={`space-y-3 rounded-md border p-3 ${error ? 'border-destructive/60 bg-destructive/5' : 'border-border/60'}`}>
      <div className="flex items-center justify-between gap-3">
        <Label className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{title}</Label>
        {!membershipLocked && (
          <DropdownMenu>
            <Tooltip>
              <TooltipTrigger asChild>
                <DropdownMenuTrigger asChild>
                  <Button
                    type="button"
                    variant="destructive"
                    size="icon"
                    className="h-8 w-8"
                    disabled={availableValues.length === 0}
                  >
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
        )}
      </div>
      {helperText ? <p className="text-sm text-muted-foreground">{helperText}</p> : null}

      <div className="space-y-2">
        {selectedValues.length === 0 ? (
          <div className={`rounded-md border border-dashed px-3 py-3 text-sm ${error ? 'border-destructive/60 text-destructive' : 'border-border/70 text-muted-foreground'}`}>
            No entries added yet.
          </div>
        ) : (
          <SortableList ids={selectedValues} onMove={(from, to) => onMove?.(from, to)} disabled={!onMove}>
            {selectedValues.map((value) => (
              <SortableRow key={value} id={value} disabled={!onMove}>
                <div className={`min-w-0 flex-1 text-sm font-medium ${dimmedValues.includes(value) ? 'text-muted-foreground line-through' : ''}`}>{value}</div>
                {renderRowExtra?.(value)}
                {!membershipLocked && (
                  <Button type="button" variant="ghost" size="sm" className="h-8 px-2 text-muted-foreground" onClick={() => onToggle(value, false)}>
                    Remove
                  </Button>
                )}
              </SortableRow>
            ))}
          </SortableList>
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
  { id: 'search', label: 'Search' },
  { id: 'advanced', label: 'Advanced' },
]

// tabFieldErrorKeys maps a dialog tab to the field errors it hosts, so the
// tab strip can flag where a failed save needs attention.
function StreamDialog({
  open,
  onOpenChange,
  initialStream,
  mode = 'edit',
  existingNames = [],
  providerNames,
  providerConnectionTotals,
  enabledProviderNames,
  indexerNames,
  enabledIndexerNames,
  movieQueryNames,
  seriesQueryNames,
  filterProfiles = [],
  metadataProfiles = [],
  formatProfiles = [],
  globalConfig,
  onSave,
  saving,
}) {
  const isEditing = mode === 'edit'
  const availNZBEnabled = isAvailNZBEnabled(globalConfig?.availnzb_mode)
  const [draft, setDraft] = useState(() => getInitialStreamDraft(initialStream, isEditing, enabledProviderNames, enabledIndexerNames))
  const [saveError, setSaveError] = useState('')
  const [fieldErrors, setFieldErrors] = useState({})
  const [activeTab, setActiveTab] = useState('general')
  const [showDiscardConfirm, setShowDiscardConfirm] = useState(false)
  const [wasOpen, setWasOpen] = useState(open)
  const dialogIdentity = `${mode}:${initialStream?.username || ''}`
  const [lastDialogIdentity, setLastDialogIdentity] = useState(dialogIdentity)

  useEffect(() => {
    if (open && (!wasOpen || dialogIdentity !== lastDialogIdentity)) {
      setDraft(getInitialStreamDraft(initialStream, isEditing, enabledProviderNames, enabledIndexerNames))
      setSaveError('')
      setFieldErrors({})
      setActiveTab('general')
      setLastDialogIdentity(dialogIdentity)
    }
    setWasOpen(open)
  }, [open, initialStream, isEditing, wasOpen, dialogIdentity, lastDialogIdentity, enabledProviderNames, enabledIndexerNames])

  const normalizedInitial = JSON.stringify(getInitialStreamDraft(initialStream, isEditing, enabledProviderNames, enabledIndexerNames))
  const normalizedCurrent = JSON.stringify(normalizeStreamDraft(draft))
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

  // A blank field means "no cap", so the entry is dropped rather than stored as
  // zero — the backend treats a missing key and a zero the same way, and keeping
  // the map sparse means a provider's own connection count stays the default.
  const setConnectionLimit = (providerName, rawValue) => {
    setDraft((current) => {
      const limits = { ...(current.provider_connection_limits || {}) }
      const parsed = Number.parseInt(rawValue, 10)
      if (!Number.isFinite(parsed) || parsed <= 0) {
        delete limits[providerName]
      } else {
        limits[providerName] = parsed
      }
      return { ...current, provider_connection_limits: limits }
    })
  }

  // Disabling keeps the provider in the list, which is what lets a stream-level
  // opinion survive automatic sync — sync owns membership, this owns which
  // members are live.
  const setProviderEnabled = (providerName, enabled) => {
    setDraft((current) => {
      const disabled = (current.disabled_providers || []).filter((name) => name !== providerName)
      if (!enabled) disabled.push(providerName)
      return { ...current, disabled_providers: disabled }
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
    const next = normalizeStreamDraft(draft)
    const nextFieldErrors = {}
    if (!next.username) {
      nextFieldErrors.username = 'Stream name is required'
    } else if (existingNames.some((name) => name.toLowerCase() === next.username.toLowerCase() && name !== initialStream?.username)) {
      nextFieldErrors.username = 'A stream with that name already exists.'
    }
    if (next.providers.length === 0) {
      nextFieldErrors.providers = 'Add at least one provider.'
    } else if (next.disabled_providers.length === next.providers.length) {
      nextFieldErrors.providers = 'Keep at least one provider enabled.'
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
      <DialogContent className="flex h-[85vh] max-h-[85vh] max-w-3xl flex-col overflow-visible" onOpenAutoFocus={focusDialogCloseButton}>
        <DialogHeader>
          <DialogTitle>{isEditing ? 'Change Stream' : 'Add Stream'}</DialogTitle>
          <DialogDescription>Create a stream or manage its provider, indexer, and search request assignments.</DialogDescription>
        </DialogHeader>

        <div className="flex-1 space-y-4 overflow-y-auto px-1">
          <div className="space-y-2">
            <Label>Name</Label>
            <Input
              className={fieldErrors.username ? "border-destructive focus-visible:ring-destructive" : ""}
              value={draft.username || ''}
              onChange={(event) => setDraft((current) => ({ ...current, username: event.target.value }))}
              placeholder="Stream01"
            />
            {isEditing && (
              <p className="text-xs text-muted-foreground">
                Renaming keeps the token, so an installed addon URL keeps working. The stream's history moves with it.
              </p>
            )}
          </div>

          <div className="flex items-center gap-1 overflow-x-auto border-b border-border">
            {STREAM_DIALOG_TABS.map((tab) => (
              <button
                key={tab.id}
                type="button"
                onClick={() => setActiveTab(tab.id)}
                className={`relative whitespace-nowrap px-3 py-2 text-sm font-medium transition-colors ${
                  activeTab === tab.id
                    ? tabHasError(tab.id, fieldErrors)
                      ? 'text-destructive after:absolute after:bottom-0 after:inset-x-0 after:h-0.5 after:bg-destructive'
                      : 'text-foreground after:absolute after:bottom-0 after:inset-x-0 after:h-0.5 after:bg-primary'
                    : tabHasError(tab.id, fieldErrors)
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
                <div className="flex flex-col gap-3 min-[420px]:flex-row min-[420px]:items-center min-[420px]:justify-between">
                  <div className="text-sm font-medium">Addon name</div>
                  <Input
                    className="h-9 w-full min-[420px]:w-64"
                    value={draft.addon_name || ''}
                    maxLength={MAX_ADDON_NAME_LENGTH}
                    onChange={(event) => setDraft((current) => ({ ...current, addon_name: event.target.value }))}
                    placeholder={defaultAddonName(draft.username)}
                  />
                </div>
                <p className="mt-3 text-sm text-muted-foreground">
                  What this stream calls itself in the client's addon list and on every result it returns. Leave blank to use “{defaultAddonName(draft.username)}”.
                </p>
                <p className="mt-1 text-xs text-muted-foreground">
                  Clients cache the manifest, so an already-installed addon keeps its old name until it is reinstalled.
                </p>
              </div>

              <div className="rounded-md border border-border/60 p-3">
                <div className="flex items-center justify-between gap-4">
                  <div className="text-sm font-medium">Filter/Sorting</div>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button type="button" variant="outline" className="h-9 w-48 justify-between">
                        <span className="truncate">{filterSortingLabel(draft)}</span>
                        <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end" className="w-48 max-h-60 overflow-y-auto">
                      <DropdownMenuItem onClick={() => setDraft((current) => applyFilterSortingMode(current, 'none', ''))}>
                        None
                      </DropdownMenuItem>
                      <DropdownMenuItem onClick={() => setDraft((current) => applyFilterSortingMode(current, 'aiostreams', ''))}>
                        AIOStreams
                      </DropdownMenuItem>
                      {(filterProfiles || []).map((fp) => (
                        <DropdownMenuItem
                          key={fp.name}
                          onClick={() => setDraft((current) => applyFilterSortingMode(current, 'none', fp.name))}
                        >
                          {fp.name}
                        </DropdownMenuItem>
                      ))}
                    </DropdownMenuContent>
                  </DropdownMenu>
                </div>
                <p className="mt-3 text-sm text-muted-foreground">
                  Apply a predefined stream mode (like AIOStreams) or select a filter profile to decide which releases this stream offers.
                </p>

                {/* AIOStreams returns every release for AIOStreams itself to filter, so
                    per-content-type profiles have nothing to act on and are hidden. */}
                {!aiostreamsMode && (filterProfiles || []).length > 0 && (
                  <div className="mt-4 space-y-2 border-t border-border/60 pt-3">
                    <div className="text-sm font-medium">By content type</div>
                    <p className="text-sm text-muted-foreground">
                      Override the profile above for a specific kind of content. Anything left on Default uses the profile selected above. Anime means animation that is not originally in English, which needs TMDB configured to detect outside of Kitsu catalogues.
                    </p>
                    <div className="grid gap-2 pt-1 sm:grid-cols-2">
                      {CONTENT_KINDS.map((kind) => {
                        const current = draft.filter_profile_by_type?.[kind.key] || ''
                        const setKind = (name) => setDraft((prev) => {
                          const next = { ...(prev.filter_profile_by_type || {}) }
                          if (name) next[kind.key] = name
                          else delete next[kind.key]
                          return { ...prev, filter_profile_by_type: next }
                        })
                        return (
                          <div key={kind.key} className="flex items-center justify-between gap-3">
                            <span className="text-sm text-muted-foreground">{kind.label}</span>
                            <DropdownMenu>
                              <DropdownMenuTrigger asChild>
                                <Button type="button" variant="outline" className="h-8 w-44 justify-between">
                                  <span className="truncate text-xs">{current || 'Default'}</span>
                                  <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                                </Button>
                              </DropdownMenuTrigger>
                              <DropdownMenuContent align="end" className="w-44 max-h-60 overflow-y-auto">
                                <DropdownMenuItem onClick={() => setKind('')}>Default</DropdownMenuItem>
                                {(filterProfiles || []).map((fp) => (
                                  <DropdownMenuItem key={fp.name} onClick={() => setKind(fp.name)}>
                                    {fp.name}
                                  </DropdownMenuItem>
                                ))}
                              </DropdownMenuContent>
                            </DropdownMenu>
                          </div>
                        )
                      })}
                    </div>
                  </div>
                )}
              </div>

              <div className="rounded-md border border-border/60 p-3">
                <div className="flex items-center justify-between gap-4">
                  <div className="text-sm font-medium">Metadata profile</div>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button type="button" variant="outline" className="h-9 w-48 justify-between">
                        <span className="truncate">{draft.metadata_profile_name || 'None'}</span>
                        <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end" className="w-48 max-h-60 overflow-y-auto">
                      <DropdownMenuItem onClick={() => setDraft((current) => ({ ...current, metadata_profile_name: '' }))}>
                        None (metadata off)
                      </DropdownMenuItem>
                      {(metadataProfiles || []).map((mp) => (
                        <DropdownMenuItem
                          key={mp.name}
                          onClick={() => setDraft((current) => ({ ...current, metadata_profile_name: mp.name }))}
                        >
                          {mp.name}
                        </DropdownMenuItem>
                      ))}
                    </DropdownMenuContent>
                  </DropdownMenu>
                </div>
                <p className="mt-3 text-sm text-muted-foreground">
                  The catalogs, display language and rating limit this stream serves, from the Metadata page.
                  None keeps the classic stream-only addon — no catalogs, no title pages, no rating cap.
                </p>
              </div>

              <div className="rounded-md border border-border/60 p-3">
                <div className="flex items-center justify-between gap-4">
                  <div className="text-sm font-medium">Format profile</div>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button type="button" variant="outline" className="h-9 w-48 justify-between">
                        <span className="truncate">{draft.format_profile_name || 'Default'}</span>
                        <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end" className="w-48 max-h-60 overflow-y-auto">
                      <DropdownMenuItem onClick={() => setDraft((current) => ({ ...current, format_profile_name: '' }))}>
                        Default (built-in)
                      </DropdownMenuItem>
                      {(formatProfiles || []).map((fp) => (
                        <DropdownMenuItem
                          key={fp.name}
                          onClick={() => setDraft((current) => ({ ...current, format_profile_name: fp.name }))}
                        >
                          {fp.name}
                        </DropdownMenuItem>
                      ))}
                    </DropdownMenuContent>
                  </DropdownMenu>
                </div>
                <p className="mt-3 text-sm text-muted-foreground">
                  How this stream&apos;s results render in Stremio, from the Formatting page. Default keeps the
                  built-in format; AIOStreams responses keep their fixed format either way.
                </p>
              </div>

              <div className="rounded-md border border-border/60 p-3">
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
                  Choose whether StreamNZB returns one combined stream or shows every matching result as a separate stream entry. AIOStreams always uses Display all.
                </p>
              </div>
            </div>
          )}

          {activeTab === 'providers' && (
            <div className="space-y-4">
              <div className="rounded-md border border-border/60 p-3">
                <div className="flex items-center justify-between gap-4">
                  <div className="text-sm font-medium">Automatic sync</div>
                  <Switch
                    checked={draft.auto_add_providers === true}
                    onCheckedChange={(checked) => setDraft((current) => (
                      checked === true
                        ? { ...current, auto_add_providers: true, providers: uniquePreserveOrder(enabledProviderNames || []) }
                        : { ...current, auto_add_providers: false }
                    ))}
                  />
                </div>
                <p className="mt-3 text-sm text-muted-foreground">
                  Keep this stream in sync with globally enabled providers. Disabled providers are removed automatically.
                </p>
                <p className="mt-1 text-xs text-muted-foreground">
                  Disable automatic sync to manage providers manually.
                </p>
              </div>
              <SelectionSection
                title="Providers"
                values={providerNames}
                selected={draft.providers || []}
                onToggle={(value, checked) => toggleListValue('providers', value, checked)}
                onMove={(fromIndex, toIndex) => moveListValue('providers', fromIndex, toIndex)}
                error={fieldErrors.providers}
                helperText="Priority is based on position. Drag to reorder. The toggle turns a provider off for this stream without removing it, so the choice survives automatic sync. Max connections caps what this stream may hold at once during playback — leave blank for no cap."
                membershipLocked={draft.auto_add_providers === true}
                dimmedValues={draft.disabled_providers || []}
                renderRowExtra={(providerName) => {
                  const total = providerConnectionTotals?.[providerName]
                  const value = draft.provider_connection_limits?.[providerName]
                  const enabled = !(draft.disabled_providers || []).includes(providerName)
                  return (
                    <div className="flex shrink-0 items-center gap-2">
                      <Input
                        type="number"
                        min={1}
                        max={total || undefined}
                        placeholder={total ? `max ${total}` : 'max'}
                        className="h-8 w-24"
                        value={value ?? ''}
                        disabled={!enabled}
                        onChange={(event) => setConnectionLimit(providerName, event.target.value)}
                      />
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <div className="inline-flex h-8 items-center">
                            <Switch
                              checked={enabled}
                              onCheckedChange={(checked) => setProviderEnabled(providerName, checked === true)}
                              className="h-5 w-10 data-[state=checked]:bg-green-500 data-[state=unchecked]:bg-muted-foreground/30"
                              thumbClassName="h-4 w-4 data-[state=checked]:translate-x-5 data-[state=unchecked]:translate-x-0"
                            />
                          </div>
                        </TooltipTrigger>
                        <TooltipContent>{enabled ? 'Disable for this stream' : 'Enable for this stream'}</TooltipContent>
                      </Tooltip>
                    </div>
                  )
                }}
              />
            </div>
          )}

          {activeTab === 'indexers' && (
            <div className="space-y-4">
              <div className="rounded-md border border-border/60 p-3">
                <div className="flex items-center justify-between gap-4">
                  <div className="text-sm font-medium">Indexer mode</div>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button type="button" variant="outline" className="h-9 w-40 justify-between">
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
              <div className="rounded-md border border-border/60 p-3">
                <div className="flex items-center justify-between gap-4">
                  <div className="text-sm font-medium">Automatic sync</div>
                  <Switch
                    checked={draft.auto_add_indexers === true}
                    onCheckedChange={(checked) => setDraft((current) => (
                      checked === true
                        ? { ...current, auto_add_indexers: true, indexers: uniquePreserveOrder(enabledIndexerNames || []) }
                        : { ...current, auto_add_indexers: false }
                    ))}
                  />
                </div>
                <p className="mt-3 text-sm text-muted-foreground">
                  Keep this stream in sync with globally enabled indexers. Disabled indexers are removed automatically.
                </p>
                <p className="mt-1 text-xs text-muted-foreground">
                  Disable automatic sync to manage indexers manually.
                </p>
              </div>
              <div className="rounded-md border border-border/60 p-3">
                <div className="flex items-center justify-between gap-4">
                  <div className="text-sm font-medium">Skip unaired episodes</div>
                  <Switch
                    checked={draft.unaired_search_gate !== false}
                    onCheckedChange={(checked) => setDraft((current) => ({ ...current, unaired_search_gate: checked === true }))}
                  />
                </div>
                <p className="mt-3 text-sm text-muted-foreground">
                  Answer with no results instead of asking these indexers for an episode that has not aired
                  yet. Uses the exact air time where a source knows one, and the whole of the air date where
                  it only knows a date; a lookup that fails always searches.
                </p>
                <p className="mt-1 text-xs text-muted-foreground">
                  Turn it off if this stream&apos;s indexers carry releases before the listed air date.
                </p>
              </div>
              <SelectionSection
                title="Indexers"
                values={indexerNames}
                selected={draft.indexers || []}
                onToggle={(value, checked) => toggleListValue('indexers', value, checked)}
                onMove={(fromIndex, toIndex) => moveListValue('indexers', fromIndex, toIndex)}
                error={fieldErrors.indexers}
                helperText="Priority is based on position. Drag to reorder."
                membershipLocked={draft.auto_add_indexers === true}
              />
            </div>
          )}

          {activeTab === 'search' && (
            <div className="space-y-4">
              <div className="rounded-md border border-border/60 p-3">
                <div className="flex items-center justify-between gap-4">
                  <div className="text-sm font-medium">Search requests</div>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button type="button" variant="outline" className="h-9 w-40 justify-between">
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
              <SelectionSection
                title="Movie Search Requests"
                values={movieQueryNames}
                selected={draft.movie_search_queries || []}
                onToggle={(value, checked) => toggleListValue('movie_search_queries', value, checked)}
                onMove={(fromIndex, toIndex) => moveListValue('movie_search_queries', fromIndex, toIndex)}
                error={fieldErrors.movie_search_queries}
              />
              <SelectionSection
                title="TV Search Requests"
                values={seriesQueryNames}
                selected={draft.series_search_queries || []}
                onToggle={(value, checked) => toggleListValue('series_search_queries', value, checked)}
                onMove={(fromIndex, toIndex) => moveListValue('series_search_queries', fromIndex, toIndex)}
                error={fieldErrors.series_search_queries}
              />
            </div>
          )}

          {activeTab === 'advanced' && (
            <div className="space-y-4">
              <div className="rounded-md border border-border/60 p-3">
                <div className="flex items-center justify-between gap-4">
                  <div className="text-sm font-medium">Failover</div>
                  <Switch
                    checked={draft.enable_failover}
                    onCheckedChange={(checked) => setDraft((current) => ({ ...current, enable_failover: checked === true }))}
                  />
                </div>
                <p className="mt-3 text-sm text-muted-foreground">
                  If enabled, StreamNZB automatically tries the next release in order when the current NZB fails during playback.
                </p>
              </div>
              <div className="rounded-md border border-border/60 p-3">
                <div className="flex items-center justify-between gap-4">
                  <div className="text-sm font-medium">Same-release attempts</div>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button type="button" variant="outline" className="h-9 w-40 justify-between">
                        <span>{variantAttemptsLabel(draft.variant_attempts)}</span>
                        <ChevronDown className="h-4 w-4 text-muted-foreground" />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end" className="w-40">
                      <DropdownMenuItem onClick={() => setDraft((current) => ({ ...current, variant_attempts: 1 }))}>
                        Merge only
                      </DropdownMenuItem>
                      <DropdownMenuItem onClick={() => setDraft((current) => ({ ...current, variant_attempts: 2 }))}>
                        2 copies
                      </DropdownMenuItem>
                      <DropdownMenuItem onClick={() => setDraft((current) => ({ ...current, variant_attempts: 3 }))}>
                        3 copies
                      </DropdownMenuItem>
                      <DropdownMenuItem onClick={() => setDraft((current) => ({ ...current, variant_attempts: VARIANT_ATTEMPTS_UNLIMITED }))}>
                        All copies
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </div>
                <p className="mt-3 text-sm text-muted-foreground">
                  Several indexers listing the same release always become one result that keeps the other copies,
                  because two indexers&apos; NZBs for one release are not always the same NZB. This is how many of
                  those copies playback tries before moving on to a different release. Merge only — the default —
                  keeps the de-cluttered list without ever spending a second startup on the same release.
                </p>
              </div>
              <div className="rounded-md border border-border/60 p-3">
                <div className="flex items-center justify-between gap-4">
                  <div className="text-sm font-medium">Preloading</div>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button type="button" variant="outline" className="h-9 w-40 justify-between">
                        <span>{preloadAttemptsLabel(draft.preload_attempts)}</span>
                        <ChevronDown className="h-4 w-4 text-muted-foreground" />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end" className="w-40">
                      <DropdownMenuItem onClick={() => setDraft((current) => ({ ...current, preload_attempts: null }))}>
                        {preloadAttemptsLabel(null)}
                      </DropdownMenuItem>
                      <DropdownMenuItem onClick={() => setDraft((current) => ({ ...current, preload_attempts: 0 }))}>
                        Off
                      </DropdownMenuItem>
                      {[1, 2, 3, 4, 5].map((count) => (
                        <DropdownMenuItem key={count} onClick={() => setDraft((current) => ({ ...current, preload_attempts: count }))}>
                          {count === 1 ? '1 result' : `${count} results`}
                        </DropdownMenuItem>
                      ))}
                    </DropdownMenuContent>
                  </DropdownMenu>
                </div>
                <p className="mt-3 text-sm text-muted-foreground">
                  After a search, the top results are prepared in the background — downloaded, mapped and
                  verified — so the one you pick starts almost instantly and broken releases are weeded out
                  before you ever see a spinner. This is how many results are preloaded per search; each
                  preloaded result spends one indexer API download. Off disables preloading for this stream.
                </p>
              </div>
              <div className="rounded-md border border-border/60 p-3">
                <div className="flex items-center justify-between gap-4">
                  <div className="text-sm font-medium">Filter AvailNZB unavailable</div>
                  <Switch
                    checked={availNZBEnabled && draft.filter_availnzb === true}
                    onCheckedChange={(checked) => setDraft((current) => ({ ...current, filter_availnzb: checked === true }))}
                    disabled={!availNZBEnabled}
                  />
                </div>
                <p className="mt-3 text-sm text-muted-foreground">
                  {availNZBEnabled
                    ? 'If enabled, releases reported as bad by AvailNZB are removed from returned streams.'
                    : 'Disabled because AvailNZB is globally disabled.'}
                </p>
              </div>
            </div>
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
          <div className="flex flex-row items-center justify-end gap-2">
            <Button type="button" variant="outline" onClick={requestClose}>Cancel</Button>
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

function StreamManagement({ globalConfig, movieSearchQueries = [], seriesSearchQueries = [], initialStreamsByName = {}, onStreamsChange, onStatus }) {
  const initialStreams = useMemo(() => streamsFromMap(initialStreamsByName), [initialStreamsByName])
  const initialStreamsSignature = useMemo(() => JSON.stringify(initialStreams), [initialStreams])
  const initialFetchStartedRef = useRef(false)
  const lastAppliedInitialSignatureRef = useRef(initialStreamsSignature)
  const [streams, setStreams] = useState(() => initialStreams)
  const [loading, setLoading] = useState(false)
  const [actionLoading, setActionLoading] = useState(null)
  const [dialogSaving, setDialogSaving] = useState(false)
  const [showAddDialog, setShowAddDialog] = useState(false)
  const [addDialogDraft, setAddDialogDraft] = useState(null)
  const [editingStream, setEditingStream] = useState(null)
  const [copiedToken, setCopiedToken] = useState('')
  const [visibleFooterStatus, setVisibleFooterStatus] = useState(null)
  const [footerStatusVisible, setFooterStatusVisible] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState('')
  const [regenerateTarget, setRegenerateTarget] = useState('')
  const [expandedStreams, setExpandedStreams] = useState({})

  const indexerNames = useMemo(
    () => (globalConfig?.indexers || []).map((indexer) => indexer.name).filter(Boolean),
    [globalConfig]
  )
  const providerNames = useMemo(
    () => (globalConfig?.providers || []).map((provider) => provider.name).filter(Boolean),
    [globalConfig]
  )
  const enabledProviderNames = useMemo(
    () => (globalConfig?.providers || [])
      .filter((provider) => provider?.enabled !== false)
      .map((provider) => provider.name)
      .filter(Boolean),
    [globalConfig]
  )
  // Each provider's own pool size, so a per-stream cap can be bounded by it.
  const providerConnectionTotals = useMemo(
    () => (globalConfig?.providers || []).reduce((acc, provider) => {
      if (provider?.name) acc[provider.name] = Number(provider.connections) || 0
      return acc
    }, {}),
    [globalConfig]
  )
  const movieQueryNames = useMemo(() => movieSearchQueries.map((query) => query.name).filter(Boolean), [movieSearchQueries])
  const seriesQueryNames = useMemo(() => seriesSearchQueries.map((query) => query.name).filter(Boolean), [seriesSearchQueries])
  const enabledIndexerNames = useMemo(
    () => (globalConfig?.indexers || [])
      .filter((indexer) => indexer?.enabled !== false)
      .map((indexer) => indexer.name)
      .filter(Boolean),
    [globalConfig]
  )
  const enabledProviderSignature = useMemo(
    () => JSON.stringify(enabledProviderNames),
    [enabledProviderNames]
  )
  const enabledIndexerSignature = useMemo(
    () => JSON.stringify(enabledIndexerNames),
    [enabledIndexerNames]
  )

  useEffect(() => {
    if (lastAppliedInitialSignatureRef.current === initialStreamsSignature) return
    lastAppliedInitialSignatureRef.current = initialStreamsSignature
    setStreams(initialStreams)
    setLoading(false)
  }, [initialStreams, initialStreamsSignature])

  const showStatus = useCallback((status) => {
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

  const fetchStreams = useCallback(async (showLoader = true, options = {}) => {
    const { silent = false } = options
    if (showLoader) setLoading(true)
    try {
      const nextStreams = await apiFetch('/api/streams')
      setStreams(Array.isArray(nextStreams) ? nextStreams : [])
      onStreamsChange?.(mapStreamsByUsername(nextStreams))
      return nextStreams
    } catch (err) {
      if (!silent) {
        const status = { type: 'error', message: err.message || 'Failed to load streams' }
        showStatus(status)
        showFooterStatus(status)
      }
      throw err
    } finally {
      if (showLoader) setLoading(false)
    }
  }, [onStreamsChange, showFooterStatus, showStatus])

  useEffect(() => {
    fetchStreams(false, { silent: true }).catch(() => {})
  }, [enabledProviderSignature, enabledIndexerSignature, fetchStreams])

  useEffect(() => {
    if (initialFetchStartedRef.current) return
    initialFetchStartedRef.current = true
    fetchStreams(false).catch(() => {})
  }, [fetchStreams])

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

  const saveStreamAssignments = async (username, draft, existingStream) => {
    const payload = {
      [username]: {
        filter_sorting_mode: draft.filter_sorting_mode,
        indexer_mode: draft.indexer_mode,
        combine_results: draft.combine_results,
        enable_failover: draft.enable_failover,
        variant_attempts: draft.variant_attempts,
        preload_attempts: draft.preload_attempts ?? null,
        results_mode: draft.results_mode,
        auto_add_providers: draft.auto_add_providers,
        auto_add_indexers: draft.auto_add_indexers,
        unaired_search_gate: draft.unaired_search_gate,
        filter_availnzb: draft.filter_availnzb,
        provider_selections: draft.providers || [],
        provider_connection_limits: draft.provider_connection_limits || {},
        disabled_providers: draft.disabled_providers || [],
        indexer_selections: draft.indexers || [],
        indexer_overrides: buildIndexerOverrides(draft.indexers, existingStream?.indexer_overrides),
        movie_search_queries: draft.movie_search_queries || [],
        series_search_queries: draft.series_search_queries || [],
        filter_profile_name: draft.filter_profile_name || '',
        filter_profile_by_type: draft.filter_profile_by_type || {},
        metadata_profile_name: draft.metadata_profile_name || '',
        format_profile_name: draft.format_profile_name || '',
        result_name_template: draft.result_name_template || '',
        result_description_template: draft.result_description_template || '',
        addon_name: draft.addon_name || '',
      },
    }
    await apiFetch('/api/streams/configs', {
      method: 'PUT',
      body: JSON.stringify(payload),
    })
  }

  const refreshStreamsAfterMutation = async () => {
    try {
      await fetchStreams(false, { silent: true })
    } catch {
      // Preserve the successful mutation state when only the refresh fails.
    }
  }

  const handleCreateStream = async (draft) => {
    setDialogSaving(true)
    showStatus(null)
    let created = false
    let createdStream = null
    try {
      const payload = await apiFetch('/api/streams', {
        method: 'POST',
        body: JSON.stringify({ username: draft.username }),
      })
      created = true
      createdStream = payload?.user || null
      await saveStreamAssignments(draft.username, draft, draft)
      setStreams((prev) => {
        const next = prev.filter((stream) => stream.username !== draft.username)
        next.push(buildStreamStateFromDraft(draft.username, createdStream?.token || '', draft, draft.indexer_overrides))
        onStreamsChange?.(mapStreamsByUsername(next))
        return next
      })
      const status = { type: 'success', message: `Stream "${draft.username}" created successfully.${CACHE_CLEARED_SUFFIX}` }
      showStatus(status)
      showFooterStatus(status)
      setAddDialogDraft(null)
      setShowAddDialog(false)
    } catch (err) {
      if (created) {
        try {
          await apiFetch(`/api/streams/${encodeURIComponent(draft.username)}`, { method: 'DELETE' })
        } catch {
          // Preserve the original create error below.
        }
      }
      const status = { type: 'error', message: err.message || 'Failed to create stream' }
      showStatus(status)
      showFooterStatus(status)
    } finally {
      setDialogSaving(false)
    }
    if (created) {
      await refreshStreamsAfterMutation()
    }
  }

  const handleCloneStream = (stream) => {
    if (!stream?.username) return
    const nextName = nextStreamName(streams)
    // Reuse the same builder the editor uses, so a clone keeps every field a
    // stream has. The hand-written copy this replaces silently dropped the
    // result templates, and would have dropped connection caps and per-stream
    // provider toggles too.
    const draft = { ...buildStreamDraft(stream), username: nextName }
    setAddDialogDraft(draft)
    setShowAddDialog(true)
  }

  const handleSaveStream = async (draft) => {
    if (!editingStream) return
    setDialogSaving(true)
    showStatus(null)
    let saved = false
    const previousName = editingStream.username
    const nextName = (draft.username || '').trim()
    try {
      // The rename has to land first: the config save is keyed by stream name,
      // so sending it under the old name would write to a stream that is about
      // to move, and under the new one to a stream that does not exist yet.
      if (nextName && nextName !== previousName) {
        await apiFetch(`/api/streams/${encodeURIComponent(previousName)}/rename`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ username: nextName }),
        })
      }
      const savedName = nextName || previousName
      await saveStreamAssignments(savedName, draft, editingStream)
      saved = true
      setStreams((prev) => {
        const next = prev.map((stream) =>
          stream.username === previousName
            ? buildStreamStateFromDraft(savedName, stream.token, draft, editingStream.indexer_overrides)
            : stream
        )
        onStreamsChange?.(mapStreamsByUsername(next))
        return next
      })
      const status = { type: 'success', message: `Stream "${savedName}" saved successfully.${CACHE_CLEARED_SUFFIX}` }
      showStatus(status)
      showFooterStatus(status)
      setEditingStream(null)
    } catch (err) {
      const status = { type: 'error', message: err.message || 'Failed to save stream' }
      showStatus(status)
      showFooterStatus(status)
    } finally {
      setDialogSaving(false)
    }
    if (saved) {
      await refreshStreamsAfterMutation()
    }
  }

  const handleDeleteStream = async (username) => {
    setActionLoading(`delete-${username}`)
    showStatus(null)
    let deleted = false
    try {
      await apiFetch(`/api/streams/${encodeURIComponent(username)}`, { method: 'DELETE' })
      deleted = true
      setStreams((prev) => {
        const next = prev.filter((stream) => stream.username !== username)
        onStreamsChange?.(mapStreamsByUsername(next))
        return next
      })
      const status = { type: 'success', message: `Stream "${username}" deleted successfully.${CACHE_CLEARED_SUFFIX}` }
      showStatus(status)
      showFooterStatus(status)
    } catch (err) {
      const status = { type: 'error', message: err.message || 'Failed to delete stream' }
      showStatus(status)
      showFooterStatus(status)
    } finally {
      setActionLoading(null)
    }
    if (deleted) {
      await refreshStreamsAfterMutation()
    }
  }

  const handleRegenerateToken = async (username) => {
    setActionLoading(`regenerate-${username}`)
    showStatus(null)
    try {
      const payload = await apiFetch(`/api/streams/${encodeURIComponent(username)}/regenerate-token`, { method: 'POST' })
      setStreams((prev) => {
        const next = prev.map((stream) => stream.username === username ? { ...stream, token: payload.token } : stream)
        onStreamsChange?.(mapStreamsByUsername(next))
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

  const toggleExpandedStream = (username) => {
    setExpandedStreams((current) => ({
      ...current,
      [username]: !current[username],
    }))
  }

  return (
    <TooltipProvider delayDuration={100}>
      <Card>
        <CardHeader>
          <div className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-3">
            <div className="min-w-0 space-y-0.5">
              <CardTitle>Streams</CardTitle>
              <CardDescription className="break-words">Configure stream-specific manifests and their provider, indexer and search order.</CardDescription>
            </div>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  type="button"
                  variant="destructive"
                  size="icon"
                  className="h-9 w-9 shrink-0"
                  onClick={() => {
                    setAddDialogDraft({ username: nextStreamName(streams) })
                    setShowAddDialog(true)
                  }}
                  aria-label="Add stream"
                >
                  <Plus className="h-4 w-4 shrink-0" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Add Stream</TooltipContent>
            </Tooltip>
          </div>
        </CardHeader>
        <CardContent className="space-y-4">
          {loading && streams.length > 0 ? (
            <div className="flex items-center justify-center p-8"><Loader2 className="h-6 w-6 animate-spin" /></div>
          ) : streams.length === 0 ? (
            <div className="p-8 text-center text-muted-foreground">No streams found. Create your first stream to get started.</div>
          ) : (
            <div className="space-y-4">
              {streams.map((stream) => (
                <Card key={stream.username}>
                  <CardContent className="pt-6">
                    <div className="space-y-4">
                      <div className="space-y-3">
                        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                          <div className="flex items-center gap-2 self-end sm:order-2">
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <Button type="button" variant="outline" size="icon" onClick={() => setEditingStream(stream)} className="h-9 w-9" aria-label={`Edit ${stream.username} stream`}>
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
                                  onClick={() => handleCloneStream(stream)}
                                  disabled={actionLoading !== null || loading}
                                  className="h-9 w-9"
                                  aria-label={`Copy ${stream.username} stream`}
                                >
                                  {actionLoading === `copy-${stream.username}` ? <Loader2 className="h-4 w-4 animate-spin" /> : <Copy className="h-4 w-4" />}
                                </Button>
                              </TooltipTrigger>
                              <TooltipContent>Copy stream</TooltipContent>
                            </Tooltip>
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <Button type="button" variant="destructive" size="icon" onClick={() => setDeleteTarget(stream.username)} disabled={actionLoading !== null || loading} className="h-9 w-9" aria-label={`Delete ${stream.username} stream`}>
                                  {actionLoading === `delete-${stream.username}` ? <Loader2 className="h-4 w-4 animate-spin" /> : <Trash2 className="h-4 w-4" />}
                                </Button>
                              </TooltipTrigger>
                              <TooltipContent>Delete stream</TooltipContent>
                            </Tooltip>
                          </div>
                          <div className="min-w-0 font-semibold sm:order-1">{stream.username}</div>
                        </div>
                      </div>

                      <div className="min-w-0 flex-1 rounded-md border border-border/70 bg-muted/20 px-3 py-2">
                        <div className="space-y-1.5">
                          <Label className="block text-xs text-muted-foreground">Manifest</Label>
                          <div className="flex items-center gap-2">
                            <code className="block min-w-0 flex-1 break-all rounded bg-muted px-2.5 py-1.5 text-[11px] leading-5">{getManifestUrl(stream.token)}</code>
                            <div className="flex shrink-0 items-center gap-2 self-center">
                              <Tooltip>
                                <TooltipTrigger asChild>
                                  <Button type="button" variant="ghost" size="icon" onClick={() => copyManifestUrl(stream.token)} className="h-8 w-8 shrink-0 bg-muted hover:bg-muted" aria-label={`Copy manifest URL for ${stream.username}`}>
                                    {copiedToken === stream.token ? <Check className="h-3.5 w-3.5" /> : <Clipboard className="h-3.5 w-3.5" />}
                                  </Button>
                                </TooltipTrigger>
                                <TooltipContent>{copiedToken === stream.token ? 'Copied' : 'Copy manifest URL'}</TooltipContent>
                              </Tooltip>
                              <Tooltip>
                                <TooltipTrigger asChild>
                                  <Button type="button" variant="outline" size="icon" onClick={() => setRegenerateTarget(stream.username)} disabled={actionLoading !== null || loading} className="h-8 w-8 shrink-0" aria-label={`Regenerate token for ${stream.username}`}>
                                    {actionLoading === `regenerate-${stream.username}` ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <RefreshCw className="h-3.5 w-3.5" />}
                                  </Button>
                                </TooltipTrigger>
                                <TooltipContent>Regenerate token</TooltipContent>
                              </Tooltip>
                            </div>
                          </div>
                        </div>
                      </div>

                      <div className="relative rounded-md border border-border/70 bg-muted/10 px-3 py-3 pb-6">
                        {expandedStreams[stream.username] ? (
                          <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-4">
                            <SummaryRow label="General" icon={Settings} values={generalDetailValues(stream)} />
                            <SummaryRow label="Providers" icon={Globe} values={activeProviderNames(stream)} />
                            <SummaryRow label="Indexers" icon={Server} values={stream.indexer_selections || Object.keys(stream.indexer_overrides || {})} />
                            <SummaryRow label="Movie" icon={Search} values={stream.movie_search_queries || []} />
                            <SummaryRow label="TV" icon={Search} values={stream.series_search_queries || []} />
                            <SummaryRow label="Filter/Sorting" icon={ArrowUpDown} values={filterSortingSummaryValues(stream)} />
                            <SummaryRow label="Metadata" icon={Clapperboard} values={metadataSummaryValues(stream)} />
                            <SummaryRow label="Formatting" icon={Type} values={formattingSummaryValues(stream)} />
                          </div>
                        ) : (
                          <div className="grid grid-cols-4 gap-3 md:grid-cols-4 xl:grid-cols-8">
                            <div className="space-y-1 text-center sm:text-left">
                              <div className="flex items-center justify-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground sm:justify-start">
                                <Settings className="h-3.5 w-3.5" />
                                <span className="hidden sm:inline">General</span>
                              </div>
                              <div className="flex flex-wrap items-center justify-center gap-2 sm:justify-start">
                                {generalCompactValues(stream).map((value) => (
                                  <div key={value} className="inline-flex items-center justify-center rounded-full border border-border px-2 py-1 text-xs text-muted-foreground">
                                    {value}
                                  </div>
                                ))}
                              </div>
                            </div>
                            <div className="space-y-1 text-center sm:text-left">
                              <div className="flex items-center justify-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground sm:justify-start">
                                <Globe className="h-3.5 w-3.5" />
                                <span className="hidden sm:inline">Providers</span>
                              </div>
                              <div className="inline-flex items-center justify-center rounded-full border border-border px-2 py-1 text-xs text-muted-foreground">{activeProviderNames(stream).length}</div>
                            </div>
                            <div className="space-y-1 text-center sm:text-left">
                              <div className="flex items-center justify-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground sm:justify-start">
                                <Server className="h-3.5 w-3.5" />
                                <span className="hidden sm:inline">Indexers</span>
                              </div>
                              <div className="inline-flex items-center justify-center rounded-full border border-border px-2 py-1 text-xs text-muted-foreground">{(stream.indexer_selections || Object.keys(stream.indexer_overrides || {})).length}</div>
                            </div>
                            <div className="space-y-1 text-center sm:text-left">
                              <div className="flex items-center justify-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground sm:justify-start">
                                <Search className="h-3.5 w-3.5" />
                                <span className="hidden sm:inline">Movie</span>
                              </div>
                              <div className="inline-flex items-center justify-center rounded-full border border-border px-2 py-1 text-xs text-muted-foreground">{(stream.movie_search_queries || []).length}</div>
                            </div>
                            <div className="space-y-1 text-center sm:text-left">
                              <div className="flex items-center justify-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground sm:justify-start">
                                <Search className="h-3.5 w-3.5" />
                                <span className="hidden sm:inline">TV</span>
                              </div>
                              <div className="inline-flex items-center justify-center rounded-full border border-border px-2 py-1 text-xs text-muted-foreground">{(stream.series_search_queries || []).length}</div>
                            </div>
                            <div className="space-y-1 text-center sm:text-left">
                              <div className="flex items-center justify-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground sm:justify-start">
                                <ArrowUpDown className="h-3.5 w-3.5" />
                                <span className="hidden sm:inline">Filter/Sorting</span>
                              </div>
                              <div className="flex flex-wrap items-center justify-center gap-2 sm:justify-start">
                                {filterSortingSummaryValues(stream).map((value) => (
                                  <div key={value} className="inline-flex items-center justify-center rounded-full border border-border px-2 py-1 text-xs text-muted-foreground">
                                    {value}
                                  </div>
                                ))}
                              </div>
                            </div>
                            <div className="space-y-1 text-center sm:text-left">
                              <div className="flex items-center justify-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground sm:justify-start">
                                <Clapperboard className="h-3.5 w-3.5" />
                                <span className="hidden sm:inline">Metadata</span>
                              </div>
                              <div className="flex flex-wrap items-center justify-center gap-2 sm:justify-start">
                                {metadataSummaryValues(stream).map((value) => (
                                  <div key={value} className="inline-flex items-center justify-center rounded-full border border-border px-2 py-1 text-xs text-muted-foreground">
                                    {value}
                                  </div>
                                ))}
                              </div>
                            </div>
                            <div className="space-y-1 text-center sm:text-left">
                              <div className="flex items-center justify-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground sm:justify-start">
                                <Type className="h-3.5 w-3.5" />
                                <span className="hidden sm:inline">Formatting</span>
                              </div>
                              <div className="flex flex-wrap items-center justify-center gap-2 sm:justify-start">
                                {formattingSummaryValues(stream).map((value) => (
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
                                onClick={() => toggleExpandedStream(stream.username)}
                                className="h-7 w-9 rounded-md border-dashed border-border/80 bg-muted text-muted-foreground shadow-sm hover:bg-muted/90"
                                aria-label={expandedStreams[stream.username] ? `Hide details for ${stream.username}` : `Show details for ${stream.username}`}
                              >
                                {expandedStreams[stream.username] ? <ChevronUp className="h-4 w-4" /> : <ChevronDown className="h-4 w-4" />}
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent>{expandedStreams[stream.username] ? 'Hide details' : 'Show details'}</TooltipContent>
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
            initialStream={addDialogDraft}
            mode="add"
            existingNames={streams.map((stream) => stream.username).filter(Boolean)}
            providerNames={providerNames}
            providerConnectionTotals={providerConnectionTotals}
            enabledProviderNames={enabledProviderNames}
            indexerNames={indexerNames}
            enabledIndexerNames={enabledIndexerNames}
            movieQueryNames={movieQueryNames}
            seriesQueryNames={seriesQueryNames}
            filterProfiles={globalConfig?.filter_profiles || []}
            metadataProfiles={globalConfig?.metadata_profiles || []}
            formatProfiles={globalConfig?.format_profiles || []}
            globalConfig={globalConfig}
            onSave={handleCreateStream}
            saving={dialogSaving}
          />

          <StreamDialog
            open={Boolean(editingStream)}
            onOpenChange={(nextOpen) => {
              if (!nextOpen) setEditingStream(null)
            }}
            initialStream={editingStream}
            mode="edit"
            existingNames={streams.map((stream) => stream.username).filter(Boolean)}
            providerNames={providerNames}
            providerConnectionTotals={providerConnectionTotals}
            enabledProviderNames={enabledProviderNames}
            indexerNames={indexerNames}
            enabledIndexerNames={enabledIndexerNames}
            movieQueryNames={movieQueryNames}
            seriesQueryNames={seriesQueryNames}
            filterProfiles={globalConfig?.filter_profiles || []}
            metadataProfiles={globalConfig?.metadata_profiles || []}
            formatProfiles={globalConfig?.format_profiles || []}
            globalConfig={globalConfig}
            onSave={handleSaveStream}
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
            void handleDeleteStream(username)
          }
        }}
      />
      <ConfirmDialog
        open={Boolean(regenerateTarget)}
        onOpenChange={(nextOpen) => {
          if (!nextOpen) setRegenerateTarget('')
        }}
        title="Regenerate token?"
        description={regenerateTarget ? `Are you sure you want to regenerate the manifest token for stream "${regenerateTarget}"? Existing links using the old token will stop working.` : ''}
        confirmLabel="Regenerate"
        onConfirm={() => {
          const username = regenerateTarget
          setRegenerateTarget('')
          if (username) {
            void handleRegenerateToken(username)
          }
        }}
      />
    </TooltipProvider>
  )
}

export default React.memo(StreamManagement)
