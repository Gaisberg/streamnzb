import React, { useCallback, useEffect, useMemo, useState } from 'react'
import { useForm } from 'react-hook-form'
import { Loader2, Paintbrush, Copy, Check } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { Form, FormField, FormItem, FormLabel, FormControl, FormMessage, FormDescription } from "@/components/ui/form"
import { PasswordInput } from "@/components/ui/password-input"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { EnvOverrideIndicator } from "@/components/EnvOverrideIndicator"
import { apiFetch } from '@/api'
import { useFieldAutoSave } from '@/hooks/useFieldAutoSave'
import { normalizeAvailNZBMode } from "@/lib/availnzb"
import { cn } from "@/lib/utils"

const CARD_FIELDS = {
  admin: ['log_level', 'verbose_nntp_logging', 'keep_log_files'],
  memory: ['memory_limit_mb'],
  playback: ['playback_startup_timeout_seconds', 'session_ttl_minutes', 'session_post_playback_ttl_minutes', 'speculative_preprobing_max_attempts', 'mute_error_video'],
  library: ['library_search_mode', 'library_max_items', 'library_max_size_mb', 'library_auto_save'],
  availnzb: ['availnzb_mode'],
}

const FIELD_CARD = Object.fromEntries(
  Object.entries(CARD_FIELDS).flatMap(([card, fields]) => fields.map((field) => [field, card]))
)

function pickInitialValues(values = {}) {
  const parsedPlaybackStartupTimeout = values.playback_startup_timeout_seconds == null
    ? 5
    : Number(values.playback_startup_timeout_seconds)
  const parsedSessionTtl = values.session_ttl_minutes == null
    ? 30
    : Number(values.session_ttl_minutes)
  const parsedSessionPostPlaybackTtl = values.session_post_playback_ttl_minutes == null
    ? 240
    : Number(values.session_post_playback_ttl_minutes)
  const rawPreProbe = values.speculative_preprobing_max_attempts ?? values.speculative_pre_probing_count
  const parsedSpeculativePreProbingMaxAttempts = rawPreProbe == null
    ? 3
    : Number(rawPreProbe)
  return {
    log_level: values.log_level ?? 'INFO',
    verbose_nntp_logging: values.verbose_nntp_logging === true,
    keep_log_files: Number(values.keep_log_files ?? 9) || 9,
    memory_limit_mb: Number(values.memory_limit_mb ?? 512),
    playback_startup_timeout_seconds: Number.isFinite(parsedPlaybackStartupTimeout) ? parsedPlaybackStartupTimeout : 5,
    session_ttl_minutes: Number.isFinite(parsedSessionTtl) ? parsedSessionTtl : 30,
    session_post_playback_ttl_minutes: Number.isFinite(parsedSessionPostPlaybackTtl) ? parsedSessionPostPlaybackTtl : 240,
    speculative_preprobing_max_attempts: Number.isFinite(parsedSpeculativePreProbingMaxAttempts) ? parsedSpeculativePreProbingMaxAttempts : 3,
    mute_error_video: values.mute_error_video === true,
    library_search_mode: values.library_search_mode ?? 'combine',
    library_max_items: Number(values.library_max_items ?? 5000) || 5000,
    library_max_size_mb: Number(values.library_max_size_mb ?? 250) || 250,
    library_auto_save: values.library_auto_save !== false,
    availnzb_mode: normalizeAvailNZBMode(values.availnzb_mode),
  }
}

