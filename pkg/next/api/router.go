package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/next/playback"
	"streamnzb/pkg/next/preset"
)

type Dependencies struct {
	Version           string
	Preset            *preset.Service
	Playback          *playback.Service
	AuthenticateToken func(string) (*auth.Device, error)
}

type resolveResponse struct {
	SessionID string `json:"session_id"`
	PlayURL   string `json:"play_url"`
}

func NewRouter(deps Dependencies) http.Handler {
	protected := http.NewServeMux()

	protected.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		writeJSON(w, http.StatusOK, newAddonManifest(deps.Version))
	})

	protected.HandleFunc("/stream/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/stream/")
		path = strings.TrimSuffix(path, ".json")
		parts := strings.Split(path, "/")
		if len(parts) < 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			http.Error(w, "Invalid stream URL", http.StatusBadRequest)
			return
		}
		matchReq := preset.MatchRequest{Type: parts[0], MetadataID: parts[1]}
		resp, err := deps.Preset.StreamsWithNZBURLResolver(r.Context(), matchReq, func(cand preset.Candidate) (string, error) {
			return deps.Playback.NormalizeDownloadURL(cand.Link)
		})
		if err != nil {
			if errors.Is(err, preset.ErrInvalidRequest) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for i := range resp.Streams {
			resp.Streams[i].URL = buildPlayURL(r, resp.Streams[i].NZBURL, matchReq.MetadataID)
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		writeJSON(w, http.StatusOK, resp)
	})

	protected.HandleFunc("/resolve", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		nzbURL := strings.TrimSpace(r.URL.Query().Get("nzburl"))
		if nzbURL == "" {
			http.Error(w, "nzburl is required", http.StatusBadRequest)
			return
		}
		metadataID := strings.TrimSpace(r.URL.Query().Get("metadata_id"))
		sessionID, _, err := deps.Playback.ResolveAndPrepareNZBURL(r.Context(), nzbURL, metadataID)
		if err != nil {
			writePlaybackError(w, r, err, "handler", "resolve", "session_id", sessionID, "metadata_id", metadataID)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		writeJSON(w, http.StatusOK, resolveResponse{
			SessionID: sessionID,
			PlayURL:   buildSessionPlayURL(r, sessionID),
		})
	})

	protected.HandleFunc("/play", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
		if sessionID != "" {
			if err := deps.Playback.ServeHTTP(w, r, sessionID); err != nil {
				writePlaybackError(w, r, err, "handler", "play", "session_id", sessionID)
			}
			return
		}
		nzbURL := strings.TrimSpace(r.URL.Query().Get("nzburl"))
		if nzbURL == "" {
			http.Error(w, "nzburl or session_id is required", http.StatusBadRequest)
			return
		}
		metadataID := strings.TrimSpace(r.URL.Query().Get("metadata_id"))
		if err := deps.Playback.ServeNZBURL(w, r, nzbURL, metadataID); err != nil {
			writePlaybackError(w, r, err, "handler", "play", "metadata_id", metadataID, "has_nzburl", nzbURL != "")
		}
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r == nil {
			http.NotFound(w, r)
			return
		}

		if token, strippedPath, ok := tokenizedProtectedPath(r.URL.Path); ok {
			strippedReq := requestWithPath(r, strippedPath)
			if authedReq, ok := authenticateExplicitToken(strippedReq, token, deps.AuthenticateToken); ok {
				protected.ServeHTTP(w, authedReq)
				return
			}
			if authedReq, ok := authenticateRequest(w, strippedReq, deps.AuthenticateToken); ok {
				protected.ServeHTTP(w, authedReq)
				return
			}
			writeUnauthorized(w)
			return
		}

		if isProtectedPath(r.URL.Path) {
			if authedReq, ok := authenticateRequest(w, r, deps.AuthenticateToken); ok {
				protected.ServeHTTP(w, authedReq)
				return
			}
			writeUnauthorized(w)
			return
		}

		http.NotFound(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writePlaybackError(w http.ResponseWriter, r *http.Request, err error, attrs ...any) {
	status := http.StatusInternalServerError
	if errors.Is(err, playback.ErrInvalidRequest) {
		status = http.StatusBadRequest
	} else if errors.Is(err, playback.ErrSessionNotFound) {
		status = http.StatusNotFound
	} else if errors.Is(err, playback.ErrPlaybackStartup) {
		status = http.StatusNotFound
	} else if errors.Is(err, playback.ErrNotReady) {
		status = http.StatusServiceUnavailable
	}

	logAttrs := []any{"status", status, "err", err}
	if r != nil {
		logAttrs = append(logAttrs, "method", r.Method, "path", r.URL.Path)
	}
	logAttrs = append(logAttrs, attrs...)

	if status >= http.StatusInternalServerError && !errors.Is(err, playback.ErrNotReady) {
		logger.Error("Next playback request failed", logAttrs...)
	} else {
		logger.Warn("Next playback request failed", logAttrs...)
	}

	http.Error(w, err.Error(), status)
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "Unauthorized",
	})
}

