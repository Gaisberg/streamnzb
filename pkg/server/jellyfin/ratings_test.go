package jellyfin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/server/stremio"
)

// likesIn reads the Likes of a UserData answer: whether the property is there
// at all, and its value.
func likesIn(t *testing.T, rec *httptest.ResponseRecorder) (present, value bool) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	v, present := raw["Likes"]
	if !present {
		return false, false
	}
	b, isBool := v.(bool)
	if !isBool {
		t.Fatalf("Likes is %T (%v), want a JSON boolean: %s", v, v, rec.Body.String())
	}
	return true, b
}

// wantLikes asserts the answer's rating: nil is unrated, so the property is
// omitted rather than written as false.
func wantLikes(t *testing.T, rec *httptest.ResponseRecorder, want *bool) {
	t.Helper()
	present, value := likesIn(t, rec)
	if present != (want != nil) || (want != nil && value != *want) {
		t.Fatalf("Likes present=%v value=%v, want %v: %s", present, value, want, rec.Body.String())
	}
}

func (f *fixture) rate(method string, id itemID, query string, headers ...string) *httptest.ResponseRecorder {
	return f.do(method, "/jellyfin/UserItems/"+id.encode()+"/Rating"+query, "", headers...)
}

func storedLikes(f *fixture, stream string, id itemID) sql.NullBool {
	st, _ := f.play.Get(stream, id.encode())
	return st.Likes
}

func yes() *bool { return boolPtr(true) }
func no() *bool  { return boolPtr(false) }

// TestRatingSetChangeClear: a like, a dislike, a change and a clear, on both
// routes, for a movie and an episode, each answered with the UserData and
// read back from the UserData route and the item itself.
func TestRatingSetChangeClear(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	series, _ := itemIDFor("series", "tt0903747")
	for _, id := range []itemID{movie, series.episode(1, 1)} {
		for _, legacy := range []bool{false, true} {
			do := func(method, query string) *httptest.ResponseRecorder {
				if legacy {
					return f.do(method, "/jellyfin/Users/u-1/Items/"+id.encode()+"/Rating"+query, "")
				}
				return f.rate(method, id, query)
			}
			wantLikes(t, do(http.MethodPost, "?likes=true"), yes())
			var ud userData
			decodeInto(t, f.do(http.MethodGet, "/jellyfin/UserItems/"+id.encode()+"/UserData", ""), &ud)
			if ud.Likes == nil || !*ud.Likes {
				t.Fatalf("%s legacy=%v: UserData after like: %+v", id.encode(), legacy, ud)
			}
			var item baseItem
			decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+id.encode(), ""), &item)
			if item.UserData == nil || item.UserData.Likes == nil || !*item.UserData.Likes {
				t.Fatalf("%s legacy=%v: item after like: %+v", id.encode(), legacy, item.UserData)
			}
			wantLikes(t, do(http.MethodPost, "?likes=false"), no())
			if got := storedLikes(f, "living-room", id); !got.Valid || got.Bool {
				t.Fatalf("stored after dislike: %+v", got)
			}
			wantLikes(t, do(http.MethodPost, "?likes=true"), yes())
			wantLikes(t, do(http.MethodDelete, ""), nil)
			if got := storedLikes(f, "living-room", id); got.Valid {
				t.Fatalf("stored after delete: %+v", got)
			}
			// A missing, empty or blank likes clears, as Jellyfin's nullable does.
			for _, clear := range []string{"", "?likes=", "?likes=%20%20", "?userId=u-1"} {
				wantLikes(t, do(http.MethodPost, "?likes=false"), no())
				wantLikes(t, do(http.MethodPost, clear), nil)
			}
			// Names and values are case-insensitive and trimmed, and the user
			// id is ignored.
			wantLikes(t, do(http.MethodPost, "?USERID=someone-else&Likes=TRUE"), yes())
			wantLikes(t, do(http.MethodPost, "?likes=%20False%20"), no())
			wantLikes(t, do(http.MethodDelete, "?userId=someone-else"), nil)
		}
	}
	// The unrated state is an omitted property, not a null or a false.
	if body := f.rate(http.MethodDelete, movie, "").Body.String(); strings.Contains(body, "Likes") {
		t.Fatalf("an unrated item carries Likes: %s", body)
	}
}

