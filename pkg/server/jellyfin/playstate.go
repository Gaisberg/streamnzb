package jellyfin

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/server/stremio"
)

// Progress is the one thing the layer keeps. Jellyfin clients report their
// position every few seconds while playing; writing each report would put a
// database write on every tick of every viewer, so ticks are held back to
// one write per item every progressInterval, while the events that carry a
// position the user will come back to — start, pause, stop, an explicit
// mark — write straight away.

const (
	// progressInterval is the least time between two tick writes for one
	// item.
	progressInterval = 30 * time.Second
	// playedThreshold is how far in a position counts as watched. Jellyfin's
	// own default is 90%.
	playedThreshold = 0.9
)

type playEvent int

const (
	playEventStart playEvent = iota
	playEventTick
	playEventPause
	playEventStop
)

type progressDebounce struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func newProgressDebounce() *progressDebounce {
	return &progressDebounce{last: make(map[string]time.Time)}
}

// due reports whether a tick for a key may write now, and records it if so.
func (d *progressDebounce) due(key string, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if last, ok := d.last[key]; ok && now.Sub(last) < progressInterval {
		return false
	}
	if len(d.last) > 10_000 {
		d.last = make(map[string]time.Time)
	}
	d.last[key] = now
	return true
}

func (d *progressDebounce) forget(key string) {
	d.mu.Lock()
	delete(d.last, key)
	d.mu.Unlock()
}

// forgetPrefix forgets every key that starts with prefix.
func (d *progressDebounce) forgetPrefix(prefix string) {
	d.mu.Lock()
	for key := range d.last {
		if strings.HasPrefix(key, prefix) {
			delete(d.last, key)
		}
	}
	d.mu.Unlock()
}

// runtimeTicksFor finds an item's runtime for the played threshold: the
// candidate list the PlaybackInfo built (the probed duration of a candidate
// beats the provider's runtime), then what was stored before, then the
// metadata itself.
func (s *Server) runtimeTicksFor(rq *request, id itemID, stored int64) int64 {
	if view, ok := s.opts.Catalog.PlaylistCached(rq.stream, id.ContentType, id.playStremioID()); ok {
		for _, entry := range view.Entries {
			if entry.Caps != nil && entry.Caps.DurationSeconds > 0 {
				return int64(entry.Caps.DurationSeconds * float64(ticksPerSecond))
			}
		}
		if view.RuntimeSeconds > 0 {
			return int64(view.RuntimeSeconds * float64(ticksPerSecond))
		}
	}
	if stored > 0 {
		return stored
	}
	if meta, err := s.meta(rq.Context(), rq, id); err == nil {
		if ticks := runtimeTicks(meta.Runtime); ticks != nil {
			return *ticks
		}
	}
	return 0
}

// recordProgress applies one report to the stored state.
func (s *Server) recordProgress(rq *request, id itemID, position int64, event playEvent) {
	if s.opts.Playstate == nil || position < 0 {
		return
	}
	encoded := id.encode()
	key := rq.streamName() + "|" + encoded
	now := time.Now()
	if event == playEventTick {
		if !s.progress.due(key, now) {
			return
		}
	} else {
		s.progress.forget(key)
	}

	prev, _ := s.opts.Playstate.Get(rq.streamName(), encoded)
	state := persistence.JellyfinPlaystate{
		StreamName:    rq.streamName(),
		ItemID:        encoded,
		ContentType:   id.ContentType,
		ContentID:     id.playStremioID(),
		PositionTicks: position,
		RuntimeTicks:  s.runtimeTicksFor(rq, id, prev.RuntimeTicks),
		Played:        prev.Played,
		PlayCount:     prev.PlayCount,
		LastPlayedAt:  now,
	}
	if event == playEventStart {
		state.PlayCount++
	}
	if watched(state.PositionTicks, state.RuntimeTicks) {
		state.Played = true
		state.PositionTicks = 0
	}
	if err := s.opts.Playstate.Upsert(state); err != nil {
		logger.Warn("Jellyfin play state write failed", "stream", rq.streamName(), "item", encoded, "err", err)
		return
	}
	logger.Debug("Jellyfin play state", "stream", rq.streamName(), "content", state.ContentID, "event", event, "position_s", state.PositionTicks/ticksPerSecond, "played", state.Played)
}

