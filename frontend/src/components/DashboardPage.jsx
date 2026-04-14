import { useEffect, useMemo, useState } from 'react'
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
import { ComposedChart, Area, XAxis, YAxis } from "recharts"
import { Activity, Globe, X, MonitorPlay, Loader2 } from "lucide-react"
import { cn } from "@/lib/utils"
import { apiFetch } from '../api'

const chartConfig = {
  speed: {
    label: "Speed",
    color: "hsl(var(--primary))",
  },
  conns: {
    label: "Connections",
    color: "hsl(var(--primary))",
  },
}

function formatDownloadedMb(mb) {
  const n = Number(mb) || 0
  if (n >= 1000) return { value: (n / 1000).toFixed(2), unit: 'GB' }
  return { value: n.toFixed(1), unit: 'MB' }
}

export function DashboardPage({ stats, chartData, sendCommand, config }) {
  const [availNZBStatus, setAvailNZBStatus] = useState(null)
  const [availNZBStatusLoading, setAvailNZBStatusLoading] = useState(false)
  const [availNZBStatusError, setAvailNZBStatusError] = useState('')
  const [activeSessionToClose, setActiveSessionToClose] = useState(null)
  const availNZBEnabled = (config?.availnzb_mode || '') !== 'disabled'
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

  useEffect(() => {
    if (!availNZBEnabled) {
      setAvailNZBStatus(null)
      setAvailNZBStatusError('')
      setAvailNZBStatusLoading(false)
      return
    }

    let cancelled = false

    const fetchAvailNZBStatus = async () => {
      setAvailNZBStatusLoading(true)
      setAvailNZBStatusError('')
      try {
        const data = await apiFetch('/api/availnzb/status')
        if (!cancelled) setAvailNZBStatus(data || null)
      } catch (error) {
        if (!cancelled) {
          setAvailNZBStatus(null)
          setAvailNZBStatusError(error.message || 'Trust unavailable')
        }
      } finally {
        if (!cancelled) setAvailNZBStatusLoading(false)
      }
    }

    fetchAvailNZBStatus()
    return () => { cancelled = true }
  }, [availNZBEnabled])

  const rawAvailNZBTrustScore = Number(availNZBStatus?.status?.trust_score)
  const maxAvailNZBTrustScore = 60
  const availNZBTrustScore = Number.isFinite(rawAvailNZBTrustScore)
    ? (Math.max(0, Math.min(maxAvailNZBTrustScore, rawAvailNZBTrustScore)) / maxAvailNZBTrustScore) * 100
    : null
  const availNZBTrustSummary = `${Math.round(availNZBTrustScore ?? 0)}%`
  const availNZBTrustBarClass = availNZBTrustScore === null
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
                      <span className={cn("h-1.5 w-1.5 rounded-full", availNZBEnabled ? "bg-green-600" : "bg-destructive")} />
                    </Badge>
                  </TooltipTrigger>
                  <TooltipContent>{availNZBEnabled ? 'Active' : 'Inactive'}</TooltipContent>
                </Tooltip>
              </TooltipProvider>
            </div>
            {availNZBEnabled ? (
              <>
                <CardTitle className="flex items-center gap-2 tabular-nums">
                  {availNZBStatusLoading && <Loader2 className="h-4 w-4 animate-spin text-primary" />}
                  <span className="text-primary">{availNZBTrustSummary}</span>
                </CardTitle>
                <div className="mt-2 h-2 w-full overflow-hidden rounded-full bg-muted/70" aria-hidden="true">
                  <div
                    className={cn("h-full rounded-full transition-all duration-500", availNZBTrustBarClass)}
                    style={{ width: `${availNZBTrustScore ?? 0}%` }}
                  />
                </div>
              </>
            ) : (
              <CardTitle className="tabular-nums text-muted-foreground">0%</CardTitle>
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
            <CardTitle className="tabular-nums text-primary">{stats.active_sessions?.length ?? 0}</CardTitle>
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

        {/* Network chart */}
        <Card className="overflow-hidden">
        <CardHeader>
          <CardTitle>Network activity</CardTitle>
          <CardDescription>Speed (Mbps) and active connections over time</CardDescription>
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
            </ComposedChart>
          </ChartContainer>
        </CardContent>
        </Card>

        {/* Active sessions */}
        {stats.active_sessions?.length > 0 && (
          <Card className="overflow-hidden">
            <CardHeader>
              <div className="flex items-center gap-2">
                <Activity className="h-5 w-5 text-primary" />
                <CardTitle className="text-lg font-semibold tracking-tight">Active streams</CardTitle>
              </div>
              <CardDescription>Streams that are currently being played.</CardDescription>
            </CardHeader>
            <CardContent>
              <div className="space-y-2">
                {stats.active_sessions.map(sess => (
                  <Card key={sess.id} className="group relative min-w-0 pr-10">
                    <CardContent className="p-3">
                      <div
                        className="min-w-0 pr-2 text-sm font-medium leading-snug whitespace-normal break-words [overflow-wrap:anywhere] md:truncate md:whitespace-nowrap"
                        title={sess.title}
                      >
                        {sess.title}
                      </div>
                      <div className="text-xs text-muted-foreground truncate min-w-0">{sess.clients.join(', ')}</div>
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
            </CardContent>
          </Card>
        )}

        {/* Providers & Indexers */}
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        <Card className="overflow-hidden">
          <CardHeader>
            <div className="flex items-center gap-2">
              <Globe className="h-5 w-5 text-primary" />
              <CardTitle className="text-lg font-semibold tracking-tight">Usenet Providers</CardTitle>
            </div>
            <CardDescription>All configured providers and their current load.</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="grid gap-3 grid-cols-1 sm:grid-cols-2">
              {displayedProviders.map((p) => {
                const loadPct = (p.active_conns / (p.max_conns || 1)) * 100
                const isEnabled = p.enabled !== false
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
                                <span className={cn("h-1.5 w-1.5 rounded-full", isEnabled ? "bg-green-600" : "bg-destructive")} />
                              </Badge>
                            </TooltipTrigger>
                            <TooltipContent>{isEnabled ? 'Active' : 'Inactive'}</TooltipContent>
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
                        <span>Downloaded: {(p.downloaded_mb ?? 0).toFixed(1)} MB</span>
                        <TooltipProvider delayDuration={100}>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Badge variant="outline" className="h-4 px-1.5 text-[10px]">{p.max_conns}</Badge>
                            </TooltipTrigger>
                            <TooltipContent>Connections</TooltipContent>
                          </Tooltip>
                        </TooltipProvider>
                      </div>
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
            <CardDescription>All configured indexers and their current usage.</CardDescription>
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
                                <span className={cn("h-1.5 w-1.5 rounded-full", isEnabled ? "bg-green-600" : "bg-destructive")} />
                              </Badge>
                            </TooltipTrigger>
                            <TooltipContent>{isEnabled ? 'Active' : 'Inactive'}</TooltipContent>
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
