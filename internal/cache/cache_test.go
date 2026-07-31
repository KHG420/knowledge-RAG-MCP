package cache

import (
	"testing"
)

func TestNormalizeQuery(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "word order",
			input: "synchronous rolling 同步横摇",
			want:  "rolling synchronous 同步横摇",
		},
		{
			name:  "reversed order same result",
			input: "同步横摇 rolling synchronous",
			want:  "rolling synchronous 同步横摇",
		},
		{
			name:  "case insensitive",
			input: "SYNCHRONOUS ROLLING 同步横摇",
			want:  "rolling synchronous 同步横摇",
		},
		{
			name:  "mixed case",
			input: "Synchronous Rolling 同步横摇",
			want:  "rolling synchronous 同步横摇",
		},
		{
			name:  "extra whitespace",
			input: "synchronous   rolling\t同步横摇",
			want:  "rolling synchronous 同步横摇",
		},
		{
			name:  "leading trailing whitespace",
			input: "  synchronous rolling 同步横摇  ",
			want:  "rolling synchronous 同步横摇",
		},
		{
			name:  "含中文",
			input: "船舶 横摇 共振 同步 参数",
			want:  "共振 参数 同步 横摇 船舶", // Go sort.Strings uses byte-level (Unicode code point) order for CJK
		},
		{
			name:  "single term",
			input: "同步横摇",
			want:  "同步横摇",
		},
		{
			name:  "single term with case",
			input: "SYNCHRONOUS",
			want:  "synchronous",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "only whitespace",
			input: "   ",
			want:  "",
		},
		{
			name:  "duplicate terms",
			input: "roll roll synchronous synchronous 同步 同步",
			want:  "roll synchronous 同步",
		},
		{
			name:  "数字术语",
			input: "omega 2 frequency natural",
			want:  "2 frequency natural omega",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeQuery(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeQuery(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestQueryHashSameForEquivalentQueries(t *testing.T) {
	// 所有变体应该产生相同的 hash
	variants := []string{
		"同步横摇 synchronous rolling 船舶",
		"synchronous rolling 船舶 同步横摇",
		"SYNCHRONOUS rolling 船舶 同步横摇",
		"synchronous  rolling   船舶   同步横摇",
	}

	hashes := make(map[string]string)
	for _, q := range variants {
		h := QueryHash(q, "hybrid", 8)
		hashes[h] = q
	}

	if len(hashes) != 1 {
		t.Errorf("expected all variants to produce the same hash, got %d distinct hashes:", len(hashes))
		for h, q := range hashes {
			t.Logf("  %s → %q", h, q)
		}
	}
}

func TestQueryHashDifferentForDifferentModes(t *testing.T) {
	q := "同步横摇 synchronous rolling"
	h1 := QueryHash(q, "bm25", 8)
	h2 := QueryHash(q, "hybrid", 8)
	if h1 == h2 {
		t.Errorf("expected different hashes for bm25 vs hybrid, got %s for both", h1)
	}
}

func TestQueryHashDifferentForDifferentLimits(t *testing.T) {
	q := "同步横摇 synchronous rolling"
	h1 := QueryHash(q, "hybrid", 8)
	h2 := QueryHash(q, "hybrid", 20)
	if h1 == h2 {
		t.Errorf("expected different hashes for limit 8 vs 20, got %s for both", h1)
	}
}

func TestQueryHashExtraParams(t *testing.T) {
	q := "同步横摇"
	h1 := QueryHash(q, "hybrid", 8)
	h2 := QueryHash(q, "hybrid", 8, "pdf")
	h3 := QueryHash(q, "hybrid", 8, "pdf", "abstract")
	if h1 == h2 {
		t.Errorf("expected different hashes with extra param, got %s", h1)
	}
	if h2 == h3 {
		t.Errorf("expected different hashes with different extra params")
	}
}

func TestQueryHashLength(t *testing.T) {
	h := QueryHash("test query", "hybrid", 8)
	if len(h) != 16 {
		t.Errorf("expected hash length 16, got %d", len(h))
	}
}

func TestKeyFormats(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{"QueryKey", QueryKey("横摇论文", "abcdef1234567890"), "query:横摇论文:abcdef1234567890:v2"},
		{"ChunkKey", ChunkKey("横摇论文", "doc-123", "005"), "chunk:横摇论文:doc-123:005:v2"},
		{"MetaKey", MetaKey("横摇论文", "doc-123"), "meta:横摇论文:doc-123:v2"},
		{"IndexKey", IndexKey("横摇论文", "doc-123"), "index:横摇论文:doc-123:v2"},
		{"KBListKey", KBListKey(), "kblist:v2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.key != tt.want {
				t.Errorf("%s = %q, want %q", tt.name, tt.key, tt.want)
			}
		})
	}
}

func TestDocInvalidatePattern(t *testing.T) {
	got := DocInvalidatePattern("横摇论文", "doc-123")
	want := "*:横摇论文:doc-123:*"
	if got != want {
		t.Errorf("DocInvalidatePattern = %q, want %q", got, want)
	}
}

func TestNoopCache(t *testing.T) {
	var c Cache = NoopCache{}

	// 所有操作应该无错误返回
	v, err := c.Get(nil, "any-key")
	if err != nil {
		t.Errorf("NoopCache.Get returned error: %v", err)
	}
	if v != nil {
		t.Errorf("NoopCache.Get returned non-nil value: %v", v)
	}

	if err := c.Set(nil, "k", []byte("v"), 0); err != nil {
		t.Errorf("NoopCache.Set returned error: %v", err)
	}
	if err := c.Delete(nil, "k"); err != nil {
		t.Errorf("NoopCache.Delete returned error: %v", err)
	}
	n, err := c.DeletePattern(nil, "*")
	if err != nil {
		t.Errorf("NoopCache.DeletePattern returned error: %v", err)
	}
	if n != 0 {
		t.Errorf("NoopCache.DeletePattern returned %d, want 0", n)
	}
	if err := c.Ping(nil); err != nil {
		t.Errorf("NoopCache.Ping returned error: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("NoopCache.Close returned error: %v", err)
	}
}

func TestIsNoop(t *testing.T) {
	if !IsNoop(nil) {
		t.Error("IsNoop(nil) should be true")
	}
	if !IsNoop(NoopCache{}) {
		t.Error("IsNoop(NoopCache{}) should be true")
	}
	if IsNoop(&RedisCache{}) {
		t.Error("IsNoop(&RedisCache{}) should be false (client nil but not NoopCache)")
	}
}

func TestTTLConstants(t *testing.T) {
	// 确保 TTL 常量是合理的值
	if QueryTTL <= 0 {
		t.Error("QueryTTL should be positive")
	}
	if ChunkTTL != 0 {
		t.Error("ChunkTTL should be 0 (no expiry)")
	}
	if MetaTTL != 0 {
		t.Error("MetaTTL should be 0")
	}
	if IndexTTL != 0 {
		t.Error("IndexTTL should be 0")
	}
	if KBListTTL <= 0 {
		t.Error("KBListTTL should be positive")
	}
}
