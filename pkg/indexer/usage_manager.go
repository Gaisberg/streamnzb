package indexer

import (
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"sync"
	"time"
)

// UsageWindow is the trailing window a "daily" indexer budget is measured
// over. Real indexers meter either a rolling 24 hours or a fixed reset on
// their own clock; a trailing window matches the former exactly and stays
// within hours of the latter, where the previous local-midnight calendar
// reset drifted against both.
const UsageWindow = 24 * time.Hour

// maxHitTimes caps one indexer's stored hit-timestamp ring. The window prune
// already bounds it by actual daily volume; this is a backstop so an
// unlimited, hammered indexer cannot grow the persisted state without bound.
const maxHitTimes = 10000

type UsageData struct {
	// APIHitTimes/DownloadTimes are the unix seconds of our own requests
	// inside the trailing UsageWindow; the "daily" counts are their lengths.
	APIHitTimes   []int64 `json:"api_hit_times,omitempty"`
	DownloadTimes []int64 `json:"download_times,omitempty"`

	// HeaderAPIUsed/HeaderDownloadsUsed hold the server-derived absolute used
	// counts as of the matching SyncedAt, from the last response that carried
	// usage headers. While a sync is younger than UsageWindow it governs: the
	// effective count is the synced value plus our own hits recorded after it.
	HeaderAPIUsed           int   `json:"header_api_used,omitempty"`
	HeaderAPISyncedAt       int64 `json:"header_api_synced_at,omitempty"`
	HeaderDownloadsUsed     int   `json:"header_downloads_used,omitempty"`
	HeaderDownloadsSyncedAt int64 `json:"header_downloads_synced_at,omitempty"`

	AllTimeAPIHitsUsed   int `json:"all_time_api_hits_used"`
	AllTimeDownloadsUsed int `json:"all_time_downloads_used"`

	// Legacy calendar-day counters, decoded only so load can migrate a
	// database written before the trailing-window model.
	LastResetDay  string `json:"last_reset_day,omitempty"`
	APIHitsUsed   int    `json:"api_hits_used,omitempty"`
	DownloadsUsed int    `json:"downloads_used,omitempty"`
}

// UsageCounts is the manager's computed view of one indexer's usage: hits in
// the trailing window (or the server's own count where a fresh header sync
// exists), plus the all-time totals.
type UsageCounts struct {
	APIHits          int
	Downloads        int
	AllTimeAPIHits   int
	AllTimeDownloads int
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
	snap := m.snapshotLocked()
	m.mu.RUnlock()
	return m.persist(snap)
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
	now := time.Now()
	today := now.Format("2006-01-02")
	for _, data := range m.data {
		if data == nil {
			continue
		}
		if migrateLegacyUsage(data, today, now) {
			needSave = true
		}
	}
	if needSave {
		_ = m.persist(m.snapshotLocked())
	}
	return nil
}

// migrateLegacyUsage converts one pre-trailing-window entry in place and
// reports whether it changed. Counters from the current calendar day are
// seeded as hits at load time — overstaying their window by up to a day, but
// never handing back a spent budget early — while a stale day is dropped, as
// the old midnight reset would have done anyway.
func migrateLegacyUsage(data *UsageData, today string, now time.Time) bool {
	if data.LastResetDay == "" && data.APIHitsUsed == 0 && data.DownloadsUsed == 0 {
		return false
	}
	// Databases from before the all-time counters existed carry the daily
	// counts as the only usage ever recorded; fold them in once.
	if data.AllTimeAPIHitsUsed == 0 && data.AllTimeDownloadsUsed == 0 &&
		(data.APIHitsUsed > 0 || data.DownloadsUsed > 0) {
		data.AllTimeAPIHitsUsed = data.APIHitsUsed
		data.AllTimeDownloadsUsed = data.DownloadsUsed
	}
	if data.LastResetDay == today {
		data.APIHitTimes = seedHitTimes(data.APIHitTimes, data.APIHitsUsed, now)
		data.DownloadTimes = seedHitTimes(data.DownloadTimes, data.DownloadsUsed, now)
	}
	data.LastResetDay = ""
	data.APIHitsUsed = 0
	data.DownloadsUsed = 0
	return true
}

func seedHitTimes(times []int64, count int, now time.Time) []int64 {
	if count > maxHitTimes {
		count = maxHitTimes
	}
	ts := now.Unix()
	for i := 0; i < count; i++ {
		times = append(times, ts)
	}
	return times
}

// snapshotLocked deep-copies the usage map so it can be serialized after the
// lock is released. Handing the live map to the state manager raced with
// concurrent counter updates — a torn value at best, a concurrent map
// iteration fault at worst. Callers must hold m.mu (read or write).
func (m *UsageManager) snapshotLocked() map[string]*UsageData {
	snap := make(map[string]*UsageData, len(m.data))
	for name, data := range m.data {
		if data == nil {
			continue
		}
		cp := *data
		cp.APIHitTimes = append([]int64(nil), data.APIHitTimes...)
		cp.DownloadTimes = append([]int64(nil), data.DownloadTimes...)
		snap[name] = &cp
	}
	return snap
}

