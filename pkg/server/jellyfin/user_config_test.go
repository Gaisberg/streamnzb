package jellyfin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"streamnzb/pkg/core/persistence"
)

// fakeUserConfig is an in-memory UserConfig that can be made to fail.
type fakeUserConfig struct {
	mu       sync.Mutex
	rows     map[string]persistence.JellyfinUserConfig
	writes   int
	readErr  error
	writeErr error
}

func newFakeUserConfig() *fakeUserConfig {
	return &fakeUserConfig{rows: map[string]persistence.JellyfinUserConfig{}}
}

// row is the stream's stored configuration, or the defaults, as the real
// store reads it. Callers hold mu.
func (f *fakeUserConfig) row(stream string) persistence.JellyfinUserConfig {
	if cfg, ok := f.rows[stream]; ok {
		return cfg
	}
	return persistence.DefaultJellyfinUserConfig()
}

func (f *fakeUserConfig) GetUserConfig(stream string) (persistence.JellyfinUserConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return persistence.JellyfinUserConfig{}, f.readErr
	}
	return f.row(stream), nil
}

func (f *fakeUserConfig) UpdateUserConfig(stream string, update func(persistence.JellyfinUserConfig) persistence.JellyfinUserConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return f.readErr
	}
	cfg := update(f.row(stream))
	if f.writeErr != nil {
		return f.writeErr
	}
	f.writes++
	f.rows[stream] = cfg
	return nil
}

func newUserConfigFixture() (*fixture, *fakeUserConfig) {
	f := newFixture()
	store := newFakeUserConfig()
	f.server.opts.UserConfig = store
	return f, store
}

// configOf reads the Configuration block of GET /Users/Me as the stream the
// token names.
func configOf(t *testing.T, f *fixture, token string) map[string]any {
	t.Helper()
	var me map[string]any
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/Me", "", "X-Emby-Token", token), &me)
	cfg, ok := me["Configuration"].(map[string]any)
	if !ok {
		t.Fatalf("Users/Me has no Configuration: %v", me)
	}
	return cfg
}

func postConfig(f *fixture, path, body string) int {
	return f.do(http.MethodPost, path, body).Code
}

type wantConfig struct {
	audio       any // nil for JSON null
	subtitle    string
	mode        string
	playDefault bool
}

func requireConfig(t *testing.T, cfg map[string]any, want wantConfig) {
	t.Helper()
	audio, present := cfg["AudioLanguagePreference"]
	if !present {
		t.Errorf("AudioLanguagePreference is missing")
	}
	if audio != want.audio || cfg["SubtitleLanguagePreference"] != want.subtitle ||
		cfg["SubtitleMode"] != want.mode || cfg["PlayDefaultAudioTrack"] != want.playDefault {
		t.Fatalf("configuration = audio %v, subtitle %v, mode %v, play default %v; want %+v",
			audio, cfg["SubtitleLanguagePreference"], cfg["SubtitleMode"], cfg["PlayDefaultAudioTrack"], want)
	}
}

var defaultsWant = wantConfig{audio: nil, subtitle: "", mode: "Default", playDefault: true}

