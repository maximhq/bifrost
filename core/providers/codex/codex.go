package codex

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const defaultBaseURL = "https://chatgpt.com/backend-api/codex"
const defaultInstructions = "You are Codex, a helpful coding assistant. Follow the user's request exactly."

var defaultModels = []string{
	"gpt-5.1-codex",
	"gpt-5.1-codex-max",
	"gpt-5.1-codex-mini",
	"gpt-5.2",
	"gpt-5.2-codex",
	"gpt-5.3-codex",
	"gpt-5.4",
	"gpt-5.4-mini",
}

type CodexProvider struct {
	logger              schemas.Logger
	client              *fasthttp.Client
	networkConfig       schemas.NetworkConfig
	sendBackRawRequest  bool
	sendBackRawResponse bool
	config              *schemas.CodexConfig
}

func NewCodexProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*CodexProvider, error) {
	config.CheckAndSetDefaults()

	requestTimeout := time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)
	client := &fasthttp.Client{
		ReadTimeout:         requestTimeout,
		WriteTimeout:        requestTimeout,
		MaxConnsPerHost:     config.NetworkConfig.MaxConnsPerHost,
		MaxIdleConnDuration: 30 * time.Second,
		MaxConnWaitTimeout:  requestTimeout,
		MaxConnDuration:     time.Second * time.Duration(schemas.DefaultMaxConnDurationInSeconds),
		ConnPoolStrategy:    fasthttp.FIFO,
	}

	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client)
	client = providerUtils.ConfigureTLS(client, config.NetworkConfig, logger)

	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = defaultBaseURL
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &CodexProvider{
		logger:              logger,
		client:              client,
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
		config:              config.CodexConfig,
	}, nil
}

func (provider *CodexProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Codex
}

func (provider *CodexProvider) buildRequestURL(path string) string {
	return provider.networkConfig.BaseURL + path
}

func (provider *CodexProvider) authHeaders(ctx *schemas.BifrostContext, key schemas.Key) (map[string]string, *schemas.BifrostError) {
	headers := map[string]string{}
	if key.CodexKeyConfig != nil {
		if key.CodexKeyConfig.AccessToken != nil {
			if token := strings.TrimSpace(key.CodexKeyConfig.AccessToken.GetValue()); token != "" && !accessTokenExpired(key.CodexKeyConfig.AccessTokenExpiresAt) {
				headers["Authorization"] = "Bearer " + token
			}
		}
		if _, ok := headers["Authorization"]; !ok {
			if refreshToken := strings.TrimSpace(key.CodexKeyConfig.RefreshToken.GetValue()); refreshToken != "" {
				requestCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				tokens, err := RefreshAccessToken(requestCtx, &http.Client{Timeout: 20 * time.Second}, refreshToken)
				if err != nil {
					statusCode := http.StatusBadGateway
					message := fmt.Sprintf("failed to refresh Codex access token: %v", err)
					return nil, &schemas.BifrostError{IsBifrostError: true, StatusCode: &statusCode, Error: &schemas.ErrorField{Message: message}, ExtraFields: schemas.BifrostErrorExtraFields{Provider: provider.GetProviderKey()}}
				}
				headers["Authorization"] = "Bearer " + tokens.AccessToken
				accountID := ExtractAccountID(tokens)
				if accountID != "" {
					headers["ChatGPT-Account-Id"] = accountID
				}
				provider.persistRefreshedCredentials(ctx, key, tokens, accountID)
			}
		}
		if key.CodexKeyConfig.AccountID != nil {
			if accountID := strings.TrimSpace(key.CodexKeyConfig.AccountID.GetValue()); accountID != "" {
				if _, ok := headers["ChatGPT-Account-Id"]; !ok {
					headers["ChatGPT-Account-Id"] = accountID
				}
			}
		}
	}
	if _, ok := headers["Authorization"]; !ok {
		if token := strings.TrimSpace(key.Value.GetValue()); token != "" {
			headers["Authorization"] = "Bearer " + token
		}
	}
	if _, ok := headers["Authorization"]; !ok {
		statusCode := http.StatusUnauthorized
		message := "Codex provider requires an authenticated key with a refresh token or access token"
		return nil, &schemas.BifrostError{IsBifrostError: true, StatusCode: &statusCode, Error: &schemas.ErrorField{Message: message}, ExtraFields: schemas.BifrostErrorExtraFields{Provider: provider.GetProviderKey()}}
	}
	if _, ok := headers["User-Agent"]; !ok {
		headers["User-Agent"] = "Bifrost Codex Provider"
	}
	if _, ok := headers["originator"]; !ok {
		headers["originator"] = "opencode"
	}
	if _, ok := headers["session_id"]; !ok {
		requestID := uuid.NewString()
		if ctx != nil {
			if value, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string); ok && strings.TrimSpace(value) != "" {
				requestID = value
			}
		}
		headers["session_id"] = requestID
	}
	return headers, nil
}

