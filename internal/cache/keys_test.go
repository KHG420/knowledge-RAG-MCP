package cache

import (
	"strings"
	"testing"
)

// ─── NormalizeQuery ──────────────────────────────────────────────────────────

func TestNormalizeQuery_Empty(t *testing.T) {
	if got := NormalizeQuery(""); got != "" {
		t.Errorf("NormalizeQuery('') = %q, want ''", got)
	}
}

func TestNormalizeQuery_WhitespaceOnly(t *testing.T) {
	if got := NormalizeQuery("   \t  \n  "); got != "" {
		t.Errorf("NormalizeQuery(whitespace) = %q, want ''", got)
	}
}

func TestNormalizeQuery_SingleTerm(t *testing.T) {
	if got := NormalizeQuery("hello"); got != "hello" {
		t.Errorf("NormalizeQuery('hello') = %q, want 'hello'", got)
	}
}

func TestNormalizeQuery_SingleTermUppercase(t *testing.T) {
	if got := NormalizeQuery("HELLO"); got != "hello" {
		t.Errorf("NormalizeQuery('HELLO') = %q, want 'hello'", got)
	}
}

func TestNormalizeQuery_TwoTerms_SameOrder(t *testing.T) {
	got := NormalizeQuery("hello world")
	if got != "hello world" {
		t.Errorf("NormalizeQuery('hello world') = %q, want 'hello world'", got)
	}
}

func TestNormalizeQuery_TwoTerms_Reversed(t *testing.T) {
	// Same terms, different order → same normalized form
	a := NormalizeQuery("hello world")
	b := NormalizeQuery("world hello")
	if a != b {
		t.Errorf("NormalizeQuery should be order-independent: %q != %q", a, b)
	}
}

func TestNormalizeQuery_CaseInsensitive(t *testing.T) {
	a := NormalizeQuery("Hello World")
	b := NormalizeQuery("hello world")
	if a != b {
		t.Errorf("NormalizeQuery should be case-insensitive: %q != %q", a, b)
	}
}

func TestNormalizeQuery_Deduplicate(t *testing.T) {
	got := NormalizeQuery("hello hello world world world")
	if got != "hello world" {
		t.Errorf("NormalizeQuery should deduplicate: got %q, want 'hello world'", got)
	}
}

func TestNormalizeQuery_DeduplicateWithOrder(t *testing.T) {
	// After sort + dedup: should be "a b c"
	got := NormalizeQuery("c b a a b c")
	if got != "a b c" {
		t.Errorf("got %q, want 'a b c'", got)
	}
}

func TestNormalizeQuery_ExtraWhitespace(t *testing.T) {
	got := NormalizeQuery("  hello    world  ")
	if got != "hello world" {
		t.Errorf("NormalizeQuery with extra whitespace = %q, want 'hello world'", got)
	}
}

func TestNormalizeQuery_CJK(t *testing.T) {
	got := NormalizeQuery("深度学习 机器学习")
	// CJK tokens are space-separated, sorted alphabetically (Unicode order)
	// Both are distinct terms; sort lexicographically.
	if !strings.Contains(got, "深度学习") || !strings.Contains(got, "机器学习") {
		t.Errorf("CJK terms should both appear: got %q", got)
	}
}

func TestNormalizeQuery_CJKMixedWithLatin(t *testing.T) {
	got := NormalizeQuery("RAG 检索 增强 生成")
	// Should contain all 4 terms in sorted order
	if got == "" {
		t.Error("mixed CJK+Latin should not be empty")
	}
	// Check key terms all present
	for _, term := range []string{"rag", "检索", "增强", "生成"} {
		if !strings.Contains(got, term) {
			t.Errorf("%q should be in normalized output %q", term, got)
		}
	}
}

func TestNormalizeQuery_SingleAfterDedup(t *testing.T) {
	got := NormalizeQuery("same same same")
	if got != "same" {
		t.Errorf("all-same dedup = %q, want 'same'", got)
	}
}

func TestNormalizeQuery_ManyUniqueTerms(t *testing.T) {
	// 100 unique terms: sorted alphabetically
	terms := make([]string, 100)
	for i := 0; i < 100; i++ {
		terms[i] = string(rune('a' + (i % 26)))
		if i >= 26 {
			terms[i] = terms[i] + string(rune('a'+(i/26)-1))
		}
	}
	input := strings.Join(terms, " ")
	got := NormalizeQuery(input)
	if got == "" {
		t.Error("many unique terms should not return empty")
	}
	// All terms should appear
	count := len(strings.Fields(got))
	if count != 100 {
		t.Errorf("expected 100 unique terms, got %d", count)
	}
}

func TestNormalizeQuery_Numbers(t *testing.T) {
	got := NormalizeQuery("10 2 1 10")
	if got != "1 10 2" {
		t.Errorf("numbers should sort as strings: got %q, want '1 10 2'", got)
	}
}

