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
// limiting caps how many of the matching ones you are offered — the one thing a
// condition about a single release cannot say on its own.
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
  const json = JSON.stringify(withoutLegacyFields(profile))
  const stream = new Blob([new TextEncoder().encode(json)]).stream().pipeThrough(new CompressionStream("gzip"))
  const packed = new Uint8Array(await new Response(stream).arrayBuffer())
  return SHARE_CODE_PREFIX + toBase64Url(packed)
}

// Share codes travel through chats and phone keyboards that quietly rewrite
// them: zero-width/invisible characters get injected around pasted text, and
// "smart punctuation" turns the hyphens of base64url into en/em dashes.
// Undo all of that, then pick the code out of any surrounding prose.
const invisibleCharsRE = /[\s\u00AD\u200B-\u200F\u2060\uFEFF]/g
const dashVariantsRE = /[\u2010-\u2015\u2212]/g

function sanitizeShareCodeInput(code) {
  const cleaned = (code || "")
    .replace(invisibleCharsRE, "")
    .replace(dashVariantsRE, "-")
  const match = cleaned.match(/SNZBP1:[A-Za-z0-9\-_+/=]+/i)
  return match ? match[0] : cleaned
}

export async function decodeProfileShareCode(code) {
  const trimmed = sanitizeShareCodeInput(code)
  if (trimmed.slice(0, SHARE_CODE_PREFIX.length).toUpperCase() !== SHARE_CODE_PREFIX) {
    throw new Error("Not a StreamNZB profile code.")
  }
  let profile
  try {
    const bytes = fromBase64Url(trimmed.slice(SHARE_CODE_PREFIX.length))
    const stream = new Blob([bytes]).stream().pipeThrough(new DecompressionStream("gzip"))
    profile = JSON.parse(await new Response(stream).text())
  } catch {
    throw new Error("The code is damaged or incomplete.")
  }
  if (!profile || typeof profile !== "object" || typeof profile.name !== "string" || !profile.ranking) {
    throw new Error("The code does not contain a filter profile.")
  }
  return withoutLegacyFields(profile)
}

// A profile is a preset plus rules. Export writes exactly that, formatted, so
// the file is readable in a diff and editable by hand — which is the point:
// share codes travel well through a chat window but cannot be reviewed in a
// pull request or versioned in a repository.
export function profileToJSON(profile) {
  return JSON.stringify(
    {
      streamnzb_profile: 1,
      name: (profile.name || "").trim(),
      preset: profile.preset || DEFAULT_PRESET,
      rules: (profile.rules || []).map((rule) => {
        const out = { name: rule.name || "", when: rule.when || "" }
        if (rule.action === "reject") out.action = "reject"
        else if (rule.points) out.points = rule.points
        if (rule.scope && rule.scope !== "all") out.scope = rule.scope
        if (rule.enabled === false) out.enabled = false
        return out
      }),
    },
    null,
    2,
  )
}

// profileFromJSON parses an exported profile. It is strict about the shape and
// forgiving about everything else: a file hand-edited in a repository should
// fail loudly on a typo rather than import half a ruleset.
export function profileFromJSON(text) {
  let parsed
  try {
    parsed = JSON.parse(text)
  } catch {
    throw new Error("That is not valid JSON.")
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("Expected a profile object.")
  }
  if (parsed.streamnzb_profile !== 1) {
    throw new Error("Missing \"streamnzb_profile\": 1 — is this a StreamNZB profile?")
  }
  const name = typeof parsed.name === "string" ? parsed.name.trim() : ""
  if (!name) throw new Error("The profile needs a name.")

  const preset = PRESETS.some((p) => p.key === parsed.preset) ? parsed.preset : DEFAULT_PRESET
  const rawRules = Array.isArray(parsed.rules) ? parsed.rules : []
  const rules = rawRules.map((rule, i) => {
    if (!rule || typeof rule !== "object") throw new Error(`Rule ${i + 1} is not an object.`)
    if (typeof rule.when !== "string" || !rule.when.trim()) {
      throw new Error(`Rule ${i + 1}${rule.name ? ` (${rule.name})` : ""} has no condition.`)
    }
    const out = { name: String(rule.name || `Rule ${i + 1}`), when: rule.when }
    if (rule.action === "reject") out.action = "reject"
    else if (Number.isFinite(rule.points)) out.points = rule.points
    if (typeof rule.scope === "string" && rule.scope !== "all") out.scope = rule.scope
    if (rule.enabled === false) out.enabled = false
    return out
  })

  return { name, preset, rules }
}