func (provider *CodexProvider) persistRefreshedCredentials(ctx *schemas.BifrostContext, key schemas.Key, tokens *TokenResponse, accountID string) {
	if ctx == nil || tokens == nil || key.ID == "" {
		return
	}
	persister, ok := ctx.Value(schemas.BifrostContextKeyCodexCredentialPersister).(schemas.CodexCredentialPersister)
	if !ok || persister == nil {
		return
	}
	refreshValue := tokens.RefreshToken
	if strings.TrimSpace(refreshValue) == "" && key.CodexKeyConfig != nil {
		refreshValue = key.CodexKeyConfig.RefreshToken.GetValue()
	}
	refreshedConfig := &schemas.CodexKeyConfig{
		RefreshToken:         *schemas.NewEnvVar(refreshValue),
		AccessToken:          schemas.NewEnvVar(tokens.AccessToken),
		AccessTokenExpiresAt: schemas.Ptr(ExpiresAtFromNow(tokens.ExpiresIn)),
		AuthMethod:           schemas.CodexAuthMethodDevice,
	}
	if key.CodexKeyConfig != nil && key.CodexKeyConfig.AuthMethod != "" {
		refreshedConfig.AuthMethod = key.CodexKeyConfig.AuthMethod
	}
	if accountID != "" {
		refreshedConfig.AccountID = schemas.NewEnvVar(accountID)
	} else if key.CodexKeyConfig != nil && key.CodexKeyConfig.AccountID != nil {
		refreshedConfig.AccountID = key.CodexKeyConfig.AccountID
	}
	if err := persister(key.ID, refreshedConfig); err != nil && provider.logger != nil {
		provider.logger.Warn("failed to persist refreshed Codex credentials for key %s: %v", key.ID, err)
	}
}

func (provider *CodexProvider) responsesRequestFromChat(request *schemas.BifrostChatRequest) *schemas.BifrostResponsesRequest {
	if len(request.Input) == 0 {
		return request.ToResponsesRequest()
	}

	instructions := make([]string, 0, 2)
	remainingIndex := 0
	for idx, message := range request.Input {
		if message.Role == "system" || message.Role == "developer" {
			if text := extractChatMessageText(message); strings.TrimSpace(text) != "" {
				instructions = append(instructions, text)
				remainingIndex = idx + 1
				continue
			}
		}
		break
	}

	clone := *request
	clone.Input = request.Input[remainingIndex:]
	responsesRequest := clone.ToResponsesRequest()
	ensureCodexResponseDefaults(nil, responsesRequest)
	if len(instructions) > 0 {
		if responsesRequest.Params == nil {
			responsesRequest.Params = &schemas.ResponsesParameters{}
		}
		joined := strings.Join(instructions, "\n\n")
		responsesRequest.Params.Instructions = &joined
	}
	return responsesRequest
}

func (provider *CodexProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	ownedBy := string(provider.GetProviderKey())
	data := make([]schemas.Model, 0, len(defaultModels))
	for _, model := range defaultModels {
		modelID := model
		data = append(data, schemas.Model{ID: modelID, OwnedBy: &ownedBy})
	}
	return &schemas.BifrostListModelsResponse{Data: data}, nil
}

func (provider *CodexProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	response, err := provider.accumulateResponsesRequest(ctx, key, provider.responsesRequestFromChat(request))
	if err != nil {
		return nil, err
	}
	return response.ToBifrostChatResponse(), nil
}

