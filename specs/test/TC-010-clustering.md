# Test Cases — Multi-Node Clustering

**Suite ID:** TC-010  
**SRS Reference:** §3.18  
**TR Reference:** TR-NF-003  
**Priority:** P1  
**Type:** Integration  
**Dependency:** TC-013 (License: clustering feature)

---

## Preconditions
- Enterprise license with `clustering` feature
- Docker Compose setup: 3 Bifrost nodes + shared PostgreSQL + shared Redis
- Node addresses: `node-1:8080`, `node-2:8081`, `node-3:8082`
- Load balancer distributing requests round-robin across all 3 nodes

---

### TC-010-001 — All Nodes Register in Cluster Table

**Priority:** P1 | **Type:** Integration

**Steps:**
1. Start all 3 Bifrost nodes
2. Wait 10 seconds for heartbeat cycles
3. `GET /api/cluster/nodes` via any node

**Expected Result:**
- HTTP 200
- Response lists 3 nodes with `status=healthy` each
- Each entry has `hostname`, `version`, `last_heartbeat`, `address`

**Status:** READY

---

### TC-010-002 — Node Failure Detected Within Heartbeat Window

**Priority:** P1 | **Type:** Integration

**Preconditions:** Heartbeat interval = 10s, failure threshold = 2 missed heartbeats → unhealthy.

**Steps:**
1. All 3 nodes healthy
2. Kill `node-3` (stop container)
3. Wait 25 seconds (2.5 heartbeat cycles)
4. `GET /api/cluster/nodes` via `node-1`

**Expected Result:**
- `node-3` status = "unhealthy" or "offline"
- `node-1` and `node-2` still "healthy"
- Traffic no longer routed to `node-3`

**Status:** READY

---

### TC-010-003 — Distributed Rate Limit Across Nodes

**Priority:** P1 | **Type:** Integration  
**TR Reference:** TR-NF-003.2

**Preconditions:** VK with `rate_limit.requests_per_minute: 10`. Redis shared rate limit counter.

**Steps:**
1. Send 5 requests via `node-1`
2. Send 5 requests via `node-2` (same VK, different node)
3. Send 1 more request via `node-3` (should exceed limit)

**Expected Result:**
- First 10 requests: HTTP 200
- 11th request: HTTP 429 (distributed counter = 11 > 10)

**Status:** READY

---

### TC-010-004 — Distributed Budget Tracking Accuracy

**Priority:** P1 | **Type:** Integration  
**TR Reference:** TR-NF-003.3

**Preconditions:** VK budget = $1.00. Requests costing $0.05 each.

**Steps:**
1. Send 10 requests spread across 3 nodes (→ each node 3-4 requests)
2. Total cost = ~$0.50
3. Verify budget usage via `GET /api/governance/virtual-keys/{id}/usage`

**Expected Result:**
- `total_cost` ≈ $0.50 (±1% drift between nodes)
- No double-counting; no under-counting

**Status:** READY

---

### TC-010-005 — Config Change Propagated to All Nodes

**Priority:** P1 | **Type:** Integration

**Steps:**
1. Update VK budget via `node-1`: `PUT /api/governance/virtual-keys/{id}` → new budget $5.00
2. Immediately query `node-2` and `node-3`: `GET /api/governance/virtual-keys/{id}`

**Expected Result:**
- Within 5 seconds: all nodes return `budget: 5.00`
- Old cached value invalidated via Redis pub/sub

**Status:** READY

---

### TC-010-006 — Cache Invalidation on Provider Update

**Priority:** P1 | **Type:** Integration

**Steps:**
1. `node-1` has cached provider info
2. Update provider via `node-2` (e.g., change base_url)
3. Send request via `node-1`

**Expected Result:**
- `node-1` uses NEW base_url (cache invalidated)
- No stale routing within 2 seconds of update

**Status:** READY

---

### TC-010-007 — Leader Election — Only One Node Runs Budget Reset

**Priority:** P1 | **Type:** Integration

**Preconditions:** Budget reset scheduled every minute. Cluster of 3 nodes.

**Steps:**
1. Wait for budget reset cycle
2. Check application logs across all 3 nodes

**Expected Result:**
- Exactly ONE node logs "Running budget reset as leader"
- Other nodes log "Skipping budget reset — not leader"
- Budget reset executes exactly once (not 3 times)

**Status:** READY

---

### TC-010-008 — New Node Joins Cluster and Receives State

**Priority:** P1 | **Type:** Integration

**Steps:**
1. 2 nodes running with VKs and active rate limits
2. Start `node-3` (new joiner)
3. Wait for registration
4. Send request via `node-3` with active VK

**Expected Result:**
- `node-3` correctly enforces rate limits (shared Redis state)
- `node-3` appears in `/api/cluster/nodes` within 15 seconds

**Status:** READY

---

### TC-010-009 — Read-Your-Writes Consistency for Management API

**Priority:** P1 | **Type:** Integration

**Steps:**
1. Create VK via `node-1`
2. Immediately do `GET /api/governance/virtual-keys` via `node-2`

**Expected Result:**
- VK appears in list on `node-2` immediately
- (PostgreSQL is the source of truth; no stale node-local DB)

**Status:** READY

---

### TC-010-010 — Clustering Feature Requires Enterprise License

**Priority:** P1 | **Type:** Integration

**Preconditions:** Community license.

**Steps:**
1. `GET /api/cluster/nodes`

**Expected Result:**
- HTTP 402 with `license_required: clustering`

**Status:** READY
