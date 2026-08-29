import { describe, expect, it } from 'vitest'
import { assignedStreams, nameKey, usageByName } from '@/lib/usage'

describe('usageByName', () => {
  it('labels string refs with the stream name and matches case-insensitively', () => {
    const streams = {
      Kids: { format_profile_name: '  Fancy ' },
      TV: { format_profile_name: 'fancy' },
      Off: { format_profile_name: '' },
    }
    const usage = usageByName(streams, (stream) => [stream.format_profile_name])
    expect(usage[nameKey('Fancy')]).toEqual(['Kids', 'TV'])
    // An empty binding never lands under the empty key.
    expect(Object.keys(usage)).toEqual(['fancy'])
  })

  it('keeps labelled refs and dedupes repeated labels', () => {
    const streams = {
      Kids: { all: 'Strict', byType: { movie: 'Strict' } },
    }
    const usage = usageByName(streams, (stream, streamName) => [
      { name: stream.all, label: `${streamName} · all content` },
      ...Object.entries(stream.byType).map(([kind, name]) => ({ name, label: `${streamName} · ${kind}` })),
      { name: stream.all, label: `${streamName} · all content` },
    ])
    expect(usage[nameKey('Strict')]).toEqual(['Kids · all content', 'Kids · movie'])
  })

  it('skips whatever the extractor rules out', () => {
    const streams = {
      Passthrough: { mode: 'aiostreams', name: 'Ignored' },
      Normal: { mode: 'none', name: 'Counted' },
    }
    const usage = usageByName(streams, (stream) => (stream.mode === 'aiostreams' ? [] : [stream.name]))
    expect(usage).toEqual({ counted: ['Normal'] })
  })
})

describe('assignedStreams', () => {
  const byName = {
    Kids: { username: 'Kids', indexer_selections: ['NzbPlanet', 'Other'] },
    TV: { username: 'TV', indexer_selections: ['nzbplanet '] },
    None: { username: 'None' },
  }

  it('matches the list field case-insensitively', () => {
    expect(assignedStreams(byName, 'indexer_selections', 'NZBPlanet')).toEqual(['Kids', 'TV'])
  })

  it('answers empty for a blank target or missing map', () => {
    expect(assignedStreams(byName, 'indexer_selections', '  ')).toEqual([])
    expect(assignedStreams(null, 'indexer_selections', 'NzbPlanet')).toEqual([])
  })
})
