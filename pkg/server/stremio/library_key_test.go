package stremio

import (
	"errors"
	"fmt"
	"testing"

	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/release"
)

func TestLibraryContentID(t *testing.T) {
	// Fixed priority: imdb > tmdb > tvdb > kitsu.
	cases := []struct {
		imdb, tmdb, tvdb, kitsu string
		want                    string
	}{
		{"tt1", "111", "222", "k9", "tt1"},
		{"", "111", "222", "k9", "111"},
		{"", "", "222", "k9", "222"},
		{"", "", "", "k9", "k9"},
		{"  ", "", "", "k9", "k9"},
		{"", "", "", "", ""},
	}
	for _, c := range cases {
		if got := libraryContentID(c.imdb, c.tmdb, c.tvdb, c.kitsu); got != c.want {
			t.Errorf("libraryContentID(%q,%q,%q,%q) = %q, want %q", c.imdb, c.tmdb, c.tvdb, c.kitsu, got, c.want)
		}
	}
}

func TestPreloadConfirmsBadRelease(t *testing.T) {
	s := &Server{}
	// The forced-decode probe wraps the underlying stream cause; a 430/corruption
	// discovered past the header must be treated as a confirmed bad release.
	confirmed := []error{
		fmt.Errorf("speculative pre-probing rejected stream: FFprobe validation failed: ffprobe execution failed (exit 1): : %w", errors.New("fetch segment abc@x: 430 No Such Article")),
		fmt.Errorf("probe: %w", errors.New("rapidyenc data corruption in segment")),
		fmt.Errorf("archive volume segment unavailable: %w", ErrFirstSegmentUnavailable),
	}
	for _, err := range confirmed {
		if !s.preloadConfirmsBadRelease(err) {
			t.Errorf("expected confirmed bad: %v", err)
		}
	}

	// Inconclusive failures must NOT poison the slot.
	inconclusive := []error{
		nil,
		errors.New("FFprobe could not decode any video frames (file=x): stream is corrupt or unplayable"),
		errors.New("context deadline exceeded"),
		errors.New("FFprobe verified media stream is audio-only (audio_codec=eac3): missing video track"),
	}
	for _, err := range inconclusive {
		if s.preloadConfirmsBadRelease(err) {
			t.Errorf("expected NOT confirmed bad: %v", err)
		}
	}
}

// The preload sweep stops at a library entry only when the stored verdict is
// good: pending means the mapping was saved but nothing ever validated it, so
// it must be preloaded like any other candidate, and a non-library release
// never short-circuits the sweep.
func TestLibraryReleaseValidated(t *testing.T) {
	cases := []struct {
		name string
		rel  *release.Release
		want bool
	}{
		{"nil_release", nil, false},
		{"not_library", &release.Release{}, false},
		{"library_without_item", &release.Release{IsLibrary: true}, false},
		{"library_pending", &release.Release{IsLibrary: true,
			SourceIndexer: &persistence.LibraryItem{Status: persistence.LibraryStatusPending}}, false},
		{"library_good", &release.Release{IsLibrary: true,
			SourceIndexer: &persistence.LibraryItem{Status: persistence.LibraryStatusGood}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := libraryReleaseValidated(tc.rel); got != tc.want {
				t.Fatalf("libraryReleaseValidated = %v, want %v", got, tc.want)
			}
		})
	}
}
