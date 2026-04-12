// Package logging provides utility functions and interfaces for the GORM-based logging plugin
package logging

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/streaming"
)

// KeyPair represents an ID-Name pair for keys
type KeyPair struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// LogManager defines the main interface that combines all logging functionality
type LogManager interface {
	// GetLog retrieves a single log entry by ID (includes all fields, including raw_request/raw_response)
	GetLog(ctx context.Context, id string) (*logstore.Log, error)

	// Search searches for log entries based on filters and pagination
	Search(ctx context.Context, filters *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.SearchResult, error)

	// GetSessionLogs returns paginated logs for a single parent_request_id session.
	GetSessionLogs(ctx context.Context, sessionID string, pagination *logstore.PaginationOptions) (*logstore.SessionDetailResult, error)

	// GetSessionSummary returns aggregate totals for a single parent_request_id session.
	GetSessionSummary(ctx context.Context, sessionID string) (*logstore.SessionSummaryResult, error)

	// GetStats calculates statistics for logs matching the given filters
	GetStats(ctx context.Context, filters *logstore.SearchFilters) (*logstore.SearchStats, error)

	// GetHistogram returns time-bucketed request counts for the given filters
	GetHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.HistogramResult, error)

	// GetTokenHistogram returns time-bucketed token usage for the given filters
	GetTokenHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.TokenHistogramResult, error)

	// GetCostHistogram returns time-bucketed cost data with model breakdown for the given filters
	GetCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.CostHistogramResult, error)

	// GetModelHistogram returns time-bucketed model usage with success/error breakdown for the given filters
	GetModelHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ModelHistogramResult, error)

	// GetLatencyHistogram returns time-bucketed latency percentiles for the given filters
	GetLatencyHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.LatencyHistogramResult, error)

	// GetProviderCostHistogram returns time-bucketed cost data with provider breakdown for the given filters
	GetProviderCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderCostHistogramResult, error)

	// GetProviderTokenHistogram returns time-bucketed token usage with provider breakdown for the given filters
	GetProviderTokenHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderTokenHistogramResult, error)

	// GetProviderLatencyHistogram returns time-bucketed latency percentiles with provider breakdown for the given filters
	GetProviderLatencyHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderLatencyHistogramResult, error)

	// GetModelRankings returns models ranked by usage with trend comparison
	GetModelRankings(ctx context.Context, filters *logstore.SearchFilters) (*logstore.ModelRankingResult, error)

	// Get the number of dropped requests
	GetDroppedRequests(ctx context.Context) int64

	// GetAvailableModels returns all unique models from logs
	GetAvailableModels(ctx context.Context) []string

	// GetAvailableAliases returns all unique alias values from logs
	GetAvailableAliases(ctx context.Context) []string

	// GetAvailableSelectedKeys returns all unique selected key ID-Name pairs from logs
	GetAvailableSelectedKeys(ctx context.Context) []KeyPair

	// GetAvailableVirtualKeys returns all unique virtual key ID-Name pairs from logs
	GetAvailableVirtualKeys(ctx context.Context) []KeyPair

	// GetAvailableRoutingRules returns all unique routing rule ID-Name pairs from logs
	GetAvailableRoutingRules(ctx context.Context) []KeyPair

	// GetAvailableRoutingEngines returns all unique routing engine types from logs
	GetAvailableRoutingEngines(ctx context.Context) []string

	// GetAvailableTeams returns all unique team ID-Name pairs from logs
	GetAvailableTeams(ctx context.Context) []KeyPair

	// GetAvailableCustomers returns all unique customer ID-Name pairs from logs
	GetAvailableCustomers(ctx context.Context) []KeyPair

	// GetAvailableUsers returns all unique user IDs from logs
	GetAvailableUsers(ctx context.Context) []KeyPair

	// GetAvailableBusinessUnits returns all unique business unit ID-Name pairs from logs
	GetAvailableBusinessUnits(ctx context.Context) []KeyPair

	// GetAvailableMetadataKeys returns distinct metadata keys and their values from recent logs
	GetAvailableMetadataKeys(ctx context.Context) (map[string][]string, error)

	// GetDimensionCostHistogram returns time-bucketed cost data grouped by the specified dimension
	GetDimensionCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64, dimension logstore.HistogramDimension) (*logstore.DimensionCostHistogramResult, error)

	// GetDimensionTokenHistogram returns time-bucketed token usage grouped by the specified dimension
	GetDimensionTokenHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64, dimension logstore.HistogramDimension) (*logstore.DimensionTokenHistogramResult, error)

	// GetDimensionLatencyHistogram returns time-bucketed latency percentiles grouped by the specified dimension
	GetDimensionLatencyHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64, dimension logstore.HistogramDimension) (*logstore.DimensionLatencyHistogramResult, error)

	// DeleteLog deletes a log entry by its ID
	DeleteLog(ctx context.Context, id string) error

	// DeleteLogs deletes multiple log entries by their IDs
	DeleteLogs(ctx context.Context, ids []string) error

	// RecalculateCosts recomputes missing costs for logs matching the filters
	RecalculateCosts(ctx context.Context, filters *logstore.SearchFilters, limit int) (*RecalculateCostResult, error)

	// MCP Tool Log methods
	// SearchMCPToolLogs searches for MCP tool log entries based on filters and pagination
	SearchMCPToolLogs(ctx context.Context, filters *logstore.MCPToolLogSearchFilters, pagination *logstore.PaginationOptions) (*logstore.MCPToolLogSearchResult, error)

	// GetMCPToolLogStats calculates statistics for MCP tool logs matching the given filters
	GetMCPToolLogStats(ctx context.Context, filters *logstore.MCPToolLogSearchFilters) (*logstore.MCPToolLogStats, error)

	// GetAvailableToolNames returns all unique tool names from MCP tool logs
	GetAvailableToolNames(ctx context.Context) ([]string, error)

	// GetAvailableServerLabels returns all unique server labels from MCP tool logs
	GetAvailableServerLabels(ctx context.Context) ([]string, error)

	// GetAvailableMCPVirtualKeys returns all unique virtual key ID-Name pairs from MCP tool logs
	GetAvailableMCPVirtualKeys(ctx context.Context) []KeyPair

	// GetMCPHistogram returns time-bucketed MCP tool call volume
	GetMCPHistogram(ctx context.Context, filters logstore.MCPToolLogSearchFilters, bucketSizeSeconds int64) (*logstore.MCPHistogramResult, error)

	// GetMCPCostHistogram returns time-bucketed MCP cost data
	GetMCPCostHistogram(ctx context.Context, filters logstore.MCPToolLogSearchFilters, bucketSizeSeconds int64) (*logstore.MCPCostHistogramResult, error)

	// GetMCPTopTools returns the top N MCP tools by call count
	GetMCPTopTools(ctx context.Context, filters logstore.MCPToolLogSearchFilters, limit int) (*logstore.MCPTopToolsResult, error)

	// DeleteMCPToolLogs deletes multiple MCP tool log entries by their IDs
	DeleteMCPToolLogs(ctx context.Context, ids []string) error
}

