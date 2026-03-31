import React, { forwardRef, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react'
import { useForm, useWatch } from 'react-hook-form'
import { Loader2, Info, AlertTriangle, Eye, EyeOff, Copy, Check, Lock, LockOpen, Save, Paintbrush } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { Form, FormField, FormItem, FormLabel, FormControl, FormMessage, FormDescription } from "@/components/ui/form"
import { PasswordInput } from "@/components/ui/password-input"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { apiFetch } from "@/api"
import { cn } from "@/lib/utils"

const CARD_FIELDS = {
  admin: ['log_level', 'keep_log_files'],
  memory: ['memory_limit_mb'],
  availnzb: ['availnzb_api_key', 'availnzb_mode'],
  metadata: ['tmdb_api_key', 'tvdb_api_key'],
}

function pickInitialValues(values = {}) {
  return {
    log_level: values.log_level ?? 'INFO',
    keep_log_files: Number(values.keep_log_files ?? 9) || 9,
    memory_limit_mb: Number(values.memory_limit_mb ?? 512),
    availnzb_api_key: values.availnzb_api_key ?? '',
    availnzb_mode: values.availnzb_mode ?? '',
    tmdb_api_key: values.tmdb_api_key ?? '',
    tvdb_api_key: values.tvdb_api_key ?? '',
  }
}

function shouldUseManualAvailNZBKeyOverride(values) {
  return Boolean(String(values?.availnzb_api_key || '').trim())
}

function EnvOverrideNote({ show }) {
  if (!show) return null
  return (
    <p className="text-xs text-muted-foreground flex items-center gap-1 mt-1">
      <AlertTriangle className="h-3.5 w-3 shrink-0" />
      Overwritten by environment variable on restart.
    </p>
  )
}

export const AdvancedSettingsSection = forwardRef(function AdvancedSettingsSection({
  initialValues,
  envOverrides,
  isSaving,
  onPersist,
  onClearCache,
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
  const [showRecoverySecret, setShowRecoverySecret] = useState(false)
  const [recoverySecretCopied, setRecoverySecretCopied] = useState(false)
  const [useManualAvailNZBKeyOverride, setUseManualAvailNZBKeyOverride] = useState(shouldUseManualAvailNZBKeyOverride(defaults))
  const [showDiscardConfirm, setShowDiscardConfirm] = useState(false)
  const [pendingTabChange, setPendingTabChange] = useState('')
  const [showUnsavedHighlights, setShowUnsavedHighlights] = useState(false)
  const [availNZBStatus, setAvailNZBStatus] = useState(null)
  const [availNZBStatusLoading, setAvailNZBStatusLoading] = useState(false)
  const [availNZBStatusError, setAvailNZBStatusError] = useState('')
  const dirtyRef = useRef(false)

  const form = useForm({ defaultValues: defaults })
  const { control, handleSubmit, reset, getValues, formState, setValue } = form
  const watchedValues = useWatch({ control })

  useEffect(() => {
    const currentValues = pickInitialValues(watchedValues)
    dirtyRef.current = JSON.stringify(currentValues) !== JSON.stringify(lastSavedValues)
    onDirtyChange?.(dirtyRef.current)
  }, [lastSavedValues, onDirtyChange, watchedValues])

  useEffect(() => {
    reset(defaults)
    setLastSavedValues(defaults)
    setUseManualAvailNZBKeyOverride(shouldUseManualAvailNZBKeyOverride(defaults))
    dirtyRef.current = false
    onDirtyChange?.(false)
  }, [defaults, onDirtyChange, reset])

  useEffect(() => {
    let cancelled = false

    const fetchAvailNZBStatus = async () => {
      setAvailNZBStatusLoading(true)
      setAvailNZBStatusError('')
      try {
        const data = await apiFetch('/api/availnzb/status')
        if (cancelled) return
        setAvailNZBStatus(data || null)
      } catch (error) {
        if (cancelled) return
        setAvailNZBStatus(null)
        setAvailNZBStatusError(error.message || 'Failed to load AvailNZB key status.')
      } finally {
        if (!cancelled) {
          setAvailNZBStatusLoading(false)
        }
      }
    }

    fetchAvailNZBStatus()

    return () => {
      cancelled = true
    }
  }, [])

  useImperativeHandle(ref, () => ({
    hasUnsavedChanges() {
      return dirtyRef.current
    },
    discardChanges() {
      reset(lastSavedValues)
      setUseManualAvailNZBKeyOverride(shouldUseManualAvailNZBKeyOverride(lastSavedValues))
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
      if (cardId === 'availnzb' && !useManualAvailNZBKeyOverride) {
        payload.availnzb_api_key = ''
      }
      await onPersist(payload, cardId)
      const nextValues = { ...lastSavedValues, ...payload }
      setLastSavedValues(nextValues)
      reset(nextValues)
      setUseManualAvailNZBKeyOverride(shouldUseManualAvailNZBKeyOverride(nextValues))
      dirtyRef.current = false
      onDirtyChange?.(false)
      setShowUnsavedHighlights(false)
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

  const handleCopyRecoverySecret = async () => {
    const recoverySecret = availNZBStatus?.recovery_secret?.trim?.() || ''
    if (!recoverySecret || !navigator?.clipboard?.writeText) return
    try {
      await navigator.clipboard.writeText(recoverySecret)
      setRecoverySecretCopied(true)
      window.setTimeout(() => setRecoverySecretCopied(false), 1500)
    } catch {
      // ignore clipboard failures
    }
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

  const recoverySecret = availNZBStatus?.recovery_secret || ''
  const availNZBStatusMessage = availNZBStatusError || availNZBStatus?.status_error || ''
  const availNZBTrustScore = Number(availNZBStatus?.status?.trust_score)
  const availNZBInfoClass = Number.isFinite(availNZBTrustScore) && availNZBTrustScore < 100
    ? 'text-red-600 dark:text-red-300'
    : 'text-muted-foreground'

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
                <div className="rounded-md border border-border/60">
                  <FormField control={control} name="log_level" render={({ field }) => (
                    <FormItem className="rounded-none border-0 p-3">
                      <div className="flex items-center justify-between gap-4">
                        <FormLabel className="text-sm font-medium">Log Level</FormLabel>
                        <FormControl>
                          <select className={fieldClassName('log_level', 'flex h-9 w-40 rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2')} {...field}>
                            <option value="DEBUG">DEBUG</option>
                            <option value="INFO">INFO</option>
                            <option value="WARN">WARN</option>
                            <option value="ERROR">ERROR</option>
                          </select>
                        </FormControl>
                      </div>
                      <FormDescription className="mt-3">Controls how verbose StreamNZB logging should be.</FormDescription>
                      <FormMessage />
                      <EnvOverrideNote show={envOverrides.includes('log_level')} />
                    </FormItem>
                  )} />
                  <FormField control={control} name="keep_log_files" render={({ field }) => (
                    <FormItem className="relative rounded-none border-0 p-3">
                      <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                      <div className="flex items-center justify-between gap-4">
                        <FormLabel className="text-sm font-medium">Keep log files</FormLabel>
                        <FormControl><Input type="number" min={1} max={50} className={fieldClassName('keep_log_files', 'h-9 w-28')} {...field} value={field.value ?? ''} onChange={e => { const v = e.target.value; field.onChange(v === '' ? 9 : Math.min(50, Math.max(1, Number(v) || 9))) }} /></FormControl>
                      </div>
                      <FormDescription className="mt-3">Number of log files to keep. Oldest rotated logs are purged on restart.</FormDescription>
                      <EnvOverrideNote show={envOverrides.includes('keep_log_files')} />
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
                      <div className="flex items-center justify-between gap-4">
                        <FormLabel className="text-sm font-medium">Memory limit (MB)</FormLabel>
                        <FormControl><Input type="number" min={0} className={fieldClassName('memory_limit_mb', 'h-9 w-28')} {...field} value={field.value ?? ''} onChange={e => { const v = e.target.value; field.onChange(v === '' ? 0 : Number(v) || 0) }} /></FormControl>
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
                <FormField control={control} name="availnzb_api_key" render={({ field }) => (
                  <FormItem className="rounded-none border-0 p-3">
                    <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                      <div className="min-w-0 xl:flex-1">
                        <div className="flex items-center gap-2">
                          <FormLabel className="min-w-0 text-sm font-medium">AvailNZB API Key</FormLabel>
                          <TooltipProvider delayDuration={100}>
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <Button
                                  type="button"
                                  variant="outline"
                                  size="icon"
                                  className="h-7 w-7 shrink-0"
                                  onClick={() => {
                                    if (useManualAvailNZBKeyOverride) {
                                      setUseManualAvailNZBKeyOverride(false)
                                      setValue('availnzb_api_key', '', { shouldDirty: true })
                                      return
                                    }
                                    setShowUnlockConfirm(true)
                                  }}
                                >
                                  {useManualAvailNZBKeyOverride ? <LockOpen className="h-3.5 w-3.5" /> : <Lock className="h-3.5 w-3.5" />}
                                </Button>
                              </TooltipTrigger>
                              <TooltipContent>
                                {useManualAvailNZBKeyOverride ? 'Use automatic key management' : 'Unlock manual override'}
                              </TooltipContent>
                            </Tooltip>
                          </TooltipProvider>
                        </div>
                      </div>
                      <FormControl>
                        <div className="w-full xl:max-w-3xl">
                          {useManualAvailNZBKeyOverride ? (
                            <PasswordInput className={fieldClassName('availnzb_api_key', 'h-9 w-full font-mono text-xs')} {...field} value={field.value || ''} />
                          ) : (
                            <Input value={availNZBStatus?.status?.name ? 'Automatically managed' : 'Managed automatically'} readOnly disabled className="h-9 w-full font-mono text-xs" />
                          )}
                        </div>
                      </FormControl>
                    </div>
                    <FormDescription className="mt-3">StreamNZB can manage this key automatically. Use a manual override only if you want to force a specific AvailNZB API key.</FormDescription>
                    <FormMessage />
                    <EnvOverrideNote show={envOverrides.includes('availnzb_api_key')} />
                  </FormItem>
                )} />
                <FormField control={control} name="availnzb_mode" render={({ field }) => (
                  <FormItem className="relative rounded-none border-0 p-3">
                    <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                    <div className="flex items-center justify-between gap-4">
                      <FormLabel className="flex items-center gap-2 text-sm font-medium">
                        <span>AvailNZB mode</span>
                        <TooltipProvider delayDuration={150}>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <span className={cn("inline-flex cursor-help items-center justify-center", availNZBInfoClass)}>
                                <span className="sr-only">AvailNZB scoring info</span>
                                <Info className="h-4 w-4" />
                              </span>
                            </TooltipTrigger>
                            <TooltipContent side="bottom" align="start" className="max-w-xs p-3">
                              <div className="space-y-1 text-xs font-normal">
                                <div className="font-medium">{availNZBStatusMessage ? 'AvailNZB status' : 'AvailNZB scoring'}</div>
                                <div>Only "GET status + POST report" increases your AvailNZB score.</div>
                                <div>"GET status only" fetches availability data but does not report your playback results back to the community.</div>
                                <div>"Disabled" skips AvailNZB entirely.</div>
                              </div>
                            </TooltipContent>
                          </Tooltip>
                        </TooltipProvider>
                      </FormLabel>
                      <FormControl>
                        <select className={fieldClassName('availnzb_mode', 'flex h-9 w-56 rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2')} {...field}>
                          <option value="">GET status + POST report</option>
                          <option value="status_only">GET status only</option>
                          <option value="disabled">Disabled</option>
                        </select>
                      </FormControl>
                    </div>
                    <FormDescription className="mt-3">Controls how StreamNZB interacts with AvailNZB.</FormDescription>
                    {recoverySecret && (
                      <div className="rounded-md border border-border bg-muted/30 p-3 space-y-2">
                        <div className="flex items-center justify-between gap-2">
                          <span className="text-sm font-medium">Recovery secret</span>
                          <span className="text-xs text-muted-foreground">{recoverySecretCopied ? 'Copied' : 'Reveal to copy'}</span>
                        </div>
                        <div className="flex items-center gap-2">
                          <Input type={showRecoverySecret ? 'text' : 'password'} value={recoverySecret} readOnly className="font-mono" />
                          <Button type="button" variant="outline" size="icon" aria-label={showRecoverySecret ? 'Hide AvailNZB recovery secret' : 'Show AvailNZB recovery secret'} onClick={() => setShowRecoverySecret((current) => !current)}>
                            {showRecoverySecret ? <EyeOff /> : <Eye />}
                          </Button>
                          <Button type="button" variant="outline" size="icon" aria-label="Copy AvailNZB recovery secret" onClick={handleCopyRecoverySecret}>
                            {recoverySecretCopied ? <Check /> : <Copy />}
                          </Button>
                        </div>
                        <p className="text-xs text-muted-foreground">Save this recovery secret somewhere safe. You can use it to recover the AvailNZB app key later.</p>
                      </div>
                    )}
                    <FormMessage />
                  </FormItem>
                )} />
              </div>
            </CardContent>
          </Card>
        </div>

        <Card>
          <CardHeader>
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0 flex-1 max-w-[34rem] space-y-0.5">
                <CardTitle>Metadata APIs</CardTitle>
                <CardDescription>API keys for metadata enrichment used during search and matching.</CardDescription>
              </div>
              <div className="shrink-0">{renderSaveButton('metadata')}</div>
            </div>
          </CardHeader>
          <CardContent>
            <div className="rounded-md border border-border/60">
              <FormField control={control} name="tmdb_api_key" render={({ field }) => (
                <FormItem className="rounded-none border-0 p-3">
                  <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                    <FormLabel className="min-w-0 text-sm font-medium xl:flex-1">TMDB Read Access Token</FormLabel>
                    <FormControl><div className="w-full xl:max-w-3xl"><PasswordInput className={fieldClassName('tmdb_api_key', 'h-9 w-full font-mono text-xs')} {...field} value={field.value || ''} /></div></FormControl>
                  </div>
                  <FormDescription className="mt-3">Used for localized titles, year enrichment, and text-based movie/show name resolution. Without it, text-search metadata is limited and some requests fall back to ID-only behavior.</FormDescription>
                  <FormMessage />
                  <EnvOverrideNote show={envOverrides.includes('tmdb_api_key')} />
                </FormItem>
              )} />
              <FormField control={control} name="tvdb_api_key" render={({ field }) => (
                <FormItem className="relative rounded-none border-0 p-3">
                  <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                  <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                    <FormLabel className="min-w-0 text-sm font-medium xl:flex-1">TVDB API Key</FormLabel>
                    <FormControl><div className="w-full xl:max-w-3xl"><PasswordInput className={fieldClassName('tvdb_api_key', 'h-9 w-full font-mono text-xs')} {...field} value={field.value || ''} /></div></FormControl>
                  </div>
                  <FormDescription className="mt-3">Used primarily for series metadata ID resolution. When available, StreamNZB can resolve TVDB IDs directly before falling back to TMDB-based lookup.</FormDescription>
                  <FormMessage />
                  <EnvOverrideNote show={envOverrides.includes('tvdb_api_key')} />
                </FormItem>
              )} />
            </div>
          </CardContent>
        </Card>

        <ConfirmDialog
          open={showUnlockConfirm}
          onOpenChange={setShowUnlockConfirm}
          title="Unlock manual override?"
          description="Do you really want to override the automatically managed AvailNZB key with a manual key?"
          confirmLabel="Unlock"
          confirmVariant="destructive"
          onConfirm={() => {
            setShowUnlockConfirm(false)
            setUseManualAvailNZBKeyOverride(true)
            setShowUnsavedHighlights(true)
          }}
        />
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
            setUseManualAvailNZBKeyOverride(shouldUseManualAvailNZBKeyOverride(lastSavedValues))
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
