package mocker

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	PluginName = "bifrost-mocker"
)

// Constants for type checking and validation
const (
	// Response types
	ResponseTypeSuccess = "success"
	ResponseTypeError   = "error"

	// Default behaviors
	DefaultBehaviorPassthrough = "passthrough"
	DefaultBehaviorError       = "error"
	DefaultBehaviorSuccess     = "success"

	// Latency types
	LatencyTypeFixed   = "fixed"
	LatencyTypeUniform = "uniform"
)

// compiledRule represents a rule with pre-compiled regex for performance
type compiledRule struct {
	MockRule
	compiledRegex *regexp.Regexp // Pre-compiled regex for fast matching
}

// MockerPlugin provides comprehensive request/response mocking capabilities
type MockerPlugin struct {
	config        MockerConfig
	rules         []MockRule
	compiledRules []compiledRule // Pre-compiled rules for performance
	mu            sync.RWMutex
	logger        schemas.Logger // Injected logger instance

	// Atomic counters for high-performance statistics tracking
	totalRequests      int64
	mockedRequests     int64
	responsesGenerated int64
	errorsGenerated    int64

	// Rule hits tracking (still needs mutex for map access)
	ruleHitsMu sync.RWMutex
	ruleHits   map[string]int64
}

// MockerConfig defines the overall configuration for the mocker plugin
type MockerConfig struct {
	Enabled         bool       `json:"enabled"`          // Enable/disable the mocker plugin
	GlobalLatency   *Latency   `json:"global_latency"`   // Global latency settings applied to all rules (can be overridden per rule)
	Rules           []MockRule `json:"rules"`            // List of mock rules to be evaluated in priority order
	DefaultBehavior string     `json:"default_behavior"` // Action when no rules match: "passthrough", "error", or "success"
}

// MockRule defines a single mocking rule with conditions and responses
// Rules are evaluated in priority order (higher numbers = higher priority)
type MockRule struct {
	Name        string     `json:"name"`        // Unique rule name for identification and statistics tracking
	Enabled     bool       `json:"enabled"`     // Enable/disable this rule (disabled rules are skipped)
	Priority    int        `json:"priority"`    // Higher priority rules are checked first (higher numbers = higher priority)
	Conditions  Conditions `json:"conditions"`  // Conditions that must match for this rule to apply
	Responses   []Response `json:"responses"`   // Possible responses (selected using weighted random selection)
	Latency     *Latency   `json:"latency"`     // Rule-specific latency override (overrides global latency if set)
	Probability float64    `json:"probability"` // Probability of rule activation (0.0=never, 1.0=always, 0=disabled)
}

// Conditions define when a mock rule should be applied
// All specified conditions must match for the rule to trigger
type Conditions struct {
	Providers    []string   `json:"providers"`     // Match specific providers (e.g., ["openai", "anthropic"])
	Models       []string   `json:"models"`        // Match specific models (e.g., ["gpt-4", "claude-3"])
	MessageRegex *string    `json:"message_regex"` // Regex pattern to match against message content
	RequestSize  *SizeRange `json:"request_size"`  // Request size constraints in bytes
}

// Response defines a mock response configuration
// Either Content (for success) or Error (for error) should be set, not both
type Response struct {
	Type           string           `json:"type"`            // Response type: "success" or "error"
	Weight         float64          `json:"weight"`          // Weight for random selection (higher = more likely)
	Content        *SuccessResponse `json:"content"`         // Success response content (required if Type="success")
	Error          *ErrorResponse   `json:"error"`           // Error response content (required if Type="error")
	AllowFallbacks *bool            `json:"allow_fallbacks"` // Control fallback behavior for errors (nil=true, false=no fallbacks)
}

// SuccessResponse defines mock success response content
// Either Message or MessageTemplate should be set (MessageTemplate takes precedence)
type SuccessResponse struct {
	Message         string                 `json:"message"`          // Static response message
	Model           *string                `json:"model"`            // Override model name in response (optional)
	Usage           *Usage                 `json:"usage"`            // Token usage info (optional, defaults applied if nil)
	FinishReason    *string                `json:"finish_reason"`    // Completion reason (optional, defaults to "stop")
	MessageTemplate *string                `json:"message_template"` // Template with variables like {{model}}, {{provider}} (overrides Message)
	CustomFields    map[string]interface{} `json:"custom_fields"`    // Additional fields stored in response metadata
}

