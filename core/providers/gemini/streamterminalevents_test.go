package gemini

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// thinkingStreamChunks returns a minimal thinking stream the way the Gemini API
// delivers it: two thought parts (the second carrying the thoughtSignature),
// then a regular text part, then a final chunk with finishReason and usage.
func thinkingStreamChunks(signature []byte) []*GenerateContentResponse {
	return []*GenerateContentResponse{
		{
			ResponseID:   "resp-thinking",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				Content: &Content{
					Role:  "model",
					Parts: []*Part{{Text: "Consider the units. ", Thought: true}},
				},
			}},
		},
		{
			ResponseID:   "resp-thinking",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				Content: &Content{
					Role:  "model",
					Parts: []*Part{{Text: "Convert metres to feet.", Thought: true, ThoughtSignature: signature}},
				},
			}},
		},
		{
			ResponseID:   "resp-thinking",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				Content: &Content{
					Role:  "model",
					Parts: []*Part{{Text: "330 metres is about 1083 feet."}},
				},
			}},
		},
		{
			ResponseID:   "resp-thinking",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				FinishReason: FinishReasonStop,
			}},
			UsageMetadata: &GenerateContentResponseUsageMetadata{
				PromptTokenCount:     10,
				CandidatesTokenCount: 20,
				TotalTokenCount:      30,
			},
		},
	}
}

// driveResponsesStream replays the given chunk sequence through
// ToBifrostResponsesStream against the given state, mirroring the
// sequence-number accounting of HandleGeminiResponsesStream.
func driveResponsesStream(t *testing.T, state *GeminiResponsesStreamState, chunks []*GenerateContentResponse) []*schemas.BifrostResponsesStreamResponse {
	t.Helper()

	var out []*schemas.BifrostResponsesStreamResponse
	seq := 0
	for _, chunk := range chunks {
		responses, bifrostErr := chunk.ToBifrostResponsesStream(seq, state)
		require.Nil(t, bifrostErr, "unexpected conversion error")
		out = append(out, responses...)
		seq += len(responses)
	}
	return out
}

func collectEventsOfType(responses []*schemas.BifrostResponsesStreamResponse, eventType schemas.ResponsesStreamResponseType) []*schemas.BifrostResponsesStreamResponse {
	var out []*schemas.BifrostResponsesStreamResponse
	for _, r := range responses {
		if r.Type == eventType {
			out = append(out, r)
		}
	}
	return out
}

func findCompletedEvent(t *testing.T, responses []*schemas.BifrostResponsesStreamResponse) *schemas.BifrostResponsesResponse {
	t.Helper()
	for _, r := range responses {
		if r.Type == schemas.ResponsesStreamResponseTypeCompleted {
			require.NotNil(t, r.Response, "response.completed must carry a response payload")
			return r.Response
		}
	}
	t.Fatal("stream did not emit response.completed")
	return nil
}

func itemsOfType(items []schemas.ResponsesMessage, itemType schemas.ResponsesMessageType) []schemas.ResponsesMessage {
	var out []schemas.ResponsesMessage
	for _, item := range items {
		if item.Type != nil && *item.Type == itemType {
			out = append(out, item)
		}
	}
	return out
}

