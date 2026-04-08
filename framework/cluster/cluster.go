// Package cluster provides Redis-backed distributed coordination for Bifrost.
// It handles: distributed rate limiting, budget enforcement, config cache
// invalidation via pub/sub, and leader election.
//
// Clustering is activated when ClusterConfig.Enabled = true.
// Without clustering, Bifrost operates in single-node mode using SQLite.
package cluster

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// ClusterConfig configures multi-node clustering.
type ClusterConfig struct {
	Enabled       bool     `json:"enabled"`
	RedisAddr     string   `json:"redis_addr"`    // single node: "redis:6379"
	RedisAddrs    []string `json:"redis_addrs"`   // cluster mode (multiple nodes)
	RedisPassword string   `json:"redis_password"`
	RedisDB       int      `json:"redis_db"`
	KeyPrefix     string   `json:"key_prefix"` // default: "bifrost:"
	NodeID        string   `json:"node_id"`    // auto-generated UUID if empty
}

// Defaults fills in zero-value fields.
func (c *ClusterConfig) Defaults() {
	if c.KeyPrefix == "" {
		c.KeyPrefix = "bifrost:"
	}
	if c.RedisAddr == "" && len(c.RedisAddrs) == 0 {
		c.RedisAddr = "localhost:6379"
	}
}

// RedisClient is a minimal interface wrapping the operations Bifrost needs
// from a Redis client. This allows swapping redis/go-redis with any compatible
// implementation or a mock in tests.
type RedisClient interface {
	// Incr increments a counter key by 1 and returns the new value.
	Incr(ctx context.Context, key string) (int64, error)
	// IncrByFloat increments a float counter by delta.
	IncrByFloat(ctx context.Context, key string, delta float64) (float64, error)
	// Get retrieves the string value of a key.
	Get(ctx context.Context, key string) (string, error)
	// Set sets a key to value with an optional TTL (0 = no expiry).
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	// SetNX sets a key only if it does not exist. Returns true if set.
	SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error)
	// Expire refreshes the TTL of a key.
	Expire(ctx context.Context, key string, ttl time.Duration) error
	// Del deletes one or more keys.
	Del(ctx context.Context, keys ...string) error
	// Publish publishes a message on a channel.
	Publish(ctx context.Context, channel, message string) error
	// Subscribe subscribes to a channel pattern and calls handler for each message.
	Subscribe(ctx context.Context, pattern string, handler func(channel, message string)) error
	// Keys returns all keys matching a pattern.
	Keys(ctx context.Context, pattern string) ([]string, error)
	// Close shuts down the client.
	Close() error
}

// ─── Distributed Rate Limiter ─────────────────────────────────────────────────

// RateLimiter implements a sliding-window rate limiter backed by Redis.
// Falls back to "allow" when Redis is unavailable (fail-open).
type RateLimiter struct {
	redis  RedisClient
	prefix string
}

// NewRateLimiter creates a Redis-backed rate limiter.
func NewRateLimiter(redis RedisClient, prefix string) *RateLimiter {
	return &RateLimiter{redis: redis, prefix: prefix}
}

// IncrAndCheck increments the sliding-window counter for key and returns
// (count, allowed). Window buckets are per-minute or per-hour granularity.
func (r *RateLimiter) IncrAndCheck(ctx context.Context, key string, limit int64, window time.Duration) (int64, bool) {
	bucket := time.Now().Truncate(window).Unix()
	redisKey := fmt.Sprintf("%srl:%s:%d", r.prefix, key, bucket)
	count, err := r.redis.Incr(ctx, redisKey)
	if err != nil {
		// Fail open: Redis unavailable → allow request
		return 0, true
	}
	_ = r.redis.Expire(ctx, redisKey, window*2)
	return count, count <= limit
}

// ─── Leader Elector ───────────────────────────────────────────────────────────

// LeaderElector performs leader election using a Redis key with TTL.
// Only the leader node executes background tasks (metrics flush, budget reset, alerts).
type LeaderElector struct {
	nodeID   string
	redis    RedisClient
	prefix   string
	isLeader atomic.Bool
	onLeader func() // called when this node becomes leader
	onFollow func() // called when this node loses leadership
	stopCh   chan struct{}
}