func (provider *CodexProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	innerStream, bifrostErr := provider.ResponsesStream(ctx, codexNoOpPostHookRunner, key, provider.responsesRequestFromChat(request))
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)
	state := newCodexChatStreamState(ctx, request.Model)
	go func() {
		defer close(responseChan)
		for chunk := range innerStream {
			if chunk == nil {
				continue
			}
			if chunk.BifrostError != nil {
				bifrostErr := *chunk.BifrostError
				bifrostErr.ExtraFields.RequestType = schemas.ChatCompletionStreamRequest
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, &bifrostErr, responseChan, provider.logger)
				return
			}
			if chunk.BifrostResponsesStreamResponse == nil {
				continue
			}
			for _, chatChunk := range state.convert(chunk.BifrostResponsesStreamResponse) {
				if chatChunk == nil {
					continue
				}
				if chatChunk.Choices != nil && len(chatChunk.Choices) > 0 && chatChunk.Choices[0].FinishReason != nil {
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				}
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, chatChunk, nil, nil, nil, nil), responseChan)
			}
		}
	}()
	return responseChan, nil
}

func (provider *CodexProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	return provider.accumulateResponsesRequest(ctx, key, request)
}

func (provider *CodexProvider) sendResponsesRequest(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	ensureCodexResponseDefaults(ctx, request)
	normalizeCodexInput(request)
	authHeaders, bifrostErr := provider.authHeaders(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return openai.HandleOpenAIResponsesRequest(
		ctx,
		provider.client,
		provider.buildRequestURL("/responses"),
		request,
		key,
		mergeExtraHeaders(provider.networkConfig.ExtraHeaders, authHeaders),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		provider.parseError,
		provider.logger,
	)
}

func (provider *CodexProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	ensureCodexResponseDefaults(ctx, request)
	normalizeCodexInput(request)
	authHeaders, bifrostErr := provider.authHeaders(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return openai.HandleOpenAIResponsesStreaming(
		ctx,
		provider.client,
		provider.buildRequestURL("/responses"),
		request,
		authHeaders,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		provider.parseError,
		nil,
		nil,
		provider.logger,
	)
}

func (provider *CodexProvider) parseError(resp *fasthttp.Response, requestType schemas.RequestType, providerName schemas.ModelProvider, model string) *schemas.BifrostError {
	bifrostErr := openai.ParseOpenAIError(resp, requestType, providerName, model)
	if bifrostErr != nil && bifrostErr.Error != nil && bifrostErr.Error.Message == "provider API error (status 400)" {
		if body := strings.TrimSpace(string(resp.Body())); body != "" {
			bifrostErr.Error.Message = bifrostErr.Error.Message + ": " + body
		}
	}
	if bifrostErr != nil && bifrostErr.Error != nil && bifrostErr.Error.Code != nil && *bifrostErr.Error.Code == "usage_not_included" {
		bifrostErr.Error.Message = bifrostErr.Error.Message + " Visit https://chatgpt.com/#pricing to upgrade your ChatGPT plan for Codex usage."
	}
	return bifrostErr
}

func (provider *CodexProvider) CountTokens(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.RerankRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) OCR(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostOCRRequest) (*schemas.BifrostOCRResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.OCRRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) ImageEdit(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) ImageEditStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditStreamRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) ImageVariation(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) VideoGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoGenerationRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) VideoRetrieve(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRetrieveRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) VideoDownload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDownloadRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) VideoDelete(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDeleteRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) VideoList(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoListRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) BatchCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) BatchDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) FileUpload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) FileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) FileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) FileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) FileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}

func mergeExtraHeaders(extraHeaders map[string]string, authHeaders map[string]string) map[string]string {
	if len(authHeaders) == 0 {
		return extraHeaders
	}
	headers := make(map[string]string, len(extraHeaders)+len(authHeaders))
	if len(extraHeaders) > 0 {
		maps.Copy(headers, extraHeaders)
	}
	maps.Copy(headers, authHeaders)
	return headers
}

