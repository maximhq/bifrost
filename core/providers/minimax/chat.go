package minimax

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

const defaultChatPath = "/v1/chat/completions"

// ChatCompletion performs a non-streaming request against MiniMax's
// OpenAI-compatible Chat Completions endpoint.
func (provider *MiniMaxProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.MiniMax, provider.customProviderConfig, schemas.ChatCompletionRequest); err != nil {
		return nil, err
	}
	if err := validateMiniMaxChatRequest(request); err != nil {
		return nil, err
	}
	if err := rejectMiniMaxLargePayload(ctx); err != nil {
		return nil, err
	}

	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, defaultChatPath, schemas.ChatCompletionRequest),
		request,
		provider.authHeaders(key),
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		handleMiniMaxOpenAIResponse[schemas.BifrostChatResponse],
		parseMiniMaxHTTPError,
		nil,
		provider.logger,
	)
}

// ChatCompletionStream performs a streaming request against MiniMax's
// OpenAI-compatible Chat Completions endpoint.
func (provider *MiniMaxProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.MiniMax, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}
	if err := validateMiniMaxChatRequest(request); err != nil {
		return nil, err
	}
	if err := rejectMiniMaxLargePayload(ctx); err != nil {
		return nil, err
	}

	requestURL := provider.buildRequestURL(ctx, defaultChatPath, schemas.ChatCompletionStreamRequest)
	var postResponseConverter func(*schemas.BifrostChatResponse) *schemas.BifrostChatResponse
	if provider.usesCumulativeChatStream(requestURL) {
		postResponseConverter = newMiniMaxChatStreamNormalizer().normalize
	}
	return openai.HandleOpenAIChatCompletionStreaming(
		ctx,
		provider.streamingClient,
		requestURL,
		request,
		provider.authHeaders(key),
		provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		handleMiniMaxOpenAIResponse[schemas.BifrostChatResponse],
		parseMiniMaxHTTPError,
		nil,
		postResponseConverter,
		nil,
		provider.logger,
		postHookSpanFinalizer,
	)
}

func validateMiniMaxChatRequest(request *schemas.BifrostChatRequest) *schemas.BifrostError {
	if request == nil || request.Params == nil || request.Params.N == nil || *request.Params.N == 1 {
		return nil
	}
	return providerUtils.NewBifrostBadRequestError(fmt.Sprintf("MiniMax chat completions only supports n=1, got %d", *request.Params.N))
}

type miniMaxReasoningStreamKey struct {
	choiceIndex int
	detailIndex int
}

type miniMaxChatStreamNormalizer struct {
	content          map[int]string
	reasoning        map[int]string
	reasoningDetails map[miniMaxReasoningStreamKey]string
}

func newMiniMaxChatStreamNormalizer() *miniMaxChatStreamNormalizer {
	return &miniMaxChatStreamNormalizer{
		content:          make(map[int]string),
		reasoning:        make(map[int]string),
		reasoningDetails: make(map[miniMaxReasoningStreamKey]string),
	}
}

// normalize converts MiniMax's cumulative Chat stream text into true deltas before
// Bifrost's stream accumulator concatenates it. If a field stops being cumulative,
// the value is treated as an ordinary delta rather than discarded.
func (n *miniMaxChatStreamNormalizer) normalize(response *schemas.BifrostChatResponse) *schemas.BifrostChatResponse {
	if response == nil {
		return nil
	}
	for i := range response.Choices {
		choice := &response.Choices[i]
		if choice.ChatStreamResponseChoice == nil || choice.ChatStreamResponseChoice.Delta == nil {
			continue
		}
		delta := choice.ChatStreamResponseChoice.Delta
		if delta.Content != nil {
			value := cumulativeTextDelta(n.content, choice.Index, *delta.Content)
			delta.Content = &value
		}
		if delta.Reasoning != nil {
			value := cumulativeTextDelta(n.reasoning, choice.Index, *delta.Reasoning)
			delta.Reasoning = &value
		}
		for j := range delta.ReasoningDetails {
			detail := &delta.ReasoningDetails[j]
			if detail.Text == nil {
				continue
			}
			key := miniMaxReasoningStreamKey{choiceIndex: choice.Index, detailIndex: detail.Index}
			value := cumulativeTextDelta(n.reasoningDetails, key, *detail.Text)
			detail.Text = &value
		}
	}
	return response
}

func cumulativeTextDelta[K comparable](seen map[K]string, key K, current string) string {
	previous := seen[key]
	if previous == "" {
		seen[key] = current
		return current
	}
	if strings.HasPrefix(current, previous) {
		seen[key] = current
		return strings.TrimPrefix(current, previous)
	}
	seen[key] = current
	return current
}

func (provider *MiniMaxProvider) usesCumulativeChatStream(requestURL string) bool {
	if provider == nil || provider.authType != schemas.MiniMaxAuthTypeBearer {
		return false
	}
	parsed, err := url.Parse(requestURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "api.minimax.io" || host == "api.minimax.cn"
}

func rejectMiniMaxLargePayload(ctx *schemas.BifrostContext) *schemas.BifrostError {
	if !providerUtils.IsLargePayloadPassthroughEnabled(ctx) {
		return nil
	}
	providerUtils.DrainLargePayloadRemainder(ctx)
	err := providerUtils.NewBifrostBadRequestError("MiniMax Chat and Responses requests do not support large-payload passthrough because provider-specific request and stream normalization is required")
	err.AllowFallbacks = schemas.Ptr(false)
	return err
}
