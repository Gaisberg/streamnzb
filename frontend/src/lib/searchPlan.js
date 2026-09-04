// The search-plan vocabulary, mirroring pkg/core/config/searchplan.go.
//
// A search request is a plan: an ordered list of attempts, one rule for when to
// stop, and one statement of what counts as a match. An attempt says how to
// address the content and what granularity to ask for — nothing else has to be
// inferred from it.

export const ADDRESS_ID = 'id'
export const ADDRESS_TITLE = 'title'

export const TARGET_EPISODE = 'episode'
export const TARGET_SEASON = 'season'
export const TARGET_SERIES = 'series'
export const TARGET_ABSOLUTE = 'absolute'

export const STOP_FIRST_HIT = 'first_hit'
export const STOP_ENOUGH_HITS = 'enough_hits'
export const STOP_ALL = 'all'

// The threshold an enough_hits plan stops at when it names none, mirroring
// config.DefaultSearchMinHits.
export const DEFAULT_MIN_HITS = 10

export const ORDER_AS_LISTED = 'as_listed'
export const ORDER_ADAPTIVE_SEASON = 'adaptive_season'

export const ADDRESS_OPTIONS = [
  { value: ADDRESS_ID, label: 'ID' },
  { value: ADDRESS_TITLE, label: 'Title' },
]

export const TARGET_OPTIONS = [
  { value: TARGET_EPISODE, label: 'Episode' },
  { value: TARGET_SEASON, label: 'Season' },
  { value: TARGET_SERIES, label: 'Series' },
  { value: TARGET_ABSOLUTE, label: 'Absolute' },
]

export const STOP_OPTIONS = [
  { value: STOP_FIRST_HIT, label: 'Stop at first hit' },
  { value: STOP_ENOUGH_HITS, label: 'Stop after enough hits' },
  { value: STOP_ALL, label: 'Run every attempt' },
]

export const ORDER_OPTIONS = [
  { value: ORDER_AS_LISTED, label: 'As listed' },
  { value: ORDER_ADAPTIVE_SEASON, label: 'Season first once it has aired' },
]

const lower = (value) => String(value ?? '').trim().toLowerCase()

export function normalizeAddress(address) {
  return lower(address) === ADDRESS_ID ? ADDRESS_ID : ADDRESS_TITLE
}

export function normalizeTarget(target) {
  switch (lower(target)) {
    case TARGET_SEASON:
      return TARGET_SEASON
    case TARGET_SERIES:
      return TARGET_SERIES
    case TARGET_ABSOLUTE:
      return TARGET_ABSOLUTE
    default:
      return TARGET_EPISODE
  }
}

export function normalizeStop(stop) {
  switch (lower(stop)) {
    case STOP_ALL:
      return STOP_ALL
    case STOP_ENOUGH_HITS:
      return STOP_ENOUGH_HITS
    default:
      return STOP_FIRST_HIT
  }
}

// normalizeMinHits settles the enough_hits threshold: a whole number of at
// least one, the default when the plan names none, and zero — omitted — for
// the stop rules that have no threshold to keep.
export function normalizeMinHits(stop, minHits) {
  if (normalizeStop(stop) !== STOP_ENOUGH_HITS) return 0
  const value = Math.floor(Number(minHits))
  return Number.isFinite(value) && value >= 1 ? value : DEFAULT_MIN_HITS
}

export function normalizeOrder(order) {
  return lower(order) === ORDER_ADAPTIVE_SEASON ? ORDER_ADAPTIVE_SEASON : ORDER_AS_LISTED
}

export function isSeriesKind(kind) {
  return kind === 'series'
}

// normalizeAttempt settles every field, so an attempt in a draft is always the
// attempt that would be dispatched: a movie attempt has no target, and an id
// attempt has no query language and no year token.
export function normalizeAttempt(attempt, kind) {
  const address = normalizeAddress(attempt?.address)
  const next = { address }
  if (isSeriesKind(kind)) {
    next.target = normalizeTarget(attempt?.target)
  }
  if (address === ADDRESS_TITLE) {
    next.title = String(attempt?.title ?? '')
    next.year = attempt?.year === true
  }
  return next
}

export function normalizeAttempts(attempts, kind) {
  const list = Array.isArray(attempts) ? attempts : []
  const seen = new Set()
  const out = []
  for (const attempt of list) {
    const normalized = normalizeAttempt(attempt, kind)
    const key = JSON.stringify(normalized)
    if (seen.has(key)) continue
    seen.add(key)
    out.push(normalized)
  }
  return out
}

// attemptKey identifies a row for drag reordering. Attempts have no id of
// their own, and two rows can be identical only until the next normalize pass,
// so the index rides along.
export function attemptKey(attempt, index) {
  return `${index}:${normalizeAddress(attempt?.address)}:${normalizeTarget(attempt?.target)}`
}

const ADDRESS_LABELS = { [ADDRESS_ID]: 'ID', [ADDRESS_TITLE]: 'Title' }
const TARGET_LABELS = {
  [TARGET_EPISODE]: 'Episode',
  [TARGET_SEASON]: 'Season',
  [TARGET_SERIES]: 'Series',
  [TARGET_ABSOLUTE]: 'Absolute',
}

export function attemptLabel(attempt, kind) {
  const address = ADDRESS_LABELS[normalizeAddress(attempt?.address)]
  if (!isSeriesKind(kind)) return address
  return `${address} · ${TARGET_LABELS[normalizeTarget(attempt?.target)]}`
}

