# Using with AIOStreams

StreamNZB works on its own — it is a complete Stremio addon with its own release parsing, ranking and filter profiles, and it needs nothing else installed. If you also run [AIOStreams](https://github.com/Viren070/AIOStreams), the two sit together comfortably: AIOStreams consolidates sources into one super-addon, and StreamNZB is both a source it can use and a set of things it does not do.

## What StreamNZB adds

- **Catalogs and metadata, without a second addon.** StreamNZB serves browsable rows, search, title pages, episode lists, air dates and artwork itself. In an AIOStreams setup that role usually falls to a separate metadata addon; here it is the same binary that already holds your Usenet configuration.
- **Per-stream profiles.** One StreamNZB serves several manifests, each with its own providers, indexers, filters, display language and rating cap. A household can run a full English board on the living-room TV and a capped, differently-languaged one on a tablet, from one instance — see [Stream model](stream-model.md).
- **Parental controls that fail closed.** A metadata profile can be capped at an age certification: capped titles vanish from rows and search, their title pages 404, and playback returns nothing. Unrated content is blocked by default, because certification data is patchy and a parental control that guesses is not one — see [Metadata & catalogs](metadata.md).
- **Your indexers and providers, re-served to everything else.** The [Newznab endpoint](newznab.md) hands Prowlarr and the *arr apps every indexer you configured behind one key; the [NNTP proxy](nntp-proxy.md) hands SABnzbd and NZBGet your whole provider pool. Both run off the same configuration the addon uses — see [Integrations](integrations.md).
- **Credentials that stay on the server.** Provider passwords and indexer API keys are never handed to a client, and playback leaves from one IP.

None of that competes with what AIOStreams is for. Run both if you want its aggregation over other sources and StreamNZB's Usenet configuration, catalogs and endpoints underneath.

## Adding StreamNZB as a preset

1. In StreamNZB, create or choose the stream you want AIOStreams to use.
2. Copy that stream's manifest URL (for example `https://your-host:7000/<token>/manifest.json`).
3. In AIOStreams, add the StreamNZB preset and paste the manifest URL.
4. **No Usenet service required in AIOStreams** — StreamNZB handles all Usenet provider connections, NZB fetching, and streaming internally. Skip the AIOStreams Usenet service configuration entirely.
5. Optionally configure additional filtering, sorting, or formatting rules in the AIOStreams UI if desired.

## Which layer should filter?

Both can. StreamNZB's filter profiles and [rules](rules.md) run before results ever reach AIOStreams, and AIOStreams can filter and sort again on top.

Doing it in StreamNZB keeps the decision next to the data — it is the layer that knows what each indexer returned, what validation dropped and why, and it is what the [History](troubleshooting.md) page explains. Doing it in AIOStreams keeps one set of rules across every source it aggregates. Pick per rule rather than per layer; a quality floor belongs here, a cross-source preference belongs there.

If you would rather AIOStreams did the formatting, StreamNZB can import an AIOStreams formatter template directly — see [Custom result formats](result-formatting.md).
