package animelists

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"streamnzb/pkg/core/persistence"
)

// Entries reduced from the real anime-lists document, keeping the shapes that
// vary: imdb as a list, themoviedb keyed by media kind, and season /
// episode_offset present, absent, or zero.
const sampleList = `[
	{
		"type": "TV",
		"kitsu_id": 49016,
		"imdb_id": ["tt9307686"],
		"tvdb_id": 355480,
		"themoviedb_id": {"tv": 88046},
		"season": {"tvdb": 3, "tmdb": 3},
		"episode_offset": {"tvdb": 12, "tmdb": 12}
	},
	{
		"type": "TV",
		"kitsu_id": 50154,
		"imdb_id": ["tt32991344"],
		"tvdb_id": 451793,
		"themoviedb_id": {"tv": 258348},
		"season": {"tvdb": 2, "tmdb": 2}
	},
	{
		"type": "TV",
		"kitsu_id": 486,
		"imdb_id": ["tt0168366"],
		"tvdb_id": 76703,
		"themoviedb_id": {"tv": 60572}
	},
	{
		"type": "SPECIAL",
		"kitsu_id": 1234,
		"tvdb_id": 76703,
		"season": {"tvdb": 0}
	},
	{
		"type": "MOVIE",
		"kitsu_id": 4321,
		"imdb_id": ["tt0190641"],
		"themoviedb_id": {"movie": [10238]}
	},
	{
		"type": "TV",
		"anilist_id": 1,
		"mal_id": 1
	}
]`

// testMappings is the package's database. persistence.GetManager is a
// process-wide singleton, so the suite opens one database in TestMain and each
// test empties it rather than trying to get its own.
var testMappings *persistence.AnimeMappingStore

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "animelists")
	if err != nil {
		panic(err)
	}
	mgr, err := persistence.GetManager(dir)
	if err != nil {
		panic(err)
	}
	testMappings = mgr.AnimeMappingStore()

	code := m.Run()

	mgr.Close()
	os.RemoveAll(dir)
	os.Exit(code)
}

// mappingStore hands back the package database, emptied. A zero timestamp reads
// as indefinitely stale, which is the state a first run starts from.
func mappingStore(t *testing.T) *persistence.AnimeMappingStore {
	t.Helper()
	if err := testMappings.Replace(nil, time.Time{}); err != nil {
		t.Fatalf("reset mappings: %v", err)
	}
	return testMappings
}

func loadedStore(t *testing.T) *Store {
	t.Helper()
	s := NewStore(mappingStore(t))
	if err := s.Load(strings.NewReader(sampleList)); err != nil {
		t.Fatalf("load: %v", err)
	}
	return s
}

func TestLoadProjectsEntries(t *testing.T) {
	s := loadedStore(t)

	// The entry without a kitsu id is unreachable and must not be imported.
	if got := s.mappings.Count(); got != 5 {
		t.Fatalf("imported %d entries, want 5", got)
	}
	if !s.Ready() {
		t.Fatal("expected the store to be ready after a successful load")
	}

	m, ok := s.LookupKitsu("49016")
	if !ok {
		t.Fatal("expected kitsu 49016 to resolve")
	}
	if m.IMDbID != "tt9307686" || m.TVDBID != "355480" || m.TMDBID != "88046" {
		t.Fatalf("ids = %q/%q/%q", m.IMDbID, m.TVDBID, m.TMDBID)
	}
	if !m.HasSeason || m.Season != 3 || m.EpisodeOffset != 12 {
		t.Fatalf("season = %d (has=%v) offset = %d", m.Season, m.HasSeason, m.EpisodeOffset)
	}

	movie, ok := s.LookupKitsu("4321")
	if !ok || movie.TMDBID != "10238" {
		t.Fatalf("movie tmdb id = %q (ok=%v), want 10238", movie.TMDBID, ok)
	}
}

func TestLookupKitsuRejectsBadInput(t *testing.T) {
	s := loadedStore(t)
	for _, id := range []string{"", "  ", "abc", "0", "-5", "99999999"} {
		if _, ok := s.LookupKitsu(id); ok {
			t.Fatalf("expected %q to miss", id)
		}
	}
	// A store with nowhere to read from misses rather than panicking.
	if _, ok := NewStore(nil).LookupKitsu("49016"); ok {
		t.Fatal("expected a store without a database to miss")
	}
	if NewStore(nil).Ready() {
		t.Fatal("a store without a database is not ready")
	}
}

