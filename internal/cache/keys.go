package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Key naming conventions.
//
// All keys are namespaced by the Redis prefix (default "kmcp:").
// KB-scoped keys include the knowledge base name to isolate caches.
//
// Pattern                     Description
// ───────                     ───────────
// query:{kb}:{hash}           Search result cache (by query hash + KB)
// chunk:{kb}:{slug}:{id}      Chunk text cache
// meta:{kb}:{slug}            Document metadata cache
// index:{kb}:{slug}           ChunksIndex (CHUNKS.toml) cache
// kblist                      Knowledge base list cache
//
// Every key also has a version suffix (e.g. ":v1") so we can bump the
// format without flushing existing entries.

const keyVersion = "v2" // bumped from v1: QueryHash now normalizes query before hashing

// QueryKey builds the cache key for a search query.
func QueryKey(kbName, queryHash string) string {
	return fmt.Sprintf("query:%s:%s:%s", kbName, queryHash, keyVersion)
}

// NormalizeQuery canonicalises a search query so that semantically equivalent
// inputs (differing only in word order, case, or whitespace) map to the same
// cache key. Steps: lowercase → split by whitespace → sort alphabetically →
// deduplicate → rejoin.
func NormalizeQuery(query string) string {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return ""
	}
	if len(terms) == 1 {
		return terms[0]
	}
	sort.Strings(terms)
	// Deduplicate.
	n := 0
	for i := range terms {
		if i == 0 || terms[i] != terms[n-1] {
			terms[n] = terms[i]
			n++
		}
	}
	return strings.Join(terms[:n], " ")
}

// QueryHash computes a stable hash of the search parameters for cache lookup.
// The query is normalised via NormalizeQuery so that reordered or differently
// cased queries share the same cache entry.
func QueryHash(query, mode string, limit int, extra ...string) string {
	h := sha256.New()
	h.Write([]byte(NormalizeQuery(query)))
	h.Write([]byte(mode))
	h.Write([]byte(fmt.Sprintf("%d", limit)))
	for _, e := range extra {
		h.Write([]byte(e))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// QueryTTL is the default lifetime for cached search results.
const QueryTTL = 5 * time.Minute

// ChunkKey builds the cache key for a chunk's text content.
func ChunkKey(kbName, docSlug, chunkID string) string {
	return fmt.Sprintf("chunk:%s:%s:%s:%s", kbName, docSlug, chunkID, keyVersion)
}

// ChunkTTL is the default lifetime for cached chunk text.
// Set to 0 (no expiry) since chunk text is immutable once uploaded;
// invalidation happens explicitly on document removal/re-upload.
const ChunkTTL = 0

// MetaKey builds the cache key for document metadata.
func MetaKey(kbName, docSlug string) string {
	return fmt.Sprintf("meta:%s:%s:%s", kbName, docSlug, keyVersion)
}

// MetaTTL is the default lifetime for cached metadata.
const MetaTTL = 0

// IndexKey builds the cache key for a ChunksIndex.
func IndexKey(kbName, docSlug string) string {
	return fmt.Sprintf("index:%s:%s:%s", kbName, docSlug, keyVersion)
}

// IndexTTL is the default lifetime for a cached ChunksIndex.
const IndexTTL = 0

// KBListKey returns the cache key for the knowledge base list.
func KBListKey() string {
	return fmt.Sprintf("kblist:%s", keyVersion)
}

// KBListTTL is the default lifetime for the cached KB list.
const KBListTTL = 1 * time.Minute

// DocInvalidatePattern returns a pattern that matches all cache entries
// for a given document (chunks, meta, index).
func DocInvalidatePattern(kbName, docSlug string) string {
	// Matches chunk:*, meta:*, index:* for this KB+docSlug.
	return fmt.Sprintf("*:%s:%s:*", kbName, docSlug)
}

// DocQueryInvalidatePattern returns a pattern that matches all query-cache
// entries for a given KB.  When any document in the KB is added, removed,
// or re-indexed, stale query results must be evicted to prevent the search
// cache from serving results that reference deleted or rewritten chunks.
func DocQueryInvalidatePattern(kbName string) string {
	return fmt.Sprintf("query:%s:*", kbName)
}
