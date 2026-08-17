# Provider speed test

Each provider card in **Settings → Providers** has a speed test (the gauge icon). It measures two things:

- **Test connection** — dials, authenticates, then times a single `DATE` round-trip. The timer starts only once the connection is established, so the number is server responsiveness, not handshake overhead.
- **Speed test** — downloads real articles at 1, 2, 4, 8 … up to your configured connection count and reports throughput plus time-to-first-byte at each step. The point is the *knee*: the smallest connection count that already reaches peak speed, which you can apply back to the provider with one click. Results also translate into playback terms — the cheapest connection count that sustains 720p, 1080p and 4K (remux-class bitrates, plus 25% headroom for peaks and seeks), and how many concurrent streams the peak covers.

By default it downloads a fixed public test NZB, which keeps results comparable between providers; switching the source to **My library** uses your most recent stored release instead, for articles of realistic age. Providers are tested one at a time — they share one uplink — and the downloaded bytes count against your account's usage like any other download.

## Ceilings

Ceilings are deployment-level and env-only. The byte ceiling is shared across the ramp with half of it reserved for the final step — that step measures your configured connection count, and it is also the fastest, so an equal split would starve exactly the measurement the report is built on. Steps the ceiling ended early show their actual window length next to the speed; anything under 1.5 s is flagged and left out of the peak. The default 4 GiB covers a full-length ramp up to roughly 2 Gbit; past that, raise it (a run costs about your line rate × 30 s) or accept shorter windows on the top step.

```env
STREAMNZB_SPEEDTEST_NZB_URL=https://sabnzbd.org/tests/test_download_10GB.nzb
STREAMNZB_SPEEDTEST_MAX_BYTES=4294967296
STREAMNZB_SPEEDTEST_MAX_SECONDS=60
STREAMNZB_SPEEDTEST_STEP_SECONDS=6
```
