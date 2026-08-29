package stremio

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/dreulavelle/jhin/rank"

	"encoding/json"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search"
	"streamnzb/pkg/search/diag"
	"streamnzb/pkg/search/parser"
	"streamnzb/pkg/search/query"
	"streamnzb/pkg/search/ranking"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/services/availnzb"
)

type namedIndexer interface {
	Name() string
}

func indexerNameFromRelease(rel *release.Release) string {
	if rel == nil {
		return ""
	}
	if rel.IsLibraryResult() {
		name := strings.TrimSpace(rel.Indexer)
		if name == "" || name == "Library" || name == "StreamNZB Library" {
			return "StreamNZB Library"
		}
		if strings.HasPrefix(name, "StreamNZB Library - ") {
			return name
		}
		return "StreamNZB Library - " + name
	}
	if name := strings.TrimSpace(rel.Indexer); name != "" {
		return name
	}
	if rel.SourceIndexer != nil {
		if n, ok := rel.SourceIndexer.(namedIndexer); ok {
			if name := strings.TrimSpace(n.Name()); name != "" {
				return name
			}
		}
	}
	return ""
}

type playlistResult struct {
	Candidates       []triage.Candidate
	FirstIsAvailGood bool
	Params           *query.SearchParams

	CachedAvailable map[string]bool

	// UnavailableDetailsURLs is the set of release DetailsURLs known to be unavailable (AvailNZB false).
	// For AIOStreams we filter these out so we only return unknown or available (true).
	UnavailableDetailsURLs map[string]bool

	// SlotPaths, when set, gives the exact play path for each candidate (e.g. from failover order).
	// Must match len(Candidates); buildStreamsFromPlaylist uses SlotPaths[i] instead of key.SlotPath(i).
	SlotPaths []string

	IsAIOStreams bool
	FilterMode   string
}

type AvailContext struct {
	Result                  *availnzb.ReleasesResult
	InputResults            int
	ByDetailsURL            map[string]*availnzb.ReleaseWithStatus
	AvailableByDetailsURL   map[string]bool
	UnavailableByDetailsURL map[string]bool
}

type rawSearchResult struct {
	Params          *query.SearchParams
	IndexerReleases []*release.Release
	Avail           *AvailContext
	// Unaired marks an empty result produced by the air-date gate rather than
	// by a search: the episode cannot exist before Air.opensAt, so no indexer
	// was asked. The cache entry expires when the gate opens instead of
	// running the normal TTL.
	Unaired bool
	Air     airWindow
}

// emptyRawSearchTTL is how long a search that found nothing is cached. The
// normal TTL is tied to the session TTLs and refreshed on access, which is
// right for a result set that exists but wrong for an empty one: an episode
// that has just aired, or a title nobody had posted yet, would keep serving
// the same zero results for hours because every check pushed the deadline out
// again. Empty entries get a short, non-sliding window instead, so a retry a
// few minutes later actually reaches the indexers — bounded so that a title
// with genuinely nothing behind it still costs at most one fan-out per window.
const emptyRawSearchTTL = 15 * time.Minute

type playlistSource struct {
	Params                 *query.SearchParams
	Releases               []*release.Release
	Avail                  *AvailContext
	CachedAvailable        map[string]bool
	UnavailableDetailsURLs map[string]bool
}

// playlistCacheTTL returns how long playlist/raw-search cache entries stay valid.
// The playlist backs deferred catalog sessions and playback failover resolution
// (failed-slot redirects call buildPlaylist), so entries must live at least as
// long as the sessions that reference them — otherwise a mid-playback reconnect
// on a failed slot forces a full indexer re-search before the redirect can be
// computed. Tied to the configured session TTLs (whichever is longer) and
// refreshed on access, matching the inactivity-based session eviction semantics.
func (s *Server) playlistCacheTTL() time.Duration {
	rt := s.runtime()
	inactive := time.Duration(rt.config.EffectiveSessionTTLSeconds()) * time.Second
	postPlayback := time.Duration(rt.config.EffectiveSessionPostPlaybackTTLSeconds()) * time.Second
	if postPlayback > inactive {
		return postPlayback
	}
	return inactive
}

type playlistCacheEntry struct {
	result *playlistResult
	until  time.Time
	// live is result with live-source state applied: releases rebound to a
	// library copy, candidates whose indexer has no download budget left
	// promoted to another copy or dropped.
	//
	// Deriving it walks every candidate and asks the library for the content's
	// stored NZBs, and it is derived on every read of the playlist — which a
	// single play does several times (slot recovery, the failover walk, the
	// fallback prefetch). On a 103-candidate list that measured ~215 ms per
	// read, most of one play's slot-resolution budget, for state that changes
	// when the library or a daily budget does — not between two reads a
	// millisecond apart.
	//
	// A writer that changes result drops this instead of updating it: a nil
	// memo is simply re-derived on the next read.
	live      *playlistResult
	liveUntil time.Time
}

// liveSourceMemoTTL is how long a derived live-source view is reused. Long
// enough to cover one play's burst of playlist reads, short enough that a
// release that has just landed in the library, or a budget that has just come
// back, is picked up while the player is still on the same title.
const liveSourceMemoTTL = 10 * time.Second

type rawSearchCacheEntry struct {
	raw   *rawSearchResult
	until time.Time
}

func streamProviderSelections(stream *auth.Stream) []string {
	return stream.ActiveProviderSelections()
}

// filterPlaylistByOrder keeps only candidates whose slot path appears in order (same key, valid index), in that order.
// SlotPaths on the result are set from order so stream URLs match the client. Non-slot-path entries are ignored.
func filterPlaylistByOrder(list *playlistResult, key StreamSlotKey, order []string) *playlistResult {
	if list == nil || len(order) == 0 {
		return list
	}
	maxIndex := len(list.Candidates) - 1
	// When slot paths are present they, not the candidate index, are the
	// authority — an earlier pass (library rebind / spent-quota drop, or the
	// AIOStreams availability filter) may already have removed candidates, so
	// the index parsed out of a stored order entry no longer addresses the same
	// release. Same precedence resolveStreamSlotFromPlaylist applies.
	bySlotPath := map[string]int{}
	if len(list.SlotPaths) == len(list.Candidates) {
		for i, slotPath := range list.SlotPaths {
			bySlotPath[slotPath] = i
		}
	}
	var filtered []triage.Candidate
	var paths []string
	for _, entry := range order {
		if !strings.HasPrefix(entry, streamSlotPrefix) {
			continue
		}
		sid, ct, id, idx, ok := parseStreamSlotID(entry)
		if !ok {
			continue
		}
		if ct != key.ContentType || id != key.ID {
			continue
		}
		if sid != "" && sid != key.StreamID {
			continue
		}
		if len(bySlotPath) > 0 {
			mapped, found := bySlotPath[entry]
			if !found {
				continue
			}
			idx = mapped
		} else if idx < 0 || idx > maxIndex {
			continue
		}
		filtered = append(filtered, list.Candidates[idx])
		paths = append(paths, entry)
	}
	if len(filtered) == 0 {
		return list
	}
	firstAvail := false
	if list.CachedAvailable != nil && filtered[0].Release != nil && filtered[0].Release.DetailsURL != "" {
		firstAvail = list.CachedAvailable[filtered[0].Release.DetailsURL]
	}
	return &playlistResult{
		Candidates:             filtered,
		FirstIsAvailGood:       firstAvail,
		Params:                 list.Params,
		CachedAvailable:        list.CachedAvailable,
		UnavailableDetailsURLs: list.UnavailableDetailsURLs,
		SlotPaths:              paths,
	}
}

