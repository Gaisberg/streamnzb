package mdblist

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/services/metadata/scrobble"
)

// oauthStub stands in for MDBList's token endpoints. tokenReply is swapped per
// test to drive the device flow and the refresh path.
type oauthStub struct {
	mux        *http.ServeMux
	server     *httptest.Server
	tokenReply atomic.Value // func() (int, string)
	tokenCalls atomic.Int64
}

func newOAuthStub(t *testing.T) *oauthStub {
	t.Helper()
	s := &oauthStub{mux: http.NewServeMux()}
	s.mux.HandleFunc("/oauth/device-authorization/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"device_code":"DEV","user_code":"WDJB-MJHT",
			"verification_uri":"https://mdblist.com/oauth/device/",
			"verification_uri_complete":"https://mdblist.com/oauth/device/?user_code=WDJB-MJHT",
			"expires_in":300,"interval":5}`))
	})
	s.mux.HandleFunc("/oauth/token/", func(w http.ResponseWriter, r *http.Request) {
		s.tokenCalls.Add(1)
		reply := s.tokenReply.Load().(func() (int, string))
		status, body := reply()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	s.mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"username":"someone","name":"Some One"}`))
	})
	s.granted("access-1", "refresh-1", 2592000)
	s.server = httptest.NewServer(s.mux)
	t.Cleanup(s.server.Close)
	return s
}

func (s *oauthStub) granted(access, refresh string, expiresIn int) {
	s.tokenReply.Store(func() (int, string) {
		return http.StatusOK, fmt.Sprintf(`{"access_token":%q,"refresh_token":%q,"expires_in":%d,"token_type":"Bearer","scope":"write"}`,
			access, refresh, expiresIn)
	})
}

func (s *oauthStub) refuses(code string) {
	s.tokenReply.Store(func() (int, string) {
		return http.StatusBadRequest, fmt.Sprintf(`{"error":%q}`, code)
	})
}

func (s *oauthStub) client(t *testing.T) *Client {
	t.Helper()
	return s.stream(NewRegistry("client-id", t.TempDir()), "alice")
}

// stream builds one stream's client against the stub server.
func (s *oauthStub) stream(reg *Registry, name string) *Client {
	c := reg.For(name)
	c.BaseURL = s.server.URL
	return c
}

// link runs the device flow to completion, the way the settings page does.
func link(t *testing.T, c *Client) {
	t.Helper()
	start, err := c.StartDevice(context.Background())
	if err != nil {
		t.Fatalf("start device: %v", err)
	}
	if start.UserCode != "WDJB-MJHT" || start.VerificationURIComplete == "" {
		t.Fatalf("device start = %+v", start)
	}
	connected, err := c.CheckDevice(context.Background())
	if err != nil || !connected {
		t.Fatalf("check device: connected=%v err=%v", connected, err)
	}
}

func TestDeviceFlowLinksTheAccount(t *testing.T) {
	logger.Init("ERROR")
	stub := newOAuthStub(t)
	client := stub.client(t)

	if client.Connected() {
		t.Fatal("connected before linking")
	}
	// Polling before approval is "not yet", not a failure.
	stub.refuses("authorization_pending")
	if _, err := client.StartDevice(context.Background()); err != nil {
		t.Fatalf("start device: %v", err)
	}
	connected, err := client.CheckDevice(context.Background())
	if connected || err != nil {
		t.Fatalf("pending poll: connected=%v err=%v", connected, err)
	}

	stub.granted("access-1", "refresh-1", 2592000)
	if connected, err := client.CheckDevice(context.Background()); err != nil || !connected {
		t.Fatalf("approved poll: connected=%v err=%v", connected, err)
	}
	if !client.Connected() || client.UserName() != "someone" {
		t.Fatalf("after linking: connected=%v user=%q", client.Connected(), client.UserName())
	}

	// A denial is terminal and stops the pending authorization dead.
	client.Disconnect()
	stub.refuses("access_denied")
	if _, err := client.StartDevice(context.Background()); err != nil {
		t.Fatalf("restart device: %v", err)
	}
	if _, err := client.CheckDevice(context.Background()); err == nil {
		t.Fatal("access_denied must be reported")
	}
	if _, err := client.CheckDevice(context.Background()); err == nil {
		t.Fatal("polling must not continue after a denial")
	}
}

