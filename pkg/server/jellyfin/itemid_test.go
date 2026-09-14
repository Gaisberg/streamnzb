package jellyfin

import (
	"strings"
	"testing"
)

func TestItemIDRoundTrips(t *testing.T) {
	cases := []struct {
		contentType, stremioID string
		wantBase               string
	}{
		{"movie", "tt0111161", "tt0111161"},
		{"series", "tt0903747", "tt0903747"},
		{"movie", "tmdb:278", "tmdb:278"},
		{"series", "tvdb:81189", "tvdb:81189"},
		{"anime", "kitsu:1", "kitsu:1"},
		{"movie", "tt123", "tt0000123"},
	}
	for _, tc := range cases {
		id, err := itemIDFor(tc.contentType, tc.stremioID)
		if err != nil {
			t.Fatalf("itemIDFor(%s, %s): %v", tc.contentType, tc.stremioID, err)
		}
		encoded := id.encode()
		if len(encoded) != 32 {
			t.Fatalf("%s: id %q is not GUID-sized", tc.stremioID, encoded)
		}
		got, err := decodeItemID(encoded)
		if err != nil {
			t.Fatalf("%s: decode: %v", tc.stremioID, err)
		}
		if got != id {
			t.Fatalf("%s: round trip %+v != %+v", tc.stremioID, got, id)
		}
		if got.baseStremioID() != tc.wantBase {
			t.Fatalf("%s: base id %q, want %q", tc.stremioID, got.baseStremioID(), tc.wantBase)
		}
		// Hyphenated GUID form and upper case decode the same.
		hyphenated := strings.ToUpper(encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:])
		if again, err := decodeItemID(hyphenated); err != nil || again != id {
			t.Fatalf("%s: hyphenated form did not decode: %v", tc.stremioID, err)
		}
	}
}

func TestItemIDDerivedKinds(t *testing.T) {
	series, _ := itemIDFor("series", "tt0903747")
	ep := series.episode(5, 14)
	if ep.playStremioID() != "tt0903747:5:14" {
		t.Fatalf("episode play id = %q", ep.playStremioID())
	}
	if ep.series() != series {
		t.Fatalf("episode.series() lost the series identity")
	}
	src := ep.source(3)
	decoded, err := decodeItemID(src.encode())
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Kind != kindSource || decoded.Slot != 3 || decoded.playable() != ep {
		t.Fatalf("source round trip: %+v", decoded)
	}
	// Long-running anime: episode numbers past 255 survive.
	anime, _ := itemIDFor("anime", "kitsu:12")
	far := anime.episode(1, 1071)
	if got, _ := decodeItemID(far.source(2).encode()); got.playable() != far || got.playable().playStremioID() != "kitsu:12:1071" {
		t.Fatalf("anime episode 1071 did not survive: %+v", got)
	}
	// A Kitsu movie is an episode with no number, and plays as the entry.
	if anime.episode(1, 0).playStremioID() != "kitsu:12" {
		t.Fatalf("kitsu movie play id = %q", anime.episode(1, 0).playStremioID())
	}
	movie, _ := itemIDFor("movie", "tt0111161")
	if movie.source(1).playable() != movie {
		t.Fatalf("movie source did not resolve to the movie")
	}
}

func TestItemIDRejectsForeignGUIDs(t *testing.T) {
	for _, bad := range []string{
		"",
		"not-a-guid",
		"00000000000000000000000000000000",
		"f1d2d2f924e986ac86fdf7b36c94bcdf", // random hex
		"0200000000000000000000000000005a", // movie kind, no content type, bad sum
		strings.Repeat("ab", 15),           // too short
	} {
		if _, err := decodeItemID(bad); err == nil {
			t.Fatalf("decodeItemID(%q) accepted a foreign id", bad)
		}
	}
	// A flipped byte in a valid id fails the checksum instead of decoding
	// into a neighbouring item.
	id, _ := itemIDFor("movie", "tt0111161")
	encoded := id.encode()
	flipped := encoded[:20] + "ff" + encoded[22:]
	if _, err := decodeItemID(flipped); err == nil {
		t.Fatalf("corrupted id %q decoded", flipped)
	}
	if _, err := itemIDFor("movie", "local:abc"); err == nil {
		t.Fatalf("unsupported scheme encoded")
	}
}

func TestViewIDResolvesCatalog(t *testing.T) {
	id, err := decodeItemID(viewID("tmdb.trending.movie"))
	if err != nil || id.Kind != kindView || id.CatalogID != "tmdb.trending.movie" {
		t.Fatalf("view id did not resolve: %+v %v", id, err)
	}
	// A profile-owned external catalog is not in the static registry. Its
	// opaque view id is validated against the requesting stream's enabled
	// catalog definitions by the route layer before it can be opened.
	unknown := viewID("external.profile.catalog")
	id, err = decodeItemID(unknown)
	if err != nil || id.Kind != kindView || id.CatalogID != unknown {
		t.Fatalf("external view candidate did not survive decoding: %+v %v", id, err)
	}
}
