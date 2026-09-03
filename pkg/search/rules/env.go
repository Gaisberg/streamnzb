// Package rules evaluates a filter profile's named conditions against a
// release.
//
// The engine — grammar, compiler, tier tracking, result-set aggregates and
// matched() inlining — is jhin's (github.com/dreulavelle/jhin/rules). This
// package supplies what jhin cannot know: the registry declaring StreamNZB's
// own attributes, and the Facts answering them per release.
//
// The attribute namespace is organised by how much a value can be trusted
// rather than by which subsystem produced it:
//
//   - inferred  — read out of the release name. Every release has it, it is
//     never stale, and it is wrong often enough that a careful rule says so.
//   - reported  — claimed by the indexer (size, age, grabs, password flag).
//   - community — the availability database, per backbone and possibly months
//     old. Reached through avail.*.
//   - measured  — what ffprobe found in the file itself. Ground truth, but
//     only library releases have ever been opened. Reached through probed.*.
//
// Bare names give the best value available: resolution, codec, hdr and
// bitDepth prefer what was measured and fall back to what the name said, with
// verified reporting which one answered. parsed.* always gives the name's
// version, so a rule can insist on one or the other.
//
// Rules that read community or measured attributes fail open. A release that
// was never probed and never checked cannot satisfy a reject rule that depends
// on data it was never going to have — otherwise turning on a single probe
// rule would empty every result list of everything except library hits.
package rules

import (
	"strings"
	"time"

	jhinparser "github.com/dreulavelle/jhin/parser"
	"github.com/dreulavelle/jhin/rank"
	jhinrules "github.com/dreulavelle/jhin/rules"

	"streamnzb/pkg/release"
	"streamnzb/pkg/search/parser"
	"streamnzb/pkg/search/triage"
)

