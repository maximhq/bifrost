package streamusage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestMain enables every embedded vocabulary, as `usage_estimation_tokenizers:
// ["all"]` does, so family tests run against their own vocabulary.
// TestConfigureGatesFamilies covers the default (none enabled).
func TestMain(m *testing.M) {
	Configure([]string{FamilyAll})
	os.Exit(m.Run())
}

func newContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

// estimateFrom runs Estimate on an accumulated snapshot handed in directly; the
// real tracer's snapshot is covered by TestTracer_CancelledStreamEstimatedFromAccumulator.
func estimateFrom(ctx *schemas.BifrostContext, snapshot *schemas.BifrostResponse, request *schemas.BifrostRequest, model string, failure *schemas.BifrostError) {
	if idle, ok := estimable(failure); ok {
		estimate(ctx, snapshot, request, model, failure, idle)
	}
}

func chatSnapshot(text string, finish bool) *schemas.BifrostResponse {
	choice := schemas.BifrostResponseChoice{ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{Message: &schemas.ChatMessage{
		Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(text)},
	}}}
	if finish {
		choice.FinishReason = schemas.Ptr("stop")
	}
	return &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{Choices: []schemas.BifrostResponseChoice{choice}}}
}

func chatRequest(model string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{RequestType: schemas.ChatCompletionStreamRequest, ChatRequest: &schemas.BifrostChatRequest{Model: model, Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}}}}
}

func cancellation() *schemas.BifrostError {
	return &schemas.BifrostError{Error: &schemas.ErrorField{Type: schemas.Ptr(schemas.RequestCancelled)}}
}

func TestEstimateReconcilesReportedUsage(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reported  *schemas.BifrostLLMUsage
		handler   bool // streamed through the Anthropic handlers, which count usage events
		updates   int
		finished  bool
		want      int
		estimated bool
	}{
		{name: "missing usage", want: 2, estimated: true},
		{name: "explicit zero", reported: &schemas.BifrostLLMUsage{PromptTokens: 10}, want: 0},
		{name: "reported count", reported: &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 9, TotalTokens: 19}, want: 9},
		{name: "anthropic provisional", reported: &schemas.BifrostLLMUsage{PromptTokens: 26, CompletionTokens: 4, TotalTokens: 30}, handler: true, updates: 1, want: 2, estimated: true},
		{name: "anthropic later usage", reported: &schemas.BifrostLLMUsage{PromptTokens: 26, CompletionTokens: 1, TotalTokens: 27}, handler: true, updates: 2, want: 1},
		{name: "anthropic finish", reported: &schemas.BifrostLLMUsage{PromptTokens: 26, CompletionTokens: 1, TotalTokens: 27}, handler: true, updates: 1, finished: true, want: 1},
		// Other hosts of Claude stream through the same handlers.
		{name: "vertex provisional", reported: &schemas.BifrostLLMUsage{PromptTokens: 26, CompletionTokens: 4, TotalTokens: 30}, handler: true, updates: 1, want: 2, estimated: true},
		{name: "bedrock provisional", reported: &schemas.BifrostLLMUsage{PromptTokens: 26, CompletionTokens: 4, TotalTokens: 30}, handler: true, updates: 1, want: 2, estimated: true},
		{name: "vertex later usage", reported: &schemas.BifrostLLMUsage{PromptTokens: 26, CompletionTokens: 9, TotalTokens: 35}, handler: true, updates: 2, want: 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, accumulated := newContext(), chatSnapshot("hello world", tc.finished)
			if tc.handler {
				ctx.SetValue(schemas.BifrostContextKeyStreamUsageProvisional, tc.updates < 2) // only message_start reported
			}
			err := cancellation()
			err.ExtraFields.BilledUsage = tc.reported
			estimateFrom(ctx, accumulated, chatRequest("unknown"), "unknown", err)
			got := err.ExtraFields.BilledUsage
			if got == nil || got.CompletionTokens != tc.want {
				t.Fatalf("usage=%+v want completion=%d", got, tc.want)
			}
			if (got.Estimation != nil) != tc.estimated {
				t.Fatalf("estimation=%+v", got.Estimation)
			}
			if tc.reported != nil && got.PromptTokens != tc.reported.PromptTokens {
				t.Fatal("reported prompt changed")
			}
			if tc.estimated && got.TotalTokens != got.PromptTokens+got.CompletionTokens {
				t.Fatal("inconsistent total")
			}
		})
	}
}

// Usage the accumulator collected from chunks counts as reported, so a provider
// that streamed usage before the cancel is not re-estimated.
func TestEstimateKeepsAccumulatedUsage(t *testing.T) {
	snapshot := chatSnapshot("hello world", false)
	snapshot.ChatResponse.Usage = &schemas.BifrostLLMUsage{PromptTokens: 12, CompletionTokens: 8, TotalTokens: 20}
	err := cancellation()
	estimateFrom(newContext(), snapshot, chatRequest("unknown"), "unknown", err)
	if err.ExtraFields.BilledUsage != nil {
		t.Fatalf("re-estimated accumulated usage: %+v", err.ExtraFields.BilledUsage)
	}
}

func TestEstimateNeedsAccumulatedOutput(t *testing.T) {
	err := cancellation()
	estimateFrom(newContext(), nil, chatRequest("unknown"), "unknown", err)
	if err.ExtraFields.BilledUsage != nil {
		t.Fatal("billed a stream with no received output")
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline) // no tracer
	Estimate(ctx, chatRequest("unknown"), "unknown", err)
	if err.ExtraFields.BilledUsage != nil {
		t.Fatal("billed without an accumulator")
	}
}

