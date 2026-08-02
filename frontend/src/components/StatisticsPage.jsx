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
import { Database, Gauge, Zap, Clock, Activity, PlayCircle } from "lucide-react"
import { apiFetch } from "@/api"

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

function summarizeEntries(entries, nameSnakeKey, namePascalKey) {
  let stamp = 0
  for (let i = 0; i < entries.length; i += 1) {
    const item = entries[i] || {}
    const name = String(pick(item, nameSnakeKey, namePascalKey, ''))
    const collectedAt = String(pick(item, 'collected_at', 'CollectedAt', ''))
    const keyCount = Object.keys(item).length
    const base = `${name}|${collectedAt}|${keyCount}`
    for (let j = 0; j < base.length; j += 1) {
      stamp = ((stamp * 31) + base.charCodeAt(j)) | 0
    }
  }
  return `${entries.length}:${stamp}`
}

function buildHistorySignature(normalized) {
  const providers = Array.isArray(normalized?.providers) ? normalized.providers : []
  const indexers = Array.isArray(normalized?.indexers) ? normalized.indexers : []
  return `${summarizeEntries(providers, 'provider_name', 'ProviderName')}|${summarizeEntries(indexers, 'indexer_name', 'IndexerName')}`
}

function parseDateValue(raw) {
  if (!raw) return null
  const d = new Date(`${raw}T00:00:00`)
  if (Number.isNaN(d.getTime())) return null
  return d
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
  if (preset === 'all') return { from: '', to: '' }
  const days = {
    '7d': 7,
    '30d': 30,
    '90d': 90,
  }[preset] || 30
  const end = new Date()
  const start = new Date(end)
  start.setDate(end.getDate() - days)
  return {
    from: formatDateInput(start),
    to: formatDateInput(end),
  }
}

const indexerMetricOptions = {
  response: { label: 'Response (ms)', key: 'avgResponseMs', suffix: ' ms' },
  speed: { label: 'Speed (req/s)', key: 'speedRps', suffix: ' req/s' },
  searches: { label: 'Searches', key: 'searchesCount', suffix: '' },
  downloads: { label: 'Downloads', key: 'downloadsCount', suffix: '' },
  uniqueHits: { label: 'Unique hits', key: 'uniqueHitsCount', suffix: '' },
  availAvailable: { label: 'Available', key: 'availAvailableCount', suffix: '' },
  availDiscarded: { label: 'Unavailable', key: 'availDiscardedCount', suffix: '' },
}

