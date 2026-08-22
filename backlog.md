# StreamNZB Backlog

Working backlog from the 2026-08-22 codebase review. Items are ordered by value
per unit of risk, not by size. Each item names the files it touches and what
"done" means, so it can be picked up cold.

Status legend: `[ ]` not started · `[~]` in progress · `[x]` done

Each finished item records what actually shipped and a one-line conventional
commit subject. Everything is landing as a single commit at the end of the run;
the per-item lines are kept so it can be composed — or split back apart — from
this file rather than from chat history.

---

## P0 — Do first

### 1. [x] CI runs the test suite

**Problem.** `.github/workflows/build-release.yml` fires on every push, builds
five platform binaries, and publishes images to ghcr + Docker Hub. It never runs
`go test`, `go vet`, or `eslint`. 32k lines of tests only execute when someone
remembers to run `build.sh` locally.

**Scope.** New `.github/workflows/ci.yml` (or a gating job in the existing
workflow) running, on push and pull_request:
- `go vet ./...`
- `go test ./pkg/...`
- `npm ci && npm run lint` in `frontend/`
- `gofmt -l` with a non-empty result failing the job

Consider adding `-race` here — it is unavailable on the dev machine (no cgo
toolchain), so CI is the only place it can run.

**Done when.** A pushed branch with a deliberately failing test goes red, and
`build-release` does not publish artifacts for a red tree.

**Files.** `.github/workflows/`

**Done.** `ci.yml` holds a `verify` job (gofmt check, `go vet`, `go test ./...`,
`npm ci`, `npm run lint`, `npm run build`) triggered on
`pull_request` and `workflow_call`. `build-release.yml` calls it as a `verify`
job and gates the publish on it via `needs: verify`, so pushes and PRs run the
same checks and neither can drift. CI reports formatting rather than rewriting
it, since `build.sh` runs `go fmt` and CI must not mutate the tree.

Follow-up left open on purpose: the `race` job (`go test -race ./...`) is
`continue-on-error: true`. The race detector needs cgo and cannot run on the dev
machine, so its first results are unknown — leaving it non-blocking means a
finding is visible immediately without stopping dev-build publishes. Promote it
into `verify` once it has been green for a while.

**Commit.** `ci: run vet, tests and lint before publishing`

---

### 2. [x] Harden admin authentication

**Problem.** Four separate weaknesses in the same path, on the panel that stores
Usenet provider credentials in plaintext and is routinely exposed to the internet:

1. `HashPassword` (`pkg/auth/stream.go:344`) is a single unsalted SHA-256.
2. Password and token comparisons are not constant-time — `passwordHash !=
   adminPasswordHash` in `Authenticate`, `stream.Token == token` in
   `AuthenticateToken`. `subtle.ConstantTimeCompare` appears exactly once in the
   whole codebase (`pkg/server/newznab/server.go:160`).
3. No rate limiting or lockout on `handleLogin` (`pkg/server/api/auth.go:25`).
4. Default credentials are `admin`/`admin` (mitigated by the forced password
   change, but the window exists).

**Scope.**
- Move to argon2id (or bcrypt) with a per-hash salt. Needs a migration path:
  detect the legacy 64-char hex hash on login, verify against it once, then
  transparently rehash and persist. Bump `CurrentConfigVersion` if the stored
  shape changes.
- `subtle.ConstantTimeCompare` for both comparisons.
- Per-IP throttle on failed logins (exponential backoff or a token bucket);
  keep it in-process, no new dependency.

**Done when.** Legacy configs still log in, the stored hash is upgraded in place
on first successful login, and repeated bad passwords from one IP are throttled.
Tests in `pkg/auth/stream_test.go` cover legacy-hash upgrade and the throttle.

**Files.** `pkg/auth/stream.go`, `pkg/server/api/auth.go`, `pkg/core/config/config.go`

**Done.** New `pkg/auth/password.go` holds argon2id hashing in PHC string
format, with the parameters carried inside each hash so raising them later
leaves existing hashes verifiable. `VerifyPassword` also accepts the legacy
unsalted SHA-256 digests, and `PasswordNeedsRehash` flags both those and
hashes written with weaker parameters; `upgradeAdminPasswordHash` rewrites them
after a successful login, the one point where the plaintext exists. The
`admin`/`admin` default deliberately stays in the legacy format — a salted hash
cannot be a constant — and upgrades itself on first use.

