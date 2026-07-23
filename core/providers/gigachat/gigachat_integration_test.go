package gigachat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

const (
	gigaChatIntegrationDefaultChatModel      = "GigaChat-2"
	gigaChatIntegrationDefaultEmbeddingModel = "Embeddings"
	gigaChatIntegrationTimeout               = 90 * time.Second
)

type gigaChatIntegrationConfig struct {
	baseURL         string
	authURL         string
	scope           string
	chatModel       string
	embeddingModel  string
	reasoningModel  string
	reasoningEffort string
	batchModel      string
	webSearchModel  string
	hasAccessToken  bool
	hasOAuth        bool
	hasPassword     bool
	runBatch        bool
	runWebSearch    bool
	caBundleFile    string
	certFile        string
	keyFile         string
}

func TestGigaChatIntegration(t *testing.T) {
	config := loadGigaChatIntegrationConfig(t)
	provider := newGigaChatIntegrationProvider(t, config)
	key := config.inferenceKey()

	t.Run("OAuthTokenFetch", func(t *testing.T) {
		if !config.hasOAuth {
			t.Skip("set GIGACHAT_CREDENTIALS and GIGACHAT_SCOPE to run OAuth token integration test")
		}
		ctx := newGigaChatIntegrationContext(t)
		token, bifrostErr := provider.getOAuthAccessToken(ctx, config.oauthKey())
		if bifrostErr != nil {
			failGigaChatIntegrationBifrostError(t, "OAuth token fetch", bifrostErr)
		}
		if strings.TrimSpace(token) == "" {
			t.Fatal("OAuth token fetch returned an empty access token")
		}
	})

	t.Run("PasswordTokenFetch", func(t *testing.T) {
		if !config.hasPassword {
			t.Skip("set GIGACHAT_USER, GIGACHAT_PASSWORD, and GIGACHAT_BASE_URL to run password token integration test")
		}
		ctx := newGigaChatIntegrationContext(t)
		token, bifrostErr := provider.getPasswordAccessToken(ctx, config.passwordKey())
		if bifrostErr != nil {
			failGigaChatIntegrationBifrostError(t, "password token fetch", bifrostErr)
		}
		if strings.TrimSpace(token) == "" {
			t.Fatal("password token fetch returned an empty access token")
		}
	})

	t.Run("ChatCompletion", func(t *testing.T) {
		ctx := newGigaChatIntegrationContext(t)
		response, bifrostErr := provider.ChatCompletion(ctx, key, gigaChatIntegrationChatRequest(config.chatModel))
		if bifrostErr != nil {
			failGigaChatIntegrationBifrostError(t, "chat completion", bifrostErr)
		}
		if response == nil || len(response.Choices) == 0 {
			t.Fatal("chat completion returned no choices")
		}
		if response.ExtraFields.Provider != schemas.GigaChat {
			t.Fatalf("provider mismatch: got %q, want %q", response.ExtraFields.Provider, schemas.GigaChat)
		}
	})

	t.Run("ChatCompletionStream", func(t *testing.T) {
		ctx := newGigaChatIntegrationContext(t)
		stream, bifrostErr := provider.ChatCompletionStream(ctx, testGigaChatPostHookRunner, nil, key, gigaChatIntegrationChatRequest(config.chatModel))
		if bifrostErr != nil {
			failGigaChatIntegrationBifrostError(t, "chat completion stream", bifrostErr)
		}
		assertGigaChatIntegrationChatStream(t, stream)
	})

	t.Run("ListModels", func(t *testing.T) {
		ctx := newGigaChatIntegrationContext(t)
		response, bifrostErr := provider.ListModels(ctx, []schemas.Key{key}, &schemas.BifrostListModelsRequest{Provider: schemas.GigaChat})
		if bifrostErr != nil {
			failGigaChatIntegrationBifrostError(t, "list models", bifrostErr)
		}
		if response == nil || len(response.Data) == 0 {
			t.Fatal("list models returned no models")
		}
	})

	t.Run("Files", func(t *testing.T) {
		runGigaChatIntegrationFileLifecycle(t, provider, key)
	})

	t.Run("Embedding", func(t *testing.T) {
		ctx := newGigaChatIntegrationContext(t)
		response, bifrostErr := provider.Embedding(ctx, key, gigaChatIntegrationEmbeddingRequest(config.embeddingModel))
		if bifrostErr != nil {
			failGigaChatIntegrationBifrostError(t, "embedding", bifrostErr)
		}
		if response == nil || len(response.Data) == 0 {
			t.Fatal("embedding returned no vectors")
		}
		if response.ExtraFields.Provider != schemas.GigaChat {
			t.Fatalf("provider mismatch: got %q, want %q", response.ExtraFields.Provider, schemas.GigaChat)
		}
	})

	t.Run("Responses", func(t *testing.T) {
		ctx := newGigaChatIntegrationContext(t)
		response, bifrostErr := provider.Responses(ctx, key, gigaChatIntegrationResponsesRequest(config.chatModel))
		if bifrostErr != nil {
			failGigaChatIntegrationBifrostError(t, "responses", bifrostErr)
		}
		if response == nil || len(response.Output) == 0 {
			t.Fatal("responses returned no output")
		}
		if response.ExtraFields.Provider != schemas.GigaChat {
			t.Fatalf("provider mismatch: got %q, want %q", response.ExtraFields.Provider, schemas.GigaChat)
		}
	})

	t.Run("CountTokens", func(t *testing.T) {
		ctx := newGigaChatIntegrationContext(t)
		response, bifrostErr := provider.CountTokens(ctx, key, gigaChatIntegrationCountTokensRequest(config.chatModel))
		if bifrostErr != nil {
			failGigaChatIntegrationBifrostError(t, "count tokens", bifrostErr)
		}
		if response == nil {
			t.Fatal("count tokens returned nil response")
		}
		if response.InputTokens <= 0 {
			t.Fatalf("count tokens returned invalid input token count: %#v", response)
		}
		if response.TotalTokens == nil || *response.TotalTokens < response.InputTokens {
			t.Fatalf("count tokens returned invalid total tokens: %#v", response)
		}
		if len(response.Tokens) != 2 {
			t.Fatalf("count tokens returned unexpected per-input counts: %#v", response.Tokens)
		}
		if response.ExtraFields.Provider != schemas.GigaChat {
			t.Fatalf("provider mismatch: got %q, want %q", response.ExtraFields.Provider, schemas.GigaChat)
		}
	})

	t.Run("ResponsesStream", func(t *testing.T) {
		ctx := newGigaChatIntegrationContext(t)
		stream, bifrostErr := provider.ResponsesStream(ctx, testGigaChatPostHookRunner, nil, key, gigaChatIntegrationResponsesRequest(config.chatModel))
		if bifrostErr != nil {
			failGigaChatIntegrationBifrostError(t, "responses stream", bifrostErr)
		}
		assertGigaChatIntegrationResponsesStream(t, stream)
	})

	t.Run("ResponsesReasoning", func(t *testing.T) {
		if config.reasoningModel == "" {
			t.Skip("set GIGACHAT_REASONING_MODEL to run Responses reasoning integration test")
		}
		ctx := newGigaChatIntegrationContext(t)
		response, bifrostErr := provider.Responses(ctx, key, gigaChatIntegrationReasoningRequest(config.reasoningModel, config.reasoningEffort))
		if bifrostErr != nil {
			failGigaChatIntegrationBifrostError(t, "responses reasoning", bifrostErr)
		}
		if response == nil || len(response.Output) == 0 {
			t.Fatal("responses reasoning returned no output")
		}
		if !gigaChatIntegrationHasReasoningOutput(response) {
			t.Skip("GigaChat reasoning model did not return a reasoning output item for the smoke prompt")
		}
	})

	t.Run("FunctionToolOptionalSchema", func(t *testing.T) {
		ctx := newGigaChatIntegrationContext(t)
		response, bifrostErr := provider.ChatCompletion(ctx, key, gigaChatIntegrationOptionalToolRequest(t, config.chatModel))
		if bifrostErr != nil {
			failGigaChatIntegrationBifrostError(t, "function tool with optional schema", bifrostErr)
		}
		if !gigaChatIntegrationHasToolCall(response) {
			t.Skip("GigaChat did not return a tool call for the forced tool request")
		}
	})

	t.Run("BatchCreateRetrieve", func(t *testing.T) {
		if !config.runBatch {
			t.Skip("set GIGACHAT_ENABLE_BATCH_TEST=1 to run GigaChat batch integration test")
		}
		ctx := newGigaChatIntegrationContext(t)
		response, bifrostErr := provider.BatchCreate(ctx, key, gigaChatIntegrationBatchCreateRequest(config.batchModel))
		if bifrostErr != nil {
			failGigaChatIntegrationBifrostError(t, "batch create", bifrostErr)
		}
		if response == nil || strings.TrimSpace(response.ID) == "" {
			t.Fatalf("batch create returned no batch ID: %#v", response)
		}

		retrieveResponse, bifrostErr := provider.BatchRetrieve(ctx, []schemas.Key{key}, &schemas.BifrostBatchRetrieveRequest{
			Provider: schemas.GigaChat,
			BatchID:  response.ID,
		})
		if bifrostErr != nil {
			failGigaChatIntegrationBifrostError(t, "batch retrieve", bifrostErr)
		}
		if retrieveResponse == nil || retrieveResponse.ID != response.ID {
			t.Fatalf("batch retrieve response mismatch: got %#v, want ID %q", retrieveResponse, response.ID)
		}
	})

	t.Run("WebSearchBuiltIn", func(t *testing.T) {
		if !config.runWebSearch {
			t.Skip("set GIGACHAT_ENABLE_WEB_SEARCH_TEST=1 to run GigaChat web search integration test")
		}
		ctx := newGigaChatIntegrationContext(t)
		response, bifrostErr := provider.Responses(ctx, key, gigaChatIntegrationWebSearchRequest(config.webSearchModel))
		if bifrostErr != nil {
			failGigaChatIntegrationBifrostError(t, "web search built-in", bifrostErr)
		}
		if response == nil || len(response.Output) == 0 {
			t.Fatal("web search built-in returned no output")
		}
	})
}