// persist writes one snapshot to the state manager, outside any lock.
func (m *UsageManager) persist(snap map[string]*UsageData) error {
	return m.state.Set("indexer_usage", snap)
}

func (m *UsageManager) ensureLocked(name string) *UsageData {
	data, ok := m.data[name]
	if !ok || data == nil {
		data = &UsageData{}
		m.data[name] = data
	}
	return data
}

// RecordHits appends our own requests to the indexer's timestamp rings and
// bumps the all-time totals.
func (m *UsageManager) RecordHits(name string, apiHits, downloads int, now time.Time) {
	if name == "" || (apiHits <= 0 && downloads <= 0) {
		return
	}
	m.mu.Lock()
	data := m.ensureLocked(name)
	pruneUsageLocked(data, now)
	ts := now.Unix()
	for i := 0; i < apiHits; i++ {
		data.APIHitTimes = append(data.APIHitTimes, ts)
	}
	for i := 0; i < downloads; i++ {
		data.DownloadTimes = append(data.DownloadTimes, ts)
	}
	data.APIHitTimes = capHitTimes(data.APIHitTimes)
	data.DownloadTimes = capHitTimes(data.DownloadTimes)
	if apiHits > 0 {
		data.AllTimeAPIHitsUsed += apiHits
	}
	if downloads > 0 {
		data.AllTimeDownloadsUsed += downloads
	}
	snap := m.snapshotLocked()
	m.mu.Unlock()

	if err := m.persist(snap); err != nil {
		logger.Error("Failed to save usage data", "err", err)
	}
}

// SetHeaderCounts stores the server-derived absolute used counts read off a
// response's usage headers; a nil count means that header was not seen and
// leaves the previous sync in place. All-time totals are untouched — they
// count our own requests, not the account's.
func (m *UsageManager) SetHeaderCounts(name string, apiUsed, downloadsUsed *int, now time.Time) {
	if name == "" || (apiUsed == nil && downloadsUsed == nil) {
		return
	}
	m.mu.Lock()
	data := m.ensureLocked(name)
	ts := now.Unix()
	if apiUsed != nil {
		data.HeaderAPIUsed = *apiUsed
		data.HeaderAPISyncedAt = ts
	}
	if downloadsUsed != nil {
		data.HeaderDownloadsUsed = *downloadsUsed
		data.HeaderDownloadsSyncedAt = ts
	}
	snap := m.snapshotLocked()
	m.mu.Unlock()

	if err := m.persist(snap); err != nil {
		logger.Error("Failed to save usage data", "err", err)
	}
}

// Counts computes the effective usage: hits inside the trailing window, or —
// while a header sync is younger than the window — the server's own count
// plus our hits recorded after it. A sync older than the window is ignored
// rather than pruned; the next response overwrites it anyway.
func (m *UsageManager) Counts(name string, now time.Time) UsageCounts {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, ok := m.data[name]
	if !ok || data == nil {
		return UsageCounts{}
	}
	pruneUsageLocked(data, now)

	counts := UsageCounts{
		APIHits:          len(data.APIHitTimes),
		Downloads:        len(data.DownloadTimes),
		AllTimeAPIHits:   data.AllTimeAPIHitsUsed,
		AllTimeDownloads: data.AllTimeDownloadsUsed,
	}
	cutoff := now.Add(-UsageWindow).Unix()
	if data.HeaderAPISyncedAt > 0 && data.HeaderAPISyncedAt >= cutoff {
		counts.APIHits = data.HeaderAPIUsed + countAfter(data.APIHitTimes, data.HeaderAPISyncedAt)
	}
	if data.HeaderDownloadsSyncedAt > 0 && data.HeaderDownloadsSyncedAt >= cutoff {
		counts.Downloads = data.HeaderDownloadsUsed + countAfter(data.DownloadTimes, data.HeaderDownloadsSyncedAt)
	}
	return counts
}

func pruneUsageLocked(data *UsageData, now time.Time) {
	cutoff := now.Add(-UsageWindow).Unix()
	data.APIHitTimes = pruneHitTimes(data.APIHitTimes, cutoff)
	data.DownloadTimes = pruneHitTimes(data.DownloadTimes, cutoff)
}

func pruneHitTimes(times []int64, cutoff int64) []int64 {
	kept := times[:0]
	for _, t := range times {
		if t >= cutoff {
			kept = append(kept, t)
		}
	}
	return capHitTimes(kept)
}

func capHitTimes(times []int64) []int64 {
	if len(times) > maxHitTimes {
		times = times[len(times)-maxHitTimes:]
	}
	return times
}

// countAfter counts hits recorded strictly after a header sync. A hit in the
// same second as the sync is treated as already inside the server's count.
func countAfter(times []int64, after int64) int {
	n := 0
	for _, t := range times {
		if t > after {
			n++
		}
	}
	return n
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
	var snap map[string]*UsageData
	if changed {
		snap = m.snapshotLocked()
	}
	m.mu.Unlock()

	if snap != nil {
		if err := m.persist(snap); err != nil {
			logger.Error("Failed to save usage data after sync", "err", err)
		}
	}
}
