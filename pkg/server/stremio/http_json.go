package stremio

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// writeJSON writes v as JSON with the CORS header every Stremio resource
// response needs. Headers must be set before the status is written.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONCached is writeJSON plus client-side cache hints for resources that
// are safe to reuse (meta, catalogs). maxAge/staleRevalidate are seconds.
func writeJSONCached(w http.ResponseWriter, v any, maxAge, staleRevalidate int) {
	if maxAge > 0 {
		cc := fmt.Sprintf("public, max-age=%d", maxAge)
		if staleRevalidate > 0 {
			cc += fmt.Sprintf(", stale-while-revalidate=%d", staleRevalidate)
		}
		w.Header().Set("Cache-Control", cc)
	}
	writeJSON(w, http.StatusOK, v)
}
