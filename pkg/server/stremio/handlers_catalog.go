package stremio

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/services/metadata/kitsu"
	"streamnzb/pkg/services/metadata/tmdb"
)

const (
	catalogRequestTimeout = 15 * time.Second
	// catalogPageSize matches TMDB's fixed page size, so Stremio skip values
	// map 1:1 onto upstream pages.
	catalogPageSize = 20

	catalogListingCacheMaxAge = 60 * 60 // trending/popular: 1h
	catalogSearchCacheMaxAge  = 5 * 60  // search results: 5min
	// Continue Watching is personal and changes on every playback — never
	// client-cached.
	catalogLocalCacheMaxAge = 0

	// externalIDConcurrency caps the parallel TMDB external-id lookups per
	// catalog page (politeness bound; the response cache makes repeats free).
	externalIDConcurrency = 8
)

// catalogRequest is one parsed /catalog/... request. StreamName comes from
// the authenticated token, not the path: the local catalogs (Continue
// Watching, Because You Watched) are per-stream.
type catalogRequest struct {
	Type       string
	ID         string
	Search     string
	Skip       int
	StreamName string
}

// handleCatalog serves /catalog/{type}/{id}.json and
// /catalog/{type}/{id}/{extra}.json. Unknown, disabled, or master-switch-off
// catalogs are 404s; upstream failures degrade to an empty page so a flaky
// provider never renders as a client error row.
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()
	if cfg == nil || !cfg.EffectiveMetadataEnabled() {
		http.NotFound(w, r)
		return
	}

	req, ok := parseCatalogPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	stream, _ := auth.StreamFromContext(r)
	req.StreamName = streamID(stream)
	def, ok := catalogDefByID(req.ID)
	if !ok || def.Type != req.Type {
		http.NotFound(w, r)
		return
	}
	enabled := false
	for _, d := range enabledCatalogDefs(cfg) {
		if d.ID == def.ID {
			enabled = true
			break
		}
	}
	if !enabled {
		http.NotFound(w, r)
		return
	}
	if req.Search != "" && !def.SupportsSearch {
		http.NotFound(w, r)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), catalogRequestTimeout)
	defer cancel()

	metas, err := s.buildCatalog(ctx, def, req)
	if err != nil {
		logger.Debug("Catalog build failed; serving empty page",
			"catalog", def.ID, "search", req.Search, "skip", req.Skip, "err", err)
		metas = nil
	}
	if req.Search == "" && len(metas) > 0 {
		metas = filterHigherRankedDuplicates(metas, s.higherRankedCatalogIDs(ctx, cfg, def))
	}
	if metas == nil {
		metas = []MetaPreview{}
	}
	maxAge := catalogListingCacheMaxAge
	switch {
	case def.Provider == "local":
		maxAge = catalogLocalCacheMaxAge
	case req.Search != "":
		maxAge = catalogSearchCacheMaxAge
	}
	writeJSONCached(w, CatalogResponse{Metas: metas, CacheMaxAge: maxAge}, maxAge, 0)
}

// parseCatalogPath splits "/catalog/{type}/{id}.json" or
// "/catalog/{type}/{id}/{extra}.json", where extra is a URL-encoded query
// string ("search=dune", "skip=40").
func parseCatalogPath(path string) (catalogRequest, bool) {
	path = strings.TrimPrefix(path, "/catalog/")
	path = strings.TrimSuffix(path, ".json")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" {
		return catalogRequest{}, false
	}
	req := catalogRequest{Type: parts[0], ID: parts[1]}
	if len(parts) == 3 {
		extra, err := url.ParseQuery(parts[2])
		if err != nil {
			return catalogRequest{}, false
		}
		req.Search = strings.TrimSpace(extra.Get("search"))
		if skip, err := strconv.Atoi(extra.Get("skip")); err == nil && skip > 0 {
			req.Skip = skip
		}
	}
	return req, true
}

func (s *Server) buildCatalog(ctx context.Context, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	switch def.Provider {
	case "tmdb":
		return s.tmdbCatalog(ctx, def, req)
	case "tvdb":
		return s.tvdbCatalog(ctx, def, req)
	case "kitsu":
		return s.kitsuCatalog(ctx, def, req)
	case "local":
		if def.Kind == "because-you-watched" {
			return s.becauseYouWatchedCatalog(ctx, def, req)
		}
		return s.continueWatchingCatalog(ctx, def, req)
	}
	return nil, fmt.Errorf("unknown catalog provider %q", def.Provider)
}

