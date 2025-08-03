// Package logging provides utility functions and interfaces for the GORM-based logging plugin
package logging

import "fmt"

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
	if filters == nil || pagination == nil {
		return nil, fmt.Errorf("filters and pagination cannot be nil")
	}
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
