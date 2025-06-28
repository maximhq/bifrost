# ⚡ Memory Management

Bifrost uses advanced memory optimization techniques including object pooling, worker pool management, and configurable concurrency to deliver high performance with minimal memory footprint.

## 📑 Table of Contents

- [💾 Object Pooling](#-object-pooling)
- [⚙️ Worker Pool Configuration](#️-worker-pool-configuration)
- [🏗️ Concurrency Settings](#️-concurrency-settings)
- [📊 Performance Tuning](#-performance-tuning)
- [🔧 Advanced Configuration](#-advanced-configuration)

---

## 💾 Object Pooling

Bifrost uses sophisticated object pooling to minimize garbage collection pressure and reduce memory allocations.

### Pool Configuration

<details>
<summary><strong>🔧 Go Package - Pool Settings</strong></summary>

```go
import "github.com/maximhq/bifrost/core/schemas"

func main() {
    account := &MyAccount{}

    client, err := bifrost.Init(schemas.BifrostConfig{
        Account:         account,
        InitialPoolSize: 1000,  // Pool size for high-throughput applications
    })
    if err != nil {
        panic(err)
    }
    defer client.Cleanup()
}
```

**Pool Size Guidelines:**

| Application Type       | Recommended Pool Size | Memory Usage |
| ---------------------- | --------------------- | ------------ |
| **Low Traffic**        | `50-100`              | ~1-2 MB      |
| **Medium Traffic**     | `200-300`             | ~4-6 MB      |
| **High Traffic**       | `500-1000`            | ~10-20 MB    |
| **Ultra High Traffic** | `1000+`               | ~20+ MB      |

</details>

<details>
<summary><strong>🌐 HTTP Transport - Pool Settings</strong></summary>

```bash
# Docker deployment with pool configuration
docker run -p 8080:8080 \
  -e APP_POOL_SIZE=1000 \
  -v $(pwd)/config.json:/app/config/config.json \
  maximhq/bifrost
```

```bash
# Go binary with pool configuration
bifrost-http -config config.json -pool-size 1000
```

</details>

### Pooled Objects

Bifrost pools the following objects for optimal performance:

- **📨 Channel Messages**: Request/response communication objects
- **📞 Response Channels**: HTTP response channels
- **❌ Error Channels**: Error communication channels
- **🔄 Provider Responses**: Provider-specific response structures

---

## ⚙️ Worker Pool Configuration

Each provider maintains its own isolated worker pool for processing requests.

### Worker Pool Settings

<details>
<summary><strong>🔧 Go Package - Worker Configuration</strong></summary>

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
            Concurrency: 20,   // Number of worker threads
            BufferSize:  200,  // Request queue size
        },
    }, nil
}
```

**Worker Pool Architecture:**

```
Provider Queue [Buffer: 200]
├── Worker 1 ──► AI Provider API
├── Worker 2 ──► AI Provider API
├── Worker 3 ──► AI Provider API
└── Worker N ──► AI Provider API
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - Worker Configuration</strong></summary>

```json
{
  "providers": {
    "openai": {
      "concurrency_and_buffer_size": {
        "concurrency": 20,
        "buffer_size": 200
      }
    }
  }
}
```

</details>

### Configuration Guidelines

| Load Level     | Concurrency | Buffer Size | Memory Impact |
| -------------- | ----------- | ----------- | ------------- |
| **Low**        | 5-10        | 50-100      | Low           |
| **Medium**     | 10-20       | 100-200     | Medium        |
| **High**       | 20-50       | 200-500     | High          |
| **Ultra High** | 50-100      | 500-1000    | Very High     |

---

## 🏗️ Concurrency Settings

### Drop Excess Requests

<details>
<summary><strong>🔧 Go Package - Request Overflow</strong></summary>

```go
func main() {
    account := &MyAccount{}

    client, err := bifrost.Init(schemas.BifrostConfig{
        Account:            account,
        DropExcessRequests: true,  // Drop requests when buffers are full
    })
    if err != nil {
        panic(err)
    }
    defer client.Cleanup()

    // Under high load, excess requests will be dropped instead of queued
    // This prevents memory accumulation and improves response times
}
```

**When to use `DropExcessRequests`:**

- ✅ **High traffic applications** - Prevent memory buildup
- ✅ **Real-time systems** - Prefer fast failures over slow responses
- ✅ **Load testing** - Avoid artificial queuing effects
- ❌ **Critical requests** - When every request must be processed

</details>

<details>
<summary><strong>🌐 HTTP Transport - Request Overflow</strong></summary>

```bash
# Docker with drop excess requests
docker run -p 8080:8080 \
  -e APP_DROP_EXCESS_REQUESTS=true \
  maximhq/bifrost
```

```bash
# Go binary with drop excess requests
bifrost-http -config config.json -drop-excess-requests=true
```

</details>

---

## 📊 Performance Tuning

### Memory vs Performance Trade-offs

| Configuration Type   | Memory Usage | Processing Speed | Best For              |
| -------------------- | ------------ | ---------------- | --------------------- |
| **High Performance** | Higher       | Faster           | Production, high-load |
| **Memory Efficient** | Lower        | Slightly slower  | Resource-constrained  |
| **Balanced**         | Medium       | Good             | Most applications     |

### Optimization Examples

<details>
<summary><strong>⚡ High Performance Configuration</strong></summary>

```go
// High throughput, maximum performance
client, err := bifrost.Init(schemas.BifrostConfig{
    Account:            account,
    InitialPoolSize:    20000,  // Large pool for memory optimization
    DropExcessRequests: true,   // Fail-fast when overloaded
})

// Provider configuration
schemas.ProviderConfig{
    ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
        Concurrency: 100,    // High concurrency for throughput
        BufferSize:  1000,   // Large buffer for request spikes
    },
}
```

**Use Cases:**

- Production APIs (>5000 RPS)
- Real-time applications
- Latency-critical systems

</details>

<details>
<summary><strong>💾 Memory Efficient Configuration</strong></summary>

```go
// Lower memory usage, slightly higher latency
client, err := bifrost.Init(schemas.BifrostConfig{
    Account:            account,
    InitialPoolSize:    100,   // Standard pool size
    DropExcessRequests: false, // Queue requests instead of dropping
})

// Provider configuration
schemas.ProviderConfig{
    ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
        Concurrency: 10,    // Moderate concurrency
        BufferSize:  50,    // Standard buffer size
    },
}
```

**Use Cases:**

- Resource-constrained environments
- Development/testing
- Low-traffic applications

</details>

<details>
<summary><strong>⚖️ Balanced Configuration</strong></summary>

```go
// Good balance of memory and performance
client, err := bifrost.Init(schemas.BifrostConfig{
    Account:            account,
    InitialPoolSize:    500,   // Medium pool size
    DropExcessRequests: false, // Queue requests
})

// Provider configuration
schemas.ProviderConfig{
    ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
        Concurrency: 20,    // Good concurrency
        BufferSize:  200,   // Reasonable buffer
    },
}
```

**Use Cases:**

- Most production applications
- General purpose deployments
- Standard web applications

</details>

---

## 🔧 Advanced Configuration

### Environment-Specific Settings

<details>
<summary><strong>🌍 Multi-Environment Memory Tuning</strong></summary>

```go
type EnvironmentAccount struct {
    environment string
}

func (a *EnvironmentAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        ConcurrencyAndBufferSize: a.getConcurrencyConfig(),
    }, nil
}

func (a *EnvironmentAccount) getConcurrencyConfig() schemas.ConcurrencyAndBufferSize {
    switch a.environment {
    case "production":
        return schemas.ConcurrencyAndBufferSize{
            Concurrency: 50,   // High concurrency for production
            BufferSize:  500,  // Large buffer for production load
        }
    case "staging":
        return schemas.ConcurrencyAndBufferSize{
            Concurrency: 20,   // Medium concurrency for staging
            BufferSize:  200,  // Medium buffer for staging
        }
    case "development":
        return schemas.ConcurrencyAndBufferSize{
            Concurrency: 5,    // Low concurrency for development
            BufferSize:  50,   // Small buffer for development
        }
    default:
        return schemas.DefaultConcurrencyAndBufferSize
    }
}
```

**Configuration Matrix:**

| Environment     | Concurrency | Buffer Size | Pool Size | Memory Usage |
| --------------- | ----------- | ----------- | --------- | ------------ |
| **Development** | 5           | 50          | 100       | ~2 MB        |
| **Staging**     | 20          | 200         | 500       | ~10 MB       |
| **Production**  | 50          | 500         | 2000      | ~40 MB       |

</details>

### Monitoring Memory Usage

<details>
<summary><strong>📊 Memory Monitoring</strong></summary>

```go
import (
    "runtime"
    "log"
)

func monitorMemory() {
    var m runtime.MemStats
    runtime.ReadMemStats(&m)

    log.Printf("Alloc = %d KB", bToKb(m.Alloc))
    log.Printf("TotalAlloc = %d KB", bToKb(m.TotalAlloc))
    log.Printf("Sys = %d KB", bToKb(m.Sys))
    log.Printf("NumGC = %v", m.NumGC)
}

func bToKb(b uint64) uint64 {
    return b / 1024
}
```

**Key Metrics to Monitor:**

- **Heap Size**: Current memory allocation
- **GC Frequency**: Garbage collection frequency
- **Pool Hit Rate**: Object pool utilization
- **Queue Depth**: Average request queue depth

</details>

### Hardware Recommendations

| Expected Load     | Recommended Instance | Buffer Size | Pool Size | Memory |
| ----------------- | -------------------- | ----------- | --------- | ------ |
| **< 1000 RPS**    | t3.medium            | 200         | 500       | 4 GB   |
| **1000-3000 RPS** | t3.large             | 500         | 1000      | 8 GB   |
| **3000-5000 RPS** | t3.xlarge            | 1000        | 2000      | 16 GB  |
| **> 5000 RPS**    | c5.2xlarge+          | 2000+       | 5000+     | 32 GB+ |

### Troubleshooting Memory Issues

| Issue                 | Symptoms              | Solution                            |
| --------------------- | --------------------- | ----------------------------------- |
| **High Memory Usage** | Excessive heap growth | Reduce pool size or concurrency     |
| **Frequent GC**       | High GC frequency     | Increase pool size                  |
| **Memory Leaks**      | Continuous growth     | Check for unclosed resources        |
| **Queue Buildup**     | High queue depth      | Increase concurrency or buffer size |
| **Out of Memory**     | System crashes        | Enable `DropExcessRequests`         |

---

## 📚 Related Documentation

- **[🌐 Networking](networking.md)** - Network configuration and connection pooling
- **[📊 Observability](observability.md)** - Memory monitoring and metrics
- **[🏗️ Architecture](../architecture/system-overview.md)** - System design and performance
- **[📊 Benchmarks](../benchmarks.md)** - Performance benchmarks and tuning

---

**Need help?** Check our [❓ FAQ](../guides/faq.md) or [🔧 Troubleshooting Guide](../guides/troubleshooting.md).
