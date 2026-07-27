// Package bedrockmantle implements the Bedrock Mantle LLM provider. It owns the Bedrock Mantle
// surface served on the bedrock-mantle.{region}.api.aws host: Claude models via the native
// Anthropic Messages API (/anthropic/v1/messages), and OpenAI-family (gpt-*) and Gemma models
// via the OpenAI-compatible API (/v1 or /openai/v1). The model id is sent verbatim, save for an
// optional leading "region/" addressing prefix. Authentication is either a Bedrock Mantle API key
// (Authorization: Bearer) or AWS SigV4 for the bedrock-mantle service.
package bedrockmantle

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	openai "github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// BedrockMantleProvider implements the Provider interface for the Bedrock Mantle endpoint.
type BedrockMantleProvider struct {
	logger                schemas.Logger        // Logger for provider operations
	mantleClient          *fasthttp.Client      // fasthttp client for unary requests (OpenAI-compatible and native-Anthropic paths)
	mantleStreamingClient *fasthttp.Client      // fasthttp streaming client for streaming requests
	networkConfig         schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawRequest    bool                  // Whether to include raw request in BifrostResponse
	sendBackRawResponse   bool                  // Whether to include raw response in BifrostResponse
}

// NewBedrockMantleProvider creates a new Bedrock Mantle provider instance.
// It initializes the fasthttp unary and streaming clients with the provided configuration.
// There is no default BaseURL: mantle requests target computed bedrock-mantle.{region}.api.aws hosts.
func NewBedrockMantleProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*BedrockMantleProvider, error) {
	config.CheckAndSetDefaults()

	requestTimeout := time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)

	// fasthttp clients for Bedrock Mantle (shared by OpenAI-compatible and native-Anthropic paths).
	// ReadTimeout is the shared provider request timeout; oversized Anthropic responses are handled
	// by PrepareResponseStreaming, not by these static settings.
	mantleFasthttpClient := &fasthttp.Client{
		ReadTimeout:         requestTimeout,
		WriteTimeout:        requestTimeout,
		MaxConnsPerHost:     config.NetworkConfig.MaxConnsPerHost,
		MaxIdleConnDuration: time.Second * time.Duration(config.NetworkConfig.KeepAliveTimeoutInSeconds),
		MaxConnWaitTimeout:  requestTimeout,
		MaxConnDuration:     time.Second * time.Duration(schemas.DefaultMaxConnDurationInSeconds),
		ConnPoolStrategy:    fasthttp.FIFO,
	}
	mantleFasthttpClient = providerUtils.ConfigureProxy(mantleFasthttpClient, config.ProxyConfig, logger)
	mantleFasthttpClient = providerUtils.ConfigureDialer(mantleFasthttpClient, config.NetworkConfig.AllowPrivateNetwork)
	mantleFasthttpClient = providerUtils.ConfigureTLS(mantleFasthttpClient, config.NetworkConfig, logger)
	mantleStreamingFasthttpClient := providerUtils.BuildStreamingClient(mantleFasthttpClient)

	return &BedrockMantleProvider{
		logger:                logger,
		mantleClient:          mantleFasthttpClient,
		mantleStreamingClient: mantleStreamingFasthttpClient,
		networkConfig:         config.NetworkConfig,
		sendBackRawRequest:    config.SendBackRawRequest,
		sendBackRawResponse:   config.SendBackRawResponse,
	}, nil
}

// mantleAnthropicVersion is the Anthropic API version sent as an HTTP header on the
// Bedrock Mantle native-Anthropic endpoint (unlike bedrock-runtime, which carries the
// version as an "anthropic_version" body field).
const mantleAnthropicVersion = "2023-06-01"

// defaultMantleRegion is the fallback AWS region used to build the bedrock-mantle host when
// no region is supplied by the model prefix, the resolved alias, or the key config.
const defaultMantleRegion = "us-east-1"

