# Test Cases — Immutable Audit Logs

**Suite ID:** TC-005  
**SRS Reference:** §3.13 (AUDIT-01 → AUDIT-10)  
**TR Reference:** TR-F-005  
**Priority:** P0  
**Type:** Integration + API  
**Dependency:** TC-013 (License), TC-004 (RBAC)

---

## Preconditions for All Tests
- Enterprise license present
- `super_admin_token` and `viewer_token` available
- PostgreSQL backend (not SQLite) for append-only trigger tests

---

### TC-005-001 — Provider Creation Generates Audit Log Entry

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-005.1

**Steps:**
1. `POST /api/providers` with `admin_token` — create provider "test-provider-abc"
2. `GET /api/audit/logs?resource=provider&action=create` with `admin_token`

**Expected Result:**
- At least one audit entry matching:
  - `action` = "create"
  - `resource` = "provider"
  - `resource_name` = "test-provider-abc"
  - `actor_role` = "admin"
  - `success` = true
  - `timestamp` is recent (within last 30s)

**Status:** READY

---

### TC-005-002 — Provider Deletion Generates Audit Log Entry

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-005.1

**Steps:**
1. `DELETE /api/providers/{id}` with `admin_token`
2. `GET /api/audit/logs?resource=provider&action=delete`

**Expected Result:**
- Audit entry with `action=delete`, `resource_id={id}`, `success=true`

**Status:** READY

---

### TC-005-003 — Login Event is Audited

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-005.6

**Steps:**
1. `POST /api/session/login` with valid credentials
2. `GET /api/audit/logs?resource=session&action=login`

**Expected Result:**
- Audit entry: `action=login`, `actor_name={username}`, `actor_ip` present

**Status:** READY

---

### TC-005-004 — Audit Log Cannot Be Updated (Append-Only)

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-005.2

**Preconditions:** Direct DB access to test append-only enforcement.

**Steps:**
1. Get audit log entry ID via API
2. Attempt direct SQL: `UPDATE audit_logs SET action='fake' WHERE id='{id}'`
3. OR attempt any API that would trigger an update

**Expected Result:**
- SQL UPDATE raises database error (trigger abort or permission denied)
- No API endpoint accepts modification of existing audit entries

**Status:** READY

---

### TC-005-005 — Audit Log Cannot Be Deleted

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-005.2

**Steps:**
1. Attempt direct SQL: `DELETE FROM audit_logs WHERE id='{id}'`
2. Verify no API endpoint allows individual entry deletion

**Expected Result:**
- SQL DELETE raises error (trigger or permission denied)
- `DELETE /api/audit/logs/{id}` returns 405 Method Not Allowed

**Status:** READY

---

### TC-005-006 — Hash Chain Integrity — Sequential Entries

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-005.3

**Steps:**
1. Perform 5 management actions (create 5 virtual keys)
2. Retrieve last 5 audit entries ordered by sequence
3. Verify: `entry[n].prev_hash == entry[n-1].entry_hash` for n=1..4
4. Verify: `entry[0].prev_hash == "genesis"` (or hash of actual predecessor)

**Expected Result:**
- Hash chain is intact for all 5 entries
- Each `entry_hash` = SHA-256 of entry fields (excluding `entry_hash` itself)

**Status:** READY

---

### TC-005-007 — Chain Integrity Verification API — Intact Chain

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-005.4

**Steps:**
1. `GET /api/audit/verify?from_seq=1&to_seq=100`

**Expected Result:**
- HTTP 200
- `{"intact": true, "invalid_sequences": []}`

**Status:** READY

---

### TC-005-008 — Chain Integrity Verification API — Tampered Chain

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-005.4

**Preconditions:** Manually corrupt one entry's `entry_hash` via direct SQL (requires superuser DB access outside Bifrost).

**Steps:**
1. Corrupt `entry_hash` of entry at sequence=50 via direct SQL
2. `GET /api/audit/verify?from_seq=1&to_seq=100`

**Expected Result:**
- HTTP 200
- `{"intact": false, "invalid_sequences": [50]}`

**Status:** READY

---

### TC-005-009 — Audit Log Search — Filter by Actor

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-005.7

**Steps:**
1. Perform actions with `admin_token` (admin user ID known)
2. `GET /api/audit/logs?actor_id={admin_user_id}`

**Expected Result:**
- Only entries by admin user returned
- Other actors' entries excluded

**Status:** READY

---

### TC-005-010 — Audit Log Search — Filter by Time Range

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-005.7

**Steps:**
1. Record `start_time = now()`
2. Perform 3 actions
3. Record `end_time = now()`
4. `GET /api/audit/logs?start_time={start_time}&end_time={end_time}`

**Expected Result:**
- Returns exactly the 3 entries within the time range
- No entries outside range included

**Status:** READY

---

### TC-005-011 — Audit Log Export — JSON Format

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-005.5

**Steps:**
1. `GET /api/audit/export?format=json&start_time={T1}&end_time={T2}` with `super_admin_token`

**Expected Result:**
- HTTP 200
- `Content-Disposition: attachment; filename="audit-export-*.json"`
- Response body is valid JSON array of audit entries
- All entries have required fields: id, timestamp, actor_id, action, resource, success

**Status:** READY

---

### TC-005-012 — Audit Log Export — CSV Format

**Priority:** P1 | **Type:** API  
**TR Reference:** TR-F-005.5

**Steps:**
1. `GET /api/audit/export?format=csv&start_time={T1}&end_time={T2}` with `super_admin_token`

**Expected Result:**
- HTTP 200
- `Content-Type: text/csv`
- First row = header row with column names
- Subsequent rows = audit entries

**Status:** READY

---

### TC-005-013 — Audit Log Write Failure Does Not Block Request

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-005.8

**Preconditions:** Temporarily disconnect PostgreSQL audit log table (simulate write failure by dropping connection in test).

**Steps:**
1. Make management API call (POST provider) while audit log write will fail
2. Verify provider creation succeeds

**Expected Result:**
- `POST /api/providers` returns HTTP 201 (success)
- Application log shows warning: "audit log write failed"
- Provider is actually created in DB

**Status:** READY

---

### TC-005-014 — viewer CAN Read Audit Logs

**Priority:** P0 | **Type:** API

**Steps:**
1. `GET /api/audit/logs` with `viewer_token`

**Expected Result:**
- HTTP 200 (viewer has read access to audit logs)

**Status:** READY

---

### TC-005-015 — viewer CANNOT Export Audit Logs

**Priority:** P0 | **Type:** API

**Steps:**
1. `GET /api/audit/export?format=json` with `viewer_token`

**Expected Result:**
- HTTP 403 (export requires admin+ permission)

**Status:** READY
