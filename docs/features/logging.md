# 📝 Logging System

Bifrost provides a comprehensive, structured logging system with configurable levels, custom loggers, and integration capabilities for both Go package and HTTP transport usage.

## 📋 Overview

**Logging Features:**

- ✅ **Structured Logging** - JSON-formatted logs with contextual fields
- ✅ **Configurable Levels** - Debug, Info, Warn, Error with filtering
- ✅ **Custom Logger Integration** - Use your existing logging infrastructure
- ✅ **Request Tracing** - Trace requests across the entire pipeline
- ✅ **Performance Metrics** - Log latency, token usage, and costs
- ✅ **Error Context** - Detailed error information with stack traces

**Benefits:**

- 🔍 **Full Observability** - Complete visibility into system behavior
- 🐛 **Debugging** - Trace issues through the entire request flow
- 📊 **Analytics** - Extract insights from structured log data
- 🚨 **Alerting** - Monitor errors and performance issues
- 📈 **Performance** - Minimal overhead structured logging

---

## ⚡ Quick Examples

<details open>
<summary><strong>🔧 Go Package Usage</strong></summary>

```go
package main

import (
    "context"
    "github.com/maximhq/bifrost/core"
    "github.com/maximhq/bifrost/core/schemas"
)

func main() {
    // Use default logger
    logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)

    // Initialize with logger
    client, err := bifrost.Init(schemas.BifrostConfig{
        Account: &myAccount,
        Logger:  logger,
    })
    if err != nil {
        panic(err)
    }
    defer client.Cleanup()

    // Logger automatically captures request/response data
    response, err := client.ChatCompletion(context.Background(), schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "gpt-4o-mini",
        Input:    requestInput,
    })

    // Manual logging with context
    logger.Info("Chat completion successful",
        schemas.LogField{Key: "provider", Value: "openai"},
        schemas.LogField{Key: "model", Value: "gpt-4o-mini"},
        schemas.LogField{Key: "tokens", Value: response.Usage.TotalTokens},
    )
}
```

**[📖 Complete Go Package Guide →](../usage/go-package/)**

</details>

<details>
<summary><strong>🌐 HTTP Transport Usage</strong></summary>

**Start with logging configuration:**

```bash
# Start with Info level logging
docker run -p 8080:8080 \
  -v $(pwd)/config.json:/app/config/config.json \
  -e LOG_LEVEL=info \
  -e LOG_FORMAT=json \
  maximhq/bifrost

# Start with Debug level for troubleshooting
docker run -p 8080:8080 \
  -v $(pwd)/config.json:/app/config/config.json \
  -e LOG_LEVEL=debug \
  -e LOG_FORMAT=json \
  maximhq/bifrost
```

**Request automatically logged:**

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

**Log output:**

```json
{
  "timestamp": "2024-01-15T10:30:45.123Z",
  "level": "info",
  "message": "Chat completion request completed",
  "fields": {
    "provider": "openai",
    "model": "gpt-4o-mini",
    "latency_ms": 1234,
    "tokens_total": 25,
    "status": "success"
  }
}
```

**[📖 Complete HTTP Transport Guide →](../usage/http-api/)**

</details>

---

## 🎯 Log Levels

### Available Levels

| Level     | When to Use                         | Example                                  |
| --------- | ----------------------------------- | ---------------------------------------- |
| **Debug** | Development, troubleshooting        | Request payloads, detailed flow          |
| **Info**  | Normal operations, request tracking | Successful requests, metrics             |
| **Warn**  | Recoverable issues, fallbacks       | Rate limits, retries, fallback usage     |
| **Error** | Serious issues, failed requests     | Authentication failures, provider errors |

### Level Configuration

<details>
<summary><strong>🔧 Go Package - Log Level Configuration</strong></summary>

```go
// Debug level - Verbose logging for development
debugLogger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)

// Info level - Production logging
infoLogger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)

// Warn level - Only warnings and errors
warnLogger := bifrost.NewDefaultLogger(schemas.LogLevelWarn)

// Error level - Only errors
errorLogger := bifrost.NewDefaultLogger(schemas.LogLevelError)

// Use with Bifrost
client, err := bifrost.Init(schemas.BifrostConfig{
    Account: account,
    Logger:  infoLogger,  // Production setting
})
```

