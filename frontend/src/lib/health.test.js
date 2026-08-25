import { describe, expect, it } from 'vitest'

import { formatSince, healthFor, healthReasonHint, healthReasonLabel, indexHealth, isBlocked } from './health'

describe('component health helpers', () => {
  it('labels every reason the backend can send', () => {
    expect(healthReasonLabel('auth_failed')).toBe('Credentials rejected')
    expect(healthReasonLabel('quota_exhausted')).toBe('Quota spent')
    expect(healthReasonLabel('throttled')).toBe('Rate limited')
    expect(healthReasonLabel('connection_limit')).toBe('Connection limit hit')
    // An unknown code still has to render as something honest.
    expect(healthReasonLabel('something_new')).toBe('Not working')
    expect(healthReasonHint('something_new')).toBe('')
  })

  it('treats only the blocked state as needing a human', () => {
    expect(isBlocked({ state: 'blocked' })).toBe(true)
    expect(isBlocked({ state: 'degraded' })).toBe(false)
    expect(isBlocked(null)).toBe(false)
  })

  it('indexes records by kind and name', () => {
    const map = indexHealth([
      { kind: 'indexer', name: 'nzbfinder', state: 'blocked' },
      { kind: 'provider', name: 'nzbfinder', state: 'degraded' },
      { name: 'no-kind' },
    ])
    // Same name, different kind: these must not collide.
    expect(healthFor(map, 'indexer', 'nzbfinder').state).toBe('blocked')
    expect(healthFor(map, 'provider', 'nzbfinder').state).toBe('degraded')
    expect(healthFor(map, 'indexer', 'missing')).toBeNull()
    expect(map.size).toBe(2)
  })

  it('survives a missing or malformed component list', () => {
    expect(indexHealth(undefined).size).toBe(0)
    expect(healthFor(null, 'indexer', 'x')).toBeNull()
    expect(healthFor(indexHealth([]), 'indexer', '')).toBeNull()
  })

  it('formats how long a component has been unhealthy', () => {
    const ago = (ms) => new Date(Date.now() - ms).toISOString()
    expect(formatSince(ago(5 * 1000))).toBe('just now')
    expect(formatSince(ago(10 * 60 * 1000))).toBe('for 10m')
    expect(formatSince(ago(3 * 60 * 60 * 1000))).toBe('for 3h')
    expect(formatSince(ago(50 * 60 * 60 * 1000))).toBe('for 2d')
    expect(formatSince('')).toBe('')
    expect(formatSince('not a date')).toBe('')
  })
})