func (s *Server) tmdbCatalog(_ context.Context, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	mediaType := "tv"
	if def.Type == "movie" {
		mediaType = "movie"
	}
	page := req.Skip/catalogPageSize + 1

	var resp *tmdb.ListingResponse
	var err error
	if req.Search != "" {
		resp, err = s.tmdbClient.SearchByType(mediaType, req.Search, page)
	} else {
		resp, err = s.tmdbClient.GetListing(mediaType, def.Kind, page)
	}
	if err != nil {
		return nil, err
	}

	previews := make([]MetaPreview, 0, len(resp.Results))
	ids := s.resolveIMDbIDs(mediaType, resp.Results)
	for i, res := range resp.Results {
		name := res.Title
		if name == "" {
			name = res.Name
		}
		if name == "" {
			continue
		}
		// tt ids keep catalog rows playable by any other stream addon the
		// user has installed; tmdb: is the fallback our own handlers accept.
		id := ids[i]
		if id == "" {
			id = fmt.Sprintf("tmdb:%d", res.ID)
		}
		preview := MetaPreview{ID: id, Type: def.Type, Name: name, Description: res.Overview}
		if res.PosterPath != "" {
			preview.Poster = tmdbPosterURL + res.PosterPath
		}
		previews = append(previews, preview)
	}
	return previews, nil
}

// resolveIMDbIDs fans out external-id lookups for one catalog page, bounded to
// externalIDConcurrency in flight. Failures leave the slot empty — the caller
// falls back to a tmdb: id.
func (s *Server) resolveIMDbIDs(mediaType string, results []tmdb.SearchMultiResult) []string {
	ids := make([]string, len(results))
	sem := make(chan struct{}, externalIDConcurrency)
	var wg sync.WaitGroup
	for i, res := range results {
		if res.ID <= 0 {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i, tmdbID int) {
			defer wg.Done()
			defer func() { <-sem }()
			ext, err := s.tmdbClient.GetExternalIDs(tmdbID, mediaType)
			if err == nil && strings.HasPrefix(ext.IMDbID, "tt") {
				ids[i] = ext.IMDbID
			}
		}(i, res.ID)
	}
	wg.Wait()
	return ids
}

// tvdbCatalog serves the TVDB filter listings. One upstream page holds
// hundreds of rows, so board pages slice into the cached first page. tt ids
// resolve through each row's extended record (bounded fan-out, cached, and
// reused later by the series meta pages those rows open).
func (s *Server) tvdbCatalog(_ context.Context, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	sort := "score"
	if def.Kind == "new" {
		sort = "firstAired"
	}
	listings, err := s.tvdbClient.FilterSeries(sort, 0)
	if err != nil {
		return nil, err
	}
	if req.Skip >= len(listings) {
		return nil, nil
	}
	listings = listings[req.Skip:]
	if len(listings) > catalogPageSize {
		listings = listings[:catalogPageSize]
	}

	ids := make([]string, len(listings))
	sem := make(chan struct{}, externalIDConcurrency)
	var wg sync.WaitGroup
	for i, listing := range listings {
		if listing.ID <= 0 {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i, tvdbID int) {
			defer wg.Done()
			defer func() { <-sem }()
			if ext, err := s.tvdbClient.GetSeriesExtended(strconv.Itoa(tvdbID)); err == nil {
				ids[i] = ext.IMDbID()
			}
		}(i, listing.ID)
	}
	wg.Wait()

	previews := make([]MetaPreview, 0, len(listings))
	for i, listing := range listings {
		if listing.Name == "" || listing.ID <= 0 {
			continue
		}
		id := ids[i]
		if id == "" {
			id = fmt.Sprintf("tvdb:%d", listing.ID)
		}
		preview := MetaPreview{ID: id, Type: def.Type, Name: listing.Name, Description: listing.Overview}
		if listing.Image != "" {
			preview.Poster = listing.Image
			if !strings.HasPrefix(preview.Poster, "http") {
				preview.Poster = "https://artworks.thetvdb.com" + preview.Poster
			}
		}
		previews = append(previews, preview)
	}
	return previews, nil
}

