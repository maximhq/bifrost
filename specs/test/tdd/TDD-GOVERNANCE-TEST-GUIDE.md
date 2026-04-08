# TDD: Governance Integration Test Guide

**Version:** 1.0  
**Status:** Active  
**Source:** Phân tích trực tiếp `tests/governance/`  
**Áp dụng cho:** Developers thêm Go-based governance tests

---

## 1. Tổng Quan

Governance tests là **Go integration tests** chạy trực tiếp với live Bifrost server. Chúng test behavior của budget enforcement, rate limiting, và virtual key lifecycle — không phải UI, mà là hành vi API.

```
tests/governance/
├── test_utils.go                          # Core utilities, types, helpers
├── e2e_test.go                            # Budget hierarchy, VK lifecycle (49KB)
├── ratelimit_test.go                      # Token/request rate limits (30KB)
├── ratelimitenforcement_test.go           # Rate limit enforcement mechanics (18KB)
├── advancedscenarios_test.go              # Multi-entity complex flows (48KB)
├── configupdatesync_test.go               # Config propagation to in-memory (37KB)
├── inmemorysync_test.go                   # In-memory store consistency (17KB)
├── usagetracking_test.go                  # Budget/token usage tracking (17KB)
├── customer_virtual_keys_response_test.go # Customer+VK response shapes (16KB)
├── customerbudget_test.go                 # Customer budget enforcement (12KB)
├── teambudget_test.go                     # Team budget enforcement (5KB)
├── vkbudget_test.go                       # VK budget enforcement (4KB)
├── providerbudget_test.go                 # Provider budget enforcement (8KB)
├── edgecases_test.go                      # Boundary conditions (6KB)
└── config.json                            # Bifrost config for test env
```

---

## 2. Types và Interfaces

### 2.1 API Communication Types

```go
// APIRequest — gói request vào helper function
type APIRequest struct {
    Method   string       // HTTP method: GET, POST, PUT, DELETE
    Path     string       // URL path: "/api/governance/virtual-keys"
    Body     interface{}  // Request body (sẽ được JSON encode)
    VKHeader *string      // Nếu set, gửi header "x-bf-vk: <value>"
}

// APIResponse — kết quả từ helper
type APIResponse struct {
    StatusCode int
    Body       map[string]interface{}  // JSON response
    RawBody    []byte
}
```

### 2.2 Resource Creation Types

```go
// Virtual Key
type CreateVirtualKeyRequest struct {
    Name            string                  `json:"name"`
    Description     string                  `json:"description,omitempty"`
    IsActive         *bool                   `json:"is_active,omitempty"`
    TeamID          *string                 `json:"team_id,omitempty"`
    CustomerID       *string                 `json:"customer_id,omitempty"`
    Budget          *BudgetRequest          `json:"budget,omitempty"`
    RateLimit       *CreateRateLimitRequest `json:"rate_limit,omitempty"`
    ProviderConfigs []ProviderConfigRequest  `json:"provider_configs,omitempty"`
}

type UpdateVirtualKeyRequest struct {
    Name            *string                 `json:"name,omitempty"`
    IsActive        *bool                   `json:"is_active,omitempty"`
    Budget          *BudgetRequest          `json:"budget,omitempty"`
    RateLimit       *CreateRateLimitRequest `json:"rate_limit,omitempty"`
    ProviderConfigs []ProviderConfigRequest  `json:"provider_configs,omitempty"`
}

// Budget
type BudgetRequest struct {
    MaxLimit      float64 `json:"max_limit"`
    ResetDuration string  `json:"reset_duration"`
}

// Rate Limit
type CreateRateLimitRequest struct {
    TokenMaxLimit        *int64  `json:"token_max_limit,omitempty"`
    TokenResetDuration   *string `json:"token_reset_duration,omitempty"`
    RequestMaxLimit      *int64  `json:"request_max_limit,omitempty"`
    RequestResetDuration *string `json:"request_reset_duration,omitempty"`
}

// Provider Config (trong VK)
type ProviderConfigRequest struct {
    Provider  string                  `json:"provider"`
    Weight    float64                 `json:"weight"`
    Budget    *BudgetRequest          `json:"budget,omitempty"`
    RateLimit *CreateRateLimitRequest `json:"rate_limit,omitempty"`
}

// Chat Completions (inference)
type ChatCompletionRequest struct {
    Model    string        `json:"model"`
    Messages []ChatMessage `json:"messages"`
    Stream   bool          `json:"stream,omitempty"`
}

type ChatMessage struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}
```

