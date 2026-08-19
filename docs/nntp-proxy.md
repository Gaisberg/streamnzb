# NNTP proxy

StreamNZB includes an NNTP proxy so other apps (SABnzbd, NZBGet) can use it as a local news server. Downloads go through the same provider pool as the addon, so your provider credentials live in one place and every app benefits from the same multi-provider failover.

## Settings

Configured in **General** (under Settings) → **NNTP Proxy Server**, or via the `NNTP_PROXY_*` environment variables (see [Configuration](configuration.md#nntp-proxy)). Changes apply live, without a restart.

| Setting | Default | Meaning |
|---|---|---|
| Enable NNTP Proxy | on | Turn the proxy listener on or off |
| Bind Host | `0.0.0.0` | Which local address to listen on |
| Port | `119` | The port clients connect to |
| Proxy Username / Password | blank | Optional credentials clients must present |

Notes on the settings:

- **Set both credentials or neither.** With both blank, no authentication is required. A username without a password is a lock with no key — no client will be able to authenticate.
- The credential fields read back as blank after a reload — they are redacted from API responses, not lost.
- On Linux/macOS native installs, port 119 is privileged; run as root, grant `CAP_NET_BIND_SERVICE`, or pick a port above 1024. In Docker (where the app runs as root) just map the port: `"119:119"`.

## Client configuration

Point SABnzbd/NZBGet at StreamNZB as if it were a Usenet provider:

- **Host**: the machine running StreamNZB; **Port**: 119 (or your configured port)
- **SSL/TLS: off** — the listener is plain TCP. Connections *upstream* to your providers still use SSL as configured per provider; only the local hop is unencrypted. Keep that hop on your LAN or VPN and never expose the proxy port to the internet — see [Remote access](remote-access.md) (an HTTP reverse proxy cannot carry it; it is not HTTP).
- **Username/Password**: your proxy credentials, or blank if none are set
- **Connections**: each in-flight article borrows a real connection from your providers, and the proxy shares that pool with Stremio playback. Asking the client for more connections than your providers allow does **not** open more upstream connections — the surplus queues for a free slot, so it buys no speed. Set the client's connection count below your total provider connections so downloads leave headroom for streams.

## Behavior

- **Download-only, by message-ID.** The proxy serves `ARTICLE`, `BODY`, `HEAD` and `STAT` by message-ID — exactly what NZB-driven downloaders use. Posting is not supported, and group browsing/header overviews (`XOVER`, article numbers) are not implemented, so it is not a server for newsreaders.
- **Failover across providers.** Each article command tries your enabled providers in priority order and only reports "no such article" after all of them miss. Success and failure feed the same provider health tracking the addon uses.
- **Saturation is transient, not a missing article.** When every provider connection is busy, the command waits up to 30 seconds for a slot and then answers `400` and closes that connection, which clients retry. It is never reported as "no such article", so an oversubscribed proxy cannot make a downloader mark good articles bad or trigger a needless repair.
- **Occasional connection drops are by design.** Article bodies are streamed straight through; if a provider fails mid-article, the proxy cannot splice in another provider without corrupting the stream, so it drops that client connection and the downloader re-requests the article (which then starts on the next provider).
- Changing proxy settings or providers restarts the listener, briefly dropping active connections.
- Active proxy connections appear on the dashboard's session list, grouped per client.

## Performance

Article bodies are relayed straight through, so proxy throughput is bounded by whichever hop is slowest: your providers, the machine running StreamNZB, or the link between StreamNZB and the client.

- **Keep the client on the same network where you can.** Pointing a remote SABnzbd at the proxy sends the whole download through StreamNZB — provider → StreamNZB → client — so the host pays for the traffic twice (in and out) and the client can never go faster than the host's uplink.
- **A subnet router is the slowest way to reach the proxy.** If the client gets to StreamNZB through a Tailscale subnet router (or any router-hosted VPN) rather than being a VPN node itself, that router encrypts and forwards every article for every device, usually on a CPU far weaker than either endpoint. Installing the VPN client on the downloader's own host removes the router from the path entirely and is normally the single largest win. Where the subnet router has to stay, Tailscale's Linux forwarding tuning (`ethtool -K <iface> rx-udp-gro-forwarding on rx-gro-list off`) is worth applying, and the router's CPU is the first thing to watch during a download.
- **Over a VPN (Tailscale, WireGuard), check that the tunnel is direct.** A relayed tunnel adds latency and is rate-limited, which shows up as a few MB/s and a host that stops responding while a download runs. `tailscale status` marks a peer `relay` when traffic is going through DERP instead of peer-to-peer; allowing inbound UDP (port 41641) on the host's firewall *and* its cloud security rules is what usually restores a direct path.
- **Cap the client's download speed if the host becomes unreachable during downloads.** A saturated uplink starves everything else sharing it, including SSH and management UIs. Limiting the downloader to roughly 80% of the link keeps the host responsive.
