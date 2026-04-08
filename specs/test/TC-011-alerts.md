# Test Cases — Alert Channels

**Suite ID:** TC-011  
**SRS Reference:** §3.19  
**TR Reference:** TR-F-009 (via Alert obligations)  
**Priority:** P1  
**Type:** Integration + API  
**Dependency:** TC-013 (License: alerts feature)

---

## Preconditions
- Enterprise license with `alerts` feature
- WireMock running to capture webhook calls
- Test Slack webhook URL pointing to WireMock
- `admin_token` for config operations

---

### TC-011-001 — Create Webhook Alert Channel

**Priority:** P1 | **Type:** API

**Steps:**
1. `POST /api/alert-channels` with `admin_token`
2. Body:
```json
{
  "name": "Test Webhook",
  "type": "webhook",
  "endpoint_url": "http://localhost:8088/capture/webhook",
  "enabled": true,
  "events": ["budget_breach", "provider_error_rate"]
}
```

**Expected Result:**
- HTTP 201
- Channel created with `id`, `type=webhook`, `enabled=true`
- `GET /api/alert-channels/{id}` returns created channel

**Status:** READY

---

### TC-011-002 — Test Alert Channel (Manual Test Trigger)

**Priority:** P1 | **Type:** API

**Steps:**
1. `POST /api/alert-channels/{id}/test` with `admin_token`

**Expected Result:**
- HTTP 200
- WireMock captures a POST to webhook URL
- Request body contains: `{"event":"test","channel":"{id}","timestamp":"..."}`

**Status:** READY

---

### TC-011-003 — Budget Breach Triggers Alert

**Priority:** P1 | **Type:** Integration

**Preconditions:**
- Alert rule: `budget_breach` → webhook channel
- VK with budget $0.001
- Alert threshold: 80% of budget = $0.0008

**Steps:**
1. Send requests until VK budget hits 80% threshold
2. Check WireMock for captured webhook call

**Expected Result:**
- Webhook received with body:
```json
{
  "event": "budget_warning",
  "severity": "warning",
  "vk_id": "...",
  "threshold_pct": 80,
  "current_usage_usd": 0.0008,
  "limit_usd": 0.001
}
```
- Alert sent only once per threshold breach (not per request)

**Status:** READY

---

### TC-011-004 — Budget 100% Breach Triggers Critical Alert

**Priority:** P1 | **Type:** Integration

**Steps:**
1. Continue from TC-011-003 — exhaust budget fully

**Expected Result:**
- Second webhook with `"event":"budget_breach"`, `"severity":"critical"`
- Inference returns 429 to caller

**Status:** READY

---

### TC-011-005 — Provider Error Rate Alert

**Priority:** P1 | **Type:** Integration

**Preconditions:**
- Alert rule: `provider_error_rate` threshold=0.3 for window=60s
- `provider-error` configured to fail 50% of requests

**Steps:**
1. Send 30 requests through `provider-error`
2. Error rate should exceed 0.3 threshold

**Expected Result:**
- Webhook fires with `"event":"provider_error_rate","provider":"provider-error","error_rate":0.5`

**Status:** READY

---

### TC-011-006 — Guardrail Violation Alert

**Priority:** P1 | **Type:** Integration

**Preconditions:** Alert rule: `guardrail_violation` subscribed.

**Steps:**
1. Trigger 5 guardrail violations (send blocked content)
2. Check webhook

**Expected Result:**
- Webhook fires per violation OR batched per minute (depends on config)
- Body contains `"event":"guardrail_violation","policy_id":"...","count":5`

**Status:** READY

---

### TC-011-007 — Alert Not Re-fired While Already Firing

**Priority:** P1 | **Type:** Integration

**Steps:**
1. VK budget at 85% (above 80% threshold — alert fires)
2. Send 5 more small requests (still above 80% but below 100%)
3. Check WireMock call count

**Expected Result:**
- Only 1 webhook call, not 6 (de-duplication while alert state is "firing")

**Status:** READY

---

### TC-011-008 — Alert Resolved (Budget Reset)

**Priority:** P1 | **Type:** Integration

**Steps:**
1. Trigger budget_warning alert (VK at 85%)
2. Reset budget manually via API
3. Check WireMock for "resolved" event

**Expected Result:**
- Second webhook call with `"event":"budget_warning","status":"resolved"`
- Alert state returns to "inactive"

**Status:** READY

---

### TC-011-009 — Disabled Alert Channel Does Not Fire

**Priority:** P1 | **Type:** Integration

**Steps:**
1. Create alert channel (enabled=true)
2. `PUT /api/alert-channels/{id}` with `"enabled": false`
3. Trigger budget breach

**Expected Result:**
- WireMock receives NO webhook call
- Alert event still recorded in `alert_history` table (internal log retained)

**Status:** READY

---

### TC-011-010 — Slack Alert Channel Format

**Priority:** P1 | **Type:** Integration

**Preconditions:** Channel type=slack, webhook URL points to WireMock.

**Steps:**
1. Trigger any alert

**Expected Result:**
- WireMock captures POST with Slack-format body:
```json
{
  "blocks": [
    {"type": "header", "text": {"type": "plain_text", "text": "⚠️ Bifrost Alert"}},
    {"type": "section", "text": {"type": "mrkdwn", "text": "Budget warning: VK ..."}},
    ...
  ]
}
```

**Status:** READY

---

### TC-011-011 — Alert Channel CRUD

**Priority:** P1 | **Type:** API

**Steps:**
1. Create channel → 201
2. GET channel → 200
3. PUT update (change endpoint URL) → 200
4. DELETE channel → 204
5. GET deleted channel → 404

**Status:** READY

---

### TC-011-012 — Rate Limit Hit Alert

**Priority:** P1 | **Type:** Integration

**Preconditions:** Alert rule: `rate_limit_hit`. VK with 1 req/min limit.

**Steps:**
1. Exhaust rate limit (send 2 requests quickly)
2. Check webhook

**Expected Result:**
- Webhook fires with `"event":"rate_limit_hit","vk_id":"..."`

**Status:** READY

---

### TC-011-013 — Alert History Queryable

**Priority:** P1 | **Type:** API

**Steps:**
1. Trigger 3 alerts of different types
2. `GET /api/alert-history?limit=10` with `admin_token`

**Expected Result:**
- HTTP 200
- 3 alert history entries with `event`, `channel_id`, `fired_at`, `resolved_at`, `status`

**Status:** READY

---

### TC-011-014 — Alerts Require Enterprise License

**Priority:** P1 | **Type:** Integration

**Preconditions:** Community license.

**Steps:**
1. `POST /api/alert-channels`

**Expected Result:**
- HTTP 402 with `license_required` error

**Status:** READY
