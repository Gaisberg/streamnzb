package config

import (
	"sort"
	"strings"
)

// A search request is a plan: an ordered list of attempts, one rule for when to
// stop, and one statement of what counts as a match.
//
// This replaces a bag of interacting flags (search mode × series scope × year ×
// absolute supplement, each meaning something different depending on the
// others) with the thing those flags were really describing — the sequence of
// questions to ask indexers. An attempt says how to address the content and
// what granularity to ask for; nothing downstream re-derives either.
const (
	// SearchAddressID asks by database id (IMDb/TVDB/TMDB/Kitsu — whichever
	// the indexer supports, picked from its caps at dispatch).
	SearchAddressID = "id"
	// SearchAddressTitle asks by title text.
	SearchAddressTitle = "title"

	// SearchTargetEpisode asks for one episode, SearchTargetSeason for the
	// whole season, SearchTargetSeries for the series alone (title or id, no
	// season or episode), and SearchTargetAbsolute for the anime absolute
	// episode number ("One Piece 63"). Movie plans carry no target.
	SearchTargetEpisode  = "episode"
	SearchTargetSeason   = "season"
	SearchTargetSeries   = "series"
	SearchTargetAbsolute = "absolute"

	// SearchStopFirstHit stops the plan at the first attempt that matched
	// anything; SearchStopAll runs every attempt and merges the results.
	SearchStopFirstHit = "first_hit"
	SearchStopAll      = "all"

	// SearchOrderAsListed runs the attempts in the order they are written.
	// SearchOrderAdaptiveSeason leads with the season attempts once every
	// episode of the requested season has aired — a finished season is where
	// the season pack lives — and otherwise leaves the order alone.
	SearchOrderAsListed       = "as_listed"
	SearchOrderAdaptiveSeason = "adaptive_season"
)

// SearchAttempt is one question to ask indexers. It is fully specified: the
// executor stamps it onto a request and dispatches, with nothing left to infer.
type SearchAttempt struct {
	Address string `json:"address"`
	// Target is the granularity asked for, on series plans only.
	Target string `json:"target,omitempty"`
	// Title is the metadata language the query text is built from, on title
	// attempts only. Empty means the original-language title.
	Title *string `json:"title,omitempty"`
	// Year puts the metadata year in the query text. Title attempts only — an
	// id attempt names an id, and carries no year. Whether a release's year
	// has to match is Acceptance.Year, a separate question.
	Year *bool `json:"year,omitempty"`
}

// SearchAcceptance is what counts as a match, and applies to every attempt in
// the plan. It is deliberately separate from the attempts: what goes out and
// what may come back are different questions, and conflating them is what made
// "title language" mean two things depending on the search mode.
type SearchAcceptance struct {
	// Titles are the metadata languages a release name may match to prove it
	// is the right content. Empty trusts whatever the indexer answered.
	Titles []string `json:"titles,omitempty"`
	// Year requires the release year to be within a year of the metadata's.
	Year *bool `json:"year,omitempty"`
	// Packs accepts a season or complete-series pack that contains the
	// requested episode. Nil is enabled.
	Packs *bool `json:"packs,omitempty"`
}

// PacksEnabled reports whether packs containing the episode are accepted.
func (a *SearchAcceptance) PacksEnabled() bool {
	if a == nil || a.Packs == nil {
		return true
	}
	return *a.Packs
}

// YearEnforced reports whether a release's year has to match.
func (a *SearchAcceptance) YearEnforced() bool {
	return a != nil && a.Year != nil && *a.Year
}

// AcceptTitles is the acceptance title languages, normalized.
func (a *SearchAcceptance) AcceptTitles() []string {
	if a == nil {
		return nil
	}
	return NormalizeSearchTitleLanguages(a.Titles)
}

// NormalizeSearchAddress maps a raw address to "id" or "title"; anything else
// is a title search, which is what an unset search mode has always been.
func NormalizeSearchAddress(address string) string {
	if strings.EqualFold(strings.TrimSpace(address), SearchAddressID) {
		return SearchAddressID
	}
	return SearchAddressTitle
}

