package bedrock

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

const testDSMLModel = "deepseek.v3.2"

func TestStripDSMLLeak(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"leaked tag after text", "I'll list the files.\n\n<｜DSML｜function_calls", "I'll list the files.\n\n"},
		{"leaked tag with trailing whitespace", "Listing. <｜DSML｜function_calls \n", "Listing. "},
		{"only whitespace and tag", "\n\n<｜DSML｜function_calls", ""},
		{"truncated tag", "Listing.<｜DSML｜function_c", "Listing."},
		{"no tag", "plain text", "plain text"},
		{"full markup kept when upstream did not parse it", "x<｜DSML｜function_calls>\n<｜DSML｜invoke name=\"Bash\">", "x<｜DSML｜function_calls>\n<｜DSML｜invoke name=\"Bash\">"},
		{"tag followed by more text kept", "a<｜DSML｜function_calls then more", "a<｜DSML｜function_calls then more"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, StripDSMLLeak(tc.in))
		})
	}
}

func TestDSMLStreamFilter(t *testing.T) {
	ctx := testBedrockCtx()

	t.Run("tag split across deltas as Bedrock sends it", func(t *testing.T) {
		f := NewDSMLStreamFilter(ctx, testDSMLModel)
		require.NotNil(t, f)
		require.Equal(t, "I'll list", f.Text("I'll list"))
		require.Equal(t, " the files.\n\n", f.Text(" the files.\n\n<｜DSML｜function_c"))
		require.Equal(t, "", f.Text("alls"))
		f.End()
		require.Equal(t, "next block", f.Text("next block"))
	})

	t.Run("markup that continues past the tag is released whole", func(t *testing.T) {
		f := NewDSMLStreamFilter(ctx, testDSMLModel)
		require.Equal(t, "x", f.Text("x<｜DSML｜function_calls"))
		require.Equal(t, "<｜DSML｜function_calls>\n<｜DSML｜invoke", f.Text(">\n<｜DSML｜invoke"))
	})

	t.Run("held whitespace is capped", func(t *testing.T) {
		f := NewDSMLStreamFilter(ctx, testDSMLModel)
		require.Equal(t, "", f.Text("<｜DSML｜function_calls"))
		out := f.Text(strings.Repeat(" ", 64))
		require.Equal(t, "<｜DSML｜function_calls"+strings.Repeat(" ", 64), out)
	})

	// "<" and "｜DSML｜" are separate tokens, so a chunk boundary can fall between them.
	t.Run("marker split across deltas", func(t *testing.T) {
		f := NewDSMLStreamFilter(ctx, testDSMLModel)
		require.Equal(t, "I'll list the files.\n\n", f.Text("I'll list the files.\n\n<"))
		require.Equal(t, "", f.Text("｜DSML｜function_c"))
		require.Equal(t, "", f.Text("alls"))
		require.Equal(t, "", f.End())
	})

	t.Run("held partial marker released when the tag does not follow", func(t *testing.T) {
		f := NewDSMLStreamFilter(ctx, testDSMLModel)
		require.Equal(t, "if a ", f.Text("if a <"))
		require.Equal(t, "< b then ", f.Text(" b then <"), "released text keeps its own trailing '<' held")
		require.Equal(t, "<= c", f.Text("= c"))
	})

	t.Run("held partial marker returned at block end", func(t *testing.T) {
		f := NewDSMLStreamFilter(ctx, testDSMLModel)
		require.Equal(t, "x ", f.Text("x <"))
		require.Equal(t, "<", f.End())
	})

	// Converse closes a block with an event that carries no text, so a lone held "<" is dropped there.
	t.Run("split inside the marker", func(t *testing.T) {
		f := NewDSMLStreamFilter(ctx, testDSMLModel)
		require.Equal(t, "Listing. ", f.Text("Listing. <｜DS"))
		require.Equal(t, "", f.Text("ML｜function_calls"))
	})

	t.Run("earlier literal marker does not hide the trailing leak", func(t *testing.T) {
		f := NewDSMLStreamFilter(ctx, testDSMLModel)
		require.Equal(t, "The header <｜DSML｜ is documented here. ", f.Text("The header <｜DSML｜ is documented here. <｜DSML｜function_calls"))
	})

	t.Run("flush returns a held tag when no tool call follows", func(t *testing.T) {
		f := NewDSMLStreamFilter(ctx, testDSMLModel)
		require.Equal(t, "The prefix is ", f.Text("The prefix is <｜DSML｜function_calls"))
		require.Equal(t, "<｜DSML｜function_calls", f.Flush())
	})

	t.Run("other models get no filter", func(t *testing.T) {
		f := NewDSMLStreamFilter(ctx, testConverseStreamModel)
		require.Nil(t, f)
		require.Equal(t, "a<｜DSML｜function_calls", f.Text("a<｜DSML｜function_calls"))
		f.End()
	})
}

