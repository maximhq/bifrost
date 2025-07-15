# Bifrost Circuit Breaker Plugin

The Circuit Breaker plugin for Bifrost provides automatic failure detection and recovery for AI provider requests. It monitors request failures and slow calls, automatically opening the circuit when thresholds are exceeded to prevent cascading failures.

## Quick Start

### Download the Plugin

   ```bash
   go get github.com/maximhq/bifrost/plugins/circuitbreaker
   ```

### Basic Usage

```go
package main

import (
    "context"
    "time"
    bifrost "github.com/maximhq/bifrost/core"
    "github.com/maximhq/bifrost/core/schemas"
    circuitbreaker "github.com/maximhq/bifrost/plugins/circuitbreaker"
)

func main() {
    // Create plugin with default configuration
    circuitbreakerPlugin, err := circuitbreaker.NewCircuitBreakerPlugin(circuitbreaker.CircuitBreakerConfig{
        FailureRateThreshold: 0.5, // 50% failure rate threshold
        SlowCallRateThreshold: 0.5, // 50% slow call rate threshold
        SlowCallDurationThreshold: 5 * time.Second,
        MinimumNumberOfCalls: 10,
        SlidingWindowType: circuitbreaker.CountBased, // Track last N calls
        SlidingWindowSize: 100, // Track last 100 calls
        PermittedNumberOfCallsInHalfOpenState: 5,
        MaxWaitDurationInHalfOpenState: 60 * time.Second,
    })
    if err != nil {
        panic(err)
    }

    // Initialize Bifrost with the plugin
    client, err := bifrost.Init(schemas.BifrostConfig{
        Account: &yourAccount,
        Plugins: []schemas.Plugin{circuitbreakerPlugin},
    })
    if err != nil {
        panic(err)
    }
    defer client.Cleanup()

    // Circuit breaker will automatically protect your requests
    response, err := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
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
}
```

### State Diagram of Circuit Breaker
![Circuit Breaker States](../../docs/media/plugins/circuit-breaker-states.png)

## Configuration

### CircuitBreakerConfig

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `FailureRateThreshold` | `float64` | `0.5` | Failure rate threshold (0.0 to 1.0) |
| `SlowCallRateThreshold` | `float64` | `0.5` | Slow call rate threshold (0.0 to 1.0) |
| `SlowCallDurationThreshold` | `time.Duration` | `5s` | Duration threshold for slow calls |
| `MinimumNumberOfCalls` | `int` | `10` | Minimum calls before evaluation |
| `SlidingWindowType` | `string` | `"count-based"` | `"count-based"` or `"time-based"` |
| `SlidingWindowSize` | `int` | `100` | Size of sliding window (calls for count-based, seconds for time-based) |
| `PermittedNumberOfCallsInHalfOpenState` | `int` | `5` | Calls allowed in half-open state |
| `MaxWaitDurationInHalfOpenState` | `time.Duration` | `60s` | Wait time before half-open transition |
| `Logger` | `schemas.Logger` | `bifrost.NewDefaultLogger(schemas.LogLevelInfo)` | Logger for circuit breaker operations |

### Sliding Window Types

The circuit breaker supports two types of sliding windows for collecting metrics:

#### Count-Based Sliding Window
- **Type**: `"count-based"`
- **Size**: Number of most recent calls to track
- **Behavior**: Maintains a fixed-size circular buffer of the last N calls
- **Use Case**: When you want to evaluate based on a specific number of recent requests
- **Example**: Track the last 100 calls to evaluate failure rates

#### Time-Based Sliding Window
- **Type**: `"time-based"`
- **Size**: Duration in seconds to look back
- **Behavior**: Maintains all calls within the specified time window
- **Use Case**: When you want to evaluate based on a time period
- **Example**: Track all calls in the last 5 minutes to evaluate failure rates

### Configuration Examples

#### Count-Based Sliding Window (Default)

```go
config := circuitbreaker.CircuitBreakerConfig{
    FailureRateThreshold: 0.3, // 30% failure rate threshold
    SlowCallRateThreshold: 0.4, // 40% slow call rate threshold
    SlowCallDurationThreshold: 10 * time.Second,
    MinimumNumberOfCalls: 20,
    SlidingWindowType: circuitbreaker.CountBased, // Track last N calls
    SlidingWindowSize: 200, // Track last 200 calls
    PermittedNumberOfCallsInHalfOpenState: 3,
    MaxWaitDurationInHalfOpenState: 30 * time.Second,
}
```

#### Time-Based Sliding Window

```go
config := circuitbreaker.CircuitBreakerConfig{
    FailureRateThreshold: 0.3, // 30% failure rate threshold
    SlowCallRateThreshold: 0.4, // 40% slow call rate threshold
    SlowCallDurationThreshold: 10 * time.Second,
    MinimumNumberOfCalls: 20,
    SlidingWindowType: circuitbreaker.TimeBased, // Track calls in time window
    SlidingWindowSize: 300, // Track calls in last 300 seconds (5 minutes)
    PermittedNumberOfCallsInHalfOpenState: 3,
    MaxWaitDurationInHalfOpenState: 30 * time.Second,
}
```

