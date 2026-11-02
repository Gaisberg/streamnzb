import { describe, expect, it } from 'vitest'

import {
  formatMb,
  indexerRow,
  providerRows,
  rangeDays,
  rangeFromPreset,
  sortRows,
  summarizeIndexers,
  summarizeProviders,
} from './statistics'

describe('indexer rows', () => {
  it('derives rates and leaves them empty without samples', () => {
    const row = indexerRow({
      indexer_name: 'NZBGeek',
      searches_count: 40,
      grab_success_count: 9,
      grab_failure_count: 1,
      avg_response_ms: 0,
    })
    expect(row.grabSuccessPercent).toBe(90)
    expect(row.availabilityPercent).toBeNull()
    // 0 means "no sample" for the averages, not "instant".
    expect(row.avgResponseMs).toBeNull()
  })

  it('reads PascalCase fields too', () => {
    expect(indexerRow({ IndexerName: 'Slug', SearchesCount: 3 }).searches).toBe(3)
  })

  it('drops unnamed rows', () => {
    expect(indexerRow({ indexer_name: '  ' })).toBeNull()
  })

  it('compares the average day against the daily limit', () => {
    const raw = { indexer_name: 'A', api_hits_used: 700, api_hits_limit: 100 }
    expect(indexerRow(raw, { days: 7 }).quotaPercent).toBe(100)
    // A range shorter than a day still compares against a whole day.
    expect(indexerRow(raw, { days: 0.5 }).quotaPercent).toBe(700)
    // Unknown span (all time) or no limit: nothing to compare against.
    expect(indexerRow(raw, { days: null }).quotaPercent).toBeNull()
    expect(indexerRow({ ...raw, api_hits_limit: 0 }, { days: 7 }).quotaPercent).toBeNull()
  })
})

describe('provider rows', () => {
  it('computes traffic share and missing article rate', () => {
    const rows = providerRows([
      { provider_name: 'A', downloaded_mb: 300, article_available_count: 98, article_missing_count: 2 },
      { provider_name: 'B', downloaded_mb: 100 },
    ])
    expect(rows.map((r) => r.sharePercent)).toEqual([75, 25])
    expect(rows[0].missingPercent).toBe(2)
    expect(rows[1].missingPercent).toBeNull()
  })
})

describe('summaries', () => {
  it('weights the indexer response time by searches', () => {
    const s = summarizeIndexers([
      indexerRow({ indexer_name: 'fast', searches_count: 90, avg_response_ms: 100 }),
      indexerRow({ indexer_name: 'slow', searches_count: 10, avg_response_ms: 1100 }),
      indexerRow({ indexer_name: 'idle' }),
    ])
    expect(s.avgResponseMs).toBe(200)
    expect(s.active).toBe(2)
    expect(s.count).toBe(3)
  })

  it('totals provider traffic and article availability', () => {
    const s = summarizeProviders(providerRows([
      { provider_name: 'A', downloaded_mb: 300, article_available_count: 90, article_missing_count: 10, current_speed_mbps: 400 },
      { provider_name: 'B', downloaded_mb: 0, current_speed_mbps: 900 },
    ]))
    expect(s.downloadedMb).toBe(300)
    expect(s.availabilityPercent).toBe(90)
    expect(s.peakMbps).toBe(900)
    expect(s.active).toBe(1)
    expect(s.top.name).toBe('A')
  })
})

describe('sortRows', () => {
  const rows = [
    { name: 'b', v: 2 },
    { name: 'a', v: null },
    { name: 'c', v: 5 },
  ]
  it('sorts both ways and keeps missing values last', () => {
    expect(sortRows(rows, 'v', 'desc').map((r) => r.name)).toEqual(['c', 'b', 'a'])
    expect(sortRows(rows, 'v', 'asc').map((r) => r.name)).toEqual(['b', 'c', 'a'])
  })
  it('sorts names alphabetically', () => {
    expect(sortRows(rows, 'name', 'asc').map((r) => r.name)).toEqual(['a', 'b', 'c'])
  })
})

describe('ranges', () => {
  const now = new Date(2026, 10, 2, 12, 0, 0)
  it('measures preset spans', () => {
    expect(rangeDays(rangeFromPreset('24h', now), now)).toBeCloseTo(1)
    // Date-only bounds: from midnight 7 days ago through now.
    expect(rangeDays(rangeFromPreset('7d', now), now)).toBeCloseTo(7.5)
    expect(rangeDays(rangeFromPreset('all', now), now)).toBeNull()
  })
  it('counts a date-only end as the whole day', () => {
    expect(rangeDays({ from: '2026-10-01', to: '2026-10-03' }, now)).toBe(3)
  })
})

describe('formatMb', () => {
  it('scales to GB and TB', () => {
    expect(formatMb(512)).toBe('512.0 MB')
    expect(formatMb(2048)).toBe('2.00 GB')
    expect(formatMb(1.5 * 1024 * 1024)).toBe('1.50 TB')
  })
})