func TestResolveEpisode(t *testing.T) {
	s := loadedStore(t)

	cases := []struct {
		name        string
		kitsuID     string
		entry       int
		wantSeason  int
		wantEpisode int
		wantOK      bool
		wantSpans   bool
	}{
		// The reported case: Fire Force Season 3 Part 2 episode 3 is S03E15.
		{"cour offset applies", "49016", 3, 3, 15, true, false},
		{"season without offset", "50154", 3, 2, 3, true, false},
		{"series-spanning entry is absolute", "486", 154, 0, 0, false, true},
		{"specials have no aired season", "1234", 1, 0, 0, false, false},
		{"episode zero is not resolvable", "49016", 0, 0, 0, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := s.LookupKitsu(tc.kitsuID)
			if !ok {
				t.Fatalf("kitsu %s did not resolve", tc.kitsuID)
			}
			season, episode, ok := m.ResolveEpisode(tc.entry)
			if ok != tc.wantOK || season != tc.wantSeason || episode != tc.wantEpisode {
				t.Fatalf("ResolveEpisode(%d) = %d/%d (ok=%v), want %d/%d (ok=%v)",
					tc.entry, season, episode, ok, tc.wantSeason, tc.wantEpisode, tc.wantOK)
			}
			if got := m.SpansSeries(); got != tc.wantSpans {
				t.Fatalf("SpansSeries() = %v, want %v", got, tc.wantSpans)
			}
		})
	}

	var nilMapping *Mapping
	if _, _, ok := nilMapping.ResolveEpisode(3); ok {
		t.Fatal("nil mapping must not resolve")
	}
	if nilMapping.SpansSeries() {
		t.Fatal("nil mapping does not span a series")
	}
}

func TestLoadRejectsUnusableDocuments(t *testing.T) {
	s := NewStore(mappingStore(t))
	for _, doc := range []string{`{"kitsu_id": 1}`, `[]`, `[{"mal_id": 1}]`, `not json`} {
		if err := s.Load(strings.NewReader(doc)); err == nil {
			t.Fatalf("expected %q to be rejected", doc)
		}
	}
	if s.Ready() {
		t.Fatal("a rejected document must not be imported")
	}
}

func TestRefreshImportsThenSkipsWhileCurrent(t *testing.T) {
	hits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte(sampleList))
	}))
	defer ts.Close()

	s := NewStore(mappingStore(t))
	s.url = ts.URL
	s.Refresh()

	if !s.Ready() {
		t.Fatal("expected the refresh to import the list")
	}
	if hits != 1 {
		t.Fatalf("fetches = %d, want 1", hits)
	}

	// A current import is left alone — a restart costs no download.
	s.Refresh()
	if hits != 1 {
		t.Fatalf("fetches = %d after a second refresh, want 1", hits)
	}

	// Once it ages out, the next refresh re-imports.
	if err := s.mappings.Replace(nil, time.Now().Add(-2*refreshInterval)); err != nil {
		t.Fatalf("age out: %v", err)
	}
	s.Refresh()
	if hits != 2 {
		t.Fatalf("fetches = %d after ageing out, want 2", hits)
	}
	if !s.Ready() {
		t.Fatal("expected the aged-out list to be re-imported")
	}
}

// A failed download must leave the previous import in place: mappings from
// yesterday resolve everything that is not brand new.
func TestRefreshKeepsPreviousImportOnFailure(t *testing.T) {
	store := mappingStore(t)
	seed := NewStore(store)
	if err := seed.Load(strings.NewReader(sampleList)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.Replace(mappingsOf(t, store), time.Now().Add(-2*refreshInterval)); err != nil {
		t.Fatalf("age out: %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer ts.Close()

	s := NewStore(store)
	s.url = ts.URL
	s.Refresh()

	if !s.Ready() {
		t.Fatal("a failed refresh must not clear the previous import")
	}
	if _, ok := s.LookupKitsu("49016"); !ok {
		t.Fatal("the previous import no longer resolves a known id")
	}
}

// mappingsOf re-reads the sample rows so a test can rewrite them with a
// different timestamp without losing the data.
func mappingsOf(t *testing.T, store *persistence.AnimeMappingStore) []persistence.AnimeMapping {
	t.Helper()
	var out []persistence.AnimeMapping
	for _, id := range []int{49016, 50154, 486, 1234, 4321} {
		m, ok := store.LookupKitsu(id)
		if !ok {
			t.Fatalf("kitsu %d missing from the seed", id)
		}
		out = append(out, *m)
	}
	return out
}
