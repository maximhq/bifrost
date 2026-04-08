# TECH-007 — Multi-Node Clustering

**Feature ID:** CLUSTER  
**SRS Reference:** §3.18 (CLUSTER-01 → CLUSTER-10)  
**CR Reference:** CR-ENT-001, CR-ENT-002  
**Version:** 1.0 | **Date:** 2026-04-08  
**Status:** Design Ready

---

## 1. Overview

Transform Bifrost from a single-node deployment to a horizontally scalable cluster. Multiple Bifrost nodes share state via PostgreSQL + Redis, with no single point of failure.

**Current single-node limitations:**
- SQLite cannot be shared across nodes
- In-memory governance counters (rate limit, budget) are per-node
- KVStore is in-memory only
- No leader election for background jobs (retention cleaner, pricing sync)

---

## 2. Architecture

```
                    ┌────────────────────────────────┐
                    │        Load Balancer            │
                    │  (HAProxy / ELB / Nginx)        │
                    └────────────┬─────────────────────┘
                                 │
              ┌──────────────────┼──────────────────┐
              │                  │                  │
     ┌────────▼──────┐   ┌───────▼───────┐  ┌──────▼────────┐
     │  Bifrost-1    │   │  Bifrost-2    │  │  Bifrost-3    │
     │  :8080        │   │  :8080        │  │  :8080        │
     └───────┬───────┘   └───────┬───────┘  └──────┬────────┘
             │                   │                  │
             └───────────────────┼──────────────────┘
                                 │
                    ┌────────────▼─────────────────┐
                    │        Shared State          │
                    ├──────────────────────────────┤
                    │  PostgreSQL (Config + Logs)  │
                    │  Redis (KV + Rate Limits)    │
                    │  (Optional: etcd for leader) │
                    └──────────────────────────────┘
```

---

## 3. Shared State Requirements

| State Category | Current | Clustered |
|---|---|---|
| Config (providers, VKs, plugins) | SQLite | PostgreSQL (existing pg backend) |
| Logs | SQLite | PostgreSQL (existing pg backend) |
| Rate limit counters | In-memory per node | Redis atomic INCR |
| Budget usage counters | In-memory per node | Redis atomic INCR |
| Session tokens | SQLite | PostgreSQL |
| KVStore (session stickiness) | In-memory | Redis |
| Leader election (background jobs) | N/A | Redis SETNX / PostgreSQL advisory lock |
| WebSocket fan-out | Local broadcast | Redis pub/sub |

---

## 4. Architecture Changes

### 4.1 PostgreSQL Config + Log Store

Already supported in `framework/configstore/postgres.go` and `framework/logstore/postgres.go`. Only configuration change needed:

```json
// config.json — switch from SQLite to PostgreSQL
{
  "database": {
    "type": "postgres",
    "dsn": "host=pg-primary port=5432 user=bifrost dbname=bifrost sslmode=require"
  }
}
```

### 4.2 Redis KVStore

```go
// framework/kvstore/redis.go (NEW)

type RedisKVStore struct {
    client *redis.Client
}

func NewRedisKVStore(cfg RedisConfig) *RedisKVStore {
    rdb := redis.NewClient(&redis.Options{
        Addr:     cfg.Addr,
        Password: cfg.Password,
        DB:       cfg.DB,
        TLSConfig: cfg.TLSConfig,
        PoolSize: cfg.PoolSize,  // default: 10
    })
    return &RedisKVStore{client: rdb}
}

func (s *RedisKVStore) Get(ctx context.Context, key string) (string, error) {
    return s.client.Get(ctx, key).Result()
}

func (s *RedisKVStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
    return s.client.Set(ctx, key, value, ttl).Err()
}

func (s *RedisKVStore) Delete(ctx context.Context, key string) error {
    return s.client.Del(ctx, key).Err()
}

// Atomic increment for rate limiting
func (s *RedisKVStore) IncrBy(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, error) {
    pipe := s.client.TxPipeline()
    incr := pipe.IncrBy(ctx, key, delta)
    pipe.Expire(ctx, key, ttl)
    _, err := pipe.Exec(ctx)
    return incr.Val(), err
}
```

### 4.3 Distributed Rate Limiting

Replace in-memory counters in `plugins/governance/` with Redis atomic operations:

