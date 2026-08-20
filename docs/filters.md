# Filters & ranking

A filter profile decides which releases a stream offers, how they are scored,
and what order they arrive in. A profile is two things and nothing else:

- a **preset** — 4K, 1080p or 720p — which sets the resolution ceiling and
  carries every other ranking default;
- **[rules](rules.md)** — named conditions over everything known about a
  release, which move its score, reject it, or cap how many like it you are
  offered.

Profiles are defined on the **Filters** page (under Settings) and take effect
only when a stream selects one. An unbound profile does nothing, and a stream
with no profile returns every release unfiltered.

Parsing and scoring are powered by [jhin](https://github.com/dreulavelle/jhin):
every release name is parsed into traits (source, codec, HDR, audio, languages,
…) and each trait contributes a score.

## Presets

The **Quality** tab, and for most people the only decision here: pick the
largest screen you watch on. The three presets differ **only in the resolution
ceiling**. Everything else —
which sources are worth what, which garbage to refuse, how to treat a title that
could not be parsed — has one right answer, so it is a default rather than a
question.

| Preset | Offers | For |
|---|---|---|
| **4K** | 2160p · 1440p · 1080p · 720p | Largest files, best picture |
| **1080p** | 1080p · 720p | Smaller files, kinder to a shared connection |
| **720p** | 720p | Slow lines and small screens |

The baseline behind all three is deliberately permissive. It rejects camcorder,
telesync, telecine and screener rips, the source-less junk that carries no
provenance (leaked copies, pre-retail rips, deleted-scene reels), and adult
releases. Everything else is *scored*, not blocked, so a poor release sorts last
rather than disappearing. Unparsable resolutions are kept for the same reason —
a name nobody could read is not evidence of a bad release.

There is no score floor. Rejecting is what rules are for, and a rule says why.

The preset decides which resolutions are offered. It does not decide the order
they arrive in — the score does, and only the score. A resolution is worth
20000 points per tier, so 4K leads a list nobody has written a rule about, but a
rule that pays more than that can put a 1080p release first. See
[Order is score, and only score](#order-is-score-and-only-score).

### Tuned for streaming, not for downloading

The scoring comes from jhin, which is built for a downloader: fetch the very
best copy and keep it, where size costs you disk once. StreamNZB assembles the
file from usenet articles *while it plays*, so size is a running cost paid on
every playback. Three places where "best copy" and "best copy to stream" differ:

- **Remux scores 1500, not jhin's 10000.** At 10000 nothing could outweigh it,
  so a remux always won however much bandwidth it cost. It is still clearly the
  best source — WEB-DL scores 200 — without being the only thing that matters.
- **Modern codecs are preferred.** HEVC and AV1 score 700, AVC 300: the same
  picture in fewer bytes is exactly the currency here. This is a uniform
  preference, not a guess about your player — what a device can decode is the
  device's business, and nothing is hidden. An AVC release still sorts, just
  below its equivalent.
- **Size is scored.** Each preset knows roughly what a good copy weighs — 20 GB
  for a 4K film, 6 GB for a 4K episode, 8 GB and 2.5 GB at 1080p — and scores
  full marks at that size, tapering to nothing at twice it. Nothing is rejected
  for size; an oversized release simply stops earning.

Together these turn what used to be a landslide into a real choice:

```
 10250   20GB  2160p WEB-DL DV HDR10+ HEVC DDP5.1 Atmos
 10200   70GB  2160p UHD BluRay REMUX DV HDR HEVC TrueHD 7.1 Atmos
  5450   16GB  2160p WEB-DL HDR HEVC DDP5.1
  1950    6GB  1080p WEB-DL DDP5.1 x265
  1850    8GB  1080p WEB-DL DDP5.1 H.264
```

The 70 GB remux and the 20 GB WEB-DL now finish within 50 points of each other,
so availability and grab count decide between them rather than source alone.
Want the remux back on top regardless? That is one rule:
`"remux" in traits → +5000`.

## Rules

The **Rules** tab. Everything beyond the preset is a rule. Rules can read the release name, what
the indexer reported, what the community availability database says, and what
ffprobe measured in library files — combined with `and` / `or` / `not`, and
scoped to a content kind.

```
dolbyVision and not hdrFallback                        → reject
sizePerEpisodeGB >= 0 and sizePerEpisodeGB > 30        → reject
quality == "WEB-DL" and group in ["FraMeSToR", "NTb"]  → +500
isAnime and "remux" in traits                          → +1000
avail.onMyBackbone                                     → +500
resolution == "2160p"                                  → keep best 3
```

`traits` is the whole vocabulary the baseline scores by — `"remux"`, `"webrip"`,
`"cam"`, `"hevc"`, `"10bit"`, `"dual_audio"` and so on — so a rule can reach
anything the baseline has an opinion about. See **[Rules](rules.md)** for the
complete attribute reference, the operators, and the fail-open contract.

## Order is score, and only score

Every release ends up with one number, and the list is that number, highest
first. Resolution, source and codec pay into it; the NZB attribute scoring pays
for size, age and grab count; the library bonus and your rules pay on top.
Nothing sorts ahead of the total.

Resolution is priced at **20000 points a tier** — 2160p 60000, 1440p 40000,
1080p 20000, 720p 0, and an unparsable resolution alongside 720p. The step is
deliberately wider than everything else the baseline scores (a remux is 1500,
HEVC 700, the preferred-language bonus 10000), so no combination of them
crosses a tier and the default order is still every 4K release, then every
1080p one. What it buys you is a price to beat: a rule worth 20000 moves its
releases up one tier, 80000 moves them past the lot.

That is worth stating plainly because it used to be untrue. Resolution sorted
first as a hard bracket, and the score only broke ties inside it — so a rule
worth 80000 points to prefer a language put its releases at the top of their own
resolution and behind every 4K release, which is not what a number that large
can be read to mean. If you want a preference to win, price it above what it is
competing with; if you want it to win only among equals, price it low.

**Scores got bigger.** A plain 1080p WEB-DL used to score 500 and now scores
20500. Nothing about the order between two releases changed except that
resolution can now be outbid, but a hand-written score floor from the old
editor is worth re-checking: it is measured against the larger numbers, so it
now lets more through.

A resolution you never want is a job for the preset (which does not offer it) or
a rule (which rejects it and says so), not for the order.

## Binding a profile to a stream

On the **Streams** page, each stream's **General** tab has a **Filter/Sorting**
dropdown: `None` (everything unfiltered), `AIOStreams` (raw results for
[AIOStreams](aiostreams.md) to filter on its side), or one of your profiles.

Below it, **By content type** overrides the chosen profile for specific kinds:
**Movies**, **Shows**, **Anime films**, **Anime shows**. Anything left on Default
uses the main profile. Exactly one profile applies per request — the per-kind
binding wins, profiles never combine. Anime detection uses Kitsu when the
request comes from a Kitsu catalog, otherwise TMDB genres (animation not
originally in English), which needs TMDB configured.

Renaming a profile updates every stream that uses it; deleting one clears it
from those streams, which then fall back to unfiltered.

Note: the default stream created on first install is set to AIOStreams mode, so
the default profile is *not* applied until you select it on a stream.

## Preview

The panel under the rules: paste release names (one per
line), optionally a title to match against, and pick which content kind to
**judge as**. Each release shows offered/rejected, its score, the rules that paid
out, and an expandable breakdown of exactly which clauses contributed what. It
re-evaluates as you type and includes unsaved edits.

It is also what the rules list measures against: the per-rule counts there
(*pays out on 2 of 4*) come from this same evaluation, so editing the release
names changes both.

A release name carries no file measurements and no availability record, so rules
reading `probed.*` or `avail.*` are reported under **Not judged here** rather
than looking broken — which is exactly what happens to them against any release
that has never been probed or reported.

## Sharing profiles

Two formats, for two jobs:

- **Share code** — a compact `SNZBP1:` string. Pastes into a chat window
  intact. Import accepts it directly.
- **JSON** — the profile as a readable file: preset, rules, nothing else. This
  is the one to commit to a repository, review in a pull request, or hand-edit.
  **Download** saves it as `<name>.streamnzb.json`.

```json
{
  "streamnzb_profile": 1,
  "name": "Samsung QN90A",
  "preset": "4k",
  "rules": [
    { "name": "DV without HDR fallback", "when": "dolbyVision and not hdrFallback", "action": "reject" },
    { "name": "WEB-DL tier 1", "when": "quality == \"WEB-DL\" and group in [\"NTb\", \"FLUX\"]", "points": 500 }
  ]
}
```

Import takes either format in the same box — a share code is base64 and a
profile file starts with `{`, so there is nothing to choose. An import is always
added as a new profile and never overwrites an existing one; a name collision
gets a numeric suffix.

## Upgrading from the old editor

Profiles tuned before presets existed are converted on first load, and **nothing
is lost**: every setting that used to have its own control becomes a rule with a
name.

| Was | Becomes |
|---|---|
| Enabled resolutions | The preset matching the highest tier you had on |
| A blocked trait | `"webrip" in traits` → reject |
| A re-scored trait | `"remux" in traits` → the difference from the baseline |
| Must match / Never match | A reject rule on the pattern |
| Preferred patterns | A score rule each, at the preference bonus |
| Excluded / required languages | A reject rule |
| NZB size / age / grab limits | A reject rule, per content kind where you set one |
| Weighted patterns | A score rule each |

Size rules use `sizePerEpisodeGB`, which is what the limits always judged: the
whole release for films and single episodes, the per-episode share for a
multi-episode release, and `-1` for a season pack whose episode count the title
does not reveal. A rule against the total would reject packs for being packs.

Migrated rules are named after whatever they came from — rename them; the name
is what shows in the score breakdown, in History, and in
[custom result formats](result-formatting.md).

A profile that only ever used the recommended values migrates to a bare preset
with no rules at all.

One thing genuinely is dropped: a hand-written `resolution_order` no longer does
anything, because resolution is priced into the score rather than sorted ahead
of it. "Prefer 1080p without banning 4K" is now a rule — `resolution == "1080p"`
worth more than 20000 — which says the same thing in the same currency as
everything else, and says it out loud.

## Seeing what a profile did

The **History** page shows the full funnel per search: raw results → validated →
deduped → known-bad removed → **Profile (name) input → kept**, with a separate
count for what rules rejected. Expanding **Rejected by profile** lists every
dropped release with its reason codes (`trash`, `resolution:480p`,
`language:missing_required`, `attribute:cam`, `rule: <name>`, …). Rule
rejections are counted separately because "the baseline blocked it" and "a rule
you wrote blocked it" send you to very different places.

A profile that filters everything out looks identical to an indexer that found
nothing unless you look here — so look here first when streams come back empty.
See also the **Search debug stream** toggle under Advanced, which prepends the
same summary as a result row in Stremio.
