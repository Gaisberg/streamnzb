package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// Device represents an authenticated user/device identity.
type Device struct {
	Username string `json:"username"`
	Token    string `json:"token"`
}

// HashPassword returns the SHA-256 hex digest of a password.
func HashPassword(password string) string {
	hash := sha256.Sum256([]byte(password))
	return hex.EncodeToString(hash[:])
}

// GenerateToken creates a cryptographically random 64-char hex token.
func GenerateToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	hash := sha256.Sum256(bytes)
	return hex.EncodeToString(hash[:]), nil
}
