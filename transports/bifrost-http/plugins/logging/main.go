// Package logging provides a BadgerDB-based logging plugin for Bifrost.
// This plugin stores comprehensive logs of all requests and responses with search,
// filter, and pagination capabilities.
package logging

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	PluginName = "bifrost-http-logging"

	// Key prefixes for different data types
	LogPrefix   = "log:"
	IndexPrefix = "idx:"
	StatsPrefix = "stats:"

	// Index types
	ProviderIndex  = "provider:"
	ModelIndex     = "model:"
	TimestampIndex = "timestamp:"
	StatusIndex    = "status:"
	LatencyIndex   = "latency:"
	TokenIndex     = "token:"
)

// ContextKey is a custom type for context keys to prevent collisions
type ContextKey string

const (
	RequestProviderKey  ContextKey = "bifrost-http-logging-provider"
	RequestModelKey     ContextKey = "bifrost-http-logging-model"
	RequestObjectKey    ContextKey = "bifrost-http-logging-object"
	RequestStartTimeKey ContextKey = "bifrost-http-logging-start-time"
	RequestChatHistory  ContextKey = "bifrost-http-logging-chat-history"
)

// LogEntry represents a complete log entry for a request/response cycle
type LogEntry struct {
	ID            string                   `json:"id"`
	Timestamp     time.Time                `json:"timestamp"`
	Object        string                   `json:"object"` // text.completion, chat.completion, or embedding
	Provider      string                   `json:"provider"`
	Model         string                   `json:"model"`
	InputHistory  []schemas.BifrostMessage `json:"input_history,omitempty"`
	OutputMessage *schemas.BifrostMessage  `json:"output_message,omitempty"`
	Params        *schemas.ModelParameters `json:"params,omitempty"`
	Tools         *[]schemas.Tool          `json:"tools,omitempty"`
	ToolCalls     *[]schemas.ToolCall      `json:"tool_calls,omitempty"`
	Latency       *float64                 `json:"latency,omitempty"`
	TokenUsage    *schemas.LLMUsage        `json:"token_usage,omitempty"`
	Status        string                   `json:"status"` // "success" or "error"
	ErrorDetails  *schemas.BifrostError    `json:"error_details,omitempty"`
	ExtraFields   map[string]interface{}   `json:"extra_fields,omitempty"`
}

// SearchFilters represents the available filters for log searches
type SearchFilters struct {
	Providers     []string   `json:"providers,omitempty"`
	Models        []string   `json:"models,omitempty"`
	Status        []string   `json:"status,omitempty"`
	Objects       []string   `json:"objects,omitempty"` // For filtering by request type (chat.completion, text.completion, embedding)
	StartTime     *time.Time `json:"start_time,omitempty"`
	EndTime       *time.Time `json:"end_time,omitempty"`
	MinLatency    *float64   `json:"min_latency,omitempty"`
	MaxLatency    *float64   `json:"max_latency,omitempty"`
	MinTokens     *int       `json:"min_tokens,omitempty"`
	MaxTokens     *int       `json:"max_tokens,omitempty"`
	ContentSearch string     `json:"content_search,omitempty"`
}

// PaginationOptions represents pagination parameters
type PaginationOptions struct {
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
	SortBy string `json:"sort_by"` // "timestamp", "latency", "tokens"
	Order  string `json:"order"`   // "asc", "desc"
}

// SearchResult represents the result of a log search
type SearchResult struct {
	Logs       []LogEntry        `json:"logs"`
	Pagination PaginationOptions `json:"pagination"`
	Stats      struct {
		TotalRequests  int64   `json:"total_requests"`
		SuccessRate    float64 `json:"success_rate"`    // Percentage of successful requests
		AverageLatency float64 `json:"average_latency"` // Average latency in milliseconds
		TotalTokens    int64   `json:"total_tokens"`    // Total tokens used
	} `json:"stats"`
}

// LogStats represents aggregated statistics
type LogStats struct {
	TotalRequests      int64            `json:"total_requests"`
	SuccessfulRequests int64            `json:"successful_requests"`
	FailedRequests     int64            `json:"failed_requests"`
	ProviderStats      map[string]int64 `json:"provider_stats"`
	ModelStats         map[string]int64 `json:"model_stats"`
	AverageLatency     float64          `json:"average_latency"`
	TotalTokens        int64            `json:"total_tokens"`
	LastUpdated        time.Time        `json:"last_updated"`
}