// NewLeaderElector creates a LeaderElector for the given node.
func NewLeaderElector(nodeID string, redis RedisClient, prefix string, onLeader, onFollow func()) *LeaderElector {
	return &LeaderElector{
		nodeID:   nodeID,
		redis:    redis,
		prefix:   prefix,
		onLeader: onLeader,
		onFollow: onFollow,
		stopCh:   make(chan struct{}),
	}
}

const (
	leaderTTL      = 15 * time.Second
	leaderRenewInt = 5 * time.Second
)

// Start begins the election loop. Call Stop() to exit.
func (e *LeaderElector) Start(ctx context.Context) {
	go e.loop(ctx)
}

// Stop signals the leader election loop to exit.
func (e *LeaderElector) Stop() { close(e.stopCh) }

// IsLeader returns true if this node is currently the leader.
func (e *LeaderElector) IsLeader() bool { return e.isLeader.Load() }

func (e *LeaderElector) loop(ctx context.Context) {
	key := e.prefix + "leader"
	ticker := time.NewTicker(leaderRenewInt)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if e.isLeader.Load() {
				// Renew leadership
				if err := e.redis.Set(ctx, key, e.nodeID, leaderTTL); err != nil {
					e.isLeader.Store(false)
					if e.onFollow != nil {
						e.onFollow()
					}
				}
			} else {
				// Try to acquire leadership
				ok, err := e.redis.SetNX(ctx, key, e.nodeID, leaderTTL)
				if err == nil && ok {
					e.isLeader.Store(true)
					if e.onLeader != nil {
						e.onLeader()
					}
				}
			}
		case <-e.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// ─── Config Pub/Sub ───────────────────────────────────────────────────────────

// ConfigPubSub broadcasts configuration change events between cluster nodes.
// Nodes subscribe to "bifrost:config:*" and invalidate local caches on receipt.
type ConfigPubSub struct {
	redis  RedisClient
	prefix string
}

// NewConfigPubSub creates a new config invalidation pub/sub.
func NewConfigPubSub(redis RedisClient, prefix string) *ConfigPubSub {
	return &ConfigPubSub{redis: redis, prefix: prefix}
}

// PublishProviderChanged notifies all nodes that provider config has changed.
func (p *ConfigPubSub) PublishProviderChanged(ctx context.Context, providerID string) error {
	return p.redis.Publish(ctx, p.prefix+"config:providers:"+providerID+":changed", providerID)
}

// PublishVirtualKeyChanged notifies all nodes that a virtual key has changed.
func (p *ConfigPubSub) PublishVirtualKeyChanged(ctx context.Context, vkID string) error {
	return p.redis.Publish(ctx, p.prefix+"config:virtual_keys:"+vkID+":changed", vkID)
}

// PublishFullReload triggers a full config reload on all nodes.
func (p *ConfigPubSub) PublishFullReload(ctx context.Context) error {
	return p.redis.Publish(ctx, p.prefix+"config:full_reload", "1")
}

// Subscribe starts listening for config change events and calls handler.
func (p *ConfigPubSub) Subscribe(ctx context.Context, handler func(channel, message string)) error {
	return p.redis.Subscribe(ctx, p.prefix+"config:*", handler)
}

// ─── Node Heartbeat ───────────────────────────────────────────────────────────

// NodeInfo is the heartbeat payload stored for each cluster node.
type NodeInfo struct {
	NodeID    string    `json:"node_id"`
	Version   string    `json:"version"`
	Addr      string    `json:"addr"`
	StartedAt time.Time `json:"started_at"`
	LastSeen  time.Time `json:"last_seen"`
	IsLeader  bool      `json:"is_leader"`
}

// ClusterStatus is returned by GET /api/cluster/status.
type ClusterStatus struct {
	NodeID       string     `json:"node_id"`
	IsLeader     bool       `json:"is_leader"`
	LeaderNodeID string     `json:"leader_node_id"`
	ClusterSize  int        `json:"cluster_size"`
	Nodes        []NodeInfo `json:"nodes"`
}
