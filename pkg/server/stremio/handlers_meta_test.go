package stremio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/services/metadata/kitsu"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
	"streamnzb/pkg/services/metadata/tvmaze"
)

// metaTestServer wires a stremio Server against stub TMDB/TVMaze/Kitsu APIs,
// with one Default metadata profile for streamTestRequest to bind.
func metaTestServer(t *testing.T, tmdbHandler, tvmazeHandler, kitsuHandler http.HandlerFunc) *Server {
	t.Helper()
	srv := &Server{config: &config.Config{
		MetadataProfiles: []config.MetadataProfileConfig{{Name: "Default"}},
	}}
	if tmdbHandler != nil {
		ts := httptest.NewServer(tmdbHandler)
		t.Cleanup(ts.Close)
		srv.tmdbClient = tmdb.NewClient("test-key")
		srv.tmdbClient.BaseURL = ts.URL
	}
	if tvmazeHandler != nil {
		ts := httptest.NewServer(tvmazeHandler)
		t.Cleanup(ts.Close)
		srv.tvmazeClient = tvmaze.NewClient(ts.Client(), nil)
		srv.tvmazeClient.BaseURL = ts.URL
	}
	if kitsuHandler != nil {
		ts := httptest.NewServer(kitsuHandler)
		t.Cleanup(ts.Close)
		srv.kitsuClient = kitsu.NewClient(ts.Client())
		srv.kitsuClient.BaseURL = ts.URL
	}
	return srv
}

// streamTestRequest builds a handler request carrying an authenticated stream
// bound to the Default profile — what SetupRoutes' token auth normally does.
func streamTestRequest(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	stream := &auth.Stream{Username: "test-stream", MetadataProfileName: "Default"}
	return req.WithContext(auth.ContextWithStream(req.Context(), stream))
}

