package ranking

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/dreulavelle/jhin/rank"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/diag"
	"streamnzb/pkg/search/rules"
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
	// rules are the profile's named conditions, compiled once.
	rules *rules.Set
	// minRank is the score floor. It is applied here rather than left to jhin
	// because jhin only ever sees what the title earned: the NZB attributes,
	// the library bonus and the rules all pay out afterwards, and a floor that
	// judges a partial score rejects releases whose final score clears it.
	minRank int
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
	// A profile is a preset plus rules. Ranking is only ever set on a config
	// that has not been migrated yet, and the legacy fields only on one that
	// predates jhin entirely; both are honoured so an in-memory profile built
	// by a test or by the preview behaves like the stored one would.
	var spec rank.Profile
	switch {
	case fp.Ranking != nil:
		spec = *fp.Ranking
	case fp.Preset == "" && fp.HasLegacyFields():
		spec = config.Synthesize(fp)
	default:
		spec = config.PresetSpec(fp.Preset)
	}
	spec.Name = fp.Name

	// jhin's own floor is disabled so that ours can judge the finished score.
	// The spec kept on the profile is the one the user configured; only the
	// copy handed to the ranker is altered.
	rankSpec := spec
	rankSpec.Options.MinRank = math.MinInt

	ranker, err := rank.New(rankSpec)
	if err != nil {
		return nil, err
	}
	ruleSet, err := rules.Compile(fp.Rules)
	if err != nil {
		return nil, err
	}
	// Size scoring comes from the preset. A profile only carries its own on a
	// config that predates presets, and the migration clears it once the
	// preset owns it.
	scoring := fp.Scoring
	if scoring == nil {
		scoring = config.PresetScoring(fp.Preset)
	}
	return &Profile{
		Name:              fp.Name,
		Ranker:            ranker,
		Spec:              spec,
		LibraryScoreBonus: fp.EffectiveLibraryScoreBonus(),
		limits:            fp.Limits,
		scoring:           scoring,
		blockPassworded:   fp.EffectiveBlockPassworded(),
		rules:             ruleSet,
		minRank:           spec.Options.MinRank,
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

// Result is one release after the profile has judged it.
type Result struct {
	Candidate triage.Candidate
	Torrent   rank.Torrent
	// Matched are the profile's named rules that paid out on this release.
	Matched []triage.RuleMatch
	// limits are the caps this release counts against, resolved after sorting.
	limits []rules.LimitMatch
}

// Request is what a profile needs to know about the search behind a result
// set: which content kind it resolved to, and the media context rules can read.
// Kind alone selects the NZB attribute limits and scoring; the rest is only
// visible to rules.
type Request struct {
	Kind    string
	IsAnime bool
	Season  int
	Episode int
	Title   string
}

// Apply runs every candidate through the profile's ranker, drops the ones it
// rejects, and returns them in the profile's sort order. Candidates are always
// evaluated so that rejected releases can still be explained in the UI.
func (p *Profile) Apply(req Request, candidates []triage.Candidate, opts rank.RankOptions) []Result {
	kept, _ := p.ApplyWithRejected(req, candidates, opts)
	return kept
}

// ApplyWithRejected is Apply plus the releases the profile turned away, each
// still carrying the reasons jhin refused it. A profile that filters a result
// set down to nothing looks identical to an indexer that found nothing unless
// the rejections survive this call, so diagnostics use this form.
//
// Between judging and sorting the profile applies everything jhin cannot see,
// in the order the score is built up: the NZB attribute limits reject, the
// attribute scoring and the library bonus add points, the rules add more and
// may reject, and only then does the score floor judge the finished number.
//
// The order is the point. Every one of those stages used to run somewhere
// downstream of the floor and of the sort, which meant the floor rejected on a
// partial score and the ordering was overwritten by whoever sorted last.
func (p *Profile) ApplyWithRejected(req Request, candidates []triage.Candidate, opts rank.RankOptions) (kept, rejected []Result) {
	results := p.Evaluate(candidates, opts)
	p.applyLimits(req.Kind, results)
	p.applyResolutionScore(results)
	p.applyScoring(req.Kind, results)
	p.applyLibraryBonus(results)
	p.applyRules(req, results)
	p.applyMinRank(results)
	p.recordVerdicts(req, results)

	kept = results[:0]
	for _, r := range results {
		if r.Torrent.Fetch {
			kept = append(kept, r)
		} else {
			rejected = append(rejected, r)
		}
	}
	p.SortResults(kept)
	// Caps are the last word, and they have to be: "keep the best three" is
	// only meaningful once every point is in and the list is in order.
	kept, capped := applyCaps(kept)
	rejected = append(rejected, capped...)
	return kept, rejected
}

// applyCaps walks the sorted list and drops what falls past a limit rule's
// count. Rules are counted independently, and a release already dropped by one
// cap does not count against another — it is gone, so it is not taking a slot.
func applyCaps(sorted []Result) (kept, dropped []Result) {
	seen := map[string]int{}
	kept = sorted[:0]
	for _, r := range sorted {
		over := ""
		for _, limit := range r.limits {
			if seen[limit.Name] >= limit.Count {
				over = fmt.Sprintf("%s%s (over the limit of %d)", diag.RuleRejectionPrefix, limit.Name, limit.Count)
				break
			}
		}
		if over != "" {
			r.Torrent.Fetch = false
			r.Torrent.Rejections = append(r.Torrent.Rejections, over)
			r.Candidate.Verdict.Rejections = r.Torrent.Rejections
			dropped = append(dropped, r)
			continue
		}
		for _, limit := range r.limits {
			seen[limit.Name]++
		}
		kept = append(kept, r)
	}
	return kept, dropped
}

// applyLibraryBonus lifts releases already in the library so proven-playable
// results outrank fresh indexer hits. It runs before the floor and before the
// sort so that the bonus counts towards both, which is what a user setting it
// expects and what putting it downstream of either quietly prevented.
func (p *Profile) applyLibraryBonus(results []Result) {
	if p == nil || p.LibraryScoreBonus == 0 {
		return
	}
	for i := range results {
		rel := results[i].Candidate.Release
		if rel != nil && rel.IsLibraryResult() {
			results[i].Torrent.Rank += p.LibraryScoreBonus
		}
	}
}

// applyRules evaluates the profile's named conditions. Rules see everything
// earlier stages know plus the two tiers nothing else in the profile can read:
// what ffprobe measured about a library file, and what the availability
// database reports.
func (p *Profile) applyRules(req Request, results []Result) {
	if p == nil || p.rules.Len() == 0 {
		return
	}
	ctx := rules.Context{
		Kind:     req.Kind,
		IsAnime:  req.IsAnime,
		Season:   req.Season,
		Episode:  req.Episode,
		Title:    req.Title,
		Episodic: req.Kind == KindSeries || req.Kind == KindAnimeShow,
	}
	for i := range results {
		r := &results[i]
		env := rules.BuildEnv(r.Candidate, r.Torrent.Data, ctx)
		outcome := p.rules.Evaluate(env, req.Kind)
		r.Torrent.Rank += outcome.Points
		r.Matched = outcome.Matched
		r.limits = outcome.Limits
		if len(outcome.Rejections) > 0 {
			r.Torrent.Fetch = false
			r.Torrent.Rejections = append(r.Torrent.Rejections, outcome.Rejections...)
		}
	}
}

// applyMinRank enforces the profile's score floor on the finished score.
func (p *Profile) applyMinRank(results []Result) {
	if p == nil || p.minRank == math.MinInt {
		return
	}
	for i := range results {
		r := &results[i]
		if r.Torrent.Rank >= p.minRank {
			continue
		}
		r.Torrent.Fetch = false
		r.Torrent.Rejections = append(r.Torrent.Rejections,
			fmt.Sprintf("score %d below minimum %d", r.Torrent.Rank, p.minRank))
	}
}

// recordVerdicts copies the profile's reasoning onto each candidate, so that
// formatting and diagnostics downstream read what the profile decided instead
// of inferring it from a score.
func (p *Profile) recordVerdicts(req Request, results []Result) {
	for i := range results {
		r := &results[i]
		r.Candidate.Verdict.Kind = req.Kind
		r.Candidate.Verdict.IsAnime = req.IsAnime
		r.Candidate.Verdict.Matched = r.Matched
		r.Candidate.Verdict.Rejections = r.Torrent.Rejections
	}
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

// SortResults orders results in place by score (Rank), and by nothing else.
//
// Score is the only ordering currency there is. Resolution used to sort ahead
// of it as a hard tier, which meant a release could never be lifted past a
// higher-resolution one however many points it earned — a rule paying 80000 to
// prefer a language sorted its releases first among their own tier and behind
// every 4K release, which is not what a number that large can be read to mean.
//
// Nothing is lost by dropping the bracket, because resolution is paid for in
// points instead — see ResolutionTierPoints, which is wider than the whole
// spread of the baseline's other scores. 4K still leads a list nobody has
// written a rule about. The difference is that a rule can now outbid it, which
// is the point of letting a rule name its own number.
func (p *Profile) SortResults(results []Result) {
	if len(results) <= 1 {
		return
	}
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Torrent.Rank > results[j].Torrent.Rank
	})
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
	// Matched are the profile's named rules that paid out.
	Matched []triage.RuleMatch `json:"matched,omitempty"`
	// Limited names the limit rules this release counts against, whether or
	// not it survived them. A cap that matched five releases and dropped two
	// did something; reporting only the drops would make a cap with room to
	// spare look like a rule that never fires.
	Limited []string `json:"limited,omitempty"`
	// SkippedRules are rules that could not be judged from a title alone,
	// each with the reason. A bench that silently omitted them would make a
	// rule reading probed.* or avail.* look broken.
	SkippedRules []string `json:"skipped_rules,omitempty"`
}

