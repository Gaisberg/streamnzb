package jellyfin

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/server/stremio"
)

// A Jellyfin library is a catalog: each enabled browse catalog of the
// stream's metadata profile is one library folder, its rows are the
// folder's items, and paging walks the catalog's pages. Search runs the
// hidden search carriers the addon uses for Stremio's search screen.

const (
	// defaultLimit is the page a client gets when it does not say; maxLimit
	// bounds how many catalog pages one request may pull.
	defaultLimit = 100
	maxLimit     = 200
	latestLimit  = 16
	resumeLimit  = 12
)

// allCatalogs is every catalog an id may name: the browse registry plus the
// search carriers.
func allCatalogs() []stremio.CatalogDef {
	return append(stremio.CatalogRegistry(), stremio.SearchCatalogs()...)
}

func catalogByID(id string) (stremio.CatalogDef, bool) {
	for _, def := range allCatalogs() {
		if def.ID == id {
			return def, true
		}
	}
	return stremio.CatalogDef{}, false
}

// videosOf is a series' episode list. A Kitsu movie has none, and is shown
// as a one-episode series rather than switching type between the catalog
// row and the detail page; its episode plays under the bare entry id.
func videosOf(meta *stremio.MetaObject) []stremio.MetaVideo {
	if meta == nil {
		return nil
	}
	if len(meta.Videos) == 0 && meta.Type == "anime" {
		return []stremio.MetaVideo{{ID: meta.ID, Title: meta.Name, Season: 1, Episode: 0, Released: meta.Released, Thumbnail: meta.Background}}
	}
	return meta.Videos
}

type seasonSummary struct {
	Number   int
	Episodes int
}

// seasonsOf lists a series' seasons in order, specials last.
func seasonsOf(meta *stremio.MetaObject) []seasonSummary {
	counts := map[int]int{}
	for _, v := range videosOf(meta) {
		counts[v.Season]++
	}
	out := make([]seasonSummary, 0, len(counts))
	for n, c := range counts {
		out = append(out, seasonSummary{Number: n, Episodes: c})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Number, out[j].Number
		if (a == 0) != (b == 0) {
			return b == 0
		}
		return a < b
	})
	return out
}

// wantsType reports whether an includeItemTypes filter admits a content
// type. No filter admits everything.
func wantsType(include []string, contentType string) bool {
	if len(include) == 0 {
		return true
	}
	want := "series"
	if contentType == "movie" {
		want = "movie"
	}
	for _, t := range include {
		if t == want {
			return true
		}
	}
	return false
}

func (s *Server) serveItems(w http.ResponseWriter, rq *request) bool {
	if !rq.is(http.MethodGet) {
		return false
	}
	segs := rq.segs
	// The per-user forms are the same routes with two leading segments.
	if len(segs) >= 2 && segs[0] == "users" {
		segs = segs[2:]
	}
	join := strings.Join(segs, "/")
	switch {
	case join == "userviews" || join == "views" || join == "library/mediafolders":
		s.handleViews(w, rq)
	case join == "userviews/groupingoptions" || join == "groupingoptions":
		writeJSON(w, http.StatusOK, []any{})
	case join == "items":
		s.handleItems(w, rq)
	case join == "items/latest":
		s.handleLatest(w, rq)
	case join == "items/resume" || join == "useritems/resume":
		writeJSON(w, http.StatusOK, s.resumeResult(rq))
	case join == "items/filters" || join == "items/filters2":
		writeJSON(w, http.StatusOK, map[string]any{"Genres": []any{}, "Tags": []any{}, "OfficialRatings": []any{}, "Years": []any{}})
	case join == "items/suggestions":
		// The home screen's suggestion row. What Jellyfin suggests from watch
		// history is already served here as its own "Because You Watched"
		// library, so the row itself is answered empty rather than repeating
		// a library inside the home screen.
		writeJSON(w, http.StatusOK, emptyResult())
	case len(segs) == 2 && segs[0] == "mediasegments":
		// Intro and credit markers, a 12.0 route. Nothing here knows where a
		// release's intro is, so the answer is an empty set rather than a 404
		// on a version this layer claims to be.
		writeJSON(w, http.StatusOK, emptyResult())
	case join == "items/counts":
		writeJSON(w, http.StatusOK, map[string]int{"MovieCount": 0, "SeriesCount": 0, "EpisodeCount": 0})
	case join == "search/hints":
		s.handleSearchHints(w, rq)
	case len(segs) == 2 && segs[0] == "items":
		// Only an id belongs here. A word this layer has no route for is
		// declined rather than answered 404, so it reaches the fallback and
		// is logged by name — that is how a missing route gets found.
		if _, err := decodeItemID(segs[1]); err != nil {
			return false
		}
		s.handleItem(w, rq, segs[1])
	case len(segs) == 3 && segs[0] == "items":
		switch segs[2] {
		case "similar", "intros":
			writeJSON(w, http.StatusOK, emptyResult())
		case "ancestors", "specialfeatures", "localtrailers", "externalidinfos", "additionalparts":
			writeJSON(w, http.StatusOK, []any{})
		case "thememedia":
			writeJSON(w, http.StatusOK, map[string]any{"ThemeVideosResult": emptyResult(), "ThemeSongsResult": emptyResult(), "SoundtrackSongsResult": emptyResult()})
		default:
			return false
		}
	default:
		return false
	}
	return true
}

