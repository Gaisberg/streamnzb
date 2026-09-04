# Search requests

The **Search** page (under Settings) holds reusable search requests — one list for movies, one for TV. A request describes *how* to ask your indexers for a title; streams then select which requests they use and in what order. Query strings are built automatically from resolved metadata (title, year, season/episode) — there is nothing to type per search.

## Anatomy of a search request

A request is a plan: an ordered list of **attempts**, one rule for **when to
stop**, and one description of what a matching release **must look like**.

| Field | Meaning |
|---|---|
| **Name** | Unique identifier; streams reference it. Fixed after creation. |
| **Attempts** | The questions the request asks indexers, in order — one indexer query per row. Each row has an **Address** and, for TV, a **Target**; Title rows also carry a language and whether to put the year in the query. Presets (*Balanced*, *Precise*, *Broad*) fill in a sensible list; drag rows to reorder. |
| **When to stop** | **Stop at first hit** walks the rows in order and stops at the first one that matched anything. **Stop after enough hits** keeps walking until the rows asked so far have matched at least **Minimum hits** distinct releases between them. **Run every attempt** asks every row every time and merges the results. |
| **Minimum hits** | The threshold for *Stop after enough hits* (default 10). Counts releases that passed validation, with the same release listed by several indexers counted once; everything found on the way is kept. |
| **Ordering** *(TV)* | **As listed** runs the rows as written. **Season first once it has aired** moves the Season rows to the front once every episode of the requested season has aired — a finished season is where the season pack lives, an airing one is where the single episode does. |
| **Accepted titles** | The metadata titles a release name may match (English, original, localized, Kitsu…). Separate from what goes out: a Title row queries under one language, this is what may come back. Empty falls back to each row's own query language. |
| **Year must match** | Requires the release year to be within ±1 of the metadata year. Off for TV by default — scene TV releases carry no year. |
| **Season packs** *(TV)* | Whether a season or complete-series pack that contains the requested episode counts as a match. |
| **Limit** | Max results per indexer; 0 = indexer maximum. |
| **Categories** | Usually empty — see [Categories](#categories). A comma-separated Newznab list here is sent to every indexer for this request instead. |

An attempt's **Address** is how it asks:

- **ID** names the content by database id (IMDb/TVDB/TMDB/Kitsu — whichever the
  indexer supports) and trusts the title the indexer answers with.
- **Title** sends the metadata title as a text query under the chosen language
  (*Original* is the original-language title, with Japanese romanized) and
  checks the answer against the accepted titles.

Its **Target** is what it asks for, on TV: **Episode** (`S01E04` /
`season=`+`ep=`), **Season** (`S01` / `season=`), **Series** (the title alone),
or **Absolute** — the anime absolute episode number (`One Piece 63`), for
indexers that number that way. Absolute rows are skipped outright for anything
that is not anime, and a row whose target the content cannot supply (an Episode
row for a season-only request) drops to the next narrower target rather than
being skipped.

Titles are normalized before they go out: accents are transliterated the way release names spell them (`König der Löwen` → `Koenig der Loewen`), apostrophes drop, other punctuation becomes spaces.

Editing, creating or deleting a request clears the search cache, so changes take effect on the next search.

## How requests run

Each stream lists its movie and TV requests (drag to reorder in the stream
editor). Every selected request runs, concurrently, and the results are merged
in configured order and deduplicated. A new stream starts with every configured
request selected.

Sequencing lives *inside* a request. Its attempts and stop rule are the fallback
chain — "ask by id, and only if that finds nothing, ask by title" is one request
with two rows and *Stop at first hit*, not two requests in an order. There is no
stream-level "stop after first request" any more; a stream that selects several
requests gets all of them.

A stream's **Indexer Mode** (in the stream editor) is the same choice one level
down, across indexers rather than across attempts: *Combine* queries them all,
*Failover* walks them in order until one answers.

## How long a search takes

Breadth is paid for in latency. *Run every attempt* and *Combine* indexers run
everything in parallel and then wait for all of it, so a search takes as long as
its **slowest** indexer on its slowest attempt — not the average, and not the
first useful answer. One indexer having a bad minute sets the time Stremio
spends showing a spinner.

A real search from the debug log, four queries wide (two attempts × two
indexers), on a request that ran every attempt:

```
DrunkenSlug  id    149 ms
altHUB       text  289 ms
altHUB       id    320 ms
DrunkenSlug  text  984 ms   ← the whole search waits here
                   ------
/stream total     1066 ms
```

The ID attempt had everything it needed after 320 ms. The remaining 664 ms
bought the text attempt's extra results, and every result in the list —
including the ones already in hand — waited for it.

That is a trade, not a bug: the text query finds releases an ID search misses,
particularly on indexers with thin ID coverage. It is also the trade the stock
requests make for you — *Stop at first hit* runs the ID row and only spends the
remaining 664 ms when that came back empty.

| If you want | Set |
|---|---|
| The most results, whatever it costs in time | The **Broad** preset (*Run every attempt*) + **Combine** indexers |
| A fast list from your best indexer, falling back only when it comes up empty | The **Precise** preset + **Failover** indexers, with your most reliable indexer first |
| Breadth without paying for the broad query every time | The **Balanced** preset + **Combine** indexers (the defaults) |

Two things worth knowing before you narrow anything:

- **Results are cached, so the cost is per unique title, not per request.** A
  second `/stream` for the same content answers from the playlist cache. Slow
  searches hurt on first view; they do not compound while browsing.
- **A skipped attempt skips its releases.** *Stop at first hit* stops at the
  first row that returns *anything* — one marginal result from a precise ID row
  ends the request, and the Title row that would have found ten more never
  runs. Order the rows accordingly, switch to *Stop after enough hits* so a thin
  answer keeps the walk going, or switch to *Run every attempt*.

*Stop after enough hits* is the middle ground. A first row that comes back
with a full list costs one round trip, like *Stop at first hit*; a first row
that comes back with two releases — a niche show, an anime that indexers file
under absolute numbering, a title with thin ID coverage — goes on to the next
row, and the next, until the rows asked so far have found the minimum between
them or the request runs out of rows. The count is of *distinct* releases: the
same NZB from three indexers is one hit, not three, so the threshold measures
choice rather than how many indexers you have.

## Same-release variants

Several indexers carrying the same release used to produce one result and a
pile of discarded duplicates. They still produce one result — but the other
copies ride along on it as *variants*:

```
Movie.2160p.Remux.HDR10-FraMeSToR
  ├─ NZBGeek copy      ← plays first
  ├─ DrunkenSlug copy
  └─ NinjaCentral copy
```

This matters because duplicate indexer entries are not always the same NZB. One
can be short of articles where another is complete, so a duplicate is redundancy
worth keeping rather than clutter worth deleting.

- **Which copy leads** — what is already in your library first, then what the
  availability database reports healthy on a backbone your providers use, then a
  recent availability confirmation, then your indexer order, with grabs and post
  age breaking the rest.
- **What failover does with them** — a copy that will not open, or that dies
  mid-stream, moves the slot to the next copy of the *same* release before the
  failover walk gives up and moves to a different release. The slot id does not
  change, so the client is never redirected: it asked for this release and it
  still gets this release, out of another indexer's NZB. **Same-release
  attempts** caps how many copies one release may spend, and it is `Merge only`
  by default — see below.
- **What a failure condemns** — the NZB that failed, not the release. The
  AvailNZB report and the persistent bad-release record key on that copy's
  details URL, so the copies beside it stay playable and stay offered.
- **What it costs, and the default** — walking copies costs time to first
  frame: a release that is simply gone is gone in all of its copies, and each
  copy playback tries is another startup spent before failover moves to a
  different release. So **Same-release attempts** defaults to `Merge only` —
  the duplicates are still folded into one result, still swapped in at search
  time when a copy is reported bad or its indexer has spent its daily limit,
  but playback never walks them. Raise it to `2 copies` or more if a stalled or
  article-short NZB is the failure you actually hit, and you would rather spend
  a second startup on the same release than move to a worse one.
- **Seeing it** — the History funnel shows **Variants kept** next to the dedup
  count, and result templates can render `{{.Variants}}` — see
  [Custom result formats](result-formatting.md).

Notes on execution:

- An ID attempt is skipped for content with no usable database ID, and a Title attempt is skipped when no title could be prepared — the History page shows what actually ran, attempt by attempt.
- An indexer that cannot answer a kind of attempt sits it out rather than failing it: Easynews is text-only, so it takes Title attempts but never ID ones.
- Episodes that haven't aired yet are answered with no results without touching indexers.
- An indexer that has spent a daily limit sits the request out — see [Daily limits](#daily-limits).

## Daily limits

Each indexer (Settings → Indexers) carries an **API hits** and a **Downloads** daily limit, measured over a **trailing 24 hours** rather than a calendar day — the same rolling window most indexers meter themselves, so a hit stops counting exactly 24 hours after it was made instead of everything resetting at midnight. API hits are spent by searches (and the occasional credential ping or capability fetch, which count too), downloads by fetching an NZB when playback starts. Capability documents are cached for a week, so restarts and settings saves reuse them instead of re-asking every indexer — a fresh fetch happens only when the cache is stale or the indexer's URL or API key changed. Live counters are on the Stats page, and the indexer's own `X-RateLimit-*` / `X-DNZBLimit-*` response headers govern them whenever it sends them: the server sees consumption our counter cannot (another app sharing the account, say) and its own resets, so its numbers win in both directions — except that a server advertising a bigger quota than the configured limit never talks StreamNZB out of your cap, so a deliberately conservative limit holds.

Either limit running out takes the indexer out of searches, not just out of grabs: a release that cannot be fetched is a dead result, and offering it would cost you a failover hop per candidate at playback time. Releases already sitting in a cached result list are dropped for the same reason, so streams from a spent indexer stop being offered without waiting for the cache to expire — unless another indexer holds a copy of the same release, in which case that copy takes over and the release stays offered (see [Same-release variants](#same-release-variants)).

Two things keep this from stranding a working indexer:

- Once either limit is spent, one search every 15 minutes is let through anyway. Only the indexer's own headers can tell us the budget came back, and they only arrive on a request — so the probe is what re-opens it after a manual quota raise, or when the indexer meters on a different clock than our trailing window.
- Nothing is discarded. Cached results are filtered, not purged, so when the limit resets the full result set is still there and no re-search (or fresh API hits) is needed.

An indexer that itself answers *request limit reached* (newznab error 201) is paused for 30 minutes rather than the 60-second cooldown a transient 429 gets — a spent daily quota stays spent for hours, and some indexers count the refused retries against it too. A `Retry-After` from the server still wins when it sends one.

Skipped indexers appear in Debug logs as *Indexer skipped: limit reached*. Releases held in the library are unaffected — their NZB is already stored locally, so they play without spending an indexer download at all.

## Per-indexer content scope

Each indexer (Settings → Indexers) carries a **Content** setting that decides which searches it participates in:

- **All content** (default) — the indexer is queried for everything.
- **Anime only** — the indexer is only queried for anime.
- **Everything except anime** — the indexer sits out anime requests.

A request counts as anime when it arrives through a Kitsu catalog, or when TMDB metadata classifies it as animation not originally in English — the same detection the Absolute attempts, anime categories and per-kind filter profiles use (TMDB must be configured for the metadata path). The scope applies wherever the indexer is used, streams and standalone add-ons alike; skipped indexers appear in Debug logs as *Indexer skipped for request*. The AnimeTosho and aniNZB presets default to **Anime only**.

Behind NZBHydra2, this pairs with Hydra's `indexers` API parameter: add the same Hydra twice — for example `http://hydra:5076/api?indexers=General1,General2` scoped to *Everything except anime* and `http://hydra:5076/api?indexers=AnimeIndexer` scoped to *Anime only* — and each request only hits the Hydra indexers that make sense for it. Query parameters on the configured URL or API path ride along on every search.

## Categories

A request does not name Newznab categories; it names a *kind* of content, and
each indexer is asked in the categories that kind means:

| Content | Categories sent |
|---|---|
| Movie | `2000` |
| TV | `5000` |
| Anime TV | `5070,5000` — plus any bucket the indexer's caps name "anime" under an id of its own |

The standard buckets are always sent, whether or not the indexer lists them in
its capabilities: a well-behaved indexer maps its content onto them regardless,
and plenty publish a TV root with no anime subcategory while still filing anime
under 5070. Caps only ever widen the list. A request counts as anime by the
same detection the [per-indexer content scope](#per-indexer-content-scope)
uses.

The **Categories** field on a request, and **Movie/TV categories** on an
indexer, replace this outright for indexers that file things under ids of their
own. A request's list wins over an indexer's.

## Adaptive ordering

Indexers do not catalogue TV consistently: one carries `Show.Name.S03E07`, the
next only has it inside `Show.Name.S03.COMPLETE`. Asking only for the episode
misses the second; asking only for the season broadens every request to find
it. The stock TV request asks for the episode first and falls back to the
season, and its **Ordering** — *Season first once it has aired* — flips that
around by the state of the season: episode first while the season is still
airing, season first once every episode of it has aired.

It reads air dates from TVMaze, TVDB or TMDB — the same sources the unaired gate
uses, and no extra configuration. When none of them can answer, the season
counts as airing and the rows run as listed. An episode with no air date keeps
its season airing, so an announced-but-undated finale does not flip the order.
The reorder is stable: Season rows move to the front together and keep their
relative order, and everything else keeps its.

## Season packs

There is no separate "pack search" — one search's results are validated and packs are accepted alongside single episodes. For an episode request, releases are kept in preference order: exact episode, multi-episode release containing it, season pack for that season, complete-series pack. The History page's search panel shows these acceptance counts per request.

## Validation

Results are checked against the metadata before anything else happens. What is *enforced* depends on how the request asked:

- **Season and episode** are enforced for every attempt. An episode request keeps only releases that can contain that episode; with **Season packs** off, only releases that name the episode.
- **Year**, when *Year must match* is on, requires the release year to be within ±1.
- **The title** is enforced for **Title attempts only**. A text query is just a keyword, so the indexer has no idea which title you meant and the check against the accepted titles is what makes the answer yours.
- **ID attempts trust the indexer.** The attempt named an IMDb/TVDB/TMDB/Kitsu id and nothing else, so the indexer resolved the title itself. Release names diverge from TMDB/TVDB constantly — `Special.Ops.Lioness.S02E01` for a show TMDB calls *Lioness* — and dropping those meant losing correct results to a naming disagreement.

An ID attempt still runs the title check, it just does not act on it: mismatches are counted and shown as *Title mismatch (kept)* in the History funnel. That number is worth a look. A handful is normal naming drift; an attempt where nearly everything is a mismatch is an indexer answering an ID search with something it was not asked for — typically an aggregator (NZBHydra2, Prowlarr) silently converting an unsupported ID search into a title search.

Where the title *is* enforced, matching tolerates leading articles, short franchise suffixes (`SVU`), punctuation and `&`/`and`. An attempt that pins a season or episode also accepts a release whose name carries a prefix the metadata title dropped, because the episode number already proves the show. Drops show up as *Title mismatch* / *Year mismatch* in the History funnel. If good releases are being dropped from a Title attempt, adding the other language to **Accepted titles** is usually the fix (results named under an original or localized title), or a second Title row querying under it.

## Defaults

A fresh install ships two requests, both assigned to the default stream, each
the *Balanced* preset for its kind:

**`DefaultMovie`** — *Stop at first hit*; year must match; accepts English and original titles.

| # | Address | Target |
|---|---|---|
| 1 | ID | — |
| 2 | Title (en-US, + year) | — |

**`DefaultTV`** — *Stop at first hit*; *Season first once it has aired*; season packs on; no year; accepts English and original titles.

| # | Address | Target |
|---|---|---|
| 1 | ID | Episode |
| 2 | Title (en-US) | Absolute *(anime only)* |
| 3 | Title (en-US) | Episode |
| 4 | ID | Season |
| 5 | Title (en-US) | Season |

Each starts as the narrowest question that could answer and widens only when it
finds nothing: one ID query for a movie that any indexer carries, up to five
for an anime episode that only exists inside a season pack on an indexer with
no ID coverage. Movie requests carry the year because movie releases are named
with one; TV requests deliberately don't — scene TV releases are named
`Title.S01E01.1080p...`, and a year token would narrow the query to nothing.

Requests from earlier versions are converted in place on first load: the old
*Search Mode*, *Scope*, *Year* and *Anime Absolute* settings become the
attempt list they described (*Dynamic* mode is an ID row then a Title row with
*Stop at first hit*; the adaptive scopes become Episode rows then Season rows),
so an upgraded request asks exactly what it asked before. The old stream-level
*Search requests mode* is gone — every selected request now runs.
