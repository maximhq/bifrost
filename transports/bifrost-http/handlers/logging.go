// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains logging-related handlers for trace search, stats, and management.
package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
	"golang.org/x/sync/errgroup"
)

// LoggingHandler manages HTTP requests for logging operations
type LoggingHandler struct {
	logManager          logging.LogManager
	redactedKeysManager RedactedKeysManager
	config              *lib.Config
}

type RedactedKeysManager interface {
	GetAllRedactedKeys(ctx context.Context, ids []string) []schemas.Key
	GetAllRedactedVirtualKeys(ctx context.Context, ids []string) []tables.TableVirtualKey
	GetAllRedactedRoutingRules(ctx context.Context, ids []string) []tables.TableRoutingRule
}

// NewLoggingHandler creates a new logging handler instance
func NewLoggingHandler(logManager logging.LogManager, redactedKeysManager RedactedKeysManager, config *lib.Config) *LoggingHandler {
	return &LoggingHandler{
		logManager:          logManager,
		redactedKeysManager: redactedKeysManager,
		config:              config,
	}
}

func (h *LoggingHandler) shouldHideDeletedVirtualKeysInFilters() bool {
	if h == nil || h.config == nil {
		return false
	}
	return h.config.ClientConfig.HideDeletedVirtualKeysInFilters
}

// RegisterRoutes registers all logging-related routes
func (h *LoggingHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// Trace retrieval with filtering, search, and pagination
	r.GET("/api/traces", lib.ChainMiddlewares(h.getTraces, middlewares...))
	r.GET("/api/traces/{id}", lib.ChainMiddlewares(h.getTraceByID, middlewares...))
	r.GET("/api/traces/stats", lib.ChainMiddlewares(h.getTracesStats, middlewares...))
	r.GET("/api/traces/histogram", lib.ChainMiddlewares(h.getTracesHistogram, middlewares...))
	r.GET("/api/traces/histogram/tokens", lib.ChainMiddlewares(h.getTracesTokenHistogram, middlewares...))
	r.GET("/api/traces/histogram/cost", lib.ChainMiddlewares(h.getTracesCostHistogram, middlewares...))
	r.GET("/api/traces/histogram/models", lib.ChainMiddlewares(h.getTracesModelHistogram, middlewares...))
	r.GET("/api/traces/histogram/latency", lib.ChainMiddlewares(h.getTracesLatencyHistogram, middlewares...))
	r.GET("/api/traces/histogram/cost/by-provider", lib.ChainMiddlewares(h.getTracesProviderCostHistogram, middlewares...))
	r.GET("/api/traces/histogram/tokens/by-provider", lib.ChainMiddlewares(h.getTracesProviderTokenHistogram, middlewares...))
	r.GET("/api/traces/histogram/latency/by-provider", lib.ChainMiddlewares(h.getTracesProviderLatencyHistogram, middlewares...))
	r.GET("/api/traces/dropped", lib.ChainMiddlewares(h.getDroppedRequests, middlewares...))
	r.GET("/api/traces/filterdata", lib.ChainMiddlewares(h.getAvailableFilterData, middlewares...))
	r.DELETE("/api/traces", lib.ChainMiddlewares(h.deleteTraces, middlewares...))
	r.POST("/api/traces/recalculate-cost", lib.ChainMiddlewares(h.recalculateTraceCosts, middlewares...))
}

