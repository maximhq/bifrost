# Test Cases — Inference API

**Suite ID:** TC-001  
**SRS Reference:** §3.1 (INF-01 → INF-07)  
**TR Reference:** TR-F-001  
**Priority:** P0  
**Type:** Integration + API  

---

## Preconditions for All Tests
- Bifrost running with mock LLM server (`X-Mock-*` headers supported)
- Provider "mock-openai" registered with `base_url: http://localhost:9090`
- Valid `api_user_token` session available (no RBAC needed for inference)

---

### TC-001-001 — Chat Completion Non-Streaming (Happy Path)

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-001.1

**Steps:**
1. `POST /v1/chat/completions` with `Authorization: Bearer {api_user_token}`
2. Body: `{"model":"gpt-4o","messages":[{"role":"user","content":"Hello"}],"stream":false}`
3. Verify response

**Expected Result:**
- HTTP 200
- Body matches OpenAI chat completion schema
- `model` field = "gpt-4o"
- `choices[0].message.role` = "assistant"
- `choices[0].message.content` is non-empty string
- `usage.total_tokens` > 0

**Status:** READY

---

### TC-001-002 — Chat Completion Streaming (SSE)

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-001.2

**Steps:**
1. `POST /v1/chat/completions` with `"stream": true`
2. Read SSE stream line by line

**Expected Result:**
- Content-Type: `text/event-stream`
- Each line prefixed `data: `
- Last line = `data: [DONE]`
- Chunks: `choices[0].delta.content` progressively builds response
- No full response buffered before first chunk arrives

**Status:** READY

---

### TC-001-003 — Malformed Request Returns 400

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-001.4

**Steps:**
1. `POST /v1/chat/completions` with body `{"model":"gpt-4o"}` (missing `messages`)

**Expected Result:**
- HTTP 400
- Body: `{"error":{"type":"invalid_request_error","message":"...messages..."}}`

**Status:** READY

---

### TC-001-004 — Unsupported Operation Returns 501

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-001.5

**Steps:**
1. `POST /v1/images/generations` targeting provider that does not support image generation
2. Provider `mock-text-only` supports only chat completions

**Expected Result:**
- HTTP 501
- Body contains provider name and "not supported"

**Status:** READY

---

### TC-001-005 — Embedding Request

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-001.7

**Steps:**
1. `POST /v1/embeddings` with `{"model":"text-embedding-ada-002","input":"Hello world"}`

**Expected Result:**
- HTTP 200
- `data[0].embedding` is array of floats
- `data[0].object` = "embedding"

**Status:** READY

---

### TC-001-006 — Provider Failover on Error

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-002.3

**Preconditions:** Provider "primary" configured with `X-Mock-Error: 500`; Provider "secondary" configured as fallback.

**Steps:**
1. Send chat completion request to primary provider
2. Primary returns 500 once
3. Bifrost should automatically retry on secondary

**Expected Result:**
- Final response is HTTP 200 from secondary provider
- Response includes `x-bifrost-fallback: true` header (or equivalent)
- Total latency < 2x normal (failover within 500ms)

**Status:** READY

---

### TC-001-007 — Streaming with Injected Latency

**Priority:** P1 | **Type:** Integration  
**TR Reference:** TR-NF-001.3

**Preconditions:** Mock LLM server configured with `X-Mock-Delay: 1000` (1s per chunk)

**Steps:**
1. Send streaming chat completion
2. Measure time to first chunk (`TTFC`)
3. Verify chunks arrive progressively (not all at once)

**Expected Result:**
- `TTFC` < 200ms (gateway overhead only — excludes provider latency)
- Chunks arrive at ~1s intervals (not batched)
- Stream completes with `[DONE]`

**Status:** READY

---

### TC-001-008 — Concurrent Requests (1,000 Simultaneous)

**Priority:** P1 | **Type:** Performance  
**TR Reference:** TR-NF-001.3

**Steps:**
1. Launch 1,000 concurrent goroutines each sending chat completion
2. All use same `api_user_token`
3. Measure: success rate, P99 latency

**Expected Result:**
- ≥ 99.5% success rate
- P99 latency ≤ 200ms (gateway overhead)
- No goroutine leak (check pprof after)

**Status:** READY

---

### TC-001-009 — Authentication Required (No Token)

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-003.1

**Steps:**
1. `POST /v1/chat/completions` with no Authorization header

**Expected Result:**
- HTTP 401
- Body: `{"error":{"type":"authentication_error","message":"..."}}`

**Status:** READY

---

### TC-001-010 — Invalid Virtual Key Returns 401

**Priority:** P0 | **Type:** API

**Steps:**
1. `POST /v1/chat/completions` with `Authorization: Bearer nonexistent_vk`

**Expected Result:**
- HTTP 401
- Error type: `authentication_error`

**Status:** READY

---

### TC-001-011 — Budget Exhausted Returns 429

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-003.2

**Preconditions:** VK `vk_tight_budget` has budget of $0.01; previous requests have exhausted it.

**Steps:**
1. `POST /v1/chat/completions` with `vk_tight_budget`

**Expected Result:**
- HTTP 429
- Body includes `"code":"budget_exceeded"`
- `Retry-After` header present

**Status:** READY

---

### TC-001-012 — Rate Limited Request Returns 429

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-003.3

