package indexer

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// ClientCore centralizes the per-indexer bookkeeping every concrete client
// needs: daily API/download limit tracking, persisted usage restore/refresh,
// response-time stats and the request rate limiter. Clients embed it so the
// logic exists once instead of being copy-pasted per implementation.
type ClientCore struct {
	name         string
	usageManager *UsageManager
	Limiter      *RequestLimiter

	mu                sync.RWMutex
	apiLimit          int
	apiUsed           int
	apiRemaining      int
	downloadLimit     int
	downloadUsed      int
	downloadRemaining int
	searchesCount     int
	totalResponseMS   int64
}

// NewClientCore builds the core and restores persisted usage counters for
// name when a usage manager is available.
func NewClientCore(name string, apiLimit, downloadLimit, rateLimitRPS int, um *UsageManager) *ClientCore {
	c := &ClientCore{
		name:              name,
		usageManager:      um,
		Limiter:           NewRequestLimiter(rateLimitRPS),
		apiLimit:          apiLimit,
		apiRemaining:      apiLimit,
		downloadLimit:     downloadLimit,
		downloadRemaining: downloadLimit,
	}
	if um != nil && name != "" {
		usage := um.GetIndexerUsage(name)
		c.apiUsed = usage.APIHitsUsed
		c.downloadUsed = usage.DownloadsUsed
		c.apiRemaining = apiLimit - usage.APIHitsUsed
		c.downloadRemaining = downloadLimit - usage.DownloadsUsed
		if c.apiRemaining < 0 && apiLimit > 0 {
			c.apiRemaining = 0
		}
		if c.downloadRemaining < 0 && downloadLimit > 0 {
			c.downloadRemaining = 0
		}
	}
	return c
}

// Usage assembles the current usage snapshot, refreshing persisted counters
// first so multiple processes/streams stay consistent.
func (c *ClientCore) Usage() Usage {
	usageData := c.RefreshUsage()

	c.mu.RLock()
	u := Usage{
		APIHitsLimit:       c.apiLimit,
		APIHitsUsed:        c.apiUsed,
		APIHitsRemaining:   c.apiRemaining,
		DownloadsLimit:     c.downloadLimit,
		DownloadsUsed:      c.downloadUsed,
		DownloadsRemaining: c.downloadRemaining,
		SearchesCount:      c.searchesCount,
	}
	if c.searchesCount > 0 {
		u.AvgResponseMS = float64(c.totalResponseMS) / float64(c.searchesCount)
	}
	c.mu.RUnlock()
	if usageData != nil {
		u.AllTimeAPIHitsUsed = usageData.AllTimeAPIHitsUsed
		u.AllTimeDownloadsUsed = usageData.AllTimeDownloadsUsed
	}
	return u
}

// RefreshUsage re-reads persisted counters and recomputes remaining budgets.
func (c *ClientCore) RefreshUsage() *UsageData {
	if c.usageManager == nil || c.name == "" {
		return nil
	}

	ud := c.usageManager.GetIndexerUsage(c.name)
	c.mu.Lock()
	c.apiUsed = ud.APIHitsUsed
	c.downloadUsed = ud.DownloadsUsed
	if c.apiLimit > 0 {
		c.apiRemaining = c.apiLimit - c.apiUsed
		if c.apiRemaining < 0 {
			c.apiRemaining = 0
		}
	}
	if c.downloadLimit > 0 {
		c.downloadRemaining = c.downloadLimit - c.downloadUsed
		if c.downloadRemaining < 0 {
			c.downloadRemaining = 0
		}
	}
	c.mu.Unlock()

	return ud
}

func (c *ClientCore) RecordSearchDuration(elapsed time.Duration) {
	ms := elapsed.Milliseconds()
	if ms < 0 {
		ms = 0
	}
	c.mu.Lock()
	c.searchesCount++
	c.totalResponseMS += ms
	c.mu.Unlock()
}

// CheckAPILimit returns an error when the configured daily API budget is
// exhausted; displayName appears in the error for the UI.
func (c *ClientCore) CheckAPILimit(displayName string) error {
	c.RefreshUsage()

	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.apiLimit > 0 && c.apiRemaining <= 0 {
		return fmt.Errorf("API limit reached for %s", displayName)
	}
	return nil
}