func (s *Server) views(rq *request) ([]stremio.CatalogDef, error) {
	defs, err := s.opts.Catalog.EnabledCatalogs(rq.stream)
	if errors.Is(err, stremio.ErrMetadataDisabled) {
		return nil, nil
	}
	return defs, err
}

func (s *Server) handleViews(w http.ResponseWriter, rq *request) {
	defs, err := s.views(rq)
	if err != nil {
		logger.Warn("Jellyfin views failed", "stream", rq.streamName(), "err", err)
		writeJSON(w, http.StatusOK, emptyResult())
		return
	}
	result := emptyResult()
	for _, def := range defs {
		result.Items = append(result.Items, s.viewItem(def))
	}
	result.TotalRecordCount = len(result.Items)
	writeJSON(w, http.StatusOK, result)
}

// handleItems serves the listing route: a catalog's rows, a series' seasons
// or episodes, a search, a lookup by ids, or the resumable set.
func (s *Server) handleItems(w http.ResponseWriter, rq *request) {
	start := max(rq.intParam("startIndex", 0), 0)
	limit := rq.intParam("limit", defaultLimit)
	if limit <= 0 || limit > maxLimit {
		limit = min(max(limit, defaultLimit), maxLimit)
	}
	include := rq.listParam("includeItemTypes")

	if ids := rq.listParam("ids"); len(ids) > 0 {
		result := emptyResult()
		for _, raw := range ids {
			if item := s.itemByRawID(rq, raw); item != nil {
				result.Items = append(result.Items, item)
			}
		}
		result.TotalRecordCount = len(result.Items)
		writeJSON(w, http.StatusOK, result)
		return
	}
	if term := rq.param("searchTerm"); term != "" {
		items := s.search(rq, term, include)
		writeJSON(w, http.StatusOK, page(items, start, limit, true))
		return
	}
	for _, f := range rq.listParam("filters") {
		if f == "isresumable" {
			writeJSON(w, http.StatusOK, s.resumeResult(rq))
			return
		}
	}

	parent := rq.param("parentId")
	if parent == "" {
		items := s.allItems(rq, include)
		writeJSON(w, http.StatusOK, page(items, start, limit, true))
		return
	}
	id, err := decodeItemID(parent)
	if err != nil {
		writeJSON(w, http.StatusOK, emptyResult())
		return
	}
	switch id.Kind {
	case kindView:
		def, ok := catalogByID(id.CatalogID)
		if !ok || !wantsType(include, def.Type) {
			writeJSON(w, http.StatusOK, emptyResult())
			return
		}
		writeJSON(w, http.StatusOK, s.catalogPage(rq, def, start, limit))
	case kindSeries, kindSeason:
		items, err := s.childrenOf(rq, id, include, rq.boolParam("recursive"))
		if err != nil {
			s.writeItemError(w, rq, parent, err)
			return
		}
		writeJSON(w, http.StatusOK, page(items, start, limit, false))
	default:
		writeJSON(w, http.StatusOK, emptyResult())
	}
}

