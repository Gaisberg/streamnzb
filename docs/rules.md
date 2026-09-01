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
release — through the [result-set functions](#about-the-result-set), what else
the search turned up, and through [`matched()`](#referring-to-another-rule),
what your other rules already say about it.

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
| **Action** | **Score** adds points (negative allowed). **Reject** removes the release. **Limit** caps how many matching releases you are offered. **Prune** removes the release [after scoring](#pruning-after-the-score), when its condition can read `finalScore`, `finalRank` and `current.*`. **Define** does nothing at all — the rule exists to be [referenced](#referring-to-another-rule) from other rules. |
| **Keep best** | For limit rules: how many survive. The best that many by final score are kept, the rest dropped. |
| **Per** | For limit rules: what the cap is counted per. Nothing caps every match together; pick a grouping and the cap is kept once per value of it. |
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
Best 3 of each resolution: keep 3 per resolution if true
UHD BluRay T1: define if group in ["FraMeSToR", "W4NK3R"]
Weak tail: prune if finalScore < -500 and count(finalScore >= -500) >= 6
Old experiment [off]: score 100 if "remux" in traits
```

A line is `Name: action if condition`. The action is `score <points>` —
negative allowed — `reject`, `keep <n>`, `keep <n> per <grouping>`, `prune`, or
`define`.
Brackets before the colon carry the
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

### Keeping N of each

A cap can be counted **per group** instead of across the whole result set, which
is what turns a run of near-identical rules into one:

```
resolution == "2160p"  →  keep best 5
resolution == "1080p"  →  keep best 5     becomes    true  →  keep best 5 per resolution
resolution == "720p"   →  keep best 5
```

**Per** is a menu of the usual groupings — resolution, quality, codec,
release group, indexer — plus **Custom expression**, which takes anything the
[attributes](#confidence-what-a-rule-can-read-and-how-much-to-trust-it) above can
express. Combining two is the case worth knowing:

```
Best 3 of each flavour: keep 3 per resolution + " " + quality if true
```

That keeps the top three 2160p Remux, the top three 2160p WEB-DL, the top three
1080p BluRay, and so on, without a rule per combination.

Each group keeps its own best, for the same reason an ungrouped cap keeps the
set's: the cap is applied to the finished list, already in score order. A cap
that groups names the group when it turns a release away —
`rule: Best 3 of each resolution (over the limit of 3 for 2160p)` — because
"over the limit of 3" on a rule that offered nine releases reads as a
contradiction on its own.

Two things to know about a grouping:

- It is judged with the condition, not after it. Grouping by `probed.height`
  makes the whole rule measured-only, so it skips releases that have never been
  opened rather than bucketing them all together as height zero.
- Every distinct value is its own group, including the empty one. Grouping by an
  attribute a release name does not carry puts every such release in one group,
  which is usually what you want and is worth knowing when it is not.

This is separate from the stream's own **Results limit**, which truncates the
finished list. A limit rule shapes *what* is in the list; the stream setting
decides how long it is.

## Pruning after the score

A **prune** rule is a reject that runs after scoring instead of during it. Its
condition can read two attributes no other rule may touch:

- `finalScore` — the release's finished, accumulated score.
- `finalRank` — its position among the surviving results sorted by that score,
  1 being the best.

Both only exist once every point is in and the list is in order, which is why
they live behind their own action: a score rule reading `finalScore` would be
reading a number it is still helping to build, so the editor refuses it — with
the suggestion to use a prune rule instead.

The case prune exists for is the **adaptive weak filter**: not a global minimum
score, but "drop the very weak only while enough materially stronger
alternatives remain":

```
Weak tail: prune if finalScore < -500 and count(finalScore >= -500) >= 6
Deep cuts: prune if finalRank > 20 and finalScore < 0
```

When a search returns six healthy releases and three scraping the bottom, the
bottom three go; when the same profile meets a thin result set, they stay as
fallbacks. A fixed **minimum score** cannot say that — it prunes the same
releases whether or not anything better exists.

Everything a normal rule can read, a prune rule can read too — `finalScore`
and `finalRank` are additions, not replacements — and `matched("Rule name")`
still reaches your score, reject and define rules. Rules reading a tier the
release does not carry [fail open](#fail-open) here exactly as they do during
scoring.

The mechanics worth knowing:

- Prune rules only ever judge **survivors**: releases already rejected never
  reach them, and neither `finalScore` nor `finalRank` exists for one.
- `count()` / `exists()` / `none()` in a prune rule run over the surviving set
  with its final scores, and the counts are taken **before any prune rule
  fires** — pruning a release never changes what another rule counted, so the
  order of your prune rules does not matter.
- Pruning runs before [limits](#limits), so a pruned release does not use up a
  cap's slot, and `finalRank` is the rank *before* any cap trims the list.
- A prune rule cannot add points. That is deliberate: the score a prune
  condition reads is exactly the score that ordered the list, always.

A pruned release is rejected like any other, with `rule: <name>` as the
reason, so History and the preview say which rule removed it.

### Comparing against the release being judged

A threshold like `finalScore < -500` has to know which band your scores land
in. That is fine until the profile scores in wide bands — a clean 1080p WEB-DL
at `+20500` and the same release carrying a `-10000` penalty at `+10500` are
both positive, so no absolute number separates "good" from "materially worse
than what else is here". `finalScore < 15000` would work, but it encodes the
resolution's score band rather than the question you meant.

`current.finalScore` and `current.finalRank` ask the question directly. Inside
`count()`, a bare `finalScore` is the release being *counted*; `current.*` is
the release being *judged*:

```
Weak tail: prune if count(finalScore >= current.finalScore + 5000) >= 6
```

Read it as: remove this release only if at least six alternatives are at least
5000 points better. It holds whatever band the scores land in, because it is a
question about the gap. The same shape works on position —
`count(finalRank < current.finalRank) >= 6` is "at least six ranked above me" —
though plain `finalRank > 6` says that more directly.

Outside a result-set question the release being judged *is* this release, so
`current.finalScore` and `finalScore` are the same number there.

One cost worth knowing: a question comparing against `current.*` has a
different answer for every release, so it is counted once per release instead
of once for the set. Releases sharing a score share the answer, and a profile
that never writes `current.*` is unaffected, but on a very large result set
this is the one condition shape that is not free.

## Where rules run

Rules run **inside** the profile, after everything cheap:

```
jhin (title traits, languages, patterns) → NZB limits → NZB scoring
  → library bonus → RULES → minimum score → sort → PRUNE → LIMITS
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
| **community** | AvailNZB (per backbone) and SeaDex (per anime title) | Partial. An availability record can be months old; SeaDex covers cataloged anime only. |
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

Two on `languages`. It holds **ISO 639-1 codes** — `"en"`, `"ja"`, `"fr"` —
so `"eng"` and `"English"` match nothing and a rule written with either
compiles cleanly and then never fires. And it is **empty unless the title says
otherwise**: most English releases carry no language tag at all, so
`"en" in languages` finds the ones that announce it, not the ones that are in
English. To demote other languages rather than reward English, say so directly:

```
"en" in languages                      # the title claims English
not ("en" in languages)                # everything else, untagged included
"ja" in languages and not dubbed       # the ones you actually want to demote
```

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

### community — from SeaDex

`seadex.known` `seadex.best` `seadex.alternative`

[SeaDex](https://releases.moe) curates, per anime title, which release groups
produced the best and the notable alternative releases of *that* title — a
per-title judgment no static group tier can reproduce. The same group can be
best for one anime and unlisted for the next, which is exactly what these
attributes carry:

```
seadex.best                                → +1000
seadex.alternative                         → +500
```

`seadex.best` is true when this release's group made a release SeaDex marks
best for the requested title; `seadex.alternative` when the group is
recommended without the best mark; `seadex.known` when SeaDex has an entry for
the title at all. Matching is by release-group name, case-insensitively —
SeaDex catalogs torrents, so the recommendation transfers to usenet whenever
the same group's release circulates under its group tag.

The lookup runs only for anime requested through Kitsu (SeaDex is keyed by
AniList id, which the anime-lists import maps per Kitsu entry), and only when
a rule or a [custom result format](result-formatting.md) actually reads these
attributes. Answers are cached for a day. The base URL can be overridden with
`STREAMNZB_SEADEX_BASE_URL`.

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

### After scoring — prune rules only

`finalScore` `finalRank` `current.finalScore` `current.finalRank`

The finished verdict: the accumulated score, and the 1-based position among
the surviving results sorted by it. They only exist once every rule has run,
so only a [prune rule](#pruning-after-the-score) can read them.

`current.*` is the release being judged, which differs from the bare names in
exactly one place: inside `count()`, `exists()` and `none()`, where a bare
`finalScore` is the release being counted. That is what lets a rule ask how
many alternatives beat this one by a margin — see [comparing against the
release being judged](#comparing-against-the-release-being-judged).

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
action](#limits), and a condition over final scores belongs to a [prune
rule](#pruning-after-the-score), where the set carries them — including
comparisons against [the release being
judged](#comparing-against-the-release-being-judged).

Fail-open extends to the set. A release missing a tier the inner condition
reads is not counted — `count(probed.height >= 2000)` counts probed releases
only — and when **no** release in the set carries that tier the question is
unanswerable, so the rule is skipped rather than fed a zero: on a fresh search
where nothing has been probed, `none(probed.height >= 2000)` must not read as
"there is no good 4K" and reject everything.

One name-sharing note: the collection builtins keep their meaning. The
two-argument forms over a list attribute — `any(hdr, # == "DV")` — and
`count([proper, repack])` over a literal list still judge the single release,
exactly as before. Only the one-argument condition form reads the result set.

The preview shows its work: each result-set condition is listed above the
results with what it counted and **which releases it counted**, so
`exists(...)` coming out true — or `none(...)` coming out false — is traceable
to the releases that caused it rather than left to be inferred from the rules
it fired.

### Referring to another rule

`matched("Rule name")`

Everything above is read off the release. This one reads your own rules: it
holds when the named rule's condition holds for the release being judged, so a
classification is written once and referred to from wherever else it matters.

The case it exists for is a tier list. Three rules already say which release
groups are trusted; a fourth rejects an untrusted 4K encode when a remux is on
offer, without a fourth copy of the group patterns:

```
Movies UHD BluRay T1: score 3000 if group in ["FraMeSToR", …]
Movies UHD BluRay T2: score 2000 if group in [ … ]
Movies UHD BluRay T3: score 1000 if group in [ … ]

Untrusted UHD encode: reject if
    resolution == "2160p" and "bluray" in traits
    and not (
        matched("Movies UHD BluRay T1")
        or matched("Movies UHD BluRay T2")
        or matched("Movies UHD BluRay T3")
    )
    and exists(resolution == "2160p" and "remux" in traits)
```

Change a tier list afterwards and the rejection follows it, which is the whole
point: the alternative is the same regexes in four places and three of them
going stale.

A reference is resolved when the profile is compiled, by copying the other
rule's condition into this one. Four consequences follow from that:

- **A reference works wherever a condition does** — on its own, inside a
  result-set call (`exists(matched("Movies Remux T1"))`), inside a limit's
  grouping. Nothing is evaluated twice.
- **Order does not matter, and neither does the other rule's action.** A rule
  may name one written below it. What it asks is whether that rule's
  *condition* holds, not what its action did with the release — for a score or
  reject rule those are the same thing; on a cap it means "counts against it",
  which is the only part of a cap knowable before the final ordering exists.
- **The referenced rule's scope comes with it.** A reference to a rule that
  applies to movies only is false for a series, whatever scope the referring
  rule has.
- **So does its tier.** Referring to a rule that reads `probed.*` makes the
  referring rule probe-dependent, and it is [skipped](#fail-open) on a release
  that was never opened rather than judged as not matching.

When a condition exists *only* to be referenced — a tier list nothing should
score by itself, a release-group regex maintained in one place — give it the
**Define** action. A define rule is a named condition and nothing more: it
never pays out, never rejects, and never appears in a score breakdown or
`.MatchedRules`, so it cannot leak points into results the way a
zero-point score rule still leaks its name. It is validated as strictly as any
other rule when the profile is saved, referenced whether or not anything names
it yet, and it is the natural home for the patterns that track upstream tier
lists:

```
UHD BluRay T1: define if group matches "(?i)^(FraMeSToR|W4NK3R|...)$"
UHD BluRay T2: define if group matches "(?i)^(HiFi|Positive|...)$"

Trusted 4K: score 3000 if resolution == "2160p" and matched("UHD BluRay T1")
Known 4K: score 1500 if resolution == "2160p" and matched("UHD BluRay T2")
Untrusted UHD encode: reject if
    resolution == "2160p" and "bluray" in traits
    and not (matched("UHD BluRay T1") or matched("UHD BluRay T2"))
    and exists(resolution == "2160p" and "remux" in traits)
```

When the upstream list changes, one definition changes with it.

A rule that is switched off classifies nothing, so a reference to it is simply
never true — and its condition is never looked at, which is what keeps a broken
rule you have turned off from blocking a save.

Rules are referenced by name. Renaming one in the editor rewrites the
references to it, and the reference panel under the editor lists your rules to
click. A reference to a name no rule has, to a name two rules share, or one
that closes a circle is refused when the profile is saved, naming the rule that
carries it.

### Define libraries

A set of defines every profile wants — release-group tiers, community lists —
does not have to be copied into each profile. A **define library** (bottom of
the Filters page) holds define rules once, and every filter profile references
them with the same `matched("Name")`; the reference panel lists them under
**Library defines**. Libraries carry *only* defines — a library cannot score,
reject or cap anything, so the data it maintains and what your profile does
with that data stay separate by construction.

References resolve against the profile's own rules first: a rule you write
under a library define's name **shadows** the library's version, which is how
one entry is overridden without forking the library. Deleting a library (or
refreshing away a define) that profiles still reference is refused at save
with the usual unknown-name error, and the delete confirmation names the
profiles that would break.

Libraries share the remote-source mechanism of
[linked profiles](filters.md#remote-profiles) — import from a URL, manual
Refresh, a confirmation diff — with one difference: a library is the
maintainer's data, so a refresh replaces its defines wholesale rather than
merging. See [Define libraries](filters.md#define-libraries) for the file
formats and the refresh contract.

## Fail-open

**A rule that reads `probed.*`, `avail.*` or `seadex.*` does not run on a
release that has nothing in that tier.** It is skipped, not failed. For
`seadex.*` the tier is per request rather than per release: the rules run when
the lookup ran — an anime SeaDex has not cataloged is then an honest
`seadex.known == false` — and are skipped when it could not (not a Kitsu
request, no AniList mapping, SeaDex unreachable).

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
| From SeaDex | every `seadex.*` attribute — name the pretend best/alternative groups and each sampled release is matched by its own parsed group |

Each group is off by default, because pretending by default would be the same
trap in a different coat. Turn one on and its rules are answered with the values
you set; leave it off and they stay reported as unjudgeable.

The values apply to every release name in the list, so to compare a large
release against a small one, change the size and read the list again.

Note this is a preview limitation, not the fail-open contract above. Indexer
rules always run in a live search — it is only a pasted name that has nothing
to answer them with.

## Syntax

Standard expression syntax, powered by the rule engine of
[jhin](https://github.com/dreulavelle/jhin) — the same library that parses
release names:

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

Write the regex as-is: backslashes in a condition string are taken literally,
so `\+`, `\d` and `\b` mean what they mean in the regex — no doubling needed,
though a defensively written `\\+` means the same thing. This is what lets a
[define library](#define-libraries) generated from an upstream regex list be
consumed without rewriting its escapes.

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
| `service(...)`, `type(...)` | — | No analogue. StreamNZB has one source type. |
| `seadex(...)` | `seadex.best`, `seadex.alternative`, `seadex.known` | Matched per title by release group, and skipped when no lookup could run. |
| `regexMatched()` / `regexScore()` | `releaseName matches "…"` | |
| `count()` | `count(condition)` | Over the [result set](#about-the-result-set); `exists()` and `none()` alongside it. |
| `max()`, `avg()`, `median()`, … | — | No analogue: rules ask about the set, they do not select from it. |
| — | `matched("Rule name")` | No SEL analogue: a rule may reuse another rule's condition rather than repeat it. |
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
