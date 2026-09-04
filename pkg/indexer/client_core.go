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

	mu sync.RWMutex
	// The configured limits are what the operator typed and never change; the
	// header limits are what the server last advertised about itself. The
	// enforced apiLimit/downloadLimit is always the stricter of the two, so a
	// server advertising a bigger quota than the operator allowed can never
	// talk us out of the configured cap.
	cfgAPILimit        int
	cfgDownloadLimit   int
	hdrAPILimit        int
	hdrDownloadLimit   int
	apiLimit           int
	apiUsed            int
	apiRemaining       int
	downloadLimit      int
	downloadUsed       int
	downloadRemaining  int
	searchesCount      int
	totalResponseMS    int64
	grabsCount         int
	totalGrabMS        int64
	throttledUntil     time.Time
	apiProbeAfter      time.Time
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

// QuotaExhaustedCooldown is the pause after an indexer states its daily quota
// is spent (newznab error 201). Unlike a bare 429 — usually a burst limiter
// that clears in seconds — a spent daily quota stays spent for hours, so
// retrying every DefaultThrottleCooldown hammers the API all day for nothing,
// and some indexers count the refused requests against the quota too.
const QuotaExhaustedCooldown = 30 * time.Minute

// BudgetExhaustedProbeInterval bounds how long an indexer whose daily API or
// download budget is spent stays skipped for search before one request is let
// through anyway.
//
// The skip itself is worth having: a search we cannot afford, or whose results
// we could never grab, is dead weight. But the skip is self-sealing if made
// absolute. The only trustworthy view of either budget is the server's own
// response headers, which ApplyHeaderUsage reads off a response — so they
// arrive only when we make a request. Our own counter cannot stand in for
// them: the trailing-window count tracks our own hits but not an indexer that
// meters differently or resets early, and a configured limit may be a
// conservative number the operator typed rather than the real quota. Left to
// itself, a wrong counter would retire a working indexer for up to a full
// window. The periodic probe is what lets the indexer tell us otherwise.
const BudgetExhaustedProbeInterval = 15 * time.Minute

// NewClientCore builds the core and restores persisted usage counters for
// name when a usage manager is available.
func NewClientCore(name string, apiLimit, downloadLimit, rateLimitRPS int, um *UsageManager) *ClientCore {
	c := &ClientCore{
		name:              name,
		usageManager:      um,
		Limiter:           NewRequestLimiter(rateLimitRPS),
		cfgAPILimit:       apiLimit,
		cfgDownloadLimit:  downloadLimit,
		apiLimit:          apiLimit,
		apiRemaining:      apiLimit,
		downloadLimit:     downloadLimit,
		downloadRemaining: downloadLimit,
	}
	if um != nil && name != "" {
		usage := um.Counts(name, time.Now())
		c.apiUsed = usage.APIHits
		c.downloadUsed = usage.Downloads
		if apiLimit > 0 {
			c.apiRemaining = clampRemaining(apiLimit - c.apiUsed)
		}
		if downloadLimit > 0 {
			c.downloadRemaining = clampRemaining(downloadLimit - c.downloadUsed)
		}
	}
	return c
}

func clampRemaining(remaining int) int {
	if remaining < 0 {
		return 0
	}
	return remaining
}

// effectiveLimit returns the stricter of an operator-configured daily limit
// and a server-advertised one; 0 means "no limit" on either side.
func effectiveLimit(configured, advertised int) int {
	switch {
	case configured <= 0:
		return advertised
	case advertised <= 0:
		return configured
	case advertised < configured:
		return advertised
	default:
		return configured
	}
}

// Usage assembles the current usage snapshot, refreshing persisted counters
// first so multiple processes/streams stay consistent.
func (c *ClientCore) Usage() Usage {
	counts := c.RefreshUsage()

	c.mu.RLock()
	u := Usage{
		APIHitsLimit:       c.apiLimit,
		APIHitsUsed:        c.apiUsed,
		APIHitsRemaining:   c.apiRemaining,
		DownloadsLimit:     c.downloadLimit,
		DownloadsUsed:      c.downloadUsed,
		DownloadsRemaining: c.downloadRemaining,
		SearchesCount:      c.searchesCount,
		GrabsCount:         c.grabsCount,
	}
	if c.searchesCount > 0 {
		u.AvgResponseMS = float64(c.totalResponseMS) / float64(c.searchesCount)
	}
	if c.grabsCount > 0 {
		u.AvgGrabMS = float64(c.totalGrabMS) / float64(c.grabsCount)
	}
	c.mu.RUnlock()
	if counts != nil {
		u.AllTimeAPIHitsUsed = counts.AllTimeAPIHits
		u.AllTimeDownloadsUsed = counts.AllTimeDownloads
	}
	return u
}

