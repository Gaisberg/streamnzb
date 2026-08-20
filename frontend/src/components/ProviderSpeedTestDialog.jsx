import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, focusDialogCloseButton } from "@/components/ui/dialog"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { ChartContainer, ChartTooltip, ChartTooltipContent } from "@/components/ui/chart"
import { Area, AreaChart, Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts"
import { AlertTriangle, ArrowDown, Check, Gauge, Loader2, Plug, X } from "lucide-react"
import { apiFetch, SPEEDTEST_PROGRESS_EVENT } from "@/api"
import { formatBytes } from "@/lib/utils"

const rampChartConfig = {
  mbps: { label: 'Mbps', color: "hsl(var(--primary))" },
}

const sparklineConfig = {
  mbps: { label: 'Mbps', color: "hsl(var(--primary))" },
}

// The gauge sweeps 270°, from bottom-left clockwise through the top.
const GAUGE_START_ANGLE = 135
const GAUGE_SWEEP = 270
const GAUGE_CX = 110
const GAUGE_CY = 110
const GAUGE_RADIUS = 88

// Ticks are spaced logarithmically: connection speeds worth distinguishing span
// three orders of magnitude, and a linear dial would bunch everything below
// 100 Mbps into the first few degrees.
const GAUGE_TICKS = [
  { label: '0', value: 0 },
  { label: '.5', value: 0.5 },
  { label: '1', value: 1 },
  { label: '10', value: 10 },
  { label: '100', value: 100 },
  { label: '500', value: 500 },
  { label: '1000+', value: 1000 },
]

const MAX_SAMPLES = 120

// Mirrors minTrustedWindowMS in pkg/usenet/speedtest: below this a step's
// window is too short to be steady, and the backend leaves it out of the peak.
const MIN_TRUSTED_WINDOW_MS = 1500

function gaugeFraction(mbps) {
  if (!mbps || mbps <= 0.1) return 0
  const clamped = Math.min(mbps, 1000)
  return Math.min(1, Math.max(0, (Math.log10(clamped) + 1) / 4))
}

function polar(radius, fraction) {
  const angle = ((GAUGE_START_ANGLE + fraction * GAUGE_SWEEP) * Math.PI) / 180
  return { x: GAUGE_CX + radius * Math.cos(angle), y: GAUGE_CY + radius * Math.sin(angle) }
}

function formatMbps(value) {
  if (!value) return '—'
  return `${value >= 100 ? Math.round(value) : value.toFixed(1)} Mbps`
}

function formatMs(value) {
  if (!value) return '—'
  return `${value >= 100 ? Math.round(value) : value.toFixed(1)} ms`
}

// useSmoothed eases a value toward its target one animation frame at a time.
//
// The dial's fill and its knob have to move as one, and two CSS transitions
// cannot promise that: a rotate-about-a-point is interpolated as a matrix, so
// the knob cuts a chord across the dial instead of riding the arc, and it lands
// out of step with the fill. Smoothing a single number here and deriving the
// readout, the fill and the knob from it keeps all three locked together.
function useSmoothed(target) {
  const [value, setValue] = useState(target)
  const latest = useRef(target)

  useEffect(() => {
    let frame = 0
    const step = () => {
      const distance = target - latest.current
      // A fifth of the remaining distance per frame closes on a new reading
      // well inside the 250ms between samples, without visible stepping.
      const next = Math.abs(distance) < 0.01 ? target : latest.current + distance * 0.2
      latest.current = next
      setValue(next)
      if (next !== target) frame = requestAnimationFrame(step)
    }
    frame = requestAnimationFrame(step)
    return () => cancelAnimationFrame(frame)
  }, [target])

  return value
}

function Speedometer({ mbps, peakMbps, caption }) {
  const smoothed = useSmoothed(mbps || 0)
  const fraction = gaugeFraction(smoothed)
  const peakFraction = gaugeFraction(peakMbps)
  const start = polar(GAUGE_RADIUS, 0)
  const end = polar(GAUGE_RADIUS, 1)
  const arcPath = `M ${start.x} ${start.y} A ${GAUGE_RADIUS} ${GAUGE_RADIUS} 0 1 1 ${end.x} ${end.y}`
  const knob = polar(GAUGE_RADIUS, fraction)
  const peakInner = polar(GAUGE_RADIUS - 9, peakFraction)
  const peakOuter = polar(GAUGE_RADIUS + 9, peakFraction)
  const display = smoothed ? (smoothed >= 100 ? smoothed.toFixed(0) : smoothed.toFixed(1)) : '0.0'

  return (
    <div className="relative mx-auto w-full max-w-[260px]">
      <svg viewBox="0 0 220 220" className="w-full" role="img" aria-label={`${display} Mbps`}>
        <path
          d={arcPath}
          fill="none"
          stroke="hsl(var(--muted))"
          strokeWidth={14}
          strokeLinecap="round"
        />
        <path
          d={arcPath}
          fill="none"
          stroke="hsl(var(--primary))"
          strokeWidth={14}
          strokeLinecap="round"
          pathLength={100}
          strokeDasharray={`${fraction * 100} 100`}
        />

        {GAUGE_TICKS.map((tick) => {
          const point = polar(GAUGE_RADIUS - 26, gaugeFraction(tick.value))
          return (
            <text
              key={tick.label}
              x={point.x}
              y={point.y}
              textAnchor="middle"
              dominantBaseline="middle"
              className="fill-muted-foreground"
              style={{ fontSize: 11 }}
            >
              {tick.label}
            </text>
          )
        })}

        {peakMbps > 0 && (
          <line
            x1={peakInner.x}
            y1={peakInner.y}
            x2={peakOuter.x}
            y2={peakOuter.y}
            stroke="hsl(var(--foreground))"
            strokeWidth={2}
            opacity={0.45}
          />
        )}

        <circle
          cx={knob.x}
          cy={knob.y}
          r={7}
          fill="hsl(var(--background))"
          stroke="hsl(var(--primary))"
          strokeWidth={3}
        />
      </svg>

      <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center">
        <ArrowDown className="h-5 w-5 text-primary" />
        <div className="mt-1 text-3xl font-semibold tabular-nums">{display}</div>
        <div className="text-[10px] uppercase tracking-wide text-muted-foreground">{caption}</div>
      </div>
    </div>
  )
}

function Panel({ label, value, hint, children }) {
  return (
    <div className="rounded-md border border-border/60 p-3">
      <div className="text-[10px] uppercase tracking-wide text-muted-foreground">{label}</div>
      {children}
      {value !== undefined && <div className="mt-1 text-lg font-semibold tabular-nums">{value}</div>}
      {hint && <div className="mt-0.5 text-[10px] text-muted-foreground">{hint}</div>}
    </div>
  )
}

// panelColumnsClass keeps the readout row square: Ping alone, Ping plus one
// suggestion, or all three.
function panelColumnsClass(hasConnectionSuggestion, hasDepthSuggestion) {
  const panels = 1 + (hasConnectionSuggestion ? 1 : 0) + (hasDepthSuggestion ? 1 : 0)
  if (panels >= 3) return 'grid-cols-3'
  if (panels === 2) return 'grid-cols-2'
  return 'grid-cols-1'
}

// pipelineOneConnStep is the step the depth suggestion was measured from, so
// the explanation under it can quote the same number the backend used.
function pipelineOneConnStep(steps) {
  return (steps || []).find((step) => step.connections === 1) || null
}

export function ProviderSpeedTestDialog({ open, onOpenChange, provider, onApplyConnections, onApplyPipelineDepth }) {
  const providerName = provider?.name || ''
  const [report, setReport] = useState(null)
  const [liveSteps, setLiveSteps] = useState([])
  const [samples, setSamples] = useState([])
  const [liveMbps, setLiveMbps] = useState(0)
  const [livePeak, setLivePeak] = useState(0)
  const [liveConnections, setLiveConnections] = useState(0)
  const [running, setRunning] = useState(false)
  const [connectionResult, setConnectionResult] = useState(null)
  const [testingConnection, setTestingConnection] = useState(false)
  const [error, setError] = useState('')
  const [quick, setQuick] = useState(false)
  const [source, setSource] = useState('public')
  const [applying, setApplying] = useState(false)

  useEffect(() => {
    if (!open || !providerName) return
    setError('')
    setLiveSteps([])
    setSamples([])
    setLiveMbps(0)
    setLivePeak(0)
    setConnectionResult(null)
    apiFetch('/api/providers/speedtest')
      .then((reports) => setReport(reports?.[providerName] || null))
      .catch(() => setReport(null))
  }, [open, providerName])

  // Steps and samples arrive over the websocket while the run's own request is
  // still open, so the gauge moves live instead of the whole ramp appearing at
  // the end.
  useEffect(() => {
    if (!open) return undefined
    const onProgress = (event) => {
      const detail = event?.detail
      if (!detail || detail.provider !== providerName) return
      if (detail.sample) {
        const mbps = detail.sample.mbps || 0
        setLiveMbps(mbps)
        setLivePeak((current) => Math.max(current, mbps))
        setLiveConnections(detail.sample.connections || 0)
        setSamples((current) => [...current, { mbps: Number(mbps.toFixed(2)) }].slice(-MAX_SAMPLES))
        return
      }
      if (detail.step) {
        setLiveSteps((current) => [...current.filter((step) => step.connections !== detail.step.connections), detail.step])
      }
    }
    window.addEventListener(SPEEDTEST_PROGRESS_EVENT, onProgress)
    return () => window.removeEventListener(SPEEDTEST_PROGRESS_EVENT, onProgress)
  }, [open, providerName])

  const runSpeedTest = useCallback(async () => {
    setRunning(true)
    setError('')
    setLiveSteps([])
    setSamples([])
    setLiveMbps(0)
    setLivePeak(0)
    setLiveConnections(0)
    setReport(null)
    try {
      const result = await apiFetch('/api/providers/speedtest', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: providerName, quick, source }),
      })
      setReport(result)
    } catch (err) {
      setError(err?.message || 'Speed test failed')
      // A run can outlive a reverse proxy's request timeout even though the
      // server finished and stored it, so recover the stored report rather than
      // showing only the transport error.
      try {
        const stored = await apiFetch('/api/providers/speedtest')
        if (stored?.[providerName]) setReport(stored[providerName])
      } catch {
        // keep the original error
      }
    } finally {
      setRunning(false)
      setLiveMbps(0)
    }
  }, [providerName, quick, source])

  const runConnectionTest = useCallback(async () => {
    setTestingConnection(true)
    setError('')
    setConnectionResult(null)
    try {
      const result = await apiFetch('/api/providers/test', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: providerName }),
      })
      setConnectionResult(result)
    } catch (err) {
      setError(err?.message || 'Connection test failed')
    } finally {
      setTestingConnection(false)
    }
  }, [providerName])

  const steps = useMemo(() => {
    const rows = report?.steps?.length ? report.steps : liveSteps
    return [...rows].sort((a, b) => a.connections - b.connections)
  }, [report, liveSteps])

  const rampChartData = useMemo(() => steps.map((step) => ({
    name: `${step.connections}`,
    mbps: Number((step.mbps || 0).toFixed(1)),
  })), [steps])

  const peakMbps = report?.peak_mbps || Math.max(0, ...steps.map((step) => step.mbps || 0))
  const gaugeValue = running ? liveMbps : peakMbps
  // While the run is live the marker is a high-water mark of the needle itself,
  // so it moves the moment a reading beats the best one seen rather than waiting
  // for the step to finish. Once the run is over it goes back to the report's
  // peak — the steady-state figure the suggestion is measured against, and the
  // one the needle is now parked on — instead of an instantaneous sample spike.
  const markerMbps = running ? Math.max(livePeak, peakMbps) : peakMbps
  const suggested = report?.suggested_connections || 0
  const plateauFound = Boolean(report?.plateau_found)
  // The saved provider wins over the stored report: the report records what the
  // last run measured, but everything shown here — what the next run will ramp
  // to, and whether the suggestion differs from what is set — is about the
  // provider's current connection count.
  const configured = provider?.connections || report?.configured_connections || 0
  // Only a real plateau is worth acting on. Without one the ramp merely ran out
  // of connections, so the number is a floor, not a recommendation.
  const canApply = plateauFound && suggested > 0 && suggested !== configured
  // A single-step run has nothing to compare: a quick test measures only the
  // count it was handed back, so reporting it as "suggested" would dress the
  // input up as a finding. While a run is in flight the toggle is frozen, so it
  // already says which kind of run this is, before any step has landed.
  const canSuggest = running ? !quick : steps.length > 1
  // Depth is measured from the single-connection step alone, so unlike the
  // connection suggestion it does not need a plateau — but it does need that
  // step, which a quick run handed a higher count never produces. Zero means
  // the run could not support a suggestion at all.
  const suggestedDepth = report?.suggested_pipeline_depth || 0
  const pipelineGain = report?.pipeline_gain || 0
  const configuredDepth = provider?.pipeline_depth ?? null
  const canApplyDepth = suggestedDepth > 0 && suggestedDepth !== configuredDepth
  const depthLabel = suggestedDepth === 1 ? 'Off' : suggestedDepth ? `${suggestedDepth}` : '—'
  const gainLabel = pipelineGain > 1 ? `+${Math.round((pipelineGain - 1) * 100)}% per connection` : 'no gap worth hiding'
  const latency = report?.latency_ms || connectionResult?.latency_ms || 0
  const hasResults = Boolean(report) || steps.length > 0 || running

  const handleApply = async () => {
    setApplying(true)
    try {
      await onApplyConnections?.(suggested)
      onOpenChange(false)
    } catch (err) {
      setError(err?.message || 'Failed to apply the suggested connection count')
    } finally {
      setApplying(false)
    }
  }

  const handleApplyDepth = async () => {
    setApplying(true)
    try {
      await onApplyPipelineDepth?.(suggestedDepth)
      onOpenChange(false)
    } catch (err) {
      setError(err?.message || 'Failed to apply the suggested articles per request')
    } finally {
      setApplying(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => {
      if (running) return
      onOpenChange(nextOpen)
    }}>
      <DialogContent className="flex max-h-[85vh] max-w-3xl flex-col overflow-hidden" onOpenAutoFocus={focusDialogCloseButton}>
        <DialogHeader>
          <DialogTitle>Speed test — {providerName}</DialogTitle>
          <DialogDescription>
            Downloads real articles at increasing connection counts to find where this provider stops getting faster.
            The bytes count against your account's usage.
          </DialogDescription>
        </DialogHeader>

        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto pr-1">
          <div className="space-y-3 rounded-md border border-border/60 p-3">
            <div className="flex flex-col gap-3 min-[420px]:flex-row min-[420px]:items-center min-[420px]:justify-between">
              <div className="min-w-0">
                <Label className="text-sm font-medium">Test articles</Label>
                <p className="mt-0.5 text-[10px] text-muted-foreground">
                  A fixed public post keeps results comparable between providers. Your library gives articles of realistic age instead.
                </p>
              </div>
              <ToggleGroup
                type="single"
                value={source}
                onValueChange={(value) => { if (value) setSource(value) }}
                variant="outline"
                size="sm"
                className="flex shrink-0 flex-wrap gap-1"
                disabled={running}
              >
                <ToggleGroupItem value="public" className="h-8 px-2.5 text-xs">Public test NZB</ToggleGroupItem>
                <ToggleGroupItem value="library" className="h-8 px-2.5 text-xs">My library</ToggleGroupItem>
              </ToggleGroup>
            </div>

            <div className="flex flex-col gap-3 border-t border-border/60 pt-3 min-[420px]:flex-row min-[420px]:items-center min-[420px]:justify-between">
              <div className="min-w-0">
                <Label className="text-sm font-medium">Quick test</Label>
                <p className="mt-0.5 text-[10px] text-muted-foreground">
                  Measure only {configured || 'the configured'} connections instead of ramping 1 → {configured || 'max'}. Faster, but it cannot suggest a connection count.
                </p>
              </div>
              <Switch checked={quick} onCheckedChange={(checked) => setQuick(checked === true)} disabled={running} />
            </div>

            <div className="flex flex-wrap items-center gap-2 border-t border-border/60 pt-3">
              <Button type="button" variant="destructive" onClick={() => void runSpeedTest()} disabled={running || testingConnection}>
                {running ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <Gauge className="mr-2 h-4 w-4" />}
                {running ? 'Running…' : 'Run speed test'}
              </Button>
              <Button type="button" variant="outline" onClick={() => void runConnectionTest()} disabled={running || testingConnection}>
                {testingConnection ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <Plug className="mr-2 h-4 w-4" />}
                Test connection
              </Button>
              {connectionResult && (
                <span className="text-xs text-muted-foreground">
                  {connectionResult.ok
                    ? `Connected in ${formatMs(connectionResult.connect_ms)}, round-trip ${formatMs(connectionResult.latency_ms)}`
                    : connectionResult.error}
                </span>
              )}
            </div>
          </div>

          {error && (
            <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
              {error}
            </div>
          )}

          {report?.warning && (
            <div className="flex items-start gap-2 rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-400">
              <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              <span>{report.warning}</span>
            </div>
          )}

          {hasResults && (
            <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] sm:items-center">
              <Speedometer
                mbps={gaugeValue}
                peakMbps={markerMbps}
                caption={running ? 'Mbps live' : 'Mbps peak'}
              />

              <div className="space-y-3">
                <Panel label="Download" value={formatMbps(gaugeValue)} hint={running && liveConnections ? `${liveConnections} connections` : report?.source}>
                  <div className="mt-1 h-14">
                    {samples.length > 1 ? (
                      <ChartContainer config={sparklineConfig} className="h-14 w-full">
                        <AreaChart data={samples} margin={{ top: 2, right: 0, left: 0, bottom: 0 }}>
                          <Area
                            type="monotone"
                            dataKey="mbps"
                            stroke="var(--color-mbps)"
                            fill="var(--color-mbps)"
                            fillOpacity={0.25}
                            strokeWidth={2}
                            isAnimationActive={false}
                            dot={false}
                          />
                        </AreaChart>
                      </ChartContainer>
                    ) : (
                      <div className="flex h-14 items-center text-[10px] text-muted-foreground">
                        {running ? 'Sampling…' : 'Run a test to see live throughput'}
                      </div>
                    )}
                  </div>
                </Panel>

                <div className={`grid gap-3 ${panelColumnsClass(canSuggest, suggestedDepth > 0)}`}>
                  <Panel label="Ping" value={formatMs(latency)} hint="DATE round-trip" />
                  {canSuggest && (
                    <Panel
                      label="Suggested"
                      value={suggested ? (plateauFound ? `${suggested}` : `≥${suggested}`) : '—'}
                      hint={
                        !suggested
                          ? 'connections'
                          : plateauFound
                            ? `connections (set: ${configured})`
                            : 'still climbing at max'
                      }
                    />
                  )}
                  {suggestedDepth > 0 && (
                    <Panel label="Articles/request" value={depthLabel} hint={gainLabel} />
                  )}
                </div>

                {suggestedDepth > 0 && (
                  <p className="text-[10px] text-muted-foreground">
                    Measured from the one-connection step: {formatMs(pipelineOneConnStep(steps)?.ttfb_median_ms)} of every article was
                    spent waiting for it to start. Requesting several at once hides that — but only once read-ahead has run out of
                    connections, so it lifts a busy or capped stream rather than an idle one.
                  </p>
                )}

                {report && (
                  <p className="text-[10px] text-muted-foreground">
                    Downloaded {formatBytes(report.total_bytes)} from {report.source}
                  </p>
                )}
              </div>
            </div>
          )}

          {report?.resolutions?.length > 0 && (
            <div className="grid grid-cols-3 gap-3">
              {report.resolutions.map((tier) => (
                <div
                  key={tier.label}
                  className={`rounded-md border p-3 ${tier.achieved ? 'border-border/60' : 'border-destructive/40'}`}
                >
                  <div className="flex items-center gap-1.5">
                    {tier.achieved
                      ? <Check className="h-3.5 w-3.5 text-green-600 dark:text-green-500" />
                      : <X className="h-3.5 w-3.5 text-destructive" />}
                    <span className="text-[10px] uppercase tracking-wide text-muted-foreground">{tier.label}</span>
                  </div>
                  <div className="mt-1 text-lg font-semibold tabular-nums">
                    {canSuggest && tier.connections
                      ? `${tier.connections} conn${tier.connections === 1 ? '' : 's'}`
                      : tier.achieved ? 'OK' : 'Too slow'}
                  </div>
                  <div className="mt-0.5 text-[10px] text-muted-foreground">
                    needs {Math.round(tier.required_mbps)} Mbps
                    {tier.streams > 0 && ` · ${tier.streams} stream${tier.streams === 1 ? '' : 's'}`}
                  </div>
                </div>
              ))}
            </div>
          )}

          {steps.length > 0 && (
            <ChartContainer config={rampChartConfig} className="w-full" style={{ height: `${Math.max(160, steps.length * 42)}px` }}>
              <BarChart data={rampChartData} layout="vertical" margin={{ top: 8, right: 12, left: 12, bottom: 8 }}>
                <CartesianGrid horizontal={false} />
                <XAxis type="number" tick={{ fontSize: 11 }} />
                <YAxis type="category" dataKey="name" width={48} tick={{ fontSize: 11 }} />
                <ChartTooltip content={<ChartTooltipContent />} />
                <Bar dataKey="mbps" fill="var(--color-mbps)" radius={4} name="mbps" />
              </BarChart>
            </ChartContainer>
          )}

          {steps.length > 0 && (
            <div className="overflow-x-auto rounded-md border border-border/60">
              <table className="w-full text-sm">
                <thead className="bg-muted/40 text-muted-foreground">
                  <tr>
                    <th className="px-3 py-2 text-left font-medium">Connections</th>
                    <th className="px-3 py-2 text-right font-medium">Speed</th>
                    <th className="px-3 py-2 text-right font-medium">TTFB p50</th>
                    <th className="px-3 py-2 text-right font-medium">TTFB p95</th>
                    <th className="px-3 py-2 text-right font-medium">Articles</th>
                    <th className="px-3 py-2 text-right font-medium">Errors</th>
                  </tr>
                </thead>
                <tbody>
                  {steps.map((step) => (
                    <tr key={step.connections} className="border-t border-border/60">
                      <td className="px-3 py-2">{step.connections}</td>
                      <td className="px-3 py-2 text-right">
                        {formatMbps(step.mbps)}
                        {step.truncated && (
                          <span
                            className={`ml-1.5 rounded-full border px-1.5 py-0.5 text-[10px] ${
                              (step.window_ms || 0) < MIN_TRUSTED_WINDOW_MS
                                ? 'border-amber-500/40 text-amber-700 dark:text-amber-400'
                                : 'border-border text-muted-foreground'
                            }`}
                            title={`The byte ceiling ended this step after ${((step.window_ms || 0) / 1000).toFixed(1)}s${
                              (step.window_ms || 0) < MIN_TRUSTED_WINDOW_MS
                                ? ' — too short to be steady, so it is excluded from the peak'
                                : ' — still long enough to measure'
                            }`}
                          >
                            {((step.window_ms || 0) / 1000).toFixed(1)}s
                          </span>
                        )}
                      </td>
                      <td className="px-3 py-2 text-right">{formatMs(step.ttfb_median_ms)}</td>
                      <td className="px-3 py-2 text-right">{formatMs(step.ttfb_p95_ms)}</td>
                      <td className="px-3 py-2 text-right">{step.segments || 0}</td>
                      <td className="px-3 py-2 text-right">{(step.errors || 0) + (step.missing_articles || 0)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {!hasResults && (
            <p className="text-sm text-muted-foreground">No results yet. Run a test to measure this provider.</p>
          )}
        </div>

        <DialogFooter className="flex items-center justify-between gap-3">
          <div className="min-h-9 flex-1 text-xs text-muted-foreground">
            {report?.completed_at && `Last run ${new Date(report.completed_at).toLocaleString()}`}
          </div>
          <div className="flex flex-row items-center justify-end gap-2">
            {canApply && (
              <Button type="button" variant="outline" onClick={() => void handleApply()} disabled={applying || running}>
                {applying && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
                Use {suggested} connections
              </Button>
            )}
            {canApplyDepth && (
              <Button type="button" variant="outline" onClick={() => void handleApplyDepth()} disabled={applying || running}>
                {applying && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
                {suggestedDepth === 1 ? 'Turn pipelining off' : `Use ${suggestedDepth} articles/request`}
              </Button>
            )}
            <Button type="button" variant="destructive" onClick={() => onOpenChange(false)} disabled={running}>
              Close
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
