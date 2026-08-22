// Mirrors config.NormalizeAvailNZBMode: AvailNZB is opt-in, so only a mode that
// explicitly names it counts as on. Anything else — empty, unknown, a config
// that predates the setting — reads as off.
export function normalizeAvailNZBMode(mode) {
  const value = String(mode ?? '').trim().toLowerCase()
  return value === 'on' || value === 'full' || value === 'status_only' ? 'on' : 'off'
}

export function isAvailNZBEnabled(mode) {
  return normalizeAvailNZBMode(mode) === 'on'
}
