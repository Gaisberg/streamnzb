package search

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"streamnzb/pkg/indexer"
	"streamnzb/pkg/release"
)

type recordingIndexer struct {
	mu   sync.Mutex
	name string
	reqs []indexer.SearchRequest
}

func (r *recordingIndexer) Search(ctx context.Context, req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
	return &indexer.SearchResponse{}, nil
}

func (r *recordingIndexer) DownloadNZB(context.Context, string) ([]byte, error) { return nil, nil }
func (r *recordingIndexer) Ping(context.Context) error                          { return nil }
func (r *recordingIndexer) Name() string                                        { return r.name }
func (r *recordingIndexer) GetUsage() indexer.Usage                             { return indexer.Usage{} }

type staticIndexer struct {
	name string
	resp *indexer.SearchResponse
}

func (s *staticIndexer) Search(ctx context.Context, req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	return s.resp, nil
}

func (s *staticIndexer) DownloadNZB(context.Context, string) ([]byte, error) { return nil, nil }
func (s *staticIndexer) Ping(context.Context) error                          { return nil }
func (s *staticIndexer) Name() string                                        { return s.name }
func (s *staticIndexer) GetUsage() indexer.Usage                             { return indexer.Usage{} }

type errIndexer struct {
	name string
	err  error
}

func (e *errIndexer) Search(ctx context.Context, req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	return nil, e.err
}

func (e *errIndexer) DownloadNZB(context.Context, string) ([]byte, error) { return nil, nil }
func (e *errIndexer) Ping(context.Context) error                          { return nil }
func (e *errIndexer) Name() string                                        { return e.name }
func (e *errIndexer) GetUsage() indexer.Usage                             { return indexer.Usage{} }

func TestRunIndexerSearchesTextRequestCarriesSeasonEpisodeWhenEnabled(t *testing.T) {
	idx := &recordingIndexer{name: "TestIndexer"}
	req := indexer.SearchRequest{
		Cat:             "5000",
		Limit:           100,
		IMDbID:          "tt1234567",
		Season:          "1",
		Episode:         "5",
		Query:           "The Walking Dead",
		ValidationQuery: "The Walking Dead",
	}

	if _, err := RunIndexerSearches(context.Background(), idx, req, "series"); err != nil {
		t.Fatalf("RunIndexerSearches() error = %v", err)
	}

	if len(idx.reqs) != 1 {
		t.Fatalf("expected 1 Search call, got %d", len(idx.reqs))
	}

	textReq := &idx.reqs[0]
	if textReq.Season != "1" || textReq.Episode != "5" {
		t.Fatalf("expected text request to keep season/episode when enabled, got season=%q episode=%q", textReq.Season, textReq.Episode)
	}
	if textReq.Query != "The Walking Dead" {
		t.Fatalf("expected text query to be preserved, got %q", textReq.Query)
	}
}

func TestRunIndexerSearchesAlwaysAppliesValidation(t *testing.T) {
	idx := &staticIndexer{
		name: "SceneNZBs",
		resp: &indexer.SearchResponse{
			Channel: indexer.Channel{
				Items: []indexer.Item{
					{Title: "Der.Patriot.2000.German.DL.1080p.BluRay.x264.iNTERNAL-VideoStar", ActualIndexer: "SceneNZBs"},
				},
			},
		},
	}
	req := indexer.SearchRequest{
		Cat:                  "2100",
		IMDbID:               "tt0187393",
		TMDBID:               "2024",
		Query:                "Der Patriot 2000",
		ValidationQuery:      "The Patriot 2000",
		EnableYearValidation: true,
	}

	got, err := RunIndexerSearches(context.Background(), idx, req, "movie")
	if err != nil {
		t.Fatalf("RunIndexerSearches() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected validation to remove mismatched title, got %d: %+v", len(got), got)
	}
}

