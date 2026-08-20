package triage

import (
	"time"

	"streamnzb/pkg/release"
)

// AvailStatus is what the community availability database says about a
// release. It is deliberately three-valued: "not reported bad" and "reported
// good" are different claims, and collapsing them to a bool loses the only
// distinction that matters when deciding whether to trust the record.
type AvailStatus string

const (
	// AvailUnknown means nobody has reported this release either way. It is
	// the common case, so every rule reading availability must treat it as
	// "no evidence" rather than as bad news.
	AvailUnknown AvailStatus = "unknown"
	// AvailAvailable means the database reports the release as retrievable.
	AvailAvailable AvailStatus = "available"
	// AvailUnavailable means the database reports it as missing.
	AvailUnavailable AvailStatus = "unavailable"
)

// AvailState is one release's community availability record. Availability is
// per backbone rather than global: a release alive on one provider's backbone
// and dead on another is not the same as a release that is simply available,
// and the stream already knows which providers it uses.
type AvailState struct {
	Status AvailStatus
	// Backbones names the backbones the database has an opinion about,
	// mapped to whether each reports the release healthy.
	Backbones map[string]bool
	// OnMyBackbone reports the release healthy on at least one backbone this
	// stream's own providers use. False when no backbone matched, including
	// when nothing is known.
	OnMyBackbone bool
	// CheckedAt is when the record was last updated; the zero time means
	// unknown. Age matters — a year-old "available" is a weaker claim than
	// yesterday's.
	CheckedAt time.Time
	// Compression is the archive format the database recorded, e.g. "rar" or
	// "7z", empty when unknown.
	Compression string
}

// Known reports whether the database has any opinion at all.
func (a AvailState) Known() bool { return a.Status == AvailAvailable || a.Status == AvailUnavailable }

// CheckedDaysAgo is how stale the record is in days, or -1 when the record
// carries no timestamp. Negative means "do not judge me on this", matching how
// every other missing attribute in the pipeline behaves.
func (a AvailState) CheckedDaysAgo() int {
	if a.CheckedAt.IsZero() {
		return -1
	}
	return int(time.Since(a.CheckedAt).Hours() / 24)
}

// RuleMatch is one named scoring rule that fired on a release, in the shape
// custom result formats consume: a name a user chose and the points it was
// worth.
type RuleMatch struct {
	Name  string `json:"name"`
	Score int    `json:"score"`
}

// Verdict is everything the pipeline decided about one release that is not the
// release itself. It exists because each stage used to collapse its judgement
// into a single score on the way out, leaving formatting, diagnostics and
// later filtering stages with a number and no way back to the reasoning.
//
// Fields are filled by whichever stage owns them and are safe to read unset:
// a candidate that no profile ranked has an empty Kind and no matches, and a
// release that was never probed or never checked simply has no caps and an
// unknown availability status.
type Verdict struct {
	// Kind is the ranking content kind the request resolved to
	// ("movie", "series", "anime_movie", "anime_show"), empty when no profile
	// ran.
	Kind string
	// IsAnime is the anime classification behind Kind, kept separately
	// because it is the half of the decision that custom formats ask for.
	IsAnime bool

	// Rejections is why a profile turned the release away, empty when it was
	// kept.
	Rejections []string
	// Matched lists the named rules that contributed points.
	Matched []RuleMatch

	// Probed is what ffprobe measured about the file. Non-nil only for
	// library releases: a fresh indexer hit has never been opened, so its
	// real properties are unknown until playback.
	Probed *release.MediaCaps

	// Avail is the community availability record.
	Avail AvailState
}
