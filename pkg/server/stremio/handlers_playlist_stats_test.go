package stremio

import (
	"testing"

	"streamnzb/pkg/core/config"
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

func TestApplyPlaylistFilteringCanDisableAvailNZBReportedBadFiltering(t *testing.T) {
	unavailableURL := "https://example.invalid/unavailable"
	availableURL := "https://example.invalid/available"
	candidates := []triage.Candidate{
		{Release: &release.Release{Title: "Unavailable", DetailsURL: unavailableURL}},
		{Release: &release.Release{Title: "Available", DetailsURL: availableURL}},
	}
	source := &playlistSource{
		UnavailableDetailsURLs: map[string]bool{unavailableURL: true},
	}

	enabledServer := &Server{
		config: &config.Config{AvailNZBFilterReportedBad: configPtrBool(true)},
	}
	enabled := enabledServer.applyPlaylistFiltering(candidates, source, false, false, "none", nil)
	if len(enabled) != 1 || enabled[0].Release == nil || enabled[0].Release.DetailsURL != availableURL {
		t.Fatalf("expected unavailable release filtered when enabled, got %+v", enabled)
	}

	disabledServer := &Server{
		config: &config.Config{AvailNZBFilterReportedBad: configPtrBool(false)},
	}
	disabled := disabledServer.applyPlaylistFiltering(candidates, source, false, false, "none", nil)
	if len(disabled) != 2 {
		t.Fatalf("expected unavailable release kept when disabled, got %+v", disabled)
	}

	modeOffServer := &Server{
		config: &config.Config{
			AvailNZBMode:              "off",
			AvailNZBFilterReportedBad: configPtrBool(true),
		},
	}
	modeOff := modeOffServer.applyPlaylistFiltering(candidates, source, false, false, "none", nil)
	if len(modeOff) != 2 {
		t.Fatalf("expected unavailable release kept when AvailNZB mode is off, got %+v", modeOff)
	}
}

func TestRecordAvailIndexerStatsSkipsDiscardedWhenAvailFilteringDisabled(t *testing.T) {
	s := &Server{
		config:            &config.Config{AvailNZBFilterReportedBad: configPtrBool(false)},
		availIndexerStats: make(map[string]AvailIndexerStats),
	}
	unavailableURL := "https://example.invalid/unavailable"
	input := []triage.Candidate{
		{Release: &release.Release{Title: "Unavailable", DetailsURL: unavailableURL, Indexer: "altHUB"}},
	}
	source := &playlistSource{
		UnavailableDetailsURLs: map[string]bool{unavailableURL: true},
		CachedAvailable:        map[string]bool{},
	}

	s.recordAvailIndexerStats(input, nil, source, true)
	stats := s.GetAvailIndexerStats()
	got := stats["altHUB"].Discarded
	if got != 0 {
		t.Fatalf("discarded count = %d, want 0 when filtering disabled", got)
	}
}

func TestAddUniqueIndexerHitsAccumulatesAndSnapshots(t *testing.T) {
	s := &Server{uniqueIndexerHits: make(map[string]int64)}

	s.addUniqueIndexerHits(map[string]int{"A": 1, "B": 2})
	s.addUniqueIndexerHits(map[string]int{"A": 3, "": 10, "B": -1})

	got := s.GetUniqueIndexerHits()
	if got["A"] != 4 {
		t.Fatalf("A hits = %d, want 4", got["A"])
	}
	if got["B"] != 2 {
		t.Fatalf("B hits = %d, want 2", got["B"])
	}
	if _, ok := got[""]; ok {
		t.Fatal("empty indexer name should not be tracked")
	}
}

func configPtrBool(v bool) *bool {
	return &v
}
