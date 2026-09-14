// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { SelectionSection } from '@/components/ui/selection-section'

const LABELS = { tvdb: 'TVDB', tmdb: 'TMDB', cinemeta: 'Cinemeta' }

function renderSection(props = {}) {
  const onToggle = vi.fn()
  const onMove = vi.fn()
  render(
    <SelectionSection
      title="Series"
      values={['tvdb', 'tmdb', 'cinemeta']}
      selected={['tvdb', 'tmdb']}
      renderLabel={(value) => LABELS[value] || value}
      onToggle={onToggle}
      onMove={onMove}
      {...props}
    />
  )
  return { onToggle, onMove }
}

describe('SelectionSection', () => {
  afterEach(cleanup)

  // The Streams page lists raw names; the metadata sources list shows display
  // names for the same values, which is the whole point of renderLabel.
  it('shows the display label for each selected value, in the stored order', () => {
    renderSection()

    const rows = screen.getAllByText(/^(TVDB|TMDB|Cinemeta)$/)
    expect(rows.map((row) => row.textContent)).toEqual(['TVDB', 'TMDB'])
  })

  it('offers only the values that are not in the list yet', () => {
    const { onToggle } = renderSection()

    fireEvent.keyDown(screen.getByRole('button', { name: 'Add series' }), { key: 'Enter' })
    expect(screen.queryByRole('menuitem', { name: 'TVDB' })).toBeNull()
    fireEvent.click(screen.getByRole('menuitem', { name: 'Cinemeta' }))

    expect(onToggle).toHaveBeenCalledWith('cinemeta', true)
  })

  it('removes a row through the same toggle', () => {
    const { onToggle } = renderSection()

    fireEvent.click(screen.getAllByRole('button', { name: 'Remove' })[1])

    expect(onToggle).toHaveBeenCalledWith('tmdb', false)
  })

  // A metadata source list must keep one entry, so the control stays visible
  // and explains itself rather than vanishing.
  it('locks removal once the list is down to its minimum', () => {
    const { onToggle } = renderSection({
      selected: ['tvdb'],
      minSelected: 1,
      minSelectedReason: 'Series needs at least one source.',
    })

    const remove = screen.getByRole('button', { name: 'Remove' })
    expect(remove).toBeDisabled()
    expect(remove).toHaveAttribute('title', 'Series needs at least one source.')

    fireEvent.click(remove)
    expect(onToggle).not.toHaveBeenCalled()
  })

  it('leaves removal alone when no minimum is set', () => {
    renderSection({ selected: ['tvdb'] })

    expect(screen.getByRole('button', { name: 'Remove' })).toBeEnabled()
  })
})
