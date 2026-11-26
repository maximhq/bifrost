package gemini

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Gemini's streaming and non-streaming egress disagreed about whether
// encrypted_content is already base64.
//
// encrypted_content is a base64 STRING; Part.ThoughtSignature is a []byte that
// Part.MarshalJSON base64-encodes on the way out. The non-streaming converters
// decoded first, so the wire carried base64(signature). The streaming converter
// assigned []byte(encryptedContent) straight through, so the wire carried
// base64(base64(signature)) -- a value Gemini cannot verify, for the same
// conversation, depending only on whether the client streamed.
//
// Both paths now go through thoughtSignatureFromEncryptedContent, so they cannot
// drift apart again.
func TestThoughtSignatureFromEncryptedContent(t *testing.T) {
	raw := []byte{0x01, 0x02, 0xff, 0xfe, 0x7f}
	encoded := base64.StdEncoding.EncodeToString(raw)

	t.Run("decodes to the original bytes", func(t *testing.T) {
		require.Equal(t, raw, thoughtSignatureFromEncryptedContent(&encoded))
	})

	t.Run("serialises to single-encoded base64", func(t *testing.T) {
		part := &Part{ThoughtSignature: thoughtSignatureFromEncryptedContent(&encoded)}
		data, err := part.MarshalJSON()
		require.NoError(t, err)

		onWire := gjson.GetBytes(data, "thoughtSignature").String()
		require.Equal(t, encoded, onWire,
			"thoughtSignature must round-trip to the value the client sent, not a re-encoding of it")

		// The specific corruption this guards against: passing the base64 string
		// through as bytes yields base64(base64(sig)), which is strictly longer
		// and decodes to the base64 TEXT rather than the signature.
		doubled := base64.StdEncoding.EncodeToString([]byte(encoded))
		require.NotEqual(t, doubled, onWire, "signature was double-encoded")
	})

	t.Run("rejects unusable values rather than corrupting them", func(t *testing.T) {
		require.Nil(t, thoughtSignatureFromEncryptedContent(nil))
		require.Nil(t, thoughtSignatureFromEncryptedContent(ptr("")))
		// Not valid base64: dropping it beats shipping a signature Gemini will
		// reject, and beats a partial decode.
		require.Nil(t, thoughtSignatureFromEncryptedContent(ptr("not!valid!base64!")))
	})
}

func ptr(s string) *string { return &s }

// Gemini 3 carries thoughtSignature on the thought part itself, and requires it
// back on replay -- there is a dedicated finish reason for its absence
// (FinishReasonMissingThoughtSignature). The ingress converter read only
// part.Text, so the signature never reached the client and could never be
// replayed; a signature-only thought part produced no message at all.
func TestThoughtPartIngressPreservesSignature(t *testing.T) {
	raw := []byte{0x0a, 0x0b, 0x0c, 0xff}
	encoded := base64.StdEncoding.EncodeToString(raw)

	t.Run("signature survives alongside text", func(t *testing.T) {
		msgs := responsesMessagesForThoughtPart(t, &Part{
			Thought: true, Text: "step by step", ThoughtSignature: raw,
		})
		require.Len(t, msgs, 1)
		require.NotNil(t, msgs[0].ResponsesReasoning, "signature dropped: no reasoning payload")
		require.NotNil(t, msgs[0].ResponsesReasoning.EncryptedContent)
		require.Equal(t, encoded, *msgs[0].ResponsesReasoning.EncryptedContent)
		// Round trip: what egress decodes must equal what Gemini sent.
		require.Equal(t, raw, thoughtSignatureFromEncryptedContent(msgs[0].ResponsesReasoning.EncryptedContent))
	})

	t.Run("signature-only thought part is not dropped", func(t *testing.T) {
		msgs := responsesMessagesForThoughtPart(t, &Part{Thought: true, ThoughtSignature: raw})
		require.Len(t, msgs, 1, "a signature-only thought part must still produce a message")
		require.NotNil(t, msgs[0].ResponsesReasoning)
		require.Equal(t, encoded, *msgs[0].ResponsesReasoning.EncryptedContent)
	})

	t.Run("thought part with neither text nor signature emits nothing", func(t *testing.T) {
		require.Empty(t, responsesMessagesForThoughtPart(t, &Part{Thought: true}))
	})
}

