import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  checkDefineLibraryForUpdate, decodeDefineLibraryShareCode, defineLibraryFromPaste,
  defineLibraryFromRuleText, defineRulesFromText, encodeDefineLibraryShareCode,
  fetchRemoteDefineLibrary, mergeDefineLibraryUpstream,
} from '@/lib/defineLibraries'
import { encodeShareCode } from '@/lib/shareCodes'

const TEXT = [
  '# Generated from upstream.',
  'Movies Remux T1 Groups: define if group in ["FraMeSToR", "W4NK3R"]',
  'Anime WEB T1 Groups [anime_show]: define if releaseName matches "(?i)-(LostYears)$"',
  'Old tier [off]: define if group == "GONE"',
].join('\n')

describe('defineRulesFromText', () => {
  it('parses defines, skips # comments, keeps scope and off tags', () => {
    const rules = defineRulesFromText(TEXT)
    expect(rules).toEqual([
      { name: 'Movies Remux T1 Groups', when: 'group in ["FraMeSToR", "W4NK3R"]', action: 'define' },
      { name: 'Anime WEB T1 Groups', when: 'releaseName matches "(?i)-(LostYears)$"', action: 'define', scope: 'anime_show' },
      { name: 'Old tier', when: 'group == "GONE"', action: 'define', enabled: false },
    ])
  })

  it('refuses anything that is not a define', () => {
    // The library contract: data for profiles to reference, never policy. A
    // score or reject rule riding in on a refresh is refused before it can
    // change what every profile does.
    expect(() => defineRulesFromText('Sneaky: score 500 if true')).toThrow(/only carry defines/)
    expect(() => defineRulesFromText('Sneaky: reject if true')).toThrow(/only carry defines/)
  })

  it('refuses duplicate define names', () => {
    const text = 'Tier: define if group == "A"\ntier: define if group == "B"'
    expect(() => defineRulesFromText(text)).toThrow(/More than one define is named/)
  })

  it('reports parse errors by the line the writer is looking at', () => {
    // The comment line is blanked, not removed, so line numbers keep pointing
    // at the file as served.
    expect(() => defineRulesFromText('# comment\nnot a rule')).toThrow(/Line 2/)
  })
})

describe('defineLibraryFromRuleText', () => {
  it('names the library and refuses an empty file', () => {
    expect(defineLibraryFromRuleText(TEXT, 'My Tiers').name).toBe('My Tiers')
    expect(defineLibraryFromRuleText(TEXT, '').name).toBe('Define Library')
    expect(() => defineLibraryFromRuleText('# nothing here', 'X')).toThrow(/no define rules/)
  })
})

describe('share codes', () => {
  it('round-trips a library', async () => {
    const library = { name: 'Tiers', rules: defineRulesFromText(TEXT) }
    const code = await encodeDefineLibraryShareCode(library)
    expect(code.startsWith('SNZBD1:')).toBe(true)
    expect(await decodeDefineLibraryShareCode(code)).toEqual(library)
  })

  it('refuses a filter profile code', async () => {
    await expect(decodeDefineLibraryShareCode('SNZBP1:abc')).rejects.toThrow(/Not a StreamNZB define library code/)
  })

  it('tells a future schema version to update, not that the code is damaged', async () => {
    const future = await encodeShareCode('SNZBD1:',
      { streamnzb_define_library: 2, name: 'Future', rules: [] })
    await expect(decodeDefineLibraryShareCode(future)).rejects.toThrow(/newer StreamNZB.*Update/)
  })
})

describe('defineLibraryFromPaste', () => {
  it('takes a code or plain rule text, like a URL does', async () => {
    const library = { name: 'Tiers', rules: defineRulesFromText(TEXT) }
    const code = await encodeDefineLibraryShareCode(library)
    expect(await defineLibraryFromPaste(`${code}\n`)).toEqual(library)
    const pasted = await defineLibraryFromPaste(TEXT)
    expect(pasted.name).toBe('Define Library')
    expect(pasted.rules).toEqual(library.rules)
  })
})

describe('fetchRemoteDefineLibrary', () => {
  afterEach(() => vi.unstubAllGlobals())
  const serve = (text) => vi.stubGlobal('fetch', vi.fn(async () => new Response(text)))

  it('reads plain rule text, names the library after the file, and synthesizes the snapshot code', async () => {
    serve(TEXT)
    const { url, code, profile } = await fetchRemoteDefineLibrary('https://example.com/streamnzb-defines.txt')
    expect(url).toBe('https://example.com/streamnzb-defines.txt')
    expect(profile.name).toBe('streamnzb defines')
    expect(profile.rules).toHaveLength(3)
    // The snapshot is always a code the config layer can bound and recognize,
    // even when the URL serves text.
    expect(await decodeDefineLibraryShareCode(code)).toEqual(profile)
  })

  it('reads a share code when the URL serves one', async () => {
    const library = { name: 'Tiers', rules: defineRulesFromText(TEXT) }
    const code = await encodeDefineLibraryShareCode(library)
    serve(`${code}\n`)
    const result = await fetchRemoteDefineLibrary('https://example.com/lib.txt')
    expect(result.profile).toEqual(library)
    expect(result.code).toBe(code)
  })

  it('says what went wrong when the URL serves something else', async () => {
    serve('Just a README.')
    await expect(fetchRemoteDefineLibrary('https://example.com/lib.txt'))
      .rejects.toThrow(/did not return a define library/)
  })
})

describe('checkDefineLibraryForUpdate', () => {
  afterEach(() => vi.unstubAllGlobals())
  const serve = (text) => vi.stubGlobal('fetch', vi.fn(async () => new Response(text)))

  it('is current when the served rules match, whatever form they arrive in', async () => {
    serve(TEXT)
    const library = {
      name: 'My Tiers',
      rules: defineRulesFromText(TEXT),
      source: { url: 'https://example.com/lib.txt', code: 'SNZBD1:stale' },
    }
    expect(await checkDefineLibraryForUpdate(library)).toEqual({ status: 'current' })
  })

  it('replaces the rules wholesale behind a diff — local edits do not survive', async () => {
    serve('Movies Remux T1 Groups: define if group in ["FraMeSToR"]')
    const library = {
      name: 'My Tiers',
      rules: [
        { name: 'Movies Remux T1 Groups', when: 'group in ["FraMeSToR", "MINE"]', action: 'define' },
        { name: 'My own tier', when: 'group == "X"', action: 'define' },
      ],
      source: { url: 'https://example.com/lib.txt', code: '' },
    }
    const result = await checkDefineLibraryForUpdate(library)
    expect(result.status).toBe('update')
    expect(result.merged.name).toBe('My Tiers')
    expect(result.merged.rules).toEqual([
      { name: 'Movies Remux T1 Groups', when: 'group in ["FraMeSToR"]', action: 'define' },
    ])
    expect(result.diff.changed).toHaveLength(1)
    expect(result.diff.removed).toEqual([
      { key: 'remove:my own tier', name: 'My own tier', line: 'My own tier: define if group == "X"' },
    ])
  })
})

describe('mergeDefineLibraryUpstream', () => {
  it('takes the rules and keeps everything else local', () => {
    const local = { name: 'Mine', rules: [{ name: 'A', when: 'true', action: 'define' }], source: { url: 'https://x' } }
    const merged = mergeDefineLibraryUpstream(local, { name: 'Theirs', rules: [] })
    expect(merged.name).toBe('Mine')
    expect(merged.source).toEqual({ url: 'https://x' })
    expect(merged.rules).toEqual([])
  })
})
