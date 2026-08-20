# Rules

A **rule** is a named condition attached to a filter profile, plus what happens
when it holds: reject the release, or move its score. Rules are edited on the
**Rules** tab of a filter profile, and are the general form of what the
weighted regex patterns used to be.

Rules are the whole of a profile beyond its preset. The old editor's sliders,
pattern lists and per-kind grids are gone; everything they could express, a rule
expresses with a name, a scope, and the ability to combine with anything else.

They exist because some things cannot be said with one pattern:

- *Dolby Vision, but only when there is no HDR fallback.*
- *Over 30 GB, unless it is 4K.*
- *Confirmed alive on a backbone my providers actually use.*
- *Measured 10-bit, not just claimed 10-bit in the name.*

A regular expression sees one string. A rule sees everything known about the
release.

```
dolbyVision and not hdrFallback                    → reject
sizeGB > 30 and resolution != "2160p"              → reject
avail.onMyBackbone                                 → +500
probed.bitDepth >= 10                              → +400
releaseName matches "(?i)\bIMAX\b"                 → +2000
```

## Anatomy

| Field | What it does |
|---|---|
| **Name** | What the rule is called. Shows in the score breakdown, in History, and in [custom result formats](result-formatting.md) via `.MatchedRules`. |
| **Condition** | An expression that answers yes or no. |
| **Applies to** | All content, or one kind (Movies, Shows, Anime films, Anime shows). |
| **Action** | **Score** adds points (negative allowed). **Reject** removes the release. **Limit** caps how many matching releases you are offered. |
| **Keep best** | For limit rules: how many survive. The best that many by final score are kept, the rest dropped. |
| **Enabled** | Turn a rule off without deleting it. A disabled rule is not compiled, so a half-written one never blocks a save. |

Every field is editable in place. A condition that will not compile rejects the
save and names the rule, the same way a broken regex always has.

## Limits

A condition describes one release, so it cannot say "at most three of these" —
that is about the result set. Limit is therefore an **action**, not a function
inside a condition:

```
resolution == "2160p"                     → keep best 3
quality == "WEB-DL" and isAnime           → keep best 5
true                                      → keep best 10
```

The survivors are the **best** matching releases by final score, not the first
ones the indexer happened to return, so a cap never costs you the release you
wanted. Limits are applied last, after every point is in and the list is in
order — which is the only point at which "the best three" means anything.

Rules count independently. A release dropped by one cap does not take a slot in
another: it is gone, so it is not competing. A release that falls past a cap is
rejected like any other, with a reason that says so —
`rule: At most 3 in 4K (over the limit of 3)`.

This is separate from the stream's own **Results limit**, which truncates the
finished list. A limit rule shapes *what* is in the list; the stream setting
decides how long it is.

## Where rules run

Rules run **inside** the profile, after everything cheap:

```
jhin (title traits, languages, patterns) → NZB limits → NZB scoring
  → library bonus → RULES → minimum score → sort → LIMITS
```

Two consequences worth knowing:

- Rule points count towards the **minimum score**, because the floor judges the
  finished score rather than what the title alone earned.
- A rule rejection appears in History and the search debug stream like any
  other, prefixed `rule: <name>`, so a profile that empties a result list tells
  you which rule did it.

Rules do not run for streams in AIOStreams mode, which return raw results by
design — same as profiles.

## Confidence: what a rule can read, and how much to trust it

The attribute namespace is grouped by **how much the value can be trusted**,
not by which part of StreamNZB produced it. That is the distinction that
decides how to write a rule — and whether it can run at all.

| Tier | Source | Coverage |
|---|---|---|
| **inferred** | Read out of the release name | Every release. Never stale, frequently wrong. |
| **reported** | Claimed by the indexer | Near-total. Fresh, unverified. |
| **community** | AvailNZB, per backbone | Partial. A record can be months old. |
| **measured** | ffprobe, in the file itself | Library releases only. Ground truth. |

### inferred — from the release name

