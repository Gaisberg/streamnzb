import React, { useEffect, useMemo, useState } from 'react'
import { useForm, useWatch } from 'react-hook-form'
import { Check, Clipboard, Loader2, RefreshCw } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { Form, FormField, FormItem, FormLabel, FormControl, FormMessage, FormDescription } from "@/components/ui/form"
import { Switch } from "@/components/ui/switch"
import { PasswordInput } from "@/components/ui/password-input"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { EnvOverrideIndicator } from "@/components/EnvOverrideIndicator"
import { useFieldAutoSave } from '@/hooks/useFieldAutoSave'
import { cn, copyToClipboard } from "@/lib/utils"

// Every card here hands StreamNZB's own resources to another application: the
// proxy shares the provider pool with a download client, the Newznab endpoint
// shares the indexer pool with any Newznab-compatible application, the NFS
// export shares the release library with a media server as files, and the
// download client API lets an *arr fill that library. The Jellyfin endpoint
// is not here: it is a per-stream login, so it lives with the stream's
// manifest URL under Streams — as do the per-stream mount line and API key
// for the two library cards.
const CARD_FIELDS = {
  proxy: ['proxy_enabled', 'proxy_host', 'proxy_port', 'proxy_auth_user', 'proxy_auth_pass'],
  newznab: ['newznab_enabled', 'newznab_api_key'],
  nfs: ['nfs_enabled', 'nfs_host', 'nfs_port'],
  sabnzbd: ['sabnzbd_enabled', 'sabnzbd_complete_dir'],
}

const FIELD_CARD = Object.fromEntries(
  Object.entries(CARD_FIELDS).flatMap(([card, fields]) => fields.map((field) => [field, card]))
)

function pickInitialValues(values = {}) {
  return {
    proxy_enabled: values.proxy_enabled !== false,
    proxy_port: Number(values.proxy_port ?? 1119),
    proxy_host: values.proxy_host ?? '',
    proxy_auth_user: values.proxy_auth_user ?? '',
    proxy_auth_pass: values.proxy_auth_pass ?? '',
    newznab_enabled: values.newznab_enabled === true,
    newznab_api_key: values.newznab_api_key ?? '',
    nfs_enabled: values.nfs_enabled === true,
    nfs_host: values.nfs_host ?? '',
    nfs_port: Number(values.nfs_port ?? 2049),
    sabnzbd_enabled: values.sabnzbd_enabled === true,
    sabnzbd_complete_dir: values.sabnzbd_complete_dir ?? '',
  }
}

// EndpointAddress prints what a client actually connects to, so the settings
// below can be turned into a working client config without working out how
// they combine.
function EndpointAddress({ label, value, hint }) {
  const [copied, setCopied] = useState(false)
  const copy = () => {
    copyToClipboard(value).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }
  return (
    <div className="mb-4 rounded-md border border-border/60 bg-muted/20 p-3">
      <div className="text-sm font-medium">{label}</div>
      <div className="mt-2 flex items-center gap-2">
        <code className="block min-w-0 flex-1 break-all rounded bg-muted px-2.5 py-1.5 text-[11px] leading-5">{value}</code>
        <Button type="button" variant="ghost" size="icon" onClick={copy} className="h-8 w-8 shrink-0 bg-muted hover:bg-muted" aria-label={`Copy ${label}`}>
          {copied ? <Check className="h-3.5 w-3.5" /> : <Clipboard className="h-3.5 w-3.5" />}
        </Button>
      </div>
      {hint ? <p className="mt-2 text-sm text-muted-foreground">{hint}</p> : null}
    </div>
  )
}

