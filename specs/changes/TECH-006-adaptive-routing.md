# TECH-006 — Adaptive Routing Engine

**Feature ID:** AROUTE  
**SRS Reference:** §3.17 (AROUTE-01 → AROUTE-10)  
**CR Reference:** CR-ENT-001, CR-ENT-002  
**Version:** 1.0 | **Date:** 2026-04-08  
**Status:** Design Ready

---

## 1. Overview

Extend the existing CEL-based routing rule system with a real-time, cost/latency-aware adaptive routing engine. The engine observes live provider health metrics and dynamically adjusts request distribution beyond static CEL rules.

**Adaptive strategies (SRS AROUTE-01):**
- `latency_optimized` — route to lowest P50 latency provider (measured last 5 min)
- `cost_optimized` — route to cheapest provider per 1M tokens for the requested model
- `availability_optimized` — avoid providers with recent error rate > threshold
- `geo_affinity` — route to provider closest to request origin region
- `round_robin` — vanilla round robin (ignores metrics)
- `weighted_random` — existing behavior (default)

---

## 2. Architecture Mapping

```
plugins/adaptiverouting/          (NEW independent Go module)
├── go.mod
├── plugin.go                     AdaptiveRoutingPlugin (LLMPlugin)
├── engine.go                     RoutingEngine — strategy evaluation
├── metrics.go                    ProviderMetricsCollector (PostHook sink)
├── strategies/
│   ├── latency.go                LatencyOptimizedStrategy
│   ├── cost.go                   CostOptimizedStrategy
│   ├── availability.go           AvailabilityStrategy
│   ├── geo.go                    GeoAffinityStrategy
│   └── roundrobin.go             RoundRobinStrategy
├── window.go                     Sliding time-window metrics aggregator
└── config.go                     AdaptiveRoutingConfig

core/schemas/
└── bifrost.go    (MODIFY) Add BifrostContextKeyAdaptiveRoutingDecision context key

transports/bifrost-http/
└── handlers/routing.go  (MODIFY) expose /api/routing/metrics endpoint
```

---

## 3. Configuration

```go
// plugins/adaptiverouting/config.go

type AdaptiveRoutingConfig struct {
    Enabled            bool
    DefaultStrategy    RoutingStrategy    // applied when no CEL rule matches
    WindowDuration     time.Duration      // metrics window (default: 5 min)
    MinSampleSize      int                // min requests before adapting (default: 10)
    ErrorRateThreshold float64            // mark provider unhealthy above this (default: 0.2)
    Strategies         []StrategyConfig
}

type RoutingStrategy string
const (
    StrategyLatency      RoutingStrategy = "latency_optimized"
    StrategyCost         RoutingStrategy = "cost_optimized"
    StrategyAvailability RoutingStrategy = "availability_optimized"
    StrategyGeoAffinity  RoutingStrategy = "geo_affinity"
    StrategyRoundRobin   RoutingStrategy = "round_robin"
    StrategyWeighted     RoutingStrategy = "weighted_random"   // existing default
)

type StrategyConfig struct {
    Strategy      RoutingStrategy
    // Latency params
    LatencyP      int     // percentile: 50, 95, 99 (default: 50)
    // Cost params
    // ← uses ModelCatalog pricing automatically
    // Availability params
    ErrorRateThreshold float64
    // Geo params
    GeoRegions    []GeoRegionMapping  // {region: "us-east-1", providers: ["openai", "bedrock"]}
}
```

---

## 4. Metrics Collection

