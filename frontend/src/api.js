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

export async function apiFetch(path, options = {}) {
  const url = getApiUrl(path)
  const headers = new Headers(options.headers || {})
  const storedToken = typeof window !== 'undefined' ? window.localStorage.getItem('auth_token') || '' : ''
  if (storedToken && !headers.has('Authorization')) {
    headers.set('Authorization', `Bearer ${storedToken}`)
  }
  const res = await fetch(url, { credentials: 'include', ...options, headers })
  let data = null
  const contentType = res.headers.get('content-type')
  if (contentType && contentType.includes('application/json')) {
    try {
      data = await res.json()
    } catch (_) {}
  }
  if (!res.ok) {
    if (res.status === 401) {
      notifyUnauthorized({ path, status: res.status })
    }
    const err = new Error((data && (data.error || data.message)) || res.statusText)
    if (data && data.errors) err.fieldErrors = data.errors
    err.status = res.status
    throw err
  }
  return data
}
