package gemini

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// collectStreamWebSearchItems drives chunks through the responses stream converter and
// returns the completed web_search_call items keyed by item ID, in emission order.
func collectStreamWebSearchItems(t *testing.T, chunks []string) ([]string, map[string]*schemas.ResponsesWebSearchToolCallAction) {
	t.Helper()
	state := acquireGeminiResponsesStreamState()
	defer releaseGeminiResponsesStreamState(state)

	var order []string
	items := map[string]*schemas.ResponsesWebSearchToolCallAction{}
	seq := 0
	for _, c := range chunks {
		var resp GenerateContentResponse
		if err := json.Unmarshal([]byte(c), &resp); err != nil {
			t.Fatal(err)
		}
		events, err := resp.ToBifrostResponsesStream(seq, state)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			if e.Item == nil || e.Item.Type == nil || *e.Item.Type != schemas.ResponsesMessageTypeWebSearchCall {
				continue
			}
			if e.Item.ResponsesToolMessage == nil || e.Item.ResponsesToolMessage.Action == nil {
				continue
			}
			a := e.Item.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction
			if a == nil || len(a.Queries) == 0 {
				continue
			}
			if e.Item.ID == nil {
				t.Fatalf("web_search_call item carried an action but no ID: %+v", a)
			}
			if _, seen := items[*e.Item.ID]; !seen {
				order = append(order, *e.Item.ID)
			}
			items[*e.Item.ID] = a
		}
		seq += len(events)
	}
	return order, items
}

// Streaming counterpart of TestServerSideToolCallMultipleRoundsWithFunctionCall: two
// server-side search rounds must produce one web_search_call item each, carrying their own
// call IDs and their own queries -- matching what the non-streaming converter produces for
// the same content rather than collapsing both rounds into a single merged item.
func TestServerSideToolCallStreamingMultipleRounds(t *testing.T) {
	chunks := []string{
		`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"thoughtSignature":"c2lnMQ==","toolCall":{"toolType":"GOOGLE_SEARCH_WEB","args":{"queries":["FIFA World Cup winner 2026","2026 FIFA World Cup winner"]},"id":"BaoigkFu"}}]}}],"modelVersion":"gemini-3.6-flash"}`,
		`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"thoughtSignature":"c2lnMg==","toolResponse":{"toolType":"GOOGLE_SEARCH_WEB","response":{"search_suggestions":"<div/>"},"id":"BaoigkFu"}}]}}],"modelVersion":"gemini-3.6-flash"}`,
		`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"thoughtSignature":"c2lnMw==","toolCall":{"toolType":"GOOGLE_SEARCH_WEB","args":{"queries":["\"2026 FIFA World Cup\" score final Spain Argentina"]},"id":"Fbe8aaSI"}}]}}],"modelVersion":"gemini-3.6-flash"}`,
		`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"thoughtSignature":"c2lnNA==","toolResponse":{"toolType":"GOOGLE_SEARCH_WEB","response":{"search_suggestions":"<div/>"},"id":"Fbe8aaSI"}}]}}],"modelVersion":"gemini-3.6-flash"}`,
		`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Spain won."}]}}],"modelVersion":"gemini-3.6-flash"}`,
		`{"candidates":[{"index":0,"finishReason":"STOP","groundingMetadata":{"webSearchQueries":["FIFA World Cup winner 2026"],"groundingChunks":[{"web":{"uri":"https://x/1","title":"fifa.com"}}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-3.6-flash"}`,
	}

	order, items := collectStreamWebSearchItems(t, chunks)

	// Same expectation the non-streaming path asserts for this content
	// (TestServerSideToolCallMultipleRoundsWithFunctionCall): one item per round, in order.
	if len(items) != 2 {
		t.Fatalf("expected one web_search_call per search round, got %d: %v", len(items), order)
	}
	want := []string{"BaoigkFu", "Fbe8aaSI"}
	for i, id := range want {
		if i >= len(order) || order[i] != id {
			t.Fatalf("expected rounds in order %v, got %v", want, order)
		}
	}

	first := items["BaoigkFu"]
	if len(first.Queries) != 2 {
		t.Errorf("first round must keep only its own 2 queries, got %v", first.Queries)
	}
	if len(first.Sources) != 1 {
		t.Errorf("grounding sources merge onto the first round, got %d", len(first.Sources))
	}

	second := items["Fbe8aaSI"]
	if len(second.Queries) != 1 || second.Queries[0] != `"2026 FIFA World Cup" score final Spain Argentina` {
		t.Errorf("second round must keep only its own query, got %v", second.Queries)
	}
}

