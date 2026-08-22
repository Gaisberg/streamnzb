package auth

import (
	"fmt"
	"strings"
	"testing"
)

func TestHashPasswordRoundTrips(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("expected a PHC argon2id string, got %q", hash)
	}
	if !VerifyPassword("correct horse battery staple", hash) {
		t.Fatal("the password that produced the hash did not verify against it")
	}
	if VerifyPassword("Correct horse battery staple", hash) {
		t.Fatal("a different password verified against the hash")
	}
}

func TestHashPasswordSaltsEachHash(t *testing.T) {
	first, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	second, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	if first == second {
		t.Fatal("hashing the same password twice produced identical output, so it is not salted")
	}
	if !VerifyPassword("same-password", second) {
		t.Fatal("the second hash did not verify")
	}
}

func TestVerifyPasswordAcceptsLegacyHashes(t *testing.T) {
	legacy := LegacyHashPassword("admin")
	if len(legacy) != legacyHashLength {
		t.Fatalf("expected a %d-character legacy digest, got %d", legacyHashLength, len(legacy))
	}
	if !VerifyPassword("admin", legacy) {
		t.Fatal("the legacy default password did not verify against its legacy hash")
	}
	if VerifyPassword("wrong", legacy) {
		t.Fatal("a wrong password verified against a legacy hash")
	}
}

func TestVerifyPasswordRejectsUnusableHashes(t *testing.T) {
	// Every one of these must fail closed. An empty hash in particular is what
	// a half-initialised config holds, and it must not authenticate anyone.
	unusable := []struct {
		name string
		hash string
	}{
		{"empty", ""},
		{"not a hash", "hunter2"},
		{"truncated phc", "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA"},
		{"wrong algorithm", "$argon2i$v=19$m=19456,t=2,p=1$c2FsdA$a2V5"},
		{"unsupported version", "$argon2id$v=16$m=19456,t=2,p=1$c2FsdA$a2V5"},
		{"zero parameters", "$argon2id$v=19$m=0,t=0,p=0$c2FsdA$a2V5"},
		{"bad base64 salt", "$argon2id$v=19$m=19456,t=2,p=1$!!!!$a2V5"},
		{"legacy length but not hex", strings.Repeat("z", legacyHashLength)},
	}
	for _, tc := range unusable {
		t.Run(tc.name, func(t *testing.T) {
			if VerifyPassword("anything", tc.hash) {
				t.Fatalf("an unusable hash (%s) authenticated a password", tc.name)
			}
		})
	}
}

func TestPasswordNeedsRehash(t *testing.T) {
	current, err := HashPassword("whatever")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	// A hash written with the parameters of an older release: same format,
	// weaker settings, so it should be replaced on the next login.
	weaker := fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=1$c2FsdHNhbHRzYWx0c2E$a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2U",
		argon2MemoryKiB/2, argon2Time)

	cases := []struct {
		name string
		hash string
		want bool
	}{
		{"legacy sha256", LegacyHashPassword("admin"), true},
		{"weaker parameters", weaker, true},
		{"current parameters", current, false},
		// Nothing can log in against a malformed hash, so there is no moment at
		// which an upgrade could be applied; claiming otherwise would be a lie.
		{"malformed", "not-a-hash", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PasswordNeedsRehash(tc.hash); got != tc.want {
				t.Fatalf("PasswordNeedsRehash(%q) = %v, want %v", tc.hash, got, tc.want)
			}
		})
	}
}
