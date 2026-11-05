package search

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/dreulavelle/jhin/rank"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/diag"
	"streamnzb/pkg/search/parser"
)

var seriesValidationSuffixRE = regexp.MustCompile(`(?i)\s+s[0-9]{1,2}(?:e[0-9]{1,3})?$`)

func movieYearMatches(expectYear, gotYear int) bool {
	if expectYear <= 0 || gotYear <= 0 {
		return true
	}
	return gotYear >= expectYear-1 && gotYear <= expectYear+1
}

func parseValidationQuery(validationQuery string) (normTitle string, year int) {
	norm := strings.ToLower(strings.TrimSpace(release.NormalizeTitleForSearchQuery(validationQuery)))
	norm = strings.TrimSpace(norm)
	if norm == "" {
		return "", 0
	}
	for i := len(norm) - 1; i >= 0; i-- {
		if norm[i] >= '0' && norm[i] <= '9' {
			continue
		}
		if norm[i] == ' ' && i+1 < len(norm) {
			trailing := strings.TrimSpace(norm[i+1:])
			if len(trailing) == 4 {
				if y, err := strconv.Atoi(trailing); err == nil && y >= 1900 && y <= 2100 {
					return strings.TrimSpace(norm[:i]), y
				}
			}
		}
		break
	}
	return norm, 0
}

func parseSeriesValidationQuery(validationQuery string) (string, int) {
	norm := strings.ToLower(strings.TrimSpace(release.NormalizeTitleForSearchQuery(validationQuery)))
	norm = strings.TrimSpace(norm)
	if norm == "" {
		return "", 0
	}
	trimmed := strings.TrimSpace(seriesValidationSuffixRE.ReplaceAllString(norm, ""))
	if trimmed == "" {
		trimmed = norm
	}
	title, year := parseValidationQuery(trimmed)
	if title != "" {
		return title, year
	}
	return trimmed, 0
}

var titleArticles = map[string]bool{"the": true, "a": true, "an": true}
var optionalTitleWords = map[string]bool{"and": true}

func filterOptionalTitleWords(words []string) []string {
	if len(words) == 0 {
		return words
	}
	filtered := make([]string, 0, len(words))
	for _, word := range words {
		if optionalTitleWords[word] {
			continue
		}
		filtered = append(filtered, word)
	}
	return filtered
}

func titleWordsForMatch(s string) []string {
	if parsed := parser.ParseReleaseTitle(s); parsed != nil {
		if parsedTitle := strings.TrimSpace(parsed.Title); parsedTitle != "" {
			s = parsedTitle
		}
	}
	return filterOptionalTitleWords(release.NormalizeTitleWordsForMatch(s))
}

// fuzzyTitleMatches looks for the expected words as a contiguous block in the
// release title. allowLeadingWords decides what may sit in front of that block:
// strict matching accepts only articles, which is why "Special Ops Lioness"
// fails "Lioness". That guard exists for text queries, where a one-word title
// keyword-matches anything containing the word ("The Science of Interstellar"
// for "Interstellar"). When the caller can back the title up with a
// season/episode check it is redundant, and all it costs is every show whose
// scene name keeps a prefix the metadata title dropped.
func fuzzyTitleMatches(expect, gotTitle string, allowLeadingWords bool) bool {
	expectWords := titleWordsForMatch(expect)
	gotWords := titleWordsForMatch(gotTitle)
	if len(expectWords) == 0 {
		return true
	}
	if len(gotWords) == 0 {
		return false
	}
	if len(gotWords) < len(expectWords) {
		return false
	}
	if !allowLeadingWords {
		if len(expectWords) == 1 {
			return len(gotWords) == 1 && gotWords[0] == expectWords[0]
		}
		// Reject when the release title has far more words than expected.
		if len(gotWords) > len(expectWords)+2 {
			return false
		}
	}
	// Find expectWords as a contiguous block in gotWords.
	for i := 0; i <= len(gotWords)-len(expectWords); i++ {
		match := true
		for j, w := range expectWords {
			if gotWords[i+j] != w {
				match = false
				break
			}
		}
		if match {
			leadingArticlesOnly := true
			for _, pre := range gotWords[:i] {
				if !titleArticles[pre] {
					leadingArticlesOnly = false
					break
				}
			}
			if !allowLeadingWords && !leadingArticlesOnly {
				return false
			}
			// An article in front of a one-word title is part of the name, not
			// noise: "The Batman" is not "Batman". A qualifier in front of it
			// is a different thing, and is the whole point of the relaxation.
			if allowLeadingWords && i > 0 && leadingArticlesOnly && len(expectWords) == 1 {
				return false
			}
			// Trailing words stay strict either way: a spin-off is named by
			// what follows the base title ("The Rookie Feds"), and no season
			// or episode number tells those apart.
			for _, post := range gotWords[i+len(expectWords):] {
				if !isAllowedTrailingTitleWord(post) {
					return false
				}
			}
			return true
		}
	}
	return false
}

