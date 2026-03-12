package api

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/media/unpack"
	"streamnzb/pkg/next/playback"
	"streamnzb/pkg/next/preset"
	"streamnzb/pkg/release"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/session"
)

const routerTestToken = "test-token"

func newAuthenticatedRouter(deps Dependencies) http.Handler {
	if deps.AuthenticateToken == nil {
		deps.AuthenticateToken = func(token string) (*auth.Device, error) {
			if strings.TrimSpace(token) == routerTestToken {
				return &auth.Device{Username: "admin", Token: routerTestToken}, nil
			}
			return nil, errors.New("invalid token")
		}
	}
	return NewRouter(deps)
}

func routerTestProtectedPath(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "/" + routerTestToken + path
}

func routerTestProtectedRequest(method, path string) *http.Request {
	return httptest.NewRequest(method, routerTestProtectedPath(path), nil)
}

func TestProtectedRoutesRequireAuthentication(t *testing.T) {
	router := newAuthenticatedRouter(Dependencies{
		Version:  "test",
		Preset:   preset.NewService("status_only"),
		Playback: playback.NewService(),
	})

	req := httptest.NewRequest(http.MethodGet, "/manifest.json", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusUnauthorized, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("expected JSON content type, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), "Unauthorized") {
		t.Fatalf("expected unauthorized body, got %q", rec.Body.String())
	}
}

func TestManifestRouteAcceptsQueryBearerAndCookieTokenAuth(t *testing.T) {
	router := newAuthenticatedRouter(Dependencies{
		Version:  "dev-build",
		Preset:   preset.NewService("status_only"),
		Playback: playback.NewService(),
	})

	tests := []struct {
		name   string
		path   string
		header string
		cookie string
	}{
		{name: "query token", path: "/manifest.json?token=" + routerTestToken},
		{name: "bearer token", path: "/manifest.json", header: "Bearer " + routerTestToken},
		{name: "session cookie", path: "/manifest.json", cookie: routerTestToken},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: "auth_session", Value: tc.cookie})
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestManifestRouteReturnsHybridAddonManifest(t *testing.T) {
	router := newAuthenticatedRouter(Dependencies{
		Version:  "dev-build",
		Preset:   preset.NewService("status_only"),
		Playback: playback.NewService(),
	})

	req := routerTestProtectedRequest(http.MethodGet, "/manifest.json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("expected CORS header, got %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("expected JSON content type, got %q", got)
	}

	var body struct {
		ID            string   `json:"id"`
		Version       string   `json:"version"`
		Name          string   `json:"name"`
		Description   string   `json:"description"`
		Resources     []string `json:"resources"`
		Types         []string `json:"types"`
		Catalogs      []any    `json:"catalogs"`
		IDPrefixes    []string `json:"idPrefixes"`
		BehaviorHints struct {
			Configurable          bool `json:"configurable"`
			ConfigurationRequired bool `json:"configurationRequired"`
		} `json:"behaviorHints"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ID != "community.streamnzb.next" {
		t.Fatalf("expected next manifest id, got %q", body.ID)
	}
	if body.Version != "0.0.0-dev.build" {
		t.Fatalf("expected normalized manifest version, got %q", body.Version)
	}
	if body.Name != "StreamNZB Next" {
		t.Fatalf("expected manifest name StreamNZB Next, got %q", body.Name)
	}
	if body.Description == "" {
		t.Fatal("expected non-empty description")
	}
	if strings.Join(body.Resources, ",") != "stream" {
		t.Fatalf("expected stream resource, got %#v", body.Resources)
	}
	if strings.Join(body.Types, ",") != "movie,series" {
		t.Fatalf("expected movie/series types, got %#v", body.Types)
	}
	if len(body.Catalogs) != 0 {
		t.Fatalf("expected empty catalogs, got %#v", body.Catalogs)
	}
	if strings.Join(body.IDPrefixes, ",") != "tt,tmdb,tvdb" {
		t.Fatalf("expected id prefixes tt,tmdb,tvdb, got %#v", body.IDPrefixes)
	}
	if body.BehaviorHints.Configurable {
		t.Fatal("expected manifest to be non-configurable")
	}
	if body.BehaviorHints.ConfigurationRequired {
		t.Fatal("expected configurationRequired false")
	}
}

func TestAddonStreamRouteReturnsNZBStreams(t *testing.T) {
	router := newAuthenticatedRouter(Dependencies{
		Version: "test",
		Preset: preset.NewServiceWithOptions(preset.Options{
			AvailNZBMode: "disabled",
			Indexer: routerTestIndexer{items: []indexer.Item{{
				Title:         "Movie Available",
				Link:          "https://api.indexer.example/api?t=get&guid=abc",
				Comments:      "https://indexer.example/details/abc",
				Size:          2 * 1024 * 1024 * 1024,
				ActualIndexer: "IndexerA",
			}}},
		}),
		Playback: playback.NewServiceWithOptions(playback.Options{
			DownloadHostAPIKeys: []playback.DownloadHostAPIKey{{Host: "indexer.example", APIKey: "secret"}},
		}),
	})

	req := routerTestProtectedRequest(http.MethodGet, "/stream/movie/tt1234567.json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("expected CORS header, got %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}

	var body preset.StreamResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(body.Streams))
	}
	stream := body.Streams[0]
	if stream.Name != "StreamNZB" {
		t.Fatalf("expected stream name StreamNZB, got %q", stream.Name)
	}
	parsedURL, err := url.Parse(stream.NZBURL)
	if err != nil {
		t.Fatalf("parse stream nzb url: %v", err)
	}
	playURL, err := url.Parse(stream.URL)
	if err != nil {
		t.Fatalf("parse stream play url: %v", err)
	}
	if playURL.Scheme != "http" || playURL.Host != "example.com" {
		t.Fatalf("unexpected play url base %q://%q", playURL.Scheme, playURL.Host)
	}
	if playURL.Path != routerTestProtectedPath("/play") {
		t.Fatalf("unexpected play url path %q", playURL.Path)
	}
	if got := playURL.Query().Get("metadata_id"); got != "tt1234567" {
		t.Fatalf("expected play metadata_id tt1234567, got %q", got)
	}
	if parsedURL.Hostname() != "api.indexer.example" {
		t.Fatalf("unexpected nzb host %q", parsedURL.Hostname())
	}
	if parsedURL.Path != "/api" {
		t.Fatalf("unexpected nzb path %q", parsedURL.Path)
	}
	if got := parsedURL.Query().Get("t"); got != "get" {
		t.Fatalf("expected type get query, got %q", got)
	}
	if got := parsedURL.Query().Get("guid"); got != "abc" {
		t.Fatalf("expected guid query abc, got %q", got)
	}
	if got := parsedURL.Query().Get("id"); got != "abc" {
		t.Fatalf("expected normalized id query abc, got %q", got)
	}
	if got := parsedURL.Query().Get("apikey"); got != "secret" {
		t.Fatalf("expected apikey query secret, got %q", got)
	}
	if got := playURL.Query().Get("nzburl"); got != parsedURL.String() {
		t.Fatalf("expected play nzburl %q, got %q", parsedURL.String(), got)
	}
	if stream.BehaviorHints == nil {
		t.Fatal("expected behavior hints")
	}
	if stream.BehaviorHints.Cached != nil && *stream.BehaviorHints.Cached {
		t.Fatalf("expected unknown cached hint to be omitted or false, got %#v", stream.BehaviorHints)
	}
	if !strings.Contains(stream.Description, "Movie Available") {
		t.Fatalf("expected description to contain title, got %q", stream.Description)
	}
}

func TestAddonStreamRouteReturnsBadRequestForInvalidPath(t *testing.T) {
	router := newAuthenticatedRouter(Dependencies{
		Version:  "test",
		Preset:   preset.NewService("status_only"),
		Playback: playback.NewService(),
	})

	req := routerTestProtectedRequest(http.MethodGet, "/stream/movie.json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestAddonStreamRouteSurfacesAvailabilityWithoutFailoverBehavior(t *testing.T) {
	router := newAuthenticatedRouter(Dependencies{
		Version: "test",
		Preset: preset.NewServiceWithOptions(preset.Options{
			AvailNZBMode: "status_only",
			Indexer: routerTestIndexer{items: []indexer.Item{{
				Title:         "Movie Available",
				Link:          "https://indexer.example/get?id=abc",
				Comments:      "https://indexer.example/details/abc",
				Size:          2 * 1024 * 1024 * 1024,
				ActualIndexer: "IndexerA",
			}}},
			AvailClient: routerTestAvailClient{result: &availnzb.ReleasesResult{Releases: []*availnzb.ReleaseWithStatus{{
				Release: &release.Release{
					Title:      "Movie Available",
					Link:       "https://avail.example/get?id=abc",
					DetailsURL: "https://indexer.example/details/abc",
					Size:       2 * 1024 * 1024 * 1024,
					Indexer:    "AvailNZB",
				},
				Available: true,
			}}}},
		}),
		Playback: playback.NewService(),
	})

	req := routerTestProtectedRequest(http.MethodGet, "/stream/movie/tt1234567.json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	var body preset.StreamResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(body.Streams))
	}
	stream := body.Streams[0]
	if stream.Name != "⚡ StreamNZB" {
		t.Fatalf("expected available stream name, got %q", stream.Name)
	}
	if stream.BehaviorHints == nil || stream.BehaviorHints.Cached == nil || !*stream.BehaviorHints.Cached {
		t.Fatalf("expected cached hint true for available stream, got %#v", stream.BehaviorHints)
	}
	if !strings.Contains(stream.Description, "Availability: Available") {
		t.Fatalf("expected description to contain available status, got %q", stream.Description)
	}
}

func TestLegacyRoutesAreNotRegistered(t *testing.T) {
	router := NewRouter(Dependencies{
		Version:  "test",
		Preset:   preset.NewService("status_only"),
		Playback: playback.NewService(),
	})

	tests := []struct {
		method         string
		path           string
		expectedStatus int
	}{
		{method: http.MethodGet, path: "/healthz", expectedStatus: http.StatusNotFound},
		{method: http.MethodPost, path: "/api/v1/preset/match", expectedStatus: http.StatusNotFound},
		{method: http.MethodPost, path: "/api/v1/service/play", expectedStatus: http.StatusNotFound},
		{method: http.MethodGet, path: "/resolve/play", expectedStatus: http.StatusUnauthorized},
		{method: http.MethodGet, path: "/play/missing", expectedStatus: http.StatusNotFound},
		{method: http.MethodPost, path: "/api/v1/reporting/playback", expectedStatus: http.StatusNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != tc.expectedStatus {
				t.Fatalf("expected status %d, got %d (%s)", tc.expectedStatus, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestPlayRouteReturnsBadRequestForMissingNZBURLOrSessionID(t *testing.T) {
	router := newAuthenticatedRouter(Dependencies{
		Version:  "test",
		Preset:   preset.NewService("status_only"),
		Playback: playback.NewService(),
	})

	req := routerTestProtectedRequest(http.MethodGet, "/play")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "nzburl or session_id is required") {
		t.Fatalf("expected missing play input error, got %q", rec.Body.String())
	}
}

func TestPlayRouteReturnsBadRequestForInvalidNZBURL(t *testing.T) {
	router := newAuthenticatedRouter(Dependencies{
		Version:  "test",
		Preset:   preset.NewService("status_only"),
		Playback: playback.NewService(),
	})

	req := routerTestProtectedRequest(http.MethodGet, "/play?nzburl=ftp://example.com/file.nzb")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), playback.ErrInvalidRequest.Error()) {
		t.Fatalf("expected invalid request error in body, got %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "http or https") {
		t.Fatalf("expected invalid nzburl cause in body, got %q", rec.Body.String())
	}

}

func TestPlayRouteReturnsBadRequestForInvalidEpisodeMetadataID(t *testing.T) {
	router := newAuthenticatedRouter(Dependencies{
		Version:  "test",
		Preset:   preset.NewService("status_only"),
		Playback: playback.NewService(),
	})

	req := routerTestProtectedRequest(http.MethodGet, "/play?nzburl="+url.QueryEscape("https://indexer.example/get?id=abc")+"&metadata_id=tvdb:456:1")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "metadata_id must include both season and episode") {
		t.Fatalf("expected invalid metadata_id error, got %q", rec.Body.String())
	}
}

func TestPlayRouteServesPlaybackFromDirectNZBURL(t *testing.T) {
	manager := session.NewManager(nil, nil, time.Minute, nil)
	t.Cleanup(manager.Shutdown)
	data := newRouterTestProbeableBytes([]byte("video-data"))
	svc := playback.NewServiceWithOptions(playback.Options{
		DownloadHostAPIKeys: []playback.DownloadHostAPIKey{{Host: "indexer.example", APIKey: "secret"}},
		SessionManager:      manager,
		Indexer:             routerTestIndexer{},
		OpenStream: func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
			return &routerTestReadSeekCloser{Reader: bytes.NewReader(data)}, "episode.mkv", int64(len(data)), nil
		},
	})
	router := newAuthenticatedRouter(Dependencies{
		Version:  "test",
		Preset:   preset.NewService("status_only"),
		Playback: svc,
	})
	rawNZBURL := "https://api.indexer.example/api?t=get&guid=abc"

	req := routerTestProtectedRequest(http.MethodGet, "/play?nzburl="+url.QueryEscape(rawNZBURL))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), data) {
		t.Fatalf("expected playback body of %d bytes, got %d", len(data), rec.Body.Len())
	}
	normalized, err := svc.NormalizeDownloadURL(rawNZBURL)
	if err != nil {
		t.Fatalf("NormalizeDownloadURL: %v", err)
	}
	sess, err := manager.GetSession(routerTestSessionID(normalized))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Release == nil || sess.Release.Link != normalized {
		t.Fatalf("unexpected session release %#v", sess.Release)
	}
	if sess.ContentType != "" || sess.ContentID != "" || sess.ContentIDs != nil {
		t.Fatalf("expected empty content context, got type=%q id=%q ids=%#v", sess.ContentType, sess.ContentID, sess.ContentIDs)
	}
}

func TestPlayRouteServesPlaybackFromSessionID(t *testing.T) {
	manager := session.NewManager(nil, nil, time.Minute, nil)
	t.Cleanup(manager.Shutdown)
	data := newRouterTestProbeableBytes([]byte("video-data"))
	svc := playback.NewServiceWithOptions(playback.Options{
		DownloadHostAPIKeys: []playback.DownloadHostAPIKey{{Host: "indexer.example", APIKey: "secret"}},
		SessionManager:      manager,
		Indexer:             routerTestIndexer{},
		OpenStream: func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
			return &routerTestReadSeekCloser{Reader: bytes.NewReader(data)}, "episode.mkv", int64(len(data)), nil
		},
	})
	router := newAuthenticatedRouter(Dependencies{
		Version:  "test",
		Preset:   preset.NewService("status_only"),
		Playback: svc,
	})
	rawNZBURL := "https://api.indexer.example/api?t=get&guid=abc"
	sessionID, _, err := svc.ResolveNZBURL(rawNZBURL, "")
	if err != nil {
		t.Fatalf("ResolveNZBURL: %v", err)
	}

	req := routerTestProtectedRequest(http.MethodGet, "/play?session_id="+url.QueryEscape(sessionID))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), data) {
		t.Fatalf("expected playback body of %d bytes, got %d", len(data), rec.Body.Len())
	}
}

func TestPlayRouteStoresOptionalContentContext(t *testing.T) {
	manager := session.NewManager(nil, nil, time.Minute, nil)
	t.Cleanup(manager.Shutdown)
	data := newRouterTestProbeableBytes([]byte("video-data"))
	svc := playback.NewServiceWithOptions(playback.Options{
		DownloadHostAPIKeys: []playback.DownloadHostAPIKey{{Host: "indexer.example", APIKey: "secret"}},
		SessionManager:      manager,
		Indexer:             routerTestIndexer{},
		OpenStream: func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
			return &routerTestReadSeekCloser{Reader: bytes.NewReader(data)}, "episode.mkv", int64(len(data)), nil
		},
	})
	router := newAuthenticatedRouter(Dependencies{
		Version:  "test",
		Preset:   preset.NewService("status_only"),
		Playback: svc,
	})
	rawNZBURL := "https://api.indexer.example/api?t=get&guid=abc"

	path := "/play?nzburl=" + url.QueryEscape(rawNZBURL) + "&metadata_id=tvdb:456:1:2"
	req := routerTestProtectedRequest(http.MethodGet, path)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	normalized, err := svc.NormalizeDownloadURL(rawNZBURL)
	if err != nil {
		t.Fatalf("NormalizeDownloadURL: %v", err)
	}
	sess, err := manager.GetSession(routerTestSessionID(normalized))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.ContentType != "series" || sess.ContentID != "tvdb:456:1:2" {
		t.Fatalf("unexpected content context type=%q id=%q", sess.ContentType, sess.ContentID)
	}
	if sess.ContentIDs == nil || sess.ContentIDs.TvdbID != "456" || sess.ContentIDs.Season != 1 || sess.ContentIDs.Episode != 2 {
		t.Fatalf("unexpected content ids %#v", sess.ContentIDs)
	}
}

func TestResolveRouteReturnsSessionIDAndPlayURL(t *testing.T) {
	manager := session.NewManager(nil, nil, time.Minute, nil)
	t.Cleanup(manager.Shutdown)
	data := newRouterTestProbeableBytes([]byte("video-data"))
	openCalls := 0
	svc := playback.NewServiceWithOptions(playback.Options{
		DownloadHostAPIKeys: []playback.DownloadHostAPIKey{{Host: "indexer.example", APIKey: "secret"}},
		SessionManager:      manager,
		Indexer:             routerTestIndexer{},
		OpenStream: func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
			openCalls++
			return &routerTestReadSeekCloser{Reader: bytes.NewReader(data)}, "episode.mkv", int64(len(data)), nil
		},
	})
	router := newAuthenticatedRouter(Dependencies{
		Version:  "test",
		Preset:   preset.NewService("status_only"),
		Playback: svc,
	})
	rawNZBURL := "https://api.indexer.example/api?t=get&guid=abc"

	req := routerTestProtectedRequest(http.MethodGet, "/resolve?nzburl="+url.QueryEscape(rawNZBURL)+"&metadata_id=tvdb:456:1:2")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	var body struct {
		SessionID string `json:"session_id"`
		PlayURL   string `json:"play_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	normalized, err := svc.NormalizeDownloadURL(rawNZBURL)
	if err != nil {
		t.Fatalf("NormalizeDownloadURL: %v", err)
	}
	expectedSessionID := routerTestSessionID(normalized)
	if body.SessionID != expectedSessionID {
		t.Fatalf("expected session_id %q, got %q", expectedSessionID, body.SessionID)
	}
	playURL, err := url.Parse(body.PlayURL)
	if err != nil {
		t.Fatalf("parse play url: %v", err)
	}
	if playURL.Scheme != "http" || playURL.Host != "example.com" {
		t.Fatalf("unexpected play url base %q://%q", playURL.Scheme, playURL.Host)
	}
	if playURL.Path != routerTestProtectedPath("/play") {
		t.Fatalf("unexpected play url path %q", playURL.Path)
	}
	if got := playURL.Query().Get("session_id"); got != expectedSessionID {
		t.Fatalf("expected play url session_id %q, got %q", expectedSessionID, got)
	}
	sess, err := manager.GetSession(expectedSessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.ContentType != "series" || sess.ContentID != "tvdb:456:1:2" {
		t.Fatalf("unexpected content context type=%q id=%q", sess.ContentType, sess.ContentID)
	}
	if sess.PlaybackValidatedAt.IsZero() {
		t.Fatal("expected resolve preflight to mark session playback validated")
	}
	snapshot, ok := sess.PlaybackStreamSnapshot()
	if !ok {
		t.Fatal("expected resolve preflight to cache playback snapshot")
	}
	if !snapshot.HasStartupInfo || !snapshot.StartupInfo.HeaderValid {
		t.Fatalf("expected valid cached startup info, got %#v", snapshot)
	}
	if openCalls != 2 {
		t.Fatalf("expected resolve preflight to probe+open once, got %d opens", openCalls)
	}
}

func TestResolveRouteReturnsNotFoundForPlaybackStartupFailure(t *testing.T) {
	manager := session.NewManager(nil, nil, time.Minute, nil)
	t.Cleanup(manager.Shutdown)

	router := newAuthenticatedRouter(Dependencies{
		Version: "test",
		Preset:  preset.NewService("status_only"),
		Playback: playback.NewServiceWithOptions(playback.Options{
			SessionManager: manager,
			Indexer:        routerTestIndexer{},
			OpenStream: func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
				return nil, "", 0, errors.New("compressed archive")
			},
		}),
	})

	req := routerTestProtectedRequest(http.MethodGet, "/resolve?nzburl="+url.QueryEscape("https://indexer.example/get?id=abc"))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusNotFound, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), playback.ErrPlaybackStartup.Error()) {
		t.Fatalf("expected startup error in body, got %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "compressed archive") {
		t.Fatalf("expected startup cause in body, got %q", rec.Body.String())
	}
}

func TestPlayRouteReturnsNotFoundForPlaybackStartupFailure(t *testing.T) {
	manager := session.NewManager(nil, nil, time.Minute, nil)
	t.Cleanup(manager.Shutdown)

	router := newAuthenticatedRouter(Dependencies{
		Version: "test",
		Preset:  preset.NewService("status_only"),
		Playback: playback.NewServiceWithOptions(playback.Options{
			SessionManager: manager,
			Indexer:        routerTestIndexer{},
			OpenStream: func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
				return nil, "", 0, errors.New("compressed archive")
			},
		}),
	})

	req := routerTestProtectedRequest(http.MethodGet, "/play?nzburl="+url.QueryEscape("https://indexer.example/get?id=abc"))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusNotFound, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), playback.ErrPlaybackStartup.Error()) {
		t.Fatalf("expected startup error in body, got %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "compressed archive") {
		t.Fatalf("expected startup cause in body, got %q", rec.Body.String())
	}
}

