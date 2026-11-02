import { Activity, CheckCircle2, Database, Globe, PlayCircle, Search, Server, Zap } from "lucide-react"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { formatCount, formatMb, formatPercent } from "@/lib/statistics"
import { StatGrid, StatTile } from "./parts"

function LatencyCard({ icon: Icon, iconClass, title, description, summary, p50Class, emptyText }) {
  const total = summary?.total
  return (
    <Card className="overflow-hidden">
      <CardHeader className="pb-3">
        <div className="flex items-center gap-2">
          <Icon className={`h-5 w-5 ${iconClass}`} />
          <CardTitle>{title}</CardTitle>
        </div>
        <CardDescription>{description}</CardDescription>
      </CardHeader>
      <CardContent>
        {total?.sample_count > 0 ? (
          <div className="grid grid-cols-3 gap-2 text-center">
            <div className="rounded-md bg-muted/40 p-2">
              <div className="text-xs font-medium text-muted-foreground">p50</div>
              <div className={`text-lg font-bold ${p50Class}`}>{total.p50_ms.toFixed(0)} ms</div>
            </div>
            <div className="rounded-md bg-muted/40 p-2">
              <div className="text-xs font-medium text-muted-foreground">p95</div>
              <div className="text-lg font-bold text-amber-500">{total.p95_ms.toFixed(0)} ms</div>
            </div>
            <div className="rounded-md bg-muted/40 p-2">
              <div className="text-xs font-medium text-muted-foreground">p99</div>
              <div className="text-lg font-bold text-rose-500">{total.p99_ms.toFixed(0)} ms</div>
            </div>
            <div className="rounded-md bg-muted/20 p-2 text-xs">Min: {total.min_ms.toFixed(0)} ms</div>
            <div className="rounded-md bg-muted/20 p-2 text-xs">Avg: {total.avg_ms.toFixed(0)} ms</div>
            <div className="rounded-md bg-muted/20 p-2 text-xs">Max: {total.max_ms.toFixed(0)} ms</div>
          </div>
        ) : (
          <div className="py-6 text-center text-sm text-muted-foreground">{emptyText}</div>
        )}
        {total?.sample_count > 0 && (
          <div className="mt-2 text-[11px] text-muted-foreground">
            {formatCount(total.sample_count)} samples since the last restart; not affected by the date range.
          </div>
        )}
      </CardContent>
    </Card>
  )
}

// OverviewTab answers "how has StreamNZB been doing overall?" — installation
// totals only; per-indexer and per-provider detail lives in their own tabs.
export function OverviewTab({ indexerSummary, providerSummary, perfStats }) {
  const grabSamples = indexerSummary.grabSuccess + indexerSummary.grabFailure
  return (
    <div className="flex flex-col gap-4 md:gap-6">
      <StatGrid>
        <StatTile
          icon={Search}
          label="Searches"
          value={formatCount(indexerSummary.searches)}
          detail={`${formatCount(indexerSummary.apiHits)} indexer API hits`}
        />
        <StatTile
          icon={CheckCircle2}
          label="Grab success"
          value={formatPercent(indexerSummary.grabSuccessPercent)}
          detail={grabSamples > 0
            ? `${formatCount(indexerSummary.grabSuccess)} played / ${formatCount(indexerSummary.grabFailure)} failed`
            : `${formatCount(indexerSummary.grabs)} NZBs grabbed`}
          hint="Share of grabbed NZBs that went on to play."
        />
        <StatTile
          icon={Database}
          label="Downloaded"
          value={formatMb(providerSummary.downloadedMb)}
          detail={providerSummary.top
            ? `Most from ${providerSummary.top.name} (${providerSummary.top.sharePercent.toFixed(0)}%)`
            : 'No traffic in range'}
        />
        <StatTile
          icon={Activity}
          label="Article availability"
          value={formatPercent(providerSummary.availabilityPercent)}
          detail={providerSummary.articles > 0
            ? `${formatCount(providerSummary.articlesMissing)} of ${formatCount(providerSummary.articles)} missing`
            : 'No articles fetched'}
          hint="Share of article requests that a provider could serve."
        />
        <StatTile
          icon={Server}
          label="Active indexers"
          value={`${indexerSummary.active} / ${indexerSummary.count}`}
          detail="Searched or grabbed in range"
        />
        <StatTile
          icon={Globe}
          label="Active providers"
          value={`${providerSummary.active} / ${providerSummary.count}`}
          detail="Served data in range"
        />
      </StatGrid>

      <div className="grid gap-4 md:grid-cols-2">
        <LatencyCard
          icon={Zap}
          iconClass="text-amber-500"
          title="API Response Time (/stream)"
          description="Stremio stream query latency percentiles."
          summary={perfStats?.stream_summary}
          p50Class="text-primary"
          emptyText="No /stream requests recorded yet."
        />
        <LatencyCard
          icon={PlayCircle}
          iconClass="text-emerald-500"
          title="Playback Start Time (TTFF)"
          description="Time to First Frame from play request to video stream output."
          summary={perfStats?.ttff_summary}
          p50Class="text-emerald-500"
          emptyText="No playback sessions recorded yet."
        />
      </div>
    </div>
  )
}
