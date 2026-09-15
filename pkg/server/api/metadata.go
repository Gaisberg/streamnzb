package api

import (
	"net/http"

	"streamnzb/pkg/server/stremio"
	"streamnzb/pkg/services/metadata/certification"
)

// handleMetadataCatalogs serves the catalog registry so the frontend Metadata
// page renders exactly the catalogs the backend can serve — the registry in
// pkg/server/stremio is the single source of truth. Simkl rows only surface
// once some stream has linked an account; an install where nobody has offering
// "Simkl Watching" would just be a permanently empty row.
//
// One linked stream is enough, because what this gates is a metadata profile,
// and a profile is shared by however many streams bind it — each serving its
// own account's watchlist through the same row.
func (s *Server) handleMetadataCatalogs(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	defs := stremio.CatalogRegistry()
	if !s.anyStreamLinkedSimkl() {
		served := defs[:0]
		for _, def := range defs {
			if def.Provider != "simkl" {
				served = append(served, def)
			}
		}
		defs = served
	}
	writeJSON(w, http.StatusOK, defs)
}

func (s *Server) anyStreamLinkedSimkl() bool {
	if s.app == nil {
		return false
	}
	comp := s.app.Components()
	return comp != nil && comp.SimklClients.AnyLinked()
}

// handleMetadataCertifications serves the selectable rating-limit options so
// the frontend has no hardcoded mirror of the certification ladder.
func (s *Server) handleMetadataCertifications(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, certification.Options())
}