```go
// plugins/governance/distributed_ratelimit.go (NEW)

type DistributedRateLimiter struct {
    kvStore schemas.KVStore  // must be RedisKVStore for distributed
    nodeID  string
}

func (r *DistributedRateLimiter) CheckAndIncrement(ctx context.Context, vkID string, window time.Duration) (int64, error) {
    // Sliding window key: rl:{vk_id}:{window_bucket}
    bucket := time.Now().Truncate(window).Unix()
    key := fmt.Sprintf("rl:%s:%d", vkID, bucket)
    
    count, err := r.kvStore.(RedisIncrBy).IncrBy(ctx, key, 1, window)
    return count, err
}

// Falls back to LocalGovernanceStore if kvStore is in-memory
func (r *DistributedRateLimiter) IsDistributed() bool {
    _, ok := r.kvStore.(*RedisKVStore)
    return ok
}
```

### 4.4 Distributed Budget Tracking

Budget counters need atomic cross-node updates:

```go
// plugins/governance/distributed_budget.go (NEW)

// Use Redis for real-time counter, sync to PostgreSQL every N seconds
type DistributedBudgetTracker struct {
    redis    *RedisKVStore
    postgres configstore.ConfigStore
    syncInterval time.Duration
}

func (t *DistributedBudgetTracker) IncrementUsage(ctx context.Context, vkID string, tokens int64, costUSD float64) error {
    // Atomic Redis increment
    tokenKey := fmt.Sprintf("budget:tokens:%s", vkID)
    costKey  := fmt.Sprintf("budget:cost:%s", vkID)
    
    pipe := t.redis.client.TxPipeline()
    pipe.IncrByFloat(ctx, costKey, costUSD)
    pipe.IncrBy(ctx, tokenKey, tokens)
    _, err := pipe.Exec(ctx)
    return err
}

func (t *DistributedBudgetTracker) GetUsage(ctx context.Context, vkID string) (tokens int64, cost float64, err error) {
    // Read from Redis for low latency
    pipe := t.redis.client.Pipeline()
    tokensCmd := pipe.Get(ctx, fmt.Sprintf("budget:tokens:%s", vkID))
    costCmd   := pipe.Get(ctx, fmt.Sprintf("budget:cost:%s", vkID))
    pipe.Exec(ctx)
    
    tokens, _ = tokensCmd.Int64()
    cost, _   = costCmd.Float64()
    return
}

// Background sync to PostgreSQL for persistence
func (t *DistributedBudgetTracker) SyncToDB(ctx context.Context) { ... }
```

### 4.5 Leader Election

Background jobs (log cleaner, pricing sync, budget reset) should only run on one node:

```go
// framework/cluster/leader.go (NEW)

type LeaderElection struct {
    kvStore  schemas.KVStore
    nodeID   string
    leaseKey string
    leaseTTL time.Duration
    mu       sync.Mutex
    isLeader atomic.Bool
}

func (l *LeaderElection) TryBecomeLeader(ctx context.Context) bool {
    // Redis SETNX equivalent: SET key nodeID NX EX ttl
    key := fmt.Sprintf("bifrost:leader:%s", l.leaseKey)
    set, err := l.kvStore.(*RedisKVStore).client.SetNX(ctx, key, l.nodeID, l.leaseTTL).Result()
    if err != nil { return false }
    l.isLeader.Store(set)
    return set
}

func (l *LeaderElection) RenewLease(ctx context.Context) bool {
    // GET + compare, then EXPIRE — or use Lua script for atomicity
    key := fmt.Sprintf("bifrost:leader:%s", l.leaseKey)
    curr, _ := l.kvStore.Get(ctx, key)
    if curr != l.nodeID { 
        l.isLeader.Store(false)
        return false 
    }
    l.kvStore.Set(ctx, key, l.nodeID, l.leaseTTL)
    l.isLeader.Store(true)
    return true
}

// Usage in background jobs:
// if !leaderElection.IsLeader() { return }  // skip if not leader
```

### 4.6 Distributed WebSocket Fan-out

The current `WebSocketHandler.Broadcast*()` is local-only. In a cluster, a log event written by Bifrost-1 must be broadcast to UI clients connected to Bifrost-2:

