# TECH-002 — Immutable Audit Logs

**Feature ID:** AUDIT  
**SRS Reference:** §3.13 (AUDIT-01 → AUDIT-10)  
**CR Reference:** CR-ENT-001, CR-ENT-002  
**Version:** 1.0 | **Date:** 2026-04-08  
**Status:** Design Ready

---

## 1. Overview

Implement an append-only audit trail capturing every management action (create/update/delete on any governed resource). Audit logs must be cryptographically tamper-evident, queryable, and exportable for compliance purposes (SOC 2, GDPR).

**Key constraints from SRS:**
- `AUDIT-03`: No `UPDATE` or `DELETE` on audit_logs table — append-only
- `AUDIT-05`: Each entry carries SHA-256 hash of previous entry (chain integrity)
- `AUDIT-07`: Export to JSON/CSV within 5 minutes for any time range
- `AUDIT-10`: Retention ≥ 2 years with configurable archive policy

---

## 2. Architecture Mapping

```
framework/
├── auditstore/               (NEW module)
│   ├── store.go              AuditStore interface
│   ├── sqlite.go             SQLite implementation
│   ├── postgres.go           PostgreSQL implementation
│   ├── tables.go             AuditLogTable GORM model
│   ├── hasher.go             SHA-256 chain integrity
│   └── exporter.go           JSON/CSV export

transports/bifrost-http/
├── handlers/
│   └── audit.go              (NEW) /api/audit/* endpoints
└── lib/
    └── audit_interceptor.go  (NEW) AuditInterceptorMiddleware
```

---

## 3. Data Model

```go
// framework/auditstore/tables.go

type AuditLogTable struct {
    ID           string    `gorm:"primaryKey;type:text"`
    Sequence     int64     `gorm:"autoIncrement;uniqueIndex"`  // monotone sequence
    Timestamp    time.Time `gorm:"index;not null"`
    
    // Actor
    ActorID      string    `gorm:"index"`   // session UserID
    ActorName    string
    ActorRole    string
    ActorIP      string
    SessionToken string    `gorm:"index"`
    
    // Action
    Action       string    `gorm:"index;not null"` // "create","update","delete","read_sensitive"
    Resource     string    `gorm:"index;not null"` // "provider","virtual_key","team",...
    ResourceID   string    `gorm:"index"`
    ResourceName string
    
    // Delta
    OldValue     string    `gorm:"type:text"`  // JSON snapshot before change
    NewValue     string    `gorm:"type:text"`  // JSON snapshot after change
    
    // Integrity chain
    PrevHash     string    `gorm:"type:text"`  // SHA-256 of previous entry
    EntryHash    string    `gorm:"type:text;uniqueIndex"`  // SHA-256(this entry minus EntryHash)
    
    // Request context
    RequestID    string
    UserAgent    string
    StatusCode   int
    
    // Outcome
    Success      bool      `gorm:"index"`
    ErrorMessage string
}
// NOTE: No UpdatedAt gorm tag — prevents GORM from issuing UPDATE statements
// Use DB-level constraint: REVOKE UPDATE, DELETE ON audit_logs FROM app_user
```

### 3.1 Append-Only Enforcement

**SQLite:** Trigger on the table:
```sql
CREATE TRIGGER audit_logs_no_update
BEFORE UPDATE ON audit_logs
BEGIN
    SELECT RAISE(ABORT, 'audit_logs is append-only');
END;

CREATE TRIGGER audit_logs_no_delete
BEFORE DELETE ON audit_logs
BEGIN
    SELECT RAISE(ABORT, 'audit_logs is append-only');
END;
```

**PostgreSQL:** Row-level security + revoke:
```sql
REVOKE UPDATE, DELETE ON audit_logs FROM bifrost_app;
ALTER TABLE audit_logs ENABLE ROW LEVEL SECURITY;
CREATE POLICY audit_insert_only ON audit_logs FOR INSERT TO bifrost_app WITH CHECK (true);
```