// ErrorResponse defines mock error response content
type ErrorResponse struct {
	Message    string  `json:"message"`     // Error message to return
	Type       *string `json:"type"`        // Error type (e.g., "rate_limit", "auth_error")
	Code       *string `json:"code"`        // Error code (e.g., "429", "401")
	StatusCode *int    `json:"status_code"` // HTTP status code for the error
}

// Latency defines latency simulation settings
type Latency struct {
	Min  time.Duration `json:"min"`  // Minimum latency as time.Duration (e.g., 100*time.Millisecond, NOT raw int)
	Max  time.Duration `json:"max"`  // Maximum latency as time.Duration (e.g., 500*time.Millisecond, NOT raw int)
	Type string        `json:"type"` // Latency type: "fixed" or "uniform"
}

// SizeRange defines request size constraints in bytes
type SizeRange struct {
	Min int `json:"min"` // Minimum request size in bytes
	Max int `json:"max"` // Maximum request size in bytes
}

// Usage defines token usage information
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// MockStats tracks plugin statistics and rule execution counts
type MockStats struct {
	TotalRequests      int64            `json:"total_requests"`      // Total number of requests processed
	MockedRequests     int64            `json:"mocked_requests"`     // Number of requests that were mocked (rules matched)
	RuleHits           map[string]int64 `json:"rule_hits"`           // Rule name -> hit count mapping
	ErrorsGenerated    int64            `json:"errors_generated"`    // Number of error responses generated
	ResponsesGenerated int64            `json:"responses_generated"` // Number of success responses generated
}

// NewMockerPlugin creates a new mocker plugin instance with sensible defaults
// Returns an error if required configuration is invalid or missing
func NewMockerPlugin(config MockerConfig) (*MockerPlugin, error) {
	// Validate configuration
	if err := validateConfig(config); err != nil {
		return nil, fmt.Errorf("invalid mocker plugin configuration: %w", err)
	}

	// Apply defaults if not set
	if config.DefaultBehavior == "" {
		config.DefaultBehavior = DefaultBehaviorPassthrough // Default to passthrough if no rules match
	}

	// If no rules provided, create a simple catch-all rule for quick testing
	if len(config.Rules) == 0 && config.Enabled {
		config.Rules = []MockRule{
			{
				Name:        "default-mock",
				Enabled:     true,
				Priority:    1,
				Conditions:  Conditions{}, // Empty conditions = match all requests
				Probability: 1.0,          // Always activate
				Responses: []Response{
					{
						Type:   ResponseTypeSuccess,
						Weight: 1.0,
						Content: &SuccessResponse{
							Message: "This is a mock response from the Mocker plugin",
						},
					},
				},
			},
		}
	}

	plugin := &MockerPlugin{
		config:   config,
		rules:    config.Rules,
		ruleHits: make(map[string]int64),
	}

	// Pre-compile all regex patterns for performance
	if err := plugin.compileRules(); err != nil {
		return nil, fmt.Errorf("failed to compile rules: %w", err)
	}

	return plugin, nil
}

// compileRules pre-compiles all regex patterns in rules for performance
func (p *MockerPlugin) compileRules() error {
	p.compiledRules = make([]compiledRule, 0, len(p.rules))

	for _, rule := range p.rules {
		compiled := compiledRule{MockRule: rule}

		// Pre-compile regex if present
		if rule.Conditions.MessageRegex != nil {
			regex, err := regexp.Compile(*rule.Conditions.MessageRegex)
			if err != nil {
				return fmt.Errorf("invalid regex in rule '%s': %w", rule.Name, err)
			}
			compiled.compiledRegex = regex
		}

		p.compiledRules = append(p.compiledRules, compiled)
	}

	// Sort compiled rules by priority (higher first)
	p.sortCompiledRulesByPriority()

	return nil
}

// validateConfig validates the mocker plugin configuration
func validateConfig(config MockerConfig) error {
	// Validate default behavior
	if config.DefaultBehavior != "" {
		switch config.DefaultBehavior {
		case DefaultBehaviorPassthrough, DefaultBehaviorError, DefaultBehaviorSuccess:
			// Valid
		default:
			return fmt.Errorf("invalid default_behavior '%s', must be one of: %s, %s, %s",
				config.DefaultBehavior, DefaultBehaviorPassthrough, DefaultBehaviorError, DefaultBehaviorSuccess)
		}
	}

	// Validate global latency if provided
	if config.GlobalLatency != nil {
		if err := validateLatency(*config.GlobalLatency); err != nil {
			return fmt.Errorf("invalid global_latency: %w", err)
		}
	}

	// Validate each rule
	for i, rule := range config.Rules {
		if err := validateRule(rule); err != nil {
			return fmt.Errorf("invalid rule at index %d (%s): %w", i, rule.Name, err)
		}
	}

	return nil
}

