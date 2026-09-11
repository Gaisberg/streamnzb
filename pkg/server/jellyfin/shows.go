package jellyfin

import (
	"net/http"
	"strconv"
)

// serveShows answers the /Shows routes clients use for a series' seasons
// and episodes. Next Up needs an episode order the addon's playback history
// does not keep per stream, so it is empty and clients fall back to Resume.
func (s *Server) serveShows(w http.ResponseWriter, rq *request) bool {
	if !rq.is(http.MethodGet) {
		return false
	}
	if _, ok := rq.match("shows", "nextup"); ok {
		writeJSON(w, http.StatusOK, emptyResult())
		return true
	}
	if _, ok := rq.match("shows", "upcoming"); ok {
		writeJSON(w, http.StatusOK, emptyResult())
		return true
	}
	c, ok := rq.match("shows", "*", "*")
	if !ok {
		return false
	}
	raw, which := c[0], c[1]
	if which != "seasons" && which != "episodes" {
		return false
	}
	id, err := decodeItemID(raw)
	if err != nil || (id.Kind != kindSeries && id.Kind != kindSeason && id.Kind != kindEpisode) {
		http.NotFound(w, rq.Request)
		return true
	}
	series := id.series()
	var items []*baseItem
	if which == "seasons" {
		items, err = s.childrenOf(rq, series, nil, false)
	} else {
		// Episodes of one season when the client names it (by id or number),
		// otherwise the whole series.
		scope := series
		if seasonID := rq.param("seasonId"); seasonID != "" {
			if sid, err := decodeItemID(seasonID); err == nil && sid.Kind == kindSeason {
				scope = sid
			}
		} else if season := rq.param("season"); season != "" {
			if n, err := strconv.Atoi(season); err == nil {
				scope = series.season(n)
			}
		}
		items, err = s.childrenOf(rq, scope, []string{"episode"}, true)
	}
	if err != nil {
		s.writeItemError(w, rq, raw, err)
		return true
	}
	start := max(rq.intParam("startIndex", 0), 0)
	limit := rq.intParam("limit", maxLimit)
	if limit <= 0 {
		limit = maxLimit
	}
	writeJSON(w, http.StatusOK, page(items, start, limit, false))
	return true
}
