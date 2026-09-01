// Known-good community templates, offered as a dropdown in the import dialog
// — one list per profile kind. Picking one only fills the URL field, so a
// template import is exactly a from-URL import: fetched, validated, added as
// a new entry that never overwrites, and linked to its source so Refresh can
// offer updates later.
//
// Curated by hand, and listing one here is an endorsement: only add sources
// that are maintained and known to import cleanly on current StreamNZB.
// label is what the dropdown shows; url must be the raw file, not a GitHub
// page around it.

export const PROFILE_TEMPLATES = [
  {
    label: "d4s87 · streamnzb-template profile",
    url: "https://raw.githubusercontent.com/d4s87/streamnzb-template/main/profile.txt",
  },
]

export const DEFINE_LIBRARY_TEMPLATES = [
  {
    label: "d4s87 · release-group defines (from Vidhin05/Releases-Regex)",
    url: "https://raw.githubusercontent.com/d4s87/streamnzb-template/refs/heads/main/generated/streamnzb-defines.txt",
  },
]

export const FORMAT_TEMPLATES = [
  {
    label: "d4s87 · streamnzb-template result format",
    url: "https://raw.githubusercontent.com/d4s87/streamnzb-template/main/formatter.txt",
  },
]
