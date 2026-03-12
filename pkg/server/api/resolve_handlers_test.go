package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/session"
)

type resolveTestIndexer struct{}

func (*resolveTestIndexer) Search(indexer.SearchRequest) (*indexer.SearchResponse, error) {
	return nil, nil
}
func (*resolveTestIndexer) DownloadNZB(context.Context, string) ([]byte, error) { return nil, nil }
func (*resolveTestIndexer) Ping() error                                         { return nil }
func (*resolveTestIndexer) Name() string                                        { return "resolve-test" }
func (*resolveTestIndexer) GetUsage() indexer.Usage                             { return indexer.Usage{} }

func newResolveTestServer() *Server {
	return &Server{
		config:     &config.Config{AddonBaseURL: "http://localhost:7000/"},
		sessionMgr: session.NewManager(nil, nil, time.Minute, nil),
		indexer:    indexer.NewAggregator(&resolveTestIndexer{}),
	}
}

func TestHandleResolveNZBCreatesDeferredSessionAndReturnsTokenizedPlayURL(t *testing.T) {
	s := newResolveTestServer()
	defer s.sessionMgr.Shutdown()

	const nzbURL = "https://nzbfinder.ws/api?t=get&id=abc123"
	req := httptest.NewRequest(http.MethodPost, "/api/resolve", bytes.NewBufferString(`{"nzb_url":"`+nzbURL+`"}`))
	req = req.WithContext(auth.ContextWithDevice(req.Context(), &auth.Device{Username: "user", Token: "device-token"}))
	rr := httptest.NewRecorder()

	s.handleResolveNZB(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var got resolveNZBResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	wantSessionID := resolveSessionID(nzbURL)
	if !got.Success {
		t.Fatal("expected success response")
	}
	if got.SessionID != wantSessionID {
		t.Fatalf("SessionID = %q, want %q", got.SessionID, wantSessionID)
	}
	if got.PlayURL != "http://localhost:7000/device-token/resolve/play/"+wantSessionID {
		t.Fatalf("PlayURL = %q", got.PlayURL)
	}
	sess, err := s.sessionMgr.GetSession(wantSessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.ReleaseURL() != nzbURL {
		t.Fatalf("ReleaseURL = %q, want %q", sess.ReleaseURL(), nzbURL)
	}
}

func TestHandleResolveNZBReusesSessionIDForSameURL(t *testing.T) {
	s := newResolveTestServer()
	defer s.sessionMgr.Shutdown()

	const nzbURL = "https://nzbfinder.ws/api?t=get&id=abc123"
	call := func(token string) resolveNZBResponse {
		req := httptest.NewRequest(http.MethodPost, "/api/resolve", bytes.NewBufferString(`{"nzb_url":"`+nzbURL+`"}`))
		req = req.WithContext(auth.ContextWithDevice(req.Context(), &auth.Device{Username: "user", Token: token}))
		rr := httptest.NewRecorder()
		s.handleResolveNZB(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}
		var got resolveNZBResponse
		if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		return got
	}

	first := call("token-a")
	second := call("token-b")
	if first.SessionID != second.SessionID {
		t.Fatalf("SessionID mismatch: %q vs %q", first.SessionID, second.SessionID)
	}
	if first.PlayURL == second.PlayURL {
		t.Fatalf("expected tokenized play URLs to differ, got %q", first.PlayURL)
	}
}

func TestHandleResolveNZBRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name   string
		method string
		body   string
		want   int
	}{
		{name: "wrong method", method: http.MethodGet, body: `{"nzb_url":"https://example.com/get.nzb"}`, want: http.StatusMethodNotAllowed},
		{name: "missing url", method: http.MethodPost, body: `{}`, want: http.StatusBadRequest},
		{name: "invalid url", method: http.MethodPost, body: `{"nzb_url":"ftp://example.com/file.nzb"}`, want: http.StatusBadRequest},
		{name: "partial episode", method: http.MethodPost, body: `{"nzb_url":"https://example.com/file.nzb","season":1}`, want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newResolveTestServer()
			defer s.sessionMgr.Shutdown()
			req := httptest.NewRequest(tt.method, "/api/resolve", bytes.NewBufferString(tt.body))
			req = req.WithContext(auth.ContextWithDevice(req.Context(), &auth.Device{Username: "user", Token: "device-token"}))
			rr := httptest.NewRecorder()
			s.handleResolveNZB(rr, req)
			if rr.Code != tt.want {
				t.Fatalf("status = %d, want %d", rr.Code, tt.want)
			}
		})
	}
}

func TestHandleResolveNZBAllowsUnauthenticatedRequests(t *testing.T) {
	s := newResolveTestServer()
	defer s.sessionMgr.Shutdown()

	const nzbURL = "https://example.com/file.nzb"
	req := httptest.NewRequest(http.MethodPost, "/api/resolve", bytes.NewBufferString(`{"nzb_url":"`+nzbURL+`"}`))
	rr := httptest.NewRecorder()

	s.handleResolveNZB(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var got resolveNZBResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.PlayURL != "http://localhost:7000/resolve/play/"+resolveSessionID(nzbURL) {
		t.Fatalf("PlayURL = %q", got.PlayURL)
	}
}
