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
  wide, posters 780px) and sent with a one-day cache header. A poster the
  advertised URL cannot supply — a `poster_url_pattern` overlay with no image
  for that title, typically — falls back to Metahub's poster for the IMDb id;
  an image nobody has is a 404, never a redirect.
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
- **Scrobbling** — a stream that scrobbles to Simkl or MDBList (see
  [Scrobbling](metadata.md#scrobbling)) reports Jellyfin playback from the
  position the client reports (`/Sessions/Playing`, `…/Progress`,
  `…/Stopped`), not from the bytes served: a player that buffers the whole
  episode in seconds and plays it from memory is still reported where it
  actually is. The client's first report is the start, its stop reports its
  position, and each service then applies its own threshold (80%), separate
  from the 90% above. Each device's play session is reported on its own; a
  device opening a new play session ends its last one, reporting it stopped
  at its last position if it never stopped. Without a runtime to
  place the position (rare: the file's own duration is normally known) the
  stop is not sent rather than guessed. Pausing reports nothing; a
  playback that reports nothing at all for 30 minutes — an app closed
  mid-play — is reported stopped at the last position the client gave, never
  further, and reporting again starts it anew. A client that sends no
  playback reports is scrobbled from the bytes served, but only with a stop:
  a start from the transfer could not be told apart from an earlier
  playback's. (While another device on the same stream reports the same
  title, such a client's transfer is taken for that device's and is not
  scrobbled.) Stremio playback of the same title on the same stream is
  scrobbled from its own transfer as before, whatever a Jellyfin client is
  doing. Marking an item watched or unwatched by hand is not scrobbled.
- **Watched seasons and series** — marking a season or series watched
  (`POST /UserPlayedItems/{id}`, or `/Users/{userId}/PlayedItems/{id}`) marks
  its episodes, as Jellyfin does: every episode that has aired, by the same
  rules Next Up uses, and for a whole series its aired specials too (a special
  counts as aired unless it is dated in the future). Episodes still to air are
  left alone. Episodes already watched keep their date and play count, and
  episodes part-way through are marked watched and leave Continue Watching;
  favourites are kept. Marking unwatched (`DELETE` on the same routes)
  clears every episode in it, progress included. Either way the whole season
  or series is written at once, or not at all. A season or series shows as
  watched once every episode it counts is: its aired episodes, and any other
  already watched, such as one the metadata dates in the future (as Jellyfin
  counts any episode it has a file for, whatever its date). Its unwatched
  count and percentage show on its own page, in its season list and in the
  Favourites row; catalog rows and search results do not count episodes and
  show it unwatched. A season with nothing aired or watched stays unwatched,
  with nothing counted, and marking it changes nothing; Jellyfin calls such a
  season watched, but also hides it, where it is listed here. Marking watched
  needs the title's metadata to know what has aired, so it fails without it:
  502 while the metadata source is unreachable, and 404 while metadata is
  switched off for the stream, as the season's own page is then not found
  either. Marking unwatched does not need it, and neither does a favourite. An unmark that cleared episodes answers at once, the
  season unwatched and uncounted, without waiting on the metadata for the
  counts; its page shows them. While the metadata is unreachable, how many
  episodes a season has is unknown, so a favourite's answer shows the season
  uncounted and unwatched, and a lookup of its UserData answers the same when
  none of its episodes is watched, and otherwise fails (502) rather than
  guess.
  A season the metadata lists no episodes for is not found (404) when
  marking it watched, favouriting it or looking it up, and nothing new is
  stored for it. What a source that has since renumbered left under one can
  still be read and removed: its stored row is answered as stored, unmarking
  clears its stored episodes, and unfavouriting clears its favourite; each
  answers 404 only when nothing was stored. Beyond marking watched with
  metadata switched off, metadata that is unreachable or switched off never
  makes a 404: unmarking and favourites go ahead without it, and a UserData
  lookup answers or fails with 502 as above. Season and episode lists that
  cannot read the stream's play state fail (500) rather than show it wrong.
  `POST /UserItems/{id}/UserData` on a season or series sets its favourite
  and nothing else. Its `Played` is ignored, true or false: clients send
  their whole UserData back with every change, and a stale watched flag would
  otherwise rewrite every episode. So a body with `IsFavorite` succeeds
  (200) and changes only the favourite, even when it also says `Played`; a
  client that means to mark the season watched through this route gets the
  favourite and not the mark, and has to use `/UserPlayedItems`. A body
  without `IsFavorite` is refused (404), as before. (Jellyfin itself never
  marks episodes from this route either.)
- **Next Up** — the episode to watch next in each series the stream has been
  watching, worked out the way Jellyfin works it out: from the furthest
  episode watched, the first one after it that is neither watched nor unaired,
  continuing into the next season. Watching episodes out of order therefore
  goes on from the furthest, not from the gap, and the furthest still counts
  if the metadata no longer lists it. A series with nothing left that has
  aired is not listed. An episode with no air date takes the aired or unaired
  state of the last dated episode before it, so the undated rest of a season
  whose premiere is still to come is not offered. A season that starts undated
  in a series dated elsewhere is the exception, since TMDB and TVDB list an
  announced season before dating it: it counts as unaired unless a later
  episode has aired, so an older undated season stays eligible. A series with
  no air dates at all counts as aired throughout. Series come newest-watched
  first. An episode you are part-way through appears only when it is the one
  to go on with, the first after the furthest watched; a rewatch you left
  half-way is not offered. It carries its position, unless the client asks for
  resumable episodes to be left out (`EnableResumable=false`, which Jellyfin's
  web client sends because it shows those under Continue Watching). Then the
  series is left out rather than skipping ahead. `SeriesId` naming a series
  asks about that one, watched or not — an unwatched one offers its first
  episode — and `ParentId`, `NextUpDateCutoff` and the 100-series limit then
  do not apply. Any other `SeriesId` is ignored, as Jellyfin ignores it.
  `ParentId` naming a library narrows the list to that library's content type,
  since a series does not remember which catalog it was found in. Jellyfin
  narrows Next Up by library only, so any other `ParentId`, a series or season
  included, lists nothing. `NextUpDateCutoff` is honoured, including a time
  with no zone, which is read as UTC. `StartIndex`, `Limit` and
  `EnableTotalRecordCount` are also honoured. Limits:
  - **Specials are never offered.** No metadata source here says where a
    special airs among the regular episodes, which is what Jellyfin places
    them by. A Kitsu film is not offered either: it is shown as a
    one-episode series, but there is nothing to go on to.
  - **Only the 100 most recently watched series are looked at**, since each
    needs its episode list. A series watched less recently than that is not
    listed.
  - **The total can be short of the real count.** Series are looked at only
    until the page is filled, plus one past it, instead of all of them as
    Jellyfin does, so a home-screen row costs about as many episode lists as
    it shows, fetched 8 at a time, rather than a hundred. The total is exact on the last page. Before that,
    it is the episodes found so far, always at least one more than the page,
    and never more than there are. A client paging by it never asks for an
    empty page, but a count shown beside the row can read low.
  - **Rewatching (`EnableRewatching`) is not supported.**
  - **History belongs to the id a series was watched under.** A series
    watched through one metadata source and later browsed through another
    starts over in Next Up. Changing a profile's series source can also
    renumber the episodes behind the same id. A series watched both from an
    anime catalog and a series catalog under the same IMDb id is listed
    twice, once for each.
  - **An anime continues within its Kitsu entry**, since Kitsu lists each
    season or cour as an entry of its own.
- **Upcoming** — the episodes still to air in the series the stream follows,
  soonest first. Jellyfin lists every dated episode in its libraries from
  yesterday on; a catalog here has no end, so Upcoming follows the series the
  stream has watched (an episode watched or part-way through) or marked as a
  favourite — the series itself, not one of its seasons or episodes. Each
  series contributes one episode, the one it is waiting on: the first dated
  episode after the furthest watched that has not aired yet. A series with an
  aired episode still to watch, part-way through included, is Next Up's and
  is not listed, however soon its later episodes air. A favourite never
  watched is listed only while nothing of it has aired, with its premiere.
  Only episodes airing in the next 90 days are listed: a series waiting on
  one further out is left out, and no later episode stands in for it.
  Episodes airing at the same time are ordered by series name. A favourite
  is not history: it does not put a series in Next Up, and unfavouriting a
  series the stream has watched leaves it followed by that history. `ParentId`
  naming a series lists all of its dated episodes still to air in those 90
  days, whether or not the stream follows it, as Jellyfin's series page asks
  for them; a season narrows that to the season. An episode the metadata
  lists twice is listed once. A library narrows the followed series to its
  content type. Any other `ParentId` lists nothing. `StartIndex` and `Limit`
  are honoured. Next to Jellyfin:
  - **The total is exact.** Every followed series is looked at before a page
    is cut, since the list is sorted by air date. Jellyfin reports the size
    of the page it returns, and its clients find the end by a short page,
    which this gives them too.
  - **Each request looks at up to 200 series**: the 100 most recently
    watched, and up to 100 favourites besides, in its own bound so that a
    long history never crowds them out (a favourite watched long ago
    included). Never-watched favourites are taken in a fixed order, the
    favourite itself having no date. A series in both is looked at once, and
    at most 8 episode lists are fetched at a time. Nothing is cached between
    requests beyond what the metadata sources cache themselves, and every
    page asked for looks at them all again.
  - **Undated episodes and specials are never listed**, nor is an episode
    whose date the metadata gives in a form that cannot be read. An episode
    dated without a time counts as airing at midnight UTC that day, so it
    leaves Upcoming (and enters Next Up) from then.
  - **The dates are believed over playback**, as Next Up believes them. A
    series whose watched episode is dated in the future waits on the next
    dated episode, passing undated ones in between; an episode part-way
    through whose date is still to come is listed, with its position.
  - **A request the client abandons part-way answers nothing**, since a
    partial scan cannot say which episodes air soonest.
  - The limits on history and Kitsu under Next Up apply here too.
- **Favourites** — the heart (or watchlist) button on a movie, series, season
  or episode is kept per stream, server-side, alongside the play state, so
  each stream signed in has its own. Favourites follow the stream through a
  rename or a database switch, and are deleted with it. Stream names are
  compared with surrounding spaces trimmed, so a stream named `alice` and one
  named `alice` with a trailing space share one set, play state included.
  Clearing a favourite keeps the item's progress. Both route forms
  (`/UserFavoriteItems/{id}` and `/Users/{id}/FavoriteItems/{id}`) and the
  `IsFavorite` field of `/UserItems/{id}/UserData` are honoured, and the
  item's `UserData` reports `IsFavorite` on its own page and in season and
  episode lists. A `UserData` body applies only the fields that differ from
  what is stored, so a client sending its whole `UserData` back to change the
  favourite leaves the progress and last-played date as they were.
  A Favourites row (`Filters=IsFavorite`, `Filters=IsFavoriteOrLikes` or
  `isFavorite=true`) lists only favourites, filtered by `IncludeItemTypes` and
  paged with `StartIndex`/`Limit`. Under a library's `ParentId` it lists the
  favourites of that library's content type — movie, series or anime — since
  a favourite does not remember which catalog it was found in; an unknown or
  disabled library lists nothing. Under a series' `ParentId` it lists that
  series' favourite seasons and/or episodes as `IncludeItemTypes` names them;
  without it, the seasons, or the episodes with `Recursive=true`. Under a
  season's it lists its favourite episodes. A genre (`GenreIds`, `Genres`, or
  a genre's `ParentId`) keeps the movies and series of that genre, and a
  genre's `ParentId` only those of its content type. Genres are matched by
  their TMDB ids, as a library's genre listing matches them, even when a
  title's metadata comes from TVDB; only a title whose metadata was built
  without TMDB (Cinemeta) is matched by its own genre names. Any other `ParentId`
  lists nothing. The same filters narrow an `Ids` lookup, a `searchTerm` search or
  `Filters=IsResumable` to the favourites among its results that the
  `ParentId` admits, by the same rules; a search only finds movies and series,
  so under a series or season it finds no favourites. An `Ids` lookup with a
  favourites filter returns the Favourites row's own rows, so it never runs
  the search `JELLYFIN_RESOLVE_ON_OPEN` would for a title page. Without a
  favourites filter, `Ids`, `searchTerm` and `IsResumable` ignore `ParentId`
  as before. A favourite whose title no longer
  resolves is left out of both the row and its count, which means every
  favourite's title is looked up (from the metadata cache, for all but new
  titles, and once per series however many of its seasons and episodes are
  favourites) each time the row is listed. The row is in a stable order
  rather than sorted by name, and building it never runs a search. Episodes
  in it carry the same media sources as in their season's list. Catalog grid
  tiles do not carry a favourite flag, so a client that
  marks favourites on its grids shows them only on the title page and in the
  Favourites row.

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
