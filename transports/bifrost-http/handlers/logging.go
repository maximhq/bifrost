// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains logging-related handlers for log search, stats, and management.
package handlers

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/plugins/logging"
	"github.com/valyala/fasthttp"
)

// LoggingHandler manages HTTP requests for logging operations
type LoggingHandler struct {
	logManager logging.LogManager
	logger     schemas.Logger
}

// NewLoggingHandler creates a new logging handler instance
func NewLoggingHandler(logManager logging.LogManager, logger schemas.Logger) *LoggingHandler {
	return &LoggingHandler{
		logManager: logManager,
		logger:     logger,
	}
}

// RegisterRoutes registers all logging-related routes
func (h *LoggingHandler) RegisterRoutes(r *router.Router) {
	// Log retrieval with filtering, search, and pagination
	r.GET("/v1/logs", h.GetLogs)

	// Statistics endpoint
	r.GET("/v1/logs/stats", h.GetStats)
}

// GetLogs handles GET /v1/logs - Get logs with filtering, search, and pagination via query parameters
func (h *LoggingHandler) GetLogs(ctx *fasthttp.RequestCtx) {
	// Parse query parameters into filters
	filters := &logging.SearchFilters{}
	pagination := &logging.PaginationOptions{}

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
	if contentSearch := string(ctx.QueryArgs().Peek("content_search")); contentSearch != "" {
		filters.ContentSearch = contentSearch
	}

	// Extract pagination parameters
	if limit := string(ctx.QueryArgs().Peek("limit")); limit != "" {
		if i, err := strconv.Atoi(limit); err == nil && i > 0 && i <= 1000 {
			pagination.Limit = i
		}
	}
	if offset := string(ctx.QueryArgs().Peek("offset")); offset != "" {
		if i, err := strconv.Atoi(offset); err == nil && i >= 0 {
			pagination.Offset = i
		}
	}
	if sortBy := string(ctx.QueryArgs().Peek("sort_by")); sortBy != "" {
		if sortBy == "timestamp" || sortBy == "latency" || sortBy == "tokens" {
			pagination.SortBy = sortBy
		}
	}
	if order := string(ctx.QueryArgs().Peek("order")); order != "" {
		if order == "asc" || order == "desc" {
			pagination.Order = order
		}
	}

	if pagination.Limit == 0 {
		pagination.Limit = 50
	}
	if pagination.Offset == 0 {
		pagination.Offset = 0
	}
	if pagination.SortBy == "" {
		pagination.SortBy = "timestamp"
	}
	if pagination.Order == "" {
		pagination.Order = "desc"
	}

	result, err := h.logManager.Search(filters, pagination)
	if err != nil {
		h.logger.Error(fmt.Errorf("failed to search logs: %w", err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Search failed: %v", err), h.logger)
		return
	}

	SendJSON(ctx, result, h.logger)
}

// GetStats handles GET /v1/logs/stats - Retrieve logging statistics
func (h *LoggingHandler) GetStats(ctx *fasthttp.RequestCtx) {
	stats := h.logManager.GetStats()
	if stats == nil {
		h.logger.Error(fmt.Errorf("failed to retrieve stats - nil response"))
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to retrieve statistics", h.logger)
		return
	}

	SendJSON(ctx, stats, h.logger)
}

// Helper functions

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