export const IntegrationsSettingsSection = React.memo(function IntegrationsSettingsSection({
  initialValues,
  envOverrides,
  proxyStatus,
  nfsStatus,
  addonBaseURL,
  onPersist,
  saveStatus,
}) {
  const defaults = useMemo(() => pickInitialValues(initialValues), [initialValues])
  const [confirmNewznabReroll, setConfirmNewznabReroll] = useState(false)

  const form = useForm({ defaultValues: defaults })
  const { control, reset, setError, clearErrors } = form
  const proxyEnabled = useWatch({ control, name: 'proxy_enabled' }) !== false
  const nfsEnabled = useWatch({ control, name: 'nfs_enabled' }) === true
  const sabnzbdEnabled = useWatch({ control, name: 'sabnzbd_enabled' }) === true
  const { saveField, savingField } = useFieldAutoSave({
    form,
    savedValues: defaults,
    onPersist,
  })

  // Both addresses are built from the live form values, so they track an edit
  // before it is even saved.
  const proxyHost = useWatch({ control, name: 'proxy_host' })
  const proxyPort = useWatch({ control, name: 'proxy_port' })
  const newznabKey = useWatch({ control, name: 'newznab_api_key' })
  // A bind host of 0.0.0.0 (or blank) listens on every interface, which is not
  // an address a client can dial — show the host this page was reached on.
  const proxyReachableHost = !proxyHost || proxyHost === '0.0.0.0' || proxyHost === '::'
    ? window.location.hostname
    : proxyHost
  const proxyAddress = `${proxyReachableHost}:${proxyPort || 1119}`
  const endpointBaseURL = (addonBaseURL || window.location.origin).replace(/\/$/, '')
  const newznabURL = `${endpointBaseURL}/newznab/api?apikey=${newznabKey || ''}`
  const nfsHost = useWatch({ control, name: 'nfs_host' })
  const nfsPort = useWatch({ control, name: 'nfs_port' }) || 2049
  const nfsReachableHost = !nfsHost || nfsHost === '0.0.0.0' || nfsHost === '::'
    ? window.location.hostname
    : nfsHost
  // The token is per stream, so the general form shows a placeholder; the
  // filled-in line for each stream is on its card under Streams.
  const nfsMountCommand = `mount -t nfs -o ro,vers=3,tcp,port=${nfsPort},mountport=${nfsPort},nolock,soft ${nfsReachableHost}:/<stream token> /mnt/streamnzb`
  const windowsMountCommand = `mount -o anon,nolock,mtype=soft,timeout=20,proto=tcp,vers=3 \\\\${nfsReachableHost}\\<stream token> Z:`
  const sabnzbdURL = `${endpointBaseURL}/sabnzbd`

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
  const blurCommit = (field, name) => () => {
    field.onBlur()
    commitField(name)
  }

  // Rerolling the Newznab key is generation plus a normal field save, so it
  // reverts like any other field if the save fails. 32 random bytes as hex is
  // the same shape the server generates when the field is left empty.
  const rerollNewznabKey = () => {
    const bytes = new Uint8Array(32)
    window.crypto.getRandomValues(bytes)
    const key = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')
    form.setValue('newznab_api_key', key, { shouldDirty: true })
    void saveField('newznab_api_key', 'newznab')
  }

  const renderCardSpinner = (cardId) => (
    savingField && CARD_FIELDS[cardId].includes(savingField)
      ? <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
      : null
  )

  const stackedFieldRowClass = "flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between sm:gap-4"
  const controlWideClass = "w-full min-w-0 sm:max-w-xs"
  const controlMediumClass = "w-full min-w-0 sm:max-w-40"
  const labelClass = "min-w-0 text-sm font-medium"

  return (
    <Form {...form}>
      <form className="space-y-6">
        <div className="grid grid-cols-1 gap-6 2xl:grid-cols-2">
          <Card>
            <CardHeader>
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0 flex-1 max-w-104 space-y-0.5">
                  <CardTitle>Newznab Endpoint</CardTitle>
                  <CardDescription>Serve your configured indexers to any Newznab-compatible application as a single indexer.</CardDescription>
                </div>
                <div className="shrink-0">{renderCardSpinner('newznab')}</div>
              </div>
              {envOverrides.includes('newznab_enabled') && (
                <div className="mt-1">
                  <EnvOverrideIndicator show message="This setting is overwritten by NEWZNAB_ENABLED on restart." />
                </div>
              )}
            </CardHeader>
            <CardContent>
              <EndpointAddress
                label="Endpoint URL"
                value={newznabURL}
                hint="In a Newznab-compatible application, add a Newznab indexer with everything before /api as the URL, /api as the API Path, and the key below as the API Key."
              />
              <div className="mb-4">
                <FormField control={control} name="newznab_enabled" render={({ field }) => (
                  <FormItem className="rounded-md border border-border/60 p-3">
                    <div className={stackedFieldRowClass}>
                      <FormLabel className={labelClass}>Enable Newznab Endpoint</FormLabel>
                      <FormControl><Switch checked={field.value === true} onCheckedChange={(checked) => { field.onChange(checked); commitField('newznab_enabled') }} /></FormControl>
                    </div>
                    <FormDescription className="mt-3">Serve every configured indexer as one Newznab API. While off, the endpoint answers nothing at all.</FormDescription>
                  </FormItem>
                )} />
              </div>
              <div className="rounded-md border border-border/60">
                <FormField control={control} name="newznab_api_key" render={({ field }) => (
                  <FormItem className="rounded-none border-0 p-3">
                    <div className={stackedFieldRowClass}>
                      <FormLabel className={cn(labelClass, 'sm:flex-1')}>API Key</FormLabel>
                      <div className={cn('flex items-center gap-2', controlWideClass)}>
                        <FormControl><Input className="h-9 w-full min-w-0" {...field} onBlur={blurCommit(field, 'newznab_api_key')} /></FormControl>
                        <Button
                          type="button"
                          variant="outline"
                          size="icon"
                          className="h-9 w-9 shrink-0"
                          disabled={savingField === 'newznab_api_key'}
                          onClick={() => setConfirmNewznabReroll(true)}
                          aria-label="Generate a new Newznab API key"
                        >
                          {savingField === 'newznab_api_key' ? <Loader2 className="h-4 w-4 animate-spin" /> : <RefreshCw className="h-4 w-4" />}
                        </Button>
                      </div>
                    </div>
                    <FormDescription className="mt-3">The only credential the endpoint accepts, generated on first start. Rerolling takes effect at once — anything still using the old key stops working until it gets the new one.</FormDescription>
                    <FormMessage />
                  </FormItem>
                )} />
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0 flex-1 max-w-104 space-y-0.5">
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
              <EndpointAddress
                label="Server address"
                value={proxyAddress}
                hint="Host and port to enter in your download client, with SSL/TLS off. A bind host of 0.0.0.0 listens on every interface, so this shows the address you reached StreamNZB on."
              />
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

          <Card>
            <CardHeader>
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0 flex-1 max-w-104 space-y-0.5">
                  <CardTitle>NFS Export</CardTitle>
                  <CardDescription>Serve the release library as an NFS share. Radarr and Sonarr import into it by renaming; Plex, Emby or Kodi scan and play from it.</CardDescription>
                </div>
                <div className="shrink-0">{renderCardSpinner('nfs')}</div>
              </div>
              {(envOverrides.includes('nfs_enabled') || envOverrides.includes('nfs_host') || envOverrides.includes('nfs_port')) && (
                <div className="mt-1">
                  <EnvOverrideIndicator show message="Some settings are overwritten by environment variables (NFS_*) on restart." />
                </div>
              )}
            </CardHeader>
            <CardContent>
              {nfsStatus && !nfsStatus.listening && (
                <div className="mb-4 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
                  The export is enabled but not listening{nfsStatus.error ? `: ${nfsStatus.error}` : '.'}
                </div>
              )}
              {/* Windows clients ask port 111 where the export lives; Linux and
                  macOS are told in the mount options. A failed 111 bind is
                  therefore a note, not an error. */}
              {nfsStatus?.listening && nfsStatus.portmapper && !nfsStatus.portmapper.listening && (
                <div className="mb-4 rounded-md border border-border/60 bg-muted/20 px-3 py-2 text-sm text-muted-foreground">
                  Port 111 could not be bound{nfsStatus.portmapper.error ? ` (${nfsStatus.portmapper.error})` : ''}, so Windows clients cannot mount this export. Linux and macOS clients are unaffected. On Linux, port 111 needs root or CAP_NET_BIND_SERVICE; in Docker, publish 111/tcp and 111/udp.
                </div>
              )}
              <EndpointAddress
                label="Mount command (Linux, macOS)"
                value={nfsMountCommand}
                hint="Run on the Plex host, with a stream's token as the path — each stream card under Streams shows its own ready-made line. Keep the share on your LAN or VPN: the token is its only credential."
              />
              <EndpointAddress
                label="Mount command (Windows)"
                value={windowsMountCommand}
                hint="Needs the Services for NFS feature (Windows Pro or Enterprise) and port 111 reachable on this host, because the Windows client cannot be told a port."
              />
              <div className="mb-4">
                <FormField control={control} name="nfs_enabled" render={({ field }) => (
                  <FormItem className="rounded-md border border-border/60 p-3">
                    <div className={stackedFieldRowClass}>
                      <FormLabel className={labelClass}>Enable NFS Export</FormLabel>
                      <FormControl><Switch checked={field.value === true} onCheckedChange={(checked) => { field.onChange(checked); commitField('nfs_enabled') }} /></FormControl>
                    </div>
                    <FormDescription className="mt-3">Opens a port of its own. Finished download-client jobs appear under downloads/, releases you played through Stremio under a read-only library/, and the rest of the share is whatever folders your *arr creates and organises. Reads go through the mounting stream's providers and connection caps.</FormDescription>
                  </FormItem>
                )} />
              </div>
              <div className="rounded-md border border-border/60">
                <FormField control={control} name="nfs_host" render={({ field }) => (
                  <FormItem className="rounded-none border-0 p-3">
                    <div className={stackedFieldRowClass}>
                      <FormLabel className={cn(labelClass, 'sm:flex-1')}>Bind Host</FormLabel>
                      <FormControl><Input placeholder="0.0.0.0" disabled={!nfsEnabled} className={`h-9 ${controlWideClass}`} {...field} onBlur={blurCommit(field, 'nfs_host')} /></FormControl>
                    </div>
                    <FormDescription className="mt-3">Which local address the export binds to. A LAN or VPN address is the sensible choice.</FormDescription>
                    <FormMessage />
                  </FormItem>
                )} />
                <FormField control={control} name="nfs_port" render={({ field }) => (
                  <FormItem className="relative rounded-none border-0 p-3">
                    <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                    <div className={stackedFieldRowClass}>
                      <FormLabel className={cn(labelClass, 'sm:flex-1')}>Port</FormLabel>
                      <FormControl><Input type="number" disabled={!nfsEnabled} className={`h-9 ${controlMediumClass}`} {...field} onChange={e => field.onChange(e.target.valueAsNumber)} onBlur={blurCommit(field, 'nfs_port')} /></FormControl>
                    </div>
                    <FormDescription className="mt-3">TCP port for both the MOUNT and NFS programs. Linux and macOS clients are told it in the mount options; Windows asks port 111, which is answered alongside whenever it can be bound.</FormDescription>
                    <FormMessage />
                  </FormItem>
                )} />
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0 flex-1 max-w-104 space-y-0.5">
                  <CardTitle>Download Client API</CardTitle>
                  <CardDescription>Let Radarr and Sonarr hand their grabs to StreamNZB as if it were SABnzbd. Each release is validated, stored in the library and reported complete on the NFS export.</CardDescription>
                </div>
                <div className="shrink-0">{renderCardSpinner('sabnzbd')}</div>
              </div>
              {(envOverrides.includes('sabnzbd_enabled') || envOverrides.includes('sabnzbd_complete_dir')) && (
                <div className="mt-1">
                  <EnvOverrideIndicator show message="Some settings are overwritten by environment variables (SABNZBD_*) on restart." />
                </div>
              )}
            </CardHeader>
            <CardContent>
              <EndpointAddress
                label="Client settings"
                value={sabnzbdURL}
                hint="Add a SABnzbd download client with this host and port, /sabnzbd as the URL Base, and a stream's token as the API Key — each stream card under Streams has it. Nothing is downloaded to disk."
              />
              <div className="mb-4">
                <FormField control={control} name="sabnzbd_enabled" render={({ field }) => (
                  <FormItem className="rounded-md border border-border/60 p-3">
                    <div className={stackedFieldRowClass}>
                      <FormLabel className={labelClass}>Enable Download Client API</FormLabel>
                      <FormControl><Switch checked={field.value === true} onCheckedChange={(checked) => { field.onChange(checked); commitField('sabnzbd_enabled') }} /></FormControl>
                    </div>
                    <FormDescription className="mt-3">Rides on the addon port. While off, the endpoint answers nothing at all.</FormDescription>
                  </FormItem>
                )} />
              </div>
              <div className="rounded-md border border-border/60">
                <FormField control={control} name="sabnzbd_complete_dir" render={({ field }) => (
                  <FormItem className="rounded-none border-0 p-3">
                    <div className={stackedFieldRowClass}>
                      <FormLabel className={cn(labelClass, 'sm:flex-1')}>Complete Directory</FormLabel>
                      <FormControl><Input placeholder="/mnt/streamnzb" disabled={!sabnzbdEnabled} className={`h-9 ${controlWideClass}`} {...field} onBlur={blurCommit(field, 'sabnzbd_complete_dir')} /></FormControl>
                    </div>
                    <FormDescription className="mt-3">Where the Radarr or Sonarr host has the NFS export mounted. Completed downloads are reported as paths under it.</FormDescription>
                    <FormMessage />
                  </FormItem>
                )} />
              </div>
            </CardContent>
          </Card>
        </div>

        <ConfirmDialog
          open={confirmNewznabReroll}
          onOpenChange={(nextOpen) => { if (!nextOpen) setConfirmNewznabReroll(false) }}
          title="Generate a new Newznab API key?"
          description="The current key stops working immediately. Anything pointed at the endpoint will fail to search until the new key is pasted into it."
          confirmLabel="Generate new key"
          confirmVariant="destructive"
          onConfirm={() => { setConfirmNewznabReroll(false); rerollNewznabKey() }}
        />
      </form>
    </Form>
  )
})
