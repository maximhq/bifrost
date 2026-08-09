package anthropic

import (
	"context"
	"fmt"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

const deepSeekV4FlashModel = "deepseek-v4-flash"

// isDeepSeekV4FlashRequest is deliberately narrower than Anthropic's
// family/capability predicates. DeepSeek's Anthropic-compatible endpoint uses
// output_config.effort for this exact wire identity; dated, generic, future,
// case-variant, and differently routed models retain stock behavior.
// Source: https://api-docs.deepseek.com/guides/anthropic_api/
func isDeepSeekV4FlashRequest(provider schemas.ModelProvider, model string) bool {
	return provider == schemas.DeepSeek && model == deepSeekV4FlashModel
}

func shouldValidateDeepSeekV4FlashUsage(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string) bool {
	return isDeepSeekV4FlashRequest(provider, schemas.ResolveCanonicalModel(ctx, model))
}

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

type deepSeekResponseMetadata struct {
	Model string                 `json:"model"`
	Usage *deepSeekUsageMetadata `json:"usage"`
}

type deepSeekStreamMessageMetadata struct {
	Model string                 `json:"model"`
	Usage *deepSeekUsageMetadata `json:"usage"`
}

type deepSeekStreamMetadata struct {
	Type    AnthropicStreamEventType       `json:"type"`
	Message *deepSeekStreamMessageMetadata `json:"message"`
	Usage   *deepSeekUsageMetadata         `json:"usage"`
}

type deepSeekStreamUsageState struct {
	sawMessageStart bool
	sawMessageDelta bool
	sawMessageStop  bool
}

func validateDeepSeekV4FlashResponseMetadata(data []byte) error {
	var metadata deepSeekResponseMetadata
	if err := sonic.Unmarshal(data, &metadata); err != nil {
		return fmt.Errorf("usage metadata decode failed: %w", err)
	}
	if metadata.Model != deepSeekV4FlashModel {
		return fmt.Errorf("response model %q does not match %q", metadata.Model, deepSeekV4FlashModel)
	}
	return validateDeepSeekPromptUsage(metadata.Usage, true)
}

func validateDeepSeekV4FlashStreamMetadata(eventType string, data []byte, state *deepSeekStreamUsageState) error {
	var metadata deepSeekStreamMetadata
	if err := sonic.Unmarshal(data, &metadata); err != nil {
		return fmt.Errorf("stream usage metadata decode failed: %w", err)
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
