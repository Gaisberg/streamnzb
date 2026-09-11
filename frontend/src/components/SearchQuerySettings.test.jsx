// @vitest-environment jsdom
import { useState } from 'react'
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { TooltipProvider } from '@/components/ui/tooltip'
import { QueryDraftFields } from '@/components/SearchQuerySettings'
import { presetPlan } from '@/lib/searchPlan'

function Editor({ kind = 'series', plan = presetPlan(kind, 'balanced') }) {
  const [draft, setDraft] = useState({ name: 'TVQuery01', ...plan })
  return (
    <TooltipProvider>
      <QueryDraftFields kind={kind} draft={draft} setDraft={setDraft} />
    </TooltipProvider>
  )
}

const addressSelects = () => screen.getAllByLabelText(/address$/)
const targetSelects = () => screen.getAllByLabelText(/target$/)

describe('attempt rows', () => {
  afterEach(cleanup)

  // The reported bug: on a plan that already asks the default question, "Add
  // attempt" added a row that was deduped away before it could be rendered,
  // so the button looked dead.
  it('adds a row for a question the plan does not already ask', () => {
    render(<Editor />)
    expect(addressSelects()).toHaveLength(5)

    fireEvent.click(screen.getByText('Add attempt'))

    expect(addressSelects()).toHaveLength(6)
    expect(addressSelects()[5].value).toBe('id')
    expect(targetSelects()[5].value).toBe('series')
  })

  // The other half: editing a row into one the plan already asks used to make
  // the row vanish. It stays, and says what it collides with.
  it('keeps a row that ends up asking what an earlier row asks, and marks it', () => {
    render(<Editor />)
    // Row 3 is Title · Episode; switching it to id repeats row 1.
    fireEvent.change(addressSelects()[2], { target: { value: 'id' } })

    expect(addressSelects()).toHaveLength(5)
    expect(addressSelects()[2].value).toBe('id')
    expect(screen.getByText('same as #1')).toBeTruthy()
  })

  // Row 2 of the preset is Title · Absolute, and an id request cannot ask
  // under the absolute number — the target select disables that pairing.
  it('moves an absolute row off the absolute number when it switches to id', () => {
    render(<Editor />)
    fireEvent.change(addressSelects()[1], { target: { value: 'id' } })

    expect(targetSelects()[1].value).toBe('episode')
  })

  it('removes the row it is asked to remove', () => {
    render(<Editor />)
    fireEvent.click(screen.getByLabelText('Remove attempt 1'))

    expect(addressSelects()).toHaveLength(4)
    expect(addressSelects()[0].value).toBe('title')
  })
})
