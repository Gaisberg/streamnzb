// Shape and vocabulary of a filter profile, mirrored for the Filters UI.
// Keys here must match the JSON the backend stores.

// Release traits grouped the way people reason about them. The policy behind
// each is { fetch, rank } on the wire; the UI calls rank "score".
export const ATTRIBUTE_GROUPS = [
  {
    id: "sources",
    label: "Sources",
    description: "Where the release came from. Turning one off rejects it outright.",
    attrs: [
      { key: "remux", label: "Remux" },
      { key: "bluray", label: "BluRay" },
      { key: "webdl", label: "WEB-DL" },
      { key: "web", label: "WEB" },
      { key: "webrip", label: "WEBRip" },
      { key: "webmux", label: "WEBMux" },
      { key: "webdlrip", label: "WEB-DLRip" },
      { key: "hdtv", label: "HDTV" },
      { key: "bdrip", label: "BDRip" },
      { key: "brrip", label: "BRRip" },
      { key: "dvd", label: "DVD" },
      { key: "dvdrip", label: "DVDRip" },
      { key: "uhdrip", label: "UHDRip" },
      { key: "hdrip", label: "HDRip" },
      { key: "tvrip", label: "TVRip" },
      { key: "satrip", label: "SATRip" },
      { key: "ppvrip", label: "PPVRip" },
      { key: "vhs", label: "VHS" },
      { key: "vhsrip", label: "VHSRip" },
    ],
  },
  {
    id: "trash",
    label: "Trash sources",
    description: "“Remove garbage titles” on the Eligibility tab rejects all of these outright. Turn that switch off to decide them one by one here.",
    attrs: [
      { key: "cam", label: "CAM" },
      { key: "telesync", label: "TeleSync" },
      { key: "telecine", label: "TeleCine" },
      { key: "screener", label: "Screener" },
      { key: "r5", label: "R5" },
      { key: "pdtv", label: "PDTV" },
    ],
  },
  {
    id: "codecs",
    label: "Codecs",
    attrs: [
      { key: "hevc", label: "HEVC" },
      { key: "avc", label: "AVC" },
      { key: "av1", label: "AV1" },
      { key: "xvid", label: "Xvid" },
      { key: "mpeg", label: "MPEG" },
    ],
  },
  {
    id: "hdr",
    label: "HDR & bit depth",
    attrs: [
      { key: "dolby_vision", label: "Dolby Vision" },
      { key: "hdr10plus", label: "HDR10+" },
      { key: "hdr", label: "HDR" },
      { key: "sdr", label: "SDR" },
      { key: "10bit", label: "10-bit" },
    ],
  },
  {
    id: "audio",
    label: "Audio",
    attrs: [
      { key: "dts_lossless", label: "DTS Lossless" },
      { key: "truehd", label: "TrueHD" },
      { key: "atmos", label: "Atmos" },
      { key: "dolby_digital_plus", label: "Dolby Digital Plus" },
      { key: "dolby_digital", label: "Dolby Digital" },
      { key: "dts_lossy", label: "DTS Lossy" },
      { key: "flac", label: "FLAC" },
      { key: "aac", label: "AAC" },
      { key: "opus", label: "Opus" },
      { key: "pcm", label: "PCM" },
      { key: "mp3", label: "MP3" },
      { key: "clean_audio", label: "Clean audio" },
    ],
  },
  {
    id: "channels",
    label: "Channels",
    attrs: [
      { key: "surround", label: "Surround" },
      { key: "stereo", label: "Stereo" },
      { key: "mono", label: "Mono" },
    ],
  },
  {
    id: "extras",
    label: "Release traits",
    attrs: [
      { key: "edition", label: "Edition" },
      { key: "proper", label: "Proper" },
      { key: "repack", label: "Repack" },
      { key: "network", label: "Network" },
      { key: "retail", label: "Retail" },
      { key: "scene", label: "Scene" },
      { key: "subbed", label: "Subbed" },
      { key: "dubbed", label: "Dubbed" },
      { key: "documentary", label: "Documentary" },
      { key: "hardcoded", label: "Hardcoded" },
      { key: "uncensored", label: "Uncensored" },
      { key: "converted", label: "Converted" },
      { key: "upscaled", label: "Upscaled" },
      { key: "3d", label: "3D" },
      { key: "site", label: "Site tag" },
      { key: "size", label: "Size in title" },
    ],
  },
]