// converseDSMLResponse is a Converse body shaped like the live deepseek.v3.2 capture.
const converseDSMLResponse = `{"output":{"message":{"role":"assistant","content":[` +
	`{"text":"I'll list the files.\n\n<｜DSML｜function_calls"},` +
	`{"toolUse":{"toolUseId":"tooluse_1","name":"Bash","input":{"command":"ls -la"}}}]}},` +
	`"stopReason":"tool_use","usage":{"inputTokens":10,"outputTokens":5,"totalTokens":15}}`

func converseJSONServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(converseDSMLResponse))
	}))
}

// converseDSMLStreamServer replays the live deepseek.v3.2 ConverseStream event order.
func converseDSMLStreamServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		writeEventStreamEvent(t, w, "messageStart", []byte(`{"role":"assistant"}`))
		writeEventStreamEvent(t, w, "contentBlockDelta", []byte(`{"delta":{"text":"I'll list the files"},"contentBlockIndex":0}`))
		writeEventStreamEvent(t, w, "contentBlockDelta", []byte(`{"delta":{"text":" now.\n\n<｜DSML｜function_c"},"contentBlockIndex":0}`))
		writeEventStreamEvent(t, w, "contentBlockDelta", []byte(`{"delta":{"text":"alls"},"contentBlockIndex":0}`))
		writeEventStreamEvent(t, w, "contentBlockStop", []byte(`{"contentBlockIndex":0}`))
		writeEventStreamEvent(t, w, "contentBlockStart", []byte(`{"start":{"toolUse":{"toolUseId":"tooluse_1","name":"Bash"}},"contentBlockIndex":1}`))
		writeEventStreamEvent(t, w, "contentBlockDelta", []byte(`{"delta":{"toolUse":{"input":"{\"command\": \"ls -la\"}"}},"contentBlockIndex":1}`))
		writeEventStreamEvent(t, w, "contentBlockStop", []byte(`{"contentBlockIndex":1}`))
		writeEventStreamEvent(t, w, "messageStop", []byte(`{"stopReason":"tool_use"}`))
		writeEventStreamEvent(t, w, "metadata", []byte(`{"usage":{"inputTokens":10,"outputTokens":5,"totalTokens":15},"metrics":{"latencyMs":1}}`))
	}))
}

// drainToJSON collects a provider stream and returns every chunk serialized, in order.
func drainToJSON(t *testing.T, stream chan *schemas.BifrostStreamChunk) string {
	t.Helper()
	var b strings.Builder
	timeout := time.After(5 * time.Second)
	for {
		select {
		case chunk, ok := <-stream:
			if !ok {
				return b.String()
			}
			require.Nil(t, chunk.BifrostError, "unexpected error chunk: %+v", chunk.BifrostError)
			out, err := sonic.Marshal(chunk)
			require.NoError(t, err)
			b.Write(out)
			b.WriteByte('\n')
		case <-timeout:
			t.Fatal("timed out draining stream")
		}
	}
}

func requireDSMLStripped(t *testing.T, out string) {
	t.Helper()
	require.NotContains(t, out, "DSML", "leaked DSML tag reached the client")
	require.Contains(t, out, "I'll list the files", "assistant text must survive")
	require.Contains(t, out, "ls -la", "tool call must survive")
}

