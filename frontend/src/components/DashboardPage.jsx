import { useMemo, useState } from 'react'
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from "@/components/ui/chart"
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { ComposedChart, Area, Line, XAxis, YAxis } from "recharts"
import { Activity, ChevronDown, Globe, ListFilter, X, MonitorPlay, Loader2, Settings2 } from "lucide-react"
import { ComponentHealthNotice } from "@/components/ComponentHealth"
import { isAvailNZBEnabled } from "@/lib/availnzb"
import { healthFor, healthReasonLabel, indexHealth, isBlocked } from "@/lib/health"
import { cn, formatBytes, streamSeriesKey } from "@/lib/utils"
import { DEFAULT_STREAM_NAME } from "@/hooks/useAdminRuntime"

const baseChartConfig = {
  speed: {
    label: "Total speed",
    color: "hsl(var(--primary))",
  },
  conns: {
    label: "Connections",
    color: "hsl(var(--primary))",
  },
}

// Per-stream lines cycle the chart palette.
const streamColors = [
  "hsl(var(--chart-1))",
  "hsl(var(--chart-2))",
  "hsl(var(--chart-3))",
  "hsl(var(--chart-4))",
  "hsl(var(--chart-5))",
]

function streamColor(index) {
  return streamColors[index % streamColors.length]
}

function formatDownloadedMb(mb) {
  const n = Number(mb) || 0
  if (n >= 1000) return { value: (n / 1000).toFixed(2), unit: 'GB' }
  return { value: n.toFixed(1), unit: 'MB' }
}

