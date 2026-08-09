import { useCallback, useEffect, useRef, useState } from 'react'

// Field-level auto-save shared by the General and Advanced settings sections.
// Each field persists on its own (switches/selects on change, inputs on blur):
// the value is compared against the last saved snapshot so blur without an
// edit is a no-op, and a failed save reverts the field to its saved value.
export function useFieldAutoSave({ form, savedValues, onPersist }) {
  const savedRef = useRef(savedValues)
  const [savingField, setSavingField] = useState('')

  useEffect(() => {
    savedRef.current = savedValues
  }, [savedValues])

  const hasFieldChanged = useCallback((name) => (
    JSON.stringify(form.getValues(name)) !== JSON.stringify(savedRef.current[name])
  ), [form])

  const revertField = useCallback((name) => {
    form.setValue(name, savedRef.current[name], { shouldDirty: true })
  }, [form])

  // saveFields commits several fields as one patch, for settings that are only
  // valid together (a database backend and its connection string). It saves
  // when any of them changed, and reverts all of them if the save fails.
  const saveFields = useCallback(async (names, cardId) => {
    if (!names.some(hasFieldChanged)) return
    setSavingField(names.find(hasFieldChanged))
    try {
      const payload = {}
      names.forEach((name) => { payload[name] = form.getValues(name) })
      await onPersist(payload, cardId)
    } catch {
      names.forEach((name) => form.setValue(name, savedRef.current[name], { shouldDirty: true }))
    } finally {
      setSavingField('')
    }
  }, [form, hasFieldChanged, onPersist])

  const saveField = useCallback((name, cardId) => saveFields([name], cardId), [saveFields])

  return { saveField, saveFields, savingField, hasFieldChanged, revertField }
}
