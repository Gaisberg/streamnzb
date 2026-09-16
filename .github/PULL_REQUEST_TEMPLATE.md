<!--
The PR title becomes the squash commit message and feeds release-please, so it
must be a Conventional Commit describing the behavior change:
  fix(loader): stop zero-filling past the last segment
  feat(jellyfin): expose season art in the items endpoint
See CONTRIBUTING.md for the full requirements.
-->

## What changed and why

<!-- What was broken or missing, and what this does about it. Link the issue as `Refs #123` — not `Fixes`/`Closes`, which would close it before the fix is released. -->

## How it was verified

<!-- Tests added or changed, and anything you checked by hand (which client, which release, what you saw in streamnzb.log). -->

## Checklist

- [ ] `bash build.sh` (or `build.bat`) finishes with `Build Complete!`
- [ ] Title is a Conventional Commit (`type(scope): behavior change`)
- [ ] Issues referenced with `Refs #N`, not `Closes`/`Fixes`
- [ ] One change per PR; no unrelated formatting or drive-by edits
- [ ] New behavior has a test; a bug fix has a test that failed before the fix
- [ ] User-facing changes are documented under `docs/` (and a new page is linked from `docs/README.md` and the README)
- [ ] No build artifacts staged (`streamnzb`, `*.log`, `streamnzb.db*`, `config.json`, `frontend/dist/`, `pkg/server/web/static/`)
