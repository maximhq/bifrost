package gigachat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestGigaChatResponsesRequestConversion(t *testing.T) {
	testGigaChatResponsesRequestConversion(t)
}

func TestGigaChatResponses(t *testing.T) {
	testGigaChatResponses(t)
}

func TestGigaChatResponsesStream(t *testing.T) {
	testGigaChatResponsesStream(t)
}

func TestGigaChatResponsesAttachmentHelpers(t *testing.T) {
	t.Run("RemoteImageURL", testGigaChatResponsesRemoteImageURLUploadHelper)
	t.Run("RemoteFileURL", testGigaChatResponsesRemoteFileURLUploadHelper)
}

func testGigaChatResponsesRequestConversion(t *testing.T) {
	t.Parallel()

	t.Run("SimpleTextInput", testGigaChatResponsesSimpleTextInput)
	t.Run("InstructionsAndMultiTurnInput", testGigaChatResponsesInstructionsAndMultiTurnInput)
	t.Run("FileInputReference", testGigaChatResponsesFileInputReference)
	t.Run("ImageInputReference", testGigaChatResponsesImageInputReference)
	t.Run("FunctionToolAndToolHistory", testGigaChatResponsesFunctionToolAndToolHistory)
	t.Run("StructuredOutput", testGigaChatResponsesStructuredOutput)
	t.Run("RejectsUnsupportedHostedTools", testGigaChatResponsesRejectsUnsupportedHostedTools)
	t.Run("RejectsUnsupportedParams", testGigaChatResponsesRejectsUnsupportedParams)
	t.Run("RejectsUnsupportedFileInputs", testGigaChatResponsesRejectsUnsupportedFileInputs)
	t.Run("FunctionCallOutputUsesCallIDAsToolsStateID", testGigaChatResponsesFunctionCallOutputUsesCallIDAsToolsStateID)
	t.Run("FunctionCallOutputInfersNameFromGeneratedCallID", testGigaChatResponsesFunctionCallOutputInfersNameFromGeneratedCallID)
	t.Run("ThreadStorage", testGigaChatResponsesThreadStorage)
}

func testGigaChatResponses(t *testing.T) {
	t.Parallel()

	t.Run("ConverterMapsTextAndUsage", testGigaChatResponsesConverterMapsTextAndUsage)
	t.Run("ConverterMapsImageFileOutput", testGigaChatResponsesConverterMapsImageFileOutput)
	t.Run("ConverterMapsWebSearchSources", testGigaChatResponsesConverterMapsWebSearchSources)
	t.Run("ConverterMapsReasoningRole", testGigaChatResponsesConverterMapsReasoningRole)
	t.Run("ConverterMapsToolCall", testGigaChatResponsesConverterMapsToolCall)
	t.Run("ConverterMapsFunctionResultWithOriginatingCallID", testGigaChatResponsesConverterMapsFunctionResultWithOriginatingCallID)
	t.Run("ConverterUsesUniqueCallIDsUnderSharedToolsStateID", testGigaChatResponsesConverterUsesUniqueCallIDsUnderSharedToolsStateID)
	t.Run("ConverterUsesToolStateIDAliasAsCallID", testGigaChatResponsesConverterUsesToolStateIDAliasAsCallID)
	t.Run("ConverterFallsBackToResponseToolsStateID", testGigaChatResponsesConverterFallsBackToResponseToolsStateID)
	t.Run("ConverterPreservesOrdinaryMessageToolStateID", testGigaChatResponsesConverterPreservesOrdinaryMessageToolStateID)
	t.Run("ConverterMapsThreadStorage", testGigaChatResponsesConverterMapsThreadStorage)
	t.Run("ExecutesWithOAuthToken", testGigaChatResponsesExecutesWithOAuthToken)
	t.Run("UploadsInputImageAttachment", testGigaChatResponsesUploadsInputImageAttachment)
	t.Run("UploadsInlineFileAttachment", testGigaChatResponsesUploadsInlineFileAttachment)
	t.Run("ReusesUploadedAttachmentAfterBackendError", testGigaChatResponsesReusesUploadedAttachmentAfterBackendError)
	t.Run("ReusesCompletedUploadsAfterPartialAttachmentFailure", testGigaChatResponsesReusesCompletedUploadsAfterPartialAttachmentFailure)
	t.Run("MapsProviderErrors", testGigaChatResponsesMapsProviderErrors)
	t.Run("RefreshesTokenAfterUnauthorized", testGigaChatResponsesRefreshesTokenAfterUnauthorized)
}

func testGigaChatResponsesStream(t *testing.T) {
	t.Parallel()

	t.Run("TextDeltasAndUsage", testGigaChatResponsesStreamTextDeltasAndUsage)
	t.Run("ReasoningDeltas", testGigaChatResponsesStreamReasoningDeltas)
	t.Run("ToolCallDeltas", testGigaChatResponsesStreamToolCallDeltas)
	t.Run("ClosesOnMessageDoneEvent", testGigaChatResponsesStreamClosesOnMessageDoneEvent)
	t.Run("MapsErrorEvents", testGigaChatResponsesStreamMapsErrorEvents)
	t.Run("HandlesContextCancellation", testGigaChatResponsesStreamHandlesContextCancellation)
	t.Run("PassthroughResponseOwnedByLargeReader", testGigaChatResponsesStreamPassthroughResponseOwnedByLargeReader)
}

func testGigaChatResponsesSimpleTextInput(t *testing.T) {
	t.Parallel()

	temperature := 0.2
	topP := 0.8
	maxOutputTokens := 256
	topLogProbs := 3
	request := &schemas.BifrostResponsesRequest{
		Model: "GigaChat-2",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")},
		}},
		Params: &schemas.ResponsesParameters{
			Temperature:     &temperature,
			TopP:            &topP,
			MaxOutputTokens: &maxOutputTokens,
			TopLogProbs:     &topLogProbs,
		},
	}

	gigaChatReq, err := ToGigaChatResponsesRequest(request)
	if err != nil {
		t.Fatalf("ToGigaChatResponsesRequest returned error: %v", err)
	}
	if gigaChatReq.Model != "GigaChat-2" {
		t.Fatalf("model mismatch: got %q", gigaChatReq.Model)
	}
	if len(gigaChatReq.Messages) != 1 {
		t.Fatalf("message count mismatch: got %d", len(gigaChatReq.Messages))
	}
	if got := gigaChatReq.Messages[0].Role; got != "user" {
		t.Fatalf("role mismatch: got %q", got)
	}
	if got := *gigaChatReq.Messages[0].Content[0].Text; got != "hello" {
		t.Fatalf("content mismatch: got %q", got)
	}
	if gigaChatReq.ModelOptions == nil ||
		gigaChatReq.ModelOptions.Temperature == nil || *gigaChatReq.ModelOptions.Temperature != temperature ||
		gigaChatReq.ModelOptions.TopP == nil || *gigaChatReq.ModelOptions.TopP != topP ||
		gigaChatReq.ModelOptions.MaxTokens == nil || *gigaChatReq.ModelOptions.MaxTokens != maxOutputTokens ||
		gigaChatReq.ModelOptions.TopLogProbs == nil || *gigaChatReq.ModelOptions.TopLogProbs != topLogProbs {
		t.Fatalf("model options mismatch: %#v", gigaChatReq.ModelOptions)
	}

	body, err := json.Marshal(gigaChatReq)
	if err != nil {
		t.Fatalf("failed to marshal GigaChat request: %v", err)
	}
	if strings.Contains(string(body), `"stream"`) {
		t.Fatalf("non-streaming v2 request should omit stream, got %s", body)
	}
}

func testGigaChatResponsesInstructionsAndMultiTurnInput(t *testing.T) {
	t.Parallel()

	instructions := "Answer in Russian."
	request := &schemas.BifrostResponsesRequest{
		Model: "GigaChat-2-Pro",
		Input: []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Be concise.")},
			},
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{
					Type: schemas.ResponsesOutputMessageContentTypeText,
					Text: schemas.Ptr("Previous answer."),
				}}},
			},
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{
					Type: schemas.ResponsesInputMessageContentBlockTypeText,
					Text: schemas.Ptr("Continue."),
				}}},
			},
		},
		Params: &schemas.ResponsesParameters{Instructions: &instructions},
	}

	gigaChatReq, err := ToGigaChatResponsesRequest(request)
	if err != nil {
		t.Fatalf("ToGigaChatResponsesRequest returned error: %v", err)
	}
	if len(gigaChatReq.Messages) != 4 {
		t.Fatalf("message count mismatch: got %d", len(gigaChatReq.Messages))
	}
	wantRoles := []string{"system", "system", "assistant", "user"}
	wantText := []string{"Answer in Russian.", "Be concise.", "Previous answer.", "Continue."}
	for index := range wantRoles {
		if got := gigaChatReq.Messages[index].Role; got != wantRoles[index] {
			t.Fatalf("message %d role mismatch: got %q, want %q", index, got, wantRoles[index])
		}
		if got := *gigaChatReq.Messages[index].Content[0].Text; got != wantText[index] {
			t.Fatalf("message %d text mismatch: got %q, want %q", index, got, wantText[index])
		}
	}
}

func testGigaChatResponsesFileInputReference(t *testing.T) {
	t.Parallel()

	fileID := " file-document "
	mime := " application/pdf "
	filename := "document.pdf"
	request := &schemas.BifrostResponsesRequest{
		Model: "GigaChat-2-Pro",
		Input: []schemas.ResponsesMessage{{
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
				{
					Type: schemas.ResponsesInputMessageContentBlockTypeText,
					Text: schemas.Ptr("Summarize this document."),
				},
				{
					Type:   schemas.ResponsesInputMessageContentBlockTypeFile,
					FileID: &fileID,
					ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
						Filename: &filename,
						FileType: &mime,
					},
				},
			}},
		}},
	}

	gigaChatReq, err := ToGigaChatResponsesRequest(request)
	if err != nil {
		t.Fatalf("ToGigaChatResponsesRequest returned error: %v", err)
	}
	if len(gigaChatReq.Messages) != 1 || len(gigaChatReq.Messages[0].Content) != 2 {
		t.Fatalf("content parts mismatch: %#v", gigaChatReq.Messages)
	}
	files := gigaChatReq.Messages[0].Content[1].Files
	if len(files) != 1 {
		t.Fatalf("file refs mismatch: %#v", files)
	}
	if files[0].ID != "file-document" {
		t.Fatalf("file id mismatch: got %q", files[0].ID)
	}
	if files[0].MIME == nil || *files[0].MIME != "application/pdf" {
		t.Fatalf("file mime mismatch: %#v", files[0].MIME)
	}
	if files[0].Target != nil {
		t.Fatalf("target should be omitted without a Bifrost source field, got %#v", files[0].Target)
	}

	body, err := json.Marshal(gigaChatReq)
	if err != nil {
		t.Fatalf("failed to marshal GigaChat request: %v", err)
	}
	if !strings.Contains(string(body), `"files":[{"id":"file-document","mime":"application/pdf"}]`) {
		t.Fatalf("request body should include GigaChat file reference, got %s", body)
	}
	if strings.Contains(string(body), filename) {
		t.Fatalf("filename has no GigaChat v2 file content target mapping and should be omitted, got %s", body)
	}
}

func testGigaChatResponsesImageInputReference(t *testing.T) {
	t.Parallel()

	fileID := " image-file "
	request := &schemas.BifrostResponsesRequest{
		Model: "GigaChat-2-Pro",
		Input: []schemas.ResponsesMessage{{
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
				{
					Type: schemas.ResponsesInputMessageContentBlockTypeText,
					Text: schemas.Ptr("Describe this image."),
				},
				{
					Type:   schemas.ResponsesInputMessageContentBlockTypeImage,
					FileID: &fileID,
				},
			}},
		}},
	}

	gigaChatReq, err := ToGigaChatResponsesRequest(request)
	if err != nil {
		t.Fatalf("ToGigaChatResponsesRequest returned error: %v", err)
	}
	if len(gigaChatReq.Messages) != 1 || len(gigaChatReq.Messages[0].Content) != 2 {
		t.Fatalf("content parts mismatch: %#v", gigaChatReq.Messages)
	}
	files := gigaChatReq.Messages[0].Content[1].Files
	if len(files) != 1 {
		t.Fatalf("image file refs mismatch: %#v", files)
	}
	if files[0].ID != "image-file" {
		t.Fatalf("image file id mismatch: got %q", files[0].ID)
	}
	if files[0].MIME != nil || files[0].Target != nil {
		t.Fatalf("input_image file_id should omit mime and target, got %#v", files[0])
	}
}

