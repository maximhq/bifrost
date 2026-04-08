# Test Cases — Governance (Virtual Keys, Budgets, Rate Limits)

**Suite ID:** TC-003  
**SRS Reference:** §3.4 (GOV-01 → GOV-07)  
**TR Reference:** TR-F-003  
**Priority:** P0  
**Type:** Integration + API

---

## Preconditions
- Bifrost running (community or enterprise license — governance is OSS)
- `admin_token` available for management operations
- Mock LLM server responding to completions

---

### TC-003-001 — Create Virtual Key with Budget Limit

**Priority:** P0 | **Type:** API

**Steps:**
1. `POST /api/governance/virtual-keys` with `admin_token`
2. Body:
```json
{
  "name": "test-vk-budget",
  "budget": {"max_budget": 5.00, "currency": "USD", "reset_duration": "monthly"},
  "allowed_models": ["gpt-4o", "gpt-4o-mini"]
}
```

**Expected Result:**
- HTTP 201
- Response includes `key` (bearer token value, shown once)
- `budget.max_budget` = 5.00

**Status:** READY

---

### TC-003-002 — Budget Accumulates Correctly Across Requests

**Priority:** P0 | **Type:** Integration

**Steps:**
1. Create VK with budget $1.00
2. Send 10 requests, each costing ~$0.09 (based on token count from mock)
3. `GET /api/governance/virtual-keys/{id}/usage`

**Expected Result:**
- `usage.total_cost` ≈ $0.90 (within ±5% of expected)
- `usage.requests_count` = 10

**Status:** READY

---

### TC-003-003 — Budget Exhaustion Blocks Requests with 429

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-003.2

**Steps:**
1. VK with budget = $0.001 (very small — will exhaust quickly)
2. Send requests until budget exhausted
3. Send one more request

**Expected Result:**
- After exhaustion: HTTP 429
- Body: `{"error":{"code":"budget_exceeded","remaining":0,"limit":0.001}}`
- `Retry-After` header present (seconds until reset)

**Status:** READY

---

### TC-003-004 — Rate Limit — Requests per Minute

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-003.3

**Steps:**
1. Create VK with `rate_limit.requests_per_minute: 2`
2. Send 3 requests in quick succession (< 60s)

**Expected Result:**
- Request 1: HTTP 200
- Request 2: HTTP 200
- Request 3: HTTP 429 with `Retry-After` header

**Status:** READY

---

### TC-003-005 — Rate Limit Resets After Window

**Priority:** P0 | **Type:** Integration

**Steps:**
1. VK with 2 req/min rate limit
2. Exhaust limit (send 2 requests)
3. Wait 61 seconds
4. Send 2 more requests

**Expected Result:**
- After window reset: both requests succeed (HTTP 200)

**Status:** READY

---

### TC-003-006 — Model Restriction Enforced

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-003.1

**Preconditions:** VK with `allowed_models: ["gpt-4o-mini"]`

**Steps:**
1. `POST /v1/chat/completions` with `"model": "gpt-4o"` (not allowed)

**Expected Result:**
- HTTP 403
- Error: model not allowed for this virtual key

**Status:** READY

---

### TC-003-007 — CEL Routing Rule — Route by Model Prefix

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-003.4

**Preconditions:**
- Provider "openai" and "anthropic" both registered
- Routing rule: `model.startsWith("claude") → provider: "anthropic"`

**Steps:**
1. `POST /v1/chat/completions` with `"model": "claude-3-opus"`

**Expected Result:**
- Request routed to Anthropic provider
- Log shows `provider: anthropic`

**Status:** READY

---

### TC-003-008 — CEL Routing Rule — Route by VK Tag

**Priority:** P0 | **Type:** Integration

**Preconditions:**
- VK has metadata tag `"env": "production"`
- Rule: `vk.tags["env"] == "production" → provider: "openai-prod"`

**Steps:**
1. Send request with production VK

**Expected Result:**
- Routed to "openai-prod" provider

**Status:** READY

---

### TC-003-009 — Virtual Key Expiry

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-003.6

**Steps:**
1. Create VK with `expires_at: (now - 1 hour)` (already expired)
2. Send inference request with this VK

**Expected Result:**
- HTTP 401
- Error: key is expired

**Status:** READY

---

### TC-003-010 — Budget Reset on Schedule

**Priority:** P1 | **Type:** Integration  
**TR Reference:** TR-F-003.5

**Preconditions:** VK with `reset_duration: "hourly"` and budget $0.01.

