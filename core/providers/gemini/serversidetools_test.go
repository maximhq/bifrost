package gemini

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Gemini reports built-in tools it ran itself as toolCall/toolResponse part pairs when
// toolConfig.includeServerSideToolInvocations is enabled. Sample shape taken from a live
// gemini-3.5-flash response (signatures and payloads shortened).
const serverSideToolResponseJSON = `{
  "candidates": [{
    "index": 0,
    "groundingMetadata": {
      "webSearchQueries": ["most recent F1 race winner", "F1 results"],
      "groundingChunks": [
        {"web": {"uri": "https://vertexaisearch.cloud.google.com/x1", "title": "formula1.com"}},
        {"web": {"uri": "https://vertexaisearch.cloud.google.com/x2", "title": "aljazeera.com"}}
      ],
      "groundingSupports": [
        {"segment": {"startIndex": 0, "endIndex": 41, "text": "Lando Norris of McLaren won the most recent"}, "groundingChunkIndices": [0, 1]}
      ]
    },
    "content": {
      "role": "model",
      "parts": [
        {"thoughtSignature": "c2lnLXRvb2xjYWxs",
         "toolCall": {"toolType": "GOOGLE_SEARCH_WEB", "args": {"queries": ["most recent F1 race winner", "Formula 1 2026 schedule"]}, "id": "nqh2j2zy"}},
        {"thoughtSignature": "c2lnLXRvb2xyZXNwb25zZQ==",
         "toolResponse": {"toolType": "GOOGLE_SEARCH_WEB", "response": {"search_suggestions": "<style>.chip{}</style><div>chips</div>"}, "id": "nqh2j2zy"}},
        {"text": "Lando Norris of McLaren won the most recent race.", "thoughtSignature": "c2lnLXRleHQ="}
      ]
    },
    "finishReason": "STOP"
  }],
  "modelVersion": "gemini-3.5-flash",
  "responseId": "Hi97aunyEZGBqfkP0ITXkQQ"
}`

// TestServerSideToolCallNonStreaming covers the non-streaming half: the new parts map onto a
// single web_search_call item (Gemini's own call ID, its queries, grounding's sources) and
// round-trip back to byte-faithful toolCall/toolResponse parts on the native GenAI path.
func TestServerSideToolCallNonStreaming(t *testing.T) {
	var g GenerateContentResponse
	require.NoError(t, json.Unmarshal([]byte(serverSideToolResponseJSON), &g))

	b := g.ToResponsesBifrostResponsesResponse()

	var searchCalls []schemas.ResponsesMessage
	reasoningCount := 0
	for _, m := range b.Output {
		if m.Type == nil {
			continue
		}
		switch *m.Type {
		case schemas.ResponsesMessageTypeWebSearchCall:
			searchCalls = append(searchCalls, m)
		case schemas.ResponsesMessageTypeReasoning:
			reasoningCount++
		}
	}

	// Exactly one search item: the server-side call absorbs the grounding-derived one.
	require.Len(t, searchCalls, 1, "grounding must not add a second web_search_call")
	call := searchCalls[0]
	require.NotNil(t, call.ID)
	assert.Equal(t, "nqh2j2zy", *call.ID, "item must carry Gemini's own tool call id")
	require.NotNil(t, call.Status)
	assert.Equal(t, "completed", *call.Status, "toolResponse completes the call")

	action := call.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction
	assert.Equal(t, []string{"most recent F1 race winner", "Formula 1 2026 schedule"}, action.Queries,
		"queries come from the toolCall, not grounding")
	assert.Len(t, action.Sources, 2, "grounding sources merge onto the same item")

	// Signatures on the tool parts must survive as reasoning items (Gemini requires them
	// back on replay). The text part's signature rides on the text message instead, so only
	// the two tool parts produce standalone reasoning items.
	assert.Equal(t, 2, reasoningCount, "each tool part's thoughtSignature must survive")

	// Native round-trip: the exact parts Google sent, in order, with payloads intact.
	back := ToGeminiResponsesResponse(b)
	require.Len(t, back.Candidates, 1)
	parts := back.Candidates[0].Content.Parts
	require.Len(t, parts, 3, "no duplicate signature-only parts")

	require.NotNil(t, parts[0].ToolCall)
	assert.Equal(t, "GOOGLE_SEARCH_WEB", parts[0].ToolCall.ToolType)
	assert.Equal(t, "nqh2j2zy", parts[0].ToolCall.ID)
	assert.Equal(t, []any{"most recent F1 race winner", "Formula 1 2026 schedule"}, parts[0].ToolCall.Args["queries"])
	assert.NotEmpty(t, parts[0].ThoughtSignature)

	require.NotNil(t, parts[1].ToolResponse)
	assert.Equal(t, "nqh2j2zy", parts[1].ToolResponse.ID)
	assert.Contains(t, parts[1].ToolResponse.Response["search_suggestions"], "chips")
	assert.NotEmpty(t, parts[1].ThoughtSignature)

	assert.Equal(t, "Lando Norris of McLaren won the most recent race.", parts[2].Text)
}

