// Package copilot implements the GitHub Copilot provider and its utility functions.
package copilot

import (
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// CopilotProvider implements the Provider interface for GitHub Copilot's API.
type CopilotProvider struct {
	logger              schemas.Logger
	client              *fasthttp.Client
	networkConfig       schemas.NetworkConfig
	sendBackRawRequest  bool
	sendBackRawResponse bool
	tokenManagers       sync.Map // map[string]*CopilotTokenManagerEntry keyed by Key.ID
}

// NewCopilotProvider creates a new Copilot provider instance.
func NewCopilotProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*CopilotProvider, error) {
	config.CheckAndSetDefaults()

	client := &fasthttp.Client{
		ReadTimeout:         time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:        time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost:     5000,
		MaxIdleConnDuration: 30 * time.Second,
		MaxConnWaitTimeout:  10 * time.Second,
	}

	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client)

	return &CopilotProvider{
		logger:              logger,
		client:              client,
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for Copilot.
func (provider *CopilotProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Copilot
}

// CopilotTokenManagerEntry pairs a copilotTokenManager with the OAuth access token it was created for.
// Storing the token value allows detecting key rotations so stale managers are replaced.
type CopilotTokenManagerEntry struct {
	tm          *copilotTokenManager
	accessToken string
}

// getOrCreateTokenManager returns the token manager for the given key, creating one if needed.
// If the key's OAuth token has changed since the last call (rotation or re-auth), the old manager
// is replaced so that revoked credentials are not reused.
func (provider *CopilotProvider) getOrCreateTokenManager(key schemas.Key) *copilotTokenManager {
	currentToken := key.Value.GetValue()
	if val, ok := provider.tokenManagers.Load(key.ID); ok {
		entry := val.(*CopilotTokenManagerEntry)
		if entry.accessToken == currentToken {
			return entry.tm
		}
		// OAuth token has been rotated — fall through to create a fresh manager
	}
	tm := newCopilotTokenManager(currentToken, provider.client, provider.logger)
	provider.tokenManagers.Store(key.ID, &CopilotTokenManagerEntry{tm: tm, accessToken: currentToken})
	return tm
}

// getCopilotAuth resolves the Copilot JWT and builds auth headers + API base URL.
func (provider *CopilotProvider) getCopilotAuth(key schemas.Key) (authHeader map[string]string, apiBase string, err *schemas.BifrostError) {
	tm := provider.getOrCreateTokenManager(key)
	token, base, bifrostErr := tm.getToken()
	if bifrostErr != nil {
		return nil, "", bifrostErr
	}

	headers := make(map[string]string, len(copilotRequiredHeaders)+1)
	headers["Authorization"] = "Bearer " + token
	for k, v := range copilotRequiredHeaders {
		headers[k] = v
	}

	return headers, base, nil
}

// mergedExtraHeaders returns the provider's networkConfig.ExtraHeaders combined with copilotRequiredHeaders.
func (provider *CopilotProvider) mergedExtraHeaders() map[string]string {
	merged := make(map[string]string, len(provider.networkConfig.ExtraHeaders)+len(copilotRequiredHeaders))
	for k, v := range provider.networkConfig.ExtraHeaders {
		merged[k] = v
	}
	for k, v := range copilotRequiredHeaders {
		merged[k] = v
	}
	return merged
}

// copilotKey returns a copy of the key with the value replaced by the Copilot JWT.
func copilotKey(key schemas.Key, token string) schemas.Key {
	k := key
	k.Value = *schemas.NewEnvVar(token)
	return k
}

// ListModels performs a list models request to the Copilot API.
func (provider *CopilotProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if len(keys) == 0 {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			StatusCode:     intPtr(401),
			Error: &schemas.ErrorField{
				Message: "no keys configured for copilot provider",
			},
		}
	}

	// Use keys[0] for the API base URL (the models endpoint is the same across all accounts).
	tm0 := provider.getOrCreateTokenManager(keys[0])
	_, apiBase, bifrostErr := tm0.getToken()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Resolve a Copilot JWT per key so that each key carries its own valid token.
	copilotKeys := make([]schemas.Key, len(keys))
	for i, k := range keys {
		tm := provider.getOrCreateTokenManager(k)
		token, _, perKeyErr := tm.getToken()
		if perKeyErr != nil {
			return nil, perKeyErr
		}
		copilotKeys[i] = copilotKey(k, token)
	}

	return openai.HandleOpenAIListModelsRequest(
		ctx,
		provider.client,
		request,
		apiBase+providerUtils.GetPathFromContext(ctx, "/models"),
		copilotKeys,
		provider.mergedExtraHeaders(),
		schemas.Copilot,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
	)
}

