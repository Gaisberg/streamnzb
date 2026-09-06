import { afterEach, describe, expect, it, vi } from 'vitest'
import { encodeProfileShareCode } from '@/lib/profiles'
import { applySelectedChanges, changeKeys, checkForUpdate, diffLinkedProfiles, mergeUpstream } from '@/lib/remoteProfiles'
import { sourceHost, validateSourceUrl } from '@/lib/shareCodes'

describe('validateSourceUrl', () => {
  it('accepts https and nothing else', () => {
    expect(validateSourceUrl(' https://raw.githubusercontent.com/u/r/main/p.txt '))
      .toBe('https://raw.githubusercontent.com/u/r/main/p.txt')
    // http could be rewritten by anyone on the path; a standing config-change
    // grant does not ride on it.
    expect(() => validateSourceUrl('http://example.com/p.txt')).toThrow(/https/)
    expect(() => validateSourceUrl('ftp://example.com/p.txt')).toThrow(/https/)
    expect(() => validateSourceUrl('not a url')).toThrow(/valid URL/)
    expect(() => validateSourceUrl('')).toThrow(/valid URL/)
  })
})

describe('sourceHost', () => {
  it('is the host, or empty for garbage', () => {
    expect(sourceHost('https://raw.githubusercontent.com/u/r/main/p.txt')).toBe('raw.githubusercontent.com')
    expect(sourceHost('nope')).toBe('')
  })
})

// The merge contract: customize by adding your own rules; edits to upstream
// rules last until the next refresh.
describe('mergeUpstream', () => {
  const rule = (name, points = 100) => ({ name, when: 'true', points })

  it('lets upstream win on a shared name, and keeps local-only rules after', () => {
    const local = {
      name: 'Mine', preset: '4k',
      rules: [rule('Shared', -50), rule('My own', 7)],
      limits: { default: { max_size_gb: 30 } },
    }
    const upstream = { name: 'Theirs', preset: '1080p', rules: [rule('Shared', 900), rule('New one')] }
    const { profile, keptLocal } = mergeUpstream(local, upstream, { rules: [rule('Shared')] })
    expect(profile.rules).toEqual([rule('Shared', 900), rule('New one'), rule('My own', 7)])
    expect(keptLocal).toEqual([rule('My own', 7)])
    // The preset follows upstream; the name and everything a share code does
    // not carry stay local.
    expect(profile.preset).toBe('1080p')
    expect(profile.name).toBe('Mine')
    expect(profile.limits).toEqual({ default: { max_size_gb: 30 } })
  })

  it('drops a rule the maintainer deleted, even an edited one', () => {
    const local = { name: 'Mine', preset: '4k', rules: [rule('Was upstream', -1), rule('My own')] }
    const upstream = { name: 'Theirs', preset: '4k', rules: [] }
    const previous = { rules: [rule('Was upstream')] }
    const { profile } = mergeUpstream(local, upstream, previous)
    expect(profile.rules).toEqual([rule('My own')])
  })

  it('keeps every unrecognized local rule when there is no snapshot', () => {
    // Failing open: without a snapshot nothing can prove a rule is not the
    // user's, and dropping one that might be theirs is the worse mistake.
    const local = { name: 'Mine', preset: '4k', rules: [rule('Mystery')] }
    const { profile } = mergeUpstream(local, { name: 'T', preset: '4k', rules: [] }, null)
    expect(profile.rules).toEqual([rule('Mystery')])
  })

  it('matches rule names case-insensitively', () => {
    const local = { name: 'Mine', preset: '4k', rules: [rule('shared', -1)] }
    const upstream = { name: 'T', preset: '4k', rules: [rule('Shared', 5)] }
    const { profile, keptLocal } = mergeUpstream(local, upstream, null)
    expect(profile.rules).toEqual([rule('Shared', 5)])
    expect(keptLocal).toEqual([])
  })

  // Scoring follows upstream like the preset does, as one map — with the
  // snapshot deciding whether a map upstream lacks was dropped or never there.
  describe('scoring', () => {
    const theirs = { movie: { size_target_gb: 20, size_weight: 500 } }
    const mine = { movie: { size_target_gb: 25, size_weight: 800 } }
    const base = { name: 'Mine', preset: '4k', rules: [] }

    it('replaces the local map with upstream\'s, local edits and all', () => {
      const { profile } = mergeUpstream({ ...base, scoring: mine }, { ...base, scoring: theirs }, { rules: [], scoring: mine })
      expect(profile.scoring).toEqual(theirs)
    })

    it('drops a map the maintainer removed', () => {
      const { profile } = mergeUpstream({ ...base, scoring: theirs }, base, { rules: [], scoring: theirs })
      expect('scoring' in profile).toBe(false)
    })

    it('keeps a map upstream never carried, with or without a snapshot', () => {
      // Hand-written scoring on a linked profile is the user's own, exactly
      // like a rule under a name upstream never used.
      expect(mergeUpstream({ ...base, scoring: mine }, base, { rules: [] }).profile.scoring).toEqual(mine)
      expect(mergeUpstream({ ...base, scoring: mine }, base, null).profile.scoring).toEqual(mine)
    })
  })
})