// validateRule validates a single mock rule
func validateRule(rule MockRule) error {
	// Rule name is required
	if rule.Name == "" {
		return fmt.Errorf("rule name is required")
	}

	// Priority should be reasonable (allow negative for low priority)
	if rule.Priority < -1000 || rule.Priority > 1000 {
		return fmt.Errorf("priority %d is out of reasonable range (-1000 to 1000)", rule.Priority)
	}

	// Probability must be between 0 and 1
	if rule.Probability < 0 || rule.Probability > 1 {
		return fmt.Errorf("probability %.2f must be between 0.0 and 1.0", rule.Probability)
	}

	// At least one response is required
	if len(rule.Responses) == 0 {
		return fmt.Errorf("at least one response is required")
	}

	// Validate rule-specific latency if provided
	if rule.Latency != nil {
		if err := validateLatency(*rule.Latency); err != nil {
			return fmt.Errorf("invalid rule latency: %w", err)
		}
	}

	// Validate conditions
	if err := validateConditions(rule.Conditions); err != nil {
		return fmt.Errorf("invalid conditions: %w", err)
	}

	// Validate each response
	for i, response := range rule.Responses {
		if err := validateResponse(response); err != nil {
			return fmt.Errorf("invalid response at index %d: %w", i, err)
		}
	}

	return nil
}

// validateLatency validates latency configuration
func validateLatency(latency Latency) error {
	// Type is required
	if latency.Type == "" {
		return fmt.Errorf("latency type is required")
	}

	// Validate type
	switch latency.Type {
	case LatencyTypeFixed, LatencyTypeUniform:
		// Valid
	default:
		return fmt.Errorf("invalid latency type '%s', must be one of: %s, %s",
			latency.Type, LatencyTypeFixed, LatencyTypeUniform)
	}

	// Min latency should be non-negative
	if latency.Min < 0 {
		return fmt.Errorf("minimum latency cannot be negative")
	}

	// For uniform type, max should be >= min
	if latency.Type == LatencyTypeUniform {
		if latency.Max < latency.Min {
			return fmt.Errorf("maximum latency (%v) cannot be less than minimum latency (%v)", latency.Max, latency.Min)
		}
	}

	return nil
}

// validateConditions validates rule conditions
func validateConditions(conditions Conditions) error {
	// Validate regex if provided
	if conditions.MessageRegex != nil {
		_, err := regexp.Compile(*conditions.MessageRegex)
		if err != nil {
			return fmt.Errorf("invalid message regex '%s': %w", *conditions.MessageRegex, err)
		}
	}

	// Validate request size range if provided
	if conditions.RequestSize != nil {
		if conditions.RequestSize.Min < 0 {
			return fmt.Errorf("request size minimum cannot be negative")
		}
		if conditions.RequestSize.Max < conditions.RequestSize.Min {
			return fmt.Errorf("request size maximum (%d) cannot be less than minimum (%d)",
				conditions.RequestSize.Max, conditions.RequestSize.Min)
		}
	}

	return nil
}

// validateResponse validates a response configuration
func validateResponse(response Response) error {
	// Type is required
	if response.Type == "" {
		return fmt.Errorf("response type is required")
	}

	// Validate type
	switch response.Type {
	case ResponseTypeSuccess, ResponseTypeError:
		// Valid
	default:
		return fmt.Errorf("invalid response type '%s', must be one of: %s, %s",
			response.Type, ResponseTypeSuccess, ResponseTypeError)
	}

	// Weight should be non-negative
	if response.Weight < 0 {
		return fmt.Errorf("response weight cannot be negative")
	}

	// Validate response content based on type
	if response.Type == ResponseTypeSuccess {
		if response.Content == nil {
			return fmt.Errorf("success response must have content")
		}
		if err := validateSuccessResponse(*response.Content); err != nil {
			return fmt.Errorf("invalid success content: %w", err)
		}
	} else if response.Type == ResponseTypeError {
		if response.Error == nil {
			return fmt.Errorf("error response must have error content")
		}
		if err := validateErrorResponse(*response.Error); err != nil {
			return fmt.Errorf("invalid error content: %w", err)
		}
	}

	return nil
}