// ChatCompletion performs a chat completion request to the Copilot API.
func (provider *CopilotProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	tm := provider.getOrCreateTokenManager(key)
	token, apiBase, bifrostErr := tm.getToken()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.client,
		apiBase+providerUtils.GetPathFromContext(ctx, "/chat/completions"),
		request,
		copilotKey(key, token),
		provider.mergedExtraHeaders(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		nil,
		provider.logger,
	)
}

// ChatCompletionStream performs a streaming chat completion request to the Copilot API.
func (provider *CopilotProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	authHeader, apiBase, bifrostErr := provider.getCopilotAuth(key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return openai.HandleOpenAIChatCompletionStreaming(
		ctx,
		provider.client,
		apiBase+providerUtils.GetPathFromContext(ctx, "/chat/completions"),
		request,
		authHeader,
		provider.mergedExtraHeaders(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		schemas.Copilot,
		postHookRunner,
		nil,
		nil,
		nil,
		nil,
		nil,
		provider.logger,
	)
}

// Responses performs a responses request to the Copilot API.
func (provider *CopilotProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	chatResponse, err := provider.ChatCompletion(ctx, key, request.ToChatRequest())
	if err != nil {
		return nil, err
	}

	response := chatResponse.ToBifrostResponsesResponse()
	response.ExtraFields.RequestType = schemas.ResponsesRequest
	response.ExtraFields.Provider = provider.GetProviderKey()
	response.ExtraFields.ModelRequested = request.Model

	return response, nil
}

// ResponsesStream performs a streaming responses request to the Copilot API.
func (provider *CopilotProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)
	return provider.ChatCompletionStream(ctx, postHookRunner, key, request.ToChatRequest())
}

// TextCompletion is not supported by the Copilot provider.
func (provider *CopilotProvider) TextCompletion(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

// TextCompletionStream is not supported by the Copilot provider.
func (provider *CopilotProvider) TextCompletionStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ schemas.Key, _ *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// Embedding is not supported by the Copilot provider.
func (provider *CopilotProvider) Embedding(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
}

// Rerank is not supported by the Copilot provider.
func (provider *CopilotProvider) Rerank(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.RerankRequest, provider.GetProviderKey())
}

// Speech is not supported by the Copilot provider.
func (provider *CopilotProvider) Speech(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

// SpeechStream is not supported by the Copilot provider.
func (provider *CopilotProvider) SpeechStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ schemas.Key, _ *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

// Transcription is not supported by the Copilot provider.
func (provider *CopilotProvider) Transcription(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
}

// TranscriptionStream is not supported by the Copilot provider.
func (provider *CopilotProvider) TranscriptionStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ schemas.Key, _ *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

// ImageGeneration is not supported by the Copilot provider.
func (provider *CopilotProvider) ImageGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationRequest, provider.GetProviderKey())
}

// ImageGenerationStream is not supported by the Copilot provider.
func (provider *CopilotProvider) ImageGenerationStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ schemas.Key, _ *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, provider.GetProviderKey())
}

// ImageEdit is not supported by the Copilot provider.
func (provider *CopilotProvider) ImageEdit(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditRequest, provider.GetProviderKey())
}

