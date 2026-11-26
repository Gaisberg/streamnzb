package stremio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/media/par2"
	"streamnzb/pkg/playback"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/parser"
	"streamnzb/pkg/session"
	"streamnzb/pkg/usenet/pool"
)

// IngestRequest is a release handed to StreamNZB to be stored in the library
// without anyone playing it — what the download-client API receives from
// Radarr or Sonarr.
type IngestRequest struct {
	// Name is the release name, as the client calls the job.
	Name string
	// NZBData is the NZB document.
	NZBData []byte
	// ContentType is "movie", "series", or "" to read it off the name.
	ContentType string
	// Source labels where the release came from, shown as the library row's
	// indexer ("Sonarr", "Radarr", or the client's category).
	Source string
	// ClientAddr names the client for the dashboard.
	ClientAddr string
	// Progress, when set, is told how many of the release's file sets have
	// been validated so far, and how many there are, each time one finishes.
	// Called from several goroutines, never after IngestLibraryRelease returns.
	Progress func(done, total int)
}

// ingestSetConnections is the connections one file set's validation is
// assumed to keep busy: a STAT sweep plus the probe's reads. The stream's
// connection budget divided by it is how many sets validate at once.
const ingestSetConnections = 4

// maxIngestParallel bounds how many sets validate at once however wide the
// stream's budget is; a probe is also a process.
const maxIngestParallel = 6

// ErrIngestRejected reports a release the validation refused: articles
// missing, no playable media, or a file ffprobe could not decode. The
// library keeps nothing of it.
var ErrIngestRejected = errors.New("release rejected")

const ingestSessionPrefix = "ingest/"

// isIngestSession reports a session created by IngestLibraryRelease. The
// library save path reads it to store the result regardless of the
// auto-save setting, which is about plays.
func isIngestSession(sess *session.Session) bool {
	return sess != nil && strings.HasPrefix(sess.ID, ingestSessionPrefix)
}

