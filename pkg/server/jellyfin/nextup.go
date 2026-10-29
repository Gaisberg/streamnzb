package jellyfin

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/server/stremio"
)

// Next Up is the episode to watch next in each series the stream has been
// watching, built the way Jellyfin builds it from the stream's own play
// state: anchored on the furthest episode played, it is the first episode
// after that one that is neither played nor unaired, specials left out.
// Series are ordered by when they were last watched.

// nextUpMaxSeries bounds how many of the most recently watched series one
// request may look at, since each needs its episode list. Jellyfin has no such
// bound; a series watched less recently than this many others is not listed.
const nextUpMaxSeries = 100

// episodeKey is an episode's place in its series: season, then episode.
type episodeKey [2]int

// nextUpSeries is one series the stream has history in.
type nextUpSeries struct {
	id     itemID
	states map[episodeKey]persistence.JellyfinPlaystate
	// last is the newest LastPlayedAt among the episodes actually played or
	// part-played; zero when there are none.
	last time.Time
	// favorite is set when the series itself is a favourite.
	favorite bool
}

// nextUpResult answers /Shows/NextUp. Series are looked at newest first, a
// batch of episode lists at a time through prefetchMetas, and only until the
// page is filled: every series would have to be looked at to count them all,
// and a home screen asks for a dozen. So the total is exact once the
// candidates run out, and otherwise the picks found so far, at least the page
// plus one: a series past the page is looked for, and its pick is what says
// another page exists. The total is never more than there are, so a client
// that pages on it never asks for an empty page.
func (s *Server) nextUpResult(rq *request) queryResult {
	start := max(rq.intParam("startIndex", 0), 0)
	result := emptyResult()
	result.StartIndex = start
	if s.opts.Playstate == nil {
		return result
	}
	limit := rq.intParam("limit", maxLimit)
	if limit <= 0 || limit > maxLimit {
		limit = maxLimit
	}
	// Both default to true in Jellyfin, so only an explicit false turns
	// them off.
	includeResumable := !strings.EqualFold(rq.param("enableResumable"), "false")
	wantTotal := !strings.EqualFold(rq.param("enableTotalRecordCount"), "false")
	need := start + limit
	if wantTotal {
		need++
	}

	series := s.nextUpCandidates(rq)
	type pick struct {
		id   itemID
		meta *stremio.MetaObject
	}
	var picks []pick
	for len(series) > 0 && len(picks) < need && rq.Context().Err() == nil {
		batch := series[:min(len(series), max(need-len(picks), metaFetchLimit))]
		series = series[len(batch):]
		ids := make([]itemID, len(batch))
		for i, sr := range batch {
			ids[i] = sr.id
		}
		metas := s.prefetchMetas(rq, ids)
		for _, sr := range batch {
			// A series whose metadata failed is left out, not the listing.
			meta := metas[metaKey(sr.id)]
			if meta == nil {
				continue
			}
			// meta.Videos, not videosOf: a Kitsu film has no episode list but
			// the one videosOf makes up for it, and is not a series to go on
			// with.
			if v, ok := nextUpVideo(meta.Videos, sr.states, includeResumable); ok {
				picks = append(picks, pick{sr.id.episode(v.Season, v.Episode), meta})
			}
		}
	}
	if wantTotal {
		result.TotalRecordCount = len(picks)
	}
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

// nextUpCandidates is the series Next Up looks at, most recently watched
// first. SeriesId naming a series asks about that one, watched or not, as
// Jellyfin's series page does; any other SeriesId is ignored, as Jellyfin
// ignores an id that is not a series. Otherwise it is every series with an
// episode played or part-played, since nextUpDateCutoff when one is given,
// inside ParentId.
func (s *Server) nextUpCandidates(rq *request) []nextUpSeries {
	bySeries := s.seriesStates(rq)
	if id, err := decodeItemID(rq.param("seriesId")); err == nil && id.Kind == kindSeries {
		if sr := bySeries[id.encode()]; sr != nil {
			return []nextUpSeries{*sr}
		}
		return []nextUpSeries{{id: id}}
	}

	inScope, ok := s.nextUpScope(rq)
	if !ok {
		return nil
	}
	cutoff := nextUpCutoff(rq.param("nextUpDateCutoff"))
	return newestSeries(bySeries, func(sr *nextUpSeries) bool {
		return !sr.last.IsZero() && !sr.last.Before(cutoff) && inScope(sr.id)
	})
}

// seriesStates groups the stream's episode states by series, and marks the
// series that are favourites themselves.
func (s *Server) seriesStates(rq *request) map[string]*nextUpSeries {
	bySeries := map[string]*nextUpSeries{}
	entry := func(id itemID) *nextUpSeries {
		key := id.series().encode()
		sr := bySeries[key]
		if sr == nil {
			sr = &nextUpSeries{id: id.series(), states: map[episodeKey]persistence.JellyfinPlaystate{}}
			bySeries[key] = sr
		}
		return sr
	}
	for _, st := range s.opts.Playstate.ListForStream(rq.streamName()) {
		id, err := decodeItemID(st.ItemID)
		if err != nil {
			continue
		}
		switch {
		case id.Kind == kindSeries && st.IsFavorite:
			entry(id).favorite = true
		case id.Kind == kindEpisode:
			sr := entry(id)
			sr.states[episodeKey{id.Season, id.Episode}] = st
			// A favourite-only or unmarked row says nothing about watching.
			if (st.Played || st.PositionTicks > 0) && st.LastPlayedAt.After(sr.last) {
				sr.last = st.LastPlayedAt
			}
		}
	}
	return bySeries
}

// newestSeries keeps the series keep admits, most recently watched first,
// up to nextUpMaxSeries.
func newestSeries(bySeries map[string]*nextUpSeries, keep func(*nextUpSeries) bool) []nextUpSeries {
	var out []nextUpSeries
	for _, sr := range bySeries {
		if keep(sr) {
			out = append(out, *sr)
		}
	}
	slices.SortFunc(out, func(a, b nextUpSeries) int {
		return cmp.Or(b.last.Compare(a.last), strings.Compare(a.id.encode(), b.id.encode()))
	})
	return out[:min(len(out), nextUpMaxSeries)]
}

// nextUpScope is what ParentId admits. Jellyfin narrows Next Up by library
// only: any other parent, a series or season included, matches no library
// and lists nothing. A library here is a catalog, and a series does not
// remember which catalog it was found in, so a library admits the series of
// its content type.
func (s *Server) nextUpScope(rq *request) (func(itemID) bool, bool) {
	raw := rq.param("parentId")
	if raw == "" {
		return func(itemID) bool { return true }, true
	}
	parent, err := decodeItemID(raw)
	if err != nil || parent.Kind != kindView {
		return nil, false
	}
	def, ok := s.enabledCatalogByID(rq, parent.CatalogID)
	if !ok {
		return nil, false
	}
	return func(id itemID) bool { return id.ContentType == def.Type }, true
}

// nextUpCutoff reads nextUpDateCutoff, and the zero time — no cutoff — when
// there is none it can read. A timestamp without a zone is read as UTC, the
// zone Jellyfin keeps play dates in and compares it against.
func nextUpCutoff(raw string) time.Time {
	if t, ok := parseISOTime(raw); ok {
		return t
	}
	t, _ := time.Parse("2006-01-02T15:04:05", strings.TrimSpace(raw))
	return t
}

// nextUpVideo picks a series' Next Up episode from its episode list and the
// stream's state on it. Specials are left out, as Jellyfin leaves them out
// when it has no airs-before placement for them, which no source here
// supplies. The anchor is the furthest episode played, found from the stored
// state rather than the list, so an episode the metadata no longer has still
// counts; with none played, the series starts from its first episode.
//
// Unaired episodes are passed over the way Jellyfin passes over its virtual
// ones. An undated episode takes the state of the last dated one before it,
// so the undated rest of a season whose premiere has not aired is unaired
// too, while one with nothing dated before it, or an aired episode, counts as
// aired. A season that starts undated in a series with dates elsewhere is the
// exception: it is unaired unless an episode after it has aired, as TMDB and
// TVDB list an announced season before they date it. A pick the stream is
// part-way through is offered only when includeResumable, and otherwise the
// series has no Next Up rather than a later episode.
func nextUpVideo(videos []stremio.MetaVideo, states map[episodeKey]persistence.JellyfinPlaystate, includeResumable bool) (stremio.MetaVideo, bool) {
	anchor, anchored := furthestPlayed(states)
	episodes := numberedEpisodes(videos)
	// lastAired is the furthest episode dated in the past.
	var lastAired episodeKey
	anyDated, anyAired := false, false
	for _, v := range episodes {
		if _, ok := parseISOTime(v.Released); ok {
			anyDated = true
			if !unaired(v.Released) {
				lastAired, anyAired = episodeKey{v.Season, v.Episode}, true
			}
		}
	}
	notAired := false
	for i, v := range episodes {
		key := episodeKey{v.Season, v.Episode}
		if _, dated := parseISOTime(v.Released); dated {
			notAired = unaired(v.Released)
		} else if anyDated && i > 0 && v.Season != episodes[i-1].Season {
			// Sources list an announced season before dating it, so an undated
			// season has aired only when an episode after it has.
			notAired = !anyAired || slices.Compare(key[:], lastAired[:]) > 0
		}
		st := states[key]
		if (anchored && slices.Compare(key[:], anchor[:]) <= 0) || st.Played || notAired {
			continue
		}
		if st.PositionTicks > 0 && !includeResumable {
			return stremio.MetaVideo{}, false
		}
		return v, true
	}
	return stremio.MetaVideo{}, false
}

// furthestPlayed is the furthest numbered episode the stream has played.
func furthestPlayed(states map[episodeKey]persistence.JellyfinPlaystate) (episodeKey, bool) {
	var anchor episodeKey
	anchored := false
	for key, st := range states {
		if st.Played && key[0] != 0 && (!anchored || slices.Compare(key[:], anchor[:]) > 0) {
			anchor, anchored = key, true
		}
	}
	return anchor, anchored
}

// numberedEpisodes is a copy of videos in episode order, specials left out.
// An episode the metadata lists twice is its first listing, the one itemOf
// renders, so a later listing never dates it.
func numberedEpisodes(videos []stremio.MetaVideo) []stremio.MetaVideo {
	episodes := slices.DeleteFunc(slices.Clone(videos), func(v stremio.MetaVideo) bool { return v.Season == 0 })
	slices.SortStableFunc(episodes, func(a, b stremio.MetaVideo) int {
		return cmp.Or(cmp.Compare(a.Season, b.Season), cmp.Compare(a.Episode, b.Episode))
	})
	return slices.CompactFunc(episodes, func(a, b stremio.MetaVideo) bool {
		return a.Season == b.Season && a.Episode == b.Episode
	})
}