func testGigaChatResponsesFunctionToolAndToolHistory(t *testing.T) {
	t.Parallel()

	toolName := "get_weather"
	callID := "tools-state-weather"
	arguments := `{"city":"Moscow"}`
	toolOutput := `{"temperature":5}`
	request := &schemas.BifrostResponsesRequest{
		Model: "GigaChat-2-Max",
		Input: []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Weather?")},
			},
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Name:      &toolName,
					CallID:    &callID,
					Arguments: &arguments,
				},
			},
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Name:   &toolName,
					CallID: &callID,
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesToolCallOutputStr: &toolOutput,
					},
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{{
				Type:        schemas.ResponsesToolTypeFunction,
				Name:        &toolName,
				Description: schemas.Ptr("Gets current weather."),
				ResponsesToolFunction: &schemas.ResponsesToolFunction{
					Parameters: mustGigaChatToolParameters(t, `{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
				},
			}},
			ToolChoice: &schemas.ResponsesToolChoice{
				ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
					Type: schemas.ResponsesToolChoiceTypeFunction,
					Name: &toolName,
				},
			},
		},
	}

	gigaChatReq, err := ToGigaChatResponsesRequest(request)
	if err != nil {
		t.Fatalf("ToGigaChatResponsesRequest returned error: %v", err)
	}
	if len(gigaChatReq.Tools) != 1 || gigaChatReq.Tools[0].Functions == nil || len(gigaChatReq.Tools[0].Functions.Specifications) != 1 {
		t.Fatalf("function tools mismatch: %#v", gigaChatReq.Tools)
	}
	specification := gigaChatReq.Tools[0].Functions.Specifications[0]
	if specification.Name != toolName || specification.Description == nil || *specification.Description != "Gets current weather." {
		t.Fatalf("function specification mismatch: %#v", specification)
	}
	if gigaChatReq.ToolConfig == nil || gigaChatReq.ToolConfig.Mode != "forced" || gigaChatReq.ToolConfig.FunctionName == nil || *gigaChatReq.ToolConfig.FunctionName != toolName {
		t.Fatalf("tool config mismatch: %#v", gigaChatReq.ToolConfig)
	}
	if gigaChatReq.Messages[1].FunctionCall == nil {
		t.Fatalf("expected function call message, got %#v", gigaChatReq.Messages[1])
	}
	if gigaChatReq.Messages[1].ToolsStateID == nil || *gigaChatReq.Messages[1].ToolsStateID != callID {
		t.Fatalf("function call tools_state_id mismatch: %#v", gigaChatReq.Messages[1].ToolsStateID)
	}
	argumentsMap, ok := gigaChatReq.Messages[1].FunctionCall.Arguments.(map[string]interface{})
	if !ok || argumentsMap["city"] != "Moscow" {
		t.Fatalf("function arguments mismatch: %#v", gigaChatReq.Messages[1].FunctionCall.Arguments)
	}
	if gigaChatReq.Messages[2].ToolsStateID == nil || *gigaChatReq.Messages[2].ToolsStateID != callID {
		t.Fatalf("function result tools_state_id mismatch: %#v", gigaChatReq.Messages[2].ToolsStateID)
	}
	if gigaChatReq.Messages[2].Content[0].FunctionResult == nil || gigaChatReq.Messages[2].Content[0].FunctionResult.Result != toolOutput {
		t.Fatalf("function result mismatch: %#v", gigaChatReq.Messages[2].Content)
	}
}

func testGigaChatResponsesFunctionCallOutputUsesCallIDAsToolsStateID(t *testing.T) {
	t.Parallel()

	toolName := "get_weather"
	callID := "019e8282-bb13-73fc-bbe8-5f52856d166b__bifrost_fc_1"
	toolOutput := `{"temperature":5}`
	request := &schemas.BifrostResponsesRequest{
		Model: "GigaChat-2-Max",
		Input: []schemas.ResponsesMessage{{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Name:   &toolName,
				CallID: &callID,
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesToolCallOutputStr: &toolOutput,
				},
			},
		}},
	}

	gigaChatReq, err := ToGigaChatResponsesRequest(request)
	if err != nil {
		t.Fatalf("ToGigaChatResponsesRequest returned error: %v", err)
	}
	if len(gigaChatReq.Messages) != 1 {
		t.Fatalf("message count mismatch: got %d", len(gigaChatReq.Messages))
	}
	message := gigaChatReq.Messages[0]
	if message.ToolsStateID == nil || *message.ToolsStateID != callID {
		t.Fatalf("function_call_output tools_state_id mismatch: %#v", message.ToolsStateID)
	}
	if message.Content[0].FunctionResult == nil || message.Content[0].FunctionResult.Result != toolOutput {
		t.Fatalf("function result mismatch: %#v", message.Content)
	}
}

func TestGigaChatResponsesFunctionCallOutputInfersNameFromPriorCallID(t *testing.T) {
	t.Parallel()

	toolName := "get_weather"
	callID := "019e8282-bb13-73fc-bbe8-5f52856d166b"
	arguments := `{"city":"Moscow"}`
	toolOutput := `{"temperature":5}`
	request := &schemas.BifrostResponsesRequest{
		Model: "GigaChat-2-Max",
		Input: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Name:      &toolName,
					CallID:    &callID,
					Arguments: &arguments,
				},
			},
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: &callID,
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesToolCallOutputStr: &toolOutput,
					},
				},
			},
		},
	}

	gigaChatReq, err := ToGigaChatResponsesRequest(request)
	if err != nil {
		t.Fatalf("ToGigaChatResponsesRequest returned error: %v", err)
	}
	if len(gigaChatReq.Messages) != 2 {
		t.Fatalf("message count mismatch: got %d", len(gigaChatReq.Messages))
	}
	functionResult := gigaChatReq.Messages[1].Content[0].FunctionResult
	if functionResult == nil {
		t.Fatalf("function result missing: %#v", gigaChatReq.Messages[1].Content)
	}
	if functionResult.Name != toolName || functionResult.Result != toolOutput {
		t.Fatalf("function result mismatch: %#v", functionResult)
	}
}

func testGigaChatResponsesFunctionCallOutputInfersNameFromGeneratedCallID(t *testing.T) {
	t.Parallel()

	response := &GigaChatResponsesResponse{
		Model: "GigaChat-2-Max",
		Messages: []GigaChatResponsesMessage{{
			Role:         "assistant",
			MessageID:    schemas.Ptr("call-message"),
			ToolsStateID: schemas.Ptr("tools-state-call"),
			Content: []GigaChatResponsesContentPart{{
				FunctionCall: &GigaChatResponsesFunctionCall{
					Name:      "get_weather",
					Arguments: map[string]interface{}{"city": "Moscow"},
				},
			}},
		}},
	}
	converted := ToBifrostResponsesResponse(schemas.GigaChat, response)
	if converted == nil || len(converted.Output) != 1 || converted.Output[0].ResponsesToolMessage == nil {
		t.Fatalf("converted output mismatch: %#v", converted)
	}

	threadID := "thread-123"
	toolOutput := `{"temperature":5}`
	request := &schemas.BifrostResponsesRequest{
		Model: "GigaChat-2-Max",
		Input: []schemas.ResponsesMessage{{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: converted.Output[0].ResponsesToolMessage.CallID,
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesToolCallOutputStr: &toolOutput,
				},
			},
		}},
		Params: &schemas.ResponsesParameters{
			PreviousResponseID: &threadID,
		},
	}

	gigaChatReq, err := ToGigaChatResponsesRequest(request)
	if err != nil {
		t.Fatalf("ToGigaChatResponsesRequest returned error: %v", err)
	}
	if len(gigaChatReq.Messages) != 1 {
		t.Fatalf("message count mismatch: got %d", len(gigaChatReq.Messages))
	}
	if gigaChatReq.Model != "" {
		t.Fatalf("previous_response_id request should omit provider model, got %q", gigaChatReq.Model)
	}
	message := gigaChatReq.Messages[0]
	assertGigaChatResponsesToolStateID(t, message, "tools-state-call")
	functionResult := message.Content[0].FunctionResult
	if functionResult == nil || functionResult.Name != "get_weather" || functionResult.Result != toolOutput {
		t.Fatalf("function result mismatch: %#v", message.Content)
	}
}

func testGigaChatResponsesThreadStorage(t *testing.T) {
	t.Parallel()

	request := testGigaChatResponsesRequest()
	gigaChatReq, err := ToGigaChatResponsesRequest(request)
	if err != nil {
		t.Fatalf("ToGigaChatResponsesRequest returned error: %v", err)
	}
	storage, ok := gigaChatReq.Storage.(*GigaChatResponsesStorage)
	if !ok {
		t.Fatalf("storage should default to an object, got %#v", gigaChatReq.Storage)
	}
	if storage.ThreadID != nil || len(storage.Metadata) != 0 {
		t.Fatalf("default storage should be empty, got %#v", storage)
	}
	body, err := json.Marshal(gigaChatReq)
	if err != nil {
		t.Fatalf("failed to marshal GigaChat request: %v", err)
	}
	if !strings.Contains(string(body), `"storage":{}`) {
		t.Fatalf("default storage object missing from request: %s", body)
	}
	if !strings.Contains(string(body), `"model":"GigaChat-2"`) {
		t.Fatalf("initial thread request should include model, got %s", body)
	}

	threadID := "thread-123"
	metadata := map[string]any{"tenant": "test"}
	request.Params = &schemas.ResponsesParameters{
		PreviousResponseID: &threadID,
		Metadata:           &metadata,
	}
	gigaChatReq, err = ToGigaChatResponsesRequest(request)
	if err != nil {
		t.Fatalf("ToGigaChatResponsesRequest with previous_response_id returned error: %v", err)
	}
	storage, ok = gigaChatReq.Storage.(*GigaChatResponsesStorage)
	if !ok || storage.ThreadID == nil || *storage.ThreadID != threadID {
		t.Fatalf("thread storage mismatch: %#v", gigaChatReq.Storage)
	}
	if storage.Metadata["tenant"] != "test" {
		t.Fatalf("storage metadata mismatch: %#v", storage.Metadata)
	}
	if gigaChatReq.Model != "" {
		t.Fatalf("previous_response_id request should omit provider model, got %q", gigaChatReq.Model)
	}
	body, err = json.Marshal(gigaChatReq)
	if err != nil {
		t.Fatalf("failed to marshal GigaChat request with previous_response_id: %v", err)
	}
	if strings.Contains(string(body), `"model"`) {
		t.Fatalf("previous_response_id request should omit model from provider body, got %s", body)
	}
	if !strings.Contains(string(body), `"thread_id":"thread-123"`) {
		t.Fatalf("previous_response_id request should include thread_id, got %s", body)
	}

	request.Params = &schemas.ResponsesParameters{
		Conversation: &threadID,
	}
	gigaChatReq, err = ToGigaChatResponsesRequest(request)
	if err != nil {
		t.Fatalf("ToGigaChatResponsesRequest with conversation returned error: %v", err)
	}
	if gigaChatReq.Model != "" {
		t.Fatalf("conversation request should omit provider model, got %q", gigaChatReq.Model)
	}

	store := false
	request.Params = &schemas.ResponsesParameters{Store: &store}
	gigaChatReq, err = ToGigaChatResponsesRequest(request)
	if err != nil {
		t.Fatalf("ToGigaChatResponsesRequest with store=false returned error: %v", err)
	}
	if disabled, ok := gigaChatReq.Storage.(bool); !ok || disabled {
		t.Fatalf("store=false should map to storage=false, got %#v", gigaChatReq.Storage)
	}

	otherThreadID := "thread-456"
	request.Params = &schemas.ResponsesParameters{
		Conversation:       &threadID,
		PreviousResponseID: &otherThreadID,
	}
	_, err = ToGigaChatResponsesRequest(request)
	if err == nil || !strings.Contains(err.Error(), "same thread_id") {
		t.Fatalf("expected thread id conflict error, got %v", err)
	}
}

func testGigaChatResponsesRemoteImageURLUploadHelper(t *testing.T) {
	t.Parallel()

	imageURL := "https://cdn.example.com/assets/cat.png"
	fetch := func(_ context.Context, resourceURL string) (string, string, error) {
		if resourceURL != imageURL {
			t.Fatalf("resource URL mismatch: got %q, want %q", resourceURL, imageURL)
		}
		return "image/png", base64.StdEncoding.EncodeToString([]byte("remote-image")), nil
	}

	upload, err := gigaChatResponsesImageURLUpload(context.Background(), 0, schemas.ResponsesMessageContentBlock{
		Type: schemas.ResponsesInputMessageContentBlockTypeImage,
		ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
			ImageURL: &imageURL,
		},
	}, fetch)
	if err != nil {
		t.Fatalf("gigaChatResponsesImageURLUpload returned error: %v", err)
	}
	if string(upload.file) != "remote-image" {
		t.Fatalf("upload bytes mismatch: %q", upload.file)
	}
	if upload.filename != "cat.png" {
		t.Fatalf("filename mismatch: got %q", upload.filename)
	}
	if upload.contentType != "image/png" {
		t.Fatalf("content type mismatch: got %q", upload.contentType)
	}
}

func testGigaChatResponsesRemoteFileURLUploadHelper(t *testing.T) {
	t.Parallel()

	fileURL := "https://cdn.example.com/docs/report"
	filename := "report.pdf"
	fetch := func(_ context.Context, resourceURL string) (string, string, error) {
		if resourceURL != fileURL {
			t.Fatalf("resource URL mismatch: got %q, want %q", resourceURL, fileURL)
		}
		return "application/pdf; charset=binary", base64.StdEncoding.EncodeToString([]byte("%PDF remote")), nil
	}

	upload, err := gigaChatResponsesFileURLUpload(context.Background(), 1, &schemas.ResponsesInputMessageContentBlockFile{
		FileURL:  &fileURL,
		Filename: &filename,
	}, fetch)
	if err != nil {
		t.Fatalf("gigaChatResponsesFileURLUpload returned error: %v", err)
	}
	if string(upload.file) != "%PDF remote" {
		t.Fatalf("upload bytes mismatch: %q", upload.file)
	}
	if upload.filename != "report.pdf" {
		t.Fatalf("filename mismatch: got %q", upload.filename)
	}
	if upload.contentType != "application/pdf" {
		t.Fatalf("content type mismatch: got %q", upload.contentType)
	}
}

func testGigaChatResponsesRejectsUnsupportedFileInputs(t *testing.T) {
	t.Parallel()

	fileID := "file-document"
	fileData := "SGVsbG8="
	fileURL := "https://example.test/document.pdf"
	cases := []struct {
		name       string
		block      schemas.ResponsesMessageContentBlock
		wantErrSub string
	}{
		{
			name: "MissingFileID",
			block: schemas.ResponsesMessageContentBlock{
				Type:   schemas.ResponsesInputMessageContentBlockTypeFile,
				FileID: schemas.Ptr("  "),
				ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
					FileType: schemas.Ptr("text/plain"),
				},
			},
			wantErrSub: "requires file_id",
		},
		{
			name: "InlineFileData",
			block: schemas.ResponsesMessageContentBlock{
				Type:   schemas.ResponsesInputMessageContentBlockTypeFile,
				FileID: &fileID,
				ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
					FileData: &fileData,
				},
			},
			wantErrSub: "pre-uploaded file_id references only",
		},
		{
			name: "InlineFileURL",
			block: schemas.ResponsesMessageContentBlock{
				Type: schemas.ResponsesInputMessageContentBlockTypeFile,
				ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
					FileURL: &fileURL,
				},
			},
			wantErrSub: "pre-uploaded file_id references only",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := &schemas.BifrostResponsesRequest{
				Model: "GigaChat-2",
				Input: []schemas.ResponsesMessage{{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
						tc.block,
					}},
				}},
			}

			_, err := ToGigaChatResponsesRequest(request)
			if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Fatalf("expected %q error, got %v", tc.wantErrSub, err)
			}
		})
	}
}

func testGigaChatResponsesStructuredOutput(t *testing.T) {
	t.Parallel()

	strict := true
	formatName := "WeatherAnswer"
	formatDescription := "Weather response."
	sourceSchema := schemas.NewOrderedMapFromPairs(
		schemas.KV("type", "object"),
		schemas.KV("properties", schemas.NewOrderedMapFromPairs(
			schemas.KV("answer", schemas.NewOrderedMapFromPairs(schemas.KV("type", "string"))),
		)),
		schemas.KV("required", []string{"answer"}),
	)
	request := &schemas.BifrostResponsesRequest{
		Model: "GigaChat-2-Pro",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Return JSON.")},
		}},
		Params: &schemas.ResponsesParameters{
			Text: &schemas.ResponsesTextConfig{
				Format: &schemas.ResponsesTextConfigFormat{
					Type:        "json_schema",
					Name:        &formatName,
					Description: &formatDescription,
					Strict:      &strict,
					JSONSchema: &schemas.ResponsesTextConfigFormatJSONSchema{
						Schema: &schemas.JSONSchemaOrBool{SchemaMap: sourceSchema},
					},
				},
			},
		},
	}

	gigaChatReq, err := ToGigaChatResponsesRequest(request)
	if err != nil {
		t.Fatalf("ToGigaChatResponsesRequest returned error: %v", err)
	}
	responseFormat := gigaChatReq.ModelOptions.ResponseFormat
	if responseFormat == nil || responseFormat.Type != "json_schema" || responseFormat.Strict == nil || !*responseFormat.Strict {
		t.Fatalf("response format mismatch: %#v", responseFormat)
	}
	schemaMap, ok := responseFormat.Schema.(*schemas.OrderedMap)
	if !ok {
		t.Fatalf("response schema has unexpected type: %#v", responseFormat.Schema)
	}
	title, _ := schemaMap.Get("title")
	description, _ := schemaMap.Get("description")
	if title != formatName || description != formatDescription {
		t.Fatalf("schema metadata mismatch: %#v", schemaMap)
	}
	schemaType, _ := schemaMap.Get("type")
	if schemaType != "object" {
		t.Fatalf("schema type mismatch: %#v", schemaMap)
	}
	if _, exists := sourceSchema.Get("title"); exists {
		t.Fatalf("source schema was mutated with title metadata: %#v", sourceSchema)
	}
	if _, exists := sourceSchema.Get("description"); exists {
		t.Fatalf("source schema was mutated with description metadata: %#v", sourceSchema)
	}
}

func testGigaChatResponsesRejectsUnsupportedHostedTools(t *testing.T) {
	t.Parallel()

	request := testGigaChatResponsesRequest()
	request.Params = &schemas.ResponsesParameters{
		Tools: []schemas.ResponsesTool{{
			Type: schemas.ResponsesToolTypeFileSearch,
			ResponsesToolFileSearch: &schemas.ResponsesToolFileSearch{
				VectorStoreIDs: []string{"vs_123"},
			},
		}},
	}

	_, err := ToGigaChatResponsesRequest(request)
	if err == nil {
		t.Fatal("expected unsupported hosted tool error, got nil")
	}
	if !strings.Contains(err.Error(), "does not support tool type") || !strings.Contains(err.Error(), "file_search") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func testGigaChatResponsesRejectsUnsupportedParams(t *testing.T) {
	t.Parallel()

	parallelToolCalls := true
	request := testGigaChatResponsesRequest()
	request.Params = &schemas.ResponsesParameters{ParallelToolCalls: &parallelToolCalls}
	_, err := ToGigaChatResponsesRequest(request)
	if err == nil || !strings.Contains(err.Error(), "parallel_tool_calls") {
		t.Fatalf("expected parallel_tool_calls error, got %v", err)
	}

	request = testGigaChatResponsesRequest()
	request.Params = &schemas.ResponsesParameters{
		Text: &schemas.ResponsesTextConfig{
			Format: &schemas.ResponsesTextConfigFormat{Type: "json_object"},
		},
	}
	_, err = ToGigaChatResponsesRequest(request)
	if err == nil || !strings.Contains(err.Error(), "json_object") {
		t.Fatalf("expected json_object error, got %v", err)
	}

	request = &schemas.BifrostResponsesRequest{
		Model: "GigaChat-2",
		Input: []schemas.ResponsesMessage{{
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{
				Type: schemas.ResponsesInputMessageContentBlockTypeImage,
				ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
					ImageURL: schemas.Ptr("https://example.test/image.png"),
				},
			}}},
		}},
	}
	_, err = ToGigaChatResponsesRequest(request)
	if err == nil || !strings.Contains(err.Error(), "input_image") {
		t.Fatalf("expected input_image error, got %v", err)
	}
}

func testGigaChatResponsesConverterMapsTextAndUsage(t *testing.T) {
	t.Parallel()

	response := &GigaChatResponsesResponse{
		MessageID: schemas.Ptr("resp-test"),
		CreatedAt: 1700000000,
		Model:     "GigaChat-2",
		Messages: []GigaChatResponsesMessage{{
			Role:      "assistant",
			MessageID: schemas.Ptr("msg-test"),
			Content: []GigaChatResponsesContentPart{{
				Text: schemas.Ptr("Здравствуйте"),
			}},
			FinishReason: schemas.Ptr("stop"),
		}},
		Usage: &GigaChatChatUsage{
			InputTokens:  7,
			OutputTokens: 3,
			TotalTokens:  10,
			InputTokensDetails: &GigaChatTokenDetails{
				CachedTokens: 2,
			},
		},
	}

	converted := ToBifrostResponsesResponse(schemas.GigaChat, response)
	if converted == nil {
		t.Fatal("expected response, got nil")
	}
	if converted.ID == nil || *converted.ID != "resp-test" {
		t.Fatalf("id mismatch: %#v", converted.ID)
	}
	if converted.Object != "response" || converted.CreatedAt != 1700000000 || converted.Model != "GigaChat-2" {
		t.Fatalf("metadata mismatch: %#v", converted)
	}
	if converted.Status == nil || *converted.Status != "completed" {
		t.Fatalf("status mismatch: %#v", converted.Status)
	}
	if converted.StopReason == nil || *converted.StopReason != "stop" {
		t.Fatalf("stop reason mismatch: %#v", converted.StopReason)
	}
	if converted.Usage == nil || converted.Usage.InputTokens != 7 || converted.Usage.OutputTokens != 3 || converted.Usage.TotalTokens != 10 {
		t.Fatalf("usage mismatch: %#v", converted.Usage)
	}
	if converted.Usage.InputTokensDetails == nil || converted.Usage.InputTokensDetails.CachedReadTokens != 2 {
		t.Fatalf("cached tokens mismatch: %#v", converted.Usage.InputTokensDetails)
	}
	if len(converted.Output) != 1 {
		t.Fatalf("output count mismatch: got %d", len(converted.Output))
	}
	output := converted.Output[0]
	if output.Type == nil || *output.Type != schemas.ResponsesMessageTypeMessage {
		t.Fatalf("output type mismatch: %#v", output.Type)
	}
	if output.Role == nil || *output.Role != schemas.ResponsesInputMessageRoleAssistant {
		t.Fatalf("output role mismatch: %#v", output.Role)
	}
	if output.Content == nil || len(output.Content.ContentBlocks) != 1 {
		t.Fatalf("content mismatch: %#v", output.Content)
	}
	block := output.Content.ContentBlocks[0]
	if block.Type != schemas.ResponsesOutputMessageContentTypeText || block.Text == nil || *block.Text != "Здравствуйте" {
		t.Fatalf("text block mismatch: %#v", block)
	}
	if converted.ExtraFields.Provider != schemas.GigaChat {
		t.Fatalf("provider mismatch: got %q, want %q", converted.ExtraFields.Provider, schemas.GigaChat)
	}
}

func testGigaChatResponsesConverterMapsImageFileOutput(t *testing.T) {
	t.Parallel()

	fileID := "629ea825-963c-4178-bea4-1415c1d15a6e"
	response := &GigaChatResponsesResponse{
		Model: "GigaChat-3-Ultra",
		Messages: []GigaChatResponsesMessage{{
			Role:      "assistant",
			MessageID: schemas.Ptr("msg-image"),
			Content: []GigaChatResponsesContentPart{
				{
					Files: []GigaChatResponsesContentFile{{
						ID:     fileID,
						MIME:   schemas.Ptr("image/jpeg"),
						Target: schemas.Ptr("image"),
					}},
				},
				{
					Text: schemas.Ptr("вот красивая картинка с коровой в космосе."),
				},
			},
			FinishReason: schemas.Ptr("stop"),
		}},
	}

	converted := ToBifrostResponsesResponse(schemas.GigaChat, response)
	if converted == nil {
		t.Fatal("expected response, got nil")
	}
	if len(converted.Output) != 2 {
		t.Fatalf("output count mismatch: got %d", len(converted.Output))
	}

	message := converted.Output[0]
	if message.Type == nil || *message.Type != schemas.ResponsesMessageTypeMessage {
		t.Fatalf("assistant output type mismatch: %#v", message.Type)
	}
	if message.Content == nil || len(message.Content.ContentBlocks) != 1 {
		t.Fatalf("assistant content mismatch: %#v", message.Content)
	}
	textBlock := message.Content.ContentBlocks[0]
	if textBlock.Text == nil || *textBlock.Text != "вот красивая картинка с коровой в космосе." {
		t.Fatalf("assistant text mismatch: %#v", textBlock)
	}

	imageCall := converted.Output[1]
	if imageCall.ID == nil || *imageCall.ID != "ig_msg-image_0" {
		t.Fatalf("image output id mismatch: %#v", imageCall.ID)
	}
	if imageCall.Type == nil || *imageCall.Type != schemas.ResponsesMessageTypeImageGenerationCall {
		t.Fatalf("image output type mismatch: %#v", imageCall.Type)
	}
	if imageCall.Status == nil || *imageCall.Status != "completed" {
		t.Fatalf("image output status mismatch: %#v", imageCall.Status)
	}
	if imageCall.ResponsesToolMessage == nil || imageCall.ResponsesToolMessage.ResponsesImageGenerationCall == nil {
		t.Fatalf("image generation call missing: %#v", imageCall.ResponsesToolMessage)
	}
	if imageCall.ResponsesToolMessage.ResponsesImageGenerationCall.Result != fileID {
		t.Fatalf("image generation result mismatch: %#v", imageCall.ResponsesToolMessage.ResponsesImageGenerationCall)
	}

	raw, err := json.Marshal(imageCall)
	if err != nil {
		t.Fatalf("failed to marshal image output: %v", err)
	}
	var marshaled map[string]interface{}
	if err := json.Unmarshal(raw, &marshaled); err != nil {
		t.Fatalf("failed to unmarshal image output JSON: %v", err)
	}
	if marshaled["type"] != string(schemas.ResponsesMessageTypeImageGenerationCall) || marshaled["result"] != fileID {
		t.Fatalf("image output JSON mismatch: %s", string(raw))
	}
}

func testGigaChatResponsesConverterMapsWebSearchSources(t *testing.T) {
	t.Parallel()

	text := "На сегодня курс доллара США к рублю, установленный Центральным банком России, составляет 72,56 RUB за 1 USD. [sources=[2, 4]]"
	response := &GigaChatResponsesResponse{
		ThreadID:  schemas.Ptr("377b9094-29c4-4711-99a0-69145bb764ec"),
		MessageID: schemas.Ptr("ba4c614c-9c04-4eea-970d-ee22f1f0f94e"),
		Model:     "GigaChat-3-Ultra:32.3.18.5",
		Messages: []GigaChatResponsesMessage{{
			Role:        "assistant",
			ToolStateID: schemas.Ptr("019e8cbd-1f65-77aa-9d24-400001ba0ccf"),
			Content: []GigaChatResponsesContentPart{{
				Text: schemas.Ptr(text),
				InlineData: map[string]interface{}{
					"images": []interface{}{},
					"sources": map[string]interface{}{
						"1": map[string]interface{}{
							"url":   "https://cbr.ru/currency_base/daily/",
							"title": "Официальные курсы валют на заданную дату, устанавливаемые...",
						},
						"2": map[string]interface{}{
							"url":   "https://www.vbr.ru/banki/kurs-valut/cbrf/usd/",
							"title": "Курс доллара США к рублю на сегодня и завтра — Официальный...",
						},
						"3": map[string]interface{}{
							"url":   "https://www.banki.ru/products/currency/cash/moskva/",
							"title": "Курсы валют в Москве на сегодня, выгодный курс обмена...",
						},
						"4": map[string]interface{}{
							"url":   "https://www.profinance.ru/cbrf/usd",
							"title": "Курс доллара к рублю сегодня: онлайн графики и все котировки",
						},
						"5": map[string]interface{}{
							"url":   "https://news.ru/vlast/centrobank-rossii-ponizil-kurs-dollara",
							"title": "Центробанк России понизил курс доллара",
						},
					},
				},
			}},
			FinishReason: schemas.Ptr("stop"),
		}},
	}

	converted := ToBifrostResponsesResponse(schemas.GigaChat, response)
	if converted == nil {
		t.Fatal("expected response, got nil")
	}
	if len(converted.Output) != 2 {
		t.Fatalf("output count mismatch: got %d", len(converted.Output))
	}

	message := converted.Output[0]
	if message.Type == nil || *message.Type != schemas.ResponsesMessageTypeMessage {
		t.Fatalf("assistant output type mismatch: %#v", message.Type)
	}
	if message.Content == nil || len(message.Content.ContentBlocks) != 1 {
		t.Fatalf("assistant content mismatch: %#v", message.Content)
	}
	block := message.Content.ContentBlocks[0]
	if block.ResponsesOutputMessageContentText == nil {
		t.Fatalf("text metadata missing: %#v", block)
	}
	annotations := block.ResponsesOutputMessageContentText.Annotations
	if len(annotations) != 2 {
		t.Fatalf("annotations count mismatch: got %d, annotations=%#v", len(annotations), annotations)
	}
	marker := "[sources=[2, 4]]"
	startIndex := strings.Index(text, marker)
	endIndex := startIndex + len(marker)
	if annotations[0].Type != "url_citation" || annotations[0].URL == nil || *annotations[0].URL != "https://www.vbr.ru/banki/kurs-valut/cbrf/usd/" {
		t.Fatalf("first annotation mismatch: %#v", annotations[0])
	}
	if annotations[0].Title == nil || *annotations[0].Title != "Курс доллара США к рублю на сегодня и завтра — Официальный..." {
		t.Fatalf("first annotation title mismatch: %#v", annotations[0].Title)
	}
	if annotations[0].StartIndex == nil || *annotations[0].StartIndex != startIndex || annotations[0].EndIndex == nil || *annotations[0].EndIndex != endIndex {
		t.Fatalf("first annotation range mismatch: %#v", annotations[0])
	}
	if annotations[1].Type != "url_citation" || annotations[1].URL == nil || *annotations[1].URL != "https://www.profinance.ru/cbrf/usd" {
		t.Fatalf("second annotation mismatch: %#v", annotations[1])
	}

	webSearch := converted.Output[1]
	if webSearch.ID == nil || *webSearch.ID != "ws_ba4c614c-9c04-4eea-970d-ee22f1f0f94e_0" {
		t.Fatalf("web search output id mismatch: %#v", webSearch.ID)
	}
	if webSearch.Type == nil || *webSearch.Type != schemas.ResponsesMessageTypeWebSearchCall {
		t.Fatalf("web search output type mismatch: %#v", webSearch.Type)
	}
	if webSearch.ResponsesToolMessage == nil || webSearch.ResponsesToolMessage.Action == nil || webSearch.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction == nil {
		t.Fatalf("web search action missing: %#v", webSearch.ResponsesToolMessage)
	}
	action := webSearch.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction
	if action.Type != "search" || len(action.Sources) != 5 {
		t.Fatalf("web search sources mismatch: %#v", action)
	}
	if action.Sources[0].URL != "https://cbr.ru/currency_base/daily/" || action.Sources[4].URL != "https://news.ru/vlast/centrobank-rossii-ponizil-kurs-dollara" {
		t.Fatalf("web search source ordering mismatch: %#v", action.Sources)
	}
}

func testGigaChatResponsesConverterMapsReasoningRole(t *testing.T) {
	t.Parallel()

	response := &GigaChatResponsesResponse{
		CreatedAt: 1780306293,
		Model:     "GigaChat-2-Reasoning:2.0.29.05",
		Messages: []GigaChatResponsesMessage{
			{
				Role: "reasoning",
				Content: []GigaChatResponsesContentPart{{
					Text: schemas.Ptr("...reasoning text..."),
				}},
			},
			{
				Role: "assistant",
				Content: []GigaChatResponsesContentPart{{
					Text: schemas.Ptr("**Столица Франции — Париж.**"),
				}},
			},
		},
		FinishReason: schemas.Ptr("stop"),
	}

	converted := ToBifrostResponsesResponse(schemas.GigaChat, response)
	if converted == nil {
		t.Fatal("expected response, got nil")
	}
	if len(converted.Output) != 2 {
		t.Fatalf("output count mismatch: got %d", len(converted.Output))
	}

	reasoning := converted.Output[0]
	if reasoning.Type == nil || *reasoning.Type != schemas.ResponsesMessageTypeReasoning {
		t.Fatalf("reasoning output type mismatch: %#v", reasoning.Type)
	}
	if reasoning.Role == nil || *reasoning.Role != schemas.ResponsesInputMessageRoleAssistant {
		t.Fatalf("reasoning output role mismatch: %#v", reasoning.Role)
	}
	if reasoning.Status == nil || *reasoning.Status != "completed" {
		t.Fatalf("reasoning status mismatch: %#v", reasoning.Status)
	}
	if reasoning.Content != nil {
		t.Fatalf("reasoning should not be converted to ordinary message content: %#v", reasoning.Content)
	}
	if reasoning.ResponsesReasoning == nil || len(reasoning.ResponsesReasoning.Summary) != 1 {
		t.Fatalf("reasoning summary mismatch: %#v", reasoning.ResponsesReasoning)
	}
	summary := reasoning.ResponsesReasoning.Summary[0]
	if summary.Type != schemas.ResponsesReasoningContentBlockTypeSummaryText || summary.Text != "...reasoning text..." {
		t.Fatalf("reasoning summary block mismatch: %#v", summary)
	}

	message := converted.Output[1]
	if message.Type == nil || *message.Type != schemas.ResponsesMessageTypeMessage {
		t.Fatalf("assistant output type mismatch: %#v", message.Type)
	}
	if message.Role == nil || *message.Role != schemas.ResponsesInputMessageRoleAssistant {
		t.Fatalf("assistant output role mismatch: %#v", message.Role)
	}
	if message.Content == nil || len(message.Content.ContentBlocks) != 1 {
		t.Fatalf("assistant content mismatch: %#v", message.Content)
	}
	block := message.Content.ContentBlocks[0]
	if block.Type != schemas.ResponsesOutputMessageContentTypeText || block.Text == nil || *block.Text != "**Столица Франции — Париж.**" {
		t.Fatalf("assistant text block mismatch: %#v", block)
	}
}

func testGigaChatResponsesConverterMapsToolCall(t *testing.T) {
	t.Parallel()

	response := &GigaChatResponsesResponse{
		Model: "GigaChat-2-Max",
		Messages: []GigaChatResponsesMessage{{
			Role:         "assistant",
			MessageID:    schemas.Ptr("call-message"),
			ToolsStateID: schemas.Ptr("tools-state-call"),
			Content: []GigaChatResponsesContentPart{{
				FunctionCall: &GigaChatResponsesFunctionCall{
					Name: "get_weather",
					Arguments: map[string]interface{}{
						"city": "Moscow",
					},
				},
			}},
			FinishReason: schemas.Ptr("function_call"),
		}},
	}

	converted := ToBifrostResponsesResponse(schemas.GigaChat, response)
	if converted == nil {
		t.Fatal("expected response, got nil")
	}
	if converted.Status == nil || *converted.Status != "completed" {
		t.Fatalf("status mismatch: %#v", converted.Status)
	}
	if converted.StopReason == nil || *converted.StopReason != "tool_calls" {
		t.Fatalf("stop reason mismatch: %#v", converted.StopReason)
	}
	if len(converted.Output) != 1 {
		t.Fatalf("output count mismatch: got %d", len(converted.Output))
	}
	output := converted.Output[0]
	if output.Type == nil || *output.Type != schemas.ResponsesMessageTypeFunctionCall {
		t.Fatalf("output type mismatch: %#v", output.Type)
	}
	if output.ResponsesToolMessage == nil || output.ResponsesToolMessage.Name == nil || *output.ResponsesToolMessage.Name != "get_weather" {
		t.Fatalf("tool message mismatch: %#v", output.ResponsesToolMessage)
	}
	if output.ResponsesToolMessage.Arguments == nil || *output.ResponsesToolMessage.Arguments != `{"city":"Moscow"}` {
		t.Fatalf("arguments mismatch: %#v", output.ResponsesToolMessage.Arguments)
	}
	assertGigaChatResponsesEncodedCallID(t, output.ResponsesToolMessage, "tools-state-call")
}

func testGigaChatResponsesConverterMapsFunctionResultWithOriginatingCallID(t *testing.T) {
	t.Parallel()

	response := &GigaChatResponsesResponse{
		Model: "GigaChat-2-Max",
		Messages: []GigaChatResponsesMessage{
			{
				Role:         "assistant",
				MessageID:    schemas.Ptr("call-message"),
				ToolsStateID: schemas.Ptr("tools-state-call"),
				Content: []GigaChatResponsesContentPart{{
					FunctionCall: &GigaChatResponsesFunctionCall{
						Name:      "get_weather",
						Arguments: map[string]interface{}{"city": "Moscow"},
					},
				}},
			},
			{
				Role:         "tool",
				MessageID:    schemas.Ptr("result-message"),
				ToolsStateID: schemas.Ptr("tools-state-call"),
				Content: []GigaChatResponsesContentPart{{
					FunctionResult: &GigaChatResponsesFunctionResult{
						Name:   "get_weather",
						Result: map[string]interface{}{"temperature": 5},
					},
				}},
			},
		},
	}

	converted := ToBifrostResponsesResponse(schemas.GigaChat, response)
	if converted == nil || len(converted.Output) != 2 {
		t.Fatalf("converted output mismatch: %#v", converted)
	}
	toolCall := converted.Output[0]
	toolResult := converted.Output[1]
	if toolCall.Type == nil || *toolCall.Type != schemas.ResponsesMessageTypeFunctionCall {
		t.Fatalf("tool call type mismatch: %#v", toolCall.Type)
	}
	if toolResult.Type == nil || *toolResult.Type != schemas.ResponsesMessageTypeFunctionCallOutput {
		t.Fatalf("tool result type mismatch: %#v", toolResult.Type)
	}
	if toolCall.ResponsesToolMessage == nil || toolCall.ResponsesToolMessage.CallID == nil {
		t.Fatalf("tool call id missing: %#v", toolCall.ResponsesToolMessage)
	}
	if toolResult.ResponsesToolMessage == nil || toolResult.ResponsesToolMessage.CallID == nil {
		t.Fatalf("tool result id missing: %#v", toolResult.ResponsesToolMessage)
	}
	if *toolResult.ResponsesToolMessage.CallID != *toolCall.ResponsesToolMessage.CallID {
		t.Fatalf("tool result call_id should match function call: call=%q result=%q", *toolCall.ResponsesToolMessage.CallID, *toolResult.ResponsesToolMessage.CallID)
	}
	assertGigaChatResponsesEncodedCallID(t, toolCall.ResponsesToolMessage, "tools-state-call")
}

func testGigaChatResponsesConverterUsesUniqueCallIDsUnderSharedToolsStateID(t *testing.T) {
	t.Parallel()

	response := &GigaChatResponsesResponse{
		Model: "GigaChat-2-Max",
		Messages: []GigaChatResponsesMessage{
			{
				Role:         "assistant",
				MessageID:    schemas.Ptr("tool-message-1"),
				ToolsStateID: schemas.Ptr("shared-tools-state"),
				Content: []GigaChatResponsesContentPart{{
					FunctionCall: &GigaChatResponsesFunctionCall{
						Name:      "get_weather",
						Arguments: map[string]interface{}{"city": "Moscow"},
					},
				}},
			},
			{
				Role:         "assistant",
				MessageID:    schemas.Ptr("tool-message-2"),
				ToolsStateID: schemas.Ptr("shared-tools-state"),
				Content: []GigaChatResponsesContentPart{{
					FunctionCall: &GigaChatResponsesFunctionCall{
						Name:      "get_time",
						Arguments: map[string]interface{}{"city": "Moscow"},
					},
				}},
			},
		},
	}

	converted := ToBifrostResponsesResponse(schemas.GigaChat, response)
	if converted == nil || len(converted.Output) != 2 {
		t.Fatalf("converted output mismatch: %#v", converted)
	}
	firstCall := converted.Output[0].ResponsesToolMessage
	secondCall := converted.Output[1].ResponsesToolMessage
	firstCallID := assertGigaChatResponsesEncodedCallID(t, firstCall, "shared-tools-state")
	secondCallID := assertGigaChatResponsesEncodedCallID(t, secondCall, "shared-tools-state")
	if firstCallID == secondCallID {
		t.Fatalf("call ids must be unique: first=%q second=%q", firstCallID, secondCallID)
	}

	weatherOutput := `{"temperature":5}`
	timeOutput := `{"time":"12:00"}`
	request := &schemas.BifrostResponsesRequest{
		Model: "GigaChat-2-Max",
		Input: []schemas.ResponsesMessage{
			converted.Output[0],
			converted.Output[1],
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: firstCall.CallID,
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesToolCallOutputStr: &weatherOutput,
					},
				},
			},
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: secondCall.CallID,
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesToolCallOutputStr: &timeOutput,
					},
				},
			},
		},
	}

	gigaChatReq, err := ToGigaChatResponsesRequest(request)
	if err != nil {
		t.Fatalf("ToGigaChatResponsesRequest returned error: %v", err)
	}
	if len(gigaChatReq.Messages) != 4 {
		t.Fatalf("message count mismatch: got %d", len(gigaChatReq.Messages))
	}
	assertGigaChatResponsesToolStateID(t, gigaChatReq.Messages[0], "shared-tools-state")
	assertGigaChatResponsesToolStateID(t, gigaChatReq.Messages[1], "shared-tools-state")
	assertGigaChatResponsesToolStateID(t, gigaChatReq.Messages[2], "shared-tools-state")
	assertGigaChatResponsesToolStateID(t, gigaChatReq.Messages[3], "shared-tools-state")
	if gigaChatReq.Messages[2].Content[0].FunctionResult == nil || gigaChatReq.Messages[2].Content[0].FunctionResult.Name != "get_weather" {
		t.Fatalf("first function result name mismatch: %#v", gigaChatReq.Messages[2].Content)
	}
	if gigaChatReq.Messages[3].Content[0].FunctionResult == nil || gigaChatReq.Messages[3].Content[0].FunctionResult.Name != "get_time" {
		t.Fatalf("second function result name mismatch: %#v", gigaChatReq.Messages[3].Content)
	}
}

func assertGigaChatResponsesToolStateID(t *testing.T, message GigaChatResponsesMessage, want string) {
	t.Helper()

	if message.ToolsStateID == nil || *message.ToolsStateID != want {
		t.Fatalf("tools_state_id mismatch: got %#v, want %q", message.ToolsStateID, want)
	}
}

func assertGigaChatResponsesEncodedCallID(t *testing.T, toolMessage *schemas.ResponsesToolMessage, toolsStateID string) string {
	t.Helper()

	if toolMessage == nil || toolMessage.CallID == nil {
		t.Fatalf("call id missing: %#v", toolMessage)
	}
	callID := strings.TrimSpace(*toolMessage.CallID)
	if callID == "" {
		t.Fatalf("call id is empty: %#v", toolMessage.CallID)
	}
	if !strings.HasPrefix(callID, gigaChatResponsesGeneratedCallIDPrefix) {
		t.Fatalf("call id should use generated prefix: got %q", callID)
	}
	if callID == toolsStateID {
		t.Fatalf("call id should not be the raw tools_state_id: got %q", callID)
	}
	if decoded := toGigaChatResponsesToolsStateIDFromCallID(callID); decoded != toolsStateID {
		t.Fatalf("decoded tools_state_id mismatch: got %q, want %q", decoded, toolsStateID)
	}
	if toolMessage.Name != nil {
		wantName := strings.TrimSpace(*toolMessage.Name)
		if decodedName := toGigaChatResponsesFunctionNameFromCallID(callID); decodedName != wantName {
			t.Fatalf("decoded function name mismatch: got %q, want %q", decodedName, wantName)
		}
	}
	return callID
}

func testGigaChatResponsesConverterUsesToolStateIDAliasAsCallID(t *testing.T) {
	t.Parallel()

	var response GigaChatResponsesResponse
	if err := json.Unmarshal([]byte(`{
		"model": "GigaChat-3-Ultra",
		"messages": [{
			"role": "assistant",
			"message_id": "call-message",
			"tool_state_id": "019e8282-bb13-73fc-bbe8-5f52856d166b",
			"content": [{
				"function_call": {
					"name": "get_weather",
					"arguments": {"city": "Moscow"}
				}
			}]
		}]
	}`), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	converted := ToBifrostResponsesResponse(schemas.GigaChat, &response)
	if converted == nil || len(converted.Output) != 1 {
		t.Fatalf("converted output mismatch: %#v", converted)
	}
	output := converted.Output[0]
	if output.Type == nil || *output.Type != schemas.ResponsesMessageTypeFunctionCall {
		t.Fatalf("output type mismatch: %#v", output.Type)
	}
	assertGigaChatResponsesEncodedCallID(t, output.ResponsesToolMessage, "019e8282-bb13-73fc-bbe8-5f52856d166b")
}

func testGigaChatResponsesConverterFallsBackToResponseToolsStateID(t *testing.T) {
	t.Parallel()

	response := &GigaChatResponsesResponse{
		Model:        "GigaChat-2-Max",
		ToolsStateID: schemas.Ptr("response-tools-state"),
		Messages: []GigaChatResponsesMessage{
			{
				Role: "assistant",
				Content: []GigaChatResponsesContentPart{{
					FunctionCall: &GigaChatResponsesFunctionCall{
						Name:      "get_weather",
						Arguments: map[string]interface{}{"city": "Moscow"},
					},
				}},
			},
			{
				Role:         "assistant",
				ToolsStateID: schemas.Ptr("message-tools-state"),
				Content: []GigaChatResponsesContentPart{{
					FunctionCall: &GigaChatResponsesFunctionCall{
						Name:      "get_time",
						Arguments: map[string]interface{}{"city": "Moscow"},
					},
				}},
			},
		},
	}

	converted := ToBifrostResponsesResponse(schemas.GigaChat, response)
	if converted == nil || len(converted.Output) != 2 {
		t.Fatalf("converted output mismatch: %#v", converted)
	}
	firstCall := converted.Output[0].ResponsesToolMessage
	assertGigaChatResponsesEncodedCallID(t, firstCall, "response-tools-state")
	secondCall := converted.Output[1].ResponsesToolMessage
	assertGigaChatResponsesEncodedCallID(t, secondCall, "message-tools-state")
}

func testGigaChatResponsesConverterPreservesOrdinaryMessageToolStateID(t *testing.T) {
	t.Parallel()

	response := &GigaChatResponsesResponse{
		Model: "GigaChat-3-Ultra",
		Messages: []GigaChatResponsesMessage{{
			Role:        "assistant",
			MessageID:   schemas.Ptr("ordinary-message"),
			ToolStateID: schemas.Ptr("019e8282-bb13-73fc-bbe8-5f52856d166b"),
			Content: []GigaChatResponsesContentPart{{
				Text: schemas.Ptr("Forecast: Next Tuesday brings a useful introduction."),
			}},
		}},
	}

	converted := ToBifrostResponsesResponse(schemas.GigaChat, response)
	if converted == nil || len(converted.Output) != 1 {
		t.Fatalf("converted output mismatch: %#v", converted)
	}
	output := converted.Output[0]
	if output.Type == nil || *output.Type != schemas.ResponsesMessageTypeMessage {
		t.Fatalf("ordinary assistant output type mismatch: %#v", output.Type)
	}
	if output.ResponsesToolMessage != nil {
		t.Fatalf("ordinary assistant message should not get tool call fields: %#v", output.ResponsesToolMessage)
	}
	rawStateIDs, ok := converted.ProviderExtraFields["message_tools_state_ids"].([]map[string]interface{})
	if !ok || len(rawStateIDs) != 1 {
		t.Fatalf("message tool state metadata mismatch: %#v", converted.ProviderExtraFields)
	}
	stateID := rawStateIDs[0]
	if stateID["tools_state_id"] != "019e8282-bb13-73fc-bbe8-5f52856d166b" || stateID["message_id"] != "ordinary-message" || stateID["role"] != "assistant" || stateID["index"] != 0 {
		t.Fatalf("message tool state metadata mismatch: %#v", stateID)
	}
}

func testGigaChatResponsesConverterMapsThreadStorage(t *testing.T) {
	t.Parallel()

	response := &GigaChatResponsesResponse{
		ThreadID:  schemas.Ptr("thread-123"),
		MessageID: schemas.Ptr("message-456"),
		Model:     "GigaChat-3-Ultra",
		Messages: []GigaChatResponsesMessage{{
			Role: "assistant",
			Content: []GigaChatResponsesContentPart{{
				Text: schemas.Ptr("Stored context response."),
			}},
			FinishReason: schemas.Ptr("stop"),
		}},
	}

	converted := ToBifrostResponsesResponse(schemas.GigaChat, response)
	if converted == nil {
		t.Fatal("expected response, got nil")
	}
	if converted.ID == nil || *converted.ID != "thread-123" {
		t.Fatalf("response id should fall back to thread id, got %#v", converted.ID)
	}
	if converted.Conversation == nil ||
		converted.Conversation.ResponsesResponseConversationStruct == nil ||
		converted.Conversation.ResponsesResponseConversationStruct.ID != "thread-123" {
		t.Fatalf("conversation mismatch: %#v", converted.Conversation)
	}
	if converted.ProviderExtraFields["thread_id"] != "thread-123" || converted.ProviderExtraFields["message_id"] != "message-456" {
		t.Fatalf("provider extra fields mismatch: %#v", converted.ProviderExtraFields)
	}
}

func testGigaChatResponsesExecutesWithOAuthToken(t *testing.T) {
	t.Parallel()

	var tokenRequests atomic.Int32
	var responsesRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth":
			tokenRequests.Add(1)
			if got := request.Header.Get("Authorization"); got != "Basic super-secret-credentials" {
				t.Fatalf("token authorization header mismatch: got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"responses-access-token","expires_at":1893456000}`))
		case "/v2/chat/completions":
			responsesRequests.Add(1)
			if got := request.Header.Get("Authorization"); got != "Bearer responses-access-token" {
				t.Fatalf("responses authorization header mismatch: got %q", got)
			}
			if strings.Contains(request.Header.Get("Authorization"), "super-secret-credentials") {
				t.Fatal("responses request leaked OAuth credentials")
			}
			assertGigaChatResponsesRequestBody(t, request)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Request-ID", "responses-request-id")
			_, _ = w.Write([]byte(`{
				"message_id":"resp-test",
				"messages":[{"role":"assistant","message_id":"msg-test","content":[{"text":"Здравствуйте"}],"finish_reason":"stop"}],
				"created_at":1700000000,
				"model":"GigaChat-2",
				"usage":{"input_tokens":7,"input_tokens_details":{"cached_tokens":2},"output_tokens":3,"total_tokens":10}
			}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	provider.sendBackRawRequest = true
	provider.sendBackRawResponse = true
	response, bifrostErr := provider.Responses(testBifrostContext(), testGigaChatOAuthKey(server.URL+"/oauth", "", "super-secret-credentials"), testGigaChatResponsesExecutionRequest())
	if bifrostErr != nil {
		t.Fatalf("Responses returned error: %v", bifrostErr)
	}
	if tokenRequests.Load() != 1 {
		t.Fatalf("token request count mismatch: got %d, want 1", tokenRequests.Load())
	}
	if responsesRequests.Load() != 1 {
		t.Fatalf("responses request count mismatch: got %d, want 1", responsesRequests.Load())
	}
	if response.ID == nil || *response.ID != "resp-test" {
		t.Fatalf("id mismatch: %#v", response.ID)
	}
	if response.ExtraFields.Provider != schemas.GigaChat {
		t.Fatalf("provider mismatch: got %q, want %q", response.ExtraFields.Provider, schemas.GigaChat)
	}
	if response.Usage == nil || response.Usage.TotalTokens != 10 {
		t.Fatalf("usage mismatch: %#v", response.Usage)
	}
	if len(response.Output) != 1 || response.Output[0].Content == nil || len(response.Output[0].Content.ContentBlocks) != 1 {
		t.Fatalf("output mismatch: %#v", response.Output)
	}
	if got := *response.Output[0].Content.ContentBlocks[0].Text; got != "Здравствуйте" {
		t.Fatalf("content mismatch: got %q", got)
	}
	if response.ExtraFields.RawRequest == nil || response.ExtraFields.RawResponse == nil {
		t.Fatalf("expected raw request and response, got request=%#v response=%#v", response.ExtraFields.RawRequest, response.ExtraFields.RawResponse)
	}
	if got := response.ExtraFields.ProviderResponseHeaders["X-Request-Id"]; got != "responses-request-id" {
		t.Fatalf("provider response header mismatch: %#v", response.ExtraFields.ProviderResponseHeaders)
	}
}

func testGigaChatResponsesUploadsInputImageAttachment(t *testing.T) {
	t.Parallel()

	var uploadRequests atomic.Int32
	var responsesRequests atomic.Int32
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
			if header.Filename != "image.png" {
				t.Fatalf("uploaded image filename mismatch: got %q", header.Filename)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"uploaded-image","object":"file","bytes":11,"created_at":1700000000,"filename":"image.png","purpose":"general"}`))
		case "/v2/chat/completions":
			responsesRequests.Add(1)
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("failed to read responses body: %v", err)
			}
			assertGigaChatResponsesBodyFile(t, body, "uploaded-image")
			bodyStr := string(body)
			if strings.Contains(bodyStr, "data:image") || strings.Contains(bodyStr, "image_url") {
				t.Fatalf("responses body leaked OpenAI image payload: %s", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"messages":[{"role":"assistant","content":[{"text":"На изображении..."}],"finish_reason":"stop"}],"model":"GigaChat-2"}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	prompt := "What's in this image?"
	imageURL := "data:image/png;base64,aW1hZ2UtYnl0ZXM="
	request := &schemas.BifrostResponsesRequest{
		Model: "GigaChat-2",
		Input: []schemas.ResponsesMessage{{
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
				{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: &prompt},
				{
					Type: schemas.ResponsesInputMessageContentBlockTypeImage,
					ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
						ImageURL: &imageURL,
					},
				},
			}},
		}},
	}

	provider := newTestGigaChatChatProvider(t, server.URL)
	response, bifrostErr := provider.Responses(testBifrostContext(), testGigaChatAccessTokenKey("image-token"), request)
	if bifrostErr != nil {
		t.Fatalf("Responses returned error: %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("expected response, got nil")
	}
	if uploadRequests.Load() != 1 {
		t.Fatalf("upload request count mismatch: got %d, want 1", uploadRequests.Load())
	}
	if responsesRequests.Load() != 1 {
		t.Fatalf("responses request count mismatch: got %d, want 1", responsesRequests.Load())
	}
}

