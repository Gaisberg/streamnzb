package ranking

import (
	"sort"
	"strings"
	"sync"

	"github.com/dreulavelle/jhin/rank"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/search/triage"
)

// Content kinds a profile can be bound to. They partition every request:
// anime is split from live action, and each side into films and episodic, so a
// request always lands in exactly one kind and selection is never ambiguous.
const (
	KindMovie      = "movie"
	KindSeries     = "series"
	KindAnimeMovie = "anime_movie"
	KindAnimeShow  = "anime_show"
)

// Kind classifies a request. Anime is recognised when the request resolved via
// Kitsu, which also reports whether the entry is a film; everything else falls
// back to the Stremio content type.
func Kind(contentType, kitsuShowType string, isAnime bool) string {
	isMovie := strings.EqualFold(strings.TrimSpace(contentType), "movie")
	if isAnime {
		// Kitsu reports TV, movie, OVA, ONA, special or music. Only "movie" is
		// a film; the rest are episodic.
		if strings.EqualFold(strings.TrimSpace(kitsuShowType), "movie") {
			return KindAnimeMovie
		}
		if strings.TrimSpace(kitsuShowType) == "" && isMovie {
			return KindAnimeMovie
		}
		return KindAnimeShow
	}
	if isMovie {
		return KindMovie
	}
	return KindSeries
}

// Service compiles filter profiles into jhin rankers and keeps them until the
// config changes. rank.New compiles every pattern in the profile, so it must
// not run per request.
type Service struct {
	mu       sync.RWMutex
	profiles map[string]*Profile
}

// Profile is a compiled filter profile: the jhin ranker plus the content
// kinds it applies to.
type Profile struct {
	Name   string
	Ranker *rank.Ranker
	Spec   rank.Profile
	// LibraryScoreBonus is added to cached library releases on top of the
	// ranker's score. Resolved from the profile config (default 500, 0 when
	// disabled there).
	LibraryScoreBonus int

	// limits bounds releases by NZB attributes (size, age, grabs), keyed by
	// content kind; resolved per request in applyLimits since the kind is not
	// known at compile time.
	limits map[string]*config.LimitsConfig
	// scoring adds points for those same attributes, keyed the same way and
	// resolved per request for the same reason.
	scoring map[string]*config.ScoringConfig
	// blockPassworded rejects releases the indexer flags as password protected.
	blockPassworded bool
}

func NewService() *Service {
	return &Service{profiles: map[string]*Profile{}}
}

// Reload recompiles every profile in cfg. Profiles that fail to compile are
// reported and skipped rather than taking the whole config down.
func (s *Service) Reload(cfg *config.Config) []error {
	if cfg == nil {
		return nil
	}
	compiled := make(map[string]*Profile, len(cfg.FilterProfiles))
	var errs []error

	for _, fp := range cfg.FilterProfiles {
		profile, err := Compile(fp)
		if err != nil {
			errs = append(errs, &ProfileError{Name: fp.Name, Err: err})
			continue
		}
		compiled[strings.ToLower(strings.TrimSpace(fp.Name))] = profile
	}

	s.mu.Lock()
	s.profiles = compiled
	s.mu.Unlock()
	return errs
}

// Compile builds the jhin ranker for one profile config. Config validation
// uses it too, so a pattern that cannot compile is rejected at save time with
// the same error Reload would report.
func Compile(fp config.FilterProfileConfig) (*Profile, error) {
	// Ranking is materialized at config load; synthesizing here is only a
	// guard for profiles built in memory (tests, the explain preview).
	spec := rank.Default()
	if fp.Ranking != nil {
		spec = *fp.Ranking
	} else {
		spec = config.Synthesize(fp)
	}
	spec.Name = fp.Name

	ranker, err := rank.New(spec)
	if err != nil {
		return nil, err
	}
	return &Profile{
		Name:              fp.Name,
		Ranker:            ranker,
		Spec:              spec,
		LibraryScoreBonus: fp.EffectiveLibraryScoreBonus(),
		limits:            fp.Limits,
		scoring:           fp.Scoring,
		blockPassworded:   fp.EffectiveBlockPassworded(),
	}, nil
}

type ProfileError struct {
	Name string
	Err  error
}

func (e *ProfileError) Error() string { return "filter profile " + e.Name + ": " + e.Err.Error() }
func (e *ProfileError) Unwrap() error { return e.Err }

// Get returns the compiled profile by name.
func (s *Service) Get(name string) (*Profile, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.profiles[key]
	return p, ok
}

// SelectName resolves which profile a request should use: the binding for its
// kind, or the stream default when that kind has none. Exactly one profile
// applies to any request; profiles never combine.
func SelectName(byKind map[string]string, fallback, kind string) string {
	if name := strings.TrimSpace(byKind[kind]); name != "" {
		return name
	}
	return strings.TrimSpace(fallback)
}

