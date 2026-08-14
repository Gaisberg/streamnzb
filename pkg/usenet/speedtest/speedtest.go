// Package speedtest benchmarks a Usenet provider: connection latency, and
// sustained throughput across a ramp of connection counts.
//
// It deliberately bypasses pkg/usenet/pool. That package owns *playback* fetch
// policy — segment caching, singleflight dedupe, provider racing, 430 cooloff —
// and every one of those contaminates a per-provider measurement; a warm segment
// cache alone would report an impossible speed. Like the NNTP proxy and the
// validation checker, this package drives nntp.ClientPool directly.
package speedtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"streamnzb/pkg/core/env"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/usenet/nntp"
)

// ErrBusy is returned when a run is already in flight. Two benchmarks at once
// would share one uplink and measure each other, not the providers.
var ErrBusy = errors.New("speedtest: a run is already in progress")

const (
	// warmup is discarded from the head of every step: the first articles pay
	// for connection setup and TCP slow start, which is not the number anyone
	// is asking for.
	warmup = 1500 * time.Millisecond
	// kneeFraction: the suggested connection count is the smallest step already
	// reaching this share of the peak. Past it, extra connections buy noise.
	kneeFraction = 0.95
	// maxRampSteps bounds how long a full ramp can take.
	maxRampSteps = 5
	// minTrustedWindowMS is the shortest measurement window still steady enough
	// to compare steps against each other.
	minTrustedWindowMS = 1500
	// sampleInterval is how often instantaneous throughput is published. Fast
	// enough for a live gauge to move smoothly, slow enough that the window
	// still covers several segment reads.
	sampleInterval = 250 * time.Millisecond
)

// resolutionProfiles are the sustained video bitrates each tier needs, aimed at
// the high end of what a Usenet release actually carries — remux-class rather
// than streaming-service-class, since that is what people pull here. Numbers are
// average video bitrate; playbackHeadroom covers peak scenes and seeks on top.
var resolutionProfiles = []struct {
	Label string
	Mbps  float64
}{
	{Label: "720p", Mbps: 10},
	{Label: "1080p", Mbps: 25},
	{Label: "4K", Mbps: 60},
}

// playbackHeadroom pads each bitrate: an average is not enough to play a file
// whose peaks run well above it, and a stream that exactly matches its bitrate
// never recovers the time a seek costs.
const playbackHeadroom = 1.25

// ResolutionVerdict answers "can this provider play it, and with how many
// connections" for one resolution tier.
type ResolutionVerdict struct {
	Label string `json:"label"`
	// RequiredMbps is the bitrate including headroom — the bar a step must clear.
	RequiredMbps float64 `json:"required_mbps"`
	// Connections is the smallest measured step that sustains it; 0 when no
	// step did.
	Connections int  `json:"connections"`
	Achieved    bool `json:"achieved"`
	// Streams is how many concurrent playbacks the peak throughput covers.
	Streams int `json:"streams"`
}

// Provider is the connection detail a benchmark needs. It mirrors the provider
// config without importing it, so this package stays below the config layer.
type Provider struct {
	Name        string
	Host        string
	Port        int
	Username    string
	Password    string
	Connections int
	UseSSL      bool
}

// Request is one benchmark run.
type Request struct {
	Provider Provider
	// Quick runs a single step at the provider's full connection count instead
	// of the ramp.
	Quick bool
	// Source supplies the articles. Nil uses the public test NZB.
	Source ArticleSource
	// UsageManager, when set, receives the bytes this run spends. A benchmark
	// downloads real articles against a real (often metered) account, so the
	// quota display would understate usage if they went uncounted.
	UsageManager *nntp.ProviderUsageManager
	// OnStep, when set, is called as each step completes so a caller can stream
	// progress while the run continues.
	OnStep func(StepResult)
	// OnSample, when set, is called several times a second with instantaneous
	// throughput, including during the warm-up a step's own result excludes.
	OnSample func(Sample)
}

// Sample is an instantaneous throughput reading taken mid-step.
type Sample struct {
	Mbps        float64 `json:"mbps"`
	Connections int     `json:"connections"`
	Warmup      bool    `json:"warmup"`
}

// StepResult is one connection count's measurement.
type StepResult struct {
	Connections int     `json:"connections"`
	Mbps        float64 `json:"mbps"`
	Bytes       int64   `json:"bytes"`
	Segments    int     `json:"segments"`
	TTFBMedian  float64 `json:"ttfb_median_ms"`
	TTFBP95     float64 `json:"ttfb_p95_ms"`
	Errors      int     `json:"errors"`
	Missing     int     `json:"missing_articles"`
	WindowMS    int64   `json:"window_ms"`
	// Truncated marks a step whose measurement window was closed by the byte
	// budget rather than by the clock. The rate is still computed over the time
	// it actually ran, but a short window is a noisy one.
	Truncated bool `json:"truncated"`
}

