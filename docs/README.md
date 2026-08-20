# StreamNZB Documentation

Reference documentation for StreamNZB. For an overview and quickstart, see the [main README](../README.md).

## Setup & configuration

- [Configuration](configuration.md) — startup flags, data directory, and the full environment variable reference
- [Database backends](database.md) — SQLite (default), Postgres, and switching between them
- [Remote access](remote-access.md) — VPN and reverse proxy setups for streaming away from home
- [Provider speed test](speed-test.md) — measuring provider throughput and finding the right connection count
- [Easynews advanced search](easynews.md) — server-side filtering options for Easynews indexers

## Features

- [Metadata & catalogs](metadata.md) — StreamNZB as a full Stremio metadata provider
- [Stream model](stream-model.md) — global configuration vs. per-stream behavior
- [Filters & ranking](filters.md) — filter profiles: what gets rejected, how the rest is scored and ordered
- [Rules](rules.md) — named conditions over everything known about a release, including what ffprobe measured and what AvailNZB reports
- [Custom result formats](result-formatting.md) — reusable format profiles, helper reference, and the AIOStreams formatter import
- [Search requests](search-queries.md) — how indexer queries are built, executed and validated
- [Obfuscated releases](obfuscated-releases.md) — how releases with random-hash filenames are identified and played
- [NNTP proxy](nntp-proxy.md) — using StreamNZB as a local news server for SABnzbd/NZBGet
- [AvailNZB](availnzb.md) — community availability database integration
- [Using with AIOStreams](aiostreams.md) — adding StreamNZB as an AIOStreams preset

## Help

- [Troubleshooting](troubleshooting.md) — reporting problems, Cloudflare Tunnel buffering, admin password recovery
- [Backup & updates](backup-and-updates.md) — what to back up and how to update safely
