// Format profile share codes and remote sources. A format profile is two Go
// templates and a name; its code rides the shared container in shareCodes.js
// behind its own prefix, and a profile imported from a URL refreshes the same
// way a filter profile does. There is no rule merge here — the templates are
// one piece, so a refresh replaces them with the maintainer's current version
// and the diff shows both sides. The local name stays the user's.

import {
  encodeShareCode, fetchShareCodeText, maxSharedNameLength, resolveFetched,
  resolveShareCode, validateSourceUrl,
} from "@/lib/shareCodes"

const SHARE_CODE_PREFIX = "SNZBF1:"

// maxTemplateLength bounds each template. The built-in description template is
// well under a kilobyte; anything past this is not a format, it is a payload.
const maxTemplateLength = 20000

// exportedFormatProfile is the form a format profile travels in — only the
// fields that survive a round trip, one shape written and read in one place
// each, mirroring what exportedProfile does for filter profiles.
function exportedFormatProfile(profile) {
  return {
    streamnzb_format_profile: 1,
    name: (profile.name || "").trim(),
    result_name_template: profile.result_name_template || "",
    result_description_template: profile.result_description_template || "",
  }
}

// formatProfileFromParsed reads what a code carried, strictly: a fresh object
// with only the known fields, bounded, or a loud failure.
function formatProfileFromParsed(parsed) {
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed) || parsed.streamnzb_format_profile !== 1) {
    throw new Error("The code does not contain a format profile.")
  }
  const name = typeof parsed.name === "string" ? parsed.name.trim() : ""
  if (!name) throw new Error("The profile needs a name.")
  if (name.length > maxSharedNameLength) throw new Error("The profile name is too long.")
  const template = (field, label) => {
    const value = typeof parsed[field] === "string" ? parsed[field] : ""
    if (value.length > maxTemplateLength) {
      throw new Error(`The ${label} template is longer than ${maxTemplateLength} characters.`)
    }
    return value
  }
  const profile = { name }
  const nameTemplate = template("result_name_template", "name")
  const descriptionTemplate = template("result_description_template", "description")
  if (nameTemplate) profile.result_name_template = nameTemplate
  if (descriptionTemplate) profile.result_description_template = descriptionTemplate
  return profile
}

export async function encodeFormatProfileShareCode(profile) {
  return encodeShareCode(SHARE_CODE_PREFIX, exportedFormatProfile(profile))
}

export async function resolveFormatProfileShareCode(code) {
  const { code: matched, parsed } = await resolveShareCode(
    SHARE_CODE_PREFIX, code, "Not a StreamNZB format profile code.")
  return { code: matched, profile: formatProfileFromParsed(parsed) }
}

export async function decodeFormatProfileShareCode(code) {
  return (await resolveFormatProfileShareCode(code)).profile
}

// fetchRemoteFormatProfile is the whole of importing from a URL: validate,
// fetch, decode — the format counterpart of fetchRemoteProfile.
export async function fetchRemoteFormatProfile(rawUrl) {
  const url = validateSourceUrl(rawUrl)
  const { code, profile } = await resolveFetched(resolveFormatProfileShareCode, await fetchShareCodeText(url))
  return { url, code, profile }
}

// mergeFormatUpstream replaces what the code carries and keeps everything
// else local — which for a format profile means: templates theirs, name (and
// the source record the caller maintains) yours.
export function mergeFormatUpstream(local, upstream) {
  const merged = { ...local }
  delete merged.result_name_template
  delete merged.result_description_template
  if (upstream.result_name_template) merged.result_name_template = upstream.result_name_template
  if (upstream.result_description_template) merged.result_description_template = upstream.result_description_template
  return merged
}

// diffFormatProfiles is what the confirmation dialog shows: each template
// that would change, both sides in full — templates are short enough to read
// whole, and a line-by-line diff of Go template syntax would obscure more
// than it shows. An empty template renders the built-in format, so say that
// rather than showing nothing.
export function diffFormatProfiles(current, merged) {
  const changes = []
  const compare = (field, label) => {
    const before = current[field] || ""
    const after = merged[field] || ""
    if (before === after) return
    changes.push({
      label,
      before: before || "(built-in format)",
      after: after || "(built-in format)",
    })
  }
  compare("result_name_template", "Name template")
  compare("result_description_template", "Description template")
  return { changes, empty: !changes.length }
}

// checkFormatForUpdate mirrors checkForUpdate for filter profiles: "current"
// means applying would change nothing, decided by the diff rather than the
// bytes, so local edits under an unchanged upstream still offer the
// maintainer's version back.
export async function checkFormatForUpdate(profile) {
  const text = await fetchShareCodeText(profile.source.url)
  const { code, profile: upstream } = await resolveFetched(resolveFormatProfileShareCode, text)
  const merged = mergeFormatUpstream(profile, upstream)
  const diff = diffFormatProfiles(profile, merged)
  if (diff.empty) {
    return { status: "current" }
  }
  return { status: "update", code, merged, diff, remoteName: upstream.name }
}
