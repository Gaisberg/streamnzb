import { describe, it, expect } from 'vitest'

import { parseDiagnosticPayload, droppedBadges, turnedAwayReleases } from './searchDiagnostics'

const diagnostic = (snap) => ({ payload: JSON.stringify(snap) })

describe('parseDiagnosticPayload', () => {
  // A row whose payload did not survive a round trip must not take the page
  // down with it: the rest of the history is still readable.
  it('returns null for a missing or unparseable payload', () => {
    expect(parseDiagnosticPayload(undefined)).toBeNull()
    expect(parseDiagnosticPayload({})).toBeNull()
    expect(parseDiagnosticPayload({ payload: '{not json' })).toBeNull()
  })

  it('parses a payload', () => {
    expect(parseDiagnosticPayload(diagnostic({ total_ms: 12 }))).toEqual({ total_ms: 12 })
  })
})

describe('turnedAwayReleases', () => {
  it('has nothing to show for a snapshot that recorded nothing', () => {
    expect(turnedAwayReleases(undefined)).toEqual({ releases: [], omitted: 0 })
    expect(turnedAwayReleases(diagnostic({ total_ms: 1 }))).toEqual({ releases: [], omitted: 0 })
  })

  // Drops happen before the profile, so they come first: the list reads in the
  // order the funnel applied the stages.
  it('lists pre-profile drops before profile rejections', () => {
    const { releases } = turnedAwayReleases(diagnostic({
      dropped: [{ title: 'Wrong.Year.2004', stage: 'validation', reason: 'year' }],
      rejected: [{ title: 'Oversized.2160p', reasons: ['rule: Oversized'] }],
    }))
    expect(releases.map((r) => r.title)).toEqual(['Wrong.Year.2004', 'Oversized.2160p'])
  })

  // The indexer and the query the release was answering are what turn "a title
  // mismatch" into "this indexer answered that query with this".
  it('carries the indexer and request as the row meta', () => {
    const { releases } = turnedAwayReleases(diagnostic({
      dropped: [{ title: 'X', stage: 'validation', reason: 'title', indexer: 'NZBGeek', request: 'ted lasso S01E01' }],
      rejected: [{ title: 'Y', indexer: 'DrunkenSlug', reasons: ['trash'] }],
    }))
    expect(releases[0].meta).toEqual(['NZBGeek', 'ted lasso S01E01'])
    expect(releases[1].meta).toEqual(['DrunkenSlug'])
  })

  // A rule the user wrote and a built-in trait send you to different parts of
  // the profile editor, so only the rule is highlighted.
  it('highlights a rule rejection and not a trait one', () => {
    const { releases } = turnedAwayReleases(diagnostic({
      rejected: [{ title: 'X', reasons: ['rule: Oversized', 'resolution:480p'] }],
    }))
    expect(releases[0].badges).toEqual([
      { key: 'rule: Oversized', label: 'rule: Oversized', highlight: true },
      { key: 'resolution:480p', label: 'resolution:480p', highlight: false },
    ])
  })

  it('reports how many drops the backend cap left unrecorded', () => {
    const { omitted } = turnedAwayReleases(diagnostic({
      dropped: [{ title: 'X', stage: 'validation' }],
      dropped_omitted: 118,
    }))
    expect(omitted).toBe(118)
  })

  // Two releases sharing a title — the same name from two indexers — must not
  // collide, or React drops one of the rows.
  it('gives same-titled releases distinct keys', () => {
    const { releases } = turnedAwayReleases(diagnostic({
      dropped: [
        { title: 'Same.Name', stage: 'validation', indexer: 'A' },
        { title: 'Same.Name', stage: 'validation', indexer: 'B' },
      ],
      rejected: [{ title: 'Same.Name', reasons: ['trash'] }],
    }))
    const keys = releases.map((r) => r.key)
    expect(new Set(keys).size).toBe(keys.length)
  })
})

describe('droppedBadges', () => {
  // The stage decides where to look next, so it always leads and is always
  // present — even for a snapshot written before the stage had a label.
  it('always leads with the stage', () => {
    expect(droppedBadges({ stage: 'validation' })[0]).toEqual({ key: 'stage', label: 'validation', highlight: true })
    expect(droppedBadges({ stage: 'bad' })[0]).toEqual({ key: 'stage', label: 'known bad', highlight: true })
    expect(droppedBadges({})[0]).toEqual({ key: 'stage', label: 'dropped', highlight: true })
  })

  it('adds the reason, the detail and the request when present', () => {
    expect(droppedBadges({
      stage: 'validation',
      reason: 'title',
      detail: 'expected "lioness", got "Special Ops Lioness"',
      request: 'lioness S01E01',
    }).map((b) => b.label)).toEqual([
      'validation',
      'title',
      'expected "lioness", got "Special Ops Lioness"',
      'lioness S01E01',
    ])
  })

  // The known-bad filter has only one way to fail, so it carries no reason and
  // must not render an empty badge.
  it('omits what a stage does not carry', () => {
    expect(droppedBadges({ stage: 'bad', detail: 'every copy of it has failed playback before' }).map((b) => b.key))
      .toEqual(['stage', 'detail'])
  })
})
