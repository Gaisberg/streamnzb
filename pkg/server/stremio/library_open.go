package stremio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/services/metadata/kitsu"
	"streamnzb/pkg/services/metadata/tvdb"
	"streamnzb/pkg/session"
)

// LibraryFile is one library release opened as a seekable byte stream, the
// same stream a /play response would serve, charged to the stream that opened
// it. Callers that present the library as files (the NFS export) read it by
// offset; Close ends the playback the open began.
type LibraryFile struct {
	io.ReadSeekCloser
	// Name is the media file's own name inside the release, Size its exact
	// byte length.
	Name string
	Size int64

	closeOnce sync.Once
	end       func()
}

// Close closes the byte stream and ends the playback it was counted as.
// Safe to call more than once.
func (f *LibraryFile) Close() error {
	var err error
	f.closeOnce.Do(func() {
		if f.ReadSeekCloser != nil {
			err = f.ReadSeekCloser.Close()
		}
		if f.end != nil {
			f.end()
		}
	})
	return err
}

// ErrLibraryItemUnplayable reports a library row that cannot be opened at all:
// no stored NZB, or a status that says the release is gone.
var ErrLibraryItemUnplayable = errors.New("library item is not playable")

// libraryItemReportMeta is the content identity a library row was stored
// under, in the form the session layer selects files and reports with. The
// row's media file name pins the selection to that file: a row from a pack
// has no episode number to select by, and without the pin a replay would
// open the largest file in the NZB instead. For an archived release the
// media name is the inner file, which matches no NZB file, and selection
// falls back to the episode as before.
func libraryItemReportMeta(item *persistence.LibraryItem) *session.AvailReportMeta {
	return &session.AvailReportMeta{
		ImdbID:  item.ImdbID,
		TmdbID:  item.TmdbID,
		TvdbID:  item.TvdbID,
		KitsuID: item.KitsuID,
		Season:  item.Season,
		Episode: item.Episode,
		File:    strings.TrimSpace(item.MediaFileName),
	}
}

// libraryNZBSource is the indexer a library-backed session names as its NZB
// source. The session already carries the stored NZB bytes, so DownloadNZB is
// never reached in practice; it exists because a deferred session insists on
// having somewhere to download from, and the library row is exactly that.
type libraryNZBSource struct {
	item *persistence.LibraryItem
}

func (l *libraryNZBSource) Search(context.Context, indexer.SearchRequest) (*indexer.SearchResponse, error) {
	return &indexer.SearchResponse{}, nil
}

func (l *libraryNZBSource) DownloadNZB(context.Context, string) ([]byte, error) {
	if l.item == nil || len(l.item.NZBData) == 0 {
		return nil, ErrLibraryItemUnplayable
	}
	if data, err := persistence.DecompressNZB(l.item.NZBData); err == nil && len(data) > 0 {
		return data, nil
	}
	return l.item.NZBData, nil
}

func (l *libraryNZBSource) Ping(context.Context) error { return nil }
func (l *libraryNZBSource) Name() string {
	if l.item == nil {
		return "StreamNZB Library"
	}
	return libraryIndexerName(l.item.IndexerName)
}
func (l *libraryNZBSource) GetUsage() indexer.Usage { return indexer.Usage{} }

// librarySessionID is the session a stream's view of one library row plays
// under. Deterministic, so reopening the same file after the session was
// evicted lands on a fresh session with the same name, and two readers of one
// file share a session rather than mapping the archive twice. The row id is
// hashed: it embeds the release title, which is long and unconstrained.
func librarySessionID(stream *auth.Stream, item *persistence.LibraryItem) string {
	sum := sha256.Sum256([]byte(item.ID))
	return "library/" + streamID(stream) + "/" + hex.EncodeToString(sum[:12])
}