// NormalizeSearchTarget maps a raw target to one of the four; anything else is
// the episode, the narrowest and the default.
func NormalizeSearchTarget(target string) string {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case SearchTargetSeason:
		return SearchTargetSeason
	case SearchTargetSeries:
		return SearchTargetSeries
	case SearchTargetAbsolute:
		return SearchTargetAbsolute
	}
	return SearchTargetEpisode
}

func NormalizeSearchStop(stop string) string {
	if strings.EqualFold(strings.TrimSpace(stop), SearchStopAll) {
		return SearchStopAll
	}
	return SearchStopFirstHit
}

func NormalizeSearchOrder(order string) string {
	if strings.EqualFold(strings.TrimSpace(order), SearchOrderAdaptiveSeason) {
		return SearchOrderAdaptiveSeason
	}
	return SearchOrderAsListed
}

// TitleLanguage is the attempt's query language, and "" for an id attempt,
// which builds no query text.
func (a SearchAttempt) TitleLanguage() string {
	if NormalizeSearchAddress(a.Address) == SearchAddressID {
		return ""
	}
	if a.Title == nil {
		return ""
	}
	return NormalizeSearchTitleLanguage(*a.Title)
}

// YearInQuery reports whether the attempt puts the year in its query text.
func (a SearchAttempt) YearInQuery() bool {
	if NormalizeSearchAddress(a.Address) == SearchAddressID {
		return false
	}
	return a.Year != nil && *a.Year
}

// Normalized is the attempt with every field settled, so an attempt that
// reaches the executor never needs interpreting.
func (a SearchAttempt) Normalized(isSeries bool) SearchAttempt {
	out := SearchAttempt{Address: NormalizeSearchAddress(a.Address)}
	if isSeries {
		out.Target = NormalizeSearchTarget(a.Target)
	}
	if out.Address == SearchAddressTitle {
		language := a.TitleLanguage()
		out.Title = &language
		if a.Year != nil && *a.Year {
			year := true
			out.Year = &year
		}
	}
	return out
}

// Label names the attempt for logs and the UI funnel ("id·episode").
func (a SearchAttempt) Label() string {
	address := NormalizeSearchAddress(a.Address)
	if a.Target == "" {
		return address
	}
	return address + "·" + NormalizeSearchTarget(a.Target)
}

// NormalizeSearchAttempts settles every attempt and drops exact duplicates —
// two identical attempts would be one wasted indexer round trip, never a
// fallback.
func NormalizeSearchAttempts(attempts []SearchAttempt, isSeries bool) []SearchAttempt {
	if len(attempts) == 0 {
		return nil
	}
	out := make([]SearchAttempt, 0, len(attempts))
	seen := make(map[string]bool, len(attempts))
	for _, attempt := range attempts {
		normalized := attempt.Normalized(isSeries)
		key := normalized.Label() + "|" + normalized.TitleLanguage()
		if normalized.YearInQuery() {
			key += "|year"
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, normalized)
	}
	return out
}

// SearchPlanContext is what the executor knows about one request that the plan
// itself cannot: whether there is a season and episode to aim at, whether the
// content is anime, and whether the season has finished airing.
type SearchPlanContext struct {
	IsSeries        bool
	HasSeason       bool
	HasEpisode      bool
	IsAnime         bool
	HasAbsolute     bool
	SeasonCompleted bool
}

