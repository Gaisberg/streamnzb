# Filters & ranking

A filter profile decides which releases a stream offers, how they are scored, and what order they arrive in. Profiles are defined once on the **Filters** page (under Settings) and take effect only when a stream selects one — an unbound profile does nothing, and a stream with no profile returns every release unfiltered.

Parsing and scoring are powered by [jhin](https://github.com/dreulavelle/jhin): every release name is parsed into traits (source, codec, HDR, audio, languages, …), each trait contributes a score, and the profile decides which traits are allowed at all.

## The model: rejecting vs. scoring

Two separate mechanisms, and keeping them apart is the key to configuring profiles well:

- **Rejection** removes a release entirely: disabled resolutions, blocked traits (the allow/block switch on a trait), "Must match" / "Never match" rules, language requirements/exclusions, garbage and adult removal.
- **Scoring** only orders what survives: every detected trait adds its score, preferred patterns and languages each add the preference bonus, weighted preferences stack on top. Kept releases are returned best-score first. By default score never rejects anything — the minimum-score threshold ships effectively disabled so that a low score sorts a release last rather than hiding it.

So: to *never see* something, block it; to *see it later*, score it down.

## Binding a profile to a stream

On the **Streams** page, each stream's **General** tab has a **Filter/Sorting** dropdown: `None` (everything unfiltered), `AIOStreams` (raw results for [AIOStreams](aiostreams.md) to filter on its side), or one of your profiles.

Below it, **By content type** overrides the chosen profile for specific kinds: **Movies**, **Shows**, **Anime films**, **Anime shows**. Anything left on Default uses the main profile. Exactly one profile applies per request — the per-kind binding wins, profiles never combine. Anime detection uses Kitsu when the request comes from a Kitsu catalog, otherwise TMDB genres (animation not originally in English), which needs TMDB configured.

Renaming a profile updates every stream that uses it; deleting one clears it from those streams, which then fall back to unfiltered.

## The default profile

A fresh install ships a **Default Profile**: HD tiers (4K–720p) plus *Unknown* enabled, SD tiers off, garbage and adult removal on, English always allowed and preferred. *Unknown* resolution stays on deliberately so a release whose title couldn't be parsed is not silently dropped. Camcorder-class sources (CAM, TeleSync, TeleCine, Screener, R5, PDTV) are blocked explicitly, so they stay gone even if you later turn "Remove garbage titles" off.

Note: the default stream created on first install is set to AIOStreams mode, so the default profile is *not* applied until you select it on a stream.

## Profile editor

### Quality

- **Resolutions** — toggle the tiers this profile offers (4K down to 240p, plus *Unknown* for unparsable titles). A disabled tier rejects.
- **Remove garbage titles** — rejects camcorder, telesync, telecine and screener rips and source-less junk (leaked/pre-retail copies). Turn it off to decide those sources one by one on the Scoring tab.
- **Skip adult content**.
- **Thresholds** — **Minimum score** (default is far below any real score, i.e. no floor), **Preference bonus** (added once when a preferred pattern matches and once more when a preferred language matches, default +10000 — the same setting the Languages tab surfaces as its bonus slider), and **Title match strictness** (how closely a name must match a requested title; only applies where a target title is known, i.e. the Try it out bench — live results are already title-validated during search, before the profile runs).
- **NZB limits** — bounds on the NZB itself rather than the parsed title. **Block password-protected releases** (on by default) rejects releases the indexer flags as passworded. The grid below it bounds **size** (decimal GB, matching the sizes stream descriptions show), **age** (days since the release hit usenet — cap it at your provider's retention) and **grabs** (minimum download count, a cheap health signal). Empty fields are off. The **All content** row applies everywhere; the **Movies** / **Shows** / **Anime films** / **Anime shows** rows override it field by field, so one profile can cap movies at 30 GB and episodes at 5 GB. Bounds fail open: multi-episode releases are judged per episode, a season pack whose episode count can't be parsed is not size-checked at all, and a release that doesn't report a date or grab count is never rejected for it. Limit rejections show up in History and the debug stream like any other profile rejection (`size 42 GB above max 30 GB`, `password protected`, …).
- **NZB scoring** — points for the NZB itself, added to the points its title earns on the Scoring tab, in the same per-kind grid as the limits. Each attribute pairs a **target** with the **points** a perfect match is worth; a target with no points (or points with no target) does nothing. **Size** scores full points at the target and tapers to zero at nothing and at twice it, so a target of 8 GB prefers 8 GB, still likes 6 and 11, and ignores 20. **Age** scores full points for a release posted just now, counting down to zero over the fresh window. **Grabs** scale logarithmically to their target, because the step from 1 to 10 grabs reads like the step from 10 to 100. Points may be negative to invert a preference (a negative grabs weight prefers the obscure release). Scoring fails open the same way the limits do: multi-episode releases are judged per episode, unparseable season packs are not size-scored, and a release that reports no date or grab count is never docked for it.

  Limits decide what is eligible; scoring decides what sorts first among the eligible. To *reject* an out-of-range release, use the limits — scoring only moves it down the list.

### Scoring

Every trait jhin can detect, grouped (Sources, Codecs, HDR & bit depth, Audio, Channels, Release traits), each with a score slider and an allow/block switch. Scores add up to decide the order; blocking a trait rejects releases carrying it. Use **Changed only** to see what you've overridden; the reset icon returns a trait to its recommended value.

**Library hit score bonus** (default 500) is added to releases already in your library — proven-playable results outrank fresh indexer hits. Set −1 to disable.

### Rules

Regular expressions matched against the full release name (case-insensitive; wrap in `/slashes/` for case-sensitive). Patterns are compiled when the config is saved — a pattern that fails to compile rejects the save with the compile error, so a broken profile can never silently stop filtering its streams.

- **Must match** — every pattern must appear or the release is rejected. For either/or, use one pattern: `(IMAX|Extended)`.
- **Never match** — any match rejects.
- **Prefer** — adds the preference bonus when matched; never rejects.
- **Weighted preferences** — patterns with their own score, stacking; presets included for dual audio, multi audio, English dub, IMAX, open matte, and hardcoded subs.

### Languages

**Required**, **Excluded** and **Preferred** language lists, plus:

- **Always allow English** — keeps English releases even when the exclusion list would catch them.
- **Reject unknown languages** — drops releases where no language could be detected (off by default; most single-language releases don't tag one).

### Try it out

The built-in bench at the bottom of the editor: paste release names (one per line), optionally a title to match against, and **Run** — each release shows kept/rejected, its score, and an expandable breakdown of exactly which clauses contributed what. It uses your current unsaved edits, so it's the fastest way to tune a profile before saving.

## Sharing profiles

**Export** turns a profile into a compact `SNZBP1:` share code; **Import** adds a shared code as a new profile (never overwriting). Handy for Discord.

## Seeing what a profile did

The **History** page shows the full funnel per search: raw results → validated → deduped → known-bad removed → **Profile (name) input → kept**. Expanding **Rejected by profile** lists every dropped release with its reason codes (`trash`, `resolution:480p`, `exclude:<pattern>`, `language:missing_required`, `attribute:cam`, …). A profile that filters everything out looks identical to an indexer that found nothing unless you look here — so look here first when streams come back empty. See also the **Search debug stream** toggle under Advanced, which prepends the same summary as a result row in Stremio.
