import React, { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { PasswordInput } from "@/components/ui/password-input"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { EnvOverrideIndicator } from "@/components/EnvOverrideIndicator"
import { ProfileManager } from "@/components/ProfileManager"
import { SortableList, SortableRow } from "@/components/SortableList"
import { Check, ChevronRight, Clapperboard, Info, KeyRound, Link2, Loader2, Plus, Search, ShieldCheck, TriangleAlert, X } from "lucide-react"
import { apiFetch } from "@/api"
import { nameKey, usageByName } from "@/lib/usage"
import { cn, selectClass } from "@/lib/utils"

const PROVIDER_LABELS = {
  tmdb: "TMDB",
  tvdb: "TVDB",
  kitsu: "Kitsu",
  simkl: "Simkl",
  local: "This server",
}


// Display languages for meta responses and catalog rows, as TMDB-style tags.
// TMDB localizes fully; TVDB series pick up translated names/overviews where
// TheTVDB has them, with English as the fallback everywhere.
const METADATA_LANGUAGES = [
  { tag: "en-US", label: "English (default)" },
  { tag: "cs-CZ", label: "Čeština" },
  { tag: "da-DK", label: "Dansk" },
  { tag: "de-DE", label: "Deutsch" },
  { tag: "el-GR", label: "Ελληνικά" },
  { tag: "es-ES", label: "Español (España)" },
  { tag: "es-MX", label: "Español (México)" },
  { tag: "fi-FI", label: "Suomi" },
  { tag: "fr-FR", label: "Français" },
  { tag: "he-IL", label: "עברית" },
  { tag: "hu-HU", label: "Magyar" },
  { tag: "it-IT", label: "Italiano" },
  { tag: "ja-JP", label: "日本語" },
  { tag: "ko-KR", label: "한국어" },
  { tag: "nb-NO", label: "Norsk" },
  { tag: "nl-NL", label: "Nederlands" },
  { tag: "pl-PL", label: "Polski" },
  { tag: "pt-BR", label: "Português (Brasil)" },
  { tag: "pt-PT", label: "Português (Portugal)" },
  { tag: "ro-RO", label: "Română" },
  { tag: "ru-RU", label: "Русский" },
  { tag: "sv-SE", label: "Svenska" },
  { tag: "th-TH", label: "ไทย" },
  { tag: "tr-TR", label: "Türkçe" },
  { tag: "uk-UA", label: "Українська" },
  { tag: "zh-CN", label: "中文 (简体)" },
  { tag: "zh-TW", label: "中文 (繁體)" },
]

// arrayMove without pulling in another helper: returns a copy with the item
// moved from -> to.
function moveItem(list, from, to) {
  const next = [...list]
  const [item] = next.splice(from, 1)
  next.splice(to, 0, item)
  return next
}

// seedRows resolves a profile's catalog list against the registry. A missing
// (null) list means "never configured": the registry defaults. An explicitly
// saved empty list stays empty — the backend mirrors this distinction.
function seedRows(registry, savedCatalogs) {
  if (!registry.length) return []
  if (savedCatalogs == null) {
    return registry.filter((def) => def.default_enabled).map((def) => def.id)
  }
  const known = new Set(registry.map((def) => def.id))
  const rows = []
  savedCatalogs.forEach((toggle) => {
    if (toggle?.enabled !== false && known.has(toggle.id) && !rows.includes(toggle.id)) {
      rows.push(toggle.id)
    }
  })
  return rows
}

// defaultMetadataProfile is a fresh profile: null catalogs means the registry
// defaults apply until the user edits the list.
function defaultMetadataProfile(name) {
  return { name, catalogs: null }
}

// summarize gives each profile card a one-line read of what it serves.
function summarize(profile, registry, certOptions) {
  const bits = []
  const rows = seedRows(registry, profile.catalogs ?? null)
  bits.push(rows.length === 1 ? "1 catalog" : `${rows.length} catalogs`)
  const lang = (profile.language || "").trim()
  if (lang && lang !== "en-US") {
    bits.push(METADATA_LANGUAGES.find((l) => l.tag === lang)?.label || lang)
  }
  if (profile.max_certification) {
    const opt = certOptions.find((o) => o.id === profile.max_certification)
    bits.push(opt ? opt.label : `max ${profile.max_certification}`)
  }
  if ((profile.poster_url_pattern || "").trim()) {
    bits.push("overlay posters")
  }
  return bits.join(" · ")
}

// describeDelete spells out the knock-on effect, since deleting a profile
// also clears it from any stream bound to it.
function describeDelete(profile, usage) {
  const name = profile?.name || ""
  const used = usage[nameKey(name)]
  if (!used?.length) {
    return `Delete “${name}”? It is not in use, so nothing else changes.`
  }
  return `Delete “${name}”? It will be cleared from ${used.join(", ")}, which will fall back to the stream-only manifest (no catalogs or metadata).`
}

function CatalogBadges({ def }) {
  return (
    <span className="flex flex-wrap items-center gap-1">
      <Badge variant="outline" className="shrink-0 text-[10px]">{PROVIDER_LABELS[def.provider] || def.provider}</Badge>
      <Badge variant="outline" className="shrink-0 text-[10px] capitalize">{def.type}</Badge>
    </span>
  )
}

// MetadataProfileEditor is the detail pane: everything one profile carries.
// Pure controlled component — every edit flows up through onChange.
function MetadataProfileEditor({ draft, onChange, registry, registryError, certOptions, simklCard }) {
  const [addOpen, setAddOpen] = useState(false)
  const [query, setQuery] = useState("")

  const defsByID = useMemo(() => new Map(registry.map((def) => [def.id, def])), [registry])
  const rows = useMemo(() => seedRows(registry, draft.catalogs ?? null), [registry, draft.catalogs])

  const setRows = (next) => {
    onChange({ ...draft, catalogs: next.map((id) => ({ id, enabled: true })) })
  }

  const available = useMemo(
    () => registry.filter((def) => !rows.includes(def.id)),
    [registry, rows]
  )
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return available
    return available.filter((def) =>
      [def.name, def.type, def.provider, PROVIDER_LABELS[def.provider] || ""]
        .some((text) => text.toLowerCase().includes(q))
    )
  }, [available, query])

  const capped = Boolean(draft.max_certification)

  return (
    <div className="space-y-4">
      <Card className="border border-border bg-card">
        <CardHeader className="pb-3">
          <div className="flex flex-wrap items-start justify-between gap-2">
            <div>
              <CardTitle className="text-base font-semibold">Catalogs</CardTitle>
              <CardDescription>
                Rows appear in the client in this order. Drag to reorder. Search is always available for every
                content type, independent of these rows.
              </CardDescription>
            </div>
            <Button size="sm" onClick={() => { setQuery(""); setAddOpen(true) }} disabled={available.length === 0}>
              <Plus className="mr-2 h-4 w-4" /> Add catalog
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {registryError ? (
            <p className="text-xs text-destructive">Could not load the catalog list from the server.</p>
          ) : rows.length === 0 ? (
            <div className="rounded-md border border-dashed border-border/70 px-3 py-6 text-center">
              <p className="text-sm text-muted-foreground">No catalogs added.</p>
              <p className="mt-1 text-xs text-muted-foreground">
                Streams on this profile still get posters, episode metadata and search, but no browse rows.
              </p>
              <Button size="sm" className="mt-3" onClick={() => { setQuery(""); setAddOpen(true) }}>
                <Plus className="mr-2 h-4 w-4" /> Add catalog
              </Button>
            </div>
          ) : (
            <SortableList ids={rows} onMove={(from, to) => setRows(moveItem(rows, from, to))}>
              <div className="space-y-2">
                {rows.map((id) => {
                  const def = defsByID.get(id)
                  if (!def) return null
                  return (
                    <SortableRow key={id} id={id}>
                      <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-1">
                        <span className="truncate text-sm font-medium">{def.name}</span>
                        <CatalogBadges def={def} />
                      </div>
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8 shrink-0 text-muted-foreground hover:text-destructive"
                        onClick={() => setRows(rows.filter((rowID) => rowID !== id))}
                        aria-label={`Remove ${def.name}`}
                      >
                        <X className="h-4 w-4" />
                      </Button>
                    </SortableRow>
                  )
                })}
              </div>
            </SortableList>
          )}
        </CardContent>
      </Card>

      <Card className="border border-border bg-card">
        <CardHeader className="pb-3">
          <CardTitle className="flex items-center gap-2 text-base font-semibold">
            <ShieldCheck className="h-4 w-4 text-muted-foreground" /> Parental controls
          </CardTitle>
          <CardDescription>
            Cap what this profile serves by age certification — catalogs, title pages and playback all respect it.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="metadata-max-cert" className="text-sm">Rating limit</Label>
            <select
              id="metadata-max-cert"
              className={selectClass}
              value={draft.max_certification || ""}
              onChange={(e) => {
                const value = e.target.value
                const next = { ...draft, max_certification: value || undefined }
                if (!value) delete next.allow_unrated
                onChange(next)
              }}
            >
              <option value="">No limit</option>
              {certOptions.map((opt) => (
                <option key={opt.id} value={opt.id}>{opt.label}</option>
              ))}
            </select>
          </div>
          {capped && (
            <>
              <div className="flex items-center justify-between gap-3 rounded-md border border-border/60 px-3 py-2.5">
                <div className="min-w-0">
                  <Label htmlFor="metadata-allow-unrated" className="text-sm">Allow unrated content</Label>
                  <p className="text-xs text-muted-foreground">
                    Certification data is patchy — niche and foreign titles often have none. Off (the default)
                    hides them; on lets them through the limit.
                  </p>
                </div>
                <Switch
                  id="metadata-allow-unrated"
                  checked={draft.allow_unrated === true}
                  onCheckedChange={(value) => {
                    const next = { ...draft }
                    if (value) next.allow_unrated = true
                    else delete next.allow_unrated
                    onChange(next)
                  }}
                />
              </div>
              <div className="flex items-start gap-2 rounded-lg border border-border bg-muted/30 px-3.5 py-2.5">
                <Info className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
                <p className="text-xs text-muted-foreground">
                  The limit only applies to streams bound to this profile. A stream with no metadata profile is
                  not capped — for a kids device, bind this profile to its stream on the Streams page.
                </p>
              </div>
            </>
          )}
        </CardContent>
      </Card>

      <Card className="border border-border bg-card">
        <CardHeader className="pb-3">
          <CardTitle className="text-base font-semibold">Sources &amp; language</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-3 sm:grid-cols-3">
            <div className="space-y-1.5">
              <Label htmlFor="metadata-source-movie" className="text-sm">Movies</Label>
              <select id="metadata-source-movie" className={selectClass} value="tmdb" disabled>
                <option value="tmdb">TMDB</option>
              </select>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="metadata-source-series" className="text-sm">Series</Label>
              <select
                id="metadata-source-series"
                className={selectClass}
                value={draft.series_source === "tmdb" ? "tmdb" : "tvdb"}
                onChange={(e) => onChange({ ...draft, series_source: e.target.value === "tmdb" ? "tmdb" : undefined })}
              >
                <option value="tvdb">TVDB (default)</option>
                <option value="tmdb">TMDB</option>
              </select>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="metadata-source-anime" className="text-sm">Anime</Label>
              <select id="metadata-source-anime" className={selectClass} value="kitsu" disabled>
                <option value="kitsu">Kitsu</option>
              </select>
            </div>
          </div>
          <p className="text-xs text-muted-foreground">
            The source a media type&apos;s name, artwork and episode list come from. When the primary source
            cannot serve a title, the other one steps in. Movies and anime have a single source today.
          </p>
          <div className="space-y-1.5">
            <Label htmlFor="metadata-language" className="text-sm">Language</Label>
            <select
              id="metadata-language"
              className={selectClass}
              value={draft.language || "en-US"}
              onChange={(e) => onChange({ ...draft, language: e.target.value === "en-US" ? undefined : e.target.value })}
            >
              {METADATA_LANGUAGES.map((lang) => (
                <option key={lang.tag} value={lang.tag}>{lang.label}</option>
              ))}
            </select>
            <p className="text-xs text-muted-foreground">
              Titles, overviews, episode names and catalog rows display in this language where the sources
              have a translation, falling back to English where they don&apos;t. Clients cache title pages for a
              few hours, so a change shows up on already-visited titles after the cache expires.
            </p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="metadata-poster-url" className="text-sm">Poster overlay URL</Label>
            <Input
              id="metadata-poster-url"
              className="h-9 w-full font-mono text-xs"
              value={draft.poster_url_pattern || ""}
              onChange={(e) => {
                const value = e.target.value
                const next = { ...draft }
                if (value.trim()) next.poster_url_pattern = value
                else delete next.poster_url_pattern
                onChange(next)
              }}
              placeholder="https://btttr.cc/poster/imdb/poster-default/{imdb_id}.jpg"
            />
            <p className="text-xs text-muted-foreground">
              Replace posters with an overlay service&apos;s — ratings, quality and genre badges baked into
              the artwork. Paste a URL template containing <span className="font-mono">{"{imdb_id}"}</span>;{" "}
              <a href="https://btttr.cc/configure" target="_blank" rel="noreferrer" className="underline underline-offset-2">BetterPosters</a>{" "}
              (free, no key) and RatingPosterDB both generate one. Anime maps to its series-level IMDb id,
              so every season of a show shares one overlay poster; titles without a resolvable IMDb id
              keep their original artwork.
            </p>
          </div>
          <div className="flex items-center justify-between gap-3 rounded-md border border-border/60 px-3 py-2.5">
            <div className="min-w-0">
              <Label htmlFor="metadata-tvmaze-airdates" className="text-sm">TVMaze air dates</Label>
              <p className="text-xs text-muted-foreground">
                Show TVMaze&apos;s exact air times on title pages instead of the source&apos;s own dates;
                TVMaze keeps them more closely for running shows. Display only — skipping unaired
                episodes is an indexer setting and always uses the best air time it can find.
              </p>
            </div>
            <Switch
              id="metadata-tvmaze-airdates"
              checked={draft.tvmaze_air_dates !== false}
              onCheckedChange={(value) => {
                const next = { ...draft }
                if (value) delete next.tvmaze_air_dates
                else next.tvmaze_air_dates = false
                onChange(next)
              }}
            />
          </div>
        </CardContent>
      </Card>

      {simklCard}

      <Dialog open={addOpen} onOpenChange={setAddOpen}>
        <DialogContent className="max-w-lg p-4 sm:p-6">
          <DialogHeader>
            <DialogTitle>Add catalog</DialogTitle>
            <DialogDescription>Catalogs already on the profile are hidden from the results.</DialogDescription>
          </DialogHeader>
          <div className="relative">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              autoFocus
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search by name, provider or type…"
              className="h-9 pl-8"
            />
          </div>
          <div className="max-h-72 space-y-1 overflow-y-auto">
            {filtered.length === 0 ? (
              <p className="px-1 py-4 text-center text-sm text-muted-foreground">
                {available.length === 0 ? "Every catalog is already added." : "No catalogs match."}
              </p>
            ) : (
              filtered.map((def) => (
                <button
                  key={def.id}
                  type="button"
                  className="flex w-full items-center justify-between gap-2 rounded-md border border-transparent px-2 py-2 text-left transition-colors hover:border-border hover:bg-muted/40"
                  onClick={() => setRows([...rows, def.id])}
                >
                  <span className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-1">
                    <span className="truncate text-sm">{def.name}</span>
                    <CatalogBadges def={def} />
                  </span>
                  <Plus className="h-4 w-4 shrink-0 text-muted-foreground" />
                </button>
              ))
            )}
          </div>
        </DialogContent>
      </Dialog>
    </div>
  )
}

