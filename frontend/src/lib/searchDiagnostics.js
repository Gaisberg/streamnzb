// Reading a persisted search-diagnostics snapshot.
//
// The shapes here mirror pkg/search/diag.Snapshot. They live in lib rather
// than in the History page because they are pure data reshaping with real
// behaviour to get wrong — which stage's releases come first, what a drop's
// badges say — and that is worth a test.

export function parseDiagnosticPayload(diagnostic) {
  if (!diagnostic?.payload) return null
  try {
    return JSON.parse(diagnostic.payload)
  } catch {
    return null
  }
}

// The stage a pre-profile drop came from, in the words the page uses. The keys
// are diag.DropStage* on the backend.
const DROP_STAGE_LABELS = {
  validation: 'validation',
  bad: 'known bad',
}

// The badges on a pre-profile drop: which stage turned it away, and what the
// check expected against what the name said. The stage is the highlighted one
// because it decides where to look next — validation means the query or the
// metadata title, "known bad" means playback retired the release.
export function droppedBadges(drop) {
  const badges = [{
    key: 'stage',
    label: DROP_STAGE_LABELS[drop.stage] || drop.stage || 'dropped',
    highlight: true,
  }]
  if (drop.reason) badges.push({ key: 'reason', label: drop.reason })
  if (drop.detail) badges.push({ key: 'detail', label: drop.detail })
  if (drop.request) badges.push({ key: 'request', label: drop.request, title: 'The search request this result was answering' })
  return badges
}

// The releases a search turned away, as rows for the same list the play
// attempts render in.
//
// Rejections and pre-profile drops are one list because they answer one
// question — what happened to this release — and a release the profile
// rejected is no less a release than one that failed playback. Drops come
// first because that is the order the funnel applies them in.
export function turnedAwayReleases(diagnostic) {
  const snap = parseDiagnosticPayload(diagnostic)
  if (!snap) return { releases: [], omitted: 0 }
  const dropped = Array.isArray(snap.dropped) ? snap.dropped : []
  const rejected = Array.isArray(snap.rejected) ? snap.rejected : []
  const releases = [
    ...dropped.map((d, index) => ({
      key: `dropped-${index}-${d.title}`,
      title: d.title,
      meta: [d.indexer, d.request].filter(Boolean),
      badges: droppedBadges(d),
    })),
    ...rejected.map((r, index) => ({
      key: `rejected-${index}-${r.title}`,
      title: r.title,
      meta: [r.indexer].filter(Boolean),
      badges: (r.reasons || []).map((reason) => ({
        key: reason,
        label: reason,
        // A rule the user wrote and a built-in trait send you to different
        // parts of the profile editor, so they must not look alike.
        highlight: reason.startsWith('rule: '),
      })),
    })),
  ]
  return { releases, omitted: snap.dropped_omitted || 0 }
}
