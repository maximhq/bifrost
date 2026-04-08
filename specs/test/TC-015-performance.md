# Test Cases — Performance & Scale

**Suite ID:** TC-015  
**SRS Reference:** §4 Non-Functional Requirements  
**TR Reference:** TR-NF-001, TR-NF-003, TR-NF-004  
**Priority:** P1  
**Type:** Performance (k6) + Load  
**Tool:** k6 + Grafana + pprof

---

## Environment Setup
- Bifrost running with 4 CPU cores, 8GB RAM
- PostgreSQL 15 on separate host
- Redis 7 on separate host
- Mock LLM server: 10ms simulated latency
- k6 v0.50+

---

## Baseline Thresholds

| Metric | P50 | P95 | P99 |
|--------|-----|-----|-----|
| Inference latency (gateway overhead only) | ≤ 5ms | ≤ 11ms | ≤ 50ms |
| Management API latency | ≤ 50ms | ≤ 200ms | ≤ 500ms |
| RBAC middleware overhead | ≤ 1ms | ≤ 3ms | ≤ 5ms |
| Guardrails (keyword/regex) overhead | ≤ 2ms | ≤ 5ms | ≤ 10ms |

---

### TC-015-001 — Steady State 1,000 RPS (Baseline)

**Priority:** P1 | **Type:** Performance

**k6 Script:**
```javascript
// scenarios/baseline.js
export const options = {
  scenarios: {
    constant_load: {
      executor: 'constant-arrival-rate',
      rate: 1000,
      timeUnit: '1s',
      duration: '5m',
      preAllocatedVUs: 200,
    }
  },
  thresholds: {
    http_req_duration: ['p(99)<50'],  // 50ms P99 gateway overhead
    http_req_failed: ['rate<0.001'],   // <0.1% error rate
  }
};
```

**Expected Result:**
- P99 ≤ 50ms (gateway overhead, mock LLM excluded)
- Error rate ≤ 0.1%
- No goroutine leak (pprof check after)
- Memory stable (no growth over 5 minutes)

**Status:** READY

---

### TC-015-002 — Ramp to 5,000 RPS

**Priority:** P1 | **Type:** Performance  
**TR Reference:** TR-NF-001.1

**k6 Script:**
```javascript
// scenarios/ramp.js
export const options = {
  stages: [
    { duration: '2m', target: 1000 },
    { duration: '3m', target: 5000 },
    { duration: '5m', target: 5000 },  // sustain
    { duration: '2m', target: 0 },
  ],
  thresholds: {
    http_req_duration: ['p(99)<50'],
    http_req_failed: ['rate<0.01'],
  }
};
```

**Expected Result:**
- System handles 5,000 RPS with ≤ P99 50ms overhead
- No OOM kill
- CPU usage < 80% at peak (headroom for spikes)

**Status:** READY

---

### TC-015-003 — 1,000 Concurrent Streaming Connections

**Priority:** P1 | **Type:** Performance  
**TR Reference:** TR-NF-001.3

**Steps:**
1. 1,000 goroutines each maintain a streaming SSE connection
2. Each stream receives 50 chunks, then completes
3. New streams replace completed ones (steady state)

**Expected Result:**
- All 1,000 streams active simultaneously without degradation
- Time to first chunk (TTFC) ≤ 50ms per stream
- No "connection closed" errors
- Memory per connection ≤ 10KB

**Status:** READY

---

### TC-015-004 — Provider Failover Latency

**Priority:** P1 | **Type:** Performance  
**TR Reference:** TR-F-002.3 + TR-NF-001.4

**Steps:**
1. Primary mock LLM configured to fail (return 503)
2. Secondary mock LLM healthy
3. Send 100 requests through primary
4. Measure time between first retry and successful response from secondary

**Expected Result:**
- Failover detected within 100ms
- Total additional latency from failover ≤ 200ms
- No request returns 500 (all failing over to secondary)

**Status:** READY

---

### TC-015-005 — RBAC Middleware Overhead

**Priority:** P1 | **Type:** Performance

**Steps:**
1. Baseline: 1,000 RPS to management endpoint with RBAC disabled
2. Test: 1,000 RPS to same endpoint with RBAC enabled (super_admin token)
3. Compare P99 latency

**Expected Result:**
- RBAC overhead ≤ 5ms P99
- No additional error rate

**Status:** READY

---

### TC-015-006 — Guardrails Keyword Check Overhead

**Priority:** P1 | **Type:** Performance

**Steps:**
1. Baseline: 1,000 RPS with no guardrails plugin
2. Test: 1,000 RPS with 10 active keyword policies (100 keywords total)
3. Test: 1,000 RPS with 10 active regex policies (5 patterns each)

**Expected Result:**
- Keyword check overhead: ≤ 2ms P99
- Regex check overhead: ≤ 5ms P99

**Status:** READY

---

### TC-015-007 — PII Detection Overhead

**Priority:** P1 | **Type:** Performance

**Steps:**
1. Baseline: 1,000 RPS without PII plugin
2. Test: 1,000 RPS with PII plugin (all 10 entity types enabled)
3. Average message length: 500 tokens (~2KB)

**Expected Result:**
- PII detection overhead: ≤ 5ms P99
- No OOM (entity detection is purely in-process)

**Status:** READY

---

### TC-015-008 — Memory Stability Under Load (1 Hour)

**Priority:** P1 | **Type:** Performance  
**TR Reference:** TR-NF-004.1

**Steps:**
1. Run 1,000 RPS steady load for 60 minutes
2. Sample heap size every 5 minutes via pprof

**Expected Result:**
- Heap growth < 50MB over 60 minutes (no memory leak)
- GC pauses < 10ms per cycle
- `sync.Pool` hit rate > 90% (object reuse working)

**Status:** READY

---

### TC-015-009 — Spike Load (10x Sudden Burst)

**Priority:** P1 | **Type:** Performance

**Steps:**
1. Steady state: 500 RPS for 2 minutes
2. Instant spike: 5,000 RPS for 30 seconds
3. Return: 500 RPS for 2 minutes

**Expected Result:**
- During spike: error rate < 5% (queuing expected, not OOM)
- After spike: system recovers within 30 seconds
- No goroutine leak post-spike

**Status:** READY

---

### TC-015-010 — Large Payload Streaming (100MB Audio)

**Priority:** P1 | **Type:** Performance  
**TR Reference:** TR-F-001.7 (Transcription)

**Steps:**
1. Upload 100MB WAV file to `/v1/audio/transcriptions`
2. Measure: upload throughput, memory usage during upload, time to response

**Expected Result:**
- Memory peak during upload: ≤ 15MB (streaming path, no full buffering)
- Upload throughput: ≥ 50MB/s (local loopback)
- No OOM kill
- HTTP 200 with transcription result

**Status:** READY
