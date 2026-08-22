package auth

import "testing"

func TestAuthenticateAcceptsCorrectAdminCredentials(t *testing.T) {
	hash, err := HashPassword("s3cret")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	dm := &StreamManager{}

	stream, err := dm.Authenticate("admin", "s3cret", "admin", hash, "admin-token")
	if err != nil {
		t.Fatalf("Authenticate rejected correct credentials: %v", err)
	}
	if stream.Username != "admin" || stream.Token != "admin-token" {
		t.Fatalf("unexpected stream returned: %+v", stream)
	}
}

func TestAuthenticateAcceptsLegacyStoredHash(t *testing.T) {
	// Existing installs carry an unsalted SHA-256 digest in config.json. They
	// must keep working across the upgrade, or the admin is locked out of their
	// own instance by a release note.
	dm := &StreamManager{}

	stream, err := dm.Authenticate("admin", "admin", "admin", LegacyHashPassword("admin"), "admin-token")
	if err != nil {
		t.Fatalf("Authenticate rejected a valid legacy credential: %v", err)
	}
	if stream.Token != "admin-token" {
		t.Fatalf("unexpected token: %q", stream.Token)
	}
	if !PasswordNeedsRehash(LegacyHashPassword("admin")) {
		t.Fatal("a legacy hash should be reported as needing a rehash after login")
	}
}

func TestAuthenticateRejects(t *testing.T) {
	hash, err := HashPassword("s3cret")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}

	cases := []struct {
		name          string
		loginUsername string
		password      string
		adminUsername string
		storedHash    string
		adminToken    string
	}{
		{"wrong password", "admin", "guess", "admin", hash, "admin-token"},
		{"wrong username", "root", "s3cret", "admin", hash, "admin-token"},
		{"empty password", "admin", "", "admin", hash, "admin-token"},
		// Without a token there is nothing to hand back, so a correct password
		// still must not authenticate.
		{"no admin token", "admin", "s3cret", "admin", hash, ""},
		{"no stored hash", "admin", "", "admin", "", "admin-token"},
		{"renamed admin, old name used", "admin", "s3cret", "operator", hash, "admin-token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dm := &StreamManager{}
			if _, err := dm.Authenticate(tc.loginUsername, tc.password, tc.adminUsername, tc.storedHash, tc.adminToken); err == nil {
				t.Fatal("Authenticate accepted credentials it should have rejected")
			}
		})
	}
}

func TestAuthenticateDefaultsAdminUsername(t *testing.T) {
	hash, err := HashPassword("s3cret")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	dm := &StreamManager{}

	if _, err := dm.Authenticate("admin", "s3cret", "", hash, "admin-token"); err != nil {
		t.Fatalf("an empty admin username should fall back to \"admin\": %v", err)
	}
}

func TestAuthenticateTokenMatchesAdminAndStreams(t *testing.T) {
	dm := &StreamManager{
		streams: map[string]*Stream{
			"living-room": {Username: "living-room", Token: "stream-token"},
		},
	}

	stream, err := dm.AuthenticateToken("admin-token", "admin", "admin-token")
	if err != nil {
		t.Fatalf("AuthenticateToken rejected the admin token: %v", err)
	}
	if stream.Username != "admin" {
		t.Fatalf("expected the admin stream, got %q", stream.Username)
	}

	stream, err = dm.AuthenticateToken("stream-token", "admin", "admin-token")
	if err != nil {
		t.Fatalf("AuthenticateToken rejected a valid stream token: %v", err)
	}
	if stream.Username != "living-room" {
		t.Fatalf("expected the living-room stream, got %q", stream.Username)
	}

	for _, token := range []string{"", "stream-toke", "stream-tokenn", "nope"} {
		if _, err := dm.AuthenticateToken(token, "admin", "admin-token"); err == nil {
			t.Fatalf("AuthenticateToken accepted %q", token)
		}
	}

	// An unset admin token must not turn the empty string into a master key.
	if _, err := dm.AuthenticateToken("", "admin", ""); err == nil {
		t.Fatal("AuthenticateToken accepted an empty token against an unset admin token")
	}
}