// Non-streamed reasoning items carry an id, like the streaming path and OpenAI. Clients key
// output items by id (opentelemetry-instrumentation-openai drops the span on a null one).
// The id must not look like OpenAI's rs_, which the OpenAI converter keeps on replay and
// OpenAI rejects when it did not issue it.
func TestNonStreamReasoningItemHasID(t *testing.T) {
	for name, part := range map[string]*Part{
		"thought text only":               {Thought: true, Text: "thinking"},
		"thought text and signature":      {Thought: true, Text: "thinking", ThoughtSignature: []byte{0x01, 0x02}},
		"signature on a non-thought part": {ThoughtSignature: []byte{0x03, 0x04}},
	} {
		t.Run(name, func(t *testing.T) {
			reasoning := responsesMessagesForThoughtPart(t, part)
			require.Len(t, reasoning, 1)
			require.NotNil(t, reasoning[0].ID, "reasoning item id is null")
			require.NotEmpty(t, *reasoning[0].ID)
			require.False(t, strings.HasPrefix(*reasoning[0].ID, "rs_"), "id %q mimics an OpenAI id", *reasoning[0].ID)
			require.Regexp(t, `^msg_\w+_reasoning_\d+$`, *reasoning[0].ID, "not the streaming path's generateItemID form")
			wire, err := json.Marshal(reasoning[0])
			require.NoError(t, err)
			require.True(t, gjson.GetBytes(wire, "summary").IsArray(), "reasoning item has no summary array: %s", wire)
		})
	}

	t.Run("items of one response share the message id and differ by index", func(t *testing.T) {
		out := convertGeminiCandidatesToResponsesOutput([]*Candidate{{Content: &Content{Parts: []*Part{
			{Thought: true, Text: "first"}, {Text: "OK"}, {Thought: true, Text: "second"},
		}}}}, "msg_test")
		var ids []string
		for _, msg := range out {
			ids = append(ids, *msg.ID)
		}
		require.Len(t, ids, 3)
		prefix := strings.TrimSuffix(ids[0], "_reasoning_0")
		require.Regexp(t, `^msg_\w+$`, prefix)
		require.Equal(t, []string{prefix + "_reasoning_0", prefix + "_item_1", prefix + "_reasoning_2"}, ids)
	})

	// A text-only thought replays to Gemini through the content-block path; giving it an
	// empty ResponsesReasoning would route it to the summary path and drop the text.
	t.Run("text-only thought still replays to Gemini", func(t *testing.T) {
		contents, _, err := convertResponsesMessagesToGeminiContents(responsesMessagesForThoughtPart(t, &Part{Thought: true, Text: "thinking"}), "gemini-2.5-flash", schemas.Gemini)
		require.NoError(t, err)
		wire, err := json.Marshal(contents)
		require.NoError(t, err)
		require.Contains(t, string(wire), `"thought":true,"text":"thinking"`)
		require.Equal(t, 1, strings.Count(string(wire), `"text":"thinking"`), "thought text must replay exactly once: %s", wire)
	})
}

// Item ids are scoped by the response's unique id, so two responses converted in the same
// second (CreatedAt has whole-second precision) never share reasoning or text item ids.
func TestNonStreamItemIDsUniqueAcrossResponsesInSameSecond(t *testing.T) {
	convert := func() *schemas.BifrostResponsesResponse {
		return (&GenerateContentResponse{Candidates: []*Candidate{{Content: &Content{Role: "model", Parts: []*Part{
			{Thought: true, Text: "thinking"}, {Text: "OK"},
		}}}}}).ToResponsesBifrostResponsesResponse()
	}
	first, second := convert(), convert()
	for first.CreatedAt != second.CreatedAt { // crossed a second boundary; retry so the case is exercised
		first, second = convert(), convert()
	}
	seen := map[string]bool{}
	for _, resp := range []*schemas.BifrostResponsesResponse{first, second} {
		require.Len(t, resp.Output, 2)
		for _, item := range resp.Output {
			require.NotNil(t, item.ID, "item without an id: %+v", item)
			require.False(t, seen[*item.ID], "item id %q reused across two responses created in the same second", *item.ID)
			seen[*item.ID] = true
		}
	}
}