```go
// plugins/adaptiverouting/metrics.go

// ProviderMetrics holds a sliding window of observations per (provider, model) pair
type ProviderMetrics struct {
    mu          sync.RWMutex
    windows     map[string]*MetricWindow  // key: "provider:model"
    windowDur   time.Duration
}

type MetricWindow struct {
    observations []Observation  // ring buffer
    head, count  int
    capacity     int
}

type Observation struct {
    Timestamp time.Time
    LatencyMs float64
    Success   bool
    InputTokens  int64
    OutputTokens int64
    CostUSD   float64
}

// Summary computed from window
type ProviderSummary struct {
    Provider     string
    Model        string
    P50LatencyMs float64
    P95LatencyMs float64
    ErrorRate    float64   // 0.0 – 1.0
    AvgCostPer1M float64   // USD per 1M input tokens
    SampleCount  int
    Healthy      bool
}

func (m *ProviderMetrics) Record(provider, model string, obs Observation) {
    m.mu.Lock()
    defer m.mu.Unlock()
    key := provider + ":" + model
    w, ok := m.windows[key]
    if !ok {
        w = newMetricWindow(1000)  // ring buffer capacity
        m.windows[key] = w
    }
    w.add(obs)
}

func (m *ProviderMetrics) Summarize(provider, model string) *ProviderSummary {
    m.mu.RLock()
    defer m.mu.RUnlock()
    key := provider + ":" + model
    w, ok := m.windows[key]
    if !ok {
        return nil
    }
    return w.compute(provider, model, m.windowDur)
}
```

---

## 5. Plugin Implementation

```go
// plugins/adaptiverouting/plugin.go

type AdaptiveRoutingPlugin struct {
    config   AdaptiveRoutingConfig
    metrics  *ProviderMetrics
    engine   *RoutingEngine
    catalog  modelcatalog.ModelCatalog
    mu       sync.RWMutex
}

// PreLLMHook: select the best provider/model override and inject into request
func (p *AdaptiveRoutingPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (
    *schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
    
    if !p.config.Enabled {
        return req, nil, nil
    }
    
    // Resolve candidate providers from request's provider field
    // (This plugin works as an advisor — actual routing still done by core key selection)
    candidates := p.engine.ResolveCandidates(req)
    if len(candidates) == 0 {
        return req, nil, nil
    }
    
    strategy := p.resolveStrategy(req)
    best, err := p.engine.SelectBest(strategy, candidates, req)
    if err != nil || best == nil {
        return req, nil, nil  // fall back to existing key selection
    }
    
    // Override the model field to route to the selected provider
    if best.Provider != string(req.Provider) || best.Model != req.Model {
        newReq := *req
        newReq.Provider = schemas.ModelProvider(best.Provider)
        newReq.Model = best.Model
        ctx.SetValue(schemas.BifrostContextKeyAdaptiveRoutingDecision, best)
        return &newReq, nil, nil
    }
    
    return req, nil, nil
}

// PostLLMHook: record metrics for the completed request
func (p *AdaptiveRoutingPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (
    *schemas.BifrostResponse, *schemas.BifrostError, error) {
    
    obs := extractObservation(ctx, resp, bifrostErr)
    if obs != nil {
        p.metrics.Record(obs.Provider, obs.Model, *obs)
    }
    return resp, bifrostErr, nil
}
```

---

## 6. Routing Engine

```go
// plugins/adaptiverouting/engine.go

type RoutingDecision struct {
    Provider string
    Model    string
    Reason   string    // "latency_p50_lowest", "cost_optimal", etc.
    Score    float64
}

type RoutingEngine struct {
    strategies map[RoutingStrategy]Strategy
    metrics    *ProviderMetrics
}

type Strategy interface {
    Score(summary *ProviderSummary) float64
    IsEligible(summary *ProviderSummary, config AdaptiveRoutingConfig) bool
}

func (e *RoutingEngine) SelectBest(strategy RoutingStrategy, candidates []Candidate, req *schemas.BifrostRequest) (*RoutingDecision, error) {
    strat := e.strategies[strategy]
    
    var best *RoutingDecision
    for _, c := range candidates {
        summary := e.metrics.Summarize(c.Provider, c.Model)
        if summary == nil || summary.SampleCount < e.config.MinSampleSize {
            // Insufficient data — don't penalize, use neutral score
            summary = &ProviderSummary{Provider: c.Provider, Model: c.Model, Healthy: true}
        }
        if !strat.IsEligible(summary, e.config) {
            continue
        }
        score := strat.Score(summary)
        if best == nil || score > best.Score {
            best = &RoutingDecision{Provider: c.Provider, Model: c.Model, Score: score}
        }
    }
    
    if best != nil {
        best.Reason = string(strategy) + "_selected"
    }
    return best, nil
}
```

