import { useCallback, useEffect, useMemo, useRef, useState, memo } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from "@/components/ui/chart"
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Database, Gauge, Zap, Clock, Activity, PlayCircle, Calendar, CalendarDays, ChevronDown, Check, Trash2 } from "lucide-react"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { apiFetch } from "@/api"
import { cn } from "@/lib/utils"

const PRESETS = [
  { id: '24h', label: 'Last 24 Hours', description: 'Past 24 hours of snapshots' },
  { id: '7d', label: 'Last 7 Days', description: 'Past 7 days of snapshots' },
  { id: '30d', label: 'Last 30 Days', description: 'Past 30 days of snapshots' },
  { id: '90d', label: 'Last 90 Days', description: 'Past quarter of snapshots' },
  { id: 'mtd', label: 'Month to Date', description: 'From 1st of this month' },
  { id: 'all', label: 'All Time', description: 'Complete recorded history' },
]

const providerChartConfig = {
  response: {
    label: "Response (ms)",
    color: "hsl(var(--primary))",
  },
  downloaded: {
    label: "Downloaded (MB)",
    color: "hsl(var(--primary))",
  },
}

function toNumber(value, fallback = 0) {
  const n = Number(value)
  return Number.isFinite(n) ? n : fallback
}

function pick(obj, snakeKey, pascalKey, fallback = undefined) {
  if (!obj || typeof obj !== 'object') return fallback
  if (obj[snakeKey] != null) return obj[snakeKey]
  if (obj[pascalKey] != null) return obj[pascalKey]
  return fallback
}

function normalizeHistoryStats(data) {
  const providers = Array.isArray(data?.providers) ? [...data.providers] : []
  const indexers = Array.isArray(data?.indexers) ? [...data.indexers] : []
  providers.sort((a, b) => String(pick(a, 'provider_name', 'ProviderName', '')).localeCompare(String(pick(b, 'provider_name', 'ProviderName', ''))))
  indexers.sort((a, b) => String(pick(a, 'indexer_name', 'IndexerName', '')).localeCompare(String(pick(b, 'indexer_name', 'IndexerName', ''))))
  return { providers, indexers }
}

function buildHistorySignature(normalized) {
  return JSON.stringify(normalized || {})
}

function formatDownloadedMb(mb) {
  const n = toNumber(mb)
  if (n >= 1024) return `${(n / 1024).toFixed(2)} GB`
  return `${n.toFixed(1)} MB`
}

function formatDateInput(date) {
  const y = date.getFullYear()
  const m = String(date.getMonth() + 1).padStart(2, '0')
  const d = String(date.getDate()).padStart(2, '0')
  return `${y}-${m}-${d}`
}

function formatDisplayDate(dateStr) {
  if (!dateStr) return ''
  const parts = dateStr.split('-').map(Number)
  if (parts.length < 3) return dateStr
  const [y, m, d] = parts
  const date = new Date(y, m - 1, d)
  return date.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })
}

function defaultDateRange() {
  const end = new Date()
  const start = new Date(end)
  start.setDate(end.getDate() - 30)
  return {
    from: formatDateInput(start),
    to: formatDateInput(end),
  }
}

function rangeFromPreset(preset) {
  const end = new Date()
  const todayStr = formatDateInput(end)
  if (preset === 'all') return { from: '', to: '' }
  if (preset === '24h') {
    // Full timestamp instead of a calendar date so the backend applies a
    // rolling 24-hour window; empty "to" keeps the range open through now.
    const start = new Date(end.getTime() - 24 * 60 * 60 * 1000)
    return { from: start.toISOString(), to: '' }
  }
  if (preset === 'mtd') {
    const start = new Date(end.getFullYear(), end.getMonth(), 1)
    return { from: formatDateInput(start), to: todayStr }
  }
  const days = { '7d': 7, '30d': 30, '90d': 90 }[preset] || 30
  const start = new Date(end)
  start.setDate(end.getDate() - days)
  return { from: formatDateInput(start), to: todayStr }
}

