import { useEffect, useMemo, useRef, useState } from "react"

// uniqueProfileName appends a counter until the name is free.
export function uniqueProfileName(profiles, base) {
  const taken = new Set(profiles.map((p) => p.name.trim().toLowerCase()))
  if (!taken.has(base.trim().toLowerCase())) return base
  let n = 2
  while (taken.has(`${base} ${n}`.toLowerCase())) n += 1
  return `${base} ${n}`
}

// useProfileDrafts owns the draft lifecycle for a named-profile list: a
// selected index, a draft of the selected profile, and auto-save — edits
// commit as a debounced whole-list save, so there is no Save button to
// forget. ProfileManager renders it; the pages never touch it directly.
//
// Renames are safe under auto-save: the backend matches profiles by index
// when propagating renames into stream bindings, and each debounced save
// carries one rename step, so bindings follow wherever the name ends up.
// An invalid name (empty, duplicate) holds the save and shows the error until
// it is fixed; switching profiles with an invalid name discards that edit.
//
// normalizeOnSave adjusts the edited profile as it is committed (e.g. filter
// profiles mirror the name into ranking.name); normalizeOnDuplicate cleans a
// fresh copy (e.g. stripping legacy fields). Both default to identity.
export function useProfileDrafts({
  profiles,
  onSave,
  newProfile,
  newProfileBaseName = "New Profile",
  normalizeOnSave = (profile) => profile,
  normalizeOnDuplicate = (profile) => profile,
}) {
  const [selected, setSelected] = useState(0)
  const [draft, setDraft] = useState(null)
  const [nameError, setNameError] = useState("")

  // pendingRef counts in-flight saves of our own, so the reseed effect can
  // tell our save echoing back through the config broadcast from an external
  // change worth adopting.
  const pendingRef = useRef(0)
  const timerRef = useRef(null)
  const adoptedRef = useRef(null)
  const draftRef = useRef(null)
  // forceReseedRef marks a list operation the user just performed — select,
  // add, duplicate, delete. After one of those the draft has to be re-adopted
  // from the saved list even though one of our own saves is still in flight:
  // that save belongs to the row we just left, and the guards below would
  // otherwise leave the editor holding it while the selection points somewhere
  // else. Armed to start, because the first render is itself a selection.
  const forceReseedRef = useRef(true)
  // prunedRef holds the list a delete just committed. A delete renumbers every
  // row after it, so until that save lands the saved list has a different
  // profile at `selected` than the user is now looking at.
  const prunedRef = useRef(null)
  useEffect(() => { draftRef.current = draft }, [draft])

  const commit = (next, nextIndex) => {
    if (timerRef.current) {
      window.clearTimeout(timerRef.current)
      timerRef.current = null
    }
    pendingRef.current += 1
    Promise.resolve(onSave(next)).finally(() => {
      // Let the post-save refetch settle before reseeding is allowed again.
      window.setTimeout(() => { pendingRef.current = Math.max(0, pendingRef.current - 1) }, 500)
    })
    if (typeof nextIndex === "number") setSelected(nextIndex)
  }

  const validateName = (candidate, index) => {
    const name = (candidate?.name || "").trim()
    if (!name) return "Name is required."
    const clash = profiles.some((p, i) => i !== index && p.name.trim().toLowerCase() === name.toLowerCase())
    if (clash) return "Another profile already uses this name."
    return ""
  }

  // listWithDraft folds the current (valid) edit into the saved list, so list
  // operations like add/duplicate/delete never silently drop a pending edit.
  const listWithDraft = () => {
    const current = draftRef.current
    if (!current || validateName(current, selected)) return [...profiles]
    return profiles.map((p, i) => (i === selected ? normalizeOnSave({ ...current, name: current.name.trim() }) : p))
  }

  // dirty compares what a save WOULD store against what is stored — the
  // normalized draft, not the raw one. Comparing the raw draft would stick
  // dirty (and the saving indicator) on forever after a rename, because
  // normalizeOnSave changes the committed profile (trimmed name, mirrored
  // ranking.name) and the echo then never matches the draft.
  const dirty = useMemo(() => {
    const current = profiles[selected]
    if (!current || !draft) return false
    const name = (draft.name || "").trim()
    if (!name) return true
    return JSON.stringify(current) !== JSON.stringify(normalizeOnSave({ ...draft, name }))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [profiles, selected, draft])

  // Auto-save: any valid change to the draft schedules a debounced whole-list
  // save. An invalid name parks the save and surfaces the error instead.
  useEffect(() => {
    if (!draft || !dirty) return undefined
    const err = validateName(draft, selected)
    setNameError(err)
    if (err) return undefined
    const handle = window.setTimeout(() => {
      timerRef.current = null
      const saved = normalizeOnSave({ ...draft, name: draft.name.trim() })
      // Pre-adopt what we are about to save, so the save's own echo through
      // the config broadcast is recognized and never clobbers newer typing.
      adoptedRef.current = JSON.stringify(saved)
      commit(profiles.map((p, i) => (i === selected ? saved : p)))
    }, 600)
    timerRef.current = handle
    return () => {
      window.clearTimeout(handle)
      if (timerRef.current === handle) timerRef.current = null
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [draft])

  // Adopt the saved profile when the selection changes, or when the profile
  // itself changed underneath us (another page, another browser).
  //
  // The in-flight guards exist to stop a save's own echo from clobbering newer
  // typing on the profile being edited. They must not apply to a selection
  // change: there is no newer typing for a row the user has only just moved
  // to, and skipping left the draft holding the previous profile while the
  // selection pointed at the next one — which the list then rendered under the
  // wrong row, since a selected row shows the draft's name.
  useEffect(() => {
    // Read a just-deleted-from list from the delete itself rather than from the
    // saved list, which still carries the removed profile — and so still has
    // the row the user was on sitting at `selected`. Once the prop catches up
    // the two agree and the stashed list is done.
    if (prunedRef.current && prunedRef.current.length === profiles.length) prunedRef.current = null
    const source = forceReseedRef.current && prunedRef.current ? prunedRef.current : profiles
    const current = source[selected]
    if (forceReseedRef.current) {
      // An add or duplicate selects an index the saved list does not contain
      // until its save lands. Stay armed until there is something to adopt.
      if (!current) return
      forceReseedRef.current = false
    } else {
      if (pendingRef.current > 0 || timerRef.current) return
      if ((current ? JSON.stringify(current) : null) === adoptedRef.current && draftRef.current) return
    }
    adoptedRef.current = current ? JSON.stringify(current) : null
    setDraft(current ? structuredClone(current) : null)
    setNameError("")
  }, [profiles, selected])

  const selectProfile = (index) => {
    if (index === selected) return
    // A pending valid edit is flushed into the same save that switches; an
    // invalid one is discarded — auto-save has nothing else to do with it.
    const base = listWithDraft()
    if (JSON.stringify(base) !== JSON.stringify(profiles)) {
      commit(base)
    }
    adoptedRef.current = null
    forceReseedRef.current = true
    setSelected(index)
  }

  const addProfile = () => {
    const base = listWithDraft()
    const name = uniqueProfileName(base, newProfileBaseName)
    adoptedRef.current = null
    forceReseedRef.current = true
    commit([...base, newProfile(name)], base.length)
  }

  const duplicateProfile = () => {
    const base = listWithDraft()
    const copy = normalizeOnDuplicate(structuredClone(base[selected]))
    copy.name = uniqueProfileName(base, `${copy.name} copy`)
    adoptedRef.current = null
    forceReseedRef.current = true
    commit([...base, normalizeOnSave(copy)], base.length)
  }

  const deleteProfile = (index) => {
    const next = listWithDraft().filter((_, i) => i !== index)
    adoptedRef.current = null
    forceReseedRef.current = true
    prunedRef.current = next
    commit(next, Math.max(0, Math.min(selected, next.length - 1)))
  }

  return { selected, draft, setDraft, nameError, dirty, selectProfile, addProfile, duplicateProfile, deleteProfile }
}
