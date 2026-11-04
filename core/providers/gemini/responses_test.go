package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/internal/memtest"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// The stream loop must treat a refused prompt's response.failed as the final chunk: sent with
// the stream-end indicator (post-hooks finalize logging and the trace span on it) and followed
// by nothing, since the finalizer has no terminal left to emit.
func TestGeminiResponsesStreamPromptBlockedEndsOnFailed(t *testing.T) {
	var finalTypes []schemas.ResponsesStreamResponseType
	postHook := func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		if final, _ := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator).(bool); final && result != nil && result.ResponsesStreamResponse != nil {
			finalTypes = append(finalTypes, result.ResponsesStreamResponse.Type)
		}
		return result, err
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + geminiPromptBlockedBody + "\n\n"))
	}))
	defer ts.Close()

	stream, bErr := HandleGeminiResponsesStream(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		&fasthttp.Client{}, ts.URL+"/models/gemini-4-argon:streamGenerateContent?alt=sse", []byte(`{}`),
		map[string]string{"Accept": "text/event-stream"}, nil, 30, false, false, schemas.Vertex, "gemini-4-argon",
		postHook, nil, testNoopLogger{}, func(context.Context) {})
	if bErr != nil {
		t.Fatalf("stream setup failed: %v", bErr)
	}
	var got []schemas.ResponsesStreamResponseType
	for chunk := range stream {
		if chunk.BifrostError != nil {
			t.Fatalf("unexpected stream error: %+v", chunk.BifrostError.Error)
		}
		if chunk.BifrostResponsesStreamResponse != nil {
			got = append(got, chunk.BifrostResponsesStreamResponse.Type)
		}
	}
	want := []schemas.ResponsesStreamResponseType{
		schemas.ResponsesStreamResponseTypeCreated,
		schemas.ResponsesStreamResponseTypeInProgress,
		schemas.ResponsesStreamResponseTypeFailed,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(finalTypes, []schemas.ResponsesStreamResponseType{schemas.ResponsesStreamResponseTypeFailed}) {
		t.Fatalf("chunks sent with the stream-end indicator = %v, want only response.failed", finalTypes)
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

// geminiPromptBlockedBody is the raw Vertex body for a prompt refused before generation:
// no candidates, the reason carried only by promptFeedback.
const geminiPromptBlockedBody = `{"promptFeedback":{"blockReason":"SAFETY","blockReasonMessage":"The prompt is blocked due to safety"},"usageMetadata":{"trafficType":"ON_DEMAND"},"modelVersion":"gemini-4-argon"}`

// A blocked prompt used to normalize to status "completed" with empty output and no error,
// indistinguishable from an empty answer. It reports failed with OpenAI's invalid_prompt code
// (content_filter is not a valid ResponseError code) and keeps stop_reason content_filter.
func TestGeminiResponsesPromptBlockedReportsFailed(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantStatus  string
		wantMessage string
	}{
		{name: "message", body: geminiPromptBlockedBody, wantStatus: schemas.ResponsesResponseStatusFailed, wantMessage: "The prompt is blocked due to safety"},
		{name: "reason only", body: `{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT"},"modelVersion":"gemini-4-argon"}`, wantStatus: schemas.ResponsesResponseStatusFailed, wantMessage: "PROHIBITED_CONTENT"},
		{name: "unspecified reason is not a block", body: `{"promptFeedback":{"blockReason":"BLOCK_REASON_UNSPECIFIED"},"modelVersion":"gemini-4-argon"}`, wantStatus: schemas.ResponsesResponseStatusCompleted},
		{name: "feedback beside a candidate is not a block", body: `{"promptFeedback":{"safetyRatings":[{"category":"HARM_CATEGORY_HARASSMENT","probability":"NEGLIGIBLE"}]},"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"modelVersion":"gemini-4-argon"}`, wantStatus: schemas.ResponsesResponseStatusCompleted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var geminiResp GenerateContentResponse
			if err := sonic.Unmarshal([]byte(tt.body), &geminiResp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			resp := geminiResp.ToResponsesBifrostResponsesResponse().WithDefaults()
			if resp.Status == nil || *resp.Status != tt.wantStatus {
				t.Fatalf("status = %s, want %q", derefStatus(resp.Status), tt.wantStatus)
			}
			if tt.wantMessage == "" {
				if resp.Error != nil {
					t.Fatalf("error = %+v, want nil", resp.Error)
				}
				return
			}
			if resp.Error == nil || resp.Error.Code != "invalid_prompt" || resp.Error.Message != tt.wantMessage {
				t.Fatalf("error = %+v, want code invalid_prompt message %q", resp.Error, tt.wantMessage)
			}
			if resp.StopReason == nil || *resp.StopReason != "content_filter" {
				t.Fatalf("stop_reason = %s, want content_filter", derefStatus(resp.StopReason))
			}
			if len(resp.Output) != 0 {
				t.Fatalf("output = %d items, want none", len(resp.Output))
			}
		})
	}
}

// The streamed block chunk must close the stream itself with a response.failed terminal
// carrying the reason, rather than leaving the finalizer to report an unexplained incomplete.
func TestGeminiResponsesStreamPromptBlockedReportsFailed(t *testing.T) {
	state := acquireGeminiResponsesStreamState()
	defer releaseGeminiResponsesStreamState(state)
	var chunk GenerateContentResponse
	if err := sonic.Unmarshal([]byte(geminiPromptBlockedBody), &chunk); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	events, bErr := chunk.ToBifrostResponsesStream(0, state)
	if bErr != nil {
		t.Fatalf("ToBifrostResponsesStream error: %v", bErr)
	}
	if len(events) == 0 {
		t.Fatal("no events emitted for the blocked chunk")
	}
	terminal := events[len(events)-1]
	if terminal.Type != schemas.ResponsesStreamResponseTypeFailed {
		t.Fatalf("terminal event type = %q, want %q", terminal.Type, schemas.ResponsesStreamResponseTypeFailed)
	}
	if terminal.Response == nil || terminal.Response.Status == nil || *terminal.Response.Status != schemas.ResponsesResponseStatusFailed {
		t.Fatalf("terminal status = %+v, want failed", terminal.Response)
	}
	if terminal.Response.Error == nil || terminal.Response.Error.Code != "invalid_prompt" || terminal.Response.Error.Message != "The prompt is blocked due to safety" {
		t.Fatalf("terminal error = %+v, want invalid_prompt with the block message", terminal.Response.Error)
	}
	if terminal.Response.StopReason == nil || *terminal.Response.StopReason != "content_filter" {
		t.Fatalf("terminal stop_reason = %s, want content_filter", derefStatus(terminal.Response.StopReason))
	}
	if extra := FinalizeGeminiResponsesStream(state, chunk.UsageMetadata, len(events)); len(extra) != 0 {
		t.Fatalf("finalize emitted %d events after the terminal, want none", len(extra))
	}
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

// Gemini-native retrieval tools (retrieval.vertexAiSearch, retrieval.vertexRagStore,
// googleSearchRetrieval, ...) have no neutral Responses tool. A /genai request routed
// to Vertex (or a bare model) goes through ToBifrostResponsesRequest and back out
// through ToGeminiResponsesRequest, and these tools used to vanish on the way, so the
// model answered ungrounded with a 200. They must reach the outbound request intact,
// alongside the neutral tools, on every retry attempt.
func TestGenAINativeRetrievalToolsSurviveNeutralRoundTrip(t *testing.T) {
	threshold := float32(0.3)
	in := &GeminiGenerationRequest{
		Model:    "vertex/gemini-2.5-flash",
		Contents: []Content{{Role: "user", Parts: []*Part{{Text: "What does the index say?"}}}},
		Tools: []Tool{
			{Retrieval: &Retrieval{VertexAISearch: &VertexAISearch{Datastore: "projects/p/locations/global/collections/default_collection/dataStores/ds"}}},
			{Retrieval: &Retrieval{VertexRAGStore: &VertexRAGStore{RAGResources: []*VertexRAGStoreRAGResource{{RAGCorpus: "projects/p/locations/us-central1/ragCorpora/c"}}}}},
			{GoogleSearchRetrieval: &GoogleSearchRetrieval{DynamicRetrievalConfig: &DynamicRetrievalConfig{Mode: "MODE_DYNAMIC", DynamicThreshold: &threshold}}},
			{Retrieval: &Retrieval{}},
			{CodeExecution: &ToolCodeExecution{}},
		},
	}
	bifrostReq := in.ToBifrostResponsesRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline))
	if bifrostReq == nil || bifrostReq.Params == nil {
		t.Fatalf("ToBifrostResponsesRequest returned no params")
	}

	for attempt := 1; attempt <= 2; attempt++ {
		out, err := ToGeminiResponsesRequest(nil, bifrostReq)
		if err != nil {
			t.Fatalf("attempt %d: ToGeminiResponsesRequest error: %v", attempt, err)
		}
		var vertexSearch, ragStore, emptyRetrieval, searchRetrieval, codeExecution int
		for _, tool := range out.Tools {
			if tool.Retrieval != nil {
				switch {
				case tool.Retrieval.VertexAISearch != nil:
					if tool.Retrieval.VertexAISearch.Datastore != "projects/p/locations/global/collections/default_collection/dataStores/ds" {
						t.Fatalf("attempt %d: datastore = %q", attempt, tool.Retrieval.VertexAISearch.Datastore)
					}
					vertexSearch++
				case tool.Retrieval.VertexRAGStore != nil:
					ragStore++
				default:
					emptyRetrieval++
				}
			}
			if tool.GoogleSearchRetrieval != nil {
				cfg := tool.GoogleSearchRetrieval.DynamicRetrievalConfig
				if cfg == nil || cfg.Mode != "MODE_DYNAMIC" || cfg.DynamicThreshold == nil || *cfg.DynamicThreshold != threshold {
					t.Fatalf("attempt %d: googleSearchRetrieval config lost: %+v", attempt, cfg)
				}
				searchRetrieval++
			}
			if tool.CodeExecution != nil {
				codeExecution++
			}
		}
		if vertexSearch != 1 || ragStore != 1 || emptyRetrieval != 1 || searchRetrieval != 1 || codeExecution != 1 {
			t.Fatalf("attempt %d: tools = %+v, want vertexAiSearch, vertexRagStore, bare retrieval, googleSearchRetrieval and codeExecution each once", attempt, out.Tools)
		}
		for key := range out.GetExtraParams() {
			if strings.Contains(key, "tool") {
				t.Fatalf("attempt %d: carrier key %q leaked onto the wire", attempt, key)
			}
		}
	}
}

