import React, { useEffect, useState, useMemo, useRef, useCallback } from 'react'
import { useForm, useFieldArray } from 'react-hook-form'
import { AlertTriangle, Network, SlidersHorizontal, Server, Globe, Search } from "lucide-react"
import { IndexerSettings } from "@/components/IndexerSettings"
import { ProviderSettings } from "@/components/ProviderSettings"
import { SearchQuerySettings } from "@/components/SearchQuerySettings"
import { NetworkSettingsSection } from "@/components/NetworkSettingsSection"
import { AdvancedSettingsSection } from "@/components/AdvancedSettingsSection"
import { apiFetch } from './api'
import { cn } from "@/lib/utils"

const TABS = [
  { id: 'network', label: 'Network', icon: Network },
  { id: 'indexers', label: 'Indexers', icon: Server },
  { id: 'providers', label: 'Providers', icon: Globe },
  { id: 'search_query', label: 'Search', icon: Search },
  { id: 'advanced', label: 'Advanced', icon: SlidersHorizontal },
]

const ACTIVE_TAB_STORAGE_KEY = 'streamnzb.settings.activeTab'
const NETWORK_TAB_FIELDS = [
  'addon_port',
  'addon_base_url',
  'proxy_enabled',
  'proxy_port',
  'proxy_host',
  'proxy_auth_user',
  'proxy_auth_pass',
  'indexer_query_header',
  'indexer_grab_header',
  'provider_header',
]
const ADVANCED_TAB_FIELDS = [
  'log_level',
  'keep_log_files',
  'memory_limit_mb',
  'availnzb_api_key',
  'availnzb_mode',
  'tmdb_api_key',
  'tvdb_api_key',
]

/** Map a dotted field name (e.g. "indexers.0.url") to a tab id. */
function fieldToTab(fieldName) {
  if (fieldName.startsWith('indexers')) {
    const searchQueryFields = [
      'movie_categories',
      'tv_categories',
      'extra_search_terms',
      'disable_id_search',
      'disable_string_search',
      'search_result_limit',
      'search_title_language',
    ]
    if (searchQueryFields.some((suffix) => fieldName.endsWith(`.${suffix}`))) return 'search_query'
    return 'indexers'
  }
  if (fieldName.startsWith('providers')) return 'providers'
  if (fieldName.startsWith('movie_search_queries') || fieldName.startsWith('series_search_queries')) return 'search_query'
  if (NETWORK_TAB_FIELDS.includes(fieldName)) return 'network'
  if (ADVANCED_TAB_FIELDS.includes(fieldName)) return 'advanced'
  return null
}

function isConfigTab(tabId) {
  return tabId === 'network' || tabId === 'advanced'
}

function pickConfigSlice(values, keys) {
  return keys.reduce((acc, key) => {
    acc[key] = values?.[key]
    return acc
  }, {})
}

function buildNamedValidationSummary(errors, prefix, items, fallbackLabel) {
  if (!errors) return ''
  const summaries = []
  const seen = new Set()

  Object.entries(errors).forEach(([path, message]) => {
    const match = path.match(new RegExp(`^${prefix}\\.(\\d+)\\.`))
    if (!match) return
    const index = Number(match[1])
    const item = Array.isArray(items) ? items[index] : null
    const label = item?.name || item?.host || item?.url || `${fallbackLabel} ${index + 1}`
    const summary = `${label}: ${message}`
    if (seen.has(summary)) return
    seen.add(summary)
    summaries.push(summary)
  })

  if (summaries.length === 0) return ''
  if (summaries.length === 1) return summaries[0]
  const visible = summaries.slice(0, 2)
  const remaining = summaries.length - visible.length
  return remaining > 0 ? `${visible.join(' | ')} | +${remaining} more` : visible.join(' | ')
}

function summarizeConfigErrors(errors, sourceTab, values) {
  if (!errors) return ''
  if (sourceTab === 'providers') {
    return buildNamedValidationSummary(errors, 'providers', values?.providers, 'Provider')
  }
  if (sourceTab === 'indexers') {
    return buildNamedValidationSummary(errors, 'indexers', values?.indexers, 'Indexer')
  }
  return ''
}