func TestGigaChatIntegrationConfigUsesAccessToken(t *testing.T) {
	t.Setenv("GIGACHAT_ACCESS_TOKEN", "integration-test-access-token")
	t.Setenv("GIGACHAT_CREDENTIALS", "")
	t.Setenv("GIGACHAT_SCOPE", "")
	t.Setenv("GIGACHAT_USER", "")
	t.Setenv("GIGACHAT_PASSWORD", "")
	t.Setenv("GIGACHAT_BASE_URL", "")
	t.Setenv("GIGACHAT_CERT_FILE", "")
	t.Setenv("GIGACHAT_KEY_FILE", "")

	config := loadGigaChatIntegrationConfig(t)
	if !config.hasAccessToken {
		t.Fatal("access-token-only integration config was not accepted")
	}
	if config.hasOAuth || config.hasPassword {
		t.Fatalf("unexpected token flow flags: oauth=%t password=%t", config.hasOAuth, config.hasPassword)
	}

	key := config.inferenceKey()
	if key.Name != "gigachat-integration-access-token" {
		t.Fatalf("inference key name mismatch: got %q", key.Name)
	}
	if key.GigaChatKeyConfig == nil || !key.GigaChatKeyConfig.AccessToken.IsSet() {
		t.Fatalf("inference key did not use access-token auth: %#v", key.GigaChatKeyConfig)
	}
	if key.GigaChatKeyConfig.AccessToken.GetRawRef() != "env.GIGACHAT_ACCESS_TOKEN" {
		t.Fatalf("access token env var mismatch: got %q", key.GigaChatKeyConfig.AccessToken.GetRawRef())
	}
}

