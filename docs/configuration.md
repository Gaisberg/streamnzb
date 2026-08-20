# Configuration: startup, flags & environment variables

Most settings are configured in the web UI and stored in `config.json` in the data directory. Environment variables exist for deployments (Docker, NAS, provisioning) where you want configuration to live outside the app — anything set via env **overrides and owns** the corresponding setting: the value from the environment wins on every load, so changing that field in the UI will not stick while the variable is set.

## Starting StreamNZB

```
streamnzb [-config <path>] [-log-file <path>]
```

| Flag | Meaning |
|---|---|
| `-config <path>`, `-c <path>` | Path to `config.json`, or a directory to use as the data directory. |
| `-log-file <path>` | Path to the log file, or a directory to write `streamnzb.log` into. Outranks `LOG_PATH`. |

A `.env` file in the working directory is loaded automatically at startup, so you can keep env-based config next to the binary instead of exporting variables. In Docker, use the `environment:` block of your compose file instead.

### Data directory

The data directory holds `config.json`, `streamnzb.db`, logs, and runtime state. It is resolved in this order:

1. The `-config` flag (a file pins its parent directory; a directory is used as-is)
2. The `CONFIG_PATH` environment variable (same file/directory rule)
3. `/app/data` when running inside Docker
4. The working directory, if it already contains a `config.json`
5. On Windows: `%LOCALAPPDATA%\streamnzb`
6. Otherwise the working directory

## Environment variable reference

Booleans accept `true`/`1` (and `false`/`0` where a variable can force a setting off); unparsable values are treated as unset rather than flipping a default. Variables listed as `STREAMNZB_*` with a legacy alias accept either name, with the `STREAMNZB_`-prefixed one winning.

### Core

| Variable | Meaning |
|---|---|
| `ADDON_PORT` | HTTP port for the addon UI and streams (default 7000) |
| `ADDON_BASE_URL` | Public base URL clients use to reach this instance |
| `CONFIG_PATH` | Data directory (or path to `config.json`) — see above |
| `LOG_LEVEL` | Log level (default `INFO`) |
| `STREAMNZB_LOG_PATH` (legacy `LOG_PATH`) | Log file path, or a directory for `streamnzb.log`. Deployment-only: not in `config.json` or the UI |
| `KEEP_LOG_FILES` | How many rotated log files to keep (minimum 1) |
| `TZ` | Time zone |

### Admin account

