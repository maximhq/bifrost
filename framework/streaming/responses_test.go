package streaming

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func testResponsesAccumulator(tb testing.TB) *Accumulator {
	tb.Helper()
	acc := NewAccumulator(nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
	tb.Cleanup(acc.Cleanup)
	return acc
}

func TestAccumulatedResponsesStreamPreservesServiceTierBeforeUsageOnlyChunk(t *testing.T) {
	acc := testResponsesAccumulator(t)
	requestID := "responses-service-tier"
	flex := schemas.BifrostServiceTierFlex

	require.NoError(t, acc.addResponsesStreamChunk(requestID, &ResponsesStreamChunk{
		ChunkIndex:  1,
		Timestamp:   time.Now(),
		ServiceTier: &flex,
	}, false))
	require.NoError(t, acc.addResponsesStreamChunk(requestID, &ResponsesStreamChunk{
		ChunkIndex: 2,
		Timestamp:  time.Now(),
		TokenUsage: &schemas.BifrostLLMUsage{TotalTokens: 1},
	}, true))

	data, err := acc.processAccumulatedResponsesStreamingChunks(requestID, nil, true)
	require.NoError(t, err)
	require.NotNil(t, data.ServiceTier)
	require.Equal(t, schemas.BifrostServiceTierFlex, *data.ServiceTier)
}

// TestBuildResponsesMessageConcatenatesTextDeltas verifies that many streamed
// text deltas are joined in order. This is the path that previously accumulated
// via O(n²) `*Text += delta`; the builder rewrite must produce identical bytes.
func TestBuildResponsesMessageConcatenatesTextDeltas(t *testing.T) {
	acc := testResponsesAccumulator(t)
	ci := 0
	var want strings.Builder
	var chunks []*ResponsesStreamChunk
	for i := 0; i < 500; i++ {
		d := fmt.Sprintf("tok%d ", i)
		want.WriteString(d)
		chunks = append(chunks, &ResponsesStreamChunk{
			ChunkIndex: i,
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type:         schemas.ResponsesStreamResponseTypeOutputTextDelta,
				Delta:        schemas.Ptr(d),
				ContentIndex: &ci,
			},
		})
	}

	msgs := acc.buildCompleteMessageFromResponsesStreamChunks(chunks)
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d", len(msgs))
	}
	got := msgs[0].Content.ContentBlocks[0].Text
	if got == nil {
		t.Fatal("text block is nil")
	}
	if *got != want.String() {
		t.Fatalf("text mismatch:\n got %q\nwant %q", *got, want.String())
	}
}

// TestBuildResponsesMessageRoutesParallelToolArgs verifies that interleaved
// function-call argument deltas are routed to the correct item by ItemID and
// concatenated independently.
func TestBuildResponsesMessageRoutesParallelToolArgs(t *testing.T) {
	acc := testResponsesAccumulator(t)
	item := func(id string) *ResponsesStreamChunk {
		return &ResponsesStreamChunk{
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type: schemas.ResponsesStreamResponseTypeOutputItemAdded,
				Item: &schemas.ResponsesMessage{ID: schemas.Ptr(id)},
			},
		}
	}
	argDelta := func(id, delta string) *ResponsesStreamChunk {
		return &ResponsesStreamChunk{
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type:   schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
				ItemID: schemas.Ptr(id),
				Delta:  schemas.Ptr(delta),
			},
		}
	}
	chunks := []*ResponsesStreamChunk{
		item("call_a"), item("call_b"),
		argDelta("call_a", `{"x":`), argDelta("call_b", `{"y":`),
		argDelta("call_a", `1}`), argDelta("call_b", `2}`),
	}
	for i, c := range chunks {
		c.ChunkIndex = i
	}

	msgs := acc.buildCompleteMessageFromResponsesStreamChunks(chunks)
	gotArgs := map[string]string{}
	for _, m := range msgs {
		if m.ID != nil && m.ResponsesToolMessage != nil && m.ResponsesToolMessage.Arguments != nil {
			gotArgs[*m.ID] = *m.ResponsesToolMessage.Arguments
		}
	}
	if gotArgs["call_a"] != `{"x":1}` {
		t.Errorf("call_a args: got %q, want %q", gotArgs["call_a"], `{"x":1}`)
	}
	if gotArgs["call_b"] != `{"y":2}` {
		t.Errorf("call_b args: got %q, want %q", gotArgs["call_b"], `{"y":2}`)
	}
}

