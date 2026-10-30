package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"sort"
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
	// drop thins a bucket the way the addon does when a provider row has
	// no id it can use: skip → rows removed from the end of that bucket.
	drop         map[int]int
	catalogCalls []int
	// metaDelay holds each Meta call open, so peakMeta shows how many a
	// listing runs at once.
	metaDelay time.Duration
	inflight  int
	peakMeta  int
	// onMeta, when set, runs at the start of every Meta call, outside the
	// lock, with the call's context.
	onMeta func(context.Context)
	// genreLists replaces Genres' English lists, keyed by content type, as
	// a stream with another metadata language sees TMDB's.
	genreLists map[string][]stremio.Genre
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

// Genres is TMDB's list for the type; the movie and series lists share
// Comedy and Drama under different ids, as TMDB's do.
func (f *fakeCatalog) Genres(_ *auth.Stream, contentType string) ([]stremio.Genre, error) {
	if f.disabled {
		return nil, stremio.ErrMetadataDisabled
	}
	if list, ok := f.genreLists[contentType]; ok {
		return list, nil
	}
	if contentType == "movie" {
		return []stremio.Genre{{ID: 28, Name: "Action"}, {ID: 35, Name: "Comedy"}, {ID: 18, Name: "Drama"}}, nil
	}
	return []stremio.Genre{{ID: 10759, Name: "Action & Adventure"}, {ID: 35, Name: "Comedy"}, {ID: 18, Name: "Drama"}}, nil
}

func (f *fakeCatalog) SearchCatalogs(*auth.Stream) []stremio.CatalogDef {
	return []stremio.CatalogDef{
		{ID: "tmdb.search.movie", Type: "movie", Name: "Search Movies", Provider: "tmdb", Kind: "search", SupportsSearch: true},
		{ID: "tmdb.search.series", Type: "series", Name: "Search Series", Provider: "tmdb", Kind: "search", SupportsSearch: true},
		{ID: "kitsu.search.anime", Type: "anime", Name: "Search Anime", Provider: "kitsu", Kind: "search", SupportsSearch: true},
	}
}

func (f *fakeCatalog) Catalog(_ context.Context, _ *auth.Stream, catalogID, _, search string, skip int) ([]stremio.MetaPreview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if search != "" {
		f.searches = append(f.searches, catalogID+"="+search)
		return f.rows[catalogID], nil
	}
	f.catalogCalls = append(f.catalogCalls, skip)
	rows := f.rows[catalogID]
	if skip >= len(rows) {
		return nil, nil
	}
	rows = rows[skip:]
	if len(rows) > stremio.CatalogPageSize {
		rows = rows[:stremio.CatalogPageSize]
	}
	if n := f.drop[skip]; n > 0 && n <= len(rows) {
		rows = rows[:len(rows)-n]
	}
	return rows, nil
}

func (f *fakeCatalog) Meta(ctx context.Context, _ *auth.Stream, contentType, id string) (*stremio.MetaObject, error) {
	if f.onMeta != nil {
		f.onMeta(ctx)
	}
	f.mu.Lock()
	f.metaCalls++
	f.inflight++
	f.peakMeta = max(f.peakMeta, f.inflight)
	delay := f.metaDelay
	f.mu.Unlock()
	time.Sleep(delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inflight--
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
	gets  int
	// favErr fails every SetFavorite, as a database error would, after
	// counting it in favWrites.
	favErr    error
	favWrites int
	// playedErr fails every SetPlayed and SetPlayedMany, writing nothing, as
	// a rolled-back transaction would.
	playedErr error
	// lists counts ListForStream calls, each a read of the whole stream,
	// and prefixReads ListPrefix calls, each a read of one series.
	lists, prefixReads int
	// readErr fails every ListPrefix, as a failed query would; onRead runs
	// before each one, outside the lock.
	readErr error
	onRead  func()
}

func newFakePlaystate() *fakePlaystate {
	return &fakePlaystate{rows: map[string]persistence.JellyfinPlaystate{}}
}

func (p *fakePlaystate) Upsert(state persistence.JellyfinPlaystate) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.saves++
	// Like the store, an upsert never writes the favourite flag.
	key := state.StreamName + "|" + state.ItemID
	state.IsFavorite = p.rows[key].IsFavorite
	p.rows[key] = state
	return nil
}

func (p *fakePlaystate) Get(streamName, itemID string) (persistence.JellyfinPlaystate, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gets++
	st, ok := p.rows[streamName+"|"+itemID]
	return st, ok
}

// ListResume follows the store: newest first, then cut to limit.
func (p *fakePlaystate) ListResume(streamName string, limit int) []persistence.JellyfinPlaystate {
	var out []persistence.JellyfinPlaystate
	for _, st := range p.ListForStream(streamName) {
		if !st.Played && st.PositionTicks > 0 {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastPlayedAt.After(out[j].LastPlayedAt) })
	return out[:min(limit, len(out))]
}

func (p *fakePlaystate) ListForStream(streamName string) []persistence.JellyfinPlaystate {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lists++
	var out []persistence.JellyfinPlaystate
	for _, st := range p.rows {
		if st.StreamName == streamName {
			out = append(out, st)
		}
	}
	return out
}

// MarkPlayedMany follows the store: a played row is left as it was, the
// missing ones are created, and every row changed carries one timestamp.
func (p *fakePlaystate) MarkPlayedMany(streamName string, items []persistence.JellyfinPlaystate) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.playedErr != nil {
		return p.playedErr
	}
	now := time.Now()
	for _, item := range items {
		key := streamName + "|" + item.ItemID
		st, ok := p.rows[key]
		if st.Played {
			continue
		}
		if !ok {
			st = persistence.JellyfinPlaystate{StreamName: streamName, ItemID: item.ItemID, ContentType: item.ContentType, ContentID: item.ContentID}
		}
		st.Played, st.PositionTicks, st.LastPlayedAt = true, 0, now
		if st.PlayCount == 0 {
			st.PlayCount = 1
		}
		p.saves++
		p.rows[key] = st
	}
	return nil
}

// ListPrefix follows the store, failing with readErr as a failed query
// would, after running onRead.
func (p *fakePlaystate) ListPrefix(streamName, prefix string) ([]persistence.JellyfinPlaystate, error) {
	p.mu.Lock()
	onRead := p.onRead
	p.mu.Unlock()
	if onRead != nil {
		onRead()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prefixReads++
	if p.readErr != nil {
		return nil, p.readErr
	}
	var out []persistence.JellyfinPlaystate
	for _, st := range p.rows {
		if st.StreamName == streamName && strings.HasPrefix(st.ItemID, prefix) {
			out = append(out, st)
		}
	}
	return out, nil
}

// ClearPlayedPrefix follows the store: rows with progress or a played mark
// are cleared in place, nothing is created.
func (p *fakePlaystate) ClearPlayedPrefix(streamName, prefix string) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.playedErr != nil {
		return 0, p.playedErr
	}
	now := time.Now()
	var cleared int64
	for key, st := range p.rows {
		if st.StreamName == streamName && strings.HasPrefix(st.ItemID, prefix) && (st.Played || st.PositionTicks > 0) {
			st.Played, st.PositionTicks, st.LastPlayedAt = false, 0, now
			p.saves++
			cleared++
			p.rows[key] = st
		}
	}
	return cleared, nil
}

// SetPlayed follows the store: the position reset, a first play counted
// when marking, and the row dated now.
func (p *fakePlaystate) SetPlayed(streamName, itemID, contentType, contentID string, played bool) error {
	p.mu.Lock()
	err := p.playedErr
	p.mu.Unlock()
	if err != nil {
		return err
	}
	st, ok := p.Get(streamName, itemID)
	if !ok {
		st = persistence.JellyfinPlaystate{StreamName: streamName, ItemID: itemID, ContentType: contentType, ContentID: contentID}
	}
	st.Played = played
	st.PositionTicks = 0
	if played && st.PlayCount == 0 {
		st.PlayCount = 1
	}
	st.LastPlayedAt = time.Now()
	return p.Upsert(st)
}

// SetFavorite follows the store: marking writes the flag alone, creating a
// row if there is none; clearing only updates an existing row.
func (p *fakePlaystate) SetFavorite(streamName, itemID, contentType, contentID string, favorite bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.favWrites++
	if p.favErr != nil {
		return p.favErr
	}
	key := streamName + "|" + itemID
	st, ok := p.rows[key]
	if !ok {
		if !favorite {
			return nil
		}
		st = persistence.JellyfinPlaystate{StreamName: streamName, ItemID: itemID, ContentType: contentType, ContentID: contentID}
	}
	st.IsFavorite = favorite
	p.rows[key] = st
	return nil
}

