# NFS export + download client test stack

StreamNZB, Radarr and Sonarr in one compose stack, with StreamNZB's NFS
export mounted into both *arrs. It exercises the whole loop end to end:
Radarr grabs through StreamNZB as a SABnzbd download client, the release is
validated and published under `downloads/`, and Radarr imports it by renaming
into its own folder on the same mount — with the Docker host's Linux kernel
as the NFS client.

## Build the image

From the repository root, after a `bash build.sh` (the frontend assets must
be in place):

```
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w -X main.Version=dev" -o dist/linux_amd64/streamnzb ./cmd/streamnzb
docker build --build-arg TARGETARCH=amd64 -t streamnzb:arr-test .
```

## Start

1. `docker compose up -d streamnzb`, open <http://localhost:7001>, and set up
   providers, indexers and a stream — or copy an existing `config.json` into
   `data/streamnzb/` first. Copy the stream's token.
2. `cp .env.example .env`; set `NFS_HOST` to this machine's LAN address and
   `STREAM_TOKEN` to the token. The volume is mounted by the Docker host's
   kernel, which reaches StreamNZB through the published port (`NFS_PORT`,
   2050 by default) on that address, so `localhost` and the service name do
   not work there. The ports sit beside the defaults so a StreamNZB already
   running on the machine is not disturbed.
3. `docker compose up -d`. Radarr (<http://localhost:7878>) and Sonarr
   (<http://localhost:8989>) start once StreamNZB is healthy, with the export
   at `/mnt/streamnzb` inside each.

## Configure the *arrs

In each: **Settings → Download Clients → + → SABnzbd**

| Field | Value |
|---|---|
| Host | `streamnzb` |
| Port | `7000` (the container's own port; the *arrs reach it on the compose network) |
| URL Base | `/sabnzbd` |
| API Key | the stream token |
| Category | `movies` (Radarr) / `tv` (Sonarr) |

**Settings → Indexers → + → Newznab** can point at StreamNZB's Newznab
endpoint too (`http://streamnzb:7000/newznab`, API path `/api`, key from
Settings → Integrations), so searches also go through it.

Root folder: `/mnt/streamnzb/movies` in Radarr, `/mnt/streamnzb/tv` in
Sonarr — create them from the *arr's folder picker; the export accepts the
`MKDIR`.

## Silo

The stack also runs [Silo](https://github.com/Silo-Server/silo-server), a media
server, with the export mounted read-only at `/mnt/media`. Set
`SILO_SECRET_KEY` in `.env` (`openssl rand -base64 48`), then open
<http://localhost:8090>, complete onboarding, and add libraries pointing at
`/mnt/media/movies`, `/mnt/media/tv` (the *arrs' folders) or
`/mnt/media/library/Movies` and `/mnt/media/library/Shows` (StreamNZB's own
layout of played releases). Its Jellyfin-compatible endpoint is on 8096.

## What to watch

- `docker compose logs -f streamnzb`: `SABnzbd API accepted release`,
  `Library ingest stored release`, `NFS export published download`, then
  `NFS export mkdir` / `NFS export rename` as the *arr imports.
- `docker compose exec radarr ls -la /mnt/streamnzb /mnt/streamnzb/downloads`:
  the mount from the Linux client's side.
- The *arr's Activity → Queue: the job goes *Downloading* → *Completed* →
  imported; the file appears under its root folder with the *arr's name.

## Tear down

`docker compose down -v` removes the containers and the NFS volume
definition; `data/` keeps the configs and the StreamNZB database.