// ImageEditStream is not supported by the Copilot provider.
func (provider *CopilotProvider) ImageEditStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ schemas.Key, _ *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditStreamRequest, provider.GetProviderKey())
}

// ImageVariation is not supported by the Copilot provider.
func (provider *CopilotProvider) ImageVariation(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, provider.GetProviderKey())
}

// VideoGeneration is not supported by the Copilot provider.
func (provider *CopilotProvider) VideoGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoGenerationRequest, provider.GetProviderKey())
}

// VideoRetrieve is not supported by the Copilot provider.
func (provider *CopilotProvider) VideoRetrieve(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRetrieveRequest, provider.GetProviderKey())
}

// VideoDownload is not supported by the Copilot provider.
func (provider *CopilotProvider) VideoDownload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDownloadRequest, provider.GetProviderKey())
}

// VideoDelete is not supported by the Copilot provider.
func (provider *CopilotProvider) VideoDelete(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDeleteRequest, provider.GetProviderKey())
}

// VideoList is not supported by the Copilot provider.
func (provider *CopilotProvider) VideoList(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoListRequest, provider.GetProviderKey())
}

// VideoRemix is not supported by the Copilot provider.
func (provider *CopilotProvider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, provider.GetProviderKey())
}

// CountTokens is not supported by the Copilot provider.
func (provider *CopilotProvider) CountTokens(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, provider.GetProviderKey())
}

// BatchCreate is not supported by the Copilot provider.
func (provider *CopilotProvider) BatchCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, provider.GetProviderKey())
}

// BatchList is not supported by the Copilot provider.
func (provider *CopilotProvider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, provider.GetProviderKey())
}

// BatchRetrieve is not supported by the Copilot provider.
func (provider *CopilotProvider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, provider.GetProviderKey())
}

// BatchCancel is not supported by the Copilot provider.
func (provider *CopilotProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, provider.GetProviderKey())
}

// BatchDelete is not supported by the Copilot provider.
func (provider *CopilotProvider) BatchDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, provider.GetProviderKey())
}

// BatchResults is not supported by the Copilot provider.
func (provider *CopilotProvider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, provider.GetProviderKey())
}

// FileUpload is not supported by the Copilot provider.
func (provider *CopilotProvider) FileUpload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, provider.GetProviderKey())
}

// FileList is not supported by the Copilot provider.
func (provider *CopilotProvider) FileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, provider.GetProviderKey())
}

// FileRetrieve is not supported by the Copilot provider.
func (provider *CopilotProvider) FileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, provider.GetProviderKey())
}

// FileDelete is not supported by the Copilot provider.
func (provider *CopilotProvider) FileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, provider.GetProviderKey())
}

// FileContent is not supported by the Copilot provider.
func (provider *CopilotProvider) FileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, provider.GetProviderKey())
}

// ContainerCreate is not supported by the Copilot provider.
func (provider *CopilotProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

// ContainerList is not supported by the Copilot provider.
func (provider *CopilotProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve is not supported by the Copilot provider.
func (provider *CopilotProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

// ContainerDelete is not supported by the Copilot provider.
func (provider *CopilotProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

// ContainerFileCreate is not supported by the Copilot provider.
func (provider *CopilotProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

// ContainerFileList is not supported by the Copilot provider.
func (provider *CopilotProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

// ContainerFileRetrieve is not supported by the Copilot provider.
func (provider *CopilotProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

// ContainerFileContent is not supported by the Copilot provider.
func (provider *CopilotProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

// ContainerFileDelete is not supported by the Copilot provider.
func (provider *CopilotProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}

// Passthrough is not supported by the Copilot provider.
func (provider *CopilotProvider) Passthrough(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughRequest, provider.GetProviderKey())
}

// PassthroughStream is not supported by the Copilot provider.
func (provider *CopilotProvider) PassthroughStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughStreamRequest, provider.GetProviderKey())
}
