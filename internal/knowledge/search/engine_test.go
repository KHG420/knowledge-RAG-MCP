package search

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/logging"
)

func TestSetKBNameBindsMatchingVectorIndex(t *testing.T) {
	first := knowledge.NewHNSWIndex(2)
	second := knowledge.NewHNSWIndex(2)
	state := &knowledge.VectorIndexState{}
	state.SetCache(map[string]*knowledge.HNSWIndex{
		"first":  first,
		"second": second,
	})

	engine := New(&sync.Mutex{}, logging.NewNopLogger())
	engine.SetVecState(state)

	engine.SetKBName("first")
	if engine.vectorIndex != first {
		t.Fatal("first KB did not bind its vector index")
	}

	engine.SetKBName("second")
	if engine.vectorIndex != second {
		t.Fatal("second KB did not replace the previous vector index")
	}

	engine.SetKBName("missing")
	if engine.vectorIndex != nil {
		t.Fatal("cache miss retained a stale vector index")
	}
}

func TestCacheQueryHash_ProducesDifferentKeysForDifferentInputs(t *testing.T) {
	a := cacheQueryHash("hello world", "hybrid", 10, knowledge.SearchFilter{SourceType: "type-a", Section: "sec-1"})
	b := cacheQueryHash("hello world", "hybrid", 10, knowledge.SearchFilter{SourceType: "type-b", Section: "sec-1"})
	c := cacheQueryHash("hello world", "hybrid", 20, knowledge.SearchFilter{SourceType: "type-a", Section: "sec-1"})
	d := cacheQueryHash("different query", "hybrid", 10, knowledge.SearchFilter{SourceType: "type-a", Section: "sec-1"})

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
	filter := knowledge.SearchFilter{SourceType: "type-a", Section: "sec-1"}
	a := cacheQueryHash("hello world", "hybrid", 10, filter)
	b := cacheQueryHash("hello world", "hybrid", 10, filter)

	if a != b {
		t.Errorf("same input should produce same hash, got %q vs %q", a, b)
	}
}

func TestCacheQueryHash_UsesSHA256(t *testing.T) {
	// Verify that the hash is actually a SHA-256 hex digest (64 chars)
	h := cacheQueryHash("hello", "bm25", 5, knowledge.SearchFilter{SourceType: "doc", Section: "main"})
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
	h := cacheQueryHash(query, "bm25", 10, knowledge.SearchFilter{SourceType: "doc", Section: "main"})
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

func TestCacheQueryHashIncludesEveryResultChangingFilter(t *testing.T) {
	base := knowledge.SearchFilter{}
	baseHash := cacheQueryHash("query", "hybrid", 10, base)
	filters := []knowledge.SearchFilter{
		{DocSlug: "doc"},
		{SourceType: "paper"},
		{Section: "methods"},
		{Tags: []string{"stability"}},
		{AddedAfter: time.Unix(100, 0)},
		{AddedBefore: time.Unix(200, 0)},
		{Coarse: true},
	}
	for _, filter := range filters {
		if got := cacheQueryHash("query", "hybrid", 10, filter); got == baseHash {
			t.Fatalf("filter %+v did not affect cache key", filter)
		}
	}
}

func TestWithKBReturnsIndependentSearchViews(t *testing.T) {
	first := knowledge.NewHNSWIndex(2)
	second := knowledge.NewHNSWIndex(2)
	state := &knowledge.VectorIndexState{}
	state.SetCache(map[string]*knowledge.HNSWIndex{"first": first, "second": second})
	engine := New(&sync.Mutex{}, logging.NewNopLogger())
	engine.SetVecState(state)

	firstView := engine.WithKB("first", nil).(*Engine)
	secondView := engine.WithKB("second", nil).(*Engine)

	if engine.KBName() != "" || firstView.KBName() != "first" || secondView.KBName() != "second" {
		t.Fatal("KB views mutated one another")
	}
	if firstView.vectorIndex != first || secondView.vectorIndex != second {
		t.Fatal("KB views did not bind independent vector indexes")
	}
}

func TestSortByRerankScoresDownranksReferenceSections(t *testing.T) {
	entries := []searchEntry{
		{chunkID: "reference", sectionRole: "references"},
		{chunkID: "body", sectionRole: "methodology"},
	}
	got, scores := sortByRerankScores(entries, []float64{0.99, 0.90})
	if got[0].chunkID != "body" {
		t.Fatalf("reference section outranked explanatory body: %+v", got)
	}
	if scores[1] >= 0.99 {
		t.Fatal("reference penalty was not applied")
	}
}
