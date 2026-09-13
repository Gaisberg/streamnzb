# Contributing to StreamNZB

Thanks for helping out. This page covers what a pull request needs to be merged.
For a deeper guide to the codebase — package layering, where new logic belongs,
concurrency rules, and the shared helpers to reuse — see
[`.claude/skills/streamnzb-dev/SKILL.md`](.claude/skills/streamnzb-dev/SKILL.md).
It is written for AI coding assistants but applies to humans just the same.

## Before you start

- **Bugs and small fixes**: open a pull request directly. A clear description
  of the failure and a test that reproduces it are worth more than a long
  explanation.
- **Features and behavior changes**: open an
  [issue](https://github.com/Gaisberg/streamnzb/issues) or ask in the
  [Discord](https://snzb.stream/discord) first. StreamNZB has some deliberate
  design boundaries (see below), and a quick check saves rewriting a large diff.
- **Dependency bumps**: Dependabot handles these on a weekly schedule. Don't
  open manual bump pull requests unless something is broken.

## Setup

You need **Go 1.25+** and **Node.js 22+**.

```bash
git clone https://github.com/Gaisberg/streamnzb.git
cd streamnzb
cd frontend && npm install && cd ..
bash build.sh        # build.bat on Windows without a Bash shell
```

`build.sh` is the whole verification pipeline: `go fmt`, `go vet`, the Go test
suite, ESLint, Vitest, the Vite production build, and the final `go build`. It
ends with `Build Complete!` when everything passed. CI runs the same steps, plus
`go test -race`.

While iterating, targeted commands are fine:

```bash
go test ./pkg/session -run TestName -v         # one Go test
cd frontend && npx vitest run src/lib/streams.test.js   # one frontend test
cd frontend && npm run dev                     # frontend with hot reload
```

They never replace a final `build.sh` run before you push.

`-race` needs cgo. If your machine has no C toolchain, reproduce a CI race
failure in a container:

```bash
docker run --rm -v "$PWD:/src" -w /src golang:1.25 sh -c \
  'rm -f /.dockerenv && mkdir -p pkg/server/web/static && touch pkg/server/web/static/.gitkeep && go test -race ./...'
```

Local builds run without an embedded ffprobe and fall back to one on `PATH` or
the runtime auto-downloader. You don't need `go run ./tools/fetch-ffprobe`
unless you're working on the embedding itself.

## Pull request requirements

A pull request is ready for review when all of these hold:

1. **`bash build.sh` is green** on your machine, and CI is green on the PR.
2. **The title is a Conventional Commit.** Pull requests are squash-merged and
   the title becomes the commit message, which
   [release-please](https://github.com/googleapis/release-please) reads to
   derive the next version and the changelog. Use `feat:`, `fix:`, `perf:`,
   `refactor:`, `docs:`, `test:`, `chore:`, with an optional scope:
   `fix(stremio): ...`, `feat(jellyfin): ...`. A breaking change needs `!`
   after the type/scope. The title should describe the behavior change, not
   the code change (`fix(loader): stop zero-filling past the last segment`,
   not `fix: update loader.go`).
3. **One change per pull request.** A refactor that enables a fix is fine in
   the same PR if it is small and mentioned; a second unrelated fix is not.
   Drive-by formatting of files you didn't otherwise touch makes review harder.
4. **New behavior has a test.** Bug fixes should include a test that failed
   before the fix. Go tests sit next to the code (`foo_test.go`); frontend
   tests sit next to the module (`foo.test.js` / `foo.test.jsx`).
5. **User-facing changes update `docs/`.** A new setting, env var, feature, or
   troubleshooting scenario is documented on the matching page under `docs/`
   in the same PR. A new page must also be linked from both `docs/README.md`
   and the README's Documentation list. Don't add reference material to the
   README itself; it is the landing page only.
6. **No build artifacts.** `streamnzb`, `streamnzb.exe`, `*.log`,
   `streamnzb.db*`, `frontend/dist/`, `pkg/server/web/static/`, `config.json`
   and the ffprobe binaries are all gitignored. Check `git status` before
   committing a large change.
7. **Reuse before writing.** Search for an existing helper, component, or hook
   before adding one, and extend it rather than adding a parallel copy. The
   skill has a table of the helpers most often re-implemented by accident.

The PR template will remind you of these.

## Where code goes

Packages are layered, and imports only ever point downward:

```
cmd/streamnzb                                 composition root: flags, config, wiring
  ↓
pkg/server/{stremio,api,jellyfin,newznab,web} HTTP only — parse, call a service, respond
  ↓
pkg/playback  pkg/search  pkg/session          orchestration + domain logic
  ↓
pkg/media/*  pkg/indexer/*  pkg/services/*      media plans, indexer clients, external APIs
  ↓
pkg/usenet/*  pkg/core/*  pkg/release           NNTP, config, persistence, logging
```

A handler should read as *resolve inputs → call one service → format the
result*. If a change seems to need an upward import, or a handler that
downloads, decodes or retries, the logic is in the wrong layer. The skill has a
table mapping the kind of change to the package it belongs in, plus the
boundaries that must not be crossed (`loader` never imports `unpack`,
`search/query` is pure, only `usenet/pool` fetches playback segments).

## Design principles that shape reviews

These are the things most likely to send a PR back, so they're worth knowing
up front:

- **Client codec responsibility.** Never reorder, hide, or gate streams by
  client User-Agent to work around a codec incompatibility. Surface capability
  metadata client-agnostically; the client decides what it can play.
- **Fail-open on inconclusive checks.** Only definitive evidence (a 430 on
  every provider, corrupt data) may poison a release or slot. Timeouts and
  cancellations must never blacklist anything.
- **Validation is layered.** Article existence → STAT sweeps; archive
  structure → RAR scan / blueprint; decodability → ffprobe; sustained
  playability → playback with failover. Use the right layer for the question.
- **Never hold a lock across IO.** Detach the resource under the lock, act
  outside it. Lock ordering is `Manager.mu` → `Session.mu`, never inverted.
- **Portable SQL in stores.** Persistence supports SQLite and Postgres through
  one schema; write dialect-neutral DDL and `?` placeholders, and add columns
  through the migration table rather than editing `CREATE TABLE`.

## Review

Every PR gets an automated review from CodeRabbit and a maintainer review.
Address or reply to each comment; "fixed in abc123" is enough. Force-pushing
after review is fine — the PR is squash-merged anyway — but a follow-up commit
is easier to re-review.

## Reporting bugs

See the [troubleshooting guide](docs/troubleshooting.md) for what a useful
report includes, then open an issue or post in the Discord `#help` channel
(the two sync).

## License

By contributing you agree that your contributions are licensed under the
project's [license](LICENSE).
