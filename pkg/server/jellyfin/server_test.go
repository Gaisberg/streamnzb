package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/release"
	"streamnzb/pkg/server/stremio"
)

func init() {
	logger.Init("ERROR")
}

// fakeStreams is a two-stream world: "living-room", which has a token and the
// password "room-pw", and an admin, which is not a stream at all.
type fakeStreams struct{}

const (
	testToken      = "tok-living-room-0123456789abcdef"
	testAdminToken = "tok-admin-fedcba9876543210"
	testOtherToken = "tok-kitchen-0123456789abcdef00"
	testPassword   = "room-pw"
)

func (fakeStreams) AuthenticateStream(username, password string) (*auth.Stream, error) {
	if strings.EqualFold(username, "living-room") && (password == testPassword || password == testToken) {
		return &auth.Stream{Username: "living-room", Token: testToken}, nil
	}
	if strings.EqualFold(username, "kitchen") && password == testOtherToken {
		return &auth.Stream{Username: "kitchen", Token: testOtherToken}, nil
	}
	return nil, errors.New("invalid credentials")
}

func (fakeStreams) AuthenticateToken(token, adminUsername, adminToken string) (*auth.Stream, error) {
	switch token {
	case testToken:
		return &auth.Stream{Username: "living-room", Token: testToken}, nil
	case testOtherToken:
		return &auth.Stream{Username: "kitchen", Token: testOtherToken}, nil
	case adminToken:
		return &auth.Stream{Username: adminUsername, Token: adminToken}, nil
	}
	return nil, errors.New("invalid token")
}

type fakeCatalog struct {
	mu            sync.Mutex
	catalogs      []stremio.CatalogDef
	rows          map[string][]stremio.MetaPreview
	metas         map[string]*stremio.MetaObject
	playlist      *stremio.PlaylistView
	playErr       error
	cached        *stremio.PlaylistView
	searches      []string
	metaCalls     int
	playlistCalls int
	served        []servedPlay
	disabled      bool
}

// releaseCaps is what ffprobe measured on the candidate that played: a
// 100-minute file, shorter than the 142 minutes the metadata claims.
var releaseCaps = release.MediaCaps{VideoCodec: "h264", AudioCodec: "aac", Width: 1920, Height: 1080, DurationSeconds: 100 * 60}

type servedPlay struct {
	slotPath string
	opts     stremio.PlayServeOptions
}

// searchSet is the set of searches run, since the carriers run in parallel.
func (f *fakeCatalog) searchSet() map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]bool{}
	for _, s := range f.searches {
		out[s] = true
	}
	return out
}

func (f *fakeCatalog) EnabledCatalogs(*auth.Stream) ([]stremio.CatalogDef, error) {
	if f.disabled {
		return nil, stremio.ErrMetadataDisabled
	}
	return f.catalogs, nil
}

func (f *fakeCatalog) Catalog(_ context.Context, _ *auth.Stream, catalogID, _, search string, skip int) ([]stremio.MetaPreview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if search != "" {
		f.searches = append(f.searches, catalogID+"="+search)
		return f.rows[catalogID], nil
	}
	rows := f.rows[catalogID]
	if skip >= len(rows) {
		return nil, nil
	}
	rows = rows[skip:]
	if len(rows) > stremio.CatalogPageSize {
		rows = rows[:stremio.CatalogPageSize]
	}
	return rows, nil
}

func (f *fakeCatalog) Meta(_ context.Context, _ *auth.Stream, contentType, id string) (*stremio.MetaObject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metaCalls++
	if f.disabled {
		return nil, stremio.ErrMetadataDisabled
	}
	meta, ok := f.metas[contentType+"/"+id]
	if !ok {
		return nil, fmt.Errorf("no meta for %s/%s", contentType, id)
	}
	return meta, nil
}

func (f *fakeCatalog) Playlist(context.Context, *auth.Stream, string, string) (*stremio.PlaylistView, error) {
	f.mu.Lock()
	f.playlistCalls++
	f.mu.Unlock()
	if f.playErr != nil {
		return nil, f.playErr
	}
	if f.playlist == nil {
		return &stremio.PlaylistView{}, nil
	}
	return f.playlist, nil
}

func (f *fakeCatalog) PlaylistCached(*auth.Stream, string, string) (*stremio.PlaylistView, bool) {
	return f.cached, f.cached != nil
}

func (f *fakeCatalog) ServePlay(w http.ResponseWriter, _ *http.Request, _ *auth.Stream, slotPath string, opts stremio.PlayServeOptions) {
	f.mu.Lock()
	f.served = append(f.served, servedPlay{slotPath: slotPath, opts: opts})
	f.mu.Unlock()
	w.Header().Set("X-Slot", slotPath)
	w.WriteHeader(http.StatusOK)
}

type fakePlaystate struct {
	mu    sync.Mutex
	rows  map[string]persistence.JellyfinPlaystate
	saves int
}

func newFakePlaystate() *fakePlaystate {
	return &fakePlaystate{rows: map[string]persistence.JellyfinPlaystate{}}
}

func (p *fakePlaystate) Upsert(state persistence.JellyfinPlaystate) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.saves++
	p.rows[state.StreamName+"|"+state.ItemID] = state
	return nil
}

func (p *fakePlaystate) Get(streamName, itemID string) (persistence.JellyfinPlaystate, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st, ok := p.rows[streamName+"|"+itemID]
	return st, ok
}

func (p *fakePlaystate) ListResume(streamName string, limit int) []persistence.JellyfinPlaystate {
	var out []persistence.JellyfinPlaystate
	for _, st := range p.ListForStream(streamName) {
		if !st.Played && st.PositionTicks > 0 && len(out) < limit {
			out = append(out, st)
		}
	}
	return out
}

func (p *fakePlaystate) ListForStream(streamName string) []persistence.JellyfinPlaystate {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []persistence.JellyfinPlaystate
	for _, st := range p.rows {
		if st.StreamName == streamName {
			out = append(out, st)
		}
	}
	return out
}

func (p *fakePlaystate) SetPlayed(streamName, itemID, contentType, contentID string, played bool) error {
	st, ok := p.Get(streamName, itemID)
	if !ok {
		st = persistence.JellyfinPlaystate{StreamName: streamName, ItemID: itemID, ContentType: contentType, ContentID: contentID}
	}
	st.Played = played
	st.PositionTicks = 0
	return p.Upsert(st)
}

func previews(contentType, prefix string, n int) []stremio.MetaPreview {
	out := make([]stremio.MetaPreview, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, stremio.MetaPreview{ID: fmt.Sprintf("%s%07d", prefix, i), Type: contentType, Name: fmt.Sprintf("Title %d", i), Poster: fmt.Sprintf("https://img.test/%s/%d.jpg", contentType, i), Background: fmt.Sprintf("https://img.test/%s/%d-bg.jpg", contentType, i)})
	}
	return out
}

// testCDNServer stands in for a provider's image CDN: it answers any path
// with 200 and the path itself as the body, so a test can tell which URL
// the relay actually fetched, and answers a path containing "missing" with
// 404. One instance covers the whole test binary; there is nothing to
// serve differently per test that a distinct path can't already express.
var testCDNServer = sync.OnceValue(func() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "missing") {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(r.URL.Path, "huge") {
			// Announces a body far past the relay cap; a well-behaved client
			// gets refused before either header reaches it.
			w.Header().Set("Content-Length", strconv.Itoa(maxImageBody+1))
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte("not the whole thing"))
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte(r.URL.Path))
	}))
})

