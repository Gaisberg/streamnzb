// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fetchShareCodeText } from '@/lib/shareCodes'

const jsonResponse = (status, body) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('fetchShareCodeText', () => {
  it('reads the URL directly when the browser can reach it', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response('SNZBP1:direct'))
    vi.stubGlobal('fetch', fetchMock)
    await expect(fetchShareCodeText('https://example.com/p.txt')).resolves.toBe('SNZBP1:direct')
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('falls back to the server relay when the browser fetch fails', async () => {
    const fetchMock = vi.fn()
      .mockRejectedValueOnce(new TypeError('Failed to fetch'))
      .mockResolvedValueOnce(jsonResponse(200, { text: 'SNZBP1:relayed' }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(fetchShareCodeText('https://example.com/p.txt')).resolves.toBe('SNZBP1:relayed')
    const [path, options] = fetchMock.mock.calls[1]
    expect(path).toMatch(/\/api\/share\/fetch$/)
    expect(JSON.parse(options.body)).toEqual({ url: 'https://example.com/p.txt' })
  })

  it("shows the relay's reason when the server could not fetch it either", async () => {
    vi.stubGlobal('fetch', vi.fn()
      .mockRejectedValueOnce(new TypeError('Failed to fetch'))
      .mockResolvedValueOnce(jsonResponse(502, { error: 'The URL answered HTTP 404.' })))
    await expect(fetchShareCodeText('https://example.com/p.txt')).rejects.toThrow('The URL answered HTTP 404.')
  })

  it('keeps the browser message when the relay itself is unavailable', async () => {
    vi.stubGlobal('fetch', vi.fn()
      .mockRejectedValueOnce(new TypeError('Failed to fetch'))
      .mockResolvedValueOnce(jsonResponse(403, { error: 'Only admin can fetch share codes' })))
    await expect(fetchShareCodeText('https://example.com/p.txt')).rejects.toThrow(/CORS/)
  })
})
