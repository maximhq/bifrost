package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	bfws "github.com/maximhq/bifrost/transports/bifrost-http/websocket"
)

func TestShouldAccumulateRealtimeOutput(t *testing.T) {
	provider := &openai.OpenAIProvider{}
	if !provider.ShouldAccumulateRealtimeOutput(schemas.RTEventResponseTextDelta) {
		t.Fatal("expected response.text.delta to accumulate output text")
	}
	if !provider.ShouldAccumulateRealtimeOutput(schemas.RTEventResponseAudioTransDelta) {
		t.Fatal("expected response.audio_transcript.delta to accumulate output transcript")
	}
	if provider.ShouldAccumulateRealtimeOutput(schemas.RTEventInputAudioTransDelta) {
		t.Fatal("did not expect input audio transcription delta to accumulate assistant output")
	}
}

func TestExtractRealtimeTurnSummary(t *testing.T) {
	event := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemCreate,
		Item: &schemas.RealtimeItem{
			Content: []byte(`[{"type":"input_text","text":"hello from realtime"}]`),
		},
	}

	got := extractRealtimeTurnSummary(event, "")
	if got != "hello from realtime" {
		t.Fatalf("extractRealtimeTurnSummary() = %q, want %q", got, "hello from realtime")
	}
}

func TestFinalizedRealtimeInputSummary(t *testing.T) {
	userCreate := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemCreate,
		Item: &schemas.RealtimeItem{
			Role:    "user",
			Content: []byte(`[{"type":"input_text","text":"hello from browser"}]`),
		},
	}
	if got := finalizedRealtimeInputSummary(userCreate); got != "hello from browser" {
		t.Fatalf("finalizedRealtimeInputSummary(user create) = %q, want %q", got, "hello from browser")
	}

	userRetrieved := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemRetrieved,
		Item: &schemas.RealtimeItem{
			Role:    "user",
			Content: []byte(`[{"type":"input_text","text":"hello from retrieved item"}]`),
		},
	}
	if got := finalizedRealtimeInputSummary(userRetrieved); got != "hello from retrieved item" {
		t.Fatalf("finalizedRealtimeInputSummary(user retrieved) = %q, want %q", got, "hello from retrieved item")
	}

	userCreated := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemCreated,
		Item: &schemas.RealtimeItem{
			Role:    "user",
			Content: []byte(`[{"type":"input_text","text":"hello from provider created item"}]`),
		},
	}
	if got := finalizedRealtimeInputSummary(userCreated); got != "hello from provider created item" {
		t.Fatalf("finalizedRealtimeInputSummary(user created) = %q, want %q", got, "hello from provider created item")
	}

	userAdded := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemAdded,
		Item: &schemas.RealtimeItem{
			Role:    "user",
			Content: []byte(`[{"type":"input_text","text":"hello from provider added item"}]`),
		},
	}
	if got := finalizedRealtimeInputSummary(userAdded); got != "hello from provider added item" {
		t.Fatalf("finalizedRealtimeInputSummary(user added) = %q, want %q", got, "hello from provider added item")
	}

	userCreatedWithoutTranscript := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemCreated,
		Item: &schemas.RealtimeItem{
			Role:    "user",
			Type:    "message",
			Content: []byte(`[{"type":"input_audio","audio":null,"transcript":null}]`),
		},
		RawData: []byte(`{"type":"conversation.item.created","item":{"type":"message","role":"user","content":[{"type":"input_audio","audio":null,"transcript":null}]}}`),
	}
	if got := finalizedRealtimeInputSummary(userCreatedWithoutTranscript); got != "" {
		t.Fatalf("finalizedRealtimeInputSummary(user created without transcript) = %q, want empty", got)
	}

	userDoneWithoutTranscript := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemDone,
		Item: &schemas.RealtimeItem{
			Role:    "user",
			Type:    "message",
			Status:  "completed",
			Content: []byte(`[{"type":"input_audio","audio":null,"transcript":null}]`),
		},
		RawData: []byte(`{"type":"conversation.item.done","item":{"type":"message","role":"user","status":"completed","content":[{"type":"input_audio","audio":null,"transcript":null}]}}`),
	}
	if got := finalizedRealtimeInputSummary(userDoneWithoutTranscript); got != realtimeMissingTranscriptText {
		t.Fatalf("finalizedRealtimeInputSummary(user done without transcript) = %q, want %q", got, realtimeMissingTranscriptText)
	}

	inputTranscript := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventInputAudioTransCompleted,
		ExtraParams: map[string]json.RawMessage{
			"transcript": json.RawMessage(`"spoken user turn"`),
		},
	}
	if got := finalizedRealtimeInputSummary(inputTranscript); got != "spoken user turn" {
		t.Fatalf("finalizedRealtimeInputSummary(input transcript) = %q, want %q", got, "spoken user turn")
	}

	emptyInputTranscript := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventInputAudioTransCompleted,
		ExtraParams: map[string]json.RawMessage{
			"transcript": json.RawMessage(`""`),
		},
		RawData: []byte(`{"type":"conversation.item.input_audio_transcription.completed","transcript":"","usage":{"total_tokens":11}}`),
	}
	if got := finalizedRealtimeInputSummary(emptyInputTranscript); got != realtimeMissingTranscriptText {
		t.Fatalf("finalizedRealtimeInputSummary(empty input transcript) = %q, want %q", got, realtimeMissingTranscriptText)
	}

	missingInputTranscript := &schemas.BifrostRealtimeEvent{
		Type:    schemas.RTEventInputAudioTransCompleted,
		RawData: []byte(`{"type":"conversation.item.input_audio_transcription.completed","usage":{"total_tokens":11}}`),
	}
	if got := finalizedRealtimeInputSummary(missingInputTranscript); got != realtimeMissingTranscriptText {
		t.Fatalf("finalizedRealtimeInputSummary(missing input transcript) = %q, want %q", got, realtimeMissingTranscriptText)
	}

	assistantCreate := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemCreate,
		Item: &schemas.RealtimeItem{
			Role:    "assistant",
			Content: []byte(`[{"type":"text","text":"assistant text"}]`),
		},
	}
	if got := finalizedRealtimeInputSummary(assistantCreate); got != "" {
		t.Fatalf("finalizedRealtimeInputSummary(assistant create) = %q, want empty", got)
	}
}

