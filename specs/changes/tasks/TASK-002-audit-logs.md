# TASK-002 — Audit Logs

**Feature:** Audit Logs  
**TECH Spec:** [TECH-002-audit-logs.md](../TECH-002-audit-logs.md)  
**Phase:** 1 (Security Core)  
**Depends on:** TASK-014 (license), TASK-001 (RBAC — for actor identity)  
**Estimate:** 4 days  
**Assignee:** —  
**Status:** 🟢 Completed

---

## Context

Audit logs must be **append-only** with **SHA-256 hash chaining** to detect tampering.  
Every state-changing operation (admin API POST/PUT/DELETE) must produce an audit record.  
The audit log writer runs asynchronously via a buffered channel to avoid adding latency to request handlers.

**Critical ordering rule:** The audit log plugin must run **after** PII Redactor in the plugin chain so that PII is never written into audit records.

---

## Tasks

### TASK-002-01 — Database schema + GORM migration

**Files to create/modify:**
- `framework/logstore/tables/audit.go` — `AuditLogTable`
- `framework/logstore/migrations/` — migration file

**Schema:**
```go
type AuditLogTable struct {
    ID          string    `gorm:"primaryKey;type:text"`  // UUID v4
    Seq         int64     `gorm:"autoIncrement;uniqueIndex"` // monotonic sequence
    Timestamp   time.Time `gorm:"index;not null"`
    ActorID     string    `gorm:"index;not null"`   // user ID or "system"
    ActorEmail  string
    ActorRole   string                               // role at time of action
    Action      string    `gorm:"index;not null"`   // "providers.create" | "vk.delete" | ...
    Resource    string    `gorm:"index"`             // resource type
    ResourceID  string    `gorm:"index"`             // affected entity ID
    RequestID   string    `gorm:"index"`
    ClientIP    string
    UserAgent   string
    OldValue    string    `gorm:"type:text"`  // JSON snapshot before change
    NewValue    string    `gorm:"type:text"`  // JSON snapshot after change
    Result      string    // "success" | "failure"
    ErrorMsg    string
    Hash        string    `gorm:"not null"` // SHA-256(Seq + Timestamp + ActorID + Action + PrevHash)
    PrevHash    string                      // hash of previous record (chain)
}
```

**Acceptance criteria:**
- [ ] Migration runs cleanly on fresh database
- [ ] `Seq` column is auto-increment and unique indexed
- [ ] `Hash` and `PrevHash` columns exist for chain integrity verification

---

### TASK-002-02 — Audit log writer (async, with hash chaining)

**Files to create:**
- `framework/logstore/audit_writer.go` — `AuditWriter` struct

**Implementation requirements:**
```go
type AuditWriter struct {
    db      *gorm.DB
    queue   chan *AuditLogEntry   // buffered, size 1000
    mu      sync.Mutex            // protects prevHash
    prevHash string
}

func (w *AuditWriter) Write(entry *AuditLogEntry) {
    // Non-blocking send; drop with warning if queue full
    select {
    case w.queue <- entry:
    default:
        logger.Warn("audit log queue full, dropping entry", "action", entry.Action)
    }
}

func (w *AuditWriter) processLoop() {
    for entry := range w.queue {
        w.mu.Lock()
        entry.Hash = sha256Chain(entry.Seq, entry.Timestamp, entry.ActorID, entry.Action, w.prevHash)
        entry.PrevHash = w.prevHash
        w.prevHash = entry.Hash
        w.mu.Unlock()
        w.db.Create(entry)
    }
}
```

**Acceptance criteria:**
- [ ] Write is non-blocking; full queue drops with warn log (never blocks request handler)
- [ ] `processLoop` goroutine started at server bootstrap
- [ ] SHA-256 hash is correctly computed over: `seq|timestamp|actorID|action|prevHash`
- [ ] `prevHash` of first record is `"0000...0000"` (64 zeros)
- [ ] Graceful shutdown: drain queue before process exit

---

### TASK-002-03 — Audit middleware (HTTP interceptor)

**Files to create:**
- `transports/bifrost-http/handlers/audit_middleware.go` — `AuditMiddleware`

**What is auto-logged:**
- All `POST`, `PUT`, `PATCH`, `DELETE` requests to `/api/*` routes
- Actor ID, role, client IP, user agent from session/request context
- Request body snapshot (before change) and response body snapshot (after change)
- HTTP status → `"success"` if `2xx`, `"failure"` otherwise

**Acceptance criteria:**
- [ ] Middleware wraps response writer to capture status code and response body
- [ ] `OldValue` captured before handler executes (for update operations: fetch existing record)
- [ ] `NewValue` captured from response body after handler executes
- [ ] PII fields in request/response bodies masked before writing to `OldValue`/`NewValue` (integration with PII Redactor if enabled)
- [ ] Middleware added to all admin API route groups

---

### TASK-002-04 — Audit log query API

**Files to create:**
- `transports/bifrost-http/handlers/audit.go`
- `framework/logstore/audit_query.go` — query functions

**Endpoints:**
```
GET /api/audit/logs
  Query params: actor_id, action, resource, resource_id, start, end, page, page_size
  Requires: audit_logs:read permission (admin+)

GET /api/audit/logs/{id}
  Get single audit log entry

GET /api/audit/verify
  Verifies hash chain integrity from seq start to end
  Returns: { valid: bool, broken_at_seq: int64|null }

GET /api/audit/stats
  Total events, events per action type, top actors (last 30 days)
```

**Acceptance criteria:**
- [ ] `GET /api/audit/logs` supports all filter params with pagination
- [ ] `GET /api/audit/verify` correctly detects if any record hash is broken in the chain
- [ ] All endpoints require `audit_logs:read` permission
- [ ] When `audit_logs` license feature disabled → `GET /api/audit/logs` returns 402

---

### TASK-002-05 — Programmatic write helper (for non-HTTP operations)

**Files to create:**
- `transports/bifrost-http/handlers/audit_helper.go` — `WriteAuditLog(ctx, action, resource, resourceID, oldVal, newVal)`

Used by RBAC role assignment, plugin enable/disable, etc., that aren't captured by the HTTP middleware.

**Acceptance criteria:**
- [ ] Helper correctly extracts actor ID and role from `BifrostContext`
- [ ] Can be called from any handler without blocking

---

### TASK-002-06 — UI: Audit log viewer

**Files to create:**
- `ui/app/enterprise/audit-logs/page.tsx`
- `ui/app/enterprise/audit-logs/components/AuditLogTable.tsx`
- `ui/app/enterprise/audit-logs/components/AuditLogFilters.tsx`
- `ui/app/enterprise/audit-logs/components/AuditLogDetail.tsx`
- `ui/app/enterprise/audit-logs/components/ChainVerifyBanner.tsx`

**Acceptance criteria:**
- [ ] Infinite scroll or pagination for audit log table
- [ ] Filters: actor, action type, date range, resource type
- [ ] Detail drawer shows old/new value diff
- [ ] "Verify Chain Integrity" button triggers `GET /api/audit/verify` and shows result banner
- [ ] Page rendered inside `<EnterpriseGate feature="audit_logs">`

---

## Definition of Done

- [ ] All subtasks complete
- [ ] Unit test: hash chaining produces correct chain; tampering detection works
- [ ] Integration test: POST /api/providers → audit record created with correct actor and hash
- [ ] Integration test: DELETE operation → `OldValue` populated, `NewValue` null
- [ ] `GET /api/audit/verify` returns `valid: true` for uncorrupted chain
- [ ] `make build` passes