// IngestLibraryRelease validates a release the way preloading does — NZB
// parse, archive mapping, an article check of the selected file and a strict
// decode probe — and stores it in the library as good rows, charged to the
// stream's providers. It returns the stored rows, which carry each media
// file's name and exact size and are what the NFS export publishes.
//
// One row per content set in the NZB — each lone media file, each
// multi-volume archive — addressed by file, not by what its name parses to.
// A movie is one set; a season pack is one per episode, double episodes and
// oddly named files included, because nothing here needs to understand the
// names: the client that organises the files does. Each set is validated on
// its own, so a pack with one bad file still yields the rest; only when no
// set at all could be stored does the call fail, with ErrIngestRejected.
// Season and episode numbers are still read off each file's name where they
// parse, as metadata for the row so Stremio can find it.
//
// The release is identified by its name: the parsed title and year are
// looked up on TMDB (falling back to the name alone), so the rows land under
// the ids the catalogs use.
func (s *Server) IngestLibraryRelease(ctx context.Context, stream *auth.Stream, req IngestRequest) ([]*persistence.LibraryItem, error) {
	if s == nil || s.sessionManager == nil || s.playback == nil {
		return nil, fmt.Errorf("playback is not available")
	}
	if s.attemptRecorder == nil || s.attemptRecorder.LibraryStore() == nil {
		return nil, fmt.Errorf("library is not available")
	}
	name := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(req.Name), ".nzb"))
	if name == "" {
		return nil, fmt.Errorf("release has no name")
	}
	if len(req.NZBData) == 0 {
		return nil, fmt.Errorf("empty NZB")
	}
	if len(req.NZBData) > MaxDirectPlayNZBSize {
		return nil, fmt.Errorf("NZB exceeds the %d MiB limit", MaxDirectPlayNZBSize>>20)
	}
	parsedNZB, err := nzb.ParseWithContext(ctx, bytes.NewReader(req.NZBData))
	if err != nil {
		return nil, fmt.Errorf("%w: parse NZB: %v", ErrIngestRejected, err)
	}

	parsed := parser.ParseReleaseTitle(name)
	contentType, season, episode := ingestContentType(req.ContentType, parsed)
	title, year := name, 0
	if parsed != nil && parsed.Result != nil && strings.TrimSpace(parsed.Title) != "" {
		title, year = strings.TrimSpace(parsed.Title), parsed.Year
	}
	imdbID, tmdbID, tvdbID := s.resolveReleaseIdentity(title, year, contentType == "movie")
	contentID := libraryContentID(imdbID, tmdbID, tvdbID, "")
	if contentID == "" {
		// No provider could place it; keyed by title so the row still has
		// an identity the export and the library page can group on.
		contentID = "title:" + release.NormalizeTitleForDedup(title)
	}

	sets := parsedNZB.ContentSets()
	if len(sets) == 0 {
		return nil, fmt.Errorf("%w: no content files in NZB", ErrIngestRejected)
	}

	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "Download client"
	}
	rel := &release.Release{Title: name, Size: parsedNZB.TotalSize(), Indexer: source}
	sum := sha256.Sum256(req.NZBData)
	baseID := ingestSessionPrefix + streamID(stream) + "/" + hex.EncodeToString(sum[:12])

	parallel := ingestParallelism(s.segmentFetcherForStream(stream), len(sets))
	logger.Info("Library ingest starting", "stream", streamID(stream), "title", name, "type", contentType, "content_id", contentID, "source", source, "sets", len(sets), "parallel", parallel)
	if req.Progress != nil {
		req.Progress(0, len(sets))
	}

	// A pack's sets validate side by side, bounded by the stream's
	// connection budget: each is its own deferred session drawing on the one
	// lease, so the cap holds however many run. Results keep NZB order.
	results := make([]*persistence.LibraryItem, len(sets))
	errs := make([]error, len(sets))
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		done int
		sem  = make(chan struct{}, parallel)
	)
	for i, set := range sets {
		if ctx.Err() != nil {
			break
		}
		lead := set[0]
		setSeason, setEpisode := episodeMetaForSet(contentType, season, episode, lead, len(sets) > 1)
		meta := &session.AvailReportMeta{ImdbID: imdbID, TmdbID: tmdbID, TvdbID: tvdbID, Season: setSeason, Episode: setEpisode, File: lead.Filename}
		if contentType == "series" && setSeason == 0 && setEpisode == 0 {
			meta.Seasonless = true
		}
		// One release name per row: a pack's rows share the job name but
		// each names its file, so their library ids stay distinct and the
		// Library page says which file a row is.
		rowTitle := name
		sessionID := baseID
		if len(sets) > 1 {
			rowTitle = fmt.Sprintf("%s [%s]", name, lead.Filename)
			sessionID += fmt.Sprintf("/%d", i)
		}
		setRel := *rel
		setRel.Title = rowTitle
		sem <- struct{}{}
		wg.Add(1)
		go func(i int, setRel release.Release, meta *session.AvailReportMeta, sessionID, rowTitle, file string) {
			defer wg.Done()
			defer func() { <-sem }()
			item, err := s.ingestOne(ctx, stream, sessionID, parsedNZB, req.NZBData, &setRel, meta, contentType, contentID, title, rowTitle)
			if err != nil && ctx.Err() == nil {
				logger.Warn("Library ingest rejected a file set", "title", name, "file", file, "err", err)
			}
			results[i], errs[i] = item, err
			mu.Lock()
			done++
			n := done
			mu.Unlock()
			if req.Progress != nil && err == nil {
				req.Progress(n, len(sets))
			}
		}(i, setRel, meta, sessionID, rowTitle, lead.Filename)
	}
	wg.Wait()

	var stored []*persistence.LibraryItem
	var firstErr error
	for i := range sets {
		if results[i] != nil {
			stored = append(stored, results[i])
		} else if errs[i] != nil && firstErr == nil {
			firstErr = errs[i]
		}
	}
	if ctx.Err() != nil {
		if firstErr == nil {
			firstErr = ctx.Err()
		}
		return stored, firstErr
	}
	if len(stored) == 0 {
		if firstErr == nil {
			firstErr = errors.New("nothing to store")
		}
		if errors.Is(firstErr, ErrIngestRejected) {
			return nil, firstErr
		}
		return nil, fmt.Errorf("%w: %v", ErrIngestRejected, firstErr)
	}
	if firstErr != nil {
		logger.Warn("Library ingest stored a partial release", "title", name, "stored", len(stored), "sets", len(sets))
	}
	stored = s.restoreFileNames(ctx, stream, parsedNZB, stored, contentType, season)
	return stored, nil
}

