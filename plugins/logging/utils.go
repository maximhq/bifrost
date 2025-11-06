// Package logging provides utility functions and interfaces for the GORM-based logging plugin
package logging

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// KeyPair represents an ID-Name pair for keys
type KeyPair struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// LogManager defines the main interface that combines all logging functionality
type LogManager interface {
	// Search searches for log entries based on filters and pagination
	Search(ctx context.Context, filters *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.SearchResult, error)

	// Get the number of dropped requests
	GetDroppedRequests(ctx context.Context) int64

	// GetAvailableModels returns all unique models from logs
	GetAvailableModels(ctx context.Context) []string

	// GetAvailableSelectedKeys returns all unique selected key ID-Name pairs from logs
	GetAvailableSelectedKeys(ctx context.Context) []KeyPair

	// GetAvailableVirtualKeys returns all unique virtual key ID-Name pairs from logs
	GetAvailableVirtualKeys(ctx context.Context) []KeyPair
}

// PluginLogManager implements LogManager interface wrapping the plugin
type PluginLogManager struct {
	plugin *LoggerPlugin
}

func (p *PluginLogManager) Search(ctx context.Context, filters *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.SearchResult, error) {
	if filters == nil || pagination == nil {
		return nil, fmt.Errorf("filters and pagination cannot be nil")
	}
	return p.plugin.SearchLogs(ctx, *filters, *pagination)
}

func (p *PluginLogManager) GetDroppedRequests(ctx context.Context) int64 {
	return p.plugin.droppedRequests.Load()
}

// GetAvailableModels returns all unique models from logs
func (p *PluginLogManager) GetAvailableModels(ctx context.Context) []string {
	return p.plugin.GetAvailableModels(ctx)
}

// GetAvailableSelectedKeys returns all unique selected key ID-Name pairs from logs
func (p *PluginLogManager) GetAvailableSelectedKeys(ctx context.Context) []KeyPair {
	return p.plugin.GetAvailableSelectedKeys(ctx)
}

// GetAvailableVirtualKeys returns all unique virtual key ID-Name pairs from logs
func (p *PluginLogManager) GetAvailableVirtualKeys(ctx context.Context) []KeyPair {
	return p.plugin.GetAvailableVirtualKeys(ctx)
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
	if request.ResponsesRequest != nil && len(request.ResponsesRequest.Input) > 0 {
		return []schemas.ChatMessage{}, request.ResponsesRequest.Input
	}
	if request.TextCompletionRequest != nil {
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
	return []schemas.ChatMessage{}, []schemas.ResponsesMessage{}
}

// getStringFromContext safely extracts a string value from context
func getStringFromContext(ctx context.Context, key any) string {
	if value := ctx.Value(key); value != nil {
		if str, ok := value.(string); ok {
			return str
		}
	}
	return ""
}

// getIntFromContext safely extracts an int value from context
func getIntFromContext(ctx context.Context, key any) int {
	if value := ctx.Value(key); value != nil {
		if intVal, ok := value.(int); ok {
			return intVal
		}
	}
	return 0
}
