package persistence

import (
	"strconv"
	"testing"
	"time"
)

func animeMappingFixture() []AnimeMapping {
	return []AnimeMapping{
		// Fire Force Season 3 Part 2: a cour that opens on episode 13.
		{KitsuID: 49016, IMDbID: "tt9307686", TVDBID: "355480", TMDBID: "88046",
			Season: 3, HasSeason: true, EpisodeOffset: 12, EntryType: "TV"},
		// A season with no cour offset.
		{KitsuID: 50154, IMDbID: "tt32991344", TVDBID: "451793", TMDBID: "258348",
			Season: 2, HasSeason: true, EntryType: "TV"},
		// An entry spanning the whole series: no season published at all.
		{KitsuID: 486, IMDbID: "tt0168366", TVDBID: "76703", TMDBID: "60572", EntryType: "TV"},
		// Specials: season 0 is published, which is not the same as absent.
		{KitsuID: 1234, TVDBID: "76703", Season: 0, HasSeason: true, EntryType: "SPECIAL"},
	}
}

func TestAnimeMappingStoreReplaceAndLookup(t *testing.T) {
	m := suiteDB(t).open(t)
	defer m.Close()
	store := m.AnimeMappingStore()

	if store.Count() != 0 {
		t.Fatalf("Count() = %d on a fresh database, want 0", store.Count())
	}
	if !store.LastUpdated().IsZero() {
		t.Fatalf("LastUpdated() = %v on a fresh database, want zero", store.LastUpdated())
	}

	imported := time.Now().Truncate(time.Millisecond)
	if err := store.Replace(animeMappingFixture(), imported); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if got := store.Count(); got != 4 {
		t.Fatalf("Count() = %d, want 4", got)
	}
	if got := store.LastUpdated(); !got.Equal(imported) {
		t.Fatalf("LastUpdated() = %v, want %v", got, imported)
	}

	cour, ok := store.LookupKitsu(49016)
	if !ok {
		t.Fatal("kitsu 49016 did not resolve")
	}
	if cour.IMDbID != "tt9307686" || cour.TVDBID != "355480" || cour.TMDBID != "88046" {
		t.Fatalf("ids = %q/%q/%q", cour.IMDbID, cour.TVDBID, cour.TMDBID)
	}
	if !cour.HasSeason || cour.Season != 3 || cour.EpisodeOffset != 12 {
		t.Fatalf("season = %d (has=%v) offset = %d", cour.Season, cour.HasSeason, cour.EpisodeOffset)
	}

	// An absent season must not come back looking like season 0, or a
	// series-spanning entry would be mistaken for specials.
	spanning, ok := store.LookupKitsu(486)
	if !ok || spanning.HasSeason {
		t.Fatalf("kitsu 486 has_season = %v (ok=%v), want false", spanning.HasSeason, ok)
	}
	specials, ok := store.LookupKitsu(1234)
	if !ok || !specials.HasSeason || specials.Season != 0 {
		t.Fatalf("kitsu 1234 season = %d (has=%v), want 0/true", specials.Season, specials.HasSeason)
	}
	// Empty ids round-trip as empty strings, not as a scan failure.
	if specials.IMDbID != "" || specials.TMDBID != "" {
		t.Fatalf("expected empty ids, got %q/%q", specials.IMDbID, specials.TMDBID)
	}

	if _, ok := store.LookupKitsu(999999); ok {
		t.Fatal("expected an unknown id to miss")
	}
	if _, ok := store.LookupKitsu(0); ok {
		t.Fatal("expected a zero id to miss")
	}
}

// The list is a snapshot: entries are retired upstream as well as added, so a
// replace must not leave the previous import behind.
func TestAnimeMappingStoreReplaceIsWholesale(t *testing.T) {
	m := suiteDB(t).open(t)
	defer m.Close()
	store := m.AnimeMappingStore()

	if err := store.Replace(animeMappingFixture(), time.Now()); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	next := []AnimeMapping{{KitsuID: 49016, IMDbID: "tt9307686", Season: 4, HasSeason: true}}
	if err := store.Replace(next, time.Now()); err != nil {
		t.Fatalf("Replace (second): %v", err)
	}

	if got := store.Count(); got != 1 {
		t.Fatalf("Count() = %d after replace, want 1", got)
	}
	if _, ok := store.LookupKitsu(486); ok {
		t.Fatal("a retired entry survived the replace")
	}
	updated, ok := store.LookupKitsu(49016)
	if !ok || updated.Season != 4 {
		t.Fatalf("season = %d (ok=%v), want 4", updated.Season, ok)
	}
}

// Mappings must outlive a restart, or every start would re-download the list.
func TestAnimeMappingStoreSurvivesReopen(t *testing.T) {
	db := suiteDB(t)
	m := db.open(t)
	imported := time.Now().Truncate(time.Millisecond)
	if err := m.AnimeMappingStore().Replace(animeMappingFixture(), imported); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	m.Close()

	reopened := db.open(t)
	defer reopened.Close()
	store := reopened.AnimeMappingStore()
	if got := store.Count(); got != 4 {
		t.Fatalf("Count() = %d after reopen, want 4", got)
	}
	if got := store.LastUpdated(); !got.Equal(imported) {
		t.Fatalf("LastUpdated() = %v after reopen, want %v", got, imported)
	}
}

// The real list is ~22k rows, written in chunks. Cross several chunk
// boundaries, including a final partial one, so an off-by-one in the batching
// cannot silently drop rows.
func TestAnimeMappingStoreReplaceSpansChunks(t *testing.T) {
	m := suiteDB(t).open(t)
	defer m.Close()
	store := m.AnimeMappingStore()

	const count = animeMappingInsertChunk*2 + 7
	rows := make([]AnimeMapping, 0, count)
	for i := 1; i <= count; i++ {
		rows = append(rows, AnimeMapping{
			KitsuID: i,
			IMDbID:  "tt" + strconv.Itoa(1000000+i),
			Season:  i % 5,
			// Alternate so the boolean column is exercised in both states.
			HasSeason:     i%2 == 0,
			EpisodeOffset: i % 13,
		})
	}
	if err := store.Replace(rows, time.Now()); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if got := store.Count(); got != count {
		t.Fatalf("Count() = %d, want %d", got, count)
	}
	// Spot-check the first row, the row after a chunk boundary, and the last.
	for _, id := range []int{1, animeMappingInsertChunk + 1, count} {
		got, ok := store.LookupKitsu(id)
		if !ok {
			t.Fatalf("kitsu %d missing", id)
		}
		if got.IMDbID != "tt"+strconv.Itoa(1000000+id) || got.Season != id%5 ||
			got.HasSeason != (id%2 == 0) || got.EpisodeOffset != id%13 {
			t.Fatalf("kitsu %d round-tripped as %+v", id, got)
		}
	}
}

func TestAnimeMappingStoreNilSafe(t *testing.T) {
	var store *AnimeMappingStore
	if _, ok := store.LookupKitsu(49016); ok {
		t.Fatal("nil store must miss")
	}
	if store.Count() != 0 || !store.LastUpdated().IsZero() {
		t.Fatal("nil store must report empty")
	}
	if err := store.Replace(animeMappingFixture(), time.Now()); err != nil {
		t.Fatalf("nil store Replace: %v", err)
	}
}
