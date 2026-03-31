import React, { useEffect, useMemo, useRef, useState } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { apiFetch } from "@/api"
import { AlertTriangle, Download, Plus, Settings, Trash2 } from "lucide-react"

function normalizeName(value) {
  return (value || '').trim().toLowerCase()
}

function normalizeIndexerIdentity(draft) {
  const next = normalizeIndexerDraft(draft)
  if (next.type === 'easynews') {
    return `easynews::${normalizeName(next.username)}`
  }
  return `indexer::${normalizeName(next.type)}::${normalizeName(next.url)}::${normalizeName(next.api_path)}::${normalizeName(next.api_key)}`
}

const INDEXER_PRESETS = [
  { name: 'NZBHydra2', url: 'http://localhost:5076', api_path: '/api', type: 'aggregator', api_hits_day: 0, downloads_day: 0 },
  { name: 'Prowlarr', url: 'http://localhost:9696', api_path: '{indexer_id}/api', type: 'aggregator', api_hits_day: 0, downloads_day: 0 },
  { name: 'abNZB', url: 'https://abnzb.com', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'altHUB', url: 'https://api.althub.co.za', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'AnimeTosho (Usenet)', url: 'https://feed.animetosho.org', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'DOGnzb', url: 'https://api.dognzb.cr', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'DrunkenSlug', url: 'https://drunkenslug.com', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'GingaDADDY', url: 'https://www.gingadaddy.com', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'Miatrix', url: 'https://www.miatrix.com', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'Newz69', url: 'https://newz69.keagaming.com', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'NinjaCentral', url: 'https://ninjacentral.co.za', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'Nzb.life', url: 'https://api.nzb.life', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'NZBCat', url: 'https://nzb.cat', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'NZBFinder', url: 'https://nzbfinder.ws', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'NZBgeek', url: 'https://api.nzbgeek.info', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'NzbNoob', url: 'https://www.nzbnoob.com', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'NZBNDX', url: 'https://www.nzbndx.com', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'NzbPlanet', url: 'https://api.nzbplanet.net', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'NZBStars', url: 'https://nzbstars.com', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'SceneNZBs', url: 'https://scenenzbs.com', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'Tabula Rasa', url: 'https://www.tabula-rasa.pw', api_path: '/api/v1', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'Usenet Crawler', url: 'https://www.usenet-crawler.com', api_path: '/api', type: 'newznab', api_hits_day: 100, downloads_day: 50 },
  { name: 'Easynews', url: '', api_path: '/api', type: 'easynews', api_hits_day: 100, downloads_day: 50 },
]

const PROWLARR_INDEXER_ID_PLACEHOLDER = '{indexer_id}'

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
    timeout_seconds: Number(value.timeout_seconds || 0),
    enabled: value.enabled !== false,
    username: value.username || '',
    password: value.password || '',
  }
}

function emptyIndexerDraft() {
  return normalizeIndexerDraft({})
}

function getDefaultIndexerTimeoutSeconds(type) {
  return type === 'aggregator' ? 10 : 5
}

function getPresetDefaults(preset) {
  return {
    timeout_seconds: getDefaultIndexerTimeoutSeconds(preset.type),
    api_hits_day: Number(preset.api_hits_day || 0),
    downloads_day: Number(preset.downloads_day || 0),
  }
}

function summarizeIndexer(indexer, caps) {
  const parts = []
  parts.push(indexer.type === 'aggregator' ? 'Aggregator' : indexer.type === 'easynews' ? 'Easynews' : 'Newznab')
  if (indexer.url) parts.push(indexer.url)
  if (indexer.timeout_seconds > 0) parts.push(`Timeout: ${indexer.timeout_seconds}s`)
  else parts.push(`Timeout: ${getDefaultIndexerTimeoutSeconds(indexer.type)}s default`)
  if (indexer.api_hits_day > 0) parts.push(`Hits/day: ${indexer.api_hits_day}`)
  if (indexer.downloads_day > 0) parts.push(`DLs/day: ${indexer.downloads_day}`)
  return parts
}

function mapDevicesByUsername(devices) {
  return (Array.isArray(devices) ? devices : []).reduce((acc, device) => {
    if (!device?.username) return acc
    acc[device.username] = device
    return acc
  }, {})
}

function assignedStreamsForIndexer(devicesByName, indexerName) {
  const target = normalizeName(indexerName)
  if (!target || !devicesByName) return []
  return Object.values(devicesByName)
    .filter(Boolean)
    .filter((device) => Array.isArray(device.indexer_selections) && device.indexer_selections.some((name) => normalizeName(name) === target))
    .map((device) => device.username)
}

