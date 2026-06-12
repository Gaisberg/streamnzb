package pool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/media/decode"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/usenet/nntp"

	"golang.org/x/sync/singleflight"
)

type countReader struct {
	io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.Reader.Read(p)
	c.n += int64(n)
	return n, err
}

var (
	ErrNoProvidersConfigured = errors.New("usenet/pool: no providers configured")
	ErrNoProvidersAvailable  = errors.New("usenet/pool: no providers available")
)

// isArticleNotFound reports whether err indicates 430 No Such Article (article missing on server).
func isArticleNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "430") || strings.Contains(s, "no such article")
}

// IsArticleNotFoundError reports whether err indicates 430 No Such Article.
func IsArticleNotFoundError(err error) bool {
	return isArticleNotFound(err)
}

func shouldCacheFetchedSegment(ctx context.Context) bool {
	return ctx == nil || ctx.Err() == nil
}

type ProviderConfig struct {
	ID         string
	Priority   int
	IsBackup   bool
	ClientPool *nntp.ClientPool
}

type Config struct {
	Providers                  []ProviderConfig
	SegmentCache               SegmentCache
	PermanentMissingMaxEntries int
}

type Pool struct {
	providers            []ProviderConfig
	cache                SegmentCache
	sf                   *singleflight.Group
	missing              *permanentMissingSegments
	providerSig          string
	articleStats         map[string]*providerArticleCounter
	consecutive430s      map[string]int
	consecutiveSuccesses map[string]int
	mu                   sync.RWMutex
	activeFetches        atomic.Int64
}

type providerArticleCounter struct {
	host             string
	availableCount   atomic.Int64
	unavailableCount atomic.Int64
}

type ProviderArticleStats struct {
	ProviderID       string
	Host             string
	AvailableCount   int64
	UnavailableCount int64
}

type permanentMissingSegments struct {
	mu         sync.RWMutex
	m          map[string]time.Time
	maxEntries int
}

const defaultPermanentMissingMaxEntries = 50000

func newPermanentMissingSegments(maxEntries int) *permanentMissingSegments {
	if maxEntries <= 0 {
		maxEntries = defaultPermanentMissingMaxEntries
	}
	return &permanentMissingSegments{
		m:          make(map[string]time.Time),
		maxEntries: maxEntries,
	}
}

func (p *permanentMissingSegments) has(key string) bool {
	p.mu.RLock()
	_, ok := p.m[key]
	p.mu.RUnlock()
	return ok
}

func (p *permanentMissingSegments) delete(key string) {
	p.mu.Lock()
	delete(p.m, key)
	p.mu.Unlock()
}

func (p *permanentMissingSegments) add(key string) {
	now := time.Now()
	p.mu.Lock()
	p.m[key] = now
	for len(p.m) > p.maxEntries {
		var oldestKey string
		var oldest time.Time
		for k, insertedAt := range p.m {
			if oldestKey == "" || insertedAt.Before(oldest) {
				oldestKey = k
				oldest = insertedAt
			}
		}
		if oldestKey == "" {
			break
		}
		delete(p.m, oldestKey)
	}
	p.mu.Unlock()
}

func providerSignature(providers []ProviderConfig) string {
	ids := make([]string, 0, len(providers))
	for i := range providers {
		if id := strings.TrimSpace(providers[i].ID); id != "" {
			ids = append(ids, id)
		}
	}
	return strings.Join(ids, ",")
}

func (p *Pool) missingKey(messageID string) string {
	return p.providerSig + "|" + strings.TrimSpace(messageID)
}

func (p *Pool) isKnownMissing(messageID string) bool {
	if p == nil || p.missing == nil {
		return false
	}
	key := p.missingKey(messageID)
	return p.missing.has(key)
}

func (p *Pool) markKnownMissing(messageID string) {
	if p == nil || p.missing == nil {
		return
	}
	key := p.missingKey(messageID)
	p.missing.add(key)
}

