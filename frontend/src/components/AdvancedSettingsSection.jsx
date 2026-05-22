import React, { forwardRef, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react'
import { useForm, useWatch } from 'react-hook-form'
import { Loader2, AlertTriangle, Save, Paintbrush } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { Form, FormField, FormItem, FormLabel, FormControl, FormMessage, FormDescription } from "@/components/ui/form"
import { PasswordInput } from "@/components/ui/password-input"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { normalizeAvailNZBMode } from "@/lib/availnzb"
import { cn } from "@/lib/utils"

const CARD_FIELDS = {
  admin: ['log_level', 'verbose_nntp_logging', 'keep_log_files', 'nzb_history_retention_days'],
  memory: ['memory_limit_mb'],
  playback: ['playback_startup_timeout_seconds', 'failover_fast_mode'],
  availnzb: ['availnzb_mode', 'availnzb_filter_reported_bad'],
  metadata: ['tmdb_api_key', 'tvdb_api_key'],
}

function pickInitialValues(values = {}) {
  const parsedRetentionDays = values.nzb_history_retention_days == null
    ? 90
    : Number(values.nzb_history_retention_days)
  const parsedPlaybackStartupTimeout = values.playback_startup_timeout_seconds == null
    ? 5
    : Number(values.playback_startup_timeout_seconds)
  return {
    log_level: values.log_level ?? 'INFO',
    verbose_nntp_logging: values.verbose_nntp_logging === true,
    keep_log_files: Number(values.keep_log_files ?? 9) || 9,
    nzb_history_retention_days: Number.isFinite(parsedRetentionDays) ? parsedRetentionDays : 90,
    memory_limit_mb: Number(values.memory_limit_mb ?? 512),
    playback_startup_timeout_seconds: Number.isFinite(parsedPlaybackStartupTimeout) ? parsedPlaybackStartupTimeout : 5,
    failover_fast_mode: values.failover_fast_mode == null ? true : values.failover_fast_mode === true,
    availnzb_mode: normalizeAvailNZBMode(values.availnzb_mode),
    availnzb_filter_reported_bad: values.availnzb_filter_reported_bad === true,
    tmdb_api_key: values.tmdb_api_key ?? '',
    tvdb_api_key: values.tvdb_api_key ?? '',
  }
}

function EnvOverrideIndicator({ show, message = 'Overwritten by environment variable on restart.' }) {
  if (!show) return null
  return (
    <TooltipProvider delayDuration={100}>
      <Tooltip>
        <TooltipTrigger asChild>
          <button type="button" className="inline-flex items-center text-amber-600 hover:text-amber-700 align-middle" aria-label={message}>
            <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
          </button>
        </TooltipTrigger>
        <TooltipContent side="top" align="start">{message}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}

export const AdvancedSettingsSection = forwardRef(function AdvancedSettingsSection({
  initialValues,
  envOverrides,
  isSaving,
  onPersist,
  onClearCache,
  onRefreshAvailNZBStatus,
  onDirtyChange,
  onProceedTabChange,
}, ref) {
  const defaults = useMemo(() => pickInitialValues(initialValues), [initialValues])
  const [lastSavedValues, setLastSavedValues] = useState(defaults)
  const [savingCard, setSavingCard] = useState('')
  const [clearingCache, setClearingCache] = useState(false)
  const [showClearCacheConfirm, setShowClearCacheConfirm] = useState(false)
  const [showUnlockConfirm, setShowUnlockConfirm] = useState(false)
  const [showRestartConfirm, setShowRestartConfirm] = useState(false)
  const [showDiscardConfirm, setShowDiscardConfirm] = useState(false)
  const [pendingTabChange, setPendingTabChange] = useState('')
  const [showUnsavedHighlights, setShowUnsavedHighlights] = useState(false)
  const dirtyRef = useRef(false)

  const form = useForm({ defaultValues: defaults })
  const { control, handleSubmit, reset, getValues, formState } = form
  const watchedValues = useWatch({ control })
  const availNZBModeEnabled = normalizeAvailNZBMode(watchedValues?.availnzb_mode) === 'on'

  useEffect(() => {
    const currentValues = pickInitialValues(watchedValues)
    dirtyRef.current = JSON.stringify(currentValues) !== JSON.stringify(lastSavedValues)
    onDirtyChange?.(dirtyRef.current)
  }, [lastSavedValues, onDirtyChange, watchedValues])

  useEffect(() => {
    reset(defaults)
    setLastSavedValues(defaults)
    dirtyRef.current = false
    onDirtyChange?.(false)
  }, [defaults, onDirtyChange, reset])

  useImperativeHandle(ref, () => ({
    hasUnsavedChanges() {
      return dirtyRef.current
    },
    discardChanges() {
      reset(lastSavedValues)
      dirtyRef.current = false
      onDirtyChange?.(false)
    },
    requestTabChange(nextTab) {
      if (!dirtyRef.current) {
        onProceedTabChange(nextTab)
        return true
      }
      setPendingTabChange(nextTab)
      setShowDiscardConfirm(true)
      return false
    },
  }), [lastSavedValues, onProceedTabChange, reset])

  const saveCard = async (cardId) => {
    setSavingCard(cardId)
    try {
      const values = getValues()
      const payload = Object.fromEntries(CARD_FIELDS[cardId].map((key) => [key, values[key]]))
      await onPersist(payload, cardId)
      const nextValues = { ...lastSavedValues, ...payload }
      setLastSavedValues(nextValues)
      reset(nextValues)
      dirtyRef.current = false
      onDirtyChange?.(false)
      setShowUnsavedHighlights(false)
      if (cardId === 'availnzb') {
        void onRefreshAvailNZBStatus?.()
      }
    } finally {
      setSavingCard('')
    }
  }

  const handleCardSave = (cardId) => handleSubmit(async () => {
    if (cardId === 'memory' && formState.dirtyFields?.memory_limit_mb) {
      setShowRestartConfirm(true)
      return
    }
    await saveCard(cardId)
  })()

  const renderSaveButton = (cardId) => (
    <TooltipProvider delayDuration={100}>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            type="button"
            variant="destructive"
            size="icon"
            onClick={() => { void handleCardSave(cardId) }}
            disabled={isSaving || formState.isSubmitting}
            className="h-9 w-9"
          >
            {(isSaving || formState.isSubmitting) && savingCard === cardId
              ? <Loader2 className="h-4 w-4 animate-spin" />
              : <Save className="h-4 w-4" />}
          </Button>
        </TooltipTrigger>
        <TooltipContent>Save</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )

  const fieldClassName = (fieldName, baseClassName = '') => cn(
    baseClassName,
    showUnsavedHighlights && formState.dirtyFields?.[fieldName] && 'border-destructive ring-1 ring-destructive focus-visible:ring-destructive'
  )
  const stackedFieldRowClass = "flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between sm:gap-4"
  const controlWideClass = "w-full min-w-0 sm:max-w-xs"
  const controlMediumClass = "w-full min-w-0 sm:max-w-[10rem]"
  const controlSelectClass = "flex h-9 w-full min-w-0 max-w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 overflow-hidden text-ellipsis whitespace-nowrap sm:max-w-[14rem]"
  const labelClass = "min-w-0 text-sm font-medium"

  const handleClearCacheClick = async () => {
    if (clearingCache) return
    setClearingCache(true)
    try {
      await onClearCache()
    } finally {
      setClearingCache(false)
    }
  }

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
                    <CardDescription>Log level and file retention.</CardDescription>
                  </div>
                  <div className="flex items-center gap-2">{renderSaveButton('admin')}</div>
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
                            <select className={fieldClassName('log_level', controlSelectClass)} {...field}>
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
                              onCheckedChange={field.onChange}
                              className={showUnsavedHighlights && formState.dirtyFields?.verbose_nntp_logging ? 'ring-2 ring-destructive ring-offset-2 ring-offset-background' : ''}
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
                          <FormControl><Input type="number" min={1} max={50} className={fieldClassName('keep_log_files', `h-9 ${controlMediumClass}`)} {...field} value={field.value ?? ''} onChange={e => { const v = e.target.value; field.onChange(v === '' ? 9 : Math.min(50, Math.max(1, Number(v) || 9))) }} /></FormControl>
                        </div>
                        <FormDescription className="mt-3">Number of log files to keep. Oldest rotated logs are purged on restart.</FormDescription>
                        <FormMessage />
                      </FormItem>
                    )} />
                  </div>

                  <div className="rounded-md border border-border/60">
                    <FormField control={control} name="nzb_history_retention_days" render={({ field }) => (
                      <FormItem className="rounded-none border-0 p-3">
                        <div className={stackedFieldRowClass}>
                          <FormLabel className={cn(labelClass, 'sm:flex-1')}>NZB history retention (days)</FormLabel>
                          <FormControl><Input type="number" min={0} max={3650} className={fieldClassName('nzb_history_retention_days', `h-9 ${controlMediumClass}`)} {...field} value={field.value ?? ''} onChange={e => { const v = e.target.value; const next = Number(v); field.onChange(v === '' ? 90 : Math.min(3650, Math.max(0, Number.isNaN(next) ? 90 : next))) }} /></FormControl>
                        </div>
                        <FormDescription className="mt-3">Delete NZB history entries older than this many days on startup.</FormDescription>
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
                  <div className="shrink-0">{renderSaveButton('playback')}</div>
                </div>
              </CardHeader>
              <CardContent>
                <div className="rounded-md border border-border/60">
                  <FormField control={control} name="playback_startup_timeout_seconds" render={({ field }) => (
                    <FormItem className="rounded-none border-0 p-3">
                      <div className={stackedFieldRowClass}>
                        <FormLabel className={cn(labelClass, 'sm:flex-1')}>Playback startup timeout (s)</FormLabel>
                        <FormControl><Input type="number" min={1} max={60} className={fieldClassName('playback_startup_timeout_seconds', `h-9 ${controlMediumClass}`)} {...field} value={field.value ?? ''} onChange={e => { const v = e.target.value; const next = Number(v); field.onChange(v === '' ? 5 : Math.min(60, Math.max(1, Number.isNaN(next) ? 5 : next))) }} /></FormControl>
                      </div>
                      <FormDescription className="mt-3">How long StreamNZB waits for the initial playback probe/open before failing over to the next release. Higher values reduce false startup timeouts but delay failover.</FormDescription>
                      <FormMessage />
                    </FormItem>
                  )} />
                  <FormField control={control} name="failover_fast_mode" render={({ field }) => (
                    <FormItem className="relative rounded-none border-0 p-3">
                      <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                      <div className={stackedFieldRowClass}>
                        <div className="sm:flex-1">
                          <FormLabel className={labelClass}>Failover fast mode</FormLabel>
                        </div>
                        <FormControl>
                          <Switch
                            checked={field.value === true}
                            onCheckedChange={field.onChange}
                            className={showUnsavedHighlights && formState.dirtyFields?.failover_fast_mode ? 'ring-2 ring-destructive ring-offset-2 ring-offset-background' : ''}
                          />
                        </FormControl>
                      </div>
                      <FormDescription className="mt-3">
                        When enabled, failover prioritizes faster startup and may skip deeper checks.
                        When disabled, failover spends longer exhausting all options before moving on.
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
                  <div className="shrink-0">{renderSaveButton('availnzb')}</div>
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
                            onCheckedChange={(checked) => field.onChange(checked ? 'on' : 'off')}
                            className={showUnsavedHighlights && formState.dirtyFields?.availnzb_mode ? 'ring-2 ring-destructive ring-offset-2 ring-offset-background' : ''}
                          />
                        </FormControl>
                      </div>
                      <FormDescription className="mt-3">Controls whether StreamNZB uses AvailNZB. API key management is automatic.</FormDescription>
                      <FormMessage />
                    </FormItem>
                  )} />
                  <FormField control={control} name="availnzb_filter_reported_bad" render={({ field }) => (
                    <FormItem className="relative rounded-none border-0 p-3">
                      <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                      <div className={stackedFieldRowClass}>
                        <div className="sm:flex-1">
                          <FormLabel className={labelClass}>Filter reported bad releases</FormLabel>
                        </div>
                        <FormControl>
                          <Switch
                            checked={availNZBModeEnabled && field.value === true}
                            onCheckedChange={(checked) => field.onChange(checked === true)}
                            disabled={!availNZBModeEnabled}
                            className={showUnsavedHighlights && formState.dirtyFields?.availnzb_filter_reported_bad ? 'ring-2 ring-destructive ring-offset-2 ring-offset-background' : ''}
                          />
                        </FormControl>
                      </div>
                      <FormDescription className="mt-3">
                        {availNZBModeEnabled
                          ? 'When enabled, releases reported as bad by AvailNZB are removed from returned streams.'
                          : 'Enable AvailNZB mode to control reported-bad filtering.'}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )} />
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
                  <div className="shrink-0">{renderSaveButton('memory')}</div>
                </div>
              </CardHeader>
              <CardContent className="space-y-3">
                <div className="rounded-md border border-border/60">
                  <FormField control={control} name="memory_limit_mb" render={({ field }) => (
                    <FormItem className="rounded-none border-0 p-3">
                      <div className={stackedFieldRowClass}>
                        <FormLabel className={cn(labelClass, 'sm:flex-1')}>Memory limit (MB)</FormLabel>
                        <FormControl><Input type="number" min={0} className={fieldClassName('memory_limit_mb', `h-9 ${controlMediumClass}`)} {...field} value={field.value ?? ''} onChange={e => { const v = e.target.value; field.onChange(v === '' ? 0 : Number(v) || 0) }} /></FormControl>
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

        <Card>
          <CardHeader>
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0 flex-1 max-w-[34rem] space-y-0.5">
                <CardTitle>Metadata APIs</CardTitle>
                <CardDescription>Optional API keys for metadata enrichment during search and matching. Built-in defaults are available, but using your own keys is recommended.</CardDescription>
              </div>
              <div className="shrink-0">{renderSaveButton('metadata')}</div>
            </div>
          </CardHeader>
          <CardContent>
            <div className="rounded-md border border-border/60">
              <FormField control={control} name="tmdb_api_key" render={({ field }) => (
                <FormItem className="rounded-none border-0 p-3">
                  <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                    <FormLabel className="min-w-0 text-sm font-medium xl:flex-1 flex items-center gap-1.5">TMDB Read Access Token <EnvOverrideIndicator show={envOverrides.includes('tmdb_api_key')} /></FormLabel>
                    <FormControl><div className="w-full xl:max-w-3xl"><PasswordInput className={fieldClassName('tmdb_api_key', 'h-9 w-full font-mono text-xs')} {...field} value={field.value || ''} /></div></FormControl>
                  </div>
                  <FormDescription className="mt-3">Used for localized titles, year enrichment, and text-based movie/show name resolution. Without it, text-search metadata is limited and some requests fall back to ID-only behavior.</FormDescription>
                  <FormMessage />
                </FormItem>
              )} />
              <FormField control={control} name="tvdb_api_key" render={({ field }) => (
                <FormItem className="relative rounded-none border-0 p-3">
                  <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                  <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                    <FormLabel className="min-w-0 text-sm font-medium xl:flex-1 flex items-center gap-1.5">TVDB API Key <EnvOverrideIndicator show={envOverrides.includes('tvdb_api_key')} /></FormLabel>
                    <FormControl><div className="w-full xl:max-w-3xl"><PasswordInput className={fieldClassName('tvdb_api_key', 'h-9 w-full font-mono text-xs')} {...field} value={field.value || ''} /></div></FormControl>
                  </div>
                  <FormDescription className="mt-3">Used primarily for series metadata ID resolution. When available, StreamNZB can resolve TVDB IDs directly before falling back to TMDB-based lookup.</FormDescription>
                  <FormMessage />
                </FormItem>
              )} />
            </div>
          </CardContent>
        </Card>

        <ConfirmDialog
          open={showRestartConfirm}
          onOpenChange={setShowRestartConfirm}
          title="Restart required"
          description="Changing the memory limit requires a StreamNZB restart. Do you want to save this change now?"
          confirmLabel="Save"
          confirmVariant="destructive"
          onConfirm={() => {
            setShowRestartConfirm(false)
            setShowUnsavedHighlights(false)
            void saveCard('memory')
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
        <ConfirmDialog
          open={showDiscardConfirm}
          onOpenChange={(nextOpen) => {
            setShowDiscardConfirm(nextOpen)
            if (!nextOpen) {
              setPendingTabChange('')
              if (dirtyRef.current) setShowUnsavedHighlights(true)
            }
          }}
          title="Discard advanced changes?"
          description="Your unsaved changes in the Advanced tab will be lost."
          confirmLabel="Discard"
          onConfirm={() => {
            const nextTab = pendingTabChange
            setShowDiscardConfirm(false)
            setPendingTabChange('')
            reset(lastSavedValues)
            dirtyRef.current = false
            onDirtyChange?.(false)
            setShowUnsavedHighlights(false)
            if (nextTab) onProceedTabChange(nextTab)
          }}
        />
      </form>
    </Form>
  )
})