export const ATTRIBUTE_LABELS = ATTRIBUTE_GROUPS.reduce((acc, group) => {
  group.attrs.forEach((a) => { acc[a.key] = a.label })
  return acc
}, {})

// TRAIT_KEYS is every attribute key the parser can report, flattened from the
// groups above. Rules read them through `traits`, which is what makes a rule
// able to say anything the ranking baseline can.
export const TRAIT_KEYS = ATTRIBUTE_GROUPS.flatMap((g) => g.attrs.map((a) => a.key)).sort()

// Content kinds partition every request, so exactly one profile ever applies.
// A profile itself is global; a stream picks which one each kind uses.
export const CONTENT_KINDS = [
  { key: "movie", label: "Movies" },
  { key: "series", label: "Shows" },
  { key: "anime_movie", label: "Anime films" },
  { key: "anime_show", label: "Anime shows" },
]

// PRESETS are the whole of a profile's baseline. Mirrors Presets in
// pkg/core/config/preset.go — keep the keys in step.
//
// They differ only in the resolution ceiling, because that is the one decision
// that is about the person rather than the release. Everything else — which
// sources are worth what, which garbage to refuse — has one right answer and is
// a default rather than a question. Anything beyond that is a rule.
export const PRESETS = [
  {
    key: "4k",
    label: "4K",
    tiers: "2160p · 1440p · 1080p · 720p",
    description: "Everything up to 2160p. Largest files, best picture.",
  },
  {
    key: "1080p",
    label: "1080p",
    tiers: "1080p · 720p",
    description: "Smaller files, kinder to a shared connection.",
  },
  {
    key: "720p",
    label: "720p",
    tiers: "720p",
    description: "Smallest files, for slow lines and small screens.",
  },
]

export const DEFAULT_PRESET = "4k"

// matchesReleaseName builds the condition for the common case: a regular
// expression applied to the whole release name. Mirrors MatchesReleaseName in
// pkg/core/config/rules.go so a rule written here reads the same as one the
// migration produced.
export function matchesReleaseName(pattern) {
  const inner = /^\/.*\/$/.test(pattern)
    ? pattern.slice(1, -1)
    : (pattern.startsWith("(?i)") ? pattern : `(?i)${pattern}`)
  return `releaseName matches ${JSON.stringify(inner)}`
}

// Starting points for the rule table. The first six are the weighted patterns
// this used to ship as presets; the rest are the conditions no single regular
// expression could express, which is why rules exist.
export const RULE_PRESETS = [
  { name: "Dual audio", when: matchesReleaseName("\\bDual[. _-]?Audio\\b"), points: 5000 },
  { name: "Multi audio", when: matchesReleaseName("\\bMulti[. _-]?Audio\\b"), points: 3000 },
  { name: "English dub", when: matchesReleaseName("\\b(ENG[. _-]?DUB|English[. _-]?Dub)\\b"), points: 4000 },
  { name: "IMAX", when: matchesReleaseName("\\bIMAX\\b"), points: 2000 },
  { name: "Open matte", when: matchesReleaseName("\\bOpen[. _-]?Matte\\b"), points: 1000 },
  { name: "Hardcoded subs", when: matchesReleaseName("\\bHC\\b|\\bHardsub"), points: -3000 },
  { name: "DV without HDR fallback", when: "dolbyVision and not hdrFallback", action: "reject" },
  { name: "Oversized unless 4K", when: 'sizeGB > 30 and resolution != "2160p"', action: "reject" },
  { name: "Known unavailable", when: 'avail.status == "unavailable"', action: "reject" },
  { name: "Alive on our backbone", when: "avail.onMyBackbone", points: 500 },
  { name: "Recently confirmed", when: "avail.checkedDaysAgo >= 0 and avail.checkedDaysAgo < 30", points: 300 },
  { name: "Measured 10-bit", when: "probed.bitDepth >= 10", points: 400 },
  { name: "At most 3 in 4K", when: 'resolution == "2160p"', action: "limit", count: 3 },
  { name: "At most 5 in 1080p", when: 'resolution == "1080p"', action: "limit", count: 5 },
]