func testCatalog() *fakeCatalog {
	cdn := testCDNServer().URL
	movie := &stremio.MetaObject{
		ID: "tt0111161", Type: "movie", Name: "The Shawshank Redemption",
		Poster: cdn + "/shawshank.jpg", Background: cdn + "/shawshank-bg.jpg", Logo: cdn + "/shawshank-logo.png",
		Description: "Two imprisoned men bond.", ReleaseInfo: "1994", Released: "1994-09-23T00:00:00.000Z",
		IMDBRating: "9.3", Runtime: "142 min", Genres: []string{"Drama"}, Cast: []string{"Tim Robbins"}, Director: []string{"Frank Darabont"},
		Trailers: []stremio.MetaTrailer{{Source: "6hB3S9bIaco", Type: "Trailer"}},
		AppExtras: &stremio.MetaAppExtras{Cast: []stremio.MetaCastMember{
			{Name: "Tim Robbins", Character: "Andy Dufresne", Photo: cdn + "/tim-robbins.jpg"},
		}},
	}
	series := &stremio.MetaObject{
		ID: "tt0903747", Type: "series", Name: "Breaking Bad", Poster: "https://img.test/bb.jpg", Background: cdn + "/bb-bg.jpg",
		ReleaseInfo: "2008-2013", Released: "2008-01-20T00:00:00.000Z", Runtime: "49 min",
		Videos: []stremio.MetaVideo{
			{ID: "tt0903747:1:1", Title: "Pilot", Season: 1, Episode: 1, Released: "2008-01-20T00:00:00.000Z", Thumbnail: cdn + "/bb-s1e1.jpg"},
			{ID: "tt0903747:1:2", Title: "Cat's in the Bag...", Season: 1, Episode: 2},
			{ID: "tt0903747:2:1", Title: "Seven Thirty-Seven", Season: 2, Episode: 1},
			{ID: "tt0903747:0:1", Title: "Minisode", Season: 0, Episode: 1},
		},
	}
	kitsuMovie := &stremio.MetaObject{ID: "kitsu:9", Type: "anime", Name: "Your Name.", Poster: "https://img.test/yn.jpg", Runtime: "106 min"}
	return &fakeCatalog{
		catalogs: []stremio.CatalogDef{
			{ID: "tmdb.trending.movie", Type: "movie", Name: "Trending Movies", SupportsSkip: true},
			{ID: "tmdb.trending.series", Type: "series", Name: "Trending Series", SupportsSkip: true},
			{ID: "kitsu.trending.anime", Type: "anime", Name: "Trending Anime"},
		},
		rows: map[string][]stremio.MetaPreview{
			"tmdb.trending.movie":  previews("movie", "tt", 45),
			"tmdb.trending.series": previews("series", "tt", 3),
			"kitsu.trending.anime": []stremio.MetaPreview{{ID: "kitsu:9", Type: "anime", Name: "Your Name."}},
			"tmdb.search.movie":    []stremio.MetaPreview{{ID: "tt0111161", Type: "movie", Name: "The Shawshank Redemption"}},
			"tmdb.search.series":   []stremio.MetaPreview{{ID: "tt0903747", Type: "series", Name: "Breaking Bad"}},
			"kitsu.search.anime":   nil,
		},
		metas: map[string]*stremio.MetaObject{
			"movie/tt0111161":  movie,
			"series/tt0903747": series,
			"anime/kitsu:9":    kitsuMovie,
		},
	}
}

type fixture struct {
	server        *Server
	catalog       *fakeCatalog
	play          *fakePlaystate
	serverID      string
	baseURL       string
	maxSources    int
	resolveOnOpen bool
}

func newFixture() *fixture {
	f := &fixture{catalog: testCatalog(), play: newFakePlaystate(), serverID: "srv-0001", baseURL: "https://nzb.example"}
	f.server = New(Options{
		ServerID:           func() string { return f.serverID },
		BaseURL:            func() string { return f.baseURL },
		Admin:              func() (string, string, string) { return "admin", "$argon2id$hash", testAdminToken },
		MaxPlaybackSources: func() int { return f.maxSources },
		ResolveOnOpen:      func() bool { return f.resolveOnOpen },
		Streams:            fakeStreams{},
		Catalog:            f.catalog,
		Playstate:          f.play,
		Version:            "test",
	})
	return f
}