### 2.3 Resource Tracking

```go
// GlobalTestData tracks all created resources for cleanup
type GlobalTestData struct {
    VirtualKeys []string  // IDs của VKs đã tạo
    Teams       []string  // IDs của teams
    Customers   []string  // IDs của customers
}

func NewGlobalTestData() *GlobalTestData {
    return &GlobalTestData{}
}

func (g *GlobalTestData) AddVirtualKey(id string) { g.VirtualKeys = append(g.VirtualKeys, id) }
func (g *GlobalTestData) AddTeam(id string)       { g.Teams = append(g.Teams, id) }
func (g *GlobalTestData) AddCustomer(id string)   { g.Customers = append(g.Customers, id) }

// Cleanup: xóa VKs → Teams → Customers theo dependency order
// Retry 5 lần với progressive backoff: 100ms, 200ms, 300ms, 400ms, 500ms
func (g *GlobalTestData) Cleanup(t *testing.T) { ... }
```

---

## 3. Helper Functions

### 3.1 HTTP Helper

```go
// MakeRequest — gửi request và parse response
func MakeRequest(t *testing.T, req APIRequest) *APIResponse {
    baseURL := "http://localhost:8080"  // hardcoded
    url := baseURL + req.Path
    
    // JSON encode body
    // Set Content-Type: application/json
    // Set x-bf-vk header nếu VKHeader != nil
    // HTTP client với 30s timeout
    
    return &APIResponse{
        StatusCode: resp.StatusCode,
        Body:       jsonBody,
        RawBody:    rawBody,
    }
}

// MakeRequestWithCustomHeaders — request với headers tùy chỉnh
func MakeRequestWithCustomHeaders(
    t *testing.T,
    req APIRequest,
    headers map[string]string,
) *APIResponse { ... }
```

### 3.2 Assertion Helpers

```go
// ExtractIDFromResponse — tìm ID trong response body
// Navigate: body["virtual_key"]["id"] hoặc body["team"]["id"] hoặc body["customer"]["id"]
func ExtractIDFromResponse(t *testing.T, resp *APIResponse) string {
    // Try virtual_key.id first
    // Then team.id
    // Then customer.id
    // t.Fatalf nếu không tìm thấy
    return id
}

// CheckErrorMessage — kiểm tra error message chứa text (case-insensitive)
func CheckErrorMessage(t *testing.T, resp *APIResponse, expectedText string) bool {
    // Tìm trong body["error"] hoặc body["message"]
    // Case-insensitive contains
    return found
}
```

### 3.3 Async Helpers

```go
// WaitForCondition — poll cho đến khi condition đúng hoặc timeout
func WaitForCondition(
    t *testing.T,
    checkFunc func() bool,
    timeout time.Duration,
    description string,
) bool {
    deadline := time.Now().Add(timeout)
    for time.Now().Before(deadline) {
        if checkFunc() { return true }
        time.Sleep(100 * time.Millisecond)
    }
    t.Logf("Condition not met in time: %s", description)
    return false
}

// WaitForAPICondition — poll API endpoint
func WaitForAPICondition(
    t *testing.T,
    req APIRequest,
    condition func(*APIResponse) bool,
    timeout time.Duration,
    description string,
) (*APIResponse, bool) { ... }
```

### 3.4 Cost Calculation

```go
// CalculateCost — tính cost từ token usage
func CalculateCost(model string, inputTokens, outputTokens int) (float64, error)

// Test model pricing (tháng 04/2026):
TestModels = {
    "openai/gpt-4o":               InputPer1K=$0.0025,  OutputPer1K=$0.01
    "anthropic/claude-3-7-sonnet": InputPer1K=$0.003,   OutputPer1K=$0.015
    "anthropic/claude-4-opus":     InputPer1K=$0.015,   OutputPer1K=$0.075
}

// Utility
func generateRandomID() string  // 8-char hex (crypto/rand)
```

---

## 4. Test Patterns

### 4.1 Pattern: Basic Governance Test

```go
func TestMyFeature(t *testing.T) {
    t.Parallel()  // LUÔN parallel
    
    testData := NewGlobalTestData()
    defer testData.Cleanup(t)  // LUÔN defer cleanup

    // 1. Tạo resources
    vkName := "test-my-feature-" + generateRandomID()
    createResp := MakeRequest(t, APIRequest{
        Method: "POST",
        Path:   "/api/governance/virtual-keys",
        Body: CreateVirtualKeyRequest{
            Name: vkName,
        },
    })

    if createResp.StatusCode != 200 {
        t.Fatalf("Failed to create VK: status %d", createResp.StatusCode)
    }

    vkID := ExtractIDFromResponse(t, createResp)
    testData.AddVirtualKey(vkID)  // LUÔN track cho cleanup

    vk := createResp.Body["virtual_key"].(map[string]interface{})
    vkValue := vk["value"].(string)

    // 2. Test logic
    t.Logf("Created VK: %s", vkName)

    // 3. Assertions
    // ...

    t.Logf("My feature verified ✓")
}
```

