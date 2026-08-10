package anthropic

import (
	"context"
	"fmt"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// deepSeekV4FlashModel is the exact model identity supported by these
// Anthropic-compatibility adaptations.
const deepSeekV4FlashModel = "deepseek-v4-flash"

// isDeepSeekV4FlashRequest is deliberately narrower than Anthropic's
// family/capability predicates. DeepSeek's Anthropic-compatible endpoint uses
// output_config.effort for this exact wire identity; dated, generic, future,
// case-variant, and differently routed models retain stock behavior.
// Source: https://api-docs.deepseek.com/guides/anthropic_api/
func isDeepSeekV4FlashRequest(provider schemas.ModelProvider, model string) bool {
	return provider == schemas.DeepSeek && model == deepSeekV4FlashModel
}

// shouldValidateDeepSeekV4FlashUsage resolves aliases before deciding whether
// the response requires the V4 Flash usage-fidelity checks.
func shouldValidateDeepSeekV4FlashUsage(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string) bool {
	return isDeepSeekV4FlashRequest(provider, schemas.ResolveCanonicalModel(ctx, model))
}

// shouldValidateDeepSeekV4FlashUsageFromBody extracts the requested model from
// a serialized request and applies the same exact, alias-aware validation gate.
func shouldValidateDeepSeekV4FlashUsageFromBody(ctx *schemas.BifrostContext, provider schemas.ModelProvider, body []byte) bool {
	if provider != schemas.DeepSeek || len(body) == 0 {
		return false
	}
	model := providerUtils.GetJSONField(body, "model").String()
	return isDeepSeekV4FlashRequest(provider, schemas.ResolveCanonicalModel(ctx, model))
}

// resetAnthropicStreamAttemptState clears transport state local to the prior
// attempt. Bifrost reuses the request context across ordered fallbacks; an idle
// close must not make the next healthy Anthropic-compatible attempt look closed.
func resetAnthropicStreamAttemptState(ctx *schemas.BifrostContext) {
	if ctx != nil {
		ctx.SetValue(schemas.BifrostContextKeyConnectionClosed, false)
	}
}

// deepSeekUsageMetadata is intentionally payload-incapable. This decoder can
// retain accounting fields only: no content, tool calls, or reasoning payload.
type deepSeekUsageMetadata struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	ProviderPromptTokens     *int `json:"prompt_tokens"`
}

// deepSeekResponseMetadata contains the model identity and accounting fields
// needed to validate a non-streaming response.
type deepSeekResponseMetadata struct {
	Model string                 `json:"model"`
	Usage *deepSeekUsageMetadata `json:"usage"`
}

// deepSeekStreamMessageMetadata contains the nested model and usage metadata
// carried by a message_start event.
type deepSeekStreamMessageMetadata struct {
	Model string                 `json:"model"`
	Usage *deepSeekUsageMetadata `json:"usage"`
}

// deepSeekStreamMetadata is the payload-free subset decoded from each
// Anthropic-compatible stream event.
type deepSeekStreamMetadata struct {
	Type    AnthropicStreamEventType       `json:"type"`
	Message *deepSeekStreamMessageMetadata `json:"message"`
	Usage   *deepSeekUsageMetadata         `json:"usage"`
}

// deepSeekStreamUsageState tracks the required usage-bearing event lifecycle
// without retaining streamed content.
type deepSeekStreamUsageState struct {
	sawMessageStart bool
	sawMessageDelta bool
	sawMessageStop  bool
}

// validateDeepSeekV4FlashResponseMetadata verifies the exact served model and
// complete, internally consistent usage metadata on a unary response.
func validateDeepSeekV4FlashResponseMetadata(data []byte) error {
	var metadata deepSeekResponseMetadata
	if err := sonic.Unmarshal(data, &metadata); err != nil {
		return fmt.Errorf("usage metadata decode failed")
	}
	if metadata.Model != deepSeekV4FlashModel {
		return fmt.Errorf("response model %q does not match %q", metadata.Model, deepSeekV4FlashModel)
	}
	return validateDeepSeekPromptUsage(metadata.Usage, true)
}

