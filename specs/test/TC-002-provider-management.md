# Test Cases — Provider Management

**Suite ID:** TC-002  
**SRS Reference:** §3.2, §3.3  
**TR Reference:** TR-F-002  
**Priority:** P0  
**Type:** Integration + API

---

### TC-002-001 — Create Provider (Happy Path)

**Priority:** P0 | **Type:** API

**Steps:**
1. `POST /api/providers` with `admin_token`
2. Body: `{"name":"test-openai","type":"openai","api_keys":[{"key":"sk-test123","weight":100}],"base_url":"http://localhost:9090"}`

**Expected Result:**
- HTTP 201; `id` in response
- `GET /api/providers/{id}` returns provider; key value masked (`sk-***`)

**Status:** READY

---

### TC-002-002 — API Key Stored Encrypted (Not Plaintext)

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-002.4

**Steps:**
1. Create provider with API key `sk-test123`
2. Query DB directly: `SELECT value FROM provider_keys WHERE ...`

**Expected Result:**
- DB value is NOT `sk-test123` (encrypted/hashed)
- Decrypts to `sk-test123` when Bifrost loads it

**Status:** READY

---

### TC-002-003 — API Key Masked in GET Response

**Priority:** P0 | **Type:** API

**Steps:**
1. `GET /api/providers/{id}` after creating with real key

**Expected Result:**
- `api_keys[0].key` = `"sk-***"` or `"..."` (masked, not plaintext)

**Status:** READY

---

### TC-002-004 — Multiple Keys with Weighted Distribution

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-002.2

**Steps:**
1. Create provider with 2 keys: `key-A` (weight=80), `key-B` (weight=20)
2. Send 100 requests through provider
3. Check per-key usage logs

**Expected Result:**
- `key-A` used ≈ 80 times (±10%)
- `key-B` used ≈ 20 times (±10%)

**Status:** READY

---

### TC-002-005 — Provider CRUD Lifecycle

**Priority:** P0 | **Type:** API

**Steps:**
1. POST → 201
2. GET → 200
3. PUT (update `base_url`) → 200
4. DELETE → 204
5. GET → 404

**Status:** READY

---

### TC-002-006 — Provider Health Check

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-002.5

**Steps:**
1. `GET /api/providers/{id}/health`

**Expected Result:**
- HTTP 200
- `{"status":"healthy","latency_ms":12}` (mock server healthy)

**Status:** READY

---

### TC-002-007 — Provider Health Check — Unhealthy Provider

**Priority:** P0 | **Type:** Integration

**Preconditions:** Mock server shut down.

**Steps:**
1. `GET /api/providers/{id}/health`

**Expected Result:**
- `{"status":"unhealthy","error":"connection refused"}`

**Status:** READY

---

### TC-002-008 — Duplicate Provider Name Rejected

**Priority:** P0 | **Type:** API

**Steps:**
1. Create provider with name "openai"
2. Create another provider with name "openai"

**Expected Result:**
- Second create: HTTP 409 Conflict

**Status:** READY

---

### TC-002-009 — Provider Deletion Blocked When Active VKs Reference It

**Priority:** P1 | **Type:** API

**Steps:**
1. Create provider and assign to VK as sole provider
2. `DELETE /api/providers/{id}`

**Expected Result:**
- HTTP 409
- Error: "provider is referenced by active virtual keys"

**Status:** READY

---

### TC-002-010 — List Providers Returns All Registered

**Priority:** P0 | **Type:** API

**Steps:**
1. Create 3 providers
2. `GET /api/providers`

**Expected Result:**
- HTTP 200
- Response array contains all 3 providers (+ any pre-existing)

**Status:** READY

---

### TC-002-011 — List Available Models for Provider

**Priority:** P0 | **Type:** API

**Steps:**
1. `GET /api/providers/{id}/models`

**Expected Result:**
- HTTP 200
- Array of model names supported by this provider

**Status:** READY

---

### TC-002-012 — Invalid Provider Type Rejected

**Priority:** P0 | **Type:** API

**Steps:**
1. `POST /api/providers` with `"type": "unknown_provider"`

**Expected Result:**
- HTTP 400
- Error: invalid provider type

**Status:** READY

---

### TC-002-013 — Fallback Chain — Primary Fails, Secondary Tries

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-002.3

**Steps:**
1. VK configured: `providers: [ {name:"primary"}, {name:"secondary"} ]`
2. `primary` responds 500
3. Send inference request

**Expected Result:**
- Final response: HTTP 200 from secondary provider
- `x-bifrost-retries` or log shows retry count

**Status:** READY

---

### TC-002-014 — Fallback Chain Exhausted Returns 502

**Priority:** P0 | **Type:** Integration

**Steps:**
1. Both providers in fallback chain respond 500

**Expected Result:**
- HTTP 502 Bad Gateway
- Error includes both provider failure messages

**Status:** READY

---

### TC-002-015 — Provider Key Rotation (Add/Remove Keys)

**Priority:** P1 | **Type:** API

**Steps:**
1. Provider has 1 key (key-A)
2. `POST /api/providers/{id}/keys` add key-B
3. `DELETE /api/providers/{id}/keys/{key_a_id}` remove key-A
4. Send 10 requests

**Expected Result:**
- All 10 requests use key-B (key-A removed)

**Status:** READY

---

### TC-002-016 — Provider Created with Vault Reference Key

**Priority:** P1 | **Type:** Integration  
**TR Reference:** TECH-008 Vault

**Preconditions:** Vault running with secret at `secret/data/bifrost/openai_key`

**Steps:**
1. Create provider with `"api_keys":[{"key":"vault://secret/data/bifrost/openai_key","weight":100}]`

**Expected Result:**
- Bifrost resolves the Vault path at runtime
- Actual requests use the resolved secret key
- GET response still masks key value

**Status:** READY

---

### TC-002-017 — Provider Import via Config File

**Priority:** P1 | **Type:** Integration

**Steps:**
1. Add provider block to `config.json` with valid API key
2. Restart Bifrost

**Expected Result:**
- Provider auto-loaded from config
- `GET /api/providers` includes file-configured provider

**Status:** READY

---

### TC-002-018 — Provider Network Config: Custom Timeout

**Priority:** P1 | **Type:** Integration

**Steps:**
1. Create provider with `"network":{"request_timeout_ms":100}`
2. Mock LLM configured with 500ms delay
3. Send request

**Expected Result:**
- Request times out after ~100ms
- HTTP 504 Gateway Timeout returned to caller

**Status:** READY
