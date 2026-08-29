import { useEffect, useState } from "react"

// The shared state chrome of the settings add/edit dialogs (indexers,
// providers, search queries): a draft reseeded when the dialog opens, a dirty
// check against the normalized initial value, the save flow with an error
// banner and per-field errors, and a discard confirmation on a dirty close.
// EntityDialog renders the matching markup; each dialog keeps what is
// actually its own — the fields, the validation rules, and how backend field
// errors map onto inputs.
//
// StreamDialog deliberately does not use this: its saving state lives in the
// parent, its save is fire-and-forget, and its reset is keyed on which stream
// it shows. Converge it when it is next touched (standing rule 9).

// The specific complaint beats the generic headline: a refused save carries
// per-field errors, and the first one names the field at fault.
export function firstFieldErrorMessage(fieldErrors, fallback) {
  const first = Object.values(fieldErrors || {}).find(Boolean)
  return first || fallback
}

// One set of layout classes for label-left/control-right dialog field rows.
export const dialogRowClass = "flex flex-col gap-3 min-[360px]:flex-row min-[360px]:items-center min-[360px]:gap-4"
export const dialogLabelClass = "min-w-0 min-[360px]:flex-1"
export const dialogControlBaseClass = "w-full min-[360px]:ml-auto min-[360px]:shrink-0"
export const dialogControlWideClass = `${dialogControlBaseClass} min-[360px]:w-[14rem]`
export const dialogControlNameClass = `${dialogControlBaseClass} flex items-center gap-2 min-[360px]:w-[16.5rem]`
export const dialogControlNarrowClass = `${dialogControlBaseClass} min-[360px]:w-[9rem]`
// A switch has no width of its own to override the stacked-layout w-full with,
// and the base class pins it against shrinking — so without w-auto it claims
// the whole row, squeezes the label box to nothing and sits on top of the
// label text instead of lining up with the inputs above it.
export const dialogControlSwitchClass = `${dialogControlBaseClass} flex min-h-9 items-center min-[360px]:w-auto`

// useEntityDialog owns the dialog's state chrome. `makeDraft` seeds the draft
// on mount and again on every open; `normalize` is the comparable form used
// for the dirty check. `onClose` runs on a plain (non-discard) close, for
// per-dialog cleanup beyond what the reopen reset covers.
export function useEntityDialog({ open, onOpenChange, initialValue, makeDraft, normalize, onClearStatus, onClose }) {
  const [draft, setDraft] = useState(makeDraft)
  const [wasOpen, setWasOpen] = useState(open)
  const [saveError, setSaveError] = useState("")
  const [fieldErrors, setFieldErrors] = useState({})
  const [saving, setSaving] = useState(false)
  const [showDiscardConfirm, setShowDiscardConfirm] = useState(false)

  useEffect(() => {
    if (open && !wasOpen) {
      setDraft(makeDraft())
      setSaveError("")
      setFieldErrors({})
    }
    setWasOpen(open)
    // makeDraft is a fresh closure every render; only the open transition
    // matters, and it reads the latest one.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, wasOpen])

  const isDirty = JSON.stringify(normalize(initialValue)) !== JSON.stringify(normalize(draft))

  const requestClose = () => {
    if (saving) return
    if (isDirty) {
      setShowDiscardConfirm(true)
      return
    }
    onClearStatus?.()
    onClose?.()
    onOpenChange(false)
  }

  const confirmDiscard = () => {
    setShowDiscardConfirm(false)
    onClearStatus?.()
    onOpenChange(false)
  }

  const update = (key, value) => setDraft((current) => ({ ...current, [key]: value }))
  const fieldClass = (key) => (fieldErrors[key] ? "border-destructive focus-visible:ring-destructive" : "")

  // runSave is the one save flow: local validation gates the request, the
  // banner names the first complaint, a refused save maps its field errors
  // through the dialog's own mapError, and anything unmapped falls back to
  // the first raw field error before the generic message.
  const runSave = async ({ validate, commit, mapError }) => {
    const nextFieldErrors = validate?.() || {}
    if (Object.keys(nextFieldErrors).length > 0) {
      setFieldErrors(nextFieldErrors)
      setSaveError(firstFieldErrorMessage(nextFieldErrors, "Please review the highlighted fields."))
      return
    }
    setSaving(true)
    setSaveError("")
    setFieldErrors({})
    try {
      await commit()
      onOpenChange(false)
    } catch (error) {
      const mapped = mapError?.(error) || {}
      setFieldErrors(mapped)
      setSaveError(firstFieldErrorMessage(mapped, firstFieldErrorMessage(error?.fieldErrors, error?.message || "Save failed")))
    } finally {
      setSaving(false)
    }
  }

  return {
    draft, setDraft, update,
    fieldErrors, setFieldErrors, fieldClass,
    saveError, setSaveError,
    saving, isDirty,
    requestClose, confirmDiscard,
    showDiscardConfirm, setShowDiscardConfirm,
    runSave,
  }
}