func TestValidateSearchResultsWithStatsCountsSeriesEpisodeMatches(t *testing.T) {
	releases := []*release.Release{
		{Title: "Right.Show.S01E02.1080p.WEB-DL.x264-GROUP"},
		{Title: "Right.Show.S01E02E03.1080p.WEB-DL.x264-GROUP"},
		{Title: "Right.Show.S01.COMPLETE.1080p.WEB-DL.x264-GROUP"},
		{Title: "Right.Show.COMPLETE.1080p.WEB-DL.x264-GROUP"},
		{Title: "Right.Show.S01E04.1080p.WEB-DL.x264-GROUP"},
	}

	filtered, stats := ValidateSearchResultsWithStats(releases, "series", "Right Show S01E02", "1", "2", true, false)

	if len(filtered) != 4 {
		t.Fatalf("expected 4 accepted results, got %d", len(filtered))
	}
	if stats.RawResults != 5 || stats.FinalResults != 4 || stats.RejectedResults != 1 {
		t.Fatalf("unexpected raw/final/rejected counts: %+v", stats)
	}
	if !stats.TitleValidationApplied {
		t.Fatalf("expected title validation to be applied, got %+v", stats)
	}
	if stats.AcceptedExactEpisode != 1 {
		t.Fatalf("expected 1 exact episode match, got %d", stats.AcceptedExactEpisode)
	}
	if stats.AcceptedMultiEpisode != 1 {
		t.Fatalf("expected 1 multi-episode match, got %d", stats.AcceptedMultiEpisode)
	}
	if stats.AcceptedSeasonPack != 1 {
		t.Fatalf("expected 1 season pack match, got %d", stats.AcceptedSeasonPack)
	}
	if stats.AcceptedCompletePack != 1 {
		t.Fatalf("expected 1 complete pack match, got %d", stats.AcceptedCompletePack)
	}
	if stats.DroppedEpisodeRequest != 1 {
		t.Fatalf("expected 1 episode-request rejection, got %d", stats.DroppedEpisodeRequest)
	}
}

// Anime absolute numbering: with an absolute episode set, releases named with
// the absolute number (dash style, S01Exxx style, or bare) must be accepted
// even though their parsed season/episode do not match the TVDB-style request.
// A text query can match an episode *title* — searching the film "Spirited
// Away" returns "Avatar The Last Airbender S01E05 Spirited Away". Movie
// validation must drop those: the parsed release title is the series name.
func TestValidateSearchResultsDropsEpisodeTitleMatches(t *testing.T) {
	releases := []*release.Release{
		{Title: "Spirited.Away.2001.JP.Hybrid.BD.Remux.1080p.AVC.DTS-HD.MA.2.0-RedDeadProject"},
		{Title: "Avatar.The.Last.Airbender.2024.S01E05.Spirited.Away.2160p.NF.WEB-DL.DDP5.1.Atmos.H.265-Kitsune"},
		{Title: "Avatar The Last Airbender 2024 S01E05 Spirited Away 2160p NF WEB-DL DDP5 1 Atmos H 265-FLUX"},
	}

	queries := []string{"Spirited Away 2001", "Spirited Away", "Sen to Chihiro no Kamikakushi"}
	filtered, stats := ValidateSearchResultsWithStatsForQueries(releases, "movie", queries, "", "", "", true, true)

	if len(filtered) != 1 || filtered[0] != releases[0] {
		t.Fatalf("expected only the film to survive, got %d: %+v", len(filtered), stats)
	}
	if stats.DroppedTitle != 2 {
		t.Fatalf("expected 2 title rejections, got %+v", stats)
	}
}

// jhin's similarity fallback rescues titles the word-block matcher cannot
// line up, while staying strict enough to keep rejecting different titles.
func TestTitleValidationSimilarityFallback(t *testing.T) {
	releases := []*release.Release{
		{Title: "Spirited.Awey.2001.1080p.BluRay.x264-GROUP"},  // near-miss spelling
		{Title: "Spirit.Untamed.2021.1080p.BluRay.x264-GROUP"}, // different film
	}

	filtered, stats := ValidateSearchResultsWithStatsForQueries(releases, "movie", []string{"Spirited Away 2001"}, "", "", "", true, false)

	if len(filtered) != 1 || filtered[0] != releases[0] {
		t.Fatalf("expected the near-miss kept and the different film dropped, got %d: %+v", len(filtered), stats)
	}
}

