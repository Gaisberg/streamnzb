package ebml_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/textproto"
	"testing"

	"streamnzb/pkg/media/ebml"
	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/usenet/pool"
)

// These tests run the hole-fill reader over the real loader. They live in an
// external test package because pkg/media/loader imports pkg/media/ebml.

// articleFetcher serves data as articles of the given sizes, answering 430 for
// the missing ones the way a provider does.
type articleFetcher struct {
	data    []byte
	sizes   []int
	missing map[int]bool
}

func (f *articleFetcher) FetchSegment(_ context.Context, seg *nzb.Segment, _ []string) (pool.SegmentData, error) {
	index := seg.Number - 1
	if f.missing[index] {
		return pool.SegmentData{}, fmt.Errorf("fetch segment %s: %w", seg.ID, &textproto.Error{Code: 430, Msg: "No Such Article"})
	}
	lo := 0
	for _, size := range f.sizes[:index] {
		lo += size
	}
	return pool.SegmentData{Body: append([]byte(nil), f.data[lo:lo+f.sizes[index]]...)}, nil
}

// openArticles is one playback session over data posted as articles of the given
// sizes, with the articles at the missing indices gone from every provider.
func openArticles(t *testing.T, data []byte, sizes []int, missing ...int) (*loader.File, io.ReadSeekCloser) {
	t.Helper()
	gone := make(map[int]bool, len(missing))
	for _, i := range missing {
		gone[i] = true
	}
	segments := make([]nzb.Segment, len(sizes))
	for i, size := range sizes {
		segments[i] = nzb.Segment{ID: fmt.Sprintf("<seg-%d>", i), Number: i + 1, Bytes: int64(size)}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx = loader.WithSkipGapProbing(ctx, true)

	nzbFile := &nzb.File{Subject: "movie.mkv", Groups: []string{"alt.test"}, Segments: segments}
	f := loader.NewFile(ctx, nzbFile, nil, &articleFetcher{data: data, sizes: sizes, missing: gone})
	stream, err := f.OpenPlaybackStreamCtx(ctx)
	if err != nil {
		t.Fatalf("OpenPlaybackStreamCtx: %v", err)
	}
	t.Cleanup(func() { stream.Close() })
	return f, stream
}

func holeFillOver(stream io.ReadSeekCloser, size int, cache *ebml.PatchCache) *ebml.HoleFillReader {
	return ebml.NewHoleFillReader(stream, stream.(ebml.HoleSource), int64(size), cache, "movie.mkv")
}

// A hole that eats a block's header makes the repair write a Void ID one byte
// before the hole. The loader learns the hole exists only when a read reaches
// it, and reads end where the article before it ends — so without a lookahead
// the first pass over a stream served the original byte and every later pass the
// repaired one, and a client that began its stream on the first pass was handed
// invalid EBML.
func TestFirstReadThroughTheLoaderMatchesEveryLaterRead(t *testing.T) {
	const article = 64
	data, hole := ebml.HeaderEatenHole(t, article)
	sizes := articlesOf(len(data), article)
	missing := int(hole.Start / article)

	_, stream := openArticles(t, data, sizes, missing)
	r := holeFillOver(stream, len(data), ebml.NewPatchCache())
	first, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if first[hole.Start-1] != ebml.IDVoid {
		t.Fatalf("first read served %#02x one byte before the hole, want the Void ID the repair writes", first[hole.Start-1])
	}

	r.Seek(0, io.SeekStart)
	second, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("a second read of the same stream differs from the first (%v)", err)
	}

	// A new session: this read is the one that discovers the hole, from a range
	// that stops short of it.
	_, stream = openArticles(t, data, sizes, missing)
	cold := holeFillOver(stream, len(data), ebml.NewPatchCache())
	cold.Seek(hole.Start-30, io.SeekStart)
	got := make([]byte, 30)
	if _, err := io.ReadFull(cold, got); err != nil || !bytes.Equal(got, first[hole.Start-30:hole.Start]) {
		t.Fatalf("a range ending at the hole differs from the same bytes of a full read (%v)", err)
	}
}

// Holding the last maxHeaderLen bytes back means a read reaches that far past
// what the caller asked for. That is the only extra the loader is asked to do: a
// range that ends more than 12 bytes short of a missing article never touches
// it, and one that ends inside that distance reads that article early — the one
// the player would be given next, already in the read-ahead window. This pins the
// distance against the real loader, so the cost is a number in a test and not a
// surprise in the zero-fill budget.
func TestLookaheadReachesTheNextArticleOnlyWithinTwelveBytes(t *testing.T) {
	const articles, size, missing = 20, 64, 10
	data := bytes.Repeat([]byte{7}, articles*size)
	articleSizes := articlesOf(len(data), size)
	holeStart := int64(missing * size)

	holesAfterReadEndingAt := func(end int64) int {
		f, stream := openArticles(t, data, articleSizes, missing)
		r := holeFillOver(stream, len(data), ebml.NewPatchCache())
		if _, err := r.Seek(end-20, io.SeekStart); err != nil {
			t.Fatalf("seek: %v", err)
		}
		if _, err := io.ReadFull(r, make([]byte, 20)); err != nil {
			t.Fatalf("read: %v", err)
		}
		return f.ZeroFilledSegments()
	}

	const lookahead = 12
	if n := holesAfterReadEndingAt(holeStart - lookahead); n != 0 {
		t.Fatalf("a range ending %d bytes before the missing article recorded %d holes; the lookahead should stop short of it", lookahead, n)
	}
	if n := holesAfterReadEndingAt(holeStart - lookahead + 1); n != 1 {
		t.Fatalf("a range ending %d bytes before the missing article recorded %d holes, want 1", lookahead-1, n)
	}
}

// articlesOf cuts size bytes into articles of one size and a shorter last one,
// as a poster does; the loader's segment map assumes nothing else.
func articlesOf(total, article int) []int {
	var sizes []int
	for ; total > 0; total -= article {
		sizes = append(sizes, min(article, total))
	}
	return sizes
}
