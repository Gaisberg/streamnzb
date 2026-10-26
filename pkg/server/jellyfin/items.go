package jellyfin

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/server/stremio"

	"golang.org/x/sync/errgroup"
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
	// Infuse uses Items/Latest as its catalog browser and otherwise never
	// follows the normal paged Items route. Keep its external-source response
	// bounded, but large enough to render the complete common public lists.
	infuseExternalLatestLimit = maxLimit
	// maxLibraryRows is where a library ends. A catalog has no end a client
	// can afford to reach — TMDB's listings run to 10,000 rows — and clients
	// that sync a library (UHF) page until the total says stop, building
	// every catalog page and every series' metadata on the way. Browsing
	// clients never scroll this far, so the cap costs them nothing.
	maxLibraryRows = 500
)

// allCatalogs is every catalog an id may name: the browse registry plus the
// search carriers.
func allCatalogs() []stremio.CatalogDef {
	// View ids are decoded before a request's stream/profile is known, so
	// every carrier any profile could select has to resolve here; the
	// profile-aware SearchCatalogs call below decides which one can serve.
	return append(stremio.CatalogRegistry(), stremio.AllSearchCatalogs()...)
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
	case join == "library/virtualfolders":
		s.handleVirtualFolders(w, rq)
	case join == "userviews/groupingoptions" || join == "groupingoptions":
		writeJSON(w, http.StatusOK, []any{})
	case join == "items":
		s.handleItems(w, rq)
	case join == "items/latest":
		s.handleLatest(w, rq)
	case join == "items/resume" || join == "useritems/resume":
		writeJSON(w, http.StatusOK, s.resumeResult(rq, false))
	case join == "items/filters" || join == "items/filters2":
		s.handleFilters(w, rq, join == "items/filters2")
	case join == "genres":
		s.handleGenres(w, rq)
	case len(segs) == 2 && segs[0] == "genres":
		s.handleGenre(w, rq, segs[1])
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

// enabledCatalogByID resolves against this request's profile rather than the
// static built-in registry. External catalog sources are profile-owned, so a
// static lookup would display their Jellyfin library but make it unopenable.
func (s *Server) enabledCatalogByID(rq *request, catalogID string) (stremio.CatalogDef, bool) {
	defs, err := s.views(rq)
	if err != nil {
		return stremio.CatalogDef{}, false
	}
	for _, def := range defs {
		if def.ID == catalogID || viewID(def.ID) == catalogID {
			return def, true
		}
	}
	return stremio.CatalogDef{}, false
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

// virtualFolder is Jellyfin's VirtualFolderInfo: a library as the server
// configuration describes it.
type virtualFolder struct {
	Name               string         `json:"Name"`
	Locations          []string       `json:"Locations"`
	CollectionType     string         `json:"CollectionType"`
	LibraryOptions     map[string]any `json:"LibraryOptions"`
	ItemID             string         `json:"ItemId"`
	PrimaryImageItemID string         `json:"PrimaryImageItemId"`
	RefreshStatus      string         `json:"RefreshStatus"`
}

// handleVirtualFolders lists the libraries in the configuration shape.
// Clients that sync a server (UHF) enumerate libraries here rather than
// through UserViews, so an empty answer leaves them with no library at all.
func (s *Server) handleVirtualFolders(w http.ResponseWriter, rq *request) {
	defs, err := s.views(rq)
	if err != nil {
		logger.Warn("Jellyfin virtual folders failed", "stream", rq.streamName(), "err", err)
	}
	folders := []virtualFolder{}
	for _, def := range defs {
		view := s.viewItem(def)
		folders = append(folders, virtualFolder{
			Name:               view.Name,
			Locations:          []string{view.Path},
			CollectionType:     view.CollectionType,
			LibraryOptions:     map[string]any{"Enabled": true, "EnableRealtimeMonitor": false, "PathInfos": []any{}},
			ItemID:             view.ID,
			PrimaryImageItemID: view.ID,
			RefreshStatus:      "Idle",
		})
	}
	writeJSON(w, http.StatusOK, folders)
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
	filters := rq.listParam("filters")
	// There are no likes here, so the web client's combined filter is the
	// favourites alone. It narrows every selector below, not just a listing.
	wantFavorites := rq.boolParam("isFavorite") || slices.Contains(filters, "isfavorite") || slices.Contains(filters, "isfavoriteorlikes")

	if ids := rq.listParam("ids"); len(ids) > 0 {
		result := emptyResult()
		if wantFavorites {
			// Rendered as the favourites listing renders them: itemByID
			// would search for a series under JELLYFIN_RESOLVE_ON_OPEN.
			favorites := s.favoriteStates(rq, include)
			var wanted []itemID
			for _, raw := range ids {
				// Checked before rendering, so a non-favourite costs nothing.
				if id, ok := favoriteID(raw); ok {
					if _, fav := favorites[id.encode()]; fav {
						wanted = append(wanted, id)
					}
				}
			}
			metas := s.prefetchMetas(rq, wanted)
			for _, id := range wanted {
				if item := s.favoriteRow(rq, id, metas); item != nil {
					item.UserData = userDataFor(item.ID, favorites[id.encode()], true)
					result.Items = append(result.Items, item)
				}
			}
		} else {
			for _, raw := range ids {
				if item := s.itemByRawID(rq, raw); item != nil {
					result.Items = append(result.Items, item)
				}
			}
		}
		result.TotalRecordCount = len(result.Items)
		writeJSON(w, http.StatusOK, result)
		return
	}
	if term := rq.param("searchTerm"); term != "" {
		items := s.search(rq, term, include)
		if wantFavorites {
			// The favourites among the matches are all of them, so the
			// count is exact rather than open-ended.
			favorites := s.favoriteStates(rq, include)
			kept := items[:0]
			for _, item := range items {
				if st, ok := favorites[item.ID]; ok {
					item.UserData = userDataFor(item.ID, st, true)
					kept = append(kept, item)
				}
			}
			writeJSON(w, http.StatusOK, page(kept, start, limit, false))
			return
		}
		writeJSON(w, http.StatusOK, page(items, start, limit, true))
		return
	}
	if slices.Contains(filters, "isresumable") {
		writeJSON(w, http.StatusOK, s.resumeResult(rq, wantFavorites))
		return
	}
	if wantFavorites {
		writeJSON(w, http.StatusOK, s.favoritesResult(rq, include, start, limit))
		return
	}
	if names, asked := s.requestedGenres(rq); asked {
		writeJSON(w, http.StatusOK, s.genreListing(rq, names, include, start, limit))
		return
	}

	parent := rq.param("parentId")
	if parent == "" && !rq.boolParam("recursive") {
		// No folder and no recursion is the user's root folder, whose
		// children are the libraries, as on a real Jellyfin server. Clients
		// that discover libraries this way (UHF) otherwise get catalog rows
		// where they expect folders, and no library ever loads.
		s.handleViews(w, rq)
		return
	}
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
		def, ok := s.enabledCatalogByID(rq, id.CatalogID)
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
// exact total. A window that reaches maxLibraryRows is the last page
// whatever the catalog holds past it.
func (s *Server) catalogPage(rq *request, def stremio.CatalogDef, start, limit int) queryResult {
	result := emptyResult()
	result.StartIndex = start
	if (start > 0 && !def.SupportsSkip) || start >= maxLibraryRows {
		result.TotalRecordCount = start
		return result
	}
	capped := start+limit >= maxLibraryRows
	limit = min(limit, maxLibraryRows-start)
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
			if item, ok := s.previewItem(rq, preview, parent); ok {
				result.Items = append(result.Items, item)
			}
		}
		if !def.SupportsSkip {
			ended = true
			break
		}
		if p == lastPage && len(metas) < bucket && !capped {
			ended = s.catalogEndsAt(rq, def, (p+1)*bucket)
		}
	}
	ended = ended || capped
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

// allItems is the recursive listing without a folder — "everything of this type",
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
				if !errors.Is(err, stremio.ErrMetadataDisabled) &&
					logger.Throttle("jellyfin-all-items:"+def.ID, 5*time.Minute) {
					logger.Warn("Jellyfin catalog failed; library omitted from listing",
						"catalog", def.ID, "stream", rq.streamName(), "err", err)
				}
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
			item, ok := s.previewItem(rq, preview, parent)
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
	defs := s.opts.Catalog.SearchCatalogs(rq.stream)
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
				logger.Debug("Jellyfin search failed", "catalog", def.ID, "stream", rq.streamName(), "err", err)
			}
			rows[i] = metas
		}(i, def)
	}
	wg.Wait()
	var items []*baseItem
	seen := map[string]bool{}
	for _, metas := range rows {
		for _, preview := range metas {
			item, ok := s.previewItem(rq, preview, "")
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
		id, err := decodeItemID(parent)
		switch {
		case err != nil || id.Kind != kindView:
			logger.Debug("Jellyfin latest: parent is not a library; serving empty",
				"parent", parent, "stream", rq.streamName(), "err", err)
		default:
			def, ok := s.enabledCatalogByID(rq, id.CatalogID)
			if !ok {
				logger.Debug("Jellyfin latest: unknown catalog; serving empty",
					"catalog", id.CatalogID, "stream", rq.streamName())
			} else if wantsType(include, def.Type) {
				// Infuse always asks its Latest browser for 20 rows and never
				// continues with a later page. Override that client limit only for
				// pasted sources so their chosen list stays complete in Infuse.
				if def.Provider == "external" && isInfuseRequest(rq) {
					limit = infuseExternalLatestLimit
				}
				items = s.catalogPage(rq, def, 0, limit).Items
			}
		}
	} else {
		all := s.allItems(rq, include)
		items = all[:min(limit, len(all))]
	}
	writeJSON(w, http.StatusOK, items)
}