// do issues a request as the living-room stream, with the token in the
// MediaBrowser header unless the path already carries one.
func (f *fixture) do(method, path string, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if len(headers) == 0 {
		req.Header.Set("Authorization", `MediaBrowser Client="Swiftfin", Device="iPhone 15, Pro", DeviceId="dev-1", Version="1.3", Token="`+testToken+`"`)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(rec, req)
	return rec
}

// isGUIDShaped mirrors what the Jellyfin Kotlin SDK's String.toUUID() accepts:
// 32 unseparated hex characters, which it hyphenates before handing to
// UUID.fromString. Wholphin runs every media source id through it and throws
// IllegalArgumentException — crashing the app — on anything else, so the ids
// this layer mints have to satisfy it.
func isGUIDShaped(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func decodeInto(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
}

func TestPublicRoutesNeedNoToken(t *testing.T) {
	f := newFixture()
	var info publicSystemInfo
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/System/Info/Public", "", "X-Forwarded-Proto", "https"), &info)
	if info.Version != reportedVersion || info.ID != "srv-0001" || info.LocalAddress != "https://example.com/jellyfin" {
		t.Fatalf("public info: %+v", info)
	}
	// A reachability probe by HEAD is answered wherever GET is.
	if rec := f.do(http.MethodHead, "/jellyfin/System/Info/Public", "", "X-Nothing", "x"); rec.Code != http.StatusOK {
		t.Fatalf("HEAD on a public route: %d", rec.Code)
	}
	if rec := f.do(http.MethodHead, "/jellyfin/", "", "X-Nothing", "x"); rec.Code != http.StatusOK {
		t.Fatalf("HEAD on the bare mount: %d", rec.Code)
	}
	// The bare mount answers the same handshake, unauthenticated: it is what
	// a client probes before it has a token to send.
	var root publicSystemInfo
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/", "", "X-Nothing", "x"), &root)
	if root.ID != "srv-0001" || root.ServerName != serverName {
		t.Fatalf("bare mount: %+v", root)
	}
	if rec := f.do(http.MethodGet, "/jellyfin/UserViews", "", "X-Nothing", "x"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated views: %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, "/jellyfin/UserViews", "", "X-Emby-Token", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", rec.Code)
	}
}

func TestAuthenticateByName(t *testing.T) {
	f := newFixture()
	var result authenticationResult
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/Users/AuthenticateByName", `{"Username":"Living-Room","Pw":"`+testToken+`"}`, "X-Emby-Authorization", `MediaBrowser Client="Findroid", Device="Pixel", DeviceId="d2", Version="0.15"`), &result)
	if result.AccessToken != testToken || result.User.Name != "living-room" || result.ServerID != "srv-0001" {
		t.Fatalf("login result: %+v", result)
	}
	if result.User.Policy.IsAdministrator {
		t.Fatalf("stream login must not be an administrator")
	}
	if result.SessionInfo.Client != "Findroid" || result.SessionInfo.DeviceName != "Pixel" {
		t.Fatalf("session info did not read the client header: %+v", result.SessionInfo)
	}

	// The token signs in only under its own stream's name.
	if rec := f.do(http.MethodPost, "/jellyfin/Users/AuthenticateByName", `{"Username":"someone-else","Pw":"`+testToken+`"}`, "X-None", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token under another name: %d", rec.Code)
	}
	// A stream signs in with its own password as well as its token.
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/emby/Users/AuthenticateByName", `{"Username":"Living-Room","Password":"`+testPassword+`"}`, "X-None", ""), &result)
	if result.AccessToken != testToken || result.User.Name != "living-room" {
		t.Fatalf("password login: %+v", result)
	}
	// The admin is not a stream: it has nothing to play, so it is refused at
	// the door rather than allowed to browse its way to a failure.
	if rec := f.do(http.MethodPost, "/jellyfin/Users/AuthenticateByName", `{"Username":"admin","Password":"admin-pw"}`, "X-None", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("admin login: %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, "/jellyfin/UserViews", "", "X-Emby-Token", testAdminToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("admin token: %d", rec.Code)
	}
}

func TestTokenFormsAndCaseInsensitivity(t *testing.T) {
	f := newFixture()
	forms := [][]string{
		{"Authorization", `MediaBrowser Token="` + testToken + `", Client="x"`},
		{"X-Emby-Authorization", `Emby Client=x, Token=` + testToken},
		{"X-Emby-Token", testToken},
		{"X-MediaBrowser-Token", testToken},
	}
	for _, form := range forms {
		var result queryResult
		decodeInto(t, f.do(http.MethodGet, "/jellyfin/USERVIEWS", "", form...), &result)
		if len(result.Items) != 3 {
			t.Fatalf("%s: %d views", form[0], len(result.Items))
		}
	}
	var result queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/abc/Views?API_KEY="+testToken, "", "X-None", ""), &result)
	if len(result.Items) != 3 || result.Items[0].CollectionType != "movies" || result.Items[2].CollectionType != "tvshows" {
		t.Fatalf("views via api_key: %+v", result.Items)
	}
	// Query names are case-insensitive too.
	view := result.Items[0].ID
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?PARENTID="+view+"&LIMIT=5", ""), &result)
	if len(result.Items) != 5 {
		t.Fatalf("upper-cased query: %d items", len(result.Items))
	}
}

func TestViewsPageThroughCatalogs(t *testing.T) {
	f := newFixture()
	view := viewID("tmdb.trending.movie")
	var result queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items?ParentId="+view+"&StartIndex=20&Limit=20", ""), &result)
	if len(result.Items) != 20 || result.Items[0].Name != "Title 21" || result.Items[19].Name != "Title 40" {
		t.Fatalf("page 2: %d items, first %q", len(result.Items), result.Items[0].Name)
	}
	if result.TotalRecordCount <= 40 {
		t.Fatalf("a full page must leave room for more: total %d", result.TotalRecordCount)
	}
	if result.Items[0].Type != "Movie" || result.Items[0].ParentID != view || result.Items[0].ImageTags["Primary"] == "" || len(result.Items[0].BackdropImageTags) != 1 {
		t.Fatalf("row shape: %+v", result.Items[0])
	}
	if len(result.Items[0].MediaSources) != 2 || len(result.Items[0].AlternateMediaSources) != 2 || result.Items[0].MediaSourceCount == nil || *result.Items[0].MediaSourceCount != 2 || result.Items[0].EnableMediaSourceDisplay == nil || !*result.Items[0].EnableMediaSourceDisplay {
		t.Fatalf("Infuse picker row contract: %+v", result.Items[0])
	}
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?ParentId="+view+"&StartIndex=40&Limit=20", ""), &result)
	if len(result.Items) != 5 || result.TotalRecordCount != 45 {
		t.Fatalf("last page: %d items, total %d", len(result.Items), result.TotalRecordCount)
	}
	// A catalog that cannot skip has only its first page.
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?ParentId="+viewID("kitsu.trending.anime")+"&StartIndex=20&Limit=20", ""), &result)
	if len(result.Items) != 0 {
		t.Fatalf("skipless catalog paged: %d", len(result.Items))
	}
	// Type filters apply to the folder.
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?ParentId="+view+"&IncludeItemTypes=Series", ""), &result)
	if len(result.Items) != 0 {
		t.Fatalf("series filter on a movie folder returned %d", len(result.Items))
	}
	// Latest is a bare array off the top of the folder.
	var latest []*baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items/Latest?ParentId="+view+"&Limit=3", ""), &latest)
	if len(latest) != 3 || latest[0].Name != "Title 1" {
		t.Fatalf("latest: %+v", latest)
	}
	// No metadata profile: no views, and items are not found.
	f.catalog.disabled = true
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/UserViews", ""), &result)
	if len(result.Items) != 0 {
		t.Fatalf("disabled metadata listed views")
	}
	movie, _ := itemIDFor("movie", "tt0111161")
	if rec := f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode(), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("item with metadata disabled: %d", rec.Code)
	}
}

func TestMovieDetail(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	var item baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items/"+movie.encode(), ""), &item)
	if item.Type != "Movie" || item.Name != "The Shawshank Redemption" || item.MediaType != "Video" || item.IsFolder {
		t.Fatalf("movie: %+v", item)
	}
	if item.RunTimeTicks == nil || *item.RunTimeTicks != 142*60*ticksPerSecond {
		t.Fatalf("runtime ticks: %v", item.RunTimeTicks)
	}
	if item.ProductionYear == nil || *item.ProductionYear != 1994 || !strings.HasPrefix(item.PremiereDate, "1994-09-23T") {
		t.Fatalf("year/premiere: %v %q", item.ProductionYear, item.PremiereDate)
	}
	if item.CommunityRating == nil || *item.CommunityRating != 9.3 || item.ProviderIDs["Imdb"] != "tt0111161" {
		t.Fatalf("rating/provider: %v %v", item.CommunityRating, item.ProviderIDs)
	}
	if len(item.People) != 2 || item.People[0].Type != "Actor" || item.People[1].Type != "Director" {
		t.Fatalf("people: %+v", item.People)
	}
	if len(item.RemoteTrailers) != 1 || !strings.Contains(item.RemoteTrailers[0].URL, "6hB3S9bIaco") {
		t.Fatalf("trailers: %+v", item.RemoteTrailers)
	}
	if item.UserData == nil || item.UserData.Played || item.UserData.ItemID != item.ID {
		t.Fatalf("user data: %+v", item.UserData)
	}
	if item.ImageTags["Primary"] == "" || item.ImageTags["Logo"] == "" || len(item.BackdropImageTags) != 1 {
		t.Fatalf("image tags: %+v %+v", item.ImageTags, item.BackdropImageTags)
	}
	// Images are relayed from the provider URL the tag hashes, not redirected
	// to it: Infuse ignores a 302 on artwork and is left with nothing.
	rec := f.do(http.MethodGet, "/jellyfin/Items/"+item.ID+"/Images/Primary?tag="+item.ImageTags["Primary"], "")
	if rec.Code != http.StatusOK || rec.Body.String() != "/shawshank.jpg" || rec.Header().Get("Cache-Control") != "public, max-age=86400" {
		t.Fatalf("primary image: %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
	}
	// A client's image loader carries no token; the tag alone must serve it,
	// and an image it cannot resolve is a missing image, never a 401.
	anon := f.do(http.MethodGet, "/jellyfin/Items/"+item.ID+"/Images/Primary?tag="+item.ImageTags["Primary"], "", "X-Nothing", "x")
	if anon.Code != http.StatusOK || anon.Body.String() != rec.Body.String() {
		t.Fatalf("anonymous image by tag: %d %s", anon.Code, anon.Body.String())
	}
	if anon := f.do(http.MethodGet, "/jellyfin/Items/"+item.ID+"/Images/Primary", "", "X-Nothing", "x"); anon.Code != http.StatusNotFound {
		t.Fatalf("anonymous image without a tag: %d", anon.Code)
	}
	rec = f.do(http.MethodGet, "/jellyfin/Items/"+item.ID+"/Images/Backdrop/0?tag="+item.BackdropImageTags[0], "")
	if rec.Code != http.StatusOK || rec.Body.String() != "/shawshank-bg.jpg" {
		t.Fatalf("backdrop image: %d %s", rec.Code, rec.Body.String())
	}
	// Item lookup by ids.
	var result queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?Ids="+item.ID+",zzz", ""), &result)
	if len(result.Items) != 1 || result.Items[0].Name != item.Name {
		t.Fatalf("ids lookup: %+v", result.Items)
	}
}

// TestInfuseFields covers the fields Infuse 8.5 requires on every item
// (Path, Etag, DateCreated): a view, a movie, a series, a season and an
// episode all must carry non-empty values, a movie's DateCreated must equal
// its PremiereDate, and the Etag must change when the poster does.
func TestInfuseFields(t *testing.T) {
	f := newFixture()
	require := func(item baseItem, at string) {
		if item.Path == "" || item.Etag == "" || item.DateCreated == "" {
			t.Fatalf("%s missing infuse fields: %+v", at, item)
		}
	}

	var views queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/UserViews", ""), &views)
	if len(views.Items) == 0 {
		t.Fatalf("no views")
	}
	require(*views.Items[0], "view")
	if views.Items[0].DateCreated != dateCreatedFallback {
		t.Fatalf("view DateCreated: %q", views.Items[0].DateCreated)
	}

	movie, _ := itemIDFor("movie", "tt0111161")
	var movieItem baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode(), ""), &movieItem)
	require(movieItem, "movie")
	if movieItem.DateCreated != movieItem.PremiereDate {
		t.Fatalf("movie DateCreated %q != PremiereDate %q", movieItem.DateCreated, movieItem.PremiereDate)
	}
	if !strings.HasPrefix(movieItem.Path, "/movie/") {
		t.Fatalf("movie path: %q", movieItem.Path)
	}

	series, _ := itemIDFor("series", "tt0903747")
	var seriesItem baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+series.encode(), ""), &seriesItem)
	require(seriesItem, "series")
	if !strings.HasPrefix(seriesItem.Path, "/series/") {
		t.Fatalf("series path: %q", seriesItem.Path)
	}
	if seriesItem.PremiereDate == "" || seriesItem.DateCreated != seriesItem.PremiereDate {
		t.Fatalf("series DateCreated %q != PremiereDate %q", seriesItem.DateCreated, seriesItem.PremiereDate)
	}

	var seasons queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Shows/"+series.encode()+"/Seasons?UserId=u", ""), &seasons)
	require(*seasons.Items[0], "season")

	var episodes queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Shows/"+series.encode()+"/Episodes?SeasonId="+seasons.Items[0].ID, ""), &episodes)
	require(*episodes.Items[0], "episode")
	if episodes.Items[0].DateCreated != episodes.Items[0].PremiereDate {
		t.Fatalf("episode DateCreated %q != PremiereDate %q", episodes.Items[0].DateCreated, episodes.Items[0].PremiereDate)
	}

	// A poster change changes the Etag: swap the fixture's meta and reread.
	before := movieItem.Etag
	f.catalog.metas["movie/tt0111161"].Poster = "/shawshank-2.jpg"
	var again baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode(), ""), &again)
	if again.Etag == before {
		t.Fatalf("etag did not change with the poster: %q", again.Etag)
	}
}

