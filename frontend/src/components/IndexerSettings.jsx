import React, { useEffect, useMemo, useRef, useState } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { ComponentHealthBadge } from "@/components/ComponentHealth"
import { healthFor, healthReasonLabel, indexHealth, isBlocked } from "@/lib/health"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { EntityDialog } from "@/components/EntityDialog"
import {
  useEntityDialog,
  dialogRowClass, dialogLabelClass, dialogControlWideClass, dialogControlNameClass, dialogControlNarrowClass,
} from "@/hooks/useEntityDialog"
import { UsageChips } from "@/components/UsageChips"
import { apiFetch } from "@/api"
import { assignedStreams } from "@/lib/usage"
import { mapStreamsByUsername } from "@/lib/streams"
import { AlertTriangle, Download, Plus, Settings, Trash2 } from "lucide-react"

function normalizeName(value) {
  return (value || '').trim().toLowerCase()
}

function normalizeIndexerIdentity(draft) {
  const next = normalizeIndexerDraft(draft)
  if (next.type === 'easynews') {
    return `easynews::${normalizeName(next.username)}`
  }
  return `indexer::${normalizeName(next.type)}::${normalizeName(next.url)}::${normalizeName(next.api_path)}::${normalizeName(next.api_key)}::${normalizeName(next.proxy_url)}`
}

const INDEXER_PRESETS = [
  { name: 'NZBHydra2', url: 'http://localhost:5076', api_path: '/api', type: 'aggregator', api_hits_day: 0, downloads_day: 0 },
  { name: 'Prowlarr', url: 'http://localhost:9696', api_path: '{indexer_id}/api', type: 'aggregator', api_hits_day: 0, downloads_day: 0 },
  { name: 'abNZB', url: 'https://abnzb.com', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'altHUB', url: 'https://api.althub.co.za', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'aniNZB', url: 'https://aninzb.moe', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50, optional_api_key: true, content_scope: 'anime' },
  { name: 'AnimeTosho (Usenet)', url: 'https://feed.animetosho.org', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50, content_scope: 'anime' },
  { name: 'DOGnzb', url: 'https://api.dognzb.cr', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'DrunkenSlug', url: 'https://drunkenslug.com', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'GingaDADDY', url: 'https://www.gingadaddy.com', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'Miatrix', url: 'https://www.miatrix.com', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'Newz69', url: 'https://newz69.keagaming.com', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'NinjaCentral', url: 'https://ninjacentral.co.za', api_path: '/api', type: 'newznab', api_hits_day: 2000, downloads_day: 450, rate_limit_rps: 0, timeout_seconds: 5 },
  { name: 'Nzb.life', url: 'https://api.nzb.life', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'NZBCat', url: 'https://nzb.cat', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'NZBFinder', url: 'https://nzbfinder.ws', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'NZBgeek', url: 'https://api.nzbgeek.info', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'NzbNoob', url: 'https://www.nzbnoob.com', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'NZBNDX', url: 'https://www.nzbndx.com', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'NzbPlanet', url: 'https://api.nzbplanet.net', api_path: '/api', type: 'newznab', api_hits_day: 5000, downloads_day: 0, rate_limit_rps: 0, timeout_seconds: 5 },
  { name: 'NZBStars', url: 'https://nzbstars.com', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'SceneNZBs', url: 'https://scenenzbs.com', api_path: '/api', type: 'newznab', api_hits_day: 0, downloads_day: 400, rate_limit_rps: 5, timeout_seconds: 5, grab_header: 'SABnzbd/4.3.0' },
  { name: 'Tabula Rasa', url: 'https://www.tabula-rasa.pw', api_path: '/api/v1', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'Usenet Crawler', url: 'https://www.usenet-crawler.com', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'Easynews', url: '', api_path: '/api', type: 'easynews', api_hits_day: 100, downloads_day: 50 },
]

const PROWLARR_INDEXER_ID_PLACEHOLDER = '{indexer_id}'
const CACHE_CLEARED_SUFFIX = ' Search cache cleared.'
const EASYNEWS_TIMEOUT_HINT = 'Applies to Easynews searches. NZB downloads use double this timeout.'

function normalizeContentScope(value) {
  return value === 'anime' || value === 'non_anime' ? value : ''
}

