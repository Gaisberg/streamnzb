package playback

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/media/unpack"
)

// Article census: STAT every article of the selected file rather than a
// sample of them. Opt-in (Config.PreloadArticleCensus), preload only.
//
// The sampled sweep it stands in for — the first two volumes in full, six
// points in every later volume — checks about five percent of a file, so a
// single missing article mid-body is invisible ninety-five times out of a
// hundred: playback starts clean and the first seek into the unsampled body
// lands on a hole. A census is the only check that can say a file is clean
// rather than probably clean, and it costs no bytes — only STAT round trips.
//
// It runs under the caller's budget and stops at the first definitive miss:
// before play, a hole means "next candidate", the same verdict the sampled
// sweep gives. What matters when the budget runs out first is the ORDER:
// anchors (the head of the file, its tail, the first article of every volume,
// which is where a missing volume shows), then a bit-reversal permutation of
// the rest, so whatever prefix was reached is spread evenly over the file — a
// census cut off at ten percent has still looked at every part of it.
//
// Width comes from the fetcher (Pool.StatConcurrency, the pool's own
// per-caller STAT budget) and is not a knob: the pool owns the connections
// and is the only thing that can size this sensibly.
const (
	// censusPreloadBudget bounds the census inside one preload attempt. The
	// sampled sweep it replaces takes a few seconds per candidate, and the
	// whole preload pass shares a three-minute window across every candidate
	// it tries, so this stays a slice of that rather than a new allowance.
	censusPreloadBudget = 10 * time.Second
	// censusAnchorHead is how many leading segments of the first volume are
	// anchors.
	censusAnchorHead = 4
	// censusDefaultWidth applies when no volume can say what its fetcher
	// allows.
	censusDefaultWidth = 4
	// censusIngestBudget bounds the census of one file handed over by a
	// download client. Nothing is waiting to play; the client wants to know
	// the file is whole, and a few thousand STATs pipelined across the
	// stream's connections take seconds, so the budget is generous and only
	// stops a census that a provider has stalled.
	censusIngestBudget = 10 * time.Minute
	// An ingest census runs this many STATs wide at least, and at most: the
	// stream's connection budget shared with the other sets validating at
	// the same time (ingest runs up to six side by side), within bounds that
	// keep one census from monopolising a small pool or a provider.
	censusIngestMinWidth = 8
	censusIngestMaxWidth = 32
	censusIngestSharers  = 6
)

// statCapableFile (dense_stat.go) is the slice of loader.File the census
// needs too.

// statWidthHinter is the optional half: loader.File reports the pool's STAT
// budget through it.
type statWidthHinter interface {
	StatConcurrency() int
}

// segmentIDer is the other optional half: loader.File says which segments
// name an article. A segment that does not — a numbering-gap placeholder from
// the NZB — has nothing to STAT and is not the census's to judge: the NZB
// declared that hole, the pre-flight already counts it, and treating it as a
// unanimous 430 would report a release bad to AvailNZB for a gap every
// provider is innocent of. Volume index 0 is the one exception, fatal
// either way, as the sampled sweep has always treated it.
type segmentIDer interface {
	SegmentHasMessageID(index int) bool
}

type censusPoint struct {
	vol int
	seg int
}

// CensusReport is what a census found.
type CensusReport struct {
	Planned   int
	Checked   int
	Missing   int
	Transient int
	// Complete reports every planned article was asked about within the
	// budget — including a census that found a miss and stopped, since it
	// had its answer; false means the budget ran out and the rest went unasked.
	Complete bool
	Width    int
	Duration time.Duration
}

// censusPreloadCtx bounds the census inside the preload attempt: the fixed
// budget, or a third of whatever the caller's deadline has left, so one
// candidate's census never eats the window the remaining candidates need.
func censusPreloadCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	budget := censusPreloadBudget
	if deadline, ok := ctx.Deadline(); ok {
		if third := time.Until(deadline) / 3; third < budget {
			budget = third
		}
	}
	return context.WithTimeout(ctx, budget)
}

// censusVolumes projects a blueprint onto the distinct volumes the census can
// STAT. Nil when there is nothing to census: not an archive blueprint, or a
// volume that cannot STAT — both inconclusive, which the caller treats as a
// pass, exactly as the sampled sweep does.
func censusVolumes(bp unpack.Blueprint) []statCapableFile {
	return censusVolumesFor(bp, nil)
}

