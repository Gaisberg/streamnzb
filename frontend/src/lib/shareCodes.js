// Share codes: how a profile travels as one pasteable string, and how one is
// fetched from a URL. This is the container and the transport, shared by every
// profile kind that can be exported — the payload JSON, gzip-compressed and
// base64url-encoded behind a versioned prefix ("SNZBP1:" for filter profiles,
// "SNZBF1:" for format profiles). What a payload may contain is each kind's
// own business; nothing here interprets it.
//
// The trust model for remote sources, in one place because everything below
// leans on it: the URL the user typed is the only source ever consulted —
// nothing fetched, and nothing inside a share code, can point a later refresh
// anywhere else. The fetch runs in the browser with no credentials, so the
// server never requests an address on the user's behalf, and nothing is
// applied without the user confirming a visible diff.

// maxDecodedBytes bounds what a code may inflate to. The largest real profile
// is a few hundred kilobytes of JSON; a payload past this is a decompression
// bomb, which matters now that codes arrive from URLs, not just from a person
// pasting one they meant to import.
const maxDecodedBytes = 4 * 1024 * 1024

// maxSharedNameLength bounds any name an imported payload carries.
export const maxSharedNameLength = 200

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

// encodeShareCode packs a payload behind its prefix.
export async function encodeShareCode(prefix, payload) {
  const json = JSON.stringify(payload)
  const stream = new Blob([new TextEncoder().encode(json)]).stream().pipeThrough(new CompressionStream("gzip"))
  const packed = new Uint8Array(await new Response(stream).arrayBuffer())
  return prefix + toBase64Url(packed)
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
function shareCodeCandidates(prefix, code) {
  const cleaned = (code || "")
    .replace(invisibleCharsRE, "")
    .replace(dashVariantsRE, "-")
  const match = cleaned.match(new RegExp(`${prefix}[A-Za-z0-9\\-_+/=\\s]+`, "i"))
  const picked = (match ? match[0] : cleaned).trim()
  const segments = picked.split(/\s+/).slice(0, maxShareCodeSegments)
  const candidates = []
  for (let n = segments.length; n >= 1; n--) candidates.push(segments.slice(0, n).join(""))
  return candidates
}

// unpackShareCode returns the payload a candidate carries, or undefined if it
// does not carry one. gzip is self-checking, so this doubles as the test of
// whether a candidate is the whole code and nothing else.
async function unpackShareCode(payload) {
  try {
    const bytes = fromBase64Url(payload)
    const reader = new Blob([bytes]).stream().pipeThrough(new DecompressionStream("gzip")).getReader()
    const chunks = []
    let total = 0
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      total += value.length
      if (total > maxDecodedBytes) {
        await reader.cancel()
        return undefined
      }
      chunks.push(value)
    }
    return JSON.parse(await new Blob(chunks).text())
  } catch {
    return undefined
  }
}

// resolveShareCode decodes a pasted or fetched blob and also reports which
// exact candidate string carried the payload. That canonical form is what a
// linked profile stores as its upstream snapshot, so a later fetch can be
// compared string-to-string before anything is decoded. wrongPrefixMessage is
// the kind's own "this is not one of mine", so a filter code pasted into the
// format importer says so instead of "damaged".
export async function resolveShareCode(prefix, code, wrongPrefixMessage) {
  const candidates = shareCodeCandidates(prefix, code)
  if (candidates[0].slice(0, prefix.length).toUpperCase() !== prefix) {
    throw new Error(wrongPrefixMessage)
  }
  for (const candidate of candidates) {
    const parsed = await unpackShareCode(candidate.slice(prefix.length))
    if (parsed !== undefined) return { code: candidate, parsed }
  }
  throw new Error("The code is damaged or incomplete.")
}

// --- Remote sources: fetching a share code from a URL ---

// maxFetchedBytes bounds the response body. A share code is a few kilobytes;
// a response past this is not a profile, whatever the URL claimed.
const maxFetchedBytes = 256 * 1024

const fetchTimeoutMs = 15000

// sourceHost is the part of the URL worth showing prominently: whom the user
// is trusting, without the path noise.
export function sourceHost(url) {
  try {
    return new URL(url).host
  } catch {
    return ""
  }
}

// validateSourceUrl returns the trimmed URL or throws. https only: a plain
// http source could be rewritten by anyone on the path, which for a standing
// "this host can propose config changes" grant is not a risk worth carrying.
export function validateSourceUrl(raw) {
  const trimmed = (raw || "").trim()
  let parsed
  try {
    parsed = new URL(trimmed)
  } catch {
    throw new Error("That is not a valid URL.")
  }
  if (parsed.protocol !== "https:" || !parsed.host) {
    throw new Error("Only https:// URLs can be linked.")
  }
  return trimmed
}

// fetchShareCodeText fetches what the URL serves, as text. Credentials are
// never sent, the response is size-capped, and the errors stay distinct so
// "the host is down" never reads as "the file is not a profile". The body is
// never echoed into an error: whatever an arbitrary URL returned does not
// belong in the UI.
export async function fetchShareCodeText(url) {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), fetchTimeoutMs)
  let response
  try {
    response = await fetch(url, {
      credentials: "omit",
      cache: "no-store",
      signal: controller.signal,
    })
  } catch {
    // The browser folds CORS refusals and network failures into one opaque
    // error, so the message has to name both.
    throw new Error("Could not fetch that URL. The host may be unreachable, or it does not allow browser access (CORS). Raw file hosts like raw.githubusercontent.com work.")
  } finally {
    clearTimeout(timer)
  }
  if (!response.ok) {
    throw new Error(`The URL answered HTTP ${response.status}.`)
  }
  const reader = response.body?.getReader()
  if (!reader) return await response.text()
  const chunks = []
  let total = 0
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    total += value.length
    if (total > maxFetchedBytes) {
      await reader.cancel()
      throw new Error("The URL returned far more data than a profile code.")
    }
    chunks.push(value)
  }
  return await new Blob(chunks).text()
}

// resolveFetched decodes what a URL served, wrapping the decoder's message so
// a fetch that returned a README reads as that rather than as a damaged
// paste. resolve is the profile kind's own resolver.
export async function resolveFetched(resolve, text) {
  try {
    return await resolve(text)
  } catch (err) {
    throw new Error(`The URL did not return a profile share code. ${err?.message || ""}`.trim())
  }
}
