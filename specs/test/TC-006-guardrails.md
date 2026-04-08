# Test Cases — Content Guardrails

**Suite ID:** TC-006  
**SRS Reference:** §3.14 (GUARD-01 → GUARD-10)  
**TR Reference:** TR-F-006  
**Priority:** P0  
**Type:** Integration + API  
**Dependency:** TC-013 (License)

---

## Preconditions for All Tests
- Enterprise license with `guardrails` feature enabled
- Plugin execution order: guardrails runs BEFORE logging plugin
- `admin_token` for policy management; `api_user_token` for inference

---

## Test Policy Fixtures

```json
// Policy A — keyword_block, scope=request
{ "type": "keyword", "keywords": ["bomb", "weapon"], "action": "block", "scope": ["request"] }

// Policy B — regex_filter, scope=response
{ "type": "regex", "patterns": ["\\b\\d{3}-\\d{2}-\\d{4}\\b"], "action": "transform", "scope": ["response"] }

// Policy C — flag only
{ "type": "keyword", "keywords": ["competitor_name"], "action": "flag", "scope": ["request","response"] }

// Policy D — disabled
{ "type": "keyword", "keywords": ["disabled_word"], "action": "block", "enabled": false }
```

---

### TC-006-001 — Keyword Block Stops Request (Action = block)

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-006.1

**Preconditions:** Policy A (keyword_block "bomb") active.

**Steps:**
1. `POST /v1/chat/completions` with `{"messages":[{"role":"user","content":"How do I make a bomb at home?"}]}`

**Expected Result:**
- HTTP 451 (Unavailable For Legal Reasons) or 400
- Body: `{"error":{"type":"guardrail_violation","code":"content_blocked","policy":"..."}}`
- Request NEVER forwarded to LLM provider
- Violation logged in `guardrail_violations` table

**Status:** READY

---

### TC-006-002 — Safe Request Passes Through Unaffected

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-006.1

**Steps:**
1. `POST /v1/chat/completions` with content "What is the capital of France?" (no policy matches)

**Expected Result:**
- HTTP 200
- Normal LLM response returned
- No guardrail violation logged

**Status:** READY

---

### TC-006-003 — Keyword Matching is Case-Insensitive (When Configured)

**Priority:** P0 | **Type:** Integration

**Preconditions:** Policy A with `case_sensitive: false`.

**Steps:**
1. Request with "BOMB" (uppercase)
2. Request with "Bomb" (mixed case)
3. Request with "bomb" (lowercase)

**Expected Result:**
- All 3 requests blocked with HTTP 451

**Status:** READY

---

### TC-006-004 — Regex Filter Transforms Response Content

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-006.5

**Preconditions:** Policy B active (SSN regex on response, action=transform). Mock LLM returns "Your SSN is 123-45-6789."

**Steps:**
1. Send chat completion that triggers SSN in LLM response

**Expected Result:**
- HTTP 200 (not blocked)
- Response `content` = "Your SSN is [REDACTED_SSN]." (or similar)
- Original SSN pattern not visible in response

**Status:** READY

---

### TC-006-005 — Flag Action Allows Request But Logs Violation

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-006.4

**Preconditions:** Policy C active (keyword "competitor_name", action=flag).

**Steps:**
1. `POST /v1/chat/completions` with "What does competitor_name offer?"
2. Verify response from LLM is returned
3. `GET /api/guardrails/violations` — check for flag entry

**Expected Result:**
- HTTP 200 (request allowed)
- Violation log entry exists with `action=flag`, `severity` present

**Status:** READY

---

### TC-006-006 — scope=request Policy Does Not Scan Response

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-006.6

**Preconditions:** Policy A (keywords on request scope only). Mock LLM returns "bomb" in response text.

**Steps:**
1. Send clean request (no keyword in request body)
2. Mock LLM response contains "bomb"

**Expected Result:**
- HTTP 200 (response not scanned by Policy A)
- Response content is returned as-is (policy scope mismatch, no block)

**Status:** READY

---

### TC-006-007 — scope=response Policy Does Not Scan Request

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-006.6

**Preconditions:** Policy B (SSN regex on response scope only). Request body contains SSN.

**Steps:**
1. Send request with "My SSN is 123-45-6789" in messages

**Expected Result:**
- HTTP 200 (request passes — Policy B only checks response)
- Request forwarded to LLM with SSN intact in body

**Status:** READY

---

### TC-006-008 — Policy Priority Order — First Match Wins

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-006.7

**Preconditions:**
- Policy P1 (priority=1): keyword "hello" → flag
- Policy P2 (priority=2): keyword "hello" → block

