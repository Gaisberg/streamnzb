# Troubleshooting

If you're stuck, please either open a [GitHub issue](https://github.com/Gaisberg/streamnzb/issues) or report it in the [Discord](https://snzb.stream/discord) `#help` channel (they sync via [GitThread](https://gitthreadsync.snzb.stream/)). Include downloaded logs when relevant, and include the copied bad match report from **History** when the issue is about a wrong or poor release match. For "why am I getting no (or few) streams", expand the request on **History** — its search panel shows what each indexer returned and what filtering dropped. Sensitive data should be automatically redacted but please double-check before posting.

## Buffering behind Cloudflare Tunnel

Exposing StreamNZB through a Cloudflare Tunnel (`cloudflared`) is not recommended. The playback path streams large amounts of video data continuously, and routing it through the tunnel can throttle sustained throughput and cause unnecessary buffering — even when the server itself is keeping up. If you see buffering that doesn't match the connection speeds on the dashboard, try playing directly against the server (LAN address or a direct reverse proxy) to rule the tunnel out. For remote access, prefer a VPN (e.g. WireGuard/Tailscale) or a plain reverse proxy on a directly reachable host. Proxying large volumes of video traffic through Cloudflare may also violate their terms of service.

## An episode still shows no streams after it aired

Search results are cached per stream and title. A search that found **nothing** is cached for 15 minutes and its deadline is never pushed out by another request, so a title that had nothing behind it a moment ago is re-searched shortly after rather than staying empty for as long as the session cache lives. A search that found results keeps the longer sliding cache.

If it is still empty after that, the air-date gate may be holding it: an episode that has not aired answers with no results without asking any indexer, and **History** shows it as **Not aired yet** with the air time it read instead of a bare "No results". Where a source knows the actual broadcast time the gate uses it; where it only knows a date, the whole of that date counts, read in the server's own timezone — so check the server's clock and timezone first. If the show's air dates simply run ahead of when its releases land, turn **Skip unaired episodes** off in that stream's **Indexers** tab (Streams → edit → Indexers).

## Force password reset on next startup

If you need to force the admin account to land on the password-change screen after restart, set:

```env
ADMIN_FORCE_PASSWORD_RESET=true
```

After the password has been changed, remove or disable this env var.
When it remains enabled, StreamNZB will keep forcing the password-reset prompt on startup.