func (p *Pool) clearKnownMissing(messageID string) {
	if p == nil || p.missing == nil {
		return
	}
	key := p.missingKey(messageID)
	p.missing.delete(key)
}

type attemptedProvidersError struct {
	err   error
	hosts []string
}

func (e *attemptedProvidersError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *attemptedProvidersError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// AttemptedProviderHosts returns the provider hosts that were actually attempted before err was returned.
func AttemptedProviderHosts(err error) []string {
	var attemptedErr *attemptedProvidersError
	if !errors.As(err, &attemptedErr) || attemptedErr == nil || len(attemptedErr.hosts) == 0 {
		return nil
	}
	return append([]string(nil), attemptedErr.hosts...)
}

func wrapAttemptedProviders(err error, hosts []string) error {
	if err == nil {
		return nil
	}
	hosts = appendUniqueHosts(nil, hosts...)
	if len(hosts) == 0 {
		return err
	}
	return &attemptedProvidersError{
		err:   err,
		hosts: hosts,
	}
}

func appendUniqueHosts(dst []string, hosts ...string) []string {
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		exists := false
		for _, existing := range dst {
			if existing == host {
				exists = true
				break
			}
		}
		if !exists {
			dst = append(dst, host)
		}
	}
	return dst
}

func (p *Pool) attemptedAllProviderIDs(attemptedIDs []string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.providers) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(attemptedIDs))
	for _, id := range attemptedIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		seen[id] = struct{}{}
	}
	if len(seen) == 0 {
		return false
	}
	for i := range p.providers {
		id := strings.TrimSpace(p.providers[i].ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; !ok {
			return false
		}
	}
	return true
}

type PoolProviderTraceSnapshot struct {
	ID     string
	Host   string
	Total  int
	Idle   int
	Active int
}

type PoolTraceSnapshot struct {
	InFlightFetches int64
	Cache           CacheStats
	Providers       []PoolProviderTraceSnapshot
}

func (s PoolTraceSnapshot) CacheSummary() string {
	if s.Cache.BudgetMax > 0 {
		return fmt.Sprintf("entries=%d bytes=%d budget=%d/%d", s.Cache.Entries, s.Cache.Bytes, s.Cache.BudgetCurrent, s.Cache.BudgetMax)
	}
	return fmt.Sprintf("entries=%d bytes=%d", s.Cache.Entries, s.Cache.Bytes)
}

func (s PoolTraceSnapshot) ProviderSummary() string {
	if len(s.Providers) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(s.Providers))
	for _, provider := range s.Providers {
		parts = append(parts, fmt.Sprintf("%s(host=%s total=%d idle=%d active=%d)", provider.ID, provider.Host, provider.Total, provider.Idle, provider.Active))
	}
	return strings.Join(parts, "; ")
}

func cacheStats(cache SegmentCache) CacheStats {
	if statser, ok := cache.(segmentCacheStatser); ok {
		return statser.Stats()
	}
	return CacheStats{}
}

func NewPool(cfg *Config) (*Pool, error) {
	if cfg == nil || len(cfg.Providers) == 0 {
		return nil, ErrNoProvidersConfigured
	}
	providers := make([]ProviderConfig, len(cfg.Providers))
	copy(providers, cfg.Providers)
	sort.Slice(providers, func(i, j int) bool {
		return providers[i].Priority < providers[j].Priority
	})
	cache := cfg.SegmentCache
	if cache == nil {
		cache = NoopSegmentCache()
	}
	articleStats := make(map[string]*providerArticleCounter, len(providers))
	for i := range providers {
		providerID := strings.TrimSpace(providers[i].ID)
		if providerID == "" {
			continue
		}
		host := ""
		if providers[i].ClientPool != nil {
			host = providers[i].ClientPool.Host()
		}
		articleStats[providerID] = &providerArticleCounter{host: host}
	}
	return &Pool{
		providers:            providers,
		cache:                cache,
		sf:                   &singleflight.Group{},
		missing:              newPermanentMissingSegments(cfg.PermanentMissingMaxEntries),
		providerSig:          providerSignature(providers),
		articleStats:         articleStats,
		consecutive430s:      make(map[string]int),
		consecutiveSuccesses: make(map[string]int),
	}, nil
}