---

## 4. Hash Chain

```go
// framework/auditstore/hasher.go

func ComputeEntryHash(entry *AuditLogTable) string {
    // Hash covers all fields EXCEPT EntryHash itself
    h := sha256.New()
    fmt.Fprintf(h, "%d|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%v",
        entry.Sequence, entry.Timestamp.UnixNano(),
        entry.ActorID, entry.ActorName, entry.ActorRole,
        entry.Action, entry.Resource, entry.ResourceID,
        entry.OldValue, entry.NewValue,
        entry.PrevHash, entry.Success,
    )
    return hex.EncodeToString(h.Sum(nil))
}

func (s *AuditStore) Append(ctx context.Context, entry *AuditLogTable) error {
    return s.db.Transaction(func(tx *gorm.DB) error {
        // Get last entry hash (serialized via TX)
        var last AuditLogTable
        if err := tx.Order("sequence DESC").Limit(1).First(&last).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
            return err
        }
        entry.PrevHash = last.EntryHash
        if entry.PrevHash == "" {
            entry.PrevHash = "genesis"
        }
        entry.EntryHash = ComputeEntryHash(entry)
        return tx.Create(entry).Error
    })
}
```

---

## 5. AuditStore Interface

```go
// framework/auditstore/store.go

type AuditStore interface {
    Append(ctx context.Context, entry *AuditLogTable) error
    Search(ctx context.Context, filters AuditSearchFilters, page PaginationOptions) (*AuditSearchResult, error)
    GetByID(ctx context.Context, id string) (*AuditLogTable, error)
    VerifyChainIntegrity(ctx context.Context, from, to int64) (bool, []int64, error)  // returns invalid sequences
    Export(ctx context.Context, filters AuditSearchFilters, format ExportFormat, w io.Writer) error
    GetStats(ctx context.Context, filters AuditSearchFilters) (*AuditStats, error)
    Close() error
}

type AuditSearchFilters struct {
    ActorIDs    []string
    Actions     []string
    Resources   []string
    ResourceIDs []string
    StartTime   *time.Time
    EndTime     *time.Time
    Success     *bool
    SearchText  string
    SortBy      string
    Order       string
}

type ExportFormat string
const (
    ExportJSON ExportFormat = "json"
    ExportCSV  ExportFormat = "csv"
)
```

---

## 6. Audit Interceptor Middleware

```go
// transports/bifrost-http/lib/audit_interceptor.go

// AuditMiddleware wraps a management handler and writes an audit entry after execution
func AuditMiddleware(resource, action string, auditStore auditstore.AuditStore) Middleware {
    return func(h fasthttp.RequestHandler) fasthttp.RequestHandler {
        return func(ctx *fasthttp.RequestCtx) {
            start := time.Now()
            
            // Capture request body snapshot (for OldValue computation)
            bodySnapshot := string(ctx.Request.Body())
            
            // Execute handler
            h(ctx)
            
            // Build audit entry asynchronously (non-blocking)
            entry := &auditstore.AuditLogTable{
                ID:           uuid.New().String(),
                Timestamp:    start,
                ActorID:      sessionUserID(ctx),
                ActorName:    sessionUsername(ctx),
                ActorRole:    sessionRole(ctx),
                ActorIP:      ctx.RemoteAddr().String(),
                Action:       action,
                Resource:     resource,
                ResourceID:   extractResourceID(ctx),
                NewValue:     bodySnapshot,
                StatusCode:   ctx.Response.StatusCode(),
                Success:      ctx.Response.StatusCode() < 400,
                RequestID:    requestID(ctx),
                UserAgent:    string(ctx.Request.Header.UserAgent()),
            }
            
            go func() {
                if err := auditStore.Append(context.Background(), entry); err != nil {
                    // Log warning — never fail the request due to audit failure
                    logger.Warn("audit log write failed", "error", err)
                }
            }()
        }
    }
}
```

