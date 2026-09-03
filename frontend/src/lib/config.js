export function normalizeSearchTitleLanguage(value) {
  const trimmed = String(value || '').trim()
  return trimmed.toLowerCase() === 'original' ? '' : trimmed
}

export function normalizeSearchTitleLanguages(values) {
  const list = Array.isArray(values) ? values : []
  const normalized = []
  const seen = new Set()
  for (const value of list) {
    const language = normalizeSearchTitleLanguage(value)
    const key = language.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    normalized.push(language)
  }
  return normalized
}