// mantleOpenAIURL builds the Bedrock Mantle OpenAI-compatible endpoint URL for the given
// region, model, and API path (e.g. "chat/completions", "responses"). The native-Anthropic
// path is built separately by mantleAnthropicURL. Pass the canonical (capability-resolved)
// model for correct path gating; the request body still carries the wire request.Model.
// Frontier families (closed gpt-5.x, Gemma 4) live under the "openai/v1" base path; gpt-oss
// uses the bare "v1" path.
func mantleOpenAIURL(region, model, path string) string {
	base := "v1"
	if strings.Contains(model, "gpt-5") || strings.Contains(model, "gemma-4") {
		base = "openai/v1"
	}
	return fmt.Sprintf("https://bedrock-mantle.%s.api.aws/%s/%s", region, base, path)
}

// mantleAnthropicURL builds the Bedrock Mantle native-Anthropic Messages endpoint URL.
func mantleAnthropicURL(region string) string {
	return fmt.Sprintf("https://bedrock-mantle.%s.api.aws/anthropic/v1/messages", region)
}

// mantleSigner returns a BodySigner that SigV4-signs the request body for the bedrock-mantle
// service, or nil when a Bedrock Mantle API key is present (auth then flows through the
// Authorization: Bearer header instead). The handler invokes the signer on the exact body it
// builds, so the signature always covers what is actually sent.
func (provider *BedrockMantleProvider) mantleSigner(ctx *schemas.BifrostContext, key schemas.Key, url, accept, region string) providerUtils.BodySigner {
	if key.Value.GetValue() != "" {
		return nil
	}
	return func(body []byte) (map[string]string, *schemas.BifrostError) {
		return bedrock.SignMantleV4Headers(ctx, body, url, accept, key, region, provider.networkConfig.ExtraHeaders)
	}
}

// GetProviderKey returns the provider identifier for Bedrock Mantle.
func (provider *BedrockMantleProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.BedrockMantle
}

// listModelsByKey lists models from the Bedrock Mantle (OpenAI-compatible) /v1/models
// endpoint for a single key, converted to a Bifrost response with the key's allow/blacklist/
// alias gating. The request is signed as it is sent (the GET cannot reuse the POST signer);
// a Bedrock Mantle API key authenticates via Authorization: Bearer, otherwise the request is
// SigV4-signed for the bedrock-mantle service and the signed headers are merged into the
// per-request extra headers consumed by the shared OpenAI list-models path.
func (provider *BedrockMantleProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	region := provider.resolveRegion(ctx, key, "")
	mURL := mantleOpenAIURL(region, "", "models")

	// Scope the catalog to the configured project via the OpenAI-Project header (default project
	// when unset). It is a plain header, so it does not need to be part of the SigV4 SignedHeaders.
	extraHeaders := bedrock.WithMantleProject(provider.networkConfig.ExtraHeaders, bedrock.MantleOpenAIProjectHeader, resolveProjectID(ctx, key))
	if key.Value.GetValue() == "" {
		// SigV4: sign the GET and overlay the signed headers; OpenAI's ListModelsByKey only sets
		// a Bearer header when the key carries a value, so the SigV4 Authorization wins here.
		sigHeaders, bifrostErr := bedrock.SignMantleV4Headers(ctx, nil, mURL, "", key, region, provider.networkConfig.ExtraHeaders)
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		merged := make(map[string]string, len(extraHeaders)+len(sigHeaders))
		maps.Copy(merged, extraHeaders)
		maps.Copy(merged, sigHeaders)
		extraHeaders = merged
	}

	return openai.ListModelsByKey(
		ctx,
		provider.mantleClient,
		mURL,
		key,
		request.Unfiltered,
		extraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
	)
}

// ListModels lists models from the Bedrock Mantle OpenAI-compatible /v1/models endpoint,
// aggregating across the supplied keys.
func (provider *BedrockMantleProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
	)
}