// TestUserConfigurationPlezySaveReload replays Plezy's account-preferences
// write (issue #397): read Users/Me, merge the change into the whole
// Configuration, POST it to /Users/{id}/Configuration, then re-read Users/Me
// and DisplayPreferences/usersettings. Before the fix the POST was answered
// 204 and dropped, so every choice reverted to its default.
func TestUserConfigurationPlezySaveReload(t *testing.T) {
	f, _ := newUserConfigFixture()

	var me map[string]any
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/Me", ""), &me)
	cfg := me["Configuration"].(map[string]any)
	requireConfig(t, cfg, defaultsWant)

	// Plezy writes ISO 639-1 codes and "" for no preference, and turning on
	// "choose audio by language" clears PlayDefaultAudioTrack.
	cfg["AudioLanguagePreference"] = "en"
	cfg["SubtitleLanguagePreference"] = "en"
	cfg["PlayDefaultAudioTrack"] = false
	cfg["SubtitleMode"] = "Always"
	body, _ := json.Marshal(cfg)
	if code := postConfig(f, "/jellyfin/Users/"+me["Id"].(string)+"/Configuration", string(body)); code != http.StatusNoContent {
		t.Fatalf("POST configuration = %d, want 204", code)
	}

	want := wantConfig{audio: "en", subtitle: "en", mode: "Always", playDefault: false}
	requireConfig(t, configOf(t, f, testToken), want)
	if code := f.do(http.MethodGet, "/jellyfin/DisplayPreferences/usersettings?client=emby", "").Code; code != http.StatusOK {
		t.Fatalf("DisplayPreferences/usersettings = %d", code)
	}
	// Every route serving the user DTO carries the saved values.
	var byID map[string]any
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/"+me["Id"].(string), ""), &byID)
	requireConfig(t, byID["Configuration"].(map[string]any), want)
	var login map[string]any
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/Users/AuthenticateByName",
		`{"Username":"living-room","Pw":"`+testPassword+`"}`,
		"Authorization", `MediaBrowser Client="Plezy", Device="TV", DeviceId="dev-2", Version="2.25"`), &login)
	requireConfig(t, login["User"].(map[string]any)["Configuration"].(map[string]any), want)

	// The fields that are not kept still read their fixed values.
	cfg = configOf(t, f, testToken)
	if cfg["RememberAudioSelections"] != true || cfg["EnableNextEpisodeAutoPlay"] != true || cfg["HidePlayedInLatest"] != true {
		t.Fatalf("unkept fields changed: %v", cfg)
	}
}

// TestUserConfigurationModernRoute covers POST /Users/Configuration?userId=,
// Jellyfin's current spelling of the same write.
func TestUserConfigurationModernRoute(t *testing.T) {
	f, _ := newUserConfigFixture()
	if code := postConfig(f, "/jellyfin/Users/Configuration?userId="+userID("living-room"),
		`{"SubtitleLanguagePreference":"deu","SubtitleMode":"Smart"}`); code != http.StatusNoContent {
		t.Fatalf("POST /Users/Configuration = %d, want 204", code)
	}
	requireConfig(t, configOf(t, f, testToken), wantConfig{audio: nil, subtitle: "deu", mode: "Smart", playDefault: true})
}