func TestEstimateOnlyForCancellationAndTimeout(t *testing.T) {
	for kind, want := range map[string]bool{schemas.RequestCancelled: true, schemas.RequestTimedOut: true, "server_error": false} {
		err := &schemas.BifrostError{Error: &schemas.ErrorField{Type: schemas.Ptr(kind)}}
		estimateFrom(newContext(), chatSnapshot("hello world", false), chatRequest("unknown"), "unknown", err)
		if got := err.ExtraFields.BilledUsage != nil; got != want {
			t.Errorf("%s: estimated=%v want %v", kind, got, want)
		}
	}
}

func TestEstimateCountsReasoningAndTools(t *testing.T) {
	snapshot := chatSnapshot("hello world", false)
	snapshot.ChatResponse.Choices[0].Message.ChatAssistantMessage = &schemas.ChatAssistantMessage{
		Reasoning: schemas.Ptr("think carefully"),
		ToolCalls: []schemas.ChatAssistantMessageToolCall{{ID: schemas.Ptr("call_1"), Type: schemas.Ptr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("weather"), Arguments: `{"city":"Paris"}`}}},
	}
	err := cancellation()
	estimateFrom(newContext(), snapshot, chatRequest("unknown"), "unknown", err)
	c, _ := selectCounter("")
	out, _ := c.count("hello world" + toolJSON("call_1", "weather", "function", `{"city":"Paris"}`))
	rn, _ := c.count("think carefully")
	got := err.ExtraFields.BilledUsage
	if got == nil || got.CompletionTokens != out+rn || got.CompletionTokensDetails.ReasoningTokens != rn {
		t.Fatalf("usage=%+v", got)
	}
}

// Gemini streams thinking only as reasoning details; Anthropic and Cohere also
// fill Reasoning with the same text, which must not be counted twice.
func TestEstimateCountsReasoningDetails(t *testing.T) {
	c, _ := selectCounter("")
	want, _ := c.count("think carefully")
	details := []schemas.ChatReasoningDetails{
		{Index: 0, Type: schemas.BifrostReasoningDetailsTypeText, Text: schemas.Ptr("think carefully")},
		{Index: 1, Type: schemas.BifrostReasoningDetailsTypeEncrypted, Signature: schemas.Ptr("c2lnbmF0dXJl")},
	}
	for name, assistant := range map[string]*schemas.ChatAssistantMessage{
		"details only": {ReasoningDetails: details},
		"both fields":  {Reasoning: schemas.Ptr("think carefully"), ReasoningDetails: details},
	} {
		snapshot := chatSnapshot("", false)
		snapshot.ChatResponse.Choices[0].Message.ChatAssistantMessage = assistant
		err := cancellation()
		estimateFrom(newContext(), snapshot, chatRequest("unknown"), "unknown", err)
		got := err.ExtraFields.BilledUsage
		if got == nil || got.CompletionTokensDetails == nil || got.CompletionTokensDetails.ReasoningTokens != want || got.CompletionTokens != want {
			t.Errorf("%s: usage=%+v want %d reasoning tokens", name, got, want)
		}
	}
}

// Generated images arrive in streamed content as base64 data URLs; they are not
// text, so they must not be counted as completion tokens.
func TestEstimateSkipsImageDataURLs(t *testing.T) {
	image := "data:image/png;base64," + strings.Repeat("iVBORw0KGgoAAAANSUhEUgAA", 400)
	c, _ := selectCounter("")
	want, _ := c.count("hello world")
	err := cancellation()
	estimateFrom(newContext(), chatSnapshot("hello world"+image, false), chatRequest("unknown"), "unknown", err)
	if got := err.ExtraFields.BilledUsage; got == nil || got.CompletionTokens != want {
		t.Fatalf("usage=%+v want completion=%d", got, want)
	}
	// An image-only stream leaves completion usage unknown rather than zero-estimated.
	err = cancellation()
	estimateFrom(newContext(), chatSnapshot(image, false), chatRequest("unknown"), "unknown", err)
	if got := err.ExtraFields.BilledUsage; got == nil || got.CompletionTokens != 0 || got.Estimation == nil || got.Estimation.Completion {
		t.Fatalf("image-only usage=%+v want completion left unestimated", got)
	}
}

func TestEstimateTextCompletionArrayPrompt(t *testing.T) {
	c, _ := selectCounter("")
	a, _ := c.count("hello world")
	b, _ := c.count("think carefully")
	request := &schemas.BifrostRequest{RequestType: schemas.TextCompletionStreamRequest, TextCompletionRequest: &schemas.BifrostTextCompletionRequest{
		Model: "unknown", Input: &schemas.TextCompletionInput{PromptArray: []string{"hello world", "think carefully"}},
	}}
	err := cancellation()
	estimateFrom(newContext(), chatSnapshot("hello world", false), request, "unknown", err)
	if got := err.ExtraFields.BilledUsage; got == nil || got.PromptTokens != a+b || got.Estimation == nil || !got.Estimation.Prompt {
		t.Fatalf("usage=%+v want prompt=%d", got, a+b)
	}
}