```go
// framework/cluster/pubsub.go (NEW)

type ClusterPubSub struct {
    redis  *redis.Client
    nodeID string
}

const logEventChannel = "bifrost:events:logs"

func (p *ClusterPubSub) Publish(ctx context.Context, eventType string, payload any) error {
    msg, _ := json.Marshal(map[string]any{
        "type":    eventType,
        "payload": payload,
        "node":    p.nodeID,
    })
    return p.redis.Publish(ctx, logEventChannel, msg).Err()
}

// In WebSocketHandler: subscribe and forward to local WS clients
func (h *WebSocketHandler) StartClusterSubscriber(pubsub *ClusterPubSub) {
    sub := pubsub.redis.Subscribe(context.Background(), logEventChannel)
    go func() {
        for msg := range sub.Channel() {
            // Forward to all locally connected WS clients
            h.BroadcastRaw([]byte(msg.Payload))
        }
    }()
}
```

---

## 5. Node Configuration

```json
// config.json — cluster mode
{
  "cluster": {
    "enabled": true,
    "node_id": "env.BIFROST_NODE_ID",   // unique per pod (e.g., pod name)
    "redis": {
      "addr": "redis-cluster:6379",
      "password": "env.REDIS_PASSWORD",
      "tls_enabled": true,
      "pool_size": 20
    },
    "leader_lease_ttl": "30s",
    "leader_renew_interval": "10s"
  }
}
```

---

## 6. Cluster Status API

```go
// transports/bifrost-http/handlers/cluster.go (NEW)

// GET /api/cluster/status
// Returns: node list, leader identity, per-node health, shared state connectivity
type ClusterStatus struct {
    NodeID        string         `json:"node_id"`
    IsLeader      bool           `json:"is_leader"`
    Nodes         []NodeInfo     `json:"nodes"`
    RedisHealth   string         `json:"redis_health"`
    PostgresHealth string        `json:"postgres_health"`
}

// POST /api/cluster/rebalance  — trigger manual key rebalancing (super_admin only)
```

---

## 7. Node Health Registration

Each node registers itself on startup and refreshes periodically:

```go
// framework/cluster/registry.go (NEW)

type NodeRegistry struct {
    kvStore  *RedisKVStore
    nodeInfo NodeInfo
}

type NodeInfo struct {
    ID          string    `json:"id"`
    Host        string    `json:"host"`
    Port        int       `json:"port"`
    Version     string    `json:"version"`
    StartedAt   time.Time `json:"started_at"`
    LastSeenAt  time.Time `json:"last_seen_at"`
    IsLeader    bool      `json:"is_leader"`
}

func (r *NodeRegistry) Register(ctx context.Context) {
    key := fmt.Sprintf("bifrost:nodes:%s", r.nodeInfo.ID)
    data, _ := json.Marshal(r.nodeInfo)
    r.kvStore.Set(ctx, key, string(data), 60*time.Second)
}

func (r *NodeRegistry) ListNodes(ctx context.Context) ([]NodeInfo, error) {
    // SCAN bifrost:nodes:* → get all node records
}
```

---

## 8. Migration Path

**Phase 1 (compatible):** Switch SQLite → PostgreSQL. Existing schema is identical.

**Phase 2:** Enable Redis KVStore. Rate limiters auto-detect and use distributed mode.

**Phase 3:** Enable cluster mode. Leader election + Redis pub/sub activated.

```go
// framework/configstore/migrations.go — add cluster tables
func migrateV5ClusterTables(db *gorm.DB) error {
    return db.AutoMigrate(&tables.ClusterNodesTable{})
}
```

---

## 9. UI Components

```
ui/app/enterprise/cluster/
├── page.tsx                  — Node topology map + health indicators
└── components/
    ├── NodeCard.tsx          — Per-node CPU, memory, request rate, leader badge
    ├── TopologyGraph.tsx     — Visual cluster topology
    └── SharedStateHealth.tsx — Redis + PostgreSQL connectivity status
```

---

## 10. Performance Targets (SRS SCALE-01→05)

| Metric | Target |
|--------|--------|
| Horizontal scale | ≥ 10 nodes |
| Rate limit accuracy | ≤ 5% over-count under split-brain |
| Budget accuracy | ≤ 1% drift with Redis pipeline batching |
| Node failover | < 30s detection + traffic reroute |
| Cluster status API latency | < 100ms |
