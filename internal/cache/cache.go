// Package cache provides a pluggable caching layer for the knowledge store.
// It supports Redis as the primary backend with a no-op fallback when
// Redis is not configured.
package cache

// Package cache provides an exact-match query result cache backed by Redis.
//
// Cache strategy (v4):
//   - Exact cache: NormalizeQuery (lowercase → sort → dedup) + sha256 prefix hash.
//     TTL = 5 min. Already implemented and active when redis_enabled=true.
//   - Semantic cache (deferred to Phase 2): embedding-based similarity match for
//     queries that differ in wording but share the same intent. Requires maintaining
//     an embedding index and threshold tuning — ~5x implementation complexity of
//     exact cache. Not required for Week 3 delivery.

import (
	"context"
	"time"
)

// Cache is the abstract caching layer. All methods are safe for concurrent use.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) error
	DeletePattern(ctx context.Context, pattern string) (int64, error)
	Ping(ctx context.Context) error
	Close() error
}

// NoopCache is a Cache that stores nothing and always misses.
type NoopCache struct{}

func (NoopCache) Get(_ context.Context, _ string) ([]byte, error)                  { return nil, nil }
func (NoopCache) Set(_ context.Context, _ string, _ []byte, _ time.Duration) error { return nil }
func (NoopCache) Delete(_ context.Context, _ ...string) error                      { return nil }
func (NoopCache) DeletePattern(_ context.Context, _ string) (int64, error)         { return 0, nil }
func (NoopCache) Ping(_ context.Context) error                                     { return nil }
func (NoopCache) Close() error                                                     { return nil }

// IsNoop reports whether c is nil or a NoopCache.
func IsNoop(c Cache) bool {
	if c == nil {
		return true
	}
	_, ok := c.(NoopCache)
	return ok
}
