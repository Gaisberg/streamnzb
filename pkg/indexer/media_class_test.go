package indexer

import (
	"context"
	"testing"
)

func TestMediaClassOfFoldsEveryVocabulary(t *testing.T) {
	cases := map[string]string{
		"movie": MediaClassMovie, "Movies": MediaClassMovie, "anime_movie": MediaClassMovie,
		"series": MediaClassSeries, "tv": MediaClassSeries, "tv_anime": MediaClassSeries, "anime_show": MediaClassSeries,
		"direct": "", "": "", "  ": "", "music": "",
	}
	for in, want := range cases {
		if got := MediaClassOf(in); got != want {
			t.Errorf("MediaClassOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWithMediaClassRoundTrip(t *testing.T) {
	ctx := WithMediaClass(context.Background(), "tv_anime")
	if got := MediaClassFromContext(ctx); got != MediaClassSeries {
		t.Fatalf("got %q, want %q", got, MediaClassSeries)
	}
	// An unknown class leaves the context alone rather than storing "".
	plain := WithMediaClass(context.Background(), "direct")
	if got := MediaClassFromContext(plain); got != "" {
		t.Fatalf("direct must not set a class, got %q", got)
	}
	if got := MediaClassFromContext(context.TODO()); got != "" {
		t.Fatalf("empty context must answer \"\", got %q", got)
	}
}
