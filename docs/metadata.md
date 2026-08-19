# Metadata & Catalogs

StreamNZB is a full metadata provider — your client needs no other addon or application for browsing, search, or title pages. Metadata is configured as **profiles**: each profile is a named bundle of catalogs, sources, display language and an optional rating limit, and every stream binds one profile (or none) on the Streams page.

## Profiles

- **A profile per audience** — Create as many profiles as you like on the **Metadata** page: a full catalog board in English for the living room, a capped kids profile in another language for the tablet. Each stream picks its profile on the Streams page, so two installs of the same server can serve completely different boards.
- **Binding is the enablement** — A stream with **no** metadata profile bound serves streams only: the manifest is byte-identical to the pre-metadata addon, and clients keep using Cinemeta or whatever they already have. There is no separate on/off switch anymore.
- **Migration** — Upgrading from the single global metadata config seeds a **Default** profile from the old settings and binds it to every existing stream, so nothing changes until you say so. If the old master switch was off, the profile is created but left unbound.
- **Renames follow, deletes clear** — Renaming a profile updates every stream bound to it; deleting one unbinds its streams, which fall back to the stream-only manifest.
- `METADATA_ENABLED=false` (env) remains a deployment-level kill-switch that blanks metadata for every stream regardless of bindings.

## What a profile carries

- **Catalogs** — A fresh profile serves one trending row per media type (movies, series, anime). The registry offers 23 catalogs across TMDB, TVDB, Kitsu and local rows (popular, top rated, now playing, upcoming, Popular/New on TVDB, and more): add them from the search dialog, drag to reorder, remove with one click. A title appears only in the highest-ordered catalog that carries it; the row order doubles as the dedup priority.
- **Search** — Always available for every content type, independent of the catalog rows a profile picks. The manifest carries hidden search-only catalogs (their search extra is declared *required*, so clients use them for search but never render them as board rows) — a kids profile built purely from family rows still searches all three types, and search results respect the profile's rating limit like everything else.
- **Family & kids catalogs** — **Family Movies**, **Animated Movies**, **Family Series**, **Kids TV** and **Kids Anime** are filtered at the source (TMDB Discover by genre and US certification, Kitsu by age rating), so they return full pages of actually kid-targeted content instead of general rows thinned out by a cap. They are the intended building blocks for a kids profile: Family Movies carries a built-in PG ceiling and Kids Anime a G/PG one even on uncapped profiles, and a profile's rating limit tightens the source filter further (an All-ages cap turns Family Movies into `certification.lte=G` and Kids Anime into G-only). The general rows (trending, popular, …) can't be pre-filtered upstream, so under a cap they only show whatever happens to pass — expect them to run short on strict caps.
- **Sources** — Series metadata comes from TVDB (or TMDB, per profile), movies from TMDB, anime from Kitsu, with TVMaze as the air-date authority. The per-profile TVMaze toggle governs the air dates *shown on title pages* only; skipping unaired episodes is a per-stream indexer setting (**Streams → edit → Indexers → Skip unaired episodes**) and always uses the best air time it can find, for every stream, whether or not it binds a metadata profile.
- **Language** — Titles, overviews, episode names, logos and catalog rows display in the profile's language. TMDB content localizes fully; TVDB series overlay TheTVDB's translations where they exist; anime keeps its Kitsu titles but picks up a translated description through the anime-lists mapping. Anything without a translation falls back to English. Different streams can run different languages side by side. Clients cache title pages for a few hours, so a language change reaches already-visited titles after their cache expires. Independent of the per-search-query title language, which controls what gets sent to indexers.
- **Rating limit (parental controls)** — Cap the profile at an age certification (All ages / 7+ / 13+ / 16+ / 18+). The cap applies everywhere: capped titles disappear from catalog rows and search, their title pages 404, and playback requests return no streams. Certifications come from TMDB (movies and series), TVDB (series) and Kitsu (anime), preferring the US rating and falling back to the strictest known foreign one.
  - **Unrated content is blocked by default.** Certification data is patchy — niche and foreign titles often have none — and a parental control must fail closed. The per-profile **Allow unrated content** toggle opens that up if the limit proves too aggressive.
  - **"Metadata off" is not kid-safe.** A stream with no profile bound has no cap at all — a kids device needs a capped profile *bound to its stream*.
  - Search caches are cleared when profiles or bindings change, so a tightened cap takes effect immediately on playback; clients may still show cached catalog rows and title pages for up to their cache lifetime.

## Personal rows & artwork

- **Personal rows** — **Continue Watching** and **Because You Watched** are built from each stream's *own* playback history, so every household member with their own stream gets personal rows from the same server.
- **Artwork** — Catalog rows carry landscape backgrounds alongside posters (TMDB backdrops, TVDB fanart, Kitsu covers), so clients that build featured-hero banners from catalog rows render a proper backdrop instead of an upscaled poster. Backgrounds on title pages pick the community's highest-ranked fanart, title logos come from the source itself (TVDB clearlogo, TMDB logo) with Cinemeta's logo CDN as the fallback, and cast lists carry actor photos. Anime title pages upgrade their background and logo through the anime-lists mapping to the matching TVDB/TMDB record, keeping Kitsu's posters.

## API keys

TMDB/TVDB keys are shared by every profile and live in their own section at the bottom of the Metadata page. Built-in fallback keys make this work with zero setup; adding your own raises the ceiling and is recommended for anything beyond personal use.

> This product uses the TMDB API but is not endorsed or certified by TMDB. Series metadata is provided by [TheTVDB](https://thetvdb.com) — consider subscribing to support them.