func TestImageMissResolvesFromMetadata(t *testing.T) {
	f := newFixture()
	series, _ := itemIDFor("series", "tt0903747")
	ep := series.episode(1, 1)
	// A fresh server has no tag map; the id alone must be enough.
	rec := f.do(http.MethodGet, "/jellyfin/Items/"+ep.encode()+"/Images/Primary?tag=stale", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "/bb-s1e1.jpg" {
		t.Fatalf("episode primary on a miss: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.do(http.MethodGet, "/jellyfin/Items/"+series.season(2).encode()+"/Images/Backdrop", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "/bb-bg.jpg" {
		t.Fatalf("season backdrop: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do(http.MethodGet, "/jellyfin/Items/"+series.encode()+"/Images/Logo", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing logo: %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, "/jellyfin/Items/"+userID("person:Tim Robbins")+"/Images/Primary", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("person image: %d", rec.Code)
	}
}

func TestImageRelayHEADHasHeadersButNoBody(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	rec := f.do(http.MethodHead, "/jellyfin/Items/"+movie.encode()+"/Images/Primary", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD relay: %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("HEAD content-type: %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=86400" {
		t.Fatalf("HEAD cache-control: %q", cc)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD body: %q", rec.Body.String())
	}
}

func TestImageRelayUpstream404(t *testing.T) {
	f := newFixture()
	f.catalog.metas["movie/tt0111161"].Poster = testCDNServer().URL + "/missing.jpg"
	movie, _ := itemIDFor("movie", "tt0111161")
	if rec := f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode()+"/Images/Primary", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("upstream 404: %d", rec.Code)
	}
}

func TestImageRelayRefusesOversizedContentLength(t *testing.T) {
	f := newFixture()
	f.catalog.metas["movie/tt0111161"].Poster = testCDNServer().URL + "/huge.jpg"
	movie, _ := itemIDFor("movie", "tt0111161")
	rec := f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode()+"/Images/Primary", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("oversized content-length: %d", rec.Code)
	}
	// Refused before any of the CDN's response reaches the client.
	if rec.Header().Get("Content-Type") == "image/jpeg" || strings.Contains(rec.Body.String(), "not the whole thing") {
		t.Fatalf("oversized content-length leaked upstream headers/body: %+v %q", rec.Header(), rec.Body.String())
	}
}

func TestImageRelayUpstreamUnreachable(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL + "/gone.jpg"
	dead.Close() // closed before use: nothing answers this address any more.
	f := newFixture()
	f.catalog.metas["movie/tt0111161"].Poster = deadURL
	movie, _ := itemIDFor("movie", "tt0111161")
	if rec := f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode()+"/Images/Primary", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("upstream unreachable: %d", rec.Code)
	}
}

func TestRewriteImageSize(t *testing.T) {
	base := "https://image.tmdb.org/t/p/original/abc.jpg"
	if got := rewriteImageSize(base, "backdrop"); got != "https://image.tmdb.org/t/p/w1280/abc.jpg" {
		t.Fatalf("backdrop: %q", got)
	}
	if got := rewriteImageSize(base, "primary"); got != "https://image.tmdb.org/t/p/w780/abc.jpg" {
		t.Fatalf("primary: %q", got)
	}
	if got := rewriteImageSize(base, "thumb"); got != "https://image.tmdb.org/t/p/w780/abc.jpg" {
		t.Fatalf("thumb: %q", got)
	}
	if got := rewriteImageSize(base, "logo"); got != base {
		t.Fatalf("logo passthrough: %q", got)
	}
	if got := rewriteImageSize("https://img.test/abc.jpg", "backdrop"); got != "https://img.test/abc.jpg" {
		t.Fatalf("non-tmdb passthrough: %q", got)
	}
	if got := rewriteImageSize(base, "person"); got != "https://image.tmdb.org/t/p/h632/abc.jpg" {
		t.Fatalf("person: %q", got)
	}
}

// TestCastPhotos checks that a cast member with a headshot gets a
// PrimaryImageTag the item document can carry, and that the tag relays the
// same way any other image does.
func TestCastPhotos(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	var item baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items/"+movie.encode(), ""), &item)
	if len(item.People) != 2 || item.People[0].Name != "Tim Robbins" || item.People[0].PrimaryImageTag == "" {
		t.Fatalf("cast photo tag missing: %+v", item.People)
	}
	if tag := item.People[1].PrimaryImageTag; tag != "" {
		t.Fatalf("director has no photo, got a tag: %q", tag)
	}
	rec := f.do(http.MethodGet, "/jellyfin/Items/"+item.People[0].ID+"/Images/Primary?tag="+item.People[0].PrimaryImageTag, "")
	if rec.Code != http.StatusOK || rec.Body.String() != "/tim-robbins.jpg" {
		t.Fatalf("cast photo relay: %d %q", rec.Code, rec.Body.String())
	}
}

// TestCastPhotoRegisteredAtPersonSize checks that a TMDB cast photo is
// registered at h632 — resolved once, at registration, since the images
// route has no kind to size by when a request arrives by tag alone.
func TestCastPhotoRegisteredAtPersonSize(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	f.catalog.metas["movie/tt0111161"].AppExtras.Cast[0].Photo = "https://image.tmdb.org/t/p/original/tim-robbins.jpg"
	var item baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items/"+movie.encode(), ""), &item)
	tag := item.People[0].PrimaryImageTag
	if tag == "" {
		t.Fatalf("no photo tag registered: %+v", item.People[0])
	}
	url, ok := f.server.images.urlFor(tag)
	if !ok || url != "https://image.tmdb.org/t/p/h632/tim-robbins.jpg" {
		t.Fatalf("person photo not registered at h632: ok=%v url=%q", ok, url)
	}
}

func TestSeriesSeasonsAndEpisodes(t *testing.T) {
	f := newFixture()
	series, _ := itemIDFor("series", "tt0903747")
	var item baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+series.encode(), ""), &item)
	if item.Type != "Series" || !item.IsFolder || item.Status != "Ended" || item.ChildCount == nil || *item.ChildCount != 3 {
		t.Fatalf("series: %+v", item)
	}
	var seasons queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Shows/"+series.encode()+"/Seasons?UserId=u", ""), &seasons)
	if len(seasons.Items) != 3 || seasons.Items[0].Name != "Season 1" || seasons.Items[2].Name != "Specials" || *seasons.Items[0].ChildCount != 2 {
		t.Fatalf("seasons: %+v", seasons.Items)
	}
	if seasons.Items[0].SeriesID != series.encode() || seasons.Items[0].ImageTags["Primary"] == "" {
		t.Fatalf("season parentage/art: %+v", seasons.Items[0])
	}
	var episodes queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Shows/"+series.encode()+"/Episodes?SeasonId="+seasons.Items[0].ID, ""), &episodes)
	if len(episodes.Items) != 2 || episodes.Items[0].Name != "Pilot" || *episodes.Items[0].IndexNumber != 1 || *episodes.Items[0].ParentIndexNumber != 1 {
		t.Fatalf("episodes: %+v", episodes.Items)
	}
	ep := episodes.Items[0]
	if ep.SeriesName != "Breaking Bad" || ep.SeasonID != seasons.Items[0].ID || ep.SeriesPrimaryImageTag == "" || ep.RunTimeTicks == nil || ep.UserData == nil {
		t.Fatalf("episode shape: %+v", ep)
	}
	// Whole-series episode listing, and the same through /Items.
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Shows/"+series.encode()+"/Episodes", ""), &episodes)
	if len(episodes.Items) != 4 {
		t.Fatalf("all episodes: %d", len(episodes.Items))
	}
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?ParentId="+series.encode(), ""), &seasons)
	if len(seasons.Items) != 3 || seasons.Items[0].Type != "Season" {
		t.Fatalf("items under series: %+v", seasons.Items)
	}
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?ParentId="+series.encode()+"&IncludeItemTypes=Episode&Recursive=true", ""), &episodes)
	if len(episodes.Items) != 4 || episodes.Items[0].Type != "Episode" {
		t.Fatalf("recursive episodes: %d", len(episodes.Items))
	}
	// An episode the series does not have is not found.
	if rec := f.do(http.MethodGet, "/jellyfin/Items/"+series.episode(9, 9).encode(), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing episode: %d", rec.Code)
	}
	// A Kitsu movie is a one-episode series that plays as the entry.
	anime, _ := itemIDFor("anime", "kitsu:9")
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+anime.encode(), ""), &item)
	if item.Type != "Series" || *item.RecursiveItemCount != 1 {
		t.Fatalf("kitsu movie: %+v", item)
	}
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Shows/"+anime.encode()+"/Episodes", ""), &episodes)
	if len(episodes.Items) != 1 || episodes.Items[0].Name != "Your Name." {
		t.Fatalf("kitsu movie episodes: %+v", episodes.Items)
	}
	if id, _ := decodeItemID(episodes.Items[0].ID); id.playStremioID() != "kitsu:9" {
		t.Fatalf("kitsu movie episode plays %q", id.playStremioID())
	}
}

