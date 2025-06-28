# Observability & Monitoring

Monitor, debug, and optimize Bifrost's performance with comprehensive observability features. Track **request latency**, **success rates**, **provider health**, and **plugin performance** in real-time.

## 🎯 Key Metrics

| Metric Category | Metrics | Purpose |
|-----------------|---------|---------|
| **📊 Request Metrics** | Latency, throughput, success rate | Monitor overall system performance |
| **🏭 Provider Health** | Per-provider success rates, queue depth | Identify provider issues |
| **🔑 Key Distribution** | API key usage, rate limits | Balance load across keys |
| **🔌 Plugin Performance** | Plugin execution time, success rate | Optimize plugin efficiency |
| **💾 Memory Usage** | Pool utilization, GC frequency | Memory optimization |

---

## ⚡ Quick Setup

<details open>
<summary><strong>🔧 Go Package Monitoring</strong></summary>

```go
package main

import (
    "context"
    "fmt"
    "time"
    "github.com/maximhq/bifrost/core"
    "github.com/maximhq/bifrost/core/schemas"
)

// Monitoring plugin to track request metrics
type MonitoringPlugin struct {
    name    string
    metrics map[string]*RequestMetrics
    mutex   sync.RWMutex
}

type RequestMetrics struct {
    TotalRequests   int64
    SuccessRequests int64
    FailedRequests  int64
    TotalLatency    time.Duration
    AvgLatency      time.Duration
}

func (p *MonitoringPlugin) GetName() string {
    return p.name
}

func (p *MonitoringPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
    // Record request start time
    startTime := time.Now()
    newCtx := context.WithValue(*ctx, "monitoring_start", startTime)
    newCtx = context.WithValue(newCtx, "monitoring_provider", req.Provider)
    *ctx = newCtx
    
    // Track total requests
    p.mutex.Lock()
    providerKey := string(req.Provider)
    if p.metrics[providerKey] == nil {
        p.metrics[providerKey] = &RequestMetrics{}
    }
    p.metrics[providerKey].TotalRequests++
    p.mutex.Unlock()
    
    return req, nil, nil
}

func (p *MonitoringPlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
    // Calculate request latency
    startTime, ok := (*ctx).Value("monitoring_start").(time.Time)
    if !ok {
        return result, err, nil
    }
    
    provider, ok := (*ctx).Value("monitoring_provider").(schemas.ModelProvider)
    if !ok {
        return result, err, nil
    }
    
    latency := time.Since(startTime)
    providerKey := string(provider)
    
    p.mutex.Lock()
    metrics := p.metrics[providerKey]
    if err != nil {
        metrics.FailedRequests++
    } else {
        metrics.SuccessRequests++
    }
    
    metrics.TotalLatency += latency
    totalRequests := metrics.SuccessRequests + metrics.FailedRequests
    if totalRequests > 0 {
        metrics.AvgLatency = metrics.TotalLatency / time.Duration(totalRequests)
    }
    p.mutex.Unlock()
    
    // Log high-latency requests
    if latency > 5*time.Second {
        fmt.Printf("HIGH LATENCY WARNING: %s request took %v\n", providerKey, latency)
    }
    
    return result, err, nil
}

func (p *MonitoringPlugin) Cleanup() error {
    return nil
}

func (p *MonitoringPlugin) GetMetrics() map[string]*RequestMetrics {
    p.mutex.RLock()
    defer p.mutex.RUnlock()
    
    // Return copy to avoid race conditions
    result := make(map[string]*RequestMetrics)
    for k, v := range p.metrics {
        result[k] = &RequestMetrics{
            TotalRequests:   v.TotalRequests,
            SuccessRequests: v.SuccessRequests,
            FailedRequests:  v.FailedRequests,
            TotalLatency:    v.TotalLatency,
            AvgLatency:      v.AvgLatency,
        }
    }
    return result
}

func main() {
    // Create monitoring plugin
    monitor := &MonitoringPlugin{
        name:    "request-monitor",
        metrics: make(map[string]*RequestMetrics),
    }

    // Initialize Bifrost with monitoring
    bf, err := bifrost.Init(schemas.BifrostConfig{
        Account: &MyAccount{},
        Plugins: []schemas.Plugin{monitor},
        Logger:  bifrost.NewDefaultLogger(schemas.LogLevelInfo),
    })
    if err != nil {
        panic(err)
    }
    defer bf.Cleanup()

    // Start metrics reporting goroutine
    go func() {
        ticker := time.NewTicker(30 * time.Second)
        defer ticker.Stop()
        
        for range ticker.C {
            metrics := monitor.GetMetrics()
            for provider, stats := range metrics {
                successRate := float64(stats.SuccessRequests) / float64(stats.TotalRequests) * 100
                fmt.Printf("METRICS [%s]: Total=%d, Success=%.1f%%, AvgLatency=%v\n",
                    provider, stats.TotalRequests, successRate, stats.AvgLatency)
            }
        }
    }()

    // Use Bifrost normally - metrics are automatically collected
    handleRequests(bf)
}
```