// SearchPlanAttempts is the attempts to actually run, in order.
//
// It drops what this request cannot use rather than dispatching a query that
// cannot match: an absolute attempt for non-anime, an episode attempt with no
// episode. It also collapses attempts that would ask the same question — with
// no episode number an episode attempt *is* a season attempt — because a
// duplicate round trip is not a fallback.
func (sq *SearchQueryConfig) SearchPlanAttempts(ctx SearchPlanContext) []SearchAttempt {
	if sq == nil {
		return nil
	}
	attempts := NormalizeSearchAttempts(sq.Attempts, ctx.IsSeries)
	if len(attempts) == 0 {
		return nil
	}
	usable := make([]SearchAttempt, 0, len(attempts))
	for _, attempt := range attempts {
		if !ctx.IsSeries {
			usable = append(usable, attempt)
			continue
		}
		if attempt.Target == SearchTargetAbsolute {
			// The absolute number is how anime is named, and nothing else.
			if !ctx.IsAnime || !ctx.HasAbsolute || attempt.Address == SearchAddressID {
				continue
			}
			usable = append(usable, attempt)
			continue
		}
		// Collapse in one direction, narrowest first: without an episode
		// number an episode attempt asks exactly what a season attempt asks,
		// and without a season number that asks what the series attempt does.
		if attempt.Target == SearchTargetEpisode && !ctx.HasEpisode {
			attempt.Target = SearchTargetSeason
		}
		if attempt.Target == SearchTargetSeason && !ctx.HasSeason {
			attempt.Target = SearchTargetSeries
		}
		usable = append(usable, attempt)
	}
	// Collapsing above can produce twins, so settle the list once more.
	usable = NormalizeSearchAttempts(usable, ctx.IsSeries)
	if ctx.IsSeries && NormalizeSearchOrder(sq.Order) == SearchOrderAdaptiveSeason && ctx.SeasonCompleted {
		sort.SliceStable(usable, func(i, j int) bool {
			return seasonFirstRank(usable[i]) < seasonFirstRank(usable[j])
		})
	}
	return usable
}

// seasonFirstRank puts season attempts ahead of everything else for a season
// that has finished airing. Stable sorting keeps the listed order within each
// group, so the plan is reordered rather than rewritten.
func seasonFirstRank(attempt SearchAttempt) int {
	if attempt.Target == SearchTargetSeason {
		return 0
	}
	return 1
}

// StopsAtFirstHit reports whether the plan stops at the first attempt that
// matched anything.
func (sq *SearchQueryConfig) StopsAtFirstHit() bool {
	return sq != nil && NormalizeSearchStop(sq.Stop) == SearchStopFirstHit
}

// Acceptance is the plan's acceptance with defaults applied.
func (sq *SearchQueryConfig) Acceptance() *SearchAcceptance {
	if sq == nil || sq.Accept == nil {
		return &SearchAcceptance{}
	}
	return sq.Accept
}

// RunsAbsoluteAttempt reports whether the plan asks by absolute episode number
// anywhere. It is what decides whether the absolute number is resolved at all,
// and resolving it also lets acceptance recognise absolute-numbered releases
// whichever attempt surfaced them.
func (sq *SearchQueryConfig) RunsAbsoluteAttempt() bool {
	if sq == nil {
		return false
	}
	for _, attempt := range sq.Attempts {
		if NormalizeSearchTarget(attempt.Target) == SearchTargetAbsolute &&
			NormalizeSearchAddress(attempt.Address) == SearchAddressTitle {
			return true
		}
	}
	return false
}

// The stock plans. Both are ordered narrowest-first and stop at the first
// attempt that matches, so a request costs one indexer round trip when the
// precise question answers and widens only when it does not.
//
// Movie plans carry the year: movie releases are named with one. TV plans do
// not — scene TV releases are named "Title.S01E01.1080p...", so a year token
// narrows the query to nothing — and they accept packs, because a pack that
// contains the episode is a playable answer.

// DefaultMoviePlan is the stock movie plan: ask by id, fall back to the title.
func DefaultMoviePlan(name string) SearchQueryConfig {
	return SearchQueryConfig{
		Name: name,
		Attempts: []SearchAttempt{
			{Address: SearchAddressID},
			{Address: SearchAddressTitle, Title: ptrString("en-US"), Year: ptrBool(true)},
		},
		Stop: SearchStopFirstHit,
		Accept: &SearchAcceptance{
			Titles: DefaultIDSearchTitleLanguages(),
			Year:   ptrBool(true),
		},
	}
}