// trailingTitleAbbreviations are the franchise abbreviations a release may
// carry past the expected title ("Law and Order SVU" for "Law & Order").
// They are listed rather than recognised by length: the token that tells a
// sequel or sibling series from its base title is often just as short —
// "Sword Art Online II", "Steins;Gate 0", "Dragon Ball Z", "Example Movie 2" —
// and a short-word rule waved every one of those through.
var trailingTitleAbbreviations = map[string]bool{"svu": true, "ci": true}

func isAllowedTrailingTitleWord(word string) bool {
	return titleArticles[word] || trailingTitleAbbreviations[word]
}

func normalizedTitleMatches(expect, gotTitle string, allowLeadingWords bool) bool {
	expectNorm := release.NormalizeTitleForDedup(expect)
	gotNorm := release.NormalizeTitleForDedup(gotTitle)
	if gotNorm == "" {
		return false
	}
	if expectNorm == "" {
		return true
	}
	// Exact or prefix match (with optional 4-digit year suffix)
	if gotNorm == expectNorm {
		return true
	}
	if strings.HasPrefix(gotNorm, expectNorm) {
		rest := gotNorm[len(expectNorm):]
		if rest == "" {
			return true
		}
		if len(rest) == 4 {
			allDigit := true
			for _, r := range rest {
				if !unicode.IsDigit(r) {
					allDigit = false
					break
				}
			}
			if allDigit {
				return true
			}
		}
	}
	// Fall back to fuzzy: every expected word must appear as a word in the release title.
	return fuzzyTitleMatches(expect, gotTitle, allowLeadingWords)
}

type ValidationStats struct {
	RawResults      int
	FinalResults    int
	RejectedResults int

	TitleValidationApplied bool
	YearValidationApplied  bool

	ExpectedTitle   string
	ExpectedYear    int
	ExpectedSeason  int
	ExpectedEpisode int

	AcceptedExactEpisode int
	AcceptedMultiEpisode int
	AcceptedSeasonPack   int
	AcceptedCompletePack int
	AcceptedSeasonMatch  int

	DroppedTitle          int
	DroppedEpisodeRequest int
	DroppedSeason         int
	DroppedYear           int

	// TitleMismatchKept counts releases whose title did not match but which
	// were kept anyway, because the request was not enforcing the title. It is
	// the only trace an ID request leaves of an indexer answering with
	// something it was not asked for, so it is reported rather than dropped.
	TitleMismatchKept int

	// Dropped names the releases turned away, up to the caller's
	// RecordDropped, and DroppedOmitted counts the ones past it. The counters
	// above answer "how many"; these answer "was mine one of them", which is
	// the question someone whose release did not show up is asking.
	Dropped        []diag.DroppedRelease
	DroppedOmitted int
}

// recordDrop notes one turned-away release, up to the cap the caller asked
// for. Past it only the tally grows: a search answered with a thousand wrong
// results should not build, ship and persist a thousand of them.
func (s *ValidationStats) recordDrop(limit int, rel *release.Release, reason, detail string) {
	if limit <= 0 {
		return
	}
	if len(s.Dropped) >= limit {
		s.DroppedOmitted++
		return
	}
	drop := diag.DroppedRelease{Stage: diag.DropStageValidation, Reason: reason, Detail: detail}
	if rel != nil {
		drop.Title = rel.Title
		drop.Indexer = rel.Indexer
	}
	s.Dropped = append(s.Dropped, drop)
}

type validationExpectation struct {
	Title string
	Year  int
}

