package stremio

import (
	"slices"
	"strconv"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
	"streamnzb/pkg/session"
)

// confirmAbsoluteEpisode returns the request's absolute episode number once
// TVDB's record for the episode (found by tvdbEpisode) has had its say, or 0
// when TVDB contradicts it.
//
// A derived absolute number sums TMDB's season counts, but the season and
// episode it is added to come from TVDB's order — Cinemeta and the
// anime-lists mapping both number seasons as TVDB does — and the two split
// seasons differently (One Piece is S1 of 8 episodes on TVDB, 61 on TMDB).
// TVDB's own absolute number is no substitute: it can count specials filed
// under season 0 that releases do not (Mushoku Tensei's S01E17 is absolute 18
// there, "- 17" in release names). So TVDB only vetoes: when it numbers the
// same episode differently, which source is right is unknown, and an unknown
// number accepts no wrong episode. Season 1 maps to its own episode numbers
// whatever either source says, a number resolved up front (a Kitsu entry
// spanning the series) is not derived, and when TVDB has no number or no
// record the derived number stands as before.
func confirmAbsoluteEpisode(ids *session.AvailReportMeta, ep tvdb.Episode, found bool) int {
	if ids == nil {
		return 0
	}
	if !found || ids.AbsoluteEpisode <= 0 || ids.Seasonless || ids.Season <= 1 ||
		ep.AbsoluteNumber <= 0 || ep.AbsoluteNumber == ids.AbsoluteEpisode {
		return ids.AbsoluteEpisode
	}
	logger.Debug("TVDB numbers the episode differently; absolute episode unknown",
		"tvdb_id", ids.TvdbID, "season", ids.Season, "episode", ids.Episode,
		"derived_absolute", ids.AbsoluteEpisode, "tvdb_absolute", ep.AbsoluteNumber)
	return 0
}

// settleEpisodeNumbering resolves the air date and confirms the absolute
// number from one TVDB lookup, before the library and the search read them,
// so search and playback share one number. The id cannot say whose numbering
// the season and episode follow — this addon's own tmdb: episode ids carry
// TVDB's numbering when TVDB is the meta source — so TVDB is asked whatever
// the id, and a disagreement leaves the number unknown rather than wrong.
func (s *Server) settleEpisodeNumbering(contentType string, ids *session.AvailReportMeta) {
	if ids == nil {
		return
	}
	switch contentType {
	case "series", "anime", "tv":
	default:
		return
	}
	ep, found := s.tvdbEpisode(ids)
	ids.AirDate = s.episodeAirDateFrom(ids, ep, found)
	ids.AbsoluteEpisode = confirmAbsoluteEpisode(ids, ep, found)
}

// tvdbEpisode is TVDB's record for the request's season and episode, looked
// up once per request for both the air date and the absolute number: TVDB
// errors are not cached, so a second lookup would wait out a second timeout
// while TVDB is down. found is false when there is nothing to look up, no
// client, an error, or no such episode.
func (s *Server) tvdbEpisode(ids *session.AvailReportMeta) (ep tvdb.Episode, found bool) {
	if s == nil || ids == nil || ids.TvdbID == "" || ids.Season <= 0 || ids.Episode <= 0 {
		return tvdb.Episode{}, false
	}
	rt := s.runtime()
	if rt.tvdbClient == nil {
		return tvdb.Episode{}, false
	}
	ep, ok, err := rt.tvdbClient.GetSeriesEpisode(ids.TvdbID, ids.Season, ids.Episode)
	if err != nil {
		logger.Debug("TVDB episode lookup failed", "tvdb_id", ids.TvdbID, "season", ids.Season, "episode", ids.Episode, "err", err)
		return tvdb.Episode{}, false
	}
	return ep, ok
}

// episodeAirDateFrom is the air date a daily show's date-named releases are
// matched against, as YYYY-MM-DD, or "" when it is not known, given TVDB's
// record for the episode (see settleEpisodeNumbering).
//
// It is TVDB's date for the request's season and episode, kept only when TMDB
// does not contradict it. No single source can be trusted with the request's
// numbering: TVMaze numbers WWE Raw's seasons differently (its S33E43 aired a
// year after Stremio's), and Stremio's own numbering follows TVDB for some
// daily shows and TMDB for others (Gutfeld!'s S4E246 is a different night on
// each). A date from the wrong episode accepts another night's release, so the
// date stands only when both sources agree on it, or when TMDB plainly numbers
// the season another way (Jimmy Kimmel Live's S24E2524 against TMDB's
// S24E1-157). Anything TMDB cannot settle — a failed request, an undated
// episode, an episode or season it does not list yet, a series it cannot be
// shown to share with TVDB — leaves no date, and date-named releases are
// dropped as before. Unlike the unaired gate, which only decides whether to
// search, nothing falls back to another source.
func (s *Server) episodeAirDateFrom(ids *session.AvailReportMeta, ep tvdb.Episode, found bool) string {
	if !found {
		return ""
	}
	if _, err := time.Parse(airDateLayout, ep.Aired); err != nil {
		return ""
	}
	if verdict := s.tmdbAirDateVerdict(ids, ep.Aired); verdict != tmdbAgrees && verdict != tmdbNumbersOtherwise {
		logger.Debug("TVDB air date not confirmed by TMDB; date-named releases will not match",
			"tvdb_id", ids.TvdbID, "tmdb_id", ids.TmdbID, "season", ids.Season, "episode", ids.Episode,
			"tvdb_air_date", ep.Aired, "verdict", verdict)
		return ""
	}
	return ep.Aired
}

