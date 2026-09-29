package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/memtest"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// TestStripFunctionResponseMediaRefs_AllocationScaling pins the allocation shape of
// the $ref strip.
//
// The loop deletes one top-level key at a time, and every providerUtils.DeleteJSONField
// reserialises the whole function-response document, so N media refs cost N copies of
// it. Today N is small in practice, which is exactly why this went unnoticed: the
// complexity is wrong but the payloads have been forgiving. A tool returning many
// media parts is all it takes for that to stop being true.
//
// memtest compares allocation growth against input growth, so this fails on the
// complexity class rather than on a byte threshold that would encode this machine.
func TestStripFunctionResponseMediaRefs_AllocationScaling(t *testing.T) {
	memtest.AssertAllocScaling(t, func(refs int) []byte {
		var b bytes.Buffer
		b.WriteString(`{"output":"`)
		b.WriteString(strings.Repeat("o", 200))
		b.WriteString(`"`)
		for i := range refs {
			// Each media ref is a {"$ref": ...} placeholder, which is what the strip
			// targets. The long ref value is what makes the payload grow with N.
			b.WriteString(`,"media_`)
			b.WriteString(strings.Repeat("k", 3))
			b.WriteString(itoa(i))
			b.WriteString(`":{"$ref":"`)
			b.WriteString(strings.Repeat("r", 400))
			b.WriteString(`"}`)
		}
		b.WriteString(`}`)
		return b.Bytes()
	}, func(body []byte) {
		stripFunctionResponseMediaRefs(json.RawMessage(body))
	})
}

// itoa avoids pulling strconv in just for the payload builder above.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// Issue #7601: /v1/responses on gemini/* returned no status, and a turn cut short
// by MAX_TOKENS was indistinguishable from a complete one. OpenAI's Responses
// contract (and the Bedrock fix in #4679) sets status "completed" on a finished
// turn and status "incomplete" + incomplete_details on a truncated one. Error
// finish reasons (SAFETY etc.) keep their existing "failed" status.
var geminiResponsesStatusCases = []struct {
	finishReason   FinishReason
	wantType       schemas.ResponsesStreamResponseType
	wantStatus     string
	wantIncomplete string
}{
	{FinishReasonStop, schemas.ResponsesStreamResponseTypeCompleted, schemas.ResponsesResponseStatusCompleted, ""},
	{FinishReasonMaxTokens, schemas.ResponsesStreamResponseTypeIncomplete, schemas.ResponsesResponseStatusIncomplete, schemas.ResponsesResponseIncompleteReasonMaxOutputTokens},
	{FinishReasonLanguage, schemas.ResponsesStreamResponseTypeIncomplete, schemas.ResponsesResponseStatusIncomplete, schemas.ResponsesResponseIncompleteReasonContentFilter},
	{FinishReasonSafety, schemas.ResponsesStreamResponseTypeCompleted, "failed", ""},
	// A finish reason with no Bifrost mapping is not a confirmed clean finish.
	{FinishReason("SOME_FUTURE_FINISH_REASON"), schemas.ResponsesStreamResponseTypeIncomplete, schemas.ResponsesResponseStatusIncomplete, ""},
}

func geminiTruncationFixture(finishReason FinishReason) *GenerateContentResponse {
	return &GenerateContentResponse{
		ResponseID:   "resp-7601",
		ModelVersion: "gemini-3-flash-preview",
		Candidates: []*Candidate{{
			Content:      &Content{Role: "model", Parts: []*Part{{Text: "Rome was"}}},
			FinishReason: finishReason,
		}},
		UsageMetadata: &GenerateContentResponseUsageMetadata{PromptTokenCount: 20, CandidatesTokenCount: 37, TotalTokenCount: 57},
	}
}

