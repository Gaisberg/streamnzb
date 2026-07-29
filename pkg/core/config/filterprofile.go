package config

import "github.com/dreulavelle/jhin/rank"

// DefaultFilterProfileName is the profile seeded into a fresh config.
const DefaultFilterProfileName = "Default Profile"

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

// DefaultFilterProfile is the profile new installs start with, expressed as a
// jhin profile rather than left to legacy synthesis.
//
// It is deliberately permissive: this is the profile everyone begins from and
// branches off, so it rejects only what nobody is asking for — camcorder rips,
// the garbage that carries no source of its own, and adult releases — and lets
// the rest through. Quality preferences are expressed as score, so a poor
// release sorts last rather than disappearing.
func DefaultFilterProfile() FilterProfileConfig {
	profile := rank.Default()
	profile.Name = DefaultFilterProfileName

	// Streaming happens on the fly from archive segments, so the SD tiers are
	// more trouble than they are worth; jhin's defaults already disable them.
	// Unknown stays on so an unparsed release is not silently dropped.
	profile.Resolutions[rank.Res2160p] = true
	profile.Resolutions[rank.Res1440p] = true
	profile.Resolutions[rank.Res1080p] = true
	profile.Resolutions[rank.Res720p] = true
	profile.Resolutions[rank.ResUnknown] = true

	profile.Options.RemoveAdult = true
	// Catches the garbage the per-source toggles cannot: leaked copies,
	// pre-retail rips and deleted-scene reels carry no source of their own.
	profile.Options.RemoveTrash = true
	// Score orders results, it does not reject them: a release demoted by
	// several traits should sort last, not fall through the floor.
	profile.Options.MinRank = noScoreFloor
	profile.Options.PreferredBonus = 10000
	profile.Languages.Preferred = []string{"en"}

	profile.Attributes = defaultAttributePolicies()

	return FilterProfileConfig{
		Name:    DefaultFilterProfileName,
		Ranking: &profile,
	}
}

// defaultAttributePolicies opens every trait jhin blocks by default except the
// ones in defaultBlockedAttrs, keeping jhin's score so the order is unchanged.
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
	policies[rank.AttrRemux] = rank.Policy{Fetch: true, Rank: 5000}
	return policies
}