// TestRatingRefusesWhatItCannotRead: any other value is a 400 and writes
// nothing; the routes take POST and DELETE only; and a request that names no
// item, or one that cannot be rated, or no login, changes nothing.
func TestRatingRefusesWhatItCannotRead(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	f.rate(http.MethodPost, movie, "?likes=true")
	before := maps.Clone(f.play.rows)
	writes := f.play.likesWrites

	for _, value := range []string{"1", "0", "yes", "no", "banana", "true,false", "tru", "null"} {
		if rec := f.rate(http.MethodPost, movie, "?likes="+value); rec.Code != http.StatusBadRequest {
			t.Errorf("likes=%s: %d, want 400", value, rec.Code)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch} {
		if rec := f.rate(method, movie, "?likes=false"); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", method, rec.Code)
		}
	}
	for _, raw := range []string{"not-an-id", "00000000000000000000000000000000", viewID("tmdb.trending.movie"), genreID("movie", "Action")} {
		if rec := f.do(http.MethodPost, "/jellyfin/UserItems/"+raw+"/Rating?likes=true", ""); rec.Code != http.StatusNotFound {
			t.Errorf("item %s: %d, want 404", raw, rec.Code)
		}
	}
	if rec := f.rate(http.MethodPost, movie, "?likes=false", "X-None", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("without a login: %d, want 401", rec.Code)
	}
	if f.play.likesWrites != writes || !maps.Equal(f.play.rows, before) {
		t.Fatalf("a refused request wrote: %d writes, rows %+v", f.play.likesWrites-writes, f.play.rows)
	}
}

// TestRatingMalformedQueryNeverClears: a query that cannot be read, or that
// names likes twice, is a 400 that leaves an existing rating as it was. The
// shared parser drops what it cannot read, so without this a damaged likes
// reads as an absent one, which clears.
func TestRatingMalformedQueryNeverClears(t *testing.T) {
	f, show := folderFixture(t)
	movie, _ := itemIDFor("movie", "tt0111161")
	queries := []string{
		"?likes=%ZZ", "?likes=%", "?likes=%G1", "?likes=true&likes=false", "?likes=true&Likes=banana",
		"?likes=true&Likes=true", "?likes=false&LIKES=false", "?likes=&likes=", "?likes=true;likes=false",
		"?userId=%ZZ&likes=false", "?likes=true&%ZZ=1",
	}
	for _, id := range []itemID{movie, show.episode(1, 1), show, show.season(1)} {
		for _, legacy := range []bool{false, true} {
			path := "/jellyfin/UserItems/" + id.encode() + "/Rating"
			if legacy {
				path = "/jellyfin/Users/u-1/Items/" + id.encode() + "/Rating"
			}
			for _, query := range queries {
				wantLikes(t, f.do(http.MethodPost, path+"?likes=true", ""), yes())
				writes := f.play.likesWrites
				if rec := f.do(http.MethodPost, path+query, ""); rec.Code != http.StatusBadRequest {
					t.Errorf("%s POST %s: %d, want 400", id.encode(), query, rec.Code)
				}
				if got := storedLikes(f, "living-room", id); !got.Valid || !got.Bool || f.play.likesWrites != writes {
					t.Errorf("%s POST %s changed the rating: %+v after %d writes", id.encode(), query, got, f.play.likesWrites-writes)
				}
			}
			// DELETE reads no likes, so a damaged one does not stop it.
			if rec := f.do(http.MethodDelete, path+"?likes=%ZZ", ""); rec.Code != http.StatusOK {
				t.Errorf("%s DELETE with a damaged query: %d, want 200", id.encode(), rec.Code)
			}
			if got := storedLikes(f, "living-room", id); got.Valid {
				t.Errorf("%s DELETE left %+v", id.encode(), got)
			}
		}
	}
}

