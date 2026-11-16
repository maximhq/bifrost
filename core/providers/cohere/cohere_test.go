package cohere_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/cohere"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestCohere(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("COHERE_API_KEY")) == "" {
		t.Skip("Skipping Cohere tests because COHERE_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:                 schemas.Cohere,
		ChatModel:                "command-a-03-2025",
		VisionModel:              "command-a-vision-07-2025", // Cohere's latest vision model
		TextModel:                "",                         // Cohere focuses on chat
		EmbeddingModel:           "embed-v4.0",
		MultimodalEmbeddingModel: "embed-v4.0",
		RerankModel:              "rerank-v3.5",
		ReasoningModel:           "command-a-reasoning-08-2025",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             false, // Not typical for Cohere
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         true,
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,  // May not support automatic
			ImageURL:                   false, // Supported by c4ai-aya-vision-8b model
			ImageBase64:                true,  // Supported by c4ai-aya-vision-8b model
			MultipleImages:             false, // Supported by c4ai-aya-vision-8b model
			FileBase64:                 false, // Not supported
			FileURL:                    false, // Not supported
			CompleteEnd2End:            false,
			Embedding:                  true,
			MultimodalEmbedding:        true,
			Rerank:                     true,
			Reasoning:                  true,
			ListModels:                 true,
			CountTokens:                true,
		},
	}

	t.Run("CohereTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// A retrieve must land on /v1/models/{model} with the key's bearer token, carry the
// deprecation flag through, surface an upstream 404, and refuse a path-shaping id.
func TestCohereModelRetrieve(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		mu.Unlock()
		if r.URL.Path == "/v1/models/nope" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"model 'nope' not found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"name":"command-r-08-2024","is_deprecated":true,"endpoints":["chat","generate"],"finetuned":false,"context_length":128000,"tokenizer_url":"https://example.invalid/tokenizer.json","default_endpoints":["chat"],"features":["tools","json_mode"]}`))
	}))
	defer server.Close()

	provider, err := cohere.NewCohereProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("co-test")}

	response, bifrostErr := provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "command-r-08-2024"})
	require.Nil(t, bifrostErr)

	mu.Lock()
	path, auth := gotPath, gotAuth
	mu.Unlock()
	require.Equal(t, "/v1/models/command-r-08-2024", path)
	require.Equal(t, "Bearer co-test", auth)

	require.Equal(t, "cohere/command-r-08-2024", response.ID)
	require.Equal(t, schemas.Ptr(128000), response.ContextLength)
	require.True(t, response.IsDeprecated)
	require.Equal(t, []string{"chat", "generate"}, response.SupportedMethods)

	_, bifrostErr = provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "nope"})
	require.NotNil(t, bifrostErr)
	require.Equal(t, schemas.Ptr(http.StatusNotFound), bifrostErr.StatusCode)

	_, bifrostErr = provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "../models"})
	require.NotNil(t, bifrostErr)
	require.Equal(t, schemas.Ptr(http.StatusBadRequest), bifrostErr.StatusCode)
}

// TestToCohereResponsesRequest_InstructionsLeadSystemInput: top-level instructions and
// system/developer input both reach the system turn, instructions first (OpenAI places
// instructions ahead of input). Previously instructions were dropped whenever the input
// carried system content.
func TestToCohereResponsesRequest_InstructionsLeadSystemInput(t *testing.T) {
	user := schemas.ResponsesMessage{
		Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
	}
	developer := schemas.ResponsesMessage{
		Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleDeveloper),
		Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("DEVELOPER_MUST_SURVIVE")},
	}

	tests := []struct {
		name         string
		instructions *string
		input        []schemas.ResponsesMessage
		want         string
	}{
		{"instructions and developer input both reach system, instructions first", schemas.Ptr("BASE_MUST_SURVIVE"), []schemas.ResponsesMessage{developer, user}, "BASE_MUST_SURVIVE\nDEVELOPER_MUST_SURVIVE"},
		{"instructions alone", schemas.Ptr("BASE_MUST_SURVIVE"), []schemas.ResponsesMessage{user}, "BASE_MUST_SURVIVE"},
		{"developer input alone", nil, []schemas.ResponsesMessage{developer, user}, "DEVELOPER_MUST_SURVIVE"},
		{"empty instructions add nothing", schemas.Ptr(""), []schemas.ResponsesMessage{developer, user}, "DEVELOPER_MUST_SURVIVE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := cohere.ToCohereResponsesRequest(&schemas.BifrostResponsesRequest{
				Provider: schemas.Cohere,
				Model:    "command-a-03-2025",
				Input:    tc.input,
				Params:   &schemas.ResponsesParameters{Instructions: tc.instructions},
			})
			require.NoError(t, err)
			require.Len(t, out.Messages, 2)
			require.Equal(t, "system", out.Messages[0].Role)
			require.NotNil(t, out.Messages[0].Content)
			require.NotNil(t, out.Messages[0].Content.StringContent)
			require.Equal(t, tc.want, *out.Messages[0].Content.StringContent)
			require.Equal(t, "user", out.Messages[1].Role)
		})
	}
}

