// Remote filter-profile sources: a filter profile can be linked to a URL that
// serves its share code, so a community-maintained profile is updated by
// pressing Refresh instead of re-pasting a code. The container, the fetch and
// the trust model live in shareCodes.js; this module owns what is specific to
// filter profiles — the merge-by-rule-name contract and its diff.

import { DEFAULT_PRESET, resolveProfileShareCode, ruleKey, rulesToText, scoringToText } from "@/lib/profiles"
import { fetchShareCodeText, resolveFetched, validateSourceUrl } from "@/lib/shareCodes"

// fetchRemoteProfile is the whole of importing from a URL: validate, fetch,
// decode. It returns the profile plus the exact code it decoded, which the
// caller stores as the upstream snapshot.
export async function fetchRemoteProfile(rawUrl) {
  const url = validateSourceUrl(rawUrl)
  const { code, profile } = await resolveFetched(resolveProfileShareCode, await fetchShareCodeText(url))
  return { url, code, profile }
}

// mergeUpstream folds a new upstream profile into the local one, by rule name:
//
//   - a rule named in upstream: upstream's version wins, local edits and all;
//   - a rule named only locally, and not in the previous upstream snapshot:
//     the user's own — kept, appended after upstream's rules in its old order;
//   - a rule named only in the snapshot: the maintainer deleted it — gone,
//     even if the user edited it, or it would linger as a phantom forever.
//
// The preset and the scoring map follow upstream. Scoring is one decision, not
// a rule list: upstream's map replaces the local one whole, and a map the
// maintainer dropped is dropped here too — but only when the snapshot shows
// upstream once had one. A map upstream never carried can only be the user's
// own, and stays.
//
// The contract in one line: customize by adding your own rules; edits to
// upstream rules last until the next refresh. Everything a share code does not
// carry — limits, the profile's local name — stays the user's.
export function mergeUpstream(local, upstream, previousUpstream) {
  const upstreamRules = upstream.rules || []
  const upstreamKeys = new Set(upstreamRules.map((rule) => ruleKey(rule.name)))
  // No readable snapshot (a profile linked by hand, a corrupted config) fails
  // open: every unrecognized local rule is treated as the user's own, because
  // dropping a rule that might be theirs is the worse mistake.
  const prevKeys = previousUpstream
    ? new Set((previousUpstream.rules || []).map((rule) => ruleKey(rule.name)))
    : null
  const keptLocal = (local.rules || []).filter((rule) => {
    const key = ruleKey(rule.name)
    if (upstreamKeys.has(key)) return false
    return !prevKeys || !prevKeys.has(key)
  })
  const profile = {
    ...local,
    preset: upstream.preset || DEFAULT_PRESET,
    rules: [...upstreamRules, ...keptLocal],
  }
  const scoring = upstream.scoring || (previousUpstream?.scoring ? undefined : local.scoring)
  // Set or absent, never undefined: a profile is compared to another by its
  // keys, and an absent map is the shape the config stores.
  if (scoring) profile.scoring = scoring
  else delete profile.scoring
  return { profile, keptLocal }
}

// diffLinkedProfiles is what the confirmation dialog shows: every rule the
// refresh would change, add or remove, as the same one-line text form the
// rules editor uses. Rules are matched by name, the same identity the merge
// uses, so the diff and the merge cannot disagree about what happens.
//
// Every entry carries a `key`, unique across the three lists, because the
// dialog lets the user tick changes one by one and applySelectedChanges reads
// the ticks back.
export function diffLinkedProfiles(current, merged) {
  const byKey = (rules) => new Map((rules || []).map((rule) => [ruleKey(rule.name), rule]))
  const line = (rule) => rulesToText([rule])
  const currentRules = byKey(current.rules)
  const mergedRules = byKey(merged.rules)

  const changed = []
  const added = []
  const removed = []
  for (const [key, rule] of mergedRules) {
    const before = currentRules.get(key)
    if (!before) {
      added.push({ key: `add:${key}`, name: rule.name, line: line(rule) })
    } else if (line(before) !== line(rule)) {
      changed.push({ key: `change:${key}`, name: rule.name, before: line(before), after: line(rule) })
    }
  }
  for (const [key, rule] of currentRules) {
    if (!mergedRules.has(key)) removed.push({ key: `remove:${key}`, name: rule.name, line: line(rule) })
  }
  const preset = current.preset !== merged.preset
    ? { key: "preset", from: current.preset, to: merged.preset }
    : null
  // Scoring is compared in its text form, the same way a rule is: two maps
  // that read the same are the same, however their keys happen to be ordered.
  // An empty side means the preset's own scoring.
  const scoringBefore = scoringToText(current.scoring)
  const scoringAfter = scoringToText(merged.scoring)
  const scoring = scoringBefore !== scoringAfter
    ? { key: "scoring", before: scoringBefore, after: scoringAfter }
    : null
  return {
    changed,
    added,
    removed,
    preset,
    scoring,
    empty: !changed.length && !added.length && !removed.length && !preset && !scoring,
  }
}