// Two streams started in the same second get distinct response ids and item ids. The stream
// state's MessageID is both the response id and the scope of every streamed item id.
func TestStreamIDsUniqueAcrossStreamsInSameSecond(t *testing.T) {
	run := func() (int, []string) {
		state := acquireGeminiResponsesStreamState()
		defer releaseGeminiResponsesStreamState(state)
		chunk := &GenerateContentResponse{Candidates: []*Candidate{{Content: &Content{Role: "model", Parts: []*Part{
			{Thought: true, Text: "thinking"}, {Text: "OK"},
		}}}}}
		events, bifrostErr := chunk.ToBifrostResponsesStream(0, state)
		require.Nil(t, bifrostErr)
		var ids []string
		for _, ev := range events {
			if ev.Type == schemas.ResponsesStreamResponseTypeCreated && ev.Response != nil && ev.Response.ID != nil {
				ids = append(ids, "response:"+*ev.Response.ID)
			}
			if ev.Type == schemas.ResponsesStreamResponseTypeOutputItemAdded && ev.Item != nil && ev.Item.ID != nil {
				ids = append(ids, "item:"+*ev.Item.ID)
			}
		}
		require.GreaterOrEqual(t, len(ids), 3, "expected a response id and two item ids: %v", ids)
		return state.CreatedAt, ids
	}
	createdA, idsA := run()
	createdB, idsB := run()
	for createdA != createdB { // crossed a second boundary; retry so the case is exercised
		createdA, idsA = run()
		createdB, idsB = run()
	}
	seen := map[string]bool{}
	for _, id := range append(idsA, idsB...) {
		require.False(t, seen[id], "%s reused across two streams started in the same second", id)
		seen[id] = true
	}
}

// Server-side ToolCall and ToolResponse parts can carry a thoughtSignature; the reasoning
// item emitted for it needs an id like every other reasoning item.
func TestNonStreamSignatureOnToolPartsHasID(t *testing.T) {
	sig := []byte{0x05, 0x06}
	for name, part := range map[string]*Part{
		"tool call":     {ToolCall: &ToolCall{ToolType: "GOOGLE_SEARCH_WEB", ID: "tc1", Args: map[string]any{"queries": []any{"x"}}}, ThoughtSignature: sig},
		"tool response": {ToolResponse: &ToolResponse{ToolType: "GOOGLE_SEARCH_WEB", ID: "tc1", Response: map[string]any{}}, ThoughtSignature: sig},
	} {
		t.Run(name, func(t *testing.T) {
			resp := (&GenerateContentResponse{Candidates: []*Candidate{{Content: &Content{Role: "model", Parts: []*Part{part}}}}}).ToResponsesBifrostResponsesResponse()
			var found bool
			for _, item := range resp.Output {
				if item.Type != nil && *item.Type == schemas.ResponsesMessageTypeReasoning {
					found = true
					require.NotNil(t, item.ID, "reasoning item for the signature has no id")
					require.Regexp(t, `^msg_\w+_reasoning_\d+$`, *item.ID)
				}
			}
			require.True(t, found, "no reasoning item emitted for the signature: %+v", resp.Output)
		})
	}
}