// PluginLogManager implements LogManager interface wrapping the plugin
type PluginLogManager struct {
	plugin *LoggerPlugin
}

func (p *PluginLogManager) GetLog(ctx context.Context, id string) (*logstore.Log, error) {
	return p.plugin.GetLog(ctx, id)
}

func (p *PluginLogManager) Search(ctx context.Context, filters *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.SearchResult, error) {
	if filters == nil || pagination == nil {
		return nil, fmt.Errorf("filters and pagination cannot be nil")
	}
	return p.plugin.SearchLogs(ctx, *filters, *pagination)
}

func (p *PluginLogManager) GetSessionLogs(ctx context.Context, sessionID string, pagination *logstore.PaginationOptions) (*logstore.SessionDetailResult, error) {
	if pagination == nil {
		return nil, fmt.Errorf("pagination cannot be nil")
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("sessionID cannot be empty")
	}
	return p.plugin.GetSessionLogs(ctx, sessionID, *pagination)
}

func (p *PluginLogManager) GetSessionSummary(ctx context.Context, sessionID string) (*logstore.SessionSummaryResult, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("sessionID cannot be empty")
	}
	return p.plugin.GetSessionSummary(ctx, sessionID)
}

func (p *PluginLogManager) GetStats(ctx context.Context, filters *logstore.SearchFilters) (*logstore.SearchStats, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.GetStats(ctx, *filters)
}

func (p *PluginLogManager) GetHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.HistogramResult, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.GetHistogram(ctx, *filters, bucketSizeSeconds)
}