// Explain runs a set of release names through the whole profile and reports
// what it did to each — the same call the live pipeline makes, not an
// approximation of it.
//
// It judges the set rather than each name alone because parts of a profile only
// mean anything against a set: a limit rule caps the tail, and "the tail"
// requires knowing the finished order. A preview that evaluated one title at a
// time would show a cap as never firing.
//
// A bare name carries no NZB, no probe and no availability record, so rules
// reading those tiers report as skipped rather than being judged against
// nothing — the same fail-open behaviour they have in a real search, made
// visible.
func (p *Profile) Explain(titles []string, req Request, opts rank.RankOptions) []*Explanation {
	if p == nil || p.Ranker == nil || len(titles) == 0 {
		return nil
	}

	candidates := make([]triage.Candidate, 0, len(titles))
	for _, title := range titles {
		candidates = append(candidates, triage.Candidate{Release: &release.Release{Title: title}})
	}

	kept, rejected := p.ApplyWithRejected(req, candidates, opts)

	// Rejected releases are explained too: "why did this not show up" is the
	// question the preview exists to answer.
	out := make([]*Explanation, 0, len(kept)+len(rejected))
	for _, group := range [][]Result{kept, rejected} {
		for i := range group {
			out = append(out, p.explainResult(&group[i], req))
		}
	}
	return out
}

