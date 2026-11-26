# Download client API

StreamNZB can stand in for **SABnzbd** as a download client. Radarr, Sonarr or
anything else that drives SABnzbd sends it the NZBs they grab; StreamNZB
validates each one the way [preloading](stream-model.md#per-stream-configuration)
does, stores it in the release library, publishes it under `downloads/` on
the [NFS export](nfs.md), and reports a completed download there. Nothing is
downloaded to disk anywhere. The *arr imports it into its own folder on the export
by renaming; Plex, pointed at that folder, sees the file on its next scan; and
Stremio sees the release as a library hit the next time that title is
searched.

## Turning it on

Off by default. Turn it on under **Settings → Integrations → Download Client
API**, which also holds the complete directory. It rides on the addon
listener, so no port needs opening, and the switch applies at once.

The same settings can be pinned by environment, which wins on restart and
greys the fields out:

| Variable | Default | Meaning |
|---|---|---|
| `SABNZBD_ENABLED` | `false` | Serve the endpoint at `/sabnzbd/api` |
| `SABNZBD_COMPLETE_DIR` | `/mnt/streamnzb` | Where the *arr host has the NFS export mounted. Completed downloads are reported under it |

The log says `SABnzbd-compatible download client API enabled` at startup and
`SABnzbd API accepted release` for every NZB handed in.

## Adding it to Radarr or Sonarr

**Settings → Download Clients → + → SABnzbd**, then:

| Field | Value |
|---|---|
| Host | the StreamNZB host |
| Port | the addon port (`7000` by default) |
| Use SSL | as your addon URL is served |
| URL Base | `/sabnzbd` |
| API Key | a **stream token** — each stream card under Settings → Streams shows it in its Download client panel |
| Category | `movies` in Radarr, `tv` in Sonarr — any name works |
| Remote Path Mappings | none needed when `SABNZBD_COMPLETE_DIR` matches where the *arr sees the mount |

**Test** runs version, config, queue and history calls and should pass. The
*arr's **Completed Download Handling** can stay on: when a job completes the
*arr finds the file at the reported path on the export and imports it from
there — see [Importing](#importing) for what that means.

The API key is a stream, exactly as with the Jellyfin endpoint and the NFS
export. Validation runs through **that stream's providers under its
connection caps**, the library row carries the stream's name, and the
dashboard shows the job as a play of that stream. Give the *arr the same
stream the Plex mount uses, or one of its own with a cap on it so a batch of
grabs cannot take every connection.

The admin token is refused: it binds no providers.

## What happens to a grab

1. The *arr downloads the NZB from the indexer and posts it to the endpoint
   (`addfile`), with its category and the release name. The job appears in
   the queue as *Downloading*.
2. StreamNZB parses the release name for the title, year and episode, and
   asks TMDB for the title's ids — the same ids the catalogs and the export
   folder names use. A title TMDB cannot place is stored keyed by name and
   still works; it just has no id tag on the export.
3. The release is validated: a STAT sweep of the archive volumes, the archive
   mapping, an article check of the selected media file, and a strict
   ffprobe decode of its head. This is the preload pipeline; it downloads a
   few megabytes, not the file.
4. A release that passes is stored as a **good** library row with the media
   file's name and exact size, published on the NFS export as
   `downloads/<job name>/<media file>`, and the job moves to the history as
   *Completed* with `storage` set to `SABNZBD_COMPLETE_DIR` plus that
   folder: `/mnt/streamnzb/downloads/Iron.Man.2008.UHD.BluRay.2160p-GRP`.
   This is the layout SABnzbd itself leaves behind, so the *arr finds the
   file exactly where it expects to.
5. A **pack** is one job with one row per file in it. StreamNZB does not
   read episode numbers to decide what to store: every media file and every
   multi-volume archive in the NZB is validated on its own and published
   under its own name, double episodes and oddly named files included, so
   the folder holds what SABnzbd's would and the *arr sorts it out. A
   ten-file pack costs ten validations, a few seconds each. A file that fails
   is left out and the rest are published; the *arr then grabs just the
   missing one. The one shape not split is a single archive holding several
   episodes inside it, which is stored as one file.

   Obfuscated posts — files named by hash, which an *arr cannot place — get
   their real names back the way SABnzbd gives them back: the release's
   small `.par2` index is fetched, the original file names read out of it
   and matched to the files by exact size, and each file is published under
   its real name with the episode that name carries. The files of a pack
   are validated side by side, as many at once as the stream's connection
   budget allows (about one per four connections, at most six), and the
   queue reports the job's progress as files validated out of files in the
   release. Every file is checked in full: a census asks the providers for
   every article of it, RAR volumes or a bare file alike, and the job fails
   on the first article missing from all of them. A census that cannot
   finish — a stalled provider, the job's time running out — fails the job
   as well, since a client that is told a download completed will treat the
   file as whole. The probe then reads the container header for codecs and
   title; frames are not decoded, the census having already asked about
   every byte's article. When the index's names
   are hashes as well, each file's own container title tag is tried next,
   since WEB-DL files usually carry the episode name there. A post that
   yields neither keeps its hash names and lands in the *arr's manual-import
   queue, and the log says so.
6. A release that fails — missing articles, no playable media, a file the
   decoder refuses — is **not** stored, and the job moves to the history as
   *Failed* with the reason. The *arr sees a failed download and does what it
   does with those: blocklists the release and grabs the next one.

Jobs run one at a time, in the order they arrived. The queue shows each as
*Queued* until its turn. The history is kept across restarts (the last 500
jobs); a job in flight when StreamNZB stops is recorded as failed so the
*arr is told rather than left waiting.

## Importing

An *arr imports a completed download by **renaming** the file from the
download client's folder into its own library folder, under its own naming
scheme. On the NFS export that works the way it does against a real SABnzbd,
with one difference: nothing is moved, because the export is a tree of names
in StreamNZB's database whose files are aliases to library rows. A rename is
a row update. See
[NFS export → What the client sees](nfs.md#what-the-client-sees).

Set it up so:

1. Mount the export on the *arr host and set `SABNZBD_COMPLETE_DIR` to the
   mount point (or add a Remote Path Mapping in the *arr).
2. Make the *arr's **root folder** a folder on the mount — say
   `/mnt/streamnzb/movies` for Radarr and `/mnt/streamnzb/tv` for Sonarr.
   Create them from the *arr; it issues `MKDIR` and the export accepts it.
   Anything but `downloads/` is yours to name.
3. Leave **Completed Download Handling** on. When a job completes, the *arr
   finds `downloads/<job>/<file>`, renames it into its root folder with its
   own name, and tidies the empty download folder. Upgrades, renames and
   deletes later on behave the same way.
4. Point Plex at those same folders.

"Use Hardlinks instead of Copy" does not come into it: a Usenet import is a
move in the *arr's own code, and a move inside one filesystem is a rename.
Small files the *arr writes — its root-folder write test, `.nfo` and
subtitle sidecars — are kept on the export as ordinary files; only media is
off limits to writes.

Not a byte of media is copied anywhere in this flow.

## Relation to the NNTP proxy and the Newznab endpoint

The three can run together, and they do different things:

- The [Newznab endpoint](newznab.md) is how the *arr **searches**: all your
  indexers as one.
- This endpoint is how the *arr **"downloads"**: into the library, for Plex
  and Stremio to stream.
- The [NNTP proxy](nntp-proxy.md) is for a real SABnzbd or NZBGet that
  genuinely downloads to disk through your providers.

If the *arr should actually download files, point it at a real SABnzbd behind
the NNTP proxy, not at this.

## Security

The endpoint accepts only requests that carry a valid stream token as the
API key, the same credential that installs an addon. It rides on the addon
listener, so it is reachable wherever the addon is; a stream token already
grants playback through that stream, and this grants the ability to add
validated releases to the shared library through it. Rotate the token to cut
a client off.

## Troubleshooting

- **Test fails with "API Key Incorrect"** — the key is not a stream token,
  or it is the admin token. Copy it from the stream card.
- **Test fails with "Unable to connect"** — `SABNZBD_ENABLED` is not set, or
  the URL base is missing (`/sabnzbd`).
- **Job completes but the *arr cannot find the file** — the path in
  `storage` is `SABNZBD_COMPLETE_DIR` plus the export path. Either mount the
  export at that directory on the *arr host, set the variable to where it is
  mounted, or add a Remote Path Mapping in the *arr.
- **Every job fails with "Release unusable"** — the validation refused the
  releases. The message says why (missing articles, no media, decode
  failure); `streamnzb.log` has the detail under `Library ingest`.
- **Jobs sit in Queued** — one validation runs at a time and each can take a
  minute on a large release. A stuck job times out after thirty minutes and
  is recorded as failed.
