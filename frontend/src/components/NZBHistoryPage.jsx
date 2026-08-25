import { Fragment, useState, useEffect, useCallback, useMemo, useRef, memo } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, focusDialogCloseButton } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Checkbox } from '@/components/ui/checkbox'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { History, Loader2, ExternalLink, RefreshCw, Copy, Check, ChevronDown, ChevronRight, Info, Search as SearchIcon, SlidersHorizontal, Eraser } from 'lucide-react'
import { apiFetch } from '@/api'
import { cn } from '@/lib/utils'

function formatSize(bytes) {
  if (bytes <= 0) return '—'
  const gb = bytes / (1024 * 1024 * 1024)
  if (gb >= 1) return `${gb.toFixed(1)} GB`
  const mb = bytes / (1024 * 1024)
  if (mb >= 1) return `${mb.toFixed(0)} MB`
  const kb = bytes / 1024
  return `${kb.toFixed(0)} KB`
}

function formatAttemptResult(attempt) {
  if (attempt.preload) return 'Pending'
  if (isShortPlayAttempt(attempt)) return 'Short play'
  return attempt.success ? 'OK' : 'Failed'
}

function formatAttemptBadgeLabel(attempt) {
  if (attempt.preload) return 'Pending'
  if (attempt.success) return 'OK'
  return shortReason(attempt.failure_reason) || 'Failed'
}

function shortReason(reason) {
  const value = (reason || '').toLowerCase()
  if (!value) return ''
  if (value.includes('download limit reached') || value.includes('api limit reached') || value.includes('request limit reached')) return 'Limit'
  if (value.includes('playback startup timeout') || value.includes('timed out') || value.includes('context deadline exceeded')) return 'Timeout'
  if (value.includes('probe inspect') || value.includes('probe:') || value.includes('invalid container header')) return 'Probe'
  if (value.includes('playback probe ended') || value.includes('playback ended too early') || value.includes('threshold not reached')) return 'Short play'
  if (value.includes('episode target not found') || value.includes('no file')) return 'No file'
  if (value.includes('430') || value.includes('segment unavailable') || value.includes('not found')) return 'Segment'
  if (value.includes('eof')) return 'EOF'
  if (value.includes('corrupt') || value.includes('rapidyenc') || value.includes('yenc')) return 'Corrupt'
  if (value.includes('compressed')) return 'Compressed'
  if (value.includes('encrypted')) return 'Encrypted'
  return 'Error'
}

function isShortPlayAttempt(attempt) {
  return !attempt?.preload && !attempt?.success && shortReason(attempt?.failure_reason) === 'Short play'
}

function attemptBadgeClass(attempt, reasonLabel) {
  if (attempt.success) return 'bg-green-600 text-white hover:bg-green-600 hover:text-white dark:text-black'
  if (attempt.preload) return 'bg-muted text-foreground hover:bg-muted'
  if (reasonLabel === 'Limit') return 'bg-slate-500 text-white hover:bg-slate-500 hover:text-white dark:bg-slate-400 dark:text-black'
  if (reasonLabel === 'Short play') return 'bg-amber-500 text-white hover:bg-amber-500 hover:text-white dark:bg-amber-400 dark:text-black'
  return 'bg-red-500 text-white hover:bg-red-500 hover:text-white dark:bg-red-500 dark:text-black'
}

function formatDateTime(value) {
  return new Date(value).toLocaleString()
}

// utc forces the calendar date to be read as UTC, which a date-only value must
// be: it is stamped midnight UTC, so a local reading would slide it a day back
// for every viewer west of Greenwich.
function formatDateOnly(value, { utc = false } = {}) {
  return new Date(value).toLocaleDateString(undefined, utc ? { timeZone: 'UTC' } : undefined)
}

