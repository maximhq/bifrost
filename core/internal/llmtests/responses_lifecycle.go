package llmtests

import (
	"context"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunResponsesLifecycleTest exercises OpenAI Responses API lifecycle: create with store,
// retrieve, list input_items, delete. Cancel is only meaningful for background responses and is omitted.
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
		},
	}

	created, err := client.ResponsesRequest(bfCtx, createReq)
	if err != nil {
		t.Fatalf("create stored response: %v", err)
	}
	if created == nil || created.ID == nil || *created.ID == "" {
		t.Fatalf("expected non-empty response id")
	}
	rid := *created.ID

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