// The similarity fallback must never bridge numbering: numbered sequels are
// one edit apart yet different films, so digit runs gate the ratio.
func TestSimilarTitleMatchesGuardsNumbering(t *testing.T) {
	if similarTitleMatches("The Hunger Games Mockingjay Part 1", "The Hunger Games Mockingjay Part 2") {
		t.Fatal("numbered sequel must not match via similarity")
	}
	if !similarTitleMatches("Evangelion: 3.0+1.0 Thrice Upon a Time", "Evangelion 3.0 + 1.0 Thrice Upon A Time") {
		t.Fatal("punctuation variant with agreeing numbers must match")
	}
}

func TestValidateSearchResultsAcceptsAbsoluteNumberedAnime(t *testing.T) {
	releases := []*release.Release{
		{Title: "One.Piece.S02E02.1080p.WEB-DL.x264-GROUP"},     // standard match
		{Title: "[Judas] One Piece - 63 (1080p) [ABCD1234]"},    // absolute, dash style
		{Title: "One.Piece.S01E63.1080p.WEB-DL.AAC2.0-HatSubs"}, // absolute, S01Exxx style
		{Title: "[Judas] One Piece - 62 (1080p) [ABCD1234]"},    // wrong absolute episode
		{Title: "One.Piece.S02E05.1080p.WEB-DL.x264-GROUP"},     // wrong episode
	}

	filtered, stats := ValidateSearchResultsWithStatsForQueries(releases, "series", []string{"One Piece"}, "2", "2", "63", true, false)

	if len(filtered) != 3 {
		t.Fatalf("expected 3 accepted results, got %d: %+v", len(filtered), stats)
	}
	if stats.DroppedEpisodeRequest != 2 {
		t.Fatalf("expected 2 episode-request rejections, got %d", stats.DroppedEpisodeRequest)
	}
	if stats.AcceptedExactEpisode != 3 {
		t.Fatalf("expected 3 exact episode matches, got %d", stats.AcceptedExactEpisode)
	}

	// Without the absolute number the same absolute-styled releases must
	// still be dropped — the widening only applies when it is known.
	filtered, _ = ValidateSearchResultsWithStatsForQueries(releases, "series", []string{"One Piece"}, "2", "2", "", true, false)
	if len(filtered) != 1 {
		t.Fatalf("expected only the standard release without absolute, got %d", len(filtered))
	}
}

func TestValidateSearchResultsWithStatsCountsMovieTitleAndYearDrops(t *testing.T) {
	releases := []*release.Release{
		{Title: "The.Patriot.2000.1080p.BluRay.x264-GROUP"},
		{Title: "The.Patriot.2004.1080p.BluRay.x264-GROUP"},
		{Title: "Der.Patriot.2000.1080p.BluRay.x264-GROUP"},
	}

	filtered, stats := ValidateSearchResultsWithStats(releases, "movie", "The Patriot 2000", "", "", true, true)

	if len(filtered) != 1 {
		t.Fatalf("expected 1 accepted movie result, got %d", len(filtered))
	}
	if !stats.TitleValidationApplied || !stats.YearValidationApplied {
		t.Fatalf("expected title and year validation to be applied, got %+v", stats)
	}
	if stats.DroppedYear != 1 {
		t.Fatalf("expected 1 year rejection, got %d", stats.DroppedYear)
	}
	if stats.DroppedTitle != 1 {
		t.Fatalf("expected 1 title rejection, got %d", stats.DroppedTitle)
	}
}

func TestValidateSearchResultsWithStatsSkipsYearWhenDisabled(t *testing.T) {
	releases := []*release.Release{
		{Title: "The.Patriot.2000.1080p.BluRay.x264-GROUP"},
		{Title: "The.Patriot.2004.1080p.BluRay.x264-GROUP"},
	}

	filtered, stats := ValidateSearchResultsWithStats(releases, "movie", "The Patriot 2000", "", "", true, false)

	if len(filtered) != 2 {
		t.Fatalf("expected both results to remain when year validation is off, got %d", len(filtered))
	}
	if stats.YearValidationApplied {
		t.Fatalf("expected year validation to stay off, got %+v", stats)
	}
	if stats.DroppedYear != 0 {
		t.Fatalf("expected no year rejections, got %d", stats.DroppedYear)
	}
}