// getTraces handles GET /api/traces - Get traces with filtering, search, and pagination via query parameters
func (h *LoggingHandler) getTraces(ctx *fasthttp.RequestCtx) {
	// Parse query parameters into filters
	filters := &logstore.SearchFilters{}
	pagination := &logstore.PaginationOptions{}

	// Extract filters from query parameters
	if providers := string(ctx.QueryArgs().Peek("providers")); providers != "" {
		filters.Providers = parseCommaSeparated(providers)
	}
	if models := string(ctx.QueryArgs().Peek("models")); models != "" {
		filters.Models = parseCommaSeparated(models)
	}
	if statuses := string(ctx.QueryArgs().Peek("status")); statuses != "" {
		filters.Status = parseCommaSeparated(statuses)
	}
	if objects := string(ctx.QueryArgs().Peek("objects")); objects != "" {
		filters.Objects = parseCommaSeparated(objects)
	}
	if selectedKeyIDs := string(ctx.QueryArgs().Peek("selected_key_ids")); selectedKeyIDs != "" {
		filters.SelectedKeyIDs = parseCommaSeparated(selectedKeyIDs)
	}
	if virtualKeyIDs := string(ctx.QueryArgs().Peek("virtual_key_ids")); virtualKeyIDs != "" {
		filters.VirtualKeyIDs = parseCommaSeparated(virtualKeyIDs)
	}
	if routingRuleIDs := string(ctx.QueryArgs().Peek("routing_rule_ids")); routingRuleIDs != "" {
		filters.RoutingRuleIDs = parseCommaSeparated(routingRuleIDs)
	}
	if routingEngines := string(ctx.QueryArgs().Peek("routing_engine_used")); routingEngines != "" {
		filters.RoutingEngineUsed = parseCommaSeparated(routingEngines)
	}
	if startTime := string(ctx.QueryArgs().Peek("start_time")); startTime != "" {
		if t, err := time.Parse(time.RFC3339, startTime); err == nil {
			filters.StartTime = &t
		}
	}
	if endTime := string(ctx.QueryArgs().Peek("end_time")); endTime != "" {
		if t, err := time.Parse(time.RFC3339, endTime); err == nil {
			filters.EndTime = &t
		}
	}
	if minLatency := string(ctx.QueryArgs().Peek("min_latency")); minLatency != "" {
		if f, err := strconv.ParseFloat(minLatency, 64); err == nil {
			filters.MinLatency = &f
		}
	}
	if maxLatency := string(ctx.QueryArgs().Peek("max_latency")); maxLatency != "" {
		if val, err := strconv.ParseFloat(maxLatency, 64); err == nil {
			filters.MaxLatency = &val
		}
	}
	if minTokens := string(ctx.QueryArgs().Peek("min_tokens")); minTokens != "" {
		if val, err := strconv.Atoi(minTokens); err == nil {
			filters.MinTokens = &val
		}
	}
	if maxTokens := string(ctx.QueryArgs().Peek("max_tokens")); maxTokens != "" {
		if val, err := strconv.Atoi(maxTokens); err == nil {
			filters.MaxTokens = &val
		}
	}
	if cost := string(ctx.QueryArgs().Peek("min_cost")); cost != "" {
		if val, err := strconv.ParseFloat(cost, 64); err == nil {
			filters.MinCost = &val
		}
	}
	if maxCost := string(ctx.QueryArgs().Peek("max_cost")); maxCost != "" {
		if val, err := strconv.ParseFloat(maxCost, 64); err == nil {
			filters.MaxCost = &val
		}
	}
	if missingCost := string(ctx.QueryArgs().Peek("missing_cost_only")); missingCost != "" {
		if val, err := strconv.ParseBool(missingCost); err == nil {
			filters.MissingCostOnly = val
		}
	}
	if contentSearch := string(ctx.QueryArgs().Peek("content_search")); contentSearch != "" {
		filters.ContentSearch = contentSearch
	}

	// Extract pagination parameters
	pagination.Limit = 50 // Default limit
	if limit := string(ctx.QueryArgs().Peek("limit")); limit != "" {
		if i, err := strconv.Atoi(limit); err == nil {
			if i <= 0 {
				SendError(ctx, fasthttp.StatusBadRequest, "limit must be greater than 0")
				return
			}
			if i > 1000 {
				SendError(ctx, fasthttp.StatusBadRequest, "limit cannot exceed 1000")
				return
			}
			pagination.Limit = i
		}
	}

	pagination.Offset = 0 // Default offset
	if offset := string(ctx.QueryArgs().Peek("offset")); offset != "" {
		if i, err := strconv.Atoi(offset); err == nil {
			if i < 0 {
				SendError(ctx, fasthttp.StatusBadRequest, "offset cannot be negative")
				return
			}
			pagination.Offset = i
		}
	}

	// Sort parameters
	pagination.SortBy = "timestamp" // Default sort field
	if sortBy := string(ctx.QueryArgs().Peek("sort_by")); sortBy != "" {
		if sortBy == "timestamp" || sortBy == "latency" || sortBy == "tokens" || sortBy == "cost" {
			pagination.SortBy = sortBy
		}
	}

	pagination.Order = "desc" // Default sort order
	if order := string(ctx.QueryArgs().Peek("order")); order != "" {
		if order == "asc" || order == "desc" {
			pagination.Order = order
		}
	}

	result, err := h.logManager.SearchTraces(ctx, filters, pagination)
	if err != nil {
		logger.Error("failed to search traces: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Search failed: %v", err))
		return
	}

	selectedKeyIDs := make(map[string]struct{})
	virtualKeyIDs := make(map[string]struct{})
	routingRuleIDs := make(map[string]struct{})
	for _, trace := range result.Traces {
		if trace.SelectedKeyID != "" {
			selectedKeyIDs[trace.SelectedKeyID] = struct{}{}
		}
		if trace.VirtualKeyID != nil && *trace.VirtualKeyID != "" {
			virtualKeyIDs[*trace.VirtualKeyID] = struct{}{}
		}
		if trace.RoutingRuleID != nil && *trace.RoutingRuleID != "" {
			routingRuleIDs[*trace.RoutingRuleID] = struct{}{}
		}
	}

	toSlice := func(m map[string]struct{}) []string {
		if len(m) == 0 {
			return nil
		}
		out := make([]string, 0, len(m))
		for id := range m {
			out = append(out, id)
		}
		return out
	}

	redactedKeys := h.redactedKeysManager.GetAllRedactedKeys(ctx, toSlice(selectedKeyIDs))
	redactedVirtualKeys := h.redactedKeysManager.GetAllRedactedVirtualKeys(ctx, toSlice(virtualKeyIDs))
	redactedRoutingRules := h.redactedKeysManager.GetAllRedactedRoutingRules(ctx, toSlice(routingRuleIDs))

	// Add selected key, virtual key, and routing rule to the result
	for i, trace := range result.Traces {
		if trace.SelectedKeyID != "" && trace.SelectedKeyName != "" {
			result.Traces[i].SelectedKey = findRedactedKey(redactedKeys, trace.SelectedKeyID, trace.SelectedKeyName)
		}
		if trace.VirtualKeyID != nil && trace.VirtualKeyName != nil && *trace.VirtualKeyID != "" && *trace.VirtualKeyName != "" {
			result.Traces[i].VirtualKey = findRedactedVirtualKey(redactedVirtualKeys, *trace.VirtualKeyID, *trace.VirtualKeyName)
		}
		if trace.RoutingRuleID != nil && trace.RoutingRuleName != nil && *trace.RoutingRuleID != "" && *trace.RoutingRuleName != "" {
			result.Traces[i].RoutingRule = findRedactedRoutingRule(redactedRoutingRules, *trace.RoutingRuleID, *trace.RoutingRuleName)
		}
	}

	SendJSON(ctx, result)
}