// filterPlaylistToAvailableForAIOStreams keeps only candidates that are unknown or available (true).
// Removes only those explicitly marked unavailable (AvailNZB false). Used when returning streams to AIOStreams.
func filterPlaylistToAvailableForAIOStreams(list *playlistResult) *playlistResult {
	if list == nil || list.UnavailableDetailsURLs == nil || len(list.UnavailableDetailsURLs) == 0 {
		return list
	}
	// Slot paths are filtered in lockstep rather than dropped: they are the only
	// record of which slot each surviving candidate answers to once an earlier
	// pass has already removed candidates, and filterPlaylistByOrder below reads
	// them to line stored failover orders up against this list.
	paths := list.SlotPaths
	if len(paths) != len(list.Candidates) {
		paths = nil
	}
	var filtered []triage.Candidate
	var filteredPaths []string
	prunedCopies := false
	for i, c := range list.Candidates {
		if c.Release != nil {
			// A reported-bad copy is dropped from the release rather than
			// taking the release with it, so a candidate survives on the
			// copies nobody has reported.
			surviving, removed := search.DropCopies(c.Release.Clone(), list.UnavailableDetailsURLs)
			if surviving == nil {
				continue
			}
			prunedCopies = prunedCopies || removed
			c.Release = surviving
		}
		filtered = append(filtered, c)
		if paths != nil {
			filteredPaths = append(filteredPaths, paths[i])
		}
	}
	if len(filtered) == len(list.Candidates) && !prunedCopies {
		return list
	}
	firstAvail := false
	if len(filtered) > 0 && list.CachedAvailable != nil && filtered[0].Release != nil && filtered[0].Release.DetailsURL != "" {
		firstAvail = list.CachedAvailable[filtered[0].Release.DetailsURL]
	}
	return &playlistResult{
		Candidates:             filtered,
		FirstIsAvailGood:       firstAvail,
		Params:                 list.Params,
		CachedAvailable:        list.CachedAvailable,
		UnavailableDetailsURLs: list.UnavailableDetailsURLs,
		SlotPaths:              filteredPaths,
	}
}

// buildPlaylist returns the candidate play list for (stream, type, id).
// Raw search and play list are both cached by the stable stream slot key.
// Relevant config changes clear these caches centrally after successful saves.
func (s *Server) buildPlaylist(ctx context.Context, key StreamSlotKey, isAIOStreams bool, stream *auth.Stream) (*playlistResult, error) {
	if key.StreamID == "" {
		key.StreamID = defaultStreamID
	}
	cacheKey := key.CacheKey()
	if v, ok := s.playlistCache.Load(cacheKey); ok {
		if ent, _ := v.(*playlistCacheEntry); ent != nil && time.Now().Before(ent.until) {
			candidateCount := 0
			if ent.result != nil {
				candidateCount = len(ent.result.Candidates)
			}
			logger.Debug("Playback playlist cache hit", "key", cacheKey, "candidates", candidateCount)
			now := time.Now()
			live, liveUntil := ent.live, ent.liveUntil
			if live == nil || !now.Before(liveUntil) {
				live = s.applyLiveSourceState(ent.result, key)
				liveUntil = now.Add(liveSourceMemoTTL)
			}
			// Sliding expiry: keep the entry alive while it is being used (reconnects, seeks, failover resolution).
			// CompareAndSwap so a concurrent update (e.g. bad-release filtering) is never clobbered by the refresh.
			s.playlistCache.CompareAndSwap(cacheKey, v, &playlistCacheEntry{
				result:    ent.result,
				until:     now.Add(s.playlistCacheTTL()),
				live:      live,
				liveUntil: liveUntil,
			})
			return live, nil
		}
	}
	logger.Debug("Playback playlist cache miss", "key", cacheKey)
	list, err := s.buildPlaylistUncached(ctx, key, isAIOStreams, stream)
	if err != nil || list == nil {
		return list, err
	}
	// Cache the full list and filter the view: a fresh build can also be stale
	// by the time it is replayed, and a quota that comes back should simply
	// stop filtering rather than force a re-search.
	now := time.Now()
	live := s.applyLiveSourceState(list, key)
	s.playlistCache.Store(cacheKey, &playlistCacheEntry{
		result:    list,
		until:     now.Add(s.playlistCacheTTL()),
		live:      live,
		liveUntil: now.Add(liveSourceMemoTTL),
	})
	return live, nil
}

func (s *Server) buildPlaylistUncached(ctx context.Context, key StreamSlotKey, isAIOStreams bool, stream *auth.Stream) (*playlistResult, error) {
	// Diagnose every uncached build: the collector rides ctx through search,
	// validation, dedup and the filter profile, and the snapshot lands as one
	// history row. Cache hits skip this whole path, which is the right
	// granularity — a cached playlist answers with the funnel of the build
	// that produced it.
	ctx, collector := diag.Begin(ctx)
	raw, err := s.getOrBuildRawSearchResult(ctx, key.ContentType, key.ID, stream)
	if err != nil || raw == nil {
		return nil, err
	}
	list, err := s.buildPlaylistFromRaw(ctx, raw, isAIOStreams, stream)
	if err == nil {
		s.recordSearchDiagnostics(key, stream, raw, collector)
	}
	return list, err
}

// recordSearchDiagnostics persists the collector's snapshot for the history
// page. Best-effort: diagnostics must never fail a playlist build.
func (s *Server) recordSearchDiagnostics(key StreamSlotKey, stream *auth.Stream, raw *rawSearchResult, collector *diag.Collector) {
	if s == nil || s.attemptRecorder == nil || collector == nil {
		return
	}
	payload, err := json.Marshal(collector.Snapshot())
	if err != nil {
		return
	}
	title := ""
	if raw != nil && raw.Params != nil {
		title = raw.Params.ContentTitle
	}
	s.attemptRecorder.RecordSearchDiagnostic(persistence.SearchDiagnostic{
		StreamName:   streamID(stream),
		ContentType:  key.ContentType,
		ContentID:    key.ID,
		ContentTitle: title,
		Payload:      string(payload),
	})
}

func (s *Server) getOrBuildRawSearchResult(ctx context.Context, contentType, id string, stream *auth.Stream) (*rawSearchResult, error) {
	rawKey := streamID(stream) + ":" + contentType + ":" + id
	if v, ok := s.rawSearchCache.Load(rawKey); ok {
		if ent, _ := v.(*rawSearchCacheEntry); ent != nil && time.Now().Before(ent.until) {
			releaseCount := 0
			if ent.raw != nil {
				releaseCount = len(ent.raw.IndexerReleases)
			}
			logger.Debug("Playback candidate cache hit", "key", rawKey, "releases", releaseCount)
			// Sliding expiry: keep the entry alive while it is being used.
			// CompareAndSwap so a concurrent update (e.g. bad-release filtering) is never clobbered by the refresh.
			if rawSearchCacheSlides(ent.raw) {
				s.rawSearchCache.CompareAndSwap(rawKey, v, &rawSearchCacheEntry{raw: ent.raw, until: s.rawSearchCacheUntil(ent.raw)})
			}
			return cloneRawSearchResult(ent.raw), nil
		}
	}
	logger.Debug("Playback candidate cache miss", "key", rawKey)
	raw, err := s.buildRawSearchResult(ctx, contentType, id, stream)
	if err != nil || raw == nil {
		return nil, err
	}
	s.rawSearchCache.Store(rawKey, &rawSearchCacheEntry{raw: raw, until: s.rawSearchCacheUntil(raw)})
	return cloneRawSearchResult(raw), nil
}

