import React, { forwardRef, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react'
import { useForm, useWatch } from 'react-hook-form'
import { AlertTriangle, Loader2, Save } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { Form, FormField, FormItem, FormLabel, FormControl, FormMessage, FormDescription } from "@/components/ui/form"
import { Switch } from "@/components/ui/switch"
import { PasswordInput } from "@/components/ui/password-input"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { cn } from "@/lib/utils"

const CARD_FIELDS = {
  addon: ['addon_base_url', 'addon_port'],
  proxy: ['proxy_enabled', 'proxy_host', 'proxy_port', 'proxy_auth_user', 'proxy_auth_pass'],
  useragent: ['indexer_query_header', 'indexer_grab_header', 'provider_header'],
}

function pickInitialValues(values = {}) {
  return {
    addon_port: Number(values.addon_port ?? 7000),
    addon_base_url: values.addon_base_url ?? '',
    proxy_enabled: values.proxy_enabled !== false,
    proxy_port: Number(values.proxy_port ?? 119),
    proxy_host: values.proxy_host ?? '',
    proxy_auth_user: values.proxy_auth_user ?? '',
    proxy_auth_pass: values.proxy_auth_pass ?? '',
    indexer_query_header: values.indexer_query_header ?? '',
    indexer_grab_header: values.indexer_grab_header ?? '',
    provider_header: values.provider_header ?? '',
  }
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

export const NetworkSettingsSection = forwardRef(function NetworkSettingsSection({
  initialValues,
  envOverrides,
  isSaving,
  onPersist,
  onDirtyChange,
  onProceedTabChange,
}, ref) {
  const defaults = useMemo(() => pickInitialValues(initialValues), [initialValues])
  const [lastSavedValues, setLastSavedValues] = useState(defaults)
  const [savingCard, setSavingCard] = useState('')
  const [showRestartConfirm, setShowRestartConfirm] = useState(false)
  const [restartTarget, setRestartTarget] = useState('')
  const [showDiscardConfirm, setShowDiscardConfirm] = useState(false)
  const [pendingTabChange, setPendingTabChange] = useState('')
  const [showUnsavedHighlights, setShowUnsavedHighlights] = useState(false)
  const dirtyRef = useRef(false)

  const form = useForm({ defaultValues: defaults })
  const { control, handleSubmit, reset, getValues, formState } = form
  const watchedValues = useWatch({ control })
  const proxyEnabled = form.watch('proxy_enabled') !== false

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
    } finally {
      setSavingCard('')
    }
  }

  const handleCardSave = (cardId) => handleSubmit(async () => {
    if (cardId === 'addon' && formState.dirtyFields?.addon_port) {
      setRestartTarget('addon')
      setShowRestartConfirm(true)
      return
    }
    if (cardId === 'proxy' && formState.dirtyFields?.proxy_port) {
      setRestartTarget('proxy')
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

  return (
    <Form {...form}>
      <form className="space-y-6">
        <div className="grid grid-cols-1 gap-6 2xl:grid-cols-2">
          <Card>
            <CardHeader>
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0 flex-1 max-w-[26rem] space-y-0.5">
                  <CardTitle>Addon</CardTitle>
                  <CardDescription>Configure how the Stremio addon listens and is accessed.</CardDescription>
                </div>
                <div className="shrink-0">{renderSaveButton('addon')}</div>
              </div>
            </CardHeader>
            <CardContent>
              <div className="rounded-md border border-border/60">
                <FormField control={control} name="addon_base_url" render={({ field }) => (
                  <FormItem className="rounded-none border-0 p-3">
                    <div className="flex items-center justify-between gap-4">
                      <FormLabel className="text-sm font-medium">Base URL</FormLabel>
                      <FormControl><Input placeholder="http://localhost:7000" className={fieldClassName('addon_base_url', 'h-9 w-64')} {...field} /></FormControl>
                    </div>
                    <FormDescription className="mt-3">The public base URL clients use to reach your StreamNZB addon.</FormDescription>
                    <FormMessage />
                    <EnvOverrideNote show={envOverrides.includes('addon_base_url')} />
                  </FormItem>
                )} />
                <FormField control={control} name="addon_port" render={({ field }) => (
                  <FormItem className="relative rounded-none border-0 p-3">
                    <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                    <div className="flex items-center justify-between gap-4">
                      <FormLabel className="text-sm font-medium">Port</FormLabel>
                      <FormControl><Input type="number" className={fieldClassName('addon_port', 'h-9 w-28')} {...field} onChange={e => field.onChange(e.target.valueAsNumber)} /></FormControl>
                    </div>
                    <FormDescription className="mt-3">The local port where the addon server listens.</FormDescription>
                    <FormMessage />
                    <EnvOverrideNote show={envOverrides.includes('addon_port')} />
                  </FormItem>
                )} />
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0 flex-1 max-w-[26rem] space-y-0.5">
                  <CardTitle>NNTP Proxy Server</CardTitle>
                  <CardDescription>Allow other apps (SABnzbd, NZBGet) to use StreamNZB as a localized news server.</CardDescription>
                </div>
                <div className="shrink-0">{renderSaveButton('proxy')}</div>
              </div>
              {(envOverrides.includes('proxy_port') || envOverrides.includes('proxy_host') || envOverrides.includes('proxy_enabled') || envOverrides.includes('proxy_auth_user') || envOverrides.includes('proxy_auth_pass')) && (
                <p className="text-xs text-muted-foreground flex items-center gap-1 mt-1">
                  <AlertTriangle className="h-3.5 w-3 shrink-0" />
                  Some settings overwritten by environment variables (NNTP_PROXY_*) on restart.
                </p>
              )}
            </CardHeader>
            <CardContent>
              <div className="mb-4">
                <FormField control={control} name="proxy_enabled" render={({ field }) => (
                  <FormItem className="rounded-md border border-border/60 p-3">
                    <div className="flex items-center justify-between gap-4">
                      <FormLabel className="text-sm font-medium">Enable NNTP Proxy</FormLabel>
                      <FormControl><Switch checked={field.value !== false} onCheckedChange={field.onChange} className={showUnsavedHighlights && formState.dirtyFields?.proxy_enabled ? 'ring-2 ring-destructive ring-offset-2 ring-offset-background' : ''} /></FormControl>
                    </div>
                    <FormDescription className="mt-3">Turn the local NNTP proxy server on or off.</FormDescription>
                  </FormItem>
                )} />
              </div>
              <div className="rounded-md border border-border/60">
                <FormField control={control} name="proxy_host" render={({ field }) => (
                  <FormItem className="rounded-none border-0 p-3">
                    <div className="flex items-center justify-between gap-4">
                      <FormLabel className="text-sm font-medium">Bind Host</FormLabel>
                      <FormControl><Input placeholder="0.0.0.0" disabled={!proxyEnabled} className={fieldClassName('proxy_host', 'h-9 w-40')} {...field} /></FormControl>
                    </div>
                    <FormDescription className="mt-3">Which local address the proxy server should bind to.</FormDescription>
                    <FormMessage />
                  </FormItem>
                )} />
                <FormField control={control} name="proxy_port" render={({ field }) => (
                  <FormItem className="relative rounded-none border-0 p-3">
                    <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                    <div className="flex items-center justify-between gap-4">
                      <FormLabel className="text-sm font-medium">Port</FormLabel>
                      <FormControl><Input type="number" disabled={!proxyEnabled} className={fieldClassName('proxy_port', 'h-9 w-28')} {...field} onChange={e => field.onChange(e.target.valueAsNumber)} /></FormControl>
                    </div>
                    <FormDescription className="mt-3">The port other apps use when connecting to the local NNTP proxy.</FormDescription>
                    <FormMessage />
                  </FormItem>
                )} />
                <FormField control={control} name="proxy_auth_user" render={({ field }) => (
                  <FormItem className="relative rounded-none border-0 p-3">
                    <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                    <div className="flex items-center justify-between gap-4">
                      <FormLabel className="text-sm font-medium">Proxy Username</FormLabel>
                      <FormControl><Input disabled={!proxyEnabled} className={fieldClassName('proxy_auth_user', 'h-9 w-40')} {...field} /></FormControl>
                    </div>
                    <FormDescription className="mt-3">Optional username clients must provide when using the proxy.</FormDescription>
                    <FormMessage />
                  </FormItem>
                )} />
                <FormField control={control} name="proxy_auth_pass" render={({ field }) => (
                  <FormItem className="relative rounded-none border-0 p-3">
                    <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                    <div className="flex items-center justify-between gap-4">
                      <FormLabel className="text-sm font-medium">Proxy Password</FormLabel>
                      <FormControl><PasswordInput disabled={!proxyEnabled} className={fieldClassName('proxy_auth_pass', 'h-9 w-40')} {...field} /></FormControl>
                    </div>
                    <FormDescription className="mt-3">Optional password clients must provide when using the proxy.</FormDescription>
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
                <CardTitle>User-Agent</CardTitle>
                <CardDescription>Custom User-Agent headers for indexer queries, NZB grabs, and provider-facing requests.</CardDescription>
              </div>
              <div className="shrink-0">{renderSaveButton('useragent')}</div>
            </div>
          </CardHeader>
          <CardContent>
            <div className="rounded-md border border-border/60">
              <FormField control={control} name="indexer_query_header" render={({ field }) => (
                <FormItem className="rounded-none border-0 p-3">
                  <div className="flex items-center justify-between gap-4">
                    <FormLabel className="text-sm font-medium">Indexer Query Header</FormLabel>
                    <FormControl><Input className={fieldClassName('indexer_query_header', 'h-9 w-64')} {...field} value={field.value || ''} placeholder="Prowlarr/2.3.0.5236" /></FormControl>
                  </div>
                  <FormDescription className="mt-3">Used for indexer search and capability requests.</FormDescription>
                  <FormMessage />
                  <EnvOverrideNote show={envOverrides.includes('indexer_query_header')} />
                </FormItem>
              )} />
              <FormField control={control} name="indexer_grab_header" render={({ field }) => (
                <FormItem className="relative rounded-none border-0 p-3">
                  <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                  <div className="flex items-center justify-between gap-4">
                    <FormLabel className="text-sm font-medium">Indexer Grab Header</FormLabel>
                    <FormControl><Input className={fieldClassName('indexer_grab_header', 'h-9 w-64')} {...field} value={field.value || ''} placeholder="SABnzbd/4.5.5" /></FormControl>
                  </div>
                  <FormDescription className="mt-3">Used when grabbing NZBs from indexers.</FormDescription>
                  <FormMessage />
                  <EnvOverrideNote show={envOverrides.includes('indexer_grab_header')} />
                </FormItem>
              )} />
              <FormField control={control} name="provider_header" render={({ field }) => (
                <FormItem className="relative rounded-none border-0 p-3">
                  <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                  <div className="flex items-center justify-between gap-4">
                    <FormLabel className="text-sm font-medium">Provider Header</FormLabel>
                    <FormControl><Input className={fieldClassName('provider_header', 'h-9 w-64')} {...field} value={field.value || ''} placeholder="VLC/1.2.3.4" /></FormControl>
                  </div>
                  <FormDescription className="mt-3">Custom provider-facing User-Agent header.</FormDescription>
                  <FormMessage />
                  <EnvOverrideNote show={envOverrides.includes('provider_header')} />
                </FormItem>
              )} />
            </div>
          </CardContent>
        </Card>

        <ConfirmDialog
          open={showRestartConfirm}
          onOpenChange={(nextOpen) => {
            setShowRestartConfirm(nextOpen)
            if (!nextOpen) setRestartTarget('')
          }}
          title="Restart required"
          description={restartTarget === 'proxy'
            ? 'Changing the NNTP proxy port requires a StreamNZB restart. Do you want to save this change now?'
            : 'Changing the addon port requires a StreamNZB restart. Do you want to save this change now?'}
          confirmLabel="Save"
          confirmVariant="destructive"
          onConfirm={() => {
            const target = restartTarget || 'addon'
            setShowRestartConfirm(false)
            setRestartTarget('')
            setShowUnsavedHighlights(false)
            void saveCard(target)
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
          title="Discard network changes?"
          description="Your unsaved changes in the Network tab will be lost."
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
