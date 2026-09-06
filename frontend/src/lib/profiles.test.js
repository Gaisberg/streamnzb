import { describe, expect, it } from 'vitest'
import {
  decodeProfileShareCode, encodeProfileShareCode, inlineRuleRefs, renameRuleRefs,
  resolveProfileShareCode, rulesFromText, rulesToText, scoringToText,
} from '@/lib/profiles'
import { encodeShareCode, resolveShareCode } from '@/lib/shareCodes'

// The rules editor is a text box the user types into freehand, so parsing is
// the place a typo turns into a silently wrong ruleset.
describe('rulesFromText', () => {
  it('reads the five actions', () => {
    expect(rulesFromText('Prefer NTb: score 100 if group == "NTb"')).toEqual([
      { name: 'Prefer NTb', when: 'group == "NTb"', points: 100 },
    ])
    expect(rulesFromText('No CAM: reject if quality == "CAM"')).toEqual([
      { name: 'No CAM', when: 'quality == "CAM"', action: 'reject' },
    ])
    expect(rulesFromText('Top three: keep 3 if resolution == "2160p"')).toEqual([
      { name: 'Top three', when: 'resolution == "2160p"', action: 'limit', count: 3 },
    ])
    expect(rulesFromText('T1 groups: define if group in ["FraMeSToR", "NTb"]')).toEqual([
      { name: 'T1 groups', when: 'group in ["FraMeSToR", "NTb"]', action: 'define' },
    ])
    expect(rulesFromText('Weak tail: prune if finalScore < 0 and count(finalScore >= 0) >= 6')).toEqual([
      { name: 'Weak tail', when: 'finalScore < 0 and count(finalScore >= 0) >= 6', action: 'prune' },
    ])
  })

  it('reads what a cap groups by', () => {
    expect(rulesFromText('Per resolution: keep 3 per resolution if true')).toEqual([
      { name: 'Per resolution', when: 'true', action: 'limit', count: 3, group_by: 'resolution' },
    ])
    // The grouping is an expression, and it ends at the first " if " rather
    // than swallowing the condition behind it.
    expect(rulesFromText('Combo: keep 2 per resolution + " " + quality if sizeGB > 5')).toEqual([
      {
        name: 'Combo',
        when: 'sizeGB > 5',
        action: 'limit',
        count: 2,
        group_by: 'resolution + " " + quality',
      },
    ])
  })

  it('reads scope and off tags, and leaves lookalike names alone', () => {
    expect(rulesFromText('Anime only [anime_show]: score 50 if true')).toEqual([
      { name: 'Anime only', when: 'true', points: 50, scope: 'anime_show' },
    ])
    expect(rulesFromText('Paused [off]: score 50 if true')).toEqual([
      { name: 'Paused', when: 'true', points: 50, enabled: false },
    ])
    // Brackets only mean tags when every token is one, so a name may contain them.
    expect(rulesFromText('Remux [2160p]: score 50 if true')).toEqual([
      { name: 'Remux [2160p]', when: 'true', points: 50 },
    ])
  })

  it('splits on the colon that starts an action, not the first one', () => {
    // Both the name and the condition may contain a colon of their own.
    const [rule] = rulesFromText('Tier 1: NTb: score 10 if title contains "a: b"')
    expect(rule.name).toBe('Tier 1: NTb')
    expect(rule.when).toBe('title contains "a: b"')
  })

  it('skips blank lines', () => {
    expect(rulesFromText('\n  \nA: score 1 if true\n\n')).toHaveLength(1)
  })

  it('throws with the line number, and returns nothing at all', () => {
    // Half a ruleset silently replacing a whole one is the failure worth
    // avoiding here, so a bad line must abort the parse rather than be skipped.
    expect(() => rulesFromText('A: score 1 if true\nnonsense')).toThrow(/Line 2/)
    expect(() => rulesFromText('A: score 1 if')).toThrow(/no condition/)
    expect(() => rulesFromText('A: keep 0 if true')).toThrow(/at least one release/)
    // "per" with nothing after it is a half-typed grouping, not an ungrouped
    // cap: applying it would quietly drop what the user was in the middle of.
    expect(() => rulesFromText('A: keep 3 per  if true')).toThrow(/Line 1/)
  })
})