func (p *fakePlaystate) ListFavorites(streamName string) []persistence.JellyfinPlaystate {
	var out []persistence.JellyfinPlaystate
	for _, st := range p.ListForStream(streamName) {
		if st.IsFavorite {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ItemID < out[j].ItemID })
	return out
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
			{ID: "tt0903747:1:1", Title: "Pilot", Season: 1, Episode: 1, Released: "2008-01-20T00:00:00.000Z", Thumbnail: cdn + "/bb-s1e1.jpg", Runtime: 58},
			{ID: "tt0903747:1:2", Title: "Cat's in the Bag...", Season: 1, Episode: 2},
			{ID: "tt0903747:2:1", Title: "Seven Thirty-Seven", Season: 2, Episode: 1, Released: "2099-01-01T00:00:00.000Z"},
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
			"tmdb.search.movie":    []stremio.MetaPreview{{ID: "tt0111161", Type: "movie", Name: "The Shawshank Redemption", ReleaseInfo: "1994", IMDBRating: "9.3"}},
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

// A provider bucket comes back short whenever the addon drops rows it cannot
// identify. That is not the end of the catalog: the client must be told to
// keep paging, and the addon's positions must be kept so the next page
// neither repeats nor skips rows.
func TestShortBucketDoesNotEndCatalog(t *testing.T) {
	f := newFixture()
	f.catalog.rows["tmdb.trending.movie"] = previews("movie", "tt", 120)
	f.catalog.drop = map[int]int{0: 2, 40: 1}
	view := viewID("tmdb.trending.movie")
	var result queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items?ParentId="+view+"&StartIndex=0&Limit=100", ""), &result)
	if len(result.Items) != 97 || result.Items[0].Name != "Title 1" || result.Items[96].Name != "Title 100" {
		t.Fatalf("first page: %d items (want 97: 18+20+19+20+20), first %q last %q", len(result.Items), result.Items[0].Name, result.Items[len(result.Items)-1].Name)
	}
	if result.TotalRecordCount <= 100 {
		t.Fatalf("a thinned page must still read as more to come: total %d", result.TotalRecordCount)
	}
	// The client steps by its own page size, so the next page starts at the
	// addon's position 100, not at row 97: no repeat, no gap.
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items?ParentId="+view+"&StartIndex=100&Limit=100", ""), &result)
	if len(result.Items) != 20 || result.Items[0].Name != "Title 101" || result.Items[19].Name != "Title 120" {
		t.Fatalf("second page: %d items, first %q", len(result.Items), result.Items[0].Name)
	}
	if result.TotalRecordCount != 120 {
		t.Fatalf("an empty bucket ends the catalog with an exact total: got %d", result.TotalRecordCount)
	}
	// A short LAST bucket is probed, so a true last page is exact too.
	f.catalog.drop = map[int]int{100: 3}
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items?ParentId="+view+"&StartIndex=100&Limit=20", ""), &result)
	if len(result.Items) != 17 || result.TotalRecordCount != 117 {
		t.Fatalf("short last page: %d items, total %d", len(result.Items), result.TotalRecordCount)
	}
	// Page-sized requests walk the same rows without duplicates.
	f.catalog.drop = map[int]int{0: 2, 40: 1}
	seen := map[string]bool{}
	for start := 0; start < 120; start += 30 {
		decodeInto(t, f.do(http.MethodGet, fmt.Sprintf("/jellyfin/Users/u/Items?ParentId=%s&StartIndex=%d&Limit=30", view, start), ""), &result)
		for _, item := range result.Items {
			if seen[item.Name] {
				t.Fatalf("row %q served twice", item.Name)
			}
			seen[item.Name] = true
		}
	}
	if len(seen) != 117 {
		t.Fatalf("walked %d distinct rows, want 117", len(seen))
	}
}

// A client that syncs a library pages until the total says stop. TMDB's
// listings run to 10,000 rows, so the library has to end well before the
// catalog does.
func TestLibraryEndsAtMaxLibraryRows(t *testing.T) {
	f := newFixture()
	f.catalog.rows["tmdb.trending.movie"] = previews("movie", "tt", maxLibraryRows+100)
	view := viewID("tmdb.trending.movie")
	var result queryResult
	decodeInto(t, f.do(http.MethodGet, fmt.Sprintf("/jellyfin/Items?ParentId=%s&StartIndex=%d&Limit=100", view, maxLibraryRows-100), ""), &result)
	if len(result.Items) != 100 || result.TotalRecordCount != maxLibraryRows {
		t.Fatalf("last page: %d items, total %d, want 100/%d", len(result.Items), result.TotalRecordCount, maxLibraryRows)
	}
	// A window that straddles the cap is cut at it, and nothing past the cap
	// is fetched — not even the probe for whether the catalog goes on.
	f.catalog.catalogCalls = nil
	decodeInto(t, f.do(http.MethodGet, fmt.Sprintf("/jellyfin/Items?ParentId=%s&StartIndex=%d&Limit=100", view, maxLibraryRows-40), ""), &result)
	if len(result.Items) != 40 || result.TotalRecordCount != maxLibraryRows {
		t.Fatalf("straddling page: %d items, total %d, want 40/%d", len(result.Items), result.TotalRecordCount, maxLibraryRows)
	}
	for _, skip := range f.catalog.catalogCalls {
		if skip >= maxLibraryRows {
			t.Fatalf("fetched skip=%d past the cap", skip)
		}
	}
	f.catalog.catalogCalls = nil
	decodeInto(t, f.do(http.MethodGet, fmt.Sprintf("/jellyfin/Items?ParentId=%s&StartIndex=%d&Limit=100", view, maxLibraryRows), ""), &result)
	if len(result.Items) != 0 || len(f.catalog.catalogCalls) != 0 {
		t.Fatalf("past the cap: %d items, %d catalog calls", len(result.Items), len(f.catalog.catalogCalls))
	}
}

// Items with no folder and no recursion is the root folder, whose children
// are the libraries. UHF finds its libraries this way.
func TestRootItemsListsLibraries(t *testing.T) {
	f := newFixture()
	var views, root queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/UserViews", ""), &views)
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items", ""), &root)
	if len(root.Items) == 0 || len(root.Items) != len(views.Items) || root.TotalRecordCount != len(views.Items) {
		t.Fatalf("root items: %d (total %d), want the %d libraries", len(root.Items), root.TotalRecordCount, len(views.Items))
	}
	for i, item := range root.Items {
		if item.Type != "CollectionFolder" || item.ID != views.Items[i].ID {
			t.Fatalf("root item %d: %s %q, want library %q", i, item.Type, item.ID, views.Items[i].ID)
		}
	}
	// The configuration listing names the same libraries.
	var folders []virtualFolder
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Library/VirtualFolders", ""), &folders)
	if len(folders) != len(views.Items) {
		t.Fatalf("virtual folders: %d, want %d", len(folders), len(views.Items))
	}
	for i, folder := range folders {
		if folder.ItemID != views.Items[i].ID || folder.Name != views.Items[i].Name || folder.CollectionType == "" || len(folder.Locations) != 1 {
			t.Fatalf("virtual folder %d: %+v", i, folder)
		}
	}
	// Recursive is still everything across the libraries.
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items?Recursive=true&IncludeItemTypes=Movie", ""), &root)
	if len(root.Items) == 0 || root.Items[0].Type != "Movie" {
		t.Fatalf("recursive root items: %+v", root.Items)
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
	// A browse row carries the two-slot version signal Infuse's Direct Mode
	// reads from the list document (it never asks PlaybackInfo): two
	// placeholders, a count of two, and the display flag.
	if row := result.Items[0]; len(row.MediaSources) != 2 || len(row.AlternateMediaSources) != 2 || row.MediaSourceCount == nil || *row.MediaSourceCount != 2 || row.EnableMediaSourceDisplay == nil || !*row.EnableMediaSourceDisplay {
		t.Fatalf("browse row must carry two placeholder media sources: %+v", row)
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

func TestProfileOwnedCatalogViewPages(t *testing.T) {
	f := newFixture()
	def := stremio.CatalogDef{ID: "external.profile.front-row", Type: "movie", Name: "Profile: Front Row", Provider: "external", SupportsSkip: true}
	f.catalog.catalogs = append(f.catalog.catalogs, def)
	f.catalog.rows[def.ID] = previews("movie", "tt", 2)

	view := viewID(def.ID)
	var views queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/UserViews", ""), &views)
	found := false
	for _, item := range views.Items {
		found = found || (item.ID == view && item.Name == def.Name)
	}
	if !found {
		t.Fatalf("external profile view missing: %+v", views.Items)
	}
	var page queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?ParentId="+view+"&Limit=20", ""), &page)
	if len(page.Items) != 2 || page.Items[0].ParentID != view {
		t.Fatalf("external catalog page: %+v", page)
	}
}

func TestExternalCatalogShortFinalPageClosesPaging(t *testing.T) {
	f := newFixture()
	def := stremio.CatalogDef{ID: "external.complete-list", Type: "movie", Name: "Complete List", Provider: "external", SupportsSkip: true}
	f.catalog.catalogs = append(f.catalog.catalogs, def)
	f.catalog.rows[def.ID] = previews("movie", "tt", 45)

	var result queryResult
	view := viewID(def.ID)
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?ParentId="+view+"&StartIndex=40&Limit=20", ""), &result)
	if len(result.Items) != 5 || result.Items[0].Name != "Title 41" {
		t.Fatalf("external final rows: %+v", result.Items)
	}
	if result.TotalRecordCount != 45 {
		t.Fatalf("external short page total = %d, want 45", result.TotalRecordCount)
	}
}

func TestInfuseLatestReturnsFullExternalCatalog(t *testing.T) {
	f := newFixture()
	def := stremio.CatalogDef{ID: "external.infuse-list", Type: "movie", Name: "Infuse List", Provider: "external", SupportsSkip: true}
	f.catalog.catalogs = append(f.catalog.catalogs, def)
	f.catalog.rows[def.ID] = previews("movie", "tt", 45)

	var latest []*baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/Latest?ParentId="+viewID(def.ID)+"&Limit=20", "",
		"Authorization", `MediaBrowser Client="Infuse", Device="Apple TV", DeviceId="dev-1", Version="8.5.3", Token="`+testToken+`"`,
		"User-Agent", "Infuse-Direct/8.5.3"), &latest)
	if len(latest) != 45 || latest[44].Name != "Title 45" {
		t.Fatalf("Infuse external latest = %d rows, want complete 45", len(latest))
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
	// A tagless request is answered too, from what this item's document
	// advertised. Clients are not consistent about quoting tags: Fladder's URL
	// builder passes one for Backdrop and omits it for Primary, Thumb and
	// Logo, so refusing these left every poster it drew a blank rectangle.
	//
	// The trade is that provider artwork for an item whose document has been
	// rendered is now anonymously fetchable by item id, and item ids are
	// derived from tmdb/imdb ids rather than being secret. What is exposed is
	// public CDN artwork relayed by this server — not a library listing, and
	// not anything about who watched what.
	anonNoTag := f.do(http.MethodGet, "/jellyfin/Items/"+item.ID+"/Images/Primary", "", "X-Nothing", "x")
	if anonNoTag.Code != http.StatusOK || anonNoTag.Body.String() != rec.Body.String() {
		t.Fatalf("anonymous image without a tag: %d %s", anonNoTag.Code, anonNoTag.Body.String())
	}
	// An item this process never rendered stays a missing image, not a 401 and
	// not a metadata lookup on some arbitrary stream's behalf.
	other, _ := itemIDFor("movie", "tt0000009")
	if miss := f.do(http.MethodGet, "/jellyfin/Items/"+other.encode()+"/Images/Primary", "", "X-Nothing", "x"); miss.Code != http.StatusNotFound {
		t.Fatalf("unknown item image: %d", miss.Code)
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
	// A missing image is a miss, not a gateway failure: 404, once the fallback
	// (pointed at a 404 path in tests) has been tried too.
	if rec := f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode()+"/Images/Primary", ""); rec.Code != http.StatusNotFound {
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
	// Infuse reads a series' own MediaSourceCount to decide whether its
	// quick-play button gets a version-picker chevron, even though that
	// button plays a specific episode — this addon has no per-episode resume
	// state, so the series item stands in with the first numbered episode's
	// sources (S1E1 here, "Pilot"). Like a never-opened movie, a never-opened
	// series gets one placeholder source and no MediaSourceCount yet (a lone
	// stand-in must not advertise a version count — see
	// TestResolveOnOpenAttachesFullPlaylist); the point of this fix is that
	// MediaSources is no longer empty, and once that episode's real playlist
	// is cached (resolve-on-open, or after a play), the count follows exactly
	// as it does for a movie.
	if len(item.MediaSources) == 0 {
		t.Fatalf("series item carries no media sources for its quick-play button: %+v", item)
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
	// The pilot carries its own 58-minute runtime; the second episode has
	// none and inherits the series average of 49.
	if got := episodes.Items[0].RunTimeTicks; got == nil || *got != 58*60*ticksPerSecond {
		t.Fatalf("episode 1 runtime = %v, want the episode's own 58 min", got)
	}
	if got := episodes.Items[1].RunTimeTicks; got == nil || *got != 49*60*ticksPerSecond {
		t.Fatalf("episode 2 runtime = %v, want the series average 49 min", got)
	}
	if episodes.Items[0].LocationType != "FileSystem" {
		t.Fatalf("aired episode LocationType = %q, want FileSystem", episodes.Items[0].LocationType)
	}
	// Season 2's only episode airs in 2099: listed, but Virtual.
	var future queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Shows/"+series.encode()+"/Episodes?SeasonId="+seasons.Items[1].ID, ""), &future)
	if len(future.Items) != 1 || future.Items[0].LocationType != "Virtual" {
		t.Fatalf("unaired episode: %+v, want LocationType Virtual", future.Items)
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

// TestSeriesItemGetsVersionPickerFromCachedPlaylist confirms the fix end to
// end: once the series' first episode has a real cached playlist (however it
// got there — resolve-on-open or a prior play), the series-level item itself
// carries the multi-source signal Infuse's quick-play chevron reads, matching
// a movie in the same state (TestResolveOnOpenAttachesFullPlaylist).
func TestSeriesItemGetsVersionPickerFromCachedPlaylist(t *testing.T) {
	f := newFixture()
	series, _ := itemIDFor("series", "tt0903747")
	entries := make([]stremio.PlaylistEntry, 3)
	for i := range entries {
		entries[i] = stremio.PlaylistEntry{Index: i, Title: fmt.Sprintf("Release.%02d.mkv", i)}
	}
	f.catalog.cached = &stremio.PlaylistView{Entries: entries}

	var item baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+series.encode(), ""), &item)
	if item.MediaSourceCount == nil || *item.MediaSourceCount != 3 || item.EnableMediaSourceDisplay == nil || !*item.EnableMediaSourceDisplay {
		t.Fatalf("series version markers: count=%v display=%v", item.MediaSourceCount, item.EnableMediaSourceDisplay)
	}
	if len(item.MediaSources) != 3 || item.MediaSources[0].Name != "Release.00.mkv" {
		t.Fatalf("series media sources: %+v", item.MediaSources)
	}
}

// TestSeriesResolveOnOpenAttachesFullPlaylist is the series counterpart of
// TestResolveOnOpenAttachesFullPlaylist: with JellyfinResolveOnOpen on,
// opening an unplayed series' item page must get the same eager, full
// candidate list a movie gets, on its first view — not only once someone has
// separately opened its first episode before. resolveOnOpen's own guard
// rejects a kindSeries id directly, so this pins that itemByID's kindSeries
// case calls it with the representative episode's id instead.
func TestSeriesResolveOnOpenAttachesFullPlaylist(t *testing.T) {
	f := newFixture()
	series, _ := itemIDFor("series", "tt0903747")
	entries := make([]stremio.PlaylistEntry, 3)
	for i := range entries {
		entries[i] = stremio.PlaylistEntry{Index: i, Title: fmt.Sprintf("Release.%02d.mkv", i)}
	}
	f.catalog.playlist = &stremio.PlaylistView{Entries: entries}

	var item baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items/"+series.encode(), ""), &item)
	if len(item.MediaSources) != 1 || item.MediaSourceCount != nil {
		t.Fatalf("resolve on open off: want 1 stand-in source and no count, got media=%d count=%v", len(item.MediaSources), item.MediaSourceCount)
	}
	if calls := f.catalog.playlistCalls; calls != 0 {
		t.Fatalf("resolve on open off: Playlist called %d times, want 0", calls)
	}

	f.resolveOnOpen = true
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items/"+series.encode(), ""), &item)
	if len(item.MediaSources) != 3 || item.MediaSources[0].Name != "Release.00.mkv" {
		t.Fatalf("resolve on open: want 3 sources in order, got %+v", item.MediaSources)
	}
	if item.MediaSourceCount == nil || *item.MediaSourceCount != 3 || item.EnableMediaSourceDisplay == nil || !*item.EnableMediaSourceDisplay {
		t.Fatalf("series version markers: count=%v display=%v", item.MediaSourceCount, item.EnableMediaSourceDisplay)
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
	var rows queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items?SearchTerm=shaw&Recursive=true&IncludeItemTypes=Movie", ""), &rows)
	if got := f.catalog.searchSet(); !reflect.DeepEqual(got, map[string]bool{"tmdb.search.movie=shaw": true}) {
		t.Fatalf("movie-only searches run: %v", got)
	}
	// A list row carries the year and rating the grid badges show.
	if len(rows.Items) != 1 || rows.Items[0].ProductionYear == nil || *rows.Items[0].ProductionYear != 1994 || rows.Items[0].CommunityRating == nil || *rows.Items[0].CommunityRating != 9.3 {
		t.Fatalf("row year/rating: %+v", rows.Items)
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

	// Turned off: one stand-in naming slot 0, and nothing was searched to
	// build it. One, not several — a second stand-in would claim a version
	// that may not exist and put a picker on screen with nothing real in it.
	var item baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items/"+movie.encode(), ""), &item)
	if len(item.MediaSources) != 1 || len(item.AlternateMediaSources) != 1 {
		t.Fatalf("resolve on open off: want 1 stand-in source, got media=%d alternate=%d", len(item.MediaSources), len(item.AlternateMediaSources))
	}
	// No count is advertised for a single source: the count is what a client
	// reads to decide there is a choice to offer.
	if item.MediaSourceCount != nil {
		t.Fatalf("a lone stand-in must not advertise a version count: %v", item.MediaSourceCount)
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

// TestFavorites covers what clients confirm a favourite by — the UserData
// answered and the item read back — on every route form and favourite kind,
// and the IsFavorite listing built from them.
func TestFavorites(t *testing.T) {
	f := newFixture()
	f.resolveOnOpen = true
	movie, _ := itemIDFor("movie", "tt0111161")
	series, _ := itemIDFor("series", "tt0903747")
	season, episode := series.season(1), series.episode(1, 1)

	mark := func(method, path, body string, want bool, headers ...string) {
		t.Helper()
		var ud userData
		decodeInto(t, f.do(method, "/jellyfin"+path, body, headers...), &ud)
		if ud.IsFavorite != want {
			t.Fatalf("%s %s: IsFavorite %v, want %v", method, path, ud.IsFavorite, want)
		}
	}
	readBack := func(id itemID) bool {
		t.Helper()
		var item baseItem
		decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+id.encode(), ""), &item)
		return item.UserData != nil && item.UserData.IsFavorite
	}
	list := func(query string, headers ...string) queryResult {
		t.Helper()
		var result queryResult
		decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?Recursive=true&"+query, "", headers...), &result)
		return result
	}

	if got := list("Filters=IsFavorite"); got.TotalRecordCount != 0 || got.Items == nil || len(got.Items) != 0 {
		t.Fatalf("no favourites yet: %+v", got)
	}

	// Both route forms, each idempotent, and the newer UserData body.
	mark(http.MethodPost, "/UserFavoriteItems/"+movie.encode(), "", true)
	mark(http.MethodPost, "/UserFavoriteItems/"+movie.encode(), "", true)
	mark(http.MethodPost, "/Users/u/FavoriteItems/"+series.encode(), "", true)
	mark(http.MethodPost, "/UserFavoriteItems/"+season.encode(), "", true)
	mark(http.MethodPost, "/UserItems/"+episode.encode()+"/UserData", `{"IsFavorite":true}`, true)
	for _, id := range []itemID{movie, series, season, episode} {
		if !readBack(id) {
			t.Fatalf("item %v does not read back as a favourite", id.Kind)
		}
	}
	var seasons, episodes queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Shows/"+series.encode()+"/Seasons", ""), &seasons)
	for _, item := range seasons.Items {
		if want := item.ID == season.encode(); item.UserData.IsFavorite != want {
			t.Fatalf("season listing: %s IsFavorite %v", item.Name, item.UserData.IsFavorite)
		}
	}
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Shows/"+series.encode()+"/Episodes?Season=1", ""), &episodes)
	if !episodes.Items[0].UserData.IsFavorite || episodes.Items[1].UserData.IsFavorite {
		t.Fatalf("episode listing: %+v %+v", episodes.Items[0].UserData, episodes.Items[1].UserData)
	}

	// Playback rewrites the row, and must leave the favourite alone.
	progress := fmt.Sprintf(`{"ItemId":"%s","PositionTicks":%d}`, movie.encode(), 20*60*ticksPerSecond)
	f.do(http.MethodPost, "/jellyfin/Sessions/Playing/Stopped", progress)
	mark(http.MethodPost, "/UserPlayedItems/"+episode.encode(), "", true)
	if st, _ := f.play.Get("living-room", movie.encode()); !st.IsFavorite || st.PositionTicks != 20*60*ticksPerSecond {
		t.Fatalf("progress report lost the favourite: %+v", st)
	}

	// A listing renders from metadata and the cache, never searching, even
	// with resolve-on-open on and a series among the favourites.
	searches := f.catalog.playlistCalls
	all := list("Filters=IsFavorite")
	if f.catalog.playlistCalls != searches {
		t.Fatalf("favourites listing searched %d times", f.catalog.playlistCalls-searches)
	}
	if all.TotalRecordCount != 4 || len(all.Items) != 4 {
		t.Fatalf("all favourites: total %d, %d items", all.TotalRecordCount, len(all.Items))
	}
	for _, item := range all.Items {
		if item.UserData == nil || !item.UserData.IsFavorite {
			t.Fatalf("listed %s without its favourite flag", item.Name)
		}
	}
	for query, want := range map[string]int{
		"Filters=IsFavoriteOrLikes":                                    4,
		"isFavorite=true":                                              4,
		"Filters=IsFavorite&IncludeItemTypes=Movie":                    1,
		"isFavorite=true&IncludeItemTypes=Series,Season":               2,
		"Filters=IsFavorite&ParentId=" + viewID("tmdb.trending.movie"): 1,
	} {
		if got := list(query); got.TotalRecordCount != want || len(got.Items) != want {
			t.Fatalf("%s: total %d, %d items, want %d", query, got.TotalRecordCount, len(got.Items), want)
		}
	}
	paged := list("Filters=IsFavorite&StartIndex=1&Limit=2")
	if paged.TotalRecordCount != 4 || paged.StartIndex != 1 || len(paged.Items) != 2 || paged.Items[0].ID != all.Items[1].ID {
		t.Fatalf("paged: %+v", paged)
	}
	if past := list("Filters=IsFavorite&StartIndex=10"); past.TotalRecordCount != 4 || len(past.Items) != 0 {
		t.Fatalf("past the end: %+v", past)
	}

	// Per stream: another stream sees none of these, and its own changes on
	// the same item leave this stream's favourite in place.
	if other := list("Filters=IsFavorite", "X-Emby-Token", testOtherToken); other.TotalRecordCount != 0 {
		t.Fatalf("another stream sees %d favourites", other.TotalRecordCount)
	}
	mark(http.MethodPost, "/UserFavoriteItems/"+movie.encode(), "", true, "X-Emby-Token", testOtherToken)
	mark(http.MethodDelete, "/UserFavoriteItems/"+movie.encode(), "", false, "X-Emby-Token", testOtherToken)
	if !readBack(movie) {
		t.Fatal("another stream's unfavourite cleared this stream's")
	}

	// Clearing, by item or by one of its media sources, and idempotently.
	mark(http.MethodDelete, "/UserFavoriteItems/"+movie.source(0).encode(), "", false)
	mark(http.MethodDelete, "/Users/u/FavoriteItems/"+movie.encode(), "", false)
	mark(http.MethodPost, "/UserItems/"+series.encode()+"/UserData", `{"IsFavorite":false}`, false)
	if readBack(movie) || readBack(series) {
		t.Fatal("cleared favourite still reads back")
	}
	if got := list("Filters=IsFavorite"); got.TotalRecordCount != 2 {
		t.Fatalf("after clearing: total %d, want 2", got.TotalRecordCount)
	}
	if st, ok := f.play.Get("living-room", movie.encode()); !ok || st.PositionTicks != 20*60*ticksPerSecond {
		t.Fatalf("clearing the favourite dropped the progress: %+v (ok=%v)", st, ok)
	}

	// A library is not a favourite, and a series has no position to set.
	if rec := f.do(http.MethodPost, "/jellyfin/UserFavoriteItems/"+viewID("tmdb.trending.movie"), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("favouriting a library: status %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, "/jellyfin/UserItems/"+series.encode()+"/UserData", `{"PlaybackPositionTicks":600000000}`); rec.Code != http.StatusNotFound {
		t.Fatalf("positioning a series: status %d", rec.Code)
	}
}

// TestFavoritesPagingSkipsUnresolvable covers favourites that no longer
// resolve — a title with no metadata, an episode the series does not have —
// sitting between ones that do. They are out of the count as well as the
// page, so a client paging by StartIndex, at any page size and however it
// advances, sees each favourite exactly once and the total it was promised.
func TestFavoritesPagingSkipsUnresolvable(t *testing.T) {
	f := newFixture()
	f.resolveOnOpen = true
	movie, _ := itemIDFor("movie", "tt0111161")
	gone, _ := itemIDFor("movie", "tt0000001")
	series, _ := itemIDFor("series", "tt0903747")
	anime, _ := itemIDFor("anime", "kitsu:9")
	for _, id := range []itemID{movie, gone, series, series.episode(1, 99), anime} {
		var ud userData
		decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserFavoriteItems/"+id.encode(), ""), &ud)
	}
	want := map[string]bool{movie.encode(): true, series.encode(): true, anime.encode(): true}

	searches := f.catalog.playlistCalls
	for _, limit := range []int{1, 2, 3, 5} {
		for _, byReceived := range []bool{false, true} {
			seen := map[string]int{}
			for start := 0; start < 10; {
				var got queryResult
				decodeInto(t, f.do(http.MethodGet, fmt.Sprintf("/jellyfin/Items?Recursive=true&Filters=IsFavorite&StartIndex=%d&Limit=%d", start, limit), ""), &got)
				if got.TotalRecordCount != len(want) || got.StartIndex != start {
					t.Fatalf("limit %d start %d: total %d, start %d", limit, start, got.TotalRecordCount, got.StartIndex)
				}
				if len(got.Items) == 0 {
					break
				}
				for _, item := range got.Items {
					seen[item.ID]++
				}
				if byReceived {
					start += len(got.Items)
				} else {
					start += limit
				}
			}
			if len(seen) != len(want) {
				t.Fatalf("limit %d (by received %v): saw %v, want %v", limit, byReceived, seen, want)
			}
			for id, n := range seen {
				if !want[id] || n != 1 {
					t.Fatalf("limit %d (by received %v): %s seen %d times", limit, byReceived, id, n)
				}
			}
		}
	}
	if f.catalog.playlistCalls != searches {
		t.Fatalf("favourites paging searched %d times", f.catalog.playlistCalls-searches)
	}

	// The per-user route form, an anime library, and a library that does not
	// exist for this stream.
	for path, total := range map[string]int{
		"/jellyfin/Users/u/Items?Recursive=true&Filters=IsFavorite":                                    3,
		"/jellyfin/Items?Recursive=true&Filters=IsFavorite&ParentId=" + viewID("kitsu.trending.anime"): 1,
		"/jellyfin/Items?Recursive=true&Filters=IsFavorite&ParentId=" + viewID("not.a.catalog"):        0,
	} {
		var got queryResult
		decodeInto(t, f.do(http.MethodGet, path, ""), &got)
		if got.TotalRecordCount != total || len(got.Items) != total || got.Items == nil {
			t.Fatalf("%s: total %d, %d items, want %d", path, got.TotalRecordCount, len(got.Items), total)
		}
	}
}

// TestUserDataUpdateMixesFavoriteWithWatchedState covers a UserData body
// carrying a favourite and watched state together. A movie or episode takes
// both. A series or season takes the favourite alone: its watched state is
// marked through /UserPlayedItems, and a Played sent here, its client's echo
// of its whole UserData, is ignored, true or false. It has no position, so a
// body setting one is refused whole, as is one without IsFavorite.
func TestUserDataUpdateMixesFavoriteWithWatchedState(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	series, _ := itemIDFor("series", "tt0903747")
	episode := series.episode(1, 1)
	post := func(id itemID, body string) *httptest.ResponseRecorder {
		return f.do(http.MethodPost, "/jellyfin/UserItems/"+id.encode()+"/UserData", body)
	}

	var ud userData
	decodeInto(t, post(movie, `{"IsFavorite":true,"Played":true}`), &ud)
	if !ud.IsFavorite || !ud.Played {
		t.Fatalf("movie favourite and played: %+v", ud)
	}
	decodeInto(t, post(episode, `{"PlaybackPositionTicks":600000000}`), &ud)
	if ud.PlaybackPositionTicks != 60*ticksPerSecond || ud.IsFavorite {
		t.Fatalf("episode position: %+v", ud)
	}
	decodeInto(t, post(episode, `{"Played":true,"IsFavorite":false}`), &ud)
	if !ud.Played || ud.IsFavorite {
		t.Fatalf("episode played: %+v", ud)
	}

	for _, body := range []string{
		`{"IsFavorite":true,"PlaybackPositionTicks":600000000}`,
		`{"Played":true,"PlaybackPositionTicks":600000000}`,
	} {
		for _, id := range []itemID{series, series.season(2)} {
			if rec := post(id, body); rec.Code != http.StatusNotFound {
				t.Fatalf("%v %s: status %d, want 404", id.Kind, body, rec.Code)
			}
			if st, ok := f.play.Get("living-room", id.encode()); ok {
				t.Fatalf("%v %s: refused request still wrote %+v", id.Kind, body, st)
			}
		}
	}
	if st, ok := f.play.Get("living-room", series.episode(2, 1).encode()); ok {
		t.Fatalf("refused request marked an episode: %+v", st)
	}
	// What a client sends when it posts the whole UserData back: unplayed,
	// at the start, now a favourite. The series is part-watched, so its
	// Played false is no change, and nothing is unmarked.
	decodeInto(t, post(series, `{"IsFavorite":true,"Played":false,"PlaybackPositionTicks":0}`), &ud)
	if !ud.IsFavorite || ud.Played {
		t.Fatalf("series favourite with its unchanged state: %+v", ud)
	}
	if st, _ := f.play.Get("living-room", episode.encode()); !st.Played {
		t.Fatalf("a favourite unmarked the series: %+v", st)
	}
	// Season 1 is part-watched. Played sent with IsFavorite, whatever it
	// says, leaves it so, and the favourite is applied; a body without
	// IsFavorite, or with nothing a season takes, is refused as it always was.
	season := series.season(1)
	for _, c := range []struct {
		body     string
		favorite bool
	}{
		{`{"IsFavorite":true,"Played":true}`, true},
		{`{"IsFavorite":true,"Played":false}`, true},
		{`{"IsFavorite":false,"Played":true,"PlaybackPositionTicks":0}`, false},
		{`{"IsFavorite":false,"Played":false}`, false},
	} {
		before := f.play.ListForStream("living-room")
		decodeInto(t, post(season, c.body), &ud)
		if ud.IsFavorite != c.favorite || ud.Played || ud.UnplayedItemCount == nil || *ud.UnplayedItemCount != 1 {
			t.Fatalf("%s: %+v", c.body, ud)
		}
		var episodes []persistence.JellyfinPlaystate
		for _, st := range f.play.ListForStream("living-room") {
			if id, _ := decodeItemID(st.ItemID); id.Kind == kindEpisode {
				episodes = append(episodes, st)
			}
		}
		var beforeEpisodes []persistence.JellyfinPlaystate
		for _, st := range before {
			if id, _ := decodeItemID(st.ItemID); id.Kind == kindEpisode {
				beforeEpisodes = append(beforeEpisodes, st)
			}
		}
		if !sameRows(beforeEpisodes, episodes) {
			t.Fatalf("%s changed episodes", c.body)
		}
	}
	for _, body := range []string{`{"Played":true}`, `{"Played":false}`, `{}`, `{"PlaybackPositionTicks":0}`, `{"Likes":true,"Rating":5}`} {
		before := f.play.ListForStream("living-room")
		metas := f.catalog.metaCalls
		if rec := post(season, body); rec.Code != http.StatusNotFound {
			t.Fatalf("%s: %d, want 404", body, rec.Code)
		}
		if f.catalog.metaCalls != metas || !sameRows(before, f.play.ListForStream("living-room")) {
			t.Fatalf("%s looked up metadata or wrote", body)
		}
	}
}

// sameRows reports whether two listings of a stream hold the same rows.
func sameRows(a, b []persistence.JellyfinPlaystate) bool {
	keyed := func(rows []persistence.JellyfinPlaystate) map[string]persistence.JellyfinPlaystate {
		out := map[string]persistence.JellyfinPlaystate{}
		for _, st := range rows {
			out[st.ItemID] = st
		}
		return out
	}
	return maps.Equal(keyed(a), keyed(b))
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

// TestMediaSourceNameUsesTheStreamFormat covers the formatted label reaching a
// client's version picker, and the invariant that makes it safe: the label is
// display only. Every codec, resolution and HDR field on a media source is
// parsed out of the *raw* release title, so a stream whose format renders
// something unparseable must still report the right capabilities.
func TestMediaSourceNameUsesTheStreamFormat(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	raw := "The.Shawshank.Redemption.1994.2160p.UHD.BluRay.x265.HDR.DTS-HD.MA.5.1-GRP"
	f.catalog.playlist = &stremio.PlaylistView{
		RuntimeSeconds: 142 * 60,
		Entries: []stremio.PlaylistEntry{
			{Index: 0, Title: raw, DisplayTitle: "★★★★☆ 4K HDR · 40 GB · eweka", Size: 40 << 30},
			// No DisplayTitle: a stream with no custom format falls back to
			// the release title rather than to a blank row.
			{Index: 1, Title: "The.Shawshank.Redemption.1994.1080p.BluRay.x264.DD5.1-GRP.mkv"},
		},
	}

	var info playbackInfoResponse
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode()+"/PlaybackInfo", ""), &info)
	if len(info.MediaSources) != 2 {
		t.Fatalf("sources: %+v", info.MediaSources)
	}
	if got := info.MediaSources[0].Name; got != "★★★★☆ 4K HDR · 40 GB · eweka" {
		t.Fatalf("formatted name not used: %q", got)
	}
	if got := info.MediaSources[1].Name; got != "The.Shawshank.Redemption.1994.1080p.BluRay.x264.DD5.1-GRP.mkv" {
		t.Fatalf("unformatted entry should keep the release title: %q", got)
	}
	// The formatted label must not have been parsed in place of the title.
	streams := info.MediaSources[0].MediaStreams
	if len(streams) == 0 || streams[0].Codec != "hevc" || streams[0].Height == nil || *streams[0].Height != 2160 {
		t.Fatalf("capabilities must come from the raw title, not the label: %+v", streams)
	}
}

// TestAdvertisedImageTagsCoverWhatClientsAskFor guards the image contract.
//
// The app that fetches artwork is not the client that signed in: Flutter's and
// Android's image loaders send a bare GET with no token, so the only image a
// client can reach is one whose tag it was handed. An unadvertised kind is a
// blank poster on screen, not a fallback — Fladder rendered a whole Continue
// Watching row as flat colour because nothing advertised Thumb.
func TestAdvertisedImageTagsCoverWhatClientsAskFor(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	series, _ := itemIDFor("series", "tt0903747")

	var item baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode(), ""), &item)
	for _, kind := range []string{"Primary", "Thumb"} {
		if item.ImageTags[kind] == "" {
			t.Fatalf("movie advertises no %s tag: %+v", kind, item.ImageTags)
		}
	}
	if len(item.BackdropImageTags) == 0 {
		t.Fatal("movie advertises no backdrop")
	}

	// An episode has no landscape art of its own, so it borrows the series'.
	// Without it, an episode hero and a Continue Watching row have nothing.
	var episode baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+series.episode(1, 2).encode(), ""), &episode)
	for _, kind := range []string{"Primary", "Thumb"} {
		if episode.ImageTags[kind] == "" {
			t.Fatalf("episode advertises no %s tag: %+v", kind, episode.ImageTags)
		}
	}
	if len(episode.BackdropImageTags) == 0 {
		t.Fatal("episode advertises no backdrop")
	}

	// Every advertised tag has to actually resolve without credentials, which
	// is the half that makes advertising it worth anything.
	for kind, tag := range episode.ImageTags {
		rec := f.do(http.MethodGet, "/jellyfin/Items/"+episode.ID+"/Images/"+kind+"?tag="+tag, "", "X-Nothing", "x")
		if rec.Code == http.StatusNotFound {
			t.Fatalf("advertised %s tag does not resolve unauthenticated", kind)
		}
	}
}

// TestFladderImageURLShapes answers the exact URLs Fladder builds.
//
// From its lib/providers/image_provider.dart: getItemsImageUrl, used for
// Primary, Thumb and Logo, sends only fillHeight/fillWidth/quality — no tag.
// getBackdropImage sends the tag as well, under a /Backdrop/{index} path. The
// tag in the item document is used for Fladder's cache key, not its URL, so
// advertising more tags never helped; the request has to be answerable without
// one. Its image loader also sends no credentials.
func TestFladderImageURLShapes(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")

	// The document has to be rendered first: that is what indexes the URLs.
	var item baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode(), ""), &item)

	anon := func(path string) *httptest.ResponseRecorder {
		return f.do(http.MethodGet, path, "", "X-Nothing", "x")
	}
	base := "/jellyfin/Items/" + item.ID + "/Images/"
	sized := "?fillHeight=576&fillWidth=384&quality=90"

	for _, kind := range []string{"Primary", "Thumb", "Logo"} {
		if rec := anon(base + kind + sized); rec.Code != http.StatusOK {
			t.Fatalf("Fladder %s URL: %d", kind, rec.Code)
		}
	}
	// Backdrop is the one shape that already worked, tag and index included.
	got := anon(base + "Backdrop/0?tag=" + item.BackdropImageTags[0] + "&fillHeight=576&fillWidth=384&quality=90")
	if got.Code != http.StatusOK {
		t.Fatalf("Fladder backdrop URL: %d", got.Code)
	}
	// Ids come back in whatever hyphenation a client likes; the index is keyed
	// by the canonical form, so it has to be normalised on the way in.
	dashed := item.ID[:8] + "-" + item.ID[8:12] + "-" + item.ID[12:16] + "-" + item.ID[16:20] + "-" + item.ID[20:]
	if rec := anon("/jellyfin/Items/" + dashed + "/Images/Primary" + sized); rec.Code != http.StatusOK {
		t.Fatalf("hyphenated item id: %d", rec.Code)
	}
}

// TestListRowsCarryVersionSignal pins the list-document contract Infuse's
// Direct Mode depends on: movie rows, Latest rows and episode rows carry two
// placeholder sources naming slots 0 and 1, a cached playlist is rendered in
// their place, and none of it runs a search.
func TestListRowsCarryVersionSignal(t *testing.T) {
	f := newFixture()
	f.resolveOnOpen = true
	stream := &auth.Stream{Username: "living-room", Token: testToken}
	requireTwo := func(row *baseItem, at string) {
		t.Helper()
		if len(row.MediaSources) != 2 || row.MediaSourceCount == nil || *row.MediaSourceCount != 2 || row.EnableMediaSourceDisplay == nil || !*row.EnableMediaSourceDisplay {
			t.Fatalf("%s: want two placeholder sources with count and display, got %+v", at, row)
		}
		id, err := decodeItemID(row.ID)
		if err != nil {
			t.Fatalf("%s: row id: %v", at, err)
		}
		for i, src := range row.MediaSources {
			if src.ID != mediaSourceIDFor(id, i) || src.Path != f.server.streamURLFor(id, i, stream) || !src.SupportsDirectPlay {
				t.Fatalf("%s: slot %d is not a playable stand-in: %+v", at, i, src)
			}
		}
		if row.MediaSources[0].Name != row.Name || row.MediaSources[1].Name == row.MediaSources[0].Name {
			t.Fatalf("%s: placeholder names: %q %q", at, row.MediaSources[0].Name, row.MediaSources[1].Name)
		}
	}

	var page queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items?ParentId="+viewID("tmdb.trending.movie")+"&Limit=5", ""), &page)
	requireTwo(page.Items[0], "movie row")

	var latest []*baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items/Latest?ParentId="+viewID("tmdb.trending.movie")+"&Limit=3", ""), &latest)
	requireTwo(latest[0], "latest row")

	series, _ := itemIDFor("series", "tt0903747")
	var episodes queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Shows/"+series.encode()+"/Episodes?SeasonId="+series.season(1).encode(), ""), &episodes)
	if len(episodes.Items) == 0 {
		t.Fatalf("no episodes")
	}
	requireTwo(episodes.Items[0], "episode row")

	// Series rows are folders and carry nothing. (Fresh result: decoding
	// into a reused slice of pointers keeps fields the JSON omits.)
	var seriesPage queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items?ParentId="+viewID("tmdb.trending.series"), ""), &seriesPage)
	if len(seriesPage.Items[0].MediaSources) != 0 || seriesPage.Items[0].MediaSourceCount != nil {
		t.Fatalf("series row carries media sources: %+v", seriesPage.Items[0])
	}

	// A playlist already in cache is rendered instead of the placeholders.
	entries := make([]stremio.PlaylistEntry, 3)
	for i := range entries {
		entries[i] = stremio.PlaylistEntry{Index: i, Title: fmt.Sprintf("Release.%02d.mkv", i)}
	}
	f.catalog.cached = &stremio.PlaylistView{Entries: entries}
	var cachedPage queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items?ParentId="+viewID("tmdb.trending.movie")+"&Limit=5", ""), &cachedPage)
	if row := cachedPage.Items[0]; len(row.MediaSources) != 3 || row.MediaSources[0].Name != "Release.00.mkv" || *row.MediaSourceCount != 3 {
		t.Fatalf("cached playlist not rendered on the row: %+v", row)
	}

	// None of the above searched: the list document is built from cache and
	// placeholders only, whatever resolve-on-open is set to.
	if calls := f.catalog.playlistCalls; calls != 0 {
		t.Fatalf("list rows ran %d searches, want 0", calls)
	}

	// Playing placeholder slot 1 goes to the addon as slot 1; when the list
	// turns out shorter the addon's slot recovery wraps to the first
	// playable candidate, so nothing here has to know how many were found.
	movie, _ := itemIDFor("movie", "tt0111161")
	rec := f.do(http.MethodGet, "/jellyfin/Videos/"+movie.encode()+"/stream?Static=true&MediaSourceId="+movie.source(1).encode(), "")
	if rec.Code != http.StatusOK || f.catalog.served[len(f.catalog.served)-1].slotPath != stremio.SlotPathFor(stream, "movie", "tt0111161", 1) {
		t.Fatalf("slot 1 play: %d served %+v", rec.Code, f.catalog.served)
	}
}

// TestUserDataUpdateLeavesUnchangedPlaybackAlone covers a client posting its
// whole UserData to change only the favourite. Writing the played flag or
// position it sent back unchanged would restamp LastPlayedAt: a half-watched
// movie would jump to the front of Continue Watching, and one never played
// would gain a last-played date. Only what differs from the stored state is
// written.
func TestUserDataUpdateLeavesUnchangedPlaybackAlone(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	unwatched, _ := itemIDFor("movie", "tt0000002")
	series, _ := itemIDFor("series", "tt0903747")
	episode := series.episode(1, 1)
	half := int64(20 * 60 * ticksPerSecond)
	then := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, id := range []itemID{movie, episode} {
		f.play.Upsert(persistence.JellyfinPlaystate{StreamName: "living-room", ItemID: id.encode(), ContentType: id.ContentType,
			ContentID: id.playStremioID(), PositionTicks: half, PlayCount: 1, LastPlayedAt: then})
	}
	post := func(id itemID, body string) userData {
		t.Helper()
		var ud userData
		decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserItems/"+id.encode()+"/UserData", body), &ud)
		return ud
	}
	stored := func(id itemID) persistence.JellyfinPlaystate {
		t.Helper()
		st, _ := f.play.Get("living-room", id.encode())
		return st
	}
	whole := func(favorite bool, played bool, position int64) string {
		return fmt.Sprintf(`{"IsFavorite":%v,"Played":%v,"PlaybackPositionTicks":%d}`, favorite, played, position)
	}

	saves := f.play.saves
	for _, id := range []itemID{movie, episode} {
		// Twice: the repeat is the same no-op.
		for range 2 {
			if ud := post(id, whole(true, false, half)); !ud.IsFavorite || ud.Played || ud.PlaybackPositionTicks != half {
				t.Fatalf("%v favourite: %+v", id.Kind, ud)
			}
		}
		if st := stored(id); !st.IsFavorite || st.PositionTicks != half || !st.LastPlayedAt.Equal(then) || st.PlayCount != 1 {
			t.Fatalf("%v favourite rewrote playback: %+v", id.Kind, st)
		}
	}
	if ud := post(unwatched, whole(true, false, 0)); !ud.IsFavorite || ud.LastPlayedDate != "" {
		t.Fatalf("never-watched favourite: %+v", ud)
	}
	if st := stored(unwatched); !st.LastPlayedAt.IsZero() || st.PlayCount != 0 {
		t.Fatalf("never-watched favourite gained playback: %+v", st)
	}
	if f.play.saves != saves {
		t.Fatalf("favourite-only updates wrote playback %d times", f.play.saves-saves)
	}
	if ud := post(movie, `{}`); !ud.IsFavorite || ud.PlaybackPositionTicks != half {
		t.Fatalf("empty body changed state: %+v", ud)
	}

	// Real changes still land, explicit zero included, and alongside a
	// favourite change.
	if ud := post(movie, whole(true, false, 2*half)); ud.PlaybackPositionTicks != 2*half {
		t.Fatalf("moved position: %+v", ud)
	}
	if st := stored(movie); !st.LastPlayedAt.After(then) {
		t.Fatalf("moved position kept the old LastPlayedAt: %+v", st)
	}
	if ud := post(movie, `{"PlaybackPositionTicks":0}`); ud.PlaybackPositionTicks != 0 {
		t.Fatalf("explicit zero position: %+v", ud)
	}
	// Watched, with the stale position the client still holds: played, and
	// the position reset as a played mark resets it.
	if ud := post(episode, whole(false, true, half)); !ud.Played || ud.IsFavorite || ud.PlaybackPositionTicks != 0 {
		t.Fatalf("episode played and unfavourited: %+v", ud)
	}
	if ud := post(episode, whole(false, false, 0)); ud.Played {
		t.Fatalf("episode unplayed: %+v", ud)
	}
}

// favoritesFixture favourites, across two series and a movie, every kind the
// ParentId and Ids tests below tell apart.
func favoritesFixture(t *testing.T) (*fixture, itemID, itemID, itemID) {
	t.Helper()
	f := newFixture()
	f.catalog.metas["series/tt0944947"] = &stremio.MetaObject{
		ID: "tt0944947", Type: "series", Name: "Game of Thrones",
		Videos: []stremio.MetaVideo{{ID: "tt0944947:1:1", Title: "Winter Is Coming", Season: 1, Episode: 1}},
	}
	movie, _ := itemIDFor("movie", "tt0111161")
	bb, _ := itemIDFor("series", "tt0903747")
	got, _ := itemIDFor("series", "tt0944947")
	for _, id := range []itemID{movie, bb.season(1), bb.episode(1, 1), bb.episode(1, 2), bb.episode(0, 1), got, got.season(1), got.episode(1, 1)} {
		var ud userData
		decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserFavoriteItems/"+id.encode(), ""), &ud)
	}
	return f, movie, bb, got
}

func favoriteIDs(t *testing.T, f *fixture, query string) (int, []string) {
	t.Helper()
	var result queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?"+query, ""), &result)
	ids := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		ids = append(ids, item.ID)
	}
	return result.TotalRecordCount, ids
}

