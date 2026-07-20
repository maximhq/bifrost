package cohere

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runCohereResponsesStream replays a native Cohere event sequence through
// ToBifrostResponsesStream with a fresh stream state, the way the provider's
// stream loop does, and returns every emitted Responses stream event.
func runCohereResponsesStream(t *testing.T, state *CohereResponsesStreamState, events []CohereStreamEvent) []*schemas.BifrostResponsesStreamResponse {
	t.Helper()
	var collected []*schemas.BifrostResponsesStreamResponse
	seq := 0
	for i := range events {
		responses, bifrostErr, _ := events[i].ToBifrostResponsesStream(seq, state)
		require.Nil(t, bifrostErr, "unexpected error on event %d", i)
		collected = append(collected, responses...)
		seq += len(responses)
	}
	return collected
}

func filterStreamEvents(events []*schemas.BifrostResponsesStreamResponse, eventType schemas.ResponsesStreamResponseType) []*schemas.BifrostResponsesStreamResponse {
	var matched []*schemas.BifrostResponsesStreamResponse
	for _, event := range events {
		if event != nil && event.Type == eventType {
			matched = append(matched, event)
		}
	}
	return matched
}

func completedResponse(t *testing.T, events []*schemas.BifrostResponsesStreamResponse) *schemas.BifrostResponsesResponse {
	t.Helper()
	completed := filterStreamEvents(events, schemas.ResponsesStreamResponseTypeCompleted)
	require.Len(t, completed, 1, "expected exactly one response.completed event")
	require.NotNil(t, completed[0].Response)
	return completed[0].Response
}

func cohereMessageStartEvent(id string) CohereStreamEvent {
	return CohereStreamEvent{
		Type: StreamEventMessageStart,
		ID:   schemas.Ptr(id),
		Delta: &CohereStreamDelta{
			Message: &CohereStreamMessage{Role: schemas.Ptr("assistant")},
		},
	}
}

func cohereContentStartEvent(index int, blockType CohereContentBlockType) CohereStreamEvent {
	return CohereStreamEvent{
		Type:  StreamEventContentStart,
		Index: schemas.Ptr(index),
		Delta: &CohereStreamDelta{
			Message: &CohereStreamMessage{
				Content: &CohereStreamContentStruct{
					CohereStreamContentObject: &CohereStreamContent{Type: blockType},
				},
			},
		},
	}
}

func cohereTextDeltaEvent(index int, text string) CohereStreamEvent {
	return CohereStreamEvent{
		Type:  StreamEventContentDelta,
		Index: schemas.Ptr(index),
		Delta: &CohereStreamDelta{
			Message: &CohereStreamMessage{
				Content: &CohereStreamContentStruct{
					CohereStreamContentObject: &CohereStreamContent{Text: schemas.Ptr(text)},
				},
			},
		},
	}
}

func cohereThinkingDeltaEvent(index int, thinking string) CohereStreamEvent {
	return CohereStreamEvent{
		Type:  StreamEventContentDelta,
		Index: schemas.Ptr(index),
		Delta: &CohereStreamDelta{
			Message: &CohereStreamMessage{
				Content: &CohereStreamContentStruct{
					CohereStreamContentObject: &CohereStreamContent{Thinking: schemas.Ptr(thinking)},
				},
			},
		},
	}
}

func cohereContentEndEvent(index int) CohereStreamEvent {
	return CohereStreamEvent{Type: StreamEventContentEnd, Index: schemas.Ptr(index)}
}

func cohereToolPlanDeltaEvent(text string) CohereStreamEvent {
	return CohereStreamEvent{
		Type: StreamEventToolPlanDelta,
		Delta: &CohereStreamDelta{
			Message: &CohereStreamMessage{ToolPlan: schemas.Ptr(text)},
		},
	}
}