// CheckDownloadLimit returns an error when the configured daily download
// budget is exhausted.
func (c *ClientCore) CheckDownloadLimit(displayName string) error {
	c.RefreshUsage()

	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.downloadLimit > 0 && c.downloadRemaining <= 0 {
		return fmt.Errorf("download limit reached for %s", displayName)
	}
	return nil
}

// IncrementUsed records api/download hits against the persisted counters.
func (c *ClientCore) IncrementUsed(apiDelta, downloadDelta int) {
	if c.usageManager == nil || c.name == "" {
		return
	}
	c.usageManager.IncrementUsed(c.name, apiDelta, downloadDelta)
}

// RecordAPIHit accounts one search/API request: local counters, response
// header usage sync, and a persisted increment when no daily API limit is
// configured (limit-ful indexers persist via ApplyHeaderUsage's derived
// counts instead).
func (c *ClientCore) RecordAPIHit(h http.Header) {
	c.mu.Lock()
	c.apiUsed++
	if c.apiRemaining > 0 {
		c.apiRemaining--
	}
	c.mu.Unlock()

	c.ApplyHeaderUsage(h)

	c.mu.RLock()
	apiLimit := c.apiLimit
	c.mu.RUnlock()
	if c.usageManager != nil && apiLimit == 0 {
		c.IncrementUsed(1, 0)
	}
}

// RecordGrab accounts one NZB download (an API hit plus a download) with the
// same header-sync/persist rules as RecordAPIHit.
func (c *ClientCore) RecordGrab(h http.Header) {
	c.mu.Lock()
	c.apiUsed++
	c.downloadUsed++
	if c.apiRemaining > 0 {
		c.apiRemaining--
	}
	if c.downloadRemaining > 0 {
		c.downloadRemaining--
	}
	c.mu.Unlock()

	c.ApplyHeaderUsage(h)

	c.mu.RLock()
	apiLimit, downloadLimit := c.apiLimit, c.downloadLimit
	c.mu.RUnlock()
	if c.usageManager == nil {
		return
	}
	if apiLimit == 0 && downloadLimit == 0 {
		c.IncrementUsed(1, 1)
	} else if apiLimit == 0 {
		c.IncrementUsed(1, 0)
	}
}

// ApplyHeaderUsage ingests the de-facto newznab rate-limit response headers,
// preferring the standard X-RateLimit/X-DNZBLimit family over the x-api/x-grab
// fallbacks, and persists derived used-counts when limits are configured.
func (c *ClientCore) ApplyHeaderUsage(h http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if val := h.Get("X-RateLimit-Daily-Limit"); val != "" {
		if limit, err := strconv.Atoi(val); err == nil {
			c.apiLimit = limit
		}
	}
	if val := h.Get("X-RateLimit-Daily-Remaining"); val != "" {
		if remaining, err := strconv.Atoi(val); err == nil {
			c.apiRemaining = remaining
		}
	}

	if val := h.Get("X-DNZBLimit-Daily-Limit"); val != "" {
		if limit, err := strconv.Atoi(val); err == nil {
			c.downloadLimit = limit
		}
	}
	if val := h.Get("X-DNZBLimit-Daily-Remaining"); val != "" {
		if remaining, err := strconv.Atoi(val); err == nil {
			c.downloadRemaining = remaining
		}
	}

	if val := h.Get("x-api-remaining"); val != "" && h.Get("X-RateLimit-Daily-Remaining") == "" {
		if remaining, err := strconv.Atoi(val); err == nil {
			c.apiRemaining = remaining
		}
	}
	if val := h.Get("x-grab-remaining"); val != "" && h.Get("X-DNZBLimit-Daily-Remaining") == "" {
		if remaining, err := strconv.Atoi(val); err == nil {
			c.downloadRemaining = remaining
		}
	}

	if c.usageManager != nil && (c.apiLimit > 0 || c.downloadLimit > 0) {
		if c.apiLimit > 0 {
			c.apiUsed = c.apiLimit - c.apiRemaining
		}
		if c.downloadLimit > 0 {
			c.downloadUsed = c.downloadLimit - c.downloadRemaining
		}
		c.usageManager.UpdateUsage(c.name, c.apiUsed, c.downloadUsed)
	}
}
