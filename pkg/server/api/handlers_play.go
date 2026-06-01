package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"streamnzb/pkg/auth"
)

const maxPlayNZBUploadBytes = 16 << 20

type directPlayResponse struct {
	SessionID       string `json:"session_id"`
	PlayURL         string `json:"play_url"`
	PlayPath        string `json:"play_path"`
	ExternalPlayURL string `json:"external_play_url"`
}

func (s *Server) handleDirectPlayNZB(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.strmServer == nil {
		http.Error(w, "Playback server unavailable", http.StatusServiceUnavailable)
		return
	}

	stream, _ := auth.StreamFromContext(r)
	if stream == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	sourceURL, sourceName, nzbData, err := parseDirectPlayRequest(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var sessionID string
	if len(nzbData) > 0 {
		sessionID, err = s.strmServer.CreateDirectPlaySessionFromNZBData(r.Context(), sourceName, nzbData, stream)
	} else {
		sessionID, err = s.strmServer.CreateDirectPlaySessionFromURL(r.Context(), sourceURL, stream)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(directPlayResponse{
		SessionID:       sessionID,
		PlayURL:         s.strmServer.DirectPlayPathForSession(sessionID, stream),
		PlayPath:        s.strmServer.DirectPlayPathForSession(sessionID, stream),
		ExternalPlayURL: s.strmServer.DirectPlayURLForSession(sessionID, stream),
	})
}

func parseDirectPlayRequest(w http.ResponseWriter, r *http.Request) (sourceURL string, sourceName string, nzbData []byte, err error) {
	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, maxPlayNZBUploadBytes+1024*64)
		if parseErr := r.ParseMultipartForm(maxPlayNZBUploadBytes + 1024*64); parseErr != nil {
			return "", "", nil, fmt.Errorf("invalid multipart form payload")
		}
		sourceURL = strings.TrimSpace(r.FormValue("url"))
		file, header, fileErr := r.FormFile("file")
		if fileErr == nil {
			defer file.Close()
			sourceName = strings.TrimSpace(header.Filename)
			nzbData, err = readLimitedNZB(file, maxPlayNZBUploadBytes)
			if err != nil {
				return "", "", nil, err
			}
		} else if !errors.Is(fileErr, http.ErrMissingFile) {
			return "", "", nil, fmt.Errorf("invalid file upload")
		}
	} else {
		var payload struct {
			URL string `json:"url"`
		}
		if decodeErr := json.NewDecoder(r.Body).Decode(&payload); decodeErr != nil {
			return "", "", nil, fmt.Errorf("invalid JSON payload")
		}
		sourceURL = strings.TrimSpace(payload.URL)
	}

	if len(nzbData) == 0 && sourceURL == "" {
		return "", "", nil, fmt.Errorf("provide an NZB file or URL")
	}
	return sourceURL, sourceName, nzbData, nil
}

func readLimitedNZB(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read uploaded file")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("uploaded file is empty")
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("uploaded file exceeds max size")
	}
	return data, nil
}
