package gigachat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestGigaChatChatCompletion(t *testing.T) {
	testGigaChatChatCompletion(t)
}

func TestGigaChatChatCompletionFileDataDecoding(t *testing.T) {
	t.Parallel()

	t.Run("RawTextFileData", func(t *testing.T) {
		t.Parallel()

		filename := "note.txt"
		fileType := "text/plain"
		fileData := "test"
		upload, err := gigaChatChatFileUpload(3, &schemas.ChatInputFile{
			Filename: &filename,
			FileData: &fileData,
			FileType: &fileType,
		})
		if err != nil {
			t.Fatalf("gigaChatChatFileUpload returned error: %v", err)
		}
		if string(upload.file) != "test" {
			t.Fatalf("raw text file_data was not preserved: %q", string(upload.file))
		}
		if upload.filename != "note.txt" || upload.contentType != "text/plain" {
			t.Fatalf("upload metadata mismatch: %#v", upload)
		}
	})

	t.Run("TextDataURLBase64", func(t *testing.T) {
		t.Parallel()

		filename := "note.txt"
		fileData := "data:text/plain;base64,dGVzdA=="
		upload, err := gigaChatChatFileUpload(4, &schemas.ChatInputFile{
			Filename: &filename,
			FileData: &fileData,
		})
		if err != nil {
			t.Fatalf("gigaChatChatFileUpload returned error: %v", err)
		}
		if string(upload.file) != "test" {
			t.Fatalf("base64 data URL was not decoded: %q", string(upload.file))
		}
		if !strings.HasPrefix(upload.contentType, "text/plain") {
			t.Fatalf("content type mismatch: %q", upload.contentType)
		}
	})

	t.Run("NonTextInvalidBase64IncludesBlockIndex", func(t *testing.T) {
		t.Parallel()

		filename := "document.pdf"
		fileType := "application/pdf"
		fileData := "not-base64!"
		_, err := gigaChatChatFileUpload(7, &schemas.ChatInputFile{
			Filename: &filename,
			FileData: &fileData,
			FileType: &fileType,
		})
		if err == nil {
			t.Fatal("expected non-text invalid base64 to fail")
		}
		if !strings.Contains(err.Error(), "content block 7") || !strings.Contains(err.Error(), "file_data must be a base64 data URL or base64-encoded content") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestGigaChatChatCompletionStreamToolCallIndex(t *testing.T) {
	t.Parallel()

	functionsStateID := "call-weather"
	response := ToBifrostChatStreamResponse(schemas.GigaChat, &GigaChatChatStreamResponse{
		Model: "GigaChat",
		Choices: []GigaChatChatStreamChoice{{
			Index: 2,
			Delta: &GigaChatChatStreamDelta{
				FunctionCall: &GigaChatFunctionCall{
					Name:      "get_weather",
					Arguments: json.RawMessage(`{"city":"Moscow"}`),
				},
				FunctionsStateID: &functionsStateID,
			},
		}},
	})
	if response == nil || len(response.Choices) != 1 || response.Choices[0].ChatStreamResponseChoice == nil {
		t.Fatalf("stream response mismatch: %#v", response)
	}
	choice := response.Choices[0]
	if choice.Index != 2 {
		t.Fatalf("choice index mismatch: got %d, want 2", choice.Index)
	}
	delta := choice.ChatStreamResponseChoice.Delta
	if delta == nil || len(delta.ToolCalls) != 1 {
		t.Fatalf("tool call delta mismatch: %#v", delta)
	}
	toolCall := delta.ToolCalls[0]
	if toolCall.Index != 0 {
		t.Fatalf("tool call index mismatch: got %d, want 0", toolCall.Index)
	}
	if toolCall.ID == nil || *toolCall.ID != functionsStateID {
		t.Fatalf("tool call id mismatch: %#v", toolCall.ID)
	}
	if toolCall.Function.Name == nil || *toolCall.Function.Name != "get_weather" || toolCall.Function.Arguments != `{"city":"Moscow"}` {
		t.Fatalf("tool call function mismatch: %#v", toolCall.Function)
	}
}

func testGigaChatChatCompletion(t *testing.T) {
	t.Parallel()

	t.Run("ConverterMapsRequest", testGigaChatChatConverterMapsRequest)
	t.Run("ConverterMapsOpenAIJSONSchemaResponseFormat", testGigaChatChatConverterMapsOpenAIJSONSchemaResponseFormat)
	t.Run("ConverterMapsGigaChatJSONSchemaResponseFormat", testGigaChatChatConverterMapsGigaChatJSONSchemaResponseFormat)
	t.Run("ConverterPreservesAssistantReasoningContent", testGigaChatChatConverterPreservesAssistantReasoningContent)
	t.Run("ConverterMapsFileAttachments", testGigaChatChatConverterMapsFileAttachments)
	t.Run("ExecutesWithOAuthTokenAndExtraParams", testGigaChatChatCompletionExecutesWithOAuthTokenAndExtraParams)
	t.Run("ExecutesWithMTLSClientCertificate", testGigaChatChatCompletionExecutesWithMTLSClientCertificate)
	t.Run("UploadsInlineImageAttachment", testGigaChatChatCompletionUploadsInlineImageAttachment)
	t.Run("UploadsInlineFileAttachment", testGigaChatChatCompletionUploadsInlineFileAttachment)
	t.Run("ReusesUploadedAttachmentAfterBackendError", testGigaChatChatCompletionReusesUploadedAttachmentAfterBackendError)
	t.Run("ReusesSuccessfulAttachmentAfterPartialUploadFailure", testGigaChatChatCompletionReusesSuccessfulAttachmentAfterPartialUploadFailure)
	t.Run("DoesNotReuseUploadedAttachmentAcrossIndependentRequests", testGigaChatChatCompletionDoesNotReuseUploadedAttachmentAcrossIndependentRequests)
	t.Run("DoesNotCacheFailedAttachmentUpload", testGigaChatChatCompletionDoesNotCacheFailedAttachmentUpload)
	t.Run("RejectsUnsupportedTools", testGigaChatChatCompletionRejectsUnsupportedTools)
	t.Run("RejectsUnsupportedResponseFormat", testGigaChatChatCompletionRejectsUnsupportedResponseFormat)
	t.Run("MapsProviderErrors", testGigaChatChatCompletionMapsProviderErrors)
	t.Run("RefreshesTokenAfterUnauthorized", testGigaChatChatCompletionRefreshesTokenAfterUnauthorized)
	t.Run("DoesNotDoubleExchangeExpiredTokenOnRefresh", testGigaChatChatCompletionDoesNotDoubleExchangeExpiredTokenOnRefresh)
	t.Run("StreamsSSEChunks", testGigaChatChatCompletionStreamsSSEChunks)
	t.Run("FinalizesLargeResponsePassthrough", testGigaChatChatCompletionStreamFinalizesLargeResponsePassthrough)
	t.Run("MapsStreamingProviderErrors", testGigaChatChatCompletionMapsStreamingProviderErrors)
	t.Run("MapsStreamingErrorEvents", testGigaChatChatCompletionMapsStreamingErrorEvents)
	t.Run("RefreshesStreamingTokenAfterUnauthorized", testGigaChatChatCompletionStreamRefreshesTokenAfterUnauthorized)
	t.Run("HandlesStreamingContextCancellation", testGigaChatChatCompletionStreamHandlesContextCancellation)
}

func testGigaChatChatConverterMapsRequest(t *testing.T) {
	t.Parallel()

	maxTokens := 512
	temperature := 0.2
	topP := 0.8
	n := 1
	reasoningEffort := "high"
	text := "hello"
	request := &schemas.BifrostChatRequest{
		Model: "GigaChat",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: &text},
			},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: &maxTokens,
			Temperature:         &temperature,
			TopP:                &topP,
			N:                   &n,
			Stop:                []string{"stop"},
			Reasoning:           &schemas.ChatReasoning{Effort: &reasoningEffort},
			ExtraParams: map[string]interface{}{
				"profanity_check": false,
			},
		},
	}

	gigaChatReq, err := ToGigaChatChatRequest(testBifrostContext(), request)
	if err != nil {
		t.Fatalf("ToGigaChatChatRequest returned error: %v", err)
	}
	if gigaChatReq.MaxTokens == nil || *gigaChatReq.MaxTokens != maxTokens {
		t.Fatalf("max_tokens mismatch: got %#v, want %d", gigaChatReq.MaxTokens, maxTokens)
	}
	if gigaChatReq.Stream == nil || *gigaChatReq.Stream {
		t.Fatalf("stream mismatch: got %#v, want false", gigaChatReq.Stream)
	}
	if got := gigaChatReq.GetExtraParams()["profanity_check"]; got != false {
		t.Fatalf("extra param mismatch: got %#v", got)
	}
	if gigaChatReq.ReasoningEffort == nil || *gigaChatReq.ReasoningEffort != reasoningEffort {
		t.Fatalf("reasoning_effort mismatch: got %#v, want %q", gigaChatReq.ReasoningEffort, reasoningEffort)
	}

	body, err := json.Marshal(gigaChatReq)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}
	if strings.Contains(string(body), "max_completion_tokens") {
		t.Fatalf("request body should use max_tokens, got %s", body)
	}
	if !strings.Contains(string(body), `"max_tokens":512`) {
		t.Fatalf("request body missing max_tokens: %s", body)
	}
	if strings.Contains(string(body), `"reasoning":`) {
		t.Fatalf("request body should not send reasoning object: %s", body)
	}
	if !strings.Contains(string(body), `"reasoning_effort":"high"`) {
		t.Fatalf("request body missing reasoning_effort: %s", body)
	}
}

