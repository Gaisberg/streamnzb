# Search requests

The **Search** page (under Settings) holds reusable search requests — one list for movies, one for TV. A request describes *how* to ask your indexers for a title; streams then select which requests they use and in what order. Query strings are built automatically from resolved metadata (title, year, season/episode) — there is nothing to type per search.

## Anatomy of a search request

| Field | Meaning |
|---|---|
| **Name** | Unique identifier; streams reference it. Fixed after creation. |
| **Search Mode** | **ID Search** asks indexers by database ID (IMDb/TVDB/TMDB/Kitsu — whichever the indexer supports). **Text Search** sends the title as a text query. |
| **Category** | Newznab category list, comma-separated (`2000` movies, `5000` TV). |
| **Title Language** | Which title to use. *Original* uses the original-language title (Japanese titles are romanized). In text mode this picks the one title sent to indexers; in ID mode the request is ID-only, and the selected languages instead widen **validation**, so results named under any of them are accepted. |
| **Limit** | Max results per indexer; 0 = indexer maximum. |
| **Year** | In text mode, *Search + Validation* appends the year to the query and checks results against it (±1 year); in ID mode only validation applies. *Ignore* does neither. |
| **Scope** *(TV)* | What episode information the request carries: **Season/Episode** (`S01E04` / `season=`+`ep=` params), **Season** (`S01` / `season=`), or **None** (title only). |
| **Anime Absolute** *(TV)* | When the content looks like anime, *Supplement* adds extra text queries using the absolute episode number (`One Piece 63`) and widens the category to include TV/Anime (5070), for indexers that number that way. |

Titles are normalized before they go out: accents are transliterated the way release names spell them (`König der Löwen` → `Koenig der Loewen`), apostrophes drop, other punctuation becomes spaces.

Editing, creating or deleting a request clears the search cache, so changes take effect on the next search.

## How requests run

Each stream lists its movie and TV requests in order (drag to reorder in the stream editor), with a **Search requests** mode:

- **Combine all** (default) — every request runs in parallel; results are merged in your configured order and deduplicated.
- **Stop after first hit** — requests run in order and stop at the first one that returns results. Put your most precise request first and broader fallbacks after it.

Notes on execution:

- An ID-mode request is skipped for content with no usable database ID, and a text-mode request is skipped when no title could be prepared — the History page shows what actually ran.
- Easynews is text-only: it participates in text-mode requests (and the anime absolute supplements) but never ID searches.
- Episodes that haven't aired yet are answered with no results without touching indexers.
- An indexer that has spent a daily limit sits the request out — see [Daily limits](#daily-limits).

## Daily limits

Each indexer (Settings → Indexers) carries an **API hits** and a **Downloads** daily limit. API hits are spent by searches, downloads by fetching an NZB when playback starts. Live counters are on the Stats page, and the indexer's own `X-RateLimit-*` / `X-DNZBLimit-*` response headers correct them whenever it sends them.

Either limit running out takes the indexer out of searches, not just out of grabs: a release that cannot be fetched is a dead result, and offering it would cost you a failover hop per candidate at playback time. Releases already sitting in a cached result list are dropped for the same reason, so streams from a spent indexer stop being offered without waiting for the cache to expire.

Two things keep this from stranding a working indexer:

- Once the download limit is spent, one search every 15 minutes is let through anyway. Only the indexer's own headers can tell us the budget came back, and they only arrive on a request — so the probe is what re-opens it after a manual quota raise, or when the indexer's daily reset lands on a different clock than ours.
- Nothing is discarded. Cached results are filtered, not purged, so when the limit resets the full result set is still there and no re-search (or fresh API hits) is needed.

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

Results are checked against the metadata before anything else happens: the release title must match one of the expected titles (any selected title language, with fuzzy matching that tolerates leading articles and short franchise suffixes), and — when Year is enabled — the release year must be within ±1. Drops show up as *Title mismatch* / *Year mismatch* in the History funnel. If good releases are being dropped, adding the right **Title Language** to the request is usually the fix (e.g. results named under an original or localized title).

## Defaults

A fresh install ships four requests, all assigned to the default stream in this order:

| Name | Mode | Year | Scope |
|---|---|---|---|
| `DefaultMovieText` | Text | On | — |
| `DefaultMovieID` | ID | On | — |
| `DefaultTVText` | Text | Off | Season/Episode |
| `DefaultTVID` | ID | Off | Season/Episode |

Movie requests carry the year because movie releases are named with one. TV requests deliberately don't: scene TV releases are named `Title.S01E01.1080p...` — a year token would narrow the query to nothing and arm year validation against results that can never carry one.