func TestDeepCopyResponsesStreamResponseCopiesToolCaller(t *testing.T) {
	original := &schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeOutputItemDone,
		Item: &schemas.ResponsesMessage{
			ID:   schemas.Ptr("srvtoolu_fetch"),
			Type: schemas.Ptr(schemas.ResponsesMessageTypeWebFetchCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: schemas.Ptr("srvtoolu_fetch"),
				Caller: &schemas.ResponsesToolCaller{
					Type:   "code_execution_20260120",
					ToolID: schemas.Ptr("srvtoolu_code"),
				},
			},
		},
	}

	copied := deepCopyResponsesStreamResponse(original)
	if copied == nil || copied.Item == nil || copied.Item.ResponsesToolMessage == nil || copied.Item.ResponsesToolMessage.Caller == nil {
		t.Fatalf("expected caller to be copied, got %#v", copied)
	}
	if copied.Item.ResponsesToolMessage.Caller == original.Item.ResponsesToolMessage.Caller {
		t.Fatal("caller pointer was aliased")
	}
	if got := copied.Item.ResponsesToolMessage.Caller.Type; got != "code_execution_20260120" {
		t.Fatalf("caller type = %q", got)
	}
	if copied.Item.ResponsesToolMessage.Caller.ToolID == nil || *copied.Item.ResponsesToolMessage.Caller.ToolID != "srvtoolu_code" {
		t.Fatalf("caller tool id not preserved: %#v", copied.Item.ResponsesToolMessage.Caller)
	}
	if copied.Item.ResponsesToolMessage.Caller.ToolID == original.Item.ResponsesToolMessage.Caller.ToolID {
		t.Fatal("caller tool id pointer was aliased")
	}
}

// TestBuildResponsesMessageAccumulatesReasoningSummary verifies reasoning
// summary deltas (no content index) concatenate into a single summary entry.
func TestBuildResponsesMessageAccumulatesReasoningSummary(t *testing.T) {
	acc := testResponsesAccumulator(t)
	parts := []string{"Let me ", "think ", "step by step."}
	var chunks []*ResponsesStreamChunk
	for i, p := range parts {
		chunks = append(chunks, &ResponsesStreamChunk{
			ChunkIndex: i,
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type:   schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
				ItemID: schemas.Ptr("reason_1"),
				Delta:  schemas.Ptr(p),
			},
		})
	}

	msgs := acc.buildCompleteMessageFromResponsesStreamChunks(chunks)
	if len(msgs) != 1 || msgs[0].ResponsesReasoning == nil || len(msgs[0].ResponsesReasoning.Summary) != 1 {
		t.Fatalf("unexpected reasoning shape: %+v", msgs)
	}
	if got := msgs[0].ResponsesReasoning.Summary[0].Text; got != "Let me think step by step." {
		t.Fatalf("summary mismatch: got %q", got)
	}
}

