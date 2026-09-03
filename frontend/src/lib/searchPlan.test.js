import { describe, expect, it } from 'vitest'

import {
  ADDRESS_ID,
  ADDRESS_TITLE,
  ORDER_ADAPTIVE_SEASON,
  ORDER_AS_LISTED,
  STOP_ALL,
  TARGET_ABSOLUTE,
  TARGET_EPISODE,
  TARGET_SEASON,
  attemptLabel,
  attemptsInRunOrder,
  normalizeAttempt,
  normalizeAttempts,
  normalizeSearchPlan,
  planPresets,
  presetPlan,
} from './searchPlan'

const labels = (attempts, kind) => attempts.map((attempt) => attemptLabel(attempt, kind))

describe('normalizeAttempt', () => {
  it('settles every field, so a draft attempt is the attempt that runs', () => {
    const attempt = normalizeAttempt({ address: 'ID', target: 'SEASON', title: 'de-DE', year: true }, 'series')
    expect(attempt).toEqual({ address: ADDRESS_ID, target: TARGET_SEASON })
  })

  it('drops the target on a movie plan, which has nothing to aim at', () => {
    expect(normalizeAttempt({ address: 'title', target: 'episode' }, 'movie'))
      .toEqual({ address: ADDRESS_TITLE, title: '', year: false })
  })

  it('keeps a title attempt whole', () => {
    expect(normalizeAttempt({ address: 'title', target: 'absolute', title: 'en-US', year: true }, 'series'))
      .toEqual({ address: ADDRESS_TITLE, target: TARGET_ABSOLUTE, title: 'en-US', year: true })
  })

  it('falls back to a title attempt for anything unrecognized', () => {
    expect(normalizeAttempt({ address: 'carrier-pigeon' }, 'movie').address).toBe(ADDRESS_TITLE)
    expect(normalizeAttempt({ address: 'id', target: 'nonsense' }, 'series').target).toBe(TARGET_EPISODE)
  })
})

describe('normalizeAttempts', () => {
  it('drops twins, which would be a wasted round trip rather than a fallback', () => {
    const attempts = normalizeAttempts([
      { address: 'id', target: 'episode' },
      { address: 'ID', target: 'EPISODE' },
      { address: 'title', target: 'episode', title: 'en-US' },
      { address: 'title', target: 'episode', title: 'de-DE' },
    ], 'series')
    expect(labels(attempts, 'series')).toEqual(['ID · Episode', 'Title · Episode', 'Title · Episode'])
    expect(attempts[1].title).toBe('en-US')
    expect(attempts[2].title).toBe('de-DE')
  })
})

describe('attemptsInRunOrder', () => {
  const plan = presetPlan('series', 'balanced')

  it('runs the plan as listed while the season is still airing', () => {
    expect(labels(attemptsInRunOrder(plan.attempts, plan.order, 'series', false), 'series')).toEqual([
      'ID · Episode',
      'Title · Absolute',
      'Title · Episode',
      'ID · Season',
      'Title · Season',
    ])
  })

  it('leads with the season attempts once the season has aired', () => {
    expect(labels(attemptsInRunOrder(plan.attempts, plan.order, 'series', true), 'series')).toEqual([
      'ID · Season',
      'Title · Season',
      'ID · Episode',
      'Title · Absolute',
      'Title · Episode',
    ])
  })

  it('never reorders a plan that did not ask for it', () => {
    const asListed = labels(attemptsInRunOrder(plan.attempts, ORDER_AS_LISTED, 'series', true), 'series')
    expect(asListed[0]).toBe('ID · Episode')
  })
})

describe('planPresets', () => {
  it('offers the same three per kind, narrowest first', () => {
    for (const kind of ['movie', 'series']) {
      expect(planPresets(kind).map((preset) => preset.id)).toEqual(['balanced', 'precise', 'broad'])
    }
  })

  it('matches the stock plans the backend seeds', () => {
    const tv = presetPlan('series', 'balanced')
    expect(labels(tv.attempts, 'series')).toEqual([
      'ID · Episode',
      'Title · Absolute',
      'Title · Episode',
      'ID · Season',
      'Title · Season',
    ])
    expect(tv.order).toBe(ORDER_ADAPTIVE_SEASON)
    expect(tv.accept).toEqual({ titles: ['en-US', ''], year: false, packs: true })

    const movie = presetPlan('movie', 'balanced')
    expect(labels(movie.attempts, 'movie')).toEqual(['ID', 'Title'])
    expect(movie.accept).toEqual({ titles: ['en-US', ''], year: true })
  })

  it('runs everything and merges on the broad preset', () => {
    expect(presetPlan('movie', 'broad').stop).toBe(STOP_ALL)
  })
})

describe('normalizeSearchPlan', () => {
  it('keeps a series plan whole and drops the series-only fields from a movie plan', () => {
    const plan = normalizeSearchPlan('series', {
      name: '  TVPlan ',
      attempts: [{ address: 'id', target: 'episode' }],
      stop: 'nonsense',
      order: 'adaptive_season',
      accept: { titles: ['en-US'], year: true, packs: false },
      search_result_limit: '25',
      categories: ' 5070 ',
    })
    expect(plan).toEqual({
      name: 'TVPlan',
      attempts: [{ address: ADDRESS_ID, target: TARGET_EPISODE }],
      stop: 'first_hit',
      order: ORDER_ADAPTIVE_SEASON,
      accept: { titles: ['en-US'], year: true, packs: false },
      search_result_limit: 25,
      categories: '5070',
    })

    const movie = normalizeSearchPlan('movie', { name: 'M', attempts: [{ address: 'id' }] })
    expect(movie.order).toBeUndefined()
    expect(movie.accept.packs).toBeUndefined()
    expect(movie.categories).toBe('')
  })

  it('accepts nothing implicitly: an empty title list stays empty', () => {
    expect(normalizeSearchPlan('movie', { attempts: [{ address: 'id' }] }).accept.titles).toEqual([])
  })
})
