package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/dreulavelle/jhin/rank"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/ranking"
	"streamnzb/pkg/search/rules"
	"streamnzb/pkg/search/triage"
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
	// Kind selects which scoped rules and per-kind limits apply, so a profile
	// tuned per content kind can be previewed as each of them. Empty exercises
	// the rules that apply everywhere.
	Kind string `json:"kind,omitempty"`
	// Sample is what to pretend about the releases being judged, for the parts
	// a release name cannot carry. Absent leaves those rules unjudged.
	Sample *explainSample `json:"sample,omitempty"`
}

// explainSample mirrors the preview's simulated-release controls. Each group
// is opt-in: what is not supplied stays unknown, and rules that read it are
// reported as unjudgeable rather than answered against a zero.
type explainSample struct {
	IndexerData bool    `json:"indexer_data,omitempty"`
	SizeGB      float64 `json:"size_gb,omitempty"`
	AgeDays     int     `json:"age_days,omitempty"`
	Grabs       int     `json:"grabs,omitempty"`
	Passworded  bool    `json:"passworded,omitempty"`
	Indexer     string  `json:"indexer,omitempty"`
	Library     bool    `json:"library,omitempty"`

	Probed *explainProbe `json:"probed,omitempty"`

	AvailStatus       string `json:"avail_status,omitempty"`
	AvailOnMyBackbone bool   `json:"avail_on_my_backbone,omitempty"`
	AvailCheckedDays  int    `json:"avail_checked_days,omitempty"`

	Seadex *explainSeadex `json:"seadex,omitempty"`
}

// explainSeadex is the pretend SeaDex answer for the request. Its presence
// means "a lookup ran"; each preview title is then judged by matching its
// parsed release group against these lists, exactly as a live request would
// be, so the preview cannot claim something matching never delivers.
type explainSeadex struct {
	Known      bool     `json:"known,omitempty"`
	BestGroups []string `json:"best_groups,omitempty"`
	AltGroups  []string `json:"alt_groups,omitempty"`
}

// toSeadex builds the request-level SeaDex context, nil when the sample does
// not simulate one. Naming any group implies the title is known.
func (e *explainSample) toSeadex() *rules.SeadexContext {
	if e == nil || e.Seadex == nil {
		return nil
	}
	out := &rules.SeadexContext{
		Known: e.Seadex.Known || len(e.Seadex.BestGroups) > 0 || len(e.Seadex.AltGroups) > 0,
		Best:  make(map[string]bool, len(e.Seadex.BestGroups)),
		Alt:   make(map[string]bool, len(e.Seadex.AltGroups)),
	}
	for _, g := range e.Seadex.BestGroups {
		if g = strings.ToLower(strings.TrimSpace(g)); g != "" {
			out.Best[g] = true
		}
	}
	for _, g := range e.Seadex.AltGroups {
		if g = strings.ToLower(strings.TrimSpace(g)); g != "" && !out.Best[g] {
			out.Alt[g] = true
		}
	}
	return out
}

type explainProbe struct {
	Height      int    `json:"height,omitempty"`
	VideoCodec  string `json:"video_codec,omitempty"`
	AudioCodec  string `json:"audio_codec,omitempty"`
	BitDepth    int    `json:"bit_depth,omitempty"`
	HDR         string `json:"hdr,omitempty"`
	DolbyVision bool   `json:"dolby_vision,omitempty"`
	// Track languages as ISO 639-1 codes, and track counts, so a preview can
	// exercise `"ja" in probed.audioLanguages` and `probed.audioStreams >= 2`.
	AudioLanguages    []string `json:"audio_languages,omitempty"`
	SubtitleLanguages []string `json:"subtitle_languages,omitempty"`
	AudioStreams      int      `json:"audio_streams,omitempty"`
	SubtitleStreams   int      `json:"subtitle_streams,omitempty"`
}

