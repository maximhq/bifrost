package anthropic

import (
	"strings"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"

	"github.com/bytedance/sonic"
)

// rawWebSearchChatResponse is a non-streaming Anthropic response to a request
// that enabled the web_search server tool. The search runs on Anthropic's side:
// server_tool_use and web_search_tool_result are not OpenAI tool calls, and the
// cited text block carries web_search_result_location citations.
var rawWebSearchChatResponse = `{
  "model": "claude-sonnet-4-6",
  "id": "msg_websearch",
  "type": "message",
  "role": "assistant",
  "stop_reason": "end_turn",
  "content": [
    {"type":"text","text":"I'll search for that."},
    {"type":"server_tool_use","id":"srvtoolu_01","name":"web_search","input":{"query":"bbc top headline"}},
    {"type":"web_search_tool_result","tool_use_id":"srvtoolu_01","content":[{"type":"web_search_result","url":"https://www.bbc.com/news/a","title":"Story A","encrypted_content":"EqQ...","page_age":"1 hour ago"}]},
    {"type":"text","text":"The top story is A.","citations":[
      {"type":"web_search_result_location","url":"https://www.bbc.com/news/a","title":"Story A","encrypted_index":"Eo8...","cited_text":"Story A text..."},
      {"type":"web_search_result_location","url":"https://www.bbc.com/news/a","title":"Story A","encrypted_index":"Eo9...","cited_text":"Story A text again..."},
      {"type":"char_location","cited_text":"doc text","document_index":0,"start_char_index":0,"end_char_index":8}
    ]},
    {"type":"text","text":" Nothing else is notable."}
  ],
  "usage": {"input_tokens": 10, "output_tokens": 20, "server_tool_use": {"web_search_requests": 1}}
}`

const (
	wsIntro   = "I'll search for that."
	wsCited   = "The top story is A."
	wsTrailer = " Nothing else is notable."
	wsURL     = "https://www.bbc.com/news/a"
	wsTitle   = "Story A"
)

