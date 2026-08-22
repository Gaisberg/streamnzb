import { describe, expect, it } from 'vitest'
import {
  activeProviderNames,
  applyFilterSortingMode,
  buildIndexerOverrides,
  defaultAddonName,
  mapStreamsByUsername,
  nextStreamName,
  streamsFromMap,
  tabHasError,
  uniquePreserveOrder,
} from '@/lib/streams'

describe('uniquePreserveOrder', () => {
  it('drops duplicates and empties while keeping the order the user chose', () => {
    expect(uniquePreserveOrder(['b', 'a', 'b', '', 'c', null])).toEqual(['b', 'a', 'c'])
  })

  it('tolerates anything that is not an array', () => {
    expect(uniquePreserveOrder(undefined)).toEqual([])
    expect(uniquePreserveOrder('nope')).toEqual([])
  })
})

describe('activeProviderNames', () => {
  it('counts what the stream actually uses, not what is merely listed', () => {
    const stream = {
      provider_selections: ['Eweka', 'Newshosting', 'Frugal'],
      disabled_providers: ['Newshosting'],
    }
    expect(activeProviderNames(stream)).toEqual(['Eweka', 'Frugal'])
  })

  it('is empty for a stream with no selections', () => {
    expect(activeProviderNames({})).toEqual([])
    expect(activeProviderNames(undefined)).toEqual([])
  })
})

describe('defaultAddonName', () => {
  it('names the stream, falling back to the service on its own', () => {
    expect(defaultAddonName('Living Room')).toBe('StreamNZB · Living Room')
    expect(defaultAddonName('   ')).toBe('StreamNZB')
    expect(defaultAddonName()).toBe('StreamNZB')
  })
})

describe('nextStreamName', () => {
  it('picks the first free slot, comparing case-insensitively', () => {
    expect(nextStreamName([])).toBe('Stream01')
    expect(nextStreamName([{ username: 'Stream01' }])).toBe('Stream02')
    // Backend names are case-insensitive, so "stream01" already takes the slot.
    expect(nextStreamName([{ username: 'stream01' }, { username: 'STREAM02' }])).toBe('Stream03')
  })

  it('fills a gap rather than always appending', () => {
    expect(nextStreamName([{ username: 'Stream01' }, { username: 'Stream03' }])).toBe('Stream02')
  })
})

describe('mapStreamsByUsername / streamsFromMap', () => {
  it('round-trips', () => {
    const streams = [{ username: 'a', token: '1' }, { username: 'b', token: '2' }]
    expect(streamsFromMap(mapStreamsByUsername(streams))).toEqual(streams)
  })
})

describe('applyFilterSortingMode', () => {
  it('forces display_all when switching to aiostreams, and drops the profile', () => {
    const next = applyFilterSortingMode(
      { filter_profile_name: '4k', results_mode: 'single' },
      'aiostreams',
    )
    expect(next.filter_sorting_mode).toBe('aiostreams')
    expect(next.filter_profile_name).toBe('')
    expect(next.results_mode).toBe('display_all')
  })

  it('restores the named profile when switching back', () => {
    const next = applyFilterSortingMode({ filter_sorting_mode: 'aiostreams' }, 'none', '4k')
    expect(next.filter_sorting_mode).toBe('none')
    expect(next.filter_profile_name).toBe('4k')
  })

  it('treats anything unrecognised as none', () => {
    expect(applyFilterSortingMode({}, 'something-else').filter_sorting_mode).toBe('none')
  })
})

describe('buildIndexerOverrides', () => {
  it('keeps existing overrides for indexers that are still selected', () => {
    const overrides = buildIndexerOverrides(['A', 'B'], { A: { limit: 5 }, C: { limit: 9 } })
    expect(overrides.A).toEqual({ limit: 5 })
    expect(overrides).toHaveProperty('B')
    // A dropped indexer must not keep a setting that would reapply if re-added.
    expect(overrides).not.toHaveProperty('C')
  })
})

describe('tabHasError', () => {
  it('maps a field error onto the tab that owns the field', () => {
    expect(tabHasError('search', { movie_search_queries: 'required' })).toBe(true)
    expect(tabHasError('search', { providers: 'required' })).toBe(false)
    expect(tabHasError('providers', { providers: 'required' })).toBe(true)
    // A tab with no fields of its own can never be the one at fault.
    expect(tabHasError('general', { providers: 'required' })).toBe(false)
  })
})
