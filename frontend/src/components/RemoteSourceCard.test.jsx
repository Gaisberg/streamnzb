// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { RemoteSourceCard } from '@/components/RemoteSourceCard'
import { checkForUpdate, diffLinkedProfiles, mergeUpstream } from '@/lib/remoteProfiles'

// Only the fetch-and-decode half is stubbed — jsdom has no Blob.stream() for
// the share-code encoder, and what the dialog does with an update is the
// point. The merge and the diff it works on are the real ones.
vi.mock('@/lib/remoteProfiles', async (importOriginal) => ({
  ...(await importOriginal()),
  checkForUpdate: vi.fn(),
}))

const rule = (name, points) => ({ name, when: 'true', points })

// The whole point of the diff dialog is that the user decides: what the
// checkboxes drive is the merge itself, not a second version of it.
describe('RemoteSourceCard update dialog', () => {
  afterEach(cleanup)

  // upstream raises one rule and adds another; locally there is an edit of the
  // first and a rule of the user's own.
  const upstream = { name: 'Community', preset: '4k', rules: [rule('Shared', 900), rule('Fresh', 100)] }
  const code = 'SNZBP1:upstream'
  const openUpdate = async (onChange) => {
    const profile = {
      name: 'Mine',
      preset: '4k',
      rules: [rule('Shared', 1), rule('My own', 7)],
      source: { url: 'https://example.com/p.txt', code: 'SNZBP1:snapshot' },
    }
    const { profile: merged, keptLocal } = mergeUpstream(profile, upstream, { rules: [rule('Shared', 1)] })
    checkForUpdate.mockResolvedValue({
      status: 'update',
      code,
      merged,
      keptLocal,
      diff: diffLinkedProfiles(profile, merged),
      remoteName: upstream.name,
    })
    render(<RemoteSourceCard profile={profile} onChange={onChange} />)
    fireEvent.click(screen.getByRole('button', { name: /Refresh/ }))
    await screen.findByText('Update from example.com')
  }

  it('applies only the ticked changes and still records upstream in full', async () => {
    const onChange = vi.fn()
    await openUpdate(onChange)

    // One box per change — the updated rule, then the added one — all ticked.
    const boxes = screen.getAllByRole('checkbox')
    expect(boxes).toHaveLength(2)
    expect(screen.getByText('2 of 2 changes selected')).toBeTruthy()

    fireEvent.click(boxes[1])
    expect(screen.getByText('1 of 2 changes selected')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: /Apply 1 change/ }))

    expect(onChange).toHaveBeenCalledTimes(1)
    const applied = onChange.mock.calls[0][0]
    // The update to "Shared" landed; "Fresh" was left behind; the user's own
    // rule is untouched.
    expect(applied.rules).toEqual([rule('Shared', 900), rule('My own', 7)])
    // The snapshot is upstream in full even though half of it was skipped —
    // it records what upstream is, not what was taken.
    expect(applied.source.code).toBe(code)
    expect(screen.getByText(/Applied 1 of 2 changes/)).toBeTruthy()
  })

  it('will not apply an empty selection', async () => {
    const onChange = vi.fn()
    await openUpdate(onChange)

    fireEvent.click(screen.getByRole('button', { name: 'Select none' }))
    expect(screen.getByRole('button', { name: /Apply/ }).disabled).toBe(true)

    fireEvent.click(screen.getByRole('button', { name: 'Select all' }))
    fireEvent.click(screen.getByRole('button', { name: 'Apply update' }))
    expect(onChange.mock.calls[0][0].rules).toEqual([rule('Shared', 900), rule('Fresh', 100), rule('My own', 7)])
  })
})