func (p *Pool) recordArticleResult(providerID string, available bool) {
	providerID = strings.TrimSpace(providerID)
	if p == nil || providerID == "" {
		return
	}
	p.mu.RLock()
	counter := p.articleStats[providerID]
	p.mu.RUnlock()
	if counter == nil {
		p.mu.Lock()
		if p.articleStats == nil {
			p.articleStats = make(map[string]*providerArticleCounter)
		}
		counter = p.articleStats[providerID]
		if counter == nil {
			host := ""
			for i := range p.providers {
				if p.providers[i].ID == providerID && p.providers[i].ClientPool != nil {
					host = p.providers[i].ClientPool.Host()
					break
				}
			}
			counter = &providerArticleCounter{host: host}
			p.articleStats[providerID] = counter
		}
		p.mu.Unlock()
	}
	if available {
		counter.availableCount.Add(1)
		return
	}
	counter.unavailableCount.Add(1)
}

// RecordProviderArticleResult records an article operation outcome for a provider.
// available=true increments available count; false increments missing count.
func (p *Pool) RecordProviderArticleResult(providerID string, available bool) {
	p.recordArticleResult(providerID, available)
}

func (p *Pool) record430Error(providerID string) {
	providerID = strings.TrimSpace(providerID)
	if p == nil || providerID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.consecutive430s == nil {
		p.consecutive430s = make(map[string]int)
	}
	p.consecutive430s[providerID]++

	if p.consecutiveSuccesses == nil {
		p.consecutiveSuccesses = make(map[string]int)
	}
	p.consecutiveSuccesses[providerID] = 0

	logger.Debug("provider demotion increment consecutive 430", "provider", providerID, "count", p.consecutive430s[providerID])
}

func (p *Pool) recordSuccess(providerID string) {
	providerID = strings.TrimSpace(providerID)
	if p == nil || providerID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.consecutiveSuccesses == nil {
		p.consecutiveSuccesses = make(map[string]int)
	}
	p.consecutiveSuccesses[providerID]++

	if p.consecutiveSuccesses[providerID] >= 10 {
		if p.consecutive430s != nil && p.consecutive430s[providerID] > 0 {
			logger.Debug("provider demotion reset consecutive 430 after 10 consecutive successes", "provider", providerID)
			p.consecutive430s[providerID] = 0
		}
	}
}

func (p *Pool) ProviderArticleStats() []ProviderArticleStats {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	ids := make([]string, 0, len(p.articleStats))
	for providerID := range p.articleStats {
		ids = append(ids, providerID)
	}
	sort.Strings(ids)
	out := make([]ProviderArticleStats, 0, len(ids))
	for _, providerID := range ids {
		counter := p.articleStats[providerID]
		if counter == nil {
			continue
		}
		out = append(out, ProviderArticleStats{
			ProviderID:       providerID,
			Host:             counter.host,
			AvailableCount:   counter.availableCount.Load(),
			UnavailableCount: counter.unavailableCount.Load(),
		})
	}
	p.mu.RUnlock()
	return out
}

func (p *Pool) FetchSegment(ctx context.Context, segment *nzb.Segment, groups []string) (SegmentData, error) {
	messageID := strings.TrimSpace(segment.ID)
	if messageID == "" {
		return SegmentData{}, fmt.Errorf("empty segment message ID")
	}
	if p.sf == nil {
		p.mu.Lock()
		if p.sf == nil {
			p.sf = &singleflight.Group{}
		}
		p.mu.Unlock()
	}

	v, err, _ := p.sf.Do(messageID, func() (interface{}, error) {
		return p.fetchSegmentOnce(ctx, messageID, segment, groups)
	})
	if err != nil {
		return SegmentData{}, err
	}
	return v.(SegmentData), nil
}

