import React from "react"
import { AccountLinkCard } from "@/components/AccountLinkCard"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"

// The watch-tracking account cards. Both live with the stream rather than with
// the server: a Simkl or MDBList account is one person's, and a stream is one
// person's, so every stream links its own.

// ScrobbleToggle is the "report playback to this account" switch, shown inside
// a card and only once that card is connected — with nothing linked there is
// no account to report into, so the switch would be a control over nothing.
// It is a divided row rather than another bordered block, because it already
// sits inside one.
function ScrobbleToggle({ id, service, value, onChange }) {
  return (
    <div className="mt-3 border-t border-border/60 pt-3">
      <div className="flex items-center justify-between gap-4">
        <Label htmlFor={id} className="text-sm font-medium">Scrobble playback</Label>
        <Switch id={id} checked={value === true} onCheckedChange={(next) => onChange(next === true)} />
      </div>
      <p className="mt-2 text-xs text-muted-foreground">
        Report this stream&apos;s playback to the account above: &quot;watching now&quot; while a title
        streams, watched progress when it stops — {service} marks it watched past 80%, and keeps the
        resume position below that.
      </p>
    </div>
  )
}

// SimklCard links one stream's Simkl account. The watchlist catalog rows only
// appear in the registry once some stream has linked an account, so the parent
// refetches the catalog list on every connect/disconnect.
export function SimklCard({ stream, onAccountChange, scrobble, onScrobbleChange }) {
  return (
    <AccountLinkCard
      name="Simkl"
      stream={stream}
      endpoints={{
        status: "/api/simkl/status",
        start: "/api/simkl/pin",
        check: "/api/simkl/pin/check",
        disconnect: "/api/simkl/disconnect",
      }}
      checkBody={(code) => ({ user_code: code.user_code })}
      fallbackVerificationURL="https://simkl.com/pin/"
      onAccountChange={onAccountChange}
      description={
        <>
          This stream&apos;s own Simkl account. Linking it serves that account&apos;s watchlists —
          Watching, Plan to Watch, On Hold, Completed and Dropped — as catalog rows, and lets this
          stream scrobble to it. Every stream links its own.
        </>
      }
      notEnabled={
        <p className="text-xs text-muted-foreground">
          No Simkl client id is available in this build. Create an app at{" "}
          <a href="https://simkl.com/settings/developer/" target="_blank" rel="noreferrer" className="underline underline-offset-2">
            simkl.com/settings/developer
          </a>{" "}
          and paste its client id under API keys on the Metadata page, then connect.
        </p>
      }
    >
      <ScrobbleToggle
        id="simkl-scrobble"
        service="Simkl"
        value={scrobble}
        onChange={onScrobbleChange}
      />
    </AccountLinkCard>
  )
}

// MDBListCard links one stream's MDBList account through the OAuth device-code
// flow. Unlike Simkl no catalog rows come with it — MDBList lists are imported
// from their public URLs, linked account or not — so the card exists purely
// for scrobbling.
export function MDBListCard({ stream, scrobble, onScrobbleChange }) {
  return (
    <AccountLinkCard
      name="MDBList"
      stream={stream}
      endpoints={{
        status: "/api/mdblist/status",
        start: "/api/mdblist/device",
        check: "/api/mdblist/device/check",
        disconnect: "/api/mdblist/disconnect",
      }}
      haltOnCheckError
      fallbackVerificationURL="https://mdblist.com/oauth/device/"
      description={
        <>
          This stream&apos;s own <a href="https://mdblist.com" target="_blank" rel="noreferrer" className="underline underline-offset-2">MDBList</a>{" "}
          account. Linking it lets this stream report what it plays, so its watched history and
          continue-watching follow you to every other client reading that account.
        </>
      }
      notEnabled={
        <p className="text-xs text-muted-foreground">
          No MDBList client id is available in this build. Register a{" "}
          <span className="font-medium">Device Code App</span> at{" "}
          <a href="https://mdblist.com/developer" target="_blank" rel="noreferrer" className="underline underline-offset-2">
            mdblist.com/developer
          </a>{" "}
          and paste its client id under API keys on the Metadata page, then connect.
        </p>
      }
    >
      <ScrobbleToggle
        id="mdblist-scrobble"
        service="MDBList"
        value={scrobble}
        onChange={onScrobbleChange}
      />
    </AccountLinkCard>
  )
}