// getTraceByID handles GET /api/traces/{id} - Get a single trace by ID including all spans
func (h *LoggingHandler) getTraceByID(ctx *fasthttp.RequestCtx) {
	id, ok := ctx.UserValue("id").(string)
	if !ok || id == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "trace id is required")
		return
	}

	root, children, err := h.logManager.GetTrace(ctx, id)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get trace: %v", err))
		return
	}

	if root == nil {
		SendError(ctx, fasthttp.StatusNotFound, "trace not found")
		return
	}

	SendJSON(ctx, struct {
		Trace *logstore.SpanLog   `json:"trace"`
		Spans []*logstore.SpanLog `json:"spans"`
	}{
		Trace: root,
		Spans: children,
	})
}

// getTracesStats handles GET /api/traces/stats - Get statistics for traces with filtering
func (h *LoggingHandler) getTracesStats(ctx *fasthttp.RequestCtx) {
	// Parse query parameters into filters (same as getTraces)
	filters := &logstore.SearchFilters{}

	// Extract filters from query parameters
	if providers := string(ctx.QueryArgs().Peek("providers")); providers != "" {
		filters.Providers = parseCommaSeparated(providers)
	}
	if models := string(ctx.QueryArgs().Peek("models")); models != "" {
		filters.Models = parseCommaSeparated(models)
	}
	if statuses := string(ctx.QueryArgs().Peek("status")); statuses != "" {
		filters.Status = parseCommaSeparated(statuses)
	}
	if objects := string(ctx.QueryArgs().Peek("objects")); objects != "" {
		filters.Objects = parseCommaSeparated(objects)
	}
	if selectedKeyIDs := string(ctx.QueryArgs().Peek("selected_key_ids")); selectedKeyIDs != "" {
		filters.SelectedKeyIDs = parseCommaSeparated(selectedKeyIDs)
	}
	if virtualKeyIDs := string(ctx.QueryArgs().Peek("virtual_key_ids")); virtualKeyIDs != "" {
		filters.VirtualKeyIDs = parseCommaSeparated(virtualKeyIDs)
	}
	if routingRuleIDs := string(ctx.QueryArgs().Peek("routing_rule_ids")); routingRuleIDs != "" {
		filters.RoutingRuleIDs = parseCommaSeparated(routingRuleIDs)
	}
	if routingEngines := string(ctx.QueryArgs().Peek("routing_engine_used")); routingEngines != "" {
		filters.RoutingEngineUsed = parseCommaSeparated(routingEngines)
	}
	if startTime := string(ctx.QueryArgs().Peek("start_time")); startTime != "" {
		if t, err := time.Parse(time.RFC3339, startTime); err == nil {
			filters.StartTime = &t
		}
	}
	if endTime := string(ctx.QueryArgs().Peek("end_time")); endTime != "" {
		if t, err := time.Parse(time.RFC3339, endTime); err == nil {
			filters.EndTime = &t
		}
	}
	if minLatency := string(ctx.QueryArgs().Peek("min_latency")); minLatency != "" {
		if f, err := strconv.ParseFloat(minLatency, 64); err == nil {
			filters.MinLatency = &f
		}
	}
	if maxLatency := string(ctx.QueryArgs().Peek("max_latency")); maxLatency != "" {
		if val, err := strconv.ParseFloat(maxLatency, 64); err == nil {
			filters.MaxLatency = &val
		}
	}
	if minTokens := string(ctx.QueryArgs().Peek("min_tokens")); minTokens != "" {
		if val, err := strconv.Atoi(minTokens); err == nil {
			filters.MinTokens = &val
		}
	}
	if maxTokens := string(ctx.QueryArgs().Peek("max_tokens")); maxTokens != "" {
		if val, err := strconv.Atoi(maxTokens); err == nil {
			filters.MaxTokens = &val
		}
	}
	if cost := string(ctx.QueryArgs().Peek("min_cost")); cost != "" {
		if val, err := strconv.ParseFloat(cost, 64); err == nil {
			filters.MinCost = &val
		}
	}
	if maxCost := string(ctx.QueryArgs().Peek("max_cost")); maxCost != "" {
		if val, err := strconv.ParseFloat(maxCost, 64); err == nil {
			filters.MaxCost = &val
		}
	}
	if missingCost := string(ctx.QueryArgs().Peek("missing_cost_only")); missingCost != "" {
		if val, err := strconv.ParseBool(missingCost); err == nil {
			filters.MissingCostOnly = val
		}
	}
	if contentSearch := string(ctx.QueryArgs().Peek("content_search")); contentSearch != "" {
		filters.ContentSearch = contentSearch
	}

	stats, err := h.logManager.GetStats(ctx, filters)
	if err != nil {
		logger.Error("failed to get trace stats: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Stats calculation failed: %v", err))
		return
	}

	SendJSON(ctx, stats)
}