// watched decides whether a position means the item has been seen through.
// Without a runtime there is no threshold, and the item stays unwatched
// until the user marks it.
func watched(position, runtime int64) bool {
	return runtime > 0 && position > 0 && float64(position) >= playedThreshold*float64(runtime)
}

// setPlayed marks a movie or episode watched or unwatched. Its error is
// returned so the handler can refuse the request, as setFavorite's is.
func (s *Server) setPlayed(rq *request, id itemID, played bool) error {
	if s.opts.Playstate == nil {
		return nil
	}
	encoded := id.encode()
	if err := s.opts.Playstate.SetPlayed(rq.streamName(), encoded, id.ContentType, id.playStremioID(), played); err != nil {
		logger.Warn("Jellyfin played mark failed", "stream", rq.streamName(), "item", encoded, "err", err)
		return err
	}
	s.progress.forget(rq.streamName() + "|" + encoded)
	return nil
}

// markFolderPlayed marks a season's or series' aired episodes watched
// (folderEpisodes), in one transaction, only if the request is still wanted
// when it starts.
func (s *Server) markFolderPlayed(rq *request, id itemID, meta *stremio.MetaObject) error {
	if s.opts.Playstate == nil {
		return nil
	}
	if err := rq.Context().Err(); err != nil {
		return err
	}
	episodes := folderEpisodes(id, meta)
	rows := make([]persistence.JellyfinPlaystate, len(episodes))
	for i, ep := range episodes {
		rows[i] = persistence.JellyfinPlaystate{ItemID: ep.encode(), ContentType: ep.ContentType, ContentID: ep.playStremioID()}
	}
	err := s.opts.Playstate.MarkPlayedMany(rq.streamName(), rows)
	// Whatever this request read of the series before is stale either way.
	delete(rq.prefixed, id.series().episodesPrefix())
	if err != nil {
		logger.Warn("Jellyfin played mark failed", "stream", rq.streamName(), "item", id.encode(), "episodes", len(rows), "err", err)
		return err
	}
	for _, row := range rows {
		s.progress.forget(rq.streamName() + "|" + row.ItemID)
	}
	logger.Debug("Jellyfin played mark", "stream", rq.streamName(), "item", id.encode(), "played", true, "episodes", len(rows))
	return nil
}

// clearFolderPlayed marks unwatched every episode of a season or series the
// stream has state on, which needs no metadata, and reports how many it
// changed. The store picks the rows as it clears them (ClearPlayedPrefix),
// so an episode played between the request and the write is cleared too;
// the request has to be still wanted when it starts.
func (s *Server) clearFolderPlayed(rq *request, id itemID) (int64, error) {
	if s.opts.Playstate == nil {
		return 0, nil
	}
	if err := rq.Context().Err(); err != nil {
		return 0, err
	}
	prefix := id.episodesPrefix()
	cleared, err := s.opts.Playstate.ClearPlayedPrefix(rq.streamName(), prefix)
	delete(rq.prefixed, id.series().episodesPrefix())
	if err != nil {
		logger.Warn("Jellyfin played clear failed", "stream", rq.streamName(), "item", id.encode(), "err", err)
		return 0, err
	}
	s.progress.forgetPrefix(rq.streamName() + "|" + prefix)
	logger.Debug("Jellyfin played mark", "stream", rq.streamName(), "item", id.encode(), "played", false, "episodes", cleared)
	return cleared, nil
}