// Env is what a rule condition sees: one release's answers to the registry's
// attribute names. It implements jhin's Facts — Lookup maps the user-facing
// identifiers onto these fields, and anything it does not own falls through
// to jhin's own parse-result facts, so every core attribute jhin can read off
// a name is available to rules without a field here.
type Env struct {
	// ---- inferred, merged with measured where the probe knows better ----

	Resolution string
	Codec      string
	BitDepth   int
	// HDR lists the dynamic-range tags, in the parser's vocabulary: "DV" for
	// Dolby Vision and "HDR" for plain HDR10, so `"HDR10" in hdr` is never
	// what you want. Prefer dolbyVision and hdrFallback below, which mean the
	// same thing whichever tier answered.
	HDR []string
	// DolbyVision is true when the release carries a Dolby Vision layer.
	DolbyVision bool
	// HDRFallback is true when a device that cannot decode Dolby Vision still
	// gets an HDR picture. It is false for SDR and for DV-only releases
	// alike, which is the distinction "block DV without a fallback" needs and
	// the one no single regex can express.
	HDRFallback bool
	// Verified reports that the merged values above came from the file rather
	// than from its name.
	Verified bool

	// ---- inferred only ----

	Quality    string
	Audio      []string
	Channels   []string
	Languages  []string
	Group      string
	Edition    string
	Container  string
	Year       int
	Proper     bool
	Repack     bool
	Remastered bool
	Upscaled   bool
	ThreeD     bool
	Dubbed     bool
	Subbed     bool
	Hardcoded  bool
	Complete   bool
	SeasonPack bool
	// Traits is every attribute the parser detected, by the same keys the
	// ranking baseline scores: "remux", "webdl", "cam", "hevc", "10bit",
	// "dual_audio" and so on. It is what lets a rule reach anything the
	// baseline has an opinion about — `"cam" in traits` rejects camcorder
	// rips, `"remux" in traits` rewards remuxes — without a separate control
	// for each one.
	Traits []string
	// Parsed is the release name's own account of the merged attributes, for
	// rules that must not accept a probe's word for it (or vice versa).
	Parsed ParsedEnv

	// ---- reported by the indexer ----

	ReleaseName string
	SizeGB      float64
	// SizePerEpisodeGB is what a size condition should usually judge: the
	// whole release for films and single episodes, the per-episode share for
	// a multi-episode release, and -1 for a season pack whose episode count
	// the title does not reveal. Judging a ten-episode pack by its total
	// would reject it for being large when it is not.
	SizePerEpisodeGB float64
	AgeDays          float64
	Grabs            int
	Passworded       bool
	Indexer          string
	QuerySource      string

	// ---- community ----

	Avail AvailEnv

	// Seadex is SeaDex's (releases.moe) per-title recommendation, resolved for
	// the requested anime and judged against this release's group. Empty for
	// anything that is not anime and whenever the lookup could not run, which
	// is why rules touching it fail open.
	Seadex SeadexEnv

	// ---- measured ----

	Probed ProbedEnv

	// HasIndexerData reports that a real NZB stands behind this release, so
	// its size, age and grab count mean something. It is false only in the
	// preview, where a release name is all there is. Not exposed to
	// conditions: a rule should say what it wants, and the engine decides
	// whether it can be answered.
	HasIndexerData bool

	// ---- request context ----

	// Library reports that this exact release is already in the library. It
	// comes from StreamNZB's own store rather than the indexer, so unlike
	// the reported fields above it is answerable for every release — false
	// for a bare name — and carries no tier.
	Library bool
	Kind    string
	IsAnime bool
	Season  int
	Episode int
	Title   string
	// OriginalLanguage is the language the requested title was made in, as
	// an ISO 639-1 code from the metadata provider ("ja", "ko", "fr"), empty
	// when the provider did not say. It is what lets "prefer the original
	// audio" be one rule for every language rather than one per language.
	OriginalLanguage string

	// ---- post-scoring ----

	// FinalScore and FinalRank are the scoring pass's finished verdict: the
	// accumulated score and the release's 1-based position among the survivors
	// sorted by it. Only the prune pass sets them, and only its registry
	// declares them, so a scoring rule can never read a value that does not
	// exist yet.
	FinalScore int
	FinalRank  int

	// current is the release whose prune rule is being evaluated, which a
	// result-set question reaches through current.*. It is bound only while
	// that release's aggregates are being computed, so inside count(...) a
	// bare finalScore is the release being counted and current.finalScore the
	// one being judged. Nil means the environment answers for itself, which
	// is what current.* means everywhere outside a result-set question.
	current *Env
	// core answers every jhin attribute this struct has no field for, from
	// the parse result itself. Nil when BuildEnv had no parse, which answers
	// those attributes with their zeros — what an unparseable name has always
	// meant.
	core *jhinrules.ResultFacts
	// aggs is the request's computed result-set state, handed over by
	// AggregateState.Inject. Nil for a set without aggregates.
	aggs *jhinrules.AggregateState
}

