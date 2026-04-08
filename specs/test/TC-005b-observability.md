# Test Cases — Observability (Logs, Metrics, Tracing)

**Suite ID:** TC-005b — Provider Management & Observability  
**SRS Reference:** §3.5 (Observability), §3.6 (Semantic Caching)  
**Priority:** P1  
**Type:** Integration + API

---

> This supplement covers OSS observability features (logging, metrics, tracing, semantic cache) not part of enterprise but needed for full test coverage.

---

## Logging & Metrics

### TC-OBS-001 — Request Log Created After Inference

**Priority:** P0 | **Type:** Integration

**Steps:**
1. Send `POST /v1/chat/completions`
2. `GET /api/logs?limit=1`

**Expected Result:**
- Log entry exists with:
  - `request_id` (UUID)
  - `model`, `provider`, `input_tokens`, `output_tokens`
  - `latency_ms` > 0
  - `status` = "success"

**Status:** READY

---

### TC-OBS-002 — Failed Request Logged with Error

**Priority:** P0 | **Type:** Integration

**Preconditions:** Provider returns 500.

**Steps:**
1. Send inference request
2. `GET /api/logs?status=error`

**Expected Result:**
- Log entry with `status=error`, `error_message` from provider

**Status:** READY

---

### TC-OBS-003 — Prometheus Metrics Endpoint

**Priority:** P1 | **Type:** API

**Steps:**
1. `GET /metrics`

**Expected Result:**
- HTTP 200
- Content-Type: `text/plain; version=0.0.4`
- Includes metrics:
  - `bifrost_requests_total{provider,model,status}`
  - `bifrost_request_duration_seconds{quantile}`
  - `bifrost_tokens_total{type,provider,model}`

**Status:** READY

---

### TC-OBS-004 — Semantic Cache Hit Returns Cached Response

**Priority:** P1 | **Type:** Integration

**Preconditions:** Semantic cache enabled, similarity threshold = 0.95.

**Steps:**
1. Send: "What is the capital of France?"
2. Immediately send: "What is France's capital city?"  (semantically identical)

**Expected Result:**
- Second request: `X-Cache: HIT` response header
- No LLM call made for second request (mock LLM receives only 1 request total)
- Response identical to first

**Status:** READY

---

### TC-OBS-005 — Semantic Cache Miss on Dissimilar Query

**Priority:** P1 | **Type:** Integration

**Steps:**
1. Send: "What is the capital of France?" (cached)
2. Send: "What is the population of Germany?" (dissimilar)

**Expected Result:**
- Second request: `X-Cache: MISS` header
- LLM called for second request (2 total LLM calls)

**Status:** READY

---

### TC-OBS-006 — Log Query with Filters

**Priority:** P0 | **Type:** API

**Steps:**
1. `GET /api/logs?model=gpt-4o&provider=openai&limit=20`

**Expected Result:**
- HTTP 200
- All returned entries match filter criteria
- `model=gpt-4o`, `provider=openai`

**Status:** READY

---

### TC-OBS-007 — Usage Statistics Endpoint

**Priority:** P0 | **Type:** API

**Steps:**
1. Send 10 requests over 2 minutes
2. `GET /api/logs/stats?interval=5m`

**Expected Result:**
- HTTP 200
- `total_requests: 10`
- `total_input_tokens` and `total_output_tokens` > 0
- `average_latency_ms` reasonable

**Status:** READY

---

### TC-OBS-008 — OpenTelemetry Trace Exported

**Priority:** P1 | **Type:** Integration

**Preconditions:** OTEL plugin configured, Jaeger instance running at localhost:4317.

**Steps:**
1. Send inference request
2. Query Jaeger for trace

**Expected Result:**
- Trace appears in Jaeger within 5 seconds
- Spans: `bifrost.inference`, `provider.request`, `plugin.pre_hook`, `plugin.post_hook`
- Trace includes `model`, `provider`, `status` as span attributes

**Status:** READY

---

## Large Payload Optimization (TC-010b)

### TC-010b-001 — 200MB Audio Upload Does Not OOM

**Priority:** P1 | **Type:** Integration  
**TR Reference:** TR-F-001.7

**Preconditions:** `max_body_size: 250MB` in NetworkConfig.

**Steps:**
1. Upload 200MB WAV file via multipart `POST /v1/audio/transcriptions`
2. Monitor memory during upload (pprof)

**Expected Result:**
- Memory peak ≤ 50MB during upload (streaming reads, not full buffering)
- Transcription response received
- No OOM kill

**Status:** READY

---

### TC-010b-002 — Request Body Exceeding MaxBodySize Rejected

**Priority:** P0 | **Type:** API

**Preconditions:** `max_body_size: 10MB`

**Steps:**
1. Upload 15MB file

**Expected Result:**
- HTTP 413 Request Entity Too Large
- Error: "request body exceeds maximum allowed size"

**Status:** READY
