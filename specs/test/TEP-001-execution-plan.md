# Test Execution Plan — Bifrost v2.0 Enterprise

**Document ID:** TEP-001  
**Version:** 1.0 | **Date:** 2026-04-08  
**Reference:** TR-001, TD-001  
**Status:** READY

---

## 1. Execution Strategy

### 1.1 Sprint Test Cycle

```
Sprint 0 (Foundation)
  ├── TC-013 License Enforcement         ← Gate all enterprise suites
  └── TC-001 Inference API baseline

Sprint 1 (Security & Identity)
  ├── TC-004 RBAC
  ├── TC-005 Audit Logs
  ├── TC-008 SSO/SCIM
  └── TC-UG  User Groups

Sprint 2 (Content Safety)
  ├── TC-006 Guardrails
  └── TC-007 PII Redactor

Sprint 3 (Core OSS)
  ├── TC-002 Provider Management
  ├── TC-003 Governance
  └── TC-005b Observability

Sprint 4 (Intelligence & Scale)
  ├── TC-009 Adaptive Routing
  ├── TC-010 Clustering
  └── TC-015 Performance

Sprint 5 (Integration & Ecosystem)
  ├── TC-011 Alert Channels
  ├── TC-012 HashiCorp Vault
  └── TC-014 MCP Tool Groups & Connectors
```

---

## 2. Test Run Order (Per PR / Per Release)

### Level 1 — Fast Gate (~5 min) — Runs on Every PR

```bash
# Must pass before any merge
make test-core PROVIDER=mock           # Unit: provider converters
make test-mcp                          # Unit: MCP agent logic
go test ./tests/integration/license/... # License: TC-013-001
go test ./tests/integration/rbac/...   # RBAC: TC-004-001, -002, -003
go test ./tests/integration/inference/... -short  # TC-001 basics
```

**Pass criteria:** 100% pass rate. Any failure blocks merge.

---

### Level 2 — Integration Gate (~15 min) — Runs on main merge

```bash
# Full integration test against dockerized infra
docker-compose -f tests/docker-compose.yml up -d
go test ./tests/integration/... -timeout 10m
```

**Suites included:**
- TC-001 (all 25 cases)
- TC-002 (all 18 cases)
- TC-003 (all 20 cases)
- TC-004 (all 20 cases)
- TC-005 (all 15 cases)
- TC-006 (all 18 cases)
- TC-007 (all 14 cases)
- TC-013 (all 12 cases)

**Pass criteria:** ≥ 98% pass rate; 0 Critical/Blocker failures.

---

### Level 3 — Full E2E (~30 min) — Runs nightly + pre-release

```bash
# Playwright E2E
make run-e2e

# Includes:
# - TC-004 RBAC UI flows
# - TC-008 SSO login (OIDC browser flow)
# - TC-006 Guardrails admin UI
# - TC-013 License feature gating in UI
```

---

### Level 4 — Performance (~60 min) — Runs weekly + pre-release

```bash
cd tests/performance
k6 run baseline.js         # TC-015-001
k6 run ramp.js             # TC-015-002
k6 run streaming.js        # TC-015-003
k6 run spike.js            # TC-015-009
```

---

### Level 5 — Security Scan (~20 min) — Pre-release only

```bash
gosec ./...                 # Static analysis
govulncheck ./...           # Vulnerability check
# OWASP ZAP scan on staging
```

---

## 3. Environment Setup Scripts

### 3.1 Local Integration Test Setup

```bash
#!/bin/bash
# tests/scripts/setup-local.sh

# Start infrastructure
docker-compose -f tests/docker-compose.yml up -d postgres redis wiremock

# Wait for healthy
sleep 5

# Run DB migrations
BIFROST_DB_URL=postgres://test:test@localhost:5432/bifrost_test \
  ./bifrost-http migrate

# Seed test data
go run tests/scripts/seed.go

# Set enterprise test license
export BIFROST_LICENSE_KEY="$(cat tests/fixtures/licenses/enterprise_test.jwt)"

echo "Test environment ready ✓"
```

### 3.2 k6 Environment Variables

```bash
# tests/performance/.env.test
BASE_URL=http://localhost:8080
API_USER_TOKEN=tok_api_user_test_klmno
ADMIN_TOKEN=tok_admin_test_67890
PROVIDER_ID=provider_mock_openai
VK_UNLIMITED=vk_unlimited_test
```

---

## 4. Test Data Seeding Specification

### 4.1 Database Seed Script (`tests/scripts/seed.go`)

**Users:**
```go
users := []User{
    {ID: "usr_super",    Email: "superadmin@test.com",   Role: "super_admin"},
    {ID: "usr_admin",    Email: "admin@test.com",         Role: "admin"},
    {ID: "usr_operator", Email: "operator@test.com",      Role: "operator"},
    {ID: "usr_viewer",   Email: "viewer@test.com",         Role: "viewer"},
    {ID: "usr_api",      Email: "api@test.com",            Role: "api_user"},
}
```

**Sessions (static tokens for testing):**
```go
sessions := map[string]string{
    "tok_super_admin_test_12345": "usr_super",
    "tok_admin_test_67890":       "usr_admin",
    "tok_operator_test_abcde":    "usr_operator",
    "tok_viewer_test_fghij":      "usr_viewer",
    "tok_api_user_test_klmno":    "usr_api",
}
```

**Providers:**
```go
providers := []Provider{
    {Name: "mock-openai",   BaseURL: "http://localhost:9090", Keys: []Key{{Value: "sk-mock"}}, Type: "openai"},
    {Name: "provider-fast", BaseURL: "http://localhost:9091", Keys: []Key{{Value: "sk-fast"}}, Type: "openai"},
    {Name: "provider-slow", BaseURL: "http://localhost:9092", Keys: []Key{{Value: "sk-slow"}}, Type: "openai"},
}
```

