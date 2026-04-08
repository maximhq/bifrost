# TASK-004 — PII Redactor

**Feature:** PII Redaction  
**TECH Spec:** [TECH-004-pii-redactor.md](../TECH-004-pii-redactor.md)  
**Phase:** 2 (Content Safety)  
**Depends on:** TASK-014 (license), TASK-003 (Guardrails — ordering context)  
**Estimate:** 5 days  
**Assignee:** —  
**Status:** 🟢 Completed

---

## Context

PII Redactor operates as an `LLMPlugin` between the Guardrails plugin and the Logging plugin.

**CRITICAL execution order:**
```
guardrails.PreLLMHook → pii_redactor.PreLLMHook → [LLM call] → pii_redactor.PostLLMHook → guardrails.PostLLMHook → logging.PostLLMHook
```

This order guarantees:
1. Guardrails see original content (for pattern matching)
2. PII is redacted before it reaches the LLM (if configured) AND before it reaches the logging plugin
3. Logs never contain raw PII

**Redaction modes:**
- `mask` — replace with `[REDACTED_TYPE]` (e.g., `[REDACTED_EMAIL]`)
- `hash` — replace with SHA-256 hash (reversible with key)
- `tokenize` — replace with stable UUID token (reversible via token store)
- `partial` — partial masking (e.g., `john****@example.com`, `****-****-****-1234`)

---

## Tasks

### TASK-004-01 — Database schema + GORM migration

**Files to create:**
- `framework/configstore/tables/pii.go` — `PIIPolicyTable`, `PIIDetectorRuleTable`, `PIITokenStoreTable`
- Migration file

**Schema:**
```go
type PIIPolicyTable struct {
    ID              string    `gorm:"primaryKey;type:text"`
    Name            string    `gorm:"uniqueIndex;not null"`
    Enabled         bool      `gorm:"default:true"`
    Scope           string    // "global"|"virtual_key"|"user_group"
    ScopeID         string
    RedactInput     bool      `gorm:"default:true"`   // redact before sending to LLM
    RedactOutput    bool      `gorm:"default:false"`   // redact LLM response
    LogViolations   bool      `gorm:"default:true"`    // audit-log PII detections
    CreatedAt       time.Time
    UpdatedAt       time.Time
}

type PIIDetectorRuleTable struct {
    ID          string    `gorm:"primaryKey;type:text"`
    PolicyID    string    `gorm:"index;not null"`
    EntityType  string    // "email"|"phone"|"ssn"|"credit_card"|"name"|"address"|"ip_address"|"custom"
    Mode        string    // "mask"|"hash"|"tokenize"|"partial"
    Pattern     string    `gorm:"type:text"` // custom regex; empty = built-in detector
    Enabled     bool      `gorm:"default:true"`
}

type PIITokenStoreTable struct {
    Token       string    `gorm:"primaryKey;type:text"` // UUID
    EntityType  string    `gorm:"index"`
    OriginalHash string   // SHA-256 of original value (for dedup)
    CreatedAt   time.Time
    ExpiresAt   *time.Time
}
```

**Acceptance criteria:**
- [ ] Migration runs cleanly; idempotent on re-run
- [ ] `PIITokenStoreTable` has index on `OriginalHash` for dedup lookups

---

### TASK-004-02 — `plugins/piiredactor` Go module scaffold

**Files to create:**
- `plugins/piiredactor/go.mod`
- `plugins/piiredactor/plugin.go` — `PIIRedactorPlugin` implementing `LLMPlugin`
- `plugins/piiredactor/config.go` — `PIIRedactorConfig`
- `plugins/piiredactor/detectors/` — one file per entity type
- `plugins/piiredactor/redactor.go` — orchestrator (runs all detectors, applies mode)
- `plugins/piiredactor/tokenstore.go` — in-memory + DB token store

**Acceptance criteria:**
- [ ] Plugin registered in `go.work`
- [ ] `PreLLMHook` redacts PII from all user messages in `BifrostChatRequest`
- [ ] `PostLLMHook` optionally redacts PII from assistant messages (when `RedactOutput=true`)
- [ ] Original content stored in `BifrostContext` (for PostLLMHook restoration if needed)

---

### TASK-004-03 — Built-in PII detectors

**Files to create (one per entity type):**
- `plugins/piiredactor/detectors/email.go`
- `plugins/piiredactor/detectors/phone.go`
- `plugins/piiredactor/detectors/ssn.go`
- `plugins/piiredactor/detectors/credit_card.go`
- `plugins/piiredactor/detectors/name.go` (NER using regex heuristics + common name list)
- `plugins/piiredactor/detectors/ip_address.go`
- `plugins/piiredactor/detectors/custom.go` (user-defined regex)

