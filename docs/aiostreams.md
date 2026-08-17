# Using with AIOStreams

StreamNZB works directly out-of-the-box with Stremio using its own built-in release parsing and filter profiles. If you use [AIOStreams](https://github.com/Viren070/AIOStreams), you can also add StreamNZB as an addon preset to consolidate streams alongside other addons.

## Setup

1. In StreamNZB, create or choose the stream you want AIOStreams to use.
2. Copy that stream's manifest URL (for example `https://your-host:7000/<token>/manifest.json`).
3. In AIOStreams, add the StreamNZB preset and paste the manifest URL.
4. **No Usenet service required in AIOStreams** — StreamNZB handles all Usenet provider connections, NZB fetching, and streaming internally. Skip the AIOStreams Usenet service configuration entirely.
5. Optionally configure additional filtering, sorting, or formatting rules in the AIOStreams UI if desired.
