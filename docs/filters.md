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

**The baseline scores no language.** It used to add 10000 for English, but that
bonus only paid on a release whose *title* names a language — and most English
releases never say so, which made it a coin flip between an untagged English
release and a tagged one rather than a preference for English. A profile that
wants a language ranked writes a rule: `"en" in languages → +500`. See
[the languages note](rules.md#inferred--from-the-release-name) for the codes.

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
finalScore < -500 and count(finalScore >= -500) >= 6   → prune
```

`traits` is the whole vocabulary the baseline scores by — `"remux"`, `"webrip"`,
`"cam"`, `"hevc"`, `"10bit"`, `"dual_audio"` and so on — so a rule can reach
anything the baseline has an opinion about.

Rules also compose. `exists(…)`, `count(…)` and `none(…)` ask about the whole
result set rather than one release, which is what turns an unconditional
rejection into *reject this only when something better turned up*; and
`matched("Other rule")` holds when that rule's own condition holds, so a tier
list of trusted release groups is written once and referred to from everywhere
else that cares about it.

See **[Rules](rules.md)** for the complete attribute reference, the operators,
and the fail-open contract.

## Order is score, and only score

Every release ends up with one number, and the list is that number, highest
first. Resolution, source and codec pay into it; the NZB attribute scoring pays
for size, age and grab count; the library bonus and your rules pay on top.
Nothing sorts ahead of the total.

Resolution is priced at **20000 points a tier** — 2160p 60000, 1440p 40000,
1080p 20000, 720p 0, and an unparsable resolution alongside 720p. The step is
deliberately wider than everything else the baseline scores (a remux is 1500,
HEVC 700), so no combination of them crosses a tier and the default order is
still every 4K release, then every 1080p one. What it buys you is a price to beat: a rule worth 20000 moves its
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

A release name carries no size, no file measurements and no availability
record, so rules reading those are reported as **cannot be judged here** rather
than answered against nothing. **Pretend the release also has** supplies them —
size, age and grabs; a probed file; an availability record — in three opt-in
groups, so those rules become testable too. See
[Rules](rules.md#testing-rules-the-preview-cannot-answer-on-its-own).

## Sharing profiles

**Export** turns the whole profile — preset and rules — into one `SNZBP1:`
string. It survives a chat window: the code is picked out of any prose around
it, smart-quote mangling is undone, and a code wrapped across lines is put back
together.

**Import** takes that code and adds it as a new profile. It never overwrites an
existing one; a name collision gets a numeric suffix. A code that arrives
damaged says so, and one written before presets existed says that instead of
importing something this editor cannot express.

The import dialog also offers **community templates** — a short, curated list
of known-good sources. Picking one fills the URL field and nothing more: the
import runs exactly as it would for a URL you typed, including the
[link-and-refresh](#remote-profiles) behaviour, so a template import keeps
receiving its maintainer's updates. The same dropdown appears when importing
define libraries and result formats, each with its own list.

To version a profile or review one in a pull request, paste its code — it is a
single line, and importing it is how it gets read back.

### Share code versioning

A share code is versioned twice, and the two numbers answer different
questions.

The **prefix** (`SNZBP1:`, `SNZBF1:` for format profiles, `SNZBD1:` for define
libraries) versions the container — base64url around gzip around JSON. It
changes only if that transport itself ever changes.

The **schema marker** inside the payload (`"streamnzb_profile": 1`,
`"streamnzb_format_profile": 1`, `"streamnzb_define_library": 1`) versions
what the JSON means. It is deliberately not the app version: it increments
only when an older importer would otherwise accept a code and silently
misread it — a new load-bearing field, a changed meaning for an existing one.
Additions an old importer already refuses loudly on its own — a new rule
action, a new preset, a new expression identifier the server refuses to
compile — do not move it, which is why most releases leave it untouched.

A code carrying a higher schema version than the importer understands is
refused with an explicit "made by a newer StreamNZB — update to import it",
never as a damaged code. External tooling that decodes share codes (test
harnesses, CI checks on published profiles) can pin the expected marker value
and treat a change as the compatibility signal, under the same contract.

Two more commitments keep that refusal honest. The exporter stamps the
*lowest* schema version the payload actually needs — a profile that uses
nothing a bump added still travels under the old number, so newer StreamNZB
versions keep producing codes older ones can import whenever the content
allows it. And importers keep reading every schema version they ever
understood, so a bump never orphans existing codes. Together they mean a
higher marker is never a formality: it appears exactly when the profile truly
depends on semantics the reader lacks.

### Remote profiles

A profile can be imported **from a URL** instead of a paste: host a file that
contains nothing but the share code at a raw `https://` address, and give
Import the URL. The profile is then *linked* — it remembers the address, shows
the host it came from and a **Linked** badge in the profile list, and gains a
**Refresh** button. [Format profiles](result-formatting.md#sharing-format-profiles)
share the same mechanism with their own `SNZBF1:` codes.

Refresh is always manual. It fetches the current code and either says the
profile is up to date or shows exactly what would change; nothing is applied
until you confirm the diff. Updates merge **by rule name**:

- a rule whose name exists upstream is the maintainer's — the update replaces
  it, local edits and all;
- a rule you added under your own name is yours — it survives every refresh,
  appended after the maintainer's rules;
- a rule the maintainer deleted is removed, even if you had edited it.

The diff is also the picker: every line in it — each added, updated and removed
rule, and the preset move — has a **checkbox**, ticked to start, and Apply
takes only what is still ticked. Unticking one means "leave this as it is": an
added rule is not taken, an updated rule keeps your version, and a refused
deletion stays, appended after the maintainer's rules where your own rules
live. Nothing about the refusal is stored — the snapshot kept is upstream in
full, since that is what tells a rule you added from one the maintainer deleted
— so a change you skip is offered again on the next refresh, and a deletion you
refuse quietly becomes a rule of your own. Rules can lean on each other: skip
a define that another rule matches by name and the reference is left dangling,
which the save refuses with the unknown-name error rather than landing broken.

The contract in one line: customize by adding your own rules; edits to
upstream rules last until the next refresh. The profile's local name is also
yours — a rename upstream is shown in the diff but never applied. Everything a
share code does not carry (NZB limits, attribute scoring) stays untouched.
**Unlink** keeps the profile as it is and removes the connection.

The trust model is deliberately narrow. Only `https://` URLs are accepted, the
address you typed is the only one ever consulted — nothing inside a fetched
file can point future refreshes elsewhere — and the fetch happens in your
browser with no credentials attached, so the server never requests the URL.
Whoever controls that URL can *propose* profile changes forever; the visible
diff is what keeps that honest, which is why there is no automatic sync.

**Hosting a profile:** Export → **Download** writes the code to a file; commit
it anywhere that serves raw files over https. The host has to allow
cross-origin reads (CORS) since the browser does the fetching —
`raw.githubusercontent.com` and gist raw URLs both do. GitHub's raw CDN caches
for a few minutes, so a push may take that long to show up in Refresh. One
code per file; surrounding whitespace and prose are tolerated.

## Define libraries

The bottom of the Filters page holds **define libraries**: named bundles of
[define rules](rules.md#define-libraries) — release-group tiers, community
classifications — kept once and referenced from every profile with
`matched("Name")`. The library maintains the data; what a tier is worth stays
each profile's own rule:

```
Library (maintained upstream):
Movies Remux T1 Groups: define if group matches "(?i)^(FraMeSToR|W4NK3R|...)$"

Your profile:
Trusted remux: score 500 if "remux" in traits and matched("Movies Remux T1 Groups")
```

Libraries can only carry defines — a score or reject rule in an imported
library is refused — and a profile rule under the same name shadows the
library's, so one entry can be overridden locally without forking the library.
Editing is text-only, one define per line in the rules editor's
[text form](rules.md#referring-to-another-rule); lines starting with `#` are
comments.

Sharing works like profiles: Export produces a `SNZBD1:` code, Import takes a
code or an https URL, and an imported-from-URL library is linked with the same
manual **Refresh** → confirmation diff → apply flow and the same
[trust model](#remote-profiles). Two differences:

- **A URL may serve plain rule text** instead of a share code — one define per
  line, `#` comments allowed — which is the natural output of a generator or a
  synced upstream list. The library is then named after the file.
- **Refresh replaces the defines wholesale.** A library is the maintainer's
  data; local edits to it last until the next refresh. A lasting override
  belongs in the profile, where your rule shadows the library's. The diff's
  per-change checkboxes work the same as a profile's — a define you untick is
  left as it is and offered again next time.

A save that would break a profile is refused either way: every profile
recompiles against the new library set when a library is saved, so a refresh
that renames or removes a define some profile still references fails
validation with the unknown-name error instead of silently landing. Deleting a
library warns with the list of profiles that reference it.

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
deduped (with **Variants kept**, the duplicates that were merged into a result
rather than discarded — see [Same-release variants](search-queries.md#same-release-variants)) →
known-bad removed → **Profile (name) input → kept**, with a separate
count for what rules rejected. Expanding **Rejected by profile** lists every
dropped release with its reason codes (`trash`, `resolution:480p`,
`language:missing_required`, `attribute:cam`, `rule: <name>`, …). Rule
rejections are counted separately because "the baseline blocked it" and "a rule
you wrote blocked it" send you to very different places.

A profile that filters everything out looks identical to an indexer that found
nothing unless you look here — so look here first when streams come back empty.
See also the **Search debug stream** toggle under Advanced, which prepends the
same summary as a result row in Stremio.
