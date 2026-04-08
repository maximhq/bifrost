# Technical Design Document: Bifrost Test Architecture

**Version:** 1.0  
**Status:** Active  
**Source:** Phân tích mã nguồn thực tế tại `tests/`  
**Last Updated:** 2026-04-08

---

## 1. Tổng Quan Kiến Trúc Test

Bifrost có **4 tầng test độc lập** với công nghệ và mục đích khác nhau:

```
┌─────────────────────────────────────────────────────────────────┐
│                    BIFROST TEST PYRAMID                          │
│                                                                  │
│   ┌─────────────────────────────────────────────────────────┐   │
│   │  E2E UI Tests (Playwright TypeScript)                    │   │
│   │  tests/e2e/  ─── UI flows toàn phần qua trình duyệt    │   │
│   └─────────────────────────────────────────────────────────┘   │
│                                                                  │
│   ┌─────────────────────────────────────────────────────────┐   │
│   │  API Integration Tests (Newman/Postman)                  │   │
│   │  tests/api/  ─── REST API contract + feature flows       │   │
│   └─────────────────────────────────────────────────────────┘   │
│                                                                  │
│   ┌─────────────────────────────────────────────────────────┐   │
│   │  Governance E2E Tests (Go testing)                       │   │
│   │  tests/governance/  ─── Budget/Rate limit behavior       │   │
│   └─────────────────────────────────────────────────────────┘   │
│                                                                  │
│   ┌─────────────────────────────────────────────────────────┐   │
│   │  SDK Integration Tests (Python pytest + TS vitest)       │   │
│   │  tests/integrations/  ─── SDK compatibility drop-in      │   │
│   └─────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
```

---

## 2. Tầng 1: E2E UI Tests (`tests/e2e/`)

### 2.1 Công nghệ

| Thành phần | Công nghệ | Phiên bản |
|-----------|----------|-----------|
| Framework | Playwright | Latest |
| Ngôn ngữ | TypeScript | 5.x |
| Browser | Chromium (Desktop Chrome) | Latest |
| Reporter | HTML + List (stdout) | Built-in |
| Package Manager | npm | - |

### 2.2 Cấu trúc thư mục

```
tests/e2e/
├── playwright.config.ts           # Cấu hình toàn cục
├── global-setup.ts                # Setup/teardown toàn bộ suite
├── package.json                   # Dependencies
├── tsconfig.json
├── core/                          # Shared infrastructure
│   ├── index.ts                   # Re-export tất cả public APIs
│   ├── fixtures/
│   │   ├── base.fixture.ts        # Custom test fixture (extend Playwright test)
│   │   └── test-data.fixture.ts   # Factory functions cho test data
│   ├── pages/
│   │   ├── base.page.ts           # BasePage class — common UI actions
│   │   └── sidebar.page.ts        # Sidebar navigation page object
│   ├── actions/
│   │   ├── api.ts                 # REST API helpers (providers, VKs, teams, customers)
│   │   └── navigation.ts          # Navigation actions
│   └── utils/
│       ├── selectors.ts           # Centralized selector constants
│       └── test-helpers.ts        # Utility functions
└── features/                      # Test suites theo feature
    ├── config/                    # System config tests
    ├── dashboard/                 # Dashboard & metrics tests
    ├── governance/                # Governance UI tests
    ├── logs/                      # LLM log viewer tests
    ├── mcp-auth-config/           # MCP auth configuration
    ├── mcp-logs/                  # MCP execution log tests
    ├── mcp-registry/              # MCP client registry tests
    ├── mcp-settings/              # MCP settings tests
    ├── mcp-tool-groups/           # Tool groups management
    ├── model-limits/              # Model limit configuration
    ├── observability/             # Observability connectors tests
    ├── placeholders/              # Placeholder tests
    ├── plugins/                   # Plugin management tests
    ├── providers/                 # Provider CRUD tests
    ├── routing-rules/             # Routing rules tests
    └── virtual-keys/              # Virtual key management tests
```

### 2.3 Playwright Configuration

**File:** `tests/e2e/playwright.config.ts`

