package simkl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"streamnzb/pkg/core/logger"
)

const testAllItems = `{
	"shows": [
		{
			"added_to_watchlist_at": "2020-01-20T20:09:04Z",
			"last_watched_at": null,
			"status": "hold",
			"show": {
				"title": "Emerald City",
				"poster": "51/5152376528f644b91",
				"year": 2017,
				"ids": {"simkl": 583436, "imdb": "tt3579018", "tmdb": "62417", "tvdb": "295779"}
			}
		},
		{
			"added_to_watchlist_at": "2021-01-01T00:00:00Z",
			"last_watched_at": "2024-05-01T12:00:00Z",
			"status": "watching",
			"show": {
				"title": "Severance",
				"poster": "12/12abc",
				"year": 2022,
				"ids": {"simkl": 1, "imdb": "tt11280740", "tmdb": 95396}
			}
		},
		{
			"added_to_watchlist_at": "2023-01-01T00:00:00Z",
			"last_watched_at": "2023-06-01T12:00:00Z",
			"status": "watching",
			"show": {
				"title": "Older Watch",
				"year": 2019,
				"ids": {"simkl": 2, "tmdb": "999"}
			}
		}
	],
	"anime": [
		{
			"added_to_watchlist_at": "2022-01-01T00:00:00Z",
			"status": "plantowatch",
			"show": {
				"title": "Ajin 2",
				"year": 2016,
				"ids": {"simkl": 581835, "mal": "33253"}
			}
		}
	],
	"movies": [
		{
			"added_to_watchlist_at": "2022-03-01T00:00:00Z",
			"status": "plantowatch",
			"movie": {
				"title": "Maleficent",
				"poster": "17/1722048af2197c9a7",
				"year": 2014,
				"ids": {"simkl": 195258, "imdb": "tt1587310", "tmdb": "102651"}
			}
		}
	]
}`

