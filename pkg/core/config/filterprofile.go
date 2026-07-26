package config

import "github.com/dreulavelle/jhin/rank"

// DefaultFilterProfileName is the profile seeded into a fresh config.
const DefaultFilterProfileName = "Default Profile"

// DefaultFilterProfile is the profile new installs start with, expressed as a
// jhin profile rather than left to legacy synthesis.
//
// It reproduces what StreamNZB shipped before jhin — 4K/1080p/720p allowed and
// CAM blocked — and additionally leans on what jhin can express that the old
// schema could not: trash and adult releases are rejected outright, and scoring
// favours remuxes and better audio instead of treating every eligible release
// as equal. Sorting stays resolution-first, then score, with the Usenet signals
// breaking ties.
func DefaultFilterProfile() FilterProfileConfig {
	profile := rank.Default()
	profile.Name = DefaultFilterProfileName

	// Streaming happens on the fly from archive segments, so SD tiers are more
	// trouble than they are worth here; jhin's defaults already disable them.
	profile.Resolutions[rank.Res2160p] = true
	profile.Resolutions[rank.Res1440p] = true
	profile.Resolutions[rank.Res1080p] = true
	profile.Resolutions[rank.Res720p] = true

	profile.Options.RemoveTrash = true
	profile.Options.RemoveAdult = true

	return FilterProfileConfig{
		Name:      DefaultFilterProfileName,
		Ranking:   &profile,
		SortOrder: []string{"resolution", "rank", "size", "age"},
	}
}