function IndexerDialog({ open, onOpenChange, initialValue, onSave, onClearStatus, title, description, saveLabel, existingNames = [], existingIndexers = [], editing = false }) {
  const [draft, setDraft] = useState(() => normalizeIndexerDraft(initialValue))
  const [wasOpen, setWasOpen] = useState(open)
  const [saveError, setSaveError] = useState('')
  const [fieldErrors, setFieldErrors] = useState({})
  const [saving, setSaving] = useState(false)
  const [showDiscardConfirm, setShowDiscardConfirm] = useState(false)
  const [presetTooltipOpen, setPresetTooltipOpen] = useState(false)
  const [presetMenuOpen, setPresetMenuOpen] = useState(false)
  const nameInputRef = useRef(null)

  useEffect(() => {
    if (open && !wasOpen) {
      setDraft(normalizeIndexerDraft(initialValue))
      setSaveError('')
      setFieldErrors({})
    }
    setWasOpen(open)
  }, [open, wasOpen, initialValue])

  const normalizedInitial = JSON.stringify(normalizeIndexerDraft(initialValue))
  const normalizedCurrent = JSON.stringify(normalizeIndexerDraft(draft))
  const isDirty = normalizedInitial !== normalizedCurrent
  const isEasynews = draft.type === 'easynews'
  const hasProwlarrPlaceholder = typeof draft.api_path === 'string' && draft.api_path.includes(PROWLARR_INDEXER_ID_PLACEHOLDER)
  const duplicateName = existingNames.some((name) => normalizeName(name) === normalizeName(draft.name))
  const duplicateIndexer = existingIndexers.find((indexer) => normalizeIndexerIdentity(indexer) === normalizeIndexerIdentity(draft))
  const selectedPresetName = INDEXER_PRESETS.find((preset) =>
    preset.url === draft.url && preset.api_path === draft.api_path && preset.type === draft.type
  )?.name || ''

  const requestClose = () => {
    if (saving) return
    if (isDirty) {
      setShowDiscardConfirm(true)
      return
    }
    onClearStatus?.()
    onOpenChange(false)
  }

  const update = (key, value) => setDraft((current) => ({ ...current, [key]: value }))
  const fieldClass = (key) => fieldErrors[key] ? "border-destructive focus-visible:ring-destructive" : ""

  const handleSave = async () => {
    const nextFieldErrors = {}
    if (!draft.name?.trim()) {
      nextFieldErrors.name = 'Indexer name is required'
    }
    if (!isEasynews && !draft.url?.trim()) {
      nextFieldErrors.url = 'URL is required'
    }
    if (!isEasynews && !draft.api_key?.trim()) {
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
    if (Object.keys(nextFieldErrors).length > 0) {
      setFieldErrors(nextFieldErrors)
      setSaveError(
        nextFieldErrors.name ||
        nextFieldErrors.url ||
        nextFieldErrors.api_key ||
        nextFieldErrors.username ||
        nextFieldErrors.password ||
        'Please review the highlighted fields.'
      )
      return
    }
    setSaving(true)
    setSaveError('')
    setFieldErrors({})
    try {
      await onSave(normalizeIndexerDraft(draft))
      onOpenChange(false)
    } catch (error) {
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
      })
      setFieldErrors(nextErrors)
      setSaveError(error?.message || 'Save failed')
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
      <DialogContent className="flex max-h-[85vh] max-w-3xl flex-col overflow-hidden">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>

        <div className="min-h-0 flex-1 overflow-y-auto pr-1">
        <div className="space-y-4">
          <div className="rounded-md border border-border/60 p-3">
            <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
              <div className="min-w-0 xl:flex-1">
                <Label className="text-sm font-medium">Indexer Name</Label>
              </div>
              <div className="flex w-full items-center gap-2 xl:max-w-sm">
                <Input ref={nameInputRef} className={`h-9 ${fieldClass('name')}`} value={draft.name} onChange={(event) => update('name', event.target.value)} placeholder="e.g. NzbPlanet" disabled={editing} />
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
                <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                  <div className="min-w-0 xl:flex-1">
                    <Label className="text-sm font-medium">URL</Label>
                  </div>
                  <div className="w-full xl:max-w-sm">
                    <Input className={`h-9 ${fieldClass('url')}`} value={draft.url} onChange={(event) => update('url', event.target.value)} placeholder="https://api.nzbgeek.info" />
                  </div>
                </div>
              </div>
              <div className="relative p-3">
                <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                  <div className="min-w-0 xl:flex-1">
                    <Label className="text-sm font-medium">API Path</Label>
                  </div>
                  <div className="w-full xl:max-w-sm">
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
                <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                  <div className="min-w-0 xl:flex-1">
                    <Label className="text-sm font-medium">API Key</Label>
                  </div>
                  <div className="w-full xl:max-w-sm">
                    <Input className={`h-9 ${fieldClass('api_key')}`} type="password" value={draft.api_key} onChange={(event) => update('api_key', event.target.value)} />
                  </div>
                </div>
              </div>
            </div>
          )}

          {isEasynews && (
            <div className="rounded-md border border-border/60">
              <div className="p-3">
                <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                  <div className="min-w-0 xl:flex-1">
                    <Label className="text-sm font-medium">Username</Label>
                  </div>
                  <div className="w-full xl:max-w-sm">
                    <Input className={`h-9 ${fieldClass('username')}`} value={draft.username} onChange={(event) => update('username', event.target.value)} />
                  </div>
                </div>
              </div>
              <div className="relative p-3">
                <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                  <div className="min-w-0 xl:flex-1">
                    <Label className="text-sm font-medium">Password</Label>
                  </div>
                  <div className="w-full xl:max-w-sm">
                    <Input className={`h-9 ${fieldClass('password')}`} type="password" value={draft.password} onChange={(event) => update('password', event.target.value)} />
                  </div>
                </div>
              </div>
            </div>
          )}

          <div className="rounded-md border border-border/60">
            <div className="p-3">
              <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                <div className="min-w-0 xl:flex-1">
                  <Label className="text-sm font-medium">Timeout (seconds)</Label>
                </div>
                <div className="w-full xl:max-w-[8rem]">
                  <Input className={`h-9 ${fieldClass('timeout_seconds')}`} type="number" min={0} value={draft.timeout_seconds === 0 ? '' : draft.timeout_seconds} onChange={(event) => update('timeout_seconds', event.target.value === '' ? 0 : Number(event.target.value))} placeholder={String(getDefaultIndexerTimeoutSeconds(draft.type))} />
                </div>
              </div>
            </div>
            <div className="relative p-3">
              <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
              <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                <div className="min-w-0 xl:flex-1">
                  <Label className="text-sm font-medium">Hits/Day</Label>
                </div>
                <div className="w-full xl:max-w-[8rem]">
                  <Input className={`h-9 ${fieldClass('api_hits_day')}`} type="number" min={0} value={draft.api_hits_day === 0 ? '' : draft.api_hits_day} onChange={(event) => update('api_hits_day', event.target.value === '' ? 0 : Number(event.target.value))} placeholder="100" />
                </div>
              </div>
            </div>
            <div className="relative p-3">
              <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
              <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                <div className="min-w-0 xl:flex-1">
                  <Label className="text-sm font-medium">DLs/Day</Label>
                </div>
                <div className="w-full xl:max-w-[8rem]">
                  <Input className={`h-9 ${fieldClass('downloads_day')}`} type="number" min={0} value={draft.downloads_day === 0 ? '' : draft.downloads_day} onChange={(event) => update('downloads_day', event.target.value === '' ? 0 : Number(event.target.value))} placeholder="50" />
                </div>
              </div>
            </div>
          </div>
        </div>
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
            {saveLabel}
          </Button>
          </div>
        </DialogFooter>
      </DialogContent>
      <ConfirmDialog
        open={showDiscardConfirm}
        onOpenChange={setShowDiscardConfirm}
        title="Discard changes?"
        description="Your unsaved indexer changes will be lost."
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