// Report is the finished benchmark.
type Report struct {
	Provider             string       `json:"provider"`
	Host                 string       `json:"host"`
	CompletedAt          time.Time    `json:"completed_at"`
	LatencyMS            float64      `json:"latency_ms"`
	ConnectMS            float64      `json:"connect_ms"`
	Steps                []StepResult `json:"steps"`
	PeakMbps             float64      `json:"peak_mbps"`
	SuggestedConnections int          `json:"suggested_connections"`
	// PlateauFound reports whether throughput actually stopped rising before
	// the last step. False means the suggestion is a lower bound: the ramp ran
	// out of connections before the provider (or the local line) ran out of
	// speed.
	PlateauFound bool `json:"plateau_found"`
	// Resolutions translates the raw throughput into what it can actually play,
	// which is the question a streaming user is really asking.
	Resolutions     []ResolutionVerdict `json:"resolutions"`
	ConfiguredConns int                 `json:"configured_connections"`
	TotalBytes      int64               `json:"total_bytes"`
	Source          string              `json:"source"`
	Warning         string              `json:"warning,omitempty"`
}

// ConnectionResult is the cheap reachability check: dial, authenticate, then
// time a single DATE round-trip.
type ConnectionResult struct {
	OK        bool    `json:"ok"`
	ConnectMS float64 `json:"connect_ms"`
	LatencyMS float64 `json:"latency_ms"`
	Error     string  `json:"error,omitempty"`
}

// Tester runs benchmarks one at a time.
type Tester struct {
	mu sync.Mutex
}

func NewTester() *Tester { return &Tester{} }

// TestConnection dials the provider, authenticates, and times one DATE
// round-trip. The timer starts only after the connection is fully established:
// folding TCP/TLS handshake, greeting and auth into the number inflates it
// several times over and says nothing about the server's responsiveness.
func TestConnection(ctx context.Context, provider Provider) ConnectionResult {
	if strings.TrimSpace(provider.Host) == "" {
		return ConnectionResult{Error: "Host is required"}
	}
	pool := nntp.NewClientPool(provider.Host, provider.Port, provider.UseSSL, provider.Username, provider.Password, 1)
	defer pool.Shutdown()

	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	connectStart := time.Now()
	client, err := pool.Get(dialCtx)
	if err != nil {
		return ConnectionResult{Error: err.Error()}
	}
	connectMS := msSince(connectStart)

	pingStart := time.Now()
	if err := client.Ping(); err != nil {
		pool.Discard(client)
		return ConnectionResult{ConnectMS: connectMS, Error: err.Error()}
	}
	latencyMS := msSince(pingStart)
	pool.Put(client)

	return ConnectionResult{OK: true, ConnectMS: connectMS, LatencyMS: latencyMS}
}

