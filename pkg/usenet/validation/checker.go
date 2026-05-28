package validation

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"reflect"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/media/decode"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/usenet/nntp"
	"streamnzb/pkg/usenet/pool"

	"github.com/javi11/rardecode/v2"
	"github.com/javi11/sevenzip"
)

type Checker struct {
	mu            sync.RWMutex
	pool          *pool.Pool
	sampleSize    int
	maxConcurrent int
}

func NewChecker(up *pool.Pool, sampleSize, maxConcurrent int) *Checker {
	return &Checker{
		pool:          up,
		sampleSize:    sampleSize,
		maxConcurrent: maxConcurrent,
	}
}

type ValidationResult struct {
	Provider        string
	Host            string
	Available       bool
	TotalArticles   int
	CheckedArticles int
	MissingArticles int
	Error           error
}

func (c *Checker) GetProviderHosts() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.pool == nil {
		return nil
	}
	return c.pool.ProviderHosts()
}

func (c *Checker) GetPrimaryProviderHost() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.pool == nil {
		return ""
	}
	order := c.pool.ProviderOrder()
	if len(order) == 0 {
		return ""
	}
	return order[0]
}

func (c *Checker) ValidateNZBSingleProvider(ctx context.Context, nzbData *nzb.NZB, providerName string) *ValidationResult {
	return c.ValidateNZBSingleProviderForEpisode(ctx, nzbData, providerName, 0, 0)
}

func (c *Checker) ValidateNZBSingleProviderForEpisode(ctx context.Context, nzbData *nzb.NZB, providerName string, season, episode int) *ValidationResult {
	c.mu.RLock()
	up := c.pool
	c.mu.RUnlock()
	if up == nil {
		return &ValidationResult{Provider: providerName, Error: fmt.Errorf("usenet pool not configured")}
	}
	exclude := excludeProvider(up.ProviderOrder(), providerName)
	client, release, _, _, err := up.GetConnection(ctx, exclude, 999, false)
	if err != nil {
		return &ValidationResult{Provider: providerName, Error: fmt.Errorf("get connection: %w", err)}
	}
	defer release()
	host := up.Host(providerName)
	return c.validateProviderWithClient(ctx, nzbData, providerName, client, host, season, episode)
}

func (c *Checker) ValidateNZBSingleProviderExtended(ctx context.Context, nzbData *nzb.NZB, providerName string) *ValidationResult {
	return c.ValidateNZBSingleProviderExtendedForEpisode(ctx, nzbData, providerName, 0, 0)
}

func (c *Checker) ValidateNZBSingleProviderExtendedForEpisode(ctx context.Context, nzbData *nzb.NZB, providerName string, season, episode int) *ValidationResult {
	c.mu.RLock()
	up := c.pool
	c.mu.RUnlock()
	if up == nil {
		return &ValidationResult{Provider: providerName, Error: fmt.Errorf("usenet pool not configured")}
	}
	exclude := excludeProvider(up.ProviderOrder(), providerName)
	client, release, discard, _, err := up.GetConnection(ctx, exclude, 999, false)
	if err != nil {
		return &ValidationResult{Provider: providerName, Error: fmt.Errorf("get connection: %w", err)}
	}
	host := up.Host(providerName)
	return c.validateProviderExtendedWithClient(ctx, nzbData, providerName, client, release, discard, host, season, episode)
}

func excludeProvider(order []string, except string) []string {
	out := make([]string, 0, len(order))
	for _, id := range order {
		if id != except {
			out = append(out, id)
		}
	}
	return out
}

func (c *Checker) ValidateNZB(ctx context.Context, nzbData *nzb.NZB) map[string]*ValidationResult {
	return c.ValidateNZBForEpisode(ctx, nzbData, 0, 0)
}

