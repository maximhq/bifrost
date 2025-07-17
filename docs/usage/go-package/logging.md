# 📊 Logging

Complete guide to configuring and using custom logging in Bifrost for debugging, monitoring, and observability.

> **💡 Quick Start:** See the [30-second setup](../../quickstart/go-package.md) for basic logging configuration.

---

## 📋 Logging Overview

Bifrost's logging system provides:

- **Flexible log levels** (DEBUG, INFO, WARN, ERROR, FATAL)
- **Custom logger interfaces** for integration with your logging system
- **Request/response tracing** with correlation IDs
- **Performance metrics** and timing information
- **Provider-specific logging** for debugging integrations

```go
// Configure custom logger
client, initErr := bifrost.Init(schemas.BifrostConfig{
    Account: &MyAccount{},
    Logger:  customLogger, // Your logger implementation
})
```

---

## 🚀 Basic Logger Implementation

### **Standard Library Logger**

Use Go's standard library logger:

```go
package main

import (
    "log"
    "os"
    "github.com/maximhq/bifrost/core/schemas"
)

type StandardLogger struct {
    logger *log.Logger
    level  schemas.LogLevel
}

func NewStandardLogger(level schemas.LogLevel) *StandardLogger {
    return &StandardLogger{
        logger: log.New(os.Stdout, "[BIFROST] ", log.LstdFlags|log.Lshortfile),
        level:  level,
    }
}

func (l *StandardLogger) Log(level schemas.LogLevel, message string, fields ...schemas.LogField) {
    if level < l.level {
        return // Skip logs below current level
    }

    levelStr := l.levelToString(level)

    // Format fields
    fieldsStr := ""
    if len(fields) > 0 {
        fieldsMap := make(map[string]interface{})
        for _, field := range fields {
            fieldsMap[field.Key] = field.Value
        }
        fieldsStr = fmt.Sprintf(" %+v", fieldsMap)
    }

    l.logger.Printf("[%s] %s%s", levelStr, message, fieldsStr)
}

func (l *StandardLogger) levelToString(level schemas.LogLevel) string {
    switch level {
    case schemas.LogLevelDebug:
        return "DEBUG"
    case schemas.LogLevelInfo:
        return "INFO"
    case schemas.LogLevelWarn:
        return "WARN"
    case schemas.LogLevelError:
        return "ERROR"
    case schemas.LogLevelFatal:
        return "FATAL"
    default:
        return "UNKNOWN"
    }
}

// Usage
logger := NewStandardLogger(schemas.LogLevelInfo)
client, initErr := bifrost.Init(schemas.BifrostConfig{
    Account: &MyAccount{},
    Logger:  logger,
})
```

---

## ⚡ Advanced Logger Implementations

### **JSON Structured Logger**

Create structured JSON logs for production systems:

```go
package main

import (
    "encoding/json"
    "fmt"
    "os"
    "time"
    "github.com/maximhq/bifrost/core/schemas"
)

type JSONLogger struct {
    level     schemas.LogLevel
    service   string
    version   string
}

type LogEntry struct {
    Timestamp string                 `json:"timestamp"`
    Level     string                 `json:"level"`
    Message   string                 `json:"message"`
    Service   string                 `json:"service"`
    Version   string                 `json:"version"`
    Fields    map[string]interface{} `json:"fields,omitempty"`
}

func NewJSONLogger(level schemas.LogLevel, service, version string) *JSONLogger {
    return &JSONLogger{
        level:   level,
        service: service,
        version: version,
    }
}

func (l *JSONLogger) Log(level schemas.LogLevel, message string, fields ...schemas.LogField) {
    if level < l.level {
        return
    }

    entry := LogEntry{
        Timestamp: time.Now().UTC().Format(time.RFC3339),
        Level:     l.levelToString(level),
        Message:   message,
        Service:   l.service,
        Version:   l.version,
    }

    // Add fields
    if len(fields) > 0 {
        entry.Fields = make(map[string]interface{})
        for _, field := range fields {
            entry.Fields[field.Key] = field.Value
        }
    }

    // Output as JSON
    jsonData, _ := json.Marshal(entry)
    fmt.Fprintln(os.Stdout, string(jsonData))
}

func (l *JSONLogger) levelToString(level schemas.LogLevel) string {
    switch level {
    case schemas.LogLevelDebug:
        return "debug"
    case schemas.LogLevelInfo:
        return "info"
    case schemas.LogLevelWarn:
        return "warn"
    case schemas.LogLevelError:
        return "error"
    case schemas.LogLevelFatal:
        return "fatal"
    default:
        return "unknown"
    }
}

// Usage
logger := NewJSONLogger(schemas.LogLevelInfo, "my-app", "1.0.0")
client, initErr := bifrost.Init(schemas.BifrostConfig{
    Account: &MyAccount{},
    Logger:  logger,
})
```