// Result is one release after jhin has judged it.
type Result struct {
	Candidate triage.Candidate
	Torrent   rank.Torrent
}

// Apply runs every candidate through the profile's ranker, drops the ones jhin
// rejects, and returns them in the profile's sort order. Candidates are always
// evaluated so that rejected releases can still be explained in the UI. kind
// selects which of the profile's NZB attribute limits and scoring apply; empty
// applies the default entries only.
func (p *Profile) Apply(kind string, candidates []triage.Candidate, opts rank.RankOptions) []Result {
	kept, _ := p.ApplyWithRejected(kind, candidates, opts)
	return kept
}

// ApplyWithRejected is Apply plus the releases the profile turned away, each
// still carrying the reasons jhin refused it. A profile that filters a result
// set down to nothing looks identical to an indexer that found nothing unless
// the rejections survive this call, so diagnostics use this form.
//
// Between judging and sorting, the profile's NZB attribute limits reject and
// its attribute scoring re-ranks — neither is anything jhin can see, since both
// read the NZB rather than the title.
func (p *Profile) ApplyWithRejected(kind string, candidates []triage.Candidate, opts rank.RankOptions) (kept, rejected []Result) {
	results := p.Evaluate(candidates, opts)
	p.applyLimits(kind, results)
	p.applyScoring(kind, results)

	kept = results[:0]
	for _, r := range results {
		if r.Torrent.Fetch {
			kept = append(kept, r)
		} else {
			rejected = append(rejected, r)
		}
	}
	p.SortResults(kept)
	return kept, rejected
}

// Evaluate ranks every candidate without dropping anything.
func (p *Profile) Evaluate(candidates []triage.Candidate, opts rank.RankOptions) []Result {
	if p == nil || p.Ranker == nil || len(candidates) == 0 {
		return nil
	}
	entries := make([]rank.Entry, 0, len(candidates))
	for _, c := range candidates {
		title := ""
		if c.Release != nil {
			title = c.Release.Title
		}
		entries = append(entries, rank.Entry{Title: title})
	}

	torrents := p.Ranker.RankEntries(entries, opts)
	out := make([]Result, len(candidates))
	for i := range candidates {
		out[i] = Result{Candidate: candidates[i], Torrent: torrents[i]}
	}
	return out
}

// SortResults orders results in place by resolution precedence, then by jhin score (Rank).
func (p *Profile) SortResults(results []Result) {
	if len(results) <= 1 {
		return
	}
	precedence := p.resolutionPrecedence()

	sort.SliceStable(results, func(i, j int) bool {
		resI := precedence[results[i].Torrent.Resolution()]
		resJ := precedence[results[j].Torrent.Resolution()]
		if resI != resJ {
			return resI > resJ
		}
		return results[i].Torrent.Rank > results[j].Torrent.Rank
	})
}

// resolutionPrecedence honors the profile's ResolutionOrder when it sets one,
// so "prefer 1080p over 4K" works without banning 4K.
func (p *Profile) resolutionPrecedence() map[rank.Resolution]int {
	out := map[rank.Resolution]int{
		rank.Res2160p: 9, rank.Res1440p: 8, rank.Res1080p: 7, rank.Res720p: 6,
		rank.Res576p: 5, rank.Res480p: 4, rank.Res360p: 3, rank.Res240p: 2,
		rank.ResUnknown: 1,
	}
	for i, res := range p.Spec.ResolutionOrder {
		out[res] = 1000 - i
	}
	return out
}

// Explanation is the per-clause breakdown of one release's score, plus the
// reasons it was rejected when it was.
type Explanation struct {
	Title         string              `json:"title"`
	Rank          int                 `json:"rank"`
	Fetch         bool                `json:"fetch"`
	Rejections    []string            `json:"rejections,omitempty"`
	Resolution    string              `json:"resolution"`
	TitleRatio    float64             `json:"title_ratio,omitempty"`
	Contributions []rank.Contribution `json:"contributions"`
	Parsed        any                 `json:"parsed,omitempty"`
}

// Explain ranks a single title and returns why it scored what it did.
func (p *Profile) Explain(title string, opts rank.RankOptions) *Explanation {
	if p == nil || p.Ranker == nil {
		return nil
	}
	t := p.Ranker.Rank(title, opts)
	return &Explanation{
		Title:         title,
		Rank:          t.Rank,
		Fetch:         t.Fetch,
		Rejections:    t.Rejections,
		Resolution:    string(t.Resolution()),
		TitleRatio:    t.TitleRatio,
		Contributions: p.Ranker.Explain(&t),
		Parsed:        t.Data,
	}
}
