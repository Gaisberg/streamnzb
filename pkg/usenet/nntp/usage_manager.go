package nntp

import (
	"sync"
	"sync/atomic"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
)

type ProviderUsageData struct {
	LastResetDay string `json:"last_reset_day"`
	TotalBytes   int64  `json:"total_bytes"`
	AllTimeBytes int64  `json:"all_time_bytes"`
}

// ProviderUsageManager tracks per-provider download volume in memory and
// persists it in the background. AddBytes runs on every chunk of the download
// hot path, so it never touches the database itself — it only marks the state
// dirty; a flusher goroutine persists at most once per interval, and pool
// shutdown flushes the final totals via FlushProvider.
type ProviderUsageManager struct {
	state   *persistence.StateManager
	data    map[string]*ProviderUsageData
	mu      sync.RWMutex
	flushMu sync.Mutex // single-flight: one persist at a time
	dirty   atomic.Bool
}

const providerUsageFlushInterval = 5 * time.Second

var providerManager *ProviderUsageManager
var providerManagerMu sync.Mutex

func GetProviderUsageManager(sm *persistence.StateManager) (*ProviderUsageManager, error) {
	providerManagerMu.Lock()
	defer providerManagerMu.Unlock()

	if providerManager != nil {
		return providerManager, nil
	}

	m := &ProviderUsageManager{
		state: sm,
		data:  make(map[string]*ProviderUsageData),
	}

	if err := m.load(); err != nil {
		return nil, err
	}
	go m.flushLoop()

	providerManager = m
	return m, nil
}

// FlushProviderUsage persists pending usage to the database currently behind
// the state manager, so a database swap does not strand the bytes counted since
// the last flush tick.
func FlushProviderUsage() error {
	providerManagerMu.Lock()
	m := providerManager
	providerManagerMu.Unlock()
	if m == nil {
		return nil
	}
	return m.flushIfDirty()
}

// ReloadProviderUsage re-reads usage after the database behind the state
// manager changed. The flush loop keeps running against the manager, so without
// this it would write totals from the previous database into the new one.
func ReloadProviderUsage() error {
	providerManagerMu.Lock()
	m := providerManager
	providerManagerMu.Unlock()
	if m == nil {
		return nil
	}
	return m.load()
}

func (m *ProviderUsageManager) load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, err := m.state.Get("provider_usage", &m.data)
	if err != nil {
		return err
	}

	today := time.Now().Format("2006-01-02")
	needSave := false
	for _, data := range m.data {
		if data == nil {
			continue
		}
		if m.resetIfNeededLocked(data, today) {
			needSave = true
		}
	}
	if needSave {
		_ = m.state.Set("provider_usage", m.data)
	}
	return nil
}

// flushLoop persists dirty usage data once per interval for the life of the
// process. Final totals are covered by FlushProvider on pool shutdown.
func (m *ProviderUsageManager) flushLoop() {
	ticker := time.NewTicker(providerUsageFlushInterval)
	defer ticker.Stop()
	for range ticker.C {
		if err := m.flushIfDirty(); err != nil {
			logger.Error("Failed to save provider usage data", "err", err)
		}
	}
}

// flushIfDirty persists the current state when something changed since the
// last successful persist. On failure the dirty flag is restored so the next
// tick retries.
func (m *ProviderUsageManager) flushIfDirty() error {
	m.flushMu.Lock()
	defer m.flushMu.Unlock()
	if !m.dirty.Swap(false) {
		return nil
	}
	if err := m.save(); err != nil {
		m.dirty.Store(true)
		return err
	}
	return nil
}

func (m *ProviderUsageManager) save() error {
	m.mu.RLock()
	snapshot := make(map[string]*ProviderUsageData, len(m.data))
	for name, d := range m.data {
		if d == nil {
			continue
		}
		copied := *d
		snapshot[name] = &copied
	}
	m.mu.RUnlock()
	return m.state.Set("provider_usage", snapshot)
}

func (m *ProviderUsageManager) GetUsage(name string) *ProviderUsageData {
	today := time.Now().Format("2006-01-02")
	m.mu.Lock()
	data, reset := m.ensureUsageLocked(name, today)
	m.mu.Unlock()

	if reset {
		m.dirty.Store(true)
	}
	return data
}

func (m *ProviderUsageManager) AddBytes(name string, delta int64) {
	if delta <= 0 {
		return
	}
	today := time.Now().Format("2006-01-02")
	m.mu.Lock()
	data, _ := m.ensureUsageLocked(name, today)
	data.TotalBytes += delta
	data.AllTimeBytes += delta
	m.mu.Unlock()

	m.dirty.Store(true)
}

// FlushProvider persists any pending usage immediately. Called on pool
// shutdown so the final totals survive the process exiting between ticks.
func (m *ProviderUsageManager) FlushProvider(name string) {
	today := time.Now().Format("2006-01-02")
	m.mu.Lock()
	if data, ok := m.data[name]; ok && data != nil {
		if m.resetIfNeededLocked(data, today) {
			m.dirty.Store(true)
		}
	}
	m.mu.Unlock()

	if err := m.flushIfDirty(); err != nil {
		logger.Error("Failed to flush provider usage data", "name", name, "err", err)
	}
}

func (m *ProviderUsageManager) SyncUsage(activeNames []string) {
	m.mu.Lock()

	activeMap := make(map[string]bool)
	for _, name := range activeNames {
		activeMap[name] = true
	}

	changed := false
	for name := range m.data {
		if !activeMap[name] {
			logger.Info("Removing orphaned usage data for provider", "name", name)
			delete(m.data, name)
			changed = true
		}
	}
	m.mu.Unlock()

	if changed {
		m.dirty.Store(true)
		if err := m.flushIfDirty(); err != nil {
			logger.Error("Failed to save provider usage data after sync", "err", err)
		}
	}
}

func (m *ProviderUsageManager) ensureUsageLocked(name, today string) (*ProviderUsageData, bool) {
	data, ok := m.data[name]
	if !ok {
		data = &ProviderUsageData{LastResetDay: today}
		m.data[name] = data
		return data, false
	}

	return data, m.resetIfNeededLocked(data, today)
}

func (m *ProviderUsageManager) resetIfNeededLocked(data *ProviderUsageData, today string) bool {
	if data == nil {
		return false
	}

	if data.LastResetDay == "" {
		if data.TotalBytes > data.AllTimeBytes {
			data.AllTimeBytes = data.TotalBytes
		}
		data.LastResetDay = today
		data.TotalBytes = 0
		return true
	}

	if data.LastResetDay == today {
		return false
	}

	logger.Debug("Resetting daily usage for provider", "last_reset", data.LastResetDay, "today", today)
	data.LastResetDay = today
	data.TotalBytes = 0
	return true
}
