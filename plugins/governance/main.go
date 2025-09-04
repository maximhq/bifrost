// Package governance provides comprehensive governance plugin for Bifrost
package governance

import (
	"context"
	"fmt"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

// PluginName is the name of the governance plugin
const PluginName = "governance"

// contextKey is a custom type for context keys to avoid collisions
type contextKey string

const (
	governanceRejectedContextKey        contextKey = "bf-governance-rejected"
	governanceProviderContextKey        contextKey = "bf-governance-provider"
	governanceModelContextKey           contextKey = "bf-governance-model"
	governanceRequestTypeContextKey     contextKey = "bf-governance-request-type"
	governanceIsCacheReadContextKey     contextKey = "bf-governance-is-cache-read"
	governanceIsBatchContextKey         contextKey = "bf-governance-is-batch"
	governanceIncludeOnlyKeysContextKey contextKey = "bf-governance-include-only-keys"
)

// Config is the configuration for the governance plugin
type Config struct {
	IsVkMandatory *bool `json:"is_vk_mandatory"`
}

// GovernancePlugin implements the main governance plugin with hierarchical budget system
type GovernancePlugin struct {
	// Core components with clear separation of concerns
	store          *GovernanceStore // Pure data access layer
	resolver       *BudgetResolver  // Pure decision engine for hierarchical governance
	tracker        *UsageTracker    // Business logic owner (updates, resets, persistence)
	pricingManager *PricingManager  // Pricing data management and cost calculations

	// Dependencies
	configStore configstore.ConfigStore
	logger      schemas.Logger

	isVkMandatory *bool
}

// Init creates a new governance plugin with cleanly segregated components
// All governance features are enabled by default with optimized settings
func Init(ctx context.Context, config *Config, logger schemas.Logger, store configstore.ConfigStore, governanceConfig *configstore.GovernanceConfig) (*GovernancePlugin, error) {
	if store == nil {
		logger.Warn("governance plugin requires config store to persist data, running in memory only mode")
	}

	governanceStore, err := NewGovernanceStore(logger, store, governanceConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize governance store: %w", err)
	}
	// Initialize components in dependency order with fixed, optimal settings
	// Resolver (pure decision engine for hierarchical governance, depends only on store)
	resolver := NewBudgetResolver(governanceStore, logger)

	// 3. Tracker (business logic owner, depends on store and resolver)
	tracker := NewUsageTracker(governanceStore, resolver, store, logger)

	// 4. Pricing Manager (manages model pricing data and cost calculations)
	pricingManager, err := NewPricingManager(store, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize pricing manager: %w", err)
	}

	// 5. Perform startup reset check for any expired limits from downtime
	if store != nil {
		if err := tracker.PerformStartupResets(); err != nil {
			logger.Warn("startup reset failed: %v", err)
			// Continue initialization even if startup reset fails (non-critical)
		}
	}

	plugin := &GovernancePlugin{
		store:          governanceStore,
		resolver:       resolver,
		tracker:        tracker,
		pricingManager: pricingManager,
		configStore:    store,
		logger:         logger,
		isVkMandatory:  config.IsVkMandatory,
	}

	return plugin, nil
}

// GetName returns the name of the plugin
func (p *GovernancePlugin) GetName() string {
	return PluginName
}

// PreHook intercepts requests before they are processed (governance decision point)
func (p *GovernancePlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	// Extract governance headers and virtual key using utility functions
	headers := extractHeadersFromContext(*ctx)
	virtualKey := getStringFromContext(*ctx, ContextKey("x-bf-vk"))
	requestID := getStringFromContext(*ctx, "request-id")

	if virtualKey == "" {
		if p.isVkMandatory != nil && *p.isVkMandatory {
			return req, &schemas.PluginShortCircuit{
				Error: &schemas.BifrostError{
					Type:       bifrost.Ptr("virtual_key_required"),
					StatusCode: bifrost.Ptr(400),
					Error: schemas.ErrorField{
						Message: "x-bf-vk header is missing",
					},
				},
			}, nil
		} else {
			return req, nil, nil
		}
	}

	// Extract provider and model from request
	provider := req.Provider
	model := req.Model
	requestType := getRequestType(req)

	// Detect cache and batch operations
	isCacheRead := isCacheReadRequest(req, headers)
	isBatch := isBatchRequest(req)

	// Store original request provider/model and operation flags in context for PostHook
	*ctx = context.WithValue(*ctx, governanceProviderContextKey, provider)
	*ctx = context.WithValue(*ctx, governanceModelContextKey, model)
	*ctx = context.WithValue(*ctx, governanceRequestTypeContextKey, requestType)
	*ctx = context.WithValue(*ctx, governanceIsCacheReadContextKey, isCacheRead)
	*ctx = context.WithValue(*ctx, governanceIsBatchContextKey, isBatch)

	// Create request context for evaluation
	evaluationRequest := &EvaluationRequest{
		VirtualKey: virtualKey,
		Provider:   provider,
		Model:      model,
		Headers:    headers,
		RequestID:  requestID,
	}

	// Use resolver to make governance decision (pure decision engine)
	result := p.resolver.EvaluateRequest(ctx, evaluationRequest)

	if result.Decision != DecisionAllow {
		if ctx != nil {
			if _, ok := (*ctx).Value(governanceRejectedContextKey).(bool); !ok {
				*ctx = context.WithValue(*ctx, governanceRejectedContextKey, true)
			}
		}
	}

	// Handle decision
	switch result.Decision {
	case DecisionAllow:
		return req, nil, nil

	case DecisionVirtualKeyNotFound, DecisionVirtualKeyBlocked, DecisionModelBlocked, DecisionProviderBlocked:
		return req, &schemas.PluginShortCircuit{
			Error: &schemas.BifrostError{
				Type:       bifrost.Ptr(string(result.Decision)),
				StatusCode: bifrost.Ptr(403),
				Error: schemas.ErrorField{
					Message: result.Reason,
				},
			},
		}, nil

	case DecisionRateLimited, DecisionTokenLimited, DecisionRequestLimited:
		return req, &schemas.PluginShortCircuit{
			Error: &schemas.BifrostError{
				Type:       bifrost.Ptr(string(result.Decision)),
				StatusCode: bifrost.Ptr(429),
				Error: schemas.ErrorField{
					Message: result.Reason,
				},
			},
		}, nil

	case DecisionBudgetExceeded:
		return req, &schemas.PluginShortCircuit{
			Error: &schemas.BifrostError{
				Type:       bifrost.Ptr(string(result.Decision)),
				StatusCode: bifrost.Ptr(402),
				Error: schemas.ErrorField{
					Message: result.Reason,
				},
			},
		}, nil

	default:
		// Fallback to deny for unknown decisions
		return req, &schemas.PluginShortCircuit{
			Error: &schemas.BifrostError{
				Type: bifrost.Ptr(string(result.Decision)),
				Error: schemas.ErrorField{
					Message: "Governance decision error",
				},
			},
		}, nil
	}
}

// PostHook processes the response and updates usage tracking (business logic execution)
func (p *GovernancePlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if _, ok := (*ctx).Value(governanceRejectedContextKey).(bool); ok {
		return result, err, nil
	}

	// Extract governance information
	headers := extractHeadersFromContext(*ctx)
	virtualKey := getStringFromContext(*ctx, ContextKey("x-bf-vk"))
	requestID := getStringFromContext(*ctx, "request-id")

	// Skip if no virtual key
	if virtualKey == "" {
		return result, err, nil
	}

	// Extract provider and model from stored context values (set in PreHook)
	var provider schemas.ModelProvider
	var model string
	var requestType string

	if providerValue := (*ctx).Value(governanceProviderContextKey); providerValue != nil {
		if p, ok := providerValue.(schemas.ModelProvider); ok {
			provider = p
		}
	}
	if modelValue := (*ctx).Value(governanceModelContextKey); modelValue != nil {
		if m, ok := modelValue.(string); ok {
			model = m
		}
	}
	if requestTypeValue := (*ctx).Value(governanceRequestTypeContextKey); requestTypeValue != nil {
		if r, ok := requestTypeValue.(string); ok {
			requestType = r
		}
	}

	// If we couldn't get provider/model from context, skip usage tracking
	if provider == "" || model == "" {
		p.logger.Debug("Could not extract provider/model from context, skipping usage tracking")
		return result, err, nil
	}

	// Extract cache and batch flags from context
	isCacheRead := false
	isBatch := false
	if val := (*ctx).Value(governanceIsCacheReadContextKey); val != nil {
		if b, ok := val.(bool); ok {
			isCacheRead = b
		}
	}
	if val := (*ctx).Value(governanceIsBatchContextKey); val != nil {
		if b, ok := val.(bool); ok {
			isBatch = b
		}
	}

	// Extract team/customer info for audit trail
	var teamID, customerID *string
	if teamIDValue := headers["x-bf-team"]; teamIDValue != "" {
		teamID = &teamIDValue
	}
	if customerIDValue := headers["x-bf-customer"]; customerIDValue != "" {
		customerID = &customerIDValue
	}

	go p.postHookWorker(result, provider, model, requestType, virtualKey, requestID, teamID, customerID, isCacheRead, isBatch)

	return result, err, nil
}

// Cleanup shuts down all components gracefully
func (p *GovernancePlugin) Cleanup() error {
	if err := p.tracker.Cleanup(); err != nil {
		return err
	}

	p.pricingManager.Cleanup()

	return nil
}

// isStreamingResponse checks if the response is a streaming delta
func (p *GovernancePlugin) isStreamingResponse(result *schemas.BifrostResponse) bool {
	if result == nil {
		return false
	}

	// Check for streaming choices
	if len(result.Choices) > 0 {
		for _, choice := range result.Choices {
			if choice.BifrostStreamResponseChoice != nil {
				return true
			}
		}
	}

	// Check for streaming speech output
	if result.Speech != nil && result.Speech.BifrostSpeechStreamResponse != nil {
		return true
	}

	// Check for streaming transcription output
	if result.Transcribe != nil && result.Transcribe.BifrostTranscribeStreamResponse != nil {
		return true
	}

	return false
}

// isFinalChunk checks if this is the final chunk of a streaming response
func (p *GovernancePlugin) isFinalChunk(result *schemas.BifrostResponse) bool {
	if result == nil {
		return false
	}

	// Check for finish reason in streaming choices
	if len(result.Choices) > 0 {
		for _, choice := range result.Choices {
			if choice.BifrostStreamResponseChoice != nil && choice.FinishReason != nil {
				return true
			}
		}
	}

	// Check for usage data in speech response (indicates completion)
	if result.Speech != nil && result.Speech.BifrostSpeechStreamResponse != nil && result.Speech.Usage != nil {
		return true
	}

	// Check for usage data in transcribe response (indicates completion)
	if result.Transcribe != nil && result.Transcribe.BifrostTranscribeStreamResponse != nil && result.Transcribe.Usage != nil {
		return true
	}

	return false
}

// hasUsageData checks if the response contains actual usage information
func (p *GovernancePlugin) hasUsageData(result *schemas.BifrostResponse) bool {
	if result == nil {
		return false
	}

	// Check main usage field
	if result.Usage != nil {
		return true
	}

	// Check speech usage
	if result.Speech != nil && result.Speech.Usage != nil {
		return true
	}

	// Check transcribe usage
	if result.Transcribe != nil && result.Transcribe.Usage != nil {
		return true
	}

	return false
}

func (p *GovernancePlugin) postHookWorker(result *schemas.BifrostResponse, provider schemas.ModelProvider, model, requestType, virtualKey, requestID string, teamID, customerID *string, isCacheRead, isBatch bool) {
	// Determine if request was successful
	success := (result != nil)

	// Streaming detection
	isStreaming := p.isStreamingResponse(result)
	isFinalChunk := p.isFinalChunk(result)
	hasUsageData := p.hasUsageData(result)

	// Extract usage information from response (including speech and transcribe)
	var tokensUsed int64
	var usage *schemas.LLMUsage
	var audioSeconds *int
	var audioTokenDetails *schemas.AudioTokenDetails

	if result != nil {
		// Check main usage field
		if result.Usage != nil {
			usage = result.Usage
			tokensUsed = int64(result.Usage.TotalTokens)
		} else if result.Speech != nil && result.Speech.Usage != nil {
			// For speech synthesis, create LLMUsage from AudioLLMUsage
			tokensUsed = int64(result.Speech.Usage.TotalTokens)
			usage = &schemas.LLMUsage{
				PromptTokens:     result.Speech.Usage.InputTokens,
				CompletionTokens: 0, // Speech doesn't have completion tokens
				TotalTokens:      result.Speech.Usage.TotalTokens,
			}

			// Extract audio token details if available
			if result.Speech.Usage.InputTokensDetails != nil {
				audioTokenDetails = result.Speech.Usage.InputTokensDetails
			}
		} else if result.Transcribe != nil && result.Transcribe.Usage != nil && result.Transcribe.Usage.TotalTokens != nil {
			// For transcription, create LLMUsage from TranscriptionUsage
			tokensUsed = int64(*result.Transcribe.Usage.TotalTokens)
			inputTokens := 0
			outputTokens := 0
			if result.Transcribe.Usage.InputTokens != nil {
				inputTokens = *result.Transcribe.Usage.InputTokens
			}
			if result.Transcribe.Usage.OutputTokens != nil {
				outputTokens = *result.Transcribe.Usage.OutputTokens
			}
			usage = &schemas.LLMUsage{
				PromptTokens:     inputTokens,
				CompletionTokens: outputTokens,
				TotalTokens:      int(*result.Transcribe.Usage.TotalTokens),
			}

			// Extract audio duration if available (for duration-based pricing)
			if result.Transcribe.Usage.Seconds != nil {
				audioSeconds = result.Transcribe.Usage.Seconds
			}

			// Extract audio token details if available
			if result.Transcribe.Usage.InputTokenDetails != nil {
				audioTokenDetails = result.Transcribe.Usage.InputTokenDetails
			}
		}
	}

	cost := p.pricingManager.calculateCostForUsage(string(provider), model, usage, requestType, isCacheRead, isBatch, audioSeconds, audioTokenDetails)

	// Create usage update for tracker (business logic)
	usageUpdate := &UsageUpdate{
		VirtualKey:   virtualKey,
		Provider:     provider,
		Model:        model,
		Success:      success,
		TokensUsed:   tokensUsed,
		Cost:         cost,
		RequestID:    requestID,
		TeamID:       teamID,
		CustomerID:   customerID,
		IsStreaming:  isStreaming,
		IsFinalChunk: isFinalChunk,
		HasUsageData: hasUsageData,
	}

	// Queue usage update asynchronously using tracker
	p.tracker.UpdateUsage(usageUpdate)
}

// Public Methods exposed for handlers

// GetGovernanceStore returns the governance store
func (p *GovernancePlugin) GetGovernanceStore() *GovernanceStore {
	return p.store
}
