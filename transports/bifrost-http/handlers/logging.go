// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains logging-related handlers for log search, stats, and management.
package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// LoggingHandler manages HTTP requests for logging operations
type LoggingHandler struct {
	logManager          logging.LogManager
	redactedKeysManager RedactedKeysManager
}

type RedactedKeysManager interface {
	GetAllRedactedKeys(ctx context.Context, ids []string) []schemas.Key
	GetAllRedactedVirtualKeys(ctx context.Context, ids []string) []tables.TableVirtualKey
}

// NewLoggingHandler creates a new logging handler instance
func NewLoggingHandler(logManager logging.LogManager, redactedKeysManager RedactedKeysManager) *LoggingHandler {
	return &LoggingHandler{
		logManager:          logManager,
		redactedKeysManager: redactedKeysManager,
	}
}

// RegisterRoutes registers all logging-related routes
func (h *LoggingHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// Log retrieval with filtering, search, and pagination
	r.GET("/api/logs", lib.ChainMiddlewares(h.getLogs, middlewares...))
	r.GET("/api/logs/stats", lib.ChainMiddlewares(h.getLogsStats, middlewares...))
	r.GET("/api/logs/histogram", lib.ChainMiddlewares(h.getLogsHistogram, middlewares...))
	r.GET("/api/logs/histogram/tokens", lib.ChainMiddlewares(h.getLogsTokenHistogram, middlewares...))
	r.GET("/api/logs/histogram/cost", lib.ChainMiddlewares(h.getLogsCostHistogram, middlewares...))
	r.GET("/api/logs/histogram/models", lib.ChainMiddlewares(h.getLogsModelHistogram, middlewares...))
	r.GET("/api/logs/dropped", lib.ChainMiddlewares(h.getDroppedRequests, middlewares...))
	r.GET("/api/logs/filterdata", lib.ChainMiddlewares(h.getAvailableFilterData, middlewares...))
	r.DELETE("/api/logs", lib.ChainMiddlewares(h.deleteLogs, middlewares...))
	r.POST("/api/logs/recalculate-cost", lib.ChainMiddlewares(h.recalculateLogCosts, middlewares...))
}

// getLogs handles GET /api/logs - Get logs with filtering, search, and pagination via query parameters
func (h *LoggingHandler) getLogs(ctx *fasthttp.RequestCtx) {
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

	result, err := h.logManager.Search(ctx, filters, pagination)
	if err != nil {
		logger.Error("failed to search logs: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Search failed: %v", err))
		return
	}

	selectedKeyIDs := make(map[string]struct{})
	virtualKeyIDs := make(map[string]struct{})
	for _, log := range result.Logs {
		if log.SelectedKeyID != "" {
			selectedKeyIDs[log.SelectedKeyID] = struct{}{}
		}
		if log.VirtualKeyID != nil && *log.VirtualKeyID != "" {
			virtualKeyIDs[*log.VirtualKeyID] = struct{}{}
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

	// Add selected key and virtual key to the result
	for i, log := range result.Logs {
		if log.SelectedKeyID != "" && log.SelectedKeyName != "" {
			result.Logs[i].SelectedKey = findRedactedKey(redactedKeys, log.SelectedKeyID, log.SelectedKeyName)
		}
		if log.VirtualKeyID != nil && log.VirtualKeyName != nil && *log.VirtualKeyID != "" && *log.VirtualKeyName != "" {
			result.Logs[i].VirtualKey = findRedactedVirtualKey(redactedVirtualKeys, *log.VirtualKeyID, *log.VirtualKeyName)
		}
	}

	SendJSON(ctx, result)
}

// getLogsStats handles GET /api/logs/stats - Get statistics for logs with filtering
func (h *LoggingHandler) getLogsStats(ctx *fasthttp.RequestCtx) {
	// Parse query parameters into filters (same as getLogs)
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
		logger.Error("failed to get log stats: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Stats calculation failed: %v", err))
		return
	}

	SendJSON(ctx, stats)
}