func TestDSMLLeakStripped_ConverseChat(t *testing.T) {
	ts := converseJSONServer(t)
	defer ts.Close()
	req := testChatRequest()
	req.Model = testDSMLModel
	resp, bifrostErr := newTestProviderWithServer(t, ts).ChatCompletion(testBedrockCtx(), testBedrockKey(), req)
	require.Nil(t, bifrostErr)
	out, err := sonic.Marshal(resp)
	require.NoError(t, err)
	requireDSMLStripped(t, string(out))
}

func TestDSMLLeakStripped_ConverseResponses(t *testing.T) {
	ts := converseJSONServer(t)
	defer ts.Close()
	req := testResponsesRequest()
	req.Model = testDSMLModel
	resp, bifrostErr := newTestProviderWithServer(t, ts).Responses(testBedrockCtx(), testBedrockKey(), req)
	require.Nil(t, bifrostErr)
	out, err := sonic.Marshal(resp)
	require.NoError(t, err)
	requireDSMLStripped(t, string(out))
}

func TestDSMLLeakStripped_ConverseChatStream(t *testing.T) {
	ts := converseDSMLStreamServer(t)
	defer ts.Close()
	req := testChatRequest()
	req.Model = testDSMLModel
	stream, bifrostErr := newTestProviderWithServer(t, ts).ChatCompletionStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), req)
	require.Nil(t, bifrostErr)
	requireDSMLStripped(t, drainToJSON(t, stream))
}

func TestDSMLLeakStripped_ConverseResponsesStream(t *testing.T) {
	ts := converseDSMLStreamServer(t)
	defer ts.Close()
	req := testResponsesRequest()
	req.Model = testDSMLModel
	stream, bifrostErr := newTestProviderWithServer(t, ts).ResponsesStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), req)
	require.Nil(t, bifrostErr)
	requireDSMLStripped(t, drainToJSON(t, stream))
}

// The Converse stream with the chunk boundary between "<" and "｜DSML｜".
func TestDSMLLeakStripped_ConverseChatStreamSplitMarker(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		writeEventStreamEvent(t, w, "messageStart", []byte(`{"role":"assistant"}`))
		writeEventStreamEvent(t, w, "contentBlockDelta", []byte(`{"delta":{"text":"I'll list the files now.\n\n<"},"contentBlockIndex":0}`))
		writeEventStreamEvent(t, w, "contentBlockDelta", []byte(`{"delta":{"text":"｜DSML｜function_calls"},"contentBlockIndex":0}`))
		writeEventStreamEvent(t, w, "contentBlockStop", []byte(`{"contentBlockIndex":0}`))
		writeEventStreamEvent(t, w, "contentBlockStart", []byte(`{"start":{"toolUse":{"toolUseId":"tooluse_1","name":"Bash"}},"contentBlockIndex":1}`))
		writeEventStreamEvent(t, w, "contentBlockDelta", []byte(`{"delta":{"toolUse":{"input":"{\"command\": \"ls -la\"}"}},"contentBlockIndex":1}`))
		writeEventStreamEvent(t, w, "contentBlockStop", []byte(`{"contentBlockIndex":1}`))
		writeEventStreamEvent(t, w, "messageStop", []byte(`{"stopReason":"tool_use"}`))
	}))
	defer ts.Close()
	req := testChatRequest()
	req.Model = testDSMLModel
	stream, bifrostErr := newTestProviderWithServer(t, ts).ChatCompletionStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), req)
	require.Nil(t, bifrostErr)
	requireDSMLStripped(t, drainToJSON(t, stream))
}

// Other models pass text through untouched, tag included.
func TestDSMLLeakKept_OtherModelsConverseStream(t *testing.T) {
	ts := converseDSMLStreamServer(t)
	defer ts.Close()
	req := testChatRequest()
	req.Model = testConverseStreamModel
	stream, bifrostErr := newTestProviderWithServer(t, ts).ChatCompletionStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), req)
	require.Nil(t, bifrostErr)
	require.Contains(t, drainToJSON(t, stream), "DSML")
}

