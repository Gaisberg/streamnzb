package playback

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/usenet/pool"
)

// statFetcher answers STATs for the volume whose subject it is asked about.
// missing volumes answer (false, nil) — the definitive 430 verdict — and
// erroring ones answer (false, err), which proves nothing.
type statFetcher struct {
	missing map[string]bool
	err     error
	block   time.Duration
}

func (s *statFetcher) FetchSegment(context.Context, *nzb.Segment, []string) (pool.SegmentData, error) {
	return pool.SegmentData{}, errors.New("not used")
}

func (s *statFetcher) StatSegment(ctx context.Context, msgID string, _ []string) (bool, error) {
	if s.block > 0 {
		select {
		case <-time.After(s.block):
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	if s.err != nil {
		return false, s.err
	}
	for name := range s.missing {
		if len(msgID) >= len(name) && msgID[:len(name)] == name {
			return false, nil
		}
	}
	return true, nil
}

func (s *statFetcher) StatConcurrency() int { return 4 }

// statVolumes builds count archive volumes of 20 segments each, with message
// ids prefixed by the volume name so the fetcher can answer per volume.
func statVolumes(fetcher loader.SegmentFetcher, count int) []*loader.File {
	files := make([]*loader.File, count)
	for i := range files {
		name := fmt.Sprintf("part%02d", i)
		segments := make([]nzb.Segment, 20)
		for j := range segments {
			segments[j] = nzb.Segment{ID: fmt.Sprintf("%s-seg-%d", name, j), Number: j + 1, Bytes: 1024}
		}
		files[i] = loader.NewFile(context.Background(), &nzb.File{
			Subject:  name,
			Groups:   []string{"alt.test"},
			Segments: segments,
		}, nil, fetcher)
	}
	return files
}

func TestVerifyRequiredArchivesExistAllPresent(t *testing.T) {
	files := statVolumes(&statFetcher{}, 12)

	exists, err := VerifyRequiredArchivesExist(context.Background(), files)
	if !exists || err != nil {
		t.Fatalf("VerifyRequiredArchivesExist() = (%v, %v), want (true, nil)", exists, err)
	}
}

// A volume that every provider answers 430 for is the one case that condemns
// the release, so it must carry the sentinel the reporting layer matches on.
func TestVerifyRequiredArchivesExistDefinitiveMissIsSentinel(t *testing.T) {
	files := statVolumes(&statFetcher{missing: map[string]bool{"part07": true}}, 12)

	exists, err := VerifyRequiredArchivesExist(context.Background(), files)
	if exists {
		t.Fatalf("VerifyRequiredArchivesExist() exists = true, want false")
	}
	if !errors.Is(err, ErrFirstSegmentUnavailable) {
		t.Fatalf("VerifyRequiredArchivesExist() err = %v, want ErrFirstSegmentUnavailable", err)
	}
}

// An NZB whose own segment numbering declares more articles than it carries is
// incomplete in the document itself: no STAT can overturn that, and past the
// zero-fill budget it can never stream. It used to serve a truncated,
// byte-shifted file whose container header pointed players past EOF — an
// endless 416 loop that nothing ever classified as a slot failure.
func TestVerifyRequiredArchivesExistRefusesNZBGapPastZeroFillBudget(t *testing.T) {
	files := statVolumes(&statFetcher{}, 3)

	segments := make([]nzb.Segment, 0, 30)
	for num := 1; num <= 30+loader.MaxZeroFills+1; num++ {
		if num > 10 && num <= 10+loader.MaxZeroFills+1 {
			continue // a MaxZeroFills+1 hole in the numbering
		}
		segments = append(segments, nzb.Segment{ID: fmt.Sprintf("gappy-seg-%d", num), Number: num, Bytes: 1024})
	}
	files = append(files, loader.NewFile(context.Background(), &nzb.File{
		Subject:  "gappy",
		Groups:   []string{"alt.test"},
		Segments: segments,
	}, nil, &statFetcher{}))

	exists, err := VerifyRequiredArchivesExist(context.Background(), files)
	if exists {
		t.Fatalf("VerifyRequiredArchivesExist() exists = true, want false")
	}
	if !errors.Is(err, ErrFirstSegmentUnavailable) {
		t.Fatalf("VerifyRequiredArchivesExist() err = %v, want ErrFirstSegmentUnavailable", err)
	}
}

// The regression this whole change exists for: a STAT that never got an answer
// must not come back wearing the missing-article verdict, because callers turn
// that sentinel into an AvailNZB report and a multi-day ban.
func TestVerifyRequiredArchivesExistTransientErrorIsNotAVerdict(t *testing.T) {
	transient := errors.New("stat segment <seg>: connection reset by peer")
	files := statVolumes(&statFetcher{err: transient}, 12)

	exists, err := VerifyRequiredArchivesExist(context.Background(), files)
	if exists {
		t.Fatalf("VerifyRequiredArchivesExist() exists = true, want false")
	}
	if !errors.Is(err, transient) {
		t.Fatalf("VerifyRequiredArchivesExist() err = %v, want the underlying transient error", err)
	}
	if errors.Is(err, ErrFirstSegmentUnavailable) {
		t.Fatal("a transient STAT error was reported as a definitive missing-article verdict")
	}
}

// An expired budget is just another way to learn nothing about the release.
func TestVerifyRequiredArchivesExistDeadlineIsNotAVerdict(t *testing.T) {
	files := statVolumes(&statFetcher{block: time.Minute}, 12)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	exists, err := VerifyRequiredArchivesExist(ctx, files)
	if exists {
		t.Fatalf("VerifyRequiredArchivesExist() exists = true, want false")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("VerifyRequiredArchivesExist() err = %v, want context.DeadlineExceeded", err)
	}
	if errors.Is(err, ErrFirstSegmentUnavailable) {
		t.Fatal("an expired sampling budget was reported as a definitive missing-article verdict")
	}
}

// A proven miss outranks siblings that merely failed to answer, so a release
// with a real hole is still caught while a provider is flaking.
func TestVerifyRequiredArchivesExistMissOutranksTransientSiblings(t *testing.T) {
	slow := statVolumes(&statFetcher{missing: map[string]bool{"part03": true}, block: 0}, 12)

	exists, err := VerifyRequiredArchivesExist(context.Background(), slow)
	if exists || !errors.Is(err, ErrFirstSegmentUnavailable) {
		t.Fatalf("VerifyRequiredArchivesExist() = (%v, %v), want a definitive miss", exists, err)
	}
}

// The sampling must leave room in the phase that owns it: taking the caller's
// deadline verbatim is what let every probe still be in flight when the 5s
// startup budget expired.
func TestStatSampleCtxLeavesHalfTheCallerBudget(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ctx, cancelSample := statSampleCtx(parent)
	defer cancelSample()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("statSampleCtx() produced no deadline")
	}
	if budget := time.Until(deadline); budget > 2600*time.Millisecond {
		t.Fatalf("sampling budget = %v, want at most half of the 5s phase budget", budget)
	}
}

func TestStatSampleCtxCapsAnUnboundedCaller(t *testing.T) {
	ctx, cancel := statSampleCtx(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("statSampleCtx() produced no deadline")
	}
	if budget := time.Until(deadline); budget > statSampleBudget {
		t.Fatalf("sampling budget = %v, want at most %v", budget, statSampleBudget)
	}
}
