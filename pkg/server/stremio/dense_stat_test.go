package stremio

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"streamnzb/pkg/media/unpack"
)

// statVol implements both unpack.UnpackableFile (so it fits a blueprint part)
// and statCapableFile (so the dense sweep can STAT it).
type statVol struct {
	name     string
	segments int
	missing  map[int]bool // segment index -> definitively missing
	statErr  error        // transient error returned for every STAT when set

	mu    sync.Mutex
	stats []int // recorded STATed indices
}

func (v *statVol) statCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.stats)
}

func (v *statVol) Name() string                                               { return v.name }
func (v *statVol) Size() int64                                                { return int64(v.segments) * 750000 }
func (v *statVol) EnsureSegmentMap() error                                    { return nil }
func (v *statVol) OpenStream() (io.ReadSeekCloser, error)                     { return nil, nil }
func (v *statVol) OpenStreamCtx(context.Context) (io.ReadSeekCloser, error)   { return nil, nil }
func (v *statVol) OpenReaderAt(context.Context, int64) (io.ReadCloser, error) { return nil, nil }
func (v *statVol) ReadAt([]byte, int64) (int, error)                          { return 0, nil }
func (v *statVol) SegmentCount() int                                          { return v.segments }
func (v *statVol) StatSegmentAt(_ context.Context, index int) (bool, error) {
	v.mu.Lock()
	v.stats = append(v.stats, index)
	v.mu.Unlock()
	if v.statErr != nil {
		return false, v.statErr
	}
	return !v.missing[index], nil
}

func bpFromVols(vols ...*statVol) *unpack.ArchiveBlueprint {
	bp := &unpack.ArchiveBlueprint{MainFileName: "movie.mkv"}
	for _, v := range vols {
		bp.Parts = append(bp.Parts, unpack.VirtualPartDef{VolFile: v})
	}
	return bp
}

func TestDenseStatSweepDetectsHolePastSparseSamples(t *testing.T) {
	// The 11:06 real-world failure: hole early in volume 2, missed by sparse
	// 11-of-96 volume sampling. Startup-window full coverage must catch it.
	vols := make([]*statVol, 96)
	for i := range vols {
		vols[i] = &statVol{name: "part" + string(rune('a'+i%26)) + string(rune('0'+i/26)), segments: 129}
	}
	vols[1].missing = map[int]bool{38: true} // ~121MB in

	err := verifySelectedFileArticlesDense(context.Background(), bpFromVols(vols...))
	if err == nil {
		t.Fatal("expected dense sweep to reject the release")
	}
	if !errors.Is(err, ErrFirstSegmentUnavailable) {
		t.Fatalf("expected ErrFirstSegmentUnavailable wrap for bad-release classification, got %v", err)
	}
}

func TestDenseStatSweepPassesCleanRelease(t *testing.T) {
	vols := []*statVol{
		{name: "p1", segments: 129},
		{name: "p2", segments: 129},
		{name: "p3", segments: 129},
	}
	if err := verifySelectedFileArticlesDense(context.Background(), bpFromVols(vols...)); err != nil {
		t.Fatalf("clean release must pass, got %v", err)
	}
	// Startup window fully covered: every segment of the first two volumes.
	if vols[0].statCount() != 129 || vols[1].statCount() != 129 {
		t.Fatalf("expected full startup-window coverage, got %d/%d", vols[0].statCount(), vols[1].statCount())
	}
	// Later volumes only sampled.
	if vols[2].statCount() == 0 || vols[2].statCount() >= 129 {
		t.Fatalf("expected sampled coverage of later volumes, got %d", vols[2].statCount())
	}
}

func TestDenseStatSweepFailsOpen(t *testing.T) {
	// Transient STAT errors must never reject a release.
	flaky := []*statVol{{name: "p1", segments: 50, statErr: errors.New("timeout")}}
	if err := verifySelectedFileArticlesDense(context.Background(), bpFromVols(flaky...)); err != nil {
		t.Fatalf("transient errors must fail open, got %v", err)
	}
	// Non-archive blueprints are out of scope and must pass.
	if err := verifySelectedFileArticlesDense(context.Background(), &unpack.DirectBlueprint{}); err != nil {
		t.Fatalf("non-archive blueprint must pass, got %v", err)
	}
	if err := verifySelectedFileArticlesDense(context.Background(), nil); err != nil {
		t.Fatalf("nil blueprint must pass, got %v", err)
	}
}

func TestDenseStatSweepRespectsBudget(t *testing.T) {
	// A huge volume set must stay within the total STAT budget by thinning
	// per-volume samples, not by skipping tail volumes entirely.
	vols := make([]*statVol, 500)
	for i := range vols {
		vols[i] = &statVol{name: "v" + string(rune('0'+i%10)) + string(rune('a'+(i/10)%26)) + string(rune('a'+i/260)), segments: 129}
	}
	if err := verifySelectedFileArticlesDense(context.Background(), bpFromVols(vols...)); err != nil {
		t.Fatalf("clean huge release must pass, got %v", err)
	}
	total := 0
	for _, v := range vols {
		total += v.statCount()
	}
	if total > denseStatMaxTotal+2*129 { // startup window is always full
		t.Fatalf("budget exceeded: %d stats", total)
	}
	if last := vols[len(vols)-1]; last.statCount() == 0 {
		t.Fatal("tail volume must still be sampled")
	}
}