**📖 [Complete Go Package Setup Guide →](../quick-start/go-package.md)**

</details>

<details>
<summary><strong>🌐 HTTP Transport Monitoring</strong></summary>

**1. Enable detailed logging in configuration:**

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
      "network_config": {
        "extra_headers": {
          "X-Request-ID": "{{.RequestID}}",
          "X-User-Agent": "MyApp/1.0 Bifrost/1.0"
        }
      }
    }
  },
  "logging": {
    "level": "info",
    "format": "json",
    "include_request_details": true
  }
}
```

**2. Start server with monitoring endpoints:**

```bash
docker run -p 8080:8080 -p 9090:9090 \
  -v $(pwd)/config.json:/app/config/config.json \
  -e OPENAI_API_KEY \
  -e BIFROST_METRICS_ENABLED=true \
  -e BIFROST_METRICS_PORT=9090 \
  maximhq/bifrost
```

**3. Query metrics endpoints:**

```bash
# Health check
curl http://localhost:9090/health

# Request metrics
curl http://localhost:9090/metrics

# Provider-specific metrics
curl http://localhost:9090/metrics/providers

# Real-time statistics
curl http://localhost:9090/stats
```

**Example metrics response:**

```json
{
  "timestamp": "2024-01-15T10:30:00Z",
  "uptime_seconds": 3600,
  "total_requests": 15000,
  "requests_per_second": 4.17,
  "providers": {
    "openai": {
      "total_requests": 10000,
      "success_requests": 9950,
      "failed_requests": 50,
      "success_rate": 99.5,
      "avg_latency_ms": 1250,
      "current_queue_depth": 5,
      "active_workers": 10
    },
    "anthropic": {
      "total_requests": 5000,
      "success_requests": 4980,
      "failed_requests": 20,
      "success_rate": 99.6,
      "avg_latency_ms": 1100,
      "current_queue_depth": 2,
      "active_workers": 10
    }
  },
  "memory": {
    "pool_utilization": 85.5,
    "active_channels": 42,
    "gc_cycles": 150
  }
}
```

**📖 [Complete HTTP Transport Setup Guide →](../quick-start/http-transport.md)**

</details>

---

## 📊 Monitoring Dashboards

### Metrics Collection Setup

<details>
<summary><strong>📈 Prometheus Integration</strong></summary>

**Prometheus configuration (`prometheus.yml`):**

```yaml
global:
  scrape_interval: 15s

scrape_configs:
  - job_name: 'bifrost'
    static_configs:
      - targets: ['localhost:9090']
    metrics_path: /metrics
    scrape_interval: 10s
    
  - job_name: 'bifrost-detailed'
    static_configs:
      - targets: ['localhost:9090']
    metrics_path: /metrics/detailed
    scrape_interval: 30s
```

**Grafana Dashboard Configuration:**

```json
{
  "dashboard": {
    "title": "Bifrost AI Gateway",
    "panels": [
      {
        "title": "Request Rate",
        "type": "stat",
        "targets": [
          {
            "expr": "rate(bifrost_requests_total[5m])",
            "legendFormat": "{{provider}}"
          }
        ]
      },
      {
        "title": "Success Rate",
        "type": "stat", 
        "targets": [
          {
            "expr": "rate(bifrost_requests_success[5m]) / rate(bifrost_requests_total[5m]) * 100",
            "legendFormat": "{{provider}}"
          }
        ]
      },
      {
        "title": "Latency Distribution",
        "type": "histogram",
        "targets": [
          {
            "expr": "histogram_quantile(0.95, rate(bifrost_request_duration_seconds_bucket[5m]))",
            "legendFormat": "95th percentile"
          },
          {
            "expr": "histogram_quantile(0.50, rate(bifrost_request_duration_seconds_bucket[5m]))",
            "legendFormat": "50th percentile"
          }
        ]
      }
    ]
  }
}
```

**Key Prometheus Metrics:**
- `bifrost_requests_total{provider, model, status}` - Total request count
- `bifrost_request_duration_seconds` - Request latency histogram
- `bifrost_provider_queue_depth{provider}` - Current queue depth
- `bifrost_memory_pool_utilization` - Memory pool usage
- `bifrost_api_key_usage{provider, key_id}` - API key distribution

</details>

### Real-Time Monitoring

<details>
<summary><strong>📺 Live Dashboard Example</strong></summary>

```go
// Real-time monitoring web dashboard
package main