func assertGeminiResponsesStatus(t *testing.T, gotStatus *string, gotDetails *schemas.ResponsesResponseIncompleteDetails, wantStatus, wantIncomplete string) {
	t.Helper()
	if gotStatus == nil || *gotStatus != wantStatus {
		t.Errorf("status = %v, want %q", gotStatus, wantStatus)
	}
	if wantIncomplete == "" {
		if gotDetails != nil {
			t.Errorf("incomplete_details = %+v, want nil", gotDetails)
		}
		return
	}
	if gotDetails == nil || gotDetails.Reason != wantIncomplete {
		t.Errorf("incomplete_details = %+v, want reason %q", gotDetails, wantIncomplete)
	}
}

func TestGeminiResponsesStatusFromFinishReason(t *testing.T) {
	for _, tc := range geminiResponsesStatusCases {
		t.Run(string(tc.finishReason), func(t *testing.T) {
			resp := geminiTruncationFixture(tc.finishReason).ToResponsesBifrostResponsesResponse()
			assertGeminiResponsesStatus(t, resp.Status, resp.IncompleteDetails, tc.wantStatus, tc.wantIncomplete)
		})
	}
}

func TestGeminiResponsesStreamTerminalFromFinishReason(t *testing.T) {
	for _, tc := range geminiResponsesStatusCases {
		t.Run(string(tc.finishReason), func(t *testing.T) {
			state := &GeminiResponsesStreamState{}
			state.flush()
			events, bErr := geminiTruncationFixture(tc.finishReason).ToBifrostResponsesStream(0, state)
			if bErr != nil {
				t.Fatalf("ToBifrostResponsesStream error: %v", bErr)
			}
			if len(events) == 0 {
				t.Fatal("stream produced no events")
			}
			terminal := events[len(events)-1]
			if terminal.Type != tc.wantType {
				t.Errorf("terminal event type = %q, want %q", terminal.Type, tc.wantType)
			}
			if terminal.Response == nil {
				t.Fatal("terminal event carries no response")
			}
			assertGeminiResponsesStatus(t, terminal.Response.Status, terminal.Response.IncompleteDetails, tc.wantStatus, tc.wantIncomplete)
		})
	}
}

// Issue #7601: response.incomplete is a terminal event. The GenAI stream egress handled
// only response.completed, so a truncated turn lost its final chunk (finishReason and
// usage). A provider that reports truncation only via incomplete_details must still
// surface MAX_TOKENS rather than STOP.
func TestToGeminiResponsesStreamResponse_IncompleteCarriesFinishReason(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *schemas.BifrostResponsesResponse
		want     FinishReason
	}{
		{"stop reason length", &schemas.BifrostResponsesResponse{StopReason: schemas.Ptr("length")}, FinishReasonMaxTokens},
		{"incomplete_details only", &schemas.BifrostResponsesResponse{
			IncompleteDetails: &schemas.ResponsesResponseIncompleteDetails{Reason: schemas.ResponsesResponseIncompleteReasonMaxOutputTokens},
		}, FinishReasonMaxTokens},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.response.Usage = &schemas.ResponsesResponseUsage{InputTokens: 20, OutputTokens: 16, TotalTokens: 36}
			out := ToGeminiResponsesStreamResponse(&schemas.BifrostResponsesStreamResponse{
				Type:     schemas.ResponsesStreamResponseTypeIncomplete,
				Response: tc.response,
			}, NewBifrostToGeminiStreamState())
			if out == nil || len(out.Candidates) == 0 {
				t.Fatalf("response.incomplete produced no GenAI chunk: %+v", out)
			}
			if got := out.Candidates[0].FinishReason; got != tc.want {
				t.Errorf("finishReason = %q, want %q", got, tc.want)
			}
			if out.UsageMetadata == nil || out.UsageMetadata.CandidatesTokenCount != 16 {
				t.Errorf("final chunk must carry usage, got %+v", out.UsageMetadata)
			}
		})
	}
}