func testGigaChatResponsesUploadsInlineFileAttachment(t *testing.T) {
	t.Parallel()

	var uploadRequests atomic.Int32
	var responsesRequests atomic.Int32
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
			if header.Filename != "report.pdf" {
				t.Fatalf("uploaded filename mismatch: got %q", header.Filename)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"uploaded-pdf","object":"file","bytes":9,"created_at":1700000000,"filename":"report.pdf","purpose":"general"}`))
		case "/v2/chat/completions":
			responsesRequests.Add(1)
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("failed to read responses body: %v", err)
			}
			assertGigaChatResponsesBodyFile(t, body, "uploaded-pdf")
			bodyStr := string(body)
			if strings.Contains(bodyStr, "file_data") || strings.Contains(bodyStr, "application/pdf;base64") {
				t.Fatalf("responses body leaked OpenAI file payload: %s", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"messages":[{"role":"assistant","content":[{"text":"Краткое содержание..."}],"finish_reason":"stop"}],"model":"GigaChat-2"}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	prompt := "Summarize this file."
	filename := "report.pdf"
	fileType := "application/pdf"
	fileData := "data:application/pdf;base64,JVBERiB0ZXN0"
	request := &schemas.BifrostResponsesRequest{
		Model: "GigaChat-2",
		Input: []schemas.ResponsesMessage{{
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
				{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: &prompt},
				{
					Type: schemas.ResponsesInputMessageContentBlockTypeFile,
					ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
						Filename: &filename,
						FileType: &fileType,
						FileData: &fileData,
					},
				},
			}},
		}},
	}

	provider := newTestGigaChatChatProvider(t, server.URL)
	response, bifrostErr := provider.Responses(testBifrostContext(), testGigaChatAccessTokenKey("file-token"), request)
	if bifrostErr != nil {
		t.Fatalf("Responses returned error: %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("expected response, got nil")
	}
	if uploadRequests.Load() != 1 {
		t.Fatalf("upload request count mismatch: got %d, want 1", uploadRequests.Load())
	}
	if responsesRequests.Load() != 1 {
		t.Fatalf("responses request count mismatch: got %d, want 1", responsesRequests.Load())
	}
}

func testGigaChatResponsesReusesUploadedAttachmentAfterBackendError(t *testing.T) {
	t.Parallel()

	var uploadRequests atomic.Int32
	var responsesRequests atomic.Int32
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
		case "/v2/chat/completions":
			requestIndex := responsesRequests.Add(1)
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("failed to read responses body: %v", err)
			}
			assertGigaChatResponsesBodyFile(t, body, "uploaded-retry-pdf")
			if requestIndex == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"status":500,"message":"temporary backend failure"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"messages":[{"role":"assistant","content":[{"text":"ok"}],"finish_reason":"stop"}],"model":"GigaChat-2"}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	prompt := "Summarize this file."
	filename := "retry.pdf"
	fileType := "application/pdf"
	fileData := "data:application/pdf;base64,JVBERiByZXRyeQ=="
	request := &schemas.BifrostResponsesRequest{
		Model: "GigaChat-2",
		Input: []schemas.ResponsesMessage{{
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
				{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: &prompt},
				{
					Type: schemas.ResponsesInputMessageContentBlockTypeFile,
					ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
						Filename: &filename,
						FileType: &fileType,
						FileData: &fileData,
					},
				},
			}},
		}},
	}

	provider := newTestGigaChatChatProvider(t, server.URL)
	ctx := testBifrostContext()
	key := testGigaChatAccessTokenKey("file-token")

	firstResponse, firstErr := provider.Responses(ctx, key, request)
	if firstResponse != nil {
		t.Fatalf("expected nil response from first backend failure, got %#v", firstResponse)
	}
	if firstErr == nil || firstErr.StatusCode == nil || *firstErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected first backend 500, got %#v", firstErr)
	}

	response, bifrostErr := provider.Responses(ctx, key, request)
	if bifrostErr != nil {
		t.Fatalf("second Responses returned error: %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("expected second response, got nil")
	}
	if uploadRequests.Load() != 1 {
		t.Fatalf("upload request count mismatch: got %d, want 1", uploadRequests.Load())
	}
	if responsesRequests.Load() != 2 {
		t.Fatalf("responses request count mismatch: got %d, want 2", responsesRequests.Load())
	}
}

func testGigaChatResponsesReusesCompletedUploadsAfterPartialAttachmentFailure(t *testing.T) {
	t.Parallel()

	var firstFileUploads atomic.Int32
	var secondFileUploads atomic.Int32
	var responsesRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/files":
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

			w.Header().Set("Content-Type", "application/json")
			switch string(fileBytes) {
			case "%PDF first":
				firstFileUploads.Add(1)
				_, _ = w.Write([]byte(`{"id":"uploaded-first-pdf","object":"file","bytes":10,"created_at":1700000000,"filename":"first.pdf","purpose":"general"}`))
			case "%PDF second":
				if secondFileUploads.Add(1) == 1 {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"status":500,"message":"temporary second upload failure"}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"uploaded-second-pdf","object":"file","bytes":11,"created_at":1700000000,"filename":"second.pdf","purpose":"general"}`))
			default:
				t.Fatalf("unexpected uploaded file bytes: %q", fileBytes)
			}
		case "/v2/chat/completions":
			responsesRequests.Add(1)
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("failed to read responses body: %v", err)
			}
			var payload struct {
				Messages []struct {
					Content []struct {
						Files []struct {
							ID string `json:"id"`
						} `json:"files"`
					} `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("failed to unmarshal responses body %s: %v", body, err)
			}
			if len(payload.Messages) != 1 || len(payload.Messages[0].Content) != 3 {
				t.Fatalf("responses body content mismatch: %s", body)
			}
			firstFiles := payload.Messages[0].Content[1].Files
			secondFiles := payload.Messages[0].Content[2].Files
			if len(firstFiles) != 1 || firstFiles[0].ID != "uploaded-first-pdf" {
				t.Fatalf("first uploaded file id mismatch: %#v body %s", firstFiles, body)
			}
			if len(secondFiles) != 1 || secondFiles[0].ID != "uploaded-second-pdf" {
				t.Fatalf("second uploaded file id mismatch: %#v body %s", secondFiles, body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"messages":[{"role":"assistant","content":[{"text":"ok"}],"finish_reason":"stop"}],"model":"GigaChat-2"}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	prompt := "Compare these files."
	firstFilename := "first.pdf"
	secondFilename := "second.pdf"
	fileType := "application/pdf"
	firstFileData := "data:application/pdf;base64,JVBERiBmaXJzdA=="
	secondFileData := "data:application/pdf;base64,JVBERiBzZWNvbmQ="
	request := &schemas.BifrostResponsesRequest{
		Model: "GigaChat-2",
		Input: []schemas.ResponsesMessage{{
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
				{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: &prompt},
				{
					Type: schemas.ResponsesInputMessageContentBlockTypeFile,
					ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
						Filename: &firstFilename,
						FileType: &fileType,
						FileData: &firstFileData,
					},
				},
				{
					Type: schemas.ResponsesInputMessageContentBlockTypeFile,
					ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
						Filename: &secondFilename,
						FileType: &fileType,
						FileData: &secondFileData,
					},
				},
			}},
		}},
	}

	provider := newTestGigaChatChatProvider(t, server.URL)
	ctx := testBifrostContext()
	key := testGigaChatAccessTokenKey("file-token")

	firstResponse, firstErr := provider.Responses(ctx, key, request)
	if firstResponse != nil {
		t.Fatalf("expected nil response from partial upload failure, got %#v", firstResponse)
	}
	if firstErr == nil || firstErr.StatusCode == nil || *firstErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected second attachment upload 500, got %#v", firstErr)
	}

	response, bifrostErr := provider.Responses(ctx, key, request)
	if bifrostErr != nil {
		t.Fatalf("second Responses returned error: %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("expected second response, got nil")
	}
	if firstFileUploads.Load() != 1 {
		t.Fatalf("first file upload count mismatch: got %d, want 1", firstFileUploads.Load())
	}
	if secondFileUploads.Load() != 2 {
		t.Fatalf("second file upload count mismatch: got %d, want 2", secondFileUploads.Load())
	}
	if responsesRequests.Load() != 1 {
		t.Fatalf("responses request count mismatch: got %d, want 1", responsesRequests.Load())
	}
}

func testGigaChatResponsesMapsProviderErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":400,"code":123,"message":"bad responses request"}`))
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	response, bifrostErr := provider.Responses(testBifrostContext(), testGigaChatAccessTokenKey("provider-error-token"), testGigaChatResponsesExecutionRequest())
	if response != nil {
		t.Fatalf("expected nil response, got %#v", response)
	}
	if bifrostErr == nil {
		t.Fatal("expected provider error, got nil")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("status mismatch: %#v", bifrostErr.StatusCode)
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Message != "bad responses request" {
		t.Fatalf("message mismatch: %#v", bifrostErr.Error)
	}
	if bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "123" {
		t.Fatalf("code mismatch: %#v", bifrostErr.Error)
	}
	assertNoGigaChatSecretLeak(t, bifrostErr.String())
}