// censusVolumesFor is censusVolumes with the session's files at hand, so a
// bare file — a direct blueprint, which names its file by index into those
// files — can be censused as a single volume.
func censusVolumesFor(bp unpack.Blueprint, files []unpack.UnpackableFile) []statCapableFile {
	if db, ok := bp.(*unpack.DirectBlueprint); ok && db != nil {
		if db.FileIndex < 0 || db.FileIndex >= len(files) {
			return nil
		}
		f, ok := files[db.FileIndex].(statCapableFile)
		if !ok || f == nil {
			return nil
		}
		return []statCapableFile{f}
	}
	ab, ok := bp.(*unpack.ArchiveBlueprint)
	if !ok || ab == nil || len(ab.Parts) == 0 {
		return nil
	}
	var vols []statCapableFile
	seen := make(map[string]struct{}, len(ab.Parts))
	for _, p := range ab.Parts {
		f, ok := p.VolFile.(statCapableFile)
		if !ok || f == nil {
			return nil
		}
		if _, dup := seen[f.Name()]; dup {
			continue
		}
		seen[f.Name()] = struct{}{}
		vols = append(vols, f)
	}
	return vols
}

// censusOrder lays out the emission order: anchors, then a bit-reversal
// permutation of the flat segment space, skipping what the anchors covered.
func censusOrder(vols []statCapableFile) []censusPoint {
	total := 0
	starts := make([]int, len(vols))
	for i, v := range vols {
		starts[i] = total
		total += v.SegmentCount()
	}
	if total == 0 {
		return nil
	}
	locate := func(flat int) censusPoint {
		vol := 0
		for vol+1 < len(starts) && starts[vol+1] <= flat {
			vol++
		}
		return censusPoint{vol: vol, seg: flat - starts[vol]}
	}

	order := make([]censusPoint, 0, total)
	emitted := make([]bool, total)
	emit := func(flat int) {
		if flat < 0 || flat >= total || emitted[flat] {
			return
		}
		emitted[flat] = true
		p := locate(flat)
		if p.seg != 0 {
			if ider, ok := vols[p.vol].(segmentIDer); ok && !ider.SegmentHasMessageID(p.seg) {
				return // an NZB-declared hole: nothing to ask, not ours to judge
			}
		}
		order = append(order, p)
	}

	// Anchors: the head of the file, its tail, the first article of every
	// volume. A truncated post and a missing volume both show here, and a
	// missing first segment is fatal on its own.
	for i := 0; i < censusAnchorHead; i++ {
		emit(i)
	}
	emit(total - 1)
	for _, s := range starts {
		emit(s)
	}

	// Bit-reversal permutation of [0, total): the i-th emitted index is the
	// bit-reversed i, so every prefix is spread across the whole range.
	k := bits.Len(uint(total - 1))
	for i := 0; i < 1<<k; i++ {
		r := int(bits.Reverse(uint(i)) >> (bits.UintSize - k))
		if k == 0 {
			r = 0
		}
		emit(r)
	}
	return order
}

// censusWidth is the STAT width the volumes' fetcher allows.
func censusWidth(vols []statCapableFile) int {
	for _, v := range vols {
		if h, ok := v.(statWidthHinter); ok {
			if n := h.StatConcurrency(); n > 0 {
				return n
			}
		}
	}
	return censusDefaultWidth
}

// fetchWidthHinter is the fetch budget a file's fetcher reports.
type fetchWidthHinter interface {
	FetchConcurrency() int
}

// ingestCensusWidth is the STAT width an ingest census uses: a share of the
// connection budget rather than the pool's small per-caller STAT allowance,
// since no playback is competing for the connections.
func ingestCensusWidth(vols []statCapableFile) int {
	budget := 0
	for _, v := range vols {
		if h, ok := v.(fetchWidthHinter); ok {
			if n := h.FetchConcurrency(); n > budget {
				budget = n
			}
		}
	}
	width := budget / censusIngestSharers
	if width < censusIngestMinWidth {
		if w := censusWidth(vols); w > width {
			width = w
		}
		if width < censusIngestMinWidth {
			width = censusIngestMinWidth
		}
	}
	if width > censusIngestMaxWidth {
		width = censusIngestMaxWidth
	}
	return width
}

// runCensus asks about every point in order, width at a time, until the order
// is exhausted, ctx ends, or the first definitive miss. Transient answers are
// counted and skipped: never a verdict, and asking again would only re-spend
// the budget.
func runCensus(ctx context.Context, vols []statCapableFile, order []censusPoint, width int) (CensusReport, string, []censusPoint) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	start := time.Now()
	var (
		mu         sync.Mutex
		wg         sync.WaitGroup
		cursor     int
		unasked    int
		checked    int
		transient  int
		missing    int
		firstMiss  string
		transients []censusPoint
	)
	next := func() (censusPoint, bool) {
		mu.Lock()
		defer mu.Unlock()
		if cursor >= len(order) || runCtx.Err() != nil {
			return censusPoint{}, false
		}
		p := order[cursor]
		cursor++
		return p, true
	}
	for w := 0; w < width; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				p, ok := next()
				if !ok {
					return
				}
				vol := vols[p.vol]
				exists, err := vol.StatSegmentAt(runCtx, p.seg)
				mu.Lock()
				switch {
				case err != nil && runCtx.Err() != nil:
					// The budget ended under this STAT: unasked, not transient.
					unasked++
					mu.Unlock()
					return
				case err != nil:
					checked++
					transient++
					transients = append(transients, p)
				case exists:
					checked++
				default:
					checked++
					missing++
					if firstMiss == "" {
						firstMiss = fmt.Sprintf("volume %d (%s) segment %d", p.vol+1, vol.Name(), p.seg)
					}
					cancel()
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	return CensusReport{
		Planned:   len(order),
		Checked:   checked,
		Missing:   missing,
		Transient: transient,
		Complete:  missing > 0 || (cursor >= len(order) && unasked == 0),
		Width:     width,
		Duration:  time.Since(start),
	}, firstMiss, transients
}