// Follow-up to #7601: when thinking consumes the whole output budget, Gemini's
// streaming endpoint answers 200 with an empty SSE body -- no chunk, no finishReason.
// The finalize path then synthesized a bare response.completed with no status, no
// model and no created/in_progress, so the turn looked complete. A stream that ends
// without any finishReason never reached a clean finish: it must end incomplete,
// with incomplete_details left null because the upstream gave no reason.
func TestGeminiResponsesStreamWithoutFinishReasonEndsIncomplete(t *testing.T) {
	noopPostHook := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	for _, tc := range []struct {
		name      string
		body      string
		wantTypes []schemas.ResponsesStreamResponseType
	}{
		{"empty body", "", []schemas.ResponsesStreamResponseType{
			schemas.ResponsesStreamResponseTypeCreated,
			schemas.ResponsesStreamResponseTypeInProgress,
			schemas.ResponsesStreamResponseTypeIncomplete,
		}},
		{"content then EOF", `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Rome was"}]}}],"modelVersion":"gemini-2.5-flash"}` + "\n\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer ts.Close()

			stream, bErr := HandleGeminiResponsesStream(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
				&fasthttp.Client{}, ts.URL+"/models/gemini-2.5-flash:streamGenerateContent?alt=sse", []byte(`{}`),
				map[string]string{"Accept": "text/event-stream"}, nil, 30, false, false, schemas.Gemini, "gemini-2.5-flash",
				noopPostHook, nil, testNoopLogger{}, func(context.Context) {})
			if bErr != nil {
				t.Fatalf("stream setup failed: %v", bErr)
			}
			var events []*schemas.BifrostResponsesStreamResponse
			for chunk := range stream {
				if chunk.BifrostError != nil {
					t.Fatalf("unexpected stream error: %+v", chunk.BifrostError.Error)
				}
				if chunk.BifrostResponsesStreamResponse != nil {
					events = append(events, chunk.BifrostResponsesStreamResponse)
				}
			}
			if len(events) == 0 {
				t.Fatal("stream produced no events")
			}
			if tc.wantTypes != nil {
				var got []schemas.ResponsesStreamResponseType
				for _, e := range events {
					got = append(got, e.Type)
				}
				if len(got) != len(tc.wantTypes) {
					t.Fatalf("event types = %v, want %v", got, tc.wantTypes)
				}
				for i := range got {
					if got[i] != tc.wantTypes[i] {
						t.Fatalf("event types = %v, want %v", got, tc.wantTypes)
					}
				}
			}
			terminal := events[len(events)-1]
			if terminal.Type != schemas.ResponsesStreamResponseTypeIncomplete {
				t.Errorf("terminal event type = %q, want %q", terminal.Type, schemas.ResponsesStreamResponseTypeIncomplete)
			}
			if terminal.Response == nil {
				t.Fatal("terminal event carries no response")
			}
			if terminal.Response.Status == nil || *terminal.Response.Status != schemas.ResponsesResponseStatusIncomplete {
				t.Errorf("status = %v, want %q", terminal.Response.Status, schemas.ResponsesResponseStatusIncomplete)
			}
			if terminal.Response.IncompleteDetails != nil {
				t.Errorf("incomplete_details = %+v, want nil (upstream gave no reason)", terminal.Response.IncompleteDetails)
			}
			if terminal.Response.Model != "gemini-2.5-flash" {
				t.Errorf("model = %q, want %q", terminal.Response.Model, "gemini-2.5-flash")
			}
		})
	}
}

// Follow-up to #7601: OpenAI marks the output item that was being written when the
// cap hit as status "incomplete". The response-level status was fixed, but the
// truncated message item still reported "completed".
func TestGeminiResponsesTruncatedOutputItemIncomplete(t *testing.T) {
	resp := geminiTruncationFixture(FinishReasonMaxTokens).ToResponsesBifrostResponsesResponse()
	assertLastOutputItemStatus(t, "non-stream", resp.Output, schemas.ResponsesResponseStatusIncomplete)

	state := &GeminiResponsesStreamState{}
	state.flush()
	events, bErr := geminiTruncationFixture(FinishReasonMaxTokens).ToBifrostResponsesStream(0, state)
	if bErr != nil {
		t.Fatalf("ToBifrostResponsesStream error: %v", bErr)
	}
	assertLastOutputItemStatus(t, "stream terminal", events[len(events)-1].Response.Output, schemas.ResponsesResponseStatusIncomplete)

	// A complete turn keeps its item completed.
	done := geminiTruncationFixture(FinishReasonStop).ToResponsesBifrostResponsesResponse()
	assertLastOutputItemStatus(t, "complete turn", done.Output, "completed")
}