// getTracesHistogram handles GET /api/traces/histogram - Get time-bucketed request counts
func (h *LoggingHandler) getTracesHistogram(ctx *fasthttp.RequestCtx) {
	filters := parseHistogramFilters(ctx)
	bucketSizeSeconds := calculateBucketSize(filters.StartTime, filters.EndTime)

	result, err := h.logManager.GetHistogram(ctx, filters, bucketSizeSeconds)
	if err != nil {
		logger.Error("failed to get trace histogram: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Histogram calculation failed: %v", err))
		return
	}

	SendJSON(ctx, result)
}

// calculateBucketSize determines appropriate bucket size based on time range
func calculateBucketSize(start, end *time.Time) int64 {
	if start == nil || end == nil {
		return 3600 // Default 1 hour
	}
	duration := end.Sub(*start)
	switch {
	case duration >= 365*24*time.Hour: // >= 12 months
		return 30 * 24 * 3600 // Monthly (30 days)
	case duration >= 90*24*time.Hour: // >= 3 months
		return 7 * 24 * 3600 // Weekly (7 days)
	case duration >= 30*24*time.Hour: // >= 1 month
		return 3 * 24 * 3600 // 3 days
	case duration >= 7*24*time.Hour: // >= 7 days
		return 24 * 3600 // Daily
	case duration >= 3*24*time.Hour: // >= 3 days
		return 8 * 3600 // 8 hours
	case duration >= 24*time.Hour: // >= 24 hours
		return 3600 // Hourly
	case duration >= 2*time.Hour: // >= 2 hours
		return 600 // 10 minutes
	default:
		return 60 // 1 minute buckets for < 2 hours
	}
}