// Tool definitions and a JSON-schema response format are rendered into the
// prompt and billed as input, so they count towards the prompt estimate.
func TestEstimateCountsToolsAndResponseFormat(t *testing.T) {
	c, _ := selectCounter("")
	base := chatRequest("unknown")
	err := cancellation()
	estimateFrom(newContext(), chatSnapshot("hello world", false), base, "unknown", err)
	plain := err.ExtraFields.BilledUsage.PromptTokens

	fn := &schemas.ChatToolFunction{Name: "get_weather", Description: schemas.Ptr(strings.Repeat("Look up the current weather for a city. ", 20)),
		Parameters: &schemas.ToolFunctionParameters{Type: "object", Required: []string{"city"}}}
	schema := map[string]interface{}{"name": "forecast", "schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"summary": map[string]interface{}{"type": "string"}}}}
	var format interface{} = map[string]interface{}{"type": "json_schema", "json_schema": schema}
	withTools := chatRequest("unknown")
	withTools.ChatRequest.Params = &schemas.ChatParameters{Tools: []schemas.ChatTool{{Type: schemas.ChatToolTypeFunction, Function: fn}}, ResponseFormat: &format}
	fnJSON, _ := providerUtils.MarshalSorted(fn)
	schemaJSON, _ := providerUtils.MarshalSorted(schema)
	toolTokens, _ := c.count(string(fnJSON))
	schemaTokens, _ := c.count(string(schemaJSON))

	err = cancellation()
	estimateFrom(newContext(), chatSnapshot("hello world", false), withTools, "unknown", err)
	if got := err.ExtraFields.BilledUsage; got == nil || got.PromptTokens != plain+toolTokens+schemaTokens {
		t.Fatalf("usage=%+v want prompt=%d (+%d tool, +%d schema)", got, plain+toolTokens+schemaTokens, toolTokens, schemaTokens)
	}

	// Responses requests reach the same count through their chat conversion.
	responses := &schemas.BifrostRequest{RequestType: schemas.ResponsesStreamRequest, ResponsesRequest: withTools.ChatRequest.ToResponsesRequest()}
	err = cancellation()
	estimateFrom(newContext(), chatSnapshot("hello world", false), responses, "unknown", err)
	if got := err.ExtraFields.BilledUsage; got == nil || got.PromptTokens < plain+toolTokens {
		t.Fatalf("responses usage=%+v want prompt >= %d", got, plain+toolTokens)
	}
}

// A stream that goes silent mid-output fails with an untyped error wrapping
// ErrStreamIdleTimeout, and no billed usage is attached on that path.
func TestEstimateIdleTimeout(t *testing.T) {
	idle := func() *schemas.BifrostError {
		return &schemas.BifrostError{Error: &schemas.ErrorField{Message: "Error reading stream", Error: fmt.Errorf("read: %w", providerUtils.ErrStreamIdleTimeout)}}
	}
	err := idle()
	estimateFrom(newContext(), chatSnapshot("hello world", false), chatRequest("unknown"), "unknown", err)
	if got := err.ExtraFields.BilledUsage; got == nil || got.PromptTokens != 8 || got.CompletionTokens != 2 || got.Estimation == nil {
		t.Fatalf("idle timeout usage=%+v want prompt=8 completion=2 estimated", got)
	}

	// Usage the provider already streamed is kept; only the provisional output is replaced.
	ctx, accumulated := newContext(), chatSnapshot("hello world", false)
	ctx.SetValue(schemas.BifrostContextKeyStreamAccumulatedUsage, &schemas.BifrostLLMUsage{PromptTokens: 26, CompletionTokens: 4, TotalTokens: 30})
	ctx.SetValue(schemas.BifrostContextKeyStreamUsageProvisional, true)
	err = idle()
	estimateFrom(ctx, accumulated, chatRequest("unknown"), "unknown", err)
	if got := err.ExtraFields.BilledUsage; got == nil || got.PromptTokens != 26 || got.CompletionTokens != 2 || got.Estimation == nil || got.Estimation.Prompt {
		t.Fatalf("idle timeout with provider usage=%+v want prompt=26 kept, completion=2 estimated", got)
	}

	// Authoritative usage (a message_delta arrived) needs no estimate, but it is
	// still billed: the idle-timeout path attaches nothing of its own. A copy is
	// attached, with or without an accumulated snapshot.
	for name, snapshot := range map[string]*schemas.BifrostResponse{"snapshot": chatSnapshot("hello world", false), "no snapshot": nil} {
		ctx := newContext()
		handle := &schemas.BifrostLLMUsage{PromptTokens: 26, CompletionTokens: 4, TotalTokens: 30}
		ctx.SetValue(schemas.BifrostContextKeyStreamAccumulatedUsage, handle)
		ctx.SetValue(schemas.BifrostContextKeyStreamUsageProvisional, false)
		err = idle()
		estimateFrom(ctx, snapshot, chatRequest("unknown"), "unknown", err)
		if got := err.ExtraFields.BilledUsage; got == nil || got == handle || got.PromptTokens != 26 || got.CompletionTokens != 4 || got.TotalTokens != 30 || got.Estimation != nil {
			t.Errorf("%s: idle timeout after message_delta usage=%+v want a copy of prompt=26 completion=4 total=30, not estimated", name, got)
		}
	}

	// Other untyped stream errors are not interruptions.
	err = &schemas.BifrostError{Error: &schemas.ErrorField{Message: "Error reading stream", Error: fmt.Errorf("unexpected EOF")}}
	estimateFrom(newContext(), chatSnapshot("hello world", false), chatRequest("unknown"), "unknown", err)
	if err.ExtraFields.BilledUsage != nil {
		t.Fatalf("estimated a non-interruption error: %+v", err.ExtraFields.BilledUsage)
	}
}

func TestEstimateCountsRefusal(t *testing.T) {
	snapshot := chatSnapshot("", false)
	snapshot.ChatResponse.Choices[0].Message.ChatAssistantMessage = &schemas.ChatAssistantMessage{Refusal: schemas.Ptr("I can't help with that request.")}
	c, _ := selectCounter("")
	want, _ := c.count("I can't help with that request.")
	err := cancellation()
	estimateFrom(newContext(), snapshot, chatRequest("unknown"), "unknown", err)
	if got := err.ExtraFields.BilledUsage; got == nil || got.CompletionTokens != want || got.Estimation == nil || !got.Estimation.Completion {
		t.Fatalf("usage=%+v want completion=%d", got, want)
	}
}

// Responses custom and namespace tools do not survive the chat conversion, so
// their definitions are counted from the original request.
func TestEstimateCountsResponsesTools(t *testing.T) {
	c, _ := selectCounter("")
	responsesRequest := func(tools []schemas.ResponsesTool) *schemas.BifrostRequest {
		rr := chatRequest("unknown").ChatRequest.ToResponsesRequest()
		rr.Params = &schemas.ResponsesParameters{Tools: tools}
		return &schemas.BifrostRequest{RequestType: schemas.ResponsesStreamRequest, ResponsesRequest: rr}
	}
	err := cancellation()
	estimateFrom(newContext(), chatSnapshot("hello world", false), responsesRequest(nil), "unknown", err)
	plain := err.ExtraFields.BilledUsage.PromptTokens

	long := strings.Repeat("Read a SQL query in the documented dialect. ", 15)
	function := schemas.ResponsesTool{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("get_weather"), Description: schemas.Ptr(long),
		ResponsesToolFunction: &schemas.ResponsesToolFunction{Parameters: &schemas.ToolFunctionParameters{Type: "object"}}}
	custom := schemas.ResponsesTool{Type: schemas.ResponsesToolTypeCustom, Name: schemas.Ptr("run_sql"), Description: schemas.Ptr(long),
		ResponsesToolCustom: &schemas.ResponsesToolCustom{Format: &schemas.ResponsesToolCustomFormat{Type: "grammar", Syntax: schemas.Ptr("lark"), Definition: schemas.Ptr(strings.Repeat("start: select_stmt\n", 30))}}}
	namespace := schemas.ResponsesTool{Type: schemas.ResponsesToolTypeNamespace, Name: schemas.Ptr("crm"), Description: schemas.Ptr(long),
		ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{function}}}
	webSearch := schemas.ResponsesTool{Type: schemas.ResponsesToolTypeWebSearch}
	want := plain
	for _, tool := range []schemas.ResponsesTool{function, custom, namespace} {
		encoded, _ := providerUtils.MarshalSorted(tool)
		n, _ := c.count(string(encoded))
		want += n
	}
	err = cancellation()
	estimateFrom(newContext(), chatSnapshot("hello world", false), responsesRequest([]schemas.ResponsesTool{function, custom, namespace, webSearch}), "unknown", err)
	if got := err.ExtraFields.BilledUsage; got == nil || got.PromptTokens != want {
		t.Fatalf("usage=%+v want prompt=%d (plain %d)", got, want, plain)
	}
}

