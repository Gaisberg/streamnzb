package stremio

import (
	"context"
	"strconv"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/session"
)

// seasonCompletedState reports whether the requested season has finished
// airing — every episode the source lists for it is past its air window.
//
// It answers the one question the dynamic TV search scope asks: a finished
// season is where a season pack lives, so the search leads with the season and
// keeps the episode as its fallback; a season still airing leads with the
// episode. known is false whenever no source could say, and the caller must
// then treat the season as ongoing, which is the scope's episode-first default
// and matches what a non-adaptive scope would have done.
//
// An episode with no air date counts as not aired, so an announced-but-undated
// finale keeps its season ongoing. That errs toward the episode-first order,
// which is the cheaper mistake: the season search still runs as the fallback.
//
// Sources are the air-date authorities in the same order the unaired gate uses
// them: TVMaze, then TVDB episodes, then TMDB season details. Unlike that gate
// this is not user-gateable — it orders searches rather than suppressing them.
func (s *Server) seasonCompletedState(ctx context.Context, contentType string, ids *session.AvailReportMeta) (completed, known bool) {
	if s == nil || ids == nil || ids.Season <= 0 {
		return false, false
	}
	switch contentType {
	case "series", "anime", "tv":
	default:
		return false, false
	}

	if done, ok := s.tvmazeSeasonCompleted(ctx, ids); ok {
		return done, true
	}
	if done, ok := s.tvdbSeasonCompleted(ids); ok {
		return done, true
	}
	if done, ok := s.tmdbSeasonCompleted(ids); ok {
		return done, true
	}
	return false, false
}

// seasonScan accumulates the aired state of one season's episodes. It is the
// shared shape of the three source scans below: they differ only in how an
// episode's air window is read.
type seasonScan struct {
	episodes  int
	completed bool
}

func newSeasonScan() *seasonScan {
	return &seasonScan{completed: true}
}

// add folds one episode of the season into the scan. ok is false when the
// source stated no usable air date, which counts as not aired.
func (sc *seasonScan) add(w airWindow, ok bool) {
	sc.episodes++
	if !ok || !airedByTime(w.opensAt) {
		sc.completed = false
	}
}

// result reports the scan's verdict, and whether the source listed the season
// at all — an empty season is a source that cannot answer, not a finished one.
func (sc *seasonScan) result() (completed, known bool) {
	if sc.episodes == 0 {
		return false, false
	}
	return sc.completed, true
}

func (s *Server) tvmazeSeasonCompleted(ctx context.Context, ids *session.AvailReportMeta) (bool, bool) {
	overlay := s.tvmazeEpisodes(ctx, ids.ImdbID, ids.TvdbID)
	scan := newSeasonScan()
	for key, ep := range overlay {
		if key[0] != ids.Season {
			continue
		}
		scan.add(tvmazeEpisodeWindow(ep))
	}
	return scan.result()
}

func (s *Server) tvdbSeasonCompleted(ids *session.AvailReportMeta) (bool, bool) {
	rt := s.runtime()
	if rt.tvdbClient == nil || ids.TvdbID == "" {
		return false, false
	}
	episodes, err := rt.tvdbClient.GetSeriesEpisodes(ids.TvdbID)
	if err != nil {
		logger.Debug("TVDB episodes for season-state lookup failed", "tvdb_id", ids.TvdbID, "err", err)
		return false, false
	}
	scan := newSeasonScan()
	for _, ep := range episodes {
		if ep.SeasonNumber != ids.Season || ep.Number <= 0 {
			continue
		}
		scan.add(dateWindow(ep.Aired))
	}
	return scan.result()
}

func (s *Server) tmdbSeasonCompleted(ids *session.AvailReportMeta) (bool, bool) {
	rt := s.runtime()
	if rt.tmdbClient == nil || ids.TmdbID == "" {
		return false, false
	}
	tmdbID, err := strconv.Atoi(ids.TmdbID)
	if err != nil || tmdbID <= 0 {
		return false, false
	}
	details, err := rt.tmdbClient.GetTVSeasonDetails(tmdbID, ids.Season)
	if err != nil {
		logger.Debug("TMDB season details for season-state lookup failed", "tmdb_id", tmdbID, "season", ids.Season, "err", err)
		return false, false
	}
	scan := newSeasonScan()
	for _, ep := range details.Episodes {
		if ep.EpisodeNumber <= 0 {
			continue
		}
		scan.add(dateWindow(ep.AirDate))
	}
	return scan.result()
}
