package indexer

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/health"
)

// ClientCore centralizes the per-indexer bookkeeping every concrete client
// needs: daily API/download limit tracking, persisted usage restore/refresh,
// response-time stats and the request rate limiter. Clients embed it so the
// logic exists once instead of being copy-pasted per implementation.
type ClientCore struct {
	name         string
	usageManager *UsageManager
	Limiter      *RequestLimiter

	mu                 sync.RWMutex
	apiLimit           int
	apiUsed            int
	apiRemaining       int
	downloadLimit      int
	downloadUsed       int
	downloadRemaining  int
	searchesCount      int
	totalResponseMS    int64
	throttledUntil     time.Time
	downloadProbeAfter time.Time
}

// Rate-limit cooldown bounds. An indexer that answers 429 without a usable
// Retry-After gets DefaultThrottleCooldown; one that asks for an implausibly
// long pause is capped, since a multi-hour ban applied from a single header we
// cannot verify would silently retire the indexer for the rest of the session.
const (
	DefaultThrottleCooldown = 60 * time.Second
	MaxThrottleCooldown     = 15 * time.Minute
)

// DownloadExhaustedProbeInterval bounds how long an indexer whose daily
// download budget is spent stays skipped for search before one request is let
// through anyway.
//
// The skip itself is worth having: a result we can never grab is a dead
// candidate, and offering it costs the user a failover hop per release. But the
// skip is self-sealing if made absolute. The only trustworthy view of the
// download budget is X-DNZBLimit-Daily-Remaining, which ApplyHeaderUsage reads
// off a response — so it arrives only when we make a request. Our own counter
// cannot stand in for it: the daily reset here turns over on local midnight
// while indexers use their own clock or a rolling window, and downloadLimit may
// be a conservative number the operator typed rather than the real quota. Left
// to itself, a wrong counter would retire a working indexer until the local day
// rolled over. The periodic probe is what lets the indexer tell us otherwise.
const DownloadExhaustedProbeInterval = 15 * time.Minute

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
		c.reportDegraded(health.ReasonQuotaExhausted, "daily API hit budget spent")
		return fmt.Errorf("API limit reached for %s: %w", displayName, ErrRateLimited)
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
		c.reportDegraded(health.ReasonQuotaExhausted, "daily NZB download budget spent")
		return fmt.Errorf("download limit reached for %s: %w", displayName, ErrRateLimited)
	}
	return nil
}

// CheckSearchAllowed runs the full search preflight shared by every client:
// the rate-limit cooldown, the daily API budget, and — because a release we
// cannot grab is not worth finding — the daily download budget.
func (c *ClientCore) CheckSearchAllowed(displayName string, now time.Time) error {
	if err := c.CheckThrottled(displayName, now); err != nil {
		return err
	}
	// Refreshes the persisted counters for both budgets, which the download
	// check below then reads without a second round through the state manager.
	if err := c.CheckAPILimit(displayName); err != nil {
		return err
	}
	return c.checkDownloadBudgetForSearch(displayName, now)
}

// CheckGrabAllowed runs the NZB-download preflight: cooldown plus the daily
// download budget.
func (c *ClientCore) CheckGrabAllowed(displayName string, now time.Time) error {
	if err := c.CheckThrottled(displayName, now); err != nil {
		return err
	}
	return c.CheckDownloadLimit(displayName)
}

// checkDownloadBudgetForSearch skips a search whose results could never be
// grabbed, letting one request through every DownloadExhaustedProbeInterval so
// the indexer's own headers can re-open it. Relies on the caller having just
// refreshed usage.
func (c *ClientCore) checkDownloadBudgetForSearch(displayName string, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.downloadLimit <= 0 || c.downloadRemaining > 0 {
		return nil
	}
	if !now.Before(c.downloadProbeAfter) {
		c.downloadProbeAfter = now.Add(DownloadExhaustedProbeInterval)
		return nil
	}
	c.reportDegraded(health.ReasonQuotaExhausted, "daily NZB download budget spent")
	return fmt.Errorf("download limit reached for %s, skipping search until %s: %w",
		displayName, c.downloadProbeAfter.Sub(now).Round(time.Second), ErrRateLimited)
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

// NoteThrottled opens a cooldown after an indexer refuses a request, honouring
// Retry-After when the response carries a usable one. Until it expires,
// CheckThrottled short-circuits further requests without a round trip — which
// is the point: playback failover walks candidates one at a time, so without a
// cooldown a single throttled indexer gets one full grab attempt per candidate
// it supplied, at whatever rate the failover loop runs.
func (c *ClientCore) NoteThrottled(h http.Header, now time.Time) time.Duration {
	cooldown := DefaultThrottleCooldown
	if d, ok := parseRetryAfter(h.Get("Retry-After"), now); ok {
		cooldown = d
	}
	if cooldown > MaxThrottleCooldown {
		cooldown = MaxThrottleCooldown
	}
	if cooldown <= 0 {
		cooldown = DefaultThrottleCooldown
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	// Never shorten a cooldown already in force: a burst of concurrent grabs
	// all come back 429 and the last one must not undercut the first.
	if until := now.Add(cooldown); until.After(c.throttledUntil) {
		c.throttledUntil = until
	}
	return c.throttledUntil.Sub(now)
}

// CheckThrottled reports the remaining cooldown as an ErrRateLimited error, or
// nil when the indexer is free to try again.
func (c *ClientCore) CheckThrottled(displayName string, now time.Time) error {
	c.mu.RLock()
	until := c.throttledUntil
	c.mu.RUnlock()
	if until.IsZero() || !now.Before(until) {
		return nil
	}
	c.reportDegraded(health.ReasonThrottled, "indexer asked us to back off")
	return fmt.Errorf("%s is in a rate-limit cooldown for another %s: %w", displayName, until.Sub(now).Round(time.Second), ErrRateLimited)
}

// reportDegraded records a self-healing limit against this indexer's health.
//
// Both conditions end on their own — a cooldown expires, a daily budget resets
// — so they never block the indexer; they exist so the user can see why an
// indexer went quiet instead of guessing. Repeat reports of an unchanged state
// stay in memory, so calling this from the per-search preflight is cheap.
func (c *ClientCore) reportDegraded(reason, detail string) {
	health.Global().Report(health.KindIndexer, c.name, health.StateDegraded, reason, detail)
}

// ThrottledUntil exposes the cooldown deadline for status reporting.
func (c *ClientCore) ThrottledUntil() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.throttledUntil
}

// parseRetryAfter reads both Retry-After forms: delta-seconds, and an HTTP-date
// deadline. A value that is absent, malformed, or already in the past yields
// ok=false so the caller falls back to its default.
func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(value); err == nil {
		if secs <= 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if deadline, err := http.ParseTime(value); err == nil {
		if d := deadline.Sub(now); d > 0 {
			return d, true
		}
	}
	return 0, false
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