// A Responses stream cancelled while thinking holds only reasoning items, which
// the chat conversion defers until a following message; they are still counted.
func TestEstimateResponsesReasoningOnly(t *testing.T) {
	c, _ := selectCounter("")
	think, _ := c.count("think carefully")
	answer, _ := c.count("hello world")
	call, _ := c.count(toolJSON("call_1", "weather", "function", `{"city":"Paris"}`))
	refusal, _ := c.count("I can't help with that request.")
	reasoning := func(item schemas.ResponsesMessage) schemas.ResponsesMessage {
		item.Type = schemas.Ptr(schemas.ResponsesMessageTypeReasoning)
		return item
	}
	message := schemas.ResponsesMessage{Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
		Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{Type: schemas.ResponsesOutputMessageContentTypeText, Text: schemas.Ptr("hello world")}}}}
	for name, tc := range map[string]struct {
		output              []schemas.ResponsesMessage
		completion, thought int
	}{
		"summary": {output: []schemas.ResponsesMessage{reasoning(schemas.ResponsesMessage{ResponsesReasoning: &schemas.ResponsesReasoning{
			Summary: []schemas.ResponsesReasoningSummary{{Type: schemas.ResponsesReasoningContentBlockTypeSummaryText, Text: "think carefully"}}}})}, completion: think, thought: think},
		"reasoning text": {output: []schemas.ResponsesMessage{reasoning(schemas.ResponsesMessage{ResponsesReasoning: &schemas.ResponsesReasoning{},
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{Type: schemas.ResponsesOutputMessageContentTypeReasoning, Text: schemas.Ptr("think carefully")}}}})}, completion: think, thought: think},
		"reasoning then answer": {output: []schemas.ResponsesMessage{reasoning(schemas.ResponsesMessage{ResponsesReasoning: &schemas.ResponsesReasoning{
			Summary: []schemas.ResponsesReasoningSummary{{Type: schemas.ResponsesReasoningContentBlockTypeSummaryText, Text: "think carefully"}}}}), message}, completion: think + answer, thought: think},
		"function call": {output: []schemas.ResponsesMessage{{Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: schemas.Ptr("call_1"), Name: schemas.Ptr("weather"), Arguments: schemas.Ptr(`{"city":"Paris"}`)}}}, completion: call},
		"refusal": {output: []schemas.ResponsesMessage{{Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
			{Type: schemas.ResponsesOutputMessageContentTypeRefusal, ResponsesOutputMessageContentRefusal: &schemas.ResponsesOutputMessageContentRefusal{Refusal: "I can't help with that request."}}}}}}, completion: refusal},
	} {
		snapshot := &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{Output: tc.output}}
		err := cancellation()
		estimateFrom(newContext(), snapshot, chatRequest("unknown"), "unknown", err)
		got := err.ExtraFields.BilledUsage
		reasoningTokens := 0
		if got != nil && got.CompletionTokensDetails != nil {
			reasoningTokens = got.CompletionTokensDetails.ReasoningTokens
		}
		if got == nil || got.CompletionTokens != tc.completion || reasoningTokens != tc.thought {
			t.Errorf("%s: usage=%+v want completion=%d reasoning=%d", name, got, tc.completion, tc.thought)
		}
	}
}

