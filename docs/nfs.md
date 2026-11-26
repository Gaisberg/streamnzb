# NFS export

StreamNZB can serve its release library as an **NFSv3 share**. A media
server that only understands folders of files — Plex above all, but also
Emby, Kodi or anything that scans a mount — mounts the share and plays from
it. Nothing is downloaded to disk on either side: every file is one of the
releases the library holds, and every read is the same playback byte stream a
`/play` request would serve, fetched from your providers as the player asks
for it.

The share is filled by the [download client API](sabnzbd.md) and organised
by Radarr and Sonarr, exactly as they organise a local disk. StreamNZB names
and sorts nothing itself.

## Turning it on

The export opens a port of its own and its only credential is a stream token
in the mount path, so it is **off by default**. Turn it on under **Settings →
Integrations → NFS Export**, where the bind host and port live too and where
a listener that failed to bind says so. Changes apply at once; no restart.

The same settings can be pinned by environment, which wins on restart and
greys the fields out:

| Variable | Default | Meaning |
|---|---|---|
| `NFS_ENABLED` | `false` | Serve the export |
| `NFS_HOST` | all interfaces | Listen address. A LAN or VPN address is the sensible choice |
| `NFS_PORT` | `2049` | TCP port for both the MOUNT and NFS programs |

In Docker, publish the port on TCP and set the variables:

```yaml
services:
  streamnzb:
    environment:
      NFS_ENABLED: "true"
    ports:
      - "2049:2049"
```

The log says `NFS export listening` at startup, and `NFS export mounted` with
the stream name and client address each time a client mounts.

## Mounting