func testGigaChatResponsesRefreshesTokenAfterUnauthorized(t *testing.T) {
	t.Parallel()

	var tokenRequests atomic.Int32
	var responsesRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth":
			tokenIndex := tokenRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"access_token":"responses-token-%d","expires_at":1893456000}`, tokenIndex)))
		case "/v2/chat/completions":
			responsesIndex := responsesRequests.Add(1)
			wantAuthorization := fmt.Sprintf("Bearer responses-token-%d", responsesIndex)
			if got := request.Header.Get("Authorization"); got != wantAuthorization {
				t.Fatalf("authorization header mismatch on request %d: got %q, want %q", responsesIndex, got, wantAuthorization)
			}
			w.Header().Set("Content-Type", "application/json")
			if responsesIndex == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"status":401,"message":"expired token"}`))
				return
			}
			_, _ = w.Write([]byte(`{"messages":[{"role":"assistant","content":[{"text":"ok"}],"finish_reason":"stop"}],"model":"GigaChat-2"}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	response, bifrostErr := provider.Responses(testBifrostContext(), testGigaChatOAuthKey(server.URL+"/oauth", "", "test-credentials"), testGigaChatResponsesExecutionRequest())
	if bifrostErr != nil {
		t.Fatalf("Responses returned error: %v", bifrostErr)
	}
	if response == nil || len(response.Output) != 1 {
		t.Fatalf("unexpected response: %#v", response)
	}
	if tokenRequests.Load() != 2 {
		t.Fatalf("token request count mismatch: got %d, want 2", tokenRequests.Load())
	}
	if responsesRequests.Load() != 2 {
		t.Fatalf("responses request count mismatch: got %d, want 2", responsesRequests.Load())
	}
}

func testGigaChatResponsesStreamTextDeltasAndUsage(t *testing.T) {
	t.Parallel()

	var tokenRequests atomic.Int32
	var streamRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth":
			tokenRequests.Add(1)
			if got := request.Header.Get("Authorization"); got != "Basic super-secret-credentials" {
				t.Fatalf("token authorization header mismatch: got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"responses-stream-token","expires_at":1893456000}`))
		case "/v2/chat/completions":
			streamRequests.Add(1)
			if got := request.Header.Get("Authorization"); got != "Bearer responses-stream-token" {
				t.Fatalf("stream authorization header mismatch: got %q", got)
			}
			if strings.Contains(request.Header.Get("Authorization"), "super-secret-credentials") {
				t.Fatal("stream request leaked OAuth credentials")
			}
			assertGigaChatResponsesStreamRequestBody(t, request)
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("X-Request-ID", "responses-stream-request-id")
			_, _ = w.Write([]byte("data: {\"event\":\"message\",\"message_id\":\"resp-stream\",\"messages\":[{\"role\":\"assistant\",\"content\":[{\"text\":\"При\"}]}],\"created_at\":1700000000,\"model\":\"GigaChat-2\"}\n\n"))
			_, _ = w.Write([]byte("data: {\"event\":\"message\",\"message_id\":\"resp-stream\",\"messages\":[{\"content\":[{\"text\":\"вет\"}]}],\"created_at\":1700000000,\"model\":\"GigaChat-2\"}\n\n"))
			_, _ = w.Write([]byte("data: {\"event\":\"done\",\"message_id\":\"resp-stream\",\"messages\":[{\"finish_reason\":\"stop\"}],\"created_at\":1700000000,\"model\":\"GigaChat-2\",\"usage\":{\"input_tokens\":7,\"input_tokens_details\":{\"cached_tokens\":2},\"output_tokens\":3,\"total_tokens\":10}}\n\n"))
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

	stream, bifrostErr := provider.ResponsesStream(ctx, testGigaChatPostHookRunner, nil, testGigaChatOAuthKey(server.URL+"/oauth", "", "super-secret-credentials"), testGigaChatResponsesExecutionRequest())
	if bifrostErr != nil {
		t.Fatalf("ResponsesStream returned error: %v", bifrostErr)
	}

	chunks := collectGigaChatStreamChunks(t, stream)
	if tokenRequests.Load() != 1 {
		t.Fatalf("token request count mismatch: got %d, want 1", tokenRequests.Load())
	}
	if streamRequests.Load() != 1 {
		t.Fatalf("stream request count mismatch: got %d, want 1", streamRequests.Load())
	}

	responses := collectGigaChatResponsesStreamResponses(t, chunks)
	assertGigaChatResponsesStreamTypes(t, responses, []schemas.ResponsesStreamResponseType{
		schemas.ResponsesStreamResponseTypeCreated,
		schemas.ResponsesStreamResponseTypeInProgress,
		schemas.ResponsesStreamResponseTypeOutputItemAdded,
		schemas.ResponsesStreamResponseTypeContentPartAdded,
		schemas.ResponsesStreamResponseTypeOutputTextDelta,
		schemas.ResponsesStreamResponseTypeOutputTextDelta,
		schemas.ResponsesStreamResponseTypeOutputTextDone,
		schemas.ResponsesStreamResponseTypeContentPartDone,
		schemas.ResponsesStreamResponseTypeOutputItemDone,
		schemas.ResponsesStreamResponseTypeCompleted,
	})
	if responses[4].Delta == nil || *responses[4].Delta != "При" {
		t.Fatalf("first delta mismatch: %#v", responses[4].Delta)
	}
	if responses[5].Delta == nil || *responses[5].Delta != "вет" {
		t.Fatalf("second delta mismatch: %#v", responses[5].Delta)
	}
	finalResponse := responses[len(responses)-1]
	if finalResponse.Response == nil || finalResponse.Response.Usage == nil || finalResponse.Response.Usage.TotalTokens != 10 {
		t.Fatalf("final usage mismatch: %#v", finalResponse.Response)
	}
	if finalResponse.Response.Usage.InputTokensDetails == nil || finalResponse.Response.Usage.InputTokensDetails.CachedReadTokens != 2 {
		t.Fatalf("cached token usage mismatch: %#v", finalResponse.Response.Usage)
	}
	if finalResponse.Response.Status == nil || *finalResponse.Response.Status != "completed" {
		t.Fatalf("final status mismatch: %#v", finalResponse.Response.Status)
	}
	if finalResponse.ExtraFields.RawRequest == nil || finalResponse.ExtraFields.RawResponse == nil {
		t.Fatalf("expected raw request and response, got request=%#v response=%#v", finalResponse.ExtraFields.RawRequest, finalResponse.ExtraFields.RawResponse)
	}
	if got := ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders); got == nil {
		t.Fatal("provider response headers were not stored in context")
	}
}

