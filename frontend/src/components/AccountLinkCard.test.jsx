// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { AccountLinkCard } from '@/components/AccountLinkCard'
import { apiFetch } from '@/api'

vi.mock('@/api', () => ({ apiFetch: vi.fn() }))

const endpoints = {
  status: '/api/x/status',
  start: '/api/x/device',
  check: '/api/x/device/check',
  disconnect: '/api/x/disconnect',
}

const deviceCode = {
  user_code: 'WDJB-MJHT',
  verification_uri: 'https://mdblist.com/oauth/device/',
  verification_uri_complete: 'https://mdblist.com/oauth/device/?user_code=WDJB-MJHT',
  expires_in: 300,
  interval: 5,
}

// route answers each endpoint from a table, so a test only states the replies
// it cares about and the poll can change its mind between ticks. The ?stream=
// the card appends is stripped before matching — which stream it names is
// asserted separately.
const route = (table) => {
  apiFetch.mockImplementation((path) => {
    const reply = table[path.split('?')[0]]
    if (typeof reply === 'function') return reply()
    return Promise.resolve(reply)
  })
}

const renderCard = (props = {}) =>
  render(<AccountLinkCard name="MDBList" stream="alice" description="desc" endpoints={endpoints} {...props} />)

// Fake timers and waitFor race each other, so the flushing is explicit: a
// settle drains the promise chain a poll leaves behind, and a tick fires the
// next interval and drains what it queued.
const settle = async () => {
  await act(async () => {
    for (let i = 0; i < 4; i += 1) await Promise.resolve()
  })
}
const tick = async (ms) => {
  await act(async () => { vi.advanceTimersByTime(ms) })
  await settle()
}

describe('AccountLinkCard', () => {
  beforeEach(() => { vi.useFakeTimers({ shouldAdvanceTime: true }) })
  afterEach(() => { vi.useRealTimers(); vi.clearAllMocks(); cleanup() })

  it('links the account once the service confirms', async () => {
    let connected = false
    route({
      [endpoints.status]: { enabled: true, connected: false },
      [endpoints.start]: deviceCode,
      [endpoints.check]: () =>
        Promise.resolve(
          connected
            ? { connected: true, status: { enabled: true, connected: true, user_name: 'someone' } }
            : { connected: false },
        ),
    })
    renderCard()
    await settle()

    fireEvent.click(screen.getByRole('button', { name: /connect/i }))
    await settle()
    expect(screen.getByText('WDJB-MJHT')).toBeTruthy()
    // The href carries the pre-filled code; the label is the bare page.
    const link = screen.getByRole('link', { name: 'mdblist.com/oauth/device' })
    expect(link.getAttribute('href')).toBe(deviceCode.verification_uri_complete)

    // Still pending: the dialog stays up.
    await tick(5000)
    expect(screen.queryByText('WDJB-MJHT')).not.toBeNull()

    connected = true
    await tick(5000)
    expect(screen.getByText(/Connected as someone/)).toBeTruthy()
  })

  // MDBList reports a denial or an expired code as a failed poll, so polling
  // has to stop and say so rather than retrying until the code times out.
  it('halts on a terminal poll failure when told to', async () => {
    route({
      [endpoints.status]: { enabled: true, connected: false },
      [endpoints.start]: deviceCode,
      [endpoints.check]: () => Promise.reject(new Error('authorization was denied')),
    })
    renderCard({ haltOnCheckError: true })
    await settle()
    fireEvent.click(screen.getByRole('button', { name: /connect/i }))
    await settle()

    await tick(5000)
    expect(screen.getByText('authorization was denied')).toBeTruthy()
    expect(screen.queryByText('WDJB-MJHT')).toBeNull()

    const calls = apiFetch.mock.calls.filter(([path]) => path === endpoints.check).length
    await tick(30000)
    expect(apiFetch.mock.calls.filter(([path]) => path === endpoints.check).length).toBe(calls)
  })

  // Simkl's poll only ever fails transiently; stopping on a blink of the
  // network would strand a link that was about to succeed.
  it('keeps polling through a transient poll failure by default', async () => {
    let fail = true
    route({
      [endpoints.status]: { enabled: true, connected: false },
      [endpoints.start]: deviceCode,
      [endpoints.check]: () =>
        fail
          ? Promise.reject(new Error('network'))
          : Promise.resolve({ connected: true, status: { enabled: true, connected: true } }),
    })
    renderCard()
    await settle()
    fireEvent.click(screen.getByRole('button', { name: /connect/i }))
    await settle()

    await tick(5000)
    expect(screen.queryByText('network')).toBeNull()
    expect(screen.queryByText('WDJB-MJHT')).not.toBeNull()

    fail = false
    await tick(5000)
    expect(screen.getByText(/Connected\./)).toBeTruthy()
  })

  it('names the stream it acts for on every call', async () => {
    route({
      [endpoints.status]: { enabled: true, connected: false },
      [endpoints.start]: deviceCode,
      [endpoints.check]: { connected: false },
    })
    renderCard()
    await settle()
    fireEvent.click(screen.getByRole('button', { name: /connect/i }))
    await settle()
    await tick(5000)
    const paths = apiFetch.mock.calls.map(([path]) => path)
    expect(paths.length).toBeGreaterThan(2)
    paths.forEach((path) => expect(path).toContain('?stream=alice'))
  })

  // Without a stream there is no account to act on, so the card must not ask
  // the server about one — a blank stream would read as "some other account".
  it('stays inert with no stream', async () => {
    route({ [endpoints.status]: { enabled: true, connected: true, user_name: 'someone' } })
    render(<AccountLinkCard name="MDBList" description="desc" endpoints={endpoints} />)
    await settle()
    expect(apiFetch).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: /connect/i }).disabled).toBe(true)
  })

  // The scrobble switch rides inside the card as children: with nothing linked
  // there is no account to report into, so it must not be offered at all.
  it('shows connected-only content only once linked', async () => {
    let connected = false
    route({
      [endpoints.status]: { enabled: true, connected: false },
      [endpoints.start]: deviceCode,
      [endpoints.check]: () =>
        Promise.resolve(
          connected
            ? { connected: true, status: { enabled: true, connected: true, user_name: 'someone' } }
            : { connected: false },
        ),
    })
    renderCard({ children: <p>scrobble switch</p> })
    await settle()
    expect(screen.queryByText('scrobble switch')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: /connect/i }))
    await settle()
    connected = true
    await tick(5000)
    expect(screen.getByText('scrobble switch')).toBeTruthy()
  })

  it('offers no Connect button without a client id', async () => {
    route({ [endpoints.status]: { enabled: false, connected: false } })
    renderCard({ notEnabled: <p>register an app first</p> })
    await settle()
    expect(screen.getByText('register an app first')).toBeTruthy()
    expect(screen.getByRole('button', { name: /connect/i }).disabled).toBe(true)
  })

  it('unlinks and stops naming the account', async () => {
    const onAccountChange = vi.fn()
    route({
      [endpoints.status]: { enabled: true, connected: true, user_name: 'someone' },
      [endpoints.disconnect]: { enabled: true, connected: false },
    })
    renderCard({ onAccountChange, children: <p>scrobble switch</p> })
    await settle()
    expect(screen.getByText(/Connected as someone/)).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: /disconnect/i }))
    await settle()
    expect(screen.getByRole('button', { name: /connect/i })).toBeTruthy()
    expect(screen.queryByText(/Connected as someone/)).toBeNull()
    // ...and the switch goes with it.
    expect(screen.queryByText('scrobble switch')).toBeNull()
    expect(onAccountChange).toHaveBeenCalled()
  })
})
