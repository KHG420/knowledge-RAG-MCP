package cache

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestMemCacheGetSet(t *testing.T) {
	c := NewMemCache(WithCapacity(100))

	// Set and get.
	if err := c.Set(nil, "k1", []byte("v1"), 0); err != nil {
		t.Fatal(err)
	}
	v, err := c.Get(nil, "k1")
	if err != nil {
		t.Fatal(err)
	}
	if string(v) != "v1" {
		t.Errorf("got %q, want %q", v, "v1")
	}
}

func TestMemCacheGetMissing(t *testing.T) {
	c := NewMemCache()
	v, err := c.Get(nil, "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if v != nil {
		t.Errorf("expected nil for missing key, got %q", v)
	}
}

func TestMemCacheGetExpired(t *testing.T) {
	c := NewMemCache()
	if err := c.Set(nil, "k", []byte("v"), 1*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	v, err := c.Get(nil, "k")
	if err != nil {
		t.Fatal(err)
	}
	if v != nil {
		t.Errorf("expected nil after TTL expiry, got %q", v)
	}
}

func TestMemCacheOverride(t *testing.T) {
	c := NewMemCache()
	if err := c.Set(nil, "k", []byte("v1"), 0); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(nil, "k", []byte("v2"), 0); err != nil {
		t.Fatal(err)
	}
	v, _ := c.Get(nil, "k")
	if string(v) != "v2" {
		t.Errorf("got %q, want %q", v, "v2")
	}
}

func TestMemCacheDelete(t *testing.T) {
	c := NewMemCache()
	c.Set(nil, "k1", []byte("v1"), 0)
	c.Set(nil, "k2", []byte("v2"), 0)
	c.Delete(nil, "k1")
	if v, _ := c.Get(nil, "k1"); v != nil {
		t.Error("k1 should be deleted")
	}
	if v, _ := c.Get(nil, "k2"); string(v) != "v2" {
		t.Error("k2 should still exist")
	}
}

func TestMemCacheDeletePattern(t *testing.T) {
	c := NewMemCache()
	c.Set(nil, "a:1", []byte("x"), 0)
	c.Set(nil, "a:2", []byte("y"), 0)
	c.Set(nil, "b:1", []byte("z"), 0)

	n, err := c.DeletePattern(nil, "a:*")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("DeletePattern deleted %d keys, want 2", n)
	}
	if v, _ := c.Get(nil, "a:1"); v != nil {
		t.Error("a:1 should be deleted")
	}
	if v, _ := c.Get(nil, "a:2"); v != nil {
		t.Error("a:2 should be deleted")
	}
	if v, _ := c.Get(nil, "b:1"); string(v) != "z" {
		t.Error("b:1 should still exist")
	}
}

func TestMemCacheDeletePatternExact(t *testing.T) {
	c := NewMemCache()
	c.Set(nil, "exact", []byte("x"), 0)
	n, _ := c.DeletePattern(nil, "exact") // no wildcard — exact match
	if n != 1 {
		t.Errorf("exact match deleted %d, want 1", n)
	}
}

func TestMemCacheLRUEviction(t *testing.T) {
	c := NewMemCache(WithCapacity(3))
	c.Set(nil, "a", []byte("1"), 0)
	c.Set(nil, "b", []byte("2"), 0)
	c.Set(nil, "c", []byte("3"), 0)
	// Access "a" so it becomes most-recently-used.
	c.Get(nil, "a")
	// Insert "d" — should evict "b" (LRU).
	c.Set(nil, "d", []byte("4"), 0)

	if v, _ := c.Get(nil, "a"); string(v) != "1" {
		t.Error("a should survive (was recently accessed)")
	}
	if v, _ := c.Get(nil, "b"); v != nil {
		t.Error("b should be evicted")
	}
	if v, _ := c.Get(nil, "c"); string(v) != "3" {
		t.Error("c should survive")
	}
	if v, _ := c.Get(nil, "d"); string(v) != "4" {
		t.Error("d should be present")
	}
}

func TestMemCacheLen(t *testing.T) {
	c := NewMemCache()
	if c.Len() != 0 {
		t.Errorf("initial Len = %d, want 0", c.Len())
	}
	c.Set(nil, "a", []byte("1"), 0)
	c.Set(nil, "b", []byte("2"), 0)
	if c.Len() != 2 {
		t.Errorf("Len = %d, want 2", c.Len())
	}
	c.Delete(nil, "a")
	if c.Len() != 1 {
		t.Errorf("Len = %d, want 1", c.Len())
	}
}

