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

## Updating

Migrations (database schema and config format) run automatically on startup, so updating is just replacing the binary/image. Back up the data directory first when jumping many versions. Downgrading is not supported — a newer version may have migrated the schema past what an older binary understands; restore the pre-upgrade backup instead.

**Docker:**

```bash
docker compose pull && docker compose up -d
```

**Binary**: download the new release from the [releases page](https://github.com/Gaisberg/streamnzb/releases), replace the executable, restart. Changes per version are listed in the [changelog](../CHANGELOG.md).