// TestRatingReadFailureIsAnError: a read of the stored row that fails is an
// error, never an unrated item. A folder's rating is decided from it, so a
// failed read must not turn a clear, or an unchanged rating, into a success
// that did nothing; and an answer read back after a write must not be an
// empty UserData made up of the failure. The write itself is not undone.
func TestRatingReadFailureIsAnError(t *testing.T) {
	f, show := folderFixture(t)
	movie, _ := itemIDFor("movie", "tt0111161")
	read := errors.New("database is locked")

	for _, folder := range []itemID{show.season(1), show} {
		f.rate(http.MethodPost, folder, "?likes=true")
		f.play.getErr = read
		before := maps.Clone(f.play.rows)
		writes := f.play.likesWrites
		for _, c := range []struct{ method, query string }{
			{http.MethodDelete, ""}, {http.MethodPost, ""}, // a clear is needed
			{http.MethodPost, "?likes=true"},  // unchanged
			{http.MethodPost, "?likes=false"}, // a change
		} {
			rec := f.rate(c.method, folder, c.query)
			if rec.Code != http.StatusInternalServerError || rec.Body.String() != "Play state read failed\n" {
				t.Errorf("%s %s %s: %d %q, want 500", c.method, folder.encode(), c.query, rec.Code, rec.Body.String())
			}
		}
		if f.play.likesWrites != writes || !maps.Equal(f.play.rows, before) {
			t.Errorf("%s: a failed read let a write through", folder.encode())
		}
		f.play.getErr = nil
		wantLikes(t, f.rate(http.MethodDelete, folder, ""), nil)
	}

	// With nothing stored, the folder is genuinely unrated: a clear is a
	// no-op answered 200 and a rating is written.
	season := show.season(2)
	writes := f.play.likesWrites
	wantLikes(t, f.rate(http.MethodDelete, season, ""), nil)
	if f.play.likesWrites != writes {
		t.Error("clearing an unrated folder wrote")
	}
	wantLikes(t, f.rate(http.MethodPost, season, "?likes=false"), no())

	// A movie or an episode is written, then read back for the answer. The
	// write stands when the read fails; the answer is an error, not a UserData.
	for _, id := range []itemID{movie, show.episode(1, 1)} {
		f.play.getErr = read
		for _, c := range []struct {
			method, query string
			stored        bool
		}{{http.MethodPost, "?likes=true", true}, {http.MethodDelete, "", false}} {
			rec := f.rate(c.method, id, c.query)
			if rec.Code != http.StatusInternalServerError || rec.Body.String() != "Play state read failed\n" {
				t.Errorf("%s %s %s: %d %q, want 500", c.method, id.encode(), c.query, rec.Code, rec.Body.String())
			}
			if got := storedLikes(f, "living-room", id); got.Valid != c.stored {
				t.Errorf("%s %s %s: stored %+v after a failed read", c.method, id.encode(), c.query, got)
			}
		}
		f.play.getErr = nil
	}
}

// TestRatingIsPerStream: the token names the stream, whatever user id the
// request carries.
func TestRatingIsPerStream(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	wantLikes(t, f.rate(http.MethodPost, movie, "?likes=true&userId=kitchen"), yes())
	decoded := f.rate(http.MethodPost, movie, "?likes=false&userId=living-room", kitchenAuth...)
	wantLikes(t, decoded, no())
	if got := storedLikes(f, "living-room", movie); !got.Valid || !got.Bool {
		t.Fatalf("living-room: %+v", got)
	}
	if got := storedLikes(f, "kitchen", movie); !got.Valid || got.Bool {
		t.Fatalf("kitchen: %+v", got)
	}
	wantLikes(t, f.rate(http.MethodDelete, movie, "", kitchenAuth...), nil)
	if got := storedLikes(f, "living-room", movie); !got.Valid || !got.Bool {
		t.Fatalf("kitchen's clear reached living-room: %+v", got)
	}
}