// Non-streamed item ids are replayed as-is to OpenAI and Azure, which reject any item id
// longer than 64 characters ("Invalid 'input[1].id': string too long"). Scoping them by the
// 55-character response id produced 66-71 character ids; they must fit like streamed ids do.
func TestNonStreamItemIDsFitOpenAIIDLimit(t *testing.T) {
	parts := []*Part{{Thought: true, Text: "thinking"}, {Text: "OK"}}
	for i := 0; i < 10; i++ { // two-digit output indexes
		parts = append(parts, &Part{Thought: true, Text: "more"}, &Part{Text: "more"})
	}
	resp := (&GenerateContentResponse{Candidates: []*Candidate{{
		Content:           &Content{Role: "model", Parts: parts},
		GroundingMetadata: &GroundingMetadata{WebSearchQueries: []string{"bifrost"}},
	}}}).ToResponsesBifrostResponsesResponse()
	require.Greater(t, len(resp.Output), 20)
	for _, item := range resp.Output {
		require.NotNil(t, item.ID, "item without an id: %+v", item)
		require.LessOrEqual(t, len(*item.ID), 64, "item id %q is %d characters", *item.ID, len(*item.ID))
	}
}

// A web_search_call built from groundingMetadata (no server-side toolCall part to take an id
// from) carries an id in the streaming path's generateItemID("ws", index) form, not null.
func TestNonStreamGroundingWebSearchCallHasID(t *testing.T) {
	resp := (&GenerateContentResponse{Candidates: []*Candidate{{
		Content:           &Content{Role: "model", Parts: []*Part{{Text: "OK"}}},
		GroundingMetadata: &GroundingMetadata{WebSearchQueries: []string{"bifrost"}},
	}}}).ToResponsesBifrostResponsesResponse()
	var found bool
	for _, item := range resp.Output {
		if item.Type != nil && *item.Type == schemas.ResponsesMessageTypeWebSearchCall {
			found = true
			require.NotNil(t, item.ID, "web_search_call id is null")
			require.Regexp(t, `^msg_\w+_ws_\d+$`, *item.ID)
		}
	}
	require.True(t, found, "no web_search_call emitted: %+v", resp.Output)
}

// responsesMessagesForThoughtPart runs the real ingress converter over a single
// candidate holding one part, and returns the reasoning messages it produced.
func responsesMessagesForThoughtPart(t *testing.T, part *Part) []schemas.ResponsesMessage {
	t.Helper()
	out := convertGeminiCandidatesToResponsesOutput([]*Candidate{
		{Content: &Content{Parts: []*Part{part}}},
	}, "msg_test")
	var reasoning []schemas.ResponsesMessage
	for _, msg := range out {
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeReasoning {
			reasoning = append(reasoning, msg)
		}
	}
	return reasoning
}

// Gemini requires thought blocks back verbatim on replay: "You MUST always
// resend all thought blocks exactly as they were received from the model" and
// "You should NOT remove or modify thought blocks from the history"
// (https://ai.google.dev/gemini-api/docs/thinking). Keeping a block's signature
// while discarding its text IS modifying it.
//
// Both converters did exactly that, in two different ways.

// Egress: a reasoning item that follows a function call is scanned for its
// signature and then marked consumed, so the main loop skips it before its
// summary text can be emitted as a thought part. A reasoning item carrying BOTH
// text and a signature therefore reached Gemini as signature-only - and only
// when it happened to sit after a function call, so the same item survived
// intact in any other position.
func TestEgressKeepsThoughtTextAlongsideSignature(t *testing.T) {
	raw := []byte{0x11, 0x22, 0x33}
	encoded := base64.StdEncoding.EncodeToString(raw)
	const summary = "I should call get_time for Tokyo."

	resp := &schemas.BifrostResponsesResponse{
		Output: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Name:      schemas.Ptr("get_time"),
					CallID:    schemas.Ptr("call_1"),
					Arguments: schemas.Ptr(`{"timezone":"Asia/Tokyo"}`),
				},
			},
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{{Text: summary}},
					EncryptedContent: &encoded,
				},
			},
		},
	}

	out := ToGeminiResponsesResponse(resp)
	require.NotNil(t, out)

	// Counted, not just observed. A boolean would be satisfied by a converter
	// that emits the text twice or the signature twice, and duplication is the
	// specific hazard here: the reasoning branch further down emits its own
	// signature-only part, so carrying the signature in both places would put
	// the same value on the wire twice - which Gemini would see as a modified
	// history just as surely as a missing one.
	var signatures, thoughtTexts int
	for _, cand := range out.Candidates {
		if cand == nil || cand.Content == nil {
			continue
		}
		for _, p := range cand.Content.Parts {
			if p == nil {
				continue
			}
			if len(p.ThoughtSignature) > 0 {
				signatures++
				require.Equal(t, raw, p.ThoughtSignature)
			}
			if p.Thought && p.Text == summary {
				thoughtTexts++
			}
		}
	}

	require.Equal(t, 1, signatures, "the signature must appear exactly once, on the function call")
	require.Equal(t, 1, thoughtTexts,
		"the thought TEXT must appear exactly once - dropped means the block reached Gemini modified, duplicated means it was rewritten")
}

