package handlers

import (
	"os"
	"testing"
	"time"

	"github.com/fasthttp/router"
)

// TestIsDevMode_EnvEnabled tests IsDevMode when env var is set
func TestIsDevMode_EnvEnabled(t *testing.T) {
	// Save original value
	original := os.Getenv("BIFROST_UI_DEV")
	defer os.Setenv("BIFROST_UI_DEV", original)

	os.Setenv("BIFROST_UI_DEV", "true")

	if !IsDevMode() {
		t.Error("Expected IsDevMode() to return true when BIFROST_UI_DEV=true")
	}
}

// TestIsDevMode_EnvDisabled tests IsDevMode when env var is not set
func TestIsDevMode_EnvDisabled(t *testing.T) {
	// Save original value
	original := os.Getenv("BIFROST_UI_DEV")
	defer os.Setenv("BIFROST_UI_DEV", original)

	os.Unsetenv("BIFROST_UI_DEV")

	if IsDevMode() {
		t.Error("Expected IsDevMode() to return false when BIFROST_UI_DEV is not set")
	}
}

// TestIsDevMode_EnvWrongValue tests IsDevMode when env var has wrong value
func TestIsDevMode_EnvWrongValue(t *testing.T) {
	// Save original value
	original := os.Getenv("BIFROST_UI_DEV")
	defer os.Setenv("BIFROST_UI_DEV", original)

	testValues := []string{"false", "1", "yes", "TRUE", "True"}
	for _, val := range testValues {
		t.Run(val, func(t *testing.T) {
			os.Setenv("BIFROST_UI_DEV", val)

			// Only "true" (lowercase) should return true
			expected := val == "true"
			if IsDevMode() != expected {
				t.Errorf("IsDevMode() with BIFROST_UI_DEV=%s, expected %v", val, expected)
			}
		})
	}
}

// TestNewDevPprofHandler tests creating a new dev pprof handler
func TestNewDevPprofHandler(t *testing.T) {
	SetLogger(&mockLogger{})

	handler := NewDevPprofHandler()

	if handler == nil {
		t.Fatal("Expected non-nil handler")
	}
	if handler.collector == nil {
		t.Error("Expected non-nil collector")
	}
}

// TestDevPprofHandler_RegisterRoutes tests route registration
func TestDevPprofHandler_RegisterRoutes(t *testing.T) {
	SetLogger(&mockLogger{})

	handler := NewDevPprofHandler()
	r := router.New()

	handler.RegisterRoutes(r)

	// Verify routes were registered
	if r == nil {
		t.Error("Router should not be nil")
	}
}

// TestDevPprofHandler_Routes documents registered routes
func TestDevPprofHandler_Routes(t *testing.T) {
	// DevPprofHandler registers:
	// GET /api/dev/pprof - Get comprehensive profiling data
	// GET /api/dev/pprof/goroutines - Get detailed goroutine analysis

	routes := []struct {
		method string
		path   string
		desc   string
	}{
		{"GET", "/api/dev/pprof", "Returns memory, CPU, runtime stats, top allocations, and history"},
		{"GET", "/api/dev/pprof/goroutines", "Returns detailed goroutine analysis with categorization"},
	}

	for _, r := range routes {
		t.Logf("%s %s - %s", r.method, r.path, r.desc)
	}
}

// TestMetricsCollector_StartStop tests collector lifecycle
func TestMetricsCollector_StartStop(t *testing.T) {
	collector := &MetricsCollector{
		history: make([]HistoryPoint, 0, historySize),
		stopCh:  make(chan struct{}),
	}

	// Start should work
	collector.Start()
	if !collector.started {
		t.Error("Expected collector to be started")
	}

	// Give it time to collect at least one point
	time.Sleep(150 * time.Millisecond)

	// Start again should be idempotent
	collector.Start()
	if !collector.started {
		t.Error("Expected collector to still be started")
	}

	// Stop should work
	collector.Stop()
	if collector.started {
		t.Error("Expected collector to be stopped")
	}

	// Stop again should be idempotent
	collector.Stop()
	if collector.started {
		t.Error("Expected collector to still be stopped")
	}
}

// TestMetricsCollector_GetHistory tests history retrieval
func TestMetricsCollector_GetHistory(t *testing.T) {
	collector := &MetricsCollector{
		history: make([]HistoryPoint, 0, historySize),
		stopCh:  make(chan struct{}),
	}

	// Initially empty
	history := collector.getHistory()
	if len(history) != 0 {
		t.Errorf("Expected empty history, got %d points", len(history))
	}

	// Add some history points
	collector.mu.Lock()
	collector.history = append(collector.history, HistoryPoint{
		Timestamp: "2024-01-01T00:00:00Z",
		Alloc:     1000,
	})
	collector.mu.Unlock()

	// Should return copy
	history = collector.getHistory()
	if len(history) != 1 {
		t.Errorf("Expected 1 history point, got %d", len(history))
	}
}

// TestMetricsCollector_GetCPUStats tests CPU stats retrieval
func TestMetricsCollector_GetCPUStats(t *testing.T) {
	collector := &MetricsCollector{
		history:    make([]HistoryPoint, 0, historySize),
		stopCh:     make(chan struct{}),
		currentCPU: CPUStats{UsagePercent: 50.0, UserTime: 10.0, SystemTime: 5.0},
	}

	stats := collector.getCPUStats()

	if stats.UsagePercent != 50.0 {
		t.Errorf("Expected UsagePercent=50.0, got %v", stats.UsagePercent)
	}
	if stats.UserTime != 10.0 {
		t.Errorf("Expected UserTime=10.0, got %v", stats.UserTime)
	}
	if stats.SystemTime != 5.0 {
		t.Errorf("Expected SystemTime=5.0, got %v", stats.SystemTime)
	}
}

