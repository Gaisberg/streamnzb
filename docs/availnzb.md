# AvailNZB

[AvailNZB](https://check.snzb.stream) is a community availability database. StreamNZB doesn't download or validate NZBs before showing results — it builds an ordered play list from indexer search plus AvailNZB (skipping releases already reported bad), then tries on play. Success/failure is reported so the shared DB stays current.

## Opt-in

AvailNZB is **off by default**. While it is off StreamNZB makes no outbound request to it at all — no availability lookups, no playback reports, and no API-key registration. A fresh install never contacts the service until you turn it on.

Turning it on in **Advanced** registers an anonymous API key with AvailNZB and starts using the integration immediately — no restart. The key is generated for you and is shown, read-only, under the same setting. Turning it back off stops all contact again.

Upgrading installs keep whatever they had: an existing config that already had AvailNZB enabled stays enabled.

## Where it is controlled

AvailNZB is controlled at two levels:

- **Global** in **Advanced** (under Settings)
- **Per stream** in **Streams → Add/Change → General**

AvailNZB is only used when both the global setting and the stream setting allow it.
