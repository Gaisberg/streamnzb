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
    ]
    expect(rulesFromText(rulesToText(rules))).toEqual(rules)
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