function normalizeIndexerDraft(draft) {
  const value = draft || {}
  return {
    name: (value.name || '').trim(),
    url: (value.url || '').trim(),
    api_path: value.api_path || '/api',
    api_key: value.api_key || '',
    type: value.type || 'newznab',
    api_hits_day: Number(value.api_hits_day || 0),
    downloads_day: Number(value.downloads_day || 0),
    rate_limit_rps: Number(value.rate_limit_rps || 0),
    timeout_seconds: Number(value.timeout_seconds || 0),
    search_results_cache_time: Number(value.search_results_cache_time || 0),
    enabled: value.enabled !== false,
    username: value.username || '',
    password: value.password || '',
    proxy_url: (value.proxy_url || '').trim(),
    query_header: value.type === 'easynews' ? '' : (value.query_header || '').trim(),
    grab_header: (value.grab_header || '').trim(),
    content_scope: normalizeContentScope(value.content_scope),
  }
}

function emptyIndexerDraft() {
  return normalizeIndexerDraft({})
}

function getDefaultIndexerTimeoutSeconds(type) {
  if (type === 'easynews') return 15
  return type === 'aggregator' ? 10 : 5
}

function getPresetDefaults(preset) {
  return {
    timeout_seconds: Number(preset.timeout_seconds || getDefaultIndexerTimeoutSeconds(preset.type)),
    api_hits_day: Number(preset.api_hits_day || 0),
    downloads_day: Number(preset.downloads_day || 0),
    rate_limit_rps: Number(preset.rate_limit_rps || 0),
    query_header: preset.query_header || '',
    grab_header: preset.grab_header || '',
    content_scope: normalizeContentScope(preset.content_scope),
  }
}

function formatLimitValue(value) {
  return value > 0 ? String(value) : '∞'
}

function summarizeIndexer(indexer, defaultProxyURL = '') {
  const parts = []
  parts.push(indexer.type === 'aggregator' ? 'Aggregator' : indexer.type === 'easynews' ? 'Easynews' : 'Newznab')
  if (indexer.url) parts.push(indexer.url)
  if (indexer.timeout_seconds > 0) parts.push(`Timeout: ${indexer.timeout_seconds}s`)
  else parts.push(`Timeout: ${getDefaultIndexerTimeoutSeconds(indexer.type)}s default`)
  parts.push(`Hits/day: ${formatLimitValue(indexer.api_hits_day)}`)
  parts.push(`DLs/day: ${formatLimitValue(indexer.downloads_day)}`)
  parts.push(`RPS: ${formatLimitValue(indexer.rate_limit_rps)}`)
  if (indexer.content_scope === 'anime') parts.push('Content: anime only')
  else if (indexer.content_scope === 'non_anime') parts.push('Content: no anime')
  if (indexer.proxy_url) parts.push('Proxy: override')
  else if (defaultProxyURL) parts.push('Proxy: default')
  if (indexer.grab_header) parts.push(`Grab UA: ${indexer.grab_header}`)
  if (indexer.query_header) parts.push(`Query UA: ${indexer.query_header}`)
  if (indexer.search_results_cache_time > 0) parts.push(`Cache time: ${indexer.search_results_cache_time}m`)
  return parts
}

function assignedStreamsForIndexer(streamsByName, indexerName) {
  return assignedStreams(streamsByName, 'indexer_selections', indexerName)
}

