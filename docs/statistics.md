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
| Grab success | Share of the NZBs grabbed from this indexer that went on to play. |
| Unique plays | Releases that were exclusive to this indexer **and** played successfully. |
| Avg grab | Mean time to fetch one NZB from the indexer. |
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

### Grab success

Unique hits answer *does this indexer find releases I cannot get elsewhere?*
Grab success answers the other half: *are the releases I actually use from it
reliable?* An indexer can score well on one and badly on the other, and the pair
is what tells you whether a paid subscription is earning its keep.

A grab is scored once, against the indexer whose NZB was played, and only on a
verdict that is conclusive about the NZB itself:

- **Successful** — playback ran past the good threshold. The NZB downloaded, the
  archive unpacked, ffprobe found a real video stream and the bytes kept coming.
- **Failed** — the attempt never reached a playable candidate: a broken NZB, an
  archive that would not unpack, no suitable video file inside, articles missing
  everywhere, or a mid-stream read failure before the threshold.
- **Neither** — playback started and stopped before the threshold. Someone
  sampling a stream for ten seconds and moving on says nothing about the
  release, so it is left out rather than held against the indexer. Attempt
  history still shows those as failures; this column is deliberately stricter.

Two details:

- A play served from StreamNZB's own release library grabbed nothing — the NZB
  came off disk — so it scores for nobody, however many times you replay it.
- The column reads **No samples** until an NZB from that indexer has been played
  to a verdict. Searching alone never moves it.

**Avg grab** beside it is the mean time to fetch one NZB from the indexer, the
response body included, measured only on grabs that returned bytes. A refused
grab times the refusal rather than the indexer, so it is not counted.

### Unique plays

Unique hits and grab success each answer half a question, and this column is
where they meet: a release that no other indexer carried *and* that played. It
is the count of exclusive content the indexer actually delivered, so it is the
single number that says whether a subscription is pulling its weight.

It is a subset of the successful grabs, never larger. A release exclusive to an
indexer but unplayable counts as a failed grab and earns nothing here — being
the only one to carry something broken is not a contribution.

The verdict comes from the same merge that decides unique hits, stamped on the
release and read back when playback succeeds. It is stamped on every copy, so
[failing over](stream-model.md) to another copy of the same release keeps the
credit. Expect this to stay well below unique hits: most of what a search finds
is never played.

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
