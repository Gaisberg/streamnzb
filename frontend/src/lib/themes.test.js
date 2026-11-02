import { describe, expect, it } from 'vitest'

import { normalizeTheme, resolveTheme, THEME_PRESETS } from './themes'

describe('theme helpers', () => {
  it('keeps the base modes as they were', () => {
    expect(resolveTheme('light', true)).toEqual({ mode: 'light', preset: null })
    expect(resolveTheme('dark', false)).toEqual({ mode: 'dark', preset: null })
    expect(resolveTheme('system', true)).toEqual({ mode: 'dark', preset: null })
    expect(resolveTheme('system', false)).toEqual({ mode: 'light', preset: null })
  })

  it('puts presets on their base mode', () => {
    expect(resolveTheme('oled', false)).toEqual({ mode: 'dark', preset: 'oled' })
    for (const p of THEME_PRESETS) {
      expect(resolveTheme(p.id, false).preset).toBe(p.id)
    }
  })

  it('falls back to system for unknown stored values', () => {
    expect(normalizeTheme(null)).toBe('system')
    expect(normalizeTheme('solarized')).toBe('system')
    expect(resolveTheme('solarized', true)).toEqual({ mode: 'dark', preset: null })
  })
})
