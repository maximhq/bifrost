---
name: bifrost-tests
description: Write tests for Bifrost following established patterns. Use when asked to "write tests", "add tests", or "test coverage" for Bifrost code.
---

# Bifrost Test Writing Skill

## Test Categories

Bifrost has three categories of tests:

### 1. Integration Tests (Provider Tests)
**Location:** `core/providers/{provider}/{provider}_test.go`
**Package:** `{provider}_test` (external test package)
**Purpose:** End-to-end tests that call actual APIs

**Pattern:**
```go
package anthropic_test

import (
    "os"
    "strings"
    "testing"
    "github.com/maximhq/bifrost/core/internal/testutil"
    "github.com/maximhq/bifrost/core/schemas"
)

func TestAnthropic(t *testing.T) {
    t.Parallel()
    if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) == "" {
        t.Skip("Skipping tests because ANTHROPIC_API_KEY is not set")
    }

    client, ctx, cancel, err := testutil.SetupTest()
    if err != nil {
        t.Fatalf("Error initializing test setup: %v", err)
    }
    defer cancel()

    testConfig := testutil.ComprehensiveTestConfig{
        Provider:  schemas.Anthropic,
        ChatModel: "claude-sonnet-4-5",
    }

    t.Run("AnthropicTests", func(t *testing.T) {
        testutil.RunAllComprehensiveTests(t, client, ctx, testConfig)
    })
    client.Shutdown()
}
```

### 2. Unit Tests (Type/JSON Tests)
**Location:** `core/providers/{provider}/conversion_test.go`
**Package:** `{provider}` (internal test package)
**Purpose:** Test JSON serialization and type conversions

**Pattern - JSON Parsing with cmp.Diff:**
```go
package anthropic

import (
    "testing"
    "github.com/bytedance/sonic"
    "github.com/google/go-cmp/cmp"
)

func TestMyType(t *testing.T) {
    tests := []struct {
        name        string
        jsonPayload string
        want        MyType
    }{
        {
            name: "all fields preserved",
            jsonPayload: `{"field1": "value1", "field2": 123}`,
            want: MyType{
                Field1: "value1",
                Field2: 123,
            },
        },
        {
            name: "optional fields omitted",
            jsonPayload: `{"field1": "value1"}`,
            want: MyType{
                Field1: "value1",
            },
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            var got MyType
            if err := sonic.Unmarshal([]byte(tt.jsonPayload), &got); err != nil {
                t.Fatalf("Failed to unmarshal JSON: %v", err)
            }
            if diff := cmp.Diff(tt.want, got); diff != "" {
                t.Errorf("mismatch (-want +got):\n%s", diff)
            }
        })
    }
}
```

### 3. Conversion Tests
**Location:** `core/providers/{provider}/conversion_test.go`
**Package:** `{provider}` (internal)
**Purpose:** Test Bifrost ↔ Provider format conversions

**Pattern - Bifrost → Provider:**
```go
func TestToProviderRequest(t *testing.T) {
    tests := []struct {
        name  string
        input *schemas.BifrostChatRequest
        want  *ProviderRequest
    }{
        {
            name: "basic conversion",
            input: &schemas.BifrostChatRequest{
                Model: "model-name",
                // ...
            },
            want: &ProviderRequest{
                Model: "model-name",
                // ...
            },
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := ToProviderRequest(tt.input)
            if err != nil {
                t.Fatalf("conversion failed: %v", err)
            }
            if diff := cmp.Diff(tt.want, got); diff != "" {
                t.Errorf("mismatch (-want +got):\n%s", diff)
            }
        })
    }
}
```

**Pattern - Provider → Bifrost (from JSON):**
```go
func TestToBifrostResponse(t *testing.T) {
    tests := []struct {
        name        string
        jsonPayload string
        want        *schemas.BifrostChatResponse
    }{
        {
            name: "basic response",
            jsonPayload: `{
                "id": "msg_123",
                "content": [{"type": "text", "text": "Hello"}]
            }`,
            want: &schemas.BifrostChatResponse{
                ID: "msg_123",
                // ...
            },
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            var providerResp ProviderResponse
            if err := sonic.Unmarshal([]byte(tt.jsonPayload), &providerResp); err != nil {
                t.Fatalf("Failed to unmarshal: %v", err)
            }
            got := providerResp.ToBifrostChatResponse()
            if diff := cmp.Diff(tt.want, got); diff != "" {
                t.Errorf("mismatch (-want +got):\n%s", diff)
            }
        })
    }
}
```

## Key Libraries

- **JSON:** Use `github.com/bytedance/sonic` (NOT `encoding/json`)
- **Comparison:** Use `github.com/google/go-cmp/cmp` for struct comparison
- **Assertions:** Use standard `t.Errorf`, `t.Fatalf`

## Naming Conventions

- Test functions: `Test{TypeOrFunction}` or `Test{TypeOrFunction}_{Scenario}`
- Test cases in tables: descriptive lowercase with spaces
- Files: `conversion_test.go` for type/conversion tests

## Common Patterns

### Pointer Helpers
```go
func strPtr(s string) *string { return &s }
func intPtr(i int) *int { return &i }
func boolPtr(b bool) *bool { return &b }
func float64Ptr(f float64) *float64 { return &f }
```

### Using cmp.Diff
```go
if diff := cmp.Diff(want, got); diff != "" {
    t.Errorf("mismatch (-want +got):\n%s", diff)
}
```

## Anti-Patterns (PROHIBITED)

### ❌ Manual field-by-field comparisons
```go
// WRONG - Never do this
if got.Model != tt.want.Model {
    t.Errorf("Model: got %q, want %q", got.Model, tt.want.Model)
}
if got.MaxTokens != tt.want.MaxTokens {
    t.Errorf("MaxTokens: got %d, want %d", got.MaxTokens, tt.want.MaxTokens)
}
```

### ❌ Validate callback functions
```go
// WRONG - Never use validate callbacks
tests := []struct {
    name     string
    input    *Input
    validate func(t *testing.T, got *Output)  // PROHIBITED
}{...}
```

### ❌ Multiple top-level tests per conversion function
```go
// WRONG - Don't create separate test functions for each scenario
func TestToBifrostChatResponse_ServerToolUse(t *testing.T) {...}
func TestToBifrostChatResponse_CacheTokens(t *testing.T) {...}
func TestToBifrostChatResponse_BasicFields(t *testing.T) {...}

// CORRECT - One table-driven test with multiple cases
func TestToBifrostChatResponse(t *testing.T) {
    tests := []struct {
        name string
        // ...
    }{
        {name: "basic response", ...},
        {name: "server_tool_use maps to NumSearchQueries", ...},
        {name: "cache read and creation tokens", ...},
    }
    // ...
}
```

### ✅ Always use single cmp.Diff
```go
// CORRECT - One cmp.Diff for entire struct (or relevant sub-struct)
if diff := cmp.Diff(tt.want, got); diff != "" {
    t.Errorf("mismatch (-want +got):\n%s", diff)
}
```

## Workflow

1. Identify what needs testing (new fields, conversions, functions)
2. Choose appropriate test category
3. Use table-driven tests with `want` structs
4. Use single `cmp.Diff` for comparing entire output (never manual field checks)
5. Test both success and edge cases (nil, empty, omitted fields)
6. Run tests: `go test ./providers/{provider}/... -v`