func (s *Server) kitsuCatalog(ctx context.Context, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	var listings []kitsu.AnimeListing
	var err error
	if req.Search != "" {
		listings, err = s.kitsuClient.SearchAnime(ctx, req.Search, req.Skip)
	} else {
		listings, err = s.kitsuClient.GetAnimeListing(ctx, def.Kind, req.Skip)
	}
	if err != nil {
		return nil, err
	}
	previews := make([]MetaPreview, 0, len(listings))
	for _, item := range listings {
		if item.ID == "" || item.CanonicalTitle == "" {
			continue
		}
		previews = append(previews, MetaPreview{
			ID:          "kitsu:" + item.ID,
			Type:        "anime",
			Name:        item.CanonicalTitle,
			Poster:      item.PosterImage,
			Description: item.Synopsis,
		})
	}
	return previews, nil
}

// baseContentID collapses an attempt's request id ("tt123:1:5",
// "kitsu:486:3", bare TMDB numbers) to movie/series granularity in preview-id
// form, or "" when unrecognizable.
func baseContentID(rawID string) string {
	id := strings.TrimSpace(rawID)
	if id == "" {
		return ""
	}
	parts := strings.Split(id, ":")
	switch {
	case strings.HasPrefix(id, "kitsu:"), strings.HasPrefix(id, "tmdb:"), strings.HasPrefix(id, "tvdb:"):
		if len(parts) >= 2 && parts[1] != "" {
			return parts[0] + ":" + parts[1]
		}
		return ""
	case strings.HasPrefix(id, "tt"):
		return parts[0]
	default:
		if n, err := strconv.Atoi(parts[0]); err == nil && n > 0 {
			return "tmdb:" + parts[0]
		}
	}
	return ""
}

// recentPlayedPreviews returns the distinct titles one stream successfully
// played, newest first, as preview stubs (id + attempt title as the fallback
// name). Episodes collapse to their series.
func (s *Server) recentPlayedPreviews(streamName, contentType string) []MetaPreview {
	if s.attemptRecorder == nil {
		return nil
	}
	rows, err := s.attemptRecorder.RecentPlayedContent(streamName, contentType, 300)
	if err != nil || len(rows) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(rows))
	var previews []MetaPreview
	for _, row := range rows {
		id := baseContentID(row.ContentID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		previews = append(previews, MetaPreview{ID: id, Type: contentType, Name: row.ContentTitle})
	}
	return previews
}

// continueWatchingCatalog builds the local catalog from what the requesting
// stream actually played (per-stream resolution), falling back to the shared
// library for installs with no playback history yet.
func (s *Server) continueWatchingCatalog(ctx context.Context, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	if played := s.recentPlayedPreviews(req.StreamName, def.Type); len(played) > 0 {
		if req.Skip >= len(played) {
			return nil, nil
		}
		played = played[req.Skip:]
		if len(played) > catalogPageSize {
			played = played[:catalogPageSize]
		}
		previews := make([]MetaPreview, 0, len(played))
		for _, stub := range played {
			preview := stub
			s.fillPreviewFromMetadata(ctx, &preview, def.Type)
			if preview.Name == "" {
				continue
			}
			previews = append(previews, preview)
		}
		return previews, nil
	}
	return s.libraryCatalog(ctx, def, req)
}

// libraryCatalog is the shared-library fallback: most recently accessed
// first, one row per content id, no stream attribution.
func (s *Server) libraryCatalog(ctx context.Context, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	if s.attemptRecorder == nil || s.attemptRecorder.LibraryStore() == nil {
		return nil, nil
	}
	items, _, err := s.attemptRecorder.LibraryStore().GetFilteredItems("", def.Type, false, "good", req.Skip, 2*catalogPageSize)
	if err != nil {
		return nil, err
	}
	previews := make([]MetaPreview, 0, len(items))
	seen := make(map[string]bool)
	for _, item := range items {
		if item == nil {
			continue
		}
		id := libraryPreviewID(item)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		preview := MetaPreview{ID: id, Type: def.Type}
		s.fillPreviewFromMetadata(ctx, &preview, item.ContentType)
		if preview.Name == "" {
			preview.Name = item.ReleaseTitle
		}
		previews = append(previews, preview)
		if len(previews) >= catalogPageSize {
			break
		}
	}
	return previews, nil
}

// becauseYouWatchedSeeds caps how many recent titles seed the recommendation
// row; each costs one (cached) TMDB recommendations request.
const becauseYouWatchedSeeds = 3