// TestFavoritesUnderSeriesAndSeasonParent covers a ParentId naming a series
// or season: it admits that title's own children, as childrenOf lists them,
// and never another series' favourites.
func TestFavoritesUnderSeriesAndSeasonParent(t *testing.T) {
	f, _, bb, got := favoritesFixture(t)
	episodes := []string{bb.episode(0, 1).encode(), bb.episode(1, 1).encode(), bb.episode(1, 2).encode()}
	sort.Strings(episodes)

	for query, want := range map[string][]string{
		"Filters=IsFavorite&IncludeItemTypes=Episode&ParentId=" + bb.encode(): episodes,
		"Filters=IsFavorite&Recursive=true&ParentId=" + bb.encode():           episodes,
		"Filters=IsFavorite&ParentId=" + bb.encode():                          {bb.season(1).encode()},
		"Filters=IsFavorite&ParentId=" + got.encode():                         {got.season(1).encode()},
		"isFavorite=true&ParentId=" + bb.season(1).encode():                   {bb.episode(1, 1).encode(), bb.episode(1, 2).encode()},
		"Filters=IsFavorite&IncludeItemTypes=Movie&ParentId=" + bb.encode():   {},
		"Filters=IsFavorite&ParentId=" + bb.season(2).encode():                {},
		"Filters=IsFavorite&ParentId=not-an-id":                               {},
		"Filters=IsFavorite&ParentId=" + bb.episode(1, 1).encode():            {},
	} {
		total, ids := favoriteIDs(t, f, query)
		if want := slices.Sorted(slices.Values(want)); total != len(want) || !slices.Equal(slices.Sorted(slices.Values(ids)), want) {
			t.Fatalf("%s: total %d, %v, want %v", query, total, ids, want)
		}
	}
	// A library still admits its whole content type, both series included.
	if total, _ := favoriteIDs(t, f, "Filters=IsFavorite&Recursive=true&ParentId="+viewID("tmdb.trending.series")); total != 7 {
		t.Fatalf("series library: total %d, want 7", total)
	}
	// Paging counts after the parent narrows.
	all := "Filters=IsFavorite&IncludeItemTypes=Episode&ParentId=" + bb.encode()
	_, full := favoriteIDs(t, f, all)
	if total, ids := favoriteIDs(t, f, all+"&StartIndex=1&Limit=1"); total != 3 || len(ids) != 1 || ids[0] != full[1] {
		t.Fatalf("paged under a series: total %d, %v, want [%s] of %v", total, ids, full[1], full)
	}
}

