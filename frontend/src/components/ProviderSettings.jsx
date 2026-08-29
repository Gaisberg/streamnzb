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
  dialogRowClass, dialogLabelClass, dialogControlWideClass, dialogControlNameClass, dialogControlNarrowClass, dialogControlSwitchClass,
} from "@/hooks/useEntityDialog"
import { ProviderSpeedTestDialog } from "@/components/ProviderSpeedTestDialog"
import { UsageChips } from "@/components/UsageChips"
import { apiFetch } from "@/api"
import { assignedStreams } from "@/lib/usage"
import { mapStreamsByUsername } from "@/lib/streams"
import { Download, ExternalLink, Gauge, Plus, Settings, Trash2 } from "lucide-react"

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

// The standard NNTP ports. Toggling SSL moves between them, so the usual case
// needs no second edit — but only while the port still is one of them: a port
// the user typed themselves is theirs to keep.
const SSL_PORT = 563
const PLAIN_PORT = 119

function portForSSL(port, useSSL) {
  const current = Number(port)
  if (current !== SSL_PORT && current !== PLAIN_PORT) return port
  return useSSL ? SSL_PORT : PLAIN_PORT
}

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
    backup: value.backup === true,
    // Left undefined when unset, so it is dropped from the payload and the
    // provider keeps inheriting the deployment default rather than pinning
    // whatever number happened to be on screen.
    pipeline_depth: normalizePipelineDepth(value.pipeline_depth),
  }
}

// Articles per request. Empty means inherit; 1 means off for this provider
// alone; anything else is the number of requests it may have outstanding on one
// connection.
function normalizePipelineDepth(raw) {
  const depth = Number(raw)
  return Number.isFinite(depth) && depth > 0 ? depth : undefined
}

function describePipelineDepth(depth) {
  const value = normalizePipelineDepth(depth)
  if (!value) return null
  return value === 1 ? 'Pipelining: off' : `Articles per request: ${value}`
}

function emptyProviderDraft() {
  return normalizeProviderDraft({})
}

function summarizeProvider(provider) {
  const parts = []
  if (provider.host) parts.push(`${provider.host}:${provider.port || 563}`)
  parts.push(provider.use_ssl !== false ? 'SSL' : 'No SSL')
  parts.push(`Connections: ${provider.connections || 30}`)
  if (provider.backup === true) parts.push('Backup only')
  const pipeline = describePipelineDepth(provider.pipeline_depth)
  if (pipeline) parts.push(pipeline)
  return parts
}

function assignedStreamsForProvider(streamsByName, providerName) {
  return assignedStreams(streamsByName, 'provider_selections', providerName)
}