`Authenticate` now verifies the password on every path, including a wrong
username, so the two cannot be told apart by response time; both it and
`AuthenticateToken` compare with `subtle.ConstantTimeCompare`.
`pkg/server/api/login_throttle.go` adds a per-address backoff (four free
failures, then doubling from 2s to a 15min cap, answered as 429 with
`Retry-After`), with TTL and hard-ceiling pruning so the map cannot grow
without bound.

Two parameter choices worth knowing: argon2id runs at OWASP's minimum
19 MiB rather than the commonly quoted 64 MiB, and concurrent verifications are
capped at four by a semaphore. The memory cost that makes the hash expensive to
attack also makes it expensive to serve, and this runs on arm64 VPS and Pi-class
hardware — together those bound the transient footprint at roughly 76 MiB.

Adds `golang.org/x/crypto` as a direct dependency. `go mod tidy` also promoted
`github.com/expr-lang/expr` from indirect to direct, correcting a pre-existing
mislabel — comment-only change, no version moved.

**Commit.** `feat(auth): argon2id passwords, constant-time compares, login backoff`

---

### 3. [x] Session cookie `Secure` flag follows the actual scheme

**Problem.** `Secure: false` is hardcoded at `pkg/server/api/auth.go:59` (login)
and `:158` (logout). Behind a TLS reverse proxy or Cloudflare Tunnel — the
common deployment — the session cookie ships without the Secure flag.

**Scope.** Derive it from the request: `r.TLS != nil` or a trusted
`X-Forwarded-Proto: https`. Both call sites, plus `pkg/auth/middleware.go:48`.

Related, decide separately: the same token is also mirrored into `localStorage`
(`frontend/src/api.js:46`), which hands an XSS the credential the HttpOnly
cookie exists to protect. Split out as item 13.

**Done when.** An HTTPS request sets `Secure`, a plain-HTTP LAN request does not
(so localhost setup still works), and the trusted-proxy header is not honoured
blindly from any source.

**Files.** `pkg/server/api/auth.go`, `pkg/auth/middleware.go`, maybe `frontend/src/api.js`

**Carried over from item 2.** That item added `clientAddr` in
`pkg/server/api/login_throttle.go`, which reads `RemoteAddr` only and
deliberately ignores forwarded headers. This item needs the same trusted-proxy
decision for the scheme, so build it once here and have both use it. The
`net.SplitHostPort` fallback is currently inlined three more times
(`pkg/server/stremio/handlers_playback.go:1356` and `:2503`,
`pkg/server/api/stats.go:189`) — fold those in when the shared helper lands.
It needs a home both `pkg/server/api` and `pkg/server/stremio` can import, since
they are siblings.

**Done.** New `pkg/core/httpx` holds `ClientIP`, `HostFromAddr` and `IsSecure`
— in `pkg/core` because `pkg/auth`, which builds the cookie, sits below both
server packages. `pkg/auth/cookie.go` adds `SessionCookieName`,
`SessionCookieMaxAge`, `SessionCookie(r, value, maxAge)` and
`ClearSessionCookie(r)`, so the cookie is constructed in exactly one place; the
three hand-rolled copies (login, logout, and the stale-cookie clear in
middleware) are gone, and the middleware copy picks up the `SameSite=Strict` it
had been missing. The four `"auth_session"` string literals collapsed into the
constant, and the three inlined `net.SplitHostPort` fallbacks
(`handlers_playback.go` ×2, `stats.go`) now call the helper.

The trusted-proxy question resolved into an asymmetry rather than a config
knob: `X-Forwarded-Proto` **is** trusted for the scheme, `X-Forwarded-For` is
**not** trusted for identity. A forged proto header only ever describes the
attacker's own request and the worst it can do is mark their own cookie
`Secure`, locking themselves out of plain HTTP — while a forged
`X-Forwarded-For` would let them pick their own rate-limit bucket. So no
trusted-proxy CIDR setting was needed, which also keeps a deployment-level knob
out of the Settings UI. nginx users need `proxy_set_header X-Forwarded-Proto
$scheme` (documented in `docs/remote-access.md`); Caddy sets it already.

**Commit.** `fix(auth): mark the session cookie Secure when the client is on HTTPS`

---

## P1 — Operational correctness

### 4. [x] Graceful shutdown on SIGTERM/SIGINT

**Problem.** `main()` ends at `addonServer.wait()` with no `signal.Notify`
anywhere in the tree. `docker stop` kills the process mid-write: NNTP pools are
not drained, sessions are not closed, pending metrics batches are lost.

