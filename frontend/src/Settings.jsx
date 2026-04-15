import React, { useEffect, useState, useMemo, useRef, useCallback } from 'react'
import { useForm, useFieldArray } from 'react-hook-form'
import { AlertTriangle, Network, SlidersHorizontal, Server, Globe, Search } from "lucide-react"
import { IndexerSettings } from "@/components/IndexerSettings"
import { ProviderSettings } from "@/components/ProviderSettings"
import { SearchQuerySettings } from "@/components/SearchQuerySettings"
import { NetworkSettingsSection } from "@/components/NetworkSettingsSection"
import { AdvancedSettingsSection } from "@/components/AdvancedSettingsSection"
import { cn } from "@/lib/utils"
import { useSettingsState } from './hooks/useSettingsState'

const TABS = [
  { id: 'network', label: 'Network', icon: Network },
  { id: 'indexers', label: 'Indexers', icon: Server },
  { id: 'providers', label: 'Providers', icon: Globe },
  { id: 'search_query', label: 'Search', icon: Search },
  { id: 'advanced', label: 'Advanced', icon: SlidersHorizontal },
]

const ACTIVE_TAB_STORAGE_KEY = 'streamnzb.settings.activeTab'

function Settings({
  initialConfig,
  sendCommand,
  saveStatus,
  clearSaveStatus,
  isSaving,
  availNZBStatus,
  availNZBStatusLoading,
  availNZBStatusError,
  onRefreshAvailNZBStatus,
  adminToken,
  indexerCaps,
  stats,
}) {
  const [activeTab, setActiveTab] = useState(() => {
    if (typeof window === 'undefined') return 'network'
    const savedTab = window.sessionStorage.getItem(ACTIVE_TAB_STORAGE_KEY) || 'network'
    return TABS.some((tab) => tab.id === savedTab) ? savedTab : 'network'
  })
  const [loading, setLoading] = useState(!initialConfig)
  const [configSnapshot, setConfigSnapshot] = useState({})
  const [liveStreamsByName, setLiveStreamsByName] = useState(initialConfig?.streams || {})
  const networkSectionRef = useRef(null)
  const advancedSectionRef = useRef(null)

  const form = useForm({
    defaultValues: {
      providers: [],
      indexers: [],
      movie_search_queries: [],
      series_search_queries: []
    }
  })

  const envOverrides = initialConfig?.env_overrides ?? []
  const { control, handleSubmit, reset, setError, clearErrors, formState, setValue, watch, getValues } = form
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
        availnzb_api_key: initialConfig.availnzb_api_key ?? '',
        tmdb_api_key: initialConfig.tmdb_api_key ?? '',
        tvdb_api_key: initialConfig.tvdb_api_key ?? '',
        indexer_query_header: initialConfig.indexer_query_header ?? '',
        indexer_grab_header: initialConfig.indexer_grab_header ?? '',
        provider_header: initialConfig.provider_header ?? '',
        movie_categories: initialConfig.movie_categories ?? '',
        tv_categories: initialConfig.tv_categories ?? '',
        extra_search_terms: initialConfig.extra_search_terms ?? '',
        use_season_episode_params: initialConfig.use_season_episode_params,
        memory_limit_mb: Number(initialConfig.memory_limit_mb || 0),
        keep_log_files: Number(initialConfig.keep_log_files ?? 9) || 9,
        nzb_history_retention_days: initialConfig.nzb_history_retention_days == null ? 90 : Number(initialConfig.nzb_history_retention_days),
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
          ...query,
          name: query.name || '',
          search_mode: query.search_mode || 'id',
          movie_categories: query.movie_categories || '',
          extra_search_terms: query.extra_search_terms || '',
          search_result_limit: Number(query.search_result_limit || 0),
          search_title_language: query.search_title_language || '',
          include_year_in_text_search: query.include_year_in_text_search ?? true,
        })) || [],
        series_search_queries: initialConfig.series_search_queries?.map((query) => ({
          ...query,
          name: query.name || '',
          search_mode: query.search_mode || 'id',
          tv_categories: query.tv_categories || '',
          extra_search_terms: query.extra_search_terms || '',
          search_result_limit: Number(query.search_result_limit || 0),
          search_title_language: query.search_title_language || '',
          include_year_in_text_search: query.include_year_in_text_search ?? true,
          use_season_episode_params: query.use_season_episode_params ?? true,
        })) || []
      }
      reset({
        providers: formattedData.providers,
        indexers: formattedData.indexers,
        movie_search_queries: formattedData.movie_search_queries,
        series_search_queries: formattedData.series_search_queries,
      })
      setConfigSnapshot(formattedData)
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

        {activeTab === 'network' && (
        <NetworkSettingsSection
          ref={networkSectionRef}
          initialValues={networkInitialValues}
          envOverrides={envOverrides}
          isSaving={isSaving}
          onDirtyChange={handleNetworkDirtyChange}
          onProceedTabChange={handleNetworkProceedTabChange}
          onPersist={handleNetworkPersist}
          />
        )}

        {activeTab === 'advanced' && (
        <AdvancedSettingsSection
          ref={advancedSectionRef}
          initialValues={advancedInitialValues}
          envOverrides={envOverrides}
          isSaving={isSaving}
          availNZBStatus={availNZBStatus}
          availNZBStatusLoading={availNZBStatusLoading}
          availNZBStatusError={availNZBStatusError}
          onDirtyChange={handleAdvancedDirtyChange}
          onProceedTabChange={handleAdvancedProceedTabChange}
          onPersist={handleAdvancedPersist}
          onClearCache={handleClearCache}
          onRefreshAvailNZBStatus={onRefreshAvailNZBStatus}
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
            <IndexerSettings
              fields={indexerFields}
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