func (p *PluginLogManager) GetTokenHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.TokenHistogramResult, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.GetTokenHistogram(ctx, *filters, bucketSizeSeconds)
}

func (p *PluginLogManager) GetCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.CostHistogramResult, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.GetCostHistogram(ctx, *filters, bucketSizeSeconds)
}

func (p *PluginLogManager) GetModelHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ModelHistogramResult, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.GetModelHistogram(ctx, *filters, bucketSizeSeconds)
}

func (p *PluginLogManager) GetLatencyHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.LatencyHistogramResult, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.GetLatencyHistogram(ctx, *filters, bucketSizeSeconds)
}

func (p *PluginLogManager) GetProviderCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderCostHistogramResult, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.GetProviderCostHistogram(ctx, *filters, bucketSizeSeconds)
}

func (p *PluginLogManager) GetProviderTokenHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderTokenHistogramResult, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.GetProviderTokenHistogram(ctx, *filters, bucketSizeSeconds)
}

func (p *PluginLogManager) GetProviderLatencyHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderLatencyHistogramResult, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.GetProviderLatencyHistogram(ctx, *filters, bucketSizeSeconds)
}

func (p *PluginLogManager) GetModelRankings(ctx context.Context, filters *logstore.SearchFilters) (*logstore.ModelRankingResult, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.GetModelRankings(ctx, *filters)
}

func (p *PluginLogManager) GetDroppedRequests(ctx context.Context) int64 {
	return p.plugin.droppedRequests.Load()
}

// GetAvailableModels returns all unique models from logs
func (p *PluginLogManager) GetAvailableModels(ctx context.Context) []string {
	return p.plugin.GetAvailableModels(ctx)
}

// GetAvailableAliases returns all unique alias values from logs
func (p *PluginLogManager) GetAvailableAliases(ctx context.Context) []string {
	return p.plugin.GetAvailableAliases(ctx)
}

// GetAvailableSelectedKeys returns all unique selected key ID-Name pairs from logs
func (p *PluginLogManager) GetAvailableSelectedKeys(ctx context.Context) []KeyPair {
	return p.plugin.GetAvailableSelectedKeys(ctx)
}

// GetAvailableVirtualKeys returns all unique virtual key ID-Name pairs from logs
func (p *PluginLogManager) GetAvailableVirtualKeys(ctx context.Context) []KeyPair {
	return p.plugin.GetAvailableVirtualKeys(ctx)
}

// GetAvailableRoutingRules returns all unique routing rule ID-Name pairs from logs
func (p *PluginLogManager) GetAvailableRoutingRules(ctx context.Context) []KeyPair {
	return p.plugin.GetAvailableRoutingRules(ctx)
}

// GetAvailableRoutingEngines returns all unique routing engine types from logs
func (p *PluginLogManager) GetAvailableRoutingEngines(ctx context.Context) []string {
	return p.plugin.GetAvailableRoutingEngines(ctx)
}

// GetAvailableTeams returns all unique team ID-Name pairs from logs.
func (p *PluginLogManager) GetAvailableTeams(ctx context.Context) []KeyPair {
	return p.plugin.GetAvailableTeams(ctx)
}

// GetAvailableCustomers returns all unique customer ID-Name pairs from logs.
func (p *PluginLogManager) GetAvailableCustomers(ctx context.Context) []KeyPair {
	return p.plugin.GetAvailableCustomers(ctx)
}

// GetAvailableUsers returns all unique user IDs from logs.
func (p *PluginLogManager) GetAvailableUsers(ctx context.Context) []KeyPair {
	return p.plugin.GetAvailableUsers(ctx)
}

// GetAvailableBusinessUnits returns all unique business unit ID-Name pairs from logs.
func (p *PluginLogManager) GetAvailableBusinessUnits(ctx context.Context) []KeyPair {
	return p.plugin.GetAvailableBusinessUnits(ctx)
}

// GetDimensionCostHistogram returns time-bucketed cost data grouped by the specified dimension.
func (p *PluginLogManager) GetDimensionCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64, dimension logstore.HistogramDimension) (*logstore.DimensionCostHistogramResult, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.GetDimensionCostHistogram(ctx, *filters, bucketSizeSeconds, dimension)
}

