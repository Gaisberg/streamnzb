import React, { useEffect, useMemo, useState } from "react"
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
import { ChevronRight, Clapperboard, Info, KeyRound, Plus, Search, ShieldCheck, TriangleAlert, X } from "lucide-react"
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
  return bits.join(" · ")
}

// profileUsage maps a profile name to the streams bound to it. A profile that
// appears nowhere here serves nothing.
function profileUsage(streams = {}) {
  const usage = {}
  Object.entries(streams).forEach(([streamName, stream]) => {
    const key = (stream?.metadata_profile_name || "").trim().toLowerCase()
    if (!key) return
    if (!usage[key]) usage[key] = []
    if (!usage[key].includes(streamName)) usage[key].push(streamName)
  })
  return usage
}

// describeDelete spells out the knock-on effect, since deleting a profile
// also clears it from any stream bound to it.
function describeDelete(profile, usage) {
  const name = profile?.name || ""
  const used = usage[name.trim().toLowerCase()]
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
function MetadataProfileEditor({ draft, onChange, registry, registryError, certOptions }) {
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
              className={sourceSelectClass}
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
              <select id="metadata-source-movie" className={sourceSelectClass} value="tmdb" disabled>
                <option value="tmdb">TMDB</option>
              </select>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="metadata-source-series" className="text-sm">Series</Label>
              <select
                id="metadata-source-series"
                className={sourceSelectClass}
                value={draft.series_source === "tmdb" ? "tmdb" : "tvdb"}
                onChange={(e) => onChange({ ...draft, series_source: e.target.value === "tmdb" ? "tmdb" : undefined })}
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

export function MetadataPage({ config, onPersist, isSaving, saveStatus }) {
  const envOverrides = config?.env_overrides ?? []
  // null means the backend has not migrated yet; treat as none, never PUT
  // null back — every save sends a real array.
  const profiles = useMemo(() => config?.metadata_profiles || [], [config])
  const usage = useMemo(() => profileUsage(config?.streams || {}), [config])
  const anyInUse = Object.keys(usage).length > 0

  const [registry, setRegistry] = useState([])
  const [registryError, setRegistryError] = useState(false)
  const [certOptions, setCertOptions] = useState([])
  const [keysOpen, setKeysOpen] = useState(false)
  const [tmdbKey, setTmdbKey] = useState("")
  const [tvdbKey, setTvdbKey] = useState("")

  useEffect(() => {
    let cancelled = false
    apiFetch("/api/metadata/catalogs")
      .then((data) => { if (!cancelled) setRegistry(Array.isArray(data) ? data : []) })
      .catch(() => { if (!cancelled) setRegistryError(true) })
    apiFetch("/api/metadata/certifications")
      .then((data) => { if (!cancelled) setCertOptions(Array.isArray(data) ? data : []) })
      .catch(() => { /* the dropdown degrades to "No limit" only */ })
    return () => { cancelled = true }
  }, [])

  // API keys save on blur, one field per patch — the backend keeps a key the
  // patch does not mention, and returns keys redacted, so the inputs start
  // blank even when a key is stored.
  const commitKey = (field, value) => {
    const trimmed = value.trim()
    if (trimmed === "") return
    onPersist({ [field]: trimmed })
  }

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
              Leaving a field blank keeps the stored key. TVMaze and Kitsu need no key.
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
    </div>
  )
}
