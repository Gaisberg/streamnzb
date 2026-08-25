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
	"streamnzb/pkg/server/stremio"
)

// maxPlayNZBUploadBytes mirrors the session layer's cap so an upload the
// handler accepts is never refused one call later.
const maxPlayNZBUploadBytes = stremio.MaxDirectPlayNZBSize

// nzbTooLargeError marks the one parse failure that deserves 413 rather than
// 400: the payload was well-formed, just bigger than the cap allows.
type nzbTooLargeError struct{}

func (nzbTooLargeError) Error() string {
	return fmt.Sprintf("NZB upload exceeds the %d MiB limit", maxPlayNZBUploadBytes>>20)
}

type directPlayResponse struct {
	SessionID       string `json:"session_id"`
	PlayURL         string `json:"play_url"`
	PlayPath        string `json:"play_path"`
	ExternalPlayURL string `json:"external_play_url"`
}

func (s *Server) handleDirectPlayNZB(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
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

	sourceURL, sourceName, libraryID, nzbData, err := parseDirectPlayRequest(w, r)
	if err != nil {
		status := http.StatusBadRequest
		var tooLarge nzbTooLargeError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, err.Error(), status)
		return
	}

	// A library item plays from its stored NZB — no indexer round-trip.
	if libraryID != "" {
		if s.attemptLister == nil || s.attemptLister.LibraryStore() == nil {
			http.Error(w, "Library unavailable", http.StatusServiceUnavailable)
			return
		}
		item, itemErr := s.attemptLister.LibraryStore().GetItem(libraryID)
		if itemErr != nil || item == nil || len(item.NZBData) == 0 {
			http.Error(w, "Library item not found or has no stored NZB", http.StatusNotFound)
			return
		}
		sourceName = item.ReleaseTitle
		if sourceName == "" {
			sourceName = libraryID
		}
		nzbData = item.NZBData
		s.attemptLister.LibraryStore().TouchItem(item.ID)
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

	writeJSON(w, http.StatusOK, directPlayResponse{
		SessionID:       sessionID,
		PlayURL:         s.strmServer.DirectPlayPathForSession(sessionID, stream),
		PlayPath:        s.strmServer.DirectPlayPathForSession(sessionID, stream),
		ExternalPlayURL: s.strmServer.DirectPlayURLForSession(sessionID, stream),
	})
}

func parseDirectPlayRequest(w http.ResponseWriter, r *http.Request) (sourceURL string, sourceName string, libraryID string, nzbData []byte, err error) {
	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, maxPlayNZBUploadBytes+1024*64)
		// The small maxMemory spills big NZB parts to temp files instead of
		// holding a few hundred MB of form data in RAM; the MaxBytesReader
		// above still bounds the total body.
		if parseErr := r.ParseMultipartForm(16 << 20); parseErr != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(parseErr, &tooLarge) {
				return "", "", "", nil, nzbTooLargeError{}
			}
			return "", "", "", nil, fmt.Errorf("invalid multipart form payload")
		}
		sourceURL = strings.TrimSpace(r.FormValue("url"))
		libraryID = strings.TrimSpace(r.FormValue("library_id"))
		file, header, fileErr := r.FormFile("file")
		if fileErr == nil {
			defer file.Close()
			sourceName = strings.TrimSpace(header.Filename)
			nzbData, err = readLimitedNZB(file, maxPlayNZBUploadBytes)
			if err != nil {
				return "", "", "", nil, err
			}
		} else if !errors.Is(fileErr, http.ErrMissingFile) {
			return "", "", "", nil, fmt.Errorf("invalid file upload")
		}
	} else {
		var payload struct {
			URL       string `json:"url"`
			LibraryID string `json:"library_id"`
		}
		if decodeErr := json.NewDecoder(r.Body).Decode(&payload); decodeErr != nil {
			return "", "", "", nil, fmt.Errorf("invalid JSON payload")
		}
		sourceURL = strings.TrimSpace(payload.URL)
		libraryID = strings.TrimSpace(payload.LibraryID)
	}

	if len(nzbData) == 0 && sourceURL == "" && libraryID == "" {
		return "", "", "", nil, fmt.Errorf("provide an NZB file, URL, or library item")
	}
	return sourceURL, sourceName, libraryID, nzbData, nil
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
		return nil, nzbTooLargeError{}
	}
	return data, nil
}