function IndexerDialog({ open, onOpenChange, initialValue, onSave, onClearStatus, title, description, saveLabel, existingNames = [], existingIndexers = [], editing = false }) {
  const [presetTooltipOpen, setPresetTooltipOpen] = useState(false)
  const [presetMenuOpen, setPresetMenuOpen] = useState(false)
  const nameInputRef = useRef(null)
  const dialog = useEntityDialog({
    open,
    onOpenChange,
    initialValue,
    makeDraft: () => normalizeIndexerDraft(initialValue),
    normalize: normalizeIndexerDraft,
    onClearStatus,
  })
  const { draft, setDraft, update, fieldClass, setSaveError, setFieldErrors } = dialog

  const isEasynews = draft.type === 'easynews'
  const hasProwlarrPlaceholder = typeof draft.api_path === 'string' && draft.api_path.includes(PROWLARR_INDEXER_ID_PLACEHOLDER)
  const duplicateName = existingNames.some((name) => normalizeName(name) === normalizeName(draft.name))
  const duplicateIndexer = existingIndexers.find((indexer) => normalizeIndexerIdentity(indexer) === normalizeIndexerIdentity(draft))
  const presetForDraft = INDEXER_PRESETS.find((preset) =>
    preset.url === draft.url && preset.api_path === draft.api_path && preset.type === draft.type
  )
  const selectedPresetName = presetForDraft?.name || ''
  const apiKeyOptional = !isEasynews && presetForDraft?.optional_api_key === true

  // The shared dialog row layout, under this file's historical names.
  const rowClass = dialogRowClass
  const labelClass = dialogLabelClass
  const controlWideClass = dialogControlWideClass
  const controlNameClass = dialogControlNameClass
  const controlNarrowClass = dialogControlNarrowClass

  const handleSave = () => dialog.runSave({
    validate: () => {
      const nextFieldErrors = {}
      if (!draft.name?.trim()) {
        nextFieldErrors.name = 'Indexer name is required'
      }
      if (!isEasynews && !draft.url?.trim()) {
        nextFieldErrors.url = 'URL is required'
      }
      if (!isEasynews && !apiKeyOptional && !draft.api_key?.trim()) {
        nextFieldErrors.api_key = 'API key is required'
      }
      if (isEasynews && !draft.username?.trim()) {
        nextFieldErrors.username = 'Username is required'
      }
      if (isEasynews && !draft.password?.trim()) {
        nextFieldErrors.password = 'Password is required'
      }
      if (duplicateName) {
        nextFieldErrors.name = 'Indexer name already exists'
      }
      if (duplicateIndexer) {
        if (isEasynews) {
          nextFieldErrors.username = `An identical Easynews indexer already exists: "${duplicateIndexer.name}".`
        } else {
          nextFieldErrors.url = `An identical indexer already exists: "${duplicateIndexer.name}".`
          nextFieldErrors.api_key = `An identical indexer already exists: "${duplicateIndexer.name}".`
        }
      }
      return nextFieldErrors
    },
    commit: () => onSave(normalizeIndexerDraft(draft)),
    mapError: (error) => {
      const nextErrors = {}
      Object.entries(error?.fieldErrors || {}).forEach(([path, message]) => {
        if (path.includes('.name')) nextErrors.name = message
        else if (path.includes('.url')) nextErrors.url = message
        else if (path.includes('.api_path')) nextErrors.api_path = message
        else if (path.includes('.api_key')) nextErrors.api_key = message
        else if (path.includes('.username')) nextErrors.username = message
        else if (path.includes('.password')) nextErrors.password = message
        else if (path.includes('.timeout_seconds')) nextErrors.timeout_seconds = message
        else if (path.includes('.api_hits_day')) nextErrors.api_hits_day = message
        else if (path.includes('.downloads_day')) nextErrors.downloads_day = message
        else if (path.includes('.rate_limit_rps')) nextErrors.rate_limit_rps = message
        else if (path.includes('.proxy_url')) nextErrors.proxy_url = message
        else if (path.includes('.search_results_cache_time')) nextErrors.search_results_cache_time = message
      })
      return nextErrors
    },
  })

  return (
    <EntityDialog
      dialog={dialog}
      open={open}
      onOpenChange={onOpenChange}
      title={title}
      description={description}
      saveLabel={saveLabel}
      onSave={handleSave}
      discardDescription="Your unsaved indexer changes will be lost."
    >
          <div className="space-y-4">
            <div className="rounded-md border border-border/60 p-3">
              <div className={rowClass}>
                <div className={labelClass}>
                  <Label className="text-sm font-medium">Name</Label>
                </div>
                <div className={controlNameClass}>
                  <Input ref={nameInputRef} className={`h-9 ${fieldClass('name')}`} value={draft.name} onChange={(event) => update('name', event.target.value)} placeholder="e.g. NzbPlanet" />
                  {!editing && (
                    <TooltipProvider delayDuration={100}>
                      <Tooltip open={presetTooltipOpen && !presetMenuOpen} onOpenChange={setPresetTooltipOpen}>
                        <DropdownMenu onOpenChange={(nextOpen) => {
                          setPresetMenuOpen(nextOpen)
                          if (nextOpen) setPresetTooltipOpen(false)
                        }}>
                          <TooltipTrigger asChild>
                            <DropdownMenuTrigger asChild>
                              <Button type="button" variant={selectedPresetName ? "secondary" : "outline"} size="icon" className="h-9 w-9 shrink-0" aria-label={selectedPresetName ? `Load preset (${selectedPresetName})` : 'Load preset'}>
                                <Download className="h-4 w-4" />
                              </Button>
                            </DropdownMenuTrigger>
                          </TooltipTrigger>
                          <DropdownMenuContent
                            align="end"
                            className="max-h-80 w-56 overflow-y-auto"
                            onCloseAutoFocus={(event) => {
                              event.preventDefault()
                            }}
                          >
                            {INDEXER_PRESETS.map((preset) => (
                              <DropdownMenuItem
                                key={preset.name}
                                onClick={() => {
                                  const presetDefaults = getPresetDefaults(preset)
                                  setPresetTooltipOpen(false)
                                  setSaveError('')
                                  setFieldErrors({})
                                  setDraft((current) => ({
                                    ...current,
                                    name: preset.name,
                                    url: preset.url,
                                    api_path: preset.api_path,
                                    type: preset.type,
                                    timeout_seconds: presetDefaults.timeout_seconds,
                                    api_hits_day: presetDefaults.api_hits_day,
                                    downloads_day: presetDefaults.downloads_day,
                                    rate_limit_rps: presetDefaults.rate_limit_rps,
                                    query_header: presetDefaults.query_header,
                                    grab_header: presetDefaults.grab_header,
                                    content_scope: presetDefaults.content_scope,
                                  }))
                                  requestAnimationFrame(() => {
                                    nameInputRef.current?.focus()
                                    nameInputRef.current?.select?.()
                                  })
                                }}
                              >
                                {preset.name}
                              </DropdownMenuItem>
                            ))}
                          </DropdownMenuContent>
                        </DropdownMenu>
                        <TooltipContent>{selectedPresetName ? `Load preset (${selectedPresetName})` : 'Load preset'}</TooltipContent>
                      </Tooltip>
                    </TooltipProvider>
                  )}
                </div>
              </div>
            </div>

            {!isEasynews && (
              <div className="rounded-md border border-border/60">
                <div className="p-3">
                  <div className={rowClass}>
                    <div className={labelClass}>
                      <Label className="text-sm font-medium">URL</Label>
                    </div>
                    <div className={controlWideClass}>
                      <Input className={`h-9 ${fieldClass('url')}`} value={draft.url} onChange={(event) => update('url', event.target.value)} placeholder="https://api.nzbgeek.info" />
                    </div>
                  </div>
                </div>
                <div className="relative p-3">
                  <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                  <div className={rowClass}>
                    <div className={labelClass}>
                      <Label className="text-sm font-medium">API Path</Label>
                    </div>
                    <div className={controlWideClass}>
                      <Input className={`h-9 ${fieldClass('api_path')}`} value={draft.api_path} onChange={(event) => update('api_path', event.target.value)} placeholder="/api" />
                    </div>
                  </div>
                  {hasProwlarrPlaceholder && (
                    <div className="mt-3 flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-900">
                      <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                      <span>Replace <code>{PROWLARR_INDEXER_ID_PLACEHOLDER}</code> with the real Prowlarr indexer ID, for example <code>1/api</code>.</span>
                    </div>
                  )}
                </div>
                <div className="relative p-3">
                  <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                  <div className={rowClass}>
                    <div className={labelClass}>
                      <Label className="text-sm font-medium">API Key{apiKeyOptional && <span className="font-normal text-muted-foreground"> (optional)</span>}</Label>
                    </div>
                    <div className={controlWideClass}>
                      <Input className={`h-9 ${fieldClass('api_key')}`} type="password" value={draft.api_key} onChange={(event) => update('api_key', event.target.value)} placeholder={apiKeyOptional ? 'Not required for this indexer' : ''} />
                    </div>
                  </div>
                </div>
              </div>
            )}

            {isEasynews && (
              <div className="rounded-md border border-border/60">
                <div className="p-3">
                  <div className={rowClass}>
                    <div className={labelClass}>
                      <Label className="text-sm font-medium">Username</Label>
                    </div>
                    <div className={controlWideClass}>
                      <Input className={`h-9 ${fieldClass('username')}`} value={draft.username} onChange={(event) => update('username', event.target.value)} />
                    </div>
                  </div>
                </div>
                <div className="relative p-3">
                  <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                  <div className={rowClass}>
                    <div className={labelClass}>
                      <Label className="text-sm font-medium">Password</Label>
                    </div>
                    <div className={controlWideClass}>
                      <Input className={`h-9 ${fieldClass('password')}`} type="password" value={draft.password} onChange={(event) => update('password', event.target.value)} />
                    </div>
                  </div>
                </div>
              </div>
            )}

            <div className="rounded-md border border-border/60 p-3">
              <div className={rowClass}>
                <div className={labelClass}>
                  <Label className="text-sm font-medium">Content</Label>
                  <p className="mt-1 text-xs text-muted-foreground">Which searches this indexer participates in. Anime detection uses Kitsu and TMDB metadata.</p>
                </div>
                <div className={controlWideClass}>
                  <select
                    className="flex h-9 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2"
                    value={draft.content_scope || 'all'}
                    onChange={(event) => update('content_scope', event.target.value === 'all' ? '' : event.target.value)}
                  >
                    <option value="all">All content</option>
                    <option value="anime">Anime only</option>
                    <option value="non_anime">Everything except anime</option>
                  </select>
                </div>
              </div>
            </div>

            <div className="rounded-md border border-border/60 p-3">
              <div className={rowClass}>
                <div className={labelClass}>
                  <Label className="text-sm font-medium">HTTP(S) proxy</Label>
                  <p className="mt-1 text-xs text-muted-foreground">Optional proxy override</p>
                </div>
                <div className={controlWideClass}>
                  <Input
                    className={`h-9 ${fieldClass('proxy_url')}`}
                    value={draft.proxy_url}
                    onChange={(event) => update('proxy_url', event.target.value)}
                    placeholder="http://proxy:8888"
                    autoComplete="off"
                  />
                </div>
              </div>
            </div>

            <div className="rounded-md border border-border/60">
              {!isEasynews && (
                <div className="p-3">
                  <div className={rowClass}>
                    <div className={labelClass}>
                      <Label className="text-sm font-medium">Query Header</Label>
                      <p className="mt-1 text-xs text-muted-foreground">Optional Query Header override</p>
                    </div>
                    <div className={controlWideClass}>
                      <Input
                        className="h-9"
                        value={draft.query_header}
                        onChange={(event) => update('query_header', event.target.value)}
                        placeholder="Prowlarr/2.3.0.5236"
                        autoComplete="off"
                      />
                    </div>
                  </div>
                </div>
              )}
              <div className={!isEasynews ? "relative p-3" : "p-3"}>
                {!isEasynews && <div className="absolute left-3 right-3 top-0 border-t border-border/60" />}
                <div className={rowClass}>
                  <div className={labelClass}>
                    <Label className="text-sm font-medium">Grab Header</Label>
                    <p className="mt-1 text-xs text-muted-foreground">Optional Grab Header override</p>
                  </div>
                  <div className={controlWideClass}>
                    <Input
                      className="h-9"
                      value={draft.grab_header}
                      onChange={(event) => update('grab_header', event.target.value)}
                      placeholder="SABnzbd/4.5.5"
                      autoComplete="off"
                    />
                  </div>
                </div>
              </div>
            </div>

            <div className="rounded-md border border-border/60">
              <div className="p-3">
                <div className="space-y-3">
                  <div className={rowClass}>
                    <div className={labelClass}>
                      <Label className="text-sm font-medium">Timeout (seconds)</Label>
                    </div>
                    <div className={controlNarrowClass}>
                      <Input className={`h-9 ${fieldClass('timeout_seconds')}`} type="number" min={0} value={draft.timeout_seconds === 0 ? '' : draft.timeout_seconds} onChange={(event) => update('timeout_seconds', event.target.value === '' ? 0 : Number(event.target.value))} placeholder={String(getDefaultIndexerTimeoutSeconds(draft.type))} />
                    </div>
                  </div>
                  {isEasynews && (
                    <p className="text-sm text-muted-foreground">{EASYNEWS_TIMEOUT_HINT}</p>
                  )}
                </div>
              </div>
              <div className="relative p-3">
                <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                <div className={rowClass}>
                  <div className={labelClass}>
                    <Label className="text-sm font-medium">Requests/Second</Label>
                  </div>
                  <div className={controlNarrowClass}>
                    <Input className={`h-9 ${fieldClass('rate_limit_rps')}`} type="number" min={0} value={draft.rate_limit_rps === 0 ? '' : draft.rate_limit_rps} onChange={(event) => update('rate_limit_rps', event.target.value === '' ? 0 : Number(event.target.value))} placeholder="∞" />
                  </div>
                </div>
              </div>
              {draft.type === 'aggregator' && (
                <div className="relative p-3">
                  <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                  <div className={rowClass}>
                    <div className={labelClass}>
                      <Label className="text-sm font-medium">Search Results Cache Time (minutes)</Label>
                    </div>
                    <div className={controlNarrowClass}>
                      <Input
                        className={`h-9 ${fieldClass('search_results_cache_time')}`}
                        type="number"
                        min={0}
                        value={draft.search_results_cache_time === 0 ? '' : draft.search_results_cache_time}
                        onChange={(event) => update('search_results_cache_time', event.target.value === '' ? 0 : Number(event.target.value))}
                        placeholder="disabled"
                      />
                    </div>
                  </div>
                  <p className="mt-2 text-xs text-muted-foreground">
                    NZBHydra2: adds <code>cachetime</code> to search API calls.
                  </p>
                </div>
              )}
              <div className="relative p-3">
                <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                <div className={rowClass}>
                  <div className={labelClass}>
                    <Label className="text-sm font-medium">Hits/Day</Label>
                  </div>
                  <div className={controlNarrowClass}>
                    <Input className={`h-9 ${fieldClass('api_hits_day')}`} type="number" min={0} value={draft.api_hits_day === 0 ? '' : draft.api_hits_day} onChange={(event) => update('api_hits_day', event.target.value === '' ? 0 : Number(event.target.value))} placeholder="∞" />
                  </div>
                </div>
              </div>
              <div className="relative p-3">
                <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                <div className={rowClass}>
                  <div className={labelClass}>
                    <Label className="text-sm font-medium">DLs/Day</Label>
                  </div>
                  <div className={controlNarrowClass}>
                    <Input className={`h-9 ${fieldClass('downloads_day')}`} type="number" min={0} value={draft.downloads_day === 0 ? '' : draft.downloads_day} onChange={(event) => update('downloads_day', event.target.value === '' ? 0 : Number(event.target.value))} placeholder="∞" />
                  </div>
                </div>
              </div>
            </div>
          </div>
    </EntityDialog>
  )
}