// TestGeminiResponsesStreamThinkingTerminalEvents verifies that consecutive
// thought parts are folded into a single reasoning item whose terminal events
// carry the accumulated thought text and the thoughtSignature. Before the fix
// each thought part produced its own reasoning item, reasoning_summary_text.done
// carried no text, output_item.done carried an empty summary, and a signature
// arriving on a thought part was dropped.
func TestGeminiResponsesStreamThinkingTerminalEvents(t *testing.T) {
	signature := []byte("sig-bytes-1")
	wantText := "Consider the units. Convert metres to feet."
	wantSignature := base64.StdEncoding.EncodeToString(signature)

	state := &GeminiResponsesStreamState{}
	state.flush()
	responses := driveResponsesStream(t, state, thinkingStreamChunks(signature))

	// Both thought parts must share one reasoning item.
	var reasoningAdded []*schemas.BifrostResponsesStreamResponse
	for _, r := range collectEventsOfType(responses, schemas.ResponsesStreamResponseTypeOutputItemAdded) {
		if r.Item != nil && r.Item.Type != nil && *r.Item.Type == schemas.ResponsesMessageTypeReasoning {
			reasoningAdded = append(reasoningAdded, r)
		}
	}
	require.Len(t, reasoningAdded, 1, "consecutive thought parts must open exactly one reasoning item")

	// reasoning_summary_text.done must carry the accumulated thought text.
	summaryTextDone := collectEventsOfType(responses, schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone)
	require.Len(t, summaryTextDone, 1, "expected exactly one reasoning_summary_text.done")
	require.NotNil(t, summaryTextDone[0].Text, "reasoning_summary_text.done must carry the accumulated text")
	assert.Equal(t, wantText, *summaryTextDone[0].Text)

	// output_item.done for the reasoning item must carry the summary and the signature.
	var reasoningDone *schemas.ResponsesMessage
	for _, r := range collectEventsOfType(responses, schemas.ResponsesStreamResponseTypeOutputItemDone) {
		if r.Item != nil && r.Item.Type != nil && *r.Item.Type == schemas.ResponsesMessageTypeReasoning {
			require.Nil(t, reasoningDone, "expected exactly one reasoning output_item.done")
			reasoningDone = r.Item
		}
	}
	require.NotNil(t, reasoningDone, "reasoning item was never completed")
	require.NotNil(t, reasoningDone.ResponsesReasoning, "reasoning output_item.done must keep the reasoning payload")
	require.Len(t, reasoningDone.ResponsesReasoning.Summary, 1, "reasoning output_item.done must carry the accumulated summary")
	assert.Equal(t, schemas.ResponsesReasoningContentBlockTypeSummaryText, reasoningDone.ResponsesReasoning.Summary[0].Type)
	assert.Equal(t, wantText, reasoningDone.ResponsesReasoning.Summary[0].Text)
	require.NotNil(t, reasoningDone.ResponsesReasoning.EncryptedContent, "thoughtSignature arriving on a thought part must be kept")
	assert.Equal(t, wantSignature, *reasoningDone.ResponsesReasoning.EncryptedContent)
}

// TestGeminiResponsesStreamCompletedCarriesOutput verifies that the final
// response.completed event carries the full output array. Before the fix the
// stream path never populated it, so clients reading the terminal snapshot
// (for example openai-python's stream.get_final_response()) saw no output at
// all, while the non-streaming path returned the same content correctly.
func TestGeminiResponsesStreamCompletedCarriesOutput(t *testing.T) {
	signature := []byte("sig-bytes-2")
	state := &GeminiResponsesStreamState{}
	state.flush()
	responses := driveResponsesStream(t, state, thinkingStreamChunks(signature))

	completed := findCompletedEvent(t, responses)
	require.NotEmpty(t, completed.Output, "response.completed must carry the streamed output items")

	reasoningItems := itemsOfType(completed.Output, schemas.ResponsesMessageTypeReasoning)
	require.Len(t, reasoningItems, 1, "completed output must include the reasoning item")
	require.NotNil(t, reasoningItems[0].ResponsesReasoning)
	require.Len(t, reasoningItems[0].ResponsesReasoning.Summary, 1)
	assert.Equal(t, "Consider the units. Convert metres to feet.", reasoningItems[0].ResponsesReasoning.Summary[0].Text)

	messageItems := itemsOfType(completed.Output, schemas.ResponsesMessageTypeMessage)
	require.Len(t, messageItems, 1, "completed output must include the assistant message")
	require.NotNil(t, messageItems[0].Content)
	require.Len(t, messageItems[0].Content.ContentBlocks, 1)
	require.NotNil(t, messageItems[0].Content.ContentBlocks[0].Text)
	assert.Equal(t, "330 metres is about 1083 feet.", *messageItems[0].Content.ContentBlocks[0].Text)

	// Reasoning must come before the message, mirroring the emission order.
	require.NotNil(t, completed.Output[0].Type)
	assert.Equal(t, schemas.ResponsesMessageTypeReasoning, *completed.Output[0].Type)
}

// functionCallStreamChunks returns a stream with a text part and a complete
// function call, the way Gemini delivers tool calls on streaming responses.
func functionCallStreamChunks() []*GenerateContentResponse {
	return []*GenerateContentResponse{
		{
			ResponseID:   "resp-tool",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				Content: &Content{
					Role:  "model",
					Parts: []*Part{{Text: "Checking the weather."}},
				},
			}},
		},
		{
			ResponseID:   "resp-tool",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				Content: &Content{
					Role: "model",
					Parts: []*Part{{
						FunctionCall: &FunctionCall{
							ID:   "call-1",
							Name: "get_weather",
							Args: json.RawMessage(`{"location":"Paris"}`),
						},
					}},
				},
			}},
		},
		{
			ResponseID:   "resp-tool",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				FinishReason: FinishReasonStop,
			}},
			UsageMetadata: &GenerateContentResponseUsageMetadata{
				PromptTokenCount:     5,
				CandidatesTokenCount: 7,
				TotalTokenCount:      12,
			},
		},
	}
}

