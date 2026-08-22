# Newznab endpoint

StreamNZB re-serves every indexer you have configured as a single Newznab API, so any Newznab-compatible application can search the whole set through one URL and one API key. It is the search-side counterpart to the [NNTP proxy](nntp-proxy.md): the proxy hands other apps your provider pool, this hands them your indexer pool.

## Settings

Configured in **Integrations** (under Settings) → **Newznab Endpoint**, or via the `NEWZNAB_*` environment variables (see [Configuration](configuration.md#newznab-endpoint)). Changes apply live, without a restart.

| Setting | Default | Meaning |
|---|---|---|
| Enable Newznab Endpoint | off | Turn the endpoint on or off |
| API Key | generated | The only credential the endpoint accepts |

Notes on the settings:

- **Off means gone, not refusing.** A disabled endpoint answers 404 to everything, the same as if the feature had never been built.
- **A key is generated on first start**, so the endpoint is never accidentally open. The button beside the field rolls a new one (after a confirmation), and you can paste your own instead. Either way the old key stops working immediately, wherever it has been pasted — clients keep it until you update them.
- **Clearing the field generates a key rather than removing it.** An endpoint with no credential would be one anybody who reaches the port could search, so blank is never a saved state; if it somehow is, every request is refused.
- **The key is the whole credential.** Unlike the rest of StreamNZB, this endpoint is not addressed by stream token — Newznab clients can only send an `apikey` parameter, so that is what it checks. Anyone holding it can search every enabled indexer, so treat it like a password.
- The endpoint rides on the addon's own listener, so there is no separate host or port to set. If the addon is reachable from the internet, so is this — see [Remote access](remote-access.md).

## Client configuration

The **Endpoint URL** is shown at the top of the card, ready to copy:

```
http://<host>:<port>/newznab/api?apikey=<your key>
```

In your client, add an indexer of type **Newznab** and fill in:

| Field | Value |
|---|---|
| URL | `http://<host>:<port>/newznab` |
| API Path | `/api` (the default) |
| API Key | the key from the settings card |
| Categories | whatever you want, from the categories the caps report |

Hit **Test**: the client fetches `t=caps`, which is where the categories and search functions below come from.

Every enabled indexer answers. Per-stream indexer selections do not apply here — the endpoint has one credential, not one per stream, so it searches the whole set.

## Capabilities

`t=caps` is assembled from the capabilities StreamNZB fetched from each configured indexer, not from a fixed list:

- **Categories** are the union of every indexer's categories. Ids that belong to the Newznab standard tree are named from it, so a merged tree reads the same regardless of which indexers are configured; non-standard ids an indexer publishes are carried through under their own names. A subcategory published without its parent is filed under the parent it belongs to.
- **Search functions** (`search`, `tv-search`, `movie-search`, `audio-search`, `book-search`) are available when any indexer offers them, and their `supportedParams` are the union of what the indexers accept — narrowed to the parameters this endpoint actually forwards, so nothing is advertised that would be silently dropped.
- **Limits** take the largest page size on offer, and **retention** the longest.
- When no indexer published caps at all — none configured, or none that answered `t=caps` — the full Newznab standard category tree is advertised so clients still have something to map categories against.

Refreshing capabilities in **Settings → Indexers** also refreshes what this endpoint reports.

## Supported functions

| `t=` | Notes |
|---|---|
| `caps` | Merged capabilities, always XML |
| `search` | Text or plain listing (no query = the indexers' latest) |
| `tvsearch` | `q`, `season`, `ep`, and any id parameter |
| `movie` | `q`, `imdbid`, `tmdbid` |
| `music`, `book` | Forwarded as-is; useful only if an indexer serves those categories |
| `get` | Downloads the NZB behind a result |

`o=json` is honoured for search results. Capabilities are always XML — no client reads them any other way.

## Behavior

- **Queries are forwarded verbatim.** The function and parameters a client sends reach each indexer unchanged, including `season`/`ep` on a text `tvsearch`. This is deliberately different from how the stream pipeline builds indexer queries: a Newznab client parses release titles itself and expects to see what it asked for.
- **Results are merged and deduped, not ranked.** Nothing here filters by quality, validates articles or scores releases — that machinery serves playback. The feed is what the indexers returned, deduped across them and ordered newest first.
- **Ids are forwarded, never translated.** The caps advertise whichever id parameters your indexers publish, so an indexer claiming `rid` (TVRage) or `tvmazeid` makes that id appear on the endpoint too — StreamNZB itself only models IMDb, TVDB and TMDB and cannot convert one id into another. Each indexer is only sent an id its own caps claim: an indexer given an id it does not understand does not fail, it ignores the parameter and answers with its latest listing, which would reach you as results you never asked for. A query that also carries text is always forwarded, since the text search still means what it says.
- **Per-indexer switches still apply.** An indexer with id search disabled is not asked an id search, one with text search disabled is not asked a text search, and content scope (anime / non-anime) is honoured — a query naming category 5070 counts as anime. A query with neither text nor ids is a listing and goes to every indexer.
- **Download links point back at StreamNZB.** Each result's `link` and `enclosure` are rewritten to a `t=get` URL on this endpoint, with the origin sealed inside an encrypted id. Your indexers' own API keys are never handed to the client. Grabs are fetched through the indexer that published the release, so its daily download budget and grab User-Agent still apply.
- **Rotating the admin token invalidates outstanding download links.** The sealing key is derived from it. Clients simply re-search; nothing else is affected.
- **`total` is best effort.** A fan-out cannot know the size of the whole result set, so the response reports what has actually been served up to that page rather than a number that would make clients page into nothing.

## Limits

- The endpoint searches; it does not download media. A client that grabs a release still needs a download client of its own — point it at [SABnzbd or NZBGet backed by the NNTP proxy](nntp-proxy.md) to keep everything in one place.
- Paging past the first page is approximate: `offset` is forwarded to every indexer, and each answers it independently.
- Indexer API-hit and download budgets are shared with the addon. A client that searches aggressively spends the same quota playback does; the **Indexers** card on the dashboard shows what is left.