// What a rule can do. Scoring moves a release, rejecting removes it, and
// limiting caps how many of the matching ones you are offered — the one thing
// no condition can say, because "the best three" is about the final score
// order, which only exists after every rule has run.
export const RULE_ACTIONS = [
  { key: "score", label: "Score" },
  { key: "reject", label: "Reject" },
  { key: "limit", label: "Limit" },
]

// A rule can be limited to one content kind. "all" is the default.
export const RULE_SCOPES = [{ key: "all", label: "All content" }, ...CONTENT_KINDS]

// How far a value can be trusted. This is the axis the editor groups by:
// which subsystem computed a value is an implementation detail, but whether
// it was read off a name, claimed by an indexer, reported by strangers or
// measured in the file decides how you should write a rule about it.
export const CONFIDENCE = {
  inferred: {
    label: "inferred",
    short: "read off the release name",
    hint: "Read out of the release name. Every release has it, and it is wrong often enough to be worth saying so.",
  },
  reported: {
    label: "reported",
    short: "claimed by the indexer",
    hint: "Claimed by the indexer. Near-total coverage, fresh, unverified.",
  },
  community: {
    label: "community",
    short: "reported to AvailNZB",
    hint: "From the availability database, per backbone. Partial coverage and a record can be months old. Rules reading it skip releases nobody has reported.",
  },
  measured: {
    label: "measured",
    short: "measured by ffprobe",
    hint: "Measured by ffprobe in the file itself. Only library releases have ever been opened, so rules reading it skip everything else.",
  },
}