// page slices an in-memory listing. open marks a listing whose full size
// is unknown, where a full page implies there may be more.
func page(items []*baseItem, start, limit int, open bool) queryResult {
	result := emptyResult()
	result.StartIndex = start
	result.TotalRecordCount = len(items)
	if start < len(items) {
		end := min(start+limit, len(items))
		result.Items = items[start:end]
	}
	if open && len(result.Items) == limit {
		result.TotalRecordCount = start + limit + stremio.CatalogPageSize
	}
	return result
}

// catalogPage serves rows [start, start+limit) of a catalog. The addon pages
// in fixed buckets, so the request is mapped onto whole buckets and sliced;
// a client whose page size is a multiple of the bucket lands exactly on
// bucket edges, which is what every known client does.
//
// Positions are the addon's, not the row count served: a bucket comes back
// short whenever the provider had rows the addon could not identify, so a
// page may carry fewer rows than asked while the catalog goes on. Only an
// empty bucket ends the catalog. When the last bucket in range is short the
// next one is probed for that answer, so a true last page still reports an
// exact total.
func (s *Server) catalogPage(rq *request, def stremio.CatalogDef, start, limit int) queryResult {
	result := emptyResult()
	result.StartIndex = start
	if start > 0 && !def.SupportsSkip {
		result.TotalRecordCount = start
		return result
	}
	bucket := stremio.CatalogPageSize
	firstPage, lastPage := start/bucket, (start+limit-1)/bucket
	parent := viewID(def.ID)
	ended := false
	for p := firstPage; p <= lastPage; p++ {
		metas, err := s.opts.Catalog.Catalog(rq.Context(), rq.stream, def.ID, def.Type, "", p*bucket)
		if err != nil {
			logger.Debug("Jellyfin catalog page failed", "catalog", def.ID, "skip", p*bucket, "err", err)
			ended = true
			break
		}
		if len(metas) == 0 {
			ended = true
			break
		}
		// Row j of bucket p sits at position p*bucket+j whatever the bucket
		// lost, so a window is the same rows on every visit.
		for j, preview := range metas {
			pos := p*bucket + j
			if pos < start || pos >= start+limit {
				continue
			}
			if item, ok := s.previewItem(preview, parent); ok {
				result.Items = append(result.Items, item)
			}
		}
		if !def.SupportsSkip {
			ended = true
			break
		}
		if p == lastPage && len(metas) < bucket {
			ended = s.catalogEndsAt(rq, def, (p+1)*bucket)
		}
	}
	result.TotalRecordCount = start + len(result.Items)
	if !ended {
		// Open-ended, as page() reports it: past the window the client
		// asked for, so a page thinned by dropped rows still reads as
		// "more to come" rather than as the last one.
		result.TotalRecordCount = start + limit + bucket
	}
	return result
}

// catalogEndsAt reports whether the bucket at skip is empty, which is the
// only signal the addon gives that a catalog is exhausted.
func (s *Server) catalogEndsAt(rq *request, def stremio.CatalogDef, skip int) bool {
	metas, err := s.opts.Catalog.Catalog(rq.Context(), rq.stream, def.ID, def.Type, "", skip)
	if err != nil {
		logger.Debug("Jellyfin catalog page failed", "catalog", def.ID, "skip", skip, "err", err)
		return true
	}
	return len(metas) == 0
}

