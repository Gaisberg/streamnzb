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
})
