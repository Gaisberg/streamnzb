import React, { useId, useRef, useState } from "react"
import { Card, CardContent } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { UsageChips } from "@/components/UsageChips"
import { Copy, Import, Link2, Loader2, Plus, Share2, Trash2 } from "lucide-react"
import { useProfileDrafts } from "@/hooks/useProfileDrafts"
import { nameKey } from "@/lib/usage"
import { cn } from "@/lib/utils"

// ProfileManager is the shared master/detail shell for named-profile pages
// (filters, metadata, formatting): a selectable profile list with usage hints
// on the left, the page's own editor on the right under a sticky header bar
// that keeps the name, save state and errors visible however deep the editor
// scrolls. The draft/auto-save lifecycle lives in useProfileDrafts.
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
  const detailRef = useRef(null)
  const { selected, draft, setDraft, nameError, dirty, selectProfile, addProfile, duplicateProfile, deleteProfile } =
    useProfileDrafts({ profiles, onSave, newProfile, newProfileBaseName, normalizeOnSave, normalizeOnDuplicate })

  // Below lg the list stacks above the editor, so selecting a row changes
  // nothing on screen; bring the editor into view. Wide layouts show both
  // columns and must not jump.
  const revealEditor = () => {
    if (window.matchMedia?.("(min-width: 1024px)").matches) return
    window.requestAnimationFrame(() => detailRef.current?.scrollIntoView({ behavior: "smooth", block: "start" }))
  }

  // The specific complaint beats the "Validation failed" headline: a refused
  // save carries per-field errors, and the first one names the rule at fault.
  const saveError = saveStatus?.type === "error"
    ? String((saveStatus.errors && Object.values(saveStatus.errors)[0]) || saveStatus.msg || "Save failed")
    : ""

  if (profiles.length === 0) {
    return (
      <>
        <Card className="border border-dashed border-border bg-card">
          <CardContent className="py-10 text-center">
            <p className="text-sm text-muted-foreground">{emptyText}</p>
            <div className="mt-3 flex flex-wrap items-center justify-center gap-2">
              <Button onClick={addProfile} size="sm">
                <Plus className="mr-2 h-4 w-4" /> Create one
              </Button>
              {sharing && (
                <Button variant="outline" size="sm" onClick={sharing.openImport}>
                  <Import className="mr-2 h-4 w-4" /> Import
                </Button>
              )}
            </div>
          </CardContent>
        </Card>
        {sharing?.dialogs}
      </>
    )
  }

  return (
    <div className="grid gap-6 lg:grid-cols-[280px_minmax(0,1fr)]">
      <div className="space-y-2">
        <div className="flex gap-2">
          <Button onClick={addProfile} size="sm" className="flex-1">
            <Plus className="mr-2 h-4 w-4" /> {addButtonLabel}
          </Button>
          {sharing && (
            <Button variant="outline" size="sm" onClick={sharing.openImport}>
              <Import className="mr-2 h-4 w-4" /> Import
            </Button>
          )}
        </div>
        {profiles.map((profile, index) => {
          const used = usage[nameKey(profile.name)]
          return (
            <button
              key={index}
              type="button"
              onClick={() => { selectProfile(index); revealEditor() }}
              className={cn(
                "w-full rounded-md border px-3 py-2.5 text-left transition-colors",
                index === selected
                  ? "border-primary/50 bg-primary/5"
                  : "border-border hover:border-muted-foreground/40"
              )}
            >
              <div className="flex items-center gap-1.5">
                <span className="truncate text-sm font-medium">
                  {index === selected && draft ? draft.name || profile.name : profile.name}
                </span>
                {profile.source?.url && (
                  // The badge marks a profile subscribed to a remote source; the
                  // linked card in the editor carries the details.
                  <span
                    title={`Linked to ${profile.source.url}`}
                    className="inline-flex shrink-0 items-center gap-1 rounded-full border border-border px-1.5 py-px text-[10px] text-muted-foreground"
                  >
                    <Link2 className="h-2.5 w-2.5" /> Linked
                  </span>
                )}
              </div>
              {summarize && (
                <div className="mt-0.5 truncate text-xs text-muted-foreground">{summarize(profile)}</div>
              )}
              {used?.length ? (
                // The chips are the streams bound to this profile — names, not
                // keywords, which "In use · Kids, default" used to blur.
                <UsageChips labels={used} className="mt-1.5" />
              ) : (
                <div className="mt-1 truncate text-[11px] text-muted-foreground/70">Not in use</div>
              )}
            </button>
          )
        })}
      </div>

      {draft && (
        <div ref={detailRef} className="min-w-0 scroll-mt-4 space-y-4">
          <div className="sticky top-0 z-10 rounded-lg border border-border bg-card/95 p-3 shadow-sm backdrop-blur-sm">
            <div className="flex flex-wrap items-center gap-2">
              <Input
                id={nameId}
                aria-label={`${entityLabel} name`}
                value={draft.name || ""}
                onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                className="h-8 w-full min-w-40 flex-1 sm:w-auto sm:max-w-xs"
              />
              <div className="ml-auto flex flex-wrap items-center justify-end gap-1">
                <span className="mr-1 text-xs text-muted-foreground">
                  {isSaving ? (
                    <span className="flex items-center gap-1.5"><Loader2 className="h-3.5 w-3.5 animate-spin" /> Saving…</span>
                  ) : dirty ? (
                    // Dirty means the debounce is still counting down — no
                    // request exists yet, so no spinner pretends one does.
                    "Unsaved changes"
                  ) : saveStatus?.msg ? (
                    saveStatus.msg
                  ) : (
                    // The idle state carries the reassurance the old header
                    // card spent a whole subtitle on.
                    <span className="text-muted-foreground/60">Saves automatically</span>
                  )}
                </span>
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
              </div>
            </div>
            {(nameError || saveError) && (
              // The full complaint, wrapping — a refused save leaves the draft
              // dirty forever, so an error nobody can read is an error nobody
              // can fix.
              <p className="mt-1.5 whitespace-pre-wrap text-xs text-destructive">{nameError || saveError}</p>
            )}
          </div>

          {children}

          {renderEditor(draft, setDraft)}
        </div>
      )}

      <ConfirmDialog
        open={confirmDelete !== null}
        onOpenChange={(open) => { if (!open) setConfirmDelete(null) }}
        title={`Delete ${entityLabel}`}
        description={confirmDelete !== null && describeDelete ? describeDelete(profiles[confirmDelete], usage) : ""}
        confirmLabel="Delete"
        onConfirm={() => { deleteProfile(confirmDelete); setConfirmDelete(null) }}
      />

      {sharing?.dialogs}
    </div>
  )
}