// TestServerSideToolCallUnknownToolType checks an unmapped built-in tool is preserved on the
// native path rather than dropped, even though it has no web_search_call equivalent.
func TestServerSideToolCallUnknownToolType(t *testing.T) {
	body := `{"candidates":[{"index":0,"content":{"role":"model","parts":[
		{"thoughtSignature":"c2ln","toolCall":{"toolType":"CODE_EXECUTION","args":{"code":"print(1)"},"id":"abc"}},
		{"text":"1"}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.5-flash"}`
	var g GenerateContentResponse
	require.NoError(t, json.Unmarshal([]byte(body), &g))

	b := g.ToResponsesBifrostResponsesResponse()
	for _, m := range b.Output {
		if m.Type != nil && *m.Type == schemas.ResponsesMessageTypeWebSearchCall {
			t.Fatal("a non-search tool type must not become a web_search_call")
		}
	}

	back := ToGeminiResponsesResponse(b)
	parts := back.Candidates[0].Content.Parts
	require.NotNil(t, parts[0].ToolCall)
	assert.Equal(t, "CODE_EXECUTION", parts[0].ToolCall.ToolType)
	assert.Equal(t, "print(1)", parts[0].ToolCall.Args["code"])
}

// Shape observed on a live gemini-3.6-flash call: two server-side search rounds, no
// groundingMetadata at all, and a client functionCall in the same candidate. Verifies each
// round becomes its own item (paired by id) and that the function call is unaffected.
const liveTwoSearchRoundsJSON = `{
 "candidates":[{"index":0,"finishReason":"STOP","finishMessage":"Model generated function call(s).",
  "content":{"role":"model","parts":[
   {"thoughtSignature":"c2lnMQ==","toolCall":{"toolType":"GOOGLE_SEARCH_WEB","args":{"queries":["FIFA World Cup winner 2026","2026 FIFA World Cup winner"]},"id":"BaoigkFu"}},
   {"toolResponse":{"id":"BaoigkFu","toolType":"GOOGLE_SEARCH_WEB","response":{"search_suggestions":"<div>chips1</div>"}},"thoughtSignature":"c2lnMg=="},
   {"thoughtSignature":"c2lnMw==","toolCall":{"toolType":"GOOGLE_SEARCH_WEB","args":{"queries":["\"2026 FIFA World Cup\" score final Spain Argentina"]},"id":"Fbe8aaSI"}},
   {"toolResponse":{"toolType":"GOOGLE_SEARCH_WEB","response":{"search_suggestions":"<div>chips2</div>"},"id":"Fbe8aaSI"},"thoughtSignature":"c2lnNA=="},
   {"functionCall":{"args":{"city":"New York"},"id":"ZKIkmNGR","name":"get_weather"},"thoughtSignature":"c2lnNQ=="}
  ]}}],
 "usageMetadata":{"serviceTier":"standard","promptTokenCount":1489,"candidatesTokenCount":17,"totalTokenCount":2010,"cachedContentTokenCount":445,"thoughtsTokenCount":504},
 "modelVersion":"gemini-3.6-flash","responseId":"SDZ7asHCNM3CjuMPgPiD8AE"}`

