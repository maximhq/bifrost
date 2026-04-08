# Enterprise Feature Tasks — Master Index

**Sprint:** Enterprise v2.0  
**Source Specs:** `/specs/changes/TECH-001` through `TECH-014`  
**Task Convention:** `TASK-{TECH_ID}-{SEQ}` (e.g., `TASK-001-01`)

---

## Execution Order (Dependency Graph)

```
Phase 0 — Foundation (no dependencies)
  TASK-014-*  License Enforcement        ← gates everything else

Phase 1 — Security Core (depends on Phase 0)
  TASK-001-*  RBAC                       ← required by all admin APIs
  TASK-002-*  Audit Logs                 ← required before logging plugin changes
  TASK-005-*  SSO / SCIM                 ← requires RBAC (roles)
  TASK-012-*  User Groups                ← requires RBAC + SSO

Phase 2 — Content Safety (depends on Phase 0)
  TASK-003-*  Guardrails
  TASK-004-*  PII Redactor               ← must run before logging plugin

Phase 3 — Infrastructure (depends on Phase 0)
  TASK-007-*  Clustering                 ← Postgres + Redis shared state
  TASK-008-*  Vault Integration          ← depends on framework/encrypt

Phase 4 — Intelligence (depends on Phase 1 + 3)
  TASK-006-*  Adaptive Routing           ← requires metrics from logstore
  TASK-009-*  Alert Channels             ← requires metrics + governance
  TASK-011-*  MCP Tool Groups            ← requires RBAC + User Groups
  TASK-013-*  Data Connectors            ← requires MCP Tool Groups

Phase 5 — Performance (parallel with Phase 4)
  TASK-010-*  Large Payload Optimization
```

---

## Task Files

| File | Feature | Phase | Status |
|------|---------|-------|--------|
| [TASK-014-license.md](TASK-014-license.md) | License Enforcement | 0 | 🔴 Not Started |
| [TASK-001-rbac.md](TASK-001-rbac.md) | RBAC | 1 | 🔴 Not Started |
| [TASK-002-audit-logs.md](TASK-002-audit-logs.md) | Audit Logs | 1 | 🔴 Not Started |
| [TASK-005-sso-scim.md](TASK-005-sso-scim.md) | SSO / SCIM | 1 | 🔴 Not Started |
| [TASK-012-user-groups.md](TASK-012-user-groups.md) | User Groups | 1 | 🔴 Not Started |
| [TASK-003-guardrails.md](TASK-003-guardrails.md) | Guardrails | 2 | 🔴 Not Started |
| [TASK-004-pii-redactor.md](TASK-004-pii-redactor.md) | PII Redactor | 2 | 🔴 Not Started |
| [TASK-007-clustering.md](TASK-007-clustering.md) | Clustering | 3 | 🔴 Not Started |
| [TASK-008-vault.md](TASK-008-vault.md) | Vault Integration | 3 | 🔴 Not Started |
| [TASK-006-adaptive-routing.md](TASK-006-adaptive-routing.md) | Adaptive Routing | 4 | 🔴 Not Started |
| [TASK-009-alerts.md](TASK-009-alerts.md) | Alert Channels | 4 | 🔴 Not Started |
| [TASK-011-mcp-tool-groups.md](TASK-011-mcp-tool-groups.md) | MCP Tool Groups | 4 | 🔴 Not Started |
| [TASK-013-connectors.md](TASK-013-connectors.md) | Data Connectors | 4 | 🔴 Not Started |
| [TASK-010-large-payload.md](TASK-010-large-payload.md) | Large Payload | 5 | 🔴 Not Started |

---

## Status Legend

| Symbol | Meaning |
|--------|---------|
| 🔴 Not Started | Task not yet picked up |
| 🟡 In Progress | Actively being worked on |
| 🟢 Done | PR merged, tests passing |
| ⏸ Blocked | Waiting on dependency |

---

## Critical Cross-Cutting Rules

1. **License Gate** — Every enterprise feature handler/plugin MUST call `license.IsFeatureEnabled("feature_name")` before executing. See `TASK-014`.
2. **Plugin Order** — Guardrails → PII Redactor → Logging. Never change this order.
3. **GORM Migrations** — All new tables require a migration file in `framework/configstore/migrations/`.
4. **Audit Log** — All state-changing admin API operations (POST/PUT/DELETE) must write an audit log entry.
5. **Pool Reset** — Any pooled object must have ALL fields reset before `pool.Put()`.