### 4.2 Pattern: Budget Depletion Test

```go
func TestBudgetDepletion(t *testing.T) {
    t.Parallel()
    testData := NewGlobalTestData()
    defer testData.Cleanup(t)

    budget := 0.01  // $0.01 - đủ nhỏ để vượt sau vài requests

    // Create VK with budget
    createResp := MakeRequest(t, APIRequest{
        Method: "POST",
        Path:   "/api/governance/virtual-keys",
        Body: CreateVirtualKeyRequest{
            Name: "test-budget-" + generateRandomID(),
            Budget: &BudgetRequest{
                MaxLimit:      budget,
                ResetDuration: "1h",
            },
        },
    })
    // ...extract id, value...

    // Make requests until budget exceeded
    consumedBudget := 0.0
    for i := 0; i <= 150; i++ {
        resp := MakeRequest(t, APIRequest{
            Method:   "POST",
            Path:     "/v1/chat/completions",
            Body:     ChatCompletionRequest{
                Model:    "openai/gpt-4o",
                Messages: []ChatMessage{{Role: "user", Content: "Hi"}},
            },
            VKHeader: &vkValue,
        })

        if resp.StatusCode >= 400 {
            if CheckErrorMessage(t, resp, "budget") {
                // ✓ Budget enforced after exceeding
                t.Logf("Budget exceeded after $%.6f (limit: $%.4f)", consumedBudget, budget)
                if consumedBudget < budget {
                    t.Fatalf("Rejected BEFORE budget reached: $%.6f < $%.4f", consumedBudget, budget)
                }
                return  // Test passed
            }
            t.Fatalf("Unexpected error: %v", resp.Body)
        }

        // Extract cost from usage
        if usage, ok := resp.Body["usage"].(map[string]interface{}); ok {
            prompt := int(usage["prompt_tokens"].(float64))
            completion := int(usage["completion_tokens"].(float64))
            cost, _ := CalculateCost("openai/gpt-4o", prompt, completion)
            consumedBudget += cost
        }
    }

    t.Fatalf("Budget never enforced after 150 requests ($%.6f consumed)", consumedBudget)
}
```

### 4.3 Pattern: In-Memory State Verification

```go
// Verify state AFTER async PostHook goroutine settles
func TestMyUsageTracking(t *testing.T) {
    // ... create VK, make inference request ...

    // CRITICAL: Wait for async PostHook to update in-memory
    time.Sleep(2 * time.Second)

    // Query in-memory state
    memResp := MakeRequest(t, APIRequest{
        Method: "GET",
        Path:   "/api/governance/budgets?from_memory=true",
    })

    budgetsMap := memResp.Body["budgets"].(map[string]interface{})
    budgetData := budgetsMap[budgetID].(map[string]interface{})
    usage := budgetData["current_usage"].(float64)

    if usage <= 0 {
        t.Fatalf("Expected budget usage to increase, got $%.6f", usage)
    }
    t.Logf("Budget usage tracked: $%.6f ✓", usage)
}
```

### 4.4 Pattern: Rate Limit Reset

```go
func TestRateLimitReset(t *testing.T) {
    t.Parallel()
    testData := NewGlobalTestData()
    defer testData.Cleanup(t)

    limit := int64(1)
    resetDuration := "15s"

    // Create VK with tight rate limit
    // ...

    startTime := time.Now()

    // First request: should succeed
    resp1 := MakeRequest(t, /* chat completion with vkValue */)
    if resp1.StatusCode != 200 { t.Skip("Could not make first request") }

    // Second request immediately: should be rejected
    resp2 := MakeRequest(t, /* same */)
    if resp2.StatusCode < 400 {
        t.Fatalf("Second request should be rate-limited, got %d", resp2.StatusCode)
    }

    // Wait for reset (resetDuration + 1s buffer)
    waitTime := time.Until(startTime.Add(16 * time.Second))
    if waitTime > 0 {
        t.Logf("Waiting %.1fs for rate limit reset...", waitTime.Seconds())
        time.Sleep(waitTime)
    }

    // Third request after reset: should succeed
    resp3 := MakeRequest(t, /* same */)
    if resp3.StatusCode != 200 {
        t.Fatalf("Request after reset should succeed, got %d", resp3.StatusCode)
    }
    t.Logf("Rate limit reset verified ✓")
}
```

