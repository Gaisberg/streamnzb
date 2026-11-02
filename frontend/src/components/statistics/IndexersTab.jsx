import { useMemo, useState } from 'react'
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts"
import { CheckCircle2, Clock, Gauge, Search, Sparkles, Zap } from "lucide-react"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { ChartContainer, ChartTooltip, ChartTooltipContent } from "@/components/ui/chart"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { selectClass } from "@/lib/utils"
import { formatCount, formatMs, formatPercent, sortRows } from "@/lib/statistics"
import { MeterCell, NoSamples, SortableTable, StatGrid, StatTile } from "./parts"

// Both columns have been read as something they are not: unique hits as "how
// often this indexer answered at all", availability as the indexer's uptime.
const UNIQUE_HITS_HINT = 'Deduplicated releases this indexer alone carried — no other indexer returned a copy of them.'
const AVAILNZB_AVAILABILITY_HINT =
  'Share of this indexer\'s releases that AvailNZB confirmed as available. Blank until AvailNZB has reported on a release from this indexer.'
const GRAB_SUCCESS_HINT =
  'Share of the NZBs grabbed from this indexer that went on to play. Plays that ended before the good threshold are inconclusive and count for neither side.'
const AVG_GRAB_HINT = 'Mean time to fetch one NZB from this indexer, body included.'
const UNIQUE_SUCCESS_HINT =
  'Releases that were exclusive to this indexer and went on to play — the overlap between unique hits and successful grabs.'
const QUOTA_HINT = 'API hits per day in this range against the indexer\'s daily API limit. Blank without a limit, or for All Time.'

// Chart metrics; lowerIsBetter flips the bar order so the best is on top.
const CHART_METRICS = {
  searches: { label: 'Searches', key: 'searches' },
  apiHits: { label: 'API hits', key: 'apiHits' },
  response: { label: 'Avg response (ms)', key: 'avgResponseMs', lowerIsBetter: true },
  grabs: { label: 'Grabs', key: 'grabs' },
  grabSuccess: { label: 'Grab success (%)', key: 'grabSuccessPercent' },
  uniqueHits: { label: 'Unique hits', key: 'uniqueHits' },
  uniquePlays: { label: 'Unique plays', key: 'uniquePlays' },
  avgGrab: { label: 'Avg grab (ms)', key: 'avgGrabMs', lowerIsBetter: true },
  availability: { label: 'AvailNZB availability (%)', key: 'availabilityPercent' },
}

function quotaTone(percent) {
  if (percent >= 90) return 'bad'
  if (percent >= 70) return 'warn'
  return 'primary'
}

const COLUMNS = [
  { key: 'name', label: 'Indexer', align: 'left', defaultDir: 'asc', render: (r) => <span className="font-medium">{r.name}</span> },
  { key: 'searches', label: 'Searches', render: (r) => formatCount(r.searches) },
  { key: 'apiHits', label: 'API hits', render: (r) => formatCount(r.apiHits) },
  {
    key: 'quotaPercent',
    label: 'Quota / day',
    hint: QUOTA_HINT,
    render: (r) => (r.quotaPercent == null
      ? <span className="text-muted-foreground">{r.apiLimit > 0 ? '—' : 'No limit'}</span>
      : <MeterCell percent={r.quotaPercent} label={formatPercent(r.quotaPercent, 0)} tone={quotaTone(r.quotaPercent)} />),
  },
  { key: 'avgResponseMs', label: 'Avg response', defaultDir: 'asc', render: (r) => (r.avgResponseMs == null ? <NoSamples /> : formatMs(r.avgResponseMs)) },
  { key: 'grabs', label: 'Grabs', render: (r) => formatCount(r.grabs) },
  {
    key: 'grabSuccessPercent',
    label: 'Grab success',
    hint: GRAB_SUCCESS_HINT,
    render: (r) => (r.grabSuccessPercent == null
      ? <NoSamples />
      : <span title={`${r.grabSuccess} successful / ${r.grabFailure} failed`}>{formatPercent(r.grabSuccessPercent)}</span>),
  },
  { key: 'avgGrabMs', label: 'Avg grab', hint: AVG_GRAB_HINT, defaultDir: 'asc', render: (r) => (r.avgGrabMs == null ? <NoSamples /> : formatMs(r.avgGrabMs)) },
  { key: 'uniqueHits', label: 'Unique hits', hint: UNIQUE_HITS_HINT, render: (r) => formatCount(r.uniqueHits) },
  { key: 'uniquePlays', label: 'Unique plays', hint: UNIQUE_SUCCESS_HINT, render: (r) => formatCount(r.uniquePlays) },
  {
    key: 'availabilityPercent',
    label: 'AvailNZB',
    hint: AVAILNZB_AVAILABILITY_HINT,
    render: (r) => (r.availabilityPercent == null
      ? <NoSamples />
      : <span title={`${r.availAvailable} available / ${r.availDiscarded} unavailable`}>{formatPercent(r.availabilityPercent)}</span>),
  },
]