// newStubClient builds a client against a stub API. The data dir feeds the
// process-wide persistence singleton (never closed here — same pattern as the
// TVDB client tests). activityStamp is what /sync/activities reports; the
// counters see every list-related request.
func newStubClient(t *testing.T, activityStamp *atomic.Value, activityCalls, listCalls *atomic.Int64) *Client {
	t.Helper()
	logger.Init("ERROR")
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/pin", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result": "OK", "device_code": "d", "user_code": "ABC12", "verification_url": "https://simkl.com/pin/", "expires_in": 900, "interval": 5}`))
	})
	mux.HandleFunc("/oauth/pin/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/pin/PENDING" {
			_, _ = w.Write([]byte(`{"result": "KO", "message": "Authorization pending"}`))
			return
		}
		_, _ = w.Write([]byte(`{"result": "OK", "access_token": "test-token"}`))
	})
	mux.HandleFunc("/users/settings", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"user": {"name": "Test User"}, "account": {"id": 51}}`))
	})
	mux.HandleFunc("/sync/activities", func(w http.ResponseWriter, r *http.Request) {
		activityCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"all": "` + activityStamp.Load().(string) + `"}`))
	})
	mux.HandleFunc("/sync/all-items/", func(w http.ResponseWriter, r *http.Request) {
		listCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("simkl-api-key") != "test-client" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(testAllItems))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	dir, err := os.MkdirTemp("", "simkl_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	client := NewClient("test-client", dir)
	client.BaseURL = server.URL
	return client
}

func link(t *testing.T, client *Client) {
	t.Helper()
	connected, err := client.CheckPIN(context.Background(), "ABC12")
	if err != nil || !connected {
		t.Fatalf("CheckPIN = %v, %v; want linked", connected, err)
	}
}

func TestPINFlowLinksAccount(t *testing.T) {
	var stamp atomic.Value
	stamp.Store("2024-01-01T00:00:00Z")
	var activityCalls, listCalls atomic.Int64
	client := newStubClient(t, &stamp, &activityCalls, &listCalls)

	pin, err := client.StartPIN(context.Background())
	if err != nil || pin.UserCode != "ABC12" || pin.Interval != 5 {
		t.Fatalf("StartPIN = %+v, %v", pin, err)
	}
	if connected, err := client.CheckPIN(context.Background(), "PENDING"); err != nil || connected {
		t.Fatalf("pending CheckPIN = %v, %v; want false, nil", connected, err)
	}
	if client.Connected() {
		t.Fatal("connected before approval")
	}
	link(t, client)
	if !client.Connected() || client.UserName() != "Test User" {
		t.Fatalf("after link: connected=%v user=%q", client.Connected(), client.UserName())
	}

	// The token survives a client rebuild via the state store.
	fresh := NewClient("test-client", client.dataDir)
	fresh.BaseURL = client.BaseURL
	if !fresh.Connected() || fresh.UserName() != "Test User" {
		t.Fatalf("rebuilt client: connected=%v user=%q", fresh.Connected(), fresh.UserName())
	}

	// A different client id must not reuse the token.
	other := NewClient("other-client", client.dataDir)
	other.BaseURL = client.BaseURL
	if other.Connected() {
		t.Fatal("token minted for test-client accepted by other-client")
	}
}

func TestWatchlistFiltersSortsAndCaches(t *testing.T) {
	var stamp atomic.Value
	stamp.Store("2024-01-01T00:00:00Z")
	var activityCalls, listCalls atomic.Int64
	client := newStubClient(t, &stamp, &activityCalls, &listCalls)
	link(t, client)

	watching, err := client.Watchlist(context.Background(), "shows", "watching")
	if err != nil {
		t.Fatalf("Watchlist: %v", err)
	}
	if len(watching) != 2 || watching[0].Title != "Severance" || watching[1].Title != "Older Watch" {
		t.Fatalf("watching = %+v, want Severance (newer activity) first", watching)
	}
	// flexID: numeric and string tmdb ids both survive.
	if watching[0].TMDBID != "95396" || watching[1].TMDBID != "999" {
		t.Fatalf("tmdb ids = %q, %q", watching[0].TMDBID, watching[1].TMDBID)
	}

	hold, err := client.Watchlist(context.Background(), "shows", "hold")
	if err != nil || len(hold) != 1 || hold[0].Title != "Emerald City" || hold[0].IMDbID != "tt3579018" {
		t.Fatalf("hold = %+v, %v", hold, err)
	}
	if movies, _ := client.Watchlist(context.Background(), "movies", "plantowatch"); len(movies) != 1 || movies[0].Title != "Maleficent" {
		t.Fatalf("movies = %+v", movies)
	}
	if anime, _ := client.Watchlist(context.Background(), "anime", "plantowatch"); len(anime) != 1 || anime[0].MALID != "33253" {
		t.Fatalf("anime = %+v", anime)
	}

	// Everything above must have cost exactly one activities probe and one
	// full-list fetch — the board fires all rows against one cache.
	if activityCalls.Load() != 1 || listCalls.Load() != 1 {
		t.Fatalf("calls = %d activities, %d lists; want 1 and 1", activityCalls.Load(), listCalls.Load())
	}
}

func TestWatchlistRefetchesWhenActivitiesChange(t *testing.T) {
	var stamp atomic.Value
	stamp.Store("2024-01-01T00:00:00Z")
	var activityCalls, listCalls atomic.Int64
	client := newStubClient(t, &stamp, &activityCalls, &listCalls)
	link(t, client)

	if _, err := client.Watchlist(context.Background(), "shows", "watching"); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	// Age the cache past the probe interval, keep the stamp: revalidation must
	// probe but not refetch.
	client.listMu.Lock()
	client.lastActivityCheck = client.lastActivityCheck.Add(-2 * activitiesCheckInterval)
	client.listMu.Unlock()
	if _, err := client.Watchlist(context.Background(), "shows", "watching"); err != nil {
		t.Fatalf("revalidate: %v", err)
	}
	if activityCalls.Load() != 2 || listCalls.Load() != 1 {
		t.Fatalf("after unchanged revalidation: %d activities, %d lists; want 2 and 1", activityCalls.Load(), listCalls.Load())
	}
	// A changed stamp refetches.
	stamp.Store("2024-02-02T00:00:00Z")
	client.listMu.Lock()
	client.lastActivityCheck = client.lastActivityCheck.Add(-2 * activitiesCheckInterval)
	client.listMu.Unlock()
	if _, err := client.Watchlist(context.Background(), "shows", "watching"); err != nil {
		t.Fatalf("changed revalidation: %v", err)
	}
	if listCalls.Load() != 2 {
		t.Fatalf("after changed stamp: %d list fetches, want 2", listCalls.Load())
	}
}

func TestScrobblePayloads(t *testing.T) {
	logger.Init("ERROR")
	var lastPath string
	var lastBody map[string]interface{}
	status := http.StatusCreated
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/pin/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result": "OK", "access_token": "test-token"}`))
	})
	mux.HandleFunc("/users/settings", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"user": {"name": "Test User"}}`))
	})
	mux.HandleFunc("/scrobble/", func(w http.ResponseWriter, r *http.Request) {
		lastPath = r.URL.Path
		lastBody = nil
		_ = json.NewDecoder(r.Body).Decode(&lastBody)
		w.WriteHeader(status)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	dir, err := os.MkdirTemp("", "simkl_scrobble_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	client := NewClient("test-client", dir)
	client.BaseURL = server.URL
	link(t, client)

	// Movie stop: ids go out numeric where parseable, progress clamps to 100.
	err = client.Scrobble(context.Background(), "stop",
		ScrobbleItem{ContentType: "movie", Title: "Inception", IMDbID: "tt1375666", TMDBID: "27205"}, 123)
	if err != nil || lastPath != "/scrobble/stop" {
		t.Fatalf("movie scrobble: %v, path %q", err, lastPath)
	}
	movie := lastBody["movie"].(map[string]interface{})
	ids := movie["ids"].(map[string]interface{})
	if lastBody["progress"].(float64) != 100 || ids["imdb"] != "tt1375666" || ids["tmdb"].(float64) != 27205 {
		t.Fatalf("movie body = %+v", lastBody)
	}

	// Series carry show ids plus season/episode.
	err = client.Scrobble(context.Background(), "start",
		ScrobbleItem{ContentType: "series", IMDbID: "tt4574334", Season: 1, Episode: 3}, 42.014)
	if err != nil {
		t.Fatalf("series scrobble: %v", err)
	}
	episode := lastBody["episode"].(map[string]interface{})
	if lastBody["progress"].(float64) != 42.01 || episode["season"].(float64) != 1 || episode["number"].(float64) != 3 {
		t.Fatalf("series body = %+v", lastBody)
	}

	// Anime address by MAL id with the entry-local episode number.
	err = client.Scrobble(context.Background(), "stop",
		ScrobbleItem{ContentType: "anime", MALID: "999", Episode: 5}, 90)
	if err != nil {
		t.Fatalf("anime scrobble: %v", err)
	}
	anime := lastBody["anime"].(map[string]interface{})
	if anime["ids"].(map[string]interface{})["mal"].(float64) != 999 || lastBody["episode"].(map[string]interface{})["number"].(float64) != 5 {
		t.Fatalf("anime body = %+v", lastBody)
	}

	// Simkl's duplicate protection (409) is success, not an error.
	status = http.StatusConflict
	if err := client.Scrobble(context.Background(), "stop",
		ScrobbleItem{ContentType: "movie", IMDbID: "tt1375666"}, 90); err != nil {
		t.Fatalf("409 must not error: %v", err)
	}

	// Unaddressable items are refused before any request goes out.
	lastPath = ""
	if err := client.Scrobble(context.Background(), "stop", ScrobbleItem{ContentType: "series", IMDbID: "tt1"}, 50); err == nil || lastPath != "" {
		t.Fatalf("season-less series scrobble: err=%v path=%q", err, lastPath)
	}
}

func TestDisconnectDropsTokenAndList(t *testing.T) {
	var stamp atomic.Value
	stamp.Store("2024-01-01T00:00:00Z")
	var activityCalls, listCalls atomic.Int64
	client := newStubClient(t, &stamp, &activityCalls, &listCalls)
	link(t, client)
	if _, err := client.Watchlist(context.Background(), "shows", "watching"); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	client.Disconnect()
	if client.Connected() || client.UserName() != "" {
		t.Fatal("still connected after Disconnect")
	}
	if _, err := client.Watchlist(context.Background(), "shows", "watching"); err == nil {
		t.Fatal("Watchlist served without a linked account")
	}
	// The persisted token is gone too.
	fresh := NewClient("test-client", client.dataDir)
	fresh.BaseURL = client.BaseURL
	if fresh.Connected() {
		t.Fatal("rebuilt client still connected after Disconnect")
	}
}
