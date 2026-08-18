import React, { useEffect, useMemo, useRef, useState } from "react"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { PasswordInput } from "@/components/ui/password-input"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { EnvOverrideIndicator } from "@/components/EnvOverrideIndicator"
import { SortableList, SortableRow } from "@/components/SortableList"
import { ChevronRight, Clapperboard, Info, KeyRound, Loader2, Plus, Search, X } from "lucide-react"
import { apiFetch } from "@/api"
import { cn } from "@/lib/utils"

const PROVIDER_LABELS = {
  tmdb: "TMDB",
  tvdb: "TVDB",
  kitsu: "Kitsu",
  local: "This server",
}

const sourceSelectClass = "flex h-9 w-full min-w-0 max-w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 disabled:opacity-60"

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

// seedRows resolves the saved catalog list against the registry. A missing
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

function CatalogBadges({ def }) {
  return (
    <span className="flex flex-wrap items-center gap-1">
      <Badge variant="outline" className="shrink-0 text-[10px]">{PROVIDER_LABELS[def.provider] || def.provider}</Badge>
      <Badge variant="outline" className="shrink-0 text-[10px] capitalize">{def.type}</Badge>
      {def.supports_search && <Badge variant="outline" className="shrink-0 text-[10px]">search</Badge>}
    </span>
  )
}

