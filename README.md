# StreamNZB

[![Buy Me A Coffee](https://img.shields.io/badge/buy%20me%20a%20coffee-donate-yellow.svg)](https://buymeacoffee.com/gaisberg)
[![Discord](https://img.shields.io/badge/discord-join-7289DA.svg?logo=discord&logoColor=white)](https://snzb.stream/discord)

StreamNZB is a stream-based Usenet addon for Stremio clients (and optional integration with [AIOStreams](https://github.com/Viren070/AIOStreams)). It searches your configured indexers, filters and ranks releases using the [jhin](https://github.com/dreulavelle/jhin) parsing and ranking engine, checks availability via [AvailNZB](https://check.snzb.stream), and streams releases on-the-fly from your Usenet providers. One binary provides the addon UI, stream management, NNTP proxy, and playback pipeline behind a single IP. No extra containers, just your Usenet provider(s) and indexer(s).


## What it does

- **Standalone Stremio Addon** — Install StreamNZB directly into your Stremio client with built-in release parsing and ranking powered by [jhin](https://github.com/dreulavelle/jhin), customizable filter profiles, or optionally plug it into [AIOStreams](https://github.com/Viren070/AIOStreams).
- **Stream-based addon** — Define global providers, indexers, search queries, and filter profiles once, then create one or more streams that decide which resources belong to each stream manifest.
- **NNTP proxy** — Standard NNTP (default port 119) for SABnzbd or NZBGet. Shares the same provider pool as the addon.
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
   - If using a domain name, make sure it is reachable from your client. Example: `http://streamnzb.example.com:7000` or `https://streamnzb.example.com`
3. Go to **Settings → Providers** and add at least one Usenet provider (host, port, username, password, connections).
4. Go to **Settings → Indexers** and add at least one Newznab-compatible indexer (URL + API key).
5. Go to **Settings → Search** and configure your movie and/or TV search queries.
6. Go to **Settings → Filters** to configure custom release filter profiles and ranking rules.
7. Go to **Streams** and create a stream.
   - Choose which providers, indexers, search queries, and filter profiles belong to that stream.
   - Configure the stream's **General** options such as indexer mode, search query mode, results mode, failover, and AvailNZB behavior.
8. Click **Install** to add the manifest directly to your Stremio client (or copy the manifest URL for optional use in AIOStreams).

### Force password reset on next startup

If you need to force the admin account to land on the password-change screen after restart, set:

```env
ADMIN_FORCE_PASSWORD_RESET=true
```

After the password has been changed, remove or disable this env var.
When it remains enabled, StreamNZB will keep forcing the password-reset prompt on startup.


## Stream model

StreamNZB separates global configuration from per-stream behavior:

- **Settings → Providers** stores all Usenet providers globally.
- **Settings → Indexers** stores all supported indexers globally.
- **Settings → Search** stores reusable movie and TV search templates globally.
- **Settings → Filters** stores customizable release filtering and ranking profiles globally.
- **Streams** chooses which providers, indexers, search queries, and filter profiles are active for a specific manifest token.

Each stream also controls how its search pipeline behaves:

- **Indexers** — `Combine` or `Failover`
- **Search queries** — `Combine` or `First hit`
- **Results** — how the final stream list is returned
- **Failover** — whether playback should walk fallback slots internally
- **AvailNZB** — whether AvailNZB is allowed for that stream, in addition to the global setting

This makes it possible to run multiple different manifests from one StreamNZB instance, each with different search behavior and provider/indexer selection.


## Optional: Using with AIOStreams

StreamNZB works directly out-of-the-box with Stremio using its own built-in release parsing and filter profiles. If you use [AIOStreams](https://github.com/Viren070/AIOStreams), you can also add StreamNZB as an addon preset to consolidate streams alongside other addons.

**Setup:**

1. In StreamNZB, create or choose the stream you want AIOStreams to use.
2. Copy that stream's manifest URL (for example `https://your-host:7000/<token>/manifest.json`).
3. In AIOStreams, add the StreamNZB preset and paste the manifest URL.
4. **No Usenet service required in AIOStreams** — StreamNZB handles all Usenet provider connections, NZB fetching, and streaming internally. Skip the AIOStreams Usenet service configuration entirely.
5. Optionally configure additional filtering, sorting, or formatting rules in the AIOStreams UI if desired.


## AvailNZB

[AvailNZB](https://check.snzb.stream) is a community availability database. StreamNZB doesn't download or validate NZBs before showing results — it builds an ordered play list from indexer search plus AvailNZB (skipping releases already reported bad), then tries on play. Success/failure is reported so the shared DB stays current.

AvailNZB is controlled at two levels:

- **Global** in **Settings → Advanced**
- **Per stream** in **Streams → Add/Change → General**

AvailNZB is only used when both the global setting and the stream setting allow it.


## Troubleshooting

If you're stuck, please either open a [GitHub issue](https://github.com/Gaisberg/streamnzb/issues) or report it in the [Discord](https://snzb.stream/discord) `#help` channel (they sync via [GitThread](https://gitthreadsync.snzb.stream/)). Include downloaded logs when relevant, and include the copied bad match report from **History** when the issue is about a wrong or poor release match. Sensitive data should be automatically redacted but please double-check before posting.


## Support

If StreamNZB is useful to you, you can support development here:

**[Buy Me A Coffee](https://buymeacoffee.com/gaisberg)**


## Credits

- [dreulavelle](https://github.com/dreulavelle) for [jhin](https://github.com/dreulavelle/jhin) (the release parsing & ranking engine) and contributions.
- [javi11](https://github.com/javi11) for Go-based RAR and 7z streaming ([altmount](https://github.com/javi11/altmount)).
- [Augment](https://www.augmentcode.com/) for helping with the project.
