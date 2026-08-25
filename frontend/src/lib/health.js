// Component health: how the backend's verdict on an indexer or provider is
// phrased in the UI.
//
// The backend sends stable reason codes rather than sentences, so the copy
// lives here and stays consistent wherever a component is shown — the badge on
// a settings card and the dashboard panel must never describe the same state
// two different ways.

export const HEALTH_STATE_BLOCKED = 'blocked'
export const HEALTH_STATE_DEGRADED = 'degraded'

const REASON_COPY = {
  auth_failed: {
    label: 'Credentials rejected',
    hint: 'The server refused this account. Check the password or API key, or whether the subscription is still active.',
  },
  quota_exhausted: {
    label: 'Quota spent',
    hint: 'The daily budget is used up. It clears on its own when the allowance resets.',
  },
  throttled: {
    label: 'Rate limited',
    hint: 'The server asked us to back off. It clears on its own once the cooldown ends.',
  },
  connection_limit: {
    label: 'Connection limit hit',
    hint: 'The account allows fewer connections than are configured. Lower the connection count for this provider.',
  },
}

export function healthReasonLabel(reason) {
  return REASON_COPY[reason]?.label || 'Not working'
}

export function healthReasonHint(reason) {
  return REASON_COPY[reason]?.hint || ''
}

// isBlocked marks the states that need a human: everything else recovers by
// itself and is shown as information rather than as a call to action.
export function isBlocked(record) {
  return record?.state === HEALTH_STATE_BLOCKED
}

// indexHealth turns the flat API list into a lookup keyed by kind and name.
export function indexHealth(components) {
  const map = new Map()
  for (const record of Array.isArray(components) ? components : []) {
    if (!record?.kind || !record?.name) continue
    map.set(`${record.kind}:${record.name}`, record)
  }
  return map
}

export function healthFor(map, kind, name) {
  if (!map || !name) return null
  return map.get(`${kind}:${name}`) || null
}

// formatSince renders how long a component has been in its current state. The
// duration is the useful part — "since yesterday" tells the user whether they
// are looking at a blip or at something that has been broken all week.
export function formatSince(since) {
  if (!since) return ''
  const started = new Date(since)
  if (Number.isNaN(started.getTime())) return ''
  const seconds = Math.max(0, Math.floor((Date.now() - started.getTime()) / 1000))
  if (seconds < 90) return 'just now'
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `for ${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `for ${hours}h`
  return `for ${Math.floor(hours / 24)}d`
}
