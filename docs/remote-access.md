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
        proxy_set_header Host $http_host;             # $http_host keeps the port; $host drops it
        proxy_set_header X-Forwarded-Host $http_host; # overwrite, never pass the client's copy through
        proxy_set_header X-Forwarded-Proto $scheme;   # marks the login cookie Secure
        proxy_buffering off;        # stream video through, don't spool it
        proxy_read_timeout 1h;      # playback connections are long-lived
    }
}
```

StreamNZB reads `X-Forwarded-Proto` to tell whether the browser reached it over HTTPS, and marks the admin session cookie `Secure` when it did — so the browser will not send that cookie back over plain HTTP. Caddy sets the header on its own; nginx needs the line above. Without it, everything still works, the cookie simply misses a protection it could have had.

## Behind a reverse-proxy login (Authelia, Authentik)

If the proxy in front of StreamNZB already asks for a login — Authelia, Authentik, oauth2-proxy and similar — the dashboard's own login is a second door behind the first. Its session lasts seven days, so every week it asks for the admin password again even though the proxy already checked who you are. Trusted-proxy authentication removes that second door.

Two settings, both required (`config.json` or environment):

```
trusted_proxy_auth_header: Remote-User      # TRUSTED_PROXY_AUTH_HEADER
trusted_proxies: ["172.18.0.0/16"]          # TRUSTED_PROXIES=172.18.0.0/16
```

A request that arrives **from one of those addresses** and **carries that header with a name in it** is treated as the admin. Everything else — a request from any other address, or one without the header — still gets the normal login, so a proxy that is down or bypassed leaves the dashboard as locked as it was. Setting only one of the two, a blank entry, a catch-all network such as `0.0.0.0/0`, or a header name no proxy could send is refused when saved from the dashboard; the same values arriving from the environment leave the feature off with a warning in the log.

The address list is the security boundary. The header alone proves nothing, because anyone who can reach the listener directly can write one; the proxy's address is what an outsider cannot forge. Two rules follow:

- List only the proxy. On Docker that is the network the proxy container sits on (`docker network inspect <name>` shows the subnet), not `0.0.0.0/0`.
- Make sure nothing else can reach StreamNZB's port from those addresses — publish the port to the proxy's network only, never to the host or the internet.

Two things the proxy must do for writes to work. It must pass the identity header through, and it must tell StreamNZB the host the browser addressed, port included: either leave `Host` as the browser sent it (Traefik and Caddy do this by default; nginx needs `proxy_set_header Host $http_host;` — `$http_host` keeps a non-standard port, `$host` drops it) or **set** `X-Forwarded-Host` itself, overwriting anything the client sent (Traefik and Caddy do; nginx only with `proxy_set_header X-Forwarded-Host $http_host;`). A forwarded host the proxy merely passes through from the client is not trustworthy and must not be relied on. A proxy that rewrites `Host` and sets nothing makes every browser `Origin` look foreign, and the dashboard then loads normally but answers every save and the live stats socket with a login screen; the refusal is logged at Warn (rate-limited, the rest at Debug) with the origin and host it compared.

Traefik with Authelia's `forwardAuth` middleware:

```yaml
http:
  middlewares:
    authelia:
      forwardAuth:
        address: http://authelia:9091/api/authz/forward-auth
        trustForwardHeader: true   # only safe with entryPoints.<name>.forwardedHeaders.trustedIPs set to the upstream proxy
        authResponseHeaders:
          - Remote-User
          - Remote-Groups
          - Remote-Email
          - Remote-Name
```

`trustForwardHeader` makes Traefik pass the client's `X-Forwarded-*` headers to Authelia; that is only safe when the entry point's `forwardedHeaders.trustedIPs` names the proxy in front of Traefik, or when Traefik itself is the edge and the setting is left off. Authelia writes `Remote-User` on every request it lets through and strips whatever the client sent, which is exactly the property this relies on. Authentik uses `X-authentik-username`; oauth2-proxy uses `X-Forwarded-User` when `--set-xauthrequest` is on. Put that header name in `trusted_proxy_auth_header`.

Cross-site requests: the dashboard's own session cookie is `SameSite=Strict`, which is what stops another site from making your browser change settings. A proxy identity has no cookie of its own, so StreamNZB rebuilds that guarantee from the browser's own fetch metadata: a state-changing request marked `Sec-Fetch-Site: cross-site` or carrying an `Origin` for a different host gets no proxy identity and is refused. Reads are unaffected, and scripts behind the proxy that send neither header keep working. A WebSocket handshake counts as a write for this purpose, since the socket it opens is readable by the page that opened it. The one gap left open on purpose: a browser old enough to send neither `Sec-Fetch-Site` nor `Origin` on a cross-site form post — Safari before 16.4 — is indistinguishable from a script and is let through; keeping scripts working was judged worth that residue.

What stays the same: the Stremio addon and stream URLs are unaffected (they carry their own tokens), the admin password still exists and still works, `/api/login` still answers, and a bearer token still works for scripts. The admin token is never returned to a proxy-vouched request, so revoking the person at the proxy revokes their access. The **Log out** button clears StreamNZB's cookie only; the next request is vouched for again by the proxy, so to actually sign out, sign out of the proxy.

## What to avoid

- **Cloudflare Tunnel / proxied Cloudflare DNS** — throttles sustained video throughput and may violate Cloudflare's terms. See [Troubleshooting](troubleshooting.md#buffering-behind-cloudflare-tunnel).
- **Exposing port 7000 raw on the internet** — if you must go direct, at least front it with HTTPS via a reverse proxy; the web UI is password-protected, but credentials over plain HTTP on the open internet are a bad idea.

## Notes

- **Changing the Base URL changes your manifest URLs.** Already-installed addons keep pointing at the old address, so reinstall the stream in your clients after switching (the token stays the same, so playback history is kept).
- The **NNTP proxy** (default port 1119) is raw TCP, not HTTP — it cannot go through an HTTP reverse proxy. Reach it over the VPN, or forward its port separately. See [NNTP proxy](nntp-proxy.md).
