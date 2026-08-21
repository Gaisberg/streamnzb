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
| Request | `.Service` `.Stream` `.Content` `.Index` `.Count` `.Kind` `.IsAnime` |
| Release | `.ReleaseTitle` `.Size` `.Indexer` `.Grabs` `.Age` `.Duration` `.Score` `.Avail` `.Library` `.Caps` `.MatchedRules` |
| Measured | `.Verified` `.Probed.VideoCodec` `.Probed.AudioCodec` `.Probed.Width` `.Probed.Height` `.Probed.Profile` `.Probed.BitDepth` `.Probed.HDR` `.Probed.DolbyVision` `.Probed.HasHDRFallback` `.Probed.DynamicRange` |
| Availability | `.Availability.Status` `.Availability.Known` `.Availability.OnMyBackbone` `.Availability.CheckedDaysAgo` `.Availability.Compression` |
| Parsed | `.ParsedTitle` `.Year` `.Date` `.Resolution` `.Quality` `.Codec` `.BitDepth` `.Bitrate` `.Container` `.Extension` `.Group` `.Edition` `.Network` `.Site` `.Country` `.Region` `.Audio` `.Channels` `.HDR` `.Languages` |
| Episode | `.Season` `.Episode` `.Seasons` `.Episodes` `.EpisodeCode` `.Volumes` |
| Flags | `.Proper` `.Repack` `.Remastered` `.Upscaled` `.ThreeD` `.Scene` `.Retail` `.Hardcoded` `.Dubbed` `.Subbed` `.Commentary` `.Complete` `.Documentary` `.Unrated` `.Uncensored` `.PPV` |

List fields (`.HDR`, `.Audio`, `.Channels`, `.Languages`, `.Seasons`,
`.Episodes`, `.Volumes`) render comma-separated by default and work with
`range`, `index`, and the list helpers below. `.Caps` is the ffprobe-verified
media summary, present on library releases only. `.Duration` is the humanized
runtime (`1h 52m`), filled only when the indexer reports one (e.g. Easynews) —
newznab NZBs don't carry a runtime.

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

### Availability

`.Avail` stays the plain "reported available" boolean it has always been.
`.Availability` is the full record: `.Status` is three-valued (`available`,
`unavailable`, `unknown`), `.OnMyBackbone` reports the release healthy on a
backbone this stream's providers use, and `.CheckedDaysAgo` is how stale the
record is (`-1` when it carries no timestamp).

```
{{if .Availability.OnMyBackbone}}✅{{else if eq .Availability.Status "unknown"}}·{{end}}
```

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

Multi-argument helpers take the value last so they chain in pipelines:
`{{.ParsedTitle | title | truncate 24}}`. The exceptions are `replace` and
`join`, which keep their original value-first signatures.

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
