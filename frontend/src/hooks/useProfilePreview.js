import { useEffect, useMemo, useRef, useState } from "react"
import { apiFetch } from "@/api"

// SAMPLE_TITLES seed the preview with releases that exercise the settings most
// worth seeing the effect of: a 4K remux with Dolby Vision, a clean 1080p
// encode, an SD rip, and a camcorder that most profiles reject.
export const SAMPLE_TITLES = [
  "The Matrix 1999 2160p UHD BluRay REMUX DV HDR HEVC TrueHD 7.1 Atmos-FraMeSToR",
  "The Matrix 1999 1080p BluRay DDP5.1 x264-GRP",
  "The Matrix 1999 720p HDTV XviD-TRASH",
  "The.Matrix.1999.1080p.WEBRip.HDCAM.x264",
]

// useProfilePreview runs the profile being edited against a set of release
// names and returns what it would do to each.
//
// One request serves every consumer: the rules editor reads it for per-rule
// match counts, the bench reads it for the full breakdown. Two components
// asking the same question separately would double the traffic and let them
// disagree on screen, which is worse than either.
export function useProfilePreview(profile, { titles, kind, targetTitle } = {}) {
  const [results, setResults] = useState(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState("")

  // The profile is a large object rebuilt on every keystroke; comparing its
  // serialization is what keeps the effect from firing on identity alone.
  const profileKey = useMemo(() => JSON.stringify(profile ?? null), [profile])
  const titleKey = useMemo(() => (titles || []).join("\n"), [titles])
  const requestRef = useRef(0)

  useEffect(() => {
    const wanted = (titles || []).map((t) => t.trim()).filter(Boolean)
    if (!profile || wanted.length === 0) {
      setResults(null)
      setError("")
      return undefined
    }

    const token = ++requestRef.current
    setLoading(true)
    const handle = window.setTimeout(() => {
      apiFetch("/api/ranking/explain", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          titles: wanted,
          profile,
          kind: kind && kind !== "all" ? kind : undefined,
          target_title: targetTitle?.trim() || undefined,
        }),
      })
        .then((data) => {
          // A slower earlier request must not overwrite a newer answer.
          if (token !== requestRef.current) return
          setResults(data?.results || [])
          setError("")
        })
        .catch((err) => {
          if (token !== requestRef.current) return
          setResults(null)
          setError(err?.message || "Could not evaluate those release names.")
        })
        .finally(() => {
          if (token === requestRef.current) setLoading(false)
        })
    }, 400)

    return () => window.clearTimeout(handle)
    // profileKey and titleKey stand in for the objects they serialize.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [profileKey, titleKey, kind, targetTitle])

  // ruleStats counts, per rule name, how many sampled releases it paid out on
  // and how many it could not be judged against. It is what turns a condition
  // a user just typed into visible feedback.
  const ruleStats = useMemo(() => {
    const stats = {}
    if (!results) return stats
    results.forEach((result) => {
      (result.matched || []).forEach((m) => {
        stats[m.name] = stats[m.name] || { matched: 0, skipped: 0, rejected: 0, limited: 0 }
        stats[m.name].matched += 1
      })
      ;(result.skipped_rules || []).forEach((note) => {
        const name = note.split(":")[0]
        stats[name] = stats[name] || { matched: 0, skipped: 0, rejected: 0, limited: 0 }
        stats[name].skipped += 1
      })
      ;(result.limited || []).forEach((name) => {
        stats[name] = stats[name] || { matched: 0, skipped: 0, rejected: 0, limited: 0 }
        stats[name].limited += 1
      })
      ;(result.rejections || []).forEach((reason) => {
        if (!reason.startsWith("rule: ")) return
        // A cap reads "rule: <name> (over the limit of 3)".
        const body = reason.slice(6)
        const capped = body.match(/^(.*) \(over the limit of \d+\)$/)
        // A capped release is already counted under `limited`, which covers
        // everything falling under the cap rather than only what it dropped.
        if (capped) return
        stats[body] = stats[body] || { matched: 0, skipped: 0, rejected: 0, limited: 0 }
        stats[body].rejected += 1
      })
    })
    return stats
  }, [results])

  return { results, ruleStats, loading, error, sampleCount: (titles || []).filter((t) => t.trim()).length }
}