// GetDimensionTokenHistogram returns time-bucketed token usage grouped by the specified dimension.
func (p *PluginLogManager) GetDimensionTokenHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64, dimension logstore.HistogramDimension) (*logstore.DimensionTokenHistogramResult, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.GetDimensionTokenHistogram(ctx, *filters, bucketSizeSeconds, dimension)
}

// GetDimensionLatencyHistogram returns time-bucketed latency percentiles grouped by the specified dimension.
func (p *PluginLogManager) GetDimensionLatencyHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64, dimension logstore.HistogramDimension) (*logstore.DimensionLatencyHistogramResult, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.GetDimensionLatencyHistogram(ctx, *filters, bucketSizeSeconds, dimension)
}

func (p *PluginLogManager) GetAvailableMetadataKeys(ctx context.Context) (map[string][]string, error) {
	if p.plugin == nil || p.plugin.store == nil {
		return map[string][]string{}, nil
	}
	return p.plugin.store.GetDistinctMetadataKeys(ctx)
}

// DeleteLog deletes a log from the log store
func (p *PluginLogManager) DeleteLog(ctx context.Context, id string) error {
	if p.plugin == nil || p.plugin.store == nil {
		return fmt.Errorf("log store not initialized")
	}
	return p.plugin.store.DeleteLog(ctx, id)
}

// DeleteLogs deletes multiple logs from the log store
func (p *PluginLogManager) DeleteLogs(ctx context.Context, ids []string) error {
	if p.plugin == nil || p.plugin.store == nil {
		return fmt.Errorf("log store not initialized")
	}
	return p.plugin.store.DeleteLogs(ctx, ids)
}

func (p *PluginLogManager) RecalculateCosts(ctx context.Context, filters *logstore.SearchFilters, limit int) (*RecalculateCostResult, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.RecalculateCosts(ctx, *filters, limit)
}

// SearchMCPToolLogs searches for MCP tool log entries based on filters and pagination
func (p *PluginLogManager) SearchMCPToolLogs(ctx context.Context, filters *logstore.MCPToolLogSearchFilters, pagination *logstore.PaginationOptions) (*logstore.MCPToolLogSearchResult, error) {
	if filters == nil || pagination == nil {
		return nil, fmt.Errorf("filters and pagination cannot be nil")
	}
	return p.plugin.store.SearchMCPToolLogs(ctx, *filters, *pagination)
}

// GetMCPToolLogStats calculates statistics for MCP tool logs matching the given filters
func (p *PluginLogManager) GetMCPToolLogStats(ctx context.Context, filters *logstore.MCPToolLogSearchFilters) (*logstore.MCPToolLogStats, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.store.GetMCPToolLogStats(ctx, *filters)
}

// GetAvailableToolNames returns all unique tool names from MCP tool logs
func (p *PluginLogManager) GetAvailableToolNames(ctx context.Context) ([]string, error) {
	if p == nil || p.plugin == nil || p.plugin.store == nil {
		return []string{}, nil
	}
	return p.plugin.store.GetAvailableToolNames(ctx)
}

// GetAvailableServerLabels returns all unique server labels from MCP tool logs
func (p *PluginLogManager) GetAvailableServerLabels(ctx context.Context) ([]string, error) {
	if p == nil || p.plugin == nil || p.plugin.store == nil {
		return []string{}, nil
	}
	return p.plugin.store.GetAvailableServerLabels(ctx)
}

// GetAvailableMCPVirtualKeys returns all unique virtual key ID-Name pairs from MCP tool logs
func (p *PluginLogManager) GetAvailableMCPVirtualKeys(ctx context.Context) []KeyPair {
	if p == nil || p.plugin == nil {
		return []KeyPair{}
	}
	return p.plugin.GetAvailableMCPVirtualKeys(ctx)
}

// GetMCPHistogram returns time-bucketed MCP tool call volume
func (p *PluginLogManager) GetMCPHistogram(ctx context.Context, filters logstore.MCPToolLogSearchFilters, bucketSizeSeconds int64) (*logstore.MCPHistogramResult, error) {
	if p.plugin == nil || p.plugin.store == nil {
		return &logstore.MCPHistogramResult{}, nil
	}
	return p.plugin.store.GetMCPHistogram(ctx, filters, bucketSizeSeconds)
}

