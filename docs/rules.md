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
- *The shaky 4K encode, but only when a remux stands ready to replace it.*

A regular expression sees one string. A rule sees everything known about the
release — and, through the [result-set functions](#about-the-result-set),
what else the search turned up.

```
dolbyVision and not hdrFallback                    → reject
sizeGB > 30 and resolution != "2160p"              → reject
avail.onMyBackbone                                 → +500
probed.bitDepth >= 10                              → +400
releaseName matches "(?i)\bIMAX\b"                 → +2000
upscaled and exists("remux" in traits)             → reject
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

Every field is editable in place, and **Duplicate** drops a copy directly below
the original — tiers get built in runs (T1, T2, T3), and the same rule is often
copied between anime shows and anime films with only the scope changed.

A condition that will not compile rejects the save and names the rule, the same
way a broken regex always has.

## Writing them as text

Cards are one rule at a time. **Text** is all of them at once — the same rules
as lines, which is the shape that suits writing ten in a sitting, reordering
them, or pasting a set someone shared:

```
Atmos: score -800 if "atmos" in traits
DV without HDR fallback: reject if dolbyVision and not hdrFallback
At most 3 in 4K [movie]: keep 3 if resolution == "2160p"
Old experiment [off]: score 100 if "remux" in traits
```

A line is `Name: action if condition`. The action is `score <points>` —
negative allowed — `reject`, or `keep <n>`. Brackets before the colon carry the
scope (`movie`, `series`, `anime_movie`, `anime_show`) and `off` for a disabled
rule, in either order and both optional. Everything after `if` is the
condition, handed to the expression language exactly as written: this grammar
wraps conditions, it never parses them.

Both views are the same rules. Switching to text seeds it from the cards, and
every edit that parses is applied as you type. A line that does not parse is
reported with its number and **not** applied, so a half-typed rule never
replaces a working one. Two things are normalized on the way in: a condition
written across several lines in a card is folded onto one, and a score rule
that never had points recorded is written as `score 0`.

## Limits

"At most three of these" is about the **final score order** — which three are
best is only known after every point is in and the list is sorted, later than
any condition runs. Limit is therefore an **action**, not a function inside a
condition:

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

### About the result set

`count(condition)` `exists(condition)` `none(condition)`

Everything above describes the release being judged. These three ask about the
**whole result set**, which is what turns an unconditional rejection into a
conditional one — the main reason to want them is *reject X only when a better
Y exists*, rather than rejecting X and hoping:

```
upscaled and exists(resolution == "2160p" and "remux" in traits)   → reject
count(resolution == "2160p") < 3                                   → +500
none(quality == "WEB-DL")                                          → +200
```

The condition inside reads the same attributes, but against every release in
the set: `count()` is how many satisfy it, `exists()` whether any does, and
`none()` whether none does. `any()` is accepted as another name for `exists()`.
They cannot nest.

The set they see is fixed before any rule fires: every release still standing
after the eligibility patterns and the NZB limits, **including the one being
judged** — a 4K remux always has `count(resolution == "2160p") >= 1`. Because
the counts are taken first, a rule that rejects can never change what another
rule counted, so the order of your rules does not matter. What they cannot see
is the final ordering; "the best three of these" is still the [limit
action](#limits).

Fail-open extends to the set. A release missing a tier the inner condition
reads is not counted — `count(probed.height >= 2000)` counts probed releases
only — and when **no** release in the set carries that tier the question is
unanswerable, so the rule is skipped rather than fed a zero: on a fresh search
where nothing has been probed, `none(probed.height >= 2000)` must not read as
"there is no good 4K" and reject everything.

One name-sharing note: expr's own collection builtins keep their meaning. The
two-argument forms over a list attribute — `any(hdr, # == "DV")` — and
`count([proper, repack])` over a literal list still judge the single release,
exactly as before. Only the one-argument condition form reads the result set.

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

### Testing rules the preview cannot answer on its own

`sizeGB`, `sizePerEpisodeGB`, `ageDays`, `grabs`, `passworded`, `indexer`,
`querySource` and `library` come from the NZB. Every real result has one; a
release name pasted into the preview does not — and neither has it been probed
or reported to AvailNZB.

Rather than judge those rules against zeros — which would show a `grabs < 5`
rule rejecting everything when against real results it will do nothing of the
sort — the preview reports them as **cannot be judged here** until you say what
to assume. **Pretend the release also has** in the preview supplies exactly
that, in three opt-in groups matching the three tiers:

| Group | Answers rules about |
|---|---|
| From the indexer | `sizeGB`, `sizePerEpisodeGB`, `ageDays`, `grabs`, `passworded`, `indexer`, `library` |
| From ffprobe | every `probed.*` attribute |
| From AvailNZB | every `avail.*` attribute |

Each group is off by default, because pretending by default would be the same
trap in a different coat. Turn one on and its rules are answered with the values
you set; leave it off and they stay reported as unjudgeable.

The values apply to every release name in the list, so to compare a large
release against a small one, change the size and read the list again.

Note this is a preview limitation, not the fail-open contract above. Indexer
rules always run in a live search — it is only a pasted name that has nothing
to answer them with.

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
| `count()` | `count(condition)` | Over the [result set](#about-the-result-set); `exists()` and `none()` alongside it. |
| `max()`, `avg()`, `median()`, … | — | No analogue: rules ask about the set, they do not select from it. |
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
