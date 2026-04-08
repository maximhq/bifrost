# TASK-003 — Guardrails

**Feature:** Guardrails (Content Moderation)  
**TECH Spec:** [TECH-003-guardrails.md](../TECH-003-guardrails.md)  
**Phase:** 2 (Content Safety)  
**Depends on:** TASK-014 (license)  
**Estimate:** 6 days  
**Assignee:** —  
**Status:** 🟢 Completed

---

## Context

Guardrails implement a multi-layer content moderation pipeline executed as an `LLMPlugin`.

**Plugin execution order (CRITICAL):**
```
guardrails (PreLLMHook) → pii_redactor (PreLLMHook) → [LLM call] → pii_redactor (PostLLMHook) → guardrails (PostLLMHook) → logging (PostLLMHook)
```

**Guardrail check layers (in order):**
1. Keyword blocklist (regex/exact match, < 1ms)
2. Topic classifier (rule-based, < 5ms)
3. AI content moderator (async LLM call via Bifrost, < 200ms)
4. Custom policy engine (user-defined rules, < 10ms)

**Short-circuit behavior:** If any layer fires, the request is blocked immediately without proceeding to subsequent layers.

---

## Tasks

### TASK-003-01 — Database schema + GORM migration

**Files to create:**
- `framework/configstore/tables/guardrails.go` — `GuardrailPolicyTable`, `GuardrailRuleTable`, `GuardrailViolationTable`
- Migration file

**Schema:**
```go
type GuardrailPolicyTable struct {
    ID          string    `gorm:"primaryKey;type:text"`
    Name        string    `gorm:"uniqueIndex;not null"`
    Enabled     bool      `gorm:"default:true"`
    Scope       string    // "global"|"virtual_key"|"user_group"
    ScopeID     string    // VK ID or user group ID; empty = global
    Action      string    // "block"|"warn"|"log_only"
    LayersJSON  string    `gorm:"type:text"` // JSON: which layers to enable
    CreatedAt   time.Time
    UpdatedAt   time.Time
}

type GuardrailRuleTable struct {
    ID         string    `gorm:"primaryKey;type:text"`
    PolicyID   string    `gorm:"index;not null"`
    RuleType   string    // "keyword"|"regex"|"topic"|"custom"
    Pattern    string    `gorm:"type:text"` // regex pattern or keyword list
    TopicName  string    // for topic rules
    Direction  string    // "input"|"output"|"both"
    Severity   string    // "low"|"medium"|"high"|"critical"
    Enabled    bool      `gorm:"default:true"`
}

type GuardrailViolationTable struct {
    ID          string    `gorm:"primaryKey;type:text"`
    Timestamp   time.Time `gorm:"index"`
    RequestID   string    `gorm:"index"`
    PolicyID    string    `gorm:"index"`
    RuleID      string    `gorm:"index"`
    VirtualKeyID string   `gorm:"index"`
    Layer       string    // "keyword"|"topic"|"ai"|"custom"
    Direction   string    // "input"|"output"
    Action      string    // "blocked"|"warned"|"logged"
    Pattern     string    // matched pattern (redacted if sensitive)
    ModelUsed   string
}
```

**Acceptance criteria:**
- [ ] Migration runs cleanly; re-run is idempotent
- [ ] All indexes created as specified

---

### TASK-003-02 — `plugins/guardrails` Go module scaffold

**Files to create:**
- `plugins/guardrails/go.mod` — new Go module `github.com/maximhq/bifrost/plugins/guardrails`
- `plugins/guardrails/plugin.go` — `GuardrailsPlugin` implementing `LLMPlugin`
- `plugins/guardrails/config.go` — `GuardrailsConfig`
- `plugins/guardrails/layers/keyword.go` — keyword/regex checker
- `plugins/guardrails/layers/topic.go` — topic classifier
- `plugins/guardrails/layers/ai.go` — AI moderator (calls Bifrost)
- `plugins/guardrails/layers/custom.go` — custom policy engine
- `plugins/guardrails/violation_writer.go` — async violation logger

**Acceptance criteria:**
- [ ] `GuardrailsPlugin` registered in `go.work`
- [ ] `plugin.go` implements `PreLLMHook` and `PostLLMHook`
- [ ] `PreLLMHook` runs checks against request content (user messages)
- [ ] `PostLLMHook` runs checks against response content (assistant messages)
- [ ] Returns `LLMPluginShortCircuit` with HTTP 400 payload on block

---

### TASK-003-03 — Keyword/regex layer

**File:** `plugins/guardrails/layers/keyword.go`