func TestSearchRunsTheCarriers(t *testing.T) {
	f := newFixture()
	var result queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items?SearchTerm=shaw&Recursive=true&IncludeItemTypes=Movie,Series", ""), &result)
	if len(result.Items) != 2 {
		t.Fatalf("search results: %+v", result.Items)
	}
	// Anime is carried as a Series, so a Movie,Series search runs all three
	// carriers; the carriers run in parallel, so compare them as a set.
	if got := f.catalog.searchSet(); !reflect.DeepEqual(got, map[string]bool{"tmdb.search.movie=shaw": true, "tmdb.search.series=shaw": true, "kitsu.search.anime=shaw": true}) {
		t.Fatalf("searches run: %v", got)
	}
	// A Movie-only search leaves the series carriers alone.
	f.catalog.searches = nil
	f.do(http.MethodGet, "/jellyfin/Users/u/Items?SearchTerm=shaw&Recursive=true&IncludeItemTypes=Movie", "")
	if got := f.catalog.searchSet(); !reflect.DeepEqual(got, map[string]bool{"tmdb.search.movie=shaw": true}) {
		t.Fatalf("movie-only searches run: %v", got)
	}
	var hints struct {
		SearchHints      []searchHint
		TotalRecordCount int
	}
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Search/Hints?searchTerm=bad&includeItemTypes=Series", ""), &hints)
	if hints.TotalRecordCount != 1 || hints.SearchHints[0].Name != "Breaking Bad" || hints.SearchHints[0].Type != "Series" {
		t.Fatalf("hints: %+v", hints)
	}
}

// A media player fetches the stream URL with no credentials at all: Findroid
// hands ExoPlayer a bare URL from the SDK, which carries neither an
// Authorization header nor an api_key. Whatever the client copied off the
// media source is all there is, and it must open the video without opening
// anything else.
func TestStreamServesAPlayerCarryingNoCredentials(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	stream := &auth.Stream{Username: "living-room", Token: testToken}
	source := movie.source(0).encode()

	// The URL the media source carries in Path, played verbatim: no headers
	// of any kind, exactly as the player sends it.
	rec := f.do(http.MethodGet, "/jellyfin/Videos/"+movie.encode()+"/stream?static=true&mediaSourceId="+source+"&api_key="+testToken, "", "X-Nothing", "x")
	if rec.Code != http.StatusOK {
		t.Fatalf("bare player fetch: %d %s", rec.Code, rec.Body.String())
	}
	if len(f.catalog.served) != 1 || f.catalog.served[0].slotPath != stremio.SlotPathFor(stream, "movie", "tt0111161", 0) {
		t.Fatalf("served %+v", f.catalog.served)
	}

	// A client that built its own URL instead sends the ETag it copied as
	// tag, and nothing else.
	f.catalog.served = nil
	rec = f.do(http.MethodGet, "/jellyfin/Videos/"+movie.encode()+"/stream?static=true&mediaSourceId="+source+"&tag="+testToken, "", "X-Nothing", "x")
	if rec.Code != http.StatusOK {
		t.Fatalf("player fetch by tag: %d %s", rec.Code, rec.Body.String())
	}
	if len(f.catalog.served) != 1 || f.catalog.served[0].slotPath != stremio.SlotPathFor(stream, "movie", "tt0111161", 0) {
		t.Fatalf("served by tag %+v", f.catalog.served)
	}

	// Swiftfin's SDK writes the query name as Tag, so the casing cannot
	// matter: reading only a lowercase tag would refuse every Swiftfin play.
	f.catalog.served = nil
	if rec := f.do(http.MethodGet, "/jellyfin/Videos/"+movie.encode()+"/stream?MediaSourceId="+source+"&Tag="+testToken, "", "X-Nothing", "x"); rec.Code != http.StatusOK {
		t.Fatalf("player fetch by Tag: %d", rec.Code)
	}

	// Carrying no token at all is still refused.
	if rec := f.do(http.MethodGet, "/jellyfin/Videos/"+movie.encode()+"/stream?mediaSourceId="+source, "", "X-Nothing", "x"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, "/jellyfin/Videos/"+movie.encode()+"/stream?mediaSourceId="+source+"&tag=not-a-token", "", "X-Nothing", "x"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token in tag: %d", rec.Code)
	}

	// The token travels on the stream route and nowhere else: it opens one
	// video, and cannot be spent on browsing or on another stream's progress.
	for _, path := range []string{
		"/jellyfin/UserViews?tag=" + testToken,
		"/jellyfin/Items/" + movie.encode() + "?tag=" + testToken,
		"/jellyfin/UserItems/Resume?tag=" + testToken,
		"/jellyfin/Items/" + movie.encode() + "/PlaybackInfo?tag=" + testToken,
	} {
		if rec := f.do(http.MethodGet, path, "", "X-Nothing", "x"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: a tag token was accepted off the stream route: %d", path, rec.Code)
		}
	}
}

