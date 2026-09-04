package playback

import (
	"context"
	"errors"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"streamnzb/pkg/media/unpack"
)

// censusVol implements unpack.UnpackableFile (so it fits a blueprint part),
// statCapableFile (so the census can STAT it) and statWidthHinter.
type censusVol struct {
	name     string
	segments int
	missing  map[int]bool  // segment index -> definitively missing
	statErr  error         // transient error returned for every STAT when set
	delay    time.Duration // per-STAT latency
	width    int           // what the fetcher allows; 0 = no hint
	idless   map[int]bool  // NZB-declared holes: no message id to STAT

	mu       sync.Mutex
	stats    []int
	inflight atomic.Int32
	peak     atomic.Int32
}

func (v *censusVol) statCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.stats)
}

func (v *censusVol) Name() string                                               { return v.name }
func (v *censusVol) Size() int64                                                { return int64(v.segments) * 750000 }
func (v *censusVol) EnsureSegmentMap() error                                    { return nil }
func (v *censusVol) OpenStream() (io.ReadSeekCloser, error)                     { return nil, nil }
func (v *censusVol) OpenStreamCtx(context.Context) (io.ReadSeekCloser, error)   { return nil, nil }
func (v *censusVol) OpenReaderAt(context.Context, int64) (io.ReadCloser, error) { return nil, nil }
func (v *censusVol) ReadAt([]byte, int64) (int, error)                          { return 0, nil }
func (v *censusVol) SegmentCount() int                                          { return v.segments }
func (v *censusVol) StatConcurrency() int                                       { return v.width }
func (v *censusVol) SegmentHasMessageID(index int) bool                         { return !v.idless[index] }
func (v *censusVol) StatSegmentAt(ctx context.Context, index int) (bool, error) {
	n := v.inflight.Add(1)
	for {
		p := v.peak.Load()
		if n <= p || v.peak.CompareAndSwap(p, n) {
			break
		}
	}
	defer v.inflight.Add(-1)
	if v.delay > 0 {
		select {
		case <-time.After(v.delay):
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	v.mu.Lock()
	v.stats = append(v.stats, index)
	v.mu.Unlock()
	if v.statErr != nil {
		return false, v.statErr
	}
	// The real loader answers (false, nil) for an id-less segment — the same
	// shape as a unanimous 430 — which is exactly why the census must not ask.
	return !v.missing[index] && !v.idless[index], nil
}

func censusBP(vols ...*censusVol) *unpack.ArchiveBlueprint {
	bp := &unpack.ArchiveBlueprint{MainFileName: "movie.mkv"}
	for _, v := range vols {
		bp.Parts = append(bp.Parts, unpack.VirtualPartDef{VolFile: v})
	}
	return bp
}

func TestCensusDetectsHoleAnywhereInTheBody(t *testing.T) {
	// The reported failure: a clean head, a hole deep in the body that the
	// sampled sweep (2 full volumes + 6 points per later volume) had a ~5%
	// chance of seeing. The census looks at every article.
	vols := make([]*censusVol, 96)
	for i := range vols {
		vols[i] = &censusVol{name: "part" + string(rune('a'+i%26)) + string(rune('0'+i/26)), segments: 129, width: 8}
	}
	vols[40].missing = map[int]bool{77: true}

	err := VerifySelectedFileArticles(context.Background(), censusBP(vols...))
	if err == nil {
		t.Fatal("expected the census to reject the release")
	}
	if !errors.Is(err, ErrFirstSegmentUnavailable) {
		t.Fatalf("expected ErrFirstSegmentUnavailable wrap for bad-release classification, got %v", err)
	}
}

func TestCensusPassesCleanReleaseAndCoversEverything(t *testing.T) {
	vols := []*censusVol{
		{name: "p1", segments: 129, width: 8},
		{name: "p2", segments: 129},
		{name: "p3", segments: 129},
	}
	if err := VerifySelectedFileArticles(context.Background(), censusBP(vols...)); err != nil {
		t.Fatalf("clean release must pass, got %v", err)
	}
	for _, v := range vols {
		if v.statCount() != 129 {
			t.Fatalf("%s: STATed %d of 129 articles", v.name, v.statCount())
		}
	}
}

func TestCensusFailsOpen(t *testing.T) {
	// Transient STAT errors must never reject a release.
	flaky := []*censusVol{{name: "p1", segments: 50, statErr: errors.New("timeout")}}
	if err := VerifySelectedFileArticles(context.Background(), censusBP(flaky...)); err != nil {
		t.Fatalf("transient errors must fail open, got %v", err)
	}
	// Non-archive blueprints are out of scope and must pass.
	if err := VerifySelectedFileArticles(context.Background(), &unpack.DirectBlueprint{}); err != nil {
		t.Fatalf("non-archive blueprint must pass, got %v", err)
	}
	if err := VerifySelectedFileArticles(context.Background(), nil); err != nil {
		t.Fatalf("nil blueprint must pass, got %v", err)
	}
}

func TestCensusBudgetPassesWhatItDidNotReach(t *testing.T) {
	// A large set under a short budget: the census stops in time and passes
	// (no verdict was reached), having asked about a spread of the file.
	vols := make([]*censusVol, 200)
	for i := range vols {
		vols[i] = &censusVol{name: "v" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('0'+i/676)), segments: 129, delay: 200 * time.Microsecond, width: 8}
	}
	// 8 wide at 200 µs a STAT is ~40 STATs/ms: 150 ms reaches a few
	// thousand of the 25,800 articles, well past the 205 anchors even when
	// the race detector slows everything down, and well short of the end.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := VerifySelectedFileArticles(ctx, censusBP(vols...)); err != nil {
		t.Fatalf("an exhausted budget must pass, got %v", err)
	}
	total, touched := 0, 0
	for _, v := range vols {
		if n := v.statCount(); n > 0 {
			touched++
			total += n
		}
	}
	if total == 0 || total >= 200*129 {
		t.Fatalf("expected a partial census, got %d of %d", total, 200*129)
	}
	// Anchors alone touch every volume; the budget must not have starved them.
	if touched < 200 {
		t.Fatalf("anchors must reach every volume, touched %d of 200", touched)
	}
}

