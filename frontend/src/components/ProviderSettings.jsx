import React, { useEffect, useMemo, useRef, useState } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, focusDialogCloseButton } from "@/components/ui/dialog"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { apiFetch } from "@/api"
import { Download, ExternalLink, Plus, Settings, Trash2 } from "lucide-react"

const PROVIDER_PRESETS = [
  {
    name: 'Easynews',
    host: 'news.easynews.com',
    port: 563,
    use_ssl: true,
    connections: 20,
    info_url: 'https://help.easynews.com/kb/article/11-nntp-server-addresses/',
  },
  {
    name: 'Newshosting',
    host: 'news.newshosting.com',
    port: 563,
    use_ssl: true,
    connections: 20,
    info_url: 'https://support.newshosting.com/kb/article/104-newshosting-nntp-server-information/',
  },
  {
    name: 'Tweaknews',
    host: 'news.tweaknews.eu',
    port: 563,
    use_ssl: true,
    connections: 20,
    info_url: 'https://support.tweaknews.eu/kb/article/1752-general-configuration-guide-for-tweaknews-access/',
  },
  {
    name: 'Eweka',
    host: 'news.eweka.nl',
    port: 563,
    use_ssl: true,
    connections: 20,
    info_url: 'https://help.eweka.nl/home/',
  },
  {
    name: 'Giganews',
    host: 'news.giganews.com',
    port: 563,
    use_ssl: true,
    connections: 20,
    info_url: 'https://support.giganews.com/hc/en-us/articles/360039281452-Which-newsreaders-does-Giganews-support',
  },
  {
    name: 'PureUsenet',
    host: 'news.pureusenet.nl',
    port: 563,
    use_ssl: true,
    connections: 8,
    info_url: 'https://support.pureusenet.nl/kb/article/390-where-can-i-find-the-server-details/',
  },
  {
    name: 'SunnyUsenet',
    host: 'NEWS.SUNNYUSENET.COM',
    port: 563,
    use_ssl: true,
    connections: 10,
    info_url: 'https://support.sunnyusenet.com/kb/article/408-where-do-i-find-my-sunny-usenet-server-details/',
  },
  {
    name: 'NewsDemon',
    host: 'news.newsdemon.com',
    port: 563,
    use_ssl: true,
    connections: 20,
    info_url: 'https://www.newsdemon.com/help#server-settings',
  },
  {
    name: 'ThunderNews',
    host: 'news.thundernews.com',
    port: 563,
    use_ssl: true,
    connections: 10,
    info_url: 'https://www.thundernews.com/faq/',
  },
  {
    name: 'UsenetServer',
    host: 'news.usenetserver.com',
    port: 563,
    use_ssl: true,
    connections: 15,
    info_url: 'https://support.usenetserver.com/kb/article/298-server-and-ports/',
  },
  {
    name: 'theCubeNet',
    host: 'news.thecubenet.com',
    port: 563,
    use_ssl: true,
    connections: 10,
    info_url: 'http://www.thecubenet.com/clients/knowledgebase/1/What-are-the-server-addresses-and-ports.html',
  },
  {
    name: 'NewsgroupNinja',
    host: 'news.newsgroup.ninja',
    port: 563,
    use_ssl: true,
    connections: 20,
    info_url: 'https://support.newsgroup.ninja/kb/article/515-other-newsreaders/',
  },
]

const CACHE_CLEARED_SUFFIX = ' Search cache cleared.'

function normalizeName(value) {
  return (value || '').trim().toLowerCase()
}

function normalizeProviderIdentity(draft) {
  const next = normalizeProviderDraft(draft)
  return `provider::${normalizeName(next.host)}::${normalizeName(next.username)}`
}

function normalizeProviderDraft(draft) {
  const value = draft || {}
  return {
    name: (value.name || '').trim(),
    host: (value.host || '').trim(),
    port: Number(value.port || 563),
    username: value.username || '',
    password: value.password || '',
    connections: Number(value.connections || 30),
    use_ssl: value.use_ssl !== false,
    priority: Number(value.priority || 1),
    enabled: value.enabled !== false,
  }
}

function emptyProviderDraft() {
  return normalizeProviderDraft({})
}

function summarizeProvider(provider) {
  const parts = []
  if (provider.host) parts.push(`${provider.host}:${provider.port || 563}`)
  parts.push(provider.use_ssl !== false ? 'SSL' : 'No SSL')
  parts.push(`Connections: ${provider.connections || 30}`)
  return parts
}

