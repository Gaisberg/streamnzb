package stremio

import (
	"strings"
	"testing"

	"streamnzb/pkg/release"
	"streamnzb/pkg/search/parser"
	"streamnzb/pkg/search/triage"
)

func languageCandidate(title string, tagged ...string) triage.Candidate {
	return triage.Candidate{
		Release:  &release.Release{Title: title, Languages: tagged},
		Metadata: parser.ParseReleaseTitle(title),
	}
}

// Issue #283: an Arabic release is regularly posted under an untouched English
// name and tagged as Arabic only in the indexer's feed. AIOStreams filters on
// what it can read off a stream, so the tag has to leave StreamNZB — as codes
// in the languages field, and as flags in the description its parser scans.
func TestBuildStreamsSurfacesIndexerLanguagesToAIOStreams(t *testing.T) {
	list := &playlistResult{
		IsAIOStreams: true,
		Candidates:   []triage.Candidate{languageCandidate("Movie.2024.1080p.WEB-DL.x264-GRP", "Arabic")},
	}
	key := StreamSlotKey{StreamID: "Standalone", ContentType: "movie", ID: "tt1"}
	streams := buildStreamsFromPlaylist(list, key, "Standalone", DefaultServiceName, "http://host", true, nil)
	if len(streams) != 1 {
		t.Fatalf("got %d streams, want 1", len(streams))
	}
	if got := streams[0].Languages; len(got) != 1 || got[0] != "ar" {
		t.Errorf("Languages = %v, want [ar]", got)
	}
	if !strings.Contains(streams[0].Description, "🇸🇦") {
		t.Errorf("description carries no Arabic flag: %q", streams[0].Description)
	}
}

// The plain Stremio description is unchanged: flags are an aggregator's input,
// and a result format that wants them asks for {{.LanguageFlags}}.
func TestBuildStreamsKeepsFlagsOutOfPlainDescriptions(t *testing.T) {
	list := &playlistResult{
		Candidates: []triage.Candidate{languageCandidate("Movie.2024.1080p.WEB-DL.x264-GRP", "Arabic")},
	}
	key := StreamSlotKey{StreamID: "Standalone", ContentType: "movie", ID: "tt1"}
	streams := buildStreamsFromPlaylist(list, key, "Standalone", DefaultServiceName, "http://host", true, nil)
	if len(streams) != 1 {
		t.Fatalf("got %d streams, want 1", len(streams))
	}
	if strings.Contains(streams[0].Description, "🇸🇦") {
		t.Errorf("plain description grew a flag: %q", streams[0].Description)
	}
	if got := streams[0].Languages; len(got) != 1 || got[0] != "ar" {
		t.Errorf("Languages = %v, want [ar]", got)
	}
}

// A single-stream list (results mode other than "display all") is the same
// answer for the release that plays first.
func TestBuildStreamsSurfacesLanguagesOnCollapsedList(t *testing.T) {
	list := &playlistResult{
		IsAIOStreams: true,
		Candidates: []triage.Candidate{
			languageCandidate("Movie.2024.1080p.WEB-DL.x264-GRP", "Arabic"),
			languageCandidate("Movie.2024.720p.WEB-DL.x264-GRP", "Arabic"),
		},
	}
	key := StreamSlotKey{StreamID: "Standalone", ContentType: "movie", ID: "tt1"}
	streams := buildStreamsFromPlaylist(list, key, "Standalone", DefaultServiceName, "http://host", false, nil)
	if len(streams) == 0 {
		t.Fatal("no streams built")
	}
	if got := streams[0].Languages; len(got) != 1 || got[0] != "ar" {
		t.Errorf("Languages = %v, want [ar]", got)
	}
	if !strings.Contains(streams[0].Description, "🇸🇦") {
		t.Errorf("description carries no Arabic flag: %q", streams[0].Description)
	}
}

func TestReleaseLanguageCodesMergesBothSources(t *testing.T) {
	cand := languageCandidate("Movie.2024.1080p.WEB-DL.FRENCH.x264-GRP", "Arabic")
	got := releaseLanguageCodes(cand.Release, cand.Metadata)
	if len(got) != 2 || got[0] != "fr" || got[1] != "ar" {
		t.Errorf("codes = %v, want [fr ar]", got)
	}
}

// A flag that resolves to the wrong language is worse than no flag: 🇮🇳 reads
// back as Hindi and 🇹🇷 as Kurdish, so those languages ship as codes only.
func TestLanguageFlagsSkipAmbiguousCodes(t *testing.T) {
	got := languageFlags([]string{"ta", "tr", "fa", "ar", "en", "en"})
	if len(got) != 2 || got[0] != "🇸🇦" || got[1] != "🇬🇧" {
		t.Errorf("flags = %v, want [🇸🇦 🇬🇧]", got)
	}
	if line := languageFlagLine([]string{"ta"}); line != "" {
		t.Errorf("line = %q, want empty", line)
	}
}
