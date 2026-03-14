// Package logging provides a GORM-based logging plugin for Bifrost.
// This plugin stores comprehensive logs of all requests and responses with search,
// filter, and pagination capabilities using a trace/span architecture.
package logging

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/mcpcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

const (
	PluginName = "logging"
)

// RecalculateCostResult represents summary stats from a cost backfill operation
type RecalculateCostResult struct {
	TotalMatched int64 `json:"total_matched"`
	Updated      int   `json:"updated"`
	Skipped      int   `json:"skipped"`
	Remaining    int64 `json:"remaining"`
}

// TraceCallback is a function that gets called when a trace is persisted to the spans table
type TraceCallback func(ctx context.Context, root *logstore.SpanLog, children []*logstore.SpanLog)

type Config struct {
	DisableContentLogging *bool     `json:"disable_content_logging"`
	LoggingHeaders        *[]string `json:"logging_headers"` // Pointer to live config slice; changes are reflected immediately without restart
}

// LoggerPlugin implements the schemas.LLMPlugin and schemas.MCPPlugin interfaces
type LoggerPlugin struct {
	ctx                   context.Context
	store                 logstore.LogStore
	disableContentLogging *bool
	loggingHeaders        *[]string // Pointer to live config slice for headers to capture in metadata
	pricingManager        *modelcatalog.ModelCatalog
	mcpCatalog            *mcpcatalog.MCPCatalog
	mu                    sync.Mutex
	done                  chan struct{}
	cleanupOnce           sync.Once
	wg                    sync.WaitGroup
	logger                schemas.Logger
	traceCallback         TraceCallback
	droppedRequests       atomic.Int64
	cleanupTicker         *time.Ticker
	closed                atomic.Bool
}

// Init creates new logger plugin with given log store
func Init(ctx context.Context, config *Config, logger schemas.Logger, logsStore logstore.LogStore, pricingManager *modelcatalog.ModelCatalog, mcpCatalog *mcpcatalog.MCPCatalog) (*LoggerPlugin, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if logsStore == nil {
		return nil, fmt.Errorf("logs store cannot be nil")
	}
	if pricingManager == nil {
		logger.Warn("logging plugin requires model catalog to calculate cost, all LLM cost calculations will be skipped.")
	}
	if mcpCatalog == nil {
		logger.Warn("logging plugin requires MCP catalog to calculate cost, all MCP cost calculations will be skipped.")
	}

	plugin := &LoggerPlugin{
		ctx:                   ctx,
		store:                 logsStore,
		pricingManager:        pricingManager,
		mcpCatalog:            mcpCatalog,
		disableContentLogging: config.DisableContentLogging,
		loggingHeaders:        config.LoggingHeaders,
		done:                  make(chan struct{}),
		logger:                logger,
	}

	// Start cleanup ticker (runs every 1 minute)
	plugin.cleanupTicker = time.NewTicker(1 * time.Minute)
	plugin.wg.Add(1)
	go plugin.cleanupWorker()

	return plugin, nil
}

// cleanupWorker periodically removes old processing logs
func (p *LoggerPlugin) cleanupWorker() {
	defer p.wg.Done()
	for {
		select {
		case <-p.cleanupTicker.C:
			p.cleanupOldProcessingLogs()
		case <-p.done:
			return
		}
	}
}

// cleanupOldProcessingLogs removes stale processing spans older than 30 minutes
func (p *LoggerPlugin) cleanupOldProcessingLogs() {
	thirtyMinutesAgo := time.Now().UTC().Add(-30 * time.Minute)
	// Flush stale processing spans
	if err := p.store.Flush(p.ctx, thirtyMinutesAgo); err != nil {
		p.logger.Warn("failed to cleanup old processing logs: %v", err)
	}
}

// GetName returns the name of the plugin
func (p *LoggerPlugin) GetName() string {
	return PluginName
}

// HTTPTransportPreHook is not used for this plugin
func (p *LoggerPlugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return nil, nil
}

// HTTPTransportPostHook is not used for this plugin
func (p *LoggerPlugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	return nil
}

// HTTPTransportStreamChunkHook passes through streaming chunks unchanged
func (p *LoggerPlugin) HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

// PreLLMHook is called before a request is processed.
// No-op — data collection happens via the tracer, persistence via Inject().
func (p *LoggerPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}

// PostLLMHook is called after a response is received.
// No-op pass-through — persistence happens via Inject().
func (p *LoggerPlugin) PostLLMHook(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return result, bifrostErr, nil
}

// PreMCPHook is called before an MCP tool execution.
// No-op — MCP data comes through the tracer as MCP spans now.
func (p *LoggerPlugin) PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	return req, nil, nil
}

// PostMCPHook is called after an MCP tool execution.
// No-op — MCP data comes through the tracer as MCP spans now.
func (p *LoggerPlugin) PostMCPHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

// Cleanup is called when the plugin is being shut down
func (p *LoggerPlugin) Cleanup() error {
	p.cleanupOnce.Do(func() {
		// Stop the cleanup ticker
		if p.cleanupTicker != nil {
			p.cleanupTicker.Stop()
		}
		// Signal the cleanup worker to stop
		close(p.done)
		p.closed.Store(true)
		// Wait for the cleanup worker to finish
		p.wg.Wait()
	})
	return nil
}

// ObservabilityPlugin Interface Implementation

// Inject receives a completed trace from the tracer and persists it to the spans table.
// This is called asynchronously after the response has been written to the client,
// so it adds zero latency to the hot path.
func (p *LoggerPlugin) Inject(ctx context.Context, trace *schemas.Trace) error {
	if trace == nil {
		return nil
	}

	rootSpan, childSpans := p.convertTraceToSpanLogs(trace)
	if rootSpan == nil {
		return nil
	}

	// Enqueue for batch write to spans table
	p.enqueueTraceEntry(rootSpan, childSpans)

	return nil
}

// enqueueTraceEntry persists a root span and its children to the spans table.
func (p *LoggerPlugin) enqueueTraceEntry(root *logstore.SpanLog, children []*logstore.SpanLog) {
	if p.closed.Load() {
		return
	}

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				p.logger.Warn("panic in trace write (recovered): %v", r)
			}
		}()

		if err := p.store.CreateRootSpanWithChildren(p.ctx, root, children); err != nil {
			p.logger.Warn("failed to write trace %s: %v", root.ID, err)
			return
		}

		// Fire trace callback if set
		p.mu.Lock()
		cb := p.traceCallback
		p.mu.Unlock()
		if cb != nil {
			cb(p.ctx, root, children)
		}
	}()
}

// SetTraceCallback sets a callback function that will be called when a trace is persisted
func (p *LoggerPlugin) SetTraceCallback(callback TraceCallback) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.traceCallback = callback
}
