package compat

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	estimatedImageInputTokens  = 256
	estimatedAudioInputTokens  = 128
	estimatedBinaryFileTokens  = 128
	estimatedReferenceTokens   = 16
	estimatedBytesPerTextToken = 4
)

// countTokensFallbackState carries the original request across fallback attempts
// so PostLLMHook can synthesize a response only on the final unsupported attempt.
type countTokensFallbackState struct {
	request              *schemas.BifrostResponsesRequest
	explicitlyDisallowed bool
}

type countTokensFallbackStateKey struct{}

type countTokensEstimate struct {
	inputTokens int
	textTokens  int
	audioTokens int
	imageTokens int
}

func (e countTokensEstimate) withInput(tokens int) countTokensEstimate {
	e.inputTokens += tokens
	return e
}

func (e countTokensEstimate) withText(tokens int) countTokensEstimate {
	e.inputTokens += tokens
	e.textTokens += tokens
	return e
}

func (e countTokensEstimate) withAudio(tokens int) countTokensEstimate {
	e.inputTokens += tokens
	e.audioTokens += tokens
	return e
}

func (e countTokensEstimate) withImage(tokens int) countTokensEstimate {
	e.inputTokens += tokens
	e.imageTokens += tokens
	return e
}

func (e countTokensEstimate) add(other countTokensEstimate) countTokensEstimate {
	e.inputTokens += other.inputTokens
	e.textTokens += other.textTokens
	e.audioTokens += other.audioTokens
	e.imageTokens += other.imageTokens
	return e
}

func (p *CompatPlugin) isCountTokensExplicitlyDisallowed(provider schemas.ModelProvider) bool {
	if p == nil || p.account == nil {
		return false
	}

	config, err := p.account.GetConfigForProvider(provider)
	if err != nil || config == nil || config.CustomProviderConfig == nil || config.CustomProviderConfig.AllowedRequests == nil {
		return false
	}

	return !config.CustomProviderConfig.IsOperationAllowed(schemas.CountTokensRequest)
}

func shouldGracefullyFallbackCountTokens(state *countTokensFallbackState, ctx *schemas.BifrostContext, bifrostErr *schemas.BifrostError) bool {
	if state == nil || state.request == nil || state.explicitlyDisallowed {
		return false
	}
	if bifrostErr == nil || bifrostErr.Error == nil || bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "unsupported_operation" {
		return false
	}

	fallbackIndex := 0
	if ctx != nil {
		if idx, ok := ctx.Value(schemas.BifrostContextKeyFallbackIndex).(int); ok {
			fallbackIndex = idx
		}
	}

	return fallbackIndex >= len(state.request.Fallbacks)
}

func buildGracefulCountTokensFallbackResponse(request *schemas.BifrostResponsesRequest) *schemas.BifrostCountTokensResponse {
	if request == nil {
		return nil
	}

	estimate := estimateCountTokensFromResponsesRequest(request)
	totalTokens := estimate.inputTokens

	return &schemas.BifrostCountTokensResponse{
		Object:      "response.input_tokens",
		Model:       request.Model,
		InputTokens: estimate.inputTokens,
		InputTokensDetails: &schemas.ResponsesResponseInputTokens{
			TextTokens:  estimate.textTokens,
			AudioTokens: estimate.audioTokens,
			ImageTokens: estimate.imageTokens,
		},
		TotalTokens: &totalTokens,
	}
}

func estimateCountTokensFromResponsesRequest(request *schemas.BifrostResponsesRequest) countTokensEstimate {
	var estimate countTokensEstimate
	if request == nil {
		return estimate
	}

	for _, msg := range request.Input {
		estimate = estimate.add(estimateCountTokensFromResponsesMessage(msg))
	}

	if request.Params != nil {
		if request.Params.Instructions != nil {
			estimate = estimate.withText(estimateTokensFromText(*request.Params.Instructions))
		}
		for _, tool := range request.Params.Tools {
			estimate = estimate.withText(estimateTokensFromOptionalString(tool.Name))
			estimate = estimate.withText(estimateTokensFromOptionalString(tool.Description))
		}
	}

	if estimate.inputTokens == 0 && len(request.RawRequestBody) > 0 {
		rawTokens := estimateTokensFromBytes(len(request.RawRequestBody))
		estimate = estimate.withText(rawTokens)
	}

	return estimate
}

