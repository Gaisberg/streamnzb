# Home Assistant & live stats

`GET /api/stats/current` returns what the dashboard shows live: how many streams are playing, what they are, who is watching and how fast the pool is pulling. Any tool that can poll JSON can read it. This page uses Home Assistant as the worked example.

The numbers come from the snapshot the dashboard already gets once a second, so polling never takes a measurement of its own and never skews the dashboard's speed graphs.

## Authentication

Send a token as `Authorization: Bearer <token>`, or as `?token=<token>` for tools that cannot set headers. The token decides what you see:

| Token | Sees |
|---|---|
| Admin token, returned as `token` by `POST /api/login` | Every session, every stream, connection counts and per-provider rows |
| A stream's token, from its card under **Settings → Streams** | That stream's own sessions and speed only. Asking for another stream returns `403`. |

For a household dashboard, use a stream's token when one stream is all you want to track. It can't read anything else, and rotating it only affects that stream. The admin token unlocks the whole API, configuration included, so give it to Home Assistant only if you want the full view.

## The response

```json
{
  "timestamp": "2026-10-20T20:00:00Z",
  "active_streams": 2,
  "total_speed_mbps": 42.1,
  "active_connections": 12,
  "total_connections": 40,
  "streams": [
    { "name": "living-room", "active_sessions": 1, "speed_mbps": 30.4 },
    { "name": "kitchen",     "active_sessions": 0, "speed_mbps": 0 }
  ],
  "sessions": [
    {
      "id": "…",
      "stream": "living-room",
      "title": "Some.Movie.2024.2160p.WEB-DL",
      "clients": ["192.168.1.20"],
      "started_at": "2026-10-20T19:58:30Z",
      "duration_seconds": 90,
      "bytes_read": 734003200
    }
  ],
  "providers": [ { "name": "primary", "active_conns": 12, "max_conns": 40, "current_speed_mbps": 42.1 } ]
}
```

- `active_streams` is the number of entries in `sessions`. A session counts as active while a client has read from it in the last 60 seconds.
- `streams` lists **every configured stream**, idle ones at zero, so a sensor bound to a stream reads `0` instead of going unavailable.
- NNTP proxy clients appear in `sessions` with an empty `stream` and no `started_at`, and only in the admin's unfiltered view.
- `active_connections`, `total_connections` and `providers` are admin-only and are left out of narrowed views.
- `?stream=<name>` narrows the response to one stream. `active_streams` and `total_speed_mbps` then refer to that stream alone.
- Before the first snapshot after startup (about a second), the endpoint returns `503`.

## Home Assistant

Put the token in `secrets.yaml`. Home Assistant substitutes a secret as a whole value, so store the full header:

```yaml
# secrets.yaml
streamnzb_auth: "Bearer <your token>"
```

Then add a [RESTful](https://www.home-assistant.io/integrations/rest/) sensor block to `configuration.yaml`, adjusting the host and port:

```yaml
rest:
  - resource: http://streamnzb.local:7000/api/stats/current
    headers:
      Authorization: !secret streamnzb_auth
    scan_interval: 15
    sensor:
      - name: StreamNZB active streams
        unique_id: streamnzb_active_streams
        value_template: "{{ value_json.active_streams }}"
        state_class: measurement
        json_attributes:
          - sessions
          - streams
      - name: StreamNZB speed
        unique_id: streamnzb_speed
        value_template: "{{ value_json.total_speed_mbps | round(1) }}"
        unit_of_measurement: "Mbit/s"
        device_class: data_rate
        state_class: measurement
    binary_sensor:
      - name: StreamNZB playing
        unique_id: streamnzb_playing
        value_template: "{{ value_json.active_streams > 0 }}"
        device_class: running
```

Restart Home Assistant, or reload **YAML configuration → RESTful entities**. The active-streams and speed sensors record history, so they graph and can drive automations: for example, pause a large download or dim the lights while `binary_sensor.streamnzb_playing` is on.

### One sensor per stream

Add one block per stream with `?stream=`. Each one reads the same as the totals but for that stream alone:

```yaml
  - resource: http://streamnzb.local:7000/api/stats/current?stream=living-room
    headers:
      Authorization: !secret streamnzb_auth
    scan_interval: 15
    sensor:
      - name: StreamNZB living room
        unique_id: streamnzb_living_room
        value_template: "{{ value_json.active_streams }}"
        json_attributes: [sessions]
```

With a stream token you can leave `?stream=` off; the token already narrows the response to its stream.

### What is playing

A Markdown card reading the `sessions` attribute lists the current titles:

```yaml
type: markdown
title: Now streaming
content: >
  {% set sessions = state_attr('sensor.streamnzb_active_streams', 'sessions') or [] %}
  {% for s in sessions %}
  - **{{ s.title }}** on {{ s.stream or 'proxy' }}
    ({{ (s.duration_seconds // 60) }} min, {{ (s.bytes_read / 1073741824) | round(2) }} GB)
  {% else %}
  Nothing is playing.
  {% endfor %}
```

## Notes

- Keep `scan_interval` at 10 seconds or more. The snapshot updates every second, but a session only drops out once its client has gone quiet for 60 seconds, so faster polling doesn't make the session count any more accurate.
- If Home Assistant reaches StreamNZB through a reverse proxy with SSO, the bearer token is still checked on its own, so the proxy's login screen doesn't apply to it. See [Remote access](remote-access.md).