func TestFinalizedRealtimeToolOutputSummary(t *testing.T) {
	event := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemCreate,
		Item: &schemas.RealtimeItem{
			Type:   "function_call_output",
			Output: `{"nextResponse":"tool result"}`,
		},
	}
	if got := finalizedRealtimeToolOutputSummary(event); got != `{"nextResponse":"tool result"}` {
		t.Fatalf("finalizedRealtimeToolOutputSummary() = %q, want %q", got, `{"nextResponse":"tool result"}`)
	}

	retrieved := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemRetrieved,
		Item: &schemas.RealtimeItem{
			Type:   "function_call_output",
			Output: `{"nextResponse":"tool result from retrieved"}`,
		},
	}
	if got := finalizedRealtimeToolOutputSummary(retrieved); got != `{"nextResponse":"tool result from retrieved"}` {
		t.Fatalf("finalizedRealtimeToolOutputSummary(retrieved) = %q, want %q", got, `{"nextResponse":"tool result from retrieved"}`)
	}

	created := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemCreated,
		Item: &schemas.RealtimeItem{
			Type:   "function_call_output",
			Output: `{"nextResponse":"tool result from created"}`,
		},
	}
	if got := finalizedRealtimeToolOutputSummary(created); got != `{"nextResponse":"tool result from created"}` {
		t.Fatalf("finalizedRealtimeToolOutputSummary(created) = %q, want %q", got, `{"nextResponse":"tool result from created"}`)
	}

	added := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemAdded,
		Item: &schemas.RealtimeItem{
			Type:   "function_call_output",
			Output: `{"nextResponse":"tool result from added"}`,
		},
	}
	if got := finalizedRealtimeToolOutputSummary(added); got != `{"nextResponse":"tool result from added"}` {
		t.Fatalf("finalizedRealtimeToolOutputSummary(added) = %q, want %q", got, `{"nextResponse":"tool result from added"}`)
	}
}

func TestPendingRealtimeInputUpdate(t *testing.T) {
	t.Parallel()

	transcriptEvent := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventInputAudioTransCompleted,
		ExtraParams: map[string]json.RawMessage{
			"item_id":    json.RawMessage(`"item_123"`),
			"transcript": json.RawMessage(`"Hello."`),
		},
	}
	itemID, summary := pendingRealtimeInputUpdate(transcriptEvent)
	if itemID != "item_123" || summary != "Hello." {
		t.Fatalf("pendingRealtimeInputUpdate(transcript) = (%q, %q), want (%q, %q)", itemID, summary, "item_123", "Hello.")
	}

	retrievedEvent := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemRetrieved,
		Item: &schemas.RealtimeItem{
			ID:      "item_123",
			Role:    "user",
			Content: []byte(`[{"type":"input_text","text":"historical hello"}]`),
		},
	}
	itemID, summary = pendingRealtimeInputUpdate(retrievedEvent)
	if itemID != "" || summary != "" {
		t.Fatalf("pendingRealtimeInputUpdate(retrieved) = (%q, %q), want empty", itemID, summary)
	}
}