### **Logrus Integration**

Integrate with the popular Logrus logging library:

```go
package main

import (
    "github.com/sirupsen/logrus"
    "github.com/maximhq/bifrost/core/schemas"
)

type LogrusAdapter struct {
    logger *logrus.Logger
    level  schemas.LogLevel
}

func NewLogrusAdapter(level schemas.LogLevel) *LogrusAdapter {
    logger := logrus.New()
    logger.SetFormatter(&logrus.JSONFormatter{})

    return &LogrusAdapter{
        logger: logger,
        level:  level,
    }
}

func (l *LogrusAdapter) Log(level schemas.LogLevel, message string, fields ...schemas.LogField) {
    if level < l.level {
        return
    }

    // Convert Bifrost log level to Logrus level
    logrusLevel := l.convertLevel(level)

    // Create entry with fields
    entry := l.logger.WithFields(l.convertFields(fields))

    // Log at appropriate level
    switch logrusLevel {
    case logrus.DebugLevel:
        entry.Debug(message)
    case logrus.InfoLevel:
        entry.Info(message)
    case logrus.WarnLevel:
        entry.Warn(message)
    case logrus.ErrorLevel:
        entry.Error(message)
    case logrus.FatalLevel:
        entry.Fatal(message)
    }
}

func (l *LogrusAdapter) convertLevel(level schemas.LogLevel) logrus.Level {
    switch level {
    case schemas.LogLevelDebug:
        return logrus.DebugLevel
    case schemas.LogLevelInfo:
        return logrus.InfoLevel
    case schemas.LogLevelWarn:
        return logrus.WarnLevel
    case schemas.LogLevelError:
        return logrus.ErrorLevel
    case schemas.LogLevelFatal:
        return logrus.FatalLevel
    default:
        return logrus.InfoLevel
    }
}

func (l *LogrusAdapter) convertFields(fields []schemas.LogField) logrus.Fields {
    logrusFields := make(logrus.Fields)
    for _, field := range fields {
        logrusFields[field.Key] = field.Value
    }
    return logrusFields
}
```

---

## 🔍 Request Tracing and Correlation

### **Request Correlation Logger**

Track requests with correlation IDs:

```go
package main

import (
    "context"
    "fmt"
    "log"
    "github.com/google/uuid"
    "github.com/maximhq/bifrost/core/schemas"
)

type CorrelationLogger struct {
    baseLogger schemas.Logger
}

func NewCorrelationLogger(baseLogger schemas.Logger) *CorrelationLogger {
    return &CorrelationLogger{
        baseLogger: baseLogger,
    }
}

func (l *CorrelationLogger) Log(level schemas.LogLevel, message string, fields ...schemas.LogField) {
    // Add correlation ID if available in context
    if correlationID := l.getCorrelationID(); correlationID != "" {
        fields = append(fields, schemas.LogField{
            Key:   "correlation_id",
            Value: correlationID,
        })
    }

    l.baseLogger.Log(level, message, fields...)
}

func (l *CorrelationLogger) getCorrelationID() string {
    // This would be set in your application context
    // Implementation depends on your context management
    return ""
}

// Plugin to add correlation IDs
type CorrelationPlugin struct {
    logger schemas.Logger
}

func (p *CorrelationPlugin) GetName() string {
    return "correlation"
}

func (p *CorrelationPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
    // Generate or extract correlation ID
    correlationID := uuid.New().String()
    *ctx = context.WithValue(*ctx, "correlation_id", correlationID)

    p.logger.Log(schemas.LogLevelInfo, "Request started",
        schemas.LogField{Key: "correlation_id", Value: correlationID},
        schemas.LogField{Key: "provider", Value: req.Provider},
        schemas.LogField{Key: "model", Value: req.Model},
    )

    return req, nil, nil
}

func (p *CorrelationPlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
    correlationID, _ := (*ctx).Value("correlation_id").(string)

    if err != nil {
        p.logger.Log(schemas.LogLevelError, "Request failed",
            schemas.LogField{Key: "correlation_id", Value: correlationID},
            schemas.LogField{Key: "error", Value: err.Error.Message},
        )
    } else {
        p.logger.Log(schemas.LogLevelInfo, "Request completed",
            schemas.LogField{Key: "correlation_id", Value: correlationID},
            schemas.LogField{Key: "provider_used", Value: result.ExtraFields.Provider},
        )
    }

    return result, err, nil
}

func (p *CorrelationPlugin) Cleanup() error {
    return nil
}
```

---

## 📊 Performance and Metrics Logging

### **Performance Monitoring Logger**

Log detailed performance metrics:

