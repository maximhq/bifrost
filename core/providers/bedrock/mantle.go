package bedrock

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"

	openai "github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

const (
	// MantleOpenAIProjectHeader selects a Bedrock Mantle project on the OpenAI-compatible surface
	// (chat/completions, responses, /models). AWS routes to the account's default project when absent.
	MantleOpenAIProjectHeader = "OpenAI-Project"
	// MantleAnthropicProjectHeader selects a Bedrock Mantle project on the native-Anthropic surface
	// (/anthropic/v1/messages).
	MantleAnthropicProjectHeader = "anthropic-workspace-id"
)

// WithMantleProject returns headers with the given Mantle project header set when projectID is
// non-empty, letting AWS fall back to the account's default project when it is empty. It never
// mutates base (which may be the shared networkConfig.ExtraHeaders map). The project header is a
// plain (non x-amz-*) header, so it does not need to be part of the SigV4 SignedHeaders.
func WithMantleProject(base map[string]string, headerName, projectID string) map[string]string {
	if projectID == "" {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string, 1)
	}
	out[headerName] = projectID
	return out
}

// isMantleModel reports whether a model should be routed via the Bedrock Mantle
// OpenAI-compatible endpoint. OpenAI-family (gpt-*) and Gemma 4 models are mantle-only
// (they have no Converse equivalent). Gemma 3 is intentionally excluded: it only supports
// Chat (not Responses) on mantle, and the Converse path serves both APIs, so forcing it to
// mantle would break Responses.
//
// Deprecated: in-provider Bedrock Mantle routing is retained for backwards compatibility.
// New configurations should use the "bedrock_mantle" provider, which owns the Bedrock Mantle
// surface (Claude native-Anthropic, OpenAI-compatible, and Gemma).
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

// SignMantleV4Headers computes SigV4 auth headers for a mantle request by signing a dummy
// net/http.Request. jsonData must be the exact bytes that will be sent. accept must match
// the Accept header the actual request will send, since SigV4 signs all request headers.
// extraHeaders are the static headers the actual request will carry; any x-amz-* among them
// are added to the canonical request before signing so the signature covers them (AWS requires
// every x-amz-* header on the wire to be signed, otherwise verification fails).
func SignMantleV4Headers(
	ctx *schemas.BifrostContext,
	jsonData []byte,
	requestURL, accept string,
	key schemas.Key,
	region string,
	extraHeaders map[string]string,
) (map[string]string, *schemas.BifrostError) {
	method := http.MethodPost
	if jsonData == nil {
		method = http.MethodGet
	}
	var body io.Reader
	if jsonData != nil {
		body = bytes.NewReader(jsonData)
	}

	req, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to create signing request", err)
	}
	req.Header.Set("Accept", accept)
	for k, v := range extraHeaders {
		if strings.HasPrefix(strings.ToLower(k), "x-amz-") {
			req.Header.Set(k, v)
		}
	}

	keyCfg := key.BedrockKeyConfig
	// Create a synthetic BedrockKeyConfig in case of bedrock_mantle: its
	// BedrockMantleKeyConfig carries the same credential fields, so map them across
	// (Region is passed separately; ARN/batch config are not used for signing).
	if key.BedrockMantleKeyConfig != nil && key.BedrockKeyConfig == nil {
		keyCfg = &schemas.BedrockKeyConfig{
			AccessKey:       key.BedrockMantleKeyConfig.AccessKey,
			SecretKey:       key.BedrockMantleKeyConfig.SecretKey,
			SessionToken:    key.BedrockMantleKeyConfig.SessionToken,
			RoleARN:         key.BedrockMantleKeyConfig.RoleARN,
			ExternalID:      key.BedrockMantleKeyConfig.ExternalID,
			RoleSessionName: key.BedrockMantleKeyConfig.RoleSessionName,
		}
	}
	if bifrostErr := signAWSRequest(ctx, req, keyCfg, region, bedrockMantleSigningService); bifrostErr != nil {
		return nil, bifrostErr
	}
	// Return the headers exactly as signed: signAWSRequest defaults an empty Accept/Content-Type
	// to "application/json" and includes them in SignedHeaders, so the caller must send those same
	// values (not the original, possibly-empty, accept) or the signature won't match.
	headers := map[string]string{
		"Authorization":        req.Header.Get("Authorization"),
		"X-Amz-Date":           req.Header.Get("X-Amz-Date"),
		"x-amz-content-sha256": req.Header.Get("x-amz-content-sha256"),
		"Accept":               req.Header.Get("Accept"),
		"Content-Type":         req.Header.Get("Content-Type"),
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
			return SignMantleV4Headers(ctx, body, url, "application/json", key, region, provider.networkConfig.ExtraHeaders)
		}
	}

	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.mantleClient,
		url,
		request,
		openai.BearerAuthHeader(key),
		WithMantleProject(provider.networkConfig.ExtraHeaders, MantleOpenAIProjectHeader, resolveMantleProjectID(ctx, key)),
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
			return SignMantleV4Headers(ctx, body, url, "text/event-stream", key, region, provider.networkConfig.ExtraHeaders)
		}
	}

	return openai.HandleOpenAIChatCompletionStreaming(
		ctx, provider.mantleStreamingClient, url, request,
		openai.BearerAuthHeader(key), WithMantleProject(provider.networkConfig.ExtraHeaders, MantleOpenAIProjectHeader, resolveMantleProjectID(ctx, key)),
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
			return SignMantleV4Headers(ctx, body, url, "application/json", key, region, provider.networkConfig.ExtraHeaders)
		}
	}

	return openai.HandleOpenAIResponsesRequest(
		ctx,
		provider.mantleClient,
		url,
		request,
		openai.BearerAuthHeader(key),
		WithMantleProject(provider.networkConfig.ExtraHeaders, MantleOpenAIProjectHeader, resolveMantleProjectID(ctx, key)),
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
			return SignMantleV4Headers(ctx, body, url, "text/event-stream", key, region, provider.networkConfig.ExtraHeaders)
		}
	}

	return openai.HandleOpenAIResponsesStreaming(
		ctx, provider.mantleStreamingClient, url, request,
		openai.BearerAuthHeader(key), WithMantleProject(provider.networkConfig.ExtraHeaders, MantleOpenAIProjectHeader, resolveMantleProjectID(ctx, key)),
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
