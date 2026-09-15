import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { PasswordInput } from "@/components/ui/password-input"
import { Switch } from "@/components/ui/switch"
import { CONTENT_KINDS } from "@/lib/profiles"
import {
  MAX_ADDON_NAME_LENGTH,
  applyFilterSortingMode,
  buildIndexerOverrides,
  buildStreamDraft,
  buildStreamStateFromDraft,
  defaultAddonName,
  filterSortingLabel,
  generalCompactValues,
  getInitialStreamDraft,
  indexerModeLabel,
  mapStreamsByUsername,
  metadataSummaryValues,
  nextStreamName,
  normalizeStreamDraft,
  resultsModeLabel,
  VARIANT_ATTEMPTS_UNLIMITED,
  variantAttemptsLabel,
  preloadAttemptsLabel,
  streamsFromMap,
  tabHasError,
} from '@/lib/streams'
import { uniquePreserveOrder } from "@/lib/lists"
import { isAvailNZBEnabled } from "@/lib/availnzb"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { ManagerShell } from "@/components/ManagerShell"
import { useStreamDraft } from "@/hooks/useStreamDraft"
import { MDBListCard, SimklCard } from "@/components/WatchTrackingCards"
import { SelectionSection } from "@/components/ui/selection-section"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { apiFetch } from "@/api"
import { copyToClipboard } from "@/lib/utils"
import { Check, ChevronDown, Clipboard, Copy, Loader2, Plus, Puzzle, RefreshCw, Save, Search, Server, Settings, Trash2, Tv, Type, X, Zap } from "lucide-react"

const CACHE_CLEARED_SUFFIX = ' Search cache cleared.'

function EndpointPanel({ title, icon: Icon, hint, children }) {
  return (
    <div className="min-w-0 space-y-2 rounded-md border border-border/70 bg-muted/20 px-3 py-2.5">
      <div className="flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
        {Icon && <Icon className="h-3.5 w-3.5" />}
        <span>{title}</span>
      </div>
      <div className="space-y-1.5">{children}</div>
      {hint ? <p className="text-[11px] leading-4 text-muted-foreground">{hint}</p> : null}
    </div>
  )
}

// EndpointRow lays out a label beside a read-only value (or an input when
// there is no value) and trailing action buttons.
function EndpointRow({ label, value, htmlFor, children }) {
  return (
    <div className="grid grid-cols-[4.5rem_minmax(0,1fr)] items-center gap-2">
      <Label className="text-xs text-muted-foreground" htmlFor={htmlFor}>{label}</Label>
      <div className="flex min-w-0 items-center gap-2">
        {value !== undefined ? (
          <code className="block min-w-0 flex-1 break-all rounded bg-muted px-2.5 py-1.5 text-[11px] leading-5">{value}</code>
        ) : null}
        {children}
      </div>
    </div>
  )
}