// getLogsHistogram handles GET /api/logs/histogram - Get time-bucketed request counts
func (h *LoggingHandler) getLogsHistogram(ctx *fasthttp.RequestCtx) {
	// Parse query parameters into filters (same as getLogsStats)
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

	// Calculate bucket size based on time range
	bucketSizeSeconds := calculateBucketSize(filters.StartTime, filters.EndTime)

	result, err := h.logManager.GetHistogram(ctx, filters, bucketSizeSeconds)
	if err != nil {
		logger.Error("failed to get log histogram: %v", err)
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

// getLogsTokenHistogram handles GET /api/logs/histogram/tokens - Get time-bucketed token usage
func (h *LoggingHandler) getLogsTokenHistogram(ctx *fasthttp.RequestCtx) {
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

// getLogsCostHistogram handles GET /api/logs/histogram/cost - Get time-bucketed cost data with model breakdown
func (h *LoggingHandler) getLogsCostHistogram(ctx *fasthttp.RequestCtx) {
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

// getLogsModelHistogram handles GET /api/logs/histogram/models - Get time-bucketed model usage with success/error breakdown
func (h *LoggingHandler) getLogsModelHistogram(ctx *fasthttp.RequestCtx) {
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

// getDroppedRequests handles GET /api/logs/dropped - Get the number of dropped requests
func (h *LoggingHandler) getDroppedRequests(ctx *fasthttp.RequestCtx) {
	droppedRequests := h.logManager.GetDroppedRequests(ctx)
	SendJSON(ctx, map[string]int64{"dropped_requests": droppedRequests})
}

// getAvailableFilterData handles GET /api/logs/filterdata - Get all unique filter data from logs
func (h *LoggingHandler) getAvailableFilterData(ctx *fasthttp.RequestCtx) {
	models := h.logManager.GetAvailableModels(ctx)
	selectedKeys := h.logManager.GetAvailableSelectedKeys(ctx)
	virtualKeys := h.logManager.GetAvailableVirtualKeys(ctx)

	// Extract IDs for redaction lookup
	selectedKeyIDs := make([]string, len(selectedKeys))
	for i, key := range selectedKeys {
		selectedKeyIDs[i] = key.ID
	}
	virtualKeyIDs := make([]string, len(virtualKeys))
	for i, key := range virtualKeys {
		virtualKeyIDs[i] = key.ID
	}

	redactedSelectedKeys := make(map[string]schemas.Key)
	for _, selectedKey := range h.redactedKeysManager.GetAllRedactedKeys(ctx, selectedKeyIDs) {
		redactedSelectedKeys[selectedKey.ID] = selectedKey
	}
	redactedVirtualKeys := make(map[string]tables.TableVirtualKey)
	for _, virtualKey := range h.redactedKeysManager.GetAllRedactedVirtualKeys(ctx, virtualKeyIDs) {
		redactedVirtualKeys[virtualKey.ID] = virtualKey
	}

	// Check if all selected key ids are present in the redacted selected keys (will not be present in case a key is deleted, but we still need to show its filter)
	for _, selectedKey := range selectedKeys {
		if _, ok := redactedSelectedKeys[selectedKey.ID]; !ok {
			// Create a new key struct directly since we know it doesn't exist
			redactedSelectedKeys[selectedKey.ID] = schemas.Key{
				ID:   selectedKey.ID,
				Name: selectedKey.Name + " (deleted)",
			}
		}
	}

	// Check if all virtual key ids are present in the redacted virtual keys (will not be present in case a virtual key is deleted, but we still need to show its filter)
	for _, virtualKey := range virtualKeys {
		if _, ok := redactedVirtualKeys[virtualKey.ID]; !ok {
			// Create a new virtual key struct directly since we know it doesn't exist
			redactedVirtualKeys[virtualKey.ID] = tables.TableVirtualKey{
				ID:   virtualKey.ID,
				Name: virtualKey.Name + " (deleted)",
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

	SendJSON(ctx, map[string]interface{}{"models": models, "selected_keys": selectedKeysArray, "virtual_keys": virtualKeysArray})
}

// deleteLogs handles DELETE /api/logs - Delete logs by their IDs
func (h *LoggingHandler) deleteLogs(ctx *fasthttp.RequestCtx) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid JSON")
		return
	}

	if len(req.IDs) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "No log IDs provided")
		return
	}

	if err := h.logManager.DeleteLogs(ctx, req.IDs); err != nil {
		logger.Error("failed to delete logs: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to delete logs")
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"message": "Logs deleted successfully",
	})
}

// recalculateLogCosts handles POST /api/logs/recalculate-cost - recompute missing costs in batches
func (h *LoggingHandler) recalculateLogCosts(ctx *fasthttp.RequestCtx) {
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
		logger.Error("failed to recalculate log costs: %v", err)
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
