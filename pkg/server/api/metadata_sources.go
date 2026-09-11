package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"streamnzb/pkg/server/stremio"
	"streamnzb/pkg/services/metadata/tmdb"
)

// handleInspectMetadataSource tests a public Stremio manifest before a
// profile can save any rows from it. The response contains only non-empty,
// canonical-ID browse catalogs; external search and stream resources never
// cross this boundary.
func (s *Server) handleInspectMetadataSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can inspect catalog sources", http.MethodPost) {
		return
	}
	var body struct {
		ManifestURL string `json:"manifest_url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil || strings.TrimSpace(body.ManifestURL) == "" {
		http.Error(w, "A manifest URL is required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if listID, ok := tmdbListID(body.ManifestURL); ok {
		inspection, err := inspectTMDBList(ctx, body.ManifestURL, listID)
		if err != nil {
			http.Error(w, "Could not inspect catalog source: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, inspection)
		return
	}
	if listID, ok := mdbListID(body.ManifestURL); ok {
		inspection, err := inspectMDBList(ctx, body.ManifestURL, listID)
		if err != nil {
			http.Error(w, "Could not inspect catalog source: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, inspection)
		return
	}
	if listID, ok := letterboxdListID(body.ManifestURL); ok {
		inspection, err := inspectLetterboxdList(ctx, body.ManifestURL, listID)
		if err != nil {
			http.Error(w, "Could not inspect catalog source: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, inspection)
		return
	}
	inspection, err := stremio.InspectExternalManifest(ctx, body.ManifestURL)
	if err != nil {
		http.Error(w, "Could not inspect catalog source: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, inspection)
}

func letterboxdListID(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (strings.ToLower(u.Host) != "letterboxd.com" && strings.ToLower(u.Host) != "www.letterboxd.com") {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || parts[1] != "list" {
		return "", false
	}
	return strings.Join(parts[:3], "/"), true
}

func inspectLetterboxdList(ctx context.Context, rawURL, listID string) (any, error) {
	page, err := tmdb.FetchLetterboxdPage(ctx, rawURL, 1)
	if err != nil {
		return nil, err
	}
	if len(page.Items) == 0 {
		return nil, fmt.Errorf("Letterboxd exposes no canonical IMDb items on this public list")
	}
	name := strings.TrimSpace(page.Name)
	if name == "" {
		name = "Letterboxd List"
	}
	return map[string]any{"name": name, "kind": "letterboxd", "source_url": rawURL, "catalogs": []map[string]any{{"name": name + " (Movies)", "type": "movie", "remote_type": "movie", "remote_id": listID, "row_count": len(page.Items)}}}, nil
}

func tmdbListID(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.Contains(strings.ToLower(u.Host), "themoviedb.org") {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "list" {
		return "", false
	}
	id := strings.Split(parts[1], "-")[0]
	if _, err := strconv.Atoi(id); err != nil {
		return "", false
	}
	return id, true
}

func mdbListID(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (strings.ToLower(u.Host) != "mdblist.com" && strings.ToLower(u.Host) != "www.mdblist.com") {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "lists" || strings.TrimSpace(parts[1]) == "" || strings.TrimSpace(parts[2]) == "" {
		return "", false
	}
	return strings.Join(parts[1:], "/"), true
}

func inspectTMDBList(ctx context.Context, rawURL, listID string) (any, error) {
	items, err := tmdb.FetchAllPublicListPages(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	movie, series := 0, 0
	for _, item := range items {
		if item.Type == "movie" {
			movie++
		} else if item.Type == "tv" {
			series++
		}
	}
	if movie+series == 0 {
		return nil, fmt.Errorf("TMDB exposes no fetchable items on this public list")
	}
	cats := []map[string]any{}
	if movie > 0 {
		cats = append(cats, map[string]any{"name": "TMDB List (Movies)", "type": "movie", "remote_type": "movie", "remote_id": listID, "row_count": movie, "supports_skip": true})
	}
	if series > 0 {
		cats = append(cats, map[string]any{"name": "TMDB List (Series)", "type": "series", "remote_type": "series", "remote_id": listID, "row_count": series, "supports_skip": true})
	}
	return map[string]any{"name": "TMDB List", "kind": "tmdb_list", "source_url": rawURL, "catalogs": cats}, nil
}

func inspectMDBList(ctx context.Context, rawURL, listID string) (any, error) {
	page, err := tmdb.FetchAllMDBListPages(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	movie, series := 0, 0
	for _, item := range page.Items {
		if item.Type == "movie" {
			movie++
		} else if item.Type == "tv" {
			series++
		}
	}
	name := strings.TrimSpace(page.Name)
	if name == "" {
		name = "MDBList"
	}
	cats := []map[string]any{}
	if movie > 0 {
		cats = append(cats, map[string]any{"name": name + " (Movies)", "type": "movie", "remote_type": "movie", "remote_id": listID, "row_count": movie, "supports_skip": true})
	}
	if series > 0 {
		cats = append(cats, map[string]any{"name": name + " (Series)", "type": "series", "remote_type": "series", "remote_id": listID, "row_count": series, "supports_skip": true})
	}
	return map[string]any{"name": name, "kind": "mdblist", "source_url": rawURL, "catalogs": cats}, nil
}