// keyErrorMessage prefers the per-field reason the backend returns — the live
// provider check's own words — over the generic "Validation failed" summary.
function keyErrorMessage(error, fields) {
  const fieldErrors = error?.fieldErrors || {}
  for (const field of fields) {
    const message = fieldErrors[field]
    if (typeof message === "string" && message.trim() !== "") return message
  }
  return error?.message || "Save failed."
}

// KeySaveStatus is the only feedback these fields have: they never show a
// stored key back, so without it a rejected key and a saved one look identical.
function KeySaveStatus({ status }) {
  if (!status) return null
  if (status.state === "saving") {
    return (
      <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <Loader2 className="h-3.5 w-3.5 animate-spin" /> Saving…
      </p>
    )
  }
  if (status.state === "saved") {
    return (
      <p className="flex items-center gap-1.5 text-xs text-emerald-500">
        <Check className="h-3.5 w-3.5" /> Saved.
      </p>
    )
  }
  return <p className="text-xs text-destructive">{status.message}</p>
}

// SimklCard links a Simkl account through the PIN device flow: show the code,
// send the user to simkl.com/pin, poll until Simkl confirms. The watchlist
// catalogs only exist in the registry once an account is linked, so the parent
// refetches the catalog list on every connect/disconnect. The account — and
// the scrobble toggle — are server-wide, shared by every profile.
function SimklCard({ onAccountChange, scrobble, onScrobbleChange }) {
  const [status, setStatus] = useState(null)
  const [pin, setPin] = useState(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState("")
  // Optimistic mirror of the saved toggle, cleared when the save echoes back.
  const [scrobbleOverride, setScrobbleOverride] = useState(null)
  useEffect(() => { setScrobbleOverride(null) }, [scrobble])
  const scrobbleOn = scrobbleOverride ?? scrobble
  // Polling state lives in refs: the interval callback must see the current
  // code without re-arming the timer on every render.
  const pollRef = useRef(null)

  const stopPolling = () => {
    if (pollRef.current) {
      clearInterval(pollRef.current)
      pollRef.current = null
    }
  }

  useEffect(() => {
    let cancelled = false
    apiFetch("/api/simkl/status")
      .then((data) => { if (!cancelled) setStatus(data) })
      .catch(() => { if (!cancelled) setStatus({ enabled: false, connected: false }) })
    return () => { cancelled = true; stopPolling() }
  }, [])

  const finishLink = (nextStatus) => {
    stopPolling()
    setPin(null)
    setStatus(nextStatus)
    onAccountChange()
  }

  const startLink = async () => {
    setError("")
    setBusy(true)
    try {
      const data = await apiFetch("/api/simkl/pin", { method: "POST" })
      setPin(data)
      const expiresAt = Date.now() + (data.expires_in || 900) * 1000
      pollRef.current = setInterval(async () => {
        if (Date.now() > expiresAt) {
          stopPolling()
          setPin(null)
          setError("The code expired before it was entered. Start over to get a new one.")
          return
        }
        try {
          const check = await apiFetch("/api/simkl/pin/check", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ user_code: data.user_code }),
          })
          if (check?.connected) finishLink(check.status)
        } catch {
          // Transient poll failures are retried on the next tick.
        }
      }, Math.max(data.interval || 5, 1) * 1000)
    } catch (err) {
      setError(err?.message || "Could not start the Simkl link.")
    } finally {
      setBusy(false)
    }
  }

  const cancelLink = () => {
    stopPolling()
    setPin(null)
  }

  const disconnect = async () => {
    setError("")
    setBusy(true)
    try {
      const next = await apiFetch("/api/simkl/disconnect", { method: "POST" })
      finishLink(next)
    } catch (err) {
      setError(err?.message || "Disconnect failed.")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card className="border border-border bg-card">
      <CardHeader className="pb-3">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div>
            <CardTitle className="flex items-center gap-2 text-base font-semibold">
              <Link2 className="h-4 w-4 text-muted-foreground" /> Simkl
            </CardTitle>
            <CardDescription>
              Link your Simkl account to serve its watchlists — Watching, Plan to Watch, On Hold,
              Completed and Dropped — as catalog rows. Once linked, they appear in every profile&apos;s
              &quot;Add catalog&quot; list.
            </CardDescription>
          </div>
          {status?.connected ? (
            <Button size="sm" variant="outline" onClick={disconnect} disabled={busy}>
              Disconnect
            </Button>
          ) : (
            <Button size="sm" onClick={startLink} disabled={busy || !status || !status.enabled || Boolean(pin)}>
              {busy ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : null} Connect
            </Button>
          )}
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        {status?.connected && (
          <>
            <p className="flex items-center gap-1.5 text-sm text-emerald-500">
              <Check className="h-4 w-4" /> Connected{status.user_name ? ` as ${status.user_name}` : ""}.
            </p>
            <div className="flex items-center justify-between gap-3 rounded-md border border-border/60 px-3 py-2.5">
              <div className="min-w-0">
                <Label htmlFor="simkl-scrobble" className="text-sm">Scrobble playback to Simkl</Label>
                <p className="text-xs text-muted-foreground">
                  Report what gets played to the linked account: &quot;watching now&quot; while a title
                  streams, watched progress when it stops — Simkl marks it watched past 80%, and keeps
                  the resume position below that. Applies to every stream on this server.
                </p>
              </div>
              <Switch
                id="simkl-scrobble"
                checked={scrobbleOn}
                onCheckedChange={(value) => {
                  setScrobbleOverride(value)
                  onScrobbleChange(value)
                }}
              />
            </div>
          </>
        )}
        {status && !status.enabled && (
          <p className="text-xs text-muted-foreground">
            No Simkl client id is available in this build. Create an app at{" "}
            <a href="https://simkl.com/settings/developer/" target="_blank" rel="noreferrer" className="underline underline-offset-2">
              simkl.com/settings/developer
            </a>{" "}
            and paste its client id under API keys below, then connect.
          </p>
        )}
        {error && <p className="text-xs text-destructive">{error}</p>}
        <Dialog open={Boolean(pin)} onOpenChange={(open) => { if (!open) cancelLink() }}>
          <DialogContent className="max-w-sm p-4 sm:p-6">
            <DialogHeader>
              <DialogTitle>Link Simkl</DialogTitle>
              <DialogDescription>
                Enter this code at{" "}
                <a
                  href={pin?.verification_url || "https://simkl.com/pin/"}
                  target="_blank"
                  rel="noreferrer"
                  className="underline underline-offset-2"
                >
                  simkl.com/pin
                </a>{" "}
                and approve the app. This page updates by itself once you have.
              </DialogDescription>
            </DialogHeader>
            <p className="select-all rounded-md border border-border bg-muted/30 py-4 text-center font-mono text-3xl tracking-[0.3em]">
              {pin?.user_code}
            </p>
            <p className="flex items-center justify-center gap-1.5 text-xs text-muted-foreground">
              <Loader2 className="h-3.5 w-3.5 animate-spin" /> Waiting for approval…
            </p>
          </DialogContent>
        </Dialog>
      </CardContent>
    </Card>
  )
}