// Lookup answers one declared attribute. The names are the identifiers a rule
// author writes, so this switch is part of the user-facing language and has to
// track the registry in rules.go.
func (e *Env) Lookup(path string) (jhinrules.Value, bool) {
	switch path {
	case "resolution":
		return jhinrules.StrOf(e.Resolution), true
	case "codec":
		return jhinrules.StrOf(e.Codec), true
	case "bitDepth":
		return jhinrules.NumOf(float64(e.BitDepth)), true
	case "hdr":
		return jhinrules.StrListOf(e.HDR), true
	case "dolbyVision":
		return jhinrules.BoolOf(e.DolbyVision), true
	case "hdrFallback":
		return jhinrules.BoolOf(e.HDRFallback), true
	case "verified":
		return jhinrules.BoolOf(e.Verified), true

	case "quality":
		return jhinrules.StrOf(e.Quality), true
	case "audio":
		return jhinrules.StrListOf(e.Audio), true
	case "channels":
		return jhinrules.StrListOf(e.Channels), true
	case "languages":
		return jhinrules.StrListOf(e.Languages), true
	case "group":
		return jhinrules.StrOf(e.Group), true
	case "edition":
		return jhinrules.StrOf(e.Edition), true
	case "container":
		return jhinrules.StrOf(e.Container), true
	case "year":
		return jhinrules.NumOf(float64(e.Year)), true
	case "proper":
		return jhinrules.BoolOf(e.Proper), true
	case "repack":
		return jhinrules.BoolOf(e.Repack), true
	case "remastered":
		return jhinrules.BoolOf(e.Remastered), true
	case "upscaled":
		return jhinrules.BoolOf(e.Upscaled), true
	case "threeD":
		return jhinrules.BoolOf(e.ThreeD), true
	case "dubbed":
		return jhinrules.BoolOf(e.Dubbed), true
	case "subbed":
		return jhinrules.BoolOf(e.Subbed), true
	case "hardcoded":
		return jhinrules.BoolOf(e.Hardcoded), true
	case "complete":
		return jhinrules.BoolOf(e.Complete), true
	case "seasonPack":
		return jhinrules.BoolOf(e.SeasonPack), true
	case "traits":
		return jhinrules.StrListOf(e.Traits), true

	case "parsed.resolution":
		return jhinrules.StrOf(e.Parsed.Resolution), true
	case "parsed.codec":
		return jhinrules.StrOf(e.Parsed.Codec), true
	case "parsed.hdr":
		return jhinrules.StrListOf(e.Parsed.HDR), true
	case "parsed.bitDepth":
		return jhinrules.NumOf(float64(e.Parsed.BitDepth)), true
	case "parsed.title":
		return jhinrules.StrOf(e.Parsed.Title), true
	case "parsed.dolbyVision":
		return jhinrules.BoolOf(e.Parsed.DolbyVision), true
	case "parsed.hdrFallback":
		return jhinrules.BoolOf(e.Parsed.HDRFallback), true

	case "releaseName":
		return jhinrules.StrOf(e.ReleaseName), true
	case "sizeGB":
		return jhinrules.NumOf(e.SizeGB), true
	case "sizePerEpisodeGB":
		return jhinrules.NumOf(e.SizePerEpisodeGB), true
	case "ageDays":
		return jhinrules.NumOf(e.AgeDays), true
	case "grabs":
		return jhinrules.NumOf(float64(e.Grabs)), true
	case "passworded":
		return jhinrules.BoolOf(e.Passworded), true
	case "indexer":
		return jhinrules.StrOf(e.Indexer), true
	case "querySource":
		return jhinrules.StrOf(e.QuerySource), true
	case "library":
		return jhinrules.BoolOf(e.Library), true

	case "avail.status":
		return jhinrules.StrOf(e.Avail.Status), true
	case "avail.known":
		return jhinrules.BoolOf(e.Avail.Known), true
	case "avail.onMyBackbone":
		return jhinrules.BoolOf(e.Avail.OnMyBackbone), true
	case "avail.checkedDaysAgo":
		return jhinrules.NumOf(float64(e.Avail.CheckedDaysAgo)), true
	case "avail.compression":
		return jhinrules.StrOf(e.Avail.Compression), true

	case "seadex.known":
		return jhinrules.BoolOf(e.Seadex.Known), true
	case "seadex.best":
		return jhinrules.BoolOf(e.Seadex.Best), true
	case "seadex.alternative":
		return jhinrules.BoolOf(e.Seadex.Alternative), true
	case "seadex.dualAudio":
		return jhinrules.BoolOf(e.Seadex.DualAudio), true

	case "probed.videoCodec":
		return jhinrules.StrOf(e.Probed.VideoCodec), true
	case "probed.audioCodec":
		return jhinrules.StrOf(e.Probed.AudioCodec), true
	case "probed.width":
		return jhinrules.NumOf(float64(e.Probed.Width)), true
	case "probed.height":
		return jhinrules.NumOf(float64(e.Probed.Height)), true
	case "probed.profile":
		return jhinrules.StrOf(e.Probed.Profile), true
	case "probed.bitDepth":
		return jhinrules.NumOf(float64(e.Probed.BitDepth)), true
	case "probed.hdr":
		return jhinrules.StrOf(e.Probed.HDR), true
	case "probed.dolbyVision":
		return jhinrules.BoolOf(e.Probed.DolbyVision), true
	case "probed.hasHDRFallback":
		return jhinrules.BoolOf(e.Probed.HasHDRFallback), true
	case "probed.dynamicRange":
		return jhinrules.StrOf(e.Probed.DynamicRange), true
	case "probed.audioLanguages":
		return jhinrules.StrListOf(e.Probed.AudioLanguages), true
	case "probed.subtitleLanguages":
		return jhinrules.StrListOf(e.Probed.SubtitleLanguages), true
	case "probed.audioStreams":
		return jhinrules.NumOf(float64(e.Probed.AudioStreams)), true
	case "probed.subtitleStreams":
		return jhinrules.NumOf(float64(e.Probed.SubtitleStreams)), true

	case "kind":
		return jhinrules.StrOf(e.Kind), true
	case "isAnime":
		return jhinrules.BoolOf(e.IsAnime), true
	case "season":
		return jhinrules.NumOf(float64(e.Season)), true
	case "episode":
		return jhinrules.NumOf(float64(e.Episode)), true
	// title is the requested title, not the release name's — that one is
	// parsed.title. It shadows jhin's core field on purpose.
	case "title":
		return jhinrules.StrOf(e.Title), true
	case "originalLanguage":
		return jhinrules.StrOf(e.OriginalLanguage), true

	case "finalScore":
		return jhinrules.NumOf(float64(e.FinalScore)), true
	case "finalRank":
		return jhinrules.NumOf(float64(e.FinalRank)), true
	case "current.finalScore":
		return jhinrules.NumOf(float64(e.judged().FinalScore)), true
	case "current.finalRank":
		return jhinrules.NumOf(float64(e.judged().FinalRank)), true
	}
	if e.core != nil {
		return e.core.Lookup(path)
	}
	return jhinrules.Value{}, false
}

