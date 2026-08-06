import { useCallback, useEffect, useRef, useState } from 'react'

// Field-level auto-save shared by the Network and Advanced settings sections.
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

  const saveField = useCallback(async (name, cardId) => {
    if (!hasFieldChanged(name)) return
    setSavingField(name)
    try {
      await onPersist({ [name]: form.getValues(name) }, cardId)
    } catch {
      form.setValue(name, savedRef.current[name], { shouldDirty: true })
    } finally {
      setSavingField('')
    }
  }, [form, hasFieldChanged, onPersist])

  return { saveField, savingField, hasFieldChanged, revertField }
}
