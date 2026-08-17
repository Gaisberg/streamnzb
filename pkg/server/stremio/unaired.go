package stremio

import (
	"context"
	"strconv"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/session"
)

// unairedMargin guards clock skew and "airs at midnight" date-only values: an
// episode only counts as unaired when its air time is comfortably in the
// future. Date-only air dates parse as midnight UTC of the air day, so an
// episode is treated as aired for its whole air date.
const unairedMargin = 15 * time.Minute

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
func (s *Server) episodeAiredState(ctx context.Context, contentType string, ids *session.AvailReportMeta) (aired bool, airsAt time.Time, known bool) {
	if s == nil || ids == nil || ids.Season <= 0 || ids.Episode <= 0 {
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
			if t, err := time.Parse("2006-01-02", ep.Aired); err == nil {
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
	overlay := s.tvmazeEpisodeOverlay(ctx, ids.ImdbID, ids.TvdbID)
	ep, ok := overlay[[2]int{ids.Season, ids.Episode}]
	if !ok {
		return time.Time{}, false
	}
	if ep.Airstamp != "" {
		if t, err := time.Parse(time.RFC3339, ep.Airstamp); err == nil {
			return t, true
		}
	}
	if ep.Airdate != "" {
		if t, err := time.Parse("2006-01-02", ep.Airdate); err == nil {
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
			if t, err := time.Parse("2006-01-02", ep.AirDate); err == nil {
				return t, true
			}
			return time.Time{}, false
		}
	}
	return time.Time{}, false
}
