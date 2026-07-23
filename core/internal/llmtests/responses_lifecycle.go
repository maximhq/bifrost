package llmtests

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunResponsesLifecycleTest exercises OpenAI Responses API lifecycle: create a background
// streaming response (stored), disconnect, retrieve, resume it via streaming retrieve
// (GET /v1/responses/{id}?stream=true&starting_after=N), list input_items, delete.
// Cancel is only meaningful for background responses and is omitted.
func RunResponsesLifecycleTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ResponsesLifecycle {
		return
	}
	if testConfig.Provider != schemas.OpenAI {
		t.Skip("responses lifecycle is only run for OpenAI provider")
	}

	model := testConfig.ChatModel
	if model == "" {
		model = "gpt-4o-mini"
	}

	bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	store := true
	createReq := &schemas.BifrostResponsesRequest{
		Provider: testConfig.Provider,
		Model:    model,
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("Reply with exactly: lifecycle-ok"),
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			Store: &store,
			// background=true (paired with the streaming create below) is what makes the
			// response replayable via streaming retrieve (GET /v1/responses/{id}?stream=true).
			Background: schemas.Ptr(true),
		},
	}

	// Start a background streaming create with its OWN cancellable context. Read just
	// enough to learn the response id and the latest event sequence number, then
	// "disconnect" — the background job keeps generating server-side, so we can resume
	// it via streaming retrieve. Streaming requests must not share a BifrostContext
	// (streaming state keys leak across calls), so each stream gets a fresh one.
	createGoCtx, cancelCreateStream := context.WithCancel(ctx)
	createBfCtx := schemas.NewBifrostContext(createGoCtx, schemas.NoDeadline)
	createStream, err := client.ResponsesStreamRequest(createBfCtx, createReq)
	if err != nil {
		cancelCreateStream()
		t.Fatalf("create streaming response: %v", err)
	}
	if createStream == nil {
		cancelCreateStream()
		t.Fatalf("create streaming response: nil channel")
	}

	var rid string
	var lastSeq int
	createCtx, cancelCreate := context.WithTimeout(ctx, 60*time.Second)
	defer cancelCreate()
createLoop:
	for {
		select {
		case chunk, ok := <-createStream:
			if !ok {
				break createLoop
			}
			if chunk == nil {
				continue
			}
			if chunk.BifrostError != nil {
				cancelCreateStream()
				t.Fatalf("create stream returned error chunk: %v", chunk.BifrostError)
			}
			sr := chunk.BifrostResponsesStreamResponse
			if sr == nil {
				continue
			}
			if sr.SequenceNumber > lastSeq {
				lastSeq = sr.SequenceNumber
			}
			if sr.Response != nil && sr.Response.ID != nil && *sr.Response.ID != "" {
				rid = *sr.Response.ID
			}
			if rid != "" {
				break createLoop
			}
		case <-createCtx.Done():
			cancelCreateStream()
			t.Fatalf("timed out reading create stream (rid=%q)", rid)
		}
	}
	// Disconnect the create stream (simulating a client drop); the background job keeps
	// running. Drain any buffered chunks so the producer goroutine isn't left blocked.
	cancelCreateStream()
	go func() {
		for range createStream {
		}
	}()

	if rid == "" {
		t.Fatalf("expected a response id from the create stream")
	}
	t.Cleanup(func() {
		_, _ = client.ResponsesDeleteRequest(bfCtx, &schemas.BifrostResponsesDeleteRequest{
			Provider:   testConfig.Provider,
			ResponseID: rid,
		})
	})

	retrieved, err := client.ResponsesRetrieveRequest(bfCtx, &schemas.BifrostResponsesRetrieveRequest{
		Provider:   testConfig.Provider,
		ResponseID: rid,
	})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if retrieved == nil || retrieved.ID == nil || *retrieved.ID != rid {
		t.Fatalf("retrieve id mismatch: got %#v want id %s", retrieved, rid)
	}

	// Streaming retrieve: resume the background response from where we disconnected,
	// with its own fresh context, delivering the remaining events through the terminal one.
	streamBfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	streamChan, err := client.ResponsesRetrieveStreamRequest(streamBfCtx, &schemas.BifrostResponsesRetrieveRequest{
		Provider:      testConfig.Provider,
		ResponseID:    rid,
		Stream:        schemas.Ptr(true),
		StartingAfter: schemas.Ptr(lastSeq),
	})
	if err != nil {
		t.Fatalf("retrieve stream: %v", err)
	}
	if streamChan == nil {
		t.Fatalf("retrieve stream: nil channel")
	}

	streamCtx, cancelStream := context.WithTimeout(ctx, 60*time.Second)
	defer cancelStream()

	var chunkCount int
	var sawTerminal bool
	var streamedID string
readLoop:
	for {
		select {
		case chunk, ok := <-streamChan:
			if !ok {
				break readLoop
			}
			if chunk == nil {
				continue
			}
			if chunk.BifrostError != nil {
				t.Fatalf("retrieve stream returned error chunk: %v", chunk.BifrostError)
			}
			sr := chunk.BifrostResponsesStreamResponse
			if sr == nil {
				continue
			}
			chunkCount++
			if sr.Type == schemas.ResponsesStreamResponseTypeCompleted || sr.Type == schemas.ResponsesStreamResponseTypeIncomplete {
				sawTerminal = true
				if sr.Response != nil && sr.Response.ID != nil {
					streamedID = *sr.Response.ID
				}
			}
		case <-streamCtx.Done():
			t.Fatalf("retrieve stream: timeout after %d chunks", chunkCount)
		}
	}

	if chunkCount == 0 {
		t.Fatalf("retrieve stream: expected at least one chunk, got none")
	}
	if !sawTerminal {
		t.Fatalf("retrieve stream: expected a terminal (completed/incomplete) event, got %d chunks", chunkCount)
	}
	if streamedID != "" && streamedID != rid {
		t.Fatalf("retrieve stream id mismatch: got %s want %s", streamedID, rid)
	}
	t.Logf("✅ Streaming retrieve replayed %d chunks for %s", chunkCount, rid)

	items, err := client.ResponsesInputItemsRequest(bfCtx, &schemas.BifrostResponsesInputItemsRequest{
		Provider:   testConfig.Provider,
		ResponseID: rid,
		Limit:      schemas.Ptr(20),
	})
	if err != nil {
		t.Fatalf("input_items: %v", err)
	}
	if items == nil || items.Object == "" {
		t.Fatalf("expected input_items list payload")
	}

	deleted, err := client.ResponsesDeleteRequest(bfCtx, &schemas.BifrostResponsesDeleteRequest{
		Provider:   testConfig.Provider,
		ResponseID: rid,
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted == nil || !deleted.Deleted {
		t.Fatalf("expected deleted response with deleted=true, got %#v", deleted)
	}
}
