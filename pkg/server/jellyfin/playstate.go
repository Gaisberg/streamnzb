package jellyfin

import (
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
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

func (s *Server) setPlayed(rq *request, id itemID, played bool) {
	if s.opts.Playstate == nil {
		return
	}
	encoded := id.encode()
	s.progress.forget(rq.streamName() + "|" + encoded)
	if err := s.opts.Playstate.SetPlayed(rq.streamName(), encoded, id.ContentType, id.playStremioID(), played); err != nil {
		logger.Warn("Jellyfin played mark failed", "stream", rq.streamName(), "item", encoded, "err", err)
	}
}
