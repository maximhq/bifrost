package litellmcompat

import (
	"context"
	"net/http"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func testResponsesCompatRequest(requestType schemas.RequestType) *schemas.BifrostRequest {
	content := "hello"
	return &schemas.BifrostRequest{
		RequestType: requestType,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: schemas.ModelProvider("lmstudio"),
			Model:    "test-model",
			Input: []schemas.ResponsesMessage{{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &content,
				},
			}},
			Params: &schemas.ResponsesParameters{
				MaxOutputTokens: schemas.Ptr(7),
			},
		},
	}
}

func testResponsesCompatContext() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyCustomProviderMetadata, &schemas.CustomProviderContextMetadata{
		ProviderKey:          "lmstudio",
		BaseProviderType:     schemas.OpenAI,
		SupportsResponsesAPI: schemas.Ptr(false),
	})
	return ctx
}

func TestPreLLMHook_TransformsForcedResponsesRequestToChatCompletion(t *testing.T) {
	plugin := &LiteLLMCompatPlugin{config: Config{Enabled: true}}
	ctx := testResponsesCompatContext()

	transformedReq, shortCircuit, err := plugin.PreLLMHook(ctx, testResponsesCompatRequest(schemas.ResponsesRequest))
	if err != nil {
		t.Fatalf("PreLLMHook returned error: %v", err)
	}
	if shortCircuit != nil {
		t.Fatal("expected no short circuit")
	}
	if transformedReq == nil || transformedReq.ChatRequest == nil {
		t.Fatal("expected chat request after transform")
	}
	if transformedReq.RequestType != schemas.ChatCompletionRequest {
		t.Fatalf("expected request type %s, got %s", schemas.ChatCompletionRequest, transformedReq.RequestType)
	}
	if transformedReq.ChatRequest.Model != "test-model" {
		t.Fatalf("expected model test-model, got %s", transformedReq.ChatRequest.Model)
	}
	if len(transformedReq.ChatRequest.Input) == 0 {
		t.Fatal("expected transformed chat input")
	}
	if ctx.Value(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback) != true {
		t.Fatalf("expected fallback context marker to be set, got %v", ctx.Value(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback))
	}
	if reason, _ := ctx.Value(schemas.BifrostContextKeyResponsesToChatCompletionFallbackReason).(schemas.ResponsesToChatCompletionFallbackReason); reason != schemas.ResponsesToChatCompletionFallbackReasonConfiguredUnsupported {
		t.Fatalf("expected fallback reason %q, got %q", schemas.ResponsesToChatCompletionFallbackReasonConfiguredUnsupported, reason)
	}
	state, ok := schemas.GetResponsesToChatCompletionCompatState(ctx)
	if !ok || state == nil {
		t.Fatal("expected responses compat state to be stored on context")
	}
	if !state.Active || state.RetryEligible {
		t.Fatalf("expected active forced fallback state, got %+v", state)
	}

	chatText := "fallback response"
	finishReason := string(schemas.BifrostFinishReasonStop)
	result, bifrostErr, err := plugin.PostLLMHook(ctx, &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ID:      "chatcmpl-test",
			Created: 1,
			Model:   "test-model",
			Choices: []schemas.BifrostResponseChoice{{
				Index:        0,
				FinishReason: &finishReason,
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role: schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{
							ContentStr: &chatText,
						},
					},
				},
			}},
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider:       schemas.ModelProvider("lmstudio"),
				RequestType:    schemas.ChatCompletionRequest,
				ModelRequested: "test-model",
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("PostLLMHook returned error: %v", err)
	}
	if bifrostErr != nil {
		t.Fatalf("expected nil error, got %+v", bifrostErr)
	}
	if result == nil || result.ResponsesResponse == nil {
		t.Fatal("expected responses response after transform")
	}
	if result.ResponsesResponse.ExtraFields.RequestType != schemas.ResponsesRequest {
		t.Fatalf("expected request type %s, got %s", schemas.ResponsesRequest, result.ResponsesResponse.ExtraFields.RequestType)
	}
	if !result.ResponsesResponse.ExtraFields.LiteLLMCompat {
		t.Fatal("expected litellm compat marker to be set")
	}
	if len(result.ResponsesResponse.Output) == 0 || result.ResponsesResponse.Output[0].Content == nil || len(result.ResponsesResponse.Output[0].Content.ContentBlocks) == 0 {
		t.Fatal("expected transformed responses output")
	}
	if got := result.ResponsesResponse.Output[0].Content.ContentBlocks[0].Text; got == nil || *got != "fallback response" {
		t.Fatalf("expected fallback response text, got %+v", got)
	}
}