export function IndexerSettings({ fields = [], append, update, remove, replace, indexerCaps = {}, onPersist, onClearStatus, onStatus, stats, devicesByName = {} }) {
  const indexers = fields
  const [editingIndex, setEditingIndex] = useState(null)
  const [showAddDialog, setShowAddDialog] = useState(false)
  const [knownOnlineIndexers, setKnownOnlineIndexers] = useState({})
  const [deleteTarget, setDeleteTarget] = useState(null)
  const [deleteBlockedName, setDeleteBlockedName] = useState('')
  const indexerStatusByName = useMemo(() => {
    const map = new Map()
    ;(stats?.indexers || []).forEach((indexer) => {
      map.set((indexer.name || '').trim(), true)
    })
    return map
  }, [stats])

  useEffect(() => {
    if (!stats?.indexers?.length) return
    setKnownOnlineIndexers((current) => {
      const next = { ...current }
      stats.indexers.forEach((indexer) => {
        const name = (indexer.name || '').trim()
        if (name) next[name] = true
      })
      return next
    })
  }, [stats])

  useEffect(() => () => {
    onClearStatus?.()
  }, [onClearStatus])

  const replaceIndexers = (nextIndexers) => {
    replace(nextIndexers.map((indexer) => normalizeIndexerDraft(indexer)))
  }

  const handleCreate = async (draft) => {
    const nextIndexers = [...indexers.map((indexer) => normalizeIndexerDraft(indexer)), normalizeIndexerDraft(draft)]
    await onPersist?.(nextIndexers)
    append(normalizeIndexerDraft(draft))
    setDeleteBlockedName('')
    onStatus?.({ type: 'success', message: `Indexer "${draft.name || draft.url}" created successfully` })
  }

  const handleSave = async (index, draft) => {
    const nextIndexers = [...indexers]
    nextIndexers[index] = normalizeIndexerDraft(draft)
    await onPersist?.(nextIndexers)
    update(index, normalizeIndexerDraft(draft))
    setDeleteBlockedName('')
    onStatus?.({ type: 'success', message: `Indexer "${draft.name || draft.url}" saved successfully` })
  }

  const handleDelete = async (index) => {
    const indexer = indexers[index]
    if (!indexer) return
    let assignedStreams = []
    try {
      const liveDevices = await apiFetch('/api/streams')
      assignedStreams = assignedStreamsForIndexer(mapDevicesByUsername(liveDevices), indexer.name)
    } catch {
      assignedStreams = assignedStreamsForIndexer(devicesByName, indexer.name)
    }
    if (assignedStreams.length > 0) {
      setDeleteBlockedName(indexer.name || indexer.url || '')
      onStatus?.({
        type: 'error',
        message: `Indexer "${indexer.name || indexer.url}" cannot be deleted while assigned to stream(s): ${assignedStreams.join(', ')}`
      })
      return
    }
    setDeleteBlockedName('')
    const nextIndexers = indexers.filter((_, currentIndex) => currentIndex !== index).map((item) => normalizeIndexerDraft(item))
    try {
      await onPersist?.(nextIndexers)
      replace(nextIndexers)
      onStatus?.({ type: 'success', message: `Indexer "${indexer.name || indexer.url}" deleted successfully` })
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
            <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
              <div className="space-y-0.5">
                <CardTitle>Indexers</CardTitle>
                <CardDescription>Configure your search sources.</CardDescription>
              </div>
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button type="button" variant="destructive" size="icon" className="h-9 w-9" onClick={() => setShowAddDialog(true)} aria-label="Add indexer">
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
                const summary = summarizeIndexer(normalized, indexerCaps?.[normalized.name])
                const nameKey = (normalized.name || '').trim()
                const isOnline = indexerStatusByName.has(nameKey) || (normalized.enabled === false && knownOnlineIndexers[nameKey] === true)
                return (
                  <Card
                    key={`${normalized.name || normalized.url || 'indexer'}-${index}`}
                    className={deleteBlockedName && deleteBlockedName === (normalized.name || normalized.url || '') ? 'border-destructive/60 ring-1 ring-destructive/30' : ''}
                  >
                    <CardContent className="pt-6">
                      <div className="min-w-0 flex-1 space-y-3">
                          <div className="flex items-start justify-between gap-3">
                            <div className="min-w-0 font-semibold">{normalized.name || normalized.url || `Indexer ${index + 1}`}</div>
                            <div className="flex items-center gap-2">
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
                                  <Button type="button" variant="destructive" size="icon" className="h-9 w-9" aria-label={`Delete indexer ${normalized.name || normalized.url || index + 1}`} onClick={() => setDeleteTarget({ index, name: normalized.name || normalized.url })}>
                                    <Trash2 className="h-4 w-4" />
                                  </Button>
                                </TooltipTrigger>
                                <TooltipContent>Delete indexer</TooltipContent>
                              </Tooltip>
                            </div>
                          </div>
                          <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3">
                            <div className="flex flex-wrap gap-2 text-xs text-muted-foreground min-w-0">
                              <span className="rounded-full border border-border px-2 py-1">
                                Status:{' '}
                                <span className={`inline-block h-2 w-2 rounded-full ${isOnline ? 'bg-green-500' : 'bg-red-500'}`} />
                              </span>
                              {summary.map((part) => (
                                <span key={part} className="rounded-full border border-border px-2 py-1">{part}</span>
                              ))}
                            </div>
                          </div>
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
          description={deleteTarget ? `Are you sure you want to delete indexer "${deleteTarget.name}"?` : ''}
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
