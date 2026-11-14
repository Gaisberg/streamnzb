package session

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/usenet/pool"
)

// retiringBatchFetcher pipelines one batch: it publishes the first article,
// waits on the connection until the batch is cancelled, then publishes a reply
// that was already on the wire. Fallback fetches answer from a third host.
type retiringBatchFetcher struct {
	started  chan context.Context
	returned chan struct{}
	batchLen int // written before returned closes
}

func (f *retiringBatchFetcher) FetchSegment(ctx context.Context, seg *nzb.Segment, _ []string) (pool.SegmentData, error) {
	if err := ctx.Err(); err != nil {
		return pool.SegmentData{}, err
	}
	return pool.SegmentData{Body: []byte{byte(seg.Number)}, ProviderHost: "fallback.example"}, nil
}

func (f *retiringBatchFetcher) PipelineDepth() int    { return 3 }
func (f *retiringBatchFetcher) FetchConcurrency() int { return 1 }

func (f *retiringBatchFetcher) FetchSegmentsPipelined(ctx context.Context, segments []*nzb.Segment, groups []string) []pool.PipelinedResult {
	panic("the session wrapper dropped incremental delivery")
}

func (f *retiringBatchFetcher) FetchSegmentsPipelinedIncremental(ctx context.Context, segments []*nzb.Segment, _ []string, publish func(int, pool.SegmentData)) []pool.PipelinedResult {
	defer close(f.returned)
	results := make([]pool.PipelinedResult, len(segments))
	first := pool.SegmentData{Body: []byte{1}, ProviderHost: "pipeline.example"}
	results[0] = pool.PipelinedResult{OK: true, Data: first}
	publish(0, first)
	f.started <- ctx
	<-ctx.Done()
	// Every remaining reply was already on the wire. Delivering them all also
	// leaves the loader no fallback goroutine to outlive the test.
	for i := 1; i < len(segments); i++ {
		late := pool.SegmentData{Body: []byte{byte(i + 1)}, ProviderHost: "late.example"}
		results[i] = pool.PipelinedResult{OK: true, Data: late}
		publish(i, late)
	}
	// A duplicate delivery of an index must not be forwarded twice.
	publish(0, first)
	f.batchLen = len(segments)
	return results
}

// A pipelined batch belongs to the session that started it. Retiring that
// session mid-batch cancels the batch, so its connection is given back, and
// every article it still delivers is accounted to the retired session, never
// to the replacement now serving under the same slot id.
func TestRetiredSessionsIncrementalBatchStaysOnItsSession(t *testing.T) {
	m := newAccountingManager(t)
	const slot = "stream_test:movie:tt123:0"
	a := newSlotSession(t, m, slot, "https://indexer/a.nzb")
	a.BeginServeProviderTracking()

	base := &retiringBatchFetcher{started: make(chan context.Context, 1), returned: make(chan struct{})}
	segs := make([]nzb.Segment, 6)
	for i := range segs {
		segs[i] = nzb.Segment{Number: i + 1, ID: fmt.Sprintf("<seg-%d>", i), Bytes: 1}
	}
	file := loader.NewFile(a.Context(), &nzb.File{Subject: "video.mkv", Segments: segs}, nil, attachProviderTracking(a, base))
	file.ReadAheadRange(a.Context(), 0, 3)

	var batchCtx context.Context
	select {
	case batchCtx = <-base.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the read-ahead never started a pipelined batch")
	}
	if got := a.UsedProviderHosts(); !slices.Contains(got, "pipeline.example") {
		t.Fatalf("published article not accounted before the batch finished: used=%v", got)
	}

	b := replaceSlot(t, m, slot)
	b.BeginServeProviderTracking()
	select {
	case <-batchCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("retiring the session left its pipelined batch holding the connection")
	}
	select {
	case <-base.returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled batch never returned")
	}
	if base.batchLen != 3 {
		t.Fatalf("read-ahead pipelined %d articles, want the whole 3-article window in one batch", base.batchLen)
	}

	for _, host := range []string{"pipeline.example", "late.example"} {
		if !slices.Contains(a.UsedProviderHosts(), host) || !slices.Contains(a.ServedProviderHosts(), host) {
			t.Fatalf("%s not accounted to the retired session: used=%v served=%v", host, a.UsedProviderHosts(), a.ServedProviderHosts())
		}
	}
	if used, served := b.UsedProviderHosts(), b.ServedProviderHosts(); len(used) != 0 || len(served) != 0 {
		t.Fatalf("the replacement was charged for the retired session's batch: used=%v served=%v", used, served)
	}
}
