# Profile & template API

Rules and result formats are written against a vocabulary that grows with the
project: a field like `probed.subtitleLanguages` or a function like
`matchesExcept` arrives in a patch release, not a major one. A tool that
generates or validates profiles therefore cannot decide what is supported from
the version number alone — it has to ask.

This page documents the two endpoints that answer: one reporting what this
build understands, one evaluating a profile against releases you supply. They
are aimed at tooling (template generators, third-party editors, CI checks);
everything they report is also visible in the Filters and Result format
editors.

All endpoints are **admin-only** and take the same authentication as the rest
of the API — the session cookie, or `Authorization: Bearer <admin token>`.

## `GET /api/capabilities`

Reports what this build understands: the versions, the complete rule
vocabulary, and the complete result-format template surface.

```bash
curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/capabilities
```

```json
{
  "streamnzb": "6.1.0",
  "jhin": { "version": "0.7.1", "syntax_version": 1 },
  "rules": {
    "fields": [
      { "name": "resolution", "type": "string" },
      { "name": "seadex.best", "type": "bool", "tier": "seadex" },
      { "name": "probed.subtitleLanguages", "type": "list<string>", "tier": "tracks" },
      { "name": "finalScore", "type": "num", "prune_only": true }
    ],
    "tiers": [
      { "name": "seadex", "description": "a SeaDex lookup, which this request did not run" }
    ],
    "functions": [
      { "name": "min", "signature": "min(num, ...) -> num", "kind": "function", "description": "…" },
      { "name": "matchesExcept", "signature": "matchesExcept(string, string, string) -> bool", "kind": "function", "description": "…" },
      { "name": "count", "signature": "count(cond) -> num", "kind": "aggregate", "description": "…" }
    ],
    "actions": ["score", "reject", "limit", "prune", "define"],
    "scopes": ["all", "movie", "series", "anime_movie", "anime_show"]
  },
  "formatter": {
    "fields": [
      { "path": ".Subtitles", "type": "list<string>" },
      { "path": ".Probed.SubtitleLanguages", "type": "list<string>" },
      { "path": ".MatchedRules", "type": "list" },
      { "path": ".MatchedRules[].Name", "type": "string" }
    ],
    "helpers": [{ "name": "join", "args": 2 }, { "name": "size", "args": 1 }],
    "syntax": "Go text/template: {{.Field}}, {{if}}, {{range}}, {{with}} and | pipelines"
  }
}
```

### Versions

| Field | Meaning |
|---|---|
| `streamnzb` | the running build, `"dev"` for an untagged build |
| `jhin.version` | the rule engine's release version |
| `jhin.syntax_version` | the **rule language's** own version — the number to branch on |

`syntax_version` is the one worth checking. jhin bumps it when the grammar
changes, independently of its release version, so it is what tells a tool
whether a condition it wants to emit will parse at all. A tool that exports
profiles should record it alongside them: a condition using syntax this build
has never heard of fails as an unknown attribute, which sends the reader
looking in the wrong place.

### Rule fields

| Key | Meaning |
|---|---|
| `name` | what a condition writes, e.g. `probed.bitDepth` |
| `type` | `bool`, `num`, `string`, `list<string>` or `list<num>` |
| `tier` | the confidence group that decides when the field is answerable; absent for a field every release carries |
| `values` | the complete set the field can hold, when it is closed; absent means open |
| `prune_only` | the field only exists after scoring, so only a `prune` rule may read it |

`tier` is the part worth reading carefully. A rule whose **outcome turns on** a
field in an absent tier is skipped rather than judged against zero. That is
narrower than naming one: `and` and `or` settle the rule wherever the
answerable side is enough by itself, so `resolution == "1080p" or
seadex.best` holds for a 1080p release with no SeaDex lookup. See
[Rules](rules.md#only-when-it-would-have-changed-the-answer).

Each tier's `description` completes "needs …" in the reason a skipped rule
reports, so a tool can explain a skip without hardcoding the wording.

### Functions

`kind` distinguishes the three forms a name can take, because several names are
overloaded:

| `kind` | Form | Example |
|---|---|---|
| `function` | a plain call | `min(sizeGB, 8)` |
| `collection` | a condition per element of a list, `#` standing for the element | `any(languages, # == "ja")` |
| `aggregate` | a question about the whole result set | `exists(resolution == "2160p")` |
| `reference` | `matched()`, inlined at compile time rather than called | `matched("Good groups")` |

`count`, `any` and `none` appear twice — once as a collection form, once as an
aggregate — because which one you get depends on how many arguments you pass.

### Formatter fields

`path` is the path a template writes, leading dot included, so it is
copy-pasteable into `{{ }}`. Nested records are flattened
(`.Probed.SubtitleLanguages`), and a list of records reports both the list and
its element's fields under a `[]` segment (`.MatchedRules`,
`.MatchedRules[].Name`).

`helpers[].args` is how many arguments the helper takes, or `-1` when it is
variadic. See [Custom result formats](result-formatting.md) for what each one
does.

## `POST /api/ranking/explain`

Runs release names through a filter profile and reports what it did to each —
the same call the live pipeline makes, not an approximation. It is the
deterministic test runner: fixtures and a profile in, structured evaluation
out, with no indexer and no network.

```bash
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d @fixtures.json http://localhost:8080/api/ranking/explain
```

### Request

