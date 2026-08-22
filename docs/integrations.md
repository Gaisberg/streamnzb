# Integrations

Everything StreamNZB knows about Usenet — your providers, your indexers, your filter and ranking rules — is configured once and then re-served to whatever else you run. This page is the whole picture in one place; each surface has its own reference page for the details.

```
                      indexers ──┐
                                 ├── StreamNZB ──┬── Stremio          (addon: catalogs + streams)
                     providers ──┘               ├── Prowlarr / *arr  (Newznab API)
                                                 └── SABnzbd / NZBGet (NNTP)
```

Adding a provider, rotating an indexer key or changing a filter is one edit in one place. Nothing downstream needs reconfiguring, and none of those apps ever holds your indexer API keys or your provider password.

## Stremio

The addon itself. A stream manifest carries whatever providers, indexers, search queries and filter profiles you bound to it, and — if you bind a metadata profile — the catalogs, search, title pages and air dates too, with no Cinemeta and no companion addons.

Multiple manifests run from one instance, each with its own resources, display language and rating cap, so one server can serve a household with different rules per device. See [Stream model](stream-model.md) and [Metadata & catalogs](metadata.md).

## Prowlarr, Sonarr, Radarr

StreamNZB re-serves **every indexer you have configured** as a single Newznab API. Instead of adding each indexer to each app, you add StreamNZB once.

Turn it on in **Integrations** (under Settings) → **Newznab Endpoint**, then add it in your client as a **Generic Newznab** indexer:

| Field | Value |
|---|---|
| URL | `http://<host>:<port>/newznab` |
| API Path | `/api` (the default) |
| API Key | the key from the settings card |

Hit **Test**. The client fetches `t=caps`, and the categories and search functions it offers are assembled from what your indexers actually published — not a fixed list.

What this buys you:

- **One indexer entry instead of *n*.** Add or remove an indexer in StreamNZB and every connected app sees the change immediately.
- **Your indexer keys stay here.** Result download links point back at StreamNZB with the origin sealed inside an encrypted id, so grabs are fetched through the indexer that published the release — its budget and grab User-Agent still apply — without that indexer's key ever reaching the client.
- **One place to see the damage.** Indexer API-hit and download budgets are shared with the addon, and the **Indexers** card on the dashboard shows what is left.

Full reference, including the supported functions and what is deliberately *not* done (no ranking, no filtering — that machinery serves playback): [Newznab endpoint](newznab.md).

## SABnzbd, NZBGet

The download-client half. StreamNZB's NNTP proxy hands other apps **your whole provider pool**, with the same multi-provider failover the addon uses.

Turn it on in **Integrations** → **NNTP Proxy Server**, then point the downloader at it as if it were a provider:

| Field | Value |
|---|---|
| Host | the machine running StreamNZB |
| Port | `1119` (the default; not 119 — see the reference page) |
| SSL/TLS | off — the local hop is plain TCP |
| Username / Password | your proxy credentials, or blank if none are set |
| Connections | below your total provider connections, so downloads leave headroom for streams |

Keep that hop on your LAN or VPN. It is not HTTP, so an HTTP reverse proxy cannot carry it, and it should never be exposed to the internet — see [Remote access](remote-access.md).

Full reference, including failover behaviour and the performance notes that matter for a remote downloader: [NNTP proxy](nntp-proxy.md).

## AIOStreams

If you already run [AIOStreams](https://github.com/Viren070/AIOStreams), StreamNZB can be added to it as a preset while continuing to serve everything above. See [Using with AIOStreams](aiostreams.md).

## Putting it together

The two endpoints are complements: the Newznab endpoint is your indexers, the NNTP proxy is your providers. Enable both and an *arr stack runs entirely off one StreamNZB — Prowlarr searches through it, SABnzbd downloads through it, Stremio streams through it, and the credentials for all of it live in one `config.json` on one host.

Both are off by default and independent; turn on only what you need.