```typescript
// 3 project groups với dependency order:
projects: [
  // Group 1: Parallel tests (dashboard, logs, governance, observability...)
  { name: 'chromium', fullyParallel: true },

  // Group 2: Serial tests (plugins, virtual-keys, mcp-registry, model-limits, providers)
  // Phải chạy serial vì các test này modify shared state
  { name: 'chromium-serial', fullyParallel: false },

  // Group 3: Config tests - PHẢI chạy SAU groups 1 & 2
  // Config changes ảnh hưởng toàn hệ thống
  { name: 'chromium-config', dependencies: ['chromium', 'chromium-serial'] }
]
```

**Timeouts:**
- Action timeout: 10,000ms
- Navigation timeout: 30,000ms
- Test timeout: 60,000ms
- Expect timeout: 10,000ms
- Web server startup: 120,000ms

**Retry policy:**
- CI: 2 retries
- Local: 0 retries

**Artifacts on failure:** Screenshot + Video + Trace

### 2.4 Global Setup (`global-setup.ts`)

Global setup thực hiện **4 bước bắt buộc** trước khi bắt đầu bất kỳ test nào:

```
globalSetup()
  ├── 1. runPluginSetup()
  │      └── make build-test-plugin  →  /tmp/bifrost-test-plugin.so
  │
  ├── 2. runMCPSetup()
  │      ├── Build HTTP/SSE server (Go): examples/mcps/http-no-ping-server/
  │      │   └── ./http-server  (port 3001)
  │      ├── Build STDIO server (Node): examples/mcps/test-tools-server/
  │      │   └── npm run build
  │      └── Verify servers healthy via HTTP POST /mcp (JSON-RPC initialize)
  │
  ├── 3. runBifrostMCPAndResponsesSetup()  [chỉ khi BIFROST_BASE_URL set]
  │      ├── waitForBifrostAPI()  →  GET /health
  │      ├── ensureTestClient001()
  │      │   ├── GET /api/mcp/clients  →  tìm "TestClient001"
  │      │   ├── Nếu không có: POST /api/mcp/client  (HTTP connection, port 3001)
  │      │   └── Nếu disconnected: POST /api/mcp/client/{id}/reconnect
  │      └── seedLLMLogs(count=30)
  │          └── 30 x POST /v1/chat/completions  (batch of 5 parallel)
  │
  └── Returns teardown: runMCPTeardown()
         └── SIGTERM tất cả MCP server processes
```

**Environment variables:**
| Variable | Default | Mô tả |
|---------|---------|------|
| `BASE_URL` | `http://localhost:3000` | UI URL |
| `BIFROST_BASE_URL` | (not set) | API URL — khi set, triggers MCP+seed setup |
| `SKIP_WEB_SERVER` | (not set) | Skip auto-start Next.js dev server |
| `SEED_MODEL` | `openai/gpt-4o-mini` | Model cho LLM log seeding |
| `CI` | (not set) | Enables stricter CI mode (forbidOnly, 1 worker) |

### 2.5 Page Object Pattern

**BasePage** cung cấp các common actions:

```typescript
class BasePage {
  // Toast assertions
  getToast(type?: 'success' | 'error' | 'loading' | 'default'): Locator
  async waitForSuccessToast(message?: string): Promise<void>
  async waitForErrorToast(message?: string): Promise<void>
  async dismissToasts(): Promise<void>

  // Navigation & loading
  async waitForPageLoad(): Promise<void>         // networkidle
  async waitForChartsToLoad(): Promise<void>     // networkidle + skeleton gone
  async waitForSheetAnimation(): Promise<void>   // dialog opacity===1

  // Form interactions
  async fillByLabel(label: string, value: string)
  async fillByPlaceholder(placeholder: string, value: string)
  async fillByTestId(testId: string, value: string)
  async clickButton(text: string)
  async clickByTestId(testId: string)
  async selectOption(label: string, value: string)

  // Debug
  async closeDevProfiler(): Promise<void>   // Đóng Dev Profiler overlay nếu hiển thị
}
```

### 2.6 API Actions (Test Setup/Cleanup)

**File:** `tests/e2e/core/actions/api.ts`

```typescript
// Helper modules cho từng resource
providersApi    → GET/POST/PUT/DELETE /api/providers
virtualKeysApi  → GET/POST/PUT/DELETE /api/governance/virtual-keys
teamsApi        → GET/POST/DELETE /api/governance/teams
customersApi    → GET/POST/DELETE /api/governance/customers

// Cleanup helper (xóa VKs → teams → customers → providers theo thứ tự)
cleanupTestData(request, { virtualKeyIds, teamIds, customerIds, providerNames })
```

