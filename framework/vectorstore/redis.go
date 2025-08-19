package vectorstore

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/redis/go-redis/v9"
)

type RedisConfig struct {
	// Connection settings
	Addr     string `json:"addr"`               // Redis server address (host:port) - REQUIRED
	Username string `json:"username,omitempty"` // Username for Redis AUTH (optional)
	Password string `json:"password,omitempty"` // Password for Redis AUTH (optional)
	DB       int    `json:"db,omitempty"`       // Redis database number (default: 0)

	// Connection pool and timeout settings (passed directly to Redis client)
	PoolSize        int           `json:"pool_size,omitempty"`          // Maximum number of socket connections (optional)
	MinIdleConns    int           `json:"min_idle_conns,omitempty"`     // Minimum number of idle connections (optional)
	MaxIdleConns    int           `json:"max_idle_conns,omitempty"`     // Maximum number of idle connections (optional)
	ConnMaxLifetime time.Duration `json:"conn_max_lifetime,omitempty"`  // Connection maximum lifetime (optional)
	ConnMaxIdleTime time.Duration `json:"conn_max_idle_time,omitempty"` // Connection maximum idle time (optional)
	DialTimeout     time.Duration `json:"dial_timeout,omitempty"`       // Timeout for socket connection (optional)
	ReadTimeout     time.Duration `json:"read_timeout,omitempty"`       // Timeout for socket reads (optional)
	WriteTimeout    time.Duration `json:"write_timeout,omitempty"`      // Timeout for socket writes (optional)
	ContextTimeout  time.Duration `json:"context_timeout,omitempty"`    // Timeout for Redis operations (optional)
}

// RedisStore represents the Redis vector store.
type RedisStore struct {
	client *redis.Client
	config RedisConfig
	logger schemas.Logger
}

// withTimeout adds a timeout to the context if it is set.
func (s *RedisStore) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.config.ContextTimeout > 0 {
		return context.WithTimeout(ctx, s.config.ContextTimeout)
	}
	// No-op cancel to simplify call sites.
	return ctx, func() {}
}

func (s *RedisStore) GetChunk(ctx context.Context, contextKey string) (string, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	val, err := s.client.Get(ctx, contextKey).Result()
	if err == redis.Nil {
		return "", ErrNotFound
	}
	return val, err
}

// GetChunks retrieves a value from Redis.
func (s *RedisStore) GetChunks(ctx context.Context, chunkKeys []string) ([]any, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	vals, err := s.client.MGet(ctx, chunkKeys...).Result()
	if err != nil {
		return nil, err
	}
	return vals, nil
}

// Add adds a value to Redis.
func (s *RedisStore) Add(ctx context.Context, key string, value string, ttl time.Duration) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	return s.client.Set(ctx, key, value, ttl).Err()
}

// Delete deletes a value from Redis.
func (s *RedisStore) Delete(ctx context.Context, keys []string) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	return s.client.Del(ctx, keys...).Err()
}

// GetAll retrieves all keys matching a pattern from Redis.
func (s *RedisStore) GetAll(ctx context.Context, pattern string, cursor *string, count int64) ([]string, *string, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	var err error
	var redisCursor uint64
	if cursor != nil {
		redisCursor, err = strconv.ParseUint(*cursor, 10, 64)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("invalid cursor value: %w", err)
	}
	keys, c, err := s.client.Scan(ctx, redisCursor, pattern, count).Result()
	var nextCursor *string
	if c == 0 {
		nextCursor = nil
	} else {
		nxCursor := strconv.FormatUint(c, 10)
		nextCursor = &nxCursor
	}
	return keys, nextCursor, err
}

// Close closes the Redis connection.
func (s *RedisStore) Close(_ context.Context) error {
	return s.client.Close()
}

// newRedisStore creates a new Redis vector store.
func newRedisStore(ctx context.Context, config RedisConfig, logger schemas.Logger) (*RedisStore, error) {
	client := redis.NewClient(&redis.Options{
		Addr:            config.Addr,
		Username:        config.Username,
		Password:        config.Password,
		DB:              config.DB,
		MaxActiveConns:  config.PoolSize,
		MinIdleConns:    config.MinIdleConns,
		MaxIdleConns:    config.MaxIdleConns,
		ConnMaxLifetime: config.ConnMaxLifetime,
		ConnMaxIdleTime: config.ConnMaxIdleTime,
		DialTimeout:     config.DialTimeout,
		ReadTimeout:     config.ReadTimeout,
		WriteTimeout:    config.WriteTimeout,
	})

	// Test the connection
	pingCtx := ctx
	if config.ContextTimeout > 0 {
		var cancel context.CancelFunc
		pingCtx, cancel = context.WithTimeout(ctx, config.ContextTimeout)
		defer cancel()
	}
	if err := client.Ping(pingCtx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	return &RedisStore{
		client: client,
		config: config,
		logger: logger,
	}, nil
}