export function MetadataPage({ config, onPersist, isSaving, saveStatus }) {
  const envOverrides = config?.env_overrides ?? []
  const [registry, setRegistry] = useState([])
  const [registryError, setRegistryError] = useState(false)
  const [enabled, setEnabled] = useState(false)
  const [rows, setRows] = useState([])
  const [seeded, setSeeded] = useState(false)
  const [addOpen, setAddOpen] = useState(false)
  const [query, setQuery] = useState("")
  const [configOpen, setConfigOpen] = useState(false)
  const [seriesSource, setSeriesSource] = useState("tvdb")
  const [metaLanguage, setMetaLanguage] = useState("en-US")
  const [tvmazeAirDates, setTvmazeAirDates] = useState(true)
  const [tmdbKey, setTmdbKey] = useState("")
  const [tvdbKey, setTvdbKey] = useState("")

  useEffect(() => {
    let cancelled = false
    apiFetch("/api/metadata/catalogs")
      .then((data) => { if (!cancelled) setRegistry(Array.isArray(data) ? data : []) })
      .catch(() => { if (!cancelled) setRegistryError(true) })
    return () => { cancelled = true }
  }, [])

  const defsByID = useMemo(() => new Map(registry.map((def) => [def.id, def])), [registry])

  // Auto-save: every mutation schedules a save of the complete metadata
  // section (the config PUT replaces slices wholesale). pendingRef keeps the
  // reseed effect from clobbering local edits with our own save echoing back
  // through the post-save config refetch.
  const pendingRef = useRef(0)
  const saveTimerRef = useRef(null)
  const queueSave = (nextEnabled, nextRows) => {
    pendingRef.current += 1
    if (saveTimerRef.current) {
      window.clearTimeout(saveTimerRef.current)
      pendingRef.current -= 1
    }
    saveTimerRef.current = window.setTimeout(() => {
      saveTimerRef.current = null
      const payload = {
        metadata: {
          enabled: nextEnabled,
          catalogs: nextRows.map((id) => ({ id, enabled: true })),
        },
      }
      Promise.resolve(onPersist(payload)).finally(() => {
        // Let the post-save refetch settle before reseeding is allowed again.
        window.setTimeout(() => { pendingRef.current = Math.max(0, pendingRef.current - 1) }, 500)
      })
    }, 400)
  }
  useEffect(() => () => { if (saveTimerRef.current) window.clearTimeout(saveTimerRef.current) }, [])

  // Seed (and reseed on external change) from the saved config once the
  // registry is known. Skipped while our own save is in flight.
  const saved = useMemo(() => ({
    // The master switch defaults to on: only an explicit false is off.
    enabled: config?.metadata?.enabled !== false,
    catalogs: config?.metadata?.catalogs ?? null,
  }), [config])
  useEffect(() => {
    if (!registry.length) return
    if (pendingRef.current > 0 || saveTimerRef.current) return
    const savedRows = seedRows(registry, saved.catalogs)
    if (seeded && saved.enabled === enabled && JSON.stringify(savedRows) === JSON.stringify(rows)) return
    setEnabled(saved.enabled)
    setRows(savedRows)
    setSeriesSource(config?.metadata?.series_source === "tmdb" ? "tmdb" : "tvdb")
    setMetaLanguage(config?.metadata?.language || "en-US")
    setTvmazeAirDates(config?.metadata?.tvmaze_air_dates !== false)
    setSeeded(true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [registry, saved])

  const toggleMaster = (value) => {
    setEnabled(value)
    queueSave(value, rows)
  }
  const addCatalog = (id) => {
    if (rows.includes(id)) return
    const next = [...rows, id]
    setRows(next)
    queueSave(enabled, next)
  }
  const removeCatalog = (id) => {
    const next = rows.filter((rowID) => rowID !== id)
    setRows(next)
    queueSave(enabled, next)
  }
  const handleMove = (from, to) => {
    const next = moveItem(rows, from, to)
    setRows(next)
    queueSave(enabled, next)
  }

  // API keys save on blur, one field per patch — the backend keeps a key the
  // patch does not mention, and returns keys redacted, so the inputs start
  // blank even when a key is stored.
  const commitKey = (field, value) => {
    const trimmed = value.trim()
    if (trimmed === "") return
    onPersist({ [field]: trimmed })
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

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="flex items-center gap-2 text-lg font-medium text-foreground">
            <Clapperboard className="h-4 w-4" /> Metadata
          </h2>
          <p className="text-sm text-muted-foreground">
            Serve posters, episode lists and browsable catalogs straight from this server, so a Stremio-compatible
            client needs no other addon. Titles and artwork come from TMDB, air dates from TVMaze and Kitsu.
          </p>
        </div>
        <div className="flex h-8 items-center text-xs text-muted-foreground">
          {isSaving ? (
            <span className="flex items-center gap-1.5"><Loader2 className="h-3.5 w-3.5 animate-spin" /> Saving…</span>
          ) : saveStatus?.msg ? (
            <span className={cn(saveStatus.type === "error" && "text-destructive")}>{saveStatus.msg}</span>
          ) : null}
        </div>
      </div>

      <Card className="border border-border bg-card">
        <CardHeader className="pb-3">
          <CardTitle className="text-base font-semibold">Metadata provider</CardTitle>
          <CardDescription>
            When off, the addon serves streams only — exactly as before — and clients keep using Cinemeta or
            whichever metadata addon they already have.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex items-center justify-between gap-3">
            <Label htmlFor="metadata-enabled" className="text-sm">Enable catalogs &amp; metadata</Label>
            <Switch id="metadata-enabled" checked={enabled} onCheckedChange={toggleMaster} />
          </div>
          <div className="flex items-start gap-2 rounded-lg border border-border bg-muted/30 px-3.5 py-2.5">
            <Info className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
            <p className="text-xs text-muted-foreground">
              Changes save automatically. Clients cache the addon manifest — after changing the switch or the
              catalog list, refresh or reinstall the addon in your client for the change to show up.
            </p>
          </div>
        </CardContent>
      </Card>

      <Card className="border border-border bg-card">
        <CardHeader className="pb-3">
          <div className="flex flex-wrap items-start justify-between gap-2">
            <div>
              <CardTitle className="text-base font-semibold">Catalogs</CardTitle>
              <CardDescription>
                Rows appear in your client in this order. Drag to reorder.
                {!enabled && rows.length > 0 && " They will serve once the provider is enabled."}
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
                Clients still get posters and episode metadata for items they open, but no browse rows and no search.
              </p>
              <Button size="sm" className="mt-3" onClick={() => { setQuery(""); setAddOpen(true) }}>
                <Plus className="mr-2 h-4 w-4" /> Add catalog
              </Button>
            </div>
          ) : (
            <SortableList ids={rows} onMove={handleMove}>
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
                        onClick={() => removeCatalog(id)}
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
        <button
          type="button"
          className="flex w-full items-center justify-between gap-3 px-6 py-4 text-left"
          onClick={() => setConfigOpen((open) => !open)}
          aria-expanded={configOpen}
        >
          <span className="flex items-center gap-2">
            <KeyRound className="h-4 w-4 text-muted-foreground" />
            <span className="text-base font-semibold">Configuration</span>
          </span>
          <ChevronRight className={cn("h-4 w-4 shrink-0 text-muted-foreground transition-transform", configOpen && "rotate-90")} />
        </button>
        {configOpen && (
          <CardContent className="space-y-5 border-t border-border pt-4">
            <div className="space-y-3">
              <p className="text-sm font-medium">Metadata sources</p>
              <div className="grid gap-3 sm:grid-cols-3">
                <div className="space-y-1.5">
                  <Label htmlFor="metadata-source-movie" className="text-sm">Movies</Label>
                  <select id="metadata-source-movie" className={sourceSelectClass} value="tmdb" disabled>
                    <option value="tmdb">TMDB</option>
                  </select>
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="metadata-source-series" className="text-sm">Series</Label>
                  <select
                    id="metadata-source-series"
                    className={sourceSelectClass}
                    value={seriesSource}
                    onChange={(e) => {
                      setSeriesSource(e.target.value)
                      onPersist({ metadata: { series_source: e.target.value } })
                    }}
                  >
                    <option value="tvdb">TVDB (default)</option>
                    <option value="tmdb">TMDB</option>
                  </select>
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="metadata-source-anime" className="text-sm">Anime</Label>
                  <select id="metadata-source-anime" className={sourceSelectClass} value="kitsu" disabled>
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
                  className={sourceSelectClass}
                  value={metaLanguage}
                  onChange={(e) => {
                    setMetaLanguage(e.target.value)
                    onPersist({ metadata: { language: e.target.value } })
                  }}
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
              <div className="flex items-center justify-between gap-3 rounded-md border border-border/60 px-3 py-2.5">
                <div className="min-w-0">
                  <Label htmlFor="metadata-tvmaze-airdates" className="text-sm">TVMaze air dates</Label>
                  <p className="text-xs text-muted-foreground">
                    Override episode air dates with TVMaze&apos;s exact air times, and skip searching episodes
                    that have not aired yet.
                  </p>
                </div>
                <Switch
                  id="metadata-tvmaze-airdates"
                  checked={tvmazeAirDates}
                  onCheckedChange={(value) => {
                    setTvmazeAirDates(value)
                    onPersist({ metadata: { tvmaze_air_dates: value } })
                  }}
                />
              </div>
            </div>

            <div className="space-y-1">
              <p className="text-sm font-medium">API keys</p>
              <p className="text-xs text-muted-foreground">
                Stored server-side and never shown back, so these start blank even when a key is saved.
                Leaving a field blank keeps the stored key. TVMaze and Kitsu need no key.
              </p>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="metadata-tmdb-key" className="flex items-center gap-1.5 text-sm">
                TMDB Read Access Token <EnvOverrideIndicator show={envOverrides.includes("tmdb_api_key")} />
              </Label>
              <PasswordInput
                id="metadata-tmdb-key"
                className="h-9 w-full font-mono text-xs"
                value={tmdbKey}
                onChange={(e) => setTmdbKey(e.target.value)}
                onBlur={() => commitKey("tmdb_api_key", tmdbKey)}
              />
              <p className="text-xs text-muted-foreground">
                Powers titles, artwork, episode lists and the TMDB catalogs.
              </p>
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
                onBlur={() => commitKey("tvdb_api_key", tvdbKey)}
              />
              <p className="text-xs text-muted-foreground">
                The default source for series artwork and episode lists, and the resolver for TVDB ids.
              </p>
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

      <Dialog open={addOpen} onOpenChange={setAddOpen}>
        <DialogContent className="max-w-lg p-4 sm:p-6">
          <DialogHeader>
            <DialogTitle>Add catalog</DialogTitle>
            <DialogDescription>Catalogs already on the page are hidden from the results.</DialogDescription>
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
                  onClick={() => addCatalog(def.id)}
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
