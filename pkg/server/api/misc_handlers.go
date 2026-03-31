package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
)

func (s *Server) handleGetIndexerCaps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	caps := s.indexerCaps
	s.mu.RUnlock()
	if caps == nil {
		caps = make(map[string]*indexer.Caps)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(caps)
}

func (s *Server) handleRefreshIndexerCaps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stream, _ := auth.StreamFromContext(r)
	if stream == nil || stream.Username != s.config.GetAdminUsername() {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	s.mu.RLock()
	idx := s.indexer
	s.mu.RUnlock()
	caps := make(map[string]*indexer.Caps)
	if agg, ok := idx.(*indexer.Aggregator); ok {
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, i := range agg.Indexers {
			if c, ok := i.(indexer.IndexerWithCaps); ok {
				wg.Add(1)
				go func(name string, fetcher indexer.IndexerWithCaps) {
					defer wg.Done()
					if result, err := fetcher.GetCaps(); err == nil {
						mu.Lock()
						caps[name] = result
						mu.Unlock()
					}
				}(i.Name(), c)
			}
		}
		wg.Wait()
	}
	s.mu.Lock()
	s.indexerCaps = caps
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(caps)
}

func (s *Server) handleCloseSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stream, _ := auth.StreamFromContext(r)
	if stream == nil || stream.Username != s.config.GetAdminUsername() {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	s.sessionMgr.DeleteSession(req.ID)
	logger.Debug("API closing session", "id", req.ID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stream, _ := auth.StreamFromContext(r)
	if stream == nil || stream.Username != s.config.GetAdminUsername() {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	go func() {

		exe, _ := os.Executable()
		cmd := exec.Command(exe)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.Start()
		os.Exit(0)
	}()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "Restarting..."})
}

func (s *Server) handleDownloadLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	logPath := logger.GetCurrentLogPath()
	info, err := os.Stat(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "Log file not found", http.StatusNotFound)
			return
		}
		logger.Error("Log download stat failed", "path", logPath, "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if !info.Mode().IsRegular() {
		http.Error(w, "Log file not found", http.StatusNotFound)
		return
	}

	filename := filepath.Base(logPath)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	http.ServeFile(w, r, logPath)
}

func (s *Server) handleNZBAttempts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	lister := s.attemptLister
	s.mu.RUnlock()
	if lister == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]persistence.NZBAttempt{})
		return
	}
	q := r.URL.Query()
	opts := persistence.ListAttemptsOptions{
		ContentType: q.Get("content_type"),
		ContentID:   q.Get("content_id"),
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.Limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			opts.Offset = n
		}
	}
	if v := q.Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			opts.Since = &t
		}
	}
	list, err := lister.ListAttempts(opts)
	if err != nil {
		logger.Error("ListAttempts failed", "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []persistence.NZBAttempt{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}