// Run benchmarks one provider. Providers are never run concurrently — they
// share the uplink, so a parallel run would measure the link instead.
func (t *Tester) Run(ctx context.Context, req Request) (*Report, error) {
	if !t.mu.TryLock() {
		return nil, ErrBusy
	}
	defer t.mu.Unlock()

	provider := req.Provider
	if strings.TrimSpace(provider.Host) == "" {
		return nil, errors.New("speedtest: provider host is required")
	}
	if provider.Connections < 1 {
		provider.Connections = 1
	}

	source := req.Source
	if source == nil {
		source = PublicNZBSource{}
	}
	corpus, err := source.Corpus(ctx)
	if err != nil {
		return nil, err
	}

	runCtx, cancelRun := context.WithTimeout(ctx, time.Duration(env.SpeedTestMaxSeconds())*time.Second)
	defer cancelRun()

	steps := rampSteps(provider.Connections, req.Quick)
	pool := nntp.NewClientPool(provider.Host, provider.Port, provider.UseSSL, provider.Username, provider.Password, steps[len(steps)-1])
	defer pool.Shutdown()
	if req.UsageManager != nil && provider.Name != "" {
		pool.SetUsageManager(provider.Name, req.UsageManager)
	}

	report := &Report{
		Provider:        provider.Name,
		Host:            provider.Host,
		ConfiguredConns: provider.Connections,
		Source:          corpus.Label,
	}

	connectStart := time.Now()
	client, err := pool.Get(runCtx)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", provider.Host, err)
	}
	report.ConnectMS = msSince(connectStart)
	pingStart := time.Now()
	if err := client.Ping(); err == nil {
		report.LatencyMS = msSince(pingStart)
		pool.Put(client)
	} else {
		pool.Discard(client)
	}

	state := &runState{maxBytes: env.SpeedTestMaxBytes(), corpus: corpus, onSample: req.OnSample}
	stepDuration := time.Duration(env.SpeedTestStepSeconds()) * time.Second
	if stepDuration <= warmup {
		stepDuration = warmup + time.Second
	}

	logger.Info("Provider speed test started", "provider", provider.Name, "host", provider.Host,
		"steps", steps, "source", corpus.Label)

	for i, connections := range steps {
		// Fair-share the byte ceiling: each step may spend its slice of what is
		// left, so a fast early step cannot eat the budget and leave the last
		// one with a fraction of a window. Unspent budget rolls forward.
		spent := state.bytes.Load()
		remaining := state.maxBytes - spent
		if remaining <= 0 {
			logger.Info("Provider speed test stopped at the byte ceiling", "provider", provider.Name, "remaining_steps", len(steps)-i)
			break
		}
		state.stepLimit.Store(spent + remaining/int64(len(steps)-i))

		result := state.runStep(runCtx, pool, connections, stepDuration)
		report.Steps = append(report.Steps, result)
		if req.OnStep != nil {
			req.OnStep(result)
		}
		if runCtx.Err() != nil {
			break
		}
	}

	report.TotalBytes = state.bytes.Load()
	report.CompletedAt = time.Now()
	report.PeakMbps, report.SuggestedConnections, report.PlateauFound = peakAndKnee(report.Steps)
	report.Resolutions = resolutionVerdicts(report.Steps, report.PeakMbps)
	report.Warning = state.warning(report.Steps)
	if state.looped.Load() {
		logger.Debug("Speed test corpus wrapped", "provider", provider.Name,
			"articles", len(corpus.Articles), "source", corpus.Label)
	}

	if report.PeakMbps <= 0 {
		if state.firstError() != nil {
			return nil, state.firstError()
		}
		return nil, errors.New("speedtest: no articles could be downloaded")
	}

	logger.Info("Provider speed test finished", "provider", provider.Name,
		"peak_mbps", report.PeakMbps, "suggested_connections", report.SuggestedConnections,
		"downloaded_mb", float64(report.TotalBytes)/(1024*1024))
	return report, nil
}

// runState is the per-run bookkeeping shared by every worker of every step.
type runState struct {
	corpus   *Corpus
	maxBytes int64
	onSample func(Sample)

	bytes     atomic.Int64
	cursor    atomic.Int64
	looped    atomic.Bool
	stepLimit atomic.Int64
	truncated atomic.Bool

	mu       sync.Mutex
	firstErr error
	missing  int
}

// exhausted reports whether the whole run has spent its byte ceiling.
func (s *runState) exhausted() bool { return s.bytes.Load() >= s.maxBytes }

// budgetReached reports whether fetching must stop — either the run's ceiling
// or the current step's share of it.
func (s *runState) budgetReached() bool {
	total := s.bytes.Load()
	if total >= s.maxBytes {
		return true
	}
	limit := s.stepLimit.Load()
	return limit > 0 && total >= limit
}

func (s *runState) firstError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firstErr
}

func (s *runState) recordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.firstErr == nil {
		s.firstErr = err
	}
}

// nextArticle hands out the work list round-robin. A corpus shorter than the
// run wraps rather than stopping; the wrap is recorded for the log, since a
// re-read may be served from the provider's own cache and read faster than a
// cold article.
func (s *runState) nextArticle() Article {
	index := s.cursor.Add(1) - 1
	if index >= int64(len(s.corpus.Articles)) {
		s.looped.Store(true)
	}
	return s.corpus.Articles[index%int64(len(s.corpus.Articles))]
}

// warning describes only caveats that change how the numbers should be read.
// A step shortened by the byte ceiling is not one of them by itself — the rate
// is measured over the time it actually ran, so a shortened-but-still-steady
// window is a valid measurement. Only a window too short to be steady is worth
// warning about. Corpus wrapping is logged rather than surfaced: it is a
// diagnostic detail, not something a user can act on.
func (s *runState) warning(steps []StepResult) string {
	s.mu.Lock()
	missing := s.missing
	s.mu.Unlock()

	var notes []string
	if missing > 0 {
		notes = append(notes, fmt.Sprintf("%d article(s) were missing on this provider and skipped", missing))
	}
	for _, step := range steps {
		if step.Truncated && step.WindowMS < minTrustedWindowMS {
			notes = append(notes, fmt.Sprintf(
				"the %d MB byte ceiling cut at least one step below a steady window — raise STREAMNZB_SPEEDTEST_MAX_BYTES to measure it properly",
				s.maxBytes/(1024*1024)))
			break
		}
	}
	return strings.Join(notes, "; ")
}

