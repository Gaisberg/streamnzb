# Backup & updates

## What to back up

Everything StreamNZB owns lives in the data directory (`/app/data` in Docker; see [Configuration](configuration.md#data-directory) for where it is on bare installs):

| File | Contents |
|---|---|
| `config.json` | All settings: providers, indexers, filters, search queries, streams, credentials |
| `streamnzb.db` (+ `-wal`, `-shm`) | Library, search & play history, bad releases, metrics (SQLite default) |
| `streamnzb.log`, `streamnzb-*.log` | Logs — not needed in backups |

Anything else that appears alongside (an extracted ffprobe binary, leftovers from older versions such as `state.json`) is re-creatable or already migrated into the database — not needed in backups. Backing up the whole directory is simplest. The minimum that preserves a full install is `config.json` plus the database.

- **SQLite**: stop StreamNZB before copying `streamnzb.db`, or copy all three files (`.db`, `.db-wal`, `.db-shm`) together — the WAL file holds recent writes, and copying the `.db` alone while the app runs can produce an inconsistent snapshot.
- **Postgres**: back up with `pg_dump` as usual; `config.json` in the data directory still holds your settings and must be backed up separately.

> `config.json` contains your Usenet provider and indexer credentials in plain text. Treat backups accordingly.

## Stopping cleanly

`docker stop`, `systemctl stop` and Ctrl-C all send a termination signal that StreamNZB acts on rather than dying where it stands. It stops accepting new requests, gives in-flight ones five seconds to finish, then cuts anything still running — a playback stream can last hours, so waiting for those to end on their own is not an option. After that it closes live sessions, hands back its Usenet connections, flushes provider usage counters to the database, and closes the database.

Two things follow from that. Playback in progress stops when you stop the server, as expected. And the whole sequence fits inside Docker's default ten-second stop timeout, so the container is never `SIGKILL`ed partway through — no truncated writes, and no Usenet connections left for your provider to time out on its own, which would otherwise count against your connection limit for a while after a restart.

## Updating

Migrations (database schema and config format) run automatically on startup, so updating is just replacing the binary/image. Back up the data directory first when jumping many versions. Downgrading is not supported — a newer version may have migrated the schema past what an older binary understands; restore the pre-upgrade backup instead.

**Docker:**

```bash
docker compose pull && docker compose up -d
```

**Binary**: download the new release from the [releases page](https://github.com/Gaisberg/streamnzb/releases), replace the executable, restart. Changes per version are listed in the [changelog](../CHANGELOG.md).
