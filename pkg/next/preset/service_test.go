package preset

import (
	"context"
	"strconv"
	"testing"

	"streamnzb/pkg/indexer"
	"streamnzb/pkg/release"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/services/metadata/tmdb"
)

type testIndexer struct {
	responses []*indexer.SearchResponse
	reqs      []indexer.SearchRequest
}

func (t *testIndexer) Search(req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	t.reqs = append(t.reqs, req)
	if len(t.responses) == 0 {
		return &indexer.SearchResponse{}, nil
	}
	resp := t.responses[0]
	t.responses = t.responses[1:]
	return resp, nil
}

func (t *testIndexer) DownloadNZB(context.Context, string) ([]byte, error) { return nil, nil }
func (t *testIndexer) Ping() error                                         { return nil }
func (t *testIndexer) Name() string                                        { return "test" }
func (t *testIndexer) GetUsage() indexer.Usage                             { return indexer.Usage{} }

type testAvailClient struct {
	result    *availnzb.ReleasesResult
	imdbID    string
	tvdbID    string
	season    int
	episode   int
	indexers  []string
	providers []string
}

func (t *testAvailClient) GetReleases(imdbID string, tvdbID string, season, episode int, indexers []string, providers []string) (*availnzb.ReleasesResult, error) {
	t.imdbID = imdbID
	t.tvdbID = tvdbID
	t.season = season
	t.episode = episode
	t.indexers = append([]string(nil), indexers...)
	t.providers = append([]string(nil), providers...)
	return t.result, nil
}

type testValidator struct {
	hosts []string
}

func (t *testValidator) GetProviderHosts() []string {
	return append([]string(nil), t.hosts...)
}

type testTVDBClient struct {
	resolved string
	remoteID string
}

func (t *testTVDBClient) ResolveTVDBID(remoteID string) (string, error) {
	t.remoteID = remoteID
	return t.resolved, nil
}

type testTMDBClient struct {
	externalIDs map[string]*tmdb.ExternalIDsResponse
	resolved    string
	remoteID    string
}

func (t *testTMDBClient) GetMovieTitle(string, string) (string, error) {
	return "", nil
}

func (t *testTMDBClient) GetMovieTitleAndYear(string, string) (string, string, error) {
	return "", "", nil
}

func (t *testTMDBClient) GetMovieTitleForSearch(string, string, string, bool, bool) (string, error) {
	return "", nil
}

func (t *testTMDBClient) GetTVShowName(string, string) (string, error) {
	return "", nil
}

func (t *testTMDBClient) GetExternalIDs(tmdbID int, mediaType string) (*tmdb.ExternalIDsResponse, error) {
	if t.externalIDs == nil {
		return nil, nil
	}
	return t.externalIDs[mediaType+":"+strconv.Itoa(tmdbID)], nil
}

func (t *testTMDBClient) ResolveTVDBID(remoteID string) (string, error) {
	t.remoteID = remoteID
	return t.resolved, nil
}

func searchResponse(items ...indexer.Item) *indexer.SearchResponse {
	return &indexer.SearchResponse{Channel: indexer.Channel{Items: items}}
}

func TestMatchResolvesTMDBExternalIDsForSeries(t *testing.T) {
	idx := &testIndexer{}
	avail := &testAvailClient{result: &availnzb.ReleasesResult{}}
	tmdbClient := &testTMDBClient{externalIDs: map[string]*tmdb.ExternalIDsResponse{
		"tv:123": {IMDbID: "tt7654321", TVDBID: 9999},
	}}

	svc := NewServiceWithOptions(Options{
		AvailNZBMode: "status_only",
		Indexer:      idx,
		AvailClient:  avail,
		TMDBClient:   tmdbClient,
	})

	resp, err := svc.Match(context.Background(), MatchRequest{Type: "series", MetadataID: "tmdb:123:1:2"})
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("expected status ok, got %q", resp.Status)
	}
	if len(idx.reqs) != 1 {
		t.Fatalf("expected one indexer request, got %d", len(idx.reqs))
	}
	if idx.reqs[0].TMDBID != "123" || idx.reqs[0].IMDbID != "tt7654321" || idx.reqs[0].TVDBID != "9999" {
		t.Fatalf("expected TMDB external ids to populate request, got %#v", idx.reqs[0])
	}
	if idx.reqs[0].Season != "1" || idx.reqs[0].Episode != "2" {
		t.Fatalf("expected season/episode 1/2, got season=%q episode=%q", idx.reqs[0].Season, idx.reqs[0].Episode)
	}
	if avail.imdbID != "tt7654321" || avail.tvdbID != "9999" || avail.season != 1 || avail.episode != 2 {
		t.Fatalf("expected availability lookup to include resolved content ids, got imdb=%q tvdb=%q season=%d episode=%d", avail.imdbID, avail.tvdbID, avail.season, avail.episode)
	}
}