---

## 7. Handler

```go
// transports/bifrost-http/handlers/audit.go

// GET /api/audit/logs?actor_id=&action=&resource=&start_time=&end_time=&limit=&offset=
func (h *AuditHandler) SearchLogs(ctx *fasthttp.RequestCtx)

// GET /api/audit/logs/{id}
func (h *AuditHandler) GetLog(ctx *fasthttp.RequestCtx)

// GET /api/audit/verify?from_seq=&to_seq=
func (h *AuditHandler) VerifyChain(ctx *fasthttp.RequestCtx)

// GET /api/audit/export?format=json|csv&start_time=&end_time=
// Streams file download — sets Content-Disposition: attachment
func (h *AuditHandler) Export(ctx *fasthttp.RequestCtx)

// GET /api/audit/stats
func (h *AuditHandler) GetStats(ctx *fasthttp.RequestCtx)
```

---

## 8. Route Registration

```go
// Routes require "admin" role minimum; export requires "super_admin"
router.GET("/api/audit/logs",      ChainMiddlewares(auditHandler.SearchLogs, AuthMiddleware, RBACMiddleware("audit", "read")))
router.GET("/api/audit/logs/{id}", ChainMiddlewares(auditHandler.GetLog,     AuthMiddleware, RBACMiddleware("audit", "read")))
router.GET("/api/audit/verify",    ChainMiddlewares(auditHandler.VerifyChain, AuthMiddleware, RBACMiddleware("audit", "admin")))
router.GET("/api/audit/export",    ChainMiddlewares(auditHandler.Export,      AuthMiddleware, RBACMiddleware("audit", "admin")))
router.GET("/api/audit/stats",     ChainMiddlewares(auditHandler.GetStats,    AuthMiddleware, RBACMiddleware("audit", "read")))
```

---

## 9. Management Endpoints to Audit

Apply `AuditMiddleware` to all state-changing management routes:

| Route | Resource | Action |
|-------|----------|--------|
| `POST /api/providers` | `provider` | `create` |
| `PUT /api/providers/{id}` | `provider` | `update` |
| `DELETE /api/providers/{id}` | `provider` | `delete` |
| `POST /api/governance/virtual-keys` | `virtual_key` | `create` |
| `PUT /api/governance/virtual-keys/{id}` | `virtual_key` | `update` |
| `DELETE /api/governance/virtual-keys/{id}` | `virtual_key` | `delete` |
| `POST /api/users` | `user` | `create` |
| `POST /api/users/{id}/roles` | `user_role` | `assign` |
| `DELETE /api/users/{id}/roles/{rid}` | `user_role` | `revoke` |
| `PUT /api/config` | `config` | `update` |
| `POST /api/session/login` | `session` | `login` |
| `POST /api/session/logout` | `session` | `logout` |

---

## 10. Migration

```go
func migrateV4AuditLog(db *gorm.DB) error {
    if err := db.AutoMigrate(&tables.AuditLogTable{}); err != nil {
        return err
    }
    // Apply append-only triggers
    if db.Dialector.Name() == "sqlite" {
        return applySQLiteAuditTriggers(db)
    }
    return applyPostgresAuditRLS(db)
}
```

---

## 11. UI Components

```
ui/app/enterprise/audit/
├── page.tsx              — Audit log search & list view
├── [id]/page.tsx         — Single entry detail
└── components/
    ├── AuditLogTable.tsx — Filterable, sortable log table
    ├── ChainVerifier.tsx — Visual integrity verification tool
    └── ExportPanel.tsx   — Date range export to JSON/CSV
```

---

## 12. Performance Notes

- Hash chain computation is inside a DB transaction (serialized). For PostgreSQL at scale, use a dedicated `audit_writer` connection pool with `SERIALIZABLE` isolation.
- Export streams directly to `io.Writer` — never buffers full result set in memory.
- Index on `(resource, action, timestamp)` for common compliance query patterns.
