package cinemeta

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	c := NewClient(ts.Client())
	c.BaseURL = ts.URL
	return c
}

func TestGetMetaMovie(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/meta/movie/tt0133093.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"meta":{
			"imdb_id":"tt0133093","name":"The Matrix","description":"A hacker learns the truth.",
			"poster":"https://images.metahub.space/poster/small/tt0133093/img",
			"background":"https://images.metahub.space/background/medium/tt0133093/img",
			"logo":"https://images.metahub.space/logo/medium/tt0133093/img",
			"releaseInfo":"1999","released":"1999-03-31T00:00:00.000Z",
			"imdbRating":"8.7","runtime":"136 min","genres":["Action","Sci-Fi"],
			"cast":["Keanu Reeves","Laurence Fishburne"],
			"director":["Lana Wachowski","Lilly Wachowski"],
			"writer":["Lilly Wachowski","Lana Wachowski"],
			"trailers":[{"source":"FVI84Dfx2-I","type":"Trailer"}],
			"videos":[]
		}}`))
	})

	meta, err := c.GetMeta(context.Background(), "movie", "tt0133093")
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if meta.Name != "The Matrix" || meta.IMDbID != "tt0133093" {
		t.Fatalf("meta = %+v", meta)
	}
	if meta.Poster == "" || meta.Background == "" || meta.Logo == "" {
		t.Fatalf("artwork missing: %+v", meta)
	}
	if meta.ReleaseInfo != "1999" || meta.IMDBRating != "8.7" || meta.Runtime != "136 min" {
		t.Fatalf("fields = %+v", meta)
	}
	if len(meta.Genres) != 2 || len(meta.Cast) != 2 || len(meta.Director) != 2 || len(meta.Writer) != 2 {
		t.Fatalf("lists = %+v", meta)
	}
	if len(meta.Trailers) != 1 || meta.Trailers[0].Source != "FVI84Dfx2-I" {
		t.Fatalf("trailers = %v", meta.Trailers)
	}
}

func TestGetMetaSeriesVideos(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"meta":{
			"imdb_id":"tt0944947","name":"Game of Thrones","releaseInfo":"2011–2019",
			"imdbRating":"9.2","runtime":"57 min",
			"videos":[
				{"id":"tt0944947:0:1","name":"Inside Game of Thrones","season":0,"number":1,
				 "firstAired":"2010-12-06T05:00:00.000Z","description":"A short look.",
				 "thumbnail":"https://episodes.metahub.space/tt0944947/0/1/w780.jpg"},
				{"id":"tt0944947:1:1","name":"Winter Is Coming","season":1,"episode":1,
				 "released":"2011-04-17T21:00:00.000Z","overview":"Ned Stark is troubled."}
			]
		}}`))
	})

	meta, err := c.GetMeta(context.Background(), "series", "tt0944947")
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if len(meta.Videos) != 2 {
		t.Fatalf("videos = %d, want 2", len(meta.Videos))
	}
	special := meta.Videos[0]
	if special.ID != "tt0944947:0:1" || special.Season != 0 || special.Episode != 1 {
		t.Fatalf("videos[0] = %+v", special)
	}
	if special.Released != "2010-12-06T05:00:00.000Z" {
		t.Fatalf("videos[0] should fall back to firstAired, got %q", special.Released)
	}
	if special.Overview != "A short look." {
		t.Fatalf("videos[0] should fall back to description, got %q", special.Overview)
	}
	ep := meta.Videos[1]
	if ep.ID != "tt0944947:1:1" || ep.Season != 1 || ep.Episode != 1 {
		t.Fatalf("videos[1] = %+v", ep)
	}
	if ep.Released != "2011-04-17T21:00:00.000Z" || ep.Overview != "Ned Stark is troubled." {
		t.Fatalf("videos[1] should prefer released/overview, got %+v", ep)
	}
}

func TestGetMetaRejectsNonIMDbID(t *testing.T) {
	c := NewClient(nil)
	if _, err := c.GetMeta(context.Background(), "movie", "tmdb:603"); err == nil {
		t.Fatal("expected an error for a non-imdb id")
	}
}

func TestGetMetaNotFound(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	if _, err := c.GetMeta(context.Background(), "movie", "tt9999999"); err == nil {
		t.Fatal("expected an error on 404")
	}
}

func TestGetMetaEmptyMetaIsError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"meta":{}}`))
	})
	if _, err := c.GetMeta(context.Background(), "movie", "tt0000000"); err == nil {
		t.Fatal("expected an error for an empty meta object")
	}
}

func TestGetMetaContentTypeNormalizesToSeries(t *testing.T) {
	var gotPath string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"meta":{"imdb_id":"tt1","name":"X"}}`))
	})
	if _, err := c.GetMeta(context.Background(), "anime", "tt1"); err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if !strings.Contains(gotPath, "/meta/series/") {
		t.Fatalf("path = %q, want the series resource for an unrecognized content type", gotPath)
	}
}

func TestGetMetaCachesResponses(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"meta":{"imdb_id":"tt1","name":"X"}}`))
	})
	for i := 0; i < 3; i++ {
		if _, err := c.GetMeta(context.Background(), "movie", "tt1"); err != nil {
			t.Fatalf("GetMeta: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("upstream calls = %d, want 1 (cached)", calls)
	}
}

// TestGetMetaDoesNotCacheInvalidResponses pins the fix for caching bodies
// before validation: a response that fails validation (here, an empty meta
// object) must never get written to the cache, so a transient bad reply
// doesn't turn every later request for the same title into the same failure
// for the rest of the cache TTL. Once the upstream starts answering with a
// valid body, the very next call must succeed.
func TestGetMetaDoesNotCacheInvalidResponses(t *testing.T) {
	calls := 0
	valid := false
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if valid {
			_, _ = w.Write([]byte(`{"meta":{"imdb_id":"tt1","name":"X"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"meta":{}}`))
	})
	for i := 0; i < 3; i++ {
		if _, err := c.GetMeta(context.Background(), "movie", "tt1"); err == nil {
			t.Fatal("expected an error for the empty meta object")
		}
	}
	if calls != 3 {
		t.Fatalf("upstream calls = %d, want 3 — an invalid response must not be cached", calls)
	}
	valid = true
	if _, err := c.GetMeta(context.Background(), "movie", "tt1"); err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
}