// TestRatingResolvesMediaSourceIDs: a media source stands for the item it
// plays.
func TestRatingResolvesMediaSourceIDs(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	series, _ := itemIDFor("series", "tt0903747")
	for _, item := range []itemID{movie, series.episode(2, 3)} {
		wantLikes(t, f.rate(http.MethodPost, item.source(1), "?likes=false"), no())
		if got := storedLikes(f, "living-room", item); !got.Valid || got.Bool {
			t.Fatalf("%s: %+v", item.encode(), got)
		}
		if _, ok := f.play.Get("living-room", item.source(1).encode()); ok {
			t.Fatal("a row was stored under the media source id")
		}
	}
}

// TestRatingWriteFailureIsAnError: a failed write answers 500, not the
// UserData, and changes nothing.
func TestRatingWriteFailureIsAnError(t *testing.T) {
	f, show := folderFixture(t)
	movie, _ := itemIDFor("movie", "tt0111161")
	f.rate(http.MethodPost, movie, "?likes=true")
	f.play.likesErr = errors.New("database is locked")
	before := maps.Clone(f.play.rows)
	for _, id := range []itemID{movie, show.episode(1, 1), show, show.season(1)} {
		for _, c := range []struct{ method, query string }{{http.MethodPost, "?likes=false"}, {http.MethodDelete, ""}} {
			rec := f.rate(c.method, id, c.query)
			// A folder with no rating has nothing to clear, so nothing to fail.
			if isFolder(id) && c.method == http.MethodDelete {
				if rec.Code != http.StatusOK {
					t.Errorf("clearing a folder with no rating: %d", rec.Code)
				}
				continue
			}
			if rec.Code != http.StatusInternalServerError || rec.Body.String() != "Rating update failed\n" {
				t.Errorf("%s %s %s: %d %q", c.method, id.encode(), c.query, rec.Code, rec.Body.String())
			}
		}
	}
	if !maps.Equal(f.play.rows, before) {
		t.Fatalf("failed writes changed play state: %+v", f.play.rows)
	}
}

// TestRatingLeavesPlaybackAndFavouritesAlone and the reverse: the rating is
// one field, written by one route.
func TestRatingLeavesPlaybackAndFavouritesAlone(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	f.do(http.MethodPost, "/jellyfin/UserFavoriteItems/"+movie.encode(), "")
	f.play.Upsert(persistence.JellyfinPlaystate{StreamName: "living-room", ItemID: movie.encode(), ContentType: "movie", ContentID: "tt0111161",
		PositionTicks: 60 * ticksPerSecond, RuntimeTicks: 600 * ticksPerSecond, PlayCount: 3})
	before, _ := f.play.Get("living-room", movie.encode())

	f.rate(http.MethodPost, movie, "?likes=false")
	after, _ := f.play.Get("living-room", movie.encode())
	after.Likes = before.Likes
	if after != before {
		t.Fatalf("a rating changed more than the rating:\n%+v\n%+v", before, after)
	}

	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/jellyfin/UserPlayedItems/" + movie.encode(), ""},
		{http.MethodDelete, "/jellyfin/UserPlayedItems/" + movie.encode(), ""},
		{http.MethodDelete, "/jellyfin/UserFavoriteItems/" + movie.encode(), ""},
		{http.MethodPost, "/jellyfin/UserFavoriteItems/" + movie.encode(), ""},
		{http.MethodPost, "/jellyfin/UserItems/" + movie.encode() + "/UserData", `{"IsFavorite":false,"PlaybackPositionTicks":90000000}`},
		{http.MethodPost, "/jellyfin/Sessions/Playing/Progress", `{"ItemId":"` + movie.encode() + `","PositionTicks":120000000}`},
		// Clients send their whole UserData back; a Likes in it is not read.
		{http.MethodPost, "/jellyfin/UserItems/" + movie.encode() + "/UserData", `{"Likes":true,"Rating":5}`},
	} {
		if rec := f.do(c.method, c.path, c.body); rec.Code >= 300 {
			t.Fatalf("%s %s: %d", c.method, c.path, rec.Code)
		}
		if got := storedLikes(f, "living-room", movie); !got.Valid || got.Bool {
			t.Fatalf("%s %s changed the rating: %+v", c.method, c.path, got)
		}
	}
	// And IsFavoriteOrLikes is still the favourites alone: a liked item that
	// is no favourite is not in it.
	f.do(http.MethodDelete, "/jellyfin/UserFavoriteItems/"+movie.encode(), "")
	f.rate(http.MethodPost, movie, "?likes=true")
	var result queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?Filters=IsFavoriteOrLikes", ""), &result)
	if len(result.Items) != 0 {
		t.Fatalf("a like listed under IsFavoriteOrLikes: %+v", result.Items)
	}
}