// rawSearchCacheUntil picks a cache deadline for one raw result: the normal
// sliding TTL for a result set, the short non-sliding window for an empty one,
// and for an unaired short-circuit the moment the gate opens, so the empty
// result stops being served exactly when a search could start finding things.
func (s *Server) rawSearchCacheUntil(raw *rawSearchResult) time.Time {
	until := time.Now().Add(s.playlistCacheTTL())
	if raw == nil {
		return until
	}
	if raw.Unaired && !raw.Air.opensAt.IsZero() {
		// Match airedByTime: it opens the gate a margin early, so holding the
		// cached answer to the gate instant itself would keep serving
		// "unaired" for a quarter hour after searching became worthwhile.
		// This tracks opensAt, not the scheduled air time — pinning it to the
		// schedule would re-close the window the gate deliberately opens.
		if opens := raw.Air.opensAt.Add(-unairedMargin); opens.Before(until) {
			return opens
		}
		return until
	}
	if len(raw.IndexerReleases) == 0 {
		if empty := time.Now().Add(emptyRawSearchTTL); empty.Before(until) {
			return empty
		}
	}
	return until
}

// rawSearchCacheSlides reports whether a cache hit may push the entry's
// deadline out. Only a result set earns that: refreshing an empty or unaired
// entry on access would let a client that keeps asking hold the stale answer
// open indefinitely, which is the one thing the short window exists to stop.
func rawSearchCacheSlides(raw *rawSearchResult) bool {
	return raw != nil && !raw.Unaired && len(raw.IndexerReleases) > 0
}

func (s *Server) GetSearchReleases(ctx context.Context, contentType, id string) (*SearchReleasesResponse, error) {
	rt := s.runtime()
	fallbackStream := &auth.Stream{Username: defaultStreamID}
	if contentType == "movie" {
		fallbackStream.MovieSearchQueries = allSearchQueryNames(rt.config.MovieSearchQueries)
	} else {
		fallbackStream.SeriesSearchQueries = allSearchQueryNames(rt.config.SeriesSearchQueries)
	}
	raw, err := s.getOrBuildRawSearchResult(ctx, contentType, id, fallbackStream)
	if err != nil || raw == nil {
		return nil, err
	}
	source := buildPlaylistSource(raw, false)
	type releaseWithAvail struct {
		rel   *release.Release
		avail string
	}
	unified := make([]releaseWithAvail, 0, len(source.Releases))
	for _, r := range source.Releases {
		if r == nil {
			continue
		}
		avail := "Unknown"
		if r.DetailsURL != "" {
			if source.Avail != nil {
				if source.Avail.AvailableByDetailsURL[r.DetailsURL] {
					avail = "Available"
				} else if source.Avail.UnavailableByDetailsURL[r.DetailsURL] {
					avail = "Unavailable"
				}
			}
		}
		unified = append(unified, releaseWithAvail{rel: r, avail: avail})
	}

	releasesOut := make([]SearchReleaseTag, 0, len(unified))
	for _, u := range unified {
		r := u.rel
		idxName := r.Indexer
		if idxName == "" && r.SourceIndexer != nil {
			if idx, ok := r.SourceIndexer.(indexer.Indexer); ok {
				idxName = idx.Name()
			}
		}
		if idxName == "" {
			idxName = "Indexer"
		}
		releasesOut = append(releasesOut, SearchReleaseTag{
			Title:        r.Title,
			Link:         r.Link,
			DetailsURL:   r.DetailsURL,
			Size:         r.Size,
			Indexer:      idxName,
			Availability: u.avail,
		})
	}

	return &SearchReleasesResponse{Releases: releasesOut}, nil
}

func populateAvailable(raw *rawSearchResult) {
	if raw.Avail != nil && raw.Avail.Result != nil {
		for _, rws := range raw.Avail.Result.Releases {
			if rws == nil || rws.Release == nil {
				continue
			}
			if rws.Available {
				rws.Release.Available = &availTrue
			} else {
				rws.Release.Available = &availFalse
			}
		}
	}
	if raw.Avail != nil && len(raw.Avail.AvailableByDetailsURL) > 0 {
		for _, rel := range raw.IndexerReleases {
			if rel != nil && rel.DetailsURL != "" && raw.Avail.AvailableByDetailsURL[rel.DetailsURL] {
				rel.Available = &availTrue
			}
		}
	}
}

func (s *Server) providerHostsForStream(stream *auth.Stream) []string {
	rt := s.runtime()
	if s.sessionManager != nil {
		if hosts := s.sessionManager.ProviderHostsForProviders(streamProviderSelections(stream)); len(hosts) > 0 {
			return hosts
		}
	}
	if rt.validator != nil {
		return rt.validator.GetProviderHosts()
	}
	return nil
}

// segmentFetcherForStream builds this stream's view of the usenet pool, capped
// by its per-provider connection limits. The lease key is the stream username,
// so all of a stream's sessions draw on one connection budget instead of each
// getting a fresh allowance.
func (s *Server) segmentFetcherForStream(stream *auth.Stream) loader.SegmentFetcher {
	if s.sessionManager == nil {
		return nil
	}
	if stream == nil {
		return s.sessionManager.SegmentFetcherForLease(streamID(nil), nil, nil)
	}
	// streamID, not Username: it matches the name the session is tagged with, so
	// fetched bytes and the session they were fetched for line up under one key.
	return s.sessionManager.SegmentFetcherForLease(streamID(stream), streamProviderSelections(stream), stream.ProviderConnectionLimits)
}

func buildAllReleasesFromRaw(raw *rawSearchResult) []*release.Release {
	var out []*release.Release
	for _, rel := range raw.IndexerReleases {
		if rel == nil {
			continue
		}
		if release.IsFullDiscRelease(rel.Title) {
			continue
		}
		out = append(out, rel)
	}
	return out
}

func buildPlaylistSource(raw *rawSearchResult, filteringActive bool) *playlistSource {
	if raw == nil {
		return &playlistSource{
			CachedAvailable:        map[string]bool{},
			UnavailableDetailsURLs: map[string]bool{},
		}
	}
	populateAvailable(raw)
	cachedAvailable := map[string]bool{}
	if raw.Avail != nil && raw.Avail.AvailableByDetailsURL != nil {
		cachedAvailable = raw.Avail.AvailableByDetailsURL
	}
	return &playlistSource{
		Params:                 raw.Params,
		Releases:               buildAllReleasesFromRaw(raw),
		Avail:                  raw.Avail,
		CachedAvailable:        cachedAvailable,
		UnavailableDetailsURLs: buildUnavailableDetailsURLs(raw.Avail),
	}
}

func releasesToCandidates(releases []*release.Release) []triage.Candidate {
	var out []triage.Candidate
	for _, rel := range releases {
		if rel == nil {
			continue
		}
		// Metadata is filled in by applyRanking, which gets the parse from
		// the ranker rather than parsing every title a second time here.
		out = append(out, triage.Candidate{Release: rel})
	}
	return out
}