### 6.1 Latency Strategy

```go
// plugins/adaptiverouting/strategies/latency.go

type LatencyStrategy struct{ p int }  // p=50 or 95

func (s *LatencyStrategy) Score(summary *ProviderSummary) float64 {
    latency := summary.P50LatencyMs
    if s.p == 95 { latency = summary.P95LatencyMs }
    if latency == 0 { return 0.5 }  // neutral
    return 1.0 / latency  // lower latency → higher score
}

func (s *LatencyStrategy) IsEligible(summary *ProviderSummary, cfg AdaptiveRoutingConfig) bool {
    return summary.Healthy && summary.ErrorRate < cfg.ErrorRateThreshold
}
```

### 6.2 Cost Strategy

```go
// plugins/adaptiverouting/strategies/cost.go

type CostStrategy struct{ catalog modelcatalog.ModelCatalog }

func (s *CostStrategy) Score(summary *ProviderSummary) float64 {
    pricing := s.catalog.GetPricing(summary.Provider, summary.Model)
    if pricing == nil { return 0.5 }
    // Score: inverse of input token price (lower cost → higher score)
    return 1.0 / pricing.InputPricePerMToken
}
```

---

## 7. Metrics API

```go
// transports/bifrost-http/handlers/routing.go — new endpoint

// GET /api/routing/metrics?provider=&model=&window=5m
// Returns live ProviderSummary for each tracked (provider, model) pair
// Used by the UI dashboard to show routing decisions in real time

// GET /api/routing/decisions?limit=100
// Returns recent routing decisions with reason and score

// GET /api/routing/config
// PUT /api/routing/config  (admin+)
```

---

## 8. Metrics Persistence

Live metrics are in-memory (ring buffers). For multi-node deployments (TECH-008), metrics are aggregated via Redis pub/sub:

```go
// On each PostHook observation:
// 1. Write to local ring buffer (immediate)
// 2. Publish to Redis channel "bifrost:routing:metrics:{node_id}" (async)

// Background goroutine subscribes to all node channels and merges into global view
```

---

## 9. Integration with Existing Governance

The adaptive routing plugin runs **after** governance in the pre-hook chain. Governance may have already short-circuited (budget/rate limit), so adaptive routing only runs on requests that reach it.

Routing decisions that override the provider/model **do not bypass** governance — the new provider/model combination is re-validated by the governance `BudgetResolver` (which checks the CEL routing rule result stored in context).

---

## 10. UI Components

```
ui/app/enterprise/routing/
├── page.tsx                    — Strategy config + live metrics heatmap
└── components/
    ├── ProviderHeatmap.tsx     — Latency/error rate grid by provider×model
    ├── StrategySelector.tsx    — Dropdown + parameter forms per strategy
    ├── DecisionLog.tsx         — Recent routing decisions with reason
    └── MetricsChart.tsx        — P50/P95 latency + error rate over time
```

---

## 11. Metrics Window Implementation

```go
// plugins/adaptiverouting/window.go
// Sliding window using ring buffer — O(1) insert, O(n) summarize

func (w *MetricWindow) compute(provider, model string, windowDur time.Duration) *ProviderSummary {
    cutoff := time.Now().Add(-windowDur)
    
    var latencies []float64
    var errors, total int
    var totalCost float64
    
    for i := 0; i < w.count; i++ {
        obs := w.observations[(w.head - i - 1 + w.capacity) % w.capacity]
        if obs.Timestamp.Before(cutoff) { continue }
        total++
        latencies = append(latencies, obs.LatencyMs)
        if !obs.Success { errors++ }
        totalCost += obs.CostUSD
    }
    
    sort.Float64s(latencies)
    return &ProviderSummary{
        Provider:     provider,
        Model:        model,
        P50LatencyMs: percentile(latencies, 50),
        P95LatencyMs: percentile(latencies, 95),
        ErrorRate:    float64(errors) / float64(max(total, 1)),
        AvgCostPer1M: totalCost,
        SampleCount:  total,
        Healthy:      float64(errors)/float64(max(total,1)) < 0.2,
    }
}
```
