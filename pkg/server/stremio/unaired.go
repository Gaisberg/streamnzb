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

// maxZoneOffset is the furthest ahead of UTC any inhabited zone runs (+14:00,
// the Line Islands). A calendar date therefore begins maxZoneOffset before
// midnight UTC on that date, and not one instant earlier anywhere on Earth.
const maxZoneOffset = 14 * time.Hour

// The gate has two clocks and they are deliberately not the same one.
//
// What it gates on is the earliest instant the episode could exist anywhere.
// A release cannot precede its own broadcast, so that instant is the start of
// the air date in the furthest-east zone — nothing airs before its own date
// begins. Gating on anything later leaves a window where a real release is on
// the indexers and the gate still reports the episode as unaired, which is the
// one failure mode that costs a user a playable stream. Opening early costs a
// single fan-out that finds nothing.
//
// What it reports is the scheduled air time, when a source actually knows one.
// That is what the "airs at" line in a stream description should say, and it
// has no business deciding whether to search.
//
// The trap this replaces: TVMaze emits an airstamp for every episode, including
// the ones it holds no air time for, and those get stamped noon UTC (Silo,
// Loki, Queen Sono — every country-less web channel). Read as a broadcast
// instant, that held the gate shut until midday UTC on episodes the streamer
// had already dropped. Airstamps are also normalised to UTC rather than the
// network's own offset, so nothing about the stamp's clock reveals which kind
// it is. Only the airtime field does.

// airWindow is one episode's air time in the forms the gate needs.
type airWindow struct {
	// opensAt is the earliest instant the episode could have aired anywhere.
	opensAt time.Time
	// date is midnight UTC on the stated air date.
	date time.Time
	// scheduled is the broadcast instant a source positively stated, and is
	// zero when the source knew only a date.
	scheduled time.Time
}

// reportAt is what to tell the user: the broadcast instant when one is known,
// and otherwise the air date alone. Never opensAt — that runs up to a day
// ahead of the air date by design, and reporting it would announce an air
// time no source ever stated.
func (w airWindow) reportAt() (at time.Time, timeKnown bool) {
	if !w.scheduled.IsZero() {
		return w.scheduled, true
	}
	return w.date, false
}

// dateWindow builds the window for a bare "2006-01-02" air date: the gate
// opens when that date begins in the furthest-east zone, and there is no
// scheduled instant to report.
func dateWindow(raw string) (airWindow, bool) {
	day, err := time.ParseInLocation(airDateLayout, raw, time.UTC)
	if err != nil {
		return airWindow{}, false
	}
	return airWindow{opensAt: day.Add(-maxZoneOffset), date: day}, true
}

// withSchedule pairs a known broadcast instant with the date it belongs to.
// The gate still opens on the date, so a network airing late enough to land on
// the following UTC day cannot pull the gate shut over its own air date.
func withSchedule(scheduled time.Time, airdate string) airWindow {
	w, ok := dateWindow(airdate)
	if !ok {
		w = airWindow{opensAt: scheduled, date: scheduled.UTC().Truncate(24 * time.Hour)}
	}
	w.scheduled = scheduled
	if scheduled.Before(w.opensAt) {
		w.opensAt = scheduled
	}
	return w
}

// episodeAiredState reports whether the requested episode has aired.
//
// known is true only when a trusted source positively stated an air date —
// any lookup error, missing mapping, or missing episode leaves known=false,
// and the caller must fail open into a normal search. Getting this wrong in
// the other direction hides real releases behind a metadata gap.
//
// Sources, in order: TVMaze (the air-date authority — it alone carries a real
// broadcast time), then TVDB episodes and TMDB season details (date-only
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
func (s *Server) episodeAiredState(ctx context.Context, stream *auth.Stream, contentType string, ids *session.AvailReportMeta) (aired bool, window airWindow, known bool) {
	if s == nil || ids == nil || ids.Season <= 0 || ids.Episode <= 0 {
		return false, airWindow{}, false
	}
	if !stream.EffectiveUnairedSearchGate() {
		return false, airWindow{}, false
	}
	switch contentType {
	case "series", "anime", "tv":
	default:
		return false, airWindow{}, false
	}

	if w, ok := s.tvmazeAirWindow(ctx, ids); ok {
		return airedByTime(w.opensAt), w, true
	}
	if w, ok := s.tvdbAirWindow(ids); ok {
		return airedByTime(w.opensAt), w, true
	}
	if w, ok := s.tmdbAirWindow(ids); ok {
		return airedByTime(w.opensAt), w, true
	}
	return false, airWindow{}, false
}

func (s *Server) tvdbAirWindow(ids *session.AvailReportMeta) (airWindow, bool) {
	rt := s.runtime()
	if rt.tvdbClient == nil || ids.TvdbID == "" {
		return airWindow{}, false
	}
	episodes, err := rt.tvdbClient.GetSeriesEpisodes(ids.TvdbID)
	if err != nil {
		logger.Debug("TVDB episodes for air-date gating failed", "tvdb_id", ids.TvdbID, "err", err)
		return airWindow{}, false
	}
	for _, ep := range episodes {
		if ep.SeasonNumber == ids.Season && ep.Number == ids.Episode {
			return dateWindow(ep.Aired)
		}
	}
	return airWindow{}, false
}

func airedByTime(opensAt time.Time) bool {
	return !opensAt.After(time.Now().Add(unairedMargin))
}

func (s *Server) tvmazeAirWindow(ctx context.Context, ids *session.AvailReportMeta) (airWindow, bool) {
	overlay := s.tvmazeEpisodes(ctx, ids.ImdbID, ids.TvdbID)
	ep, ok := overlay[[2]int{ids.Season, ids.Episode}]
	if !ok {
		return airWindow{}, false
	}
	// A non-empty airtime is TVMaze saying it knows when this one broadcasts,
	// and only then does the airstamp name a real instant. With airtime empty
	// the stamp is a noon-UTC placeholder and the airdate is all there is.
	if ep.Airtime != "" && ep.Airstamp != "" {
		if t, err := time.Parse(time.RFC3339, ep.Airstamp); err == nil {
			return withSchedule(t, ep.Airdate), true
		}
	}
	return dateWindow(ep.Airdate)
}

func (s *Server) tmdbAirWindow(ids *session.AvailReportMeta) (airWindow, bool) {
	rt := s.runtime()
	if rt.tmdbClient == nil || ids.TmdbID == "" {
		return airWindow{}, false
	}
	tmdbID, err := strconv.Atoi(ids.TmdbID)
	if err != nil || tmdbID <= 0 {
		return airWindow{}, false
	}
	details, err := rt.tmdbClient.GetTVSeasonDetails(tmdbID, ids.Season)
	if err != nil {
		logger.Debug("TMDB season details for air-date gating failed", "tmdb_id", tmdbID, "season", ids.Season, "err", err)
		return airWindow{}, false
	}
	for _, ep := range details.Episodes {
		if ep.EpisodeNumber == ids.Episode {
			return dateWindow(ep.AirDate)
		}
	}
	return airWindow{}, false
}
