package stremio

import (
	"context"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
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
	if s == nil || s.config == nil {
		return
	}
	ttl := s.config.EffectiveLibraryVerifyTTL()
	if ttl <= 0 {
		logger.Debug("Library freshness sweep disabled")
		return
	}
	go func() {
		timer := time.NewTimer(librarySweepStartupDelay)
		defer timer.Stop()
		for {
			<-timer.C
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
			libStore.MarkVerified(item.ID, time.Now())
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
		exists, verr := s.sessionManager.VerifyLibraryNZB(ctx, item.NZBData, meta)
		cancel()
		if verr != nil {
			// Transient (timeout / no providers) — leave it for the next sweep.
			logger.Trace("Library freshness sweep verify inconclusive", "id", item.ID, "err", verr)
			continue
		}
		if exists {
			libStore.MarkVerified(item.ID, time.Now())
			verified++
			continue
		}
		// Keep the row but mark it bad: visible/filterable in the Library UI and
		// excluded from playback candidates.
		libStore.MarkStatus(item.ID, persistence.LibraryStatusBad, "articles no longer available (freshness sweep)")
		expired++
		logger.Info("Library freshness sweep marked release bad (articles expired)", "id", item.ID, "title", item.ReleaseTitle)
	}
	if verified > 0 || expired > 0 {
		logger.Info("Library freshness sweep completed", "checked", len(items), "verified", verified, "marked_bad", expired)
	}
}