// folderListed reports whether the metadata lists a season or series: a
// series is listed by its metadata being there, a season by its episodes.
func folderListed(id itemID, meta *stremio.MetaObject) bool {
	return id.Kind == kindSeries || slices.ContainsFunc(seasonsOf(meta), func(season seasonSummary) bool { return season.Number == id.Season })
}

// folderEpisodes is what a season or series stands for when it is marked or
// counted: its aired episodes, each once. Numbered episodes have aired as
// Next Up decides it (airedFlags). Specials have no place in that order, so a
// special has aired unless it is dated in the future, as Jellyfin decides it;
// a series includes them, as Jellyfin's does.
func folderEpisodes(id itemID, meta *stremio.MetaObject) []itemID {
	videos := videosOf(meta)
	series := id.series()
	var out []itemID
	numbered := numberedEpisodes(videos)
	for i, aired := range airedFlags(numbered) {
		if v := numbered[i]; aired && (id.Kind == kindSeries || v.Season == id.Season) {
			out = append(out, series.episode(v.Season, v.Episode))
		}
	}
	if id.Kind == kindSeries || id.Season == 0 {
		// The first listing of a special is the one itemOf renders.
		seen := map[int]bool{}
		for _, v := range videos {
			if v.Season != 0 || seen[v.Episode] {
				continue
			}
			seen[v.Episode] = true
			if !unaired(v.Released) {
				out = append(out, series.episode(0, v.Episode))
			}
		}
	}
	return out
}

// countedUserData is a season's or series' UserData from its own, ud: its
// favourite flag and dates are its own, and Played and the counts are its
// episodes', counted as Jellyfin counts them (countedEpisodes, countFolder).
// Without the metadata (nil meta) its episodes are not known, so it is ud,
// uncounted. A failed read of the stored episodes is an error, never read as
// none played.
func (s *Server) countedUserData(rq *request, id itemID, meta *stremio.MetaObject, ud *userData) (*userData, error) {
	if meta == nil {
		return ud, nil
	}
	states, err := s.episodeStates(rq, id)
	if err != nil {
		return nil, err
	}
	countFolder(ud, countedEpisodes(id, meta, states), states)
	return ud, nil
}

// countedEpisodes is what a season's or series' counts are over: its aired
// episodes (folderEpisodes), and any other it lists that the stream has
// watched. One dated in the future, or undated in an announced season, has
// plainly been seen, as Jellyfin counts any episode it has a file for,
// whatever its date. Marking watched still takes the aired ones only.
func countedEpisodes(id itemID, meta *stremio.MetaObject, states map[string]persistence.JellyfinPlaystate) []itemID {
	episodes := folderEpisodes(id, meta)
	counted := map[itemID]bool{}
	for _, ep := range episodes {
		counted[ep] = true
	}
	for _, v := range videosOf(meta) {
		ep := id.series().episode(v.Season, v.Episode)
		if (id.Kind == kindSeries || v.Season == id.Season) && !counted[ep] && states[ep.encode()].Played {
			counted[ep] = true
			episodes = append(episodes, ep)
		}
	}
	return episodes
}

// countFolder sets a folder's Played and counts from the episodes it counts:
// UnplayedItemCount and PlayedPercentage over them, Played once every one is.
// With none it is left unplayed and uncounted, where Jellyfin would call an
// empty folder played.
func countFolder(ud *userData, episodes []itemID, states map[string]persistence.JellyfinPlaystate) {
	if len(episodes) == 0 {
		return
	}
	played := 0
	for _, ep := range episodes {
		if states[ep.encode()].Played {
			played++
		}
	}
	ud.UnplayedItemCount = intPtr(len(episodes) - played)
	ud.PlayedPercentage = floatPtr(100 * float64(played) / float64(len(episodes)))
	ud.Played = played == len(episodes)
}

