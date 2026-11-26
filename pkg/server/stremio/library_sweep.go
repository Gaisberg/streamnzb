package stremio

import (
	"context"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/session"
)

const (
	librarySweepStartupDelay = 10 * time.Minute
	librarySweepInterval     = 1 * time.Hour
	librarySweepBatch        = 50
	librarySweepVerifyTimout = 20 * time.Second
)

// startLibraryFreshnessSweeper launches a background loop that re-STATs cached
// releases older than the configured TTL and prunes the ones whose articles have
// expired, so the library stops offering dead releases. Disabled when the TTL is
// non-positive. Runs a bounded batch per tick so a large backlog drains gradually.
func (s *Server) startLibraryFreshnessSweeper() {
	rt := s.runtime()
	if s == nil || rt.config == nil {
		return
	}
	ttl := rt.config.EffectiveLibraryVerifyTTL()
	if ttl <= 0 {
		logger.Debug("Library freshness sweep disabled")
		return
	}
	go func() {
		timer := time.NewTimer(librarySweepStartupDelay)
		defer timer.Stop()
		for {
			// Stops with the server. Without this the sweep keeps querying the
			// library after shutdown has begun, and can still be mid-query when
			// the database is closed underneath it.
			select {
			case <-s.stopCh:
				return
			case <-timer.C:
			}
			s.sweepStaleLibraryItems(ttl)
			timer.Reset(librarySweepInterval)
		}
	}()
}

func (s *Server) sweepStaleLibraryItems(ttl time.Duration) {
	if s.attemptRecorder == nil || s.sessionManager == nil {
		return
	}
	libStore := s.attemptRecorder.LibraryStore()
	if libStore == nil {
		return
	}

	before := time.Now().Add(-ttl)
	items, err := libStore.StaleItems(before, librarySweepBatch)
	if err != nil {
		logger.Debug("Library freshness sweep query failed", "err", err)
		return
	}
	if len(items) == 0 {
		return
	}
	logger.Debug("Library freshness sweep verifying stale items", "count", len(items))

	verified, expired := 0, 0
	for _, item := range items {
		if len(item.NZBData) == 0 {
			// Nothing to STAT against; bump the timestamp so it isn't rechecked forever.
			applySweepVerdict(libStore.MarkVerifiedIfSnapshot(item, time.Now()))
			continue
		}
		meta := &session.AvailReportMeta{
			ImdbID:  item.ImdbID,
			TmdbID:  item.TmdbID,
			TvdbID:  item.TvdbID,
			KitsuID: item.KitsuID,
			Season:  item.Season,
			Episode: item.Episode,
		}
		ctx, cancel := context.WithTimeout(context.Background(), librarySweepVerifyTimout)
		verify := s.sessionManager.VerifyLibraryNZB
		if s.libraryVerifier != nil {
			verify = s.libraryVerifier
		}
		exists, verr := verify(ctx, item.NZBData, meta)
		cancel()
		if verr != nil {
			// Transient (timeout / no providers) — leave it for the next sweep.
			logger.Trace("Library freshness sweep verify inconclusive", "id", item.ID, "err", verr)
			continue
		}
		if exists {
			if applySweepVerdict(libStore.MarkVerifiedIfSnapshot(item, time.Now())) {
				verified++
			}
			continue
		}
		// Keep the row but mark it bad: visible/filterable in the Library UI and
		// excluded from playback candidates.
		if !applySweepVerdict(libStore.MarkBadIfSnapshot(item, "articles no longer available (freshness sweep)")) {
			continue
		}
		expired++
		logger.Info("Library freshness sweep marked release bad (articles expired)", "id", item.ID, "title", item.ReleaseTitle)
	}
	if verified > 0 || expired > 0 {
		logger.Info("Library freshness sweep completed", "checked", len(items), "verified", verified, "marked_bad", expired)
	}
}

// applySweepVerdict reports whether a conditional writeback landed. A stale
// result (row replaced or no longer eligible) is dropped quietly; the row is
// re-examined on a later sweep if it is still stale.
func applySweepVerdict(applied bool, err error) bool {
	if err != nil {
		logger.Warn("Library freshness sweep writeback failed", "err", err)
	}
	return applied
}