// ingestParallelism is how many of a release's sets validate at once: the
// stream's fetch budget shared out at ingestSetConnections per set, at least
// one, at most maxIngestParallel, and never more than there are sets.
func ingestParallelism(fetcher loader.SegmentFetcher, sets int) int {
	n := 1
	if p, ok := fetcher.(*pool.Pool); ok && p != nil {
		n = p.FetchConcurrency() / ingestSetConnections
	}
	if n > maxIngestParallel {
		n = maxIngestParallel
	}
	if n > sets {
		n = sets
	}
	if n < 1 {
		n = 1
	}
	return n
}

// episodeMetaForSet is the season and episode recorded on a set's row. They
// are metadata — nothing is selected by them — but Stremio finds library hits
// by them, so a wrong number would offer the wrong file for an episode:
//
//   - a single-set job takes what the release name says, else the file name;
//   - in a pack, each file's own name decides, when it is a real name with
//     exactly one season and episode; the pack's episode (if its name even
//     has one — "REPACK2" parses as one) is never applied to a file;
//   - an obfuscated name says nothing: the pack's season and no episode.
func episodeMetaForSet(contentType string, packSeason, packEpisode int, lead *nzb.FileInfo, multi bool) (season, episode int) {
	if contentType != "series" {
		return 0, 0
	}
	fromFile := func() (int, int, bool) {
		if lead == nil || lead.ParsedInfo == nil || lead.IsObfuscated || looksObfuscated(lead.Filename) {
			return 0, 0, false
		}
		if eps := lead.ParsedInfo.Episodes; len(eps) == 1 {
			s := packSeason
			if seasons := lead.ParsedInfo.Seasons; len(seasons) == 1 {
				s = seasons[0]
			}
			return s, eps[0], true
		}
		return 0, 0, false
	}
	if !multi {
		if packEpisode > 0 {
			return packSeason, packEpisode
		}
		if s, e, ok := fromFile(); ok {
			return s, e
		}
		return packSeason, 0
	}
	if s, e, ok := fromFile(); ok {
		return s, e
	}
	return packSeason, 0
}

// looksObfuscated reports a file name that is a hash or a random token
// rather than a release name: one run of letters and digits, no separators,
// with digits among the letters.
func looksObfuscated(name string) bool {
	// The first dot-separated component: "hash.part001.rar" is a hash.
	stem, _, _ := strings.Cut(path.Base(name), ".")
	if len(stem) < 12 || strings.ContainsAny(stem, " -_[]()") {
		return false
	}
	digits, letters := 0, 0
	for _, r := range stem {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			letters++
		default:
			return false
		}
	}
	return digits >= 3 && letters >= 3
}

// maxPar2Index bounds the index read: a few hundred bytes per file.
const maxPar2Index = 8 << 20