describe('diffLinkedProfiles', () => {
  const rule = (name, points = 100) => ({ name, when: 'true', points })

  it('reports changed, added, removed and the preset move', () => {
    const current = { name: 'Mine', preset: '4k', rules: [rule('Edited', 1), rule('Gone')] }
    const merged = { name: 'Mine', preset: '1080p', rules: [rule('Edited', 2), rule('Fresh')] }
    const diff = diffLinkedProfiles(current, merged)
    expect(diff.changed).toEqual([
      { key: 'change:edited', name: 'Edited', before: 'Edited: score 1 if true', after: 'Edited: score 2 if true' },
    ])
    expect(diff.added).toEqual([{ key: 'add:fresh', name: 'Fresh', line: 'Fresh: score 100 if true' }])
    expect(diff.removed).toEqual([{ key: 'remove:gone', name: 'Gone', line: 'Gone: score 100 if true' }])
    expect(diff.preset).toEqual({ key: 'preset', from: '4k', to: '1080p' })
    expect(diff.empty).toBe(false)
    // Every decision the dialog offers, and they are distinct: a rule can be
    // added, updated and removed under one name across three profiles.
    expect(changeKeys(diff)).toEqual(['change:edited', 'add:fresh', 'remove:gone', 'preset'])
  })

  it('is empty when nothing would change', () => {
    const profile = { name: 'Mine', preset: '4k', rules: [rule('Same')] }
    expect(diffLinkedProfiles(profile, { ...profile }).empty).toBe(true)
  })

  it('reports a scoring move as one decision, in text', () => {
    const current = { name: 'Mine', preset: '4k', rules: [] }
    const merged = { ...current, scoring: { movie: { size_target_gb: 20, size_weight: 500 } } }
    const diff = diffLinkedProfiles(current, merged)
    expect(diff.scoring).toEqual({ key: 'scoring', before: '', after: 'movie: size_target_gb 20, size_weight 500' })
    expect(diff.empty).toBe(false)
    expect(changeKeys(diff)).toEqual(['scoring'])
    // Same map, different key order: nothing to decide.
    const reordered = { ...merged, scoring: { movie: { size_weight: 500, size_target_gb: 20 } } }
    expect(diffLinkedProfiles(merged, reordered).empty).toBe(true)
  })
})

// The dialog lets a change be unticked; applySelectedChanges is what an
// unticked change means. Nothing is remembered — the snapshot the caller
// stores is still upstream in full — so a skipped change comes back next time.
describe('applySelectedChanges', () => {
  const rule = (name, points = 100) => ({ name, when: 'true', points })
  const current = { name: 'Mine', preset: '4k', rules: [rule('Edited', 1), rule('Gone'), rule('My own', 7)] }
  const merged = { name: 'Mine', preset: '1080p', rules: [rule('Edited', 2), rule('Fresh'), rule('My own', 7)] }
  const diff = diffLinkedProfiles(current, merged)

  it('is the whole merge when everything is ticked', () => {
    expect(applySelectedChanges(current, merged, diff, new Set(changeKeys(diff)))).toEqual(merged)
  })

  it('leaves the profile alone when nothing is ticked', () => {
    const applied = applySelectedChanges(current, merged, diff, new Set())
    expect(applied.rules).toEqual([rule('Edited', 1), rule('My own', 7), rule('Gone')])
    expect(applied.preset).toBe('4k')
  })

  it('takes only the ticked changes', () => {
    // Take the addition and the preset move; keep the local edit, and refuse
    // the deletion — the refused rule stays, after upstream's, where the
    // user's own rules live.
    const applied = applySelectedChanges(current, merged, diff, new Set(['add:fresh', 'preset']))
    expect(applied.rules).toEqual([rule('Edited', 1), rule('Fresh'), rule('My own', 7), rule('Gone')])
    expect(applied.preset).toBe('1080p')
  })

  it('keeps the local scoring when the scoring change is unticked', () => {
    const theirs = { movie: { size_target_gb: 20, size_weight: 500 } }
    const mine = { movie: { size_target_gb: 25, size_weight: 800 } }
    const scoredMerge = { ...merged, scoring: theirs }
    // Declined: an edited local map stays, and an absent one stays absent.
    const withMine = { ...current, scoring: mine }
    let diff = diffLinkedProfiles(withMine, scoredMerge)
    expect(applySelectedChanges(withMine, scoredMerge, diff, new Set()).scoring).toEqual(mine)
    diff = diffLinkedProfiles(current, scoredMerge)
    expect('scoring' in applySelectedChanges(current, scoredMerge, diff, new Set())).toBe(false)
    // Taken on its own: the map lands and nothing else moves.
    const applied = applySelectedChanges(current, scoredMerge, diff, new Set(['scoring']))
    expect(applied.scoring).toEqual(theirs)
    expect(applied.preset).toBe('4k')
    expect(applied.rules).toEqual([rule('Edited', 1), rule('My own', 7), rule('Gone')])
  })

  it('leaves a diff without a preset move alone', () => {
    // A define library has no preset; the merge must not grow one.
    const lib = { name: 'Tiers', rules: [rule('A')] }
    const upstream = { name: 'Tiers', rules: [rule('A', 2)] }
    const libDiff = diffLinkedProfiles(lib, upstream)
    expect(applySelectedChanges(lib, upstream, libDiff, new Set())).toEqual(lib)
    expect('preset' in applySelectedChanges(lib, upstream, libDiff, new Set())).toBe(false)
  })
})

