# Easynews advanced search

Easynews indexers search the `3.0` API (`/3.0/api/search`); NZB creation has no
`3.0` equivalent and is the one call still made against `2.0`.

Searches run in Easynews' advanced mode by default, which filters server-side
so junk never reaches StreamNZB's own filters: `spamf` drops posts Easynews has
flagged as spam, and `fex` limits results to the video containers StreamNZB
would accept anyway. These are deployment-level and env-only — not in the
settings UI.

```env
STREAMNZB_EASYNEWS_ADVANCED_SEARCH=false
STREAMNZB_EASYNEWS_SPAM_FILTER=false
STREAMNZB_EASYNEWS_FILE_EXTENSIONS=mkv,mp4,avi
```

Turning advanced search off disables all three. The spam filter follows advanced
search unless set explicitly. The extension list is comma-separated without
dots; leaving it unset sends StreamNZB's own accepted-container list, so the
server-side and client-side filters cannot drift apart.

## Result pages

The `3.0` API returns at most 100 results per page. A search fetches the first
page, then the further pages it still needs (up to five pages in all), with at
most two Easynews search requests in flight per indexer. That cap is shared by
every search running at the time, so simultaneous searches queue rather than
multiply the load on your account. Results keep Easynews' page order whichever
request finishes first.

A search request's **Limit** (or the indexer's own result limit) bounds how
many pages are fetched: a limit of 100 or less costs one page, 250 costs three.
Results the client filters out (samples, password-protected or too-short posts)
don't count toward it, so a short page is topped up from the next one. Queries
proxied through the [Newznab endpoint](newznab.md) always get every page,
because that endpoint applies the caller's limit itself after sorting by date.

The **API hits** budget counts searches, not pages: a search spends one hit
however many pages it fetches, as Easynews searches always have, and a
credential check spends one. Unlike a newznab indexer, Easynews gives StreamNZB
no API quota headers to mirror, so the budget is simply your cap on searches
per day. The
**requests per second** setting, by contrast, is about load, and every page
request waits its turn under it. If a later page fails or times out, the pages that
did answer are still returned, but they are kept out of the per-indexer query
cache, so the next search that reaches Easynews asks again rather than reusing
a partial answer. A Stremio stream's own result list is cached separately, for
the session lifetime, and holds whatever the search returned.