import (
    "encoding/json"
    "net/http"
    "time"
)

type DashboardServer struct {
    monitor *MonitoringPlugin
}

func (d *DashboardServer) metricsHandler(w http.ResponseWriter, r *http.Request) {
    metrics := d.monitor.GetMetrics()
    
    response := map[string]interface{}{
        "timestamp": time.Now().Unix(),
        "providers": make(map[string]interface{}),
    }
    
    var totalRequests int64
    var totalSuccessful int64
    
    for provider, stats := range metrics {
        successRate := float64(stats.SuccessRequests) / float64(stats.TotalRequests) * 100
        
        response["providers"].(map[string]interface{})[provider] = map[string]interface{}{
            "total_requests":   stats.TotalRequests,
            "success_requests": stats.SuccessRequests,
            "failed_requests":  stats.FailedRequests,
            "success_rate":     successRate,
            "avg_latency_ms":   stats.AvgLatency.Milliseconds(),
        }
        
        totalRequests += stats.TotalRequests
        totalSuccessful += stats.SuccessRequests
    }
    
    response["summary"] = map[string]interface{}{
        "total_requests": totalRequests,
        "overall_success_rate": float64(totalSuccessful) / float64(totalRequests) * 100,
    }
    
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(response)
}

func (d *DashboardServer) healthHandler(w http.ResponseWriter, r *http.Request) {
    metrics := d.monitor.GetMetrics()
    
    healthy := true
    issues := []string{}
    
    for provider, stats := range metrics {
        successRate := float64(stats.SuccessRequests) / float64(stats.TotalRequests) * 100
        
        if successRate < 95.0 {
            healthy = false
            issues = append(issues, fmt.Sprintf("%s success rate: %.1f%%", provider, successRate))
        }
        
        if stats.AvgLatency > 10*time.Second {
            healthy = false  
            issues = append(issues, fmt.Sprintf("%s high latency: %v", provider, stats.AvgLatency))
        }
    }
    
    response := map[string]interface{}{
        "healthy": healthy,
        "issues":  issues,
        "timestamp": time.Now().Unix(),
    }
    
    status := http.StatusOK
    if !healthy {
        status = http.StatusServiceUnavailable
    }
    
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    json.NewEncoder(w).Encode(response)
}

func startDashboard(monitor *MonitoringPlugin) {
    dashboard := &DashboardServer{monitor: monitor}
    
    http.HandleFunc("/metrics", dashboard.metricsHandler)
    http.HandleFunc("/health", dashboard.healthHandler)
    
    // Serve static dashboard files
    http.Handle("/", http.FileServer(http.Dir("./dashboard/")))
    
    fmt.Println("Dashboard server starting on :9090")
    http.ListenAndServe(":9090", nil)
}
```

**Dashboard Features:**
- 📊 Real-time request rate and success rate graphs
- 🎯 Provider-specific performance metrics
- ⚠️ Automated alerts for degraded performance
- 📈 Historical trend analysis
- 🔍 Request tracing and debugging

</details>

---

## 🚨 Alerting & Health Checks

### Automated Health Monitoring

<details>
<summary><strong>🔔 Alert Configuration</strong></summary>

```go
// Health check and alerting system
type HealthChecker struct {
    monitor    *MonitoringPlugin
    thresholds HealthThresholds
    alerts     chan Alert
}

type HealthThresholds struct {
    MinSuccessRate      float64       // e.g., 95.0%
    MaxLatency          time.Duration // e.g., 10 seconds
    MaxQueueDepth       int           // e.g., 100 requests
    MaxMemoryUsage      float64       // e.g., 80.0%
}