function formatTimeOnly(value) {
  return new Date(value).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

function formatTimeWithSeconds(value) {
  return new Date(value).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function formatContentTypeLabel(contentType) {
  if (contentType === 'series') return 'Series'
  if (contentType === 'movie') return 'Movie'
  return '—'
}

function formatMatchType(value) {
  if (value === 'exact_episode') return 'Exact episode'
  if (value === 'multi_episode') return 'Multi-episode'
  if (value === 'season_pack') return 'Season pack'
  if (value === 'complete_pack') return 'Complete pack'
  if (value === 'season_match') return 'Season match'
  return ''
}

function formatContentTitle(title, contentType, contentID) {
  const baseTitle = (title || '').trim()
  if (!baseTitle) return '—'
  if (contentType !== 'series' || !contentID) return baseTitle

  const parts = String(contentID).split(':')
  if (parts.length < 3) return baseTitle

  const season = Number(parts[parts.length - 2])
  const episode = Number(parts[parts.length - 1])
  if (!Number.isInteger(season) || !Number.isInteger(episode)) return baseTitle

  return `${baseTitle} S${String(season).padStart(2, '0')}E${String(episode).padStart(2, '0')}`
}

function buildBadMatchReport(attempt) {
  const details = [
    ['Time', attempt.tried_at ? new Date(attempt.tried_at).toISOString() : '—'],
    ['Content type', attempt.content_type || '—'],
    ['Content title', attempt.content_title || '—'],
    ['Content ID', attempt.content_id || '—'],
    ['Indexer', attempt.indexer_name || '—'],
    ['Release title', attempt.release_title || '—'],
    ['Match type', formatMatchType(attempt.match_type) || '—'],
    ['Served file', attempt.served_file || '—'],
    ['Release size', formatSize(attempt.release_size)],
    ['Result', formatAttemptResult(attempt)],
    ['Failure reason', attempt.failure_reason || '—'],
    ['Release URL', attempt.release_url || '—'],
    ['Slot path', attempt.slot_path || '—'],
  ].map(([label, value]) => `- ${label}: ${value}`)

  return [
    'Bad match report',
    '',
    'Why this is a bad match:',
    '- ',
    '',
    'Metadata:',
    ...details,
  ].join('\n')
}

function withinTimeframe(attempt, timeframe) {
  if (timeframe === 'all') return true
  const triedAt = new Date(attempt.tried_at).getTime()
  const now = Date.now()
  const dayMs = 24 * 60 * 60 * 1000
  if (timeframe === 'today') {
    const start = new Date()
    start.setHours(0, 0, 0, 0)
    return triedAt >= start.getTime()
  }
  if (timeframe === '7d') return triedAt >= now - 7 * dayMs
  if (timeframe === '30d') return triedAt >= now - 30 * dayMs
  return true
}

function matchesResult(attempt, result) {
  if (result === 'all') return true
  if (result === 'preload') return Boolean(attempt.preload)
  if (result === 'ok') return !attempt.preload && Boolean(attempt.success)
  if (result === 'failed') return !attempt.preload && !attempt.success && !isShortPlayAttempt(attempt)
  if (result === 'short_play') return isShortPlayAttempt(attempt)
  return true
}

function matchesSearch(attempt, search) {
  if (!search) return true
  const haystack = [
    attempt.content_title,
    attempt.content_id,
    attempt.release_title,
    attempt.indexer_name,
    attempt.provider_name,
    attempt.served_file,
    attempt.failure_reason,
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
  return haystack.includes(search.toLowerCase())
}

function matchesStream(attempt, streamName) {
  if (!streamName || streamName === 'all') return true
  return (attempt.stream_name || 'default') === streamName
}

function buildContentKey(attempt) {
  const identity = attempt.content_id || attempt.content_title || ''
  return [attempt.stream_name || 'default', attempt.content_type || '', identity].join('::')
}

function getSafeReleaseUrl(value) {
  if (!value) return ''
  try {
    const url = new URL(value)
    return url.protocol === 'http:' || url.protocol === 'https:' ? url.toString() : ''
  } catch {
    return ''
  }
}

function buildRequestGroups(attempts) {
  const byContent = new Map()
  attempts.forEach((attempt) => {
    const key = buildContentKey(attempt)
    const list = byContent.get(key) || []
    list.push(attempt)
    byContent.set(key, list)
  })

  const requestGroups = []
  const requestWindowMs = 15 * 60 * 1000

  byContent.forEach((contentAttempts, contentKey) => {
    const sorted = [...contentAttempts].sort((a, b) => new Date(b.tried_at) - new Date(a.tried_at))
    let cluster = []

    sorted.forEach((attempt) => {
      if (cluster.length === 0) {
        cluster = [attempt]
        return
      }

      const previous = cluster[cluster.length - 1]
      const gap = Math.abs(new Date(previous.tried_at).getTime() - new Date(attempt.tried_at).getTime())
      if (gap <= requestWindowMs) {
        cluster.push(attempt)
        return
      }

      requestGroups.push({ contentKey, attempts: cluster })
      cluster = [attempt]
    })

    if (cluster.length > 0) {
      requestGroups.push({ contentKey, attempts: cluster })
    }
  })

  return requestGroups
    .map((group, index) => {
      const attemptsSorted = [...group.attempts].sort((a, b) => new Date(b.tried_at) - new Date(a.tried_at))
      const latest = attemptsSorted[0]
      const oldest = attemptsSorted[attemptsSorted.length - 1]
      const active = attemptsSorted.find((a) => a.preload) || latest
      const okCount = attemptsSorted.filter((a) => !a.preload && a.success).length
      const failedCount = attemptsSorted.filter((a) => !a.preload && !a.success && !isShortPlayAttempt(a)).length
      const preloadCount = attemptsSorted.filter((a) => a.preload).length

      return {
        key: `${group.contentKey}::${index}::${oldest?.id || latest?.id || index}`,
        contentType: latest?.content_type || '',
        contentID: latest?.content_id || '',
        title: formatContentTitle(latest?.content_title, latest?.content_type, latest?.content_id),
        attempts: attemptsSorted,
        latest,
        active,
        requestTime: oldest?.tried_at || latest?.tried_at,
        okCount,
        failedCount,
        preloadCount,
      }
    })
    .sort((a, b) => new Date(b.requestTime || 0) - new Date(a.requestTime || 0))
}

const historyWindowMs = 15 * 60 * 1000

// searchShortCircuit reports why a search returned nothing without asking any
// indexer. Both reasons are deliberate — the episode is not out, or a rating
// cap refused the title — and a bare "No results" reads as though the indexers
// were asked and came back empty, which is the opposite of what happened.
function searchShortCircuit(snap) {
  if (!snap) return null
  if (snap.unaired_airs_at) {
    // Most streaming titles have no air time on record, only a date; showing a
    // clock reading for those would state something no source ever said.
    return {
      label: 'Not aired yet',
      detail: snap.unaired_time_known
        ? formatDateTime(snap.unaired_airs_at)
        : formatDateOnly(snap.unaired_airs_at, { utc: true }),
    }
  }
  if (snap.certification_blocked) {
    return { label: 'Rating blocked', detail: snap.certification_blocked }
  }
  return null
}

function diagnosticContentKey(d) {
  return [d.stream_name || 'default', d.content_type || '', d.content_id || ''].join('::')
}

// diagnosticStreamCount reads how many streams a search ultimately returned
// from its payload: the profile's kept count when a profile ran, otherwise
// whatever survived dedup.
function diagnosticStreamCount(diagnostic) {
  const snap = parseDiagnosticPayload(diagnostic)
  if (!snap) return null
  if (snap.profile_name) return snap.profile_kept || 0
  return snap.dedup_output || 0
}

// buildHistoryTimeline merges play-attempt groups with search-diagnostics rows
// into one timeline. Searches are the primary events: a diagnostics row inside
// an attempt group's window attaches to that group, and rows no attempt ever
// followed (browsed past, or filtered to zero streams) become their own
// "search" entries so they stop being invisible. Attempts with no matching
// search — direct-play NZBs, cache-served plays hours after the build, rows
// predating diagnostics — keep their attempt-only group untouched.
function buildHistoryTimeline(attemptGroups, diagnostics, includeSearchOnly) {
  const rows = Array.isArray(diagnostics) ? diagnostics : []
  const consumed = new Set()

  const groups = attemptGroups.map((group) => {
    const latest = group.latest
    if (!latest) return group
    const key = [latest.stream_name || 'default', group.contentType, group.contentID].join('::')
    const from = new Date(group.requestTime).getTime() - historyWindowMs
    const to = new Date(latest.tried_at).getTime() + historyWindowMs
    let best = null
    for (const d of rows) {
      if (diagnosticContentKey(d) !== key) continue
      const at = new Date(d.created_at).getTime()
      if (at < from || at > to) continue
      // Every row in the window belongs to this request; rebuilds otherwise
      // resurface as bogus search-only entries. The newest one is shown.
      consumed.add(d.id)
      if (!best || at > new Date(best.created_at).getTime()) best = d
    }
    return best ? { ...group, diagnostic: best } : group
  })

  if (includeSearchOnly) {
    const leftover = rows.filter((d) => !consumed.has(d.id))
    const byContent = new Map()
    leftover.forEach((d) => {
      const key = diagnosticContentKey(d)
      const list = byContent.get(key) || []
      list.push(d)
      byContent.set(key, list)
    })
    byContent.forEach((contentRows) => {
      const sorted = [...contentRows].sort((a, b) => new Date(b.created_at) - new Date(a.created_at))
      let cluster = []
      const flush = () => {
        if (cluster.length === 0) return
        const newest = cluster[0]
        groups.push({
          kind: 'search',
          key: `search::${newest.id}`,
          contentType: newest.content_type || '',
          contentID: newest.content_id || '',
          streamName: newest.stream_name || 'default',
          title: formatContentTitle(newest.content_title, newest.content_type, newest.content_id),
          attempts: [],
          latest: null,
          requestTime: newest.created_at,
          okCount: 0,
          failedCount: 0,
          preloadCount: 0,
          diagnostic: newest,
          streamCount: diagnosticStreamCount(newest),
          shortCircuit: searchShortCircuit(parseDiagnosticPayload(newest)),
        })
        cluster = []
      }
      sorted.forEach((d) => {
        if (cluster.length === 0) {
          cluster = [d]
          return
        }
        const previous = cluster[cluster.length - 1]
        const gap = Math.abs(new Date(previous.created_at).getTime() - new Date(d.created_at).getTime())
        if (gap <= historyWindowMs) {
          cluster.push(d)
          return
        }
        flush()
        cluster = [d]
      })
      flush()
    })
  }

  return groups.sort((a, b) => new Date(b.requestTime || 0) - new Date(a.requestTime || 0))
}

function parseDiagnosticPayload(diagnostic) {
  if (!diagnostic?.payload) return null
  try {
    return JSON.parse(diagnostic.payload)
  } catch {
    return null
  }
}

function FunnelChip({ label, value, title }) {
  return (
    <div title={title} className="inline-flex items-center gap-1.5 rounded-md border border-border/60 bg-background/95 px-2 py-1 text-xs">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-medium tabular-nums">{value}</span>
    </div>
  )
}

function SearchDiagnosticsPanel({ diagnostic }) {
  const [showRejected, setShowRejected] = useState(false)
  const snap = useMemo(() => parseDiagnosticPayload(diagnostic), [diagnostic])
  if (!snap) return null

  const shortCircuit = searchShortCircuit(snap)
  const validation = Array.isArray(snap.validation) ? snap.validation : []
  const calls = Array.isArray(snap.indexer_calls) ? snap.indexer_calls : []
  const rejected = Array.isArray(snap.rejected) ? snap.rejected : []
  const rawTotal = validation.reduce((sum, v) => sum + (v.raw || 0), 0)
  const validatedTotal = validation.reduce((sum, v) => sum + (v.kept || 0), 0)
  const droppedTitle = validation.reduce((sum, v) => sum + (v.dropped_title || 0), 0)
  const droppedYear = validation.reduce((sum, v) => sum + (v.dropped_year || 0), 0)
  const titleMismatchKept = validation.reduce((sum, v) => sum + (v.title_mismatch_kept || 0), 0)

  return (
    <div className="mb-3 rounded-lg border border-border/60 bg-background/95 px-3 py-3">
      <div className="mb-2 flex items-center justify-between gap-2">
        <div className="flex items-center gap-2 text-sm font-medium">
          <SearchIcon className="size-4 text-muted-foreground" />
          Search
        </div>
        <div className="text-xs text-muted-foreground tabular-nums">{snap.total_ms >= 0 ? `${snap.total_ms} ms` : ''}</div>
      </div>

      <div className="flex flex-wrap gap-1.5">
        {rawTotal > 0 && <FunnelChip label="Raw" value={rawTotal} />}
        {rawTotal > 0 && <FunnelChip label="Validated" value={validatedTotal} />}
        {droppedTitle > 0 && <FunnelChip label="Title mismatch" value={`−${droppedTitle}`} />}
        {/* ID requests do not enforce the title — the indexer was asked by id,
            not by name. The mismatch is still worth seeing: a request that is
            nearly all mismatch is an indexer answering something else. */}
        {titleMismatchKept > 0 && (
          <FunnelChip
            label="Title mismatch (kept)"
            value={titleMismatchKept}
            title="Results whose name did not match the metadata title, kept because the request searched by ID. A high count means the indexer answered an ID search with something else."
          />
        )}
        {droppedYear > 0 && <FunnelChip label="Year mismatch" value={`−${droppedYear}`} />}
        {snap.dedup_input > 0 && <FunnelChip label="Deduped" value={snap.dedup_output} />}
        {/* Duplicates that were merged instead of discarded: they are not in
            the result count, but playback can still fall back to them. */}
        {snap.variants_kept > 0 && <FunnelChip label="Variants kept" value={snap.variants_kept} />}
        {snap.bad_filtered > 0 && <FunnelChip label="Known bad" value={`−${snap.bad_filtered}`} />}
        {shortCircuit && <FunnelChip label={shortCircuit.label} value={shortCircuit.detail} />}
        {/* A short-circuited search never reached the profile, so reporting
            "0 → 0" for it would read as the profile having dropped everything. */}
        {!shortCircuit && snap.profile_name && (
          <FunnelChip label={`Profile (${snap.profile_name})`} value={`${snap.profile_input || 0} → ${snap.profile_kept || 0}`} />
        )}
        {/* Split out because "a trait blocked it" and "a rule you wrote blocked
            it" send you to very different parts of the profile editor. */}
        {!shortCircuit && snap.rules_rejected > 0 && (
          <FunnelChip label="By rules" value={`−${snap.rules_rejected}`} />
        )}
      </div>

      {shortCircuit && (
        <p className="mt-2 text-xs text-muted-foreground">
          {snap.unaired_airs_at
            ? 'No indexer was asked: a trusted source puts this episode’s air date in the future. The gate opens as soon as that date begins anywhere in the world, so a release can never be hidden behind it. Turn off "Skip unaired episodes" in the stream’s indexer settings to search anyway.'
            : 'No indexer was asked: the stream’s metadata profile caps this title by age rating.'}
        </p>
      )}

      {calls.length > 0 && (
        <div className="mt-3 overflow-x-auto">
          <table className="w-full text-xs">
            <thead>
              <tr className="border-b border-border/60 text-left text-muted-foreground">
                <th className="py-1 pr-3 font-medium">Indexer</th>
                <th className="py-1 pr-3 font-medium">Mode</th>
                <th className="py-1 pr-3 text-right font-medium">Time</th>
                <th className="py-1 pr-3 text-right font-medium">Results</th>
                <th className="py-1 font-medium">Status</th>
              </tr>
            </thead>
            <tbody>
              {calls.map((call, index) => (
                <tr key={`${call.indexer}-${call.mode}-${index}`} className="border-b border-border/40 last:border-0">
                  <td className="py-1 pr-3">{call.indexer}</td>
                  <td className="py-1 pr-3 text-muted-foreground">{call.mode || '—'}</td>
                  <td className="py-1 pr-3 text-right tabular-nums">{call.cached ? '⚡' : ''} {call.duration_ms} ms</td>
                  <td className="py-1 pr-3 text-right tabular-nums">{call.results}</td>
                  <td className="py-1">
                    {call.error
                      ? <span className="text-red-500 [overflow-wrap:anywhere]" title={call.error}>{call.error.length > 60 ? `${call.error.slice(0, 60)}…` : call.error}</span>
                      : <span className="text-muted-foreground">{call.cached ? 'cached' : 'ok'}</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {rejected.length > 0 && (
        <div className="mt-3">
          <button
            type="button"
            onClick={() => setShowRejected((current) => !current)}
            className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
          >
            {showRejected ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
            Rejected by profile ({rejected.length})
          </button>
          {showRejected && (
            <div className="mt-2 space-y-1.5">
              {rejected.map((r, index) => (
                <div key={`${r.title}-${index}`} className="rounded-md border border-border/40 bg-muted/20 px-2 py-1.5">
                  <div className="break-words text-xs [overflow-wrap:anywhere]">{r.title || '—'}</div>
                  <div className="mt-1 flex flex-wrap items-center gap-1">
                    {r.indexer && <span className="text-[10px] text-muted-foreground">{r.indexer}</span>}
                    {(r.reasons || []).map((reason) => (
                      <Badge
                        key={reason}
                        variant="outline"
                        className={cn(
                          'px-1.5 py-0 text-[10px] font-normal',
                          reason.startsWith('rule: ')
                            ? 'border-primary/40 text-primary'
                            : 'text-muted-foreground',
                        )}
                      >
                        {reason}
                      </Badge>
                    ))}
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function formatGroupStatus(group) {
  if (group.kind === 'search') {
    if (group.shortCircuit) return group.shortCircuit.label
    if (group.streamCount === 0) return 'No results'
    return 'Searched'
  }
  if (group.okCount > 0) return 'OK'
  if (group.preloadCount > 0 && group.failedCount === 0) return 'Pending'
  if (shortReason(group.latest?.failure_reason) === 'Short play') return 'Short play'
  return 'Failed'
}

function formatAvailStatus(value) {
  if (value === 'sent') return 'Sent'
  if (value === 'skipped') return 'Skipped'
  return '—'
}

function statusTone(group) {
  if (group.kind === 'search') {
    // A short-circuit is expected behaviour, not a warning: nothing went wrong
    // and there is nothing for the user to fix.
    if (group.shortCircuit) return 'secondary'
    return group.streamCount === 0 ? 'warning' : 'secondary'
  }
  if (group.okCount > 0) return 'success'
  if (group.preloadCount > 0 && group.failedCount === 0) return 'secondary'
  if (shortReason(group.latest?.failure_reason) === 'Short play') return 'warning'
  return 'destructive'
}

function groupRequestsByDay(groups) {
  const dayMap = new Map()
  groups.forEach((group) => {
    const dayKey = formatDateOnly(group.requestTime)
    const list = dayMap.get(dayKey) || []
    list.push(group)
    dayMap.set(dayKey, list)
  })
  return Array.from(dayMap.entries()).map(([day, items]) => ({
    day,
    items,
  }))
}

function DetailSection({ title, children }) {
  return (
    <section className="mt-7 space-y-1.5 first:mt-0">
      <div className="px-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">{title}</div>
      <div className="overflow-hidden rounded-xl border border-border/60 bg-muted/20">
        {children}
      </div>
    </section>
  )
}

function DetailRow({ label, value, mono = false, tone = 'default', bordered = false }) {
  return (
    <div className="relative px-4 py-3 sm:px-5">
      {bordered ? <div className="absolute left-4 right-4 top-0 border-t border-border/60 sm:left-5 sm:right-5" /> : null}
      <div
        className={cn(
          'grid grid-cols-[5.5rem_minmax(0,1fr)] gap-3 text-sm',
          'items-center',
          tone === 'danger' && 'sm:grid-cols-[5.5rem_minmax(0,1fr)]'
        )}
      >
        <div className="text-muted-foreground">{label}</div>
        <div
          className={cn(
            'min-w-0 break-words leading-relaxed [overflow-wrap:anywhere]',
            mono && 'rounded-md border border-border/50 bg-background/70 px-3 py-2 font-mono text-xs sm:text-sm',
            tone === 'warning' && 'rounded-md border border-amber-200/70 bg-amber-50/70 px-3 py-2 text-amber-800 dark:border-amber-900/50 dark:bg-amber-950/30 dark:text-amber-200',
            tone === 'danger' && 'rounded-md border border-red-200/70 bg-red-50/70 px-3 py-2 text-red-700 dark:border-red-900/50 dark:bg-red-950/30 dark:text-red-300'
          )}
        >
          {value || '—'}
        </div>
      </div>
    </div>
  )
}

function SummaryCard({ label, value, tone }) {
  return (
    <div className="rounded-lg border border-border/60 bg-muted/30 px-3 py-2">
      <div className="text-xs uppercase tracking-wide text-muted-foreground">{label}</div>
      <div
        className={cn(
          'mt-1 text-lg font-semibold',
          tone === 'success' && 'text-green-600',
          tone === 'destructive' && 'text-destructive'
        )}
      >
        {value}
      </div>
    </div>
  )
}

function formatTimeframeLabel(value) {
  if (value === 'today') return 'Today'
  if (value === '7d') return '7 days'
  if (value === '30d') return '30 days'
  if (value === 'all') return 'All time'
  return value
}
function formatResultFilterLabel(value) {
  if (value === 'ok') return 'OK'
  if (value === 'failed') return 'Failed'
  if (value === 'short_play') return 'Short play'
  if (value === 'preload') return 'Pending'
  return 'All'
}

export const NZBHistoryPage = memo(function NZBHistoryPage({ refreshTrigger }) {
  const [attempts, setAttempts] = useState([])
  const [diagnostics, setDiagnostics] = useState([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState(null)
  const [copyError, setCopyError] = useState(null)
  const [copiedAttemptId, setCopiedAttemptId] = useState(null)
  const [expandedGroupKey, setExpandedGroupKey] = useState(null)
  const [selectedAttemptId, setSelectedAttemptId] = useState(null)
  const [timeframe, setTimeframe] = useState('7d')
  const [resultFilter, setResultFilter] = useState('all')
  const [streamFilter, setStreamFilter] = useState('all')
  const [search, setSearch] = useState('')
  const [filtersDialogOpen, setFiltersDialogOpen] = useState(false)
  const [clearOpen, setClearOpen] = useState(false)
  const [clearing, setClearing] = useState(false)
  const [clearSelection, setClearSelection] = useState(() => new Set())
  const attemptDetailScrollRef = useRef(null)



  const fetchAttempts = useCallback((showLoadingSpinner = true) => {
    if (showLoadingSpinner) setLoading(true)
    else setRefreshing(true)
    setError(null)
    // Diagnostics are decoration on the groups: their failure must never take
    // the history page down, so that fetch swallows its own errors.
    apiFetch('/api/search-diagnostics?limit=200')
      .then((data) => {
        if (Array.isArray(data)) setDiagnostics(data)
      })
      .catch(() => {})
    apiFetch('/api/nzb-attempts?limit=200')
      .then((data) => {
        if (Array.isArray(data)) setAttempts(data)
      })
      .catch((err) => {
        setError(err.message || 'Failed to load NZB history')
      })
      .finally(() => {
        setLoading(false)
        setRefreshing(false)
      })
  }, [])

  useEffect(() => {
    fetchAttempts(true)
  }, [fetchAttempts])

  useEffect(() => {
    if (refreshTrigger == null || refreshTrigger === 0) return
    fetchAttempts(false)
  }, [refreshTrigger, fetchAttempts])

  const streamOptions = useMemo(() => {
    return Array.from(new Set(attempts.map((attempt) => attempt.stream_name || 'default'))).sort((a, b) => a.localeCompare(b))
  }, [attempts])

  const attemptsPerStream = useMemo(() => {
    return attempts.reduce((counts, attempt) => {
      const name = attempt.stream_name || 'default'
      counts[name] = (counts[name] || 0) + 1
      return counts
    }, {})
  }, [attempts])

  const filteredAttempts = useMemo(() => {
    return attempts.filter((attempt) => (
      withinTimeframe(attempt, timeframe) &&
      matchesResult(attempt, resultFilter) &&
      matchesStream(attempt, streamFilter) &&
      matchesSearch(attempt, search)
    ))
  }, [attempts, timeframe, resultFilter, streamFilter, search])

  const filteredDiagnostics = useMemo(() => {
    return diagnostics.filter((d) => {
      const asEvent = { tried_at: d.created_at, stream_name: d.stream_name }
      return (
        withinTimeframe(asEvent, timeframe) &&
        matchesStream(asEvent, streamFilter) &&
        (!search || [d.content_title, d.content_id].filter(Boolean).join(' ').toLowerCase().includes(search.toLowerCase()))
      )
    })
  }, [diagnostics, timeframe, streamFilter, search])

  // Search-only entries carry no attempt to satisfy a result filter, so they
  // surface only on the unfiltered view.
  const requestGroups = useMemo(
    () => buildHistoryTimeline(buildRequestGroups(filteredAttempts), filteredDiagnostics, resultFilter === 'all'),
    [filteredAttempts, filteredDiagnostics, resultFilter]
  )

  const summary = useMemo(() => ({
    requests: requestGroups.length,
    attempts: filteredAttempts.length,
    ok: filteredAttempts.filter((attempt) => !attempt.preload && attempt.success).length,
    failed: filteredAttempts.filter((attempt) => !attempt.preload && !attempt.success && !isShortPlayAttempt(attempt)).length,
    preload: filteredAttempts.filter((attempt) => attempt.preload).length,
  }), [filteredAttempts, requestGroups])

  const toggleGroup = useCallback((key) => {
    setExpandedGroupKey((current) => (current === key ? null : key))
  }, [])

  const requestsByDay = useMemo(() => groupRequestsByDay(requestGroups), [requestGroups])

  const activeFilterChips = useMemo(() => {
    const chips = []
    if (timeframe !== '7d') {
      chips.push({ key: 'timeframe', label: formatTimeframeLabel(timeframe) })
    }
    if (streamFilter !== 'all') {
      chips.push({ key: 'stream', label: streamFilter })
    }
    if (resultFilter !== 'all') {
      chips.push({ key: 'status', label: formatResultFilterLabel(resultFilter) })
    }
    return chips
  }, [timeframe, streamFilter, resultFilter])

  const selectedAttempt = useMemo(
    () => filteredAttempts.find((attempt) => attempt.id === selectedAttemptId) || null,
    [filteredAttempts, selectedAttemptId]
  )

  useEffect(() => {
    if (!selectedAttempt || !attemptDetailScrollRef.current) return
    attemptDetailScrollRef.current.scrollTop = 0
  }, [selectedAttempt])

  const allStreamsSelected = streamOptions.length > 0 && clearSelection.size === streamOptions.length

  // The dialog opens pre-ticked with whatever stream the page is filtered to,
  // so the common "clear what I'm looking at" case stays one click.
  const openClearDialog = useCallback(() => {
    setClearSelection(streamFilter && streamFilter !== 'all' ? new Set([streamFilter]) : new Set())
    setClearOpen(true)
  }, [streamFilter])

  const toggleClearStream = useCallback((streamName) => {
    setClearSelection((prev) => {
      const next = new Set(prev)
      if (next.has(streamName)) next.delete(streamName)
      else next.add(streamName)
      return next
    })
  }, [])

  const toggleAllClearStreams = useCallback(() => {
    setClearSelection((prev) => (prev.size === streamOptions.length ? new Set() : new Set(streamOptions)))
  }, [streamOptions])

  const handleClearConfirm = useCallback(async () => {
    if (clearSelection.size === 0) return
    setClearing(true)
    try {
      // Every stream ticked means "wipe the table", which also takes rows from
      // streams whose attempts are older than the window this page loaded.
      const params = new URLSearchParams()
      if (allStreamsSelected) params.append('stream', 'all')
      else clearSelection.forEach((streamName) => params.append('stream', streamName))

      const data = await apiFetch(`/api/nzb-attempts/clear?${params.toString()}`, { method: 'DELETE' })
      if (data) {
        fetchAttempts(false)
      }
    } catch (err) {
      setError(err.message || 'Failed to clear history')
    } finally {
      setClearing(false)
      setClearOpen(false)
    }
  }, [clearSelection, allStreamsSelected, fetchAttempts])

  const resetFilters = useCallback(() => {
    setTimeframe('7d')
    setStreamFilter('all')
    setResultFilter('all')
  }, [])

  const handleCopyBadMatch = useCallback(async (attempt) => {
    if (!navigator?.clipboard?.writeText) {
      setCopyError('Clipboard access is unavailable in this browser.')
      return
    }
    try {
      await navigator.clipboard.writeText(buildBadMatchReport(attempt))
      setCopyError(null)
      setCopiedAttemptId(attempt.id)
      setTimeout(() => {
        setCopiedAttemptId((current) => (current === attempt.id ? null : current))
      }, 2000)
    } catch {
      setCopyError('Failed to copy bad match details.')
    }
  }, [])

  return (
    <div className={cn('flex min-w-0 flex-1 min-h-0 flex-col gap-4 overflow-x-hidden px-4 py-4 md:gap-6 md:py-6 lg:px-6')}>
      <Card className="flex min-w-0 flex-1 min-h-0 flex-col overflow-hidden">
        <CardHeader>
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0 flex-1 max-w-[42rem] space-y-0.5">
              <CardTitle className="flex items-center gap-2">
                <History className="size-5" />
                History
              </CardTitle>
              <CardDescription>
                Browse recent searches and play attempts grouped by requested movie or episode — including searches nothing was played from. Filters and summary reflect the currently visible set.
              </CardDescription>
            </div>
            <div className="flex shrink-0 items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() => fetchAttempts(false)}
                disabled={refreshing || loading}
              >
                {refreshing ? <Loader2 className="size-4 animate-spin" /> : <RefreshCw className="size-4" />}
                Refresh
              </Button>
              <Button
                variant="destructive"
                size="sm"
                onClick={openClearDialog}
                disabled={clearing || loading || attempts.length === 0}
                title="Choose which streams to clear history for"
              >
                {clearing ? <Loader2 className="size-4 animate-spin" /> : <Eraser className="size-4" />}
                Clear History
              </Button>
            </div>
          </div>
        </CardHeader>
        <CardContent className="flex min-w-0 flex-1 min-h-0 flex-col gap-4 overflow-hidden">
          <div className="grid grid-cols-2 gap-3 rounded-lg border border-border/60 bg-muted/20 p-3 md:grid-cols-4">
            <SummaryCard label="Requests" value={summary.requests} />
            <SummaryCard label="Attempts" value={summary.attempts} />
            <SummaryCard label="OK" value={summary.ok} tone="success" />
            <SummaryCard label="Failed" value={summary.failed} tone="destructive" />
          </div>

          <div className="flex flex-col gap-3 rounded-lg border border-border/60 bg-muted/20 p-3">
            <label className="space-y-1.5">
              <span className="text-xs font-medium uppercase tracking-wide text-muted-foreground">Search</span>
              <div className="flex items-center gap-2">
                <div className="flex h-11 min-w-0 flex-1 items-center gap-2 rounded-lg border border-border/60 bg-background px-3 shadow-xs">
                  <SearchIcon className="size-4 shrink-0 text-muted-foreground" />
                  <div className="relative min-w-0 flex-1">
                    <Input
                      value={search}
                      onChange={(event) => setSearch(event.target.value)}
                      placeholder=""
                      className="h-auto min-w-0 border-0 bg-transparent p-0 shadow-none focus-visible:ring-0"
                    />
                    {!search ? (
                      <span className="pointer-events-none absolute inset-0 overflow-hidden text-ellipsis whitespace-nowrap text-muted-foreground">
                        Request, release, provider, ID or indexer
                      </span>
                    ) : null}
                  </div>
                </div>
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  className="relative h-11 w-11 shrink-0 rounded-lg"
                  onClick={() => setFiltersDialogOpen(true)}
                  aria-label="Open filters"
                >
                  <SlidersHorizontal className="size-4" />
                  {activeFilterChips.length > 0 ? (
                    <span className="absolute -right-1 -top-1 size-3.5 rounded-full border-2 border-background bg-primary shadow-sm" />
                  ) : null}
                </Button>
              </div>
            </label>
            {activeFilterChips.length > 0 ? (
              <div className="flex flex-wrap gap-2">
                {activeFilterChips.map((chip) => (
                  <Badge key={chip.key} variant="secondary" className="rounded-full px-2.5 py-0.5 text-xs font-medium">
                    {chip.label}
                  </Badge>
                ))}
              </div>
            ) : null}
          </div>

          {loading && (
            <div className="flex items-center justify-center gap-2 py-12 text-muted-foreground">
              <Loader2 className="size-5 animate-spin" />
              Loading…
            </div>
          )}
          {error && <div className="px-2 text-destructive">{error}</div>}
          {copyError && !error && <div className="px-2 text-destructive">{copyError}</div>}

          {!loading && !error && (
            requestGroups.length === 0 ? (
              <div className="flex flex-1 min-h-[320px] items-center justify-center rounded-lg border border-dashed px-6 py-8 text-center text-sm text-muted-foreground">
                No matching NZB attempts found for the current filters.
              </div>
            ) : (
              <div className="flex min-w-0 flex-1 min-h-0 flex-col overflow-y-auto rounded-lg border border-border/60 bg-background/40 p-3 md:p-4">
                <div className="flex min-w-0 flex-col gap-5">
                  {requestsByDay.map((section) => (
                    <div key={section.day} className="space-y-3 pb-3 md:pb-4">
                      <div className="rounded-lg border border-border/60 bg-muted/30 px-4 py-3 text-xs font-medium uppercase tracking-wide text-muted-foreground">
                        {section.day}
                      </div>
                      <div className="space-y-3">
                        {section.items.map((group) => {
                          const expanded = expandedGroupKey === group.key
                          return (
                            <div
                              key={group.key}
                              className={cn(
                                'overflow-hidden rounded-xl border bg-background/95 shadow-sm transition-all duration-200 hover:bg-muted/10',
                                expanded
                                  ? 'border-primary/40 ring-1 ring-primary/25 shadow-md bg-primary/[0.03]'
                                  : 'border-border/60'
                              )}
                            >
                              <button
                                type="button"
                                onClick={() => toggleGroup(group.key)}
                                aria-label={`${expanded ? 'Collapse' : 'Expand'} attempts for ${group.title}`}
                                aria-expanded={expanded}
                                className="flex w-full min-w-0 flex-col gap-3 px-4 py-4 text-left md:px-5"
                              >
                                <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3">
                                  <div className="min-w-0">
                                    <div className="inline-flex items-center rounded-md border border-border/60 bg-muted/30 px-2.5 py-0.5 text-xs text-muted-foreground">
                                      {formatTimeOnly(group.requestTime)}
                                    </div>
                                  </div>
                                  <div className="flex items-center gap-2">
                                    <Badge
                                      variant={statusTone(group) === 'destructive' ? 'destructive' : 'secondary'}
                                      className={cn(
                                        'shrink-0',
                                        statusTone(group) === 'success' && 'bg-green-600 text-white hover:bg-green-600 hover:text-white dark:text-black',
                                        statusTone(group) === 'warning' && 'bg-amber-500 text-white hover:bg-amber-500 hover:text-white dark:bg-amber-400 dark:text-black',
                                        statusTone(group) === 'destructive' && 'bg-red-500 text-white hover:bg-red-500 hover:text-white dark:bg-red-500 dark:text-black'
                                      )}
                                    >
                                      {formatGroupStatus(group)}
                                    </Badge>
                                    <span className="text-xs text-muted-foreground">
                                      {group.kind === 'search'
                                        ? (group.streamCount == null ? '—' : `${group.streamCount} streams`)
                                        : group.attempts.length}
                                    </span>
                                    {expanded ? <ChevronDown className="size-4 shrink-0 text-muted-foreground" /> : <ChevronRight className="size-4 shrink-0 text-muted-foreground" />}
                                  </div>
                                </div>
                                <div className="space-y-1.5">
                                  <div className="line-clamp-2 text-base font-semibold leading-snug md:text-lg">
                                    {group.title}
                                  </div>
                                  <div className="text-xs text-muted-foreground [overflow-wrap:anywhere]">
                                    {[group.latest?.stream_name || group.streamName || 'default', formatContentTypeLabel(group.contentType), group.latest?.content_id || group.contentID || '—'].join(' • ')}
                                  </div>
                                </div>
                              </button>

                              {expanded && (
                                <div className="border-t border-border/60 bg-muted/20 px-3 py-3 md:px-5 md:py-4">
                                  <div className="animate-in slide-in-from-top-1 fade-in-0 space-y-2 duration-200">
                                    <SearchDiagnosticsPanel diagnostic={group.diagnostic} />
                                    {group.attempts.length === 0 && (
                                      <div className="rounded-lg border border-dashed border-border/60 px-3 py-3 text-center text-xs text-muted-foreground">
                                        No play attempts followed this search.
                                      </div>
                                    )}
                                    {group.attempts.map((attempt) => {
                                      const reasonLabel = shortReason(attempt.failure_reason)
                                      const attemptBadgeLabel = formatAttemptBadgeLabel(attempt)
                                      return (
                                        <div
                                          key={attempt.id}
                                          className="grid grid-cols-1 gap-3 rounded-lg border border-border/60 bg-background/95 px-3 py-3"
                                        >
                                          <div className="flex items-center justify-between gap-3">
                                            <div className="inline-flex items-center rounded-md border border-border/60 bg-muted/30 px-2.5 py-0.5 text-xs text-muted-foreground">
                                              {formatTimeWithSeconds(attempt.tried_at)}
                                            </div>
                                            <div className="flex items-center gap-2">
                                              <Badge
                                                variant={attempt.success ? 'default' : attempt.preload ? 'secondary' : 'destructive'}
                                                className={attemptBadgeClass(attempt, reasonLabel)}
                                              >
                                                {attemptBadgeLabel}
                                              </Badge>
                                              <TooltipProvider delayDuration={100}>
                                                <Tooltip>
                                                  <TooltipTrigger asChild>
                                                    <button
                                                      type="button"
                                                      onClick={() => setSelectedAttemptId(attempt.id)}
                                                      className="inline-flex items-center justify-center rounded-sm text-muted-foreground hover:text-foreground"
                                                      aria-label="Show attempt details"
                                                    >
                                                      <Info className="size-4" />
                                                    </button>
                                                  </TooltipTrigger>
                                                  <TooltipContent>Details</TooltipContent>
                                                </Tooltip>
                                              </TooltipProvider>
                                            </div>
                                          </div>
                                          <div className="min-w-0 space-y-1">
                                            <div className="line-clamp-3 break-words font-medium leading-snug">
                                              {attempt.release_title || '—'}
                                            </div>
                                            <div className="text-xs text-muted-foreground [overflow-wrap:anywhere]">
                                              {[attempt.indexer_name || '—', attempt.provider_name || '—', formatSize(attempt.release_size), formatMatchType(attempt.match_type), attempt.ttff_ms > 0 ? `⚡ ${attempt.ttff_ms} ms` : ''].filter(Boolean).join(' • ')}
                                            </div>
                                          </div>
                                        </div>
                                      )
                                    })}
                                  </div>
                                </div>
                              )}
                            </div>
                          )
                        })}
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            )
          )}

          <Dialog open={Boolean(selectedAttempt)} onOpenChange={(open) => {
            if (!open) setSelectedAttemptId(null)
          }}>
            <DialogContent
              className="flex max-h-[85vh] max-w-3xl flex-col overflow-hidden"
              onOpenAutoFocus={focusDialogCloseButton}
            >
              {selectedAttempt ? (
                <>
                  <DialogHeader className="space-y-1 pb-1">
                    <DialogTitle className="pr-10 text-left text-lg leading-tight break-words [overflow-wrap:anywhere] sm:text-2xl">
                      {selectedAttempt.release_title || 'Attempt details'}
                    </DialogTitle>
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge variant="outline" className="rounded-md px-2.5 py-1 text-xs font-medium text-muted-foreground">
                        {formatDateTime(selectedAttempt.tried_at)}
                      </Badge>
                      <Badge
                        variant={selectedAttempt.success ? 'default' : selectedAttempt.preload ? 'secondary' : 'destructive'}
                        className={attemptBadgeClass(selectedAttempt, shortReason(selectedAttempt.failure_reason))}
                      >
                        {formatAttemptBadgeLabel(selectedAttempt)}
                      </Badge>
                    </div>
                  </DialogHeader>
                  <div ref={attemptDetailScrollRef} className="min-h-0 flex-1 overflow-y-auto pr-1">
                    <div className="pb-6">
                      <DetailSection title="Content">
                        <div>
                          <DetailRow label="Stream" value={selectedAttempt.stream_name || 'default'} />
                          <DetailRow label="Title" value={selectedAttempt.content_title || '—'} bordered />
                          <DetailRow label="Content ID" value={selectedAttempt.content_id || '—'} bordered />
                          <DetailRow label="Match" value={formatMatchType(selectedAttempt.match_type) || '—'} bordered />
                          <DetailRow label="Size" value={formatSize(selectedAttempt.release_size)} bordered />
                          <DetailRow label="Time to First Frame (TTFF)" value={selectedAttempt.ttff_ms > 0 ? `${selectedAttempt.ttff_ms} ms` : '—'} bordered />
                        </div>
                      </DetailSection>
                      <DetailSection title="Source">
                        <div>
                          <DetailRow label="Indexer" value={selectedAttempt.indexer_name || '—'} />
                          <DetailRow label="Provider" value={selectedAttempt.provider_name || '—'} bordered />
                        </div>
                      </DetailSection>
                      <DetailSection title="File">
                        <div>
                          <DetailRow label="Served file" value={selectedAttempt.served_file || '—'} mono />
                        </div>
                      </DetailSection>
                      <DetailSection title="Debug">
                        <div>
                          <DetailRow
                            label="Reason"
                            value={selectedAttempt.failure_reason || '—'}
                            tone={shortReason(selectedAttempt.failure_reason) === 'Short play' ? 'warning' : selectedAttempt.failure_reason ? 'danger' : 'default'}
                          />
                          <DetailRow label="Slot path" value={selectedAttempt.slot_path || '—'} mono bordered />
                        </div>
                      </DetailSection>
                      <DetailSection title="AvailNZB">
                        <div>
                          <DetailRow label="Report" value={formatAvailStatus(selectedAttempt.avail_status)} />
                          <DetailRow label="Reason" value={selectedAttempt.avail_reason || '—'} bordered />
                        </div>
                      </DetailSection>
                    </div>
                  </div>
                  <DialogFooter className="flex-row flex-wrap items-center justify-end gap-2 pt-1">
                    <TooltipProvider delayDuration={100}>
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Button
                            type="button"
                            variant="outline"
                            size="sm"
                            className="min-w-24"
                            onClick={() => handleCopyBadMatch(selectedAttempt)}
                          >
                            {copiedAttemptId === selectedAttempt.id ? <Check className="size-4" /> : <Copy className="size-4" />}
                            {copiedAttemptId === selectedAttempt.id ? 'Copied' : 'Copy'}
                          </Button>
                        </TooltipTrigger>
                        <TooltipContent>Copy bad match</TooltipContent>
                      </Tooltip>
                    </TooltipProvider>
                    {getSafeReleaseUrl(selectedAttempt.release_url) ? (
                      <TooltipProvider delayDuration={100}>
                        <Tooltip>
                          <TooltipTrigger asChild>
                            <a
                              href={getSafeReleaseUrl(selectedAttempt.release_url)}
                              target="_blank"
                              rel="noopener noreferrer"
                              className="inline-flex min-w-24 items-center justify-center gap-2 rounded-md border border-input bg-background px-3 py-2 text-sm font-medium shadow-xs hover:bg-accent hover:text-accent-foreground"
                            >
                              <ExternalLink className="size-4" />
                              Open
                            </a>
                          </TooltipTrigger>
                          <TooltipContent>Open release</TooltipContent>
                        </Tooltip>
                      </TooltipProvider>
                    ) : null}
                  </DialogFooter>
                </>
              ) : null}
            </DialogContent>
          </Dialog>

            <Dialog open={filtersDialogOpen} onOpenChange={setFiltersDialogOpen}>
            <DialogContent className="w-[calc(100vw-2rem)] max-w-lg rounded-2xl px-5 sm:px-6" onOpenAutoFocus={focusDialogCloseButton}>
              <DialogHeader>
                <DialogTitle className="text-left text-xl">Filters</DialogTitle>
                <DialogDescription className="text-left">
                  Refine the currently visible NZB history entries.
                </DialogDescription>
              </DialogHeader>
              <div className="rounded-md border border-border/60">
                <div className="p-4">
                  <div className="flex items-center justify-between gap-4">
                    <div className="text-sm font-medium">Timeframe</div>
                    <select
                      aria-label="Timeframe"
                      value={timeframe}
                      onChange={(event) => setTimeframe(event.target.value)}
                      className="flex h-9 w-40 rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2"
                    >
                      <option value="today">Today</option>
                      <option value="7d">7 days</option>
                      <option value="30d">30 days</option>
                      <option value="all">All</option>
                    </select>
                  </div>
                </div>
                <div className="relative p-4">
                  <div className="absolute left-4 right-4 top-0 border-t border-border/60" />
                  <div className="flex items-center justify-between gap-4">
                    <div className="text-sm font-medium">Stream</div>
                    <select
                      aria-label="Stream"
                      value={streamFilter}
                      onChange={(event) => setStreamFilter(event.target.value)}
                      className="flex h-9 w-40 rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2"
                    >
                      <option value="all">All streams</option>
                      {streamOptions.map((streamName) => (
                        <option key={streamName} value={streamName}>{streamName}</option>
                      ))}
                    </select>
                  </div>
                </div>
                <div className="relative p-4">
                  <div className="absolute left-4 right-4 top-0 border-t border-border/60" />
                  <div className="flex items-center justify-between gap-4">
                    <div className="text-sm font-medium">Status</div>
                    <select
                      aria-label="Status"
                      value={resultFilter}
                      onChange={(event) => setResultFilter(event.target.value)}
                      className="flex h-9 w-40 rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2"
                    >
                      <option value="all">All</option>
                      <option value="ok">OK</option>
                      <option value="failed">Failed</option>
                      <option value="short_play">Short play</option>
                      <option value="preload">Pending</option>
                    </select>
                  </div>
                </div>
              </div>
              <DialogFooter className="flex-row justify-end gap-2">
                <Button type="button" variant="outline" onClick={resetFilters}>
                  Reset
                </Button>
                <Button type="button" variant="destructive" onClick={() => setFiltersDialogOpen(false)} aria-label="Close filters dialog">
                  Close
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>

          <Dialog open={clearOpen} onOpenChange={(open) => { if (!clearing) setClearOpen(open) }}>
            <DialogContent className="w-[calc(100vw-2rem)] max-w-md rounded-2xl px-5 sm:px-6" onOpenAutoFocus={focusDialogCloseButton}>
              <DialogHeader>
                <DialogTitle>Clear History</DialogTitle>
                <DialogDescription>
                  Pick the streams to clear. Deleting a stream&apos;s history also resets its Continue Watching and Because You Watched rows. Cached releases in the Library are not touched.
                </DialogDescription>
              </DialogHeader>
              <div className="flex flex-col gap-1 rounded-lg border border-border/60 bg-muted/20 p-2">
                <label className="flex cursor-pointer items-center gap-3 rounded-md px-2 py-2 text-sm font-medium hover:bg-muted/40">
                  <Checkbox
                    checked={allStreamsSelected ? true : (clearSelection.size > 0 ? 'indeterminate' : false)}
                    onCheckedChange={toggleAllClearStreams}
                    disabled={clearing}
                  />
                  All streams
                </label>
                <div className="max-h-64 overflow-y-auto">
                  {streamOptions.map((streamName) => (
                    <label
                      key={streamName}
                      className="flex cursor-pointer items-center gap-3 rounded-md px-2 py-2 text-sm hover:bg-muted/40"
                    >
                      <Checkbox
                        checked={clearSelection.has(streamName)}
                        onCheckedChange={() => toggleClearStream(streamName)}
                        disabled={clearing}
                      />
                      <span className="min-w-0 flex-1 truncate" title={streamName}>{streamName}</span>
                      <span className="shrink-0 text-xs text-muted-foreground">
                        {attemptsPerStream[streamName] || 0}
                      </span>
                    </label>
                  ))}
                </div>
              </div>
              <DialogFooter className="flex-row justify-end gap-2">
                <Button type="button" variant="outline" onClick={() => setClearOpen(false)} disabled={clearing}>
                  Cancel
                </Button>
                <Button
                  type="button"
                  variant="destructive"
                  onClick={handleClearConfirm}
                  disabled={clearing || clearSelection.size === 0}
                >
                  {clearing ? <Loader2 className="size-4 animate-spin" /> : null}
                  {allStreamsSelected
                    ? 'Delete All History'
                    : `Delete History (${clearSelection.size})`}
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        </CardContent>
      </Card>
    </div>
  )
})
