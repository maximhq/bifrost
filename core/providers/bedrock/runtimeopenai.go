package bedrock

import (
	"context"
	"fmt"
	"maps"
	"strings"

	openai "github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// runtimeOpenAIURL builds the bedrock-runtime OpenAI-compatible endpoint URL for the given
// region and API path (e.g. "responses"). Only the frontier families reach this surface, and
// they all live under "openai/v1"; gpt-oss carries a bare id and so routes to mantle instead.
// Unlike the Converse paths the model is not part of the URL — it rides in the body.
func runtimeOpenAIURL(endpoints *schemas.BedrockEndpoints, region, path string) string {
	return fmt.Sprintf("https://%s/openai/v1/%s", resolveBedrockHost(endpoints, bedrockServiceRuntime, region), path)
}

// Guardrail headers for the OpenAI-compatible surfaces. Converse takes the same intent
// as a guardrailConfig body field; these endpoints take it as headers and ignore the body
// field entirely, so the two renderings are not interchangeable.
const (
	guardrailIdentifierHeader = "X-Amzn-Bedrock-GuardrailIdentifier"
	guardrailVersionHeader    = "X-Amzn-Bedrock-GuardrailVersion"
	guardrailTraceHeader      = "X-Amzn-Bedrock-Trace"
)

// withGuardrailHeaders returns headers naming the guardrail in the request's
// guardrailConfig extra param, which is how bedrock-runtime's OpenAI-compatible
// endpoints take it. Converse expresses the same intent as a body field and these
// endpoints ignore that field, so the two renderings are not interchangeable.
//
// Identifier and version are both required upstream, so a config carrying one is left
// alone rather than half-sent. streamProcessingMode has no header equivalent.
//
// The headers are not signed: AWS requires only x-amz-* in SignedHeaders and these are
// x-amzn-*. Verified live — a guardrail applies identically either way.
//
// Mantle is deliberately not wired: it accepts these headers and enforces nothing.
func withGuardrailHeaders(base map[string]string, extraParams map[string]any) map[string]string {
	config, _ := extraParams["guardrailConfig"].(map[string]any)
	identifier, _ := config["guardrailIdentifier"].(string)
	version, _ := config["guardrailVersion"].(string)
	if identifier == "" || version == "" {
		return base
	}

	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string, 3)
	}
	setHeader(out, guardrailIdentifierHeader, identifier)
	setHeader(out, guardrailVersionHeader, version)
	if trace, _ := config["trace"].(string); trace != "" {
		setHeader(out, guardrailTraceHeader, trace)
	}
	return out
}

// setHeader assigns name, first dropping any key that differs from it only in case.
// SetExtraHeaders canonicalises every key and keeps whichever it reaches first, and Go
// map order is random, so a differently-cased entry would race this one rather than
// lose to it.
func setHeader(headers map[string]string, name, value string) {
	for existing := range headers {
		if existing != name && strings.EqualFold(existing, name) {
			delete(headers, existing)
		}
	}
	headers[name] = value
}

// runtimeResponses handles non-streaming Responses requests on bedrock-runtime's
// OpenAI-compatible surface. Payloads and SSE follow the OpenAI Responses spec, so the shared
// OpenAI handler does the work and only the URL and SigV4 scope differ from mantle.
func (provider *BedrockProvider) runtimeResponses(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	url := runtimeOpenAIURL(bedrockEndpoints(key.BedrockKeyConfig), region, "responses")
	_, request.Model = parseBedrockRegionAndModel(request.Model)
	extraHeaders := provider.networkConfig.ExtraHeaders
	if request.Params != nil {
		extraHeaders = withGuardrailHeaders(extraHeaders, request.Params.ExtraParams)
	}

	// SigV4 (empty key value): sign the exact body the handler builds via a signer closure.
	// Bearer (key has a value): no signer; auth flows through the Authorization header.
	var signer providerUtils.BodySigner
	if key.Value.GetValue() == "" {
		signer = func(body []byte) (map[string]string, *schemas.BifrostError) {
			return signOpenAIV4Headers(ctx, body, url, "application/json", key, region, extraHeaders, bedrockSigningService)
		}
	}

	// bedrock-runtime is not project-scoped, so no project header is sent here.
	return openai.HandleOpenAIResponsesRequest(
		ctx,
		provider.mantleClient,
		url,
		request,
		openai.BearerAuthHeader(key),
		extraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		nil,
		signer,
		provider.logger,
	)
}

// runtimeResponsesStream handles streaming Responses requests on bedrock-runtime's
// OpenAI-compatible surface.
func (provider *BedrockProvider) runtimeResponsesStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	url := runtimeOpenAIURL(bedrockEndpoints(key.BedrockKeyConfig), region, "responses")
	_, request.Model = parseBedrockRegionAndModel(request.Model)
	extraHeaders := provider.networkConfig.ExtraHeaders
	if request.Params != nil {
		extraHeaders = withGuardrailHeaders(extraHeaders, request.Params.ExtraParams)
	}

	var signer providerUtils.BodySigner
	if key.Value.GetValue() == "" {
		signer = func(body []byte) (map[string]string, *schemas.BifrostError) {
			return signOpenAIV4Headers(ctx, body, url, "text/event-stream", key, region, extraHeaders, bedrockSigningService)
		}
	}

	return openai.HandleOpenAIResponsesStreaming(
		ctx, provider.mantleStreamingClient, url, request,
		openai.BearerAuthHeader(key), extraHeaders,
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

// runtimeChatCompletions handles non-streaming chat requests on bedrock-runtime's
// OpenAI-compatible surface. Reached only when the operator opts in.
func (provider *BedrockProvider) runtimeChatCompletions(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	url := runtimeOpenAIURL(bedrockEndpoints(key.BedrockKeyConfig), region, "chat/completions")
	_, request.Model = parseBedrockRegionAndModel(request.Model)
	extraHeaders := provider.networkConfig.ExtraHeaders
	if request.Params != nil {
		extraHeaders = withGuardrailHeaders(extraHeaders, request.Params.ExtraParams)
	}

	var signer providerUtils.BodySigner
	if key.Value.GetValue() == "" {
		signer = func(body []byte) (map[string]string, *schemas.BifrostError) {
			return signOpenAIV4Headers(ctx, body, url, "application/json", key, region, extraHeaders, bedrockSigningService)
		}
	}

	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.mantleClient,
		url,
		request,
		openai.BearerAuthHeader(key),
		extraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		nil,
		signer,
		provider.logger,
	)
}

// runtimeChatCompletionsStream handles streaming chat requests on bedrock-runtime's
// OpenAI-compatible surface.
func (provider *BedrockProvider) runtimeChatCompletionsStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	url := runtimeOpenAIURL(bedrockEndpoints(key.BedrockKeyConfig), region, "chat/completions")
	_, request.Model = parseBedrockRegionAndModel(request.Model)
	extraHeaders := provider.networkConfig.ExtraHeaders
	if request.Params != nil {
		extraHeaders = withGuardrailHeaders(extraHeaders, request.Params.ExtraParams)
	}

	var signer providerUtils.BodySigner
	if key.Value.GetValue() == "" {
		signer = func(body []byte) (map[string]string, *schemas.BifrostError) {
			return signOpenAIV4Headers(ctx, body, url, "text/event-stream", key, region, extraHeaders, bedrockSigningService)
		}
	}

	return openai.HandleOpenAIChatCompletionStreaming(
		ctx, provider.mantleStreamingClient, url, request,
		openai.BearerAuthHeader(key), extraHeaders,
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