func loadGigaChatIntegrationConfig(t *testing.T) gigaChatIntegrationConfig {
	t.Helper()

	baseURL := gigaChatIntegrationEnv("GIGACHAT_BASE_URL")
	scope := gigaChatIntegrationEnv("GIGACHAT_SCOPE")
	hasAccessToken := gigaChatIntegrationEnv("GIGACHAT_ACCESS_TOKEN") != ""
	hasCredentials := gigaChatIntegrationEnv("GIGACHAT_CREDENTIALS") != ""
	hasUser := gigaChatIntegrationEnv("GIGACHAT_USER") != ""
	hasPasswordValue := gigaChatIntegrationEnv("GIGACHAT_PASSWORD") != ""

	config := gigaChatIntegrationConfig{
		baseURL:         baseURL,
		authURL:         gigaChatIntegrationEnv("GIGACHAT_AUTH_URL"),
		scope:           scope,
		chatModel:       gigaChatIntegrationEnvWithDefault("GIGACHAT_CHAT_MODEL", gigaChatIntegrationDefaultChatModel),
		embeddingModel:  gigaChatIntegrationEnvWithDefault("GIGACHAT_EMBEDDING_MODEL", gigaChatIntegrationDefaultEmbeddingModel),
		reasoningModel:  gigaChatIntegrationEnv("GIGACHAT_REASONING_MODEL"),
		reasoningEffort: gigaChatIntegrationEnvWithDefault("GIGACHAT_REASONING_EFFORT", "low"),
		batchModel:      gigaChatIntegrationEnvWithDefault("GIGACHAT_BATCH_MODEL", gigaChatIntegrationEnvWithDefault("GIGACHAT_CHAT_MODEL", gigaChatIntegrationDefaultChatModel)),
		webSearchModel:  gigaChatIntegrationEnvWithDefault("GIGACHAT_WEB_SEARCH_MODEL", gigaChatIntegrationEnvWithDefault("GIGACHAT_CHAT_MODEL", gigaChatIntegrationDefaultChatModel)),
		hasAccessToken:  hasAccessToken,
		hasOAuth:        hasCredentials && scope != "",
		hasPassword:     hasUser && hasPasswordValue && baseURL != "",
		runBatch:        gigaChatIntegrationEnvBool("GIGACHAT_ENABLE_BATCH_TEST"),
		runWebSearch:    gigaChatIntegrationEnvBool("GIGACHAT_ENABLE_WEB_SEARCH_TEST"),
		caBundleFile:    gigaChatIntegrationEnv("GIGACHAT_CA_BUNDLE_FILE"),
		certFile:        gigaChatIntegrationEnv("GIGACHAT_CERT_FILE"),
		keyFile:         gigaChatIntegrationEnv("GIGACHAT_KEY_FILE"),
	}

	if !config.hasAccessToken && !config.hasOAuth && !config.hasPassword {
		t.Skip("set either GIGACHAT_ACCESS_TOKEN, GIGACHAT_CREDENTIALS+GIGACHAT_SCOPE, or GIGACHAT_USER+GIGACHAT_PASSWORD+GIGACHAT_BASE_URL to run GigaChat integration tests")
	}
	if (config.certFile == "") != (config.keyFile == "") {
		t.Fatal("GIGACHAT_CERT_FILE and GIGACHAT_KEY_FILE must be set together")
	}

	return config
}

