# Database backends: SQLite (default) or Postgres

StreamNZB stores its library, NZB history, and metrics in SQLite at
`<data dir>/streamnzb.db`. No setup is needed — this is the default.

To use an existing Postgres server instead, go to **General** (under Settings)
and set the **Database** backend to Postgres with your connection string.
StreamNZB checks the server is reachable before saving, then switches over
without a restart.

Equivalently, via environment variables:

```env
STREAMNZB_DATABASE_DRIVER=postgres
STREAMNZB_DATABASE_URL=postgres://user:password@db-host:5432/streamnzb?sslmode=disable
```

or `database_driver` / `database_url` in `config.json`.

## Passwords with special characters

StreamNZB uses pgx to parse Postgres connection strings. In URL form, percent
encode special characters in the username and password, such as `@` (`%40`),
`:` (`%3A`), `/` (`%2F`), `?` (`%3F`), `#` (`%23`), and `%` (`%25`). Encode
each credential separately, not the entire connection URL, and do not encode
an already encoded value again.

For example, the password `p@assword` becomes `p%40assword`. A raw `@` ends
the credentials section, so using it in the password can cause a hostname
lookup error mentioning part of the password.

For Docker Compose, store the encoded password in `.env`:

```env
MY_PASSWORD_ENCODED=p%40assword
```

Then use it in your service's environment:

```yaml
services:
  streamnzb:
    environment:
      STREAMNZB_DATABASE_DRIVER: postgres
      STREAMNZB_DATABASE_URL: "postgres://me:${MY_PASSWORD_ENCODED}@postgres:5432/streamnzb?sslmode=disable"
```

Compose substitutes the value from `.env`; it does not URL encode it. The
Postgres server must still be configured with the original password
(`p@assword` in this example).

Alternatively, `STREAMNZB_DATABASE_URL` also accepts a keyword connection
string, which does not require URL encoding:

```env
STREAMNZB_DATABASE_URL="host=postgres port=5432 user=me password=p@assword dbname=streamnzb sslmode=disable"
```

In keyword form, enclose values containing spaces in single quotes; escape
single quotes and backslashes inside values with a backslash.

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
