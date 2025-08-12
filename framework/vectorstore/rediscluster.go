package vectorstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/redis/go-redis/v9"
)

type RedisClusterConfig struct {
	// Connection settings
	Addrs    []string `json:"addrs"`              // Redis cluster node addresses (host:port) - REQUIRED
	Username string   `json:"username,omitempty"` // Username for Redis AUTH (optional)
	Password string   `json:"password,omitempty"` // Password for Redis AUTH (optional)

	// Cluster specific settings
	MaxRedirects   int  `json:"max_redirects,omitempty"`    // Maximum number of retries for cluster redirects (optional)
	ReadOnly       bool `json:"read_only,omitempty"`        // Enable read-only mode (optional)
	RouteByLatency bool `json:"route_by_latency,omitempty"` // Route read-only commands by latency (optional)
	RouteRandomly  bool `json:"route_randomly,omitempty"`   // Route read-only commands randomly (optional)

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

// ClusterCursor represents the cursor for a Redis Cluster scan operation.
type ClusterCursor struct {
	NodeCursors map[string]uint64 `json:"node_cursors"`
}

// RedisClusterStore represents the Redis Cluster vector store.
type RedisClusterStore struct {
	client *redis.ClusterClient
	config RedisClusterConfig
	logger schemas.Logger
}

// withTimeout adds a timeout to the context if it is set.
func (s *RedisClusterStore) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	fmt.Printf("%v", s)
	if s.config.ContextTimeout > 0 {
		return context.WithTimeout(ctx, s.config.ContextTimeout)
	}
	// No-op cancel to simplify call sites.
	return ctx, func() {}
}

// GetChunk retrieves a value from Redis Cluster.
func (s *RedisClusterStore) GetChunk(ctx context.Context, contextKey string) (string, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	return s.client.Get(ctx, contextKey).Result()
}

// GetChunks retrieves values from Redis Cluster.
func (s *RedisClusterStore) GetChunks(ctx context.Context, chunkKeys []string) ([]any, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	return s.client.MGet(ctx, chunkKeys...).Result()
}

// Add adds a value to Redis Cluster.
func (s *RedisClusterStore) Add(ctx context.Context, key string, value string, ttl time.Duration) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	return s.client.Set(ctx, key, value, ttl).Err()
}

// Delete deletes values from Redis Cluster.
func (s *RedisClusterStore) Delete(ctx context.Context, keys []string) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	return s.client.Del(ctx, keys...).Err()
}

// GetAll retrieves all keys matching a pattern from Redis Cluster.
// Note: In Redis Cluster, SCAN operations need to be performed on each node
func (s *RedisClusterStore) GetAll(ctx context.Context, pattern string, cursor *string, count int64) ([]string, *string, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	var err error
	var clusterCursor ClusterCursor
	if cursor != nil {
		// Decode the composite cursor
		if err := json.Unmarshal([]byte(*cursor), &clusterCursor); err != nil {
			clusterCursor = ClusterCursor{NodeCursors: make(map[string]uint64)}
		}
	} else {
		clusterCursor = ClusterCursor{NodeCursors: make(map[string]uint64)}
	}
	// For Redis Cluster, we need to scan all master nodes
	// This is a simplified implementation - in production, you might want to
	// implement more sophisticated cursor handling across multiple nodes
	var allKeys []string

	// Get all master nodes and scan each one
	err = s.client.ForEachMaster(ctx, func(ctx context.Context, client *redis.Client) error {
		nodeAddr := client.Options().Addr
		nodeCursor := clusterCursor.NodeCursors[nodeAddr]
		keys, c, scanErr := client.Scan(ctx, nodeCursor, pattern, count).Result()
		if scanErr != nil {
			return scanErr
		}
		allKeys = append(allKeys, keys...)
		clusterCursor.NodeCursors[nodeAddr] = c
		return nil
	})

	if err != nil {
		return nil, nil, err
	}

	var nextCursor *string
	allDone := true
	for _, c := range clusterCursor.NodeCursors {
		if c != 0 {
			allDone = false
			break
		}
	}
	if allDone {
		nextCursor = nil
	} else {
		cursorBytes, _ := json.Marshal(clusterCursor)
		nextCursor = bifrost.Ptr(string(cursorBytes))
	}
	return allKeys, nextCursor, nil
}

// Close closes the Redis Cluster connection.
func (s *RedisClusterStore) Close(ctx context.Context) error {
	_, cancel := s.withTimeout(ctx)
	defer cancel()
	return s.client.Close()
}

// newRedisClusterStore creates a new Redis Cluster vector store.
func newRedisClusterStore(ctx context.Context, config RedisClusterConfig, logger schemas.Logger) (*RedisClusterStore, error) {
	if len(config.Addrs) == 0 {
		return nil, fmt.Errorf("at least one Redis cluster address is required")
	}

	options := &redis.ClusterOptions{
		Addrs:    config.Addrs,
		Username: config.Username,
		Password: config.Password,
	}

	// Set cluster-specific options
	if config.MaxRedirects > 0 {
		options.MaxRedirects = config.MaxRedirects
	}
	options.ReadOnly = config.ReadOnly
	options.RouteByLatency = config.RouteByLatency
	options.RouteRandomly = config.RouteRandomly

	// Set connection pool and timeout options if provided
	if config.PoolSize > 0 {
		options.PoolSize = config.PoolSize
	}
	if config.MinIdleConns > 0 {
		options.MinIdleConns = config.MinIdleConns
	}
	if config.MaxIdleConns > 0 {
		options.MaxIdleConns = config.MaxIdleConns
	}
	if config.ConnMaxLifetime > 0 {
		options.ConnMaxLifetime = config.ConnMaxLifetime
	}
	if config.ConnMaxIdleTime > 0 {
		options.ConnMaxIdleTime = config.ConnMaxIdleTime
	}
	if config.DialTimeout > 0 {
		options.DialTimeout = config.DialTimeout
	}
	if config.ReadTimeout > 0 {
		options.ReadTimeout = config.ReadTimeout
	}
	if config.WriteTimeout > 0 {
		options.WriteTimeout = config.WriteTimeout
	}

	client := redis.NewClusterClient(options)
	pingCtx := ctx
	var cancel context.CancelFunc
	pingTimeout := 10 * time.Second
	if config.ContextTimeout > 0 {
		pingTimeout = config.ContextTimeout
	}
	pingCtx, cancel = context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	// Test the connection
	if err := client.Ping(pingCtx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis Cluster: %w", err)
	}

	return &RedisClusterStore{
		client: client,
		config: config,
		logger: logger,
	}, nil
}
