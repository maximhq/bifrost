// Package logging provides utility functions and interfaces for the GORM-based logging plugin
package logging

import (
	"strings"
)

// LogManager defines the main interface that combines all logging functionality
type LogManager interface {
	// Search searches for log entries based on filters and pagination
	Search(filters *SearchFilters, pagination *PaginationOptions) (*SearchResult, error)

	// Get the number of dropped requests
	GetDroppedRequests() int64
}

// PluginLogManager implements LogManager interface wrapping the plugin
type PluginLogManager struct {
	plugin *LoggerPlugin
}

func (p *PluginLogManager) Search(filters *SearchFilters, pagination *PaginationOptions) (*SearchResult, error) {
	return p.plugin.SearchLogs(*filters, *pagination)
}

func (p *PluginLogManager) GetDroppedRequests() int64 {
	return p.plugin.droppedRequests.Load()
}

// GetPluginLogManager returns a LogManager interface for this plugin
func (p *LoggerPlugin) GetPluginLogManager() *PluginLogManager {
	return &PluginLogManager{
		plugin: p,
	}
}

// createContentSummary creates a searchable content summary from a complete log entry
func (p *LoggerPlugin) createContentSummary(entry *LogEntry) string {
	var parts []string

	// Add input history content
	for _, msg := range entry.InputHistoryParsed {
		if msg.Content.ContentStr != nil {
			parts = append(parts, *msg.Content.ContentStr)
		}
		// If content blocks exist, extract text from them
		if msg.Content.ContentBlocks != nil {
			for _, block := range *msg.Content.ContentBlocks {
				if block.Text != nil && *block.Text != "" {
					parts = append(parts, *block.Text)
				}
			}
		}
	}

	// Add output message content
	if entry.OutputMessageParsed != nil {
		if entry.OutputMessageParsed.Content.ContentStr != nil {
			parts = append(parts, *entry.OutputMessageParsed.Content.ContentStr)
		}
		// If content blocks exist, extract text from them
		if entry.OutputMessageParsed.Content.ContentBlocks != nil {
			for _, block := range *entry.OutputMessageParsed.Content.ContentBlocks {
				if block.Text != nil && *block.Text != "" {
					parts = append(parts, *block.Text)
				}
			}
		}
	}

	// Add tool calls content
	if entry.ToolCallsParsed != nil {
		for _, toolCall := range *entry.ToolCallsParsed {
			if toolCall.Function.Arguments != "" {
				parts = append(parts, toolCall.Function.Arguments)
			}
		}
	}

	// Add error details
	if entry.ErrorDetailsParsed != nil {
		parts = append(parts, entry.ErrorDetailsParsed.Error.Message)
	}

	return strings.Join(parts, " ")
}
