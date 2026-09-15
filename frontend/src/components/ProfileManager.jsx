import React, { useId, useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { ManagerShell } from "@/components/ManagerShell"
import { Copy, Import, Link2, Loader2, Plus, Share2, Trash2 } from "lucide-react"
import { useProfileDrafts } from "@/hooks/useProfileDrafts"
import { nameKey } from "@/lib/usage"

// ProfileManager is the named-profile half of the master/detail pages
// (filters, metadata, formatting): ManagerShell provides the layout, and this
// supplies what a profile means — an inline name that auto-saves, the
// duplicate/delete/export actions, and the draft lifecycle in useProfileDrafts.
export function ProfileManager({
  profiles,
  onSave,
  usage = {},
  summarize,
  newProfile,
  describeDelete,
  renderEditor,
  entityLabel = "profile",
  addButtonLabel = "New profile",
  newProfileBaseName = "New Profile",
  emptyText = "No profiles yet.",
  isSaving,
  saveStatus,
  normalizeOnSave = (profile) => profile,
  normalizeOnDuplicate = (profile) => profile,
  // sharing is the useProfileSharing result. When given, the manager owns the
  // whole surface: Import beside the add button, Export in the action row,
  // and the dialogs — the pages wire nothing.
  sharing,
  children,
}) {
  const nameId = useId()
  const [confirmDelete, setConfirmDelete] = useState(null)
  const { selected, draft, setDraft, nameError, dirty, selectProfile, addProfile, duplicateProfile, deleteProfile } =
    useProfileDrafts({ profiles, onSave, newProfile, newProfileBaseName, normalizeOnSave, normalizeOnDuplicate })

  // The specific complaint beats the "Validation failed" headline: a refused
  // save carries per-field errors, and the first one names the rule at fault.
  const saveError = saveStatus?.type === "error"
    ? String((saveStatus.errors && Object.values(saveStatus.errors)[0]) || saveStatus.msg || "Save failed")
    : ""

  const importButton = sharing && (
    <Button variant="outline" size="sm" onClick={sharing.openImport}>
      <Import className="mr-2 h-4 w-4" /> Import
    </Button>
  )

  const items = profiles.map((profile, index) => ({
    key: index,
    name: index === selected && draft ? draft.name || profile.name : profile.name,
    badge: profile.source?.url ? (
      // The badge marks a profile subscribed to a remote source; the linked
      // card in the editor carries the details.
      <span
        title={`Linked to ${profile.source.url}`}
        className="inline-flex shrink-0 items-center gap-1 rounded-full border border-border px-1.5 py-px text-[10px] text-muted-foreground"
      >
        <Link2 className="h-2.5 w-2.5" /> Linked
      </span>
    ) : null,
    summary: summarize ? summarize(profile) : "",
    usageLabels: usage[nameKey(profile.name)],
    emptyUsageText: "Not in use",
  }))

  return (
    <ManagerShell
      items={items}
      selectedIndex={selected}
      onSelect={selectProfile}
      emptyText={emptyText}
      emptyActions={
        <>
          <Button onClick={addProfile} size="sm">
            <Plus className="mr-2 h-4 w-4" /> Create one
          </Button>
          {importButton}
        </>
      }
      listActions={
        <>
          <Button onClick={addProfile} size="sm" className="flex-1">
            <Plus className="mr-2 h-4 w-4" /> {addButtonLabel}
          </Button>
          {importButton}
        </>
      }
      title={draft && (
        <Input
          id={nameId}
          aria-label={`${entityLabel} name`}
          value={draft.name || ""}
          onChange={(e) => setDraft({ ...draft, name: e.target.value })}
          className="h-8 w-full min-w-40 flex-1 sm:w-auto sm:max-w-xs"
        />
      )}
      status={
        isSaving ? (
          <span className="flex items-center gap-1.5"><Loader2 className="h-3.5 w-3.5 animate-spin" /> Saving…</span>
        ) : dirty ? (
          // Dirty means the debounce is still counting down — no request
          // exists yet, so no spinner pretends one does.
          "Unsaved changes"
        ) : saveStatus?.msg ? (
          saveStatus.msg
        ) : (
          // The idle state carries the reassurance the old header card spent a
          // whole subtitle on.
          <span className="text-muted-foreground/60">Saves automatically</span>
        )
      }
      actions={
        <>
          {sharing && (
            <Button variant="ghost" size="sm" className="h-8" onClick={() => sharing.exportProfile(draft)}>
              <Share2 className="mr-2 h-3.5 w-3.5" /> Export
            </Button>
          )}
          <Button variant="ghost" size="sm" className="h-8" onClick={duplicateProfile}>
            <Copy className="mr-2 h-3.5 w-3.5" /> Duplicate
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="h-8 text-destructive hover:text-destructive"
            onClick={() => setConfirmDelete(selected)}
          >
            <Trash2 className="mr-2 h-3.5 w-3.5" /> Delete
          </Button>
        </>
      }
      error={nameError || saveError}
      footer={
        <>
          <ConfirmDialog
            open={confirmDelete !== null}
            onOpenChange={(open) => { if (!open) setConfirmDelete(null) }}
            title={`Delete ${entityLabel}`}
            description={confirmDelete !== null && describeDelete ? describeDelete(profiles[confirmDelete], usage) : ""}
            confirmLabel="Delete"
            onConfirm={() => { deleteProfile(confirmDelete); setConfirmDelete(null) }}
          />
          {sharing?.dialogs}
        </>
      }
    >
      {draft && (
        <>
          {children}
          {renderEditor(draft, setDraft)}
        </>
      )}
    </ManagerShell>
  )
}
