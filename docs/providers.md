# Providers

How StreamNZB spreads work across your Usenet providers, and how to keep a
metered account out of the rotation until it is actually needed.

## Priority

Every enabled provider has a priority. It is an **ordering**, not a share: the
lowest number is asked first, and the rest follow in order whenever the one
ahead cannot serve. Priority does not cap how much a provider is used, and it
does not make a provider a last resort — a provider low in the list still takes
part in normal streaming.

Three things move traffic down the list:

- **Failover.** A provider that answers `430 No Such Article` is excluded for
  that segment and the next one is asked. This is the main reason to have more
  than one provider.
- **Cooloff.** Five consecutive 430s bench a provider for 60 seconds, and
  everything goes to the next provider for the duration. Ten consecutive
  successes clear it early.
- **First-segment races.** The first segment of a file decides whether a release
  is playable at all, so it is requested from every primary provider at once and
  the first answer wins. Each racer downloads the whole article, so every
  primary is charged for it — including the ones that lost the race.

## Backup only

Switch **Backup only** on (Settings → Providers → edit a provider) to take a
provider out of that rotation entirely. A backup:

- is never raced for a first segment,
- is never asked for an article while any other provider can serve one,
- is walked one provider at a time, in priority order, only once every primary
  has failed on that segment.

That is the setting for a pay-per-GB block account. Without it, a block account
sitting at the bottom of the priority list is still a full participant: it pays
for every first-segment race and absorbs whole streams whenever a primary is in
cooloff — enough to spend 10 GB in minutes.

It is deliberately narrow: a backup still answers `STAT` (existence checks,
which transfer no article data and are what makes it usable as a fallback in the
first place), and it still serves whole segments once the primaries have missed
them. It is a tier, not a byte budget.

Two edge cases worth knowing:

- If **every** enabled provider is marked backup, the flag is ignored and they
  all work as primaries — a backup tier with nothing above it could serve
  nothing at all. A warning is logged at startup.
- The same applies per stream: if a stream's provider selection contains only
  backup providers, they act as primaries for that stream. Give a stream at
  least one unmetered provider if you want its backup to stay held back.

The [NNTP proxy](nntp-proxy.md) follows the same order — primaries in priority
order, backups only after all of them miss — so a downloader pointed at
StreamNZB gets the same treatment as playback.

## Environment

Providers declared through the environment take `PROVIDER_<n>_BACKUP` (default
`false`); see the [configuration reference](configuration.md#providers-and-indexers).

## Connections

Connection count is per provider and is what limits how wide read-ahead can
fan out. See [Provider speed test](speed-test.md) for finding the right number
and for the per-provider **Articles per request** (pipelining) setting.
