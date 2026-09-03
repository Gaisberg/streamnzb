# Custom result formats

Result formats are **profiles**: a named pair of templates — the **name
template** (the short label on the left of each result) and the **description
template** (the multi-line detail text) — created on the **Formatting** page
(under Settings) and bound to streams from the Streams page, so one format can
be reused across streams. A stream with no profile bound renders the built-in
format. The editor auto-saves and shows a live preview rendered by the
backend; a template that fails to compile is rejected at save time and can
never break stream responses — the built-in format is the fallback at render
time.

Upgrading from per-stream inline templates converts them automatically:
streams sharing identical templates collapse onto one shared profile
("Custom", "Custom 2", …) and are bound to it, so nothing changes visually.
Renaming a profile follows into every stream bound to it; deleting one
unbinds its streams, which fall back to the built-in format.

Templates use [Go template syntax](https://pkg.go.dev/text/template) over each
release's parsed data.

## Fields

| Group | Fields |
|---|---|
| Request | `.Service` `.Stream` `.Content` `.Index` `.Count` `.TopScore` `.Kind` `.IsAnime` |
| Release | `.ReleaseTitle` `.Size` `.Indexer` `.Variants` `.VariantIndexers` `.Grabs` `.Age` `.Duration` `.Score` `.Avail` `.Library` `.Caps` `.MatchedRules` |
| Measured | `.Verified` `.Probed.VideoCodec` `.Probed.AudioCodec` `.Probed.Width` `.Probed.Height` `.Probed.Profile` `.Probed.BitDepth` `.Probed.HDR` `.Probed.DolbyVision` `.Probed.HasHDRFallback` `.Probed.DynamicRange` `.Probed.TracksProbed` `.Probed.AudioLanguages` `.Probed.SubtitleLanguages` `.Probed.AudioStreams` `.Probed.SubtitleStreams` |
| Availability | `.Availability.Status` `.Availability.Known` `.Availability.OnMyBackbone` `.Availability.CheckedDaysAgo` `.Availability.Compression` |
| SeaDex | `.Seadex.Checked` `.Seadex.Known` `.Seadex.Best` `.Seadex.Alternative` `.Seadex.DualAudio` |
| Parsed | `.ParsedTitle` `.Year` `.Date` `.Resolution` `.Quality` `.Codec` `.BitDepth` `.Bitrate` `.Container` `.Extension` `.Group` `.Edition` `.Network` `.Site` `.Country` `.Region` `.Audio` `.Channels` `.HDR` `.Languages` |
| Episode | `.Season` `.Episode` `.Seasons` `.Episodes` `.EpisodeCode` `.Volumes` |
| Flags | `.Proper` `.Repack` `.Remastered` `.Upscaled` `.ThreeD` `.Scene` `.Retail` `.Hardcoded` `.Dubbed` `.Subbed` `.Commentary` `.Complete` `.Documentary` `.Unrated` `.Uncensored` `.PPV` |

List fields (`.HDR`, `.Audio`, `.Channels`, `.Languages`, `.Seasons`,
`.Episodes`, `.Volumes`) render comma-separated by default and work with
`range`, `index`, and the list helpers below. `.Caps` is the ffprobe-verified
media summary, present on library releases only. `.Duration` is the humanized
runtime (`1h 52m`), filled when the indexer reports one (e.g. Easynews — newznab
NZBs don't carry a runtime) or, for probed library releases, from the ffprobe
measurement of the file itself.

`.Bitrate` is the release's average bitrate as text (`21.5 Mbps`). Titles
almost never spell one out, so it is derived instead. For a probed library
release it is measured: the media file's exact size over the container's own
duration. For everything else it is an estimate: release size against the
indexer-reported duration when there is one, else against the title's
metadata runtime (per episode for multi-episode releases). It stays empty when
none of those are known, or for a season pack whose episode count the title
does not reveal — and the estimates are approximate, since release size
includes container and posting overhead.

`.Variants` is how many interchangeable copies of the release the search
merged, counting the one that plays first — `1` means no other indexer listed
it, anything higher is how many NZBs playback can fall back to without leaving
this release (see [Same-release variants](search-queries.md#same-release-variants)).
`.VariantIndexers` names them, the playing copy first.

```
{{if gt .Variants 1}}Variants: {{.Variants}}{{end}}
```

`.Library` is true once the release's NZB is stored locally, and `.Indexer`
then reads `StreamNZB Library - <original indexer>`. A release that was an
indexer hit when the stream list was built flips to both the moment it plays
through and is saved, including on a reload served from cache — from then on it
replays from the stored NZB and spends no indexer download.

### Anime and content kind

`.IsAnime` is the classification StreamNZB already made for this request —
Kitsu when the request came from a Kitsu catalog, TMDB genres otherwise — not a
guess from the release name. `.Kind` is the full content kind: `movie`,
`series`, `anime_movie` or `anime_show`.

```
{{if .IsAnime}}🌸 {{end}}{{.Resolution}}
```

### Matched rules

`.MatchedRules` lists the profile [rules](rules.md) that paid out on this
release, in the order they are configured. Each entry has `.Name` and `.Score`:

```
{{range .MatchedRules}}[{{.Name}}] {{end}}
{{range .MatchedRules}}{{.Name}} {{score .Score}} · {{end}}
```

It is empty when no profile ran or nothing matched. Rules that *rejected* a
release never reach a template — the release is gone by then.

### Measured properties

`.Probed` is what ffprobe found in the actual file. It is only populated for
library releases, since a fresh indexer hit has never been opened — guard with
`.Verified`:

```
{{if .Verified}}{{.Probed.DynamicRange}} {{.Probed.BitDepth}}-bit{{end}}
```

`.Probed.DolbyVision` and `.Probed.HDR` are independent, so a DV release with
an HDR10 base layer is tellable apart from one without — the difference between
a file that looks right on a non-DV TV and one that does not.
`.Probed.HasHDRFallback` answers that directly, and `.Probed.DynamicRange`
renders it as `DV + HDR10`, `DV only`, `HDR10`, or empty for SDR.

`.Probed.AudioLanguages` and `.Probed.SubtitleLanguages` are the tagged track
languages as ISO 639-1 codes, the same codes `.Languages` holds. Guard on the
list itself rather than on `.Verified`: a library item probed before the
tracks were captured is verified and has none, and a probed file whose muxer
tagged nothing has an empty list — both should fall back to what the name
claims rather than print a bare marker. One line then covers every case:

```
{{if .Probed.AudioLanguages}}⛿ {{join .Probed.AudioLanguages " · " | upper}}{{else if .Languages}}⛿ {{join .Languages " · " | upper}}{{end}}
```

`.Probed.AudioStreams` and `.Probed.SubtitleStreams` count tracks whether or
not they carry a tag.

### Availability

`.Avail` stays the plain "reported available" boolean it has always been.
`.Availability` is the full record: `.Status` is three-valued (`available`,
`unavailable`, `unknown`), `.OnMyBackbone` reports the release healthy on a
backbone this stream's providers use, and `.CheckedDaysAgo` is how stale the
record is (`-1` when it carries no timestamp).

```
{{if .Availability.OnMyBackbone}}✅{{else if eq .Availability.Status "unknown"}}·{{end}}
```

### SeaDex

`.Seadex` is [SeaDex's](https://releases.moe) recommendation for the requested
anime, judged against this release's group — see [SeaDex in
rules](rules.md#community--from-seadex) for how the lookup works. `.Best` and
`.Alternative` are per-title judgments, so a badge built on them marks releases
recommended for *this* anime rather than groups with a good name in general:

```
{{if .Seadex.Best}}🥇 SeaDex best{{else if .Seadex.Alternative}}🥈 SeaDex pick{{end}}{{if .Seadex.DualAudio}} · dual audio{{end}}
```

`.DualAudio` is SeaDex's own flag on the group's recommended release, so it
marks dual audio even when the release name does not say so.

`.Checked` reports a lookup ran at all (only anime requested through Kitsu is
looked up), and `.Known` that SeaDex has an entry for the title. Both are false
for everything else, so the badge above simply renders nothing outside anime.

## Helpers

String helpers accept any field — list fields are coerced to their
comma-separated text.

| Helper | Example | Result |
|---|---|---|
| `size` | `{{size .Size}}` | `1.83 GB` |
| `score` | `{{score .Score}}` | `+2850` |
| `join` | `{{join .HDR "\|"}}` | `DV\|HDR10` |
| `upper` / `lower` | `{{upper .Codec}}` | `H265` |
| `title` | `{{title .ParsedTitle}}` | `Ted Lasso` |
| `smallcaps` | `{{smallcaps .Network}}` | `ɴᴇᴛꜰʟɪx` |
| `trim` | `{{.Group \| trim}}` | |
| `replace` | `{{replace .Resolution "2160p" "4K"}}` | `4K` |
| `remove` | `{{remove "DD" .Audio}}` | drops every `DD` |
| `translate` | `{{translate "0123456789" "₀₁₂₃₄₅₆₇₈₉" .Score}}` | `₂₈₅₀` |
| `truncate` | `{{truncate 24 .ParsedTitle}}` | cut to 24 runes + `…` |
| `default` | `{{.Group \| default "unknown"}}` | fallback when empty |
| `exists` | `{{if exists .HDR}}…{{end}}` | non-empty test |
| `length` | `{{if gt (length .Audio) 1}}…{{end}}` | list size / rune count |
| `sortAsc` / `sortDesc` / `sort` | `{{join (sortAsc .Audio) " · "}}` | sorted copy |
| `first` / `last` | `{{first .Audio}}` | list edge element |
| `contains` | `{{if contains "DV" .HDR}}…{{end}}` | substring test |
| `hasPrefix` / `hasSuffix` | `{{if hasPrefix "2160" .Resolution}}…{{end}}` | prefix/suffix test |
| `add` / `sub` / `mul` / `div` / `mod` | `{{div 100 .Score}}` | integer math on the value |
| `min` / `max` | `{{.Score | div 100 | min 50}}` | smaller / larger of the two |
| `repeat` | `{{repeat "▰" 3}}` | `▰▰▰` |
| `stars` | `{{stars 5 .TopScore .Score}}` | `★★★☆☆` |

Multi-argument helpers take the value last so they chain in pipelines:
`{{.ParsedTitle | title | truncate 24}}`. The exceptions are `replace` and
`join`, which keep their original value-first signatures.

### Math

The math helpers work in whole integers, take the value last like everything
else, and read as "apply N to the value": `{{sub 100 .Score}}` is score minus
100, `{{div 1000 .Size}}` is size divided by 1000. They never error — dividing
by zero yields 0 and a non-numeric value counts as 0 — so a bad expression
shows a wrong number instead of silently reverting the whole template to the
built-in format.

`stars` renders a rating: `{{stars 5 .TopScore .Score}}` scales the score
against a ceiling, rounds to the nearest of 5 stars, and prints `★★★☆☆`. The
ceiling is an argument because `.Score` has no fixed scale — it is whatever
your ranking profile's rules add up to. `.TopScore`, the highest score in the
current result list, is the natural choice: the best result always paints
full and everything else rates relative to it. A fixed ceiling
(`{{stars 5 5000 .Score}}`) works too if you'd rather rate against an
absolute bar. Values clamp into range: a negative score renders all-empty,
one past the ceiling all-filled. For custom glyphs or bars, build the same
thing from math plus `repeat`:

```
{{stars 5 .TopScore .Score}}
{{.Score | min 5000 | max 0 | div 1000 | repeat "▰"}}
```

## Conditionals and composition

Go templates provide conditionals, comparisons, and boolean logic natively:

```
{{if .Avail}}⚡{{end}}
{{if .HDR}}📺 {{join .HDR "|"}}{{else}}SDR{{end}}
{{if eq .Resolution "2160p"}}4K{{end}}
{{if and .Avail .Library}}✅ verified library hit{{end}}
{{if gt (length .Audio) 1}}multi-audio{{end}}
```

`eq`, `ne`, `lt`, `le`, `gt`, `ge`, `and`, `or`, `not`, `len`, `index`,
`slice`, and `printf` are all built in. Helpers compose by nesting
(`{{join (sortAsc .Audio) " · "}}`) or piping
(`{{.ParsedTitle | title | truncate 24}}`).

Lines that render empty are removed from the output, so a false conditional on
its own line never leaves a blank line behind. Output is capped at 1000
characters per template.

## Sharing format profiles

**Export** turns a format profile — name and both templates — into one
`SNZBF1:` string, the same shape filter profiles use. Copy shares it through a
chat window; **Download** writes it to a file for hosting.

**Import** takes a code, or the `https://` URL of a raw file that serves one,
and adds it as a new profile — a name collision gets a numeric suffix. The
dialog also offers a curated **community templates** dropdown; picking one
fills the URL field and imports through the ordinary from-URL path.

A profile imported from a URL stays **linked**: it shows the host it came
from, and a manual **Refresh** button fetches the current code and shows what
would change before anything is applied. Each template that would change has
its own checkbox, so a refresh can take the maintainer's description and leave
your name template alone; the profile's local name is yours and never follows
a rename upstream. **Unlink** keeps the profile as it is and removes the
connection. The trust rules and hosting notes are the same as for filter
profiles — see [Remote profiles](filters.md#remote-profiles).

## Importing an AIOStreams formatter

The format editor has an **Import from AIOStreams** section that converts an
[AIOStreams custom formatter](https://docs.aiostreams.viren070.me/reference/custom-formatter/)
into StreamNZB Go templates: paste the AIOStreams name/description templates,
hit Convert, and the editors above fill with the converted result.

The conversion is best-effort. Fields, modifiers, and conditionals with a
StreamNZB counterpart map across — including `::exists`/`::istrue` checks,
numeric comparisons, `::and`/`::or` chains, `{? … ?}` optional groups, nested
conditionals, and modifier chains like `::lsort::join(' · ')`. Constant fields
resolve at conversion time: `{stream.type::=usenet[…]}` keeps its true branch,
`{stream.proxied::istrue[…]}` drops entirely.

Anything without an equivalent behaves like a missing field rather than
leaking into your results: conditionals fall back to their false/missing
branch, bare expressions drop, and unsupported modifiers (`::date` patterns,
`::star`, …) render their value unformatted. Every removal and approximate
mapping (for example `{service.cached}` → `{{.Avail}}`, `{stream.seeders}` →
`{{.Grabs}}`) is listed as a warning so you can adjust by hand.