| Variable | Meaning |
|---|---|
| `ADMIN_USERNAME` | Admin login name |
| `ADMIN_FORCE_PASSWORD_RESET` | `true` forces the password-change screen on next startup — see [Troubleshooting](troubleshooting.md#force-password-reset-on-next-startup). Remove it once the password is changed |

### Database

See [Database backends](database.md) for how switching and migration work.

| Variable | Meaning |
|---|---|
| `STREAMNZB_DATABASE_DRIVER` (legacy `DATABASE_DRIVER`) | `sqlite` (default) or `postgres` |
| `STREAMNZB_DATABASE_URL` (legacy `DATABASE_URL`) | Postgres connection string |

### Metadata & AvailNZB

| Variable | Meaning |
|---|---|
| `METADATA_ENABLED` | Master switch for the built-in metadata provider — see [Metadata & Catalogs](metadata.md) |
| `TMDB_API_KEY` | Your own TMDB key (otherwise the built-in fallback key is used) |
| `TVDB_API_KEY` | Your own TVDB key (otherwise the built-in fallback key is used) |
| `TVDB_SUBSCRIBER_PIN` | Subscriber PIN for a user-supported TVDB key — required by the keys TheTVDB issues to individuals |
| `AVAILNZB_URL` | AvailNZB server URL — see [AvailNZB](availnzb.md) |
| `AVAILNZB_API_KEY` | AvailNZB API key |

### NNTP proxy

| Variable | Meaning |
|---|---|
| `NNTP_PROXY_ENABLED` | Enable/disable the built-in NNTP proxy (default off) |
| `NNTP_PROXY_HOST` | Listen host |
| `NNTP_PROXY_PORT` | Listen port (default 1119 — unprivileged, so it binds without root) |
| `NNTP_PROXY_AUTH_USER` | Username downstream clients (SABnzbd, NZBGet) authenticate with |
| `NNTP_PROXY_AUTH_PASS` | Password for the same |

### Outbound User-Agent headers

| Variable | Meaning |
|---|---|
| `STREAMNZB_INDEXER_QUERY_HEADER` (legacy `INDEXER_QUERY_HEADER`) | User-Agent sent on indexer search requests |
| `STREAMNZB_INDEXER_GRAB_HEADER` (legacy `INDEXER_GRAB_HEADER`) | User-Agent sent on NZB downloads |
| `STREAMNZB_PROVIDER_HEADER` (legacy `PROVIDER_HEADER`) | Identification sent to Usenet providers |

Indexers increasingly gate content on the client version, so a header pinned to
whatever was current when it was typed eventually starts being rejected.
**Update to latest** on the Settings → General User-Agent card looks up what
each of these tools is on today and lifts the headers to it:

| Tool | Version source |
|---|---|
| Prowlarr, Sonarr, Radarr | Latest stable GitHub release (develop builds are published as prereleases and are skipped) |
| SABnzbd, NZBGet | Latest stable GitHub release |
| VLC | VideoLAN updater manifest — VLC publishes no GitHub releases |

Whichever tool a header already names is kept, only its version moves; an empty
header is seeded with the default for its slot (Prowlarr for queries, SABnzbd
for grabs, VLC for providers), and a header naming a tool that is not in that
list is left untouched. Headers set from the environment are never rewritten —
the process value wins over config regardless. Results are cached for an hour,
since GitHub allows 60 unauthenticated requests per hour per IP.

### Bootstrapping providers and indexers

For provisioned deployments, up to 10 providers and 10 indexers can be defined entirely from the environment, numbered `1`–`10`. A provider is picked up when its `HOST` is set; an indexer when its `URL` is set.

| Variable | Meaning |
|---|---|
| `PROVIDER_<n>_HOST` | Provider hostname (required) |
| `PROVIDER_<n>_PORT` | Port (default 563) |
| `PROVIDER_<n>_USERNAME`, `PROVIDER_<n>_PASSWORD` | Credentials |
| `PROVIDER_<n>_CONNECTIONS` | Connection count (default 10) |
| `PROVIDER_<n>_SSL` | Use SSL (default true) |
| `PROVIDER_<n>_PIPELINE_DEPTH` | Articles per request for this provider; unset inherits the default, `1` switches pipelining off. See [Article pipelining](speed-test.md#article-pipelining) |
| `PROVIDER_<n>_NAME`, `PROVIDER_<n>_PRIORITY`, `PROVIDER_<n>_ENABLED` | Optional display name, priority (default `<n>`), enabled flag |
| `INDEXER_<n>_URL` | Newznab indexer URL (required) |
| `INDEXER_<n>_API_KEY` | API key |
| `INDEXER_<n>_NAME`, `INDEXER_<n>_ENABLED` | Optional display name and enabled flag |

### Feature-specific (deployment-only)

These are deliberately env-only escape hatches, documented on their feature's page:

- `STREAMNZB_SPEEDTEST_NZB_URL`, `STREAMNZB_SPEEDTEST_MAX_BYTES`, `STREAMNZB_SPEEDTEST_MAX_SECONDS`, `STREAMNZB_SPEEDTEST_STEP_SECONDS` — [Provider speed test](speed-test.md)
- `STREAMNZB_EASYNEWS_ADVANCED_SEARCH`, `STREAMNZB_EASYNEWS_SPAM_FILTER`, `STREAMNZB_EASYNEWS_FILE_EXTENSIONS` — [Easynews advanced search](easynews.md)