// Real HTTP requests keep response_format as raw JSON (to preserve schema key
// order), so the schema must be counted from that form too.
func TestEstimateCountsRawResponseFormatSchema(t *testing.T) {
	c, _ := selectCounter("")
	err := cancellation()
	estimateFrom(newContext(), chatSnapshot("hello world", false), chatRequest("unknown"), "unknown", err)
	plain := err.ExtraFields.BilledUsage.PromptTokens

	schema := `{"name":"forecast","schema":{"type":"object","properties":{"summary":{"type":"string","description":"A one-paragraph weather summary for the requested city and day."}},"required":["summary"]}}`
	var params schemas.ChatParameters
	if e := json.Unmarshal([]byte(`{"response_format":{"type":"json_schema","json_schema":`+schema+`}}`), &params); e != nil {
		t.Fatal(e)
	}
	if _, raw := (*params.ResponseFormat).(json.RawMessage); !raw {
		t.Fatalf("fixture did not decode to the raw form: %T", *params.ResponseFormat)
	}
	request := chatRequest("unknown")
	request.ChatRequest.Params = &params
	schemaTokens, _ := c.count(schema)
	err = cancellation()
	estimateFrom(newContext(), chatSnapshot("hello world", false), request, "unknown", err)
	if got := err.ExtraFields.BilledUsage; got == nil || got.PromptTokens != plain+schemaTokens {
		t.Fatalf("usage=%+v want prompt=%d (+%d schema)", got, plain+schemaTokens, schemaTokens)
	}
}

// A custom tool call's input is model output the accumulator keeps.
func TestEstimateCountsCustomToolCall(t *testing.T) {
	c, _ := selectCounter("")
	want, _ := c.count("run_sql" + "SELECT city, temperature FROM forecasts WHERE day = 'today'")
	snapshot := &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{Output: []schemas.ResponsesMessage{{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeCustomToolCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: schemas.Ptr("call_1"), Name: schemas.Ptr("run_sql"),
			ResponsesCustomToolCall: &schemas.ResponsesCustomToolCall{Input: "SELECT city, temperature FROM forecasts WHERE day = 'today'"}},
	}}}}
	err := cancellation()
	estimateFrom(newContext(), snapshot, chatRequest("unknown"), "unknown", err)
	if got := err.ExtraFields.BilledUsage; got == nil || got.CompletionTokens != want || got.Estimation == nil || !got.Estimation.Completion {
		t.Fatalf("usage=%+v want completion=%d", got, want)
	}
}

// Custom tool history does not survive the chat conversion, so a cancelled
// Responses request counts each call's input and each result's output from the
// original items.
func TestEstimateCountsResponsesCustomToolHistory(t *testing.T) {
	c, _ := selectCounter("")
	const input = "SELECT city, temperature FROM forecasts WHERE day = 'today'"
	const output = "Paris 18C, Lisbon 22C, Oslo 9C"
	history := func(input string, output *schemas.ResponsesToolMessageOutputStruct) *schemas.BifrostRequest {
		rr := chatRequest("unknown").ChatRequest.ToResponsesRequest()
		rr.Input = append(rr.Input,
			schemas.ResponsesMessage{Type: new(schemas.ResponsesMessageTypeCustomToolCall), ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: new("call_1"), Name: new("run_sql"), ResponsesCustomToolCall: &schemas.ResponsesCustomToolCall{Input: input}}},
			schemas.ResponsesMessage{Type: new(schemas.ResponsesMessageTypeCustomToolCallOutput), ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: new("call_1"), Output: output}},
		)
		return &schemas.BifrostRequest{RequestType: schemas.ResponsesStreamRequest, ResponsesRequest: rr}
	}
	prompt := func(request *schemas.BifrostRequest) int {
		err := cancellation()
		estimateFrom(newContext(), chatSnapshot("hello world", false), request, "unknown", err)
		if err.ExtraFields.BilledUsage == nil {
			t.Fatal("no usage estimated")
		}
		return err.ExtraFields.BilledUsage.PromptTokens
	}
	inputTokens, _ := c.count(input)
	outputTokens, _ := c.count(output)
	empty := prompt(history("", &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: new("")}))

	asString := prompt(history(input, &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: new(output)}))
	if got, want := asString-empty, inputTokens+outputTokens; got != want {
		t.Fatalf("custom tool history added %d prompt tokens, want %d (input %d + output %d)", got, want, inputTokens, outputTokens)
	}
	asBlocks := prompt(history(input, &schemas.ResponsesToolMessageOutputStruct{ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{
		{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: new(output)},
	}}))
	if got, want := asBlocks-empty, inputTokens+outputTokens; got != want {
		t.Fatalf("custom tool history with block output added %d prompt tokens, want %d", got, want)
	}
}

func TestEstimateResponsesSnapshot(t *testing.T) {
	snapshot := &schemas.BifrostResponse{ResponsesResponse: chatSnapshot("hello world", false).ChatResponse.ToBifrostResponsesResponse()}
	request := &schemas.BifrostRequest{RequestType: schemas.ResponsesStreamRequest, ResponsesRequest: chatRequest("unknown").ChatRequest.ToResponsesRequest()}
	err := cancellation()
	estimateFrom(newContext(), snapshot, request, "unknown", err)
	if got := err.ExtraFields.BilledUsage; got == nil || got.CompletionTokens != 2 || got.PromptTokens != 8 {
		t.Fatalf("usage=%+v", got)
	}
}