func cohereToolCallStartEvent(index int, callID, name string) CohereStreamEvent {
	return CohereStreamEvent{
		Type:  StreamEventToolCallStart,
		Index: schemas.Ptr(index),
		Delta: &CohereStreamDelta{
			Message: &CohereStreamMessage{
				ToolCalls: &CohereStreamToolCallStruct{
					CohereToolCallObject: &CohereToolCall{
						ID:       schemas.Ptr(callID),
						Type:     "function",
						Function: &CohereFunction{Name: schemas.Ptr(name)},
					},
				},
			},
		},
	}
}

func cohereToolCallDeltaEvent(index int, argumentsFragment string) CohereStreamEvent {
	return CohereStreamEvent{
		Type:  StreamEventToolCallDelta,
		Index: schemas.Ptr(index),
		Delta: &CohereStreamDelta{
			Message: &CohereStreamMessage{
				ToolCalls: &CohereStreamToolCallStruct{
					CohereToolCallObject: &CohereToolCall{
						Function: &CohereFunction{Arguments: argumentsFragment},
					},
				},
			},
		},
	}
}

func cohereToolCallEndEvent(index int) CohereStreamEvent {
	return CohereStreamEvent{Type: StreamEventToolCallEnd, Index: schemas.Ptr(index)}
}

func cohereCitationStartEvent(annotationIndex, contentIndex, start, end int, title, url string) CohereStreamEvent {
	document := json.RawMessage(`{"id":"doc-1","title":"` + title + `","url":"` + url + `","snippet":"cited text"}`)
	return CohereStreamEvent{
		Type:  StreamEventCitationStart,
		Index: schemas.Ptr(annotationIndex),
		Delta: &CohereStreamDelta{
			Message: &CohereStreamMessage{
				Citations: &CohereStreamCitationStruct{
					CohereStreamCitationObject: &CohereCitation{
						Start:        start,
						End:          end,
						ContentIndex: contentIndex,
						Sources: []CohereSource{
							{Type: "document", Document: &document},
						},
					},
				},
			},
		},
	}
}

func cohereCitationEndEvent(annotationIndex int) CohereStreamEvent {
	return CohereStreamEvent{Type: StreamEventCitationEnd, Index: schemas.Ptr(annotationIndex)}
}

func cohereMessageEndEvent(finishReason CohereFinishReason, inputTokens, outputTokens int) CohereStreamEvent {
	return CohereStreamEvent{
		Type: StreamEventMessageEnd,
		Delta: &CohereStreamDelta{
			FinishReason: &finishReason,
			Usage: &CohereUsage{
				Tokens: &CohereTokenUsage{
					InputTokens:  schemas.Ptr(inputTokens),
					OutputTokens: schemas.Ptr(outputTokens),
				},
			},
		},
	}
}

