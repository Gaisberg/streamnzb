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
- **Indexer Mode** — `Combine` (parallel query) or `Failover` (sequential).
- **Search requests mode** — `Combine all` (parallel, merged) or `Stop after first hit` (sequential fallback) — see [Search requests](search-queries.md).
- **Skip unaired episodes** — Answer with no results instead of asking this stream's indexers for an episode that has not aired. It is per stream because how early a release lands depends on which indexers the stream asks: a stream pointed at a fast scene can turn the gate off while the rest keep it. The gate uses the exact broadcast time where a source knows one and the whole of the air date where it only knows a date, read in the server's own timezone, and any lookup that fails searches as normal. Skipped searches show as **Not aired yet** in History.
- **Results Mode & Limit** — Whether each release is listed separately or collapsed into one combined entry, and the maximum release count returned to Stremio. Resolution ordering lives in the filter profile, on its Order tab — see [Filters & ranking](filters.md).
- **Filter Profiles** — General and per-kind (Movies, Shows, Anime films, Anime shows) release filter bindings powered by `jhin` — see [Filters & ranking](filters.md).
- **Metadata Profile** — Which catalogs, display language and rating limit the stream serves. None means the classic stream-only manifest — see [Metadata & Catalogs](metadata.md).
- **Format Profile** — How the stream's results render in Stremio. None means the built-in format — see [Custom result formats](result-formatting.md).
- **Failover & AvailNZB** — Automatic stream fallback walking and community availability checking. See [AvailNZB](availnzb.md).

This architecture allows running multiple distinct Stremio manifests from a single StreamNZB instance, each tailored with different search rules, filters, or provider selections.
