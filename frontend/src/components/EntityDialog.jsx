import React from "react"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, focusDialogCloseButton } from "@/components/ui/dialog"
import { ConfirmDialog } from "@/components/ConfirmDialog"

// EntityDialog renders the chrome around a useEntityDialog result: header,
// scrollable body (the caller's fields as children), footer with the error
// banner and Cancel/Save, and the discard confirmation. `bannerError`
// overrides the banner when the caller tracks errors beyond the hook's.
export function EntityDialog({
  dialog,
  open,
  onOpenChange,
  title,
  description,
  saveLabel = "Save",
  savingLabel,
  onSave,
  discardDescription = "Your unsaved changes will be lost.",
  bannerError,
  children,
}) {
  const banner = bannerError ?? dialog.saveError
  return (
    <Dialog open={open} onOpenChange={(nextOpen) => {
      if (nextOpen) {
        onOpenChange(true)
        return
      }
      dialog.requestClose()
    }}>
      <DialogContent className="flex max-h-[85vh] max-w-3xl flex-col overflow-hidden" onOpenAutoFocus={focusDialogCloseButton}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>

        <div className="min-h-0 flex-1 overflow-y-auto pr-1">
          {children}
        </div>

        <DialogFooter className="flex items-center justify-between gap-3">
          <div className="min-h-9 flex-1">
            {banner && (
              <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
                {banner}
              </div>
            )}
          </div>
          <div className="flex flex-row items-center justify-end gap-2">
            <Button type="button" variant="outline" onClick={dialog.requestClose} disabled={dialog.saving}>Cancel</Button>
            <Button type="button" variant="destructive" onClick={() => void onSave()} disabled={dialog.saving}>
              {dialog.saving && savingLabel ? savingLabel : saveLabel}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
      <ConfirmDialog
        open={dialog.showDiscardConfirm}
        onOpenChange={dialog.setShowDiscardConfirm}
        title="Discard changes?"
        description={discardDescription}
        confirmLabel="Discard"
        onConfirm={dialog.confirmDiscard}
      />
    </Dialog>
  )
}