func testGigaChatChatConverterMapsOpenAIJSONSchemaResponseFormat(t *testing.T) {
	t.Parallel()

	strict := true
	formatName := "MathAnswer"
	formatDescription := "Math answer schema."
	responseFormat := interface{}(map[string]interface{}{
		"type": "json_schema",
		"json_schema": map[string]interface{}{
			"name":        formatName,
			"description": formatDescription,
			"strict":      strict,
			"schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"steps":        map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
					"final_answer": map[string]interface{}{"type": "string"},
				},
				"required": []interface{}{"steps", "final_answer"},
			},
		},
	})

	request := testGigaChatChatRequest()
	request.Params.ResponseFormat = &responseFormat

	gigaChatReq, err := ToGigaChatChatRequest(testBifrostContext(), request)
	if err != nil {
		t.Fatalf("ToGigaChatChatRequest returned error: %v", err)
	}
	assertGigaChatJSONSchemaResponseFormat(t, gigaChatReq.ResponseFormat, formatName, formatDescription, strict)

	body, err := json.Marshal(gigaChatReq)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}
	if strings.Contains(string(body), `"json_schema":`) {
		t.Fatalf("GigaChat response_format should use schema, not OpenAI json_schema wrapper: %s", body)
	}
	if !strings.Contains(string(body), `"response_format"`) || !strings.Contains(string(body), `"schema"`) {
		t.Fatalf("request body missing response_format schema: %s", body)
	}
}

func testGigaChatChatConverterMapsGigaChatJSONSchemaResponseFormat(t *testing.T) {
	t.Parallel()

	responseFormat := interface{}(schemas.NewOrderedMapFromPairs(
		schemas.KV("type", "json_schema"),
		schemas.KV("schema", schemas.NewOrderedMapFromPairs(
			schemas.KV("type", "object"),
			schemas.KV("properties", schemas.NewOrderedMapFromPairs(
				schemas.KV("status", schemas.NewOrderedMapFromPairs(schemas.KV("type", "string"))),
			)),
			schemas.KV("required", []interface{}{"status"}),
		)),
		schemas.KV("strict", true),
	))

	request := testGigaChatChatRequest()
	request.Params.ResponseFormat = &responseFormat

	gigaChatReq, err := ToGigaChatChatRequest(testBifrostContext(), request)
	if err != nil {
		t.Fatalf("ToGigaChatChatRequest returned error: %v", err)
	}
	assertGigaChatJSONSchemaResponseFormat(t, gigaChatReq.ResponseFormat, "", "", true)
}

func testGigaChatChatConverterPreservesAssistantReasoningContent(t *testing.T) {
	t.Parallel()

	userText := "question"
	answerText := "answer"
	reasoning := "model reasoning"
	request := &schemas.BifrostChatRequest{
		Model: "GigaChat",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: &userText},
			},
			{
				Role:    schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{ContentStr: &answerText},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					Reasoning: &reasoning,
				},
			},
		},
	}

	gigaChatReq, err := ToGigaChatChatRequest(testBifrostContext(), request)
	if err != nil {
		t.Fatalf("ToGigaChatChatRequest returned error: %v", err)
	}
	if len(gigaChatReq.Messages) != 2 {
		t.Fatalf("message count mismatch: got %d", len(gigaChatReq.Messages))
	}
	assistant := gigaChatReq.Messages[1]
	if assistant.Reasoning == nil || *assistant.Reasoning != reasoning {
		t.Fatalf("assistant reasoning_content mismatch: %#v", assistant.Reasoning)
	}

	body, err := json.Marshal(gigaChatReq)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}
	if !strings.Contains(string(body), `"reasoning_content":"model reasoning"`) {
		t.Fatalf("request body missing reasoning_content: %s", body)
	}
	if strings.Contains(string(body), `"reasoning":"model reasoning"`) {
		t.Fatalf("request body should use reasoning_content, got %s", body)
	}
}

func testGigaChatChatConverterMapsFileAttachments(t *testing.T) {
	t.Parallel()

	prompt := "Кратко перескажи документ"
	fileID := "file-document"
	filename := "document.pdf"
	fileType := "application/pdf"
	request := &schemas.BifrostChatRequest{
		Model: "GigaChat",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentBlocks: []schemas.ChatContentBlock{
						{Type: schemas.ChatContentBlockTypeText, Text: &prompt},
						{
							Type: schemas.ChatContentBlockTypeFile,
							File: &schemas.ChatInputFile{
								FileID:   &fileID,
								Filename: &filename,
								FileType: &fileType,
							},
						},
					},
				},
			},
		},
	}

	gigaChatReq, err := ToGigaChatChatRequest(testBifrostContext(), request)
	if err != nil {
		t.Fatalf("ToGigaChatChatRequest returned error: %v", err)
	}
	if len(gigaChatReq.Messages) != 1 {
		t.Fatalf("message count mismatch: got %d", len(gigaChatReq.Messages))
	}
	message := gigaChatReq.Messages[0]
	if message.Content == nil || message.Content.ContentStr == nil || *message.Content.ContentStr != prompt {
		t.Fatalf("content mismatch: %#v", message.Content)
	}
	if len(message.Attachments) != 1 || message.Attachments[0] != fileID {
		t.Fatalf("attachments mismatch: %#v", message.Attachments)
	}
	if gigaChatReq.FunctionCall != "auto" {
		t.Fatalf("function_call mismatch: got %#v, want auto", gigaChatReq.FunctionCall)
	}

	body, err := json.Marshal(gigaChatReq)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}
	bodyStr := string(body)
	if !strings.Contains(bodyStr, `"attachments":["file-document"]`) {
		t.Fatalf("request body missing attachments: %s", body)
	}
	if strings.Contains(bodyStr, "file_data") || strings.Contains(bodyStr, "file_id") {
		t.Fatalf("request body should not include OpenAI file content block fields: %s", body)
	}
}