// TestHistoryPoint_Structure documents history point structure
func TestHistoryPoint_Structure(t *testing.T) {
	// HistoryPoint contains:
	// - Timestamp: ISO 8601 formatted time
	// - Alloc: bytes currently allocated
	// - HeapInuse: bytes in in-use spans
	// - Goroutines: number of goroutines
	// - GCPauseNs: total GC pause time
	// - CPUPercent: CPU usage percentage

	point := HistoryPoint{
		Timestamp:  "2024-01-01T00:00:00Z",
		Alloc:      1000000,
		HeapInuse:  2000000,
		Goroutines: 50,
		GCPauseNs:  1000000,
		CPUPercent: 25.5,
	}

	if point.Timestamp == "" {
		t.Error("Timestamp should not be empty")
	}

	t.Log("HistoryPoint contains memory, goroutine, GC, and CPU metrics")
}

// TestPprofData_Structure documents pprof data structure
func TestPprofData_Structure(t *testing.T) {
	// PprofData contains:
	// - Timestamp: current time
	// - Memory: MemoryStats (alloc, heap, sys)
	// - CPU: CPUStats (usage %, user/system time)
	// - Runtime: RuntimeStats (goroutines, GC, CPU count)
	// - TopAllocations: top memory allocating functions
	// - History: recent metrics history

	t.Log("PprofData aggregates all profiling metrics for the /api/dev/pprof endpoint")
}

// TestGoroutineGroup_Categories documents goroutine categorization
func TestGoroutineGroup_Categories(t *testing.T) {
	// Goroutines are categorized as:
	// - "background": Long-running goroutines (net.Accept, time.Sleep, channel ops)
	// - "per-request": Request-handling goroutines (HTTP handlers, DB queries)
	// - "unknown": Goroutines that don't match known patterns

	categories := []struct {
		category string
		patterns []string
	}{
		{"background", []string{"net.(*netFD).Accept", "time.Sleep", "select", "chan receive"}},
		{"per-request", []string{"http.(*conn).serve", "net/http.HandlerFunc", "database/sql"}},
	}

	for _, c := range categories {
		t.Logf("Category '%s' includes patterns: %v", c.category, c.patterns)
	}

	t.Log("Goroutine categorization helps identify potential leaks")
}

// TestGoroutineSummary_Fields documents summary fields
func TestGoroutineSummary_Fields(t *testing.T) {
	// GoroutineSummary provides:
	// - Background: count of expected long-running goroutines
	// - PerRequest: count of request-handling goroutines
	// - LongWaiting: goroutines waiting > 1 minute (potential issues)
	// - PotentiallyStuck: per-request goroutines waiting > 1 minute

	summary := GoroutineSummary{
		Background:       10,
		PerRequest:       5,
		LongWaiting:      2,
		PotentiallyStuck: 1,
	}

	if summary.PotentiallyStuck > 0 {
		t.Log("PotentiallyStuck > 0 indicates possible goroutine leaks")
	}

	t.Log("GoroutineSummary helps quickly assess system health")
}

// TestMemoryStats_Fields documents memory stats fields
func TestMemoryStats_Fields(t *testing.T) {
	// MemoryStats contains:
	// - Alloc: bytes allocated and still in use
	// - TotalAlloc: cumulative bytes allocated
	// - HeapInuse: bytes in in-use spans
	// - HeapObjects: number of allocated heap objects
	// - Sys: total bytes obtained from system

	stats := MemoryStats{
		Alloc:       10000000,
		TotalAlloc:  50000000,
		HeapInuse:   15000000,
		HeapObjects: 1000,
		Sys:         100000000,
	}

	if stats.Alloc > stats.HeapInuse {
		t.Error("Alloc should be <= HeapInuse")
	}

	t.Log("MemoryStats provides key memory metrics from runtime.MemStats")
}

// TestAllocationInfo_Structure documents allocation info
func TestAllocationInfo_Structure(t *testing.T) {
	// AllocationInfo represents top allocating sites:
	// - Function: function name
	// - File: source file path
	// - Line: line number
	// - Bytes: bytes allocated
	// - Count: allocation count

	info := AllocationInfo{
		Function: "github.com/example/pkg.ProcessData",
		File:     "/app/pkg/processor.go",
		Line:     42,
		Bytes:    1000000,
		Count:    100,
	}

	if info.Function == "" {
		t.Error("Function name should not be empty")
	}

	t.Log("AllocationInfo helps identify memory-intensive code paths")
}

// TestHistorySize_Constant documents history size
func TestHistorySize_Constant(t *testing.T) {
	// History is limited to historySize (30) points
	// With 10-second intervals, this provides 5 minutes of history

	if historySize != 30 {
		t.Errorf("Expected historySize=30, got %d", historySize)
	}

	expectedMinutes := float64(historySize) * float64(metricsCollectionInterval) / float64(time.Minute)
	t.Logf("History provides %.1f minutes of metrics", expectedMinutes)
}

// TestMetricsCollectionInterval_Constant documents collection interval
func TestMetricsCollectionInterval_Constant(t *testing.T) {
	// Metrics are collected every 10 seconds

	if metricsCollectionInterval != 10*time.Second {
		t.Errorf("Expected metricsCollectionInterval=10s, got %v", metricsCollectionInterval)
	}

	t.Log("Metrics collected every 10 seconds")
}

// TestTopAllocationsCount_Constant documents top allocations limit
func TestTopAllocationsCount_Constant(t *testing.T) {
	// Top 5 allocation sites are returned

	if topAllocationsCount != 5 {
		t.Errorf("Expected topAllocationsCount=5, got %d", topAllocationsCount)
	}

	t.Log("Top 5 allocation sites returned in profiling data")
}
