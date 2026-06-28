import { useCallback, useMemo, useRef, useState, memo } from "react"
import { Upload, Link2, PlayCircle, Loader2, X } from "lucide-react"
import { Plyr } from "plyr-react"
import "plyr-react/plyr.css"
import "./direct-play-player.css"

import { apiFetch } from "@/api"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

export const DirectPlayPage = memo(function DirectPlayPage() {
  const fileInputRef = useRef(null)
  const [isDragging, setIsDragging] = useState(false)
  const [url, setUrl] = useState("")
  const [file, setFile] = useState(null)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState("")
  const [playback, setPlayback] = useState(null)
  const [playerOpen, setPlayerOpen] = useState(false)

  const selectedLabel = useMemo(() => {
    if (!file) return "No file selected"
    return `${file.name} (${Math.max(1, Math.round(file.size / 1024))} KB)`
  }, [file])

  const pickFile = useCallback(() => {
    if (fileInputRef.current) {
      fileInputRef.current.click()
    }
  }, [])

  const onInputFile = useCallback((event) => {
    const picked = event.target.files?.[0] || null
    setFile(picked)
    setError("")
  }, [])

  const onDrop = useCallback((event) => {
    event.preventDefault()
    setIsDragging(false)
    const dropped = event.dataTransfer?.files?.[0] || null
    if (dropped) {
      setFile(dropped)
      setError("")
    }
  }, [])

  const onSubmit = useCallback(async (event) => {
    event.preventDefault()
    setSubmitting(true)
    setError("")
    try {
      const formData = new FormData()
      if (url.trim()) {
        formData.append("url", url.trim())
      }
      if (file) {
        formData.append("file", file)
      }
      const data = await apiFetch("/api/play/nzb", {
        method: "POST",
        body: formData,
      })
      const playUrl = data?.play_path || data?.play_url
      if (!playUrl) {
        throw new Error("Missing play URL in response")
      }
      const absolutePlayUrl = new URL(playUrl, window.location.origin).toString()
      const externalPlayUrl = data.external_play_url || data.play_url || absolutePlayUrl
      setPlayback({
        sessionId: data.session_id,
        playUrl: absolutePlayUrl,
        externalPlayUrl,
      })
      setPlayerOpen(true)
    } catch (submitErr) {
      setPlayback(null)
      setError(submitErr.message || "Failed to start playback.")
    } finally {
      setSubmitting(false)
    }
  }, [file, url])

  const closePlayer = useCallback(() => {
    setPlayerOpen(false)
    setPlayback(null)
  }, [])

  const playerSource = useMemo(() => {
    if (!playback?.playUrl) {
      return null
    }
    return {
      type: "video",
      sources: [{ src: playback.playUrl }],
    }
  }, [playback?.playUrl])

  const playerOptions = useMemo(() => ({
    autoplay: true,
    controls: [
      "play-large",
      "play",
      "progress",
      "current-time",
      "mute",
      "volume",
      "settings",
      "pip",
      "airplay",
    ],
    fullscreen: { enabled: false },
  }), [])

  const canSubmit = Boolean(file || url.trim()) && !submitting

  return (
    <div className="pt-4 md:pt-5 pb-3 px-4 lg:px-5 space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>Direct NZB Play</CardTitle>
          <CardDescription>Drop an `.nzb` file or paste an NZB URL to start playback in the browser.</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={onSubmit} className="space-y-4">
            <div
              className={`rounded-lg border-2 border-dashed p-6 text-center transition-colors ${
                isDragging ? "border-primary bg-primary/5" : "border-border"
              }`}
              onDragOver={(event) => {
                event.preventDefault()
                setIsDragging(true)
              }}
              onDragLeave={() => setIsDragging(false)}
              onDrop={onDrop}
            >
              <Upload className="mx-auto mb-2 h-5 w-5 text-muted-foreground" />
              <p className="text-sm mb-3">Drag and drop an NZB file here</p>
              <Button type="button" variant="outline" onClick={pickFile}>Choose file</Button>
              <input
                ref={fileInputRef}
                type="file"
                accept=".nzb,application/xml,text/xml"
                className="hidden"
                onChange={onInputFile}
              />
              <p className="mt-3 text-xs text-muted-foreground">{selectedLabel}</p>
            </div>

            <div className="space-y-2">
              <Label htmlFor="direct-play-url">Or paste NZB URL</Label>
              <div className="flex gap-2">
                <div className="relative flex-1">
                  <Link2 className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
                  <Input
                    id="direct-play-url"
                    placeholder="https://indexer.example/download?id=..."
                    value={url}
                    onChange={(event) => setUrl(event.target.value)}
                    className="pl-9"
                  />
                </div>
              </div>
            </div>

            {error && (
              <div className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
                {error}
              </div>
            )}

            <Button type="submit" disabled={!canSubmit}>
              {submitting ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <PlayCircle className="mr-2 h-4 w-4" />}
              Start playback
            </Button>
          </form>
        </CardContent>
      </Card>

      {playback?.playUrl && (
        <Card>
          <CardHeader>
            <CardTitle>Playback started</CardTitle>
            <CardDescription>Session: {playback.sessionId}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            <div>
              <Button type="button" variant="secondary" onClick={() => window.open(playback.externalPlayUrl, "_blank", "noopener,noreferrer")}>
                Open stream in new tab
              </Button>
            </div>
            <div>
              <Button type="button" variant="outline" onClick={() => setPlayerOpen(true)}>
                Open embedded player
              </Button>
            </div>
          </CardContent>
        </Card>
      )}

      {playerOpen && playback?.playUrl && playerSource && (
        <div className="direct-play-overlay z-[100]">
          <Button
            type="button"
            variant="ghost"
            size="icon"
            onClick={closePlayer}
            aria-label="Close player"
            className="absolute right-3 top-3 z-[110] bg-black/40 text-white hover:bg-black/60 hover:text-white"
          >
            <X className="h-4 w-4" />
          </Button>
          <div className="h-full w-full p-0">
            <Plyr source={playerSource} options={playerOptions} />
          </div>
        </div>
      )}
    </div>
  )
})
