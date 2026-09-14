package stremio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/httpx"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/search/query"
	"streamnzb/pkg/services/metadata/certification"
	"streamnzb/pkg/services/metadata/kitsu"
	"streamnzb/pkg/services/metadata/simkl"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
	"streamnzb/pkg/services/metadata/tvmaze"
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
	// externalInspectionConcurrency bounds the independent browse probes made
	// when someone tests a pasted manifest in the configuration UI.
	externalInspectionConcurrency = 4
	// externalInspectionMaxCatalogs caps how many catalog rows one pasted
	// manifest can queue for a live probe. Nothing else in InspectExternalManifest
	// limits candidate count, so a manifest declaring thousands of catalogs could
	// otherwise occupy the request (bounded by inspectionTimeout) issuing that
	// many outbound probes.
	externalInspectionMaxCatalogs = 100
	// externalListMaxPages is a guardrail for public web list adapters. Clients
	// still request one 20-row Stremio page at a time; this only bounds how far
	// a single deep skip may walk before a malformed source is stopped.
	externalListMaxPages = 100
)

// catalogRequest is one parsed /catalog/... request. StreamName comes from
// the authenticated token, not the path: the local catalogs (Continue
// Watching, Because You Watched) are per-stream. Profile is the requesting
// stream's resolved metadata profile.
type catalogRequest struct {
	Type       string
	ID         string
	Search     string
	Skip       int
	StreamName string
	Profile    *config.MetadataProfileConfig
}

// ExternalCatalogPreview is one browseable row discovered from a pasted
// public manifest. It contains only catalog coordinates — never remote
// configuration, search, stream, or subtitle capabilities.
type ExternalCatalogPreview struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	RemoteType   string `json:"remote_type"`
	RemoteID     string `json:"remote_id"`
	RowCount     int    `json:"row_count"`
	SupportsSkip bool   `json:"supports_skip"`
}

// ExternalManifestInspection is the safe result of testing every eligible
// browse row in a pasted manifest. Unavailable lists are deliberately omitted
// rather than presented as selectable dead rows.
type ExternalManifestInspection struct {
	Name     string                   `json:"name"`
	Catalogs []ExternalCatalogPreview `json:"catalogs"`
	Warnings []string                 `json:"warnings,omitempty"`
}