### 4.5 Pattern: Hierarchy Test

```go
func TestBudgetHierarchy(t *testing.T) {
    // Create: Customer($1000) → Team($100) → VK($0.1) → Provider($.01 = MOST RESTRICTIVE)
    // Make requests → Provider budget hit first → reject với "budget" error
    // Verify: consumed >= providerBudget at time of rejection
}
```

### 4.6 Pattern: Concurrent Requests

```go
func TestConcurrentRequests(t *testing.T) {
    t.Parallel()
    // ...create VK...
    
    numGoroutines := 5
    results := make([]int, numGoroutines)  // status codes
    var wg sync.WaitGroup

    for i := 0; i < numGoroutines; i++ {
        wg.Add(1)
        go func(idx int) {
            defer wg.Done()
            resp := MakeRequest(t, APIRequest{
                Method: "POST", Path: "/v1/chat/completions",
                Body:     ChatCompletionRequest{...},
                VKHeader: &vkValue,
            })
            results[idx] = resp.StatusCode
        }(i)
    }
    wg.Wait()

    // Verify results
    for i, code := range results {
        t.Logf("Goroutine %d: status %d", i, code)
    }
    // Assert các results theo business logic
}
```

---

## 5. In-Memory API Endpoints

Governance tests dùng các endpoints đặc biệt truy vấn internal state:

```
GET /api/governance/virtual-keys?from_memory=true
    → body["virtual_keys"] = map[vkValue] → { id, budget_id, rate_limit_id, provider_configs, is_active }

GET /api/governance/budgets?from_memory=true  
    → body["budgets"] = map[budgetID] → { max_limit, current_usage, reset_duration }

GET /api/governance/rate-limits?from_memory=true
    → body["rate_limits"] = map[rateLimitID] → { 
        token_max_limit, token_current_usage, token_reset_duration,
        request_max_limit, request_current_usage, request_reset_duration
      }

GET /api/governance/teams?from_memory=true
    → body["teams"] = map[teamID] → team data

GET /api/governance/customers?from_memory=true
    → body["customers"] = map[customerID] → customer data
```

**JSON Navigation Pattern:**
```go
// body["virtual_keys"] là map của vkValue → vkData
virtualKeysMap := resp.Body["virtual_keys"].(map[string]interface{})
vkData := virtualKeysMap[vkValue].(map[string]interface{})
budgetID := vkData["budget_id"].(string)

// budget map của budgetID → budgetData
budgetsMap := resp2.Body["budgets"].(map[string]interface{})
budgetData := budgetsMap[budgetID].(map[string]interface{})
usage := budgetData["current_usage"].(float64)
```

---

## 6. Common Failure Scenarios và Cách Fix

### 6.1 "nil pointer dereference" khi navigate body

```go
// ❌ WRONG: Không kiểm tra type assertion
usage := resp.Body["usage"].(map[string]interface{})

// ✅ CORRECT: Two-value form với ok check
if usage, ok := resp.Body["usage"].(map[string]interface{}); ok {
    // safe to use usage
}
```

### 6.2 Test flaky do race condition in-memory

```go
// ❌ WRONG: Query ngay sau API call
MakeRequest(...)
memState := MakeRequest(..., "?from_memory=true")  // Có thể chưa update

// ✅ CORRECT: Wait cho async goroutine settle
MakeRequest(...)
time.Sleep(2 * time.Second)  // PostHook goroutine cần thời gian
memState := MakeRequest(..., "?from_memory=true")
```

### 6.3 Budget consumed in first (exceeding) request

```go
// ❌ WRONG: Expect first request to be rejected
for {
    resp := MakeRequest(...)
    if resp.StatusCode >= 400 {
        // consumedBudget có thể = 0 lúc này → fail assertion
        if consumedBudget < budget { t.Fatalf("rejected before budget") }
    }
    // tích lũy cost
}

// ✅ CORRECT: Budget enforcement là POST-HOC
// Request gây vượt budget: PASS
// Request TIẾP THEO: FAIL với "budget" error
// consumedBudget tích lũy TRƯỚC khi check rejection
```

### 6.4 VK value vs VK ID

