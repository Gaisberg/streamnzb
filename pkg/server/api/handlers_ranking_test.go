package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
)

// adminConfig is the minimum config the handler needs to recognise an admin.
func adminConfig(profiles ...config.FilterProfileConfig) *config.Config {
	return &config.Config{AdminUsername: "admin", FilterProfiles: profiles}
}

func asAdmin(req *http.Request) *http.Request {
	return req.WithContext(auth.ContextWithStream(req.Context(), &auth.Stream{Username: "admin"}))
}

func postExplain(t *testing.T, s *Server, body any) *httptest.ResponseRecorder {
	t.Helper()
	blob, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := asAdmin(httptest.NewRequest(http.MethodPost, "/api/ranking/explain", bytes.NewReader(blob)))
	rec := httptest.NewRecorder()
	s.handleRankingExplain(rec, req)
	return rec
}

// The Filters UI posts the profile it is editing, so an unsaved definition
// must be evaluated without touching the saved config.
func TestHandleRankingExplainUsesPostedProfile(t *testing.T) {
	s := &Server{config: adminConfig()}
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

// A SeaDex sample answers seadex.dualAudio only for a group it also
// recommends, matching the invariant the live lookup keeps.
func TestHandleRankingExplainSeadexDualAudioNeedsARecommendation(t *testing.T) {
	s := &Server{config: adminConfig()}
	profile := &config.FilterProfileConfig{
		Name:  "Dual",
		Rules: []config.RuleConfig{{Name: "No dual", When: `seadex.dualAudio`, Action: config.RuleActionReject}},
	}
	explain := func(seadex *explainSeadex) bool {
		t.Helper()
		rec := postExplain(t, s, explainRequest{
			Titles:  []string{"Anime S01E01 1080p BluRay x265-Koala"},
			Profile: profile,
			Kind:    "anime_show",
			Sample:  &explainSample{Seadex: seadex},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var got explainResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got.Results[0].Fetch
	}
	if explain(&explainSeadex{BestGroups: []string{"Koala"}, DualAudioGroups: []string{"Koala"}}) {
		t.Error("a recommended dual-audio group must trip the reject rule")
	}
	if !explain(&explainSeadex{DualAudioGroups: []string{"Koala"}}) {
		t.Error("dual audio without a recommendation must not be answered true")
	}
}

func TestHandleRankingExplainResolvesSavedProfileByName(t *testing.T) {
	s := &Server{config: adminConfig(config.DefaultFilterProfile())}

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
	s := &Server{config: adminConfig()}

	if rec := postExplain(t, s, explainRequest{Titles: []string{"  "}}); rec.Code != http.StatusBadRequest {
		t.Errorf("blank titles: status = %d, want 400", rec.Code)
	}
	if rec := postExplain(t, s, explainRequest{Titles: []string{"Movie"}, ProfileName: "nope"}); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown profile: status = %d, want 400", rec.Code)
	}

	req := asAdmin(httptest.NewRequest(http.MethodGet, "/api/ranking/explain", nil))
	rec := httptest.NewRecorder()
	s.handleRankingExplain(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status = %d, want 405", rec.Code)
	}
}

// Profiles are configuration, and evaluating one compiles caller-supplied
// patterns, so a stream token must not reach this.
func TestHandleRankingExplainIsAdminOnly(t *testing.T) {
	s := &Server{config: adminConfig(config.DefaultFilterProfile())}
	body, _ := json.Marshal(explainRequest{
		Titles:      []string{"Movie 2020 1080p WEB-DL-GRP"},
		ProfileName: config.DefaultFilterProfileName,
	})

	for _, tc := range []struct {
		name   string
		stream *auth.Stream
	}{
		{"no stream", nil},
		{"non-admin stream", &auth.Stream{Username: "living-room"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/ranking/explain", bytes.NewReader(body))
			if tc.stream != nil {
				req = req.WithContext(auth.ContextWithStream(req.Context(), tc.stream))
			}
			rec := httptest.NewRecorder()
			s.handleRankingExplain(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", rec.Code)
			}
		})
	}
}

// A fixture set is the point of the candidates form: releases that differ in
// the parts a name cannot carry, so a cap or a size rule can be exercised
// against an uneven set rather than a uniform one.
func TestHandleRankingExplainAcceptsPerCandidateSamples(t *testing.T) {
	s := &Server{config: adminConfig()}
	rec := postExplain(t, s, explainRequest{
		Candidates: []explainCandidate{
			{
				Title:  "Movie 2020 2160p BluRay REMUX HEVC-BIG",
				Sample: &explainSample{IndexerData: true, SizeGB: 30},
			},
			{
				Title:  "Movie 2020 1080p WEB-DL H264-SMALL",
				Sample: &explainSample{IndexerData: true, SizeGB: 5},
			},
		},
		Profile: &config.FilterProfileConfig{
			Name:  "Sizes",
			Rules: []config.RuleConfig{{Name: "Oversized", When: "sizeGB > 20", Action: config.RuleActionReject}},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got explainResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got.Results))
	}
	for _, r := range got.Results {
		big := strings.Contains(r.Title, "BIG")
		if big && r.Fetch {
			t.Errorf("the 30 GB candidate should be rejected: %v", r.Rejections)
		}
		if !big && !r.Fetch {
			t.Errorf("the 5 GB candidate should survive: %v", r.Rejections)
		}
	}
}

// Titles and candidates are two ways to say the same thing, so a caller
// migrating from one to the other can mix them — a title takes the
// request-level sample, a candidate its own.
func TestHandleRankingExplainCombinesTitlesAndCandidates(t *testing.T) {
	s := &Server{config: adminConfig()}
	rec := postExplain(t, s, explainRequest{
		Titles:     []string{"Movie 2020 1080p WEB-DL H264-SHARED"},
		Candidates: []explainCandidate{{Title: "Movie 2020 1080p WEB-DL H264-OWN", Sample: &explainSample{IndexerData: true, SizeGB: 5}}},
		Sample:     &explainSample{IndexerData: true, SizeGB: 30},
		Profile: &config.FilterProfileConfig{
			Name:  "Sizes",
			Rules: []config.RuleConfig{{Name: "Oversized", When: "sizeGB > 20", Action: config.RuleActionReject}},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got explainResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got.Results))
	}
	for _, r := range got.Results {
		if strings.Contains(r.Title, "SHARED") && r.Fetch {
			t.Errorf("the bare title takes the request sample and should be rejected: %v", r.Rejections)
		}
		if strings.Contains(r.Title, "OWN") && !r.Fetch {
			t.Errorf("the candidate's own sample should win: %v", r.Rejections)
		}
	}
}

// SeaDex is one lookup per request, so a per-candidate answer is refused
// rather than silently ignored: a fixture set claiming two answers for one
// title would report a state no live search can reach.
func TestHandleRankingExplainRejectsPerCandidateSeadex(t *testing.T) {
	s := &Server{config: adminConfig()}
	rec := postExplain(t, s, explainRequest{
		Candidates: []explainCandidate{{
			Title:  "[Group] Show - 01 (1080p)",
			Sample: &explainSample{Seadex: &explainSeadex{BestGroups: []string{"Group"}}},
		}},
		Profile: &config.FilterProfileConfig{Name: "Anime"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "per request") {
		t.Errorf("error should explain that seadex is per request, got %s", rec.Body.String())
	}
}

func TestHandleRankingExplainRequiresSomethingToJudge(t *testing.T) {
	s := &Server{config: adminConfig()}
	rec := postExplain(t, s, explainRequest{
		Titles:  []string{"  ", ""},
		Profile: &config.FilterProfileConfig{Name: "Empty"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