export function IndexerSettings({ fields = [], append, update, replace, defaultProxyURL = '', onPersist, onClearStatus, onStatus, componentHealth, streamsByName = {} }) {
  const indexers = fields
  const [editingIndex, setEditingIndex] = useState(null)
  const [showAddDialog, setShowAddDialog] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState(null)
  const [deleteBlockedName, setDeleteBlockedName] = useState('')
  const healthByName = useMemo(() => indexHealth(componentHealth), [componentHealth])

  useEffect(() => () => {
    onClearStatus?.()
  }, [onClearStatus])

  const handleCreate = async (draft) => {
    const nextIndexers = [...indexers.map((indexer) => normalizeIndexerDraft(indexer)), normalizeIndexerDraft(draft)]
    await onPersist?.(nextIndexers)
    append(normalizeIndexerDraft(draft))
    setDeleteBlockedName('')
    onStatus?.({ type: 'success', message: `Indexer "${draft.name || draft.url}" created successfully.${CACHE_CLEARED_SUFFIX}` })
  }

  const handleSave = async (index, draft) => {
    const nextIndexers = [...indexers]
    nextIndexers[index] = normalizeIndexerDraft(draft)
    await onPersist?.(nextIndexers)
    update(index, normalizeIndexerDraft(draft))
    setDeleteBlockedName('')
    onStatus?.({ type: 'success', message: `Indexer "${draft.name || draft.url}" saved successfully.${CACHE_CLEARED_SUFFIX}` })
  }

  const onRequestDelete = async (index) => {
    const indexer = indexers[index]
    if (!indexer) return
    let assignedStreams = []
    try {
      const liveStreams = await apiFetch('/api/streams')
      assignedStreams = assignedStreamsForIndexer(mapStreamsByUsername(liveStreams), indexer.name)
    } catch {
      assignedStreams = assignedStreamsForIndexer(streamsByName, indexer.name)
    }
    setDeleteTarget({ index, name: indexer.name || indexer.url || '', assignedStreams })
  }

  const handleDelete = async (index) => {
    const indexer = indexers[index]
    if (!indexer) return
    setDeleteBlockedName('')
    const nextIndexers = indexers.filter((_, currentIndex) => currentIndex !== index).map((item) => normalizeIndexerDraft(item))
    try {
      await onPersist?.(nextIndexers)
      replace(nextIndexers)
      onStatus?.({ type: 'success', message: `Indexer "${indexer.name || indexer.url}" deleted successfully.${CACHE_CLEARED_SUFFIX}` })
    } catch (error) {
      onStatus?.({
        type: 'error',
        message: error?.message || `Failed to delete indexer "${indexer.name || indexer.url}".`,
      })
    }
  }

  const handleToggleEnabled = async (index, enabled) => {
    const current = indexers[index]
    if (!current) return
    const nextIndexers = [...indexers]
    nextIndexers[index] = {
      ...normalizeIndexerDraft(current),
      enabled,
    }
    await onPersist?.(nextIndexers)
    replace(nextIndexers.map((indexer) => normalizeIndexerDraft(indexer)))
    setDeleteBlockedName('')
    onStatus?.({ type: 'success', message: `Indexer "${current.name || current.url}" ${enabled ? 'enabled' : 'disabled'} successfully` })
  }

  return (
    <TooltipProvider delayDuration={100}>
      <div className="space-y-4">
        <Card>
          <CardHeader>
            <div className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-3">
              <div className="min-w-0 space-y-0.5">
                <CardTitle>Indexers</CardTitle>
                <CardDescription className="break-words">Configure your search sources.</CardDescription>
              </div>
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button type="button" variant="destructive" size="icon" className="h-9 w-9 shrink-0" onClick={() => setShowAddDialog(true)} aria-label="Add indexer">
                    <Plus className="h-4 w-4" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>Add Indexer</TooltipContent>
              </Tooltip>
            </div>
          </CardHeader>
          <CardContent className="space-y-3">
            {indexers.length === 0 ? (
              <p className="text-sm text-muted-foreground">No indexers configured yet.</p>
            ) : (
              indexers.map((indexer, index) => {
                const normalized = normalizeIndexerDraft(indexer)
                const summary = summarizeIndexer(normalized, defaultProxyURL)
                const nameKey = (normalized.name || '').trim()
                const healthRecord = healthFor(healthByName, 'indexer', nameKey)
                // The dot shows exactly two synchronous facts: the switch next
                // to it (disabled = gray, instantly) and the health verdict
                // (red blocked / amber degraded / green). It deliberately does
                // NOT consult the stats snapshot: presence there lags the
                // enabled toggle through a debounced save, an async reload and
                // a stats tick, which is how the dot and the switch used to
                // disagree for seconds at a time.
                const isDisabled = normalized.enabled === false
                const dotClass = isDisabled ? 'bg-muted-foreground/40'
                  : isBlocked(healthRecord) ? 'bg-red-500'
                    : healthRecord ? 'bg-amber-500'
                      : 'bg-green-500'
                const dotTitle = isDisabled ? 'Disabled'
                  : healthRecord ? healthReasonLabel(healthRecord.reason)
                    : 'Active'
                return (
                  <Card
                    key={`${normalized.name || normalized.url || 'indexer'}-${index}`}
                    className={deleteBlockedName && deleteBlockedName === (normalized.name || normalized.url || '') ? 'border-destructive/60 ring-1 ring-destructive/30' : ''}
                  >
                    <CardContent className="pt-6">
                      <div className="min-w-0 flex-1 space-y-3">
                        <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
                          <div className="flex items-center gap-2 self-end sm:order-2">
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <div className="inline-flex h-9 w-20 items-center justify-center rounded-md border border-border/60 bg-muted/30 px-2">
                                  <Switch
                                    checked={normalized.enabled !== false}
                                    onCheckedChange={(checked) => handleToggleEnabled(index, checked === true)}
                                    aria-label={normalized.enabled !== false ? `Disable indexer ${normalized.name || normalized.url || index + 1}` : `Enable indexer ${normalized.name || normalized.url || index + 1}`}
                                    className="h-6 w-12 data-[state=checked]:bg-green-500 data-[state=unchecked]:bg-muted-foreground/30"
                                    thumbClassName="flex h-5 w-5 items-center justify-center data-[state=checked]:translate-x-6 data-[state=unchecked]:translate-x-0"
                                  />
                                </div>
                              </TooltipTrigger>
                              <TooltipContent>{normalized.enabled !== false ? 'Disable indexer' : 'Enable indexer'}</TooltipContent>
                            </Tooltip>
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <Button type="button" variant="outline" size="icon" className="h-9 w-9" aria-label={`Edit indexer ${normalized.name || normalized.url || index + 1}`} onClick={() => {
                                  setDeleteBlockedName('')
                                  onClearStatus?.()
                                  setEditingIndex(index)
                                }}>
                                  <Settings className="h-4 w-4" />
                                </Button>
                              </TooltipTrigger>
                              <TooltipContent>Edit indexer</TooltipContent>
                            </Tooltip>
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <Button type="button" variant="destructive" size="icon" className="h-9 w-9" aria-label={`Delete indexer ${normalized.name || normalized.url || index + 1}`} onClick={() => void onRequestDelete(index)}>
                                  <Trash2 className="h-4 w-4" />
                                </Button>
                              </TooltipTrigger>
                              <TooltipContent>Delete indexer</TooltipContent>
                            </Tooltip>
                          </div>
                          <div className="min-w-0 font-semibold sm:order-1">{normalized.name || normalized.url || `Indexer ${index + 1}`}</div>
                        </div>
                        <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3">
                          <div className="flex flex-wrap gap-2 text-xs text-muted-foreground min-w-0">
                            <span className="rounded-full border border-border px-2 py-1" title={dotTitle}>
                              Status:{' '}
                              <span className={`inline-block h-2 w-2 rounded-full ${dotClass}`} />
                            </span>
                            <ComponentHealthBadge record={healthRecord} />
                            {summary.map((part) => (
                              <span key={part} className="rounded-full border border-border px-2 py-1">{part}</span>
                            ))}
                          </div>
                        </div>
                        <UsageChips labels={assignedStreamsForIndexer(streamsByName, normalized.name)} />
                      </div>
                    </CardContent>

                    <IndexerDialog
                      open={editingIndex === index}
                      onOpenChange={(nextOpen) => {
                        if (!nextOpen) {
                          setDeleteBlockedName('')
                        }
                        setEditingIndex(nextOpen ? index : null)
                      }}
                      initialValue={normalized}
                      existingNames={indexers.filter((_, currentIndex) => currentIndex !== index).map((indexer) => indexer?.name || '')}
                      existingIndexers={indexers.filter((_, currentIndex) => currentIndex !== index)}
                      onSave={(draft) => handleSave(index, draft)}
                      onClearStatus={onClearStatus}
                      title={normalized.name || normalized.url || 'Edit Indexer'}
                      description="Edit indexer settings."
                      saveLabel="Save"
                      editing
                    />
                  </Card>
                )
              })
            )}
          </CardContent>
        </Card>

        <IndexerDialog
          open={showAddDialog}
          onOpenChange={(nextOpen) => {
            setShowAddDialog(nextOpen)
          }}
          initialValue={emptyIndexerDraft()}
          existingNames={indexers.map((indexer) => indexer?.name || '')}
          existingIndexers={indexers}
          onSave={handleCreate}
          onClearStatus={onClearStatus}
          title="Add Indexer"
          description="Add a new indexer."
          saveLabel="Save"
          editing={false}
        />
        <ConfirmDialog
          open={Boolean(deleteTarget)}
          onOpenChange={(nextOpen) => {
            if (!nextOpen) setDeleteTarget(null)
          }}
          title="Delete indexer?"
          description={
            deleteTarget
              ? deleteTarget.assignedStreams?.length > 0
                ? `Indexer "${deleteTarget.name}" is currently used by stream(s): ${deleteTarget.assignedStreams.join(', ')}. Are you sure you want to delete it? It will also be removed from the configured streams.`
                : `Are you sure you want to delete indexer "${deleteTarget.name}"?`
              : ''
          }
          confirmLabel="Delete"
          onConfirm={() => {
            const target = deleteTarget
            setDeleteTarget(null)
            if (target) {
              void handleDelete(target.index)
            }
          }}
        />
      </div>
    </TooltipProvider>
  )
}
