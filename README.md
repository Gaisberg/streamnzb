# StreamNZB

[![Buy Me A Coffee](https://img.shields.io/badge/buy%20me%20a%20coffee-donate-yellow.svg)](https://buymeacoffee.com/gaisberg)
[![Discord](https://img.shields.io/badge/discord-join-7289DA.svg?logo=discord&logoColor=white)](https://snzb.stream/discord)

StreamNZB is a stream-based Usenet addon for Stremio clients and [AIOStreams](https://github.com/Viren070/AIOStreams). It searches your configured indexers, checks availability via [AvailNZB](https://check.snzb.stream), and streams releases on-the-fly from your Usenet providers. One binary provides the addon UI, stream management, NNTP proxy, and playback pipeline behind a single IP. No extra containers, just your Usenet provider(s) and indexer(s).


## What it does

- **Stream-based addon** — Define global providers, indexers, and search requests once, then create one or more streams that decide which of those resources are used for a given manifest token.
- **Works with Stremio and AIOStreams** — Use StreamNZB directly as a Stremio-compatible addon, or plug it into [AIOStreams](https://github.com/Viren070/AIOStreams) and let AIOStreams do the final presentation and triage.
- **NNTP proxy** — Standard NNTP (default port 119) for SABnzbd or NZBGet. Same provider pool as the addon.
- **AvailNZB** — Community availability database. Bad releases are skipped; success/failure is reported on play so the shared DB stays current.
- **Single binary** — Docker image or native Windows/Linux/macOS. No other containers required.


## Release types we don't support

Streaming is done on-the-fly from archive segments. That only works when the inner file is stored uncompressed:

- **Compressed RAR** — RAR must be STORE (no compression). Compressed RAR releases will not play.
- **Compressed 7z** — Same idea: only uncompressed (copy/store) 7z content is streamable.


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
      - "119:119"
    volumes:
      - /path/to/config:/app/data
```

Or run the binary from the [releases](https://github.com/Gaisberg/streamnzb/releases) page (Windows, Linux, macOS). See `.env.example` for config via environment variables.

## Upgrade note

When updating from older device-based versions:

- Global configuration is kept.
- Providers and indexers are kept.
- Legacy device entries are intentionally reset and are **not** migrated to the new stream model.
- After updating, recreate your streams in the UI.

For Docker, keep your existing `/app/data` volume mounted so `config.json` and the rest of the persistent state survive the container update.

**First use:**

1. Open `http://localhost:7000`. Default login is `admin` / `admin`; you'll be asked to change the password.
2. Go to **Settings → Network** and set your addon **Base URL** and **Port**.
  - If using Tailscale, use the IP address of the machine running StreamNZB. Example: `http://100.64.0.1:7000`
  - If using a domain name, make sure it is reachable from the client or AIOStreams host. Example: `http://streamnzb.example.com:7000` or `https://streamnzb.example.com`
3. Go to **Settings → Providers** and add at least one Usenet provider (host, port, username, password, connections).
4. Go to **Settings → Indexers** and add at least one Newznab-compatible indexer (URL + API key).
5. Go to **Settings → Search Requests** and create at least one movie and/or TV request.
6. Go to **Streams** and create a stream.
  - Choose which providers, indexers, and search requests belong to that stream.
  - Configure the stream's **General** options such as indexer mode, search request mode, results mode, failover, and AvailNZB behavior.
7. Save the stream and copy its manifest URL from the install action or stream list.
8. Add that manifest URL to your Stremio client or [AIOStreams](https://github.com/Viren070/AIOStreams).


## Stream model

StreamNZB now separates global configuration from per-stream behavior:

- **Settings → Providers** stores all Usenet providers globally.
- **Settings → Indexers** stores all supported indexers globally.
- **Settings → Search Requests** stores reusable movie and TV search templates globally.
- **Streams** chooses which providers, indexers, and search requests are active for a specific manifest token.

Each stream also controls how its search pipeline behaves:

- **Indexers** — `Combine` or `Failover`
- **Search requests** — `Combine` or `First hit`
- **Results** — how the final stream list is returned
- **Failover** — whether playback should walk fallback slots internally
- **AvailNZB** — whether AvailNZB is allowed for that stream, in addition to the global setting

This makes it possible to run multiple different manifests from one StreamNZB instance, each with different search behavior and provider/indexer selection.


## Using with AIOStreams

[AIOStreams](https://github.com/Viren070/AIOStreams) is THE way to use StreamNZB. It consolidates multiple Stremio addons into a single super-addon with advanced filtering, sorting, and formatting — all configured in one place.

**Setup:**

1. In StreamNZB, create or choose the stream you want AIOStreams to use.
2. Copy that stream's manifest URL (for example `https://your-host:7000/<token>/manifest.json`).
3. In AIOStreams, add the StreamNZB preset and paste the manifest URL.
4. **No Usenet service required in AIOStreams** — StreamNZB handles all Usenet provider connections, NZB fetching, and streaming internally. Skip the AIOStreams Usenet service configuration entirely.
5. Configure your filtering, sorting, and stream formatting rules in the AIOStreams UI. AIOStreams will aggregate StreamNZB results alongside any other addons you use and apply your rules uniformly.

If you want an AIO-oriented stream, create a dedicated stream for it and configure that stream accordingly. Stream behavior is no longer driven by User-Agent detection.


## AvailNZB

[AvailNZB](https://check.snzb.stream) is a community availability database. StreamNZB doesn't download or validate NZBs before showing results — it builds an ordered play list from indexer search plus AvailNZB (skipping releases already reported bad), then tries on play. Success/failure is reported so the shared DB stays current.

AvailNZB is controlled at two levels:

- **Global** in **Settings → Advanced**
- **Per stream** in **Streams → Add/Change → General**

AvailNZB is only used when both the global setting and the stream setting allow it.


## Troubleshooting

If you're stuck, please either open a [GitHub issue](https://github.com/Gaisberg/streamnzb/issues) or report it in the [Discord](https://snzb.stream/discord) `#help` channel (they sync via [GitThread](https://gitthreadsync.snzb.stream/)). Include downloaded logs when relevant, and include the copied bad match report from **NZB History** when the issue is about a wrong or poor release match. Sensitive data should be automatically redacted but please double-check before posting.


## Support

If StreamNZB is useful to you, you can support development here:

**[Buy Me A Coffee](https://buymeacoffee.com/gaisberg)**


## Credits

- [javi11](https://github.com/javi11) for Go-based RAR and 7z streaming ([altmount](https://github.com/javi11/altmount)).
- [Augment](https://www.augmentcode.com/) for helping with the project.
