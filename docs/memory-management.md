# Bifrost Memory and Concurrency Management

This document outlines the key configurations for managing memory usage and concurrency in Bifrost.

## 1. Initial Pool Size

The `InitialPoolSize` configuration determines the initial size of object pools that Bifrost creates during initialization. These pools are used to reduce runtime allocations and improve performance.

### Default Value

- Default: `100`

### Configuration

```golang
client, err := bifrost.Init(schemas.BifrostConfig{
    Account:            &yourAccount,
    InitialPoolSize:    500,  // Custom pool size
    DropExcessRequests: true,
})
```

### Impact

- Higher values reduce runtime allocations and latency
- Higher values increase memory usage
- Recommended to set based on your expected concurrent request volume

## 2. Drop Excess Requests

The `DropExcessRequests` flag controls how Bifrost handles requests when queues are full.

### Default Value

- Default: `false`

### Configuration

```golang
client, err := bifrost.Init(schemas.BifrostConfig{
    Account:            &yourAccount,
    InitialPoolSize:    500,
    DropExcessRequests: true,  // Enable dropping excess requests
})
```

### Behavior

- When `true`: Requests are dropped immediately if the queue is full
- When `false`: Requests wait for queue space to become available

## 3. Provider Concurrency and Buffer Size

Each provider can be configured with specific concurrency and buffer size settings.

### Default Values

- Default Concurrency: `10` workers
- Default Buffer Size: `100` requests

### Configuration

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY",
          "models": ["gpt-4o-mini"],
          "weight": 1.0
        }
      ],
      "concurrency_and_buffer_size": {
        "concurrency": 20, // Number of concurrent workers
        "buffer_size": 200 // Size of the request queue
      }
    }
  }
}
```

### Impact

- **Concurrency**: Controls the number of parallel workers processing requests

  - Higher values increase throughput but also increase resource usage
  - Should be set based on your provider's rate limits and server capacity

- **Buffer Size**: Controls the size of the request queue
  - Higher values allow more requests to be queued
  - Should be set based on your expected request volume and latency requirements

### Best Practices

1. Set `InitialPoolSize` to match your expected concurrent request volume
2. Enable `DropExcessRequests` if you want to fail fast when the system is overloaded
3. Configure provider concurrency based on:
   - Provider's rate limits
   - Available system resources
   - Expected request patterns
4. Set buffer size to handle expected request spikes while considering memory constraints

Remember that these configurations have direct impact on your system's performance and resource usage. It's recommended to test your configuration under expected load conditions to find the optimal settings for your use case.