// TestBuildResponsesMessageAccumulatesAnnotations verifies that streamed
// output_text.annotation.added events (citations) are folded into the
// accumulated message. Providers emit these during a streamed responses call
// (e.g. Anthropic citations_delta -> convertAnthropicCitationToAnnotation) and
// the non-stream path preserves them on
// ResponsesOutputMessageContentText.Annotations, so the accumulated message
// (which feeds logging, observability, and cache) must too.
func TestBuildResponsesMessageAccumulatesAnnotations(t *testing.T) {
	acc := testResponsesAccumulator(t)
	ci := 0
	itemID := "msg_1"
	chunks := []*ResponsesStreamChunk{
		{
			ChunkIndex: 0,
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type: schemas.ResponsesStreamResponseTypeOutputItemAdded,
				Item: &schemas.ResponsesMessage{ID: schemas.Ptr(itemID)},
			},
		},
		{
			ChunkIndex: 1,
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type:         schemas.ResponsesStreamResponseTypeOutputTextDelta,
				Delta:        schemas.Ptr("The capital of France is Paris."),
				ContentIndex: &ci,
			},
		},
		{
			ChunkIndex: 2,
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type:         schemas.ResponsesStreamResponseTypeOutputTextAnnotationAdded,
				ItemID:       schemas.Ptr(itemID),
				ContentIndex: &ci,
				Annotation: &schemas.ResponsesOutputMessageContentTextAnnotation{
					Type:  "url_citation",
					URL:   schemas.Ptr("https://example.com/paris"),
					Title: schemas.Ptr("Paris"),
				},
			},
		},
	}

	msgs := acc.buildCompleteMessageFromResponsesStreamChunks(chunks)
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d", len(msgs))
	}
	if msgs[0].Content == nil || len(msgs[0].Content.ContentBlocks) == 0 {
		t.Fatalf("want a content block, got %+v", msgs[0].Content)
	}
	block := msgs[0].Content.ContentBlocks[0]
	// Text delta must still be preserved alongside the annotation.
	if block.Text == nil || *block.Text != "The capital of France is Paris." {
		t.Fatalf("text not preserved: %+v", block.Text)
	}
	if block.ResponsesOutputMessageContentText == nil {
		t.Fatal("want ResponsesOutputMessageContentText, got nil")
	}
	ann := block.ResponsesOutputMessageContentText.Annotations
	if len(ann) != 1 {
		t.Fatalf("want 1 annotation, got %d", len(ann))
	}
	if ann[0].Type != "url_citation" || ann[0].URL == nil || *ann[0].URL != "https://example.com/paris" {
		t.Fatalf("annotation not preserved: %+v", ann[0])
	}

	// Idempotent under the multi-plugin rebuild (this build runs once per plugin
	// post-hook): a second build over the same chunks must yield the same single
	// annotation, not a doubled one.
	msgs2 := acc.buildCompleteMessageFromResponsesStreamChunks(chunks)
	if len(msgs2) != 1 || msgs2[0].Content == nil || len(msgs2[0].Content.ContentBlocks) == 0 ||
		msgs2[0].Content.ContentBlocks[0].ResponsesOutputMessageContentText == nil ||
		len(msgs2[0].Content.ContentBlocks[0].ResponsesOutputMessageContentText.Annotations) != 1 {
		t.Fatalf("second build not idempotent: %+v", msgs2)
	}
}

// TestBuildResponsesMessageAccumulatesAnnotationsWithoutItemID covers the
// fallback path used by providers that emit output_text.annotation.added
// without an ItemID (e.g. Cohere): the annotation attaches to the most recent
// message, mirroring how text deltas without an ItemID are routed.
func TestBuildResponsesMessageAccumulatesAnnotationsWithoutItemID(t *testing.T) {
	acc := testResponsesAccumulator(t)
	ci := 0
	chunks := []*ResponsesStreamChunk{
		{
			ChunkIndex: 0,
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type:         schemas.ResponsesStreamResponseTypeOutputTextDelta,
				Delta:        schemas.Ptr("Grounded answer."),
				ContentIndex: &ci,
			},
		},
		{
			ChunkIndex: 1,
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type:         schemas.ResponsesStreamResponseTypeOutputTextAnnotationAdded,
				ContentIndex: &ci, // no ItemID -> route to the most recent message
				Annotation: &schemas.ResponsesOutputMessageContentTextAnnotation{
					Type: "url_citation",
					URL:  schemas.Ptr("https://example.org/source"),
				},
			},
		},
	}

	msgs := acc.buildCompleteMessageFromResponsesStreamChunks(chunks)
	if len(msgs) != 1 || msgs[0].Content == nil || len(msgs[0].Content.ContentBlocks) == 0 {
		t.Fatalf("unexpected message shape: %+v", msgs)
	}
	block := msgs[0].Content.ContentBlocks[0]
	if block.ResponsesOutputMessageContentText == nil || len(block.ResponsesOutputMessageContentText.Annotations) != 1 {
		t.Fatalf("want 1 annotation on the last message, got %+v", block.ResponsesOutputMessageContentText)
	}
	if got := block.ResponsesOutputMessageContentText.Annotations[0].URL; got == nil || *got != "https://example.org/source" {
		t.Fatalf("annotation URL not preserved: %+v", got)
	}
}

