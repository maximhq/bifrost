package streaming

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

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
