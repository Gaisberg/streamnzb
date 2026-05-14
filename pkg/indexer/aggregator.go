package indexer

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"sort"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/release"
	"strings"
	"sync"
)

type Aggregator struct {
	Indexers []Indexer
}

func (a *Aggregator) Name() string {
	return "Aggregator"
}

func (a *Aggregator) GetIndexers() []Indexer {
	return a.Indexers
}

func (a *Aggregator) GetUsage() Usage {
	var usage Usage
	var weightedResponseTotal float64
	for _, idx := range a.Indexers {
		u := idx.GetUsage()
		usage.APIHitsLimit += u.APIHitsLimit
		usage.APIHitsUsed += u.APIHitsUsed
		usage.APIHitsRemaining += u.APIHitsRemaining
		usage.DownloadsLimit += u.DownloadsLimit
		usage.DownloadsUsed += u.DownloadsUsed
		usage.DownloadsRemaining += u.DownloadsRemaining
		usage.AllTimeAPIHitsUsed += u.AllTimeAPIHitsUsed
		usage.AllTimeDownloadsUsed += u.AllTimeDownloadsUsed
		usage.SearchesCount += u.SearchesCount
		weightedResponseTotal += float64(u.SearchesCount) * u.AvgResponseMS
	}
	if usage.SearchesCount > 0 {
		usage.AvgResponseMS = weightedResponseTotal / float64(usage.SearchesCount)
	}
	return usage
}

func NewAggregator(indexers ...Indexer) *Aggregator {
	return &Aggregator{
		Indexers: indexers,
	}
}

func (a *Aggregator) Ping() error {
	var lastErr error
	successCount := 0

	for _, idx := range a.Indexers {
		if err := idx.Ping(); err != nil {
			lastErr = err
		} else {
			successCount++
		}
	}

	if successCount == 0 && len(a.Indexers) > 0 {
		return fmt.Errorf("all indexers failed ping, last error: %w", lastErr)
	}
	return nil
}