func TestMatchMergesAvailabilityAndIndexerResults(t *testing.T) {
	idx := &testIndexer{responses: []*indexer.SearchResponse{searchResponse(
		indexer.Item{Title: "Movie Available", Link: "https://idx/download/1", Comments: "https://idx/details/available", Size: 1, ActualIndexer: "IndexerA"},
		indexer.Item{Title: "Movie Unknown", Link: "https://idx/download/2", Comments: "https://idx/details/unknown", Size: 2, ActualIndexer: "IndexerA"},
	)}}
	avail := &testAvailClient{result: &availnzb.ReleasesResult{Releases: []*availnzb.ReleaseWithStatus{
		{Release: &release.Release{Title: "Movie Available", DetailsURL: "https://idx/details/available", Link: "https://avail/download/1", Indexer: "AvailNZB"}, Available: true},
		{Release: &release.Release{Title: "Movie Unavailable", DetailsURL: "https://idx/details/unavailable", Link: "https://avail/download/2", Indexer: "AvailNZB"}, Available: false},
	}}}

	svc := NewServiceWithOptions(Options{
		AvailNZBMode:         "status_only",
		Indexer:              idx,
		AvailClient:          avail,
		AvailNZBIndexerHosts: []string{"indexer-a"},
		Validator:            &testValidator{hosts: []string{"provider-a"}},
	})

	resp, err := svc.Match(context.Background(), MatchRequest{Type: "movie", MetadataID: "tt1234567"})
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("expected status ok, got %q", resp.Status)
	}
	if len(resp.Candidates) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(resp.Candidates))
	}
	if resp.Candidates[0].Availability != "Available" || resp.Candidates[0].QuerySource != "availability" {
		t.Fatalf("expected first candidate to be available from availability source, got %#v", resp.Candidates[0])
	}
	if resp.Candidates[1].Availability != "Unavailable" {
		t.Fatalf("expected second candidate unavailable, got %#v", resp.Candidates[1])
	}
	if resp.Candidates[2].Availability != "Unknown" || resp.Candidates[2].DetailsURL != "https://idx/details/unknown" {
		t.Fatalf("expected final candidate to remain unknown, got %#v", resp.Candidates[2])
	}
	if avail.imdbID != "tt1234567" {
		t.Fatalf("expected avail lookup imdb id tt1234567, got %q", avail.imdbID)
	}
	if len(avail.providers) != 1 || avail.providers[0] != "provider-a" {
		t.Fatalf("expected provider hosts to be forwarded, got %#v", avail.providers)
	}
	if len(avail.indexers) != 1 || avail.indexers[0] != "indexer-a" {
		t.Fatalf("expected avail indexers to be forwarded, got %#v", avail.indexers)
	}
}

func TestMatchBuildsSeriesSearchAndAvailabilityIDs(t *testing.T) {
	idx := &testIndexer{responses: []*indexer.SearchResponse{{}}}
	avail := &testAvailClient{}
	tvdb := &testTVDBClient{resolved: "9999"}

	svc := NewServiceWithOptions(Options{
		AvailNZBMode: "status_only",
		Indexer:      idx,
		AvailClient:  avail,
		TVDBClient:   tvdb,
	})

	resp, err := svc.Match(context.Background(), MatchRequest{Type: "series", MetadataID: "tt7654321:1:2"})
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("expected status ok, got %q", resp.Status)
	}
	if len(idx.reqs) != 1 {
		t.Fatalf("expected one indexer request, got %d", len(idx.reqs))
	}
	req := idx.reqs[0]
	if req.IMDbID != "tt7654321" || req.TVDBID != "9999" {
		t.Fatalf("expected imdb/tvdb ids to be populated, got %#v", req)
	}
	if req.Season != "1" || req.Episode != "2" {
		t.Fatalf("expected season/episode 1/2, got season=%q episode=%q", req.Season, req.Episode)
	}
	if req.Cat != "5000" {
		t.Fatalf("expected tv category 5000, got %q", req.Cat)
	}
	if avail.imdbID != "tt7654321" || avail.tvdbID != "9999" || avail.season != 1 || avail.episode != 2 {
		t.Fatalf("expected avail lookup to receive resolved ids and episode info, got imdb=%q tvdb=%q season=%d episode=%d", avail.imdbID, avail.tvdbID, avail.season, avail.episode)
	}
	if tvdb.remoteID != "tt7654321" {
		t.Fatalf("expected TVDB resolver to receive imdb id, got %q", tvdb.remoteID)
	}
}
