package jellyfin

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/server/stremio"
)

// Genres come from TMDB: its listing rows name their genres by id, and the
// genre list in the stream's metadata language names the ids. A library from
// another source has rows without genres, so it lists none, and a genre
// filter on it is empty. Clients that build their categories from genres
// (UHF) otherwise have nothing to show for a library.

// hasGenres reports whether a library's rows carry genres.
func hasGenres(def stremio.CatalogDef) bool {
	return def.Provider == "tmdb"
}

// genreList is a content type's genres, fetched once per request.
func (s *Server) genreList(rq *request, contentType string) []stremio.Genre {
	key := "series"
	if contentType == "movie" {
		key = "movie"
	}
	if list, ok := rq.genres[key]; ok {
		return list
	}
	list, err := s.opts.Catalog.Genres(rq.stream, contentType)
	if err != nil && !errors.Is(err, stremio.ErrMetadataDisabled) {
		logger.Debug("Jellyfin genre list failed", "type", contentType, "stream", rq.streamName(), "err", err)
	}
	if rq.genres == nil {
		rq.genres = map[string][]stremio.Genre{}
	}
	rq.genres[key] = list
	return list
}

// rowGenres names a catalog row's genres.
func (s *Server) rowGenres(rq *request, preview stremio.MetaPreview) []string {
	if len(preview.GenreIDs) == 0 {
		return nil
	}
	list := s.genreList(rq, preview.Type)
	var names []string
	for _, id := range preview.GenreIDs {
		for _, g := range list {
			if g.ID == id {
				names = append(names, g.Name)
				break
			}
		}
	}
	return names
}

// genreNameOf resolves a decoded genre id to its name.
func (s *Server) genreNameOf(rq *request, id itemID) (string, bool) {
	for _, g := range s.genreList(rq, id.ContentType) {
		if genreID(id.ContentType, g.Name) == id.GenreKey {
			return g.Name, true
		}
	}
	return "", false
}

func (s *Server) genreItem(contentType, name string) *baseItem {
	id := genreID(contentType, name)
	return &baseItem{
		Name:     name,
		ServerID: s.serverID(),
		ID:       id,
		SortName: strings.ToLower(name),
		IsFolder: true,
		Type:     "Genre",
		UserData: &userData{Key: id, ItemID: id},
	}
}

// genreDefs is the libraries a genre route covers: the one ParentId names,
// or every library. A ParentId that is not a library covers none.
func (s *Server) genreDefs(rq *request) []stremio.CatalogDef {
	if parent := rq.param("parentId"); parent != "" {
		id, err := decodeItemID(parent)
		if err != nil || id.Kind != kindView {
			return nil
		}
		def, ok := s.enabledCatalogByID(rq, id.CatalogID)
		if !ok {
			return nil
		}
		return []stremio.CatalogDef{def}
	}
	defs, _ := s.views(rq)
	return defs
}

// genresOf lists the genres of libraries, by name, one entry per name.
func (s *Server) genresOf(rq *request, defs []stremio.CatalogDef) []*baseItem {
	// Only the content types filter genres; a client may also name Genre.
	var include []string
	for _, t := range rq.listParam("includeItemTypes") {
		if t == "movie" || t == "series" {
			include = append(include, t)
		}
	}
	var items []*baseItem
	seen := map[string]bool{}
	for _, def := range defs {
		if !hasGenres(def) || !wantsType(include, def.Type) {
			continue
		}
		for _, g := range s.genreList(rq, def.Type) {
			key := strings.ToLower(g.Name)
			if seen[key] {
				continue
			}
			seen[key] = true
			items = append(items, s.genreItem(def.Type, g.Name))
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].SortName < items[j].SortName })
	return items
}

// handleGenres serves /Genres: the genres of one library or of all of them.
func (s *Server) handleGenres(w http.ResponseWriter, rq *request) {
	items := s.genresOf(rq, s.genreDefs(rq))
	start := max(rq.intParam("startIndex", 0), 0)
	limit := rq.intParam("limit", 0)
	if limit <= 0 {
		limit = max(len(items), 1)
	}
	writeJSON(w, http.StatusOK, page(items, start, limit, false))
}

// handleGenre serves /Genres/{name}.
func (s *Server) handleGenre(w http.ResponseWriter, rq *request, name string) {
	for _, item := range s.genresOf(rq, s.genreDefs(rq)) {
		if strings.EqualFold(item.Name, name) {
			writeJSON(w, http.StatusOK, item)
			return
		}
	}
	http.NotFound(w, rq.Request)
}

