// Package rules evaluates a filter profile's named conditions against a
// release.
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

	"streamnzb/pkg/release"
	"streamnzb/pkg/search/parser"
	"streamnzb/pkg/search/triage"
)

// Env is what a rule condition sees. Field names are the identifiers a rule
// author writes, so the expr tags are part of the user-facing language.
type Env struct {
	// ---- inferred, merged with measured where the probe knows better ----

	Resolution string `expr:"resolution"`
	Codec      string `expr:"codec"`
	BitDepth   int    `expr:"bitDepth"`
	// HDR lists the dynamic-range tags, in the parser's vocabulary: "DV" for
	// Dolby Vision and "HDR" for plain HDR10, so `"HDR10" in hdr` is never
	// what you want. Prefer dolbyVision and hdrFallback below, which mean the
	// same thing whichever tier answered.
	HDR []string `expr:"hdr"`
	// DolbyVision is true when the release carries a Dolby Vision layer.
	DolbyVision bool `expr:"dolbyVision"`
	// HDRFallback is true when a device that cannot decode Dolby Vision still
	// gets an HDR picture. It is false for SDR and for DV-only releases
	// alike, which is the distinction "block DV without a fallback" needs and
	// the one no single regex can express.
	HDRFallback bool `expr:"hdrFallback"`
	// Verified reports that the merged values above came from the file rather
	// than from its name.
	Verified bool `expr:"verified"`

	// ---- inferred only ----

	Quality    string   `expr:"quality"`
	Audio      []string `expr:"audio"`
	Channels   []string `expr:"channels"`
	Languages  []string `expr:"languages"`
	Group      string   `expr:"group"`
	Edition    string   `expr:"edition"`
	Container  string   `expr:"container"`
	Year       int      `expr:"year"`
	Proper     bool     `expr:"proper"`
	Repack     bool     `expr:"repack"`
	Remastered bool     `expr:"remastered"`
	Upscaled   bool     `expr:"upscaled"`
	ThreeD     bool     `expr:"threeD"`
	Dubbed     bool     `expr:"dubbed"`
	Subbed     bool     `expr:"subbed"`
	Hardcoded  bool     `expr:"hardcoded"`
	Complete   bool     `expr:"complete"`
	SeasonPack bool     `expr:"seasonPack"`
	// Traits is every attribute the parser detected, by the same keys the
	// ranking baseline scores: "remux", "webdl", "cam", "hevc", "10bit",
	// "dual_audio" and so on. It is what lets a rule reach anything the
	// baseline has an opinion about — `"cam" in traits` rejects camcorder
	// rips, `"remux" in traits` rewards remuxes — without a separate control
	// for each one.
	Traits []string `expr:"traits"`
	// Parsed is the release name's own account of the merged attributes, for
	// rules that must not accept a probe's word for it (or vice versa).
	Parsed ParsedEnv `expr:"parsed"`

	// ---- reported by the indexer ----

	ReleaseName string  `expr:"releaseName"`
	SizeGB      float64 `expr:"sizeGB"`
	// SizePerEpisodeGB is what a size condition should usually judge: the
	// whole release for films and single episodes, the per-episode share for
	// a multi-episode release, and -1 for a season pack whose episode count
	// the title does not reveal. Judging a ten-episode pack by its total
	// would reject it for being large when it is not.
	SizePerEpisodeGB float64 `expr:"sizePerEpisodeGB"`
	AgeDays          float64 `expr:"ageDays"`
	Grabs            int     `expr:"grabs"`
	Passworded       bool    `expr:"passworded"`
	Indexer          string  `expr:"indexer"`
	QuerySource      string  `expr:"querySource"`

	// ---- community ----

	Avail AvailEnv `expr:"avail"`

	// ---- measured ----

	Probed ProbedEnv `expr:"probed"`

	// HasIndexerData reports that a real NZB stands behind this release, so
	// its size, age and grab count mean something. It is false only in the
	// preview, where a release name is all there is. Not exposed to
	// conditions: a rule should say what it wants, and the engine decides
	// whether it can be answered.
	HasIndexerData bool `expr:"-"`

	// ---- request context ----

	Library bool   `expr:"library"`
	Kind    string `expr:"kind"`
	IsAnime bool   `expr:"isAnime"`
	Season  int    `expr:"season"`
	Episode int    `expr:"episode"`
	Title   string `expr:"title"`
}

// ParsedEnv is what the release name claims, untouched by anything measured.
type ParsedEnv struct {
	Resolution  string   `expr:"resolution"`
	Codec       string   `expr:"codec"`
	HDR         []string `expr:"hdr"`
	BitDepth    int      `expr:"bitDepth"`
	Title       string   `expr:"title"`
	DolbyVision bool     `expr:"dolbyVision"`
	HDRFallback bool     `expr:"hdrFallback"`
}

// AvailEnv is the community availability record.
type AvailEnv struct {
	// Status is "available", "unavailable" or "unknown".
	Status string `expr:"status"`
	// Known is false when nobody has reported the release either way.
	Known bool `expr:"known"`
	// OnMyBackbone reports the release healthy on a backbone this stream's
	// own providers use.
	OnMyBackbone bool `expr:"onMyBackbone"`
	// CheckedDaysAgo is how stale the record is, or -1 when it carries no
	// timestamp.
	CheckedDaysAgo int `expr:"checkedDaysAgo"`
	// Compression is the archive format the database recorded, e.g. "rar".
	Compression string `expr:"compression"`
}

// ProbedEnv is what ffprobe measured. Every field is zero for a release that
// has never been opened, which is why rules touching it fail open.
type ProbedEnv struct {
	VideoCodec string `expr:"videoCodec"`
	AudioCodec string `expr:"audioCodec"`
	Width      int    `expr:"width"`
	Height     int    `expr:"height"`
	Profile    string `expr:"profile"`
	BitDepth   int    `expr:"bitDepth"`
	// HDR is the base HDR format ("HDR10", "HDR10+", "HLG"), empty for SDR
	// and for Dolby Vision with no fallback layer.
	HDR string `expr:"hdr"`
	// DolbyVision is independent of HDR: a DV release with an empty HDR is
	// DV-only and shows as SDR on a device that cannot decode it.
	DolbyVision bool `expr:"dolbyVision"`
	// HasHDRFallback is true when a device without Dolby Vision still gets an
	// HDR picture.
	HasHDRFallback bool `expr:"hasHDRFallback"`
	// DynamicRange is the human-readable form: "DV + HDR10", "DV only",
	// "HDR10", or "" for SDR.
	DynamicRange string `expr:"dynamicRange"`
}

// Context is the per-request half of the environment, the same for every
// release in one result set.
type Context struct {
	Kind    string
	IsAnime bool
	Season  int
	Episode int
	Title   string
	// Episodic marks a request for episodic content, which is what decides
	// whether a size is judged whole or per episode.
	Episodic bool
}

// BuildEnv assembles the environment for one release. parsed may be nil when
// the title could not be parsed; every field then reports its zero value,
// which is the same thing an absent attribute has always meant here.
func BuildEnv(cand triage.Candidate, parsed *jhinparser.Result, ctx Context) Env {
	env := Env{
		Kind:    ctx.Kind,
		IsAnime: ctx.IsAnime,
		Season:  ctx.Season,
		Episode: ctx.Episode,
		Title:   ctx.Title,
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
		env.HasIndexerData = rel.Size > 0 || rel.Grabs > 0 || rel.PubDate != "" || rel.IsLibraryResult()
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
	applyMerged(&env, cand.Verdict.Probed)
	return env
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
