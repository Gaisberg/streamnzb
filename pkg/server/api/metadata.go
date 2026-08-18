package api

import (
	"net/http"

	"streamnzb/pkg/server/stremio"
	"streamnzb/pkg/services/metadata/certification"
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

// handleMetadataCertifications serves the selectable rating-limit options so
// the frontend has no hardcoded mirror of the certification ladder.
func (s *Server) handleMetadataCertifications(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, certification.Options())
}