// TestCohereResponsesStreamThinkingTerminalEvents pins that the reasoning
// terminal events carry the accumulated thinking text instead of empty shells.
func TestCohereResponsesStreamThinkingTerminalEvents(t *testing.T) {
	state := acquireCohereResponsesStreamState()
	defer releaseCohereResponsesStreamState(state)

	events := runCohereResponsesStream(t, state, []CohereStreamEvent{
		cohereMessageStartEvent("msg-thinking"),
		cohereContentStartEvent(0, CohereContentBlockTypeThinking),
		cohereThinkingDeltaEvent(0, "First step."),
		cohereThinkingDeltaEvent(0, " Second step."),
		cohereContentEndEvent(0),
		cohereMessageEndEvent(FinishReasonComplete, 10, 20),
	})

	reasoningDeltas := filterStreamEvents(events, schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta)
	require.Len(t, reasoningDeltas, 2)
	for _, delta := range reasoningDeltas {
		require.NotNil(t, delta.SummaryIndex, "reasoning_summary_text.delta must carry summary_index")
		assert.Equal(t, 0, *delta.SummaryIndex)
	}

	reasoningDone := filterStreamEvents(events, schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone)
	require.Len(t, reasoningDone, 1)
	require.NotNil(t, reasoningDone[0].Text)
	assert.Equal(t, "First step. Second step.", *reasoningDone[0].Text)
	require.NotNil(t, reasoningDone[0].SummaryIndex, "reasoning_summary_text.done must carry summary_index")
	assert.Equal(t, 0, *reasoningDone[0].SummaryIndex)

	partDone := filterStreamEvents(events, schemas.ResponsesStreamResponseTypeContentPartDone)
	require.Len(t, partDone, 1)
	require.NotNil(t, partDone[0].Part)
	require.NotNil(t, partDone[0].Part.Text)
	assert.Equal(t, "First step. Second step.", *partDone[0].Part.Text)

	itemDone := filterStreamEvents(events, schemas.ResponsesStreamResponseTypeOutputItemDone)
	require.Len(t, itemDone, 1)
	item := itemDone[0].Item
	require.NotNil(t, item)
	require.NotNil(t, item.Type)
	assert.Equal(t, schemas.ResponsesMessageTypeReasoning, *item.Type)
	require.NotNil(t, item.Content, "reasoning output_item.done must carry the accumulated text as a content block")
	require.Len(t, item.Content.ContentBlocks, 1)
	block := item.Content.ContentBlocks[0]
	assert.Equal(t, schemas.ResponsesOutputMessageContentTypeReasoning, block.Type)
	require.NotNil(t, block.Text)
	assert.Equal(t, "First step. Second step.", *block.Text)
}

// TestCohereResponsesStreamCompletedCarriesOutput pins that response.completed
// carries the output array with the streamed message, matching the
// non-streaming path, plus the mapped stop reason.
func TestCohereResponsesStreamCompletedCarriesOutput(t *testing.T) {
	state := acquireCohereResponsesStreamState()
	defer releaseCohereResponsesStreamState(state)

	events := runCohereResponsesStream(t, state, []CohereStreamEvent{
		cohereMessageStartEvent("msg-text"),
		cohereContentStartEvent(0, CohereContentBlockTypeText),
		cohereTextDeltaEvent(0, "Hello "),
		cohereTextDeltaEvent(0, "world."),
		cohereContentEndEvent(0),
		cohereMessageEndEvent(FinishReasonComplete, 5, 7),
	})

	response := completedResponse(t, events)
	require.NotNil(t, response.Output, "response.completed must carry the output array")
	require.Len(t, response.Output, 1)

	message := response.Output[0]
	require.NotNil(t, message.Type)
	assert.Equal(t, schemas.ResponsesMessageTypeMessage, *message.Type)
	require.NotNil(t, message.Content)
	require.Len(t, message.Content.ContentBlocks, 1)
	require.NotNil(t, message.Content.ContentBlocks[0].Text)
	assert.Equal(t, "Hello world.", *message.Content.ContentBlocks[0].Text)

	require.NotNil(t, response.StopReason, "response.completed must map the Cohere finish reason")
	assert.Equal(t, "stop", *response.StopReason)
	require.NotNil(t, response.Usage)
	assert.Equal(t, 5, response.Usage.InputTokens)
	assert.Equal(t, 7, response.Usage.OutputTokens)
}

