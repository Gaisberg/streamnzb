// Define library share codes and remote sources. A library is a named bundle
// of define rules — release-group tiers, community classifications — shared
// by every filter profile through matched("Name"). Its code rides the shared
// container in shareCodes.js behind its own prefix, and a library imported
// from a URL refreshes the way a linked profile does, with one difference: a
// library is the maintainer's data, so a refresh replaces the rules wholesale
// rather than merging. A user who wants their own version of one define
// writes a profile rule under the same name — the profile's own rule shadows
// the library's.
//
// A remote library may also be a plain rule-text file — one define per line
// in the editor's own grammar, the natural output of a generator — instead of
// a share code. Lines starting with # are comments and ignored; whichever
// form arrives, what is stored is the parsed rules plus a canonical share
// code as the applied-version record.

import { maxConditionLength, maxProfileRules, ruleKey, rulesFromText } from "@/lib/profiles"
import { diffLinkedProfiles } from "@/lib/remoteProfiles"
import {
  encodeShareCode, fetchShareCodeText, maxSharedNameLength, requireSchemaVersion,
  resolveShareCode, validateSourceUrl,
} from "@/lib/shareCodes"

export const SHARE_CODE_PREFIX = "SNZBD1:"

// DEFINE_SCHEMA_VERSION is the define-library payload's schema version — the
// value of the streamnzb_define_library marker. requireSchemaVersion in
// shareCodes.js says when it moves and when it must not.
export const DEFINE_SCHEMA_VERSION = 1

// validatedDefineRules is the one gate every imported rule list passes,
// whatever form it arrived in: define-only, named, and bounded. Define-only
// is the library contract — a library is data for profiles to reference,
// never policy, so a score or reject rule riding in on a refresh is refused
// before it can change what every profile does.
function validatedDefineRules(rawRules) {
  if (rawRules.length > maxProfileRules) {
    throw new Error(`The library carries ${rawRules.length} rules; the most it can hold is ${maxProfileRules}.`)
  }
  const seen = new Set()
  return rawRules.map((rule, i) => {
    if (!rule || typeof rule !== "object") throw new Error(`Rule ${i + 1} is not an object.`)
    const name = String(rule.name || "").trim()
    const label = `Rule ${i + 1}${name ? ` (${name})` : ""}`
    if (!name) throw new Error(`${label} has no name; a define exists only to be referenced.`)
    if (name.length > maxSharedNameLength) throw new Error(`${label} has a name longer than ${maxSharedNameLength} characters.`)
    if (rule.action !== "define") throw new Error(`${label} is not a define rule; a library may only carry defines.`)
    if (typeof rule.when !== "string" || !rule.when.trim()) throw new Error(`${label} has no condition.`)
    if (rule.when.length > maxConditionLength) {
      throw new Error(`${label} has a condition longer than ${maxConditionLength} characters.`)
    }
    if (seen.has(ruleKey(name))) throw new Error(`More than one define is named “${name}”.`)
    seen.add(ruleKey(name))
    const out = { name, when: rule.when, action: "define" }
    if (typeof rule.scope === "string" && rule.scope !== "all") out.scope = rule.scope
    if (rule.enabled === false) out.enabled = false
    return out
  })
}

// exportedDefineLibrary is the form a library travels in — only the fields
// that survive a round trip, mirroring exportedProfile.
function exportedDefineLibrary(library) {
  return {
    streamnzb_define_library: DEFINE_SCHEMA_VERSION,
    name: (library.name || "").trim(),
    rules: (library.rules || []).map((rule) => {
      const out = { name: rule.name || "", when: rule.when || "", action: "define" }
      if (rule.scope && rule.scope !== "all") out.scope = rule.scope
      if (rule.enabled === false) out.enabled = false
      return out
    }),
  }
}

// defineLibraryFromParsed reads what a code carried, strictly: a fresh object
// with only the known fields, bounded, or a loud failure.
function defineLibraryFromParsed(parsed) {
  requireSchemaVersion(parsed, "streamnzb_define_library", DEFINE_SCHEMA_VERSION,
    "The code does not contain a define library.")
  const name = typeof parsed.name === "string" ? parsed.name.trim() : ""
  if (!name) throw new Error("The library needs a name.")
  if (name.length > maxSharedNameLength) throw new Error("The library name is too long.")
  const rules = validatedDefineRules(Array.isArray(parsed.rules) ? parsed.rules : [])
  return { name, rules }
}

export async function encodeDefineLibraryShareCode(library) {
  return encodeShareCode(SHARE_CODE_PREFIX, exportedDefineLibrary(library))
}

