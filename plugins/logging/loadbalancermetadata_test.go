package logging

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestMergeLoadBalancerMetadata covers the pure merge rules: nothing on the context leaves the map
// untouched, unprefixed keys are dropped, and a prefixed key overrides a caller's value.
func TestMergeLoadBalancerMetadata(t *testing.T) {
	prefix := schemas.LoadBalancerMetadataPrefix
	if got := mergeLoadBalancerMetadata(nil, nil); got != nil {
		t.Fatalf("expected nil metadata for nil ctx, got %#v", got)
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if got := mergeLoadBalancerMetadata(nil, ctx); got != nil {
		t.Fatalf("expected nil metadata when the context carries no attempt, got %#v", got)
	}
	ctx.SetValue(schemas.BifrostContextKeyLoadBalancerAttempt, map[string]string{
		prefix + "decision": "pinned_rerouted",
		prefix + "rerouted": "true",
		"decision":          "leaked",
	})
	got := mergeLoadBalancerMetadata(map[string]interface{}{prefix + "decision": "spoofed", "tenant": "acme"}, ctx)
	if got[prefix+"decision"] != "pinned_rerouted" {
		t.Fatalf("expected the load balancer's decision to win over the caller's, got %#v", got[prefix+"decision"])
	}
	if got[prefix+"rerouted"] != "true" {
		t.Fatalf("expected rerouted flag to be merged, got %#v", got[prefix+"rerouted"])
	}
	if _, leaked := got["decision"]; leaked {
		t.Fatalf("expected an unprefixed key to be dropped, got %#v", got["decision"])
	}
	if got["tenant"] != "acme" {
		t.Fatalf("expected caller metadata to be preserved, got %#v", got["tenant"])
	}
	if got := mergeLoadBalancerMetadata(nil, ctx); got == nil || got[prefix+"decision"] != "pinned_rerouted" {
		t.Fatalf("expected a fresh map when metadata was nil, got %#v", got)
	}
}

// TestPostLLMHookMergesLoadBalancerMetadata drives the pending-entry path: the decision stamped on
// the context reaches the persisted row, and a caller header spelling the same key does not.
func TestPostLLMHookMergesLoadBalancerMetadata(t *testing.T) {
	prefix := schemas.LoadBalancerMetadataPrefix
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-lb-metadata")
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{
		"x-bf-lh-tenant":                 "acme",
		"x-bf-lh-" + prefix + "decision": "spoofed",
	})

	req := &schemas.BifrostRequest{
		RequestType: schemas.ResponsesStreamRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: schemas.Bedrock,
			Model:    "us.anthropic.claude-opus-4-7",
			Params:   &schemas.ResponsesParameters{},
		},
	}
	if _, _, err = plugin.PreLLMHook(ctx, req); err != nil {
		t.Fatalf("PreLLMHook() error = %v", err)
	}
	// Key selection runs after PreLLMHook, so the decision lands on the context between the hooks.
	ctx.SetValue(schemas.BifrostContextKeyLoadBalancerAttempt, map[string]string{
		prefix + "decision":     "pinned_kept_failed",
		prefix + "key_decision": "weighted",
	})

	statusCode := 500
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		Error:          &schemas.ErrorField{Message: "provider failed"},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType:            schemas.ResponsesStreamRequest,
			Provider:               schemas.Bedrock,
			OriginalModelRequested: "us.anthropic.claude-opus-4-7",
			ResolvedModelUsed:      "us.anthropic.claude-opus-4-7",
		},
	}
	if _, _, err = plugin.PostLLMHook(ctx, nil, bifrostErr); err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	logEntry, err := store.FindByID(context.Background(), "req-lb-metadata")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if logEntry.MetadataParsed == nil {
		t.Fatalf("expected metadata to be persisted")
	}
	if got := logEntry.MetadataParsed[prefix+"decision"]; got != "pinned_kept_failed" {
		t.Fatalf("expected the load balancer decision on the row, got %#v", got)
	}
	if got := logEntry.MetadataParsed[prefix+"key_decision"]; got != "weighted" {
		t.Fatalf("expected the key decision on the row, got %#v", got)
	}
	if got := logEntry.MetadataParsed["tenant"]; got != "acme" {
		t.Fatalf("expected caller metadata to survive the merge, got %#v", got)
	}
}

// TestPostLLMHookNoPendingMergesLoadBalancerMetadata drives the minimal error entry written when
// no pending log data exists; a failed attempt must still carry its routing decision.
func TestPostLLMHookNoPendingMergesLoadBalancerMetadata(t *testing.T) {
	prefix := schemas.LoadBalancerMetadataPrefix
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-lb-metadata-no-pending")
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{"x-bf-lh-tenant": "acme"})
	ctx.SetValue(schemas.BifrostContextKeyLoadBalancerAttempt, map[string]string{
		prefix + "decision": "pinned",
		prefix + "rerouted": "false",
	})

	statusCode := 500
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		Error:          &schemas.ErrorField{Message: "provider failed"},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType:            schemas.ChatCompletionRequest,
			Provider:               schemas.OpenAI,
			OriginalModelRequested: "gpt-4o",
			ResolvedModelUsed:      "gpt-4o",
		},
	}
	if _, _, err = plugin.PostLLMHook(ctx, nil, bifrostErr); err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	logEntry, err := store.FindByID(context.Background(), "req-lb-metadata-no-pending")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if logEntry.Status != "error" {
		t.Fatalf("expected error status, got %q", logEntry.Status)
	}
	if got := logEntry.MetadataParsed[prefix+"decision"]; got != "pinned" {
		t.Fatalf("expected the load balancer decision on the minimal error row, got %#v", got)
	}
	if got := logEntry.MetadataParsed[prefix+"rerouted"]; got != "false" {
		t.Fatalf("expected the rerouted flag on the minimal error row, got %#v", got)
	}
	if got := logEntry.MetadataParsed["tenant"]; got != "acme" {
		t.Fatalf("expected caller metadata to survive the merge, got %#v", got)
	}
}