**Preconditions:** VK `vk_rate_limited` = 1 request/minute. Send first request to consume limit.

**Steps:**
1. Send first request (succeeds)
2. Immediately send second request

**Expected Result:**
- Second request: HTTP 429
- `Retry-After` header indicates seconds until limit resets

**Status:** READY

---

### TC-001-013 — Model Restriction Enforced

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-003.1

**Preconditions:** VK `vk_model_restricted` allows only `gpt-4o-mini`.

**Steps:**
1. `POST /v1/chat/completions` with `"model":"gpt-4o"` (not allowed)

**Expected Result:**
- HTTP 403
- Error indicates model not allowed for this key

**Status:** READY

---

### TC-001-014 — Expired Virtual Key Rejected

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-003.6

**Steps:**
1. `POST /v1/chat/completions` with `vk_expired` token

**Expected Result:**
- HTTP 401
- Error: key is expired

**Status:** READY

---

### TC-001-015 — Transcription (Audio Upload)

**Priority:** P1 | **Type:** API  
**TR Reference:** TR-F-001.7

**Steps:**
1. `POST /v1/audio/transcriptions` multipart with `file` field (WAV fixture, 10MB)
2. `model: whisper-1`

**Expected Result:**
- HTTP 200
- `{"text": "...transcription..."}` returned
- No OOM or crash on 10MB payload

**Status:** READY

---

### TC-001-016 — OpenAI Responses API (Stateful)

**Priority:** P1 | **Type:** API

**Steps:**
1. `POST /v1/responses` with `{"model":"gpt-4o","input":"Hello"}`
2. Capture `response.id`
3. `POST /v1/responses` with `{"previous_response_id": "{id}"}`

**Expected Result:**
- Both responses HTTP 200
- Second response references conversation context

**Status:** READY

---

### TC-001-017 — MCP Tool Injection on tool_choice

**Priority:** P1 | **Type:** Integration

**Steps:**
1. MCP client "web-search" registered with tool `search_web`
2. `POST /v1/chat/completions` with `"tool_choice":"auto"` and no explicit tools array

**Expected Result:**
- Response includes `choices[0].message.tool_calls[0].function.name` = "search_web"
- Or model responds normally if no tool needed

**Status:** READY

---

### TC-001-018 — X-Bifrost-Provider Header Override

**Priority:** P2 | **Type:** API

**Steps:**
1. Two providers registered: "openai" and "anthropic"
2. Send request with `X-Bifrost-Provider: anthropic` header

**Expected Result:**
- Request routed to Anthropic (response structure matches Anthropic output format converted to OpenAI)
- Log shows `provider: anthropic`

**Status:** READY

---

### TC-001-019 — Image Generation

**Priority:** P1 | **Type:** API

**Steps:**
1. `POST /v1/images/generations` with mock DALL-E provider
2. `{"model":"dall-e-3","prompt":"A blue cat","size":"1024x1024"}`

**Expected Result:**
- HTTP 200
- `data[0].url` or `data[0].b64_json` present
- Images array length = `n` (default 1)

**Status:** READY

---

### TC-001-020 — Batch Request (File Upload + Poll)

**Priority:** P2 | **Type:** API

**Steps:**
1. Upload JSONL file via `POST /v1/files`
2. Create batch via `POST /v1/batches` referencing file ID
3. Poll `GET /v1/batches/{id}` until status = "completed"
4. Download results via `GET /v1/files/{output_file_id}/content`

**Expected Result:**
- All 4 steps succeed
- Results file contains one result per input line

**Status:** READY

---

### TC-001-021 — Response with Empty Content (Edge Case)

**Priority:** P1 | **Type:** API

**Steps:**
1. Mock LLM configured to return `choices[0].message.content = ""`
2. Send chat completion request

**Expected Result:**
- HTTP 200 (not 500)
- `choices[0].message.content` = "" in response

**Status:** READY

---

### TC-001-022 — Very Long Context Window

**Priority:** P1 | **Type:** API

**Steps:**
1. Send chat with 100,000 token context (large messages array)
2. Mock LLM configured to accept any context length

**Expected Result:**
- HTTP 200
- No timeout (within 60s)
- No OOM error

**Status:** READY

---

### TC-001-023 — Provider Returns Non-JSON Error

**Priority:** P1 | **Type:** Integration

**Preconditions:** Mock LLM returns `500 Internal Server Error` with HTML body.

**Steps:**
1. Send chat completion

**Expected Result:**
- Bifrost returns HTTP 502
- Body: `{"error":{"type":"provider_error","message":"provider returned non-JSON error"}}`

**Status:** READY

---

### TC-001-024 — SDK Compatibility (LangChain Format)

**Priority:** P1 | **Type:** API

**Steps:**
1. Send request matching LangChain OpenAI SDK format (extra `model_kwargs` field)
2. Bifrost should strip unknown fields and forward cleanly

**Expected Result:**
- HTTP 200
- No 422 Unprocessable Entity

**Status:** READY

---

### TC-001-025 — Health Check Endpoint

**Priority:** P0 | **Type:** API

**Steps:**
1. `GET /health`

**Expected Result:**
- HTTP 200
- Body: `{"status":"ok"}` or similar
- Response time < 10ms

**Status:** READY