// TestForceCleanupStreamAccumulatorReapsRegardlessOfRefcount is the Tier-1 leak
// guard: it reproduces a stream that ended without its per-plugin refcount being
// driven to zero (e.g. a client abort, or multiple plugins that each Create but
// not all Cleanup) and asserts the finalizer's force-reap still frees it.
func TestForceCleanupStreamAccumulatorReapsRegardlessOfRefcount(t *testing.T) {
	acc := testResponsesAccumulator(t)

	const requestID = "force-reap-test"
	// Simulate two independent plugins each taking a hold (logging + maxim).
	acc.CreateStreamAccumulator(requestID, time.Now())
	acc.CreateStreamAccumulator(requestID, time.Now())

	// Accumulate a chunk so the accumulator holds real data.
	ci := 0
	chunk := acc.getResponsesStreamChunk()
	chunk.ChunkIndex = 0
	chunk.StreamResponse = &schemas.BifrostResponsesStreamResponse{
		Type:         schemas.ResponsesStreamResponseTypeOutputTextDelta,
		Delta:        schemas.Ptr("hello"),
		ContentIndex: &ci,
	}
	if err := acc.addResponsesStreamChunk(requestID, chunk, false); err != nil {
		t.Fatalf("addResponsesStreamChunk: %v", err)
	}

	// A single refcount-based cleanup must NOT reap (refcount went 2 -> 1).
	_ = acc.CleanupStreamAccumulator(requestID)
	if _, ok := acc.streamAccumulators.Load(requestID); !ok {
		t.Fatal("accumulator was reaped by a single refcount cleanup despite refcount > 0")
	}

	// The end-of-stream finalizer force-reaps regardless of the remaining hold.
	acc.ForceCleanupStreamAccumulator(requestID)
	if _, ok := acc.streamAccumulators.Load(requestID); ok {
		t.Fatal("accumulator survived ForceCleanupStreamAccumulator")
	}

	// Idempotent: calling again after the entry is gone must not panic.
	acc.ForceCleanupStreamAccumulator(requestID)
}

// BenchmarkBuildResponsesMessageTextDeltas guards against regressing back to
// O(n²) accumulation. allocs/op and B/op should scale ~linearly with the chunk
// count, not quadratically.
func BenchmarkBuildResponsesMessageTextDeltas(b *testing.B) {
	acc := testResponsesAccumulator(b)
	ci := 0
	const n = 2000
	chunks := make([]*ResponsesStreamChunk, n)
	for i := 0; i < n; i++ {
		chunks[i] = &ResponsesStreamChunk{
			ChunkIndex: i,
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type:         schemas.ResponsesStreamResponseTypeOutputTextDelta,
				Delta:        schemas.Ptr("hello world "),
				ContentIndex: &ci,
			},
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = acc.buildCompleteMessageFromResponsesStreamChunks(chunks)
	}
}

// TestDeepCopyResponsesStreamResponsePreservesAllFields guards the deep-copy
// helper against silently dropping fields that survive unmarshal/WithDefaults.
// Covers the fields introduced in PR #3528 (Phase, SummaryIndex, Obfuscation)
// plus the latent leaks the same PR incidentally fixed (Status, Signature).
func TestDeepCopyResponsesStreamResponsePreservesAllFields(t *testing.T) {
	original := &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
		SequenceNumber: 4,
		SummaryIndex:   schemas.Ptr(2),
		Signature:      schemas.Ptr("sig-xyz"),
		Obfuscation:    schemas.Ptr("opaque-padding"),
		Item: &schemas.ResponsesMessage{
			ID:     schemas.Ptr("msg_123"),
			Status: schemas.Ptr("in_progress"),
			Phase:  schemas.Ptr("final_answer"),
		},
	}

	copied := deepCopyResponsesStreamResponse(original)
	if copied == nil {
		t.Fatal("expected non-nil deep copy")
	}

	// Value equality on the new + latent-leak fields.
	if got := copied.SummaryIndex; got == nil || *got != 2 {
		t.Errorf("SummaryIndex: want 2, got %#v", got)
	}
	if got := copied.Signature; got == nil || *got != "sig-xyz" {
		t.Errorf("Signature: want %q, got %#v", "sig-xyz", got)
	}
	if got := copied.Obfuscation; got == nil || *got != "opaque-padding" {
		t.Errorf("Obfuscation: want %q, got %#v", "opaque-padding", got)
	}
	if got := copied.Item.Status; got == nil || *got != "in_progress" {
		t.Errorf("Item.Status: want %q, got %#v", "in_progress", got)
	}
	if got := copied.Item.Phase; got == nil || *got != "final_answer" {
		t.Errorf("Item.Phase: want %q, got %#v", "final_answer", got)
	}

	// Independence: mutating the original's pointees must not mutate the copy.
	*original.SummaryIndex = 99
	*original.Signature = "mutated"
	*original.Obfuscation = "mutated"
	*original.Item.Status = "mutated"
	*original.Item.Phase = "mutated"

	if *copied.SummaryIndex != 2 {
		t.Errorf("SummaryIndex aliased original: got %d", *copied.SummaryIndex)
	}
	if *copied.Signature != "sig-xyz" {
		t.Errorf("Signature aliased original: got %q", *copied.Signature)
	}
	if *copied.Obfuscation != "opaque-padding" {
		t.Errorf("Obfuscation aliased original: got %q", *copied.Obfuscation)
	}
	if *copied.Item.Status != "in_progress" {
		t.Errorf("Item.Status aliased original: got %q", *copied.Item.Status)
	}
	if *copied.Item.Phase != "final_answer" {
		t.Errorf("Item.Phase aliased original: got %q", *copied.Item.Phase)
	}
}