// A model that belongs to a specialized family is counted with its vocabulary
// without any configuration; unknown models use the generic encoding.
// A vocabulary whose post-processor adds special tokens (llama2's <s> BOS) adds
// them once per encoded sequence. An estimate sums many fragments, so the BOS
// is counted once for the prompt and never for the output.
func TestEstimateCountsTemplateTokensOncePerPrompt(t *testing.T) {
	const model = "llama-2-70b-chat"
	c, err := selectCounter(model)
	if err != nil || c.name != "llama2" {
		t.Fatalf("counter=%q err=%v want llama2", c.name, err)
	}
	bos := c.counter.(*bpeCounter).templateTokens
	if bos == 0 {
		t.Fatal("llama2 vocabulary has no template tokens; the test needs one")
	}
	if n, _ := c.count(""); n != 0 {
		t.Errorf("count(\"\")=%d want 0: a fragment must not carry the BOS", n)
	}
	// Content-only counts, derived from the parity entry point.
	content := func(s string) int { n, _ := c.counter.Count(s); return n - bos }
	err2 := cancellation()
	estimateFrom(newContext(), chatSnapshot("hello world", false), chatRequest(model), model, err2)
	got := err2.ExtraFields.BilledUsage
	if got == nil {
		t.Fatal("no usage estimated")
	}
	// chatRequest holds one user message "hi": 3 priming + 3 per message + role + content + one BOS.
	if want := 3 + 3 + content("user") + content("hi") + bos; got.PromptTokens != want {
		t.Errorf("prompt=%d want %d (BOS counted once)", got.PromptTokens, want)
	}
	if want := content("hello world"); got.CompletionTokens != want {
		t.Errorf("completion=%d want %d (no BOS on output)", got.CompletionTokens, want)
	}
	// Each text completion prompt is its own sequence, with its own BOS.
	text := func(input *schemas.TextCompletionInput) int {
		e := cancellation()
		request := &schemas.BifrostRequest{RequestType: schemas.TextCompletionStreamRequest, TextCompletionRequest: &schemas.BifrostTextCompletionRequest{Model: model, Input: input}}
		estimateFrom(newContext(), chatSnapshot("hello world", false), request, model, e)
		if e.ExtraFields.BilledUsage == nil {
			t.Fatal("no text completion usage estimated")
		}
		return e.ExtraFields.BilledUsage.PromptTokens
	}
	if got, want := text(&schemas.TextCompletionInput{PromptStr: new("hi")}), content("hi")+bos; got != want {
		t.Errorf("text prompt=%d want %d", got, want)
	}
	if got, want := text(&schemas.TextCompletionInput{PromptArray: []string{"hi", "hello world"}}), content("hi")+content("hello world")+2*bos; got != want {
		t.Errorf("text prompt array=%d want %d (one BOS per prompt)", got, want)
	}
}

func TestEstimateSelectsModelFamily(t *testing.T) {
	for model, want := range map[string]string{"deepseek-chat": "deepseek", "llama-3-70b": "llama3", "claude-opus-4-5": "anthropic", "gpt-4o": "o200k_base", "command-r": "cl100k_base"} {
		err := cancellation()
		estimateFrom(newContext(), chatSnapshot("hello world", false), chatRequest(model), model, err)
		if got := err.ExtraFields.BilledUsage; got == nil || got.Estimation == nil || got.Estimation.Tokenizer != want {
			t.Errorf("%s: usage=%+v want tokenizer %s", model, got, want)
		}
	}
}

// A family's vocabulary is used only once configured; until then its models are
// counted with the base encodings, and disabling a family drops its vocabulary.
func TestConfigureGatesFamilies(t *testing.T) {
	t.Cleanup(func() { Configure([]string{FamilyAll}) })
	tokenizer := func(model string) string {
		t.Helper()
		err := cancellation()
		estimateFrom(newContext(), chatSnapshot("hello world", false), chatRequest(model), model, err)
		if got := err.ExtraFields.BilledUsage; got != nil && got.Estimation != nil {
			return got.Estimation.Tokenizer
		}
		return ""
	}

	if unknown := Configure(nil); len(unknown) != 0 {
		t.Fatalf("Configure(nil) unknown=%v", unknown)
	}
	for _, kind := range Families {
		if _, ok := counters.Load(kind); ok {
			t.Errorf("%s vocabulary still cached after it was disabled", kind)
		}
	}
	for model, want := range map[string]string{"claude-opus-4-5": "cl100k_base", "deepseek-chat": "cl100k_base", "llama-3-70b": "cl100k_base", "gpt-4o": "o200k_base"} {
		if got := tokenizer(model); got != want {
			t.Errorf("default: %s tokenizer=%q want %q", model, got, want)
		}
	}

	// Configuring a family starts loading it before any stream is cancelled.
	if unknown := Configure([]string{" Anthropic ", "no-such-family"}); len(unknown) != 1 || unknown[0] != "no-such-family" {
		t.Fatalf("unknown=%v want [no-such-family]", unknown)
	}
	if _, ok := counters.Load("anthropic"); !ok {
		t.Error("anthropic vocabulary was not preloaded")
	}
	if got := tokenizer("claude-opus-4-5"); got != "anthropic" {
		t.Errorf("anthropic enabled: tokenizer=%q", got)
	}
	if got := tokenizer("deepseek-chat"); got != "cl100k_base" {
		t.Errorf("deepseek not enabled: tokenizer=%q want cl100k_base", got)
	}

	Configure([]string{FamilyAll})
	for model, want := range map[string]string{"claude-opus-4-5": "anthropic", "deepseek-chat": "deepseek", "llama-2-70b-chat": "llama2", "llama-3-70b": "llama3"} {
		if got := tokenizer(model); got != want {
			t.Errorf("all: %s tokenizer=%q want %q", model, got, want)
		}
	}
}

// BenchmarkEstimateNotCancelled is what every non-cancelled stream error pays.
func BenchmarkEstimateNotCancelled(b *testing.B) {
	ctx := newContext()
	err := &schemas.BifrostError{Error: &schemas.ErrorField{Type: schemas.Ptr("server_error")}}
	request := chatRequest("unknown")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Estimate(ctx, request, "unknown", err)
	}
}

func benchmarkEstimate(b *testing.B, model, output string) {
	ctx, accumulated := newContext(), chatSnapshot(output, false)
	request := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionStreamRequest, ChatRequest: &schemas.BifrostChatRequest{Model: model, Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(benchmarkOutput(2048))}}}}}
	_, _ = selectCounter(model) // the one-time vocabulary load is BenchmarkVocabularyLoad
	cpu := processCPU()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		estimateFrom(ctx, accumulated, request, model, cancellation())
	}
	b.StopTimer()
	reportCPU(b, cpu)
}