func gigaChatIntegrationEnv(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}

func gigaChatIntegrationEnvWithDefault(name string, defaultValue string) string {
	value := gigaChatIntegrationEnv(name)
	if value == "" {
		return defaultValue
	}
	return value
}

func gigaChatIntegrationEnvBool(name string) bool {
	switch strings.ToLower(gigaChatIntegrationEnv(name)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func newGigaChatIntegrationProvider(t *testing.T, config gigaChatIntegrationConfig) *GigaChatProvider {
	t.Helper()

	providerConfig := &schemas.ProviderConfig{}
	if config.baseURL != "" {
		providerConfig.NetworkConfig.BaseURL = config.baseURL
	}
	provider, err := NewGigaChatProvider(providerConfig, nil)
	if err != nil {
		failGigaChatIntegrationError(t, "new provider", err)
	}
	return provider
}

func (config gigaChatIntegrationConfig) inferenceKey() schemas.Key {
	if config.hasAccessToken {
		return config.accessTokenKey()
	}
	if config.hasOAuth {
		return config.oauthKey()
	}
	return config.passwordKey()
}

func (config gigaChatIntegrationConfig) accessTokenKey() schemas.Key {
	keyConfig := config.keyConfig()
	keyConfig.AccessToken = schemas.NewSecretVar("env.GIGACHAT_ACCESS_TOKEN")
	return schemas.Key{
		Name:              "gigachat-integration-access-token",
		Models:            schemas.WhiteList{"*"},
		GigaChatKeyConfig: keyConfig,
	}
}

func (config gigaChatIntegrationConfig) oauthKey() schemas.Key {
	keyConfig := config.keyConfig()
	keyConfig.Credentials = schemas.NewSecretVar("env.GIGACHAT_CREDENTIALS")
	keyConfig.Scope = config.scope
	return schemas.Key{
		Name:              "gigachat-integration-oauth",
		Models:            schemas.WhiteList{"*"},
		GigaChatKeyConfig: keyConfig,
	}
}

func (config gigaChatIntegrationConfig) passwordKey() schemas.Key {
	keyConfig := config.keyConfig()
	keyConfig.User = schemas.NewSecretVar("env.GIGACHAT_USER")
	keyConfig.Password = schemas.NewSecretVar("env.GIGACHAT_PASSWORD")
	return schemas.Key{
		Name:              "gigachat-integration-password",
		Models:            schemas.WhiteList{"*"},
		GigaChatKeyConfig: keyConfig,
	}
}

func (config gigaChatIntegrationConfig) keyConfig() *schemas.GigaChatKeyConfig {
	return &schemas.GigaChatKeyConfig{
		AuthURL:      config.authURL,
		BaseURL:      config.baseURL,
		CertFile:     config.certFile,
		KeyFile:      config.keyFile,
		CABundleFile: config.caBundleFile,
	}
}

func newGigaChatIntegrationContext(t *testing.T) *schemas.BifrostContext {
	t.Helper()

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), gigaChatIntegrationTimeout)
	t.Cleanup(cancel)
	return ctx
}