export const StatisticsPage = memo(function StatisticsPage() {
  const [historyStats, setHistoryStats] = useState({ providers: [], indexers: [] })
  const [perfStats, setPerfStats] = useState(null)
  const [preset, setPreset] = useState('30d')
  const [customRange, setCustomRange] = useState(defaultDateRange())
  const [activeRange, setActiveRange] = useState(defaultDateRange())
  const [indexerMetric, setIndexerMetric] = useState('response')
  const [loading, setLoading] = useState(false)
  const [loadError, setLoadError] = useState('')
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
      const signature = buildHistorySignature(normalized)
      if (signature !== lastSignatureRef.current) {
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
    if (preset !== 'custom') return ''
    const fromDate = parseDateValue(customRange.from)
    const toDate = parseDateValue(customRange.to)
    if (!fromDate || !toDate) return 'Select both dates.'
    const today = new Date()
    today.setHours(0, 0, 0, 0)
    if (toDate < fromDate) return 'To date must be on or after From date.'
    if (fromDate > today || toDate > today) return 'Future dates are not allowed.'
    return ''
  }, [customRange.from, customRange.to, preset])

  const indexerRows = useMemo(() => {
    const metricKey = indexerMetricOptions[indexerMetric]?.key || 'avgResponseMs'
    const isResponseMetric = metricKey === 'avgResponseMs'
    const rows = (historyStats?.indexers || [])
      .map((indexer) => {
        const name = String(pick(indexer, 'indexer_name', 'IndexerName', '') || '').trim()
        if (!name) return null
        const avgResponseMs = toNumber(pick(indexer, 'avg_response_ms', 'AvgResponseMS'))
        return {
          name,
          avgResponseMs,
          speedRps: avgResponseMs > 0 ? 1000 / avgResponseMs : 0,
          searchesCount: toNumber(pick(indexer, 'searches_count', 'SearchesCount')),
          downloadsCount: toNumber(pick(indexer, 'downloads_used', 'DownloadsUsed')),
          uniqueHitsCount: toNumber(pick(indexer, 'unique_hits_count', 'UniqueHitsCount')),
          availAvailableCount: toNumber(pick(indexer, 'avail_available_count', 'AvailAvailableCount')),
          availDiscardedCount: toNumber(pick(indexer, 'avail_discarded_count', 'AvailDiscardedCount')),
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
    return (historyStats?.providers || [])
      .map((provider) => {
        const name = String(pick(provider, 'provider_name', 'ProviderName', '') || '').trim()
        if (!name) return null
        return {
          name,
          host: String(pick(provider, 'host', 'Host', '')),
          downloadedMb: toNumber(pick(provider, 'downloaded_mb', 'DownloadedMB')),
          activeConns: toNumber(pick(provider, 'active_conns', 'ActiveConns')),
          maxConns: toNumber(pick(provider, 'max_conns', 'MaxConns')),
          usagePercent: toNumber(pick(provider, 'usage_percent', 'UsagePercent')),
          articleAvailableCount: toNumber(pick(provider, 'article_available_count', 'ArticleAvailableCount')),
          articleMissingCount: toNumber(pick(provider, 'article_missing_count', 'ArticleMissingCount')),
        }
      })
      .filter(Boolean)
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
    const from = activeRange?.from || 'Beginning'
    const to = activeRange?.to || 'Now'
    return `${from} - ${to}`
  }, [activeRange])

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
        <CardHeader>
          <CardTitle>Date Range</CardTitle>
          <CardDescription>Use quick presets or choose a custom range for persisted snapshots.</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex flex-wrap items-center gap-2">
            <ToggleGroup
              type="single"
              value={preset}
              onValueChange={(value) => {
                if (!value) return
                setPreset(value)
              }}
              variant="outline"
              size="sm"
              className="justify-start flex-wrap gap-1"
            >
              <ToggleGroupItem value="7d">7D</ToggleGroupItem>
              <ToggleGroupItem value="30d">30D</ToggleGroupItem>
              <ToggleGroupItem value="90d">90D</ToggleGroupItem>
              <ToggleGroupItem value="all">All</ToggleGroupItem>
              <ToggleGroupItem value="custom">Custom</ToggleGroupItem>
            </ToggleGroup>
            <div className="text-xs text-muted-foreground">{loading ? 'Loading...' : `Showing: ${rangeLabel}`}</div>
          </div>

          {preset === 'custom' && (
            <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
              <div className="w-full sm:w-auto">
                <div className="mb-1 text-xs text-muted-foreground">From</div>
                <Input
                  type="date"
                  value={customRange.from}
                  onChange={(event) => setCustomRange((prev) => ({ ...prev, from: event.target.value }))}
                  className="h-9 sm:w-44"
                />
              </div>
              <div className="w-full sm:w-auto">
                <div className="mb-1 text-xs text-muted-foreground">To</div>
                <Input
                  type="date"
                  value={customRange.to}
                  onChange={(event) => setCustomRange((prev) => ({ ...prev, to: event.target.value }))}
                  className="h-9 sm:w-44"
                />
              </div>
              <Button
                type="button"
                variant="outline"
                className="h-9 sm:min-w-28"
                disabled={loading || Boolean(customRangeValidation)}
                onClick={() => {
                  if (customRangeValidation) return
                  setActiveRange(customRange)
                  void loadStats(customRange.from, customRange.to)
                }}
              >
                Apply Custom
              </Button>
            </div>
          )}
          {preset === 'custom' && customRangeValidation && (
            <div className="text-xs text-destructive">{customRangeValidation}</div>
          )}
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
            <CardDescription>Average response time, speed estimate, searches, and downloads by indexer.</CardDescription>
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
                  <ToggleGroupItem value="speed">Speed</ToggleGroupItem>
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
                    <th className="px-3 py-2 text-right font-medium">Speed</th>
                    <th className="px-3 py-2 text-right font-medium">Searches</th>
                    <th className="px-3 py-2 text-right font-medium">Downloads</th>
                    <th className="px-3 py-2 text-right font-medium">Unique hits</th>
                    <th className="px-3 py-2 text-right font-medium">Available</th>
                    <th className="px-3 py-2 text-right font-medium">Unavailable</th>
                  </tr>
                </thead>
                <tbody>
                  {indexerRows.map((row) => (
                    <tr key={row.name} className="border-t border-border/50">
                      <td className="px-3 py-2"><span className="truncate">{row.name}</span></td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.avgResponseMs > 0 ? `${row.avgResponseMs.toFixed(0)} ms` : 'N/A'}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.speedRps > 0 ? `${row.speedRps.toFixed(2)} req/s` : 'N/A'}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.searchesCount}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.downloadsCount}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.uniqueHitsCount}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.availAvailableCount}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.availDiscardedCount}</td>
                    </tr>
                  ))}
                  {indexerRows.length === 0 && (
                    <tr>
                      <td colSpan={8} className="px-3 py-6 text-center text-muted-foreground">No indexer statistics available.</td>
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
                    <th className="px-3 py-2 text-right font-medium">Connections</th>
                    <th className="px-3 py-2 text-right font-medium">Usage</th>
                    <th className="px-3 py-2 text-right font-medium">Article available</th>
                    <th className="px-3 py-2 text-right font-medium">Article missing</th>
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
                      <td className="px-3 py-2 text-right tabular-nums">{row.activeConns}/{row.maxConns || 0}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.usagePercent.toFixed(1)}%</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.articleAvailableCount}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.articleMissingCount}</td>
                    </tr>
                  ))}
                  {providerRows.length === 0 && (
                    <tr>
                      <td colSpan={6} className="px-3 py-6 text-center text-muted-foreground">No provider statistics available.</td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </CardContent>
        </Card>
      </div>

    </div>
  )
})

