package mocker

import (
	"fmt"
	"maps"
	"math/rand"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jaswdr/faker/v2"
	bifrost "github.com/maximhq/bifrost/core"
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

// compiledRule represents a rule with pre-compiled regex and normalized weights for performance
type compiledRule struct {
	MockRule
	compiledRegex     *regexp.Regexp // Pre-compiled regex for fast matching
	normalizedWeights []float64      // Pre-calculated normalized weights for fast response selection
}

// MockerPlugin provides comprehensive request/response mocking capabilities
type MockerPlugin struct {
	config        MockerConfig
	rules         []MockRule
	compiledRules []compiledRule // Pre-compiled rules for performance
	mu            sync.RWMutex
	faker         faker.Faker // Use jaswdr/faker library

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

// Init creates a new mocker plugin instance with sensible defaults
// Returns an error if required configuration is invalid or missing
func Init(config MockerConfig) (*MockerPlugin, error) {
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
		faker:    faker.New(), // Initialize faker
	}

	// Pre-compile all regex patterns for performance
	if err := plugin.compileRules(); err != nil {
		return nil, fmt.Errorf("failed to compile rules: %w", err)
	}

	return plugin, nil
}

// compileRules pre-compiles all regex patterns and calculates normalized weights for performance
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

		// Pre-calculate normalized weights for fast response selection
		compiled.normalizedWeights = p.calculateNormalizedWeights(rule.Responses)

		p.compiledRules = append(p.compiledRules, compiled)
	}

	// Sort compiled rules by priority (higher first)
	p.sortCompiledRulesByPriority()

	return nil
}

