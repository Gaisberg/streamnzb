// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, renderHook } from '@testing-library/react'
import { useStreamDraft } from '@/hooks/useStreamDraft'

const buildDraft = (stream) => ({ username: stream.username, results_mode: stream.results_mode || 'combined_stream' })

describe('useStreamDraft', () => {
  beforeEach(() => { vi.useFakeTimers({ shouldAdvanceTime: true }) })
  afterEach(() => { vi.useRealTimers(); cleanup() })

  const settle = async () => { await act(async () => { await Promise.resolve(); await Promise.resolve() }) }

  it('debounces edits into one save', async () => {
    const onSave = vi.fn().mockResolvedValue()
    const stream = { username: 'alice' }
    const { result } = renderHook(() => useStreamDraft({ stream, buildDraft, onSave, debounceMs: 500 }))

    act(() => { result.current.setDraft({ username: 'alice', results_mode: 'display_all' }) })
    act(() => { result.current.setDraft({ username: 'alice', results_mode: 'combined_stream' }) })
    expect(result.current.dirty).toBe(true)
    expect(onSave).not.toHaveBeenCalled()

    await act(async () => { vi.advanceTimersByTime(500) })
    await settle()
    expect(onSave).toHaveBeenCalledTimes(1)
    expect(onSave).toHaveBeenCalledWith('alice', { username: 'alice', results_mode: 'combined_stream' })
    expect(result.current.dirty).toBe(false)
  })

  // The timer belongs to the stream it was typed for. Firing it after the
  // selection moved would save one stream's settings onto the editor's view of
  // another — and cancelling it would silently drop the edit instead.
  it('writes a pending edit out against the stream it was typed for', async () => {
    const onSave = vi.fn().mockResolvedValue()
    const { result, rerender } = renderHook(
      ({ stream }) => useStreamDraft({ stream, buildDraft, onSave, debounceMs: 500 }),
      { initialProps: { stream: { username: 'alice' } } },
    )
    act(() => { result.current.setDraft({ username: 'alice', results_mode: 'display_all' }) })

    rerender({ stream: { username: 'bob' } })
    await settle()
    expect(onSave).toHaveBeenCalledTimes(1)
    expect(onSave).toHaveBeenCalledWith('alice', { username: 'alice', results_mode: 'display_all' })
    // ...and the editor now holds bob's own settings, not alice's edit.
    expect(result.current.draft.username).toBe('bob')
    expect(result.current.dirty).toBe(false)
  })

  it('surfaces per-field errors from a refused save', async () => {
    const err = Object.assign(new Error('Validation failed'), { fieldErrors: { indexers: 'pick one' } })
    const onSave = vi.fn().mockRejectedValue(err)
    const { result } = renderHook(() => useStreamDraft({ stream: { username: 'alice' }, buildDraft, onSave, debounceMs: 100 }))

    act(() => { result.current.setDraft({ username: 'alice', results_mode: 'display_all' }) })
    await act(async () => { vi.advanceTimersByTime(100) })
    await settle()
    expect(result.current.error).toBe('Validation failed')
    expect(result.current.fieldErrors).toEqual({ indexers: 'pick one' })
  })

  // An external refresh is welcome, but not over something half-typed.
  it('adopts external changes only when nothing is in hand', async () => {
    const onSave = vi.fn().mockResolvedValue()
    const { result, rerender } = renderHook(
      ({ stream }) => useStreamDraft({ stream, buildDraft, onSave, debounceMs: 500 }),
      { initialProps: { stream: { username: 'alice', results_mode: 'combined_stream' } } },
    )
    rerender({ stream: { username: 'alice', results_mode: 'display_all' } })
    expect(result.current.draft.results_mode).toBe('display_all')

    act(() => { result.current.setDraft({ username: 'alice', results_mode: 'combined_stream' }) })
    rerender({ stream: { username: 'alice', results_mode: 'display_all' } })
    expect(result.current.draft.results_mode).toBe('combined_stream')
  })
})
