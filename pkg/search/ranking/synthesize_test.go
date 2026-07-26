package ranking_test

import (
	"testing"

	"github.com/dreulavelle/jhin/rank"

	"streamnzb/pkg/core/config"
)

// The legacy "must contain" rules map onto jhin's Require, which gates
// without exempting a release from the profile's other rules.
func TestSynthesizeRequireRules(t *testing.T) {
	yes := true
	p := config.Synthesize(config.FilterProfileConfig{
		Name:               "Legacy",
		AllowedResolutions: []string{"1080p", "720p"},
		BlockedQualities:   []string{"CAM"},
		RequireHDR:         &yes,
		RequiredKeywords:   []string{"Obsession"},
	})
	r, err := rank.New(p)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		title string
		want  bool
	}{
		{"Obsession 2026 1080p BluRay HDR", true},
		{"Obsession 2026 1080p BluRay", false},          // HDR required
		{"Something Else 2026 1080p BluRay HDR", false}, // keyword required
		{"Obsession 2026 1080p CAM HDR", false},         // require must not bypass the CAM block
		{"Obsession 2026 2160p BluRay HDR", false},      // require must not bypass the resolution gate
	}
	for _, tt := range tests {
		got := r.Rank(tt.title)
		if got.Fetch != tt.want {
			t.Errorf("%-42s Fetch=%v want %v (rejections %v)", tt.title, got.Fetch, tt.want, got.Rejections)
		}
	}
}