// judged is the release a current.* reference names: the one whose prune rule
// is being evaluated, or this one when no other is bound. The fallback is not
// a convenience — outside a result-set question the release being judged is
// this release, so current.finalScore and finalScore are the same number.
func (e *Env) judged() *Env {
	if e.current != nil {
		return e.current
	}
	return e
}

// TierPresent reports whether this release carries anything in a tier.
// Returning false skips every rule that reads it — the fail-open contract.
func (e *Env) TierPresent(tier string) bool {
	switch tier {
	case "":
		return true
	case tierMeasured:
		return e.Verified
	case tierTracks:
		return e.Verified && e.Probed.TracksProbed
	case tierAvail:
		return e.Avail.Known
	case tierSeadex:
		return e.Seadex.Checked
	case tierIndexer:
		return e.HasIndexerData
	}
	return false
}

// ParsedEnv is what the release name claims, untouched by anything measured.
type ParsedEnv struct {
	Resolution  string
	Codec       string
	HDR         []string
	BitDepth    int
	Title       string
	DolbyVision bool
	HDRFallback bool
}

// AvailEnv is the community availability record.
type AvailEnv struct {
	// Status is "available", "unavailable" or "unknown".
	Status string
	// Known is false when nobody has reported the release either way.
	Known bool
	// OnMyBackbone reports the release healthy on a backbone this stream's
	// own providers use.
	OnMyBackbone bool
	// CheckedDaysAgo is how stale the record is, or -1 when it carries no
	// timestamp.
	CheckedDaysAgo int
	// Compression is the archive format the database recorded, e.g. "rar".
	Compression string
}

