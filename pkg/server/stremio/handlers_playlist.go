package stremio

import (
	"context"
	"sort"
	"strings"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/services/availnzb"
)

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

	releasesOnly := make([]*release.Release, 0, len(unified))
	for _, u := range unified {
		releasesOnly = append(releasesOnly, u.rel)
	}
	candidates := s.triageService.Filter(releasesOnly)
	releaseScores := make(map[string]struct {
		Fits  bool
		Score int
	}, len(candidates))
	for _, c := range candidates {
		if c.Release == nil {
			continue
		}
		releaseScores[release.Key(c.Release)] = struct {
			Fits  bool
			Score int
		}{Fits: true, Score: c.Score}
	}

	streamInfos := []SearchStreamInfo{{ID: defaultStreamID, Name: "StreamNZB"}}

	sort.Slice(unified, func(i, j int) bool {
		si := releaseScores[release.Key(unified[i].rel)].Score
		sj := releaseScores[release.Key(unified[j].rel)].Score
		if si != sj {
			return si > sj
		}
		availOrder := map[string]int{"Available": 2, "Unknown": 1, "Unavailable": 0}
		return availOrder[unified[i].avail] > availOrder[unified[j].avail]
	})

	releasesOut := make([]SearchReleaseTag, 0, len(unified))
	for _, u := range unified {
		r := u.rel
		key := release.Key(r)
		ts := releaseScores[key]
		tags := []SearchStreamTag{{
			StreamID:   defaultStreamID,
			StreamName: "StreamNZB",
			Fits:       ts.Fits,
			Score:      ts.Score,
		}}
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
			StreamTags:   tags,
		})
	}

	return &SearchReleasesResponse{Streams: streamInfos, Releases: releasesOut}, nil
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
		UnavailableDetailsURLs: buildUnavailableDetailsURLs(raw.Avail, filteringActive),
	}
}

func releasesToCandidates(releases []*release.Release) []triage.Candidate {
	var out []triage.Candidate
	for _, rel := range releases {
		if rel == nil {
			continue
		}
		out = append(out, triage.Candidate{Release: rel, Score: 0})
	}
	return out
}

func (s *Server) buildPlaylistFromRaw(raw *rawSearchResult, isAIOStreams bool, stream *auth.Stream) (*playlistResult, error) {
	filterMode, filteringActive := resolveFilterMode(stream)
	source := buildPlaylistSource(raw, filteringActive)
	candidates := buildPlaylistCandidates(source)
	candidates = s.applyPlaylistFiltering(candidates, source, isAIOStreams, filteringActive, filterMode, stream)
	candidates = applyPlaylistSorting(candidates, s.triageService, filteringActive, filterMode, stream)
	return buildPlaylistResult(source, candidates), nil
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

func buildUnavailableDetailsURLs(availCtx *AvailContext, filteringActive bool) map[string]bool {
	out := make(map[string]bool)
	if !filteringActive || availCtx == nil {
		return out
	}
	for detailsURL := range availCtx.UnavailableByDetailsURL {
		out[detailsURL] = true
	}
	return out
}

func filterCandidates(merged []triage.Candidate, isAIOStreams, filteringActive bool, unavailableDetailsURLs map[string]bool) []triage.Candidate {
	if !filteringActive {
		return merged
	}
	var seenTitle map[string]bool
	if isAIOStreams {
		seenTitle = make(map[string]bool)
	}
	filtered := merged[:0]
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
	if !filteringActive {
		return candidates
	}
	inputResults := len(candidates)
	candidates = filterCandidates(candidates, isAIOStreams, filteringActive, source.UnavailableDetailsURLs)
	candidates = s.filterCachedUnhealthyCandidates(candidates, source.Avail, filteringActive, stream)
	logStreamFiltering(stream, filterMode, inputResults, len(candidates))
	return candidates
}

func applyPlaylistSorting(candidates []triage.Candidate, triageService *triage.Service, filteringActive bool, filterMode string, stream *auth.Stream) []triage.Candidate {
	if !filteringActive {
		return candidates
	}
	inputResults := len(candidates)
	sortCandidates(triageService, candidates)
	logStreamSorting(stream, filterMode, inputResults, len(candidates))
	return candidates
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