func TestPlaybackInfo(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	f.catalog.playlist = &stremio.PlaylistView{
		ContentTitle:   "The Shawshank Redemption",
		RuntimeSeconds: 142 * 60,
		Entries: []stremio.PlaylistEntry{
			{Index: 0, SlotPath: "stream:living-room:movie:tt0111161:0", Title: "The.Shawshank.Redemption.1994.2160p.UHD.BluRay.x265.HDR.DTS-HD.MA.5.1-GRP", Size: 40 << 30, Score: 90},
			{Index: 1, SlotPath: "stream:living-room:movie:tt0111161:1", Title: "The.Shawshank.Redemption.1994.1080p.BluRay.x264.DD5.1-GRP.mkv", Size: 12 << 30, Score: 70},
		},
	}
	var info playbackInfoResponse
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/Items/"+movie.encode()+"/PlaybackInfo?UserId=u", `{"DeviceProfile":{}}`), &info)
	if len(info.MediaSources) != 2 || info.ErrorCode != "" || info.PlaySessionID == "" {
		t.Fatalf("playback info: %+v", info)
	}
	best := info.MediaSources[0]
	// The id is a bare GUID; the token travels on Path and ETag instead,
	// because the player that fetches the stream sends no credentials of
	// its own. See streamURLFor.
	if best.ID != movie.source(0).encode() || !best.SupportsDirectPlay || best.SupportsTranscoding || best.SupportsDirectStream || best.SupportsProbing || !best.IsRemote || best.Protocol != "Http" || best.RequiredHTTPHeaders == nil {
		t.Fatalf("media source: %+v", best)
	}
	if !isGUIDShaped(best.ID) {
		t.Fatalf("media source id is not a GUID (Wholphin parses it as one): %q", best.ID)
	}
	if best.ETag != testToken || !best.IsRemote {
		t.Fatalf("token carriers: %+v", best)
	}
	if best.Path != "https://nzb.example/jellyfin/videos/"+movie.encode()+"/stream?api_key="+testToken+"&mediaSourceId="+movie.source(0).encode()+"&static=true" {
		t.Fatalf("stream URL: %q", best.Path)
	}
	if best.RunTimeTicks == nil || *best.RunTimeTicks != 142*60*ticksPerSecond || best.Size == nil || best.Bitrate == nil {
		t.Fatalf("source runtime/size: %+v", best)
	}
	if len(best.MediaStreams) < 2 || best.MediaStreams[0].Codec != "hevc" || *best.MediaStreams[0].Height != 2160 || best.MediaStreams[0].VideoRangeType != "HDR10" || best.MediaStreams[1].Type != "Audio" {
		t.Fatalf("media streams: %+v", best.MediaStreams)
	}
	if second := info.MediaSources[1]; second.MediaStreams[0].Codec != "h264" || second.Container != "mkv" {
		t.Fatalf("second source: %+v", second)
	}
	// Nothing found is the Jellyfin "no compatible stream" answer, not an
	// error status.
	f.catalog.playlist = nil
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode()+"/PlaybackInfo", ""), &info)
	if len(info.MediaSources) != 0 || info.ErrorCode != "NoCompatibleStream" {
		t.Fatalf("empty playback info: %+v", info)
	}
	f.catalog.playErr = errors.New("indexers down")
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode()+"/PlaybackInfo", ""), &info)
	if info.ErrorCode != "NoCompatibleStream" {
		t.Fatalf("failed playback info: %+v", info)
	}
	// A source id works as the item too.
	f.catalog.playErr = nil
	if rec := f.do(http.MethodGet, "/jellyfin/Items/"+movie.source(1).encode()+"/PlaybackInfo", ""); rec.Code != http.StatusOK {
		t.Fatalf("playback info by source id: %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, "/jellyfin/Items/"+viewID("tmdb.trending.movie")+"/PlaybackInfo", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("playback info for a folder: %d", rec.Code)
	}
}

// TestPlaybackInfoHonoursChosenSource covers switching release. A client
// re-requests PlaybackInfo naming the source it picked, and some clients then
// play mediaSources[0] without rereading the ids, so the pick has to come back
// at the front of the list or the switch silently plays the old release.
func TestPlaybackInfoHonoursChosenSource(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	f.catalog.playlist = &stremio.PlaylistView{
		Entries: []stremio.PlaylistEntry{
			{Index: 0, Title: "Release.00.mkv"},
			{Index: 1, Title: "Release.01.mkv"},
			{Index: 2, Title: "Release.02.mkv"},
		},
	}
	chosen := movie.source(1).encode()

	assertChosenFirst := func(what string, info playbackInfoResponse) {
		t.Helper()
		if len(info.MediaSources) != 3 {
			t.Fatalf("%s: the releases not picked must stay in the list, got %d", what, len(info.MediaSources))
		}
		if info.MediaSources[0].Name != "Release.01.mkv" || info.MediaSources[0].ID != chosen {
			t.Fatalf("%s: chosen source is not first: %+v", what, info.MediaSources[0])
		}
		// Everything else keeps its ranking behind the pick.
		if info.MediaSources[1].Name != "Release.00.mkv" || info.MediaSources[2].Name != "Release.02.mkv" {
			t.Fatalf("%s: ranking lost: %+v", what, info.MediaSources)
		}
	}

	var info playbackInfoResponse
	// In the posted DTO, which is where the Jellyfin SDK puts it.
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/Items/"+movie.encode()+"/PlaybackInfo", `{"DeviceProfile":{},"MediaSourceId":"`+chosen+`"}`), &info)
	assertChosenFirst("posted", info)

	// In the query.
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode()+"/PlaybackInfo?mediaSourceId="+chosen, ""), &info)
	assertChosenFirst("query", info)

	// Encoded in the path, for a client that navigates by source id.
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+movie.source(1).encode()+"/PlaybackInfo", ""), &info)
	assertChosenFirst("path", info)

	// A pick belonging to another title steers nothing: the ranking stands.
	other, _ := itemIDFor("movie", "tt0068646")
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode()+"/PlaybackInfo?mediaSourceId="+other.source(1).encode(), ""), &info)
	if info.MediaSources[0].Name != "Release.00.mkv" {
		t.Fatalf("a foreign media source id was honoured: %+v", info.MediaSources[0])
	}

	// A pick that ranked outside the cap survives it, or a switch would be
	// answered with the release it switched away from.
	f.maxSources = 2
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode()+"/PlaybackInfo?mediaSourceId="+movie.source(2).encode(), ""), &info)
	if len(info.MediaSources) != 2 || info.MediaSources[0].Name != "Release.02.mkv" {
		t.Fatalf("capped pick: %+v", info.MediaSources)
	}
}

// TestMediaSourceIDsAreGUIDs guards the shape of every media source id this
// layer hands out, wherever it is rendered. Clients read the field as a GUID
// even though the Jellyfin API types it as a string — Wholphin parses it with
// the SDK's toUUID() and crashes on anything else — so an id that stops being
// one is a client crash, not a cosmetic change.
func TestMediaSourceIDsAreGUIDs(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	f.catalog.playlist = &stremio.PlaylistView{
		Entries: []stremio.PlaylistEntry{
			{Index: 0, Title: "Release.00.mkv"},
			{Index: 1, Title: "Release.01.mkv"},
		},
	}

	check := func(what string, sources []mediaSource) {
		t.Helper()
		if len(sources) == 0 {
			t.Fatalf("%s: no media sources to check", what)
		}
		for _, src := range sources {
			if !isGUIDShaped(src.ID) {
				t.Fatalf("%s: media source id is not a GUID: %q", what, src.ID)
			}
			// The token must still reach the player, on both carriers.
			if src.ETag != testToken {
				t.Fatalf("%s: ETag does not carry the token: %+v", what, src)
			}
			if !strings.Contains(src.Path, "api_key="+testToken) {
				t.Fatalf("%s: Path does not carry the token: %q", what, src.Path)
			}
		}
	}

	var info playbackInfoResponse
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode()+"/PlaybackInfo", ""), &info)
	check("PlaybackInfo", info.MediaSources)

	// The stand-in source an item document carries before anything is cached.
	var item baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode(), ""), &item)
	check("item document", item.MediaSources)

	// And the full list it carries once the playlist is cached.
	f.catalog.cached = f.catalog.playlist
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode(), ""), &item)
	check("cached item document", item.MediaSources)
}

// TestPlaybackInfoCapsSources checks that a playlist longer than the
// configured limit is truncated to the limit, keeping the best-ranked
// entries (the playlist already arrives ranked best-first).
func TestPlaybackInfoCapsSources(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	entries := make([]stremio.PlaylistEntry, 25)
	for i := range entries {
		entries[i] = stremio.PlaylistEntry{Index: i, Title: fmt.Sprintf("Release.%02d.mkv", i)}
	}
	f.catalog.playlist = &stremio.PlaylistView{Entries: entries}

	var info playbackInfoResponse
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode()+"/PlaybackInfo", ""), &info)
	if len(info.MediaSources) != 20 {
		t.Fatalf("want 20 sources capped from 25, got %d", len(info.MediaSources))
	}
	for i, src := range info.MediaSources {
		if src.Name != fmt.Sprintf("Release.%02d.mkv", i) {
			t.Fatalf("source %d out of order: %+v", i, src)
		}
	}

	// A configured limit is honoured too.
	f.maxSources = 3
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode()+"/PlaybackInfo", ""), &info)
	if len(info.MediaSources) != 3 {
		t.Fatalf("want 3 sources with a configured limit, got %d", len(info.MediaSources))
	}
}

