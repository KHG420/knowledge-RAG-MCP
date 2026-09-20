package knowledge

import (
	"context"
	"testing"

	"knowledge-mcp/internal/cache"
	"knowledge-mcp/internal/logging"
)

// TestStoreInvalidateDocRealCache exercises Store.InvalidateDoc with a real
// MemCache (whose DeletePattern only supports a single "*" wildcard) and
// asserts that all document-scoped entries are removed while another document
// and another KB survive. It covers both a named KB and the empty default KB.
func TestStoreInvalidateDocRealCache(t *testing.T) {
	for _, kb := range []string{"kb", ""} {
		t.Run("kb="+kb, func(t *testing.T) {
			c := cache.NewMemCache()
			ctx := context.Background()
			s := &Store{cacheClient: c, kbName: kb, logger: logging.NewNopLogger()}

			stale := []string{
				cache.ChunkKey(kb, "doc", "000"),
				cache.MetaKey(kb, "doc"),
				cache.IndexKey(kb, "doc"),
				"meta:" + kb + ":doc",
				"index:" + kb + ":doc",
				cache.QueryKey(kb, "q"),
			}
			preserve := []string{
				cache.ChunkKey(kb, "other", "000"),
				cache.MetaKey(kb, "other"),
				"meta:" + kb + ":other",
				"index:" + kb + ":other",
				cache.QueryKey("otherkb", "q"),
			}
			for _, k := range append(append([]string{}, stale...), preserve...) {
				if err := c.Set(ctx, k, []byte("stale"), 0); err != nil {
					t.Fatalf("seed %s: %v", k, err)
				}
			}

			s.InvalidateDoc("doc")

			for _, k := range stale {
				if v, _ := c.Get(ctx, k); v != nil {
					t.Errorf("stale cache survived invalidation: %s", k)
				}
			}
			for _, k := range preserve {
				if v, _ := c.Get(ctx, k); v == nil {
					t.Errorf("unrelated cache entry was evicted: %s", k)
				}
			}
		})
	}
}
