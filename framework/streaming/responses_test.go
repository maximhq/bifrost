package streaming

import (
	"fmt"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

func testToolParamType(t *testing.T, value interface{}) interface{} {
	t.Helper()

	switch v := value.(type) {
	case map[string]interface{}:
		return v["type"]
	case *schemas.OrderedMap:
		got, _ := v.Get("type")
		return got
	case schemas.OrderedMap:
		got, _ := v.Get("type")
		return got
	default:
		t.Fatalf("unexpected schema value type %T", value)
		return nil
	}
}

func testResponsesAccumulator(tb testing.TB) *Accumulator {
	tb.Helper()
	acc := NewAccumulator(nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
	tb.Cleanup(acc.Cleanup)
	return acc
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

func TestDeepCopyResponsesStreamResponsePreservesToolSearchFields(t *testing.T) {
	toolSearchType := schemas.ResponsesMessageTypeToolSearchOutput
	const wantNamespace = "mcp__codexself"
	const wantExecution = "client"
	const wantFunction = "codex_reply"
	namespace := wantNamespace
	execution := wantExecution
	functionName := wantFunction
	paramDesc := "reply payload"
	params := &schemas.ToolFunctionParameters{
		Type:        "object",
		Description: &paramDesc,
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.Pair{Key: "message", Value: map[string]interface{}{"type": "string"}},
		),
	}

	original := &schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeOutputItemDone,
		Item: &schemas.ResponsesMessage{
			Type: &toolSearchType,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Namespace: &namespace,
				Execution: &execution,
				Tools: []schemas.ResponsesTool{
					{
						Type: schemas.ResponsesToolType("namespace"),
						Name: schemas.Ptr(namespace),
						ResponsesToolNamespace: &schemas.ResponsesToolNamespace{
							Tools: []schemas.ResponsesTool{
								{
									Type: schemas.ResponsesToolType("function"),
									Name: schemas.Ptr(functionName),
									ResponsesToolFunction: &schemas.ResponsesToolFunction{
										Parameters: params,
									},
								},
							},
						},
					},
				},
			},
		},
	}

	copied := deepCopyResponsesStreamResponse(original)
	if copied == nil || copied.Item == nil || copied.Item.ResponsesToolMessage == nil {
		t.Fatal("expected non-nil deep copy with tool message")
	}
	if copied.Item.ResponsesToolMessage.Namespace == nil || *copied.Item.ResponsesToolMessage.Namespace != wantNamespace {
		t.Fatalf("Namespace: want %q, got %#v", wantNamespace, copied.Item.ResponsesToolMessage.Namespace)
	}
	if copied.Item.ResponsesToolMessage.Execution == nil || *copied.Item.ResponsesToolMessage.Execution != wantExecution {
		t.Fatalf("Execution: want %q, got %#v", wantExecution, copied.Item.ResponsesToolMessage.Execution)
	}
	if len(copied.Item.ResponsesToolMessage.Tools) != 1 || copied.Item.ResponsesToolMessage.Tools[0].Type != schemas.ResponsesToolType("namespace") {
		t.Fatalf("Tools: unexpected copy %#v", copied.Item.ResponsesToolMessage.Tools)
	}
	if *copied.Item.ResponsesToolMessage.Tools[0].ResponsesToolNamespace.Tools[0].Name != wantFunction {
		t.Fatalf("Nested tool name: want %q, got %#v", wantFunction, copied.Item.ResponsesToolMessage.Tools[0].ResponsesToolNamespace.Tools[0].Name)
	}
	if got := testToolParamType(t, copied.Item.ResponsesToolMessage.Tools[0].ResponsesToolNamespace.Tools[0].ResponsesToolFunction.Parameters.Properties.ToMap()["message"]); got != "string" {
		t.Fatalf("Nested tool params: want string type, got %#v", got)
	}

	// Mutate the original after copying; the copy must not observe any of it.
	*original.Item.ResponsesToolMessage.Namespace = "mutated-namespace"
	*original.Item.ResponsesToolMessage.Execution = "server"
	original.Item.ResponsesToolMessage.Tools[0].Type = schemas.ResponsesToolType("mutated")
	*original.Item.ResponsesToolMessage.Tools[0].ResponsesToolNamespace.Tools[0].Name = "mutated-function"
	original.Item.ResponsesToolMessage.Tools[0].ResponsesToolNamespace.Tools[0].ResponsesToolFunction.Parameters.Properties.Set("message", map[string]interface{}{"type": "number"})

	if *copied.Item.ResponsesToolMessage.Namespace != wantNamespace {
		t.Fatalf("Namespace aliased original: got %q", *copied.Item.ResponsesToolMessage.Namespace)
	}
	if *copied.Item.ResponsesToolMessage.Execution != wantExecution {
		t.Fatalf("Execution aliased original: got %q", *copied.Item.ResponsesToolMessage.Execution)
	}
	if copied.Item.ResponsesToolMessage.Tools[0].Type != schemas.ResponsesToolType("namespace") {
		t.Fatalf("Tools aliased original: got %#v", copied.Item.ResponsesToolMessage.Tools)
	}
	if *copied.Item.ResponsesToolMessage.Tools[0].ResponsesToolNamespace.Tools[0].Name != wantFunction {
		t.Fatalf("Nested tool name aliased original: got %#v", copied.Item.ResponsesToolMessage.Tools[0].ResponsesToolNamespace.Tools[0].Name)
	}
	if got := testToolParamType(t, copied.Item.ResponsesToolMessage.Tools[0].ResponsesToolNamespace.Tools[0].ResponsesToolFunction.Parameters.Properties.ToMap()["message"]); got != "string" {
		t.Fatalf("Nested tool params aliased original: got %#v", got)
	}
}