// TestUserConfigurationPartialAndNull covers a body naming only some fields
// (the rest keep their stored values), field names and subtitle modes in any
// case, and null and "" clearing a language.
func TestUserConfigurationPartialAndNull(t *testing.T) {
	f, _ := newUserConfigFixture()
	path := "/jellyfin/Users/" + userID("living-room") + "/Configuration"

	if code := postConfig(f, path, `{"AudioLanguagePreference":" jpn ","SubtitleLanguagePreference":"eng","SubtitleMode":"OnlyForced","PlayDefaultAudioTrack":false}`); code != http.StatusNoContent {
		t.Fatalf("full POST = %d", code)
	}
	requireConfig(t, configOf(t, f, testToken), wantConfig{audio: "jpn", subtitle: "eng", mode: "OnlyForced", playDefault: false})

	if code := postConfig(f, path, `{"subtitlemode":"always"}`); code != http.StatusNoContent {
		t.Fatalf("partial POST = %d", code)
	}
	requireConfig(t, configOf(t, f, testToken), wantConfig{audio: "jpn", subtitle: "eng", mode: "Always", playDefault: false})

	// Jellyfin also takes the mode as its enum number.
	if code := postConfig(f, path, `{"SubtitleMode":4}`); code != http.StatusNoContent {
		t.Fatalf("numeric mode POST = %d", code)
	}
	requireConfig(t, configOf(t, f, testToken), wantConfig{audio: "jpn", subtitle: "eng", mode: "Smart", playDefault: false})
	if code := postConfig(f, path, `{"SubtitleMode":1}`); code != http.StatusNoContent {
		t.Fatalf("numeric mode POST = %d", code)
	}

	// The length limit counts characters, not bytes.
	wide := strings.Repeat("é", maxLanguagePreference)
	if code := postConfig(f, path, `{"SubtitleLanguagePreference":"`+wide+`"}`); code != http.StatusNoContent {
		t.Fatalf("POST of %d two-byte characters = %d", maxLanguagePreference, code)
	}
	requireConfig(t, configOf(t, f, testToken), wantConfig{audio: "jpn", subtitle: wide, mode: "Always", playDefault: false})
	if code := postConfig(f, path, `{"SubtitleLanguagePreference":"eng"}`); code != http.StatusNoContent {
		t.Fatalf("restore POST = %d", code)
	}

	if code := postConfig(f, path, `{"AudioLanguagePreference":null,"SubtitleLanguagePreference":""}`); code != http.StatusNoContent {
		t.Fatalf("clearing POST = %d", code)
	}
	requireConfig(t, configOf(t, f, testToken), wantConfig{audio: nil, subtitle: "", mode: "Always", playDefault: false})

	// A field named twice takes its last value in the document, whatever the
	// case of its names, as in Jellyfin; a UTF-8 byte order mark is skipped.
	if code := postConfig(f, path, `{"subtitleMode":"None","SubtitleMode":"Always","subtitlemode":"OnlyForced"}`); code != http.StatusNoContent {
		t.Fatalf("duplicate-field POST = %d", code)
	}
	requireConfig(t, configOf(t, f, testToken), wantConfig{audio: nil, subtitle: "", mode: "OnlyForced", playDefault: false})
	if code := postConfig(f, path, "\xef\xbb\xbf"+`{"SubtitleMode":"Always"}`); code != http.StatusNoContent {
		t.Fatalf("POST with a byte order mark = %d", code)
	}

	// A body with none of the kept fields changes nothing.
	if code := postConfig(f, path, `{"OrderedViews":[],"DisplayMissingEpisodes":true}`); code != http.StatusNoContent {
		t.Fatalf("unrelated POST = %d", code)
	}
	cfg := configOf(t, f, testToken)
	requireConfig(t, cfg, wantConfig{audio: nil, subtitle: "", mode: "Always", playDefault: false})
	if cfg["DisplayMissingEpisodes"] != false {
		t.Fatalf("an unkept field was stored: %v", cfg["DisplayMissingEpisodes"])
	}
}