// episodeStates is a series' stored episode rows by item id, read once per
// request (prefixStates), so a page of seasons, or of several series, reads
// each series once and never the rest of the stream. A season's are its
// series'; counting picks its own.
func (s *Server) episodeStates(rq *request, id itemID) (map[string]persistence.JellyfinPlaystate, error) {
	return s.prefixStates(rq, id.series().episodesPrefix())
}

// prefixStates is the stream's stored rows whose item id starts with
// prefix, by item id, read once per request with ListPrefix; a mark drops
// what it changes (markFolderPlayed, clearFolderPlayed).
func (s *Server) prefixStates(rq *request, prefix string) (map[string]persistence.JellyfinPlaystate, error) {
	states := map[string]persistence.JellyfinPlaystate{}
	if s.opts.Playstate == nil {
		return states, nil
	}
	if cached, ok := rq.prefixed[prefix]; ok {
		return cached, nil
	}
	rows, err := s.opts.Playstate.ListPrefix(rq.streamName(), prefix)
	if err != nil {
		logger.Warn("Jellyfin play state read failed", "stream", rq.streamName(), "prefix", prefix, "err", err)
		return nil, err
	}
	for _, st := range rows {
		states[st.ItemID] = st
	}
	if rq.prefixed == nil {
		rq.prefixed = map[string]map[string]persistence.JellyfinPlaystate{}
	}
	rq.prefixed[prefix] = states
	return states, nil
}

// renderedUserData is a season's or series' UserData where it is only shown,
// a listed row or a favourite's answer: from its own UserData, ud, counted
// when its metadata is in hand (countedUserData). When the stored episodes
// cannot be read it is shown uncounted, as an unreadable row is shown
// unplayed (userData), rather than fail what it is shown with.
func (s *Server) renderedUserData(rq *request, id itemID, meta *stremio.MetaObject, ud *userData) *userData {
	if counted, err := s.countedUserData(rq, id, meta, ud); err == nil {
		return counted
	}
	return ud
}

func isFolder(id itemID) bool { return id.Kind == kindSeason || id.Kind == kindSeries }

// storedRow reports whether the stream has a row of its own for id.
func (s *Server) storedRow(rq *request, id itemID) bool {
	if s.opts.Playstate == nil {
		return false
	}
	_, ok := s.opts.Playstate.Get(rq.streamName(), id.encode())
	return ok
}

// errPlayStateRead is a listing that could not read the play state it
// shows, which it fails with rather than show it wrong (writeItemError).
var errPlayStateRead = errors.New("jellyfin: play state read failed")

func writePlayedError(w http.ResponseWriter) {
	http.Error(w, "Played update failed", http.StatusInternalServerError)
}

func writePlayStateError(w http.ResponseWriter) {
	http.Error(w, "Play state read failed", http.StatusInternalServerError)
}

// setFavorite returns the store's error so the handler can refuse the
// request: a client that gets UserData back treats the favourite as saved.
func (s *Server) setFavorite(rq *request, id itemID, favorite bool) error {
	if s.opts.Playstate == nil {
		return nil
	}
	encoded := id.encode()
	err := s.opts.Playstate.SetFavorite(rq.streamName(), encoded, id.ContentType, id.playStremioID(), favorite)
	if err != nil {
		logger.Warn("Jellyfin favourite mark failed", "stream", rq.streamName(), "item", encoded, "err", err)
	}
	return err
}

func writeFavoriteError(w http.ResponseWriter) {
	http.Error(w, "Favourite update failed", http.StatusInternalServerError)
}

// favoriteID resolves an id a client may favourite: a movie, series, season
// or episode, with a media source standing for the item it plays. Libraries
// and genres are not favourited.
func favoriteID(raw string) (itemID, bool) {
	id, err := decodeItemID(raw)
	if err != nil {
		return itemID{}, false
	}
	id = id.playable()
	switch id.Kind {
	case kindMovie, kindSeries, kindSeason, kindEpisode:
		return id, true
	}
	return itemID{}, false
}
