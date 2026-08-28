# Search requests

The **Search** page (under Settings) holds reusable search requests — one list for movies, one for TV. A request describes *how* to ask your indexers for a title; streams then select which requests they use and in what order. Query strings are built automatically from resolved metadata (title, year, season/episode) — there is nothing to type per search.

## Anatomy of a search request

| Field | Meaning |
|---|---|
| **Name** | Unique identifier; streams reference it. Fixed after creation. |
| **Search Mode** | **ID Search** asks indexers by database ID (IMDb/TVDB/TMDB/Kitsu — whichever the indexer supports). **Text Search** sends the title as a text query. |
| **Category** | Newznab category list, comma-separated (`2000` movies, `5000` TV). |
| **Title Language** *(Text mode)* | Which title to use — *Original* uses the original-language title (Japanese titles are romanized). It is both the title sent to indexers and the title results are checked against. ID requests have no such setting: they name an id, not a title, and do not gate on the answer. |
| **Limit** | Max results per indexer; 0 = indexer maximum. |
| **Year** | In text mode, *Search + Validation* appends the year to the query and checks results against it (±1 year); in ID mode only validation applies — unlike the title, the year stays enforced for ID requests. *Ignore* does neither. |
| **Scope** *(TV)* | What episode information the request carries: **Season/Episode** (`S01E04` / `season=`+`ep=` params), **Season** (`S01` / `season=`), or **None** (title only). |
| **Anime Absolute** *(TV)* | When the content looks like anime, *Supplement* adds extra text queries using the absolute episode number (`One Piece 63`) and widens the category to include TV/Anime (5070), for indexers that number that way. |

Titles are normalized before they go out: accents are transliterated the way release names spell them (`König der Löwen` → `Koenig der Loewen`), apostrophes drop, other punctuation becomes spaces.

Editing, creating or deleting a request clears the search cache, so changes take effect on the next search.

## How requests run

Each stream lists its movie and TV requests in order (drag to reorder in the stream editor), with a **Search requests** mode:

- **Combine all** (default) — every request runs in parallel; results are merged in your configured order and deduplicated.
- **Stop after first hit** — requests run in order and stop at the first one that returns results. Put your most precise request first and broader fallbacks after it.

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

