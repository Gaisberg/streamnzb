// Statistics page helpers: the date-range presets, turning /api/stats/history
// rows into display rows with their derived rates, and the per-tab summaries.
// Kept free of React so the arithmetic is testable on its own.

const DAY_MS = 24 * 60 * 60 * 1000

export const RANGE_PRESETS = [
  { id: '24h', label: 'Last 24 Hours', short: '24H', description: 'Past 24 hours of snapshots' },
  { id: '7d', label: 'Last 7 Days', short: '7D', description: 'Past 7 days of snapshots' },
  { id: '30d', label: 'Last 30 Days', short: '30D', description: 'Past 30 days of snapshots' },
  { id: '90d', label: 'Last 90 Days', short: '90D', description: 'Past quarter of snapshots' },
  { id: 'mtd', label: 'Month to Date', short: 'MTD', description: 'From 1st of this month' },
  { id: 'all', label: 'All Time', short: 'All', description: 'Complete recorded history' },
]

export function toNumber(value, fallback = 0) {
  const n = Number(value)
  return Number.isFinite(n) ? n : fallback
}

// pick reads a field the backend may send snake_case or PascalCase.
export function pick(obj, snakeKey, pascalKey, fallback = undefined) {
  if (!obj || typeof obj !== 'object') return fallback
  if (obj[snakeKey] != null) return obj[snakeKey]
  if (obj[pascalKey] != null) return obj[pascalKey]
  return fallback
}

export function formatDateInput(date) {
  const y = date.getFullYear()
  const m = String(date.getMonth() + 1).padStart(2, '0')
  const d = String(date.getDate()).padStart(2, '0')
  return `${y}-${m}-${d}`
}

export function formatDisplayDate(dateStr) {
  if (!dateStr) return ''
  const parts = dateStr.split('-').map(Number)
  if (parts.length < 3) return dateStr
  const [y, m, d] = parts
  return new Date(y, m - 1, d).toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })
}

export function defaultDateRange(now = new Date()) {
  const start = new Date(now)
  start.setDate(now.getDate() - 30)
  return { from: formatDateInput(start), to: formatDateInput(now) }
}

export function rangeFromPreset(preset, now = new Date()) {
  const todayStr = formatDateInput(now)
  if (preset === 'all') return { from: '', to: '' }
  if (preset === '24h') {
    // Full timestamp instead of a calendar date so the backend applies a
    // rolling 24-hour window; empty "to" keeps the range open through now.
    const start = new Date(now.getTime() - DAY_MS)
    return { from: start.toISOString(), to: '' }
  }
  if (preset === 'mtd') {
    const start = new Date(now.getFullYear(), now.getMonth(), 1)
    return { from: formatDateInput(start), to: todayStr }
  }
  const days = { '7d': 7, '30d': 30, '90d': 90 }[preset] || 30
  const start = new Date(now)
  start.setDate(now.getDate() - days)
  return { from: formatDateInput(start), to: todayStr }
}

function parseRangeBound(value, endOfDay) {
  if (!value) return null
  if (/^\d{4}-\d{2}-\d{2}$/.test(value)) {
    const [y, m, d] = value.split('-').map(Number)
    return new Date(y, m - 1, endOfDay ? d + 1 : d)
  }
  const t = new Date(value)
  return Number.isNaN(t.getTime()) ? null : t
}

// rangeDays is how many days the range spans, mirroring how the backend reads
// it (a date-only "to" includes that whole day, and the range never runs past
// now). Null for an open start, where the span is unknown.
export function rangeDays(range, now = new Date()) {
  const from = parseRangeBound(range?.from, false)
  if (!from) return null
  let to = parseRangeBound(range?.to, true) || now
  if (to > now) to = now
  const days = (to.getTime() - from.getTime()) / DAY_MS
  return days > 0 ? days : null
}

function ratio(part, whole) {
  return whole > 0 ? (part / whole) * 100 : null
}

export function indexerRow(raw, { days = null } = {}) {
  const name = String(pick(raw, 'indexer_name', 'IndexerName', '') || '').trim()
  if (!name) return null
  const apiHits = toNumber(pick(raw, 'api_hits_used', 'APIHitsUsed'))
  const apiLimit = toNumber(pick(raw, 'api_hits_limit', 'APIHitsLimit'))
  const grabSuccess = toNumber(pick(raw, 'grab_success_count', 'GrabSuccessCount'))
  const grabFailure = toNumber(pick(raw, 'grab_failure_count', 'GrabFailureCount'))
  const availAvailable = toNumber(pick(raw, 'avail_available_count', 'AvailAvailableCount'))
  const availDiscarded = toNumber(pick(raw, 'avail_discarded_count', 'AvailDiscardedCount'))
  // Limits are per day, so quota use is the average day's hits against it.
  // Ranges under a day still compare against a whole day.
  const quotaPercent = apiLimit > 0 && days != null ? ratio(apiHits / Math.max(days, 1), apiLimit) : null
  return {
    name,
    searches: toNumber(pick(raw, 'searches_count', 'SearchesCount')),
    apiHits,
    apiLimit,
    quotaPercent,
    grabs: toNumber(pick(raw, 'downloads_used', 'DownloadsUsed')),
    grabSuccess,
    grabFailure,
    grabSamples: grabSuccess + grabFailure,
    grabSuccessPercent: ratio(grabSuccess, grabSuccess + grabFailure),
    uniqueHits: toNumber(pick(raw, 'unique_hits_count', 'UniqueHitsCount')),
    uniquePlays: toNumber(pick(raw, 'unique_success_count', 'UniqueSuccessCount')),
    avgResponseMs: toNumber(pick(raw, 'avg_response_ms', 'AvgResponseMS')) || null,
    avgGrabMs: toNumber(pick(raw, 'avg_grab_ms', 'AvgGrabMS')) || null,
    availAvailable,
    availDiscarded,
    availSamples: availAvailable + availDiscarded,
    availabilityPercent: ratio(availAvailable, availAvailable + availDiscarded),
  }
}