**Debug Level Output Example:**

```json
{
  "timestamp": "2024-01-15T10:30:45.123Z",
  "level": "debug",
  "message": "Sending request to provider",
  "fields": {
    "provider": "openai",
    "model": "gpt-4o-mini",
    "url": "https://api.openai.com/v1/chat/completions",
    "payload_size": 245,
    "request_id": "req_123"
  }
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - Log Level Configuration</strong></summary>

**Environment variable:**

```bash
export LOG_LEVEL=debug  # debug, info, warn, error
export LOG_FORMAT=json  # json, text
```

**Docker run:**

```bash
docker run -p 8080:8080 \
  -v $(pwd)/config.json:/app/config/config.json \
  -e LOG_LEVEL=info \
  -e LOG_FORMAT=json \
  maximhq/bifrost
```

**Binary flag:**

```bash
bifrost-http -config config.json -log-level info -log-format json
```

</details>

---

## 🔍 Custom Logger Integration

### Integrate with Existing Loggers

<details>
<summary><strong>🔧 Go Package - Custom Logger Implementation</strong></summary>

```go
import (
    "log/slog"
    "github.com/maximhq/bifrost/core/schemas"
)

// Implement Bifrost Logger interface
type SlogBifrostLogger struct {
    logger *slog.Logger
}

func (l *SlogBifrostLogger) Debug(msg string, fields ...schemas.LogField) {
    attrs := make([]slog.Attr, len(fields))
    for i, field := range fields {
        attrs[i] = slog.Any(field.Key, field.Value)
    }
    l.logger.LogAttrs(context.Background(), slog.LevelDebug, msg, attrs...)
}

func (l *SlogBifrostLogger) Info(msg string, fields ...schemas.LogField) {
    attrs := make([]slog.Attr, len(fields))
    for i, field := range fields {
        attrs[i] = slog.Any(field.Key, field.Value)
    }
    l.logger.LogAttrs(context.Background(), slog.LevelInfo, msg, attrs...)
}

func (l *SlogBifrostLogger) Warn(msg string, fields ...schemas.LogField) {
    attrs := make([]slog.Attr, len(fields))
    for i, field := range fields {
        attrs[i] = slog.Any(field.Key, field.Value)
    }
    l.logger.LogAttrs(context.Background(), slog.LevelWarn, msg, attrs...)
}

func (l *SlogBifrostLogger) Error(msg string, fields ...schemas.LogField) {
    attrs := make([]slog.Attr, len(fields))
    for i, field := range fields {
        attrs[i] = slog.Any(field.Key, field.Value)
    }
    l.logger.LogAttrs(context.Background(), slog.LevelError, msg, attrs...)
}

// Use custom logger
func main() {
    slogger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
        Level: slog.LevelInfo,
    }))

    customLogger := &SlogBifrostLogger{logger: slogger}

    client, err := bifrost.Init(schemas.BifrostConfig{
        Account: account,
        Logger:  customLogger,
    })
}
```

**Logrus Integration Example:**

```go
import (
    "github.com/sirupsen/logrus"
    "github.com/maximhq/bifrost/core/schemas"
)

type LogrusBifrostLogger struct {
    logger *logrus.Logger
}

func (l *LogrusBifrostLogger) Info(msg string, fields ...schemas.LogField) {
    logFields := logrus.Fields{}
    for _, field := range fields {
        logFields[field.Key] = field.Value
    }
    l.logger.WithFields(logFields).Info(msg)
}

// ... implement other methods

