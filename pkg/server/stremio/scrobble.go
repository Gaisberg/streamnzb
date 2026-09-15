package stremio

import (
	"context"
	"strconv"
	"strings"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/search/query"
	"streamnzb/pkg/services/metadata/scrobble"
	"streamnzb/pkg/session"
)

const scrobbleRequestTimeout = 15 * time.Second

// scrobbleMinAdvancePercent is how far the watched high-water mark must move
// past the last reported progress before another stop is worth sending. It
// also filters serves too short to mean anything (a play abandoned within the
// first percent).
const scrobbleMinAdvancePercent = 1

// scrobbleTargets is the set of watch-tracking services this stream's playback
// is reported to right now: those the stream has switched on and whose account
// the stream has linked. Both halves are per stream — the account belongs to
// the person the stream belongs to — so one household member's viewing can
// never land in another's history. A stream that has linked nothing, which is
// the default, reports nothing.
func scrobbleTargets(rt serverRuntime, stream *auth.Stream) []scrobble.Target {
	if stream == nil {
		return nil
	}
	var targets []scrobble.Target
	if stream.SimklScrobble {
		if client := rt.simklClients.For(stream.Username); client.Connected() {
			targets = append(targets, client)
		}
	}
	if stream.MDBListScrobble {
		if client := rt.mdblistClients.For(stream.Username); client.Connected() {
			targets = append(targets, client)
		}
	}
	return targets
}

// scrobbleItemForSession maps the session's request context onto the
// addressing the targets need, or reports false when the content can be placed
// nowhere (direct NZB plays with no ids, series requests without an episode,
// anime neither the MAL mapping nor anime-lists' aired ids can name).
func (s *Server) scrobbleItemForSession(sess *session.Session) (scrobble.Item, bool) {
	meta := sess.ContentIDs
	if meta == nil {
		return scrobble.Item{}, false
	}
	item := scrobble.Item{Title: strings.TrimSpace(sess.ContentTitle)}
	switch {
	case meta.KitsuID != "" || sess.ContentType == "anime":
		item.ContentType = "anime"
		// Both addressings go in, because the targets disagree on how anime is
		// identified: Simkl wants the MAL entry and its entry-local episode,
		// MDBList wants the aired series anime-lists resolved it onto.
		item.IMDbID, item.TMDBID, item.TVDBID = meta.ImdbID, meta.TmdbID, meta.TvdbID
		item.Season, item.Episode = meta.Season, meta.Episode
		if mapping, ok := s.animeLists.LookupKitsu(meta.KitsuID); ok {
			item.AnimeMovie = strings.EqualFold(mapping.Type, "MOVIE")
			if mapping.MALID > 0 {
				item.MALID = strconv.Itoa(mapping.MALID)
				// The entry-local episode number, which MAL shares with Kitsu
				// — not meta.Episode, which anime-lists may have remapped to
				// the aired series numbering.
				parsed := query.ParseContentID(sess.ContentID)
				item.MALEpisode, _ = strconv.Atoi(parsed.KitsuEpisode)
			}
		}
	case sess.ContentType == "movie":
		item.ContentType = "movie"
		item.IMDbID, item.TMDBID = meta.ImdbID, meta.TmdbID
	default:
		item.ContentType = "series"
		item.IMDbID, item.TMDBID, item.TVDBID = meta.ImdbID, meta.TmdbID, meta.TvdbID
		item.Season, item.Episode = meta.Season, meta.Episode
	}
	return item, item.Addressable()
}

// scrobbleStart reports "watching now" once real playback is proven (the
// good-attempt commit). Fire-and-forget: scrobbling must never slow a serve.
func (s *Server) scrobbleStart(stream *auth.Stream, sess *session.Session) {
	if sess == nil {
		return
	}
	targets := scrobbleTargets(s.runtime(), stream)
	if len(targets) == 0 {
		return
	}
	item, ok := s.scrobbleItemForSession(sess)
	if !ok {
		return
	}
	s.sendScrobble(targets, sess, scrobble.VerbStart, item, sess.ServedProgressPercent())
}

// scrobbleStopIfIdle runs as a serve unwinds, after playback bookkeeping: when
// the last open play on the session has ended, the current watched progress
// goes out as a scrobble stop. Both services mark an item watched at ≥80% and
// store a resumable playback below that, so a pause/stop mid-film shows up at
// the right position and a finished film lands in history.
func (s *Server) scrobbleStopIfIdle(stream *auth.Stream, sess *session.Session) {
	if sess == nil || sess.CurrentActivePlays() != 0 {
		return
	}
	targets := scrobbleTargets(s.runtime(), stream)
	if len(targets) == 0 {
		return
	}
	progress := sess.ServedProgressPercent()
	// A stop is only news when the mark moved: seeks reopen the request every
	// few minutes, and each brief zero-plays gap between requests lands here.
	if progress <= sess.LastReportedProgress()+scrobbleMinAdvancePercent {
		return
	}
	item, ok := s.scrobbleItemForSession(sess)
	if !ok {
		return
	}
	sess.SetLastReportedProgress(progress)
	s.sendScrobble(targets, sess, scrobble.VerbStop, item, progress)
}

func (s *Server) sendScrobble(targets []scrobble.Target, sess *session.Session, verb string, item scrobble.Item, progress float64) {
	contentID := sess.ContentID
	for _, target := range targets {
		// A target that cannot address this item is skipped silently: anime
		// only anime-lists' aired mapping names has no Simkl address, and
		// anime only MAL names has no MDBList one. Neither is a failure.
		if !target.Supports(item) {
			continue
		}
		go func(target scrobble.Target) {
			ctx, cancel := context.WithTimeout(context.Background(), scrobbleRequestTimeout)
			defer cancel()
			if err := target.Scrobble(ctx, verb, item, progress); err != nil {
				logger.Debug("Scrobble failed", "target", target.Name(), "verb", verb, "content", contentID, "err", err)
				return
			}
			logger.Debug("Scrobble sent", "target", target.Name(), "verb", verb, "content", contentID, "progress", int(progress))
		}(target)
	}
}