// changeKeys lists every decision a rule-shaped diff offers, in the order the
// dialog shows them — what "select all" means, and what the count counts.
export function changeKeys(diff) {
  const entries = [...(diff.changed || []), ...(diff.added || []), ...(diff.removed || [])]
  if (diff.preset) entries.push(diff.preset)
  if (diff.scoring) entries.push(diff.scoring)
  return entries.map((entry) => entry.key)
}

// applySelectedChanges narrows a merge to the changes the user ticked. An
// unticked change leaves the profile as it is: an added rule is not taken, an
// updated rule keeps the local version, a refused removal stays — appended
// after upstream's rules, where every rule of the user's own lives — and an
// unticked preset or scoring change keeps the local value.
//
// Nothing about the refusal is remembered. The stored snapshot is still the
// upstream code in full, because it has to describe what upstream *is* for the
// next merge to tell a user-added rule from an upstream-deleted one. So a rule
// skipped today is offered again on the next refresh — the maintainer still
// has it — while a deletion refused today becomes the user's own rule, since
// the snapshot it used to appear in is gone.
export function applySelectedChanges(current, merged, diff, selected) {
  const currentByKey = new Map((current.rules || []).map((rule) => [ruleKey(rule.name), rule]))
  const unticked = (entries) => new Set(
    entries.filter((entry) => !selected.has(entry.key)).map((entry) => ruleKey(entry.name)))
  const skippedAdds = unticked(diff.added || [])
  const keptLocalEdits = unticked(diff.changed || [])

  const rules = []
  for (const rule of merged.rules || []) {
    const key = ruleKey(rule.name)
    if (skippedAdds.has(key)) continue
    rules.push(keptLocalEdits.has(key) ? currentByKey.get(key) : rule)
  }
  for (const entry of diff.removed || []) {
    if (!selected.has(entry.key)) rules.push(currentByKey.get(ruleKey(entry.name)))
  }

  const out = { ...merged, rules }
  if (diff.preset && !selected.has(diff.preset.key)) out.preset = current.preset
  if (diff.scoring && !selected.has(diff.scoring.key)) {
    if (current.scoring) out.scoring = current.scoring
    else delete out.scoring
  }
  return out
}

// checkForUpdate is the whole of the Refresh button: fetch, decode, merge,
// diff. "Current" means the merge would change nothing — never that the bytes
// match the snapshot, because the local profile drifts on its own: deleting an
// imported rule and pressing Refresh is asking for it back, and an unchanged
// upstream must not read as "nothing to do". The byte compare still earns its
// keep in the other direction — a matching code means the upstream just
// decoded *is* the snapshot, sparing a second decode — and a maintainer
// re-encoding the same profile into different bytes still comes back
// "current", because the diff is what gates applying.
export async function checkForUpdate(profile) {
  const text = await fetchShareCodeText(profile.source.url)
  const { code, profile: upstream } = await resolveFetched(resolveProfileShareCode, text)
  let previousUpstream = null
  if (code === (profile.source.code || "")) {
    previousUpstream = upstream
  } else if (profile.source.code) {
    try {
      previousUpstream = (await resolveProfileShareCode(profile.source.code)).profile
    } catch {
      // An unreadable snapshot only costs the merge its memory of what came
      // from upstream; mergeUpstream fails open on that.
    }
  }
  const { profile: merged, keptLocal } = mergeUpstream(profile, upstream, previousUpstream)
  const diff = diffLinkedProfiles(profile, merged)
  if (diff.empty) {
    return { status: "current" }
  }
  return { status: "update", code, merged, diff, keptLocal, remoteName: upstream.name }
}
