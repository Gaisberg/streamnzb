package auth

import (
	"path/filepath"
	"testing"

	"streamnzb/pkg/core/config"
)

// streamsFor builds a manager holding the given streams, backed by a config
// in a temp directory so a save writes nothing outside the test.
func streamsFor(t *testing.T, streams ...*Stream) *StreamManager {
	t.Helper()
	cfg := &config.Config{
		LoadedPath: filepath.Join(t.TempDir(), "config.json"),
		Streams:    make(map[string]*config.StreamEntry, len(streams)),
	}
	for _, s := range streams {
		cfg.Streams[s.Username] = streamToEntry(s)
	}
	dm := &StreamManager{cfg: cfg, saveFn: func() error { return nil }}
	if err := dm.load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	return dm
}

func TestAuthenticateStreamAcceptsPasswordAndToken(t *testing.T) {
	hash, err := HashPassword("room-pw")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	dm := streamsFor(t, &Stream{Username: "living-room", Token: "tok-living-room", PasswordHash: hash})

	for _, credential := range []string{"room-pw", "tok-living-room"} {
		stream, err := dm.AuthenticateStream("living-room", credential)
		if err != nil {
			t.Fatalf("rejected %q: %v", credential, err)
		}
		if stream.Username != "living-room" {
			t.Fatalf("wrong stream for %q: %+v", credential, stream)
		}
	}

	// The name is matched the way a user types it on a remote.
	if _, err := dm.AuthenticateStream("Living-Room", "room-pw"); err != nil {
		t.Fatalf("rejected a differently cased name: %v", err)
	}
}

func TestAuthenticateStreamRejects(t *testing.T) {
	hash, err := HashPassword("room-pw")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	dm := streamsFor(t,
		&Stream{Username: "living-room", Token: "tok-living-room", PasswordHash: hash},
		&Stream{Username: "kitchen", Token: "tok-kitchen"},
	)

	cases := []struct{ name, username, password, why string }{
		{"wrong password", "living-room", "nope", "a wrong password must not sign in"},
		{"empty password", "living-room", "", "an empty password must not sign in"},
		{"unknown stream", "nobody", "room-pw", "an unknown stream must not sign in"},
		{"another stream's password", "kitchen", "room-pw", "a password must not sign in under another stream's name"},
		{"another stream's token", "kitchen", "tok-living-room", "a token must not sign in under another stream's name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := dm.AuthenticateStream(c.username, c.password); err == nil {
				t.Fatal(c.why)
			}
		})
	}
}

// A stream with no password set is reachable by its token alone, so adding
// passwords cannot lock anyone out of a stream they already had.
func TestAuthenticateStreamWithoutAPasswordStillTakesItsToken(t *testing.T) {
	dm := streamsFor(t, &Stream{Username: "kitchen", Token: "tok-kitchen"})

	if _, err := dm.AuthenticateStream("kitchen", "tok-kitchen"); err != nil {
		t.Fatalf("token rejected for a stream with no password: %v", err)
	}
	// An empty stored hash must not mean "any password will do".
	if _, err := dm.AuthenticateStream("kitchen", "anything"); err == nil {
		t.Fatal("a stream with no password accepted an arbitrary one")
	}
	if _, err := dm.AuthenticateStream("kitchen", ""); err == nil {
		t.Fatal("a stream with no password accepted an empty one")
	}
}

func TestSetStreamPasswordStoresAHashAndClears(t *testing.T) {
	dm := streamsFor(t, &Stream{Username: "kitchen", Token: "tok-kitchen"})

	if err := dm.SetStreamPassword("kitchen", "hunter2"); err != nil {
		t.Fatalf("SetStreamPassword: %v", err)
	}
	stored := dm.streams["kitchen"].PasswordHash
	if stored == "" || stored == "hunter2" {
		t.Fatalf("password not stored as a hash: %q", stored)
	}
	if _, err := dm.AuthenticateStream("kitchen", "hunter2"); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}

	// Clearing leaves the token as the way in, not an open door.
	if err := dm.SetStreamPassword("kitchen", ""); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if dm.streams["kitchen"].PasswordHash != "" {
		t.Fatal("password was not cleared")
	}
	if _, err := dm.AuthenticateStream("kitchen", "hunter2"); err == nil {
		t.Fatal("cleared password still signs in")
	}
	if _, err := dm.AuthenticateStream("kitchen", "tok-kitchen"); err != nil {
		t.Fatalf("token rejected after clearing the password: %v", err)
	}

	if err := dm.SetStreamPassword("nobody", "x"); err == nil {
		t.Fatal("setting a password on an unknown stream should fail")
	}
}