export function MetadataPage({ config, onPersist, isSaving, saveStatus }) {
  const envOverrides = config?.env_overrides ?? []
  // null means the backend has not migrated yet; treat as none, never PUT
  // null back — every save sends a real array.
  const profiles = useMemo(() => config?.metadata_profiles || [], [config])
  // A metadata profile bound nowhere serves nothing.
  const usage = useMemo(() => usageByName(config?.streams, (stream) => [stream.metadata_profile_name]), [config])
  const anyInUse = Object.keys(usage).length > 0

  const [registry, setRegistry] = useState([])
  const [registryError, setRegistryError] = useState(false)
  const [certOptions, setCertOptions] = useState([])
  const [keysOpen, setKeysOpen] = useState(false)
  const [tmdbKey, setTmdbKey] = useState("")
  const [tvdbKey, setTvdbKey] = useState("")
  const [simklId, setSimklId] = useState("")
  const [keyStatus, setKeyStatus] = useState({})
  // The last patch committed per group, so re-focusing a field does not fire
  // another provider round trip for a value that already landed.
  const committedRef = useRef({})

  // Refetched on Simkl connect/disconnect too — the backend only lists the
  // watchlist catalogs while an account is linked.
  const loadRegistry = useCallback(() => {
    apiFetch("/api/metadata/catalogs")
      .then((data) => { setRegistry(Array.isArray(data) ? data : []); setRegistryError(false) })
      .catch(() => setRegistryError(true))
  }, [])

  useEffect(() => {
    let cancelled = false
    loadRegistry()
    apiFetch("/api/metadata/certifications")
      .then((data) => { if (!cancelled) setCertOptions(Array.isArray(data) ? data : []) })
      .catch(() => { /* the dropdown degrades to "No limit" only */ })
    return () => { cancelled = true }
  }, [loadRegistry])

  // API keys save on blur. The backend keeps a key the patch does not mention,
  // so a blank field means "leave the stored one alone" and is never sent.
  const commitKeys = (group, fields) => {
    const patch = {}
    Object.entries(fields).forEach(([field, value]) => {
      const trimmed = value.trim()
      if (trimmed !== "") patch[field] = trimmed
    })
    const signature = JSON.stringify(patch)
    if (Object.keys(patch).length === 0 || committedRef.current[group] === signature) return
    committedRef.current[group] = signature
    setKeyStatus((prev) => ({ ...prev, [group]: { state: "saving" } }))
    Promise.resolve(onPersist(patch))
      .then(() => setKeyStatus((prev) => ({ ...prev, [group]: { state: "saved" } })))
      .catch((err) => {
        // Nothing landed, so the next blur has to be free to retry.
        committedRef.current[group] = ""
        setKeyStatus((prev) => ({
          ...prev,
          [group]: { state: "error", message: keyErrorMessage(err, Object.keys(patch)) },
        }))
      })
  }

  // Rendered inside the profile editor under Sources & language; the account
  // itself is server-wide. Falls back to a standalone card while no profile
  // exists yet, so linking stays reachable.
  const simklCard = (
    <SimklCard
      onAccountChange={loadRegistry}
      scrobble={Boolean(config?.simkl_scrobble)}
      onScrobbleChange={(value) => onPersist({ simkl_scrobble: value })}
    />
  )

  return (
    <div className="space-y-6">
      <div>
        <h2 className="flex items-center gap-2 text-lg font-medium text-foreground">
          <Clapperboard className="h-4 w-4" /> Metadata
        </h2>
        <p className="text-sm text-muted-foreground">
          A metadata profile decides which catalogs a stream serves, in which language, and what content its
          rating limit allows. Bind one to a stream from the Streams page — a stream without one serves
          streams only, exactly as before this feature existed.
        </p>
      </div>

      {profiles.length > 0 && !anyInUse && (
        <div className="flex items-start gap-2 rounded-lg border border-amber-500/30 bg-amber-500/5 px-3.5 py-2.5">
          <TriangleAlert className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
          <p className="text-xs text-muted-foreground">
            No stream is bound to any of these profiles, so no catalogs or metadata are served. Bind a profile
            to a stream on the Streams page.
          </p>
        </div>
      )}

      <ProfileManager
        profiles={profiles}
        onSave={(next) => onPersist({ metadata_profiles: next })}
        usage={usage}
        summarize={(profile) => summarize(profile, registry, certOptions)}
        newProfile={defaultMetadataProfile}
        describeDelete={describeDelete}
        entityLabel="metadata profile"
        emptyText="No metadata profiles yet. Without one, every stream serves streams only."
        isSaving={isSaving}
        saveStatus={saveStatus}
        renderEditor={(draft, setDraft) => (
          <MetadataProfileEditor
            draft={draft}
            onChange={setDraft}
            registry={registry}
            registryError={registryError}
            certOptions={certOptions}
            simklCard={simklCard}
          />
        )}
      >
        <div className="flex items-start gap-2 rounded-lg border border-border bg-muted/30 px-3.5 py-2.5">
          <Info className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
          <p className="text-xs text-muted-foreground">
            Clients cache the addon manifest — after changing a profile&apos;s catalogs, refresh or
            reinstall the addon in the client for the change to show up.
          </p>
        </div>
      </ProfileManager>

      {profiles.length === 0 && simklCard}

      <Card className="border border-border bg-card">
        <button
          type="button"
          className="flex w-full items-center justify-between gap-3 px-6 py-4 text-left"
          onClick={() => setKeysOpen((open) => !open)}
          aria-expanded={keysOpen}
        >
          <span className="flex items-center gap-2">
            <KeyRound className="h-4 w-4 text-muted-foreground" />
            <span className="text-base font-semibold">API keys</span>
            <span className="text-xs text-muted-foreground">(shared by every profile)</span>
          </span>
          <ChevronRight className={cn("h-4 w-4 shrink-0 text-muted-foreground transition-transform", keysOpen && "rotate-90")} />
        </button>
        {keysOpen && (
          <CardContent className="space-y-5 border-t border-border pt-4">
            <p className="text-xs text-muted-foreground">
              Stored server-side and never shown back, so these start blank even when a key is saved.
              Leaving a field blank keeps the stored key. Each key is verified against its provider as
              you leave the field, and only saved once it answers. TVMaze and Kitsu need no key.
            </p>
            <div className="space-y-1.5">
              <Label htmlFor="metadata-tmdb-key" className="flex items-center gap-1.5 text-sm">
                TMDB Read Access Token <EnvOverrideIndicator show={envOverrides.includes("tmdb_api_key")} />
              </Label>
              <PasswordInput
                id="metadata-tmdb-key"
                className="h-9 w-full font-mono text-xs"
                value={tmdbKey}
                onChange={(e) => setTmdbKey(e.target.value)}
                onBlur={() => commitKeys("tmdb", { tmdb_api_key: tmdbKey })}
              />
              <p className="text-xs text-muted-foreground">
                Powers titles, artwork, episode lists and the TMDB catalogs. This is the long{" "}
                <span className="font-mono">Read Access Token</span> from the API settings page, not the
                short v3 API key.
              </p>
              <KeySaveStatus status={keyStatus.tmdb} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="metadata-tvdb-key" className="flex items-center gap-1.5 text-sm">
                TVDB API Key <EnvOverrideIndicator show={envOverrides.includes("tvdb_api_key")} />
              </Label>
              <PasswordInput
                id="metadata-tvdb-key"
                className="h-9 w-full font-mono text-xs"
                value={tvdbKey}
                onChange={(e) => setTvdbKey(e.target.value)}
                onBlur={() => commitKeys("tvdb", { tvdb_api_key: tvdbKey })}
              />
              <p className="text-xs text-muted-foreground">
                The default source for series artwork and episode lists, and the resolver for TVDB ids.
                Leave blank to use the built-in key, which has no quota you would need to escape.
              </p>
              <KeySaveStatus status={keyStatus.tvdb} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="metadata-simkl-id" className="flex items-center gap-1.5 text-sm">
                Simkl Client ID <EnvOverrideIndicator show={envOverrides.includes("simkl_client_id")} />
              </Label>
              <PasswordInput
                id="metadata-simkl-id"
                className="h-9 w-full font-mono text-xs"
                value={simklId}
                onChange={(e) => setSimklId(e.target.value)}
                onBlur={() => commitKeys("simkl", { simkl_client_id: simklId })}
              />
              <p className="text-xs text-muted-foreground">
                The Simkl app the account link authorizes against. Leave blank to use the built-in
                one; changing it unlinks the current account until it is re-linked.
              </p>
              <KeySaveStatus status={keyStatus.simkl} />
            </div>
            <p className="rounded-md border border-border/60 bg-muted/20 px-3 py-2.5 text-[11px] leading-relaxed text-muted-foreground/80">
              This product uses the TMDB API but is not endorsed or certified by TMDB. Series metadata is
              provided by TheTVDB — consider subscribing to support them. The built-in fallback keys are
              shared by every StreamNZB install and exist for personal use; for anything beyond that,
              request your own keys from the providers and enter them here.
            </p>
          </CardContent>
        )}
      </Card>
    </div>
  )
}
