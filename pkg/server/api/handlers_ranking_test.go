package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"streamnzb/pkg/core/config"
)

func postExplain(t *testing.T, s *Server, body any) *httptest.ResponseRecorder {
	t.Helper()
	blob, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/ranking/explain", bytes.NewReader(blob))
	rec := httptest.NewRecorder()
	s.handleRankingExplain(rec, req)
	return rec
}

// The Filters UI posts the profile it is editing, so an unsaved definition
// must be evaluated without touching the saved config.
func TestHandleRankingExplainUsesPostedProfile(t *testing.T) {
	s := &Server{}
	rec := postExplain(t, s, explainRequest{
		Titles: []string{
			"Movie 2020 2160p BluRay REMUX DV TrueHD 7.1-GRP",
			"Movie 2020 1080p CAM x264-TRASH",
		},
		Profile: &config.FilterProfileConfig{
			Name:             "Unsaved",
			BlockedQualities: []string{"CAM"},
		},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got explainResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Profile != "Unsaved" {
		t.Errorf("Profile = %q, want %q", got.Profile, "Unsaved")
	}
	if len(got.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got.Results))
	}
	if !got.Results[0].Fetch {
		t.Errorf("remux should be eligible: %v", got.Results[0].Rejections)
	}
	if got.Results[1].Fetch {
		t.Error("CAM release should be rejected")
	}
	if len(got.Results[0].Contributions) == 0 {
		t.Error("expected a score breakdown")
	}
}

func TestHandleRankingExplainResolvesSavedProfileByName(t *testing.T) {
	s := &Server{config: &config.Config{
		FilterProfiles: []config.FilterProfileConfig{config.DefaultFilterProfile()},
	}}

	rec := postExplain(t, s, explainRequest{
		Titles:      []string{"Movie 2020 1080p WEB-DL DDP5.1 H.264-GRP"},
		ProfileName: config.DefaultFilterProfileName,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got explainResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Profile != config.DefaultFilterProfileName {
		t.Errorf("Profile = %q, want %q", got.Profile, config.DefaultFilterProfileName)
	}
	if !got.Results[0].Fetch {
		t.Errorf("a 1080p WEB-DL should pass the default profile: %v", got.Results[0].Rejections)
	}
}

func TestHandleRankingExplainRejectsBadRequests(t *testing.T) {
	s := &Server{config: &config.Config{}}

	if rec := postExplain(t, s, explainRequest{Titles: []string{"  "}}); rec.Code != http.StatusBadRequest {
		t.Errorf("blank titles: status = %d, want 400", rec.Code)
	}
	if rec := postExplain(t, s, explainRequest{Titles: []string{"Movie"}, ProfileName: "nope"}); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown profile: status = %d, want 400", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/ranking/explain", nil)
	rec := httptest.NewRecorder()
	s.handleRankingExplain(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status = %d, want 405", rec.Code)
	}
}