export function defaultAttempt(kind) {
  return normalizeAttempt({ address: ADDRESS_ID, target: TARGET_EPISODE }, kind)
}

// The presets are the stock plans, and the same lists pkg/core/config seeds a
// fresh install with. Each is ordered narrowest-first: a plan costs one indexer
// round trip when its precise question answers, and widens only when it does
// not.
export function planPresets(kind) {
  if (!isSeriesKind(kind)) {
    return [
      {
        id: 'balanced',
        label: 'Balanced',
        description: 'Ask by id, fall back to the title.',
        plan: {
          attempts: [
            { address: ADDRESS_ID },
            { address: ADDRESS_TITLE, title: 'en-US', year: true },
          ],
          stop: STOP_FIRST_HIT,
          order: ORDER_AS_LISTED,
          accept: { titles: ['en-US', ''], year: true },
        },
      },
      {
        id: 'precise',
        label: 'Precise',
        description: 'One id request, nothing else.',
        plan: {
          attempts: [{ address: ADDRESS_ID }],
          stop: STOP_FIRST_HIT,
          order: ORDER_AS_LISTED,
          accept: { titles: ['en-US', ''], year: true },
        },
      },
      {
        id: 'broad',
        label: 'Broad',
        description: 'Run both every time and merge the results.',
        plan: {
          attempts: [
            { address: ADDRESS_ID },
            { address: ADDRESS_TITLE, title: 'en-US', year: true },
          ],
          stop: STOP_ALL,
          order: ORDER_AS_LISTED,
          accept: { titles: ['en-US', ''], year: true },
        },
      },
    ]
  }
  return [
    {
      id: 'balanced',
      label: 'Balanced',
      description: 'Ask for the episode both ways before settling for a pack.',
      plan: {
        attempts: [
          { address: ADDRESS_ID, target: TARGET_EPISODE },
          { address: ADDRESS_TITLE, target: TARGET_ABSOLUTE, title: 'en-US' },
          { address: ADDRESS_TITLE, target: TARGET_EPISODE, title: 'en-US' },
          { address: ADDRESS_ID, target: TARGET_SEASON },
          { address: ADDRESS_TITLE, target: TARGET_SEASON, title: 'en-US' },
        ],
        stop: STOP_FIRST_HIT,
        order: ORDER_ADAPTIVE_SEASON,
        accept: { titles: ['en-US', ''], year: false, packs: true },
      },
    },
    {
      id: 'precise',
      label: 'Precise',
      description: 'One id request for the episode, no packs.',
      plan: {
        attempts: [{ address: ADDRESS_ID, target: TARGET_EPISODE }],
        stop: STOP_FIRST_HIT,
        order: ORDER_AS_LISTED,
        accept: { titles: ['en-US', ''], year: false, packs: false },
      },
    },
    {
      id: 'broad',
      label: 'Broad',
      description: 'Run every attempt every time and merge the results.',
      plan: {
        attempts: [
          { address: ADDRESS_ID, target: TARGET_EPISODE },
          { address: ADDRESS_TITLE, target: TARGET_EPISODE, title: 'en-US' },
          { address: ADDRESS_TITLE, target: TARGET_ABSOLUTE, title: 'en-US' },
          { address: ADDRESS_ID, target: TARGET_SEASON },
          { address: ADDRESS_TITLE, target: TARGET_SEASON, title: 'en-US' },
        ],
        stop: STOP_ALL,
        order: ORDER_AS_LISTED,
        accept: { titles: ['en-US', ''], year: false, packs: true },
      },
    },
  ]
}

export function presetPlan(kind, id) {
  const preset = planPresets(kind).find((entry) => entry.id === id)
  return preset ? preset.plan : planPresets(kind)[0].plan
}

// A plan reordered by the adaptive ordering, for the preview the dialog shows:
// a finished season leads with the season attempts, and the listed order holds
// within each group.
export function attemptsInRunOrder(attempts, order, kind, seasonCompleted) {
  const list = normalizeAttempts(attempts, kind)
  if (!isSeriesKind(kind) || normalizeOrder(order) !== ORDER_ADAPTIVE_SEASON || !seasonCompleted) {
    return list
  }
  const rank = (attempt) => (normalizeTarget(attempt.target) === TARGET_SEASON ? 0 : 1)
  return list
    .map((attempt, index) => ({ attempt, index }))
    .sort((a, b) => rank(a.attempt) - rank(b.attempt) || a.index - b.index)
    .map((entry) => entry.attempt)
}

// normalizeSearchPlan settles a whole plan: the shape the editor works on and
// the shape saved back. The backend migrates the pre-plan schema on load, so
// there is nothing legacy to read here.
export function normalizeSearchPlan(kind, plan) {
  const value = plan || {}
  const accept = value.accept || {}
  const next = {
    name: String(value.name ?? '').trim(),
    attempts: normalizeAttempts(value.attempts, kind),
    stop: normalizeStop(value.stop),
    min_hits: normalizeMinHits(value.stop, value.min_hits),
    accept: {
      titles: Array.isArray(accept.titles) ? accept.titles.filter((title) => typeof title === 'string') : [],
      year: accept.year === true,
    },
    search_result_limit: Number(value.search_result_limit || 0),
    categories: String(value.categories ?? '').trim(),
  }
  if (isSeriesKind(kind)) {
    next.order = normalizeOrder(value.order)
    next.accept.packs = accept.packs !== false
  }
  return next
}
