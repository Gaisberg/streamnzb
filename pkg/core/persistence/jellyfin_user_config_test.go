package persistence

import (
	"strings"
	"sync"
	"testing"
)

// setUserConfig replaces the stream's row whole.
func setUserConfig(store *JellyfinPlaystateStore, stream string, cfg JellyfinUserConfig) error {
	return store.UpdateUserConfig(stream, func(JellyfinUserConfig) JellyfinUserConfig { return cfg })
}

func getUserConfig(t *testing.T, store *JellyfinPlaystateStore, stream string) JellyfinUserConfig {
	t.Helper()
	cfg, err := store.GetUserConfig(stream)
	if err != nil {
		t.Fatalf("get user config %q: %v", stream, err)
	}
	return cfg
}

// TestJellyfinUserConfigSurvivesRestart covers the store a client's saved
// language preferences live in: a stream without a row reads the defaults, a
// write is read back whole, a second write replaces it, and both outlive the
// process.
func TestJellyfinUserConfigSurvivesRestart(t *testing.T) {
	db := suiteDB(t)
	m := db.open(t)
	store := m.JellyfinPlaystateStore()

	if got := getUserConfig(t, store, "alice"); got != DefaultJellyfinUserConfig() {
		t.Fatalf("a stream that saved nothing reads %+v, want the defaults", got)
	}
	// An update of a stream with no row starts from the defaults.
	if err := store.UpdateUserConfig("alice", func(cur JellyfinUserConfig) JellyfinUserConfig {
		if cur != DefaultJellyfinUserConfig() {
			t.Errorf("update started from %+v, want the defaults", cur)
		}
		cur.AudioLanguagePreference = "eng"
		return cur
	}); err != nil {
		t.Fatalf("first update: %v", err)
	}
	first := JellyfinUserConfig{AudioLanguagePreference: "eng", SubtitleMode: "Default", PlayDefaultAudioTrack: true}
	if got := getUserConfig(t, store, "alice"); got != first {
		t.Fatalf("read back %+v, want %+v", got, first)
	}
	second := JellyfinUserConfig{AudioLanguagePreference: "jpn", SubtitleLanguagePreference: "eng", SubtitleMode: "OnlyForced"}
	if err := setUserConfig(store, " alice ", second); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened := db.open(t)
	t.Cleanup(func() { _ = reopened.Close() })
	if got := getUserConfig(t, reopened.JellyfinPlaystateStore(), "alice"); got != second {
		t.Fatalf("after restart %+v, want %+v", got, second)
	}
}

// TestJellyfinUserConfigUpdatesDoNotLoseEachOther covers concurrent partial
// updates of one stream: each sees the row the one before it wrote, so none
// of their changes is lost.
func TestJellyfinUserConfigUpdatesDoNotLoseEachOther(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()

	const writers = 40
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := store.UpdateUserConfig("alice", func(cur JellyfinUserConfig) JellyfinUserConfig {
				cur.AudioLanguagePreference += "x"
				return cur
			})
			if err != nil {
				t.Errorf("update: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := getUserConfig(t, store, "alice"); got.AudioLanguagePreference != strings.Repeat("x", writers) {
		t.Fatalf("lost updates: %q, want %d writes", got.AudioLanguagePreference, writers)
	}
}

func TestJellyfinUserConfigIsPerStream(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()

	alice := JellyfinUserConfig{AudioLanguagePreference: "eng", SubtitleMode: "Default", PlayDefaultAudioTrack: true}
	bob := JellyfinUserConfig{AudioLanguagePreference: "deu", SubtitleLanguagePreference: "deu", SubtitleMode: "Smart"}
	if err := setUserConfig(store, "alice", alice); err != nil {
		t.Fatalf("set alice: %v", err)
	}
	if err := setUserConfig(store, "bob", bob); err != nil {
		t.Fatalf("set bob: %v", err)
	}
	if got := getUserConfig(t, store, "alice"); got != alice {
		t.Fatalf("alice = %+v", got)
	}
	if got := getUserConfig(t, store, "bob"); got != bob {
		t.Fatalf("bob = %+v", got)
	}
	if err := setUserConfig(store, "  ", alice); err == nil {
		t.Fatal("a blank stream name was accepted")
	}
}

// TestJellyfinUserConfigFollowsTheStream covers rename and delete. A rename
// moves the row, without counting it as history, and displaces whatever a
// deleted stream left under the new name, even when the renamed stream has no
// row to bring; a delete drops it with the play state, and only for that
// stream.
func TestJellyfinUserConfigFollowsTheStream(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()
	defaults := DefaultJellyfinUserConfig()
	mine := JellyfinUserConfig{AudioLanguagePreference: "eng", SubtitleMode: "Always"}
	stale := JellyfinUserConfig{AudioLanguagePreference: "fra", SubtitleMode: "None", PlayDefaultAudioTrack: true}

	if err := setUserConfig(store, "living-room", mine); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := setUserConfig(store, "tv", stale); err != nil {
		t.Fatalf("set stale: %v", err)
	}
	moved, err := m.RenameStreamReferences("living-room", "tv")
	if err != nil {
		t.Fatalf("rename onto stale row: %v", err)
	}
	if moved != 0 {
		t.Fatalf("rename counted %d history rows for a stream with only a user config", moved)
	}
	if got := getUserConfig(t, store, "living-room"); got != defaults {
		t.Fatalf("old name kept its row: %+v", got)
	}
	if got := getUserConfig(t, store, "tv"); got != mine {
		t.Fatalf("renamed row %+v, want %+v", got, mine)
	}

	// A stream with no row renamed onto a stale one is left on the defaults.
	if err := setUserConfig(store, "den", stale); err != nil {
		t.Fatalf("set stale den: %v", err)
	}
	if _, err := m.RenameStreamReferences("kitchen", "den"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got := getUserConfig(t, store, "den"); got != defaults {
		t.Fatalf("renamed stream inherited a stale row: %+v", got)
	}

	if err := setUserConfig(store, "tv-2", stale); err != nil {
		t.Fatalf("set tv-2: %v", err)
	}
	if err := store.Upsert(JellyfinPlaystate{StreamName: "tv", ItemID: "a", ContentType: "movie", ContentID: "tta", PositionTicks: 5}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.DeleteForStream("tv"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := getUserConfig(t, store, "tv"); got != defaults {
		t.Fatalf("user config survived the delete: %+v", got)
	}
	if rows := store.ListForStream("tv"); len(rows) != 0 {
		t.Fatalf("play state survived the delete: %+v", rows)
	}
	if got := getUserConfig(t, store, "tv-2"); got != stale {
		t.Fatalf("a name sharing the prefix lost its row: %+v", got)
	}
}