func TestCensusWidthComesFromTheFetcher(t *testing.T) {
	vol := &censusVol{name: "p1", segments: 600, delay: 100 * time.Microsecond, width: 3}
	if err := VerifySelectedFileArticles(context.Background(), censusBP(vol)); err != nil {
		t.Fatalf("clean release must pass, got %v", err)
	}
	if peak := vol.peak.Load(); peak > 3 {
		t.Fatalf("census ran %d wide, fetcher allows 3", peak)
	}
	if peak := vol.peak.Load(); peak < 2 {
		t.Fatalf("census ran %d wide, expected to use the allowance", peak)
	}
	// No hint: the default width, not one.
	plain := &censusVol{name: "p2", segments: 600, delay: 100 * time.Microsecond}
	if err := VerifySelectedFileArticles(context.Background(), censusBP(plain)); err != nil {
		t.Fatalf("clean release must pass, got %v", err)
	}
	if peak := plain.peak.Load(); peak > censusDefaultWidth {
		t.Fatalf("unhinted census ran %d wide, default is %d", peak, censusDefaultWidth)
	}
}

// A hole the NZB itself declares is not a provider miss: the census skips it
// (the pre-flight owns it) and passes a release whose only holes are gaps in
// the numbering — except segment 0, which stays fatal as it always was.
func TestCensusSkipsNZBDeclaredHoles(t *testing.T) {
	vols := []*censusVol{
		{name: "p1", segments: 129, width: 8},
		{name: "p2", segments: 129, idless: map[int]bool{40: true, 41: true}},
		{name: "p3", segments: 129},
	}
	if err := VerifySelectedFileArticles(context.Background(), censusBP(vols...)); err != nil {
		t.Fatalf("NZB-declared holes must not be reported as missing on providers, got %v", err)
	}
	if n := vols[1].statCount(); n != 127 {
		t.Fatalf("id-less segments were STATed: %d of 129 asked, want 127", n)
	}
	headless := []*censusVol{{name: "p1", segments: 129, width: 8, idless: map[int]bool{0: true}}}
	if err := VerifySelectedFileArticles(context.Background(), censusBP(headless...)); !errors.Is(err, ErrFirstSegmentUnavailable) {
		t.Fatalf("a missing first segment stays fatal, got %v", err)
	}
}

func TestCensusOrderIsSpread(t *testing.T) {
	// Any prefix of the order is a uniform sample of the file: after the
	// anchors, the first 64 points of a 4096-article file are spaced no
	// worse than about twice the ideal 64-article stride.
	vol := &censusVol{name: "p1", segments: 4096}
	order := censusOrder([]statCapableFile{vol})
	if len(order) != 4096 {
		t.Fatalf("planned %d, want 4096", len(order))
	}
	prefix := make([]int, 0, 64)
	for _, p := range order[:64] {
		prefix = append(prefix, p.seg)
	}
	sort.Ints(prefix)
	maxGap := prefix[0]
	for i := 1; i < len(prefix); i++ {
		if gap := prefix[i] - prefix[i-1]; gap > maxGap {
			maxGap = gap
		}
	}
	if maxGap > 2*4096/64+8 {
		t.Fatalf("prefix is not spread: max gap %d", maxGap)
	}
	seen := make(map[int]bool, 4096)
	for _, p := range order {
		if seen[p.seg] {
			t.Fatalf("segment %d emitted twice", p.seg)
		}
		seen[p.seg] = true
	}
}

func TestCensusPreloadCtxStaysInsideTheWindow(t *testing.T) {
	// A caller with 12 s left gets 4 s, not the 10 s budget; one with no
	// deadline gets the budget.
	parent, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	ctx, cancel2 := censusPreloadCtx(parent)
	defer cancel2()
	d, ok := ctx.Deadline()
	if !ok || time.Until(d) > 4*time.Second+100*time.Millisecond {
		t.Fatalf("budget = %v, want about a third of 12 s", time.Until(d))
	}
	ctx2, cancel3 := censusPreloadCtx(context.Background())
	defer cancel3()
	d2, _ := ctx2.Deadline()
	if left := time.Until(d2); left > censusPreloadBudget || left < censusPreloadBudget-100*time.Millisecond {
		t.Fatalf("budget = %v, want %v", left, censusPreloadBudget)
	}
}