func (s *Server) buildPlaylistFromRaw(ctx context.Context, raw *rawSearchResult, isAIOStreams bool, stream *auth.Stream) (*playlistResult, error) {
	filterMode, filteringActive := resolveFilterMode(stream)
	source := buildPlaylistSource(raw, filteringActive)
	inputCandidates := buildPlaylistCandidates(source)
	candidates := s.applyPlaylistFiltering(inputCandidates, source, isAIOStreams, filteringActive, filterMode, stream)
	candidates = s.applyRanking(ctx, candidates, source, filteringActive, filterMode, stream)
	s.recordAvailIndexerStats(inputCandidates, candidates, source, filteringActive, stream)
	res := buildPlaylistResult(source, candidates)
	res.IsAIOStreams = isAIOStreams
	res.FilterMode = filterMode
	return res, nil
}

func (s *Server) shouldFilterAvailNZBReportedBad(stream *auth.Stream) bool {
	rt := s.runtime()
	if s == nil || rt.config == nil {
		return false
	}
	return stream.EffectiveFilterAvailNZB(rt.config)
}

func (s *Server) recordAvailIndexerStats(inputCandidates, finalCandidates []triage.Candidate, source *playlistSource, filteringActive bool, stream *auth.Stream) {
	if source == nil {
		return
	}
	availableByIndexer := make(map[string]int)
	discardedByIndexer := make(map[string]int)

	// Every copy, not just the primary: a merged release carries one copy per
	// indexer, and the availability record is keyed by the copy's own details
	// URL. Counting only the primary would credit whichever indexer won the
	// variant merge and leave every other indexer's availability at zero.
	if s.shouldFilterAvailNZBReportedBad(stream) && len(source.UnavailableDetailsURLs) > 0 {
		for _, c := range inputCandidates {
			for _, copyRel := range c.Release.Copies() {
				if copyRel == nil || copyRel.DetailsURL == "" {
					continue
				}
				if !source.UnavailableDetailsURLs[copyRel.DetailsURL] {
					continue
				}
				if name := indexerNameFromRelease(copyRel); name != "" {
					discardedByIndexer[name]++
				}
			}
		}
	}

	if len(source.CachedAvailable) > 0 {
		for _, c := range finalCandidates {
			for _, copyRel := range c.Release.Copies() {
				if copyRel == nil || copyRel.DetailsURL == "" {
					continue
				}
				if !source.CachedAvailable[copyRel.DetailsURL] {
					continue
				}
				if name := indexerNameFromRelease(copyRel); name != "" {
					availableByIndexer[name]++
				}
			}
		}
	}

	s.addAvailIndexerStats(availableByIndexer, discardedByIndexer)
}

func resolveFilterMode(stream *auth.Stream) (string, bool) {
	filterMode := "none"
	if stream != nil && strings.TrimSpace(stream.FilterSortingMode) != "" {
		filterMode = strings.ToLower(strings.TrimSpace(stream.FilterSortingMode))
	}
	return filterMode, filterMode != "none"
}

func cloneReleaseForPlaylist(rel *release.Release) *release.Release {
	return rel.Clone()
}

func cloneAvailContext(availCtx *AvailContext) *AvailContext {
	if availCtx == nil {
		return nil
	}
	next := &AvailContext{
		InputResults:            availCtx.InputResults,
		ByDetailsURL:            make(map[string]*availnzb.ReleaseWithStatus, len(availCtx.ByDetailsURL)),
		AvailableByDetailsURL:   make(map[string]bool, len(availCtx.AvailableByDetailsURL)),
		UnavailableByDetailsURL: make(map[string]bool, len(availCtx.UnavailableByDetailsURL)),
	}
	for k, v := range availCtx.AvailableByDetailsURL {
		next.AvailableByDetailsURL[k] = v
	}
	for k, v := range availCtx.UnavailableByDetailsURL {
		next.UnavailableByDetailsURL[k] = v
	}
	if availCtx.Result != nil {
		result := &availnzb.ReleasesResult{
			ImdbID: availCtx.Result.ImdbID,
			Count:  availCtx.Result.Count,
		}
		for _, rws := range availCtx.Result.Releases {
			if rws == nil {
				result.Releases = append(result.Releases, nil)
				continue
			}
			copyRWS := *rws
			copyRWS.Release = cloneReleaseForPlaylist(rws.Release)
			result.Releases = append(result.Releases, &copyRWS)
			if copyRWS.Release != nil && copyRWS.Release.DetailsURL != "" {
				next.ByDetailsURL[copyRWS.Release.DetailsURL] = &copyRWS
			}
		}
		next.Result = result
	}
	return next
}

func cloneRawSearchResult(raw *rawSearchResult) *rawSearchResult {
	if raw == nil {
		return nil
	}
	next := &rawSearchResult{
		Params:  cloneSearchParams(raw.Params),
		Avail:   cloneAvailContext(raw.Avail),
		Unaired: raw.Unaired,
		Air:     raw.Air,
	}
	if raw.IndexerReleases != nil {
		next.IndexerReleases = make([]*release.Release, 0, len(raw.IndexerReleases))
		for _, rel := range raw.IndexerReleases {
			next.IndexerReleases = append(next.IndexerReleases, cloneReleaseForPlaylist(rel))
		}
	}
	return next
}

func clonePlaylistResult(list *playlistResult) *playlistResult {
	if list == nil {
		return nil
	}
	next := &playlistResult{
		FirstIsAvailGood:       list.FirstIsAvailGood,
		Params:                 cloneSearchParams(list.Params),
		CachedAvailable:        make(map[string]bool, len(list.CachedAvailable)),
		UnavailableDetailsURLs: make(map[string]bool, len(list.UnavailableDetailsURLs)),
	}
	if list.Candidates != nil {
		next.Candidates = make([]triage.Candidate, 0, len(list.Candidates))
		for _, candidate := range list.Candidates {
			copyCandidate := candidate
			copyCandidate.Release = cloneReleaseForPlaylist(candidate.Release)
			next.Candidates = append(next.Candidates, copyCandidate)
		}
	}
	if list.SlotPaths != nil {
		next.SlotPaths = append([]string(nil), list.SlotPaths...)
	}
	for k, v := range list.CachedAvailable {
		next.CachedAvailable[k] = v
	}
	for k, v := range list.UnavailableDetailsURLs {
		next.UnavailableDetailsURLs[k] = v
	}
	return next
}

// Outcomes of re-resolving one cached candidate against live source state.
const (
	liveSourceKeep = iota
	liveSourceRebindLibrary
	liveSourcePromoteCopy
	liveSourceDrop
)

