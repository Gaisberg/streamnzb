package indexer

import (
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"sync"
	"time"
)

type UsageData struct {
	LastResetDay         string `json:"last_reset_day"`
	APIHitsUsed          int    `json:"api_hits_used"`
	DownloadsUsed        int    `json:"downloads_used"`
	AllTimeAPIHitsUsed   int    `json:"all_time_api_hits_used"`
	AllTimeDownloadsUsed int    `json:"all_time_downloads_used"`
}

type UsageManager struct {
	state *persistence.StateManager
	data  map[string]*UsageData
	mu    sync.RWMutex
}

var globalManager *UsageManager
var managerMu sync.Mutex

func GetUsageManager(sm *persistence.StateManager) (*UsageManager, error) {
	managerMu.Lock()
	defer managerMu.Unlock()

	if globalManager != nil {
		return globalManager, nil
	}

	m := &UsageManager{
		state: sm,
		data:  make(map[string]*UsageData),
	}

	if err := m.load(); err != nil {
		return nil, err
	}

	globalManager = m
	return m, nil
}

// FlushUsageManager persists in-memory usage to the database currently behind
// the state manager. Called before a database swap so counters recorded since
// the last save land in the database being left, and are carried across by the
// import rather than lost.
func FlushUsageManager() error {
	managerMu.Lock()
	m := globalManager
	managerMu.Unlock()
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.save()
}

// ReloadUsageManager re-reads usage after the database behind the state manager
// changed. Without it the manager keeps serving — and flushing — counters it
// loaded from the previous database.
func ReloadUsageManager() error {
	managerMu.Lock()
	m := globalManager
	managerMu.Unlock()
	if m == nil {
		return nil
	}
	return m.load()
}

func (m *UsageManager) load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, err := m.state.Get("indexer_usage", &m.data)
	if err != nil {
		return err
	}

	var needSave bool
	for _, data := range m.data {
		if data == nil {
			continue
		}
		if data.AllTimeAPIHitsUsed != 0 || data.AllTimeDownloadsUsed != 0 {
			continue
		}
		if data.APIHitsUsed > 0 || data.DownloadsUsed > 0 {
			data.AllTimeAPIHitsUsed = data.APIHitsUsed
			data.AllTimeDownloadsUsed = data.DownloadsUsed
			needSave = true
		}
	}
	if needSave {
		_ = m.state.Set("indexer_usage", m.data)
	}
	return nil
}

func (m *UsageManager) save() error {
	return m.state.Set("indexer_usage", m.data)
}

func (m *UsageManager) GetIndexerUsage(name string) *UsageData {
	m.mu.Lock()

	today := time.Now().Format("2006-01-02")
	data, ok := m.data[name]
	if !ok {
		data = &UsageData{LastResetDay: today}
		m.data[name] = data
		m.mu.Unlock()
		return data
	}

	reset := false
	if data.LastResetDay != today {
		logger.Debug("Resetting daily usage for indexer", "name", name, "last_reset", data.LastResetDay, "today", today)
		data.LastResetDay = today
		data.APIHitsUsed = 0
		data.DownloadsUsed = 0
		reset = true
	}
	m.mu.Unlock()

	if reset {
		if err := m.save(); err != nil {
			logger.Error("Failed to save reset usage data", "name", name, "err", err)
		}
	}

	return data
}

func (m *UsageManager) UpdateUsage(name string, apiHits, downloads int) {
	m.mu.Lock()

	today := time.Now().Format("2006-01-02")
	data, ok := m.data[name]
	if !ok {
		data = &UsageData{LastResetDay: today}
		m.data[name] = data
	}

	if data.LastResetDay != today {
		data.LastResetDay = today
		data.APIHitsUsed = 0
		data.DownloadsUsed = 0
	}

	deltaHits := apiHits - data.APIHitsUsed
	deltaDls := downloads - data.DownloadsUsed
	data.APIHitsUsed = apiHits
	data.DownloadsUsed = downloads
	if deltaHits > 0 {
		data.AllTimeAPIHitsUsed += deltaHits
	}
	if deltaDls > 0 {
		data.AllTimeDownloadsUsed += deltaDls
	}
	m.mu.Unlock()

	if err := m.save(); err != nil {
		logger.Error("Failed to save usage data", "err", err)
	}
}

func (m *UsageManager) IncrementUsed(name string, hits, downloads int) {
	m.mu.Lock()
	today := time.Now().Format("2006-01-02")
	data, ok := m.data[name]
	if !ok {
		data = &UsageData{LastResetDay: today}
		m.data[name] = data
	}

	if data.LastResetDay != today {
		data.LastResetDay = today
		data.APIHitsUsed = hits
		data.DownloadsUsed = downloads
	} else {
		data.APIHitsUsed += hits
		data.DownloadsUsed += downloads
	}
	data.AllTimeAPIHitsUsed += hits
	data.AllTimeDownloadsUsed += downloads
	m.mu.Unlock()

	if err := m.save(); err != nil {
		logger.Error("Failed to save usage data", "err", err)
	}
}

func (m *UsageManager) GetUsageByPrefix(prefix string) map[string]*UsageData {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*UsageData)
	for name, data := range m.data {
		if len(name) >= len(prefix) && name[:len(prefix)] == prefix {

			cp := *data
			result[name] = &cp
		}
	}
	return result
}

func (m *UsageManager) SyncUsage(activeNames []string) {
	m.mu.Lock()

	activeMap := make(map[string]bool)
	for _, name := range activeNames {
		activeMap[name] = true
	}

	isActive := func(name string) bool {
		if activeMap[name] {
			return true
		}

		for active := range activeMap {
			prefix := active + ": "
			if len(name) > len(prefix) && name[:len(prefix)] == prefix {
				return true
			}
		}
		return false
	}

	changed := false
	for name := range m.data {
		if !isActive(name) {
			logger.Info("Removing orphaned usage data for indexer", "name", name)
			delete(m.data, name)
			changed = true
		}
	}
	m.mu.Unlock()

	if changed {
		if err := m.save(); err != nil {
			logger.Error("Failed to save usage data after sync", "err", err)
		}
	}
}
