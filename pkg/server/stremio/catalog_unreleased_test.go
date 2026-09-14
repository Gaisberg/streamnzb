package stremio

import (
	"testing"
	"time"

	"streamnzb/pkg/core/config"
)

// The window keeps what is about to land and drops what is only announced,
// while never hiding a row whose provider published no date at all — that is
// missing data, not a distant release.
func TestFilterUnreleasedPreviews(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	previews := []MetaPreview{
		{ID: "released", released: "2024-01-07"},
		// The source says this has not aired and publishes no date for it:
		// nothing can place it inside a window, unlike "undated" below, whose
		// source says nothing at all.
		{ID: "upcoming-undated", unreleased: true},
		{ID: "upcoming-dated", unreleased: true, released: "2026-09-21"},
		{ID: "yesterday", released: "2026-09-13"},
		{ID: "in-a-week", released: "2026-09-21"},
		{ID: "in-two-months", released: "2026-11-20"},
		{ID: "next-year", released: "2027"},
		{ID: "this-year-bare", released: "2026"},
		{ID: "undated", released: ""},
		{ID: "unparseable", released: "soon"},
	}

	for _, tc := range []struct {
		window int
		want   []string
	}{
		{30, []string{"released", "upcoming-dated", "yesterday", "in-a-week", "this-year-bare", "undated", "unparseable"}},
		{0, []string{"released", "yesterday", "this-year-bare", "undated", "unparseable"}},
		// The far end of the slider reads as "everything upcoming", so the
		// dateless announcements come back too.
		{365, []string{"released", "upcoming-undated", "upcoming-dated", "yesterday", "in-a-week", "in-two-months", "next-year", "this-year-bare", "undated", "unparseable"}},
	} {
		rows := make([]MetaPreview, len(previews))
		copy(rows, previews)
		got := searchPreviewIDs(filterUnreleasedPreviews(rows, tc.window, now))
		if len(got) != len(tc.want) {
			t.Fatalf("window %d kept %v, want %v", tc.window, got, tc.want)
		}
		for i, id := range got {
			if id != tc.want[i] {
				t.Fatalf("window %d kept %v, want %v", tc.window, got, tc.want)
			}
		}
	}
}

// nil is a profile that never set the window, which is the default month
// rather than "no limit"; a saved value is clamped to the editor's range.
func TestEffectiveUnreleasedWindowDays(t *testing.T) {
	days := func(v int) *int { return &v }
	for _, tc := range []struct {
		profile *config.MetadataProfileConfig
		want    int
	}{
		{nil, config.DefaultUnreleasedWindowDays},
		{&config.MetadataProfileConfig{}, config.DefaultUnreleasedWindowDays},
		{&config.MetadataProfileConfig{UnreleasedWindowDays: days(0)}, 0},
		{&config.MetadataProfileConfig{UnreleasedWindowDays: days(90)}, 90},
		{&config.MetadataProfileConfig{UnreleasedWindowDays: days(-5)}, 0},
		{&config.MetadataProfileConfig{UnreleasedWindowDays: days(10000)}, config.MaxUnreleasedWindowDays},
	} {
		if got := tc.profile.EffectiveUnreleasedWindowDays(); got != tc.want {
			t.Errorf("EffectiveUnreleasedWindowDays() = %d, want %d", got, tc.want)
		}
	}
}

// Artwork is what a client renders a row as, so a row without it is the blank
// tile the sources hand back for a record nobody has filled in.
func TestFilterIncompletePreviews(t *testing.T) {
	got := searchPreviewIDs(filterIncompletePreviews([]MetaPreview{
		{ID: "complete", Name: "A Title", Poster: "https://images/p.jpg"},
		{ID: "no-poster", Name: "A Title"},
		{ID: "blank-poster", Name: "A Title", Poster: "   "},
		{ID: "poster-but-nothing-else", Poster: "https://images/q.jpg"},
	}))
	want := []string{"complete", "poster-but-nothing-else"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("kept %v, want %v", got, want)
	}
}

// Unset means on: an unrenderable row is noise in every client, so a profile
// that never touched the toggle still gets the filter.
func TestEffectiveHideIncompleteMetadata(t *testing.T) {
	off, on := false, true
	for _, tc := range []struct {
		profile *config.MetadataProfileConfig
		want    bool
	}{
		{nil, true},
		{&config.MetadataProfileConfig{}, true},
		{&config.MetadataProfileConfig{HideIncompleteMetadata: &on}, true},
		{&config.MetadataProfileConfig{HideIncompleteMetadata: &off}, false},
	} {
		if got := tc.profile.EffectiveHideIncompleteMetadata(); got != tc.want {
			t.Errorf("EffectiveHideIncompleteMetadata() = %v, want %v", got, tc.want)
		}
	}
}
