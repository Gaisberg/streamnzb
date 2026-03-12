# StreamNZB

[![Buy Me A Coffee](https://img.shields.io/badge/buy%20me%20a%20coffee-donate-yellow.svg)](https://buymeacoffee.com/gaisberg)
[![Discord](https://img.shields.io/badge/discord-join-7289DA.svg?logo=discord&logoColor=white)](https://snzb.stream/discord)

StreamNZB is an API-first Stremio/Nuvio addon runtime that streams from Usenet via your indexers. The canonical `streamnzb` build now targets the Next discovery/playback service under `pkg/next/*`, exposing the addon manifest plus `/stream`, `/resolve`, and `/play`. The old bundled frontend/admin UI is no longer part of the default build.


## What it does

- **Stremio & Nuvio addon** – Add the manifest URL in [Stremio](https://www.stremio.com) or [Nuvio](https://nuvioapp.space). Open a title and you get one row per **stream config** (e.g. “Global”, “1080p”). Each row shows “StreamNZB [availNZB]” when the top release is known good, or “X possible releases”. Play uses that stream’s ordered list; if playback fails we report to AvailNZB and try the next release.
- **Discovery + playback API** – `/stream` returns candidate NZB-backed streams, `/resolve` performs playback preflight, and `/play` starts or resumes the prepared playback session.
- **AvailNZB** – Reuse others’ availability checks and report your own so the shared DB stays useful. Bad releases are skipped when building play lists; good/bad is reported on play.
- **Single binary** – Docker image or native Windows/Linux/macOS binary with the default HTTP listen port on `7000`.


## Rewrite status: playback / unpacking

The shipped `streamnzb` / `streamnzb.exe` binary now builds from `cmd/streamnzb-next`. The runtime is still not at full legacy parity yet, but this README now refers to that Next API-first service rather than the old bundled frontend/admin flow.

What the rewrite already covers:

- selected-NZB playback via `POST /api/v1/service/play` and `GET /play/{sessionID}`
- deferred session creation and lazy NZB download on play
- normalized download URLs and indexer API key injection
- episode-aware media selection via `unpack.GetMediaStreamForEpisode(...)`
- basic HTTP streaming and bytes-read tracking
- basic AvailNZB playback success/failure reporting

What still only exists in the legacy playback path today:

- startup probing / startup metadata caching
- startup timeout handling and expected-stream reopening
- `t=` time-offset handling and more nuanced `HEAD` / range behavior
- probe-like request classification to avoid false playback reports
- first-segment availability checks and richer corruption/failure classification
- fallback-to-next-release orchestration and related session state
- attempt recording / failure persistence / dedupe around playback

The migration plan for this area is to:

1. harden single-stream playback reliability in the rewrite
2. verify real client `HEAD` / range / `t=` behavior
3. explicitly decide which legacy fallback behavior should **not** be ported
4. validate the result with real-client smoke tests before retiring legacy paths


## Release types we don’t support

Streaming is done on-the-fly from archive segments. That only works when the inner file is stored uncompressed:

- **Compressed RAR** – RAR must be STORE (no compression). Compressed RAR releases will not play.
- **Compressed 7z** – Same idea: only uncompressed (copy/store) 7z content is streamable.


## Run it

**Docker (recommended):**

```yaml
services:
  streamnzb:
    image: ghcr.io/gaisberg/streamnzb:latest
    container_name: streamnzb
    restart: unless-stopped
    ports:
      - "7000:7000"
    volumes:
      - /path/to/config:/app/data
```

Or run the binary from the [releases](https://github.com/Gaisberg/streamnzb/releases) page (Windows, Linux, macOS). See `.env.example` for config via environment variables.

**First use:** Configure providers/indexers in `config.json` or via environment variables, start the service, then point your client at `http://localhost:7000/manifest.json`. The current shipped runtime is API-first and does not include the old admin frontend.


## AvailNZB

[AvailNZB](https://check.snzb.stream) is a community availability database. We don’t download or validate NZBs before showing results—we build an ordered play list from indexer search plus AvailNZB (skipping releases already reported bad), then try on play. StreamNZB reports success/failure so the shared DB stays current. Official builds can utilize the project’s AvailNZB instance, and the URL/API key can be configured through env/config.


## Troubleshooting

If you’re stuck, please either open a [GitHub issue](https://github.com/Gaisberg/streamnzb/issues) or report it in the [Discord](https://snzb.stream/discord) `#help` channel (they sync via [GitThread](https://gitthreadsync.snzb.stream/)). Include downloaded logs from **Settings → Logs** when relevant, and include the copied bad match report from **NZB History** when the issue is about a wrong or poor release match. Sensitive data should be automatically redacted but please double-check before posting. 


## Support

If StreamNZB is useful to you, you can support development here:

**[Buy Me A Coffee](https://buymeacoffee.com/gaisberg)**


## Credits

- [javi11](https://github.com/javi11) for Go-based RAR and 7z streaming ([altmount](https://github.com/javi11/altmount)).
- [Augment](https://www.augmentcode.com/) for helping with the project.