// SeadexEnv is the SeaDex recommendation for the requested anime, seen from
// one release. best and alternative are per-title judgments: the same group
// can be best for one anime and unlisted for the next.
type SeadexEnv struct {
	// Known is false when SeaDex has no entry for the requested title. That is
	// an answer, not missing data — an uncataloged anime evaluates normally.
	Known bool
	// Best is true when this release's group produced a release SeaDex marks
	// best for this title.
	Best bool
	// Alternative is true when the group is recommended for this title without
	// the best mark.
	Alternative bool
	// DualAudio is true when the group has a recommended release SeaDex marks
	// dual audio for this title. Per title, like Best: the same group can
	// ship dual audio for one anime and sub-only for the next.
	DualAudio bool
	// Checked reports that the lookup ran at all. When it is false — not an
	// anime request, no AniList mapping, or SeaDex unreachable — rules reading
	// seadex.* are skipped rather than judged against zero values. Not exposed
	// to conditions, same contract as HasIndexerData.
	Checked bool
}

// ProbedEnv is what ffprobe measured. Every field is zero for a release that
// has never been opened, which is why rules touching it fail open.
type ProbedEnv struct {
	VideoCodec string
	AudioCodec string
	Width      int
	Height     int
	Profile    string
	BitDepth   int
	// HDR is the base HDR format ("HDR10", "HDR10+", "HLG"), empty for SDR
	// and for Dolby Vision with no fallback layer.
	HDR string
	// DolbyVision is independent of HDR: a DV release with an empty HDR is
	// DV-only and shows as SDR on a device that cannot decode it.
	DolbyVision bool
	// HasHDRFallback is true when a device without Dolby Vision still gets an
	// HDR picture.
	HasHDRFallback bool
	// DynamicRange is the human-readable form: "DV + HDR10", "DV only",
	// "HDR10", or "" for SDR.
	DynamicRange string
	// TracksProbed reports the four track fields were captured; a library
	// item probed before they existed leaves it false and the tracks tier
	// absent, so rules reading them are skipped rather than judged against
	// empty lists.
	TracksProbed bool
	// AudioLanguages and SubtitleLanguages are the tagged track languages
	// (ISO 639-1, stream order). AudioStreams and SubtitleStreams count every
	// track, tagged or not. The count is not a language claim — a commentary
	// or a stereo downmix is a second stream in one language — so dual audio
	// is len(probed.audioLanguages) >= 2, and the counts are for what a count
	// says on its own.
	AudioLanguages    []string
	SubtitleLanguages []string
	AudioStreams      int
	SubtitleStreams   int
}

// Context is the per-request half of the environment, the same for every
// release in one result set.
type Context struct {
	Kind    string
	IsAnime bool
	Season  int
	Episode int
	Title   string
	// OriginalLanguage is the requested title's original language (ISO
	// 639-1), empty when metadata did not carry one.
	OriginalLanguage string
	// Episodic marks a request for episodic content, which is what decides
	// whether a size is judged whole or per episode.
	Episodic bool
	// IndexerDataKnown vouches for the release's size, age and grab count even
	// when they are zero. A live search never needs it — an indexer result
	// always has a size — but the preview builds its releases itself, and a
	// simulated release with nothing grabbed yet is still a release whose grab
	// count is known to be nought.
	IndexerDataKnown bool
	// Seadex is the resolved SeaDex recommendation for the requested title,
	// nil when no lookup ran.
	Seadex *SeadexContext
}

// SeadexContext is the per-request SeaDex answer: which release groups the
// entry recommends, keyed by group name lowercased (the caller normalizes).
type SeadexContext struct {
	// Known is false when the lookup succeeded but SeaDex has no entry for
	// the title.
	Known bool
	// Best holds the groups with a release marked best for this title, Alt
	// the recommended groups without a best mark, DualAudio the groups with
	// a recommended release marked dual audio. A group may be in DualAudio
	// and either of the other two.
	Best      map[string]bool
	Alt       map[string]bool
	DualAudio map[string]bool
}

