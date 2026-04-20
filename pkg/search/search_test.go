package search

import (
	"context"
	"fmt"
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

func (r *recordingIndexer) Search(req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
	return &indexer.SearchResponse{}, nil
}

func (r *recordingIndexer) DownloadNZB(context.Context, string) ([]byte, error) { return nil, nil }
func (r *recordingIndexer) Ping() error                                         { return nil }
func (r *recordingIndexer) Name() string                                        { return r.name }
func (r *recordingIndexer) GetUsage() indexer.Usage                             { return indexer.Usage{} }

type staticIndexer struct {
	name string
	resp *indexer.SearchResponse
}

func (s *staticIndexer) Search(req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	return s.resp, nil
}

func (s *staticIndexer) DownloadNZB(context.Context, string) ([]byte, error) { return nil, nil }
func (s *staticIndexer) Ping() error                                         { return nil }
func (s *staticIndexer) Name() string                                        { return s.name }
func (s *staticIndexer) GetUsage() indexer.Usage                             { return indexer.Usage{} }

type errIndexer struct {
	name string
	err  error
}

func (e *errIndexer) Search(req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	return nil, e.err
}

func (e *errIndexer) DownloadNZB(context.Context, string) ([]byte, error) { return nil, nil }
func (e *errIndexer) Ping() error                                         { return nil }
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

	if _, err := RunIndexerSearches(idx, req, "series"); err != nil {
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

	got, err := RunIndexerSearches(idx, req, "movie")
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

	if _, err := RunIndexerSearches(idx, req, "movie"); err != nil {
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

	if _, err := RunIndexerSearches(idx, req, "movie"); err != nil {
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

	_, err := RunIndexerSearches(idx, req, "series")
	if err == nil {
		t.Fatalf("expected text search error, got nil")
	}
	if got := err.Error(); got == "" || !strings.Contains(got, "text search failed") {
		t.Fatalf("expected wrapped text search error, got %q", got)
	}
}

func TestRunIndexerSearchesSkipsWithoutValidationBasis(t *testing.T) {
	idx := &recordingIndexer{name: "TestIndexer"}
	req := indexer.SearchRequest{
		SearchMode: "id",
		Cat:        "2000",
		IMDbID:     "tt1655441",
	}

	got, err := RunIndexerSearches(idx, req, "movie")
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