// Config represents the configuration for the logging plugin
type Config struct {
	DatabasePath    string `json:"database_path"`
	IndexingEnabled bool   `json:"indexing_enabled"` // Enable/disable indexing for faster searches
	StatsEnabled    bool   `json:"stats_enabled"`    // Enable/disable stats collection
}

// LogCallback is a function that gets called when a new log entry is created
type LogCallback func(*LogEntry)

// LoggerPlugin implements the schemas.Plugin interface
type LoggerPlugin struct {
	config      *Config
	db          *badger.DB
	mu          sync.RWMutex
	stats       *LogStats
	logQueue    chan *LogEntry
	done        chan struct{}
	wg          sync.WaitGroup
	logCallback LogCallback // Callback for real-time log updates
}

// NewLoggerPlugin creates a new logging plugin
func NewLoggerPlugin(config *Config) (*LoggerPlugin, error) {
	if config == nil {
		config = &Config{
			DatabasePath:    "./badger_logs",
			IndexingEnabled: true,
			StatsEnabled:    true,
		}
	}

	// Open BadgerDB
	opts := badger.DefaultOptions(config.DatabasePath)
	opts.Logger = nil // Disable BadgerDB's own logging to avoid noise

	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open BadgerDB: %w", err)
	}

	plugin := &LoggerPlugin{
		config:   config,
		db:       db,
		logQueue: make(chan *LogEntry, 1000), // Buffer for 1000 log entries
		done:     make(chan struct{}),
		stats: &LogStats{
			ProviderStats: make(map[string]int64),
			ModelStats:    make(map[string]int64),
		},
	}

	// Load existing stats if enabled
	if config.StatsEnabled {
		plugin.loadStats()
	}

	// Start background worker
	plugin.wg.Add(1)
	go plugin.backgroundWorker()

	return plugin, nil
}

// SetLogCallback sets the callback function for real-time log updates
func (p *LoggerPlugin) SetLogCallback(callback LogCallback) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.logCallback = callback
}

// backgroundWorker processes log entries asynchronously
func (p *LoggerPlugin) backgroundWorker() {
	defer p.wg.Done()

	for {
		select {
		case entry := <-p.logQueue:
			// Process the log entry
			if err := p.storeLogEntry(entry); err != nil {
				// Log the error but continue processing
				fmt.Printf("BadgerDB Logger: failed to store log entry: %v\n", err)
			}

			// Update stats if enabled
			if p.config.StatsEnabled {
				p.updateStats(entry)
			}

			// Call the callback if set
			p.mu.RLock()
			if p.logCallback != nil {
				p.logCallback(entry)
			}
			p.mu.RUnlock()

		case <-p.done:
			// Drain the remaining queue before exiting
			for {
				select {
				case entry := <-p.logQueue:
					if err := p.storeLogEntry(entry); err != nil {
						fmt.Printf("BadgerDB Logger: failed to store log entry during shutdown: %v\n", err)
					}
					if p.config.StatsEnabled {
						p.updateStats(entry)
					}
					// Call the callback if set
					p.mu.RLock()
					if p.logCallback != nil {
						p.logCallback(entry)
					}
					p.mu.RUnlock()
				default:
					return
				}
			}
		}
	}
}

// GetName returns the name of the plugin
func (p *LoggerPlugin) GetName() string {
	return PluginName
}