// BenchmarkCancelledStreamEstimate is the per-cancellation cost of counting a
// 2 KB prompt and the received output, for each specialized family against the
// generic encoding an unrecognised model gets.
func BenchmarkCancelledStreamEstimate(b *testing.B) {
	for _, size := range []int{1 << 10, 16 << 10, 128 << 10} {
		output := benchmarkOutput(size)
		b.Run(fmt.Sprintf("%dKB/generic", size>>10), func(b *testing.B) { benchmarkEstimate(b, "unknown", output) })
		for _, f := range specializedBenchmarks {
			b.Run(fmt.Sprintf("%dKB/%s", size>>10, f.kind), func(b *testing.B) { benchmarkEstimate(b, f.model, output) })
		}
	}
}

// BenchmarkCancelledStreamEstimateParallel shows throughput when many streams
// are cancelled at once and share one loaded vocabulary.
func BenchmarkCancelledStreamEstimateParallel(b *testing.B) {
	output := benchmarkOutput(16 << 10)
	models := append([]struct{ kind, model string }{{"generic", "unknown"}}, specializedBenchmarks...)
	for _, f := range models {
		b.Run(f.kind, func(b *testing.B) {
			ctx, accumulated := newContext(), chatSnapshot(output, false)
			request := chatRequest(f.model)
			_, _ = selectCounter(f.model)
			cpu := processCPU()
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					estimateFrom(ctx, accumulated, request, f.model, cancellation())
				}
			})
			b.StopTimer()
			reportCPU(b, cpu)
		})
	}
}

func BenchmarkCancelledUsage(b *testing.B) {
	c, _ := selectCounter("")
	text := strings.Repeat("hello world ", 1000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = c.count(text)
	}
}

func TestPythonTokenizerParity(t *testing.T) {
	data, err := os.ReadFile("testdata/tokenizers.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Tokenizer string
		Text      string
		Tokens    int
	}
	if err = json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	models := map[string]string{"cl100k_base": "unknown", "o200k_base": "gpt-4o", "anthropic": "claude-opus-4-5", "deepseek": "deepseek-chat", "llama2": "llama-2", "llama3": "llama-3"}
	for _, f := range fixtures {
		t.Run(f.Tokenizer+"/"+f.Text[:min(len(f.Text), 20)], func(t *testing.T) {
			c, err := selectCounter(models[f.Tokenizer])
			if err != nil {
				t.Fatal(err)
			}
			if c.name != f.Tokenizer {
				t.Fatalf("expected %s, selected fallback %s", f.Tokenizer, c.name)
			}
			// The fixtures encode one full sequence, special tokens included.
			got, err := c.count(f.Text)
			got += c.sequenceTokens()
			if err != nil {
				t.Fatal(err)
			}
			if got != f.Tokens {
				t.Fatalf("got=%d Python=%d", got, f.Tokens)
			}
		})
	}
}

// Run once in a fresh test process with -benchtime=1x to measure startup and
// retained memory for all four embedded vocabularies.
func BenchmarkTokenizerStartup(b *testing.B) {
	if b.N != 1 {
		b.Skip("run with -benchtime=1x in a fresh process")
	}
	rss := func() uint64 {
		data, err := os.ReadFile("/proc/self/statm")
		if err != nil {
			return 0
		}
		fields := strings.Fields(string(data))
		if len(fields) < 2 {
			return 0
		}
		n, _ := strconv.ParseUint(fields[1], 10, 64)
		return n * uint64(os.Getpagesize())
	}
	measure := func(prefix string, warm func()) {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		r0, cpu, start := rss(), processCPU(), time.Now()
		warm()
		elapsed, used := time.Since(start), processCPU()-cpu
		runtime.GC()
		runtime.ReadMemStats(&after)
		r1 := rss()
		b.ReportMetric(float64(elapsed.Nanoseconds()), prefix+"-wall-ns")
		b.ReportMetric(float64(used.Nanoseconds()), prefix+"-cpu-ns")
		if after.HeapAlloc > before.HeapAlloc {
			b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc), prefix+"-retained-Go-B")
		}
		if r1 > r0 {
			b.ReportMetric(float64(r1-r0), prefix+"-retained-RSS-B")
		}
	}
	b.ResetTimer()
	// The generic encodings load for every deployment; specialized is the extra.
	measure("generic", func() {
		_, _ = selectCounter("")
		_, _ = selectCounter("gpt-4o")
	})
	measure("specialized", func() {
		for _, f := range specializedBenchmarks {
			_, _ = selectCounter(f.model)
		}
	})
	b.StopTimer()
}

// Every embedded vocabulary must be the exact upstream file the manifest pins.
func TestEmbeddedVocabulariesMatchManifest(t *testing.T) {
	data, err := os.ReadFile("assets/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Assets []struct{ Kind, SHA256 string }
	}
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	embedded, err := vocabularies.ReadDir("assets")
	if err != nil {
		t.Fatal(err)
	}
	if len(embedded) != len(manifest.Assets) {
		t.Fatalf("embedded %d vocabularies, manifest lists %d", len(embedded), len(manifest.Assets))
	}
	for _, asset := range manifest.Assets {
		compressed, err := vocabularies.ReadFile("assets/" + asset.Kind + ".json.gz")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := gunzip(compressed)
		if err != nil {
			t.Fatal(err)
		}
		if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != asset.SHA256 {
			t.Fatalf("%s vocabulary does not match manifest checksum", asset.Kind)
		}
	}
}

