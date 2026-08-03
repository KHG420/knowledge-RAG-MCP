package cache

import (
	"container/list"
	"context"
	"strings"
	"sync"
	"time"
)

// MemCache is an in-memory LRU cache implementing the Cache interface.
// It provides basic caching without external dependencies (Redis), suitable
// for single-node deployments. Capacity and default TTL are configurable.
//
// Eviction: least-recently-used entries are evicted when capacity is reached.
// Expiry: entries past their per-key TTL are silently skipped on Get and
// cleaned up lazily.
type MemCache struct {
	mu       sync.Mutex
	cap      int
	list     *list.List
	items    map[string]*list.Element
	defTTL   time.Duration
}

type memEntry struct {
	key    string
	value  []byte
	expiry time.Time
}

// MemCacheOption configures a MemCache.
type MemCacheOption func(*MemCache)

// WithCapacity sets the maximum number of entries. Default 10000.
func WithCapacity(n int) MemCacheOption {
	return func(m *MemCache) { m.cap = n }
}

// WithDefaultTTL sets the default TTL for Set calls that use a zero duration.
// Default 0 (no expiry).
func WithDefaultTTL(d time.Duration) MemCacheOption {
	return func(m *MemCache) { m.defTTL = d }
}

// NewMemCache creates an in-memory LRU cache. By default it holds up to
// 10 000 entries with no per-entry TTL.
func NewMemCache(opts ...MemCacheOption) *MemCache {
	m := &MemCache{
		cap:   10000,
		list:  list.New(),
		items: make(map[string]*list.Element),
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Get returns the value for key, or nil when the key is missing or expired.
func (m *MemCache) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	el, ok := m.items[key]
	if !ok {
		return nil, nil
	}
	e := el.Value.(*memEntry)
	if !e.expiry.IsZero() && time.Now().After(e.expiry) {
		m.list.Remove(el)
		delete(m.items, key)
		return nil, nil
	}
	// Move to front (most-recently-used).
	m.list.MoveToFront(el)
	// Return a copy so the caller can't mutate our stored slice.
	v := make([]byte, len(e.value))
	copy(v, e.value)
	return v, nil
}

// Set stores a value under key. When ttl is zero, defTTL is used.
func (m *MemCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl == 0 {
		ttl = m.defTTL
	}
	// Copy incoming slice.
	v := make([]byte, len(value))
	copy(v, value)

	m.mu.Lock()
	defer m.mu.Unlock()

	expiry := time.Time{}
	if ttl > 0 {
		expiry = time.Now().Add(ttl)
	}

	if el, ok := m.items[key]; ok {
		e := el.Value.(*memEntry)
		e.value = v
		e.expiry = expiry
		m.list.MoveToFront(el)
		return nil
	}

	// Evict if at capacity (LRU = from back).
	for m.list.Len() >= m.cap {
		back := m.list.Back()
		if back == nil {
			break
		}
		delete(m.items, back.Value.(*memEntry).key)
		m.list.Remove(back)
	}

	e := &memEntry{key: key, value: v, expiry: expiry}
	el := m.list.PushFront(e)
	m.items[key] = el
	return nil
}

// Delete removes one or more keys. Missing keys are silently ignored.
func (m *MemCache) Delete(_ context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, key := range keys {
		if el, ok := m.items[key]; ok {
			m.list.Remove(el)
			delete(m.items, key)
		}
	}
	return nil
}

// DeletePattern removes all keys matching the glob-like pattern.
// Only "*" wildcard (match any sequence) is supported. For example,
// "query:ship:*" removes every key whose prefix is "query:ship:".
func (m *MemCache) DeletePattern(_ context.Context, pattern string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	prefix, suffix, hasStar := strings.Cut(pattern, "*")
	if !hasStar {
		// Exact match.
		if el, ok := m.items[pattern]; ok {
			m.list.Remove(el)
			delete(m.items, pattern)
			return 1, nil
		}
		return 0, nil
	}

	var count int64
	for key, el := range m.items {
		if strings.HasPrefix(key, prefix) && strings.HasSuffix(key, suffix) {
			m.list.Remove(el)
			delete(m.items, key)
			count++
		}
	}
	return count, nil
}

// Ping always returns nil — memory is always "reachable".
func (m *MemCache) Ping(_ context.Context) error { return nil }

// Close clears all entries.
func (m *MemCache) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.list.Init()
	m.items = make(map[string]*list.Element)
	return nil
}

// Len returns the current number of entries.
func (m *MemCache) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.list.Len()
}
