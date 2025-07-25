// Package governance provides comprehensive governance plugin for Bifrost
package governance

import (
	"context"
	"fmt"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

// PluginName is the name of the governance plugin
const PluginName = "governance"

// contextKey is a custom type for context keys to avoid collisions
type contextKey string

const (
	governanceRejectedContextKey contextKey = "bf-governance-rejected"
	governanceProviderContextKey contextKey = "bf-governance-provider"
	governanceModelContextKey    contextKey = "bf-governance-model"
)

// GovernancePlugin implements the main governance plugin with hierarchical budget system
type GovernancePlugin struct {
	// Core components with clear separation of concerns
	store    *GovernanceStore // Pure data access layer
	resolver *BudgetResolver  // Pure decision engine for hierarchical governance
	tracker  *UsageTracker    // Business logic owner (updates, resets, persistence)

	// Dependencies
	db     *gorm.DB
	logger schemas.Logger

	isVkMandatory bool
}

// NewGovernancePlugin creates a new governance plugin with cleanly segregated components
// All governance features are enabled by default with optimized settings
func NewGovernancePlugin(db *gorm.DB, logger schemas.Logger, isVkMandatory bool) (*GovernancePlugin, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection cannot be nil")
	}

	// Auto-migrate governance tables
	if err := AutoMigrateGovernanceTables(db); err != nil {
		return nil, fmt.Errorf("failed to migrate governance tables: %w", err)
	}

	// Initialize components in dependency order with fixed, optimal settings
	// 1. Store (pure data access)
	store, err := NewGovernanceStore(db, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize governance store: %w", err)
	}

	// 2. Resolver (pure decision engine for hierarchical governance, depends only on store)
	resolver := NewBudgetResolver(store, logger)

	// 3. Tracker (business logic owner, depends on store and resolver)
	tracker := NewUsageTracker(store, resolver, db, logger)

	// 4. Perform startup reset check for any expired limits from downtime
	if err := tracker.PerformStartupResets(); err != nil {
		logger.Error(fmt.Errorf("startup reset failed: %w", err))
		// Continue initialization even if startup reset fails (non-critical)
	}

	plugin := &GovernancePlugin{
		store:         store,
		resolver:      resolver,
		tracker:       tracker,
		db:            db,
		logger:        logger,
		isVkMandatory: isVkMandatory,
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
	virtualKey := getStringFromContext(*ctx, "x-bf-vk")
	requestID := getStringFromContext(*ctx, "request-id")

	if virtualKey == "" {
		if p.isVkMandatory {
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

	// Store original request provider/model in context for PostHook
	*ctx = context.WithValue(*ctx, governanceProviderContextKey, provider)
	*ctx = context.WithValue(*ctx, governanceModelContextKey, model)

	// Create request context for evaluation
	requestContext := &RequestContext{
		VirtualKey: virtualKey,
		Provider:   provider,
		Model:      model,
		Headers:    headers,
		RequestID:  requestID,
	}

	// Use resolver to make governance decision (pure decision engine)
	result := p.resolver.EvaluateRequest(requestContext)

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
		p.logger.Debug(fmt.Sprintf("Request allowed by governance: %s", result.Reason))
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

// UpdateBudgetCache updates a specific budget in the in-memory cache after API operations
func (p *GovernancePlugin) UpdateBudgetCache(budgetID string) error {
	return p.store.UpdateBudgetInMemory(budgetID)
}

// UpdateRateLimitCache updates a specific rate limit in the in-memory cache after API operations
func (p *GovernancePlugin) UpdateRateLimitCache(rateLimitID string, vkValue string) error {
	return p.store.UpdateRateLimitInMemory(rateLimitID, vkValue)
}

// PostHook processes the response and updates usage tracking (business logic execution)
func (p *GovernancePlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if _, ok := (*ctx).Value(governanceRejectedContextKey).(bool); ok {
		return result, err, nil
	}

	// Extract governance information
	headers := extractHeadersFromContext(*ctx)
	virtualKey := getStringFromContext(*ctx, "x-bf-vk")
	requestID := getStringFromContext(*ctx, "request-id")

	// Skip if no virtual key
	if virtualKey == "" {
		return result, err, nil
	}

	// Extract provider and model from stored context values (set in PreHook)
	var provider schemas.ModelProvider
	var model string

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

	// If we couldn't get provider/model from context, skip usage tracking
	if provider == "" || model == "" {
		p.logger.Debug("Could not extract provider/model from context, skipping usage tracking")
		return result, err, nil
	}

	// Extract team/customer info for audit trail
	var teamID, customerID *string
	if teamIDValue := headers["x-bf-team"]; teamIDValue != "" {
		teamID = &teamIDValue
	}
	if customerIDValue := headers["x-bf-customer"]; customerIDValue != "" {
		customerID = &customerIDValue
	}

	go p.postHookWorker(result, provider, model, virtualKey, requestID, teamID, customerID)

	return result, err, nil
}

// Cleanup shuts down all components gracefully
func (p *GovernancePlugin) Cleanup() error {
	if err := p.tracker.Cleanup(); err != nil {
		return err
	}

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

func (p *GovernancePlugin) postHookWorker(result *schemas.BifrostResponse, provider schemas.ModelProvider, model string, virtualKey string, requestID string, teamID *string, customerID *string) {
	// Determine if request was successful
	success := (result != nil)

	// Streaming detection
	isStreaming := p.isStreamingResponse(result)
	isFinalChunk := p.isFinalChunk(result)
	hasUsageData := p.hasUsageData(result)

	// Extract usage information from response (including speech and transcribe)
	var tokensUsed int64
	if result != nil {
		// Check main usage field
		if result.Usage != nil {
			tokensUsed = int64(result.Usage.TotalTokens)
		} else if result.Speech != nil && result.Speech.Usage != nil {
			// For speech synthesis, use characters or similar metric
			tokensUsed = int64(result.Speech.Usage.TotalTokens)
		} else if result.Transcribe != nil && result.Transcribe.Usage != nil && result.Transcribe.Usage.TotalTokens != nil {
			// For transcription, use duration or similar metric
			tokensUsed = int64(*result.Transcribe.Usage.TotalTokens)
		}
	}

	costCents := calculateCostInCents(provider, model, tokensUsed) // Will be implemented later

	// Create usage update for tracker (business logic)
	usageUpdate := &UsageUpdate{
		VirtualKey:   virtualKey,
		Provider:     provider,
		Model:        model,
		Success:      success,
		TokensUsed:   tokensUsed,
		CostCents:    costCents,
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