func testGigaChatChatCompletionExecutesWithOAuthTokenAndExtraParams(t *testing.T) {
	t.Parallel()

	var tokenRequests atomic.Int32
	var chatRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth":
			tokenRequests.Add(1)
			if got := request.Header.Get("Authorization"); got != "Basic super-secret-credentials" {
				t.Fatalf("token authorization header mismatch: got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"chat-access-token","expires_at":1893456000}`))
		case "/v1/chat/completions":
			chatRequests.Add(1)
			if got := request.Header.Get("Authorization"); got != "Bearer chat-access-token" {
				t.Fatalf("chat authorization header mismatch: got %q", got)
			}
			if strings.Contains(request.Header.Get("Authorization"), "super-secret-credentials") {
				t.Fatal("chat request leaked OAuth credentials")
			}
			assertGigaChatChatRequestBody(t, request)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Request-ID", "chat-request-id")
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-test",
				"choices":[{"index":0,"message":{"role":"assistant","content":"Здравствуйте","reasoning_content":"Думаю"},"finish_reason":"stop"}],
				"created":1700000000,
				"model":"GigaChat",
				"object":"chat.completion",
				"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10,"precached_prompt_tokens":2}
			}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	provider.sendBackRawRequest = true
	provider.sendBackRawResponse = true
	ctx := testBifrostContext()
	ctx.SetValue(schemas.BifrostContextKeyCaptureRawRequest, true)
	ctx.SetValue(schemas.BifrostContextKeyCaptureRawResponse, true)
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)

	response, bifrostErr := provider.ChatCompletion(ctx, testGigaChatOAuthKey(server.URL+"/oauth", "", "super-secret-credentials"), testGigaChatChatRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion returned error: %v", bifrostErr)
	}
	if tokenRequests.Load() != 1 {
		t.Fatalf("token request count mismatch: got %d, want 1", tokenRequests.Load())
	}
	if chatRequests.Load() != 1 {
		t.Fatalf("chat request count mismatch: got %d, want 1", chatRequests.Load())
	}
	if response.ID != "chatcmpl-test" {
		t.Fatalf("response id mismatch: got %q", response.ID)
	}
	if response.ExtraFields.Provider != schemas.GigaChat {
		t.Fatalf("provider mismatch: got %q, want %q", response.ExtraFields.Provider, schemas.GigaChat)
	}
	if response.Usage == nil || response.Usage.TotalTokens != 10 {
		t.Fatalf("usage mismatch: %#v", response.Usage)
	}
	if response.Usage.PromptTokensDetails == nil || response.Usage.PromptTokensDetails.CachedReadTokens != 2 {
		t.Fatalf("precached prompt tokens were not mapped: %#v", response.Usage.PromptTokensDetails)
	}
	if len(response.Choices) != 1 || response.Choices[0].ChatNonStreamResponseChoice == nil {
		t.Fatalf("unexpected choices: %#v", response.Choices)
	}
	content := response.Choices[0].ChatNonStreamResponseChoice.Message.Content
	if content == nil || content.ContentStr == nil || *content.ContentStr != "Здравствуйте" {
		t.Fatalf("content mismatch: %#v", content)
	}
	assistant := response.Choices[0].ChatNonStreamResponseChoice.Message.ChatAssistantMessage
	if assistant == nil || assistant.Reasoning == nil || *assistant.Reasoning != "Думаю" {
		t.Fatalf("reasoning_content was not mapped: %#v", assistant)
	}
	if len(assistant.ReasoningDetails) != 1 || assistant.ReasoningDetails[0].Text == nil || *assistant.ReasoningDetails[0].Text != "Думаю" {
		t.Fatalf("reasoning details mismatch: %#v", assistant.ReasoningDetails)
	}
	if got := ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders); got == nil {
		t.Fatal("provider response headers were not stored in context")
	}
}

func testGigaChatChatCompletionExecutesWithMTLSClientCertificate(t *testing.T) {
	t.Parallel()

	var oauthRequests atomic.Int32
	var chatRequests atomic.Int32
	server, caBundleFile, certFile, keyFile := newGigaChatMTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth", "/api/v2/oauth", "/v1/token", "/api/v1/token":
			oauthRequests.Add(1)
			t.Fatalf("unexpected token endpoint request: %s", request.URL.Path)
		case "/v1/chat/completions":
			chatRequests.Add(1)
			if got := request.Header.Get("Authorization"); got != "" {
				t.Fatalf("chat authorization header mismatch: got %q, want empty", got)
			}
			if request.TLS == nil || len(request.TLS.PeerCertificates) == 0 {
				t.Fatal("expected client certificate on API request")
			}
			assertGigaChatChatRequestBody(t, request)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-mtls",
				"choices":[{"index":0,"message":{"role":"assistant","content":"Здравствуйте"},"finish_reason":"stop"}],
				"created":1700000000,
				"model":"GigaChat",
				"object":"chat.completion",
				"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}
			}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))

	provider := newTestGigaChatChatProvider(t, server.URL)
	ctx := testBifrostContext()
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	response, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{
		GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
			CertFile:     certFile,
			KeyFile:      keyFile,
			CABundleFile: caBundleFile,
		},
	}, testGigaChatChatRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion returned error: %v", bifrostErr)
	}
	if oauthRequests.Load() != 0 {
		t.Fatalf("oauth request count mismatch: got %d, want 0", oauthRequests.Load())
	}
	if chatRequests.Load() != 1 {
		t.Fatalf("chat request count mismatch: got %d, want 1", chatRequests.Load())
	}
	if response.ID != "chatcmpl-mtls" {
		t.Fatalf("response id mismatch: got %q", response.ID)
	}
}

func testGigaChatChatCompletionUploadsInlineImageAttachment(t *testing.T) {
	t.Parallel()

	var uploadRequests atomic.Int32
	var chatRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/files":
			uploadRequests.Add(1)
			if got := request.Header.Get("Authorization"); got != "Bearer image-token" {
				t.Fatalf("file upload authorization header mismatch: got %q", got)
			}
			if err := request.ParseMultipartForm(1024); err != nil {
				t.Fatalf("failed to parse upload multipart form: %v", err)
			}
			if got := request.FormValue("purpose"); got != "general" {
				t.Fatalf("upload purpose mismatch: got %q", got)
			}
			file, header, err := request.FormFile("file")
			if err != nil {
				t.Fatalf("failed to read uploaded file: %v", err)
			}
			defer file.Close()
			fileBytes, err := io.ReadAll(file)
			if err != nil {
				t.Fatalf("failed to read uploaded bytes: %v", err)
			}
			if string(fileBytes) != "image-bytes" {
				t.Fatalf("uploaded image bytes mismatch: %q", fileBytes)
			}
			if header.Filename != "image.jpg" {
				t.Fatalf("uploaded image filename mismatch: got %q", header.Filename)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"uploaded-image","object":"file","bytes":11,"created_at":1700000000,"filename":"image.jpg","purpose":"general"}`))
		case "/v1/chat/completions":
			chatRequests.Add(1)
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("failed to read chat body: %v", err)
			}
			payload := assertGigaChatChatBodyAttachment(t, body, "uploaded-image")
			bodyStr := string(body)
			if strings.Contains(bodyStr, "data:image") || strings.Contains(bodyStr, "image_url") {
				t.Fatalf("chat body leaked OpenAI image_url payload: %s", body)
			}
			if _, ok := payload["function_call"]; ok {
				t.Fatalf("image-only attachment should not force function_call auto: %s", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"На изображении..."},"finish_reason":"stop"}],"model":"GigaChat","object":"chat.completion"}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	prompt := "Что на изображении?"
	imageURL := "data:image/jpg;base64,aW1hZ2UtYnl0ZXM="
	request := &schemas.BifrostChatRequest{
		Model: "GigaChat",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentBlocks: []schemas.ChatContentBlock{
						{Type: schemas.ChatContentBlockTypeText, Text: &prompt},
						{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: imageURL}},
					},
				},
			},
		},
	}

	provider := newTestGigaChatChatProvider(t, server.URL)
	response, bifrostErr := provider.ChatCompletion(testBifrostContext(), testGigaChatAccessTokenKey("image-token"), request)
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion returned error: %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("expected response, got nil")
	}
	if uploadRequests.Load() != 1 {
		t.Fatalf("upload request count mismatch: got %d, want 1", uploadRequests.Load())
	}
	if chatRequests.Load() != 1 {
		t.Fatalf("chat request count mismatch: got %d, want 1", chatRequests.Load())
	}
}

func testGigaChatChatCompletionUploadsInlineFileAttachment(t *testing.T) {
	t.Parallel()

	var uploadRequests atomic.Int32
	var chatRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/files":
			uploadRequests.Add(1)
			if err := request.ParseMultipartForm(1024); err != nil {
				t.Fatalf("failed to parse upload multipart form: %v", err)
			}
			file, header, err := request.FormFile("file")
			if err != nil {
				t.Fatalf("failed to read uploaded file: %v", err)
			}
			defer file.Close()
			fileBytes, err := io.ReadAll(file)
			if err != nil {
				t.Fatalf("failed to read uploaded bytes: %v", err)
			}
			if string(fileBytes) != "%PDF test" {
				t.Fatalf("uploaded file bytes mismatch: %q", fileBytes)
			}
			if header.Filename != "Day_2_v6.pdf" {
				t.Fatalf("uploaded filename mismatch: got %q", header.Filename)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"uploaded-pdf","object":"file","bytes":9,"created_at":1700000000,"filename":"Day_2_v6.pdf","purpose":"general"}`))
		case "/v1/chat/completions":
			chatRequests.Add(1)
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("failed to read chat body: %v", err)
			}
			payload := assertGigaChatChatBodyAttachment(t, body, "uploaded-pdf")
			bodyStr := string(body)
			if got := payload["function_call"]; got != "auto" {
				t.Fatalf("document attachment should enable function_call auto: got %#v body %s", got, body)
			}
			if strings.Contains(bodyStr, "file_data") || strings.Contains(bodyStr, "application/pdf;base64") {
				t.Fatalf("chat body leaked OpenAI file payload: %s", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"Краткое содержание..."},"finish_reason":"stop"}],"model":"GigaChat","object":"chat.completion"}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	prompt := "Create a comprehensive summary of this pdf"
	filename := "Day_2_v6.pdf"
	fileData := "data:application/pdf;base64,JVBERiB0ZXN0"
	request := &schemas.BifrostChatRequest{
		Model: "GigaChat",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentBlocks: []schemas.ChatContentBlock{
						{Type: schemas.ChatContentBlockTypeText, Text: &prompt},
						{
							Type: schemas.ChatContentBlockTypeFile,
							File: &schemas.ChatInputFile{
								Filename: &filename,
								FileData: &fileData,
							},
						},
					},
				},
			},
		},
	}

	provider := newTestGigaChatChatProvider(t, server.URL)
	response, bifrostErr := provider.ChatCompletion(testBifrostContext(), testGigaChatAccessTokenKey("file-token"), request)
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion returned error: %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("expected response, got nil")
	}
	if uploadRequests.Load() != 1 {
		t.Fatalf("upload request count mismatch: got %d, want 1", uploadRequests.Load())
	}
	if chatRequests.Load() != 1 {
		t.Fatalf("chat request count mismatch: got %d, want 1", chatRequests.Load())
	}
}