// TestGeminiResponsesStreamCompletedCarriesFunctionCall verifies that tool
// calls appear in the response.completed output with their arguments.
func TestGeminiResponsesStreamCompletedCarriesFunctionCall(t *testing.T) {
	state := &GeminiResponsesStreamState{}
	state.flush()
	responses := driveResponsesStream(t, state, functionCallStreamChunks())

	completed := findCompletedEvent(t, responses)
	require.NotEmpty(t, completed.Output, "response.completed must carry the streamed output items")

	functionCalls := itemsOfType(completed.Output, schemas.ResponsesMessageTypeFunctionCall)
	require.Len(t, functionCalls, 1, "completed output must include the function call")
	require.NotNil(t, functionCalls[0].ResponsesToolMessage)
	require.NotNil(t, functionCalls[0].ResponsesToolMessage.Name)
	assert.Equal(t, "get_weather", *functionCalls[0].ResponsesToolMessage.Name)
	require.NotNil(t, functionCalls[0].ResponsesToolMessage.Arguments)
	assert.JSONEq(t, `{"location":"Paris"}`, *functionCalls[0].ResponsesToolMessage.Arguments)

	messageItems := itemsOfType(completed.Output, schemas.ResponsesMessageTypeMessage)
	require.Len(t, messageItems, 1, "completed output must include the assistant message")
}

// TestGeminiResponsesStreamCompletedIncludesWebSearchItems verifies that
// grounded streams carry the web_search_call item, the annotated text message,
// and the rendered-content message in the completed output, with their actual
// payloads and not just the right item types.
func TestGeminiResponsesStreamCompletedIncludesWebSearchItems(t *testing.T) {
	state := &GeminiResponsesStreamState{}
	state.flush()
	responses := driveGroundedResponsesStream(t, state)

	completed := findCompletedEvent(t, responses)
	require.NotEmpty(t, completed.Output, "response.completed must carry the streamed output items")

	webSearchItems := itemsOfType(completed.Output, schemas.ResponsesMessageTypeWebSearchCall)
	require.Len(t, webSearchItems, 1, "completed output must include the web_search_call item")

	messageItems := itemsOfType(completed.Output, schemas.ResponsesMessageTypeMessage)
	require.Len(t, messageItems, 2, "completed output must include the text message and the rendered-content message")

	// The text message must keep its text and the grounding citation.
	textMsg := messageItems[0]
	require.NotNil(t, textMsg.Content)
	require.Len(t, textMsg.Content.ContentBlocks, 1)
	textBlock := textMsg.Content.ContentBlocks[0]
	require.NotNil(t, textBlock.Text)
	assert.Equal(t, "The Eiffel Tower is 330 metres tall.", *textBlock.Text)
	require.NotNil(t, textBlock.ResponsesOutputMessageContentText, "text block must keep its text payload")
	annotations := textBlock.Annotations
	require.Len(t, annotations, 1, "the grounding citation must reach the completed snapshot")
	assert.Equal(t, "url_citation", annotations[0].Type)
	require.NotNil(t, annotations[0].URL)
	assert.Equal(t, "https://vertexaisearch.cloud.google.com/grounding-api-redirect/example", *annotations[0].URL)
	require.NotNil(t, annotations[0].Title)
	assert.Equal(t, "toureiffel.paris", *annotations[0].Title)
	require.NotNil(t, annotations[0].StartIndex)
	assert.Equal(t, 0, *annotations[0].StartIndex)
	require.NotNil(t, annotations[0].EndIndex)
	assert.Equal(t, 36, *annotations[0].EndIndex)

	// The rendered-content message must keep the search suggestions payload.
	renderedMsg := messageItems[1]
	require.NotNil(t, renderedMsg.Content)
	require.NotEmpty(t, renderedMsg.Content.ContentBlocks)
	renderedBlock := renderedMsg.Content.ContentBlocks[0]
	assert.Equal(t, schemas.ResponsesOutputMessageContentTypeRenderedContent, renderedBlock.Type)
	require.NotNil(t, renderedBlock.ResponsesOutputMessageContentRenderedContent, "rendered-content block must keep its payload")
	assert.Equal(t, "<div>Google Search Suggestions</div>", renderedBlock.RenderedContent)
}