func TestServerSideToolCallMultipleRoundsWithFunctionCall(t *testing.T) {
	var g GenerateContentResponse
	require.NoError(t, json.Unmarshal([]byte(liveTwoSearchRoundsJSON), &g))

	b := g.ToResponsesBifrostResponsesResponse()

	var searchIDs []string
	functionCalls := 0
	for _, m := range b.Output {
		if m.Type == nil {
			continue
		}
		switch *m.Type {
		case schemas.ResponsesMessageTypeWebSearchCall:
			require.NotNil(t, m.ID)
			require.NotNil(t, m.Status)
			assert.Equal(t, "completed", *m.Status)
			searchIDs = append(searchIDs, *m.ID)
		case schemas.ResponsesMessageTypeFunctionCall:
			functionCalls++
		}
	}
	assert.Equal(t, []string{"BaoigkFu", "Fbe8aaSI"}, searchIDs, "one item per search round, paired by id")
	assert.Equal(t, 1, functionCalls, "the client function call must survive alongside them")

	// Every part round-trips in order, including the function call.
	back := ToGeminiResponsesResponse(b)
	parts := back.Candidates[0].Content.Parts
	require.Len(t, parts, 5)
	require.NotNil(t, parts[0].ToolCall)
	assert.Equal(t, "BaoigkFu", parts[0].ToolCall.ID)
	require.NotNil(t, parts[1].ToolResponse)
	assert.Equal(t, "BaoigkFu", parts[1].ToolResponse.ID)
	require.NotNil(t, parts[2].ToolCall)
	assert.Equal(t, "Fbe8aaSI", parts[2].ToolCall.ID)
	require.NotNil(t, parts[3].ToolResponse)
	assert.Equal(t, "Fbe8aaSI", parts[3].ToolResponse.ID)
	require.NotNil(t, parts[4].FunctionCall)
	assert.Equal(t, "get_weather", parts[4].FunctionCall.Name)
	for i, p := range parts {
		assert.NotEmpty(t, p.ThoughtSignature, "part %d lost its thoughtSignature", i)
	}
}

// Two search rounds WITH groundingMetadata. The forward path merges grounding's sources
// onto the FIRST search call; the reverse path rebuilt groundingMetadata from the LAST
// web_search_call, so with 2+ rounds the sources fell off the round trip entirely.
const liveTwoSearchRoundsWithGroundingJSON = `{
 "candidates":[{"index":0,"finishReason":"STOP",
  "groundingMetadata":{
    "webSearchQueries":["FIFA World Cup winner 2026"],
    "groundingChunks":[
      {"web":{"uri":"https://vertexaisearch.cloud.google.com/g1","title":"fifa.com"}},
      {"web":{"uri":"https://vertexaisearch.cloud.google.com/g2","title":"bbc.com"}}
    ]
  },
  "content":{"role":"model","parts":[
   {"thoughtSignature":"c2lnMQ==","toolCall":{"toolType":"GOOGLE_SEARCH_WEB","args":{"queries":["FIFA World Cup winner 2026"]},"id":"BaoigkFu"}},
   {"toolResponse":{"id":"BaoigkFu","toolType":"GOOGLE_SEARCH_WEB","response":{"search_suggestions":"<div>chips1</div>"}},"thoughtSignature":"c2lnMg=="},
   {"thoughtSignature":"c2lnMw==","toolCall":{"toolType":"GOOGLE_SEARCH_WEB","args":{"queries":["2026 final score"]},"id":"Fbe8aaSI"}},
   {"toolResponse":{"toolType":"GOOGLE_SEARCH_WEB","response":{"search_suggestions":"<div>chips2</div>"},"id":"Fbe8aaSI"},"thoughtSignature":"c2lnNA=="},
   {"text":"Spain won.","thoughtSignature":"c2lnNQ=="}
  ]}}],
 "modelVersion":"gemini-3.6-flash","responseId":"grounded-two-rounds"}`

func TestServerSideToolCallMultipleRoundsPreservesGroundingSources(t *testing.T) {
	var g GenerateContentResponse
	require.NoError(t, json.Unmarshal([]byte(liveTwoSearchRoundsWithGroundingJSON), &g))

	b := g.ToResponsesBifrostResponsesResponse()

	// Exactly one of the two search items carries the sources on the forward path.
	sourced := 0
	for _, m := range b.Output {
		if m.Type == nil || *m.Type != schemas.ResponsesMessageTypeWebSearchCall {
			continue
		}
		if m.ResponsesToolMessage != nil && m.ResponsesToolMessage.Action != nil {
			if a := m.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction; a != nil && len(a.Sources) > 0 {
				sourced++
			}
		}
	}
	require.Equal(t, 1, sourced, "grounding sources land on exactly one of the two search items")

	// The reverse path must find that item rather than blindly taking the last one.
	back := ToGeminiResponsesResponse(b)
	require.NotEmpty(t, back.Candidates)
	require.NotNil(t, back.Candidates[0].GroundingMetadata, "grounding metadata must survive the round trip")
	assert.Len(t, back.Candidates[0].GroundingMetadata.GroundingChunks, 2,
		"both grounding chunks must survive; picking the last web_search_call drops them")
}