// TestFolderRating: a series and a season are rated as favourites are, the
// answer counted from their episodes with the rating kept, the episodes left
// as they were.
func TestFolderRating(t *testing.T) {
	f, show := folderFixture(t)
	season := show.season(1)
	f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+show.episode(1, 1).encode(), "")
	episodes := func() map[string]persistence.JellyfinPlaystate {
		rows := map[string]persistence.JellyfinPlaystate{}
		for _, st := range f.play.ListForStream("living-room") {
			if id, _ := decodeItemID(st.ItemID); id.Kind == kindEpisode {
				rows[st.ItemID] = st
			}
		}
		return rows
	}
	beforeEpisodes := episodes()
	before := folderUD(t, f, season)

	for _, id := range []itemID{season, show} {
		var ud userData
		decodeInto(t, f.rate(http.MethodPost, id, "?likes=false"), &ud)
		if ud.Likes == nil || *ud.Likes {
			t.Fatalf("%s: %+v", id.encode(), ud)
		}
		if id == season && (ud.UnplayedItemCount == nil || ud.PlayedPercentage == nil) {
			t.Fatalf("the answer is not counted: %+v", ud)
		}
	}
	if !maps.Equal(beforeEpisodes, episodes()) {
		t.Fatal("rating a folder wrote to its episodes")
	}
	after := folderUD(t, f, season)
	if after.Likes == nil || *after.Likes {
		t.Fatalf("season UserData lost its rating: %+v", after)
	}
	after.Likes = nil
	if after.UnplayedItemCount == nil || *after.UnplayedItemCount != *before.UnplayedItemCount || after.Played != before.Played {
		t.Fatalf("rating changed the counters: %+v, was %+v", after, before)
	}
	// The season lists with its rating.
	var seasons queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Shows/"+show.encode()+"/Seasons", ""), &seasons)
	found := false
	for _, item := range seasons.Items {
		if item.ID == season.encode() {
			found = item.UserData != nil && item.UserData.Likes != nil && !*item.UserData.Likes
		}
	}
	if !found {
		t.Fatalf("the season list does not carry the rating: %+v", seasons.Items)
	}

	// An unchanged rating is not written again; a change and a clear are.
	writes := f.play.likesWrites
	f.rate(http.MethodPost, season, "?likes=false")
	if f.play.likesWrites != writes {
		t.Fatal("an unchanged folder rating was written")
	}
	wantLikes(t, f.rate(http.MethodPost, season, "?likes=true"), yes())
	wantLikes(t, f.rate(http.MethodDelete, season, ""), nil)
	if f.play.likesWrites != writes+2 {
		t.Fatalf("%d writes, want 2", f.play.likesWrites-writes)
	}
}

