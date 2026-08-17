# Database backends: SQLite (default) or Postgres

StreamNZB stores its library, NZB history, and metrics in SQLite at
`<data dir>/streamnzb.db`. No setup is needed — this is the default.

To use an existing Postgres server instead, go to **General** (under Settings)
and set the **Database** backend to Postgres with your connection string.
StreamNZB checks the server is reachable before saving, then switches over
without a restart.

Equivalently, via environment variables:

```env
DATABASE_DRIVER=postgres
DATABASE_URL=postgres://user:password@db-host:5432/streamnzb?sslmode=disable
```

or `database_driver` / `database_url` in `config.json`.

## Switching backends

Switching backends carries your data with it — library, search and play
history, bad releases, and metrics — in either direction, and leaves the
database you came from untouched. Switching back later syncs only what the
other side added in the meantime, so nothing is lost or duplicated by moving
between the two. Set `database_skip_migration: true` in `config.json` to
switch without copying.

The one exception is switching *into* a database that already holds history but
has never been synced with the one you are leaving: there is no way to tell what
it already has, so history is left alone rather than duplicated (the library and
settings still migrate). Every switch after that is incremental.

> One StreamNZB instance per database. Instances cache state in memory and would
> overwrite each other's indexer usage counters if they shared one.