// ChatCompletion performs a chat completion request to the Bedrock Mantle endpoint, dispatching
// by model family: Anthropic-family (Claude) models use the native Anthropic Messages surface;
// all other (OpenAI-family / Gemma) models use the OpenAI-compatible surface.
func (provider *BedrockMantleProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	region := provider.resolveRegion(ctx, key, request.Model)

	// Anthropic-family models (Claude) use the native Anthropic Messages surface; all other
	// (OpenAI-family / Gemma) models use the OpenAI-compatible surface.
	if schemas.IsAnthropicModelFamily(ctx, request.Model) {
		url := mantleAnthropicURL(region)
		_, bareModel := parseBedrockRegionAndModel(request.Model)
		return anthropic.HandleAnthropicChatCompletionRequest(
			ctx,
			provider.mantleClient,
			url,
			request,
			anthropic.AnthropicRequestBuildConfig{
				Provider:                  schemas.BedrockMantle,
				Model:                     bareModel,
				BetaHeaderOverrides:       provider.networkConfig.BetaHeaderOverrides,
				ShouldSendBackRawRequest:  provider.sendBackRawRequest,
				ShouldSendBackRawResponse: provider.sendBackRawResponse,
			},
			openai.BearerAuthHeader(key),
			addAnthropicHeaders(bedrock.WithMantleProject(provider.networkConfig.ExtraHeaders, bedrock.MantleAnthropicProjectHeader, resolveProjectID(ctx, key))),
			provider.mantleSigner(ctx, key, url, "application/json", region),
			provider.logger,
		)
	}

	url := mantleOpenAIURL(region, schemas.ResolveCanonicalModel(ctx, request.Model), "chat/completions")
	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.mantleClient,
		url,
		request,
		openai.BearerAuthHeader(key),
		bedrock.WithMantleProject(provider.networkConfig.ExtraHeaders, bedrock.MantleOpenAIProjectHeader, resolveProjectID(ctx, key)),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		nil,
		provider.mantleSigner(ctx, key, url, "application/json", region),
		provider.logger,
	)
}

// ChatCompletionStream performs a streaming chat completion request to the Bedrock Mantle
// endpoint, dispatching by model family (native Anthropic vs OpenAI-compatible).
func (provider *BedrockMantleProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	region := provider.resolveRegion(ctx, key, request.Model)

	// Anthropic-family models (Claude) use the native Anthropic Messages surface; all other
	// (OpenAI-family / Gemma) models use the OpenAI-compatible surface.
	if schemas.IsAnthropicModelFamily(ctx, request.Model) {
		url := mantleAnthropicURL(region)

		_, bareModel := parseBedrockRegionAndModel(request.Model)
		jsonData, bifrostErr := anthropic.BuildAnthropicChatRequestBody(ctx, request, anthropic.AnthropicRequestBuildConfig{
			Provider:                  schemas.BedrockMantle,
			Model:                     bareModel,
			IsStreaming:               true,
			BetaHeaderOverrides:       provider.networkConfig.BetaHeaderOverrides,
			ShouldSendBackRawRequest:  provider.sendBackRawRequest,
			ShouldSendBackRawResponse: provider.sendBackRawResponse,
		})
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		return anthropic.HandleAnthropicChatCompletionStreaming(
			ctx,
			provider.mantleStreamingClient,
			url,
			jsonData,
			openai.BearerAuthHeader(key),
			addAnthropicHeaders(bedrock.WithMantleProject(provider.networkConfig.ExtraHeaders, bedrock.MantleAnthropicProjectHeader, resolveProjectID(ctx, key))),
			provider.networkConfig.StreamIdleTimeoutInSeconds,
			provider.networkConfig.BetaHeaderOverrides,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			provider.GetProviderKey(),
			postHookRunner,
			nil,
			provider.mantleSigner(ctx, key, url, "text/event-stream", region),
			provider.logger,
			postHookSpanFinalizer,
		)
	}

	url := mantleOpenAIURL(region, schemas.ResolveCanonicalModel(ctx, request.Model), "chat/completions")
	return openai.HandleOpenAIChatCompletionStreaming(
		ctx, provider.mantleStreamingClient, url, request,
		openai.BearerAuthHeader(key), bedrock.WithMantleProject(provider.networkConfig.ExtraHeaders, bedrock.MantleOpenAIProjectHeader, resolveProjectID(ctx, key)),
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(), postHookRunner,
		nil, nil, nil, nil, nil,
		provider.mantleSigner(ctx, key, url, "text/event-stream", region),
		provider.logger, postHookSpanFinalizer,
	)
}

