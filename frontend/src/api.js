export function getApiUrl(path) {
  const base = window.location.pathname.split('/').filter(Boolean)[0]
  const prefix = base && base !== 'api' ? `/${base}` : ''
  return `${prefix}${path}`
}

export const UNAUTHORIZED_EVENT = 'streamnzb:unauthorized'

export function notifyUnauthorized(detail = {}) {
  if (typeof window === 'undefined') return
  window.dispatchEvent(new CustomEvent(UNAUTHORIZED_EVENT, { detail }))
}

const MAX_TEXT_ERROR_LENGTH = 300

// readTextError pulls a plain-text error body out of a failed response.
// Proxies and gateways answer with HTML pages that would be noise in an error
// toast, so those fall back to the caller's status-line default.
async function readTextError(res) {
  let body = ''
  try {
    body = (await res.text()).trim()
  } catch {
    return ''
  }
  if (!body || body.startsWith('<')) return ''
  return body.length > MAX_TEXT_ERROR_LENGTH
    ? `${body.slice(0, MAX_TEXT_ERROR_LENGTH)}…`
    : body
}

export async function apiFetch(path, options = {}) {
  const url = getApiUrl(path)
  const headers = new Headers(options.headers || {})
  const storedToken = typeof window !== 'undefined' ? window.localStorage.getItem('auth_token') || '' : ''
  if (storedToken && !headers.has('Authorization')) {
    headers.set('Authorization', `Bearer ${storedToken}`)
  }
  const res = await fetch(url, { credentials: 'include', ...options, headers })
  let data = null
  let textError = ''
  const contentType = res.headers.get('content-type')
  if (contentType && contentType.includes('application/json')) {
    try {
      data = await res.json()
    } catch {
      data = null
    }
  } else if (!res.ok) {
    // Go's http.Error replies as text/plain. Without reading it the UI would
    // only ever show the bare status line ("Bad Request") and drop the reason.
    textError = await readTextError(res)
  }
  if (!res.ok) {
    if (res.status === 401 && !options.skipAuthNotify) {
      notifyUnauthorized({ path, status: res.status })
    }
    const err = new Error((data && (data.error || data.message)) || textError || res.statusText)
    if (data && data.errors) err.fieldErrors = data.errors
    err.status = res.status
    throw err
  }
  return data
}