// inlineDataStreamChunks returns a stream carrying a single inline data part
// (an image) followed by the finish chunk.
func inlineDataStreamChunks() []*GenerateContentResponse {
	return []*GenerateContentResponse{
		{
			ResponseID:   "resp-inline",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				Content: &Content{
					Role:  "model",
					Parts: []*Part{{InlineData: &Blob{MIMEType: "image/png", Data: "aW1n"}}},
				},
			}},
		},
		{
			ResponseID:   "resp-inline",
			ModelVersion: "gemini-2.5-flash",
			Candidates:   []*Candidate{{FinishReason: FinishReasonStop}},
			UsageMetadata: &GenerateContentResponseUsageMetadata{
				PromptTokenCount:     2,
				CandidatesTokenCount: 3,
				TotalTokenCount:      5,
			},
		},
	}
}

// TestGeminiResponsesStreamCompletedCarriesInlineData verifies that an inline
// data item keeps its content block on output_item.done and in the completed
// output, instead of the empty shell the done event used to carry.
func TestGeminiResponsesStreamCompletedCarriesInlineData(t *testing.T) {
	state := &GeminiResponsesStreamState{}
	state.flush()
	responses := driveResponsesStream(t, state, inlineDataStreamChunks())

	var doneItem *schemas.ResponsesMessage
	for _, r := range collectEventsOfType(responses, schemas.ResponsesStreamResponseTypeOutputItemDone) {
		if r.Item != nil && r.Item.Type != nil && *r.Item.Type == schemas.ResponsesMessageTypeMessage {
			doneItem = r.Item
		}
	}
	require.NotNil(t, doneItem, "inline data item was never completed")
	require.NotNil(t, doneItem.Content)
	require.Len(t, doneItem.Content.ContentBlocks, 1, "output_item.done must keep the inline data block")

	completed := findCompletedEvent(t, responses)
	messageItems := itemsOfType(completed.Output, schemas.ResponsesMessageTypeMessage)
	require.Len(t, messageItems, 1, "completed output must include the inline data item")
	require.NotNil(t, messageItems[0].Content)
	require.Len(t, messageItems[0].Content.ContentBlocks, 1, "completed output must keep the inline data payload")
	block := messageItems[0].Content.ContentBlocks[0]
	assert.Equal(t, schemas.ResponsesInputMessageContentBlockTypeImage, block.Type)
	require.NotNil(t, block.ResponsesInputMessageContentBlockImage)
	require.NotNil(t, block.ImageURL)
	assert.Equal(t, "data:image/png;base64,aW1n", *block.ImageURL)
}

// TestGeminiResponsesStreamCompletedCarriesFileData verifies the same for a
// file data part referenced by URI.
func TestGeminiResponsesStreamCompletedCarriesFileData(t *testing.T) {
	chunks := []*GenerateContentResponse{
		{
			ResponseID:   "resp-file",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				Content: &Content{
					Role:  "model",
					Parts: []*Part{{FileData: &FileData{MIMEType: "image/png", FileURI: "gs://bucket/diagram.png"}}},
				},
			}},
		},
		{
			ResponseID:   "resp-file",
			ModelVersion: "gemini-2.5-flash",
			Candidates:   []*Candidate{{FinishReason: FinishReasonStop}},
			UsageMetadata: &GenerateContentResponseUsageMetadata{
				PromptTokenCount:     2,
				CandidatesTokenCount: 3,
				TotalTokenCount:      5,
			},
		},
	}

	state := &GeminiResponsesStreamState{}
	state.flush()
	responses := driveResponsesStream(t, state, chunks)

	completed := findCompletedEvent(t, responses)
	messageItems := itemsOfType(completed.Output, schemas.ResponsesMessageTypeMessage)
	require.Len(t, messageItems, 1, "completed output must include the file data item")
	require.NotNil(t, messageItems[0].Content)
	require.Len(t, messageItems[0].Content.ContentBlocks, 1, "completed output must keep the file data payload")
	block := messageItems[0].Content.ContentBlocks[0]
	assert.Equal(t, schemas.ResponsesInputMessageContentBlockTypeImage, block.Type)
	require.NotNil(t, block.ResponsesInputMessageContentBlockImage)
	require.NotNil(t, block.ImageURL)
	assert.Equal(t, "gs://bucket/diagram.png", *block.ImageURL)
}