```go
// VK có 2 trường khác nhau:
// - id: UUID, dùng cho management API (PUT /api/.../virtual-keys/{id})
// - value: "vk-xxxx", dùng làm API key trong header x-bf-vk

vk := createResp.Body["virtual_key"].(map[string]interface{})
vkID    := vk["id"].(string)     // UUID → management
vkValue := vk["value"].(string)  // "vk-xxx" → inference header

// In-memory map key by vkValue (không phải vkID)
virtualKeysMap := resp.Body["virtual_keys"].(map[string]interface{})
vkData := virtualKeysMap[vkValue]  // ← dùng VALUE, không ID
```

---

## 7. Reset Duration Format

```go
// Supported formats:
"15s"   // 15 giây (dùng cho test nhanh)
"1m"    // 1 phút
"1h"    // 1 giờ (phổ biến nhất trong tests)
"1d"    // 1 ngày  
"1M"    // 1 tháng
"1y"    // 1 năm

// Ví dụ từ tests:
requestResetDuration := "15s"  // Rate limit test nhanh (chờ 16s để reset)
budgetResetDuration  := "1h"   // Budget tests (không cần chờ reset)
```

---

## 8. Run Governance Tests

```bash
# Tất cả governance tests (không cần live LLM API nếu dùng mock)
make test-governance

# Chạy với Go test trực tiếp
go test ./tests/governance/... -v -timeout 10m

# Specific test
go test ./tests/governance/... -run TestMultipleVKsSharingTeamBudgetFairness -v

# Test pattern
go test ./tests/governance/... -run TestBudget* -v

# Parallel tests với race detector
go test -race ./tests/governance/... -timeout 15m

# Debug mode (Delve)
make test-governance DEBUG=1
```

**Prerequisites:**
```bash
# Bifrost server running với governance plugin
make dev  # hoặc ./bifrost-http -config tests/governance/config.json

# Optional: Live LLM API keys cho inference tests
export OPENAI_API_KEY=sk-...
```

---

## 9. Budget và Rate Limit Cheat Sheet

### Budget Levels (Thứ tự ưu tiên, most restrictive wins)

```
Provider Budget (VK.ProviderConfigs[i].Budget)
    ↓ Nếu truất qua
VK Budget (VK.Budget)
    ↓ Nếu truất qua
Team Budget (Team.Budget)
    ↓ Nếu truất qua
Customer Budget (Customer.Budget)
```

**Rule:** Bất kỳ level nào bị vượt quá → request TIẾP THEO bị từ chối với "budget" error.

### Rate Limit Levels

```
Provider Rate Limit (VK.ProviderConfigs[i].RateLimit)
    +
VK Rate Limit (VK.RateLimit)
```

**Rule:** Token và Request limits kiểm tra độc lập. Nếu hoặc token HOẶC request vượt → reject với "rate" error.

### Error Messages

| Condition | Error contains |
|----------|---------------|
| Budget exceeded | `"budget"` |
| Rate limit exceeded | `"rate"` hoặc `"token"` hoặc `"request"` |
| VK inactive | `"blocked"` |
| Invalid VK | `"invalid"` hoặc `"unauthorized"` |

### Common Budget Test Values

| Purpose | Value |
|---------|-------|
| Quick exceed (few requests) | `0.01` ($0.01) |
| Medium (10-20 requests) | `0.1` ($0.1) |
| Won't restrict gpt-4o | `100.0` ($100) |
| Mock provider budget | `0.001` ($0.001) |

---

## 10. Checklist Thêm Governance Test Mới

- [ ] `func TestMyTest(t *testing.T)` — exported function
- [ ] `t.Parallel()` là dòng đầu tiên
- [ ] `testData := NewGlobalTestData()` + `defer testData.Cleanup(t)`  
- [ ] Dùng `generateRandomID()` cho tất cả resource names
- [ ] `testData.AddVirtualKey(id)` ngay sau tạo VK thành công
- [ ] `testData.AddTeam(id)` ngay sau tạo Team
- [ ] `testData.AddCustomer(id)` ngay sau tạo Customer
- [ ] Kiểm tra `createResp.StatusCode != 200` và `t.Fatalf` nếu fail
- [ ] `time.Sleep(500ms)` sau PUT/management operations
- [ ] `time.Sleep(2s)` sau inference requests, trước in-memory query
- [ ] Budget assertions: verify `consumedBudget >= budget` trước khi assert rejection
- [ ] Dùng `t.Logf(...)` để document từng step
- [ ] Kết thúc với `t.Logf("Feature verified ✓")`
- [ ] Không dùng `t.Fatalf` trong goroutines (causes panic) — dùng channel hoặc collect results