// watchHistory returns the requesting stream's watched preview ids in recency
// order (per-stream playback history first, shared library as the fallback
// ordering), plus a membership set that always folds the library in — a
// recommendation row must not re-offer what is already at hand.
func (s *Server) watchHistory(streamName, contentType string) (ordered []string, watched map[string]bool) {
	watched = make(map[string]bool)
	for _, stub := range s.recentPlayedPreviews(streamName, contentType) {
		if !watched[stub.ID] {
			watched[stub.ID] = true
			ordered = append(ordered, stub.ID)
		}
	}
	playedCount := len(ordered)
	if s.attemptRecorder != nil && s.attemptRecorder.LibraryStore() != nil {
		items, _, _ := s.attemptRecorder.LibraryStore().GetFilteredItems("", contentType, false, "good", 0, 100)
		for _, item := range items {
			if item == nil {
				continue
			}
			id := libraryPreviewID(item)
			if id == "" || watched[id] {
				continue
			}
			watched[id] = true
			// Library rows only order the row when the stream has no
			// playback history of its own.
			if playedCount == 0 {
				ordered = append(ordered, id)
			}
		}
	}
	return ordered, watched
}

// becauseYouWatchedCatalog builds a recommendation row from the requesting
// stream's most recent watches: TMDB's recommendations for the last few
// distinct titles, interleaved so no single seed dominates, minus everything
// already watched or in the library.
func (s *Server) becauseYouWatchedCatalog(ctx context.Context, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	ordered, watched := s.watchHistory(req.StreamName, def.Type)
	if len(ordered) == 0 {
		return nil, nil
	}
	mediaType := "tv"
	if def.Type == "movie" {
		mediaType = "movie"
	}

	var seedTMDBIDs []int
	seenSeeds := make(map[int]bool)
	for _, id := range ordered {
		if len(seedTMDBIDs) >= becauseYouWatchedSeeds {
			break
		}
		if tmdbID := s.tmdbIDForPreviewID(id, def.Type); tmdbID > 0 && !seenSeeds[tmdbID] {
			seenSeeds[tmdbID] = true
			seedTMDBIDs = append(seedTMDBIDs, tmdbID)
		}
	}
	if len(seedTMDBIDs) == 0 {
		return nil, nil
	}

	// One recommendation page per seed, interleaved round-robin so the row
	// mixes tastes instead of leading with 20 titles like the newest watch.
	perSeed := make([][]tmdb.SearchMultiResult, 0, len(seedTMDBIDs))
	for _, seedID := range seedTMDBIDs {
		resp, err := s.tmdbClient.GetRecommendations(mediaType, seedID, 1)
		if err != nil {
			logger.Debug("TMDB recommendations failed", "tmdb_id", seedID, "err", err)
			continue
		}
		perSeed = append(perSeed, resp.Results)
	}

	var merged []tmdb.SearchMultiResult
	seenTMDB := make(map[int]bool)
	for round := 0; ; round++ {
		advanced := false
		for _, results := range perSeed {
			if round >= len(results) {
				continue
			}
			advanced = true
			res := results[round]
			if res.ID <= 0 || seenTMDB[res.ID] || seenSeeds[res.ID] {
				continue
			}
			seenTMDB[res.ID] = true
			merged = append(merged, res)
		}
		if !advanced {
			break
		}
	}

	ids := s.resolveIMDbIDs(mediaType, merged)
	previews := make([]MetaPreview, 0, len(merged))
	for i, res := range merged {
		name := res.Title
		if name == "" {
			name = res.Name
		}
		if name == "" {
			continue
		}
		id := ids[i]
		if id == "" {
			id = fmt.Sprintf("tmdb:%d", res.ID)
		}
		if watched[id] {
			continue
		}
		preview := MetaPreview{ID: id, Type: def.Type, Name: name, Description: res.Overview}
		if res.PosterPath != "" {
			preview.Poster = tmdbPosterURL + res.PosterPath
		}
		previews = append(previews, preview)
	}

	// Skip/limit over the merged list: at three seeds the pool is ~60 titles.
	if req.Skip >= len(previews) {
		return nil, nil
	}
	previews = previews[req.Skip:]
	if len(previews) > catalogPageSize {
		previews = previews[:catalogPageSize]
	}
	return previews, nil
}