// validateDeepSeekV4FlashStreamMetadata validates one event and advances the
// usage lifecycle state when the event is ordered and complete.
func validateDeepSeekV4FlashStreamMetadata(eventType string, data []byte, state *deepSeekStreamUsageState) error {
	var metadata deepSeekStreamMetadata
	if err := sonic.Unmarshal(data, &metadata); err != nil {
		return fmt.Errorf("stream usage metadata decode failed")
	}
	if string(metadata.Type) != eventType {
		return fmt.Errorf("stream event type mismatch: envelope=%q data=%q", eventType, metadata.Type)
	}
	if state.sawMessageStop {
		return fmt.Errorf("stream event %q arrived after message_stop", metadata.Type)
	}

	switch metadata.Type {
	case AnthropicStreamEventTypeMessageStart:
		if state.sawMessageStart {
			return fmt.Errorf("duplicate message_start")
		}
		if metadata.Message == nil {
			return fmt.Errorf("message_start is missing message metadata")
		}
		if metadata.Message.Model != deepSeekV4FlashModel {
			return fmt.Errorf("response model %q does not match %q", metadata.Message.Model, deepSeekV4FlashModel)
		}
		if err := validateDeepSeekPromptUsage(metadata.Message.Usage, true); err != nil {
			return err
		}
		state.sawMessageStart = true
	case AnthropicStreamEventTypeMessageDelta:
		if !state.sawMessageStart {
			return fmt.Errorf("message_delta arrived before message_start")
		}
		if err := validateDeepSeekOutputUsage(metadata.Usage); err != nil {
			return err
		}
		state.sawMessageDelta = true
	case AnthropicStreamEventTypeMessageStop:
		if !state.sawMessageStart {
			return fmt.Errorf("message_stop arrived before message_start")
		}
		if !state.sawMessageDelta {
			return fmt.Errorf("message_stop arrived without usage-bearing message_delta")
		}
		state.sawMessageStop = true
	}

	return nil
}

// validateDeepSeekV4FlashStreamComplete rejects streams that end before every
// required usage-bearing lifecycle event has arrived.
func validateDeepSeekV4FlashStreamComplete(state *deepSeekStreamUsageState) error {
	if state == nil || !state.sawMessageStart {
		return fmt.Errorf("stream ended without message_start usage metadata")
	}
	if !state.sawMessageDelta {
		return fmt.Errorf("stream ended without usage-bearing message_delta")
	}
	if !state.sawMessageStop {
		return fmt.Errorf("stream ended without message_stop")
	}
	return nil
}

// validateDeepSeekPromptUsage checks prompt-side token presence, non-negativity,
// and conservation, optionally requiring output tokens on the same envelope.
func validateDeepSeekPromptUsage(usage *deepSeekUsageMetadata, requireOutput bool) error {
	if usage == nil {
		return fmt.Errorf("usage metadata is absent")
	}
	required := []struct {
		name  string
		value *int
	}{
		{name: "input_tokens", value: usage.InputTokens},
		{name: "cache_creation_input_tokens", value: usage.CacheCreationInputTokens},
		{name: "cache_read_input_tokens", value: usage.CacheReadInputTokens},
	}
	if requireOutput {
		required = append(required, struct {
			name  string
			value *int
		}{name: "output_tokens", value: usage.OutputTokens})
	}
	for _, field := range required {
		if field.value == nil {
			return fmt.Errorf("usage metadata is collapsed: missing %s", field.name)
		}
		if *field.value < 0 {
			return fmt.Errorf("usage metadata is negative: %s", field.name)
		}
	}
	if usage.ProviderPromptTokens != nil {
		if *usage.ProviderPromptTokens < 0 {
			return fmt.Errorf("usage metadata is negative: prompt_tokens")
		}
		input := int64(*usage.InputTokens)
		created := int64(*usage.CacheCreationInputTokens)
		read := int64(*usage.CacheReadInputTokens)
		if input+created+read != int64(*usage.ProviderPromptTokens) {
			return fmt.Errorf("usage metadata is non-conserving")
		}
	}
	return nil
}

// validateDeepSeekOutputUsage checks the output-token metadata carried by a
// streaming message_delta event.
func validateDeepSeekOutputUsage(usage *deepSeekUsageMetadata) error {
	if usage == nil {
		return fmt.Errorf("message_delta usage metadata is absent")
	}
	if usage.OutputTokens == nil {
		return fmt.Errorf("message_delta usage metadata is missing output_tokens")
	}
	if *usage.OutputTokens < 0 {
		return fmt.Errorf("usage metadata is negative: output_tokens")
	}
	return nil
}

// newDeepSeekUsageFidelityError converts a validation failure into a typed 502
// that remains eligible for the configured provider fallback chain.
func newDeepSeekUsageFidelityError(err error) *schemas.BifrostError {
	statusCode := 502
	errorType := "upstream_usage_invalid"
	allowFallbacks := true
	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		AllowFallbacks: &allowFallbacks,
		Error: &schemas.ErrorField{
			Type:    &errorType,
			Code:    schemas.Ptr("deepseek_usage_fidelity"),
			Message: fmt.Sprintf("deepseek-v4-flash response usage failed fidelity validation: %v", err),
		},
	}
}

// sendDeepSeekUsageFidelityStreamError terminates the stream and emits the
// enriched, fallback-eligible fidelity error through the normal post-hook path.
func sendDeepSeekUsageFidelityStreamError(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	responseChan chan *schemas.BifrostStreamChunk,
	logger schemas.Logger,
	postHookSpanFinalizer func(context.Context),
	jsonBody []byte,
	sendBackRawRequest bool,
	err error,
) {
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	bifrostErr := providerUtils.EnrichError(ctx, newDeepSeekUsageFidelityError(err), jsonBody, nil, sendBackRawRequest, false)
	providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger, postHookSpanFinalizer)
}