### 2.7 Anti-patterns (Được tài liệu hóa)

| ❌ Anti-pattern | ✅ Thay thế |
|---------------|-----------|
| `page.waitForTimeout(2000)` | `page.waitForLoadState('networkidle')` |
| `{ force: true }` | Fix root cause (scroll, dismiss toast first) |
| `.locator('..')` chained selectors | `data-testid` attributes |
| Static test data names | Sử dụng `Date.now()` timestamp |
| Conditional assertions (`count >= 0`) | Deterministic assertions |
| Thiếu cleanup | Delete resources sau assertions |

---

## 3. Tầng 2: API Tests (`tests/e2e/api/`)

### 3.1 Công nghệ

| Thành phần | Công nghệ |
|-----------|----------|
| Framework | Newman (CLI runner cho Postman) |
| Collections | Postman Collection v2.1 JSON |
| DB Verification | Custom newman-reporter-dbverify |
| Environment | `bifrost-v1.postman_environment.json` |

### 3.2 Collections (15 collections)

| Collection | Nội dung | Requests |
|-----------|---------|---------|
| `bifrost-v1-complete` | Full API coverage (chat, embed, responses) | ~Large |
| `bifrost-openai-integration` | OpenAI API compatibility | ~65KB |
| `bifrost-anthropic-integration` | Anthropic API compatibility | ~28KB |
| `bifrost-bedrock-integration` | AWS Bedrock API compatibility | ~39KB |
| `bifrost-composite-integrations` | Multi-provider scenarios | ~33KB |
| `bifrost-api-management` | Provider/VK/Team CRUD | ~49KB |
| `bifrost-v1-inference-features` | Streaming, tools, vision | ~63KB |
| `bifrost-v1-streaming` | SSE streaming | ~3KB |
| `bifrost-v1-fallbacks` | Fallback routing | ~5KB |
| `bifrost-v1-rate-limit` | Rate limiting enforcement | ~7KB |
| `bifrost-v1-vk-auth` | VK authentication | ~21KB |
| `bifrost-v1-vk-routing` | VK-based routing | ~7KB |
| `bifrost-v1-mgmt-flows` | Management workflow flows | ~20KB |
| `bifrost-v1-session` | Session management | ~5KB |
| `bifrost-v1-async` | Async/batch operations | ~10KB |

### 3.3 Provider Capabilities Matrix

File `provider-capabilities.json` (~24KB) định nghĩa capabilities của từng provider:
- Supported request types (chat, embed, image, speech, etc.)
- Feature flags (streaming, tools, vision, etc.)
- Model lists per provider

**Dùng bởi:** Newman runner để skip tests không applicable với provider cụ thể.

### 3.4 Setup Scripts

```bash
# Setup MCP client trước khi chạy API tests
./tests/e2e/api/setup-mcp.sh

# Setup plugin trước khi chạy plugin-dependent tests
./tests/e2e/api/setup-plugin.sh
```

---

## 4. Tầng 3: Governance Tests (`tests/governance/`)

### 4.1 Công nghệ

| Thành phần | Công nghệ |
|-----------|----------|
| Framework | Go `testing` package (standard library) |
| HTTP Client | `net/http` |
| Target | `http://localhost:8080` (hardcoded) |
| Parallelism | `t.Parallel()` trên mọi test |

### 4.2 Test Files

| File | Test Coverage |
|------|--------------|
| `e2e_test.go` | Budget hierarchy, shared team budgets, inactive VK, rate limit boundary (49KB) |
| `ratelimit_test.go` | Token limits, request limits, provider-level limits, in-memory sync (30KB) |
| `ratelimitenforcement_test.go` | Budget+rate limit enforcement mechanics (18KB) |
| `advancedscenarios_test.go` | Complex multi-entity scenarios (48KB) |
| `configupdatesync_test.go` | Config changes propagate to in-memory store (37KB) |
| `inmemorysync_test.go` | In-memory store consistency (17KB) |
| `usagetracking_test.go` | Budget/token usage tracking (17KB) |
| `customer_virtual_keys_response_test.go` | Customer+VK response format (16KB) |
| `customerbudget_test.go` | Customer-level budget enforcement (12KB) |
| `teambudget_test.go` | Team-level budget enforcement (5KB) |
| `vkbudget_test.go` | VK-level budget enforcement (4KB) |
| `providerbudget_test.go` | Provider-level budget enforcement (8KB) |
| `edgecases_test.go` | Edge cases và boundary conditions (6KB) |
| `test_utils.go` | Shared types, helpers, fixtures (17KB) |