// RefreshUsage re-reads persisted counters and recomputes remaining budgets.
func (c *ClientCore) RefreshUsage() *UsageCounts {
	if c.usageManager == nil || c.name == "" {
		return nil
	}

	counts := c.usageManager.Counts(c.name, time.Now())
	c.mu.Lock()
	c.apiUsed = counts.APIHits
	c.downloadUsed = counts.Downloads
	if c.apiLimit > 0 {
		c.apiRemaining = clampRemaining(c.apiLimit - c.apiUsed)
	}
	if c.downloadLimit > 0 {
		c.downloadRemaining = clampRemaining(c.downloadLimit - c.downloadUsed)
	}
	c.mu.Unlock()

	return &counts
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

// RecordGrabDuration times one NZB download, the grab-side counterpart to
// RecordSearchDuration. Only grabs that handed back NZB bytes are timed: a
// refused or failed download measures the refusal, not how fast the indexer
// serves an NZB.
func (c *ClientCore) RecordGrabDuration(elapsed time.Duration) {
	ms := elapsed.Milliseconds()
	if ms < 0 {
		ms = 0
	}
	c.mu.Lock()
	c.grabsCount++
	c.totalGrabMS += ms
	c.mu.Unlock()
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
	// One refresh of the persisted counters serves both budget checks below.
	c.RefreshUsage()
	if err := c.checkAPIBudgetForSearch(displayName, now); err != nil {
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

// checkAPIBudgetForSearch skips a search once the daily API budget is spent,
// letting one request through every BudgetExhaustedProbeInterval so the
// indexer's own headers can re-open it — without the probe the block would be
// self-sealing until local midnight even after the indexer's window reset.
// Relies on the caller having just refreshed usage.
func (c *ClientCore) checkAPIBudgetForSearch(displayName string, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.apiLimit <= 0 || c.apiRemaining > 0 {
		return nil
	}
	if !now.Before(c.apiProbeAfter) {
		c.apiProbeAfter = now.Add(BudgetExhaustedProbeInterval)
		return nil
	}
	c.reportDegraded(health.ReasonQuotaExhausted, "daily API hit budget spent")
	return fmt.Errorf("API limit reached for %s, skipping search until %s: %w",
		displayName, c.apiProbeAfter.Sub(now).Round(time.Second), ErrRateLimited)
}

// checkDownloadBudgetForSearch skips a search whose results could never be
// grabbed, with the same probe-through valve as the API budget. Relies on the
// caller having just refreshed usage.
func (c *ClientCore) checkDownloadBudgetForSearch(displayName string, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.downloadLimit <= 0 || c.downloadRemaining > 0 {
		return nil
	}
	if !now.Before(c.downloadProbeAfter) {
		c.downloadProbeAfter = now.Add(BudgetExhaustedProbeInterval)
		return nil
	}
	c.reportDegraded(health.ReasonQuotaExhausted, "daily NZB download budget spent")
	return fmt.Errorf("download limit reached for %s, skipping search until %s: %w",
		displayName, c.downloadProbeAfter.Sub(now).Round(time.Second), ErrRateLimited)
}

// RecordAPIHit accounts one search/API request: local counters, the persisted
// trailing-window ring, then response header sync via ApplyHeaderUsage.
func (c *ClientCore) RecordAPIHit(h http.Header) {
	c.mu.Lock()
	c.apiUsed++
	if c.apiRemaining > 0 {
		c.apiRemaining--
	}
	c.mu.Unlock()

	c.recordHits(1, 0)
	c.ApplyHeaderUsage(h)
}

// RecordGrab accounts one NZB download (an API hit plus a download) with the
// same ring/header-sync rules as RecordAPIHit.
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

	c.recordHits(1, 1)
	c.ApplyHeaderUsage(h)
}

func (c *ClientCore) recordHits(apiHits, downloads int) {
	if c.usageManager != nil && c.name != "" {
		c.usageManager.RecordHits(c.name, apiHits, downloads, time.Now())
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
	return c.extendCooldown(now, cooldown)
}

// NoteQuotaExhausted opens the longer cooldown for an indexer that stated its
// daily quota is spent. A usable Retry-After still wins — the server knows its
// own reset better than our default — but without one the pause is
// QuotaExhaustedCooldown rather than the transient-throttle default.
func (c *ClientCore) NoteQuotaExhausted(h http.Header, now time.Time) time.Duration {
	if _, ok := parseRetryAfter(h.Get("Retry-After"), now); ok {
		return c.NoteThrottled(h, now)
	}
	c.reportDegraded(health.ReasonQuotaExhausted, "indexer reported its daily quota spent")
	return c.extendCooldown(now, QuotaExhaustedCooldown)
}

// extendCooldown pushes the throttle deadline out to now+cooldown, never
// shortening one already in force: a burst of concurrent grabs all come back
// 429 and the last one must not undercut the first.
func (c *ClientCore) extendCooldown(now time.Time, cooldown time.Duration) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
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
// fallbacks, and persists the server-derived counts as a header sync.
//
// The server's counters govern whenever they are usable — it sees consumption
// we cannot (other apps sharing the account) and resets we cannot (its own
// window, which our trailing-window count can only approximate) — but they
// never expand the budget past the configured cap: the enforced limit is the
// stricter of the configured and advertised limits, and a remaining count
// from a server that never told us its own limit is trusted only up to that
// cap, since a value beyond it says nothing about our share of the server's
// larger budget and deriving a used count from it would go negative and
// disable the cap entirely.
func (c *ClientCore) ApplyHeaderUsage(h http.Header) {
	c.mu.Lock()

	if limit, ok := headerInt(h, "X-RateLimit-Daily-Limit"); ok {
		c.hdrAPILimit = limit
	}
	if limit, ok := headerInt(h, "X-DNZBLimit-Daily-Limit"); ok {
		c.hdrDownloadLimit = limit
	}
	c.apiLimit = effectiveLimit(c.cfgAPILimit, c.hdrAPILimit)
	c.downloadLimit = effectiveLimit(c.cfgDownloadLimit, c.hdrDownloadLimit)

	// Only server-governed counts become a header sync; a header that was
	// ignored must not freeze the local trailing count as a stale baseline.
	var hdrAPIUsed, hdrDownloadsUsed *int
	if remaining, ok := firstHeaderInt(h, "X-RateLimit-Daily-Remaining", "x-api-remaining"); ok {
		var governed bool
		c.apiUsed, c.apiRemaining, governed = reconcileRemaining(c.apiUsed, c.apiRemaining, c.apiLimit, c.hdrAPILimit, remaining)
		if governed {
			used := c.apiUsed
			hdrAPIUsed = &used
		}
	}
	if remaining, ok := firstHeaderInt(h, "X-DNZBLimit-Daily-Remaining", "x-grab-remaining"); ok {
		var governed bool
		c.downloadUsed, c.downloadRemaining, governed = reconcileRemaining(c.downloadUsed, c.downloadRemaining, c.downloadLimit, c.hdrDownloadLimit, remaining)
		if governed {
			used := c.downloadUsed
			hdrDownloadsUsed = &used
		}
	}

	name, um := c.name, c.usageManager
	c.mu.Unlock()

	if um != nil && name != "" {
		um.SetHeaderCounts(name, hdrAPIUsed, hdrDownloadsUsed, time.Now())
	}
}

// reconcileRemaining folds one remaining-count header into the local counters.
// governed reports whether the server's view won — only then may the caller
// persist the result as an authoritative header sync.
//
// With the server's own limit known, the pair states the server's full view —
// including consumption by other apps on the account — so the server-derived
// used count wins and remaining is re-measured against the enforced limit.
// With only a remaining count, the server still governs in both directions as
// long as the value fits within the enforced limit: honouring it only downward
// left a locally-exhausted budget sealed for a full window even after the
// indexer's own window had reset, defeating the exhaustion probe. A value
// beyond the limit is ignored — the server's budget dwarfs the configured cap,
// so its remaining count says nothing about our share of it.
func reconcileRemaining(localUsed, localRemaining, limit, advertisedLimit, headerRemaining int) (used, remaining int, governed bool) {
	if headerRemaining < 0 {
		headerRemaining = 0
	}
	if advertisedLimit > 0 {
		used = clampRemaining(advertisedLimit - headerRemaining)
		if limit <= 0 {
			return used, headerRemaining, true
		}
		return used, clampRemaining(limit - used), true
	}
	if limit <= 0 {
		// Nothing to enforce; keep the local hit count for the stats and show
		// the server's remaining as-is.
		return localUsed, headerRemaining, false
	}
	if headerRemaining > limit {
		return localUsed, localRemaining, false
	}
	return clampRemaining(limit - headerRemaining), headerRemaining, true
}

// headerInt reads one header as an integer; absent or malformed yields ok=false.
func headerInt(h http.Header, key string) (int, bool) {
	val := strings.TrimSpace(h.Get(key))
	if val == "" {
		return 0, false
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return 0, false
	}
	return n, true
}

// firstHeaderInt returns the first of keys that parses as an integer.
func firstHeaderInt(h http.Header, keys ...string) (int, bool) {
	for _, key := range keys {
		if n, ok := headerInt(h, key); ok {
			return n, true
		}
	}
	return 0, false
}
