package config

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/dreulavelle/jhin/rank"
)

// ProfileSourceConfig links a profile — filter or format — to the remote
// share code it was imported from, so the UI can offer a manual Refresh. The
// frontend owns the whole flow — fetching, merging, the confirmation diff —
// and the server never requests the URL; it only keeps the record and refuses
// one it could not have written.
type ProfileSourceConfig struct {
	// URL serves the profile's current share code. It is the trust anchor:
	// the address the user typed, and the only one a refresh ever consults.
	URL string `json:"url"`
	// Code is the upstream share code last applied, verbatim. It is both the
	// cheap up-to-date check (string compare against a fresh fetch) and the
	// merge baseline that tells a user-added rule from an upstream-deleted one.
	Code string `json:"code,omitempty"`
	// CheckedAt and AppliedAt are RFC3339 timestamps, display-only.
	CheckedAt string `json:"checked_at,omitempty"`
	AppliedAt string `json:"applied_at,omitempty"`
}

// maxSourceCodeBytes bounds the stored snapshot. A real code is a few
// kilobytes; the cap only exists so a hostile save cannot bloat the config.
const maxSourceCodeBytes = 256 * 1024

// Validate refuses a source record the importer could not have produced.
// Nil is valid: most profiles are not linked to anything.
func (ps *ProfileSourceConfig) Validate() error {
	if ps == nil {
		return nil
	}
	u, err := url.Parse(strings.TrimSpace(ps.URL))
	if err != nil {
		return fmt.Errorf("invalid source URL: %w", err)
	}
	// https only: the URL is a standing "this host can propose config
	// changes" grant, and a plain-http one could be rewritten on the path.
	if strings.ToLower(u.Scheme) != "https" || u.Host == "" {
		return fmt.Errorf("source URL must be https:// with a host")
	}
	if len(ps.URL) > 2048 {
		return fmt.Errorf("source URL is too long")
	}
	if len(ps.Code) > maxSourceCodeBytes {
		return fmt.Errorf("source share code is too large")
	}
	if ps.Code != "" && !strings.HasPrefix(strings.ToUpper(ps.Code), "SNZBP1:") {
		return fmt.Errorf("source share code must start with SNZBP1:")
	}
	return nil
}

// DefaultFilterProfileName is the profile seeded into a fresh config.
const DefaultFilterProfileName = "Default Profile"

// LimitKindDefault is the limits entry every content kind inherits from; the
// four ranking kinds ("movie", "series", "anime_movie", "anime_show") override
// it field by field.
const LimitKindDefault = "default"

// LimitKinds are the per-kind limit entries a profile may carry, the default
// first. The kind keys match the ranking package's content kinds.
var LimitKinds = []string{LimitKindDefault, "movie", "series", "anime_movie", "anime_show"}

// LimitsConfig bounds releases by NZB attributes rather than parsed quality.
// A zero value leaves that bound unenforced.
type LimitsConfig struct {
	// MinSizeGB and MaxSizeGB bound the release size in decimal gigabytes,
	// matching the sizes stream descriptions show. Multi-episode releases are
	// judged per episode; a season pack whose episode count cannot be parsed
	// is not judged at all.
	MinSizeGB float64 `json:"min_size_gb,omitempty"`
	MaxSizeGB float64 `json:"max_size_gb,omitempty"`
	// MaxAgeDays rejects releases posted longer ago than this. Releases with
	// no parseable date (library results) are kept.
	MaxAgeDays int `json:"max_age_days,omitempty"`
	// MinGrabs rejects releases with fewer recorded grabs. Releases that
	// report no grabs at all are kept.
	MinGrabs int `json:"min_grabs,omitempty"`
}

// Enabled reports whether any bound is set.
func (l LimitsConfig) Enabled() bool {
	return l.MinSizeGB > 0 || l.MaxSizeGB > 0 || l.MaxAgeDays > 0 || l.MinGrabs > 0
}

