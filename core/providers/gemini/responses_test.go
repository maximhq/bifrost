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
	"github.com/maximhq/bifrost/core/providers/anthropic"
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

// TestConvertResponsesToolsToGemini_FunctionWithoutSchema verifies that a function tool
// declared without a schema (an Anthropic tool with no input_schema converts
// to a ResponsesTool whose ResponsesToolFunction is nil) still reaches Gemini as a
// FunctionDeclaration; dropping it tells the model it has no tools.
func TestConvertResponsesToolsToGemini_FunctionWithoutSchema(t *testing.T) {
	tests := []struct {
		name           string
		tool           schemas.ResponsesTool
		wantName       string
		wantDesc       string
		wantParameters string
	}{
		{
			name: "nil function struct",
			tool: schemas.ResponsesTool{
				Type:        schemas.ResponsesToolTypeFunction,
				Name:        schemas.Ptr("get_time"),
				Description: schemas.Ptr("Returns the current time"),
			},
			wantName: "get_time",
			wantDesc: "Returns the current time",
		},
		{
			name: "nil function struct and no description",
			tool: schemas.ResponsesTool{
				Type: schemas.ResponsesToolTypeFunction,
				Name: schemas.Ptr("ping"),
			},
			wantName: "ping",
		},
		{
			name: "function struct with parameters",
			tool: schemas.ResponsesTool{
				Type: schemas.ResponsesToolTypeFunction,
				Name: schemas.Ptr("lookup"),
				ResponsesToolFunction: &schemas.ResponsesToolFunction{
					Parameters: &schemas.ToolFunctionParameters{Type: "object"},
				},
			},
			wantName:       "lookup",
			wantParameters: `{"type":"object","properties":{}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools, err := convertResponsesToolsToGemini([]schemas.ResponsesTool{tt.tool}, false, schemas.Gemini, "gemini-2.5-pro")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(tools) != 1 || len(tools[0].FunctionDeclarations) != 1 {
				t.Fatalf("expected exactly one function declaration, got %+v", tools)
			}
			decl := tools[0].FunctionDeclarations[0]
			if decl.Name != tt.wantName {
				t.Errorf("name = %q, want %q", decl.Name, tt.wantName)
			}
			if decl.Description != tt.wantDesc {
				t.Errorf("description = %q, want %q", decl.Description, tt.wantDesc)
			}
			got := ""
			if raw, ok := decl.ParametersJSONSchema.(json.RawMessage); ok {
				got = string(raw)
			} else if decl.ParametersJSONSchema != nil {
				t.Fatalf("unexpected parametersJsonSchema type %T", decl.ParametersJSONSchema)
			}
			if got != tt.wantParameters {
				t.Errorf("parametersJsonSchema = %q, want %q", got, tt.wantParameters)
			}
		})
	}
}

// TestConvertResponsesMessagesToGeminiContents_FunctionResponsesFollowCallOrder verifies that
// function responses follow the order of the preceding function calls. Vertex strips the
// call/response ids and pairs them by position, so results returned in a different order than
// the calls would otherwise be attached to the wrong call.
func TestConvertResponsesMessagesToGeminiContents_FunctionResponsesFollowCallOrder(t *testing.T) {
	call := func(id string) schemas.ResponsesMessage {
		return schemas.ResponsesMessage{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    schemas.Ptr(id),
				Name:      schemas.Ptr("read_file"),
				Arguments: schemas.Ptr(`{}`),
			},
		}
	}
	output := func(id string) schemas.ResponsesMessage {
		return schemas.ResponsesMessage{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: schemas.Ptr(id),
				Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("result " + id)},
			},
		}
	}
	user := schemas.ResponsesMessage{
		Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("continue")},
	}

	tests := []struct {
		name     string
		messages []schemas.ResponsesMessage
		wantIDs  []string
	}{
		{
			name:     "out of order results are reordered (last message)",
			messages: []schemas.ResponsesMessage{call("a"), call("b"), call("c"), output("c"), output("a"), output("b")},
			wantIDs:  []string{"a", "b", "c"},
		},
		{
			name:     "out of order results are reordered (flushed by next message)",
			messages: []schemas.ResponsesMessage{call("a"), call("b"), output("b"), output("a"), user},
			wantIDs:  []string{"a", "b"},
		},
		{
			name:     "unmatched results keep relative order at the end",
			messages: []schemas.ResponsesMessage{call("a"), call("b"), output("x"), output("b"), output("y"), output("a")},
			wantIDs:  []string{"a", "b", "x", "y"},
		},
		{
			name:     "in order results are unchanged",
			messages: []schemas.ResponsesMessage{call("a"), call("b"), output("a"), output("b")},
			wantIDs:  []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contents, _, err := convertResponsesMessagesToGeminiContents(tt.messages, "gemini-2.5-pro", schemas.Vertex)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var gotIDs []string
			for _, c := range contents {
				for _, p := range c.Parts {
					if p.FunctionResponse != nil {
						gotIDs = append(gotIDs, p.FunctionResponse.ID)
					}
				}
			}
			if len(gotIDs) != len(tt.wantIDs) {
				t.Fatalf("function response ids = %v, want %v", gotIDs, tt.wantIDs)
			}
			for i := range gotIDs {
				if gotIDs[i] != tt.wantIDs[i] {
					t.Fatalf("function response ids = %v, want %v", gotIDs, tt.wantIDs)
				}
			}
		})
	}
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

// A failed run keeps its raw output and carries the outcome on the item status, on every
// path that builds the call; replay reads the status back rather than the log text.
func TestGeminiCodeExecutionFailureKeepsOutput(t *testing.T) {
	failed := &CodeExecutionResult{Outcome: OutcomeFailed, Output: "ZeroDivisionError"}
	assertFailedCall := func(t *testing.T, msg *schemas.ResponsesMessage) {
		t.Helper()
		if msg == nil || msg.ResponsesToolMessage == nil || msg.ResponsesToolMessage.ResponsesCodeInterpreterToolCall == nil {
			t.Fatalf("not a code_interpreter_call: %+v", msg)
		}
		if msg.Status == nil || *msg.Status != "failed" {
			t.Fatalf("status = %v, want failed", msg.Status)
		}
		ci := msg.ResponsesToolMessage.ResponsesCodeInterpreterToolCall
		if len(ci.Outputs) != 1 || ci.Outputs[0].ResponsesCodeInterpreterOutputLogs == nil ||
			ci.Outputs[0].ResponsesCodeInterpreterOutputLogs.Logs != "ZeroDivisionError" {
			t.Fatalf("outputs = %+v, want the raw output", ci.Outputs)
		}
	}

	t.Run("attached to its code", func(t *testing.T) {
		out := convertGeminiCandidatesToResponsesOutput([]*Candidate{{Content: &Content{Role: "model", Parts: []*Part{
			{ExecutableCode: &ExecutableCode{Language: "PYTHON", Code: "1/0"}},
			{CodeExecutionResult: failed},
		}}}})
		assertFailedCall(t, &out[len(out)-1])
	})

	t.Run("constructed without code", func(t *testing.T) {
		out := convertGeminiCandidatesToResponsesOutput([]*Candidate{{Content: &Content{Role: "model", Parts: []*Part{{CodeExecutionResult: failed}}}}})
		assertFailedCall(t, &out[len(out)-1])
	})

	t.Run("streamed", func(t *testing.T) {
		state := &GeminiResponsesStreamState{}
		state.flush()
		events, bErr := (&GenerateContentResponse{Candidates: []*Candidate{{Content: &Content{Role: "model", Parts: []*Part{
			{ExecutableCode: &ExecutableCode{Language: "PYTHON", Code: "1/0"}},
			{CodeExecutionResult: failed},
		}}}}}).ToBifrostResponsesStream(0, state)
		if bErr != nil {
			t.Fatalf("stream: %v", bErr)
		}
		var done *schemas.ResponsesMessage
		for _, ev := range events {
			if ev.Type == schemas.ResponsesStreamResponseTypeOutputItemDone && ev.Item != nil && ev.Item.Type != nil &&
				*ev.Item.Type == schemas.ResponsesMessageTypeCodeInterpreterCall {
				done = ev.Item
			}
		}
		assertFailedCall(t, done)
	})

	call := func(code *string, status, logs string) *schemas.ResponsesMessage {
		return &schemas.ResponsesMessage{
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeCodeInterpreterCall),
			Status: schemas.Ptr(status),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{ResponsesCodeInterpreterToolCall: &schemas.ResponsesCodeInterpreterToolCall{
				Code:    code,
				Outputs: []schemas.ResponsesCodeInterpreterOutput{{ResponsesCodeInterpreterOutputLogs: &schemas.ResponsesCodeInterpreterOutputLogs{Type: "logs", Logs: logs}}},
			}},
		}
	}

	t.Run("replay reads the status, not the log text", func(t *testing.T) {
		parts := geminiPartsFromCodeInterpreterCall(call(schemas.Ptr("print('Error: none')"), "completed", "Error: none\n"))
		if len(parts) != 2 || parts[1].CodeExecutionResult == nil ||
			*parts[1].CodeExecutionResult != (CodeExecutionResult{Outcome: OutcomeOK, Output: "Error: none\n"}) {
			t.Fatalf("a successful run printing Error: was misread: %+v", parts)
		}
		parts = geminiPartsFromCodeInterpreterCall(call(schemas.Ptr("1/0"), "failed", "ZeroDivisionError"))
		if len(parts) != 2 || parts[1].CodeExecutionResult == nil ||
			*parts[1].CodeExecutionResult != (CodeExecutionResult{Outcome: OutcomeFailed, Output: "ZeroDivisionError"}) {
			t.Fatalf("failed run not replayed as OUTCOME_FAILED: %+v", parts)
		}
	})

	t.Run("a silent successful run keeps its result", func(t *testing.T) {
		resp := &GenerateContentResponse{ModelVersion: "gemini-2.5-flash", Candidates: []*Candidate{{FinishReason: FinishReasonStop, Content: &Content{Role: "model", Parts: []*Part{
			{ExecutableCode: &ExecutableCode{Language: "PYTHON", Code: "x = 1"}},
			{CodeExecutionResult: &CodeExecutionResult{Outcome: OutcomeOK}},
		}}}}}
		br := resp.ToResponsesBifrostResponsesResponse()
		want := CodeExecutionResult{Outcome: OutcomeOK}
		if parts := geminiPartsFromCodeInterpreterCall(&br.Output[0]); len(parts) != 2 || parts[1].CodeExecutionResult == nil || *parts[1].CodeExecutionResult != want {
			t.Fatalf("replay lost the empty result: %+v", parts)
		}
		if parts := ToGeminiResponsesResponse(br).Candidates[0].Content.Parts; len(parts) != 2 || parts[1].CodeExecutionResult == nil || *parts[1].CodeExecutionResult != want {
			t.Fatalf("GenAI response lost the empty result: %+v", parts)
		}
	})

	t.Run("a call without outputs replays as code only", func(t *testing.T) {
		msg := call(schemas.Ptr("x = 1"), "completed", "")
		msg.ResponsesToolMessage.ResponsesCodeInterpreterToolCall.Outputs = nil
		if parts := geminiPartsFromCodeInterpreterCall(msg); len(parts) != 1 || parts[0].ExecutableCode == nil {
			t.Fatalf("want only the code, got %+v", parts)
		}
	})

	t.Run("a failed result without code is replayed as prefixed text", func(t *testing.T) {
		parts := geminiPartsFromCodeInterpreterCall(call(nil, "failed", "ZeroDivisionError"))
		if len(parts) != 1 || parts[0].Text != "Error: ZeroDivisionError" {
			t.Fatalf("want the prefixed text, got %+v", parts)
		}
	})

	t.Run("anthropic renders the failure as stderr", func(t *testing.T) {
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		defer cancel()
		br := (&GenerateContentResponse{ModelVersion: "gemini-2.5-flash", Candidates: []*Candidate{{FinishReason: FinishReasonStop, Content: &Content{Role: "model", Parts: []*Part{
			{ExecutableCode: &ExecutableCode{Language: "PYTHON", Code: "1/0"}},
			{CodeExecutionResult: failed},
		}}}}}).ToResponsesBifrostResponsesResponse()
		var result *anthropic.AnthropicContentBlock
		blocks := anthropic.ToAnthropicResponsesResponse(ctx, br).Content
		for i := range blocks {
			if blocks[i].Type == anthropic.AnthropicContentBlockTypeCodeExecutionToolResult {
				result = &blocks[i]
			}
		}
		if result == nil || result.Content == nil || result.Content.ContentObj == nil {
			b, _ := schemas.Marshal(blocks)
			t.Fatalf("no code_execution_tool_result content: %s", b)
		}
		inner := result.Content.ContentObj
		if inner.Stderr == nil || *inner.Stderr != "ZeroDivisionError" || inner.Stdout != nil || inner.ReturnCode == nil || *inner.ReturnCode == 0 {
			b, _ := schemas.Marshal(inner)
			t.Fatalf("want stderr ZeroDivisionError with a non-zero return code, got %s", b)
		}
	})
}

// Streaming previously dropped both parts on the floor, so a streamed code execution
// reached the client as nothing at all.
// A code_interpreter_call replayed as input must reach Gemini as the executableCode and
// codeExecutionResult parts it came from; it carries no content, so it used to be dropped.
func TestGeminiCodeInterpreterCallReplaysAsCodeParts(t *testing.T) {
	user := func(text string) schemas.ResponsesMessage {
		return schemas.ResponsesMessage{Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr(text)}}
	}
	replay := func(t *testing.T, call schemas.ResponsesMessage) []*Part {
		contents, _, err := convertResponsesMessagesToGeminiContents([]schemas.ResponsesMessage{user("run it"), call, user("what did it print?")}, "gemini-2.5-flash", schemas.Gemini)
		if err != nil {
			t.Fatalf("convert: %v", err)
		}
		if len(contents) != 3 || contents[1].Role != "model" {
			t.Fatalf("want user, model, user; got %+v", contents)
		}
		return contents[1].Parts
	}

	t.Run("round trip of a Gemini response", func(t *testing.T) {
		resp := &GenerateContentResponse{Candidates: []*Candidate{{Content: &Content{Role: "model", Parts: []*Part{
			{ExecutableCode: &ExecutableCode{Language: "PYTHON", Code: "print(6*7)"}},
			{CodeExecutionResult: &CodeExecutionResult{Outcome: OutcomeOK, Output: "42\n"}},
		}}}}}
		output := resp.ToResponsesBifrostResponsesResponse().Output
		if len(output) != 1 {
			t.Fatalf("want one code_interpreter_call, got %+v", output)
		}
		parts := replay(t, output[0])
		if len(parts) != 2 || parts[0].ExecutableCode == nil || parts[0].ExecutableCode.Code != "print(6*7)" ||
			parts[1].CodeExecutionResult == nil || *parts[1].CodeExecutionResult != (CodeExecutionResult{Outcome: OutcomeOK, Output: "42\n"}) {
			t.Fatalf("code parts not rebuilt: %+v", parts)
		}
	})

	t.Run("failed run keeps its outcome", func(t *testing.T) {
		resp := &GenerateContentResponse{Candidates: []*Candidate{{Content: &Content{Role: "model", Parts: []*Part{
			{ExecutableCode: &ExecutableCode{Language: "PYTHON", Code: "1/0"}},
			{CodeExecutionResult: &CodeExecutionResult{Outcome: OutcomeFailed, Output: "ZeroDivisionError"}},
		}}}}}
		parts := replay(t, resp.ToResponsesBifrostResponsesResponse().Output[0])
		if len(parts) != 2 || parts[1].CodeExecutionResult == nil ||
			*parts[1].CodeExecutionResult != (CodeExecutionResult{Outcome: OutcomeFailed, Output: "ZeroDivisionError"}) {
			t.Fatalf("failed result not rebuilt: %+v", parts)
		}
	})

	t.Run("OpenAI-shaped call without a role", func(t *testing.T) {
		call := schemas.ResponsesMessage{
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeCodeInterpreterCall),
			Status: schemas.Ptr("completed"),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{ResponsesCodeInterpreterToolCall: &schemas.ResponsesCodeInterpreterToolCall{
				Code:        schemas.Ptr("print('a7c3e91f')"),
				ContainerID: "cntr_1",
				Outputs:     []schemas.ResponsesCodeInterpreterOutput{{ResponsesCodeInterpreterOutputLogs: &schemas.ResponsesCodeInterpreterOutputLogs{Type: "logs", Logs: "a7c3e91f\n"}}},
			}},
		}
		parts := replay(t, call)
		if len(parts) != 2 || parts[1].CodeExecutionResult == nil || parts[1].CodeExecutionResult.Output != "a7c3e91f\n" {
			t.Fatalf("OpenAI call not rebuilt: %+v", parts)
		}
	})
}

// The GenAI and Anthropic integrations convert Gemini's code execution through Responses
// items and back; each direction must keep the code and its result.
func TestGeminiCodeExecutionThroughIntegrations(t *testing.T) {
	codeParts := []*Part{
		{ExecutableCode: &ExecutableCode{Language: "PYTHON", Code: "print(6*7)"}},
		{CodeExecutionResult: &CodeExecutionResult{Outcome: OutcomeOK, Output: "42\n"}},
	}
	parts := append(append([]*Part{{Text: "I'll compute it."}}, codeParts...), &Part{Text: "The answer is 42."})
	response := func(parts []*Part) *schemas.BifrostResponsesResponse {
		return (&GenerateContentResponse{ModelVersion: "gemini-2.5-flash", Candidates: []*Candidate{{
			FinishReason: FinishReasonStop, Content: &Content{Role: "model", Parts: parts},
		}}}).ToResponsesBifrostResponsesResponse()
	}
	countCode := func(parts []*Part) (code, result int) {
		for _, p := range parts {
			if p.ExecutableCode != nil {
				code++
			}
			if p.CodeExecutionResult != nil {
				result++
			}
		}
		return code, result
	}

	t.Run("genai non-streaming keeps the code parts", func(t *testing.T) {
		out := ToGeminiResponsesResponse(response(parts)).Candidates[0].Content.Parts
		if len(out) != 4 || out[1].ExecutableCode == nil || out[1].ExecutableCode.Code != "print(6*7)" ||
			out[2].CodeExecutionResult == nil || out[2].CodeExecutionResult.Output != "42\n" {
			t.Fatalf("want text, executableCode, codeExecutionResult, text; got %+v", out)
		}
	})

	t.Run("genai non-streaming with web search does not duplicate them", func(t *testing.T) {
		searched := append([]*Part{
			{ToolCall: &ToolCall{ToolType: "GOOGLE_SEARCH_WEB", ID: "s1"}},
			{ToolResponse: &ToolResponse{ToolType: "GOOGLE_SEARCH_WEB", ID: "s1"}},
		}, parts...)
		out := ToGeminiResponsesResponse(response(searched)).Candidates[0].Content.Parts
		if code, result := countCode(out); code != 1 || result != 1 {
			t.Fatalf("want one executableCode and one codeExecutionResult, got %d and %d: %+v", code, result, out)
		}
	})

	t.Run("genai streaming emits the code parts when the call closes", func(t *testing.T) {
		state := &GeminiResponsesStreamState{}
		state.flush()
		events, bErr := (&GenerateContentResponse{ModelVersion: "gemini-2.5-flash", Candidates: []*Candidate{{
			Content: &Content{Role: "model", Parts: codeParts},
		}}}).ToBifrostResponsesStream(0, state)
		if bErr != nil {
			t.Fatalf("ToBifrostResponsesStream: %v", bErr)
		}
		out := NewBifrostToGeminiStreamState()
		var streamed []*Part
		for _, ev := range events {
			if g := ToGeminiResponsesStreamResponse(ev, out); g != nil && len(g.Candidates) > 0 && g.Candidates[0].Content != nil {
				streamed = append(streamed, g.Candidates[0].Content.Parts...)
			}
		}
		if code, result := countCode(streamed); code != 1 || result != 1 {
			t.Fatalf("want the code and result streamed once each, got %d and %d: %+v", code, result, streamed)
		}
	})

	t.Run("genai request replays native code parts", func(t *testing.T) {
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		defer cancel()
		req := (&GeminiGenerationRequest{Model: "gemini-2.5-flash", Contents: []Content{
			{Role: "user", Parts: []*Part{{Text: "compute 6*7"}}},
			{Role: "model", Parts: codeParts},
			{Role: "user", Parts: []*Part{{Text: "what did it print?"}}},
		}}).ToBifrostResponsesRequest(ctx)
		contents, _, err := convertResponsesMessagesToGeminiContents(req.Input, "gemini-2.5-flash", schemas.Gemini)
		if err != nil {
			t.Fatalf("convert: %v", err)
		}
		if len(contents) != 3 {
			t.Fatalf("want user, model, user; got %+v", contents)
		}
		if code, result := countCode(contents[1].Parts); code != 1 || result != 1 {
			t.Fatalf("code parts lost on the way to Gemini: %+v", contents[1].Parts)
		}
	})

	t.Run("anthropic non-streaming renders code_execution blocks", func(t *testing.T) {
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		defer cancel()
		blocks := anthropic.ToAnthropicResponsesResponse(ctx, response(parts)).Content
		var use, result *anthropic.AnthropicContentBlock
		for i := range blocks {
			switch blocks[i].Type {
			case anthropic.AnthropicContentBlockTypeServerToolUse:
				use = &blocks[i]
			case anthropic.AnthropicContentBlockTypeCodeExecutionToolResult:
				result = &blocks[i]
			}
		}
		if use == nil || result == nil || result.ToolUseID == nil || use.ID == nil || *result.ToolUseID != *use.ID {
			t.Fatalf("want a server_tool_use and its code_execution_tool_result, got %+v", blocks)
		}
		if b, _ := schemas.Marshal(blocks); !strings.Contains(string(b), `print(6*7)`) || !strings.Contains(string(b), `42\n`) {
			t.Fatalf("code or stdout missing: %s", b)
		}
	})
}

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
	// The call is announced while Google is still running it; only the close knows the outcome.
	if added.Item.Status == nil || *added.Item.Status != "in_progress" {
		t.Fatalf("added status = %v, want in_progress", added.Item.Status)
	}
	if done.Item.Status == nil || *done.Item.Status != "completed" {
		t.Fatalf("done status = %v, want completed", done.Item.Status)
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

// Text after code execution opens a new message item; writing it onto the
// closed first one drops the answer from response.completed.
func TestGeminiResponsesStreamTextAfterCodeExecutionOpensNewItem(t *testing.T) {
	state := &GeminiResponsesStreamState{}
	state.flush()
	chunks := []*GenerateContentResponse{
		{Candidates: []*Candidate{{Content: &Content{Role: "model", Parts: []*Part{{Text: "Let me compute that."}}}}}},
		{Candidates: []*Candidate{{Content: &Content{Role: "model", Parts: []*Part{
			{ExecutableCode: &ExecutableCode{Language: "PYTHON", Code: "print(6*7)"}},
			{CodeExecutionResult: &CodeExecutionResult{Outcome: OutcomeOK, Output: "42\n"}},
		}}}}},
		{Candidates: []*Candidate{{
			Content:      &Content{Role: "model", Parts: []*Part{{Text: "The answer is 42."}}},
			FinishReason: FinishReasonStop,
		}}},
	}
	var events []*schemas.BifrostResponsesStreamResponse
	for _, chunk := range chunks {
		chunk.ModelVersion = "gemini-2.5-flash"
		out, bErr := chunk.ToBifrostResponsesStream(len(events), state)
		if bErr != nil {
			t.Fatalf("ToBifrostResponsesStream error: %v", bErr)
		}
		events = append(events, out...)
	}

	closed := map[int]bool{}
	var completed *schemas.BifrostResponsesStreamResponse
	for _, event := range events {
		switch event.Type {
		case schemas.ResponsesStreamResponseTypeOutputItemDone:
			closed[*event.OutputIndex] = true
		case schemas.ResponsesStreamResponseTypeOutputTextDelta:
			if closed[*event.OutputIndex] {
				t.Fatalf("delta %q targets output_index %d, which is already done", *event.Delta, *event.OutputIndex)
			}
		case schemas.ResponsesStreamResponseTypeCompleted:
			completed = event
		}
	}
	if completed == nil || completed.Response == nil {
		t.Fatalf("no response.completed among %d events", len(events))
	}
	output := completed.Response.Output
	if len(output) != 3 {
		t.Fatalf("response.completed has %d output items, want message, code_interpreter_call, message", len(output))
	}
	last := output[2]
	if last.Type == nil || *last.Type != schemas.ResponsesMessageTypeMessage || last.Content == nil ||
		len(last.Content.ContentBlocks) == 0 || last.Content.ContentBlocks[0].Text == nil ||
		*last.Content.ContentBlocks[0].Text != "The answer is 42." {
		t.Fatalf("last output item = %+v, want the answer message", last)
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
