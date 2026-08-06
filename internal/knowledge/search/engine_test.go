package search

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestCacheQueryHash_ProducesDifferentKeysForDifferentInputs(t *testing.T) {
	a := cacheQueryHash("hello world", "hybrid", 10, "type-a", "sec-1")
	b := cacheQueryHash("hello world", "hybrid", 10, "type-b", "sec-1")
	c := cacheQueryHash("hello world", "hybrid", 20, "type-a", "sec-1")
	d := cacheQueryHash("different query", "hybrid", 10, "type-a", "sec-1")

	if a == b {
		t.Error("different sourceType should produce different hashes")
	}
	if a == c {
		t.Error("different limit should produce different hashes")
	}
	if a == d {
		t.Error("different query should produce different hashes")
	}
}

func TestCacheQueryHash_ProducesSameKeyForSameInputs(t *testing.T) {
	a := cacheQueryHash("hello world", "hybrid", 10, "type-a", "sec-1")
	b := cacheQueryHash("hello world", "hybrid", 10, "type-a", "sec-1")

	if a != b {
		t.Errorf("same input should produce same hash, got %q vs %q", a, b)
	}
}

func TestCacheQueryHash_UsesSHA256(t *testing.T) {
	// Verify that the hash is actually a SHA-256 hex digest (64 chars)
	h := cacheQueryHash("hello", "bm25", 5, "doc", "main")
	if len(h) != sha256.Size*2 {
		t.Errorf("expected hash length %d, got %d", sha256.Size*2, len(h))
	}
	// Should be all lowercase hex
	for _, c := range h {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("hash should be all lowercase hex, got %q", h)
			break
		}
	}
}

func TestCacheQueryHash_DoesNotContainRawInput(t *testing.T) {
	query := "this is a very long and sensitive query that should not be visible in the key"
	h := cacheQueryHash(query, "bm25", 10, "doc", "main")
	// The hash should be a hex string, not containing the query
	if strings.Contains(h, query) {
		t.Error("hash should not contain the raw query text")
	}
	// Verify it's a valid SHA-256 hash
	_, err := hex.DecodeString(h)
	if err != nil {
		t.Errorf("hash should be valid hex: %v", err)
	}
}
