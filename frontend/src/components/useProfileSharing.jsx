import React, { useState } from "react"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Copy, Download, Import } from "lucide-react"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

// One box style for every share-code textarea.
const shareBoxClass = "w-full resize-y rounded-md border border-input bg-background p-2.5 font-mono text-[11px] leading-relaxed focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"

// downloadText hands the user a file, for hosting a share code at a raw URL
// without a copy-paste step in the middle.
function downloadText(filename, text) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }))
  const anchor = document.createElement("a")
  anchor.href = url
  anchor.download = filename
  anchor.click()
  URL.revokeObjectURL(url)
}

// useProfileSharing owns import and export for one profile kind: the share
// code dialogs, the from-URL import that links the new profile to its source,
// and the file download for hosting a code. The pages differ only in their
// codec — how a profile becomes a code and back — so the whole flow lives
// here once.
//
// codec: {
//   encode(profile) -> code, decode(code) -> profile,
//   fetchRemote(url) -> { url, code, profile }, placeholder: "SNZBP1:…",
// }
// noun names the kind in the dialogs ("profile", "define library");
// importNote overrides the import dialog's description where the default's
// share-code framing does not fit the kind.
// Returns { openImport, exportProfile, dialogs } — render `dialogs` once in
// the page, hand `openImport` to a header button and `exportProfile` to the
// per-profile action row.
export function useProfileSharing({ profiles, onSave, isSaving, codec, noun = "profile", importNote }) {
  const [exportCode, setExportCode] = useState(null)
  const [exportName, setExportName] = useState("")
  const [exportCopied, setExportCopied] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [importCode, setImportCode] = useState("")
  const [importUrl, setImportUrl] = useState("")
  const [importBusy, setImportBusy] = useState(false)
  const [importError, setImportError] = useState("")

  const exportProfile = async (draft) => {
    if (!draft) return
    try {
      setExportCopied(false)
      setExportName(draft.name || "")
      setExportCode(await codec.encode(draft))
    } catch {
      setExportCode("")
    }
  }

  const openImport = () => {
    setImportCode("")
    setImportUrl("")
    setImportError("")
    setImportOpen(true)
  }

  const copyText = async (text) => {
    if (!text) return
    try {
      await navigator.clipboard.writeText(text)
      setExportCopied(true)
    } catch {
      // Clipboard needs a secure context; the textarea is still selectable.
    }
  }

  // addImported gives the profile a free name and saves it. Import never
  // overwrites: a name collision gets a numeric suffix. The save is awaited so
  // a rejection keeps the dialog open with the reason, instead of closing over
  // a profile that never landed.
  const addImported = async (profile) => {
    const taken = new Set(profiles.map((p) => p.name.trim().toLowerCase()))
    let name = (profile.name || "").trim() || `Imported ${noun.replace(/\b\w/g, (c) => c.toUpperCase())}`
    if (taken.has(name.toLowerCase())) {
      let n = 2
      while (taken.has(`${name} ${n}`.toLowerCase())) n += 1
      name = `${name} ${n}`
    }
    profile.name = name
    await onSave([...profiles, profile])
    setImportOpen(false)
    setImportCode("")
    setImportUrl("")
    setImportError("")
  }

  const importProfile = async () => {
    setImportBusy(true)
    try {
      if (importUrl.trim()) {
        // A URL import links the profile to its source: the code it applied is
        // kept verbatim, so Refresh can tell an update from a re-encode and
        // knows what came from upstream.
        const { url, code, profile } = await codec.fetchRemote(importUrl)
        const stamp = new Date().toISOString()
        profile.source = { url, code, checked_at: stamp, applied_at: stamp }
        await addImported(profile)
      } else {
        await addImported(await codec.decode(importCode))
      }
    } catch (err) {
      // A save the server refused carries the specific complaint per field;
      // that beats the generic "Validation failed" headline.
      const fieldError = err?.fieldErrors && Object.values(err.fieldErrors)[0]
      setImportError(fieldError || err?.message || `Could not read that ${noun}.`)
    } finally {
      setImportBusy(false)
    }
  }

  const dialogs = (
    <>
      <Dialog open={exportCode !== null} onOpenChange={(open) => { if (!open) setExportCode(null) }}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>Export “{exportName}”</DialogTitle>
            <DialogDescription>
              The whole {noun} as one string. It pastes into a chat window intact, and whoever receives it
              imports it from this page. Download writes it to a file; host that file at a raw https URL (a
              GitHub repo or gist works) and others can import from the URL and refresh later.
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-1.5">
            <div className="flex items-center justify-between gap-2">
              <Label className="text-sm">Share code</Label>
              <div className="flex items-center gap-1">
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-7 px-2 text-xs"
                  disabled={!exportCode}
                  onClick={() => downloadText(`${(exportName || "profile").trim().replace(/[^\w.-]+/g, "-")}.txt`, exportCode + "\n")}
                >
                  <Download className="mr-1.5 h-3 w-3" /> Download
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-7 px-2 text-xs"
                  disabled={!exportCode}
                  onClick={() => copyText(exportCode)}
                >
                  <Copy className="mr-1.5 h-3 w-3" /> Copy
                </Button>
              </div>
            </div>
            <textarea
              readOnly
              value={exportCode || "This browser cannot generate share codes."}
              onFocus={(e) => e.target.select()}
              rows={4}
              className={shareBoxClass}
            />
          </div>

          <DialogFooter className="flex-row items-center justify-between gap-2 sm:justify-between sm:space-x-0">
            <span className="text-xs text-muted-foreground">{exportCopied ? "Copied to clipboard." : ""}</span>
            <Button type="button" variant="outline" size="sm" onClick={() => setExportCode(null)}>Done</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={importOpen} onOpenChange={setImportOpen}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>Import {noun}</DialogTitle>
            <DialogDescription>
              {importNote || `Paste a share code, or the https URL of a file that serves one. Either way it is added as a new
              ${noun} and never overwrites an existing one. A ${noun} imported from a URL stays linked to it:
              a Refresh button fetches updates, which apply only after you review them.`}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-1.5">
              <Label className="text-sm">Share code</Label>
              <textarea
                value={importCode}
                onChange={(e) => { setImportCode(e.target.value); setImportError("") }}
                rows={5}
                placeholder={codec.placeholder}
                disabled={!!importUrl.trim()}
                className={`${shareBoxClass} disabled:opacity-50`}
              />
            </div>
            <div className="space-y-1.5">
              <Label className="text-sm">Or import from URL</Label>
              <Input
                value={importUrl}
                onChange={(e) => { setImportUrl(e.target.value); setImportError("") }}
                placeholder="https://raw.githubusercontent.com/…/profile.txt"
                className="font-mono text-xs"
              />
            </div>
          </div>
          {importError && <p className="text-xs text-destructive">{importError}</p>}
          <DialogFooter className="flex-row items-center justify-end gap-2 sm:space-x-0">
            <Button type="button" variant="outline" size="sm" onClick={() => setImportOpen(false)}>
              Cancel
            </Button>
            <Button
              type="button"
              size="sm"
              disabled={(!importCode.trim() && !importUrl.trim()) || importBusy || isSaving}
              onClick={importProfile}
            >
              <Import className="mr-2 h-3.5 w-3.5" /> {importUrl.trim() ? "Import from URL" : "Import"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )

  return { openImport, exportProfile, dialogs }
}