// A native tool's sub-fields Bifrost does not model (retrieval.dynamicRetrievalConfig is not
// a real retrieval field, so Google must be the one to reject it) used to be dropped by the
// typed copy, turning an invalid request into an ungrounded 200 with {"retrieval":{}}.
// Every native-only tool entry must reach the egress body exactly as the client sent it,
// including googleSearchRetrieval.dynamicRetrievalConfig, which Gemini does define.
func TestGenAINativeToolUnknownSubFieldsReachEgressUnchanged(t *testing.T) {
	retrieval := `{"dynamicRetrievalConfig":{"mode":"MODE_DYNAMIC","dynamicThreshold":0.7}}`
	searchRetrieval := `{"dynamicRetrievalConfig":{"mode":"MODE_DYNAMIC","dynamicThreshold":0.3},"futureKnob":{"z":1,"a":2}}`
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[` +
		`{"retrieval":` + retrieval + `},` +
		`{"googleSearchRetrieval":` + searchRetrieval + `},` +
		`{"urlContext":{"futureKnob":true}},` +
		`{"functionDeclarations":[{"name":"lookup","description":"look up"}]}]}`

	var in GeminiGenerationRequest
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		t.Fatalf("decode ingress body: %v", err)
	}
	in.Model = "vertex/gemini-2.5-flash"
	bifrostReq := in.ToBifrostResponsesRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline))
	if bifrostReq == nil || bifrostReq.Params == nil {
		t.Fatalf("ToBifrostResponsesRequest returned no params")
	}

	for attempt := 1; attempt <= 2; attempt++ {
		out, err := ToGeminiResponsesRequest(nil, bifrostReq)
		if err != nil {
			t.Fatalf("attempt %d: ToGeminiResponsesRequest error: %v", attempt, err)
		}
		wire, err := providerUtils.MarshalProviderRequest(out)
		if err != nil {
			t.Fatalf("attempt %d: marshal egress: %v", attempt, err)
		}
		var gotRetrieval, gotSearchRetrieval, gotURLContext, gotFunction string
		for _, tool := range gjson.GetBytes(wire, "tools").Array() {
			if v := tool.Get("retrieval"); v.Exists() {
				gotRetrieval = v.Raw
			}
			if v := tool.Get("googleSearchRetrieval"); v.Exists() {
				gotSearchRetrieval = v.Raw
			}
			if v := tool.Get("urlContext"); v.Exists() {
				gotURLContext = v.Raw
			}
			if v := tool.Get("functionDeclarations.0.name"); v.Exists() {
				gotFunction = v.String()
			}
		}
		if gotRetrieval != retrieval {
			t.Fatalf("attempt %d: retrieval = %s, want %s (body %s)", attempt, gotRetrieval, retrieval, wire)
		}
		if gotSearchRetrieval != searchRetrieval {
			t.Fatalf("attempt %d: googleSearchRetrieval = %s, want %s (body %s)", attempt, gotSearchRetrieval, searchRetrieval, wire)
		}
		if gotURLContext != `{"futureKnob":true}` {
			t.Fatalf("attempt %d: urlContext = %s, want {\"futureKnob\":true} (body %s)", attempt, gotURLContext, wire)
		}
		if gotFunction != "lookup" {
			t.Fatalf("attempt %d: function declaration lost (body %s)", attempt, wire)
		}
	}

	// The carrier must also survive a JSON round trip of ExtraParams (the in-memory slice is
	// not the only form it can arrive in). Decoding into interface{} already loses key order,
	// so this form is compared by value.
	carried, err := providerUtils.MarshalSorted(bifrostReq.Params.ExtraParams[geminiNativeToolsExtraParamKey])
	if err != nil {
		t.Fatalf("marshal carrier: %v", err)
	}
	var decoded interface{}
	if err := json.Unmarshal(carried, &decoded); err != nil {
		t.Fatalf("decode carrier: %v", err)
	}
	wire, err := providerUtils.MarshalSorted(extractGeminiNativeTools(decoded))
	if err != nil {
		t.Fatalf("marshal decoded carrier: %v", err)
	}
	var gotValue, wantValue interface{}
	if err := json.Unmarshal([]byte(gjson.GetBytes(wire, "0.retrieval").Raw), &gotValue); err != nil {
		t.Fatalf("JSON-decoded carrier lost retrieval: %s", wire)
	}
	if err := json.Unmarshal([]byte(retrieval), &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON-decoded carrier retrieval = %v, want %v", gotValue, wantValue)
	}
}

// urlContextMetadata has no field in Bifrost's OpenAI-shaped Responses schema. A /genai
// generateContent answer routed through the Responses path came back with groundingMetadata
// but without candidates[0].urlContextMetadata, so a client could not see which URLs the
// urlContext tool fetched. It must survive Gemini -> Bifrost Responses -> GenAI, in memory
// and after the ProviderExtraFields have been through JSON.
func TestGeminiURLContextMetadataSurvivesResponsesRoundTrip(t *testing.T) {
	native := &GenerateContentResponse{
		ResponseID:   "resp-url",
		ModelVersion: "gemini-2.5-flash",
		Candidates: []*Candidate{{
			Content:      &Content{Role: "model", Parts: []*Part{{Text: "The page is about cats."}}},
			FinishReason: FinishReasonStop,
			URLContextMetadata: &URLContextMetadata{URLMetadata: []*URLMetadata{
				{RetrievedURL: "https://example.com/cats", URLRetrievalStatus: "URL_RETRIEVAL_STATUS_SUCCESS"},
			}},
		}},
	}

	check := func(t *testing.T, bifrostResp *schemas.BifrostResponsesResponse) {
		t.Helper()
		out := ToGeminiResponsesResponse(bifrostResp)
		if out == nil || len(out.Candidates) == 0 {
			t.Fatalf("no candidates restored")
		}
		meta := out.Candidates[0].URLContextMetadata
		if meta == nil || len(meta.URLMetadata) != 1 {
			t.Fatalf("candidates[0].urlContextMetadata = %+v, want one entry", meta)
		}
		if meta.URLMetadata[0].RetrievedURL != "https://example.com/cats" || meta.URLMetadata[0].URLRetrievalStatus != "URL_RETRIEVAL_STATUS_SUCCESS" {
			t.Fatalf("urlMetadata = %+v", meta.URLMetadata[0])
		}
	}

	t.Run("in memory", func(t *testing.T) {
		check(t, native.ToResponsesBifrostResponsesResponse())
	})
	t.Run("json decoded extra fields", func(t *testing.T) {
		bifrostResp := native.ToResponsesBifrostResponsesResponse()
		raw, err := json.Marshal(bifrostResp.ProviderExtraFields)
		if err != nil {
			t.Fatalf("marshal extra fields: %v", err)
		}
		var decoded map[string]interface{}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("decode extra fields: %v", err)
		}
		bifrostResp.ProviderExtraFields = decoded
		check(t, bifrostResp)
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

// BenchmarkGenAIToolsResponsesEgress measures a /genai request with tools decoded, carried
// through the Responses path and marshalled for the provider: "function" is the common case
// that must not pay for native-tool fidelity, "native" carries retrieval/urlContext entries.
func BenchmarkGenAIToolsResponsesEgress(b *testing.B) {
	bodies := map[string][]byte{
		"function": []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"lookup","description":"look up","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}]}]}`),
		"native":   []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"retrieval":{"vertexAiSearch":{"datastore":"projects/p/locations/global/collections/default_collection/dataStores/ds"}}},{"googleSearchRetrieval":{"dynamicRetrievalConfig":{"mode":"MODE_DYNAMIC","dynamicThreshold":0.3}}},{"urlContext":{}}]}`),
	}
	for _, name := range []string{"function", "native"} {
		body := bodies[name]
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var in GeminiGenerationRequest
				if err := sonic.Unmarshal(body, &in); err != nil {
					b.Fatal(err)
				}
				in.Model = "gemini-2.5-flash"
				bifrostReq := in.ToBifrostResponsesRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline))
				out, err := ToGeminiResponsesRequest(nil, bifrostReq)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := providerUtils.MarshalProviderRequest(out); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkGeminiResponsesResponseRoundTrip measures Gemini -> Bifrost Responses -> GenAI for
// a plain answer (common case) and one carrying urlContextMetadata.
func BenchmarkGeminiResponsesResponseRoundTrip(b *testing.B) {
	plain := &GenerateContentResponse{
		ResponseID:   "resp",
		ModelVersion: "gemini-2.5-flash",
		Candidates: []*Candidate{{
			Content:      &Content{Role: "model", Parts: []*Part{{Text: "hello"}}},
			FinishReason: FinishReasonStop,
		}},
	}
	withURL := &GenerateContentResponse{
		ResponseID:   "resp",
		ModelVersion: "gemini-2.5-flash",
		Candidates: []*Candidate{{
			Content:      &Content{Role: "model", Parts: []*Part{{Text: "hello"}}},
			FinishReason: FinishReasonStop,
			URLContextMetadata: &URLContextMetadata{URLMetadata: []*URLMetadata{
				{RetrievedURL: "https://example.com", URLRetrievalStatus: "URL_RETRIEVAL_STATUS_SUCCESS"},
			}},
		}},
	}
	for name, resp := range map[string]*GenerateContentResponse{"plain": plain, "urlContext": withURL} {
		resp := resp
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = ToGeminiResponsesResponse(resp.ToResponsesBifrostResponsesResponse())
			}
		})
	}
}

// response.function_call_arguments.done is {type, sequence_number, item_id, output_index,
// arguments}. It used to carry a partial item with no type, and strict Responses clients
// (opencode v2) validate item.type on every event, so every Gemini tool turn aborted. The
// genai and anthropic subtests pin that the reverse converters still recover each call
// from output_item.added once the done event stops carrying it.
func TestGeminiResponsesStreamFunctionCallArgumentsDoneShape(t *testing.T) {
	sig := []byte("gemini-thought-signature")
	state := &GeminiResponsesStreamState{}
	state.flush()
	events, bErr := (&GenerateContentResponse{ModelVersion: "gemini-3-flash-preview", Candidates: []*Candidate{{
		Content: &Content{Role: "model", Parts: []*Part{
			{Text: "Writing the probe."},
			{FunctionCall: &FunctionCall{ID: "call_a", Name: "write_file", Args: json.RawMessage(`{"path":"/tmp/probe.txt","content":"OK"}`)}, ThoughtSignature: sig},
			{FunctionCall: &FunctionCall{ID: "call_b", Name: "read_file", Args: json.RawMessage(`{"path":"/tmp/probe.txt"}`)}},
		}},
		FinishReason: FinishReasonStop,
	}}}).ToBifrostResponsesStream(0, state)
	if bErr != nil {
		t.Fatalf("ToBifrostResponsesStream: %v", bErr)
	}
	wantArgs := []string{`{"path":"/tmp/probe.txt","content":"OK"}`, `{"path":"/tmp/probe.txt"}`}

	// /v1/responses marshals the event as is; /openai/v1/responses marshals WithDefaults().
	for route, render := range map[string]func(*schemas.BifrostResponsesStreamResponse) *schemas.BifrostResponsesStreamResponse{
		"v1 responses":        func(ev *schemas.BifrostResponsesStreamResponse) *schemas.BifrostResponsesStreamResponse { return ev },
		"openai v1 responses": (*schemas.BifrostResponsesStreamResponse).WithDefaults,
	} {
		t.Run(route, func(t *testing.T) {
			addedIDs := map[int64]string{}
			var doneArgs []string
			for _, ev := range events {
				wire, err := schemas.Marshal(render(ev))
				if err != nil {
					t.Fatalf("marshal %s: %v", ev.Type, err)
				}
				if item := gjson.GetBytes(wire, "item"); item.Exists() && (item.Get("type").Type != gjson.String || item.Get("type").Str == "") {
					t.Fatalf("%s carries an item without a string type: %s", ev.Type, wire)
				}
				if ev.Type == schemas.ResponsesStreamResponseTypeOutputItemAdded && gjson.GetBytes(wire, "item.type").Str == "function_call" {
					addedIDs[gjson.GetBytes(wire, "output_index").Int()] = gjson.GetBytes(wire, "item.id").Str
				}
				if ev.Type != schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone {
					continue
				}
				if gjson.GetBytes(wire, "item").Exists() {
					t.Fatalf("function_call_arguments.done must not carry an item: %s", wire)
				}
				index := gjson.GetBytes(wire, "output_index")
				if !index.Exists() || gjson.GetBytes(wire, "item_id").Str == "" || gjson.GetBytes(wire, "item_id").Str != addedIDs[index.Int()] {
					t.Fatalf("done event does not point at its output_item.added (%v): %s", addedIDs, wire)
				}
				doneArgs = append(doneArgs, gjson.GetBytes(wire, "arguments").Str)
			}
			if !reflect.DeepEqual(doneArgs, wantArgs) {
				t.Fatalf("done arguments = %q, want %q", doneArgs, wantArgs)
			}
		})
	}

	t.Run("genai", func(t *testing.T) {
		out := NewBifrostToGeminiStreamState()
		var calls []*Part
		for _, ev := range events {
			g := ToGeminiResponsesStreamResponse(ev, out)
			if g == nil || len(g.Candidates) == 0 || g.Candidates[0].Content == nil {
				continue
			}
			for _, p := range g.Candidates[0].Content.Parts {
				if p.FunctionCall != nil {
					calls = append(calls, p)
				}
			}
		}
		if len(calls) != 2 {
			t.Fatalf("want two functionCall parts, got %+v", calls)
		}
		for i, want := range []struct {
			name, id string
			sig      []byte
		}{{"write_file", "call_a", sig}, {"read_file", "call_b", nil}} {
			fc := calls[i].FunctionCall
			if fc.Name != want.name || !strings.HasPrefix(fc.ID, want.id) || string(fc.Args) != wantArgs[i] {
				t.Fatalf("call %d = %s %s %s, want %s %s… %s", i, fc.Name, fc.ID, fc.Args, want.name, want.id, wantArgs[i])
			}
			if !bytes.Equal(calls[i].ThoughtSignature, want.sig) {
				t.Fatalf("call %d thoughtSignature = %q, want %q", i, calls[i].ThoughtSignature, want.sig)
			}
		}
	})

	t.Run("anthropic", func(t *testing.T) {
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		defer cancel()
		names := map[int]string{}
		inputs := map[int]string{}
		var order []int
		for _, ev := range events {
			for _, out := range anthropic.ToAnthropicResponsesStreamResponse(ctx, ev) {
				if out.Index == nil {
					continue
				}
				if out.ContentBlock != nil && out.ContentBlock.Type == anthropic.AnthropicContentBlockTypeToolUse && out.ContentBlock.Name != nil {
					names[*out.Index] = *out.ContentBlock.Name
					order = append(order, *out.Index)
				}
				if out.Delta != nil && out.Delta.PartialJSON != nil {
					inputs[*out.Index] += *out.Delta.PartialJSON
				}
			}
		}
		if len(order) != 2 {
			t.Fatalf("want two tool_use blocks, got %v", names)
		}
		for i, want := range []string{"write_file", "read_file"} {
			if names[order[i]] != want || inputs[order[i]] != wantArgs[i] {
				t.Fatalf("tool_use %d = %s %s, want %s %s", i, names[order[i]], inputs[order[i]], want, wantArgs[i])
			}
		}
	})
}

// A native tool decoded from a request re-emits its ingress bytes, so an edit to a typed
// field after decoding does not reach the wire until raw is cleared (the nativeToolJSON
// contract); a value whose raw is cleared marshals from its fields again.
func TestNativeToolRawBytesWinUntilCleared(t *testing.T) {
	var r Retrieval
	if err := json.Unmarshal([]byte(`{"vertexAiSearch":{"datastore":"projects/p/ds/original"}}`), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	r.VertexAISearch.Datastore = "projects/p/ds/edited"

	withRaw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal with raw: %v", err)
	}
	if !strings.Contains(string(withRaw), "original") || strings.Contains(string(withRaw), "edited") {
		t.Fatalf("a decoded tool must re-emit its ingress bytes while raw is set: %s", withRaw)
	}

	r.raw = nil
	rebuilt, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal after clearing raw: %v", err)
	}
	if !strings.Contains(string(rebuilt), "edited") || strings.Contains(string(rebuilt), "original") {
		t.Fatalf("clearing raw must marshal from the typed fields: %s", rebuilt)
	}
}

// TestToGeminiResponsesRequest_InstructionsLeadSystemInput: top-level instructions and
// system/developer input both reach systemInstruction, instructions first (OpenAI places
// instructions ahead of input). Previously instructions were dropped whenever the input
// carried system content.
func TestToGeminiResponsesRequest_InstructionsLeadSystemInput(t *testing.T) {
	user := schemas.ResponsesMessage{
		Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
	}
	developer := schemas.ResponsesMessage{
		Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleDeveloper),
		Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("DEVELOPER_MUST_SURVIVE")},
	}

	tests := []struct {
		name         string
		instructions *string
		input        []schemas.ResponsesMessage
		want         []string
	}{
		{"instructions and developer input both reach system, instructions first", schemas.Ptr("BASE_MUST_SURVIVE"), []schemas.ResponsesMessage{developer, user}, []string{"BASE_MUST_SURVIVE", "DEVELOPER_MUST_SURVIVE"}},
		{"instructions alone", schemas.Ptr("BASE_MUST_SURVIVE"), []schemas.ResponsesMessage{user}, []string{"BASE_MUST_SURVIVE"}},
		{"developer input alone", nil, []schemas.ResponsesMessage{developer, user}, []string{"DEVELOPER_MUST_SURVIVE"}},
		{"empty instructions add nothing", schemas.Ptr(""), []schemas.ResponsesMessage{developer, user}, []string{"DEVELOPER_MUST_SURVIVE"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ToGeminiResponsesRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), &schemas.BifrostResponsesRequest{
				Provider: schemas.Gemini,
				Model:    "gemini-2.5-flash",
				Input:    tc.input,
				Params:   &schemas.ResponsesParameters{Instructions: tc.instructions},
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out.SystemInstruction == nil {
				t.Fatalf("systemInstruction missing, want %q", tc.want)
			}
			var got []string
			for _, part := range out.SystemInstruction.Parts {
				got = append(got, part.Text)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("systemInstruction parts = %q, want %q", got, tc.want)
			}
			if len(out.Contents) != 1 || out.Contents[0].Role != "user" {
				t.Fatalf("expected only the user turn in contents, got %+v", out.Contents)
			}
		})
	}
}

// /genai clients send Vertex's string session_id or the Gemini API's int64 one. Either form rides
// as prompt_cache_key, so only a Vertex key with session routing puts it back on the wire.
func TestGenAISessionIDRidesAsPromptCacheKey(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	const contents = `"model":"vertex/gemini-3.8-flash","contents":[{"role":"user","parts":[{"text":"hi"}]}]`
	for _, tc := range []struct {
		name, field, want string
	}{
		{"string", `"session_id":"prefix-1"`, "prefix-1"},
		{"camelCase is not read", `"sessionId":"prefix-2"`, ""},
		{"Gemini API number", `"session_id":12345`, "12345"},
		{"null", `"session_id":null`, ""},
		{"empty", `"session_id":""`, ""},
		{"absent", `"labels":{}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var in GeminiGenerationRequest
			if err := sonic.Unmarshal([]byte("{"+contents+","+tc.field+"}"), &in); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got := ""
			if key := in.ToBifrostResponsesRequest(ctx).Params.PromptCacheKey; key != nil {
				got = *key
			}
			if got != tc.want {
				t.Fatalf("prompt_cache_key = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("a non-string, non-number value is rejected", func(t *testing.T) {
		var in GeminiGenerationRequest
		if err := sonic.Unmarshal([]byte(`{"session_id":true}`), &in); err == nil {
			t.Fatal("want an error for a boolean session_id")
		}
	})

	t.Run("marshals in the form it was received", func(t *testing.T) {
		for body, want := range map[string]string{
			`{"session_id":12345}`: `"session_id":12345`,
			`{"session_id":"abc"}`: `"session_id":"abc"`,
		} {
			var in GeminiGenerationRequest
			if err := sonic.Unmarshal([]byte(body), &in); err != nil {
				t.Fatalf("unmarshal %s: %v", body, err)
			}
			out, err := providerUtils.MarshalSorted(&in)
			if err != nil || !strings.Contains(string(out), want) {
				t.Fatalf("marshal %s = %s, %v; want it to contain %s", body, out, err, want)
			}
		}
	})

	// The Gemini API's session_id is an int64, so a string prompt_cache_key must never reach it.
	t.Run("Gemini API requests never carry it", func(t *testing.T) {
		chat, err := ToGeminiChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
			Provider: schemas.Gemini,
			Model:    "gemini-3.8-flash",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("hi")}}},
			Params:   &schemas.ChatParameters{PromptCacheKey: new("prefix-1")},
		})
		if err != nil {
			t.Fatalf("chat: %v", err)
		}
		responses, err := ToGeminiResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
			Provider: schemas.Gemini,
			Model:    "gemini-3.8-flash",
			Input:    []schemas.ResponsesMessage{{Role: new(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: new("hi")}}},
			Params:   &schemas.ResponsesParameters{PromptCacheKey: new("prefix-1")},
		})
		if err != nil {
			t.Fatalf("responses: %v", err)
		}
		for name, req := range map[string]*GeminiGenerationRequest{"chat": chat, "responses": responses} {
			body, err := providerUtils.MarshalSorted(req)
			if err != nil || strings.Contains(string(body), "ession") {
				t.Fatalf("%s body = %s, %v; want no session id", name, body, err)
			}
		}
	})
}