// TestUserConfigurationRejectsInvalidBodies covers bodies that are not a
// UserConfiguration, or carry a bad value: each is refused with 400 and
// nothing is written, the valid fields beside a bad one included.
func TestUserConfigurationRejectsInvalidBodies(t *testing.T) {
	f, store := newUserConfigFixture()
	path := "/jellyfin/Users/" + userID("living-room") + "/Configuration"
	if code := postConfig(f, path, `{"AudioLanguagePreference":"eng","SubtitleMode":"Always"}`); code != http.StatusNoContent {
		t.Fatalf("seed POST = %d", code)
	}
	saved := wantConfig{audio: "eng", subtitle: "", mode: "Always", playDefault: true}

	for name, body := range map[string]string{
		"empty":                    ``,
		"not json":                 `not json`,
		"null":                     `null`,
		"array":                    `[]`,
		"truncated":                `{"SubtitleMode":"Always"`,
		"trailing data":            `{"SubtitleMode":"Always"} {}`,
		"two objects":              `{}{"SubtitleMode":"None"}`,
		"string":                   `"SubtitleMode"`,
		"bad nested value":         `{"OrderedViews":[1,}`,
		"unknown subtitle mode":    `{"SubtitleMode":"Sometimes"}`,
		"null subtitle mode":       `{"SubtitleMode":null}`,
		"empty subtitle mode":      `{"SubtitleMode":""}`,
		"out of range mode":        `{"SubtitleMode":5}`,
		"negative mode":            `{"SubtitleMode":-1}`,
		"fractional mode":          `{"SubtitleMode":1.5}`,
		"overlong wide language":   `{"AudioLanguagePreference":"` + strings.Repeat("é", maxLanguagePreference+1) + `"}`,
		"string default track":     `{"PlayDefaultAudioTrack":"false"}`,
		"null default track":       `{"PlayDefaultAudioTrack":null}`,
		"numeric language":         `{"AudioLanguagePreference":7}`,
		"object language":          `{"SubtitleLanguagePreference":{}}`,
		"overlong language":        `{"AudioLanguagePreference":"` + strings.Repeat("a", maxLanguagePreference+1) + `"}`,
		"valid beside invalid":     `{"AudioLanguagePreference":"fra","SubtitleLanguagePreference":"fra","SubtitleMode":"Bogus"}`,
		"oversized":                `{"OrderedViews":"` + strings.Repeat("x", 64<<10) + `"}`,
		"invalid in second casing": `{"SubtitleMode":"None","subtitleMode":"Bogus"}`,
		"invalid then valid":       `{"SubtitleMode":"Bogus","SubtitleMode":"None"}`,
	} {
		t.Run(name, func(t *testing.T) {
			before := store.writes
			if code := postConfig(f, path, body); code != http.StatusBadRequest {
				t.Fatalf("POST = %d, want 400", code)
			}
			if store.writes != before {
				t.Fatal("an invalid body was written")
			}
			requireConfig(t, configOf(t, f, testToken), saved)
		})
	}

	// Unauthenticated writes are refused before the body is read.
	if code := f.do(http.MethodPost, path, `{"SubtitleMode":"None"}`, "X-Other", "x").Code; code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated POST = %d, want 401", code)
	}
	requireConfig(t, configOf(t, f, testToken), saved)
}

// TestUserConfigurationIsPerStream covers two streams: each reads back only
// its own preferences, and the user id in the path does not pick the stream
// written — the token does.
func TestUserConfigurationIsPerStream(t *testing.T) {
	f, _ := newUserConfigFixture()
	// The living room's token, aimed at the kitchen's user id.
	if code := postConfig(f, "/jellyfin/Users/"+userID("kitchen")+"/Configuration", `{"AudioLanguagePreference":"eng"}`); code != http.StatusNoContent {
		t.Fatalf("POST = %d", code)
	}
	req := f.do(http.MethodPost, "/jellyfin/Users/"+userID("kitchen")+"/Configuration", `{"AudioLanguagePreference":"spa","SubtitleMode":"None"}`,
		"X-Emby-Token", testOtherToken)
	if req.Code != http.StatusNoContent {
		t.Fatalf("kitchen POST = %d", req.Code)
	}
	requireConfig(t, configOf(t, f, testToken), wantConfig{audio: "eng", subtitle: "", mode: "Default", playDefault: true})
	requireConfig(t, configOf(t, f, testOtherToken), wantConfig{audio: "spa", subtitle: "", mode: "None", playDefault: true})
}

// TestUserConfigurationDefaultsWithoutStore covers a server with no store, or
// a stream that saved nothing: the configuration reads exactly as it did
// before preferences were kept, and a valid write is still answered 204.
func TestUserConfigurationDefaultsWithoutStore(t *testing.T) {
	f := newFixture()
	requireConfig(t, configOf(t, f, testToken), defaultsWant)
	if code := postConfig(f, "/jellyfin/Users/"+userID("living-room")+"/Configuration", `{"AudioLanguagePreference":"eng"}`); code != http.StatusNoContent {
		t.Fatalf("POST without a store = %d, want 204", code)
	}
	if code := postConfig(f, "/jellyfin/Users/"+userID("living-room")+"/Configuration", `{"SubtitleMode":"Bogus"}`); code != http.StatusBadRequest {
		t.Fatalf("invalid POST without a store = %d, want 400", code)
	}
	requireConfig(t, configOf(t, f, testToken), defaultsWant)

	g, _ := newUserConfigFixture()
	requireConfig(t, configOf(t, g, testOtherToken), defaultsWant)
}