func testGigaChatResponsesStreamReasoningDeltas(t *testing.T) {
	t.Parallel()

	state := schemas.AcquireChatToResponsesStreamState()
	defer schemas.ReleaseChatToResponsesStreamState(state)

	response := &GigaChatResponsesResponse{
		MessageID: schemas.Ptr("resp-reasoning-stream"),
		CreatedAt: 1780306293,
		Model:     "GigaChat-2-Reasoning:2.0.29.05",
		Messages: []GigaChatResponsesMessage{{
			Role: "reasoning",
			Content: []GigaChatResponsesContentPart{{
				Text: schemas.Ptr("streamed reasoning"),
			}},
		}},
	}

	events := ToBifrostResponsesStreamResponse(schemas.GigaChat, response, state)
	if len(events) == 0 {
		t.Fatal("expected stream events, got none")
	}

	var foundReasoningDelta bool
	for _, event := range events {
		if event == nil {
			continue
		}
		if event.Type == schemas.ResponsesStreamResponseTypeOutputTextDelta && event.Delta != nil && *event.Delta == "streamed reasoning" {
			t.Fatalf("reasoning delta was emitted as output_text: %#v", event)
		}
		if event.Type == schemas.ResponsesStreamResponseTypeOutputItemAdded && event.Item != nil && event.Item.Role != nil && *event.Item.Role == schemas.ResponsesMessageRoleType("reasoning") {
			t.Fatalf("reasoning delta created ordinary message role=reasoning: %#v", event.Item)
		}
		if event.Type == schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta && event.Delta != nil && *event.Delta == "streamed reasoning" {
			foundReasoningDelta = true
		}
	}
	if !foundReasoningDelta {
		t.Fatalf("expected reasoning summary delta, got %#v", events)
	}
}

