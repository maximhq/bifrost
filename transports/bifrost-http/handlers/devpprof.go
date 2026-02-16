package handlers

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fasthttp/router"
	"github.com/google/pprof/profile"
	"github.com/maximhq/bifrost/core/pool"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

const (
	// Collection interval for metrics
	metricsCollectionInterval = 10 * time.Second
	// Number of data points to keep (5 minutes / 10 seconds = 30 points)
	historySize = 30
	// Top allocations to return (increased for better coverage with lower MemProfileRate)
	topAllocationsCount = 20
)

// MemoryStats represents memory statistics at a point in time
type MemoryStats struct {
	Alloc       uint64 `json:"alloc"`
	TotalAlloc  uint64 `json:"total_alloc"`
	HeapInuse   uint64 `json:"heap_inuse"`
	HeapObjects uint64 `json:"heap_objects"`
	Sys         uint64 `json:"sys"`
	// Detailed breakdown for memory diagnostics
	HeapIdle     uint64 `json:"heap_idle"`     // Heap spans with no objects (available for reuse)
	HeapReleased uint64 `json:"heap_released"` // Heap memory returned to OS
	HeapSys      uint64 `json:"heap_sys"`      // Heap memory obtained from OS
	StackInuse   uint64 `json:"stack_inuse"`   // Goroutine stack memory
	StackSys     uint64 `json:"stack_sys"`     // Stack memory obtained from OS
	MSpanInuse   uint64 `json:"mspan_inuse"`   // MSpan structure memory
	MCacheInuse  uint64 `json:"mcache_inuse"`  // MCache structure memory
	BuckHashSys  uint64 `json:"buck_hash_sys"` // Profiling bucket hash table
	GCSys        uint64 `json:"gc_sys"`        // GC metadata memory
	OtherSys     uint64 `json:"other_sys"`     // Other runtime allocations
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

// StackFrame represents a single frame in a call stack
type StackFrame struct {
	Function string `json:"function"`
	File     string `json:"file"`
	Line     int    `json:"line"`
}

// AllocationInfo represents a single allocation site with full call stack
type AllocationInfo struct {
	Function string       `json:"function"`
	File     string       `json:"file"`
	Line     int          `json:"line"`
	Bytes    int64        `json:"bytes"`
	Count    int64        `json:"count"`
	Stack    []StackFrame `json:"stack"`
}

// GoroutineGroup represents a group of goroutines with the same stack trace
type GoroutineGroup struct {
	Count       int      `json:"count"`
	State       string   `json:"state"`
	WaitReason  string   `json:"wait_reason,omitempty"`
	WaitMinutes int      `json:"wait_minutes,omitempty"` // Parsed wait time in minutes
	TopFunc     string   `json:"top_func"`
	Stack       []string `json:"stack"`
	Category    string   `json:"category"` // "background", "per-request", "unknown"
}

// GoroutineProfile represents the goroutine profile response
type GoroutineProfile struct {
	Timestamp       string           `json:"timestamp"`
	TotalGoroutines int              `json:"total_goroutines"`
	Groups          []GoroutineGroup `json:"groups"`
	Summary         GoroutineSummary `json:"summary"`
	RawProfile      string           `json:"raw_profile,omitempty"`
}

// GoroutineSummary provides a quick overview of goroutine health
type GoroutineSummary struct {
	Background       int `json:"background"`        // Expected long-running goroutines
	PerRequest       int `json:"per_request"`       // Goroutines that should complete with requests
	LongWaiting      int `json:"long_waiting"`      // Goroutines waiting > 1 minute (potential leaks)
	PotentiallyStuck int `json:"potentially_stuck"` // Per-request goroutines waiting > 1 minute
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

// NewDevPprofHandler creates a new dev pprof handler.
// Sets a lower MemProfileRate for better heap profiling coverage
// (1 sample per 64KB instead of default 1 per 512KB — 8x more coverage
// without the huge overhead of 4KB sampling which caused 4GB+ peak memory).
func NewDevPprofHandler() *DevPprofHandler {
	runtime.MemProfileRate = 65536
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

	// Find the indices for inuse_objects and inuse_space sample types.
	// Using inuse (current live) instead of alloc (cumulative) to show
	// what is actually consuming memory right now.
	var inuseObjectsIdx, inuseSpaceIdx int
	for i, st := range p.SampleType {
		switch st.Type {
		case "inuse_objects":
			inuseObjectsIdx = i
		case "inuse_space":
			inuseSpaceIdx = i
		}
	}

	// Build allocation entries with full stack traces.
	// Each unique stack trace is a separate entry so callers are visible.
	allocMap := make(map[string]*AllocationInfo)

	for _, sample := range p.Sample {
		if len(sample.Location) == 0 {
			continue
		}

		inuseBytes := sample.Value[inuseSpaceIdx]
		inuseObjects := sample.Value[inuseObjectsIdx]
		if inuseBytes == 0 && inuseObjects == 0 {
			continue
		}

		// Build the full stack trace (innermost first)
		var stack []StackFrame
		var topFn StackFrame
		hasTop := false
		isProfiler := false
		for _, loc := range sample.Location {
			for _, line := range loc.Line {
				fn := line.Function
				if fn == nil {
					continue
				}
				if isProfilerFunction(fn.Name, fn.Filename) {
					isProfiler = true
					break
				}
				frame := StackFrame{
					Function: fn.Name,
					File:     fn.Filename,
					Line:     int(line.Line),
				}
				stack = append(stack, frame)
				if !hasTop {
					topFn = frame
					hasTop = true
				}
			}
			if isProfiler {
				break
			}
		}
		if isProfiler || !hasTop || len(stack) == 0 {
			continue
		}

		// Build a key from the full stack to group identical call paths
		var keyBuilder strings.Builder
		for _, frame := range stack {
			keyBuilder.WriteString(frame.File)
			keyBuilder.WriteByte(':')
			keyBuilder.WriteString(frame.Function)
			keyBuilder.WriteByte(':')
			keyBuilder.WriteString(fmt.Sprintf("%d", frame.Line))
			keyBuilder.WriteByte(';')
		}
		key := keyBuilder.String()

		if existing, ok := allocMap[key]; ok {
			existing.Bytes += inuseBytes
			existing.Count += inuseObjects
		} else {
			allocMap[key] = &AllocationInfo{
				Function: topFn.Function,
				File:     topFn.File,
				Line:     topFn.Line,
				Bytes:    inuseBytes,
				Count:    inuseObjects,
				Stack:    stack,
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
	r.GET("/api/dev/pprof/goroutines", lib.ChainMiddlewares(h.getGoroutines, middlewares...))
	r.GET("/api/dev/pprof/pools", lib.ChainMiddlewares(h.getPoolStats, middlewares...))
}

// getPprof handles GET /api/dev/pprof
func (h *DevPprofHandler) getPprof(ctx *fasthttp.RequestCtx) {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	data := PprofData{
		Timestamp: time.Now().Format(time.RFC3339),
		Memory: MemoryStats{
			Alloc:        memStats.Alloc,
			TotalAlloc:   memStats.TotalAlloc,
			HeapInuse:    memStats.HeapInuse,
			HeapObjects:  memStats.HeapObjects,
			Sys:          memStats.Sys,
			HeapIdle:     memStats.HeapIdle,
			HeapReleased: memStats.HeapReleased,
			HeapSys:      memStats.HeapSys,
			StackInuse:   memStats.StackInuse,
			StackSys:     memStats.StackSys,
			MSpanInuse:   memStats.MSpanInuse,
			MCacheInuse:  memStats.MCacheInuse,
			BuckHashSys:  memStats.BuckHashSys,
			GCSys:        memStats.GCSys,
			OtherSys:     memStats.OtherSys,
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

// getGoroutines handles GET /api/dev/pprof/goroutines
// Returns goroutine stack traces grouped by stack signature
func (h *DevPprofHandler) getGoroutines(ctx *fasthttp.RequestCtx) {
	// Check if raw output is requested
	includeRaw := string(ctx.QueryArgs().Peek("raw")) == "true"

	// Get goroutine profile
	var buf bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 2); err != nil {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		SendJSON(ctx, map[string]string{"error": "failed to get goroutine profile"})
		return
	}

	rawProfile := buf.String()
	allGroups := parseGoroutineProfile(rawProfile)

	// Filter out profiler goroutines and calculate summary
	groups := make([]GoroutineGroup, 0, len(allGroups))
	summary := GoroutineSummary{}
	profilerGoroutineCount := 0

	for i := range allGroups {
		categorizeGoroutine(&allGroups[i])

		// Skip profiler's own goroutines
		if isProfilerGoroutine(&allGroups[i]) {
			profilerGoroutineCount += allGroups[i].Count
			continue
		}

		groups = append(groups, allGroups[i])

		switch allGroups[i].Category {
		case "background":
			summary.Background += allGroups[i].Count
		case "per-request":
			summary.PerRequest += allGroups[i].Count
		}

		if allGroups[i].WaitMinutes >= 1 {
			summary.LongWaiting += allGroups[i].Count
			if allGroups[i].Category == "per-request" {
				summary.PotentiallyStuck += allGroups[i].Count
			}
		}
	}

	// Sort: potentially stuck first, then by wait time, then by count
	sort.Slice(groups, func(i, j int) bool {
		// Potentially stuck (per-request + long wait) first
		iStuck := groups[i].Category == "per-request" && groups[i].WaitMinutes >= 1
		jStuck := groups[j].Category == "per-request" && groups[j].WaitMinutes >= 1
		if iStuck != jStuck {
			return iStuck
		}
		// Then by wait time
		if groups[i].WaitMinutes != groups[j].WaitMinutes {
			return groups[i].WaitMinutes > groups[j].WaitMinutes
		}
		// Then by count
		return groups[i].Count > groups[j].Count
	})

	// Calculate app goroutines (total minus profiler goroutines)
	// Calculate total goroutines from profile snapshot
	totalFromProfile := 0
	for _, g := range groups {
		totalFromProfile += g.Count
	}

	response := GoroutineProfile{
		Timestamp:       time.Now().Format(time.RFC3339),
		TotalGoroutines: totalFromProfile,
		Groups:          groups,
		Summary:         summary,
	}

	if includeRaw {
		response.RawProfile = rawProfile
	}

	SendJSON(ctx, response)
}

// getPoolStats handles GET /api/dev/pprof/pools
// Returns statistics for all registered object pools.
// In production builds (without -tags pooldebug), returns an empty array.
// In debug builds, returns per-pool acquire/release/create/active counts and hit rates.
func (h *DevPprofHandler) getPoolStats(ctx *fasthttp.RequestCtx) {
	stats := pool.AllStats()
	if stats == nil {
		stats = []pool.PoolStats{}
	}
	SendJSON(ctx, map[string]interface{}{
		"timestamp": time.Now().Format(time.RFC3339),
		"build":     poolBuildMode,
		"pools":     stats,
	})
}

// categorizeGoroutine determines if a goroutine is a background worker or per-request
func categorizeGoroutine(g *GoroutineGroup) {
	// Parse wait time from wait reason (e.g., "5 minutes" -> 5)
	g.WaitMinutes = parseWaitMinutes(g.WaitReason)

	stackStr := strings.Join(g.Stack, " ")

	// Background goroutines - expected to run forever
	backgroundPatterns := []string{
		"requestWorker",                  // Provider queue workers
		"collectLoop",                    // Metrics collector
		"cleanupWorker",                  // Various cleanup workers
		"startAccumulatorMapCleanup",     // Stream accumulator cleanup
		"cleanupOldTraces",               // Trace store cleanup
		"startCleanup",                   // Generic cleanup
		"monitorLoop",                    // Health monitor
		"StartHeartbeat",                 // WebSocket heartbeat
		"time.Sleep",                     // Ticker-based workers
		"runtime.gopark",                 // Runtime parking (often tickers)
		"sync.(*Cond).Wait",              // Condition variable waits
		"net/http.(*persistConn)",        // HTTP connection pool
		"internal/poll.runtime_pollWait", // Network polling
	}

	for _, pattern := range backgroundPatterns {
		if strings.Contains(stackStr, pattern) {
			g.Category = "background"
			return
		}
	}

	// Per-request goroutines - should complete when request ends
	perRequestPatterns := []string{
		"PreLLMHook",
		"PostLLMHook",
		"PreMCPHook",
		"PostMCPHook",
		"HTTPTransportPreHook",
		"HTTPTransportPostHook",
		"completeAndFlushTrace",
		"ProcessAndSend",
		"handleProvider",
		"Inject",                // Observability plugin inject
		"insertInitialLogEntry", // Logging
		"updateLogEntry",        // Logging
		"updateStreamingLogEntry",
		"retryOnNotFound",
		"BroadcastLogUpdate",
	}

	for _, pattern := range perRequestPatterns {
		if strings.Contains(stackStr, pattern) {
			g.Category = "per-request"
			return
		}
	}

	g.Category = "unknown"
}

// parseWaitMinutes extracts wait time in minutes from wait reason string
func parseWaitMinutes(waitReason string) int {
	if waitReason == "" {
		return 0
	}

	// Match patterns like "5 minutes", "1 minute", "30 seconds", "2 hours"
	minuteRegex := regexp.MustCompile(`(\d+)\s*minute`)
	if matches := minuteRegex.FindStringSubmatch(waitReason); len(matches) >= 2 {
		if mins, err := strconv.Atoi(matches[1]); err == nil {
			return mins
		}
	}

	hourRegex := regexp.MustCompile(`(\d+)\s*hour`)
	if matches := hourRegex.FindStringSubmatch(waitReason); len(matches) >= 2 {
		if hours, err := strconv.Atoi(matches[1]); err == nil {
			return hours * 60
		}
	}

	secondRegex := regexp.MustCompile(`(\d+)\s*second`)
	if matches := secondRegex.FindStringSubmatch(waitReason); len(matches) >= 2 {
		if secs, err := strconv.Atoi(matches[1]); err == nil {
			return secs / 60 // Convert to minutes, will be 0 for < 60 seconds
		}
	}

	return 0
}

// parseGoroutineProfile parses the text output of pprof goroutine profile
// and groups goroutines by their stack trace
func parseGoroutineProfile(profile string) []GoroutineGroup {
	// Regex to match goroutine header: "goroutine N [state, wait reason]:"
	// Examples:
	//   goroutine 1 [running]:
	//   goroutine 42 [select, 5 minutes]:
	//   goroutine 100 [chan receive]:
	headerRegex := regexp.MustCompile(`goroutine \d+ \[([^\]]+)\]:`)

	// Split by "goroutine " to get individual goroutine blocks
	blocks := strings.Split(profile, "goroutine ")

	// Map to group goroutines by stack signature
	groupMap := make(map[string]*GoroutineGroup)

	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}

		// Re-add "goroutine " prefix for regex matching
		fullBlock := "goroutine " + block

		// Extract state from header
		matches := headerRegex.FindStringSubmatch(fullBlock)
		if len(matches) < 2 {
			continue
		}

		stateInfo := matches[1]
		state := stateInfo
		waitReason := ""

		// Parse state and wait reason (e.g., "select, 5 minutes" -> state="select", waitReason="5 minutes")
		if idx := strings.Index(stateInfo, ","); idx != -1 {
			state = strings.TrimSpace(stateInfo[:idx])
			waitReason = strings.TrimSpace(stateInfo[idx+1:])
		}

		// Get stack trace (everything after the header line)
		lines := strings.Split(block, "\n")
		if len(lines) < 2 {
			continue
		}

		// Extract stack frames (skip the header line which is lines[0])
		var stackLines []string
		var topFunc string
		for i := 1; i < len(lines); i++ {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			stackLines = append(stackLines, line)

			// First function line (not a file:line) is the top function
			if topFunc == "" && !strings.HasPrefix(line, "/") && !strings.Contains(line, ".go:") {
				topFunc = line
			}
		}

		if len(stackLines) == 0 {
			continue
		}

		// Create a signature from the stack (top 10 frames for grouping)
		maxFrames := 10
		if len(stackLines) < maxFrames {
			maxFrames = len(stackLines)
		}
		signature := state + "|" + strings.Join(stackLines[:maxFrames], "|")

		// Group by signature
		if existing, ok := groupMap[signature]; ok {
			existing.Count++
		} else {
			groupMap[signature] = &GoroutineGroup{
				Count:      1,
				State:      state,
				WaitReason: waitReason,
				TopFunc:    topFunc,
				Stack:      stackLines,
			}
		}
	}

	// Convert map to slice
	groups := make([]GoroutineGroup, 0, len(groupMap))
	for _, group := range groupMap {
		groups = append(groups, *group)
	}

	return groups
}

// profilerPatterns contains patterns to identify profiler-related code
var profilerPatterns = []string{
	"devpprof",
	"pprof.WriteHeapProfile",
	"pprof.Lookup",
	"profile.Parse",
	"MetricsCollector",
	"collectLoop",
	"getTopAllocations",
	"parseGoroutineProfile",
	"getGoroutines",
	"getCPUSample",
}

// isProfilerFunction checks if a function belongs to the profiler itself
func isProfilerFunction(funcName, fileName string) bool {
	for _, pattern := range profilerPatterns {
		if strings.Contains(funcName, pattern) || strings.Contains(fileName, pattern) {
			return true
		}
	}
	return false
}

// isProfilerGoroutine checks if a goroutine belongs to the profiler
func isProfilerGoroutine(g *GoroutineGroup) bool {
	stackStr := strings.Join(g.Stack, " ")
	for _, pattern := range profilerPatterns {
		if strings.Contains(stackStr, pattern) {
			return true
		}
	}
	return false
}

// Cleanup stops the metrics collector
func (h *DevPprofHandler) Cleanup() {
	if h.collector != nil {
		h.collector.Stop()
	}
}