// TMDB's answer about TVDB's date for one season and episode. Only
// tmdbAgrees and tmdbNumbersOtherwise let the date stand; the rest are either
// a contradiction or nothing TMDB could vouch for, which are logged apart but
// end the same way.
const (
	tmdbAgrees           = "agrees"
	tmdbNumbersOtherwise = "numbers_otherwise"
	tmdbSeasonUnlisted   = "season_unlisted"
	tmdbEpisodeUnlisted  = "episode_unlisted"
	tmdbDisagrees        = "disagrees"
	tmdbEpisodeUndated   = "episode_undated"
	tmdbUnavailable      = "unavailable"
	tmdbSeriesUnknown    = "series_unknown"
	tmdbIdentityMismatch = "identity_unverified"
	tmdbRequestFailed    = "request_failed"
)

// nightlySeasonEpisodes is more episodes than a nightly show airs in a year.
// A request that far past TMDB's last episode of the season is not TMDB
// lagging behind but TMDB numbering the season another way.
const nightlySeasonEpisodes = 366

// tmdbRefreshWindow is how recently TVDB must date the episode for a season
// or episode TMDB does not list to be worth asking TMDB about again: TMDB's
// answers are cached for a day, so only a recent episode can be one the
// cache predates. Older episodes TMDB does not list are settled from the
// cache, so a show TMDB numbers differently pays the uncached request only
// in the week after each episode airs.
const tmdbRefreshWindow = 7 * 24 * time.Hour

// tmdbAirDateVerdict asks TMDB whether it holds an episode under the same
// season and episode numbers, and if so, whether it aired on tvdbDate. The
// dates must match exactly: nightly episodes are a day apart, so a one-day
// allowance would wave through the neighbouring episode.
func (s *Server) tmdbAirDateVerdict(ids *session.AvailReportMeta, tvdbDate string) string {
	rt := s.runtime()
	if rt.tmdbClient == nil {
		return tmdbUnavailable
	}
	tmdbID, err := strconv.Atoi(ids.TmdbID)
	if err != nil || tmdbID <= 0 {
		return tmdbSeriesUnknown
	}
	ext, err := rt.tmdbClient.GetExternalIDs(tmdbID, "tv")
	if err != nil {
		return tmdbRequestFailed
	}
	if !tmdbNamesSameSeries(ext, ids) {
		return tmdbIdentityMismatch
	}
	verdict := tmdbEpisodeVerdict(rt.tmdbClient.GetTVDetails, rt.tmdbClient.GetTVSeasonDetails, tmdbID, ids, tvdbDate)
	if aired, _ := time.Parse(airDateLayout, tvdbDate); (verdict != tmdbSeasonUnlisted && verdict != tmdbEpisodeUnlisted) || time.Since(aired) > tmdbRefreshWindow {
		return verdict
	}
	// The cached series or season may predate the episode: it is the newest
	// one, the very night a wrong TVDB date would let another night through.
	// Only what lacked it is asked again.
	getDetails := rt.tmdbClient.GetTVDetails
	if verdict == tmdbSeasonUnlisted {
		getDetails = rt.tmdbClient.RefreshTVDetails
	}
	return tmdbEpisodeVerdict(getDetails, rt.tmdbClient.RefreshTVSeasonDetails, tmdbID, ids, tvdbDate)
}

// tmdbEpisodeVerdict is tmdbAirDateVerdict's reading of one series details
// and season answer, however they were fetched.
func tmdbEpisodeVerdict(
	getDetails func(int) (*tmdb.TVDetails, error),
	getSeason func(int, int) (*tmdb.TVSeasonDetails, error),
	tmdbID int, ids *session.AvailReportMeta, tvdbDate string,
) string {
	// The season list tells a season TMDB does not have from a request that
	// failed, which the season request's own error cannot.
	details, err := getDetails(tmdbID)
	if err != nil {
		return tmdbRequestFailed
	}
	if !slices.ContainsFunc(details.Seasons, func(season tmdb.TVSeasonInfo) bool { return season.SeasonNumber == ids.Season }) {
		return tmdbSeasonUnlisted
	}
	season, err := getSeason(tmdbID, ids.Season)
	if err != nil {
		return tmdbRequestFailed
	}
	last := 0
	for _, ep := range season.Episodes {
		last = max(last, ep.EpisodeNumber)
		if ep.EpisodeNumber != ids.Episode {
			continue
		}
		switch ep.AirDate {
		case tvdbDate:
			return tmdbAgrees
		case "":
			return tmdbEpisodeUndated
		default:
			return tmdbDisagrees
		}
	}
	if ids.Episode > last+nightlySeasonEpisodes {
		return tmdbNumbersOtherwise
	}
	return tmdbEpisodeUnlisted
}

// tmdbNamesSameSeries reports whether TMDB's series is the one TVDB was asked
// about: TMDB maps it to the same TVDB id, or — when it maps it to none — to
// the same IMDb id the request carries.
func tmdbNamesSameSeries(ext *tmdb.ExternalIDsResponse, ids *session.AvailReportMeta) bool {
	if ext == nil {
		return false
	}
	if ext.TVDBID > 0 {
		return strconv.Itoa(ext.TVDBID) == ids.TvdbID
	}
	return ext.IMDbID != "" && ext.IMDbID == ids.ImdbID
}