export async function resolveDefineLibraryShareCode(code) {
  const { code: matched, parsed } = await resolveShareCode(
    SHARE_CODE_PREFIX, code, "Not a StreamNZB define library code.")
  return { code: matched, library: defineLibraryFromParsed(parsed) }
}

export async function decodeDefineLibraryShareCode(code) {
  return (await resolveDefineLibraryShareCode(code)).library
}

// defineLibraryFromPaste reads whatever was pasted into the import dialog: a
// share code when one is present, plain rule text otherwise — the same two
// forms a library URL may serve.
export async function defineLibraryFromPaste(text) {
  if (String(text || "").toUpperCase().includes(SHARE_CODE_PREFIX)) {
    return decodeDefineLibraryShareCode(text)
  }
  return defineLibraryFromRuleText(text, "")
}

// defineRulesFromText reads define rules out of plain rule text: the editor's
// own one-line grammar, define rules only, # lines ignored. The grammar
// reports errors by line number, so blank stand-ins keep the numbers pointing
// at the text the writer is actually looking at.
export function defineRulesFromText(text) {
  const withoutComments = String(text || "")
    .split(/\r?\n/)
    .map((line) => (line.trimStart().startsWith("#") ? "" : line))
    .join("\n")
  return validatedDefineRules(rulesFromText(withoutComments))
}

// defineLibraryFromRuleText reads a whole remote rule-text file — a library
// needs at least one define, and a name the file itself cannot carry.
export function defineLibraryFromRuleText(text, name) {
  const rules = defineRulesFromText(text)
  if (!rules.length) throw new Error("The file contains no define rules.")
  return { name: (name || "").trim() || "Define Library", rules }
}

// libraryNameFromUrl turns the fetched file's name into the library's default
// name — a rule-text file carries no name of its own.
function libraryNameFromUrl(url) {
  try {
    const base = decodeURIComponent(new URL(url).pathname.split("/").pop() || "")
    return base.replace(/\.[^.]*$/, "").replace(/[-_]+/g, " ").trim()
  } catch {
    return ""
  }
}

// resolveDefineLibraryText reads whatever a library URL served: a share code
// when one is present, plain rule text otherwise. Either way the canonical
// share code of what was parsed is returned as the applied-version record —
// for a text file it is synthesized, so the stored snapshot is always a code
// the config layer can bound and recognize.
async function resolveDefineLibraryText(text, fallbackName) {
  if (String(text || "").toUpperCase().includes(SHARE_CODE_PREFIX)) {
    return resolveDefineLibraryShareCode(text)
  }
  const library = defineLibraryFromRuleText(text, fallbackName)
  return { code: await encodeDefineLibraryShareCode(library), library }
}

// fetchRemoteDefineLibrary is the whole of importing from a URL: validate,
// fetch, decode — shaped like fetchRemoteProfile so the sharing hook can use
// it unchanged. The `profile` key is the hook's word for "the thing imported".
export async function fetchRemoteDefineLibrary(rawUrl) {
  const url = validateSourceUrl(rawUrl)
  const text = await fetchShareCodeText(url)
  try {
    const { code, library } = await resolveDefineLibraryText(text, libraryNameFromUrl(url))
    return { url, code, profile: library }
  } catch (err) {
    throw new Error(`The URL did not return a define library. ${err?.message || ""}`.trim())
  }
}

// mergeDefineLibraryUpstream replaces the rules and keeps everything else
// local — the library is the maintainer's data, so unlike a linked filter
// profile there is no merge: local edits last until the next refresh, and a
// lasting override belongs in the profile, where its rule shadows the
// library's.
export function mergeDefineLibraryUpstream(local, upstream) {
  return { ...local, rules: upstream.rules || [] }
}

// checkDefineLibraryForUpdate mirrors checkForUpdate: "current" means
// applying would change nothing, decided by the rule diff rather than the
// bytes — a text file and a code that parse to the same rules are the same
// library.
export async function checkDefineLibraryForUpdate(library) {
  const text = await fetchShareCodeText(library.source.url)
  let resolved
  try {
    resolved = await resolveDefineLibraryText(text, library.name)
  } catch (err) {
    throw new Error(`The URL did not return a define library. ${err?.message || ""}`.trim())
  }
  const { code, library: upstream } = resolved
  const merged = mergeDefineLibraryUpstream(library, upstream)
  const diff = diffLinkedProfiles(library, merged)
  if (diff.empty) {
    return { status: "current" }
  }
  return { status: "update", code, merged, diff, remoteName: upstream.name }
}

// summarizeDefineLibrary is the list row's one-line read of a library.
export function summarizeDefineLibrary(library) {
  const rules = (library.rules || []).filter((rule) => rule && rule.enabled !== false)
  return `${rules.length} define${rules.length === 1 ? "" : "s"}`
}