func TestValidateSearchResultsWithStatsAllowsOptionalAndWordMatches(t *testing.T) {
	releases := []*release.Release{
		{Title: "Your.Friends.Neighbors.S01E01.1080p.WEB-DL.x264-GROUP"},
	}

	filtered, stats := ValidateSearchResultsWithStats(releases, "series", "Your Friends & Neighbors", "1", "1", true, false)

	if len(filtered) != 1 {
		t.Fatalf("expected optional '&/and' title match to pass, got %d results", len(filtered))
	}
	if stats.DroppedTitle != 0 {
		t.Fatalf("expected no title rejection, got %+v", stats)
	}
	if stats.AcceptedExactEpisode != 1 {
		t.Fatalf("expected exact episode match, got %+v", stats)
	}
}

func TestValidateSearchResultsWithStatsForQueriesMatchesAnyExpectedTitle(t *testing.T) {
	releases := []*release.Release{
		{Title: "Koenig.der.Loewen.1994.1080p.BluRay.x264-GROUP"},
	}

	filtered, stats := ValidateSearchResultsWithStatsForQueries(releases, "movie", []string{"The Lion King 1994", "Koenig der Loewen 1994"}, "", "", "", true, true)

	if len(filtered) != 1 {
		t.Fatalf("expected multilingual validation to accept a matching alternate title, got %d results", len(filtered))
	}
	if stats.DroppedTitle != 0 || stats.DroppedYear != 0 {
		t.Fatalf("expected no validation drops, got %+v", stats)
	}
}

func TestValidateSearchResultsWithStatsAllowsDashedSeasonEpisodePattern(t *testing.T) {
	releases := []*release.Release{
		{Title: "[SubsPlease] Tensei Shitara Slime Datta Ken S4 - 03 (720p) [370B1C65]"},
	}

	filtered, stats := ValidateSearchResultsWithStats(releases, "series", "Tensei shitara Slime Datta Ken", "4", "3", true, false)

	if len(filtered) != 1 {
		t.Fatalf("expected dashed season/episode title to pass, got %d results", len(filtered))
	}
	if stats.DroppedEpisodeRequest != 0 || stats.DroppedTitle != 0 {
		t.Fatalf("expected no rejection for dashed season/episode pattern, got %+v", stats)
	}
	if stats.AcceptedExactEpisode != 1 {
		t.Fatalf("expected exact episode match, got %+v", stats)
	}
}

func TestValidateSearchResultsWithStatsRejectsExtraTrailingTitleWords(t *testing.T) {
	releases := []*release.Release{
		{Title: "The.Rookie.Feds.S01E01.1080p.WEB-DL.x264-GROUP"},
	}

	filtered, stats := ValidateSearchResultsWithStats(releases, "series", "The Rookie", "1", "1", true, false)

	if len(filtered) != 0 {
		t.Fatalf("expected trailing title words to be rejected, got %d results", len(filtered))
	}
	if stats.DroppedTitle != 1 {
		t.Fatalf("expected title rejection for extra trailing words, got %+v", stats)
	}
}

func TestRunIndexerSearchesQueryWithIDsDoesNotAlsoRunIDSearch(t *testing.T) {
	idx := &recordingIndexer{name: "TestIndexer"}
	req := indexer.SearchRequest{
		Query:           "Meal Ticket 2026",
		Cat:             "2000",
		Limit:           100,
		IMDbID:          "tt40232255",
		TMDBID:          "1649758",
		ValidationQuery: "Meal Ticket 2026",
	}

	if _, err := RunIndexerSearches(context.Background(), idx, req, "movie"); err != nil {
		t.Fatalf("RunIndexerSearches() error = %v", err)
	}

	if len(idx.reqs) != 1 {
		t.Fatalf("expected exactly 1 Search call, got %d", len(idx.reqs))
	}
	if idx.reqs[0].SearchMode != "text" {
		t.Fatalf("expected query request to remain text-only, got mode %q", idx.reqs[0].SearchMode)
	}
	if idx.reqs[0].Query != "Meal Ticket 2026" {
		t.Fatalf("expected text query to be preserved, got %q", idx.reqs[0].Query)
	}
}