// applyLiveSourceState re-resolves every candidate's source against the two
// things that move after a playlist is cached: the library gains a stored NZB
// when a release plays through, and an indexer's daily download budget runs
// out. Both caches freeze SourceIndexer at build time, so without this a replay
// inside the TTL re-grabs an NZB already sitting in SQLite — spending the very
// budget this pass exists to protect — while candidates behind a spent quota
// stay on offer for the user to walk one failover hop at a time.
//
// The result is a filtered view; the caller keeps caching the full list. That
// is deliberate: a quota is transient, so dropping candidates from the cache
// would mean re-searching (and paying API hits) the moment the day rolls over,
// where a view simply stops filtering.
func (s *Server) applyLiveSourceState(list *playlistResult, key StreamSlotKey) *playlistResult {
	if list == nil || len(list.Candidates) == 0 {
		return list
	}
	started := time.Now()
	libraryItems := s.libraryNZBsByDetailsURL(list.Params)
	grabbable := make(map[indexer.Indexer]bool)
	actions := make([]int, len(list.Candidates))
	rebinds, promotions, drops := 0, 0, 0
	for i, candidate := range list.Candidates {
		actions[i] = s.liveSourceAction(candidate.Release, libraryItems, grabbable)
		switch actions[i] {
		case liveSourceRebindLibrary:
			rebinds++
		case liveSourcePromoteCopy:
			promotions++
		case liveSourceDrop:
			drops++
		}
	}
	if rebinds == 0 && promotions == 0 && drops == 0 {
		return list
	}

	next := clonePlaylistResult(list)
	// Slot paths are captured before filtering and carried through, so a client
	// holding /play/<key>:3 still resolves to the same release after a drop —
	// the same lockstep markPlaylistResultUnavailable maintains.
	paths := playlistSlotPaths(next, key)
	if len(paths) != len(next.Candidates) {
		paths = nil
	}
	keptCandidates := make([]triage.Candidate, 0, len(next.Candidates))
	keptPaths := make([]string, 0, len(next.Candidates))
	for i, candidate := range next.Candidates {
		switch actions[i] {
		case liveSourceDrop:
			continue
		case liveSourceRebindLibrary:
			rebindReleaseToLibrary(candidate.Release, libraryItems[candidate.Release.DetailsURL])
		case liveSourcePromoteCopy:
			promoted := s.promoteGrabbableCopy(candidate.Release, grabbable)
			if promoted == nil {
				continue
			}
			candidate.Release = promoted
		}
		keptCandidates = append(keptCandidates, candidate)
		if paths != nil {
			keptPaths = append(keptPaths, paths[i])
		}
	}
	next.Candidates = keptCandidates
	if paths != nil {
		next.SlotPaths = keptPaths
	}
	recomputePlaylistAvailability(next)
	logger.Debug("Playlist sources re-resolved",
		"key", key.CacheKey(),
		"took_ms", time.Since(started).Milliseconds(),
		"rebound_to_library", rebinds,
		"promoted_to_other_copy", promotions,
		"dropped_no_download_budget", drops,
		"candidates", len(next.Candidates),
	)
	return next
}

// liveSourceAction decides what to do with one cached candidate. Rebind is
// tested before the download budget and never the other way round: a release
// now held in the library still names the indexer it originally came from, so
// checking the budget first would drop exactly the candidates that no longer
// need an indexer at all.
func (s *Server) liveSourceAction(rel *release.Release, libraryItems map[string]*persistence.LibraryItem, grabbable map[indexer.Indexer]bool) int {
	if rel == nil || rel.IsLibraryResult() {
		return liveSourceKeep
	}
	if url := strings.TrimSpace(rel.DetailsURL); url != "" {
		if item := libraryItems[url]; item != nil {
			return liveSourceRebindLibrary
		}
	}
	if s.releaseSourceCanGrab(rel, grabbable) {
		return liveSourceKeep
	}
	// A spent budget kills the copy, not the release: another indexer's copy of
	// the same release is a live source, and dropping the candidate would
	// throw it away along with the dead one.
	for i := 1; i < rel.CopyCount(); i++ {
		if s.releaseSourceCanGrab(rel.CopyAt(i), grabbable) {
			return liveSourcePromoteCopy
		}
	}
	return liveSourceDrop
}

// promoteGrabbableCopy reorders a merged release so a copy that can still be
// grabbed leads, keeping the spent ones behind it rather than dropping them —
// a daily budget comes back, and the copies are only a failover hop away.
// Returns nil when no copy can be grabbed at all.
func (s *Server) promoteGrabbableCopy(rel *release.Release, grabbable map[indexer.Indexer]bool) *release.Release {
	var live, spent []*release.Release
	for _, c := range rel.Copies() {
		if s.releaseSourceCanGrab(c, grabbable) {
			live = append(live, c)
			continue
		}
		spent = append(spent, c)
	}
	if len(live) == 0 {
		return nil
	}
	primary := live[0]
	variants := append(append([]*release.Release(nil), live[1:]...), spent...)
	for _, variant := range variants {
		// The old primary is now a variant, and variants never nest.
		variant.Variants = nil
	}
	primary.Variants = variants
	return primary
}

// releaseSourceCanGrab reports whether the indexer behind rel can still fetch
// an NZB. Only a spent daily download budget counts: a throttle cooldown is
// minutes long and failover steps past it cheaply, but a spent quota holds
// until the day turns over, and every candidate behind it is a dead slot.
func (s *Server) releaseSourceCanGrab(rel *release.Release, grabbable map[indexer.Indexer]bool) bool {
	rt := s.runtime()
	idx, _ := rel.SourceIndexer.(indexer.Indexer)
	if idx == nil {
		// Unknown source: the aggregate answers for it, and it still has budget
		// as long as any single indexer does.
		idx = rt.indexer
	}
	if idx == nil {
		return true
	}
	if can, ok := grabbable[idx]; ok {
		return can
	}
	usage := idx.GetUsage()
	can := usage.DownloadsLimit <= 0 || usage.DownloadsRemaining > 0
	grabbable[idx] = can
	return can
}

// rebindReleaseToLibrary points a cached release at its library row, so the NZB
// comes from SQLite instead of a grab and the name matches a fresh library hit.
// Link is left pointing at the indexer: nothing reads it once NZB bytes are
// present, and keeping it preserves the grab as a fallback. Ranking and parse
// metadata already on the release are untouched — only the source moves.
func rebindReleaseToLibrary(rel *release.Release, item *persistence.LibraryItem) {
	if rel == nil || item == nil {
		return
	}
	rel.SourceIndexer = item
	rel.IsLibrary = true
	rel.Indexer = libraryIndexerName(item.IndexerName)
}

// libraryNZBsByDetailsURL indexes this content's library rows that carry a
// stored NZB, keyed by details URL. Gated on the library search mode: with it
// disabled a fresh build would not have surfaced these as library hits either,
// and this pass exists to make a cached playlist agree with what a fresh one
// would produce.
func (s *Server) libraryNZBsByDetailsURL(params *query.SearchParams) map[string]*persistence.LibraryItem {
	rt := s.runtime()
	if s == nil || rt.config == nil || params == nil || s.attemptRecorder == nil {
		return nil
	}
	if rt.config.EffectiveLibrarySearchMode() == "disabled" {
		return nil
	}
	libStore := s.attemptRecorder.LibraryStore()
	if libStore == nil {
		return nil
	}
	season, episode := 0, 0
	if params.ContentIDs != nil {
		season = params.ContentIDs.Season
		episode = params.ContentIDs.Episode
	}
	items, err := libStore.GetCandidatesByIDs(params.ContentType, params.Req.IMDbID, params.Req.TMDBID, params.Req.TVDBID, params.Req.KitsuID, season, episode)
	if err != nil || len(items) == 0 {
		return nil
	}
	byURL := make(map[string]*persistence.LibraryItem, len(items))
	for _, item := range items {
		if item == nil || len(item.NZBData) == 0 {
			continue
		}
		if url := strings.TrimSpace(item.DetailsURL); url != "" {
			byURL[url] = item
		}
	}
	return byURL
}

func playlistSlotPaths(list *playlistResult, key StreamSlotKey) []string {
	if list == nil || len(list.Candidates) == 0 {
		return nil
	}
	if len(list.SlotPaths) == len(list.Candidates) {
		return append([]string(nil), list.SlotPaths...)
	}
	paths := make([]string, len(list.Candidates))
	for i := range list.Candidates {
		paths[i] = key.SlotPath(i)
	}
	return paths
}