type Alert struct {
    Level     string    `json:"level"`     // "warning", "critical"
    Provider  string    `json:"provider"`
    Message   string    `json:"message"`
    Timestamp time.Time `json:"timestamp"`
    Value     float64   `json:"value"`
}

func (h *HealthChecker) checkHealth() {
    ticker := time.NewTicker(1 * time.Minute)
    defer ticker.Stop()
    
    for range ticker.C {
        metrics := h.monitor.GetMetrics()
        
        for provider, stats := range metrics {
            // Check success rate
            successRate := float64(stats.SuccessRequests) / float64(stats.TotalRequests) * 100
            if successRate < h.thresholds.MinSuccessRate {
                h.alerts <- Alert{
                    Level:     "critical",
                    Provider:  provider,
                    Message:   fmt.Sprintf("Success rate below threshold: %.1f%%", successRate),
                    Timestamp: time.Now(),
                    Value:     successRate,
                }
            }
            
            // Check latency
            if stats.AvgLatency > h.thresholds.MaxLatency {
                h.alerts <- Alert{
                    Level:     "warning",
                    Provider:  provider,
                    Message:   fmt.Sprintf("High latency detected: %v", stats.AvgLatency),
                    Timestamp: time.Now(),
                    Value:     float64(stats.AvgLatency.Milliseconds()),
                }
            }
        }
    }
}

func (h *HealthChecker) handleAlerts() {
    for alert := range h.alerts {
        // Send to monitoring system (Slack, PagerDuty, email, etc.)
        h.sendAlert(alert)
        
        // Log alert
        fmt.Printf("ALERT [%s] %s: %s (value: %.2f)\n",
            strings.ToUpper(alert.Level), alert.Provider, alert.Message, alert.Value)
    }
}

func (h *HealthChecker) sendAlert(alert Alert) {
    // Example: Send to Slack webhook
    payload := map[string]interface{}{
        "text": fmt.Sprintf("🚨 Bifrost Alert: %s", alert.Message),
        "attachments": []map[string]interface{}{
            {
                "color": map[string]string{
                    "warning":  "warning",
                    "critical": "danger",
                }[alert.Level],
                "fields": []map[string]interface{}{
                    {"title": "Provider", "value": alert.Provider, "short": true},
                    {"title": "Level", "value": alert.Level, "short": true},
                    {"title": "Value", "value": fmt.Sprintf("%.2f", alert.Value), "short": true},
                    {"title": "Time", "value": alert.Timestamp.Format(time.RFC3339), "short": true},
                },
            },
        },
    }
    
    // Send HTTP POST to Slack webhook
    // Implementation depends on your alerting system
}
```

**Health Check Endpoints:**

```bash
# Basic health check
curl http://localhost:9090/health
# Returns: {"healthy": true, "issues": [], "timestamp": 1642234567}

# Detailed health check
curl http://localhost:9090/health/detailed
# Returns comprehensive health information

# Provider-specific health
curl http://localhost:9090/health/providers/openai
# Returns health status for specific provider
```

</details>

### Performance Thresholds

<details>
<summary><strong>📏 Performance SLA Monitoring</strong></summary>

```go
// SLA monitoring configuration
type SLAMonitor struct {
    targets map[string]SLATarget
    metrics *MonitoringPlugin
}

type SLATarget struct {
    Provider        string        `json:"provider"`
    SuccessRate     float64       `json:"success_rate"`     // e.g., 99.9%
    P95Latency      time.Duration `json:"p95_latency"`      // e.g., 2 seconds
    P99Latency      time.Duration `json:"p99_latency"`      // e.g., 5 seconds
    Availability    float64       `json:"availability"`     // e.g., 99.5%
}

func (s *SLAMonitor) generateSLAReport() SLAReport {
    metrics := s.metrics.GetMetrics()
    report := SLAReport{
        Period:    "24h",
        Timestamp: time.Now(),
        Providers: make(map[string]SLAStatus),
    }
    
    for provider, target := range s.targets {
        stats := metrics[provider]
        if stats == nil {
            continue
        }
        
        successRate := float64(stats.SuccessRequests) / float64(stats.TotalRequests) * 100
        
        status := SLAStatus{
            Provider:         provider,
            TargetSuccess:    target.SuccessRate,
            ActualSuccess:    successRate,
            TargetP95:        target.P95Latency,
            ActualP95:        s.calculateP95Latency(provider),
            SLAMet:          successRate >= target.SuccessRate,
            AvailabilityMet: s.checkAvailability(provider, target.Availability),
        }
        
        report.Providers[provider] = status
    }
    
    return report
}