describe('rulesToText', () => {
  it('round-trips through rulesFromText', () => {
    const rules = [
      { name: 'Prefer NTb', when: 'group == "NTb"', points: 100 },
      { name: 'No CAM', when: 'quality == "CAM"', action: 'reject' },
      { name: 'Top three', when: 'resolution == "2160p"', action: 'limit', count: 3 },
      { name: 'T1 groups', when: 'group in ["FraMeSToR", "NTb"]', action: 'define' },
      { name: 'Weak tail', when: 'finalScore < 0 and count(finalScore >= 0) >= 6', action: 'prune' },
      { name: 'Anime only', when: 'true', points: 50, scope: 'anime_show' },
      { name: 'Paused', when: 'true', points: 50, enabled: false },
      { name: 'Per resolution', when: 'true', action: 'limit', count: 3, group_by: 'resolution' },
      {
        name: 'Per combo',
        when: 'true',
        action: 'limit',
        count: 2,
        group_by: 'resolution + " " + quality',
      },
    ]
    expect(rulesFromText(rulesToText(rules))).toEqual(rules)
  })

  it('leaves a grouping off anything that is not a cap', () => {
    // Only a limit rule can carry one — the server refuses a grouped score or
    // reject rule — so a grouping left behind by an action change is dropped
    // here rather than written into a line the parser would then honour.
    const text = rulesToText([{ name: 'Stale', when: 'true', points: 50, group_by: 'resolution' }])
    expect(text).toBe('Stale: score 50 if true')
  })

  it('folds a multi-line condition onto one line', () => {
    // The text form is one line per rule; the expression language does not care
    // where the whitespace fell.
    const text = rulesToText([{ name: 'A', when: 'a == 1\n  && b == 2', points: 1 }])
    expect(text).toBe('A: score 1 if a == 1 && b == 2')
    expect(text.split('\n')).toHaveLength(1)
  })

  it('is empty for no rules', () => {
    expect(rulesToText([])).toBe('')
    expect(rulesToText()).toBe('')
  })
})