func gigaChatIntegrationChatRequest(model string) *schemas.BifrostChatRequest {
	maxTokens := 64
	text := "Reply with one short sentence."
	return &schemas.BifrostChatRequest{
		Model: model,
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: &text},
		}},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: &maxTokens,
		},
	}
}

func gigaChatIntegrationEmbeddingRequest(model string) *schemas.BifrostEmbeddingRequest {
	return &schemas.BifrostEmbeddingRequest{
		Model: model,
		Input: &schemas.EmbeddingInput{Text: schemas.Ptr("integration test")},
	}
}

func gigaChatIntegrationResponsesRequest(model string) *schemas.BifrostResponsesRequest {
	maxTokens := 64
	return &schemas.BifrostResponsesRequest{
		Model: model,
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Reply with one short sentence.")},
		}},
		Params: &schemas.ResponsesParameters{
			MaxOutputTokens: &maxTokens,
		},
	}
}

func gigaChatIntegrationCountTokensRequest(model string) *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Model: model,
		Input: []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Привет, как дела?")},
			},
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Hello, how are you?")},
			},
		},
	}
}

func gigaChatIntegrationReasoningRequest(model string, effort string) *schemas.BifrostResponsesRequest {
	maxTokens := 128
	return &schemas.BifrostResponsesRequest{
		Model: model,
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Think briefly, then answer with exactly: four.")},
		}},
		Params: &schemas.ResponsesParameters{
			MaxOutputTokens: &maxTokens,
			Reasoning: &schemas.ResponsesParametersReasoning{
				Effort: &effort,
			},
		},
	}
}

func gigaChatIntegrationOptionalToolRequest(t *testing.T, model string) *schemas.BifrostChatRequest {
	t.Helper()

	request := testGigaChatChatToolRequest(t, "get_weather")
	request.Model = model
	request.Input[0].Content = &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Use the get_weather function for Moscow. Leave units empty if unknown.")}
	request.Params.Tools[0].Function.Parameters = mustGigaChatToolParameters(t, `{
		"type": "object",
		"properties": {
			"city": {"anyOf": [{"type": "string"}, {"type": "null"}]},
			"units": {"type": ["string", "null"], "nullable": true}
		},
		"required": ["city"]
	}`)
	request.Params.ToolChoice = &schemas.ChatToolChoice{
		ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
			Type: schemas.ChatToolChoiceTypeFunction,
			Function: &schemas.ChatToolChoiceFunction{
				Name: "get_weather",
			},
		},
	}
	return request
}

func gigaChatIntegrationBatchCreateRequest(model string) *schemas.BifrostBatchCreateRequest {
	return &schemas.BifrostBatchCreateRequest{
		Provider:         schemas.GigaChat,
		Endpoint:         schemas.BatchEndpointChatCompletions,
		CompletionWindow: "24h",
		Requests: []schemas.BatchRequestItem{{
			CustomID: fmt.Sprintf("bifrost-gigachat-integration-%d", time.Now().UnixNano()),
			Method:   "POST",
			URL:      string(schemas.BatchEndpointChatCompletions),
			Body: map[string]interface{}{
				"model": model,
				"messages": []map[string]interface{}{{
					"role":    "user",
					"content": "Reply with one short sentence.",
				}},
				"max_tokens": 32,
			},
		}},
	}
}