// restoreFileNames gives obfuscated files their real names so an *arr can
// tell which file is which. First the way SABnzbd does: the release's PAR2
// index, fetched through the stream's providers like any other article set,
// its file descriptions matched to rows by exact size. Where the index names
// are hashes as well — a post obfuscated before it was par'd — the file's own
// container title tag is the fallback, when it names an episode. A matched
// row is re-stored under the real name with the season and episode that name
// carries. Anything that cannot be matched is left as it was.
func (s *Server) restoreFileNames(ctx context.Context, stream *auth.Stream, parsedNZB *nzb.NZB, stored []*persistence.LibraryItem, contentType string, packSeason int) []*persistence.LibraryItem {
	needs := false
	for _, item := range stored {
		if item != nil && looksObfuscated(item.MediaFileName) {
			needs = true
			break
		}
	}
	if !needs || parsedNZB == nil {
		return stored
	}
	byLength := s.par2Names(ctx, stream, parsedNZB)
	if len(byLength) > 0 {
		obfuscated, total := 0, 0
		for _, names := range byLength {
			for _, n := range names {
				total++
				if looksObfuscated(n) {
					obfuscated++
				}
			}
		}
		logger.Debug("Library ingest read the PAR2 index", "files", total, "obfuscated_names", obfuscated)
	}
	libStore := s.attemptRecorder.LibraryStore()
	renamed := 0
	for i, item := range stored {
		if item == nil || !looksObfuscated(item.MediaFileName) || item.MediaFileSize <= 0 {
			continue
		}
		full, err := libStore.GetItem(item.ID)
		if err != nil || full == nil {
			continue
		}
		realName, source := "", ""
		if names := byLength[uint64(item.MediaFileSize)]; len(names) == 1 && !looksObfuscated(names[0]) {
			realName, source = path.Base(strings.ReplaceAll(names[0], "\\", "/")), "par2"
		} else if title := containerTitle(full.MediaCapsJSON); title != "" {
			realName, source = title+mediaFileExt(item.MediaFileName), "container title"
		}
		if realName == "" {
			continue
		}
		season, episode := full.Season, full.Episode
		if contentType == "series" {
			if parsed := parser.ParseReleaseTitle(realName); parsed != nil && len(parsed.Episodes) == 1 {
				episode = parsed.Episodes[0]
				if len(parsed.Seasons) == 1 {
					season = parsed.Seasons[0]
				} else {
					season = packSeason
				}
			}
		}
		oldID := full.ID
		full.MediaFileName = realName
		full.ReleaseTitle = strings.Replace(full.ReleaseTitle, "["+item.MediaFileName+"]", "["+realName+"]", 1)
		full.Season, full.Episode = season, episode
		full.ID = ""
		if err := libStore.StoreItem(full); err != nil {
			logger.Debug("Library ingest could not re-store a renamed row", "id", oldID, "err", err)
			continue
		}
		if full.ID != oldID {
			_ = libStore.DeleteItem(oldID)
		}
		logger.Info("Library ingest restored file name", "source", source, "was", item.MediaFileName, "now", realName, "season", season, "episode", episode)
		stored[i] = full
		renamed++
	}
	if renamed == 0 {
		logger.Info("Library ingest could not restore obfuscated file names; the client will need a manual import", "files", len(stored))
	}
	return stored
}

// containerTitle is the probed container title when it names an episode or a
// film well enough to be a file name: a real name, not a hash, that parses to
// a title. Returned sanitised for a file name, without extension.
func containerTitle(mediaCapsJSON string) string {
	if mediaCapsJSON == "" {
		return ""
	}
	var caps struct {
		Title string `json:"Title"`
	}
	if err := json.Unmarshal([]byte(mediaCapsJSON), &caps); err != nil {
		return ""
	}
	title := strings.TrimSpace(caps.Title)
	if title == "" || looksObfuscated(title) {
		return ""
	}
	parsed := parser.ParseReleaseTitle(title)
	if parsed == nil || strings.TrimSpace(parsed.Title) == "" {
		return ""
	}
	// Keep the tag as the file name, cleaned: a title such as
	// "House.of.the.Dragon.S03E01.1080p.WEB" is exactly what an *arr wants.
	var b strings.Builder
	for _, r := range title {
		switch {
		case r < 0x20 || strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteRune('.')
		default:
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), ". ")
}

func mediaFileExt(name string) string {
	if ext := strings.ToLower(path.Ext(name)); ext != "" && len(ext) <= 8 {
		return ext
	}
	return ".mkv"
}