func isInfuseRequest(rq *request) bool {
	return strings.Contains(strings.ToLower(rq.UserAgent()), "infuse")
}

// resumeResult lists what the stream has partway through, newest first.
// favoritesOnly narrows it to the favourites the request's ParentId admits.
func (s *Server) resumeResult(rq *request, favoritesOnly bool) queryResult {
	result := emptyResult()
	if s.opts.Playstate == nil {
		return result
	}
	limit := rq.intParam("limit", resumeLimit)
	if limit <= 0 || limit > maxLimit {
		limit = resumeLimit
	}
	include := rq.listParam("includeItemTypes")
	var states []persistence.JellyfinPlaystate
	if favoritesOnly {
		// Narrowed before the limit, so the non-favourites played since
		// never crowd a favourite off the page.
		for _, fav := range s.scopedFavorites(rq, include) {
			if !fav.state.Played && fav.state.PositionTicks > 0 {
				states = append(states, fav.state)
			}
		}
		slices.SortStableFunc(states, func(a, b persistence.JellyfinPlaystate) int { return b.LastPlayedAt.Compare(a.LastPlayedAt) })
	} else {
		states = s.opts.Playstate.ListResume(rq.streamName(), limit)
	}
	for _, state := range states {
		if len(result.Items) == limit {
			break
		}
		id, err := decodeItemID(state.ItemID)
		if err != nil {
			continue
		}
		if len(include) > 0 {
			want := "episode"
			if id.Kind == kindMovie {
				want = "movie"
			}
			if !slices.Contains(include, want) {
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

// favoriteKinds names each kind that can be a favourite the way
// IncludeItemTypes spells it.
var favoriteKinds = map[itemKind]string{kindMovie: "movie", kindSeries: "series", kindSeason: "season", kindEpisode: "episode"}

type scopedFavorite struct {
	id    itemID
	state persistence.JellyfinPlaystate
}

// scopedFavorites is the stream's favourites that the request's ParentId
// admits (favoriteScope), in the store's order. Every selector a favourite
// filter narrows reads this one list, so they cannot disagree on the scope.
func (s *Server) scopedFavorites(rq *request, include []string) []scopedFavorite {
	admit, ok := s.favoriteScope(rq, include)
	if s.opts.Playstate == nil || !ok {
		return nil
	}
	var out []scopedFavorite
	for _, st := range s.opts.Playstate.ListFavorites(rq.streamName()) {
		if id, err := decodeItemID(st.ItemID); err == nil && favoriteKinds[id.Kind] != "" && admit(id) {
			out = append(out, scopedFavorite{id, st})
		}
	}
	return out
}

// favoriteStates is scopedFavorites keyed by item id, for checking whether
// one item is among them.
func (s *Server) favoriteStates(rq *request, include []string) map[string]persistence.JellyfinPlaystate {
	out := map[string]persistence.JellyfinPlaystate{}
	for _, fav := range s.scopedFavorites(rq, include) {
		out[fav.state.ItemID] = fav.state
	}
	return out
}

// favoritesResult lists the stream's favourites, filtered by IncludeItemTypes
// and by ParentId: a library admits its content type, and a series or season
// admits what its own children listing shows (childrenOf).
//
// Every admitted favourite is rendered before the page is cut. One that no
// longer resolves (its metadata gone, an episode the series dropped) is then
// out of the total as well as the page, so offsets stay contiguous and a
// client paging by the rows it has received never sees one twice. Rendering
// costs one metadata lookup per title, not per favourite: a series' favourite
// seasons and episodes share it, and the titles are fetched concurrently.
// Every page resolves every title, since only that keeps the total exact and
// no row on two pages when some metadata has gone.
func (s *Server) favoritesResult(rq *request, include []string, start, limit int) queryResult {
	var favs []scopedFavorite
	for _, fav := range s.scopedFavorites(rq, include) {
		if len(include) == 0 || slices.Contains(include, favoriteKinds[fav.id.Kind]) {
			favs = append(favs, fav)
		}
	}
	ids := make([]itemID, len(favs))
	for i, fav := range favs {
		ids[i] = fav.id
	}
	metas := s.prefetchMetas(rq, ids)
	genres, byGenre := s.requestedGenres(rq)
	var items []*baseItem
	for _, fav := range favs {
		if item := s.favoriteRow(rq, fav.id, metas); item != nil && (!byGenre || s.favoriteInGenres(rq, fav.id, metas[metaKey(fav.id)], genres)) {
			// The listed row already holds the state to render.
			item.UserData = userDataFor(item.ID, fav.state, true)
			items = append(items, item)
		}
	}
	return page(items, start, limit, false)
}

// favoriteInGenres matches a favourite as genreListing matches a row: on its
// TMDB genre ids, named from the stream's genre list. The metadata's own
// names may come from TVDB or Cinemeta, in another vocabulary or language,
// so they count only when the build read no TMDB details. Seasons and
// episodes carry no genres, so never match.
func (s *Server) favoriteInGenres(rq *request, id itemID, meta *stremio.MetaObject, names []string) bool {
	if meta == nil || (id.Kind != kindMovie && id.Kind != kindSeries) {
		return false
	}
	if len(meta.GenreIDs) > 0 {
		return hasAnyGenre(s.genreNamesOf(rq, id.ContentType, meta.GenreIDs), names)
	}
	return hasAnyGenre(meta.Genres, names)
}

// metaFetchLimit bounds the metadata lookups one listing runs at once.
const metaFetchLimit = 8

func metaKey(id itemID) string { return id.ContentType + "/" + id.baseStremioID() }

// prefetchMetas fetches the metadata of each distinct title behind ids, at
// most metaFetchLimit at a time, keyed by metaKey. A failed lookup is kept as
// nil, so a cold cache costs the slowest lookups rather than their sum and a
// title is never fetched twice.
func (s *Server) prefetchMetas(rq *request, ids []itemID) map[string]*stremio.MetaObject {
	metas := map[string]*stremio.MetaObject{}
	var titles []itemID
	for _, id := range ids {
		if _, seen := metas[metaKey(id)]; !seen {
			metas[metaKey(id)] = nil
			titles = append(titles, id)
		}
	}
	fetched := make([]*stremio.MetaObject, len(titles))
	var g errgroup.Group
	g.SetLimit(metaFetchLimit)
	for i, id := range titles {
		// A client that has gone needs no more lookups; the rest stay nil.
		if rq.Context().Err() != nil {
			break
		}
		g.Go(func() error {
			fetched[i], _ = s.meta(rq.Context(), rq, id)
			return nil
		})
	}
	_ = g.Wait()
	for i, id := range titles {
		metas[metaKey(id)] = fetched[i]
	}
	return metas
}

// favoriteScope is what the request's ParentId admits. A ParentId that
// decodes to nothing listable names no favourites, as it names no items in
// an ordinary listing.
func (s *Server) favoriteScope(rq *request, include []string) (func(itemID) bool, bool) {
	raw := rq.param("parentId")
	if raw == "" {
		return func(itemID) bool { return true }, true
	}
	parent, err := decodeItemID(raw)
	if err != nil {
		return nil, false
	}
	series := parent.series().encode()
	switch parent.Kind {
	case kindView:
		def, ok := s.enabledCatalogByID(rq, parent.CatalogID)
		if !ok {
			return nil, false
		}
		return func(id itemID) bool { return id.ContentType == def.Type }, true
	case kindGenre:
		// The genre's own content type; its name is matched once the row
		// is rendered (requestedGenres).
		return func(id itemID) bool { return id.ContentType == parent.ContentType }, true
	case kindSeries:
		// The seasons or episodes IncludeItemTypes names; without it,
		// seasons, or episodes when the client recurses.
		recursive := rq.boolParam("recursive")
		return func(id itemID) bool {
			if (id.Kind != kindSeason && id.Kind != kindEpisode) || id.series().encode() != series {
				return false
			}
			if len(include) > 0 {
				return slices.Contains(include, favoriteKinds[id.Kind])
			}
			return (id.Kind == kindEpisode) == recursive
		}, true
	case kindSeason:
		return func(id itemID) bool {
			return id.Kind == kindEpisode && id.Season == parent.Season && id.series().encode() == series
		}, true
	}
	return nil, false
}

// favoriteRow renders one favourite as the row its own listing shows: a
// movie or series as a catalog row, a season or episode as childrenOf
// renders it, list sources included, from metas as prefetchMetas returns it.
// Nothing here searches: itemByID would run resolveOnOpen for a series.
func (s *Server) favoriteRow(rq *request, id itemID, metas map[string]*stremio.MetaObject) *baseItem {
	meta := metas[metaKey(id)]
	if meta == nil {
		return nil
	}
	switch id.Kind {
	case kindSeason:
		for _, season := range seasonsOf(meta) {
			if season.Number == id.Season {
				return s.seasonItem(id.series(), meta, season.Number, season.Episodes)
			}
		}
		return nil
	case kindEpisode:
		for _, v := range videosOf(meta) {
			if v.Season == id.Season && v.Episode == id.Episode {
				item := s.episodeItem(id.series(), meta, v)
				s.attachListSources(rq, id, item)
				return item
			}
		}
		return nil
	}
	item := s.metaItem(id, meta)
	s.attachListSources(rq, id, item)
	return item
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
		def, ok := s.enabledCatalogByID(rq, id.CatalogID)
		if !ok {
			return nil, nil
		}
		return s.viewItem(def), nil
	case kindSource:
		return s.itemByID(rq, id.playable())
	case kindGenre:
		name, ok := s.genreNameOf(rq, id)
		if !ok {
			return nil, nil
		}
		return s.genreItem(id.ContentType, name), nil
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
		item.UserData = s.userData(rq, id)
		// Infuse (and other clients) read a series' own MediaSourceCount to
		// decide whether its quick-play button gets a version-picker chevron,
		// even though the button actually plays a specific episode. This addon
		// has no per-episode resume state (see serveShows: NextUp is
		// deliberately empty), so — matching the client's own fallback when it
		// has no resume position either — the first numbered episode stands in.
		// A version-picker for the wrong episode once the user has actually
		// progressed into the series is still strictly better than never
		// offering one at all, which is the status quo this replaces.
		//
		// resolveOnOpen accepts kindEpisode, so passing the same representative
		// episode's id — rather than this series id, which its own guard
		// rejects — lets a series page get the same eager, full-candidate
		// search on first view that JELLYFIN_RESOLVE_ON_OPEN already gives a
		// movie, instead of only ever showing the lone placeholder source
		// attachMediaSources falls back to when nothing is cached yet.
		if videos := videosOf(meta); len(videos) > 0 {
			first := videos[0]
			episodeID := id.episode(first.Season, first.Episode)
			s.attachMediaSources(rq, episodeID, item)
			s.resolveOnOpen(rq, episodeID, item)
		}
		return item, nil
	case kindSeason:
		for _, season := range seasonsOf(meta) {
			if season.Number == id.Season {
				item := s.seasonItem(id.series(), meta, season.Number, season.Episodes)
				item.UserData = s.userData(rq, id)
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
	// One query for the whole listing rather than one per row.
	states := map[string]persistence.JellyfinPlaystate{}
	if s.opts.Playstate != nil {
		for _, st := range s.opts.Playstate.ListForStream(rq.streamName()) {
			states[st.ItemID] = st
		}
	}
	if !wantEpisodes {
		for _, season := range seasonsOf(meta) {
			item := s.seasonItem(series, meta, season.Number, season.Episodes)
			st, ok := states[item.ID]
			item.UserData = userDataFor(item.ID, st, ok)
			items = append(items, item)
		}
		return items, nil
	}
	for _, v := range videosOf(meta) {
		if id.Kind == kindSeason && v.Season != id.Season {
			continue
		}
		item := s.episodeItem(series, meta, v)
		st, ok := states[item.ID]
		item.UserData = userDataFor(item.ID, st, ok)
		// Episodes are list rows too in Infuse's Direct Mode.
		s.attachListSources(rq, series.episode(v.Season, v.Episode), item)
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