func (c *Checker) ValidateNZBForEpisode(ctx context.Context, nzbData *nzb.NZB, season, episode int) map[string]*ValidationResult {
	results := make(map[string]*ValidationResult)
	var mu sync.Mutex
	var wg sync.WaitGroup

	logger.Trace("ValidateNZB start", "hash", nzbData.Hash())

	c.mu.RLock()
	up := c.pool
	c.mu.RUnlock()
	if up == nil {
		return results
	}
	providerOrder := up.ProviderOrder()

	for _, providerName := range providerOrder {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			exclude := excludeProvider(providerOrder, name)
			client, release, discard, _, err := up.GetConnection(ctx, exclude, 999, false)
			if err != nil {
				mu.Lock()
				results[name] = &ValidationResult{Provider: name, Error: err}
				mu.Unlock()
				return
			}
			host := up.Host(name)
			result := c.validateProviderExtendedWithClient(ctx, nzbData, name, client, release, discard, host, season, episode)
			mu.Lock()
			results[name] = result
			mu.Unlock()
		}(providerName)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logger.Trace("ValidateNZB: all providers done", "results", len(results))
	case <-ctx.Done():

		logger.Debug("Validation cancelled, returning partial results")
		logger.Trace("ValidateNZB: ctx.Done", "partial_results", len(results))
		return results
	case <-time.After(30 * time.Second):

		logger.Warn("Validation timeout, returning partial results", "providers", len(providerOrder))
		logger.Trace("ValidateNZB: 30s timeout", "partial_results", len(results))
		return results
	}

	return results
}

func (c *Checker) InvalidateCache(hash string) {}

func maxOr(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (c *Checker) validateProviderWithClient(ctx context.Context, nzbData *nzb.NZB, providerName string, client *nntp.Client, host string, season, episode int) *ValidationResult {
	result := &ValidationResult{
		Provider: providerName,
		Host:     host,
	}

	info := selectValidationFile(nzbData, season, episode)
	articles := c.getSampleArticlesForEpisode(nzbData, season, episode)
	if info != nil && info.File != nil {
		result.TotalArticles = len(info.File.Segments)
	}
	result.CheckedArticles = len(articles)

	type statResult struct {
		exists bool
		err    error
	}
	statChan := make(chan statResult, len(articles))
	sem := make(chan struct{}, maxOr(c.maxConcurrent, 5))
	for _, articleID := range articles {
		articleID := articleID
		select {
		case <-ctx.Done():
			result.Error = ctx.Err()
			return result
		default:
		}
		go func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			exists, err := client.StatArticle(articleID)
			statChan <- statResult{exists, err}
		}()
	}
	missing := 0
	for range articles {
		select {
		case <-ctx.Done():
			result.Error = ctx.Err()
			return result
		case res := <-statChan:
			if res.err != nil {
				if c.pool != nil && pool.IsArticleNotFoundError(res.err) {
					c.pool.RecordProviderArticleResult(providerName, false)
				}
				result.Error = res.err
				return result
			}
			if !res.exists {
				missing++
				if c.pool != nil {
					c.pool.RecordProviderArticleResult(providerName, false)
				}
			} else {
				if c.pool != nil {
					c.pool.RecordProviderArticleResult(providerName, true)
				}
			}
		}
	}

	result.MissingArticles = missing
	result.Available = missing == 0

	logger.Debug("Provider check", "provider", providerName, "available", result.CheckedArticles-missing, "total", result.CheckedArticles)

	return result
}

