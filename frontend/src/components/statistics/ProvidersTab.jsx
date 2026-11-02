import { Activity, Database, Gauge, Globe } from "lucide-react"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { formatCount, formatMb, formatPercent } from "@/lib/statistics"
import { MeterCell, NoSamples, ShareBar, SortableTable, StatGrid, StatTile } from "./parts"

const MISSING_HINT = 'Share of article requests this provider could not serve. Each miss sends the request on to the next provider.'
const PEAK_HINT = 'Highest speed seen in a snapshot during the range.'

function missingTone(percent) {
  if (percent >= 10) return 'bad'
  if (percent >= 2) return 'warn'
  return 'primary'
}

const COLUMNS = [
  {
    key: 'name',
    label: 'Provider',
    align: 'left',
    defaultDir: 'asc',
    render: (r) => (
      <div className="min-w-0">
        <div className="truncate font-medium">{r.name}</div>
        {r.host && <div className="truncate text-xs text-muted-foreground">{r.host}</div>}
      </div>
    ),
  },
  { key: 'downloadedMb', label: 'Downloaded', render: (r) => formatMb(r.downloadedMb) },
  { key: 'sharePercent', label: 'Traffic share', render: (r) => <MeterCell percent={r.sharePercent} label={formatPercent(r.sharePercent)} /> },
  { key: 'peakMbps', label: 'Peak speed', hint: PEAK_HINT, render: (r) => (r.peakMbps > 0 ? `${r.peakMbps.toFixed(1)} Mbps` : '—') },
  { key: 'articles', label: 'Articles', render: (r) => formatCount(r.articles) },
  {
    key: 'missingPercent',
    label: 'Missing',
    hint: MISSING_HINT,
    defaultDir: 'asc',
    render: (r) => (r.missingPercent == null
      ? <NoSamples />
      : (
        <MeterCell
          percent={r.missingPercent}
          label={formatPercent(r.missingPercent)}
          tone={missingTone(r.missingPercent)}
          title={`${r.articlesAvailable} available / ${r.articlesMissing} missing`}
        />
      )),
  },
  { key: 'maxConns', label: 'Connections', render: (r) => (r.maxConns > 0 ? formatCount(r.maxConns) : '—') },
]

// ProvidersTab is everything about download: which provider carries the
// traffic, how fast, and how often it is missing what was asked for.
export function ProvidersTab({ rows, summary, actions }) {
  const shareItems = [...rows]
    .sort((a, b) => b.downloadedMb - a.downloadedMb)
    .map((r) => ({ name: r.name, percent: r.sharePercent, downloadedMb: r.downloadedMb }))

  return (
    <div className="flex flex-col gap-4 md:gap-6">
      <StatGrid className="xl:grid-cols-4">
        <StatTile icon={Database} label="Downloaded" value={formatMb(summary.downloadedMb)} detail={`${summary.active} of ${summary.count} providers served data`} />
        <StatTile
          icon={Activity}
          label="Article availability"
          value={formatPercent(summary.availabilityPercent)}
          detail={summary.articles > 0 ? `${formatCount(summary.articlesMissing)} missing of ${formatCount(summary.articles)}` : 'No articles fetched'}
          hint="Share of article requests that the asked provider could serve."
        />
        <StatTile icon={Gauge} label="Peak speed" value={summary.peakMbps > 0 ? `${summary.peakMbps.toFixed(1)} Mbps` : '—'} detail="Fastest single provider" hint={PEAK_HINT} />
        <StatTile
          icon={Globe}
          label="Top provider"
          value={summary.top ? formatPercent(summary.top.sharePercent, 0) : '—'}
          detail={summary.top ? `of traffic via ${summary.top.name}` : 'No traffic in range'}
        />
      </StatGrid>

      <Card className="overflow-hidden">
        <CardHeader>
          <CardTitle>Traffic distribution</CardTitle>
          <CardDescription>How downloaded data splits across providers — a quick check that priority and failover behave as configured.</CardDescription>
        </CardHeader>
        <CardContent>
          {summary.downloadedMb > 0
            ? <ShareBar items={shareItems} format={(i) => formatMb(i.downloadedMb)} />
            : <div className="py-6 text-center text-sm text-muted-foreground">No data downloaded in this range.</div>}
        </CardContent>
      </Card>

      <Card className="overflow-hidden">
        <CardHeader>
          <CardTitle>Per provider</CardTitle>
          <CardDescription>Click a column to sort. Hover a header for what it measures.</CardDescription>
        </CardHeader>
        <CardContent>
          <SortableTable
            columns={COLUMNS}
            rows={rows}
            initialSort={{ key: 'downloadedMb', dir: 'desc' }}
            emptyText="No provider statistics in this range."
            actions={actions}
          />
        </CardContent>
      </Card>
    </div>
  )
}
