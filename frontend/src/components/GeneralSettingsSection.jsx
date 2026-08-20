import React, { useEffect, useMemo, useState } from 'react'
import { useForm, useWatch } from 'react-hook-form'
import { Loader2, RefreshCw } from "lucide-react"
import { apiFetch } from '@/api'
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { Form, FormField, FormItem, FormLabel, FormControl, FormMessage, FormDescription } from "@/components/ui/form"
import { Switch } from "@/components/ui/switch"
import { PasswordInput } from "@/components/ui/password-input"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { EnvOverrideIndicator } from "@/components/EnvOverrideIndicator"
import { useFieldAutoSave } from '@/hooks/useFieldAutoSave'
import { cn } from "@/lib/utils"

// The TMDB/TVDB API keys live on the Metadata page, next to what they power.
const CARD_FIELDS = {
  addon: ['addon_base_url', 'addon_port'],
  proxy: ['proxy_enabled', 'proxy_host', 'proxy_port', 'proxy_auth_user', 'proxy_auth_pass'],
  useragent: ['indexer_query_header', 'indexer_grab_header', 'provider_header'],
  database: ['database_driver', 'database_url', 'nzb_history_retention_days'],
}

const FIELD_CARD = Object.fromEntries(
  Object.entries(CARD_FIELDS).flatMap(([card, fields]) => fields.map((field) => [field, card]))
)

// Which slot each header fills, for /api/useragents/latest. The role only
// decides what an empty field is seeded with — a field that already names a
// tool keeps that tool and is only lifted to its current version.
const USER_AGENT_ROLES = {
  indexer_query_header: 'query',
  indexer_grab_header: 'grab',
  provider_header: 'provider',
}

// productToken pulls "Prowlarr" out of "Prowlarr/2.3.0.5236 (windows 10.0)".
const productToken = (value) => String(value || '').trim().split('/')[0].trim()

// Every setting here is applied without a restart, so nothing is gated on one.
// The addon port still warns: rebinding drops the connection this page is
// served over, so the browser has to be reopened on the new port.
const CONFIRM_FIELDS = {
  addon_port: {
    title: 'Reopen the browser after this change',
    description: 'The addon rebinds to the new port immediately, which closes this page’s connection. You will need to reopen StreamNZB on the new port to keep using it. Save the change?',
    confirmLabel: 'Save and rebind',
  },
}

function pickInitialValues(values = {}) {
  const parsedRetentionDays = values.nzb_history_retention_days == null
    ? 90
    : Number(values.nzb_history_retention_days)
  return {
    addon_port: Number(values.addon_port ?? 7000),
    addon_base_url: values.addon_base_url ?? '',
    proxy_enabled: values.proxy_enabled !== false,
    proxy_port: Number(values.proxy_port ?? 1119),
    proxy_host: values.proxy_host ?? '',
    proxy_auth_user: values.proxy_auth_user ?? '',
    proxy_auth_pass: values.proxy_auth_pass ?? '',
    indexer_query_header: values.indexer_query_header ?? '',
    indexer_grab_header: values.indexer_grab_header ?? '',
    provider_header: values.provider_header ?? '',
    database_driver: values.database_driver || 'sqlite',
    database_url: values.database_url ?? '',
    nzb_history_retention_days: Number.isFinite(parsedRetentionDays) ? parsedRetentionDays : 90,
  }
}