func markRawSearchResultUnavailable(raw *rawSearchResult, detailsURL string) bool {
	if raw == nil || strings.TrimSpace(detailsURL) == "" {
		return false
	}
	changed := false
	if raw.Avail != nil {
		if raw.Avail.AvailableByDetailsURL != nil && raw.Avail.AvailableByDetailsURL[detailsURL] {
			delete(raw.Avail.AvailableByDetailsURL, detailsURL)
			changed = true
		}
		if raw.Avail.UnavailableByDetailsURL == nil {
			raw.Avail.UnavailableByDetailsURL = make(map[string]bool)
		}
		if !raw.Avail.UnavailableByDetailsURL[detailsURL] {
			raw.Avail.UnavailableByDetailsURL[detailsURL] = true
			changed = true
		}
		if rws := raw.Avail.ByDetailsURL[detailsURL]; rws != nil {
			if rws.Available {
				rws.Available = false
				changed = true
			}
			if rws.Release != nil {
				if rws.Release.Available == nil || *rws.Release.Available {
					rws.Release.Available = &availFalse
					changed = true
				}
			}
		}
		if raw.Avail.Result != nil {
			for _, rws := range raw.Avail.Result.Releases {
				if rws == nil || rws.Release == nil || rws.Release.DetailsURL != detailsURL {
					continue
				}
				if rws.Available {
					rws.Available = false
					changed = true
				}
				if rws.Release.Available == nil || *rws.Release.Available {
					rws.Release.Available = &availFalse
					changed = true
				}
			}
		}
	}
	for _, rel := range raw.IndexerReleases {
		for _, c := range rel.Copies() {
			if c == nil || c.DetailsURL != detailsURL {
				continue
			}
			if c.Available == nil || *c.Available {
				c.Available = &availFalse
				changed = true
			}
		}
	}
	return changed
}

func markPlaylistResultUnavailable(list *playlistResult, key StreamSlotKey, detailsURL, slotPath string) bool {
	if list == nil {
		return false
	}
	changed := false
	detailsURL = strings.TrimSpace(detailsURL)
	slotPath = strings.TrimSpace(slotPath)
	if detailsURL != "" {
		if list.CachedAvailable != nil && list.CachedAvailable[detailsURL] {
			delete(list.CachedAvailable, detailsURL)
			changed = true
		}
		if list.UnavailableDetailsURLs == nil {
			list.UnavailableDetailsURLs = make(map[string]bool)
		}
		if !list.UnavailableDetailsURLs[detailsURL] {
			list.UnavailableDetailsURLs[detailsURL] = true
			changed = true
		}
	}

	paths := playlistSlotPaths(list, key)
	if len(paths) != len(list.Candidates) {
		recomputePlaylistAvailability(list)
		return changed
	}

	filteredCandidates := make([]triage.Candidate, 0, len(list.Candidates))
	filteredPaths := make([]string, 0, len(paths))
	removed := false
	for i, candidate := range list.Candidates {
		// Any copy matching retires the whole candidate: this runs once the
		// release has been given up on, and by then the copy that failed last
		// is not necessarily the one the candidate leads with.
		matchesURL := detailsURL != "" && candidate.Release.HasCopyURL(detailsURL)
		if matchesURL || (slotPath != "" && paths[i] == slotPath) {
			removed = true
			continue
		}
		filteredCandidates = append(filteredCandidates, candidate)
		filteredPaths = append(filteredPaths, paths[i])
	}
	if removed {
		list.Candidates = filteredCandidates
		list.SlotPaths = filteredPaths
		changed = true
	}
	recomputePlaylistAvailability(list)
	return changed
}

func recomputePlaylistAvailability(list *playlistResult) {
	if list == nil {
		return
	}
	list.FirstIsAvailGood = false
	if len(list.Candidates) == 0 || list.CachedAvailable == nil || list.Candidates[0].Release == nil {
		return
	}
	detailsURL := list.Candidates[0].Release.DetailsURL
	if detailsURL == "" {
		return
	}
	list.FirstIsAvailGood = list.CachedAvailable[detailsURL]
}

func (s *Server) markCachedReleaseUnavailable(key StreamSlotKey, detailsURL, slotPath string) {
	if strings.TrimSpace(detailsURL) == "" {
		return
	}
	updated := false
	cacheKey := key.CacheKey()
	if v, ok := s.playlistCache.Load(cacheKey); ok {
		if ent, _ := v.(*playlistCacheEntry); ent != nil && time.Now().Before(ent.until) && ent.result != nil {
			nextResult := clonePlaylistResult(ent.result)
			if markPlaylistResultUnavailable(nextResult, key, detailsURL, slotPath) {
				if len(nextResult.Candidates) == 0 {
					s.playlistCache.Delete(cacheKey)
				} else {
					s.playlistCache.Store(cacheKey, &playlistCacheEntry{result: nextResult, until: ent.until})
				}
				updated = true
			}
		}
	}

	rawKey := key.StreamID + ":" + key.ContentType + ":" + key.ID
	if v, ok := s.rawSearchCache.Load(rawKey); ok {
		if ent, _ := v.(*rawSearchCacheEntry); ent != nil && time.Now().Before(ent.until) && ent.raw != nil {
			nextRaw := cloneRawSearchResult(ent.raw)
			if markRawSearchResultUnavailable(nextRaw, detailsURL) {
				if len(nextRaw.IndexerReleases) == 0 {
					s.rawSearchCache.Delete(rawKey)
				} else {
					s.rawSearchCache.Store(rawKey, &rawSearchCacheEntry{raw: nextRaw, until: ent.until})
				}
				updated = true
			}
		}
	}
	if updated {
		logger.Debug("Playback caches updated after bad release report", "key", cacheKey, "details_url", detailsURL, "slot", slotPath)
	}
}

func buildUnavailableDetailsURLs(availCtx *AvailContext) map[string]bool {
	out := make(map[string]bool)
	if availCtx == nil {
		return out
	}
	for detailsURL := range availCtx.UnavailableByDetailsURL {
		out[detailsURL] = true
	}
	return out
}

func filterCandidates(merged []triage.Candidate, isAIOStreams, filteringActive bool, unavailableDetailsURLs map[string]bool) []triage.Candidate {
	if len(merged) == 0 {
		return merged
	}
	seenTitle := make(map[string]bool)
	seenDetailsURL := make(map[string]bool)
	seenGUID := make(map[string]bool)

	filtered := make([]triage.Candidate, 0, len(merged))
	for _, c := range merged {
		if c.Release == nil {
			continue
		}
		if len(unavailableDetailsURLs) > 0 {
			// Reported bad retires the copy that was reported, not the release
			// it is a copy of: a release only drops out once every copy of it
			// is reported bad.
			surviving, _ := search.DropCopies(c.Release.Clone(), unavailableDetailsURLs)
			if surviving == nil {
				continue
			}
			c.Release = surviving
		}
		if c.Release.DetailsURL != "" {
			if seenDetailsURL[c.Release.DetailsURL] {
				continue
			}
			seenDetailsURL[c.Release.DetailsURL] = true
		}
		if c.Release.GUID != "" {
			if seenGUID[c.Release.GUID] {
				continue
			}
			seenGUID[c.Release.GUID] = true
		}
		if c.Release.Title != "" {
			titleKey := release.NormalizeTitleForDedup(c.Release.Title)
			if titleKey != "" {
				if seenTitle[titleKey] {
					continue
				}
				seenTitle[titleKey] = true
			}
		}
		filtered = append(filtered, c)
	}
	return filtered
}

