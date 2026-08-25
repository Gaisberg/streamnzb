// Pure helpers behind the Streams page: shaping a stream between its config
// form, its draft in the dialog, and the summary rows on the card.
//
// They were 250-odd lines at the top of StreamManagement.jsx, which made them
// unreachable from a test — there is no way to call one without mounting a
// 1,600-line component. Nothing here touches React, the DOM, or the network.

import { CONTENT_KINDS } from '@/lib/profiles'

export function mapStreamsByUsername(streams) {
  return (Array.isArray(streams) ? streams : []).reduce((acc, stream) => {
    if (!stream?.username) return acc
    acc[stream.username] = stream
    return acc
  }, {})
}

export function streamsFromMap(streamsByName) {
  return Object.values(streamsByName || {}).filter(Boolean)
}

export function uniquePreserveOrder(values) {
  const seen = new Set()
  const next = []
  ;(Array.isArray(values) ? values : []).forEach((value) => {
    if (!value || seen.has(value)) return
    seen.add(value)
    next.push(value)
  })
  return next
}

// sortedByKey rebuilds a map in key order. The dirty check compares serialized
// drafts and Go marshals maps sorted, so insertion order would otherwise read
// as an unsaved change.
function sortedByKey(value) {
  const out = {}
  Object.keys(value || {}).sort().forEach((key) => { out[key] = value[key] })
  return out
}

// pickConnectionLimits keeps caps only for providers the stream still selects,
// so removing a provider does not leave a limit behind that silently reapplies
// if it is added back later.
function pickConnectionLimits(limits, selectedProviders) {
  const kept = {}
  selectedProviders.forEach((name) => {
    const limit = Number.parseInt(limits?.[name], 10)
    if (Number.isFinite(limit) && limit > 0) kept[name] = limit
  })
  return kept
}

// Mirrors MaxAddonNameLength and the default manifest name in
// pkg/server/stremio/manifest.go. Keep them in step.
export const MAX_ADDON_NAME_LENGTH = 60
const SERVICE_NAME = 'StreamNZB'

export function defaultAddonName(streamName) {
  const trimmed = (streamName || '').trim()
  return trimmed ? `${SERVICE_NAME} · ${trimmed}` : SERVICE_NAME
}

// activeProviderNames mirrors Stream.ActiveProviderSelections on the backend:
// summaries should count what the stream actually uses, not what is merely
// listed. Keep the two in step.
export function activeProviderNames(stream) {
  const disabled = new Set(stream?.disabled_providers || [])
  return (stream?.provider_selections || []).filter((name) => !disabled.has(name))
}

export function normalizeStreamDraft(draft) {
  const normalizedFilterSortingMode = draft?.filter_sorting_mode === 'aiostreams' ? 'aiostreams' : 'none'
  const providers = uniquePreserveOrder(draft?.providers)
  return {
    filter_sorting_mode: normalizedFilterSortingMode,
    indexer_mode: draft?.indexer_mode === 'failover' ? 'failover' : 'combine',
    username: (draft?.username || '').trim(),
    combine_results: draft?.combine_results !== false,
    enable_failover: draft?.enable_failover !== false,
    variant_attempts: normalizeVariantAttempts(draft?.variant_attempts),
    results_mode: normalizedFilterSortingMode === 'aiostreams' || draft?.results_mode === 'display_all' ? 'display_all' : 'combined_stream',
    auto_add_providers: draft?.auto_add_providers === true,
    auto_add_indexers: draft?.auto_add_indexers === true,
    unaired_search_gate: draft?.unaired_search_gate !== false,
    filter_availnzb: draft?.filter_availnzb === true,
    providers,
    provider_connection_limits: pickConnectionLimits(draft?.provider_connection_limits, providers),
    disabled_providers: uniquePreserveOrder(draft?.disabled_providers).filter((name) => providers.includes(name)),
    indexers: uniquePreserveOrder(draft?.indexers),
    indexer_overrides: draft?.indexer_overrides || {},
    movie_search_queries: uniquePreserveOrder(draft?.movie_search_queries),
    series_search_queries: uniquePreserveOrder(draft?.series_search_queries),
    filter_profile_name: normalizedFilterSortingMode === 'aiostreams' ? '' : (draft?.filter_profile_name || ''),
    filter_profile_by_type: sortedByKey(draft?.filter_profile_by_type),
    metadata_profile_name: draft?.metadata_profile_name || '',
    format_profile_name: draft?.format_profile_name || '',
    result_name_template: draft?.result_name_template || '',
    result_description_template: draft?.result_description_template || '',
    addon_name: (draft?.addon_name || '').trim(),
  }
}