// TestBuildResponsesMessageKeepsServerToolPayloadFromItemDone verifies that a server-side
// tool item keeps the payload that only arrives on output_item.done. output_item.added is a
// bare shell for these items, so dropping the done event loses the search queries entirely.
func TestBuildResponsesMessageKeepsServerToolPayloadFromItemDone(t *testing.T) {
	acc := testResponsesAccumulator(t)
	queries := []string{"positive good news August 24 2026", "site:apnews.com positive news"}
	chunks := []*ResponsesStreamChunk{
		{
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type: schemas.ResponsesStreamResponseTypeOutputItemAdded,
				Item: &schemas.ResponsesMessage{
					ID:     schemas.Ptr("ws_1"),
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall),
					Status: schemas.Ptr("in_progress"),
				},
			},
		},
		{
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type: schemas.ResponsesStreamResponseTypeOutputItemDone,
				Item: &schemas.ResponsesMessage{
					ID:     schemas.Ptr("ws_1"),
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall),
					Status: schemas.Ptr("completed"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						Action: &schemas.ResponsesToolMessageActionStruct{
							ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{
								Type:    "search",
								Query:   schemas.Ptr(queries[0]),
								Queries: queries,
							},
						},
					},
				},
			},
		},
	}
	for i, c := range chunks {
		c.ChunkIndex = i
	}

	msgs := acc.buildCompleteMessageFromResponsesStreamChunks(chunks)
	require.Len(t, msgs, 1)
	require.NotNil(t, msgs[0].Status)
	require.Equal(t, "completed", *msgs[0].Status)
	require.NotNil(t, msgs[0].ResponsesToolMessage)
	require.NotNil(t, msgs[0].ResponsesToolMessage.Action)
	require.NotNil(t, msgs[0].ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction)
	require.Equal(t, queries, msgs[0].ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction.Queries)
}

// TestBuildResponsesMessageItemDoneKeepsStreamedText verifies that adopting the done event does
// not clobber content that arrived as deltas: the assembled text wins, the final status is taken.
func TestBuildResponsesMessageItemDoneKeepsStreamedText(t *testing.T) {
	acc := testResponsesAccumulator(t)
	chunks := []*ResponsesStreamChunk{
		{
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type: schemas.ResponsesStreamResponseTypeOutputItemAdded,
				Item: &schemas.ResponsesMessage{
					ID:     schemas.Ptr("msg_1"),
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Status: schemas.Ptr("in_progress"),
				},
			},
		},
		{
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type:         schemas.ResponsesStreamResponseTypeOutputTextDelta,
				ItemID:       schemas.Ptr("msg_1"),
				ContentIndex: schemas.Ptr(0),
				Delta:        schemas.Ptr("hello "),
			},
		},
		{
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type:         schemas.ResponsesStreamResponseTypeOutputTextDelta,
				ItemID:       schemas.Ptr("msg_1"),
				ContentIndex: schemas.Ptr(0),
				Delta:        schemas.Ptr("world"),
			},
		},
		{
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type: schemas.ResponsesStreamResponseTypeOutputItemDone,
				Item: &schemas.ResponsesMessage{
					ID:     schemas.Ptr("msg_1"),
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Status: schemas.Ptr("completed"),
				},
			},
		},
	}
	for i, c := range chunks {
		c.ChunkIndex = i
	}

	msgs := acc.buildCompleteMessageFromResponsesStreamChunks(chunks)
	require.Len(t, msgs, 1)
	require.NotNil(t, msgs[0].Status)
	require.Equal(t, "completed", *msgs[0].Status)
	require.NotNil(t, msgs[0].Content)
	require.Len(t, msgs[0].Content.ContentBlocks, 1)
	require.NotNil(t, msgs[0].Content.ContentBlocks[0].Text)
	require.Equal(t, "hello world", *msgs[0].Content.ContentBlocks[0].Text)
}

