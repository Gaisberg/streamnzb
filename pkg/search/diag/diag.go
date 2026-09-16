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
	"strings"
	"sync"
	"time"
)

// RuleRejectionPrefix marks a rejection written by a profile's named rule, so
// that a rule can be told apart from a trait, a limit or a language in the one
// list they all share. It lives here because this package sits below everyone
// who writes or reads those lists.
const RuleRejectionPrefix = "rule: "

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
	// TitleMismatchKept counts results whose title did not match the metadata
	// but which were kept because the request did not enforce the title (ID
	// requests). A high count against a low Raw is how an indexer answering an
	// ID search with something else becomes visible.
	TitleMismatchKept int `json:"title_mismatch_kept,omitempty"`
}

// RejectedRelease is one release the filter profile turned away, with jhin's
// reasons ("trash", "attribute:cam", "resolution:480p", ...).
type RejectedRelease struct {
	Title   string   `json:"title"`
	Indexer string   `json:"indexer,omitempty"`
	Reasons []string `json:"reasons"`
}

// Drop stages and reasons. They are constants because both the capture sites
// and the history panel name them, and a typo on either side is a filter that
// silently matches nothing.
const (
	DropStageValidation = "validation"
	DropStageBad        = "bad"

	DropReasonTitle    = "title"
	DropReasonYear     = "year"
	DropReasonSeason   = "season"
	DropReasonEpisode  = "episode"
	DropReasonUnparsed = "unparsed"
)

// DroppedRelease is one release a stage before the profile turned away:
// title/year validation, or the known-bad filter. It is RejectedRelease one
// stage earlier, and exists for the same reason — the counts answer "how
// many" but never "was mine one of them", which is the question someone
// whose release did not appear is actually asking.
type DroppedRelease struct {
	Title   string `json:"title"`
	Indexer string `json:"indexer,omitempty"`
	// Stage is "validation" for a title/year/episode mismatch, or "bad" for a
	// release playback has already proven unplayable.
	Stage string `json:"stage"`
	// Reason is the check that turned it away — "title", "year", "season",
	// "episode" or "unparsed" — empty for a stage with only one way to fail.
	Reason string `json:"reason,omitempty"`
	// Detail is what was expected set against what the name said, phrased for
	// a reader: `expected "lioness", got "Special Ops Lioness"`.
	Detail string `json:"detail,omitempty"`
	// Request is the query label the release arrived under, empty for a stage
	// that runs once over the merged set rather than per request.
	Request string `json:"request,omitempty"`
}

// Snapshot is the complete diagnostics record for one playlist build. Zero
// values mean "stage did not run": a build served from the raw-search cache
// has no IndexerCalls, only the profile stages.
type Snapshot struct {
	IndexerCalls []IndexerCall    `json:"indexer_calls,omitempty"`
	Validation   []ValidationStat `json:"validation,omitempty"`

	DedupInput  int `json:"dedup_input,omitempty"`
	DedupOutput int `json:"dedup_output,omitempty"`
	// VariantsKept is how many duplicates survived as same-release variants
	// rather than being discarded. It is the difference between "deduplication
	// removed 15 results" and "15 results are still there as fallbacks".
	VariantsKept int `json:"variants_kept,omitempty"`
	BadFiltered  int `json:"bad_filtered,omitempty"`

	ProfileName string `json:"profile_name,omitempty"`
	// ProfileInput and ProfileKept are not omitempty: a profile that saw
	// nothing, or kept nothing, reports a real zero, and omitting it left the
	// history panel rendering "undefined → undefined" for exactly the searches
	// worth explaining. ProfileName stays the flag for whether a profile ran.
	ProfileInput int               `json:"profile_input"`
	ProfileKept  int               `json:"profile_kept"`
	Rejected     []RejectedRelease `json:"rejected,omitempty"`
	// RulesRejected is how many of those rejections came from the profile's
	// named rules rather than from its traits, limits or languages. A profile
	// that empties a result list reads very differently depending on which,
	// and the reason strings alone make that a counting exercise.
	RulesRejected int `json:"rules_rejected,omitempty"`

	// Dropped are the releases the stages before the profile turned away, each
	// naming the check that did it. The validation counts above say how many;
	// this says which.
	Dropped []DroppedRelease `json:"dropped,omitempty"`
	// DroppedOmitted is how many drops were not recorded because the per-stage
	// cap was reached. A search answered by an indexer with a thousand wrong
	// results must not persist a thousand of them, and a list silently cut
	// short reads as a complete one.
	DroppedOmitted int `json:"dropped_omitted,omitempty"`

	// UnairedAirsAt (RFC 3339) is set when the search was short-circuited
	// because the episode has not aired yet — the reason there are no
	// indexer calls to report.
	UnairedAirsAt string `json:"unaired_airs_at,omitempty"`

	// UnairedTimeKnown reports whether UnairedAirsAt carries a real broadcast
	// time or only an air date. Most streaming titles have no air time on
	// record, and rendering their midnight-UTC placeholder as a clock time
	// would state something no source ever said.
	UnairedTimeKnown bool `json:"unaired_time_known,omitempty"`

	// CertificationBlocked is set when the search was short-circuited by the
	// stream's metadata-profile certification cap (e.g. "R over cap 13", or
	// "unrated" when the cap fails closed on unknown certifications).
	CertificationBlocked string `json:"certification_blocked,omitempty"`

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

func (c *Collector) SetDedup(input, output, variantsKept int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.snap.DedupInput, c.snap.DedupOutput, c.snap.VariantsKept = input, output, variantsKept
	c.mu.Unlock()
}

func (c *Collector) SetUnaired(airsAt string, timeKnown bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.snap.UnairedAirsAt = airsAt
	c.snap.UnairedTimeKnown = timeKnown
	c.mu.Unlock()
}

func (c *Collector) SetCertificationBlocked(reason string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.snap.CertificationBlocked = reason
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

// AddDropped records releases a pre-profile stage turned away, and how many
// more it turned away without recording.
func (c *Collector) AddDropped(dropped []DroppedRelease, omitted int) {
	if c == nil || (len(dropped) == 0 && omitted == 0) {
		return
	}
	c.mu.Lock()
	c.snap.Dropped = append(c.snap.Dropped, dropped...)
	c.snap.DroppedOmitted += omitted
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
	c.snap.RulesRejected = countRuleRejections(rejected)
	c.mu.Unlock()
}

// countRuleRejections counts releases turned away by at least one named rule.
// Rule rejections are prefixed so they stay tellable apart from a trait or a
// limit in the same list.
func countRuleRejections(rejected []RejectedRelease) int {
	n := 0
	for _, r := range rejected {
		for _, reason := range r.Reasons {
			if strings.HasPrefix(reason, RuleRejectionPrefix) {
				n++
				break
			}
		}
	}
	return n
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
	snap.Dropped = append([]DroppedRelease(nil), c.snap.Dropped...)
	return snap
}