export function buildStreamDraft(stream) {
  return normalizeStreamDraft({
    filter_sorting_mode: stream?.filter_sorting_mode,
    indexer_mode: stream?.indexer_mode,
    username: stream?.username || '',
    combine_results: stream?.combine_results,
    enable_failover: stream?.enable_failover,
    variant_attempts: stream?.variant_attempts,
    results_mode: stream?.results_mode,
    auto_add_providers: stream?.auto_add_providers,
    auto_add_indexers: stream?.auto_add_indexers,
    unaired_search_gate: stream?.unaired_search_gate,
    filter_availnzb: stream?.filter_availnzb,
    providers: stream?.provider_selections || stream?.providers || [],
    provider_connection_limits: stream?.provider_connection_limits || {},
    disabled_providers: stream?.disabled_providers || [],
    indexers: stream?.indexer_selections || stream?.indexers || Object.keys(stream?.indexer_overrides || {}),
    indexer_overrides: stream?.indexer_overrides || {},
    movie_search_queries: stream?.movie_search_queries || [],
    series_search_queries: stream?.series_search_queries || [],
    filter_profile_name: stream?.filter_profile_name || '',
    filter_profile_by_type: stream?.filter_profile_by_type || {},
    metadata_profile_name: stream?.metadata_profile_name || '',
    format_profile_name: stream?.format_profile_name || '',
    result_name_template: stream?.result_name_template || '',
    result_description_template: stream?.result_description_template || '',
    addon_name: stream?.addon_name || '',
  })
}

export function buildIndexerOverrides(selectedIndexerNames, existingOverrides = {}) {
  return selectedIndexerNames.reduce((acc, name) => {
    acc[name] = existingOverrides?.[name] || {}
    return acc
  }, {})
}

export function buildStreamStateFromDraft(username, token, draft, existingOverrides = {}) {
  return {
    username,
    token: token || '',
    filter_sorting_mode: draft.filter_sorting_mode,
    indexer_mode: draft.indexer_mode,
    combine_results: draft.combine_results,
    enable_failover: draft.enable_failover,
    variant_attempts: draft.variant_attempts,
    results_mode: draft.results_mode,
    auto_add_providers: draft.auto_add_providers,
    auto_add_indexers: draft.auto_add_indexers,
    unaired_search_gate: draft.unaired_search_gate,
    filter_availnzb: draft.filter_availnzb,
    provider_selections: draft.providers || [],
    provider_connection_limits: draft.provider_connection_limits || {},
    disabled_providers: draft.disabled_providers || [],
    indexer_selections: draft.indexers || [],
    indexer_overrides: buildIndexerOverrides(draft.indexers || [], draft.indexer_overrides || existingOverrides),
    movie_search_queries: draft.movie_search_queries || [],
    series_search_queries: draft.series_search_queries || [],
    filter_profile_name: draft.filter_profile_name || '',
    filter_profile_by_type: draft.filter_profile_by_type || {},
    metadata_profile_name: draft.metadata_profile_name || '',
    format_profile_name: draft.format_profile_name || '',
    result_name_template: draft.result_name_template || '',
    result_description_template: draft.result_description_template || '',
    addon_name: draft.addon_name || '',
  }
}

export function generalCompactValues(stream) {
  return [stream?.filter_sorting_mode === 'aiostreams' ? 'AIOStreams' : 'Custom']
}

export function generalDetailValues(stream) {
  return [
    `Failover ${stream?.enable_failover !== false ? 'On' : 'Off'}`,
    `Variants ${variantAttemptsLabel(stream?.variant_attempts)}`,
    `Indexers ${(stream?.indexer_mode || 'combine') === 'failover' ? 'Failover' : 'Combine'}`,
    `Search ${stream?.combine_results !== false ? 'Combine' : 'First hit'}`,
    `Results ${stream?.results_mode === 'display_all' ? 'All' : 'Combine'}`,
    `Auto providers ${stream?.auto_add_providers === true ? 'On' : 'Off'}`,
    `Auto indexers ${stream?.auto_add_indexers === true ? 'On' : 'Off'}`,
  ]
}