func BenchmarkDSMLStreamFilter(b *testing.B) {
	ctx := testBedrockCtx()
	text := "I'll list the files in the current directory and then read README.md."
	b.Run("other model (nil filter)", func(b *testing.B) {
		f := NewDSMLStreamFilter(ctx, testConverseStreamModel)
		ev := &BedrockStreamEvent{Delta: &BedrockContentBlockDelta{Text: &text}}
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			f.filterConverseEvent(ev)
		}
	})
	b.Run("deepseek text without tag", func(b *testing.B) {
		f := NewDSMLStreamFilter(ctx, testDSMLModel)
		ev := &BedrockStreamEvent{Delta: &BedrockContentBlockDelta{Text: &text}}
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			f.filterConverseEvent(ev)
		}
	})
	b.Run("deepseek leaked tag", func(b *testing.B) {
		f := NewDSMLStreamFilter(ctx, testDSMLModel)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			f.Text(" now.\n\n<｜DSML｜function_c")
			f.Text("alls")
			f.End()
		}
	})
	b.Run("gate check", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = IsDSMLLeakModel(ctx, testConverseStreamModel)
		}
	})
}

func TestStripDSMLLeakChat_RequiresToolCall(t *testing.T) {
	answer := "The prefix is <｜DSML｜function_calls"
	resp := &schemas.BifrostChatResponse{Choices: []schemas.BifrostResponseChoice{{
		FinishReason: schemas.Ptr("stop"),
		ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{Message: &schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(answer)},
		}},
	}}}
	StripDSMLLeakChat(resp)
	require.Equal(t, answer, *resp.Choices[0].Message.Content.ContentStr, "an answer with no tool call must be left alone")
}

func TestStripDSMLLeakConverse_OnlyBeforeToolUse(t *testing.T) {
	var resp BedrockConverseResponse
	require.NoError(t, sonic.Unmarshal([]byte(`{"output":{"message":{"role":"assistant","content":[`+
		`{"text":"<｜DSML｜function_calls"}]}},"stopReason":"end_turn"}`), &resp))
	stripDSMLLeakConverse(&resp)
	require.Len(t, resp.Output.Message.Content, 1, "an end_turn answer with no toolUse must keep its block")
	require.Equal(t, "<｜DSML｜function_calls", *resp.Output.Message.Content[0].Text)
}

// converseTextOnlyStreamServer streams one text block with no toolUse, split as given.
func converseTextOnlyStreamServer(t *testing.T, deltas ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		writeEventStreamEvent(t, w, "messageStart", []byte(`{"role":"assistant"}`))
		for _, d := range deltas {
			payload, err := sonic.Marshal(map[string]any{"delta": map[string]string{"text": d}, "contentBlockIndex": 0})
			require.NoError(t, err)
			writeEventStreamEvent(t, w, "contentBlockDelta", payload)
		}
		writeEventStreamEvent(t, w, "contentBlockStop", []byte(`{"contentBlockIndex":0}`))
		writeEventStreamEvent(t, w, "messageStop", []byte(`{"stopReason":"end_turn"}`))
		writeEventStreamEvent(t, w, "metadata", []byte(`{"usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2},"metrics":{"latencyMs":1}}`))
	}))
}