// FetchSegmentFirst tries all providers in parallel for the first segment (e.g. segment 0).
// It returns as soon as one provider succeeds, or the last error if all fail.
// Call this for segment 0 to reduce latency when the article is missing on all providers.
func (p *Pool) FetchSegmentFirst(ctx context.Context, segment *nzb.Segment, groups []string) (SegmentData, error) {
	messageID := strings.TrimSpace(segment.ID)
	if messageID == "" {
		return SegmentData{}, fmt.Errorf("empty segment message ID")
	}
	if p.isKnownMissing(messageID) {
		return SegmentData{}, fmt.Errorf("fetch segment %s: 430 No Such Article (cached)", messageID)
	}
	if data, ok := p.cache.Get(messageID); ok {
		logger.Trace("fetch segment cache hit", "message_id", messageID)
		return data, nil
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	p.mu.RLock()
	providers := p.providers
	p.mu.RUnlock()

	// Exclude set for each provider: all other provider IDs so getConnection returns that provider.
	providerIDs := make([]string, len(providers))
	for i := range providers {
		providerIDs[i] = providers[i].ID
	}

	type segResult struct {
		data       SegmentData
		err        error
		host       string
		providerID string
	}
	ch := make(chan segResult, len(providers))

	for i := range providers {
		exclude := make([]string, 0, len(providers)-1)
		for j := range providerIDs {
			if j != i {
				exclude = append(exclude, providerIDs[j])
			}
		}
		go func(exclude []string) {
			conn, release, discard, providerID, err := p.getConnection(fetchCtx, exclude, 999, false)
			if err != nil {
				ch <- segResult{err: err}
				return
			}
			host := p.Host(providerID)
			p.activeFetches.Add(1)
			defer p.activeFetches.Add(-1)

			// Connection leak guard: if fetchCtx is cancelled (e.g. another provider succeeded
			// or the caller gave up), discard the connection to interrupt the blocking read.
			stopWatch := make(chan struct{})
			go func() {
				select {
				case <-fetchCtx.Done():
					discard()
				case <-stopWatch:
				}
			}()
			defer func() {
				close(stopWatch)
				release()
			}()

			if len(groups) > 0 {
				if err := conn.Group(groups[0]); err != nil {
					logger.Debug("fetch segment group failed", "provider", providerID, "err", err)
					ch <- segResult{err: err, host: host, providerID: providerID}
					return
				}
			}
			r, err := conn.Body(messageID)
			if err != nil {
				logger.Debug("fetch segment body failed", "provider", providerID, "err", err)
				ch <- segResult{err: err, host: host, providerID: providerID}
				return
			}
			cr := &countReader{Reader: r}
			frame, err := decode.DecodeToBytes(cr)
			// Close ensures EndResponse is called even if decode stopped before EOF.
			r.Close()
			if err != nil {
				ctxErr := fetchCtx.Err()
				if ctxErr != nil {
					logger.Trace("fetch segment decode aborted",
						"provider", providerID,
						"err", err,
						"message_id", messageID,
						"raw_body_bytes", cr.n,
						"ctx_err", ctxErr)
				} else {
					logger.Debug("fetch segment decode failed",
						"provider", providerID,
						"err", err,
						"message_id", messageID,
						"raw_body_bytes", cr.n,
						"ctx_err", ctxErr)
				}
				ch <- segResult{err: err, host: host, providerID: providerID}
				return
			}
			ch <- segResult{data: SegmentData{
				Body:         frame.Data,
				Size:         int64(len(frame.Data)),
				ProviderHost: host,
			}, providerID: providerID}
		}(exclude)
	}

	var lastErr error
	var attempted []string
	var articleNotFoundErr error
	sawNonArticleNotFound := false
	for range providers {
		res := <-ch
		attempted = appendUniqueHosts(attempted, res.host)
		if res.host != "" {
			// no-op, keep host tracking for wrapped error context
		}
		if res.err == nil {
			p.recordArticleResult(res.providerID, true)
			p.recordSuccess(res.providerID)
			if !shouldCacheFetchedSegment(fetchCtx) {
				cancel()
				return SegmentData{}, fetchCtx.Err()
			}
			cached := res.data
			cached.ProviderHost = ""
			p.cache.Set(messageID, cached)
			p.clearKnownMissing(messageID)
			cancel()
			logger.Trace("fetch segment ok (parallel)", "message_id", messageID, "size", res.data.Size)
			return res.data, nil
		}
		lastErr = res.err
		if isArticleNotFound(res.err) {
			p.recordArticleResult(res.providerID, false)
			p.record430Error(res.providerID)
			if articleNotFoundErr == nil {
				articleNotFoundErr = res.err
			}
			// FetchSegmentFirst uses fixed one-provider workers; provider IDs are
			// not surfaced in results, so we cannot prove all providers were
			// attempted here. Do not mark permanent-missing from this fast path.
			continue
		}
		sawNonArticleNotFound = true
	}
	if articleNotFoundErr != nil && !sawNonArticleNotFound {
		return SegmentData{}, wrapAttemptedProviders(fmt.Errorf("fetch segment %s: %w", messageID, articleNotFoundErr), attempted)
	}
	if lastErr != nil {
		return SegmentData{}, wrapAttemptedProviders(fmt.Errorf("fetch segment %s: failed after retries: %w", messageID, lastErr), attempted)
	}
	return SegmentData{}, fmt.Errorf("fetch segment %s: failed after retries", messageID)
}

func (p *Pool) fetchSegmentOnce(ctx context.Context, messageID string, segment *nzb.Segment, groups []string) (SegmentData, error) {
	if p.isKnownMissing(messageID) {
		return SegmentData{}, fmt.Errorf("fetch segment %s: 430 No Such Article (cached)", messageID)
	}
	if data, ok := p.cache.Get(messageID); ok {
		logger.Trace("fetch segment cache hit", "message_id", messageID)
		return data, nil
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	p.mu.RLock()
	providerCount := len(p.providers)
	p.mu.RUnlock()

	var exclude []string
	var lastErr error
	var attempted []string
	var attemptedIDs []string
	var articleNotFoundErr error
	sawNonArticleNotFound := false
	maxAttempts := providerCount
	if maxAttempts < 3 {
		maxAttempts = 3
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		conn, release, discard, providerID, err := p.getConnection(fetchCtx, exclude, 999, false)
		if err != nil {
			if errors.Is(err, ErrNoProvidersAvailable) && len(exclude) > 0 {
				if articleNotFoundErr != nil && !sawNonArticleNotFound && providerCount > 0 && len(exclude) >= providerCount {
					break
				}
				exclude = nil
				continue
			}
			return SegmentData{}, err
		}
		host := p.Host(providerID)
		attempted = appendUniqueHosts(attempted, host)
		attemptedIDs = appendUniqueHosts(attemptedIDs, providerID)

		data, articleNotFound, err := func() (SegmentData, bool, error) {
			p.activeFetches.Add(1)
			defer p.activeFetches.Add(-1)

			// Interrupt pending body read if session is closed/cancelled.
			stopWatch := make(chan struct{})
			go func() {
				select {
				case <-fetchCtx.Done():
					discard()
				case <-stopWatch:
				}
			}()
			defer func() {
				close(stopWatch)
				release()
			}()

			if len(groups) > 0 {
				if err := conn.Group(groups[0]); err != nil {
					logger.Debug("fetch segment group failed", "provider", providerID, "err", err)
					return SegmentData{}, false, err
				}
			}

			r, err := conn.Body(messageID)
			if err != nil {
				logger.Debug("fetch segment body failed", "provider", providerID, "err", err)
				return SegmentData{}, isArticleNotFound(err), err
			}

			cr := &countReader{Reader: r}
			frame, err := decode.DecodeToBytes(cr)
			// Close ensures EndResponse is called even if decode stopped before EOF.
			r.Close()
			if err != nil {
				discard()
				ctxErr := fetchCtx.Err()
				if ctxErr != nil {
					logger.Trace("fetch segment decode aborted",
						"provider", providerID,
						"err", err,
						"message_id", messageID,
						"raw_body_bytes", cr.n,
						"ctx_err", ctxErr)
				} else {
					logger.Debug("fetch segment decode failed",
						"provider", providerID,
						"err", err,
						"message_id", messageID,
						"raw_body_bytes", cr.n,
						"ctx_err", ctxErr)
				}
				return SegmentData{}, false, err
			}

			return SegmentData{
				Body:         frame.Data,
				Size:         int64(len(frame.Data)),
				ProviderHost: host,
			}, false, nil
		}()
		if err != nil {
			lastErr = err
			if articleNotFound {
				p.recordArticleResult(providerID, false)
				p.record430Error(providerID)
				if articleNotFoundErr == nil {
					articleNotFoundErr = err
				}
			} else {
				sawNonArticleNotFound = true
			}
			exclude = append(exclude, providerID)
			continue
		}
		p.recordArticleResult(providerID, true)
		p.recordSuccess(providerID)

		if !shouldCacheFetchedSegment(fetchCtx) {
			return SegmentData{}, fetchCtx.Err()
		}
		cached := data
		cached.ProviderHost = ""
		p.cache.Set(messageID, cached)
		p.clearKnownMissing(messageID)
		logger.Trace("fetch segment ok", "message_id", messageID, "size", data.Size)
		return data, nil
	}

	if articleNotFoundErr != nil && !sawNonArticleNotFound {
		if p.attemptedAllProviderIDs(attemptedIDs) {
			p.markKnownMissing(messageID)
		}
		return SegmentData{}, wrapAttemptedProviders(fmt.Errorf("fetch segment %s: %w", messageID, articleNotFoundErr), attempted)
	}
	if lastErr != nil {
		return SegmentData{}, wrapAttemptedProviders(fmt.Errorf("fetch segment %s: failed after retries: %w", messageID, lastErr), attempted)
	}
	return SegmentData{}, fmt.Errorf("fetch segment %s: failed after retries", messageID)
}

// StatSegment checks whether the article exists on any provider (STAT only, no body).
// Returns (true, nil) if found, (false, nil) if 430 on all providers, (false, err) on other errors.
// Use this before opening a stream to fail fast when the first segment is missing.
func (p *Pool) StatSegment(ctx context.Context, messageID string, groups []string) (exists bool, err error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return false, fmt.Errorf("empty segment message ID")
	}
	if p.isKnownMissing(messageID) {
		return false, nil
	}

	statCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	p.mu.RLock()
	providers := p.providers
	p.mu.RUnlock()

	providerIDs := make([]string, len(providers))
	for i := range providers {
		providerIDs[i] = providers[i].ID
	}

	type statResult struct {
		exists     bool
		err        error
		host       string
		providerID string
	}
	ch := make(chan statResult, len(providers))

	for i := range providers {
		exclude := make([]string, 0, len(providers)-1)
		for j := range providerIDs {
			if j != i {
				exclude = append(exclude, providerIDs[j])
			}
		}
		go func(exclude []string) {
			conn, release, discard, providerID, getErr := p.getConnection(statCtx, exclude, 999, false)
			if getErr != nil {
				ch <- statResult{err: getErr}
				return
			}
			host := p.Host(providerID)

			// Watchdog: if the context is cancelled while we are waiting for
			// StatArticle (or Group), call discard() so the connection is closed
			// and the pool slot is freed immediately instead of leaking until the
			// 30-second statCtx deadline expires.
			stopWatch := make(chan struct{})
			go func() {
				select {
				case <-statCtx.Done():
					discard()
				case <-stopWatch:
				}
			}()

			var doRelease = true
			defer func() {
				close(stopWatch)
				if doRelease {
					release()
				}
				// discard() is called by the watchdog when context is done;
				// if we're here normally the watchdog exits via stopWatch.
			}()

			if len(groups) > 0 {
				if groupErr := conn.Group(groups[0]); groupErr != nil {
					logger.Debug("stat segment group failed", "provider", providerID, "err", groupErr)
					doRelease = false
					discard()
					ch <- statResult{err: groupErr, host: host, providerID: providerID}
					return
				}
			}
			exists, statErr := conn.StatArticle(messageID)
			if statErr != nil {
				logger.Debug("stat segment failed", "provider", providerID, "err", statErr)
				doRelease = false
				discard()
				ch <- statResult{err: statErr, host: host, providerID: providerID}
				return
			}
			ch <- statResult{exists: exists, host: host, providerID: providerID}
		}(exclude)
	}

	var lastErr error
	var attempted []string
	var attemptedIDs []string
	sawNotFound := false
	sawError := false
	for range providers {
		res := <-ch
		attempted = appendUniqueHosts(attempted, res.host)
		if res.host != "" {
			for i := range providers {
				if providers[i].ClientPool != nil && providers[i].ClientPool.Host() == res.host {
					attemptedIDs = appendUniqueHosts(attemptedIDs, providers[i].ID)
					break
				}
			}
		}
		if res.err == nil && res.exists {
			p.recordArticleResult(res.providerID, true)
			p.clearKnownMissing(messageID)
			cancel()
			logger.Trace("stat segment ok", "message_id", messageID)
			return true, nil
		}
		if res.err != nil {
			if isArticleNotFound(res.err) {
				p.recordArticleResult(res.providerID, false)
			}
			lastErr = res.err
			sawError = true
			continue
		}
		if !res.exists {
			p.recordArticleResult(res.providerID, false)
			sawNotFound = true
		}
	}
	if lastErr != nil {
		return false, wrapAttemptedProviders(fmt.Errorf("stat segment %s: %w", messageID, lastErr), attempted)
	}
	if p.attemptedAllProviderIDs(attemptedIDs) && sawNotFound && !sawError {
		p.markKnownMissing(messageID)
	}
	logger.Trace("stat segment not found (430)", "message_id", messageID)
	return false, nil
}

func (p *Pool) getConnection(ctx context.Context, exclude []string, maxPriority int, useBackup bool) (client *nntp.Client, release, discard func(), providerID string, err error) {
	p.mu.RLock()
	providers := p.providers
	// Create a local snapshot of consecutive 430s to avoid holding the lock during blocking Get()
	consecutive := make(map[string]int, len(p.consecutive430s))
	for k, v := range p.consecutive430s {
		consecutive[k] = v
	}
	p.mu.RUnlock()

	excludeSet := make(map[string]bool)
	for _, id := range exclude {
		excludeSet[id] = true
	}

	// Pass 1: Try healthy providers first (consecutive 430 errors < 3)
	for i := range providers {
		prov := &providers[i]
		if excludeSet[prov.ID] {
			continue
		}
		if prov.Priority > maxPriority {
			continue
		}
		if prov.IsBackup != useBackup {
			continue
		}
		if consecutive[prov.ID] >= 3 {
			continue
		}

		c, ok := prov.ClientPool.TryGet(ctx)
		if !ok {
			var getErr error
			c, getErr = prov.ClientPool.Get(ctx)
			if getErr != nil {
				if errors.Is(getErr, context.Canceled) {
					return nil, nil, nil, "", getErr
				}
				continue
			}
		}

		pool := prov.ClientPool
		pid := prov.ID
		var once sync.Once
		release := func() {
			once.Do(func() {
				pool.Put(c)
			})
		}
		discard := func() {
			once.Do(func() {
				pool.Discard(c)
			})
		}
		return c, release, discard, pid, nil
	}

	// Pass 2: Fall back to demoted providers (consecutive 430 errors >= 3)
	for i := range providers {
		prov := &providers[i]
		if excludeSet[prov.ID] {
			continue
		}
		if prov.Priority > maxPriority {
			continue
		}
		if prov.IsBackup != useBackup {
			continue
		}
		if consecutive[prov.ID] < 3 {
			continue
		}

		c, ok := prov.ClientPool.TryGet(ctx)
		if !ok {
			var getErr error
			c, getErr = prov.ClientPool.Get(ctx)
			if getErr != nil {
				if errors.Is(getErr, context.Canceled) {
					return nil, nil, nil, "", getErr
				}
				continue
			}
		}

		pool := prov.ClientPool
		pid := prov.ID
		var once sync.Once
		release := func() {
			once.Do(func() {
				pool.Put(c)
			})
		}
		discard := func() {
			once.Do(func() {
				pool.Discard(c)
			})
		}
		return c, release, discard, pid, nil
	}

	return nil, nil, nil, "", ErrNoProvidersAvailable
}

func (p *Pool) GetConnection(ctx context.Context, exclude []string, maxPriority int, useBackup bool) (client *nntp.Client, release, discard func(), providerID string, err error) {
	return p.getConnection(ctx, exclude, maxPriority, useBackup)
}

func (p *Pool) DiscardConnection(client *nntp.Client, pool *nntp.ClientPool) {
	if client != nil && pool != nil {
		pool.Discard(client)
	}
}

// PurgeCache drops all entries from the segment cache and resets budget accounting.
// Call when no sessions are active so the GC can reclaim the segment memory.
func (p *Pool) PurgeCache() {
	p.cache.Purge()
	logger.Trace("pool PurgeCache: segment cache purged")
}

func (p *Pool) TraceSnapshot() PoolTraceSnapshot {
	p.mu.RLock()
	providers := make([]ProviderConfig, len(p.providers))
	copy(providers, p.providers)
	cache := p.cache
	p.mu.RUnlock()

	snapshot := PoolTraceSnapshot{
		InFlightFetches: p.activeFetches.Load(),
		Cache:           cacheStats(cache),
		Providers:       make([]PoolProviderTraceSnapshot, 0, len(providers)),
	}
	for _, provider := range providers {
		clientPool := provider.ClientPool
		if clientPool == nil {
			continue
		}
		snapshot.Providers = append(snapshot.Providers, PoolProviderTraceSnapshot{
			ID:     provider.ID,
			Host:   clientPool.Host(),
			Total:  clientPool.TotalConnections(),
			Idle:   clientPool.IdleConnections(),
			Active: clientPool.ActiveConnections(),
		})
	}
	return snapshot
}

func (p *Pool) CountProviders() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.providers)
}