func testGigaChatChatCompletionReusesUploadedAttachmentAfterBackendError(t *testing.T) {
	t.Parallel()

	var uploadRequests atomic.Int32
	var chatRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/files":
			uploadRequests.Add(1)
			if err := request.ParseMultipartForm(1024); err != nil {
				t.Fatalf("failed to parse upload multipart form: %v", err)
			}
			file, _, err := request.FormFile("file")
			if err != nil {
				t.Fatalf("failed to read uploaded file: %v", err)
			}
			defer file.Close()
			fileBytes, err := io.ReadAll(file)
			if err != nil {
				t.Fatalf("failed to read uploaded bytes: %v", err)
			}
			if string(fileBytes) != "%PDF retry" {
				t.Fatalf("uploaded file bytes mismatch: %q", fileBytes)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"uploaded-retry-pdf","object":"file","bytes":10,"created_at":1700000000,"filename":"retry.pdf","purpose":"general"}`))
		case "/v1/chat/completions":
			requestIndex := chatRequests.Add(1)
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("failed to read chat body: %v", err)
			}
			assertGigaChatChatBodyAttachment(t, body, "uploaded-retry-pdf")
			if requestIndex == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"status":500,"message":"temporary backend failure"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"model":"GigaChat","object":"chat.completion"}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	prompt := "Summarize this file."
	filename := "retry.pdf"
	fileData := "data:application/pdf;base64,JVBERiByZXRyeQ=="
	request := &schemas.BifrostChatRequest{
		Model: "GigaChat",
		Input: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{
				ContentBlocks: []schemas.ChatContentBlock{
					{Type: schemas.ChatContentBlockTypeText, Text: &prompt},
					{
						Type: schemas.ChatContentBlockTypeFile,
						File: &schemas.ChatInputFile{
							Filename: &filename,
							FileData: &fileData,
						},
					},
				},
			},
		}},
	}

	provider := newTestGigaChatChatProvider(t, server.URL)
	ctx := testBifrostContext()
	key := testGigaChatAccessTokenKey("file-token")

	firstResponse, firstErr := provider.ChatCompletion(ctx, key, request)
	if firstResponse != nil {
		t.Fatalf("expected nil response from first backend failure, got %#v", firstResponse)
	}
	if firstErr == nil || firstErr.StatusCode == nil || *firstErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected first backend 500, got %#v", firstErr)
	}

	response, bifrostErr := provider.ChatCompletion(ctx, key, request)
	if bifrostErr != nil {
		t.Fatalf("second ChatCompletion returned error: %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("expected second response, got nil")
	}
	if uploadRequests.Load() != 1 {
		t.Fatalf("upload request count mismatch: got %d, want 1", uploadRequests.Load())
	}
	if chatRequests.Load() != 2 {
		t.Fatalf("chat request count mismatch: got %d, want 2", chatRequests.Load())
	}
}

func testGigaChatChatCompletionReusesSuccessfulAttachmentAfterPartialUploadFailure(t *testing.T) {
	t.Parallel()

	var firstFileUploads atomic.Int32
	var secondFileUploads atomic.Int32
	var chatRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/files":
			if err := request.ParseMultipartForm(1024); err != nil {
				t.Fatalf("failed to parse upload multipart form: %v", err)
			}
			file, header, err := request.FormFile("file")
			if err != nil {
				t.Fatalf("failed to read uploaded file: %v", err)
			}
			defer file.Close()
			fileBytes, err := io.ReadAll(file)
			if err != nil {
				t.Fatalf("failed to read uploaded bytes: %v", err)
			}

			w.Header().Set("Content-Type", "application/json")
			switch header.Filename {
			case "first.txt":
				firstFileUploads.Add(1)
				if string(fileBytes) != "first attachment" {
					t.Fatalf("first uploaded file bytes mismatch: %q", fileBytes)
				}
				_, _ = w.Write([]byte(`{"id":"uploaded-first","object":"file","bytes":16,"created_at":1700000000,"filename":"first.txt","purpose":"general"}`))
			case "second.txt":
				uploadIndex := secondFileUploads.Add(1)
				if string(fileBytes) != "second attachment" {
					t.Fatalf("second uploaded file bytes mismatch: %q", fileBytes)
				}
				if uploadIndex == 1 {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"status":500,"message":"temporary upload failure"}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"uploaded-second","object":"file","bytes":17,"created_at":1700000000,"filename":"second.txt","purpose":"general"}`))
			default:
				t.Fatalf("unexpected uploaded filename: %q", header.Filename)
			}
		case "/v1/chat/completions":
			chatRequests.Add(1)
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("failed to read chat body: %v", err)
			}
			var payload map[string]interface{}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("failed to unmarshal chat body %s: %v", body, err)
			}
			messages, ok := payload["messages"].([]interface{})
			if !ok || len(messages) != 1 {
				t.Fatalf("messages mismatch: %#v", payload["messages"])
			}
			message, ok := messages[0].(map[string]interface{})
			if !ok {
				t.Fatalf("message shape mismatch: %#v", messages[0])
			}
			attachments, ok := message["attachments"].([]interface{})
			if !ok || len(attachments) != 2 || attachments[0] != "uploaded-first" || attachments[1] != "uploaded-second" {
				t.Fatalf("attachments mismatch: %#v body %s", message["attachments"], body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"model":"GigaChat","object":"chat.completion"}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	prompt := "Summarize both files."
	firstFilename := "first.txt"
	firstFileData := "data:text/plain;base64,Zmlyc3QgYXR0YWNobWVudA=="
	secondFilename := "second.txt"
	secondFileData := "data:text/plain;base64,c2Vjb25kIGF0dGFjaG1lbnQ="
	request := &schemas.BifrostChatRequest{
		Model: "GigaChat",
		Input: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{
				ContentBlocks: []schemas.ChatContentBlock{
					{Type: schemas.ChatContentBlockTypeText, Text: &prompt},
					{
						Type: schemas.ChatContentBlockTypeFile,
						File: &schemas.ChatInputFile{
							Filename: &firstFilename,
							FileData: &firstFileData,
						},
					},
					{
						Type: schemas.ChatContentBlockTypeFile,
						File: &schemas.ChatInputFile{
							Filename: &secondFilename,
							FileData: &secondFileData,
						},
					},
				},
			},
		}},
	}

	provider := newTestGigaChatChatProvider(t, server.URL)
	parentCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := schemas.NewBifrostContext(parentCtx, schemas.NoDeadline)
	key := testGigaChatAccessTokenKey("file-token")

	firstResponse, firstErr := provider.ChatCompletion(ctx, key, request)
	if firstResponse != nil {
		t.Fatalf("expected nil response from partial upload failure, got %#v", firstResponse)
	}
	if firstErr == nil || firstErr.StatusCode == nil || *firstErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected partial upload 500, got %#v", firstErr)
	}

	response, bifrostErr := provider.ChatCompletion(ctx, key, request)
	if bifrostErr != nil {
		t.Fatalf("second ChatCompletion returned error: %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("expected second response, got nil")
	}
	if firstFileUploads.Load() != 1 {
		t.Fatalf("first attachment upload count mismatch: got %d, want 1", firstFileUploads.Load())
	}
	if secondFileUploads.Load() != 2 {
		t.Fatalf("second attachment upload count mismatch: got %d, want 2", secondFileUploads.Load())
	}
	if chatRequests.Load() != 1 {
		t.Fatalf("chat request count mismatch: got %d, want 1", chatRequests.Load())
	}
}

