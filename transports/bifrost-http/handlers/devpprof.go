package handlers

import (
	"bytes"
	"os"
	"runtime"
	"runtime/pprof"
	"sort"
	"sync"
	"time"

	"github.com/fasthttp/router"
	"github.com/google/pprof/profile"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

const (
	// Collection interval for metrics
	metricsCollectionInterval = 10 * time.Second
	// Number of data points to keep (5 minutes / 10 seconds = 30 points)
	historySize = 30
	// Top allocations to return
	topAllocationsCount = 5
)

// MemoryStats represents memory statistics at a point in time
type MemoryStats struct {
	Alloc       uint64 `json:"alloc"`
	TotalAlloc  uint64 `json:"total_alloc"`
	HeapInuse   uint64 `json:"heap_inuse"`
	HeapObjects uint64 `json:"heap_objects"`
	Sys         uint64 `json:"sys"`
}

// CPUStats represents CPU statistics
type CPUStats struct {
	UsagePercent float64 `json:"usage_percent"`
	UserTime     float64 `json:"user_time"`
	SystemTime   float64 `json:"system_time"`
}

// RuntimeStats represents runtime statistics
type RuntimeStats struct {
	NumGoroutine int    `json:"num_goroutine"`
	NumGC        uint32 `json:"num_gc"`
	GCPauseNs    uint64 `json:"gc_pause_ns"`
	NumCPU       int    `json:"num_cpu"`
	GOMAXPROCS   int    `json:"gomaxprocs"`
}

// AllocationInfo represents a single allocation site
type AllocationInfo struct {
	Function string `json:"function"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Bytes    int64  `json:"bytes"`
	Count    int64  `json:"count"`
}

// HistoryPoint represents a single point in the metrics history
type HistoryPoint struct {
	Timestamp  string  `json:"timestamp"`
	Alloc      uint64  `json:"alloc"`
	HeapInuse  uint64  `json:"heap_inuse"`
	Goroutines int     `json:"goroutines"`
	GCPauseNs  uint64  `json:"gc_pause_ns"`
	CPUPercent float64 `json:"cpu_percent"`
}

// PprofData represents the complete pprof response
type PprofData struct {
	Timestamp      string           `json:"timestamp"`
	Memory         MemoryStats      `json:"memory"`
	CPU            CPUStats         `json:"cpu"`
	Runtime        RuntimeStats     `json:"runtime"`
	TopAllocations []AllocationInfo `json:"top_allocations"`
	History        []HistoryPoint   `json:"history"`
}

// cpuSample holds a CPU time sample for calculating usage
type cpuSample struct {
	timestamp  time.Time
	userTime   time.Duration
	systemTime time.Duration
}

// MetricsCollector collects and stores runtime metrics
type MetricsCollector struct {
	mu            sync.RWMutex
	history       []HistoryPoint
	stopCh        chan struct{}
	started       bool
	lastCPUSample cpuSample
	currentCPU    CPUStats
}

// DevPprofHandler handles development profiling endpoints
type DevPprofHandler struct {
	collector *MetricsCollector
}

// Global collector instance
var globalCollector *MetricsCollector
var collectorOnce sync.Once

// IsDevMode checks if dev mode is enabled via environment variable
func IsDevMode() bool {
	return os.Getenv("BIFROST_UI_DEV") == "true"
}

// getOrCreateCollector returns the global metrics collector, creating it if needed
func getOrCreateCollector() *MetricsCollector {
	collectorOnce.Do(func() {
		globalCollector = &MetricsCollector{
			history: make([]HistoryPoint, 0, historySize),
			stopCh:  make(chan struct{}),
		}
	})
	return globalCollector
}

// NewDevPprofHandler creates a new dev pprof handler
func NewDevPprofHandler() *DevPprofHandler {
	return &DevPprofHandler{
		collector: getOrCreateCollector(),
	}
}

// Start begins the background metrics collection
func (c *MetricsCollector) Start() {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return
	}
	c.stopCh = make(chan struct{})
	c.started = true
	c.mu.Unlock()

	go c.collectLoop()
}

// Stop stops the background metrics collection
func (c *MetricsCollector) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return
	}
	close(c.stopCh)
	c.stopCh = nil
	c.started = false
}

func (c *MetricsCollector) collectLoop() {
	// Initialize CPU sample
	c.lastCPUSample = getCPUSample()

	// Wait a bit before first collection to get accurate CPU reading
	time.Sleep(100 * time.Millisecond)

	// Collect immediately on start
	c.collect()

	ticker := time.NewTicker(metricsCollectionInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.collect()
		case <-c.stopCh:
			return
		}
	}
}

// calculateCPUUsage calculates CPU usage percentage between two samples
func calculateCPUUsage(prev, curr cpuSample, numCPU int) CPUStats {
	elapsed := curr.timestamp.Sub(prev.timestamp)
	if elapsed <= 0 {
		return CPUStats{}
	}

	userDelta := curr.userTime - prev.userTime
	systemDelta := curr.systemTime - prev.systemTime
	totalCPUTime := userDelta + systemDelta

	// Calculate percentage: (CPU time used / wall time) * 100
	// Normalized by number of CPUs to get 0-100% range
	cpuPercent := (float64(totalCPUTime) / float64(elapsed)) * 100.0

	// Cap at 100% * numCPU (in case of measurement errors)
	maxPercent := float64(numCPU) * 100.0
	if cpuPercent > maxPercent {
		cpuPercent = maxPercent
	}

	return CPUStats{
		UsagePercent: cpuPercent,
		UserTime:     userDelta.Seconds(),
		SystemTime:   systemDelta.Seconds(),
	}
}

func (c *MetricsCollector) collect() {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	// Get current CPU sample and calculate usage
	currentSample := getCPUSample()
	cpuStats := calculateCPUUsage(c.lastCPUSample, currentSample, runtime.NumCPU())
	c.lastCPUSample = currentSample

	point := HistoryPoint{
		Timestamp:  time.Now().Format(time.RFC3339),
		Alloc:      memStats.Alloc,
		HeapInuse:  memStats.HeapInuse,
		Goroutines: runtime.NumGoroutine(),
		GCPauseNs:  memStats.PauseNs[(memStats.NumGC+255)%256],
		CPUPercent: cpuStats.UsagePercent,
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Store current CPU stats for API response
	c.currentCPU = cpuStats

	// Append to history, maintaining ring buffer behavior
	if len(c.history) >= historySize {
		// Shift left by one and append
		copy(c.history, c.history[1:])
		c.history[len(c.history)-1] = point
	} else {
		c.history = append(c.history, point)
	}
}

func (c *MetricsCollector) getHistory() []HistoryPoint {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Return a copy to avoid race conditions
	result := make([]HistoryPoint, len(c.history))
	copy(result, c.history)
	return result
}

func (c *MetricsCollector) getCPUStats() CPUStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.currentCPU
}

// getTopAllocations analyzes heap profile to find top allocation sites
func getTopAllocations() []AllocationInfo {
	// Write heap profile to buffer
	var buf bytes.Buffer
	if err := pprof.WriteHeapProfile(&buf); err != nil {
		return []AllocationInfo{}
	}

	// Parse the protobuf profile
	p, err := profile.Parse(&buf)
	if err != nil {
		return []AllocationInfo{}
	}

	// Find the indices for alloc_objects and alloc_space sample types
	var allocObjectsIdx, allocSpaceIdx int
	for i, st := range p.SampleType {
		switch st.Type {
		case "alloc_objects":
			allocObjectsIdx = i
		case "alloc_space":
			allocSpaceIdx = i
		}
	}

	// Aggregate allocations by function (top of stack = allocation site)
	allocMap := make(map[string]*AllocationInfo)

	for _, sample := range p.Sample {
		if len(sample.Location) == 0 {
			continue
		}
		loc := sample.Location[0] // Top of stack = allocation site
		if len(loc.Line) == 0 {
			continue
		}
		line := loc.Line[0]
		fn := line.Function
		if fn == nil {
			continue
		}

		key := fn.Name
		if existing, ok := allocMap[key]; ok {
			existing.Bytes += sample.Value[allocSpaceIdx]
			existing.Count += sample.Value[allocObjectsIdx]
		} else {
			allocMap[key] = &AllocationInfo{
				Function: fn.Name,
				File:     fn.Filename,
				Line:     int(line.Line),
				Bytes:    sample.Value[allocSpaceIdx],
				Count:    sample.Value[allocObjectsIdx],
			}
		}
	}

	// Convert map to slice
	allocations := make([]AllocationInfo, 0, len(allocMap))
	for _, alloc := range allocMap {
		allocations = append(allocations, *alloc)
	}

	// Sort by bytes descending
	sort.Slice(allocations, func(i, j int) bool {
		return allocations[i].Bytes > allocations[j].Bytes
	})

	// Return top N allocations
	if len(allocations) > topAllocationsCount {
		allocations = allocations[:topAllocationsCount]
	}

	return allocations
}

// RegisterRoutes registers the dev pprof routes
func (h *DevPprofHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// Start the collector when routes are registered
	h.collector.Start()

	r.GET("/api/dev/pprof", lib.ChainMiddlewares(h.getPprof, middlewares...))
}

// getPprof handles GET /api/dev/pprof
func (h *DevPprofHandler) getPprof(ctx *fasthttp.RequestCtx) {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	data := PprofData{
		Timestamp: time.Now().Format(time.RFC3339),
		Memory: MemoryStats{
			Alloc:       memStats.Alloc,
			TotalAlloc:  memStats.TotalAlloc,
			HeapInuse:   memStats.HeapInuse,
			HeapObjects: memStats.HeapObjects,
			Sys:         memStats.Sys,
		},
		CPU: h.collector.getCPUStats(),
		Runtime: RuntimeStats{
			NumGoroutine: runtime.NumGoroutine(),
			NumGC:        memStats.NumGC,
			GCPauseNs:    memStats.PauseNs[(memStats.NumGC+255)%256],
			NumCPU:       runtime.NumCPU(),
			GOMAXPROCS:   runtime.GOMAXPROCS(0),
		},
		TopAllocations: getTopAllocations(),
		History:        h.collector.getHistory(),
	}

	SendJSON(ctx, data)
}

// Cleanup stops the metrics collector
func (h *DevPprofHandler) Cleanup() {
	if h.collector != nil {
		h.collector.Stop()
	}
}