func validationExpectationsForQueries(contentType string, validationQueries []string) []validationExpectation {
	expectations := make([]validationExpectation, 0, len(validationQueries))
	seen := make(map[string]bool, len(validationQueries))
	for _, validationQuery := range validationQueries {
		trimmed := strings.TrimSpace(validationQuery)
		if trimmed == "" {
			continue
		}
		var title string
		var year int
		if contentType == "movie" {
			title, year = parseValidationQuery(trimmed)
		} else {
			title, year = parseSeriesValidationQuery(trimmed)
		}
		if title == "" {
			continue
		}
		key := title + "\x00" + strconv.Itoa(year)
		if seen[key] {
			continue
		}
		seen[key] = true
		expectations = append(expectations, validationExpectation{
			Title: title,
			Year:  year,
		})
	}
	return expectations
}

func titleMatchesAnyExpectation(expectations []validationExpectation, gotTitle string, allowLeadingWords bool) bool {
	if len(expectations) == 0 {
		return true
	}
	for _, expectation := range expectations {
		if expectation.Title == "" {
			continue
		}
		if normalizedTitleMatches(expectation.Title, gotTitle, allowLeadingWords) {
			return true
		}
	}
	// jhin's indel-ratio similarity is the tolerant fallback: release naming
	// rewrites punctuation and spacing in ways the word-block matcher cannot
	// line up ("Evangelion: 3.0+1.0" vs "Evangelion 3 0 1 0"), anime titles
	// especially. The strict matcher stays primary; this only rescues
	// near-misses.
	for _, expectation := range expectations {
		if expectation.Title != "" && similarTitleMatches(expectation.Title, gotTitle) {
			return true
		}
	}
	return false
}

// similarTitleMatches applies jhin's similarity at its default threshold,
// guarded so fuzziness never bridges numbering: "Mockingjay Part 1" and
// "Part 2" are one edit apart yet different films, so the digits on both
// sides must agree, in order, before the ratio counts. The digits are
// compared as one sequence rather than per run because normalization
// collapses punctuation between them ("3.0+1.0" and "3.0 + 1.0" split into
// different runs but carry the same numbering).
func similarTitleMatches(expect, gotTitle string) bool {
	expectNorm, gotNorm := rank.Normalize(expect), rank.Normalize(gotTitle)
	if digitSequence(expectNorm) != digitSequence(gotNorm) {
		return false
	}
	return rank.TitleMatch(expectNorm, gotNorm, 0)
}

