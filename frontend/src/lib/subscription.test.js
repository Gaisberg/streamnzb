import { describe, expect, it } from 'vitest'
import { subscriptionExpiryStatus } from './subscription'

const now = new Date(2027, 0, 31, 15, 30)

describe('subscriptionExpiryStatus', () => {
  it('returns null for empty or malformed values', () => {
    expect(subscriptionExpiryStatus('', now)).toBeNull()
    expect(subscriptionExpiryStatus(undefined, now)).toBeNull()
    expect(subscriptionExpiryStatus('15/03/2027', now)).toBeNull()
    expect(subscriptionExpiryStatus('2027-02-30', now)).toBeNull()
  })

  it('counts whole calendar days regardless of time of day', () => {
    expect(subscriptionExpiryStatus('2027-03-17', now)).toMatchObject({ days: 45, tone: 'ok', label: 'Expires in 45 days' })
    expect(subscriptionExpiryStatus('2027-01-31', new Date(2027, 0, 31, 23, 59))).toMatchObject({ days: 0, label: 'Expires today' })
  })

  it('warns at 30 days and 7 days', () => {
    expect(subscriptionExpiryStatus('2027-03-02', now)).toMatchObject({ days: 30, tone: 'warning' })
    expect(subscriptionExpiryStatus('2027-03-03', now)).toMatchObject({ days: 31, tone: 'ok' })
    expect(subscriptionExpiryStatus('2027-02-07', now)).toMatchObject({ days: 7, tone: 'urgent', label: 'Expires in 7 days' })
    expect(subscriptionExpiryStatus('2027-02-01', now)).toMatchObject({ days: 1, tone: 'urgent', label: 'Expires tomorrow' })
  })

  it('reports expired subscriptions', () => {
    expect(subscriptionExpiryStatus('2027-01-30', now)).toMatchObject({ days: -1, tone: 'expired', label: 'Expired yesterday' })
    expect(subscriptionExpiryStatus('2027-01-28', now)).toMatchObject({ days: -3, tone: 'expired', label: 'Expired 3 days ago' })
  })

  it('is not thrown off by a DST change between today and the expiry', () => {
    const beforeSpringForward = new Date(2027, 2, 13, 12)
    expect(subscriptionExpiryStatus('2027-03-15', beforeSpringForward)).toMatchObject({ days: 2 })
  })
})
