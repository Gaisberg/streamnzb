# AvailNZB

[AvailNZB](https://check.snzb.stream) is a community availability database. StreamNZB doesn't download or validate NZBs before showing results — it builds an ordered play list from indexer search plus AvailNZB (skipping releases already reported bad), then tries on play. Success/failure is reported so the shared DB stays current. Reports key on the details URL of the NZB that was played, so when several indexers' copies of one release have been merged into a single result, a verdict retires that copy and leaves the others playable — see [Same-release variants](search-queries.md#same-release-variants).

## Opt-in

AvailNZB is **off by default**. While it is off StreamNZB makes no outbound request to it at all — no availability lookups, no playback reports, and no API-key registration. A fresh install never contacts the service until you turn it on.

Turning it on in **Advanced** registers an anonymous API key with AvailNZB and starts using the integration immediately — no restart. The key is generated for you and is shown, read-only, under the same setting. Turning it back off stops all contact again.

Upgrading installs keep whatever they had: an existing config that already had AvailNZB enabled stays enabled.

## Dead-release filtering (Warden)

Community Warden lists record releases that have been taken down at a backbone. AvailNZB subscribes to those lists on your behalf and folds the verdict into the availability answers StreamNZB already asks for — there is no separate setting, no list to download, and no extra request. Turning AvailNZB on is what turns this on.

To match a release against a list, AvailNZB needs a fingerprint, which it mints from the release's usenet poster and post date. StreamNZB reads both from the indexer (the newznab `poster` and `usenetdate` attributes) and includes them in the playback reports it already sends; AvailNZB does the fingerprinting itself. Availability responses then carry a `warden` verdict, and `available` already accounts for it — a release dead across every backbone you can reach reports as unavailable and drops out of the play list like any other bad release.

Verdicts are scoped to the Usenet providers you have configured. StreamNZB sends your provider hostnames with each lookup and AvailNZB matches them against its own exact-host map, so reseller subdomains resolve to the right backbone — `newshosting.tweaknews.eu` is Omicron, not Base IP, which a root-domain guess gets wrong. A release dead only on a backbone none of your providers reach is left alone.

Two things to expect:

- **Coverage starts at zero and grows.** A verdict needs a release AvailNZB holds a fingerprint for, which only happens once somebody has played and reported that release since this shipped. Releases AvailNZB already knew about cannot be backfilled — it holds no indexer credentials and so cannot look up their poster or date.
- **Library replays are covered too.** The poster and post date are stored alongside the library entry, so re-watching something you already played still reports with a fingerprint. Entries saved before this upgrade have neither stored; they gain the pair the next time the release is seen in an indexer search.
- **Sources that report neither attribute never gain a verdict.** Easynews has no equivalent of the newznab attributes, and library results carry neither. Those releases are treated exactly as they are today. A missing verdict always means "nothing known", never "bad".

## Where it is controlled

AvailNZB is controlled at two levels:

- **Global** in **Advanced** (under Settings)
- **Per stream** in **Streams → Add/Change → General**

AvailNZB is only used when both the global setting and the stream setting allow it.
