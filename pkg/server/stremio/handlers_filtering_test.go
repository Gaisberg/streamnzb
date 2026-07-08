package stremio

import (
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/parser"
	"streamnzb/pkg/search/triage"
)

func TestMatchFilterProfile(t *testing.T) {
	ptrBool := func(v bool) *bool { return &v }

	profile := &config.FilterProfileConfig{
		Name:               "TestProfile",
		AllowedResolutions: []string{"1080p", "720p"},
		BlockedQualities:   []string{"CAM", "TeleSync"},
		RequireHDR:         ptrBool(true),
		ExcludedKeywords:   []string{"subbed", "dubbed"},
		RequiredKeywords:   []string{"Obsession"},
	}

	tests := []struct {
		title    string
		metadata *parser.ParsedRelease
		expected bool
	}{
		{
			title: "Obsession 2026 1080p BluRay HDR",
			metadata: &parser.ParsedRelease{
				Resolution: "1080p",
				Quality:    "BluRay",
				HDR:        []string{"HDR"},
			},
			expected: true,
		},
		{
			title: "Obsession 2026 1080p CAM HDR",
			metadata: &parser.ParsedRelease{
				Resolution: "1080p",
				Quality:    "CAM",
				HDR:        []string{"HDR"},
			},
			expected: false, // Blocked quality
		},
		{
			title: "Obsession 2026 2160p BluRay HDR",
			metadata: &parser.ParsedRelease{
				Resolution: "2160p",
				Quality:    "BluRay",
				HDR:        []string{"HDR"},
			},
			expected: false, // Not allowed resolution
		},
		{
			title: "Obsession 2026 1080p BluRay SDR",
			metadata: &parser.ParsedRelease{
				Resolution: "1080p",
				Quality:    "BluRay",
				HDR:        []string{"SDR"},
			},
			expected: false, // HDR required
		},
		{
			title: "Obsession 2026 1080p BluRay HDR Subbed",
			metadata: &parser.ParsedRelease{
				Resolution: "1080p",
				Quality:    "BluRay",
				HDR:        []string{"HDR"},
			},
			expected: false, // Excluded keyword "subbed"
		},
		{
			title: "Something Else 2026 1080p BluRay HDR",
			metadata: &parser.ParsedRelease{
				Resolution: "1080p",
				Quality:    "BluRay",
				HDR:        []string{"HDR"},
			},
			expected: false, // Required keyword "Obsession" missing
		},
	}

	for _, tc := range tests {
		cand := triage.Candidate{
			Release:  &release.Release{Title: tc.title},
			Metadata: tc.metadata,
		}
		got := matchFilterProfile(cand, profile)
		if got != tc.expected {
			t.Errorf("matchFilterProfile(%q) = %v, want %v", tc.title, got, tc.expected)
		}
	}
}

func TestCustomSortCandidates(t *testing.T) {
	candidates := []triage.Candidate{
		{
			Release: &release.Release{Title: "Release A", Size: 5000000000}, // 5 GB
			Metadata: &parser.ParsedRelease{
				Resolution: "1080p",
				Quality:    "WEB-DL",
			},
		},
		{
			Release: &release.Release{Title: "Release B", Size: 8000000000}, // 8 GB
			Metadata: &parser.ParsedRelease{
				Resolution: "2160p",
				Quality:    "WEB-DL",
			},
		},
		{
			Release: &release.Release{Title: "Release C", Size: 12000000000}, // 12 GB
			Metadata: &parser.ParsedRelease{
				Resolution: "1080p",
				Quality:    "BluRay",
			},
		},
	}

	// 1. Sort by Resolution first
	customSortCandidates(candidates, []string{"resolution"})
	if candidates[0].Release.Title != "Release B" { // B is 2160p, others are 1080p
		t.Errorf("Expected first to be Release B, got %q", candidates[0].Release.Title)
	}

	// 2. Sort by Quality first
	customSortCandidates(candidates, []string{"quality"})
	if candidates[0].Release.Title != "Release C" { // C is BluRay, others WEB-DL
		t.Errorf("Expected first to be Release C, got %q", candidates[0].Release.Title)
	}

	// 3. Sort by Size first
	customSortCandidates(candidates, []string{"size"})
	if candidates[0].Release.Title != "Release C" { // C is 12GB, B is 8GB, A is 5GB
		t.Errorf("Expected first to be Release C, got %q", candidates[0].Release.Title)
	}
}