// TestFavoriteFilterNarrowsIdsAndSearch covers the selectors that run before
// the favourites listing: an Ids lookup or a search with a favourite filter
// returns only the favourites among its items, and is unchanged without one.
func TestFavoriteFilterNarrowsIdsAndSearch(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	series, _ := itemIDFor("series", "tt0903747")
	var ud userData
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserFavoriteItems/"+movie.encode(), ""), &ud)

	ids := "Ids=" + movie.encode() + "," + series.encode()
	for query, want := range map[string][]string{
		ids:                                {movie.encode(), series.encode()},
		ids + "&Filters=IsFavorite":        {movie.encode()},
		ids + "&Filters=IsFavoriteOrLikes": {movie.encode()},
		ids + "&isFavorite=true":           {movie.encode()},
		"Ids=" + movie.source(1).encode() + "," + series.encode() + "&Filters=IsFavorite": {movie.encode()},
		"searchTerm=x&Filters=IsFavorite":                                                 {movie.encode()},
		"searchTerm=x&isFavorite=true&IncludeItemTypes=Series":                            {},
		"searchTerm=x&Filters=IsFavoriteOrLikes&IncludeItemTypes=Movie":                   {movie.encode()},
	} {
		total, got := favoriteIDs(t, f, query)
		if total != len(want) || !slices.Equal(got, want) {
			t.Fatalf("%s: total %d, %v, want %v", query, total, got, want)
		}
	}
	if total, got := favoriteIDs(t, f, "searchTerm=x"); len(got) != 2 || total < 2 {
		t.Fatalf("unfiltered search: total %d, %v", total, got)
	}

	var found queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?searchTerm=x&Filters=IsFavorite", ""), &found)
	if found.Items[0].UserData == nil || !found.Items[0].UserData.IsFavorite {
		t.Fatalf("favourite search row without its favourite flag: %+v", found.Items[0].UserData)
	}
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserFavoriteItems/"+series.encode(), ""), &ud)
	if total, got := favoriteIDs(t, f, "searchTerm=x&Filters=IsFavorite&StartIndex=1&Limit=1"); total != 2 || len(got) != 1 {
		t.Fatalf("paged favourite search: total %d, %v", total, got)
	}
}