// Regression tests for issue #8546: Gemini/Vertex received the full "<id>_ts_<sig>" string
// as functionCall.id / functionResponse.id.
//
// Bifrost embeds a thought signature into the tool-call id on the way out to the client,
// because the Anthropic and OpenAI wire formats have no per-tool-call signature field. On
// the way back to Gemini the signature is lifted into Part.ThoughtSignature, but the joined
// id was forwarded unchanged, so Gemini saw a ~500-character id with the base64 signature
// in it as plain text -- and a model asked to reference a call by its id reproduced the
// whole string. The id Gemini issued is the base id, so that is what must go back.
//
// The same stripping is already applied for OpenAI; see the utils.StripThoughtSignature
// call sites in core/providers/openai.

// thoughtSignatureCallID builds the client-facing id Bifrost hands out for a call that
// carried a thought signature, and returns it alongside the raw signature bytes.
func thoughtSignatureCallID(t *testing.T, baseID string, raw []byte) (string, []byte) {
	t.Helper()
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	return baseID + thoughtSignatureSeparator + encoded, raw
}

// TestResponsesStripsThoughtSignatureFromGeminiIDs covers the /anthropic/v1/messages ->
// Gemini path named in the issue: the id on the wire is the base id, while the signature
// still reaches Gemini through thoughtSignature.
func TestResponsesStripsThoughtSignatureFromGeminiIDs(t *testing.T) {
	callID, raw := thoughtSignatureCallID(t, "call_1", []byte{0x01, 0x02, 0x03})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	out, err := ToGeminiResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-3-pro-preview",
		Input: []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("What is the weather in Tokyo?")},
			},
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    schemas.Ptr(callID),
					Name:      schemas.Ptr("get_weather"),
					Arguments: schemas.Ptr(`{"location":"Tokyo"}`),
				},
			},
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr(callID),
					Name:   schemas.Ptr("get_weather"),
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesToolCallOutputStr: schemas.Ptr(`{"temperature":22}`),
					},
				},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, out)

	call := findGeminiFunctionCallPart(t, out, "get_weather")
	assert.Equal(t, "call_1", call.FunctionCall.ID,
		"Gemini must receive the id it issued, not the signature-joined one")
	assert.Equal(t, raw, []byte(call.ThoughtSignature),
		"the signature must still reach Gemini through thoughtSignature")

	response := findGeminiFunctionResponsePart(t, out)
	assert.Equal(t, "call_1", response.FunctionResponse.ID,
		"the response id must be stripped the same way so it still pairs with the call")

	// Asserted on the serialized payload, not only the structs: the id was the one field
	// the signature ever travelled through, so any id that still carries it puts the
	// separator back on the wire.
	encoded, err := json.Marshal(out)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), thoughtSignatureSeparator,
		"no id on the wire may carry the thought-signature separator")
}