func testGigaChatResponsesStreamToolCallDeltas(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v2/chat/completions" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		assertGigaChatResponsesStreamRequestBody(t, request)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"event\":\"message\",\"message_id\":\"resp-tools\",\"messages\":[{\"role\":\"assistant\",\"tools_state_id\":\"call-weather\",\"content\":[{\"function_call\":{\"name\":\"get_weather\",\"arguments\":{\"city\":\"Moscow\"}}}]}],\"created_at\":1700000000,\"model\":\"GigaChat-2\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"event\":\"done\",\"message_id\":\"resp-tools\",\"messages\":[{\"finish_reason\":\"function_call\"}],\"created_at\":1700000000,\"model\":\"GigaChat-2\",\"usage\":{\"input_tokens\":11,\"output_tokens\":4,\"total_tokens\":15}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	stream, bifrostErr := provider.ResponsesStream(testBifrostContext(), testGigaChatPostHookRunner, nil, testGigaChatAccessTokenKey("responses-stream-token"), testGigaChatResponsesExecutionRequest())
	if bifrostErr != nil {
		t.Fatalf("ResponsesStream returned error: %v", bifrostErr)
	}

	responses := collectGigaChatResponsesStreamResponses(t, collectGigaChatStreamChunks(t, stream))
	assertGigaChatResponsesStreamTypes(t, responses, []schemas.ResponsesStreamResponseType{
		schemas.ResponsesStreamResponseTypeCreated,
		schemas.ResponsesStreamResponseTypeInProgress,
		schemas.ResponsesStreamResponseTypeOutputItemAdded,
		schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
		schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone,
		schemas.ResponsesStreamResponseTypeOutputItemDone,
		schemas.ResponsesStreamResponseTypeCompleted,
	})
	if responses[2].Item == nil || responses[2].Item.ResponsesToolMessage == nil || responses[2].Item.ResponsesToolMessage.Name == nil || *responses[2].Item.ResponsesToolMessage.Name != "get_weather" {
		t.Fatalf("tool item mismatch: %#v", responses[2].Item)
	}
	if responses[3].Delta == nil || *responses[3].Delta != `{"city":"Moscow"}` {
		t.Fatalf("tool delta mismatch: %#v", responses[3].Delta)
	}
	if responses[4].Arguments == nil || *responses[4].Arguments != `{"city":"Moscow"}` {
		t.Fatalf("tool arguments mismatch: %#v", responses[4].Arguments)
	}
	finalResponse := responses[len(responses)-1]
	if finalResponse.Response == nil || finalResponse.Response.Usage == nil || finalResponse.Response.Usage.TotalTokens != 15 {
		t.Fatalf("final usage mismatch: %#v", finalResponse.Response)
	}
}

