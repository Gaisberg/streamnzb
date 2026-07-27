package stremio

import (
	"context"
	"strings"
	"time"

	"github.com/dreulavelle/jhin/rank"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/parser"
	"streamnzb/pkg/search/ranking"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/services/metadata/tmdb"
)

type namedIndexer interface {
	Name() string
}

func indexerNameFromRelease(rel *release.Release) string {
	if rel == nil {
		return ""
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
	Params           *SearchParams

	CachedAvailable map[string]bool

	// UnavailableDetailsURLs is the set of release DetailsURLs known to be unavailable (AvailNZB false).
	// For AIOStreams we filter these out so we only return unknown or available (true).
	UnavailableDetailsURLs map[string]bool

	// SlotPaths, when set, gives the exact play path for each candidate (e.g. from failover order).
	// Must match len(Candidates); buildStreamsFromPlaylist uses SlotPaths[i] instead of key.SlotPath(i).
	SlotPaths []string
}

type AvailContext struct {
	Result                  *availnzb.ReleasesResult
	InputResults            int
	ByDetailsURL            map[string]*availnzb.ReleaseWithStatus
	AvailableByDetailsURL   map[string]bool
	UnavailableByDetailsURL map[string]bool
}

type rawSearchResult struct {
	Params          *SearchParams
	IndexerReleases []*release.Release
	Avail           *AvailContext
}

type playlistSource struct {
	Params                 *SearchParams
	Releases               []*release.Release
	Avail                  *AvailContext
	CachedAvailable        map[string]bool
	UnavailableDetailsURLs map[string]bool
}

const playlistCacheTTL = 10 * time.Minute

type playlistCacheEntry struct {
	result *playlistResult
	until  time.Time
}

type rawSearchCacheEntry struct {
	raw   *rawSearchResult
	until time.Time
}

func streamProviderSelections(stream *auth.Stream) []string {
	if stream == nil {
		return nil
	}
	return append([]string(nil), stream.ProviderSelections...)
}

// filterPlaylistByOrder keeps only candidates whose slot path appears in order (same key, valid index), in that order.
// SlotPaths on the result are set from order so stream URLs match the client. Non-slot-path entries are ignored.
func filterPlaylistByOrder(list *playlistResult, key StreamSlotKey, order []string) *playlistResult {
	if list == nil || len(order) == 0 {
		return list
	}
	maxIndex := len(list.Candidates) - 1
	var filtered []triage.Candidate
	var paths []string
	for _, entry := range order {
		if !strings.HasPrefix(entry, streamSlotPrefix) {
			continue
		}
		sid, ct, id, idx, ok := parseStreamSlotID(entry)
		if !ok || idx < 0 || idx > maxIndex {
			continue
		}
		if ct != key.ContentType || id != key.ID {
			continue
		}
		if sid != "" && sid != key.StreamID {
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
	var filtered []triage.Candidate
	for _, c := range list.Candidates {
		if c.Release == nil || c.Release.DetailsURL == "" {
			filtered = append(filtered, c)
			continue
		}
		if list.UnavailableDetailsURLs[c.Release.DetailsURL] {
			continue
		}
		filtered = append(filtered, c)
	}
	if len(filtered) == len(list.Candidates) {
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
		SlotPaths:              nil,
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
			return ent.result, nil
		}
	}
	logger.Debug("Playback playlist cache miss", "key", cacheKey)
	list, err := s.buildPlaylistUncached(ctx, key, isAIOStreams, stream)
	if err != nil || list == nil {
		return list, err
	}
	s.playlistCache.Store(cacheKey, &playlistCacheEntry{result: list, until: time.Now().Add(playlistCacheTTL)})
	return list, nil
}

func (s *Server) buildPlaylistUncached(ctx context.Context, key StreamSlotKey, isAIOStreams bool, stream *auth.Stream) (*playlistResult, error) {
	raw, err := s.getOrBuildRawSearchResult(ctx, key.ContentType, key.ID, stream)
	if err != nil || raw == nil {
		return nil, err
	}
	return s.buildPlaylistFromRaw(raw, isAIOStreams, stream)
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
			return cloneRawSearchResult(ent.raw), nil
		}
	}
	logger.Debug("Playback candidate cache miss", "key", rawKey)
	raw, err := s.buildRawSearchResult(ctx, contentType, id, stream)
	if err != nil || raw == nil {
		return nil, err
	}
	s.rawSearchCache.Store(rawKey, &rawSearchCacheEntry{raw: raw, until: time.Now().Add(playlistCacheTTL)})
	return cloneRawSearchResult(raw), nil
}