**Tổng:** ~290KB Go test code

### 4.3 Key Data Types (`test_utils.go`)

```go
// Request/Response tropic
type APIRequest struct { Method, Path string; Body interface{}; VKHeader *string }
type APIResponse struct { StatusCode int; Body map[string]interface{}; RawBody []byte }

// Resource creation types
type CreateVirtualKeyRequest struct {
  Name, Description string
  IsActive *bool
  TeamID, CustomerID *string
  Budget *BudgetRequest
  RateLimit *CreateRateLimitRequest
  ProviderConfigs []ProviderConfigRequest
}
type BudgetRequest struct { MaxLimit float64; ResetDuration string }
type CreateRateLimitRequest struct {
  TokenMaxLimit *int64; TokenResetDuration *string
  RequestMaxLimit *int64; RequestResetDuration *string
}

// GlobalTestData: resource tracker với cleanup
type GlobalTestData struct {
  VirtualKeys []string; Teams []string; Customers []string
}
func (g *GlobalTestData) Cleanup(t *testing.T)  // delete với retry 5x
```

### 4.4 Test Utilities

```go
// HTTP helpers
MakeRequest(t, req APIRequest) *APIResponse
MakeRequestWithCustomHeaders(t, req, headers) *APIResponse

// Assertions
ExtractIDFromResponse(t, resp) string           // Navigate: virtual_key|team|customer → id
CheckErrorMessage(t, resp, expectedText) bool   // Case-insensitive body search

// Async helpers
WaitForCondition(t, checkFunc, timeout, desc) bool
WaitForAPICondition(t, req, condition, timeout, desc) (*APIResponse, bool)

// Cleanup
deleteWithRetry(t, path, type, id)  // 5 retries với progressive backoff 100ms→500ms

// Cost calculation
CalculateCost(model, inputTokens, outputTokens) (float64, error)
```

### 4.5 Cost Model

```go
TestModels = {
  "openai/gpt-4o":               { input: $0.0000025, output: $0.00001 },
  "anthropic/claude-3-7-sonnet": { input: $0.000003,  output: $0.000015 },
  "anthropic/claude-4-opus":     { input: $0.000015,  output: $0.000075 },
  "openrouter/anthropic/claude": { input: $0.000003,  output: $0.000015 },
  "openrouter/openai/gpt-4o":   { input: $0.0000025, output: $0.00001 },
}
```

### 4.6 Test Patterns

**Pattern 1: Budget Depletion**
```go
// Tạo entity → gửi requests cho đến khi budget bị từ chối → verify với từ khóa "budget"
for requestNum <= 150 {
  resp := MakeRequest(t, APIRequest{...VKHeader: &vkValue})
  if resp.StatusCode >= 400 {
    if CheckErrorMessage(t, resp, "budget") { break }
    t.Fatalf("unexpected error")
  }
  // tính cost, cộng dồn
}
```

**Pattern 2: State Verification via In-Memory API**
```go
// Gửi request → sleep 2s → query /api/governance/*?from_memory=true
time.Sleep(2 * time.Second) // PostHook goroutine async
getResp := MakeRequest(t, APIRequest{ Path: "/api/governance/budgets?from_memory=true" })
budgetsMap := getResp.Body["budgets"].(map[string]interface{})
budgetData := budgetsMap[budgetID].(map[string]interface{})
usage := budgetData["current_usage"].(float64)
```

**Pattern 3: Hierarchical Budget Enforcement**
```go
// Customer($1000) → Team($100) → VK($0.1) → Provider($0.01 = MOST RESTRICTIVE)
// Provider budget hit first → blocked với "budget" error
// Verify: consumedBudget >= providerBudget before rejection
```

**Pattern 4: Rate Limit Reset**
```go
// req1 OK → req2 rejected → sleep(resetDuration + 1s) → req3 OK
startTime := time.Now()
// ... make requests ...
waitTime := time.Until(startTime.Add(16 * time.Second))
time.Sleep(waitTime)
resp3 := MakeRequest(...)
```

### 4.7 Run Commands