// toSample turns the request's pretend-release into what ranking evaluates
// against.
func (e *explainSample) toSample() *ranking.Sample {
	if e == nil {
		return nil
	}
	out := &ranking.Sample{
		IndexerData: e.IndexerData,
		SizeBytes:   int64(e.SizeGB * 1e9),
		AgeDays:     e.AgeDays,
		Grabs:       e.Grabs,
		Passworded:  e.Passworded,
		Indexer:     e.Indexer,
		Library:     e.Library,
	}
	if p := e.Probed; p != nil {
		out.Probed = &release.MediaCaps{
			Height:      p.Height,
			Width:       p.Height * 16 / 9,
			VideoCodec:  p.VideoCodec,
			AudioCodec:  p.AudioCodec,
			BitDepth:    p.BitDepth,
			HDR:         p.HDR,
			DolbyVision: p.DolbyVision,

			TracksProbed: len(p.AudioLanguages) > 0 || len(p.SubtitleLanguages) > 0 ||
				p.AudioStreams > 0 || p.SubtitleStreams > 0,
			AudioLanguages:    p.AudioLanguages,
			SubtitleLanguages: p.SubtitleLanguages,
			AudioStreams:      max(p.AudioStreams, len(p.AudioLanguages)),
			SubtitleStreams:   max(p.SubtitleStreams, len(p.SubtitleLanguages)),
		}
	}
	switch strings.ToLower(strings.TrimSpace(e.AvailStatus)) {
	case "available":
		out.Avail.Status = triage.AvailAvailable
	case "unavailable":
		out.Avail.Status = triage.AvailUnavailable
	default:
		out.Avail.Status = triage.AvailUnknown
	}
	out.Avail.OnMyBackbone = e.AvailOnMyBackbone
	if out.Avail.Known() && e.AvailCheckedDays >= 0 {
		out.Avail.CheckedAt = time.Now().AddDate(0, 0, -e.AvailCheckedDays)
	}
	return out
}

type explainResponse struct {
	Profile string                 `json:"profile"`
	Results []*ranking.Explanation `json:"results"`
	// Aggregates are the request's result-set conditions — exists(), none(),
	// count() — with what each counted and which releases it counted. They are
	// set-wide, so they travel beside the per-release results rather than
	// inside any of them.
	Aggregates []rules.AggregateReport `json:"aggregates,omitempty"`
}

// handleRankingExplain scores titles against a profile and returns the
// per-clause breakdown behind each score, including why a release was rejected.
func (s *Server) handleRankingExplain(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	// Filter profiles are configuration, and evaluating one compiles patterns
	// the caller supplies, so this is admin-only like the rest of the config
	// endpoints.
	adminUsername := s.adminUsername()
	if stream, _ := auth.StreamFromContext(r); stream == nil || stream.Username != adminUsername {
		http.Error(w, "Only admin can evaluate filter profiles", http.StatusForbidden)
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
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	explainReq := ranking.Request{
		Kind:    kind,
		IsAnime: kind == ranking.KindAnimeMovie || kind == ranking.KindAnimeShow,
		Title:   strings.TrimSpace(req.TargetTitle),
		Seadex:  req.Sample.toSeadex(),
		Sample:  req.Sample.toSample(),
	}
	results, aggregates := profile.Explain(titles, explainReq, opts)

	writeJSON(w, http.StatusOK, explainResponse{Profile: profile.Name, Results: results, Aggregates: aggregates})
}

func writeExplainError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// explainProfile compiles the posted profile definition, or looks up a saved
// one by name when no definition was sent.
func (s *Server) explainProfile(req explainRequest) (*ranking.Profile, error) {
	fp := req.Profile
	name := strings.TrimSpace(req.ProfileName)
	if fp == nil && name == "" {
		return nil, errNoProfile
	}
	// Copy while holding the lock: a config save replaces the whole slices,
	// so a pointer into them would outlive what it points at. The slice
	// header itself is safe to keep — a save never mutates the old backing
	// array. Libraries always come from the saved config, so a previewed
	// profile resolves matched() against the same defines a saved one would.
	var libraries []config.DefineLibraryConfig
	s.mu.RLock()
	if s.config != nil {
		libraries = s.config.DefineLibraries
		if fp == nil {
			for i := range s.config.FilterProfiles {
				if strings.EqualFold(s.config.FilterProfiles[i].Name, name) {
					found := s.config.FilterProfiles[i]
					fp = &found
					break
				}
			}
		}
	}
	s.mu.RUnlock()
	if fp == nil {
		return nil, errUnknownProfile
	}

	// Compile in isolation: this is a preview, so it must never disturb the
	// rankers the addon is serving from.
	svc := ranking.NewService()
	if errs := svc.Reload(&config.Config{
		FilterProfiles:  []config.FilterProfileConfig{*fp},
		DefineLibraries: libraries,
	}); len(errs) > 0 {
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
