// Shape and vocabulary of a filter profile, mirrored for the Filters UI.
// Keys here must match the JSON the backend stores.

export const RESOLUTIONS = [
  { key: "2160p", label: "4K" },
  { key: "1440p", label: "1440p" },
  { key: "1080p", label: "1080p" },
  { key: "720p", label: "720p" },
  { key: "576p", label: "576p" },
  { key: "480p", label: "480p" },
  { key: "360p", label: "360p" },
  { key: "240p", label: "240p" },
  { key: "unknown", label: "Unknown" },
]

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
    description: "Rejected outright while “Remove garbage titles” is on. Turn that off to decide these one by one.",
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

// The baseline a trait falls back to when a profile does not change it.
export const DEFAULT_POLICIES = {
  remux: { fetch: true, rank: 5000 }, webdl: { fetch: true, rank: 200 },
  web: { fetch: true, rank: 100 }, bluray: { fetch: true, rank: 100 },
  webrip: { fetch: true, rank: -1000 }, dvd: { fetch: false, rank: -5000 },
  hdtv: { fetch: true, rank: -5000 }, bdrip: { fetch: false, rank: -5000 },
  dvdrip: { fetch: false, rank: -5000 }, uhdrip: { fetch: false, rank: -5000 },
  vhs: { fetch: false, rank: -10000 }, webmux: { fetch: false, rank: -10000 },
  brrip: { fetch: false, rank: -10000 }, hdrip: { fetch: true, rank: -10000 },
  ppvrip: { fetch: false, rank: -10000 }, tvrip: { fetch: false, rank: -10000 },
  vhsrip: { fetch: false, rank: -10000 }, webdlrip: { fetch: false, rank: -10000 },
  satrip: { fetch: false, rank: -10000 },
  cam: { fetch: false, rank: -10000 }, telecine: { fetch: false, rank: -10000 },
  telesync: { fetch: false, rank: -10000 }, screener: { fetch: false, rank: -10000 },
  r5: { fetch: false, rank: -10000 }, pdtv: { fetch: false, rank: -10000 },
  avc: { fetch: true, rank: 500 }, hevc: { fetch: true, rank: 500 },
  av1: { fetch: true, rank: 500 }, xvid: { fetch: false, rank: -10000 },
  mpeg: { fetch: false, rank: -1000 },
  dolby_vision: { fetch: true, rank: 3000 }, hdr10plus: { fetch: true, rank: 2100 },
  hdr: { fetch: true, rank: 2000 }, sdr: { fetch: true, rank: 0 },
  "10bit": { fetch: true, rank: 100 },
  dts_lossless: { fetch: true, rank: 2000 }, truehd: { fetch: true, rank: 2000 },
  atmos: { fetch: true, rank: 1000 }, dolby_digital_plus: { fetch: true, rank: 150 },
  dts_lossy: { fetch: true, rank: 100 }, aac: { fetch: true, rank: 100 },
  dolby_digital: { fetch: true, rank: 50 }, flac: { fetch: true, rank: 0 },
  opus: { fetch: true, rank: 0 }, pcm: { fetch: true, rank: 0 },
  mp3: { fetch: false, rank: -1000 }, clean_audio: { fetch: false, rank: -10000 },
  surround: { fetch: true, rank: 0 }, stereo: { fetch: true, rank: 0 },
  mono: { fetch: false, rank: 0 },
  edition: { fetch: true, rank: 100 }, proper: { fetch: true, rank: 20 },
  repack: { fetch: true, rank: 20 }, network: { fetch: true, rank: 0 },
  retail: { fetch: true, rank: 0 }, subbed: { fetch: true, rank: 0 },
  scene: { fetch: true, rank: 0 }, uncensored: { fetch: true, rank: 0 },
  hardcoded: { fetch: true, rank: 0 }, documentary: { fetch: false, rank: -250 },
  converted: { fetch: false, rank: -1000 }, dubbed: { fetch: true, rank: -1000 },
  "3d": { fetch: false, rank: -10000 }, upscaled: { fetch: false, rank: -10000 },
  site: { fetch: false, rank: -10000 }, size: { fetch: false, rank: -10000 },
}

export const LANGUAGE_CODES = [
  "en", "ja", "ko", "zh", "fr", "es", "la", "pt", "it", "de", "ru", "uk", "nl",
  "da", "fi", "sv", "no", "el", "pl", "cs", "sk", "hu", "ro", "bg", "sr", "hr",
  "sl", "hi", "ta", "te", "ml", "kn", "mr", "ar", "tr", "he", "fa", "vi", "id",
  "th", "ms",
]