// par2Names fetches the release's PAR2 index and returns its file names by
// length. Nil when the release has no index or it could not be read.
func (s *Server) par2Names(ctx context.Context, stream *auth.Stream, parsedNZB *nzb.NZB) map[uint64][]string {
	var index *nzb.FileInfo
	for _, info := range parsedNZB.GetFileInfo() {
		if info == nil || info.Extension != ".par2" {
			continue
		}
		lower := strings.ToLower(info.Filename)
		if strings.Contains(lower, ".vol") {
			continue
		}
		if index == nil || info.Size < index.Size {
			index = info
		}
	}
	if index == nil || index.Size <= 0 || index.Size > maxPar2Index {
		return nil
	}
	fetcher := s.segmentFetcherForStream(stream)
	if fetcher == nil {
		return nil
	}
	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Bound to readCtx: cancelling it on return is what releases the file.
	f := loader.NewFile(readCtx, index.File, loader.NewSegmentSizeEstimator(), fetcher)
	data, err := io.ReadAll(io.NewSectionReader(f, 0, f.Size()))
	if err != nil && len(data) == 0 {
		logger.Debug("Library ingest could not read the PAR2 index", "file", index.Filename, "err", err)
		return nil
	}
	descs, err := par2.ParseIndex(data)
	if err != nil {
		logger.Debug("Library ingest could not parse the PAR2 index", "file", index.Filename, "err", err)
		return nil
	}
	return par2.NamesByLength(descs)
}

// bytesNZBSource is the indexer an ingest session names as its NZB source.
// The bytes are already in hand, so DownloadNZB simply returns them; it
// exists because a deferred session insists on somewhere to download from.
type bytesNZBSource struct {
	data []byte
	name string
}

func (b *bytesNZBSource) Search(context.Context, indexer.SearchRequest) (*indexer.SearchResponse, error) {
	return &indexer.SearchResponse{}, nil
}
func (b *bytesNZBSource) DownloadNZB(context.Context, string) ([]byte, error) { return b.data, nil }
func (b *bytesNZBSource) Ping(context.Context) error                          { return nil }
func (b *bytesNZBSource) Name() string                                        { return b.name }
func (b *bytesNZBSource) GetUsage() indexer.Usage                             { return indexer.Usage{} }

// ingestOne validates and stores one file set (or the whole of a movie) and
// returns its row.
func (s *Server) ingestOne(ctx context.Context, stream *auth.Stream, sessionID string, parsedNZB *nzb.NZB, nzbData []byte, rel *release.Release, meta *session.AvailReportMeta, contentType, contentID, title, name string) (*persistence.LibraryItem, error) {
	// A deferred session, not one built from the parsed NZB: a session that
	// already holds its NZB counts as warm and preloading returns at once,
	// validating nothing. The bytes are handed over for the lazy load to
	// find, as a library replay does, so preloading runs in full.
	sess, _, err := s.sessionManager.CreateDeferredSessionWithFetcherOutcome(sessionID, "ingest:"+sessionID, rel, &bytesNZBSource{data: nzbData, name: rel.Indexer}, meta,
		contentType, contentID, title, streamID(stream), s.segmentFetcherForStream(stream), s.providerHostsForStream(stream))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIngestRejected, err)
	}
	sess.NZBBytes = nzbData
	_ = parsedNZB
	// The session exists only to be validated; whatever happens it goes
	// when this returns, loader files and all.
	defer s.sessionManager.DeleteSession(sessionID)

	hold := s.sessionManager.BeginSessionStartup(sess)
	// Download-client validation: every article of the file is asked for,
	// and the probe stops at the header. "Completed" has to mean whole.
	ok, err := s.playback.Preload(playback.WithIngestValidation(ctx), sess)
	hold.Release()
	if err != nil || !ok {
		if err == nil {
			err = errors.New("validation did not complete")
		}
		if ctx.Err() != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrIngestRejected, err)
	}
	item, err := s.ingestedLibraryItem(contentType, contentID, meta.Season, meta.Episode, name)
	if err != nil {
		return nil, err
	}
	logger.Info("Library ingest stored release", "title", name, "id", item.ID, "file", item.MediaFileName, "size", item.MediaFileSize)
	return item, nil
}