// allItems is the listing without a folder — "everything of this type",
// which a catalog server has no way to enumerate. It is the first page of
// every matching catalog, deduplicated, so the view is populated rather than
// empty.
func (s *Server) allItems(rq *request, include []string) []*baseItem {
	defs, err := s.views(rq)
	if err != nil {
		return nil
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	rows := make([][]stremio.MetaPreview, len(defs))
	for i, def := range defs {
		if !wantsType(include, def.Type) {
			continue
		}
		wg.Add(1)
		go func(i int, def stremio.CatalogDef) {
			defer wg.Done()
			metas, err := s.opts.Catalog.Catalog(rq.Context(), rq.stream, def.ID, def.Type, "", 0)
			if err != nil {
				return
			}
			mu.Lock()
			rows[i] = metas
			mu.Unlock()
		}(i, def)
	}
	wg.Wait()
	var items []*baseItem
	seen := map[string]bool{}
	for i, def := range defs {
		parent := viewID(def.ID)
		for _, preview := range rows[i] {
			item, ok := s.previewItem(preview, parent)
			if !ok || seen[item.ID] {
				continue
			}
			seen[item.ID] = true
			items = append(items, item)
		}
	}
	return items
}

// search runs the search carriers the filter admits, in parallel.
func (s *Server) search(rq *request, term string, include []string) []*baseItem {
	defs := stremio.SearchCatalogs()
	var wg sync.WaitGroup
	rows := make([][]stremio.MetaPreview, len(defs))
	for i, def := range defs {
		if !wantsType(include, def.Type) {
			continue
		}
		wg.Add(1)
		go func(i int, def stremio.CatalogDef) {
			defer wg.Done()
			metas, err := s.opts.Catalog.Catalog(rq.Context(), rq.stream, def.ID, def.Type, term, 0)
			if err != nil && !errors.Is(err, stremio.ErrMetadataDisabled) {
				logger.Debug("Jellyfin search failed", "catalog", def.ID, "err", err)
			}
			rows[i] = metas
		}(i, def)
	}
	wg.Wait()
	var items []*baseItem
	seen := map[string]bool{}
	for _, metas := range rows {
		for _, preview := range metas {
			item, ok := s.previewItem(preview, "")
			if !ok || seen[item.ID] {
				continue
			}
			seen[item.ID] = true
			items = append(items, item)
		}
	}
	return items
}

type searchHint struct {
	ItemID    string `json:"ItemId"`
	ID        string `json:"Id"`
	Name      string `json:"Name"`
	Type      string `json:"Type"`
	MediaType string `json:"MediaType,omitempty"`
	IsFolder  bool   `json:"IsFolder"`
	// Artists is always empty — there is no music here — but required.
	Artists             []string `json:"Artists"`
	ProductionYear      *int     `json:"ProductionYear,omitempty"`
	PrimaryImageTag     string   `json:"PrimaryImageTag,omitempty"`
	BackdropImageTag    string   `json:"BackdropImageTag,omitempty"`
	BackdropImageItemID string   `json:"BackdropImageItemId,omitempty"`
}

func (s *Server) handleSearchHints(w http.ResponseWriter, rq *request) {
	term := rq.param("searchTerm")
	hints := []searchHint{}
	if term != "" {
		limit := rq.intParam("limit", defaultLimit)
		for _, item := range s.search(rq, term, rq.listParam("includeItemTypes")) {
			if limit > 0 && len(hints) >= limit {
				break
			}
			hint := searchHint{ItemID: item.ID, ID: item.ID, Name: item.Name, Type: item.Type, MediaType: item.MediaType, IsFolder: item.IsFolder, Artists: []string{}, ProductionYear: item.ProductionYear, PrimaryImageTag: item.ImageTags["Primary"]}
			if len(item.BackdropImageTags) > 0 {
				hint.BackdropImageTag, hint.BackdropImageItemID = item.BackdropImageTags[0], item.ID
			}
			hints = append(hints, hint)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"SearchHints": hints, "TotalRecordCount": len(hints)})
}

// handleLatest serves the home-screen "Latest" strip of a library: the top
// of the catalog. The response is a bare array, per the Jellyfin route.
func (s *Server) handleLatest(w http.ResponseWriter, rq *request) {
	limit := rq.intParam("limit", latestLimit)
	if limit <= 0 || limit > maxLimit {
		limit = latestLimit
	}
	include := rq.listParam("includeItemTypes")
	items := []*baseItem{}
	if parent := rq.param("parentId"); parent != "" {
		if id, err := decodeItemID(parent); err == nil && id.Kind == kindView {
			if def, ok := catalogByID(id.CatalogID); ok && wantsType(include, def.Type) {
				items = s.catalogPage(rq, def, 0, limit).Items
			}
		}
	} else {
		all := s.allItems(rq, include)
		items = all[:min(limit, len(all))]
	}
	writeJSON(w, http.StatusOK, items)
}

// resumeResult lists what the stream has partway through, newest first.
func (s *Server) resumeResult(rq *request) queryResult {
	result := emptyResult()
	if s.opts.Playstate == nil {
		return result
	}
	limit := rq.intParam("limit", resumeLimit)
	if limit <= 0 || limit > maxLimit {
		limit = resumeLimit
	}
	include := rq.listParam("includeItemTypes")
	for _, state := range s.opts.Playstate.ListResume(rq.streamName(), limit) {
		id, err := decodeItemID(state.ItemID)
		if err != nil {
			continue
		}
		if len(include) > 0 {
			want := "episode"
			if id.Kind == kindMovie {
				want = "movie"
			}
			admitted := false
			for _, t := range include {
				admitted = admitted || t == want
			}
			if !admitted {
				continue
			}
		}
		item, err := s.itemByID(rq, id)
		if err != nil || item == nil {
			continue
		}
		result.Items = append(result.Items, item)
	}
	result.TotalRecordCount = len(result.Items)
	return result
}

func (s *Server) handleItem(w http.ResponseWriter, rq *request, raw string) {
	id, err := decodeItemID(raw)
	if err != nil {
		http.NotFound(w, rq.Request)
		return
	}
	item, err := s.itemByID(rq, id)
	if err != nil {
		s.writeItemError(w, rq, raw, err)
		return
	}
	if item == nil {
		http.NotFound(w, rq.Request)
		return
	}
	s.resolveOnOpen(rq, id, item)
	writeJSON(w, http.StatusOK, item)
}

// resolveOnOpen runs the search immediately when a title page is opened,
// for clients (SenPlayer) that build their version picker from the item
// document's MediaSources rather than asking PlaybackInfo. It is opt-in —
// unlike PlaybackInfo, nothing else forces this search to run — and only
// fires here, on the single-item route: a list, Resume or NextUp page would
// mean one search per row.
func (s *Server) resolveOnOpen(rq *request, id itemID, item *baseItem) {
	if s.opts.ResolveOnOpen == nil || !s.opts.ResolveOnOpen() {
		return
	}
	if id.Kind != kindMovie && id.Kind != kindEpisode {
		return
	}
	if _, cached := s.opts.Catalog.PlaylistCached(rq.stream, id.ContentType, id.playStremioID()); cached {
		return
	}
	view, err := s.opts.Catalog.Playlist(rq.Context(), rq.stream, id.ContentType, id.playStremioID())
	if err != nil || view == nil || len(view.Entries) == 0 {
		logger.Debug("Jellyfin resolve on open failed", "item", id.playStremioID(), "stream", rq.streamName(), "err", err)
		return
	}
	setItemMediaSources(item, s.renderedSources(rq, id, view))
}

func (s *Server) writeItemError(w http.ResponseWriter, rq *request, raw string, err error) {
	if errors.Is(err, stremio.ErrMetadataDisabled) {
		http.NotFound(w, rq.Request)
		return
	}
	logger.Warn("Jellyfin item lookup failed", "item", raw, "stream", rq.streamName(), "err", err)
	http.Error(w, "Metadata lookup failed", http.StatusBadGateway)
}

func (s *Server) itemByRawID(rq *request, raw string) *baseItem {
	id, err := decodeItemID(raw)
	if err != nil {
		return nil
	}
	item, err := s.itemByID(rq, id)
	if err != nil {
		return nil
	}
	return item
}

// meta fetches the movie or series behind an id.
func (s *Server) meta(ctx context.Context, rq *request, id itemID) (*stremio.MetaObject, error) {
	meta, err := s.opts.Catalog.Meta(ctx, rq.stream, id.ContentType, id.baseStremioID())
	if err != nil {
		return nil, err
	}
	if meta == nil {
		return nil, errors.New("no metadata")
	}
	return meta, nil
}

// itemByID renders any item the id names. nil with no error is an id that
// decodes but names nothing (an episode the series does not have).
func (s *Server) itemByID(rq *request, id itemID) (*baseItem, error) {
	switch id.Kind {
	case kindView:
		def, ok := catalogByID(id.CatalogID)
		if !ok {
			return nil, nil
		}
		return s.viewItem(def), nil
	case kindSource:
		return s.itemByID(rq, id.playable())
	}
	meta, err := s.meta(rq.Context(), rq, id)
	if err != nil {
		return nil, err
	}
	switch id.Kind {
	case kindMovie:
		item := s.metaItem(id, meta)
		item.UserData = s.userData(rq, id)
		s.attachMediaSources(rq, id, item)
		return item, nil
	case kindSeries:
		item := s.metaItem(id, meta)
		item.UserData = &userData{Key: item.ID, ItemID: item.ID}
		return item, nil
	case kindSeason:
		for _, season := range seasonsOf(meta) {
			if season.Number == id.Season {
				item := s.seasonItem(id.series(), meta, season.Number, season.Episodes)
				item.UserData = &userData{Key: item.ID, ItemID: item.ID}
				return item, nil
			}
		}
		return nil, nil
	case kindEpisode:
		for _, v := range videosOf(meta) {
			if v.Season == id.Season && v.Episode == id.Episode {
				item := s.episodeItem(id.series(), meta, v)
				item.UserData = s.userData(rq, id)
				s.attachMediaSources(rq, id, item)
				return item, nil
			}
		}
		return nil, nil
	}
	return nil, nil
}

// childrenOf lists a series' seasons, or its episodes when the client asks
// for episodes (or recurses), and a season's episodes.
func (s *Server) childrenOf(rq *request, id itemID, include []string, recursive bool) ([]*baseItem, error) {
	meta, err := s.meta(rq.Context(), rq, id)
	if err != nil {
		return nil, err
	}
	wantEpisodes := recursive || id.Kind == kindSeason
	for _, t := range include {
		if t == "episode" {
			wantEpisodes = true
		}
	}
	series := id.series()
	var items []*baseItem
	if !wantEpisodes {
		for _, season := range seasonsOf(meta) {
			item := s.seasonItem(series, meta, season.Number, season.Episodes)
			item.UserData = &userData{Key: item.ID, ItemID: item.ID}
			items = append(items, item)
		}
		return items, nil
	}
	// One query for the whole listing rather than one per episode.
	states := map[string]persistence.JellyfinPlaystate{}
	if s.opts.Playstate != nil {
		for _, st := range s.opts.Playstate.ListForStream(rq.streamName()) {
			states[st.ItemID] = st
		}
	}
	for _, v := range videosOf(meta) {
		if id.Kind == kindSeason && v.Season != id.Season {
			continue
		}
		item := s.episodeItem(series, meta, v)
		st, ok := states[item.ID]
		item.UserData = userDataFor(item.ID, st, ok)
		items = append(items, item)
	}
	return items, nil
}

func (s *Server) userData(rq *request, id itemID) *userData {
	encoded := id.encode()
	if s.opts.Playstate == nil {
		return userDataFor(encoded, persistence.JellyfinPlaystate{}, false)
	}
	state, ok := s.opts.Playstate.Get(rq.streamName(), encoded)
	return userDataFor(encoded, state, ok)
}