func buildPlaylistCandidates(source *playlistSource) []triage.Candidate {
	if source == nil {
		return nil
	}
	return releasesToCandidates(source.Releases)
}

func (s *Server) applyPlaylistFiltering(candidates []triage.Candidate, source *playlistSource, isAIOStreams, filteringActive bool, filterMode string, stream *auth.Stream) []triage.Candidate {
	availReportedBadFilteringEnabled := s.shouldFilterAvailNZBReportedBad(stream)
	unavailableDetailsURLs := map[string]bool{}
	if availReportedBadFilteringEnabled {
		// Remove releases explicitly known as unavailable by AvailNZB when enabled.
		unavailableDetailsURLs = source.UnavailableDetailsURLs
	}
	availFilteredOut := countCandidatesByUnavailableDetailsURL(candidates, unavailableDetailsURLs)
	candidates = filterCandidates(candidates, isAIOStreams, filteringActive, unavailableDetailsURLs)
	unavailableKnownCount := 0
	if source != nil && len(source.UnavailableDetailsURLs) > 0 {
		unavailableKnownCount = len(source.UnavailableDetailsURLs)
	}
	logAvailReportedBadFiltering(stream, availReportedBadFilteringEnabled, availFilteredOut, unavailableKnownCount)
	if !filteringActive {
		return candidates
	}
	inputResults := len(candidates)
	candidates = s.filterCachedUnhealthyCandidates(candidates, source.Avail, filteringActive, stream)
	logStreamFiltering(stream, filterMode, inputResults, len(candidates))
	return candidates
}

// applyRanking hands the surviving candidates to jhin: it decides which are
// eligible, scores them, and orders them. Availability filtering has already
// run, so everything dropped here is a profile decision.
//
// Streams with no profile bound keep the pre-jhin ordering, so filtering stays
// opt-in rather than silently changing what an unconfigured stream returns.
func (s *Server) applyRanking(ctx context.Context, candidates []triage.Candidate, source *playlistSource, filteringActive bool, filterMode string, stream *auth.Stream) []triage.Candidate {
	rt := s.runtime()
	req := rankingRequest(source)
	profile := s.profileForKind(req.Kind, stream)
	inputResults := len(candidates)
	req.Seadex = s.seadexContext(ctx, source, stream, profile)

	// What ffprobe measured and what the availability database reports are
	// attached before any profile runs: stream descriptions render them
	// whether or not this stream filters, and profile rules read them.
	s.annotateVerdicts(candidates, source, stream)

	if profile == nil {
		if filteringActive {
			sortCandidates(rt.triageService, candidates)
		}
		// Unconditional: scoring happens to populate metadata today, but
		// stream descriptions need it whatever that path does.
		ensureCandidateMetadata(candidates)
		// With no profile there is no recordVerdicts pass, so the SeaDex
		// verdict a format template renders is attached here.
		annotateSeadexVerdicts(candidates, req.Seadex)
		logRankingSelection(source, stream, req.Kind, nil, inputResults, inputResults)
		logStreamSorting(stream, filterMode, len(candidates), len(candidates))
		return candidates
	}

	results, rejected := profile.ApplyWithRejected(req, candidates, rank.RankOptions{})
	logRankingSelection(source, stream, req.Kind, profile, inputResults, len(results))
	diag.From(ctx).SetProfile(profile.Name, inputResults, len(results), rejectedForDiag(rejected))

	out := make([]triage.Candidate, 0, len(results))
	for _, r := range results {
		cand := r.Candidate
		// Reuse the ranker's parse instead of parsing the title again.
		cand.Metadata = parser.FromResult(r.Torrent.Raw, r.Torrent.Data)
		cand.Group = cand.Metadata.ResolutionGroup()
		// The computed score replaces the old size/age/grabs heuristic, so
		// everything downstream ranks the same way. Every source of points —
		// NZB attributes, the library bonus, rules — has already contributed
		// and the profile has already ordered the list, so nothing re-sorts it
		// here: doing so used to discard the profile resolution precedence.
		cand.Score = r.Torrent.Rank
		out = append(out, cand)
	}

	logStreamFiltering(stream, filterMode, inputResults, len(out))
	logStreamSorting(stream, filterMode, inputResults, len(out))
	return out
}

// annotateVerdicts attaches the two attribute tiers that live outside the
// release itself: what ffprobe measured when a library item was last played,
// and what the community availability database reports for it.
//
// Availability is recorded per backbone rather than as one flag, because a
// release healthy on a backbone this stream does not use is not a release this
// stream can play.
func (s *Server) annotateVerdicts(candidates []triage.Candidate, source *playlistSource, stream *auth.Stream) {
	rt := s.runtime()
	var byDetailsURL map[string]*availnzb.ReleaseWithStatus
	if source != nil && source.Avail != nil {
		byDetailsURL = source.Avail.ByDetailsURL
	}
	var ourBackbones map[string]bool
	if rt.availClient != nil && len(byDetailsURL) > 0 {
		ourBackbones, _ = rt.availClient.OurBackbones(s.providerHostsForStream(stream))
	}

	for i := range candidates {
		rel := candidates[i].Release
		if rel == nil {
			continue
		}
		candidates[i].Verdict.Probed = libraryCapsForRelease(rel)
		candidates[i].Verdict.Avail = availStateFor(byDetailsURL[rel.DetailsURL], ourBackbones)
	}
}

// availStateFor turns one availability record into the tri-state the rest of
// the pipeline reads. A release nobody has reported stays unknown, which is a
// different claim from reported-bad and must not be conflated with it.
func availStateFor(status *availnzb.ReleaseWithStatus, ourBackbones map[string]bool) triage.AvailState {
	state := triage.AvailState{Status: triage.AvailUnknown}
	if status == nil {
		return state
	}
	state.Status = triage.AvailUnavailable
	if status.Available {
		state.Status = triage.AvailAvailable
	}
	state.Compression = status.CompressionType
	if len(status.Summary) == 0 {
		return state
	}
	state.Backbones = make(map[string]bool, len(status.Summary))
	for backbone, report := range status.Summary {
		state.Backbones[backbone] = report.Healthy
		if report.LastUpdated.After(state.CheckedAt) {
			state.CheckedAt = report.LastUpdated
		}
		if report.Healthy && ourBackbones[backbone] {
			state.OnMyBackbone = true
		}
	}
	return state
}

// rejectedForDiag converts the profile's turned-away releases into the diag
// shape: title, source indexer and jhin's reasons, nothing else.
func rejectedForDiag(rejected []ranking.Result) []diag.RejectedRelease {
	if len(rejected) == 0 {
		return nil
	}
	out := make([]diag.RejectedRelease, 0, len(rejected))
	for _, r := range rejected {
		rr := diag.RejectedRelease{Reasons: r.Torrent.Rejections}
		if r.Candidate.Release != nil {
			rr.Title = r.Candidate.Release.Title
			rr.Indexer = indexerNameFromRelease(r.Candidate.Release)
		}
		out = append(out, rr)
	}
	return out
}

// ensureCandidateMetadata parses any candidate that nothing else has, so
// stream descriptions always have metadata to render.
func ensureCandidateMetadata(candidates []triage.Candidate) {
	for i := range candidates {
		if candidates[i].Metadata != nil || candidates[i].Release == nil {
			continue
		}
		candidates[i].Metadata = parser.ParseReleaseTitle(candidates[i].Release.Title)
		candidates[i].Group = candidates[i].Metadata.ResolutionGroup()
	}
}

