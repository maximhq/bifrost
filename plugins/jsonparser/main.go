package jsonparser

import (
	"context"
	"encoding/json"
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

type ContextKey string

const (
	EnableStreamingJSONParser ContextKey = "enable-streaming-json-parser"
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

// TransportInterceptor is not used for this plugin
func (p *JsonParserPlugin) TransportInterceptor(url string, headers map[string]string, body map[string]any) (map[string]string, map[string]any, error) {
	return headers, body, nil
}

// PreHook is not used for this plugin as we only process responses
func (p *JsonParserPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	return req, nil, nil
}

// PostHook processes streaming responses by accumulating chunks and making accumulated content valid JSON
func (p *JsonParserPlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	// If there's an error, don't process
	if err != nil {
		return result, err, nil
	}

	// Check if plugin should run based on usage type
	if !p.shouldRun(ctx, result.ExtraFields.RequestType) {
		return result, err, nil
	}

	// If no result, return as is
	if result == nil {
		return result, err, nil
	}

	// Get request ID for state management, if it's not set, return as is
	requestID := p.getRequestID(ctx, result)
	if requestID == "" {
		return result, err, nil
	}

	// Process only streaming choices to accumulate and fix partial JSON
	if len(result.Choices) > 0 {
		for i := range result.Choices {
			choice := &result.Choices[i]

			// Handle only streaming response
			if choice.BifrostStreamResponseChoice != nil {
				if choice.BifrostStreamResponseChoice.Delta.Content != nil {
					content := *choice.BifrostStreamResponseChoice.Delta.Content
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
						choice.BifrostStreamResponseChoice.Delta.Content = &fixedContent
					}
				}
			}
		}
	}

	// If this is the final chunk, cleanup the accumulated content for this request
	if streamEndIndicatorValue := (*ctx).Value(schemas.BifrostContextKeyStreamEndIndicator); streamEndIndicatorValue != nil {
		isFinalChunk, ok := streamEndIndicatorValue.(bool)
		if ok && isFinalChunk {
			p.ClearRequestState(requestID)
		}
	}

	return result, err, nil
}

// getRequestID extracts a unique identifier for the request to maintain state
func (p *JsonParserPlugin) getRequestID(ctx *context.Context, result *schemas.BifrostResponse) string {

	// Try to get from result
	if result != nil && result.ID != "" {
		return result.ID
	}

	// Try to get from context if not available in result
	if ctx != nil {
		if requestID, ok := (*ctx).Value(schemas.BifrostContextKeyRequestID).(string); ok && requestID != "" {
			return requestID
		}
	}

	return ""
}

// accumulateContent adds new content to the accumulated content for a specific request
func (p *JsonParserPlugin) accumulateContent(requestID, newContent string) string {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Get existing accumulated content
	existing := p.accumulatedContent[requestID]

	if existing != nil {
		// Append to existing builder
		existing.Content.WriteString(newContent)
		return existing.Content.String()
	} else {
		// Create new builder
		builder := &strings.Builder{}
		builder.WriteString(newContent)
		p.accumulatedContent[requestID] = &AccumulatedContent{
			Content:   builder,
			Timestamp: time.Now(),
		}
		return builder.String()
	}
}

// shouldRun determines if the plugin should process the request based on usage type
func (p *JsonParserPlugin) shouldRun(ctx *context.Context, requestType schemas.RequestType) bool {
	// Run only for chat completion stream requests
	if requestType != schemas.ChatCompletionStreamRequest {
		return false
	}

	switch p.usage {
	case AllRequests:
		return true
	case PerRequest:
		// Check if the context contains the plugin-specific key
		if ctx != nil {
			if value, ok := (*ctx).Value(EnableStreamingJSONParser).(bool); ok {
				return value
			}
		}
		return false
	default:
		return false
	}
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

// parsePartialJSON parses a JSON string that may be missing closing braces
func (p *JsonParserPlugin) parsePartialJSON(s string) string {
	// Trim whitespace
	s = strings.TrimSpace(s)
	if s == "" {
		return "{}"
	}

	// Quick check: if it starts with { or [, it might be JSON
	if s[0] != '{' && s[0] != '[' {
		return s
	}

	// First, try to parse the string as-is (fast path)
	if p.isValidJSON(s) {
		return s
	}

	// Use a more efficient approach: build the completion directly
	return p.completeJSON(s)
}

// isValidJSON checks if a string is valid JSON
func (p *JsonParserPlugin) isValidJSON(s string) bool {
	// Trim whitespace
	s = strings.TrimSpace(s)

	// Empty string after trimming is not valid JSON
	if s == "" {
		return false
	}

	return json.Valid([]byte(s))
}

// completeJSON completes partial JSON with O(n) time complexity
func (p *JsonParserPlugin) completeJSON(s string) string {
	// Pre-allocate buffer with estimated capacity
	capacity := len(s) + 10 // Estimate max 10 closing characters needed
	result := make([]byte, 0, capacity)

	var stack []byte
	inString := false
	escaped := false

	// Process the string once
	for i := 0; i < len(s); i++ {
		char := s[i]
		result = append(result, char)

		if escaped {
			escaped = false
			continue
		}

		if char == '\\' {
			escaped = true
			continue
		}

		if char == '"' {
			inString = !inString
			continue
		}

		if inString {
			continue
		}

		switch char {
		case '{', '[':
			if char == '{' {
				stack = append(stack, '}')
			} else {
				stack = append(stack, ']')
			}
		case '}', ']':
			if len(stack) > 0 && stack[len(stack)-1] == char {
				stack = stack[:len(stack)-1]
			}
		}
	}

	// Close any unclosed strings
	if inString {
		if escaped {
			// Remove the trailing backslash
			if len(result) > 0 {
				result = result[:len(result)-1]
			}
		}
		result = append(result, '"')
	}

	// Add closing characters in reverse order
	for i := len(stack) - 1; i >= 0; i-- {
		result = append(result, stack[i])
	}

	// Validate the result
	if p.isValidJSON(string(result)) {
		return string(result)
	}

	// If still invalid, try progressive truncation (but more efficiently)
	return p.progressiveTruncation(s, result)
}

// progressiveTruncation efficiently tries different truncation points
func (p *JsonParserPlugin) progressiveTruncation(original string, completed []byte) string {
	// Try removing characters from the end until we get valid JSON
	// Use binary search for better performance
	left, right := 0, len(completed)

	for left < right {
		mid := (left + right) / 2
		candidate := completed[:mid]

		if p.isValidJSON(string(candidate)) {
			left = mid + 1
		} else {
			right = mid
		}
	}

	// Try the best candidate
	if left > 0 && p.isValidJSON(string(completed[:left-1])) {
		return string(completed[:left-1])
	}

	// Fallback to original
	return original
}

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
