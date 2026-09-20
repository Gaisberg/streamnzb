package stremio

import (
	"os"
	"testing"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/services/metadata/animelists"
	"streamnzb/pkg/services/metadata/mdblist"
	"streamnzb/pkg/services/metadata/scrobble"
	"streamnzb/pkg/session"
)

func TestScrobbleItemForSession(t *testing.T) {
	logger.Init("ERROR")
	srv := &Server{}

	// Movies address by imdb/tmdb id.
	movie := &session.Session{
		ContentType:  "movie",
		ContentTitle: "Inception",
		ContentIDs:   &session.AvailReportMeta{ImdbID: "tt1375666", TmdbID: "27205"},
	}
	item, ok := srv.scrobbleItemForSession(movie)
	if !ok || item.ContentType != "movie" || item.IMDbID != "tt1375666" || item.TMDBID != "27205" || item.Title != "Inception" {
		t.Fatalf("movie item = %+v, ok=%v", item, ok)
	}

	// A movie with no ids at all cannot be addressed.
	if _, ok := srv.scrobbleItemForSession(&session.Session{ContentType: "movie", ContentIDs: &session.AvailReportMeta{}}); ok {
		t.Fatal("id-less movie mapped")
	}

	// Series need a show id plus season/episode.
	series := &session.Session{
		ContentType: "series",
		ContentIDs:  &session.AvailReportMeta{ImdbID: "tt4574334", TvdbID: "305288", Season: 1, Episode: 3},
	}
	item, ok = srv.scrobbleItemForSession(series)
	if !ok || item.ContentType != "series" || item.Season != 1 || item.Episode != 3 || item.TVDBID != "305288" {
		t.Fatalf("series item = %+v, ok=%v", item, ok)
	}
	noEpisode := &session.Session{ContentType: "series", ContentIDs: &session.AvailReportMeta{ImdbID: "tt4574334"}}
	if _, ok := srv.scrobbleItemForSession(noEpisode); ok {
		t.Fatal("series without an episode mapped")
	}

	// Anime with neither a MAL mapping nor aired ids can be placed nowhere.
	anime := &session.Session{
		ContentType: "anime",
		ContentID:   "kitsu:486:5",
		ContentIDs:  &session.AvailReportMeta{KitsuID: "486", Season: 3, Episode: 17},
	}
	if _, ok := srv.scrobbleItemForSession(anime); ok {
		t.Fatal("anime mapped with no anime-lists store")
	}

	// The same anime with the aired ids resolved is addressable even without a
	// MAL mapping — that is the addressing MDBList uses.
	anime.ContentIDs.TvdbID = "305288"
	item, ok = srv.scrobbleItemForSession(anime)
	if !ok || item.MALID != "" || item.TVDBID != "305288" || item.Season != 3 || item.Episode != 17 {
		t.Fatalf("aired-only anime item = %+v, ok=%v", item, ok)
	}
	if !(&mdblist.Client{}).Supports(item) {
		t.Fatal("MDBList cannot place an anime episode with aired ids")
	}

	// Direct plays carry no request context at all.
	if _, ok := srv.scrobbleItemForSession(&session.Session{ContentType: "movie"}); ok {
		t.Fatal("session without ContentIDs mapped")
	}
}