// TestChatCompletionsStripsThoughtSignatureFromGeminiIDs covers the chat-completions path.
// chat.go embeds the signature into the id it returns to the client there too, so a client
// echoing that id back reaches the same defect in the request converter.
func TestChatCompletionsStripsThoughtSignatureFromGeminiIDs(t *testing.T) {
	callID, raw := thoughtSignatureCallID(t, "call_1", []byte{0x0a, 0x0b, 0x0c})

	out, err := ToGeminiChatCompletionRequest(nil, &schemas.BifrostChatRequest{
		Model: "gemini-3-pro-preview",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("What is the weather in Tokyo?")},
			},
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{{
						ID:   schemas.Ptr(callID),
						Type: schemas.Ptr("function"),
						Function: schemas.ChatAssistantMessageToolCallFunction{
							Name:      schemas.Ptr("get_weather"),
							Arguments: `{"location":"Tokyo"}`,
						},
					}},
				},
			},
			{
				Role:            schemas.ChatMessageRoleTool,
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr(callID)},
				Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr(`{"temperature":"22C"}`)},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, out)

	call := findGeminiFunctionCallPart(t, out, "get_weather")
	assert.Equal(t, "call_1", call.FunctionCall.ID)
	assert.Equal(t, raw, []byte(call.ThoughtSignature))

	response := findGeminiFunctionResponsePart(t, out)
	assert.Equal(t, "call_1", response.FunctionResponse.ID)
	// The call-id-to-name map is keyed on the full id the client sent, so stripping the
	// wire id must not cost the response its tool name.
	assert.Equal(t, "get_weather", response.FunctionResponse.Name)

	encoded, err := json.Marshal(out)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), thoughtSignatureSeparator)
}

