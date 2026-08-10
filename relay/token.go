package relay

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Token handling for agent authentication.
//
// Each agent gets its own token rather than sharing one passcode across the
// fleet. That way a single compromised box can be revoked without re-keying
// every other server, and the hub knows which agent is talking without
// trusting the name the agent claims.
//
// The hub stores only argon2id hashes. A leaked agents database does not let
// an attacker connect.

const (
	tokenBytes = 32

	argonTime    = 1
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// NewToken returns a fresh random agent token, URL-safe so it survives being
// pasted through a terminal or a chat message.
func NewToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken derives a storable argon2id hash of a token.
func HashToken(token string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	key := argon2.IDKey([]byte(token), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$%d$%d$%d$%s$%s",
		argonTime, argonMemory, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyToken reports whether token matches the stored hash. Comparison is
// constant time so a caller cannot learn the hash by measuring how long a
// rejection takes.
func VerifyToken(token, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "argon2id" {
		return false
	}

	var time, memory uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[1], "%d", &time); err != nil {
		return false
	}
	if _, err := fmt.Sscanf(parts[2], "%d", &memory); err != nil {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "%d", &threads); err != nil {
		return false
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}

	got := argon2.IDKey([]byte(token), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