// GetMCPCostHistogram returns time-bucketed MCP cost data
func (p *PluginLogManager) GetMCPCostHistogram(ctx context.Context, filters logstore.MCPToolLogSearchFilters, bucketSizeSeconds int64) (*logstore.MCPCostHistogramResult, error) {
	if p.plugin == nil || p.plugin.store == nil {
		return &logstore.MCPCostHistogramResult{}, nil
	}
	return p.plugin.store.GetMCPCostHistogram(ctx, filters, bucketSizeSeconds)
}

// GetMCPTopTools returns the top N MCP tools by call count
func (p *PluginLogManager) GetMCPTopTools(ctx context.Context, filters logstore.MCPToolLogSearchFilters, limit int) (*logstore.MCPTopToolsResult, error) {
	if p.plugin == nil || p.plugin.store == nil {
		return &logstore.MCPTopToolsResult{}, nil
	}
	return p.plugin.store.GetMCPTopTools(ctx, filters, limit)
}

// DeleteMCPToolLogs deletes multiple MCP tool log entries by their IDs
func (p *PluginLogManager) DeleteMCPToolLogs(ctx context.Context, ids []string) error {
	if p.plugin == nil || p.plugin.store == nil {
		return fmt.Errorf("log store not initialized")
	}
	return p.plugin.store.DeleteMCPToolLogs(ctx, ids)
}

// GetPluginLogManager returns a LogManager interface for this plugin
func (p *LoggerPlugin) GetPluginLogManager() *PluginLogManager {
	return &PluginLogManager{
		plugin: p,
	}
}

// retryOnNotFound retries a function up to 3 times with 1-second delays if it returns logstore.ErrNotFound
func retryOnNotFound(ctx context.Context, operation func() error) error {
	const maxRetries = 3
	const retryDelay = time.Second

	var lastErr error
	for attempt := range maxRetries {
		err := operation()
		if err == nil {
			return nil
		}

		// Check if the error is logstore.ErrNotFound
		if !errors.Is(err, logstore.ErrNotFound) {
			return err
		}

		lastErr = err

		// Don't wait after the last attempt
		if attempt < maxRetries-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryDelay):
				// Continue to next retry
			}
		}
	}

	return lastErr
}

// extractInputHistory extracts input history from request input
func (p *LoggerPlugin) extractInputHistory(request *schemas.BifrostRequest) ([]schemas.ChatMessage, []schemas.ResponsesMessage) {
	if request.ChatRequest != nil {
		return request.ChatRequest.Input, []schemas.ResponsesMessage{}
	}
	if request.RequestType == schemas.RealtimeRequest && request.ResponsesRequest != nil {
		return extractRealtimeInputHistory(request.ResponsesRequest.Input), []schemas.ResponsesMessage{}
	}
	if request.ResponsesRequest != nil && len(request.ResponsesRequest.Input) > 0 {
		return []schemas.ChatMessage{}, request.ResponsesRequest.Input
	}
	if request.TextCompletionRequest != nil {
		if request.TextCompletionRequest.Input == nil {
			return []schemas.ChatMessage{}, []schemas.ResponsesMessage{}
		}
		var text string
		if request.TextCompletionRequest.Input.PromptStr != nil {
			text = *request.TextCompletionRequest.Input.PromptStr
		} else {
			var stringBuilder strings.Builder
			for _, prompt := range request.TextCompletionRequest.Input.PromptArray {
				stringBuilder.WriteString(prompt)
			}
			text = stringBuilder.String()
		}
		return []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: &text,
				},
			},
		}, []schemas.ResponsesMessage{}
	}
	if request.EmbeddingRequest != nil {
		// Large payload passthrough can intentionally leave Input nil to avoid
		// materializing giant request bodies. Logging should degrade gracefully.
		if request.EmbeddingRequest.Input == nil {
			return []schemas.ChatMessage{}, []schemas.ResponsesMessage{}
		}
		texts := request.EmbeddingRequest.Input.Texts

		if len(texts) == 0 && request.EmbeddingRequest.Input.Text != nil {
			texts = []string{*request.EmbeddingRequest.Input.Text}
		}

		contentBlocks := make([]schemas.ChatContentBlock, len(texts))
		for i, text := range texts {
			// Create a per-iteration copy to avoid reusing the same memory address
			t := text
			contentBlocks[i] = schemas.ChatContentBlock{
				Type: schemas.ChatContentBlockTypeText,
				Text: &t,
			}
		}
		return []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentBlocks: contentBlocks,
				},
			},
		}, []schemas.ResponsesMessage{}
	}
	if request.RerankRequest != nil {
		query := request.RerankRequest.Query
		return []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: &query,
				},
			},
		}, []schemas.ResponsesMessage{}
	}
	if request.OCRRequest != nil {
		var docRef string
		if request.OCRRequest.Document.DocumentURL != nil {
			docRef = *request.OCRRequest.Document.DocumentURL
		} else if request.OCRRequest.Document.ImageURL != nil {
			docRef = *request.OCRRequest.Document.ImageURL
		}
		// Strip query parameters to avoid logging sensitive tokens (e.g., pre-signed URLs)
		if idx := strings.Index(docRef, "?"); idx != -1 {
			docRef = docRef[:idx]
		}
		if docRef == "" {
			return []schemas.ChatMessage{}, []schemas.ResponsesMessage{}
		}
		return []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: &docRef,
				},
			},
		}, []schemas.ResponsesMessage{}
	}
	if request.CountTokensRequest != nil && len(request.CountTokensRequest.Input) > 0 {
		return []schemas.ChatMessage{}, request.CountTokensRequest.Input
	}
	return []schemas.ChatMessage{}, []schemas.ResponsesMessage{}
}

