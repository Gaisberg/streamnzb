# Profile & template API

Rules and result formats are written against a vocabulary that grows with the
project: a field like `probed.subtitleLanguages` or a function like
`matchesExcept` arrives in a patch release, not a major one. A tool that
generates or validates profiles therefore cannot decide what is supported from
the version number alone — it has to ask.

This page documents the endpoints that answer. They are aimed at tooling
(template generators, third-party editors, CI checks); everything they report
is also visible in the Filters and Result format editors.

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

`tier` is the part worth reading carefully. A rule naming a field whose tier is
absent on a release is **skipped**, not judged against zero — see
[Rules](rules.md#confidence-what-a-rule-can-read-and-how-much-to-trust-it). Each tier's `description` completes
"needs …" in the reason a skipped rule reports, so a tool can explain a skip
without hardcoding the wording.

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