// The /genai SSE surface converts Gemini -> Bifrost -> Gemini. The native server-side
// toolCall/toolResponse parts must survive that round-trip with their thoughtSignature
// bytes, exactly as they do on the non-streaming path -- a client replaying the streamed
// turn needs the same parts Google sent, and Gemini rejects a replay whose signatures are
// missing. Each signature must appear exactly once: the reasoning item carrying it and the
// native part carrying it are the same part, not two.
func TestServerSideToolCallStreamingGenAIRoundTrip(t *testing.T) {
	chunks := []string{
		`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"thoughtSignature":"c2lnMQ==","toolCall":{"toolType":"GOOGLE_SEARCH_WEB","args":{"queries":["f1 winner 2026"]},"id":"nqh2j2zy"}}]}}],"modelVersion":"gemini-3.6-flash"}`,
		`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"thoughtSignature":"c2lnMg==","toolResponse":{"toolType":"GOOGLE_SEARCH_WEB","response":{"search_suggestions":"<div>chips</div>"},"id":"nqh2j2zy"}}]}}],"modelVersion":"gemini-3.6-flash"}`,
		`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Lando Norris won."}]}}],"modelVersion":"gemini-3.6-flash"}`,
		`{"candidates":[{"index":0,"finishReason":"STOP","groundingMetadata":{"webSearchQueries":["f1 winner 2026"],"groundingChunks":[{"web":{"uri":"https://x/1","title":"formula1.com"}}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-3.6-flash"}`,
	}

	state := acquireGeminiResponsesStreamState()
	defer releaseGeminiResponsesStreamState(state)
	outState := NewBifrostToGeminiStreamState()

	var outParts []*Part
	seq := 0
	for _, c := range chunks {
		var resp GenerateContentResponse
		if err := json.Unmarshal([]byte(c), &resp); err != nil {
			t.Fatal(err)
		}
		events, err := resp.ToBifrostResponsesStream(seq, state)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			out := ToGeminiResponsesStreamResponse(e, outState)
			if out == nil {
				continue
			}
			for _, cand := range out.Candidates {
				if cand.Content == nil {
					continue
				}
				outParts = append(outParts, cand.Content.Parts...)
			}
		}
		seq += len(events)
	}

	var toolCalls, toolResponses, signatureOnly int
	sigCounts := map[string]int{}
	for _, p := range outParts {
		if len(p.ThoughtSignature) > 0 {
			sigCounts[base64.StdEncoding.EncodeToString(p.ThoughtSignature)]++
		}
		switch {
		case p.ToolCall != nil:
			toolCalls++
			if p.ToolCall.ID != "nqh2j2zy" {
				t.Errorf("toolCall lost its id: %q", p.ToolCall.ID)
			}
			if len(p.ThoughtSignature) == 0 {
				t.Error("streamed toolCall part lost its thoughtSignature")
			}
		case p.ToolResponse != nil:
			toolResponses++
			if p.ToolResponse.ID != "nqh2j2zy" {
				t.Errorf("toolResponse lost its id: %q", p.ToolResponse.ID)
			}
			if len(p.ThoughtSignature) == 0 {
				t.Error("streamed toolResponse part lost its thoughtSignature")
			}
		case len(p.ThoughtSignature) > 0 && p.Text == "":
			signatureOnly++
		}
	}

	if toolCalls != 1 {
		t.Errorf("expected the native toolCall part to survive streaming, got %d", toolCalls)
	}
	if toolResponses != 1 {
		t.Errorf("expected the native toolResponse part to survive streaming, got %d", toolResponses)
	}
	// The signature rides on the native part; a separate signature-only part for the same
	// bytes would hand the client the same reasoning twice.
	for sig, n := range sigCounts {
		if n != 1 {
			t.Errorf("thoughtSignature %s emitted %d times, want exactly 1", sig, n)
		}
	}
	if signatureOnly != 0 {
		t.Errorf("expected no duplicate signature-only parts alongside the native ones, got %d", signatureOnly)
	}
}

