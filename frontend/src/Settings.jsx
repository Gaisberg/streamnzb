import React, { useEffect, useState, useRef, useCallback } from 'react'
import { useForm, useFieldArray } from 'react-hook-form'
import { AlertTriangle, Network, SlidersHorizontal, Server, Globe, Search, Loader2, Save, Filter } from "lucide-react"
import { IndexerSettings } from "@/components/IndexerSettings"
import { ProviderSettings } from "@/components/ProviderSettings"
import { SearchQuerySettings } from "@/components/SearchQuerySettings"
import { NetworkSettingsSection } from "@/components/NetworkSettingsSection"
import { AdvancedSettingsSection } from "@/components/AdvancedSettingsSection"
import { cn } from "@/lib/utils"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { useSettingsState } from '@/hooks/useSettingsState'
import { normalizeAvailNZBMode } from '@/lib/availnzb'
import { normalizeQueryYearSetting, normalizeSearchTitleLanguage, normalizeSearchTitleLanguages } from '@/lib/config'

const TABS = [
  { id: 'network', label: 'Network', icon: Network },
  { id: 'indexers', label: 'Indexers', icon: Server },
  { id: 'providers', label: 'Providers', icon: Globe },
  { id: 'search_query', label: 'Search', icon: Search },
  { id: 'advanced', label: 'Advanced', icon: SlidersHorizontal },
]

const ACTIVE_TAB_STORAGE_KEY = 'streamnzb.settings.activeTab'

function normalizeSeriesScopeFromLegacy(scope, legacyUseSeasonEpisodeParams) {
  const normalizedScope = String(scope || '').trim().toLowerCase()
  if (normalizedScope) return normalizedScope
  if (legacyUseSeasonEpisodeParams != null) return 'season_episode'
  return ''
}

function normalizeSearchQueryLanguages(query) {
  const searchMode = String(query?.search_mode || '').trim().toLowerCase()
  const searchTitleLanguage = normalizeSearchTitleLanguage(query?.search_title_language || '')
  const searchTitleLanguages = normalizeSearchTitleLanguages(query?.search_title_languages)
  if (searchMode === 'id') {
    if (searchTitleLanguages.length > 0) return searchTitleLanguages
    if (searchTitleLanguage === '') return ['en-US', '']
    return [searchTitleLanguage]
  }
  return []
}