func authenticateRequest(w http.ResponseWriter, r *http.Request, authenticateToken func(string) (*auth.Device, error)) (*http.Request, bool) {
	if authenticateToken == nil || r == nil {
		return r, false
	}

	if cookie, err := r.Cookie("auth_session"); err == nil && cookie != nil {
		if authedReq, ok := authenticateExplicitToken(r, cookie.Value, authenticateToken); ok {
			return authedReq, true
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "auth_session",
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			MaxAge:   -1,
		})
	}

	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && parts[0] == "Bearer" {
			if authedReq, ok := authenticateExplicitToken(r, parts[1], authenticateToken); ok {
				return authedReq, true
			}
		}
	}

	if authedReq, ok := authenticateExplicitToken(r, r.URL.Query().Get("token"), authenticateToken); ok {
		return authedReq, true
	}

	return r, false
}

func authenticateExplicitToken(r *http.Request, token string, authenticateToken func(string) (*auth.Device, error)) (*http.Request, bool) {
	if authenticateToken == nil || r == nil {
		return r, false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return r, false
	}
	device, err := authenticateToken(token)
	if err != nil || device == nil {
		return r, false
	}
	return r.WithContext(auth.ContextWithDevice(r.Context(), device)), true
}

func isProtectedPath(path string) bool {
	path = strings.TrimSpace(path)
	switch {
	case path == "/manifest.json":
		return true
	case path == "/resolve":
		return true
	case path == "/play":
		return true
	case strings.HasPrefix(path, "/stream/"):
		return true
	default:
		return false
	}
}

func tokenizedProtectedPath(path string) (token, strippedPath string, ok bool) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(path), "/")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) < 2 || strings.TrimSpace(parts[0]) == "" {
		return "", "", false
	}
	strippedPath = "/" + strings.TrimPrefix(parts[1], "/")
	if !isProtectedPath(strippedPath) {
		return "", "", false
	}
	return parts[0], strippedPath, true
}

func requestWithPath(r *http.Request, path string) *http.Request {
	if r == nil || r.URL == nil {
		return r
	}
	cloned := r.Clone(r.Context())
	urlCopy := *r.URL
	urlCopy.Path = path
	urlCopy.RawPath = ""
	cloned.URL = &urlCopy
	return cloned
}

func buildPlayURL(r *http.Request, nzbURL, metadataID string) string {
	q := url.Values{}
	q.Set("nzburl", strings.TrimSpace(nzbURL))
	metadataID = strings.TrimSpace(metadataID)
	if metadataID != "" {
		q.Set("metadata_id", metadataID)
	}
	return buildRequestURL(r, "/play?"+q.Encode())
}

func buildSessionPlayURL(r *http.Request, sessionID string) string {
	q := url.Values{}
	q.Set("session_id", strings.TrimSpace(sessionID))
	return buildRequestURL(r, "/play?"+q.Encode())
}

func buildRequestURL(r *http.Request, path string) string {
	path = requestTokenPrefix(r) + path
	base := requestBaseURL(r)
	if base == "" {
		return path
	}
	return base + path
}

func requestBaseURL(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	if host == "" {
		return ""
	}
	scheme := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + host
}

func requestTokenPrefix(r *http.Request) string {
	if r == nil {
		return ""
	}
	device, ok := auth.DeviceFromContext(r)
	if !ok || device == nil {
		return ""
	}
	token := strings.TrimSpace(device.Token)
	if token == "" {
		return ""
	}
	return "/" + token
}