func testGigaChatResponsesStreamClosesOnMessageDoneEvent(t *testing.T) {
	t.Parallel()

	releaseServer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v2/chat/completions" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		assertGigaChatResponsesStreamRequestBody(t, request)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer does not support flushing")
		}
		_, _ = w.Write([]byte("event: response.message.delta\ndata: {\"message_id\":\"resp-done-event\",\"messages\":[{\"role\":\"assistant\",\"content\":[{\"text\":\"Привет\"}]}],\"created_at\":1780315871,\"model\":\"GigaChat-2-Reasoning:2.0.29.05\"}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("event: response.message.done\ndata: {\"model\":\"GigaChat-2-Reasoning:2.0.29.05\",\"created_at\":1780315871,\"finish_reason\":\"stop\",\"usage\":{\"input_tokens\":29,\"input_tokens_details\":{\"prompt_tokens\":29,\"cached_tokens\":3},\"output_tokens\":109,\"total_tokens\":138}}\n\n"))
		flusher.Flush()
		select {
		case <-request.Context().Done():
		case <-releaseServer:
		}
	}))
	defer server.Close()
	defer close(releaseServer)

	provider := newTestGigaChatChatProvider(t, server.URL)
	stream, bifrostErr := provider.ResponsesStream(testBifrostContext(), testGigaChatPostHookRunner, nil, testGigaChatAccessTokenKey("responses-stream-token"), testGigaChatResponsesExecutionRequest())
	if bifrostErr != nil {
		t.Fatalf("ResponsesStream returned error: %v", bifrostErr)
	}

	chunksDone := make(chan []*schemas.BifrostStreamChunk, 1)
	go func() {
		chunks := make([]*schemas.BifrostStreamChunk, 0)
		for chunk := range stream {
			chunks = append(chunks, chunk)
		}
		chunksDone <- chunks
	}()

	var chunks []*schemas.BifrostStreamChunk
	select {
	case chunks = <-chunksDone:
	case <-time.After(time.Second):
		t.Fatal("responses stream did not close after response.message.done")
	}

	responses := collectGigaChatResponsesStreamResponses(t, chunks)
	assertGigaChatResponsesStreamTypes(t, responses, []schemas.ResponsesStreamResponseType{
		schemas.ResponsesStreamResponseTypeCreated,
		schemas.ResponsesStreamResponseTypeInProgress,
		schemas.ResponsesStreamResponseTypeOutputItemAdded,
		schemas.ResponsesStreamResponseTypeContentPartAdded,
		schemas.ResponsesStreamResponseTypeOutputTextDelta,
		schemas.ResponsesStreamResponseTypeOutputTextDone,
		schemas.ResponsesStreamResponseTypeContentPartDone,
		schemas.ResponsesStreamResponseTypeOutputItemDone,
		schemas.ResponsesStreamResponseTypeCompleted,
	})
	finalResponse := responses[len(responses)-1]
	if finalResponse.Response == nil || finalResponse.Response.Usage == nil || finalResponse.Response.Usage.TotalTokens != 138 {
		t.Fatalf("final usage mismatch: %#v", finalResponse.Response)
	}
}