// TestFolderRatingUnlistedSeason: a season the metadata does not list cannot
// be rated, but a rating stored for it before it was renumbered away can be
// read and cleared, as a favourite can.
func TestFolderRatingUnlistedSeason(t *testing.T) {
	f, show := folderFixture(t)
	ghost := show.season(9)
	for _, query := range []string{"?likes=true", "?likes=false", ""} {
		if rec := f.rate(http.MethodPost, ghost, query); rec.Code != http.StatusNotFound {
			t.Errorf("POST %q on an unlisted season: %d, want 404", query, rec.Code)
		}
	}
	if rec := f.rate(http.MethodDelete, ghost, ""); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE on an unlisted season with nothing stored: %d, want 404", rec.Code)
	}
	if len(f.play.rows) != 0 {
		t.Fatalf("a refused rating stored a row: %+v", f.play.rows)
	}
	f.play.SetLikes("living-room", ghost.encode(), "series", ghost.playStremioID(), yes())
	wantLikes(t, f.rate(http.MethodDelete, ghost, ""), nil)
	if got := storedLikes(f, "living-room", ghost); got.Valid {
		t.Fatalf("the stored rating was not cleared: %+v", got)
	}
}

// pausedRating starts a rating request on a folder and holds it inside its
// metadata lookup, which comes after the request has read the folder's state
// and before it writes or answers. Only that first lookup is held, so the
// requests made meanwhile pass. The returned func lets it go and gives its
// answer; nothing here waits on a clock.
func pausedRating(f *fixture, method string, id itemID, query string) (answer func() *httptest.ResponseRecorder) {
	var taken atomic.Bool
	paused, resume := make(chan struct{}), make(chan struct{})
	f.catalog.onMeta = func(context.Context) {
		if taken.CompareAndSwap(false, true) {
			close(paused)
			<-resume
		}
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.rate(method, id, query) }()
	<-paused
	return func() *httptest.ResponseRecorder {
		close(resume)
		return <-done
	}
}

// TestFolderRatingAnswersWithTheCurrentState: a favourite saved while a
// folder rating waits on the metadata is in the rating's answer, with the
// rating and the counted episodes. An answer from the state read before the
// wait would say the folder is no favourite, and a client posting its UserData
// back, as clients do with every change, would then clear the favourite.
func TestFolderRatingAnswersWithTheCurrentState(t *testing.T) {
	for _, c := range []struct {
		name, method, query string
		stored, want        *bool
	}{
		{"set", http.MethodPost, "?likes=true", nil, yes()},
		{"change", http.MethodPost, "?likes=false", yes(), no()},
		{"clear", http.MethodDelete, "", no(), nil},
		{"unchanged", http.MethodPost, "?likes=true", yes(), yes()},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, show := folderFixture(t)
			season := show.season(1)
			f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+show.episode(1, 1).encode(), "")
			if c.stored != nil {
				f.rate(http.MethodPost, season, "?likes="+map[bool]string{true: "true", false: "false"}[*c.stored])
			}
			counted := folderUD(t, f, season)
			writes := f.play.likesWrites

			answer := pausedRating(f, c.method, season, c.query)
			f.do(http.MethodPost, "/jellyfin/UserFavoriteItems/"+season.encode(), "")
			rec := answer()

			wantLikes(t, rec, c.want)
			var ud userData
			decodeInto(t, rec, &ud)
			if !ud.IsFavorite {
				t.Fatalf("the answer lost the favourite saved meanwhile: %+v", ud)
			}
			if ud.UnplayedItemCount == nil || counted.UnplayedItemCount == nil || *ud.UnplayedItemCount != *counted.UnplayedItemCount || ud.PlayedPercentage == nil {
				t.Fatalf("the answer is not counted as before: %+v, was %+v", ud, counted)
			}
			if got := f.play.likesWrites - writes; (c.name == "unchanged") != (got == 0) {
				t.Fatalf("%d rating writes", got)
			}

			// A client sends the UserData it was given back with its next change.
			body, _ := json.Marshal(ud)
			if rec := f.do(http.MethodPost, "/jellyfin/UserItems/"+season.encode()+"/UserData", string(body)); rec.Code != http.StatusOK {
				t.Fatalf("repost: %d: %s", rec.Code, rec.Body.String())
			}
			if st, _ := f.play.Get("living-room", season.encode()); !st.IsFavorite || st.Likes != storedFrom(c.want) {
				t.Fatalf("after the repost: %+v, want the favourite and the rating kept", st)
			}
		})
	}
}