// TestFavoriteEpisodeMatchesEpisodeListing covers a favourite episode carrying
// the same media sources as in its season's listing: clients like Infuse
// decide on the version picker from them, so an episode must not offer one in
// one place and not the other.
func TestFavoriteEpisodeMatchesEpisodeListing(t *testing.T) {
	f := newFixture()
	series, _ := itemIDFor("series", "tt0903747")
	episode := series.episode(1, 1)
	var ud userData
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserFavoriteItems/"+episode.encode(), ""), &ud)

	var favs, listing queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?Filters=IsFavorite&IncludeItemTypes=Episode", ""), &favs)
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Shows/"+series.encode()+"/Episodes?Season=1", ""), &listing)
	if len(favs.Items) != 1 || len(listing.Items) == 0 || listing.Items[0].ID != episode.encode() {
		t.Fatalf("favourites %d items, listing %d", len(favs.Items), len(listing.Items))
	}
	fav, row := favs.Items[0], listing.Items[0]
	sourceIDs := func(item *baseItem) []string {
		var out []string
		for _, src := range item.MediaSources {
			out = append(out, src.ID)
		}
		return out
	}
	if len(row.MediaSources) < 2 || !slices.Equal(sourceIDs(fav), sourceIDs(row)) {
		t.Fatalf("media sources: favourite %v, listing %v", sourceIDs(fav), sourceIDs(row))
	}
	if !reflect.DeepEqual(fav.MediaSourceCount, row.MediaSourceCount) || !reflect.DeepEqual(fav.EnableMediaSourceDisplay, row.EnableMediaSourceDisplay) {
		t.Fatalf("version signal: favourite %v/%v, listing %v/%v", fav.MediaSourceCount, fav.EnableMediaSourceDisplay, row.MediaSourceCount, row.EnableMediaSourceDisplay)
	}

	// The episode's own page is not a list row and keeps its single source.
	var detail baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+episode.encode(), ""), &detail)
	if len(detail.MediaSources) != 1 {
		t.Fatalf("episode detail: %d sources, want 1", len(detail.MediaSources))
	}
}

