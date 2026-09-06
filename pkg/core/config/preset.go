package config

import (
	"strings"

	"github.com/dreulavelle/jhin/rank"
)

// Presets are the whole of a filter profile's baseline. A profile picks one and
// writes rules; there is nothing else to configure.
//
// They differ only in the resolution ceiling, because that is the one decision
// that is genuinely about the person rather than about the release: a 4K
// display wants 4K, a 1080p display does not want to spend the bandwidth, and a
// slow line wants 720p. Everything else — which sources are worth what, which
// garbage to refuse, how to treat unparsable titles — has one right answer, so
// it is a default rather than a question.
const (
	PresetUHD   = "4k"
	Preset1080p = "1080p"
	Preset720p  = "720p"
)

// DefaultPreset is what a profile with no preset recorded is treated as, and
// what a fresh install starts on. It is 4K because that is what the shipped
// profile has always offered — a fresh install that suddenly capped at 1080p
// would be a silent downgrade for everyone who never opens this page.
const DefaultPreset = PresetUHD

// PresetDefinition describes one preset for the UI.
type PresetDefinition struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	// Resolutions are the tiers this preset offers, best first.
	Resolutions []string `json:"resolutions"`
}

// Presets are listed best-quality first, which is the order the picker shows.
var Presets = []PresetDefinition{
	{
		Key:         PresetUHD,
		Label:       "4K",
		Description: "Everything up to 2160p. Largest files, best picture.",
		Resolutions: []string{"2160p", "1440p", "1080p", "720p"},
	},
	{
		Key:         Preset1080p,
		Label:       "1080p",
		Description: "Up to 1080p. Smaller files, kinder to a shared connection.",
		Resolutions: []string{"1080p", "720p"},
	},
	{
		Key:         Preset720p,
		Label:       "720p",
		Description: "Up to 720p. Smallest files, for slow lines and small screens.",
		Resolutions: []string{"720p"},
	},
}

// NormalizePreset maps any recorded value onto a known preset.
func NormalizePreset(preset string) string {
	switch strings.ToLower(strings.TrimSpace(preset)) {
	case PresetUHD, "2160p", "uhd":
		return PresetUHD
	case Preset1080p:
		return Preset1080p
	case Preset720p:
		return Preset720p
	default:
		return DefaultPreset
	}
}

// presetCeilings is the set of tiers each preset enables, keyed by preset.
var presetCeilings = map[string][]rank.Resolution{
	PresetUHD:   {rank.Res2160p, rank.Res1440p, rank.Res1080p, rank.Res720p},
	Preset1080p: {rank.Res1080p, rank.Res720p},
	Preset720p:  {rank.Res720p},
}

// PresetSpec is the complete ranking profile behind a preset: the shared
// baseline with that preset's resolution ceiling applied.
//
// The baseline is deliberately permissive about everything except garbage. It
// rejects camcorder-class rips, source-less junk and adult releases, and
// expresses every other preference as score, so a poor release sorts last
// rather than disappearing. Anything a user wants beyond that is a rule.
func PresetSpec(preset string) rank.Profile {
	profile := rank.Default()

	// Unknown stays on so a release whose title could not be parsed is not
	// silently dropped; it simply sorts below everything that could.
	enabled := map[rank.Resolution]bool{rank.ResUnknown: true}
	for _, res := range presetCeilings[NormalizePreset(preset)] {
		enabled[res] = true
	}
	profile.Resolutions = map[rank.Resolution]bool{
		rank.Res2160p: enabled[rank.Res2160p],
		rank.Res1440p: enabled[rank.Res1440p],
		rank.Res1080p: enabled[rank.Res1080p],
		rank.Res720p:  enabled[rank.Res720p],
		rank.Res576p:  false,
		rank.Res480p:  false,
		rank.Res360p:  false,
		rank.Res240p:  false,
		// Streaming happens on the fly from archive segments, so the SD tiers
		// are more trouble than they are worth whatever the ceiling.
		rank.ResUnknown: true,
	}

	// No ResolutionOrder: the ceiling decides which tiers are offered, and
	// the score decides the order they come in. Pinning the order here made
	// resolution a bracket no amount of points could cross, so a rule that
	// preferred a language or an edition could only ever reorder releases
	// within one tier.

	profile.Options.RemoveAdult = true
	// Catches the garbage the per-source scores cannot: leaked copies,
	// pre-retail rips and deleted-scene reels carry no source of their own.
	profile.Options.RemoveTrash = true
	// Score orders results, it does not reject them: a release demoted by
	// several traits should sort last, not fall through a floor. Rejecting is
	// what rules are for, and a rule says why.
	profile.Options.MinRank = noScoreFloor
	// No preferred language. The baseline used to add 10000 for English, but
	// the bonus only ever landed on a release whose *title* names a language,
	// and most English releases never say so — which made it a coin flip
	// between an untagged English release and a tagged one rather than a
	// preference for English. A profile that wants a language ranked writes a
	// rule for it (`"en" in languages` — the codes are ISO 639-1), which says
	// so on the screen and can be scoped, scored and switched off.
	profile.Attributes = defaultAttributePolicies()

	return profile
}