// MDBList access tokens last 30 days, so the renewal path is the difference
// between scrobbling that keeps working and scrobbling that dies a month in.
func TestExpiredTokenIsRefreshedBeforeUse(t *testing.T) {
	logger.Init("ERROR")
	stub := newOAuthStub(t)
	var scrobbled atomic.Int64
	var sawAuth atomic.Value
	sawAuth.Store("")
	stub.mux.HandleFunc("/scrobble/", func(w http.ResponseWriter, r *http.Request) {
		scrobbled.Add(1)
		sawAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	})
	client := stub.client(t)
	link(t, client)

	// Age the stored token past its expiry, as a month of uptime would.
	expired := client.snapshot()
	expired.ExpiresAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	client.persist(expired)

	stub.granted("access-2", "refresh-2", 2592000)
	before := stub.tokenCalls.Load()
	item := scrobble.Item{ContentType: "movie", IMDbID: "tt1375666"}
	if err := client.Scrobble(context.Background(), scrobble.VerbStop, item, 90); err != nil {
		t.Fatalf("scrobble across expiry: %v", err)
	}
	if stub.tokenCalls.Load() != before+1 {
		t.Fatalf("%d refreshes, want 1", stub.tokenCalls.Load()-before)
	}
	if got := sawAuth.Load().(string); got != "Bearer access-2" {
		t.Fatalf("scrobbled with %q, want the refreshed token", got)
	}
	// The renewed pair is persisted, so the next call spends no refresh.
	if client.snapshot().RefreshToken != "refresh-2" {
		t.Fatalf("refresh token not rotated: %+v", client.snapshot())
	}
	before = stub.tokenCalls.Load()
	if err := client.Scrobble(context.Background(), scrobble.VerbStop, item, 92); err != nil {
		t.Fatalf("second scrobble: %v", err)
	}
	if stub.tokenCalls.Load() != before {
		t.Fatal("a valid token was refreshed again")
	}
	if scrobbled.Load() != 2 {
		t.Fatalf("%d scrobbles reached MDBList, want 2", scrobbled.Load())
	}
}

