import React, { useEffect, useRef, useState } from "react"
import { TriangleAlert } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { defineRulesFromText } from "@/lib/defineLibraries"
import { rulesToText } from "@/lib/profiles"

// DefineLibraryEditor edits a library's defines as text — the same one rule
// per line the rules editor's Text mode uses, minus every action but define.
// A library is a flat list of named conditions, usually generated or pasted
// in bulk, which is exactly the shape the text form is for; the card editor's
// actions, points and previews have nothing to do here.
export function DefineLibraryEditor({ library, onChange }) {
  const [text, setText] = useState(() => rulesToText(library.rules || []))
  const [error, setError] = useState("")

  // committedRef is the canonical text of the rules this editor last
  // produced. When the library's rules stop matching it, the change came from
  // outside — another library selected, a refresh applied — and the textarea
  // reseeds; the user's own edits echoing back through auto-save do not.
  const committedRef = useRef(text)
  useEffect(() => {
    const canonical = rulesToText(library.rules || [])
    if (canonical !== committedRef.current) {
      committedRef.current = canonical
      setText(canonical)
      setError("")
    }
  }, [library.rules])

  const edit = (next) => {
    setText(next)
    try {
      const rules = defineRulesFromText(next)
      committedRef.current = rulesToText(rules)
      onChange({ ...library, rules })
      setError("")
    } catch (err) {
      setError(err.message)
    }
  }

  const count = (library.rules || []).length
  return (
    <Card className="border border-border bg-card">
      <CardContent className="space-y-2 py-4">
        <p className="text-sm font-medium">Defines</p>
        <p className="text-xs text-muted-foreground">
          One define per line, in the rules editor&apos;s text form —{" "}
          <code className="font-mono">Name [scope]: define if &lt;condition&gt;</code>. Lines starting
          with <code className="font-mono">#</code> are ignored. Every filter profile can reference these
          with <code className="font-mono">matched(&quot;Name&quot;)</code>; a profile rule under the same
          name shadows the library&apos;s.
        </p>
        <textarea
          value={text}
          onChange={(e) => edit(e.target.value)}
          rows={Math.min(24, Math.max(8, text.split("\n").length + 1))}
          spellCheck={false}
          placeholder={'Movies Remux T1 Groups: define if group in ["FraMeSToR", "W4NK3R"]\nMovies WEB T1 Groups [movie]: define if releaseName matches "(?i)-(NTb|FLUX)$"'}
          className="w-full resize-y rounded-md border border-input bg-background p-2.5 font-mono text-xs leading-relaxed focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
          aria-label="Library defines as text"
        />
        {error ? (
          <div className="flex items-start gap-2 rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2">
            <TriangleAlert className="mt-0.5 h-3.5 w-3.5 shrink-0 text-destructive" />
            <p className="text-xs text-destructive">{error} Nothing is saved until that line parses.</p>
          </div>
        ) : (
          <p className="text-[11px] text-muted-foreground">
            {count === 0
              ? "No defines yet — an empty library does nothing."
              : `${count} define${count === 1 ? "" : "s"}, saved as you type.`}
          </p>
        )}
      </CardContent>
    </Card>
  )
}