func testGigaChatChatCompletionDoesNotReuseUploadedAttachmentAcrossIndependentRequests(t *testing.T) {
	t.Parallel()

	var uploadRequests atomic.Int32
	var chatRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/files":
			uploadIndex := uploadRequests.Add(1)
			if err := request.ParseMultipartForm(1024); err != nil {
				t.Fatalf("failed to parse upload multipart form: %v", err)
			}
			file, _, err := request.FormFile("file")
			if err != nil {
				t.Fatalf("failed to read uploaded file: %v", err)
			}
			defer file.Close()
			fileBytes, err := io.ReadAll(file)
			if err != nil {
				t.Fatalf("failed to read uploaded bytes: %v", err)
			}
			if string(fileBytes) != "%PDF independent" {
				t.Fatalf("uploaded file bytes mismatch: %q", fileBytes)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"uploaded-independent-` + formatInt32(uploadIndex) + `","object":"file","bytes":16,"created_at":1700000000,"filename":"independent.pdf","purpose":"general"}`))
		case "/v1/chat/completions":
			chatIndex := chatRequests.Add(1)
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("failed to read chat body: %v", err)
			}
			assertGigaChatChatBodyAttachment(t, body, "uploaded-independent-"+formatInt32(chatIndex))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"model":"GigaChat","object":"chat.completion"}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	ctx := testBifrostContext()
	key := testGigaChatAccessTokenKey("file-token")

	firstResponse, firstErr := provider.ChatCompletion(ctx, key, testGigaChatInlineFileChatRequest("independent.pdf", "data:application/pdf;base64,JVBERiBpbmRlcGVuZGVudA=="))
	if firstErr != nil {
		t.Fatalf("first ChatCompletion returned error: %v", firstErr)
	}
	if firstResponse == nil {
		t.Fatal("expected first response, got nil")
	}

	secondResponse, secondErr := provider.ChatCompletion(ctx, key, testGigaChatInlineFileChatRequest("independent.pdf", "data:application/pdf;base64,JVBERiBpbmRlcGVuZGVudA=="))
	if secondErr != nil {
		t.Fatalf("second ChatCompletion returned error: %v", secondErr)
	}
	if secondResponse == nil {
		t.Fatal("expected second response, got nil")
	}

	if uploadRequests.Load() != 2 {
		t.Fatalf("upload request count mismatch: got %d, want 2", uploadRequests.Load())
	}
	if chatRequests.Load() != 2 {
		t.Fatalf("chat request count mismatch: got %d, want 2", chatRequests.Load())
	}
}

func testGigaChatChatCompletionDoesNotCacheFailedAttachmentUpload(t *testing.T) {
	t.Parallel()

	var uploadRequests atomic.Int32
	var chatRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/files":
			uploadIndex := uploadRequests.Add(1)
			if err := request.ParseMultipartForm(1024); err != nil {
				t.Fatalf("failed to parse upload multipart form: %v", err)
			}
			file, _, err := request.FormFile("file")
			if err != nil {
				t.Fatalf("failed to read uploaded file: %v", err)
			}
			defer file.Close()
			fileBytes, err := io.ReadAll(file)
			if err != nil {
				t.Fatalf("failed to read uploaded bytes: %v", err)
			}
			if string(fileBytes) != "stale-secret-inline" {
				t.Fatalf("uploaded file bytes mismatch: %q", fileBytes)
			}
			w.Header().Set("Content-Type", "application/json")
			if uploadIndex == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"status":500,"message":"upload failed","id":"stale-file-id"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"uploaded-after-error","object":"file","bytes":19,"created_at":1700000000,"filename":"secret.txt","purpose":"general"}`))
		case "/v1/chat/completions":
			chatRequests.Add(1)
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("failed to read chat body: %v", err)
			}
			assertGigaChatChatBodyAttachment(t, body, "uploaded-after-error")
			if strings.Contains(string(body), "stale-file-id") {
				t.Fatalf("chat body used stale file id: %s", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"model":"GigaChat","object":"chat.completion"}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	provider.sendBackRawRequest = true
	provider.sendBackRawResponse = true
	ctx := testBifrostContext()
	key := testGigaChatAccessTokenKey("file-token")
	request := testGigaChatInlineFileChatRequest("secret.txt", "data:text/plain;base64,c3RhbGUtc2VjcmV0LWlubGluZQ==")

	firstResponse, firstErr := provider.ChatCompletion(ctx, key, request)
	if firstResponse != nil {
		t.Fatalf("expected nil response from failed upload, got %#v", firstResponse)
	}
	if firstErr == nil || firstErr.StatusCode == nil || *firstErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected upload 500, got %#v", firstErr)
	}
	if cacheID := ctx.Value(gigaChatAttachmentCacheKey); cacheID != nil {
		t.Fatalf("failed attachment upload allocated context cache state: %#v", cacheID)
	}
	provider.attachmentCache.mu.Lock()
	cacheEntries := len(provider.attachmentCache.entries)
	provider.attachmentCache.mu.Unlock()
	if cacheEntries != 0 {
		t.Fatalf("failed attachment upload retained %d cache entries", cacheEntries)
	}
	firstErrorOutput := firstErr.String() + stringifyGigaChatRaw(firstErr.ExtraFields.RawRequest) + stringifyGigaChatRaw(firstErr.ExtraFields.RawResponse)
	if strings.Contains(firstErrorOutput, "stale-secret-inline") || strings.Contains(firstErrorOutput, "c3RhbGUtc2VjcmV0LWlubGluZQ") {
		t.Fatalf("failed upload leaked inline payload in error output: %s", firstErrorOutput)
	}

	response, bifrostErr := provider.ChatCompletion(ctx, key, request)
	if bifrostErr != nil {
		t.Fatalf("second ChatCompletion returned error: %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("expected second response, got nil")
	}
	if uploadRequests.Load() != 2 {
		t.Fatalf("upload request count mismatch: got %d, want 2", uploadRequests.Load())
	}
	if chatRequests.Load() != 1 {
		t.Fatalf("chat request count mismatch: got %d, want 1", chatRequests.Load())
	}
}