// rankingRequest classifies a request and gathers the media context a profile
// can read. A Kitsu request is anime by definition and carries its own
// film/episodic subtype; anything else falls back to what the metadata says,
// since most anime is browsed through ordinary catalogues.
//
// The anime classification rides on the request rather than being recomputed
// downstream: it used to be derived here, used for one profile lookup and
// discarded, which left custom result formats guessing at it from release
// names.
func rankingRequest(source *playlistSource) ranking.Request {
	contentType, kitsuShowType := "", ""
	isAnime := false
	req := ranking.Request{}
	if source != nil && source.Params != nil {
		contentType = source.Params.ContentType
		req.Season = atoiOrZero(source.Params.Req.Season)
		req.Episode = atoiOrZero(source.Params.Req.Episode)
		req.Title = source.Params.ContentTitle
		if meta := source.Params.Metadata; meta != nil {
			if meta.KitsuDetails != nil {
				isAnime = true
				kitsuShowType = meta.KitsuDetails.ShowType
			} else {
				isAnime = query.MetadataLooksLikeAnime(meta, contentType)
			}
		}
	}
	req.IsAnime = isAnime
	req.Kind = ranking.Kind(contentType, kitsuShowType, isAnime)
	return req
}

// logRankingSelection records how a request was classified, which profile that
// resolved to, and what the profile did to the result count. A nil profile
// means nothing was bound and the results passed through untouched.
func logRankingSelection(source *playlistSource, stream *auth.Stream, kind string, profile *ranking.Profile, inputResults, finalResults int) {
	title, contentType, id, kitsu := "", "", "", false
	if source != nil && source.Params != nil {
		title = source.Params.ContentTitle
		contentType = source.Params.ContentType
		id = source.Params.ID
		kitsu = source.Params.Metadata != nil && source.Params.Metadata.KitsuDetails != nil
	}
	name := "none"
	if profile != nil {
		name = profile.Name
	}
	streamName := ""
	if stream != nil {
		streamName = stream.Username
	}
	logger.Debug("Filter profile selected",
		"stream", streamName,
		"title", title,
		"id", id,
		"content_type", contentType,
		"kind", kind,
		"anime", kind == ranking.KindAnimeMovie || kind == ranking.KindAnimeShow,
		"kitsu", kitsu,
		"profile", name,
		"input_results", inputResults,
		"final_results", finalResults,
	)
}

// profileForKind resolves the filter profile for a request of this content
// kind, preferring a per-content-kind binding over the stream default.
//
// AIOStreams owns filtering and ordering for streams in that mode, so a bound
// profile is ignored rather than letting both systems shape one result set.
// The UI blocks the combination, but a config edited directly can still
// carry it.
func (s *Server) profileForKind(kind string, stream *auth.Stream) *ranking.Profile {
	if s == nil || s.rankingService == nil || stream == nil {
		return nil
	}
	if streamUsesAIOStreamsProfile(stream) {
		return nil
	}
	name := ranking.SelectName(stream.FilterProfileByType, stream.FilterProfileName, kind)
	if name == "" {
		return nil
	}
	profile, ok := s.rankingService.Get(name)
	if !ok {
		logger.Warn("Stream references an unknown filter profile",
			"stream", stream.Username,
			"profile", name,
		)
		return nil
	}
	return profile
}

func buildPlaylistResult(source *playlistSource, candidates []triage.Candidate) *playlistResult {
	firstIsAvailGood := false
	if len(candidates) > 0 && candidates[0].Release != nil && candidates[0].Release.DetailsURL != "" {
		firstIsAvailGood = source.CachedAvailable[candidates[0].Release.DetailsURL]
	}
	return &playlistResult{
		Candidates:             candidates,
		FirstIsAvailGood:       firstIsAvailGood,
		Params:                 source.Params,
		CachedAvailable:        source.CachedAvailable,
		UnavailableDetailsURLs: source.UnavailableDetailsURLs,
	}
}

func (s *Server) filterCachedUnhealthyCandidates(merged []triage.Candidate, availCtx *AvailContext, filteringActive bool, stream *auth.Stream) []triage.Candidate {
	rt := s.runtime()
	if !filteringActive || availCtx == nil || availCtx.Result == nil || rt.availClient == nil {
		return merged
	}
	ourBackbones, _ := rt.availClient.OurBackbones(s.providerHostsForStream(stream))
	cachedUnhealthyForUs := make(map[string]bool)
	for _, rws := range availCtx.Result.Releases {
		if rws == nil || rws.Release == nil || rws.Available {
			continue
		}
		if len(ourBackbones) > 0 && len(rws.Summary) > 0 {
			ourReported, ourHealthy := 0, 0
			for backbone, status := range rws.Summary {
				if ourBackbones[backbone] {
					ourReported++
					if status.Healthy {
						ourHealthy++
					}
				}
			}
			if ourReported > 0 && ourHealthy == 0 {
				cachedUnhealthyForUs[rws.Release.DetailsURL] = true
			}
		}
	}
	if len(cachedUnhealthyForUs) == 0 {
		return merged
	}
	filtered := merged[:0]
	for _, c := range merged {
		if c.Release == nil || !cachedUnhealthyForUs[c.Release.DetailsURL] {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

// streamLogName labels a stream in logs, falling back to "legacy" for
// requests that arrive without one.
func streamLogName(stream *auth.Stream) string {
	if stream != nil {
		return stream.Username
	}
	return "legacy"
}

func logStreamFiltering(stream *auth.Stream, filterMode string, inputResults, finalResults int) {
	logger.Debug("Stream filtering",
		"stream", streamLogName(stream),
		"mode", filterMode,
		"input_results", inputResults,
		"final_results", finalResults,
	)
}

func countCandidatesByUnavailableDetailsURL(candidates []triage.Candidate, unavailableDetailsURLs map[string]bool) int {
	if len(candidates) == 0 || len(unavailableDetailsURLs) == 0 {
		return 0
	}
	filtered := 0
	for _, c := range candidates {
		if c.Release == nil || c.Release.DetailsURL == "" {
			continue
		}
		if allCopiesUnavailable(c.Release, unavailableDetailsURLs) {
			filtered++
		}
	}
	return filtered
}

// allCopiesUnavailable reports whether every copy of a release is reported bad,
// which is what it takes for reported-bad filtering to drop the whole thing.
func allCopiesUnavailable(rel *release.Release, unavailableDetailsURLs map[string]bool) bool {
	for _, c := range rel.Copies() {
		if c == nil || c.DetailsURL == "" || !unavailableDetailsURLs[c.DetailsURL] {
			return false
		}
	}
	return true
}

func logAvailReportedBadFiltering(stream *auth.Stream, enabled bool, availFilteredOut, unavailableKnown int) {
	logger.Debug("AvailNZB reported-bad filtering",
		"stream", streamLogName(stream),
		"enabled", enabled,
		"avail_filtered_out", availFilteredOut,
		"known_unavailable", unavailableKnown,
	)
}

func sortCandidates(triageService *triage.Service, candidates []triage.Candidate) {
	triageService.SortCandidates(candidates)
}

func logStreamSorting(stream *auth.Stream, filterMode string, inputResults, finalResults int) {
	logger.Debug("Stream sorting",
		"stream", streamLogName(stream),
		"mode", filterMode,
		"input_results", inputResults,
		"final_results", finalResults,
	)
}

// atoiOrZero reads a numeric request field, yielding zero for the empty or
// non-numeric values a movie request carries.
func atoiOrZero(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
