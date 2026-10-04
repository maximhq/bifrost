package logging

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestMergeVirtualKeyMetadata covers the pure merge rules: nothing on the context leaves the map
// untouched, the key's values win over caller-supplied ones, other caller keys survive, and a
// load balancer key is never written.
func TestMergeVirtualKeyMetadata(t *testing.T) {
	if got := mergeVirtualKeyMetadata(nil, nil); got != nil {
		t.Fatalf("expected nil metadata for nil ctx, got %#v", got)
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if got := mergeVirtualKeyMetadata(nil, ctx); got != nil {
		t.Fatalf("expected nil metadata when the context carries no key metadata, got %#v", got)
	}
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyMetadata, map[string]string{
		"cost_center": "cc-42",
		schemas.LoadBalancerMetadataPrefix + "decision": "forged",
	})
	got := mergeVirtualKeyMetadata(map[string]interface{}{"cost_center": "spoofed", "tenant": "acme"}, ctx)
	if got["cost_center"] != "cc-42" {
		t.Fatalf("expected the virtual key's value to win over the caller's, got %#v", got["cost_center"])
	}
	if got["tenant"] != "acme" {
		t.Fatalf("expected caller metadata to be preserved, got %#v", got["tenant"])
	}
	if _, leaked := got[schemas.LoadBalancerMetadataPrefix+"decision"]; leaked {
		t.Fatalf("expected a load balancer key to be skipped, got %#v", got)
	}
	if got := mergeVirtualKeyMetadata(nil, ctx); got == nil || got["cost_center"] != "cc-42" {
		t.Fatalf("expected a fresh map when metadata was nil, got %#v", got)
	}
}

// TestPostLLMHookSnapshotsVirtualKeyMetadata drives the pending-entry path end to end. The key is
// stamped either before PreLLMHook (the usual HTTP path) or between the hooks (the passthrough
// path); either way the persisted row carries the key's metadata, and an x-bf-lh-* header or
// x-bf-dim-* dimension spelling the same key cannot override it.
func TestPostLLMHookSnapshotsVirtualKeyMetadata(t *testing.T) {
	for _, tc := range []struct {
		name           string
		stampBeforePre bool
		requestID      string
	}{
		{name: "stamped before PreLLMHook", stampBeforePre: true, requestID: "req-vk-metadata-pre"},
		{name: "stamped between hooks", stampBeforePre: false, requestID: "req-vk-metadata-post"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
			if err != nil {
				t.Fatalf("Init() error = %v", err)
			}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyRequestID, tc.requestID)
			ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{
				"x-bf-lh-tenant":      "acme",
				"x-bf-lh-cost_center": "spoofed-by-header",
			})
			ctx.SetValue(schemas.BifrostContextKeyDimensions, map[string]string{"owner": "spoofed-by-dimension"})
			vkMetadata := map[string]string{"cost_center": "cc-42", "owner": "a@example.com"}
			if tc.stampBeforePre {
				ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyMetadata, vkMetadata)
			}

			req := &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o", Params: &schemas.ChatParameters{}},
			}
			if _, _, err = plugin.PreLLMHook(ctx, req); err != nil {
				t.Fatalf("PreLLMHook() error = %v", err)
			}
			if !tc.stampBeforePre {
				ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyMetadata, vkMetadata)
			}

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

			logEntry, err := store.FindByID(context.Background(), tc.requestID)
			if err != nil {
				t.Fatalf("FindByID() error = %v", err)
			}
			for key, want := range map[string]string{"cost_center": "cc-42", "owner": "a@example.com", "tenant": "acme"} {
				if got := logEntry.MetadataParsed[key]; got != want {
					t.Fatalf("metadata[%q] = %#v, want %q (row metadata %#v)", key, got, want, logEntry.MetadataParsed)
				}
			}
		})
	}
}