func extractChatMessageText(message schemas.ChatMessage) string {
	if message.Content == nil {
		return ""
	}
	if message.Content.ContentStr != nil {
		return *message.Content.ContentStr
	}
	parts := make([]string, 0, len(message.Content.ContentBlocks))
	for _, block := range message.Content.ContentBlocks {
		if block.Text != nil && strings.TrimSpace(*block.Text) != "" {
			parts = append(parts, *block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func accessTokenExpired(expiresAt *string) bool {
	if expiresAt == nil || strings.TrimSpace(*expiresAt) == "" {
		return true
	}
	parsed, err := time.Parse(time.RFC3339, *expiresAt)
	if err != nil {
		return true
	}
	return time.Now().After(parsed.Add(-30 * time.Second))
}

func ensureCodexResponseDefaults(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest) {
	if request == nil {
		return
	}
	if request.Params == nil {
		request.Params = &schemas.ResponsesParameters{}
	}
	stripCodexUnsupportedParams(request.Params)
	store := false
	request.Params.Store = &store
	if request.Params.Instructions == nil || strings.TrimSpace(*request.Params.Instructions) == "" {
		instructions := defaultInstructions
		request.Params.Instructions = &instructions
	}
	if request.Params.PromptCacheKey == nil {
		promptCacheKey := uuid.NewString()
		if ctx != nil {
			if value, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string); ok && strings.TrimSpace(value) != "" {
				promptCacheKey = value
			}
		}
		request.Params.PromptCacheKey = &promptCacheKey
	}
}

func stripCodexUnsupportedParams(params *schemas.ResponsesParameters) {
	if params == nil {
		return
	}

	// Codex rejects explicit tuning/limit fields that are valid for the public OpenAI API.
	// Keep the request working by stripping only the parameters we verified against the upstream endpoint.
	params.Temperature = nil
	params.TopP = nil
	params.MaxOutputTokens = nil
	if len(params.ExtraParams) == 0 {
		return
	}
	delete(params.ExtraParams, "temperature")
	delete(params.ExtraParams, "top_p")
	delete(params.ExtraParams, "max_output_tokens")
	delete(params.ExtraParams, "presence_penalty")
	delete(params.ExtraParams, "frequency_penalty")
}

func normalizeCodexInput(request *schemas.BifrostResponsesRequest) {
	if request == nil || len(request.Input) == 0 {
		return
	}
	for idx := range request.Input {
		message := request.Input[idx]
		role := ""
		if message.Role != nil {
			role = string(*message.Role)
		}
		if message.Content == nil {
			continue
		}
		if message.Content.ContentStr != nil {
			text := strings.TrimSpace(*message.Content.ContentStr)
			blockType := codexTextBlockTypeForRole(role)
			block := schemas.ResponsesMessageContentBlock{
				Type: blockType,
				Text: &text,
			}
			if blockType == schemas.ResponsesOutputMessageContentTypeText {
				block.ResponsesOutputMessageContentText = &schemas.ResponsesOutputMessageContentText{}
			}
			message.Content = &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{block},
			}
			request.Input[idx] = message
			continue
		}
		if len(message.Content.ContentBlocks) == 0 {
			continue
		}
		blocks := make([]schemas.ResponsesMessageContentBlock, 0, len(message.Content.ContentBlocks))
		for _, block := range message.Content.ContentBlocks {
			if block.Text != nil {
				block.Type = codexTextBlockTypeForRole(role)
				if block.Type == schemas.ResponsesOutputMessageContentTypeText && block.ResponsesOutputMessageContentText == nil {
					block.ResponsesOutputMessageContentText = &schemas.ResponsesOutputMessageContentText{}
				}
			}
			blocks = append(blocks, block)
		}
		message.Content = &schemas.ResponsesMessageContent{ContentBlocks: blocks}
		request.Input[idx] = message
	}
}

func codexTextBlockTypeForRole(role string) schemas.ResponsesMessageContentBlockType {
	switch role {
	case string(schemas.ResponsesInputMessageRoleAssistant):
		return schemas.ResponsesOutputMessageContentTypeText
	default:
		return schemas.ResponsesInputMessageContentBlockTypeText
	}
}

func (provider *CodexProvider) accumulateResponsesRequest(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	streamCtx := cloneBifrostContext(ctx)
	stream, bifrostErr := provider.ResponsesStream(streamCtx, codexNoOpPostHookRunner, key, request)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	accumulator := newCodexResponsesAccumulator(request.Model)
	for chunk := range stream {
		if chunk == nil {
			continue
		}
		if chunk.BifrostError != nil {
			return nil, chunk.BifrostError
		}
		if chunk.BifrostResponsesStreamResponse == nil {
			continue
		}
		accumulator.add(chunk.BifrostResponsesStreamResponse)
		if isFinalCodexResponsesChunk(chunk.BifrostResponsesStreamResponse) {
			return accumulator.response(), nil
		}
	}

	statusCode := http.StatusBadGateway
	message := "codex stream ended before a final response was accumulated"
	return nil, &schemas.BifrostError{IsBifrostError: true, StatusCode: &statusCode, Error: &schemas.ErrorField{Message: message}, ExtraFields: schemas.BifrostErrorExtraFields{Provider: provider.GetProviderKey(), ModelRequested: request.Model, RequestType: schemas.ResponsesRequest}}
}

func cloneBifrostContext(parent *schemas.BifrostContext) *schemas.BifrostContext {
	if parent == nil {
		return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}
	deadline, ok := parent.Deadline()
	if !ok {
		deadline = schemas.NoDeadline
	}
	return schemas.NewBifrostContext(parent, deadline)
}

func isFinalCodexResponsesChunk(resp *schemas.BifrostResponsesStreamResponse) bool {
	if resp == nil {
		return false
	}
	switch resp.Type {
	case schemas.ResponsesStreamResponseTypeCompleted, schemas.ResponsesStreamResponseTypeFailed, schemas.ResponsesStreamResponseTypeIncomplete:
		return true
	default:
		return false
	}
}

type codexResponsesAccumulator struct {
	latest      *schemas.BifrostResponsesResponse
	itemsBySlot map[int]schemas.ResponsesMessage
	model       string
}

type codexChatStreamState struct {
	id         string
	model      string
	created    int
	chunkIndex int
}

func newCodexResponsesAccumulator(model string) *codexResponsesAccumulator {
	return &codexResponsesAccumulator{
		itemsBySlot: make(map[int]schemas.ResponsesMessage),
		model:       model,
	}
}

func newCodexChatStreamState(ctx *schemas.BifrostContext, requestedModel string) *codexChatStreamState {
	id := uuid.NewString()
	if ctx != nil {
		if requestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string); ok && strings.TrimSpace(requestID) != "" {
			id = requestID
		}
	}
	return &codexChatStreamState{
		id:         id,
		model:      requestedModel,
		created:    int(time.Now().Unix()),
		chunkIndex: -1,
	}
}

func codexNoOpPostHookRunner(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, err
}

func (s *codexChatStreamState) convert(resp *schemas.BifrostResponsesStreamResponse) []*schemas.BifrostChatResponse {
	if resp == nil {
		return nil
	}
	if resp.Response != nil {
		if resp.Response.ID != nil && *resp.Response.ID != "" {
			s.id = *resp.Response.ID
		}
		if resp.Response.Model != "" {
			s.model = resp.Response.Model
		}
		if resp.Response.CreatedAt > 0 {
			s.created = resp.Response.CreatedAt
		}
	}

	switch resp.Type {
	case schemas.ResponsesStreamResponseTypeOutputItemAdded:
		return s.outputItemAdded(resp)
	case schemas.ResponsesStreamResponseTypeOutputTextDelta:
		if resp.Delta == nil {
			return nil
		}
		return []*schemas.BifrostChatResponse{s.chunk(&schemas.ChatStreamResponseChoiceDelta{Content: resp.Delta}, nil, nil)}
	case schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta:
		if resp.Delta == nil {
			return nil
		}
		return []*schemas.BifrostChatResponse{s.chunk(&schemas.ChatStreamResponseChoiceDelta{Reasoning: resp.Delta}, nil, nil)}
	case schemas.ResponsesStreamResponseTypeRefusalDelta:
		if resp.Refusal == nil {
			return nil
		}
		return []*schemas.BifrostChatResponse{s.chunk(&schemas.ChatStreamResponseChoiceDelta{Refusal: resp.Refusal}, nil, nil)}
	case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta:
		if resp.Delta == nil {
			return nil
		}
		toolType := "function"
		toolCall := schemas.ChatAssistantMessageToolCall{
			Index:    uint16(valueOrZero(resp.OutputIndex)),
			Type:     &toolType,
			ID:       resp.ItemID,
			Function: schemas.ChatAssistantMessageToolCallFunction{Arguments: *resp.Delta},
		}
		return []*schemas.BifrostChatResponse{s.chunk(&schemas.ChatStreamResponseChoiceDelta{ToolCalls: []schemas.ChatAssistantMessageToolCall{toolCall}}, nil, nil)}
	case schemas.ResponsesStreamResponseTypeCompleted:
		finishReason := s.finishReason(resp)
		usage := responsesUsageToChatUsage(resp.Response)
		return []*schemas.BifrostChatResponse{s.chunk(&schemas.ChatStreamResponseChoiceDelta{}, &finishReason, usage)}
	case schemas.ResponsesStreamResponseTypeIncomplete:
		finishReason := "length"
		usage := responsesUsageToChatUsage(resp.Response)
		return []*schemas.BifrostChatResponse{s.chunk(&schemas.ChatStreamResponseChoiceDelta{}, &finishReason, usage)}
	case schemas.ResponsesStreamResponseTypeFailed:
		finishReason := "stop"
		usage := responsesUsageToChatUsage(resp.Response)
		return []*schemas.BifrostChatResponse{s.chunk(&schemas.ChatStreamResponseChoiceDelta{}, &finishReason, usage)}
	default:
		return nil
	}
}

func (s *codexChatStreamState) outputItemAdded(resp *schemas.BifrostResponsesStreamResponse) []*schemas.BifrostChatResponse {
	if resp.Item == nil {
		return nil
	}
	responses := make([]*schemas.BifrostChatResponse, 0, 2)
	if resp.Item.Role != nil && *resp.Item.Role == schemas.ResponsesInputMessageRoleAssistant {
		role := "assistant"
		responses = append(responses, s.chunk(&schemas.ChatStreamResponseChoiceDelta{Role: &role}, nil, nil))
	}
	if resp.Item.Type != nil && *resp.Item.Type == schemas.ResponsesMessageTypeFunctionCall && resp.Item.ResponsesToolMessage != nil {
		toolType := "function"
		toolCall := schemas.ChatAssistantMessageToolCall{
			Index: uint16(valueOrZero(resp.OutputIndex)),
			Type:  &toolType,
			ID:    resp.Item.CallID,
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      resp.Item.Name,
				Arguments: "",
			},
		}
		responses = append(responses, s.chunk(&schemas.ChatStreamResponseChoiceDelta{ToolCalls: []schemas.ChatAssistantMessageToolCall{toolCall}}, nil, nil))
	}
	return responses
}