**Scope.** Signal handler in `cmd/streamnzb/main.go` that triggers, in order:
`http.Server.Shutdown` with a timeout, `sessionManager.Shutdown()`, NNTP proxy
stop, provider pool `Shutdown()`, persistence close. `rebindableServer` needs a
`shutdown(ctx)` that closes the current listener and drains.

**Done when.** SIGTERM exits 0 within the grace period with no truncated writes,
and an in-flight playback stream is closed rather than severed.

**Files.** `cmd/streamnzb/main.go`, `cmd/streamnzb/httpserver.go`

**Done.** New `cmd/streamnzb/shutdown.go` holds `shutdownSignals` (SIGTERM and
SIGINT) and `gracefulShutdown`. `main` now selects between a listener failure
and a signal, and runs the teardown on **both** paths — a dead listener still
leaves sessions, provider connections and unflushed counters behind.
`rebindableServer.wait()` became `failures()` so it can be selected on, and
gained `shutdown(ctx)`.

The ordering is the substance and is commented as such: HTTP, then the NNTP
proxy, then sessions, then provider pools, then persistence. Two of those edges
are load-bearing rather than tidy — sessions must close before the pools they
borrowed connections from, and `ClientPool.Shutdown` flushes usage counters
*through* the state manager, so closing the database first would silently drop
the last window of provider usage.

`session.Manager.Shutdown` now also closes every live session, not just the
cleanup loop. That is what actually releases the NNTP connections: dropping the
sockets instead leaves a provider counting them against the account's limit
until it notices, so a restart can come back short of connections it is
entitled to.

Grace period is 5s — enough for an API call, and inside Docker's 10s stop
timeout with room for the teardown that follows. Playback is deliberately cut
rather than waited on; `http.Server.Shutdown` leaves established connections
alone, so `Close` follows it.

Also widened `go test ./pkg/...` to `./...` in both `build.sh` and `ci.yml`:
`cmd/streamnzb` had no tests before, so nothing there would ever have run.

**Commit.** `feat(shutdown): release sessions, connections and counters on SIGTERM`

---

### 5. [x] HTTP server read/idle timeouts

**Problem.** `&http.Server{Handler: handler}` (`cmd/streamnzb/httpserver.go:28`)
sets no timeouts at all.

**Scope.** Add `ReadHeaderTimeout` and `IdleTimeout`. Leave `ReadTimeout`
generous and `WriteTimeout` at 0 — streaming responses run for hours, and the
per-write deadline is already handled by `writeTimeoutResponseWriter`
(`pkg/server/stremio/handlers_playback.go:43`). Note that in a comment so nobody
"fixes" it later by setting WriteTimeout.

**Done when.** A connection that opens and sends no headers is dropped; a
multi-hour range read is unaffected.

**Files.** `cmd/streamnzb/httpserver.go`

**Done.** `ReadHeaderTimeout` 20s, `ReadTimeout` 5min, `IdleTimeout` 2min,
`WriteTimeout` explicitly 0 with a comment saying why, so it does not get
"fixed" later. `ReadTimeout` is sized against `maxPlayNZBUploadBytes` (16 MiB)
— the largest thing a client can legitimately send — rather than picked round.

`ReadTimeout` needed checking rather than reasoning. Go applies it to the
connection, not just to the request, and the server keeps a background read
running on that connection to notice client disconnects; if that read inherited
the deadline, every playback response would die exactly `ReadTimeout` after it
started. It does not — `startBackgroundRead` clears the deadline first — but
that is an implementation detail of net/http, not a promise, so
`TestReadTimeoutDoesNotCutALongResponse` pins it: a response still being written
well past the deadline must arrive whole. Should a future Go release change it,
the fix is `ReadTimeout: 0`, keeping `ReadHeaderTimeout` for the slowloris case.

No docs change: nothing here is configurable and nothing changes that a user
would observe, beyond a stalled upload now failing instead of hanging.

**Commit.** `fix(server): bound header and idle timeouts without capping playback`

---

### 6. [x] Expire the search caches in the background

**Problem.** `playlistCache` and `rawSearchCache` carry TTLs, but expiry is only
evaluated on read. An entry for a title nobody requests again lives until a
config change calls `ClearSearchCaches`. `nextReleaseIndex` has no TTL at all —
`LoadOrStore` only, cleared wholesale.

**Scope.** One janitor goroutine sweeping all three on a ticker (drop entries
past `until`; give `nextReleaseIndex` cursors a last-touched stamp and an idle
TTL). Must stop on shutdown — depends on item 4 for the stop channel.