// TestResponsesPairsStrippedFunctionResponsesWithTheirCalls guards the invariant that makes
// the strip safe. Vertex drops functionCall.id / functionResponse.id and pairs results with
// calls by position, so convertResponsesMessagesToGeminiContents reorders each batch of
// responses to follow the calls using those ids. Stripping only one of the two sides would
// leave every id unmatched and silently fall back to input order.
func TestResponsesPairsStrippedFunctionResponsesWithTheirCalls(t *testing.T) {
	firstID, _ := thoughtSignatureCallID(t, "call_1", []byte{0x01})
	secondID, _ := thoughtSignatureCallID(t, "call_2", []byte{0x02})

	functionCall := func(callID, name, args string) schemas.ResponsesMessage {
		return schemas.ResponsesMessage{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    schemas.Ptr(callID),
				Name:      schemas.Ptr(name),
				Arguments: schemas.Ptr(args),
			},
		}
	}
	functionOutput := func(callID, name, output string) schemas.ResponsesMessage {
		return schemas.ResponsesMessage{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: schemas.Ptr(callID),
				Name:   schemas.Ptr(name),
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesToolCallOutputStr: schemas.Ptr(output),
				},
			},
		}
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	out, err := ToGeminiResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-3-pro-preview",
		Input: []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Weather and time in Tokyo?")},
			},
			functionCall(firstID, "get_weather", `{"location":"Tokyo"}`),
			functionCall(secondID, "get_time", `{"timezone":"Asia/Tokyo"}`),
			// Supplied in the opposite order to the calls, so input order alone cannot
			// produce the expected result.
			functionOutput(secondID, "get_time", `{"time":"09:00"}`),
			functionOutput(firstID, "get_weather", `{"temperature":22}`),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, out)

	var responseIDs []string
	for _, content := range out.Contents {
		for _, part := range content.Parts {
			if part != nil && part.FunctionResponse != nil {
				responseIDs = append(responseIDs, part.FunctionResponse.ID)
			}
		}
	}
	assert.Equal(t, []string{"call_1", "call_2"}, responseIDs,
		"responses must be reordered to follow their calls, matched on the stripped ids")
}