func (s *codexChatStreamState) chunk(delta *schemas.ChatStreamResponseChoiceDelta, finishReason *string, usage *schemas.BifrostLLMUsage) *schemas.BifrostChatResponse {
	s.chunkIndex++
	return &schemas.BifrostChatResponse{
		ID:      s.id,
		Object:  "chat.completion.chunk",
		Created: s.created,
		Model:   s.model,
		Usage:   usage,
		Choices: []schemas.BifrostResponseChoice{{
			Index:                    0,
			ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: delta},
			FinishReason:             finishReason,
		}},
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    schemas.ChatCompletionStreamRequest,
			Provider:       schemas.Codex,
			ModelRequested: s.model,
			ChunkIndex:     s.chunkIndex,
		},
	}
}

func (s *codexChatStreamState) finishReason(resp *schemas.BifrostResponsesStreamResponse) string {
	if resp != nil && resp.Response != nil {
		for _, item := range resp.Response.Output {
			if item.Type != nil && *item.Type == schemas.ResponsesMessageTypeFunctionCall {
				return "tool_calls"
			}
		}
	}
	return "stop"
}

func responsesUsageToChatUsage(resp *schemas.BifrostResponsesResponse) *schemas.BifrostLLMUsage {
	if resp == nil || resp.Usage == nil {
		return nil
	}
	return resp.Usage.ToBifrostLLMUsage()
}

func valueOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func (a *codexResponsesAccumulator) add(event *schemas.BifrostResponsesStreamResponse) {
	if event == nil {
		return
	}
	if event.Response != nil {
		copy := *event.Response
		a.latest = &copy
		if copy.Model != "" {
			a.model = copy.Model
		}
	}
	if event.Item != nil && event.OutputIndex != nil && event.Type == schemas.ResponsesStreamResponseTypeOutputItemDone {
		a.itemsBySlot[*event.OutputIndex] = *event.Item
	}
}

func (a *codexResponsesAccumulator) response() *schemas.BifrostResponsesResponse {
	resp := &schemas.BifrostResponsesResponse{
		Object: "response",
		Model:  a.model,
	}
	if a.latest != nil {
		copy := *a.latest
		resp = &copy
	}
	if len(a.itemsBySlot) > 0 {
		indices := make([]int, 0, len(a.itemsBySlot))
		for index := range a.itemsBySlot {
			indices = append(indices, index)
		}
		slices.Sort(indices)
		output := make([]schemas.ResponsesMessage, 0, len(indices))
		for _, index := range indices {
			output = append(output, a.itemsBySlot[index])
		}
		resp.Output = output
	}
	if resp.Model == "" {
		resp.Model = a.model
	}
	if resp.Object == "" {
		resp.Object = "response"
	}
	return resp
}

func (provider *CodexProvider) Passthrough(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughRequest, provider.GetProviderKey())
}

func (provider *CodexProvider) PassthroughStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughStreamRequest, provider.GetProviderKey())
}