```go
package main

import (
    "time"
    "github.com/maximhq/bifrost/core/schemas"
)

type PerformanceLogger struct {
    baseLogger   schemas.Logger
    slowThreshold time.Duration
}

func NewPerformanceLogger(baseLogger schemas.Logger, slowThreshold time.Duration) *PerformanceLogger {
    return &PerformanceLogger{
        baseLogger:   baseLogger,
        slowThreshold: slowThreshold,
    }
}

func (l *PerformanceLogger) Log(level schemas.LogLevel, message string, fields ...schemas.LogField) {
    // Check for latency information
    var latency time.Duration
    for _, field := range fields {
        if field.Key == "latency" {
            if duration, ok := field.Value.(time.Duration); ok {
                latency = duration
                break
            }
        }
    }

    // Upgrade log level for slow requests
    if latency > l.slowThreshold && level < schemas.LogLevelWarn {
        level = schemas.LogLevelWarn
        message = fmt.Sprintf("[SLOW REQUEST] %s", message)
    }

    l.baseLogger.Log(level, message, fields...)
}

// Plugin for performance logging
type PerformancePlugin struct {
    logger schemas.Logger
}

func (p *PerformancePlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
    *ctx = context.WithValue(*ctx, "request_start_time", time.Now())
    return req, nil, nil
}

func (p *PerformancePlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
    startTime, _ := (*ctx).Value("request_start_time").(time.Time)
    latency := time.Since(startTime)

    fields := []schemas.LogField{
        {Key: "latency", Value: latency},
        {Key: "latency_ms", Value: latency.Milliseconds()},
    }

    if result != nil {
        fields = append(fields,
            schemas.LogField{Key: "tokens_used", Value: result.Usage.TotalTokens},
            schemas.LogField{Key: "provider_used", Value: result.ExtraFields.Provider},
        )
    }

    if err != nil {
        p.logger.Log(schemas.LogLevelError, "Request failed", fields...)
    } else {
        p.logger.Log(schemas.LogLevelInfo, "Request completed", fields...)
    }

    return result, err, nil
}
```

---

## 🔧 Environment-Specific Logging

### **Development vs Production Logging**

Configure different logging for different environments:

```go
package main

import (
    "os"
    "github.com/maximhq/bifrost/core/schemas"
)

func createLogger() schemas.Logger {
    env := os.Getenv("ENVIRONMENT")

    switch env {
    case "development":
        return NewDevelopmentLogger()
    case "staging":
        return NewStagingLogger()
    case "production":
        return NewProductionLogger()
    default:
        return NewDefaultLogger()
    }
}

func NewDevelopmentLogger() schemas.Logger {
    // Verbose logging for development
    return NewStandardLogger(schemas.LogLevelDebug)
}

func NewStagingLogger() schemas.Logger {
    // Structured logging for staging
    return NewJSONLogger(schemas.LogLevelInfo, "bifrost-staging", "1.0.0")
}

func NewProductionLogger() schemas.Logger {
    // Minimal logging for production
    logger := NewJSONLogger(schemas.LogLevelWarn, "bifrost-prod", "1.0.0")

    // Add performance monitoring
    return NewPerformanceLogger(logger, 5*time.Second)
}

func NewDefaultLogger() schemas.Logger {
    return NewStandardLogger(schemas.LogLevelInfo)
}

// Usage
client, initErr := bifrost.Init(schemas.BifrostConfig{
    Account: &MyAccount{},
    Logger:  createLogger(),
})
```

### **Multiple Output Destinations**

Log to multiple destinations simultaneously:

```go
package main

import (
    "io"
    "os"
    "github.com/maximhq/bifrost/core/schemas"
)

type MultiLogger struct {
    loggers []schemas.Logger
}

func NewMultiLogger(loggers ...schemas.Logger) *MultiLogger {
    return &MultiLogger{
        loggers: loggers,
    }
}

func (l *MultiLogger) Log(level schemas.LogLevel, message string, fields ...schemas.LogField) {
    for _, logger := range l.loggers {
        logger.Log(level, message, fields...)
    }
}

// Create multi-destination logger
func createMultiLogger() schemas.Logger {
    // Console logger for development
    consoleLogger := NewStandardLogger(schemas.LogLevelDebug)

    // File logger for persistence
    logFile, _ := os.OpenFile("bifrost.log", os.O_CREATE|os.O_WRITABLE|os.O_APPEND, 0666)
    fileLogger := NewFileLogger(logFile, schemas.LogLevelInfo)

    // Remote logger for monitoring (hypothetical)
    remoteLogger := NewRemoteLogger("https://logs.example.com", schemas.LogLevelError)

    return NewMultiLogger(consoleLogger, fileLogger, remoteLogger)
}
```

---

## 🛡️ Security and Sanitization