// The rule attribute namespace, shown as a reference in the editor and used to
// sanity-check a condition before it reaches the server. Grouped by how much
// each value can be trusted. Mirrors Env in pkg/search/rules/env.go.
export const RULE_ATTRIBUTES = [
  {
    tier: "inferred",
    title: "From the release name",
    note: "Bare names prefer what was measured when the file has been opened, and fall back to the name. parsed.* is always the name's own account.",
    items: [
      { name: "resolution", type: "text", example: '"2160p"' },
      { name: "quality", type: "text", example: '"WEB-DL", "BluRay", "REMUX"' },
      { name: "codec", type: "text", example: '"x265"' },
      { name: "bitDepth", type: "number", example: "10" },
      { name: "hdr", type: "list", example: '"DV" in hdr — note the parser writes plain HDR10 as "HDR"' },
      { name: "dolbyVision", type: "yes/no", example: "carries a Dolby Vision layer" },
      { name: "hdrFallback", type: "yes/no", example: "a non-DV device still gets HDR" },
      { name: "audio", type: "list", example: '"TrueHD" in audio' },
      { name: "channels", type: "list", example: '"7.1" in channels' },
      { name: "languages", type: "list", example: '"en" in languages' },
      { name: "group", type: "text" },
      { name: "edition", type: "text" },
      { name: "container", type: "text" },
      { name: "year", type: "number" },
      { name: "seasonPack", type: "yes/no" },
      { name: "proper", type: "yes/no" },
      { name: "repack", type: "yes/no" },
      { name: "remastered", type: "yes/no" },
      { name: "upscaled", type: "yes/no" },
      { name: "threeD", type: "yes/no" },
      { name: "dubbed", type: "yes/no" },
      { name: "subbed", type: "yes/no" },
      { name: "hardcoded", type: "yes/no" },
      { name: "complete", type: "yes/no" },
      { name: "verified", type: "yes/no", example: "the values above came from the file, not its name" },
      { name: "parsed.resolution", type: "text" },
      { name: "parsed.codec", type: "text" },
      { name: "parsed.hdr", type: "list" },
      { name: "parsed.bitDepth", type: "number" },
      { name: "parsed.dolbyVision", type: "yes/no" },
      { name: "parsed.hdrFallback", type: "yes/no" },
      { name: "parsed.title", type: "text" },
    ],
  },
  {
    tier: "reported",
    title: "From the indexer",
    items: [
      { name: "releaseName", type: "text", example: 'releaseName matches "(?i)\\bIMAX\\b"' },
      { name: "sizeGB", type: "number", example: "the whole release" },
      { name: "sizePerEpisodeGB", type: "number", example: "per-episode share; -1 for an uncountable season pack" },
      { name: "ageDays", type: "number", example: "-1 when the indexer reported no date" },
      { name: "grabs", type: "number" },
      { name: "passworded", type: "yes/no" },
      { name: "indexer", type: "text" },
      { name: "querySource", type: "text" },
      { name: "library", type: "yes/no", example: "already in your library" },
    ],
  },
  {
    tier: "community",
    title: "From AvailNZB",
    note: "A rule reading any of these skips releases nobody has reported, so turning one on never empties a result list.",
    items: [
      { name: "avail.status", type: "text", example: '"available", "unavailable" or "unknown"' },
      { name: "avail.known", type: "yes/no" },
      { name: "avail.onMyBackbone", type: "yes/no", example: "healthy on a backbone your providers use" },
      { name: "avail.checkedDaysAgo", type: "number", example: "-1 when the record has no timestamp" },
      { name: "avail.compression", type: "text", example: '"rar", "7z"' },
    ],
  },
  {
    tier: "community",
    title: "From SeaDex",
    note: "SeaDex (releases.moe) recommends releases per anime title, matched here by release group. Only Kitsu-addressed anime requests are looked up, so rules reading these skip everything else.",
    items: [
      { name: "seadex.known", type: "yes/no", example: "SeaDex has an entry for this title" },
      { name: "seadex.best", type: "yes/no", example: "this group made a release marked best for this title" },
      { name: "seadex.alternative", type: "yes/no", example: "recommended for this title, without the best mark" },
    ],
  },
  {
    tier: "measured",
    title: "From ffprobe",
    note: "Library releases only — a fresh indexer hit has never been opened. Rules reading these skip everything unprobed.",
    items: [
      { name: "probed.height", type: "number" },
      { name: "probed.width", type: "number" },
      { name: "probed.videoCodec", type: "text", example: '"hevc"' },
      { name: "probed.audioCodec", type: "text" },
      { name: "probed.profile", type: "text", example: '"Main 10"' },
      { name: "probed.bitDepth", type: "number" },
      { name: "probed.hdr", type: "text", example: '"HDR10", "HDR10+", "HLG", or "" for SDR' },
      { name: "probed.dolbyVision", type: "yes/no" },
      { name: "probed.hasHDRFallback", type: "yes/no" },
      { name: "probed.dynamicRange", type: "text", example: '"DV + HDR10", "DV only", "HDR10"' },
    ],
  },
  {
    tier: "inferred",
    title: "Detected traits",
    note: "Every trait the parser found, by key. `\"remux\" in traits` reaches anything the baseline has an opinion about, without a separate control for each one.",
    items: TRAIT_KEYS.map((key) => ({ name: key, type: "trait" })),
  },
  {
    title: "About the result set",
    note: "count(), exists() and none() judge the whole result set instead of one release, so a rule can hold back unless something better is actually on offer — reject the shaky 4K only when exists(resolution == \"2160p\" and \"remux\" in traits). The condition inside reads the same attributes; releases missing a tier it needs are not counted, and the rule skips when no release in the set carries it. any() is exists() under another name.",
    items: [
      { name: "count(…)", insert: "count()", type: "number", example: 'count(resolution == "2160p") < 3' },
      { name: "exists(…)", insert: "exists()", type: "yes/no", example: "exists(library)" },
      { name: "none(…)", insert: "none()", type: "yes/no", example: 'none(quality == "WEB-DL")' },
    ],
  },
  {
    tier: "inferred",
    title: "About the request",
    items: [
      { name: "kind", type: "text", example: '"movie", "series", "anime_movie", "anime_show"' },
      { name: "isAnime", type: "yes/no" },
      { name: "season", type: "number" },
      { name: "episode", type: "number" },
      { name: "title", type: "text", example: "the title that was searched for" },
    ],
  },
]