**Acceptance criteria:**
- [ ] Loads keyword blocklist patterns from `GuardrailRuleTable` on startup + hot-reload on policy update
- [ ] Supports exact match and regex patterns
- [ ] Case-insensitive matching option
- [ ] Performance: < 1ms for 1000-word blocklist against 2KB message
- [ ] Returns which pattern matched in violation record

---

### TASK-003-04 — Topic classifier layer

**File:** `plugins/guardrails/layers/topic.go`

**Topics to implement (rule-based, no ML dependency):**
- `violence`, `hate_speech`, `adult_content`, `self_harm`, `illegal_activity`, `pii_leak`, `jailbreak`

**Jailbreak detection patterns to implement:**
- "ignore previous instructions" variants
- Role-play prompt injection ("pretend you are", "act as DAN")
- Base64/hex encoded instruction attempts

**Acceptance criteria:**
- [ ] Each topic has a curated pattern list (minimum 10 patterns per topic)
- [ ] `Direction` respected: input-only rules don't block output
- [ ] Performance: < 5ms for all topic checks against 4KB message

---

### TASK-003-05 — AI moderator layer

**File:** `plugins/guardrails/layers/ai.go`

Uses Bifrost itself to call a configurable moderation model (e.g., `openai/omni-moderation-latest`):

**Acceptance criteria:**
- [ ] Moderation API call timeout configurable (default: 5s)
- [ ] Falls back to allow (fail open) on timeout/error, logs warning
- [ ] Response parsed: `flagged: true` + categories → block decision
- [ ] AI moderator result cached by content hash (TTL: 5 minutes) to avoid redundant calls
- [ ] Layer only active when configured model is specified in policy

---

### TASK-003-06 — Policy management API

**Files to create:**
- `transports/bifrost-http/handlers/guardrails.go`

**Endpoints:**
```
GET    /api/guardrails/policies           — list policies (admin+)
POST   /api/guardrails/policies           — create policy (admin+)
GET    /api/guardrails/policies/{id}      — get policy + rules
PUT    /api/guardrails/policies/{id}      — update policy (admin+)
DELETE /api/guardrails/policies/{id}      — delete policy (admin+)
POST   /api/guardrails/policies/{id}/test — test policy against sample text

GET    /api/guardrails/policies/{id}/rules       — list rules
POST   /api/guardrails/policies/{id}/rules       — add rule
DELETE /api/guardrails/policies/{id}/rules/{rid} — remove rule

GET /api/guardrails/violations?policy_id=&start=&end=&page=  — violation history
GET /api/guardrails/stats  — violations per policy, per layer, per action (last 7 days)
```

**Acceptance criteria:**
- [ ] All endpoints require `guardrails` feature enabled (license check)
- [ ] `POST /api/guardrails/policies/{id}/test` runs all layers against provided text and returns which layers fired
- [ ] Policy enable/disable is audit-logged

---

### TASK-003-07 — Virtual key policy attachment

**Files to modify:**
- `transports/bifrost-http/handlers/governance.go` — add guardrail policy to VK CRUD
- `framework/configstore/tables/governance.go` — `VirtualKeysTable.GuardrailPolicyID *string`

**Acceptance criteria:**
- [ ] VK create/update accepts `guardrail_policy_id` field
- [ ] Guardrail plugin loads correct policy for the active VK in context

---

### TASK-003-08 — UI: policy editor

**Files to create:**
- `ui/app/enterprise/guardrails/page.tsx`
- `ui/app/enterprise/guardrails/[id]/page.tsx`
- `ui/app/enterprise/guardrails/components/PolicyCard.tsx`
- `ui/app/enterprise/guardrails/components/RuleEditor.tsx`
- `ui/app/enterprise/guardrails/components/ViolationChart.tsx`
- `ui/app/enterprise/guardrails/components/TestPanel.tsx`

**Acceptance criteria:**
- [ ] Policy list with enable/disable toggle per policy
- [ ] Rule editor: add keyword/regex/topic rules with direction and severity
- [ ] Test panel: textarea input → shows which layers triggered
- [ ] Violation chart: bar chart of violations per day for last 7 days

---

## Definition of Done

- [ ] All subtasks complete
- [ ] Unit tests for keyword, topic, and AI moderator layers (mock AI calls)
- [ ] Integration test: `PreLLMHook` blocks request containing blocklist keyword → 400 response
- [ ] Integration test: `PostLLMHook` blocks response containing `violence` topic patterns
- [ ] Performance test: keyword check < 1ms, full pipeline (no AI layer) < 10ms
- [ ] Plugin executes BEFORE PII Redactor in plugin chain (verified by test)
- [ ] `make build` passes