// TestVertexEntryPointStripsThoughtSignatureFromGeminiIDs covers the other host that reaches
// these converters. Vertex has no converters of its own: core/providers/vertex/vertex.go
// calls ToGeminiChatCompletionRequestWithImageURLSchemes and
// ToGeminiResponsesRequestWithImageURLSchemes, which delegate to the two functions fixed
// here. This drives the chat entry point Vertex uses so that delegation stays asserted
// rather than assumed.
func TestVertexEntryPointStripsThoughtSignatureFromGeminiIDs(t *testing.T) {
	callID, raw := thoughtSignatureCallID(t, "call_1", []byte{0x0d, 0x0e})

	out, err := ToGeminiChatCompletionRequestWithImageURLSchemes(nil, &schemas.BifrostChatRequest{
		Model: "gemini-3-pro-preview",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{{
						ID:   schemas.Ptr(callID),
						Type: schemas.Ptr("function"),
						Function: schemas.ChatAssistantMessageToolCallFunction{
							Name:      schemas.Ptr("get_weather"),
							Arguments: `{"location":"Tokyo"}`,
						},
					}},
				},
			},
			{
				Role:            schemas.ChatMessageRoleTool,
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr(callID)},
				Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr(`{"temperature":"22C"}`)},
			},
		},
	}, "http", "https", "gs")
	require.NoError(t, err)
	require.NotNil(t, out)

	call := findGeminiFunctionCallPart(t, out, "get_weather")
	assert.Equal(t, "call_1", call.FunctionCall.ID)
	assert.Equal(t, raw, []byte(call.ThoughtSignature))
	assert.Equal(t, "call_1", findGeminiFunctionResponsePart(t, out).FunctionResponse.ID)
}

