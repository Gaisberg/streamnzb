import { describe, expect, it } from 'vitest'
import { moveItem, uniquePreserveOrder } from '@/lib/lists'

describe('moveItem', () => {
  it('moves an entry to its new position without touching the original', () => {
    const list = ['a', 'b', 'c']
    expect(moveItem(list, 0, 2)).toEqual(['b', 'c', 'a'])
    expect(moveItem(list, 2, 0)).toEqual(['c', 'a', 'b'])
    expect(list).toEqual(['a', 'b', 'c'])
  })
})

describe('uniquePreserveOrder', () => {
  it('drops duplicates and empties while keeping the order the user chose', () => {
    expect(uniquePreserveOrder(['b', 'a', 'b', '', 'c', null])).toEqual(['b', 'a', 'c'])
  })

  it('tolerates anything that is not an array', () => {
    expect(uniquePreserveOrder(undefined)).toEqual([])
    expect(uniquePreserveOrder('nope')).toEqual([])
  })
})