function ProviderDialog({ open, onOpenChange, initialValue, onSave, onClearStatus, title, description, saveLabel, existingNames = [], existingProviders = [], editing = false }) {
  const nameInputRef = useRef(null)
  const dialog = useEntityDialog({
    open,
    onOpenChange,
    initialValue,
    makeDraft: () => normalizeProviderDraft(initialValue),
    normalize: normalizeProviderDraft,
    onClearStatus,
  })
  const { draft, setDraft, update, fieldClass, setSaveError, setFieldErrors } = dialog

  const duplicateName = existingNames.some((name) => normalizeName(name) === normalizeName(draft.name))
  const duplicateProvider = existingProviders.find((provider) => normalizeProviderIdentity(provider) === normalizeProviderIdentity(draft))
  const selectedPreset = PROVIDER_PRESETS.find((preset) => normalizeName(preset.host) === normalizeName(draft.host))

  // The shared dialog row layout, under this file's historical names.
  const rowClass = dialogRowClass
  const labelClass = dialogLabelClass
  const controlWideClass = dialogControlWideClass
  const controlNameClass = dialogControlNameClass
  const controlNarrowClass = dialogControlNarrowClass
  const controlSwitchClass = dialogControlSwitchClass

  const handleSave = () => dialog.runSave({
    validate: () => {
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
      return nextFieldErrors
    },
    commit: () => onSave(normalizeProviderDraft(draft)),
    mapError: (error) => {
      const nextErrors = {}
      Object.entries(error?.fieldErrors || {}).forEach(([path, message]) => {
        if (path.includes('.name')) nextErrors.name = message
        else if (path.includes('.host')) nextErrors.host = message
        else if (path.includes('.username')) nextErrors.username = message
        else if (path.includes('.password')) nextErrors.password = message
        else if (path.includes('.port')) nextErrors.port = message
        else if (path.includes('.connections')) nextErrors.connections = message
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
      discardDescription="Your unsaved provider changes will be lost."
    >
        <div className="space-y-4">
          <div className="rounded-md border border-border/60 p-3">
            <div className={rowClass}>
              <div className={labelClass}>
                <Label className="text-sm font-medium">Name</Label>
              </div>
              <div className={controlNameClass}>
                <Input ref={nameInputRef} className={`h-9 ${fieldClass('name')}`} value={draft.name} onChange={(event) => update('name', event.target.value)} placeholder="e.g. Newshosting" />
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
              <div className={rowClass}>
                <div className={labelClass}>
                  <Label className="text-sm font-medium">Host</Label>
                </div>
                <div className={controlWideClass}>
                  <Input className={`h-9 ${fieldClass('host')}`} value={draft.host} onChange={(event) => update('host', event.target.value)} placeholder="news.example.com" />
                </div>
              </div>
            </div>

            <div className="relative p-3">
              <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
              <div className={rowClass}>
                <div className={labelClass}>
                  <Label className="text-sm font-medium">Port</Label>
                </div>
                <div className={controlNarrowClass}>
                  <Input className={`h-9 ${fieldClass('port')}`} type="number" min={1} value={draft.port} onChange={(event) => update('port', event.target.value === '' ? (draft.use_ssl ? SSL_PORT : PLAIN_PORT) : Number(event.target.value))} />
                </div>
              </div>
            </div>

            <div className="relative p-3">
              <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
              <div className={rowClass}>
                <div className={labelClass}>
                  <Label className="text-sm font-medium">SSL</Label>
                </div>
                <div className={controlSwitchClass}>
                  <Switch
                    checked={draft.use_ssl}
                    onCheckedChange={(checked) => {
                      const useSSL = checked === true
                      setDraft((current) => ({ ...current, use_ssl: useSSL, port: portForSSL(current.port, useSSL) }))
                    }}
                  />
                </div>
              </div>
            </div>
          </div>

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

          <div className="rounded-md border border-border/60 p-3">
            <div className={rowClass}>
              <div className={labelClass}>
                <Label className="text-sm font-medium">Connections</Label>
              </div>
              <div className={controlNarrowClass}>
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

            <div className={`${rowClass} mt-4 border-t border-border/60 pt-4`}>
              <div className={labelClass}>
                <Label className="text-sm font-medium">Backup only</Label>
              </div>
              <div className={controlSwitchClass}>
                <Switch
                  checked={draft.backup === true}
                  onCheckedChange={(checked) => update('backup', checked === true)}
                />
              </div>
            </div>
            <p className="mt-3 text-[10px] text-muted-foreground">
              Hold this provider back for failover. It is never asked for an article while another provider can serve one, so a metered block account is charged only for what the others are missing.
            </p>
            <p className="mt-1 text-[10px] text-muted-foreground">
              Priority orders the providers that share the work; this takes one out of that rotation entirely. Turn it on for pay-per-GB block accounts, off for the unlimited providers you stream from.
            </p>

            <div className={`${rowClass} mt-4 border-t border-border/60 pt-4`}>
              <div className={labelClass}>
                <Label className="text-sm font-medium">Articles per request</Label>
              </div>
              <div className={controlNarrowClass}>
                <select
                  className="flex h-9 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2"
                  value={draft.pipeline_depth ?? ''}
                  onChange={(event) => update('pipeline_depth', event.target.value === '' ? undefined : Number(event.target.value))}
                >
                  <option value="">Default</option>
                  <option value="1">Off</option>
                  {[2, 3, 4, 5, 6, 7, 8].map((depth) => (
                    <option key={depth} value={depth}>{depth}</option>
                  ))}
                </select>
              </div>
            </div>
            <p className="mt-3 text-[10px] text-muted-foreground">
              How many articles may be requested at once on one connection, so the next article is already arriving when the current one ends. It only takes effect when read-ahead runs out of connections; while there are spare ones, each article still gets its own.
            </p>
            <p className="mt-1 text-[10px] text-muted-foreground">
              Higher values pay off the further away the server is: 2 covers a nearby provider, 3 covers a distant one. Leave it on Default unless this provider's latency differs from your others.
            </p>
            <p className="mt-1 text-[10px] text-muted-foreground">
              Set it to Off if this provider mishandles more than one request at a time — the symptom is stalled or corrupt playback that clears up once it is off.
            </p>
          </div>
        </div>
    </EntityDialog>
  )
}

export function ProviderSettings({ fields = [], replace, onPersist, onClearStatus, onStatus, componentHealth, streamsByName = {} }) {
  const providers = fields
  const [editingIndex, setEditingIndex] = useState(null)
  const [speedTestIndex, setSpeedTestIndex] = useState(null)
  const [showAddDialog, setShowAddDialog] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState(null)
  const [deleteBlockedName, setDeleteBlockedName] = useState('')
  const healthByName = useMemo(() => indexHealth(componentHealth), [componentHealth])

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

  // Applying a suggestion goes through the normal save path, so it validates,
  // persists and reloads exactly like an edit in the dialog.
  const handleApplyConnections = async (index, connections) => {
    const current = providers[index]
    if (!current || !connections) return
    await handleSave(index, { ...normalizeProviderDraft(current), connections })
  }

  const handleApplyPipelineDepth = async (index, depth) => {
    const current = providers[index]
    if (!current || !normalizePipelineDepth(depth)) return
    await handleSave(index, { ...normalizeProviderDraft(current), pipeline_depth: depth })
  }

  const onRequestDelete = async (index) => {
    const provider = providers[index]
    if (!provider) return
    let assignedStreams = []
    try {
      const liveStreams = await apiFetch('/api/streams')
      assignedStreams = assignedStreamsForProvider(mapStreamsByUsername(liveStreams), provider.name)
    } catch {
      assignedStreams = assignedStreamsForProvider(streamsByName, provider.name)
    }
    setDeleteTarget({ index, name: provider.name || provider.host || '', assignedStreams })
  }

  const handleDelete = async (index) => {
    const provider = providers[index]
    if (!provider) return
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
                const healthRecord = healthFor(healthByName, 'provider', (normalized.name || '').trim())
                // Same contract as the indexer dot: the switch and the health
                // verdict, nothing derived or delayed. Matching by name also
                // ends the by-host lookup that made two providers on one host
                // share a status.
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
                                    setSpeedTestIndex(index)
                                  }}>
                                    <Gauge className="h-4 w-4" />
                                  </Button>
                                </TooltipTrigger>
                                <TooltipContent>Speed test provider</TooltipContent>
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
                                  <Button type="button" variant="destructive" size="icon" className="h-9 w-9" onClick={() => void onRequestDelete(index)}>
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
                          <UsageChips labels={assignedStreamsForProvider(streamsByName, normalized.name)} />
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

                    <ProviderSpeedTestDialog
                      open={speedTestIndex === index}
                      onOpenChange={(nextOpen) => setSpeedTestIndex(nextOpen ? index : null)}
                      provider={normalized}
                      onApplyConnections={(connections) => handleApplyConnections(index, connections)}
                      onApplyPipelineDepth={(depth) => handleApplyPipelineDepth(index, depth)}
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
          description={
            deleteTarget
              ? deleteTarget.assignedStreams?.length > 0
                ? `Provider "${deleteTarget.name}" is currently used by stream(s): ${deleteTarget.assignedStreams.join(', ')}. Are you sure you want to delete it? It will also be removed from the configured streams.`
                : `Are you sure you want to delete provider "${deleteTarget.name}"?`
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