func TestRunIndexerSearchesIDModePreservesPreparedQuery(t *testing.T) {
	idx := &recordingIndexer{name: "TestIndexer"}
	req := indexer.SearchRequest{
		Cat:             "2000",
		Limit:           100,
		SearchMode:      "id",
		IMDbID:          "tt1655441",
		TMDBID:          "1655441",
		Query:           "The Age of Adaline",
		ValidationQuery: "The Age of Adaline",
	}

	if _, err := RunIndexerSearches(context.Background(), idx, req, "movie"); err != nil {
		t.Fatalf("RunIndexerSearches() error = %v", err)
	}

	if len(idx.reqs) != 1 {
		t.Fatalf("expected exactly 1 Search call, got %d", len(idx.reqs))
	}
	if idx.reqs[0].SearchMode != "id" {
		t.Fatalf("expected id mode to stay id-only, got mode %q", idx.reqs[0].SearchMode)
	}
	if idx.reqs[0].Query != "The Age of Adaline" {
		t.Fatalf("expected id request to preserve prepared query, got %q", idx.reqs[0].Query)
	}
}

func TestRunIndexerSearchesReturnsTextSearchErrors(t *testing.T) {
	idx := &errIndexer{name: "BrokenIndexer", err: fmt.Errorf("backend unavailable")}
	req := indexer.SearchRequest{
		SearchMode:      "text",
		Query:           "The King Who Never Was",
		ValidationQuery: "The King Who Never Was",
		StreamLabel:     "TestStream",
		RequestLabel:    "Text Request",
	}

	_, err := RunIndexerSearches(context.Background(), idx, req, "series")
	if err == nil {
		t.Fatalf("expected text search error, got nil")
	}
	if got := err.Error(); got == "" || !strings.Contains(got, "text search failed") {
		t.Fatalf("expected wrapped text search error, got %q", got)
	}
}

// A text request without a title has nothing to search for.
func TestRunIndexerSearchesSkipsTextRequestWithoutValidationBasis(t *testing.T) {
	idx := &recordingIndexer{name: "TestIndexer"}
	req := indexer.SearchRequest{
		SearchMode: "text",
		Cat:        "2000",
		IMDbID:     "tt1655441",
	}

	got, err := RunIndexerSearches(context.Background(), idx, req, "movie")
	if err != nil {
		t.Fatalf("RunIndexerSearches() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no releases without validation basis, got %d", len(got))
	}
	if len(idx.reqs) != 0 {
		t.Fatalf("expected no Search call without validation basis, got %d", len(idx.reqs))
	}
}

// An ID request needs an id, not a title. Metadata that resolved an id but no
// usable title used to sit the whole request out, which spent the one search
// that did not need the title at all.
func TestRunIndexerSearchesRunsIDRequestWithoutValidationBasis(t *testing.T) {
	idx := &recordingIndexer{name: "TestIndexer"}
	req := indexer.SearchRequest{
		SearchMode: "id",
		Cat:        "2000",
		IMDbID:     "tt1655441",
	}

	if _, err := RunIndexerSearches(context.Background(), idx, req, "movie"); err != nil {
		t.Fatalf("RunIndexerSearches() error = %v", err)
	}
	if len(idx.reqs) != 1 {
		t.Fatalf("expected the id search to run, got %d Search calls", len(idx.reqs))
	}
	if idx.reqs[0].SearchMode != "id" {
		t.Fatalf("expected an id request, got mode %q", idx.reqs[0].SearchMode)
	}
}

// An ID request trusts the indexer: it asked by imdbid/tvdbid, so a release
// whose scene name diverges from the metadata title is kept, and counted.
func TestRunIndexerSearchesIDRequestKeepsTitleMismatches(t *testing.T) {
	idx := &staticIndexer{
		name: "SceneNZBs",
		resp: &indexer.SearchResponse{
			Channel: indexer.Channel{
				Items: []indexer.Item{
					{Title: "Special.Ops.Lioness.S02E01.1080p.WEB.h264-GROUP", ActualIndexer: "SceneNZBs"},
				},
			},
		},
	}
	req := indexer.SearchRequest{
		SearchMode:      "id",
		Cat:             "5000",
		TVDBID:          "420299",
		Season:          "2",
		Episode:         "1",
		ValidationQuery: "Lioness",
	}

	got, err := RunIndexerSearches(context.Background(), idx, req, "series")
	if err != nil {
		t.Fatalf("RunIndexerSearches() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected the id result to survive validation, got %d", len(got))
	}
}