`resolution` `quality` `codec` `bitDepth` `hdr` `dolbyVision` `hdrFallback`
`audio` `channels` `languages` `group` `edition` `container` `year`
`seasonPack` `proper` `repack` `remastered` `upscaled` `threeD` `dubbed`
`subbed` `hardcoded` `complete` `verified`

Also `parsed.resolution`, `parsed.codec`, `parsed.hdr`, `parsed.bitDepth`,
`parsed.dolbyVision`, `parsed.hdrFallback`, `parsed.title`.

The bare names give the **best value available** — what ffprobe measured when
the file has been opened, what the name claimed otherwise — and `verified`
reports which one answered. `parsed.*` is always the name's own account, so a
rule can insist on one or the other:

```
resolution == "2160p"                  # measured 4K, or claimed 4K if unprobed
parsed.resolution == "2160p"           # the name says 4K, whatever the file is
verified and resolution == "2160p"     # measured 4K, and only measured
```

Two notes on HDR. The parser writes plain HDR10 as `"HDR"`, so `"HDR10" in hdr`
is never what you want. Use `dolbyVision` and `hdrFallback` instead — they mean
the same thing whichever tier answered, and `hdrFallback` is the one that
matters: it is false for SDR and for DV-with-no-base-layer alike, which is
exactly the distinction a device without Dolby Vision support cares about.

### reported — from the indexer

`releaseName` `sizeGB` `sizePerEpisodeGB` `ageDays` `grabs` `passworded`
`indexer` `querySource` `library`

`sizeGB` is the whole release. `sizePerEpisodeGB` is usually what you want: the
whole release for films and single episodes, the per-episode share for a
multi-episode release, and `-1` for a season pack whose episode count the title
does not reveal — so write size rules as
`sizePerEpisodeGB >= 0 and sizePerEpisodeGB > 30`.

`ageDays` is `-1` when the indexer reported no date. A missing date is not age
zero, so write freshness rules as `ageDays >= 0 and ageDays < 7`.

### community — from AvailNZB

`avail.status` `avail.known` `avail.onMyBackbone` `avail.checkedDaysAgo`
`avail.compression`

`avail.status` is three-valued: `"available"`, `"unavailable"`, `"unknown"`.
Unknown is the common case and means nobody has reported the release either
way — it is not a weaker form of bad news.

`avail.onMyBackbone` is the stronger signal: healthy on a backbone that this
stream's own providers sit behind. A release alive somewhere you cannot reach
is not a release you can play.

`avail.checkedDaysAgo` is `-1` when the record has no timestamp.

### measured — from ffprobe

`probed.height` `probed.width` `probed.videoCodec` `probed.audioCodec`
`probed.profile` `probed.bitDepth` `probed.hdr` `probed.dolbyVision`
`probed.hasHDRFallback` `probed.dynamicRange`

Only library releases have ever been opened, so these are only populated for
them. `probed.hdr` is the base layer (`"HDR10"`, `"HDR10+"`, `"HLG"`, or empty)
and `probed.dolbyVision` is independent of it — that pair is what makes
"Dolby Vision with no fallback" a measurement rather than a guess.

### Traits

`traits` is every attribute the parser detected, by the same keys the preset
baseline scores: `"remux"`, `"bluray"`, `"webdl"`, `"webrip"`, `"cam"`,
`"hevc"`, `"av1"`, `"dolby_vision"`, `"hdr10plus"`, `"10bit"`, `"atmos"`,
`"dual_audio"`, and so on. The rule editor lists the full set.

This is what makes rules a complete replacement for the old per-trait controls:

```
"cam" in traits                            → reject      (was: block the CAM trait)
"remux" in traits                          → +4000       (was: raise the remux slider)
"webrip" in traits and resolution == "2160p" → reject     (was: not expressible)
```

### About the request

`kind` `isAnime` `season` `episode` `title`

## Fail-open

**A rule that reads `probed.*` or `avail.*` does not run on a release that has
nothing in that tier.** It is skipped, not failed.