func TestWebSearchChat_NonStream_CitationsBecomeAnnotations(t *testing.T) {
	var anthropicResp AnthropicMessageResponse
	if err := sonic.Unmarshal([]byte(rawWebSearchChatResponse), &anthropicResp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	bifrostResp := anthropicResp.ToBifrostChatResponse(&schemas.BifrostContext{})
	if bifrostResp == nil || len(bifrostResp.Choices) != 1 {
		t.Fatal("ToBifrostChatResponse returned no choice")
	}
	msg := bifrostResp.Choices[0].Message
	if msg.Content == nil || msg.Content.ContentStr == nil {
		t.Fatalf("content did not collapse to a string: %+v", msg.Content)
	}
	content := *msg.Content.ContentStr
	if want := wsIntro + wsCited + wsTrailer; content != want {
		t.Fatalf("content = %q, want %q", content, want)
	}
	if msg.ChatAssistantMessage == nil {
		t.Fatal("expected an assistant message carrying annotations")
	}
	if len(msg.ChatAssistantMessage.ToolCalls) != 0 {
		t.Errorf("server-side web search leaked as tool_calls: %+v", msg.ChatAssistantMessage.ToolCalls)
	}

	anns := msg.ChatAssistantMessage.Annotations
	if len(anns) != 1 {
		t.Fatalf("expected 1 deduplicated url_citation annotation, got %d: %+v", len(anns), anns)
	}
	ann := anns[0]
	if ann.Type != "url_citation" {
		t.Errorf("annotation type = %q, want url_citation", ann.Type)
	}
	if ann.URLCitation.URL == nil || *ann.URLCitation.URL != wsURL {
		t.Errorf("annotation url = %v, want %q", ann.URLCitation.URL, wsURL)
	}
	if ann.URLCitation.Title != wsTitle {
		t.Errorf("annotation title = %q, want %q", ann.URLCitation.Title, wsTitle)
	}
	wantStart, wantEnd := len(wsIntro), len(wsIntro)+len(wsCited)
	if ann.URLCitation.StartIndex != wantStart || ann.URLCitation.EndIndex != wantEnd {
		t.Errorf("annotation span = [%d,%d), want [%d,%d)", ann.URLCitation.StartIndex, ann.URLCitation.EndIndex, wantStart, wantEnd)
	}
	if got := content[ann.URLCitation.StartIndex:ann.URLCitation.EndIndex]; got != wsCited {
		t.Errorf("span selects %q, want %q", got, wsCited)
	}

	if bifrostResp.Choices[0].FinishReason == nil || *bifrostResp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %v, want stop", bifrostResp.Choices[0].FinishReason)
	}

	out, err := sonic.Marshal(bifrostResp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if !strings.Contains(string(out), `"annotations":[{"type":"url_citation"`) {
		t.Errorf("wire response missing url_citation annotations: %s", out)
	}
}

// A real function tool_use in the same turn as a web search must still come
// back as an OpenAI tool call.
func TestWebSearchChat_NonStream_RealToolUseStillToolCall(t *testing.T) {
	raw := `{
  "model": "claude-sonnet-4-6",
  "id": "msg_mixed",
  "type": "message",
  "role": "assistant",
  "stop_reason": "tool_use",
  "content": [
    {"type":"server_tool_use","id":"srvtoolu_01","name":"web_search","input":{"query":"weather"}},
    {"type":"web_search_tool_result","tool_use_id":"srvtoolu_01","content":[]},
    {"type":"text","text":"Checking.","citations":[{"type":"web_search_result_location","url":"https://example.com/w","title":"W","cited_text":"w"}]},
    {"type":"tool_use","id":"toolu_01","name":"get_weather","input":{"city":"Paris"}}
  ],
  "usage": {"input_tokens": 10, "output_tokens": 20}
}`
	var anthropicResp AnthropicMessageResponse
	if err := sonic.Unmarshal([]byte(raw), &anthropicResp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	msg := anthropicResp.ToBifrostChatResponse(&schemas.BifrostContext{}).Choices[0].Message
	if msg.ChatAssistantMessage == nil || len(msg.ChatAssistantMessage.ToolCalls) != 1 {
		t.Fatalf("expected exactly the one real tool call, got %+v", msg.ChatAssistantMessage)
	}
	tc := msg.ChatAssistantMessage.ToolCalls[0]
	if tc.ID == nil || *tc.ID != "toolu_01" || tc.Function.Name == nil || *tc.Function.Name != "get_weather" {
		t.Errorf("unexpected tool call: %+v", tc)
	}
	if tc.Function.Arguments != `{"city":"Paris"}` {
		t.Errorf("tool call arguments = %q", tc.Function.Arguments)
	}
	anns := msg.ChatAssistantMessage.Annotations
	if len(anns) != 1 || anns[0].URLCitation.StartIndex != 0 || anns[0].URLCitation.EndIndex != len("Checking.") {
		t.Errorf("unexpected annotations: %+v", anns)
	}
}

// wsStreamResult collects what the OpenAI-format stream carries.
type wsStreamResult struct {
	content     string
	toolCalls   []schemas.ChatAssistantMessageToolCall
	annotations []schemas.ChatAssistantMessageAnnotation
	// annotationAfterEvent is the event index that produced each annotation chunk.
	annotationAfterEvent []int
}

func runWebSearchStream(t *testing.T, events []string) wsStreamResult {
	t.Helper()
	state := NewAnthropicStreamState()
	var res wsStreamResult
	for i, raw := range events {
		var ev AnthropicStreamEvent
		if err := sonic.Unmarshal([]byte(raw), &ev); err != nil {
			t.Fatalf("unmarshal event %d: %v", i, err)
		}
		resp, bErr, _ := ev.ToBifrostChatCompletionStream(nil, "", state)
		if bErr != nil {
			t.Fatalf("event %d: unexpected error %+v", i, bErr)
		}
		if resp == nil {
			continue
		}
		delta := resp.Choices[0].ChatStreamResponseChoice.Delta
		if delta.Content != nil {
			res.content += *delta.Content
		}
		res.toolCalls = append(res.toolCalls, delta.ToolCalls...)
		if len(delta.Annotations) > 0 {
			res.annotations = append(res.annotations, delta.Annotations...)
			res.annotationAfterEvent = append(res.annotationAfterEvent, i)
		}
	}
	return res
}

var webSearchStreamEvents = []string{
	`{"type":"message_start","message":{"id":"msg_ws","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I'll search "}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"for that."}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srvtoolu_01","name":"web_search","input":{}}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":""}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query"}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\": \"bbc top headline\"}"}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"content_block_start","index":2,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01","content":[{"type":"web_search_result","url":"https://www.bbc.com/news/a","title":"Story A","encrypted_content":"EqQ...","page_age":"1 hour ago"}]}}`,
	`{"type":"content_block_stop","index":2}`,
	`{"type":"content_block_start","index":3,"content_block":{"type":"text","text":"","citations":[]}}`,
	`{"type":"content_block_delta","index":3,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","url":"https://www.bbc.com/news/a","title":"Story A","encrypted_index":"Eo8...","cited_text":"Story A text..."}}}`,
	`{"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"The top story is A."}}`,
	`{"type":"content_block_stop","index":3}`,
	`{"type":"content_block_start","index":4,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":4,"delta":{"type":"text_delta","text":" Nothing else is notable."}}`,
	`{"type":"content_block_stop","index":4}`,
	`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20,"server_tool_use":{"web_search_requests":1}}}`,
	`{"type":"message_stop"}`,
}

func TestWebSearchChat_Stream_NoToolCallLeakAndAnnotationAtBlockStop(t *testing.T) {
	res := runWebSearchStream(t, webSearchStreamEvents)

	if len(res.toolCalls) != 0 {
		t.Fatalf("server_tool_use leaked %d tool_calls deltas: %+v", len(res.toolCalls), res.toolCalls)
	}
	if want := wsIntro + wsCited + wsTrailer; res.content != want {
		t.Fatalf("content = %q, want %q", res.content, want)
	}
	if len(res.annotations) != 1 {
		t.Fatalf("expected 1 annotation, got %d: %+v", len(res.annotations), res.annotations)
	}
	// Emitted on the cited block's content_block_stop (event 15), not later.
	if len(res.annotationAfterEvent) != 1 || res.annotationAfterEvent[0] != 15 {
		t.Errorf("annotation emitted after events %v, want [15] (index 3 content_block_stop)", res.annotationAfterEvent)
	}
	ann := res.annotations[0]
	if ann.Type != "url_citation" || ann.URLCitation.URL == nil || *ann.URLCitation.URL != wsURL || ann.URLCitation.Title != wsTitle {
		t.Errorf("unexpected annotation: %+v", ann)
	}
	wantStart, wantEnd := len(wsIntro), len(wsIntro)+len(wsCited)
	if ann.URLCitation.StartIndex != wantStart || ann.URLCitation.EndIndex != wantEnd {
		t.Errorf("annotation span = [%d,%d), want [%d,%d)", ann.URLCitation.StartIndex, ann.URLCitation.EndIndex, wantStart, wantEnd)
	}
	if got := res.content[ann.URLCitation.StartIndex:ann.URLCitation.EndIndex]; got != wsCited {
		t.Errorf("span selects %q, want %q", got, wsCited)
	}
}

// A real function tool_use streamed after a web search keeps its tool call
// index 0 and its full arguments; the server_tool_use input never mixes in.
func TestWebSearchChat_Stream_RealToolUseUnaffected(t *testing.T) {
	events := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_01","name":"web_search","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\": \"weather\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01","content":[]}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_01","name":"get_weather","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":""}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_stop"}`,
	}
	res := runWebSearchStream(t, events)

	if len(res.toolCalls) != 3 {
		t.Fatalf("expected setup + 2 argument deltas for the real tool, got %d: %+v", len(res.toolCalls), res.toolCalls)
	}
	setup := res.toolCalls[0]
	if setup.Index != 0 || setup.ID == nil || *setup.ID != "toolu_01" || setup.Function.Name == nil || *setup.Function.Name != "get_weather" {
		t.Errorf("unexpected setup chunk: %+v", setup)
	}
	var args string
	for _, tc := range res.toolCalls {
		if tc.Index != 0 {
			t.Errorf("tool call delta has index %d, want 0", tc.Index)
		}
		args += tc.Function.Arguments
	}
	if args != `{"city":"Paris"}` {
		t.Errorf("accumulated arguments = %q, want {\"city\":\"Paris\"}", args)
	}
	if len(res.annotations) != 0 {
		t.Errorf("unexpected annotations: %+v", res.annotations)
	}
}

// Non-web citation types are ignored without error, and a text block without
// citations emits nothing extra on stop.
func TestWebSearchChat_Stream_OtherCitationTypesIgnored(t *testing.T) {
	events := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{"type":"char_location","cited_text":"x","document_index":0,"start_char_index":0,"end_char_index":1}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"From the doc."}}`,
		`{"type":"content_block_stop","index":0}`,
	}
	res := runWebSearchStream(t, events)
	if res.content != "From the doc." {
		t.Errorf("content = %q", res.content)
	}
	if len(res.annotations) != 0 || len(res.toolCalls) != 0 {
		t.Errorf("expected no annotations or tool calls, got %+v / %+v", res.annotations, res.toolCalls)
	}
}