func extractRealtimeInputHistory(input []schemas.ResponsesMessage) []schemas.ChatMessage {
	messages := make([]schemas.ChatMessage, 0, len(input))
	for _, item := range input {
		if item.Type == nil {
			continue
		}
		switch *item.Type {
		case schemas.ResponsesMessageTypeMessage:
			if item.Role == nil || item.Content == nil {
				continue
			}
			content := extractRealtimeResponsesContent(item.Content)
			if content == "" {
				continue
			}
			messages = append(messages, schemas.ChatMessage{
				Role: mapRealtimeResponsesRole(*item.Role),
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr(content),
				},
			})
		case schemas.ResponsesMessageTypeFunctionCallOutput,
			schemas.ResponsesMessageTypeCustomToolCallOutput,
			schemas.ResponsesMessageTypeLocalShellCallOutput,
			schemas.ResponsesMessageTypeComputerCallOutput:
			content := extractRealtimeToolOutputContent(item.ResponsesToolMessage)
			if content == "" {
				continue
			}
			messages = append(messages, schemas.ChatMessage{
				Role: schemas.ChatMessageRoleTool,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr(content),
				},
				ChatToolMessage: &schemas.ChatToolMessage{
					ToolCallID: item.ResponsesToolMessage.CallID,
				},
			})
		}
	}
	return messages
}

func mapRealtimeResponsesRole(role schemas.ResponsesMessageRoleType) schemas.ChatMessageRole {
	switch role {
	case schemas.ResponsesInputMessageRoleAssistant:
		return schemas.ChatMessageRoleAssistant
	case schemas.ResponsesInputMessageRoleSystem:
		return schemas.ChatMessageRoleSystem
	case schemas.ResponsesInputMessageRoleDeveloper:
		return schemas.ChatMessageRoleDeveloper
	default:
		return schemas.ChatMessageRoleUser
	}
}