// runStep measures one connection count. Bytes are counted continuously and
// sampled at the window edges, so an article straddling a boundary contributes
// exactly the bytes that landed inside the window.
//
// The window closes as soon as the workers stop, not when the clock runs out.
// When the byte budget ends a step early, holding the denominator at the full
// duration while the numerator stops would understate that step by whatever
// fraction of the window it spent idle — which is how a truncated last step
// used to look like a provider that got slower under load.
func (s *runState) runStep(ctx context.Context, pool *nntp.ClientPool, connections int, duration time.Duration) StepResult {
	stepCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	accum := &stepAccum{windowStart: time.Now().Add(warmup)}
	go s.publishSamples(stepCtx, connections, accum.windowStart)

	var wg sync.WaitGroup
	for i := 0; i < connections; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.worker(stepCtx, pool, accum)
		}()
	}

	s.awaitFetching(stepCtx, warmup)
	baseBytes, windowT0 := s.bytes.Load(), time.Now()
	s.awaitFetching(stepCtx, duration-warmup)
	windowBytes, window := s.bytes.Load()-baseBytes, time.Since(windowT0)

	cancel()
	wg.Wait()

	result := StepResult{
		Connections: connections,
		Bytes:       windowBytes,
		WindowMS:    window.Milliseconds(),
		Truncated:   s.budgetReached(),
	}
	if window > 0 && windowBytes > 0 {
		result.Mbps = float64(windowBytes) * 8 / window.Seconds() / 1_000_000
	}
	if result.Truncated {
		s.truncated.Store(true)
	}
	accum.fill(&result)
	logger.Debug("Speed test step", "connections", connections, "mbps", result.Mbps,
		"segments", result.Segments, "errors", result.Errors,
		"window_ms", result.WindowMS, "truncated", result.Truncated)
	return result
}

// awaitFetching waits up to d, returning early once the byte budget stops the
// workers so the caller can close its measurement window at the same instant.
func (s *runState) awaitFetching(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	deadline := time.Now().Add(d)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if !now.Before(deadline) || s.budgetReached() {
				return
			}
		}
	}
}

// publishSamples reports throughput over each sample interval for as long as the
// step runs. Samples are a live readout only — a step's own number still comes
// from the steady-state window, so warm-up samples are flagged rather than
// dropped and the gauge can start moving immediately.
func (s *runState) publishSamples(ctx context.Context, connections int, windowStart time.Time) {
	if s.onSample == nil {
		return
	}
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()

	lastBytes, lastAt := s.bytes.Load(), time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			current := s.bytes.Load()
			elapsed := now.Sub(lastAt).Seconds()
			delta := current - lastBytes
			lastBytes, lastAt = current, now
			if elapsed <= 0 {
				continue
			}
			s.onSample(Sample{
				Mbps:        float64(delta) * 8 / elapsed / 1_000_000,
				Connections: connections,
				Warmup:      now.Before(windowStart),
			})
		}
	}
}

// worker holds one connection for the whole step, fetching articles back to
// back. Article bodies are discarded unread: yEnc decoding costs the same on
// every provider, so including it would only add CPU noise to a comparison.
func (s *runState) worker(ctx context.Context, pool *nntp.ClientPool, accum *stepAccum) {
	client, err := pool.Get(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.recordError(err)
			accum.addError()
		}
		return
	}
	healthy := true
	defer func() {
		if healthy {
			pool.Put(client)
			return
		}
		pool.Discard(client)
	}()

	sink := counterWriter{total: &s.bytes}
	for ctx.Err() == nil && !s.budgetReached() {
		article := s.nextArticle()

		start := time.Now()
		body, err := client.Body(article.ID)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if nntp.IsArticleNotFound(err) {
				s.mu.Lock()
				s.missing++
				s.mu.Unlock()
				accum.addMissing()
				continue
			}
			s.recordError(err)
			accum.addError()
			healthy = false
			return
		}
		ttfb := time.Since(start)

		_, copyErr := io.Copy(sink, body)
		body.Close()
		if copyErr != nil {
			if ctx.Err() == nil {
				s.recordError(copyErr)
				accum.addError()
				healthy = false
			}
			return
		}
		accum.addSegment(start, ttfb)
	}
}

// counterWriter discards the body while counting the bytes that reached it.
// io.Copy hands it 32 KiB at a time, which is fine enough granularity to make
// the window edges accurate.
type counterWriter struct{ total *atomic.Int64 }

