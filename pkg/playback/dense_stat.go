package playback

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/media/unpack"
)

const (
	// denseStatStartupVolumes: every segment of the first N volumes is checked —
	// this is where playback startup lives and where a hole hurts most. Both of
	// today's real-world failures (holes at 1.27MB and 121MB) fall in the first
	// two ~92MB volumes, which the old 11-of-96 volume sampling missed.
	denseStatStartupVolumes = 2
	// denseStatMaxTotal caps the total STATs per sweep; the per-volume sample
	// count shrinks for very large volume sets instead of skipping tails.
	denseStatMaxTotal    = 2000
	denseStatConcurrency = 12
	denseStatTimeout     = 45 * time.Second
)

// statCapableFile is the slice of loader.File the dense sweep needs; matched
// structurally so this package needs no direct dependency on the concrete type
// behind unpack.UnpackableFile.
type statCapableFile interface {
	Name() string
	SegmentCount() int
	StatSegmentAt(ctx context.Context, index int) (bool, error)
}

// verifySelectedFileArticlesDense STATs the SELECTED file's volumes densely:
// every article of the first volumes (startup window) plus spread samples in
// every remaining volume. It runs during speculative pre-probing only — a
// definitive miss (430 on all providers) rejects the release before it is ever
// offered, instead of failing mid-playback.
//
// Fail-open by design: anything inconclusive (non-archive blueprint, no statter,
// transient errors, timeout) passes — only a definitive missing article rejects.
func VerifySelectedFileArticlesDense(ctx context.Context, bp unpack.Blueprint) error {
	ab, ok := bp.(*unpack.ArchiveBlueprint)
	if !ok || ab == nil || len(ab.Parts) == 0 {
		return nil
	}

	// Ordered, distinct volumes of the selected file.
	var vols []statCapableFile
	seen := make(map[string]struct{}, len(ab.Parts))
	for _, p := range ab.Parts {
		f, ok := p.VolFile.(statCapableFile)
		if !ok || f == nil {
			return nil // cannot STAT this volume type — inconclusive, pass
		}
		if _, dup := seen[f.Name()]; dup {
			continue
		}
		seen[f.Name()] = struct{}{}
		vols = append(vols, f)
	}
	if len(vols) == 0 {
		return nil
	}

	type job struct {
		vol    statCapableFile
		volIdx int
		segIdx int
	}
	var jobs []job
	addVolume := func(volIdx int, indices []int) {
		v := vols[volIdx]
		n := v.SegmentCount()
		added := make(map[int]struct{}, len(indices))
		for _, idx := range indices {
			if idx < 0 || idx >= n {
				continue
			}
			if _, dup := added[idx]; dup {
				continue
			}
			added[idx] = struct{}{}
			jobs = append(jobs, job{vol: v, volIdx: volIdx, segIdx: idx})
		}
	}

	// Startup window: every segment of the first volumes.
	full := denseStatStartupVolumes
	if full > len(vols) {
		full = len(vols)
	}
	for i := 0; i < full; i++ {
		n := vols[i].SegmentCount()
		all := make([]int, n)
		for j := range all {
			all[j] = j
		}
		addVolume(i, all)
	}
	// Remaining volumes: spread samples. Budget-aware — shrink the per-volume
	// sample set for very large volume counts instead of silently skipping tails.
	rest := len(vols) - full
	perVol := 6
	if rest > 0 {
		if budget := denseStatMaxTotal - len(jobs); budget < rest*perVol {
			perVol = budget / rest
			if perVol < 2 {
				perVol = 2 // never below first+last
			}
		}
	}
	for i := full; i < len(vols); i++ {
		n := vols[i].SegmentCount()
		switch {
		case perVol >= 6:
			addVolume(i, []int{0, 1, n / 4, n / 2, (3 * n) / 4, n - 1})
		case perVol >= 3:
			addVolume(i, []int{0, n / 2, n - 1})
		default:
			addVolume(i, []int{0, n - 1})
		}
	}

	statCtx, cancel := context.WithTimeout(ctx, denseStatTimeout)
	defer cancel()

	start := time.Now()
	var (
		wg         sync.WaitGroup
		sem        = make(chan struct{}, denseStatConcurrency)
		transient  atomic.Int64
		checked    atomic.Int64
		missMu     sync.Mutex
		missDetail string
	)
	for _, j := range jobs {
		if statCtx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()
			if statCtx.Err() != nil {
				return
			}
			exists, err := j.vol.StatSegmentAt(statCtx, j.segIdx)
			checked.Add(1)
			if err != nil {
				transient.Add(1) // inconclusive; never reject on transient errors
				return
			}
			if !exists {
				missMu.Lock()
				if missDetail == "" {
					missDetail = fmt.Sprintf("volume %d (%s) segment %d", j.volIdx+1, j.vol.Name(), j.segIdx)
				}
				missMu.Unlock()
				cancel() // stop remaining work; verdict is already definitive
			}
		}(j)
	}
	wg.Wait()

	missMu.Lock()
	miss := missDetail
	missMu.Unlock()

	logger.Debug("Dense article STAT sweep finished",
		"volumes", len(vols),
		"stats_planned", len(jobs),
		"stats_checked", checked.Load(),
		"transient_errors", transient.Load(),
		"missing", miss != "",
		"duration", time.Since(start))

	if miss != "" {
		// Wrap ErrFirstSegmentUnavailable so the bad-release classification
		// (preProbeConfirmsBadRelease → persistent verdict + AvailNZB report) fires.
		return fmt.Errorf("dense article verification: %s missing on all providers: %w", miss, ErrFirstSegmentUnavailable)
	}
	return nil
}
