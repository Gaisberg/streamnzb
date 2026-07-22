package indexer

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/release"
)

type QueryCache struct {
	mu    sync.RWMutex
	items map[string]*queryCacheEntry
}

type queryCacheEntry struct {
	resp  *SearchResponse
	until time.Time
}

func NewQueryCache() *QueryCache {
	return &QueryCache{
		items: make(map[string]*queryCacheEntry),
	}
}

func BuildQueryCacheKey(indexerName string, req SearchRequest) string {
	parts := []string{
		"idx=" + strings.TrimSpace(indexerName),
		"mode=" + strings.ToLower(strings.TrimSpace(req.SearchMode)),
		"q=" + strings.TrimSpace(req.Query),
		"imdb=" + strings.TrimSpace(req.IMDbID),
		"tmdb=" + strings.TrimSpace(req.TMDBID),
		"tvdb=" + strings.TrimSpace(req.TVDBID),
		"cat=" + strings.TrimSpace(req.Cat),
		"s=" + strings.TrimSpace(req.Season),
		"ep=" + strings.TrimSpace(req.Episode),
		"limit=" + fmt.Sprintf("%d", req.Limit),
		"scope=" + strings.TrimSpace(req.SeriesSearchScope),
		"no_filter=" + fmt.Sprintf("%t", req.DisableResultFiltering),
		"year_val=" + fmt.Sprintf("%t", req.EnableYearValidation),
	}
	if req.OptionalOverrides != nil {
		parts = append(parts,
			"ov_limit="+fmt.Sprintf("%d", req.OptionalOverrides.SearchResultLimit),
		)
	}
	return strings.Join(parts, "|")
}

func (qc *QueryCache) Get(key string) (*SearchResponse, bool) {
	if qc == nil {
		return nil, false
	}
	qc.mu.RLock()
	entry, ok := qc.items[key]
	if !ok || time.Now().After(entry.until) {
		qc.mu.RUnlock()
		return nil, false
	}
	resp := cloneSearchResponse(entry.resp)
	qc.mu.RUnlock()
	return resp, true
}

func (qc *QueryCache) Put(key string, resp *SearchResponse, ttl time.Duration) {
	if qc == nil || resp == nil {
		return
	}
	qc.mu.Lock()
	defer qc.mu.Unlock()
	qc.items[key] = &queryCacheEntry{
		resp:  cloneSearchResponse(resp),
		until: time.Now().Add(ttl),
	}
}

func (qc *QueryCache) Clear() {
	if qc == nil {
		return
	}
	qc.mu.Lock()
	defer qc.mu.Unlock()
	qc.items = make(map[string]*queryCacheEntry)
}

func cloneSearchResponse(resp *SearchResponse) *SearchResponse {
	if resp == nil {
		return nil
	}
	cloned := *resp
	if resp.Releases != nil {
		cloned.Releases = make([]*release.Release, len(resp.Releases))
		for i, rel := range resp.Releases {
			if rel != nil {
				relCopy := *rel
				if rel.Languages != nil {
					relCopy.Languages = append([]string(nil), rel.Languages...)
				}
				cloned.Releases[i] = &relCopy
			}
		}
	}
	if resp.Channel.Items != nil {
		cloned.Channel.Items = make([]Item, len(resp.Channel.Items))
		for i, item := range resp.Channel.Items {
			itemCopy := item
			if item.Attributes != nil {
				itemCopy.Attributes = append([]Attribute(nil), item.Attributes...)
			}
			cloned.Channel.Items[i] = itemCopy
		}
	}
	return &cloned
}

type CachedIndexer struct {
	underlying Indexer
	cache      *QueryCache
	ttl        time.Duration
	sf         singleflight.Group
}

var _ Indexer = (*CachedIndexer)(nil)
var _ IndexerWithCaps = (*CachedIndexer)(nil)

func NewCachedIndexer(underlying Indexer, cache *QueryCache, ttl time.Duration) *CachedIndexer {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &CachedIndexer{
		underlying: underlying,
		cache:      cache,
		ttl:        ttl,
	}
}

func (c *CachedIndexer) Search(req SearchRequest) (*SearchResponse, error) {
	if c.underlying == nil {
		return nil, nil
	}
	if c.cache == nil {
		return c.underlying.Search(req)
	}

	key := BuildQueryCacheKey(c.Name(), req)
	if cachedResp, ok := c.cache.Get(key); ok {
		logger.Debug("Indexer query cache hit", "indexer", c.Name(), "key", key, "releases", len(cachedResp.Releases))
		return cachedResp, nil
	}

	v, err, shared := c.sf.Do(key, func() (interface{}, error) {
		if cachedResp, ok := c.cache.Get(key); ok {
			return cachedResp, nil
		}
		logger.Debug("Indexer query cache miss", "indexer", c.Name(), "key", key)
		resp, err := c.underlying.Search(req)
		if err != nil || resp == nil {
			return resp, err
		}
		c.cache.Put(key, resp, c.ttl)
		return resp, nil
	})

	if err != nil {
		return nil, err
	}

	if shared {
		logger.Debug("Indexer query joined in-flight request", "indexer", c.Name(), "key", key)
	}

	resp, _ := v.(*SearchResponse)
	return cloneSearchResponse(resp), nil
}

func (c *CachedIndexer) DownloadNZB(ctx context.Context, nzbURL string) ([]byte, error) {
	return c.underlying.DownloadNZB(ctx, nzbURL)
}

func (c *CachedIndexer) Ping() error {
	return c.underlying.Ping()
}

func (c *CachedIndexer) Name() string {
	return c.underlying.Name()
}

func (c *CachedIndexer) GetUsage() Usage {
	return c.underlying.GetUsage()
}

func (c *CachedIndexer) GetCaps() (*Caps, error) {
	if capsFetcher, ok := c.underlying.(IndexerWithCaps); ok {
		return capsFetcher.GetCaps()
	}
	return nil, fmt.Errorf("underlying indexer does not support GetCaps")
}

func (c *CachedIndexer) Unwrap() Indexer {
	return c.underlying
}
