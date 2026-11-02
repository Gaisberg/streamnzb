// UI theme registry. "light", "dark" and "system" are the base modes; every
// preset rides on one of them (so `dark:` utilities keep working) and only
// overrides the colour tokens, via a [data-theme="<id>"] block in index.css.
// Adding a preset is an entry here plus that CSS block — components never
// need to know which preset is active.

export const BASE_THEMES = ['light', 'dark', 'system']

// swatch: [background, accent] in hex, for the picker only.
export const THEME_PRESETS = [
  { id: 'oled', label: 'OLED', mode: 'dark', swatch: ['#000000', '#ff8985'] },
  { id: 'nord', label: 'Nord', mode: 'dark', swatch: ['#2e3440', '#88c0d0'] },
  { id: 'dracula', label: 'Dracula', mode: 'dark', swatch: ['#282a36', '#bd93f9'] },
  { id: 'gruvbox', label: 'Gruvbox', mode: 'dark', swatch: ['#282828', '#fe8019'] },
]

export const DEFAULT_THEME = 'system'

export function presetFor(theme) {
  return THEME_PRESETS.find((p) => p.id === theme) || null
}

// normalizeTheme maps a stored preference onto a theme we still ship, so a
// stale or hand-edited localStorage value falls back instead of leaving the
// page without a base mode.
export function normalizeTheme(theme) {
  if (BASE_THEMES.includes(theme) || presetFor(theme)) return theme
  return DEFAULT_THEME
}

// resolveTheme returns the base mode class and the preset id (or null) to put
// on <html>.
export function resolveTheme(theme, prefersDark) {
  const id = normalizeTheme(theme)
  const preset = presetFor(id)
  if (preset) return { mode: preset.mode, preset: preset.id }
  if (id === 'system') return { mode: prefersDark ? 'dark' : 'light', preset: null }
  return { mode: id, preset: null }
}