// The anime path must send the entry-local (Kitsu/MAL) episode number, not the
// aired-series numbers anime-lists remapped into the report meta.
func TestScrobbleItemForSessionAnimeUsesEntryEpisode(t *testing.T) {
	logger.Init("ERROR")
	dir, err := os.MkdirTemp("", "scrobble_anime_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sm, err := persistence.GetManager(dir)
	if err != nil {
		t.Fatalf("state manager: %v", err)
	}
	if err := sm.AnimeMappingStore().Replace([]persistence.AnimeMapping{
		{KitsuID: 486, MALID: 999, Season: 3, HasSeason: true, EpisodeOffset: 12},
	}, time.Now()); err != nil {
		t.Fatalf("seed mappings: %v", err)
	}
	srv := &Server{animeLists: animelists.NewStore(sm.AnimeMappingStore())}

	anime := &session.Session{
		ContentType: "anime",
		ContentID:   "kitsu:486:5",
		// Season/Episode already remapped to aired numbering — must NOT be
		// what goes to Simkl.
		ContentIDs: &session.AvailReportMeta{KitsuID: "486", Season: 3, Episode: 17},
	}
	item, ok := srv.scrobbleItemForSession(anime)
	if !ok || item.ContentType != "anime" || item.MALID != "999" || item.MALEpisode != 5 {
		t.Fatalf("anime item = %+v, ok=%v; want MAL 999 entry episode 5", item, ok)
	}
	// The aired numbering rides along untouched for the targets that address
	// anime as its aired series.
	if item.Season != 3 || item.Episode != 17 {
		t.Fatalf("anime item lost the aired numbering: %+v", item)
	}
}

// Scrobbling is opt-in per stream and per service, and a service with no
// linked account is not a target at all — nothing goes out until both halves
// are in place.
func TestScrobbleTargets(t *testing.T) {
	rt := serverRuntime{config: &config.Config{}}
	// No stream at all: an unauthenticated play reports nowhere.
	if targets := scrobbleTargets(rt, nil); len(targets) != 0 {
		t.Fatalf("no stream, but %d targets", len(targets))
	}
	// A stream that has not opted in reports nowhere either.
	if targets := scrobbleTargets(rt, &auth.Stream{Username: "alice"}); len(targets) != 0 {
		t.Fatalf("stream with scrobbling off, but %d targets", len(targets))
	}
	// Opted in, but nothing is linked yet.
	opted := &auth.Stream{Username: "alice", SimklScrobble: true, MDBListScrobble: true}
	if targets := scrobbleTargets(rt, opted); len(targets) != 0 {
		t.Fatalf("toggles on with no accounts, but %d targets", len(targets))
	}
	// A client id on its own is not an account: only a linked one is a target,
	// and the account has to be this stream's own.
	rt.mdblistClients = mdblist.NewRegistry("client-id", t.TempDir())
	if targets := scrobbleTargets(rt, opted); len(targets) != 0 {
		t.Fatalf("client id but no linked account, yet %d targets", len(targets))
	}
}

// Item.Addressable is what decides whether a session is worth reporting at
// all; each target then narrows that to what it can actually place.
func TestItemAddressable(t *testing.T) {
	cases := []struct {
		name string
		item scrobble.Item
		want bool
	}{
		{"movie with an id", scrobble.Item{ContentType: "movie", IMDbID: "tt1"}, true},
		{"movie without ids", scrobble.Item{ContentType: "movie"}, false},
		{"series with episode", scrobble.Item{ContentType: "series", TVDBID: "1", Season: 1, Episode: 2}, true},
		{"series without episode", scrobble.Item{ContentType: "series", TVDBID: "1", Season: 1}, false},
		{"anime by MAL only", scrobble.Item{ContentType: "anime", MALID: "9"}, true},
		{"anime film by imdb", scrobble.Item{ContentType: "anime", AnimeMovie: true, IMDbID: "tt1"}, true},
		{"anime with neither", scrobble.Item{ContentType: "anime", Season: 1, Episode: 2}, false},
	}
	for _, tc := range cases {
		if got := tc.item.Addressable(); got != tc.want {
			t.Errorf("%s: Addressable() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestServedProgressBookkeeping(t *testing.T) {
	sess := &session.Session{}
	if pct := sess.ServedProgressPercent(); pct != 0 {
		t.Fatalf("fresh session progress = %v", pct)
	}
	// A player resuming at a range start is credited that position outright;
	// how far past it the serve buffered is bounded by how long it ran.
	sess.NoteServedWindow(session.ServeWindow{StartOffset: 440, MaxOffset: 900, TotalSize: 1000})
	sess.NoteServedWindow(session.ServeWindow{StartOffset: 200, MaxOffset: 300, TotalSize: 1000}) // an earlier offset never lowers the mark
	if pct := sess.ServedProgressPercent(); pct != 44 {
		t.Fatalf("progress = %v, want 44", pct)
	}
	sess.NoteServedWindow(session.ServeWindow{StartOffset: 2000, MaxOffset: 2000, TotalSize: 1000}) // clamped
	if pct := sess.ServedProgressPercent(); pct != 100 {
		t.Fatalf("clamped progress = %v, want 100", pct)
	}
	sess.SetLastReportedProgress(44.5)
	if got := sess.LastReportedProgress(); got != 44.5 {
		t.Fatalf("reported progress = %v, want 44.5", got)
	}
}

// A client that opens "bytes=0-" and buffers the whole file in milliseconds
// reached the end of the transfer, not the end of the film: crediting the
// furthest byte delivered used to report it as 100% watched (#341).
func TestServedProgressIgnoresFastBufferedTransfer(t *testing.T) {
	const size = 8 << 20
	sess := &session.Session{}
	sess.NoteServedWindow(session.ServeWindow{StartOffset: 0, MaxOffset: size, TotalSize: size, Elapsed: 5 * time.Millisecond})
	if pct := sess.ServedProgressPercent(); pct >= 1 {
		t.Fatalf("progress after a 5ms full-file transfer = %v, want under 1", pct)
	}

	// The same bytes with the wall clock a real viewing would take do count.
	sess.NoteServedWindow(session.ServeWindow{StartOffset: 0, MaxOffset: size, TotalSize: size, Elapsed: 90 * time.Minute})
	if pct := sess.ServedProgressPercent(); pct != 100 {
		t.Fatalf("progress after a 90m full-file serve = %v, want 100", pct)
	}
}