// PreHook is called before a request is processed
func (p *LoggerPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	// Generate unique request ID and record start time
	startTime := time.Now()

	// Store request ID and start time in context
	if ctx != nil {
		*ctx = context.WithValue(*ctx, RequestProviderKey, req.Provider)
		*ctx = context.WithValue(*ctx, RequestModelKey, req.Model)
		*ctx = context.WithValue(*ctx, RequestObjectKey, func() string {
			if req.Input.ChatCompletionInput != nil {
				return "chat.completion"
			} else if req.Input.TextCompletionInput != nil {
				return "text.completion"
			} else if req.Input.EmbeddingInput != nil {
				return "embedding"
			}
			return "unknown"
		}())
		*ctx = context.WithValue(*ctx, RequestStartTimeKey, startTime)

		if req.Input.ChatCompletionInput != nil {
			*ctx = context.WithValue(*ctx, RequestChatHistory, *req.Input.ChatCompletionInput)
		} else if req.Input.TextCompletionInput != nil {
			*ctx = context.WithValue(*ctx, RequestChatHistory, []schemas.BifrostMessage{
				{
					Role: schemas.ModelChatMessageRoleUser,
					Content: schemas.MessageContent{
						ContentStr: req.Input.TextCompletionInput,
					},
				},
			})
		}
	}

	return req, nil, nil
}

// PostHook is called after a response is received
func (p *LoggerPlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	// Extract request metadata from context
	var startTime time.Time

	if ctx != nil {
		if st, ok := (*ctx).Value(RequestStartTimeKey).(time.Time); ok {
			startTime = st
		}
	}

	if startTime.IsZero() {
		startTime = time.Now()
	}

	// Calculate latency
	latency := float64(time.Since(startTime).Milliseconds())

	// Create log entry
	logEntry := &LogEntry{
		Timestamp: startTime,
	}

	// Determine status and populate entry
	if err != nil {
		logEntry.Status = "error"
		logEntry.ErrorDetails = err

		if ctx != nil {
			if provider, ok := (*ctx).Value(RequestProviderKey).(schemas.ModelProvider); ok {
				logEntry.Provider = string(provider)
			}
			if model, ok := (*ctx).Value(RequestModelKey).(string); ok {
				logEntry.Model = model
			}
			if chatHistory, ok := (*ctx).Value(RequestChatHistory).([]schemas.BifrostMessage); ok {
				logEntry.InputHistory = chatHistory
			} else {
				logEntry.InputHistory = []schemas.BifrostMessage{}
			}
			if object, ok := (*ctx).Value(RequestObjectKey).(string); ok {
				logEntry.Object = object
			}
		}

	} else {
		logEntry.Status = "success"

		if result != nil {
			logEntry.ID = result.ID
			logEntry.Object = result.Object
			logEntry.Provider = string(result.ExtraFields.Provider)
			logEntry.Model = result.Model
			logEntry.Latency = &latency
			logEntry.TokenUsage = &result.Usage
			logEntry.Params = &result.ExtraFields.Params

			if result.ID == "" {
				logEntry.ID = uuid.New().String()
			}

			// Extract output message
			if len(result.Choices) > 0 {
				logEntry.OutputMessage = &result.Choices[0].Message

				// Extract tool calls if present
				if result.Choices[0].Message.AssistantMessage != nil &&
					result.Choices[0].Message.AssistantMessage.ToolCalls != nil {
					logEntry.ToolCalls = result.Choices[0].Message.AssistantMessage.ToolCalls
				}
			}

			// Extract chat history if available
			if result.ExtraFields.ChatHistory != nil {
				logEntry.InputHistory = *result.ExtraFields.ChatHistory
			} else {
				if chatHistory, ok := (*ctx).Value(RequestChatHistory).([]schemas.BifrostMessage); ok {
					logEntry.InputHistory = chatHistory
				} else {
					logEntry.InputHistory = []schemas.BifrostMessage{}
				}
			}

			// Extract tools from params
			if result.ExtraFields.Params.Tools != nil {
				logEntry.Tools = result.ExtraFields.Params.Tools
			}
		}
	}

	// Queue the log entry for async processing (non-blocking)
	select {
	case p.logQueue <- logEntry:
		// Successfully queued
	default:
		// Queue is full, log warning but don't block the request
		fmt.Printf("Logger: log queue is full, dropping log entry\n")
	}

	return result, err, nil
}

// Cleanup is called when the plugin is being shut down
func (p *LoggerPlugin) Cleanup() error {
	// Signal the background worker to stop
	close(p.done)

	// Wait for the background worker to finish processing remaining items
	p.wg.Wait()

	// Close the database
	if p.db != nil {
		return p.db.Close()
	}
	return nil
}