```bash
# Run tất cả governance tests
make test-governance

# Run individual test
go test ./tests/governance/... -run TestMultipleVKsSharingTeamBudgetFairness -v

# Run với timeout
go test ./tests/governance/... -timeout 10m -v
```

---

## 5. Tầng 4: SDK Integration Tests (`tests/integrations/`)

### 5.1 Python SDK Tests

**Location:** `tests/integrations/python/`  
**Framework:** pytest  
**Runner:** `run_all_tests.py` hoặc `run_integration_tests.py`  
**Python version:** `.python-version` file

**Test suites:**

| File | SDK | Provider | Size |
|------|-----|---------|------|
| `test_openai.py` | OpenAI Python SDK | OpenAI | 179KB |
| `test_anthropic.py` | Anthropic Python SDK | Anthropic | 158KB |
| `test_google.py` | Google GenAI SDK | Google | 151KB |
| `test_azure.py` | OpenAI SDK (Azure mode) | Azure | 100KB |
| `test_bedrock.py` | Boto3 (AWS SDK) | Bedrock | 82KB |
| `test_langchain.py` | LangChain | OpenAI + Anthropic | 60KB |
| `test_litellm.py` | LiteLLM | OpenAI | 35KB |
| `test_pydanticai.py` | PydanticAI | - | 30KB |

**Required env vars per integration:**
```
openai:    OPENAI_API_KEY
anthropic: ANTHROPIC_API_KEY
litellm:   OPENAI_API_KEY
langchain: OPENAI_API_KEY, ANTHROPIC_API_KEY
google:    GOOGLE_API_KEY
bedrock:   AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY
```

**Run commands:**
```bash
# Tất cả integrations
python run_all_tests.py

# Specific integration
python run_all_tests.py --integration openai

# Parallel (max 3 workers)
python run_all_tests.py --parallel

# Verbose + specific
python run_all_tests.py --integration google --verbose

# List trạng thái env
python run_all_tests.py --list
```

**Makefile target:**
```bash
make test-integrations-py
```

### 5.2 TypeScript SDK Tests

**Location:** `tests/integrations/typescript/`  
**Framework:** Vitest  
**Config:** `vitest.config.ts`

**Test files:**

| File | SDK | Provider | Size |
|------|-----|---------|------|
| `test-openai.test.ts` | openai npm package | OpenAI | 75KB |
| `test-anthropic.test.ts` | @anthropic-ai/sdk | Anthropic | 75KB |
| `test-langchain.test.ts` | LangChain JS | Multi | 31KB |
| `test-azure.test.ts` | openai (Azure) | Azure | 51KB |
| `test-google.test.ts` | @google/generative-ai | Google | 25KB |
| `test-bedrock.test.ts` | @aws-sdk/client-bedrock | Bedrock | 23KB |

**Run command:**
```bash
make test-integrations-ts
```

### 5.3 Test Setup (`conftest.py`, `setup.ts`)

```python
# conftest.py — pytest fixtures
@pytest.fixture
def bifrost_client():
    """OpenAI client pointing to Bifrost"""
    return openai.OpenAI(
        api_key="bifrost-key",
        base_url=os.getenv("BIFROST_BASE_URL", "http://localhost:8080") + "/v1"
    )
```

---

## 6. Infrastructure (`tests/docker-compose.yml`)

### 6.1 Services

| Service | Image | Port | Dùng cho |
|---------|-------|------|---------|
| `weaviate` | `semitechnologies/weaviate:1.32.4` | 9000→8080 | Vector store tests |
| `redis-stack` | `redis/redis-stack:7.4.0-v6` | 6379, 8001 | Redis vector store tests |
| `qdrant` | `qdrant/qdrant:v1.16.0` | 6334 (gRPC) | Qdrant vector store tests |
| `pinecone-local` | `ghcr.io/pinecone-io/pinecone-index` | 5081 | Pinecone vector store tests |
| `postgres` | `postgres:16-alpine` | 5432 | ConfigStore + encryption |

### 6.2 Network Configuration

```yaml
networks:
  bifrost_network:
    driver: bridge
    ipam: { subnet: 172.28.0.0/16, gateway: 172.28.0.1 }

# Static IPs:
weaviate:       172.28.0.12
redis-stack:    172.28.0.13
qdrant:         172.28.0.14
pinecone-local: 172.28.0.15
postgres:       172.28.0.16
```

### 6.3 Governance Test Config