// Example SLA targets
func createSLATargets() map[string]SLATarget {
    return map[string]SLATarget{
        "openai": {
            Provider:     "openai",
            SuccessRate:  99.5,
            P95Latency:   2 * time.Second,
            P99Latency:   5 * time.Second,
            Availability: 99.9,
        },
        "anthropic": {
            Provider:     "anthropic",
            SuccessRate:  99.0,
            P95Latency:   3 * time.Second,
            P99Latency:   7 * time.Second,
            Availability: 99.5,
        },
    }
}
```

**Performance Indicators:**
- ✅ **Success Rate**: > 99.5% for production workloads
- ⚡ **P95 Latency**: < 2 seconds for interactive applications
- 🎯 **P99 Latency**: < 5 seconds for batch processing
- 🔄 **Availability**: > 99.9% uptime
- 📊 **Throughput**: Sustained RPS capacity

</details>

---

## 🔍 Debugging & Troubleshooting

### Request Tracing

<details>
<summary><strong>🕵️ Request Flow Debugging</strong></summary>

```go
// Debug tracing plugin for request flow analysis
type TracingPlugin struct {
    name      string
    traceLog  map[string]*TraceData
    mutex     sync.RWMutex
}

type TraceData struct {
    RequestID    string                 `json:"request_id"`
    Provider     string                 `json:"provider"`
    Model        string                 `json:"model"`
    StartTime    time.Time             `json:"start_time"`
    Stages       []TraceStage          `json:"stages"`
    FinalResult  string                `json:"final_result"`
    TotalTime    time.Duration         `json:"total_time"`
}

type TraceStage struct {
    Stage     string        `json:"stage"`
    Timestamp time.Time     `json:"timestamp"`
    Duration  time.Duration `json:"duration"`
    Details   string        `json:"details"`
}

func (p *TracingPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
    requestID := generateRequestID()
    startTime := time.Now()
    
    trace := &TraceData{
        RequestID: requestID,
        Provider:  string(req.Provider),
        Model:     req.Model,
        StartTime: startTime,
        Stages:    []TraceStage{},
    }
    
    trace.Stages = append(trace.Stages, TraceStage{
        Stage:     "prehook_start",
        Timestamp: startTime,
        Duration:  0,
        Details:   fmt.Sprintf("Provider: %s, Model: %s", req.Provider, req.Model),
    })
    
    p.mutex.Lock()
    p.traceLog[requestID] = trace
    p.mutex.Unlock()
    
    // Add request ID to context for other plugins
    newCtx := context.WithValue(*ctx, "trace_id", requestID)
    newCtx = context.WithValue(newCtx, "trace_start", startTime)
    *ctx = newCtx
    
    return req, nil, nil
}

func (p *TracingPlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
    requestID, ok := (*ctx).Value("trace_id").(string)
    if !ok {
        return result, err, nil
    }
    
    startTime, ok := (*ctx).Value("trace_start").(time.Time)
    if !ok {
        return result, err, nil
    }
    
    endTime := time.Now()
    totalDuration := endTime.Sub(startTime)
    
    p.mutex.Lock()
    trace := p.traceLog[requestID]
    if trace != nil {
        resultStatus := "success"
        details := "Request completed successfully"
        
        if err != nil {
            resultStatus = "error"
            details = fmt.Sprintf("Error: %s", err.Error.Message)
        }
        
        trace.Stages = append(trace.Stages, TraceStage{
            Stage:     "posthook_complete",
            Timestamp: endTime,
            Duration:  totalDuration,
            Details:   details,
        })
        
        trace.FinalResult = resultStatus
        trace.TotalTime = totalDuration
        
        // Log slow requests for analysis
        if totalDuration > 5*time.Second {
            p.logSlowRequest(trace)
        }
        
        // Clean up old traces (keep only last 1000)
        if len(p.traceLog) > 1000 {
            p.cleanupOldTraces()
        }
    }
    p.mutex.Unlock()
    
    return result, err, nil
}

