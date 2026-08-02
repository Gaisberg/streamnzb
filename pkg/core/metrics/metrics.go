package metrics

import (
	"math"
	"sort"
	"sync"
	"time"
)

// StreamAPISample captures performance timing for a /stream/... request.
type StreamAPISample struct {
	Timestamp        time.Time     `json:"timestamp"`
	ContentType      string        `json:"content_type"`
	ID               string        `json:"id"`
	TotalDuration    time.Duration `json:"total_duration_ms"`
	MetadataDuration time.Duration `json:"metadata_duration_ms"`
	SearchDuration   time.Duration `json:"search_duration_ms"`
	RankingDuration  time.Duration `json:"ranking_duration_ms"`
	AvailNZBDuration time.Duration `json:"avail_nzb_duration_ms"`
	CandidateCount   int           `json:"candidate_count"`
	ResultCount      int           `json:"result_count"`
}

// PlaybackTTFFSample captures performance timing for a /play/... request up to the first written byte.
type PlaybackTTFFSample struct {
	Timestamp           time.Time     `json:"timestamp"`
	SessionID           string        `json:"session_id"`
	ProviderName        string        `json:"provider_name"`
	TTFF                time.Duration `json:"ttff_ms"`
	SessionResolution   time.Duration `json:"session_resolution_ms"`
	NZBFetchDuration    time.Duration `json:"nzb_fetch_duration_ms"`
	NNTPConnectDuration time.Duration `json:"nntp_connect_duration_ms"`
	ProbeDuration       time.Duration `json:"probe_duration_ms"`
	FirstByteDuration   time.Duration `json:"first_byte_duration_ms"`
	IsCacheHit          bool          `json:"is_cache_hit"`
}

// PerformanceSummary provides aggregated percentiles and statistics.
type PerformanceSummary struct {
	SampleCount int     `json:"sample_count"`
	MinMS       float64 `json:"min_ms"`
	MaxMS       float64 `json:"max_ms"`
	AvgMS       float64 `json:"avg_ms"`
	P50MS       float64 `json:"p50_ms"`
	P95MS       float64 `json:"p95_ms"`
	P99MS       float64 `json:"p99_ms"`
}

// StreamAPIStatsSummary holds current summary stats for stream API calls.
type StreamAPIStatsSummary struct {
	Total    PerformanceSummary `json:"total"`
	Metadata PerformanceSummary `json:"metadata"`
	Search   PerformanceSummary `json:"search"`
	Ranking  PerformanceSummary `json:"ranking"`
	AvailNZB PerformanceSummary `json:"avail_nzb"`
}

// PlaybackTTFFStatsSummary holds current summary stats for TTFF.
type PlaybackTTFFStatsSummary struct {
	Total       PerformanceSummary `json:"total"`
	NZBFetch    PerformanceSummary `json:"nzb_fetch"`
	NNTPConnect PerformanceSummary `json:"nntp_connect"`
	Probe       PerformanceSummary `json:"probe"`
	FirstByte   PerformanceSummary `json:"first_byte"`
}

const defaultMaxSamples = 1000

// Collector manages in-memory performance metrics for API latency and TTFF.
type Collector struct {
	mu             sync.RWMutex
	streamSamples  []StreamAPISample
	ttffSamples    []PlaybackTTFFSample
	maxSamples     int
	onStreamSample func(StreamAPISample)
	onTTFFSample   func(PlaybackTTFFSample)
}

var globalCollector = NewCollector(defaultMaxSamples)

// Default returns the global Collector instance.
func Default() *Collector {
	return globalCollector
}

// NewCollector constructs a Collector with a maximum sample capacity.
func NewCollector(maxSamples int) *Collector {
	if maxSamples <= 0 {
		maxSamples = defaultMaxSamples
	}
	return &Collector{
		streamSamples: make([]StreamAPISample, 0, maxSamples),
		ttffSamples:   make([]PlaybackTTFFSample, 0, maxSamples),
		maxSamples:    maxSamples,
	}
}

// SetOnStreamAPISample sets a callback invoked when a new stream API sample is recorded.
func (c *Collector) SetOnStreamAPISample(fn func(StreamAPISample)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onStreamSample = fn
}

// SetOnPlaybackTTFFSample sets a callback invoked when a new playback TTFF sample is recorded.
func (c *Collector) SetOnPlaybackTTFFSample(fn func(PlaybackTTFFSample)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onTTFFSample = fn
}

// Hydrate populates initial in-memory samples (e.g. loaded from database on startup).
func (c *Collector) Hydrate(streamSamples []StreamAPISample, ttffSamples []PlaybackTTFFSample) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(streamSamples) > c.maxSamples {
		streamSamples = streamSamples[len(streamSamples)-c.maxSamples:]
	}
	if len(ttffSamples) > c.maxSamples {
		ttffSamples = ttffSamples[len(ttffSamples)-c.maxSamples:]
	}
	c.streamSamples = append([]StreamAPISample(nil), streamSamples...)
	c.ttffSamples = append([]PlaybackTTFFSample(nil), ttffSamples...)
}