// streamedText returns the text a client assembles from chat or Responses stream chunks.
func streamedText(t *testing.T, stream chan *schemas.BifrostStreamChunk) (deltas string, done string) {
	t.Helper()
	var b strings.Builder
	timeout := time.After(5 * time.Second)
	for {
		select {
		case chunk, ok := <-stream:
			if !ok {
				return b.String(), done
			}
			require.Nil(t, chunk.BifrostError, "unexpected error chunk: %+v", chunk.BifrostError)
			if r := chunk.BifrostChatResponse; r != nil {
				for _, c := range r.Choices {
					if c.ChatStreamResponseChoice != nil && c.Delta != nil && c.Delta.Content != nil {
						b.WriteString(*c.Delta.Content)
					}
				}
			}
			if r := chunk.BifrostResponsesStreamResponse; r != nil {
				switch r.Type {
				case schemas.ResponsesStreamResponseTypeOutputTextDelta:
					if r.Delta != nil {
						b.WriteString(*r.Delta)
					}
				case schemas.ResponsesStreamResponseTypeOutputTextDone:
					if r.Text != nil {
						done = *r.Text
					}
				}
			}
		case <-timeout:
			t.Fatal("timed out draining stream")
		}
	}
}

// With no toolUse after it, held text is the answer and must reach the client in full.
func TestDSMLConverseStreamKeepsTextWithoutToolUse(t *testing.T) {
	cases := []struct {
		name   string
		deltas []string
	}{
		{"literal tag at the end", []string{"The prefix is <｜DSML｜function_calls"}},
		{"literal tag split across chunks", []string{"The prefix is <", "｜DSML｜function_c", "alls"}},
		{"lone trailing angle bracket", []string{"this result is <"}},
		{"angle bracket then more text", []string{"this result is <", "30"}},
	}
	for _, tc := range cases {
		want := strings.Join(tc.deltas, "")
		t.Run("chat/"+tc.name, func(t *testing.T) {
			ts := converseTextOnlyStreamServer(t, tc.deltas...)
			defer ts.Close()
			req := testChatRequest()
			req.Model = testDSMLModel
			stream, bifrostErr := newTestProviderWithServer(t, ts).ChatCompletionStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), req)
			require.Nil(t, bifrostErr)
			got, _ := streamedText(t, stream)
			require.Equal(t, want, got)
		})
		t.Run("responses/"+tc.name, func(t *testing.T) {
			ts := converseTextOnlyStreamServer(t, tc.deltas...)
			defer ts.Close()
			req := testResponsesRequest()
			req.Model = testDSMLModel
			stream, bifrostErr := newTestProviderWithServer(t, ts).ResponsesStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), req)
			require.Nil(t, bifrostErr)
			got, done := streamedText(t, stream)
			require.Equal(t, want, got, "output_text deltas")
			require.Equal(t, want, done, "output_text.done must carry the same text")
		})
	}
}

// A closing chunk can arrive without a delta object; held text must still be settled on it.
func TestDSMLFilterChatChunk_FinishWithoutDelta(t *testing.T) {
	ctx := testBedrockCtx()
	finish := func(reason string) *schemas.BifrostChatResponse {
		return &schemas.BifrostChatResponse{Choices: []schemas.BifrostResponseChoice{{FinishReason: schemas.Ptr(reason)}}}
	}
	content := func(r *schemas.BifrostChatResponse) *string {
		c := r.Choices[0].ChatStreamResponseChoice
		if c == nil || c.Delta == nil {
			return nil
		}
		return c.Delta.Content
	}
	cases := []struct {
		name, held, reason string
		want               *string
	}{
		{"stop returns a held tag", "The prefix is <｜DSML｜function_calls", "stop", schemas.Ptr("<｜DSML｜function_calls")},
		{"stop returns a held angle bracket", "use a <", "stop", schemas.Ptr("<")},
		{"tool_calls drops a held tag", "I'll list.\n\n<｜DSML｜function_calls", "tool_calls", nil},
		{"tool_calls returns a held angle bracket", "use a <", "tool_calls", schemas.Ptr("<")},
		{"nothing held leaves the chunk alone", "plain text", "stop", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := NewDSMLStreamFilter(ctx, testDSMLModel)
			f.Text(tc.held)
			r := finish(tc.reason)
			f.FilterChatChunk(r)
			if tc.want == nil {
				require.Nil(t, r.Choices[0].ChatStreamResponseChoice, "no delta should be invented")
				return
			}
			require.NotNil(t, content(r))
			require.Equal(t, *tc.want, *content(r))
			require.False(t, f.holding)
		})
	}
}
