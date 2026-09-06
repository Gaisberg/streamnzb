// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { ComponentHealthNotice } from '@/components/ComponentHealth'

describe('ComponentHealthNotice', () => {
  afterEach(cleanup)

  // The label and hint are our reading of the server; the detail is the
  // server's own line. Only the latter can contradict a wrong reading, so the
  // dashboard has to show it, not just the settings tooltip.
  it('shows the server line alongside the interpreted reason', () => {
    render(
      <ComponentHealthNotice
        record={{
          kind: 'provider',
          name: 'eweka',
          state: 'blocked',
          reason: 'auth_failed',
          detail: '502 "Authentication Failed"',
          since: new Date().toISOString(),
        }}
      />
    )
    expect(screen.getByText('Credentials rejected')).toBeTruthy()
    expect(screen.getByText('502 "Authentication Failed"')).toBeTruthy()
  })

  it('renders nothing for a healthy component', () => {
    const { container } = render(<ComponentHealthNotice record={{ state: 'ok' }} />)
    expect(container.innerHTML).toBe('')
  })
})