func TestMemCachePing(t *testing.T) {
	if err := NewMemCache().Ping(nil); err != nil {
		t.Errorf("MemCache.Ping should always succeed: %v", err)
	}
}

func TestMemCacheClose(t *testing.T) {
	c := NewMemCache()
	c.Set(nil, "a", []byte("1"), 0)
	c.Close()
	if c.Len() != 0 {
		t.Errorf("Len after Close = %d, want 0", c.Len())
	}
}

func TestMemCacheValueIsolation(t *testing.T) {
	c := NewMemCache()
	orig := []byte("hello")
	c.Set(nil, "k", orig, 0)
	// Mutate original slice — cache must return original value.
	orig[0] = 'H'
	v, _ := c.Get(nil, "k")
	if string(v) != "hello" {
		t.Errorf("value was mutated externally: got %q", v)
	}
	// Mutate retrieved slice — cache must still return original value.
	v[0] = 'X'
	v2, _ := c.Get(nil, "k")
	if string(v2) != "hello" {
		t.Errorf("value was mutated via retrieved slice: got %q", v2)
	}
}

func TestMemCacheDefaultTTL(t *testing.T) {
	c := NewMemCache(WithDefaultTTL(10 * time.Millisecond))
	c.Set(nil, "k", []byte("v"), 0) // zero TTL → uses default
	time.Sleep(20 * time.Millisecond)
	if v, _ := c.Get(nil, "k"); v != nil {
		t.Error("key should expire after default TTL")
	}
}

func TestMemCacheZeroCapacity(t *testing.T) {
	// Zero-cap: every Set evicts the oldest. Only 1 entry should remain.
	c := NewMemCache(WithCapacity(1))
	c.Set(nil, "a", []byte("1"), 0)
	c.Set(nil, "b", []byte("2"), 0)
	if v, _ := c.Get(nil, "a"); v != nil {
		t.Error("a should be evicted")
	}
	if v, _ := c.Get(nil, "b"); string(v) != "2" {
		t.Error("b should survive")
	}
}

func TestMemCacheConcurrent(t *testing.T) {
	c := NewMemCache(WithCapacity(500))
	var wg sync.WaitGroup
	const goroutines = 10
	const opsPerG = 500

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := 0; i < opsPerG; i++ {
				key := fmt.Sprintf("k%d-%d", base, i)
				c.Set(nil, key, []byte("v"), 0)
				v, _ := c.Get(nil, key)
				if v != nil && string(v) != "v" {
					t.Errorf("wrong value for %q: %q", key, v)
				}
				if i%10 == 0 {
					c.Delete(nil, key)
				}
			}
		}(g)
	}
	wg.Wait()

	// Should still be functional after concurrent stress.
	c.Set(nil, "final", []byte("ok"), 0)
	if v, _ := c.Get(nil, "final"); string(v) != "ok" {
		t.Error("cache broken after concurrent stress")
	}
}

func TestMemCacheDeletePatternConcurrent(t *testing.T) {
	c := NewMemCache(WithCapacity(2000))
	// Pre-populate.
	for i := 0; i < 500; i++ {
		c.Set(nil, fmt.Sprintf("ns:a:%d", i), []byte("x"), 0)
		c.Set(nil, fmt.Sprintf("ns:b:%d", i), []byte("y"), 0)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			c.Set(nil, fmt.Sprintf("ns:a:%d", 600+i), []byte("z"), 0)
		}
	}()
	go func() {
		defer wg.Done()
		c.DeletePattern(nil, "ns:b:*")
	}()
	wg.Wait()

	// All "ns:b:*" keys should be gone.
	for i := 0; i < 500; i++ {
		if v, _ := c.Get(nil, fmt.Sprintf("ns:b:%d", i)); v != nil {
			t.Errorf("ns:b:%d should be deleted", i)
		}
	}
	// "ns:a:*" keys should still be there.
	if v, _ := c.Get(nil, "ns:a:0"); string(v) != "x" {
		t.Error("ns:a:0 should survive")
	}
}
