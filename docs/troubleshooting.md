# Troubleshooting

If you're stuck, please either open a [GitHub issue](https://github.com/Gaisberg/streamnzb/issues) or report it in the [Discord](https://snzb.stream/discord) `#help` channel (they sync via [GitThread](https://gitthreadsync.snzb.stream/)). Include downloaded logs when relevant, and include the copied bad match report from **History** when the issue is about a wrong or poor release match. For "why am I getting no (or few) streams", expand the request on **History** — its search panel shows what each indexer returned and what filtering dropped. Sensitive data should be automatically redacted but please double-check before posting.

## Buffering behind Cloudflare Tunnel

Exposing StreamNZB through a Cloudflare Tunnel (`cloudflared`) is not recommended. The playback path streams large amounts of video data continuously, and routing it through the tunnel can throttle sustained throughput and cause unnecessary buffering — even when the server itself is keeping up. If you see buffering that doesn't match the connection speeds on the dashboard, try playing directly against the server (LAN address or a direct reverse proxy) to rule the tunnel out. For remote access, prefer a VPN (e.g. WireGuard/Tailscale) or a plain reverse proxy on a directly reachable host. Proxying large volumes of video traffic through Cloudflare may also violate their terms of service.

## Playback takes several seconds to start

Starting a release means measuring things that are not written down anywhere. The NZB has to be fetched, its volumes STAT-sampled for holes, the archive mapped, the container header run past ffprobe — and, for every volume, one article has to be downloaded just to learn how large a decoded segment actually is, because the NZB only records the encoded size. On a large multi-volume release those measurements are most of the wait before the first frame.

**Within a release, an article size is measured once.** Volumes of one release share a posting, so the size measured on the first volume is reused by the rest — a `Segment map probe plan` line reporting `known_from_estimator=1` measured only what it had not seen before. Only the final, short article of each volume is genuinely its own.

**Most posts write their own map.** The yEnc headers inside each article declare the file's total size and the article's exact offset within it. When those declarations prove a regular layout, the map is taken from them directly (`Exact segment map from yEnc part headers`) — exact to the byte, from the same one or two articles the measurement already fetched. Only posts with irregular or contradictory headers fall back to measuring and scaling.

**They are paid once per release, not once per play.** The plan and the measured segment maps are stored with the release in the library, so a later play of the same release restores both and goes straight to reading — for a multi-volume archive and for a directly-posted file alike. The log says `Reused persisted blueprint from library` and `Restored persisted segment maps with blueprint` when that happens; a release still being measured logs `Segment map probe plan` instead.

**Thorough ffprobe runs read the stream over a loopback listener.** Preloading (speculative pre-probing) and strict validation serve the stream to ffprobe on an ephemeral `127.0.0.1` port (answering only a per-probe random path) instead of a pipe, so ffprobe seeks to exactly the container structures it needs — including a trailing MP4 `moov`, which is unreadable over a pipe. The port exists only for the seconds the probe runs; a sandbox that forbids loopback listeners makes those probes fall back to the pipe. The quick serve-time validation stays on the pipe on purpose: it sits on time-to-first-byte, and being unseekable is what keeps it bounded.

**The end of the file is warmed while the beginning is being probed.** Players read the container index — Matroska cues, a trailing MP4 `moov` — before they show a frame, and that read lands where nothing else has touched: the last volume of an archive, or the final articles of a direct file. Both are prepared alongside the ffprobe run rather than after it (`Warmed playback tail during startup`), so the seek that follows startup does not stall on its own round trips.

**Where the time went is in the log.** At DEBUG, `Play resolve timing`, `Playback open source timing` and `Playback prepare timing` break one startup into its phases in milliseconds — slot recovery, NZB download, the volume STAT sweep, archive mapping, header validation, probe and open. A slow start is one of those numbers, and they say which.

**A cold search is a separate cost.** If the play URL outlived its cached search — a long pause, or a restart — the indexers are queried again before the release can be resolved, logged as `Playback playlist cache miss` followed by `Play: recovered after cache/session eviction`. That is the search fan-out, not the release; the numbers above start after it.

## Playback glitches or drops on a damaged release

Usenet releases decay: individual articles go missing from providers over time. StreamNZB tiers its response by how much of a file is actually gone, rather than treating every miss the same way.

**Preloading can check every article instead of a sample.** The sampled check — the first two volumes in full, six points in every later volume — sees about five percent of a file, so a single missing article mid-body usually goes unseen until a seek lands on it. **Check every article when preloading** (Settings → Advanced → Playback, off by default) makes preloading STAT every article of the selected file, in an order that keeps whatever it reaches within its budget spread evenly over the file (the head, the tail and the first article of every volume first, then a bit-reversal permutation of the rest). It runs at the pool's own STAT width and inside the preload attempt's window, logged as `Article census finished` with how much of the file it reached. A hole found sends the player to the next candidate, exactly as the sampled check does. Holes the NZB itself declares (numbering gaps) are not the census's to judge — the pre-flight already counts those — so it only ever reports articles a provider was asked about. It applies to RAR archive releases; direct and 7z releases are checked as before. It costs STAT connections and a few more seconds per preloaded candidate, which is why it is opt-in.

