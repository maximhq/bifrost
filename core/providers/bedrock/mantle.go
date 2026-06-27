package bedrock

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	openai "github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// isMantleModel reports whether a model should be routed via the Bedrock Mantle
// OpenAI-compatible endpoint. OpenAI-family (gpt-*) and Gemma 4 models are mantle-only
// (they have no Converse equivalent). Gemma 3 is intentionally excluded: it only supports
// Chat (not Responses) on mantle, and the Converse path serves both APIs, so forcing it to
// mantle would break Responses.
func isMantleModel(ctx *schemas.BifrostContext, model string) bool {
	return schemas.IsOpenAIModelFamily(ctx, model) || strings.Contains(model, "gemma-4")
}

// mantleOpenAIURL builds the Bedrock Mantle OpenAI-compatible endpoint URL for the given
// region, model, and API path (e.g. "chat/completions", "responses"). Pass the canonical
// (capability-resolved) model for correct path gating; the request body still carries the
// wire request.Model. Frontier families (closed gpt-5.x, Gemma 4) live under the "openai/v1"
// base path; gpt-oss uses the bare "v1" path.
func mantleOpenAIURL(region, model, path string) string {
	base := "v1"
	if strings.Contains(model, "gpt-5") || strings.Contains(model, "gemma-4") {
		base = "openai/v1"
	}
	return fmt.Sprintf("https://bedrock-mantle.%s.api.aws/%s/%s", region, base, path)
}

// mantleSigV4Headers computes SigV4 auth headers for a mantle request by signing a dummy
// net/http.Request. jsonData must be the exact bytes that will be sent. accept must match
// the Accept header the actual request will send, since SigV4 signs all request headers.
// extraHeaders are the static headers the actual request will carry; any x-amz-* among them
// are added to the canonical request before signing so the signature covers them (AWS requires
// every x-amz-* header on the wire to be signed, otherwise verification fails).
func (provider *BedrockProvider) mantleSigV4Headers(
	ctx *schemas.BifrostContext,
	jsonData []byte,
	requestURL, accept string,
	key schemas.Key,
	region string,
	extraHeaders map[string]string,
) (map[string]string, *schemas.BifrostError) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(jsonData))
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to create signing request", err)
	}
	req.Header.Set("Accept", accept)
	for k, v := range extraHeaders {
		if strings.HasPrefix(strings.ToLower(k), "x-amz-") {
			req.Header.Set(k, v)
		}
	}
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

// mantleChatCompletions handles non-streaming chat completions for mantle models (gpt-*
// and Gemma 4) via the Bedrock Mantle OpenAI-compatible endpoint.
func (provider *BedrockProvider) mantleChatCompletions(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	url := mantleOpenAIURL(region, schemas.ResolveCanonicalModel(ctx, request.Model), "chat/completions")

	// SigV4 (empty key value): sign the exact body the handler builds via a signer closure.
	// Bearer (key has a value): no signer; auth flows through the Authorization header.
	var signer providerUtils.BodySigner
	if key.Value.GetValue() == "" {
		signer = func(body []byte) (map[string]string, *schemas.BifrostError) {
			return provider.mantleSigV4Headers(ctx, body, url, "application/json", key, region, provider.networkConfig.ExtraHeaders)
		}
	}

	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.mantleClient,
		url,
		request,
		openai.BearerAuthHeader(key),
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		nil,
		signer,
		provider.logger,
	)
}

// mantleChatCompletionsStream handles streaming chat completions for mantle models (gpt-*
// and Gemma 4) via the Bedrock Mantle OpenAI-compatible endpoint.
func (provider *BedrockProvider) mantleChatCompletionsStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	url := mantleOpenAIURL(region, schemas.ResolveCanonicalModel(ctx, request.Model), "chat/completions")

	// SigV4 (empty key value): sign the exact body the handler builds via a signer closure.
	// Bearer (key has a value): no signer; auth flows through the Authorization header.
	var signer providerUtils.BodySigner
	if key.Value.GetValue() == "" {
		signer = func(body []byte) (map[string]string, *schemas.BifrostError) {
			return provider.mantleSigV4Headers(ctx, body, url, "text/event-stream", key, region, provider.networkConfig.ExtraHeaders)
		}
	}

	return openai.HandleOpenAIChatCompletionStreaming(
		ctx, provider.mantleStreamingClient, url, request,
		openai.BearerAuthHeader(key), provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(), postHookRunner,
		nil,
		nil,
		nil,
		nil,
		nil,
		signer,
		provider.logger,
		postHookSpanFinalizer,
	)
}

// mantleResponses handles non-streaming Responses API requests for mantle models (gpt-*
// and Gemma 4) via the Bedrock Mantle OpenAI-compatible endpoint.
func (provider *BedrockProvider) mantleResponses(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	url := mantleOpenAIURL(region, schemas.ResolveCanonicalModel(ctx, request.Model), "responses")

	// SigV4 (empty key value): sign the exact body the handler builds via a signer closure.
	// Bearer (key has a value): no signer; auth flows through the Authorization header.
	var signer providerUtils.BodySigner
	if key.Value.GetValue() == "" {
		signer = func(body []byte) (map[string]string, *schemas.BifrostError) {
			return provider.mantleSigV4Headers(ctx, body, url, "application/json", key, region, provider.networkConfig.ExtraHeaders)
		}
	}

	return openai.HandleOpenAIResponsesRequest(
		ctx,
		provider.mantleClient,
		url,
		request,
		openai.BearerAuthHeader(key),
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		nil,
		signer,
		provider.logger,
	)
}

// mantleResponsesStream handles streaming Responses API requests for mantle models (gpt-*
// and Gemma 4) via the Bedrock Mantle OpenAI-compatible endpoint.
func (provider *BedrockProvider) mantleResponsesStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	url := mantleOpenAIURL(region, schemas.ResolveCanonicalModel(ctx, request.Model), "responses")

	// SigV4 (empty key value): sign the exact body the handler builds via a signer closure.
	// Bearer (key has a value): no signer; auth flows through the Authorization header.
	var signer providerUtils.BodySigner
	if key.Value.GetValue() == "" {
		signer = func(body []byte) (map[string]string, *schemas.BifrostError) {
			return provider.mantleSigV4Headers(ctx, body, url, "text/event-stream", key, region, provider.networkConfig.ExtraHeaders)
		}
	}

	return openai.HandleOpenAIResponsesStreaming(
		ctx, provider.mantleStreamingClient, url, request,
		openai.BearerAuthHeader(key), provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(), postHookRunner,
		nil,
		nil,
		nil,
		nil,
		signer,
		provider.logger,
		postHookSpanFinalizer,
	)
}