// handleFilters serves Items/Filters and Items/Filters2, the genre picker of
// a library.
func (s *Server) handleFilters(w http.ResponseWriter, rq *request, v2 bool) {
	genres := s.genresOf(rq, s.genreDefs(rq))
	if v2 {
		named := make([]nameGUID, 0, len(genres))
		for _, g := range genres {
			named = append(named, nameGUID{Name: g.Name, ID: g.ID})
		}
		writeJSON(w, http.StatusOK, map[string]any{"Genres": named, "Tags": []any{}})
		return
	}
	names := make([]string, 0, len(genres))
	for _, g := range genres {
		names = append(names, g.Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"Genres": names, "Tags": []any{}, "OfficialRatings": []any{}, "Years": []any{}})
}

// requestedGenres reads a listing's genre filter: GenreIds, Genres (names,
// pipe-separated) or a ParentId that is a genre. asked is true whenever a
// filter was given, even one naming no genre this stream knows, which then
// matches nothing rather than everything.
func (s *Server) requestedGenres(rq *request) (names []string, asked bool) {
	ids := rq.listParam("genreIds")
	if parent := rq.param("parentId"); parent != "" {
		if id, err := decodeItemID(parent); err == nil && id.Kind == kindGenre {
			ids = append(ids, parent)
		}
	}
	for _, raw := range ids {
		asked = true
		id, err := decodeItemID(raw)
		if err != nil || id.Kind != kindGenre {
			continue
		}
		if name, ok := s.genreNameOf(rq, id); ok {
			names = append(names, name)
		}
	}
	for _, name := range strings.Split(rq.param("genres"), "|") {
		if name = strings.TrimSpace(name); name != "" {
			asked = true
			names = append(names, name)
		}
	}
	return names, asked
}

func hasAnyGenre(item *baseItem, names []string) bool {
	for _, have := range item.Genres {
		for _, want := range names {
			if strings.EqualFold(have, want) {
				return true
			}
		}
	}
	return false
}

// genreListing serves a listing filtered by genre: within the library
// ParentId names, or across the first page of every library.
func (s *Server) genreListing(rq *request, names []string, include []string, start, limit int) queryResult {
	if len(names) == 0 {
		result := emptyResult()
		result.StartIndex = start
		return result
	}
	parent := rq.param("parentId")
	var view *itemID
	if parent != "" {
		if id, err := decodeItemID(parent); err == nil && id.Kind == kindView {
			view = &id
		}
	}
	if view == nil {
		var items []*baseItem
		for _, item := range s.allItems(rq, include) {
			if hasAnyGenre(item, names) {
				items = append(items, item)
			}
		}
		return page(items, start, limit, false)
	}
	def, ok := s.enabledCatalogByID(rq, view.CatalogID)
	if !ok || !hasGenres(def) || !wantsType(include, def.Type) {
		result := emptyResult()
		result.StartIndex = start
		return result
	}
	return s.genreCatalogPage(rq, def, names, start, limit)
}

// genreCatalogPage is catalogPage for the rows of one genre. The matches are
// counted from the top of the library, so the walk starts there for every
// page and stops once the window is full; reaching the end of the library,
// or maxLibraryRows, makes the total exact.
func (s *Server) genreCatalogPage(rq *request, def stremio.CatalogDef, names []string, start, limit int) queryResult {
	result := emptyResult()
	result.StartIndex = start
	parent := viewID(def.ID)
	bucket := stremio.CatalogPageSize
	matched := 0
	ended := true
	for skip := 0; skip < maxLibraryRows; skip += bucket {
		if matched >= start+limit {
			ended = false
			break
		}
		metas, err := s.opts.Catalog.Catalog(rq.Context(), rq.stream, def.ID, def.Type, "", skip)
		if err != nil {
			logger.Debug("Jellyfin catalog page failed", "catalog", def.ID, "skip", skip, "err", err)
			break
		}
		if len(metas) == 0 {
			break
		}
		for _, preview := range metas {
			item, ok := s.previewItem(rq, preview, parent)
			if !ok || !hasAnyGenre(item, names) {
				continue
			}
			if matched >= start && matched < start+limit {
				result.Items = append(result.Items, item)
			}
			matched++
		}
		if !def.SupportsSkip {
			break
		}
	}
	result.TotalRecordCount = matched
	if !ended {
		result.TotalRecordCount = start + limit + bucket
	}
	return result
}
