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
