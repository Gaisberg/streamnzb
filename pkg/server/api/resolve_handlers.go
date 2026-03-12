package api

import (
	"crypto/md5"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/session"
)

type resolveNZBRequest struct {
	NZBURL      string `json:"nzb_url"`
	ContentType string `json:"content_type,omitempty"`
	ContentID   string `json:"content_id,omitempty"`
	Season      int    `json:"season,omitempty"`
	Episode     int    `json:"episode,omitempty"`
}

type resolveNZBResponse struct {
	Success   bool   `json:"success"`
	SessionID string `json:"session_id"`
	PlayURL   string `json:"play_url"`
}

func (s *Server) handleResolveNZB(w http.ResponseWriter, r *http.Request) {

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	device, _ := auth.DeviceFromContext(r)
	if device == nil {
		device = &auth.Device{Username: "admin"}
	}

	var req resolveNZBRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	nzbURL := strings.TrimSpace(req.NZBURL)
	contentType := strings.TrimSpace(req.ContentType)
	contentID := strings.TrimSpace(req.ContentID)
	logger.Debug("API resolve NZB request",
		"username", device.Username,
		"nzb_url", nzbURL,
		"content_type", contentType,
		"content_id", contentID,
		"season", req.Season,
		"episode", req.Episode,
	)
	if !isValidResolveNZBURL(nzbURL) {
		http.Error(w, "Invalid nzb_url", http.StatusBadRequest)
		return
	}
	if (req.Season > 0) != (req.Episode > 0) {
		http.Error(w, "season and episode must be provided together", http.StatusBadRequest)
		return
	}

	var contentIDs *session.AvailReportMeta
	if req.Season > 0 && req.Episode > 0 {
		contentIDs = &session.AvailReportMeta{Season: req.Season, Episode: req.Episode}
	}
	sessionID := resolveSessionID(nzbURL)
	if _, err := s.sessionMgr.CreateDeferredSession(sessionID, nzbURL, nil, s.indexer, contentIDs, contentType, contentID); err != nil {
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resolveNZBResponse{
		Success:   true,
		SessionID: sessionID,
		PlayURL:   s.resolvePlayURL(device, sessionID),
	})
}

func resolveSessionID(nzbURL string) string {
	return "resolve-" + strings.ToLower(strings.TrimSpace(fmtHash(nzbURL)))
}

func fmtHash(value string) string {
	sum := md5.Sum([]byte(strings.TrimSpace(value)))
	return string(hexDigest(sum[:]))
}

func hexDigest(sum []byte) []byte {
	const hextable = "0123456789abcdef"
	out := make([]byte, len(sum)*2)
	for i, b := range sum {
		out[i*2] = hextable[b>>4]
		out[i*2+1] = hextable[b&0x0f]
	}
	return out
}

func isValidResolveNZBURL(rawURL string) bool {
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil || parsed.Host == "" {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return true
	default:
		return false
	}
}

func (s *Server) resolvePlayURL(device *auth.Device, sessionID string) string {
	base := ""
	if s.config != nil {
		base = strings.TrimSuffix(strings.TrimSpace(s.config.AddonBaseURL), "/")
	}
	playPath := "/resolve/play/" + sessionID
	if device != nil && device.Token != "" {
		if base == "" {
			return "/" + device.Token + playPath
		}
		return base + "/" + device.Token + playPath
	}
	if base == "" {
		return playPath
	}
	return base + playPath
}