// Responses performs a Responses API request to the Bedrock Mantle endpoint, dispatching by
// model family (native Anthropic vs OpenAI-compatible).
func (provider *BedrockMantleProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	region := provider.resolveRegion(ctx, key, request.Model)

	// Anthropic-family models (Claude) use the native Anthropic Messages surface; all other
	// (OpenAI-family / Gemma) models use the OpenAI-compatible surface.
	if schemas.IsAnthropicModelFamily(ctx, request.Model) {
		url := mantleAnthropicURL(region)

		_, bareModel := parseBedrockRegionAndModel(request.Model)
		return anthropic.HandleAnthropicResponsesRequest(
			ctx,
			provider.mantleClient,
			url,
			request,
			anthropic.AnthropicRequestBuildConfig{
				Provider:                  schemas.BedrockMantle,
				Model:                     bareModel,
				ValidateTools:             true,
				BetaHeaderOverrides:       provider.networkConfig.BetaHeaderOverrides,
				ShouldSendBackRawRequest:  provider.sendBackRawRequest,
				ShouldSendBackRawResponse: provider.sendBackRawResponse,
			},
			openai.BearerAuthHeader(key),
			addAnthropicHeaders(bedrock.WithMantleProject(provider.networkConfig.ExtraHeaders, bedrock.MantleAnthropicProjectHeader, resolveProjectID(ctx, key))),
			provider.mantleSigner(ctx, key, url, "application/json", region),
			provider.logger,
		)
	}

	url := mantleOpenAIURL(region, schemas.ResolveCanonicalModel(ctx, request.Model), "responses")
	return openai.HandleOpenAIResponsesRequest(
		ctx,
		provider.mantleClient,
		url,
		request,
		openai.BearerAuthHeader(key),
		bedrock.WithMantleProject(provider.networkConfig.ExtraHeaders, bedrock.MantleOpenAIProjectHeader, resolveProjectID(ctx, key)),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil, nil,
		provider.mantleSigner(ctx, key, url, "application/json", region),
		provider.logger,
	)
}

// ResponsesStream performs a streaming Responses API request to the Bedrock Mantle endpoint,
// dispatching by model family (native Anthropic vs OpenAI-compatible).
func (provider *BedrockMantleProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	region := provider.resolveRegion(ctx, key, request.Model)

	// Anthropic-family models (Claude) use the native Anthropic Messages surface; all other
	// (OpenAI-family / Gemma) models use the OpenAI-compatible surface.
	if schemas.IsAnthropicModelFamily(ctx, request.Model) {
		url := mantleAnthropicURL(region)

		_, bareModel := parseBedrockRegionAndModel(request.Model)
		jsonData, bifrostErr := anthropic.BuildAnthropicResponsesRequestBody(ctx, request, anthropic.AnthropicRequestBuildConfig{
			Provider:                  schemas.BedrockMantle,
			Model:                     bareModel,
			IsStreaming:               true,
			ValidateTools:             true,
			BetaHeaderOverrides:       provider.networkConfig.BetaHeaderOverrides,
			ShouldSendBackRawRequest:  provider.sendBackRawRequest,
			ShouldSendBackRawResponse: provider.sendBackRawResponse,
		})
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		return anthropic.HandleAnthropicResponsesStream(
			ctx,
			provider.mantleStreamingClient,
			url,
			jsonData,
			openai.BearerAuthHeader(key),
			addAnthropicHeaders(bedrock.WithMantleProject(provider.networkConfig.ExtraHeaders, bedrock.MantleAnthropicProjectHeader, resolveProjectID(ctx, key))),
			provider.networkConfig.StreamIdleTimeoutInSeconds,
			provider.networkConfig.BetaHeaderOverrides,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			provider.GetProviderKey(),
			postHookRunner,
			nil,
			provider.mantleSigner(ctx, key, url, "text/event-stream", region),
			provider.logger,
			postHookSpanFinalizer,
		)
	}

	url := mantleOpenAIURL(region, schemas.ResolveCanonicalModel(ctx, request.Model), "responses")
	return openai.HandleOpenAIResponsesStreaming(
		ctx, provider.mantleStreamingClient, url, request,
		openai.BearerAuthHeader(key), bedrock.WithMantleProject(provider.networkConfig.ExtraHeaders, bedrock.MantleOpenAIProjectHeader, resolveProjectID(ctx, key)),
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(), postHookRunner,
		nil, nil, nil, nil,
		provider.mantleSigner(ctx, key, url, "text/event-stream", region),
		provider.logger, postHookSpanFinalizer,
	)
}