func testGigaChatChatCompletionRejectsUnsupportedTools(t *testing.T) {
	t.Parallel()

	text := "hello"
	request := &schemas.BifrostChatRequest{
		Model: "GigaChat",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &text}},
		},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{
				{
					Type: schemas.ChatToolTypeFunction,
					Function: &schemas.ChatToolFunction{
						Name: "get_weather",
					},
				},
			},
		},
	}

	_, err := ToGigaChatChatRequest(testBifrostContext(), request)
	if err == nil {
		t.Fatal("expected unsupported tools error, got nil")
	}
	if !strings.Contains(err.Error(), "tools") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func testGigaChatChatCompletionRejectsUnsupportedResponseFormat(t *testing.T) {
	t.Parallel()

	responseFormat := interface{}(map[string]interface{}{"type": "json_object"})
	request := testGigaChatChatRequest()
	request.Params.ResponseFormat = &responseFormat

	_, err := ToGigaChatChatRequest(testBifrostContext(), request)
	if err == nil {
		t.Fatal("expected unsupported response_format error, got nil")
	}
	if !strings.Contains(err.Error(), `response_format type "json_object" is not supported`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func testGigaChatChatCompletionMapsProviderErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":400,"code":123,"message":"bad request"}`))
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	response, bifrostErr := provider.ChatCompletion(testBifrostContext(), testGigaChatAccessTokenKey("provider-error-token"), testGigaChatChatRequest())
	if response != nil {
		t.Fatalf("expected nil response, got %#v", response)
	}
	if bifrostErr == nil {
		t.Fatal("expected provider error, got nil")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("status mismatch: %#v", bifrostErr.StatusCode)
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Message != "bad request" {
		t.Fatalf("message mismatch: %#v", bifrostErr.Error)
	}
	if bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "123" {
		t.Fatalf("code mismatch: %#v", bifrostErr.Error)
	}
	assertNoGigaChatSecretLeak(t, bifrostErr.String())
}

func testGigaChatChatCompletionRefreshesTokenAfterUnauthorized(t *testing.T) {
	t.Parallel()

	var tokenRequests atomic.Int32
	var chatRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth":
			tokenIndex := tokenRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"access_token":"token-%d","expires_at":1893456000}`, tokenIndex)))
		case "/v1/chat/completions":
			chatIndex := chatRequests.Add(1)
			wantAuthorization := fmt.Sprintf("Bearer token-%d", chatIndex)
			if got := request.Header.Get("Authorization"); got != wantAuthorization {
				t.Fatalf("authorization header mismatch on request %d: got %q, want %q", chatIndex, got, wantAuthorization)
			}
			w.Header().Set("Content-Type", "application/json")
			if chatIndex == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"status":401,"message":"expired token"}`))
				return
			}
			_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"model":"GigaChat","object":"chat.completion"}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	response, bifrostErr := provider.ChatCompletion(testBifrostContext(), testGigaChatOAuthKey(server.URL+"/oauth", "", "test-credentials"), testGigaChatChatRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion returned error: %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("expected response, got nil")
	}
	if tokenRequests.Load() != 2 {
		t.Fatalf("token request count mismatch: got %d, want 2", tokenRequests.Load())
	}
	if chatRequests.Load() != 2 {
		t.Fatalf("chat request count mismatch: got %d, want 2", chatRequests.Load())
	}
}

func testGigaChatChatCompletionDoesNotDoubleExchangeExpiredTokenOnRefresh(t *testing.T) {
	t.Parallel()

	var nowUnix atomic.Int64
	nowUnix.Store(time.Unix(1_700_000_000, 0).Unix())
	currentNow := func() time.Time {
		return time.Unix(nowUnix.Load(), 0)
	}
	var tokenRequests atomic.Int32
	var chatRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth":
			tokenIndex := tokenRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"access_token":"token-%d","expires_at":%d}`, tokenIndex, currentNow().Add(30*time.Minute).Unix())))
		case "/v1/chat/completions":
			chatIndex := chatRequests.Add(1)
			wantAuthorization := fmt.Sprintf("Bearer token-%d", chatIndex)
			if got := request.Header.Get("Authorization"); got != wantAuthorization {
				t.Fatalf("authorization header mismatch on request %d: got %q, want %q", chatIndex, got, wantAuthorization)
			}
			w.Header().Set("Content-Type", "application/json")
			if chatIndex == 1 {
				nowUnix.Add(int64(31 * time.Minute / time.Second))
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"status":401,"message":"expired token"}`))
				return
			}
			_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"model":"GigaChat","object":"chat.completion"}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	provider.tokenCache = newGigaChatTokenCache(currentNow)

	response, bifrostErr := provider.ChatCompletion(testBifrostContext(), testGigaChatOAuthKey(server.URL+"/oauth", "", "test-credentials"), testGigaChatChatRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion returned error: %v", bifrostErr)
	}
	if response == nil || len(response.Choices) != 1 {
		t.Fatalf("unexpected response: %#v", response)
	}
	if tokenRequests.Load() != 2 {
		t.Fatalf("token request count mismatch: got %d, want 2", tokenRequests.Load())
	}
	if chatRequests.Load() != 2 {
		t.Fatalf("chat request count mismatch: got %d, want 2", chatRequests.Load())
	}
}

