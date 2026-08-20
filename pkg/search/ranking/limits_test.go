package ranking_test

import (
	"strings"
	"testing"
	"time"

	"github.com/dreulavelle/jhin/rank"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/ranking"
	"streamnzb/pkg/search/triage"
)

func limitsProfile(t *testing.T, fp config.FilterProfileConfig) *ranking.Profile {
	t.Helper()
	if fp.Name == "" {
		fp.Name = "Limits"
	}
	// A profile with no scoring of its own inherits the preset's size
	// preference. These tests measure one attribute at a time, so they opt out
	// with an empty map rather than a nil one.
	if fp.Scoring == nil {
		fp.Scoring = map[string]*config.ScoringConfig{}
	}
	p, err := ranking.Compile(fp)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func candidateWith(rel *release.Release) []triage.Candidate {
	return []triage.Candidate{{Release: rel}}
}

// applyOne runs one release through the profile and reports whether it was
// kept, plus the rejection reasons when it was not.
func applyOne(p *ranking.Profile, kind string, rel *release.Release) (bool, []string) {
	kept, rejected := p.ApplyWithRejected(ranking.Request{Kind: kind}, candidateWith(rel), rank.RankOptions{})
	if len(kept) == 1 {
		return true, nil
	}
	if len(rejected) == 1 {
		return false, rejected[0].Torrent.Rejections
	}
	return false, nil
}

func TestLimitsSizeBounds(t *testing.T) {
	gb := int64(1e9)
	p := limitsProfile(t, config.FilterProfileConfig{
		Limits: map[string]*config.LimitsConfig{
			config.LimitKindDefault: {MinSizeGB: 1, MaxSizeGB: 30},
			ranking.KindSeries:      {MaxSizeGB: 5},
		},
	})

	tests := []struct {
		name  string
		kind  string
		title string
		size  int64
		want  bool
	}{
		{"movie within bounds", ranking.KindMovie, "Movie 2020 1080p BluRay-GRP", 20 * gb, true},
		{"movie above max", ranking.KindMovie, "Movie 2020 1080p BluRay-GRP", 42 * gb, false},
		{"movie below min", ranking.KindMovie, "Movie 2020 1080p BluRay-GRP", gb / 2, false},
		{"series overrides default max", ranking.KindSeries, "Show S01E01 1080p WEB-GRP", 8 * gb, false},
		{"series within its own max", ranking.KindSeries, "Show S01E01 1080p WEB-GRP", 4 * gb, true},
		{"movie ignores the series override", ranking.KindMovie, "Movie 2020 1080p BluRay-GRP", 8 * gb, true},
		{"season pack unknown count passes", ranking.KindSeries, "Show S01 1080p WEB-GRP", 40 * gb, true},
		{"unreported size passes", ranking.KindMovie, "Movie 2020 1080p BluRay-GRP", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reasons := applyOne(p, tt.kind, &release.Release{Title: tt.title, Size: tt.size})
			if got != tt.want {
				t.Errorf("kept = %v, want %v (rejections: %v)", got, tt.want, reasons)
			}
		})
	}
}

func TestLimitsAgeAndGrabs(t *testing.T) {
	p := limitsProfile(t, config.FilterProfileConfig{
		Limits: map[string]*config.LimitsConfig{
			config.LimitKindDefault: {MaxAgeDays: 300, MinGrabs: 5},
		},
	})

	old := time.Now().Add(-400 * 24 * time.Hour).Format(time.RFC1123Z)
	fresh := time.Now().Add(-10 * 24 * time.Hour).Format(time.RFC1123Z)

	tests := []struct {
		name string
		rel  *release.Release
		want bool
	}{
		{"fresh enough", &release.Release{Title: "Movie 2020 1080p BluRay-GRP", PubDate: fresh, Grabs: 9}, true},
		{"too old", &release.Release{Title: "Movie 2020 1080p BluRay-GRP", PubDate: old, Grabs: 9}, false},
		{"no date fails open", &release.Release{Title: "Movie 2020 1080p BluRay-GRP", Grabs: 9}, true},
		{"too few grabs", &release.Release{Title: "Movie 2020 1080p BluRay-GRP", PubDate: fresh, Grabs: 2}, false},
		{"unreported grabs fail open", &release.Release{Title: "Movie 2020 1080p BluRay-GRP", PubDate: fresh}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reasons := applyOne(p, ranking.KindMovie, tt.rel)
			if got != tt.want {
				t.Errorf("kept = %v, want %v (rejections: %v)", got, tt.want, reasons)
			}
		})
	}
}

func TestLimitsBlockPassworded(t *testing.T) {
	rel := func() *release.Release {
		return &release.Release{Title: "Movie 2020 1080p BluRay-GRP", Password: true}
	}

	// Default: passworded releases are rejected, with a reason that says so.
	p := limitsProfile(t, config.FilterProfileConfig{})
	kept, reasons := applyOne(p, ranking.KindMovie, rel())
	if kept {
		t.Fatal("expected the passworded release to be rejected")
	}
	if len(reasons) == 0 || !strings.Contains(strings.Join(reasons, " "), "password") {
		t.Errorf("expected a password rejection reason, got %v", reasons)
	}

	// Explicitly disabled: the release passes.
	off := false
	p = limitsProfile(t, config.FilterProfileConfig{BlockPassworded: &off})
	if kept, reasons := applyOne(p, ranking.KindMovie, rel()); !kept {
		t.Errorf("expected the release to be kept with blocking off (rejections: %v)", reasons)
	}
}