// ingestedLibraryItem finds the row the preload just stored.
func (s *Server) ingestedLibraryItem(contentType, contentID string, season, episode int, name string) (*persistence.LibraryItem, error) {
	libStore := s.attemptRecorder.LibraryStore()
	items, err := libStore.GetCandidates(contentType, contentID, season, episode)
	if err != nil {
		return nil, fmt.Errorf("read stored release: %w", err)
	}
	for _, item := range items {
		if item != nil && strings.EqualFold(strings.TrimSpace(item.ReleaseTitle), name) {
			if item.Status != persistence.LibraryStatusGood {
				return nil, fmt.Errorf("%w: stored with status %s", ErrIngestRejected, item.Status)
			}
			return item, nil
		}
	}
	return nil, fmt.Errorf("validated release was not stored (library auto-save may be off and the save failed)")
}

// ingestContentType decides movie or series, and the episode a series
// release is, from the client's category when it said and the name when it
// did not. A multi-episode or season pack keeps episode at zero (and the
// season, when the pack names one).
func ingestContentType(requested string, parsed *parser.ParsedRelease) (contentType string, season, episode int) {
	if parsed != nil {
		switch {
		case len(parsed.Seasons) == 1 && len(parsed.Episodes) == 1:
			season, episode = parsed.Seasons[0], parsed.Episodes[0]
		case parsed.Season > 0 && parsed.Episode > 0:
			season, episode = parsed.Season, parsed.Episode
		case len(parsed.Seasons) == 1:
			// A season pack: the season is known, the episodes come from
			// the files.
			season = parsed.Seasons[0]
		}
	}
	episodic := parsed != nil && (len(parsed.Seasons) > 0 || len(parsed.Episodes) > 0)
	switch strings.ToLower(strings.TrimSpace(requested)) {
	case "movie":
		return "movie", 0, 0
	case "series":
		return "series", season, episode
	}
	if episodic {
		return "series", season, episode
	}
	return "movie", 0, 0
}

// resolveReleaseIdentity names the title a release is of: a TMDB search on
// the parsed title, matched on year when one was parsed, then the external
// ids of the match. Everything empty when TMDB is unavailable or has no
// answer; the row is then keyed by title.
func (s *Server) resolveReleaseIdentity(title string, year int, movieLike bool) (imdbID, tmdbID, tvdbID string) {
	rt := s.runtime()
	if rt.tmdbClient == nil || strings.TrimSpace(title) == "" {
		return "", "", ""
	}
	mediaType := "tv"
	if movieLike {
		mediaType = "movie"
	}
	listing, err := rt.tmdbClient.SearchByType(mediaType, title, 1, "")
	if err != nil || listing == nil || len(listing.Results) == 0 {
		return "", "", ""
	}
	pick := -1
	for i, res := range listing.Results {
		if year <= 0 {
			pick = i
			break
		}
		date := res.ReleaseDate
		if !movieLike {
			date = res.FirstAirDate
		}
		if y := yearOfDate(date); y != 0 && (y == year || y == year-1 || y == year+1) {
			pick = i
			break
		}
	}
	if pick < 0 {
		// Nothing matched the year; a series' first-air year often differs
		// from the one in a release name, so take the top hit over nothing.
		if !movieLike {
			pick = 0
		} else {
			return "", "", ""
		}
	}
	id := listing.Results[pick].ID
	tmdbID = strconv.Itoa(id)
	if ext, err := rt.tmdbClient.GetExternalIDs(id, mediaType); err == nil && ext != nil {
		imdbID = strings.TrimSpace(ext.IMDbID)
		if ext.TVDBID > 0 {
			tvdbID = strconv.Itoa(ext.TVDBID)
		}
	}
	return imdbID, tmdbID, tvdbID
}