// ResolveLimits merges a profile's limits map down to the bounds that apply to
// one content kind: the default entry, overridden field by field by the kind's
// own entry.
func ResolveLimits(limits map[string]*LimitsConfig, kind string) LimitsConfig {
	out := LimitsConfig{}
	if len(limits) == 0 {
		return out
	}
	merge := func(l *LimitsConfig) {
		if l == nil {
			return
		}
		if l.MinSizeGB > 0 {
			out.MinSizeGB = l.MinSizeGB
		}
		if l.MaxSizeGB > 0 {
			out.MaxSizeGB = l.MaxSizeGB
		}
		if l.MaxAgeDays > 0 {
			out.MaxAgeDays = l.MaxAgeDays
		}
		if l.MinGrabs > 0 {
			out.MinGrabs = l.MinGrabs
		}
	}
	merge(limits[LimitKindDefault])
	if kind != "" && kind != LimitKindDefault {
		merge(limits[kind])
	}
	return out
}

// ScoringConfig scores releases by NZB attributes rather than parsed quality,
// as points added to the rank the title earned. It is the counterpart to
// LimitsConfig: limits decide what is eligible, scoring decides what sorts
// first among the eligible.
//
// Each attribute pairs a target with a weight. The target normalizes the
// attribute to a 0-1 factor and the weight says what a perfect score is worth,
// so a release contributes weight x factor points. A weight with no target (or
// a target with no weight) is inert. Weights may be negative to invert the
// preference — a negative grabs weight prefers the obscure release.
//
// Attributes fail open, like the limits do: a release that does not carry the
// attribute an entry needs scores zero for it rather than being penalized.
type ScoringConfig struct {
	// SizeTargetGB is the release size that scores best, in decimal gigabytes.
	// The factor falls off linearly either side of it, reaching zero at nothing
	// and at twice the target. Multi-episode releases are judged per episode
	// and unparseable packs are not judged, matching LimitsConfig.
	SizeTargetGB float64 `json:"size_target_gb,omitempty"`
	SizeWeight   int     `json:"size_weight,omitempty"`
	// AgeFreshDays is the age at which a release stops earning anything. A
	// release posted just now scores the full weight, one this many days old
	// scores nothing, and the factor falls linearly between.
	AgeFreshDays int `json:"age_fresh_days,omitempty"`
	AgeWeight    int `json:"age_weight,omitempty"`
	// GrabsTarget is the grab count that earns the full weight. The factor is
	// logarithmic: the step from 1 to 10 grabs is worth as much as the step
	// from 10 to 100, because that is how the difference actually reads.
	GrabsTarget int `json:"grabs_target,omitempty"`
	GrabsWeight int `json:"grabs_weight,omitempty"`
}

// Enabled reports whether any attribute would contribute points.
func (s ScoringConfig) Enabled() bool {
	return (s.SizeTargetGB > 0 && s.SizeWeight != 0) ||
		(s.AgeFreshDays > 0 && s.AgeWeight != 0) ||
		(s.GrabsTarget > 0 && s.GrabsWeight != 0)
}

// ResolveScoring merges a profile's scoring map down to the entry that applies
// to one content kind: the default entry, overridden field by field by the
// kind's own entry. Same shape and same precedence as ResolveLimits.
func ResolveScoring(scoring map[string]*ScoringConfig, kind string) ScoringConfig {
	out := ScoringConfig{}
	if len(scoring) == 0 {
		return out
	}
	merge := func(s *ScoringConfig) {
		if s == nil {
			return
		}
		if s.SizeTargetGB > 0 {
			out.SizeTargetGB = s.SizeTargetGB
		}
		if s.SizeWeight != 0 {
			out.SizeWeight = s.SizeWeight
		}
		if s.AgeFreshDays > 0 {
			out.AgeFreshDays = s.AgeFreshDays
		}
		if s.AgeWeight != 0 {
			out.AgeWeight = s.AgeWeight
		}
		if s.GrabsTarget > 0 {
			out.GrabsTarget = s.GrabsTarget
		}
		if s.GrabsWeight != 0 {
			out.GrabsWeight = s.GrabsWeight
		}
	}
	merge(scoring[LimitKindDefault])
	if kind != "" && kind != LimitKindDefault {
		merge(scoring[kind])
	}
	return out
}

