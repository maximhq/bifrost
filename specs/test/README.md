# Bifrost AI Gateway — Test Documentation Index

**Project:** Bifrost v2.0 — Enterprise Edition  
**Test Lead:** QA Team  
**Date:** 2026-04-08  
**SRS Reference:** SRS v2.0 | PRD | URD  

---

## Document Structure

```
specs/test/
├── README.md                        ← This file — master index
├── TR-001-test-requirements.md      ← Test Requirements (what must be tested)
├── TD-001-test-design.md            ← Test Design Strategy (how to test)
├── TDD-001-unit-tests.md            ← TDD: Unit Test Specifications
├── TDD-002-integration-tests.md     ← TDD: Integration Test Specifications
├── TC-001-inference-api.md          ← Test Cases: Unified Inference API
├── TC-002-provider-management.md    ← Test Cases: Provider CRUD & Key Management
├── TC-003-governance.md             ← Test Cases: Virtual Keys, Budgets, Rate Limits
├── TC-004-rbac.md                   ← Test Cases: Role-Based Access Control
├── TC-005-audit-logs.md             ← Test Cases: Immutable Audit Logs
├── TC-006-guardrails.md             ← Test Cases: Content Guardrails
├── TC-007-pii-redactor.md           ← Test Cases: PII Detection & Redaction
├── TC-008-sso-scim.md               ← Test Cases: SSO / SCIM 2.0
├── TC-009-adaptive-routing.md       ← Test Cases: Adaptive Routing Engine
├── TC-010-clustering.md             ← Test Cases: Multi-Node Clustering
├── TC-011-alerts.md                 ← Test Cases: Alert Channels
├── TC-012-vault.md                  ← Test Cases: HashiCorp Vault Integration
├── TC-013-license.md                ← Test Cases: License Enforcement
├── TC-014-mcp.md                    ← Test Cases: MCP Tool Groups & Connectors
└── TC-015-performance.md            ← Test Cases: Performance & Scale
```

---

## Test Traceability Matrix (Summary)

| ID | Test Suite | SRS Section | Priority | Cases |
|----|-----------|-------------|----------|-------|
| TC-001 | Inference API | §3.1 | P0 | 25 |
| TC-002 | Provider Management | §3.2, §3.3 | P0 | 18 |
| TC-003 | Governance (VK/Budget/RL) | §3.4 | P0 | 22 |
| TC-004 | RBAC | §3.12 | P0 | 20 |
| TC-005 | Audit Logs | §3.13 | P0 | 15 |
| TC-006 | Guardrails | §3.14 | P0 | 18 |
| TC-007 | PII Redactor | §3.15 | P0 | 14 |
| TC-008 | SSO / SCIM | §3.16 | P1 | 20 |
| TC-009 | Adaptive Routing | §3.17 | P1 | 12 |
| TC-010 | Clustering | §3.18 | P1 | 10 |
| TC-011 | Alert Channels | §3.19 | P1 | 14 |
| TC-012 | Vault | §3.20 | P1 | 10 |
| TC-013 | License Enforcement | §3.25 | P0 | 12 |
| TC-014 | MCP Tool Groups & Connectors | §3.22, §3.24 | P2 | 16 |
| TC-015 | Performance & Scale | §4 NFR | P1 | 10 |
| **TOTAL** | | | | **236** |

---

## Test Environment Requirements

| Environment | Purpose | Config |
|-------------|---------|--------|
| `dev-unit` | Unit tests | No external deps, mocked providers |
| `dev-integration` | Integration tests | Real PostgreSQL, Redis; mocked LLM |
| `staging` | E2E tests | Full stack, sandbox LLM API keys |
| `perf` | Performance tests | k6 + Prometheus, 5,000 RPS target |

---

## Test ID Convention

```
TC-{suite}-{sequence}
Examples:
  TC-004-001   RBAC suite, test case #1
  TC-006-012   Guardrails suite, test case #12
```

**Status values:** `DRAFT` | `READY` | `EXECUTED` | `PASSED` | `FAILED` | `BLOCKED`
