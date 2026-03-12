package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/next/playback"
	"streamnzb/pkg/next/preset"
)

const appTestToken = "test-token"

func appTestAuthenticateToken(token string) (*auth.Device, error) {
	if strings.TrimSpace(token) != appTestToken {
		return nil, nil
	}
	return &auth.Device{Username: "admin", Token: appTestToken}, nil
}

func appTestProtectedPath(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "/" + appTestToken + path
}

func TestNewDefaultsRequireAuthentication(t *testing.T) {
	t.Parallel()

	application := New(Options{Version: "dev-build"})
	req := httptest.NewRequest(http.MethodGet, "/manifest.json", nil)
	rec := httptest.NewRecorder()

	application.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusUnauthorized, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Unauthorized") {
		t.Fatalf("expected unauthorized response, got %q", rec.Body.String())
	}
	if application.Handler() == nil {
		t.Fatal("expected handler to be initialized")
	}
}

func TestNewUsesInjectedServices(t *testing.T) {
	t.Parallel()

	application := New(Options{
		Version: "test",
		Preset: preset.NewServiceWithOptions(preset.Options{
			AvailNZBMode: "disabled",
			Indexer: appTestIndexer{items: []indexer.Item{{
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
		AuthenticateToken: appTestAuthenticateToken,
	})

	req := httptest.NewRequest(http.MethodGet, appTestProtectedPath("/stream/movie/tt1234567.json"), nil)
	rec := httptest.NewRecorder()

	application.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	var body preset.StreamResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode stream response: %v", err)
	}
	if len(body.Streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(body.Streams))
	}
	if !strings.Contains(body.Streams[0].NZBURL, "apikey=secret") {
		t.Fatalf("expected normalized nzb url with api key, got %q", body.Streams[0].NZBURL)
	}
	if !strings.Contains(body.Streams[0].URL, "/"+appTestToken+"/play") {
		t.Fatalf("expected tokenized play url, got %q", body.Streams[0].URL)
	}
}

type appTestIndexer struct {
	items []indexer.Item
}

func (a appTestIndexer) Search(indexer.SearchRequest) (*indexer.SearchResponse, error) {
	return &indexer.SearchResponse{Channel: indexer.Channel{Items: append([]indexer.Item(nil), a.items...)}}, nil
}

func (appTestIndexer) DownloadNZB(context.Context, string) ([]byte, error) { return nil, nil }

func (appTestIndexer) Ping() error { return nil }

func (appTestIndexer) Name() string { return "app-test" }

func (appTestIndexer) GetUsage() indexer.Usage { return indexer.Usage{} }

var _ indexer.Indexer = appTestIndexer{}
