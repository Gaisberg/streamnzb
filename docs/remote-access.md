# Remote access

To stream away from home, the **Base URL** you set in **General** (under Settings) must be reachable from the device running your Stremio client — the manifest, catalog and video URLs your client uses are all built from it. StreamNZB itself serves plain HTTP; if you want HTTPS, terminate TLS in front of it with a reverse proxy.

The playback path moves a lot of data continuously, so how you expose the server matters more than for a typical web app. Options, in order of recommendation:

## VPN (recommended): Tailscale or WireGuard

The simplest safe setup. Nothing is exposed to the internet, and throughput is limited only by your line.

- **Tailscale** — install it on the server and on each client device, then set the Base URL to the server's Tailscale IP, e.g. `http://100.64.0.1:7000`. Plain HTTP is fine here: the tunnel is already encrypted.
- **WireGuard** — same idea with a self-hosted tunnel; use the server's VPN address as the Base URL.

## Reverse proxy with HTTPS

For access without a VPN, put a reverse proxy with a real domain in front of StreamNZB on a directly reachable host, and set the Base URL to `https://streamnzb.example.com`.

**Caddy** (automatic HTTPS):

```
streamnzb.example.com {
    reverse_proxy localhost:7000
}
```

**nginx** — the two settings that matter for streaming are buffering and timeouts:

```nginx
server {
    server_name streamnzb.example.com;
    listen 443 ssl;
    # ssl_certificate ...; ssl_certificate_key ...;

    location / {
        proxy_pass http://127.0.0.1:7000;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_buffering off;        # stream video through, don't spool it
        proxy_read_timeout 1h;      # playback connections are long-lived
    }
}
```

## What to avoid

- **Cloudflare Tunnel / proxied Cloudflare DNS** — throttles sustained video throughput and may violate Cloudflare's terms. See [Troubleshooting](troubleshooting.md#buffering-behind-cloudflare-tunnel).
- **Exposing port 7000 raw on the internet** — if you must go direct, at least front it with HTTPS via a reverse proxy; the web UI is password-protected, but credentials over plain HTTP on the open internet are a bad idea.

## Notes

- **Changing the Base URL changes your manifest URLs.** Already-installed addons keep pointing at the old address, so reinstall the stream in your clients after switching (the token stays the same, so playback history is kept).
- The **NNTP proxy** (default port 119) is raw TCP, not HTTP — it cannot go through an HTTP reverse proxy. Reach it over the VPN, or forward its port separately. See [NNTP proxy](nntp-proxy.md).