export function DashboardPage({ stats, chartData, sendCommand, config, onNavigate, availNZBStatus, availNZBStatusLoading, availNZBStatusError, componentHealth, onRefreshComponentHealth }) {
  const [activeSessionToClose, setActiveSessionToClose] = useState(null)
  // Hidden rather than selected names, so a stream that starts playing mid-session
  // shows up without the user having to re-open the filter.
  const [hiddenStreamNames, setHiddenStreamNames] = useState(() => new Set())
  const availNZBEnabled = isAvailNZBEnabled(config?.availnzb_mode)

  const activeSessions = useMemo(() => stats?.active_sessions || [], [stats])

  const healthByName = useMemo(() => indexHealth(componentHealth), [componentHealth])

  // One entry per configured stream, not per playing release: sessions arrive
  // oldest-first, so a stream holds its slot (and its colour) while it plays.
  const streams = useMemo(() => {
    const byName = new Map()
    activeSessions.forEach((sess) => {
      const name = sess.stream_name || DEFAULT_STREAM_NAME
      if (!byName.has(name)) {
        byName.set(name, { name, seriesKey: streamSeriesKey(name), sessions: [] })
      }
      byName.get(name).sessions.push(sess)
    })
    return Array.from(byName.values()).map((stream, index) => ({
      ...stream,
      color: streamColor(index),
      shown: !hiddenStreamNames.has(stream.name),
    }))
  }, [activeSessions, hiddenStreamNames])

  const shownStreams = useMemo(() => streams.filter((s) => s.shown), [streams])
  const allStreamsShown = shownStreams.length === streams.length

  const chartConfig = useMemo(() => {
    const cfg = { ...baseChartConfig }
    shownStreams.forEach((stream) => {
      cfg[stream.seriesKey] = { label: stream.name, color: stream.color }
    })
    return cfg
  }, [shownStreams])

  // The newest chart point doubles as the live per-stream speed readout.
  const latestPoint = chartData?.[chartData.length - 1]

  const toggleStream = (name) => {
    setHiddenStreamNames((prev) => {
      const next = new Set(prev)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  }

  const indexerUrls = useMemo(() => {
    const urls = new Map()
    ;(config?.indexers || []).forEach((idx) => {
      const name = (idx?.name || '').trim()
      if (!name) return
      urls.set(name, idx?.url || '')
    })
    return urls
  }, [config])

  const displayedProviders = useMemo(() => {
    const statMap = new Map((stats?.providers || []).map((provider) => [String(provider.name || '').trim(), provider]))
    const rows = []

    ;(config?.providers || []).forEach((provider) => {
      const name = String(provider.name || '').trim()
      const stat = statMap.get(name)
      rows.push({
        name: stat?.name || name || provider.host || 'Provider',
        host: stat?.host || provider.host || '',
        max_conns: stat?.max_conns ?? Number(provider.connections || 0),
        active_conns: stat?.active_conns ?? 0,
        current_speed_mbps: stat?.current_speed_mbps ?? 0,
        downloaded_mb: stat?.downloaded_mb ?? 0,
        enabled: provider.enabled !== false,
      })
      statMap.delete(name)
    })

    statMap.forEach((provider) => {
      rows.push({
        ...provider,
        enabled: true,
      })
    })

    return rows
  }, [config, stats])

  const displayedIndexers = useMemo(() => {
    const statMap = new Map((stats?.indexers || []).map((indexer) => [String(indexer.name || '').trim(), indexer]))
    const rows = []

    ;(config?.indexers || []).forEach((indexer) => {
      const name = String(indexer.name || '').trim()
      const stat = statMap.get(name)
      rows.push({
        name: stat?.name || name || 'Indexer',
        api_hits_used: stat?.api_hits_used ?? 0,
        api_hits_limit: stat?.api_hits_limit ?? Number(indexer.api_hits_day || 0),
        api_hits_remaining: stat?.api_hits_remaining ?? Number(indexer.api_hits_day || 0),
        downloads_used: stat?.downloads_used ?? 0,
        downloads_limit: stat?.downloads_limit ?? Number(indexer.downloads_day || 0),
        downloads_remaining: stat?.downloads_remaining ?? Number(indexer.downloads_day || 0),
        enabled: indexer.enabled !== false,
      })
      statMap.delete(name)
    })

    statMap.forEach((indexer) => {
      rows.push({
        ...indexer,
        enabled: true,
      })
    })

    return rows
  }, [config, stats])

  const rawAvailNZBTrustScore = Number(availNZBStatus?.status?.trust_score)
  const maxAvailNZBTrustScore = 60
  const availNZBStatusMessage = availNZBStatusError || availNZBStatus?.status_error || ''
  const hasAvailNZBTrustError = availNZBEnabled && !availNZBStatusLoading && Boolean(availNZBStatusMessage)
  const availNZBTrustScore = Number.isFinite(rawAvailNZBTrustScore)
    ? (Math.max(0, Math.min(maxAvailNZBTrustScore, rawAvailNZBTrustScore)) / maxAvailNZBTrustScore) * 100
    : null
  const availNZBTrustSummary = hasAvailNZBTrustError ? 'Error' : `${Math.round(availNZBTrustScore ?? 0)}%`
  const availNZBTrustBarClass = hasAvailNZBTrustError
    ? 'bg-destructive/50'
    : availNZBTrustScore === null
    ? 'bg-muted-foreground/20'
    : availNZBTrustScore < 34
      ? 'bg-destructive'
      : availNZBTrustScore < 67
        ? 'bg-chart-4'
        : 'bg-primary'

  const confirmCloseActiveSession = () => {
    if (!activeSessionToClose) return
    sendCommand('close_session', { id: activeSessionToClose.id })
    setActiveSessionToClose(null)
  }

  return (
    <>
      <div className="flex flex-col gap-4 py-4 md:gap-6 md:py-6 px-4 lg:px-6">
        {/* KPI cards */}
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-5">
        <Card className="overflow-hidden">
          <CardHeader>
            <div className="flex items-center justify-between gap-2">
              <CardDescription>AvailNZB Trust</CardDescription>
              <TooltipProvider delayDuration={100}>
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Badge variant="outline" className="h-5 min-w-5 rounded-full px-1.5">
                      <span className={cn("h-1.5 w-1.5 rounded-full", availNZBEnabled ? "bg-green-600" : "bg-muted-foreground")} />
                    </Badge>
                  </TooltipTrigger>
                  <TooltipContent>{availNZBEnabled ? 'Active' : 'Not enabled'}</TooltipContent>
                </Tooltip>
              </TooltipProvider>
            </div>
            {availNZBEnabled ? (
              <>
                <CardTitle className="flex items-center gap-2 tabular-nums">
                  {availNZBStatusLoading && <Loader2 className="h-4 w-4 animate-spin text-primary" />}
                  <span className={hasAvailNZBTrustError ? 'text-destructive' : 'text-primary'}>{availNZBTrustSummary}</span>
                </CardTitle>
                {hasAvailNZBTrustError ? (
                  <p className="mt-2 line-clamp-2 text-xs text-destructive">{availNZBStatusMessage}</p>
                ) : (
                  <div className="mt-2 h-2 w-full overflow-hidden rounded-full bg-muted/70" aria-hidden="true">
                    <div
                      className={cn("h-full rounded-full transition-all duration-500", availNZBTrustBarClass)}
                      style={{ width: `${availNZBTrustScore ?? 0}%` }}
                    />
                  </div>
                )}
              </>
            ) : (
              // AvailNZB is opt-in, so "off" is the normal state, not a fault:
              // say so and point at the switch rather than showing a 0% score
              // that reads like the integration is failing.
              <>
                <CardTitle className="text-lg text-muted-foreground">Not enabled</CardTitle>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="mt-2 h-7 w-fit gap-1.5 px-2 text-xs"
                  onClick={() => onNavigate?.('settings-advanced')}
                >
                  <Settings2 className="h-3.5 w-3.5" />
                  Enable
                </Button>
              </>
            )}
          </CardHeader>
        </Card>
        <Card>
          <CardHeader>
            <CardDescription>Total Speed</CardDescription>
            <CardTitle className="flex items-baseline gap-1.5 tabular-nums">
              <span className="text-primary">{(stats.total_speed_mbps ?? 0).toFixed(1)}</span>
              <span className="text-sm font-normal text-muted-foreground">Mbps</span>
            </CardTitle>
          </CardHeader>
        </Card>
        <Card>
          <CardHeader>
            <CardDescription>Active Connections</CardDescription>
            <CardTitle className="tabular-nums text-primary">{activeSessions.length}</CardTitle>
            <p className="text-xs text-muted-foreground">streaming</p>
          </CardHeader>
        </Card>
        <Card>
          <CardHeader>
            <CardDescription>Pool Connections</CardDescription>
            <CardTitle className="flex items-baseline gap-1.5 tabular-nums">
              <span className="text-primary">{stats.active_connections}</span>
              <span className="text-sm font-normal text-muted-foreground">/ {stats.total_connections}</span>
            </CardTitle>
          </CardHeader>
        </Card>
        <Card>
          <CardHeader>
            <CardDescription>Downloaded Today</CardDescription>
            <CardTitle className="flex items-baseline gap-1.5 tabular-nums">
              {(() => {
                const { value, unit } = formatDownloadedMb(stats.total_downloaded_mb)
                return <><span className="text-primary">{value}</span><span className="text-sm font-normal text-muted-foreground">{unit}</span></>
              })()}
            </CardTitle>
          </CardHeader>
        </Card>
      </div>

        {/* Network chart + the streams feeding it */}
        <Card className="overflow-hidden">
        <CardHeader>
          <div className="flex flex-wrap items-start justify-between gap-2">
            <div className="min-w-0 space-y-1.5">
              <CardTitle>Network activity</CardTitle>
              <CardDescription>Total speed (Mbps), pool connections and per-stream speed over the last 20 seconds</CardDescription>
            </div>
            {streams.length > 0 && (
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="outline" size="sm" className="gap-2">
                    <ListFilter className="h-4 w-4" />
                    {allStreamsShown ? 'All streams' : `${shownStreams.length} of ${streams.length} streams`}
                    <ChevronDown className="h-3.5 w-3.5 opacity-60" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-72">
                  <DropdownMenuLabel>Stream activity</DropdownMenuLabel>
                  <DropdownMenuSeparator />
                  <DropdownMenuCheckboxItem
                    checked={allStreamsShown}
                    onCheckedChange={() => setHiddenStreamNames(new Set())}
                    onSelect={(event) => event.preventDefault()}
                  >
                    All streams
                  </DropdownMenuCheckboxItem>
                  <DropdownMenuSeparator />
                  {streams.map((stream) => (
                    <DropdownMenuCheckboxItem
                      key={stream.name}
                      checked={stream.shown}
                      onCheckedChange={() => toggleStream(stream.name)}
                      onSelect={(event) => event.preventDefault()}
                    >
                      <span className="flex min-w-0 items-center gap-2">
                        <span className="h-2 w-2 shrink-0 rounded-[2px]" style={{ backgroundColor: stream.color }} />
                        <span className="truncate" title={stream.name}>{stream.name}</span>
                        <span className="ml-auto shrink-0 text-xs text-muted-foreground tabular-nums">
                          {stream.sessions.length}
                        </span>
                      </span>
                    </DropdownMenuCheckboxItem>
                  ))}
                </DropdownMenuContent>
              </DropdownMenu>
            )}
          </div>
        </CardHeader>
        <CardContent className="p-0">
          <ChartContainer config={chartConfig} className="h-[200px] w-full">
            <ComposedChart data={chartData} margin={{ top: 8, right: 8, bottom: 8, left: 32 }}>
              <defs>
                <linearGradient id="chartSpeed" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor="hsl(var(--primary))" stopOpacity={0.4} />
                  <stop offset="100%" stopColor="hsl(var(--primary))" stopOpacity={0} />
                </linearGradient>
                <linearGradient id="chartConns" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor="hsl(var(--primary))" stopOpacity={0.25} />
                  <stop offset="100%" stopColor="hsl(var(--primary))" stopOpacity={0} />
                </linearGradient>
              </defs>
              <XAxis dataKey="time" tick={{ fontSize: 10 }} />
              <YAxis yAxisId="left" tick={{ fontSize: 10 }} width={28} />
              <YAxis yAxisId="right" orientation="right" tick={{ fontSize: 10 }} allowDecimals={false} width={28} />
              <ChartTooltip content={<ChartTooltipContent />} />
              <Area
                yAxisId="left"
                type="monotone"
                dataKey="speed"
                stroke="hsl(var(--primary))"
                strokeWidth={2}
                fill="url(#chartSpeed)"
                dot={false}
                isAnimationActive={false}
                name="speed"
              />
              <Area
                yAxisId="right"
                type="monotone"
                dataKey="conns"
                stroke="hsl(var(--primary))"
                strokeWidth={2}
                strokeOpacity={0.7}
                fill="url(#chartConns)"
                dot={false}
                isAnimationActive={false}
                name="conns"
              />
              {shownStreams.map((stream) => (
                <Line
                  key={stream.name}
                  yAxisId="left"
                  type="monotone"
                  dataKey={stream.seriesKey}
                  stroke={stream.color}
                  strokeWidth={1.75}
                  dot={false}
                  isAnimationActive={false}
                  name={stream.seriesKey}
                />
              ))}
            </ComposedChart>
          </ChartContainer>
        </CardContent>
        {streams.length > 0 && (
          <CardContent className="pt-2">
            <div className="mb-2 flex items-center gap-2">
              <Activity className="h-4 w-4 text-primary" />
              <p className="text-sm font-semibold tracking-tight">Active streams</p>
            </div>
            <div className="space-y-4">
              {streams.map((stream) => {
                const mbps = Number(latestPoint?.[stream.seriesKey]) || 0
                return (
                  <div key={stream.name} className={cn("space-y-2", !stream.shown && "opacity-60")}>
                    <div className="flex items-center justify-between gap-2">
                      <div className="flex min-w-0 items-center gap-2">
                        <span
                          className="h-2 w-2 shrink-0 rounded-[2px]"
                          style={{ backgroundColor: stream.color, opacity: stream.shown ? 1 : 0.4 }}
                          aria-hidden="true"
                        />
                        <span className="truncate text-sm font-medium" title={stream.name}>{stream.name}</span>
                        <Badge variant="outline" className="h-4 shrink-0 px-1.5 text-[10px]">{stream.sessions.length}</Badge>
                      </div>
                      <span className="shrink-0 text-xs text-muted-foreground tabular-nums">{mbps.toFixed(1)} Mbps</span>
                    </div>
                    {stream.sessions.map((sess) => (
                      <Card key={sess.id} className="group relative min-w-0 pr-10">
                        <CardContent className="p-3">
                          <div
                            className="min-w-0 pr-2 text-sm font-medium leading-snug whitespace-normal break-words [overflow-wrap:anywhere] md:truncate md:whitespace-nowrap"
                            title={sess.title}
                          >
                            {sess.title}
                          </div>
                          <div className="text-xs text-muted-foreground truncate min-w-0">
                            {sess.clients.join(', ')}
                            {sess.bytes_read > 0 && <span className="tabular-nums"> • {formatBytes(sess.bytes_read)} downloaded</span>}
                          </div>
                        </CardContent>
                        <Button
                          variant="ghost"
                          size="icon"
                          className="absolute right-2 top-1/2 -translate-y-1/2 h-7 w-7 text-destructive hover:text-destructive hover:bg-destructive/10"
                          onClick={() => setActiveSessionToClose(sess)}
                          title="End stream"
                          aria-label={`End stream ${sess.title}`}
                        >
                          <X className="h-4 w-4" />
                        </Button>
                      </Card>
                    ))}
                  </div>
                )
              })}
            </div>
          </CardContent>
        )}
        </Card>

        {/* Providers & Indexers */}
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        <Card className="overflow-hidden">
          <CardHeader>
            <div className="flex items-center gap-2">
              <Globe className="h-5 w-5 text-primary" />
              <CardTitle className="text-lg font-semibold tracking-tight">Usenet Providers</CardTitle>
            </div>
            <CardDescription>All configured providers and their daily usage & load.</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="grid gap-3 grid-cols-1 sm:grid-cols-2">
              {displayedProviders.map((p) => {
                const loadPct = (p.active_conns / (p.max_conns || 1)) * 100
                const isEnabled = p.enabled !== false
                const healthRecord = healthFor(healthByName, 'provider', (p.name || '').trim())
                const healthBlocked = isBlocked(healthRecord)
                return (
                  <Card
                    key={p.name}
                    className={cn("min-h-[170px]", !isEnabled && "opacity-60 grayscale")}
                  >
                    <CardHeader className="p-3 pb-1">
                      <div className="flex items-center gap-2">
                        <CardTitle className="text-base font-semibold truncate leading-tight" title={p.name}>{p.name}</CardTitle>
                        <TooltipProvider delayDuration={100}>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Badge variant="outline" className="ml-auto h-5 min-w-5 rounded-full px-1.5">
                                <span className={cn("h-1.5 w-1.5 rounded-full",
                                  !isEnabled || healthBlocked ? "bg-destructive"
                                    : healthRecord ? "bg-amber-500"
                                      : "bg-green-600")} />
                              </Badge>
                            </TooltipTrigger>
                            <TooltipContent>
                              {!isEnabled ? 'Inactive' : healthRecord ? healthReasonLabel(healthRecord.reason) : 'Active'}
                            </TooltipContent>
                          </Tooltip>
                        </TooltipProvider>
                      </div>
                      <p className="text-[10px] text-muted-foreground truncate" title={p.host}>{p.host}</p>
                    </CardHeader>
                    <CardContent className="p-3 pt-0">
                      <div className="flex items-center justify-between mt-2">
                        <div className="flex flex-col">
                          <span className="text-[10px] uppercase text-muted-foreground font-medium">Load</span>
                          <span className="text-lg font-bold tabular-nums text-primary">{loadPct.toFixed(0)}%</span>
                        </div>
                        <div className="flex flex-col text-right">
                          <span className="text-[10px] uppercase text-muted-foreground font-medium">Speed</span>
                          <span className="text-lg font-bold tabular-nums text-primary">{(p.current_speed_mbps ?? 0).toFixed(1)} <span className="text-[10px]">Mbps</span></span>
                        </div>
                      </div>
                      <div className="w-full bg-muted h-2 rounded-full mt-2 overflow-hidden">
                        <div className="bg-primary h-full transition-all duration-500 rounded-full" style={{ width: `${loadPct}%` }} />
                      </div>
                      <div className="mt-2 flex items-center justify-between gap-2 text-[11px] text-muted-foreground">
                        <span>Downloaded today: {(p.downloaded_mb ?? 0).toFixed(1)} MB</span>
                        <TooltipProvider delayDuration={100}>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Badge variant="outline" className="h-4 px-1.5 text-[10px]">{p.max_conns}</Badge>
                            </TooltipTrigger>
                            <TooltipContent>Connections</TooltipContent>
                          </Tooltip>
                        </TooltipProvider>
                      </div>
                      <ComponentHealthNotice record={healthRecord} onRefresh={onRefreshComponentHealth} />
                    </CardContent>
                  </Card>
                )
              })}
              {displayedProviders.length === 0 && (
                <div className="col-span-full py-8 text-center rounded-lg border border-dashed text-muted-foreground text-sm">
                  No internal providers configured.
                </div>
              )}
            </div>
          </CardContent>
        </Card>

        <Card className="overflow-hidden">
          <CardHeader>
            <div className="flex items-center gap-2">
              <MonitorPlay className="h-5 w-5 text-primary" />
              <CardTitle className="text-lg font-semibold tracking-tight">Indexers</CardTitle>
            </div>
            <CardDescription>All configured indexers and their daily usage.</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="grid gap-3 grid-cols-1 sm:grid-cols-2">
              {displayedIndexers.map((idx) => {
                const apiUsedPct = idx.api_hits_limit > 0 ? ((idx.api_hits_limit - idx.api_hits_remaining) / idx.api_hits_limit) * 100 : 0
                const dlUsedPct = idx.downloads_limit > 0 ? ((idx.downloads_limit - idx.downloads_remaining) / idx.downloads_limit) * 100 : 0
                const barColor = (pct) => pct >= 90 ? 'bg-destructive' : pct >= 75 ? 'bg-chart-4' : 'bg-primary'
                const hasApiLimit = idx.api_hits_limit > 0
                const hasDlLimit = idx.downloads_limit > 0
                const isEnabled = idx.enabled !== false
                const indexerUrl = indexerUrls.get((idx.name || '').trim()) || ''
                const healthRecord = healthFor(healthByName, 'indexer', (idx.name || '').trim())
                const healthBlocked = isBlocked(healthRecord)
                return (
                  <Card
                    key={idx.name}
                    className={cn("overflow-hidden h-full", !isEnabled && "opacity-60 grayscale")}
                  >
                    <CardHeader className="p-4 pb-2">
                      <div className="flex items-center gap-2">
                        <CardTitle className="text-base font-semibold truncate leading-tight" title={idx.name}>{idx.name}</CardTitle>
                        <TooltipProvider delayDuration={100}>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Badge variant="outline" className="ml-auto h-5 min-w-5 rounded-full px-1.5">
                                <span className={cn("h-1.5 w-1.5 rounded-full",
                                  !isEnabled || healthBlocked ? "bg-destructive"
                                    : healthRecord ? "bg-amber-500"
                                      : "bg-green-600")} />
                              </Badge>
                            </TooltipTrigger>
                            <TooltipContent>
                              {!isEnabled ? 'Inactive' : healthRecord ? healthReasonLabel(healthRecord.reason) : 'Active'}
                            </TooltipContent>
                          </Tooltip>
                        </TooltipProvider>
                      </div>
                      <p className="text-[10px] text-muted-foreground truncate" title={indexerUrl}>{indexerUrl}</p>
                    </CardHeader>
                    <CardContent className="p-4 pt-0">
                      <div className="grid grid-cols-2 gap-4">
                        <div className="space-y-1.5">
                          <p className="text-[11px] font-medium text-muted-foreground uppercase tracking-wider">API hits</p>
                          <p className="text-lg font-bold tabular-nums text-primary">{idx.api_hits_used}</p>
                          {hasApiLimit && (
                            <div className="w-full bg-muted h-2 rounded-full overflow-hidden mt-1">
                              <div className={cn("h-full transition-all duration-500 rounded-full", barColor(apiUsedPct))} style={{ width: `${apiUsedPct}%` }} />
                            </div>
                          )}
                          <p className="text-[11px] text-muted-foreground">
                            {hasApiLimit ? `of ${idx.api_hits_limit} today` : 'Unlimited'}
                          </p>
                        </div>
                        <div className="space-y-1.5">
                          <p className="text-[11px] font-medium text-muted-foreground uppercase tracking-wider">Downloads</p>
                          <p className="text-lg font-bold tabular-nums text-primary">{idx.downloads_used}</p>
                          {hasDlLimit && (
                            <div className="w-full bg-muted h-2 rounded-full overflow-hidden mt-1">
                              <div className={cn("h-full transition-all duration-500 rounded-full", barColor(dlUsedPct))} style={{ width: `${dlUsedPct}%` }} />
                            </div>
                          )}
                          <p className="text-[11px] text-muted-foreground">
                            {hasDlLimit ? `of ${idx.downloads_limit} today` : 'Unlimited'}
                          </p>
                        </div>
                      </div>
                      <ComponentHealthNotice record={healthRecord} onRefresh={onRefreshComponentHealth} />
                    </CardContent>
                  </Card>
                )
              })}
              {displayedIndexers.length === 0 && (
                <div className="col-span-full py-8 text-center rounded-lg border border-dashed text-muted-foreground text-sm">
                  No internal indexers configured.
                </div>
              )}
            </div>
          </CardContent>
        </Card>
        </div>
      </div>

      <Dialog open={Boolean(activeSessionToClose)} onOpenChange={(open) => { if (!open) setActiveSessionToClose(null) }}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>End active stream?</DialogTitle>
            <DialogDescription className="break-words [overflow-wrap:anywhere]">
              {activeSessionToClose
                ? `This will stop playback for "${activeSessionToClose.title}".`
                : 'This will stop playback for the selected stream.'}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter className="flex-row flex-wrap items-center justify-center gap-2 sm:justify-center sm:space-x-0">
            <Button type="button" variant="outline" className="min-w-28" onClick={() => setActiveSessionToClose(null)}>
              Cancel
            </Button>
            <Button type="button" variant="destructive" className="min-w-28" onClick={confirmCloseActiveSession}>
              End stream
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
