package stremio

import (
	"testing"

	"streamnzb/pkg/auth"
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
	s := &Server{
		config:            &config.Config{AvailNZBMode: "on"},
		availIndexerStats: make(map[string]AvailIndexerStats),
	}
	unavailableURL := "https://example.invalid/unavailable"
	input := []triage.Candidate{
		{Release: &release.Release{Title: "Unavailable", DetailsURL: unavailableURL, Indexer: "altHUB"}},
	}
	final := []triage.Candidate{}
	source := &playlistSource{
		UnavailableDetailsURLs: map[string]bool{unavailableURL: true},
		CachedAvailable:        map[string]bool{},
	}

	stream := &auth.Stream{FilterAvailNZB: configPtrBool(true)}
	s.recordAvailIndexerStats(input, final, source, true, stream)
	stats := s.GetAvailIndexerStats()
	got := stats["altHUB"].Discarded
	if got != 1 {
		t.Fatalf("discarded count = %d, want 1", got)
	}
}

func TestRecordAvailIndexerStatsAttributesEveryCopy(t *testing.T) {
	s := &Server{
		config:            &config.Config{AvailNZBMode: "on"},
		availIndexerStats: make(map[string]AvailIndexerStats),
	}
	primaryURL := "https://example.invalid/primary"
	variantURL := "https://example.invalid/variant"
	unavailableVariantURL := "https://example.invalid/unavailable-variant"
	merged := &release.Release{
		Title:      "Release",
		DetailsURL: primaryURL,
		Indexer:    "altHUB",
		Variants: []*release.Release{
			{Title: "Release", DetailsURL: variantURL, Indexer: "abNZB"},
			{Title: "Release", DetailsURL: unavailableVariantURL, Indexer: "NinjaCentral"},
		},
	}
	input := []triage.Candidate{{Release: merged}}
	source := &playlistSource{
		CachedAvailable:        map[string]bool{primaryURL: true, variantURL: true},
		UnavailableDetailsURLs: map[string]bool{unavailableVariantURL: true},
	}

	stream := &auth.Stream{FilterAvailNZB: configPtrBool(true)}
	s.recordAvailIndexerStats(input, input, source, true, stream)
	stats := s.GetAvailIndexerStats()
	if got := stats["altHUB"].AvailableReturned; got != 1 {
		t.Fatalf("altHUB available count = %d, want 1", got)
	}
	if got := stats["abNZB"].AvailableReturned; got != 1 {
		t.Fatalf("abNZB available count = %d, want 1", got)
	}
	if got := stats["NinjaCentral"].Discarded; got != 1 {
		t.Fatalf("NinjaCentral discarded count = %d, want 1", got)
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

	server := &Server{
		config: &config.Config{AvailNZBMode: "on"},
	}
	enabledStream := &auth.Stream{FilterAvailNZB: configPtrBool(true)}
	enabled := server.applyPlaylistFiltering(candidates, source, false, false, "none", enabledStream)
	if len(enabled) != 1 || enabled[0].Release == nil || enabled[0].Release.DetailsURL != availableURL {
		t.Fatalf("expected unavailable release filtered when enabled, got %+v", enabled)
	}

	disabledStream := &auth.Stream{FilterAvailNZB: configPtrBool(false)}
	disabled := server.applyPlaylistFiltering(candidates, source, false, false, "none", disabledStream)
	if len(disabled) != 2 {
		t.Fatalf("expected unavailable release kept when disabled, got %+v", disabled)
	}

	modeOffServer := &Server{
		config: &config.Config{
			AvailNZBMode: "off",
		},
	}
	modeOff := modeOffServer.applyPlaylistFiltering(candidates, source, false, false, "none", enabledStream)
	if len(modeOff) != 2 {
		t.Fatalf("expected unavailable release kept when AvailNZB mode is off, got %+v", modeOff)
	}
}

func TestRecordAvailIndexerStatsSkipsDiscardedWhenAvailFilteringDisabled(t *testing.T) {
	s := &Server{
		config:            &config.Config{AvailNZBMode: "on"},
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
	disabledStream := &auth.Stream{FilterAvailNZB: configPtrBool(false)}

	s.recordAvailIndexerStats(input, nil, source, true, disabledStream)
	if stats := s.GetAvailIndexerStats(); len(stats) != 0 {
		t.Fatalf("expected empty stats when avail filtering is disabled, got %+v", stats)
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