// TestResolveOnOpenAttachesFullPlaylist checks the opt-in behaviour SenPlayer
// needs: with JellyfinResolveOnOpen on, opening an unplayed movie's item page
// runs the search immediately and the item carries the full (capped) list,
// rather than the two cheap stand-in sources Infuse needs on list and detail
// documents to expose its version picker.
func TestResolveOnOpenAttachesFullPlaylist(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	entries := make([]stremio.PlaylistEntry, 3)
	for i := range entries {
		entries[i] = stremio.PlaylistEntry{Index: i, Title: fmt.Sprintf("Release.%02d.mkv", i)}
	}
	f.catalog.playlist = &stremio.PlaylistView{Entries: entries}

	// Off by default: two picker stand-ins, and nothing was searched to build them.
	var item baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items/"+movie.encode(), ""), &item)
	if len(item.MediaSources) != 2 || len(item.AlternateMediaSources) != 2 {
		t.Fatalf("resolve on open off: want 2 stand-in sources, got media=%d alternate=%d", len(item.MediaSources), len(item.AlternateMediaSources))
	}
	if item.MediaSourceCount == nil || *item.MediaSourceCount != 2 || item.EnableMediaSourceDisplay == nil || !*item.EnableMediaSourceDisplay {
		t.Fatalf("stand-in version markers: count=%v display=%v", item.MediaSourceCount, item.EnableMediaSourceDisplay)
	}
	if calls := f.catalog.playlistCalls; calls != 0 {
		t.Fatalf("resolve on open off: Playlist called %d times, want 0", calls)
	}

	// On: the same route resolves and attaches the full list.
	f.resolveOnOpen = true
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items/"+movie.encode(), ""), &item)
	if len(item.MediaSources) != 3 || len(item.AlternateMediaSources) != 3 {
		t.Fatalf("resolve on open: want 3 sources, got media=%d alternate=%d", len(item.MediaSources), len(item.AlternateMediaSources))
	}
	if item.MediaSources[0].Name != "Release.00.mkv" {
		t.Fatalf("resolve on open: sources out of order: %+v", item.MediaSources)
	}
	if item.MediaSourceCount == nil || *item.MediaSourceCount != 3 || item.EnableMediaSourceDisplay == nil || !*item.EnableMediaSourceDisplay {
		t.Fatalf("multi-source version markers: count=%v display=%v", item.MediaSourceCount, item.EnableMediaSourceDisplay)
	}
	if calls := f.catalog.playlistCalls; calls != 1 {
		t.Fatalf("resolve on open: Playlist called %d times, want 1", calls)
	}
}

// TestResolveOnOpenSkipsListsAndCache checks that resolving on open only
// fires on the single-item route, and only when the playlist is not already
// cached — a list page would otherwise cost one search per row.
func TestResolveOnOpenSkipsListsAndCache(t *testing.T) {
	f := newFixture()
	f.resolveOnOpen = true
	movie, _ := itemIDFor("movie", "tt0111161")
	f.catalog.playlist = &stremio.PlaylistView{Entries: []stremio.PlaylistEntry{{Index: 0, Title: "Release.mkv"}}}

	// A library listing never triggers a search, however many rows it has.
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?ParentId="+viewID("tmdb.trending.movie"), ""), new(queryResult))
	if calls := f.catalog.playlistCalls; calls != 0 {
		t.Fatalf("listing: Playlist called %d times, want 0", calls)
	}

	// An already-cached playlist is not searched again.
	f.catalog.cached = f.catalog.playlist
	var item baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items/"+movie.encode(), ""), &item)
	if calls := f.catalog.playlistCalls; calls != 0 {
		t.Fatalf("cached playlist: Playlist called %d times, want 0", calls)
	}
	if len(item.MediaSources) != 1 {
		t.Fatalf("cached playlist: want 1 source from the cache, got %d", len(item.MediaSources))
	}
}

func TestStreamServesTheSlotAndRewritesFailover(t *testing.T) {
	f := newFixture()
	series, _ := itemIDFor("series", "tt0903747")
	ep := series.episode(1, 2)
	stream := &auth.Stream{Username: "living-room", Token: testToken}
	rec := f.do(http.MethodGet, "/jellyfin/Videos/"+ep.encode()+"/stream.mkv?Static=true&MediaSourceId="+ep.source(1).encode(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("stream: %d %s", rec.Code, rec.Body.String())
	}
	want := stremio.SlotPathFor(stream, "series", "tt0903747:1:2", 1)
	if len(f.catalog.served) != 1 || f.catalog.served[0].slotPath != want {
		t.Fatalf("served %+v, want %s", f.catalog.served, want)
	}
	opts := f.catalog.served[0].opts
	if !opts.FailWithStatus || opts.SlotLocation == nil {
		t.Fatalf("play options: %+v", opts)
	}
	// A failover to slot 3 lands back on this route with the next source
	// and the token carried in the query, since the header may not
	// survive the client's redirect.
	loc, err := url.Parse(opts.SlotLocation(stremio.SlotPathFor(stream, "series", "tt0903747:1:2", 3)))
	if err != nil {
		t.Fatal(err)
	}
	if loc.Path != "/jellyfin/videos/"+ep.encode()+"/stream" {
		t.Fatalf("redirect path %q", loc.Path)
	}
	q := loc.Query()
	// The client sent MediaSourceId; the redirect must carry exactly one
	// media source, under the casing this layer writes.
	if len(q["MediaSourceId"]) != 0 {
		t.Fatalf("redirect kept the client's casing too: %v", q)
	}
	if q.Get("mediaSourceId") != ep.source(3).encode() || q.Get("Static") != "true" || q.Get("api_key") != testToken {
		t.Fatalf("redirect query %v", q)
	}
	// The redirect target authenticates by that query alone.
	rec = f.do(http.MethodGet, loc.String(), "", "X-None", "")
	if rec.Code != http.StatusOK || f.catalog.served[1].slotPath != stremio.SlotPathFor(stream, "series", "tt0903747:1:2", 3) {
		t.Fatalf("redirected stream: %d served %+v", rec.Code, f.catalog.served)
	}
	// A media source of another item does not steer the slot.
	other, _ := itemIDFor("movie", "tt0111161")
	f.do(http.MethodGet, "/jellyfin/Videos/"+ep.encode()+"/stream?mediaSourceId="+other.source(2).encode()+"."+testToken, "")
	if got := f.catalog.served[2].slotPath; got != stremio.SlotPathFor(stream, "series", "tt0903747:1:2", 0) {
		t.Fatalf("foreign media source steered to %s", got)
	}
	if rec := f.do(http.MethodGet, "/jellyfin/Videos/"+ep.encode()+"/main.m3u8", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("hls form: %d", rec.Code)
	}
}

func TestProgressResumeAndPlayed(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	id := movie.encode()
	report := func(event string, ticks int64, paused bool) {
		body := fmt.Sprintf(`{"ItemId":"%s","MediaSourceId":"%s","PositionTicks":%d,"IsPaused":%v,"PlaySessionId":"ps"}`, id, movie.source(0).encode(), ticks, paused)
		if rec := f.do(http.MethodPost, "/jellyfin/Sessions/Playing"+event, body); rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d", event, rec.Code)
		}
	}
	report("", 0, false)
	if st, ok := f.play.Get("living-room", id); !ok || st.PlayCount != 1 || st.RuntimeTicks != 142*60*ticksPerSecond || st.ContentID != "tt0111161" {
		t.Fatalf("after start: %+v %v", st, ok)
	}
	// Ticks are debounced: the first writes, the next within the interval
	// does not, a pause always does.
	report("/Progress", 10*ticksPerSecond, false)
	report("/Progress", 20*ticksPerSecond, false)
	if st, _ := f.play.Get("living-room", id); st.PositionTicks != 10*ticksPerSecond {
		t.Fatalf("tick debounce: position %d", st.PositionTicks/ticksPerSecond)
	}
	report("/Progress", 30*ticksPerSecond, true)
	if st, _ := f.play.Get("living-room", id); st.PositionTicks != 30*ticksPerSecond {
		t.Fatalf("pause write: position %d", st.PositionTicks/ticksPerSecond)
	}
	report("/Stopped", 40*60*ticksPerSecond, false)

	// Resume lists it with its position and percentage.
	var result queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items/Resume?Limit=12&MediaTypes=Video", ""), &result)
	if len(result.Items) != 1 || result.Items[0].ID != id || result.Items[0].UserData.PlaybackPositionTicks != 40*60*ticksPerSecond {
		t.Fatalf("resume: %+v", result.Items)
	}
	if pct := result.Items[0].UserData.PlayedPercentage; pct == nil || *pct < 28 || *pct > 29 {
		t.Fatalf("played percentage: %v", pct)
	}
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?Filters=IsResumable&Recursive=true", ""), &result)
	if len(result.Items) != 1 {
		t.Fatalf("IsResumable filter: %d", len(result.Items))
	}

	// Past 90% the item is played and leaves Resume.
	report("/Stopped", 135*60*ticksPerSecond, false)
	if st, _ := f.play.Get("living-room", id); !st.Played || st.PositionTicks != 0 {
		t.Fatalf("watched through: %+v", st)
	}
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/UserItems/Resume", ""), &result)
	if len(result.Items) != 0 {
		t.Fatalf("played item still resumable")
	}
	var ud userData
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/UserItems/"+id+"/UserData", ""), &ud)
	if !ud.Played || ud.PlayCount != 1 {
		t.Fatalf("user data: %+v", ud)
	}
	// Explicit marks, both route forms.
	decodeInto(t, f.do(http.MethodDelete, "/jellyfin/Users/u/PlayedItems/"+id, ""), &ud)
	if ud.Played {
		t.Fatalf("unmark did not clear")
	}
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+id, ""), &ud)
	if !ud.Played {
		t.Fatalf("mark did not set")
	}
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserItems/"+id+"/UserData", `{"Played":false,"PlaybackPositionTicks":600000000}`), &ud)
	if ud.Played || ud.PlaybackPositionTicks != 60*ticksPerSecond {
		t.Fatalf("user data update: %+v", ud)
	}
	// The legacy query-parameter form reports too.
	rec := f.do(http.MethodPost, "/jellyfin/Users/u/PlayingItems/"+id+"/Progress?PositionTicks=900000000&IsPaused=true", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("legacy progress: %d", rec.Code)
	}
	if st, _ := f.play.Get("living-room", id); st.PositionTicks != 90*ticksPerSecond {
		t.Fatalf("legacy progress position %d", st.PositionTicks/ticksPerSecond)
	}
	// Episode progress shows on the episode listing.
	series, _ := itemIDFor("series", "tt0903747")
	ep := series.episode(1, 1)
	epBody := fmt.Sprintf(`{"ItemId":"%s","PositionTicks":%d}`, ep.encode(), 20*60*ticksPerSecond)
	f.do(http.MethodPost, "/jellyfin/Sessions/Playing/Stopped", epBody)
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Shows/"+series.encode()+"/Episodes?Season=1", ""), &result)
	if result.Items[0].UserData.PlaybackPositionTicks != 20*60*ticksPerSecond || result.Items[1].UserData.PlaybackPositionTicks != 0 {
		t.Fatalf("episode user data: %+v %+v", result.Items[0].UserData, result.Items[1].UserData)
	}
	// Per-stream: a different stream sees none of it, while this one still does.
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/UserItems/Resume", "", "X-Emby-Token", testToken), &result)
	if len(result.Items) == 0 {
		t.Fatalf("own stream lost its progress")
	}
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/UserItems/Resume", "", "X-Emby-Token", testOtherToken), &result)
	if len(result.Items) != 0 {
		t.Fatalf("another stream saw this one's progress")
	}
}

