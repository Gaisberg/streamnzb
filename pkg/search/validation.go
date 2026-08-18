package search

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/dreulavelle/jhin/rank"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/release"
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

func fuzzyTitleMatches(expect, gotTitle string) bool {
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
	if len(expectWords) == 1 {
		return len(gotWords) == 1 && gotWords[0] == expectWords[0]
	}
	// Reject when the release title has far more words than expected
	// (prevents "The Science of Interstellar" matching "Interstellar").
	if len(gotWords) > len(expectWords)+2 {
		return false
	}
	// Find expectWords as a contiguous block in gotWords.
	// Words before the block must all be common articles.
	for i := 0; i <= len(gotWords)-len(expectWords); i++ {
		match := true
		for j, w := range expectWords {
			if gotWords[i+j] != w {
				match = false
				break
			}
		}
		if match {
			for _, pre := range gotWords[:i] {
				if !titleArticles[pre] {
					return false
				}
			}
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

func isAllowedTrailingTitleWord(word string) bool {
	if titleArticles[word] {
		return true
	}
	// Keep short franchise suffixes such as "SVU" or "CI" matching their
	// base title, while still rejecting broader spin-off titles like "Feds".
	return len(word) > 0 && len(word) <= 3
}

func normalizedTitleMatches(expect, gotTitle string) bool {
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
	return fuzzyTitleMatches(expect, gotTitle)
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

func titleMatchesAnyExpectation(expectations []validationExpectation, gotTitle string) bool {
	if len(expectations) == 0 {
		return true
	}
	for _, expectation := range expectations {
		if expectation.Title == "" {
			continue
		}
		if normalizedTitleMatches(expectation.Title, gotTitle) {
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
// expected title/year/season/episode. absoluteEpisode ("" when unknown) is the
// anime absolute number of the requested episode; when set, a release that
// carries the absolute number (with no season or season 1) is accepted even
// though its parsed season/episode do not match the request.
func ValidateSearchResultsWithStatsForQueries(releases []*release.Release, contentType string, validationQueries []string, season, episode, absoluteEpisode string, enableTitleValidation, enableYearValidation bool) ([]*release.Release, ValidationStats) {
	stats := ValidationStats{}
	if contentType != "movie" && contentType != "series" {
		stats.RawResults = len(releases)
		stats.FinalResults = len(releases)
		return releases, stats
	}
	expectSeason, _ := strconv.Atoi(season)
	expectEpisode, _ := strconv.Atoi(episode)
	expectAbsolute, _ := strconv.Atoi(absoluteEpisode)
	stats.ExpectedSeason = expectSeason
	stats.ExpectedEpisode = expectEpisode

	expectations := validationExpectationsForQueries(contentType, validationQueries)
	if len(expectations) > 0 {
		stats.ExpectedTitle = expectations[0].Title
		stats.ExpectedYear = expectations[0].Year
	}
	stats.TitleValidationApplied = enableTitleValidation && len(expectations) > 0
	stats.YearValidationApplied = enableYearValidation && anyExpectationHasYear(expectations)

	var out []*release.Release
	for _, rel := range releases {
		if rel == nil {
			continue
		}
		stats.RawResults++
		parsed := parser.ParseReleaseTitle(rel.Title)
		if parsed == nil {
			stats.DroppedTitle++
			logger.Trace("ValidateSearchResults dropped: unparsed_title",
				"release", rel.Title,
			)
			continue
		}

		if contentType == "movie" {
			if stats.TitleValidationApplied && !titleMatchesAnyExpectation(expectations, parsed.Title) {
				stats.DroppedTitle++
				logger.Trace("ValidateSearchResults dropped: title",
					"expect_title", stats.ExpectedTitle,
					"got_title", parsed.Title,
					"release", rel.Title,
				)
				continue
			}
		} else {
			if stats.TitleValidationApplied && !titleMatchesAnyExpectation(expectations, parsed.Title) {
				stats.DroppedTitle++
				logger.Trace("ValidateSearchResults dropped: title",
					"expect_title", stats.ExpectedTitle,
					"got_title", parsed.Title,
					"release", rel.Title,
				)
				continue
			}
			if expectEpisode > 0 {
				matches := parsed.MatchesEpisodeRequest(expectSeason, expectEpisode)
				if !matches && expectAbsolute > 0 {
					// Absolute-numbered anime releases carry no season (or
					// season 1), which is exactly the season<=0 match path.
					matches = parsed.MatchesEpisodeRequest(0, expectAbsolute)
				}
				if !matches {
					stats.DroppedEpisodeRequest++
					logger.Trace("ValidateSearchResults dropped: episode_request",
						"expect_season", expectSeason,
						"expect_episode", expectEpisode,
						"expect_absolute", expectAbsolute,
						"got_seasons", parsed.Seasons,
						"got_episodes", parsed.Episodes,
						"complete", parsed.Complete,
						"release", rel.Title,
					)
					continue
				}
			} else if expectSeason > 0 && !parsed.HasSeason(expectSeason) {
				stats.DroppedSeason++
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
			logger.Trace("ValidateSearchResults dropped: year",
				"expect_year", stats.ExpectedYear,
				"got_year", parsed.Year,
				"release", rel.Title,
			)
			continue
		}

		if contentType == "series" {
			switch {
			case expectEpisode > 0:
				rank := parsed.EpisodeMatchRank(expectSeason, expectEpisode)
				if expectAbsolute > 0 {
					if absRank := parsed.EpisodeMatchRank(0, expectAbsolute); absRank > rank {
						rank = absRank
					}
				}
				switch rank {
				case 4:
					stats.AcceptedExactEpisode++
				case 3:
					stats.AcceptedMultiEpisode++
				case 2:
					stats.AcceptedSeasonPack++
				case 1:
					stats.AcceptedCompletePack++
				}
			case expectSeason > 0:
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
