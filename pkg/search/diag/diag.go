// Package diag collects per-request search diagnostics: what each indexer was
// asked, how long it took, and where results were dropped on the way from raw
// indexer answers to the streams a client sees.
//
// The collector rides the request context. Every capture site asks the context
// for it and records into it when present, so the search path needs no new
// parameters and pays nothing when no collector was attached. The package
// imports nothing above the standard library on purpose: pkg/indexer,
// pkg/search and pkg/server all record into it, so it must sit below them all.
package diag

import (
	"context"
	"sync"
	"time"
)

// IndexerCall is one Search round trip to one indexer, cache hits included —
// a request answered from cache is still part of the story of a search, it is
// just a very fast call.
type IndexerCall struct {
	Indexer    string `json:"indexer"`
	Mode       string `json:"mode,omitempty"` // "id" or "text"
	DurationMS int64  `json:"duration_ms"`
	Results    int    `json:"results"`
	Cached     bool   `json:"cached,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ValidationStat is the title/year validation outcome of one search request
// (one query label in one mode, summed across its indexers).
type ValidationStat struct {
	Request      string `json:"request"`
	Mode         string `json:"mode"`
	Raw          int    `json:"raw"`
	Kept         int    `json:"kept"`
	DroppedTitle int    `json:"dropped_title"`
	DroppedYear  int    `json:"dropped_year"`
}

// RejectedRelease is one release the filter profile turned away, with jhin's
// reasons ("trash", "attribute:cam", "resolution:480p", ...).
type RejectedRelease struct {
	Title   string   `json:"title"`
	Indexer string   `json:"indexer,omitempty"`
	Reasons []string `json:"reasons"`
}

// Snapshot is the complete diagnostics record for one playlist build. Zero
// values mean "stage did not run": a build served from the raw-search cache
// has no IndexerCalls, only the profile stages.
type Snapshot struct {
	IndexerCalls []IndexerCall    `json:"indexer_calls,omitempty"`
	Validation   []ValidationStat `json:"validation,omitempty"`

	DedupInput  int `json:"dedup_input,omitempty"`
	DedupOutput int `json:"dedup_output,omitempty"`
	BadFiltered int `json:"bad_filtered,omitempty"`

	ProfileName  string            `json:"profile_name,omitempty"`
	ProfileInput int               `json:"profile_input,omitempty"`
	ProfileKept  int               `json:"profile_kept,omitempty"`
	Rejected     []RejectedRelease `json:"rejected,omitempty"`

	TotalMS int64 `json:"total_ms"`
}

// Collector accumulates a Snapshot. All methods are safe on a nil receiver and
// for concurrent use — indexer calls land from the aggregator's goroutines.
type Collector struct {
	mu    sync.Mutex
	snap  Snapshot
	start time.Time
}

type ctxKey struct{}

// Begin attaches a fresh collector to ctx and returns both. The TotalMS clock
// starts here.
func Begin(ctx context.Context) (context.Context, *Collector) {
	c := &Collector{start: time.Now()}
	return context.WithValue(ctx, ctxKey{}, c), c
}

// From returns the collector attached to ctx, or nil when the request is not
// being diagnosed. Callers never need to nil-check: recording into a nil
// collector is a no-op.
func From(ctx context.Context) *Collector {
	if ctx == nil {
		return nil
	}
	c, _ := ctx.Value(ctxKey{}).(*Collector)
	return c
}

func (c *Collector) AddIndexerCall(call IndexerCall) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.snap.IndexerCalls = append(c.snap.IndexerCalls, call)
	c.mu.Unlock()
}

func (c *Collector) AddValidation(stat ValidationStat) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.snap.Validation = append(c.snap.Validation, stat)
	c.mu.Unlock()
}

func (c *Collector) SetDedup(input, output int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.snap.DedupInput, c.snap.DedupOutput = input, output
	c.mu.Unlock()
}

func (c *Collector) SetBadFiltered(count int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.snap.BadFiltered = count
	c.mu.Unlock()
}

func (c *Collector) SetProfile(name string, input, kept int, rejected []RejectedRelease) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.snap.ProfileName = name
	c.snap.ProfileInput, c.snap.ProfileKept = input, kept
	c.snap.Rejected = rejected
	c.mu.Unlock()
}

// Snapshot stamps the elapsed time and returns a copy of everything recorded
// so far. The collector stays usable, but a snapshot is normally taken once,
// at the end of the build.
func (c *Collector) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	snap := c.snap
	snap.TotalMS = time.Since(c.start).Milliseconds()
	snap.IndexerCalls = append([]IndexerCall(nil), c.snap.IndexerCalls...)
	snap.Validation = append([]ValidationStat(nil), c.snap.Validation...)
	snap.Rejected = append([]RejectedRelease(nil), c.snap.Rejected...)
	return snap
}