// TestCohereResponsesStreamCompletedCarriesReasoningAndFunctionCall pins the
// full output array for a thinking + tool-call turn, ordered by output index.
func TestCohereResponsesStreamCompletedCarriesReasoningAndFunctionCall(t *testing.T) {
	state := acquireCohereResponsesStreamState()
	defer releaseCohereResponsesStreamState(state)

	events := runCohereResponsesStream(t, state, []CohereStreamEvent{
		cohereMessageStartEvent("msg-tools"),
		cohereContentStartEvent(0, CohereContentBlockTypeThinking),
		cohereThinkingDeltaEvent(0, "Check the weather."),
		cohereContentEndEvent(0),
		cohereToolCallStartEvent(1, "call_1", "get_weather"),
		cohereToolCallDeltaEvent(1, `{"location":`),
		cohereToolCallDeltaEvent(1, `"Paris"}`),
		cohereToolCallEndEvent(1),
		cohereMessageEndEvent(FinishReasonToolCall, 12, 34),
	})

	response := completedResponse(t, events)
	require.NotNil(t, response.Output)
	require.Len(t, response.Output, 2)

	reasoning := response.Output[0]
	require.NotNil(t, reasoning.Type)
	assert.Equal(t, schemas.ResponsesMessageTypeReasoning, *reasoning.Type)
	require.NotNil(t, reasoning.Content)
	require.Len(t, reasoning.Content.ContentBlocks, 1)
	require.NotNil(t, reasoning.Content.ContentBlocks[0].Text)
	assert.Equal(t, "Check the weather.", *reasoning.Content.ContentBlocks[0].Text)

	functionCall := response.Output[1]
	require.NotNil(t, functionCall.Type)
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCall, *functionCall.Type)
	require.NotNil(t, functionCall.ResponsesToolMessage)
	require.NotNil(t, functionCall.ResponsesToolMessage.Name)
	assert.Equal(t, "get_weather", *functionCall.ResponsesToolMessage.Name)
	require.NotNil(t, functionCall.ResponsesToolMessage.Arguments)
	assert.Equal(t, `{"location":"Paris"}`, *functionCall.ResponsesToolMessage.Arguments)

	require.NotNil(t, response.StopReason)
	assert.Equal(t, "tool_calls", *response.StopReason)
}

// TestCohereResponsesStreamCitationAnnotations pins that citation annotation
// events carry the item ID of the text item they belong to, and that
// citations streaming before content-end reach the done events and the final
// snapshot.
func TestCohereResponsesStreamCitationAnnotations(t *testing.T) {
	state := acquireCohereResponsesStreamState()
	defer releaseCohereResponsesStreamState(state)

	events := runCohereResponsesStream(t, state, []CohereStreamEvent{
		cohereMessageStartEvent("msg-cite"),
		cohereContentStartEvent(0, CohereContentBlockTypeText),
		cohereTextDeltaEvent(0, "Paris is the capital of France."),
		cohereCitationStartEvent(0, 0, 0, 5, "Paris", "https://example.com/paris"),
		cohereCitationEndEvent(0),
		cohereContentEndEvent(0),
		cohereMessageEndEvent(FinishReasonComplete, 8, 9),
	})

	annotationAdded := filterStreamEvents(events, schemas.ResponsesStreamResponseTypeOutputTextAnnotationAdded)
	require.Len(t, annotationAdded, 1)
	require.NotNil(t, annotationAdded[0].ItemID, "annotation.added must carry the text item's ID")
	assert.Equal(t, "msg_msg-cite_item_0", *annotationAdded[0].ItemID)

	annotationDone := filterStreamEvents(events, schemas.ResponsesStreamResponseTypeOutputTextAnnotationDone)
	require.Len(t, annotationDone, 1)
	require.NotNil(t, annotationDone[0].ItemID, "annotation.done must carry the text item's ID")
	assert.Equal(t, "msg_msg-cite_item_0", *annotationDone[0].ItemID)

	itemDone := filterStreamEvents(events, schemas.ResponsesStreamResponseTypeOutputItemDone)
	require.Len(t, itemDone, 1)
	require.NotNil(t, itemDone[0].Item)
	require.NotNil(t, itemDone[0].Item.Content)
	require.Len(t, itemDone[0].Item.Content.ContentBlocks, 1)
	doneBlock := itemDone[0].Item.Content.ContentBlocks[0]
	require.NotNil(t, doneBlock.ResponsesOutputMessageContentText)
	require.Len(t, doneBlock.Annotations, 1, "output_item.done must carry citations that streamed before content-end")

	response := completedResponse(t, events)
	require.NotNil(t, response.Output)
	require.Len(t, response.Output, 1)
	snapshotBlock := response.Output[0].Content.ContentBlocks[0]
	require.NotNil(t, snapshotBlock.ResponsesOutputMessageContentText)
	require.Len(t, snapshotBlock.Annotations, 1)
	annotation := snapshotBlock.Annotations[0]
	require.NotNil(t, annotation.URL)
	assert.Equal(t, "https://example.com/paris", *annotation.URL)
	require.NotNil(t, annotation.Title)
	assert.Equal(t, "Paris", *annotation.Title)
	require.NotNil(t, annotation.StartIndex)
	assert.Equal(t, 0, *annotation.StartIndex)
	require.NotNil(t, annotation.EndIndex)
	assert.Equal(t, 5, *annotation.EndIndex)
}