export const LANGUAGE_OPTIONS = [
  { code: "all", name: "All Languages", isGroup: true },
  { code: "common", name: "Common Languages (en, ja, ko, zh, fr, es, de, ru, it, pt)", isGroup: true },
  { code: "anime", name: "Anime Languages (ja, en)", isGroup: true },
  { code: "non_anime", name: "Non-Anime Languages", isGroup: true },
  { code: "en", name: "English" },
  { code: "ja", name: "Japanese" },
  { code: "ko", name: "Korean" },
  { code: "zh", name: "Chinese" },
  { code: "fr", name: "French" },
  { code: "es", name: "Spanish" },
  { code: "de", name: "German" },
  { code: "ru", name: "Russian" },
  { code: "it", name: "Italian" },
  { code: "pt", name: "Portuguese" },
  { code: "uk", name: "Ukrainian" },
  { code: "nl", name: "Dutch" },
  { code: "da", name: "Danish" },
  { code: "fi", name: "Finnish" },
  { code: "sv", name: "Swedish" },
  { code: "no", name: "Norwegian" },
  { code: "el", name: "Greek" },
  { code: "pl", name: "Polish" },
  { code: "cs", name: "Czech" },
  { code: "sk", name: "Slovak" },
  { code: "hu", name: "Hungarian" },
  { code: "ro", name: "Romanian" },
  { code: "bg", name: "Bulgarian" },
  { code: "sr", name: "Serbian" },
  { code: "hr", name: "Croatian" },
  { code: "sl", name: "Slovenian" },
  { code: "hi", name: "Hindi" },
  { code: "ta", name: "Tamil" },
  { code: "te", name: "Telugu" },
  { code: "ml", name: "Malayalam" },
  { code: "kn", name: "Kannada" },
  { code: "mr", name: "Marathi" },
  { code: "ar", name: "Arabic" },
  { code: "tr", name: "Turkish" },
  { code: "he", name: "Hebrew" },
  { code: "fa", name: "Persian" },
  { code: "vi", name: "Vietnamese" },
  { code: "id", name: "Indonesian" },
  { code: "th", name: "Thai" },
  { code: "ms", name: "Malay" },
  { code: "la", name: "Latin" },
]

// Each of these resolves to a set of language codes.
export const LANGUAGE_GROUPS = ["anime", "common", "non_anime", "all"]

export const SORT_KEYS = [
  { key: "resolution", label: "Resolution", hint: "Highest tier first" },
  { key: "rank", label: "Score", hint: "Highest scoring first" },
  { key: "title_ratio", label: "Title match", hint: "Closest match to what was requested" },
  { key: "size", label: "Size", hint: "Largest first" },
  { key: "age", label: "Age", hint: "Newest first" },
  { key: "grabs", label: "Grabs", hint: "Most grabbed first" },
]

// Content kinds partition every request, so exactly one profile ever applies.
// Common weighted patterns, so the usual preferences do not need a regex.
// These match on the release name, which is the only place they appear.
export const PATTERN_PRESETS = [
  { label: "Dual audio", pattern: "\\bDual[. _-]?Audio\\b", rank: 5000 },
  { label: "Multi audio", pattern: "\\bMulti[. _-]?Audio\\b", rank: 3000 },
  { label: "English dub", pattern: "\\b(ENG[. _-]?DUB|English[. _-]?Dub)\\b", rank: 4000 },
  { label: "IMAX", pattern: "\\bIMAX\\b", rank: 2000 },
  { label: "Open matte", pattern: "\\bOpen[. _-]?Matte\\b", rank: 1000 },
  { label: "Hardcoded subs", pattern: "\\bHC\\b|\\bHardsub", rank: -3000 },
]

export const CONTENT_KINDS = [
  { key: "movie", label: "Movies" },
  { key: "series", label: "Shows" },
  { key: "anime_movie", label: "Anime films" },
  { key: "anime_show", label: "Anime shows" },
]

// eligibleProfiles returns the profiles offered for a kind. An untagged
// profile is offered everywhere.
export function eligibleProfiles(profiles = [], kind) {
  return profiles.filter((p) => !p.applies_to?.length || p.applies_to.includes(kind))
}

// The traits a new profile rejects, matching defaultBlockedAttrs in
// pkg/core/config/filterprofile.go: the CAM-class rips, the fake audio dubbed
// over them, and satellite rips. Everything else jhin demotes is opened up and
// left to sort last. Keep the two lists in step.
const BLOCKED_ATTRS = ["cam", "telesync", "telecine", "screener", "r5", "pdtv", "clean_audio", "satrip"]

// NO_SCORE_FLOOR is low enough that no stack of demotions reaches it, so score
// orders results rather than rejecting them.
const NO_SCORE_FLOOR = -1000000

function defaultAttributePolicies() {
  const policies = {}
  Object.entries(DEFAULT_POLICIES).forEach(([attr, policy]) => {
    if (BLOCKED_ATTRS.includes(attr)) policies[attr] = { fetch: false, rank: policy.rank }
    else if (!policy.fetch) policies[attr] = { fetch: true, rank: policy.rank }
  })
  return policies
}

export function defaultRankProfile(name = "New Profile") {
  return {
    name,
    require: [],
    exclude: [],
    preferred: [],
    pattern_ranks: [],
    resolutions: {
      "2160p": true, "1440p": true, "1080p": true, "720p": true,
      "576p": false, "480p": false, "360p": false, "240p": false,
      unknown: true,
    },
    languages: { required: [], allowed: [], exclude: [], preferred: [] },
    options: {
      title_threshold: 0.85,
      remove_trash: true,
      remove_adult: true,
      remove_unknown_languages: false,
      allow_english: true,
      min_rank: NO_SCORE_FLOOR,
      preferred_bonus: 10000,
    },
    attributes: defaultAttributePolicies(),
  }
}

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
  return {
    name,
    ranking: defaultRankProfile(name),
    applies_to: [],
    sort_order: ["resolution", "rank", "size", "age"],
  }
}

// effectivePolicy resolves what a trait actually does: the profile's own
// setting when it has one, otherwise the baseline. The wire format calls the
// number "rank"; it is surfaced as "score" so the UI reads consistently.
export function effectivePolicy(ranking, attr) {
  const override = ranking?.attributes?.[attr]
  const base = DEFAULT_POLICIES[attr] || { fetch: true, rank: 0 }
  const policy = override || base
  return {
    fetch: policy.fetch !== false,
    score: policy.rank ?? 0,
    overridden: Boolean(override),
  }
}

export function formatScore(value) {
  if (typeof value !== "number" || Number.isNaN(value)) return "0"
  return value > 0 ? `+${value.toLocaleString()}` : value.toLocaleString()
}
