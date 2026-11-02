import { useCallback, useEffect, useMemo, useRef, useState, memo } from 'react'
import { Database, LayoutDashboard, Server, Trash2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { apiFetch } from "@/api"
import {
  RANGE_PRESETS,
  formatDisplayDate,
  indexerRows as toIndexerRows,
  pick,
  providerRows as toProviderRows,
  rangeDays,
  rangeFromPreset,
  summarizeIndexers,
  summarizeProviders,
} from "@/lib/statistics"
import { RangePicker } from "./statistics/RangePicker"
import { OverviewTab } from "./statistics/OverviewTab"
import { IndexersTab } from "./statistics/IndexersTab"
import { ProvidersTab } from "./statistics/ProvidersTab"

const TABS = ['overview', 'indexers', 'providers']
const TAB_STORAGE_KEY = 'statisticsTab'
const EMPTY_HISTORY = { providers: [], indexers: [] }

function normalizeHistoryStats(data) {
  const providers = Array.isArray(data?.providers) ? [...data.providers] : []
  const indexers = Array.isArray(data?.indexers) ? [...data.indexers] : []
  providers.sort((a, b) => String(pick(a, 'provider_name', 'ProviderName', '')).localeCompare(String(pick(b, 'provider_name', 'ProviderName', ''))))
  indexers.sort((a, b) => String(pick(a, 'indexer_name', 'IndexerName', '')).localeCompare(String(pick(b, 'indexer_name', 'IndexerName', ''))))
  return { providers, indexers }
}

function initialTab() {
  const stored = localStorage.getItem(TAB_STORAGE_KEY)
  return TABS.includes(stored) ? stored : 'overview'
}

export const StatisticsPage = memo(function StatisticsPage() {
  const [historyStats, setHistoryStats] = useState(EMPTY_HISTORY)
  const [perfStats, setPerfStats] = useState(null)
  const [tab, setTab] = useState(initialTab)
  const [preset, setPreset] = useState('24h')
  const [activeRange, setActiveRange] = useState(() => rangeFromPreset('24h'))
  const [loading, setLoading] = useState(false)
  const [loadError, setLoadError] = useState('')
  const [deleteTarget, setDeleteTarget] = useState(null)
  const [deleting, setDeleting] = useState(false)
  const inFlightRef = useRef(false)
  const lastSignatureRef = useRef(JSON.stringify(EMPTY_HISTORY))

  const loadStats = useCallback(async (from, to, { background = false } = {}) => {
    if (inFlightRef.current) return
    inFlightRef.current = true
    if (!background) {
      setLoading(true)
      setLoadError('')
    }
    try {
      const query = new URLSearchParams()
      if (from) query.set('from', from)
      if (to) query.set('to', to)
      const [data, perfData] = await Promise.all([
        apiFetch(`/api/stats/history?${query.toString()}`),
        apiFetch('/api/stats/performance').catch(() => null),
      ])
      const normalized = normalizeHistoryStats(data)
      const signature = `${from || ''}:${to || ''}:${JSON.stringify(normalized)}`
      if (!background || signature !== lastSignatureRef.current) {
        lastSignatureRef.current = signature
        setHistoryStats(normalized)
      }
      if (perfData) {
        setPerfStats(perfData)
      }
    } catch (error) {
      if (!background) {
        setLoadError(error?.message || 'Failed to load statistics.')
        setHistoryStats(EMPTY_HISTORY)
        lastSignatureRef.current = JSON.stringify(EMPTY_HISTORY)
      }
    } finally {
      if (!background) {
        setLoading(false)
      }
      inFlightRef.current = false
    }
  }, [])

  const handleDeleteStats = useCallback(async () => {
    if (!deleteTarget) return
    setDeleting(true)
    try {
      const query = new URLSearchParams()
      query.set('type', deleteTarget.type)
      query.set('name', deleteTarget.name)
      if (activeRange.from) query.set('from', activeRange.from)
      if (activeRange.to) query.set('to', activeRange.to)
      await apiFetch(`/api/stats/history?${query.toString()}`, { method: 'DELETE' })
      setDeleteTarget(null)
      await loadStats(activeRange.from, activeRange.to)
    } catch (err) {
      console.error('Failed to delete statistics:', err)
    } finally {
      setDeleting(false)
    }
  }, [deleteTarget, activeRange, loadStats])

  useEffect(() => {
    void loadStats(activeRange.from, activeRange.to)
    // Only on mount; range changes load through the handlers below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    let timeoutId = null
    let cancelled = false
    const pollDelayMs = 7000

    const poll = async () => {
      if (cancelled) return
      if (document.hidden) {
        timeoutId = window.setTimeout(poll, pollDelayMs)
        return
      }
      await loadStats(activeRange.from, activeRange.to, { background: true })
      if (cancelled) return
      timeoutId = window.setTimeout(poll, pollDelayMs)
    }

    const handleVisibilityChange = () => {
      if (document.hidden || cancelled) return
      if (timeoutId != null) {
        window.clearTimeout(timeoutId)
        timeoutId = null
      }
      void poll()
    }

    timeoutId = window.setTimeout(poll, pollDelayMs)
    document.addEventListener('visibilitychange', handleVisibilityChange)

    return () => {
      cancelled = true
      document.removeEventListener('visibilitychange', handleVisibilityChange)
      if (timeoutId != null) window.clearTimeout(timeoutId)
    }
  }, [activeRange.from, activeRange.to, loadStats])

  const days = useMemo(() => rangeDays(activeRange), [activeRange])
  const indexers = useMemo(() => toIndexerRows(historyStats.indexers, { days }), [historyStats.indexers, days])
  const providers = useMemo(() => toProviderRows(historyStats.providers), [historyStats.providers])
  const indexerSummary = useMemo(() => summarizeIndexers(indexers), [indexers])
  const providerSummary = useMemo(() => summarizeProviders(providers), [providers])

  const rangeLabel = useMemo(() => {
    if (preset !== 'custom') return RANGE_PRESETS.find((p) => p.id === preset)?.label || 'All Time'
    const fromStr = formatDisplayDate(activeRange.from) || 'Beginning'
    const toStr = formatDisplayDate(activeRange.to) || 'Today'
    return `${fromStr} – ${toStr}`
  }, [activeRange, preset])

  const applyRange = (nextPreset, nextRange) => {
    setPreset(nextPreset)
    setActiveRange(nextRange)
    void loadStats(nextRange.from, nextRange.to)
  }

  const handleTabChange = (value) => {
    setTab(value)
    localStorage.setItem(TAB_STORAGE_KEY, value)
  }

  const deleteAction = (type) => (row) => (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      className="h-7 w-7 text-muted-foreground hover:text-destructive"
      title={`Delete statistics for ${row.name} in selected range`}
      aria-label={`Delete statistics for ${row.name}`}
      onClick={() => setDeleteTarget({ type, name: row.name })}
    >
      <Trash2 className="h-3.5 w-3.5" />
    </Button>
  )

  return (
    <div className="flex flex-col gap-4 py-4 md:gap-6 md:py-6 px-4 lg:px-6">
      <Tabs value={tab} onValueChange={handleTabChange} className="flex flex-col gap-4 md:gap-6">
        <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
          <TabsList className="h-10 w-full justify-start overflow-x-auto md:w-auto">
            <TabsTrigger value="overview" className="h-8"><LayoutDashboard className="h-4 w-4" />Overview</TabsTrigger>
            <TabsTrigger value="indexers" className="h-8"><Server className="h-4 w-4" />Indexers<span className="text-xs text-muted-foreground">{indexers.length}</span></TabsTrigger>
            <TabsTrigger value="providers" className="h-8"><Database className="h-4 w-4" />Providers<span className="text-xs text-muted-foreground">{providers.length}</span></TabsTrigger>
          </TabsList>
          <div className="flex items-center justify-between gap-3 md:justify-end">
            <span className="text-xs text-muted-foreground">
              {loading ? 'Refreshing…' : <>Showing <span className="font-medium text-foreground">{rangeLabel}</span></>}
            </span>
            <RangePicker
              preset={preset}
              loading={loading}
              onSelectPreset={(id) => applyRange(id, rangeFromPreset(id))}
              onApplyCustom={(range) => applyRange('custom', range)}
            />
          </div>
        </div>

        {loadError && (
          <Card>
            <CardContent className="pt-6 text-sm text-destructive">{loadError}</CardContent>
          </Card>
        )}

        <TabsContent value="overview" className="mt-0">
          <OverviewTab indexerSummary={indexerSummary} providerSummary={providerSummary} perfStats={perfStats} />
        </TabsContent>
        <TabsContent value="indexers" className="mt-0">
          <IndexersTab rows={indexers} summary={indexerSummary} actions={deleteAction('indexer')} />
        </TabsContent>
        <TabsContent value="providers" className="mt-0">
          <ProvidersTab rows={providers} summary={providerSummary} actions={deleteAction('provider')} />
        </TabsContent>
      </Tabs>

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => {
          if (!open && !deleting) setDeleteTarget(null)
        }}
        title={`Delete ${deleteTarget?.type === 'provider' ? 'Provider' : 'Indexer'} Statistics?`}
        description={deleteTarget ? `Are you sure you want to delete statistics for ${deleteTarget.type} "${deleteTarget.name}" in the selected range (${rangeLabel})? This action cannot be undone.` : ''}
        confirmLabel="Delete"
        onConfirm={handleDeleteStats}
      />
    </div>
  )
})