export function filterSortingSummaryValues(stream) {
  if (stream?.filter_sorting_mode === 'aiostreams') {
    return ['AIOStreams']
  }
  if (stream?.filter_profile_name) {
    return [stream.filter_profile_name]
  }
  return ['None']
}

export function filterSortingLabel(draft) {
  if (draft?.filter_sorting_mode === 'aiostreams') {
    return 'AIOStreams'
  }
  if (draft?.filter_profile_name) {
    return draft.filter_profile_name
  }
  return 'None'
}

export function metadataSummaryValues(stream) {
  return [stream?.metadata_profile_name || 'Off']
}

export function formattingSummaryValues(stream) {
  if (stream?.format_profile_name) return [stream.format_profile_name]
  // Legacy inline templates survive until the migration has run.
  return [stream?.result_name_template || stream?.result_description_template ? 'Custom' : 'Default']
}

export function searchRequestsLabel(combineResults) {
  return combineResults !== false ? 'Combine all' : 'Stop after first hit'
}

export function indexerModeLabel(value) {
  return value === 'failover' ? 'Failover' : 'Combine'
}

export function resultsModeLabel(value) {
  return value === 'display_all' ? 'Display all' : 'Combined stream'
}

// VARIANT_ATTEMPTS_UNLIMITED matches config.VariantAttemptsUnlimited: -1 asks
// for every copy the merge kept, 0 means the backend default.
export const VARIANT_ATTEMPTS_UNLIMITED = -1

export function normalizeVariantAttempts(value) {
  const parsed = Number.parseInt(value, 10)
  if (!Number.isFinite(parsed)) return 0
  if (parsed === VARIANT_ATTEMPTS_UNLIMITED) return VARIANT_ATTEMPTS_UNLIMITED
  if (parsed < 1) return 0
  return parsed
}

export function variantAttemptsLabel(value) {
  const attempts = normalizeVariantAttempts(value)
  if (attempts === VARIANT_ATTEMPTS_UNLIMITED) return 'All copies'
  // 0 is "use the backend default", which is Merge only.
  if (attempts <= 1) return 'Merge only'
  return `${attempts} copies`
}

export function applyFilterSortingMode(current, nextMode, profileName = '') {
  const normalizedMode = nextMode === 'aiostreams' ? 'aiostreams' : 'none'
  const nextDraft = {
    ...current,
    filter_sorting_mode: normalizedMode,
    filter_profile_name: normalizedMode === 'aiostreams' ? '' : profileName,
    filter_profile_by_type: current?.filter_profile_by_type || {},
  }
  if (normalizedMode === 'aiostreams') {
    nextDraft.results_mode = 'display_all'
  }
  return normalizeStreamDraft(nextDraft)
}


const tabFieldErrorKeys = {
  providers: ['providers'],
  indexers: ['indexers'],
  search: ['movie_search_queries', 'series_search_queries'],
}

export function tabHasError(tabId, fieldErrors) {
  return (tabFieldErrorKeys[tabId] || []).some((key) => fieldErrors[key])
}

function defaultStreamName(index) {
  return `Stream${String(index + 1).padStart(2, '0')}`
}

export function nextStreamName(streams) {
  const existing = new Set((Array.isArray(streams) ? streams : []).map((stream) => (stream?.username || '').toLowerCase()))
  for (let index = 0; index < 999; index += 1) {
    const candidate = defaultStreamName(index)
    if (!existing.has(candidate.toLowerCase())) {
      return candidate
    }
  }
  return `Stream${Date.now()}`
}

export function getInitialStreamDraft(initialStream, isEditing, enabledProviderNames = [], enabledIndexerNames = []) {
  const base = buildStreamDraft(initialStream)
  if (!isEditing) {
    base.auto_add_providers = true
    base.auto_add_indexers = true
    base.providers = uniquePreserveOrder(enabledProviderNames)
    base.indexers = uniquePreserveOrder(enabledIndexerNames)
  }
  return base
}