func (p *TracingPlugin) GetTrace(requestID string) *TraceData {
    p.mutex.RLock()
    defer p.mutex.RUnlock()
    
    if trace, exists := p.traceLog[requestID]; exists {
        // Return copy to avoid race conditions
        traceCopy := *trace
        traceCopy.Stages = make([]TraceStage, len(trace.Stages))
        copy(traceCopy.Stages, trace.Stages)
        return &traceCopy
    }
    
    return nil
}

func (p *TracingPlugin) logSlowRequest(trace *TraceData) {
    fmt.Printf("SLOW REQUEST DETECTED:\n")
    fmt.Printf("  Request ID: %s\n", trace.RequestID)
    fmt.Printf("  Provider: %s, Model: %s\n", trace.Provider, trace.Model)
    fmt.Printf("  Total Time: %v\n", trace.TotalTime)
    fmt.Printf("  Stages:\n")
    
    for _, stage := range trace.Stages {
        fmt.Printf("    - %s: %v (%s)\n", stage.Stage, stage.Duration, stage.Details)
    }
    fmt.Printf("\n")
}
```

**Trace Analysis:**
- 🔍 **Request Flow**: Track request through entire pipeline
- ⏱️ **Stage Timing**: Identify bottlenecks in processing
- 🐛 **Error Analysis**: Detailed error context and timing
- 📊 **Performance Patterns**: Identify slow request patterns

</details>

### Performance Profiling

<details>
<summary><strong>📈 Go Runtime Profiling</strong></summary>

```go
// Performance profiling setup
import (
    _ "net/http/pprof"
    "net/http"
    "runtime"
)

func enableProfiling() {
    // Start pprof server for Go runtime profiling
    go func() {
        fmt.Println("Profiling server starting on :6060")
        http.ListenAndServe(":6060", nil)
    }()
}

func enableMemoryProfiling() {
    // Configure runtime for better profiling
    runtime.SetBlockProfileRate(1)
    runtime.SetMutexProfileFraction(1)
    
    // Periodic memory stats logging
    go func() {
        ticker := time.NewTicker(1 * time.Minute)
        defer ticker.Stop()
        
        for range ticker.C {
            var m runtime.MemStats
            runtime.ReadMemStats(&m)
            
            fmt.Printf("MEMORY STATS:\n")
            fmt.Printf("  Alloc: %d KB", bToKb(m.Alloc))
            fmt.Printf("  TotalAlloc: %d KB", bToKb(m.TotalAlloc))
            fmt.Printf("  Sys: %d KB", bToKb(m.Sys))
            fmt.Printf("  NumGC: %d\n", m.NumGC)
        }
    }()
}

func bToKb(b uint64) uint64 {
    return b / 1024
}
```

**Profiling Commands:**

```bash
# CPU profiling
go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30

# Memory profiling  
go tool pprof http://localhost:6060/debug/pprof/heap

# Goroutine profiling
go tool pprof http://localhost:6060/debug/pprof/goroutine

# Mutex contention profiling
go tool pprof http://localhost:6060/debug/pprof/mutex

# Block profiling
go tool pprof http://localhost:6060/debug/pprof/block
```

</details>

---

## 🎯 Next Steps

Ready to monitor Bifrost in production? Choose your monitoring strategy:

| **Monitoring Type** | **Documentation** | **Time Investment** |
|---------------------|-------------------|-------------------|
| **📊 Basic Metrics** | [Plugin Development](plugins.md) | 15 minutes |
| **📈 Prometheus/Grafana** | [External Monitoring Setup](#) | 30 minutes |
| **🚨 Alerting System** | [Alert Configuration](#) | 20 minutes |
| **🔍 Request Tracing** | [Debug Tracing Setup](#) | 25 minutes |
| **⚡ Performance Tuning** | [Optimization Guide](../benchmarks.md) | 45 minutes |

### 📖 Complete Reference

- **[🔌 Plugin System](plugins.md)** - Custom monitoring plugins
- **[🏗️ System Architecture](../architecture/system-overview.md)** - Understanding performance
- **[⚙️ Configuration Guide](../configuration/)** - Production monitoring setup
- **[❓ Troubleshooting](../guides/troubleshooting.md)** - Common monitoring issues

---

**🚀 Ready to deploy?** Start with our [📖 Quick Start Guides](../quick-start/) to get monitoring running in under 2 minutes.