func assertLastOutputItemStatus(t *testing.T, label string, output []schemas.ResponsesMessage, want string) {
	t.Helper()
	if len(output) == 0 {
		t.Fatalf("%s: no output items", label)
	}
	last := output[len(output)-1]
	if last.Status == nil || *last.Status != want {
		t.Errorf("%s: last output item status = %v, want %q", label, derefStatus(last.Status), want)
	}
}

func derefStatus(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// A terminal chunk that carries finishReason but no content (thinking used the whole
// budget) opens no item, so it cannot close the stream itself. Its finishReason must
// still reach the terminal event rather than being reported as an unknown finish.
func TestGeminiResponsesStreamContentlessFinishReasonReachesTerminal(t *testing.T) {
	state := &GeminiResponsesStreamState{}
	state.flush()
	chunk := &GenerateContentResponse{
		ModelVersion:  "gemini-2.5-flash",
		Candidates:    []*Candidate{{Content: &Content{Role: "model"}, FinishReason: FinishReasonMaxTokens}},
		UsageMetadata: &GenerateContentResponseUsageMetadata{PromptTokenCount: 23, ThoughtsTokenCount: 12, TotalTokenCount: 35},
	}
	events, bErr := chunk.ToBifrostResponsesStream(0, state)
	if bErr != nil {
		t.Fatalf("ToBifrostResponsesStream error: %v", bErr)
	}
	events = append(events, FinalizeGeminiResponsesStream(state, chunk.UsageMetadata, len(events))...)
	terminal := events[len(events)-1]
	if terminal.Type != schemas.ResponsesStreamResponseTypeIncomplete {
		t.Fatalf("terminal event type = %q, want %q", terminal.Type, schemas.ResponsesStreamResponseTypeIncomplete)
	}
	assertGeminiResponsesStatus(t, terminal.Response.Status, terminal.Response.IncompleteDetails,
		schemas.ResponsesResponseStatusIncomplete, schemas.ResponsesResponseIncompleteReasonMaxOutputTokens)
}

// responsesToolOutputHasRefKey reports whether any object at any depth of raw has a "$ref"
// key. Gemini reads {"$ref": "<displayName>"} inside function_response.response as a pointer
// to a multimodal part (#7694).
func responsesToolOutputHasRefKey(raw []byte) bool {
	var found bool
	var walk func(v gjson.Result)
	walk = func(v gjson.Result) {
		v.ForEach(func(key, value gjson.Result) bool {
			if v.IsObject() && key.String() == "$ref" {
				found = true
				return false
			}
			if value.IsObject() || value.IsArray() {
				walk(value)
			}
			return !found
		})
	}
	walk(gjson.ParseBytes(raw))
	return found
}

// TestConvertResponsesMessagesToGeminiContents_FunctionOutputRefKeyStaysOpaque pins the
// Responses API twin of #7694: a function_call_output whose text is a JSON object holding a
// "$ref" key anywhere must reach Gemini as an opaque string under "output", not embedded as
// raw JSON, for both the string form and the content-block form of the output. Output without
// "$ref" keeps the structured embedding.
func TestConvertResponsesMessagesToGeminiContents_FunctionOutputRefKeyStaysOpaque(t *testing.T) {
	const refOutput = `{"schema":{"$ref":"#/components/schemas/SOM_computer_post_response"}}`
	const plainOutput = `{"temperature":22,"condition":"sunny"}`

	build := func(output *schemas.ResponsesToolMessageOutputStruct) []schemas.ResponsesMessage {
		return []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Fetch the spec, then reply ok.")},
			},
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    schemas.Ptr("c1"),
					Name:      schemas.Ptr("bash"),
					Arguments: schemas.Ptr(`{"command":"cat spec.json"}`),
				},
			},
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("c1"),
					Name:   schemas.Ptr("bash"),
					Output: output,
				},
			},
		}
	}

	functionResponse := func(t *testing.T, msgs []schemas.ResponsesMessage) []byte {
		t.Helper()
		contents, _, err := convertResponsesMessagesToGeminiContents(msgs, "gemini-flash-latest", schemas.Gemini)
		if err != nil {
			t.Fatalf("convert: %v", err)
		}
		for _, c := range contents {
			for _, p := range c.Parts {
				if p.FunctionResponse != nil {
					return p.FunctionResponse.Response
				}
			}
		}
		t.Fatal("no functionResponse part produced")
		return nil
	}

	t.Run("string output with nested $ref is wrapped as opaque text", func(t *testing.T) {
		resp := functionResponse(t, build(&schemas.ResponsesToolMessageOutputStruct{
			ResponsesToolCallOutputStr: schemas.Ptr(refOutput),
		}))
		if responsesToolOutputHasRefKey(resp) {
			t.Errorf("function_response.response must not carry a $ref key: %s", resp)
		}
		if got := gjson.GetBytes(resp, "output"); got.Type != gjson.String || got.String() != refOutput {
			t.Errorf("tool text must survive verbatim as a string under \"output\", got %s", resp)
		}
	})

	t.Run("text block output with nested $ref is wrapped as opaque text", func(t *testing.T) {
		resp := functionResponse(t, build(&schemas.ResponsesToolMessageOutputStruct{
			ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{{
				Type: schemas.ResponsesInputMessageContentBlockTypeText,
				Text: schemas.Ptr(refOutput),
			}},
		}))
		if responsesToolOutputHasRefKey(resp) {
			t.Errorf("function_response.response must not carry a $ref key: %s", resp)
		}
		if got := gjson.GetBytes(resp, "output"); got.Type != gjson.String || got.String() != refOutput {
			t.Errorf("tool text must survive verbatim as a string under \"output\", got %s", resp)
		}
	})

	t.Run("unicode-escaped $ref key is wrapped as opaque text", func(t *testing.T) {
		// JSON allows any character of a key to be escaped; "$r\u0065f" decodes to "$ref".
		escaped := `{"schema":{"$r\u0065f":"#/x"}}`
		resp := functionResponse(t, build(&schemas.ResponsesToolMessageOutputStruct{
			ResponsesToolCallOutputStr: schemas.Ptr(escaped),
		}))
		if responsesToolOutputHasRefKey(resp) {
			t.Errorf("function_response.response must not carry a $ref key: %s", resp)
		}
		if got := gjson.GetBytes(resp, "output"); got.Type != gjson.String || got.String() != escaped {
			t.Errorf("tool text must survive verbatim as a string under \"output\", got %s", resp)
		}
	})

	t.Run("JSON output without $ref keeps the structured embedding", func(t *testing.T) {
		resp := functionResponse(t, build(&schemas.ResponsesToolMessageOutputStruct{
			ResponsesToolCallOutputStr: schemas.Ptr(plainOutput),
		}))
		if got := gjson.GetBytes(resp, "output"); !got.IsObject() || got.Get("temperature").Int() != 22 {
			t.Errorf("plain JSON output must still be embedded as a structured object, got %s", resp)
		}
	})
}