func digitSequence(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func anyExpectationHasYear(expectations []validationExpectation) bool {
	for _, expectation := range expectations {
		if expectation.Year > 0 {
			return true
		}
	}
	return false
}

func yearMatchesAnyExpectation(expectations []validationExpectation, gotYear int) bool {
	for _, expectation := range expectations {
		if expectation.Year <= 0 {
			continue
		}
		if movieYearMatches(expectation.Year, gotYear) {
			return true
		}
	}
	return false
}

func ValidateSearchResults(releases []*release.Release, contentType, validationQuery, season, episode string, enableTitleValidation, enableYearValidation bool) []*release.Release {
	filtered, _ := ValidateSearchResultsWithStats(releases, contentType, validationQuery, season, episode, enableTitleValidation, enableYearValidation)
	return filtered
}

func ValidateSearchResultsForQueries(releases []*release.Release, contentType string, validationQueries []string, season, episode string, enableTitleValidation, enableYearValidation bool) []*release.Release {
	filtered, _ := ValidateSearchResultsWithStatsForQueries(releases, contentType, validationQueries, season, episode, "", enableTitleValidation, enableYearValidation)
	return filtered
}

func ValidateSearchResultsWithStats(releases []*release.Release, contentType, validationQuery, season, episode string, enableTitleValidation, enableYearValidation bool) ([]*release.Release, ValidationStats) {
	return ValidateSearchResultsWithStatsForQueries(releases, contentType, []string{validationQuery}, season, episode, "", enableTitleValidation, enableYearValidation)
}

// ValidateSearchResultsWithStatsForQueries filters releases against the
// expected title/year/season/episode. enableTitleValidation decides only
// whether a title mismatch drops the release: the check runs either way, and a
// mismatch that is not enforced is counted in TitleMismatchKept.
//
// absoluteEpisode ("" when unknown) is the anime absolute number of the
// requested episode; when set, a release that carries the absolute number (with
// no season or season 1) is accepted even though its parsed season/episode do
// not match the request.
func ValidateSearchResultsWithStatsForQueries(releases []*release.Release, contentType string, validationQueries []string, season, episode, absoluteEpisode string, enableTitleValidation, enableYearValidation bool) ([]*release.Release, ValidationStats) {
	return ValidateSearchResultsWithOptions(releases, contentType, validationQueries, ValidationOptions{
		Season:          season,
		Episode:         episode,
		AbsoluteEpisode: absoluteEpisode,
		EnforceTitle:    enableTitleValidation,
		EnforceYear:     enableYearValidation,
		AcceptPacks:     true,
	})
}

// ValidationOptions is what a search plan's acceptance says a match is. It
// replaces a positional argument list that had grown to eight, four of them
// strings, and gives the plan's Accept section somewhere to land whole.
type ValidationOptions struct {
	Season          string
	Episode         string
	AbsoluteEpisode string
	// AirDate ("" when unknown) is the requested episode's air date as
	// YYYY-MM-DD; a release named only by that date is the episode.
	AirDate string
	// OVASeries says the requested entry is itself an OVA series (Kitsu's show
	// type), so an OVA numbered as its season-1 episode is that episode.
	// Without it an OVA is never a regular episode.
	OVASeries bool
	// EnforceTitle decides only whether a title mismatch drops the release;
	// the check runs either way. An id attempt does not enforce it, because
	// the indexer resolved the title itself.
	EnforceTitle bool
	EnforceYear  bool
	// AcceptPacks accepts a season or complete-series pack that contains the
	// requested episode. Off keeps only releases that name the episode.
	AcceptPacks bool
	// RecordDropped is how many turned-away releases to record in
	// Stats.Dropped, naming the check that did it. Zero records none, which is
	// the default and what the per-profile debug sweep wants: it re-validates
	// every raw release purely for counts and would otherwise build a list
	// nothing reads. The rest are counted in Stats.DroppedOmitted, because a
	// list quietly cut short reads as a complete one.
	RecordDropped int
}

// ValidateSearchResultsWithOptions filters releases against the expected
// title/year/season/episode and the plan's acceptance.
//
// "anime" is episodic anime (an anime film arrives as "movie"), checked
// leniently because fansub names (romaji titles, absolute or per-season
// numbering) do not reliably match the metadata: an episode request drops a
// release only when it names another episode outright. The title and year are
// handled as for a series — the title enforced on title attempts, where the
// query was just a keyword, and the year when the plan asks (a fansub name
// carrying none passes) — and so is a season request.
func ValidateSearchResultsWithOptions(releases []*release.Release, contentType string, validationQueries []string, opts ValidationOptions) ([]*release.Release, ValidationStats) {
	season, episode, absoluteEpisode := opts.Season, opts.Episode, opts.AbsoluteEpisode
	enableTitleValidation, enableYearValidation := opts.EnforceTitle, opts.EnforceYear
	stats := ValidationStats{}
	anime := contentType == "anime"
	episodic := contentType == "series" || anime
	if contentType != "movie" && !episodic {
		stats.RawResults = len(releases)
		stats.FinalResults = len(releases)
		return releases, stats
	}
	// Season "0" is Stremio's Specials season and asks for S00 releases; only
	// an absent season means the request named none, which is what lets an
	// absolute-numbered or unmapped Kitsu episode match by number alone.
	expectSeason, seasonErr := strconv.Atoi(strings.TrimSpace(season))
	seasonless := seasonErr != nil || expectSeason < 0
	if seasonless {
		expectSeason = 0
	}
	expectEpisode, _ := strconv.Atoi(episode)
	expectAbsolute, _ := strconv.Atoi(absoluteEpisode)
	target := parser.EpisodeTarget{Season: expectSeason, Seasonless: seasonless, Episode: expectEpisode, Absolute: expectAbsolute, AirDate: opts.AirDate}
	stats.ExpectedSeason = expectSeason
	stats.ExpectedEpisode = expectEpisode

	expectations := validationExpectationsForQueries(contentType, validationQueries)
	if len(expectations) > 0 {
		stats.ExpectedTitle = expectations[0].Title
		stats.ExpectedYear = expectations[0].Year
	}
	// The title is checked whenever there is something to check it against;
	// enableTitleValidation only decides whether a mismatch drops the release.
	checkTitle := len(expectations) > 0
	stats.TitleValidationApplied = enableTitleValidation && checkTitle
	stats.YearValidationApplied = enableYearValidation && anyExpectationHasYear(expectations)
	// A requested season or episode backs the title up, so the release does not
	// have to *start* with the expected title to be the right show — scene
	// names keep prefixes the metadata title drops ("Special Ops: Lioness").
	allowLeadingTitleWords := episodic && (!seasonless || expectEpisode > 0)

	var out []*release.Release
	for _, rel := range releases {
		if rel == nil {
			continue
		}
		stats.RawResults++
		parsed := parser.ParseReleaseTitle(rel.Title)
		if parsed == nil {
			stats.DroppedTitle++
			stats.recordDrop(opts.RecordDropped, rel, diag.DropReasonUnparsed, "the release name did not parse")
			logger.Trace("ValidateSearchResults dropped: unparsed_title",
				"release", rel.Title,
			)
			continue
		}

		if checkTitle && !titleMatchesAnyExpectation(expectations, parsed.Title, allowLeadingTitleWords) {
			if stats.TitleValidationApplied {
				stats.DroppedTitle++
				stats.recordDrop(opts.RecordDropped, rel, diag.DropReasonTitle,
					fmt.Sprintf("expected %q, got %q", stats.ExpectedTitle, parsed.Title))
				logger.Trace("ValidateSearchResults dropped: title",
					"expect_title", stats.ExpectedTitle,
					"got_title", parsed.Title,
					"release", rel.Title,
				)
				continue
			}
			stats.TitleMismatchKept++
			logger.Trace("ValidateSearchResults kept: title mismatch",
				"expect_title", stats.ExpectedTitle,
				"got_title", parsed.Title,
				"release", rel.Title,
			)
		}

		episodeRank := 0
		if episodic {
			if expectEpisode > 0 {
				episodeRank = parsed.TargetMatchRank(target)
				// An OVA matched by a bare number names an OVA, not the regular
				// episode sharing it, unless the entry is itself an OVA series;
				// namesOtherEpisode then decides. A numbered batch bundling an
				// OVA ("01-12 + OVA") is the episodes it names and stays.
				if anime && episodeRank == 4 && !opts.OVASeries && (target.Season > 0 || target.Seasonless) &&
					len(parsed.Seasons) == 0 && isOVA(parsed, rel.Title) {
					episodeRank = 0
				}
				// An anime release that did not rank stays unless it names
				// another episode.
				if episodeRank == 0 && (!anime || namesOtherEpisode(parsed, rel.Title, target, opts.OVASeries)) {
					stats.DroppedEpisodeRequest++
					detail := fmt.Sprintf("expected S%02dE%02d, got seasons %v episodes %v", expectSeason, expectEpisode, parsed.Seasons, parsed.Episodes)
					if parsed.Date != "" {
						detail += fmt.Sprintf(", dated %s (episode air date %s)", parsed.Date, cmp.Or(opts.AirDate, "unknown"))
					}
					stats.recordDrop(opts.RecordDropped, rel, diag.DropReasonEpisode, detail)
					logger.Trace("ValidateSearchResults dropped: episode_request",
						"expect_season", expectSeason,
						"expect_episode", expectEpisode,
						"expect_absolute", expectAbsolute,
						"expect_air_date", opts.AirDate,
						"got_date", parsed.Date,
						"got_seasons", parsed.Seasons,
						"got_episodes", parsed.Episodes,
						"complete", parsed.Complete,
						"release", rel.Title,
					)
					continue
				}
			} else if !seasonless && !parsed.HasSeason(expectSeason) {
				stats.DroppedSeason++
				stats.recordDrop(opts.RecordDropped, rel, diag.DropReasonSeason,
					fmt.Sprintf("expected season %d, got %v", expectSeason, parsed.Seasons))
				logger.Trace("ValidateSearchResults dropped: season",
					"expect_season", expectSeason,
					"got_seasons", parsed.Seasons,
					"release", rel.Title,
				)
				continue
			}
		}

		if stats.YearValidationApplied && !yearMatchesAnyExpectation(expectations, parsed.Year) {
			stats.DroppedYear++
			stats.recordDrop(opts.RecordDropped, rel, diag.DropReasonYear,
				fmt.Sprintf("expected %d, got %d", stats.ExpectedYear, parsed.Year))
			logger.Trace("ValidateSearchResults dropped: year",
				"expect_year", stats.ExpectedYear,
				"got_year", parsed.Year,
				"release", rel.Title,
			)
			continue
		}

		if episodic {
			switch {
			case expectEpisode > 0:
				// Ranks 2 and 1 are a season pack and a complete-series pack:
				// they contain the episode without naming it, and a plan that
				// does not accept packs wants neither. Anime also keeps another
				// season's pack unranked (see namesOtherEpisode), which is a
				// pack all the same.
				otherPack := anime && episodeRank == 0 && isSeasonOnly(parsed)
				if !opts.AcceptPacks && (otherPack || (episodeRank > 0 && episodeRank < 3)) {
					detail := "a pack containing the episode, which this request does not accept"
					if otherPack {
						detail = "a pack that may contain the episode, which this request does not accept"
					}
					stats.DroppedEpisodeRequest++
					stats.recordDrop(opts.RecordDropped, rel, diag.DropReasonEpisode, detail)
					logger.Trace("ValidateSearchResults dropped: pack not accepted",
						"expect_season", expectSeason,
						"expect_episode", expectEpisode,
						"release", rel.Title,
					)
					continue
				}
				switch episodeRank {
				case 4:
					stats.AcceptedExactEpisode++
				case 3:
					stats.AcceptedMultiEpisode++
				case 2:
					stats.AcceptedSeasonPack++
				case 1:
					stats.AcceptedCompletePack++
				}
			case !seasonless:
				if !opts.AcceptPacks && (parsed.IsShowPack() || parsed.IsSeasonPack(expectSeason)) {
					stats.DroppedSeason++
					stats.recordDrop(opts.RecordDropped, rel, diag.DropReasonSeason,
						"a pack, which this request does not accept")
					continue
				}
				switch {
				case parsed.IsShowPack():
					stats.AcceptedCompletePack++
				case parsed.IsSeasonPack(expectSeason):
					stats.AcceptedSeasonPack++
				case parsed.HasSeason(expectSeason):
					stats.AcceptedSeasonMatch++
				}
			}
		}

		out = append(out, rel)
	}
	stats.FinalResults = len(out)
	stats.RejectedResults = stats.RawResults - stats.FinalResults
	return out, stats
}

// namesOtherEpisode reports whether an anime release that did not match the
// target has a title that confidently names another episode: a season or
// episode number the parser reads, which the target's numberings do not
// match. It judges the release title only; playback validates the files
// themselves, whose names may differ. Everything else is no evidence of
// another episode and is kept (packs are left to the plan's pack setting):
//
//   - a title with no number the parser reads ("Show (BD)", "Show - SP02");
//   - a target that cannot pick a file (an unmapped Kitsu entry with no
//     absolute number), which playback does not check either;
//   - for a special (season 0), OVA notation with no season, which the parser
//     flags but cannot number against TVDB's specials: "Show - OVA 2" reads as
//     episode 2 only for want of a better reading. Ancillary extras (Sample,
//     Trailer, Featurette, NCOP, NCED) number nothing and excuse nothing;
//   - a season-only release naming the requested season or an earlier one
//     when the absolute number is known: anime packs are often numbered
//     absolutely inside, so "Show S01 [BD]" may hold "Show - 13.mkv" for
//     S02E01, while a later season's pack holds only later episodes;
//   - for a special (season 0), a season-only release tagged OVA, which BD
//     packs use for the specials they bundle ("Show S01 + OVA"). The parser
//     tags only the word "OVA"; "OVAs" and "Specials" go unrecognised.
//
// A season tag with continuing absolute numbering ("S3 - 38" for S03E01,
// absolute 38) is dropped: it parses exactly like the season's own episode 38.
// So is an OVA numbered as another episode, for a regular episode:
// "Show - 23 [OVA]", "Show OVA 2" for S01E05, and "Show S01 OVA 2" for S02E01
// (for S01E05 the parser reads that one as season 1's pack, ranked above). One
// numbered as a season-1 episode itself, under no other season, stays — an
// OVA-only series (Hellsing Ultimate, FLCL) lists its OVAs as season 1 — but
// not for a later season's episode. An unnumbered OVA bundle ("Show (TV + OVA)
// [BD]") and a season pack tagged OVA ("Show S03 + OVA") name no other
// episode and are left to the rules above.
func namesOtherEpisode(p *parser.ParsedRelease, title string, t parser.EpisodeTarget, ovaSeries bool) bool {
	if !t.Valid() {
		return false
	}
	// The parser misses an OVA glued to its number ("OVA2") and does not read
	// the number as an episode.
	ovaNumber, numbered := -1, false
	if m := numberedOVA.FindStringSubmatch(title); m != nil {
		ovaNumber, _ = strconv.Atoi(m[1])
		numbered = true
	}
	ova := numbered || slices.Contains(p.Extras, "OVA")
	special := t.Season == 0 && !t.Seasonless
	// Named only by an air date: another night's unless it is within a day of
	// the episode's; with the episode's date unknown nothing confirms it.
	if len(p.Seasons) == 0 && len(p.Episodes) == 0 && p.Date != "" && (t.AirDate == "" || airDateApart(p.Date, t.AirDate)) {
		return true
	}
	if ova && !special && (numbered || !isSeasonOnly(p)) {
		numbers := p.Episodes
		if len(numbers) == 0 && numbered {
			numbers = []int{ovaNumber}
		}
		// Only an OVA series (by Kitsu's show type) lists its OVAs as
		// episodes, as season 1.
		if ovaSeries && (t.Season <= 1 || t.Seasonless) && (len(p.Seasons) == 0 || p.HasSeason(1)) && slices.Contains(numbers, t.Episode) {
			return false
		}
		if len(numbers) > 0 {
			return true
		}
	}
	if ova && special && len(p.Seasons) == 0 {
		return false
	}
	// Specials (S00) hold no regular episode, whatever their numbering.
	regularSeasonUpTo := slices.ContainsFunc(p.Seasons, func(s int) bool { return s >= 1 && (t.Seasonless || s <= t.Season) })
	if isSeasonOnly(p) && ((t.Absolute > 0 && regularSeasonUpTo) || (ova && special)) {
		return false
	}
	return len(p.Seasons) > 0 || len(p.Episodes) > 0
}

// isOVA reports whether the release is tagged as an OVA, by the parser or by an
// OVA number the parser missed.
func isOVA(p *parser.ParsedRelease, title string) bool {
	return slices.Contains(p.Extras, "OVA") || numberedOVA.MatchString(title)
}

// numberedOVA finds an OVA's own number ("OVA 2", "OVA-02", "_OVA_02_",
// "OVA2", "Show.OVA.02.1080p", "OVA 02v2", "OVA 02[1080p]"), which the parser
// leaves unread. The number must end the token — anything but a letter,
// digit or dot may follow, or a version ("02v2") — so "OVA.720p", "OVA 10bit"
// and "OVA 5.1" (a dot before one or two digits is a channel count, before
// three or more a resolution) are no OVA numbers; the edges are spelled out
// because \b does not fire beside an underscore.
var numberedOVA = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])OVA[\s._#-]*(\d{1,3})(?:[^a-z0-9.]|v\d|\.(?:\D|\d{3,}|$)|$)`)

// airDateApart reports whether a release dated date is another night's than
// the episode's airDate (both YYYY-MM-DD): more than a day apart, since a
// late-night broadcast (25:30 JST) is often dated the next calendar day.
func airDateApart(date, airDate string) bool {
	d, err1 := time.Parse(time.DateOnly, date)
	a, err2 := time.Parse(time.DateOnly, airDate)
	if err1 != nil || err2 != nil {
		return false
	}
	return d.Sub(a).Abs() > 24*time.Hour
}

// isSeasonOnly reports whether the title names seasons and no episodes: a
// season pack, of whichever season.
func isSeasonOnly(p *parser.ParsedRelease) bool {
	return len(p.Seasons) > 0 && len(p.Episodes) == 0
}