// TestCohereResponsesStreamCitationAfterContentEndInSnapshot pins that
// citations streaming after the text item completed still reach the final
// snapshot, so the handling is order-independent.
func TestCohereResponsesStreamCitationAfterContentEndInSnapshot(t *testing.T) {
	state := acquireCohereResponsesStreamState()
	defer releaseCohereResponsesStreamState(state)

	events := runCohereResponsesStream(t, state, []CohereStreamEvent{
		cohereMessageStartEvent("msg-late-cite"),
		cohereContentStartEvent(0, CohereContentBlockTypeText),
		cohereTextDeltaEvent(0, "Paris is the capital of France."),
		cohereContentEndEvent(0),
		cohereCitationStartEvent(0, 0, 0, 5, "Paris", "https://example.com/paris"),
		cohereCitationEndEvent(0),
		cohereMessageEndEvent(FinishReasonComplete, 8, 9),
	})

	annotationAdded := filterStreamEvents(events, schemas.ResponsesStreamResponseTypeOutputTextAnnotationAdded)
	require.Len(t, annotationAdded, 1)
	require.NotNil(t, annotationAdded[0].ItemID)
	assert.Equal(t, "msg_msg-late-cite_item_0", *annotationAdded[0].ItemID)

	response := completedResponse(t, events)
	require.NotNil(t, response.Output)
	require.Len(t, response.Output, 1)
	snapshotBlock := response.Output[0].Content.ContentBlocks[0]
	require.NotNil(t, snapshotBlock.ResponsesOutputMessageContentText)
	require.Len(t, snapshotBlock.Annotations, 1, "late citations must be folded into the final snapshot")
	require.NotNil(t, snapshotBlock.Annotations[0].URL)
	assert.Equal(t, "https://example.com/paris", *snapshotBlock.Annotations[0].URL)
}

// TestCohereResponsesStreamToolPlanClosedAtMessageEnd pins that a tool-plan
// text item left open when the stream ends is completed before
// response.completed, and appears in the output array.
func TestCohereResponsesStreamToolPlanClosedAtMessageEnd(t *testing.T) {
	state := acquireCohereResponsesStreamState()
	defer releaseCohereResponsesStreamState(state)

	events := runCohereResponsesStream(t, state, []CohereStreamEvent{
		cohereMessageStartEvent("msg-plan"),
		cohereToolPlanDeltaEvent("I will look this up."),
		cohereMessageEndEvent(FinishReasonComplete, 3, 4),
	})

	textDone := filterStreamEvents(events, schemas.ResponsesStreamResponseTypeOutputTextDone)
	require.Len(t, textDone, 1, "the open tool-plan item must be completed at message-end")
	require.NotNil(t, textDone[0].Text)
	assert.Equal(t, "I will look this up.", *textDone[0].Text)

	itemDone := filterStreamEvents(events, schemas.ResponsesStreamResponseTypeOutputItemDone)
	require.Len(t, itemDone, 1)

	response := completedResponse(t, events)
	require.NotNil(t, response.Output)
	require.Len(t, response.Output, 1)
	require.NotNil(t, response.Output[0].Content)
	require.Len(t, response.Output[0].Content.ContentBlocks, 1)
	require.NotNil(t, response.Output[0].Content.ContentBlocks[0].Text)
	assert.Equal(t, "I will look this up.", *response.Output[0].Content.ContentBlocks[0].Text)

	completedIndex := -1
	itemDoneIndex := -1
	for i, event := range events {
		switch event.Type {
		case schemas.ResponsesStreamResponseTypeOutputItemDone:
			itemDoneIndex = i
		case schemas.ResponsesStreamResponseTypeCompleted:
			completedIndex = i
		}
	}
	assert.Less(t, itemDoneIndex, completedIndex, "output_item.done must precede response.completed")
}