export function providerRows(rawList) {
  const rows = (Array.isArray(rawList) ? rawList : [])
    .map((raw) => {
      const name = String(pick(raw, 'provider_name', 'ProviderName', '') || '').trim()
      if (!name) return null
      const available = toNumber(pick(raw, 'article_available_count', 'ArticleAvailableCount'))
      const missing = toNumber(pick(raw, 'article_missing_count', 'ArticleMissingCount'))
      return {
        name,
        host: String(pick(raw, 'host', 'Host', '') || ''),
        downloadedMb: toNumber(pick(raw, 'downloaded_mb', 'DownloadedMB')),
        peakMbps: toNumber(pick(raw, 'current_speed_mbps', 'CurrentSpeedMbps')),
        maxConns: toNumber(pick(raw, 'max_conns', 'MaxConns')),
        articlesAvailable: available,
        articlesMissing: missing,
        articles: available + missing,
        missingPercent: ratio(missing, available + missing),
      }
    })
    .filter(Boolean)
  const total = rows.reduce((sum, r) => sum + r.downloadedMb, 0)
  return rows.map((r) => ({ ...r, sharePercent: total > 0 ? (r.downloadedMb / total) * 100 : 0 }))
}

export function indexerRows(rawList, opts) {
  return (Array.isArray(rawList) ? rawList : []).map((raw) => indexerRow(raw, opts)).filter(Boolean)
}

const sum = (rows, key) => rows.reduce((acc, r) => acc + (r[key] || 0), 0)

export function summarizeIndexers(rows) {
  const searches = sum(rows, 'searches')
  const grabSuccess = sum(rows, 'grabSuccess')
  const grabFailure = sum(rows, 'grabFailure')
  // Weight each indexer's mean response by its searches so a rarely used slow
  // indexer does not drag the overall figure.
  const timed = rows.filter((r) => r.avgResponseMs)
  const weight = timed.reduce((acc, r) => acc + (r.searches || 1), 0)
  const avgResponseMs = timed.length
    ? timed.reduce((acc, r) => acc + r.avgResponseMs * (r.searches || 1), 0) / weight
    : null
  return {
    count: rows.length,
    active: rows.filter((r) => r.searches > 0 || r.apiHits > 0 || r.grabs > 0).length,
    searches,
    apiHits: sum(rows, 'apiHits'),
    grabs: sum(rows, 'grabs'),
    grabSuccess,
    grabFailure,
    grabSuccessPercent: ratio(grabSuccess, grabSuccess + grabFailure),
    uniqueHits: sum(rows, 'uniqueHits'),
    uniquePlays: sum(rows, 'uniquePlays'),
    avgResponseMs,
  }
}

export function summarizeProviders(rows) {
  const available = sum(rows, 'articlesAvailable')
  const missing = sum(rows, 'articlesMissing')
  const top = rows.reduce((best, r) => (!best || r.downloadedMb > best.downloadedMb ? r : best), null)
  return {
    count: rows.length,
    active: rows.filter((r) => r.downloadedMb > 0).length,
    downloadedMb: sum(rows, 'downloadedMb'),
    peakMbps: rows.reduce((max, r) => Math.max(max, r.peakMbps), 0),
    articles: available + missing,
    articlesMissing: missing,
    availabilityPercent: ratio(available, available + missing),
    top: top && top.downloadedMb > 0 ? top : null,
  }
}

// sortRows orders rows by one column. Missing values (null) always sink to
// the bottom whichever way the column is sorted, and ties fall back to name.
export function sortRows(rows, key, direction = 'desc') {
  const dir = direction === 'asc' ? 1 : -1
  return [...rows].sort((a, b) => {
    const av = a[key]
    const bv = b[key]
    const aMissing = av == null
    const bMissing = bv == null
    if (aMissing || bMissing) {
      if (aMissing && bMissing) return a.name.localeCompare(b.name)
      return aMissing ? 1 : -1
    }
    if (typeof av === 'string' || typeof bv === 'string') {
      return String(av).localeCompare(String(bv)) * dir
    }
    if (av === bv) return a.name.localeCompare(b.name)
    return (av - bv) * dir
  })
}

export function formatMb(mb) {
  const n = toNumber(mb)
  if (n >= 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(2)} TB`
  if (n >= 1024) return `${(n / 1024).toFixed(2)} GB`
  return `${n.toFixed(1)} MB`
}

export function formatPercent(value, digits = 1) {
  return value == null ? '—' : `${value.toFixed(digits)}%`
}

export function formatMs(value) {
  if (value == null) return '—'
  if (value >= 10000) return `${(value / 1000).toFixed(1)} s`
  return `${Math.round(value)} ms`
}

export function formatCount(value) {
  return toNumber(value).toLocaleString()
}
