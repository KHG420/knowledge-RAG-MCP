package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisCache implements Cache backed by a Redis server.
type RedisCache struct {
	client *redis.Client
	prefix string // key namespace prefix, e.g. "kmcp:"
}

// RedisConfig holds the connection parameters for Redis.
type RedisConfig struct {
	// Addr is the host:port of the Redis server (default "127.0.0.1:6379").
	Addr string

	// Password for authenticating with Redis. Empty means no auth.
	Password string

	// DB selects the Redis database number (default 0).
	DB int

	// Prefix is prepended to every cache key to namespace entries.
	// Default "kmcp:".
	Prefix string

	// DialTimeout is the timeout for establishing new connections.
	DialTimeout time.Duration

	// ReadTimeout is the timeout for socket reads.
	ReadTimeout time.Duration

	// WriteTimeout is the timeout for socket writes.
	WriteTimeout time.Duration

	// PoolSize is the maximum number of socket connections.
	PoolSize int
}

// DefaultRedisConfig returns sensible defaults.
func DefaultRedisConfig() RedisConfig {
	return RedisConfig{
		Addr:         "127.0.0.1:6379",
		Password:     "",
		DB:           0,
		Prefix:       "kmcp:",
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolSize:     10,
	}
}

// NewRedisCache creates a Redis-backed cache and pings the server to
// verify connectivity. Returns an error when the server is unreachable.
func NewRedisCache(cfg RedisConfig) (*RedisCache, error) {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:6379"
	}
	if cfg.Prefix == "" {
		cfg.Prefix = "kmcp:"
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = 3 * time.Second
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 3 * time.Second
	}

	client := redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		PoolSize:     cfg.PoolSize,
	})

	// Verify connectivity on creation.
	ctx, cancel := context.WithTimeout(context.Background(), cfg.DialTimeout)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("redis ping failed: %w", err)
	}

	return &RedisCache{client: client, prefix: cfg.Prefix}, nil
}

// NewRedisCacheOrNil is like NewRedisCache but returns nil (no cache) when
// the Redis server is unreachable, so the application can start without it.
func NewRedisCacheOrNil(cfg RedisConfig) *RedisCache {
	c, err := NewRedisCache(cfg)
	if err != nil {
		return nil
	}
	return c
}

// key returns the namespaced key.
func (rc *RedisCache) key(k string) string { return rc.prefix + k }

// Get retrieves a value. Returns nil, nil on cache miss.
func (rc *RedisCache) Get(ctx context.Context, key string) ([]byte, error) {
	val, err := rc.client.Get(ctx, rc.key(key)).Bytes()
	if err == redis.Nil {
		return nil, nil // cache miss
	}
	if err != nil {
		return nil, fmt.Errorf("redis get %q: %w", key, err)
	}
	return val, nil
}

// Set stores a value with TTL. Zero TTL means no expiration.
func (rc *RedisCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := rc.client.Set(ctx, rc.key(key), value, ttl).Err(); err != nil {
		return fmt.Errorf("redis set %q: %w", key, err)
	}
	return nil
}

// Delete removes keys. Missing keys are not an error.
func (rc *RedisCache) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	prefixed := make([]string, len(keys))
	for i, k := range keys {
		prefixed[i] = rc.key(k)
	}
	if err := rc.client.Del(ctx, prefixed...).Err(); err != nil {
		return fmt.Errorf("redis del: %w", err)
	}
	return nil
}

// DeletePattern removes keys matching a glob pattern (Redis KEYS command).
// Returns the count of deleted keys.
func (rc *RedisCache) DeletePattern(ctx context.Context, pattern string) (int64, error) {
	fullPattern := rc.key(pattern)
	keys, err := rc.client.Keys(ctx, fullPattern).Result()
	if err != nil {
		return 0, fmt.Errorf("redis keys %q: %w", pattern, err)
	}
	if len(keys) == 0 {
		return 0, nil
	}
	deleted, err := rc.client.Del(ctx, keys...).Result()
	if err != nil {
		return 0, fmt.Errorf("redis del pattern: %w", err)
	}
	return deleted, nil
}

// Ping checks connectivity.
func (rc *RedisCache) Ping(ctx context.Context) error {
	return rc.client.Ping(ctx).Err()
}

// Close releases the connection pool.
func (rc *RedisCache) Close() error {
	return rc.client.Close()
}

// Client returns the underlying go-redis client for direct operations.
func (rc *RedisCache) Client() *redis.Client { return rc.client }