**Isolated holes are filled and playback continues.** When an article past a file's first segment is missing from every configured provider, that segment is zero-filled and the stream keeps running. Expect a brief glitch — a smeared frame, a click in the audio — where the gap was. The log records each one as `Segment unavailable, zero-filling gap`. A single file may accumulate up to 10 such holes, and no more than 4 of them in a row: a longer run is seconds of zeros inside the stream, which players do not survive, so it ends the stream as below instead. A gap the NZB itself declares is refused before the release is offered when it is longer than 4 articles, for the same reason.

**Past that, the release is unplayable.** The next hole ends the stream: the slot is marked failed, **History** records a failure for it, and the player is sent to the next candidate — or, when no candidate is left, a Stremio player gets the error video while a direct-play session (the **Play** page and the `/api/play` URLs) gets a plain HTTP error naming the cause, so scripts and external players see a failure instead of a placeholder clip. Reaching this point means the release itself is damaged, not that your providers are having a bad minute; transient errors (timeouts, cancellations, a provider dropping a connection) never count toward the limit — they surface as retries or a failover, never as silently zero-filled bytes.

**A missing first segment always fails immediately.** It carries the container and volume headers, and nothing downstream can make sense of a zero-filled one, so that miss stays a fast, definitive verdict about the release.

**Articles missing from the NZB itself are treated the same way.** Some indexers serve NZBs whose segment numbering skips articles the indexer never saw — the post is incomplete in the document, so no provider can ever supply those bytes. Small gaps are kept at their declared offsets and zero-filled like any other hole (logged as `NZB file is missing articles`); a gap larger than the 10-hole budget fails the release before playback starts, and the player is sent to the next candidate. Previously such a release served a file shorter than its own container header declared, which left some players looping on requests past the end of the stream instead of failing over.

**A missing first RAR volume is repaired, not refused.** Where the release ships a PAR2 recovery set with enough blocks, the volume is reconstructed from it in memory and playback proceeds — so a set that is missing the one file that opens it is still playable. The reconstruction is capped at 128 MB, which covers ordinary volume sizes; past that the release fails over as any other unplayable one would.

**Encrypted archives play when the password is in the NZB.** AES-encrypted RAR and 7z sets are decrypted as they stream, using the `password` meta field indexers put in the NZB head. A set whose password lives only in a forum post or a `.nfo` cannot be opened — there is nowhere to enter one.

**Ranges are proven before they are promised.** Before writing a response header, StreamNZB reads the first byte of the range the player asked for. A release that cannot deliver that byte gets a redirect to the next candidate — logged as `Refusing to advertise a range this release cannot deliver` — instead of a `206` that advertises a full length and then delivers nothing. Players fail over promptly rather than waiting on a response that will never arrive.

**A player stuck asking past the end of the file is failed over.** When the served file turns out smaller than its own container metadata declares — a truncated post the pre-flights missed, or a wrongly estimated size — the player keeps requesting a tail offset the stream cannot have and gets `416` after `416`, reloading forever. Three such requests on a play that has not yet delivered real bytes are taken as that loop, not a client quirk: the slot is failed over — logged as `Player keeps requesting ranges past the served size` — and the player lands on the next candidate. A play that already reached the good threshold is never failed by this, and no bad-release report is filed, since the articles themselves may be fine.

## Stalls after a seek, on a release with very large articles

Read-ahead — how much of the file is being fetched ahead of what the player is reading — is sized as a fraction of the file, which works out to a fraction of its runtime whatever the bitrate, and is then clamped to a sane number of megabytes and of articles.

It has to be, because the size of a single article is a poster's choice and varies by more than an order of magnitude between releases. Fetches in flight share the line, so a window counted in articles rather than bytes means a release posted in 4 MiB articles queues several times more data ahead of the player than one posted in 700 KB articles — and the article the player is actually waiting on arrives no sooner than the dozens it does not need yet. In the log that looks like a wave of `Slow segment fetch` warnings all reporting a similar duration, finishing at the same moment, with `Serve window` showing `read_blocked` close to the whole window.

Nothing is configurable here and nothing needs to be. If you see that pattern with modest article sizes, the bottleneck is the link or the providers rather than the window — check the connection speeds on the dashboard and the **Buffering behind Cloudflare Tunnel** note above.

## A provider reports "too many connections"

The provider's `502` at login means the account already has as many connections open as the plan allows, and the dashboard shows the provider as degraded with that reason. StreamNZB enforces the configured connection count per **account** rather than per activity: playback, the NNTP proxy, speed tests, connection tests, settings validation, health probes and a settings save that re-points a provider all draw on one allowance for that account, so the total StreamNZB holds never exceeds the count on the provider card — two provider entries with the same host and username share one allowance, and changing a provider's settings re-uses its existing connections instead of opening a second set beside them.

If you still see the message, the connections are being counted from somewhere else: another client on the same account (a downloader, a second StreamNZB), a connection count set above what the plan includes, or connections a previous run left for the provider to time out on its own — see [Backup and updates](backup-and-updates.md) for why a clean stop avoids the last one. The state clears itself; nothing is parked, and the next dial that succeeds marks the provider healthy again.

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