// validateSuccessResponse validates success response content
func validateSuccessResponse(content SuccessResponse) error {
	// Either Message or MessageTemplate must be provided
	if content.Message == "" && (content.MessageTemplate == nil || *content.MessageTemplate == "") {
		return fmt.Errorf("either message or message_template is required")
	}

	// If usage is provided, validate it
	if content.Usage != nil {
		if content.Usage.PromptTokens < 0 || content.Usage.CompletionTokens < 0 || content.Usage.TotalTokens < 0 {
			return fmt.Errorf("token counts cannot be negative")
		}
	}

	return nil
}

// validateErrorResponse validates error response content
func validateErrorResponse(errorContent ErrorResponse) error {
	// Message is required
	if errorContent.Message == "" {
		return fmt.Errorf("error message is required")
	}

	// Status code should be reasonable if provided
	if errorContent.StatusCode != nil {
		if *errorContent.StatusCode < 100 || *errorContent.StatusCode > 599 {
			return fmt.Errorf("status code %d is out of valid HTTP range (100-599)", *errorContent.StatusCode)
		}
	}

	return nil
}

// GetName returns the plugin name
func (p *MockerPlugin) GetName() string {
	return PluginName
}

// PreHook intercepts requests and applies mocking rules based on configuration
// This is called before the actual provider request and can short-circuit the flow
func (p *MockerPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	// Skip processing if plugin is disabled
	if !p.config.Enabled {
		return req, nil, nil
	}

	// Track total request count using atomic operation (no lock needed)
	atomic.AddInt64(&p.totalRequests, 1)

	// Find the first matching rule based on priority order
	rule := p.findMatchingCompiledRule(req)
	if rule == nil {
		// No rules matched, handle according to default behavior
		return p.handleDefaultBehavior(req)
	}

	// Check if rule should activate based on probability (0.0 = never, 1.0 = always)
	if rule.Probability > 0 && rand.Float64() > rule.Probability {
		// Rule didn't activate due to probability, continue with normal flow
		return req, nil, nil
	}

	// Apply artificial latency simulation if configured
	if latency := p.getLatency(&rule.MockRule); latency != nil {
		delay := p.calculateLatency(latency)
		time.Sleep(delay)
	}

	// Select a response from the rule's possible responses (weighted random)
	response := p.selectResponse(rule.Responses)
	if response == nil {
		// No valid response configuration, continue with normal flow
		return req, nil, nil
	}

	// Update statistics using atomic operations and minimal locking
	atomic.AddInt64(&p.mockedRequests, 1)

	// Rule hits still need a mutex since it's a map, but we minimize lock time
	p.ruleHitsMu.Lock()
	p.ruleHits[rule.Name]++
	p.ruleHitsMu.Unlock()

	// Generate appropriate mock response based on type
	if response.Type == ResponseTypeSuccess {
		return p.generateSuccessShortCircuit(req, response)
	} else if response.Type == ResponseTypeError {
		return p.generateErrorShortCircuit(req, response)
	}

	// Fallback: continue with normal flow if response type is unrecognized
	return req, nil, nil
}

// PostHook processes responses after provider calls
func (p *MockerPlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return result, err, nil
}

// Cleanup performs plugin cleanup and frees memory
// IMPORTANT: Call GetStats() before Cleanup() if you need the statistics,
// as this method clears all statistics data to free memory
func (p *MockerPlugin) Cleanup() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Clear all statistics to free memory using atomic operations
	atomic.StoreInt64(&p.totalRequests, 0)
	atomic.StoreInt64(&p.mockedRequests, 0)
	atomic.StoreInt64(&p.responsesGenerated, 0)
	atomic.StoreInt64(&p.errorsGenerated, 0)

	// Clear rule hits map
	p.ruleHitsMu.Lock()
	p.ruleHits = make(map[string]int64)
	p.ruleHitsMu.Unlock()

	// Clear rules to free memory
	p.rules = nil
	p.compiledRules = nil

	return nil
}

