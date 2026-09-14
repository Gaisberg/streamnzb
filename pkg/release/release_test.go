package release

import "testing"

func TestIsFullDiscRelease(t *testing.T) {
	tests := []struct {
		title string
		want  bool
	}{
		{"Friday.Night.Lights.2004.2160p.UHD.BluRay.HDR10.HEVC.TrueHD.7.1.Atmos-UnKn0wn.mkv", false},
		{"Friday.Night.Lights.2004.COMPLETE.UHD.BLURAY-B0MBARDiERS", true},
		{"Some.Movie.2023.1080p.BD25", true},
		{"Another.Movie.2023.1080p.BD50", true},
		{"Movie.With.ISO.Extension.iso", true},
		{"Movie.With.ISO.In.Middle.iso.rar", true},
		{"Movie.With.BDMV.In.Title.BDMV", true},
		{"Isolation.2015.1080p.mkv", false},
		{"Normal.Movie.2023.1080p.BluRay.REMUX-Group", false},
	}

	for _, tt := range tests {
		got := IsFullDiscRelease(tt.title)
		if got != tt.want {
			t.Errorf("IsFullDiscRelease(%q) = %v, want %v", tt.title, got, tt.want)
		}
	}
}

// The unique-hit verdict is decided once at search time and read back at
// playback, with a playlist clone in between — so cloning has to carry it, on
// the variants as well as the primary.
func TestCloneCarriesUniqueHitOntoEveryCopy(t *testing.T) {
	rel := &Release{
		Title:     "Movie.2160p.Remux-GRP",
		Indexer:   "NZBGeek",
		UniqueHit: true,
		Variants:  []*Release{{Title: "Movie.2160p.Remux-GRP", Indexer: "NZBGeek", UniqueHit: true}},
	}

	clone := rel.Clone()
	for i, c := range clone.Copies() {
		if !c.UniqueHit {
			t.Fatalf("copy %d: UniqueHit = false, want true", i)
		}
	}

	// A clone is a snapshot, not an alias: re-marking the original must not
	// reach through to a playlist already built from it.
	rel.UniqueHit = false
	if !clone.UniqueHit {
		t.Fatal("clone.UniqueHit followed the original, want an independent copy")
	}
}

// AvailNZB mints the Warden fingerprint from the poster and the usenet date
// together, so a release has to carry both onto every copy a playlist hands to
// playback — the report is sent from whichever copy actually played.
func TestCloneCarriesPosterAndUsenetDate(t *testing.T) {
	rel := &Release{
		Title:      "Movie.2160p.Remux-GRP",
		Poster:     "someone@example.com",
		UsenetDate: "Mon, 08 Jun 2026 12:23:54 +0000",
		Variants: []*Release{{
			Title:      "Movie.2160p.Remux-GRP",
			Poster:     "other@example.com",
			UsenetDate: "Tue, 09 Jun 2026 01:02:03 +0000",
		}},
	}

	clone := rel.Clone()
	for i, c := range clone.Copies() {
		want := rel.CopyAt(i)
		if c.Poster != want.Poster || c.UsenetDate != want.UsenetDate {
			t.Fatalf("copy %d: poster/date = %q/%q, want %q/%q", i, c.Poster, c.UsenetDate, want.Poster, want.UsenetDate)
		}
	}
}

// The usenet date goes to AvailNZB as unix seconds because that is the one
// form no host locale can re-bucket. A value that carries no zone matches none
// of the known layouts, so it is dropped rather than reported and guessed at.
func TestUsenetDateUnix(t *testing.T) {
	tests := []struct {
		name   string
		value  string
		want   int64
		wantOK bool
	}{
		{"rfc1123z", "Mon, 08 Jun 2026 12:23:54 +0000", 1780921434, true},
		{"rfc3339", "2026-06-08T12:23:54Z", 1780921434, true},
		{"offset applied", "2026-06-08T14:23:54+02:00", 1780921434, true},
		{"zone-less rejected", "2026-06-08 12:23:54", 0, false},
		{"unparseable rejected", "not a date", 0, false},
		{"absent", "", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := (&Release{UsenetDate: tt.value}).UsenetDateUnix()
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("UsenetDateUnix(%q) = %d, %v; want %d, %v", tt.value, got, ok, tt.want, tt.wantOK)
			}
		})
	}

	if got, ok := (*Release)(nil).UsenetDateUnix(); ok || got != 0 {
		t.Fatalf("nil release: UsenetDateUnix = %d, %v; want 0, false", got, ok)
	}
}