// RecordStreamAPI adds a new stream API timing sample.
func (c *Collector) RecordStreamAPI(sample StreamAPISample) {
	c.mu.Lock()
	if len(c.streamSamples) >= c.maxSamples {
		c.streamSamples = c.streamSamples[1:]
	}
	c.streamSamples = append(c.streamSamples, sample)
	cb := c.onStreamSample
	c.mu.Unlock()

	if cb != nil {
		go cb(sample)
	}
}

// RecordPlaybackTTFF adds a new playback TTFF sample.
func (c *Collector) RecordPlaybackTTFF(sample PlaybackTTFFSample) {
	c.mu.Lock()
	if len(c.ttffSamples) >= c.maxSamples {
		c.ttffSamples = c.ttffSamples[1:]
	}
	c.ttffSamples = append(c.ttffSamples, sample)
	cb := c.onTTFFSample
	c.mu.Unlock()

	if cb != nil {
		go cb(sample)
	}
}

// GetStreamAPISamples returns a copy of recent stream API samples.
func (c *Collector) GetStreamAPISamples() []StreamAPISample {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]StreamAPISample, len(c.streamSamples))
	copy(result, c.streamSamples)
	return result
}

// GetPlaybackTTFFSamples returns a copy of recent TTFF samples.
func (c *Collector) GetPlaybackTTFFSamples() []PlaybackTTFFSample {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]PlaybackTTFFSample, len(c.ttffSamples))
	copy(result, c.ttffSamples)
	return result
}

// GetStreamAPISummary calculates statistical percentiles for stream API requests.
func (c *Collector) GetStreamAPISummary() StreamAPIStatsSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()

	n := len(c.streamSamples)
	if n == 0 {
		return StreamAPIStatsSummary{}
	}

	totals := make([]float64, n)
	metadatas := make([]float64, n)
	searches := make([]float64, n)
	rankings := make([]float64, n)
	avails := make([]float64, n)

	for i, s := range c.streamSamples {
		totals[i] = durationToMS(s.TotalDuration)
		metadatas[i] = durationToMS(s.MetadataDuration)
		searches[i] = durationToMS(s.SearchDuration)
		rankings[i] = durationToMS(s.RankingDuration)
		avails[i] = durationToMS(s.AvailNZBDuration)
	}

	return StreamAPIStatsSummary{
		Total:    calculateSummary(totals),
		Metadata: calculateSummary(metadatas),
		Search:   calculateSummary(searches),
		Ranking:  calculateSummary(rankings),
		AvailNZB: calculateSummary(avails),
	}
}

// GetPlaybackTTFFSummary calculates statistical percentiles for playback TTFF.
func (c *Collector) GetPlaybackTTFFSummary() PlaybackTTFFStatsSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()

	n := len(c.ttffSamples)
	if n == 0 {
		return PlaybackTTFFStatsSummary{}
	}

	totals := make([]float64, n)
	nzbFetches := make([]float64, n)
	nntpConnects := make([]float64, n)
	probes := make([]float64, n)
	firstBytes := make([]float64, n)

	for i, s := range c.ttffSamples {
		totals[i] = durationToMS(s.TTFF)
		nzbFetches[i] = durationToMS(s.NZBFetchDuration)
		nntpConnects[i] = durationToMS(s.NNTPConnectDuration)
		probes[i] = durationToMS(s.ProbeDuration)
		firstBytes[i] = durationToMS(s.FirstByteDuration)
	}

	return PlaybackTTFFStatsSummary{
		Total:       calculateSummary(totals),
		NZBFetch:    calculateSummary(nzbFetches),
		NNTPConnect: calculateSummary(nntpConnects),
		Probe:       calculateSummary(probes),
		FirstByte:   calculateSummary(firstBytes),
	}
}

func durationToMS(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}

func calculateSummary(values []float64) PerformanceSummary {
	n := len(values)
	if n == 0 {
		return PerformanceSummary{}
	}

	sorted := make([]float64, n)
	copy(sorted, values)
	sort.Float64s(sorted)

	var sum float64
	for _, v := range sorted {
		sum += v
	}

	return PerformanceSummary{
		SampleCount: n,
		MinMS:       round2(sorted[0]),
		MaxMS:       round2(sorted[n-1]),
		AvgMS:       round2(sum / float64(n)),
		P50MS:       round2(percentile(sorted, 50)),
		P95MS:       round2(percentile(sorted, 95)),
		P99MS:       round2(percentile(sorted, 99)),
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}

	rank := (p / 100.0) * float64(len(sorted)-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	if lower == upper {
		return sorted[lower]
	}
	weight := rank - float64(lower)
	return sorted[lower]*(1-weight) + sorted[upper]*weight
}

func round2(val float64) float64 {
	return math.Round(val*100) / 100
}