// DefaultTVPlan is the stock TV plan. It asks for the episode both ways before
// settling for a season pack, keeps the anime absolute number as an early
// attempt (skipped outright for non-anime), and reorders itself to lead with
// the season once the season has finished airing.
func DefaultTVPlan(name string) SearchQueryConfig {
	return SearchQueryConfig{
		Name: name,
		Attempts: []SearchAttempt{
			{Address: SearchAddressID, Target: SearchTargetEpisode},
			{Address: SearchAddressTitle, Target: SearchTargetAbsolute, Title: ptrString("en-US")},
			{Address: SearchAddressTitle, Target: SearchTargetEpisode, Title: ptrString("en-US")},
			{Address: SearchAddressID, Target: SearchTargetSeason},
			{Address: SearchAddressTitle, Target: SearchTargetSeason, Title: ptrString("en-US")},
		},
		Stop:  SearchStopFirstHit,
		Order: SearchOrderAdaptiveSeason,
		Accept: &SearchAcceptance{
			Titles: DefaultIDSearchTitleLanguages(),
			Year:   ptrBool(false),
			Packs:  ptrBool(true),
		},
	}
}

// migrateSearchPlan builds a plan from the pre-plan schema — search mode ×
// series scope × year × absolute supplement — and clears it.
//
// The mapping is faithful, including the two adaptive settings that shipped
// just before the plan model: they were an ordered pair of concrete settings
// each, which is what an attempt list says outright.
func migrateSearchPlan(query *SearchQueryConfig, isSeries bool) bool {
	if query == nil || len(query.Attempts) > 0 {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(query.LegacySearchMode))
	if mode == "" && query.LegacyUseSeasonEpisodeParams != nil {
		mode = SearchModeID
	}
	addresses := []string{SearchAddressTitle}
	switch mode {
	case SearchModeID:
		addresses = []string{SearchAddressID}
	case SearchModeDynamic:
		addresses = []string{SearchAddressID, SearchAddressTitle}
	}

	targets := []string(nil)
	if isSeries {
		switch legacyScope(query.LegacySeriesSearchScope) {
		case SeriesSearchScopeSeason:
			targets = []string{SearchTargetSeason}
		case SeriesSearchScopeNone:
			targets = []string{SearchTargetSeries}
		case SeriesSearchScopeEpisodeThenSeason, SeriesSearchScopeDynamic:
			targets = []string{SearchTargetEpisode, SearchTargetSeason}
		default:
			targets = []string{SearchTargetEpisode}
		}
	} else {
		targets = []string{""}
	}

	language := NormalizeSearchTitleLanguage(query.LegacySearchTitleLanguage)
	includeYear := isSeries == false
	if query.LegacyIncludeYear != nil {
		includeYear = *query.LegacyIncludeYear
	} else if query.LegacyIncludeYearInText != nil {
		includeYear = *query.LegacyIncludeYearInText
	} else if mode == SearchModeID {
		includeYear = false
	}

	// Scope outer, address inner: the old executor ran the pair of scopes as
	// the outer loop, because widening the scope changes what a hit is while
	// changing the address only changes how it was asked for.
	attempts := make([]SearchAttempt, 0, len(targets)*len(addresses)+1)
	for _, target := range targets {
		for _, address := range addresses {
			attempt := SearchAttempt{Address: address, Target: target}
			if address == SearchAddressTitle {
				attempt.Title = ptrString(language)
				if includeYear {
					attempt.Year = ptrBool(true)
				}
			}
			attempts = append(attempts, attempt)
		}
	}
	// The absolute supplement used to ride along inside every attempt; it is
	// its own attempt now, placed where it pays off — anime is named by
	// absolute number, so ahead of the broader season attempts.
	if isSeries && (query.LegacyTryAbsoluteEpisode == nil || *query.LegacyTryAbsoluteEpisode) {
		absolute := SearchAttempt{
			Address: SearchAddressTitle,
			Target:  SearchTargetAbsolute,
			Title:   ptrString(language),
		}
		attempts = append(attempts[:1:1], append([]SearchAttempt{absolute}, attempts[1:]...)...)
	}

	accept := &SearchAcceptance{Year: ptrBool(includeYear)}
	if isSeries {
		accept.Packs = ptrBool(true)
	}
	// The old model kept two title-language fields and picked one by mode: the
	// single language a text query went out under, and the list an id search
	// validated against. Acceptance is one list, so it is their union.
	titles := NormalizeSearchTitleLanguages(query.LegacySearchTitleLanguages)
	if len(titles) == 0 || mode != SearchModeID {
		titles = NormalizeSearchTitleLanguages(append(titles, language))
	}
	accept.Titles = titles

	query.Attempts = NormalizeSearchAttempts(attempts, isSeries)
	query.Accept = accept
	query.Stop = SearchStopFirstHit
	if isSeries && legacyScope(query.LegacySeriesSearchScope) == SeriesSearchScopeDynamic {
		query.Order = SearchOrderAdaptiveSeason
	} else {
		query.Order = SearchOrderAsListed
	}
	// Categories were per kind on the plan; a plan is one kind, so one field
	// carries it — and the stock values are what every indexer answers with
	// anyway, so they migrate to "let the indexer say".
	categories := strings.TrimSpace(query.LegacyMovieCategories)
	if isSeries {
		categories = strings.TrimSpace(query.LegacyTVCategories)
	}
	if categories != "" && categories != "2000" && categories != "5000" {
		query.Categories = categories
	}

	query.LegacySearchMode = ""
	query.LegacySeriesSearchScope = ""
	query.LegacyIncludeYear = nil
	query.LegacyIncludeYearInText = nil
	query.LegacyUseSeasonEpisodeParams = nil
	query.LegacyTryAbsoluteEpisode = nil
	query.LegacySearchTitleLanguage = ""
	query.LegacySearchTitleLanguages = nil
	query.LegacyMovieCategories = ""
	query.LegacyTVCategories = ""
	return true
}