// The same release under a text request: the title is enforced there, but the
// season/episode backs it up, so a prefix the metadata title dropped is fine.
func TestValidateSearchResultsAcceptsPrefixedSceneTitleForEpisodeRequest(t *testing.T) {
	releases := []*release.Release{
		{Title: "Special.Ops.Lioness.S02E01.1080p.WEB.h264-GROUP"},
		{Title: "Some.Other.Show.S02E01.1080p.WEB.h264-GROUP"},
	}

	filtered, stats := ValidateSearchResultsWithStats(releases, "series", "Lioness", "2", "1", true, false)

	if len(filtered) != 1 || filtered[0] != releases[0] {
		t.Fatalf("expected the prefixed scene title kept and the other show dropped, got %d: %+v", len(filtered), stats)
	}
	if stats.DroppedTitle != 1 {
		t.Fatalf("expected 1 title rejection, got %+v", stats)
	}
}

// Not enforcing the title keeps the mismatch, and counts it: an aggregator
// that turned an unsupported id search into a title search shows up as a
// request that is nearly all mismatch, not as a request that looked fine.
func TestValidateSearchResultsCountsTitleMismatchWhenNotEnforced(t *testing.T) {
	releases := []*release.Release{
		{Title: "The.Patriot.2000.1080p.BluRay.x264-GROUP"},
		{Title: "Something.Else.Entirely.2000.1080p.BluRay.x264-GROUP"},
	}

	filtered, stats := ValidateSearchResultsWithStats(releases, "movie", "The Patriot 2000", "", "", false, true)

	if len(filtered) != 2 {
		t.Fatalf("expected both results kept when the title is not enforced, got %d", len(filtered))
	}
	if stats.TitleValidationApplied {
		t.Fatalf("expected title validation not to be enforced, got %+v", stats)
	}
	if stats.DroppedTitle != 0 {
		t.Fatalf("expected no title drops, got %+v", stats)
	}
	if stats.TitleMismatchKept != 1 {
		t.Fatalf("expected 1 counted title mismatch, got %+v", stats)
	}
}

func TestRunIndexerSearchesUsesValidationQueriesWhenPresent(t *testing.T) {
	idx := &staticIndexer{
		name: "SceneNZBs",
		resp: &indexer.SearchResponse{
			Channel: indexer.Channel{
				Items: []indexer.Item{
					{Title: "Koenig.der.Loewen.1994.1080p.BluRay.x264-GROUP", ActualIndexer: "SceneNZBs"},
				},
			},
		},
	}
	req := indexer.SearchRequest{
		SearchMode:           "id",
		IMDbID:               "tt0110357",
		TMDBID:               "8587",
		ValidationQueries:    []string{"The Lion King 1994", "Koenig der Loewen 1994"},
		EnableYearValidation: true,
	}

	got, err := RunIndexerSearches(context.Background(), idx, req, "movie")
	if err != nil {
		t.Fatalf("RunIndexerSearches() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected multilingual validation queries to keep the result, got %d", len(got))
	}
}

func TestValidationQueriesForRequestUsesProfiles(t *testing.T) {
	req := indexer.SearchRequest{
		ValidationQueryProfiles: []indexer.ValidationQueryProfile{
			{Languages: []string{"en-US"}, Query: "Witch Hat Atelier"},
			{Languages: []string{"original"}, Query: "Tongari Boushi no Atelier"},
		},
	}

	got := validationQueriesForRequest(req)
	want := []string{"Witch Hat Atelier", "Tongari Boushi no Atelier"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("validationQueriesForRequest() = %#v, want %#v", got, want)
	}
}

func TestValidationProfilesForRequestPreferExplicitProfiles(t *testing.T) {
	req := indexer.SearchRequest{
		ValidationQueryProfiles: []indexer.ValidationQueryProfile{
			{Languages: []string{"en-US"}, Query: "Witch Hat Atelier"},
			{Languages: []string{"original"}, Query: "Tongari Boushi no Atelier"},
		},
		ValidationQueries: []string{"ignored"},
	}

	got := validationProfilesForRequest(req)
	want := []indexer.ValidationQueryProfile{
		{Languages: []string{"en-US"}, Query: "Witch Hat Atelier"},
		{Languages: []string{"original"}, Query: "Tongari Boushi no Atelier"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("validationProfilesForRequest() = %#v, want %#v", got, want)
	}
}
