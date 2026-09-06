# Indexer & provider health

An indexer or provider being **enabled** says what you asked for. It does not say whether it still works. A password changed at the provider's end, an API key revoked by an indexer, a subscription that lapsed — none of those flip the switch in Settings, and before this existed they showed up only as searches quietly returning less and playback quietly failing over.

StreamNZB tracks a second, separate fact for every configured indexer and provider: whether it is actually usable right now. It is never written back into your configuration — your switch stays yours — and it appears in three places:

- **Dashboard** — inside the affected provider or indexer card in the *Usenet Providers* / *Indexers* sections: the status dot turns red (blocked) or amber (degraded) and a notice underneath names the reason and quotes the server's own line, with **Check again** right there for blocked components.
- **Settings → Indexers / Providers** — a badge on the affected card, next to the status dot.
- **Live** — state changes are pushed to the open UI, so a subscription that lapses mid-session appears where you are already looking.

## The three states

| State | Meaning | Effect |
|---|---|---|
| **OK** | Nothing to report. | Normal use. No badge, no panel row. |
| **Degraded** | Working, but limited right now — daily quota spent, or the indexer asked us to back off. | Keeps being used where it can be. Ends on its own. |
| **Blocked** | The server rejected the account itself. | Indexers are skipped entirely; providers drop to last resort behind healthy ones. Needs you, or a probe that finds it fixed. |

## What can block a component

Only a definitive rejection. Timeouts, 5xx responses, connection resets and rate limits say nothing about your account, so they never block anything — a bad afternoon at an indexer must not retire a working API key.

| Reason | Raised when |
|---|---|
| `auth_failed` | An indexer answers a newznab `1xx` error code or HTTP 401; a provider rejects AUTHINFO with 481/482, or with a 502 whose text is about the account (`502 "Authentication Failed"` is what Eweka says for a lapsed subscription). |
| `quota_exhausted` | The configured daily API-hit or NZB-download budget is spent. Degraded, not blocked. |
| `throttled` | The indexer returned 429/503 and we are inside the cooldown. Degraded, not blocked. |
| `connection_limit` | The provider refused a connection and said so in words — "too many connections", "connection limit". Your account allows fewer connections than are configured — lower the count for that provider. Degraded, not blocked. |
| `login_refused` | The provider answered 502 without naming either the account or the connection count. Degraded, not blocked; the server's own line is shown so you can read what we could not. |

A provider's `502` is the ambiguous one: it is the pre-RFC 4643 code for a rejected login *and* what many servers answer when the account is out of connections. The code alone never decides — the words after it do, and they are shown under the notice in every case.

## When it is noticed

Every authenticated request is a chance to find out, so a broken credential rarely waits for a search:

- **On save, before the save is accepted.** Saving a changed indexer runs a live check against it with the new credentials, and a rejected key fails validation — the save is refused with the error on the field, not stored and discovered later. The check issues a minimal real search rather than a capabilities request, because many indexers serve capabilities publicly and a public endpoint cannot vouch for a key.
- **On startup.** Providers are dialled and indexers' capabilities fetched during boot; both report what the server said.
- **During use.** Any search or authenticated connection that is rejected records the verdict.
- **On probe.** The background re-check and the **Check again** button ask directly.

A private indexer's `403` is deliberately *not* treated as a credential verdict: those commonly come from a WAF that disliked something about the request rather than from your key.

## Recovery

A blocked component is never blocked permanently. Three things clear it:

1. **It works again.** Any successful search or authenticated connection clears the verdict on the spot.
2. **A background probe finds it fixed.** Blocked components are re-checked automatically, starting 15 minutes after the failure and backing off to hourly while the answer stays no. Indexers get a minimal authenticated search (one API hit — the price of an honest answer); providers get a throwaway connection that logs in and hangs up.
3. **You change the credentials.** Editing a provider's username, password, host or port — or an indexer's API key, username, password or URL — retires the stored verdict immediately, because you telling us the password is different is better evidence than anything recorded before you did.

The **Check again** button on the card runs the probe from step 2 immediately, for when you have just fixed something and want to see it go green. It works even for a provider whose credentials were already wrong at startup, and which therefore never connected.

Deleting an indexer or provider drops its health with it, and the state survives restarts — a password that changed yesterday is not rediscovered by hammering the provider on every boot.

## API

All three endpoints are admin-only; health names which credentials are failing, which no device token should be able to read.

| Endpoint | Method | Purpose |
|---|---|---|
| `/api/health/components` | GET | Every component that is not OK, blocked first. |
| `/api/health/components/retry` | POST | Re-check one component now. Body: `{"kind": "indexer"\|"provider", "name": "..."}`. |

Websocket clients also receive a `component_health` message per change, carrying the single updated record (a recovery arrives as state `ok`).
