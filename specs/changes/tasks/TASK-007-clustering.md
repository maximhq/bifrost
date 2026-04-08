# TASK-007 — Clustering

**Feature:** Multi-Node Clustering  
**TECH Spec:** [TECH-007-clustering.md](../TECH-007-clustering.md)  
**Phase:** 3 (Infrastructure)  
**Depends on:** TASK-014 (license)  
**Estimate:** 8 days  
**Assignee:** —  
**Status:** 🟢 Completed

---

## Context

Clustering transitions Bifrost from single-node (SQLite) to multi-node shared state (PostgreSQL + Redis). Nodes elect a leader for background tasks (metrics flush, budget reset) and use Redis pub/sub for config cache invalidation across nodes.

**Key architectural changes:**
- SQLite → PostgreSQL for all persistent state
- In-memory rate limit counters → Redis atomic increments
- Local config cache → Redis-backed cache with pub/sub invalidation
- Leader election via Redis with TTL-based heartbeats
- Health endpoint enhanced with cluster membership info

---

## Tasks

### TASK-007-01 — PostgreSQL migration support

**Files to modify:**
- `framework/configstore/store.go` — add `PostgreSQL` as database option
- `framework/configstore/migrations/` — ensure all migrations are Postgres-compatible (no SQLite-only syntax)

**Files to create:**
- `framework/configstore/postgres.go` — Postgres GORM connection setup with connection pooling

**Acceptance criteria:**
- [ ] `BIFROST_DB_TYPE=postgres BIFROST_DB_DSN=postgres://...` starts without error
- [ ] All existing migrations run correctly on PostgreSQL (test with `make test-governance DB=postgres`)
- [ ] Connection pool: `MaxOpenConns=25`, `MaxIdleConns=5`, `ConnMaxLifetime=5m` (configurable)
- [ ] SQLite remains default for backward compatibility (no breaking change)

---

### TASK-007-02 — Redis client integration

**Files to create:**
- `framework/cluster/redis.go` — `RedisClient` wrapper (using `github.com/redis/go-redis/v9`)
- `framework/cluster/config.go` — `ClusterConfig`

**Configuration:**
```go
type ClusterConfig struct {
    Enabled     bool
    RedisAddr   string   // single node: "redis:6379"
    RedisAddrs  []string // cluster mode
    RedisPassword string
    RedisDB     int
    KeyPrefix   string  // default: "bifrost:"
}
```

**Acceptance criteria:**
- [ ] Both standalone and Redis Cluster modes supported
- [ ] Connection tested at bootstrap; startup fails with clear error if clustering enabled but Redis unreachable
- [ ] All keys use `KeyPrefix` (default `bifrost:`) to avoid collisions in shared Redis

---

### TASK-007-03 — Distributed rate limiting

**Files to modify:**
- `plugins/governance/ratelimit.go` — replace in-memory counters with Redis-backed counters

**Implementation:**
```go
// Sliding window counter using Redis atomic increment
func (r *DistributedRateLimiter) IncrAndCheck(ctx context.Context, key string, limit int64, window time.Duration) (int64, bool) {
    redisKey := fmt.Sprintf("bifrost:rl:%s:%d", key, time.Now().Truncate(window).Unix())
    count, err := r.redis.Incr(ctx, redisKey).Result()
    if err != nil {
        // Fail open: Redis unavailable → allow request
        logger.Warn("Redis rate limit unavailable, allowing request", "key", key)
        return 0, true
    }
    r.redis.Expire(ctx, redisKey, window*2)
    return count, count <= limit
}
```

**Acceptance criteria:**
- [ ] Rate limit counters are consistent across all nodes (within Redis replication lag)
- [ ] Redis unavailability → fail open (allow request), log warning
- [ ] Existing governance rate limit tests pass with Redis backend
- [ ] Sliding window: per-minute and per-hour windows implemented
- [ ] Key format: `bifrost:rl:{vk_id|team_id|customer_id}:{window_start_unix}`

---

### TASK-007-04 — Distributed budget enforcement

**Files to modify:**
- `plugins/governance/budget.go` — use Redis for real-time budget tracking

**Implementation:**
```go
// Budget key: "bifrost:budget:{entity_id}:{period_start}"
// On each request completion: INCRBYFLOAT key cost_amount EX window_seconds
// On budget check: GET key → compare with limit
```