// The code_interpreter tool has to reach Gemini as its codeExecution tool, or the model
// never runs code at all and the response-side conversion has nothing to convert.
func TestConvertResponsesToolsToGeminiCodeExecution(t *testing.T) {
	t.Run("code_interpreter becomes codeExecution", func(t *testing.T) {
		tools, err := convertResponsesToolsToGemini([]schemas.ResponsesTool{
			{
				Type:                         schemas.ResponsesToolTypeCodeInterpreter,
				ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{},
			},
		}, false, schemas.Gemini, "gemini-2.5-flash")
		if err != nil {
			t.Fatalf("convertResponsesToolsToGemini error: %v", err)
		}
		if len(tools) != 1 || tools[0].CodeExecution == nil {
			t.Fatalf("tools = %+v, want one entry carrying CodeExecution", tools)
		}
	})

	// Same mixed-tool restriction as Google Search: a function tool the model cannot
	// otherwise invoke wins over the server-side tool.
	t.Run("dropped alongside a function tool", func(t *testing.T) {
		tools, err := convertResponsesToolsToGemini([]schemas.ResponsesTool{
			{
				Type:                         schemas.ResponsesToolTypeCodeInterpreter,
				ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{},
			},
			{
				Type:                  schemas.ResponsesToolTypeFunction,
				Name:                  schemas.Ptr("get_weather"),
				ResponsesToolFunction: &schemas.ResponsesToolFunction{},
			},
		}, false, schemas.Gemini, "gemini-2.5-flash")
		if err != nil {
			t.Fatalf("convertResponsesToolsToGemini error: %v", err)
		}
		for _, tool := range tools {
			if tool.CodeExecution != nil {
				t.Fatalf("CodeExecution survived alongside a function tool: %+v", tools)
			}
		}
		if len(tools) != 1 || len(tools[0].FunctionDeclarations) != 1 {
			t.Fatalf("tools = %+v, want the function declaration to survive", tools)
		}
	})

	t.Run("round-trips back to code_interpreter", func(t *testing.T) {
		got := convertGeminiToolsToResponsesTools([]Tool{{CodeExecution: &ToolCodeExecution{}}})
		if len(got) != 1 || got[0].Type != schemas.ResponsesToolTypeCodeInterpreter {
			t.Fatalf("tools = %+v, want one code_interpreter tool", got)
		}
	})
}

