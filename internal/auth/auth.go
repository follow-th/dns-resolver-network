// Package auth handles API keys: generation, SHA-256 hashing and constant-time matching.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

const keyPrefix = "drn_"

// GenerateKey returns a new random key and its SHA-256 hex hash.
func GenerateKey() (key, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	key = keyPrefix + hex.EncodeToString(b)
	return key, Hash(key), nil
}

// Hash returns the SHA-256 hex digest of a key.
func Hash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// ValidHash reports whether s is a 64-character hex SHA-256 digest.
func ValidHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// Set is a collection of key hashes.
type Set struct {
	hashes [][]byte
	ids    []string
}

// NewSet builds a Set from hex-encoded SHA-256 hashes.
func NewSet(hexHashes []string) (*Set, error) {
	s := &Set{}
	for _, h := range hexHashes {
		h = strings.ToLower(strings.TrimSpace(h))
		if !ValidHash(h) {
			return nil, fmt.Errorf("invalid key hash %q: want 64 hex characters", truncate(h))
		}
		raw, _ := hex.DecodeString(h)
		s.hashes = append(s.hashes, raw)
		s.ids = append(s.ids, h)
	}
	return s, nil
}

// Len returns the number of hashes in the set.
func (s *Set) Len() int { return len(s.hashes) }

// Match returns the hash (hex) of the matching entry. Every entry is compared
// so the time taken does not depend on which entry matched.
func (s *Set) Match(key string) (id string, ok bool) {
	if key == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(key))
	for i, h := range s.hashes {
		if subtle.ConstantTimeCompare(sum[:], h) == 1 {
			id, ok = s.ids[i], true
		}
	}
	return id, ok
}

// BearerToken extracts the token from an "Authorization: Bearer <token>" header.
func BearerToken(h http.Header) string {
	v := h.Get("Authorization")
	const p = "bearer "
	if len(v) > len(p) && strings.EqualFold(v[:len(p)], p) {
		return strings.TrimSpace(v[len(p):])
	}
	return ""
}

func truncate(s string) string {
	if len(s) > 8 {
		return s[:8] + "..."
	}
	return s
}