// TextCompletion is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

// TextCompletionStream is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// Embedding is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
}

// Speech is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

// Rerank is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.RerankRequest, provider.GetProviderKey())
}

// OCR is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) OCR(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostOCRRequest) (*schemas.BifrostOCRResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.OCRRequest, provider.GetProviderKey())
}

// SpeechStream is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

// Transcription is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
}

// TranscriptionStream is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

// ImageGeneration is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationRequest, provider.GetProviderKey())
}

// ImageGenerationStream is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, provider.GetProviderKey())
}

// ImageEdit is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) ImageEdit(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditRequest, provider.GetProviderKey())
}

// ImageEditStream is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) ImageEditStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditStreamRequest, provider.GetProviderKey())
}

// ImageVariation is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) ImageVariation(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, provider.GetProviderKey())
}

// VideoGeneration is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) VideoGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoGenerationRequest, provider.GetProviderKey())
}

// VideoRetrieve is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) VideoRetrieve(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRetrieveRequest, provider.GetProviderKey())
}

// VideoDownload is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) VideoDownload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDownloadRequest, provider.GetProviderKey())
}

// VideoDelete is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) VideoDelete(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDeleteRequest, provider.GetProviderKey())
}

// VideoList is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) VideoList(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoListRequest, provider.GetProviderKey())
}

// VideoRemix is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, provider.GetProviderKey())
}

// FileUpload is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) FileUpload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, provider.GetProviderKey())
}

// FileList is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) FileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, provider.GetProviderKey())
}

// FileRetrieve is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) FileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, provider.GetProviderKey())
}

// FileDelete is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) FileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, provider.GetProviderKey())
}

// FileContent is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) FileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, provider.GetProviderKey())
}

// BatchCreate is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) BatchCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, provider.GetProviderKey())
}

// BatchList is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, provider.GetProviderKey())
}

// BatchRetrieve is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, provider.GetProviderKey())
}

// BatchCancel is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, provider.GetProviderKey())
}

// BatchDelete is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) BatchDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, provider.GetProviderKey())
}

// BatchResults is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, provider.GetProviderKey())
}

// CountTokens is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) CountTokens(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, provider.GetProviderKey())
}

// Compaction is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) Compaction(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostCompactionRequest) (*schemas.BifrostCompactionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CompactionRequest, provider.GetProviderKey())
}

// CachedContentCreate is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) CachedContentCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostCachedContentCreateRequest) (*schemas.BifrostCachedContentCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentCreateRequest, provider.GetProviderKey())
}

// CachedContentList is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) CachedContentList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostCachedContentListRequest) (*schemas.BifrostCachedContentListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentListRequest, provider.GetProviderKey())
}

// CachedContentRetrieve is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) CachedContentRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostCachedContentRetrieveRequest) (*schemas.BifrostCachedContentRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentRetrieveRequest, provider.GetProviderKey())
}

// CachedContentUpdate is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) CachedContentUpdate(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostCachedContentUpdateRequest) (*schemas.BifrostCachedContentUpdateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentUpdateRequest, provider.GetProviderKey())
}

// CachedContentDelete is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) CachedContentDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostCachedContentDeleteRequest) (*schemas.BifrostCachedContentDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentDeleteRequest, provider.GetProviderKey())
}

// ContainerCreate is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

// ContainerList is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

// ContainerDelete is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

// ContainerFileCreate is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

// ContainerFileList is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

// ContainerFileRetrieve is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

// ContainerFileContent is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

// ContainerFileDelete is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}

// Passthrough is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) Passthrough(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughRequest, provider.GetProviderKey())
}

// PassthroughStream is not supported by the Bedrock Mantle provider.
func (provider *BedrockMantleProvider) PassthroughStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughStreamRequest, provider.GetProviderKey())
}