// OpenLibraryFile opens a library row as the byte stream playback would serve,
// through the stream's own provider view and connection caps, and counts it as
// a play of that stream until the returned file is closed. clientAddr names
// the reader the way a /play request's client IP does, for the dashboard.
//
// The returned stream is bound to ctx for its whole life — the playback
// probe and read-ahead run under it — so the caller cancels ctx only after
// Close, never when this returns.
//
// A row listed without its NZB (listings leave the blob out) is reloaded from
// the store first. The open runs the same startup as a play — NZB parse,
// blueprint rehydration, probe — so the first open of a cold row takes the
// seconds a cold play takes; a second open while the session lives is quick.
func (s *Server) OpenLibraryFile(ctx context.Context, stream *auth.Stream, item *persistence.LibraryItem, clientAddr string) (*LibraryFile, error) {
	if s == nil || s.sessionManager == nil || s.playback == nil {
		return nil, fmt.Errorf("playback is not available")
	}
	if item == nil {
		return nil, ErrLibraryItemUnplayable
	}
	if item.Status == persistence.LibraryStatusBad {
		return nil, fmt.Errorf("%w: %s", ErrLibraryItemUnplayable, firstNonEmptyString(item.StatusReason, "marked bad"))
	}
	if len(item.NZBData) == 0 {
		if s.attemptRecorder == nil || s.attemptRecorder.LibraryStore() == nil {
			return nil, ErrLibraryItemUnplayable
		}
		full, err := s.attemptRecorder.LibraryStore().GetItem(item.ID)
		if err != nil {
			return nil, fmt.Errorf("load library item: %w", err)
		}
		if full == nil || len(full.NZBData) == 0 {
			return nil, fmt.Errorf("%w: no stored NZB", ErrLibraryItemUnplayable)
		}
		item = full
	}

	rel := convertLibraryItemToRelease(item)
	sessionID := librarySessionID(stream, item)
	downloadURL := strings.TrimSpace(item.DetailsURL)
	if downloadURL == "" {
		downloadURL = "library:" + item.ID
	}
	sess, _, err := s.sessionManager.CreateDeferredSessionWithFetcherOutcome(
		sessionID, downloadURL, rel, &libraryNZBSource{item: item}, libraryItemReportMeta(item),
		item.ContentType, item.ContentID, item.ReleaseTitle, streamID(stream),
		s.segmentFetcherForStream(stream), s.providerHostsForStream(stream))
	if err != nil {
		return nil, fmt.Errorf("create library session: %w", err)
	}

	hold := s.sessionManager.BeginSessionStartup(sess)
	prepared, err := s.playback.Prepare(ctx, sess)
	hold.Release()
	if err != nil {
		logger.Debug("Library file open failed", "session", sessionID, "title", item.ReleaseTitle, "err", err)
		return nil, err
	}
	s.sessionManager.StartPlayback(sess, clientAddr)
	if s.attemptRecorder != nil && s.attemptRecorder.LibraryStore() != nil {
		libStore := s.attemptRecorder.LibraryStore()
		libStore.TouchItem(item.ID)
		// A row stored before the media size was recorded reports the
		// release size to clients, which is larger than the file; the open
		// just measured the real one, so keep it.
		if prepared.Spec.Size > 0 && (item.MediaFileSize != prepared.Spec.Size || item.MediaFileName == "") {
			if err := libStore.SetMediaFile(item.ID, prepared.Spec.Name, prepared.Spec.Size); err != nil {
				logger.Debug("Could not record media size on library row", "id", item.ID, "err", err)
			} else {
				logger.Debug("Recorded media size on library row", "id", item.ID, "size", prepared.Spec.Size, "was", item.MediaFileSize)
			}
		}
	}
	return &LibraryFile{
		ReadSeekCloser: prepared.Stream,
		Name:           prepared.Spec.Name,
		Size:           prepared.Spec.Size,
		end:            func() { s.sessionManager.EndPlayback(sess, clientAddr) },
	}, nil
}

// LibraryTitle names the title a library row belongs to — the show or film,
// not the release — with its first-release year when known. It reads the
// cached metadata clients directly rather than a stream's metadata profile,
// so it answers for a stream with no profile bound, and it never searches an
// indexer. ok is false when no provider could name it; callers fall back to
// the release name. The NFS export names its library folder with it.
func (s *Server) LibraryTitle(ctx context.Context, item *persistence.LibraryItem) (name string, year int, ok bool) {
	if s == nil || item == nil {
		return "", 0, false
	}
	rt := s.runtime()
	movieLike := item.Season == 0 && item.Episode == 0

	if item.KitsuID != "" && s.kitsuClient != nil {
		if meta, err := s.kitsuClient.GetAnimeMeta(ctx, item.KitsuID); err == nil && meta != nil && meta.CanonicalTitle != "" {
			return kitsu.DisplayTitle("", meta.EnglishTitle, meta.CanonicalTitle), yearOfDate(meta.StartDate), true
		}
	}
	if item.TvdbID != "" && rt.tvdbClient != nil && !movieLike {
		if ext, err := rt.tvdbClient.GetSeriesExtendedTranslated(item.TvdbID, tvdb.LanguageToISO3("")); err == nil && ext != nil && ext.Name != "" {
			y := yearOfDate(ext.FirstAired)
			if y == 0 {
				y, _ = strconv.Atoi(strings.TrimSpace(ext.Year))
			}
			return ext.Name, y, true
		}
	}
	if rt.tmdbClient == nil {
		return "", 0, false
	}
	tmdbID := 0
	if item.TmdbID != "" {
		tmdbID, _ = strconv.Atoi(item.TmdbID)
	}
	imdbID := item.ImdbID
	if imdbID == "" && strings.HasPrefix(item.ContentID, "tt") {
		// Rows stored before the per-scheme id columns carry the id only here.
		imdbID = item.ContentID
	}
	if tmdbID <= 0 && strings.HasPrefix(imdbID, "tt") {
		contentType := "series"
		if movieLike {
			contentType = "movie"
		}
		if find, err := rt.tmdbClient.Find(imdbID, "imdb_id"); err == nil {
			if res, found := pickFindResult(find, contentType); found {
				tmdbID = res.ID
			}
		}
	}
	if tmdbID <= 0 {
		return "", 0, false
	}
	if movieLike {
		if details, err := rt.tmdbClient.GetMovieDetailsWithLanguage(tmdbID, ""); err == nil && details != nil && details.Title != "" {
			return details.Title, yearOfDate(details.ReleaseDate), true
		}
		return "", 0, false
	}
	if details, err := rt.tmdbClient.GetTVDetailsWithLanguage(tmdbID, ""); err == nil && details != nil && details.Name != "" {
		return details.Name, yearOfDate(details.FirstAirDate), true
	}
	return "", 0, false
}

// yearOfDate reads the year off a YYYY-MM-DD (or bare YYYY) date; 0 when the
// string carries none.
func yearOfDate(date string) int {
	date = strings.TrimSpace(date)
	if len(date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil || y < 1800 || y > 2200 {
		return 0
	}
	return y
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
