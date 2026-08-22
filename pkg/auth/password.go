package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Admin passwords are stored as argon2id in the PHC string format:
//
//	$argon2id$v=19$m=19456,t=2,p=1$<base64 salt>$<base64 key>
//
// The parameters carry inside the string rather than being read from these
// constants at verification time, so raising them later keeps every existing
// hash verifiable — only newly written hashes use the stronger settings, and
// PasswordNeedsRehash spots the old ones so they get upgraded on next login.
const (
	// OWASP's minimum recommended argon2id configuration. StreamNZB runs on
	// hardware as small as a Raspberry Pi or an arm64 VPS, where the more
	// commonly quoted 64 MiB would be a memory spike an attacker could aim at
	// deliberately (see verifySlots below).
	argon2MemoryKiB  = 19456
	argon2Time       = 2
	argon2Threads    = 1
	argon2SaltLength = 16
	argon2KeyLength  = 32
)

// legacyHashLength is the length of the unsalted hex SHA-256 digests written
// before argon2id. A stored hash of exactly this length that is valid hex is
// treated as legacy; anything else must parse as a PHC string.
const legacyHashLength = sha256.Size * 2

// verifySlots bounds how many argon2id computations run at once. Each one
// allocates argon2MemoryKiB, so an unbounded login endpoint would let a burst
// of requests exhaust memory on small hardware — the cost that makes the hash
// expensive to attack makes it expensive to serve. Four concurrent
// verifications cap the transient footprint at roughly 76 MiB, and a real admin
// never has more than one login in flight.
var verifySlots = make(chan struct{}, 4)

var errMalformedHash = errors.New("malformed password hash")

// HashPassword returns a new argon2id hash of password in PHC string format.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argon2SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argon2Time, argon2MemoryKiB, argon2Threads, argon2KeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argon2MemoryKiB, argon2Time, argon2Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether password matches encoded, which may be either
// an argon2id PHC string or one of the legacy unsalted SHA-256 digests.
//
// A malformed or empty stored hash never matches, but still costs the same work
// as a real one: callers verify on every login attempt, including ones with the
// wrong username, so that a wrong username cannot be told from a wrong password
// by how long the answer took.
func VerifyPassword(password, encoded string) bool {
	if isLegacyHash(encoded) {
		return subtle.ConstantTimeCompare([]byte(legacyHashPassword(password)), []byte(encoded)) == 1
	}

	params, salt, want, err := parseArgon2Hash(encoded)
	if err != nil {
		// Burn the same work an intact hash would have cost. Without this a
		// blank or corrupt stored hash answers instantly, which tells an
		// attacker the instance has no password set.
		decoyVerify(password)
		return false
	}

	verifySlots <- struct{}{}
	got := argon2.IDKey([]byte(password), salt, params.time, params.memoryKiB, params.threads, uint32(len(want)))
	<-verifySlots

	return subtle.ConstantTimeCompare(got, want) == 1
}

// PasswordNeedsRehash reports whether encoded should be replaced by a fresh
// hash after a successful login: it is either a legacy SHA-256 digest, or an
// argon2id hash written with weaker parameters than the current constants.
func PasswordNeedsRehash(encoded string) bool {
	if isLegacyHash(encoded) {
		return true
	}
	params, _, _, err := parseArgon2Hash(encoded)
	if err != nil {
		// Unparseable hashes cannot be verified against, so nothing will ever
		// log in to trigger the rehash. Say no and let the caller fail the
		// login rather than promising an upgrade that cannot happen.
		return false
	}
	return params.memoryKiB < argon2MemoryKiB || params.time < argon2Time
}

// LegacyHashPassword is the pre-argon2id hashing scheme, kept only so config
// defaults and migration tests can produce a hash in the old format. Never use
// it to store a new password.
func LegacyHashPassword(password string) string { return legacyHashPassword(password) }

func legacyHashPassword(password string) string {
	sum := sha256.Sum256([]byte(password))
	return hex.EncodeToString(sum[:])
}

func isLegacyHash(encoded string) bool {
	if len(encoded) != legacyHashLength {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}

// decoyVerify runs one argon2id computation whose result is discarded, so an
// unusable stored hash takes as long to reject as a usable one.
func decoyVerify(password string) {
	verifySlots <- struct{}{}
	argon2.IDKey([]byte(password), make([]byte, argon2SaltLength), argon2Time, argon2MemoryKiB, argon2Threads, argon2KeyLength)
	<-verifySlots
}

type argon2Params struct {
	memoryKiB uint32
	time      uint32
	threads   uint8
}

func parseArgon2Hash(encoded string) (argon2Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	// A PHC string starts with an empty field, since it opens with "$".
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return argon2Params{}, nil, nil, errMalformedHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return argon2Params{}, nil, nil, errMalformedHash
	}
	if version != argon2.Version {
		return argon2Params{}, nil, nil, fmt.Errorf("%w: unsupported argon2 version %d", errMalformedHash, version)
	}

	var params argon2Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.memoryKiB, &params.time, &params.threads); err != nil {
		return argon2Params{}, nil, nil, errMalformedHash
	}
	if params.memoryKiB == 0 || params.time == 0 || params.threads == 0 {
		return argon2Params{}, nil, nil, errMalformedHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return argon2Params{}, nil, nil, errMalformedHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return argon2Params{}, nil, nil, errMalformedHash
	}

	return params, salt, key, nil
}