// VerifySelectedFileArticles censuses the selected file under ctx's budget
// and rejects the release on the first article that is missing on every
// provider — the same verdict the sampled sweep gives, reached from a check
// that actually looks. Fail-open by design: a blueprint that cannot be
// censused, transient errors and an exhausted budget all pass.
func VerifySelectedFileArticles(ctx context.Context, bp unpack.Blueprint) error {
	vols := censusVolumes(bp)
	if len(vols) == 0 {
		return nil
	}
	order := censusOrder(vols)
	if len(order) == 0 {
		return nil
	}
	report, firstMiss, _ := runCensus(ctx, vols, order, censusWidth(vols))
	rate := 0.0
	if report.Duration > 0 {
		rate = float64(report.Checked) / report.Duration.Seconds()
	}
	logger.Debug("Article census finished",
		"volumes", len(vols),
		"planned", report.Planned,
		"checked", report.Checked,
		"transient", report.Transient,
		"missing", report.Missing,
		"complete", report.Complete,
		"width", report.Width,
		"stats_per_s", int(rate),
		"duration", report.Duration)
	if report.Missing > 0 {
		// Wrap ErrFirstSegmentUnavailable so the bad-release classification
		// (preloadConfirmsBadRelease → persistent verdict + AvailNZB report) fires.
		return fmt.Errorf("article census: %s missing on all providers: %w", firstMiss, ErrFirstSegmentUnavailable)
	}
	return nil
}

// ErrArticlesUnverified reports an ingest census that could not ask about
// every article: the budget ran out, a provider kept answering with errors,
// or the blueprint is one no census can read. Not a verdict on the release —
// nothing was found missing — but a file a download client is told is
// complete must have been checked in full, so the ingest refuses it.
var ErrArticlesUnverified = errors.New("articles could not all be verified")

// VerifyAllArticles is the census a download-client ingest runs: every
// article of the selected file, RAR volumes or a bare file alike, and the
// verdict fails closed. A definitive miss rejects as a bad release; a census
// that could not finish rejects as unverified. Transient answers are asked
// once more before they count against the file.
func VerifyAllArticles(ctx context.Context, bp unpack.Blueprint, files []unpack.UnpackableFile) error {
	vols := censusVolumesFor(bp, files)
	if len(vols) == 0 {
		return fmt.Errorf("%w: the file's articles cannot be enumerated", ErrArticlesUnverified)
	}
	order := censusOrder(vols)
	if len(order) == 0 {
		return fmt.Errorf("%w: no articles to check", ErrArticlesUnverified)
	}
	budget := censusIngestBudget
	if deadline, ok := ctx.Deadline(); ok {
		if left := time.Until(deadline) - 30*time.Second; left < budget {
			budget = left
		}
	}
	if budget <= 0 {
		return fmt.Errorf("%w: no time left in the job", ErrArticlesUnverified)
	}
	censusCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	width := ingestCensusWidth(vols)
	report, firstMiss, transients := runCensus(censusCtx, vols, order, width)
	if report.Missing == 0 && report.Complete && len(transients) > 0 {
		// Ask the unanswered ones again; a provider hiccup is not a hole.
		again, miss, left := runCensus(censusCtx, vols, transients, width)
		report.Checked += again.Checked
		report.Missing += again.Missing
		report.Transient = len(left)
		report.Complete = again.Complete
		report.Duration += again.Duration
		if miss != "" {
			firstMiss = miss
		}
		transients = left
	}
	rate := 0.0
	if report.Duration > 0 {
		rate = float64(report.Checked) / report.Duration.Seconds()
	}
	logger.Info("Ingest article census finished",
		"volumes", len(vols),
		"planned", report.Planned,
		"checked", report.Checked,
		"transient", len(transients),
		"missing", report.Missing,
		"complete", report.Complete,
		"width", report.Width,
		"stats_per_s", int(rate),
		"duration", report.Duration.Round(time.Millisecond))
	switch {
	case report.Missing > 0:
		return fmt.Errorf("article census: %s missing on all providers: %w", firstMiss, ErrFirstSegmentUnavailable)
	case !report.Complete:
		return fmt.Errorf("%w: budget ended with %d of %d articles asked", ErrArticlesUnverified, report.Checked, report.Planned)
	case len(transients) > 0:
		return fmt.Errorf("%w: %d articles answered with errors twice", ErrArticlesUnverified, len(transients))
	}
	return nil
}