func storedFrom(likes *bool) sql.NullBool {
	if likes == nil {
		return sql.NullBool{}
	}
	return sql.NullBool{Bool: *likes, Valid: true}
}

// TestFolderRatingFinalReadFailure: the read that builds the answer is
// checked. When it fails the answer is a 500, the rating written before it
// stays written, and nothing says otherwise.
func TestFolderRatingFinalReadFailure(t *testing.T) {
	for _, c := range []struct {
		name, method, query string
		stored, want        *bool
	}{
		{"set", http.MethodPost, "?likes=true", nil, yes()},
		{"clear", http.MethodDelete, "", no(), nil},
		{"unchanged", http.MethodPost, "?likes=true", yes(), yes()},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, show := folderFixture(t)
			season := show.season(1)
			if c.stored != nil {
				f.play.SetLikes("living-room", season.encode(), "series", season.playStremioID(), c.stored)
			}
			// The first read, before the lookup, succeeds; the last fails.
			f.catalog.onMeta = func(context.Context) {
				f.play.mu.Lock()
				f.play.getErr = errors.New("database is locked")
				f.play.mu.Unlock()
			}
			rec := f.rate(c.method, season, c.query)
			if rec.Code != http.StatusInternalServerError || rec.Body.String() != "Play state read failed\n" {
				t.Fatalf("%d %q, want 500", rec.Code, rec.Body.String())
			}
			if got := storedLikes(f, "living-room", season); got != storedFrom(c.want) {
				t.Fatalf("stored %+v after the failed read, want %+v", got, storedFrom(c.want))
			}
		})
	}
}

// TestFolderRatingWithoutMetadata: with the metadata switched off a folder
// is rated all the same, and answered uncounted; and a request already
// cancelled writes nothing.
func TestFolderRatingWithoutMetadata(t *testing.T) {
	f, show := folderFixture(t)
	season := show.season(1)
	f.catalog.disabled = true
	var ud userData
	decodeInto(t, f.rate(http.MethodPost, season, "?likes=true"), &ud)
	if ud.Likes == nil || !*ud.Likes || ud.UnplayedItemCount != nil {
		t.Fatalf("without metadata: %+v", ud)
	}
	f.catalog.disabled = false

	writes := f.play.likesWrites
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodDelete, "/jellyfin/UserItems/"+season.encode()+"/Rating", strings.NewReader(""))
	req.Header.Set("Authorization", `MediaBrowser Client="Swiftfin", DeviceId="dev-1", Token="`+testToken+`"`)
	f.server.Handler().ServeHTTP(httptest.NewRecorder(), req)
	if got := storedLikes(f, "living-room", season); !got.Valid || !got.Bool || f.play.likesWrites != writes {
		t.Fatalf("a cancelled request wrote: %+v", got)
	}
}

