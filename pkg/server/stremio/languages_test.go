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

// The other half of issue #283: the reporter's filter was on subtitles, not
// audio. AIOStreams reads a stream's subtitle languages from nothing but its
// parsedMediaInfo block, which its own Newznab integration fills from the
// feed's subs attribute, so a release tagged with Arabic subtitles has to
// ship the same block.
func TestBuildStreamsSurfacesSubtitlesInParsedMediaInfo(t *testing.T) {
	cand := languageCandidate("Movie.2024.1080p.WEB-DL.x264-GRP")
	cand.Release.Subtitles = []string{"ar"}
	list := &playlistResult{IsAIOStreams: true, Candidates: []triage.Candidate{cand}}
	key := StreamSlotKey{StreamID: "Standalone", ContentType: "movie", ID: "tt1"}
	streams := buildStreamsFromPlaylist(list, key, "Standalone", DefaultServiceName, "http://host", true, nil)
	if len(streams) != 1 {
		t.Fatalf("got %d streams, want 1", len(streams))
	}
	info := streams[0].MediaInfo
	if info == nil {
		t.Fatal("stream carries no parsedMediaInfo")
	}
	if info.Quality != "indexer" {
		t.Errorf("mediaInfoQuality = %q, want indexer", info.Quality)
	}
	if len(info.Subtitles) != 1 || info.Subtitles[0] != "ar" {
		t.Errorf("subtitles = %v, want [ar]", info.Subtitles)
	}
	if len(info.Languages) != 0 {
		t.Errorf("languages = %v, want none for an untagged, unlabelled release", info.Languages)
	}

	// Nothing known, nothing sent: an empty block would read as "no
	// subtitles" rather than "unknown".
	bare := &playlistResult{IsAIOStreams: true, Candidates: []triage.Candidate{languageCandidate("Movie.2024.1080p.WEB-DL.x264-GRP")}}
	if got := buildStreamsFromPlaylist(bare, key, "Standalone", DefaultServiceName, "http://host", true, nil); got[0].MediaInfo != nil {
		t.Errorf("bare release carries parsedMediaInfo %+v, want none", got[0].MediaInfo)
	}
}

// The name is a source of subtitle languages too, merged with the tag and
// with what ffprobe measured.
func TestReleaseSubtitleCodesMergesAllSources(t *testing.T) {
	cand := languageCandidate("Movie.2024.1080p.WEB-DL.Arabic.Subs.x264-GRP")
	cand.Release.Subtitles = []string{"French"}
	probed := &release.MediaCaps{TracksProbed: true, SubtitleLanguages: []string{"en", "ar"}}
	got := releaseSubtitleCodes(cand.Release, cand.Metadata, probed)
	if len(got) != 3 || got[0] != "ar" || got[1] != "fr" || got[2] != "en" {
		t.Errorf("codes = %v, want [ar fr en]", got)
	}
	if got := releaseSubtitleCodes(cand.Release, cand.Metadata, &release.MediaCaps{SubtitleLanguages: []string{"en"}}); len(got) != 2 {
		t.Errorf("unprobed caps counted: %v", got)
	}
}