const indexerMetricOptions = {
  response: { label: 'Response (ms)', key: 'avgResponseMs', suffix: ' ms' },
  searches: { label: 'Searches', key: 'searchesCount', suffix: '' },
  downloads: { label: 'Downloads', key: 'downloadsCount', suffix: '' },
  uniqueHits: { label: 'Unique hits', key: 'uniqueHitsCount', suffix: '' },
  availAvailable: { label: 'Available', key: 'availAvailableCount', suffix: '' },
  availDiscarded: { label: 'Unavailable', key: 'availDiscardedCount', suffix: '' },
}

export const StatisticsPage = memo(function StatisticsPage() {
  const [historyStats, setHistoryStats] = useState({ providers: [], indexers: [] })
  const [perfStats, setPerfStats] = useState(null)
  const [preset, setPreset] = useState('24h')
  const [customRange, setCustomRange] = useState(defaultDateRange())
  const [activeRange, setActiveRange] = useState(() => rangeFromPreset('24h'))
  const [popoverOpen, setPopoverOpen] = useState(false)
  const [indexerMetric, setIndexerMetric] = useState('response')
  const [loading, setLoading] = useState(false)
  const [loadError, setLoadError] = useState('')
  const [deleteTarget, setDeleteTarget] = useState(null)
  const [deleting, setDeleting] = useState(false)
  const inFlightRef = useRef(false)
  const lastSignatureRef = useRef(buildHistorySignature({ providers: [], indexers: [] }))

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
      const signature = `${from || ''}:${to || ''}:${buildHistorySignature(normalized)}`
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
        setHistoryStats({ providers: [], indexers: [] })
        lastSignatureRef.current = buildHistorySignature({ providers: [], indexers: [] })
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
    if (preset === 'custom') return
    const nextRange = rangeFromPreset(preset)
    setActiveRange(nextRange)
    void loadStats(nextRange.from, nextRange.to)
  }, [loadStats, preset])

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

  const customRangeValidation = useMemo(() => {
    if (!customRange.from || !customRange.to) return 'Select both From and To dates.'
    const todayStr = formatDateInput(new Date())
    if (customRange.from > customRange.to) return 'From date must be on or before To date.'
    if (customRange.from > todayStr || customRange.to > todayStr) return 'Future dates are not allowed.'
    return ''
  }, [customRange.from, customRange.to])

  const indexerRows = useMemo(() => {
    const metricKey = indexerMetricOptions[indexerMetric]?.key || 'avgResponseMs'
    const isResponseMetric = metricKey === 'avgResponseMs'
    const rows = (historyStats?.indexers || [])
      .map((indexer) => {
        const name = String(pick(indexer, 'indexer_name', 'IndexerName', '') || '').trim()
        if (!name) return null
        const avgResponseMs = toNumber(pick(indexer, 'avg_response_ms', 'AvgResponseMS'))
        const availAvailableCount = toNumber(pick(indexer, 'avail_available_count', 'AvailAvailableCount'))
        const availDiscardedCount = toNumber(pick(indexer, 'avail_discarded_count', 'AvailDiscardedCount'))
        const availTotal = availAvailableCount + availDiscardedCount
        return {
          name,
          avgResponseMs,
          searchesCount: toNumber(pick(indexer, 'searches_count', 'SearchesCount')),
          downloadsCount: toNumber(pick(indexer, 'downloads_used', 'DownloadsUsed')),
          uniqueHitsCount: toNumber(pick(indexer, 'unique_hits_count', 'UniqueHitsCount')),
          availAvailableCount,
          availDiscardedCount,
          availTotal,
          availabilityPercent: availTotal > 0 ? (availAvailableCount / availTotal) * 100 : 0,
        }
      })
      .filter(Boolean)
    return rows.sort((a, b) => {
      const aMetric = toNumber(a[metricKey])
      const bMetric = toNumber(b[metricKey])
      if (isResponseMetric) {
        if (aMetric <= 0 && bMetric <= 0) return a.name.localeCompare(b.name)
        if (aMetric <= 0) return 1
        if (bMetric <= 0) return -1
        return aMetric - bMetric
      }
      if (aMetric === bMetric) return a.name.localeCompare(b.name)
      return bMetric - aMetric
    })
  }, [historyStats, indexerMetric])

  const providerRows = useMemo(() => {
    const rawRows = (historyStats?.providers || [])
      .map((provider) => {
        const name = String(pick(provider, 'provider_name', 'ProviderName', '') || '').trim()
        if (!name) return null
        const articleAvailableCount = toNumber(pick(provider, 'article_available_count', 'ArticleAvailableCount'))
        const articleMissingCount = toNumber(pick(provider, 'article_missing_count', 'ArticleMissingCount'))
        const articleTotal = articleAvailableCount + articleMissingCount
        return {
          name,
          host: String(pick(provider, 'host', 'Host', '')),
          downloadedMb: toNumber(pick(provider, 'downloaded_mb', 'DownloadedMB')),
          articleAvailableCount,
          articleMissingCount,
          articleTotal,
          missingPercent: articleTotal > 0 ? (articleMissingCount / articleTotal) * 100 : 0,
        }
      })
      .filter(Boolean)

    const totalDownloaded = rawRows.reduce((sum, r) => sum + r.downloadedMb, 0)

    return rawRows
      .map((row) => ({
        ...row,
        usagePercent: totalDownloaded > 0 ? (row.downloadedMb / totalDownloaded) * 100 : 0,
      }))
      .sort((a, b) => b.downloadedMb - a.downloadedMb)
  }, [historyStats])

  const indexerChartData = useMemo(() => indexerRows.map((row) => ({
    name: row.name,
    value: toNumber(row[indexerMetricOptions[indexerMetric]?.key]),
  })), [indexerMetric, indexerRows])

  const providerChartData = useMemo(() => providerRows.map((row) => ({
    name: row.name,
    downloaded: row.downloadedMb,
  })), [providerRows])

  const indexerChartConfig = useMemo(() => ({
    value: {
      label: indexerMetricOptions[indexerMetric]?.label || 'Metric',
      color: "hsl(var(--primary))",
    },
  }), [indexerMetric])

  const rangeLabel = useMemo(() => {
    if (preset === '24h') return 'Last 24 Hours'
    if (preset === 'all') return 'All Time'
    if (!activeRange.from && !activeRange.to) return 'All Time'
    const fromStr = formatDisplayDate(activeRange.from) || 'Beginning'
    const toStr = formatDisplayDate(activeRange.to) || 'Today'
    return `${fromStr} – ${toStr}`
  }, [activeRange, preset])

  const handleSelectPreset = (pId) => {
    setPreset(pId)
    if (pId !== 'custom') {
      const nextRange = rangeFromPreset(pId)
      setActiveRange(nextRange)
      void loadStats(nextRange.from, nextRange.to)
      setPopoverOpen(false)
    }
  }

  const handleApplyCustomRange = () => {
    if (customRangeValidation) return
    setPreset('custom')
    setActiveRange(customRange)
    void loadStats(customRange.from, customRange.to)
    setPopoverOpen(false)
  }

  const indexerChartHeight = Math.max(220, indexerChartData.length * 42)
  const providerChartHeight = Math.max(220, providerChartData.length * 42)

  return (
    <div className="flex flex-col gap-4 py-4 md:gap-6 md:py-6 px-4 lg:px-6">
      {/* Real-time Performance Metrics */}
      <div className="grid gap-4 md:grid-cols-2">
        <Card className="overflow-hidden">
          <CardHeader className="pb-3">
            <div className="flex items-center gap-2">
              <Zap className="h-5 w-5 text-amber-500" />
              <CardTitle>API Response Time (/stream)</CardTitle>
            </div>
            <CardDescription>Stremio stream query latency percentiles.</CardDescription>
          </CardHeader>
          <CardContent>
            {perfStats?.stream_summary?.total?.sample_count > 0 ? (
              <div className="grid grid-cols-3 gap-2 text-center">
                <div className="p-2 bg-muted/40 rounded-md">
                  <div className="text-xs text-muted-foreground font-medium">p50</div>
                  <div className="text-lg font-bold text-primary">{perfStats.stream_summary.total.p50_ms.toFixed(0)} ms</div>
                </div>
                <div className="p-2 bg-muted/40 rounded-md">
                  <div className="text-xs text-muted-foreground font-medium">p95</div>
                  <div className="text-lg font-bold text-amber-500">{perfStats.stream_summary.total.p95_ms.toFixed(0)} ms</div>
                </div>
                <div className="p-2 bg-muted/40 rounded-md">
                  <div className="text-xs text-muted-foreground font-medium">p99</div>
                  <div className="text-lg font-bold text-rose-500">{perfStats.stream_summary.total.p99_ms.toFixed(0)} ms</div>
                </div>
                <div className="p-2 bg-muted/20 rounded-md text-xs">Min: {perfStats.stream_summary.total.min_ms.toFixed(0)} ms</div>
                <div className="p-2 bg-muted/20 rounded-md text-xs">Avg: {perfStats.stream_summary.total.avg_ms.toFixed(0)} ms</div>
                <div className="p-2 bg-muted/20 rounded-md text-xs">Max: {perfStats.stream_summary.total.max_ms.toFixed(0)} ms</div>
              </div>
            ) : (
              <div className="py-6 text-center text-sm text-muted-foreground">No /stream requests recorded yet.</div>
            )}
          </CardContent>
        </Card>

        <Card className="overflow-hidden">
          <CardHeader className="pb-3">
            <div className="flex items-center gap-2">
              <PlayCircle className="h-5 w-5 text-emerald-500" />
              <CardTitle>Playback Start Time (TTFF)</CardTitle>
            </div>
            <CardDescription>Time to First Frame from play request to video stream output.</CardDescription>
          </CardHeader>
          <CardContent>
            {perfStats?.ttff_summary?.total?.sample_count > 0 ? (
              <div className="grid grid-cols-3 gap-2 text-center">
                <div className="p-2 bg-muted/40 rounded-md">
                  <div className="text-xs text-muted-foreground font-medium">p50</div>
                  <div className="text-lg font-bold text-emerald-500">{perfStats.ttff_summary.total.p50_ms.toFixed(0)} ms</div>
                </div>
                <div className="p-2 bg-muted/40 rounded-md">
                  <div className="text-xs text-muted-foreground font-medium">p95</div>
                  <div className="text-lg font-bold text-amber-500">{perfStats.ttff_summary.total.p95_ms.toFixed(0)} ms</div>
                </div>
                <div className="p-2 bg-muted/40 rounded-md">
                  <div className="text-xs text-muted-foreground font-medium">p99</div>
                  <div className="text-lg font-bold text-rose-500">{perfStats.ttff_summary.total.p99_ms.toFixed(0)} ms</div>
                </div>
                <div className="p-2 bg-muted/20 rounded-md text-xs">Min: {perfStats.ttff_summary.total.min_ms.toFixed(0)} ms</div>
                <div className="p-2 bg-muted/20 rounded-md text-xs">Avg: {perfStats.ttff_summary.total.avg_ms.toFixed(0)} ms</div>
                <div className="p-2 bg-muted/20 rounded-md text-xs">Max: {perfStats.ttff_summary.total.max_ms.toFixed(0)} ms</div>
              </div>
            ) : (
              <div className="py-6 text-center text-sm text-muted-foreground">No playback sessions recorded yet.</div>
            )}
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between pb-3">
          <div>
            <CardTitle className="text-base font-semibold flex items-center gap-2">
              <Calendar className="h-4 w-4 text-primary" />
              Date Range Filter
            </CardTitle>
            <CardDescription>Filter statistics snapshots by quick presets or custom date range.</CardDescription>
          </div>
          <div className="flex items-center gap-2">
            <ToggleGroup
              type="single"
              value={preset}
              onValueChange={(val) => {
                if (val) handleSelectPreset(val)
              }}
              variant="outline"
              size="sm"
              className="hidden md:flex flex-wrap gap-1"
            >
              <ToggleGroupItem value="24h" className="h-8 px-2.5 text-xs">24H</ToggleGroupItem>
              <ToggleGroupItem value="7d" className="h-8 px-2.5 text-xs">7D</ToggleGroupItem>
              <ToggleGroupItem value="30d" className="h-8 px-2.5 text-xs">30D</ToggleGroupItem>
              <ToggleGroupItem value="90d" className="h-8 px-2.5 text-xs">90D</ToggleGroupItem>
              <ToggleGroupItem value="mtd" className="h-8 px-2.5 text-xs">MTD</ToggleGroupItem>
              <ToggleGroupItem value="all" className="h-8 px-2.5 text-xs">All</ToggleGroupItem>
            </ToggleGroup>

            <Popover open={popoverOpen} onOpenChange={setPopoverOpen}>
              <PopoverTrigger asChild>
                <Button
                  variant="outline"
                  size="sm"
                  className="h-8 gap-2 px-3 font-normal border-primary/20 hover:border-primary/40 focus:ring-1 focus:ring-primary transition-all"
                >
                  <CalendarDays className="h-4 w-4 text-primary shrink-0" />
                  <span className="font-medium text-xs sm:text-sm">
                    {preset === '24h' && 'Last 24 Hours'}
                    {preset === '7d' && 'Last 7 Days'}
                    {preset === '30d' && 'Last 30 Days'}
                    {preset === '90d' && 'Last 90 Days'}
                    {preset === 'mtd' && 'Month to Date'}
                    {preset === 'all' && 'All Time'}
                    {preset === 'custom' && 'Custom Range'}
                  </span>
                  <ChevronDown className="h-3.5 w-3.5 opacity-50 shrink-0 ml-1" />
                </Button>
              </PopoverTrigger>
              <PopoverContent className="w-80 p-3" align="end">
                <div className="space-y-3">
                  <div className="text-xs font-semibold text-muted-foreground uppercase tracking-wider px-1">Quick Presets</div>
                  <div className="grid grid-cols-1 gap-1">
                    {PRESETS.map((p) => {
                      const isSelected = preset === p.id
                      return (
                        <button
                          key={p.id}
                          type="button"
                          onClick={() => handleSelectPreset(p.id)}
                          className={cn(
                            "flex items-center justify-between w-full px-2.5 py-1.5 rounded-md text-xs transition-colors text-left",
                            isSelected
                              ? "bg-primary/10 text-primary font-medium"
                              : "hover:bg-muted text-foreground"
                          )}
                        >
                          <div>
                            <div className="font-medium">{p.label}</div>
                            <div className="text-[10px] text-muted-foreground">{p.description}</div>
                          </div>
                          {isSelected && <Check className="h-3.5 w-3.5 text-primary shrink-0 ml-2" />}
                        </button>
                      )
                    })}
                  </div>

                  <div className="border-t pt-3 space-y-2">
                    <div className="text-xs font-semibold text-muted-foreground uppercase tracking-wider px-1">Custom Range</div>
                    <div className="grid grid-cols-2 gap-2">
                      <div>
                        <label className="text-[11px] font-medium text-muted-foreground mb-1 block">From</label>
                        <Input
                          type="date"
                          max={formatDateInput(new Date())}
                          value={customRange.from}
                          onChange={(e) => {
                            setCustomRange((prev) => ({ ...prev, from: e.target.value }))
                            setPreset('custom')
                          }}
                          className="h-8 text-xs px-2"
                        />
                      </div>
                      <div>
                        <div className="flex items-center justify-between mb-1">
                          <label className="text-[11px] font-medium text-muted-foreground">To</label>
                          <button
                            type="button"
                            onClick={() => {
                              setCustomRange((prev) => ({ ...prev, to: formatDateInput(new Date()) }))
                              setPreset('custom')
                            }}
                            className="text-[10px] text-primary hover:underline font-medium"
                          >
                            Today
                          </button>
                        </div>
                        <Input
                          type="date"
                          max={formatDateInput(new Date())}
                          value={customRange.to}
                          onChange={(e) => {
                            setCustomRange((prev) => ({ ...prev, to: e.target.value }))
                            setPreset('custom')
                          }}
                          className="h-8 text-xs px-2"
                        />
                      </div>
                    </div>

                    {preset === 'custom' && customRangeValidation && (
                      <div className="text-[11px] text-destructive px-1">{customRangeValidation}</div>
                    )}

                    <Button
                      type="button"
                      size="sm"
                      className="w-full h-8 text-xs mt-1"
                      disabled={loading || Boolean(customRangeValidation)}
                      onClick={handleApplyCustomRange}
                    >
                      Apply Custom Range
                    </Button>
                  </div>
                </div>
              </PopoverContent>
            </Popover>
          </div>
        </CardHeader>
        <CardContent className="pt-2 pb-3 flex items-center justify-between text-xs text-muted-foreground border-t bg-muted/20 px-6 py-2.5 rounded-b-lg">
          <div className="flex items-center gap-2">
            <span className="font-medium text-foreground">Showing:</span>
            <span className="font-semibold text-primary">{rangeLabel}</span>
          </div>
          <div>{loading ? 'Refreshing stats...' : 'Live Data'}</div>
        </CardContent>
      </Card>

      {loadError && (
        <Card>
          <CardContent className="pt-6 text-sm text-destructive">{loadError}</CardContent>
        </Card>
      )}

      <div className="grid grid-cols-1 gap-6 xl:grid-cols-2">
        <Card className="overflow-hidden">
          <CardHeader>
            <div className="flex items-center gap-2">
              <Gauge className="h-5 w-5 text-primary" />
              <CardTitle>Indexer Statistics</CardTitle>
            </div>
            <CardDescription>Response time, searches, downloads, and availability rate by indexer.</CardDescription>
            <div className="pt-2">
              <div className="sm:hidden">
                <select
                  value={indexerMetric}
                  onChange={(e) => e.target.value && setIndexerMetric(e.target.value)}
                  className="h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm font-medium shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                >
                  {Object.entries(indexerMetricOptions).map(([key, opt]) => (
                    <option key={key} value={key}>
                      {opt.label}
                    </option>
                  ))}
                </select>
              </div>
              <div className="hidden sm:block overflow-x-auto max-w-full pb-1">
                <ToggleGroup
                  type="single"
                  value={indexerMetric}
                  onValueChange={(value) => {
                    if (!value) return
                    setIndexerMetric(value)
                  }}
                  variant="outline"
                  size="sm"
                  className="justify-start flex-wrap gap-1"
                >
                  <ToggleGroupItem value="response">Response</ToggleGroupItem>
                  <ToggleGroupItem value="searches">Searches</ToggleGroupItem>
                  <ToggleGroupItem value="downloads">Downloads</ToggleGroupItem>
                  <ToggleGroupItem value="uniqueHits">Unique hits</ToggleGroupItem>
                  <ToggleGroupItem value="availAvailable">Available</ToggleGroupItem>
                  <ToggleGroupItem value="availDiscarded">Unavailable</ToggleGroupItem>
                </ToggleGroup>
              </div>
            </div>
          </CardHeader>
          <CardContent className="space-y-4">
            <ChartContainer config={indexerChartConfig} className="w-full" style={{ height: `${indexerChartHeight}px` }}>
              <BarChart data={indexerChartData} layout="vertical" margin={{ top: 8, right: 12, left: 12, bottom: 8 }}>
                <CartesianGrid horizontal={false} />
                <XAxis type="number" tick={{ fontSize: 11 }} />
                <YAxis type="category" dataKey="name" width={160} tick={{ fontSize: 11 }} />
                <ChartTooltip content={<ChartTooltipContent />} />
                <Bar dataKey="value" fill="var(--color-value)" radius={4} name="value" />
              </BarChart>
            </ChartContainer>

            <div className="overflow-x-auto rounded-md border border-border/60">
              <table className="w-full text-sm">
                <thead className="bg-muted/40 text-muted-foreground">
                  <tr>
                    <th className="px-3 py-2 text-left font-medium">Indexer</th>
                    <th className="px-3 py-2 text-right font-medium">Avg response</th>
                    <th className="px-3 py-2 text-right font-medium">Searches</th>
                    <th className="px-3 py-2 text-right font-medium">Downloads</th>
                    <th className="px-3 py-2 text-right font-medium">Unique hits</th>
                    <th className="px-3 py-2 text-right font-medium">Availability</th>
                    <th className="px-3 py-2 text-right font-medium">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {indexerRows.map((row) => (
                    <tr key={row.name} className="border-t border-border/50">
                      <td className="px-3 py-2"><span className="truncate">{row.name}</span></td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.avgResponseMs > 0 ? `${row.avgResponseMs.toFixed(0)} ms` : 'N/A'}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.searchesCount}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.downloadsCount}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.uniqueHitsCount}</td>
                      <td
                        className="px-3 py-2 text-right tabular-nums"
                        title={row.availTotal > 0 ? `${row.availAvailableCount} available / ${row.availDiscardedCount} unavailable` : undefined}
                      >
                        {row.availTotal > 0 ? `${row.availabilityPercent.toFixed(1)}%` : 'N/A'}
                      </td>
                      <td className="px-3 py-2 text-right">
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon"
                          className="h-7 w-7 text-muted-foreground hover:text-destructive"
                          title={`Delete statistics for ${row.name} in selected range`}
                          onClick={() => setDeleteTarget({ type: 'indexer', name: row.name })}
                        >
                          <Trash2 className="h-3.5 w-3.5" />
                        </Button>
                      </td>
                    </tr>
                  ))}
                  {indexerRows.length === 0 && (
                    <tr>
                      <td colSpan={7} className="px-3 py-6 text-center text-muted-foreground">No indexer statistics available.</td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </CardContent>
        </Card>

        <Card className="overflow-hidden">
          <CardHeader>
            <div className="flex items-center gap-2">
              <Database className="h-5 w-5 text-primary" />
              <CardTitle>Provider Statistics</CardTitle>
            </div>
            <CardDescription>Downloaded volume and connection usage by provider.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <ChartContainer config={providerChartConfig} className="w-full" style={{ height: `${providerChartHeight}px` }}>
              <BarChart data={providerChartData} layout="vertical" margin={{ top: 8, right: 12, left: 12, bottom: 8 }}>
                <CartesianGrid horizontal={false} />
                <XAxis type="number" tick={{ fontSize: 11 }} />
                <YAxis type="category" dataKey="name" width={160} tick={{ fontSize: 11 }} />
                <ChartTooltip content={<ChartTooltipContent />} />
                <Bar dataKey="downloaded" fill="var(--color-downloaded)" radius={4} name="downloaded" />
              </BarChart>
            </ChartContainer>

            <div className="overflow-x-auto rounded-md border border-border/60">
              <table className="w-full text-sm">
                <thead className="bg-muted/40 text-muted-foreground">
                  <tr>
                    <th className="px-3 py-2 text-left font-medium">Provider</th>
                    <th className="px-3 py-2 text-right font-medium">Downloaded</th>
                    <th className="px-3 py-2 text-right font-medium">Usage</th>
                    <th className="px-3 py-2 text-right font-medium">Articles missing</th>
                    <th className="px-3 py-2 text-right font-medium">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {providerRows.map((row) => (
                    <tr key={row.name} className="border-t border-border/50">
                      <td className="px-3 py-2">
                        <div className="flex items-center gap-2"><span className="truncate">{row.name}</span></div>
                        {row.host && <div className="text-xs text-muted-foreground truncate">{row.host}</div>}
                      </td>
                      <td className="px-3 py-2 text-right tabular-nums">{formatDownloadedMb(row.downloadedMb)}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.usagePercent.toFixed(1)}%</td>
                      <td
                        className="px-3 py-2 text-right tabular-nums"
                        title={row.articleTotal > 0 ? `${row.articleAvailableCount} available / ${row.articleMissingCount} missing` : undefined}
                      >
                        {row.articleTotal > 0 ? `${row.missingPercent.toFixed(1)}%` : 'N/A'}
                      </td>
                      <td className="px-3 py-2 text-right">
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon"
                          className="h-7 w-7 text-muted-foreground hover:text-destructive"
                          title={`Delete statistics for ${row.name} in selected range`}
                          onClick={() => setDeleteTarget({ type: 'provider', name: row.name })}
                        >
                          <Trash2 className="h-3.5 w-3.5" />
                        </Button>
                      </td>
                    </tr>
                  ))}
                  {providerRows.length === 0 && (
                    <tr>
                      <td colSpan={5} className="px-3 py-6 text-center text-muted-foreground">No provider statistics available.</td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </CardContent>
        </Card>
      </div>

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

