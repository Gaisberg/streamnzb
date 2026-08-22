// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { UNAUTHORIZED_EVENT, apiFetch, getApiUrl } from '@/api'

function jsonResponse(body, { ok = true, status = 200 } = {}) {
  return {
    ok,
    status,
    statusText: 'Status Text',
    headers: new Headers({ 'content-type': 'application/json' }),
    json: async () => body,
    text: async () => JSON.stringify(body),
  }
}

function textResponse(body, { status = 400 } = {}) {
  return {
    ok: false,
    status,
    statusText: 'Bad Request',
    headers: new Headers({ 'content-type': 'text/plain' }),
    json: async () => { throw new Error('not json') },
    text: async () => body,
  }
}

describe('apiFetch', () => {
  beforeEach(() => {
    window.localStorage.clear()
    globalThis.fetch = vi.fn()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('returns the parsed body on success', async () => {
    globalThis.fetch.mockResolvedValue(jsonResponse({ hello: 'world' }))
    await expect(apiFetch('/api/thing')).resolves.toEqual({ hello: 'world' })
  })

  it('authenticates with the cookie and sends no credential of its own', async () => {
    // Anything readable from JS is readable by an XSS, so the browser keeps no
    // copy of the token: the HttpOnly cookie is the whole credential.
    window.localStorage.setItem('auth_token', 'left-over-from-an-old-version')
    globalThis.fetch.mockResolvedValue(jsonResponse({}))

    await apiFetch('/api/thing')

    const [, options] = globalThis.fetch.mock.calls[0]
    expect(options.credentials).toBe('include')
    expect(options.headers.get('Authorization')).toBeNull()
  })

  it('still sends an Authorization header the caller set explicitly', async () => {
    globalThis.fetch.mockResolvedValue(jsonResponse({}))

    await apiFetch('/api/thing', { headers: { Authorization: 'Bearer caller' } })

    const [, options] = globalThis.fetch.mock.calls[0]
    expect(options.headers.get('Authorization')).toBe('Bearer caller')
  })

  it('raises the server error message rather than the bare status line', async () => {
    globalThis.fetch.mockResolvedValue(jsonResponse({ error: 'Provider unreachable' }, { ok: false, status: 400 }))
    await expect(apiFetch('/api/thing')).rejects.toThrow('Provider unreachable')
  })

  it('reads a text/plain error body, which is what http.Error sends', async () => {
    globalThis.fetch.mockResolvedValue(textResponse('port already in use'))
    await expect(apiFetch('/api/thing')).rejects.toThrow('port already in use')
  })

  it('falls back to the status line for an HTML error page from a proxy', async () => {
    // A gateway's HTML would be noise in a toast, so it is dropped.
    globalThis.fetch.mockResolvedValue(textResponse('<html><body>502</body></html>', { status: 502 }))
    await expect(apiFetch('/api/thing')).rejects.toThrow('Bad Request')
  })

  it('carries field errors and the status onto the thrown error', async () => {
    globalThis.fetch.mockResolvedValue(
      jsonResponse({ error: 'Invalid', errors: { addon_port: 'in use' } }, { ok: false, status: 422 }),
    )
    await expect(apiFetch('/api/thing')).rejects.toMatchObject({
      status: 422,
      fieldErrors: { addon_port: 'in use' },
    })
  })

  it('announces a 401 so the shell can send the user back to the login screen', async () => {
    const seen = vi.fn()
    window.addEventListener(UNAUTHORIZED_EVENT, seen)
    globalThis.fetch.mockResolvedValue(jsonResponse({ error: 'Unauthorized' }, { ok: false, status: 401 }))

    await expect(apiFetch('/api/thing')).rejects.toThrow()

    expect(seen).toHaveBeenCalledTimes(1)
    expect(seen.mock.calls[0][0].detail).toMatchObject({ path: '/api/thing', status: 401 })
    window.removeEventListener(UNAUTHORIZED_EVENT, seen)
  })

  it('stays quiet on a 401 the caller expects', async () => {
    // The auth check probes on purpose and must not trigger a logout of itself.
    const seen = vi.fn()
    window.addEventListener(UNAUTHORIZED_EVENT, seen)
    globalThis.fetch.mockResolvedValue(jsonResponse({}, { ok: false, status: 401 }))

    await expect(apiFetch('/api/auth/check', { skipAuthNotify: true })).rejects.toThrow()

    expect(seen).not.toHaveBeenCalled()
    window.removeEventListener(UNAUTHORIZED_EVENT, seen)
  })
})

describe('getApiUrl', () => {
  it('keeps the mount prefix when the UI is served under a sub-path', () => {
    window.history.replaceState({}, '', '/streamnzb/dashboard')
    expect(getApiUrl('/api/config')).toBe('/streamnzb/api/config')

    window.history.replaceState({}, '', '/')
    expect(getApiUrl('/api/config')).toBe('/api/config')
  })
})
