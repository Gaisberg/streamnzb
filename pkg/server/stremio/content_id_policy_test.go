package stremio

import (
	"context"
	"strings"
	"testing"
)

// The meta endpoint and the search path now split a content id with the same
// function but keep different policies on top of it, and that difference is
// deliberate: search answers an unusable id with an empty result set, while a
// client waiting on /meta needs to be told the id is no good rather than handed
// an empty document.
//
// These are the ids query.ParseContentID tolerates and this endpoint must not.
// Nothing here reaches a metadata client, so a bare Server is enough.
func TestResolveMetaIDRejectsWhatTheSearchPathTolerates(t *testing.T) {
	srv := &Server{}

	for _, tc := range []struct {
		id       string
		wantWord string
	}{
		{"tmdb:abc", "tmdb"},
		{"tmdb:0", "tmdb"},
		{"tmdb:-3", "tmdb"},
		{"tmdb:", "tmdb"},
		{"kitsu:", "kitsu"},
		{"tvdb:", "tvdb"},
		{"anilist:1234", "unrecognized"},
		{"wat", "unrecognized"},
		{"", "unrecognized"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			rid, err := srv.resolveMetaID(context.Background(), "movie", tc.id)
			if err == nil {
				t.Fatalf("expected an error, got %+v", rid)
			}
			if !strings.Contains(err.Error(), tc.wantWord) {
				t.Fatalf("error %q does not name %q", err, tc.wantWord)
			}
			if !strings.Contains(err.Error(), tc.id) {
				t.Fatalf("error %q does not quote the id it rejected", err)
			}
		})
	}
}

// The same ids through the shared parser: empty, not an error. If this ever
// starts failing because ParseContentID grew an error return, the two policies
// have been collapsed into one and the meta endpoint has quietly changed.
func TestTheSearchPathStillToleratesThoseIDs(t *testing.T) {
	srv := &Server{}
	for _, id := range []string{"anilist:1234", "wat", ""} {
		if _, err := srv.resolveMetaID(context.Background(), "movie", id); err == nil {
			t.Fatalf("meta should reject %q", id)
		}
		if got := baseContentID(id); got != "" {
			t.Fatalf("baseContentID(%q) = %q, want empty rather than an error", id, got)
		}
	}
}

// baseContentID collapses a request id — which may carry a season and episode —
// to the preview-id form the catalogs key on. It is the other caller that reads
// a full request id, so it shares the parser; what it keeps is the rule that a
// number has to be a usable id, not merely numeric.
func TestBaseContentID(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"tt0903747:2:4", "tt0903747"},
		{"tt0133093", "tt0133093"},
		{"kitsu:486:3", "kitsu:486"},
		{"kitsu:486", "kitsu:486"},
		{"tmdb:1396:2:4", "tmdb:1396"},
		{"tvdb:81189:2:4", "tvdb:81189"},
		{"603", "tmdb:603"},
		{"603:1:2", "tmdb:603"},
		{"  603  ", "tmdb:603"},

		// Not ids, and each for its own reason.
		{"0", ""},
		{"-5", ""},
		{"kitsu:", ""},
		{"tmdb:", ""},
		{"anilist:99", ""},
		{"", ""},
	} {
		if got := baseContentID(tc.in); got != tc.want {
			t.Fatalf("baseContentID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