// parseHistogramFilters extracts common filter parameters from query args
func parseHistogramFilters(ctx *fasthttp.RequestCtx) *logstore.SearchFilters {
	filters := &logstore.SearchFilters{}

	if providers := string(ctx.QueryArgs().Peek("providers")); providers != "" {
		filters.Providers = parseCommaSeparated(providers)
	}
	if models := string(ctx.QueryArgs().Peek("models")); models != "" {
		filters.Models = parseCommaSeparated(models)
	}
	if statuses := string(ctx.QueryArgs().Peek("status")); statuses != "" {
		filters.Status = parseCommaSeparated(statuses)
	}
	if objects := string(ctx.QueryArgs().Peek("objects")); objects != "" {
		filters.Objects = parseCommaSeparated(objects)
	}
	if selectedKeyIDs := string(ctx.QueryArgs().Peek("selected_key_ids")); selectedKeyIDs != "" {
		filters.SelectedKeyIDs = parseCommaSeparated(selectedKeyIDs)
	}
	if virtualKeyIDs := string(ctx.QueryArgs().Peek("virtual_key_ids")); virtualKeyIDs != "" {
		filters.VirtualKeyIDs = parseCommaSeparated(virtualKeyIDs)
	}
	if routingRuleIDs := string(ctx.QueryArgs().Peek("routing_rule_ids")); routingRuleIDs != "" {
		filters.RoutingRuleIDs = parseCommaSeparated(routingRuleIDs)
	}
	if routingEngines := string(ctx.QueryArgs().Peek("routing_engine_used")); routingEngines != "" {
		filters.RoutingEngineUsed = parseCommaSeparated(routingEngines)
	}
	if startTime := string(ctx.QueryArgs().Peek("start_time")); startTime != "" {
		if t, err := time.Parse(time.RFC3339, startTime); err == nil {
			filters.StartTime = &t
		}
	}
	if endTime := string(ctx.QueryArgs().Peek("end_time")); endTime != "" {
		if t, err := time.Parse(time.RFC3339, endTime); err == nil {
			filters.EndTime = &t
		}
	}
	if minLatency := string(ctx.QueryArgs().Peek("min_latency")); minLatency != "" {
		if f, err := strconv.ParseFloat(minLatency, 64); err == nil {
			filters.MinLatency = &f
		}
	}
	if maxLatency := string(ctx.QueryArgs().Peek("max_latency")); maxLatency != "" {
		if val, err := strconv.ParseFloat(maxLatency, 64); err == nil {
			filters.MaxLatency = &val
		}
	}
	if minTokens := string(ctx.QueryArgs().Peek("min_tokens")); minTokens != "" {
		if val, err := strconv.Atoi(minTokens); err == nil {
			filters.MinTokens = &val
		}
	}
	if maxTokens := string(ctx.QueryArgs().Peek("max_tokens")); maxTokens != "" {
		if val, err := strconv.Atoi(maxTokens); err == nil {
			filters.MaxTokens = &val
		}
	}
	if cost := string(ctx.QueryArgs().Peek("min_cost")); cost != "" {
		if val, err := strconv.ParseFloat(cost, 64); err == nil {
			filters.MinCost = &val
		}
	}
	if maxCost := string(ctx.QueryArgs().Peek("max_cost")); maxCost != "" {
		if val, err := strconv.ParseFloat(maxCost, 64); err == nil {
			filters.MaxCost = &val
		}
	}
	if missingCost := string(ctx.QueryArgs().Peek("missing_cost_only")); missingCost != "" {
		if val, err := strconv.ParseBool(missingCost); err == nil {
			filters.MissingCostOnly = val
		}
	}
	if contentSearch := string(ctx.QueryArgs().Peek("content_search")); contentSearch != "" {
		filters.ContentSearch = contentSearch
	}

	return filters
}

