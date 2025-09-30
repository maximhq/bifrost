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
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/logging"
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
	r.GET("/api/logs", h.getLogs)
	r.GET("/api/logs/dropped", h.getDroppedRequests)
	r.GET("/api/logs/models", h.getAvailableModels)
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
	if contentSearch := string(ctx.QueryArgs().Peek("content_search")); contentSearch != "" {
		filters.ContentSearch = contentSearch
	}

	// Extract pagination parameters
	pagination.Limit = 50 // Default limit
	if limit := string(ctx.QueryArgs().Peek("limit")); limit != "" {
		if i, err := strconv.Atoi(limit); err == nil {
			if i <= 0 {
				SendError(ctx, fasthttp.StatusBadRequest, "limit must be greater than 0", h.logger)
				return
			}
			if i > 1000 {
				SendError(ctx, fasthttp.StatusBadRequest, "limit cannot exceed 1000", h.logger)
				return
			}
			pagination.Limit = i
		}
	}

	pagination.Offset = 0 // Default offset
	if offset := string(ctx.QueryArgs().Peek("offset")); offset != "" {
		if i, err := strconv.Atoi(offset); err == nil {
			if i < 0 {
				SendError(ctx, fasthttp.StatusBadRequest, "offset cannot be negative", h.logger)
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

	result, err := h.logManager.Search(filters, pagination)
	if err != nil {
		h.logger.Error("failed to search logs: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Search failed: %v", err), h.logger)
		return
	}	
	SendJSON(ctx, result, h.logger)
}

// getDroppedRequests handles GET /api/logs/dropped - Get the number of dropped requests
func (h *LoggingHandler) getDroppedRequests(ctx *fasthttp.RequestCtx) {
	droppedRequests := h.logManager.GetDroppedRequests()
	SendJSON(ctx, map[string]int64{"dropped_requests": droppedRequests}, h.logger)
}

// getAvailableModels handles GET /api/logs/models - Get all unique models from logs
func (h *LoggingHandler) getAvailableModels(ctx *fasthttp.RequestCtx) {
	models := h.logManager.GetAvailableModels()
	SendJSON(ctx, map[string]interface{}{"models": models}, h.logger)
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