// Share codes now arrive from URLs as well as from a paste, so the decoder is
// a trust boundary: whatever a fetched blob claims to be, only a bounded,
// well-shaped profile may come out of it.
describe('share codes', () => {
  const profile = {
    name: 'Community 4K',
    preset: '4k',
    rules: [
      { name: 'IMAX', when: 'releaseName matches "(?i)\\bIMAX\\b"', points: 2000 },
      { name: 'No CAM', when: 'quality == "CAM"', action: 'reject' },
      { name: 'Weak tail', when: 'finalScore < 0 and count(finalScore >= 0) >= 6', action: 'prune' },
    ],
  }

  it('round-trips a profile', async () => {
    const code = await encodeProfileShareCode(profile)
    expect(await decodeProfileShareCode(code)).toEqual(profile)
  })

  it('reports the exact candidate that decoded, for snapshot comparison', async () => {
    const code = await encodeProfileShareCode(profile)
    const resolved = await resolveProfileShareCode(`Try this one!\n${code}\nenjoy`)
    expect(resolved.code).toBe(code)
    expect(resolved.profile).toEqual(profile)
  })

  it('refuses a profile with too many rules', async () => {
    const bloated = {
      name: 'Bloat',
      preset: '4k',
      rules: Array.from({ length: 501 }, (_, i) => ({ name: `R${i}`, when: 'true', points: 1 })),
    }
    await expect(decodeProfileShareCode(await encodeProfileShareCode(bloated)))
      .rejects.toThrow(/most a profile can hold/)
  })

  it('refuses an oversized condition and an oversized name', async () => {
    const longWhen = {
      name: 'Long', preset: '4k',
      rules: [{ name: 'A', when: `true || ${'x'.repeat(10001)}`, points: 1 }],
    }
    await expect(decodeProfileShareCode(await encodeProfileShareCode(longWhen)))
      .rejects.toThrow(/condition longer/)
    const longName = { name: 'n'.repeat(201), preset: '4k', rules: [] }
    await expect(decodeProfileShareCode(await encodeProfileShareCode(longName)))
      .rejects.toThrow(/name is too long/)
  })

  it('tells a future schema version to update, not that the code is damaged', async () => {
    // This message has to be deployed before the next schema bump ever
    // ships — it is the old versions in the field that need to speak it.
    const future = await encodeShareCode('SNZBP1:',
      { streamnzb_profile: 3, name: 'Future', preset: '4k', rules: [] })
    await expect(decodeProfileShareCode(future)).rejects.toThrow(/newer StreamNZB.*Update/)
  })

  // Attribute scoring travels since schema version 2. The importer refuses
  // what it does not know, as it does for rules, but leaves the meaning of a
  // map — which pairs are inert — to the server, which fails open on them.
  describe('scoring', () => {
    const payload = async (code) => (await resolveShareCode('SNZBP1:', code, 'not a profile')).parsed
    const scoring = {
      default: { age_fresh_days: 30, age_weight: 100 },
      movie: { size_target_gb: 20, size_weight: 500 },
      anime_movie: { size_target_gb: 20, size_weight: 500 },
      series: { size_target_gb: 6, size_weight: 500, grabs_target: 50, grabs_weight: -150 },
      anime_show: { size_target_gb: 6.5, size_weight: 500 },
    }

    it('round-trips every field, the default entry and a negative weight', async () => {
      const scored = { ...profile, scoring }
      expect(await decodeProfileShareCode(await encodeProfileShareCode(scored))).toEqual(scored)
    })

    it('stamps schema version 2 only when scoring is carried', async () => {
      // The lowest version the payload needs: a profile without scoring keeps
      // importing into every StreamNZB that ever read version 1, and one with
      // scoring is refused there instead of quietly losing it.
      expect((await payload(await encodeProfileShareCode(profile))).streamnzb_profile).toBe(1)
      const scored = await payload(await encodeProfileShareCode({ ...profile, scoring }))
      expect(scored.streamnzb_profile).toBe(2)
      expect(scored.scoring).toEqual(scoring)
    })

    it('carries an inert pair as written and leaves zeros behind', async () => {
      // A weight with no target does nothing on the server; that is the
      // server's fail-open, not a reason to refuse the code. Zero means
      // "inherit" on the wire and is the same as absent.
      const scored = { ...profile, scoring: { movie: { size_weight: 500, grabs_target: 0 }, series: { age_weight: 0 } } }
      expect((await decodeProfileShareCode(await encodeProfileShareCode(scored))).scoring)
        .toEqual({ movie: { size_weight: 500 } })
      // Nothing left is no scoring at all — and a version-1 code.
      const inert = await encodeProfileShareCode({ ...profile, scoring: { movie: { size_weight: 0 } } })
      expect((await payload(inert)).streamnzb_profile).toBe(1)
      expect(await decodeProfileShareCode(inert)).toEqual(profile)
    })

    it('refuses an unknown kind or field as newer, and a wrong type as damage', async () => {
      const code = (scoring) => encodeShareCode('SNZBP1:',
        { streamnzb_profile: 2, name: 'P', preset: '4k', rules: [], scoring })
      await expect(decodeProfileShareCode(await code({ podcast: { size_weight: 1 } })))
        .rejects.toThrow(/content kind this StreamNZB does not know.*podcast/)
      await expect(decodeProfileShareCode(await code({ movie: { runtime_weight: 1 } })))
        .rejects.toThrow(/field this StreamNZB does not know.*runtime_weight/)
      await expect(decodeProfileShareCode(await code({ movie: { size_weight: 500.5 } })))
        .rejects.toThrow(/size_weight that is not a whole number/)
      await expect(decodeProfileShareCode(await code({ movie: { size_target_gb: '20' } })))
        .rejects.toThrow(/size_target_gb that is not a number/)
      await expect(decodeProfileShareCode(await code({ movie: [500] })))
        .rejects.toThrow(/scoring for movie is not an object/)
      await expect(decodeProfileShareCode(await code([])))
        .rejects.toThrow(/scoring is not an object/)
    })

    it('renders as one line per kind in the config field names', () => {
      expect(scoringToText(scoring).split('\n')).toEqual([
        'default: age_fresh_days 30, age_weight 100',
        'movie: size_target_gb 20, size_weight 500',
        'series: size_target_gb 6, size_weight 500, grabs_target 50, grabs_weight -150',
        'anime_movie: size_target_gb 20, size_weight 500',
        'anime_show: size_target_gb 6.5, size_weight 500',
      ])
      expect(scoringToText(undefined)).toBe('')
      expect(scoringToText({ movie: { size_weight: 0 } })).toBe('')
    })
  })

  it('refuses an unknown rule action instead of importing it as a score rule', async () => {
    const code = await encodeShareCode('SNZBP1:', {
      streamnzb_profile: 1, name: 'P', preset: '4k',
      rules: [{ name: 'A', when: 'true', action: 'boost' }],
    })
    await expect(decodeProfileShareCode(code)).rejects.toThrow(/action this StreamNZB does not know/)
  })

  it('refuses an unknown preset instead of downgrading it to the default', async () => {
    const code = await encodeShareCode('SNZBP1:',
      { streamnzb_profile: 1, name: 'P', preset: '8k', rules: [] })
    await expect(decodeProfileShareCode(code)).rejects.toThrow(/preset this StreamNZB does not know/)
    // An absent preset is not an unknown one: it still falls back.
    const bare = await encodeShareCode('SNZBP1:', { streamnzb_profile: 1, name: 'P', rules: [] })
    expect((await decodeProfileShareCode(bare)).preset).toBe('4k')
  })

  it('refuses a decompression bomb', async () => {
    // A few bytes of code inflating to megabytes is not a profile. The bomb
    // here is a hugely repetitive name, which gzip shrinks to almost nothing.
    const bomb = { name: 'a'.repeat(6 * 1024 * 1024), preset: '4k', rules: [] }
    await expect(decodeProfileShareCode(await encodeProfileShareCode(bomb)))
      .rejects.toThrow(/damaged or incomplete/)
  })
})

