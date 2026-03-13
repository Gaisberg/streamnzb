# StreamNZB

[![Buy Me A Coffee](https://img.shields.io/badge/buy%20me%20a%20coffee-donate-yellow.svg)](https://buymeacoffee.com/gaisberg)
[![Discord](https://img.shields.io/badge/discord-join-7289DA.svg?logo=discord&logoColor=white)](https://snzb.stream/discord)

StreamNZB is an API-first Stremio/Nuvio addon that streams media from Usenet. Point it at your indexers and providers, install the addon in your client, and play — no intermediate downloads, no web UI, just a single binary.


## What it does

- **AIOStreams service and preset** – Install the manifest URL in [AIOStreams](https://github.com/Viren070/AIOStreams). Playback streams directly from Usenet segments on the fly.
- **AvailNZB integration** – Leverages the community [AvailNZB](https://check.snzb.stream) availability database to prioritise known-good releases and skip known-bad ones. Playback results are reported back so the shared DB stays current.
- **NNTP proxy** – Optionally exposes your provider pool as a local NNTP server for other tools like SABnzbd.
- **Single binary** – Docker image or native Windows / Linux / macOS binary. Default listen port `7000`.


## Release types we don't support

Streaming is done on-the-fly from archive segments. That only works when the inner file is stored uncompressed:

- **Compressed RAR** – RAR must be STORE (no compression). Compressed RAR releases will not play.
- **Compressed 7z** – Same idea: only uncompressed (copy/store) 7z content is streamable.


## Getting started

### 1. Configure

All user configuration lives in a single **`.env`** file (or real environment variables). Copy `.env.example` to `.env` and fill in your providers and indexers:

```env
# Usenet provider
PROVIDER_1_HOST=news.example.com
PROVIDER_1_USERNAME=myuser
PROVIDER_1_PASSWORD=mypassword

# Newznab indexer
INDEXER_1_URL=https://api.nzbindexer.com
INDEXER_1_API_KEY=your-key
```

See [`.env.example`](.env.example) for all available settings (port, base URL, log level, NNTP proxy, memory limit, etc.).

### 2. Run

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
      - /path/to/data:/app/data
      - /path/to/.env:/app/.env
```

**Binary:**

Download from [Releases](https://github.com/Gaisberg/streamnzb/releases), place a `.env` next to it, and run.

### 3. Install the addon

On first start the log will print a manifest URL containing your admin token:

```
Manifest URL  url=http://localhost:7000/<token>/manifest.json
```

Add that URL in Stremio or Nuvio. The token is auto-generated and persisted — it survives restarts.


## Configuration model

| File | Purpose | Managed by |
|------|---------|------------|
| `.env` | User settings — providers, indexers, ports, log level, AvailNZB mode | You |
| `streamnzb.dat` | Internal runtime state — admin token, TVDB token, AvailNZB key & recovery secret | StreamNZB (auto-generated) |

There is no `config.json` or database. Everything the app needs to persist internally goes into `streamnzb.dat` (a small JSON file in the data directory). You should never need to edit it manually.


## AvailNZB

[AvailNZB](https://check.snzb.stream) is a community availability database. StreamNZB builds an ordered play list from indexer results combined with AvailNZB status — known-bad releases are skipped, known-good releases are prioritised. On playback, success or failure is reported back so the shared DB stays accurate.

AvailNZB auto-registers on first run and stores the API key and recovery secret in `streamnzb.dat`. You can override the key via `AVAILNZB_API_KEY` in `.env`, or control the mode with `AVAILNZB_MODE` (`full`, `status_only`, or `disabled`).


## Troubleshooting

If you're stuck, please either open a [GitHub issue](https://github.com/Gaisberg/streamnzb/issues) or ask in the [Discord](https://snzb.stream/discord) `#help` channel. Include log output when relevant — logs are written to `streamnzb.log` in the data directory and rotated on startup (controlled by `KEEP_LOG_FILES`, default 9). Sensitive data is automatically redacted but please double-check before posting.


## Support

If StreamNZB is useful to you, you can support development here:

**[Buy Me A Coffee](https://buymeacoffee.com/gaisberg)**


## Credits

- [javi11](https://github.com/javi11) for Go-based RAR and 7z streaming ([altmount](https://github.com/javi11/altmount)).
- [Augment](https://www.augmentcode.com/) for helping with the project.