// getTracesTokenHistogram handles GET /api/traces/histogram/tokens - Get time-bucketed token usage
func (h *LoggingHandler) getTracesTokenHistogram(ctx *fasthttp.RequestCtx) {
	filters := parseHistogramFilters(ctx)
	bucketSizeSeconds := calculateBucketSize(filters.StartTime, filters.EndTime)

	result, err := h.logManager.GetTokenHistogram(ctx, filters, bucketSizeSeconds)
	if err != nil {
		logger.Error("failed to get token histogram: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Token histogram calculation failed: %v", err))
		return
	}

	SendJSON(ctx, result)
}

// getTracesCostHistogram handles GET /api/traces/histogram/cost - Get time-bucketed cost data with model breakdown
func (h *LoggingHandler) getTracesCostHistogram(ctx *fasthttp.RequestCtx) {
	filters := parseHistogramFilters(ctx)
	bucketSizeSeconds := calculateBucketSize(filters.StartTime, filters.EndTime)

	result, err := h.logManager.GetCostHistogram(ctx, filters, bucketSizeSeconds)
	if err != nil {
		logger.Error("failed to get cost histogram: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Cost histogram calculation failed: %v", err))
		return
	}

	SendJSON(ctx, result)
}

// getTracesModelHistogram handles GET /api/traces/histogram/models - Get time-bucketed model usage with success/error breakdown
func (h *LoggingHandler) getTracesModelHistogram(ctx *fasthttp.RequestCtx) {
	filters := parseHistogramFilters(ctx)
	bucketSizeSeconds := calculateBucketSize(filters.StartTime, filters.EndTime)

	result, err := h.logManager.GetModelHistogram(ctx, filters, bucketSizeSeconds)
	if err != nil {
		logger.Error("failed to get model histogram: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Model histogram calculation failed: %v", err))
		return
	}

	SendJSON(ctx, result)
}

// getTracesLatencyHistogram handles GET /api/traces/histogram/latency - Get time-bucketed latency percentiles
func (h *LoggingHandler) getTracesLatencyHistogram(ctx *fasthttp.RequestCtx) {
	filters := parseHistogramFilters(ctx)
	bucketSizeSeconds := calculateBucketSize(filters.StartTime, filters.EndTime)

	result, err := h.logManager.GetLatencyHistogram(ctx, filters, bucketSizeSeconds)
	if err != nil {
		logger.Error("failed to get latency histogram: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Latency histogram calculation failed: %v", err))
		return
	}

	SendJSON(ctx, result)
}

// getTracesProviderCostHistogram handles GET /api/traces/histogram/cost/by-provider - Get time-bucketed cost data with provider breakdown
func (h *LoggingHandler) getTracesProviderCostHistogram(ctx *fasthttp.RequestCtx) {
	filters := parseHistogramFilters(ctx)
	bucketSizeSeconds := calculateBucketSize(filters.StartTime, filters.EndTime)

	result, err := h.logManager.GetProviderCostHistogram(ctx, filters, bucketSizeSeconds)
	if err != nil {
		logger.Error("failed to get provider cost histogram: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Provider cost histogram calculation failed: %v", err))
		return
	}

	SendJSON(ctx, result)
}

// getTracesProviderTokenHistogram handles GET /api/traces/histogram/tokens/by-provider - Get time-bucketed token usage with provider breakdown
func (h *LoggingHandler) getTracesProviderTokenHistogram(ctx *fasthttp.RequestCtx) {
	filters := parseHistogramFilters(ctx)
	bucketSizeSeconds := calculateBucketSize(filters.StartTime, filters.EndTime)

	result, err := h.logManager.GetProviderTokenHistogram(ctx, filters, bucketSizeSeconds)
	if err != nil {
		logger.Error("failed to get provider token histogram: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Provider token histogram calculation failed: %v", err))
		return
	}

	SendJSON(ctx, result)
}

// getTracesProviderLatencyHistogram handles GET /api/traces/histogram/latency/by-provider - Get time-bucketed latency percentiles with provider breakdown
func (h *LoggingHandler) getTracesProviderLatencyHistogram(ctx *fasthttp.RequestCtx) {
	filters := parseHistogramFilters(ctx)
	bucketSizeSeconds := calculateBucketSize(filters.StartTime, filters.EndTime)

	result, err := h.logManager.GetProviderLatencyHistogram(ctx, filters, bucketSizeSeconds)
	if err != nil {
		logger.Error("failed to get provider latency histogram: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Provider latency histogram calculation failed: %v", err))
		return
	}

	SendJSON(ctx, result)
}

