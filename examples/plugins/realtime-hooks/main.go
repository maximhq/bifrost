package main

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

type contextKey uint8

const (
	requestTypeKey contextKey = iota
	userIDKey
)

// GetName identifies this plugin when Bifrost loads its exported hooks.
func GetName() string { return "realtime-hooks" }

// HTTPTransportPreHook opts a small user identifier into native session contexts.
// The header is demonstration metadata, not an authenticated identity.
func HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	if userID := req.CaseInsensitiveHeaderLookup("X-Example-User-ID"); userID != "" {
		ctx.SetValueForRealtimeTurns(userIDKey, userID)
	}
	return nil, nil
}

// PreRequestHook leaves routing to Bifrost. Realtime turns use the LLM hooks below.
func PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

// PreLLMHook observes each native realtime turn or Live billing unit before admission.
func PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if req.RequestType != schemas.RealtimeRequest && req.RequestType != schemas.LiveRequest {
		return req, nil, nil
	}
	// Keep small per-unit state here, rather than in a global session map.
	ctx.SetValue(requestTypeKey, req.RequestType)
	provider, model, _ := req.GetRequestFields()
	ctx.Log(schemas.LogLevelInfo, fmt.Sprintf("Starting %s unit: provider=%s model=%s", req.RequestType, provider, model))
	return req, nil, nil
}

// PostLLMHook observes usage and cost without changing the response or charging budgets.
func PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	requestType, ok := ctx.Value(requestTypeKey).(schemas.RequestType)
	if !ok {
		return resp, bifrostErr, nil
	}
	// Read HTTP setup metadata normally, without retaining the HTTP context.
	if userID, _ := ctx.Value(userIDKey).(string); userID != "" {
		ctx.Log(schemas.LogLevelInfo, fmt.Sprintf("%s unit metadata: user_id=%q", requestType, userID))
	}
	if bifrostErr != nil {
		// Avoid logging provider error text, which can contain request content.
		ctx.Log(schemas.LogLevelWarn, fmt.Sprintf("%s unit ended with an error", requestType))
	}
	if resp != nil && resp.ResponsesResponse != nil {
		if usage := resp.ResponsesResponse.Usage; usage != nil {
			ctx.Log(schemas.LogLevelInfo, fmt.Sprintf("%s usage: input_tokens=%d output_tokens=%d", requestType, usage.InputTokens, usage.OutputTokens))
			if usage.AudioSeconds != nil {
				ctx.Log(schemas.LogLevelInfo, fmt.Sprintf("%s usage: audio_seconds=%.3f", requestType, *usage.AudioSeconds))
			}
		}
		// Calculate synchronously while the unit's context and pricing scope are valid.
		// A missing catalog or missing billable usage can produce a nil breakdown.
		if cost := ctx.CalculateCostBreakdown(resp); cost != nil {
			ctx.Log(schemas.LogLevelInfo, fmt.Sprintf("%s cost: %.8f USD", requestType, cost.TotalCost))
		}
	}
	return resp, bifrostErr, nil
}

// Cleanup has no resources to release because this plugin owns no connections or workers.
func Cleanup() error { return nil }
