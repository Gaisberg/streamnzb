// The settings pages keep asking one question in different shapes: which
// streams reference this name? Profiles are referenced from the config's
// streams map (stream name → entry with *_profile_name fields); indexers,
// providers and search queries from the API's stream objects (username +
// selection lists). This is the one implementation of both shapes — it used
// to exist five times across the pages, each with its own lowercasing.

// nameKey is the one way a referenced name becomes a lookup key. Matching is
// case-insensitive on the trimmed name everywhere a stream binds something.
export function nameKey(value) {
  return (value || "").trim().toLowerCase()
}

// usageByName folds a streams map into { nameKey: [labels] }. `refs` returns
// what one stream references: strings (labelled with the stream's own name)
// or { name, label } entries when the label carries more than the stream name
// (the Filters page labels per content kind).
export function usageByName(streams, refs) {
  const usage = {}
  const note = (name, label) => {
    const key = nameKey(name)
    if (!key) return
    if (!usage[key]) usage[key] = []
    if (!usage[key].includes(label)) usage[key].push(label)
  }
  Object.entries(streams || {}).forEach(([streamName, stream]) => {
    if (!stream) return
    ;(refs(stream, streamName) || []).forEach((ref) => {
      if (typeof ref === "string") note(ref, streamName)
      else if (ref) note(ref.name, ref.label ?? streamName)
    })
  })
  return usage
}

// assignedStreams answers the single-name form over the API's
// streams-by-username map: the usernames whose `listField` selection contains
// `target`.
export function assignedStreams(streamsByName, listField, target) {
  const key = nameKey(target)
  if (!key || !streamsByName) return []
  return Object.values(streamsByName)
    .filter(Boolean)
    .filter((stream) => Array.isArray(stream[listField]) && stream[listField].some((name) => nameKey(name) === key))
    .map((stream) => stream.username)
}