### Logging Configuration

The circuit breaker plugin includes comprehensive logging to help you monitor its behavior. By default, it uses Bifrost's default logger with `Info` level logging. You can customize the logger by providing your own implementation:

```go
// Use custom logger
customLogger := yourCustomLoggerImplementation
config := circuitbreaker.CircuitBreakerConfig{
    FailureRateThreshold: 0.3,
    // ... other config options
    Logger: customLogger, // Use your custom logger
}

// Or use Bifrost's default logger with different log level
config := circuitbreaker.CircuitBreakerConfig{
    FailureRateThreshold: 0.3,
    // ... other config options
    Logger: bifrost.NewDefaultLogger(schemas.LogLevelDebug), // More verbose logging
}
```

## Circuit States

### CLOSED (Normal Operation)
- Requests are sent to providers normally
- Circuit breaker monitors failures and slow calls
- Metrics are collected in sliding window

### OPEN (Failure Protection)
- All requests are immediately rejected
- No provider calls are made
- Prevents cascading failures
- Automatically transitions to HALF_OPEN after wait duration

### HALF_OPEN (Recovery Testing)
- Limited number of requests are allowed through
- Success/failure determines next state
- Success → CLOSED (recovery complete)
- Failure → OPEN (still failing)

### CircuitState Type

The `CircuitState` type is an `int32` enum that represents the three possible states of the circuit breaker. It includes a `String()` method that provides human-readable string representations:

- `StateClosed` → `"CLOSED"`
- `StateOpen` → `"OPEN"`  
- `StateHalfOpen` → `"HALF_OPEN"`

This is useful for logging, debugging, and displaying circuit breaker status in monitoring dashboards.

### Error Classification

The circuit breaker distinguishes between different types of errors:
- **Server Errors (5xx)**: Considered failures that contribute to the failure rate
- **Rate Limit Errors (429)**: Considered failures that contribute to the failure rate
- **Other Client Errors (4xx)**: Considered successful for circuit breaker purposes (e.g., invalid requests, authentication errors)

This classification ensures that rate limiting issues and server-side problems trigger circuit breaker protection, while other client-side issues (like invalid API keys or malformed requests) don't.

## Monitoring

### Get Circuit State

```go
state := plugin.GetState(schemas.OpenAI)
switch state {
case circuitbreaker.StateClosed:
    fmt.Println("Circuit is CLOSED - normal operation")
case circuitbreaker.StateOpen:
    fmt.Println("Circuit is OPEN - requests blocked")
case circuitbreaker.StateHalfOpen:
    fmt.Println("Circuit is HALF_OPEN - testing recovery")
}
```

### Get Metrics

```go
metrics, err := plugin.GetMetrics(schemas.OpenAI)
if err == nil {
    fmt.Printf("Total Calls: %d\n", metrics.TotalCalls)
    fmt.Printf("Failed Calls: %d\n", metrics.FailedCalls)
    fmt.Printf("Failure Rate: %.2f%%\n", metrics.FailureRate*100)
    fmt.Printf("Slow Call Rate: %.2f%%\n", metrics.SlowCallRate*100)
}
```

## Advanced Operations

### Manual Circuit Control

The circuit breaker provides manual control functions for testing and emergency situations:

```go
// Force the circuit to open state (blocks all requests)
err := plugin.ForceOpen(schemas.OpenAI)
if err != nil {
    fmt.Printf("Error forcing circuit open: %v\n", err)
}

// Force the circuit to closed state (allows all requests)
err = plugin.ForceClose(schemas.OpenAI)
if err != nil {
    fmt.Printf("Error forcing circuit closed: %v\n", err)
}

// Reset the circuit breaker (clears all metrics and returns to closed state)
err = plugin.Reset(schemas.OpenAI)
if err != nil {
    fmt.Printf("Error resetting circuit: %v\n", err)
}
```

**Note**: Manual control should be used sparingly and primarily for testing or emergency situations. The automatic circuit breaker logic is designed to handle most scenarios optimally.

## Performance

The Circuit Breaker plugin is optimized for high-performance scenarios:

- **Atomic Operations**: Uses atomic counters for thread-safe statistics
- **Lock-Free Reads**: Read operations don't block other operations
- **Memory Efficient**: Pre-allocated data structures with minimal allocations

## Best Practices

1. **Monitor Metrics**: Regularly check circuit states and failure rates
2. **Adjust Thresholds**: Lower thresholds for critical services, higher for non-critical
3. **Test Recovery**: Verify half-open state works correctly in your environment
4. **Use Fallbacks**: Combine with Bifrost's fallback providers for maximum resilience

**Need help?** Check the [Bifrost documentation](../../docs/plugins.md) or open an issue on GitHub.