func TestPlayedThresholdUsesProbedDuration(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	// The metadata says 142 min, the candidate that played says 100.
	f.catalog.cached = &stremio.PlaylistView{RuntimeSeconds: 142 * 60, Entries: []stremio.PlaylistEntry{{Caps: &releaseCaps}}}
	body := fmt.Sprintf(`{"ItemId":"%s","PositionTicks":%d}`, movie.encode(), 92*60*ticksPerSecond)
	f.do(http.MethodPost, "/jellyfin/Sessions/Playing/Stopped", body)
	if st, _ := f.play.Get("living-room", movie.encode()); !st.Played || st.RuntimeTicks != 100*60*ticksPerSecond {
		t.Fatalf("probed duration not used: %+v", st)
	}
}

func TestStubsAnswerClients(t *testing.T) {
	f := newFixture()
	checks := []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/jellyfin/System/Info", http.StatusOK},
		{http.MethodGet, "/jellyfin/Users/Me", http.StatusOK},
		{http.MethodGet, "/jellyfin/Users/abc", http.StatusOK},
		{http.MethodGet, "/jellyfin/Sessions", http.StatusOK},
		{http.MethodPost, "/jellyfin/Sessions/Capabilities/Full", http.StatusNoContent},
		{http.MethodPost, "/jellyfin/Sessions/Logout", http.StatusNoContent},
		{http.MethodGet, "/jellyfin/DisplayPreferences/usersettings?client=emby&userId=u", http.StatusOK},
		{http.MethodPost, "/jellyfin/DisplayPreferences/usersettings?client=emby&userId=u", http.StatusNoContent},
		{http.MethodGet, "/jellyfin/Localization/Cultures", http.StatusOK},
		{http.MethodGet, "/jellyfin/Plugins", http.StatusOK},
		{http.MethodGet, "/jellyfin/ScheduledTasks", http.StatusOK},
		{http.MethodGet, "/jellyfin/Shows/NextUp?UserId=u", http.StatusOK},
		{http.MethodGet, "/jellyfin/Genres", http.StatusOK},
		{http.MethodGet, "/jellyfin/MediaSegments/020101000000163ff900000000000007", http.StatusOK},
		{http.MethodGet, "/jellyfin/Items/Filters2", http.StatusOK},
		{http.MethodGet, "/jellyfin/Playback/BitrateTest?size=1000", http.StatusOK},
		{http.MethodGet, "/jellyfin/QuickConnect/Enabled", http.StatusOK},
		{http.MethodGet, "/jellyfin/Branding/Configuration", http.StatusOK},
		{http.MethodGet, "/jellyfin/socket", http.StatusNotFound},
		{http.MethodGet, "/jellyfin/no/such/route", http.StatusNotFound},
		{http.MethodGet, "/jellyfin/Users/u/Images/Primary", http.StatusNotFound},
	}
	for _, c := range checks {
		if rec := f.do(c.method, c.path, ""); rec.Code != c.status {
			t.Fatalf("%s %s: %d, want %d (%s)", c.method, c.path, rec.Code, c.status, rec.Body.String())
		}
	}
	var me userDto
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/Me", ""), &me)
	if me.Name != "living-room" || me.ID != userID("living-room") || me.Policy.IsAdministrator || !me.Policy.EnableMediaPlayback {
		t.Fatalf("me: %+v", me)
	}
}

func TestMediaBrowserHeaderParsing(t *testing.T) {
	id, ok := parseMediaBrowserHeader(`MediaBrowser Client="Jellyfin Android", Device="Pixel 8, black", DeviceId="abc", Version="2.6.1", Token="t1"`)
	if !ok || id.Client != "Jellyfin Android" || id.Device != "Pixel 8, black" || id.DeviceID != "abc" || id.Version != "2.6.1" || id.Token != "t1" {
		t.Fatalf("parsed %+v", id)
	}
	if _, ok := parseMediaBrowserHeader(`Bearer abc`); ok {
		t.Fatalf("bearer parsed as MediaBrowser")
	}
	if id, ok := parseMediaBrowserHeader(`Emby UserId="", Client="Infuse", Device="Apple TV", DeviceId="d", Version="7", Token=abc`); !ok || id.Token != "abc" || id.Client != "Infuse" {
		t.Fatalf("emby form: %+v %v", id, ok)
	}
}

func TestJellyfinTime(t *testing.T) {
	if got := jellyfinTime(time.Date(2024, 5, 1, 12, 30, 0, 0, time.UTC)); got != "2024-05-01T12:30:00.0000000Z" {
		t.Fatalf("time format %q", got)
	}
	if got := premiereDate("1994-09-23"); got != "1994-09-23T00:00:00.0000000Z" {
		t.Fatalf("date-only premiere %q", got)
	}
}