function Settings({ initialConfig, sendCommand, saveStatus, clearSaveStatus, isSaving, adminToken, indexerCaps, stats }) {
  const [activeTab, setActiveTab] = useState(() => {
    if (typeof window === 'undefined') return 'network'
    const savedTab = window.sessionStorage.getItem(ACTIVE_TAB_STORAGE_KEY) || 'network'
    return TABS.some((tab) => tab.id === savedTab) ? savedTab : 'network'
  })
  const [visibleFooterStatus, setVisibleFooterStatus] = useState(null)
  const [footerStatusVisible, setFooterStatusVisible] = useState(false)
  const [lastSettingsSaveCard, setLastSettingsSaveCard] = useState('')
  const [lastConfigSaveSource, setLastConfigSaveSource] = useState('network')
  const footerTimeoutRef = useRef(null)
  const footerHideTimeoutRef = useRef(null)
  const [loading, setLoading] = useState(!initialConfig)
  const [configSnapshot, setConfigSnapshot] = useState({})
  const [liveDevicesByName, setLiveDevicesByName] = useState(initialConfig?.devices || {})
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
      setLiveDevicesByName(initialConfig.devices || {})
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

  const showFooterStatus = (status, onHide) => {
    if (footerTimeoutRef.current) {
      window.clearTimeout(footerTimeoutRef.current)
      footerTimeoutRef.current = null
    }
    if (footerHideTimeoutRef.current) {
      window.clearTimeout(footerHideTimeoutRef.current)
      footerHideTimeoutRef.current = null
    }

    if (!status?.message) {
      setFooterStatusVisible(false)
      setVisibleFooterStatus(null)
      return
    }

    setVisibleFooterStatus(status)
    requestAnimationFrame(() => setFooterStatusVisible(true))
    if (status.type === 'error') return

    footerTimeoutRef.current = window.setTimeout(() => {
      setFooterStatusVisible(false)
      footerTimeoutRef.current = null
      footerHideTimeoutRef.current = window.setTimeout(() => {
        setVisibleFooterStatus(null)
        footerHideTimeoutRef.current = null
        onHide?.()
      }, 180)
    }, 2600)
  }

  const clearTransientStatus = useCallback(() => {
    showFooterStatus(null)
    clearSaveStatus?.()
  }, [clearSaveStatus])

  useEffect(() => () => {
    if (footerTimeoutRef.current) {
      window.clearTimeout(footerTimeoutRef.current)
    }
    if (footerHideTimeoutRef.current) {
      window.clearTimeout(footerHideTimeoutRef.current)
    }
  }, [])

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

  // Compute which tabs have validation errors from server
  const tabsWithErrors = useMemo(() => {
    if (saveStatus.type !== 'error') return new Set()
    const errs = saveStatus.errors
    if (!errs) return new Set()
    const tabs = new Set()
    Object.keys(errs).forEach((key) => tabs.add(fieldToTab(key)))
    return tabs
  }, [saveStatus.errors, saveStatus.type])

  // When errors arrive, switch to the first failing tab
  useEffect(() => {
    if (!isConfigTab(lastConfigSaveSource)) return
    if (tabsWithErrors.size > 0 && !tabsWithErrors.has(activeTab)) {
      const firstErrorTab = TABS.find((t) => tabsWithErrors.has(t.id))
      if (firstErrorTab) setActiveTab(firstErrorTab.id)
    }
  }, [activeTab, lastConfigSaveSource, tabsWithErrors])

  const errorCount = saveStatus.errors ? Object.keys(saveStatus.errors).length : 0
  const settingsCardTitles = {
    network: 'Network',
    advanced: 'Advanced',
    addon: 'Addon',
    proxy: 'NNTP Proxy Server',
    useragent: 'User-Agent',
    admin: 'Logs',
    memory: 'Memory & Cache',
    availnzb: 'AvailNZB',
    metadata: 'Metadata APIs',
  }
  const networkInitialValues = useMemo(
    () => pickConfigSlice(configSnapshot, NETWORK_TAB_FIELDS),
    [configSnapshot]
  )
  const advancedInitialValues = useMemo(
    () => pickConfigSlice(configSnapshot, ADVANCED_TAB_FIELDS),
    [configSnapshot]
  )
  const saveFooterMsg = saveStatus.type === 'error' && errorCount > 0
    ? `Validation failed — ${errorCount} field${errorCount > 1 ? 's' : ''} need${errorCount === 1 ? 's' : ''} attention`
    : saveStatus.type === 'success' && lastSettingsSaveCard
      ? `${settingsCardTitles[lastSettingsSaveCard] || 'Settings'} saved.`
      : saveStatus.type === 'normal' && lastConfigSaveSource
        ? `Saving ${settingsCardTitles[lastConfigSaveSource] || 'Settings'}...`
        : saveStatus.msg

  useEffect(() => {
    if (activeTab !== 'advanced' && activeTab !== 'network') return
    if (lastConfigSaveSource !== activeTab) {
      setVisibleFooterStatus(null)
      return
    }
    if (!saveFooterMsg) return
    showFooterStatus({ type: saveStatus.type, message: saveFooterMsg }, () => {
      clearSaveStatus()
      setLastSettingsSaveCard('')
    })
  }, [activeTab, clearSaveStatus, lastConfigSaveSource, saveFooterMsg, saveStatus.type])

  const onSubmit = useCallback(async (overrides = null, sourceTab = activeTab) => {
    try {
      const trimData = (obj) => {
        if (typeof obj !== 'object' || obj === null) return obj;
        if (Array.isArray(obj)) {
          return obj.map(item => trimData(item));
        }
        const newObj = {};
        for (const key in obj) {
          if (typeof obj[key] === 'string') {
            newObj[key] = obj[key].trim();
          } else if (typeof obj[key] === 'object') {
            newObj[key] = trimData(obj[key]);
          } else {
            newObj[key] = obj[key];
          }
        }
        return newObj;
      };

      const baseValues = {
        ...configSnapshot,
        providers: getValues('providers'),
        indexers: getValues('indexers'),
        movie_search_queries: getValues('movie_search_queries'),
        series_search_queries: getValues('series_search_queries'),
      }
      const nextValues = overrides ? { ...baseValues, ...overrides } : baseValues
      const trimmedFullData = trimData(nextValues)

      if (typeof trimmedFullData.memory_limit_mb !== 'number') {
        trimmedFullData.memory_limit_mb = Number(trimmedFullData.memory_limit_mb) || 0
      }
      const keepLog = Number(trimmedFullData.keep_log_files)
      trimmedFullData.keep_log_files = Math.min(50, Math.max(1, isNaN(keepLog) ? 9 : keepLog))

      const payload = overrides
        ? Object.keys(overrides).reduce((acc, key) => {
            acc[key] = trimmedFullData[key]
            return acc
          }, {})
        : trimmedFullData

      setLastConfigSaveSource(sourceTab)
      await sendCommand('save_config', payload)

      const nextInitialValues = overrides
        ? {
            ...configSnapshot,
            ...Object.keys(overrides).reduce((acc, key) => {
              acc[key] = trimmedFullData[key]
              return acc
            }, {}),
          }
        : trimmedFullData

      setConfigSnapshot(nextInitialValues)
      return true
    } catch (error) {
      const summary = summarizeConfigErrors(error?.fieldErrors, sourceTab, {
        providers: getValues('providers'),
        indexers: getValues('indexers'),
      })
      if (summary) {
        error.message = summary
      }
      console.error('Error saving configuration:', error)
      if (sourceTab !== 'network' && sourceTab !== 'advanced') {
        showFooterStatus({ type: 'error', message: error.message || 'Failed to save configuration.' })
      }
      setError('root', { message: 'Failed to save configuration: ' + error.message })
      throw error
    }
  }, [activeTab, configSnapshot, getValues, sendCommand, setError, showFooterStatus])

  const handleNetworkPersist = useCallback((payload, cardId = 'network') => {
    setLastSettingsSaveCard(cardId)
    return onSubmit(payload, 'network')
  }, [onSubmit])

  const handleAdvancedPersist = useCallback((payload, cardId = 'advanced') => {
    setLastSettingsSaveCard(cardId)
    return onSubmit(payload, 'advanced')
  }, [onSubmit])

  if (loading) return null

  const handleClearCache = async () => {
    showFooterStatus({ type: 'normal', message: 'Clearing search cache...' })
    try {
      const data = await apiFetch('/api/cache/clear', { method: 'POST' })
      showFooterStatus({ type: 'success', message: data?.message || 'Search cache cleared.' })
    } catch (error) {
      showFooterStatus({ type: 'error', message: error?.message || 'Failed to clear search cache.' })
    }
  }
  
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
          onDirtyChange={handleAdvancedDirtyChange}
          onProceedTabChange={handleAdvancedProceedTabChange}
          onPersist={handleAdvancedPersist}
            onClearCache={handleClearCache}
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
              devicesByName={liveDevicesByName}
              append={appendIndexer}
              update={updateIndexer}
              remove={removeIndexer}
              replace={replaceIndexers}
              onClearStatus={clearTransientStatus}
              onPersist={(nextIndexers) => onSubmit({ indexers: nextIndexers }, 'indexers')}
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
              devicesByName={liveDevicesByName}
              append={append}
              remove={remove}
              replace={replace}
              onClearStatus={clearTransientStatus}
              onPersist={(nextProviders) => onSubmit({ providers: nextProviders }, 'providers')}
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
              devicesByName={liveDevicesByName}
              onPersist={(overrides) => onSubmit(overrides, 'search_query')}
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