// TestGeminiResponsesStreamStateRecycleTerminalClean verifies that a recycled
// pooled state starts with empty output bookkeeping, so a later stream's
// response.completed only carries its own items.
func TestGeminiResponsesStreamStateRecycleTerminalClean(t *testing.T) {
	state := &GeminiResponsesStreamState{}
	state.flush()

	first := driveResponsesStream(t, state, thinkingStreamChunks([]byte("sig-run-1")))
	firstCompleted := findCompletedEvent(t, first)
	require.Len(t, firstCompleted.Output, 2, "first run must carry reasoning + message")

	// Between two streaming requests the pool recycles the state through
	// releaseGeminiResponsesStreamState and acquireGeminiResponsesStreamState,
	// and flush is the only reset either of them runs.
	state.flush()

	second := driveResponsesStream(t, state, functionCallStreamChunks())
	secondCompleted := findCompletedEvent(t, second)
	require.Len(t, secondCompleted.Output, 2, "second run must carry message + function call only")
	assert.Empty(t, itemsOfType(secondCompleted.Output, schemas.ResponsesMessageTypeReasoning),
		"a recycled state must not leak the previous stream's reasoning item")
}

// TestGeminiResponsesStreamSignatureOnlyPartKeepsStandaloneItem guards the
// pre-existing behavior for signature-only parts (no thought text): they keep
// producing their own reasoning item with encrypted content, and that item now
// also shows up in the completed output.
func TestGeminiResponsesStreamSignatureOnlyPartKeepsStandaloneItem(t *testing.T) {
	signature := []byte("sig-standalone")
	chunks := []*GenerateContentResponse{
		{
			ResponseID:   "resp-sig",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				Content: &Content{
					Role:  "model",
					Parts: []*Part{{ThoughtSignature: signature}},
				},
			}},
		},
		{
			ResponseID:   "resp-sig",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				Content: &Content{
					Role:  "model",
					Parts: []*Part{{Text: "Done."}},
				},
			}},
		},
		{
			ResponseID:   "resp-sig",
			ModelVersion: "gemini-2.5-flash",
			Candidates:   []*Candidate{{FinishReason: FinishReasonStop}},
			UsageMetadata: &GenerateContentResponseUsageMetadata{
				PromptTokenCount:     3,
				CandidatesTokenCount: 2,
				TotalTokenCount:      5,
			},
		},
	}

	state := &GeminiResponsesStreamState{}
	state.flush()
	responses := driveResponsesStream(t, state, chunks)

	wantSignature := base64.StdEncoding.EncodeToString(signature)
	var standaloneAdded *schemas.ResponsesMessage
	for _, r := range collectEventsOfType(responses, schemas.ResponsesStreamResponseTypeOutputItemAdded) {
		if r.Item != nil && r.Item.Type != nil && *r.Item.Type == schemas.ResponsesMessageTypeReasoning {
			standaloneAdded = r.Item
		}
	}
	require.NotNil(t, standaloneAdded, "signature-only part must keep opening its own reasoning item")
	require.NotNil(t, standaloneAdded.ResponsesReasoning)
	require.NotNil(t, standaloneAdded.ResponsesReasoning.EncryptedContent)
	assert.Equal(t, wantSignature, *standaloneAdded.ResponsesReasoning.EncryptedContent)

	completed := findCompletedEvent(t, responses)
	reasoningItems := itemsOfType(completed.Output, schemas.ResponsesMessageTypeReasoning)
	require.Len(t, reasoningItems, 1, "completed output must include the standalone signature item")
	require.NotNil(t, reasoningItems[0].ResponsesReasoning)
	require.NotNil(t, reasoningItems[0].ResponsesReasoning.EncryptedContent)
	assert.Equal(t, wantSignature, *reasoningItems[0].ResponsesReasoning.EncryptedContent)
}

