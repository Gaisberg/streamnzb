# Statistics

**Settings → Statistics** shows how the indexers and providers you configured are
actually behaving: how fast they answer, how much of your traffic each one
carries, and which of them are earning their keep.

Two kinds of data are on the page:

- **Live performance** — the `/stream` response-time percentiles and the playback
  start time (TTFF) charts, measured from requests served since startup.
- **History** — everything below the date range filter. A snapshot of every
  indexer and provider counter is written to the database every 30 seconds, and
  the tables show the change across the range you picked (24H, 7D, 30D, 90D,
  month-to-date, all, or a custom range). Counters that reset — a restart clears
  the in-memory ones — are handled: the range shows what accumulated, not a
  negative jump.

The trash icon on a row deletes that indexer's or provider's snapshots **within
the selected range only**.

## Indexer columns

| Column | Meaning |
|---|---|
| Avg response | Mean search round-trip for that indexer. |
| Searches | Search API calls made. One playback request runs one search per attempt in each selected search plan, so this climbs faster than "number of things you played". |
| Downloads | NZB downloads fetched from the indexer. |
| Unique hits | Deduplicated releases **this indexer alone carried**. |
| AvailNZB availability | Share of this indexer's releases that [AvailNZB](availnzb.md) confirmed as available. |

### Unique hits

After a search, copies of the same release coming from different indexers are
merged into one result (the losers ride along as variants). A unique hit is a
merged release whose every copy came from one indexer — nobody else in your
indexer list had it.

That makes the column an answer to the question worth asking about a paid
indexer: *does it contribute content I could not get anywhere else?* An indexer
that mirrors what the others already have will sit near zero however many
searches it answers, and an indexer with a handful of unique hits per session is
carrying releases you would otherwise not see.

Two details:

- A copy served from StreamNZB's own release library is the same content coming
  back from disk, not a second indexer having it, so it never cancels a hit.
- Releases rejected as unplayable are counted after the rejection, so a bad
  release earns nobody a hit.

### AvailNZB availability

This is not the indexer's uptime — it is what AvailNZB reported about the
releases that indexer returned: available results kept versus results discarded
as unavailable. It reads **No samples** until AvailNZB has a record for at least
one release from that indexer, which is normal for a fresh install, for content
nobody has reported on yet, and for indexers whose releases you have not played.
Discards are only counted when the stream is set to filter out
AvailNZB-reported-bad releases.

## Provider columns

| Column | Meaning |
|---|---|
| Downloaded | Volume pulled from that provider. |
| Usage | Its share of the total volume downloaded across all providers. |
| Articles missing | Share of requested articles the provider answered `430` for. **No samples** until articles have actually been fetched from it. |

A high "articles missing" on one provider next to a low one on another is the
signal that the first has worse retention or completion for what you watch — see
[Providers](providers.md) for priority and failover.
