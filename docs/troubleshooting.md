# Troubleshooting

If you're stuck, please either open a [GitHub issue](https://github.com/Gaisberg/streamnzb/issues) or report it in the [Discord](https://snzb.stream/discord) `#help` channel (they sync via [GitThread](https://gitthreadsync.snzb.stream/)). Include downloaded logs when relevant, and include the copied bad match report from **History** when the issue is about a wrong or poor release match. For "why am I getting no (or few) streams", expand the request on **History** — its search panel shows what each indexer returned and what filtering dropped. Sensitive data should be automatically redacted but please double-check before posting.

## Buffering behind Cloudflare Tunnel

Exposing StreamNZB through a Cloudflare Tunnel (`cloudflared`) is not recommended. The playback path streams large amounts of video data continuously, and routing it through the tunnel can throttle sustained throughput and cause unnecessary buffering — even when the server itself is keeping up. If you see buffering that doesn't match the connection speeds on the dashboard, try playing directly against the server (LAN address or a direct reverse proxy) to rule the tunnel out. For remote access, prefer a VPN (e.g. WireGuard/Tailscale) or a plain reverse proxy on a directly reachable host. Proxying large volumes of video traffic through Cloudflare may also violate their terms of service.

## Playback glitches or drops on a damaged release

Usenet releases decay: individual articles go missing from providers over time. StreamNZB tiers its response by how much of a file is actually gone, rather than treating every miss the same way.

**Isolated holes are filled and playback continues.** When an article past a file's first segment is missing from every configured provider, that segment is zero-filled and the stream keeps running. Expect a brief glitch — a smeared frame, a click in the audio — where the gap was. The log records each one as `Segment unavailable, zero-filling gap`. A single file may accumulate up to 10 such holes.

**Past that, the release is unplayable.** The next hole ends the stream: the slot is marked failed, **History** records a failure for it, and the player is sent to the next candidate — or to the error video when no candidate is left. Reaching this point means the release itself is damaged, not that your providers are having a bad minute; transient errors (timeouts, cancellations, a provider dropping a connection) never count toward the limit.

**A missing first segment always fails immediately.** It carries the container and volume headers, and nothing downstream can make sense of a zero-filled one, so that miss stays a fast, definitive verdict about the release.

**Ranges are proven before they are promised.** Before writing a response header, StreamNZB reads the first byte of the range the player asked for. A release that cannot deliver that byte gets a redirect to the next candidate — logged as `Refusing to advertise a range this release cannot deliver` — instead of a `206` that advertises a full length and then delivers nothing. Players fail over promptly rather than waiting on a response that will never arrive.

## An episode still shows no streams after it aired

Search results are cached per stream and title. A search that found **nothing** is cached for 15 minutes and its deadline is never pushed out by another request, so a title that had nothing behind it a moment ago is re-searched shortly after rather than staying empty for as long as the session cache lives. A search that found results keeps the longer sliding cache.

If it is still empty after that, the air-date gate may be holding it: an episode that has not aired answers with no results without asking any indexer, and **History** shows it as **Not aired yet** with the air date it read instead of a bare "No results". The gate opens as soon as that date begins anywhere on Earth (midnight at UTC+14), so it cannot hide a release that already exists — nothing airs before its own air date starts. The server's timezone does not enter into it; only its clock being badly wrong would. If the show's air dates simply run ahead of when its releases land, turn **Skip unaired episodes** off in that stream's **Indexers** tab (Streams → edit → Indexers).

## Locked out after repeated failed logins

The admin login backs off after repeated wrong passwords from the same address. The first four failures cost nothing; after that each further failure doubles the wait before the next attempt is accepted, starting at 2 seconds and stopping at 15 minutes. Attempts made during a wait are answered with `429 Too Many Requests` and a `Retry-After` header rather than being checked.

Waiting is the only thing required — the penalty expires on its own, and restarting StreamNZB is not needed. A single correct password clears the history immediately, so a run of typos leaves nothing behind once you get in.

One caveat behind a reverse proxy or tunnel: every request arrives from the proxy's address, so the backoff applies to that one address rather than to each client separately. Forwarded headers are deliberately ignored here, because anyone can set them and would otherwise be able to sidestep their own backoff. On a single-admin instance this mainly means someone else's failed attempts can make you wait too.

Passwords themselves are stored as salted argon2id hashes. Instances created before that change carry the older format and are upgraded silently on the next successful login — there is nothing to do, and no need to re-enter the password.

## Force password reset on next startup

If you need to force the admin account to land on the password-change screen after restart, set:

```env
ADMIN_FORCE_PASSWORD_RESET=true
```

After the password has been changed, remove or disable this env var.
When it remains enabled, StreamNZB will keep forcing the password-reset prompt on startup.