func findGeminiFunctionCallPart(t *testing.T, req *GeminiGenerationRequest, name string) *Part {
	t.Helper()
	for _, content := range req.Contents {
		for _, part := range content.Parts {
			if part != nil && part.FunctionCall != nil && part.FunctionCall.Name == name {
				return part
			}
		}
	}
	require.FailNowf(t, "function call not found", "no functionCall part named %q", name)
	return nil
}

func findGeminiFunctionResponsePart(t *testing.T, req *GeminiGenerationRequest) *Part {
	t.Helper()
	for _, content := range req.Contents {
		for _, part := range content.Parts {
			if part != nil && part.FunctionResponse != nil {
				return part
			}
		}
	}
	require.FailNow(t, "no functionResponse part found")
	return nil
}

// BenchmarkResponsesSignatureBearingCallIDs measures the request-path cost of the strip on
// the shape that pays for it: a history of signature-bearing tool calls and their outputs,
// which is the common case once a Gemini 3 conversation has used a tool. The strip is one
// strings.Cut per call and per output, returning a substring of the existing id, so it adds
// no allocation of its own.
func BenchmarkResponsesSignatureBearingCallIDs(b *testing.B) {
	const turns = 8
	sig := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, 256))

	input := []schemas.ResponsesMessage{{
		Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
		Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Weather in Tokyo?")},
	}}
	for i := 0; i < turns; i++ {
		callID := fmt.Sprintf("call_%d%s%s", i, thoughtSignatureSeparator, sig)
		input = append(input,
			schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    schemas.Ptr(callID),
					Name:      schemas.Ptr("get_weather"),
					Arguments: schemas.Ptr(`{"location":"Tokyo"}`),
				},
			},
			schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr(callID),
					Name:   schemas.Ptr("get_weather"),
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesToolCallOutputStr: schemas.Ptr(`{"temperature":22}`),
					},
				},
			},
		)
	}

	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-3-pro-preview",
		Input:    input,
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := ToGeminiResponsesRequest(ctx, req)
		if err != nil || out == nil {
			b.Fatalf("conversion failed: %v", err)
		}
	}
}

// Long decode: a hop that hits the per-call cap ends with finishReason CONTINUATION and a
// continuationToken the client resends. Dropping either makes an SDK's automatic
// continuation read a truncated hop as a finished answer.
func TestGeminiContinuationResponseRoundTrip(t *testing.T) {
	upstream := `{"candidates":[{"content":{"role":"model","parts":[{"text":"hop one"}]},"finishReason":"CONTINUATION","continuationToken":"tok-1"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":65536,"totalTokenCount":65540}}`
	var resp GenerateContentResponse
	if err := sonic.Unmarshal([]byte(upstream), &resp); err != nil {
		t.Fatalf("decode upstream: %v", err)
	}

	bifrostResp := resp.ToResponsesBifrostResponsesResponse()
	if bifrostResp.StopReason == nil {
		t.Error("bifrost stop reason = nil, want continuation")
	} else if *bifrostResp.StopReason != "continuation" {
		t.Errorf("bifrost stop reason = %q, want continuation (Bifrost-format reasons are lowercase)", *bifrostResp.StopReason)
	}

	out, err := sonic.Marshal(ToGeminiResponsesResponse(bifrostResp))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := gjson.GetBytes(out, "candidates.0.finishReason").String(); got != "CONTINUATION" {
		t.Errorf("finishReason = %q, want CONTINUATION; body %s", got, out)
	}
	if got := gjson.GetBytes(out, "candidates.0.continuationToken").String(); got != "tok-1" {
		t.Errorf("continuationToken = %q, want tok-1; body %s", got, out)
	}
}

