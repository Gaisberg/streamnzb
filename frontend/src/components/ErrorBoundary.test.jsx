// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { useState } from 'react'
import ErrorBoundary from '@/components/ErrorBoundary'

function Boom({ shouldThrow = true }) {
  if (shouldThrow) throw new Error('the page exploded')
  return <div>page content</div>
}

describe('ErrorBoundary', () => {
  beforeEach(() => {
    // React logs the caught error itself, and the boundary logs it again on
    // purpose. Neither belongs in the test output.
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })

  afterEach(cleanup)

  it('renders its children when nothing throws', () => {
    render(
      <ErrorBoundary label="dashboard">
        <Boom shouldThrow={false} />
      </ErrorBoundary>,
    )
    expect(screen.getByText('page content')).toBeInTheDocument()
  })

  it('shows the failure instead of a blank page, and names what broke', () => {
    render(
      <ErrorBoundary label="dashboard">
        <Boom />
      </ErrorBoundary>,
    )
    expect(screen.getByText(/Something broke in dashboard/)).toBeInTheDocument()
    expect(screen.getByText('the page exploded')).toBeInTheDocument()
    // A blank admin UI reads as "the server died", so the fallback says otherwise.
    expect(screen.getByText(/server/i)).toBeInTheDocument()
  })

  it('leaves everything outside it mounted', () => {
    // This is the whole point of a boundary per page rather than only one at
    // the root: the sidebar and header have to survive a page that throws.
    render(
      <div>
        <nav>sidebar</nav>
        <ErrorBoundary label="dashboard">
          <Boom />
        </ErrorBoundary>
      </div>,
    )
    expect(screen.getByText('sidebar')).toBeInTheDocument()
    expect(screen.getByText(/Something broke/)).toBeInTheDocument()
  })

  it('offers a reload the caller can handle', () => {
    const onReload = vi.fn()
    render(
      <ErrorBoundary label="dashboard" onReload={onReload}>
        <Boom />
      </ErrorBoundary>,
    )
    screen.getByRole('button', { name: /reload/i }).click()
    expect(onReload).toHaveBeenCalledTimes(1)
  })

  it('lands back on the fallback when a retry hits the same failure', () => {
    function Flaky() {
      const [broken] = useState(true)
      return <Boom shouldThrow={broken} />
    }

    render(
      <ErrorBoundary label="dashboard">
        <Flaky />
      </ErrorBoundary>,
    )
    expect(screen.getByText(/Something broke/)).toBeInTheDocument()

    // Retrying while the cause persists must not escape to a blank page.
    screen.getByRole('button', { name: /try again/i }).click()
    expect(screen.getByText(/Something broke/)).toBeInTheDocument()
  })

  it('says something useful when the thrown value has no message', () => {
    function ThrowString() {
      throw 'just a string'
    }
    render(
      <ErrorBoundary>
        <ThrowString />
      </ErrorBoundary>,
    )
    expect(screen.getByText('just a string')).toBeInTheDocument()
  })
})