// ScoringForKind resolves this profile's attribute scoring for one content kind.
func (fp *FilterProfileConfig) ScoringForKind(kind string) ScoringConfig {
	if fp == nil {
		return ScoringConfig{}
	}
	return ResolveScoring(fp.Scoring, kind)
}

// LimitsForKind resolves this profile's limits for one content kind.
func (fp *FilterProfileConfig) LimitsForKind(kind string) LimitsConfig {
	if fp == nil {
		return LimitsConfig{}
	}
	return ResolveLimits(fp.Limits, kind)
}

// EffectiveBlockPassworded reports whether the profile rejects releases the
// indexer flags as password protected. Nil (unset) defaults to true.
func (fp *FilterProfileConfig) EffectiveBlockPassworded() bool {
	if fp == nil || fp.BlockPassworded == nil {
		return true
	}
	return *fp.BlockPassworded
}

// defaultBlockedAttrs are the traits the shipped profile rejects outright: the
// CAM-class rips, the fake audio track dubbed over them, and satellite rips.
// Everything else jhin demotes is opened up and left to sort last instead.
//
// These stay blocked here as well as under the garbage veto, so turning that
// veto off does not quietly let camcorder rips back in. BLOCKED_ATTRS in
// frontend/src/lib/profiles.js mirrors this list for profiles made in the UI.
var defaultBlockedAttrs = []rank.Attr{
	rank.AttrCam, rank.AttrTeleSync, rank.AttrTeleCine,
	rank.AttrScreener, rank.AttrR5, rank.AttrPDTV,
	rank.AttrCleanAudio, rank.AttrSATRip,
}

// noScoreFloor is low enough that no stack of demotions reaches it, which
// leaves eligibility to the traits and resolutions alone.
const noScoreFloor = -1000000

// streamingOverrides are the scores where jhin's defaults and StreamNZB's job
// disagree.
//
// jhin is tuned for a downloader: fetch the very best copy and keep it, where
// file size costs you disk once. StreamNZB assembles the file from usenet
// articles while it plays, so size is a running cost paid on every playback —
// a 70 GB remux needs several times the bandwidth of a 20 GB encode and is far
// likelier to stall. These are the places where "best copy" and "best copy to
// stream" are not the same release.
var streamingOverrides = map[rank.Attr]int{
	// Remux at jhin's 10000 buries everything: no combination of resolution,
	// HDR and audio can outweigh it, so a remux always wins whatever it costs
	// to play. At 1500 it is still clearly the best source — WEB-DL scores
	// 200 — without being the only thing that matters.
	rank.AttrRemux: 1500,

	// Modern codecs carry the same picture in fewer bytes, which is exactly
	// the currency here. This is a uniform preference, not a guess about the
	// client: what a device can decode is the device's business, and nothing
	// here is hidden — an AVC release still sorts, just below its equivalent.
	rank.AttrHEVC: 700,
	rank.AttrAV1:  700,
	rank.AttrAVC:  300,
}

// defaultAttributePolicies opens every trait jhin blocks by default except the
// ones in defaultBlockedAttrs, then applies the streaming overrides. Traits not
// mentioned keep jhin's score.
//
// The blocked ones are written out too, so the profile says what it rejects
// rather than leaving it to the baseline.
func defaultAttributePolicies() map[rank.Attr]rank.Policy {
	blocked := make(map[rank.Attr]bool, len(defaultBlockedAttrs))
	for _, attr := range defaultBlockedAttrs {
		blocked[attr] = true
	}

	policies := make(map[rank.Attr]rank.Policy, len(rank.DefaultPolicies))
	for attr, policy := range rank.DefaultPolicies {
		switch {
		case blocked[attr]:
			policies[attr] = rank.Policy{Fetch: false, Rank: policy.Rank}
		case !policy.Fetch:
			policies[attr] = rank.Policy{Fetch: true, Rank: policy.Rank}
		}
	}
	for attr, score := range streamingOverrides {
		policies[attr] = rank.Policy{Fetch: true, Rank: score}
	}
	return policies
}