File `tests/governance/config.json`:
- Bifrost config dùng cho governance test suite
- Chứa provider definitions và mock configurations

---

## 7. Load Testing (`tests/load_test_parameter_ordering.sh`)

**File:** `tests/load_test_parameter_ordering.sh` (27KB)

Script bash kiểm tra **parameter ordering** cho load tests. Đây là regression script đảm bảo thứ tự parameter trong requests không ảnh hưởng đến hành vi.

---

## 8. Scripts (`tests/scripts/`)

| Directory | Nội dung |
|-----------|---------|
| `1millogs/` | Script sinh 1 triệu LLM log entries cho stress testing |
| `migration-checker/` | Kiểm tra database migration compatibility |

---

## 9. Makefile Test Targets (từ root `Makefile`)

```bash
# Core LLM tests (live API calls)
make test-core                               # Tất cả providers
make test-core PROVIDER=openai               # Specific provider
make test-core PROVIDER=openai TESTCASE=... # Specific test case
make test-core DEBUG=1                       # Với Delve debugger :2345

# MCP/Agent tests (mock-based, không cần live API)
make test-mcp                                # Tất cả MCP tests
make test-mcp TESTCASE=TestAgentLoop        # Specific test
make test-mcp TYPE=agent                    # By category (agent|tool|connection|codemode)

# Plugin tests
make test-plugins                            # Tất cả plugins
make test-governance                         # Governance plugin tests

# SDK integration tests
make test-integrations-py                    # Python SDK tests
make test-integrations-ts                    # TypeScript SDK tests

# E2E UI tests
make run-e2e                                 # Tất cả E2E flows
make run-e2e FLOW=providers                 # Specific flow
make run-e2e-headed                         # Visible browser mode
make run-e2e-ui                             # Playwright UI mode
```

---

## 10. Test Data & State Management

### 10.1 Governance Tests — Resource Lifecycle

```
Test Start
  ├── testData := NewGlobalTestData()     // Empty tracker
  ├── defer testData.Cleanup(t)           // Guaranteed cleanup
  ├── Create resource → testData.AddVirtualKey(id)
  ├── ... test logic ...
  └── Cleanup() runs on exit
        ├── DELETE /api/governance/virtual-keys/{id}  (retry 5x, progressive backoff)
        ├── DELETE /api/governance/teams/{id}
        └── DELETE /api/governance/customers/{id}
```

### 10.2 E2E Tests — Resource Naming

```typescript
// Tất cả E2E tests dùng timestamp để tránh collision
const name = `provider-${Date.now()}`
const vkName = `vk-test-${Date.now()}`

// Cleanup trong afterEach/test body
await cleanupTestData(request, {
  virtualKeyIds: [vkId],
  providerNames: [providerName]
})
```

### 10.3 In-Memory Store Verification

Governance tests verify state bằng cách query internal in-memory store qua API:

```
GET /api/governance/virtual-keys?from_memory=true   → Virtual key state
GET /api/governance/budgets?from_memory=true        → Budget usage
GET /api/governance/rate-limits?from_memory=true    → Rate limit usage
```

**Timing consideration:** Do budget/rate limit updates xảy ra trong async PostHook goroutine, tests phải `time.Sleep(2 * time.Second)` sau request trước khi verify in-memory state.

---

## 11. CI/CD Integration

### 11.1 E2E Tests — GitHub Actions

```yaml
# .github/workflows/e2e-tests.yml
# Runs each FLOW in separate job in parallel
# Triggers: push/PR khi ui/, tests/e2e/, hoặc workflow file thay đổi
# Manual trigger: Actions → E2E Tests → Run workflow
#   Optional input: comma-separated flows (e.g., providers,config,plugins)
```

**Worker configuration:**
- CI: 1 worker per job (stability over speed)
- Local: unlimited parallel workers

### 11.2 Test Reports

| Test Layer | Report Type | Location |
|-----------|-----------|---------|
| E2E UI | Playwright HTML | `tests/e2e/playwright-report/` |
| API | Newman HTML | (output dir tùy config) |
| Governance | `go test -v` output | stdout |
| Python SDK | pytest output | stdout |
| TS SDK | Vitest output | stdout |

---

## 12. Dependency Graph cho Test Execution