func testGigaChatResponsesStreamMapsErrorEvents(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v2/chat/completions" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"status\":429,\"code\":42901,\"message\":\"rate limit\"}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	stream, bifrostErr := provider.ResponsesStream(testBifrostContext(), testGigaChatPostHookRunner, nil, testGigaChatAccessTokenKey("responses-stream-token"), testGigaChatResponsesExecutionRequest())
	if bifrostErr != nil {
		t.Fatalf("ResponsesStream returned error before stream: %v", bifrostErr)
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

func testGigaChatResponsesStreamHandlesContextCancellation(t *testing.T) {
	t.Parallel()

	firstChunkWritten := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v2/chat/completions" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"event\":\"message\",\"message_id\":\"resp-cancel\",\"messages\":[{\"role\":\"assistant\",\"content\":[{\"text\":\"partial\"}]}],\"created_at\":1700000000,\"model\":\"GigaChat-2\"}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(firstChunkWritten)
		<-request.Context().Done()
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	stream, bifrostErr := provider.ResponsesStream(ctx, testGigaChatPostHookRunner, nil, testGigaChatAccessTokenKey("responses-stream-token"), testGigaChatResponsesExecutionRequest())
	if bifrostErr != nil {
		t.Fatalf("ResponsesStream returned error: %v", bifrostErr)
	}

	select {
	case <-firstChunkWritten:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first stream chunk")
	}

	select {
	case firstChunk := <-stream:
		if firstChunk == nil || firstChunk.BifrostResponsesStreamResponse == nil {
			t.Fatalf("missing first responses stream chunk: %#v", firstChunk)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first response chunk")
	}

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

func testGigaChatResponsesStreamPassthroughResponseOwnedByLargeReader(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v2/chat/completions" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		assertGigaChatResponsesStreamRequestBody(t, request)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"event\":\"message\",\"message_id\":\"resp-large\",\"messages\":[{\"role\":\"assistant\",\"content\":[{\"text\":\"large\"}]}],\"created_at\":1700000000,\"model\":\"GigaChat-2\"}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	ctx := testBifrostContext()
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)

	var finalizerCalls atomic.Int32
	stream, bifrostErr := provider.ResponsesStream(ctx, testGigaChatPostHookRunner, func(context.Context) {
		finalizerCalls.Add(1)
	}, testGigaChatAccessTokenKey("responses-stream-token"), testGigaChatResponsesExecutionRequest())
	if bifrostErr != nil {
		t.Fatalf("ResponsesStream returned error: %v", bifrostErr)
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
	if !strings.Contains(string(body), `"message_id":"resp-large"`) {
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

func testGigaChatResponsesRequest() *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Model: "GigaChat-2",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")},
		}},
	}
}

func testGigaChatResponsesExecutionRequest() *schemas.BifrostResponsesRequest {
	maxTokens := 128
	return &schemas.BifrostResponsesRequest{
		Model: "GigaChat-2",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Привет")},
		}},
		Params: &schemas.ResponsesParameters{
			MaxOutputTokens: &maxTokens,
		},
	}
}

func assertGigaChatResponsesRequestBody(t *testing.T, request *http.Request) {
	t.Helper()

	if request.Method != http.MethodPost {
		t.Fatalf("method mismatch: got %s, want POST", request.Method)
	}
	if got := request.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content type mismatch: got %q", got)
	}
	if got := request.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("accept header mismatch: got %q", got)
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
	if got := payload["model"]; got != "GigaChat-2" {
		t.Fatalf("model mismatch: got %#v", got)
	}
	if _, ok := payload["stream"]; ok {
		t.Fatalf("non-streaming responses request should omit stream: %s", body)
	}
	modelOptions, ok := payload["model_options"].(map[string]interface{})
	if !ok {
		t.Fatalf("model_options mismatch: %#v", payload["model_options"])
	}
	if got := modelOptions["max_tokens"]; got != float64(128) {
		t.Fatalf("max_tokens mismatch: got %#v", got)
	}
	storage, ok := payload["storage"].(map[string]interface{})
	if !ok || len(storage) != 0 {
		t.Fatalf("storage mismatch: %#v", payload["storage"])
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
	content, ok := message["content"].([]interface{})
	if !ok || len(content) != 1 {
		t.Fatalf("message content mismatch: %#v", message["content"])
	}
	contentPart, ok := content[0].(map[string]interface{})
	if !ok || contentPart["text"] != "Привет" {
		t.Fatalf("content part mismatch: %#v", content[0])
	}
}

func assertGigaChatResponsesBodyFile(t *testing.T, body []byte, wantFileID string) map[string]interface{} {
	t.Helper()

	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("failed to unmarshal responses body %s: %v", body, err)
	}
	messages, ok := payload["messages"].([]interface{})
	if !ok || len(messages) != 1 {
		t.Fatalf("messages mismatch: %#v", payload["messages"])
	}
	message, ok := messages[0].(map[string]interface{})
	if !ok {
		t.Fatalf("message shape mismatch: %#v", messages[0])
	}
	content, ok := message["content"].([]interface{})
	if !ok || len(content) != 2 {
		t.Fatalf("message content mismatch: %#v", message["content"])
	}
	filePart, ok := content[1].(map[string]interface{})
	if !ok {
		t.Fatalf("file content shape mismatch: %#v", content[1])
	}
	files, ok := filePart["files"].([]interface{})
	if !ok || len(files) != 1 {
		t.Fatalf("files mismatch: %#v", filePart["files"])
	}
	file, ok := files[0].(map[string]interface{})
	if !ok {
		t.Fatalf("file shape mismatch: %#v", files[0])
	}
	if got := file["id"]; got != wantFileID {
		t.Fatalf("file id mismatch: got %#v, want %q body %s", got, wantFileID, body)
	}
	return payload
}

func assertGigaChatResponsesStreamRequestBody(t *testing.T, request *http.Request) {
	t.Helper()

	if request.Method != http.MethodPost {
		t.Fatalf("method mismatch: got %s, want POST", request.Method)
	}
	if got := request.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content type mismatch: got %q", got)
	}
	if got := request.Header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("accept header mismatch: got %q", got)
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
	if got := payload["model"]; got != "GigaChat-2" {
		t.Fatalf("model mismatch: got %#v", got)
	}
	if got := payload["stream"]; got != true {
		t.Fatalf("stream mismatch: got %#v, want true; body=%s", got, body)
	}
	messages, ok := payload["messages"].([]interface{})
	if !ok || len(messages) != 1 {
		t.Fatalf("messages mismatch: %#v", payload["messages"])
	}
}

func collectGigaChatResponsesStreamResponses(t *testing.T, chunks []*schemas.BifrostStreamChunk) []*schemas.BifrostResponsesStreamResponse {
	t.Helper()

	responses := make([]*schemas.BifrostResponsesStreamResponse, 0, len(chunks))
	for _, chunk := range chunks {
		if chunk == nil {
			t.Fatal("got nil stream chunk")
		}
		if chunk.BifrostError != nil {
			t.Fatalf("unexpected stream error: %v", chunk.BifrostError)
		}
		if chunk.BifrostResponsesStreamResponse == nil {
			t.Fatalf("missing responses stream response: %#v", chunk)
		}
		if chunk.BifrostResponsesStreamResponse.ExtraFields.Provider != schemas.GigaChat {
			t.Fatalf("provider mismatch: got %q, want %q", chunk.BifrostResponsesStreamResponse.ExtraFields.Provider, schemas.GigaChat)
		}
		if chunk.BifrostResponsesStreamResponse.Response != nil &&
			chunk.BifrostResponsesStreamResponse.Response.ExtraFields.Provider != schemas.GigaChat {
			t.Fatalf("nested response provider mismatch: got %q, want %q", chunk.BifrostResponsesStreamResponse.Response.ExtraFields.Provider, schemas.GigaChat)
		}
		responses = append(responses, chunk.BifrostResponsesStreamResponse)
	}
	return responses
}

func assertGigaChatResponsesStreamTypes(t *testing.T, responses []*schemas.BifrostResponsesStreamResponse, want []schemas.ResponsesStreamResponseType) {
	t.Helper()

	if len(responses) != len(want) {
		gotTypes := make([]schemas.ResponsesStreamResponseType, 0, len(responses))
		for _, response := range responses {
			gotTypes = append(gotTypes, response.Type)
		}
		t.Fatalf("response type count mismatch: got %d %v, want %d %v", len(responses), gotTypes, len(want), want)
	}
	for index, wantType := range want {
		if responses[index].Type != wantType {
			t.Fatalf("response type[%d] mismatch: got %q, want %q", index, responses[index].Type, wantType)
		}
	}
}

func mustGigaChatToolParameters(t *testing.T, raw string) *schemas.ToolFunctionParameters {
	t.Helper()

	var parameters schemas.ToolFunctionParameters
	if err := json.Unmarshal([]byte(raw), &parameters); err != nil {
		t.Fatalf("failed to unmarshal tool parameters: %v", err)
	}
	return &parameters
}