// tmdbIDForPreviewID resolves a catalog preview id back to a TMDB id, through
// the cached Find lookup for tt ids.
func (s *Server) tmdbIDForPreviewID(previewID, contentType string) int {
	if raw, ok := strings.CutPrefix(previewID, "tmdb:"); ok {
		id, _ := strconv.Atoi(raw)
		return id
	}
	if strings.HasPrefix(previewID, "tt") {
		if find, err := s.tmdbClient.Find(previewID, "imdb_id"); err == nil {
			if res, ok := pickFindResult(find, contentType); ok {
				return res.ID
			}
		}
	}
	return 0
}

// higherRankedCatalogIDs collects the first-page preview ids of every enabled
// catalog of the same type ranked above current, so a title appears only in
// the highest-ranked row that carries it. Best-effort: a failing higher
// catalog just contributes nothing.
func (s *Server) higherRankedCatalogIDs(ctx context.Context, cfg *config.Config, current CatalogDef) map[string]bool {
	ids := make(map[string]bool)
	for _, def := range enabledCatalogDefs(cfg) {
		if def.ID == current.ID {
			break
		}
		if def.Type != current.Type {
			continue
		}
		metas, err := s.buildCatalog(ctx, def, catalogRequest{Type: def.Type, ID: def.ID})
		if err != nil {
			continue
		}
		for _, preview := range metas {
			ids[preview.ID] = true
		}
	}
	return ids
}

// filterHigherRankedDuplicates drops previews already shown by a higher-ranked
// catalog on the client's board.
func filterHigherRankedDuplicates(metas []MetaPreview, higher map[string]bool) []MetaPreview {
	if len(higher) == 0 {
		return metas
	}
	filtered := metas[:0]
	for _, preview := range metas {
		if !higher[preview.ID] {
			filtered = append(filtered, preview)
		}
	}
	return filtered
}

// libraryPreviewID picks the catalog id for a library row: tt first so any
// installed addon can serve it, then the scheme-prefixed fallbacks.
func libraryPreviewID(item *persistence.LibraryItem) string {
	switch {
	case strings.HasPrefix(item.ImdbID, "tt"):
		return item.ImdbID
	case item.TmdbID != "":
		return "tmdb:" + item.TmdbID
	case item.KitsuID != "":
		return "kitsu:" + item.KitsuID
	case strings.HasPrefix(item.ContentID, "tt"):
		return item.ContentID
	}
	return ""
}

// fillPreviewFromMetadata resolves display name and poster for a preview stub
// through the cached metadata clients, keyed off the preview id. A stub whose
// resolution fails keeps whatever name it already carries.
func (s *Server) fillPreviewFromMetadata(ctx context.Context, preview *MetaPreview, contentType string) {
	if kitsuID, ok := strings.CutPrefix(preview.ID, "kitsu:"); ok {
		if animeMeta, err := s.kitsuClient.GetAnimeMeta(ctx, kitsuID); err == nil && animeMeta.CanonicalTitle != "" {
			preview.Name = animeMeta.CanonicalTitle
			preview.Poster = animeMeta.PosterImage
		}
		return
	}
	if tvdbID, ok := strings.CutPrefix(preview.ID, "tvdb:"); ok {
		if ext, err := s.tvdbClient.GetSeriesExtended(tvdbID); err == nil && ext.Name != "" {
			preview.Name = ext.Name
			preview.Poster = ext.Image
		}
		return
	}
	tmdbID := 0
	if raw, ok := strings.CutPrefix(preview.ID, "tmdb:"); ok {
		tmdbID, _ = strconv.Atoi(raw)
	} else if strings.HasPrefix(preview.ID, "tt") {
		if find, err := s.tmdbClient.Find(preview.ID, "imdb_id"); err == nil {
			if res, ok := pickFindResult(find, contentType); ok {
				tmdbID = res.ID
			}
		}
	}
	if tmdbID == 0 {
		return
	}
	if contentType == "movie" {
		if details, err := s.tmdbClient.GetMovieDetails(tmdbID); err == nil && details.Title != "" {
			preview.Name = details.Title
			if details.PosterPath != "" {
				preview.Poster = tmdbPosterURL + details.PosterPath
			}
		}
		return
	}
	if details, err := s.tmdbClient.GetTVDetails(tmdbID); err == nil && details.Name != "" {
		preview.Name = details.Name
		if details.PosterPath != "" {
			preview.Poster = tmdbPosterURL + details.PosterPath
		}
	}
}
