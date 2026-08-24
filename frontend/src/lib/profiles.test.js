import { describe, expect, it } from 'vitest'
import { rulesFromText, rulesToText } from '@/lib/profiles'

// The rules editor is a text box the user types into freehand, so parsing is
// the place a typo turns into a silently wrong ruleset.
describe('rulesFromText', () => {
  it('reads the three actions', () => {
    expect(rulesFromText('Prefer NTb: score 100 if group == "NTb"')).toEqual([
      { name: 'Prefer NTb', when: 'group == "NTb"', points: 100 },
    ])
    expect(rulesFromText('No CAM: reject if quality == "CAM"')).toEqual([
      { name: 'No CAM', when: 'quality == "CAM"', action: 'reject' },
    ])
    expect(rulesFromText('Top three: keep 3 if resolution == "2160p"')).toEqual([
      { name: 'Top three', when: 'resolution == "2160p"', action: 'limit', count: 3 },
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