// checkForUpdate against a mocked fetch: the decoded diff is what decides,
// never the byte compare alone — a locally drifted profile under an unchanged
// upstream is still an update.
describe('checkForUpdate', () => {
  afterEach(() => vi.unstubAllGlobals())

  const serve = (text) => vi.stubGlobal('fetch', vi.fn(async () => new Response(text)))
  const upstream = {
    name: 'Community',
    preset: '4k',
    rules: [{ name: 'IMAX', when: 'releaseName matches "(?i)IMAX"', points: 2000 }],
  }

  it('is current when the snapshot matches and nothing drifted locally', async () => {
    const code = await encodeProfileShareCode(upstream)
    serve(`${code}\n`)
    const profile = { ...upstream, source: { url: 'https://example.com/p.txt', code } }
    expect(await checkForUpdate(profile)).toEqual({ status: 'current' })
  })

  it('offers the upstream rules back when local edits drifted under an unchanged code', async () => {
    // The reported case: import, delete an imported rule, press Refresh. The
    // bytes upstream are identical, but the merge contract says upstream-owned
    // rules come back — "Up to date" here would make Refresh a dead button.
    const code = await encodeProfileShareCode(upstream)
    serve(code)
    const profile = {
      name: 'Mine',
      preset: '4k',
      rules: [],
      source: { url: 'https://example.com/p.txt', code },
    }
    const result = await checkForUpdate(profile)
    expect(result.status).toBe('update')
    expect(result.merged.rules.map((r) => r.name)).toEqual(['IMAX'])
    expect(result.diff.added).toEqual([
      { key: 'add:imax', name: 'IMAX', line: 'IMAX: score 2000 if releaseName matches "(?i)IMAX"' },
    ])
  })

  it('is current when a re-encoded upstream decodes to no visible change', async () => {
    // Different bytes, same profile: the decoded diff, not the string
    // compare, is what decides.
    const code = await encodeProfileShareCode(upstream)
    serve(code)
    const profile = { ...structuredClone(upstream), source: { url: 'https://example.com/p.txt', code: 'SNZBP1:stale' } }
    expect(await checkForUpdate(profile)).toEqual({ status: 'current' })
  })

  it('returns the merged update behind a diff', async () => {
    const oldCode = await encodeProfileShareCode(upstream)
    const newUpstream = {
      ...upstream,
      name: 'Community v2',
      rules: [{ name: 'IMAX', when: 'releaseName matches "(?i)IMAX"', points: 3000 }],
    }
    const newCode = await encodeProfileShareCode(newUpstream)
    serve(newCode)
    const profile = {
      name: 'My community profile',
      preset: '4k',
      rules: [...structuredClone(upstream.rules), { name: 'My tweak', when: 'true', points: 5 }],
      source: { url: 'https://example.com/p.txt', code: oldCode },
    }
    const result = await checkForUpdate(profile)
    expect(result.status).toBe('update')
    expect(result.code).toBe(newCode)
    expect(result.remoteName).toBe('Community v2')
    expect(result.merged.name).toBe('My community profile')
    expect(result.merged.rules.map((r) => r.name)).toEqual(['IMAX', 'My tweak'])
    expect(result.merged.rules[0].points).toBe(3000)
    expect(result.diff.changed).toHaveLength(1)
    expect(result.keptLocal.map((r) => r.name)).toEqual(['My tweak'])
  })

  it('says what went wrong when the URL serves something else', async () => {
    serve('# Not a profile\nJust a README.')
    const profile = { name: 'Mine', preset: '4k', rules: [], source: { url: 'https://example.com/p.txt', code: '' } }
    await expect(checkForUpdate(profile)).rejects.toThrow(/did not return a profile share code/)
  })

  it('reports an HTTP failure as the status it was', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('gone', { status: 404 })))
    const profile = { name: 'Mine', preset: '4k', rules: [], source: { url: 'https://example.com/p.txt', code: '' } }
    await expect(checkForUpdate(profile)).rejects.toThrow(/HTTP 404/)
  })
})
