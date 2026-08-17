package api

import (
	"net/http"

	"streamnzb/pkg/server/stremio"
)

// handleMetadataCatalogs serves the catalog registry so the frontend Metadata
// page renders exactly the catalogs the backend can serve — the registry in
// pkg/server/stremio is the single source of truth.
func (s *Server) handleMetadataCatalogs(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, stremio.CatalogRegistry())
}
