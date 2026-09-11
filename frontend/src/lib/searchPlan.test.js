import { describe, expect, it } from 'vitest'

import {
  ADDRESS_ID,
  ADDRESS_TITLE,
  ORDER_ADAPTIVE_SEASON,
  ORDER_AS_LISTED,
  STOP_ALL,
  STOP_ENOUGH_HITS,
  STOP_FIRST_HIT,
  TARGET_ABSOLUTE,
  TARGET_EPISODE,
  TARGET_SEASON,
  attemptLabel,
  attemptsInRunOrder,
  duplicateAttemptSources,
  nextAttempt,
  normalizeAttempt,
  normalizeAttempts,
  normalizeMinHits,
  normalizeSearchPlan,
  normalizeStop,
  planPresets,
  presetPlan,
  settleAttempts,
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

describe('settleAttempts', () => {
  it('keeps a row that repeats another, which the editor has to be able to show', () => {
    const attempts = settleAttempts([
      { address: 'id', target: 'episode' },
      { address: 'ID', target: 'EPISODE' },
    ], 'series')
    expect(labels(attempts, 'series')).toEqual(['ID · Episode', 'ID · Episode'])
  })
})

describe('duplicateAttemptSources', () => {
  it('points each repeat at the row it repeats', () => {
    const duplicates = duplicateAttemptSources([
      { address: 'id', target: 'episode' },
      { address: 'title', target: 'episode', title: 'en-US' },
      { address: 'ID', target: 'EPISODE' },
      { address: 'title', target: 'episode', title: 'de-DE' },
    ], 'series')
    expect([...duplicates.entries()]).toEqual([[2, 0]])
  })

  it('reads an id attempt without its title language, as the executor does', () => {
    const duplicates = duplicateAttemptSources([
      { address: 'id', target: 'season', title: 'en-US' },
      { address: 'id', target: 'season', title: 'de-DE', year: true },
    ], 'series')
    expect([...duplicates.entries()]).toEqual([[1, 0]])
  })
})

describe('nextAttempt', () => {
  // The bug this answers: adding an attempt the plan already asks looked like
  // the button doing nothing at all, because the new row was deduped away.
  it('adds a question the plan does not already ask', () => {
    const plan = presetPlan('series', 'balanced')
    expect(attemptLabel(nextAttempt(plan.attempts, 'series'), 'series')).toBe('ID · Series')
  })

  it('starts from the narrowest id question on an empty plan', () => {
    expect(attemptLabel(nextAttempt([], 'series'), 'series')).toBe('ID · Episode')
    expect(attemptLabel(nextAttempt([], 'movie'), 'movie')).toBe('ID')
  })

  it('never offers the absolute number to an id request, which cannot ask under it', () => {
    const asked = [
      { address: ADDRESS_ID, target: TARGET_EPISODE },
      { address: ADDRESS_TITLE, target: TARGET_EPISODE, title: 'en-US' },
      { address: ADDRESS_ID, target: TARGET_SEASON },
      { address: ADDRESS_TITLE, target: TARGET_SEASON, title: 'en-US' },
      { address: ADDRESS_ID, target: 'series' },
      { address: ADDRESS_TITLE, target: 'series', title: 'en-US' },
      { address: ADDRESS_TITLE, target: TARGET_ABSOLUTE, title: 'en-US' },
    ]
    expect(attemptLabel(nextAttempt(asked, 'series'), 'series')).toBe('ID · Episode')
  })

  it('falls back to the movie title attempt once the id one is taken', () => {
    expect(attemptLabel(nextAttempt([{ address: ADDRESS_ID }], 'movie'), 'movie')).toBe('Title')
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
      min_hits: 7,
    })
    expect(plan).toEqual({
      name: 'TVPlan',
      attempts: [{ address: ADDRESS_ID, target: TARGET_EPISODE }],
      stop: 'first_hit',
      min_hits: 0,
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

  it('keeps the threshold only on a plan that stops after enough hits', () => {
    const plan = normalizeSearchPlan('movie', { attempts: [{ address: 'id' }], stop: 'enough_hits', min_hits: '12' })
    expect(plan.stop).toBe(STOP_ENOUGH_HITS)
    expect(plan.min_hits).toBe(12)
    expect(normalizeSearchPlan('movie', { attempts: [{ address: 'id' }], stop: 'all', min_hits: 12 }).min_hits).toBe(0)
  })
})

describe('normalizeStop', () => {
  it('knows the three stop rules and falls back to the first hit', () => {
    expect(normalizeStop(' Enough_Hits ')).toBe(STOP_ENOUGH_HITS)
    expect(normalizeStop('all')).toBe(STOP_ALL)
    expect(normalizeStop('sometimes')).toBe(STOP_FIRST_HIT)
  })
})

describe('normalizeMinHits', () => {
  it('settles the threshold to a whole number of at least one, or the default', () => {
    expect(normalizeMinHits(STOP_ENOUGH_HITS, 4.9)).toBe(4)
    expect(normalizeMinHits(STOP_ENOUGH_HITS, 0)).toBe(10)
    expect(normalizeMinHits(STOP_ENOUGH_HITS, 'lots')).toBe(10)
    expect(normalizeMinHits(STOP_ENOUGH_HITS, undefined)).toBe(10)
  })

  it('is nothing for the stop rules that have no threshold', () => {
    expect(normalizeMinHits(STOP_FIRST_HIT, 12)).toBe(0)
    expect(normalizeMinHits(STOP_ALL, 12)).toBe(0)
  })
})
