# Metadata & Catalogs

StreamNZB is a full metadata provider, on by default — your client needs no other addon or application for browsing, search, or title pages.

- **Catalogs** — A fresh install serves one trending row per media type (movies, series, anime), each carrying search. The **Metadata** page offers an 18-catalog registry across TMDB, TVDB, Kitsu and local rows (popular, top rated, now playing, upcoming, Popular/New on TVDB, and more): add them from the search dialog, drag to reorder, remove with one click. Changes save automatically.
- **Personal rows** — **Continue Watching** and **Because You Watched** are built from each stream's *own* playback history, so every household member with their own stream gets personal rows from the same server.
- **No duplicate rows** — A title appears only in the highest-ordered catalog that carries it; your row order doubles as the dedup priority.
- **Sources** — Series metadata comes from TVDB, movies from TMDB, anime from Kitsu (configurable per media type), with TVMaze as the air-date authority. Episodes that have not aired yet answer instantly with no results instead of burning an indexer search, and the empty answer expires exactly at air time.
- **Artwork** — Backgrounds pick the community's highest-ranked fanart, title logos come from the source itself (TVDB clearlogo, TMDB logo) with Cinemeta's logo CDN as the fallback, and cast lists carry actor photos. Anime title pages upgrade their background and logo through the anime-lists mapping to the matching TVDB/TMDB record, keeping Kitsu's posters.
- **Opting out** — Prefer Cinemeta or another metadata addon? Turn the master switch off on the Metadata page and StreamNZB serves streams only, exactly as before.

Built-in fallback TMDB/TVDB keys make this work with zero setup; adding your own keys on the Metadata page raises the ceiling and is recommended for anything beyond personal use.

> This product uses the TMDB API but is not endorsed or certified by TMDB. Series metadata is provided by [TheTVDB](https://thetvdb.com) — consider subscribing to support them.