// Media in a Responses tool output reaches Cohere as a placeholder, as on the Chat path.
func TestToCohereResponsesRequest_ToolOutputNamesMedia(t *testing.T) {
	req, err := cohere.ToCohereResponsesRequest(&schemas.BifrostResponsesRequest{
		Provider: schemas.Cohere,
		Model:    "command-a-03-2025",
		Input: []schemas.ResponsesMessage{
			{
				Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: schemas.Ptr("c1"), Name: schemas.Ptr("chart"), Arguments: schemas.Ptr(`{}`)},
			},
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("c1"),
					Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{
						{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: schemas.Ptr("chart below")},
						{Type: schemas.ResponsesInputMessageContentBlockTypeImage, ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{ImageURL: schemas.Ptr("data:image/png;base64,iVBORw0KGgo=")}},
						{Type: schemas.ResponsesInputMessageContentBlockTypeFile, ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{FileData: schemas.Ptr("JVBERi0xLjQK"), FileType: schemas.Ptr("application/pdf"), Filename: schemas.Ptr("report.pdf")}},
					}},
				},
			},
		},
	})
	require.NoError(t, err)
	var toolMsg *cohere.CohereMessage
	for i := range req.Messages {
		if req.Messages[i].Role == "tool" {
			toolMsg = &req.Messages[i]
		}
	}
	require.NotNil(t, toolMsg, "no tool message")
	wire, err := schemas.MarshalSorted(toolMsg)
	require.NoError(t, err)
	for _, want := range []string{"chart below", "[image/png", "[application/pdf report.pdf"} {
		require.Contains(t, string(wire), want)
	}
	require.NotContains(t, string(wire), "image_url", "no image part may reach a Cohere tool message")
}

// TestCohereResponsesStreamAttachesEachRawEventOnce verifies a Cohere event that expands into several Responses events
// rides on exactly one of them, the one streaming its text, declared as Cohere's format, so its raw text never
// reaches the client ahead of (or apart from) the normalized text it backs.
func TestCohereResponsesStreamAttachesEachRawEventOnce(t *testing.T) {
	events := []string{
		`{"type":"message-start","id":"msg_1","delta":{"message":{"role":"assistant"}}}`,
		`{"type":"content-start","index":0,"delta":{"message":{"content":{"type":"text","text":""}}}}`,
		`{"type":"content-delta","index":0,"delta":{"message":{"content":{"text":"mail alex@example.com"}}}}`,
		`{"type":"content-end","index":0}`,
		`{"type":"message-end","delta":{"finish_reason":"COMPLETE","usage":{"billed_units":{"input_tokens":3,"output_tokens":4},"tokens":{"input_tokens":3,"output_tokens":4}}}}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			_, _ = w.Write([]byte("data: " + event + "\n\n"))
		}
	}))
	defer server.Close()

	provider, err := cohere.NewCohereProvider(&schemas.ProviderConfig{
		NetworkConfig:       schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 30},
		SendBackRawResponse: true,
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	request := &schemas.BifrostResponsesRequest{
		Provider: schemas.Cohere,
		Model:    "command-a-03-2025",
		Input: []schemas.ResponsesMessage{{
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}
	passthrough := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	stream, bifrostErr := provider.ResponsesStream(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), passthrough, nil, schemas.Key{Value: *schemas.NewSecretVar("co-test")}, request)
	require.Nil(t, bifrostErr)

	carried := map[string]int{}
	var textCarrier *schemas.BifrostResponsesStreamResponse
	for chunk := range stream {
		event := chunk.BifrostResponsesStreamResponse
		if event == nil {
			continue
		}
		if raw, ok := event.ExtraFields.RawResponse.(string); ok {
			carried[raw]++
			if raw == events[2] {
				textCarrier = event
			}
		}
	}
	for _, event := range events {
		require.LessOrEqual(t, carried[event], 1, "raw event attached more than once: %s", event)
	}
	require.NotNil(t, textCarrier, "the text event's raw payload was not attached")
	require.Equal(t, schemas.ResponsesStreamResponseTypeOutputTextDelta, textCarrier.Type)
	require.Equal(t, schemas.RawResponseFormatCohereChatStreamEvent, textCarrier.ExtraFields.RawResponseFormat)

	redacted := *textCarrier
	redacted.Delta = schemas.Ptr("mail [EMAIL]")
	synced, err := schemas.SyncRawResponseText(textCarrier.ExtraFields.RawResponse, textCarrier.ExtraFields.RawResponseFormat, textCarrier, &redacted)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"content-delta","index":0,"delta":{"message":{"content":{"text":"mail [EMAIL]"}}}}`, synced.(string))
}
