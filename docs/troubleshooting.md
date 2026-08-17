# Troubleshooting

If you're stuck, please either open a [GitHub issue](https://github.com/Gaisberg/streamnzb/issues) or report it in the [Discord](https://snzb.stream/discord) `#help` channel (they sync via [GitThread](https://gitthreadsync.snzb.stream/)). Include downloaded logs when relevant, and include the copied bad match report from **History** when the issue is about a wrong or poor release match. For "why am I getting no (or few) streams", expand the request on **History** — its search panel shows what each indexer returned and what filtering dropped. Sensitive data should be automatically redacted but please double-check before posting.

## Buffering behind Cloudflare Tunnel

Exposing StreamNZB through a Cloudflare Tunnel (`cloudflared`) is not recommended. The playback path streams large amounts of video data continuously, and routing it through the tunnel can throttle sustained throughput and cause unnecessary buffering — even when the server itself is keeping up. If you see buffering that doesn't match the connection speeds on the dashboard, try playing directly against the server (LAN address or a direct reverse proxy) to rule the tunnel out. For remote access, prefer a VPN (e.g. WireGuard/Tailscale) or a plain reverse proxy on a directly reachable host. Proxying large volumes of video traffic through Cloudflare may also violate their terms of service.

## Force password reset on next startup

If you need to force the admin account to land on the password-change screen after restart, set:

```env
ADMIN_FORCE_PASSWORD_RESET=true
```

After the password has been changed, remove or disable this env var.
When it remains enabled, StreamNZB will keep forcing the password-reset prompt on startup.