- An ID-mode request is skipped for content with no usable database ID, and a text-mode request is skipped when no title could be prepared — the History page shows what actually ran.
- Easynews is text-only: it participates in text-mode requests (and the anime absolute supplements) but never ID searches.
- Episodes that haven't aired yet are answered with no results without touching indexers.
- An indexer that has spent a daily limit sits the request out — see [Daily limits](#daily-limits).

## Daily limits

Each indexer (Settings → Indexers) carries an **API hits** and a **Downloads** daily limit. API hits are spent by searches (and the occasional credential ping or capability fetch, which count too), downloads by fetching an NZB when playback starts. Capability documents are cached for a week, so restarts and settings saves reuse them instead of re-asking every indexer — a fresh fetch happens only when the cache is stale or the indexer's URL or API key changed. Live counters are on the Stats page, and the indexer's own `X-RateLimit-*` / `X-DNZBLimit-*` response headers correct them whenever it sends them — downward only. Headers can reveal that the account has less budget left than our counter thought (another app sharing the account, say), but a server advertising a bigger quota than the configured limit never talks StreamNZB out of your cap, so a deliberately conservative limit holds.

Either limit running out takes the indexer out of searches, not just out of grabs: a release that cannot be fetched is a dead result, and offering it would cost you a failover hop per candidate at playback time. Releases already sitting in a cached result list are dropped for the same reason, so streams from a spent indexer stop being offered without waiting for the cache to expire — unless another indexer holds a copy of the same release, in which case that copy takes over and the release stays offered (see [Same-release variants](#same-release-variants)).

Two things keep this from stranding a working indexer:

- Once either limit is spent, one search every 15 minutes is let through anyway. Only the indexer's own headers can tell us the budget came back, and they only arrive on a request — so the probe is what re-opens it after a manual quota raise, or when the indexer's daily reset lands on a different clock than ours (our counters turn over at local midnight).
- Nothing is discarded. Cached results are filtered, not purged, so when the limit resets the full result set is still there and no re-search (or fresh API hits) is needed.

An indexer that itself answers *request limit reached* (newznab error 201) is paused for 30 minutes rather than the 60-second cooldown a transient 429 gets — a spent daily quota stays spent for hours, and some indexers count the refused retries against it too. A `Retry-After` from the server still wins when it sends one.

Skipped indexers appear in Debug logs as *Indexer skipped: limit reached*. Releases held in the library are unaffected — their NZB is already stored locally, so they play without spending an indexer download at all.

## Per-indexer content scope

Each indexer (Settings → Indexers) carries a **Content** setting that decides which searches it participates in:

- **All content** (default) — the indexer is queried for everything.
- **Anime only** — the indexer is only queried for anime.
- **Everything except anime** — the indexer sits out anime requests.

A request counts as anime when it arrives through a Kitsu catalog, or when TMDB metadata classifies it as animation not originally in English — the same detection the Anime Absolute supplement and the per-kind filter profiles use (TMDB must be configured for the metadata path). The scope applies wherever the indexer is used, streams and standalone add-ons alike; skipped indexers appear in Debug logs as *Indexer skipped for request*. The AnimeTosho and aniNZB presets default to **Anime only**.

Behind NZBHydra2, this pairs with Hydra's `indexers` API parameter: add the same Hydra twice — for example `http://hydra:5076/api?indexers=General1,General2` scoped to *Everything except anime* and `http://hydra:5076/api?indexers=AnimeIndexer` scoped to *Anime only* — and each request only hits the Hydra indexers that make sense for it. Query parameters on the configured URL or API path ride along on every search.

## Season packs

There is no separate "pack search" — one search's results are validated and packs are accepted alongside single episodes. For an episode request, releases are kept in preference order: exact episode, multi-episode release containing it, season pack for that season, complete-series pack. The History page's search panel shows these acceptance counts per request.

## Validation

Results are checked against the metadata before anything else happens. What is *enforced* depends on how the request asked:

- **Season, episode and year** are enforced for every request. An episode request keeps only releases that can contain that episode; Year, when enabled, requires the release year to be within ±1.
- **The title** is enforced for **text requests only**. A text query is just a keyword, so the indexer has no idea which title you meant and the check is what makes the answer yours.
- **ID requests trust the indexer.** The request named an IMDb/TVDB/TMDB/Kitsu id and nothing else, so the indexer resolved the title itself. Release names diverge from TMDB/TVDB constantly — `Special.Ops.Lioness.S02E01` for a show TMDB calls *Lioness* — and dropping those meant losing correct results to a naming disagreement.

An ID request still runs the title check, it just does not act on it: mismatches are counted and shown as *Title mismatch (kept)* in the History funnel. That number is worth a look. A handful is normal naming drift; a request where nearly everything is a mismatch is an indexer answering an ID search with something it was not asked for — typically an aggregator (NZBHydra2, Prowlarr) silently converting an unsupported ID search into a title search.

Where the title *is* enforced, matching tolerates leading articles, short franchise suffixes (`SVU`), punctuation and `&`/`and`. A request that pins a season or episode also accepts a release whose name carries a prefix the metadata title dropped, because the episode number already proves the show. Drops show up as *Title mismatch* / *Year mismatch* in the History funnel. If good releases are being dropped from a text request, switching its **Title Language** is usually the fix (e.g. results named under an original or localized title) — or add a second text request under the other language. ID requests need no such tuning; they are checked against the English and original titles (plus every Kitsu title for anime) and drop nothing on a title anyway.

## Defaults

A fresh install ships four requests, all assigned to the default stream in this order:

| Name | Mode | Year | Scope |
|---|---|---|---|
| `DefaultMovieText` | Text | On | — |
| `DefaultMovieID` | ID | On | — |
| `DefaultTVText` | Text | Off | Season/Episode |
| `DefaultTVID` | ID | Off | Season/Episode |

Movie requests carry the year because movie releases are named with one. TV requests deliberately don't: scene TV releases are named `Title.S01E01.1080p...` — a year token would narrow the query to nothing and arm year validation against results that can never carry one.