function Settings({
  initialConfig,
  sendCommand,
  saveStatus,
  clearSaveStatus,
  isSaving,
  onRefreshAvailNZBStatus,
  indexerCaps,
  stats,
  activeTab: activeTabProp,
  hideTabs = false,
}) {
  const [internalActiveTab, setInternalActiveTab] = useState(() => {
    if (typeof window === 'undefined') return 'network'
    const savedTab = window.sessionStorage.getItem(ACTIVE_TAB_STORAGE_KEY) || 'network'
    return TABS.some((tab) => tab.id === savedTab) ? savedTab : 'network'
  })

  const activeTab = activeTabProp || internalActiveTab
  const setActiveTab = (tab) => {
    setInternalActiveTab(tab)
    if (typeof window !== 'undefined') {
      window.sessionStorage.setItem(ACTIVE_TAB_STORAGE_KEY, tab)
    }
  }
  const [loading, setLoading] = useState(!initialConfig)
  const [configSnapshot, setConfigSnapshot] = useState({})
  const [liveStreamsByName, setLiveStreamsByName] = useState(initialConfig?.streams || {})
  const [globalIndexerProxyURL, setGlobalIndexerProxyURL] = useState('')
  const [savingGlobalIndexerProxy, setSavingGlobalIndexerProxy] = useState(false)
  const networkSectionRef = useRef(null)
  const advancedSectionRef = useRef(null)

  const form = useForm({
    defaultValues: {
      providers: [],
      indexers: [],
      movie_search_queries: [],
      series_search_queries: [],
      filter_profiles: []
    }
  })

  const envOverrides = initialConfig?.env_overrides ?? []
  const { control, reset, setError, clearErrors, watch, getValues } = form
  const { fields, append, remove, replace } = useFieldArray({
    control,
    name: 'providers'
  })
  
  const { fields: indexerFields, append: appendIndexer, remove: removeIndexer, update: updateIndexer, replace: replaceIndexers } = useFieldArray({
    control,
    name: 'indexers'
  })

  const { fields: movieSearchQueryFields, append: appendMovieSearchQuery, remove: removeMovieSearchQuery, update: updateMovieSearchQuery } = useFieldArray({
    control,
    name: 'movie_search_queries'
  })

  const { fields: seriesSearchQueryFields, append: appendSeriesSearchQuery, remove: removeSeriesSearchQuery, update: updateSeriesSearchQuery } = useFieldArray({
    control,
    name: 'series_search_queries'
  })

  useEffect(() => {
    if (initialConfig) {
      const { env_overrides: _envOverrides, admin_username: _au, admin_password: _ap, ...configForForm } = initialConfig
      const formattedData = {
        ...configForForm,
        addon_port: Number(initialConfig.addon_port),
        proxy_port: Number(initialConfig.proxy_port),
        proxy_enabled: initialConfig.proxy_enabled !== false,
        availnzb_mode: normalizeAvailNZBMode(initialConfig.availnzb_mode),
        tmdb_api_key: initialConfig.tmdb_api_key ?? '',
        tvdb_api_key: initialConfig.tvdb_api_key ?? '',
        indexer_query_header: initialConfig.indexer_query_header ?? '',
        indexer_grab_header: initialConfig.indexer_grab_header ?? '',
        provider_header: initialConfig.provider_header ?? '',
        movie_categories: initialConfig.movie_categories ?? '',
        tv_categories: initialConfig.tv_categories ?? '',
        extra_search_terms: initialConfig.extra_search_terms ?? '',
        memory_limit_mb: Number(initialConfig.memory_limit_mb || 0),
        keep_log_files: Number(initialConfig.keep_log_files ?? 9) || 9,
        nzb_history_retention_days: initialConfig.nzb_history_retention_days == null ? 90 : Number(initialConfig.nzb_history_retention_days),
        session_ttl_minutes: initialConfig.session_ttl_minutes == null ? 30 : Number(initialConfig.session_ttl_minutes),
        session_post_playback_ttl_minutes: initialConfig.session_post_playback_ttl_minutes == null ? 240 : Number(initialConfig.session_post_playback_ttl_minutes),
        speculative_pre_probing_count: initialConfig.speculative_pre_probing_count == null ? 1 : Number(initialConfig.speculative_pre_probing_count),
        mute_error_video: initialConfig.mute_error_video === true,
        providers: initialConfig.providers?.map((p, index) => ({
          ...p,
          priority: p.priority != null ? p.priority : index + 1,
          enabled: p.enabled != null ? p.enabled : true,
          port: Number(p.port),
          connections: Number(p.connections)
        })) || [],
        indexers: initialConfig.indexers?.map(idx => ({
          ...idx,
          enabled: idx.enabled != null ? idx.enabled : true,
          api_path: idx.api_path || '/api',
          api_hits_day: Number(idx.api_hits_day || 0),
	          downloads_day: Number(idx.downloads_day || 0),
	          rate_limit_rps: Number(idx.rate_limit_rps || 0),
	          timeout_seconds: Number(idx.timeout_seconds || 0),
          username: idx.username || '',
          password: idx.password || '',
          disable_id_search: idx.disable_id_search === true,
          disable_string_search: idx.disable_string_search === true
        })) || [],
        movie_search_queries: initialConfig.movie_search_queries?.map((query) => ({
          name: query.name || '',
          search_mode: query.search_mode || 'id',
          movie_categories: query.movie_categories || '',
          search_result_limit: Number(query.search_result_limit || 0),
          search_title_language: normalizeSearchTitleLanguage(query.search_title_language || ''),
          search_title_languages: normalizeSearchQueryLanguages(query),
          include_year: normalizeQueryYearSetting(query.search_mode, query.include_year, query.include_year_in_text_search),
        })) || [],
        series_search_queries: initialConfig.series_search_queries?.map((query) => ({
          name: query.name || '',
          search_mode: query.search_mode || 'id',
          tv_categories: query.tv_categories || '',
          search_result_limit: Number(query.search_result_limit || 0),
          search_title_language: normalizeSearchTitleLanguage(query.search_title_language || ''),
          search_title_languages: normalizeSearchQueryLanguages(query),
          include_year: normalizeQueryYearSetting(query.search_mode, query.include_year, query.include_year_in_text_search),
          series_search_scope: normalizeSeriesScopeFromLegacy(query.series_search_scope, query.use_season_episode_params),
        })) || [],
        filter_profiles: initialConfig.filter_profiles?.map((profile) => ({
          name: profile.name || '',
          allowed_resolutions: profile.allowed_resolutions || [],
          blocked_resolutions: profile.blocked_resolutions || [],
          allowed_qualities: profile.allowed_qualities || [],
          blocked_qualities: profile.blocked_qualities || [],
          allowed_codecs: profile.allowed_codecs || [],
          blocked_codecs: profile.blocked_codecs || [],
          require_hdr: !!profile.require_hdr,
          allowed_hdrs: profile.allowed_hdrs || [],
          blocked_hdrs: profile.blocked_hdrs || [],
          required_keywords: profile.required_keywords || [],
          excluded_keywords: profile.excluded_keywords || [],
          allowed_languages: profile.allowed_languages || [],
          blocked_languages: profile.blocked_languages || [],
          preferred_languages: profile.preferred_languages || [],
          sort_order: profile.sort_order || [],
        })) || [],
      }
      reset({
        providers: formattedData.providers,
        indexers: formattedData.indexers,
        movie_search_queries: formattedData.movie_search_queries,
        series_search_queries: formattedData.series_search_queries,
        filter_profiles: formattedData.filter_profiles,
      })
      setConfigSnapshot(formattedData)
      const normalizedProxyURL = formattedData.indexer_proxy_url || ''
      setGlobalIndexerProxyURL(normalizedProxyURL)
      setLiveStreamsByName(initialConfig.streams || {})
      setLoading(false)
    }
  }, [initialConfig, reset])

  useEffect(() => {
    if (saveStatus.type === 'error' && saveStatus.errors) {
      Object.keys(saveStatus.errors).forEach((key) => {
        setError(key, { type: 'server', message: saveStatus.errors[key] })
      })
      return
    }
    clearErrors()
  }, [clearErrors, saveStatus.errors, saveStatus.type, setError])

  useEffect(() => {
    if (typeof window === 'undefined') return
    window.sessionStorage.setItem(ACTIVE_TAB_STORAGE_KEY, activeTab)
  }, [activeTab])

  useEffect(() => {
    const normalizedProxyURL = configSnapshot?.indexer_proxy_url || ''
    setGlobalIndexerProxyURL(normalizedProxyURL)
  }, [configSnapshot?.indexer_proxy_url])

  const handleTabChange = (nextTab) => {
    if (nextTab === activeTab) return
    if (typeof document !== 'undefined' && document.activeElement instanceof HTMLElement) {
      document.activeElement.blur()
    }
    window.setTimeout(() => {
      if (activeTab === 'network') {
        networkSectionRef.current?.requestTabChange?.(nextTab)
        return
      }
      if (activeTab === 'advanced') {
        advancedSectionRef.current?.requestTabChange?.(nextTab)
        return
      }
      clearTransientStatus()
      setActiveTab(nextTab)
    }, 0)
  }

  const {
    advancedInitialValues,
    clearTransientStatus,
    footerStatusVisible,
    handleAdvancedPersist,
    handleClearCache,
    handleNetworkPersist,
    networkInitialValues,
    showFooterStatus,
    submitSettings,
    tabsWithErrors,
    visibleFooterStatus,
  } = useSettingsState({
    activeTab,
    clearSaveStatus,
    configSnapshot,
    getValues,
    saveStatus,
    sendCommand,
    setActiveTab,
    setConfigSnapshot,
    setError,
  })

  const handleNetworkDirtyChange = useCallback(() => {}, [])

  const handleAdvancedDirtyChange = useCallback(() => {}, [])

  const handleNetworkProceedTabChange = useCallback((nextTab) => {
    clearTransientStatus()
    setActiveTab(nextTab)
  }, [clearTransientStatus])

  const handleAdvancedProceedTabChange = useCallback((nextTab) => {
    clearTransientStatus()
    setActiveTab(nextTab)
  }, [clearTransientStatus])

  if (loading) return null
  
  return (
      <div className="pb-10">
        {/* Tab bar */}
        {!hideTabs && (
          <div className="flex items-center gap-1 border-b border-border mb-6 -mt-1 overflow-x-auto">
            {TABS.map((tab) => {
              const Icon = tab.icon
              const hasError = tabsWithErrors.has(tab.id)
              return (
                <button
                  key={tab.id}
                  type="button"
                  onClick={() => handleTabChange(tab.id)}
                  className={cn(
                    'relative flex items-center gap-1.5 px-3 py-2 text-sm font-medium whitespace-nowrap transition-colors',
                    'hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded-t-md',
                    activeTab === tab.id
                      ? 'text-foreground after:absolute after:bottom-0 after:inset-x-0 after:h-0.5 after:bg-primary'
                      : 'text-muted-foreground',
                    hasError && 'text-destructive'
                  )}
                >
                  <Icon className="h-4 w-4" />
                  {tab.label}
                  {hasError && (
                    <span className="flex h-2 w-2 rounded-full bg-destructive" />
                  )}
                </button>
              )
            })}
          </div>
        )}

        {activeTab === 'network' && (
        <NetworkSettingsSection
          ref={networkSectionRef}
          initialValues={networkInitialValues}
          envOverrides={envOverrides}
          isSaving={isSaving}
          onDirtyChange={handleNetworkDirtyChange}
          onProceedTabChange={handleNetworkProceedTabChange}
          onPersist={handleNetworkPersist}
          saveStatus={saveStatus}
          />
        )}

        {activeTab === 'advanced' && (
        <AdvancedSettingsSection
          ref={advancedSectionRef}
          initialValues={advancedInitialValues}
          envOverrides={envOverrides}
          isSaving={isSaving}
          onDirtyChange={handleAdvancedDirtyChange}
          onProceedTabChange={handleAdvancedProceedTabChange}
          onPersist={handleAdvancedPersist}
          onClearCache={handleClearCache}
          onRefreshAvailNZBStatus={onRefreshAvailNZBStatus}
          saveStatus={saveStatus}
          />
        )}

        {activeTab === 'indexers' && (
          <div className="space-y-4">
            {envOverrides.includes('indexers') && (
              <div className="rounded-lg border border-border bg-muted/50 p-3">
                <p className="text-sm text-muted-foreground flex items-center gap-2">
                  <AlertTriangle className="h-4 w-4 shrink-0" />
                  Indexer list is overwritten by environment variables (INDEXER_1_*, etc.) on restart.
                </p>
              </div>
            )}
            <Card>
              <CardHeader>
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0 flex-1 max-w-[26rem] space-y-0.5">
                    <CardTitle>Default Proxy</CardTitle>
                    <CardDescription>Global HTTP(S) proxy used by indexers when a per-indexer proxy is not set.</CardDescription>
                  </div>
                  <TooltipProvider delayDuration={100}>
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <Button
                          type="button"
                          variant="destructive"
                          size="icon"
                          className="h-9 w-9 shrink-0"
                          onClick={async () => {
                            setSavingGlobalIndexerProxy(true)
                            try {
                              const payload = { indexer_proxy_url: globalIndexerProxyURL }
                              await submitSettings(payload, 'indexers')
                              showFooterStatus({ type: 'success', message: `Global indexer proxy saved.${' Search cache cleared.'}` })
                            } catch (error) {
                              showFooterStatus({ type: 'error', message: error?.message || 'Failed to save global indexer proxy.' })
                            } finally {
                              setSavingGlobalIndexerProxy(false)
                            }
                          }}
                          disabled={isSaving || savingGlobalIndexerProxy}
                        >
                          {(isSaving || savingGlobalIndexerProxy)
                            ? <Loader2 className="h-4 w-4 animate-spin" />
                            : <Save className="h-4 w-4" />}
                        </Button>
                      </TooltipTrigger>
                      <TooltipContent>Save</TooltipContent>
                    </Tooltip>
                  </TooltipProvider>
                </div>
              </CardHeader>
              <CardContent className="space-y-3">
                <div className="flex flex-col gap-2">
                  <Label htmlFor="indexer-global-proxy">HTTP(S) Proxy URL</Label>
                  <Input
                    id="indexer-global-proxy"
                    value={globalIndexerProxyURL}
                    onChange={(event) => setGlobalIndexerProxyURL(event.target.value)}
                    placeholder="http://proxy:8888"
                    autoComplete="off"
                  />
                </div>
              </CardContent>
            </Card>
            <IndexerSettings
              fields={indexerFields}
              defaultProxyURL={globalIndexerProxyURL}
              indexerCaps={indexerCaps || {}}
              stats={stats}
              streamsByName={liveStreamsByName}
              append={appendIndexer}
              update={updateIndexer}
              remove={removeIndexer}
              replace={replaceIndexers}
              onClearStatus={clearTransientStatus}
              onPersist={(nextIndexers) => submitSettings({ indexers: nextIndexers }, 'indexers')}
              onStatus={(status) => showFooterStatus(status)}
            />
          </div>
        )}

        {activeTab === 'providers' && (
          <div className="space-y-4">
            {envOverrides.includes('providers') && (
              <div className="rounded-lg border border-border bg-muted/50 p-3">
                <p className="text-sm text-muted-foreground flex items-center gap-2">
                  <AlertTriangle className="h-4 w-4 shrink-0" />
                  Provider list is overwritten by environment variables (PROVIDER_1_*, etc.) on restart.
                </p>
              </div>
            )}
            <ProviderSettings
              fields={fields}
              stats={stats}
              streamsByName={liveStreamsByName}
              append={append}
              remove={remove}
              replace={replace}
              onClearStatus={clearTransientStatus}
              onPersist={(nextProviders) => submitSettings({ providers: nextProviders }, 'providers')}
              onStatus={(status) => showFooterStatus(status)}
            />
          </div>
        )}

        {activeTab === 'search_query' && (
          <div className="space-y-4">
            <SearchQuerySettings
              control={control}
              watch={watch}
              movieFields={movieSearchQueryFields}
              seriesFields={seriesSearchQueryFields}
              appendMovie={appendMovieSearchQuery}
              appendSeries={appendSeriesSearchQuery}
              removeMovie={removeMovieSearchQuery}
              removeSeries={removeSeriesSearchQuery}
              updateMovie={updateMovieSearchQuery}
              updateSeries={updateSeriesSearchQuery}
              streamsByName={liveStreamsByName}
              onPersist={(overrides) => submitSettings(overrides, 'search_query')}
              onStatus={(status) => showFooterStatus(status)}
              onClearStatus={clearTransientStatus}
            />
          </div>
        )}

        {visibleFooterStatus?.message && (
          <div className={cn(
            "fixed bottom-4 left-4 right-4 z-40 transition-all duration-200 ease-out md:left-[calc(var(--sidebar-width)+1rem)]",
            footerStatusVisible ? "translate-y-0 opacity-100" : "translate-y-2 opacity-0"
          )}>
            <div className={cn(
              "mx-auto w-full rounded-md border px-4 py-3 text-sm shadow-lg backdrop-blur supports-[backdrop-filter]:bg-background/90",
              visibleFooterStatus.type === 'error'
                ? "border-destructive/30 bg-destructive/10 text-destructive"
                : visibleFooterStatus.type === 'success'
                  ? "border-green-500/30 bg-green-50 text-green-700"
                  : "border-border bg-background/95 text-muted-foreground"
            )}>
              {visibleFooterStatus.message}
            </div>
          </div>
        )}
      </div>
  )
}

export default Settings