func gigaChatIntegrationWebSearchRequest(model string) *schemas.BifrostResponsesRequest {
	maxTokens := 128
	searchContextSize := "low"
	return &schemas.BifrostResponsesRequest{
		Model: model,
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Use web search and answer in one sentence: what is the official GigaChat developer documentation site?")},
		}},
		Params: &schemas.ResponsesParameters{
			MaxOutputTokens: &maxTokens,
			Tools: []schemas.ResponsesTool{{
				Type: schemas.ResponsesToolTypeWebSearchPreview,
				ResponsesToolWebSearchPreview: &schemas.ResponsesToolWebSearchPreview{
					SearchContextSize: &searchContextSize,
				},
			}},
			ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
				Type: schemas.ResponsesToolChoiceTypeWebSearchPreview,
			}},
		},
	}
}

func runGigaChatIntegrationFileLifecycle(t *testing.T, provider *GigaChatProvider, key schemas.Key) {
	t.Helper()

	ctx := newGigaChatIntegrationContext(t)
	content := []byte("bifrost gigachat integration file\n")
	contentType := "text/plain"
	uploadResponse, bifrostErr := provider.FileUpload(ctx, key, &schemas.BifrostFileUploadRequest{
		Provider:    schemas.GigaChat,
		File:        content,
		Filename:    fmt.Sprintf("bifrost-gigachat-integration-%d.txt", time.Now().UnixNano()),
		Purpose:     schemas.FilePurposeUserData,
		ContentType: &contentType,
	})
	if bifrostErr != nil {
		failGigaChatIntegrationBifrostError(t, "file upload", bifrostErr)
	}
	if uploadResponse == nil || strings.TrimSpace(uploadResponse.ID) == "" {
		t.Fatalf("file upload returned no file ID: %#v", uploadResponse)
	}

	fileID := uploadResponse.ID
	deleted := false
	defer func() {
		if deleted {
			return
		}
		if _, cleanupErr := provider.FileDelete(ctx, []schemas.Key{key}, &schemas.BifrostFileDeleteRequest{
			Provider: schemas.GigaChat,
			FileID:   fileID,
		}); cleanupErr != nil {
			t.Logf("cleanup file delete failed: %s", redactGigaChatIntegrationSecrets(cleanupErr.String()))
		}
	}()

	listResponse, bifrostErr := provider.FileList(ctx, []schemas.Key{key}, &schemas.BifrostFileListRequest{Provider: schemas.GigaChat})
	if bifrostErr != nil {
		failGigaChatIntegrationBifrostError(t, "file list", bifrostErr)
	}
	if listResponse == nil || !gigaChatIntegrationFileListContains(listResponse.Data, fileID) {
		t.Fatalf("file list did not include uploaded file %q", fileID)
	}

	retrieveResponse, bifrostErr := provider.FileRetrieve(ctx, []schemas.Key{key}, &schemas.BifrostFileRetrieveRequest{
		Provider: schemas.GigaChat,
		FileID:   fileID,
	})
	if bifrostErr != nil {
		failGigaChatIntegrationBifrostError(t, "file retrieve", bifrostErr)
	}
	if retrieveResponse == nil || retrieveResponse.ID != fileID {
		t.Fatalf("file retrieve response mismatch: got %#v, want ID %q", retrieveResponse, fileID)
	}

	contentResponse, bifrostErr := provider.FileContent(ctx, []schemas.Key{key}, &schemas.BifrostFileContentRequest{
		Provider: schemas.GigaChat,
		FileID:   fileID,
	})
	if bifrostErr != nil {
		failGigaChatIntegrationBifrostError(t, "file content", bifrostErr)
	}
	if contentResponse == nil || !bytes.Equal(contentResponse.Content, content) {
		t.Fatalf("file content mismatch: got %q, want %q", string(contentResponse.Content), string(content))
	}

	deleteResponse, bifrostErr := provider.FileDelete(ctx, []schemas.Key{key}, &schemas.BifrostFileDeleteRequest{
		Provider: schemas.GigaChat,
		FileID:   fileID,
	})
	if bifrostErr != nil {
		failGigaChatIntegrationBifrostError(t, "file delete", bifrostErr)
	}
	if deleteResponse == nil || deleteResponse.ID != fileID || !deleteResponse.Deleted {
		t.Fatalf("file delete response mismatch: %#v", deleteResponse)
	}
	deleted = true
}

