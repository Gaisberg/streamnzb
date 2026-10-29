package jellyfin

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/server/stremio"
)

// Upcoming is the episodes still to air. Jellyfin lists every dated episode
// in its libraries from yesterday on; a catalog here is open-ended, so the
// list follows the series the stream follows instead — watched or a
// favourite — with the one episode each is waiting on: the next dated one
// after the furthest played, once nothing aired is left to watch. A series
// ParentId lists all of that series' dated episodes still to air, as
// Jellyfin's series page asks for them. Either way only episodes airing
// within upcomingWindow are listed, soonest first.

// upcomingWindow is how far ahead Upcoming looks. Jellyfin needs none, its
// library holding only what is announced; metadata sources date episodes
// months out, and without one the list becomes a schedule.
const upcomingWindow = 90 * 24 * time.Hour

type upcomingPick struct {
	id   itemID
	meta *stremio.MetaObject
	at   time.Time
}

// upcomingResult answers /Shows/Upcoming. The list is sorted by air date, so
// every candidate is looked at before a page is cut, and the total is exact.
// Jellyfin's own total is the size of the page it returns; clients find the
// end by a short page, which an exact total also gives them.
func (s *Server) upcomingResult(rq *request) queryResult {
	start := max(rq.intParam("startIndex", 0), 0)
	result := emptyResult()
	result.StartIndex = start
	limit := rq.intParam("limit", maxLimit)
	if limit <= 0 || limit > maxLimit {
		limit = maxLimit
	}
	picks := s.upcomingPicks(rq)
	// A scan the client abandoned is incomplete, so nothing it found is sure
	// to belong on the page.
	if rq.Context().Err() != nil {
		return result
	}
	slices.SortFunc(picks, func(a, b upcomingPick) int {
		return cmp.Or(a.at.Compare(b.at), strings.Compare(a.meta.Name, b.meta.Name),
			strings.Compare(a.id.series().encode(), b.id.series().encode()),
			cmp.Compare(a.id.Season, b.id.Season), cmp.Compare(a.id.Episode, b.id.Episode))
	})
	result.TotalRecordCount = len(picks)
	if start >= len(picks) {
		return result
	}
	for _, p := range picks[start:min(start+limit, len(picks))] {
		if item := s.itemOf(rq, p.id, p.meta); item != nil {
			result.Items = append(result.Items, item)
		}
	}
	return result
}

// upcomingPicks finds the episodes ParentId asks for: a series' or season's
// own, the followed series inside a library, or with no ParentId every
// followed series. Any other ParentId, as in Jellyfin, holds no episodes.
func (s *Server) upcomingPicks(rq *request) []upcomingPick {
	from := now()
	inWindow := func(v stremio.MetaVideo) (time.Time, bool) {
		at, ok := parseISOTime(v.Released)
		return at, ok && at.After(from) && !at.After(from.Add(upcomingWindow))
	}
	if parent, err := decodeItemID(rq.param("parentId")); err == nil && (parent.Kind == kindSeries || parent.Kind == kindSeason) {
		meta, err := s.meta(rq.Context(), rq, parent)
		if err != nil {
			return nil
		}
		var picks []upcomingPick
		for _, v := range numberedEpisodes(meta.Videos) {
			if parent.Kind == kindSeason && v.Season != parent.Season {
				continue
			}
			if at, ok := inWindow(v); ok {
				picks = append(picks, upcomingPick{parent.episode(v.Season, v.Episode), meta, at})
			}
		}
		return picks
	}

	if s.opts.Playstate == nil {
		return nil
	}
	inScope, ok := s.nextUpScope(rq)
	if !ok {
		return nil
	}
	// Watched and favourite series are bounded apart, so a long history
	// cannot crowd the favourites out; a series in both is looked at once.
	bySeries := s.seriesStates(rq)
	series := newestSeries(bySeries, func(sr *nextUpSeries) bool {
		return !sr.last.IsZero() && inScope(sr.id)
	})
	watched := map[string]bool{}
	for _, sr := range series {
		watched[sr.id.encode()] = true
	}
	series = append(series, newestSeries(bySeries, func(sr *nextUpSeries) bool {
		return sr.favorite && !watched[sr.id.encode()] && inScope(sr.id)
	})...)
	ids := make([]itemID, len(series))
	for i, sr := range series {
		ids[i] = sr.id
	}
	metas := s.prefetchMetas(rq, ids)
	var picks []upcomingPick
	for _, sr := range series {
		// A series whose metadata failed is left out, not the listing.
		meta := metas[metaKey(sr.id)]
		if meta == nil {
			continue
		}
		if v, ok := upcomingVideo(meta.Videos, sr.states, from); ok {
			if at, ok := inWindow(v); ok {
				picks = append(picks, upcomingPick{sr.id.episode(v.Season, v.Episode), meta, at})
			}
		}
	}
	return picks
}

// upcomingVideo is the episode a series is waiting on: none while it has a
// Next Up, resumable or not, since an aired episode is left to watch;
// otherwise the first dated episode after the furthest played that is still
// to air. Undated and special episodes are never it. Dates are read as Next
// Up reads them, even where playback contradicts them: an undated episode
// after a played episode dated in the future is passed over for the next
// dated one, and an episode part-way through whose date is still to come is
// the one waited on, carrying its position.
//
// Still to air means after from, the request's own time. It is read before
// nextUpVideo reads the clock, so an episode Next Up found unaired is
// unaired at from too, and one airing while the request runs is never
// passed over for a later one.
func upcomingVideo(videos []stremio.MetaVideo, states map[episodeKey]persistence.JellyfinPlaystate, from time.Time) (stremio.MetaVideo, bool) {
	if _, ok := nextUpVideo(videos, states, true); ok {
		return stremio.MetaVideo{}, false
	}
	anchor, anchored := furthestPlayed(states)
	for _, v := range numberedEpisodes(videos) {
		key := episodeKey{v.Season, v.Episode}
		if anchored && slices.Compare(key[:], anchor[:]) <= 0 {
			continue
		}
		if at, ok := parseISOTime(v.Released); ok && at.After(from) {
			return v, true
		}
	}
	return stremio.MetaVideo{}, false
}
