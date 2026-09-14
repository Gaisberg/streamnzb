// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { RulesEditor } from '@/components/RulesEditor'

const TIERS = [
  { name: 'Trusted UHD', when: 'group == "GRP"', points: 100 },
  { name: 'Probe rule', when: 'probed.height >= 2000', points: 50 },
]

describe('RulesEditor rule references', () => {
  afterEach(cleanup)

  // The reference panel is the answer to "what can I write in here": without
  // the rule names in it, matched() is a feature you have to read the docs to
  // discover.
  it('offers each named rule as something to insert', () => {
    const onChange = vi.fn()
    render(<RulesEditor values={[...TIERS, { name: 'Uses it', when: '' }]} onChange={onChange} />)

    fireEvent.click(screen.getByText('What a rule can read'))
    fireEvent.click(screen.getByTitle('matched("Trusted UHD")'))

    expect(onChange).toHaveBeenCalledTimes(1)
    // With no condition focused the insert lands in the last rule, which is
    // the one just added.
    expect(onChange.mock.calls[0][0][2].when).toBe('matched("Trusted UHD")')
  })

  // A reference carries the referenced rule's tier, so the chip beside a rule
  // has to follow one rather than read only what is typed in front of it.
  it('shows the tier a reference pulls in', () => {
    render(<RulesEditor values={[...TIERS, { name: 'Uses it', when: 'matched("Probe rule")' }]} onChange={() => {}} />)

    expect(screen.getAllByText('measured')).toHaveLength(2)
  })

  it('rewrites references when the rule they name is renamed', () => {
    const onChange = vi.fn()
    const values = [...TIERS, { name: 'Uses it', when: 'not matched("Trusted UHD")' }]
    render(<RulesEditor values={values} onChange={onChange} />)

    fireEvent.change(screen.getAllByLabelText('Rule name')[0], { target: { value: 'Trusted 4K' } })

    const next = onChange.mock.calls[0][0]
    expect(next[0].name).toBe('Trusted 4K')
    expect(next[2].when).toBe('not matched("Trusted 4K")')
  })

  // The text box is seeded once on the way in. Switching to another profile
  // while in text mode has to reseed it — the old rules staying on screen is
  // an editor lying about what it edits — but the box's own rules echoing back
  // from the parent must not, or every keystroke would be reformatted.
  describe('text mode', () => {
    const showText = () => fireEvent.click(screen.getByRole('button', { name: 'Text' }))
    const box = () => screen.getByLabelText('All rules as text')

    it('follows a profile switch', () => {
      const { rerender } = render(<RulesEditor values={TIERS} onChange={() => {}} />)
      showText()
      expect(box().value).toContain('Trusted UHD: score 100')

      const other = [{ name: 'Remux only', when: 'remux', points: 5 }]
      rerender(<RulesEditor values={other} onChange={() => {}} />)
      expect(box().value).toBe('Remux only: score 5 if remux')
    })

    it('keeps what was typed when its own rules come back round', () => {
      const onChange = vi.fn()
      const { rerender } = render(<RulesEditor values={TIERS} onChange={onChange} />)
      showText()
      // Typed with a double space: parses fine, but the canonical text differs.
      const typed = 'Trusted UHD: score 100 if group  == "GRP"'
      fireEvent.change(box(), { target: { value: typed } })
      rerender(<RulesEditor values={onChange.mock.calls[0][0]} onChange={onChange} />)
      expect(box().value).toBe(typed)
    })
  })
})