**Done when.** Memory for a long-lived instance browsing many distinct titles
plateaus instead of climbing. A test asserts expired entries disappear without
an intervening read.

**Files.** `pkg/server/stremio/server.go`, `pkg/server/stremio/handlers_playlist.go`

**Done.** New `pkg/server/stremio/cache_sweep.go`: a janitor on a 5-minute
ticker calling `sweepSearchCaches`, which drops expired playlist and raw-search
entries plus `nextReleaseIndex` cursors idle for a full cache lifetime.
`nextReleaseCursor` gained `lastTouched`, stamped at creation as well as on use
— left zero, a cursor would look idle forever in the window between
`LoadOrStore` and taking its lock. Cursors share the playlist TTL rather than
getting one of their own: once the playlist a cursor walks has expired, the
position it holds means nothing.

Deliberately a TTL sweep, not a size cap: entries are already bounded by their
deadline, and count-based eviction would throw away playlists that are still
valid. `sweepSearchCaches` returns its counts so tests assert on behaviour
rather than reaching into the maps.

Lifecycle: `stopCh`/`stopOnce` on the server, `Shutdown()` (nil-safe, since the
package's tests build bare `&Server{}` literals), called from
`gracefulShutdown` right after HTTP.

**Found while wiring this up:** `startLibraryFreshnessSweeper` had an
unconditional `for { <-timer.C; ... }` with no stop path at all. It queries the
library, so during shutdown it could still have been mid-query when the database
closed underneath it — the exact hazard item 4 sequenced everything else to
avoid. It now selects on `stopCh` too. Also moved the janitor start below
`CheckPort`, so a rejected port no longer leaves goroutines on a `Server` nobody
holds.

**Commit.** `fix(stremio): expire search caches and cursors on a timer, not on read`

---

## P2 — Structural

### 7. [x] Move session-scoped registries onto `Session`

**Problem.** `stremio.Server` has 40 fields, ten of which are `sync.Map`s used as
ad-hoc per-session registries: `recordedSuccessSessionIDs`,
`recordedPreloadSessionIDs`, `recordedFailureSessionIDs`,
`loggedThresholdSkipIDs`, `pendingLibrarySavedIDs`,
`pendingAttemptResolutions`, `preProbeCancels`, `nextReleaseIndex`. Each is keyed
by session ID and garbage-collected by a goroutine parked on `<-sess.Done()` —
so a live session parks several goroutines whose only job is to delete map keys.

This is per-session state living outside `Session`, and it is the direct cause of
item 8.

**Scope.** Fold the once-per-session flags into `Session` as guarded fields (or a
small `once` set), cleaned once at teardown. Keep the genuinely cross-session
caches (`playlistCache`, `rawSearchCache`) on the server. Respect the existing
lock ordering: `Manager.mu` → `Session.mu`, never inverted.

**Done when.** No `go func(){ <-done; m.Delete(id) }()` pattern remains, the
`Server` struct is materially smaller, and `pkg/server/stremio` tests still pass
unchanged.

**Files.** `pkg/session/manager.go`, `pkg/server/stremio/server.go`,
`pkg/server/stremio/handlers_playback.go`

**Done.** Ten `sync.Map`s down to four. The six keyed by session ID are gone;
the four that remain (`playlistCache`, `rawSearchCache`, `nextReleaseIndex`,
`preProbeCancels`) are keyed by something else and genuinely belong on the
server. All five `go func(){ <-done; m.Delete(id) }()` cleanup goroutines are
gone — the only `<-done` waiters left are real cancellation watchers.

New `pkg/session/bookkeeping.go` provides the mechanism, deliberately generic:
`Once`/`OnceDone`/`ResetOnce` over an `OnceKey`, plus
`BeginDeferred`/`DeferredIsCurrent`/`CancelDeferred` for work that can be
superseded. `pkg/session` therefore learns no stremio vocabulary — the keys and
their meanings live in `pkg/server/stremio/once_keys.go`. Both are guarded by
the existing session mutex, so no new lock and no new ordering.

Why this works at all: a session ID is a *slot path*, and slot paths are reused.
That is the entire reason the old maps needed a parked goroutine per entry —
the flag had to be forgotten before the same slot came back. Held on the
session, the flags end when the session does, and a later play of the same slot
starts clean because it is a different session. `TestOnceIsPerSession` pins that.

`pendingAttemptResolutions` became a per-session counter instead of a
`time.Now().UnixNano()` token, so two resolutions scheduled in the same instant
can no longer collide.

Two deliberate behaviour changes, both narrowing:

- Added `Manager.PeekSession`, a lookup that does not stamp `LastAccess`, and
  used it for the "has this slot committed?" question in
  `advanceNextReleaseCursor`. Going through `GetSession` would have kept alive
  the session being asked about — a bug this refactor would otherwise have
  introduced, since the old code read a plain map.
- A slot whose session has been evicted now reads as not-committed immediately,
  rather than depending on whether its cleanup goroutine had run yet. Same
  answer, minus the race.

`handlePlay` fell from 438 to 422 lines as a side effect. Item 8 is the one that
actually addresses its shape.

**Commit.** `refactor(stremio): keep per-session bookkeeping on the session`

---

### 8. [x] Break up `handlePlay`

**Problem.** 438 lines (`pkg/server/stremio/handlers_playback.go:1177`) — the
longest function in the repo. It contains the failover retry loop, five dedup
registries, four spawned goroutines, and a closure that mutates an outer
`serveFailureRecorded` bool across goroutine boundaries. It is correct as far as
review could tell and it is well commented, but it cannot be unit tested and the
next change to it will be expensive.

**Scope.** Do item 7 first — it removes a large share of the noise. Then extract
the failover resolution loop into something callable (returning a resolved
session + prepared stream), leaving the handler as: resolve → serve → record.
Per the layering rules, the failover mechanics belong in `pkg/playback`, not the
handler.

**Done when.** The handler reads as resolve → call one service → format, and the
extracted resolution step has direct tests.

**Files.** `pkg/server/stremio/handlers_playback.go`, `pkg/playback/service.go`

**Done.** 438 lines to 19. `handlePlay` is now: cancel the pre-probe, resolve,
serve. The body moved into two new files —
`pkg/server/stremio/play_resolve.go` (`resolvePlaybackSlot`, 74 lines, plus
`openPlaySession`, `recordFailedSlot`, `nextFallbackSlot`, `playbackContext`,
`redirectToResolvedSlot`) and `pkg/server/stremio/play_serve.go`
(`servePlaybackStream`, 162 lines, plus `primeRangeOrFailover`, `recordTTFF`,
`serveWindowLogger`, `finishServeBookkeeping`). The longest function in the repo
is now 162 lines rather than 438.

The seam is worth stating: **resolve writes nothing to the response body.** That
is what makes failover possible at all — a client that has already been handed a
status line and a Content-Length cannot be moved to a different release. Serve
starts only once the slot is settled, which is why nothing in it reconsiders
which release to play.

**It did not move to `pkg/playback`, and the backlog was wrong to assume it
should.** `pkg/playback` owns opening and probing *one* stream for *one*
session, and `Prepare` is already called from the loop. Everything else in that
loop is stremio's vocabulary: slot paths, playlists, `auth.Stream`, attempt
history rows, AvailNZB verdicts, HTTP redirects. Moving it would have meant
handing `pkg/playback` six or seven callbacks over types that sit above it —
inverting real dependencies to satisfy a table entry, and leaving the code
harder to follow than it is now.

Two simplifications fell out. `requestedSessionID` was dropped from
`commitGoodAttemptIfQualified` and `logBelowGoodThresholdOnce`: it could only
ever equal `sessionID` there, so both were logging the same value under two
keys. And `providerName` in the TTFF path turned out to be declared, read, and
never assigned — see item 15.

Six direct tests on the extracted seams (context cancellation from both ends,
the redirect's Location/query/stream release, HEAD skipping the range prime,
failover-disabled short-circuit, failed-slot bookkeeping). Writing them turned
up a nil-context panic in `session.Done()` on any session not built by the
manager — it guarded a nil receiver but not a nil context, the way `Context()`
already did. Fixed there.

**Commit.** `refactor(stremio): split handlePlay into resolve and serve`

---

### 9. [x] Resolve the dead `failedOver` branch

**Problem.** `handlers_playback.go:1351` computes
`failedOver := sessionID != requestedSessionID`, but the block at `:1315` returns
unconditionally when those two differ. So `failedOver` is always false and the
`r.Header.Del("Range")` beneath it never runs.

**Decide which it is:** the redirect made the Range reset obsolete (delete the
dead branch), or the internal-failover path silently lost its Range reset when
the redirect was introduced (a real bug — a failed-over slot would inherit a
Range computed against a different file's length).

**Done when.** Either the branch is gone, or the Range reset happens where
failover actually occurs, with a test pinning the behaviour.

**Files.** `pkg/server/stremio/handlers_playback.go`

**Done — the branch was obsolete, not a lost bug.** Internal failover no longer
serves a different file over the URL the client asked for: it redirects, and the
client re-sends its Range against the file it is actually about to get. So there
is nothing left to reset. The branch is gone, along with the two `failed_over`
log fields that could only ever print `false`.

Item 8 made this moot rather than merely fixing it — after the split, serve only
ever runs on a slot the client asked for, so the condition cannot be written any
more. `TestRedirectToResolvedSlotSendsTheClientOnAndReleasesTheStream` pins the
redirect that replaced it, including that the query string survives.

**Commit.** folded into item 8

---

## P3 — Frontend

### 10. [x] Error boundary around the admin UI

**Problem.** No `ErrorBoundary` or `componentDidCatch` anywhere in
`frontend/src`. One throw during render blanks the entire admin UI with no
recovery path and no indication of what happened.

**Scope.** A boundary at the app shell plus one per routed page, rendering a
readable fallback with a reload action, in the existing design language
(`frontend/src/components/ui/`).

**Done when.** A component that throws degrades to the fallback while the rest of
the shell stays usable.

**Files.** `frontend/src/App.jsx`, `frontend/src/components/`

**Done.** New `frontend/src/components/ErrorBoundary.jsx` — a class component,
since React only reports render errors to one, with a fallback in the existing
Card/Button language offering both "Try again" (resets the boundary) and a full
reload. It logs the error and component stack to the console, and says plainly
that the server and any playing streams are unaffected, because a blank admin
page reads like the whole thing died.

Two boundaries, doing different jobs:

- One per page in `App.jsx`, wrapping the routed content inside `SidebarInset`,
  so a page that throws leaves the sidebar and header usable. It is keyed on
  `activePage` — React never resets a boundary by itself, so without the key a
  crash on one page would follow the user to every other page.
- One around `<App />` in `main.jsx` for the shell itself: the sidebar, the
  header, and the login and password-change screens that render before the page
  boundary exists.

Its test comes with item 12, which is where the frontend gets a test runner.

**Commit.** `feat(ui): keep a crashed page from taking down the admin UI`

---

### 11. [~] Split the oversized components

**Problem.** `StreamManagement.jsx` is 1,683 lines with 21 `useState` calls;
`SearchQuerySettings.jsx` 1,250; `NZBHistoryPage.jsx` 1,201.

**Scope.** Opportunistic, not a dedicated sprint — split the next one you have to
touch anyway. Pull the state cluster into a hook under `frontend/src/hooks/`
(the pattern already exists: `useSettingsState`, `useAdminRuntime`) and lift the
repeated sub-sections into components. Per the DRY rules, extract and
parameterize rather than copying.

**Done when.** Nothing measurable — treat as a standing rule for edits in those
files. Left at `[~]`: a policy has no finish line.

**Files.** `frontend/src/components/`

**First application.** `StreamManagement.jsx` 1,683 → 1,442 lines, with 266
lines of pure logic moved to `frontend/src/lib/streams.js`.

The cut was chosen to serve item 12 rather than to hit a line count: everything
moved is a pure function — shaping a stream between its config form, its draft
in the dialog, and the summary rows on the card — with no React, no DOM and no
network in any of it. Sitting at the top of a 1,600-line component they could
not be called from a test at all without mounting the component; in `lib/` they
are ordinary functions with ordinary inputs.

Three helpers (`defaultStreamName`, `pickConnectionLimits`, `sortedByKey`) and
one constant (`tabFieldErrorKeys`) turned out to be used only by their
neighbours, so they stayed unexported rather than becoming public surface by
accident.

The remaining 1,442 lines are `StreamDialog` and `StreamManagement` themselves —
JSX and hooks, which is the part that only splits by pulling state into a hook.
That is the next application, when something in there needs changing anyway.

**Commit.** `refactor(ui): lift the Streams page's pure logic out of the component`

---

### 12. [x] Frontend tests

**Problem.** Zero test files in `frontend/src` against 18.6k lines. The backend
is at roughly 50% test-to-source; the frontend is at zero.

**Scope.** Vitest + Testing Library. Start with the logic that has real branches
and no DOM: `frontend/src/lib/profiles.js` (651 lines), `frontend/src/api.js`
error/401 handling, `useSettingsState`. Component tests only where behaviour is
non-obvious. Wire `npm test` into `build.sh` and the CI job from item 1.

**Done when.** `npm test` runs in CI and covers the lib/hook layer.

**Files.** `frontend/`, `build.sh`, `.github/workflows/`

**Done.** Vitest + Testing Library + jsdom, 37 tests across four files, wired
into `build.sh`, `build.bat` and the CI `verify` job. `vite.config.js` carries
the test block so the `@` alias and the React plugin apply identically to tests
and to the build.

The default environment is `node`, with jsdom opted into per file by a
`@vitest-environment` docblock. The lib tests are plain functions and run in
6ms; paying for a DOM they never touch would have made the suite slower for no
reason.

Covered: `rulesToText`/`rulesFromText` in `lib/profiles.js` (round-trip, the
three actions, scope and off tags, the colon-splitting rule that lets both a
name and a condition contain one, and that a bad line throws with its line
number instead of returning half a ruleset); the `lib/streams.js` helpers item
11 extracted; `apiFetch`'s error handling (JSON error, `http.Error` text body,
a proxy's HTML page falling back to the status line, field errors, and the 401
event including the `skipAuthNotify` case the auth check depends on); and the
item 10 `ErrorBoundary`, including that a sibling outside it stays mounted —
the property that makes a per-page boundary worth having.

One environment quirk documented in `src/test/setup.js`: Vitest's jsdom
environment exposes `sessionStorage` but not `localStorage`, though jsdom
itself provides it — verified directly rather than assumed. The setup file
installs the missing half rather than reshaping `api.js` around a gap in the
test environment.

`build.bat` had also drifted out of step with `build.sh` — still on
`go test ./pkg/...` from before item 4 — and is back in line.

**Commit.** `test(ui): add vitest and cover the rules parser, api client and error boundary`

---

### 13. [x] Stop mirroring the session token into `localStorage`

**Problem.** On login the token is written to `localStorage` as well as being
set as an HttpOnly cookie (`frontend/src/Login.jsx:33`,
`frontend/src/components/ChangePassword.jsx:41`, `frontend/src/App.jsx:190`),
and `apiFetch` sends it as a bearer header. Any XSS in the admin UI can read it
straight out, which is the exact thing HttpOnly exists to prevent.

**Checked while doing item 3 — removal looks viable.** Every browser-side
consumer already works on the cookie alone: `apiFetch` sends
`credentials: 'include'`, the log download in `LogsPage.jsx:21` is a plain fetch
with `credentials: 'include'` and only an *optional* bearer, and the WebSocket
authenticates from the cookie in `pkg/server/api/websocket.go:38`. The bearer
path stays on the server for non-browser API clients regardless — this is only
about the browser keeping a second copy.

**Scope.** Drop the writes and reads of `auth_token`, keep the server's bearer
support. The upgrade path needs a look: a session that authenticated with a
token but has no cookie would be logged out once, which is acceptable but should
be a deliberate decision rather than a surprise.

**Done when.** No `localStorage` reference to `auth_token` remains in
`frontend/src`, and login, reload, log download and the live log WebSocket all
still work.

**Done.** Every read and write of `auth_token` is gone; the HttpOnly cookie is
the browser's whole credential. `apiFetch` no longer attaches a bearer of its
own (a caller may still set `Authorization`, and nothing in the UI does), the
log download relies on `credentials: 'include'`, and the WebSocket dropped its
`?token=` query parameter — that put a credential in a URL for nothing, since
after a reload the token was gone and the cookie was already doing the work. The
server's bearer support is untouched for non-browser clients.

Two things turned up while pulling the thread:

- **The legacy-path check was backwards.** It decided "this is a Stremio
  token-in-URL request" from *the absence of a stored token*, so an admin
  reaching the UI through a reverse-proxy sub-path was read as a stream client
  whenever that copy was missing — and removing localStorage would have made
  that the only outcome. It now asks the server first and treats
  not-authenticated-under-a-prefix as the legacy case, which answers both
  correctly and needs nothing stored. The 401 path is handled too, since
  `apiFetch` rejects on a non-OK response.
- **`Settings` never accepted the `adminToken` prop** App.jsx was passing it.
  Dead prop, removed.

Upgrade note: a browser holding a token but no valid session cookie is logged
out once. The cookie is set at login with a seven-day lifetime, so this is the
rare case rather than the common one.

**Commit.** `fix(ui): let the session cookie be the only credential the browser holds`

**Files.** `frontend/src/api.js`, `frontend/src/App.jsx`,
`frontend/src/components/Login.jsx`, `frontend/src/components/ChangePassword.jsx`,
`frontend/src/components/LogsPage.jsx`

---

### 14. [x] Decide whether a throttled indexer should poison the slot

**Problem.** Two pieces of code disagree, and the second silently wins.
`recordFailedSlot` (`pkg/server/stremio/play_resolve.go`) skips
`SetSlotFailedDuringPlayback` when `isIndexerLimitErr` matches, with a comment
saying a quota error is temporary and must not poison the slot for retries after
the limit clears. Two lines later `reportBadReleaseOutcome` reaches
`purgeFailedRelease` (`handlers_playback.go:1464`), which calls
`SetSlotFailedDuringPlayback` unconditionally — including for failures it has
just decided are inconclusive.

Found while writing tests for item 8; it predates that work.

**The question is which rule is right,** and it is a product decision:

- If a quota error should leave the slot retryable, the guard in
  `recordFailedSlot` is correct and `purgeFailedRelease` needs the same
  condition — likely gated on its existing `durable` flag.
- If any failure should move the player along *now* while only conclusive ones
  earn a durable verdict, then `purgeFailedRelease` is correct and the guard in
  `recordFailedSlot` is dead weight that reads as a promise it cannot keep.

The second reading matches `purgeFailedRelease`'s own comment ("so failover
redirects immediately") and is probably the intended one, in which case the fix
is to delete the misleading guard rather than to add a condition.

**Done when.** One of the two is removed, and a test states the chosen rule.

**Files.** `pkg/server/stremio/play_resolve.go`,
`pkg/server/stremio/handlers_playback.go`

**Decided: the second reading. The guard is gone.** Marking the slot is about
moving the player along now, not about judging the release, and two things
already keep it from becoming a verdict — the mark expires with the session TTL
(`SetSlotFailedDuringPlayback` stamps `now + m.ttl`), and the durable
bad-release verdict is gated on `conclusiveBadRelease`, which already excludes
quota errors. So the concern the guard was written for — retrying once the
limit clears — was covered without it, while the guard itself read as a promise
the code never kept.

Both halves are now pinned: `TestRecordFailedSlotMarksThrottledSlotsToo` for the
marking, `TestThrottledIndexerEarnsNoDurableVerdict` for the part that actually
protects the release.

**Commit.** `fix(stremio): drop the throttle guard that never reached the slot`

---

### 15. [x] TTFF metric records the indexer in a field named ProviderName

**Problem.** The TTFF sample written on first byte
(`recordTTFF`, `pkg/server/stremio/play_serve.go`) fills
`metrics.PlaybackTTFFSample.ProviderName` with `rel.Indexer` — the indexer the
release came from, not the Usenet provider that served the bytes. Everywhere
else uses `providerNameFromHosts(providerHostsForOutcome(sess, success))` for
that field.

It came from a `providerName` variable in the old `handlePlay` that was
declared, read as a preferred value, and **never assigned** — so the fallback to
`rel.Indexer` was the only branch that ever ran. Item 8 removed the dead
variable and kept the behaviour, which is why the metric still reports what it
always has.

**Scope.** Decide whether the field wants the provider (use the same helper as
the other call site) or the indexer (rename it). Changing the value changes what
existing TTFF history means, so this is not a silent fix.

**Done when.** The field name and its contents agree.

**Files.** `pkg/server/stremio/play_serve.go`, `pkg/core/metrics`

**Done — the field keeps its name and gets the right value.** `recordTTFF` now
uses `providerNameFromHosts(providerHostsForOutcome(sess, true))`, the same
resolution as every other provider field, and is empty when nothing has served
yet rather than substituting an indexer name.

Renaming the field was the alternative and is the wrong one: the sample sits
next to `NNTPConnectDuration`, so a reader diagnosing a slow start-up was being
pointed at the wrong subsystem entirely. Blast radius is small — the samples are
a rolling window of 1,000 that self-corrects within days, and `recent_ttff` is
exposed by the API but not currently rendered anywhere in the UI.

**Commit.** `fix(metrics): record the serving provider in the TTFF sample`

---

## Not doing (decided)

- **TypeScript migration.** 18.6k lines of working JSX; the cost is not repaid
  at this project's size and rate of change. Revisit only if the frontend grows
  a second contributor.
- **Down-migrations for the DB schema.** The idempotent
  `CREATE IF NOT EXISTS` + `addedColumns` approach in
  `pkg/core/persistence/schema.go` fits a single-tenant self-hosted app.