// findMatchingCompiledRule finds the first rule that matches the request using pre-compiled rules
func (p *MockerPlugin) findMatchingCompiledRule(req *schemas.BifrostRequest) *compiledRule {
	for i := range p.compiledRules {
		rule := &p.compiledRules[i]
		if !rule.Enabled {
			continue
		}

		if p.matchesConditionsFast(req, &rule.Conditions, rule.compiledRegex) {
			return rule
		}
	}
	return nil
}

// matchesConditionsFast checks if request matches rule conditions with optimized performance
func (p *MockerPlugin) matchesConditionsFast(req *schemas.BifrostRequest, conditions *Conditions, compiledRegex *regexp.Regexp) bool {
	// Check providers - optimized string comparison
	if len(conditions.Providers) > 0 {
		providerStr := string(req.Provider)
		found := false
		for _, provider := range conditions.Providers {
			if providerStr == provider {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Check models - direct string comparison
	if len(conditions.Models) > 0 {
		found := false
		for _, model := range conditions.Models {
			if req.Model == model {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Check message regex using pre-compiled regex (major performance improvement)
	if compiledRegex != nil {
		// Extract message content from request (cached if possible)
		messageContent := p.extractMessageContentFast(req)
		if !compiledRegex.MatchString(messageContent) {
			return false
		}
	}

	// Check request size - only calculate if needed
	if conditions.RequestSize != nil {
		size := p.calculateRequestSizeFast(req)
		if size < conditions.RequestSize.Min || size > conditions.RequestSize.Max {
			return false
		}
	}

	// All conditions matched
	return true
}

// extractMessageContentFast extracts message content with optimized performance
func (p *MockerPlugin) extractMessageContentFast(req *schemas.BifrostRequest) string {
	// Handle text completion input
	if req.Input.TextCompletionInput != nil {
		return *req.Input.TextCompletionInput
	}

	// Handle chat completion input - optimized for common cases
	if req.Input.ChatCompletionInput != nil {
		messages := *req.Input.ChatCompletionInput
		if len(messages) == 0 {
			return ""
		}

		// Fast path for single message
		if len(messages) == 1 {
			if messages[0].Content.ContentStr != nil {
				return *messages[0].Content.ContentStr
			}
			return ""
		}

		// Multiple messages - use string builder for efficiency
		var builder strings.Builder
		for i, message := range messages {
			if message.Content.ContentStr != nil {
				if i > 0 {
					builder.WriteByte(' ')
				}
				builder.WriteString(*message.Content.ContentStr)
			}
		}
		return builder.String()
	}

	return ""
}

// calculateRequestSizeFast calculates request size with minimal overhead
func (p *MockerPlugin) calculateRequestSizeFast(req *schemas.BifrostRequest) int {
	// Approximate size calculation to avoid expensive JSON marshaling
	size := len(req.Model) + len(string(req.Provider))

	// Add input size
	if req.Input.TextCompletionInput != nil {
		size += len(*req.Input.TextCompletionInput)
	}

	if req.Input.ChatCompletionInput != nil {
		for _, message := range *req.Input.ChatCompletionInput {
			if message.Content.ContentStr != nil {
				size += len(*message.Content.ContentStr)
			}
			size += 50 // Approximate overhead for message structure
		}
	}

	return size
}

// generateSuccessShortCircuit creates a success response short-circuit with optimized allocations
func (p *MockerPlugin) generateSuccessShortCircuit(req *schemas.BifrostRequest, response *Response) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	if response.Content == nil {
		return req, nil, nil
	}

	content := response.Content
	message := content.Message

	// Apply message template if provided
	if content.MessageTemplate != nil {
		message = p.applyTemplateFast(*content.MessageTemplate, req)
	}

	// Apply defaults for token usage if not provided
	var usage schemas.LLMUsage
	if content.Usage != nil {
		usage = schemas.LLMUsage{
			PromptTokens:     p.getOrDefault(content.Usage.PromptTokens, 10),
			CompletionTokens: p.getOrDefault(content.Usage.CompletionTokens, 20),
			TotalTokens:      p.getOrDefault(content.Usage.TotalTokens, content.Usage.PromptTokens+content.Usage.CompletionTokens),
		}
	} else {
		// Default usage when none specified
		usage = schemas.LLMUsage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		}
	}

	// Get finish reason with minimal allocation
	var finishReason *string
	if content.FinishReason != nil {
		finishReason = content.FinishReason
	} else {
		// Use a static string to avoid allocation
		static := "stop"
		finishReason = &static
	}

	// Create mock response with proper structure
	mockResponse := &schemas.BifrostResponse{
		Model: req.Model,
		Usage: usage,
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				Message: schemas.BifrostMessage{
					Role: schemas.ModelChatMessageRoleAssistant,
					Content: schemas.MessageContent{
						ContentStr: &message,
					},
				},
				FinishReason: finishReason,
			},
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: req.Provider,
		},
	}

	// Override model if specified
	if content.Model != nil {
		mockResponse.Model = *content.Model
	}

	// Only create raw response map if there are custom fields (avoid allocation)
	if len(content.CustomFields) > 0 {
		rawResponse := make(map[string]interface{}, len(content.CustomFields)+1)

		// Add custom fields
		for key, value := range content.CustomFields {
			rawResponse[key] = value
		}

		// Add mock metadata
		rawResponse["mock_rule"] = "success"
		mockResponse.ExtraFields.RawResponse = rawResponse
	}

	// Increment success response counter using atomic operation
	atomic.AddInt64(&p.responsesGenerated, 1)

	return req, &schemas.PluginShortCircuit{
		Response: mockResponse,
	}, nil
}

// generateErrorShortCircuit creates an error response short-circuit with optimized performance
func (p *MockerPlugin) generateErrorShortCircuit(req *schemas.BifrostRequest, response *Response) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	if response.Error == nil {
		return req, nil, nil
	}

	errorContent := response.Error
	allowFallbacks := response.AllowFallbacks

	// Create mock error
	mockError := &schemas.BifrostError{
		Error: schemas.ErrorField{
			Message: errorContent.Message,
		},
		AllowFallbacks: allowFallbacks,
	}

	// Set error type
	if errorContent.Type != nil {
		mockError.Error.Type = errorContent.Type
	}

	// Set error code
	if errorContent.Code != nil {
		mockError.Error.Code = errorContent.Code
	}

	// Set status code
	if errorContent.StatusCode != nil {
		mockError.StatusCode = errorContent.StatusCode
	}

	// Increment error counter using atomic operation
	atomic.AddInt64(&p.errorsGenerated, 1)

	return req, &schemas.PluginShortCircuit{
		Error: mockError,
	}, nil
}

// selectResponse selects a response based on weights
func (p *MockerPlugin) selectResponse(responses []Response) *Response {
	if len(responses) == 0 {
		return nil
	}

	if len(responses) == 1 {
		return &responses[0]
	}

	// Calculate total weight, applying default weight of 1.0 if not specified
	totalWeight := 0.0
	for _, response := range responses {
		weight := response.Weight
		if weight == 0 {
			weight = 1.0 // Default weight
		}
		totalWeight += weight
	}

	// Weighted random selection
	randomValue := rand.Float64() * totalWeight
	currentWeight := 0.0

	for _, response := range responses {
		weight := response.Weight
		if weight == 0 {
			weight = 1.0 // Default weight
		}
		currentWeight += weight
		if randomValue <= currentWeight {
			return &response
		}
	}

	// Fallback to last response
	return &responses[len(responses)-1]
}

// getLatency returns the applicable latency configuration
func (p *MockerPlugin) getLatency(rule *MockRule) *Latency {
	if rule.Latency != nil {
		return rule.Latency
	}
	return p.config.GlobalLatency
}

// calculateLatency calculates the actual delay based on latency configuration
func (p *MockerPlugin) calculateLatency(latency *Latency) time.Duration {
	switch latency.Type {
	case LatencyTypeFixed:
		return latency.Min
	case LatencyTypeUniform:
		if latency.Max <= latency.Min {
			return latency.Min
		}
		// Calculate random duration between Min and Max
		diff := latency.Max - latency.Min
		return latency.Min + time.Duration(rand.Float64()*float64(diff))
	default:
		// Default to fixed latency
		return latency.Min
	}
}

// handleDefaultBehavior handles requests when no rules match
func (p *MockerPlugin) handleDefaultBehavior(req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	switch p.config.DefaultBehavior {
	case DefaultBehaviorError:
		return req, &schemas.PluginShortCircuit{
			Error: &schemas.BifrostError{
				Error: schemas.ErrorField{
					Message: "Mock plugin default error",
				},
			},
		}, nil
	case DefaultBehaviorSuccess:
		finishReason := "stop"
		return req, &schemas.PluginShortCircuit{
			Response: &schemas.BifrostResponse{
				Model: req.Model,
				Usage: schemas.LLMUsage{
					PromptTokens:     5,
					CompletionTokens: 10,
					TotalTokens:      15,
				},
				Choices: []schemas.BifrostResponseChoice{
					{
						Index: 0,
						Message: schemas.BifrostMessage{
							Role: schemas.ModelChatMessageRoleAssistant,
							Content: schemas.MessageContent{
								ContentStr: func() *string { s := "Mock plugin default response"; return &s }(),
							},
						},
						FinishReason: &finishReason,
					},
				},
				ExtraFields: schemas.BifrostResponseExtraFields{
					Provider: req.Provider,
				},
			},
		}, nil
	default: // DefaultBehaviorPassthrough
		return req, nil, nil
	}
}

// Helper functions

// sortCompiledRulesByPriority sorts rules by priority (descending)
func (p *MockerPlugin) sortCompiledRulesByPriority() {
	for i := 0; i < len(p.compiledRules)-1; i++ {
		for j := i + 1; j < len(p.compiledRules); j++ {
			if p.compiledRules[i].Priority < p.compiledRules[j].Priority {
				p.compiledRules[i], p.compiledRules[j] = p.compiledRules[j], p.compiledRules[i]
			}
		}
	}
}

// applyTemplateFast applies template variables with optimized string operations
func (p *MockerPlugin) applyTemplateFast(template string, req *schemas.BifrostRequest) string {
	// Fast path: no template variables
	if !strings.Contains(template, "{{") {
		return template
	}

	// Use string replacer for better performance than multiple Replace calls
	replacer := strings.NewReplacer(
		"{{provider}}", string(req.Provider),
		"{{model}}", req.Model,
	)

	return replacer.Replace(template)
}

// getOrDefault returns value or default if 0
func (p *MockerPlugin) getOrDefault(value, defaultValue int) int {
	if value == 0 {
		return defaultValue
	}
	return value
}

// GetStats returns current plugin statistics
// IMPORTANT: Call this method before Cleanup() if you need the statistics,
// as Cleanup() clears all statistics data to free memory
func (p *MockerPlugin) GetStats() MockStats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Create a deep copy using atomic reads for counters
	statsCopy := MockStats{
		TotalRequests:      atomic.LoadInt64(&p.totalRequests),
		MockedRequests:     atomic.LoadInt64(&p.mockedRequests),
		ErrorsGenerated:    atomic.LoadInt64(&p.errorsGenerated),
		ResponsesGenerated: atomic.LoadInt64(&p.responsesGenerated),
		RuleHits:           make(map[string]int64),
	}

	// Copy rule hits map (still needs lock)
	p.ruleHitsMu.RLock()
	maps.Copy(statsCopy.RuleHits, p.ruleHits)
	p.ruleHitsMu.RUnlock()

	return statsCopy
}

