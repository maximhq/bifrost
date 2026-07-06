package bedrock

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"net/http"
	"strings"

	openai "github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// isMantleModel reports whether a model should be routed via the Bedrock Mantle endpoint.
// Accepts "gpt-*"/"gemma-4-*", "openai.gpt-*"/"google.gemma-4-*", or region-prefixed variants.
// Gemma 3 is intentionally excluded: it only supports Chat (not Responses) on mantle, and the
// non-mantle path serves both APIs via Converse, so forcing it to mantle would break Responses.
func isMantleModel(model string) bool {
	return strings.Contains(model, "gpt-") || strings.Contains(model, "gemma-4")
}

// mantleURL builds the Bedrock Mantle endpoint URL for the given region, model, and API path.
// Pass the canonical (capability-resolved) model for correct path gating; the request body
// still carries the wire request.Model. Frontier families (closed gpt-5.x, Gemma 4) live under
// the "openai/v1" base path; gpt-oss uses the bare "v1" path.
func mantleURL(region, model, path string) string {
	base := "v1"
	if strings.Contains(model, "gpt-5") || strings.Contains(model, "gemma-4") {
		base = "openai/v1"
	}
	return fmt.Sprintf("https://bedrock-mantle.%s.api.aws/%s/%s", region, base, path)
}

// mantleSigV4Headers computes SigV4 auth headers for a mantle request by signing a dummy
// net/http.Request. jsonData must be the exact bytes that will be sent. accept must match
// the Accept header the actual request will send, since SigV4 signs all request headers.
func (provider *BedrockProvider) mantleSigV4Headers(
	ctx *schemas.BifrostContext,
	jsonData []byte,
	requestURL, accept string,
	key schemas.Key,
	region string,
) (map[string]string, *schemas.BifrostError) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(jsonData))
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to create signing request", err)
	}
	req.Header.Set("Accept", accept)
	if bifrostErr := signAWSRequestFromKey(ctx, req, key.BedrockKeyConfig, region, bedrockMantleSigningService); bifrostErr != nil {
		return nil, bifrostErr
	}
	headers := map[string]string{
		"Authorization":        req.Header.Get("Authorization"),
		"X-Amz-Date":           req.Header.Get("X-Amz-Date"),
		"x-amz-content-sha256": req.Header.Get("x-amz-content-sha256"),
		"Accept":               accept,
	}
	if token := req.Header.Get("X-Amz-Security-Token"); token != "" {
		headers["X-Amz-Security-Token"] = token
	}
	return headers, nil
}

// chatCompletionViaMantle handles non-streaming chat completions for mantle (gpt-oss) models.
func (provider *BedrockProvider) chatCompletionViaMantle(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	url := mantleURL(region, schemas.ResolveCanonicalModel(ctx, request.Model), "chat/completions")

	// Build extraHeaders: always start with network-config headers, then overlay SigV4 if needed.
	// Allocate explicitly so maps.Copy never writes into a nil map.
	extraHeaders := make(map[string]string, len(provider.networkConfig.ExtraHeaders))
	maps.Copy(extraHeaders, provider.networkConfig.ExtraHeaders)
	if key.Value.GetValue() == "" {
		// SigV4: pre-build body for signing. HandleOpenAIChatCompletionRequest rebuilds the
		// same bytes (deterministic marshaling), so the signature stays valid.
		jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(ctx, request, func() (providerUtils.RequestBodyWithExtraParams, error) {
			return openai.ToOpenAIChatRequest(ctx, request), nil
		})
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		sigHeaders, bifrostErr := provider.mantleSigV4Headers(ctx, jsonData, url, "application/json", key, region)
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		maps.Copy(extraHeaders, sigHeaders)
	}

	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.mantleClient,
		url,
		request,
		key,
		extraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil, nil,
		provider.logger,
	)
}