// MigrateSearchPlans converts every pre-plan search request in the config, and
// settles the plans that are already converted.
func (c *Config) MigrateSearchPlans() bool {
	changed := false
	for i := range c.MovieSearchQueries {
		if migrateSearchPlan(&c.MovieSearchQueries[i], false) {
			changed = true
		}
	}
	for i := range c.SeriesSearchQueries {
		if migrateSearchPlan(&c.SeriesSearchQueries[i], true) {
			changed = true
		}
	}
	return changed
}

// Content classes: what a request is after, in the terms an indexer's category
// vocabulary answers. A class is derived from the content kind and whether the
// content is anime — never typed by hand — and each indexer translates it into
// its own category ids at dispatch. See the newznab client's category
// resolution.
const (
	SearchClassMovie   = "movie"
	SearchClassTV      = "tv"
	SearchClassTVAnime = "tv_anime"
)

// SearchClassFor is the class of one request.
func SearchClassFor(isSeries, isAnime bool) string {
	if !isSeries {
		return SearchClassMovie
	}
	if isAnime {
		return SearchClassTVAnime
	}
	return SearchClassTV
}

// SearchClassIsSeries reports whether a class is one of the TV classes.
func SearchClassIsSeries(class string) bool {
	switch class {
	case SearchClassTV, SearchClassTVAnime:
		return true
	}
	return false
}

// legacyScope reads a pre-plan series scope verbatim, including the two
// adaptive values and the older aliases, so the migration can tell them apart.
// NormalizeSeriesSearchScope cannot: it answers in the concrete vocabulary the
// indexer clients speak, which is all they ever need.
func legacyScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case SeriesSearchScopeSeason, legacySeriesSearchScopeSeasonParam, legacySeriesSearchScopeSeasonQuery:
		return SeriesSearchScopeSeason
	case SeriesSearchScopeNone:
		return SeriesSearchScopeNone
	case SeriesSearchScopeEpisodeThenSeason:
		return SeriesSearchScopeEpisodeThenSeason
	case SeriesSearchScopeDynamic:
		return SeriesSearchScopeDynamic
	}
	return SeriesSearchScopeSeasonEpisode
}