// streamThroughResponsesAccumulator assembles messages from events the way the
// accumulator does in production: each event is deep-copied when it is
// ingested, and each item is deep-copied again when messages are built.
func streamThroughResponsesAccumulator(tb testing.TB, events ...*schemas.BifrostResponsesStreamResponse) []schemas.ResponsesMessage {
	tb.Helper()
	acc := testResponsesAccumulator(tb)
	chunks := make([]*ResponsesStreamChunk, len(events))
	for i, event := range events {
		chunks[i] = &ResponsesStreamChunk{ChunkIndex: i, StreamResponse: deepCopyResponsesStreamResponse(event)}
	}
	return acc.buildCompleteMessageFromResponsesStreamChunks(chunks)
}

func responsesItemEvent(eventType schemas.ResponsesStreamResponseType, item schemas.ResponsesMessage) *schemas.BifrostResponsesStreamResponse {
	return &schemas.BifrostResponsesStreamResponse{Type: eventType, ItemID: item.ID, Item: &item}
}

// TestBuildResponsesMessageKeepsToolsetName verifies that a computer-toolset
// member call keeps toolset_name. The Anthropic stream converter puts it on
// output_item.added; the argument deltas that follow make the accumulator keep
// that copy and take only the status from output_item.done, so a copy that
// drops the field loses it for good.
func TestBuildResponsesMessageKeepsToolsetName(t *testing.T) {
	call := func(status string) schemas.ResponsesMessage {
		return schemas.ResponsesMessage{
			ID:     schemas.Ptr("toolu_1"),
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			Status: schemas.Ptr(status),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:      schemas.Ptr("toolu_1"),
				Name:        schemas.Ptr("screenshot"),
				ToolsetName: schemas.Ptr("computer"),
				Arguments:   schemas.Ptr(""),
			},
		}
	}

	msgs := streamThroughResponsesAccumulator(t,
		responsesItemEvent(schemas.ResponsesStreamResponseTypeOutputItemAdded, call("in_progress")),
		&schemas.BifrostResponsesStreamResponse{
			Type:   schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			ItemID: schemas.Ptr("toolu_1"),
			Delta:  schemas.Ptr("{}"),
		},
		responsesItemEvent(schemas.ResponsesStreamResponseTypeOutputItemDone, call("completed")),
	)

	require.Len(t, msgs, 1)
	require.NotNil(t, msgs[0].ResponsesToolMessage)
	require.NotNil(t, msgs[0].ResponsesToolMessage.ToolsetName, "toolset_name was dropped")
	require.Equal(t, "computer", *msgs[0].ResponsesToolMessage.ToolsetName)
	require.Equal(t, "{}", *msgs[0].ResponsesToolMessage.Arguments)
}