func (c *Checker) validateProviderExtendedWithClient(ctx context.Context, nzbData *nzb.NZB, providerName string, client *nntp.Client, release, discard func(), host string, season, episode int) *ValidationResult {
	result := c.validateProviderWithClient(ctx, nzbData, providerName, client, host, season, episode)
	if result.Error != nil || !result.Available {
		release()
		return result
	}

	info := selectValidationFile(nzbData, season, episode)
	if info == nil || info.File == nil || len(info.File.Segments) == 0 {
		release()
		return result
	}

	segments := info.File.Segments
	probeIndices := probeSegmentIndices(len(segments))

	if len(info.File.Groups) > 0 {
		_ = client.Group(info.File.Groups[0])
	}

	ct := compressionTypeForValidation(nzbData, season, episode)
	var firstSegData []byte
	var lastSegData []byte

	for _, idx := range probeIndices {
		body, err := client.Body(segments[idx].ID)
		if err != nil {
			if c.pool != nil && pool.IsArticleNotFoundError(err) {
				c.pool.RecordProviderArticleResult(providerName, false)
			}
			result.Available = false
			result.Error = fmt.Errorf("body probe segment %d: %w", idx, err)
			logger.Debug("Extended check BODY failed", "provider", providerName, "segment", idx, "err", err)
			discard()
			return result
		}
		frame, err := decode.DecodeToBytes(body)
		if err != nil {
			_, _ = io.Copy(io.Discard, body)
			if c.pool != nil {
				c.pool.RecordProviderArticleResult(providerName, true)
			}
			result.Available = false
			result.Error = fmt.Errorf("decode probe segment %d: %w", idx, err)
			logger.Debug("Extended check decode failed", "provider", providerName, "segment", idx, "err", err)
			discard()
			return result
		}
		if len(frame.Data) == 0 {
			if c.pool != nil {
				c.pool.RecordProviderArticleResult(providerName, true)
			}
			result.Available = false
			result.Error = fmt.Errorf("probe segment %d decoded to empty data", idx)
			logger.Debug("Extended check empty segment", "provider", providerName, "segment", idx)
			discard()
			return result
		}
		if idx == 0 {
			firstSegData = frame.Data
		}
		if idx == len(segments)-1 {
			lastSegData = frame.Data
		}
		if c.pool != nil {
			c.pool.RecordProviderArticleResult(providerName, true)
		}
	}

	if firstSegData == nil {
		firstSegData = lastSegData
	}
	if lastSegData == nil {
		lastSegData = firstSegData
	}

	if ct != "direct" && len(firstSegData) > 0 {
		password := ""
		if nzbData != nil {
			password = nzbData.Password()
		}
		if err := verifyArchiveHeader(ct, firstSegData, lastSegData, info, password); err != nil {
			result.Available = false
			result.Error = err
			logger.Debug("Extended check archive header failed", "provider", providerName, "compression", ct, "err", err)
			release()
			return result
		}
	}

	release()
	logger.Debug("Extended check passed", "provider", providerName, "probed", len(probeIndices), "compression", ct)
	return result
}

func probeSegmentIndices(total int) []int {
	if total <= 0 {
		return nil
	}
	if total == 1 {
		return []int{0}
	}
	if total == 2 {
		return []int{0, 1}
	}
	return []int{0, total / 2, total - 1}
}

func selectValidationFile(nzbData *nzb.NZB, season, episode int) *nzb.FileInfo {
	if nzbData == nil {
		return nil
	}
	return nzbData.GetPlaybackFileForEpisode(season, episode)
}

func compressionTypeForValidation(nzbData *nzb.NZB, season, episode int) string {
	if nzbData == nil {
		return "direct"
	}
	return nzbData.CompressionTypeForEpisode(season, episode)
}

func (c *Checker) getSampleArticles(nzbData *nzb.NZB) []string {
	return c.getSampleArticlesForEpisode(nzbData, 0, 0)
}

func (c *Checker) getSampleArticlesForEpisode(nzbData *nzb.NZB, season, episode int) []string {
	if len(nzbData.Files) == 0 {
		return nil
	}

	var file *nzb.File
	if info := selectValidationFile(nzbData, season, episode); info != nil {
		file = info.File
	} else {
		file = &nzbData.Files[0]
	}
	segments := file.Segments

	if len(segments) == 0 {
		return nil
	}

	sampleSize := c.sampleSize
	if sampleSize > len(segments) {
		sampleSize = len(segments)
	}

	articles := make([]string, 0, sampleSize)

	articles = append(articles, segments[0].ID)

	if len(segments) > 1 {
		articles = append(articles, segments[len(segments)-1].ID)
	}

	remainingSlots := sampleSize - len(articles)
	if remainingSlots > 0 {

		startIdx := 1
		endIdx := len(segments) - 1
		if startIdx < endIdx {
			totalSpan := endIdx - startIdx
			step := float64(totalSpan) / float64(remainingSlots)

			for i := 0; i < remainingSlots; i++ {

				idx := startIdx + int(float64(i)*step)
				if idx < endIdx {
					articles = append(articles, segments[idx].ID)
				}
			}
		}
	}

	return articles
}

