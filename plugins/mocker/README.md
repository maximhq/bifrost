# Bifrost Mocker Plugin

The Mocker plugin for Bifrost allows you to intercept and mock AI provider responses for testing, development, and simulation purposes. It provides flexible rule-based mocking with support for custom responses, error simulation, latency injection, and comprehensive statistics tracking.

**⚡ Performance Optimized** - Designed for high-throughput scenarios including benchmarking with minimal overhead.

## Table of Contents

1. [Quick Start](#quick-start)
2. [Installation](#installation)
3. [Basic Usage](#basic-usage)
4. [Configuration Reference](#configuration-reference)
5. [Advanced Features](#advanced-features)
6. [Faker Support](#faker-support)
7. [Examples](#examples)
8. [Statistics and Monitoring](#statistics-and-monitoring)
9. [Performance](#performance)
10. [Best Practices](#best-practices)
11. [Troubleshooting](#troubleshooting)

## Quick Start

### Minimal Configuration

The simplest way to use the Mocker plugin is with no configuration - it will create a default catch-all rule:

```go
package main

import (
    "context"
    bifrost "github.com/maximhq/bifrost/core"
    "github.com/maximhq/bifrost/core/schemas"
    mocker "github.com/maximhq/bifrost/plugins/mocker"
)

func main() {
    // Create plugin with minimal config
    plugin, err := mocker.NewMockerPlugin(mocker.MockerConfig{
        Enabled: true, // That's it! Default rule will be created automatically
    })
    if err != nil {
        panic(err)
    }

    // Initialize Bifrost with the plugin
    client, initErr := bifrost.Init(schemas.BifrostConfig{
        Account: &yourAccount,
        Plugins: []schemas.Plugin{plugin},
    })
    if err != nil {
        panic(err)
    }
    defer client.Cleanup()

    // All requests will now return: "This is a mock response from the Mocker plugin"
    response, _ := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "gpt-4",
        Input: schemas.RequestInput{
            ChatCompletionInput: &[]schemas.BifrostMessage{
                {
                    Role: schemas.ModelChatMessageRoleUser,
                    Content: schemas.MessageContent{
                        ContentStr: bifrost.Ptr("Hello!"),
                    },
                },
            },
        },
    })

    // response.Choices[0].Message.Content.ContentStr == "This is a mock response from the Mocker plugin"
}
```

### Quick Custom Response

```go
plugin, err := mocker.NewMockerPlugin(mocker.MockerConfig{
    Enabled: true,
    Rules: []mocker.MockRule{
        {
            Name:        "openai-mock",
            Enabled:     true,
            Probability: 1.0, // Always trigger
            Conditions: mocker.Conditions{
                Providers: []string{"openai"},
            },
            Responses: []mocker.Response{
                {
                    Type: mocker.ResponseTypeSuccess,
                    Content: &mocker.SuccessResponse{
                        Message: "Hello! This is a custom mock response for OpenAI.",
                        Usage: &mocker.Usage{
                            PromptTokens:     15,
                            CompletionTokens: 25,
                            TotalTokens:      40,
                        },
                    },
                },
            },
        },
    },
})
```

## Installation

### As a Go Module

1. Add the plugin to your project:

   ```bash
   go get github.com/maximhq/bifrost/plugins/mocker
   ```

2. Import in your code:

   ```go
   import mocker "github.com/maximhq/bifrost/plugins/mocker"
   ```

### Development Setup

1. Clone the repository:

   ```bash
   git clone https://github.com/maximhq/bifrost.git
   cd bifrost/plugins/mocker
   ```

2. Install dependencies:

   ```bash
   go mod tidy
   ```

3. Run tests:

   ```bash
   go test -v
   ```

## Basic Usage

### Creating the Plugin

```go
// Basic configuration
config := mocker.MockerConfig{
    Enabled: true,
    DefaultBehavior: mocker.DefaultBehaviorPassthrough, // Optional: "passthrough", "success", "error"
    Rules: []mocker.MockRule{
        // Your rules here
    },
}

plugin, err := mocker.NewMockerPlugin(config)
if err != nil {
    // Handle configuration errors
    log.Fatal(err)
}
```

### Adding to Bifrost

```go
client, initErr := bifrost.Init(schemas.BifrostConfig{
    Account: &yourAccount,
    Plugins: []schemas.Plugin{
        plugin, // Add your mocker plugin
        // Other plugins...
    },
    Logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo),
})
```

### Disabling the Plugin

```go
// Temporarily disable without removing
config := mocker.MockerConfig{
    Enabled: false, // All requests pass through to real providers
}
```

## Configuration Reference

### MockerConfig

| Field             | Type         | Required | Default         | Description                                                         |
| ----------------- | ------------ | -------- | --------------- | ------------------------------------------------------------------- |
| `Enabled`         | `bool`       | Yes      | `false`         | Enable/disable the entire plugin                                    |
| `DefaultBehavior` | `string`     | No       | `"passthrough"` | Action when no rules match: `"passthrough"`, `"success"`, `"error"` |
| `GlobalLatency`   | `*Latency`   | No       | `nil`           | Global latency applied to all rules (can be overridden per rule)    |
| `Rules`           | `[]MockRule` | No       | `[]`            | List of mock rules evaluated in priority order                      |

### MockRule

| Field         | Type         | Required | Default | Description                                        |
| ------------- | ------------ | -------- | ------- | -------------------------------------------------- |
| `Name`        | `string`     | Yes      | -       | Unique rule name for identification and statistics |
| `Enabled`     | `bool`       | No       | `true`  | Enable/disable this specific rule                  |
| `Priority`    | `int`        | No       | `0`     | Higher numbers = higher priority (checked first)   |
| `Probability` | `float64`    | No       | `1.0`   | Activation probability (0.0=never, 1.0=always)     |
| `Conditions`  | `Conditions` | No       | `{}`    | Matching conditions (empty = match all)            |
| `Responses`   | `[]Response` | Yes      | -       | Possible responses (weighted random selection)     |
| `Latency`     | `*Latency`   | No       | `nil`   | Rule-specific latency override                     |

### Conditions

| Field          | Type         | Required | Default | Description                                         |
| -------------- | ------------ | -------- | ------- | --------------------------------------------------- |
| `Providers`    | `[]string`   | No       | `[]`    | Match specific providers: `["openai", "anthropic"]` |
| `Models`       | `[]string`   | No       | `[]`    | Match specific models: `["gpt-4", "claude-3"]`      |
| `MessageRegex` | `*string`    | No       | `nil`   | Regex pattern to match message content              |
| `RequestSize`  | `*SizeRange` | No       | `nil`   | Request size constraints in bytes                   |

### Response

| Field            | Type               | Required    | Default | Description                                            |
| ---------------- | ------------------ | ----------- | ------- | ------------------------------------------------------ |
| `Type`           | `string`           | Yes         | -       | Response type: `"success"` or `"error"`                |
| `Weight`         | `float64`          | No          | `1.0`   | Weight for random selection (higher = more likely)     |
| `Content`        | `*SuccessResponse` | Conditional | -       | Required if `Type="success"`                           |
| `Error`          | `*ErrorResponse`   | Conditional | -       | Required if `Type="error"`                             |
| `AllowFallbacks` | `*bool`            | No          | `nil`   | Control fallback behavior (`nil`=allow, `false`=block) |

### SuccessResponse

| Field             | Type                     | Required    | Default        | Description                                                         |
| ----------------- | ------------------------ | ----------- | -------------- | ------------------------------------------------------------------- |
| `Message`         | `string`                 | Conditional | -              | Static response message (required if no template)                   |
| `MessageTemplate` | `*string`                | Conditional | -              | Template with variables: `{{provider}}`, `{{model}}`, `{{faker.*}}` |
| `Model`           | `*string`                | No          | `nil`          | Override model name in response                                     |
| `Usage`           | `*Usage`                 | No          | Default values | Token usage information                                             |
| `FinishReason`    | `*string`                | No          | `"stop"`       | Completion reason                                                   |
| `CustomFields`    | `map[string]interface{}` | No          | `{}`           | Additional metadata fields                                          |

### ErrorResponse

| Field        | Type      | Required | Default | Description                                       |
| ------------ | --------- | -------- | ------- | ------------------------------------------------- |
| `Message`    | `string`  | Yes      | -       | Error message to return                           |
| `Type`       | `*string` | No       | `nil`   | Error type (e.g., `"rate_limit"`, `"auth_error"`) |
| `Code`       | `*string` | No       | `nil`   | Error code (e.g., `"429"`, `"401"`)               |
| `StatusCode` | `*int`    | No       | `nil`   | HTTP status code                                  |

### Latency

| Field  | Type            | Required    | Default | Description                                                        |
| ------ | --------------- | ----------- | ------- | ------------------------------------------------------------------ |
| `Type` | `string`        | Yes         | -       | Latency type: `"fixed"` or `"uniform"`                             |
| `Min`  | `time.Duration` | Yes         | -       | Minimum/exact latency (use `time.Millisecond`, NOT raw int)        |
| `Max`  | `time.Duration` | Conditional | -       | Maximum latency (required for `"uniform"`, use `time.Millisecond`) |

**⚠️ Important**: Use Go's `time.Duration` constants, not raw integers:

- ✅ Correct: `100 * time.Millisecond`
- ❌ Wrong: `100` (this would be 100 nanoseconds, barely noticeable)

### SizeRange

| Field | Type  | Required | Default | Description                   |
| ----- | ----- | -------- | ------- | ----------------------------- |
| `Min` | `int` | Yes      | -       | Minimum request size in bytes |
| `Max` | `int` | Yes      | -       | Maximum request size in bytes |

### Usage

| Field              | Type  | Required | Default | Description                        |
| ------------------ | ----- | -------- | ------- | ---------------------------------- |
| `PromptTokens`     | `int` | No       | `10`    | Number of tokens in the prompt     |
| `CompletionTokens` | `int` | No       | `20`    | Number of tokens in the completion |
| `TotalTokens`      | `int` | No       | `30`    | Total tokens (prompt + completion) |

## Advanced Features

### Template Variables

Use templates to create dynamic responses:

```go
Response{
    Type: mocker.ResponseTypeSuccess,
    Content: &mocker.SuccessResponse{
        MessageTemplate: stringPtr("Hello from {{provider}} using model {{model}}!"),
    },
}
```

**Available Variables:**

- `{{provider}}` - Provider name (e.g., "openai", "anthropic")
- `{{model}}` - Model name (e.g., "gpt-4", "claude-3")
- `{{faker.*}}` - Fake data generation (see [Faker Support](#faker-support) section for full list)

### Weighted Response Selection

Configure multiple responses with different weights:

```go
Responses: []mocker.Response{
    {
        Type:   mocker.ResponseTypeSuccess,
        Weight: 0.8, // 80% chance
        Content: &mocker.SuccessResponse{
            Message: "Success response",
        },
    },
    {
        Type:   mocker.ResponseTypeError,
        Weight: 0.2, // 20% chance
        Error: &mocker.ErrorResponse{
            Message: "Simulated error",
            StatusCode: intPtr(500),
        },
    },
}
```

### Latency Simulation

Add realistic delays to responses:

```go
// Fixed latency
Latency: &mocker.Latency{
    Type: mocker.LatencyTypeFixed,
    Min:  100 * time.Millisecond,
}

// Variable latency
Latency: &mocker.Latency{
    Type: mocker.LatencyTypeUniform,
    Min:  50 * time.Millisecond,
    Max:  200 * time.Millisecond,
}
```

**⚠️ Critical**: Always use `time.Duration` constants (e.g., `time.Millisecond`), never raw integers. Raw integers are interpreted as nanoseconds and will be barely noticeable.

### Regex Message Matching

Match specific message content:

```go
Conditions: mocker.Conditions{
    MessageRegex: stringPtr(`(?i).*error.*|.*help.*`), // Case-insensitive match for "error" or "help"
}
```

### Request Size Filtering

Match requests by size:

```go
Conditions: mocker.Conditions{
    RequestSize: &mocker.SizeRange{
        Min: 100,  // Minimum 100 bytes
        Max: 1000, // Maximum 1000 bytes
    },
}
```

## Faker Support

The Mocker plugin includes comprehensive fake data generation capabilities using the [jaswdr/faker](https://github.com/jaswdr/faker) library. This allows you to create realistic mock responses with dynamic, fake data that changes on each request.

### Using Faker in Templates

Faker variables can be used in the `MessageTemplate` field using the `{{faker.method}}` syntax:

```go
Response{
    Type: mocker.ResponseTypeSuccess,
    Content: &mocker.SuccessResponse{
        MessageTemplate: stringPtr(`Hello {{faker.first_name}}! Your email {{faker.email}} has been verified.
Your account ID is {{faker.uuid}} and your phone number {{faker.phone}} is on file.`),
    },
}
```

### Available Faker Methods

#### Personal Information

- `{{faker.name}}` - Full name (e.g., "John Smith")
- `{{faker.first_name}}` - First name (e.g., "John")
- `{{faker.last_name}}` - Last name (e.g., "Smith")
- `{{faker.email}}` - Email address (e.g., "john123@example.com")
- `{{faker.phone}}` - Phone number (e.g., "+1-555-123-4567")

#### Location

- `{{faker.address}}` - Full address (e.g., "123 Main St")
- `{{faker.city}}` - City name (e.g., "New York")
- `{{faker.state}}` - State name (e.g., "California")
- `{{faker.zip_code}}` - Postal code (e.g., "12345")

#### Business

- `{{faker.company}}` - Company name (e.g., "Tech Solutions Inc.")
- `{{faker.job_title}}` - Job title (e.g., "Software Engineer")

#### Text and Lorem Ipsum

- `{{faker.word}}` - Single word (e.g., "example")
- `{{faker.sentence}}` - Sentence with default 8 words
- `{{faker.sentence:5}}` - Sentence with 5 words
- `{{faker.lorem_ipsum}}` - Lorem ipsum text with default 10 words
- `{{faker.lorem_ipsum:20}}` - Lorem ipsum text with 20 words

#### Identifiers and Data

- `{{faker.uuid}}` - UUID v4 (e.g., "123e4567-e89b-12d3-a456-426614174000")
- `{{faker.hex_color}}` - Hex color code (e.g., "#FF5733")

#### Numbers and Dates

- `{{faker.integer}}` - Random integer between 1-100
- `{{faker.integer:10,50}}` - Random integer between 10-50
- `{{faker.float}}` - Random float between 0-100 (2 decimal places)
- `{{faker.float:1,10}}` - Random float between 1-10
- `{{faker.boolean}}` - Random boolean (true/false)
- `{{faker.date}}` - Date in YYYY-MM-DD format
- `{{faker.datetime}}` - Datetime in YYYY-MM-DD HH:MM:SS format

### Faker Examples

#### Customer Support Simulation

```go
{
    Name:        "customer-support-faker",
    Enabled:     true,
    Priority:    100,
    Probability: 1.0,
    Conditions: mocker.Conditions{
        MessageRegex: stringPtr(`(?i).*support.*|.*help.*`),
    },
    Responses: []mocker.Response{
        {
            Type: mocker.ResponseTypeSuccess,
            Content: &mocker.SuccessResponse{
                MessageTemplate: stringPtr(`Hello {{faker.first_name}}!

Thank you for contacting {{faker.company}} support. I've reviewed your account and here are the details:

**Account Information:**
- Name: {{faker.name}}
- Email: {{faker.email}}
- Phone: {{faker.phone}}
- Account ID: {{faker.uuid}}
- Address: {{faker.address}}, {{faker.city}}, {{faker.state}} {{faker.zip_code}}

**Support Ticket:** #{{faker.integer:10000,99999}}
**Priority:** {{faker.boolean}}
**Estimated Resolution:** {{faker.date}}

How can I help you today?`),
                Usage: &mocker.Usage{
                    PromptTokens:     25,
                    CompletionTokens: 150,
                    TotalTokens:      175,
                },
            },
        },
    },
}
```

#### E-commerce Order Confirmation

```go
{
    Name:        "ecommerce-order-faker",
    Enabled:     true,
    Priority:    100,
    Probability: 1.0,
    Conditions: mocker.Conditions{
        MessageRegex: stringPtr(`(?i).*order.*|.*purchase.*`),
    },
    Responses: []mocker.Response{
        {
            Type: mocker.ResponseTypeSuccess,
            Content: &mocker.SuccessResponse{
                MessageTemplate: stringPtr(`🎉 Order Confirmed!

**Order Details:**
- Order ID: {{faker.uuid}}
- Customer: {{faker.name}}
- Email: {{faker.email}}
- Total: ${{faker.float:10,500}}
- Items: {{faker.integer:1,5}} items

**Shipping Address:**
{{faker.address}}
{{faker.city}}, {{faker.state}} {{faker.zip_code}}

**Estimated Delivery:** {{faker.date}}
**Tracking Number:** {{faker.integer:100000000,999999999}}

Thank you for shopping with {{faker.company}}!`),
            },
        },
    },
}
```

#### User Profile Generation

```go
{
    Name:        "user-profile-faker",
    Enabled:     true,
    Priority:    100,
    Probability: 1.0,
    Conditions: mocker.Conditions{
        MessageRegex: stringPtr(`(?i).*profile.*|.*user.*info.*`),
    },
    Responses: []mocker.Response{
        {
            Type: mocker.ResponseTypeSuccess,
            Content: &mocker.SuccessResponse{
                MessageTemplate: stringPtr(`**User Profile Generated:**

**Personal Information:**
- Full Name: {{faker.name}}
- Email: {{faker.email}}
- Phone: {{faker.phone}}
- Preferred Color: {{faker.hex_color}}

**Professional Details:**
- Company: {{faker.company}}
- Job Title: {{faker.job_title}}
- Work Phone: {{faker.phone}}

**Address:**
{{faker.address}}
{{faker.city}}, {{faker.state}} {{faker.zip_code}}

**Account Settings:**
- User ID: {{faker.uuid}}
- Account Created: {{faker.date}}
- Email Notifications: {{faker.boolean}}
- SMS Alerts: {{faker.boolean}}

**Bio:** {{faker.lorem_ipsum:25}}`),
            },
        },
    },
}
```

### Faker with Weighted Responses

You can combine faker with weighted response selection for even more realistic scenarios:

```go
{
    Name:        "mixed-faker-responses",
    Enabled:     true,
    Priority:    100,
    Probability: 1.0,
    Responses: []mocker.Response{
        {
            Type:   mocker.ResponseTypeSuccess,
            Weight: 0.7, // 70% positive responses
            Content: &mocker.SuccessResponse{
                MessageTemplate: stringPtr(`Great news, {{faker.first_name}}! Your request has been approved.
Reference number: {{faker.uuid}}.
Contact us at {{faker.phone}} if you have questions.`),
            },
        },
        {
            Type:   mocker.ResponseTypeSuccess,
            Weight: 0.2, // 20% neutral responses
            Content: &mocker.SuccessResponse{
                MessageTemplate: stringPtr(`Hello {{faker.first_name}}, your request is being processed.
Ticket ID: {{faker.integer:1000,9999}}.
Expected completion: {{faker.date}}.`),
            },
        },
        {
            Type:   mocker.ResponseTypeError,
            Weight: 0.1, // 10% error responses
            Error: &mocker.ErrorResponse{
                Message: fmt.Sprintf("Account validation failed for user %s. Please contact support.", "{{faker.email}}"),
                Type:    stringPtr("validation_error"),
                Code:    stringPtr("VAL_001"),
            },
        },
    },
}
```

### Important Notes

- **Dynamic Generation**: Faker values are generated fresh on each request, ensuring unique responses
- **Performance**: Faker generation is highly optimized and adds minimal overhead
- **Parameters**: Some faker methods support parameters (e.g., `{{faker.sentence:10}}` for 10 words)
- **Reliability**: Uses the established [jaswdr/faker](https://github.com/jaswdr/faker) library with zero dependencies
- **Template Mixing**: You can freely mix faker variables with regular template variables like `{{provider}}` and `{{model}}`

## Examples

### Development Environment Mock

```go
config := mocker.MockerConfig{
    Enabled: true,
    DefaultBehavior: mocker.DefaultBehaviorPassthrough,
    Rules: []mocker.MockRule{
        {
            Name:        "dev-openai-mock",
            Enabled:     true,
            Priority:    100,
            Probability: 1.0,
            Conditions: mocker.Conditions{
                Providers: []string{"openai"},
                Models:    []string{"gpt-4", "gpt-4-turbo"},
            },
            Responses: []mocker.Response{
                {
                    Type: mocker.ResponseTypeSuccess,
                    Content: &mocker.SuccessResponse{
                        MessageTemplate: stringPtr("Development mock response from {{model}} ({{provider}})"),
                        Usage: &mocker.Usage{
                            PromptTokens:     20,
                            CompletionTokens: 30,
                            TotalTokens:      50,
                        },
                    },
                },
            },
        },
    },
}
```

### Error Simulation for Testing

```go
config := mocker.MockerConfig{
    Enabled: true,
    Rules: []mocker.MockRule{
        {
            Name:        "rate-limit-simulation",
            Enabled:     true,
            Priority:    200,
            Probability: 0.1, // 10% of requests
            Conditions: mocker.Conditions{
                Providers: []string{"openai", "anthropic"},
            },
            Responses: []mocker.Response{
                {
                    Type:           mocker.ResponseTypeError,
                    AllowFallbacks: boolPtr(true), // Allow fallback providers
                    Error: &mocker.ErrorResponse{
                        Message:    "Rate limit exceeded. Please try again later.",
                        Type:       stringPtr("rate_limit"),
                        Code:       stringPtr("429"),
                        StatusCode: intPtr(429),
                    },
                },
            },
            Latency: &mocker.Latency{
                Type: mocker.LatencyTypeFixed,
                Min:  500 * time.Millisecond, // Simulate slow error response
            },
        },
    },
}
```

### A/B Testing Different Responses

```go
config := mocker.MockerConfig{
    Enabled: true,
    Rules: []mocker.MockRule{
        {
            Name:        "ab-test-responses",
            Enabled:     true,
            Priority:    100,
            Probability: 1.0,
            Conditions: mocker.Conditions{
                MessageRegex: stringPtr(`(?i).*greeting.*|.*hello.*`),
            },
            Responses: []mocker.Response{
                {
                    Type:   mocker.ResponseTypeSuccess,
                    Weight: 0.5, // 50% - Version A
                    Content: &mocker.SuccessResponse{
                        Message: "Hello! How can I help you today?",
                        CustomFields: map[string]interface{}{
                            "ab_test_version": "A",
                            "response_style":  "formal",
                        },
                    },
                },
                {
                    Type:   mocker.ResponseTypeSuccess,
                    Weight: 0.5, // 50% - Version B
                    Content: &mocker.SuccessResponse{
                        Message: "Hey there! What's up?",
                        CustomFields: map[string]interface{}{
                            "ab_test_version": "B",
                            "response_style":  "casual",
                        },
                    },
                },
            },
        },
    },
}
```

### Provider-Specific Behavior

```go
config := mocker.MockerConfig{
    Enabled: true,
    Rules: []mocker.MockRule{
        {
            Name:        "openai-success",
            Enabled:     true,
            Priority:    100,
            Probability: 0.9, // 90% success rate
            Conditions: mocker.Conditions{
                Providers: []string{"openai"},
            },
            Responses: []mocker.Response{
                {
                    Type: mocker.ResponseTypeSuccess,
                    Content: &mocker.SuccessResponse{
                        Message: "OpenAI mock response - high reliability",
                    },
                },
            },
        },
        {
            Name:        "anthropic-mixed",
            Enabled:     true,
            Priority:    100,
            Probability: 1.0,
            Conditions: mocker.Conditions{
                Providers: []string{"anthropic"},
            },
            Responses: []mocker.Response{
                {
                    Type:   mocker.ResponseTypeSuccess,
                    Weight: 0.7, // 70% success
                    Content: &mocker.SuccessResponse{
                        Message: "Anthropic mock response",
                    },
                },
                {
                    Type:   mocker.ResponseTypeError,
                    Weight: 0.3, // 30% error
                    AllowFallbacks: boolPtr(true),
                    Error: &mocker.ErrorResponse{
                        Message:    "Anthropic service temporarily unavailable",
                        StatusCode: intPtr(503),
                    },
                },
            },
        },
    },
}
```

## Statistics and Monitoring

### Getting Statistics

```go
// Get current statistics
stats := plugin.GetStats()
fmt.Printf("Total Requests: %d\n", stats.TotalRequests)
fmt.Printf("Mocked Requests: %d\n", stats.MockedRequests)
fmt.Printf("Success Responses: %d\n", stats.ResponsesGenerated)
fmt.Printf("Error Responses: %d\n", stats.ErrorsGenerated)

// Per-rule statistics
for ruleName, hits := range stats.RuleHits {
    fmt.Printf("Rule '%s': %d hits\n", ruleName, hits)
}
```

### Statistics Structure

```go
type MockStats struct {
    TotalRequests      int64            `json:"total_requests"`      // Total requests processed
    MockedRequests     int64            `json:"mocked_requests"`     // Requests that matched rules
    RuleHits           map[string]int64 `json:"rule_hits"`           // Per-rule hit counts
    ErrorsGenerated    int64            `json:"errors_generated"`    // Error responses generated
    ResponsesGenerated int64            `json:"responses_generated"` // Success responses generated
}
```

### Monitoring Example

```go
// Periodic monitoring
go func() {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            stats := plugin.GetStats()
            log.Printf("Mocker Stats - Total: %d, Mocked: %d, Success: %d, Errors: %d",
                stats.TotalRequests, stats.MockedRequests,
                stats.ResponsesGenerated, stats.ErrorsGenerated)
        }
    }
}()
```

## Performance

The Mocker plugin has been extensively optimized for high-throughput scenarios, including benchmarking and load testing. Here are the key performance characteristics and optimizations:

### 🚀 **Key Optimizations**

#### 1. **Pre-compiled Regex Patterns**

- All regex patterns are compiled once during plugin initialization
- **Before**: `regexp.Compile()` on every request (~1000x slower)
- **After**: Pre-compiled patterns with direct matching

#### 2. **Atomic Counters for Statistics**

- Statistics use `sync/atomic` operations instead of mutex locks
- **Before**: Mutex lock/unlock for every counter increment
- **After**: Lock-free atomic operations

#### 3. **Optimized String Operations**

- Fast-path string matching and content extraction
- Efficient template processing with `strings.NewReplacer`
- Minimal memory allocations in hot paths

#### 4. **Smart Memory Management**

- Pre-allocated data structures
- Reduced map allocations
- Efficient string building for multi-message content

### 🧪 **Running Benchmarks**

Since performance varies significantly across different hardware configurations, you should run benchmarks on your specific system to get accurate measurements:

```bash
# Run all benchmarks with memory allocation stats
go test -bench=. -benchmem

# Run specific benchmark scenarios
go test -bench=BenchmarkMockerPlugin_PreHook_SimpleRule -benchmem
go test -bench=BenchmarkMockerPlugin_PreHook_RegexRule -benchmem
go test -bench=BenchmarkMockerPlugin_PreHook_NoMatch -benchmem

# Run with CPU profiling for detailed analysis
go test -bench=. -cpuprofile=cpu.prof

# Run with memory profiling
go test -bench=. -memprofile=mem.prof

# Run benchmarks multiple times for statistical accuracy
go test -bench=. -count=5
```

### 📊 **Understanding Benchmark Output**

The benchmark output will show:

- **Operations per second**: How many operations can be performed per second
- **Nanoseconds per operation**: Average time per operation
- **Bytes per operation**: Memory allocated per operation
- **Allocations per operation**: Number of memory allocations per operation

Example output format:

```text
BenchmarkMockerPlugin_PreHook_SimpleRule-8    5000000    250 ns/op    400 B/op    5 allocs/op
```

### 📈 **Performance Reference**

As a reference, here are results from testing on a system with 16GB RAM:

```text
BenchmarkMockerPlugin_PreHook_SimpleRule      6,306,205 ops   189.6 ns/op   416 B/op    5 allocs/op
BenchmarkMockerPlugin_PreHook_RegexRule         712,371 ops  1637 ns/op     420 B/op    5 allocs/op
BenchmarkMockerPlugin_PreHook_MultipleRules   5,604,916 ops   214.1 ns/op   416 B/op    5 allocs/op
BenchmarkMockerPlugin_PreHook_NoMatch        155,663,086 ops     7.7 ns/op     0 B/op    0 allocs/op
BenchmarkMockerPlugin_PreHook_Template           864,408 ops  1351 ns/op    1688 B/op   19 allocs/op
```

**Note**: Your results may vary based on your hardware configuration. Run the benchmarks yourself for accurate measurements on your system.

### 🎯 **Performance Characteristics**

Based on the optimizations implemented, you can expect:

#### **Ultra-Fast No-Match Path**

- Minimal overhead when no rules match
- Perfect for production with selective mocking
- Zero allocations when plugin is disabled

#### **High-Speed Simple Rules**

- Fast provider/model string matching
- Suitable for high-frequency benchmarking
- Minimal memory allocations

#### **Efficient Regex Matching**

- Pre-compiled patterns (much faster than runtime compilation)
- Good performance for pattern-based mocking
- Scales well with multiple regex rules

#### **Multiple Rule Evaluation**

- Priority-based early termination
- Performance doesn't degrade significantly with rule count
- Optimized rule traversal

### ⚡ **Configuration for Maximum Performance**

#### For **Benchmarking** (Maximum Speed):

```go
config := mocker.MockerConfig{
    Enabled: true,
    Rules: []mocker.MockRule{
        {
            Name:        "benchmark-rule",
            Enabled:     true,
            Priority:    100,
            Probability: 1.0, // Always match for consistent results
            Conditions: mocker.Conditions{
                Providers: []string{"openai"}, // Simple string match (fastest)
            },
            Responses: []mocker.Response{
                {
                    Type: mocker.ResponseTypeSuccess,
                    Content: &mocker.SuccessResponse{
                        Message: "Benchmark response", // Static message (no templates)
                        Usage: &mocker.Usage{          // Pre-defined usage
                            PromptTokens:     10,
                            CompletionTokens: 20,
                            TotalTokens:      30,
                        },
                    },
                },
            },
        },
    },
}
```

#### For **Production** (Minimal Overhead):

```go
config := mocker.MockerConfig{
    Enabled:         true,
    DefaultBehavior: mocker.DefaultBehaviorPassthrough, // Fast no-match path
    Rules: []mocker.MockRule{
        // Only critical error simulation rules
        {
            Name:        "rate-limit-sim",
            Enabled:     true,
            Probability: 0.01, // 1% activation rate
            Conditions: mocker.Conditions{
                Providers: []string{"openai"}, // Simple conditions only
            },
            // ... error response
        },
    },
}
```

### 🔧 **Performance Tuning Tips**

#### 1. **Rule Optimization**

```go
// ✅ FAST: Simple string matching
Conditions: mocker.Conditions{
    Providers: []string{"openai", "anthropic"},
    Models:    []string{"gpt-4", "claude-3"},
}

// ⚠️ SLOWER: Regex patterns (but still fast with pre-compilation)
Conditions: mocker.Conditions{
    MessageRegex: stringPtr(`(?i)error|fail`),
}

// ❌ AVOID: Complex regex patterns
Conditions: mocker.Conditions{
    MessageRegex: stringPtr(`^.*complex.*nested.*(pattern|match).*$`),
}
```

#### 2. **Response Optimization**

```go
// ✅ FAST: Static responses
Content: &mocker.SuccessResponse{
    Message: "Static response",
    Usage:   &predefinedUsage, // Reuse objects
}

// ⚠️ MODERATE: Simple templates
Content: &mocker.SuccessResponse{
    MessageTemplate: stringPtr("Response from {{provider}}"), // Minimal variables
}

// ❌ AVOID: Complex templates with many variables
Content: &mocker.SuccessResponse{
    MessageTemplate: stringPtr("Complex {{var1}} {{var2}} {{var3}} template"),
}
```

#### 3. **Rule Priority**

```go
// ✅ Put most common rules first (higher priority)
Rules: []mocker.MockRule{
    {Priority: 100, /* most common conditions */},
    {Priority: 50,  /* less common conditions */},
    {Priority: 10,  /* rare conditions */},
}
```

### 📈 **Monitoring Performance**

Track performance metrics in your application:

```go
func monitorMockerPerformance(plugin *mocker.MockerPlugin) {
    ticker := time.NewTicker(10 * time.Second)
    defer ticker.Stop()

    lastStats := plugin.GetStats()
    lastTime := time.Now()

    for {
        select {
        case <-ticker.C:
            currentStats := plugin.GetStats()
            currentTime := time.Now()

            duration := currentTime.Sub(lastTime)
            requests := currentStats.TotalRequests - lastStats.TotalRequests

            rps := float64(requests) / duration.Seconds()
            mockRate := float64(currentStats.MockedRequests) / float64(currentStats.TotalRequests) * 100

            log.Printf("Mocker Performance: %.0f req/s, %.1f%% mock rate", rps, mockRate)

            lastStats = currentStats
            lastTime = currentTime
        }
    }
}
```

**🏆 The Mocker plugin is optimized for high-throughput scenarios and adds minimal overhead to your application.**

## Best Practices

### 1. Rule Organization

- **Use descriptive names**: `"rate-limit-openai"` instead of `"rule1"`
- **Set appropriate priorities**: Critical rules should have higher priority
- **Group related rules**: Keep similar functionality together

### 2. Development vs Production

```go
// Development - High mock rate
config := mocker.MockerConfig{
    Enabled: true,
    DefaultBehavior: mocker.DefaultBehaviorSuccess, // Mock everything by default
}

// Production - Selective mocking
config := mocker.MockerConfig{
    Enabled: true,
    DefaultBehavior: mocker.DefaultBehaviorPassthrough, // Pass through by default
    Rules: []mocker.MockRule{
        // Only specific error simulation rules
    },
}
```

### 3. Error Handling

- **Use appropriate fallback settings**: Allow fallbacks for temporary errors
- **Provide meaningful error messages**: Help with debugging
- **Set realistic status codes**: Match actual provider behavior

### 4. Performance Considerations

- **Limit regex complexity**: Simple patterns perform better
- **Use probability wisely**: Don't mock 100% in production
- **Monitor statistics**: Watch for unexpected behavior

### 5. Testing

```go
func TestYourAppWithMocking(t *testing.T) {
    // Create predictable mock responses for testing
    plugin, _ := mocker.NewMockerPlugin(mocker.MockerConfig{
        Enabled: true,
        Rules: []mocker.MockRule{
            {
                Name:        "test-success",
                Enabled:     true,
                Probability: 1.0, // Always trigger for consistent tests
                Responses: []mocker.Response{
                    {
                        Type: mocker.ResponseTypeSuccess,
                        Content: &mocker.SuccessResponse{
                            Message: "Predictable test response",
                        },
                    },
                },
            },
        },
    })

    // Use plugin in your tests...
}
```

## Troubleshooting

### Common Issues

#### 1. Plugin Not Triggering

**Problem**: Requests pass through to real providers instead of being mocked.

**Solutions**:

- Check `Enabled: true` in config
- Verify rule conditions match your requests
- Check rule `Probability` (should be > 0)
- Ensure rule is `Enabled: true`

#### 2. Configuration Validation Errors

**Problem**: `NewMockerPlugin()` returns validation errors.

**Common Issues**:

```go
// ❌ Missing rule name
{
    Name: "", // Error: rule name required
}

// ❌ Invalid probability
{
    Probability: 1.5, // Error: must be 0.0-1.0
}

// ❌ Invalid response type
{
    Type: "invalid", // Error: must be "success" or "error"
}

// ❌ Missing response content
{
    Type: mocker.ResponseTypeSuccess,
    // Error: Content required for success type
}
```

#### 3. Statistics Not Updating

**Problem**: `GetStats()` shows zero values.

**Solutions**:

- Ensure rules are actually matching (check conditions)
- Verify plugin is enabled
- Call `GetStats()` before `Cleanup()` (cleanup clears stats)

#### 4. Regex Not Matching

**Problem**: `MessageRegex` conditions not working.

**Solutions**:

```go
// ❌ Invalid regex
MessageRegex: stringPtr("[invalid"), // Syntax error

// ✅ Valid regex patterns
MessageRegex: stringPtr(`(?i)hello`),           // Case-insensitive
MessageRegex: stringPtr(`error|fail|problem`), // Multiple options
MessageRegex: stringPtr(`\d+`),                // Numbers only
```

#### 5. Unexpected Fallback Behavior

**Problem**: Errors don't trigger fallbacks as expected.

**Solutions**:

```go
// Control fallback behavior explicitly
Response{
    Type: mocker.ResponseTypeError,
    AllowFallbacks: boolPtr(true),  // Explicitly allow fallbacks
    // or
    AllowFallbacks: boolPtr(false), // Explicitly block fallbacks
    // or
    AllowFallbacks: nil,            // Default behavior (allow)
}
```

#### 6. Latency Not Working

**Problem**: Latency simulation has no effect or causes errors.

**Common Issue**: Using raw integers instead of `time.Duration` values.

**Solutions**:

```go
// ❌ WRONG: Raw integers (these are nanoseconds, barely noticeable)
Latency: &mocker.Latency{
    Type: mocker.LatencyTypeFixed,
    Min:  100,    // 100 nanoseconds = 0.0001ms
    Max:  500,    // 500 nanoseconds = 0.0005ms
}

// ✅ CORRECT: Use time.Duration constants
Latency: &mocker.Latency{
    Type: mocker.LatencyTypeFixed,
    Min:  100 * time.Millisecond,  // 100ms
}

// ✅ CORRECT: Various duration examples
Latency: &mocker.Latency{
    Type: mocker.LatencyTypeUniform,
    Min:  50 * time.Millisecond,   // 50ms
    Max:  200 * time.Millisecond,  // 200ms
}

// ✅ CORRECT: Other duration units
Min: 1 * time.Second,              // 1 second
Min: 500 * time.Microsecond,       // 500 microseconds
Min: 2500 * time.Nanosecond,       // 2500 nanoseconds (rarely used)
```

### Debug Mode

Enable debug logging to troubleshoot issues:

```go
client, initErr := bifrost.Init(schemas.BifrostConfig{
    Account: &account,
    Plugins: []schemas.Plugin{plugin},
    Logger:  bifrost.NewDefaultLogger(schemas.LogLevelDebug), // Enable debug logs
})
```

### Validation Testing

Test your configuration before deployment:

```go
func validateMockerConfig(config mocker.MockerConfig) error {
    _, err := mocker.NewMockerPlugin(config)
    return err
}

// Test in your code
if err := validateMockerConfig(yourConfig); err != nil {
    log.Fatalf("Invalid mocker configuration: %v", err)
}
```

---

**Need help?** Check the [Bifrost documentation](../../docs/usage/http-transport/configuration/plugins.md) or open an issue on GitHub.

```

```
