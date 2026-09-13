# Jellyfin endpoint

StreamNZB can present itself as a Jellyfin server. A Jellyfin client — Swiftfin,
Infuse, Findroid, the Android TV app — adds the StreamNZB URL as a server,
signs in, and browses and plays the same catalogs the Stremio addon serves.

It is a translation layer, not a second media server: every library, detail
page and play request maps onto what the addon already answers. Nothing is
scanned, nothing is stored on disk, and no separate library needs setting up.

## Connecting a client

There is no switch. The authenticated endpoint is always available. It rides
on the addon listener, so it needs no port of its own, and — like a manifest
URL — it answers nothing useful without a stream's credentials:

```
https://your-streamnzb-host/jellyfin
```

## Signing in

A login is a stream. Every stream card under **Settings → Streams** has a
**Jellyfin** panel next to the Stremio manifest, with the three things a
client asks for:

| Client field | What to enter |
|---|---|
| Server | the URL shown (`/jellyfin` on the addon base URL) |
| Username | the stream name |
| Password | the password set in that panel, or the stream's token |

The password exists because a token is 64 characters and a TV remote is a
poor place to type one. It is stored as an argon2id hash and never leaves the
server, so a forgotten password is replaced rather than recovered.

Setting one is optional: a stream's **token also works as its password**, so a
client configured before passwords existed keeps working, and a stream with no
password still has a way in.

This password is only for signing in to a Jellyfin client. **The dashboard
stays admin-only** — a stream password grants nothing there.

The dashboard **admin cannot sign in** to the Jellyfin endpoint. The admin is an
identity rather than a stream: it has no search queries, indexers or filter
profile bound to it, so it could browse catalogs and then fail at playback, the
one thing a media client is for. It is refused at the door instead.

Each stream is its own Jellyfin user: its own libraries (whatever catalogs its
metadata profile enables), its own filters, and its own watch progress. One
stream never sees another's.

## What a client gets

- **Libraries** — one folder per enabled browse catalog, paged as you scroll.
- **Movies and series** — with seasons, episodes, overviews, years and ratings
  from the same metadata providers the addon uses, with cast and crew, photos
  included.
- **Search** — the client's search box runs the addon's search carriers
  (TMDB movies, TMDB series, Kitsu anime) in parallel.
- **Images** — posters and backdrops are fetched from the provider's CDN and
  relayed by StreamNZB, because some clients (Infuse) do not follow redirects
  for artwork. TMDB images are requested at bounded sizes (backdrops 1280px
  wide, posters 780px) and sent with a one-day cache header.
- **Playback** — pressing play searches, ranks and returns every candidate
  release as a *media source*, best first. The client's media-source picker is
  therefore the stream list, and switching source is switching release. Each
  source is labelled with the stream's own [result format](result-formatting.md)
  when one is set, flattened to a single line — the same template that names
  rows in Stremio. At most 20 sources are offered per play
  (`JELLYFIN_MAX_PLAYBACK_SOURCES`); some clients' pickers break on longer
  lists.
- **The version picker** — Clients build their picker from the item document
  rather than from the play request (Infuse and SenPlayer both do), so opening
  a title runs the search then and there. That is `JELLYFIN_RESOLVE_ON_OPEN`,
  and it is **on by default**: it costs one indexer search per title page
  opened, and the alternative is a picker with nothing real in it. Set it to
  `false` if you would rather not spend the search — a title then offers the
  one best release until you press play.
- **List rows** — Infuse's Direct Mode never asks PlaybackInfo: it plays from
  the row's media sources and reads the version count from the list
  document. Every movie and episode row therefore carries two placeholder
  sources (slots 0 and 1), or the resolved list when it is already cached.
  Nothing is searched to build a row; playing a slot the search did not
  fill falls back to the first playable release.
- **Resume** — position and watched state are kept per stream, server-side,
  because Jellyfin clients expect the server to remember rather than keeping
  it locally the way Stremio clients do. 90% in counts as watched.

## What it deliberately does not do

- **No transcoding.** Every media source is offered as direct play. The file is
  served as it is, and the client's decoder decides what it can handle — the
  same principle the Stremio side follows. A device that cannot play HEVC
  10-bit will not be rescued here.
- **No server administration.** Nobody is a Jellyfin administrator, the admin
  included, so clients do not render dashboards, user management or library
  scans that would lead nowhere.
- **No live TV, no sync, no downloads, no plugins.** These are answered as
  empty rather than implemented.

## Failover

When a release stops being playable mid-stream, the endpoint redirects to the
next candidate exactly as the addon does, presenting it as a media-source
switch. The client re-issues its Range request against the new slot rather than
receiving the tail of a different file. The access token is carried in the
redirect URL, since a client that sent it in a header may not replay the header
on a redirect.

## How a player authenticates

The app that fetches the video is not the client that signed in. It is handed a
URL and plays it with no session of its own — no `Authorization` header, no
`api_key` — because neither Jellyfin SDK attaches credentials to a stream URL.

So each media source carries the stream's token in two places, and clients take
whichever they read:

- **`Path`**, the full stream URL, played verbatim by Findroid and by Wholphin.
- **`ETag`**, which Swiftfin and Wholphin copy into the `tag` parameter of a URL
  they build themselves.

Both are accepted on the stream route and nowhere else, so a token lifted off
either one opens a single video and cannot list a library or read progress.

This is why **`addon_base_url` has to be right** for the Jellyfin endpoint, not
just for Stremio: it is the address baked into `Path`. If it points somewhere a
client cannot reach, clients that play `Path` fail while clients that build
their own URL keep working — which looks like "it works on my phone but not on
the TV" rather than like a misconfiguration.

## Notes

- **Changing the server id makes every client forget its login.** The id is
  generated once on first start and never rotated; it is what clients key their
  saved server and its state by.
- **Media source ids are GUIDs.** The Jellyfin API types the field as a plain
  string, but enough clients parse it as a GUID — and crash when it is not one —
  that it is not worth being right about.
- Clients that prefix routes with `/emby` are answered too — those are the same
  routes under an older name.