**Steps:**
1. Exhaust budget
2. Trigger budget reset (manually via test helper or wait for scheduled reset)
3. Send request after reset

**Expected Result:**
- After reset: HTTP 200 (budget restored to $0.01)
- `usage.total_cost` = 0 after reset

**Status:** READY

---

### TC-003-011 — Customer-Level Budget (Hierarchical)

**Priority:** P1 | **Type:** Integration

**Preconditions:** Customer "Enterprise Inc" with team budget $10.00. Two VKs under this customer.

**Steps:**
1. Exhaust $10.00 across both VKs combined
2. Send request with either VK

**Expected Result:**
- Customer-level budget triggers 429 even if individual VK budgets are not exhausted

**Status:** READY

---

### TC-003-012 — Budget Usage Returned via GET

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-003.7

**Steps:**
1. Send 5 requests with known token counts via VK
2. `GET /api/governance/virtual-keys/{id}/usage`

**Expected Result:**
- HTTP 200
- `total_requests`, `total_input_tokens`, `total_output_tokens`, `total_cost_usd` all non-zero and accurate

**Status:** READY

---

### TC-003-013 — Weighted Key Selection

**Priority:** P1 | **Type:** Integration

**Preconditions:** Provider "openai" has 2 API keys: key-A (weight=90), key-B (weight=10).

**Steps:**
1. Send 100 requests
2. Count how many go to each key (via log)

**Expected Result:**
- key-A selected ~90 times (±10%)
- key-B selected ~10 times (±10%)

**Status:** READY

---

### TC-003-014 — Virtual Key CRUD Lifecycle

**Priority:** P0 | **Type:** API

**Steps:**
1. `POST /api/governance/virtual-keys` → 201, get VK ID
2. `GET /api/governance/virtual-keys/{id}` → 200 with correct data
3. `PUT /api/governance/virtual-keys/{id}` with updated budget → 200
4. `DELETE /api/governance/virtual-keys/{id}` → 204
5. `GET /api/governance/virtual-keys/{id}` → 404

**Expected Result:**
- All steps succeed with correct HTTP codes

**Status:** READY

---

### TC-003-015 — Tokens Per Minute Rate Limit

**Priority:** P0 | **Type:** Integration

**Preconditions:** VK with `rate_limit.tokens_per_minute: 100`

**Steps:**
1. Send request consuming 80 tokens (succeeds)
2. Immediately send request consuming 30 tokens (would push to 110 > 100)

**Expected Result:**
- Second request: HTTP 429 with token-rate-limit error

**Status:** READY

---

### TC-003-016 — Governance Real-time Usage Histogram

**Priority:** P1 | **Type:** API

**Steps:**
1. Send 20 requests over 2 minutes
2. `GET /api/logs/usage-histogram?vk_id={id}&interval=1m`

**Expected Result:**
- HTTP 200
- Array of time-bucketed usage data (2 buckets: last minute + previous minute)
- Each bucket has `requests`, `tokens`, `cost`

**Status:** READY

---

### TC-003-017 — Disabled Virtual Key Rejected

**Priority:** P0 | **Type:** API

**Steps:**
1. Create VK, then `PUT /api/governance/virtual-keys/{id}` with `"enabled": false`
2. Send inference request with that VK

**Expected Result:**
- HTTP 401 or 403
- Error: virtual key is disabled

**Status:** READY

---

### TC-003-018 — Multiple Rate Limits Combined (Req + Token)

**Priority:** P1 | **Type:** Integration

**Preconditions:** VK with `requests_per_minute: 10` AND `tokens_per_minute: 50`

**Steps:**
1. Send 5 requests × 15 tokens = 75 tokens consumed
2. Next request (6th, would hit token limit first at 75+15=90>50)

**Expected Result:**
- Blocked by token rate limit before hitting request count limit

**Status:** READY

---

### TC-003-019 — Routing Rule Invalid CEL Expression Rejected

**Priority:** P0 | **Type:** API

**Steps:**
1. `POST /api/governance/routing-rules` with `"cel_expression": "invalid === syntax !!"`

**Expected Result:**
- HTTP 400
- Error: invalid CEL expression with syntax error details

**Status:** READY

---

### TC-003-020 — Empty Budget (null) = Unlimited

**Priority:** P0 | **Type:** Integration

**Steps:**
1. Create VK with `"budget": null`
2. Send 1,000 requests

**Expected Result:**
- All 1,000 requests succeed (no budget enforcement)
- No 429 responses

**Status:** READY
