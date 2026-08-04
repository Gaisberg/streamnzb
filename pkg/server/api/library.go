package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"streamnzb/pkg/core/logger"
)

type LibraryItemDTO struct {
	ID             string `json:"id"`
	ContentType    string `json:"content_type"`
	ContentID      string `json:"content_id"`
	Season         int    `json:"season"`
	Episode        int    `json:"episode"`
	ReleaseTitle   string `json:"release_title"`
	DetailsURL     string `json:"details_url"`
	IndexerName    string `json:"indexer_name"`
	SizeBytes      int64  `json:"size_bytes"`
	MediaFileName  string `json:"media_file_name,omitempty"`
	MediaFileSize  int64  `json:"media_file_size,omitempty"`
	VideoCodec     string `json:"video_codec,omitempty"`
	Height         int    `json:"height,omitempty"`
	BitDepth       int    `json:"bit_depth,omitempty"`
	HDR            string `json:"hdr,omitempty"`
	DolbyVision    bool   `json:"dolby_vision,omitempty"`
	AudioCodec     string `json:"audio_codec,omitempty"`
	HasBlueprint   bool   `json:"has_blueprint"`
	HasNZB         bool   `json:"has_nzb"`
	CreatedAt      string `json:"created_at"`
	LastAccessedAt string `json:"last_accessed_at"`
	Pinned         bool   `json:"pinned"`
	Status         string `json:"status"`
	StatusReason   string `json:"status_reason,omitempty"`
}

type LibraryListResponse struct {
	Items  []LibraryItemDTO `json:"items"`
	Total  int              `json:"total"`
	Offset int              `json:"offset"`
	Limit  int              `json:"limit"`
}

func (s *Server) handleGetLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.attemptLister == nil || s.attemptLister.LibraryStore() == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(LibraryListResponse{Items: []LibraryItemDTO{}, Total: 0})
		return
	}

	query := r.URL.Query()
	q := query.Get("q")
	contentType := query.Get("type")
	pinnedOnly := query.Get("pinned") == "true" || query.Get("pinned") == "1"
	statusFilter := query.Get("status") // "", "all", "good", "pending", "bad"

	offset, _ := strconv.Atoi(query.Get("offset"))
	limit, _ := strconv.Atoi(query.Get("limit"))
	if limit <= 0 {
		limit = 50
	}

	items, total, err := s.attemptLister.LibraryStore().GetFilteredItems(q, contentType, pinnedOnly, statusFilter, offset, limit)
	if err != nil {
		logger.Error("Failed to fetch library items", "err", err)
		http.Error(w, "Failed to fetch library items", http.StatusInternalServerError)
		return
	}

	dtos := make([]LibraryItemDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, LibraryItemDTO{
			ID:             item.ID,
			ContentType:    item.ContentType,
			ContentID:      item.ContentID,
			Season:         item.Season,
			Episode:        item.Episode,
			ReleaseTitle:   item.ReleaseTitle,
			DetailsURL:     item.DetailsURL,
			IndexerName:    item.IndexerName,
			SizeBytes:      item.SizeBytes,
			MediaFileName:  item.MediaFileName,
			MediaFileSize:  item.MediaFileSize,
			VideoCodec:     item.VideoCodec,
			Height:         item.Height,
			BitDepth:       item.BitDepth,
			HDR:            item.HDR,
			DolbyVision:    item.DolbyVision,
			AudioCodec:     item.AudioCodec,
			HasBlueprint:   item.BlueprintJSON != "",
			HasNZB:         len(item.NZBData) > 0 || item.NZBSizeBytes > 0,
			CreatedAt:      item.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			LastAccessedAt: item.LastAccessedAt.Format("2006-01-02T15:04:05Z07:00"),
			Pinned:         item.Pinned,
			Status:         item.Status,
			StatusReason:   item.StatusReason,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(LibraryListResponse{
		Items:  dtos,
		Total:  total,
		Offset: offset,
		Limit:  limit,
	})
}

func (s *Server) handlePinLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.attemptLister == nil || s.attemptLister.LibraryStore() == nil {
		http.Error(w, "Library store unavailable", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		ID     string `json:"id"`
		Pinned bool   `json:"pinned"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	if err := s.attemptLister.LibraryStore().SetPinned(req.ID, req.Pinned); err != nil {
		logger.Error("Failed to set pinned state", "id", req.ID, "err", err)
		http.Error(w, "Failed to update pinned state", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "id": req.ID, "pinned": req.Pinned})
}

func (s *Server) handleDeleteLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.attemptLister == nil || s.attemptLister.LibraryStore() == nil {
		http.Error(w, "Library store unavailable", http.StatusServiceUnavailable)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" && r.Method == http.MethodPost {
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			id = req.ID
		}
	}
	id = strings.TrimSpace(id)
	if id == "" {
		http.Error(w, "Missing library item id", http.StatusBadRequest)
		return
	}

	if err := s.attemptLister.LibraryStore().DeleteItem(id); err != nil {
		logger.Error("Failed to delete library item", "id", id, "err", err)
		http.Error(w, "Failed to delete library item", http.StatusInternalServerError)
		return
	}

	if s.strmServer != nil {
		s.strmServer.ClearSearchCaches()
	}
	if s.sessionMgr != nil {
		s.sessionMgr.ClearBlueprintCache()
	}

	logger.Info("Library item deleted and playback/search caches cleared", "id", id)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "id": id})
}

func (s *Server) handleLibraryStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.attemptLister == nil || s.attemptLister.LibraryStore() == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"total_items": 0, "max_items": 5000})
		return
	}

	stats, err := s.attemptLister.LibraryStore().GetStats()
	if err != nil {
		logger.Error("Failed to get library stats", "err", err)
		http.Error(w, "Failed to get library stats", http.StatusInternalServerError)
		return
	}

	maxItems := 5000
	maxSizeMB := 250
	if s.config != nil {
		maxItems = s.config.EffectiveLibraryMaxItems()
		maxSizeMB = s.config.EffectiveLibraryMaxSizeMB()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"total_items":       stats.TotalItems,
		"total_nzb_bytes":   stats.TotalNZBBytes,
		"total_media_bytes": stats.TotalMediaBytes,
		"total_size_bytes":  stats.TotalSizeBytes,
		"pinned_items":      stats.PinnedItems,
		"max_items":         maxItems,
		"max_size_mb":       maxSizeMB,
	})
}