**Acceptance criteria:**
- [ ] Budget consumption is consistent across nodes
- [ ] Budget reset (period end) clearing via Redis key expiry (TTL = window duration)
- [ ] Redis unavailable → fail open (allow request) with log warning
- [ ] Existing governance budget E2E tests pass with Redis backend (`REDIS_ADDR` env var set)

---

### TASK-007-05 — Config cache invalidation via Redis pub/sub

**Files to create:**
- `framework/cluster/pubsub.go` — `ClusterPubSub`

**Events published on config changes:**
- `bifrost:config:providers:{id}:changed`
- `bifrost:config:virtual_keys:{id}:changed`
- `bifrost:config:plugins:changed`
- `bifrost:config:full_reload` (for bulk changes)

**Subscription in server:**
```go
// On receive: invalidate local in-memory cache for affected entity
pubsub.Subscribe("bifrost:config:*", func(event ConfigChangeEvent) {
    configCache.Invalidate(event.EntityType, event.EntityID)
})
```

**Acceptance criteria:**
- [ ] Config change in Node A reflected in Node B within 500ms (Redis pub/sub latency)
- [ ] Re-subscription on Redis connection loss (retry with backoff)
- [ ] Events published from all state-modifying API handlers

---

### TASK-007-06 — Leader election

**Files to create:**
- `framework/cluster/leader.go` — `LeaderElector`

**Implementation:**
```go
// Leader key: "bifrost:leader" with TTL=15s
// Each node tries SET NX EX 15 with node ID
// Leader renews TTL every 5s
// Non-leaders poll every 5s

type LeaderElector struct {
    nodeID   string
    redis    *RedisClient
    isLeader atomic.Bool
    onLeader func()   // callback when becoming leader
    onFollow func()   // callback when losing leadership
}
```

**Only leader executes:**
- Metrics aggregation flush to DB
- Budget reset on period boundary
- Alert rule evaluation
- SCIM sync polling

**Acceptance criteria:**
- [ ] Only one node is leader at any time (verified by integration test with 3 nodes)
- [ ] Leader failover within 20 seconds (TTL=15s + detection lag)
- [ ] `GET /api/cluster/status` returns `is_leader: true/false` and `leader_node_id`

---

### TASK-007-07 — Cluster management API

**Files to create:**
- `transports/bifrost-http/handlers/cluster.go`

**Endpoints:**
```
GET /api/cluster/status
  Returns: node_id, is_leader, leader_node_id, cluster_size, nodes (list with health)

GET /api/cluster/nodes
  Returns list of known nodes with last_heartbeat, version, is_leader

POST /api/cluster/drain
  Gracefully stop accepting new requests (for rolling restarts) — super_admin only
```

**Node heartbeat:**
```
Key: "bifrost:node:{node_id}"
Value: JSON {version, addr, started_at, last_seen}
TTL: 30s (renewed every 10s)
```

**Acceptance criteria:**
- [ ] Stale nodes (TTL expired) automatically removed from cluster view
- [ ] `GET /api/cluster/status` works even when not in cluster mode (returns single-node info)
- [ ] All endpoints require `clustering` feature enabled (license check)

---

### TASK-007-08 — UI: cluster status dashboard

**Files to create:**
- `ui/app/enterprise/cluster/page.tsx`
- `ui/app/enterprise/cluster/components/NodeCard.tsx`
- `ui/app/enterprise/cluster/components/LeaderBadge.tsx`
- `ui/app/enterprise/cluster/components/HealthTimeline.tsx`

**Acceptance criteria:**
- [ ] Node list with leader badge, version, uptime for each node
- [ ] Auto-refresh every 10 seconds
- [ ] Page inside `<EnterpriseGate feature="clustering">`

---

## Definition of Done

- [ ] All subtasks complete
- [ ] Integration test: 2-node cluster with shared Redis → rate limits enforced across both nodes
- [ ] Integration test: leader election — kill leader node → new leader elected within 20s
- [ ] Integration test: config change on Node A → cache invalidated on Node B within 500ms
- [ ] Existing governance tests pass with `BIFROST_DB_TYPE=postgres`
- [ ] `make build` passes
