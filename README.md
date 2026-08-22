# StreamNZB

[![Buy Me A Coffee](https://img.shields.io/badge/buy%20me%20a%20coffee-donate-yellow.svg)](https://buymeacoffee.com/gaisberg)
[![Discord](https://img.shields.io/badge/discord-join-7289DA.svg?logo=discord&logoColor=white)](https://snzb.stream/discord)

**StreamNZB is the one place you configure Usenet.** Providers, indexers, filter and ranking rules, search requests and metadata all live in a single binary — and everything else consumes that configuration. Set a provider up once and every app you point at StreamNZB gets it, with your credentials and API keys staying on the server.

The playback engine underneath streams releases on-the-fly from your providers, recovers obfuscated releases from their own bytes, and repairs a missing archive volume from PAR2 rather than giving up on the release.


## Consume it from

| From | What it gets | Docs |
|---|---|---|
| **Stremio** | A complete addon: catalogs, search, title pages, air dates and streams. No Cinemeta, no companion addons | [Metadata & catalogs](docs/metadata.md) |
| **Prowlarr, Sonarr, Radarr** | Every indexer you configured, as one Newznab API behind one key | [Newznab endpoint](docs/newznab.md) |
| **SABnzbd, NZBGet** | Your whole provider pool, as one NNTP server | [NNTP proxy](docs/nntp-proxy.md) |
| **AIOStreams** | StreamNZB as a preset, alongside what it already runs | [Using with AIOStreams](docs/aiostreams.md) |

One config, one IP, no extra containers — just your Usenet provider(s) and indexer(s). See [Integrations](docs/integrations.md) for the whole picture in one page.


## What it does

- **Configure once, use everywhere** — Define global providers, indexers, search queries and filter profiles once, then create one or more streams that decide which of those resources belong to each manifest. The same configuration backs the Newznab endpoint and the NNTP proxy, so adding a provider or swapping an indexer is one edit, not one per app.
- **The only addon you need** — StreamNZB serves catalogs and full metadata alongside streams: trending/popular/top-rated rows, search, series pages with episode lists and exact air dates, plus per-stream **Continue Watching** and **Because You Watched** rows built from your own playback history. Works out of the box in any Stremio-compatible client, including ones without Cinemeta.
- **Standalone Stremio Addon** — Install StreamNZB directly into your Stremio client with built-in release parsing and ranking powered by [jhin](https://github.com/dreulavelle/jhin), customizable filter profiles, or optionally plug it into [AIOStreams](https://github.com/Viren070/AIOStreams).
- **Newznab endpoint** — Every configured indexer re-served as a single Newznab API (off by default), so Prowlarr, Sonarr, Radarr or any Newznab client searches the whole set through one URL and one key. Your indexers' own API keys are never handed to the client.
- **NNTP proxy** — Standard NNTP (default port 1119, off by default) for SABnzbd or NZBGet. Shares the same provider pool as the addon.
- **AvailNZB** — Community availability database, opt-in. Bad releases are skipped; success/failure is reported on play so the shared DB stays current. Off by default, and nothing is sent until you enable it.
- **Search history & diagnostics** — The **History** page shows every search with its play attempts nested under it: per-indexer API timings, what validation/dedup/filtering dropped and why, including searches that returned nothing. An optional **Search debug stream** toggle (under Advanced) prepends the same summary as a result row in Stremio — selecting it just plays the top real result.
- **SQLite or Postgres** — A local file by default, or point it at an existing Postgres server. Switching migrates your data either way, without a restart.
- **Single binary** — Docker image or native Windows/Linux/macOS. No other containers required.


## What can and cannot be streamed

Playing from an archive without downloading it first means seeking to an arbitrary byte inside it. That is possible when the inner file is **stored** (no compression) and impossible when it is compressed, because reaching byte *n* of a compressed stream means decoding everything before it. This is a property of the formats, not of StreamNZB — Stremio's own addon SDK documents its native 7zip support as *"supports seeking only when compression is not used"*, and every streaming implementation lands in the same place.

**Plays:**

- **RAR in STORE mode**, RAR4 and RAR5, including multi-volume sets — which is how scene and P2P releases are packed.
- **7z with copy/store content.**
- **Encrypted RAR and 7z**, where the NZB carries the password — AES sets are decrypted as they stream.
- **Obfuscated releases**, where the filenames were stripped before upload. Names are recovered from the release's own bytes — see [Obfuscated releases](docs/obfuscated-releases.md).
- **Damaged releases, up to a point.** Isolated missing articles are zero-filled and playback continues; a missing first RAR volume is repaired from PAR2 recovery data rather than failing the release. See [Troubleshooting](docs/troubleshooting.md#playback-glitches-or-drops-on-a-damaged-release).

**Does not play:**

- **Compressed RAR or 7z.** Not streamable by anyone, per the above. Such a release has to be downloaded and unpacked — point [SABnzbd or NZBGet at the NNTP proxy](docs/nntp-proxy.md) if you want that from the same provider pool.

### Why run the engine at all

Stremio v5 can stream NZBs natively on desktop, with Android TV in beta. Two reasons to keep playback on the server anyway:

- **Every other client.** Web, iOS, Android mobile and TV platforms have no native path; StreamNZB serves all of them the same way.
- **Your provider credentials stay put.** The native path passes NNTP server strings — username and password included — to the client in the stream object. Through StreamNZB they never leave the server, and playback comes from one IP.


## Quickstart

**Docker (recommended):**

```yaml
services:
  streamnzb:
    image: ghcr.io/gaisberg/streamnzb:latest
    container_name: streamnzb
    restart: unless-stopped
    ports:
      - "7000:7000"
      - "119:119"
    volumes:
      - /path/to/config:/app/data
```

Or run the binary from the [releases](https://github.com/Gaisberg/streamnzb/releases) page (Windows, Linux, macOS). See [Configuration](docs/configuration.md) for startup flags and config via environment variables.

### Setup Guide

1. Open `http://localhost:7000`. Default login is `admin` / `admin`; you'll be asked to change the password.
2. Go to **General** (under Settings) and set your addon **Base URL** and **Port**.
   - If using Tailscale, use the IP address of the machine running StreamNZB. Example: `http://100.64.0.1:7000`
   - If using a domain name, make sure it is reachable from your client. Example: `http://streamnzb.example.com:7000` or `https://streamnzb.example.com`
   - Changing the port takes effect immediately, without a restart — but it closes the connection this page is served over, so reopen StreamNZB on the new port afterwards.
3. Go to **Providers** and add at least one Usenet provider (host, port, username, password, connections).
4. Go to **Indexers** and add at least one Newznab-compatible indexer (URL + API key).
5. Go to **Filters** to configure release filtering profiles and ranking rules.
6. Go to **Search** and configure your movie and/or TV search queries.
7. Optionally go to **Metadata** to curate the catalogs your clients see (trending rows are on by default) — see [Metadata & Catalogs](docs/metadata.md).
8. Go to **Streams** and click **Add Stream** to create a stream manifest.
   - Select which providers, indexers, search queries, and filter profiles belong to this stream.
   - Configure stream options such as indexer mode, search query mode, results mode, failover, and AvailNZB behavior.
9. Click **Install** on your stream to add the manifest directly to your Stremio client (or copy the manifest URL for optional use in AIOStreams).


## Documentation

Full reference documentation lives in the [docs](docs/README.md) folder:

- [Configuration](docs/configuration.md) — startup flags, data directory, and the full environment variable reference
- [Database backends](docs/database.md) — SQLite (default), Postgres, and switching between them
- [Remote access](docs/remote-access.md) — VPN and reverse proxy setups for streaming away from home
- [Providers](docs/providers.md) — priority, failover, and holding a metered account back as a backup
- [Provider speed test](docs/speed-test.md) — measuring provider throughput and finding the right connection count
- [Easynews advanced search](docs/easynews.md) — server-side filtering options for Easynews indexers
- [Metadata & catalogs](docs/metadata.md) — StreamNZB as a full Stremio metadata provider
- [Stream model](docs/stream-model.md) — global configuration vs. per-stream behavior
- [Filters & ranking](docs/filters.md) — filter profiles: what gets rejected, how the rest is scored and ordered
- [Rules](docs/rules.md) — named conditions over everything known about a release
- [Custom result formats](docs/result-formatting.md) — per-stream result templates, helper reference, and the AIOStreams formatter import
- [Search requests](docs/search-queries.md) — how indexer queries are built, executed and validated
- [Obfuscated releases](docs/obfuscated-releases.md) — how releases with random-hash filenames are identified and played
- [Integrations](docs/integrations.md) — one configuration serving Stremio, Prowlarr/*arr and your download client
- [NNTP proxy](docs/nntp-proxy.md) — using StreamNZB as a local news server for SABnzbd/NZBGet
- [Newznab endpoint](docs/newznab.md) — serving your configured indexers to any Newznab-compatible application
- [AvailNZB](docs/availnzb.md) — community availability database integration
- [Using with AIOStreams](docs/aiostreams.md) — what StreamNZB adds alongside it, and how to add it as a preset
- [Troubleshooting](docs/troubleshooting.md) — reporting problems, Cloudflare Tunnel buffering, admin password recovery
- [Backup & updates](docs/backup-and-updates.md) — what to back up and how to update safely


## Troubleshooting

If you're stuck, please either open a [GitHub issue](https://github.com/Gaisberg/streamnzb/issues) or report it in the [Discord](https://snzb.stream/discord) `#help` channel (they sync via [GitThread](https://gitthreadsync.snzb.stream/)). See the [troubleshooting guide](docs/troubleshooting.md) for what to include and common issues.


## Support

If StreamNZB is useful to you, you can support development here:

**[Buy Me A Coffee](https://buymeacoffee.com/gaisberg)**


## Credits

- [dreulavelle](https://github.com/dreulavelle) for [jhin](https://github.com/dreulavelle/jhin) (the release parsing & ranking engine) and contributions.
- [javi11](https://github.com/javi11) for Go-based RAR and 7z streaming ([altmount](https://github.com/javi11/altmount)).