// Ingress: every standalone reasoning message was skipped outright, so reasoning
// text never reached Gemini on this path at all - independent of any look-ahead,
// and independent of whether a signature was present.
func TestIngressSendsThoughtTextForStandaloneReasoning(t *testing.T) {
	const summary = "Breaking the problem into two steps."
	encoded := base64.StdEncoding.EncodeToString([]byte{0x44, 0x55})

	contents, _, err := convertResponsesMessagesToGeminiContents([]schemas.ResponsesMessage{
		{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Solve it.")},
		},
		{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			ResponsesReasoning: &schemas.ResponsesReasoning{
				Summary:          []schemas.ResponsesReasoningSummary{{Text: summary}},
				EncryptedContent: &encoded,
			},
		},
	}, "gemini-2.5-flash", schemas.Gemini)
	require.NoError(t, err)

	var thoughtTexts, signatures int
	for _, c := range contents {
		for _, p := range c.Parts {
			if p == nil {
				continue
			}
			if p.Thought && p.Text == summary {
				thoughtTexts++
			}
			if len(p.ThoughtSignature) > 0 {
				signatures++
			}
		}
	}
	require.Equal(t, 1, thoughtTexts,
		"a standalone reasoning message must contribute its thought block exactly once")
	// No function call precedes this item, so nothing else can be carrying its
	// signature - dropping it here loses the value Gemini needs back.
	require.Equal(t, 1, signatures,
		"the standalone reasoning item's signature was lost: no preceding function call consumed it")
}

// BenchmarkNonStreamReasoningOutput measures converting a thought part to a reasoning item.
func BenchmarkNonStreamReasoningOutput(b *testing.B) {
	candidates := []*Candidate{{Content: &Content{Parts: []*Part{{Thought: true, Text: "thinking"}, {Text: "OK"}}}}}
	b.ReportAllocs()
	for b.Loop() {
		_ = convertGeminiCandidatesToResponsesOutput(candidates, "msg_test")
	}
}

// TestNonStreamThoughtTextRendersOnceInGenAIOutput pins that a non-streamed thought keeps
// its text exactly once when the Responses output is rendered back as a GenAI response. The
// renderer converts every message's content blocks, so reading the reasoning_text block a
// second time from the reasoning branch would send the thought twice.
func TestNonStreamThoughtTextRendersOnceInGenAIOutput(t *testing.T) {
	for name, part := range map[string]*Part{
		"thought text only":          {Thought: true, Text: "thinking"},
		"thought text and signature": {Thought: true, Text: "thinking", ThoughtSignature: []byte{0x01, 0x02}},
	} {
		t.Run(name, func(t *testing.T) {
			bifrostResp := (&GenerateContentResponse{Candidates: []*Candidate{{Content: &Content{Role: "model", Parts: []*Part{
				part, {Text: "OK"},
			}}}}}).ToResponsesBifrostResponsesResponse()
			genai := ToGeminiResponsesResponse(bifrostResp)
			require.NotEmpty(t, genai.Candidates)
			var thoughts int
			for _, p := range genai.Candidates[0].Content.Parts {
				if p.Thought && p.Text == "thinking" {
					thoughts++
				}
			}
			wire, err := json.Marshal(genai.Candidates[0].Content.Parts)
			require.NoError(t, err)
			require.Equal(t, 1, thoughts, "thought text must render exactly once: %s", wire)
		})
	}
}