// Both IDs carry omitempty, so Gemini can omit them. Keying searchCallIndexByID on the
// empty string collapsed every round onto one entry: earlier rounds stayed in_progress
// and every emitted item carried an empty ID.
const twoSearchRoundsNoIDsJSON = `{
 "candidates":[{"index":0,"finishReason":"STOP",
  "content":{"role":"model","parts":[
   {"thoughtSignature":"c2lnMQ==","toolCall":{"toolType":"GOOGLE_SEARCH_WEB","args":{"queries":["round one"]}}},
   {"toolResponse":{"toolType":"GOOGLE_SEARCH_WEB","response":{"search_suggestions":"<div>a</div>"}},"thoughtSignature":"c2lnMg=="},
   {"thoughtSignature":"c2lnMw==","toolCall":{"toolType":"GOOGLE_SEARCH_WEB","args":{"queries":["round two"]}}},
   {"toolResponse":{"toolType":"GOOGLE_SEARCH_WEB","response":{"search_suggestions":"<div>b</div>"}},"thoughtSignature":"c2lnNA=="}
  ]}}],
 "modelVersion":"gemini-3.6-flash","responseId":"no-ids"}`

func TestServerSideToolCallRoundsWithoutIDs(t *testing.T) {
	var g GenerateContentResponse
	require.NoError(t, json.Unmarshal([]byte(twoSearchRoundsNoIDsJSON), &g))

	b := g.ToResponsesBifrostResponsesResponse()

	var ids []string
	for _, m := range b.Output {
		if m.Type == nil || *m.Type != schemas.ResponsesMessageTypeWebSearchCall {
			continue
		}
		require.NotNil(t, m.ID)
		assert.NotEmpty(t, *m.ID, "an ID-less round must still get a synthetic item ID")
		require.NotNil(t, m.Status)
		assert.Equal(t, "completed", *m.Status, "each round's toolResponse must complete its own item")
		ids = append(ids, *m.ID)
	}

	require.Len(t, ids, 2, "two search rounds must produce two items even with no IDs")
	assert.NotEqual(t, ids[0], ids[1], "the two ID-less rounds must not collide on one key")
}

// Gemini interleaves text, tool and thought parts, and thought_signature carries positional
// context. Capturing only the tool parts and prepending them reordered a candidate whose
// text came first.
const textBeforeToolCallJSON = `{
 "candidates":[{"index":0,"finishReason":"STOP",
  "content":{"role":"model","parts":[
   {"text":"Let me look that up.","thoughtSignature":"c2lnVGV4dA=="},
   {"thoughtSignature":"c2lnQ2FsbA==","toolCall":{"toolType":"GOOGLE_SEARCH_WEB","args":{"queries":["who won"]},"id":"abc123"}},
   {"toolResponse":{"id":"abc123","toolType":"GOOGLE_SEARCH_WEB","response":{"search_suggestions":"<div>c</div>"}},"thoughtSignature":"c2lnUmVzcA=="},
   {"text":"Spain won.","thoughtSignature":"c2lnQW5zd2Vy"}
  ]}}],
 "modelVersion":"gemini-3.6-flash","responseId":"interleaved"}`

func TestServerSideToolPartsPreserveOriginalOrder(t *testing.T) {
	var g GenerateContentResponse
	require.NoError(t, json.Unmarshal([]byte(textBeforeToolCallJSON), &g))

	b := g.ToResponsesBifrostResponsesResponse()
	back := ToGeminiResponsesResponse(b)

	require.NotEmpty(t, back.Candidates)
	parts := back.Candidates[0].Content.Parts

	// The leading text must stay ahead of the tool call it introduces.
	firstText, firstTool := -1, -1
	for i, p := range parts {
		if firstText == -1 && p.Text == "Let me look that up." {
			firstText = i
		}
		if firstTool == -1 && p.ToolCall != nil {
			firstTool = i
		}
	}
	require.NotEqual(t, -1, firstText, "the leading text part must survive the round trip")
	require.NotEqual(t, -1, firstTool, "the tool call must survive the round trip")
	assert.Less(t, firstText, firstTool,
		"Gemini emitted text before the toolCall; prepending tool parts inverts that order")
}