// TestUserConfigurationStoreFailures covers a failing store. A failed read
// serves the defaults on GET, but fails a write with 500 rather than merging
// over the defaults and resetting what the body left out; a failed write is
// a 500 too, never a 204 for a choice that was not kept.
func TestUserConfigurationStoreFailures(t *testing.T) {
	f, store := newUserConfigFixture()
	path := "/jellyfin/Users/" + userID("living-room") + "/Configuration"
	if code := postConfig(f, path, `{"AudioLanguagePreference":"eng","PlayDefaultAudioTrack":false}`); code != http.StatusNoContent {
		t.Fatalf("seed POST = %d", code)
	}

	store.readErr = errors.New("database is locked")
	requireConfig(t, configOf(t, f, testToken), defaultsWant)
	if code := postConfig(f, path, `{"SubtitleMode":"Always"}`); code != http.StatusInternalServerError {
		t.Fatalf("POST with a failing read = %d, want 500", code)
	}
	store.readErr = nil
	requireConfig(t, configOf(t, f, testToken), wantConfig{audio: "eng", subtitle: "", mode: "Default", playDefault: false})

	store.writeErr = errors.New("disk full")
	if code := postConfig(f, path, `{"SubtitleMode":"Always"}`); code != http.StatusInternalServerError {
		t.Fatalf("POST with a failing write = %d, want 500", code)
	}
}

// TestParseSubtitleMode covers every form Jellyfin's enum converter reads a
// SubtitlePlaybackMode from — a name in any case, a JSON number, or a string
// holding a number — each normalized to the canonical name, and the values
// it refuses.
func TestParseSubtitleMode(t *testing.T) {
	valid := map[string]string{
		`"Default"`: "Default", `"always"`: "Always", `" OnlyForced "`: "OnlyForced", `"NONE"`: "None", `"smart"`: "Smart",
		`0`: "Default", `1`: "Always", `2`: "OnlyForced", `3`: "None", `4`: "Smart",
		`"0"`: "Default", `"1"`: "Always", `"2"`: "OnlyForced", `"3"`: "None", `"4"`: "Smart", `" 4 "`: "Smart",
	}
	for raw, want := range valid {
		if got, ok := parseSubtitleMode(json.RawMessage(raw)); !ok || got != want {
			t.Errorf("parseSubtitleMode(%s) = %q, %v; want %q", raw, got, ok, want)
		}
	}
	for _, raw := range []string{
		`""`, `null`, `"Sometimes"`, `5`, `-1`, `1.5`, `"5"`, `"-1"`, `"1.5"`, `"2a"`, `"0x2"`, `true`, `[]`, `{}`,
	} {
		if got, ok := parseSubtitleMode(json.RawMessage(raw)); ok {
			t.Errorf("parseSubtitleMode(%s) = %q, accepted", raw, got)
		}
	}
}

// TestUserConfigurationNumericStringMode covers a mode sent as a string
// holding its number: it is stored under its canonical name, and an
// out-of-range one is refused without writing anything.
func TestUserConfigurationNumericStringMode(t *testing.T) {
	f, store := newUserConfigFixture()
	path := "/jellyfin/Users/" + userID("living-room") + "/Configuration"
	if code := postConfig(f, path, `{"SubtitleMode":"2","AudioLanguagePreference":"jpn"}`); code != http.StatusNoContent {
		t.Fatalf("POST = %d, want 204", code)
	}
	want := wantConfig{audio: "jpn", subtitle: "", mode: "OnlyForced", playDefault: true}
	requireConfig(t, configOf(t, f, testToken), want)
	writes := store.writes
	if code := postConfig(f, path, `{"SubtitleMode":"7","AudioLanguagePreference":"eng"}`); code != http.StatusBadRequest {
		t.Fatalf("out-of-range POST = %d, want 400", code)
	}
	if store.writes != writes {
		t.Fatal("an invalid body was written")
	}
	requireConfig(t, configOf(t, f, testToken), want)
}