func verifyArchiveHeader(ct string, firstSeg, lastSeg []byte, info *nzb.FileInfo, password string) error {
	switch ct {
	case "rar":
		opts := []rardecode.Option{}
		if password != "" {
			opts = append(opts, rardecode.Password(password))
		}
		r, err := rardecode.NewReader(bytes.NewReader(firstSeg), opts...)
		if err != nil {
			return fmt.Errorf("invalid RAR archive: %w", err)
		}
		hdr, err := r.Next()
		if err != nil {
			return fmt.Errorf("cannot read RAR file entry: %w", err)
		}
		if stored, known := rarHeaderStored(hdr); known && !stored {
			return fmt.Errorf("RAR archive uses compression (STORE mode required for streaming)")
		}
	case "7z":
		if err := verify7zHeader(firstSeg, lastSeg, info, password); err != nil {
			return err
		}
	}
	return nil
}

func rarHeaderStored(hdr *rardecode.FileHeader) (bool, bool) {
	if hdr == nil {
		return false, false
	}
	v := reflect.ValueOf(hdr)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return false, false
	}
	field := v.Elem().FieldByName("Stored")
	if !field.IsValid() || field.Kind() != reflect.Bool {
		return false, false
	}
	return field.Bool(), true
}

func verify7zHeader(headData, tailData []byte, info *nzb.FileInfo, password string) error {
	if info == nil {
		return nil
	}
	totalSize := info.Size
	if totalSize <= 0 {
		return nil
	}

	if int64(len(headData)) >= totalSize {
		ra := bytes.NewReader(headData[:totalSize])
		return parse7z(ra, totalSize, password)
	}

	if len(tailData) == 0 {
		return nil
	}

	tailOffset := totalSize - int64(len(tailData))
	if tailOffset < 0 {
		tailOffset = 0
	}
	ra := &sparse7zReader{
		head:       headData,
		tail:       tailData,
		tailOffset: tailOffset,
		totalSize:  totalSize,
	}
	return parse7z(ra, totalSize, password)
}

func parse7z(ra io.ReaderAt, size int64, password string) error {
	r, err := sevenzip.NewReaderWithPassword(ra, size, password)
	if err != nil {
		return fmt.Errorf("invalid 7z archive: %w", err)
	}
	infos, err := r.ListFilesWithOffsets()
	if err != nil {
		return fmt.Errorf("cannot list 7z contents: %w", err)
	}
	for _, fi := range infos {
		if fi.Size > 50*1024*1024 && fi.Compressed {
			return fmt.Errorf("7z archive uses compression (STORE mode required for streaming)")
		}
	}
	return nil
}

type sparse7zReader struct {
	head       []byte
	tail       []byte
	tailOffset int64
	totalSize  int64
}

func (s *sparse7zReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= s.totalSize {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) && off < s.totalSize {
		pos := off
		if pos < int64(len(s.head)) {

			copied := copy(p[n:], s.head[pos:])
			n += copied
			off += int64(copied)
		} else if pos >= s.tailOffset {

			idx := pos - s.tailOffset
			if idx >= int64(len(s.tail)) {
				break
			}
			copied := copy(p[n:], s.tail[idx:])
			n += copied
			off += int64(copied)
		} else {

			end := s.tailOffset
			if end > off+int64(len(p)-n) {
				end = off + int64(len(p)-n)
			}
			gap := int(end - off)
			for i := 0; i < gap; i++ {
				p[n+i] = 0
			}
			n += gap
			off += int64(gap)
		}
	}
	if n == 0 {
		return 0, io.EOF
	}
	if off >= s.totalSize && n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func GetBestProvider(results map[string]*ValidationResult) *ValidationResult {
	var bestResult *ValidationResult
	var bestScore float64

	for _, result := range results {
		if result.Error != nil || !result.Available {
			continue
		}

		var score float64
		if result.CheckedArticles == 0 {

			score = 1.0
		} else {
			score = float64(result.CheckedArticles-result.MissingArticles) / float64(result.CheckedArticles)
		}

		if score >= bestScore {
			bestScore = score
			bestResult = result
		}
	}

	return bestResult
}