func TestGeminiContinuationStreamRoundTrip(t *testing.T) {
	chunks := []*GenerateContentResponse{
		{Candidates: []*Candidate{{Content: &Content{Role: "model", Parts: []*Part{{Text: "hop one"}}}}}},
		{
			Candidates:    []*Candidate{{FinishReason: FinishReasonContinuation, ContinuationToken: "tok-1"}},
			UsageMetadata: &GenerateContentResponseUsageMetadata{PromptTokenCount: 4, CandidatesTokenCount: 65536, TotalTokenCount: 65540},
		},
	}
	forward := &GeminiResponsesStreamState{}
	forward.flush()
	reverse := NewBifrostToGeminiStreamState()

	var finish FinishReason
	var token string
	seq := 0
	for _, chunk := range chunks {
		events, bifrostErr := chunk.ToBifrostResponsesStream(seq, forward)
		if bifrostErr != nil {
			t.Fatalf("forward conversion: %v", bifrostErr)
		}
		seq += len(events)
		for _, event := range events {
			out := ToGeminiResponsesStreamResponse(event, reverse)
			if out == nil {
				continue
			}
			for _, candidate := range out.Candidates {
				if candidate.FinishReason != "" {
					finish = candidate.FinishReason
				}
				if candidate.ContinuationToken != "" {
					token = candidate.ContinuationToken
				}
			}
		}
	}
	if finish != FinishReasonContinuation {
		t.Errorf("finishReason = %q, want CONTINUATION", finish)
	}
	if token != "tok-1" {
		t.Errorf("continuationToken = %q, want tok-1", token)
	}
}

// The next hop resends the original request plus only the latest token, in either spelling.
func TestGeminiContinuationRequestRoundTrip(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	for name, body := range map[string]string{
		"camelCase":  `{"model":"gemini/gemini-4-argon","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":262144},"continuationToken":"tok-1"}`,
		"snake_case": `{"model":"gemini/gemini-4-argon","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":262144},"continuation_token":"tok-1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			var in GeminiGenerationRequest
			if err := sonic.Unmarshal([]byte(body), &in); err != nil {
				t.Fatalf("decode: %v", err)
			}
			out, err := ToGeminiResponsesRequest(ctx, in.ToBifrostResponsesRequest(ctx))
			if err != nil {
				t.Fatalf("ToGeminiResponsesRequest: %v", err)
			}
			wire, err := providerUtils.MarshalSorted(out)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if got := gjson.GetBytes(wire, "continuationToken").String(); got != "tok-1" {
				t.Errorf("continuationToken = %q, want tok-1; body %s", got, wire)
			}
			if gjson.GetBytes(wire, "continuation_token").Exists() {
				t.Errorf("snake_case continuation_token must not reach the wire; body %s", wire)
			}
		})
	}
}

// TestGeminiResponsesRawAttachment verifies a Gemini chunk's raw payload rides on the event carrying its text, so the
// closing events a finishing chunk also expands into never hold text they do not back, and that the payload is
// declared only when that one event carries all of the chunk's text.
func TestGeminiResponsesRawAttachment(t *testing.T) {
	textChunk := func(finish FinishReason, texts ...string) *GenerateContentResponse {
		parts := make([]*Part, 0, len(texts))
		for _, text := range texts {
			parts = append(parts, &Part{Text: text})
		}
		return &GenerateContentResponse{Candidates: []*Candidate{{Content: &Content{Role: "model", Parts: parts}, FinishReason: finish}}}
	}
	cases := map[string]struct {
		chunks     []*GenerateContentResponse
		wantType   schemas.ResponsesStreamResponseType
		wantFormat schemas.RawResponseFormat
		notLast    bool // the chunk also expands into closing events after its text
	}{
		"first chunk": {
			chunks:     []*GenerateContentResponse{textChunk("", "Hello")},
			wantType:   schemas.ResponsesStreamResponseTypeOutputTextDelta,
			wantFormat: schemas.RawResponseFormatGeminiStreamChunk,
		},
		"finishing chunk": {
			chunks:     []*GenerateContentResponse{textChunk("", "Hello"), textChunk(FinishReasonStop, " there")},
			wantType:   schemas.ResponsesStreamResponseTypeOutputTextDelta,
			wantFormat: schemas.RawResponseFormatGeminiStreamChunk,
			notLast:    true,
		},
		"finishing chunk without text": {
			chunks:     []*GenerateContentResponse{textChunk("", "Hello"), textChunk(FinishReasonStop)},
			wantType:   schemas.ResponsesStreamResponseTypeCompleted,
			wantFormat: schemas.RawResponseFormatGeminiStreamChunk,
		},
		"text spread over several deltas": {
			chunks:   []*GenerateContentResponse{textChunk("", "Hello"), textChunk(FinishReasonStop, " there", " friend")},
			wantType: schemas.ResponsesStreamResponseTypeCompleted,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			state := &GeminiResponsesStreamState{}
			state.flush()
			var responses []*schemas.BifrostResponsesStreamResponse
			for _, chunk := range tc.chunks {
				converted, bifrostErr := chunk.ToBifrostResponsesStream(0, state)
				require.Nil(t, bifrostErr)
				responses = converted
			}

			rawIdx, format := geminiResponsesRawAttachment(responses)

			require.GreaterOrEqual(t, rawIdx, 0)
			assert.Equal(t, tc.wantType, responses[rawIdx].Type)
			assert.Equal(t, tc.wantFormat, format)
			if tc.notLast {
				assert.Less(t, rawIdx, len(responses)-1, "raw must move off the closing events")
			}
		})
	}
}

// The /anthropic ingress routed to gemini-4: Claude Code style thinking.budget_tokens
// must reach Google as thinkingLevel, because gemini-4 rejects thinkingBudget.
// Source: https://ai.google.dev/gemini-api/docs/thinking
func TestAnthropicThinkingBudgetReachesGemini4AsLevel(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &anthropic.AnthropicMessageRequest{
		Model:     "gemini/gemini-4-argon",
		MaxTokens: 16000,
		Messages: []anthropic.AnthropicMessage{
			{Role: anthropic.AnthropicMessageRoleUser, Content: anthropic.AnthropicContent{ContentStr: schemas.Ptr("hi")}},
		},
		Thinking: &anthropic.AnthropicThinking{Type: "enabled", BudgetTokens: schemas.Ptr(4096)},
	}
	normalized := req.ToBifrostResponsesRequest(ctx)
	out, err := ToGeminiResponsesRequest(ctx, normalized)
	require.NoError(t, err)
	got := out.GenerationConfig.ThinkingConfig
	require.NotNil(t, got)
	assert.Nil(t, got.ThinkingBudget, "gemini-4 rejects thinkingBudget with a 400")
	require.NotNil(t, got.ThinkingLevel)
	assert.Equal(t, "medium", *got.ThinkingLevel)
}