func estimateCountTokensFromResponsesMessage(msg schemas.ResponsesMessage) countTokensEstimate {
	var estimate countTokensEstimate

	if msg.Content != nil {
		if msg.Content.ContentStr != nil {
			estimate = estimate.withText(estimateTokensFromText(*msg.Content.ContentStr))
		} else {
			for _, block := range msg.Content.ContentBlocks {
				estimate = estimate.add(estimateCountTokensFromContentBlock(block))
			}
		}
	}

	if msg.ResponsesToolMessage != nil {
		estimate = estimate.withText(estimateTokensFromOptionalString(msg.Name))
		estimate = estimate.withText(estimateTokensFromOptionalString(msg.Namespace))
		estimate = estimate.withText(estimateTokensFromOptionalString(msg.Arguments))
		if msg.Output != nil {
			estimate = estimate.withText(estimateTokensFromOptionalString(msg.Output.ResponsesToolCallOutputStr))
			for _, block := range msg.Output.ResponsesFunctionToolCallOutputBlocks {
				estimate = estimate.add(estimateCountTokensFromContentBlock(block))
			}
		}
		estimate = estimate.withText(estimateTokensFromOptionalString(msg.Error))
	}

	if msg.ResponsesToolMessage != nil && msg.ResponsesCustomToolCall != nil {
		estimate = estimate.withText(estimateTokensFromText(msg.ResponsesCustomToolCall.Input))
	}

	if msg.ResponsesReasoning != nil {
		for _, summary := range msg.Summary {
			estimate = estimate.withText(estimateTokensFromText(summary.Text))
		}
		if msg.EncryptedContent != nil {
			estimate = estimate.withInput(estimateTokensFromBytes(len(*msg.EncryptedContent)))
		}
	}

	return estimate
}

func estimateCountTokensFromContentBlock(block schemas.ResponsesMessageContentBlock) countTokensEstimate {
	var estimate countTokensEstimate

	if block.Text != nil {
		return estimate.withText(estimateTokensFromText(*block.Text))
	}

	if block.ResponsesInputMessageContentBlockImage != nil {
		estimate = estimate.withImage(estimatedImageInputTokens)
		if block.ImageURL != nil {
			estimate = estimate.withText(estimatedReferenceTokens)
		}
	}
	if block.ResponsesInputMessageContentBlockFile != nil {
		if block.FileData != nil {
			estimate = estimate.withInput(estimateTokensFromBytes(len(*block.FileData)))
		} else {
			estimate = estimate.withInput(estimatedBinaryFileTokens)
		}
		if block.FileURL != nil || block.FileID != nil {
			estimate = estimate.withText(estimatedReferenceTokens)
		}
		if block.Filename != nil {
			estimate = estimate.withText(estimateTokensFromText(*block.Filename))
		}
	}
	if block.Audio != nil {
		if block.Audio.Data != "" {
			estimate = estimate.withAudio(max(estimatedAudioInputTokens, estimateTokensFromBytes(len(block.Audio.Data))))
		} else {
			estimate = estimate.withAudio(estimatedAudioInputTokens)
		}
	}
	if block.ResponsesOutputMessageContentRenderedContent != nil {
		estimate = estimate.withText(estimateTokensFromText(block.RenderedContent))
	}
	if block.ResponsesOutputMessageContentCompaction != nil {
		estimate = estimate.withText(estimateTokensFromText(block.Summary))
	}
	if block.ResponsesOutputMessageContentRefusal != nil {
		estimate = estimate.withText(estimateTokensFromText(block.Refusal))
	}

	return estimate
}

func estimateTokensFromOptionalString(value *string) int {
	if value == nil {
		return 0
	}
	return estimateTokensFromText(*value)
}

func estimateTokensFromText(text string) int {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}

	return len(strings.Fields(trimmed))
}

func estimateTokensFromBytes(byteLen int) int {
	if byteLen <= 0 {
		return 0
	}
	return max(1, (byteLen+estimatedBytesPerTextToken-1)/estimatedBytesPerTextToken)
}