// explainResult renders one judged release. Rule contributions are appended to
// the ranker's own so the breakdown adds up to the score shown.
func (p *Profile) explainResult(r *Result, req Request) *Explanation {
	title := ""
	if r.Candidate.Release != nil {
		title = r.Candidate.Release.Title
	}
	out := &Explanation{
		Title:         title,
		Rank:          r.Torrent.Rank,
		Fetch:         r.Torrent.Fetch,
		Rejections:    r.Torrent.Rejections,
		Resolution:    string(r.Torrent.Resolution()),
		TitleRatio:    r.Torrent.TitleRatio,
		Contributions: p.Ranker.Explain(&r.Torrent),
		Parsed:        r.Torrent.Data,
		Matched:       r.Matched,
	}
	for _, limit := range r.limits {
		out.Limited = append(out.Limited, limit.Name)
	}
	for _, m := range r.Matched {
		out.Contributions = append(out.Contributions, rank.Contribution{Source: "rule:" + m.Name, Rank: m.Score})
	}
	if p.rules.Len() > 0 {
		env := rules.BuildEnv(r.Candidate, r.Torrent.Data, rules.Context{
			Kind:     req.Kind,
			IsAnime:  req.IsAnime,
			Season:   req.Season,
			Episode:  req.Episode,
			Title:    req.Title,
			Episodic: req.Kind == KindSeries || req.Kind == KindAnimeShow,
		})
		out.SkippedRules = p.rules.Evaluate(env, req.Kind).Skipped
	}
	return out
}
