package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/dreulavelle/jhin/rank"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/search/ranking"
)

const maxExplainTitles = 100

// explainRequest asks how a profile would judge some titles. Profile carries
// the definition being edited rather than a saved name, so the Filters UI can
// preview a change before it is saved; ProfileName falls back to a saved one.
type explainRequest struct {
	Titles      []string                    `json:"titles"`
	Profile     *config.FilterProfileConfig `json:"profile,omitempty"`
	ProfileName string                      `json:"profile_name,omitempty"`
	TargetTitle string                      `json:"target_title,omitempty"`
}

type explainResponse struct {
	Profile string                 `json:"profile"`
	Results []*ranking.Explanation `json:"results"`
}

// handleRankingExplain scores titles against a profile and returns the
// per-clause breakdown behind each score, including why a release was rejected.
func (s *Server) handleRankingExplain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req explainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeExplainError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	titles := make([]string, 0, len(req.Titles))
	for _, t := range req.Titles {
		if t = strings.TrimSpace(t); t != "" {
			titles = append(titles, t)
		}
	}
	if len(titles) == 0 {
		writeExplainError(w, http.StatusBadRequest, "at least one title is required")
		return
	}
	if len(titles) > maxExplainTitles {
		titles = titles[:maxExplainTitles]
	}

	profile, err := s.explainProfile(req)
	if err != nil {
		writeExplainError(w, http.StatusBadRequest, err.Error())
		return
	}

	opts := rank.RankOptions{TargetTitle: strings.TrimSpace(req.TargetTitle)}
	results := make([]*ranking.Explanation, 0, len(titles))
	for _, title := range titles {
		results = append(results, profile.Explain(title, opts))
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(explainResponse{Profile: profile.Name, Results: results})
}

func writeExplainError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// explainProfile compiles the posted profile definition, or looks up a saved
// one by name when no definition was sent.
func (s *Server) explainProfile(req explainRequest) (*ranking.Profile, error) {
	fp := req.Profile
	if fp == nil {
		s.mu.RLock()
		cfg := s.config
		s.mu.RUnlock()

		name := strings.TrimSpace(req.ProfileName)
		if cfg == nil || name == "" {
			return nil, errNoProfile
		}
		for i := range cfg.FilterProfiles {
			if strings.EqualFold(cfg.FilterProfiles[i].Name, name) {
				fp = &cfg.FilterProfiles[i]
				break
			}
		}
		if fp == nil {
			return nil, errUnknownProfile
		}
	}

	// Compile in isolation: this is a preview, so it must never disturb the
	// rankers the addon is serving from.
	svc := ranking.NewService()
	if errs := svc.Reload(&config.Config{FilterProfiles: []config.FilterProfileConfig{*fp}}); len(errs) > 0 {
		return nil, errs[0]
	}
	profile, ok := svc.Get(fp.Name)
	if !ok {
		return nil, errUnknownProfile
	}
	return profile, nil
}

type explainError string

func (e explainError) Error() string { return string(e) }

const (
	errNoProfile      = explainError("a profile definition or profile_name is required")
	errUnknownProfile = explainError("unknown filter profile")
)