func TestPendingRealtimeToolOutputUpdate(t *testing.T) {
	t.Parallel()

	toolOutputEvent := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemDone,
		Item: &schemas.RealtimeItem{
			ID:     "item_tool_123",
			Type:   "function_call_output",
			Output: `{"nextResponse":"tool result"}`,
		},
	}
	itemID, summary := pendingRealtimeToolOutputUpdate(toolOutputEvent)
	if itemID != "item_tool_123" || summary != `{"nextResponse":"tool result"}` {
		t.Fatalf("pendingRealtimeToolOutputUpdate(done) = (%q, %q), want (%q, %q)", itemID, summary, "item_tool_123", `{"nextResponse":"tool result"}`)
	}

	retrievedToolOutputEvent := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemRetrieved,
		Item: &schemas.RealtimeItem{
			ID:     "item_tool_123",
			Type:   "function_call_output",
			Output: `{"nextResponse":"historical tool result"}`,
		},
	}
	itemID, summary = pendingRealtimeToolOutputUpdate(retrievedToolOutputEvent)
	if itemID != "" || summary != "" {
		t.Fatalf("pendingRealtimeToolOutputUpdate(retrieved) = (%q, %q), want empty", itemID, summary)
	}
}

func TestBuildRealtimeTurnPostResponseUsesFullResponseDonePayload(t *testing.T) {
	rawRequest := `{"type":"conversation.item.input_audio_transcription.completed","transcript":""}`
	rawResponse := []byte(`{
		"type":"response.done",
		"response":{
			"output":[
				{
					"id":"item_message_123",
					"type":"message",
					"content":[
						{
							"type":"audio",
							"transcript":"assistant turn text"
						}
					]
				}
			],
			"usage":{
				"total_tokens":26,
				"input_tokens":17,
				"output_tokens":9,
				"input_token_details":{
					"text_tokens":12,
					"audio_tokens":5,
					"image_tokens":0,
					"cached_tokens":4
				},
				"output_token_details":{
					"text_tokens":7,
					"audio_tokens":2
				}
			}
		}
	}`)

	resp := buildRealtimeTurnPostResponse(&openai.OpenAIProvider{}, schemas.OpenAI, "gpt-4o-realtime-preview-2025-06-03", rawRequest, rawResponse, "", 4321)
	if resp == nil || resp.ResponsesResponse == nil {
		t.Fatal("expected realtime post response to be built")
	}
	if resp.ResponsesResponse.ExtraFields.Latency != 4321 {
		t.Fatalf("Latency = %d, want %d", resp.ResponsesResponse.ExtraFields.Latency, 4321)
	}
	if resp.ResponsesResponse.Usage == nil || resp.ResponsesResponse.Usage.InputTokens != 17 || resp.ResponsesResponse.Usage.OutputTokens != 9 || resp.ResponsesResponse.Usage.TotalTokens != 26 {
		t.Fatalf("Usage = %+v, want input=17 output=9 total=26", resp.ResponsesResponse.Usage)
	}
	if len(resp.ResponsesResponse.Output) != 1 {
		t.Fatalf("len(Output) = %d, want 1", len(resp.ResponsesResponse.Output))
	}
	if resp.ResponsesResponse.Output[0].Content == nil || resp.ResponsesResponse.Output[0].Content.ContentStr == nil || *resp.ResponsesResponse.Output[0].Content.ContentStr != "assistant turn text" {
		t.Fatalf("Output[0].Content = %+v, want assistant turn text", resp.ResponsesResponse.Output[0].Content)
	}
	if got, ok := resp.ResponsesResponse.ExtraFields.RawRequest.(string); !ok || got != rawRequest {
		t.Fatalf("RawRequest = %#v, want %q", resp.ResponsesResponse.ExtraFields.RawRequest, rawRequest)
	}
	if got, ok := resp.ResponsesResponse.ExtraFields.RawResponse.(string); !ok || got == "" {
		t.Fatalf("RawResponse = %#v, want raw response string", resp.ResponsesResponse.ExtraFields.RawResponse)
	}
}