func TestServerSideToolCallStreaming(t *testing.T) {
	chunks := []string{
		`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"thoughtSignature":"c2ln","toolCall":{"toolType":"GOOGLE_SEARCH_WEB","args":{"queries":["f1 winner 2026","f1 results"]},"id":"nqh2j2zy"}}]}}],"modelVersion":"gemini-3.5-flash"}`,
		`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"thoughtSignature":"c2ln","toolResponse":{"toolType":"GOOGLE_SEARCH_WEB","response":{"search_suggestions":"<div/>"},"id":"nqh2j2zy"}}]}}],"modelVersion":"gemini-3.5-flash"}`,
		`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Lando Norris won."}]}}],"modelVersion":"gemini-3.5-flash"}`,
		`{"candidates":[{"index":0,"finishReason":"STOP","groundingMetadata":{"webSearchQueries":["f1 winner 2026"],"groundingChunks":[{"web":{"uri":"https://x/1","title":"formula1.com"}}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-3.5-flash"}`,
	}
	_, webSearchItems := collectStreamWebSearchItems(t, chunks)

	if len(webSearchItems) != 1 {
		t.Fatalf("expected exactly 1 web_search_call, got %d", len(webSearchItems))
	}
	// Fatal, not Error: every assertion below reads through this key, so continuing past a
	// miss would panic on the nil action and bury the real failure.
	if _, ok := webSearchItems["nqh2j2zy"]; !ok {
		t.Fatalf("web_search_call did not use the model's own tool call id, got %v", webSearchItems)
	}
	a := webSearchItems["nqh2j2zy"]
	if len(a.Queries) != 2 {
		t.Errorf("expected the toolCall's 2 queries, got %v", a.Queries)
	}
	if len(a.Sources) != 1 {
		t.Errorf("expected grounding sources merged in, got %d", len(a.Sources))
	}
}

// Every event in the web_search_call lifecycle correlates by the top-level item_id.
// output_item.added set only Item.ID, so consumers keying on item_id could not match
// the added event to the web_search_call.* events that follow it.
func TestServerSideToolCallStreamingOutputItemAddedCarriesItemID(t *testing.T) {
	chunks := []string{
		`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"thoughtSignature":"c2ln","toolCall":{"toolType":"GOOGLE_SEARCH_WEB","args":{"queries":["f1 winner 2026"]},"id":"nqh2j2zy"}}]}}],"modelVersion":"gemini-3.5-flash"}`,
		`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"thoughtSignature":"c2ln","toolResponse":{"toolType":"GOOGLE_SEARCH_WEB","response":{"search_suggestions":"<div/>"},"id":"nqh2j2zy"}}]}}],"modelVersion":"gemini-3.5-flash"}`,
		`{"candidates":[{"index":0,"finishReason":"STOP","groundingMetadata":{"webSearchQueries":["f1 winner 2026"],"groundingChunks":[{"web":{"uri":"https://x/1","title":"formula1.com"}}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-3.5-flash"}`,
	}

	state := acquireGeminiResponsesStreamState()
	defer releaseGeminiResponsesStreamState(state)

	seen := 0
	seq := 0
	for _, c := range chunks {
		var resp GenerateContentResponse
		if err := json.Unmarshal([]byte(c), &resp); err != nil {
			t.Fatal(err)
		}
		events, err := resp.ToBifrostResponsesStream(seq, state)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			if e.Type != schemas.ResponsesStreamResponseTypeOutputItemAdded {
				continue
			}
			if e.Item == nil || e.Item.Type == nil || *e.Item.Type != schemas.ResponsesMessageTypeWebSearchCall {
				continue
			}
			seen++
			if e.ItemID == nil {
				t.Fatalf("output_item.added for a web_search_call carried no top-level ItemID: %+v", e.Item)
			}
			if e.Item.ID == nil || *e.ItemID != *e.Item.ID {
				t.Fatalf("top-level ItemID %v does not match Item.ID %v", e.ItemID, e.Item.ID)
			}
		}
		seq += len(events)
	}

	if seen == 0 {
		t.Fatal("no output_item.added event was emitted for the web_search_call")
	}
}