// TestRatingOnlyRowsChangeNothingElse: rows holding nothing but a rating are
// not read as progress, history, favourites or watching, in any list built
// from the stream's play state.
func TestRatingOnlyRowsChangeNothingElse(t *testing.T) {
	f, show := folderFixture(t)
	// Other is rated but not followed, and is wholly unreleased, its premiere
	// airing inside the Upcoming window: were it taken for followed, Upcoming
	// would list the premiere. It must stay out. Followed is watched up to date with
	// one still to air, so Upcoming lists it, which shows the window is live.
	other := addSeries(f, "tt0000202", "Other",
		stremio.MetaVideo{Season: 1, Episode: 1, Released: day(7)},
		stremio.MetaVideo{Season: 1, Episode: 2, Released: day(14)},
		stremio.MetaVideo{Season: 2, Episode: 1, Released: day(30)})
	followed := addSeries(f, "tt0000203", "Followed",
		stremio.MetaVideo{Season: 1, Episode: 1, Released: day(-30)},
		stremio.MetaVideo{Season: 1, Episode: 2, Released: day(7)})
	f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+followed.episode(1, 1).encode(), "")
	movie, _ := itemIDFor("movie", "tt0111161")
	started, _ := itemIDFor("movie", "tt0068646")

	f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+show.episode(1, 1).encode(), "")
	f.do(http.MethodPost, "/jellyfin/UserFavoriteItems/"+show.encode(), "")
	f.play.Upsert(persistence.JellyfinPlaystate{StreamName: "living-room", ItemID: started.encode(), ContentType: "movie", ContentID: "tt0068646",
		PositionTicks: 60 * ticksPerSecond, RuntimeTicks: 600 * ticksPerSecond, PlayCount: 1})

	lists := []string{
		"/jellyfin/UserItems/Resume",
		"/jellyfin/Shows/NextUp",
		"/jellyfin/Shows/Upcoming",
		"/jellyfin/Items?Filters=IsFavorite",
		"/jellyfin/Items?Filters=IsResumable",
		"/jellyfin/UserItems/" + show.season(1).encode() + "/UserData",
		"/jellyfin/Shows/" + show.encode() + "/Seasons",
	}
	read := func() map[string]string {
		out := map[string]string{}
		for _, path := range lists {
			rec := f.do(http.MethodGet, path, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: %d", path, rec.Code)
			}
			out[path] = rec.Body.String()
		}
		return out
	}
	beforeLists, beforeRows := read(), maps.Clone(f.play.rows)
	upcoming := "/jellyfin/Shows/Upcoming"
	if !strings.Contains(beforeLists[upcoming], followed.episode(1, 2).encode()) {
		t.Fatalf("the followed series is not upcoming, so the window proves nothing: %s", beforeLists[upcoming])
	}

	for _, id := range []itemID{movie, other, other.season(1), other.season(2), other.episode(1, 1), other.episode(1, 2), other.episode(2, 1)} {
		wantLikes(t, f.rate(http.MethodPost, id, "?likes=false"), no())
	}
	afterLists := read()
	if strings.Contains(afterLists[upcoming], other.encode()) || strings.Contains(afterLists[upcoming], other.episode(1, 1).encode()) {
		t.Errorf("a series that is only rated is upcoming: %s", afterLists[upcoming])
	}
	for path, body := range beforeLists {
		if afterLists[path] != body {
			t.Errorf("%s changed:\n%s\n%s", path, body, afterLists[path])
		}
	}
	for key, st := range f.play.rows {
		if _, ok := beforeRows[key]; ok {
			continue
		}
		if !st.Likes.Valid || st.PositionTicks != 0 || st.Played || st.PlayCount != 0 || st.IsFavorite || !st.LastPlayedAt.IsZero() {
			t.Errorf("%s: %+v", key, st)
		}
	}
	for _, id := range []itemID{other, other.season(1), other.episode(1, 1)} {
		if ud := folderUD(t, f, id); ud.Played || ud.PlaybackPositionTicks != 0 || ud.PlayCount != 0 || ud.LastPlayedDate != "" || ud.IsFavorite {
			t.Errorf("%s: a rating changed more than Likes: %+v", id.encode(), ud)
		}
	}
	if got := played(f, "living-room"); len(got) != 2 {
		t.Errorf("played %v after rating, want just the two marked", got)
	}
}