// DefaultFilterProfile is the profile new installs start with.
func DefaultFilterProfile() FilterProfileConfig {
	return FilterProfileConfig{
		Name:   DefaultFilterProfileName,
		Preset: DefaultPreset,
	}
}

// HasLegacyFields reports whether the profile still carries the pre-jhin
// schema. Those profiles are synthesized into a spec on load and then migrated
// to a preset like any other, so this only matters for a profile built in
// memory that never went through a load.
func (fp *FilterProfileConfig) HasLegacyFields() bool {
	if fp == nil {
		return false
	}
	return len(fp.AllowedResolutions) > 0 || len(fp.BlockedResolutions) > 0 ||
		len(fp.AllowedQualities) > 0 || len(fp.BlockedQualities) > 0 ||
		len(fp.AllowedCodecs) > 0 || len(fp.BlockedCodecs) > 0 ||
		fp.RequireHDR != nil || len(fp.AllowedHDRs) > 0 || len(fp.BlockedHDRs) > 0 ||
		len(fp.RequiredKeywords) > 0 || len(fp.ExcludedKeywords) > 0 ||
		len(fp.AllowedLanguages) > 0 || len(fp.BlockedLanguages) > 0 ||
		len(fp.PreferredLanguages) > 0
}

// sizeTargetsGB is the size a release of each kind should ideally be, per
// preset, in decimal gigabytes. Scoring peaks at the target and tapers to
// nothing at twice it, so these are "what a good copy of this weighs" rather
// than a ceiling.
//
// This is the counterweight to source scoring. Without it nothing in the
// baseline knows what a release costs to play, and a 70 GB remux beats a 20 GB
// encode by a margin no other attribute can close — which is the right answer
// for a downloader and the wrong one for something assembling the file from
// usenet while it plays.
var sizeTargetsGB = map[string]map[string]float64{
	PresetUHD: {
		LimitKindDefault: 20, "movie": 20, "anime_movie": 20,
		"series": 6, "anime_show": 6,
	},
	Preset1080p: {
		LimitKindDefault: 8, "movie": 8, "anime_movie": 8,
		"series": 2.5, "anime_show": 2.5,
	},
	Preset720p: {
		LimitKindDefault: 4, "movie": 4, "anime_movie": 4,
		"series": 1.2, "anime_show": 1.2,
	},
}

// sizeWeight is what a well-sized release earns. It is deliberately of the same
// order as the source scores: enough that an efficient encode is a real
// alternative to a remux rather than an also-ran, not so much that size is the
// only thing that matters.
const sizeWeight = 3000

// PresetScoring is the NZB attribute scoring behind a preset, keyed by content
// kind exactly like a profile's own. It applies to every profile whose Scoring
// is nil; a profile that carries a map replaces it whole (see ranking.Compile).
func PresetScoring(preset string) map[string]*ScoringConfig {
	targets := sizeTargetsGB[NormalizePreset(preset)]
	out := make(map[string]*ScoringConfig, len(targets))
	for kind, target := range targets {
		out[kind] = &ScoringConfig{SizeTargetGB: target, SizeWeight: sizeWeight}
	}
	return out
}