// getDroppedRequests handles GET /api/traces/dropped - Get the number of dropped requests
func (h *LoggingHandler) getDroppedRequests(ctx *fasthttp.RequestCtx) {
	droppedRequests := h.logManager.GetDroppedRequests(ctx)
	SendJSON(ctx, map[string]int64{"dropped_requests": droppedRequests})
}

// getAvailableFilterData handles GET /api/traces/filterdata - Get all unique filter data from traces
func (h *LoggingHandler) getAvailableFilterData(ctx *fasthttp.RequestCtx) {
	hideDeletedVirtualKeys := h.shouldHideDeletedVirtualKeysInFilters()

	var (
		models         []string
		selectedKeys   []logging.KeyPair
		virtualKeys    []logging.KeyPair
		routingRules   []logging.KeyPair
		routingEngines []string
		mu             sync.Mutex
	)

	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		result := h.logManager.GetAvailableModels(gCtx)
		mu.Lock()
		models = result
		mu.Unlock()
		return nil
	})
	g.Go(func() error {
		result := h.logManager.GetAvailableSelectedKeys(gCtx)
		mu.Lock()
		selectedKeys = result
		mu.Unlock()
		return nil
	})
	g.Go(func() error {
		result := h.logManager.GetAvailableVirtualKeys(gCtx)
		mu.Lock()
		virtualKeys = result
		mu.Unlock()
		return nil
	})
	g.Go(func() error {
		result := h.logManager.GetAvailableRoutingRules(gCtx)
		mu.Lock()
		routingRules = result
		mu.Unlock()
		return nil
	})
	g.Go(func() error {
		result := h.logManager.GetAvailableRoutingEngines(gCtx)
		mu.Lock()
		routingEngines = result
		mu.Unlock()
		return nil
	})

	if err := g.Wait(); err != nil {
		logger.Error("failed to get filter data: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get filter data: %v", err))
		return
	}

	// Extract IDs for redaction lookup
	selectedKeyIDs := make([]string, len(selectedKeys))
	for i, key := range selectedKeys {
		selectedKeyIDs[i] = key.ID
	}
	virtualKeyIDs := make([]string, len(virtualKeys))
	for i, key := range virtualKeys {
		virtualKeyIDs[i] = key.ID
	}
	routingRuleIDs := make([]string, len(routingRules))
	for i, rule := range routingRules {
		routingRuleIDs[i] = rule.ID
	}

	redactedSelectedKeys := make(map[string]schemas.Key)
	for _, selectedKey := range h.redactedKeysManager.GetAllRedactedKeys(ctx, selectedKeyIDs) {
		redactedSelectedKeys[selectedKey.ID] = selectedKey
	}
	redactedVirtualKeys := make(map[string]tables.TableVirtualKey)
	for _, virtualKey := range h.redactedKeysManager.GetAllRedactedVirtualKeys(ctx, virtualKeyIDs) {
		redactedVirtualKeys[virtualKey.ID] = virtualKey
	}
	redactedRoutingRules := make(map[string]tables.TableRoutingRule)
	for _, routingRule := range h.redactedKeysManager.GetAllRedactedRoutingRules(ctx, routingRuleIDs) {
		redactedRoutingRules[routingRule.ID] = routingRule
	}

	for _, selectedKey := range selectedKeys {
		if _, ok := redactedSelectedKeys[selectedKey.ID]; !ok {
			redactedSelectedKeys[selectedKey.ID] = schemas.Key{
				ID:   selectedKey.ID,
				Name: selectedKey.Name + " (deleted)",
			}
		}
	}

	for _, virtualKey := range virtualKeys {
		if _, ok := redactedVirtualKeys[virtualKey.ID]; !ok {
			if hideDeletedVirtualKeys {
				continue
			}
			redactedVirtualKeys[virtualKey.ID] = tables.TableVirtualKey{
				ID:   virtualKey.ID,
				Name: virtualKey.Name + " (deleted)",
			}
		}
	}

	for _, routingRule := range routingRules {
		if _, ok := redactedRoutingRules[routingRule.ID]; !ok {
			redactedRoutingRules[routingRule.ID] = tables.TableRoutingRule{
				ID:   routingRule.ID,
				Name: routingRule.Name + " (deleted)",
			}
		}
	}

	// Convert maps to arrays for frontend consumption
	selectedKeysArray := make([]schemas.Key, 0, len(redactedSelectedKeys))
	for _, key := range redactedSelectedKeys {
		selectedKeysArray = append(selectedKeysArray, key)
	}

	virtualKeysArray := make([]tables.TableVirtualKey, 0, len(redactedVirtualKeys))
	for _, key := range redactedVirtualKeys {
		virtualKeysArray = append(virtualKeysArray, key)
	}

	routingRulesArray := make([]tables.TableRoutingRule, 0, len(redactedRoutingRules))
	for _, rule := range redactedRoutingRules {
		routingRulesArray = append(routingRulesArray, rule)
	}

	SendJSON(ctx, map[string]interface{}{"models": models, "selected_keys": selectedKeysArray, "virtual_keys": virtualKeysArray, "routing_rules": routingRulesArray, "routing_engines": routingEngines})
}