// TestCohereResponsesStreamToolPlanThenContentKeepsBothItems pins that a
// content block following a tool plan gets its own output index, so the
// completed output array keeps both items and annotations stay on the item
// they belong to.
func TestCohereResponsesStreamToolPlanThenContentKeepsBothItems(t *testing.T) {
	state := acquireCohereResponsesStreamState()
	defer releaseCohereResponsesStreamState(state)

	events := runCohereResponsesStream(t, state, []CohereStreamEvent{
		cohereMessageStartEvent("msg-plan-content"),
		cohereToolPlanDeltaEvent("Plan the answer."),
		cohereContentStartEvent(0, CohereContentBlockTypeText),
		cohereTextDeltaEvent(0, "Final answer."),
		cohereContentEndEvent(0),
		cohereMessageEndEvent(FinishReasonComplete, 4, 5),
	})

	added := filterStreamEvents(events, schemas.ResponsesStreamResponseTypeOutputItemAdded)
	require.Len(t, added, 2)
	require.NotNil(t, added[0].OutputIndex)
	require.NotNil(t, added[1].OutputIndex)
	assert.NotEqual(t, *added[0].OutputIndex, *added[1].OutputIndex, "items must not share an output index")

	response := completedResponse(t, events)
	require.NotNil(t, response.Output)
	require.Len(t, response.Output, 2, "both the tool-plan item and the content item must survive in the output array")
	require.NotNil(t, response.Output[0].Content)
	require.Len(t, response.Output[0].Content.ContentBlocks, 1)
	require.NotNil(t, response.Output[0].Content.ContentBlocks[0].Text)
	assert.Equal(t, "Plan the answer.", *response.Output[0].Content.ContentBlocks[0].Text)
	require.NotNil(t, response.Output[1].Content)
	require.Len(t, response.Output[1].Content.ContentBlocks, 1)
	require.NotNil(t, response.Output[1].Content.ContentBlocks[0].Text)
	assert.Equal(t, "Final answer.", *response.Output[1].Content.ContentBlocks[0].Text)
}