**Acceptance criteria per detector:**

| Entity | Pattern | Must Detect | Must Not Flag |
|--------|---------|-------------|---------------|
| email | RFC 5321 regex | `user@example.com` | plain words |
| phone | E.164 + common US formats | `+1-800-555-0100` | ZIP codes |
| SSN | `\d{3}-\d{2}-\d{4}` | `123-45-6789` | dates like `123-45` |
| credit_card | Luhn-validated 13-19 digit | `4111111111111111` | phone numbers |
| ip_address | IPv4 + IPv6 | `192.168.1.1` | versions like `1.2.3.4.5` |

- [ ] Each detector has unit tests with true positive and false positive cases
- [ ] Custom regex detector supports named capture groups for partial masking

---

### TASK-004-04 — Redaction mode implementations

**File:** `plugins/piiredactor/redactor.go`

**Acceptance criteria:**
- [ ] `mask` mode: replaces match with `[REDACTED_EMAIL]` (uppercase entity type)
- [ ] `hash` mode: replaces with `[HASH:abc123def456]` (first 12 chars of SHA-256)
- [ ] `tokenize` mode: replaces with `[TOKEN:uuid-v4]`; token stored in `PIITokenStoreTable`; same value → same token (dedup via hash)
- [ ] `partial` mode: 
  - email: `u***@example.com`
  - credit card: keep last 4 digits `****-****-****-1234`
  - phone: keep last 4 digits `***-***-0100`
  - SSN: keep last 4 `***-**-6789`
- [ ] All replacements preserve surrounding text position offsets

---

### TASK-004-05 — Policy management API

**Files to create:**
- `transports/bifrost-http/handlers/pii.go`

**Endpoints:**
```
GET    /api/pii/policies            — list policies (admin+)
POST   /api/pii/policies            — create policy (admin+)
GET    /api/pii/policies/{id}       — get policy + rules
PUT    /api/pii/policies/{id}       — update policy (admin+)
DELETE /api/pii/policies/{id}       — delete policy (admin+)
POST   /api/pii/policies/{id}/test  — test policy against sample text; returns redacted output

GET    /api/pii/tokens/{token}      — de-tokenize (admin+ only, audit-logged)
DELETE /api/pii/tokens/expired      — cleanup expired tokens (operator+)
```

**Acceptance criteria:**
- [ ] All endpoints require `pii_redactor` feature enabled (license check)
- [ ] `POST /api/pii/policies/{id}/test` returns both original and redacted text side-by-side
- [ ] De-tokenization endpoint is audit-logged with requester identity

---

### TASK-004-06 — Virtual key policy attachment

**Files to modify:**
- `framework/configstore/tables/governance.go` — `VirtualKeysTable.PIIPolicyID *string`
- `transports/bifrost-http/handlers/governance.go` — VK CRUD accepts `pii_policy_id`

**Acceptance criteria:**
- [ ] VK create/update accepts `pii_policy_id`
- [ ] Plugin loads correct PII policy for active VK

---

### TASK-004-07 — UI: PII redaction policy editor

**Files to create:**
- `ui/app/enterprise/pii-redactor/page.tsx`
- `ui/app/enterprise/pii-redactor/[id]/page.tsx`
- `ui/app/enterprise/pii-redactor/components/EntityTypeToggle.tsx`
- `ui/app/enterprise/pii-redactor/components/RedactionModeSelector.tsx`
- `ui/app/enterprise/pii-redactor/components/TestPanel.tsx`

**Acceptance criteria:**
- [ ] Per-entity-type toggle (email, phone, SSN, credit card, etc.)
- [ ] Per-entity redaction mode selector (mask/hash/tokenize/partial)
- [ ] Test panel: input text → shows redacted output with highlighted replacements
- [ ] Page inside `<EnterpriseGate feature="pii_redactor">`

---

## Definition of Done

- [ ] All subtasks complete
- [ ] Unit tests: each detector with true/false positive cases
- [ ] Unit tests: each redaction mode produces correct output
- [ ] Integration test: email in user message → redacted before LLM call
- [ ] Integration test: PII Redactor runs AFTER Guardrails and BEFORE Logging (verified by middleware order test)
- [ ] `POST /api/pii/policies/{id}/test` returns correct redacted diff
- [ ] `make build` passes