function CopyButton({ copied, onCopy, label }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button type="button" variant="ghost" size="icon" onClick={onCopy} className="h-8 w-8 shrink-0 bg-muted hover:bg-muted" aria-label={label}>
          {copied ? <Check className="h-3.5 w-3.5" /> : <Clipboard className="h-3.5 w-3.5" />}
        </Button>
      </TooltipTrigger>
      <TooltipContent>{copied ? 'Copied' : label}</TooltipContent>
    </Tooltip>
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
// StreamNameDialog is the one place a stream name is typed. Creating and
// renaming share it because they ask the same question and refuse the same
// answers — a blank name, or one already taken.
function StreamNameDialog({ open, onOpenChange, title, description, value, onValueChange, taken, confirmLabel, saving, onConfirm }) {
  const trimmed = (value || '').trim()
  const clash = taken.some((name) => name.toLowerCase() === trimmed.toLowerCase())
  const problem = !trimmed ? 'Stream name is required' : clash ? 'A stream with that name already exists.' : ''
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-sm p-4 sm:p-6">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <Input
          autoFocus
          value={value || ''}
          onChange={(event) => onValueChange(event.target.value)}
          onKeyDown={(event) => { if (event.key === 'Enter' && !problem && !saving) onConfirm(trimmed) }}
          placeholder="Stream01"
          className={problem && trimmed ? 'border-destructive focus-visible:ring-destructive' : ''}
        />
        {problem && trimmed && <p className="text-xs text-destructive">{problem}</p>}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button type="button" disabled={Boolean(problem) || saving} onClick={() => onConfirm(trimmed)}>
            {saving && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
            {confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// AddStreamDialog asks only for the name. Everything else is edited inline
// once the stream exists, which is also how the profile pages create.
function AddStreamDialog({ open, onOpenChange, draft, existingNames, saving, onCreate }) {
  const [name, setName] = useState('')
  useEffect(() => { if (open) setName(draft?.username || '') }, [open, draft])
  return (
    <StreamNameDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Add stream"
      description="Pick a name. It is the Jellyfin login for this stream, and it labels its addon."
      value={name}
      onValueChange={setName}
      taken={existingNames}
      confirmLabel="Create"
      saving={saving}
      onConfirm={(trimmed) => onCreate({ ...(draft || {}), username: trimmed })}
    />
  )
}

function RenameStreamDialog({ target, onChange, existingNames, saving, onRename }) {
  return (
    <StreamNameDialog
      open={Boolean(target)}
      onOpenChange={(nextOpen) => { if (!nextOpen) onChange(null) }}
      title="Rename stream"
      description="The token is untouched, so an installed addon URL keeps working. The stream's history moves with it."
      value={target?.to ?? ''}
      onValueChange={(next) => onChange({ ...target, to: next })}
      taken={existingNames.filter((name) => name !== target?.from)}
      confirmLabel="Rename"
      saving={saving}
      onConfirm={(trimmed) => onRename(target.from, trimmed)}
    />
  )
}

// StreamEditor is the detail pane of the streams master/detail: every setting
// a stream carries, under tabs. The draft is owned by the page (useStreamDraft)
// so edits auto-save as they are made, and the stream's name is not here at
// all — it is a login identity, renamed deliberately from the header.
function StreamEditor({
  draft,
  setDraft,
  fieldErrors = {},
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
  onAccountsChange,
}) {
  const availNZBEnabled = isAvailNZBEnabled(globalConfig?.availnzb_mode)
  const [activeTab, setActiveTab] = useState('general')

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

  const aiostreamsMode = draft.filter_sorting_mode === 'aiostreams'

  return (
    <Tabs value={activeTab} onValueChange={setActiveTab} className="w-full space-y-4">
          <TabsList className="w-full justify-start overflow-x-auto">
            {STREAM_DIALOG_TABS.map((tab) => (
              <TabsTrigger
                key={tab.id}
                value={tab.id}
                // A refused save marks the tab holding the field at fault, so
                // the complaint in the header has somewhere to point.
                className={tabHasError(tab.id, fieldErrors) ? 'text-destructive data-[state=active]:text-destructive' : ''}
              >
                {tab.label}
              </TabsTrigger>
            ))}
          </TabsList>

          <TabsContent value="general">
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

              <>
                  <SimklCard
                    stream={draft.username}
                    onAccountChange={onAccountsChange}
                    scrobble={draft.simkl_scrobble}
                    onScrobbleChange={(checked) => setDraft((current) => ({ ...current, simkl_scrobble: checked }))}
                  />
                  <MDBListCard
                    stream={draft.username}
                    scrobble={draft.mdblist_scrobble}
                    onScrobbleChange={(checked) => setDraft((current) => ({ ...current, mdblist_scrobble: checked }))}
                  />
              </>

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
          </TabsContent>

          <TabsContent value="providers">
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
          </TabsContent>

          <TabsContent value="indexers">
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
          </TabsContent>

          <TabsContent value="search">
            <div className="space-y-4">
              <p className="text-sm text-muted-foreground">
                Every selected request runs and the results are combined. Each request decides on its own
                which attempts to make and when to stop.
              </p>
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
          </TabsContent>

          <TabsContent value="advanced">
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
          </TabsContent>
    </Tabs>
  )
}

// StreamEndpoints is how a client reaches this stream: the Stremio manifest
// URL its token addresses, and the Jellyfin login the same stream answers to.
// It sits above the settings because it is what a freshly created stream is
// for — the thing you copy into a client before configuring anything.
function StreamEndpoints({
  stream,
  manifestUrl,
  jellyfinUrl,
  copiedKey,
  onCopy,
  busy,
  onRegenerate,
  passwordDraft,
  onPasswordDraftChange,
  onSetPassword,
  actionLoading,
}) {
  return (
      <div className="grid gap-3 lg:grid-cols-2">
        <EndpointPanel title="Stremio" icon={Puzzle}>
          <EndpointRow label="Manifest" value={manifestUrl}>
            <CopyButton
              copied={copiedKey === `manifest-${stream.username}`}
              onCopy={() => onCopy(`manifest-${stream.username}`, manifestUrl)}
              label="Copy manifest URL"
            />
            <Tooltip>
              <TooltipTrigger asChild>
                <Button type="button" variant="outline" size="icon" onClick={() => onRegenerate()} disabled={busy} className="h-8 w-8 shrink-0" aria-label={`Regenerate token for ${stream.username}`}>
                  {actionLoading === `regenerate-${stream.username}` ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <RefreshCw className="h-3.5 w-3.5" />}
                </Button>
              </TooltipTrigger>
              <TooltipContent>Regenerate token</TooltipContent>
            </Tooltip>
          </EndpointRow>
        </EndpointPanel>

        <EndpointPanel title="Jellyfin" icon={Tv} hint="Sign in from Swiftfin, Infuse or Findroid with these. The stream token works as the password too.">
          <EndpointRow label="Server" value={jellyfinUrl}>
            <CopyButton
              copied={copiedKey === `jellyfin-url-${stream.username}`}
              onCopy={() => onCopy(`jellyfin-url-${stream.username}`, jellyfinUrl)}
              label="Copy server URL"
            />
          </EndpointRow>
          <EndpointRow label="Username" value={stream.username}>
            <CopyButton
              copied={copiedKey === `jellyfin-user-${stream.username}`}
              onCopy={() => onCopy(`jellyfin-user-${stream.username}`, stream.username)}
              label="Copy username"
            />
          </EndpointRow>
          <EndpointRow label="Password" htmlFor={`jellyfin-password-${stream.username}`}>
            <PasswordInput
              id={`jellyfin-password-${stream.username}`}
              className="h-8 text-[11px]"
              placeholder={stream.has_password ? 'Password set — type to replace' : 'Not set — type to set one'}
              autoComplete="new-password"
              value={passwordDraft || ''}
              onChange={(e) => onPasswordDraftChange(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter' && (passwordDraft || '').trim()) onSetPassword(false) }}
            />
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  className="h-8 w-8 shrink-0"
                  disabled={busy || !(passwordDraft || '').trim()}
                  onClick={() => onSetPassword(false)}
                  aria-label={`Save Jellyfin password for ${stream.username}`}
                >
                  {actionLoading === `password-${stream.username}` ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
                </Button>
              </TooltipTrigger>
              <TooltipContent>Save password</TooltipContent>
            </Tooltip>
            {stream.has_password ? (
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="h-8 w-8 shrink-0"
                    disabled={busy}
                    onClick={() => onSetPassword(true)}
                    aria-label={`Remove Jellyfin password for ${stream.username}`}
                  >
                    <X className="h-3.5 w-3.5" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>Remove password (token still works)</TooltipContent>
              </Tooltip>
            ) : null}
          </EndpointRow>
        </EndpointPanel>
      </div>
  )
}

function StreamManagement({ globalConfig, movieSearchQueries = [], seriesSearchQueries = [], initialStreamsByName = {}, onStreamsChange, onStatus }) {
  const initialStreams = useMemo(() => streamsFromMap(initialStreamsByName), [initialStreamsByName])
  const initialStreamsSignature = useMemo(() => JSON.stringify(initialStreams), [initialStreams])
  const initialFetchStartedRef = useRef(false)
  const lastAppliedInitialSignatureRef = useRef(initialStreamsSignature)
  const [streams, setStreams] = useState(() => initialStreams)
  // A ref mirror so the auto-save callback can read the current list without
  // being rebuilt on every refresh, which would restart the debounce.
  const streamsRef = useRef(streams)
  useEffect(() => { streamsRef.current = streams }, [streams])
  const [loading, setLoading] = useState(false)
  const [actionLoading, setActionLoading] = useState(null)
  const [creating, setCreating] = useState(false)
  const [showAddDialog, setShowAddDialog] = useState(false)
  const [addDialogDraft, setAddDialogDraft] = useState(null)
  const [selectedIndex, setSelectedIndex] = useState(0)
  const [renameTarget, setRenameTarget] = useState(null)
  const [copiedKey, setCopiedKey] = useState('')
  const [visibleFooterStatus, setVisibleFooterStatus] = useState(null)
  const [footerStatusVisible, setFooterStatusVisible] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState('')
  const [regenerateTarget, setRegenerateTarget] = useState('')
  // Draft Jellyfin passwords, per stream. Held here rather than in the stream
  // list because the server only ever returns whether one is set, never a value.
  const [passwordDrafts, setPasswordDrafts] = useState({})

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

  const endpointBaseUrl = globalConfig?.addon_base_url
    ? globalConfig.addon_base_url.replace(/\/$/, '')
    : window.location.origin
  const getManifestUrl = (token) => `${endpointBaseUrl}/${token}/manifest.json`
  // The Jellyfin endpoint is one URL for every stream; what differs per stream
  // is the login, which is the stream name and its password (or token).
  const jellyfinUrl = `${endpointBaseUrl}/jellyfin`

  const copyValue = (key, value) => {
    copyToClipboard(value).then(() => {
      setCopiedKey(key)
      setTimeout(() => setCopiedKey(''), 2000)
    })
  }

  const saveStreamAssignments = async (username, draft, existingStream) => {
    const payload = {
      [username]: {
        filter_sorting_mode: draft.filter_sorting_mode,
        indexer_mode: draft.indexer_mode,
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
        simkl_scrobble: draft.simkl_scrobble === true,
        mdblist_scrobble: draft.mdblist_scrobble === true,
      },
    }
    await apiFetch('/api/streams/configs', {
      method: 'PUT',
      body: JSON.stringify(payload),
    })
  }

  const refreshStreamsAfterMutation = async () => {
    try {
      return await fetchStreams(false, { silent: true })
    } catch {
      // Preserve the successful mutation state when only the refresh fails.
      return null
    }
  }

  const handleCreateStream = async (draft) => {
    setCreating(true)
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
      setCreating(false)
    }
    if (created) {
      const next = await refreshStreamsAfterMutation()
      const index = (next || []).findIndex((stream) => stream.username === draft.username)
      if (index >= 0) setSelectedIndex(index)
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

  // Settings auto-save through useStreamDraft; the API validates the payload,
  // so a refusal comes back with per-field errors the editor marks tabs from.
  const saveStreamSettings = useCallback(async (username, draft) => {
    const existing = streamsRef.current.find((stream) => stream.username === username)
    await saveStreamAssignments(username, normalizeStreamDraft(draft), existing)
    setStreams((prev) => {
      const next = prev.map((stream) =>
        stream.username === username
          // Merged onto the stream rather than replacing it: the builder only
          // knows the fields the editor owns, and facts like has_password
          // belong to the stream alone. Auto-save does not refetch, so a
          // field dropped here stays dropped.
          ? { ...stream, ...buildStreamStateFromDraft(username, stream.token, draft, existing?.indexer_overrides) }
          : stream
      )
      onStreamsChange?.(mapStreamsByUsername(next))
      return next
    })
    showFooterStatus({ type: 'success', message: `Stream "${username}" saved.${CACHE_CLEARED_SUFFIX}` })
  }, [onStreamsChange, showFooterStatus])

  // Renaming is its own request because the name is the credential clients
  // authenticate with: it is never something the settings debounce does on the
  // way past, and the token survives so an installed addon URL keeps working.
  const handleRenameStream = async (previousName, nextName) => {
    const trimmed = (nextName || '').trim()
    if (!trimmed || trimmed === previousName) return
    setActionLoading(`rename-${previousName}`)
    showStatus(null)
    try {
      await apiFetch(`/api/streams/${encodeURIComponent(previousName)}/rename`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: trimmed }),
      })
      setPasswordDrafts((prev) => {
        if (!(previousName in prev)) return prev
        const { [previousName]: moved, ...rest } = prev
        return moved ? { ...rest, [trimmed]: moved } : rest
      })
      setRenameTarget(null)
      const status = { type: 'success', message: `Stream renamed to "${trimmed}".${CACHE_CLEARED_SUFFIX}` }
      showStatus(status)
      showFooterStatus(status)
      const next = await refreshStreamsAfterMutation()
      // Follow the stream that was just renamed rather than whatever now sits
      // at the old index.
      const moved = (next || []).findIndex((stream) => stream.username === trimmed)
      if (moved >= 0) setSelectedIndex(moved)
    } catch (err) {
      const status = { type: 'error', message: err.message || 'Failed to rename stream' }
      showStatus(status)
      showFooterStatus(status)
    } finally {
      setActionLoading(null)
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
      // Drop any unsaved password typed for the stream that just went away,
      // or a stream later created under the same name would open with it
      // already in the field.
      setPasswordDrafts((prev) => {
        if (!(username in prev)) return prev
        const { [username]: _removed, ...rest } = prev
        return rest
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

  // Setting a Jellyfin password is a request of its own rather than part of the
  // stream config save: the plaintext should not travel with everything else,
  // and the server hands back only whether one is now set.
  const handleSetPassword = async (username, clear = false) => {
    const password = clear ? '' : (passwordDrafts[username] || '')
    setActionLoading(`password-${username}`)
    showStatus(null)
    try {
      const payload = await apiFetch(`/api/streams/${encodeURIComponent(username)}/password`, {
        method: 'POST',
        body: JSON.stringify({ password }),
      })
      setStreams((prev) => {
        const next = prev.map((stream) => stream.username === username ? { ...stream, has_password: payload.has_password } : stream)
        onStreamsChange?.(mapStreamsByUsername(next))
        return next
      })
      setPasswordDrafts((prev) => ({ ...prev, [username]: '' }))
      const status = {
        type: 'success',
        message: payload.has_password ? `Jellyfin password set for "${username}"` : `Jellyfin password removed for "${username}"`,
      }
      showStatus(status)
      showFooterStatus(status)
    } catch (err) {
      const status = { type: 'error', message: err.message || 'Failed to update the Jellyfin password' }
      showStatus(status)
      showFooterStatus(status)
    } finally {
      setActionLoading(null)
    }
  }

  const selectedStream = streams[selectedIndex] || null
  const buildDraft = useCallback(
    (stream, isExisting = true) =>
      getInitialStreamDraft(stream, isExisting, enabledProviderNames, enabledIndexerNames, movieQueryNames, seriesQueryNames),
    [enabledProviderNames, enabledIndexerNames, movieQueryNames, seriesQueryNames],
  )
  const { draft, setDraft, dirty, saving, error: draftError, fieldErrors } = useStreamDraft({
    stream: selectedStream,
    buildDraft,
    onSave: saveStreamSettings,
  })

  // A delete or an external removal can leave the selection past the end.
  useEffect(() => {
    if (selectedIndex > 0 && selectedIndex >= streams.length) {
      setSelectedIndex(Math.max(0, streams.length - 1))
    }
  }, [streams.length, selectedIndex])

  const openAddDialog = () => {
    setAddDialogDraft(buildDraft({ username: nextStreamName(streams) }, false))
    setShowAddDialog(true)
  }

  return (
    <TooltipProvider delayDuration={100}>
      <div className="space-y-6">
        <div>
          <h2 className="flex items-center gap-2 text-lg font-medium text-foreground">
            <Zap className="h-4 w-4" /> Streams
          </h2>
          <p className="text-sm text-muted-foreground">
            Each stream is one person&apos;s addon: its own manifest URL, its own providers, indexers and
            search order, and its own watch-tracking accounts. Settings save as you change them.
          </p>
        </div>

        {loading && streams.length === 0 ? (
          <div className="flex items-center justify-center p-8"><Loader2 className="h-6 w-6 animate-spin" /></div>
        ) : (
          <ManagerShell
            items={streams.map((stream) => ({
              key: stream.username,
              name: stream.username,
              summary: generalCompactValues(stream).join(' · '),
              usageLabels: metadataSummaryValues(stream),
              emptyUsageText: 'No metadata profile',
            }))}
            selectedIndex={selectedIndex}
            onSelect={setSelectedIndex}
            emptyText="No streams yet. Create one to get an addon URL."
            emptyActions={
              <Button size="sm" onClick={openAddDialog}>
                <Plus className="mr-2 h-4 w-4" /> Create one
              </Button>
            }
            listActions={
              <Button size="sm" className="flex-1" onClick={openAddDialog}>
                <Plus className="mr-2 h-4 w-4" /> New stream
              </Button>
            }
            title={selectedStream && (
              <div className="flex min-w-0 items-center gap-2">
                <span className="truncate text-sm font-medium">{selectedStream.username}</span>
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-8"
                  onClick={() => setRenameTarget({ from: selectedStream.username, to: selectedStream.username })}
                >
                  <Type className="mr-2 h-3.5 w-3.5" /> Rename
                </Button>
              </div>
            )}
            status={
              saving ? (
                <span className="flex items-center gap-1.5"><Loader2 className="h-3.5 w-3.5 animate-spin" /> Saving…</span>
              ) : dirty ? (
                // The debounce is still counting down — no request exists
                // yet, so no spinner pretends one does.
                'Unsaved changes'
              ) : (
                <span className="text-muted-foreground/60">Saves automatically</span>
              )
            }
            actions={selectedStream && (
              <>
                <Button variant="ghost" size="sm" className="h-8" onClick={() => handleCloneStream(selectedStream)}>
                  <Copy className="mr-2 h-3.5 w-3.5" /> Duplicate
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-8 text-destructive hover:text-destructive"
                  onClick={() => setDeleteTarget(selectedStream.username)}
                >
                  <Trash2 className="mr-2 h-3.5 w-3.5" /> Delete
                </Button>
              </>
            )}
            error={draftError}
            footer={
              <>
                <AddStreamDialog
                  open={showAddDialog}
                  onOpenChange={(nextOpen) => {
                    setShowAddDialog(nextOpen)
                    if (!nextOpen) setAddDialogDraft(null)
                  }}
                  draft={addDialogDraft}
                  existingNames={streams.map((stream) => stream.username).filter(Boolean)}
                  saving={creating}
                  onCreate={handleCreateStream}
                />
                <RenameStreamDialog
                  target={renameTarget}
                  onChange={setRenameTarget}
                  existingNames={streams.map((stream) => stream.username).filter(Boolean)}
                  saving={actionLoading === `rename-${renameTarget?.from}`}
                  onRename={handleRenameStream}
                />
                <ConfirmDialog
                  open={Boolean(deleteTarget)}
                  onOpenChange={(nextOpen) => { if (!nextOpen) setDeleteTarget('') }}
                  title="Delete stream?"
                  description={deleteTarget ? `Are you sure you want to delete stream "${deleteTarget}"?` : ''}
                  confirmLabel="Delete"
                  onConfirm={() => {
                    const username = deleteTarget
                    setDeleteTarget('')
                    if (username) void handleDeleteStream(username)
                  }}
                />
                <ConfirmDialog
                  open={Boolean(regenerateTarget)}
                  onOpenChange={(nextOpen) => { if (!nextOpen) setRegenerateTarget('') }}
                  title="Regenerate token?"
                  description={regenerateTarget ? `Are you sure you want to regenerate the manifest token for stream "${regenerateTarget}"? Existing links using the old token will stop working.` : ''}
                  confirmLabel="Regenerate"
                  onConfirm={() => {
                    const username = regenerateTarget
                    setRegenerateTarget('')
                    if (username) void handleRegenerateToken(username)
                  }}
                />
              </>
            }
          >
            {selectedStream && draft && (
              <>
                <StreamEndpoints
                  stream={selectedStream}
                  manifestUrl={getManifestUrl(selectedStream.token)}
                  jellyfinUrl={jellyfinUrl}
                  copiedKey={copiedKey}
                  onCopy={copyValue}
                  busy={actionLoading !== null || loading}
                  actionLoading={actionLoading}
                  onRegenerate={() => setRegenerateTarget(selectedStream.username)}
                  passwordDraft={passwordDrafts[selectedStream.username]}
                  onPasswordDraftChange={(value) =>
                    setPasswordDrafts((prev) => ({ ...prev, [selectedStream.username]: value }))}
                  onSetPassword={(clear) => void handleSetPassword(selectedStream.username, clear)}
                />
                <StreamEditor
                  draft={draft}
                  setDraft={setDraft}
                  fieldErrors={fieldErrors}
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
                  onAccountsChange={refreshStreamsAfterMutation}
                />
              </>
            )}
        </ManagerShell>
      )}
      </div>
      {visibleFooterStatus?.message && (
        <div
          className={`pointer-events-none fixed bottom-4 left-1/2 z-50 -translate-x-1/2 rounded-md border px-4 py-2 text-sm shadow-lg transition-all duration-300 ${
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
    </TooltipProvider>
  )
}

export default React.memo(StreamManagement)