func testGigaChatChatCompletionStreamsSSEChunks(t *testing.T) {
	t.Parallel()

	var tokenRequests atomic.Int32
	var streamRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth":
			tokenRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"stream-access-token","expires_at":1893456000}`))
		case "/v1/chat/completions":
			streamRequests.Add(1)
			if got := request.Header.Get("Authorization"); got != "Bearer stream-access-token" {
				t.Fatalf("stream authorization header mismatch: got %q", got)
			}
			if strings.Contains(request.Header.Get("Authorization"), "super-secret-credentials") {
				t.Fatal("stream request leaked OAuth credentials")
			}
			assertGigaChatChatStreamRequestBody(t, request)
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("X-Request-ID", "stream-request-id")
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-stream\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"Думаю\",\"content\":\"З\"}}],\"created\":1700000000,\"model\":\"GigaChat\",\"object\":\"chat.completion\"}\n\n"))
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-stream\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"дравствуйте\"}}],\"created\":1700000000,\"model\":\"GigaChat\",\"object\":\"chat.completion\"}\n\n"))
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-stream\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"created\":1700000000,\"model\":\"GigaChat\",\"object\":\"chat.completion\",\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10}}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	provider.sendBackRawRequest = true
	provider.sendBackRawResponse = true
	ctx := testBifrostContext()
	ctx.SetValue(schemas.BifrostContextKeyCaptureRawRequest, true)
	ctx.SetValue(schemas.BifrostContextKeyCaptureRawResponse, true)
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)

	stream, bifrostErr := provider.ChatCompletionStream(ctx, testGigaChatPostHookRunner, nil, testGigaChatOAuthKey(server.URL+"/oauth", "", "super-secret-credentials"), testGigaChatChatRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", bifrostErr)
	}

	chunks := collectGigaChatStreamChunks(t, stream)
	if tokenRequests.Load() != 1 {
		t.Fatalf("token request count mismatch: got %d, want 1", tokenRequests.Load())
	}
	if streamRequests.Load() != 1 {
		t.Fatalf("stream request count mismatch: got %d, want 1", streamRequests.Load())
	}
	if len(chunks) != 3 {
		t.Fatalf("chunk count mismatch: got %d, want 3: %#v", len(chunks), chunks)
	}

	assertGigaChatStreamContentChunk(t, chunks[0], "З")
	assertGigaChatStreamReasoningChunk(t, chunks[0], "Думаю")
	if chunks[0].BifrostChatResponse.ExtraFields.RawResponse == nil {
		t.Fatal("expected raw response on content chunk")
	}
	assertGigaChatStreamContentChunk(t, chunks[1], "дравствуйте")
	if chunks[1].BifrostChatResponse.ExtraFields.RawResponse == nil {
		t.Fatal("expected raw response on content chunk")
	}
	finalChunk := chunks[2].BifrostChatResponse
	if finalChunk == nil {
		t.Fatalf("final chunk missing chat response: %#v", chunks[2])
	}
	if finalChunk.ExtraFields.Provider != schemas.GigaChat {
		t.Fatalf("provider mismatch: got %q, want %q", finalChunk.ExtraFields.Provider, schemas.GigaChat)
	}
	if finalChunk.Usage == nil || finalChunk.Usage.TotalTokens != 10 {
		t.Fatalf("usage mismatch: %#v", finalChunk.Usage)
	}
	if len(finalChunk.Choices) != 1 || finalChunk.Choices[0].FinishReason == nil || *finalChunk.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish reason mismatch: %#v", finalChunk.Choices)
	}
	if finalChunk.ExtraFields.RawRequest == nil {
		t.Fatalf("expected raw request on final chunk, got request=%#v", finalChunk.ExtraFields.RawRequest)
	}
	if got := ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders); got == nil {
		t.Fatal("provider response headers were not stored in context")
	}
}

func testGigaChatChatCompletionStreamFinalizesLargeResponsePassthrough(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		assertGigaChatChatStreamRequestBody(t, request)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-large\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"large\"}}],\"created\":1700000000,\"model\":\"GigaChat\",\"object\":\"chat.completion\"}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	ctx := testBifrostContext()
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)

	var finalizerCalls atomic.Int32
	stream, bifrostErr := provider.ChatCompletionStream(ctx, testGigaChatPostHookRunner, func(context.Context) {
		finalizerCalls.Add(1)
	}, testGigaChatAccessTokenKey("chat-stream-token"), testGigaChatChatRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", bifrostErr)
	}

	select {
	case chunk, ok := <-stream:
		if ok {
			t.Fatalf("passthrough stream channel should be closed without chunks, got %#v", chunk)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for passthrough stream channel to close")
	}
	if got := finalizerCalls.Load(); got != 0 {
		t.Fatalf("finalizer ran before passthrough delivery: got %d calls", got)
	}

	reader, ok := ctx.Value(schemas.BifrostContextKeyLargeResponseReader).(io.ReadCloser)
	if !ok || reader == nil {
		t.Fatalf("large response reader missing from context: %#v", ctx.Value(schemas.BifrostContextKeyLargeResponseReader))
	}
	finalizingReader, ok := reader.(*gigaChatPassthroughReadCloser)
	if !ok {
		t.Fatalf("passthrough reader type mismatch: %T", reader)
	}
	largeReader, ok := finalizingReader.ReadCloser.(*providerUtils.LargeResponseReader)
	if !ok {
		t.Fatalf("wrapped large response reader type mismatch: %T", finalizingReader.ReadCloser)
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read passthrough body: %v", err)
	}
	if !strings.Contains(string(body), `"id":"chatcmpl-large"`) {
		t.Fatalf("passthrough body mismatch: %s", body)
	}
	if got := finalizerCalls.Load(); got != 0 {
		t.Fatalf("finalizer ran before passthrough reader close: got %d calls", got)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("failed to close large response reader: %v", err)
	}
	if got := finalizerCalls.Load(); got != 1 {
		t.Fatalf("finalizer calls mismatch after passthrough delivery: got %d, want 1", got)
	}
	if ended, _ := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator).(bool); !ended {
		t.Fatal("passthrough stream was not marked complete before finalization")
	}
	if largeReader.Resp != nil {
		t.Fatal("large response reader did not release its fasthttp response")
	}
}

func testGigaChatChatCompletionMapsStreamingProviderErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":400,"code":123,"message":"bad stream request"}`))
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(testBifrostContext(), testGigaChatPostHookRunner, nil, testGigaChatAccessTokenKey("provider-error-token"), testGigaChatChatRequest())
	if stream != nil {
		t.Fatalf("expected nil stream, got %#v", stream)
	}
	if bifrostErr == nil {
		t.Fatal("expected provider error, got nil")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("status mismatch: %#v", bifrostErr.StatusCode)
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Message != "bad stream request" {
		t.Fatalf("message mismatch: %#v", bifrostErr.Error)
	}
	assertNoGigaChatSecretLeak(t, bifrostErr.String())
}

func testGigaChatChatCompletionMapsStreamingErrorEvents(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"status\":429,\"code\":42901,\"message\":\"rate limit\"}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(testBifrostContext(), testGigaChatPostHookRunner, nil, testGigaChatAccessTokenKey("provider-error-token"), testGigaChatChatRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream returned error before stream: %v", bifrostErr)
	}
	chunks := collectGigaChatStreamChunks(t, stream)
	if len(chunks) != 1 || chunks[0].BifrostError == nil {
		t.Fatalf("expected one error chunk, got %#v", chunks)
	}
	streamErr := chunks[0].BifrostError
	if streamErr.StatusCode == nil || *streamErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status mismatch: %#v", streamErr.StatusCode)
	}
	if streamErr.Error == nil || streamErr.Error.Message != "rate limit" {
		t.Fatalf("message mismatch: %#v", streamErr.Error)
	}
	if streamErr.Error.Code == nil || *streamErr.Error.Code != "42901" {
		t.Fatalf("code mismatch: %#v", streamErr.Error)
	}
}

func testGigaChatChatCompletionStreamRefreshesTokenAfterUnauthorized(t *testing.T) {
	t.Parallel()

	var tokenRequests atomic.Int32
	var streamRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth":
			tokenIndex := tokenRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"access_token":"stream-token-%d","expires_at":1893456000}`, tokenIndex)))
		case "/v1/chat/completions":
			streamIndex := streamRequests.Add(1)
			wantAuthorization := fmt.Sprintf("Bearer stream-token-%d", streamIndex)
			if got := request.Header.Get("Authorization"); got != wantAuthorization {
				t.Fatalf("authorization header mismatch on stream request %d: got %q, want %q", streamIndex, got, wantAuthorization)
			}
			if streamIndex == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"status":401,"message":"expired token"}`))
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}],\"model\":\"GigaChat\",\"object\":\"chat.completion\"}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	stream, bifrostErr := provider.ChatCompletionStream(testBifrostContext(), testGigaChatPostHookRunner, nil, testGigaChatOAuthKey(server.URL+"/oauth", "", "test-credentials"), testGigaChatChatRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", bifrostErr)
	}
	chunks := collectGigaChatStreamChunks(t, stream)
	if tokenRequests.Load() != 2 {
		t.Fatalf("token request count mismatch: got %d, want 2", tokenRequests.Load())
	}
	if streamRequests.Load() != 2 {
		t.Fatalf("stream request count mismatch: got %d, want 2", streamRequests.Load())
	}
	if len(chunks) != 2 {
		t.Fatalf("chunk count mismatch: got %d, want 2: %#v", len(chunks), chunks)
	}
	assertGigaChatStreamContentChunk(t, chunks[0], "ok")
}

func testGigaChatChatCompletionStreamHandlesContextCancellation(t *testing.T) {
	t.Parallel()

	firstChunkWritten := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}],\"model\":\"GigaChat\",\"object\":\"chat.completion\"}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(firstChunkWritten)
		<-request.Context().Done()
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	stream, bifrostErr := provider.ChatCompletionStream(ctx, testGigaChatPostHookRunner, nil, testGigaChatAccessTokenKey("stream-token"), testGigaChatChatRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", bifrostErr)
	}

	select {
	case <-firstChunkWritten:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first stream chunk")
	}

	firstChunk := <-stream
	assertGigaChatStreamContentChunk(t, firstChunk, "partial")
	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for context cancellation")
	}

	streamClosed := make(chan struct{})
	go func() {
		for range stream {
		}
		close(streamClosed)
	}()

	select {
	case <-streamClosed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stream to close after context cancellation")
	}
}

func newTestGigaChatChatProvider(t *testing.T, baseURL string) *GigaChatProvider {
	t.Helper()

	provider, err := NewGigaChatProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: baseURL,
		},
	}, nil)
	if err != nil {
		t.Fatalf("NewGigaChatProvider returned error: %v", err)
	}
	dialer := &net.Dialer{}
	provider.client.Dial = func(addr string) (net.Conn, error) {
		return dialer.Dial("tcp", addr)
	}
	provider.client.DialTimeout = nil
	provider.streamingClient.Dial = provider.client.Dial
	provider.streamingClient.DialTimeout = nil
	return provider
}

func testGigaChatPostHookRunner(_ *schemas.BifrostContext, response *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return response, bifrostErr
}

func testGigaChatAccessTokenKey(accessToken string) schemas.Key {
	return schemas.Key{
		GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
			AccessToken: schemas.NewSecretVar(accessToken),
		},
	}
}

