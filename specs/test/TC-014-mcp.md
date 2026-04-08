# Test Cases — MCP Tool Groups & Data Connectors

**Suite ID:** TC-014  
**SRS Reference:** §3.22 (MCP Tool Groups), §3.24 (Data Connectors)  
**Priority:** P2  
**Type:** Integration + API  
**Dependency:** TC-013 (License)

---

## PART A — MCP Tool Groups (§3.22)

### Preconditions (MCP)
- Enterprise license with `mcp_tool_groups` feature
- MCP client "web-tools" registered with tools: `search_web`, `fetch_url`, `summarize`
- MCP client "code-tools" registered with tools: `run_code`, `bash_exec`
- admin_token available

---

### TC-014-001 — Create MCP Tool Group

**Priority:** P2 | **Type:** API

**Steps:**
1. `POST /api/mcp/tool-groups` with `admin_token`
2. Body:
```json
{
  "name": "safe-research-tools",
  "description": "Read-only web tools for research",
  "tools": [
    {"mcp_client_id": "client_web_tools", "tool_name": "search_web"},
    {"mcp_client_id": "client_web_tools", "tool_name": "fetch_url"}
  ]
}
```

**Expected Result:**
- HTTP 201
- Group created with `id`, `name`, `tools` array

**Status:** READY

---

### TC-014-002 — Assign Tool Group to Virtual Key

**Priority:** P2 | **Type:** API

**Steps:**
1. Create tool group (TC-014-001)
2. `PUT /api/governance/virtual-keys/{vk_id}/mcp-tool-groups` with `admin_token`
3. Body: `{"tool_group_ids": ["{group_id}"]}`

**Expected Result:**
- HTTP 200
- VK now has tool group assigned

**Status:** READY

---

### TC-014-003 — VK with Tool Group — Only Group Tools Available

**Priority:** P2 | **Type:** Integration

**Preconditions:** VK assigned to "safe-research-tools" (search_web, fetch_url). `run_code` NOT in group.

**Steps:**
1. `POST /v1/chat/completions` with VK, and `tool_choice=auto`
2. Model attempts to call `run_code`

**Expected Result:**
- `run_code` tool NOT injected into available tools for this VK
- Model can only use `search_web`, `fetch_url`
- Response ONLY references allowed tools

**Status:** READY

---

### TC-014-004 — Tool Group CRUD Lifecycle

**Priority:** P2 | **Type:** API

**Steps:**
1. POST → 201
2. GET → 200 with correct tools
3. PUT (add tool) → 200
4. DELETE → 204
5. GET → 404

**Status:** READY

---

### TC-014-005 — Duplicate Tool in Group Rejected

**Priority:** P2 | **Type:** API

**Steps:**
1. Create group with `search_web` twice in tools array

**Expected Result:**
- HTTP 400
- Error: duplicate tool in group

**Status:** READY

---

### TC-014-006 — Remove Tool from Group Affects Running VKs

**Priority:** P2 | **Type:** Integration

**Steps:**
1. VK assigned to group with `search_web` + `fetch_url`
2. `DELETE /api/mcp/tool-groups/{id}/tools/search_web`
3. Send inference request with VK

**Expected Result:**
- `search_web` tool NO LONGER available in MCP context
- Only `fetch_url` injected

**Status:** READY

---

### TC-014-007 — Tool Group Usage Quota Per VK

**Priority:** P2 | **Type:** Integration

**Preconditions:** Tool group with `quota: max_calls_per_hour: 5`.

**Steps:**
1. Make 5 MCP tool calls via inference requests
2. Make 6th tool call

**Expected Result:**
- 6th call blocked with quota error
- HTTP 429 or tool execution fails with budget error

**Status:** READY

---

### TC-014-008 — List Tool Groups Returns Paginated Results

**Priority:** P2 | **Type:** API

**Steps:**
1. Create 10 tool groups
2. `GET /api/mcp/tool-groups?page=1&limit=5`

**Expected Result:**
- HTTP 200
- 5 groups returned
- `pagination.total` = 10, `pagination.page` = 1

**Status:** READY

---

## PART B — Data Connectors (§3.24)

### Preconditions (Connectors)
- Enterprise license with `data_connectors` feature
- WireMock running to capture HTTP connector calls
- admin_token available

---

### TC-014-009 — Create Webhook Data Connector

**Priority:** P2 | **Type:** API

**Steps:**
1. `POST /api/data-connectors` with `admin_token`
2. Body:
```json
{
  "name": "test-webhook-connector",
  "type": "webhook",
  "config": {
    "endpoint_url": "http://localhost:8088/capture/logs",
    "auth_header": "Bearer test",
    "batch_size": 100,
    "flush_interval_seconds": 60
  },
  "enabled": true
}
```

**Expected Result:**
- HTTP 201
- Connector created with id

**Status:** READY

---

### TC-014-010 — Test Data Connector Connectivity

**Priority:** P2 | **Type:** API

**Steps:**
1. `POST /api/data-connectors/{id}/test` with `admin_token`

**Expected Result:**
- HTTP 200
- WireMock captures test ping request
- Response: `{"status":"connected","latency_ms":15}`

**Status:** READY

---

### TC-014-011 — Data Connector Exports Logs Automatically

**Priority:** P2 | **Type:** Integration

**Preconditions:** Connector with `flush_interval_seconds: 30`.

**Steps:**
1. Send 10 inference requests (logging plugin records them)
2. Wait 31 seconds for flush
3. Check WireMock for captured log batch

**Expected Result:**
- WireMock receives POST with batch of 10 log entries
- Each entry has: `request_id`, `model`, `provider`, `input_tokens`, `output_tokens`, `latency_ms`

**Status:** READY

---

### TC-014-012 — Connector Failure Does Not Block Inference

**Priority:** P2 | **Type:** Integration

**Preconditions:** Connector endpoint returns 500 (simulated failure).

**Steps:**
1. Normal inference requests continue while connector fails

**Expected Result:**
- Inference HTTP 200 (unaffected)
- Connector failure logged as warning
- Failed batch retried on next flush cycle

**Status:** READY

---

### TC-014-013 — Data Connector CRUD

**Priority:** P2 | **Type:** API

**Steps:**
1. POST → 201
2. GET → 200
3. PUT update endpoint_url → 200
4. DELETE → 204

**Status:** READY

---

### TC-014-014 — BigQuery Connector Configuration (Schema Validation)

**Priority:** P2 | **Type:** API

**Steps:**
1. `POST /api/data-connectors` with `type: bigquery`
2. Body includes BQ-specific: `{"project_id":"my-project","dataset":"bifrost_logs","table":"requests","credentials":"{json}"}`

**Expected Result:**
- HTTP 201 (config schema accepted)
- Connector type stored as "bigquery"

**Status:** READY

---

### TC-014-015 — Datadog Connector — API Key Format Validated

**Priority:** P2 | **Type:** API

**Steps:**
1. `POST /api/data-connectors` with `type: datadog`, providing invalid `api_key` format

**Expected Result:**
- HTTP 400
- Error: "invalid Datadog API key format"

**Status:** READY

---

### TC-014-016 — Disabled Connector Does Not Export

**Priority:** P2 | **Type:** Integration

**Steps:**
1. Create connector with `enabled: false`
2. Run inference requests
3. Wait for flush interval
4. Check WireMock

**Expected Result:**
- WireMock receives NO export calls while connector is disabled

**Status:** READY
