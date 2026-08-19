package stremio

import (
	"context"
	"strconv"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/session"
)

// unairedMargin guards clock skew: an episode only counts as unaired when the
// gate opens comfortably in the future.
const unairedMargin = 15 * time.Minute

// airDateLayout is the date-only form every source falls back to.
const airDateLayout = "2006-01-02"

// Air times arrive in two shapes and the gate treats them differently.
//
// A TVMaze airstamp carries a real broadcast time and its network's offset, so
// it names an actual instant: gate on it directly and the search opens when the
// episode is genuinely out, not hours before. Everything else — TVMaze's
// date-only airdate, and what TVDB and TMDB serve — is a bare calendar date
// with no time behind it, and the only honest reading of "2026-08-19" is the
// whole of that day.
//
// The bug this replaces: a bare date was parsed as midnight UTC, which is the
// previous evening in the Americas and mid-morning east of Greenwich, so
// whether an episode counted as aired on its own air date depended on where the
// server happened to sit. parseAirDate reads it as midnight in the server's own
// zone instead, which makes the date mean the same day everywhere.

// parseAirDate reads a bare "2006-01-02" air date as the start of that day in
// the server's local zone.
func parseAirDate(raw string) (time.Time, bool) {
	t, err := time.ParseInLocation(airDateLayout, raw, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// localMidnightOn rebuilds a calendar date — read in whatever zone t carries —
// as the start of that same date locally.
func localMidnightOn(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

// episodeAiredState reports whether the requested episode has aired.
//
// known is true only when a trusted source positively stated an air time —
// any lookup error, missing mapping, or missing episode leaves known=false,
// and the caller must fail open into a normal search. Getting this wrong in
// the other direction hides real releases behind a metadata gap.
//
// Sources, in order: TVMaze (the air-date authority — airstamps carry the
// actual air time), then TVDB episodes and TMDB season details (date-only
// fallbacks, mirroring the meta source policy). Kitsu numbering is
// entry-relative and cannot be matched to the resolved season/episode safely,
// so anime relies on the same sources via its mapped ids.
//
// None of that depends on a stream having metadata enabled. The ids come from
// the search request's own metadata resolution, which every stream request
// does anyway, and the sources are queried directly — a stream serving the
// bare stream-only manifest is gated exactly like one serving catalogs.
//
// The stream can turn the gate off (unaired_search_gate, in its indexer
// settings); that reports known=false and searches as usual.
func (s *Server) episodeAiredState(ctx context.Context, stream *auth.Stream, contentType string, ids *session.AvailReportMeta) (aired bool, airsAt time.Time, known bool) {
	if s == nil || ids == nil || ids.Season <= 0 || ids.Episode <= 0 {
		return false, time.Time{}, false
	}
	if !stream.EffectiveUnairedSearchGate() {
		return false, time.Time{}, false
	}
	switch contentType {
	case "series", "anime", "tv":
	default:
		return false, time.Time{}, false
	}

	if airsAt, ok := s.tvmazeAirTime(ctx, ids); ok {
		return airedByTime(airsAt), airsAt, true
	}
	if airsAt, ok := s.tvdbAirTime(ids); ok {
		return airedByTime(airsAt), airsAt, true
	}
	if airsAt, ok := s.tmdbAirTime(ids); ok {
		return airedByTime(airsAt), airsAt, true
	}
	return false, time.Time{}, false
}

func (s *Server) tvdbAirTime(ids *session.AvailReportMeta) (time.Time, bool) {
	if s.tvdbClient == nil || ids.TvdbID == "" {
		return time.Time{}, false
	}
	episodes, err := s.tvdbClient.GetSeriesEpisodes(ids.TvdbID)
	if err != nil {
		logger.Debug("TVDB episodes for air-date gating failed", "tvdb_id", ids.TvdbID, "err", err)
		return time.Time{}, false
	}
	for _, ep := range episodes {
		if ep.SeasonNumber == ids.Season && ep.Number == ids.Episode {
			if t, ok := parseAirDate(ep.Aired); ok {
				return t, true
			}
			return time.Time{}, false
		}
	}
	return time.Time{}, false
}

func airedByTime(airsAt time.Time) bool {
	return !airsAt.After(time.Now().Add(unairedMargin))
}

func (s *Server) tvmazeAirTime(ctx context.Context, ids *session.AvailReportMeta) (time.Time, bool) {
	overlay := s.tvmazeEpisodes(ctx, ids.ImdbID, ids.TvdbID)
	ep, ok := overlay[[2]int{ids.Season, ids.Episode}]
	if !ok {
		return time.Time{}, false
	}
	if ep.Airstamp != "" {
		if t, err := time.Parse(time.RFC3339, ep.Airstamp); err == nil {
			// TVMaze builds the airstamp from the air date, the air time and
			// the network's zone. Shows it holds no air time for still get an
			// airstamp — one landing exactly on midnight in the network's own
			// zone — so that is a date in disguise, not a broadcast at
			// 00:00, and it gets the date-only reading rather than gating the
			// search until midnight somewhere else in the world.
			if h, m, sec := t.Clock(); h == 0 && m == 0 && sec == 0 {
				return localMidnightOn(t), true
			}
			return t, true
		}
	}
	if ep.Airdate != "" {
		if t, ok := parseAirDate(ep.Airdate); ok {
			return t, true
		}
	}
	return time.Time{}, false
}

func (s *Server) tmdbAirTime(ids *session.AvailReportMeta) (time.Time, bool) {
	if s.tmdbClient == nil || ids.TmdbID == "" {
		return time.Time{}, false
	}
	tmdbID, err := strconv.Atoi(ids.TmdbID)
	if err != nil || tmdbID <= 0 {
		return time.Time{}, false
	}
	details, err := s.tmdbClient.GetTVSeasonDetails(tmdbID, ids.Season)
	if err != nil {
		logger.Debug("TMDB season details for air-date gating failed", "tmdb_id", tmdbID, "season", ids.Season, "err", err)
		return time.Time{}, false
	}
	for _, ep := range details.Episodes {
		if ep.EpisodeNumber == ids.Episode {
			if t, ok := parseAirDate(ep.AirDate); ok {
				return t, true
			}
			return time.Time{}, false
		}
	}
	return time.Time{}, false
}
