# Provider speed test

Each provider card in **Settings → Providers** has a speed test (the gauge icon). It measures two things:

- **Test connection** — dials, authenticates, then times a single `DATE` round-trip. The timer starts only once the connection is established, so the number is server responsiveness, not handshake overhead.
- **Speed test** — downloads real articles at 1, 2, 4, 8 … up to your configured connection count and reports throughput plus time-to-first-byte at each step. The point is the *knee*: the smallest connection count that already reaches peak speed, which you can apply back to the provider with one click. Results also translate into playback terms — the cheapest connection count that sustains 720p, 1080p and 4K (remux-class bitrates, plus 25% headroom for peaks and seeks), and how many concurrent streams the peak covers.

By default it downloads a fixed public test NZB, which keeps results comparable between providers; switching the source to **My library** uses your most recent stored release instead, for articles of realistic age. Providers are tested one at a time — they share one uplink — and the downloaded bytes count against your account's usage like any other download.

Both tests draw on the same connection allowance as playback: every connection StreamNZB opens to an account — streaming, speed test, connection test, settings validation or a health probe — is counted against that provider's configured connection count, so the total never exceeds what you configured. A speed test run while something is playing therefore waits for connections to free up rather than opening extra ones, and its numbers will reflect the sharing; run it while idle for a clean measurement.

## Ceilings

Ceilings are deployment-level and env-only. The byte ceiling is shared across the ramp with half of it reserved for the final step — that step measures your configured connection count, and it is also the fastest, so an equal split would starve exactly the measurement the report is built on. Steps the ceiling ended early show their actual window length next to the speed; anything under 1.5 s is flagged and left out of the peak. The default 4 GiB covers a full-length ramp up to roughly 2 Gbit; past that, raise it (a run costs about your line rate × 30 s) or accept shorter windows on the top step.

```env
STREAMNZB_SPEEDTEST_NZB_URL=https://sabnzbd.org/tests/test_download_10GB.nzb
STREAMNZB_SPEEDTEST_MAX_BYTES=4294967296
STREAMNZB_SPEEDTEST_MAX_SECONDS=60
STREAMNZB_SPEEDTEST_STEP_SECONDS=6
```

## Article pipelining

A segment fetch normally costs one idle round trip per article: the `BODY` for the next one is only sent once the current reply has finished arriving, so the connection sits silent for an RTT between articles. Read-ahead can instead hand a connection several segments at once and let the provider start the next article while the current one is still draining.

Depth and connection count are substitutes, not additions — a read-ahead window is a fixed number of segments, so putting three of them on one connection covers it with three times fewer sockets, and most providers throttle per connection. StreamNZB therefore only pipelines what does not fit: while the stream still has idle connections, every segment gets one to itself exactly as before. Batching starts when connections run out — a per-stream cap, several streams sharing one account, or read-ahead falling behind the read pointer — which is precisely when an extra connection was not available to spend instead.

Anything a batch cannot deliver (a missing article, a connection dropped mid-batch, a provider that never had it) falls back to the ordinary per-segment path, where provider failover and retries live. A batch talks to one provider only; it never decides a segment is missing.

What it is worth depends on the round-trip time to your provider and the bandwidth of one connection — roughly `1 + RTT / (article ÷ per-connection bandwidth)`, measured on a 768 KB article:

| Per-connection bandwidth | 30 ms RTT | 80 ms RTT |
|---|---|---|
| 25 Mbit/s | +12% | +31% |
| 100 Mbit/s | +46% | +117% |

Depth 2 covers a 30 ms link; depth 3 covers 80 ms even on a fast connection, and past that extra depth adds nothing but bytes already committed to a connection when the viewer seeks. The default is 3.

The speed test works this out for you. Its one-connection step measures both halves of an article's cost — the whole per-article time, and the part of it (`TTFB`) spent waiting for the article to start arriving — and pipelining hides exactly that waiting part. The suggested depth is `mean / (mean − TTFB)` rounded up: one request outstanding per unit of speedup. It appears as **Articles/request** in the speed test readout, with the predicted per-connection gain and a one-click apply, next to the suggested connection count.

Two things it will not do. It stays silent when the run had no usable one-connection step (a quick test, or a step the byte ceiling cut short) rather than guessing from a noisy measurement. And the gain it predicts is **per connection** — it is what this provider is worth once read-ahead has run out of connections, not a promise about a stream that still has spare ones.

Because the useful depth follows the round-trip time to one particular server, it is set **per provider** — a nearby primary and an overseas backup rarely want the same number. Each provider card in **Settings → Providers** has **Articles per request** next to the connection count:

- **Default** — inherit the deployment default. Leave it here unless this provider's latency differs from your others.
- **Off** — never pipeline against this provider. The escape hatch for a server that mishandles more than one outstanding command; the symptom is stalled or corrupt playback that clears up once it is off.
- **2–8** — pin a depth for this provider alone.

The deployment default that "Default" resolves to, and the value for providers bootstrapped from the environment:

```env
STREAMNZB_NNTP_PIPELINE_DEPTH=3
PROVIDER_1_PIPELINE_DEPTH=2
```

Setting the deployment default to `1` switches pipelining off everywhere except providers that pinned their own depth. Values above 8 are clamped.