func TestNormalizeQuery_SpecialCharacters(t *testing.T) {
	got := NormalizeQuery("hello! world? hello!")
	if got != "hello! world?" {
		t.Errorf("special chars: got %q, want 'hello! world?'", got)
	}
}

// ─── QueryHash ───────────────────────────────────────────────────────────────

func TestQueryHash_Deterministic(t *testing.T) {
	h1 := QueryHash("hello world", "hybrid", 10)
	h2 := QueryHash("hello world", "hybrid", 10)
	if h1 != h2 {
		t.Errorf("QueryHash should be deterministic: %q != %q", h1, h2)
	}
}

func TestQueryHash_DifferentQuery(t *testing.T) {
	h1 := QueryHash("hello world", "hybrid", 10)
	h2 := QueryHash("goodbye world", "hybrid", 10)
	if h1 == h2 {
		t.Error("different queries should produce different hashes")
	}
}

func TestQueryHash_DifferentLimit(t *testing.T) {
	h1 := QueryHash("hello", "hybrid", 10)
	h2 := QueryHash("hello", "hybrid", 20)
	if h1 == h2 {
		t.Error("different limits should produce different hashes")
	}
}

func TestQueryHash_DifferentMode(t *testing.T) {
	h1 := QueryHash("hello", "hybrid", 10)
	h2 := QueryHash("hello", "bm25", 10)
	if h1 == h2 {
		t.Error("different modes should produce different hashes")
	}
}

func TestQueryHash_CaseInsensitive(t *testing.T) {
	// NormalizeQuery lowercases — so the hash should be the same
	h1 := QueryHash("Hello World", "hybrid", 10)
	h2 := QueryHash("hello world", "hybrid", 10)
	if h1 != h2 {
		t.Errorf("QueryHash should be case-insensitive: %q != %q", h1, h2)
	}
}

func TestQueryHash_OrderInsensitive(t *testing.T) {
	h1 := QueryHash("hello world", "hybrid", 10)
	h2 := QueryHash("world hello", "hybrid", 10)
	if h1 != h2 {
		t.Errorf("QueryHash should be order-insensitive: %q != %q", h1, h2)
	}
}

func TestQueryHash_DedupInsensitive(t *testing.T) {
	h1 := QueryHash("hello hello world", "hybrid", 10)
	h2 := QueryHash("hello world hello", "hybrid", 10)
	if h1 != h2 {
		t.Errorf("QueryHash should be dedup-insensitive: %q != %q", h1, h2)
	}
}

func TestQueryHash_ExtraParams(t *testing.T) {
	h1 := QueryHash("hello", "hybrid", 10, "filter:pdf")
	h2 := QueryHash("hello", "hybrid", 10, "filter:pdf")
	if h1 != h2 {
		t.Errorf("same extra params should match: %q != %q", h1, h2)
	}
	h3 := QueryHash("hello", "hybrid", 10, "filter:md")
	if h1 == h3 {
		t.Error("different extra params should differ")
	}
}

func TestQueryHash_Length(t *testing.T) {
	h := QueryHash("test query", "hybrid", 10)
	if len(h) != 16 {
		t.Errorf("QueryHash length = %d, want 16", len(h))
	}
}

func TestQueryHash_Empty(t *testing.T) {
	// Empty query should still produce a hash (hashing empty NormalizeQuery result)
	h1 := QueryHash("", "hybrid", 10)
	h2 := QueryHash("", "hybrid", 10)
	if h1 != h2 || h1 == "" {
		t.Error("empty query should produce a consistent hash")
	}
}

func TestQueryHash_MultipleExtra(t *testing.T) {
	h1 := QueryHash("q", "hybrid", 10, "a", "b", "c")
	h2 := QueryHash("q", "hybrid", 10, "a", "b", "c")
	if h1 != h2 {
		t.Error("multiple extra params should be consistent")
	}
	h3 := QueryHash("q", "hybrid", 10, "a", "b", "d")
	if h1 == h3 {
		t.Error("different extra params should produce different hashes")
	}
}

// ─── Key builders ────────────────────────────────────────────────────────────

func TestQueryKey(t *testing.T) {
	k := QueryKey("mykb", "abc123")
	if k == "" {
		t.Error("QueryKey should not be empty")
	}
	if !strings.Contains(k, "query:") {
		t.Errorf("QueryKey should contain 'query:': %q", k)
	}
	if !strings.Contains(k, "mykb") {
		t.Errorf("QueryKey should contain kb name: %q", k)
	}
	if !strings.Contains(k, "abc123") {
		t.Errorf("QueryKey should contain hash: %q", k)
	}
	if !strings.Contains(k, keyVersion) {
		t.Errorf("QueryKey should contain key version %s: %q", keyVersion, k)
	}
}

func TestChunkKey(t *testing.T) {
	k := ChunkKey("mykb", "slug-001", "chunk-005")
	if k == "" {
		t.Error("ChunkKey should not be empty")
	}
	if !strings.Contains(k, "chunk:") {
		t.Errorf("ChunkKey should contain 'chunk:': %q", k)
	}
	if !strings.Contains(k, "mykb") && !strings.Contains(k, "slug-001") && !strings.Contains(k, "chunk-005") {
		t.Error("ChunkKey should contain all components")
	}
}