func TestPlayRouteReturnsNotFoundForMissingSessionID(t *testing.T) {
	manager := session.NewManager(nil, nil, time.Minute, nil)
	t.Cleanup(manager.Shutdown)
	router := newAuthenticatedRouter(Dependencies{
		Version: "test",
		Preset:  preset.NewService("status_only"),
		Playback: playback.NewServiceWithOptions(playback.Options{
			SessionManager: manager,
			Indexer:        routerTestIndexer{},
		}),
	})

	req := routerTestProtectedRequest(http.MethodGet, "/play?session_id=missing")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusNotFound, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), playback.ErrSessionNotFound.Error()) {
		t.Fatalf("expected missing session error, got %q", rec.Body.String())
	}
}

type routerTestIndexer struct {
	items          []indexer.Item
	nzbData        []byte
	nzbErr         error
	downloadedURLs *[]string
}

type routerTestAvailClient struct {
	result *availnzb.ReleasesResult
}

func (r routerTestIndexer) Search(indexer.SearchRequest) (*indexer.SearchResponse, error) {
	return &indexer.SearchResponse{Channel: indexer.Channel{Items: append([]indexer.Item(nil), r.items...)}}, nil
}

func (r routerTestIndexer) DownloadNZB(_ context.Context, nzbURL string) ([]byte, error) {
	if r.downloadedURLs != nil {
		*r.downloadedURLs = append(*r.downloadedURLs, nzbURL)
	}
	if r.nzbErr != nil {
		return nil, r.nzbErr
	}
	return append([]byte(nil), r.nzbData...), nil
}

func (routerTestIndexer) Ping() error { return nil }

func (routerTestIndexer) Name() string { return "router-test" }

func (routerTestIndexer) GetUsage() indexer.Usage { return indexer.Usage{} }

func (r routerTestAvailClient) GetReleases(string, string, int, int, []string, []string) (*availnzb.ReleasesResult, error) {
	return r.result, nil
}

func routerTestSessionID(downloadURL string) string {
	sum := md5.Sum([]byte(strings.TrimSpace(downloadURL)))
	return "resolve-" + hex.EncodeToString(sum[:])
}

type routerTestReadSeekCloser struct {
	*bytes.Reader
}

func (r *routerTestReadSeekCloser) Close() error { return nil }

func newRouterTestProbeableBytes(payload []byte) []byte {
	data := make([]byte, 0, 8+len(payload))
	data = append(data, 0x1A, 0x45, 0xDF, 0xA3, 0x00, 0x00, 0x00, 0x00)
	data = append(data, payload...)
	return data
}