**Virtual Keys:**
```go
virtualKeys := []VirtualKey{
    {ID: "vk_unlimited",        Name: "unlimited",        Budget: nil,  RateLimit: nil},
    {ID: "vk_tight_budget",     Name: "tight-budget",     Budget: &Budget{Max: 0.001, Currency: "USD"}},
    {ID: "vk_rate_limited",     Name: "rate-limited",     RateLimit: &RateLimit{RPM: 1}},
    {ID: "vk_expired",          Name: "expired",          ExpiresAt: time.Date(2020,1,1,0,0,0,0,time.UTC)},
    {ID: "vk_model_restricted", Name: "model-restricted", AllowedModels: []string{"gpt-4o-mini"}},
}
```

---

## 5. Docker Compose (Test Infrastructure)

```yaml
# tests/docker-compose.yml
version: "3.8"

services:
  postgres:
    image: postgres:15
    environment:
      POSTGRES_USER: test
      POSTGRES_PASSWORD: test
      POSTGRES_DB: bifrost_test
    ports:
      - "5432:5432"
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "test"]
      interval: 5s
      retries: 5

  redis:
    image: redis:7
    ports:
      - "6379:6379"

  wiremock:
    image: wiremock/wiremock:3.3.1
    ports:
      - "8088:8080"
    volumes:
      - ./fixtures/wiremock:/home/wiremock

  mock-llm:
    build:
      context: .
      dockerfile: mocks/Dockerfile.llm
    ports:
      - "9090:9090"   # provider-fast
      - "9091:9091"   # provider-slow (200ms delay)
      - "9092:9092"   # provider-error (50% fail rate)

  vault:
    image: hashicorp/vault:1.15
    environment:
      VAULT_DEV_ROOT_TOKEN_ID: "dev-root"
      VAULT_DEV_LISTEN_ADDRESS: "0.0.0.0:8200"
    ports:
      - "8200:8200"
    cap_add:
      - IPC_LOCK
```

---

## 6. CI/CD Pipeline Integration

```yaml
# .github/workflows/test.yml (excerpt)

jobs:
  unit-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v4
        with: { go-version: '1.26' }
      - run: make test-core
      - run: make test-mcp
      - run: make test-plugins

  integration-tests:
    runs-on: ubuntu-latest
    needs: unit-tests
    services:
      postgres:
        image: postgres:15
        env: { POSTGRES_PASSWORD: test }
      redis:
        image: redis:7
    steps:
      - run: go test ./tests/integration/... -timeout 15m

  e2e-tests:
    runs-on: ubuntu-latest
    needs: integration-tests
    if: github.event_name == 'push' && github.ref == 'refs/heads/main'
    steps:
      - run: make dev &
      - run: make run-e2e

  performance-tests:
    runs-on: ubuntu-latest
    needs: integration-tests
    if: github.event_name == 'schedule'  # weekly
    steps:
      - run: |
          k6 run tests/performance/baseline.js \
            --out json=k6-results.json
      - uses: actions/upload-artifact@v4
        with:
          name: k6-results
          path: k6-results.json
```

---

## 7. Defect Tracking Template

```markdown
## Bug Report Template

**Bug ID:** BUG-{NNN}
**Suite:** TC-{SUITE}-{NNN}
**Severity:** Critical | Blocker | Major | Minor | Trivial
**Status:** Open | In Progress | Fixed | Verified | Closed

**Title:** {Short description}

**Steps to Reproduce:**
1. ...

**Actual Result:** ...
**Expected Result:** (from test case)

**Environment:**
- Bifrost version: ...
- Go version: ...
- OS: ...

**Logs / Screenshots:**
```

---

## 8. Test Sign-Off Checklist (Pre-Release)

```
Release Version: v2.0.0-enterprise
Test Lead: _______________
Date: _______________

P0 Test Suites:
[ ] TC-013 License — All 12 cases PASSED
[ ] TC-004 RBAC — All 20 cases PASSED
[ ] TC-005 Audit Logs — All 15 cases PASSED (0 bypass possible)
[ ] TC-006 Guardrails — All 18 cases PASSED
[ ] TC-007 PII Redactor — All 14 cases PASSED
[ ] TC-001 Inference API — All 25 cases PASSED
[ ] TC-003 Governance — All 20 cases PASSED

P1 Test Suites:
[ ] TC-008 SSO/SCIM — All 20 cases PASSED
[ ] TC-009 Adaptive Routing — All 12 cases PASSED
[ ] TC-010 Clustering — All 10 cases PASSED
[ ] TC-011 Alerts — All 14 cases PASSED
[ ] TC-012 Vault — All 10 cases PASSED
[ ] TC-002 Provider Management — All 18 cases PASSED
[ ] TC-015 Performance — All 10 scenarios PASSED (targets met)

P2 Test Suites:
[ ] TC-014 MCP Tool Groups & Connectors — ≥ 80% PASSED
[ ] TC-UG User Groups — ≥ 80% PASSED

Security:
[ ] gosec scan — 0 High severity findings
[ ] govulncheck — 0 Critical CVEs
[ ] Manual RBAC bypass test — No bypass found
[ ] PII logging audit — 0 PII found in log samples

Performance Targets:
[ ] Overhead P99 ≤ 11µs at 5,000 RPS
[ ] 1,000 concurrent streams handled
[ ] Failover ≤ 200ms
[ ] Memory stable over 1 hour

Sign-off: _______________   Date: _______________
```