// A rule reference is by name, so the editor has to follow one to tell what a
// rule really depends on, and rewrite one when the rule it names is renamed.
describe('rule references', () => {
  const tiers = [
    { name: 'Trusted', when: 'group == "GRP"' },
    { name: 'Probed', when: 'probed.height >= 2000' },
    { name: 'Off', when: 'probed.height < 100', enabled: false },
  ]

  it('inlines what a reference pulls in', () => {
    expect(inlineRuleRefs('matched("Probed") and resolution == "2160p"', tiers))
      .toBe('(probed.height >= 2000) and resolution == "2160p"')
  })

  it('follows a chain and matches names case-insensitively', () => {
    const rules = [...tiers, { name: 'Middle', when: 'matched("probed") and year > 2000' }]
    expect(inlineRuleRefs('matched("Middle")', rules))
      .toBe('((probed.height >= 2000) and year > 2000)')
  })

  it('drops a reference to a rule that is off, missing, or circular', () => {
    expect(inlineRuleRefs('matched("Off")', tiers)).toBe('')
    expect(inlineRuleRefs('matched("Nothing")', tiers)).toBe('')
    const loop = [{ name: 'A', when: 'matched("B")' }, { name: 'B', when: 'matched("A")' }]
    expect(inlineRuleRefs('matched("A")', loop)).not.toContain('matched(')
  })

  it('rewrites references when a rule is renamed', () => {
    const rules = [
      { name: 'Trusted UHD', when: 'group == "GRP"' },
      { name: 'Uses it', when: 'not matched("Trusted UHD") and resolution == "2160p"' },
      { name: 'Cap', when: 'true', action: 'limit', count: 3, group_by: 'matched("trusted uhd")' },
      { name: 'Untouched', when: 'year > 2000' },
    ]
    const renamed = renameRuleRefs(rules, 'Trusted UHD', 'Trusted 4K')
    expect(renamed[1].when).toBe('not matched("Trusted 4K") and resolution == "2160p"')
    expect(renamed[2].group_by).toBe('matched("Trusted 4K")')
    expect(renamed[3]).toBe(rules[3])
  })

  it('leaves everything alone when the rename is empty or a no-op', () => {
    const rules = [{ name: 'Uses it', when: 'matched("Trusted")' }]
    expect(renameRuleRefs(rules, 'Trusted', '  ')).toBe(rules)
    expect(renameRuleRefs(rules, '', 'Trusted')).toBe(rules)
    expect(renameRuleRefs(rules, 'Trusted', 'trusted')).toBe(rules)
  })
})
