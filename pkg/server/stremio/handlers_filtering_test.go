package stremio

import (
	"testing"

	"github.com/dreulavelle/jhin"

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
		AllowedLanguages:   []string{"en", "fi"},
		BlockedLanguages:   []string{"ru"},
	}

	tests := []struct {
		title    string
		metadata *parser.ParsedRelease
		expected bool
	}{
		{
			title: "Obsession 2026 1080p BluRay HDR",
			metadata: &parser.ParsedRelease{Result: &jhin.Result{
				Resolution: "1080p",
				Quality:    "BluRay",
				HDR:        []string{"HDR"},
				Languages:  []string{"en"},
			}},
			expected: true,
		},
		{
			title: "Obsession 2026 1080p CAM HDR",
			metadata: &parser.ParsedRelease{Result: &jhin.Result{
				Resolution: "1080p",
				Quality:    "CAM",
				HDR:        []string{"HDR"},
				Languages:  []string{"en"},
			}},
			expected: false, // Blocked quality
		},
		{
			title: "Obsession 2026 2160p BluRay HDR",
			metadata: &parser.ParsedRelease{Result: &jhin.Result{
				Resolution: "2160p",
				Quality:    "BluRay",
				HDR:        []string{"HDR"},
				Languages:  []string{"en"},
			}},
			expected: false, // Not allowed resolution
		},
		{
			title: "Obsession 2026 1080p BluRay SDR",
			metadata: &parser.ParsedRelease{Result: &jhin.Result{
				Resolution: "1080p",
				Quality:    "BluRay",
				HDR:        []string{"SDR"},
				Languages:  []string{"en"},
			}},
			expected: false, // HDR required
		},
		{
			title: "Obsession 2026 1080p BluRay HDR Subbed",
			metadata: &parser.ParsedRelease{Result: &jhin.Result{
				Resolution: "1080p",
				Quality:    "BluRay",
				HDR:        []string{"HDR"},
				Languages:  []string{"en"},
			}},
			expected: false, // Excluded keyword "subbed"
		},
		{
			title: "Something Else 2026 1080p BluRay HDR",
			metadata: &parser.ParsedRelease{Result: &jhin.Result{
				Resolution: "1080p",
				Quality:    "BluRay",
				HDR:        []string{"HDR"},
				Languages:  []string{"en"},
			}},
			expected: false, // Required keyword "Obsession" missing
		},
		{
			title: "Obsession 2026 1080p BluRay HDR RU",
			metadata: &parser.ParsedRelease{Result: &jhin.Result{
				Resolution: "1080p",
				Quality:    "BluRay",
				HDR:        []string{"HDR"},
				Languages:  []string{"ru"},
			}},
			expected: false, // Blocked language "ru"
		},
		{
			title: "Obsession 2026 1080p BluRay HDR",
			metadata: &parser.ParsedRelease{Result: &jhin.Result{
				Resolution: "1080p",
				Quality:    "BluRay",
				HDR:        []string{"HDR"},
				Languages:  []string{"de"}, // not in allowed languages
			}},
			expected: false, // Allowed languages en/fi, release is de
		},
		{
			title: "Obsession 2026 1080p BluRay HDR NORDIC",
			metadata: &parser.ParsedRelease{Result: &jhin.Result{
				Resolution: "1080p",
				Quality:    "BluRay",
				HDR:        []string{"HDR"},
				// Alias expansion: "nordic" -> da, fi, no, sv; fi is allowed.
				Languages: []string{"da", "fi", "no", "sv"},
			}},
			expected: true, // fi is in allowed languages
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
			Metadata: &parser.ParsedRelease{Result: &jhin.Result{
				Resolution: "1080p",
				Quality:    "WEB-DL",
			}},
		},
		{
			Release: &release.Release{Title: "Release B", Size: 8000000000}, // 8 GB
			Metadata: &parser.ParsedRelease{Result: &jhin.Result{
				Resolution: "2160p",
				Quality:    "WEB-DL",
			}},
		},
		{
			Release: &release.Release{Title: "Release C", Size: 12000000000}, // 12 GB
			Metadata: &parser.ParsedRelease{Result: &jhin.Result{
				Resolution: "1080p",
				Quality:    "BluRay",
			}},
		},
	}

	// 1. Sort by Resolution first
	customSortCandidates(candidates, []string{"resolution"}, nil)
	if candidates[0].Release.Title != "Release B" { // B is 2160p, others are 1080p
		t.Errorf("Expected first to be Release B, got %q", candidates[0].Release.Title)
	}

	// 2. Sort by Quality first
	customSortCandidates(candidates, []string{"quality"}, nil)
	if candidates[0].Release.Title != "Release C" { // C is BluRay, others WEB-DL
		t.Errorf("Expected first to be Release C, got %q", candidates[0].Release.Title)
	}

	// 3. Sort by Size first
	customSortCandidates(candidates, []string{"size"}, nil)
	if candidates[0].Release.Title != "Release C" { // C is 12GB, B is 8GB, A is 5GB
		t.Errorf("Expected first to be Release C, got %q", candidates[0].Release.Title)
	}
}

func TestCustomSortCandidatesByLanguage(t *testing.T) {
	candidates := []triage.Candidate{
		{
			Release: &release.Release{Title: "Release A"},
			Metadata: &parser.ParsedRelease{Result: &jhin.Result{
				Languages: []string{"en"},
			}},
		},
		{
			Release: &release.Release{Title: "Release B"},
			Metadata: &parser.ParsedRelease{Result: &jhin.Result{
				// Alias-expanded nordic release includes fi.
				Languages: []string{"da", "fi", "no", "sv"},
			}},
		},
		{
			Release: &release.Release{Title: "Release C"},
			Metadata: &parser.ParsedRelease{Result: &jhin.Result{
				Languages: []string{"de"},
			}},
		},
		{
			// Release D has no title-parsed languages but the indexer reported fi.
			Release: &release.Release{Title: "Release D", Languages: []string{"fi"}},
			Metadata: &parser.ParsedRelease{Result: &jhin.Result{
				Languages: nil,
			}},
		},
		{
			Release: &release.Release{Title: "Release E"},
			Metadata: &parser.ParsedRelease{Result: &jhin.Result{
				Languages: nil,
			}},
		},
	}

	// Preferred languages: fi and en. Releases A (en), B (fi), D (fi via release.Languages)
	// should rank above C (de, no preferred match) and E (no languages at all).
	customSortCandidates(candidates, []string{"language"}, []string{"fi", "en"})

	top3 := map[string]bool{candidates[0].Release.Title: true, candidates[1].Release.Title: true, candidates[2].Release.Title: true}
	if !top3["Release A"] {
		t.Errorf("Expected Release A in top 3, got %q, %q, %q", candidates[0].Release.Title, candidates[1].Release.Title, candidates[2].Release.Title)
	}
	if !top3["Release B"] {
		t.Errorf("Expected Release B in top 3, got %q, %q, %q", candidates[0].Release.Title, candidates[1].Release.Title, candidates[2].Release.Title)
	}
	if !top3["Release D"] {
		t.Errorf("Expected Release D (fi via release.Languages) in top 3, got %q, %q, %q", candidates[0].Release.Title, candidates[1].Release.Title, candidates[2].Release.Title)
	}
	if candidates[4].Release.Title != "Release E" {
		t.Errorf("Expected Release E (no languages) last, got %q", candidates[4].Release.Title)
	}
}
