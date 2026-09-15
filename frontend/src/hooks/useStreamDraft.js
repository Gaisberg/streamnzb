import { useCallback, useEffect, useRef, useState } from "react"

// useStreamDraft owns the draft lifecycle for the selected stream: a draft of
// its settings and a debounced save of that one stream.
//
// It is deliberately not useProfileDrafts. Profiles are an array in the config
// written as a whole and matched by index, so a rename is just another field
// in the next debounced save. A stream is its own resource, saved under its
// username — and that username is the credential clients authenticate with.
// Renaming it is therefore an explicit action with its own endpoint, never
// something a debounce timer does on the way past.
//
// onSave(username, draft) performs the write and may reject with an error
// carrying fieldErrors, which the editor uses to mark the offending tab.
export function useStreamDraft({ stream, buildDraft, onSave, debounceMs = 700 }) {
  const name = stream?.username || ""
  const [draft, setDraftState] = useState(null)
  const [saving, setSaving] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [error, setError] = useState("")
  const [fieldErrors, setFieldErrors] = useState({})

  const timerRef = useRef(null)
  // pendingRef holds the edit the timer is counting down for, so switching
  // streams can write it out rather than drop it on the floor.
  const pendingRef = useRef(null)
  const inFlightRef = useRef(0)
  const draftRef = useRef(null)
  const streamRef = useRef(stream)
  const buildRef = useRef(buildDraft)
  const saveRef = useRef(onSave)
  useEffect(() => { streamRef.current = stream })
  useEffect(() => { buildRef.current = buildDraft })
  useEffect(() => { saveRef.current = onSave })

  const cancelTimer = () => {
    if (timerRef.current) {
      window.clearTimeout(timerRef.current)
      timerRef.current = null
    }
  }

  const save = useCallback((username, next) => {
    if (!username || !next) return
    inFlightRef.current += 1
    setSaving(true)
    setDirty(false)
    Promise.resolve(saveRef.current(username, next))
      .then(() => { setError(""); setFieldErrors({}) })
      .catch((err) => {
        setError(err?.message || "Failed to save stream")
        setFieldErrors(err?.fieldErrors || {})
      })
      .finally(() => {
        inFlightRef.current -= 1
        if (inFlightRef.current === 0) setSaving(false)
      })
  }, [])

  // flushPending writes whatever the timer was waiting on, immediately.
  const flushPending = useCallback(() => {
    cancelTimer()
    const pending = pendingRef.current
    pendingRef.current = null
    if (pending) save(pending.username, pending.draft)
  }, [save])

  const setDraft = useCallback((updater) => {
    const next = typeof updater === "function" ? updater(draftRef.current) : updater
    draftRef.current = next
    setDraftState(next)
    pendingRef.current = { username: streamRef.current?.username || "", draft: next }
    setDirty(true)
    setError("")
    cancelTimer()
    timerRef.current = window.setTimeout(() => {
      timerRef.current = null
      const pending = pendingRef.current
      pendingRef.current = null
      if (pending) save(pending.username, pending.draft)
    }, debounceMs)
  }, [debounceMs, save])

  const seed = useCallback(() => {
    const next = streamRef.current ? buildRef.current(streamRef.current) : null
    draftRef.current = next
    setDraftState(next)
    setDirty(false)
    setError("")
    setFieldErrors({})
  }, [])

  // Switching streams writes out the edit in flight first: the timer belongs
  // to the stream being left, and firing it later would save one stream's
  // settings while the editor shows another's.
  useEffect(() => {
    flushPending()
    seed()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name])

  // Adopt an external change — another tab, a config broadcast — but never
  // over an edit the user has in hand.
  const signature = JSON.stringify(stream ?? null)
  useEffect(() => {
    if (dirty || pendingRef.current || inFlightRef.current > 0) return
    seed()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [signature])

  // A pending edit must survive the page unmounting — closing the tab is not
  // a decision to discard what was typed.
  useEffect(() => () => flushPending(), [flushPending])

  return { draft, setDraft, dirty, saving, error, fieldErrors, flushPending }
}
