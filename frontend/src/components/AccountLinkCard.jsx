import React, { useEffect, useRef, useState } from "react"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Check, Link2, Loader2 } from "lucide-react"
import { apiFetch } from "@/api"

// verificationLink normalises what the two services call the same thing.
// Simkl returns verification_url, MDBList verification_uri plus a
// ..._complete variant with the code already filled in — worth preferring for
// the href, because then the user only has to confirm rather than retype.
function verificationLink(code, fallback) {
  const shown = code?.verification_uri || code?.verification_url || fallback
  const href = code?.verification_uri_complete || shown
  let label = shown
  try {
    const parsed = new URL(shown)
    label = `${parsed.host}${parsed.pathname}`.replace(/\/$/, "")
  } catch {
    // A non-absolute URL is shown as-is rather than hidden behind a parse error.
  }
  return { href, label }
}

// AccountLinkCard drives the device-code link both watch-tracking services
// use: ask the server for a code, show it, poll until the service confirms.
// Simkl and MDBList differ only in their endpoints, their wording and how a
// failed poll should be read, so the flow itself lives here once.
//
// Every request names the stream the account belongs to: accounts are per
// stream, because a watch history is one person's and a stream is one
// person's.
//
// It renders as one of the stream editor's settings blocks rather than as a
// Card, because that is where it lives — a Card's own fill and shadow read as
// a panel dropped into the form rather than a row of it.
//
// haltOnCheckError says whether a failing poll is terminal. MDBList reports
// real outcomes that way — the user denied it, the code expired — so polling
// must stop and say so. Simkl's poll only fails transiently, and stopping on a
// blink of the network would strand a link that was about to succeed.
export function AccountLinkCard({
  name,
  stream,
  description,
  endpoints,
  checkBody,
  haltOnCheckError = false,
  fallbackVerificationURL,
  notEnabled,
  onAccountChange,
  children,
}) {
  const [status, setStatus] = useState(null)
  const [code, setCode] = useState(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState("")
  // Polling state lives in a ref: the interval callback must see the current
  // code without re-arming the timer on every render.
  const pollRef = useRef(null)

  const stopPolling = () => {
    if (pollRef.current) {
      clearInterval(pollRef.current)
      pollRef.current = null
    }
  }

  // Every call carries the stream, so one card cannot act on another's account.
  const withStream = (path) => `${path}?stream=${encodeURIComponent(stream || "")}`

  useEffect(() => {
    let cancelled = false
    if (!stream) {
      setStatus({ enabled: false, connected: false })
      return () => { cancelled = true; stopPolling() }
    }
    apiFetch(withStream(endpoints.status))
      .then((data) => { if (!cancelled) setStatus(data) })
      .catch(() => { if (!cancelled) setStatus({ enabled: false, connected: false }) })
    return () => { cancelled = true; stopPolling() }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [endpoints.status, stream])

  const finish = (nextStatus) => {
    stopPolling()
    setCode(null)
    setStatus(nextStatus)
    onAccountChange?.()
  }

  const startLink = async () => {
    setError("")
    setBusy(true)
    try {
      const data = await apiFetch(withStream(endpoints.start), { method: "POST" })
      setCode(data)
      const expiresAt = Date.now() + (data.expires_in || 900) * 1000
      pollRef.current = setInterval(async () => {
        if (Date.now() > expiresAt) {
          stopPolling()
          setCode(null)
          setError("The code expired before it was entered. Start over to get a new one.")
          return
        }
        try {
          const body = checkBody?.(data)
          const check = await apiFetch(withStream(endpoints.check), {
            method: "POST",
            ...(body ? { headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) } : {}),
          })
          if (check?.connected) finish(check.status)
        } catch (err) {
          if (!haltOnCheckError) return
          stopPolling()
          setCode(null)
          setError(err?.message || `${name} refused the authorization.`)
        }
      }, Math.max(data.interval || 5, 1) * 1000)
    } catch (err) {
      setError(err?.message || `Could not start the ${name} link.`)
    } finally {
      setBusy(false)
    }
  }

  const cancelLink = () => {
    stopPolling()
    setCode(null)
  }

  const disconnect = async () => {
    setError("")
    setBusy(true)
    try {
      finish(await apiFetch(withStream(endpoints.disconnect), { method: "POST" }))
    } catch (err) {
      setError(err?.message || "Disconnect failed.")
    } finally {
      setBusy(false)
    }
  }

  const link = verificationLink(code, fallbackVerificationURL)

  return (
    <div className="rounded-md border border-border/60 p-3">
      <div className="flex items-center justify-between gap-4">
        <div className="flex items-center gap-2 text-sm font-medium">
          <Link2 className="h-4 w-4 text-muted-foreground" /> {name}
        </div>
        {status?.connected ? (
          <Button size="sm" variant="outline" onClick={disconnect} disabled={busy}>
            Disconnect
          </Button>
        ) : (
          <Button size="sm" onClick={startLink} disabled={busy || !status || !status.enabled || Boolean(code)}>
            {busy ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : null} Connect
          </Button>
        )}
      </div>
      <p className="mt-3 text-sm text-muted-foreground">{description}</p>
      {status?.connected && (
        <>
          <p className="mt-2 flex items-center gap-1.5 text-sm text-emerald-500">
            <Check className="h-4 w-4" /> Connected{status.user_name ? ` as ${status.user_name}` : ""}.
          </p>
          {children}
        </>
      )}
      {status && !status.enabled && <div className="mt-2">{notEnabled}</div>}
      {error && <p className="mt-2 text-xs text-destructive">{error}</p>}
      <Dialog open={Boolean(code)} onOpenChange={(open) => { if (!open) cancelLink() }}>
        <DialogContent className="max-w-sm p-4 sm:p-6">
          <DialogHeader>
            <DialogTitle>Link {name}</DialogTitle>
            <DialogDescription>
              Enter this code at{" "}
              <a href={link.href} target="_blank" rel="noreferrer" className="underline underline-offset-2">
                {link.label}
              </a>{" "}
              and approve the app. This page updates by itself once you have.
            </DialogDescription>
          </DialogHeader>
          <p className="select-all rounded-md border border-border bg-muted/30 py-4 text-center font-mono text-3xl tracking-[0.3em]">
            {code?.user_code}
          </p>
          <p className="flex items-center justify-center gap-1.5 text-xs text-muted-foreground">
            <Loader2 className="h-3.5 w-3.5 animate-spin" /> Waiting for approval…
          </p>
        </DialogContent>
      </Dialog>
    </div>
  )
}