// IndexersTab is everything about search: how much each indexer is asked,
// how fast it answers, how close it runs to its API limit and how useful
// what it returns turns out to be.
export function IndexersTab({ rows, summary, actions }) {
  const [metric, setMetric] = useState('searches')
  const spec = CHART_METRICS[metric]

  const chartData = useMemo(() => {
    const withValue = rows.filter((r) => r[spec.key] != null)
    return sortRows(withValue, spec.key, spec.lowerIsBetter ? 'asc' : 'desc')
      .map((r) => ({ name: r.name, value: Number(r[spec.key].toFixed(1)) }))
  }, [rows, spec])

  const chartConfig = useMemo(() => ({ value: { label: spec.label, color: "hsl(var(--primary))" } }), [spec])
  const chartHeight = Math.max(160, chartData.length * 36)
  const grabSamples = summary.grabSuccess + summary.grabFailure

  return (
    <div className="flex flex-col gap-4 md:gap-6">
      <StatGrid>
        <StatTile icon={Search} label="Searches" value={formatCount(summary.searches)} detail={`${summary.active} of ${summary.count} indexers used`} />
        <StatTile icon={Zap} label="API hits" value={formatCount(summary.apiHits)} detail="Requests sent to indexers" />
        <StatTile
          icon={Clock}
          label="Avg response"
          value={formatMs(summary.avgResponseMs)}
          detail="Weighted by searches"
        />
        <StatTile
          icon={CheckCircle2}
          label="Grab success"
          value={formatPercent(summary.grabSuccessPercent)}
          detail={grabSamples > 0 ? `${formatCount(summary.grabFailure)} failed of ${formatCount(grabSamples)}` : 'No completed plays yet'}
          hint={GRAB_SUCCESS_HINT}
        />
        <StatTile icon={Gauge} label="NZBs grabbed" value={formatCount(summary.grabs)} detail="Downloads used" />
        <StatTile
          icon={Sparkles}
          label="Unique hits"
          value={formatCount(summary.uniqueHits)}
          detail={`${formatCount(summary.uniquePlays)} went on to play`}
          hint={UNIQUE_HITS_HINT}
        />
      </StatGrid>

      <Card className="overflow-hidden">
        <CardHeader className="gap-3 sm:flex-row sm:items-start sm:justify-between">
          <div className="space-y-1.5">
            <CardTitle>Compare indexers</CardTitle>
            <CardDescription>Best performer on top for the selected metric.</CardDescription>
          </div>
          <div className="sm:max-w-[60%]">
            <select
              value={metric}
              onChange={(e) => e.target.value && setMetric(e.target.value)}
              className={`${selectClass} xl:hidden`}
              aria-label="Metric"
            >
              {Object.entries(CHART_METRICS).map(([key, opt]) => <option key={key} value={key}>{opt.label}</option>)}
            </select>
            <ToggleGroup
              type="single"
              value={metric}
              onValueChange={(v) => v && setMetric(v)}
              variant="outline"
              size="sm"
              className="hidden xl:flex flex-wrap justify-end gap-1"
            >
              {Object.entries(CHART_METRICS).map(([key, opt]) => (
                <ToggleGroupItem key={key} value={key} className="h-8 px-2.5 text-xs">{opt.label.replace(/ \((ms|%)\)$/, '')}</ToggleGroupItem>
              ))}
            </ToggleGroup>
          </div>
        </CardHeader>
        <CardContent>
          {chartData.length > 0 ? (
            <ChartContainer config={chartConfig} className="w-full" style={{ height: `${chartHeight}px` }}>
              <BarChart data={chartData} layout="vertical" margin={{ top: 4, right: 12, left: 4, bottom: 4 }}>
                <CartesianGrid horizontal={false} />
                <XAxis type="number" tick={{ fontSize: 11 }} />
                <YAxis type="category" dataKey="name" width={130} tick={{ fontSize: 11 }} />
                <ChartTooltip content={<ChartTooltipContent />} />
                <Bar dataKey="value" fill="var(--color-value)" radius={4} name="value" />
              </BarChart>
            </ChartContainer>
          ) : (
            <div className="py-8 text-center text-sm text-muted-foreground">No samples for this metric in range.</div>
          )}
        </CardContent>
      </Card>

      <Card className="overflow-hidden">
        <CardHeader>
          <CardTitle>Per indexer</CardTitle>
          <CardDescription>Click a column to sort. Hover a header for what it measures.</CardDescription>
        </CardHeader>
        <CardContent>
          <SortableTable
            columns={COLUMNS}
            rows={rows}
            initialSort={{ key: 'searches', dir: 'desc' }}
            emptyText="No indexer statistics in this range."
            actions={actions}
          />
        </CardContent>
      </Card>
    </div>
  )
}