func TestResolveMetaID(t *testing.T) {
	srv := metaTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// /find/{id} responses for both schemes.
		switch {
		case strings.Contains(r.URL.Path, "tt0133093"):
			_, _ = w.Write([]byte(`{"movie_results": [{"id": 603}]}`))
		case strings.Contains(r.URL.Path, "121361"):
			_, _ = w.Write([]byte(`{"tv_results": [{"id": 1399}]}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}, nil, nil)

	cases := []struct {
		name, contentType, id string
		wantCanonical         string
		wantTMDB              int
		wantKitsu             string
		wantErr               bool
	}{
		{"imdb movie", "movie", "tt0133093", "tt0133093", 603, "", false},
		{"tmdb prefixed", "series", "tmdb:1399", "tmdb:1399", 1399, "", false},
		{"tvdb prefixed", "series", "tvdb:121361", "tmdb:1399", 1399, "", false},
		{"kitsu", "anime", "kitsu:486", "kitsu:486", 0, "486", false},
		{"bare numeric", "movie", "603", "tmdb:603", 603, "", false},
		{"garbage", "movie", "wat", "", 0, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rid, err := srv.resolveMetaID(context.Background(), tc.contentType, tc.id)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveMetaID: %v", err)
			}
			if rid.canonicalID != tc.wantCanonical || rid.tmdbID != tc.wantTMDB || rid.kitsuID != tc.wantKitsu {
				t.Fatalf("got canonical=%q tmdb=%d kitsu=%q", rid.canonicalID, rid.tmdbID, rid.kitsuID)
			}
		})
	}
}

func TestBuildMovieMeta(t *testing.T) {
	srv := metaTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/find/"):
			_, _ = w.Write([]byte(`{"movie_results": [{"id": 603}]}`))
		case strings.Contains(r.URL.Path, "/movie/603"):
			_, _ = w.Write([]byte(`{
				"id": 603, "title": "The Matrix", "release_date": "1999-03-30",
				"overview": "A hacker learns the truth.", "poster_path": "/p.jpg",
				"backdrop_path": "/b.jpg", "runtime": 136, "vote_average": 8.2,
				"imdb_id": "tt0133093", "genres": [{"id": 878, "name": "Science Fiction"}],
				"credits": {
					"cast": [
						{"name": "Keanu Reeves", "character": "Neo", "profile_path": "/keanu.jpg", "order": 0},
						{"name": "Carrie-Anne Moss", "order": 1}
					],
					"crew": [
						{"name": "Lana Wachowski", "job": "Director"},
						{"name": "Lilly Wachowski", "job": "Director"},
						{"name": "Lana Wachowski", "job": "Writer"}
					]
				},
				"images": {"logos": [
					{"file_path": "/logo-de.png", "iso_639_1": "de", "vote_average": 9.9},
					{"file_path": "/logo-en-low.png", "iso_639_1": "en", "vote_average": 1.2},
					{"file_path": "/logo-en.png", "iso_639_1": "en", "vote_average": 5.8}
				]},
				"videos": {"results": [
					{"key": "fan123", "site": "YouTube", "type": "Trailer", "official": false},
					{"key": "vKQi3bBA1y8", "site": "YouTube", "type": "Trailer", "official": true},
					{"key": "clip99", "site": "YouTube", "type": "Clip", "official": true}
				]}
			}`))
		default:
			http.NotFound(w, r)
		}
	}, nil, nil)

	meta, err := srv.buildMeta(context.Background(), &config.MetadataProfileConfig{}, "movie", "tt0133093")
	if err != nil {
		t.Fatalf("buildMeta: %v", err)
	}
	if meta.ID != "tt0133093" || meta.Type != "movie" || meta.Name != "The Matrix" {
		t.Fatalf("meta = %+v", meta)
	}
	if meta.Poster != tmdbPosterURL+"/p.jpg" || meta.Background != tmdbBackdropURL+"/b.jpg" {
		t.Fatalf("images = %q / %q", meta.Poster, meta.Background)
	}
	if meta.ReleaseInfo != "1999" || meta.Runtime != "136 min" || meta.IMDBRating != "8.2" {
		t.Fatalf("meta fields = %q %q %q", meta.ReleaseInfo, meta.Runtime, meta.IMDBRating)
	}
	if len(meta.Genres) != 1 || meta.Genres[0] != "Science Fiction" {
		t.Fatalf("genres = %v", meta.Genres)
	}
	// The details-panel fields Cinemeta users expect.
	if len(meta.Cast) != 2 || meta.Cast[0] != "Keanu Reeves" {
		t.Fatalf("cast = %v", meta.Cast)
	}
	// app_extras carries the cast photos clients render as avatars.
	if meta.AppExtras == nil || len(meta.AppExtras.Cast) != 2 {
		t.Fatalf("app_extras = %+v", meta.AppExtras)
	}
	if m := meta.AppExtras.Cast[0]; m.Character != "Neo" || m.Photo != tmdbProfileURL+"/keanu.jpg" {
		t.Fatalf("app_extras cast[0] = %+v", m)
	}
	if m := meta.AppExtras.Cast[1]; m.Photo != "" {
		t.Fatalf("app_extras cast[1] = %+v, want no photo", m)
	}
	if len(meta.Director) != 2 || len(meta.Writer) != 1 {
		t.Fatalf("director/writer = %v / %v", meta.Director, meta.Writer)
	}
	// Official trailers first, clips excluded.
	if len(meta.Trailers) != 2 || meta.Trailers[0].Source != "vKQi3bBA1y8" {
		t.Fatalf("trailers = %v", meta.Trailers)
	}
	if meta.Released != "1999-03-30T00:00:00.000Z" {
		t.Fatalf("released = %q", meta.Released)
	}
	// The highest-voted English TMDB logo wins over the metahub fallback.
	if meta.Logo != tmdbLogoURL+"/logo-en.png" {
		t.Fatalf("logo = %q", meta.Logo)
	}
}

func TestBuildSeriesMetaWithTVMazeOverlay(t *testing.T) {
	tmdbStub := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/find/"):
			_, _ = w.Write([]byte(`{"tv_results": [{"id": 1399}]}`))
		case strings.Contains(r.URL.Path, "/tv/1399"):
			_, _ = w.Write([]byte(`{
				"id": 1399, "name": "Game of Thrones", "first_air_date": "2011-04-17",
				"overview": "Winter is coming.", "poster_path": "/got.jpg",
				"vote_average": 8.4, "episode_run_time": [60],
				"external_ids": {"imdb_id": "tt0944947", "tvdb_id": 121361},
				"seasons": [
					{"season_number": 0, "episode_count": 3, "name": "Specials"},
					{"season_number": 1, "episode_count": 2, "name": "Season 1"}
				],
				"season/1": {"season_number": 1, "episodes": [
					{"episode_number": 1, "season_number": 1, "name": "Winter Is Coming",
					 "overview": "Ned Stark...", "air_date": "2011-04-17", "still_path": "/e1.jpg"},
					{"episode_number": 2, "season_number": 1, "name": "The Kingsroad",
					 "air_date": "2011-04-24"}
				]}
			}`))
		default:
			http.NotFound(w, r)
		}
	}
	tvmazeStub := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/lookup/shows"):
			_, _ = w.Write([]byte(`{"id": 82, "name": "Game of Thrones"}`))
		case strings.HasPrefix(r.URL.Path, "/shows/82"):
			_, _ = w.Write([]byte(`{"id": 82, "_embedded": {"episodes": [
				{"id": 1, "season": 1, "number": 1, "airstamp": "2011-04-18T01:00:00+00:00",
				 "summary": "<p>TVMaze summary</p>", "image": {"medium": "https://img/e1.jpg"}}
			]}}`))
		default:
			http.NotFound(w, r)
		}
	}
	srv := metaTestServer(t, tmdbStub, tvmazeStub, nil)

	meta, err := srv.buildMeta(context.Background(), &config.MetadataProfileConfig{}, "series", "tt0944947")
	if err != nil {
		t.Fatalf("buildMeta: %v", err)
	}
	if meta.Name != "Game of Thrones" || meta.ID != "tt0944947" {
		t.Fatalf("meta = %+v", meta)
	}
	if len(meta.Videos) != 2 {
		t.Fatalf("videos = %d, want 2 (specials excluded)", len(meta.Videos))
	}
	ep1 := meta.Videos[0]
	if ep1.ID != "tt0944947:1:1" {
		t.Fatalf("video id = %q", ep1.ID)
	}
	// TVMaze airstamp wins over TMDB's date-only air_date.
	if ep1.Released != "2011-04-18T01:00:00+00:00" {
		t.Fatalf("released = %q, want the TVMaze airstamp", ep1.Released)
	}
	// TMDB still wins for the thumbnail when it has one.
	if ep1.Thumbnail != tmdbStillURL+"/e1.jpg" {
		t.Fatalf("thumbnail = %q", ep1.Thumbnail)
	}
	// Episode 2 has no TVMaze entry: TMDB date stands.
	if meta.Videos[1].Released != "2011-04-24T00:00:00.000Z" {
		t.Fatalf("ep2 released = %q", meta.Videos[1].Released)
	}
}

func TestBuildAnimeMeta(t *testing.T) {
	kitsuStub := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/episodes"):
			_, _ = w.Write([]byte(`{"data": [
				{"attributes": {"canonicalTitle": "Asteroid Blues", "number": 1,
				 "airdate": "1998-10-24", "thumbnail": {"original": "https://img/1.jpg"}}},
				{"attributes": {"number": 2, "airdate": "1998-11-01"}}
			]}`))
		case strings.Contains(r.URL.Path, "/anime/1"):
			_, _ = w.Write([]byte(`{"data": {"id": "1", "attributes": {
				"canonicalTitle": "Cowboy Bebop", "synopsis": "Space bounty hunters.",
				"startDate": "1998-04-03", "averageRating": "82.5", "episodeCount": 26,
				"showType": "TV",
				"posterImage": {"original": "https://img/poster.jpg"},
				"coverImage": {"original": "https://img/cover.jpg"}
			}}}`))
		default:
			http.NotFound(w, r)
		}
	}
	srv := metaTestServer(t, nil, nil, kitsuStub)

	meta, err := srv.buildMeta(context.Background(), &config.MetadataProfileConfig{}, "anime", "kitsu:1")
	if err != nil {
		t.Fatalf("buildMeta: %v", err)
	}
	if meta.ID != "kitsu:1" || meta.Name != "Cowboy Bebop" {
		t.Fatalf("meta = %+v", meta)
	}
	if meta.IMDBRating != "8.2" {
		t.Fatalf("rating = %q, want Kitsu's 0-100 scale mapped to 0-10 (82.5 -> 8.2)", meta.IMDBRating)
	}
	if len(meta.Videos) != 2 {
		t.Fatalf("videos = %d", len(meta.Videos))
	}
	// The video id must be the season-less kitsu:<id>:<ep> the stream handler parses.
	if meta.Videos[0].ID != "kitsu:1:1" || meta.Videos[1].ID != "kitsu:1:2" {
		t.Fatalf("video ids = %q, %q", meta.Videos[0].ID, meta.Videos[1].ID)
	}
	if meta.Videos[1].Title != "Episode 2" {
		t.Fatalf("untitled episode fallback = %q", meta.Videos[1].Title)
	}
}

// tvdbStubHandler serves the TVDB endpoints the series meta path needs:
// login, remoteid resolution from imdb, extended details, and episodes.
func tvdbStubHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login":
			_, _ = w.Write([]byte(`{"status": "success", "data": {"token": "t"}}`))
		case strings.HasPrefix(r.URL.Path, "/search/remoteid/"):
			_, _ = w.Write([]byte(`{"status": "success", "data": [{"series": {"id": 121361}}]}`))
		case r.URL.Path == "/series/121361/extended":
			_, _ = w.Write([]byte(`{"status": "success", "data": {
				"id": 121361, "name": "Game of Thrones (TVDB)", "overview": "From TVDB.",
				"image": "https://artworks.thetvdb.com/got.jpg", "year": "2011",
				"lastAired": "2019-05-19", "status": {"name": "Ended"},
				"averageRuntime": 55, "genres": [{"name": "Fantasy"}],
				"artworks": [
					{"image": "https://artworks.thetvdb.com/got-fanart-bad.jpg", "type": 3, "score": 3},
					{"image": "https://artworks.thetvdb.com/got-fanart.jpg", "type": 3, "score": 87},
					{"image": "https://artworks.thetvdb.com/got-logo.png", "type": 23, "score": 12}
				],
				"characters": [
					{"name": "Daenerys Targaryen", "personName": "Emilia Clarke", "peopleType": "Actor", "personImgURL": "https://artworks.thetvdb.com/clarke.jpg"},
					{"personName": "Ramin Djawadi", "peopleType": "Musician"},
					{"personName": "Kit Harington", "peopleType": "Actor"}
				],
				"trailers": [{"url": "https://www.youtube.com/watch?v=KPLWWIOCOOQ"}]
			}}`))
		case r.URL.Path == "/series/121361/episodes/default":
			_, _ = w.Write([]byte(`{"status": "success", "data": {"episodes": [
				{"seasonNumber": 1, "number": 1, "name": "Winter Is Coming (TVDB)",
				 "aired": "2011-04-17", "image": "https://artworks.thetvdb.com/e1.jpg"},
				{"seasonNumber": 0, "number": 1, "name": "Special", "aired": "2011-01-01"}
			]}, "links": {"next": null}}`))
		default:
			http.NotFound(w, r)
		}
	}
}

// withTVDBStub attaches a stubbed TVDB client to a test server. The data dir
// feeds the process-wide persistence singleton (not closed here, same pattern
// as newBadReleaseTestServer).
func withTVDBStub(t *testing.T, srv *Server, handler http.HandlerFunc) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	dir, err := os.MkdirTemp("", "meta_tvdb_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	srv.tvdbClient = tvdb.NewClient("test-key", dir)
	srv.tvdbClient.BaseURL = ts.URL
}

// TestBuildSeriesMetaTVDBPrimary pins the source policy: series meta comes
// from TVDB (resolved from the imdb id), with TVMaze still owning air dates.
func TestBuildSeriesMetaTVDBPrimary(t *testing.T) {
	tmdbStub := func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/find/") {
			_, _ = w.Write([]byte(`{"tv_results": [{"id": 1399}]}`))
			return
		}
		http.NotFound(w, r)
	}
	tvmazeStub := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/lookup/shows"):
			_, _ = w.Write([]byte(`{"id": 82}`))
		case strings.HasPrefix(r.URL.Path, "/shows/82"):
			_, _ = w.Write([]byte(`{"id": 82, "_embedded": {"episodes": [
				{"id": 1, "season": 1, "number": 1, "airstamp": "2011-04-18T01:00:00+00:00"}
			]}}`))
		default:
			http.NotFound(w, r)
		}
	}
	srv := metaTestServer(t, tmdbStub, tvmazeStub, nil)
	withTVDBStub(t, srv, tvdbStubHandler())

	meta, err := srv.buildMeta(context.Background(), &config.MetadataProfileConfig{}, "series", "tt0944947")
	if err != nil {
		t.Fatalf("buildMeta: %v", err)
	}
	if meta.Name != "Game of Thrones (TVDB)" {
		t.Fatalf("name = %q, want the TVDB record", meta.Name)
	}
	if meta.Poster != "https://artworks.thetvdb.com/got.jpg" || meta.Background != "https://artworks.thetvdb.com/got-fanart.jpg" {
		t.Fatalf("artwork = %q / %q", meta.Poster, meta.Background)
	}
	if len(meta.Videos) != 1 {
		t.Fatalf("videos = %d, want 1 (specials excluded)", len(meta.Videos))
	}
	ep := meta.Videos[0]
	if ep.ID != "tt0944947:1:1" {
		t.Fatalf("video id = %q, want it anchored to the request's canonical id", ep.ID)
	}
	// TVMaze always wins the air date.
	if ep.Released != "2011-04-18T01:00:00+00:00" {
		t.Fatalf("released = %q, want the TVMaze airstamp", ep.Released)
	}
	if ep.Thumbnail != "https://artworks.thetvdb.com/e1.jpg" {
		t.Fatalf("thumbnail = %q, want TVDB's episode image", ep.Thumbnail)
	}
	// Details-panel enrichment: actors only, ended-run year range, trailer id.
	if len(meta.Cast) != 2 || meta.Cast[0] != "Emilia Clarke" || meta.Cast[1] != "Kit Harington" {
		t.Fatalf("cast = %v", meta.Cast)
	}
	if meta.AppExtras == nil || len(meta.AppExtras.Cast) != 2 {
		t.Fatalf("app_extras = %+v", meta.AppExtras)
	}
	if m := meta.AppExtras.Cast[0]; m.Character != "Daenerys Targaryen" || m.Photo != "https://artworks.thetvdb.com/clarke.jpg" {
		t.Fatalf("app_extras cast[0] = %+v", m)
	}
	if meta.ReleaseInfo != "2011-2019" {
		t.Fatalf("releaseInfo = %q, want the ended-run year range", meta.ReleaseInfo)
	}
	if len(meta.Trailers) != 1 || meta.Trailers[0].Source != "KPLWWIOCOOQ" {
		t.Fatalf("trailers = %v", meta.Trailers)
	}
	// TVDB's own clearlogo beats the metahub fallback.
	if meta.Logo != "https://artworks.thetvdb.com/got-logo.png" {
		t.Fatalf("logo = %q", meta.Logo)
	}
}

// TestBuildSeriesMetaSourceOverride flips series_source to tmdb and expects
// the TMDB record even though TVDB could serve.
func TestBuildSeriesMetaSourceOverride(t *testing.T) {
	tmdbStub := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/find/"):
			_, _ = w.Write([]byte(`{"tv_results": [{"id": 1399}]}`))
		case strings.Contains(r.URL.Path, "/tv/1399"):
			_, _ = w.Write([]byte(`{"id": 1399, "name": "Game of Thrones (TMDB)",
				"seasons": [{"season_number": 1, "episode_count": 1}],
				"season/1": {"season_number": 1, "episodes": [
					{"episode_number": 1, "season_number": 1, "name": "Winter Is Coming", "air_date": "2011-04-17"}
				]}}`))
		default:
			http.NotFound(w, r)
		}
	}
	srv := metaTestServer(t, tmdbStub, nil, nil)
	withTVDBStub(t, srv, tvdbStubHandler())

	meta, err := srv.buildMeta(context.Background(), &config.MetadataProfileConfig{SeriesSource: "tmdb"}, "series", "tt0944947")
	if err != nil {
		t.Fatalf("buildMeta: %v", err)
	}
	if meta.Name != "Game of Thrones (TMDB)" {
		t.Fatalf("name = %q, want the TMDB record when series_source=tmdb", meta.Name)
	}
}

// TestTVMazeAirDatesDisabled turns the TVMaze overlay off: the source's own
// air date must stand.
func TestTVMazeAirDatesDisabled(t *testing.T) {
	tvmazeStub := func(w http.ResponseWriter, r *http.Request) {
		t.Error("TVMaze must not be called when air dates are disabled")
	}
	srv := metaTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/find/") {
			_, _ = w.Write([]byte(`{"tv_results": [{"id": 1399}]}`))
			return
		}
		http.NotFound(w, r)
	}, tvmazeStub, nil)
	withTVDBStub(t, srv, tvdbStubHandler())
	off := false

	meta, err := srv.buildMeta(context.Background(), &config.MetadataProfileConfig{TVMazeAirDates: &off}, "series", "tt0944947")
	if err != nil {
		t.Fatalf("buildMeta: %v", err)
	}
	if meta.Videos[0].Released != "2011-04-17T00:00:00.000Z" {
		t.Fatalf("released = %q, want TVDB's own air date with TVMaze off", meta.Videos[0].Released)
	}
}

// TestHandleMetaKillSwitchOff pins the env kill-switch: even a stream with a
// bound profile serves no metadata when METADATA_ENABLED is off.
func TestHandleMetaKillSwitchOff(t *testing.T) {
	off := false
	srv := &Server{config: &config.Config{
		Metadata:         config.MetadataConfig{Enabled: &off},
		MetadataProfiles: []config.MetadataProfileConfig{{Name: "Default"}},
	}}
	req := streamTestRequest(http.MethodGet, "/meta/movie/tt0133093.json")
	rec := httptest.NewRecorder()
	srv.handleMeta(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 with the kill-switch off", rec.Code)
	}
}

// TestHandleMetaUnboundStream pins the opt-in contract: a stream with no
// metadata profile bound has no meta resource.
func TestHandleMetaUnboundStream(t *testing.T) {
	srv := &Server{config: &config.Config{
		MetadataProfiles: []config.MetadataProfileConfig{{Name: "Default"}},
	}}
	req := httptest.NewRequest(http.MethodGet, "/meta/movie/tt0133093.json", nil)
	req = req.WithContext(auth.ContextWithStream(req.Context(), &auth.Stream{Username: "unbound"}))
	rec := httptest.NewRecorder()
	srv.handleMeta(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a stream with no profile bound", rec.Code)
	}
}

func TestHandleMetaServesEnvelope(t *testing.T) {
	srv := metaTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/find/"):
			_, _ = w.Write([]byte(`{"movie_results": [{"id": 603}]}`))
		default:
			_, _ = w.Write([]byte(`{"id": 603, "title": "The Matrix"}`))
		}
	}, nil, nil)

	req := streamTestRequest(http.MethodGet, "/meta/movie/tt0133093.json")
	rec := httptest.NewRecorder()
	srv.handleMeta(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp MetaResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Meta == nil || resp.Meta.Name != "The Matrix" {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.CacheMaxAge != metaCacheMaxAge {
		t.Fatalf("cacheMaxAge = %d", resp.CacheMaxAge)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age=") {
		t.Fatalf("Cache-Control = %q", cc)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("missing CORS header")
	}
}

// TestStremioRouteListIncludesMetaAndCatalog guards the two-list lockstep: an
// unauthenticated request to a Stremio resource must 401, never fall through
// to the SPA.
func TestStremioRouteListIncludesMetaAndCatalog(t *testing.T) {
	srv := &Server{config: &config.Config{}}
	mux := http.NewServeMux()
	srv.SetupRoutes(mux)

	for _, path := range []string{
		"/badtoken/manifest.json",
		"/badtoken/stream/movie/tt1.json",
		"/badtoken/meta/movie/tt1.json",
		"/badtoken/catalog/movie/tmdb.trending.movie.json",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", path, rec.Code)
		}
	}
}

func TestParseCatalogPath(t *testing.T) {
	cases := []struct {
		path string
		want catalogRequest
		ok   bool
	}{
		{"/catalog/movie/tmdb.trending.movie.json", catalogRequest{Type: "movie", ID: "tmdb.trending.movie"}, true},
		{"/catalog/series/tmdb.trending.series/skip=40.json", catalogRequest{Type: "series", ID: "tmdb.trending.series", Skip: 40}, true},
		{"/catalog/movie/tmdb.trending.movie/search=dune.json", catalogRequest{Type: "movie", ID: "tmdb.trending.movie", Search: "dune"}, true},
		{"/catalog/movie/tmdb.trending.movie/search=the%20matrix&skip=20.json", catalogRequest{Type: "movie", ID: "tmdb.trending.movie", Search: "the matrix", Skip: 20}, true},
		{"/catalog/movie.json", catalogRequest{}, false},
		{"/catalog/movie/id/extra/too-deep.json", catalogRequest{}, false},
	}
	for _, tc := range cases {
		got, ok := parseCatalogPath(tc.path)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseCatalogPath(%q) = %+v ok=%v, want %+v ok=%v", tc.path, got, ok, tc.want, tc.ok)
		}
	}
}

func TestHandleCatalogUnknownAndDisabled(t *testing.T) {
	srv := &Server{config: &config.Config{
		MetadataProfiles: []config.MetadataProfileConfig{{
			Name:     "Default",
			Catalogs: []config.CatalogToggle{{ID: "tmdb.trending.movie", Enabled: true}},
		}},
	}}

	for _, tc := range []struct {
		name, path string
	}{
		{"unknown id", "/catalog/movie/not.real.json"},
		{"type mismatch", "/catalog/series/tmdb.trending.movie.json"},
		{"disabled catalog", "/catalog/series/tmdb.trending.series.json"},
	} {
		req := streamTestRequest(http.MethodGet, tc.path)
		rec := httptest.NewRecorder()
		srv.handleCatalog(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", tc.name, rec.Code)
		}
	}
}

func TestHandleCatalogServesTMDBTrending(t *testing.T) {
	srv := metaTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/trending/movie/week"):
			_, _ = w.Write([]byte(`{"page": 1, "results": [
				{"id": 603, "title": "The Matrix", "poster_path": "/m.jpg", "overview": "..." },
				{"id": 604, "title": "Reloaded"}
			]}`))
		case strings.Contains(r.URL.Path, "/movie/603/external_ids"):
			_, _ = w.Write([]byte(`{"id": 603, "imdb_id": "tt0133093"}`))
		case strings.Contains(r.URL.Path, "/movie/604/external_ids"):
			_, _ = w.Write([]byte(`{"id": 604}`))
		default:
			http.NotFound(w, r)
		}
	}, nil, nil)

	req := streamTestRequest(http.MethodGet, "/catalog/movie/tmdb.trending.movie.json")
	rec := httptest.NewRecorder()
	srv.handleCatalog(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp CatalogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Metas) != 2 {
		t.Fatalf("metas = %d", len(resp.Metas))
	}
	if resp.Metas[0].ID != "tt0133093" {
		t.Fatalf("first id = %q, want the resolved tt id", resp.Metas[0].ID)
	}
	if resp.Metas[1].ID != "tmdb:604" {
		t.Fatalf("second id = %q, want the tmdb: fallback", resp.Metas[1].ID)
	}
	if resp.Metas[0].Poster != tmdbPosterURL+"/m.jpg" {
		t.Fatalf("poster = %q", resp.Metas[0].Poster)
	}
}

func TestContinueWatchingCatalogFromLibrary(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cw_catalog_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tempDir) })
	// GetManager is a process-wide singleton; not closed here on purpose (see
	// newBadReleaseTestServer).
	mgr, err := persistence.GetManager(tempDir)
	if err != nil {
		t.Fatalf("get manager: %v", err)
	}
	for _, item := range []*persistence.LibraryItem{
		{ContentType: "movie", ContentID: "tt0133093", ImdbID: "tt0133093", ReleaseTitle: "The.Matrix.1999.1080p", Status: "good", NZBData: []byte("<nzb/>")},
		// Second release of the same movie must collapse into one row.
		{ContentType: "movie", ContentID: "tt0133093", ImdbID: "tt0133093", ReleaseTitle: "The.Matrix.1999.2160p", Status: "good", NZBData: []byte("<nzb/>")},
	} {
		if err := mgr.LibraryStore().StoreItem(item); err != nil {
			t.Fatalf("store item: %v", err)
		}
		// The singleton database is shared by every test in the package; leave
		// it the way it was found.
		id := item.ID
		t.Cleanup(func() { _ = mgr.LibraryStore().DeleteItem(id) })
	}

	srv := metaTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/find/"):
			_, _ = w.Write([]byte(`{"movie_results": [{"id": 603}]}`))
		case strings.Contains(r.URL.Path, "/movie/603"):
			_, _ = w.Write([]byte(`{"id": 603, "title": "The Matrix", "poster_path": "/m.jpg"}`))
		default:
			http.NotFound(w, r)
		}
	}, nil, nil)
	srv.attemptRecorder = mgr
	// Continue Watching is opt-in since the defaults were suppressed.
	srv.config.MetadataProfiles[0].Catalogs = []config.CatalogToggle{{ID: "streamnzb.continue-watching.movie", Enabled: true}}

	req := streamTestRequest(http.MethodGet, "/catalog/movie/streamnzb.continue-watching.movie.json")
	rec := httptest.NewRecorder()
	srv.handleCatalog(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp CatalogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Metas) != 1 {
		t.Fatalf("metas = %d, want 1 (deduped by content id)", len(resp.Metas))
	}
	got := resp.Metas[0]
	if got.ID != "tt0133093" || got.Name != "The Matrix" || got.Poster != tmdbPosterURL+"/m.jpg" {
		t.Fatalf("preview = %+v", got)
	}
	if resp.CacheMaxAge != 0 {
		t.Fatalf("cacheMaxAge = %d, want 0 (personal catalog)", resp.CacheMaxAge)
	}
}

// TestBecauseYouWatchedCatalog seeds the library with two watched movies and
// expects TMDB recommendations for them, minus everything already watched.
func TestBecauseYouWatchedCatalog(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "byw_catalog_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	mgr, err := persistence.GetManager(tempDir)
	if err != nil {
		t.Fatalf("get manager: %v", err)
	}
	for _, item := range []*persistence.LibraryItem{
		{ContentType: "movie", ContentID: "tt0133093", ImdbID: "tt0133093", ReleaseTitle: "The.Matrix.1999", Status: "good", NZBData: []byte("<nzb/>")},
		{ContentType: "movie", ContentID: "tt0234215", ImdbID: "tt0234215", ReleaseTitle: "The.Matrix.Reloaded.2003", Status: "good", NZBData: []byte("<nzb/>")},
	} {
		if err := mgr.LibraryStore().StoreItem(item); err != nil {
			t.Fatalf("store item: %v", err)
		}
		id := item.ID
		t.Cleanup(func() { _ = mgr.LibraryStore().DeleteItem(id) })
	}

	srv := metaTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/find/tt0133093"):
			_, _ = w.Write([]byte(`{"movie_results": [{"id": 603}]}`))
		case strings.Contains(r.URL.Path, "/find/tt0234215"):
			_, _ = w.Write([]byte(`{"movie_results": [{"id": 604}]}`))
		case strings.Contains(r.URL.Path, "/movie/603/recommendations"):
			_, _ = w.Write([]byte(`{"page": 1, "results": [
				{"id": 604, "title": "The Matrix Reloaded", "poster_path": "/r.jpg"},
				{"id": 605, "title": "The Matrix Revolutions", "poster_path": "/v.jpg"}
			]}`))
		case strings.Contains(r.URL.Path, "/movie/604/recommendations"):
			_, _ = w.Write([]byte(`{"page": 1, "results": []}`))
		case strings.Contains(r.URL.Path, "/movie/604/external_ids"):
			_, _ = w.Write([]byte(`{"id": 604, "imdb_id": "tt0234215"}`))
		case strings.Contains(r.URL.Path, "/movie/605/external_ids"):
			_, _ = w.Write([]byte(`{"id": 605, "imdb_id": "tt0242653"}`))
		default:
			http.NotFound(w, r)
		}
	}, nil, nil)
	srv.attemptRecorder = mgr
	srv.config.MetadataProfiles[0].Catalogs = []config.CatalogToggle{{ID: "streamnzb.because-you-watched.movie", Enabled: true}}

	req := streamTestRequest(http.MethodGet, "/catalog/movie/streamnzb.because-you-watched.movie.json")
	rec := httptest.NewRecorder()
	srv.handleCatalog(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp CatalogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Metas) != 1 {
		t.Fatalf("metas = %+v, want only the unwatched recommendation", resp.Metas)
	}
	got := resp.Metas[0]
	if got.ID != "tt0242653" || got.Name != "The Matrix Revolutions" {
		t.Fatalf("preview = %+v", got)
	}
}

// TestContinueWatchingIsPerStream pins the per-stream resolution: the row
// shows what the requesting stream played, not the whole household's library.
func TestContinueWatchingIsPerStream(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cw_perstream_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	mgr, err := persistence.GetManager(tempDir)
	if err != nil {
		t.Fatalf("get manager: %v", err)
	}
	mgr.RecordAttempt(persistence.RecordAttemptParams{
		StreamName: "livingroom", ContentType: "movie", ContentID: "tt0133093",
		ContentTitle: "The Matrix", ReleaseTitle: "The.Matrix.1999", Success: true,
	})
	mgr.RecordAttempt(persistence.RecordAttemptParams{
		StreamName: "bedroom", ContentType: "movie", ContentID: "tt0234215",
		ContentTitle: "The Matrix Reloaded", ReleaseTitle: "The.Matrix.Reloaded.2003", Success: true,
	})
	// The shared database outlives this test; drop the attempt rows on the way out.
	t.Cleanup(func() { _, _ = mgr.DeleteAttemptsBefore(time.Now().Add(time.Hour)) })

	srv := metaTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/find/tt0133093"):
			_, _ = w.Write([]byte(`{"movie_results": [{"id": 603}]}`))
		case strings.Contains(r.URL.Path, "/movie/603"):
			_, _ = w.Write([]byte(`{"id": 603, "title": "The Matrix", "poster_path": "/m.jpg"}`))
		default:
			http.NotFound(w, r)
		}
	}, nil, nil)
	srv.attemptRecorder = mgr
	srv.config.MetadataProfiles[0].Catalogs = []config.CatalogToggle{{ID: "streamnzb.continue-watching.movie", Enabled: true}}

	req := httptest.NewRequest(http.MethodGet, "/catalog/movie/streamnzb.continue-watching.movie.json", nil)
	req = req.WithContext(auth.ContextWithStream(req.Context(), &auth.Stream{Username: "livingroom", MetadataProfileName: "Default"}))
	rec := httptest.NewRecorder()
	srv.handleCatalog(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp CatalogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Metas) != 1 {
		t.Fatalf("metas = %+v, want only livingroom's watch", resp.Metas)
	}
	if resp.Metas[0].ID != "tt0133093" || resp.Metas[0].Name != "The Matrix" {
		t.Fatalf("preview = %+v", resp.Metas[0])
	}
}

// TestCatalogCrossDeduplication pins the board rule: a title shows only in the
// highest-ranked catalog of its type; lower catalogs filter it out.
func TestCatalogCrossDeduplication(t *testing.T) {
	srv := metaTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/trending/movie/week"):
			_, _ = w.Write([]byte(`{"page": 1, "results": [{"id": 603, "title": "The Matrix"}]}`))
		case strings.Contains(r.URL.Path, "/movie/popular"):
			_, _ = w.Write([]byte(`{"page": 1, "results": [
				{"id": 603, "title": "The Matrix"},
				{"id": 604, "title": "The Matrix Reloaded"}
			]}`))
		case strings.Contains(r.URL.Path, "/external_ids"):
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}, nil, nil)
	srv.config.MetadataProfiles[0].Catalogs = []config.CatalogToggle{
		{ID: "tmdb.trending.movie", Enabled: true},
		{ID: "tmdb.popular.movie", Enabled: true},
	}

	// The higher-ranked catalog keeps the shared title...
	rec := httptest.NewRecorder()
	srv.handleCatalog(rec, streamTestRequest(http.MethodGet, "/catalog/movie/tmdb.trending.movie.json"))
	var trending CatalogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &trending); err != nil {
		t.Fatalf("decode trending: %v", err)
	}
	if len(trending.Metas) != 1 || trending.Metas[0].ID != "tmdb:603" {
		t.Fatalf("trending = %+v", trending.Metas)
	}

	// ...and the lower-ranked one drops it.
	rec = httptest.NewRecorder()
	srv.handleCatalog(rec, streamTestRequest(http.MethodGet, "/catalog/movie/tmdb.popular.movie.json"))
	var popular CatalogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &popular); err != nil {
		t.Fatalf("decode popular: %v", err)
	}
	if len(popular.Metas) != 1 || popular.Metas[0].ID != "tmdb:604" {
		t.Fatalf("popular = %+v, want the shared title deduplicated away", popular.Metas)
	}
}

// TestHandleCatalogSearchOnlyCarrier pins the hidden search carriers: they
// answer search for every profile — even one whose browse rows carry no
// search — and 404 a bare listing request (their search extra is required).
func TestHandleCatalogSearchOnlyCarrier(t *testing.T) {
	srv := metaTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/search/movie"):
			_, _ = w.Write([]byte(`{"page": 1, "results": [{"id": 603, "title": "The Matrix"}]}`))
		case strings.Contains(r.URL.Path, "/external_ids"):
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}, nil, nil)
	// A profile with only a family row — no browse catalog carries search.
	srv.config.MetadataProfiles[0].Catalogs = []config.CatalogToggle{{ID: "tmdb.family.movie", Enabled: true}}

	req := streamTestRequest(http.MethodGet, "/catalog/movie/tmdb.search.movie/search=matrix.json")
	rec := httptest.NewRecorder()
	srv.handleCatalog(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("search status = %d", rec.Code)
	}
	var resp CatalogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Metas) != 1 || resp.Metas[0].Name != "The Matrix" {
		t.Fatalf("metas = %+v", resp.Metas)
	}

	// Without a query the carrier does not exist as a listing.
	rec = httptest.NewRecorder()
	srv.handleCatalog(rec, streamTestRequest(http.MethodGet, "/catalog/movie/tmdb.search.movie.json"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bare listing status = %d, want 404", rec.Code)
	}
}

func TestHandleCatalogUpstreamFailureServesEmptyPage(t *testing.T) {
	srv := metaTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}, nil, nil)

	req := streamTestRequest(http.MethodGet, "/catalog/movie/tmdb.trending.movie.json")
	rec := httptest.NewRecorder()
	srv.handleCatalog(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with empty metas", rec.Code)
	}
	var resp CatalogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Metas == nil || len(resp.Metas) != 0 {
		t.Fatalf("metas = %v, want empty non-nil", resp.Metas)
	}
}