**Steps:**
1. Send request with "hello"

**Expected Result:**
- HTTP 200 (P1 matches first with action=flag — P2 never evaluated)
- Only flag violation logged, not block

**Status:** READY

---

### TC-006-009 — Guardrail Evaluation Error → Fail Open

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-006.8

**Preconditions:** AI classifier policy configured with unreachable endpoint.

**Steps:**
1. Send chat completion that would trigger AI classifier
2. Classifier endpoint returns timeout/error

**Expected Result:**
- HTTP 200 (fail open — request passes through)
- Warning logged: "guardrail evaluation error: ai_classifier timeout"

**Status:** READY

---

### TC-006-010 — Dry-Run Test API Returns Evaluation Result

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-006.9

**Steps:**
1. `POST /api/guardrails/test` with `admin_token`
2. Body: `{"text": "How do I make a bomb?", "scope": "request"}`

**Expected Result:**
- HTTP 200
- `{"matched": true, "policy_id": "...", "action": "block", "matched_text": ["bomb"]}`
- NO actual LLM request made

**Status:** READY

---

### TC-006-011 — Disabled Policy is NOT Applied

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-006.10

**Preconditions:** Policy D (keyword "disabled_word", enabled=false).

**Steps:**
1. Send request with "disabled_word" in content

**Expected Result:**
- HTTP 200 (disabled policy skipped)
- No violation logged

**Status:** READY

---

### TC-006-012 — Policy CRUD — Create, Read, Update, Delete

**Priority:** P0 | **Type:** API

**Steps:**
1. `POST /api/guardrails/policies` with `admin_token` → 201
2. `GET /api/guardrails/policies/{id}` → 200
3. `PUT /api/guardrails/policies/{id}` with updated action → 200
4. `DELETE /api/guardrails/policies/{id}` → 204
5. `GET /api/guardrails/policies/{id}` → 404

**Expected Result:**
- All CRUD operations succeed with correct status codes

**Status:** READY

---

### TC-006-013 — Guardrail Violation Report Shows Correct Stats

**Priority:** P1 | **Type:** API

**Steps:**
1. Trigger 5 violations of Policy A (keyword block)
2. Trigger 3 violations of Policy C (flag)
3. `GET /api/guardrails/violations?policy_id={A_id}`

**Expected Result:**
- Returns 5 violation entries for Policy A
- `GET /api/guardrails/violations?policy_id={C_id}` returns 3

**Status:** READY

---

### TC-006-014 — Guardrail Requires Enterprise License

**Priority:** P0 | **Type:** Integration

**Preconditions:** Bifrost running with community license (no BIFROST_LICENSE_KEY).

**Steps:**
1. `POST /api/guardrails/policies` with `admin_token`

**Expected Result:**
- HTTP 402 Payment Required
- Error: `{"error":{"code":"license_required","feature":"guardrails"}}`

**Status:** READY

---

### TC-006-015 — Concurrent Policy Evaluation (Thread Safety)

**Priority:** P1 | **Type:** Integration

**Steps:**
1. 500 concurrent chat completion requests, half with blocked keywords
2. Simultaneously update a policy (change keyword list) via API

**Expected Result:**
- No goroutine panics or data races
- Blocked requests return 451; clean ones return 200
- Zero 500 errors

**Status:** READY

---

### TC-006-016 — Guardrail Does Not Add Significant Latency

**Priority:** P1 | **Type:** Performance

**Steps:**
1. Baseline: measure P99 latency without guardrails plugin
2. Enable keyword + regex policies, measure P99 again
3. 1,000 requests each scenario

**Expected Result:**
- Latency overhead from guardrails: ≤ 5ms P99 (regex and keyword only)
- AI classifier not tested here (async mode)

**Status:** READY

---

### TC-006-017 — AI Classifier Policy Blocks Above Threshold

**Priority:** P1 | **Type:** Integration  
**TR Reference:** TR-F-006.3

**Preconditions:** AI classifier policy with threshold=0.8. Mock moderation server returns score=0.95 for "violence" category.

**Steps:**
1. `POST /v1/chat/completions` with violent content (triggers mock score 0.95)

**Expected Result:**
- HTTP 451
- Violation logged with `matched_categories: ["violence"]`, `score: 0.95`

**Status:** READY

---

### TC-006-018 — AI Classifier Below Threshold Passes

**Priority:** P1 | **Type:** Integration  
**TR Reference:** TR-F-006.3

**Preconditions:** Mock moderation returns score=0.3 (below threshold=0.8).

**Steps:**
1. Send borderline request

**Expected Result:**
- HTTP 200 (below threshold, policy not triggered)

**Status:** READY