| Field | Meaning |
|---|---|
| `titles` | release names to judge, each against the request-level `sample` |
| `candidates` | releases carrying their own `sample`, for a set that must not be uniform |
| `profile` | a full profile definition to evaluate, unsaved — what the Filters preview posts |
| `profile_name` | a saved profile to evaluate instead; one of the two is required |
| `kind` | the content kind to judge as: `movie`, `series`, `anime_movie`, `anime_show`. Empty exercises only the rules that apply everywhere |
| `target_title` | the requested title, for title-similarity scoring |
| `original_language` | what to pretend the requested title was made in (ISO 639-1), for rules reading `originalLanguage` |
| `sample` | what to pretend about every release in `titles`, for the parts a name cannot carry |

`titles` and `candidates` may be combined and are judged as one set. At most
100 releases per call.

A bare name carries no NZB, no probe and no availability record, so a rule
whose outcome turns on those tiers is reported as **skipped** rather than
judged against zeros — the same fail-open behaviour it has in a real search,
made visible. A rule that another part of its condition already settles is not
skipped, and appears in `matched` or in nothing at all like any other.
`sample` is how you answer them:

```json
{
  "profile_name": "Default",
  "kind": "anime_show",
  "candidates": [
    {
      "title": "[GoodGroup] Show - 01 (1080p) [Dual Audio]",
      "sample": {
        "indexer_data": true, "size_gb": 4.2, "age_days": 30, "grabs": 120,
        "probed": { "height": 1080, "video_codec": "hevc", "audio_languages": ["ja", "en"], "subtitle_languages": ["en"] },
        "avail_status": "available"
      }
    },
    { "title": "[OtherGroup] Show - 01 (1080p)", "sample": { "indexer_data": true, "size_gb": 22 } }
  ],
  "sample": { "seadex": { "best_groups": ["GoodGroup"] } }
}
```

Two things about samples are worth knowing:

- **`indexer_data` vouches for the zeros.** Size, age and grab count are all
  absent on a bare name, so a rule reading `grabs == 0` is skipped rather than
  paid. Setting `indexer_data` says the zeros are real — a release nobody has
  grabbed yet has a known grab count of nought. It applies per fixture.
- **`seadex` belongs on the request-level `sample` only.** One lookup answers
  for the requested title and each release is judged by matching its group
  against that answer, so a per-candidate `seadex` is refused rather than
  silently ignored. Each fixture is still judged individually: name a group in
  `best_groups` and only releases parsed as that group come out best.

### Response

```json
{
  "profile": "Default",
  "results": [
    {
      "title": "[GoodGroup] Show - 01 (1080p) [Dual Audio]",
      "rank": 12450,
      "fetch": true,
      "resolution": "1080p",
      "contributions": [
        { "source": "resolution:1080p", "rank": 4000 },
        { "source": "rule:SeaDex best", "rank": 5000 }
      ],
      "matched": [{ "name": "SeaDex best", "score": 5000 }],
      "limited": [{ "name": "Cap per group", "group": "goodgroup" }],
      "skipped_rules": ["Needs a probe: needs a probed file, which this release is not"],
      "parsed": { "…": "the full jhin parse" }
    },
    {
      "title": "[OtherGroup] Show - 01 (1080p)",
      "rank": 0,
      "fetch": false,
      "rejections": ["rule: Oversized"]
    }
  ],
  "aggregates": [
    { "source": "exists(\"remux\" in traits)", "count": 0, "matched": [] }
  ]
}
```

| Field | Meaning |
|---|---|
| `rank` | the final score, every stage included |
| `fetch` | whether the release survived; `false` releases are still explained, which is the question the endpoint exists to answer |
| `contributions` | the score broken down per clause — the native attribute scores plus one `rule:<name>` entry per rule that paid. `detail` carries a rule's score expression when the points were computed rather than fixed |
| `matched` | the named rules that paid out, with the points each was worth |
| `rejections` | why the release was turned away; a rule's own rejection is prefixed `rule: ` |
| `limited` | the caps this release counts against and the bucket it counts in, whether or not it survived them |
| `skipped_rules` | rules that could not be judged, each with the reason — a rule reading `probed.*` against a bare name reports here rather than failing |
| `parsed` | the full parse of the release name |
| `aggregates` | the set-wide conditions (`exists`, `count`, `none`), what each counted and which releases it counted; they belong to no single release's breakdown. `known: false` means nothing in the set carried the tiers the condition reads, which skips every rule depending on it |

Rejected releases are returned after the surviving ones, so the order of
`results` is not the order of the request. Match on `title`.

### Using it as a CI check

The endpoint is deterministic: the same fixtures and profile give the same
scores, with no indexer involved. A check that a profile still ranks a known
set the way it should is a POST and an assertion on `rank` order:

```bash
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d @fixtures.json http://localhost:8080/api/ranking/explain \
  | jq -r '.results | sort_by(-.rank) | .[] | select(.fetch) | .title' \
  | diff - expected-order.txt
```

`skipped_rules` is worth asserting on too: a rule that quietly stops being
judgeable — because the fixture lost the sample that answered it — reads as a
rule that no longer fires.

## Notes for tool authors

- **Treat the field list as the source of truth, not this page.** The endpoint
  is generated from the same registries the live pipeline compiles rules
  against, so it cannot drift from what a rule is actually checked against.
  This page can.
- **Field names are permanent.** Renaming one would break every stored rule
  that reads it, so a name that appears here stays. Fields are added, not
  renamed.
- **Absence means unsupported.** If a field or function is missing from the
  report, this build will refuse a profile that names it at save time, which is
  better than a rule that silently never fires.