func main() {
    logrusLogger := logrus.New()
    logrusLogger.SetFormatter(&logrus.JSONFormatter{})

    customLogger := &LogrusBifrostLogger{logger: logrusLogger}

    client, err := bifrost.Init(schemas.BifrostConfig{
        Account: account,
        Logger:  customLogger,
    })
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - External Logging</strong></summary>

**Configure external log aggregation:**

**Fluentd configuration:**

```yaml
# fluent.conf
<source>
@type forward
port 24224
bind 0.0.0.0
</source>

<match bifrost.**>
@type elasticsearch
host elasticsearch
port 9200
index_name bifrost
type_name logs
</match>
```

**Docker with log driver:**

```bash
docker run -p 8080:8080 \
  --log-driver=fluentd \
  --log-opt fluentd-address=localhost:24224 \
  --log-opt tag="bifrost.http" \
  -v $(pwd)/config.json:/app/config/config.json \
  maximhq/bifrost
```

**ELK Stack integration:**

```bash
# Send logs to Logstash
docker run -p 8080:8080 \
  --log-driver=gelf \
  --log-opt gelf-address=udp://logstash:12201 \
  -v $(pwd)/config.json:/app/config/config.json \
  maximhq/bifrost
```

</details>

---

## 📊 Structured Logging Fields

### Standard Log Fields

Bifrost automatically includes these fields in logs:

| Field          | Description               | Example                      |
| -------------- | ------------------------- | ---------------------------- |
| `timestamp`    | ISO 8601 timestamp        | `2024-01-15T10:30:45.123Z`   |
| `level`        | Log level                 | `info`, `error`              |
| `message`      | Human-readable message    | `Chat completion successful` |
| `provider`     | AI provider used          | `openai`, `anthropic`        |
| `model`        | Model name                | `gpt-4o-mini`                |
| `request_id`   | Unique request identifier | `req_abc123`                 |
| `latency_ms`   | Request latency in ms     | `1234`                       |
| `tokens_total` | Total tokens used         | `25`                         |
| `status`       | Request status            | `success`, `error`           |

### Request Flow Logging

<details>
<summary><strong>🔧 Go Package - Request Tracing</strong></summary>

```go
// Example of logs generated for a single request
func ExampleRequestFlow() {
    client, _ := bifrost.Init(schemas.BifrostConfig{
        Account: account,
        Logger:  bifrost.NewDefaultLogger(schemas.LogLevelDebug),
    })

    response, err := client.ChatCompletion(context.Background(), schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "gpt-4o-mini",
        Input:    requestInput,
    })
}

// Generated logs:
// 1. Request start
{
  "timestamp": "2024-01-15T10:30:45.100Z",
  "level": "debug",
  "message": "Starting chat completion request",
  "fields": {
    "provider": "openai",
    "model": "gpt-4o-mini",
    "request_id": "req_abc123"
  }
}

// 2. Provider selection
{
  "timestamp": "2024-01-15T10:30:45.105Z",
  "level": "debug",
  "message": "Selected API key for provider",
  "fields": {
    "provider": "openai",
    "key_id": "sk-...xyz",
    "weight": 0.7
  }
}

// 3. HTTP request
{
  "timestamp": "2024-01-15T10:30:45.110Z",
  "level": "debug",
  "message": "Sending HTTP request to provider",
  "fields": {
    "provider": "openai",
    "url": "https://api.openai.com/v1/chat/completions",
    "method": "POST",
    "payload_size": 245
  }
}

// 4. Successful response
{
  "timestamp": "2024-01-15T10:30:46.344Z",
  "level": "info",
  "message": "Chat completion successful",
  "fields": {
    "provider": "openai",
    "model": "gpt-4o-mini",
    "latency_ms": 1234,
    "tokens_prompt": 15,
    "tokens_completion": 10,
    "tokens_total": 25,
    "request_id": "req_abc123"
  }
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - Request Lifecycle</strong></summary>

**HTTP request automatically generates structured logs:**

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Request-ID: my-custom-id" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

**Generated log sequence:**

```json
// 1. Request received
{
  "timestamp": "2024-01-15T10:30:45.100Z",
  "level": "info",
  "message": "HTTP request received",
  "fields": {
    "method": "POST",
    "path": "/v1/chat/completions",
    "remote_addr": "192.168.1.100",
    "user_agent": "curl/7.68.0",
    "request_id": "my-custom-id"
  }
}

// 2. Request processing
{
  "timestamp": "2024-01-15T10:30:45.110Z",
  "level": "debug",
  "message": "Processing chat completion request",
  "fields": {
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages_count": 1,
    "request_id": "my-custom-id"
  }
}

// 3. Provider response
{
  "timestamp": "2024-01-15T10:30:46.344Z",
  "level": "info",
  "message": "Request completed successfully",
  "fields": {
    "provider": "openai",
    "model": "gpt-4o-mini",
    "status_code": 200,
    "latency_ms": 1244,
    "tokens_total": 25,
    "request_id": "my-custom-id"
  }
}
```

</details>

---

## 🚨 Error Logging

### Error Context and Stack Traces

<details>
<summary><strong>🔧 Go Package - Error Handling</strong></summary>

```go
response, err := client.ChatCompletion(ctx, request)
if err != nil {
    var bifrostErr *schemas.BifrostError
    if errors.As(err, &bifrostErr) {
        // Bifrost automatically logs detailed error context
        // Log entry includes:
        // - Error type and code
        // - Provider response details
        // - Fallback attempt information
        // - Stack trace if available
    }
}

// Example error log:
{
  "timestamp": "2024-01-15T10:30:46.500Z",
  "level": "error",
  "message": "Provider request failed",
  "fields": {
    "provider": "openai",
    "model": "gpt-4o",
    "error_type": "rate_limit_error",
    "error_code": "rate_limit_exceeded",
    "status_code": 429,
    "provider_message": "Rate limit reached for requests",
    "retry_after": 60,
    "fallback_attempted": true,
    "fallback_provider": "anthropic",
    "request_id": "req_abc123"
  }
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - Error Responses</strong></summary>

**Failed request generates detailed error logs:**

```bash
# Request that will fail due to invalid API key
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

**Error log output:**

```json
{
  "timestamp": "2024-01-15T10:30:46.500Z",
  "level": "error",
  "message": "Authentication failed",
  "fields": {
    "provider": "openai",
    "model": "gpt-4o-mini",
    "error_type": "authentication_error",
    "error_code": "invalid_api_key",
    "status_code": 401,
    "provider_response": "Incorrect API key provided",
    "request_id": "req_abc123",
    "client_ip": "192.168.1.100"
  }
}
```

</details>

---

## 📈 Performance and Usage Logging

### Metrics and Analytics

<details>
<summary><strong>🔧 Go Package - Performance Metrics</strong></summary>

```go
// Custom logger to capture performance metrics
type MetricsLogger struct {
    baseLogger schemas.Logger
    metrics    *prometheus.Registry
}

func (m *MetricsLogger) Info(msg string, fields ...schemas.LogField) {
    // Log normally
    m.baseLogger.Info(msg, fields...)

    // Extract metrics from log fields
    for _, field := range fields {
        switch field.Key {
        case "latency_ms":
            if latency, ok := field.Value.(float64); ok {
                // Update latency histogram
            }
        case "tokens_total":
            if tokens, ok := field.Value.(int); ok {
                // Update token counter
            }
        }
    }
}

// Usage tracking example
func trackModelUsage(logger schemas.Logger) {
    logger.Info("Model usage tracked",
        schemas.LogField{Key: "provider", Value: "openai"},
        schemas.LogField{Key: "model", Value: "gpt-4o-mini"},
        schemas.LogField{Key: "tokens_prompt", Value: 15},
        schemas.LogField{Key: "tokens_completion", Value: 10},
        schemas.LogField{Key: "cost_usd", Value: 0.00025},
        schemas.LogField{Key: "user_id", Value: "user_123"},
        schemas.LogField{Key: "session_id", Value: "session_456"},
    )
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - Usage Analytics</strong></summary>

**Automatic usage logging with custom fields:**

```bash
# Add custom tracking headers
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-User-ID: user_123" \
  -H "X-Session-ID: session_456" \
  -H "X-App-Version: 1.2.3" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

**Generated analytics log:**

```json
{
  "timestamp": "2024-01-15T10:30:46.344Z",
  "level": "info",
  "message": "Usage metrics",
  "fields": {
    "provider": "openai",
    "model": "gpt-4o-mini",
    "tokens_prompt": 15,
    "tokens_completion": 10,
    "tokens_total": 25,
    "estimated_cost_usd": 0.00025,
    "latency_ms": 1234,
    "user_id": "user_123",
    "session_id": "session_456",
    "app_version": "1.2.3",
    "request_id": "req_abc123"
  }
}
```

</details>

---

## 🔧 Advanced Configuration

### Log Filtering and Sampling

<details>
<summary><strong>🔧 Go Package - Custom Filtering</strong></summary>

```go
// Implement filtered logger
type FilteredLogger struct {
    baseLogger schemas.Logger
    minLevel   schemas.LogLevel
    sampleRate float64  // 0.0 to 1.0
}

func (f *FilteredLogger) Info(msg string, fields ...schemas.LogField) {
    // Sample logs in production
    if rand.Float64() > f.sampleRate {
        return
    }

    // Filter sensitive information
    filteredFields := make([]schemas.LogField, 0, len(fields))
    for _, field := range fields {
        if field.Key != "api_key" && field.Key != "secret" {
            filteredFields = append(filteredFields, field)
        }
    }

    f.baseLogger.Info(msg, filteredFields...)
}

// Use filtered logger for production
filteredLogger := &FilteredLogger{
    baseLogger: bifrost.NewDefaultLogger(schemas.LogLevelInfo),
    minLevel:   schemas.LogLevelInfo,
    sampleRate: 0.1,  // Log only 10% of requests
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - Log Configuration</strong></summary>

**Environment-based configuration:**

```bash
# Production: Limited logging, JSON format
export LOG_LEVEL=info
export LOG_FORMAT=json
export LOG_SAMPLE_RATE=0.1
export LOG_EXCLUDE_FIELDS="authorization,api_key"

# Development: Verbose logging, readable format
export LOG_LEVEL=debug
export LOG_FORMAT=text
export LOG_SAMPLE_RATE=1.0
export LOG_EXCLUDE_FIELDS=""

docker run -p 8080:8080 \
  -e LOG_LEVEL \
  -e LOG_FORMAT \
  -e LOG_SAMPLE_RATE \
  -e LOG_EXCLUDE_FIELDS \
  maximhq/bifrost
```

</details>

---

## 🔗 Integration Examples

### ELK Stack Integration

```yaml
# docker-compose.yml
version: "3.8"
services:
  bifrost:
    image: maximhq/bifrost:latest
    ports:
      - "8080:8080"
    environment:
      - LOG_LEVEL=info
      - LOG_FORMAT=json
    logging:
      driver: "json-file"
      options:
        max-size: "100m"
        max-file: "3"
    volumes:
      - ./config.json:/app/config/config.json

  filebeat:
    image: elastic/filebeat:7.15.0
    volumes:
      - ./filebeat.yml:/usr/share/filebeat/filebeat.yml
      - /var/lib/docker/containers:/var/lib/docker/containers:ro
      - /var/run/docker.sock:/var/run/docker.sock:ro

  elasticsearch:
    image: elasticsearch:7.15.0
    environment:
      - discovery.type=single-node
    ports:
      - "9200:9200"

  kibana:
    image: kibana:7.15.0
    ports:
      - "5601:5601"
    environment:
      - ELASTICSEARCH_HOSTS=http://elasticsearch:9200
```

### Prometheus Metrics from Logs

```go
// Extract metrics from logs
type PrometheusLogExporter struct {
    requestDuration prometheus.Histogram
    tokenUsage      prometheus.Counter
    errorRate       prometheus.Counter
}

func (p *PrometheusLogExporter) Info(msg string, fields ...schemas.LogField) {
    for _, field := range fields {
        switch field.Key {
        case "latency_ms":
            if latency, ok := field.Value.(float64); ok {
                p.requestDuration.Observe(latency / 1000.0)
            }
        case "tokens_total":
            if tokens, ok := field.Value.(float64); ok {
                p.tokenUsage.Add(tokens)
            }
        }
    }
}
```

---

## 📚 Related Documentation

- **[📊 Observability](observability.md)** - Metrics and monitoring
- **[🔧 Configuration](../configuration/)** - Setup and configuration
- **[🔍 Troubleshooting](../guides/troubleshooting.md)** - Debug common issues
- **[🏗️ Architecture](../architecture/)** - System design and performance

---

**Need help?** Check our [❓ FAQ](../guides/faq.md) or [🔧 Troubleshooting Guide](../guides/troubleshooting.md).