// TestGeminiResponsesStreamReasoningItemReplaysSignature verifies the full
// chain: the reasoning item emitted by the stream survives a JSON round-trip
// (the client echoes the completed output back as request input) and the
// request converter recovers the thoughtSignature from its encrypted content,
// the same way it already does for non-streaming responses.
func TestGeminiResponsesStreamReasoningItemReplaysSignature(t *testing.T) {
	signature := []byte("sig-replay")
	state := &GeminiResponsesStreamState{}
	state.flush()
	responses := driveResponsesStream(t, state, thinkingStreamChunks(signature))

	completed := findCompletedEvent(t, responses)
	reasoningItems := itemsOfType(completed.Output, schemas.ResponsesMessageTypeReasoning)
	require.Len(t, reasoningItems, 1)

	// Client-side echo: serialize the item and decode it back, the way an
	// OpenAI-SDK client replays conversation history verbatim.
	encoded, err := json.Marshal(reasoningItems[0])
	require.NoError(t, err)
	var replayed schemas.ResponsesMessage
	require.NoError(t, json.Unmarshal(encoded, &replayed))
	require.NotNil(t, replayed.ResponsesReasoning, "reasoning payload must survive the JSON round-trip")
	require.NotNil(t, replayed.ResponsesReasoning.EncryptedContent, "encrypted content must survive the JSON round-trip")

	// The request converter attaches the signature to the function call that
	// precedes the reasoning item in the replayed input.
	callID := "call-replay"
	toolName := "get_weather"
	toolArgs := `{"location":"Paris"}`
	functionCall := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID:    &callID,
			Name:      &toolName,
			Arguments: &toolArgs,
		},
	}

	request, err := ToGeminiResponsesRequest(nil, &schemas.BifrostResponsesRequest{
		Model: "gemini-2.5-flash",
		Input: []schemas.ResponsesMessage{functionCall, replayed},
	})
	require.NoError(t, err)
	require.NotNil(t, request)

	var recovered []byte
	for _, content := range request.Contents {
		for _, part := range content.Parts {
			if part.FunctionCall != nil && len(part.ThoughtSignature) > 0 {
				recovered = part.ThoughtSignature
			}
		}
	}
	require.NotNil(t, recovered, "replayed input must carry the thoughtSignature on the function call")
	assert.Equal(t, signature, recovered, "the signature must round-trip byte for byte")
}

// TestGeminiNonStreamThoughtPartKeepsSignature verifies the non-streaming
// converter keeps a thoughtSignature arriving on a thought part, both when the
// part carries text and when the thought-flagged part carries only the
// signature. Before the fix the signature was dropped in both shapes.
func TestGeminiNonStreamThoughtPartKeepsSignature(t *testing.T) {
	signature := []byte("sig-nonstream")
	wantSignature := base64.StdEncoding.EncodeToString(signature)

	withText := convertGeminiCandidatesToResponsesOutput([]*Candidate{{
		Content: &Content{
			Role: "model",
			Parts: []*Part{
				{Text: "Think.", Thought: true, ThoughtSignature: signature},
				{Text: "Answer."},
			},
		},
	}})
	reasoningItems := itemsOfType(withText, schemas.ResponsesMessageTypeReasoning)
	require.Len(t, reasoningItems, 1, "thought part must produce a reasoning message")
	require.NotNil(t, reasoningItems[0].Content)
	require.Len(t, reasoningItems[0].Content.ContentBlocks, 1)
	require.NotNil(t, reasoningItems[0].Content.ContentBlocks[0].Text)
	assert.Equal(t, "Think.", *reasoningItems[0].Content.ContentBlocks[0].Text)
	require.NotNil(t, reasoningItems[0].ResponsesReasoning, "signature on a thought part must be kept")
	require.NotNil(t, reasoningItems[0].ResponsesReasoning.EncryptedContent)
	assert.Equal(t, wantSignature, *reasoningItems[0].ResponsesReasoning.EncryptedContent)

	signatureOnly := convertGeminiCandidatesToResponsesOutput([]*Candidate{{
		Content: &Content{
			Role:  "model",
			Parts: []*Part{{Thought: true, ThoughtSignature: signature}},
		},
	}})
	reasoningItems = itemsOfType(signatureOnly, schemas.ResponsesMessageTypeReasoning)
	require.Len(t, reasoningItems, 1, "a thought-flagged signature-only part must produce a reasoning message")
	require.NotNil(t, reasoningItems[0].ResponsesReasoning)
	require.NotNil(t, reasoningItems[0].ResponsesReasoning.EncryptedContent)
	assert.Equal(t, wantSignature, *reasoningItems[0].ResponsesReasoning.EncryptedContent)
}