function firstFieldErrorMessage(fieldErrors, fallback) {
  const first = Object.values(fieldErrors || {}).find(Boolean)
  return first || fallback
}

function mapStreamsByUsername(streams) {
  return (Array.isArray(streams) ? streams : []).reduce((acc, stream) => {
    if (!stream?.username) return acc
    acc[stream.username] = stream
    return acc
  }, {})
}

function assignedStreamsForProvider(streamsByName, providerName) {
  const target = normalizeName(providerName)
  if (!target || !streamsByName) return []
  return Object.values(streamsByName)
    .filter(Boolean)
    .filter((stream) => Array.isArray(stream.provider_selections) && stream.provider_selections.some((name) => normalizeName(name) === target))
    .map((stream) => stream.username)
}

function ProviderDialog({ open, onOpenChange, initialValue, onSave, onClearStatus, title, description, saveLabel, existingNames = [], existingProviders = [], editing = false }) {
  const [draft, setDraft] = useState(() => normalizeProviderDraft(initialValue))
  const [wasOpen, setWasOpen] = useState(open)
  const [saveError, setSaveError] = useState('')
  const [fieldErrors, setFieldErrors] = useState({})
  const [saving, setSaving] = useState(false)
  const [showDiscardConfirm, setShowDiscardConfirm] = useState(false)
  const nameInputRef = useRef(null)

  useEffect(() => {
    if (open && !wasOpen) {
      setDraft(normalizeProviderDraft(initialValue))
      setSaveError('')
      setFieldErrors({})
    }
    setWasOpen(open)
  }, [open, wasOpen, initialValue])

  const normalizedInitial = JSON.stringify(normalizeProviderDraft(initialValue))
  const normalizedCurrent = JSON.stringify(normalizeProviderDraft(draft))
  const isDirty = normalizedInitial !== normalizedCurrent
  const duplicateName = existingNames.some((name) => normalizeName(name) === normalizeName(draft.name))
  const duplicateProvider = existingProviders.find((provider) => normalizeProviderIdentity(provider) === normalizeProviderIdentity(draft))
  const selectedPreset = PROVIDER_PRESETS.find((preset) => normalizeName(preset.host) === normalizeName(draft.host))

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
      nextFieldErrors.name = 'Provider name is required'
    }
    if (!draft.host?.trim()) {
      nextFieldErrors.host = 'Host is required'
    }
    if (!draft.username?.trim()) {
      nextFieldErrors.username = 'Username is required'
    }
    if (!draft.password?.trim()) {
      nextFieldErrors.password = 'Password is required'
    }
    if (duplicateName) {
      nextFieldErrors.name = 'Provider name already exists'
    }
    if (duplicateProvider) {
      nextFieldErrors.host = `An identical provider already exists: "${duplicateProvider.name}".`
      nextFieldErrors.username = `An identical provider already exists: "${duplicateProvider.name}".`
    }
    if (Object.keys(nextFieldErrors).length > 0) {
      setFieldErrors(nextFieldErrors)
      setSaveError(
        nextFieldErrors.name ||
        nextFieldErrors.host ||
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
      await onSave(normalizeProviderDraft(draft))
      onOpenChange(false)
    } catch (error) {
      const nextErrors = {}
      Object.entries(error?.fieldErrors || {}).forEach(([path, message]) => {
        if (path.includes('.name')) nextErrors.name = message
        else if (path.includes('.host')) nextErrors.host = message
        else if (path.includes('.username')) nextErrors.username = message
        else if (path.includes('.password')) nextErrors.password = message
        else if (path.includes('.port')) nextErrors.port = message
        else if (path.includes('.connections')) nextErrors.connections = message
      })
      setFieldErrors(nextErrors)
      setSaveError(firstFieldErrorMessage(nextErrors, error?.message || 'Save failed'))
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
      <DialogContent className="flex max-h-[85vh] max-w-3xl flex-col overflow-hidden" onOpenAutoFocus={focusDialogCloseButton}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>

        <div className="min-h-0 flex-1 overflow-y-auto pr-1">
        <div className="space-y-4">
          <div className="rounded-md border border-border/60 p-3">
            <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
              <div className="min-w-0 xl:flex-1">
                <Label className="text-sm font-medium">Provider Name</Label>
              </div>
              <div className="flex w-full items-center gap-2 xl:max-w-sm">
                <Input ref={nameInputRef} className={`h-9 ${fieldClass('name')}`} value={draft.name} onChange={(event) => update('name', event.target.value)} placeholder="e.g. Newshosting" disabled={editing} />
                {!editing && (
                  <>
                    <DropdownMenu>
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <DropdownMenuTrigger asChild>
                            <Button type="button" variant={selectedPreset ? "secondary" : "outline"} size="icon" className="h-9 w-9 shrink-0">
                              <Download className="h-4 w-4" />
                            </Button>
                          </DropdownMenuTrigger>
                        </TooltipTrigger>
                        <TooltipContent>{selectedPreset ? `Load preset (${selectedPreset.name})` : 'Load preset'}</TooltipContent>
                      </Tooltip>
                      <DropdownMenuContent
                        align="end"
                        className="max-h-80 w-56 overflow-y-auto"
                        onCloseAutoFocus={(event) => {
                          event.preventDefault()
                        }}
                      >
                        {PROVIDER_PRESETS.map((preset) => (
                          <DropdownMenuItem
                            key={preset.name}
                            onClick={() => {
                              setSaveError('')
                              setFieldErrors({})
                              setDraft((current) => ({
                                ...current,
                                name: preset.name,
                                host: preset.host,
                                port: preset.port,
                                use_ssl: preset.use_ssl,
                                connections: preset.connections,
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
                    {selectedPreset?.info_url && (
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Button
                            type="button"
                            variant="outline"
                            size="icon"
                            className="h-9 w-9 shrink-0"
                            onClick={() => window.open(selectedPreset.info_url, '_blank', 'noopener,noreferrer')}
                          >
                            <ExternalLink className="h-4 w-4" />
                          </Button>
                        </TooltipTrigger>
                        <TooltipContent>Open provider info</TooltipContent>
                      </Tooltip>
                    )}
                  </>
                )}
              </div>
            </div>
          </div>

          <div className="rounded-md border border-border/60">

            <div className="relative p-3">
              <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                <div className="min-w-0 xl:flex-1">
                  <Label className="text-sm font-medium">Host</Label>
                </div>
                <div className="w-full xl:max-w-sm">
                  <Input className={`h-9 ${fieldClass('host')}`} value={draft.host} onChange={(event) => update('host', event.target.value)} placeholder="news.example.com" />
                </div>
              </div>
            </div>

            <div className="relative p-3">
              <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
              <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                <div className="min-w-0 xl:flex-1">
                  <Label className="text-sm font-medium">Port</Label>
                </div>
                <div className="w-full xl:max-w-[8rem]">
                  <Input className={`h-9 ${fieldClass('port')}`} type="number" min={1} value={draft.port} onChange={(event) => update('port', event.target.value === '' ? 563 : Number(event.target.value))} />
                </div>
              </div>
            </div>

            <div className="relative p-3">
              <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
              <div className="flex items-center justify-between gap-4">
                <div className="text-sm font-medium">SSL</div>
                <Switch checked={draft.use_ssl} onCheckedChange={(checked) => update('use_ssl', checked === true)} />
              </div>
            </div>
          </div>

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

          <div className="rounded-md border border-border/60 p-3">
            <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
              <div className="min-w-0 xl:flex-1">
                <Label className="text-sm font-medium">Connections</Label>
              </div>
              <div className="w-full xl:max-w-[8rem]">
                <Input className={`h-9 ${fieldClass('connections')}`} type="number" min={1} value={draft.connections} onChange={(event) => update('connections', event.target.value === '' ? 30 : Number(event.target.value))} />
              </div>
            </div>
            <p className="mt-3 text-[10px] text-muted-foreground">
              Check allowed connections based on your current plan.
            </p>
            <p className="mt-1 text-[10px] text-muted-foreground">
              Most users will find that using between 10 and 20 connections provides a good balance of speed and performance. However, those on faster Internet connections or accessing a larger volume of articles may benefit from increasing the number of connections.
            </p>
            <p className="mt-1 text-[10px] text-muted-foreground">
              Using too many connections may lead to slower speeds or errors. If performance drops or connection issues occur, try lowering the number of connections.
            </p>
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
          <div className="flex flex-row items-center justify-end gap-2">
            <Button type="button" variant="outline" onClick={requestClose}>Cancel</Button>
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
        description="Your unsaved provider changes will be lost."
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

export function ProviderSettings({ fields = [], append, remove, replace, onPersist, onClearStatus, onStatus, stats, streamsByName = {} }) {
  const providers = fields
  const [editingIndex, setEditingIndex] = useState(null)
  const [showAddDialog, setShowAddDialog] = useState(false)
  const [knownOnlineHosts, setKnownOnlineHosts] = useState({})
  const [deleteTarget, setDeleteTarget] = useState(null)
  const [deleteBlockedName, setDeleteBlockedName] = useState('')
  const providerStatusByHost = useMemo(() => {
    const map = new Map()
    ;(stats?.providers || []).forEach((provider) => {
      map.set((provider.host || '').toLowerCase(), true)
    })
    return map
  }, [stats])

  useEffect(() => {
    if (!stats?.providers?.length) return
    setKnownOnlineHosts((current) => {
      const next = { ...current }
      stats.providers.forEach((provider) => {
        const host = (provider.host || '').toLowerCase()
        if (host) next[host] = true
      })
      return next
    })
  }, [stats])

  useEffect(() => () => {
    onClearStatus?.()
  }, [onClearStatus])

  const replaceProviders = (nextProviders) => {
    const normalized = nextProviders.map((provider, index) => ({
      ...normalizeProviderDraft(provider),
      priority: index + 1,
    }))
    replace(normalized)
    return normalized
  }

  const handleCreate = async (draft) => {
    const nextProviders = providers.map((provider) => normalizeProviderDraft(provider))
    nextProviders.push(normalizeProviderDraft({ ...draft, priority: providers.length + 1 }))
    const normalizedProviders = nextProviders.map((provider, index) => ({
      ...provider,
      priority: index + 1,
    }))
    await onPersist?.(normalizedProviders)
    replaceProviders(normalizedProviders)
    setDeleteBlockedName('')
    onStatus?.({ type: 'success', message: `Provider "${draft.name || draft.host}" created successfully.${CACHE_CLEARED_SUFFIX}` })
  }

  const handleSave = async (index, draft) => {
    const current = providers[index]
    if (!current) return
    const updated = {
      ...normalizeProviderDraft(draft),
      priority: Number(current.priority || index + 1),
    }
    const nextProviders = [...providers]
    nextProviders[index] = updated
    const normalizedProviders = nextProviders.map((provider, providerIndex) => ({
      ...normalizeProviderDraft(provider),
      priority: providerIndex + 1,
    }))
    await onPersist?.(normalizedProviders)
    replaceProviders(normalizedProviders)
    setDeleteBlockedName('')
    onStatus?.({ type: 'success', message: `Provider "${draft.name || draft.host}" saved successfully.${CACHE_CLEARED_SUFFIX}` })
  }

  const handleDelete = async (index) => {
    const provider = providers[index]
    if (!provider) return
    let assignedStreams = []
    try {
      const liveStreams = await apiFetch('/api/streams')
      assignedStreams = assignedStreamsForProvider(mapStreamsByUsername(liveStreams), provider.name)
    } catch {
      assignedStreams = assignedStreamsForProvider(streamsByName, provider.name)
    }
    if (assignedStreams.length > 0) {
      setDeleteBlockedName(provider.name || provider.host || '')
      onStatus?.({
        type: 'error',
        message: `Provider "${provider.name || provider.host}" cannot be deleted while assigned to stream(s): ${assignedStreams.join(', ')}`
      })
      return
    }
    setDeleteBlockedName('')
    const nextProviders = providers.filter((_, currentIndex) => currentIndex !== index)
    try {
      await onPersist?.(nextProviders)
      replaceProviders(nextProviders)
      onStatus?.({ type: 'success', message: `Provider "${provider.name || provider.host}" deleted successfully.${CACHE_CLEARED_SUFFIX}` })
    } catch (error) {
      onStatus?.({
        type: 'error',
        message: error?.message || `Failed to delete provider "${provider.name || provider.host}".`,
      })
    }
  }

  const handleToggleEnabled = async (index, enabled) => {
    const current = providers[index]
    if (!current) return
    const nextProviders = [...providers]
    nextProviders[index] = {
      ...normalizeProviderDraft(current),
      enabled,
      priority: Number(current.priority || index + 1),
    }
    const normalizedProviders = nextProviders.map((provider, providerIndex) => ({
      ...normalizeProviderDraft(provider),
      priority: providerIndex + 1,
    }))
    await onPersist?.(normalizedProviders)
    replaceProviders(normalizedProviders)
    setDeleteBlockedName('')
    onStatus?.({ type: 'success', message: `Provider "${current.name || current.host}" ${enabled ? 'enabled' : 'disabled'} successfully.${CACHE_CLEARED_SUFFIX}` })
  }

  return (
    <TooltipProvider delayDuration={100}>
      <div className="space-y-4">
        <Card>
          <CardHeader>
            <div className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-3">
              <div className="min-w-0 space-y-0.5">
                <CardTitle>Providers</CardTitle>
                <CardDescription className="break-words">Configure your Usenet provider connections.</CardDescription>
              </div>
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button type="button" variant="destructive" size="icon" className="h-9 w-9 shrink-0" onClick={() => setShowAddDialog(true)}>
                    <Plus className="h-4 w-4" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>Add Provider</TooltipContent>
              </Tooltip>
            </div>
          </CardHeader>
          <CardContent className="space-y-3">
            {providers.length === 0 ? (
              <p className="text-sm text-muted-foreground">No providers configured yet.</p>
            ) : (
              providers.map((provider, index) => {
                const normalized = normalizeProviderDraft(provider)
                const summary = summarizeProvider(normalized)
                const hostKey = (normalized.host || '').toLowerCase()
                const isOnline = providerStatusByHost.has(hostKey) || (normalized.enabled === false && knownOnlineHosts[hostKey] === true)
                return (
                  <Card
                    key={`${normalized.name || normalized.host || 'provider'}-${index}`}
                    className={deleteBlockedName && deleteBlockedName === (normalized.name || normalized.host || '') ? 'border-destructive/60 ring-1 ring-destructive/30' : ''}
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
                                      className="h-6 w-12 data-[state=checked]:bg-green-500 data-[state=unchecked]:bg-muted-foreground/30"
                                      thumbClassName="flex h-5 w-5 items-center justify-center data-[state=checked]:translate-x-6 data-[state=unchecked]:translate-x-0"
                                    />
                                  </div>
                                </TooltipTrigger>
                                <TooltipContent>{normalized.enabled !== false ? 'Disable provider' : 'Enable provider'}</TooltipContent>
                              </Tooltip>
                              <Tooltip>
                                <TooltipTrigger asChild>
                                  <Button type="button" variant="outline" size="icon" className="h-9 w-9" onClick={() => {
                                    setDeleteBlockedName('')
                                    onClearStatus?.()
                                    setEditingIndex(index)
                                  }}>
                                    <Settings className="h-4 w-4" />
                                  </Button>
                                </TooltipTrigger>
                                <TooltipContent>Edit provider</TooltipContent>
                              </Tooltip>
                              <Tooltip>
                                <TooltipTrigger asChild>
                                  <Button type="button" variant="destructive" size="icon" className="h-9 w-9" onClick={() => setDeleteTarget({ index, name: normalized.name || normalized.host })}>
                                    <Trash2 className="h-4 w-4" />
                                  </Button>
                                </TooltipTrigger>
                                <TooltipContent>Delete provider</TooltipContent>
                              </Tooltip>
                            </div>
                            <div className="min-w-0 font-semibold sm:order-1">{normalized.name || normalized.host || `Provider ${index + 1}`}</div>
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

                    <ProviderDialog
                      open={editingIndex === index}
                      onOpenChange={(nextOpen) => {
                        if (!nextOpen) {
                          setDeleteBlockedName('')
                        }
                        setEditingIndex(nextOpen ? index : null)
                      }}
                      initialValue={normalized}
                      existingNames={providers.filter((_, currentIndex) => currentIndex !== index).map((provider) => provider?.name || '')}
                      existingProviders={providers.filter((_, currentIndex) => currentIndex !== index)}
                      onSave={(draft) => handleSave(index, draft)}
                      onClearStatus={onClearStatus}
                      title="Change Provider"
                      description="Edit provider settings."
                      saveLabel="Save"
                      editing
                    />
                  </Card>
                )
              })
            )}
          </CardContent>
        </Card>

        <ProviderDialog
          open={showAddDialog}
          onOpenChange={(nextOpen) => {
            setShowAddDialog(nextOpen)
          }}
          initialValue={emptyProviderDraft()}
          existingNames={providers.map((provider) => provider?.name || '')}
          existingProviders={providers}
          onSave={handleCreate}
          onClearStatus={onClearStatus}
          title="Add Provider"
          description="Add a new provider."
          saveLabel="Save"
          editing={false}
        />
        <ConfirmDialog
          open={Boolean(deleteTarget)}
          onOpenChange={(nextOpen) => {
            if (!nextOpen) setDeleteTarget(null)
          }}
          title="Delete provider?"
          description={deleteTarget ? `Are you sure you want to delete provider "${deleteTarget.name}"?` : ''}
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