// deleteTraces handles DELETE /api/traces - Delete traces by their IDs
func (h *LoggingHandler) deleteTraces(ctx *fasthttp.RequestCtx) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid JSON")
		return
	}

	if len(req.IDs) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "No trace IDs provided")
		return
	}

	if err := h.logManager.DeleteTraces(ctx, req.IDs); err != nil {
		logger.Error("failed to delete traces: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to delete traces")
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Traces deleted successfully",
	})
}

// recalculateTraceCosts handles POST /api/traces/recalculate-cost - recompute missing costs in batches
func (h *LoggingHandler) recalculateTraceCosts(ctx *fasthttp.RequestCtx) {
	var payload recalculateCostRequest
	body := ctx.PostBody()
	if len(body) > 0 {
		if err := sonic.Unmarshal(body, &payload); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, "Invalid JSON")
			return
		}
	}

	limit := 200
	if payload.Limit != nil {
		limit = *payload.Limit
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}

	filters := payload.Filters
	filters.MissingCostOnly = true

	result, err := h.logManager.RecalculateCosts(ctx, &filters, limit)
	if err != nil {
		logger.Error("failed to recalculate trace costs: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to recalculate costs: %v", err))
		return
	}

	SendJSON(ctx, result)
}

// Helper functions

func findRedactedKey(redactedKeys []schemas.Key, id string, name string) *schemas.Key {
	if len(redactedKeys) == 0 {
		return &schemas.Key{
			ID: id,
			Name: func() string {
				if name != "" {
					return name + " (deleted)"
				} else {
					return ""
				}
			}(),
		}
	}
	for _, key := range redactedKeys {
		if key.ID == id {
			return &key
		}
	}
	return &schemas.Key{
		ID: id,
		Name: func() string {
			if name != "" {
				return name + " (deleted)"
			} else {
				return ""
			}
		}(),
	}
}

func findRedactedVirtualKey(redactedVirtualKeys []tables.TableVirtualKey, id string, name string) *tables.TableVirtualKey {
	if len(redactedVirtualKeys) == 0 {
		return &tables.TableVirtualKey{
			ID: id,
			Name: func() string {
				if name != "" {
					return name + " (deleted)"
				} else {
					return ""
				}
			}(),
		}
	}
	for _, virtualKey := range redactedVirtualKeys {
		if virtualKey.ID == id {
			return &virtualKey
		}
	}
	return &tables.TableVirtualKey{
		ID: id,
		Name: func() string {
			if name != "" {
				return name + " (deleted)"
			} else {
				return ""
			}
		}(),
	}
}

func findRedactedRoutingRule(redactedRoutingRules []tables.TableRoutingRule, id string, name string) *tables.TableRoutingRule {
	if len(redactedRoutingRules) == 0 {
		return &tables.TableRoutingRule{
			ID: id,
			Name: func() string {
				if name != "" {
					return name + " (deleted)"
				} else {
					return ""
				}
			}(),
		}
	}
	for _, routingRule := range redactedRoutingRules {
		if routingRule.ID == id {
			return &routingRule
		}
	}
	return &tables.TableRoutingRule{
		ID: id,
		Name: func() string {
			if name != "" {
				return name + " (deleted)"
			} else {
				return ""
			}
		}(),
	}
}

// parseCommaSeparated splits a comma-separated string into a slice
func parseCommaSeparated(s string) []string {
	if s == "" {
		return nil
	}

	var result []string
	for _, item := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			result = append(result, trimmed)
		}
	}

	return result
}

type recalculateCostRequest struct {
	Filters logstore.SearchFilters `json:"filters"`
	Limit   *int                   `json:"limit,omitempty"`
}