func TestPreLLMHook_PreparesRuntimeResponsesFallback(t *testing.T) {
	plugin := &LiteLLMCompatPlugin{config: Config{Enabled: true}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyCustomProviderMetadata, &schemas.CustomProviderContextMetadata{
		ProviderKey:      "lmstudio",
		BaseProviderType: schemas.OpenAI,
	})

	originalReq := testResponsesCompatRequest(schemas.ResponsesRequest)
	transformedReq, shortCircuit, err := plugin.PreLLMHook(ctx, originalReq)
	if err != nil {
		t.Fatalf("PreLLMHook returned error: %v", err)
	}
	if shortCircuit != nil {
		t.Fatal("expected no short circuit")
	}
	if transformedReq != originalReq {
		t.Fatal("expected native responses request to be kept for runtime probing")
	}
	if ctx.Value(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback) != nil {
		t.Fatalf("expected fallback context marker to stay unset before runtime retry, got %v", ctx.Value(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback))
	}
	state, ok := schemas.GetResponsesToChatCompletionCompatState(ctx)
	if !ok || state == nil {
		t.Fatal("expected compat state to be stored for runtime retry")
	}
	if state.Active || !state.RetryEligible {
		t.Fatalf("expected runtime retry state to be eligible but inactive, got %+v", state)
	}
	if state.RetryPolicy == nil {
		t.Fatal("expected runtime retry policy to be prepared by compat plugin")
	}
	if state.FallbackRequest == nil || state.FallbackRequest.ChatRequest == nil {
		t.Fatal("expected fallback chat request to be prepared")
	}
	if state.FallbackRequest.RequestType != schemas.ChatCompletionRequest {
		t.Fatalf("expected fallback request type %s, got %s", schemas.ChatCompletionRequest, state.FallbackRequest.RequestType)
	}
	if state.OriginalRequestType != schemas.ResponsesRequest {
		t.Fatalf("expected original request type %s, got %s", schemas.ResponsesRequest, state.OriginalRequestType)
	}
	statusCode := http.StatusNotFound
	if !state.ShouldRetry(&schemas.BifrostError{StatusCode: &statusCode}) {
		t.Fatal("expected compat state retry policy to treat 404 as runtime fallback trigger")
	}
}

func TestPreLLMHook_TransformsForcedResponsesStreamRequestToChatCompletionStream(t *testing.T) {
	plugin := &LiteLLMCompatPlugin{config: Config{Enabled: true}}
	ctx := testResponsesCompatContext()

	transformedReq, shortCircuit, err := plugin.PreLLMHook(ctx, testResponsesCompatRequest(schemas.ResponsesStreamRequest))
	if err != nil {
		t.Fatalf("PreLLMHook returned error: %v", err)
	}
	if shortCircuit != nil {
		t.Fatal("expected no short circuit")
	}
	if transformedReq == nil || transformedReq.ChatRequest == nil {
		t.Fatal("expected chat stream request after transform")
	}
	if transformedReq.RequestType != schemas.ChatCompletionStreamRequest {
		t.Fatalf("expected request type %s, got %s", schemas.ChatCompletionStreamRequest, transformedReq.RequestType)
	}

	state, ok := schemas.GetResponsesToChatCompletionCompatState(ctx)
	if !ok || state == nil {
		t.Fatal("expected compat state to be stored for forced streaming fallback")
	}
	if !state.Active || !state.IsStreaming || state.FallbackRequest == nil {
		t.Fatalf("expected active streaming fallback state, got %+v", state)
	}
}
