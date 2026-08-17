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