// InspectExternalManifest reads a public manifest and live-tests every
// supported browse row. It is intentionally catalog-only: required-search
// rows, custom content types, and all non-catalog resources are ignored.
func InspectExternalManifest(ctx context.Context, rawURL string, allowPrivate []*net.IPNet) (*ExternalManifestInspection, error) {
	manifestURL, err := validExternalManifestURL(rawURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := externalHTTPClient(allowPrivate).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest returned %s", resp.Status)
	}
	var manifest struct {
		Name     string `json:"name"`
		Catalogs []struct {
			ID    string `json:"id"`
			Type  string `json:"type"`
			Name  string `json:"name"`
			Extra []struct {
				Name       string `json:"name"`
				IsRequired bool   `json:"isRequired"`
			} `json:"extra"`
			// ExtraSupported and ExtraRequired are the older spelling of the
			// same contract. Plenty of addons still publish only these, and
			// reading just "extra" made every one of their rows look
			// unpaged — a saved row that then served its first 20 items and
			// nothing else.
			ExtraSupported []string `json:"extraSupported"`
			ExtraRequired  []string `json:"extraRequired"`
		} `json:"catalogs"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, externalCatalogMaxBody)).Decode(&manifest); err != nil {
		return nil, err
	}
	inspection := &ExternalManifestInspection{Name: strings.TrimSpace(manifest.Name)}
	type candidate struct {
		def          CatalogDef
		name         string
		supportsSkip bool
	}
	var candidates []candidate
	for _, cat := range manifest.Catalogs {
		contentType := strings.ToLower(strings.TrimSpace(cat.Type))
		if contentType == "tv" {
			contentType = "series"
		}
		name := strings.TrimSpace(cat.Name)
		if cat.ID == "" || name == "" || (contentType != "movie" && contentType != "series" && contentType != "anime") {
			continue
		}
		searchOnly, supportsSkip := false, false
		for _, extra := range cat.Extra {
			if extra.Name == "search" && extra.IsRequired {
				searchOnly = true
			}
			if extra.Name == "skip" {
				supportsSkip = true
			}
		}
		if slices.Contains(cat.ExtraSupported, "skip") {
			supportsSkip = true
		}
		if slices.Contains(cat.ExtraRequired, "search") {
			searchOnly = true
		}
		if searchOnly {
			continue
		}
		if len(candidates) >= externalInspectionMaxCatalogs {
			inspection.Warnings = append(inspection.Warnings, "too many catalogs in this manifest; only the first rows were tested")
			break
		}
		candidates = append(candidates, candidate{
			def:          CatalogDef{Type: contentType, Provider: "external", ExternalManifestURL: manifestURL.String(), ExternalRemoteType: cat.Type, ExternalRemoteID: cat.ID},
			name:         name,
			supportsSkip: supportsSkip,
		})
	}
	if len(candidates) == 0 {
		return inspection, nil
	}
	type result struct {
		preview ExternalCatalogPreview
		err     error
	}
	results := make([]result, len(candidates))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(externalInspectionConcurrency, len(candidates)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				candidate := candidates[i]
				// The untruncated first response: what the row is worth is how
				// many rows the source really hands over, not the page size we
				// serve it in.
				metas, err := externalManifestCatalogPage(ctx, candidate.def, 0, allowPrivate)
				if err != nil {
					results[i].err = err
					continue
				}
				if len(metas) == 0 {
					results[i].err = fmt.Errorf("catalog returned no canonical items")
					continue
				}
				results[i].preview = ExternalCatalogPreview{Name: candidate.name, Type: candidate.def.Type, RemoteType: candidate.def.ExternalRemoteType, RemoteID: candidate.def.ExternalRemoteID, RowCount: len(metas), SupportsSkip: candidate.supportsSkip}
			}
		}()
	}
	for i := range candidates {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	for i, result := range results {
		if result.err != nil {
			inspection.Warnings = append(inspection.Warnings, fmt.Sprintf("%s: %v", candidates[i].name, result.err))
			continue
		}
		inspection.Catalogs = append(inspection.Catalogs, result.preview)
	}
	return inspection, nil
}

// handleCatalog serves /catalog/{type}/{id}.json and
// /catalog/{type}/{id}/{extra}.json. Unknown or disabled catalogs — including
// every catalog for a stream with no metadata profile bound — are 404s;
// upstream failures degrade to an empty page so a flaky provider never
// renders as a client error row.
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	stream, _ := auth.StreamFromContext(r)
	profile := s.metadataProfileFor(stream)
	if profile == nil {
		http.NotFound(w, r)
		return
	}

	req, ok := parseCatalogPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	req.StreamName = streamID(stream)
	req.Profile = profile
	def, ok := resolveCatalogDef(profile, req)
	if !ok {
		http.NotFound(w, r)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), catalogRequestTimeout)
	defer cancel()

	metas := s.serveCatalog(ctx, def, req)
	maxAge := catalogListingCacheMaxAge
	switch {
	case def.Provider == "local" || def.Provider == "simkl":
		// Personal rows — never client-cached.
		maxAge = catalogLocalCacheMaxAge
	case req.Search != "":
		maxAge = catalogSearchCacheMaxAge
	}
	writeJSONCached(w, CatalogResponse{Metas: metas, CacheMaxAge: maxAge}, maxAge, 0)
}

// parseCatalogPath splits "/catalog/{type}/{id}.json" or
// "/catalog/{type}/{id}/{extra}.json", where extra is a URL-encoded query
// string ("search=dune", "skip=40"). Existing user catalog ids can contain
// slashes, so the optional extra is detected from the final query segment.
func parseCatalogPath(path string) (catalogRequest, bool) {
	path = strings.TrimPrefix(path, "/catalog/")
	path = strings.TrimSuffix(path, ".json")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return catalogRequest{}, false
	}
	req := catalogRequest{Type: parts[0], ID: strings.Join(parts[1:], "/")}
	if len(parts) >= 3 && strings.Contains(parts[len(parts)-1], "=") {
		extra, err := url.ParseQuery(parts[len(parts)-1])
		if err != nil {
			return catalogRequest{}, false
		}
		req.ID = strings.Join(parts[1:len(parts)-1], "/")
		if req.ID == "" {
			return catalogRequest{}, false
		}
		req.Search = strings.TrimSpace(extra.Get("search"))
		if skip, err := strconv.Atoi(extra.Get("skip")); err == nil && skip > 0 {
			req.Skip = skip
		}
	}
	return req, true
}

// resolveCatalogDef finds the catalog a request names and decides whether
// this profile may read it. The hidden search carriers answer for every
// profile, but only with a query — their search extra is declared required, so
// a bare listing request is a client ignoring the manifest. Browse catalogs
// must be enabled on the profile, and only take a query when they support one.
func resolveCatalogDef(profile *config.MetadataProfileConfig, req catalogRequest) (CatalogDef, bool) {
	if def, ok := searchCatalogDefByID(profile, req.ID); ok {
		return def, def.Type == req.Type && req.Search != ""
	}
	for _, d := range enabledCatalogDefs(profile) {
		if d.ID == req.ID && d.Type == req.Type {
			return d, req.Search == "" || d.SupportsSearch
		}
	}
	return CatalogDef{}, false
}

// serveCatalog is the one catalog page path: build, drop rows a higher-ranked
// catalog already shows, overlay posters. Upstream failures degrade to an
// empty page so a flaky provider never renders as a client error row. The
// result is never nil.
func (s *Server) serveCatalog(ctx context.Context, def CatalogDef, req catalogRequest) []MetaPreview {
	metas, err := s.buildCatalog(ctx, def, req)
	if err != nil {
		// Warn, throttled per catalog: a library that renders empty with
		// nothing above DEBUG in the log is indistinguishable from "no rows".
		if logger.Throttle("catalog-build-failed:"+def.ID, 5*time.Minute) {
			logger.Warn("Catalog build failed; serving empty page",
				"catalog", def.ID, "provider", def.Provider, "searched", req.Search != "", "skip", req.Skip, "err", err)
		} else {
			logger.Debug("Catalog build failed; serving empty page",
				"catalog", def.ID, "search", req.Search, "skip", req.Skip, "err", err)
		}
		metas = nil
	}
	if def.Provider == "external" && len(metas) > 0 {
		// Manifests and pasted list pages are catalog coordinates, not a trusted
		// artwork provider. Fill their canonical ids through our cached metadata
		// clients so they render with the same poster/backdrop treatment as every
		// built-in row.
		s.enrichExternalCatalogPreviews(ctx, metas, def.Type, req.Profile.EffectiveLanguage())
		if cap, capped := capForProfile(req.Profile); capped {
			metas = s.filterPreviewsByCertification(ctx, cap, metas, def.Type)
		}
	}
	// External rows are explicitly chosen by the user as complete lists. Do
	// not remove overlaps with an earlier board row: apart from making a saved
	// list incomplete, a shortened Stremio page makes Jellyfin clients believe
	// they reached the end and they never request the later pages.
	switch {
	case req.Search == "" && catalogUsesCrossDedup(def) && len(metas) > 0:
		metas = filterHigherRankedDuplicates(metas, s.higherRankedCatalogIDs(ctx, req.Profile, def), previewIDKeys)
	case req.Search != "" && def.Kind == "search" && len(metas) > 0:
		metas = filterHigherRankedDuplicates(metas, s.higherRankedSearchKeys(ctx, req.Profile, def, req.Search), s.canonicalKeysFor(def.Type))
	}
	if len(metas) > 0 {
		s.fillPreviewAirDates(ctx, req.Profile, metas)
		metas = filterUnreleasedPreviews(metas, req.Profile.EffectiveUnreleasedWindowDays(), time.Now())
	}
	if len(metas) > 0 && hidesIncompleteRows(def) && req.Profile.EffectiveHideIncompleteMetadata() {
		metas = filterIncompletePreviews(metas)
	}
	s.applyPosterOverlays(ctx, req.Profile, metas)
	if metas == nil {
		metas = []MetaPreview{}
	}
	return metas
}

// hidesIncompleteRows excludes the two kinds of row the filter must never
// thin. An external row is a list the user chose: apart from making a saved
// list incomplete, a shortened Stremio page makes Jellyfin clients believe
// they reached the end of it. A local row is personal — Continue Watching is
// a title already being played, and a provider hiccup that costs it its
// poster must not also cost it its place in the row.
func hidesIncompleteRows(def CatalogDef) bool {
	return def.Provider != "external" && def.Provider != "local"
}

// fillPreviewAirDates dates the rows nothing else could, from the air-date
// authority. TMDB search publishes an empty first_air_date for a record
// nobody has filled in and TVDB search carries a year at best, so without
// this the unreleased window has nothing to compare and the row stays — which
// is right for missing data and wrong for a title TVMaze knows perfectly well
// is years off. It also supplies the "not out yet" statement TMDB rows never
// carry. Only rows still missing a date are looked up, and only when the
// profile has TVMaze air dates on.
func (s *Server) fillPreviewAirDates(ctx context.Context, profile *config.MetadataProfileConfig, metas []MetaPreview) {
	if s.tvmazeClient == nil || !profile.EffectiveTVMazeAirDates() {
		return
	}
	sem := make(chan struct{}, externalIDConcurrency)
	var wg sync.WaitGroup
	for i := range metas {
		if metas[i].released != "" || ctx.Err() != nil {
			continue
		}
		// Anime keeps Kitsu ids, which TVMaze has no lookup for; its entries
		// carry Kitsu's own start date instead.
		imdbID, tvdbID := "", ""
		switch {
		case strings.HasPrefix(metas[i].ID, "tt"):
			imdbID = metas[i].ID
		case strings.HasPrefix(metas[i].ID, "tvdb:"):
			tvdbID = strings.TrimPrefix(metas[i].ID, "tvdb:")
		default:
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, imdbID, tvdbID string) {
			defer wg.Done()
			defer func() { <-sem }()
			var show *tvmaze.Show
			var err error
			if imdbID != "" {
				show, err = s.tvmazeClient.LookupByIMDB(ctx, imdbID)
			} else {
				show, err = s.tvmazeClient.LookupByTVDB(ctx, tvdbID)
			}
			if err != nil || show == nil {
				return
			}
			if premiered := strings.TrimSpace(show.Premiered); premiered != "" {
				metas[i].released = premiered
				if metas[i].ReleaseInfo == "" && len(premiered) >= 4 {
					metas[i].ReleaseInfo = premiered[:4]
				}
				return
			}
			if tvmazeUnreleased(show.Status) {
				metas[i].unreleased = true
			}
		}(i, imdbID, tvdbID)
	}
	wg.Wait()
}

// tvmazeUnreleased reads TVMaze's own words for a show that has not started.
// "To Be Determined" is a show with a confirmed future run and no date yet;
// "In Development" is one that has not even reached that.
func tvmazeUnreleased(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "in development", "to be determined":
		return true
	}
	return false
}

// filterIncompletePreviews drops rows that no source could describe. Artwork
// is the test because it is what a client renders a row as: a row without it
// is a blank tile in every grid, and the sources only ever leave one that
// bare for a record nobody has filled in — a duplicate, a placeholder, an
// announcement with nothing behind it yet. It runs after the search fallback
// chain and its gap filling, so a row is only dropped once every source the
// profile ranks has had its turn.
func filterIncompletePreviews(metas []MetaPreview) []MetaPreview {
	filtered := metas[:0]
	for _, preview := range metas {
		if strings.TrimSpace(preview.Poster) == "" {
			continue
		}
		filtered = append(filtered, preview)
	}
	return filtered
}

// filterUnreleasedPreviews drops rows scheduled further out than the
// profile's window. A row whose provider published no date is kept: the
// window exists to keep announcements off the board, and a missing date is
// missing data, not a release far in the future.
func filterUnreleasedPreviews(metas []MetaPreview, windowDays int, now time.Time) []MetaPreview {
	cutoff := now.AddDate(0, 0, windowDays)
	filtered := metas[:0]
	for _, preview := range metas {
		released, known := previewReleaseTime(preview.released)
		switch {
		case known && released.After(cutoff):
			continue
		case !known && preview.unreleased && windowDays < config.MaxUnreleasedWindowDays:
			// The source says this has not come out and publishes no date for
			// it, so nothing can place it inside a window — as opposed to a
			// row with no date and no such statement, which is missing data
			// and stays. The far end of the slider means "everything
			// upcoming" and keeps these too.
			continue
		}
		filtered = append(filtered, preview)
	}
	return filtered
}

// previewReleaseTime reads the date a provider published for a row. Sources
// give either a full date ("2026-01-07") or a bare year; a bare year is read
// as its first day, so next year's entries fall outside any window under a
// year while this year's stay inside it.
func previewReleaseTime(released string) (time.Time, bool) {
	released = strings.TrimSpace(released)
	for _, layout := range []string{time.DateOnly, "2006"} {
		if parsed, err := time.Parse(layout, released); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func catalogUsesCrossDedup(def CatalogDef) bool {
	return def.Provider != "external"
}

// pagesLocally reports whether a catalog can answer a skip out of a response
// it already knows how to fetch, rather than by asking the source for that
// page. Manifest rows can: the remote hands over a whole list at once. The
// list kinds are excluded because they walk their own pages already; the
// split mirrors buildCatalog's dispatch, where everything else is a manifest
// (a pasted manifest saves no kind at all).
func pagesLocally(def CatalogDef) bool {
	if def.Provider != "external" {
		return false
	}
	switch def.ExternalKind {
	case "tmdb_list", "mdblist", "letterboxd":
		return false
	}
	return true
}

func (s *Server) buildCatalog(ctx context.Context, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	if def.Kind == "search" {
		return s.searchCatalog(ctx, def, req)
	}
	if req.Skip > 0 && req.Search == "" && !def.SupportsSkip && !pagesLocally(def) {
		// A catalog without paging has only its first page; answering a
		// skip with the first page again would repeat it.
		return nil, nil
	}
	switch def.Provider {
	case "tmdb":
		return s.tmdbCatalog(ctx, def, req)
	case "tvdb":
		return s.tvdbCatalog(ctx, def, req)
	case "kitsu":
		return s.kitsuCatalog(ctx, def, req)
	case "simkl":
		return s.simklCatalog(ctx, def, req)
	case "local":
		if def.Kind == "because-you-watched" {
			return s.becauseYouWatchedCatalog(ctx, def, req)
		}
		return s.continueWatchingCatalog(ctx, def, req)
	case "external":
		if def.ExternalKind == "tmdb_list" {
			return s.tmdbExternalListCatalog(ctx, def, req)
		}
		if def.ExternalKind == "mdblist" {
			return s.mdbListCatalog(ctx, def, req)
		}
		if def.ExternalKind == "letterboxd" {
			return s.letterboxdCatalog(ctx, def, req)
		}
		return externalManifestCatalog(ctx, def, req, s.catalogSourceNetworks())
	}
	return nil, fmt.Errorf("unknown catalog provider %q", def.Provider)
}

// searchCatalog answers a search from the profile's priority list for that
// media type. The leading source is the carrier's own; the rest are consulted
// only when it cannot answer, which is what the list has always promised on
// title pages and now means in search too — a series TMDB never populated is
// served from TVDB when TVDB leads, rather than as a blank row.
func (s *Server) searchCatalog(ctx context.Context, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	sources := searchSources(req.Profile, def.Type)
	var lastErr error
	for i, source := range sources {
		// Only the leading source is owed the request's full budget; a
		// fallback runs on what is left of it.
		if i > 0 && ctx.Err() != nil {
			break
		}
		metas, err := s.searchFromSource(ctx, source, def, req)
		if err != nil {
			lastErr = err
		}
		if err == nil && len(metas) > 0 {
			s.fillSearchPreviewGaps(ctx, metas, def.Type, sources[i+1:])
			return metas, nil
		}
		if i < len(sources)-1 {
			logger.Debug("Search source had no answer; trying the next one",
				"type", def.Type, "source", source, "next", sources[i+1], "search", req.Search, "err", err)
		}
	}
	return nil, lastErr
}

func (s *Server) searchFromSource(ctx context.Context, source string, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	switch source {
	case "tmdb":
		return s.tmdbCatalog(ctx, def, req)
	case "tvdb":
		if def.Type == "anime" {
			return s.tvdbAnimeSearchCatalog(ctx, def, req)
		}
		return s.tvdbSeriesSearchCatalog(ctx, def, req)
	case "kitsu":
		return s.kitsuCatalog(ctx, def, req)
	case "cinemeta":
		return s.cinemetaSearchCatalog(ctx, def, req)
	}
	return nil, fmt.Errorf("source %q cannot serve a search", source)
}

// tvdbSeriesSearchCatalog searches TVDB for ordinary series. Unlike the anime
// carrier it keeps the record's own identity — search already returns the
// IMDb id, so the row is playable by any other addon the user has installed
// without a lookup per result.
func (s *Server) tvdbSeriesSearchCatalog(ctx context.Context, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	rt := s.runtime()
	if rt.tvdbClient == nil {
		return nil, fmt.Errorf("TVDB client not configured")
	}
	// TVDB search answers one page and has no skip; a later page is empty,
	// not the same twenty again.
	if req.Skip > 0 {
		return nil, nil
	}
	results, err := rt.tvdbClient.SearchSeries(req.Search)
	if err != nil {
		return nil, err
	}
	lang3 := tvdb.LanguageToISO3(req.Profile.EffectiveLanguage())
	previews := make([]MetaPreview, 0, len(results))
	seen := map[string]bool{}
	for _, result := range results {
		title := result.TitleIn(lang3)
		id := result.IMDbID()
		if id == "" {
			if seriesID := result.SeriesID(); seriesID != "" {
				id = "tvdb:" + seriesID
			}
		}
		if title == "" || id == "" || seen[id] {
			continue
		}
		seen[id] = true
		preview := MetaPreview{ID: id, Type: def.Type, Name: title, Description: result.Overview, ReleaseInfo: result.Year, released: result.Year, unreleased: result.Unreleased(), tmdbID: result.TMDBID()}
		if result.ImageURL != "" {
			preview.Poster = result.ImageURL
			if !strings.HasPrefix(preview.Poster, "http") {
				preview.Poster = tvdbArtworkURL + preview.Poster
			}
		}
		previews = append(previews, preview)
		if len(previews) >= catalogPageSize {
			break
		}
	}
	if cap, capped := capForProfile(req.Profile); capped {
		previews = s.filterPreviewsByCertification(ctx, cap, previews, def.Type)
	}
	return previews, nil
}

// cinemetaSearchCatalog searches the public Cinemeta catalog. Every row is
// IMDb-keyed already, which is the id scheme the rest of the addon prefers.
func (s *Server) cinemetaSearchCatalog(ctx context.Context, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	if s.cinemetaClient == nil {
		return nil, fmt.Errorf("cinemeta client not configured")
	}
	if req.Skip > 0 {
		return nil, nil
	}
	results, err := s.cinemetaClient.Search(ctx, def.Type, req.Search)
	if err != nil {
		return nil, err
	}
	previews := make([]MetaPreview, 0, len(results))
	for _, result := range results {
		previews = append(previews, MetaPreview{
			ID:          result.IMDbID,
			Type:        def.Type,
			Name:        result.Name,
			Poster:      result.Poster,
			Description: result.Description,
			ReleaseInfo: result.ReleaseInfo,
			IMDBRating:  result.IMDBRating,
			released:    releaseInfoYear(result.ReleaseInfo),
		})
		if len(previews) >= catalogPageSize {
			break
		}
	}
	if cap, capped := capForProfile(req.Profile); capped {
		previews = s.filterPreviewsByCertification(ctx, cap, previews, def.Type)
	}
	return previews, nil
}

// releaseInfoYear reads the first year out of a "2011-2019" / "2023-" range.
func releaseInfoYear(releaseInfo string) string {
	releaseInfo = strings.TrimSpace(releaseInfo)
	if len(releaseInfo) < 4 {
		return ""
	}
	year := releaseInfo[:4]
	if _, err := strconv.Atoi(year); err != nil {
		return ""
	}
	return year
}

// fillSearchPreviewGaps completes rows the winning source left thin, from a
// source ranked behind it. TVDB search publishes no rating at all, so ranking
// it first would otherwise cost every row the badge a TMDB row carries. The
// fill is per-field and never replaces what the winner did publish: the point
// of ranking a source first is that its answer is the one shown.
func (s *Server) fillSearchPreviewGaps(ctx context.Context, metas []MetaPreview, contentType string, fallbacks []string) {
	rt := s.runtime()
	if rt.tmdbClient == nil || !slices.Contains(fallbacks, "tmdb") {
		return
	}
	sem := make(chan struct{}, externalIDConcurrency)
	var wg sync.WaitGroup
	for i := range metas {
		if metas[i].Poster != "" && metas[i].IMDBRating != "" && metas[i].Description != "" {
			continue
		}
		if (!strings.HasPrefix(metas[i].ID, "tt") && metas[i].tmdbID <= 0) || ctx.Err() != nil {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			fill, ok := s.tmdbPreviewFill(metas[i], contentType)
			if !ok {
				return
			}
			if metas[i].IMDBRating == "" && fill.vote > 0 {
				metas[i].IMDBRating = fmt.Sprintf("%.1f", fill.vote)
			}
			if metas[i].Poster == "" && fill.posterPath != "" {
				metas[i].Poster = tmdbPosterURL + fill.posterPath
			}
			if metas[i].Background == "" && fill.backdropPath != "" {
				metas[i].Background = tmdbBackdropURL + fill.backdropPath
			}
			if metas[i].Description == "" {
				metas[i].Description = fill.overview
			}
			if metas[i].released == "" && fill.date != "" {
				metas[i].released = fill.date
				if metas[i].ReleaseInfo == "" {
					metas[i].ReleaseInfo = fill.date[:4]
				}
			}
		}(i)
	}
	wg.Wait()
}

// previewFill is the subset of a TMDB record a thin row can borrow.
type previewFill struct {
	overview     string
	posterPath   string
	backdropPath string
	date         string
	vote         float64
}

// tmdbPreviewFill reaches TMDB by whichever id the row carries: its IMDb id
// through find, or the TMDB id its source volunteered. A row identified only
// as tvdb:N has no find route at all, which is exactly the row most likely to
// be missing a rating.
func (s *Server) tmdbPreviewFill(preview MetaPreview, contentType string) (previewFill, bool) {
	rt := s.runtime()
	if strings.HasPrefix(preview.ID, "tt") {
		find, err := rt.tmdbClient.Find(preview.ID, "imdb_id")
		if err != nil {
			return previewFill{}, false
		}
		res, ok := pickFindResult(find, contentType)
		if !ok {
			return previewFill{}, false
		}
		return previewFill{overview: res.Overview, posterPath: res.PosterPath, backdropPath: res.BackdropPath, vote: res.VoteAverage, date: firstNonEmptyDate(res.ReleaseDate, res.FirstAirDate)}, true
	}
	if preview.tmdbID <= 0 {
		return previewFill{}, false
	}
	if contentType == "movie" {
		details, err := rt.tmdbClient.GetMovieDetails(preview.tmdbID)
		if err != nil {
			return previewFill{}, false
		}
		return previewFill{overview: details.Overview, posterPath: details.PosterPath, backdropPath: details.BackdropPath, vote: details.VoteAverage, date: firstNonEmptyDate(details.ReleaseDate)}, true
	}
	details, err := rt.tmdbClient.GetTVDetails(preview.tmdbID)
	if err != nil {
		return previewFill{}, false
	}
	return previewFill{overview: details.Overview, posterPath: details.PosterPath, backdropPath: details.BackdropPath, vote: details.VoteAverage, date: firstNonEmptyDate(details.FirstAirDate)}, true
}

func firstNonEmptyDate(dates ...string) string {
	for _, date := range dates {
		if len(date) >= 4 {
			return date
		}
	}
	return ""
}

func (s *Server) letterboxdCatalog(ctx context.Context, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	return s.publicListCatalog(ctx, def, req, 1, func(page int) ([]tmdb.PublicListItem, error) {
		list, err := tmdb.FetchLetterboxdPage(ctx, def.ExternalManifestURL, page)
		return list.Items, err
	})
}

func (s *Server) tmdbExternalListCatalog(ctx context.Context, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	return s.publicListCatalog(ctx, def, req, 1, func(page int) ([]tmdb.PublicListItem, error) {
		return tmdb.FetchPublicListPage(ctx, def.ExternalManifestURL, page)
	})
}

func (s *Server) mdbListCatalog(ctx context.Context, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	return s.publicListCatalog(ctx, def, req, 0, func(page int) ([]tmdb.PublicListItem, error) {
		listing, err := tmdb.FetchMDBListPage(ctx, def.ExternalManifestURL, page)
		return listing.Items, err
	})
}

// publicListCatalog turns public web list pages into a normal Stremio page.
// It reads as many upstream pages as the requested skip needs, then slices to
// catalogPageSize. This is what makes a 200-item pasted list reachable in its
// entirety instead of exposing only the source's first HTML page.
func (s *Server) publicListCatalog(_ context.Context, def CatalogDef, req catalogRequest, firstPage int, fetch func(page int) ([]tmdb.PublicListItem, error)) ([]MetaPreview, error) {
	needed := req.Skip + catalogPageSize
	previews := make([]MetaPreview, 0, needed)
	seen := make(map[string]struct{})
	// fetched tracks every row the source has handed over, of either media
	// type, so that "this page was new" can be told apart from "this page had
	// nothing for *this* catalog".
	fetched := make(map[string]struct{})
	for page := firstPage; page < firstPage+externalListMaxPages && len(previews) < needed; page++ {
		items, err := fetch(page)
		if err != nil {
			// Public HTML lists often signal their final page with a 404. It is
			// terminal pagination, not a failed catalog the client should retry.
			if errors.Is(err, tmdb.ErrPublicListPageNotFound) {
				break
			}
			return nil, err
		}
		fresh := 0
		for _, item := range items {
			imdbID := strings.TrimSpace(item.IMDbID)
			raw := fmt.Sprintf("%s:%d:%s", item.Type, item.ID, imdbID)
			if _, repeated := fetched[raw]; repeated {
				continue
			}
			fetched[raw] = struct{}{}
			fresh++
			isMovie := item.Type == "movie"
			name := cleanExternalPreviewName(item.Name)
			if (def.Type == "movie") != isMovie || (item.ID <= 0 && imdbID == "") || name == "" {
				continue
			}
			id := imdbID
			if id == "" {
				id = fmt.Sprintf("tmdb:%d", item.ID)
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			previews = append(previews, MetaPreview{ID: id, Type: def.Type, Name: name})
		}
		// An empty page is terminal, and so is one that repeats rows already
		// fetched: not every source answers a page past the end with an empty
		// document — MDBList's infinite-scroll endpoint re-serves the last one —
		// and without this a request past the end of a list would walk all
		// externalListMaxPages before giving up. A page holding only the other
		// media type still counts as progress, so a mixed list keeps paging.
		if len(items) == 0 || fresh == 0 {
			break
		}
	}
	if req.Skip >= len(previews) {
		return nil, nil
	}
	previews = previews[req.Skip:]
	if len(previews) > catalogPageSize {
		previews = previews[:catalogPageSize]
	}
	return previews, nil
}

const externalCatalogMaxBody = 2 << 20

var externalReleaseDateSuffix = regexp.MustCompile(`\s+\((?:18|19|20)\d{2}(?:-\d{2}(?:-\d{2})?)?\)$`)

// externalManifestCatalog fetches only a previously selected catalog resource
// from a public HTTPS manifest. It never forwards a search term or any caller
// credentials, so an external addon cannot become part of StreamNZB search or
// playback.
// externalManifestCatalog serves one page of a pasted manifest's row. A
// remote that declares paging is asked for the skip directly; one that does
// not is asked once and paged locally, because an addon that answers with its
// whole list in a single response still has every row past the twentieth —
// truncating there and refusing later skips was what made a saved source look
// like a 20-item list.
func externalManifestCatalog(ctx context.Context, def CatalogDef, req catalogRequest, allowPrivate []*net.IPNet) ([]MetaPreview, error) {
	if req.Search != "" {
		return nil, fmt.Errorf("external catalogs do not support search")
	}
	remoteSkip, localSkip := req.Skip, 0
	if !def.SupportsSkip {
		remoteSkip, localSkip = 0, req.Skip
	}
	metas, err := externalManifestCatalogPage(ctx, def, remoteSkip, allowPrivate)
	if err != nil {
		return nil, err
	}
	if localSkip >= len(metas) {
		return nil, nil
	}
	return limitCatalogPage(metas[localSkip:]), nil
}

func externalManifestCatalogPage(ctx context.Context, def CatalogDef, skip int, allowPrivate []*net.IPNet) ([]MetaPreview, error) {
	manifest, err := validExternalManifestURL(def.ExternalManifestURL)
	if err != nil {
		return nil, err
	}
	basePath := strings.TrimSuffix(manifest.Path, "manifest.json")
	endpoint := *manifest
	endpoint.Path = basePath + "catalog/" + url.PathEscape(def.ExternalRemoteType) + "/" + url.PathEscape(def.ExternalRemoteID) + ".json"
	endpoint.RawQuery = ""
	if skip > 0 {
		endpoint.Path = strings.TrimSuffix(endpoint.Path, ".json") + "/skip=" + strconv.Itoa(skip) + ".json"
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "application/json")
	resp, err := externalHTTPClient(allowPrivate).Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("external catalog returned %s", resp.Status)
	}
	// Decode only the two fields a pasted row may contribute. Reusing
	// MetaPreview here meant one addon spelling releaseInfo as a number
	// (2024, not "2024") failed the whole catalog on a field we never read.
	var remote struct {
		Metas []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"metas"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, externalCatalogMaxBody)).Decode(&remote); err != nil {
		return nil, err
	}
	metas := make([]MetaPreview, 0, len(remote.Metas))
	for _, remoteMeta := range remote.Metas {
		id := canonicalExternalCatalogID(remoteMeta.ID)
		name := cleanExternalPreviewName(remoteMeta.Name)
		if id == "" || name == "" {
			continue
		}
		// A pasted manifest supplies browse coordinates only. Its presentation
		// fields are untrusted; local metadata enrichment owns artwork and copy.
		metas = append(metas, MetaPreview{ID: id, Type: def.Type, Name: name})
	}
	return metas, nil
}

// cleanExternalPreviewName removes only a trailing release year or ISO date
// that public lists append to an otherwise canonical title. The preview id is
// still authoritative; normal metadata supplies the real title and date.
func cleanExternalPreviewName(name string) string {
	return strings.TrimSpace(externalReleaseDateSuffix.ReplaceAllString(strings.TrimSpace(name), ""))
}

func limitCatalogPage(metas []MetaPreview) []MetaPreview {
	if len(metas) > catalogPageSize {
		return metas[:catalogPageSize]
	}
	return metas
}

func validExternalManifestURL(rawURL string) (*url.URL, error) {
	manifest, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || manifest.Scheme != "https" || manifest.Host == "" || !strings.HasSuffix(manifest.Path, "/manifest.json") {
		return nil, fmt.Errorf("invalid external manifest URL")
	}
	return manifest, nil
}

// catalogSourceNetworks is the operator's allowlist of non-public networks an
// external catalog source may live on, read fresh so a config reload takes
// effect without restarting.
func (s *Server) catalogSourceNetworks() []*net.IPNet {
	return s.currentConfig().CatalogSourceAllowedNetworks()
}

// externalHTTPClient fetches a user-pasted manifest URL and its catalog pages.
// The destination is admin-supplied, so every dial — including ones a redirect
// points at — is guarded against reaching internal infrastructure (SSRF), and
// redirects may not leave HTTPS. allowPrivate carries the operator's
// CatalogSourceNetworks, which is what makes a self-hosted addon on their own
// LAN reachable; httpx pools one transport per distinct allowlist.
// externalHTTPClient is the guarded client every operator-supplied fetch
// goes through. It is a var so a test can substitute a client that trusts a
// local TLS stub — the guard itself is covered by its own tests.
var externalHTTPClient = func(allowPrivate []*net.IPNet) *http.Client {
	return httpx.GuardedClient(catalogRequestTimeout, allowPrivate)
}

func canonicalExternalCatalogID(id string) string {
	id = strings.TrimSpace(id)
	if strings.HasPrefix(id, "tt") {
		if _, err := strconv.ParseInt(strings.TrimPrefix(id, "tt"), 10, 64); err == nil {
			return id
		}
	}
	for _, prefix := range []string{"tmdb:", "tvdb:", "kitsu:"} {
		if raw, ok := strings.CutPrefix(id, prefix); ok {
			if _, err := strconv.ParseInt(raw, 10, 64); err == nil {
				return id
			}
		}
	}
	return ""
}

func (s *Server) tmdbCatalog(_ context.Context, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	rt := s.runtime()
	mediaType := "tv"
	if def.Type == "movie" {
		mediaType = "movie"
	}
	page := req.Skip/catalogPageSize + 1

	var resp *tmdb.ListingResponse
	var err error
	switch {
	case req.Search != "":
		resp, err = rt.tmdbClient.SearchByType(mediaType, req.Search, page, req.Profile.EffectiveLanguage())
	case def.Kind == "discover":
		filters := tmdb.DiscoverFilters{Genres: def.DiscoverGenres}
		// Movies push the ceiling upstream (certification.lte) so the row
		// stays dense under a cap; TV discover has no certification filter,
		// so series rows rely on the post-filter below.
		if ceiling := catalogCertCeilingAge(def, req.Profile); ceiling >= 0 && mediaType == "movie" {
			filters.MaxCert = certification.USMovieCertLTE(ceiling)
		}
		resp, err = rt.tmdbClient.Discover(mediaType, filters, page, req.Profile.EffectiveLanguage())
	default:
		resp, err = rt.tmdbClient.GetListing(mediaType, def.Kind, page, req.Profile.EffectiveLanguage())
	}
	if err != nil {
		return nil, err
	}
	if cap, capped := capForProfile(req.Profile); capped {
		resp.Results = s.filterTMDBResults(mediaType, resp.Results, cap)
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
		if date := res.ReleaseDate; date != "" || res.FirstAirDate != "" {
			if date == "" {
				date = res.FirstAirDate
			}
			preview.released = date
			if len(date) >= 4 {
				preview.ReleaseInfo = date[:4]
			}
		}
		if res.VoteAverage > 0 {
			preview.IMDBRating = fmt.Sprintf("%.1f", res.VoteAverage)
		}
		if res.PosterPath != "" {
			preview.Poster = tmdbPosterURL + res.PosterPath
		}
		if res.BackdropPath != "" {
			preview.Background = tmdbBackdropURL + res.BackdropPath
		}
		previews = append(previews, preview)
	}
	return previews, nil
}

// resolveIMDbIDs fans out external-id lookups for one catalog page, bounded to
// externalIDConcurrency in flight. Failures leave the slot empty — the caller
// falls back to a tmdb: id.
func (s *Server) resolveIMDbIDs(mediaType string, results []tmdb.SearchMultiResult) []string {
	rt := s.runtime()
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
			ext, err := rt.tmdbClient.GetExternalIDs(tmdbID, mediaType)
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
func (s *Server) tvdbCatalog(ctx context.Context, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	rt := s.runtime()
	if req.Search != "" {
		return s.tvdbAnimeSearchCatalog(ctx, def, req)
	}
	sort := "score"
	if def.Kind == "new" {
		sort = "firstAired"
	}
	listings, err := rt.tvdbClient.FilterSeries(sort, 0)
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

	// The extended fan-out already fetches the record certifications ride on,
	// so a capped profile filters here for free.
	cap, capped := capForProfile(req.Profile)
	ids := make([]string, len(listings))
	backgrounds := make([]string, len(listings))
	allowed := make([]bool, len(listings))
	sem := make(chan struct{}, externalIDConcurrency)
	var wg sync.WaitGroup
	for i, listing := range listings {
		allowed[i] = !capped || cap.AllowUnrated
		if listing.ID <= 0 {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i, tvdbID int) {
			defer wg.Done()
			defer func() { <-sem }()
			if ext, err := rt.tvdbClient.GetSeriesExtended(strconv.Itoa(tvdbID)); err == nil {
				ids[i] = ext.IMDbID()
				backgrounds[i] = ext.Background()
				if capped {
					allowed[i] = cap.Allows(certification.Resolve(tvdbCertEntries(ext.ContentRatings)))
				}
			}
		}(i, listing.ID)
	}
	wg.Wait()

	previews := make([]MetaPreview, 0, len(listings))
	for i, listing := range listings {
		if listing.Name == "" || listing.ID <= 0 || !allowed[i] {
			continue
		}
		id := ids[i]
		if id == "" {
			id = fmt.Sprintf("tvdb:%d", listing.ID)
		}
		preview := MetaPreview{ID: id, Type: def.Type, Name: listing.Name, Description: listing.Overview, Background: backgrounds[i], released: listing.Year}
		if listing.Image != "" {
			preview.Poster = listing.Image
			if !strings.HasPrefix(preview.Poster, "http") {
				preview.Poster = tvdbArtworkURL + preview.Poster
			}
		}
		previews = append(previews, preview)
	}
	return previews, nil
}

// tvdbAnimeSearchCatalog keeps the search contract aligned with metadata
// priority. A TVDB result is accepted only when anime-lists can translate it
// to a playable Kitsu entry; unrelated shows and Kitsu's broad fuzzy matches
// never leak into the result grid. If TVDB has no usable row, Kitsu is the
// explicit backup.
// tvdbAnimeSearchCatalog searches TVDB but answers in Kitsu ids: anime keeps
// Kitsu's playback identity whichever source describes it. An entry the
// anime-lists mapping cannot place is skipped, and a query that leaves
// nothing falls through to the profile's next anime source in searchCatalog.
func (s *Server) tvdbAnimeSearchCatalog(ctx context.Context, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	rt := s.runtime()
	if rt.tvdbClient == nil {
		return nil, fmt.Errorf("TVDB client not configured")
	}
	results, err := rt.tvdbClient.SearchSeries(req.Search)
	if err != nil {
		return nil, err
	}
	previews := make([]MetaPreview, 0, len(results))
	seen := map[string]bool{}
	lang3 := tvdb.LanguageToISO3(req.Profile.EffectiveLanguage())
	for _, result := range results {
		if s.animeLists == nil {
			break
		}
		mapping, ok := s.animeLists.LookupTVDB(result.SeriesID())
		title := result.TitleIn(lang3)
		if !ok || mapping.KitsuID <= 0 || title == "" {
			continue
		}
		id := fmt.Sprintf("kitsu:%d", mapping.KitsuID)
		if seen[id] {
			continue
		}
		seen[id] = true
		previews = append(previews, MetaPreview{ID: id, Type: "anime", Name: title, Poster: result.ImageURL, released: result.Year, unreleased: result.Unreleased()})
		if len(previews) >= catalogPageSize {
			break
		}
	}
	if cap, capped := capForProfile(req.Profile); capped {
		previews = s.filterPreviewsByCertification(ctx, cap, previews, def.Type)
	}
	return previews, nil
}

func (s *Server) kitsuCatalog(ctx context.Context, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	if s.kitsuClient == nil {
		return nil, fmt.Errorf("kitsu client not configured")
	}
	var listings []kitsu.AnimeListing
	var err error
	switch {
	case req.Search != "":
		listings, err = s.kitsuClient.SearchAnime(ctx, req.Search, req.Skip)
	case def.Kind == "kids":
		// Kitsu filters age ratings server-side; the profile cap tightens the
		// catalog's built-in G,PG ceiling to G when it caps below 7.
		listings, err = s.kitsuClient.GetAnimeKidsListing(ctx, req.Skip, certification.KitsuRatingsLTE(catalogCertCeilingAge(def, req.Profile)))
	default:
		listings, err = s.kitsuClient.GetAnimeListing(ctx, def.Kind, req.Skip)
	}
	if err != nil {
		return nil, err
	}
	// Kitsu listings carry ageRating inline, so a capped profile filters with
	// zero extra fetches.
	cap, capped := capForProfile(req.Profile)
	previews := make([]MetaPreview, 0, len(listings))
	for _, item := range listings {
		if item.ID == "" || (item.CanonicalTitle == "" && item.EnglishTitle == "") {
			continue
		}
		if capped && !cap.Allows(certification.NormalizeKitsu(item.AgeRating, item.Nsfw)) {
			continue
		}
		previews = append(previews, MetaPreview{
			ID:          "kitsu:" + item.ID,
			Type:        "anime",
			Name:        kitsu.DisplayTitle(req.Profile.EffectiveLanguage(), item.EnglishTitle, item.CanonicalTitle),
			Poster:      item.PosterImage,
			Background:  item.CoverImage,
			Description: item.Synopsis,
			released:    item.StartDate,
		})
	}
	return previews, nil
}

// simklTypeForCatalog maps a catalog media type onto Simkl's list naming.
func simklTypeForCatalog(contentType string) string {
	switch contentType {
	case "movie":
		return "movies"
	case "anime":
		return "anime"
	}
	return "shows"
}

// simklCatalog serves one of the linked Simkl account's watchlists (def.Kind
// is the Simkl status). Entries arrive with their cross-service ids plus
// Simkl's own title and poster, so no per-item metadata fan-out is needed.
// Without a linked account the build fails, which the handler degrades to an
// empty page.
func (s *Server) simklCatalog(ctx context.Context, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	rt := s.runtime()
	if rt.simklClient == nil {
		return nil, fmt.Errorf("simkl is not configured")
	}
	entries, err := rt.simklClient.Watchlist(ctx, simklTypeForCatalog(def.Type), def.Kind)
	if err != nil {
		return nil, err
	}
	previews := make([]MetaPreview, 0, len(entries))
	for _, entry := range entries {
		id := s.simklPreviewID(entry, def.Type)
		if id == "" {
			continue
		}
		previews = append(previews, MetaPreview{
			ID:     id,
			Type:   def.Type,
			Name:   entry.Title,
			Poster: simkl.PosterURL(entry.Poster),
		})
	}
	if req.Skip >= len(previews) {
		return nil, nil
	}
	previews = previews[req.Skip:]
	if len(previews) > catalogPageSize {
		previews = previews[:catalogPageSize]
	}
	if cap, capped := capForProfile(req.Profile); capped {
		previews = s.filterPreviewsByCertification(ctx, cap, previews, def.Type)
	}
	return previews, nil
}

// simklPreviewID picks the preview id for one watchlist entry: tt ids first so
// any other installed addon can serve the row too. Anime resolves through the
// MAL→Kitsu mapping instead, landing on the same anime pipeline — and the same
// ids, for cross-catalog dedup — as the Kitsu rows, with the entry's own tt id
// as the fallback while the mapping has no answer.
func (s *Server) simklPreviewID(entry simkl.Entry, contentType string) string {
	if contentType == "anime" {
		if mapping, ok := s.animeLists.LookupMAL(entry.MALID); ok && mapping.KitsuID > 0 {
			return fmt.Sprintf("kitsu:%d", mapping.KitsuID)
		}
		if strings.HasPrefix(entry.IMDbID, "tt") {
			return entry.IMDbID
		}
		return ""
	}
	switch {
	case strings.HasPrefix(entry.IMDbID, "tt"):
		return entry.IMDbID
	case entry.TMDBID != "":
		return "tmdb:" + entry.TMDBID
	case entry.TVDBID != "" && contentType == "series":
		return "tvdb:" + entry.TVDBID
	}
	return ""
}

// applyPosterOverlays swaps catalog posters for the profile's overlay
// service's. Rows that resolved a tt id carry it as their preview id; kitsu:
// rows resolve to the series-level id through the anime-lists mapping — the
// granularity overlay services key on, so every cour of a series shares one
// overlay poster. tvdb:/tmdb: fallbacks and unmapped anime keep their source
// artwork.
func (s *Server) applyPosterOverlays(ctx context.Context, profile *config.MetadataProfileConfig, metas []MetaPreview) {
	if profile == nil || strings.TrimSpace(profile.PosterURLPattern) == "" {
		return
	}
	sem := make(chan struct{}, externalIDConcurrency)
	var wg sync.WaitGroup
	for i := range metas {
		id := metas[i].ID
		if kitsuID, ok := strings.CutPrefix(id, "kitsu:"); ok {
			id = s.animeSeriesIMDbID(kitsuID)
		}
		if profile.PosterOverlayURL(id) == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id string) {
			defer wg.Done()
			defer func() { <-sem }()
			if overlay := s.overlayPosterFor(ctx, profile, id); overlay != "" {
				metas[i].Poster = overlay
			}
		}(i, id)
	}
	wg.Wait()
}

const (
	// An overlay service's answer for a title barely changes, and a miss is
	// worth re-checking sooner than a hit: a poster that does not exist yet
	// is one somebody may add.
	overlayHitTTL  = 24 * time.Hour
	overlayMissTTL = 6 * time.Hour
)

// overlayPosterFor is the overlay URL for a title, or "" when the overlay
// service has no artwork for it and the source's own poster should stand.
// Overlay services cover the popular catalogue, not all of it; substituting
// blindly turned every title they have never heard of into a blank tile in
// the client. The answer is cached like any other metadata response, so this
// costs one HEAD per title per day at most.
func (s *Server) overlayPosterFor(ctx context.Context, profile *config.MetadataProfileConfig, imdbID string) string {
	overlay := profile.PosterOverlayURL(imdbID)
	if overlay == "" {
		return ""
	}
	if s.overlayCache != nil {
		if body, ok := s.overlayCache.Get(overlay); ok {
			if string(body) == "1" {
				return overlay
			}
			return ""
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, overlay, nil)
	if err != nil {
		return ""
	}
	resp, err := externalHTTPClient(s.catalogSourceNetworks()).Do(req)
	if err != nil {
		// A transient failure is not evidence the overlay is missing. Keep
		// the profile's choice and re-check next time rather than caching a
		// verdict drawn from a network hiccup.
		logger.Debug("Poster overlay probe failed; using the overlay anyway", "url", overlay, "err", err)
		return overlay
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		if s.overlayCache != nil {
			s.overlayCache.Put(overlay, []byte("1"), overlayHitTTL)
		}
		return overlay
	}
	if s.overlayCache != nil {
		s.overlayCache.Put(overlay, []byte("0"), overlayMissTTL)
	}
	logger.Debug("Poster overlay has no artwork for this title; keeping the source poster",
		"url", overlay, "status", resp.Status)
	return ""
}

// enrichExternalCatalogPreviews gives catalog-only imports the same artwork
// contract as native rows. It is bounded and cache-backed: every worker owns
// one preview slot, while the metadata client collapses repeat title requests.
func (s *Server) enrichExternalCatalogPreviews(ctx context.Context, metas []MetaPreview, contentType, lang string) {
	sem := make(chan struct{}, externalIDConcurrency)
	var wg sync.WaitGroup
	for i := range metas {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			s.fillPreviewFromMetadata(ctx, &metas[i], contentType, lang)
		}(i)
	}
	wg.Wait()
}

// animeSeriesIMDbID resolves a Kitsu id to its series-level IMDb id via the
// anime-lists mapping, or "" when unmapped.
func (s *Server) animeSeriesIMDbID(kitsuID string) string {
	if s.animeLists == nil {
		return ""
	}
	if mapping, ok := s.animeLists.LookupKitsu(kitsuID); ok {
		return mapping.IMDbID
	}
	return ""
}

// baseContentID collapses an attempt's request id ("tt123:1:5",
// "kitsu:486:3", bare TMDB numbers) to movie/series granularity in preview-id
// form, or "" when unrecognizable.
func baseContentID(rawID string) string {
	parsed := query.ParseContentID(strings.TrimSpace(rawID))
	switch {
	case parsed.KitsuID != "":
		return "kitsu:" + parsed.KitsuID
	case parsed.TVDBID != "":
		return "tvdb:" + parsed.TVDBID
	case parsed.IMDbID != "":
		return parsed.IMDbID
	case parsed.TMDBID != "":
		// A bare "0" splits fine but is not an id. The parser is deliberately
		// total and says nothing about whether a number is usable, so the
		// caller that cares checks — this one does.
		if n, err := strconv.Atoi(parsed.TMDBID); err != nil || n <= 0 {
			return ""
		}
		return "tmdb:" + parsed.TMDBID
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
			s.fillPreviewFromMetadata(ctx, &preview, def.Type, req.Profile.EffectiveLanguage())
			if preview.Name == "" {
				continue
			}
			previews = append(previews, preview)
		}
		if cap, capped := capForProfile(req.Profile); capped {
			previews = s.filterPreviewsByCertification(ctx, cap, previews, def.Type)
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
		s.fillPreviewFromMetadata(ctx, &preview, item.ContentType, req.Profile.EffectiveLanguage())
		if preview.Name == "" {
			preview.Name = item.ReleaseTitle
		}
		previews = append(previews, preview)
		if len(previews) >= catalogPageSize {
			break
		}
	}
	if cap, capped := capForProfile(req.Profile); capped {
		previews = s.filterPreviewsByCertification(ctx, cap, previews, def.Type)
	}
	return previews, nil
}

// becauseYouWatchedSeeds caps how many titles seed the recommendation row;
// each costs one (cached) TMDB recommendations request per page fetched.
const becauseYouWatchedSeeds = 3

// becauseYouWatchedWindow is how many recent distinct titles compete for the
// seed slots. Seeds are picked by play count blended with recency inside this
// window, so an ongoing binge keeps its slot instead of being evicted by
// every one-off play.
const becauseYouWatchedWindow = 10

// becauseYouWatchedMaxPages bounds how many recommendation pages per seed a
// deep scroll may pull; pages beyond the first are fetched only while the
// requested skip window still needs previews.
const becauseYouWatchedMaxPages = 3

// watchHistory returns the requesting stream's watched preview ids in recency
// order (per-stream playback history first, shared library as the fallback
// ordering), each id's play count (library-only entries stay at zero), plus a
// membership set that always folds the library in — a recommendation row must
// not re-offer what is already at hand.
func (s *Server) watchHistory(streamName, contentType string) (ordered []string, plays map[string]int, watched map[string]bool) {
	watched = make(map[string]bool)
	plays = make(map[string]int)
	if s.attemptRecorder != nil {
		rows, _ := s.attemptRecorder.RecentPlayedContent(streamName, contentType, 300)
		for _, row := range rows {
			id := baseContentID(row.ContentID)
			if id == "" {
				continue
			}
			plays[id]++
			if !watched[id] {
				watched[id] = true
				ordered = append(ordered, id)
			}
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
	return ordered, plays, watched
}

// becauseYouWatchedCatalog builds a recommendation row from the requesting
// stream's watch history: TMDB's recommendations for the highest-scoring
// recent titles, interleaved so no single seed dominates, minus everything
// already watched or in the library. Deep skips lazily pull further
// recommendation pages instead of ending the row at one page per seed.
func (s *Server) becauseYouWatchedCatalog(ctx context.Context, def CatalogDef, req catalogRequest) ([]MetaPreview, error) {
	rt := s.runtime()
	ordered, plays, watched := s.watchHistory(req.StreamName, def.Type)
	if len(ordered) == 0 {
		return nil, nil
	}
	mediaType := "tv"
	if def.Type == "movie" {
		mediaType = "movie"
	}

	// watchedTMDB mirrors the watched set in TMDB id space, so a
	// recommendation whose IMDb resolution fails — and therefore falls back
	// to a tmdb: preview id — still can't re-offer a watched title. It fills
	// for free: parsed tmdb: preview ids plus every candidate resolved below.
	watchedTMDB := make(map[int]bool)
	for id := range watched {
		if raw, ok := strings.CutPrefix(id, "tmdb:"); ok {
			if n, _ := strconv.Atoi(raw); n > 0 {
				watchedTMDB[n] = true
			}
		}
	}

	// Score the recent window instead of taking the newest three outright:
	// play count (a binge is many attempt rows) plus a recency bonus for the
	// newest few, so one rewatch of something old doesn't reshuffle the
	// whole row.
	type seed struct {
		tmdbID    int
		score     int
		exhausted bool
	}
	var candidates []*seed
	seenCandidates := make(map[int]bool)
	for idx, id := range ordered {
		if len(candidates) >= becauseYouWatchedWindow {
			break
		}
		if ctx.Err() != nil {
			break
		}
		tmdbID := s.tmdbIDForPreviewID(id, def.Type)
		if tmdbID <= 0 || seenCandidates[tmdbID] {
			continue
		}
		seenCandidates[tmdbID] = true
		watchedTMDB[tmdbID] = true
		score := plays[id]
		if bonus := becauseYouWatchedSeeds - idx; bonus > 0 {
			score += bonus
		}
		candidates = append(candidates, &seed{tmdbID: tmdbID, score: score})
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	seeds := candidates
	if len(seeds) > becauseYouWatchedSeeds {
		seeds = seeds[:becauseYouWatchedSeeds]
	}

	// One recommendation page per seed per round, interleaved round-robin so
	// the row mixes tastes instead of leading with 20 titles like the top
	// seed. Later rounds fetch the next page of each non-exhausted seed, and
	// only run while the skip window still needs previews — deterministic
	// across requests, so pagination stays coherent.
	needed := req.Skip + catalogPageSize
	seenTMDB := make(map[int]bool)
	var previews []MetaPreview
	for page := 1; page <= becauseYouWatchedMaxPages && len(previews) < needed && ctx.Err() == nil; page++ {
		perSeed := make([][]tmdb.SearchMultiResult, 0, len(seeds))
		for _, sd := range seeds {
			if sd.exhausted {
				continue
			}
			if ctx.Err() != nil {
				break
			}
			resp, err := rt.tmdbClient.GetRecommendations(mediaType, sd.tmdbID, page, req.Profile.EffectiveLanguage())
			if err != nil {
				logger.Debug("TMDB recommendations failed", "tmdb_id", sd.tmdbID, "err", err)
				sd.exhausted = true
				continue
			}
			if page >= resp.TotalPages || len(resp.Results) == 0 {
				sd.exhausted = true
			}
			perSeed = append(perSeed, resp.Results)
		}
		if len(perSeed) == 0 {
			break
		}
		if ctx.Err() != nil {
			// A call already in flight when the deadline passed can still
			// return real results, since GetRecommendations does not accept
			// ctx. Stop here rather than paying for the enrichment below
			// (filterTMDBResults, resolveIMDbIDs) on a page the deadline has
			// already ended.
			break
		}

		var merged []tmdb.SearchMultiResult
		for round := 0; ; round++ {
			advanced := false
			for _, results := range perSeed {
				if round >= len(results) {
					continue
				}
				advanced = true
				res := results[round]
				if res.ID <= 0 || seenTMDB[res.ID] || watchedTMDB[res.ID] {
					continue
				}
				seenTMDB[res.ID] = true
				merged = append(merged, res)
			}
			if !advanced {
				break
			}
		}

		if cap, capped := capForProfile(req.Profile); capped {
			merged = s.filterTMDBResults(mediaType, merged, cap)
		}

		ids := s.resolveIMDbIDs(mediaType, merged)
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
			if res.BackdropPath != "" {
				preview.Background = tmdbBackdropURL + res.BackdropPath
			}
			previews = append(previews, preview)
		}
	}

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
	rt := s.runtime()
	if raw, ok := strings.CutPrefix(previewID, "tmdb:"); ok {
		id, _ := strconv.Atoi(raw)
		return id
	}
	if strings.HasPrefix(previewID, "tt") {
		if find, err := rt.tmdbClient.Find(previewID, "imdb_id"); err == nil {
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
func (s *Server) higherRankedCatalogIDs(ctx context.Context, profile *config.MetadataProfileConfig, current CatalogDef) map[string]bool {
	ids := make(map[string]bool)
	for _, def := range enabledCatalogDefs(profile) {
		if def.ID == current.ID {
			break
		}
		if def.Type != current.Type {
			continue
		}
		// User-pasted catalogs are intentionally excluded from cross-board
		// deduplication. Never fetch an uncached third-party catalog merely to
		// build the duplicate set for a built-in board row.
		if def.Provider == "external" {
			continue
		}
		// Best-effort de-duplication only: once the request's own deadline is
		// gone, stop paying for more of it. A higher catalog that never
		// checks ctx itself (tmdbCatalog and tvdbCatalog both discard it
		// today) can otherwise burn the whole budget on its own, leaving
		// nothing for the catalog actually being served.
		if ctx.Err() != nil {
			break
		}
		metas, err := s.buildCatalog(ctx, def, catalogRequest{Type: def.Type, ID: def.ID, Profile: profile})
		if err != nil {
			continue
		}
		for _, preview := range metas {
			ids[preview.ID] = true
		}
	}
	return ids
}

// higherRankedSearchKeys collects the ids a search carrier ranked above
// current already answers this query with, so one title reaches the client
// once rather than once per carrier. Only carriers that outrank current are
// fetched: the winning side never pays for the losing side's query.
func (s *Server) higherRankedSearchKeys(ctx context.Context, profile *config.MetadataProfileConfig, current CatalogDef, search string) map[string]bool {
	keys := make(map[string]bool)
	for _, def := range searchCatalogDefs(profile) {
		if def.ID == current.ID || !searchCarrierOutranks(profile, def, current) {
			continue
		}
		// Same best-effort budget as the board rows: once the request's own
		// deadline is gone, stop paying for a de-duplication pass.
		if ctx.Err() != nil {
			break
		}
		metas, err := s.buildCatalog(ctx, def, catalogRequest{Type: def.Type, ID: def.ID, Search: search, Profile: profile})
		if err != nil {
			continue
		}
		canonical := s.canonicalKeysFor(def.Type)
		for _, preview := range metas {
			for _, key := range canonical(preview) {
				keys[key] = true
			}
		}
	}
	return keys
}

// searchCarrierOutranks decides which of two search carriers keeps a title
// they both return. Carriers of the same content type never collide — there
// is exactly one per type — so the only overlap is an anime entry against the
// aired series or film it belongs to, and the profile's anime priority list is
// what ranks those sources. A source the list does not name ranks last, which
// is why a TMDB series hit yields to whichever anime source the profile leads
// with.
func searchCarrierOutranks(profile *config.MetadataProfileConfig, def, current CatalogDef) bool {
	if (def.Type == "anime") == (current.Type == "anime") {
		return false
	}
	order := profile.EffectiveAnimeMetaSources()
	defRank, currentRank := metaSourceRank(order, def.Provider), metaSourceRank(order, current.Provider)
	if defRank != currentRank {
		return defRank < currentRank
	}
	// One source carrying both carriers (TVDB leading anime *and* series)
	// ranks equally against itself, so the media type settles it: an anime
	// entry belongs to the anime carrier, which is the one keeping Kitsu's
	// playback ids.
	return def.Type == "anime"
}

// metaSourceRank is a provider's position in a priority list; one past the end
// for a provider the list does not name.
func metaSourceRank(order []string, provider string) int {
	for i, source := range order {
		if source == provider {
			return i
		}
	}
	return len(order)
}

// canonicalKeysFor identifies previews of one catalog across provider id
// spaces. kind is the media kind that catalog's own ids belong to, which TMDB
// ids need: it numbers films and series separately, so "tmdb:5" names two
// different titles depending on which list it came from.
func (s *Server) canonicalKeysFor(kind string) func(MetaPreview) []string {
	return func(preview MetaPreview) []string {
		if kitsuID, ok := strings.CutPrefix(preview.ID, "kitsu:"); ok {
			return s.animeEntryKeys(preview.ID, kitsuID)
		}
		if tmdbID, ok := strings.CutPrefix(preview.ID, "tmdb:"); ok {
			return []string{tmdbKey(kind, tmdbID)}
		}
		return []string{preview.ID}
	}
}

// animeEntryKeys are the ids an anime entry is known by outside Kitsu. The
// aired-series catalogs never use a Kitsu id, so the anime-lists mapping is
// what makes "the same show" decidable across sources. Every cour maps onto
// the same series ids, which is the point: the side that loses the priority
// contest drops all of its entries for that series, not just the one that
// happened to match.
func (s *Server) animeEntryKeys(previewID, kitsuID string) []string {
	keys := []string{previewID}
	if s.animeLists == nil {
		return keys
	}
	mapping, ok := s.animeLists.LookupKitsu(kitsuID)
	if !ok {
		return keys
	}
	if mapping.IMDbID != "" {
		keys = append(keys, mapping.IMDbID)
	}
	if mapping.TMDBID != "" {
		kind := "series"
		if strings.EqualFold(mapping.Type, "movie") {
			kind = "movie"
		}
		keys = append(keys, tmdbKey(kind, mapping.TMDBID))
	}
	if mapping.TVDBID != "" {
		keys = append(keys, "tvdb:"+mapping.TVDBID)
	}
	return keys
}

// tmdbKey qualifies a TMDB id with the list it was drawn from.
func tmdbKey(kind, id string) string {
	if kind == "movie" {
		return "tmdb:movie:" + id
	}
	return "tmdb:tv:" + id
}

// previewIDKeys is the board rows' identity: the catalog id itself. Rows of
// one type share an id space already, and expanding a Kitsu id to its series
// ids there would collapse the separate cours a Kitsu-backed row exists to
// show.
func previewIDKeys(preview MetaPreview) []string {
	return []string{preview.ID}
}

// filterHigherRankedDuplicates drops previews a higher-ranked catalog already
// shows. keys names the ids a preview can be recognised by.
func filterHigherRankedDuplicates(metas []MetaPreview, higher map[string]bool, keys func(MetaPreview) []string) []MetaPreview {
	if len(higher) == 0 {
		return metas
	}
	filtered := metas[:0]
	for _, preview := range metas {
		duplicate := false
		for _, key := range keys(preview) {
			if higher[key] {
				duplicate = true
				break
			}
		}
		if !duplicate {
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

// fillPreviewFromMetadata resolves display name, poster and background for a
// preview stub through the cached metadata clients, keyed off the preview id.
// A stub whose resolution fails keeps whatever name it already carries. lang
// is the profile's display language tag ("" for the English default).
func (s *Server) fillPreviewFromMetadata(ctx context.Context, preview *MetaPreview, contentType, lang string) {
	rt := s.runtime()
	if kitsuID, ok := strings.CutPrefix(preview.ID, "kitsu:"); ok {
		if animeMeta, err := s.kitsuClient.GetAnimeMeta(ctx, kitsuID); err == nil && animeMeta.CanonicalTitle != "" {
			preview.Name = kitsu.DisplayTitle(lang, animeMeta.EnglishTitle, animeMeta.CanonicalTitle)
			preview.Poster = animeMeta.PosterImage
			preview.Background = animeMeta.CoverImage
		}
		return
	}
	if tvdbID, ok := strings.CutPrefix(preview.ID, "tvdb:"); ok {
		if ext, err := rt.tvdbClient.GetSeriesExtendedTranslated(tvdbID, tvdb.LanguageToISO3(lang)); err == nil && ext.Name != "" {
			preview.Name = ext.Name
			preview.Poster = ext.Image
			preview.Background = ext.Background()
		}
		return
	}
	tmdbID := 0
	if raw, ok := strings.CutPrefix(preview.ID, "tmdb:"); ok {
		tmdbID, _ = strconv.Atoi(raw)
	} else if strings.HasPrefix(preview.ID, "tt") {
		if find, err := rt.tmdbClient.Find(preview.ID, "imdb_id"); err == nil {
			if res, ok := pickFindResult(find, contentType); ok {
				tmdbID = res.ID
			}
		}
	}
	if tmdbID == 0 {
		return
	}
	if contentType == "movie" {
		if details, err := rt.tmdbClient.GetMovieDetailsWithLanguage(tmdbID, lang); err == nil && details.Title != "" {
			preview.Name = details.Title
			if details.PosterPath != "" {
				preview.Poster = tmdbPosterURL + details.PosterPath
			}
			if details.BackdropPath != "" {
				preview.Background = tmdbBackdropURL + details.BackdropPath
			}
		}
		return
	}
	if details, err := rt.tmdbClient.GetTVDetailsWithLanguage(tmdbID, lang); err == nil && details.Name != "" {
		preview.Name = details.Name
		if details.PosterPath != "" {
			preview.Poster = tmdbPosterURL + details.PosterPath
		}
		if details.BackdropPath != "" {
			preview.Background = tmdbBackdropURL + details.BackdropPath
		}
	}
}
