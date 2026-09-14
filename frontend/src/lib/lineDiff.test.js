import { describe, expect, it } from 'vitest'
import { collapseUnchanged, lineDiff } from '@/lib/lineDiff'

describe('lineDiff', () => {
  it('marks only the lines that differ', () => {
    const before = ['a', 'b', 'c', 'd'].join('\n')
    const after = ['a', 'B', 'c', 'd', 'e'].join('\n')
    expect(lineDiff(before, after)).toEqual([
      { kind: 'same', text: 'a' },
      { kind: 'del', text: 'b' },
      { kind: 'add', text: 'B' },
      { kind: 'same', text: 'c' },
      { kind: 'same', text: 'd' },
      { kind: 'add', text: 'e' },
    ])
  })

  it('is a whole replacement when nothing is shared', () => {
    expect(lineDiff('x', 'y')).toEqual([
      { kind: 'del', text: 'x' },
      { kind: 'add', text: 'y' },
    ])
  })

  it('keeps an unchanged text unchanged', () => {
    expect(lineDiff('a\nb', 'a\nb')).toEqual([
      { kind: 'same', text: 'a' },
      { kind: 'same', text: 'b' },
    ])
  })
})

describe('collapseUnchanged', () => {
  const same = (n) => Array.from({ length: n }, (_, i) => ({ kind: 'same', text: `l${i}` }))

  it('folds long unchanged runs to context around each change', () => {
    const lines = [...same(5), { kind: 'add', text: 'new' }, ...same(5)]
    const out = collapseUnchanged(lines, 2)
    expect(out.map((l) => l.kind)).toEqual([
      'skip', 'same', 'same', 'add', 'same', 'same', 'skip',
    ])
    expect(out[0].count).toBe(3)
    expect(out[6].count).toBe(3)
  })

  it('leaves a run short enough to show whole', () => {
    const lines = [...same(2), { kind: 'del', text: 'old' }, ...same(4), { kind: 'add', text: 'new' }]
    expect(collapseUnchanged(lines, 2).map((l) => l.kind)).toEqual([
      'same', 'same', 'del', 'same', 'same', 'same', 'same', 'add',
    ])
  })
})