// chatCompletionStreamViaMantle handles streaming chat completions for mantle (gpt-oss) models.
func (provider *BedrockProvider) chatCompletionStreamViaMantle(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	url := mantleURL(region, schemas.ResolveCanonicalModel(ctx, request.Model), "chat/completions")

	// Bearer: identical to Groq / any OpenAI-compatible provider.
	if key.Value.GetValue() != "" {
		authHeader := map[string]string{"Authorization": "Bearer " + key.Value.GetValue()}
		return openai.HandleOpenAIChatCompletionStreaming(
			ctx, provider.mantleStreamingClient, url, request,
			authHeader, provider.networkConfig.ExtraHeaders,
			provider.networkConfig.StreamIdleTimeoutInSeconds,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			provider.GetProviderKey(), postHookRunner,
			nil, nil, nil, nil, nil,
			provider.logger, postHookSpanFinalizer,
		)
	}

	// SigV4: pre-build body to sign, then pass it via customRequestConverter so the handler
	// sends the exact same bytes we signed.
	openaiReq := openai.ToOpenAIChatRequest(ctx, request)
	openaiReq.Stream = schemas.Ptr(true)
	openaiReq.StreamOptions = &schemas.ChatStreamOptions{IncludeUsage: schemas.Ptr(true)}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(ctx, request, func() (providerUtils.RequestBodyWithExtraParams, error) {
		return openaiReq, nil
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	authHeader, bifrostErr := provider.mantleSigV4Headers(ctx, jsonData, url, "text/event-stream", key, region)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return openai.HandleOpenAIChatCompletionStreaming(
		ctx, provider.mantleStreamingClient, url, request,
		authHeader, provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(), postHookRunner,
		func(_ *schemas.BifrostChatRequest) (providerUtils.RequestBodyWithExtraParams, error) {
			return openaiReq, nil
		},
		nil, nil, nil, nil,
		provider.logger, postHookSpanFinalizer,
	)
}

// responsesViaMantle handles non-streaming Responses API requests for mantle (gpt-oss) models.
func (provider *BedrockProvider) responsesViaMantle(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	url := mantleURL(region, schemas.ResolveCanonicalModel(ctx, request.Model), "responses")

	extraHeaders := make(map[string]string, len(provider.networkConfig.ExtraHeaders))
	maps.Copy(extraHeaders, provider.networkConfig.ExtraHeaders)
	if key.Value.GetValue() == "" {
		jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(ctx, request, func() (providerUtils.RequestBodyWithExtraParams, error) {
			return openai.ToOpenAIResponsesRequest(ctx, request), nil
		})
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		sigHeaders, bifrostErr := provider.mantleSigV4Headers(ctx, jsonData, url, "application/json", key, region)
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		maps.Copy(extraHeaders, sigHeaders)
	}

	return openai.HandleOpenAIResponsesRequest(
		ctx,
		provider.mantleClient,
		url,
		request,
		key,
		extraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil, nil,
		provider.logger,
	)
}

// responsesStreamViaMantle handles streaming Responses API requests for mantle (gpt-oss) models.
func (provider *BedrockProvider) responsesStreamViaMantle(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	url := mantleURL(region, schemas.ResolveCanonicalModel(ctx, request.Model), "responses")

	// Bearer: identical to Groq / any OpenAI-compatible provider.
	if key.Value.GetValue() != "" {
		authHeader := map[string]string{"Authorization": "Bearer " + key.Value.GetValue()}
		return openai.HandleOpenAIResponsesStreaming(
			ctx, provider.mantleStreamingClient, url, request,
			authHeader, provider.networkConfig.ExtraHeaders,
			provider.networkConfig.StreamIdleTimeoutInSeconds,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			provider.GetProviderKey(), postHookRunner,
			nil, nil, nil, nil,
			provider.logger, postHookSpanFinalizer,
		)
	}

	// SigV4: pre-build body to sign.
	openaiReq := openai.ToOpenAIResponsesRequest(ctx, request)
	openaiReq.Stream = schemas.Ptr(true)

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(ctx, request, func() (providerUtils.RequestBodyWithExtraParams, error) {
		return openaiReq, nil
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	authHeader, bifrostErr := provider.mantleSigV4Headers(ctx, jsonData, url, "text/event-stream", key, region)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return openai.HandleOpenAIResponsesStreaming(
		ctx, provider.mantleStreamingClient, url, request,
		authHeader, provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(), postHookRunner,
		nil, nil,
		func(_ *openai.OpenAIResponsesRequest) *openai.OpenAIResponsesRequest {
			return openaiReq
		},
		nil,
		provider.logger, postHookSpanFinalizer,
	)
}
