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
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	adminUsername := s.adminUsername()
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

	writeJSON(w, http.StatusOK, stremio.RenderFormatPreview(req.NameTemplate, req.DescriptionTemplate))
}

// handleFormatConvert translates AIOStreams custom-formatter templates into
// StreamNZB Go result templates, best-effort, returning per-template
// warnings for anything that did not convert cleanly.
func (s *Server) handleFormatConvert(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	adminUsername := s.adminUsername()
	if stream, _ := auth.StreamFromContext(r); stream == nil || stream.Username != adminUsername {
		http.Error(w, "Only admin can convert result formats", http.StatusForbidden)
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

	name := stremio.ConvertAIOStreamsFormat(req.NameTemplate)
	desc := stremio.ConvertAIOStreamsFormat(req.DescriptionTemplate)
	writeJSON(w, http.StatusOK, map[string]any{
		"name_template":        name.Template,
		"name_warnings":        name.Warnings,
		"description_template": desc.Template,
		"description_warnings": desc.Warnings,
	})
}
