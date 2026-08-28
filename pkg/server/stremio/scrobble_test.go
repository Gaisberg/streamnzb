package stremio

import (
	"os"
	"testing"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/services/metadata/animelists"
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

	// Anime without a MAL mapping available is skipped rather than misfiled.
	anime := &session.Session{
		ContentType: "anime",
		ContentID:   "kitsu:486:5",
		ContentIDs:  &session.AvailReportMeta{KitsuID: "486", Season: 3, Episode: 17},
	}
	if _, ok := srv.scrobbleItemForSession(anime); ok {
		t.Fatal("anime mapped with no anime-lists store")
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
	if !ok || item.ContentType != "anime" || item.MALID != "999" || item.Episode != 5 {
		t.Fatalf("anime item = %+v, ok=%v; want MAL 999 episode 5", item, ok)
	}
}

func TestServedProgressBookkeeping(t *testing.T) {
	sess := &session.Session{}
	if pct := sess.ServedProgressPercent(); pct != 0 {
		t.Fatalf("fresh session progress = %v", pct)
	}
	sess.NoteServedWindow(440, 1000)
	sess.NoteServedWindow(200, 1000) // an earlier offset never lowers the mark
	if pct := sess.ServedProgressPercent(); pct != 44 {
		t.Fatalf("progress = %v, want 44", pct)
	}
	sess.NoteServedWindow(2000, 1000) // clamped
	if pct := sess.ServedProgressPercent(); pct != 100 {
		t.Fatalf("clamped progress = %v, want 100", pct)
	}
	sess.SetLastReportedProgress(44.5)
	if got := sess.LastReportedProgress(); got != 44.5 {
		t.Fatalf("reported progress = %v, want 44.5", got)
	}
}