// A vocabulary using components the counter does not implement must fail to
// load, so selection falls back to tiktoken instead of miscounting.
func TestUnsupportedVocabularyComponentsRejected(t *testing.T) {
	base := `{"added_tokens":[],"model":{"type":"BPE","vocab":{"a":0,"b":1,"ab":2},"merges":["a b"]}`
	if _, err := parseBPE([]byte(base + `}`)); err != nil {
		t.Fatalf("minimal vocabulary: %v", err)
	}
	for name, extra := range map[string]string{
		"normalizer":     `,"normalizer":{"type":"Lowercase"}`,
		"pre-tokenizer":  `,"pre_tokenizer":{"type":"Whitespace"}`,
		"split behavior": `,"pre_tokenizer":{"type":"Split","pattern":{"Regex":"a"},"behavior":"Removed","invert":false}`,
		"post-processor": `,"post_processor":{"type":"RobertaProcessing"}`,
	} {
		if _, err := parseBPE([]byte(base + extra + `}`)); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
	if _, err := parseBPE([]byte(`{"model":{"type":"WordPiece","vocab":{}}}`)); err == nil {
		t.Error("WordPiece model: expected rejection")
	}
}

// Pins the contracts the helper doc comments state.
func TestDocumentedHelperContracts(t *testing.T) {
	// isNull: an explicit null component is the same as an absent one.
	if _, err := parseBPE([]byte(`{"normalizer":null,"pre_tokenizer":null,"post_processor":null,"model":{"type":"BPE","vocab":{"a":0,"b":1,"ab":2},"merges":["a b"]}}`)); err != nil {
		t.Errorf("null components: %v", err)
	}
	// parseMerges: merges may be "left right" strings or [left, right] pairs.
	if c, err := parseBPE([]byte(`{"model":{"type":"BPE","vocab":{"a":0,"b":1,"ab":2},"merges":[["a","b"]]}}`)); err != nil {
		t.Errorf("pair-form merges: %v", err)
	} else if n, _ := c.Count("ab"); n != 1 {
		t.Errorf("pair-form merge not applied: Count(ab)=%d want 1", n)
	}
	// loadVocabularyCounter: a kind with no embedded asset is an error.
	if _, err := loadVocabularyCounter("no-such-family"); err == nil {
		t.Error("unknown vocabulary kind: expected an error")
	}
	// family: Claude 3 models are not counted with the Anthropic vocabulary.
	if c, err := selectCounter("claude-3-5-sonnet-20241022"); err != nil || c.name != "cl100k_base" {
		t.Errorf("claude-3 counter=%q err=%v want cl100k_base", c.name, err)
	}
	// addedTokens: matches are leftmost-longest, whatever order tokens were added in.
	var added addedTokens
	added.add("ab")
	added.add("abc")
	var segments []string
	matches := 0
	added.each("xabcd", func(s string) { segments = append(segments, s) }, func() { matches++ })
	if matches != 1 || strings.Join(segments, "|") != "x|d" {
		t.Errorf("added-token split=%q matches=%d want x|d with 1 match", segments, matches)
	}
	// finished: a Responses stream that ended incomplete is finished; one in progress is not.
	for status, want := range map[string]bool{"completed": true, "incomplete": true, "in_progress": false} {
		response := &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{Status: new(status)}}
		if got := finished(response); got != want {
			t.Errorf("finished(status=%s)=%v want %v", status, got, want)
		}
	}
}

// Words longer than any merge chain exercise the heap-ordered merge loop.
func TestBPEMergeOrder(t *testing.T) {
	c, err := parseBPE([]byte(`{"model":{"type":"BPE","vocab":{"a":0,"b":1,"ab":2,"abab":3,"ba":4},"merges":["a b","ab ab","b a"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	// "ab" outranks "ba", so "abab" becomes one token rather than a+ba+b.
	for text, want := range map[string]int{"": 0, "a": 1, "ab": 1, "aba": 2, "abab": 1, "ababab": 2, "bab": 2} {
		if got, err := c.Count(text); err != nil || got != want {
			t.Errorf("Count(%q) = %d, %v; want %d", text, got, err, want)
		}
	}
}

// specializedBenchmarks pairs each tokenizer family with a model that selects it.
var specializedBenchmarks = []struct{ kind, model string }{
	{"anthropic", "claude-opus-4-5"},
	{"deepseek", "deepseek-chat"},
	{"llama2", "llama-2"},
	{"llama3", "llama-3"},
}

// benchmarkOutput builds deterministic mixed output (prose, code, JSON, CJK) of
// roughly size bytes, resembling what a cancelled stream has received.
func benchmarkOutput(size int) string {
	parts := []string{
		"The model streamed a detailed answer about distributed tracing and token budgets. ",
		"func handle(ctx context.Context) error {\n\treturn nil\n}\n",
		`{"city":"Paris","temperature":21.5,"tags":["a","b"]} `,
		"日本語のテキストと中文测试。 ",
		"It's 12345 o'clock; we'll retry 3 times.\n",
	}
	var b strings.Builder
	for i := 0; b.Len() < size; i++ {
		b.WriteString(parts[i%len(parts)])
	}
	return b.String()[:size]
}

// processCPU returns user+system CPU time consumed by this process, including
// GC workers, from /proc. It returns 0 where /proc is unavailable.
func processCPU() time.Duration {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data[strings.LastIndexByte(string(data), ')')+1:]))
	if len(fields) < 13 {
		return 0
	}
	utime, _ := strconv.ParseInt(fields[11], 10, 64)
	stime, _ := strconv.ParseInt(fields[12], 10, 64)
	return time.Duration(utime+stime) * 10 * time.Millisecond // USER_HZ is 100 on Linux
}

func reportCPU(b *testing.B, start time.Duration) {
	if cpu := processCPU() - start; cpu > 0 && b.N > 0 {
		b.ReportMetric(float64(cpu.Nanoseconds())/float64(b.N), "cpu-ns/op")
	}
}

// BenchmarkVocabularyLoad is the one-time cost of enabling a family: time, CPU,
// bytes allocated while loading, and Go heap retained by the loaded counter.
func BenchmarkVocabularyLoad(b *testing.B) {
	for _, f := range specializedBenchmarks {
		b.Run(f.kind, func(b *testing.B) {
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			kept, err := loadVocabularyCounter(f.kind)
			if err != nil {
				b.Fatal(err)
			}
			runtime.GC()
			runtime.ReadMemStats(&after)
			runtime.KeepAlive(kept)
			cpu := processCPU()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := loadVocabularyCounter(f.kind); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			reportCPU(b, cpu)
			b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc), "retained-heap-B")
		})
	}
}
