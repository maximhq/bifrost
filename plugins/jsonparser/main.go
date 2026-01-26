package jsonparser

import (
	"strings"
	"sync"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	PluginName = "streaming-json-parser"
)

type Usage string

const (
	AllRequests Usage = "all_requests"
	PerRequest  Usage = "per_request"
)

// AccumulatedContent holds both the content and timestamp for a request
type AccumulatedContent struct {
	Content   *strings.Builder
	Timestamp time.Time
}

// JsonParserPlugin provides JSON parsing capabilities for streaming responses
// It handles partial JSON chunks by accumulating them and making the accumulated content valid JSON
type JsonParserPlugin struct {
	usage Usage
	// State management for accumulating chunks
	accumulatedContent map[string]*AccumulatedContent // requestID -> accumulated content with timestamp
	mutex              sync.RWMutex
	// Cleanup configuration
	cleanupInterval time.Duration
	maxAge          time.Duration
	stopCleanup     chan struct{}
	stopOnce        sync.Once
}

// PluginConfig holds configuration options for the JSON parser plugin
type PluginConfig struct {
	Usage           Usage
	CleanupInterval time.Duration
	MaxAge          time.Duration
}

const (
	EnableStreamingJSONParser schemas.BifrostContextKey = "enable-streaming-json-parser"
)

// Init creates a new JSON parser plugin instance with custom configuration
func Init(config PluginConfig) (*JsonParserPlugin, error) {
	// Set defaults if not provided
	if config.CleanupInterval <= 0 {
		config.CleanupInterval = 5 * time.Minute
	}
	if config.MaxAge <= 0 {
		config.MaxAge = 30 * time.Minute
	}
	if config.Usage == "" {
		config.Usage = PerRequest
	}

	plugin := &JsonParserPlugin{
		usage:              config.Usage,
		accumulatedContent: make(map[string]*AccumulatedContent),
		cleanupInterval:    config.CleanupInterval,
		maxAge:             config.MaxAge,
		stopCleanup:        make(chan struct{}),
	}

	// Start the cleanup goroutine
	go plugin.startCleanupGoroutine()

	return plugin, nil
}

// GetName returns the plugin name
func (p *JsonParserPlugin) GetName() string {
	return PluginName
}

// HTTPTransportPreHook is not used for this plugin
func (p *JsonParserPlugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return nil, nil
}

// HTTPTransportPostHook is not used for this plugin
func (p *JsonParserPlugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	return nil
}

// HTTPTransportStreamChunkHook passes through streaming chunks unchanged
func (p *JsonParserPlugin) HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

// PreHook is not used for this plugin as we only process responses
// Parameters:
//   - ctx: The Bifrost context
//   - req: The Bifrost request
//
// Returns:
//   - *schemas.BifrostRequest: The processed request
//   - *schemas.PluginShortCircuit: The plugin short circuit if the request is not allowed
//   - error: Any error that occurred during processing
func (p *JsonParserPlugin) PreHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	return req, nil, nil
}

// PostHook processes streaming responses by accumulating chunks and making accumulated content valid JSON
// Parameters:
//   - ctx: The Bifrost context
//   - result: The Bifrost response to be processed
//   - err: The Bifrost error to be processed
//
// Returns:
//   - *schemas.BifrostResponse: The processed response
//   - *schemas.BifrostError: The processed error
//   - error: Any error that occurred during processing
func (p *JsonParserPlugin) PostHook(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	// If there's an error, don't process
	if err != nil {
		return result, err, nil
	}

	extraFields := result.GetExtraFields()

	// Check if plugin should run based on usage type
	if !p.shouldRun(ctx, extraFields.RequestType) {
		return result, err, nil
	}

	// If no chat response, return as is
	if result == nil || result.ChatResponse == nil {
		return result, err, nil
	}

	// Get request ID for state management, if it's not set, return as is
	requestID := p.getRequestID(ctx, result)
	if requestID == "" {
		return result, err, nil
	}

	// Create a deep copy of the result to avoid modifying the original pointer
	// This ensures other plugins using the same pointer don't get corrupted data
	resultCopy := p.deepCopyBifrostResponse(result)
	if resultCopy == nil || resultCopy.ChatResponse == nil {
		return result, err, nil
	}

	// Process only streaming choices to accumulate and fix partial JSON
	if len(resultCopy.ChatResponse.Choices) > 0 {
		for i := range resultCopy.ChatResponse.Choices {
			choice := &resultCopy.ChatResponse.Choices[i]

			// Handle only streaming response
			if choice.ChatStreamResponseChoice != nil {
				if choice.ChatStreamResponseChoice.Delta.Content != nil {
					content := *choice.ChatStreamResponseChoice.Delta.Content
					if content != "" {
						// Accumulate the content
						accumulated := p.accumulateContent(requestID, content)

						// Process the accumulated content to make it valid JSON
						fixedContent := p.parsePartialJSON(accumulated)

						if !p.isValidJSON(fixedContent) {
							err = &schemas.BifrostError{
								Error: &schemas.ErrorField{
									Message: "Invalid JSON in streaming response",
								},
								StreamControl: &schemas.StreamControl{
									SkipStream: bifrost.Ptr(true),
								},
							}

							return nil, err, nil
						}

						// Replace the delta content with the complete valid JSON
						choice.ChatStreamResponseChoice.Delta.Content = &fixedContent
					}
				}
			}
		}
	}

	// If this is the final chunk, cleanup the accumulated content for this request
	if streamEndIndicatorValue := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator); streamEndIndicatorValue != nil {
		isFinalChunk, ok := streamEndIndicatorValue.(bool)
		if ok && isFinalChunk {
			p.ClearRequestState(requestID)
		}
	}

	// Return the modified copy instead of the original
	return resultCopy, err, nil
}

// Cleanup performs plugin cleanup and clears accumulated content
func (p *JsonParserPlugin) Cleanup() error {
	// Stop the cleanup goroutine
	p.StopCleanup()

	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Clear accumulated content
	p.accumulatedContent = make(map[string]*AccumulatedContent)
	return nil
}

// ClearRequestState clears the accumulated content for a specific request
func (p *JsonParserPlugin) ClearRequestState(requestID string) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	delete(p.accumulatedContent, requestID)
}

// CLEANUP METHODS

// startCleanupGoroutine starts a goroutine that periodically cleans up old accumulated content
func (p *JsonParserPlugin) startCleanupGoroutine() {
	ticker := time.NewTicker(p.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.cleanupOldEntries()
		case <-p.stopCleanup:
			return
		}
	}
}

// cleanupOldEntries removes accumulated content entries that are older than maxAge
func (p *JsonParserPlugin) cleanupOldEntries() {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	now := time.Now()
	cutoff := now.Add(-p.maxAge)

	for requestID, content := range p.accumulatedContent {
		if content.Timestamp.Before(cutoff) {
			delete(p.accumulatedContent, requestID)
		}
	}
}

// StopCleanup stops the cleanup goroutine
func (p *JsonParserPlugin) StopCleanup() {
	p.stopOnce.Do(func() {
		close(p.stopCleanup)
	})
}