// NewPlugin creates a new mocker plugin instance using standardized configuration
// This is the standardized constructor that all plugins should implement
func NewPlugin(configJSON json.RawMessage) (schemas.Plugin, error) {
	var config MockerConfig

	// Parse the JSON configuration
	if len(configJSON) > 0 {
		if err := json.Unmarshal(configJSON, &config); err != nil {
			return nil, fmt.Errorf("failed to parse mocker plugin configuration: %w", err)
		}
	} else {
		// Default configuration if none provided
		config = MockerConfig{
			Enabled:         true,
			DefaultBehavior: DefaultBehaviorPassthrough,
		}
	}

	return NewMockerPlugin(config)
}

// SetLogger injects a logger instance into the plugin
func (p *MockerPlugin) SetLogger(logger schemas.Logger) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.logger = logger
}

// log is a helper method that logs messages using the injected logger or falls back to no-op
func (p *MockerPlugin) log(level string, message string, args ...interface{}) {
	p.mu.RLock()
	logger := p.logger
	p.mu.RUnlock()

	if logger != nil {
		formattedMsg := fmt.Sprintf(message, args...)
		switch level {
		case "debug":
			logger.Debug(formattedMsg)
		case "info":
			logger.Info(formattedMsg)
		case "warn":
			logger.Warn(formattedMsg)
		case "error":
			logger.Error(fmt.Errorf("%s", formattedMsg))
		}
	}
	// If no logger is injected, do nothing (graceful degradation)
}