export const GeneralSettingsSection = React.memo(function GeneralSettingsSection({
  initialValues,
  envOverrides,
  proxyStatus,
  onPersist,
  saveStatus,
}) {
  const defaults = useMemo(() => pickInitialValues(initialValues), [initialValues])
  const [confirmField, setConfirmField] = useState('')
  const [agentRefresh, setAgentRefresh] = useState({ busy: false, message: '', error: false })

  const form = useForm({ defaultValues: defaults })
  const { control, reset, setError, clearErrors } = form
  const proxyEnabled = useWatch({ control, name: 'proxy_enabled' }) !== false
  const usingPostgres = useWatch({ control, name: 'database_driver' }) === 'postgres'
  const { saveField, saveFields, savingField, hasFieldChanged, revertField } = useFieldAutoSave({
    form,
    savedValues: defaults,
    onPersist,
  })

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

  const commitField = (name) => { void saveField(name, FIELD_CARD[name]) }
  // Fields with a confirmation go through the dialog instead of saving
  // straight away. Used by both blur (inputs) and change (selects).
  const commitGated = (name) => {
    if (CONFIRM_FIELDS[name]) {
      if (hasFieldChanged(name)) setConfirmField(name)
      return
    }
    commitField(name)
  }
  const blurCommit = (field, name) => () => {
    field.onBlur()
    commitGated(name)
  }

  // Postgres cannot be opened without a connection string, so selecting it is
  // not saved on its own — the pair is committed once the string is entered.
  // Selecting SQLite needs nothing else and saves straight away.
  const commitDatabaseDriver = () => {
    if (form.getValues('database_driver') === 'postgres') return
    void saveFields(['database_driver'], 'database')
  }
  const commitDatabaseURL = (field) => () => {
    field.onBlur()
    void saveFields(['database_driver', 'database_url'], 'database')
  }

  // Indexers are beginning to reject headers pinned to a version that has long
  // since shipped. Rather than making the user chase release pages, this looks
  // up what each configured tool is on today and lifts the header to it,
  // keeping whichever tool they picked. Env-set headers are left alone: the
  // process value wins regardless, so writing to config would only mislead.
  const refreshUserAgents = async () => {
    setAgentRefresh({ busy: true, message: '', error: false })
    try {
      const { agents = [], errors = [] } = await apiFetch('/api/useragents/latest') || {}
      const applied = []
      const unknown = []
      Object.entries(USER_AGENT_ROLES).forEach(([name, role]) => {
        if (envOverrides.includes(name)) return
        const current = form.getValues(name) || ''
        const token = productToken(current)
        const match = token
          ? agents.find((agent) => agent.product.toLowerCase() === token.toLowerCase())
          : agents.find((agent) => agent.role === role)
        if (!match) {
          if (token) unknown.push(token)
          return
        }
        if (current === match.user_agent) return
        form.setValue(name, match.user_agent, { shouldDirty: true })
        applied.push(match.user_agent)
      })
      if (applied.length) await saveFields(CARD_FIELDS.useragent, 'useragent')
      const notes = [applied.length ? `Updated to ${applied.join(', ')}.` : 'Already on the latest versions.']
      if (unknown.length) notes.push(`No release source for ${[...new Set(unknown)].join(', ')} — left unchanged.`)
      if (errors.length) notes.push(`Could not check ${errors.join('; ')}.`)
      setAgentRefresh({ busy: false, message: notes.join(' '), error: false })
    } catch (err) {
      setAgentRefresh({ busy: false, message: err?.message || 'Could not reach the release APIs.', error: true })
    }
  }

  const renderCardSpinner = (cardId) => (
    savingField && CARD_FIELDS[cardId].includes(savingField)
      ? <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
      : null
  )

  const stackedFieldRowClass = "flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between sm:gap-4"
  const controlWideClass = "w-full min-w-0 sm:max-w-xs"
  const controlMediumClass = "w-full min-w-0 sm:max-w-[10rem]"
  const labelClass = "min-w-0 text-sm font-medium"
  const controlSelectClass = "flex h-9 w-full min-w-0 max-w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 overflow-hidden text-ellipsis whitespace-nowrap sm:max-w-[14rem]"

  return (
    <Form {...form}>
      <form className="space-y-6">
        <div className="grid grid-cols-1 gap-6 2xl:grid-cols-2">
          {/* Stacked in one column so the short Addon card is not pulled to
              the height of the taller proxy card beside it. */}
          <div className="space-y-6">
            <Card>
              <CardHeader>
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0 flex-1 max-w-[26rem] space-y-0.5">
                    <CardTitle>Addon</CardTitle>
                    <CardDescription>Configure how the Stremio addon listens and is accessed. Changes save automatically.</CardDescription>
                  </div>
                  <div className="shrink-0">{renderCardSpinner('addon')}</div>
                </div>
              </CardHeader>
              <CardContent>
                <div className="rounded-md border border-border/60">
                  <FormField control={control} name="addon_base_url" render={({ field }) => (
                    <FormItem className="rounded-none border-0 p-3">
                      <div className={stackedFieldRowClass}>
                        <FormLabel className={cn(labelClass, 'flex items-center gap-1.5 sm:flex-1')}>Base URL <EnvOverrideIndicator show={envOverrides.includes('addon_base_url')} /></FormLabel>
                        <FormControl><Input placeholder="http://localhost:7000" className={`h-9 ${controlWideClass}`} {...field} onBlur={blurCommit(field, 'addon_base_url')} /></FormControl>
                      </div>
                      <FormDescription className="mt-3">The public base URL clients use to reach your StreamNZB addon.</FormDescription>
                      <FormMessage />
                    </FormItem>
                  )} />
                  <FormField control={control} name="addon_port" render={({ field }) => (
                    <FormItem className="relative rounded-none border-0 p-3">
                      <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                      <div className={stackedFieldRowClass}>
                        <FormLabel className={cn(labelClass, 'flex items-center gap-1.5 sm:flex-1')}>Port <EnvOverrideIndicator show={envOverrides.includes('addon_port')} /></FormLabel>
                        <FormControl><Input type="number" className={`h-9 ${controlMediumClass}`} {...field} onChange={e => field.onChange(e.target.valueAsNumber)} onBlur={blurCommit(field, 'addon_port')} /></FormControl>
                      </div>
                      <FormDescription className="mt-3">The local port where the addon server listens.</FormDescription>
                      <FormMessage />
                    </FormItem>
                  )} />
                </div>
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0 flex-1 max-w-[26rem] space-y-0.5">
                    <CardTitle>Database</CardTitle>
                    <CardDescription>Where the library, NZB history, and metrics are stored. Changes are applied without a restart.</CardDescription>
                  </div>
                  <div className="shrink-0">{renderCardSpinner('database')}</div>
                </div>
                {(envOverrides.includes('database_driver') || envOverrides.includes('database_url')) && (
                  <div className="mt-1">
                    <EnvOverrideIndicator show message="Some settings are overwritten by environment variables (DATABASE_*) on restart." />
                  </div>
                )}
              </CardHeader>
              <CardContent>
                <div className="rounded-md border border-border/60">
                  <FormField control={control} name="database_driver" render={({ field }) => (
                    <FormItem className="rounded-none border-0 p-3">
                      <div className={stackedFieldRowClass}>
                        <FormLabel className={cn(labelClass, 'flex items-center gap-1.5 sm:flex-1')}>Backend <EnvOverrideIndicator show={envOverrides.includes('database_driver')} /></FormLabel>
                        <FormControl>
                          <select className={controlSelectClass} {...field} onChange={(e) => { field.onChange(e); commitDatabaseDriver() }}>
                            <option value="sqlite">SQLite (default)</option>
                            <option value="postgres">Postgres</option>
                          </select>
                        </FormControl>
                      </div>
                      <FormDescription className="mt-3">
                        SQLite keeps everything in a local <code>streamnzb.db</code> and needs no setup. Switching to Postgres copies that file across, so your library and history come with you. Selecting Postgres saves once you fill in the connection string below.
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )} />
                  <FormField control={control} name="database_url" render={({ field }) => (
                    <FormItem className="relative rounded-none border-0 p-3">
                      <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                      <div className={stackedFieldRowClass}>
                        <FormLabel className={cn(labelClass, 'flex items-center gap-1.5 sm:flex-1')}>Connection String <EnvOverrideIndicator show={envOverrides.includes('database_url')} /></FormLabel>
                        <FormControl><Input placeholder="postgres://user:password@db-host:5432/streamnzb?sslmode=disable" disabled={!usingPostgres} className={`h-9 ${controlWideClass}`} {...field} value={field.value || ''} onBlur={commitDatabaseURL(field)} /></FormControl>
                      </div>
                      <FormDescription className="mt-3">
                        The password is hidden here. Editing this field replaces the whole connection string, so include the password when you change it. Saving verifies the server is reachable, then switches over without a restart.
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )} />
                  <FormField control={control} name="nzb_history_retention_days" render={({ field }) => (
                    <FormItem className="relative rounded-none border-0 p-3">
                      <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                      <div className={stackedFieldRowClass}>
                        <FormLabel className={cn(labelClass, 'sm:flex-1')}>NZB history retention (days)</FormLabel>
                        <FormControl><Input type="number" min={0} max={3650} className={`h-9 ${controlMediumClass}`} {...field} value={field.value ?? ''} onChange={e => { const v = e.target.value; const next = Number(v); field.onChange(v === '' ? 90 : Math.min(3650, Math.max(0, Number.isNaN(next) ? 90 : next))) }} onBlur={blurCommit(field, 'nzb_history_retention_days')} /></FormControl>
                      </div>
                      <FormDescription className="mt-3">Delete NZB history entries older than this many days on startup.</FormDescription>
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
                <div className="min-w-0 flex-1 max-w-[26rem] space-y-0.5">
                  <CardTitle>NNTP Proxy Server</CardTitle>
                  <CardDescription>Allow other apps (SABnzbd, NZBGet) to use StreamNZB as a localized news server.</CardDescription>
                </div>
                <div className="shrink-0">{renderCardSpinner('proxy')}</div>
              </div>
              {(envOverrides.includes('proxy_port') || envOverrides.includes('proxy_host') || envOverrides.includes('proxy_enabled') || envOverrides.includes('proxy_auth_user') || envOverrides.includes('proxy_auth_pass')) && (
                <div className="mt-1">
                  <EnvOverrideIndicator show message="Some settings are overwritten by environment variables (NNTP_PROXY_*) on restart." />
                </div>
              )}
            </CardHeader>
            <CardContent>
              {/* An enabled proxy that never bound looks identical to a working
                  one from here, so say so rather than leaving the user to find
                  it in the log. */}
              {proxyStatus && !proxyStatus.listening && (
                <div className="mb-4 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
                  The proxy is enabled but not listening{proxyStatus.error ? `: ${proxyStatus.error}` : '.'}
                </div>
              )}
              <div className="mb-4">
                <FormField control={control} name="proxy_enabled" render={({ field }) => (
                  <FormItem className="rounded-md border border-border/60 p-3">
                    <div className={stackedFieldRowClass}>
                      <FormLabel className={labelClass}>Enable NNTP Proxy</FormLabel>
                      <FormControl><Switch checked={field.value !== false} onCheckedChange={(checked) => { field.onChange(checked); commitField('proxy_enabled') }} /></FormControl>
                    </div>
                    <FormDescription className="mt-3">Turn the local NNTP proxy server on or off.</FormDescription>
                  </FormItem>
                )} />
              </div>
              <div className="rounded-md border border-border/60">
                <FormField control={control} name="proxy_host" render={({ field }) => (
                  <FormItem className="rounded-none border-0 p-3">
                    <div className={stackedFieldRowClass}>
                      <FormLabel className={cn(labelClass, 'sm:flex-1')}>Bind Host</FormLabel>
                      <FormControl><Input placeholder="0.0.0.0" disabled={!proxyEnabled} className={`h-9 ${controlWideClass}`} {...field} onBlur={blurCommit(field, 'proxy_host')} /></FormControl>
                    </div>
                    <FormDescription className="mt-3">Which local address the proxy server should bind to.</FormDescription>
                    <FormMessage />
                  </FormItem>
                )} />
                <FormField control={control} name="proxy_port" render={({ field }) => (
                  <FormItem className="relative rounded-none border-0 p-3">
                    <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                    <div className={stackedFieldRowClass}>
                      <FormLabel className={cn(labelClass, 'sm:flex-1')}>Port</FormLabel>
                      <FormControl><Input type="number" disabled={!proxyEnabled} className={`h-9 ${controlMediumClass}`} {...field} onChange={e => field.onChange(e.target.valueAsNumber)} onBlur={blurCommit(field, 'proxy_port')} /></FormControl>
                    </div>
                    <FormDescription className="mt-3">The port other apps use when connecting to the local NNTP proxy.</FormDescription>
                    <FormMessage />
                  </FormItem>
                )} />
                <FormField control={control} name="proxy_auth_user" render={({ field }) => (
                  <FormItem className="relative rounded-none border-0 p-3">
                    <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                    <div className={stackedFieldRowClass}>
                      <FormLabel className={cn(labelClass, 'sm:flex-1')}>Proxy Username</FormLabel>
                      <FormControl><Input disabled={!proxyEnabled} className={`h-9 ${controlWideClass}`} {...field} onBlur={blurCommit(field, 'proxy_auth_user')} /></FormControl>
                    </div>
                    <FormDescription className="mt-3">Optional username clients must provide when using the proxy.</FormDescription>
                    <FormMessage />
                  </FormItem>
                )} />
                <FormField control={control} name="proxy_auth_pass" render={({ field }) => (
                  <FormItem className="relative rounded-none border-0 p-3">
                    <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                    <div className={stackedFieldRowClass}>
                      <FormLabel className={cn(labelClass, 'sm:flex-1')}>Proxy Password</FormLabel>
                      <FormControl>
                        <div className={controlWideClass}>
                          <PasswordInput disabled={!proxyEnabled} className="h-9 w-full" {...field} onBlur={blurCommit(field, 'proxy_auth_pass')} />
                        </div>
                      </FormControl>
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
              <div className="flex shrink-0 items-center gap-2">
                {renderCardSpinner('useragent')}
                <Button type="button" variant="outline" size="sm" disabled={agentRefresh.busy} onClick={() => { void refreshUserAgents() }}>
                  {agentRefresh.busy
                    ? <Loader2 className="h-4 w-4 animate-spin" />
                    : <RefreshCw className="h-4 w-4" />}
                  Update to latest
                </Button>
              </div>
            </div>
          </CardHeader>
          <CardContent>
            {agentRefresh.message ? (
              <p className={cn('mb-3 text-sm', agentRefresh.error ? 'text-destructive' : 'text-muted-foreground')}>{agentRefresh.message}</p>
            ) : null}
            <div className="rounded-md border border-border/60">
              <FormField control={control} name="indexer_query_header" render={({ field }) => (
                <FormItem className="rounded-none border-0 p-3">
                  <div className={stackedFieldRowClass}>
                    <FormLabel className={cn(labelClass, 'flex items-center gap-1.5 sm:flex-1')}>Indexer Query Header <EnvOverrideIndicator show={envOverrides.includes('indexer_query_header')} /></FormLabel>
                    <FormControl><Input className={`h-9 ${controlWideClass}`} {...field} value={field.value || ''} placeholder="Prowlarr/2.3.0.5236" onBlur={blurCommit(field, 'indexer_query_header')} /></FormControl>
                  </div>
                  <FormDescription className="mt-3">Used for indexer search and capability requests.</FormDescription>
                  <FormMessage />
                </FormItem>
              )} />
              <FormField control={control} name="indexer_grab_header" render={({ field }) => (
                <FormItem className="relative rounded-none border-0 p-3">
                  <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                  <div className={stackedFieldRowClass}>
                    <FormLabel className={cn(labelClass, 'flex items-center gap-1.5 sm:flex-1')}>Indexer Grab Header <EnvOverrideIndicator show={envOverrides.includes('indexer_grab_header')} /></FormLabel>
                    <FormControl><Input className={`h-9 ${controlWideClass}`} {...field} value={field.value || ''} placeholder="SABnzbd/4.5.5" onBlur={blurCommit(field, 'indexer_grab_header')} /></FormControl>
                  </div>
                  <FormDescription className="mt-3">Used when grabbing NZBs from indexers.</FormDescription>
                  <FormMessage />
                </FormItem>
              )} />
              <FormField control={control} name="provider_header" render={({ field }) => (
                <FormItem className="relative rounded-none border-0 p-3">
                  <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                  <div className={stackedFieldRowClass}>
                    <FormLabel className={cn(labelClass, 'flex items-center gap-1.5 sm:flex-1')}>Provider Header <EnvOverrideIndicator show={envOverrides.includes('provider_header')} /></FormLabel>
                    <FormControl><Input className={`h-9 ${controlWideClass}`} {...field} value={field.value || ''} placeholder="VLC/1.2.3.4" onBlur={blurCommit(field, 'provider_header')} /></FormControl>
                  </div>
                  <FormDescription className="mt-3">Custom provider-facing User-Agent header.</FormDescription>
                  <FormMessage />
                </FormItem>
              )} />
            </div>
          </CardContent>
        </Card>

        <ConfirmDialog
          open={!!confirmField}
          onOpenChange={(nextOpen) => {
            if (!nextOpen) {
              if (confirmField) revertField(confirmField)
              setConfirmField('')
            }
          }}
          title={CONFIRM_FIELDS[confirmField]?.title || ''}
          description={CONFIRM_FIELDS[confirmField]?.description || ''}
          confirmLabel={CONFIRM_FIELDS[confirmField]?.confirmLabel || 'Save'}
          confirmVariant="destructive"
          onConfirm={() => {
            const target = confirmField
            setConfirmField('')
            if (target) commitField(target)
          }}
        />
      </form>
    </Form>
  )
})