func (s *Server) GetSearchReleases(ctx context.Context, contentType, id string) (*SearchReleasesResponse, error) {
	fallbackStream := &auth.Stream{Username: defaultStreamID}
	if contentType == "movie" {
		fallbackStream.MovieSearchQueries = allSearchQueryNames(s.config.MovieSearchQueries)
	} else {
		fallbackStream.SeriesSearchQueries = allSearchQueryNames(s.config.SeriesSearchQueries)
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
	if s.sessionManager != nil {
		if hosts := s.sessionManager.ProviderHostsForProviders(streamProviderSelections(stream)); len(hosts) > 0 {
			return hosts
		}
	}
	if s.validator != nil {
		return s.validator.GetProviderHosts()
	}
	return nil
}

func (s *Server) segmentFetcherForStream(stream *auth.Stream) loader.SegmentFetcher {
	if s.sessionManager == nil {
		return nil
	}
	return s.sessionManager.SegmentFetcherForProviders(streamProviderSelections(stream))
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

func (s *Server) buildPlaylistFromRaw(raw *rawSearchResult, isAIOStreams bool, stream *auth.Stream) (*playlistResult, error) {
	filterMode, filteringActive := resolveFilterMode(stream)
	source := buildPlaylistSource(raw, filteringActive)
	inputCandidates := buildPlaylistCandidates(source)
	candidates := s.applyPlaylistFiltering(inputCandidates, source, isAIOStreams, filteringActive, filterMode, stream)
	candidates = s.applyRanking(candidates, source, filteringActive, filterMode, stream)
	s.recordAvailIndexerStats(inputCandidates, candidates, source, filteringActive)
	return buildPlaylistResult(source, candidates), nil
}

func (s *Server) shouldFilterAvailNZBReportedBad() bool {
	if s == nil || s.config == nil {
		return true
	}
	return s.config.EffectiveAvailNZBFilterReportedBad()
}

func (s *Server) recordAvailIndexerStats(inputCandidates, finalCandidates []triage.Candidate, source *playlistSource, filteringActive bool) {
	if source == nil {
		return
	}
	availableByIndexer := make(map[string]int)
	discardedByIndexer := make(map[string]int)

	if s.shouldFilterAvailNZBReportedBad() && len(source.UnavailableDetailsURLs) > 0 {
		for _, c := range inputCandidates {
			if c.Release == nil || c.Release.DetailsURL == "" {
				continue
			}
			if !source.UnavailableDetailsURLs[c.Release.DetailsURL] {
				continue
			}
			if name := indexerNameFromRelease(c.Release); name != "" {
				discardedByIndexer[name]++
			}
		}
	}

	if len(source.CachedAvailable) > 0 {
		for _, c := range finalCandidates {
			if c.Release == nil || c.Release.DetailsURL == "" {
				continue
			}
			if !source.CachedAvailable[c.Release.DetailsURL] {
				continue
			}
			if name := indexerNameFromRelease(c.Release); name != "" {
				availableByIndexer[name]++
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
	if rel == nil {
		return nil
	}
	next := *rel
	if rel.Languages != nil {
		next.Languages = append([]string(nil), rel.Languages...)
	}
	if rel.Available != nil {
		available := *rel.Available
		next.Available = &available
	}
	return &next
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
		Params: cloneSearchParams(raw.Params),
		Avail:  cloneAvailContext(raw.Avail),
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
		if rel == nil || rel.DetailsURL != detailsURL {
			continue
		}
		if rel.Available == nil || *rel.Available {
			rel.Available = &availFalse
			changed = true
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
		candidateDetailsURL := ""
		if candidate.Release != nil {
			candidateDetailsURL = candidate.Release.DetailsURL
		}
		if (detailsURL != "" && candidateDetailsURL == detailsURL) || (slotPath != "" && paths[i] == slotPath) {
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
				s.playlistCache.Store(cacheKey, &playlistCacheEntry{result: nextResult, until: ent.until})
				updated = true
			}
		}
	}

	rawKey := key.StreamID + ":" + key.ContentType + ":" + key.ID
	if v, ok := s.rawSearchCache.Load(rawKey); ok {
		if ent, _ := v.(*rawSearchCacheEntry); ent != nil && time.Now().Before(ent.until) && ent.raw != nil {
			nextRaw := cloneRawSearchResult(ent.raw)
			if markRawSearchResultUnavailable(nextRaw, detailsURL) {
				s.rawSearchCache.Store(rawKey, &rawSearchCacheEntry{raw: nextRaw, until: ent.until})
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
	if len(unavailableDetailsURLs) == 0 && !filteringActive {
		return merged
	}
	var seenTitle map[string]bool
	if isAIOStreams {
		seenTitle = make(map[string]bool)
	}
	// Build a new slice instead of compacting in-place, so callers that keep a
	// reference to the original candidate list (for metrics accounting) are not
	// accidentally mutated by filtering.
	filtered := make([]triage.Candidate, 0, len(merged))
	for _, c := range merged {
		if c.Release == nil {
			continue
		}
		if c.Release.DetailsURL != "" && unavailableDetailsURLs[c.Release.DetailsURL] {
			continue
		}
		if seenTitle != nil && c.Release.Title != "" {
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
	availReportedBadFilteringEnabled := s.shouldFilterAvailNZBReportedBad()
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
func (s *Server) applyRanking(candidates []triage.Candidate, source *playlistSource, filteringActive bool, filterMode string, stream *auth.Stream) []triage.Candidate {
	profile := s.profileForRequest(source, stream)
	if profile == nil {
		if filteringActive {
			sortCandidates(s.triageService, candidates)
		}
		// Unconditional: scoring happens to populate metadata today, but
		// stream descriptions need it whatever that path does.
		ensureCandidateMetadata(candidates)
		logStreamSorting(stream, filterMode, len(candidates), len(candidates))
		return candidates
	}

	inputResults := len(candidates)
	results := profile.Apply(candidates, rank.RankOptions{})
	logger.Debug("Filter profile applied",
		"stream", stream.Username,
		"kind", kindForRequest(source),
		"profile", profile.Name,
		"input_results", inputResults,
		"final_results", len(results),
	)

	out := make([]triage.Candidate, 0, len(results))
	for _, r := range results {
		cand := r.Candidate
		// Reuse the ranker's parse instead of parsing the title again.
		cand.Metadata = parser.FromResult(r.Torrent.Raw, r.Torrent.Data)
		cand.Group = cand.Metadata.ResolutionGroup()
		// The computed score replaces the old size/age/grabs heuristic, so
		// everything downstream ranks the same way.
		cand.Score = r.Torrent.Rank
		out = append(out, cand)
	}

	logStreamFiltering(stream, filterMode, inputResults, len(out))
	logStreamSorting(stream, filterMode, inputResults, len(out))
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

// kindForRequest classifies a request. A Kitsu request is anime by definition
// and carries its own film/episodic subtype; anything else falls back to what
// the metadata says, since most anime is browsed through ordinary catalogues.
func kindForRequest(source *playlistSource) string {
	contentType, kitsuShowType := "", ""
	isAnime := false
	if source != nil && source.Params != nil {
		contentType = source.Params.ContentType
		if meta := source.Params.Metadata; meta != nil {
			if meta.KitsuDetails != nil {
				isAnime = true
				kitsuShowType = meta.KitsuDetails.ShowType
			} else {
				isAnime = metadataLooksLikeAnime(meta, contentType)
			}
		}
	}
	return ranking.Kind(contentType, kitsuShowType, isAnime)
}

// metadataLooksLikeAnime treats anime as animation that is not originally in
// English: an explicit anime genre, or an animation genre on something made in
// another language. Without metadata it reports false, so a request is only
// ever promoted to anime on evidence.
func metadataLooksLikeAnime(meta *resolvedSearchMetadata, contentType string) bool {
	if meta == nil {
		return false
	}
	var genres []tmdb.Genre
	var originalLanguage string
	if strings.EqualFold(strings.TrimSpace(contentType), "movie") {
		if meta.MovieDetails == nil {
			return false
		}
		genres, originalLanguage = meta.MovieDetails.Genres, meta.MovieDetails.OriginalLanguage
	} else {
		if meta.TVDetails == nil {
			return false
		}
		genres, originalLanguage = meta.TVDetails.Genres, meta.TVDetails.OriginalLanguage
	}

	animation := false
	for _, genre := range genres {
		switch strings.ToLower(strings.TrimSpace(genre.Name)) {
		case "anime":
			return true
		case "animation":
			animation = true
		}
	}
	// TMDB reports ISO 639-1, so English is "en".
	return animation && !strings.EqualFold(strings.TrimSpace(originalLanguage), "en")
}

// profileForRequest resolves the filter profile for this request, preferring a
// per-content-kind binding over the stream default.
func (s *Server) profileForRequest(source *playlistSource, stream *auth.Stream) *ranking.Profile {
	if s == nil || s.rankingService == nil || stream == nil {
		return nil
	}
	kind := kindForRequest(source)
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
	if !filteringActive || availCtx == nil || availCtx.Result == nil || s.availClient == nil {
		return merged
	}
	ourBackbones, _ := s.availClient.OurBackbones(s.providerHostsForStream(stream))
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

func logStreamFiltering(stream *auth.Stream, filterMode string, inputResults, finalResults int) {
	logger.Debug("Stream filtering",
		"stream", func() string {
			if stream != nil {
				return stream.Username
			}
			return "legacy"
		}(),
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
		if unavailableDetailsURLs[c.Release.DetailsURL] {
			filtered++
		}
	}
	return filtered
}

func logAvailReportedBadFiltering(stream *auth.Stream, enabled bool, availFilteredOut, unavailableKnown int) {
	logger.Debug("AvailNZB reported-bad filtering",
		"stream", func() string {
			if stream != nil {
				return stream.Username
			}
			return "legacy"
		}(),
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
		"stream", func() string {
			if stream != nil {
				return stream.Username
			}
			return "legacy"
		}(),
		"mode", filterMode,
		"input_results", inputResults,
		"final_results", finalResults,
	)
}
