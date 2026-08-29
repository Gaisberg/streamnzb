// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, renderHook } from '@testing-library/react'
import { useEntityDialog } from '@/hooks/useEntityDialog'

// The chrome shared by the indexer/provider/query dialogs. The behaviours
// worth pinning are the ones a retrofit could silently lose: validation
// gating the request, the dirty check driving the discard confirm, and the
// reseed on reopen.

const normalize = (value) => ({ name: (value?.name || '').trim() })

function setup(props = {}) {
  const onOpenChange = vi.fn()
  const onClearStatus = vi.fn()
  const initialValue = props.initialValue ?? { name: 'A' }
  const view = renderHook((p) => useEntityDialog(p), {
    initialProps: {
      open: true,
      onOpenChange,
      onClearStatus,
      initialValue,
      makeDraft: () => normalize(initialValue),
      normalize,
      ...props,
    },
  })
  return { view, onOpenChange, onClearStatus }
}

describe('useEntityDialog', () => {
  afterEach(cleanup)

  it('a failed validation blocks the commit and names the first complaint', async () => {
    const { view } = setup()
    const commit = vi.fn()
    await act(() => view.result.current.runSave({
      validate: () => ({ name: 'Name is required', url: 'URL is required' }),
      commit,
    }))
    expect(commit).not.toHaveBeenCalled()
    expect(view.result.current.saveError).toBe('Name is required')
    expect(view.result.current.fieldErrors.url).toBe('URL is required')
  })

  it('a clean save commits and closes', async () => {
    const { view, onOpenChange } = setup()
    await act(() => view.result.current.runSave({
      validate: () => ({}),
      commit: () => Promise.resolve(),
    }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
    expect(view.result.current.saveError).toBe('')
  })

  it('a refused save maps field errors and falls back through raw errors to the message', async () => {
    const { view, onOpenChange } = setup()
    const refusal = Object.assign(new Error('Validation failed'), {
      fieldErrors: { 'indexers.0.url': 'URL is unreachable' },
    })
    await act(() => view.result.current.runSave({
      commit: () => Promise.reject(refusal),
      mapError: (error) => (error.fieldErrors['indexers.0.url'] ? { url: 'URL is unreachable' } : {}),
    }))
    expect(onOpenChange).not.toHaveBeenCalledWith(false)
    expect(view.result.current.fieldErrors.url).toBe('URL is unreachable')
    expect(view.result.current.saveError).toBe('URL is unreachable')

    // Unmapped: the raw field error still beats the generic message.
    await act(() => view.result.current.runSave({
      commit: () => Promise.reject(refusal),
      mapError: () => ({}),
    }))
    expect(view.result.current.saveError).toBe('URL is unreachable')
  })

  it('a dirty close asks to discard; a clean close just closes', () => {
    const { view, onOpenChange, onClearStatus } = setup()
    act(() => view.result.current.requestClose())
    expect(onOpenChange).toHaveBeenCalledWith(false)
    expect(onClearStatus).toHaveBeenCalled()

    onOpenChange.mockClear()
    act(() => view.result.current.update('name', 'B'))
    act(() => view.result.current.requestClose())
    expect(onOpenChange).not.toHaveBeenCalled()
    expect(view.result.current.showDiscardConfirm).toBe(true)
    act(() => view.result.current.confirmDiscard())
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it('reopening reseeds the draft and clears the errors', () => {
    const { view } = setup({ open: false })
    act(() => view.result.current.update('name', 'edited'))
    act(() => view.result.current.setSaveError('stale'))
    view.rerender({
      open: true,
      onOpenChange: vi.fn(),
      initialValue: { name: 'A' },
      makeDraft: () => ({ name: 'A' }),
      normalize,
    })
    expect(view.result.current.draft).toEqual({ name: 'A' })
    expect(view.result.current.saveError).toBe('')
  })
})