func (a *Aggregator) DownloadNZB(ctx context.Context, nzbURL string) ([]byte, error) {
	if len(a.Indexers) == 0 {
		return nil, fmt.Errorf("no indexers configured")
	}
	var lastErr error
	for _, idx := range a.Indexers {
		data, err := idx.DownloadNZB(ctx, nzbURL)
		if err == nil {
			return data, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// isIDOnlySearch returns true when the request is an ID-based search
// (has IMDb/TVDB IDs and no text query).
func isIDOnlySearch(req SearchRequest) bool {
	return strings.EqualFold(strings.TrimSpace(req.SearchMode), "id")
}

// isTextSearch returns true when the request carries a text query.
func isTextSearch(req SearchRequest) bool {
	return strings.EqualFold(strings.TrimSpace(req.SearchMode), "text") && req.Query != ""
}

// shouldSkipIndexer checks the per-indexer DisableIdSearch / DisableStringSearch
// flags and returns true if this indexer should be skipped for the given request.
func shouldSkipIndexer(req SearchRequest, overrides *config.IndexerSearchConfig) bool {
	if overrides == nil {
		return false
	}
	if isIDOnlySearch(req) && overrides.DisableIdSearch != nil && *overrides.DisableIdSearch {
		return true
	}
	if isTextSearch(req) && overrides.DisableStringSearch != nil && *overrides.DisableStringSearch {
		return true
	}
	return false
}

func ShouldSkipIndexerForRequest(req SearchRequest, overrides *config.IndexerSearchConfig) bool {
	return shouldSkipIndexer(req, overrides)
}

func (a *Aggregator) Search(req SearchRequest) (*SearchResponse, error) {
	if strings.EqualFold(strings.TrimSpace(req.IndexerMode), "failover") {
		return a.searchWithFailover(req)
	}
	return a.searchCombined(req)
}

func (a *Aggregator) searchCombined(req SearchRequest) (*SearchResponse, error) {
	resultsChan := make(chan []Item, len(a.Indexers))
	var wg sync.WaitGroup

	for _, idx := range a.Indexers {
		wg.Add(1)
		go func(indexer Indexer, r SearchRequest) {
			defer wg.Done()
			items, err := searchItemsForIndexer(indexer, r)
			if err != nil {
				logger.Warn("Indexer search failed", "indexer", indexer.Name(), "err", err)
				resultsChan <- []Item{}
				return
			}
			resultsChan <- items
		}(idx, req)
	}

	wg.Wait()
	close(resultsChan)

	var allItems []Item
	for items := range resultsChan {
		allItems = append(allItems, items...)
	}

	seenGUID := make(map[string]bool)
	seenLink := make(map[string]bool)
	seenTitleSize := make(map[string]bool)
	uniqueItems := []Item{}

	for _, item := range allItems {

		normalizedTitle := release.NormalizeTitleForDedup(item.Title)

		if item.GUID != "" {
			if seenGUID[item.GUID] {
				continue
			}
			seenGUID[item.GUID] = true
			uniqueItems = append(uniqueItems, item)
			continue
		}

		if item.Link != "" {

			normalizedLink := normalizeURL(item.Link)
			if seenLink[normalizedLink] {
				continue
			}
			seenLink[normalizedLink] = true
			uniqueItems = append(uniqueItems, item)
			continue
		}

		titleSizeKey := fmt.Sprintf("%s:%d", normalizedTitle, item.Size)
		if item.Size > 0 && seenTitleSize[titleSizeKey] {
			continue
		}
		if item.Size > 0 {
			seenTitleSize[titleSizeKey] = true
		}
		uniqueItems = append(uniqueItems, item)
	}

	sort.Slice(uniqueItems, func(i, j int) bool {
		return uniqueItems[i].Size > uniqueItems[j].Size
	})

	resp := &SearchResponse{
		XMLName: xml.Name{Local: "rss"},
		Channel: Channel{
			Items: uniqueItems,
		},
	}
	NormalizeSearchResponse(resp)
	return resp, nil
}

func (a *Aggregator) searchWithFailover(req SearchRequest) (*SearchResponse, error) {
	type failoverResult struct {
		index int
		items []Item
		err   error
	}

	results := make(chan failoverResult, len(a.Indexers))
	for i, idx := range a.Indexers {
		go func(index int, idx Indexer) {
			items, err := searchItemsForIndexer(idx, req)
			results <- failoverResult{
				index: index,
				items: items,
				err:   err,
			}
		}(i, idx)
	}

	pending := len(a.Indexers)
	done := make([]bool, len(a.Indexers))
	itemsByIndexer := make([][]Item, len(a.Indexers))

	for pending > 0 {
		result := <-results
		pending--
		done[result.index] = true
		if result.err != nil {
			logger.Warn("Indexer search failed", "indexer", a.Indexers[result.index].Name(), "err", result.err)
		} else {
			itemsByIndexer[result.index] = result.items
		}

		for i := 0; i < len(a.Indexers); i++ {
			if !done[i] {
				break
			}
			if len(itemsByIndexer[i]) == 0 {
				continue
			}
			resp := &SearchResponse{
				XMLName: xml.Name{Local: "rss"},
				Channel: Channel{
					Items: itemsByIndexer[i],
				},
			}
			NormalizeSearchResponse(resp)
			return resp, nil
		}
	}
	resp := &SearchResponse{
		XMLName: xml.Name{Local: "rss"},
		Channel: Channel{Items: []Item{}},
	}
	NormalizeSearchResponse(resp)
	return resp, nil
}

func searchItemsForIndexer(idx Indexer, req SearchRequest) ([]Item, error) {
	var indexerOverrides *config.IndexerSearchConfig
	if req.EffectiveByIndexer != nil {
		indexerOverrides = req.EffectiveByIndexer[idx.Name()]
	}

	reqCopy := req
	reqCopy.EffectiveByIndexer = nil
	reqCopy.OptionalOverrides = indexerOverrides
	if shouldSkipIndexer(reqCopy, indexerOverrides) {
		skipReason := "request mode disabled"
		if isIDOnlySearch(reqCopy) {
			skipReason = "id search disabled for this request"
		} else if isTextSearch(reqCopy) {
			skipReason = "text search disabled for this request"
		}
		logger.Debug("Indexer skipped for request",
			"stream", reqCopy.StreamLabel,
			"request", reqCopy.RequestLabel,
			"indexer", idx.Name(),
			"reason", skipReason,
			"is_id", isIDOnlySearch(reqCopy),
			"is_text", isTextSearch(reqCopy),
		)
		return []Item{}, nil
	}
	resp, err := idx.Search(reqCopy)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return []Item{}, nil
	}
	return resp.Channel.Items, nil
}

func normalizeURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(rawURL))
	}

	normalized := fmt.Sprintf("%s://%s%s", parsed.Scheme, parsed.Host, parsed.Path)
	return strings.ToLower(strings.TrimSpace(normalized))
}