// Fields from the pre-migration schema. They are read once, to build a
// ranking profile for a config that predates it, and ignored from then on —
// so a profile created now should not carry them, and copying one forward
// would only preserve values that can no longer be edited.
const LEGACY_FIELDS = [
  "allowed_resolutions", "blocked_resolutions", "allowed_qualities", "blocked_qualities",
  "allowed_codecs", "blocked_codecs", "require_hdr", "allowed_hdrs", "blocked_hdrs",
  "required_keywords", "excluded_keywords", "allowed_languages", "blocked_languages",
  "preferred_languages",
]

// withoutLegacyFields strips the pre-migration fields from a profile.
export function withoutLegacyFields(profile) {
  const out = { ...profile }
  LEGACY_FIELDS.forEach((field) => { delete out[field] })
  return out
}

export function defaultProfile(name = "New Profile") {
  return { name, preset: DEFAULT_PRESET, rules: [] }
}

export function formatScore(value) {
  if (typeof value !== "number" || Number.isNaN(value)) return "0"
  return value > 0 ? `+${value.toLocaleString()}` : value.toLocaleString()
}

// What a rule does. Three actions, not two: anything unrecorded is a score
// rule, but "reject" and "limit" are not interchangeable, and reading the
// action as a boolean is what made a limit rule export as a score rule worth
// nothing.
export function ruleAction(rule) {
  return rule?.action === "reject" || rule?.action === "limit" ? rule.action : "score"
}

// What a limit rule keeps when it does not say. A limit of zero is not a limit
// — the backend refuses the whole profile over it — so a count is never below
// one.
export const DEFAULT_LIMIT_COUNT = 3

function limitCount(value) {
  const count = Math.trunc(Number(value))
  return Number.isFinite(count) && count >= 1 ? count : DEFAULT_LIMIT_COUNT
}

// exportedProfile is the form a profile travels in: a preset plus rules, with
// only the fields that survive a round trip. It is what a share code carries,
// and profileFromParsed is what reads it back — one shape, written and read in
// one place each, because the two drifting apart is what broke sharing before.
function exportedProfile(profile) {
  return {
    streamnzb_profile: 1,
    name: (profile.name || "").trim(),
    preset: profile.preset || DEFAULT_PRESET,
    rules: (profile.rules || []).map((rule) => {
      const out = { name: rule.name || "", when: rule.when || "" }
      const action = ruleAction(rule)
      if (action === "reject") {
        out.action = "reject"
      } else if (action === "limit") {
        out.action = "limit"
        out.count = limitCount(rule.count)
      } else if (rule.points) {
        out.points = rule.points
      }
      if (rule.scope && rule.scope !== "all") out.scope = rule.scope
      if (rule.enabled === false) out.enabled = false
      return out
    }),
  }
}

// Profile share codes: the profile JSON, gzip-compressed and base64url-encoded
// behind a versioned prefix, so a whole profile travels as one pasteable string.
const SHARE_CODE_PREFIX = "SNZBP1:"

function toBase64Url(bytes) {
  let binary = ""
  const chunk = 0x8000
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk))
  }
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "")
}

function fromBase64Url(text) {
  const binary = atob(text.replaceAll("-", "+").replaceAll("_", "/"))
  return Uint8Array.from(binary, (c) => c.charCodeAt(0))
}

export async function encodeProfileShareCode(profile) {
  const json = JSON.stringify(exportedProfile(profile))
  const stream = new Blob([new TextEncoder().encode(json)]).stream().pipeThrough(new CompressionStream("gzip"))
  const packed = new Uint8Array(await new Response(stream).arrayBuffer())
  return SHARE_CODE_PREFIX + toBase64Url(packed)
}

// Share codes travel through chats and phone keyboards that quietly rewrite
// them: zero-width/invisible characters get injected around pasted text,
// "smart punctuation" turns the hyphens of base64url into en/em dashes, and a
// long code arrives wrapped across several lines. Undo all of that, then pick
// the code out of any surrounding prose.
const invisibleCharsRE = /[\u00AD\u200B-\u200F\u2060\uFEFF]/g
const dashVariantsRE = /[\u2010-\u2015\u2212]/g

// How many whitespace-separated pieces of a paste are worth trying. Each one
// costs a decode attempt, and no chat client wraps a code into more lines than
// this.
const maxShareCodeSegments = 8