// A revoked app answers 401 before the recorded expiry, which is worth one
// refresh — but a refresh MDBList itself rejects means the account is gone.
func TestRejectedTokenIsRetriedThenUnlinked(t *testing.T) {
	logger.Init("ERROR")
	stub := newOAuthStub(t)
	var accepted atomic.Bool
	stub.mux.HandleFunc("/scrobble/", func(w http.ResponseWriter, r *http.Request) {
		if accepted.Load() && r.Header.Get("Authorization") == "Bearer access-2" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	client := stub.client(t)
	link(t, client)

	// One 401, one refresh, one retry that lands.
	accepted.Store(true)
	stub.granted("access-2", "refresh-2", 2592000)
	item := scrobble.Item{ContentType: "movie", IMDbID: "tt1375666"}
	if err := client.Scrobble(context.Background(), scrobble.VerbStop, item, 90); err != nil {
		t.Fatalf("401 must be retried after a refresh: %v", err)
	}
	if !client.Connected() {
		t.Fatal("a recoverable 401 unlinked the account")
	}

	// Now the refresh token is refused too: nothing can be recovered.
	accepted.Store(false)
	stub.refuses("invalid_grant")
	if err := client.Scrobble(context.Background(), scrobble.VerbStop, item, 90); err == nil {
		t.Fatal("a dead refresh token must surface as an error")
	}
	if client.Connected() {
		t.Fatal("a rejected refresh token must unlink the account")
	}
}

// A transient refresh failure is not a revocation: the token has to survive it
// or one MDBList outage would silently unlink every install.
func TestTransientRefreshFailureKeepsTheAccount(t *testing.T) {
	logger.Init("ERROR")
	stub := newOAuthStub(t)
	client := stub.client(t)
	link(t, client)

	expired := client.snapshot()
	expired.ExpiresAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	client.persist(expired)

	stub.tokenReply.Store(func() (int, string) { return http.StatusInternalServerError, `{}` })
	item := scrobble.Item{ContentType: "movie", IMDbID: "tt1375666"}
	if err := client.Scrobble(context.Background(), scrobble.VerbStop, item, 90); err == nil {
		t.Fatal("a failed refresh must surface as an error")
	}
	if !client.Connected() {
		t.Fatal("a 500 from the token endpoint unlinked the account")
	}
}

// A token stored under a different client id cannot speak for the one in use.
func TestTokenIsPinnedToTheClientID(t *testing.T) {
	logger.Init("ERROR")
	stub := newOAuthStub(t)
	dir := t.TempDir()
	client := stub.stream(NewRegistry("client-id", dir), "alice")
	link(t, client)

	replaced := stub.stream(NewRegistry("a-different-client-id", dir), "alice")
	if replaced.Connected() {
		t.Fatal("a token from another client id was accepted")
	}
}

// Accounts are per stream: linking one stream must not sign in another.
func TestAccountsArePerStream(t *testing.T) {
	logger.Init("ERROR")
	stub := newOAuthStub(t)
	reg := NewRegistry("client-id", t.TempDir())

	alice := stub.stream(reg, "alice")
	bob := stub.stream(reg, "bob")
	link(t, alice)
	if !alice.Connected() || bob.Connected() {
		t.Fatalf("alice=%v bob=%v; linking one must not link the other", alice.Connected(), bob.Connected())
	}
	if got := reg.LinkedStreams(); len(got) != 1 || got[0] != "alice" {
		t.Fatalf("LinkedStreams = %v, want [alice]", got)
	}

	link(t, bob)
	alice.Disconnect()
	if alice.Connected() || !bob.Connected() {
		t.Fatalf("after alice disconnect: alice=%v bob=%v", alice.Connected(), bob.Connected())
	}
}

func TestScrobblePayloads(t *testing.T) {
	logger.Init("ERROR")
	stub := newOAuthStub(t)
	var lastPath string
	var lastBody map[string]any
	status := http.StatusOK
	stub.mux.HandleFunc("/scrobble/", func(w http.ResponseWriter, r *http.Request) {
		lastPath = r.URL.Path
		lastBody = nil
		_ = json.NewDecoder(r.Body).Decode(&lastBody)
		w.WriteHeader(status)
	})
	client := stub.client(t)
	link(t, client)

	// Movies address by imdb/tmdb id, numeric where parseable, and progress
	// clamps into the documented 0–100 range.
	item := scrobble.Item{ContentType: "movie", Title: "Inception", IMDbID: "tt1375666", TMDBID: "27205"}
	if err := client.Scrobble(context.Background(), scrobble.VerbStop, item, 123); err != nil {
		t.Fatalf("movie scrobble: %v", err)
	}
	if lastPath != "/scrobble/stop" {
		t.Fatalf("path = %q", lastPath)
	}
	ids := lastBody["movie"].(map[string]any)["ids"].(map[string]any)
	if lastBody["progress"].(float64) != 100 || ids["imdb"] != "tt1375666" || ids["tmdb"].(float64) != 27205 {
		t.Fatalf("movie body = %+v", lastBody)
	}

	// Series carry show ids plus the aired season/episode, flat.
	series := scrobble.Item{ContentType: "series", IMDbID: "tt4574334", TVDBID: "305288", Season: 1, Episode: 3}
	if err := client.Scrobble(context.Background(), scrobble.VerbStart, series, 42.014); err != nil {
		t.Fatalf("series scrobble: %v", err)
	}
	show := lastBody["show"].(map[string]any)
	if lastBody["progress"].(float64) != 42.01 || show["season"].(float64) != 1 || show["episode"].(float64) != 3 {
		t.Fatalf("series body = %+v", lastBody)
	}
	if show["ids"].(map[string]any)["tvdb"].(float64) != 305288 {
		t.Fatalf("series ids = %+v", show["ids"])
	}

	// Anime goes out as its aired series — MDBList has no MAL addressing — and
	// an anime film as a movie.
	anime := scrobble.Item{ContentType: "anime", MALID: "999", MALEpisode: 5, TVDBID: "305288", Season: 3, Episode: 17}
	if err := client.Scrobble(context.Background(), scrobble.VerbStop, anime, 90); err != nil {
		t.Fatalf("anime scrobble: %v", err)
	}
	show = lastBody["show"].(map[string]any)
	if show["season"].(float64) != 3 || show["episode"].(float64) != 17 {
		t.Fatalf("anime body = %+v", lastBody)
	}
	film := scrobble.Item{ContentType: "anime", AnimeMovie: true, MALID: "999", IMDbID: "tt0245429", TMDBID: "129"}
	if err := client.Scrobble(context.Background(), scrobble.VerbStop, film, 95); err != nil {
		t.Fatalf("anime film scrobble: %v", err)
	}
	movie, ok := lastBody["movie"].(map[string]any)
	if !ok {
		t.Fatalf("anime film body = %+v", lastBody)
	}
	// anime-lists prefers an entry's TMDB series record, so a film goes out on
	// its IMDb id alone rather than an id that may name the series.
	if _, sent := movie["ids"].(map[string]any)["tmdb"]; sent {
		t.Fatalf("anime film carried an untrustworthy tmdb id: %+v", lastBody)
	}
	if !client.Supports(scrobble.Item{ContentType: "movie", TMDBID: "129"}) {
		t.Fatal("an ordinary movie must still address by tmdb id")
	}
	if client.Supports(scrobble.Item{ContentType: "anime", AnimeMovie: true, TMDBID: "129"}) {
		t.Fatal("anime film reported as supported on a tmdb id alone")
	}

	// Anime only MAL can name has no MDBList address, and is refused before
	// any request goes out rather than sent as a half-addressed show.
	malOnly := scrobble.Item{ContentType: "anime", MALID: "999", MALEpisode: 5}
	if client.Supports(malOnly) {
		t.Fatal("MAL-only anime reported as supported")
	}
	lastPath = ""
	if err := client.Scrobble(context.Background(), scrobble.VerbStop, malOnly, 50); err == nil || lastPath != "" {
		t.Fatalf("MAL-only anime scrobble: err=%v path=%q", err, lastPath)
	}

	// A refused request is an error, not a silent success.
	status = http.StatusInternalServerError
	if err := client.Scrobble(context.Background(), scrobble.VerbStop, item, 50); err == nil {
		t.Fatal("500 must not pass as success")
	}
}