A mount names the StreamNZB host, the **stream token** as the exported path,
and the port — twice, because MOUNT and NFS are separate RPC programs and the
client would otherwise ask the portmapper on 111, which StreamNZB also
answers when it can bind that port (see [Windows](#windows)). Every stream
card under **Settings → Streams** shows its own ready-made mount line next to
the Stremio manifest and the Jellyfin login, with the token filled in.

Linux:

```
sudo mount -t nfs -o ro,vers=3,tcp,port=2049,mountport=2049,nolock,soft \
  streamnzb-host:/<stream token> /mnt/streamnzb
```

macOS:

```
sudo mount -t nfs -o ro,vers=3,tcp,port=2049,mountport=2049,nolock,soft,rdonly \
  streamnzb-host:/<stream token> /Volumes/streamnzb
```

`/etc/fstab`, so it comes back after a reboot:

```
streamnzb-host:/<stream token>  /mnt/streamnzb  nfs  ro,vers=3,tcp,port=2049,mountport=2049,nolock,soft,_netdev,nofail  0  0
```

Notes on the options:

- `vers=3,tcp` — the export speaks NFSv3 over TCP only.
- `port=` and `mountport=` — both must name the export's port.
- `nolock` — there is no lock manager, and nothing to lock.
- `soft` — a read that cannot be served (articles gone, provider down) fails
  with an I/O error instead of hanging the process forever. Plex copes with
  an error; it does not cope with a scanner thread blocked indefinitely.

**Plex in Docker**: do the mount on the host and bind-mount the directory
into the Plex container (`-v /mnt/streamnzb:/media/streamnzb:ro`). Mounting
from inside a container needs extra privileges that are not worth granting.

### Windows

The Windows NFS client cannot be told a port: it always asks a **portmapper**
on port 111 where MOUNT and NFS live. StreamNZB answers that whenever the
export is on and port 111 can be bound — there is nothing to switch on. It
reports only its own two programs, over TCP and UDP, and refuses
registration. Port 111 is privileged on Linux: run as root or grant
`CAP_NET_BIND_SERVICE`, and in Docker publish `111:111` and `111:111/udp`. A
host that cannot bind it still serves Linux and macOS clients; the NFS Export
card says so, and the log line is `NFS portmapper is not listening`.

On the Windows machine, add the **Services for NFS → Client for NFS** feature
(Pro and Enterprise; Home has no NFS client), then in an administrator prompt:

```
mount -o anon,nolock,mtype=soft,timeout=20,proto=tcp,vers=3 \\streamnzb-host\<stream token> Z:
```

The stream card shows this line filled in. `anon`
maps you to the anonymous user, which is what the read-only export expects;
`mtype=soft` keeps a failed read from hanging the process, and `timeout=20`
gives a read twenty seconds rather than the default 0.8: the first read of a
cold file waits while the release opens, which takes a second or a few.
Windows caches
directory listings for longer than Linux does, so a release added a moment
ago may take a minute to appear. Plex for Windows then takes `Z:\Movies` and
`Z:\Shows` as library folders.

### Which stream to mount

A mount is a stream: the reads it causes go through **that stream's
providers**, under **its per-provider connection caps**, and show on the
dashboard under its name, exactly like a play. Give a Plex server a stream of
its own so its scans do not compete with a living-room stream for the same
connection budget, and so a cap on it bounds what a scan can take.

The admin identity is refused at mount time. It binds no providers and owns
no connection budget, so there would be nothing to charge a read to.

Regenerating a stream's token invalidates the handles every client of that
stream holds — they see `Stale file handle` until they remount with the new
token. Renaming a stream changes nothing; the token is what the mount uses.

## What the client sees

```
/
├── downloads/                    ← finished download-client jobs
│   └── Iron.Man.2008.UHD.BluRay.2160p-GRP/
│       └── Iron.Man.2008.UHD.BluRay.2160p-GRP.mkv
├── movies/                       ← made by Radarr, named by Radarr
│   └── Iron Man (2008)/
│       └── Iron Man (2008) Bluray-2160p.mkv
├── tv/                           ← made by Sonarr
└── library/                      ← generated, read-only: what nobody organises
    ├── Movies/
    │   └── The Shawshank Redemption (1994) {imdb-tt0111161}/
    │       └── The Shawshank Redemption (1994) - The.Shawshank.Redemption.1994.2160p.BluRay-GRP.mkv
    └── Shows/
        └── Game of Thrones (2011) {imdb-tt0944947}/
            └── Season 01/
                └── Game of Thrones (2011) - s01e02 - Game.of.Thrones.S01E02.1080p-GRP.mkv
```

Apart from `library/`, the share is a directory tree of names the client
chooses, kept in StreamNZB's database; only `downloads/` is there from the
start. A file in it is an **alias to a library row**:
renaming or moving it is a row update, deleting it drops the alias, and the
release it pointed at stays in the library. No media is ever written, moved
or copied — there is none to move.

- **`downloads/`** is where the [download client API](sabnzbd.md) publishes
  each finished job, as `<job name>/<media file>`, the way SABnzbd leaves a
  job in its complete folder. The *arr is handed that folder.
- **Everything else** the *arr makes. Give Radarr a root folder of
  `<mount>/movies` and Sonarr `<mount>/tv`, let them create the folders, and
  on import each creates `movies/Iron Man (2008)/`, renames the file into it
  under its own scheme, sets permissions and removes the empty download
  folder. All of that is accepted: `MKDIR`, `RENAME`, `REMOVE`, `RMDIR`, and
  a `SETATTR` that says yes and keeps nothing. Plex scans those folders.
- **`library/`** is StreamNZB's own layout of the releases nobody
  organises: rows that reached the library by being played through Stremio
  and have no alias anywhere else on the share. They are laid out the way a
  scanner wants, with `{imdb-…}`, `{tmdb-…}` or `{tvdb-…}` tags on the
  folders so Plex matches them without guessing, titles from your metadata
  providers (the release name until a provider answers), and several
  releases of one title in one folder as versions. It is read-only. The
  moment an *arr imports a release, it gains an alias in the *arr's folder
  and leaves `library/`, so nothing is listed twice. Point Plex at it only if
  you want your Stremio plays in Plex too.
- **Only validated releases appear.** A row has to be `good` in the Library;
  an alias to a row the [freshness sweep](database.md) marked `bad` because
  its articles expired leaves the listing within thirty seconds, and the *arr
  sees a missing file to replace.
- **File sizes are the media file's exact size.** A row stored before that
  was recorded is measured the first time a client looks the file up, which
  costs one open, and the size is written back to the row.
- **Small files the client writes are kept.** Radarr and Sonarr drop a
  write-test file in a folder before accepting it as a root folder, and leave
  `.nfo` and subtitle sidecars beside imports; those are created, written and
  read as ordinary files, stored in the database and capped at 8 MB each. A
  release itself is never writable: `WRITE` to it, or truncating it, is
  `EROFS`. Media still arrives only through the download client API.
- What stays refused: symlinks and hard links. The `downloads/` folder
  cannot be renamed or removed; it is where jobs land. `library/` cannot be
  changed at all.

Free space is reported as a very large number, because an import is refused
by the *arr when it believes the disk is full, and there is no disk.

## Plex

Add a library of type **Movies** pointing at Radarr's folder on the mount,
and one of type **TV Shows** pointing at Sonarr's. The *arr's naming carries
the ids Plex matches on.

Then, in the library's **Advanced** settings and under **Settings →
Library**, turn off everything that reads whole files. Plex does several
things to a new file that are harmless on a local disk and are a full
download of the file here:

| Setting | Set to | Why |
|---|---|---|
| Generate video preview thumbnails | never | Decodes the entire file |
| Generate intro / credits markers | never | Decodes a large part of every episode |
| Analyze audio tracks for loudness | off | Decodes the entire audio |
| Generate chapter thumbnails | never | Seeks through the whole file |
| Perform extensive media analysis during maintenance | off | Re-reads files on a schedule |
| Scan my library periodically | your call | Cheap: directory listings only |

With those off, adding a file costs Plex one *analysis* — ffprobe-like, a
few megabytes from the head and tail of the file — and playback costs
whatever is watched, the same as playing it through Stremio.

If **Run a partial scan when changes are detected** is on, Plex watches the
mount with inotify, which NFS does not deliver. Leave periodic scanning on
instead, or trigger a scan from StreamNZB's side with the Plex API when you
want new library rows to appear promptly.

## How a read works

The client sends reads of up to one megabyte at a byte offset. The first
read on a file opens the release the way a play does — stored NZB, stored
archive blueprint, a quick header probe — so the first bytes of a cold file
take the seconds a cold play takes; everything after that is served from an
open stream that reads ahead of the player. A file nobody has read for five
minutes is closed, which is also when its play ends on the dashboard.

Linux clients pipeline several reads at once. Reads on different files run
concurrently; reads on one file are served in order through its single
stream, so a scanner probing twenty files in parallel opens twenty streams,
each under the mounting stream's connection cap.

A stream that fails mid-read — a provider dropped, articles gone — is closed
and the read answered with an I/O error; the next read on that file opens it
afresh. If the release itself is gone the freshness sweep marks it bad and
the file leaves the listing.

## Security

NFSv3 has, for practical purposes, **no authentication**: the client tells
the server what user it is and the server believes it. StreamNZB does not use
that at all. The stream token in the mount path is the whole gate — a mount
without a valid token is refused, `EXPORT` lists nothing, and every file
handle is bound to the token that mounted it.

That is adequate on a network you control and not elsewhere:

- Bind `NFS_HOST` to a LAN or VPN address, or keep the port off the
  internet with the firewall. Do not forward it, and do not route it through
  a Cloudflare Tunnel or an HTTP reverse proxy — it is not HTTP.
- The token appears in the mount path, which is visible to anyone who can
  read the client's mount table (`mount`, `/proc/mounts`). Treat the client
  machine as trusted, or give it a stream whose token you are happy to
  rotate.
- Anyone who can reach the port and knows a token can list and read that
  stream's library through that stream's providers. Rotate the token to
  shut them out.

## What it deliberately does not do

- **Write.** Nothing can be added, renamed or deleted through the mount. The
  library is populated by playing, or by preloading, through StreamNZB.
- **NFSv4.** A large protocol with nothing to offer this export. The
  portmapper answers for nothing but this export's two programs.
- **Search.** A file is a release already in the library. Browsing the
  export does not search indexers, and nothing in Plex can trigger one.
- **Transcode or remux.** Files are served byte-for-byte; the client's own
  decoder decides what it can play, as everywhere else in StreamNZB.

## Troubleshooting

- **`mount.nfs: Connection refused` / `timed out`** — the port is not
  reachable: `NFS_ENABLED` is not set, the Docker port is not published, or
  a firewall is in the way. Check the log for `NFS export listening`.
- **`mount.nfs: access denied by server`** — the token in the path is not a
  stream's token, or it is the admin token. Copy it from the stream card.
- **`mount.nfs: requested NFS version or transport protocol is not
  supported`** — `vers=3` or `tcp` is missing from the options, or the
  client tried the portmapper because `port=`/`mountport=` were not given
  and port 111 is not bound on the host.
- **Windows: "Network Error - 53" or the mount hangs** — port 111 (TCP and
  UDP) is not bound on the host or not reachable from the Windows machine.
  The NFS Export card says whether it is listening.
- **`Stale file handle`** — the stream's token was regenerated, or the
  stream was deleted. Remount with the current token.
- **A file is listed but will not play** — the release has gone bad since
  the listing was cached. The sweep marks it within the hour; `streamnzb.log`
  has the read error under `NFS read failed`.
- **Plex is downloading everything** — one of the analysis tasks in the
  table above is still on. The dashboard shows the mount's stream reading at
  full speed with nobody watching.