// executableCode and codeExecutionResult arrive as two sibling parts. They must land as
// one structured code_interpreter_call carrying code and outputs, not as prose text —
// that is the shape OpenAI and Anthropic already produce.
func TestGeminiCodeExecutionPartsBecomeCodeInterpreterCall(t *testing.T) {
	out := convertGeminiCandidatesToResponsesOutput([]*Candidate{
		{Content: &Content{Role: "model", Parts: []*Part{
			{ExecutableCode: &ExecutableCode{Language: "PYTHON", Code: "print(6*7)"}},
			{CodeExecutionResult: &CodeExecutionResult{Outcome: OutcomeOK, Output: "42\n"}},
		}}},
	})

	var calls []schemas.ResponsesMessage
	for _, msg := range out {
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeCodeInterpreterCall {
			calls = append(calls, msg)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("code_interpreter_call count = %d, want 1 (output: %+v)", len(calls), out)
	}
	if calls[0].ResponsesToolMessage == nil {
		t.Fatalf("call carries no tool message: %+v", calls[0])
	}
	ci := calls[0].ResponsesToolMessage.ResponsesCodeInterpreterToolCall
	if ci == nil || ci.Code == nil || *ci.Code != "print(6*7)" {
		t.Fatalf("code = %+v, want print(6*7)", ci)
	}
	if len(ci.Outputs) != 1 || ci.Outputs[0].ResponsesCodeInterpreterOutputLogs == nil ||
		ci.Outputs[0].ResponsesCodeInterpreterOutputLogs.Logs != "42\n" {
		t.Fatalf("outputs = %+v, want one logs output of %q", ci.Outputs, "42\n")
	}
}

// A failed run still carries its output; the outcome is prefixed rather than dropped.
func TestGeminiCodeExecutionFailureKeepsOutput(t *testing.T) {
	out := convertGeminiCandidatesToResponsesOutput([]*Candidate{
		{Content: &Content{Role: "model", Parts: []*Part{
			{ExecutableCode: &ExecutableCode{Language: "PYTHON", Code: "1/0"}},
			{CodeExecutionResult: &CodeExecutionResult{Outcome: OutcomeFailed, Output: "ZeroDivisionError"}},
		}}},
	})
	last := out[len(out)-1]
	if last.ResponsesToolMessage == nil || last.ResponsesToolMessage.ResponsesCodeInterpreterToolCall == nil {
		t.Fatalf("last item is not a code_interpreter_call: %+v", last)
	}
	ci := last.ResponsesToolMessage.ResponsesCodeInterpreterToolCall
	if len(ci.Outputs) != 1 || ci.Outputs[0].ResponsesCodeInterpreterOutputLogs == nil ||
		ci.Outputs[0].ResponsesCodeInterpreterOutputLogs.Logs != "Error: ZeroDivisionError" {
		t.Fatalf("outputs = %+v, want the failure prefixed", ci.Outputs)
	}
}

// Streaming previously dropped both parts on the floor, so a streamed code execution
// reached the client as nothing at all.
func TestGeminiResponsesStreamEmitsCodeInterpreterCall(t *testing.T) {
	state := &GeminiResponsesStreamState{}
	state.flush()
	chunk := &GenerateContentResponse{
		ModelVersion: "gemini-2.5-flash",
		Candidates: []*Candidate{{Content: &Content{Role: "model", Parts: []*Part{
			{ExecutableCode: &ExecutableCode{Language: "PYTHON", Code: "print(6*7)"}},
			{CodeExecutionResult: &CodeExecutionResult{Outcome: OutcomeOK, Output: "42\n"}},
		}}}},
	}
	events, bErr := chunk.ToBifrostResponsesStream(0, state)
	if bErr != nil {
		t.Fatalf("ToBifrostResponsesStream error: %v", bErr)
	}

	var added, done *schemas.BifrostResponsesStreamResponse
	for _, event := range events {
		if event.Item == nil || event.Item.Type == nil ||
			*event.Item.Type != schemas.ResponsesMessageTypeCodeInterpreterCall {
			continue
		}
		switch event.Type {
		case schemas.ResponsesStreamResponseTypeOutputItemAdded:
			added = event
		case schemas.ResponsesStreamResponseTypeOutputItemDone:
			done = event
		}
	}
	if added == nil || done == nil {
		t.Fatalf("want an added and a done code_interpreter_call event, got %d events", len(events))
	}
	if added.ItemID == nil || done.ItemID == nil || *added.ItemID != *done.ItemID {
		t.Fatalf("item IDs differ between added and done: %v / %v", added.ItemID, done.ItemID)
	}
	ci := done.Item.ResponsesToolMessage.ResponsesCodeInterpreterToolCall
	if ci.Code == nil || *ci.Code != "print(6*7)" {
		t.Fatalf("code = %+v, want print(6*7)", ci.Code)
	}
	if len(ci.Outputs) != 1 || ci.Outputs[0].ResponsesCodeInterpreterOutputLogs.Logs != "42\n" {
		t.Fatalf("outputs = %+v, want one logs output", ci.Outputs)
	}
	// The closed item must also reach response.completed's Output array.
	if state.OutputItems[*done.OutputIndex] == nil {
		t.Fatalf("closed call was not recorded for response.completed")
	}
}

// A stream that ends after the code but before its result must still close the item,
// or the client is left with an item that never completes.
func TestGeminiResponsesStreamClosesDanglingCodeInterpreterCall(t *testing.T) {
	state := &GeminiResponsesStreamState{}
	state.flush()
	chunk := &GenerateContentResponse{
		ModelVersion: "gemini-2.5-flash",
		Candidates: []*Candidate{{
			Content:      &Content{Role: "model", Parts: []*Part{{ExecutableCode: &ExecutableCode{Code: "print(1)"}}}},
			FinishReason: FinishReasonStop,
		}},
	}
	events, bErr := chunk.ToBifrostResponsesStream(0, state)
	if bErr != nil {
		t.Fatalf("ToBifrostResponsesStream error: %v", bErr)
	}
	for _, event := range events {
		if event.Type == schemas.ResponsesStreamResponseTypeOutputItemDone && event.Item != nil &&
			event.Item.Type != nil && *event.Item.Type == schemas.ResponsesMessageTypeCodeInterpreterCall {
			return
		}
	}
	t.Fatalf("dangling code_interpreter_call was never closed (%d events)", len(events))
}