// BuildEnv assembles the environment for one release. parsed may be nil when
// the title could not be parsed; every field then reports its zero value,
// which is the same thing an absent attribute has always meant here.
func BuildEnv(cand triage.Candidate, parsed *jhinparser.Result, ctx Context) Env {
	env := Env{
		Kind:             ctx.Kind,
		IsAnime:          ctx.IsAnime,
		Season:           ctx.Season,
		Episode:          ctx.Episode,
		Title:            ctx.Title,
		OriginalLanguage: ctx.OriginalLanguage,
	}

	if parsed != nil {
		dv, fallback := dynamicRangeFromTags(parsed.HDR)
		env.Parsed = ParsedEnv{
			Resolution:  parsed.Resolution,
			Codec:       parsed.Codec,
			HDR:         parsed.HDR,
			BitDepth:    atoiSafe(parsed.BitDepth),
			Title:       parsed.Title,
			DolbyVision: dv,
			HDRFallback: fallback,
		}
		env.Quality = parsed.Quality
		env.Audio = parsed.Audio
		env.Channels = parsed.Channels
		env.Languages = parsed.Languages
		env.Group = parsed.Group
		env.Edition = parsed.Edition
		env.Container = parsed.Container
		env.Year = atoiSafe(parsed.Year)
		env.Proper = parsed.Proper
		env.Repack = parsed.Repack
		env.Remastered = parsed.Remastered
		env.Upscaled = parsed.Upscaled
		env.ThreeD = parsed.ThreeD
		env.Dubbed = parsed.Dubbed
		env.Subbed = parsed.Subbed
		env.Hardcoded = parsed.Hardcoded
		env.Complete = parsed.Complete
		env.SeasonPack = len(parsed.Seasons) > 0 && len(parsed.Episodes) == 0
		for _, attr := range rank.Attributes(parsed) {
			env.Traits = append(env.Traits, string(attr))
		}
	}

	if rel := cand.Release; rel != nil {
		env.ReleaseName = rel.Title
		// A release the indexer actually returned has at least a size. A
		// preview sample is a bare title and has none of the three.
		env.HasIndexerData = ctx.IndexerDataKnown ||
			rel.Size > 0 || rel.Grabs > 0 || rel.PubDate != "" || rel.IsLibraryResult()
		env.SizeGB = float64(rel.Size) / 1e9
		if per, ok := parser.EffectiveEpisodeSize(rel.Size, ctx.Episodic, parsed); ok {
			env.SizePerEpisodeGB = float64(per) / 1e9
		} else {
			// Unknown is not zero: a pack nobody can count must not satisfy a
			// "smaller than" rule by default.
			env.SizePerEpisodeGB = -1
		}
		env.Grabs = rel.Grabs
		env.Passworded = rel.Password
		env.Indexer = rel.Indexer
		env.QuerySource = rel.QuerySource
		env.Library = rel.IsLibraryResult()
		if at, ok := rel.PublishedAt(); ok {
			env.AgeDays = time.Since(at).Hours() / 24
		} else {
			// No date is not age zero: a brand-new release and one whose
			// indexer never reported a date would otherwise be the same
			// value, and the second must not satisfy a freshness rule.
			env.AgeDays = -1
		}
	}

	env.Avail = availEnv(cand.Verdict.Avail)
	env.Probed = probedEnv(cand.Verdict.Probed)
	env.Seadex = ctx.Seadex.For(env.Group)
	applyMerged(&env, cand.Verdict.Probed)
	env.core = jhinrules.FromResult(env.ReleaseName, parsed, env.Traits)
	return env
}

// For judges one release's group against the request's SeaDex answer. A nil
// context means no lookup ran, which leaves Checked false so seadex.* rules
// are skipped rather than evaluated against zeros.
func (c *SeadexContext) For(group string) SeadexEnv {
	if c == nil {
		return SeadexEnv{}
	}
	g := strings.ToLower(strings.TrimSpace(group))
	return SeadexEnv{
		Checked:     true,
		Known:       c.Known,
		Best:        g != "" && c.Best[g],
		Alternative: g != "" && c.Alt[g],
		DualAudio:   g != "" && c.DualAudio[g],
	}
}

