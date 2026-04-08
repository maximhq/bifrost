# Test Cases — Adaptive Routing Engine

**Suite ID:** TC-009  
**SRS Reference:** §3.17 (AROUTE-01 → AROUTE-05)  
**TR Reference:** TR-F-009  
**Priority:** P1  
**Type:** Integration + API  
**Dependency:** TC-013 (License: adaptive_routing feature)

---

## Preconditions
- Enterprise license with `adaptive_routing` feature
- 3 mock providers registered: `provider-fast` (5ms latency), `provider-slow` (200ms latency), `provider-error` (50% error rate)
- Minimal sliding window: 20 requests to build baseline metrics

---

### TC-009-001 — Latency-Optimized Strategy Routes to Fastest Provider

**Priority:** P1 | **Type:** Integration  
**TR Reference:** TR-F-009.1

**Preconditions:** Strategy=latency_optimized. Send 20 warm-up requests split across providers.

**Steps:**
1. Configure adaptive routing: `strategy: latency_optimized`
2. Send 50 requests
3. Check routing logs for provider selection distribution

**Expected Result:**
- `provider-fast` selected ≥ 80% of the time
- `provider-slow` selected ≤ 20%
- `provider-error` weight reduced due to error rate

**Status:** READY

---

### TC-009-002 — Error Rate Above Threshold Excludes Provider

**Priority:** P1 | **Type:** Integration  
**TR Reference:** TR-F-009.3

**Preconditions:** `min_healthy_threshold: error_rate < 0.3`. `provider-error` has error_rate ≈ 0.5.

**Steps:**
1. Send 30 warm-up requests (enough to establish error_rate metric)
2. Send 20 more test requests

**Expected Result:**
- `provider-error` NOT selected for test requests (excluded due to error rate)
- Routing falls back to healthy providers only

**Status:** READY

---

### TC-009-003 — Cost-Optimized Strategy Routes to Cheapest Provider

**Priority:** P1 | **Type:** Integration  
**TR Reference:** TR-F-009.2

**Preconditions:**
- `provider-fast`: $0.10 per 1M output tokens
- `provider-slow`: $0.02 per 1M output tokens
- Strategy: `cost_optimized`

**Steps:**
1. Send 50 requests with strategy=cost_optimized

**Expected Result:**
- `provider-slow` selected ≥ 70% (cheapest despite higher latency)
- `provider-fast` still gets some traffic (hedging)

**Status:** READY

---

### TC-009-004 — Fallback to Weighted Random When Sample Size Insufficient

**Priority:** P1 | **Type:** Integration  
**TR Reference:** TR-F-009.4

**Preconditions:** Clean state — no prior requests (sample_size = 0). Min sample = 20.

**Steps:**
1. Send 10 requests (below minimum sample threshold)

**Expected Result:**
- Requests distributed evenly across providers (weighted_random mode)
- No single provider dominates (no adaptive scoring yet)

**Status:** READY

---

### TC-009-005 — Metrics API Returns Provider Stats

**Priority:** P1 | **Type:** API  
**TR Reference:** TR-F-009.5

**Preconditions:** 50 warm-up requests sent.

**Steps:**
1. `GET /api/adaptive-routing/stats` with `admin_token`

**Expected Result:**
```json
{
  "providers": [
    {
      "name": "provider-fast",
      "latency_p50_ms": 5,
      "latency_p95_ms": 8,
      "error_rate": 0.0,
      "requests_sampled": 30
    },
    ...
  ],
  "window_seconds": 300
}
```

**Status:** READY

---

### TC-009-006 — Strategy Config Update Takes Effect Within One Window

**Priority:** P1 | **Type:** Integration

**Steps:**
1. Set strategy=latency_optimized, warm up 30 requests — note provider distribution
2. `PUT /api/adaptive-routing/config` — change to `cost_optimized`
3. Wait for current window to expire (if applicable) OR send 30 more requests

**Expected Result:**
- Distribution shifts toward cheaper provider after config change

**Status:** READY

---

### TC-009-007 — Balanced Strategy Uses Weighted Score

**Priority:** P1 | **Type:** Integration

**Preconditions:** Strategy=balanced with `latency_weight=0.4, cost_weight=0.4, error_weight=0.2`

**Steps:**
1. Send 50 requests
2. Check distribution

**Expected Result:**
- No single provider dominates (balanced weighting)
- `provider-error` gets less traffic than healthy providers

**Status:** READY

---

### TC-009-008 — Adaptive Routing Requires Enterprise License

**Priority:** P1 | **Type:** Integration

**Preconditions:** Community license.

**Steps:**
1. `PUT /api/adaptive-routing/config` → should return 402
2. `GET /api/adaptive-routing/stats` → should return 402

**Expected Result:**
- HTTP 402 for both endpoint requests

**Status:** READY

---

### TC-009-009 — Adaptive Routing Does Not Double-Count Retry Failures

**Priority:** P1 | **Type:** Integration

**Preconditions:** Provider-A fails → retried on Provider-B (success).

**Steps:**
1. Configure Provider-A to fail once
2. Send request — Bifrost retries on Provider-B
3. Check Provider-A error_rate metric

**Expected Result:**
- Provider-A error count increments by 1
- Provider-B does NOT get an error count (it succeeded)
- The retry itself is counted as success route

**Status:** READY

---

### TC-009-010 — Adaptive Metrics Reset on Window Expiry

**Priority:** P1 | **Type:** Integration

**Preconditions:** Sliding window = 60 seconds.

**Steps:**
1. Send 30 requests (establish metrics)
2. Wait 61 seconds
3. Check `GET /api/adaptive-routing/stats`

**Expected Result:**
- `requests_sampled` = 0 or very low (window expired)
- System falls back to weighted_random until next window fills

**Status:** READY

---

### TC-009-011 — Adaptive Routing + Governance Compatible

**Priority:** P1 | **Type:** Integration

**Preconditions:** VK has model restriction `gpt-4o-mini`. Adaptive routing selects provider based on latency.

**Steps:**
1. Send request with restricted VK
2. Adaptive routing picks `provider-fast`
3. `provider-fast` doesn't support `gpt-4o-mini`

**Expected Result:**
- Adaptive routing falls back to next best provider that supports the model
- OR returns model restriction error if no compatible provider is available

**Status:** READY

---

### TC-009-012 — Concurrent Metric Updates are Thread-Safe

**Priority:** P1 | **Type:** Integration

**Steps:**
1. 500 concurrent requests hitting 3 providers simultaneously
2. Metrics updated concurrently by all goroutines
3. After completion: `GET /api/adaptive-routing/stats`

**Expected Result:**
- No data races (run with `-race` flag)
- Total `requests_sampled` = ~500 (±5%)
- No negative latency or error_rate > 1.0

**Status:** READY
