package api

import (
	"encoding/json"
	"io"
	"net/http"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/server/stremio"
)

// handleFormatPreview renders the posted result-format templates over canned
// sample results, exactly as the live stream path would. Result formats are
// configuration, so this is admin-only like the other config endpoints.
func (s *Server) handleFormatPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	adminUsername := s.config.GetAdminUsername()
	s.mu.RUnlock()
	if stream, _ := auth.StreamFromContext(r); stream == nil || stream.Username != adminUsername {
		http.Error(w, "Only admin can preview result formats", http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	var req struct {
		NameTemplate        string `json:"name_template"`
		DescriptionTemplate string `json:"description_template"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stremio.RenderFormatPreview(req.NameTemplate, req.DescriptionTemplate))
}