### **Secure Logger**

Sanitize sensitive information from logs:

```go
package main

import (
    "regexp"
    "strings"
    "github.com/maximhq/bifrost/core/schemas"
)

type SecureLogger struct {
    baseLogger      schemas.Logger
    sensitiveFields []string
    apiKeyPattern   *regexp.Regexp
}

func NewSecureLogger(baseLogger schemas.Logger) *SecureLogger {
    return &SecureLogger{
        baseLogger: baseLogger,
        sensitiveFields: []string{
            "api_key", "secret", "password", "token", "authorization",
        },
        apiKeyPattern: regexp.MustCompile(`(?i)(sk-[a-zA-Z0-9]{48}|xoxb-[a-zA-Z0-9-]+)`),
    }
}

func (l *SecureLogger) Log(level schemas.LogLevel, message string, fields ...schemas.LogField) {
    // Sanitize message
    sanitizedMessage := l.sanitizeString(message)

    // Sanitize fields
    sanitizedFields := make([]schemas.LogField, len(fields))
    for i, field := range fields {
        sanitizedFields[i] = schemas.LogField{
            Key:   field.Key,
            Value: l.sanitizeValue(field.Key, field.Value),
        }
    }

    l.baseLogger.Log(level, sanitizedMessage, sanitizedFields...)
}

func (l *SecureLogger) sanitizeString(s string) string {
    // Replace API keys with placeholder
    s = l.apiKeyPattern.ReplaceAllString(s, "[REDACTED_API_KEY]")

    // Add more sanitization patterns as needed
    return s
}

func (l *SecureLogger) sanitizeValue(key string, value interface{}) interface{} {
    // Check if field is sensitive
    keyLower := strings.ToLower(key)
    for _, sensitive := range l.sensitiveFields {
        if strings.Contains(keyLower, sensitive) {
            return "[REDACTED]"
        }
    }

    // Sanitize string values
    if strValue, ok := value.(string); ok {
        return l.sanitizeString(strValue)
    }

    return value
}
```

---

## 🧪 Testing Logging

### **Mock Logger for Testing**

Create a mock logger for unit tests:

```go
package main

import (
    "sync"
    "github.com/maximhq/bifrost/core/schemas"
)

type MockLogger struct {
    mu      sync.RWMutex
    entries []LogEntry
}

type LogEntry struct {
    Level   schemas.LogLevel
    Message string
    Fields  []schemas.LogField
}

func NewMockLogger() *MockLogger {
    return &MockLogger{
        entries: make([]LogEntry, 0),
    }
}

func (l *MockLogger) Log(level schemas.LogLevel, message string, fields ...schemas.LogField) {
    l.mu.Lock()
    defer l.mu.Unlock()

    l.entries = append(l.entries, LogEntry{
        Level:   level,
        Message: message,
        Fields:  fields,
    })
}

func (l *MockLogger) GetEntries() []LogEntry {
    l.mu.RLock()
    defer l.mu.RUnlock()

    entries := make([]LogEntry, len(l.entries))
    copy(entries, l.entries)
    return entries
}

func (l *MockLogger) GetEntriesByLevel(level schemas.LogLevel) []LogEntry {
    l.mu.RLock()
    defer l.mu.RUnlock()

    var filtered []LogEntry
    for _, entry := range l.entries {
        if entry.Level == level {
            filtered = append(filtered, entry)
        }
    }
    return filtered
}

func (l *MockLogger) Clear() {
    l.mu.Lock()
    defer l.mu.Unlock()

    l.entries = l.entries[:0]
}

// Usage in tests
func TestLogging(t *testing.T) {
    mockLogger := NewMockLogger()

    client, initErr := bifrost.Init(schemas.BifrostConfig{
        Account: &TestAccount{},
        Logger:  mockLogger,
    })
    require.Nil(t, initErr)
    defer client.Cleanup()

    // Make a request
    response, err := client.ChatCompletionRequest(context.Background(), request)

    // Check logs
    entries := mockLogger.GetEntries()
    assert.Greater(t, len(entries), 0)

    // Check for specific log messages
    errorEntries := mockLogger.GetEntriesByLevel(schemas.LogLevelError)
    assert.Equal(t, 0, len(errorEntries), "Should have no error logs")
}
```

---

## 📚 Related Documentation

- **[🤖 Bifrost Client](./bifrost-client.md)** - Client initialization with custom loggers
- **[🔌 Plugins](./plugins.md)** - Logging plugins and middleware
- **[📋 Schemas](./schemas.md)** - Logger interface and log level definitions
- **[🌐 HTTP Transport](../http-transport/)** - HTTP transport logging configuration

> **🏛️ Architecture:** For logging system design and best practices, see [Architecture Documentation](../../architecture/).