func testGigaChatChatRequest() *schemas.BifrostChatRequest {
	maxTokens := 128
	text := "Привет"
	return &schemas.BifrostChatRequest{
		Model: "GigaChat",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: &text},
			},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: &maxTokens,
			ExtraParams: map[string]interface{}{
				"profanity_check": false,
			},
		},
	}
}

func testGigaChatInlineFileChatRequest(filename string, fileData string) *schemas.BifrostChatRequest {
	prompt := "Summarize this file."
	return &schemas.BifrostChatRequest{
		Model: "GigaChat",
		Input: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{
				ContentBlocks: []schemas.ChatContentBlock{
					{Type: schemas.ChatContentBlockTypeText, Text: &prompt},
					{
						Type: schemas.ChatContentBlockTypeFile,
						File: &schemas.ChatInputFile{
							Filename: &filename,
							FileData: &fileData,
						},
					},
				},
			},
		}},
	}
}

func assertGigaChatChatRequestBody(t *testing.T, request *http.Request) {
	t.Helper()

	assertGigaChatChatRequestBodyWithStream(t, request, false)
}

func assertGigaChatChatStreamRequestBody(t *testing.T, request *http.Request) {
	t.Helper()

	assertGigaChatChatRequestBodyWithStream(t, request, true)
}

func assertGigaChatChatRequestBodyWithStream(t *testing.T, request *http.Request, wantStream bool) {
	t.Helper()

	if request.Method != http.MethodPost {
		t.Fatalf("method mismatch: got %s, want POST", request.Method)
	}
	if got := request.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content type mismatch: got %q", got)
	}
	if got := request.Header.Get(gigaChatUserAgentHeader); got != gigaChatUserAgent {
		t.Fatalf("user-agent mismatch: got %q, want %q", got, gigaChatUserAgent)
	}

	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("failed to read request body: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("failed to unmarshal request body %s: %v", body, err)
	}
	if got := payload["model"]; got != "GigaChat" {
		t.Fatalf("model mismatch: got %#v", got)
	}
	if got := payload["max_tokens"]; got != float64(128) {
		t.Fatalf("max_tokens mismatch: got %#v", got)
	}
	if _, ok := payload["max_completion_tokens"]; ok {
		t.Fatalf("max_completion_tokens should not be sent: %s", body)
	}
	if got := payload["stream"]; got != wantStream {
		t.Fatalf("stream mismatch: got %#v, want %v", got, wantStream)
	}
	if got := payload["profanity_check"]; got != false {
		t.Fatalf("profanity_check mismatch: got %#v", got)
	}
	messages, ok := payload["messages"].([]interface{})
	if !ok || len(messages) != 1 {
		t.Fatalf("messages mismatch: %#v", payload["messages"])
	}
	message, ok := messages[0].(map[string]interface{})
	if !ok {
		t.Fatalf("message shape mismatch: %#v", messages[0])
	}
	if got := message["role"]; got != "user" {
		t.Fatalf("message role mismatch: got %#v", got)
	}
	if got := message["content"]; got != "Привет" {
		t.Fatalf("message content mismatch: got %#v", got)
	}
}

func assertGigaChatChatBodyAttachment(t *testing.T, body []byte, wantAttachment string) map[string]interface{} {
	t.Helper()

	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("failed to unmarshal chat body %s: %v", body, err)
	}
	messages, ok := payload["messages"].([]interface{})
	if !ok || len(messages) != 1 {
		t.Fatalf("messages mismatch: %#v", payload["messages"])
	}
	message, ok := messages[0].(map[string]interface{})
	if !ok {
		t.Fatalf("message shape mismatch: %#v", messages[0])
	}
	attachments, ok := message["attachments"].([]interface{})
	if !ok || len(attachments) != 1 || attachments[0] != wantAttachment {
		t.Fatalf("attachments mismatch: %#v body %s", message["attachments"], body)
	}
	return payload
}

func assertGigaChatJSONSchemaResponseFormat(t *testing.T, responseFormat interface{}, wantTitle string, wantDescription string, wantStrict bool) {
	t.Helper()

	formatMap, ok := schemas.SafeExtractOrderedMap(responseFormat)
	if !ok || formatMap == nil {
		t.Fatalf("response_format should be a JSON object: %#v", responseFormat)
	}
	if got, _ := formatMap.Get("type"); got != "json_schema" {
		t.Fatalf("response_format type mismatch: got %#v", got)
	}
	if _, hasOpenAIWrapper := formatMap.Get("json_schema"); hasOpenAIWrapper {
		t.Fatalf("response_format should not contain OpenAI json_schema wrapper: %#v", formatMap)
	}
	strictRaw, ok := formatMap.Get("strict")
	if !ok {
		t.Fatal("response_format strict is missing")
	}
	strict, ok := schemas.SafeExtractBool(strictRaw)
	if !ok || strict != wantStrict {
		t.Fatalf("response_format strict mismatch: got %#v, want %v", strictRaw, wantStrict)
	}
	schemaRaw, ok := formatMap.Get("schema")
	if !ok {
		t.Fatal("response_format schema is missing")
	}
	schemaMap, ok := schemas.SafeExtractOrderedMap(schemaRaw)
	if !ok || schemaMap == nil {
		t.Fatalf("response_format schema should be a JSON object: %#v", schemaRaw)
	}
	if got, _ := schemaMap.Get("type"); got != "object" {
		t.Fatalf("schema type mismatch: got %#v", got)
	}
	if wantTitle != "" {
		if got, _ := schemaMap.Get("title"); got != wantTitle {
			t.Fatalf("schema title mismatch: got %#v, want %q", got, wantTitle)
		}
	}
	if wantDescription != "" {
		if got, _ := schemaMap.Get("description"); got != wantDescription {
			t.Fatalf("schema description mismatch: got %#v, want %q", got, wantDescription)
		}
	}
}

func collectGigaChatStreamChunks(t *testing.T, stream chan *schemas.BifrostStreamChunk) []*schemas.BifrostStreamChunk {
	t.Helper()

	chunks := make([]*schemas.BifrostStreamChunk, 0)
	for chunk := range stream {
		chunks = append(chunks, chunk)
	}
	return chunks
}

func assertGigaChatStreamContentChunk(t *testing.T, chunk *schemas.BifrostStreamChunk, wantContent string) {
	t.Helper()

	if chunk == nil || chunk.BifrostChatResponse == nil {
		t.Fatalf("missing chat stream response: %#v", chunk)
	}
	response := chunk.BifrostChatResponse
	if response.ExtraFields.Provider != schemas.GigaChat {
		t.Fatalf("provider mismatch: got %q, want %q", response.ExtraFields.Provider, schemas.GigaChat)
	}
	if len(response.Choices) != 1 || response.Choices[0].ChatStreamResponseChoice == nil || response.Choices[0].ChatStreamResponseChoice.Delta == nil {
		t.Fatalf("unexpected choices: %#v", response.Choices)
	}
	content := response.Choices[0].ChatStreamResponseChoice.Delta.Content
	if content == nil || *content != wantContent {
		t.Fatalf("content mismatch: got %#v, want %q", content, wantContent)
	}
}

func assertGigaChatStreamReasoningChunk(t *testing.T, chunk *schemas.BifrostStreamChunk, wantReasoning string) {
	t.Helper()

	if chunk == nil || chunk.BifrostChatResponse == nil {
		t.Fatalf("missing chat stream response: %#v", chunk)
	}
	response := chunk.BifrostChatResponse
	if len(response.Choices) != 1 || response.Choices[0].ChatStreamResponseChoice == nil || response.Choices[0].ChatStreamResponseChoice.Delta == nil {
		t.Fatalf("unexpected choices: %#v", response.Choices)
	}
	delta := response.Choices[0].ChatStreamResponseChoice.Delta
	if delta.Reasoning == nil || *delta.Reasoning != wantReasoning {
		t.Fatalf("reasoning mismatch: got %#v, want %q", delta.Reasoning, wantReasoning)
	}
	if len(delta.ReasoningDetails) != 1 || delta.ReasoningDetails[0].Text == nil || *delta.ReasoningDetails[0].Text != wantReasoning {
		t.Fatalf("reasoning details mismatch: %#v", delta.ReasoningDetails)
	}
}
