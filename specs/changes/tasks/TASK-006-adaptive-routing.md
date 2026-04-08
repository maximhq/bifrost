# TASK-006 — Adaptive Routing

**Feature:** Adaptive Routing Engine  
**TECH Spec:** [TECH-006-adaptive-routing.md](../TECH-006-adaptive-routing.md)  
**Phase:** 4 (Intelligence)  
**Depends on:** TASK-014 (license), TASK-007 (Clustering — for shared metrics state)  
**Estimate:** 6 days  
**Assignee:** —  
**Status:** 🟢 Completed

---

## Context

Adaptive routing dynamically selects the best provider/model for each request based on real-time metrics (latency, error rate, cost, availability). It extends the existing provider key selection mechanism in `core/bifrost.go`.

**Routing strategies:**
- `latency_optimized` — lowest P95 latency
- `cost_optimized` — lowest cost per token
- `quality_optimized` — highest quality score (from feedback)
- `availability_optimized` — highest availability (lowest error rate)
- `balanced` — weighted score across all dimensions
- `canary` — percentage-based traffic split between models

---

## Tasks

### TASK-006-01 — Database schema + GORM migration

**Files to create:**
- `framework/configstore/tables/adaptive_routing.go` — `RoutingPolicyTable`, `ProviderMetricsTable`, `ModelQualityScoreTable`
- Migration file

**Schema:**
```go
type RoutingPolicyTable struct {
    ID              string    `gorm:"primaryKey;type:text"`
    Name            string    `gorm:"uniqueIndex;not null"`
    Enabled         bool      `gorm:"default:true"`
    Strategy        string    // "latency_optimized"|"cost_optimized"|"quality_optimized"|"availability_optimized"|"balanced"|"canary"
    // Scope
    VirtualKeyID    string    `gorm:"index"` // empty = global
    // Strategy weights (for "balanced")
    WeightLatency   float64   `gorm:"default:0.25"`
    WeightCost      float64   `gorm:"default:0.25"`
    WeightQuality   float64   `gorm:"default:0.25"`
    WeightAvail     float64   `gorm:"default:0.25"`
    // Thresholds
    MaxLatencyMs    *float64  // fallback if P95 exceeds this
    MaxErrorRatePct *float64  // fallback if error rate exceeds
    MinQualityScore *float64  // fallback if quality below
    // Canary config JSON: [{provider, model, pct}]
    CanaryConfigJSON string   `gorm:"type:text"`
    CreatedAt       time.Time
    UpdatedAt       time.Time
}

type ProviderMetricsTable struct {
    ID          string    `gorm:"primaryKey;type:text"`
    Provider    string    `gorm:"index;not null"`
    Model       string    `gorm:"index;not null"`
    WindowStart time.Time `gorm:"index"`
    P50Ms       float64
    P95Ms       float64
    P99Ms       float64
    ErrorRatePct float64
    TotalRequests int64
    TotalCost   float64
    AvgTokens   float64
    UpdatedAt   time.Time
}

type ModelQualityScoreTable struct {
    Provider    string    `gorm:"primaryKey;type:text"`
    Model       string    `gorm:"primaryKey;type:text"`
    Score       float64   // 0.0–1.0 (from feedback or benchmarks)
    Source      string    // "manual"|"feedback"|"benchmark"
    UpdatedAt   time.Time
}
```

**Acceptance criteria:**
- [ ] Migration runs cleanly; idempotent
- [ ] `ProviderMetricsTable` supports time-windowed queries (5m, 1h, 24h)

---

### TASK-006-02 — Metrics collector (ObservabilityPlugin)

**Files to create:**
- `plugins/adaptiverouting/metrics_collector.go` — `MetricsCollector` implementing `ObservabilityPlugin`
- `plugins/adaptiverouting/go.mod`

**Implementation:**
```go
func (c *MetricsCollector) Inject(ctx context.Context, trace *schemas.Trace) error {
    // Extract: provider, model, latency, tokens, cost, error status
    // Update in-memory ring buffer for current 5-minute window
    // Flush to ProviderMetricsTable every 60s
    c.ring.Record(trace)
    return nil
}
```