func (p *Pool) ProviderOrder() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	ids := make([]string, 0, len(p.providers))
	for i := range p.providers {
		ids = append(ids, p.providers[i].ID)
	}
	return ids
}

func (p *Pool) ProviderHosts() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	hosts := make([]string, 0, len(p.providers))
	for i := range p.providers {
		if h := p.providers[i].ClientPool.Host(); h != "" {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

func (p *Pool) Subset(providerIDs []string) *Pool {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()

	var subset []ProviderConfig
	if len(providerIDs) == 0 {
		subset = make([]ProviderConfig, len(p.providers))
		copy(subset, p.providers)
	} else {
		byID := make(map[string]ProviderConfig, len(p.providers))
		for i := range p.providers {
			byID[p.providers[i].ID] = p.providers[i]
		}
		subset = make([]ProviderConfig, 0, len(providerIDs))
		for _, id := range providerIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if cfg, ok := byID[id]; ok {
				cfg.Priority = len(subset)
				subset = append(subset, cfg)
			}
		}
	}

	if len(subset) == 0 {
		return nil
	}

	return &Pool{
		providers:            subset,
		cache:                p.cache,
		sf:                   p.sf,
		missing:              p.missing,
		providerSig:          providerSignature(subset),
		articleStats:         p.articleStats,
		consecutive430s:      make(map[string]int),
		consecutiveSuccesses: make(map[string]int),
	}
}

func (p *Pool) Host(providerID string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for i := range p.providers {
		if p.providers[i].ID == providerID {
			return p.providers[i].ClientPool.Host()
		}
	}
	return ""
}