// TestBuildResponsesMessageKeepsServerToolResults verifies the Anthropic
// server-tool payloads that only the provider-specific carriers can hold: the
// advisor result, the tool references a tool_search discovered, and the
// code-execution call details. The stream converter sends each on
// output_item.done, which the accumulator adopts through a deep copy.
func TestBuildResponsesMessageKeepsServerToolResults(t *testing.T) {
	advisor := &schemas.ResponsesAdvisorCall{
		ResultType: "advisor_result",
		Text:       schemas.Ptr("Decide what graceful means."),
	}
	toolSearch := &schemas.ResponsesToolSearchCall{ToolReferences: []string{"get_weather"}}
	codeExecution := &schemas.ResponsesCodeExecutionCall{
		ToolName:   "bash_code_execution",
		Input:      schemas.Ptr(`{"command":"pwd"}`),
		ResultType: "bash_code_execution_result",
		Stdout:     schemas.Ptr("/tmp\n"),
		ReturnCode: schemas.Ptr(0),
		Files:      []schemas.ResponsesCodeExecutionFileOutput{{FileID: "file_1"}},
	}
	item := func(id string, msgType schemas.ResponsesMessageType, status string, toolMessage schemas.ResponsesToolMessage) schemas.ResponsesMessage {
		toolMessage.CallID = schemas.Ptr(id)
		return schemas.ResponsesMessage{
			ID:                   schemas.Ptr(id),
			Type:                 schemas.Ptr(msgType),
			Status:               schemas.Ptr(status),
			ResponsesToolMessage: &toolMessage,
		}
	}

	msgs := streamThroughResponsesAccumulator(t,
		responsesItemEvent(schemas.ResponsesStreamResponseTypeOutputItemAdded, item("srvtoolu_adv", schemas.ResponsesMessageTypeAdvisorCall, "in_progress", schemas.ResponsesToolMessage{})),
		responsesItemEvent(schemas.ResponsesStreamResponseTypeOutputItemDone, item("srvtoolu_adv", schemas.ResponsesMessageTypeAdvisorCall, "completed", schemas.ResponsesToolMessage{ResponsesAdvisorCall: advisor})),
		responsesItemEvent(schemas.ResponsesStreamResponseTypeOutputItemAdded, item("srvtoolu_ts", schemas.ResponsesMessageTypeToolSearchCall, "in_progress", schemas.ResponsesToolMessage{})),
		responsesItemEvent(schemas.ResponsesStreamResponseTypeOutputItemDone, item("srvtoolu_ts", schemas.ResponsesMessageTypeToolSearchCall, "completed", schemas.ResponsesToolMessage{ResponsesToolSearchCall: toolSearch})),
		responsesItemEvent(schemas.ResponsesStreamResponseTypeOutputItemAdded, item("srvtoolu_ce", schemas.ResponsesMessageTypeCodeInterpreterCall, "in_progress", schemas.ResponsesToolMessage{
			ResponsesCodeExecutionCall: &schemas.ResponsesCodeExecutionCall{ToolName: "bash_code_execution"},
		})),
		responsesItemEvent(schemas.ResponsesStreamResponseTypeOutputItemDone, item("srvtoolu_ce", schemas.ResponsesMessageTypeCodeInterpreterCall, "completed", schemas.ResponsesToolMessage{ResponsesCodeExecutionCall: codeExecution})),
	)

	require.Len(t, msgs, 3)
	byID := map[string]*schemas.ResponsesToolMessage{}
	for _, msg := range msgs {
		require.NotNil(t, msg.ID)
		byID[*msg.ID] = msg.ResponsesToolMessage
	}
	require.Equal(t, advisor, byID["srvtoolu_adv"].ResponsesAdvisorCall)
	require.NotSame(t, advisor, byID["srvtoolu_adv"].ResponsesAdvisorCall)
	require.Equal(t, toolSearch, byID["srvtoolu_ts"].ResponsesToolSearchCall)
	require.NotSame(t, toolSearch, byID["srvtoolu_ts"].ResponsesToolSearchCall)
	require.Equal(t, codeExecution, byID["srvtoolu_ce"].ResponsesCodeExecutionCall)
	require.NotSame(t, codeExecution, byID["srvtoolu_ce"].ResponsesCodeExecutionCall)
}

// TestBuildResponsesMessageKeepsProviderNativeParts verifies that the raw
// Gemini parts the stream converter attaches to a reasoning item survive the
// accumulator, and that the accumulated item owns its bytes.
func TestBuildResponsesMessageKeepsProviderNativeParts(t *testing.T) {
	native := json.RawMessage(`[{"toolCall":{"id":"tc_1"},"thoughtSignature":"c2ln"}]`)
	item := func(status string) schemas.ResponsesMessage {
		return schemas.ResponsesMessage{
			ID:     schemas.Ptr("rs_0"),
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Status: schemas.Ptr(status),
			ResponsesReasoning: &schemas.ResponsesReasoning{
				Summary:          []schemas.ResponsesReasoningSummary{},
				EncryptedContent: schemas.Ptr("c2ln"),
			},
			ProviderNativeParts: native,
		}
	}

	msgs := streamThroughResponsesAccumulator(t,
		responsesItemEvent(schemas.ResponsesStreamResponseTypeOutputItemAdded, item("in_progress")),
		responsesItemEvent(schemas.ResponsesStreamResponseTypeOutputItemDone, item("completed")),
	)

	require.Len(t, msgs, 1)
	require.Equal(t, string(native), string(msgs[0].ProviderNativeParts))
	require.NotSame(t, &native[0], &msgs[0].ProviderNativeParts[0])
}