func TestMetaKey(t *testing.T) {
	k := MetaKey("mykb", "slug-001")
	if !strings.Contains(k, "meta:") {
		t.Errorf("MetaKey should contain 'meta:': %q", k)
	}
}

func TestIndexKey(t *testing.T) {
	k := IndexKey("mykb", "slug-001")
	if !strings.Contains(k, "index:") {
		t.Errorf("IndexKey should contain 'index:': %q", k)
	}
}

func TestKBListKey(t *testing.T) {
	k := KBListKey()
	if !strings.Contains(k, "kblist:") {
		t.Errorf("KBListKey should contain 'kblist:': %q", k)
	}
}

func TestDocInvalidatePatternFormat(t *testing.T) {
	p := DocInvalidatePattern("mykb", "slug-001")
	if !strings.Contains(p, "mykb") {
		t.Errorf("pattern should contain kb name: %q", p)
	}
	if !strings.Contains(p, "slug-001") {
		t.Errorf("pattern should contain doc slug: %q", p)
	}
	if !strings.Contains(p, "*") {
		t.Errorf("pattern should contain wildcard: %q", p)
	}
}

func TestDocQueryInvalidatePattern(t *testing.T) {
	p := DocQueryInvalidatePattern("mykb")
	if !strings.Contains(p, "query:") {
		t.Errorf("pattern should contain 'query:': %q", p)
	}
	if !strings.Contains(p, "mykb") {
		t.Errorf("pattern should contain kb name: %q", p)
	}
	if !strings.Contains(p, "*") {
		t.Errorf("pattern should contain wildcard: %q", p)
	}
}

// ─── Key interop with DeletePattern ──────────────────────────────────────────

func TestKeysWorkWithDeletePattern(t *testing.T) {
	c := NewMemCache(WithCapacity(1000))

	// Set a cached query entry using QueryKey
	qKey := QueryKey("kb1", "abc123")
	c.Set(nil, qKey, []byte("cached-result"), 0)

	// Set chunk keys
	c.Set(nil, ChunkKey("kb1", "doc1", "001"), []byte("chunk-data"), 0)
	c.Set(nil, ChunkKey("kb1", "doc1", "002"), []byte("chunk-data"), 0)

	// Set meta and index
	c.Set(nil, MetaKey("kb1", "doc1"), []byte("meta"), 0)
	c.Set(nil, IndexKey("kb1", "doc1"), []byte("index"), 0)

	// Set an unrelated key from another KB
	otherKey := QueryKey("kb2", "def456")
	c.Set(nil, otherKey, []byte("other"), 0)

	// Use a simple single-star pattern — MemCache.DeletePattern handles only one "*"
	// Delete all chunk keys for kb1/doc1
	n, err := c.DeletePattern(nil, "chunk:kb1:doc1:*")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("DeletePattern should delete 2 chunk keys, got %d", n)
	}

	// Delete meta
	n2, _ := c.DeletePattern(nil, MetaKey("kb1", "doc1"))
	if n2 != 1 {
		t.Errorf("exact delete of meta key should delete 1, got %d", n2)
	}

	// Delete index with pattern
	n3, _ := c.DeletePattern(nil, "index:kb1:doc1:*")
	if n3 != 1 {
		t.Errorf("delete index pattern should delete 1, got %d", n3)
	}

	// Query cache should survive
	if v, _ := c.Get(nil, qKey); string(v) != "cached-result" {
		t.Error("query cache should survive doc invalidation")
	}

	// Other KB's query should survive
	if v, _ := c.Get(nil, otherKey); string(v) != "other" {
		t.Error("other KB's query should survive")
	}

	// Delete query cache for kb1
	n4, _ := c.DeletePattern(nil, "query:kb1:*")
	if n4 != 1 {
		t.Errorf("query invalidation should delete 1 key, got %d", n4)
	}
}

func TestKeysAreVersioned(t *testing.T) {
	// All key builders must include keyVersion
	keys := []string{
		QueryKey("kb", "hash"),
		ChunkKey("kb", "slug", "001"),
		MetaKey("kb", "slug"),
		IndexKey("kb", "slug"),
		KBListKey(),
	}
	for i, k := range keys {
		if !strings.Contains(k, keyVersion) {
			t.Errorf("key[%d]=%q does not contain version %s", i, k, keyVersion)
		}
	}
}

// ─── TTL constants ───────────────────────────────────────────────────────────

func TestTTLConstants_NonNegative(t *testing.T) {
	if QueryTTL <= 0 {
		t.Error("QueryTTL should be positive")
	}
	// Zero TTL means "no expiry" which is fine for chunk/meta/index
	if ChunkTTL < 0 || MetaTTL < 0 || IndexTTL < 0 {
		t.Error("TTLs should not be negative")
	}
	if KBListTTL <= 0 {
		t.Error("KBListTTL should be positive")
	}
}
