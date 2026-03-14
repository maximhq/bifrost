// Package logging provides utility functions and interfaces for the trace/span-based logging plugin
package logging

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/framework/logstore"
)

// KeyPair represents an ID-Name pair for keys
type KeyPair struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// LogManager defines the main interface that combines all logging functionality
type LogManager interface {
	// SearchTraces searches for trace entries (root spans) based on filters and pagination
	SearchTraces(ctx context.Context, filters *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.TraceSearchResult, error)

	// GetTrace retrieves a single trace (root span + children) by ID
	GetTrace(ctx context.Context, id string) (*logstore.SpanLog, []*logstore.SpanLog, error)

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

	// GetDroppedRequests returns the number of dropped requests
	GetDroppedRequests(ctx context.Context) int64

	// GetAvailableModels returns all unique models from logs
	GetAvailableModels(ctx context.Context) []string

	// GetAvailableSelectedKeys returns all unique selected key ID-Name pairs from logs
	GetAvailableSelectedKeys(ctx context.Context) []KeyPair

	// GetAvailableVirtualKeys returns all unique virtual key ID-Name pairs from logs
	GetAvailableVirtualKeys(ctx context.Context) []KeyPair

	// GetAvailableRoutingRules returns all unique routing rule ID-Name pairs from logs
	GetAvailableRoutingRules(ctx context.Context) []KeyPair

	// GetAvailableRoutingEngines returns all unique routing engine types from logs
	GetAvailableRoutingEngines(ctx context.Context) []string

	// DeleteTraces deletes traces (root spans and their children) by their IDs
	DeleteTraces(ctx context.Context, ids []string) error

	// RecalculateCosts recomputes missing costs for traces matching the filters
	RecalculateCosts(ctx context.Context, filters *logstore.SearchFilters, limit int) (*RecalculateCostResult, error)
}

// PluginLogManager implements LogManager interface wrapping the plugin
type PluginLogManager struct {
	plugin *LoggerPlugin
}

func (p *PluginLogManager) SearchTraces(ctx context.Context, filters *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.TraceSearchResult, error) {
	if filters == nil || pagination == nil {
		return nil, fmt.Errorf("filters and pagination cannot be nil")
	}
	return p.plugin.SearchTraces(ctx, *filters, *pagination)
}

func (p *PluginLogManager) GetTrace(ctx context.Context, id string) (*logstore.SpanLog, []*logstore.SpanLog, error) {
	return p.plugin.GetTrace(ctx, id)
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

func (p *PluginLogManager) GetDroppedRequests(ctx context.Context) int64 {
	return p.plugin.droppedRequests.Load()
}

func (p *PluginLogManager) GetAvailableModels(ctx context.Context) []string {
	return p.plugin.GetAvailableModels(ctx)
}

func (p *PluginLogManager) GetAvailableSelectedKeys(ctx context.Context) []KeyPair {
	return p.plugin.GetAvailableSelectedKeys(ctx)
}

func (p *PluginLogManager) GetAvailableVirtualKeys(ctx context.Context) []KeyPair {
	return p.plugin.GetAvailableVirtualKeys(ctx)
}

func (p *PluginLogManager) GetAvailableRoutingRules(ctx context.Context) []KeyPair {
	return p.plugin.GetAvailableRoutingRules(ctx)
}

func (p *PluginLogManager) GetAvailableRoutingEngines(ctx context.Context) []string {
	return p.plugin.GetAvailableRoutingEngines(ctx)
}

func (p *PluginLogManager) DeleteTraces(ctx context.Context, ids []string) error {
	if p.plugin == nil || p.plugin.store == nil {
		return fmt.Errorf("log store not initialized")
	}
	return p.plugin.DeleteTraces(ctx, ids)
}

func (p *PluginLogManager) RecalculateCosts(ctx context.Context, filters *logstore.SearchFilters, limit int) (*RecalculateCostResult, error) {
	if filters == nil {
		return nil, fmt.Errorf("filters cannot be nil")
	}
	return p.plugin.RecalculateCosts(ctx, *filters, limit)
}

// GetPluginLogManager returns a LogManager interface for this plugin
func (p *LoggerPlugin) GetPluginLogManager() *PluginLogManager {
	return &PluginLogManager{
		plugin: p,
	}
}