```
                      ┌─────────────────────┐
                      │  Bifrost HTTP server  │ :8080
                      │  (make dev / binary)  │
                      └──────────┬──────────┘
                                 │
         ┌───────────────────────┼───────────────────────┐
         │                       │                       │
         ▼                       ▼                       ▼
┌────────────────┐   ┌───────────────────┐   ┌──────────────────────┐
│ governance/    │   │ integrations/     │   │ e2e/                 │
│ (Go tests)     │   │ (Python/TS SDK)   │   │ (Playwright)         │
│                │   │                   │   │                      │
│ Cần:           │   │ Cần:              │   │ Cần:                 │
│ - Bifrost :8080│   │ - Bifrost :8080   │   │ - Next.js UI :3000   │
│ - Live API keys│   │ - Live API keys   │   │ - Bifrost API :8080  │
│                │   │                   │   │ - MCP server :3001   │
│ Run: go test   │   │ Run: pytest/vitest│   │ - Test Plugin .so    │
└────────────────┘   └───────────────────┘   └──────────────────────┘
         │
         ▼
┌────────────────────┐
│ docker-compose.yml │
│ - weaviate :9000   │
│ - redis :6379      │
│ - qdrant :6334     │
│ - pinecone :5081   │
│ - postgres :5432   │
└────────────────────┘
```

---

## 13. Các Điểm Quan Trọng Cho Developer

### 13.1 Governance Tests — Budget Enforcement Behavior

> **CRITICAL:** Budget enforcement là **POST-HOC**.  
> Request gây ra budget vượt quá giới hạn vẫn **được phép** hoàn thành.  
> Request **tiếp theo** mới bị từ chối.

```go
// Correct expectation:
if consumedBudget >= providerBudget {
  shouldStop = true  // Next request sẽ bị block
}
// Request SAU ĐÂY bị reject với "budget" error
```

### 13.2 E2E Tests — Config Feature Ordering

Config tests phải chạy **sau cùng** vì config changes ảnh hưởng toàn hệ thống:
```typescript
{ name: 'chromium-config', dependencies: ['chromium', 'chromium-serial'] }
```

### 13.3 E2E Tests — Serial vs Parallel

Các features sau **phải chạy serial** (modify shared state):
- `plugins` — Plugin CRUD ảnh hưởng middleware chain
- `virtual-keys` — VK creation/deletion có in-memory side effects  
- `mcp-registry` — MCP client registration là shared state
- `model-limits` — Model limits áp dụng globally
- `providers` — Provider CRUD ảnh hưởng routing

### 13.4 In-Memory State Lag

Sau khi gọi management API (create/update), cần wait cho in-memory propagation:
```go
time.Sleep(500 * time.Millisecond)  // Management config changes
time.Sleep(2 * time.Second)          // Budget/rate-limit async PostHook
```

### 13.5 Test Cleanup Order (VK → Team → Customer)

```
Luôn xóa theo thứ tự:
1. Virtual Keys (dependent on teams/customers)
2. Teams (dependent on customers)
3. Customers (root entity)
```

---

## 14. Checklist Thêm Test Mới

### E2E UI Test

- [ ] Tạo file trong đúng `features/<feature>/` directory
- [ ] Import từ `../../core` (không import trực tiếp từ Playwright)  
- [ ] Dùng `data-testid` selectors, không dùng text/class selectors
- [ ] Tên test data có timestamp: `${Date.now()}`
- [ ] Cleanup resources trong test body (tránh test pollution)
- [ ] Không dùng `waitForTimeout()` — dùng semantic waits
- [ ] Kiểm tra feature có cần `chromium-serial` không (modify shared state)

### Governance Go Test

- [ ] `t.Parallel()` ở đầu mỗi test function
- [ ] `testData := NewGlobalTestData()` + `defer testData.Cleanup(t)`
- [ ] Sau POST/PUT management requests: `time.Sleep(500ms)` cho in-memory sync
- [ ] Sau inference requests + muốn verify budget: `time.Sleep(2s)` vì async PostHook
- [ ] Dùng `generateRandomID()` cho names để tránh collision
- [ ] Verify budget enforcement là post-hoc (1 request được phép vượt, request sau bị chặn)

### SDK Integration Test

- [ ] Kiểm tra env var trước: `os.getenv("OPENAI_API_KEY")`  
- [ ] Target `BIFROST_BASE_URL` không phải OpenAI trực tiếp
- [ ] Test cả streaming và non-streaming responses
- [ ] Verify response schema match OpenAI API spec