func extractRealtimeResponsesContent(content *schemas.ResponsesMessageContent) string {
	if content == nil {
		return ""
	}
	if content.ContentStr != nil {
		return strings.TrimSpace(*content.ContentStr)
	}
	parts := make([]string, 0, len(content.ContentBlocks))
	for _, block := range content.ContentBlocks {
		switch {
		case block.Text != nil && strings.TrimSpace(*block.Text) != "":
			parts = append(parts, strings.TrimSpace(*block.Text))
		case block.ResponsesOutputMessageContentRefusal != nil && strings.TrimSpace(block.Refusal) != "":
			parts = append(parts, strings.TrimSpace(block.Refusal))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func extractRealtimeToolOutputContent(toolMessage *schemas.ResponsesToolMessage) string {
	if toolMessage == nil || toolMessage.Output == nil {
		return ""
	}
	switch {
	case toolMessage.Output.ResponsesToolCallOutputStr != nil:
		return strings.TrimSpace(*toolMessage.Output.ResponsesToolCallOutputStr)
	case len(toolMessage.Output.ResponsesFunctionToolCallOutputBlocks) > 0:
		content := &schemas.ResponsesMessageContent{ContentBlocks: toolMessage.Output.ResponsesFunctionToolCallOutputBlocks}
		return extractRealtimeResponsesContent(content)
	default:
		return ""
	}
}

// convertToProcessedStreamResponse converts a StreamAccumulatorResult to ProcessedStreamResponse
// for use with the logging plugin's streaming log update functionality.
func convertToProcessedStreamResponse(result *schemas.StreamAccumulatorResult, requestType schemas.RequestType) *streaming.ProcessedStreamResponse {
	if result == nil {
		return nil
	}

	// Determine stream type from request type
	var streamType streaming.StreamType
	switch requestType {
	case schemas.TextCompletionStreamRequest:
		streamType = streaming.StreamTypeText
	case schemas.ChatCompletionStreamRequest:
		streamType = streaming.StreamTypeChat
	case schemas.ResponsesStreamRequest:
		streamType = streaming.StreamTypeResponses
	case schemas.SpeechStreamRequest:
		streamType = streaming.StreamTypeAudio
	case schemas.TranscriptionStreamRequest:
		streamType = streaming.StreamTypeTranscription
	case schemas.ImageGenerationStreamRequest:
		streamType = streaming.StreamTypeImage
	default:
		streamType = streaming.StreamTypeChat
	}

	// Build accumulated data
	data := &streaming.AccumulatedData{
		RequestID:             result.RequestID,
		Model:                 result.RequestedModel,
		Status:                result.Status,
		Stream:                true,
		Latency:               result.Latency,
		TimeToFirstToken:      result.TimeToFirstToken,
		OutputMessage:         result.OutputMessage,
		OutputMessages:        result.OutputMessages,
		ErrorDetails:          result.ErrorDetails,
		TokenUsage:            result.TokenUsage,
		Cost:                  result.Cost,
		AudioOutput:           result.AudioOutput,
		TranscriptionOutput:   result.TranscriptionOutput,
		ImageGenerationOutput: result.ImageGenerationOutput,
		FinishReason:          result.FinishReason,
		RawResponse:           result.RawResponse,
	}

	// Handle tool calls if present
	if result.OutputMessage != nil && result.OutputMessage.ChatAssistantMessage != nil {
		data.ToolCalls = result.OutputMessage.ChatAssistantMessage.ToolCalls
	}

	resp := &streaming.ProcessedStreamResponse{
		RequestID:      result.RequestID,
		StreamType:     streamType,
		Provider:       result.Provider,
		RequestedModel: result.RequestedModel,
		ResolvedModel:  result.ResolvedModel,
		Data:           data,
	}

	if result.RawRequest != nil {
		rawReq := result.RawRequest
		resp.RawRequest = &rawReq
	}

	return resp
}

func mergeRealtimeMetadata(metadata map[string]interface{}, ctx *schemas.BifrostContext) map[string]interface{} {
	if ctx == nil {
		return metadata
	}
	set := func(key string, ctxKey schemas.BifrostContextKey) {
		if value := bifrost.GetStringFromContext(ctx, ctxKey); value != "" {
			if metadata == nil {
				metadata = make(map[string]interface{})
			}
			metadata[key] = value
		}
	}

	set("realtime_session_id", schemas.BifrostContextKeyRealtimeSessionID)
	set("provider_session_id", schemas.BifrostContextKeyRealtimeProviderSessionID)
	set("realtime_source", schemas.BifrostContextKeyRealtimeSource)
	set("realtime_event_type", schemas.BifrostContextKeyRealtimeEventType)
	if bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyRealtimeSessionID) != "" {
		if metadata == nil {
			metadata = make(map[string]interface{})
		}
		metadata["realtime"] = true
	}
	return metadata
}

// formatRoutingEngineLogs formats routing engine logs into a human-readable string.
// Format: [timestamp] [engine] - message
// Parameters:
//   - logs: Slice of routing engine log entries
//
// Returns:
//   - string: Formatted log string (empty string if no logs)
func formatRoutingEngineLogs(logs []schemas.RoutingEngineLogEntry) string {
	if len(logs) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, log := range logs {
		sb.WriteString(fmt.Sprintf("[%d] [%s] - %s\n", log.Timestamp, log.Engine, log.Message))
	}
	return sb.String()
}