**Acceptance criteria:**
- [ ] Metrics updated per trace in O(1) time (ring buffer, no locks on hot path)
- [ ] Metrics flushed to DB every 60 seconds (background goroutine)
- [ ] Handles missing trace fields gracefully (nil cost, nil tokens)
- [ ] Ring buffer covers 3 windows: 5m, 1h, 24h

---

### TASK-006-03 — Routing engine

**Files to create:**
- `plugins/adaptiverouting/engine.go` — `RoutingEngine`
- `plugins/adaptiverouting/strategies/latency.go`
- `plugins/adaptiverouting/strategies/cost.go`
- `plugins/adaptiverouting/strategies/quality.go`
- `plugins/adaptiverouting/strategies/balanced.go`
- `plugins/adaptiverouting/strategies/canary.go`

**Integration point — PreLLMHook:**
```go
func (p *AdaptiveRoutingPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (
    *schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
    
    if !license.IsFeatureEnabled("adaptive_routing") {
        return req, nil, nil
    }
    
    policy := p.engine.GetPolicy(ctx)
    if policy == nil {
        return req, nil, nil  // no policy = pass through
    }
    
    selected, err := p.engine.SelectProvider(ctx, req, policy)
    if err != nil {
        return req, nil, nil  // fail open
    }
    
    // Override the provider/model in the request
    req.Provider = selected.Provider
    req.Model = selected.Model
    return req, nil, nil
}
```

**Acceptance criteria:**
- [ ] `SelectProvider` falls back to original request provider if all candidates are unhealthy
- [ ] Canary routing distributes traffic deterministically by request hash (same user → same canary bucket)
- [ ] Policy cache TTL: 30 seconds (invalidated on policy update)
- [ ] Provider selection considers only currently configured and enabled providers

---

### TASK-006-04 — Routing policy management API

**Files to create:**
- `transports/bifrost-http/handlers/adaptive_routing.go`

**Endpoints:**
```
GET    /api/routing/policies             — list policies (admin+)
POST   /api/routing/policies             — create policy (admin+)
GET    /api/routing/policies/{id}        — get policy
PUT    /api/routing/policies/{id}        — update policy (admin+)
DELETE /api/routing/policies/{id}        — delete policy (admin+)

GET /api/routing/metrics                 — current provider metrics snapshot
  Query: provider=, model=, window=5m|1h|24h

GET /api/routing/simulate?request_body=  — simulate routing decision for a request
  Returns: selected provider, model, strategy, reason
```

**Acceptance criteria:**
- [ ] All endpoints require `adaptive_routing` feature enabled
- [ ] `/api/routing/simulate` does not make actual LLM calls — dry run only
- [ ] Metrics endpoint returns data for all providers within requested window

---

### TASK-006-05 — UI: routing dashboard

**Files to create:**
- `ui/app/enterprise/adaptive-routing/page.tsx`
- `ui/app/enterprise/adaptive-routing/components/ProviderMetricsGrid.tsx`
- `ui/app/enterprise/adaptive-routing/components/RoutingPolicyForm.tsx`
- `ui/app/enterprise/adaptive-routing/components/CanaryConfigPanel.tsx`
- `ui/app/enterprise/adaptive-routing/components/RoutingSimulator.tsx`

**Acceptance criteria:**
- [ ] Provider metrics grid: latency, error rate, cost columns for each provider/model
- [ ] Color-coded health indicators (green/yellow/red) based on thresholds
- [ ] Routing simulator: paste a request body → shows which provider would be selected and why
- [ ] Canary config panel with percentage sliders (must sum to 100%)
- [ ] Page inside `<EnterpriseGate feature="adaptive_routing">`

---

## Definition of Done

- [ ] All subtasks complete
- [ ] Unit tests: each strategy selects correct provider given mock metrics
- [ ] Unit tests: canary distribution is within ±2% of configured percentages over 10,000 requests
- [ ] Integration test: high error rate provider → engine selects alternative
- [ ] Integration test: metrics correctly accumulated from ObservabilityPlugin traces
- [ ] `make build` passes