Without this, one rule like `probed.height < 1080 → reject` would empty every
result list of everything except library hits — every fresh indexer hit has a
probed height of zero. This is the same fail-open contract the NZB attribute
limits keep ("a release that doesn't report a date is never rejected for it"),
but here it is the *common* case rather than the exception, so the editor marks
affected rules and the preview lists what it skipped and why.

Practical consequence: a probe rule can only ever *reward* or *remove* library
releases. It cannot be used to demote everything else by omission.

## Syntax

Standard expression syntax, powered by
[expr](https://github.com/expr-lang/expr):

| | |
|---|---|
| Comparison | `==` `!=` `<` `<=` `>` `>=` |
| Logic | `and` `or` `not` |
| Membership | `"DV" in hdr` |
| Text | `releaseName matches "(?i)regex"`, `releaseName contains "IMAX"`, `startsWith`, `endsWith` |
| Arithmetic | `+` `-` `*` `/` |
| Grouping | `( … )` |
| Conditional | `cond ? a : b` |

`matches` takes a Go (RE2) regular expression. RE2 has no lookahead or
lookbehind, and rules are the reason it does not need one: `\bDV\b(?!.*HDR10)`
becomes `dolbyVision and not hdrFallback`.

## Coming from AIOStreams

If you know [AIOStreams' Stream Expression
Language](https://github.com/Viren070/AIOStreams/wiki/Stream-Expression-Language),
the operators carry over unchanged — `and`, `or`, `not`, `in`, `? :`,
comparisons all mean what you expect. The function vocabulary does not, because
it describes torrents and debrid services. This is a translation table, not a
compatibility layer:

| SEL | StreamNZB | Note |
|---|---|---|
| `resolution(streams, '2160p')` | `resolution == "2160p"` | SEL selects from a list; a rule judges one release. |
| `quality(...)` | `quality == "WEB-DL"` | |
| `encode(...)` | `codec == "x265"` | |
| `visualTag(...)` | `"DV" in hdr`, `dolbyVision` | |
| `audioTag(...)` / `audioChannels(...)` | `"TrueHD" in audio` / `"7.1" in channels` | |
| `language(...)` | `"en" in languages` | Or the Languages controls, which reject rather than score. |
| `releaseGroup(...)` | `group == "FraMeSToR"` | |
| `size(...)` | `sizeGB > 30` | Decimal GB. |
| `age(...)` | `ageDays` | `-1` when unknown, unlike SEL. |
| `seasonPack(...)` | `seasonPack` | |
| `addon(...)` | `indexer` | |
| `library(...)` | `library` | |
| `seeders(...)` | `grabs` | Closest usenet analogue: how many people fetched it. |
| `cached()` / `uncached()` | `avail.status`, `avail.onMyBackbone` | **Not equivalent.** SEL's `cached` is a guarantee from a debrid service; ours is a community report that can be months stale and is per backbone. |
| `service(...)`, `type(...)`, `seadex(...)` | — | No analogue. StreamNZB has one source type. |
| `regexMatched()` / `regexScore()` | `releaseName matches "…"` | |
| `count()`, `max()`, `avg()`, `median()`, … | — | Rules judge one release at a time, so there is no list to aggregate. |
| — | `probed.*` | No SEL analogue: measured from the file. |
| — | `avail.onMyBackbone` | No SEL analogue. |
| — | `passworded`, `avail.compression` | No SEL analogue. |

## Upgrading

Weighted patterns migrate to rules automatically on first load. Each becomes a
score rule named after its pattern, with the condition
`releaseName matches "…"` and the same points, and the pattern is cleared from
the old list so nothing is scored twice. Rename them from there — a rule named
`Dual Audio` reads better in a score breakdown than `\bDual[. _-]?Audio\b`, and
the name is what [custom result formats](result-formatting.md) see.

**Must match**, **Never match** and **Prefer** are unchanged. They still live on
the Eligibility and Ranking tabs as plain pattern lists, because a single
pattern is the right tool when a single pattern is enough.
