# Stream Model

StreamNZB separates global configuration from per-stream behavior:

- **General** — Base URL, port, NNTP proxy, User-Agent headers, and database backend.
- **Metadata** — Catalogs and their order, per-media-type metadata sources, TVMaze air dates, and TMDB/TVDB API keys.
- **Indexers** — Global registry of Newznab and EasyNews search sources.
- **Providers** — Global registry of Usenet provider server connections.
- **Filters** — Release filtering rules and `jhin` ranking profiles — see [Filters & ranking](filters.md).
- **Search** — Reusable movie and TV search requests — see [Search requests](search-queries.md).
- **Streams** — Configured Stremio addon manifests (`<token>/manifest.json`). A stream can be renamed at any time; manifest URLs are built from the token, not the name, so an already-installed addon keeps working, and the stream's playback history moves with it.

## Per-stream configuration

Each stream configuration defines:

- **Addon Name** — Optional override for the name this stream reports to clients. Blank shows `StreamNZB · <stream name>`; setting it replaces that label entirely, so a stream can appear as, say, `Usenet 4K` in the client's addon list *and* on every result it returns (including the `{{.Service}}` template variable). Clients cache the manifest, so an already-installed addon keeps its old name until it is reinstalled — results relabel immediately.
- **Resource Selections** — Which providers, indexers, search queries, and filter profiles are active for the manifest.
- **Per-provider enable/disable** — Turn a provider off for one stream without removing it from the list. Because it stays a member, the choice survives automatic sync, which owns membership rather than intent. At least one provider must stay enabled.
- **Per-provider connection caps** — Optionally limit how many of a provider's connections this stream may hold during playback, so one manifest cannot monopolise the account. It is a ceiling, not a reservation: it stops a stream taking everything, but does not hold connections back for anyone else. The [provider speed test](speed-test.md) tells you the floor — the connection count each resolution needs.
- **Indexer Mode** — `Combine` (query all, merge) or `Failover` (walk them in order until one answers).

  It defaults to *Combine*, which runs its queries in parallel but waits for
  all of them, so a search costs the slowest indexer on the slowest attempt —
  see [How long a search takes](search-queries.md#how-long-a-search-takes)
  for what that is worth and how to trade it.
- **Search requests** — Which movie and TV [search requests](search-queries.md) the stream runs. Every selected request runs and the results are merged; a new stream starts with all of them selected. When to stop, and in what order to ask, is each request's own setting.
- **Skip unaired episodes** — Answer with no results instead of asking this stream's indexers for an episode that has not aired. It is per stream because how early a release lands depends on which indexers the stream asks: a stream pointed at a fast scene can turn the gate off while the rest keep it. The gate opens the moment the air date begins anywhere on Earth (midnight at UTC+14, so up to 14 hours before midnight UTC on that date), which is the earliest instant a broadcast — and therefore a release — can exist. A known broadcast time is reported, never used to hold the gate shut, so no release can sit on an indexer while the gate still calls the episode unaired. Any lookup that fails searches as normal. Skipped searches show as **Not aired yet** in History.
- **Results Mode & Limit** — Whether each release is listed separately or collapsed into one combined entry, and the maximum release count returned to Stremio. Which releases are offered, and the order they come in, lives in the filter profile — see [Filters & ranking](filters.md).
- **Filter Profiles** — General and per-kind (Movies, Shows, Anime films, Anime shows) release filter bindings powered by `jhin` — see [Filters & ranking](filters.md).
- **Metadata Profile** — Which catalogs, display language and rating limit the stream serves. None means the classic stream-only manifest — see [Metadata & Catalogs](metadata.md).
- **Format Profile** — How the stream's results render in Stremio. None means the built-in format — see [Custom result formats](result-formatting.md).
- **Failover & AvailNZB** — Automatic stream fallback walking and community availability checking. See [AvailNZB](availnzb.md).
- **Same-release attempts** — Several indexers listing the same release always become one result that keeps the other copies. This caps how many of those copies playback tries before moving on to a different release: `Merge only` (the default) never switches copies, `All copies` walks every one. See [Same-release variants](search-queries.md#same-release-variants).
- **Preloading** — After a search, the stream's top results are prepared in the background — downloaded, mapped and strictly validated — so the one picked starts almost instantly and provably broken releases are weeded out (and reported) before they are ever played. This sets how many results are preloaded per search; each preloaded result from an indexer spends one API download. `Off` disables it for the stream; the default inherits the deployment-wide count (3). The sweep walks the results in display order and stops early at the first verified candidate — including a [library](database.md) entry with a good verdict, which is already validated and startup-ready, so a library-topped list preloads nothing and spends nothing. A library entry never validated (stored mid-play, status pending) is preloaded like any other candidate, except its NZB comes from the database rather than an indexer API download. Preloading stops the moment a real play starts, so it never competes with an active stream for connections.

This architecture allows running multiple distinct Stremio manifests from a single StreamNZB instance, each tailored with different search rules, filters, or provider selections.

## Per-stream activity on the dashboard

The dashboard's **Network activity** chart plots total provider speed and pool
connections, plus one line per stream. The dropdown at the top right picks which
streams are drawn — all of them by default — and the list underneath groups what
is currently playing by the stream serving it.

A stream's line is measured the same way the total is, by the same meter, on the
same tick: bytes read off the provider connections, charged to the stream that
asked for them. Read-ahead counts, because prefetch is issued through the same
per-stream view of the pool as the read that triggered it. So the stream lines
add up to the total, minus whatever the pool did for nobody in particular —
availability checks, NZB downloads, probes and speed tests.

Two consequences worth knowing:

- A stream's line runs **above** its media bitrate whenever playback is building
  buffer ahead of the player, and drops back toward the bitrate once it is full.
- Segments served from cache cost nothing on the wire, so a stream replaying
  something already fetched can show a flat line while playback continues.