// TestBuildResponsesMessageKeepsNonTextContentBlocks verifies content blocks
// the stream converters emit whole on output_item.added/done: an Anthropic
// compaction summary with its cache_control, an Anthropic server-side fallback
// boundary, and Gemini's rendered search entry point.
func TestBuildResponsesMessageKeepsNonTextContentBlocks(t *testing.T) {
	blocks := map[string]schemas.ResponsesMessageContentBlock{
		"cmp_0": {
			Type:                                    schemas.ResponsesOutputMessageContentTypeCompaction,
			ResponsesOutputMessageContentCompaction: &schemas.ResponsesOutputMessageContentCompaction{Summary: "Earlier turns covered the schema."},
			CacheControl:                            &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, TTL: schemas.Ptr("1h")},
		},
		"fb_1": {
			Type: schemas.ResponsesOutputMessageContentTypeFallback,
			ResponsesOutputMessageContentFallback: &schemas.ResponsesOutputMessageContentFallback{
				FromModel:       "claude-fable-5",
				ToModel:         "claude-opus-4-8",
				TriggerType:     "refusal",
				TriggerCategory: schemas.Ptr("cyber"),
			},
		},
		"rc_2": {
			Type: schemas.ResponsesOutputMessageContentTypeRenderedContent,
			ResponsesOutputMessageContentRenderedContent: &schemas.ResponsesOutputMessageContentRenderedContent{RenderedContent: "<div>chips</div>"},
		},
	}
	var events []*schemas.BifrostResponsesStreamResponse
	for _, id := range []string{"cmp_0", "fb_1", "rc_2"} {
		item := schemas.ResponsesMessage{
			ID:      schemas.Ptr(id),
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Status:  schemas.Ptr("completed"),
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{blocks[id]}},
		}
		events = append(events,
			responsesItemEvent(schemas.ResponsesStreamResponseTypeOutputItemAdded, item),
			responsesItemEvent(schemas.ResponsesStreamResponseTypeOutputItemDone, item),
		)
	}

	msgs := streamThroughResponsesAccumulator(t, events...)

	require.Len(t, msgs, 3)
	for _, msg := range msgs {
		require.NotNil(t, msg.ID)
		require.NotNil(t, msg.Content)
		require.Len(t, msg.Content.ContentBlocks, 1)
		require.Equal(t, blocks[*msg.ID], msg.Content.ContentBlocks[0], "content block %s", *msg.ID)
	}
}

// TestDeepCopyResponsesMessageDoesNotShareWebSearchAction verifies the copy
// owns the web_search_call action's slices and pointers. The accumulator
// copies stream items so that no plugin observes another's writes; the action
// used to be copied one level deep and still shared its queries and sources.
func TestDeepCopyResponsesMessageDoesNotShareWebSearchAction(t *testing.T) {
	newAction := func() *schemas.ResponsesWebSearchToolCallAction {
		return &schemas.ResponsesWebSearchToolCallAction{
			Type:    "search",
			URL:     schemas.Ptr("https://example.com"),
			Query:   schemas.Ptr("bifrost"),
			Queries: []string{"bifrost"},
			Sources: []schemas.ResponsesWebSearchToolCallActionSearchSource{{
				Type:  "url",
				URL:   "https://example.com/a",
				Title: schemas.Ptr("A"),
			}},
			Pattern:      schemas.Ptr("bif.*"),
			ImageQueries: []string{"bridge"},
		}
	}
	original := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			Action: &schemas.ResponsesToolMessageActionStruct{ResponsesWebSearchToolCallAction: newAction()},
		},
	}

	copied := deepCopyResponsesMessage(original)

	action := original.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction
	*action.URL = "mutated"
	*action.Query = "mutated"
	action.Queries[0] = "mutated"
	action.Sources[0].URL = "mutated"
	*action.Sources[0].Title = "mutated"
	*action.Pattern = "mutated"
	action.ImageQueries[0] = "mutated"
	require.Equal(t, newAction(), copied.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction)
}