export const AdvancedSettingsSection = React.memo(function AdvancedSettingsSection({
  initialValues,
  envOverrides,
  onPersist,
  onClearCache,
  saveStatus,
}) {
  const defaults = useMemo(() => pickInitialValues(initialValues), [initialValues])
  const [clearingCache, setClearingCache] = useState(false)
  const [showClearCacheConfirm, setShowClearCacheConfirm] = useState(false)
  const [showRestartConfirm, setShowRestartConfirm] = useState(false)
  const [availKeyInfo, setAvailKeyInfo] = useState({ key: '', url: '', status: null, loading: true })
  const [copiedKey, setCopiedKey] = useState(false)

  const form = useForm({ defaultValues: defaults })
  const { control, reset, setError, clearErrors } = form
  const { saveField, savingField, hasFieldChanged, revertField } = useFieldAutoSave({
    form,
    savedValues: defaults,
    onPersist,
  })

  const fetchAvailKeyInfo = useCallback(() => {
    apiFetch('/api/availnzb/status')
      .then((res) => {
        if (res) {
          setAvailKeyInfo({
            key: res.api_key || '',
            url: res.url || '',
            status: res.status || null,
            statusError: res.status_error || null,
            loading: false,
          })
        }
      })
      .catch(() => {
        setAvailKeyInfo((prev) => ({ ...prev, loading: false }))
      })
  }, [])

  useEffect(() => {
    fetchAvailKeyInfo()
  }, [fetchAvailKeyInfo])

  const handleCopyKey = () => {
    if (!availKeyInfo.key) return
    navigator.clipboard.writeText(availKeyInfo.key)
    setCopiedKey(true)
    setTimeout(() => setCopiedKey(false), 2000)
  }

  useEffect(() => {
    if (saveStatus?.type === 'error' && saveStatus.errors) {
      Object.entries(saveStatus.errors).forEach(([key, msg]) => {
        if (Object.values(CARD_FIELDS).flat().includes(key)) {
          setError(key, { type: 'server', message: msg })
        }
      })
    } else {
      clearErrors()
    }
  }, [saveStatus, setError, clearErrors])

  // Sync in fresh config values (post-save refetch, WS reconnect) without
  // clobbering a field the user is currently editing.
  useEffect(() => {
    reset(defaults, { keepDirtyValues: true })
  }, [defaults, reset])

  // Change-committing wrappers: switches and selects persist immediately,
  // text/number inputs persist on blur when their value actually changed.
  const commitField = (name) => { void saveField(name, FIELD_CARD[name]) }
  const blurCommit = (field, name) => () => { field.onBlur(); commitField(name) }

  const handleMemoryLimitBlur = (field) => () => {
    field.onBlur()
    if (hasFieldChanged('memory_limit_mb')) setShowRestartConfirm(true)
  }

  const handleClearCacheClick = async () => {
    if (clearingCache) return
    setClearingCache(true)
    try {
      await onClearCache()
    } finally {
      setClearingCache(false)
    }
  }

  const renderCardSpinner = (cardId) => (
    savingField && CARD_FIELDS[cardId].includes(savingField)
      ? <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
      : null
  )

  const stackedFieldRowClass = "flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between sm:gap-4"
  const controlMediumClass = "w-full min-w-0 sm:max-w-[10rem]"
  const controlSelectClass = "flex h-9 w-full min-w-0 max-w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 overflow-hidden text-ellipsis whitespace-nowrap sm:max-w-[14rem]"
  const labelClass = "min-w-0 text-sm font-medium"

  return (
    <Form {...form}>
      <form className="space-y-6">
        <div className="grid grid-cols-1 gap-6 2xl:grid-cols-2">
          <div className="space-y-6">
            <Card>
              <CardHeader>
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0 flex-1 max-w-[26rem] space-y-0.5">
                    <CardTitle>Logs</CardTitle>
                    <CardDescription>Log level and file retention. Changes save automatically.</CardDescription>
                  </div>
                  <div className="flex items-center gap-2">{renderCardSpinner('admin')}</div>
                </div>
              </CardHeader>
              <CardContent>
                <div className="space-y-4">
                  <div className="rounded-md border border-border/60">
                    <FormField control={control} name="log_level" render={({ field }) => (
                      <FormItem className="rounded-none border-0 p-3">
                        <div className={stackedFieldRowClass}>
                          <FormLabel className={cn(labelClass, 'flex items-center gap-1.5 sm:flex-1')}>Log Level <EnvOverrideIndicator show={envOverrides.includes('log_level')} /></FormLabel>
                          <FormControl>
                            <select className={controlSelectClass} {...field} onChange={(e) => { field.onChange(e); commitField('log_level') }}>
                              <option value="DEBUG">DEBUG</option>
                              <option value="INFO">INFO</option>
                              <option value="WARN">WARN</option>
                              <option value="ERROR">ERROR</option>
                            </select>
                          </FormControl>
                        </div>
                        <FormDescription className="mt-3">Controls how verbose StreamNZB logging should be.</FormDescription>
                        <FormMessage />
                      </FormItem>
                    )} />
                    <FormField control={control} name="verbose_nntp_logging" render={({ field }) => (
                      <FormItem className="relative rounded-none border-0 p-3">
                        <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                        <div className={stackedFieldRowClass}>
                          <div className="sm:flex-1">
                            <FormLabel className={labelClass}>Verbose NNTP logging</FormLabel>
                          </div>
                          <FormControl>
                            <Switch
                              checked={field.value === true}
                              onCheckedChange={(checked) => { field.onChange(checked); commitField('verbose_nntp_logging') }}
                            />
                          </FormControl>
                        </div>
                        <FormDescription className="mt-3">Include low-level NNTP connection and pool logs in DEBUG output.</FormDescription>
                        <FormMessage />
                      </FormItem>
                    )} />
                    <FormField control={control} name="keep_log_files" render={({ field }) => (
                      <FormItem className="relative rounded-none border-0 p-3">
                        <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                        <div className={stackedFieldRowClass}>
                          <FormLabel className={cn(labelClass, 'flex items-center gap-1.5 sm:flex-1')}>Keep log files <EnvOverrideIndicator show={envOverrides.includes('keep_log_files')} /></FormLabel>
                          <FormControl><Input type="number" min={1} max={50} className={`h-9 ${controlMediumClass}`} {...field} value={field.value ?? ''} onChange={e => { const v = e.target.value; field.onChange(v === '' ? 9 : Math.min(50, Math.max(1, Number(v) || 9))) }} onBlur={blurCommit(field, 'keep_log_files')} /></FormControl>
                        </div>
                        <FormDescription className="mt-3">Number of log files to keep. Oldest rotated logs are purged on restart.</FormDescription>
                        <FormMessage />
                      </FormItem>
                    )} />
                  </div>
                </div>
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0 flex-1 max-w-[30rem] space-y-0.5">
                    <CardTitle>Playback</CardTitle>
                    <CardDescription>Startup behavior before the first playable response is sent.</CardDescription>
                  </div>
                  <div className="shrink-0">{renderCardSpinner('playback')}</div>
                </div>
              </CardHeader>
              <CardContent>
                <div className="rounded-md border border-border/60">
                  <FormField control={control} name="playback_startup_timeout_seconds" render={({ field }) => (
                    <FormItem className="rounded-none border-0 p-3">
                      <div className={stackedFieldRowClass}>
                        <FormLabel className={cn(labelClass, 'sm:flex-1')}>Playback startup timeout (s)</FormLabel>
                        <FormControl><Input type="number" min={1} max={60} className={`h-9 ${controlMediumClass}`} {...field} value={field.value ?? ''} onChange={e => { const v = e.target.value; const next = Number(v); field.onChange(v === '' ? 5 : Math.min(60, Math.max(1, Number.isNaN(next) ? 5 : next))) }} onBlur={blurCommit(field, 'playback_startup_timeout_seconds')} /></FormControl>
                      </div>
                      <FormDescription className="mt-3">How long StreamNZB waits for the initial playback probe/open before failing over to the next release. Higher values reduce false startup timeouts but delay failover.</FormDescription>
                      <FormMessage />
                    </FormItem>
                  )} />
                  <FormField control={control} name="session_ttl_minutes" render={({ field }) => (
                    <FormItem className="relative rounded-none border-0 p-3">
                      <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                      <div className={stackedFieldRowClass}>
                        <FormLabel className={cn(labelClass, 'sm:flex-1')}>Session inactive TTL (m)</FormLabel>
                        <FormControl>
                          <Input
                            type="number"
                            min={1}
                            max={1440}
                            className={`h-9 ${controlMediumClass}`}
                            {...field}
                            value={field.value ?? ''}
                            onChange={e => {
                              const v = e.target.value;
                              const next = Number(v);
                              field.onChange(v === '' ? 30 : Math.min(1440, Math.max(1, Number.isNaN(next) ? 30 : next)))
                            }}
                            onBlur={blurCommit(field, 'session_ttl_minutes')}
                          />
                        </FormControl>
                      </div>
                      <FormDescription className="mt-3">
                        How long inactive or deferred catalog sessions stay in memory before being evicted.
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )} />
                  <FormField control={control} name="session_post_playback_ttl_minutes" render={({ field }) => (
                    <FormItem className="relative rounded-none border-0 p-3">
                      <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                      <div className={stackedFieldRowClass}>
                        <FormLabel className={cn(labelClass, 'sm:flex-1')}>Paused playback TTL (m)</FormLabel>
                        <FormControl>
                          <Input
                            type="number"
                            min={1}
                            max={1440}
                            className={`h-9 ${controlMediumClass}`}
                            {...field}
                            value={field.value ?? ''}
                            onChange={e => {
                              const v = e.target.value;
                              const next = Number(v);
                              field.onChange(v === '' ? 240 : Math.min(1440, Math.max(1, Number.isNaN(next) ? 240 : next)))
                            }}
                            onBlur={blurCommit(field, 'session_post_playback_ttl_minutes')}
                          />
                        </FormControl>
                      </div>
                      <FormDescription className="mt-3">
                        How long a session stays in memory after active playback ends (e.g. when paused). Keeping this long prevents needing to reload the catalog to resume.
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )} />
                  <FormField control={control} name="speculative_preprobing_max_attempts" render={({ field }) => (
                    <FormItem className="relative rounded-none border-0 p-3">
                      <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                      <div className={stackedFieldRowClass}>
                        <FormLabel className={cn(labelClass, 'sm:flex-1')}>Speculative pre-probing max attempts</FormLabel>
                        <FormControl>
                          <Input
                            type="number"
                            min={0}
                            max={5}
                            className={`h-9 ${controlMediumClass}`}
                            {...field}
                            value={field.value ?? ''}
                            onChange={e => {
                              const v = e.target.value;
                              const next = Number(v);
                              field.onChange(v === '' ? 3 : Math.min(5, Math.max(0, Number.isNaN(next) ? 3 : next)))
                            }}
                            onBlur={blurCommit(field, 'speculative_preprobing_max_attempts')}
                          />
                        </FormControl>
                      </div>
                      <FormDescription className="mt-3">
                        Maximum number of failover pre-probe attempts to test sequentially until a verified working stream is found when loading a stream list. Set 0 to disable. Pre-probing reduces cold-start latency but consumes indexer API downloads.
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )} />
                  <FormField control={control} name="mute_error_video" render={({ field }) => (
                    <FormItem className="relative rounded-none border-0 p-3">
                      <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                      <div className={stackedFieldRowClass}>
                        <div className="sm:flex-1">
                          <FormLabel className={labelClass}>Mute error video</FormLabel>
                        </div>
                        <FormControl>
                          <Switch
                            checked={field.value === true}
                            onCheckedChange={(checked) => { field.onChange(checked === true); commitField('mute_error_video') }}
                          />
                        </FormControl>
                      </div>
                      <FormDescription className="mt-3">
                        Mutes the audio of the "Failed to start video" playback stream played when all releases fail.
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )} />
                </div>
              </CardContent>
            </Card>

          </div>

          <div className="space-y-6">
            <Card>
              <CardHeader>
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0 flex-1 max-w-[26rem] space-y-0.5">
                    <CardTitle>AvailNZB</CardTitle>
                    <CardDescription>Configure how StreamNZB interacts with AvailNZB.</CardDescription>
                  </div>
                  <div className="shrink-0">{renderCardSpinner('availnzb')}</div>
                </div>
              </CardHeader>
              <CardContent>
                <div className="rounded-md border border-border/60">
                  <FormField control={control} name="availnzb_mode" render={({ field }) => (
                    <FormItem className="rounded-none border-0 p-3">
                      <div className={stackedFieldRowClass}>
                        <div className="sm:flex-1">
                          <FormLabel className={labelClass}>AvailNZB mode</FormLabel>
                        </div>
                        <FormControl>
                          <Switch
                            checked={normalizeAvailNZBMode(field.value) === 'on'}
                            onCheckedChange={(checked) => { field.onChange(checked ? 'on' : 'off'); commitField('availnzb_mode') }}
                          />
                        </FormControl>
                      </div>
                      <FormDescription className="mt-3">Controls whether StreamNZB uses AvailNZB. API key management is automatic.</FormDescription>
                      <FormMessage />
                    </FormItem>
                  )} />
                  <div className="relative rounded-none border-0 p-3">
                    <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                    <div className="space-y-2">
                      <div className="flex items-center justify-between gap-2">
                        <label className={labelClass}>AvailNZB API Key</label>
                        {availKeyInfo.status?.trust_level && (
                          <span className="text-xs text-muted-foreground">
                            Trust level: <strong className="capitalize text-foreground">{availKeyInfo.status.trust_level}</strong>
                          </span>
                        )}
                      </div>
                      <div className="flex items-center gap-2">
                        <div className="flex-1">
                          <PasswordInput
                            readOnly
                            value={availKeyInfo.key || (availKeyInfo.loading ? 'Loading key...' : 'Auto-generated on startup')}
                            className="h-9 w-full font-mono text-xs"
                          />
                        </div>
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          disabled={!availKeyInfo.key}
                          onClick={handleCopyKey}
                          className="h-9 shrink-0 gap-1.5 px-3"
                        >
                          {copiedKey ? <Check className="h-3.5 w-3.5 text-emerald-500" /> : <Copy className="h-3.5 w-3.5" />}
                          {copiedKey ? 'Copied' : 'Copy'}
                        </Button>
                      </div>
                      <p className="text-xs text-muted-foreground mt-2">
                        Your unique AvailNZB client API key. Used to contribute to and query the community availability database.
                      </p>
                    </div>
                  </div>
                </div>
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0 flex-1 max-w-[30rem] space-y-0.5">
                    <CardTitle>Memory & Cache</CardTitle>
                    <CardDescription>Runtime memory limits and search cache maintenance.</CardDescription>
                  </div>
                  <div className="shrink-0">{renderCardSpinner('memory')}</div>
                </div>
              </CardHeader>
              <CardContent className="space-y-3">
                <div className="rounded-md border border-border/60">
                  <FormField control={control} name="memory_limit_mb" render={({ field }) => (
                    <FormItem className="rounded-none border-0 p-3">
                      <div className={stackedFieldRowClass}>
                        <FormLabel className={cn(labelClass, 'sm:flex-1')}>Memory limit (MB)</FormLabel>
                        <FormControl><Input type="number" min={0} className={`h-9 ${controlMediumClass}`} {...field} value={field.value ?? ''} onChange={e => { const v = e.target.value; field.onChange(v === '' ? 0 : Number(v) || 0) }} onBlur={handleMemoryLimitBlur(field)} /></FormControl>
                      </div>
                      <FormDescription className="mt-3">Soft limit on total process memory (0 = no limit). Segment cache uses 80% of this. Restart required.</FormDescription>
                      <FormMessage />
                    </FormItem>
                  )} />
                </div>
                <div className="rounded-md border border-border/60 p-3">
                  <div className="flex items-center justify-between gap-4">
                    <div className="space-y-0.5">
                      <div className="text-sm font-medium">Clear cache</div>
                    </div>
                    <Button type="button" variant="destructive" onClick={() => setShowClearCacheConfirm(true)} disabled={clearingCache} className="h-9 shrink-0 gap-2">
                      {clearingCache ? <Loader2 className="h-4 w-4 animate-spin" /> : <Paintbrush className="h-4 w-4" />}
                      Clear
                    </Button>
                  </div>
                  <div className="mt-3 text-sm text-muted-foreground">Clears the in-memory playlist and raw search caches immediately.</div>
                </div>
              </CardContent>
            </Card>
          </div>
        </div>

        {/* Full width rather than a grid column: every row here puts its
            control far right of a long label, which a half-width column
            squeezed into wrapped copy and a cramped row on wide screens. */}
        <Card>
          <CardHeader>
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0 flex-1 space-y-0.5">
                <CardTitle>Library & Pre-Indexer Engine</CardTitle>
                <CardDescription>Cache NZBs and probed archive blueprints locally to serve repeated requests in &lt; 5ms with 0 indexer API calls.</CardDescription>
              </div>
              <div className="shrink-0">{renderCardSpinner('library')}</div>
            </div>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="rounded-md border border-border/60">
              <FormField control={control} name="library_search_mode" render={({ field }) => (
                <FormItem className="rounded-none border-0 p-3">
                  <div className={stackedFieldRowClass}>
                    <div className="sm:flex-1">
                      <FormLabel className={labelClass}>Library Search Priority</FormLabel>
                    </div>
                    <FormControl>
                      <select className={controlSelectClass} {...field} onChange={(e) => { field.onChange(e); commitField('library_search_mode') }}>
                        <option value="library_first">Library First (Fastest)</option>
                        <option value="combine">Combine Library + Indexers</option>
                        <option value="fallback_only">Fallback to Library</option>
                        <option value="disabled">Disabled</option>
                      </select>
                    </FormControl>
                  </div>
                  <FormDescription className="mt-3">Controls whether StreamNZB searches the local library database before making Newznab HTTP API calls.</FormDescription>
                  <FormMessage />
                </FormItem>
              )} />
              <FormField control={control} name="library_auto_save" render={({ field }) => (
                <FormItem className="relative rounded-none border-0 p-3">
                  <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                  <div className={stackedFieldRowClass}>
                    <div className="sm:flex-1">
                      <FormLabel className={labelClass}>Auto-Cache Playback Releases</FormLabel>
                    </div>
                    <FormControl>
                      <Switch
                        checked={field.value !== false}
                        onCheckedChange={(checked) => { field.onChange(checked); commitField('library_auto_save') }}
                      />
                    </FormControl>
                  </div>
                  <FormDescription className="mt-3">Automatically store NZB files and probed RAR/7z blueprints in the library when playback completes.</FormDescription>
                  <FormMessage />
                </FormItem>
              )} />
              <FormField control={control} name="library_max_items" render={({ field }) => (
                <FormItem className="relative rounded-none border-0 p-3">
                  <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                  <div className={stackedFieldRowClass}>
                    <div className="sm:flex-1">
                      <FormLabel className={labelClass}>Max Cached Items</FormLabel>
                    </div>
                    <FormControl>
                      <Input type="number" min={10} max={100000} {...field} onChange={e => field.onChange(Number(e.target.value))} onBlur={blurCommit(field, 'library_max_items')} className={`h-9 font-mono text-xs ${controlMediumClass}`} />
                    </FormControl>
                  </div>
                  <FormDescription className="mt-3">Maximum releases to store before LRU eviction deletes oldest entries. Default 5000.</FormDescription>
                  <FormMessage />
                </FormItem>
              )} />
            </div>
          </CardContent>
        </Card>

        <ConfirmDialog
          open={showRestartConfirm}
          onOpenChange={(nextOpen) => {
            setShowRestartConfirm(nextOpen)
            if (!nextOpen) revertField('memory_limit_mb')
          }}
          title="Restart required"
          description="Changing the memory limit requires a StreamNZB restart. Do you want to save this change now?"
          confirmLabel="Save"
          confirmVariant="destructive"
          onConfirm={() => {
            setShowRestartConfirm(false)
            commitField('memory_limit_mb')
          }}
        />
        <ConfirmDialog
          open={showClearCacheConfirm}
          onOpenChange={setShowClearCacheConfirm}
          title="Clear search cache?"
          description="This clears the in-memory playlist and raw search caches immediately."
          confirmLabel="Clear"
          onConfirm={() => {
            setShowClearCacheConfirm(false)
            void handleClearCacheClick()
          }}
        />
      </form>
    </Form>
  )
})