func (w counterWriter) Write(p []byte) (int, error) {
	w.total.Add(int64(len(p)))
	return len(p), nil
}

// stepAccum collects the per-article samples of one step.
type stepAccum struct {
	windowStart time.Time

	mu       sync.Mutex
	ttfb     []time.Duration
	segments int
	errors   int
	missing  int
}

func (a *stepAccum) addSegment(start time.Time, ttfb time.Duration) {
	if start.Before(a.windowStart) {
		return
	}
	a.mu.Lock()
	a.segments++
	a.ttfb = append(a.ttfb, ttfb)
	a.mu.Unlock()
}

func (a *stepAccum) addError() {
	a.mu.Lock()
	a.errors++
	a.mu.Unlock()
}

func (a *stepAccum) addMissing() {
	a.mu.Lock()
	a.missing++
	a.mu.Unlock()
}

func (a *stepAccum) fill(result *StepResult) {
	a.mu.Lock()
	defer a.mu.Unlock()
	result.Segments = a.segments
	result.Errors = a.errors
	result.Missing = a.missing
	result.TTFBMedian = percentileMS(a.ttfb, 0.5)
	result.TTFBP95 = percentileMS(a.ttfb, 0.95)
}

// rampSteps derives the connection counts to measure. The ladder is what makes
// the result actionable — one number at full depth cannot say whether the
// configured connection count is buying anything.
func rampSteps(maxConnections int, quick bool) []int {
	if maxConnections < 1 {
		maxConnections = 1
	}
	if quick || maxConnections == 1 {
		return []int{maxConnections}
	}
	steps := make([]int, 0, maxRampSteps)
	for _, candidate := range []int{1, 2, 4, 8} {
		if candidate >= maxConnections || len(steps) == maxRampSteps-1 {
			break
		}
		steps = append(steps, candidate)
	}
	return append(steps, maxConnections)
}

// resolutionVerdicts reports, per resolution tier, the smallest measured step
// that sustains it and how many concurrent streams the peak covers. Steps are
// read in ascending order, so the answer is the cheapest connection count that
// works — not merely one that does.
func resolutionVerdicts(steps []StepResult, peak float64) []ResolutionVerdict {
	verdicts := make([]ResolutionVerdict, 0, len(resolutionProfiles))
	for _, profile := range resolutionProfiles {
		required := profile.Mbps * playbackHeadroom
		verdict := ResolutionVerdict{Label: profile.Label, RequiredMbps: required}
		for _, step := range steps {
			// A step too short to be steady cannot promise sustained playback.
			if step.WindowMS < minTrustedWindowMS && len(steps) > 1 {
				continue
			}
			if step.Mbps >= required {
				verdict.Connections = step.Connections
				verdict.Achieved = true
				break
			}
		}
		if peak >= required {
			verdict.Streams = int(peak / required)
			verdict.Achieved = true
		}
		verdicts = append(verdicts, verdict)
	}
	return verdicts
}

// peakAndKnee reports the best throughput seen and the smallest connection
// count that already reaches kneeFraction of it.
//
// Steps whose window was cut short by the byte budget are excluded when any
// full-length step exists: a sub-second window is too noisy to set the peak a
// recommendation is measured against.
// The third return reports whether the ramp actually flattened. When the top
// step is still the fastest, no plateau was found: the limit is at or beyond
// the configured connection count — or somewhere else entirely, like the
// user's own line — and the suggestion is a floor, not a recommendation.
func peakAndKnee(steps []StepResult) (float64, int, bool) {
	trusted := make([]StepResult, 0, len(steps))
	for _, step := range steps {
		if step.WindowMS >= minTrustedWindowMS {
			trusted = append(trusted, step)
		}
	}
	if len(trusted) == 0 {
		trusted = steps
	}

	var peak float64
	for _, step := range trusted {
		if step.Mbps > peak {
			peak = step.Mbps
		}
	}
	if peak <= 0 {
		return 0, 0, false
	}
	knee := trusted[len(trusted)-1].Connections
	for _, step := range trusted {
		if step.Mbps >= peak*kneeFraction {
			knee = step.Connections
			break
		}
	}
	return peak, knee, knee < trusted[len(trusted)-1].Connections
}

func percentileMS(samples []time.Duration, p float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	// Nearest-rank: with the handful of samples a short step produces, rounding
	// down would report p95 as a mid-pack value and hide the tail entirely.
	index := int(math.Ceil(p*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return float64(sorted[index].Microseconds()) / 1000
}

func msSince(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000
}
