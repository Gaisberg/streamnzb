export const SUBSCRIPTION_WARN_DAYS = 30
export const SUBSCRIPTION_URGENT_DAYS = 7

const DATE_ONLY = /^(\d{4})-(\d{2})-(\d{2})$/
const DAY_MS = 24 * 60 * 60 * 1000

function dayNumber(year, monthIndex, day) {
  return Date.UTC(year, monthIndex, day) / DAY_MS
}

// subscriptionExpiryStatus turns a YYYY-MM-DD expiry into whole calendar days
// from today (local time) and a tone for the UI. Returns null for an empty or
// malformed value so the caller shows nothing rather than a wrong date.
export function subscriptionExpiryStatus(value, now = new Date()) {
  const match = DATE_ONLY.exec((value || '').trim())
  if (!match) return null
  const [year, month, day] = [Number(match[1]), Number(match[2]), Number(match[3])]
  const check = new Date(Date.UTC(year, month - 1, day))
  if (check.getUTCFullYear() !== year || check.getUTCMonth() !== month - 1 || check.getUTCDate() !== day) return null

  const days = dayNumber(year, month - 1, day) - dayNumber(now.getFullYear(), now.getMonth(), now.getDate())

  if (days < 0) {
    const ago = -days
    return { days, tone: 'expired', label: ago === 1 ? 'Expired yesterday' : `Expired ${ago} days ago` }
  }
  const label = days === 0 ? 'Expires today' : days === 1 ? 'Expires tomorrow' : `Expires in ${days} days`
  const tone = days <= SUBSCRIPTION_URGENT_DAYS ? 'urgent' : days <= SUBSCRIPTION_WARN_DAYS ? 'warning' : 'ok'
  return { days, tone, label }
}