// shareCodeCandidates lists the payloads a pasted blob might hold, longest
// first. Whitespace inside a code is ambiguous — it separates the code from a
// trailing "enjoy!" as readily as it wraps the code across two lines — and only
// a decode attempt can tell those apart, so every prefix that ends at a break
// is a candidate.
function shareCodeCandidates(code) {
  const cleaned = (code || "")
    .replace(invisibleCharsRE, "")
    .replace(dashVariantsRE, "-")
  const match = cleaned.match(/SNZBP1:[A-Za-z0-9\-_+/=\s]+/i)
  const picked = (match ? match[0] : cleaned).trim()
  const segments = picked.split(/\s+/).slice(0, maxShareCodeSegments)
  const candidates = []
  for (let n = segments.length; n >= 1; n--) candidates.push(segments.slice(0, n).join(""))
  return candidates
}

// unpackShareCode returns the profile a payload carries, or undefined if it
// does not carry one. gzip is self-checking, so this doubles as the test of
// whether a candidate is the whole code and nothing else.
async function unpackShareCode(payload) {
  try {
    const bytes = fromBase64Url(payload)
    const stream = new Blob([bytes]).stream().pipeThrough(new DecompressionStream("gzip"))
    return JSON.parse(await new Response(stream).text())
  } catch {
    return undefined
  }
}

export async function decodeProfileShareCode(code) {
  const candidates = shareCodeCandidates(code)
  if (candidates[0].slice(0, SHARE_CODE_PREFIX.length).toUpperCase() !== SHARE_CODE_PREFIX) {
    throw new Error("Not a StreamNZB profile code.")
  }
  let parsed
  for (const candidate of candidates) {
    parsed = await unpackShareCode(candidate.slice(SHARE_CODE_PREFIX.length))
    if (parsed !== undefined) break
  }
  if (parsed === undefined) {
    throw new Error("The code is damaged or incomplete.")
  }
  // A code made before presets carries a hand-tuned ranking profile that this
  // editor can no longer express, so say that rather than complain about a
  // missing marker the code was never going to have.
  if (parsed && typeof parsed === "object" && parsed.streamnzb_profile !== 1 && parsed.ranking) {
    throw new Error("That code predates filter presets and can no longer be imported.")
  }
  return profileFromParsed(parsed)
}

// profileFromParsed reads what a share code carried. It is strict about the
// shape and says which rule is wrong when one is: a code that arrives damaged
// should fail loudly rather than import half a ruleset.
function profileFromParsed(parsed) {
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed) || parsed.streamnzb_profile !== 1) {
    throw new Error("The code does not contain a filter profile.")
  }
  const name = typeof parsed.name === "string" ? parsed.name.trim() : ""
  if (!name) throw new Error("The profile needs a name.")

  const preset = PRESETS.some((p) => p.key === parsed.preset) ? parsed.preset : DEFAULT_PRESET
  const rawRules = Array.isArray(parsed.rules) ? parsed.rules : []
  const rules = rawRules.map((rule, i) => {
    if (!rule || typeof rule !== "object") throw new Error(`Rule ${i + 1} is not an object.`)
    const label = `Rule ${i + 1}${rule.name ? ` (${rule.name})` : ""}`
    if (typeof rule.when !== "string" || !rule.when.trim()) {
      throw new Error(`${label} has no condition.`)
    }
    const out = { name: String(rule.name || `Rule ${i + 1}`), when: rule.when }
    const action = ruleAction(rule)
    if (action === "reject") {
      out.action = "reject"
    } else if (action === "limit") {
      // The server refuses a profile whose limit keeps nothing, so a count
      // that would be dropped there is refused here, where it can still be
      // pointed at the rule that carries it.
      const count = Math.trunc(Number(rule.count))
      if (!Number.isFinite(count) || count < 1) {
        throw new Error(`${label} limits releases but does not say how many to keep.`)
      }
      out.action = "limit"
      out.count = count
    } else if (Number.isFinite(rule.points)) {
      out.points = rule.points
    }
    if (typeof rule.scope === "string" && rule.scope !== "all") out.scope = rule.scope
    if (rule.enabled === false) out.enabled = false
    return out
  })

  return { name, preset, rules }
}