// TestFavoritesListingReadsEachTitleOnce covers what one favourites page
// costs: one metadata lookup per title however many of its seasons and
// episodes are favourites, no per-row play-state reads on top of the one
// listing query, and no searches.
func TestFavoritesListingReadsEachTitleOnce(t *testing.T) {
	f, _, _, _ := favoritesFixture(t)
	f.resolveOnOpen = true
	metas, gets, searches := f.catalog.metaCalls, f.play.gets, f.catalog.playlistCalls
	total, ids := favoriteIDs(t, f, "Filters=IsFavorite&Recursive=true&Limit=3")
	if total != 8 || len(ids) != 3 {
		t.Fatalf("favourites: total %d, %d items", total, len(ids))
	}
	if n := f.catalog.metaCalls - metas; n != 3 {
		t.Fatalf("metadata lookups for 3 titles: %d", n)
	}
	if n := f.play.gets - gets; n != 0 {
		t.Fatalf("per-row play-state reads: %d", n)
	}
	if f.catalog.playlistCalls != searches {
		t.Fatalf("favourites listing searched %d times", f.catalog.playlistCalls-searches)
	}
	// Pages are slices of one ordering: together they hold each favourite once.
	seen := map[string]bool{}
	for start := 0; start < total; start += 3 {
		_, page := favoriteIDs(t, f, fmt.Sprintf("Filters=IsFavorite&Recursive=true&StartIndex=%d&Limit=3", start))
		for _, id := range page {
			if seen[id] {
				t.Fatalf("%s listed twice", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != total {
		t.Fatalf("pages held %d of %d favourites", len(seen), total)
	}
}

// TestFavoriteIdsAndSearchRespectParent covers a favourite filter on an Ids
// lookup or a search combined with ParentId: each admits only the favourites
// the favourites listing would under that parent, counted after narrowing.
// Without a favourite filter both still ignore ParentId, as they always have.
func TestFavoriteIdsAndSearchRespectParent(t *testing.T) {
	f, movie, bb, got := favoritesFixture(t)
	var ud userData
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserFavoriteItems/"+bb.encode(), ""), &ud)
	movies, series := viewID("tmdb.trending.movie"), viewID("tmdb.trending.series")

	ids := "Ids=" + movie.encode() + "," + got.encode()
	episodes := "Ids=" + bb.episode(1, 1).encode() + "," + got.episode(1, 1).encode() + "," + bb.episode(0, 1).encode() + "," + bb.season(1).encode()
	for query, want := range map[string][]string{
		ids + "&Filters=IsFavorite&ParentId=" + movies:                                    {movie.encode()},
		ids + "&isFavorite=true&ParentId=" + series:                                       {got.encode()},
		ids + "&Filters=IsFavorite&ParentId=not-an-id":                                    {},
		episodes + "&Filters=IsFavorite&IncludeItemTypes=Episode&ParentId=" + bb.encode(): {bb.episode(1, 1).encode(), bb.episode(0, 1).encode()},
		episodes + "&Filters=IsFavorite&ParentId=" + bb.encode():                          {bb.season(1).encode()},
		episodes + "&Filters=IsFavorite&ParentId=" + bb.season(1).encode():                {bb.episode(1, 1).encode()},
		ids + "&ParentId=" + movies:                                                       {movie.encode(), got.encode()},

		// Search finds only movies and series, so a series or season parent,
		// whose favourites are seasons and episodes, admits none of them.
		"searchTerm=x&Filters=IsFavorite&ParentId=" + movies:                {movie.encode()},
		"searchTerm=x&Filters=IsFavoriteOrLikes&ParentId=" + series:         {bb.encode()},
		"searchTerm=x&Filters=IsFavorite&ParentId=" + bb.encode():           {},
		"searchTerm=x&Filters=IsFavorite&ParentId=" + bb.season(1).encode(): {},
		"searchTerm=x&Filters=IsFavorite&ParentId=not-an-id":                {},
	} {
		total, ids := favoriteIDs(t, f, query)
		if total != len(want) || !slices.Equal(ids, want) {
			t.Fatalf("%s: total %d, %v, want %v", query, total, ids, want)
		}
	}
	if _, ids := favoriteIDs(t, f, "searchTerm=x&ParentId="+movies); len(ids) != 2 {
		t.Fatalf("unfiltered search under a library: %v, want both matches", ids)
	}
}

// TestFavoriteIdsMatchFavoritesListing covers an Ids lookup with a favourite
// filter: its rows are the favourites listing's own, media sources included,
// so even with resolve-on-open a favourite series costs no search. Without the
// filter, Ids keeps its detail rendering.
func TestFavoriteIdsMatchFavoritesListing(t *testing.T) {
	f, movie, bb, got := favoritesFixture(t)
	f.resolveOnOpen = true
	var ud userData
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserFavoriteItems/"+bb.encode(), ""), &ud)
	favs := []itemID{movie, bb, bb.season(1), bb.episode(1, 1), bb.episode(1, 2), bb.episode(0, 1), got, got.season(1), got.episode(1, 1)}
	raws := []string{bb.season(2).encode(), movie.source(1).encode()}
	for _, id := range favs {
		raws = append(raws, id.encode())
	}

	var listing queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?Filters=IsFavorite&Recursive=true", ""), &listing)
	rows := map[string]*baseItem{}
	for _, item := range listing.Items {
		rows[item.ID] = item
	}
	searches, gets, metas := f.catalog.playlistCalls, f.play.gets, f.catalog.metaCalls
	var byIds queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?Filters=IsFavorite&Ids="+strings.Join(raws, ","), ""), &byIds)
	if n := f.catalog.playlistCalls - searches; n != 0 {
		t.Fatalf("favourite Ids ran %d searches, want 0", n)
	}
	if n := f.play.gets - gets; n != 0 {
		t.Fatalf("favourite Ids read play state %d times per row", n)
	}
	if n := f.catalog.metaCalls - metas; n != 3 {
		t.Fatalf("favourite Ids looked up metadata %d times, want 3", n)
	}
	// The source id stands for its movie, which is then listed twice, as asked.
	if byIds.TotalRecordCount != len(favs)+1 || len(byIds.Items) != len(favs)+1 {
		t.Fatalf("favourite Ids: total %d, %d items, want %d", byIds.TotalRecordCount, len(byIds.Items), len(favs)+1)
	}
	sourceIDs := func(item *baseItem) []string {
		var out []string
		for _, src := range item.MediaSources {
			out = append(out, src.ID)
		}
		return out
	}
	for _, item := range byIds.Items {
		row := rows[item.ID]
		if row == nil {
			t.Fatalf("%s is not in the favourites listing", item.ID)
		}
		if !slices.Equal(sourceIDs(item), sourceIDs(row)) || !reflect.DeepEqual(item.MediaSourceCount, row.MediaSourceCount) {
			t.Fatalf("%s sources: Ids %v, listing %v", item.ID, sourceIDs(item), sourceIDs(row))
		}
		if item.UserData == nil || !item.UserData.IsFavorite || item.UserData.ItemID != item.ID {
			t.Fatalf("%s user data: %+v", item.ID, item.UserData)
		}
	}

	// Unfiltered, a series is still its detail document, searched on open.
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?Ids="+bb.encode(), ""), &byIds)
	if len(byIds.Items) != 1 || f.catalog.playlistCalls == searches {
		t.Fatalf("unfiltered Ids: %d items, %d searches", len(byIds.Items), f.catalog.playlistCalls-searches)
	}
}

// TestFavoritesMetadataLookupsPerPage pins what a favourites page costs: one
// lookup per distinct title, run concurrently, with exact totals and
// contiguous pages whatever share of the favourites no longer resolves.
func TestFavoritesMetadataLookupsPerPage(t *testing.T) {
	for _, n := range []int{20, 100, 300} {
		f := newFixture()
		f.catalog.metaDelay = time.Millisecond
		resolvable := 0
		for i := 1; i <= n; i++ {
			contentID := fmt.Sprintf("tt9%06d", i)
			// Every tenth movie has lost its metadata.
			if i%10 != 0 {
				f.catalog.metas["movie/"+contentID] = &stremio.MetaObject{ID: contentID, Type: "movie", Name: contentID}
				resolvable++
			}
			id, _ := itemIDFor("movie", contentID)
			f.play.SetFavorite("living-room", id.encode(), "movie", contentID, true)
		}
		// Five episodes of one series, one the series does not have.
		bb, _ := itemIDFor("series", "tt0903747")
		for _, ep := range []itemID{bb.episode(1, 1), bb.episode(1, 2), bb.episode(2, 1), bb.episode(0, 1), bb.episode(9, 9)} {
			f.play.SetFavorite("living-room", ep.encode(), "series", ep.playStremioID(), true)
		}
		resolvable += 4
		titles := n + 1

		seen := map[string]bool{}
		calls, pages := 0, 0
		for start := 0; ; start += 20 {
			before := f.catalog.metaCalls
			var got queryResult
			decodeInto(t, f.do(http.MethodGet, fmt.Sprintf("/jellyfin/Items?Filters=IsFavorite&Recursive=true&StartIndex=%d&Limit=20", start), ""), &got)
			lookups := f.catalog.metaCalls - before
			if lookups != titles {
				t.Fatalf("%d favourites, page at %d: %d lookups, want %d", n, start, lookups, titles)
			}
			calls += lookups
			if got.TotalRecordCount != resolvable {
				t.Fatalf("%d favourites: total %d, want %d", n, got.TotalRecordCount, resolvable)
			}
			if len(got.Items) == 0 {
				break
			}
			pages++
			for _, item := range got.Items {
				if seen[item.ID] {
					t.Fatalf("%s listed twice", item.ID)
				}
				seen[item.ID] = true
			}
		}
		if len(seen) != resolvable {
			t.Fatalf("%d favourites: pages held %d, want %d", n, len(seen), resolvable)
		}
		if f.catalog.peakMeta < 2 || f.catalog.peakMeta > metaFetchLimit {
			t.Fatalf("%d favourites: %d lookups at once, want 2..%d", n, f.catalog.peakMeta, metaFetchLimit)
		}
		t.Logf("%d favourites (%d titles): %d pages, %d lookups, peak %d at once", n, titles, pages, calls, f.catalog.peakMeta)
	}
}

// TestPlayedWithZeroPositionWritesOnce covers a client marking a half-watched
// item played and sending the zero position that goes with it: the played
// mark already stores that zero, so it is one write, not two.
func TestPlayedWithZeroPositionWritesOnce(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	half := int64(20 * 60 * ticksPerSecond)
	f.play.Upsert(persistence.JellyfinPlaystate{StreamName: "living-room", ItemID: movie.encode(), ContentType: "movie", ContentID: "tt0111161", PositionTicks: half, PlayCount: 1})
	post := func(body string) userData {
		t.Helper()
		var ud userData
		decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserItems/"+movie.encode()+"/UserData", body), &ud)
		return ud
	}

	saves := f.play.saves
	if ud := post(`{"Played":true,"PlaybackPositionTicks":0}`); !ud.Played || ud.PlaybackPositionTicks != 0 {
		t.Fatalf("played with zero: %+v", ud)
	}
	if n := f.play.saves - saves; n != 1 {
		t.Fatalf("played with zero wrote %d times, want 1", n)
	}
	// Unplaying to a real position is two changes, and both land.
	saves = f.play.saves
	if ud := post(`{"Played":false,"PlaybackPositionTicks":` + fmt.Sprint(half) + `}`); ud.Played || ud.PlaybackPositionTicks != half {
		t.Fatalf("unplayed at a position: %+v", ud)
	}
	if n := f.play.saves - saves; n != 2 {
		t.Fatalf("unplayed at a position wrote %d times, want 2", n)
	}
}

// TestPlayedDiscardsStalePosition covers a client marking an item played
// while it still holds an old position, after another device moved it on:
// the played mark resets the position, and the stale one sent with it must
// not write it back. Every other position change still lands.
func TestPlayedDiscardsStalePosition(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	series, _ := itemIDFor("series", "tt0903747")
	episode := series.episode(1, 1)
	stale, moved := int64(10*60*ticksPerSecond), int64(20*60*ticksPerSecond)
	post := func(id itemID, body string) userData {
		t.Helper()
		var ud userData
		decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserItems/"+id.encode()+"/UserData", body), &ud)
		return ud
	}
	for _, id := range []itemID{movie, episode} {
		f.play.Upsert(persistence.JellyfinPlaystate{StreamName: "living-room", ItemID: id.encode(), ContentType: id.ContentType,
			ContentID: id.playStremioID(), PositionTicks: moved, PlayCount: 1})
		body := fmt.Sprintf(`{"Played":true,"PlaybackPositionTicks":%d,"IsFavorite":true}`, stale)
		// Twice: the repeat changes nothing.
		for range 2 {
			if ud := post(id, body); !ud.Played || ud.PlaybackPositionTicks != 0 || !ud.IsFavorite {
				t.Fatalf("%v played with a stale position: %+v", id.Kind, ud)
			}
		}
		for _, st := range f.play.ListResume("living-room", 10) {
			if st.ItemID == id.encode() {
				t.Fatalf("%v marked played is still resumable: %+v", id.Kind, st)
			}
		}
		// A position on its own is still a real change.
		if ud := post(id, fmt.Sprintf(`{"PlaybackPositionTicks":%d}`, stale)); ud.PlaybackPositionTicks != stale {
			t.Fatalf("%v position after played: %+v", id.Kind, ud)
		}
		// So is unplaying at a position.
		if ud := post(id, fmt.Sprintf(`{"Played":false,"PlaybackPositionTicks":%d}`, moved)); ud.Played || ud.PlaybackPositionTicks != moved {
			t.Fatalf("%v unplayed at a position: %+v", id.Kind, ud)
		}
	}
}

// TestSeriesFavoritesByIncludedType covers IncludeItemTypes choosing between
// a series' favourite seasons and episodes, recursive or not, and never
// another series' favourites.
func TestSeriesFavoritesByIncludedType(t *testing.T) {
	f, _, bb, got := favoritesFixture(t)
	var ud userData
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserFavoriteItems/"+bb.season(0).encode(), ""), &ud)
	seasons := []string{bb.season(0).encode(), bb.season(1).encode()}
	episodes := []string{bb.episode(0, 1).encode(), bb.episode(1, 1).encode(), bb.episode(1, 2).encode()}
	both := append(slices.Clone(seasons), episodes...)

	for query, want := range map[string][]string{
		"Recursive=true&IncludeItemTypes=Season":          seasons,
		"Recursive=false&IncludeItemTypes=Season":         seasons,
		"Recursive=true&IncludeItemTypes=Episode":         episodes,
		"IncludeItemTypes=Episode":                        episodes,
		"Recursive=true&IncludeItemTypes=Season,Episode":  both,
		"IncludeItemTypes=Season,Episode":                 both,
		"Recursive=true":                                  episodes,
		"Recursive=false":                                 seasons,
		"Recursive=true&IncludeItemTypes=Season,Movie":    seasons,
		"Recursive=true&IncludeItemTypes=Series":          {},
		"Recursive=true&IncludeItemTypes=Season&Limit=50": seasons,
	} {
		total, ids := favoriteIDs(t, f, "Filters=IsFavorite&ParentId="+bb.encode()+"&"+query)
		if want := slices.Sorted(slices.Values(want)); total != len(want) || !slices.Equal(slices.Sorted(slices.Values(ids)), want) {
			t.Fatalf("%s: total %d, %v, want %v", query, total, ids, want)
		}
	}
	if total, ids := favoriteIDs(t, f, "Filters=IsFavorite&Recursive=true&IncludeItemTypes=Season&ParentId="+got.encode()); total != 1 || ids[0] != got.season(1).encode() {
		t.Fatalf("other series: total %d, %v", total, ids)
	}
	// Paged after the type narrows.
	query := "Filters=IsFavorite&Recursive=true&IncludeItemTypes=Season,Episode&ParentId=" + bb.encode()
	_, full := favoriteIDs(t, f, query)
	if total, ids := favoriteIDs(t, f, query+"&StartIndex=3&Limit=3"); total != 5 || !slices.Equal(ids, full[3:]) {
		t.Fatalf("paged: total %d, %v, want %v", total, ids, full[3:])
	}
}

// TestResumableFavoritesIntersect covers IsResumable and IsFavorite together:
// in either order they list only the favourites partway through, narrowed
// before the limit, and each alone is unchanged.
func TestResumableFavoritesIntersect(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	bb, _ := itemIDFor("series", "tt0903747")
	half := int64(20 * 60 * ticksPerSecond)
	then := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	// Oldest first: the movie and S1E2 are favourites in progress, the
	// newest in progress is no favourite, and the favourite S2E1 is not in
	// progress.
	for i, id := range []itemID{movie, bb.episode(1, 2), bb.episode(1, 1)} {
		f.play.Upsert(persistence.JellyfinPlaystate{StreamName: "living-room", ItemID: id.encode(), ContentType: id.ContentType,
			ContentID: id.playStremioID(), PositionTicks: half, LastPlayedAt: then.Add(time.Duration(i) * time.Hour)})
	}
	for _, id := range []itemID{movie, bb.episode(1, 2), bb.episode(2, 1)} {
		var ud userData
		decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserFavoriteItems/"+id.encode(), ""), &ud)
	}
	resumable := []string{movie.encode(), bb.episode(1, 2).encode()}

	searches := f.catalog.playlistCalls
	for query, want := range map[string][]string{
		"Filters=IsResumable,IsFavorite":                                                  resumable,
		"Filters=IsFavorite,IsResumable":                                                  resumable,
		"Filters=IsResumable&isFavorite=true&Limit=1":                                     {bb.episode(1, 2).encode()},
		"Filters=IsResumable,IsFavoriteOrLikes&IncludeItemTypes=Movie&Limit=1":            {movie.encode()},
		"Filters=IsResumable,IsFavorite&IncludeItemTypes=Episode":                         {bb.episode(1, 2).encode()},
		"Filters=IsResumable,IsFavorite&IncludeItemTypes=Episode&ParentId=" + bb.encode(): {bb.episode(1, 2).encode()},
		"Filters=IsResumable,IsFavorite&ParentId=" + bb.encode():                          {},
		"Filters=IsResumable":                                                             {bb.episode(1, 1).encode(), bb.episode(1, 2).encode(), movie.encode()},
		"Filters=IsResumable&Limit=1":                                                     {bb.episode(1, 1).encode()},
		"Filters=IsFavorite&Recursive=true":                                               {movie.encode(), bb.episode(1, 2).encode(), bb.episode(2, 1).encode()},
	} {
		total, ids := favoriteIDs(t, f, query)
		if total != len(want) || !slices.Equal(slices.Sorted(slices.Values(ids)), slices.Sorted(slices.Values(want))) {
			t.Fatalf("%s: total %d, %v, want %v", query, total, ids, want)
		}
	}
	var empty queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?Filters=IsResumable,IsFavorite&ParentId="+bb.encode(), ""), &empty)
	if empty.Items == nil || empty.TotalRecordCount != 0 {
		t.Fatalf("empty intersection: %+v", empty)
	}
	if f.catalog.playlistCalls != searches {
		t.Fatalf("resumable favourites searched %d times", f.catalog.playlistCalls-searches)
	}
}

// TestPrefetchMetasStopsWhenClientGoes covers a request cancelled before its
// favourites resolve: no lookups start, and every title comes back missing
// rather than blocking the response.
func TestPrefetchMetasStopsWhenClientGoes(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	bb, _ := itemIDFor("series", "tt0903747")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rq := &request{Request: httptest.NewRequest(http.MethodGet, "/jellyfin/Items", nil).WithContext(ctx)}

	metas := f.server.prefetchMetas(rq, []itemID{movie, bb.episode(1, 1), bb.season(1)})
	if f.catalog.metaCalls != 0 || len(metas) != 2 || metas[metaKey(movie)] != nil || metas[metaKey(bb)] != nil {
		t.Fatalf("cancelled prefetch: %d lookups, %v", f.catalog.metaCalls, metas)
	}
}

// TestFavoriteWriteFailureIsAnError covers a favourite the store fails to
// write. Every route answers 500 rather than UserData, which a client would
// take as saved, and a UserData body stops there: no played mark or position
// is written after the failed favourite. Once the store recovers, the same
// requests answer as before.
func TestFavoriteWriteFailureIsAnError(t *testing.T) {
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")
	series, _ := itemIDFor("series", "tt0903747")
	episode := series.episode(1, 1)
	// A half-watched favourite, so clearing it is a write rather than a no-op.
	f.play.rows["living-room|"+movie.encode()] = persistence.JellyfinPlaystate{
		StreamName: "living-room", ItemID: movie.encode(), ContentType: "movie", ContentID: "tt0111161",
		PositionTicks: 60 * ticksPerSecond, PlayCount: 1, IsFavorite: true,
	}
	f.play.favErr = errors.New("database is locked")
	before := maps.Clone(f.play.rows)

	type call struct{ method, path, body string }
	calls := []call{
		{http.MethodPost, "/UserFavoriteItems/" + episode.encode(), ""},
		{http.MethodDelete, "/UserFavoriteItems/" + movie.encode(), ""},
		{http.MethodPost, "/Users/u/FavoriteItems/" + series.encode(), ""},
		{http.MethodDelete, "/Users/u/FavoriteItems/" + movie.encode(), ""},
		{http.MethodPost, "/UserItems/" + episode.encode() + "/UserData", `{"IsFavorite":true,"Played":true}`},
		{http.MethodPost, "/UserItems/" + episode.encode() + "/UserData", `{"IsFavorite":true,"PlaybackPositionTicks":900000000}`},
		{http.MethodPost, "/UserItems/" + movie.encode() + "/UserData", `{"IsFavorite":false,"Played":true}`},
		{http.MethodPost, "/UserItems/" + movie.encode() + "/UserData", `{"IsFavorite":false,"PlaybackPositionTicks":1200000000}`},
		{http.MethodPost, "/UserItems/" + series.season(1).encode() + "/UserData", `{"IsFavorite":true}`},
	}
	for _, c := range calls {
		writes, saves := f.play.favWrites, f.play.saves
		rec := f.do(c.method, "/jellyfin"+c.path, c.body)
		if rec.Code != http.StatusInternalServerError || rec.Body.String() != "Favourite update failed\n" {
			t.Fatalf("%s %s %s: %d %q, want 500 and no UserData", c.method, c.path, c.body, rec.Code, rec.Body.String())
		}
		if n := f.play.favWrites - writes; n != 1 {
			t.Fatalf("%s %s %s: %d favourite writes, want 1", c.method, c.path, c.body, n)
		}
		if f.play.saves != saves {
			t.Fatalf("%s %s %s: playback written after the favourite failed", c.method, c.path, c.body)
		}
	}
	if !maps.Equal(f.play.rows, before) {
		t.Fatalf("failed writes changed play state: %+v, was %+v", f.play.rows, before)
	}

	f.play.favErr = nil
	for _, c := range []struct {
		call
		favorite, played bool
	}{
		{calls[0], true, false},
		{calls[1], false, false},
		{calls[2], true, false},
		{calls[4], true, true},
		{call{http.MethodPost, calls[6].path, `{"IsFavorite":true}`}, true, false},
	} {
		var ud userData
		decodeInto(t, f.do(c.method, "/jellyfin"+c.path, c.body), &ud)
		if ud.IsFavorite != c.favorite || ud.Played != c.played {
			t.Fatalf("%s %s %s after recovery: %+v", c.method, c.path, c.body, ud)
		}
	}
}