// applyMerged fills the bare attribute names: what the file measured when it
// was opened, what its name claimed otherwise.
func applyMerged(env *Env, caps *release.MediaCaps) {
	env.Resolution = env.Parsed.Resolution
	env.Codec = env.Parsed.Codec
	env.HDR = env.Parsed.HDR
	env.BitDepth = env.Parsed.BitDepth
	env.DolbyVision = env.Parsed.DolbyVision
	env.HDRFallback = env.Parsed.HDRFallback
	if caps == nil {
		return
	}
	env.Verified = true
	if res := resolutionForHeight(caps.Height); res != "" {
		env.Resolution = res
	}
	if caps.VideoCodec != "" {
		env.Codec = caps.VideoCodec
	}
	if caps.BitDepth > 0 {
		env.BitDepth = caps.BitDepth
	}
	if hdr := measuredHDRList(caps); hdr != nil {
		env.HDR = hdr
		env.DolbyVision = caps.DolbyVision
		env.HDRFallback = caps.HasHDRFallback()
	}
}

// measuredHDRList renders the probe's HDR reading in the parsed list's
// vocabulary, so `"DV" in hdr` means one thing whichever tier answered. The
// parser reports plain HDR10 as "HDR", and a list that spelled it two ways
// depending on whether the file had been opened would be a trap.
//
// It returns nil for a probe that found no HDR at all, leaving the parsed list
// in place rather than asserting SDR — bit depth and transfer are not always
// enough to rule HDR out.
func measuredHDRList(caps *release.MediaCaps) []string {
	if caps == nil {
		return nil
	}
	var out []string
	if caps.DolbyVision {
		out = append(out, "DV")
	}
	switch caps.HDR {
	case "":
	case "HDR10":
		out = append(out, "HDR")
	default:
		out = append(out, caps.HDR)
	}
	return out
}

// dynamicRangeFromTags reads a parsed HDR tag list. Anything that is not the
// Dolby Vision marker is a base layer a non-DV device can fall back to.
func dynamicRangeFromTags(tags []string) (dolbyVision, fallback bool) {
	for _, tag := range tags {
		if strings.EqualFold(tag, "DV") || strings.EqualFold(tag, "DOVI") {
			dolbyVision = true
			continue
		}
		if strings.TrimSpace(tag) != "" {
			fallback = true
		}
	}
	return dolbyVision, fallback
}

// resolutionForHeight buckets a measured pixel height into the resolution
// labels the rest of the pipeline uses. Heights land on the nearest tier at or
// below them, so a 1920x1036 scope-ratio file is 1080p rather than 720p.
func resolutionForHeight(height int) string {
	switch {
	case height <= 0:
		return ""
	case height >= 2000:
		return "2160p"
	case height >= 1400:
		return "1440p"
	case height >= 900:
		return "1080p"
	case height >= 680:
		return "720p"
	case height >= 560:
		return "576p"
	case height >= 460:
		return "480p"
	case height >= 340:
		return "360p"
	default:
		return "240p"
	}
}

func availEnv(state triage.AvailState) AvailEnv {
	status := string(state.Status)
	if status == "" {
		status = string(triage.AvailUnknown)
	}
	return AvailEnv{
		Status:         status,
		Known:          state.Known(),
		OnMyBackbone:   state.OnMyBackbone,
		CheckedDaysAgo: state.CheckedDaysAgo(),
		Compression:    state.Compression,
	}
}

func probedEnv(caps *release.MediaCaps) ProbedEnv {
	if caps == nil {
		return ProbedEnv{}
	}
	return ProbedEnv{
		VideoCodec:     caps.VideoCodec,
		AudioCodec:     caps.AudioCodec,
		Width:          caps.Width,
		Height:         caps.Height,
		Profile:        caps.Profile,
		BitDepth:       caps.BitDepth,
		HDR:            caps.HDR,
		DolbyVision:    caps.DolbyVision,
		HasHDRFallback: caps.HasHDRFallback(),
		DynamicRange:   caps.DynamicRange(),

		TracksProbed:      caps.TracksProbed,
		AudioLanguages:    caps.AudioLanguages,
		SubtitleLanguages: caps.SubtitleLanguages,
		AudioStreams:      caps.AudioStreams,
		SubtitleStreams:   caps.SubtitleStreams,
	}
}

func atoiSafe(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}