// TestCohereResponsesStreamStateRecycleTerminalClean pins that a recycled
// stream state starts with clean bookkeeping: nothing from a previous stream
// leaks into the next stream's response.completed.
func TestCohereResponsesStreamStateRecycleTerminalClean(t *testing.T) {
	state := acquireCohereResponsesStreamState()
	defer releaseCohereResponsesStreamState(state)

	first := runCohereResponsesStream(t, state, []CohereStreamEvent{
		cohereMessageStartEvent("msg-run-1"),
		cohereContentStartEvent(0, CohereContentBlockTypeText),
		cohereTextDeltaEvent(0, "First run text."),
		cohereCitationStartEvent(0, 0, 0, 5, "One", "https://example.com/one"),
		cohereContentEndEvent(0),
		cohereMessageEndEvent(FinishReasonComplete, 1, 2),
	})
	firstResponse := completedResponse(t, first)
	require.NotNil(t, firstResponse.Output)
	require.Len(t, firstResponse.Output, 1)

	// flush is what both release and acquire run, so this is the recycle path.
	state.flush()

	// The second run reuses output index 0 for a plain text item: leaked
	// OutputItems would change the item count, and a leaked annotation map
	// would fold the first run's citation into this run's text block.
	second := runCohereResponsesStream(t, state, []CohereStreamEvent{
		cohereMessageStartEvent("msg-run-2"),
		cohereContentStartEvent(0, CohereContentBlockTypeText),
		cohereTextDeltaEvent(0, "Second run text."),
		cohereContentEndEvent(0),
		cohereMessageEndEvent(FinishReasonComplete, 3, 4),
	})
	secondResponse := completedResponse(t, second)
	require.NotNil(t, secondResponse.Output)
	require.Len(t, secondResponse.Output, 1, "recycled state must not leak items from the previous stream")
	require.NotNil(t, secondResponse.Output[0].Type)
	assert.Equal(t, schemas.ResponsesMessageTypeMessage, *secondResponse.Output[0].Type)
	require.NotNil(t, secondResponse.Output[0].Content)
	require.Len(t, secondResponse.Output[0].Content.ContentBlocks, 1)
	block := secondResponse.Output[0].Content.ContentBlocks[0]
	require.NotNil(t, block.Text)
	assert.Equal(t, "Second run text.", *block.Text)
	require.NotNil(t, block.ResponsesOutputMessageContentText)
	assert.Empty(t, block.Annotations, "annotations from the previous stream must not leak")
}

// TestCohereResponsesStreamReasoningItemReplayable pins the full replay chain:
// the reasoning item from response.completed, JSON-echoed the way a client
// replays history, converts back into a Cohere thinking block.
func TestCohereResponsesStreamReasoningItemReplayable(t *testing.T) {
	state := acquireCohereResponsesStreamState()
	defer releaseCohereResponsesStreamState(state)

	events := runCohereResponsesStream(t, state, []CohereStreamEvent{
		cohereMessageStartEvent("msg-replay"),
		cohereContentStartEvent(0, CohereContentBlockTypeThinking),
		cohereThinkingDeltaEvent(0, "Reason to replay."),
		cohereContentEndEvent(0),
		cohereMessageEndEvent(FinishReasonComplete, 2, 3),
	})

	response := completedResponse(t, events)
	require.NotNil(t, response.Output)
	require.Len(t, response.Output, 1)

	echoed, err := json.Marshal(response.Output[0])
	require.NoError(t, err)
	var replayed schemas.ResponsesMessage
	require.NoError(t, json.Unmarshal(echoed, &replayed))

	cohereMessages := ConvertBifrostMessagesToCohereMessages([]schemas.ResponsesMessage{replayed}, nil)
	require.Len(t, cohereMessages, 1)
	assert.Equal(t, "assistant", cohereMessages[0].Role)
	require.NotNil(t, cohereMessages[0].Content)
	require.Len(t, cohereMessages[0].Content.BlocksContent, 1)
	replayedBlock := cohereMessages[0].Content.BlocksContent[0]
	assert.Equal(t, CohereContentBlockTypeThinking, replayedBlock.Type)
	require.NotNil(t, replayedBlock.Thinking)
	assert.Equal(t, "Reason to replay.", *replayedBlock.Thinking)
}

// TestCohereNonStreamResponsesCarriesStopReason pins that the non-streaming
// Responses conversion maps the Cohere finish reason the way the chat
// conversion already does.
func TestCohereNonStreamResponsesCarriesStopReason(t *testing.T) {
	finishReason := FinishReasonComplete
	response := (&CohereChatResponse{
		ID:           "resp-1",
		FinishReason: &finishReason,
	}).ToBifrostResponsesResponse()

	require.NotNil(t, response)
	require.NotNil(t, response.StopReason, "non-streaming Responses must map the Cohere finish reason")
	assert.Equal(t, "stop", *response.StopReason)
}
