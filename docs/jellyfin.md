# Jellyfin endpoint

StreamNZB can present itself as a Jellyfin server. A Jellyfin client — Swiftfin,
Infuse, Findroid, the Android TV app — adds the StreamNZB URL as a server,
signs in, and browses and plays the same catalogs the Stremio addon serves.

It is a translation layer, not a second media server: every library, detail
page and play request maps onto what the addon already answers. Nothing is
scanned, nothing is stored on disk, and no separate library needs setting up.

## Enabling it

**Settings → Integrations → Jellyfin Endpoint** has the switch and the server
URL to hand the client. It rides on the addon listener, so it needs no port of
its own:

```
https://your-streamnzb-host/jellyfin
```

While the switch is off the endpoint answers nothing at all — a client probing
the URL sees what it would see had the feature never been built.

`JELLYFIN_ENABLED=true` sets the switch from the environment; like every env
override it wins over the dashboard on restart.

## Signing in

A login is a stream:

| Client field | What to enter |
|---|---|
| Username | the stream name |
| Password | the stream's Jellyfin password, or its token |

Set a password per stream under **Settings → Streams**, next to that stream's
manifest URL. It exists because a token is 64 characters and a TV remote is a
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
  from the same metadata providers the addon uses.
- **Search** — the client's search box runs the addon's search carriers
  (TMDB movies, TMDB series, Kitsu anime) in parallel.
- **Images** — posters and backdrops redirect to the provider's CDN, the same
  URLs Stremio is given. They are never proxied through StreamNZB.
- **Playback** — pressing play searches, ranks and returns every candidate
  release as a *media source*, best first. The client's media-source picker is
  therefore the stream list, and switching source is switching release. At
  most 20 sources are offered per play (`JELLYFIN_MAX_PLAYBACK_SOURCES`); some
  clients' pickers break on longer lists.
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

## Notes

- **Changing the server id makes every client forget its login.** The id is
  generated once on first start and never rotated; it is what clients key their
  saved server and its state by.
- Clients that prefix routes with `/emby` are answered too — those are the same
  routes under an older name.
