import { afterEach, describe, expect, it, vi } from 'vitest'
import { encodeProfileShareCode } from '@/lib/profiles'
import { encodeShareCode } from '@/lib/shareCodes'
import {
  applySelectedFormatChanges, checkFormatForUpdate, decodeFormatProfileShareCode, diffFormatProfiles,
  encodeFormatProfileShareCode, formatChangeKeys, mergeFormatUpstream,
} from '@/lib/formatProfiles'

const profile = {
  name: 'Compact',
  result_name_template: '{{.Resolution}} {{.Codec}}',
  result_description_template: '{{.SizeGB}} GB · {{.Group}}',
}

describe('format share codes', () => {
  it('round-trips a profile', async () => {
    const code = await encodeFormatProfileShareCode(profile)
    expect(code.startsWith('SNZBF1:')).toBe(true)
    expect(await decodeFormatProfileShareCode(code)).toEqual(profile)
  })

  it('leaves an empty template absent, matching what the backend stores', async () => {
    const nameOnly = { name: 'Half', result_name_template: '{{.Resolution}}' }
    expect(await decodeFormatProfileShareCode(await encodeFormatProfileShareCode(nameOnly)))
      .toEqual(nameOnly)
  })

  it('refuses a filter profile code, by name', async () => {
    // The two kinds must not cross-import: a filter code in the format
    // importer is a user mistake worth naming, not a damaged code.
    const filterCode = await encodeProfileShareCode({ name: 'Filters', preset: '4k', rules: [] })
    await expect(decodeFormatProfileShareCode(filterCode)).rejects.toThrow(/format profile code/)
  })

  it('tells a future schema version to update, not that the code is damaged', async () => {
    const future = await encodeShareCode('SNZBF1:',
      { streamnzb_format_profile: 2, name: 'Future' })
    await expect(decodeFormatProfileShareCode(future)).rejects.toThrow(/newer StreamNZB.*Update/)
  })

  it('refuses an oversized template', async () => {
    const bloated = { name: 'Big', result_name_template: 'x'.repeat(20001) }
    await expect(decodeFormatProfileShareCode(await encodeFormatProfileShareCode(bloated)))
      .rejects.toThrow(/name template is longer/)
  })
})

// The format merge contract: templates theirs, name yours.
describe('mergeFormatUpstream and diffFormatProfiles', () => {
  it('replaces the templates and keeps the local name', () => {
    const local = { ...profile, name: 'Mine', source: { url: 'https://example.com/f.txt' } }
    const upstream = { name: 'Theirs', result_name_template: '{{.Codec}} only' }
    const merged = mergeFormatUpstream(local, upstream)
    expect(merged.name).toBe('Mine')
    expect(merged.source).toEqual(local.source)
    expect(merged.result_name_template).toBe('{{.Codec}} only')
    // Upstream cleared its description template, so the merge drops it too —
    // that half falls back to the built-in format.
    expect(merged.result_description_template).toBeUndefined()
  })

  it('shows a cleared template as the built-in format, not as nothing', () => {
    const merged = { name: 'Mine', result_name_template: '{{.Codec}}' }
    const diff = diffFormatProfiles(profile, merged)
    expect(diff.changes).toEqual([
      {
        key: 'result_name_template', field: 'result_name_template', label: 'Name template',
        before: '{{.Resolution}} {{.Codec}}', after: '{{.Codec}}',
      },
      {
        key: 'result_description_template', field: 'result_description_template', label: 'Description template',
        before: '{{.SizeGB}} GB · {{.Group}}', after: '(built-in format)',
      },
    ])
    expect(diff.empty).toBe(false)
    expect(diffFormatProfiles(profile, { ...profile }).empty).toBe(true)
  })
})

// The dialog offers the two templates separately; an unticked one keeps
// whatever is there now, down to being absent.
describe('applySelectedFormatChanges', () => {
  // Upstream sets a name template and carries no description template, which
  // the merge reads as "the built-in format".
  const merged = mergeFormatUpstream(profile, { name: 'Theirs', result_name_template: '{{.Codec}}' })
  const diff = diffFormatProfiles(profile, merged)

  it('is the whole merge when both templates are ticked', () => {
    expect(applySelectedFormatChanges(profile, merged, diff, new Set(formatChangeKeys(diff)))).toEqual(merged)
  })

  it('takes only the ticked template', () => {
    const applied = applySelectedFormatChanges(profile, merged, diff, new Set(['result_name_template']))
    expect(applied.result_name_template).toBe('{{.Codec}}')
    // The description was cleared upstream and left unticked, so the local one
    // stays rather than falling back to the built-in format.
    expect(applied.result_description_template).toBe('{{.SizeGB}} GB · {{.Group}}')
  })

  it('leaves the profile alone when nothing is ticked', () => {
    expect(applySelectedFormatChanges(profile, merged, diff, new Set())).toEqual(profile)
  })
})

describe('checkFormatForUpdate', () => {
  afterEach(() => vi.unstubAllGlobals())

  const serve = (text) => vi.stubGlobal('fetch', vi.fn(async () => new Response(text)))

  it('is current when applying would change nothing', async () => {
    serve(await encodeFormatProfileShareCode(profile))
    const linked = { ...profile, source: { url: 'https://example.com/f.txt', code: 'SNZBF1:stale' } }
    expect(await checkFormatForUpdate(linked)).toEqual({ status: 'current' })
  })

  it('offers the maintainer templates back when local edits drifted', async () => {
    const code = await encodeFormatProfileShareCode(profile)
    serve(code)
    const linked = {
      name: 'Mine',
      result_name_template: 'tweaked locally',
      source: { url: 'https://example.com/f.txt', code },
    }
    const result = await checkFormatForUpdate(linked)
    expect(result.status).toBe('update')
    expect(result.code).toBe(code)
    expect(result.remoteName).toBe('Compact')
    expect(result.merged.name).toBe('Mine')
    expect(result.merged.result_name_template).toBe(profile.result_name_template)
    expect(result.diff.changes.map((c) => c.label)).toEqual(['Name template', 'Description template'])
  })
})