// calculateNormalizedWeights pre-calculates normalized cumulative weights for fast response selection
func (p *MockerPlugin) calculateNormalizedWeights(responses []Response) []float64 {
	if len(responses) == 0 {
		return nil
	}

	if len(responses) == 1 {
		return []float64{1.0} // Single response always gets 100% probability
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

	// Calculate normalized cumulative weights for O(1) selection
	normalizedWeights := make([]float64, len(responses))
	cumulativeWeight := 0.0

	for i, response := range responses {
		weight := response.Weight
		if weight == 0 {
			weight = 1.0 // Default weight
		}
		cumulativeWeight += weight / totalWeight // Normalize to [0, 1]
		normalizedWeights[i] = cumulativeWeight
	}

	// Ensure the last weight is exactly 1.0 to handle floating point precision issues
	if len(normalizedWeights) > 0 {
		normalizedWeights[len(normalizedWeights)-1] = 1.0
	}

	return normalizedWeights
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

// HTTPTransportPreHook is not used for this plugin
func (p *MockerPlugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return nil, nil
}

// HTTPTransportPostHook is not used for this plugin
func (p *MockerPlugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	return nil
}

// HTTPTransportStreamChunkHook passes through streaming chunks unchanged
func (p *MockerPlugin) HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

// PreHook intercepts requests and applies mocking rules based on configuration
// This is called before the actual provider request and can short-circuit the flow
func (p *MockerPlugin) PreHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	// Skip processing if plugin is disabled
	if !p.config.Enabled {
		return req, nil, nil
	}

	skipMocker, ok := ctx.Value(schemas.BifrostContextKey("skip-mocker")).(bool)
	if ok && skipMocker {
		return req, nil, nil
	}

	if req.RequestType != schemas.ChatCompletionRequest && req.RequestType != schemas.ResponsesRequest {
		return req, nil, nil
	}

	startTime := time.Now()

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

	// Select a response from the rule's possible responses using pre-calculated weights
	response := p.selectResponse(rule)
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
	switch response.Type {
	case ResponseTypeSuccess:
		return p.generateSuccessShortCircuit(req, response, startTime)
	case ResponseTypeError:
		return p.generateErrorShortCircuit(req, response)
	}

	// Fallback: continue with normal flow if response type is unrecognized
	return req, nil, nil
}

// PostHook processes responses after provider calls
func (p *MockerPlugin) PostHook(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
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
	provider, model, _ := req.GetRequestFields()

	// Check providers - optimized string comparison
	if len(conditions.Providers) > 0 {
		providerStr := string(provider)
		found := slices.Contains(conditions.Providers, providerStr)
		if !found {
			return false
		}
	}

	// Check models - direct string comparison
	if len(conditions.Models) > 0 {
		found := false
		for _, conditionModel := range conditions.Models {
			if model == conditionModel {
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
	switch req.RequestType {
	case schemas.TextCompletionRequest:
		// Handle text completion input
		if req.TextCompletionRequest.Input.PromptStr != nil {
			return *req.TextCompletionRequest.Input.PromptStr
		} else {
			var stringBuilder strings.Builder
			for _, prompt := range req.TextCompletionRequest.Input.PromptArray {
				stringBuilder.WriteString(prompt)
			}
			return stringBuilder.String()
		}
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		// Handle chat completion input - optimized for common cases
		if req.ChatRequest.Input != nil {
			messages := req.ChatRequest.Input
			if len(messages) == 0 {
				return ""
			}

			// Fast path for single message
			if len(messages) == 1 {
				if messages[0].Content != nil && messages[0].Content.ContentStr != nil {
					return *messages[0].Content.ContentStr
				}
				return ""
			}

			// Multiple messages - use string builder for efficiency
			var builder strings.Builder
			for i, message := range messages {
				if message.Content != nil && message.Content.ContentStr != nil {
					if i > 0 {
						builder.WriteByte(' ')
					}
					builder.WriteString(*message.Content.ContentStr)
				}
			}
			return builder.String()
		}
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		// Handle responses input - optimized for common cases
		if req.ResponsesRequest.Input != nil {
			messages := req.ResponsesRequest.Input
			if len(messages) == 0 {
				return ""
			}

			// Fast path for single message
			if len(messages) == 1 {
				if messages[0].Content != nil && messages[0].Content.ContentStr != nil {
					return *messages[0].Content.ContentStr
				}
				return ""
			}

			// Multiple messages - use string builder for efficiency
			var builder strings.Builder
			for i, message := range messages {
				if message.Content == nil || message.Content.ContentStr == nil {
					continue
				}
				if i > 0 {
					builder.WriteByte(' ')
				}
				builder.WriteString(*message.Content.ContentStr)
			}
			return builder.String()
		}
	default:
		return ""
	}

	return ""
}

// calculateRequestSizeFast calculates request size with minimal overhead
func (p *MockerPlugin) calculateRequestSizeFast(req *schemas.BifrostRequest) int {
	provider, model, _ := req.GetRequestFields()

	// Approximate size calculation to avoid expensive JSON marshaling
	size := len(model) + len(string(provider))

	// Add input size
	if req.TextCompletionRequest != nil {
		if req.TextCompletionRequest.Input.PromptStr != nil {
			size += len(*req.TextCompletionRequest.Input.PromptStr)
		} else {
			for _, prompt := range req.TextCompletionRequest.Input.PromptArray {
				size += len(prompt)
			}
		}
	}

	if req.ChatRequest.Input != nil {
		for _, message := range req.ChatRequest.Input {
			if message.Content.ContentStr != nil {
				size += len(*message.Content.ContentStr)
			}
			size += 50 // Approximate overhead for message structure
		}
	}

	if req.ResponsesRequest.Input != nil {
		for _, message := range req.ResponsesRequest.Input {
			if message.Content != nil && message.Content.ContentStr != nil {
				size += len(*message.Content.ContentStr)
			}
			size += 50 // Approximate overhead for message structure
		}
	}

	return size
}

// generateSuccessShortCircuit creates a success response short-circuit with optimized allocations
func (p *MockerPlugin) generateSuccessShortCircuit(req *schemas.BifrostRequest, response *Response, startTime time.Time) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	if response.Content == nil {
		return req, nil, nil
	}

	content := response.Content
	message := content.Message

	// Apply message template if provided
	if content.MessageTemplate != nil {
		message = p.applyTemplate(*content.MessageTemplate, req)
	}

	// Apply defaults for token usage if not provided
	var usage schemas.BifrostLLMUsage
	if content.Usage != nil {
		usage = schemas.BifrostLLMUsage{
			PromptTokens:     p.getOrDefault(content.Usage.PromptTokens, 10),
			CompletionTokens: p.getOrDefault(content.Usage.CompletionTokens, 20),
			TotalTokens:      p.getOrDefault(content.Usage.TotalTokens, content.Usage.PromptTokens+content.Usage.CompletionTokens),
		}
	} else {
		// Default usage when none specified
		usage = schemas.BifrostLLMUsage{
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

	provider, model, _ := req.GetRequestFields()

	// Create mock response with proper structure
	mockResponse := &schemas.BifrostResponse{}

	if req.RequestType == schemas.ChatCompletionRequest {
		mockResponse.ChatResponse = &schemas.BifrostChatResponse{
			Model: model,
			Usage: &usage,
			Choices: []schemas.BifrostResponseChoice{
				{
					Index: 0,
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: &schemas.ChatMessage{
							Role: schemas.ChatMessageRoleAssistant,
							Content: &schemas.ChatMessageContent{
								ContentStr: &message,
							},
						},
					},
					FinishReason: finishReason,
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:    schemas.ChatCompletionRequest,
				Provider:       provider,
				ModelRequested: model,
				Latency:        int64(time.Since(startTime).Milliseconds()),
			},
		}
	} else if req.RequestType == schemas.ResponsesRequest {
		mockResponse.ResponsesResponse = &schemas.BifrostResponsesResponse{
			CreatedAt: int(time.Now().Unix()),
			Output: []schemas.ResponsesMessage{
				{
					Role: bifrost.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentStr: &message,
					},
					Type: bifrost.Ptr(schemas.ResponsesMessageTypeMessage),
				},
			},
			Usage: &schemas.ResponsesResponseUsage{
				InputTokens:  usage.PromptTokens,
				OutputTokens: usage.CompletionTokens,
				TotalTokens:  usage.TotalTokens,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:    schemas.ResponsesRequest,
				Provider:       provider,
				ModelRequested: model,
				Latency:        int64(time.Since(startTime).Milliseconds()),
			},
		}
	}

	// Override model if specified
	if content.Model != nil {
		mockResponse.ChatResponse.Model = *content.Model
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
		extraFields := mockResponse.GetExtraFields()
		extraFields.RawResponse = rawResponse
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

	provider, model, _ := req.GetRequestFields()

	errorContent := response.Error
	allowFallbacks := response.AllowFallbacks

	// Create mock error
	mockError := &schemas.BifrostError{
		Error: &schemas.ErrorField{
			Message: errorContent.Message,
		},
		AllowFallbacks: allowFallbacks,
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType:    req.RequestType,
			Provider:       provider,
			ModelRequested: model,
		},
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

// selectResponse selects a response using pre-calculated normalized weights for optimal performance
func (p *MockerPlugin) selectResponse(rule *compiledRule) *Response {
	responses := rule.Responses
	normalizedWeights := rule.normalizedWeights

	if len(responses) == 0 {
		return nil
	}

	if len(responses) == 1 {
		return &responses[0]
	}

	// Fast O(log n) binary search using pre-calculated cumulative weights
	randomValue := rand.Float64()

	// Binary search for the selected response
	left, right := 0, len(normalizedWeights)-1
	for left < right {
		mid := (left + right) / 2
		if randomValue <= normalizedWeights[mid] {
			right = mid
		} else {
			left = mid + 1
		}
	}

	return &responses[left]
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
	provider, model, _ := req.GetRequestFields()

	switch p.config.DefaultBehavior {
	case DefaultBehaviorError:
		return req, &schemas.PluginShortCircuit{
			Error: &schemas.BifrostError{
				Error: &schemas.ErrorField{
					Message: "Mock plugin default error",
				},
			},
		}, nil
	case DefaultBehaviorSuccess:
		finishReason := "stop"
		return req, &schemas.PluginShortCircuit{
			Response: &schemas.BifrostResponse{
				ChatResponse: &schemas.BifrostChatResponse{
					Model: model,
					Usage: &schemas.BifrostLLMUsage{
						PromptTokens:     5,
						CompletionTokens: 10,
						TotalTokens:      15,
					},
					Choices: []schemas.BifrostResponseChoice{
						{
							Index: 0,
							ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
								Message: &schemas.ChatMessage{
									Role: schemas.ChatMessageRoleAssistant,
									Content: &schemas.ChatMessageContent{
										ContentStr: bifrost.Ptr("Mock plugin default response"),
									},
								},
							},
							FinishReason: &finishReason,
						},
					},
					ExtraFields: schemas.BifrostResponseExtraFields{
						RequestType:    schemas.ChatCompletionRequest,
						Provider:       provider,
						ModelRequested: model,
					},
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
	sort.Slice(p.compiledRules, func(i, j int) bool {
		return p.compiledRules[i].Priority > p.compiledRules[j].Priority
	})
}

// applyTemplate applies template variables with optimized string operations including faker support
func (p *MockerPlugin) applyTemplate(template string, req *schemas.BifrostRequest) string {
	provider, model, _ := req.GetRequestFields()

	// Fast path: no template variables
	if !strings.Contains(template, "{{") {
		return template
	}

	result := template

	// Replace basic variables first
	replacer := strings.NewReplacer(
		"{{provider}}", string(provider),
		"{{model}}", model,
	)
	result = replacer.Replace(result)

	// Handle faker variables with regex for more complex patterns
	fakerRegex := regexp.MustCompile(`\{\{faker\.([^}]+)\}\}`)
	result = fakerRegex.ReplaceAllStringFunc(result, func(match string) string {
		// Extract the faker method name
		submatch := fakerRegex.FindStringSubmatch(match)
		if len(submatch) < 2 {
			return match // Return original if no match
		}

		fakerMethod := submatch[1]
		return p.generateFakerValue(fakerMethod)
	})

	return result
}

// generateFakerValue generates fake data based on the faker method name
func (p *MockerPlugin) generateFakerValue(method string) string {
	// Parse method with potential parameters (e.g., "lorem_ipsum:20" for 20 words)
	parts := strings.Split(method, ":")
	baseMethod := parts[0]

	switch baseMethod {
	case "name":
		return p.faker.Person().Name()
	case "first_name":
		return p.faker.Person().FirstName()
	case "last_name":
		return p.faker.Person().LastName()
	case "email":
		return p.faker.Internet().Email()
	case "phone":
		return p.faker.Phone().Number()
	case "address":
		return p.faker.Address().Address()
	case "city":
		return p.faker.Address().City()
	case "state":
		return p.faker.Address().State()
	case "zip_code":
		return p.faker.Address().PostCode()
	case "company":
		return p.faker.Company().Name()
	case "job_title":
		return p.faker.Company().JobTitle()
	case "lorem_ipsum":
		wordCount := 10 // default
		if len(parts) > 1 {
			if count, err := fmt.Sscanf(parts[1], "%d", &wordCount); err != nil || count != 1 {
				wordCount = 10
			}
		}
		return p.faker.Lorem().Sentence(wordCount)
	case "uuid":
		return p.faker.UUID().V4()
	case "hex_color":
		return p.faker.Color().Hex()
	case "integer":
		min, max := 1, 100 // defaults
		if len(parts) > 1 {
			params := strings.Split(parts[1], ",")
			if len(params) >= 2 {
				if _, err := fmt.Sscanf(params[0], "%d", &min); err != nil {
					min = 1 // fallback to default on parse error
				}
				if _, err := fmt.Sscanf(params[1], "%d", &max); err != nil {
					max = 100 // fallback to default on parse error
				}
			}
		}
		return fmt.Sprintf("%d", p.faker.IntBetween(min, max))
	case "float":
		min, max := 0, 100 // defaults as integers
		if len(parts) > 1 {
			params := strings.Split(parts[1], ",")
			if len(params) >= 2 {
				if _, err := fmt.Sscanf(params[0], "%d", &min); err != nil {
					min = 0 // fallback to default on parse error
				}
				if _, err := fmt.Sscanf(params[1], "%d", &max); err != nil {
					max = 100 // fallback to default on parse error
				}
			}
		}
		return fmt.Sprintf("%.2f", p.faker.Float64(2, min, max))
	case "boolean":
		return fmt.Sprintf("%t", p.faker.Bool())
	case "date":
		return p.faker.Time().Time(time.Now()).Format("2006-01-02")
	case "datetime":
		return p.faker.Time().Time(time.Now()).Format("2006-01-02 15:04:05")
	case "word":
		return p.faker.Lorem().Word()
	case "sentence":
		wordCount := 8 // default
		if len(parts) > 1 {
			if count, err := fmt.Sscanf(parts[1], "%d", &wordCount); err != nil || count != 1 {
				wordCount = 8
			}
		}
		return p.faker.Lorem().Sentence(wordCount)
	default:
		// Return the original placeholder if method is not recognized
		return fmt.Sprintf("{{faker.%s}}", method)
	}
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
