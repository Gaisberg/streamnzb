package stremio

import (
	"testing"

	"streamnzb/pkg/release"
	"streamnzb/pkg/search/triage"
)

func TestFilterCandidatesDoesNotMutateInputSlice(t *testing.T) {
	unavailableURL := "https://example.invalid/unavailable"
	availableURL := "https://example.invalid/available"
	original := []triage.Candidate{
		{Release: &release.Release{Title: "Unavailable", DetailsURL: unavailableURL, Indexer: "altHUB"}},
		{Release: &release.Release{Title: "Available", DetailsURL: availableURL, Indexer: "altHUB"}},
	}

	inputSnapshotFirstURL := original[0].Release.DetailsURL
	filtered := filterCandidates(original, false, true, map[string]bool{unavailableURL: true})

	if len(filtered) != 1 || filtered[0].Release == nil || filtered[0].Release.DetailsURL != availableURL {
		t.Fatalf("expected filtered slice to keep only available candidate, got %+v", filtered)
	}

	// Ensure callers that retain the original slice still see the original entries.
	if original[0].Release == nil || original[0].Release.DetailsURL != inputSnapshotFirstURL {
		t.Fatalf("filterCandidates mutated original slice; got first URL %q, want %q", original[0].Release.DetailsURL, inputSnapshotFirstURL)
	}
}

func TestRecordAvailIndexerStatsCountsUnavailableFromOriginalCandidates(t *testing.T) {
	s := &Server{availIndexerStats: make(map[string]AvailIndexerStats)}
	unavailableURL := "https://example.invalid/unavailable"
	input := []triage.Candidate{
		{Release: &release.Release{Title: "Unavailable", DetailsURL: unavailableURL, Indexer: "altHUB"}},
	}
	final := []triage.Candidate{}
	source := &playlistSource{
		UnavailableDetailsURLs: map[string]bool{unavailableURL: true},
		CachedAvailable:        map[string]bool{},
	}

	s.recordAvailIndexerStats(input, final, source, true)
	stats := s.GetAvailIndexerStats()
	got := stats["altHUB"].Discarded
	if got != 1 {
		t.Fatalf("discarded count = %d, want 1", got)
	}
}

