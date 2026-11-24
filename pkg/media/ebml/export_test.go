package ebml

import "testing"

// HeaderEatenHole hands the loader-backed tests, which live in package
// ebml_test because pkg/media/loader imports this package, a Matroska file and
// a hole that is exactly one article of the given size and begins one byte into
// a block's header: the case where the repair rewrites a byte before the hole.
//
// The frames are sized until the block lands on an article boundary, since the
// loader only ever zero-fills whole articles of one size.
func HeaderEatenHole(t *testing.T, article int) (data []byte, hole Range) {
	t.Helper()
	for frameLen := 300; frameLen < 300+article; frameLen++ {
		f := buildFixture(t, fixtureOpts{frameLen: frameLen})
		if start := f.blockIn(1, 1).start + 1; start%int64(article) == 0 {
			return f.data, Range{Start: start, End: start + int64(article)}
		}
	}
	t.Fatalf("no frame size puts a block header on a %d byte article boundary", article)
	return nil, Range{}
}