// Rules as text.
//
// The card editor is one rule per card, which is the wrong shape for writing
// or reviewing twenty of them at once. The text form is the same rules as
// lines:
//
//   Atmos [movie]: score -800 if "atmos" in traits
//   DV without HDR fallback: reject if dolbyVision and not hdrFallback
//   4K cap [off]: keep 3 if resolution == "2160p"
//
// Everything after `if` is the condition, carried through untouched. This
// grammar wraps conditions and never parses them, so there is still exactly
// one thing that understands `"atmos" in traits` — the same expression
// language the server compiles.

const scopeKeys = new Set(CONTENT_KINDS.map((kind) => kind.key))

// A rule line is NAME [tags]: ACTION if CONDITION. Tags are the scope, "off",
// or both; the brackets and the name are optional.
const ruleBodyRE = /^\s*(?:score\s+([+-]?\d+)|reject|keep\s+(\d+))\s+if\b\s*([\s\S]*)$/i
const ruleTagsRE = /^(.*?)\s*\[([^\]]*)\]\s*$/

function ruleToText(rule) {
  const tags = []
  if (rule.scope && rule.scope !== "all") tags.push(rule.scope)
  if (rule.enabled === false) tags.push("off")
  const action = ruleAction(rule)
  const verb = action === "reject" ? "reject"
    : action === "limit" ? `keep ${limitCount(rule.count)}`
    : `score ${Math.trunc(Number(rule.points)) || 0}`
  // A condition written across several lines folds onto one: the text form is
  // a line per rule, and the expression language does not care where the
  // whitespace falls.
  const when = (rule.when || "").replace(/\s+/g, " ").trim()
  const head = `${(rule.name || "").trim()}${tags.length ? ` [${tags.join(", ")}]` : ""}`
  return `${head}: ${verb} if ${when}`
}

export function rulesToText(rules = []) {
  return rules.map(ruleToText).join("\n")
}

// splitRuleLine finds the ":" that divides the name from the action: the first
// one whose remainder reads as an action, so a rule may be named "Tier 1: NTb"
// and a condition may contain a colon of its own.
function splitRuleLine(line) {
  for (let at = line.indexOf(":"); at >= 0; at = line.indexOf(":", at + 1)) {
    const body = ruleBodyRE.exec(line.slice(at + 1))
    if (body) return { head: line.slice(0, at), body }
  }
  return null
}

// parseRuleTags reads a trailing [movie, off]. Brackets only mean tags when
// every token is one, so a rule named "Remux [2160p]" stays named that.
function parseRuleTags(head) {
  const match = ruleTagsRE.exec(head)
  if (!match) return { name: head.trim() }
  const tokens = match[2].split(",").map((token) => token.trim().toLowerCase()).filter(Boolean)
  if (!tokens.length || !tokens.every((token) => token === "off" || scopeKeys.has(token))) {
    return { name: head.trim() }
  }
  return {
    name: match[1].trim(),
    scope: tokens.find((token) => token !== "off"),
    off: tokens.includes("off"),
  }
}

// rulesFromText parses the whole block, or throws naming the line that stopped
// it. Partial results are never returned: half a ruleset silently replacing a
// whole one is the failure worth avoiding here.
export function rulesFromText(text) {
  const lines = String(text || "").split(/\r?\n/)
  const rules = []
  lines.forEach((raw, i) => {
    const line = raw.trim()
    if (!line) return
    const at = `Line ${i + 1}`
    const split = splitRuleLine(line)
    if (!split) {
      throw new Error(`${at}: expected “Name: score 100 if <condition>”, or reject / keep 3 in place of score.`)
    }
    const [, points, count, when] = split.body
    if (!when.trim()) throw new Error(`${at}: the rule has no condition.`)

    const tags = parseRuleTags(split.head)
    const rule = { name: tags.name, when: when.trim() }
    if (count !== undefined) {
      if (Number(count) < 1) throw new Error(`${at}: a limit has to keep at least one release.`)
      rule.action = "limit"
      rule.count = Number(count)
    } else if (points !== undefined) {
      rule.points = Number(points)
    } else {
      rule.action = "reject"
    }
    if (tags.scope) rule.scope = tags.scope
    if (tags.off) rule.enabled = false
    rules.push(rule)
  })
  return rules
}