func TestFinalizeRealtimeTurnHooksWithErrorCompletesActiveHooks(t *testing.T) {
	t.Parallel()

	session := bfws.NewSession(nil)
	session.SetProviderSessionID("sess_provider_123")
	session.AddRealtimeInput("hello from user", `{"type":"conversation.item.added"}`)
	session.AppendRealtimeOutputText("partial assistant output")

	var (
		capturedResp *schemas.BifrostResponse
		capturedErr  *schemas.BifrostError
		cleanedUp    bool
	)
	session.SetRealtimeTurnHooks(&bfws.RealtimeTurnPluginState{
		RequestID: "req_realtime_123",
		StartedAt: time.Now().Add(-time.Second),
		PreHookValues: map[any]any{
			schemas.BifrostContextKeyGovernanceVirtualKeyID: "vk_123",
		},
		PostHookRunner: func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
			capturedResp = result
			capturedErr = err
			return result, nil
		},
		Cleanup: func() {
			cleanedUp = true
		},
	})

	rawResponse := []byte(`{"type":"error","error":{"type":"server_error","message":"Virtual key is required."}}`)
	postErr := finalizeRealtimeTurnHooksWithError(
		nil,
		nil,
		session,
		schemas.OpenAI,
		"gpt-realtime",
		nil,
		schemas.RTEventError,
		rawResponse,
		newRealtimeWireBifrostError(401, "server_error", "Virtual key is required."),
	)
	if postErr != nil {
		t.Fatalf("finalizeRealtimeTurnHooksWithError() post error = %v, want nil", postErr)
	}
	if capturedResp != nil {
		t.Fatalf("captured response = %#v, want nil", capturedResp)
	}
	if capturedErr == nil {
		t.Fatal("expected captured error")
	}
	if capturedErr.ExtraFields.RequestType != schemas.RealtimeRequest {
		t.Fatalf("request type = %q, want %q", capturedErr.ExtraFields.RequestType, schemas.RealtimeRequest)
	}
	if capturedErr.ExtraFields.Provider != schemas.OpenAI {
		t.Fatalf("provider = %q, want %q", capturedErr.ExtraFields.Provider, schemas.OpenAI)
	}
	if capturedErr.ExtraFields.OriginalModelRequested != "gpt-realtime" {
		t.Fatalf("model requested = %q, want %q", capturedErr.ExtraFields.OriginalModelRequested, "gpt-realtime")
	}
	rawRequest, ok := capturedErr.ExtraFields.RawRequest.(string)
	if !ok || rawRequest == "" {
		t.Fatalf("raw request = %#v, want non-empty string", capturedErr.ExtraFields.RawRequest)
	}
	rawResp, ok := capturedErr.ExtraFields.RawResponse.(json.RawMessage)
	if !ok || string(rawResp) != string(rawResponse) {
		t.Fatalf("raw response = %#v, want %s", capturedErr.ExtraFields.RawResponse, string(rawResponse))
	}
	if session.PeekRealtimeTurnHooks() != nil {
		t.Fatal("expected active hooks to be cleared")
	}
	if got := session.ConsumeRealtimeTurnInputs(); len(got) != 0 {
		t.Fatalf("remaining turn inputs = %d, want 0", len(got))
	}
	if got := session.ConsumeRealtimeOutputText(); got != "" {
		t.Fatalf("remaining output text = %q, want empty", got)
	}
	if !cleanedUp {
		t.Fatal("expected realtime hook cleanup to run")
	}
}

func TestNewBifrostErrorFromRealtimeErrorCarriesRealtimeMetadata(t *testing.T) {
	t.Parallel()

	rawResponse := []byte(`{"type":"error","error":{"type":"invalid_request_error","code":"invalid_request_error","message":"bad request","param":"session.type"}}`)
	bifrostErr := newBifrostErrorFromRealtimeError(
		schemas.OpenAI,
		"gpt-realtime",
		rawResponse,
		&schemas.RealtimeError{
			Type:    "invalid_request_error",
			Code:    "invalid_request_error",
			Message: "bad request",
			Param:   "session.type",
		},
	)
	if bifrostErr == nil {
		t.Fatal("expected bifrost error")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 400 {
		t.Fatalf("status code = %#v, want 400", bifrostErr.StatusCode)
	}
	if bifrostErr.ExtraFields.RequestType != schemas.RealtimeRequest {
		t.Fatalf("request type = %q, want %q", bifrostErr.ExtraFields.RequestType, schemas.RealtimeRequest)
	}
	if bifrostErr.ExtraFields.Provider != schemas.OpenAI {
		t.Fatalf("provider = %q, want %q", bifrostErr.ExtraFields.Provider, schemas.OpenAI)
	}
	if bifrostErr.ExtraFields.OriginalModelRequested != "gpt-realtime" {
		t.Fatalf("model requested = %q, want %q", bifrostErr.ExtraFields.OriginalModelRequested, "gpt-realtime")
	}
	rawResp, ok := bifrostErr.ExtraFields.RawResponse.(json.RawMessage)
	if !ok || string(rawResp) != string(rawResponse) {
		t.Fatalf("raw response = %#v, want %s", bifrostErr.ExtraFields.RawResponse, string(rawResponse))
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Param != "session.type" {
		t.Fatalf("error param = %#v, want session.type", bifrostErr.Error)
	}
}