func gigaChatIntegrationFileListContains(files []schemas.FileObject, fileID string) bool {
	for _, file := range files {
		if file.ID == fileID {
			return true
		}
	}
	return false
}

func assertGigaChatIntegrationChatStream(t *testing.T, stream chan *schemas.BifrostStreamChunk) {
	t.Helper()

	if stream == nil {
		t.Fatal("chat completion stream is nil")
	}

	receivedResponse := false
	for chunk := range stream {
		if chunk == nil {
			continue
		}
		if chunk.BifrostError != nil {
			failGigaChatIntegrationBifrostError(t, "chat completion stream chunk", chunk.BifrostError)
		}
		if chunk.BifrostChatResponse != nil {
			receivedResponse = true
			if chunk.BifrostChatResponse.ExtraFields.Provider != schemas.GigaChat {
				t.Fatalf("provider mismatch: got %q, want %q", chunk.BifrostChatResponse.ExtraFields.Provider, schemas.GigaChat)
			}
		}
	}
	if !receivedResponse {
		t.Fatal("chat completion stream returned no response chunks")
	}
}

func assertGigaChatIntegrationResponsesStream(t *testing.T, stream chan *schemas.BifrostStreamChunk) {
	t.Helper()

	if stream == nil {
		t.Fatal("responses stream is nil")
	}

	receivedCompleted := false
	for chunk := range stream {
		if chunk == nil {
			continue
		}
		if chunk.BifrostError != nil {
			failGigaChatIntegrationBifrostError(t, "responses stream chunk", chunk.BifrostError)
		}
		if chunk.BifrostResponsesStreamResponse == nil {
			continue
		}
		if chunk.BifrostResponsesStreamResponse.Response != nil &&
			chunk.BifrostResponsesStreamResponse.Response.ExtraFields.Provider != schemas.GigaChat {
			t.Fatalf("provider mismatch: got %q, want %q", chunk.BifrostResponsesStreamResponse.Response.ExtraFields.Provider, schemas.GigaChat)
		}
		if chunk.BifrostResponsesStreamResponse.Type == schemas.ResponsesStreamResponseTypeCompleted {
			receivedCompleted = true
		}
	}
	if !receivedCompleted {
		t.Fatal("responses stream did not emit response.completed")
	}
}

func gigaChatIntegrationHasReasoningOutput(response *schemas.BifrostResponsesResponse) bool {
	if response == nil {
		return false
	}
	for _, output := range response.Output {
		if output.Type != nil && *output.Type == schemas.ResponsesMessageTypeReasoning {
			return true
		}
		if output.ResponsesReasoning != nil && len(output.ResponsesReasoning.Summary) > 0 {
			return true
		}
	}
	return false
}

func gigaChatIntegrationHasToolCall(response *schemas.BifrostChatResponse) bool {
	if response == nil {
		return false
	}
	for _, choice := range response.Choices {
		if choice.ChatNonStreamResponseChoice == nil || choice.ChatNonStreamResponseChoice.Message == nil {
			continue
		}
		message := choice.ChatNonStreamResponseChoice.Message
		if message.ChatAssistantMessage != nil && len(message.ChatAssistantMessage.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

func failGigaChatIntegrationBifrostError(t *testing.T, operation string, bifrostErr *schemas.BifrostError) {
	t.Helper()

	if bifrostErr == nil {
		t.Fatalf("%s failed", operation)
	}
	failGigaChatIntegrationError(t, operation, errors.New(bifrostErr.String()))
}

func failGigaChatIntegrationError(t *testing.T, operation string, err error) {
	t.Helper()

	message := fmt.Sprintf("%s failed: %v", operation, err)
	t.Fatal(redactGigaChatIntegrationSecrets(message))
}

func redactGigaChatIntegrationSecrets(message string) string {
	for _, envName := range []string{
		"GIGACHAT_ACCESS_TOKEN",
		"GIGACHAT_CREDENTIALS",
		"GIGACHAT_USER",
		"GIGACHAT_PASSWORD",
		"GIGACHAT_CERT_FILE",
		"GIGACHAT_KEY_FILE",
		"GIGACHAT_CA_BUNDLE_FILE",
	} {
		if value := os.Getenv(envName); strings.TrimSpace(value) != "" {
			message = strings.ReplaceAll(message, value, "<redacted>")
		}
	}
	return message
}
