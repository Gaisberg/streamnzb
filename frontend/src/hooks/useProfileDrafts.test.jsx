// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, renderHook } from '@testing-library/react'
import { uniqueProfileName, useProfileDrafts } from '@/hooks/useProfileDrafts'

// The machinery under test carries every documented past bug of the profile
// pages: a save echo clobbering newer typing, the spinner that never stopped,
// the delete that renumbered rows under the editor. Each test pins one of the
// behaviours those bugs taught.

function setup(profiles, opts = {}) {
  const onSave = vi.fn()
  const props = { profiles, onSave, newProfile: (name) => ({ name }), ...opts }
  const view = renderHook((p) => useProfileDrafts(p), { initialProps: props })
  return { view, onSave, props }
}

describe('uniqueProfileName', () => {
  it('counts from 2 and matches case-insensitively', () => {
    const profiles = [{ name: 'New Profile' }, { name: 'new profile 2' }]
    expect(uniqueProfileName(profiles, 'New Profile')).toBe('New Profile 3')
    expect(uniqueProfileName(profiles, 'Other')).toBe('Other')
  })
})

describe('useProfileDrafts', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  it('adopts the selected profile as the draft, cloned', () => {
    const first = { name: 'A' }
    const { view } = setup([first, { name: 'B' }])
    expect(view.result.current.draft).toEqual({ name: 'A' })
    // A clone, not the row itself — edits must not mutate the saved list.
    expect(view.result.current.draft).not.toBe(first)
  })

  it('debounces an edit into one whole-list save', () => {
    const { view, onSave } = setup([{ name: 'A' }, { name: 'B' }])
    act(() => view.result.current.setDraft({ name: 'A', tag: 1 }))
    expect(view.result.current.dirty).toBe(true)
    act(() => vi.advanceTimersByTime(599))
    expect(onSave).not.toHaveBeenCalled()
    act(() => vi.advanceTimersByTime(1))
    expect(onSave).toHaveBeenCalledTimes(1)
    expect(onSave.mock.calls[0][0]).toEqual([{ name: 'A', tag: 1 }, { name: 'B' }])
  })

  it('commits the normalized profile, name trimmed', () => {
    const { view, onSave } = setup([{ name: 'A' }], {
      normalizeOnSave: (p) => ({ ...p, mirrored: p.name }),
    })
    act(() => view.result.current.setDraft({ name: '  A renamed  ' }))
    act(() => vi.advanceTimersByTime(600))
    expect(onSave.mock.calls[0][0][0]).toEqual({ name: 'A renamed', mirrored: 'A renamed' })
  })

  it('parks the save while the name is invalid and surfaces the error', () => {
    const { view, onSave } = setup([{ name: 'A' }, { name: 'B' }])
    act(() => view.result.current.setDraft({ name: 'b' }))
    act(() => vi.advanceTimersByTime(5000))
    expect(onSave).not.toHaveBeenCalled()
    expect(view.result.current.nameError).toBe('Another profile already uses this name.')

    act(() => view.result.current.setDraft({ name: '   ' }))
    act(() => vi.advanceTimersByTime(5000))
    expect(onSave).not.toHaveBeenCalled()
    expect(view.result.current.nameError).toBe('Name is required.')
  })

  it('flushes a pending valid edit into the save that switches profiles', () => {
    const { view, onSave } = setup([{ name: 'A' }, { name: 'B' }])
    act(() => view.result.current.setDraft({ name: 'A renamed' }))
    act(() => view.result.current.selectProfile(1))
    expect(onSave).toHaveBeenCalledTimes(1)
    expect(onSave.mock.calls[0][0]).toEqual([{ name: 'A renamed' }, { name: 'B' }])
    // The newly selected row is adopted even though that save is in flight.
    expect(view.result.current.draft).toEqual({ name: 'B' })
  })

  it('discards an invalid pending edit on switch instead of saving it', () => {
    const { view, onSave } = setup([{ name: 'A' }, { name: 'B' }])
    act(() => view.result.current.setDraft({ name: '' }))
    act(() => view.result.current.selectProfile(1))
    expect(onSave).not.toHaveBeenCalled()
    expect(view.result.current.draft).toEqual({ name: 'B' })
    expect(view.result.current.nameError).toBe('')
  })

  it('adds with a free name and adopts the row once the save echoes back', () => {
    const { view, onSave, props } = setup([{ name: 'New Profile' }])
    act(() => view.result.current.addProfile())
    const saved = onSave.mock.calls[0][0]
    expect(saved.map((p) => p.name)).toEqual(['New Profile', 'New Profile 2'])
    expect(view.result.current.selected).toBe(1)
    // Until the saved list arrives back through props there is nothing at the
    // new index; the previous draft holds.
    act(() => view.rerender({ ...props, profiles: saved }))
    expect(view.result.current.draft).toEqual({ name: 'New Profile 2' })
  })

  it('duplicates as a normalized fork named "copy"', () => {
    const { view, onSave } = setup([{ name: 'A', source: { url: 'https://x' }, rules: [1] }], {
      normalizeOnDuplicate: (p) => {
        const copy = { ...p }
        delete copy.source
        return copy
      },
    })
    act(() => view.result.current.duplicateProfile())
    const saved = onSave.mock.calls[0][0]
    expect(saved[1].name).toBe('A copy')
    expect(saved[1].rules).toEqual([1])
    expect(saved[1].source).toBeUndefined()
    expect(view.result.current.selected).toBe(1)
  })

  it('deletes, clamps the selection, and edits the survivor not the ghost', () => {
    const { view, onSave } = setup([{ name: 'A' }, { name: 'B' }, { name: 'C' }])
    act(() => view.result.current.selectProfile(2))
    act(() => view.result.current.deleteProfile(2))
    expect(onSave.mock.calls.at(-1)[0]).toEqual([{ name: 'A' }, { name: 'B' }])
    expect(view.result.current.selected).toBe(1)
    // The saved prop still carries the removed row at this point; the draft
    // must come from the pruned list, or the editor shows the deleted profile.
    expect(view.result.current.draft).toEqual({ name: 'B' })
  })

  it('never lets its own save echo clobber newer typing', () => {
    const { view, onSave, props } = setup([{ name: 'A' }])
    act(() => view.result.current.setDraft({ name: 'A', v: 1 }))
    act(() => vi.advanceTimersByTime(600))
    const echoed = onSave.mock.calls[0][0]

    // Newer typing lands while the save is still in flight…
    act(() => view.result.current.setDraft({ name: 'A', v: 2 }))
    // …and the echo arrives through the config broadcast.
    act(() => view.rerender({ ...props, profiles: echoed }))
    expect(view.result.current.draft).toEqual({ name: 'A', v: 2 })

    // The newer typing still saves on its own debounce.
    act(() => vi.advanceTimersByTime(600))
    expect(onSave).toHaveBeenCalledTimes(2)
    expect(onSave.mock.calls[1][0]).toEqual([{ name: 'A', v: 2 }])
  })

  it('adopts an external change once its own saves have settled', () => {
    const { view, props } = setup([{ name: 'A' }])
    // Another browser rewrote the profile; no local edit, no save in flight.
    act(() => view.rerender({ ...props, profiles: [{ name: 'A', v: 'external' }] }))
    expect(view.result.current.draft).toEqual({ name: 'A', v: 'external' })
  })
})
